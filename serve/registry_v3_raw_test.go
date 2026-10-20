package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type registry3PaddedSpecTransport struct {
	wire.EndpointTransport
	t       *testing.T
	unknown bool
}

func (t registry3PaddedSpecTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	t.t.Helper()
	frame = bytes.Replace(frame, []byte(`"registration": {`), append([]byte(`"registration": {`), bytes.Repeat([]byte(" "), maxRegistrationSpecBytes)...), 1)
	if t.unknown {
		frame = bytes.Replace(frame, []byte(`"registration": {`), []byte(`"registration": {"unexpected":true,`), 1)
	}
	var envelope struct {
		Arguments struct {
			Registration json.RawMessage `json:"registration"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil {
		t.t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, envelope.Arguments.Registration); err != nil {
		t.t.Fatal(err)
	}
	if len(envelope.Arguments.Registration) <= maxRegistrationSpecBytes || compact.Len() > maxRegistrationSpecBytes || len(frame) >= 1<<20 {
		t.t.Fatal("wrong raw/compact/frame fixture", len(envelope.Arguments.Registration), compact.Len(), len(frame))
	}
	return t.EndpointTransport.ExchangeFrame(frame)
}

func TestRegistryV3NativeRawSpecBoundBeforePublication(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("native Program proof unavailable")
	}
	r, caller := registryObserverFixture(t)
	d := registration1459Directory(t, t.TempDir())
	d.owner = caller.Account
	r.operator.credentials.runtimeRights.applications = d
	r.providers.registrationDirectory = func() *applicationDirectory { return d }
	r.providers.installation = ""
	endpoint := listen.Endpoint(fmt.Sprintf("registry3-raw-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	h, err := listenRegistry(endpoint, r, caller.Account, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		_ = h.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	transport := (listen.FrameClient{Endpoint: endpoint, Timeout: 3 * time.Second, MaxFrame: 1 << 20, Server: &listen.ServerExpectation{Principal: principal, Program: program}}).WithContext(ctx)
	client := registration.NewRegistryV3Client(transport)
	padded := registration.NewRegistryV3Client(registry3PaddedSpecTransport{EndpointTransport: transport, t: t})
	empty, err := client.Registrations("", 1)
	if err != nil || empty.Outcome != registration.OutcomePage || empty.Cursor != "" || len(empty.Registrations) != 0 {
		t.Fatal("empty terminal directory", empty, err)
	}
	spec := registration.RegistrationSpec{Providers: []registration.ProviderFacet{}, Servers: []registration.ServerFacet{{Name: "raw-host", Hosted: true, Kind: "openai-compatible", Base: "https://raw.fixture.invalid", Modalities: []string{"chat"}}}, Remotes: []registration.RemoteFacet{}, Applications: []registration.ApplicationFacet{}}
	created, err := client.Register(spec)
	if err != nil || created.Outcome != registration.OutcomeApplied {
		t.Fatal(created, err)
	}
	snapshot := func() map[string][]byte {
		entries, err := os.ReadDir(d.v3.dir)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]byte{}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				raw, err := os.ReadFile(filepath.Join(d.v3.dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				out[entry.Name()] = raw
			}
		}
		return out
	}
	for _, method := range []string{"Register", "Replace"} {
		t.Run(method, func(t *testing.T) {
			before := snapshot()
			var out registration.Change
			var err error
			if method == "Register" {
				other := spec
				other.Servers = append([]registration.ServerFacet(nil), spec.Servers...)
				other.Servers[0].Name = "raw-new-host"
				out, err = padded.Register(other)
			} else {
				out, err = padded.Replace(created.Revision, spec)
			}
			if err != nil || out.Outcome != registration.OutcomeInvalid || out.Reason != "encoded_size" {
				t.Errorf("oversized raw input admitted: %+v %v", out, err)
			}
			if !maps.EqualFunc(before, snapshot(), bytes.Equal) {
				t.Error("raw-bound refusal wrote canonical index/payload")
			}
		})
	}
	t.Run("native-directory-continuation", func(t *testing.T) {
		for i := 1; i <= 2; i++ {
			other := spec
			other.Servers = append([]registration.ServerFacet(nil), spec.Servers...)
			other.Servers[0].Name = fmt.Sprintf("raw-page-%d", i)
			out, err := client.Register(other)
			if err != nil || out.Outcome != registration.OutcomeApplied {
				t.Fatal(out, err)
			}
		}
		seen := map[string]bool{}
		cursor := ""
		terminal := false
		for range 4 {
			page, err := client.Registrations(cursor, 1)
			if err != nil || page.Outcome != registration.OutcomePage || len(page.Registrations) != 1 {
				t.Fatal("native drain", page, err)
			}
			key := page.Registrations[0].Key
			if seen[key] {
				t.Fatal("directory continuation repeated record", key)
			}
			seen[key] = true
			if page.Cursor == "" {
				terminal = true
				break
			}
			if page.Cursor == cursor {
				t.Fatal("continuation did not advance")
			}
			cursor = page.Cursor
		}
		if !terminal || len(seen) != 3 {
			t.Fatal("directory did not terminate exactly once", terminal, seen)
		}
		first, err := client.Observe("", 0)
		if err != nil || first.Outcome != registration.OutcomePage || first.Cursor == "" {
			t.Fatal("Observe snapshot cursor", first, err)
		}
		start := time.Now()
		unchanged, err := client.Observe(first.Cursor, 50)
		if err != nil || unchanged.Outcome != registration.OutcomePage || unchanged.Cursor != first.Cursor || time.Since(start) < 40*time.Millisecond {
			t.Fatal("unchanged Observe did not retain bounded wait", unchanged, err, time.Since(start))
		}
	})
	before := snapshot()
	if err := r.operator.credentials.policy.Set(rights.Subject{Account: caller.Account, Program: caller.Program}, ActionProviderManage, "account", false); err != nil {
		t.Fatal(err)
	}
	denied, err := padded.Register(spec)
	if err != nil || denied.Outcome != registration.OutcomeForbidden || denied.Refusal == nil || denied.Refusal.Source != rights.RefusalSourceRights || denied.Refusal.Decision != rights.RefusalDecisionDenied {
		t.Fatal("raw bound changed receiving Rights refusal", denied, err)
	}
	malformed := registration.NewRegistryV3Client(registry3PaddedSpecTransport{EndpointTransport: transport, t: t, unknown: true})
	if _, err := malformed.Register(spec); err == nil {
		t.Fatal("raw check bypassed generated unknown-field refusal")
	}
	if !maps.EqualFunc(before, snapshot(), bytes.Equal) {
		t.Fatal("refused raw frame wrote canonical index/payload")
	}
}
