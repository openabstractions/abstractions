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
	"sync/atomic"
	"testing"
	"time"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// endpointSeq and the process id make a test endpoint unique on the machine.
// Windows pipe names are machine-wide, and this host's clock advances in
// steps of about 300 microseconds: names built from time.Now().UnixNano()
// repeated within one test binary and across concurrent test binaries, and
// the runtime holding the second copy failed to start.
var endpointSeq atomic.Int64

func testPipe(prefix, service string) string {
	return listen.Endpoint(fmt.Sprintf("%s-%d-%d-%s", prefix, os.Getpid(), endpointSeq.Add(1), service))
}

// shortSocketDir keeps Unix socket paths below macOS's sun_path limit. Each
// caller owns and removes its directory; no machine endpoint is reused.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "oa-s-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// runtimeWait bounds every wait on an in-process runtime or fixture.
const runtimeWait = 15 * time.Second

// awaitReadiness returns the runtime's first line, the error the runtime
// returned before writing one, or a timeout. A runtime that cannot start never
// writes its readiness line, and a bare read of it waits for the test binary's
// own timeout.
func awaitReadiness(ready io.Reader, done <-chan error, wait time.Duration) (string, error) {
	line := make(chan string, 1)
	go func() { l, _ := bufio.NewReader(ready).ReadString('\n'); line <- l }()
	select {
	case l := <-line:
		return l, nil
	case err := <-done:
		return "", fmt.Errorf("runtime returned before readiness: %w", err)
	case <-time.After(wait):
		return "", fmt.Errorf("no readiness within %v", wait)
	}
}

// awaitStopped waits a bounded time for a cancelled runtime or fixture to
// return and names the one that did not.
func awaitStopped(t *testing.T, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(runtimeWait):
		t.Errorf("%s did not return within %v of cancellation", what, runtimeWait)
		return nil
	}
}

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

// isolatedRuntimeOptions configures the composition isolatedRuntime builds.
type isolatedRuntimeOptions struct {
	// ProductHosts composes the runtime with defaultDeclarationEnv's real
	// probe of this machine's installed products, the way a person's
	// `openabstractions serve runtime --isolated` does, instead of this test
	// binary's package-wide empty-fixture stub (runtime_inference_declared_test.go
	// init). A live test that expects the hosts a person would see opts in;
	// every other test stays explicit about composing with none.
	ProductHosts bool
}

// isolatedRuntime defaults to no product hosts at all: see isolatedRuntimeOptions.
func isolatedRuntime(t *testing.T, opts ...isolatedRuntimeOptions) (runtimeFlags, []string) {
	t.Helper()
	var options isolatedRuntimeOptions
	if len(opts) > 0 {
		options = opts[0]
	}
	if options.ProductHosts {
		saved := declarationEnv
		declarationEnv = defaultDeclarationEnv
		t.Cleanup(func() { declarationEnv = saved })
	}
	base := ""
	if runtime.GOOS == "darwin" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "oa-supervised-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	endpoint := func(s string) string {
		if runtime.GOOS == "windows" {
			return testPipe("oa-supervised", s)
		}
		return filepath.Join(dir, s)
	}
	o := runtimeFlags{endpoint: endpoint("r"), logEndpoint: endpoint("l"), configEndpoint: endpoint("c"), out: filepath.Join(dir, "records"), jobEndpoint: endpoint("j"), stateDir: filepath.Join(dir, "private")}
	args := []string{"serve", "runtime", "--supervised", "--endpoint", o.endpoint, "--log-endpoint", o.logEndpoint, "--config-endpoint", o.configEndpoint, "--out", o.out, "--jobs-endpoint", o.jobEndpoint, "--state-dir", o.stateDir}
	return o, args
}

