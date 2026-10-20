package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestInstalledSetupTransportRefusesForeignSelection(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "tools", "openabstractions.exe")
	state := filepath.Join(root, "runtime-v1")
	panel := filepath.Join(root, "tools", "Abstraction Panel.exe")
	endpoint := listen.Endpoint(fmt.Sprintf("oa-installed-qualification-unserved-%d", os.Getpid()))
	selected := bootstrap.Selection{Endpoint: endpoint, Server: listen.ServerExpectation{Program: program}}
	input := setup.QualificationInput{RuntimeEndpoint: endpoint, RuntimeProgram: program, RuntimeStateDir: state}
	for name, mutate := range map[string]func(*setup.QualificationInput, *bootstrap.Selection, *string){
		"endpoint": func(v *setup.QualificationInput, _ *bootstrap.Selection, _ *string) { v.RuntimeEndpoint += "-other" },
		"program":  func(v *setup.QualificationInput, _ *bootstrap.Selection, _ *string) { v.RuntimeProgram += ".other" },
		"state":    func(v *setup.QualificationInput, _ *bootstrap.Selection, _ *string) { v.RuntimeStateDir += "-other" },
		"env":      func(_ *setup.QualificationInput, s *bootstrap.Selection, _ *string) { s.EndpointFromEnvironment = true },
		"panel": func(_ *setup.QualificationInput, _ *bootstrap.Selection, p *string) {
			*p = filepath.Join(root, filepath.Base(*p))
		},
	} {
		t.Run(name, func(t *testing.T) {
			in, selection, self := input, selected, panel
			mutate(&in, &selection, &self)
			_, err := installedSetupMachine(context.Background(), in, selection, state, self)
			if err == nil || strings.Contains(err.Error(), "verify the installed runtime connection") {
				t.Fatalf("foreign selection passed startup metadata: %v", err)
			}
		})
	}
}

func TestInstalledSetupTransportRefusesUntrustedPeer(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "tools", "openabstractions.exe")
	state := filepath.Join(root, "runtime-v1")
	panel := filepath.Join(root, "tools", "Abstraction Panel.exe")
	principal, _, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := listen.Endpoint(fmt.Sprintf("oa-installed-qualification-foreign-%d", os.Getpid()))
	catalog, err := resolution.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	host, err := resolution.Listen(endpoint, catalog, func(*identity.Peer, wire.ServiceReference) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	defer func() {
		cancel()
		_ = host.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	selection := bootstrap.Selection{Endpoint: endpoint, Server: listen.ServerExpectation{Principal: principal, Program: program}}
	input := setup.QualificationInput{RuntimeEndpoint: endpoint, RuntimeProgram: program, RuntimeStateDir: state}
	_, err = installedSetupMachine(ctx, input, selection, state, panel)
	if !errors.Is(err, listen.ErrServerUntrusted) {
		t.Fatalf("untrusted resolver error: %v", err)
	}
}
