package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	download "github.com/openabstractions/abstraction-download/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	job "github.com/openabstractions/abstraction-job/go"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func TestJobsMigrateHelpAndMappingDecoder(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"migrate-legacy"}, {"migrate-legacy", "--help"}} {
		var out bytes.Buffer
		if err := jobsCommand(args, &out, &bytes.Buffer{}); err != nil {
			t.Fatal(args, err)
		}
		if !strings.Contains(out.String(), "migrate-legacy") {
			t.Fatalf("help for %q: %s", args, out.String())
		}
	}
	for _, args := range [][]string{{"unknown"}, {"migrate-legacy", "unknown"}, {"migrate-legacy", "inspect", "extra"}, {"migrate-legacy", "apply"}, {"migrate-legacy", "inspect", "--state-dir", "relative"}, {"migrate-legacy", "inspect", "--state-dir="}} {
		var exit *exitError
		if err := jobsCommand(args, &bytes.Buffer{}, &bytes.Buffer{}); !errors.As(err, &exit) || exit.code != exitUsage {
			t.Fatalf("%q: %v", args, err)
		}
	}
	program, _ := filepath.Abs("caller")
	valid := fmt.Sprintf(`{"format":%q,"assignments":[{"operation_id":"op","record_sha256":"%s","caller":{"account_kind":"posix","principal":"1000","program":%q},"request_key":"k","submission":{"kind":"download","spec":{"a":1},"required_guarantees":[]}}]}`, LegacyMappingFormat, strings.Repeat("a", 64), program)
	if m, _, err := decodeLegacyMapping([]byte(valid)); err != nil || len(m.Assignments) != 1 || !strings.HasPrefix(m.Assignments[0].CallerScope, "owner-program@1:") {
		t.Fatal(m, err)
	}
	for name, data := range map[string]string{
		"unknown field":   strings.Replace(valid, `"request_key"`, `"note":"x","request_key"`, 1),
		"duplicate field": strings.Replace(valid, `"request_key":"k"`, `"request_key":"k","request_key":"other"`, 1),
		"format":          strings.Replace(valid, LegacyMappingFormat, "other@1", 1),
		"trailing":        valid + `{}`,
		"relative caller": strings.Replace(valid, fmt.Sprintf("%q", program), `"caller"`, 1),
	} {
		if _, _, err := decodeLegacyMapping([]byte(data)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

type legacyFixture struct {
	root  string
	store *job.FileStore
	ids   map[job.State]string
	sinks map[string]string
}

func (f *legacyFixture) submit(t *testing.T, name string, body []byte) string {
	t.Helper()
	spec := download.Spec{Artifact: download.Artifact{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Size: int64(len(body))},
		Sources: []download.Source{{Scheme: "https", Locator: "https://legacy.invalid/" + name}}, Sink: download.Sink{Final: "models/" + name + ".bin"}}
	id, err := download.Submit(f.store, spec)
	if err != nil {
		t.Fatal(err)
	}
	f.sinks[id] = spec.Sink.Final
	return id
}

func (f *legacyFixture) finish(t *testing.T, id string, lease *job.Record, edit func(*job.Record)) {
	t.Helper()
	if lease == nil {
		var err error
		if lease, err = f.store.Claim(id, "legacy-worker", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.Update(id, lease.Lease.Epoch, func(r *job.Record) error { edit(r); return nil }); err != nil {
		t.Fatal(err)
	}
}

func (f *legacyFixture) deliver(t *testing.T, id string, body []byte) {
	t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(f.sinks[id]))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func runCLI(t *testing.T, bin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

func TestJobsMigrateLegacyCommandEndToEnd(t *testing.T) {
	dir, err := os.MkdirTemp("", "oa-legacy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin := filepath.Join(dir, "openabstractions")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	state := filepath.Join(dir, "state")
	f := &legacyFixture{root: filepath.Join(state, "jobs"), ids: map[job.State]string{}, sinks: map[string]string{}}
	if f.store, err = job.NewFileStore(f.root); err != nil {
		t.Fatal(err)
	}
	bodies := map[string][]byte{}
	for _, s := range []job.State{job.StatePending, job.StateRunning, job.StateTransferred, job.StateComplete, job.StateFailed, job.StateCancelled} {
		body := []byte("legacy caller-chosen bytes for " + string(s))
		id := f.submit(t, string(s), body)
		f.ids[s], bodies[id] = id, body
	}
	running, err := f.store.Claim(f.ids[job.StateRunning], "legacy-worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	transferred, err := f.store.Claim(f.ids[job.StateTransferred], "legacy-worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.finish(t, transferred.ID, transferred, func(r *job.Record) {
		r.State = job.StateTransferred
		r.Delegation = &job.Delegation{System: "bits", ExternalID: "legacy-delegate-handle"}
	})
	f.deliver(t, f.ids[job.StateComplete], bodies[f.ids[job.StateComplete]])
	f.finish(t, f.ids[job.StateComplete], nil, func(r *job.Record) { r.State = job.StateComplete; r.Progress.Done = r.Progress.Total })
	f.finish(t, f.ids[job.StateFailed], nil, func(r *job.Record) { r.State = job.StateFailed; r.Error = "legacy failure" })
	f.finish(t, f.ids[job.StateCancelled], nil, func(r *job.Record) { r.State = job.StateCancelled })

	if out, _, code := runCLI(t, bin, "jobs", "migrate-legacy"); code != 0 || !strings.Contains(out, "Mapping file") {
		t.Fatalf("no-argument help: %d %s", code, out)
	}
	out, stderr, code := runCLI(t, bin, "jobs", "migrate-legacy", "inspect", "--state-dir", state)
	if code != 0 {
		t.Fatalf("inspect: %d %s", code, stderr)
	}
	for s, reason := range map[job.State]string{job.StatePending: "not_terminal", job.StateRunning: "active_lease", job.StateTransferred: "active_delegation"} {
		if !regexp.MustCompile(regexp.QuoteMeta(f.ids[s]) + `\s+` + string(s) + `\s+[0-9a-f]{64}\s+` + reason).MatchString(out) {
			t.Fatalf("inspect lacks %s %s:\n%s", s, reason, out)
		}
	}
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stateOf := map[string]job.State{}
	for s, id := range f.ids {
		stateOf[id] = s
	}
	writeMapping := func(name string, edit func(*mappingEntry)) string {
		t.Helper()
		template, stderr, code := runCLI(t, bin, "jobs", "migrate-legacy", "inspect", "--template", "--state-dir", state)
		var m mappingFile
		if code != 0 || json.Unmarshal([]byte(template), &m) != nil || len(m.Assignments) != 6 {
			t.Fatalf("template: %d %s %s", code, template, stderr)
		}
		for i := range m.Assignments {
			e := &m.Assignments[i]
			e.Caller.Principal, e.Caller.Program = owner.Uid, program
			e.RequestKey = "legacy-" + string(stateOf[e.OperationID])
			if edit != nil {
				edit(e)
			}
		}
		data, _ := json.MarshalIndent(m, "", "  ")
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	before := storageTree(t, f.root)

	_, stderr, code = runCLI(t, bin, "jobs", "migrate-legacy", "apply", "--state-dir", state, "--mapping", writeMapping("active.json", nil))
	if code != exitRefused || !strings.Contains(stderr, "legacy migration refused") {
		t.Fatalf("active refusal: %d %s", code, stderr)
	}
	for s, reason := range map[job.State]string{job.StatePending: "not_terminal", job.StateRunning: "active_lease", job.StateTransferred: "active_delegation"} {
		if !strings.Contains(stderr, "refused "+f.ids[s]+": "+reason) {
			t.Fatalf("missing typed refusal %s: %s", reason, stderr)
		}
	}
	unknown := writeMapping("unknown.json", nil)
	data, _ := os.ReadFile(unknown)
	os.WriteFile(unknown, bytes.Replace(data, []byte(`"format"`), []byte(`"comment": "x", "format"`), 1), 0600)
	if _, stderr, code = runCLI(t, bin, "jobs", "migrate-legacy", "apply", "--state-dir", state, "--mapping", unknown); code != exitUsage || !strings.Contains(stderr, "unknown field") {
		t.Fatalf("unknown field: %d %s", code, stderr)
	}
	if !reflect.DeepEqual(before, storageTree(t, f.root)) {
		t.Fatal("refused CLI migration changed storage")
	}

	// The existing worker and delegate finish their work through the legacy provider.
	f.deliver(t, f.ids[job.StateRunning], bodies[f.ids[job.StateRunning]])
	f.finish(t, running.ID, running, func(r *job.Record) { r.State = job.StateComplete; r.Progress.Done = r.Progress.Total })
	f.finish(t, transferred.ID, transferred, func(r *job.Record) { r.State = job.StateFailed; r.Error = "delegate reported failure" })
	f.finish(t, f.ids[job.StatePending], nil, func(r *job.Record) { r.State = job.StateCancelled })
	mapping := writeMapping("mapping.json", nil)

	guard, err := acceptanceprovider.AcquireHost(f.root)
	if err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runCLI(t, bin, "jobs", "migrate-legacy", "apply", "--state-dir", state, "--mapping", mapping)
	guard.Close()
	if code != exitHostActive || !strings.Contains(stderr, "runtime job host is running") {
		t.Fatalf("live host: %d %s", code, stderr)
	}
	out, stderr, code = runCLI(t, bin, "jobs", "migrate-legacy", "apply", "--state-dir", state, "--mapping", mapping)
	if code != 0 || !strings.Contains(out, "Legacy migration complete: 6 records") {
		t.Fatalf("apply: %d %s %s", code, out, stderr)
	}
	epoch := regexp.MustCompile(`history epoch: (\S+)`).FindStringSubmatch(out)
	if epoch == nil {
		t.Fatal(out)
	}
	if _, stderr, code = runCLI(t, bin, "storage", "check", "--state-dir", state); code != 0 {
		t.Fatalf("storage check after migration: %d %s", code, stderr)
	}
	conflict := writeMapping("conflict.json", func(e *mappingEntry) { e.RequestKey += "-next-caller" })
	if _, stderr, code = runCLI(t, bin, "jobs", "migrate-legacy", "apply", "--state-dir", state, "--mapping", conflict); code != exitConflict || !strings.Contains(stderr, "conflicts") {
		t.Fatalf("conflict: %d %s", code, stderr)
	}

	endpoint := func(s string) string {
		if runtime.GOOS == "windows" {
			return testPipe("oa-legacy-migration", s)
		}
		return filepath.Join(dir, s)
	}
	o := runtimeFlags{endpoint: endpoint("r"), logEndpoint: endpoint("l"), configEndpoint: endpoint("c"), jobEndpoint: endpoint("j"), out: filepath.Join(dir, "records.jsonl"), stateDir: state, withoutModels: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, o, func() error { close(ready); return nil }) }()
	// The runtime sends exactly one result. Once a receive has taken it, stop
	// must not receive again: that second receive blocked until the test
	// binary's timeout whenever the runtime failed to start.
	returned := false
	stop := func() {
		if !returned {
			returned = true
			cancel()
			if err := awaitStopped(t, done, "managed runtime"); err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		}
	}
	defer stop()
	select {
	case <-ready:
	case err := <-done:
		returned = true
		t.Fatal("managed runtime did not start on the migrated root:", err)
	case <-time.After(20 * time.Second):
		t.Fatal("managed runtime startup")
	}
	for _, args := range [][]string{{"apply", "--mapping", mapping}, {"abandon"}} {
		_, stderr, code := runCLI(t, bin, append(append([]string{"jobs", "migrate-legacy"}, args...), "--state-dir", state)...)
		if code != exitHostActive || !strings.Contains(stderr, "runtime job host is running") {
			t.Fatalf("%v beside running runtime: %d %s", args, code, stderr)
		}
	}

	wait, cancelWait := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelWait()
	finalState := map[job.State]string{job.StatePending: "cancelled", job.StateRunning: "complete", job.StateTransferred: "failed", job.StateComplete: "complete", job.StateFailed: "failed", job.StateCancelled: "cancelled"}
	// Darwin's peer transport cannot satisfy listen.Program. The measured
	// refusal selects the fail-closed expectation; the migration assertions
	// after the runtime stops hold on every platform.
	if statusTransportProvesProgram(t) {
		observeMigratedJobs(t, wait, o.endpoint, epoch[1], f, bodies, finalState)
	} else if _, err := client.New(o.endpoint).ResolveJobOperations(wait, client.Requirements{}); err == nil {
		t.Fatal("unproven transport resolved job operations")
	} else {
		t.Log("UNPROVEN job operations over the migrated root on Darwin; fail-closed resolution verified")
	}
	stop()
	if _, stderr, code = runCLI(t, bin, "jobs", "migrate-legacy", "abandon", "--state-dir", state); code != exitConflict {
		t.Fatalf("abandon completed migration: %d %s", code, stderr)
	}
	after := storageTree(t, f.root)
	for id, sink := range f.sinks {
		if after[filepath.FromSlash(sink)] != string(bodies[id]) && finalState[stateOf[id]] == "complete" {
			t.Fatalf("result file moved or rewritten: %s", sink)
		}
	}
}

func observeMigratedJobs(t *testing.T, wait context.Context, endpoint, epoch string, f *legacyFixture, bodies map[string][]byte, finalState map[job.State]string) {
	t.Helper()
	jobs, err := client.New(endpoint).ResolveJobOperations(wait, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	window, err := jobs.GetHistoryWindow(wait)
	if err != nil || window.HistoryEpoch != epoch {
		t.Fatalf("history window %+v %v", window, err)
	}
	for s, id := range f.ids {
		identity := api.RequestIdentity{Key: "legacy-" + string(s), HistoryEpoch: window.HistoryEpoch}
		observed, err := jobs.ObserveWork(wait, identity)
		if err != nil || observed.Outcome != "observed" || observed.Snapshot.Receipt.OperationId != id || observed.Snapshot.State != finalState[s] {
			t.Fatalf("observe %s: %+v %v", s, observed, err)
		}
		if finalState[s] == "complete" {
			var got bytes.Buffer
			if _, err := jobs.CopyResult(wait, identity, &got); err != nil || !bytes.Equal(got.Bytes(), bodies[id]) {
				t.Fatalf("result %s at caller-chosen sink %s: %q %v", s, f.sinks[id], got.Bytes(), err)
			}
			continue
		}
		if read, err := jobs.ReadResult(wait, identity, 0, 1024); err != nil || read.Outcome != "unavailable" {
			t.Fatalf("terminal non-result %s: %+v %v", s, read, err)
		}
	}
}
