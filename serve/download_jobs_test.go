package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// decoded is what the commands' --json documents carry, named as the contract
// names the fields.
type decoded struct {
	URL     string `json:"url"`
	Receipt struct {
		OperationID        string   `json:"operation_id"`
		LogicalOwner       string   `json:"logical_owner"`
		AcceptedGuarantees []string `json:"accepted_guarantees"`
		Identity           struct {
			Key          string `json:"key"`
			HistoryEpoch string `json:"history_epoch"`
			Attempt      *int64 `json:"attempt"`
		} `json:"identity"`
	} `json:"receipt"`
	Snapshot  *decodedSnapshot  `json:"snapshot"`
	Snapshots []decodedSnapshot `json:"snapshots"`
	Outcome   string            `json:"outcome"`
	Out       string            `json:"out"`
	Bytes     int64             `json:"bytes"`
}

type decodedSnapshot struct {
	State   string `json:"state"`
	Receipt struct {
		OperationID string `json:"operation_id"`
		Identity    struct {
			Key     string `json:"key"`
			Attempt int64  `json:"attempt"`
		} `json:"identity"`
	} `json:"receipt"`
	Failure *struct {
		Classification string `json:"classification"`
		Cause          string `json:"cause"`
	} `json:"failure"`
	Label        string `json:"label"`
	LabelDerived bool   `json:"label_derived"`
}

func assertExit(t *testing.T, err error, code int, what string) *exitError {
	t.Helper()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != code {
		t.Fatalf("%s: want exit %d, got %v", what, code, err)
	}
	return exit
}

// serviceRuntime starts one isolated in-process runtime that executes
// downloads, and returns its bootstrap endpoint.
func serviceRuntime(t *testing.T) string {
	t.Helper()
	options, _ := isolatedRuntime(t)
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
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})
	return options.endpoint
}

// served starts an HTTP server on the loopback interface holding known bytes.
func served(t *testing.T, body []byte) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/thing.bin"
}

// submissionFor builds the submission `download <locator>` sends, so the key
// derivation is measured on the same bytes the command would submit.
func submissionFor(t *testing.T, locator string) api.Submission {
	t.Helper()
	source, _, err := downloadSource(locator)
	if err != nil {
		t.Fatal(err)
	}
	payload := request.Encode(&request.Request{Sources: []request.Source{source}})
	return api.Submission{Kind: download.Kind, Spec: payload, RequiredGuarantees: slices.Clone(api.AdmissionGuarantees)}
}

func runJSON(t *testing.T, what string, run func(io.Writer, io.Writer) error) decoded {
	t.Helper()
	var out, diagnostics bytes.Buffer
	if err := run(&out, &diagnostics); err != nil {
		t.Fatalf("%s: %v (stderr %q)", what, err, diagnostics.String())
	}
	var document decoded
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("%s: %v: %s", what, err, out.Bytes())
	}
	return document
}