// Default isolatedRuntime composes with this test binary's empty-fixture
// declarationEnv (runtime_inference_declared_test.go init): GOOS "linux"
// regardless of the machine running the tests. ProductHosts rebinds
// declarationEnv to defaultDeclarationEnv, which reports this machine's own
// GOOS, for the call's duration; a t.Cleanup it registers restores the stub
// so every other test keeps composing with no product hosts.
func TestIsolatedRuntimeProductHostsRestoresTheRealProbe(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the stub and the real probe would both report linux here")
	}
	if got := declarationEnv(nil).GOOS; got != "linux" {
		t.Fatalf("isolatedRuntime's default composition probed GOOS %q, want the test binary's stub (linux)", got)
	}
	isolatedRuntime(t, isolatedRuntimeOptions{ProductHosts: true})
	if got := declarationEnv(nil).GOOS; got != runtime.GOOS {
		t.Fatalf("ProductHosts probed GOOS %q, want this machine's own %q", got, runtime.GOOS)
	}
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
	if runtime.GOOS == "darwin" {
		for _, endpoint := range []string{o.endpoint, o.logEndpoint, o.configEndpoint, o.jobEndpoint} {
			l, err := listen.Listen(endpoint)
			if err != nil {
				t.Fatalf("listener %s not released: %v", endpoint, err)
			}
			l.Close()
		}
		return
	}
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

// The test's Unix fixture cannot prove Program on macOS. The installed
// runtime uses XPC; here startup must refuse before acknowledging readiness
// and must release every endpoint it briefly opened.
func assertMacSupervisedProofRefusal(t *testing.T) {
	t.Helper()
	o, _ := isolatedRuntime(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var ready bytes.Buffer
	err = superviseRuntime(context.Background(), o, r, &ready)
	if !errors.Is(err, identity.ErrNotProven) || ready.Len() != 0 {
		t.Fatalf("Unix Program startup: err %v, readiness %q", err, ready.String())
	}
	assertListenersReleased(t, o)
}

func TestMacUnixSupervisedStartupRefusesProgramProof(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Unix transport contract")
	}
	assertMacSupervisedProofRefusal(t)
}
func TestSupervisedParentClose(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("parent-close readiness requires Program proof; Unix refusal is checked separately")
	}
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
	if runtime.GOOS == "darwin" {
		t.Skip("parent-death readiness requires Program proof; Unix refusal is checked separately")
	}
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
	if runtime.GOOS == "darwin" {
		t.Skip("occupied-endpoint sequence requires Program proof; Unix refusal is checked separately")
	}
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
	if runtime.GOOS == "darwin" {
		t.Skip("readiness-writer sequence requires Program proof; Unix refusal is checked separately")
	}
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
	if runtime.GOOS == "darwin" {
		t.Skip("cancellation-after-readiness requires Program proof; Unix refusal is checked separately")
	}
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
	line, err := awaitReadiness(readyR, done, runtimeWait)
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

// A runtime whose endpoint is already held returns before readiness, and the
// readiness wait ends with that error instead of waiting for the test timeout.
func TestSupervisedStartupFailureEndsTheReadinessWait(t *testing.T) {
	o, _ := isolatedRuntime(t)
	held, err := listen.Listen(o.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	readyR, readyW := io.Pipe()
	defer readyR.Close()
	defer readyW.Close()
	done := make(chan error, 1)
	go func() { done <- superviseRuntime(context.Background(), o, r, readyW) }()
	began := time.Now()
	line, err := awaitReadiness(readyR, done, runtimeWait)
	if err == nil || !strings.Contains(err.Error(), "returned before readiness") {
		t.Fatalf("held endpoint: line %q, err %v", line, err)
	}
	if elapsed := time.Since(began); elapsed > 10*time.Second {
		t.Fatalf("startup failure took %v to end the readiness wait", elapsed)
	}
}

type shortReady struct{}

func (shortReady) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestSupervisedShortReadinessCleansListeners(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("short-readiness sequence requires Program proof; Unix refusal is checked separately")
	}
	o, _ := isolatedRuntime(t)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if err := superviseRuntime(context.Background(), o, r, shortReady{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short readiness: %v", err)
	}
	assertListenersReleased(t, o)
}
