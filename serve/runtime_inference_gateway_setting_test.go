package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// startInferenceRuntime runs an isolated runtime with a file credentials
// backend until the returned stop is called or the test ends.
func startInferenceRuntime(t *testing.T, options runtimeFlags) func() {
	t.Helper()
	path := filepath.Join(options.stateDir, "credentials", credentialsBackendFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("file-0600\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(stop)
	return stop
}

// listening reports whether something accepts TCP connections at address.
func listening(address string) bool {
	c, err := net.DialTimeout("tcp4", address, 2*time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// The persisted gateway setting: off by default, gateway on opens the window
// on the running runtime and keeps it open across a restart without
// --gateway, gateway off closes the listener, a caller without host.manage is
// refused, and an address off loopback is invalid.
func TestGatewaySettingOpensAndClosesTheWindowAcrossRestarts(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	address := freeLoopbackPort(t)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	stop := startInferenceRuntime(t, options)

	out, err := runInference(t, append([]string{"gateway", "status"}, endpoint...)...)
	if err != nil || !strings.Contains(out, "setting: off") || !strings.Contains(out, "window:  closed") {
		t.Fatalf("default status: %v\n%s", err, out)
	}
	if listening(address) {
		t.Fatalf("something listens on %s before gateway on", address)
	}
	out, err = runInference(t, append([]string{"gateway", "on", "--address", address}, endpoint...)...)
	if err != nil || !strings.Contains(out, "setting: on at "+address) || !strings.Contains(out, "listening on http://"+address+"/v1") {
		t.Fatalf("gateway on: %v\n%s", err, out)
	}
	if !listening(address) {
		t.Fatal("gateway on did not open the listener")
	}

	stop()
	if listening(address) {
		t.Fatal("the window outlived its runtime")
	}
	stop = startInferenceRuntime(t, options)
	if !listening(address) {
		t.Fatal("the setting did not reopen the window after a restart")
	}
	out, err = runInference(t, append([]string{"gateway", "status", "--json"}, endpoint...)...)
	if err != nil || !strings.Contains(out, `"Open":true`) || !strings.Contains(out, `"Listening":true`) {
		t.Fatalf("status after restart: %v\n%s", err, out)
	}

	call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.New(options.endpoint).ResolveInferenceOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := operator.Gateway(call)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"0.0.0.0:8793", "192.168.0.2:8793", "localhost:8793", "[::1]:8793"} {
		change, err := operator.SetGateway(call, state.Revision, true, bad)
		if err != nil || change.Outcome.String() != "invalid" || change.Reason != "address" {
			t.Fatalf("SetGateway %s: %+v %v", bad, change, err)
		}
	}
	if _, err := runInference(t, append([]string{"gateway", "on", "--address", "0.0.0.0:8793"}, endpoint...)...); err == nil {
		t.Fatal("the command line accepted an address off loopback")
	}

	out, err = runInference(t, append([]string{"gateway", "off"}, endpoint...)...)
	if err != nil || !strings.Contains(out, "setting: off") || !strings.Contains(out, "window:  closed") {
		t.Fatalf("gateway off: %v\n%s", err, out)
	}
	if listening(address) {
		t.Fatal("gateway off left the listener open")
	}
	stop()
	startInferenceRuntime(t, options)
	if listening(address) {
		t.Fatal("gateway off did not survive a restart")
	}

	// A caller without host.manage: deny the rule this operator program holds by
	// installation, then every gateway call is forbidden.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	rightsOperator, err := client.New(options.endpoint).ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := rightsOperator.ListPolicyContext(call, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("list policy: %+v %v", page, err)
	}
	deny := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}, Action: inference.ActionHostManage, Resource: "account", Permit: false}
	if edit, err := rightsOperator.SetRuleContext(call, page.Revision, deny); err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("deny host.manage: %+v %v", edit, err)
	}
	operator, err = client.New(options.endpoint).ResolveInferenceOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := operator.Gateway(call); err != nil || state.Outcome.String() != "forbidden" || state.Revision != "" {
		t.Fatalf("Gateway without host.manage: %+v %v", state, err)
	}
	if change, err := operator.SetGateway(call, "", true, address); err != nil || change.Outcome.String() != "forbidden" {
		t.Fatalf("SetGateway without host.manage: %+v %v", change, err)
	}
	_, err = runInference(t, append([]string{"gateway", "on", "--address", address}, endpoint...)...)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitRefusedCall || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("gateway on without host.manage: %v", err)
	}
	if listening(address) {
		t.Fatal("a refused gateway on opened the listener")
	}
}