// The whole surface against one runtime: a download to a chosen sink, the
// inventory and observation of what it created, an equal resubmission, a second
// copy of the result and cancellation of terminal and live work.
func TestDownloadAndJobsThroughTheRuntime(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	endpoint := serviceRuntime(t)
	body := bytes.Repeat([]byte("openabstractions "), 4096)
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	source := served(t, body)
	sink := t.TempDir()

	// The URL comes first, with flags after it, as the examples write it.
	args := []string{source, "--endpoint", endpoint, "--out", sink, "--sha256", digest, "--json", "--timeout", "90s"}
	first := runJSON(t, "download", func(out, diag io.Writer) error { return downloadCommand(args, out, diag) })
	if first.Snapshot == nil || first.Snapshot.State != "complete" {
		t.Fatalf("download did not complete: %+v", first.Snapshot)
	}
	operation := first.Receipt.OperationID
	if operation == "" || first.Receipt.LogicalOwner == "" || len(first.Receipt.AcceptedGuarantees) != 3 {
		t.Fatalf("receipt did not carry the admission it required: %+v", first.Receipt)
	}
	if a := first.Receipt.Identity.Attempt; a == nil || *a != 0 {
		t.Fatalf("the original request is not reported as attempt 0: %v", a)
	}
	delivered, err := os.ReadFile(filepath.Join(sink, "thing.bin"))
	if err != nil || !bytes.Equal(delivered, body) {
		t.Fatalf("download delivered different bytes: %v", err)
	}
	if first.Out != filepath.Join(sink, "thing.bin") || first.Bytes != int64(len(body)) {
		t.Fatalf("reported sink or size: %q %d", first.Out, first.Bytes)
	}

	shown := runJSON(t, "jobs show", func(out, diag io.Writer) error {
		return jobsCommand([]string{"show", operation, "--endpoint", endpoint, "--json"}, out, diag)
	})
	if shown.Snapshot == nil || shown.Snapshot.Receipt.OperationID != operation || shown.Snapshot.State != "complete" {
		t.Fatalf("jobs show did not observe the completed operation: %+v", shown.Snapshot)
	}
	// Without --label the runtime derives one from the URL (JOB-A12).
	const derived = "127.0.0.1 · thing.bin"
	if shown.Snapshot.Label != derived || !shown.Snapshot.LabelDerived {
		t.Fatalf("jobs show label: %q derived=%v", shown.Snapshot.Label, shown.Snapshot.LabelDerived)
	}

	listed := runJSON(t, "jobs list", func(out, diag io.Writer) error {
		return jobsCommand([]string{"list", "--endpoint", endpoint, "--json"}, out, diag)
	})
	found := false
	for _, s := range listed.Snapshots {
		found = found || s.Receipt.OperationID == operation && s.Label == derived && s.LabelDerived
	}
	if !found {
		t.Fatalf("jobs list omitted this program's own work or its label: %+v", listed.Snapshots)
	}
	var table bytes.Buffer
	if err := jobsCommand([]string{"list", "--endpoint", endpoint}, &table, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), derived+" (derived)") {
		t.Fatalf("jobs list table lacks the derived label:\n%s", table.String())
	}

	// An equal submission returns the original receipt rather than starting a
	// second transfer (JOB-A2).
	again := runJSON(t, "download again", func(out, diag io.Writer) error { return downloadCommand(args, out, diag) })
	if again.Receipt.OperationID != operation {
		t.Fatalf("an equal submission created new work: %s then %s", operation, again.Receipt.OperationID)
	}

	// jobs result writes the file it is told to; the result carries no name.
	second := filepath.Join(t.TempDir(), "again.bin")
	copied := runJSON(t, "jobs result", func(out, diag io.Writer) error {
		return jobsCommand([]string{"result", operation, "--out", second, "--endpoint", endpoint, "--json"}, out, diag)
	})
	if copied.Bytes != int64(len(body)) || copied.Out != second {
		t.Fatalf("jobs result copied %d of %d bytes to %q", copied.Bytes, len(body), copied.Out)
	}
	recopied, err := os.ReadFile(copied.Out)
	if err != nil || !bytes.Equal(recopied, body) {
		t.Fatalf("jobs result delivered different bytes: %v", err)
	}

	// Cancelling finished work is not an error: there is nothing to cancel.
	terminal := runJSON(t, "jobs cancel terminal", func(out, diag io.Writer) error {
		return jobsCommand([]string{"cancel", operation, "--endpoint", endpoint, "--json"}, out, diag)
	})
	if terminal.Outcome != "already_terminal" {
		t.Fatalf("cancelling complete work: %q", terminal.Outcome)
	}

	// Cancelling live work records intent, which is not proof that it stopped.
	live := runJSON(t, "download no-wait", func(out, diag io.Writer) error {
		return downloadCommand([]string{"--endpoint", endpoint, "--no-wait", "--json", "--label", "the model that never arrives", "http://127.0.0.1:1/never"}, out, diag)
	})
	if live.Receipt.OperationID == "" || live.Snapshot != nil {
		t.Fatalf("--no-wait reported more than the receipt: %+v", live)
	}
	// A caller label wins over derivation and is not marked derived.
	var liveShown bytes.Buffer
	if err := jobsCommand([]string{"show", live.Receipt.OperationID, "--endpoint", endpoint}, &liveShown, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(liveShown.String(), "  label      the model that never arrives\n") {
		t.Fatalf("jobs show lacks the caller label:\n%s", liveShown.String())
	}
	requested := runJSON(t, "jobs cancel live", func(out, diag io.Writer) error {
		return jobsCommand([]string{"cancel", live.Receipt.OperationID, "--endpoint", endpoint, "--json"}, out, diag)
	})
	if requested.Outcome != "requested" {
		t.Fatalf("cancelling live work: %q", requested.Outcome)
	}
}

