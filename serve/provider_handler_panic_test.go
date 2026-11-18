package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// A provider whose handler panics answers the call handler_error "handler
// failed", and the runtime's log of that provider holds the service, method,
// panic value and stack the generated dispatcher wrote to standard error.
// The supervisor launches the fixture provider as a child and captures its
// output the way it captures every launched provider's.
func TestAProviderHandlerPanicReachesTheProviderLog(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	program := copyTestBinary(t, "oa-fixture-provider")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = identity.CanonicalProgramPath(filepath.Clean(self))
	pidDir := t.TempDir()
	p, err := openFixtureProviders(t, t.TempDir(), false, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	const name = "panics"
	f := providerFile{Declaration: providerDeclaration{Name: name, Program: program, Endpoint: name, Transport: transportNative,
		Contracts: []string{inference.Contract}, Activation: wire.ActivationOnDemand.String(),
		Arguments: []string{providerFixtureArg, "chat-panic", providerEndpointToken, pidDir, self}}}
	s := newFixtureProviderSupervisor(t, p, f)
	p.mu.Lock()
	p.supervisors[name] = s
	p.mu.Unlock()
	s.start()
	defer s.halt()
	if _, why := p.awaitReady(t.Context(), time.Minute, func() (*providerSupervisor, string) { return s, "" }); why != "" {
		t.Fatalf("the provider did not read ready: %q %+v", why, s.state())
	}
	address, err := os.ReadFile(filepath.Join(pidDir, "endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	transport := listen.FrameClient{Endpoint: string(address), Timeout: 10 * time.Second, MaxFrame: 8 << 20,
		Server: &listen.ServerExpectation{Principal: p.principal, Program: program}}.WithContext(t.Context())
	_, err = iwire.NewChatClient(transport).Start(iwire.Request{Model: "fixture-model",
		Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "hello"}}}}})
	var refusal *iwire.ServiceError
	if !errors.As(err, &refusal) || refusal.Code != iwire.ServiceErrorCodeHandlerError || refusal.Message != "handler failed" {
		t.Fatalf("Start of a panicking handler: %#v", err)
	}

	logPath := providerLogPath(p.dir, name)
	var log string
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, _ := os.ReadFile(logPath)
		log = string(raw)
		if strings.Contains(log, "fixtureChat.Start(") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{" Start: handler panic: fixture chat start broke\n", "goroutine ", "runtime/debug.Stack()", "fixtureChat.Start("} {
		if !strings.Contains(log, want) {
			t.Fatalf("provider log %s lacks %q:\n%s", logPath, want, log)
		}
	}
}
