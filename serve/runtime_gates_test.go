package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

var gatedRuntimeSeq atomic.Int64

// gatedRuntime starts the shipped runtime composition in this process, as
// `serve runtime --isolated <name> --state-dir <dir>` selects it, with no local
// inference hosts. The test executable is the runtime and its operator program.
func gatedRuntime(t *testing.T) runtimeFlags {
	t.Helper()
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on this transport")
	}
	state, err := os.MkdirTemp("", "oa-gates-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	for dir, file := range map[string][2]string{
		"credentials": {credentialsBackendFile, "file-0600\n"},
		"inference":   {inferenceHostsFile, `{"local": []}`},
	} {
		if err := os.MkdirAll(filepath.Join(state, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, dir, file[0]), []byte(file[1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	name := fmt.Sprintf("gates-%d-%d", os.Getpid(), gatedRuntimeSeq.Add(1))
	options, err := parseRuntime([]string{"--isolated", name, "--state-dir", state}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
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
	return options
}

// probeBinary builds the serve image as openabstractions-probe: a second
// program, and a second rights subject, beside the test executable.
func probeBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), probeClientName)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", probeClientName, err, out)
	}
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// probeAs runs the probe binary against the runtime and returns its one result
// and exit code.
func probeAs(t *testing.T, bin string, options runtimeFlags, args ...string) (probeResult, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bin, append(args, "--endpoint", options.endpoint, "--runtime-program", filepath.Clean(exe), "--json")...)
	var out, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostics
	err = command.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var result probeResult
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
		t.Fatalf("probe %v printed %q (%s): %v", args, out.String(), diagnostics.String(), decodeErr)
	}
	return result, code
}

// gateCase is one gated call of the shipped runtime, its rule, and the outcome
// the call reads when the rule permits it.
type gateCase struct {
	args             []string
	action, resource string
	allowed          string
}

// shippedGates are the gated calls the shipped runtime composes, each made by a
// probe. Model lookup names a revision hf refuses as invalid after the gate, so
// no registry is reached.
func shippedGates(download string) []gateCase {
	return []gateCase{
		{[]string{"config", "rewrite"}, host.ConfigEditAction, host.ConfigEditResource, "applied"},
		{[]string{"logging", "history"}, host.LogHistoryAction, host.LogHistoryResource, "page"},
		{[]string{"model", "resolve", "hf:org/model@not/a/revision"}, host.ModelLookupAction, "hf", "invalid"},
		{[]string{"router", "hosts"}, routerservice.ActionInventory, routerservice.ResourceInventory, "listed"},
		{[]string{"router", "pick", "probe-model"}, routerservice.ActionRoute, host.RoutesResource, "not-here"},
		{[]string{"jobs", "submit", download}, host.JobSubmitAction, host.JobResource, "accepted"},
	}
}

