package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
)

// changeGate is one runtime of the shipped composition with the change gate
// on (rights RIGHTS-O17, asks ASK-O17). This test process is the command
// line: the runtime's own image, an operator program. panel makes its calls
// the Panel window's, the person's own clicks; elevated makes its token read
// elevated. Both are the only injections: the policy, the asks Book, the
// operator services and the transport are the runtime's own.
type changeGate struct {
	options  runtimeFlags
	panel    atomic.Bool
	elevated atomic.Bool
}

func startChangeGate(t *testing.T, lifetime time.Duration) *changeGate {
	t.Helper()
	g := &changeGate{}
	g.options = gatedRuntimeWith(t, func(o *runtimeFlags) {
		o.composedRights = func(r *runtimeRights) {
			r.operatorDirect = func(*identity.Peer) bool { return g.panel.Load() }
			r.peerElevated = func(*identity.Peer) bool { return g.elevated.Load() }
			r.composedChanges = func(c *rightsChanges) {
				if lifetime > 0 {
					c.lifetime = lifetime
				}
			}
		}
	})
	return g
}

// rights runs one rights command as the command line.
func (g *changeGate) rights(t *testing.T, args ...string) (rightsReply, error) {
	t.Helper()
	return runRightsCommand(t, append(args, "--endpoint", g.options.endpoint)...)
}

// grantArgs grants (or with extra, denies) the probe the download job.
func (g *changeGate) grantArgs(program string, extra ...string) []string {
	return append([]string{"grant", "--program", program, "--action", host.JobSubmitAction, "--resource", host.JobKindResource("download")}, extra...)
}

// rule reads the probe's download rule: found or unknown.
func (g *changeGate) rule(t *testing.T, program string) rightsReply {
	t.Helper()
	read, err := g.rights(t, "read", "--program", program, "--action", host.JobSubmitAction, "--resource", host.JobKindResource("download"))
	if err != nil && read.Outcome != "unknown" {
		t.Fatalf("read the rule: %v %+v", err, read)
	}
	return read
}

