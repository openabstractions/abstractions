package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestPairedReadConsumersPreserveOneTypedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		outcome pairedReadOutcome
		cause   router.AvailabilityCause
	}{
		{"refused", fmt.Errorf("captured: %w", remote.ErrTrust), pairedReadRefused, router.AvailabilityUnknown},
		{"unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("fixture unreachable")}, pairedReadUnreachable, router.AvailabilityTransportUnavailable},
		{"handshake-deadline", fmt.Errorf("%w: %w", remote.ErrTrust, context.DeadlineExceeded), pairedReadUnreachable, router.AvailabilityTransportUnavailable},
		{"stale", fmt.Errorf("captured: %w", errPairingRevision), pairedReadStale, router.AvailabilityUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reports []error
			p := &runtimeProviders{dir: t.TempDir(), supervisors: map[string]*providerSupervisor{}, report: func(err error) { reports = append(reports, err) }}
			p.setPairedHosts(func() ([]*router.Host, error) {
				return []*router.Host{{Name: "must-not-escape", BindingID: "paired:fixture"}}, tc.err
			})
			_, routerErr := routerHosts(p)
			if got := p.Candidates(); len(got) != 0 {
				t.Fatal("failed peer became candidate", got)
			}
			m := &providerMediation{providers: p, report: p.report}
			if got := m.desired(); len(got) != 0 {
				t.Fatal("failed peer became route", got)
			}
			if len(reports) != 2 {
				t.Fatal("catalogue/mediation lost typed diagnostics", reports)
			}
			for _, err := range append([]error{routerErr}, reports...) {
				var failure *pairedReadError
				if !errors.As(err, &failure) || failure.Outcome != tc.outcome || failure.Cause != tc.cause || !errors.Is(err, tc.err) {
					t.Fatal("consumer classified differently", err, failure)
				}
			}
		})
	}
}

func TestPairedNameClashAppearsInHostsOnArrival(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{inference.ActionHostManage}, []installationRule{{inference.ActionHostManage, credentials.ResourceAccount}}); err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{dir: t.TempDir(), supervisors: map[string]*providerSupervisor{}, report: func(err error) { t.Log(err) }}
	entry := iwire.HostEntry{Name: "shared-name", Hosted: true, Kind: router.WireOpenAICompatible, Base: "https://fixture.invalid", Profiles: []string{"chat"}}
	raw, err := json.Marshal(newHostFile(entry, declaredByOperator))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.dir, "shared-name.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	p.setPairedHosts(func() ([]*router.Host, error) { return nil, &pairedNameClash{name: "shared-name"} })
	o := &inferenceOperator{dir: t.TempDir(), credentials: &runtimeCredentials{runtimeRights: r}, providers: p, router: router.New(), report: p.report}
	caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
	list := o.Hosts(context.Background(), caller)
	if list.Outcome != iwire.ListOutcomePage || len(list.Hosts) != 1 || list.Hosts[0].Up || list.Hosts[0].Why != pairedNameClashReason {
		t.Fatal("clash hidden at first read", list)
	}
	if hosts, err := routerHosts(p); len(hosts) != 0 || !errors.Is(err, errPairingConflict) {
		t.Fatal("ambiguous route escaped", hosts, err)
	}
	p.setPairedHosts(func() ([]*router.Host, error) { return nil, nil })
	list = o.Hosts(context.Background(), caller)
	if len(list.Hosts) != 1 || list.Hosts[0].Why == pairedNameClashReason {
		t.Fatal("resolved clash stayed visible", list)
	}
}
