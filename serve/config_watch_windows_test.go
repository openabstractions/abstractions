//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	configclient "github.com/openabstractions/abstraction-config/go/client"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// keptModelHostConfig is the runtime's configuration service over a registry
// holding a kept model host whose program is this test binary, beside the
// owner's other registrations, with the runtime's own key-read policy.
type keptModelHostConfig struct {
	endpoint string
	path     string
	calls    *atomic.Int64
}

func startKeptModelHostConfig(t *testing.T) keptModelHostConfig {
	t.Helper()
	call, closeCall := messages1608Call(t)
	peer, err := call.Peer()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := identity.SubjectOf(peer, listen.Program)
	if err != nil {
		t.Fatal(err)
	}
	closeCall()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	held, err := openRegistryProgramImage(t.Context(), peer, program)
	if err != nil {
		t.Fatal(err)
	}
	source := applicationV3Source{Program: program, FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}
	held.Close()

	f := &modelHostOperatorFixture{page: registration.Page{Outcome: registration.OutcomePage}, outcome: registration.OutcomeApplied}
	writer, err := newModelHostRegistryWriter(t.Context(), f, f)
	if err != nil {
		t.Fatal(err)
	}
	w := newWaiting(0)
	defer w.stop()
	settings := hostSettings{program: program, engine: filepath.Join(t.TempDir(), "llama-server.exe"), endpoint: "modelhost"}
	var out bytes.Buffer
	if err := declareHost(w, writer, wire.DeclarationList{}, settings, []hostedModel{{Name: "qwen3.6-35b-a3b", Store: "store", Object: "object"}}, "models host", &out, false, "configured"); err != nil {
		t.Fatal(err)
	}

	state := t.TempDir()
	d := registration1459Directory(t, state)
	d.owner = subject.Account
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	// The model host is no operator: its reads pass the registration proof.
	r.operators = []string{filepath.Join(t.TempDir(), "Abstraction Runtime.exe")}
	d.programConfigKeep = r.keepProviderConfig
	d.programConfigRemove = r.removeProviderConfig
	d.programConfigReconcile = r.retireProviderConfig
	options := host.Options{}
	configureRuntimeTypedConfig(&options, &applicationsHost{directory: d}, r, filepath.Join(state, "records.jsonl"))

	// The owner's registry holds six programs; five besides the model host.
	for i := range 5 {
		other := registry3Candidate(t, fmt.Sprintf("other%d", i))
		if created := d.publishProgramRegistration(t.Context(), "", true, other, nil, "operator"); created.Outcome != registration.OutcomeApplied {
			t.Fatal(created)
		}
	}
	candidate := registry3Candidate(t, "modelhost")
	candidate.Key = f.spec.Program
	candidate.Spec = f.spec
	candidate.State = registration.StatePendingApproval
	d.askKeep = func(string) error {
		record, _, _, ok := d.programSelfEntry(candidate.Key)
		if !ok || !d.setProgramQuestion(t.Context(), candidate.Key, program, "kept-question", record.Revision) {
			t.Error("Keep question not bound")
		}
		return nil
	}
	if created := d.publishProgramRegistration(t.Context(), "", true, candidate, &source, "operator"); created.Outcome != registration.OutcomeApplied {
		t.Fatal(created)
	}
	if !d.keepProgramOwned(t.Context(), candidate.Key, program, "kept-question") {
		t.Fatal("Keep refused")
	}

	// Count the runtime policy's decision passes.
	calls := &atomic.Int64{}
	policy := options.ConfigTyped.ReadPolicyKeys
	if policy == nil {
		t.Fatal("the runtime composed no key-set read policy")
	}
	options.ConfigTyped.ReadPolicyKeys = func(ctx context.Context, peer *identity.Peer, keys []string) map[string]error {
		calls.Add(1)
		return policy(ctx, peer, keys)
	}
	endpoint := listen.Endpoint(fmt.Sprintf("config-watch-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	path := filepath.Join(state, "config.json")
	provider, err := configservice.ListenWithStore(endpoint, casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.OmitMachineRung(); err != nil {
		t.Fatal(err)
	}
	if err := provider.EnableTypedConfig(options.ConfigTyped); err != nil {
		t.Fatal(err)
	}
	if err := provider.EnableObservation(configservice.FileObservation(path)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- provider.Serve(context.Background()) }()
	t.Cleanup(func() {
		provider.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("config service did not stop")
		}
	})
	return keptModelHostConfig{endpoint: endpoint, path: path, calls: calls}
}

// A kept model host's watch of model_context answers within its wait, and a
// change the person makes reaches it. On R29 one 2 s watch took over 7 s and
// the 25 s watch never answered, so a context change never reached a running
// model host.
func TestModelHostContextWatchAnswersWithinItsWait(t *testing.T) {
	c := startKeptModelHostConfig(t)
	client := configclient.NewWithTransport(listen.FrameClient{Endpoint: c.endpoint})
	ctx := t.Context()

	started, before := time.Now(), c.calls.Load()
	read, err := client.ReadTypedContext(ctx, []string{modelContextKeyForTest})
	t.Logf("read of model_context: %v, %d decision passes", time.Since(started), c.calls.Load()-before)
	if err != nil || read.Outcome != configwire.ReadOutcomeRead || len(read.Entries) != 1 {
		t.Fatalf("the kept model host cannot read model_context: %+v %v", read, err)
	}

	started, before = time.Now(), c.calls.Load()
	first, err := client.ObserveTypedContext(ctx, "", 0)
	t.Logf("first observation: %v, %d decision passes", time.Since(started), c.calls.Load()-before)
	if err != nil || first.Outcome != configwire.ObservationOutcomeV2Changed || first.Snapshot == nil {
		t.Fatalf("first observation: %+v %v", first, err)
	}
	if len(first.Snapshot.Entries) != 1 || first.Snapshot.Entries[0].Key != modelContextKeyForTest {
		t.Fatalf("the model host saw other keys or none: %+v", first.Snapshot)
	}
	// Every other key is refused as undesignated, as a per-key read refuses it.
	if len(first.Snapshot.Refused) != len(configwire.ConfigKeyDescriptors())-1 {
		t.Fatalf("refused keys: %+v", first.Snapshot.Refused)
	}
	for _, refused := range first.Snapshot.Refused {
		if refused.Reason != configwire.KeyRefusalReasonNotGranted || refused.Refusal == nil || refused.Refusal.Source != rightswire.RefusalSourceIdentity {
			t.Fatalf("%s refused as %+v, want undesignated", refused.Key, refused)
		}
	}

	const wait = 2 * time.Second
	started, before = time.Now(), c.calls.Load()
	quiet, err := client.ObserveTypedContext(ctx, first.Cursor, wait.Milliseconds())
	took, passes := time.Since(started), c.calls.Load()-before
	t.Logf("quiet %v watch: %v, %d decision passes", wait, took, passes)
	if err != nil || quiet.Outcome != configwire.ObservationOutcomeV2Unchanged {
		t.Fatalf("quiet watch: %+v %v", quiet, err)
	}
	if took > wait+time.Second {
		t.Fatalf("a %v watch answered after %v", wait, took)
	}
	// Opening, two probes of two passes each, and the answer's check. R29
	// decided each key on its own, 133 times for this watch.
	if passes > 6 {
		t.Fatalf("a quiet watch made %d decision passes, want at most 6", passes)
	}

	answered := make(chan configwire.ConfigObservationV2, 1)
	failed := make(chan error, 1)
	started = time.Now()
	go func() {
		next, err := client.ObserveTypedContext(ctx, quiet.Cursor, wait.Milliseconds())
		if err != nil {
			failed <- err
			return
		}
		answered <- next
	}()
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(c.path, []byte(`{"model_context":{"qwen3.6":"65536"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failed:
		t.Fatal(err)
	case next := <-answered:
		took := time.Since(started)
		t.Logf("changed watch: %v", took)
		if next.Outcome != configwire.ObservationOutcomeV2Changed || next.Snapshot == nil {
			t.Fatalf("the change did not reach the watcher: %+v", next)
		}
		values, err := modelContextValuesForTest(*next.Snapshot)
		if err != nil || values["qwen3.6"] != "65536" {
			t.Fatalf("model_context after the change: %v %v", values, err)
		}
		if took > wait+time.Second {
			t.Fatalf("the change reached the watcher after %v", took)
		}
	case <-time.After(wait + 5*time.Second):
		t.Fatal("the watch did not answer")
	}
}

const modelContextKeyForTest = "model_context"

func modelContextValuesForTest(result configwire.ReadResult) (map[string]string, error) {
	for _, entry := range result.Entries {
		if entry.Key == modelContextKeyForTest && entry.Value != nil && entry.Value.Kind == configwire.ValueKindTextMap {
			return entry.Value.TextMap, nil
		}
	}
	return nil, fmt.Errorf("no model_context in %+v", result)
}