// asPanel answers one question as the person in the Panel window.
func (g *changeGate) asPanel(t *testing.T, id, option string) askclient.OperatorDecision {
	t.Helper()
	g.panel.Store(true)
	defer g.panel.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	operator, err := client.New(g.options.endpoint).ResolveAsksOperator(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := operator.AnswerQuestionIfPendingContext(ctx, id, option)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

// question reads the person's question for id as the Panel lists it.
func (g *changeGate) question(t *testing.T, id string) askclient.RecordMetadata {
	t.Helper()
	g.panel.Store(true)
	defer g.panel.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	operator, err := client.New(g.options.endpoint).ResolveAsksOperator(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	records, err := operator.ListAllQuestionsContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.ID == id {
			return record
		}
	}
	t.Fatalf("no question %s", id)
	return askclient.RecordMetadata{}
}

// activity returns the Activity lines the runtime wrote to its log.
func (g *changeGate) activity(t *testing.T, want int) []map[string]string {
	t.Helper()
	path := filepath.Join(g.options.stateDir, "logging", "records.jsonl")
	var lines []map[string]string
	deadline := time.Now().Add(10 * time.Second)
	for {
		lines = nil
		if data, err := os.ReadFile(path); err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(data))
			scanner.Buffer(make([]byte, 1<<20), 1<<20)
			for scanner.Scan() {
				var record struct {
					Msg   string            `json:"msg"`
					Attrs map[string]string `json:"attrs"`
				}
				if json.Unmarshal(scanner.Bytes(), &record) == nil && record.Attrs["oa_event"] == rightsChangeEvent {
					record.Attrs["msg"] = record.Msg
					lines = append(lines, record.Attrs)
				}
			}
		}
		if len(lines) >= want || time.Now().After(deadline) {
			return lines
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func exitCode(err error) int {
	var exit *exitError
	if errors.As(err, &exit) {
		return exit.code
	}
	return -1
}

// An unelevated command-line grant is a pending request: nothing changes
// until the person allows it in the runtime's popup, the command line cannot
// answer that popup itself, and Allow applies exactly that change, once,
// with an Activity line and its Undo. Not now changes nothing.
func TestCommandLineGrantWaitsForThePerson(t *testing.T) {
	g := startChangeGate(t, 0)
	bin := probeBinary(t)

	pending, err := g.rights(t, append(g.grantArgs(bin), "--wait", "0s")...)
	if exitCode(err) != exitWaiting || pending.Outcome != "pending" || pending.Request == "" || pending.Expires == "" {
		t.Fatalf("an unelevated grant did not wait for the person: %v %+v", err, pending)
	}
	if read := g.rule(t, bin); read.Outcome != "unknown" {
		t.Fatalf("a pending grant changed the policy: %+v", read)
	}
	question := g.question(t, pending.Request)
	if question.Key != "rights.change" || !strings.HasPrefix(question.Text, "Let the command line give ") || question.Option != "" {
		t.Fatalf("the person's question: %+v", question)
	}

	// questions answer is not an answer path for an unelevated command line.
	var out, diagnostics bytes.Buffer
	err = questionsCommand([]string{"answer", pending.Request, "allow", "--endpoint", g.options.endpoint}, &out, &diagnostics)
	if exitCode(err) != exitRefusedCall || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("the command line answered its own request: %v %s%s", err, out.String(), diagnostics.String())
	}
	if read := g.rule(t, bin); read.Outcome != "unknown" {
		t.Fatalf("a refused answer changed the policy: %+v", read)
	}
	if again := g.question(t, pending.Request); again.Option != "" {
		t.Fatalf("a refused answer was recorded: %+v", again)
	}

	// The same request again joins the pending one.
	joined, err := g.rights(t, append(g.grantArgs(bin), "--wait", "0s")...)
	if exitCode(err) != exitWaiting || joined.Request != pending.Request {
		t.Fatalf("a repeated request made a second question: %v %+v", err, joined)
	}

	// The person allows it in the popup.
	if decision := g.asPanel(t, pending.Request, "allow"); decision.Outcome != askclient.OperatorDecisionOutcomeAnswered {
		t.Fatalf("the person's Allow: %+v", decision)
	}
	var follow bytes.Buffer
	err = rightsCommand([]string{"requests", "--follow", pending.Request, "--wait", "10s", "--endpoint", g.options.endpoint, "--json"}, &follow, &diagnostics)
	var followed requestsReply
	if json.Unmarshal(follow.Bytes(), &followed) != nil || err != nil || followed.Outcome != "applied" || len(followed.Requests) != 1 || followed.Requests[0].Exempt {
		t.Fatalf("the allowed request: %v %s", err, follow.String())
	}
	read := g.rule(t, bin)
	if read.Outcome != "found" || read.Record == nil || !read.Record.Rule.Permit || read.Record.SetBy.Program != probeSelf().Program {
		t.Fatalf("the allowed grant: %+v", read)
	}
	lines := g.activity(t, 1)
	if len(lines) != 1 || lines[0]["exempt"] != "false" || lines[0]["requester"] != "the command line" || lines[0]["undo"] == "" || !strings.HasPrefix(lines[0]["change"], "give ") {
		t.Fatalf("the Activity line of an allowed change: %+v", lines)
	}
	var undo changeUndo
	if err := json.Unmarshal([]byte(lines[0]["undo"]), &undo); err != nil || len(undo.Changes) != 1 || undo.Changes[0].Rule == nil || undo.After == nil || !undo.After.Present {
		t.Fatalf("the Undo of an allowed grant: %v %+v", err, undo)
	}

	// Not now changes nothing: a revoke the person refuses leaves the rule.
	revoke, err := g.rights(t, "revoke", "--program", bin, "--action", host.JobSubmitAction, "--resource", host.JobKindResource("download"), "--wait", "0s")
	if exitCode(err) != exitWaiting || revoke.Outcome != "pending" {
		t.Fatalf("an unelevated revoke: %v %+v", err, revoke)
	}
	g.asPanel(t, revoke.Request, "not_now")
	follow.Reset()
	err = rightsCommand([]string{"requests", "--follow", revoke.Request, "--wait", "10s", "--endpoint", g.options.endpoint, "--json"}, &follow, &diagnostics)
	if exitCode(err) != exitRefusedCall || !strings.Contains(follow.String(), `"refused"`) {
		t.Fatalf("a refused revoke: %v %s", err, follow.String())
	}
	if read := g.rule(t, bin); read.Outcome != "found" {
		t.Fatalf("a refused revoke removed the rule: %+v", read)
	}
}

// A command line whose own token is elevated is exempt while the
// elevated-callers default is on: its change applies at once and writes an
// Activity line with Undo. Switched off, it asks again.
func TestElevatedCommandLineIsExemptUntilTheSwitchIsOff(t *testing.T) {
	g := startChangeGate(t, 0)
	bin := probeBinary(t)
	g.elevated.Store(true)

	applied, err := g.rights(t, append(g.grantArgs(bin, "--deny"), "--wait", "0s")...)
	if err != nil || applied.Outcome != "applied" || applied.Request == "" {
		t.Fatalf("an elevated grant was not exempt: %v %+v", err, applied)
	}
	if read := g.rule(t, bin); read.Outcome != "found" || read.Record.Rule.Permit {
		t.Fatalf("the exempt deny: %+v", read)
	}
	lines := g.activity(t, 1)
	if len(lines) != 1 || lines[0]["exempt"] != "true" || lines[0]["msg"] != "Rights changed by the command line running as administrator" || lines[0]["undo"] == "" {
		t.Fatalf("the exempt change's Activity line: %+v", lines)
	}
	// The exempt change is listed among this program's requests.
	var out, diagnostics bytes.Buffer
	if err := rightsCommand([]string{"requests", "--endpoint", g.options.endpoint, "--json"}, &out, &diagnostics); err != nil || !strings.Contains(out.String(), `"exempt":true`) {
		t.Fatalf("the exempt change is not listed: %v %s", err, out.String())
	}

	// The person turns the switch off in the Panel.
	g.panel.Store(true)
	off, err := g.rights(t, "defaults", "--off", rights.DefaultElevatedCallers)
	g.panel.Store(false)
	if err != nil || off.Outcome != "applied" {
		t.Fatalf("switching elevated-callers off: %v %+v", err, off)
	}
	pending, err := g.rights(t, "revoke", "--program", bin, "--action", host.JobSubmitAction, "--resource", host.JobKindResource("download"), "--wait", "0s")
	if exitCode(err) != exitWaiting || pending.Outcome != "pending" {
		t.Fatalf("with the switch off, an elevated revoke did not ask: %v %+v", err, pending)
	}
	if read := g.rule(t, bin); read.Outcome != "found" {
		t.Fatalf("a pending revoke changed the policy: %+v", read)
	}
	if n := len(g.activity(t, 1)); n != 1 {
		t.Fatalf("a pending change wrote an Activity line: %d", n)
	}
}

// A request nobody answers expires after its lifetime: the question goes,
// a late Allow does nothing, and the policy never changes.
func TestUnansweredChangeRequestExpires(t *testing.T) {
	g := startChangeGate(t, 1500*time.Millisecond)
	bin := probeBinary(t)

	pending, err := g.rights(t, append(g.grantArgs(bin), "--wait", "0s")...)
	if exitCode(err) != exitWaiting || pending.Outcome != "pending" {
		t.Fatalf("an unelevated grant: %v %+v", err, pending)
	}
	var follow, diagnostics bytes.Buffer
	err = rightsCommand([]string{"requests", "--follow", pending.Request, "--wait", "10s", "--endpoint", g.options.endpoint, "--json"}, &follow, &diagnostics)
	if exitCode(err) != exitRefusedCall || !strings.Contains(follow.String(), `"expired"`) {
		t.Fatalf("an unanswered request did not expire: %v %s", err, follow.String())
	}
	if decision := g.asPanel(t, pending.Request, "allow"); decision.Outcome == askclient.OperatorDecisionOutcomeAnswered {
		t.Fatalf("an expired request's question was answered: %+v", decision)
	}
	time.Sleep(200 * time.Millisecond)
	if read := g.rule(t, bin); read.Outcome != "unknown" {
		t.Fatalf("an expired request changed the policy: %+v", read)
	}
	if n := len(g.activity(t, 0)); n != 0 {
		t.Fatalf("an expired request wrote an Activity line: %d", n)
	}
}