// A retained key reused with different arguments is a typed refusal, not a
// second download and not a silent replacement.
func TestDownloadRefusesAReusedKeyWithDifferentArguments(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	endpoint := serviceRuntime(t)
	var out, diagnostics bytes.Buffer
	if err := downloadCommand([]string{"--endpoint", endpoint, "--key", "shared", "--no-wait", "http://127.0.0.1:1/first"}, &out, &diagnostics); err != nil {
		t.Fatalf("first submission: %v (stderr %q)", err, diagnostics.String())
	}
	out.Reset()
	err := downloadCommand([]string{"--endpoint", endpoint, "--key", "shared", "--no-wait", "http://127.0.0.1:1/second"}, &out, &diagnostics)
	exit := assertExit(t, err, exitRefusedCall, "reused key")
	if !strings.Contains(exit.Error(), "key_conflict") {
		t.Fatalf("refusal did not name the contract's word: %v", exit)
	}
	if out.Len() != 0 {
		t.Fatalf("refusal wrote a result document: %s", out.Bytes())
	}
}

// Observing work that this program did not submit is a refusal, never an empty
// answer presented as an observation.
func TestJobsRefusesAnOperationOutsideThisScope(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	endpoint := serviceRuntime(t)
	var out bytes.Buffer
	err := jobsCommand([]string{"show", "absent-operation", "--endpoint", endpoint, "--json"}, &out, io.Discard)
	assertExit(t, err, exitRefusedCall, "absent operation")
	if out.Len() != 0 {
		t.Fatalf("refusal wrote a document: %s", out.Bytes())
	}
	// An empty scope is reported as empty, and that is not a failure.
	var listed bytes.Buffer
	if err := jobsCommand([]string{"list", "--endpoint", endpoint}, &listed, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed.String(), "no work submitted by this program") {
		t.Fatalf("empty scope: %q", listed.String())
	}
}

// Without a resolvable runtime every service command refuses and names why. No
// command falls back to a local store.
func TestServiceCommandsRefuseWithoutARuntime(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "no-runtime-here")
	if os.PathSeparator == '\\' {
		absent = testPipe("oa-absent-runtime", "r")
	}
	for name, run := range map[string]func(io.Writer, io.Writer) error{
		"download": func(out, diag io.Writer) error {
			return downloadCommand([]string{"--endpoint", absent, "--timeout", "3s", "http://127.0.0.1:1/never"}, out, diag)
		},
		"jobs list": func(out, diag io.Writer) error {
			return jobsCommand([]string{"list", "--endpoint", absent, "--timeout", "3s"}, out, diag)
		},
		"jobs show": func(out, diag io.Writer) error {
			return jobsCommand([]string{"show", "--key", "k", "--endpoint", absent, "--timeout", "3s"}, out, diag)
		},
	} {
		var out, diagnostics bytes.Buffer
		err := run(&out, &diagnostics)
		exit := assertExit(t, err, exitNotResolved, name)
		if !strings.Contains(exit.Error(), "no runtime resolved") {
			t.Fatalf("%s: %v", name, exit)
		}
		if out.Len() != 0 {
			t.Fatalf("%s wrote output without a runtime: %s", name, out.Bytes())
		}
	}
}

// Usage refusals happen before anything is sent, so they need no runtime.
func TestDownloadAndJobsUsageRefusalsSendNothing(t *testing.T) {
	for _, args := range [][]string{
		{"ftp://example.invalid/thing"},
		{"http:///nohost"},
		{"https://user:secret@example.invalid/thing"},
		{"http://example.invalid/a", "http://example.invalid/b"},
		{"--sha256", "short", "http://example.invalid/a"},
		{"--size", "-1", "http://example.invalid/a"},
		{"--timeout", "-1s", "http://example.invalid/a"},
		{"--verify", "http://example.invalid/a"},
	} {
		var out bytes.Buffer
		assertExit(t, downloadCommand(args, &out, io.Discard), exitUsage, strings.Join(args, " "))
		if out.Len() != 0 {
			t.Fatalf("%v wrote %q", args, out.String())
		}
	}
	for _, args := range [][]string{
		{"show"},
		{"show", "id", "--key", "k"},
		{"show", "one", "two"},
		{"show", "--epoch", "e"},
		{"show", "id", "--attempt", "1"},
		{"show", "--key", "k", "--attempt", "-1"},
		{"result", "id"},
		{"result", "id", "--out", t.TempDir()},
		{"result", "id", "--out", "."},
		{"result", "id", "--out", "somewhere" + string(os.PathSeparator)},
		{"list", "unexpected"},
		{"unknown"},
	} {
		var out bytes.Buffer
		assertExit(t, jobsCommand(args, &out, io.Discard), exitUsage, strings.Join(args, " "))
		if out.Len() != 0 {
			t.Fatalf("%v wrote %q", args, out.String())
		}
	}
}

