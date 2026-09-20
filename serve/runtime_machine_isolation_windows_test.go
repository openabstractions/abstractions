package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	configclient "github.com/openabstractions/abstraction-config/go/client"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
)

// An isolated runtime reads no machine-wide configuration. %ProgramData% points
// at a sentinel directory holding a machine file. A machine read checks that
// file's owner first and, finding this unprivileged account, prints "ignoring
// <path>" to stderr; a privileged account would merge its value instead. The
// runtime runs as a child process, so its stderr is its own: neither the path
// nor the value may appear.
func TestIsolatedRuntimeReadsNoMachineConfiguration(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on this transport")
	}
	const marker = "SENTINEL-MACHINE-store-should-never-be-read"
	sentinel := t.TempDir()
	machine := filepath.Join(sentinel, config.Name, "config.json")
	if err := os.MkdirAll(filepath.Dir(machine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(machine, []byte(`{"store":"`+marker+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := t.TempDir()
	state, err := os.MkdirTemp("", "oa-machine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	for dir, file := range map[string][2]string{
		"credentials": {credentialsBackendFile, "file-0600\n"},
		"inference":   {inferenceHostsFile, `{"local": []}`},
	} {
		if err := os.MkdirAll(filepath.Join(state, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, dir, file[0]), []byte(file[1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	name := fmt.Sprintf("machine-%d", os.Getpid())
	options, err := parseRuntime([]string{"--isolated", name, "--state-dir", state, "--without-jobs"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })

	child := exec.Command(os.Args[0], "-test.run=^TestSupervisedProcessHelper$")
	child.Env = append(helperEnv("runtime", []string{"serve", "runtime", "--supervised", "--isolated", name, "--state-dir", state, "--without-jobs"}),
		"ProgramData="+sentinel, "APPDATA="+profile, "LOCALAPPDATA="+profile)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			if lines.Text() == "READY 1" {
				ready <- true
			}
		}
		io.Copy(io.Discard, stdout)
		close(ready)
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		stdin.Close()
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		select {
		case <-done:
		case <-time.After(runtimeWait):
			child.Process.Kill()
			<-done
			t.Error("isolated runtime did not stop on stdin EOF")
		}
	}
	defer stop()
	select {
	case ok := <-ready:
		if !ok {
			stop()
			t.Fatalf("isolated runtime exited before READY:\n%s", stderr.String())
		}
	case <-time.After(runtimeWait):
		t.Fatalf("isolated runtime not ready within %v:\n%s", runtimeWait, stderr.String())
	}
	endpoint, err := bootstrap.Endpoint(name + "-config")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := configclient.New(endpoint).ReadContext(ctx)
	if err != nil {
		t.Fatalf("isolated config read: %v\n%s", err, stderr.String())
	}
	stop()
	if snapshot.Store == marker || snapshot.Origins.Store.Rung == config.Machine {
		t.Fatalf("the isolated runtime answered with the machine rung: %+v", snapshot)
	}
	if strings.Contains(strings.ToLower(stderr.String()), strings.ToLower(sentinel)) {
		t.Fatalf("the isolated runtime read the sentinel machine directory:\n%s", stderr.String())
	}
}
