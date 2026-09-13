package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// The helper runs the real CLI entry point in a separate executable process.
func TestSupervisedProcessHelper(t *testing.T) {
	role := os.Getenv("OA_SUPERVISED_HELPER")
	if role == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("OA_SUPERVISED_ARGS")), &args); err != nil {
		panic(err)
	}
	if role == "runtime" {
		os.Args = append([]string{os.Args[0]}, args...)
		main()
		fmt.Println("EXIT")
		os.Exit(0)
	}
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSupervisedProcessHelper$")
	child.Env = helperEnv("runtime", args)
	child.Stdin = r
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		panic(err)
	}
	r.Close()
	fmt.Printf("CHILD %d\n", child.Process.Pid)
	err = child.Wait()
	runtime.KeepAlive(w) // Only this parent owns the write end throughout the child's lifetime.
	w.Close()
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
func helperEnv(role string, args []string) []string {
	b, _ := json.Marshal(args)
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "OA_SUPERVISED_") {
			env = append(env, v)
		}
	}
	return append(env, "OA_SUPERVISED_HELPER="+role, "OA_SUPERVISED_ARGS="+string(b))
}
func isolatedRuntime(t *testing.T) (runtimeFlags, []string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "oa-supervised-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	endpoint := func(s string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-supervised-%d-%s`, time.Now().UnixNano(), s)
		}
		return filepath.Join(dir, s)
	}
	o := runtimeFlags{endpoint: endpoint("r"), logEndpoint: endpoint("l"), configEndpoint: endpoint("c"), out: filepath.Join(dir, "records"), jobEndpoint: endpoint("j"), stateDir: filepath.Join(dir, "private")}
	args := []string{"serve", "runtime", "--supervised", "--endpoint", o.endpoint, "--log-endpoint", o.logEndpoint, "--config-endpoint", o.configEndpoint, "--out", o.out, "--jobs-endpoint", o.jobEndpoint, "--state-dir", o.stateDir}
	return o, args
}
func expectLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	select {
	case got := <-lines:
		if got != want {
			t.Fatalf("output %q, want %q", got, want)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("waiting for %q", want)
	}
}
func processLines(t *testing.T, c *exec.Cmd) <-chan string {
	t.Helper()
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		s := bufio.NewScanner(out)
		for s.Scan() {
			lines <- s.Text()
		}
	}()
	return lines
}
func assertListenersReleased(t *testing.T, o runtimeFlags) {
	t.Helper()
	sink, err := logging.OpenFileSink(o.out)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	h, err := host.Listen(host.Options{Endpoint: o.endpoint, LogEndpoint: o.logEndpoint, ConfigEndpoint: o.configEndpoint, Sink: sink, JobRoot: filepath.Join(o.stateDir, "jobs"), ManagedJobs: true, JobEndpoint: o.jobEndpoint, JobExecutor: downloadserve.HTTPExecution{}})
	if err != nil {
		t.Fatalf("listeners not released: %v", err)
	}
	h.Close()
}
func TestSupervisedParentClose(t *testing.T) {
	o, args := isolatedRuntime(t)
	c := exec.Command(os.Args[0], "-test.run=^TestSupervisedProcessHelper$")
	c.Env = helperEnv("runtime", args)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	defer r.Close()
	c.Stdin = r
	var stderr bytes.Buffer
	c.Stderr = &stderr
	lines := processLines(t, c)
	if err = c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Process.Kill()
	r.Close()
	expectLine(t, lines, "READY 1")
	w.Close()
	expectLine(t, lines, "EXIT")
	if err = c.Wait(); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	assertListenersReleased(t, o)
}
func TestSupervisedParentDeath(t *testing.T) {
	o, args := isolatedRuntime(t)
	c := exec.Command(os.Args[0], "-test.run=^TestSupervisedProcessHelper$")
	c.Env = helperEnv("owner", args)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	lines := processLines(t, c)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Process.Kill()
	var childPID int
	select {
	case line := <-lines:
		if _, err := fmt.Sscanf(line, "CHILD %d", &childPID); err != nil {
			t.Fatal(line)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("owner startup")
	}
	child, err := os.FindProcess(childPID)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Kill()
	defer child.Release()
	expectLine(t, lines, "READY 1")
	if err = c.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Wait only after the child's inherited output acknowledges its cleaned-up exit.
	expectLine(t, lines, "EXIT")
	_ = c.Wait()
	assertListenersReleased(t, o)
}
func TestSupervisedFailedStartHasNoReadiness(t *testing.T) {
	o, args := isolatedRuntime(t)
	// Force the final resolver initialization to fail after provider setup.
	sink, err := logging.OpenFileSink(o.out)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	h, err := host.Listen(host.Options{Endpoint: o.endpoint, LogEndpoint: o.logEndpoint + "-held", ConfigEndpoint: o.configEndpoint + "-held", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	c := exec.Command(os.Args[0], "-test.run=^TestSupervisedProcessHelper$")
	c.Env = helperEnv("runtime", args)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	c.Stdin = r
	var out bytes.Buffer
	c.Stdout = &out
	if err := c.Run(); err == nil {
		t.Fatal("startup succeeded with occupied endpoints")
	}
	if out.Len() != 0 {
		t.Fatalf("failed startup acknowledged: %q", out.String())
	}
	h.Close()
	assertListenersReleased(t, o)
}
func TestSupervisedPipeRequiredBeforeFilesystem(t *testing.T) {
	o, _ := isolatedRuntime(t)
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	o.out = filepath.Join(t.TempDir(), "absent", "records")
	if err = superviseRuntime(context.Background(), o, input, io.Discard); err == nil {
		t.Fatal("accepted file")
	}
	if _, err = os.Stat(filepath.Dir(o.out)); !os.IsNotExist(err) {
		t.Fatalf("created storage: %v", err)
	}
}

type refusedReady struct{}

func (refusedReady) Write([]byte) (int, error) { return 0, errors.New("ready closed") }
func TestSupervisedReadinessFailureCleansListeners(t *testing.T) {
	o, _ := isolatedRuntime(t)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if err := superviseRuntime(context.Background(), o, r, refusedReady{}); err == nil {
		t.Fatal("lost readiness error")
	}
	assertListenersReleased(t, o)
}
func TestSupervisedCancellationDoesNotWaitForStdin(t *testing.T) {
	o, _ := isolatedRuntime(t)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyR, readyW := io.Pipe()
	defer readyR.Close()
	defer readyW.Close()
	done := make(chan error, 1)
	go func() { done <- superviseRuntime(ctx, o, r, readyW) }()
	line, err := bufio.NewReader(readyR).ReadString('\n')
	if err != nil || line != "READY 1\n" {
		t.Fatal(line, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for parent stdin")
	}
	assertListenersReleased(t, o)
}

type shortReady struct{}

func (shortReady) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestSupervisedShortReadinessCleansListeners(t *testing.T) {
	o, _ := isolatedRuntime(t)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if err := superviseRuntime(context.Background(), o, r, shortReady{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short readiness: %v", err)
	}
	assertListenersReleased(t, o)
}