// Help is available for the new verbs, and it names migrate-legacy's separate
// command set rather than replacing it.
func TestDownloadAndJobsHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}} {
		var out bytes.Buffer
		if err := downloadCommand(args, &out, io.Discard); err != nil {
			t.Fatalf("download help %v: %v", args, err)
		}
		if !strings.Contains(out.String(), "openabstractions download <url>") {
			t.Fatalf("download help: %q", out.String())
		}
	}
	var out bytes.Buffer
	if err := jobsCommand([]string{"--help"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"list", "show", "cancel", "result", "migrate-legacy"} {
		if !strings.Contains(out.String(), word) {
			t.Fatalf("jobs help omitted %q: %s", word, out.String())
		}
	}
}

// Bytes that do not hash to the digest the caller named end the operation, and
// the command says so with exit 5. The runtime keeps the record failed rather
// than fetching the same wrong file again on the next sweep, so a second
// observation of the same work reports the same terminal answer.
func TestDownloadEndsFailedOnADigestMismatch(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	endpoint := serviceRuntime(t)
	source := served(t, []byte("the bytes this server actually holds"))
	// The digest of something else entirely: what the source sends can never
	// satisfy it, however many times it is fetched.
	wanted := sha256.Sum256([]byte("the bytes the caller asked for"))
	sink := t.TempDir()
	args := []string{source, "--endpoint", endpoint, "--out", sink, "--sha256", hex.EncodeToString(wanted[:]), "--json", "--timeout", "90s"}

	var out, diagnostics bytes.Buffer
	exit := assertExit(t, downloadCommand(args, &out, &diagnostics), exitEnded, "download with a wrong digest")
	if !strings.Contains(exit.Error(), "failed") || !strings.Contains(exit.Error(), "permanent") {
		t.Fatalf("exit 5 did not name a permanent failure: %v", exit)
	}
	if out.Len() != 0 {
		t.Fatalf("failed work wrote a result document: %s", out.String())
	}
	entries, err := os.ReadDir(sink)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed work left files behind: %v %v", entries, err)
	}

	// Terminal and left alone: the work is not adopted again behind the command.
	listed := runJSON(t, "jobs list", func(out, diag io.Writer) error {
		return jobsCommand([]string{"list", "--endpoint", endpoint, "--json"}, out, diag)
	})
	if len(listed.Snapshots) != 1 {
		t.Fatalf("this program's scope holds %d operations, want the one that failed", len(listed.Snapshots))
	}
	if s := listed.Snapshots[0]; s.State != "failed" || s.Failure == nil || s.Failure.Classification != "permanent" {
		t.Fatalf("the mismatch did not stay failed: %+v", s)
	}
	// The contract's own word for why it ended, not only that it ended.
	if cause := listed.Snapshots[0].Failure.Cause; cause != "digest_mismatch" {
		t.Fatalf("the failure document lost its typed cause: %q", cause)
	}
	var shown bytes.Buffer
	if err := jobsCommand([]string{"list", "--endpoint", endpoint}, &shown, &diagnostics); err != nil {
		t.Fatalf("jobs list: %v", err)
	}
	if !strings.Contains(shown.String(), "digest_mismatch") {
		t.Fatalf("the human form lost the typed cause: %s", shown.String())
	}
}

// The default key is a function of the submission, so an equal command line
// names one identity and a different one names another.
func TestSubmissionKeyFollowsTheSubmission(t *testing.T) {
	source, name, err := downloadSource("https://example.invalid/dir/thing.bin")
	if err != nil || name != "thing.bin" || source.Scheme != "https" {
		t.Fatalf("%+v %q %v", source, name, err)
	}
	if _, unnamed, err := downloadSource("https://example.invalid"); err != nil || unnamed != "download.bin" {
		t.Fatalf("%q %v", unnamed, err)
	}
	artifact, err := downloadArtifact(strings.Repeat("AB", 32), 7)
	if err != nil || artifact.Digest != "sha256:"+strings.Repeat("ab", 32) || artifact.Size != 7 {
		t.Fatalf("%+v %v", artifact, err)
	}
	build := func(locator string) string {
		return submissionKey(submissionFor(t, locator))
	}
	first, second := build("https://example.invalid/a"), build("https://example.invalid/a")
	if first != second || !strings.HasPrefix(first, "download-") || len(first) != len("download-")+32 {
		t.Fatalf("unstable or malformed key: %q %q", first, second)
	}
	if build("https://example.invalid/b") == first {
		t.Fatal("different sources produced one identity")
	}
}