// For every gated call the shipped runtime composes, a second program is
// refused with no rule, served after a grant with the rule and its setter on
// record, refused after the revoke, and refused under a deny, with its own
// decision reading not_granted, permitted, not_granted and denied. With the
// policy file removed, every gated call is unavailable while config reads and
// the job inventory still answer.
func TestShippedRuntimeGatesEachActionByItsRule(t *testing.T) {
	options := gatedRuntime(t)
	bin := probeBinary(t)
	payload := []byte("gated by a rights rule\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(payload) }))
	defer server.Close()
	self := probeSelf()
	edit := func(command string, c gateCase, extra ...string) {
		t.Helper()
		reply, err := runRightsCommand(t, append([]string{command, "--endpoint", options.endpoint, "--program", bin, "--action", c.action, "--resource", c.resource}, extra...)...)
		if err != nil || reply.Outcome != "applied" {
			t.Fatalf("rights %s %s: %v %+v", command, c.action, err, reply)
		}
	}
	decide := func(c gateCase) string {
		t.Helper()
		d, _ := probeAs(t, bin, options, "rights", "decide", c.action, c.resource)
		return d.Outcome
	}
	for _, c := range shippedGates(server.URL + "/payload.bin") {
		name := strings.Join(c.args[:2], " ")
		refused, code := probeAs(t, bin, options, c.args...)
		if refused.Outcome != "forbidden" || code != exitRefusedCall || refused.Rule == nil || refused.Rule.Action != c.action || refused.Rule.Resource != c.resource || refused.Subject.Program != bin {
			t.Fatalf("%s with no rule: exit %d %+v", name, code, refused)
		}
		if d := decide(c); d != "not_granted" {
			t.Fatalf("%s decision with no rule: %s", name, d)
		}
		edit("grant", c, "--why", "gate test")
		if allowed, _ := probeAs(t, bin, options, c.args...); allowed.Outcome != c.allowed {
			t.Fatalf("%s after a grant: %+v", name, allowed)
		}
		read, err := runRightsCommand(t, "read", "--endpoint", options.endpoint, "--program", bin, "--action", c.action, "--resource", c.resource)
		if err != nil || read.Outcome != "found" || read.Record == nil || read.Record.SetBy.Program != self.Program || read.Record.Why != "gate test" {
			t.Fatalf("%s rule on record: %v %+v", name, err, read)
		}
		if d := decide(c); d != "permitted" {
			t.Fatalf("%s decision after a grant: %s", name, d)
		}
		edit("revoke", c)
		if again, code := probeAs(t, bin, options, c.args...); again.Outcome != "forbidden" || code != exitRefusedCall {
			t.Fatalf("%s after a revoke: exit %d %+v", name, code, again)
		}
		if d := decide(c); d != "not_granted" {
			t.Fatalf("%s decision after a revoke: %s", name, d)
		}
		edit("grant", c, "--deny")
		if denied, _ := probeAs(t, bin, options, c.args...); denied.Outcome != "forbidden" {
			t.Fatalf("%s under a deny: %+v", name, denied)
		}
		if d := decide(c); d != "denied" {
			t.Fatalf("%s decision under a deny: %s", name, d)
		}
		edit("grant", c)
	}

	policy := filepath.Join(options.stateDir, "rights", "decisions.json")
	if err := os.Rename(policy, policy+".moved"); err != nil {
		t.Fatal(err)
	}
	unavailable := map[string]string{"config rewrite": "unavailable", "logging history": "policy_unavailable", "model resolve": "unavailable",
		"router hosts": "policy_unavailable", "router pick": "policy_unavailable", "jobs submit": "unavailable"}
	for _, c := range shippedGates(server.URL + "/other.bin") {
		name := strings.Join(c.args[:2], " ")
		if outage, _ := probeAs(t, bin, options, c.args...); outage.Outcome != unavailable[name] {
			t.Fatalf("%s during a policy outage: %+v", name, outage)
		}
	}
	for args, want := range map[string]string{"config read": "read", "config user": "read", "jobs inventory": "page"} {
		if open, code := probeAs(t, bin, options, strings.Fields(args)...); open.Outcome != want || code != 0 {
			t.Fatalf("%s during a policy outage: exit %d %+v", args, code, open)
		}
	}
}

// A second program neither lists nor cancels the command line's download until
// it holds the rules: account-wide inventory under inventory.read, and the
// cancellation under acceptance.cancel. The command line, an operator program,
// lists and observes the same work, and its own job show sees the intent.
func TestProbeCancelsTheCommandLinesJobOnlyUnderTheCancelRule(t *testing.T) {
	options := gatedRuntime(t)
	bin := probeBinary(t)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("the first bytes of a slow download"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	receipt := runJSON(t, "download", func(out, diagnostics io.Writer) error {
		return downloadCommand([]string{server.URL + "/slow.bin", "--no-wait", "--label", "the command line's download", "--endpoint", options.endpoint, "--json"}, out, diagnostics)
	})
	operation := receipt.Receipt.OperationID
	if operation == "" {
		t.Fatalf("download receipt %+v", receipt)
	}
	grant := func(action string) {
		t.Helper()
		reply, err := runRightsCommand(t, "grant", "--endpoint", options.endpoint, "--program", bin, "--action", action, "--resource", host.JobResource)
		if err != nil || reply.Outcome != "applied" {
			t.Fatalf("grant %s: %v %+v", action, err, reply)
		}
	}

	if own, code := probeAs(t, bin, options, "jobs", "inventory"); own.Outcome != "page" || code != 0 || strings.Contains(fmt.Sprint(own.Summary), "command line") {
		t.Fatalf("the probe's own inventory: exit %d %+v", code, own)
	}
	if all, code := probeAs(t, bin, options, "jobs", "all"); all.Outcome != "forbidden" || code != exitRefusedCall {
		t.Fatalf("account inventory with no rule: exit %d %+v", code, all)
	}
	grant(host.JobInventoryAction)
	if all, code := probeAs(t, bin, options, "jobs", "all"); all.Outcome != "page" || code != 0 || !strings.Contains(fmt.Sprint(all.Summary), operation) {
		t.Fatalf("account inventory under its rule: exit %d %+v", code, all)
	}
	if cancel, code := probeAs(t, bin, options, "jobs", "cancel", operation); cancel.Outcome != "forbidden" || code != exitRefusedCall {
		t.Fatalf("cancellation with no rule: exit %d %+v", code, cancel)
	}
	shown := runJSON(t, "jobs show", func(out, diagnostics io.Writer) error {
		return jobsCommand([]string{"show", operation, "--endpoint", options.endpoint, "--json"}, out, diagnostics)
	})
	if shown.Snapshot == nil || shown.Snapshot.State == "cancelled" {
		t.Fatalf("a refused cancellation changed the work %+v", shown.Snapshot)
	}
	grant(host.JobCancelAction)
	if cancel, code := probeAs(t, bin, options, "jobs", "cancel", operation); cancel.Outcome != "requested" || code != 0 {
		t.Fatalf("cancellation under its rule: exit %d %+v", code, cancel)
	}
	var listed bytes.Buffer
	if err := jobsCommand([]string{"list", "--all", "--endpoint", options.endpoint}, &listed, io.Discard); err != nil || !strings.Contains(listed.String(), operation) {
		t.Fatalf("jobs list --all as the operator: %v %q", err, listed.String())
	}
	deadline := time.Now().Add(runtimeWait)
	for {
		shown = runJSON(t, "jobs show", func(out, diagnostics io.Writer) error {
			return jobsCommand([]string{"show", operation, "--endpoint", options.endpoint, "--json"}, out, diagnostics)
		})
		if shown.Snapshot != nil && shown.Snapshot.State == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the submitter never saw its work cancelled: %+v", shown.Snapshot)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var again bytes.Buffer
	err := jobsCommand([]string{"cancel", "--all", operation, "--endpoint", options.endpoint}, &again, io.Discard)
	if err != nil || !strings.Contains(again.String(), "already_terminal") {
		t.Fatalf("jobs cancel --all after the end: %v %q", err, again.String())
	}
}

// rights grant --for writes each exact rule of a bundle with its reason, and
// the program's gated calls then pass; the rules outside the bundle stay
// ungranted. A stale revision stops the bundle before any rule lands, and usage
// mistakes send nothing.
func TestRightsGrantForBundlesWritesEachExactRule(t *testing.T) {
	options := gatedRuntime(t)
	bin := probeBinary(t)
	payload := []byte("allowed by a bundle\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(payload) }))
	defer server.Close()
	bundle := func(want int, args ...string) bundleReply {
		t.Helper()
		var out bytes.Buffer
		err := rightsCommand(append([]string{"grant", "--endpoint", options.endpoint, "--program", bin, "--json"}, args...), &out, io.Discard)
		if want == 0 && err != nil {
			t.Fatalf("grant %v: %v %s", args, err, out.String())
		}
		if want != 0 {
			assertExit(t, err, want, strings.Join(args, " "))
		}
		var reply bundleReply
		if out.Len() > 0 {
			if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
				t.Fatalf("grant %v printed %q: %v", args, out.String(), err)
			}
		}
		return reply
	}
	for _, usage := range [][]string{
		{"--for", "everything"}, {"--for", "inference"}, {"--for", "downloads", "--host", "a"},
		{"--for", "downloads", "--action", host.JobSubmitAction}, {"--for", "downloads", "--deny"}, {"--registry", "hf"},
	} {
		bundle(exitUsage, usage...)
	}
	if stale := bundle(exitRefusedCall, "--for", "downloads", "--registry", "hf", "--revision", "sha256:stale"); stale.Outcome != "conflict" || len(stale.Landed) != 0 || stale.Stopped == nil || stale.Stopped.Action != host.JobSubmitAction {
		t.Fatalf("stale revision %+v", stale)
	}
	if refused, _ := probeAs(t, bin, options, "jobs", "submit", server.URL+"/bundle.bin"); refused.Outcome != "forbidden" {
		t.Fatalf("submit before the bundle %+v", refused)
	}

	downloads := bundle(0, "--for", "downloads", "--registry", "hf")
	if downloads.Outcome != "applied" || len(downloads.Landed) != 2 || downloads.Why != "allow downloads" {
		t.Fatalf("downloads bundle %+v", downloads)
	}
	for _, r := range downloads.Landed {
		read, err := runRightsCommand(t, "read", "--endpoint", options.endpoint, "--program", bin, "--action", r.Action, "--resource", r.Resource)
		if err != nil || read.Outcome != "found" || read.Record == nil || read.Record.Why != "allow downloads" || !read.Record.Rule.Permit {
			t.Fatalf("bundle rule %+v on record: %v %+v", r, err, read)
		}
	}
	if accepted, _ := probeAs(t, bin, options, "jobs", "submit", server.URL+"/bundle.bin"); accepted.Outcome != "accepted" {
		t.Fatalf("submit under the bundle %+v", accepted)
	}
	if hf, _ := probeAs(t, bin, options, "model", "resolve", "hf:org/model@not/a/revision"); hf.Outcome != "invalid" {
		t.Fatalf("hf lookup under the bundle %+v", hf)
	}
	if ollama, _ := probeAs(t, bin, options, "model", "resolve", "ollama:library/model@not/a/revision"); ollama.Outcome != "forbidden" {
		t.Fatalf("a registry outside the bundle %+v", ollama)
	}

	inference := bundle(0, "--for", "inference", "--host", "openrouter", "--credential", "openrouter", "--why", "chat for the probe")
	if inference.Outcome != "applied" || len(inference.Landed) != 3 || inference.Landed[1].Resource != "host:openrouter" || inference.Landed[2].Resource != "credential:openrouter" {
		t.Fatalf("inference bundle %+v", inference)
	}
	if pick, _ := probeAs(t, bin, options, "router", "pick", "probe-model"); pick.Outcome == "forbidden" {
		t.Fatalf("route under the inference bundle %+v", pick)
	}
	if hosts, _ := probeAs(t, bin, options, "router", "hosts"); hosts.Outcome != "forbidden" {
		t.Fatalf("router inventory outside the bundle %+v", hosts)
	}
}

// pendingFirstUse lists the pending first-use questions through the operator.
func pendingFirstUse(t *testing.T, options runtimeFlags) []askclient.RecordMetadata {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	operator, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.ListQuestionsContext(ctx, "", 64)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("questions %+v %v", page, err)
	}
	var pending []askclient.RecordMetadata
	for _, r := range page.Records {
		if r.Key == asks.FirstUseKey && r.Option == "" {
			pending = append(pending, r)
		}
	}
	return pending
}

// runtimeExpectation expects the in-process runtime: this account and this
// test executable.
func runtimeExpectation(t *testing.T) listen.ServerExpectation {
	t.Helper()
	who, _, err := principal()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return listen.ServerExpectation{Principal: who, Program: filepath.Clean(exe)}
}

// answerFirstUse answers one question as the operator and writes its rule, as
// the Panel does.
func answerFirstUse(t *testing.T, options runtimeFlags, id, option string) grants.Answered {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	questions, err := machine.ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	answered, err := grants.AnswerQuestion(ctx, questions, policy, probeSelf().Account, id, option)
	if err != nil || answered.Decision.Outcome.String() != "answered" {
		t.Fatalf("answer %s %s: %+v %v", id, option, answered, err)
	}
	return answered
}

// A refused spending call admits one first-use question and is refused at
// once. Allow writes the permit with why "asked at first use" and the retry is
// accepted; never writes an exact deny and admits no further question; refuse
// writes nothing, and the answered question admits no new one. An application
// asks only under question.ask and never with the runtime's own key.
func TestFirstUseAsksOnceAndTheAnswerWritesTheRule(t *testing.T) {
	options := gatedRuntime(t)
	bin := probeBinary(t)
	payload := []byte("asked at first use\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(payload) }))
	defer server.Close()
	submit := []string{"jobs", "submit", server.URL + "/first-use.bin"}

	if refused, _ := probeAs(t, bin, options, submit...); refused.Outcome != "forbidden" {
		t.Fatalf("first submit %+v", refused)
	}
	if again, _ := probeAs(t, bin, options, submit...); again.Outcome != "forbidden" {
		t.Fatalf("second submit %+v", again)
	}
	pending := pendingFirstUse(t, options)
	if len(pending) != 1 || pending[0].About != bin || !strings.Contains(pending[0].Text, " wants to "+host.JobSubmitAction+" on "+host.JobResource) {
		t.Fatalf("pending questions after two refusals %+v", pending)
	}
	allowed := answerFirstUse(t, options, pending[0].ID, "allow")
	if allowed.Edit == nil || allowed.Edit.Outcome.String() != "applied" || allowed.Rule == nil || !allowed.Rule.Permit || allowed.Rule.Subject.Program != bin {
		t.Fatalf("allow %+v", allowed)
	}
	read, err := runRightsCommand(t, "read", "--endpoint", options.endpoint, "--program", bin, "--action", host.JobSubmitAction, "--resource", host.JobResource)
	if err != nil || read.Outcome != "found" || read.Record.Why != grants.FirstUseWhy || !read.Record.Rule.Permit {
		t.Fatalf("rule written by allow %v %+v", err, read)
	}
	if accepted, _ := probeAs(t, bin, options, submit...); accepted.Outcome != "accepted" {
		t.Fatalf("retry after allow %+v", accepted)
	}

	lookup := []string{"model", "resolve", "hf:org/model@not/a/revision"}
	if refused, _ := probeAs(t, bin, options, lookup...); refused.Outcome != "forbidden" {
		t.Fatalf("lookup %+v", refused)
	}
	pending = pendingFirstUse(t, options)
	if len(pending) != 1 || !strings.Contains(pending[0].Text, host.ModelLookupAction+" on hf") {
		t.Fatalf("pending lookup question %+v", pending)
	}
	never := answerFirstUse(t, options, pending[0].ID, "never")
	if never.Edit == nil || never.Edit.Outcome.String() != "applied" || never.Rule.Permit {
		t.Fatalf("never %+v", never)
	}
	if denied, _ := probeAs(t, bin, options, lookup...); denied.Outcome != "forbidden" {
		t.Fatalf("lookup under never %+v", denied)
	}
	if d, _ := probeAs(t, bin, options, "rights", "decide", host.ModelLookupAction, "hf"); d.Outcome != "denied" {
		t.Fatalf("decision under never %+v", d)
	}
	if more := pendingFirstUse(t, options); len(more) != 0 {
		t.Fatalf("a deny admitted another question %+v", more)
	}

	ollama := []string{"model", "resolve", "ollama:library/model@not/a/revision"}
	probeAs(t, bin, options, ollama...)
	pending = pendingFirstUse(t, options)
	if len(pending) != 1 {
		t.Fatalf("pending ollama question %+v", pending)
	}
	refused := answerFirstUse(t, options, pending[0].ID, "refuse")
	if refused.Rule != nil || refused.Edit != nil {
		t.Fatalf("refuse wrote a rule %+v", refused)
	}
	if again, _ := probeAs(t, bin, options, ollama...); again.Outcome != "forbidden" {
		t.Fatalf("lookup after refuse %+v", again)
	}
	if more := pendingFirstUse(t, options); len(more) != 0 {
		t.Fatalf("a refused question admitted another %+v", more)
	}
	if read, _ := runRightsCommand(t, "read", "--endpoint", options.endpoint, "--program", bin, "--action", host.ModelLookupAction, "--resource", "ollama"); read.Outcome != "unknown" {
		t.Fatalf("refuse left a rule %+v", read)
	}

	// An application asks under question.ask, and never with the runtime's key.
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	app, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveAsks(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	question := askclient.ApplicationQuestion{RequestKey: "reach-example", Key: "download.reach", Slots: map[string]string{"host": "example.com"}}
	if o, err := app.AskContext(ctx, question); err != nil || o.Outcome.String() != "forbidden" {
		t.Fatalf("ask with no rule %+v %v", o, err)
	}
	if reply, err := runRightsCommand(t, "grant", "--endpoint", options.endpoint, "--program", probeSelf().Program, "--action", host.QuestionAskAction, "--resource", host.QuestionAskResource); err != nil || reply.Outcome != "applied" {
		t.Fatalf("grant question.ask %v %+v", err, reply)
	}
	if o, err := app.AskContext(ctx, question); err != nil || o.Outcome.String() != "pending" {
		t.Fatalf("ask under its rule %+v %v", o, err)
	}
	forged := askclient.ApplicationQuestion{RequestKey: "forged", Key: asks.FirstUseKey, Slots: map[string]string{"program": bin, "action": host.JobSubmitAction, "resource": host.JobResource}}
	if o, err := app.AskContext(ctx, forged); err != nil || o.Outcome.String() != "forbidden" {
		t.Fatalf("an application admitted the runtime's key %+v %v", o, err)
	}
}

// probe all makes no write call: the operator holds the config edit rule by
// installation, and its user settings file stays unwritten until a named
// rewrite.
func TestProbeAllMakesNoWriteCall(t *testing.T) {
	options := gatedRuntime(t)
	settings := filepath.Join(options.stateDir, "config", "config.json")
	results, err := runProbeCommand(t, "all", "--endpoint", options.endpoint)
	if err != nil || len(results) < 8 {
		t.Fatalf("probe all %v %+v", err, results)
	}
	for _, r := range results {
		if r.Operation == "rewrite" || r.Operation == "submit" {
			t.Fatalf("probe all ran %+v", r)
		}
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("probe all wrote the user settings: %v", err)
	}
	rewrite, err := runProbeCommand(t, "config", "rewrite", "--endpoint", options.endpoint)
	if err != nil || len(rewrite) != 1 || rewrite[0].Outcome != "applied" {
		t.Fatalf("named rewrite %v %+v", err, rewrite)
	}
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("named rewrite left no settings file: %v", err)
	}
}