// A failed attempt stays failed, and a retry is a numbered attempt of the same
// request that only the caller asks for (JOB-A7). The runtime judges whether
// the next attempt is eligible; the command only chooses which one to present.
func TestDownloadRetrySubmitsTheNextAttempt(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	endpoint := serviceRuntime(t)
	body := []byte("the bytes the caller asked for")
	sum := sha256.Sum256(body)
	var repaired atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := []byte("bytes the source held before it was repaired")
		if repaired.Load() {
			answer = body
		}
		if _, err := w.Write(answer); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	source := server.URL + "/thing.bin"
	sink := t.TempDir()
	args := []string{source, "--endpoint", endpoint, "--out", sink, "--sha256", hex.EncodeToString(sum[:]), "--json", "--timeout", "90s"}
	run := func(extra ...string) (decoded, error) {
		var out bytes.Buffer
		err := downloadCommand(append(slices.Clone(args), extra...), &out, io.Discard)
		var document decoded
		if err == nil {
			if jerr := json.Unmarshal(out.Bytes(), &document); jerr != nil {
				t.Fatalf("%v: %s", jerr, out.Bytes())
			}
		}
		return document, err
	}

	_, err := run()
	exit := assertExit(t, err, exitEnded, "attempt 0 against a broken source")
	if !strings.Contains(exit.Error(), "--retry submits attempt 1") {
		t.Fatalf("the failure did not say how to retry: %v", exit)
	}
	// The source is repaired, and still nothing retries without being asked.
	repaired.Store(true)
	_, err = run()
	assertExit(t, err, exitEnded, "the same command after the repair")

	retried, err := run("--retry")
	if err != nil {
		t.Fatalf("--retry: %v", err)
	}
	if retried.Snapshot == nil || retried.Snapshot.State != "complete" || retried.Receipt.Identity.Attempt == nil || *retried.Receipt.Identity.Attempt != 1 {
		t.Fatalf("--retry did not complete attempt 1: %+v %+v", retried.Receipt, retried.Snapshot)
	}
	if delivered, err := os.ReadFile(filepath.Join(sink, "thing.bin")); err != nil || !bytes.Equal(delivered, body) {
		t.Fatalf("attempt 1 delivered different bytes: %v", err)
	}
	key := retried.Receipt.Identity.Key

	// Both attempts are this program's work, each its own operation.
	listed := runJSON(t, "jobs list", func(out, diag io.Writer) error {
		return jobsCommand([]string{"list", "--endpoint", endpoint, "--json"}, out, diag)
	})
	states := map[int64]string{}
	for _, s := range listed.Snapshots {
		if s.Receipt.Identity.Key == key {
			states[s.Receipt.Identity.Attempt] = s.State
		}
	}
	if len(states) != 2 || states[0] != "failed" || states[1] != "complete" {
		t.Fatalf("attempts of %s: %v", key, states)
	}

	// A key names its latest attempt unless an attempt is given.
	latest := runJSON(t, "jobs show --key", func(out, diag io.Writer) error {
		return jobsCommand([]string{"show", "--key", key, "--endpoint", endpoint, "--json"}, out, diag)
	})
	if latest.Snapshot == nil || latest.Snapshot.State != "complete" || latest.Snapshot.Receipt.Identity.Attempt != 1 {
		t.Fatalf("--key without --attempt: %+v", latest.Snapshot)
	}
	original := runJSON(t, "jobs show --attempt 0", func(out, diag io.Writer) error {
		return jobsCommand([]string{"show", "--key", key, "--attempt", "0", "--endpoint", endpoint, "--json"}, out, diag)
	})
	if original.Snapshot == nil || original.Snapshot.State != "failed" {
		t.Fatalf("--attempt 0: %+v", original.Snapshot)
	}

	// The same command line now returns the latest attempt's receipt.
	again, err := run()
	if err != nil || again.Receipt.OperationID != retried.Receipt.OperationID {
		t.Fatalf("rerun after the retry: %v, %s then %s", err, retried.Receipt.OperationID, again.Receipt.OperationID)
	}
	// A complete attempt makes the next one ineligible, and the runtime says so.
	_, err = run("--retry")
	exit = assertExit(t, err, exitRefusedCall, "--retry after a complete attempt")
	if !strings.Contains(exit.Error(), "invalid") || !strings.Contains(exit.Error(), "attempt 1 is complete") {
		t.Fatalf("the ineligible retry did not name why: %v", exit)
	}
}
