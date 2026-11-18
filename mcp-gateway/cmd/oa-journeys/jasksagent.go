//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-facade/go-core/childproc"
)

// J-ASKS-AGENT (memory "Assistant answers asks"; asks ASK-R5, ASK-R6, ASK-R14;
// S5 ruling 2026-11-22; owner design A 2026-11-22; sandboxing O6). Claude's
// connection asks to answer for the person and the person allows it. A peer
// message from OpenCode wakes Claude's chat: in that reply-only turn Claude
// may list questions and may not answer them. After the person's turn Claude
// answers OpenCode's routine picture questions; the person-only one is
// refused to it. Activity names Claude for each answer. In the rights
// playground, a rights change through the command line (design A) and App A's
// read of the dummy secret are person-only: Claude is not offered them and
// its answer is refused; the person allows and App A reads.
func init() {
	registerTimed(81, asksAgentJourney, func(*options) time.Duration { return 6 * time.Minute }, runAsksAgent)
}

const (
	asksAgentJourney         = "J-ASKS-AGENT"
	playgroundIsolatedPrefix = "rights-playground-"
	// playgroundProgram is examples/rights-playground, built by journeys.py.
	playgroundProgram = "rights-playground.exe"
)

func runAsksAgent(ctx context.Context, e *env, o *options, r *result) {
	if _, err := os.Stat(filepath.Join(e.bin, playgroundProgram)); err != nil {
		r.check("the rights playground is built", false, "%v", err)
		return
	}
	if !ensureJourneyImageProvider(ctx, e, r) {
		return
	}
	if err := writePicturePolicy(ctx, e, testImageModel); !r.check("picture policy written", err == nil, "%v", err) {
		return
	}
	claude, err := e.enrollAgent(ctx, "oa-claude-integration", "Claude")
	if !r.check("Claude's connection kept", err == nil, "%v", err) {
		return
	}
	opencode, err := e.enrollAgent(ctx, "oa-opencode-integration", "OpenCode")
	if !r.check("OpenCode's connection kept", err == nil, "%v", err) {
		return
	}
	ca, err := e.connect(ctx, claude, []string{"CLAUDE_CODE_SESSION_ID=" + newUUID()})
	if !r.check("Claude connected", err == nil, "%v", err) {
		return
	}
	defer ca.Close()
	adapterKey := newID() + newID()
	_, text, _, err := ca.call(ctx, "oa_claude_session_start", map[string]any{"chat_title": "Journey answers", "adapter_key": adapterKey})
	r.check("Claude's chat bound with the Mod's key", err == nil && strings.Contains(text, `"observed"`), "%s", clip(firstNonEmpty(text, errString(err)), 300))
	opened := false
	for i := 0; i < 5 && !opened; i++ {
		if i > 0 {
			time.Sleep(time.Second)
		}
		_, text, _, err = ca.call(ctx, "oa_messages_inbox_open", map[string]any{})
		opened = err == nil && strings.Contains(text, `"opened"`)
	}
	r.check("Claude's inbox open", opened, "%s", clip(firstNonEmpty(text, errString(err)), 300))
	ob, err := e.connect(ctx, opencode, []string{"OA_OPENCODE_HOST_CHAT_ID=" + newUUID()})
	if !r.check("OpenCode connected", err == nil, "%v", err) {
		return
	}
	defer ob.Close()
	// Codex asks for the picture. OpenCode may message Claude, which makes it
	// a party to Claude's answers (asks ASK-R9, the messaging pair).
	codex, err := e.enrollAgent(ctx, askerBase, "Codex")
	if !r.check("Codex's connection kept", err == nil, "%v", err) {
		return
	}
	cx, err := e.connect(ctx, codex, nil)
	if !r.check("Codex connected", err == nil, "%v", err) {
		return
	}
	defer cx.Close()
	defer func() { r.Calls = ca.calls + ob.calls + cx.calls }()

	// 1. Claude asks to answer; only the person allows it.
	mark := e.answers.mark()
	_, text, out, err := ca.call(ctx, "oa_asks_request_answering", map[string]any{})
	enrolled := err == nil && enrollmentPermitted(out)
	if !enrolled && err == nil {
		e.answers.waitAnswered(ctx, mark, 60*time.Second)
		_, text, out, err = ca.call(ctx, "oa_asks_request_answering", map[string]any{})
		enrolled = err == nil && enrollmentPermitted(out)
	}
	asked := false
	for _, q := range e.answers.since(mark) {
		asked = asked || strings.Contains(q.Text, "question.answer") || strings.Contains(q.Text, "answer routine questions")
	}
	r.check("the person was asked to let Claude answer", asked, "%d questions", len(e.answers.since(mark)))
	if !r.check("Claude may answer after the person's yes", enrolled, "%s", clip(firstNonEmpty(text, errString(err)), 300)) {
		return
	}

	// 2. OpenCode messages Claude; the person allows the direction.
	if !sendPeerMessage(ctx, e, r, ob, "Claude", "app:oa-claude-integration") {
		return
	}

	// 3. The person is away. Codex starts a picture and waits for answers.
	e.answers.pause()
	defer e.answers.resume()
	args := map[string]any{"prompt": "a lighthouse at dusk", "size": "auto"}
	pictures := make(chan submitOutcome, 1)
	go func() {
		res, text, st, err := cx.call(ctx, pictureToolName, cloneArgs(args))
		pictures <- submitOutcome{res, text, st, err}
	}()
	var first askRecord
	var watched map[string]any
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline) && first.ID == ""; time.Sleep(500 * time.Millisecond) {
		//unchecked: a poll; session.call logs the error and the check below reports a question never offered
		_, _, lout, _ := ca.call(ctx, "oa_asks_list_answerable", map[string]any{"limit": 32})
		if recs := askRecords(lout); len(recs) > 0 {
			first = recs[0]
		}
	}
	if !r.check("Claude is offered Codex's routine question", first.ID != "", "none within 60 s") {
		return
	}
	_, _, watched, werr := ca.call(ctx, "oa_messages_watch", map[string]any{"wait_ms": 0, "adapter_key": adapterKey})
	asksWake, _ := watched["asks_wake"].(string)
	r.check("Claude's watch carries a question wake key (ASK-R14)", werr == nil && asksWake != "", "%s %s", clip(fmt.Sprint(watched), 300), errString(werr))

	// 4. S5: OpenCode's message wakes Claude's chat; that turn is reply-only.
	wake, _ := watched["wake"].(string)
	_, text, reserved, err := ca.call(ctx, "oa_messages_watch", map[string]any{"wait_ms": 0, "adapter_key": adapterKey, "reserve_wake": wake})
	ticket, _ := reserved["wake_ticket"].(string)
	if r.check("the Mod reserves the message wake", wake != "" && ticket != "", "wake %q: %s", wake, clip(firstNonEmpty(text, errString(err)), 300)) {
		res, ltext, _, lerr := ca.call(ctx, "oa_asks_list_answerable", map[string]any{"limit": 32})
		r.check("a reply-only turn may list questions", lerr == nil && res != nil && !res.IsError && !strings.Contains(ltext, "This turn was started by"), "%s", clip(firstNonEmpty(ltext, errString(lerr)), 200))
		res, atext, aout, aerr := ca.call(ctx, "oa_asks_answer", map[string]any{"id": first.ID, "option": pick(first.Options, false)})
		r.check("a reply-only turn may not answer (S5: answering writes a rights rule)", aerr == nil && res != nil && res.IsError &&
			strings.Contains(atext, "This turn was started by a message from") && asksAnswerOutcome(aout) != askswire.OperatorDecisionOutcomeAnswered,
			"%s", clip(firstNonEmpty(atext, errString(aerr)), 300))
		res, rtext, _, rerr := ca.call(ctx, "oa_asks_request_answering", map[string]any{})
		r.check("a reply-only turn may not ask for answering rights", rerr == nil && res != nil && res.IsError && strings.Contains(rtext, "This turn was started by"), "%s", clip(firstNonEmpty(rtext, errString(rerr)), 200))
		_, ttext, _, terr := ca.call(ctx, "oa_messages_watch", map[string]any{"wait_ms": 0, "adapter_key": adapterKey, "turn": "person"})
		r.check("the person's turn ends the mark", terr == nil, "%s", clip(firstNonEmpty(ttext, errString(terr)), 200))
	}

	// 5. In a turn the person started, Claude answers the routine questions;
	// a question that stays with the person is refused to it.
	var answered []string
	done := map[string]bool{}
	notListed := map[string]int{}
	personOnly, refusal := "", ""
	accepted := false
	var last string
	for attempt := 0; attempt < 5 && !accepted; attempt++ {
		if attempt > 0 {
			go func() {
				res, text, st, err := cx.call(ctx, pictureToolName, cloneArgs(args))
				pictures <- submitOutcome{res, text, st, err}
			}()
		}
		var got submitOutcome
	wait:
		for {
			select {
			case got = <-pictures:
				break wait
			case <-time.After(1500 * time.Millisecond):
			case <-ctx.Done():
				got = submitOutcome{err: ctx.Err()}
				break wait
			}
			_, _, lout, lerr := ca.call(ctx, "oa_asks_list_answerable", map[string]any{"limit": 32})
			if lerr != nil {
				continue
			}
			listed := map[string]bool{}
			for _, rec := range askRecords(lout) {
				listed[rec.ID] = true
				if done[rec.ID] {
					continue
				}
				done[rec.ID] = true
				_, atext, aout, aerr := ca.call(ctx, "oa_asks_answer", map[string]any{"id": rec.ID, "option": pick(rec.Options, false)})
				effectText, _ := firstOf(aout, "effect", "Effect").(string)
				effect, _ := askswire.ParseResponderEffectOutcome(effectText)
				if r.check("Claude's answer applied: "+questionAction(rec.Text), aerr == nil && asksAnswerOutcome(aout) == askswire.OperatorDecisionOutcomeAnswered && effect == askswire.ResponderEffectOutcomeApplied,
					"%s", clip(firstNonEmpty(atext, errString(aerr)), 200)) {
					answered = append(answered, rec.ID)
				}
			}
			for _, q := range panelPending(ctx, e, codex.program) {
				if listed[q.ID] || done[q.ID] {
					continue
				}
				if notListed[q.ID]++; notListed[q.ID] < 3 {
					continue
				}
				done[q.ID] = true
				_, atext, aout, aerr := ca.call(ctx, "oa_asks_answer", map[string]any{"id": q.ID, "option": "allow"})
				personOnly = questionAction(q.Text)
				if outcome := asksAnswerOutcome(aout); aerr == nil && outcome == askswire.OperatorDecisionOutcomeForbidden {
					refusal = atext
				}
				e.answers.resume()
				e.answers.waitIDs(ctx, []string{q.ID}, 60*time.Second)
				e.answers.pause()
			}
		}
		if got.err != nil {
			last = got.err.Error()
			break
		}
		last = got.text
		if !permissionPending(got.text, got.st) {
			accepted = got.res != nil && !got.res.IsError && pictureStarted(got.text, got.st)
			break
		}
	}
	r.check("Codex's picture is made after Claude answered", accepted, "%s", clip(last, 300))
	r.check("Claude answered routine questions", len(answered) > 0, "%d answers", len(answered))
	r.check("a person-only question is refused to Claude with a sentence", refusal != "" && strings.Contains(refusal, "only the person may answer"),
		"%s -> %s", firstNonEmpty(personOnly, "(none arose)"), clip(refusal, 400))

	// 6. Activity names Claude for each of its answers.
	checkActivityNamesAgent(ctx, e, r, answered, claude)

	// 7. The rights playground: person-only questions from App A and from
	// the command line.
	e.answers.resume()
	runPlaygroundAsks(ctx, e, r, ca)
}

// sendPeerMessage sends one note from s to receiver, asking the person for the
// direction once.
func sendPeerMessage(ctx context.Context, e *env, r *result, s *session, receiver, receiverApp string) bool {
	for i := 0; i < 20; i++ {
		_, text, _, err := s.call(ctx, "oa_opencode_session_start", map[string]any{"chat_title": "Journey build"})
		if err == nil && strings.Contains(text, `"observed"`) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	//unchecked: session.call logs the error, and the send below reports whether this inbox can message
	_, _, _, _ = s.call(ctx, "oa_messages_inbox_open", map[string]any{})
	body := "please look at the picture job " + newID()[:6]
	var last string
	for step := 0; step < 6; step++ {
		_, text, out, err := s.call(ctx, "oa_messages_send", map[string]any{"receiver": receiver, "body": body})
		if err != nil {
			last = err.Error()
			break
		}
		last = text
		outcome, _ := out["outcome"].(string)
		switch {
		case outcome == "sent":
			return r.check("OpenCode's message to "+receiver+" sent", true, "")
		case strings.Contains(text, "request_access") || strings.Contains(text, "needs_permission") || outcome == "forbidden":
			mark := e.answers.mark()
			//unchecked: session.call logs the error, and the next send reports whether access was granted
			_, _, _, _ = s.call(ctx, "oa_messages_request_access", map[string]any{"receiver": receiverApp, "idempotency_key": "journey-asks-" + newID()[:8]})
			e.answers.waitAnswered(ctx, mark, 60*time.Second)
		default:
			time.Sleep(2 * time.Second)
		}
	}
	return r.check("OpenCode's message to "+receiver+" sent", false, "%s", clip(last, 300))
}

// checkActivityNamesAgent reads the Panel's Activity answers and requires a
// responder line naming the agent for each answer. This driver is the
// runtime's operator program, so the journey's Panel reads no app
// registrations (applications.go snapshot: application.read per app) and
// names the agent by its connection's file stem, as monitor/program_titles.go
// names an unregistered program.
func checkActivityNamesAgent(ctx context.Context, e *env, r *result, answered []string, who *agent) {
	names := []string{" by " + who.title, " by " + strings.TrimSuffix(filepath.Base(who.program), ".exe") + " (not registered)"}
	var page struct {
		Outcome string `json:"outcome"`
		Events  []struct {
			QuestionID string `json:"questionId"`
			Result     string `json:"result"`
			AnsweredBy string `json:"answeredBy"`
		} `json:"events"`
	}
	_, err := e.panel(ctx, "GET", "/activity/answers", nil, &page)
	named, lines := 0, []string{}
	for _, ev := range page.Events {
		lines = append(lines, ev.Result+" ["+ev.AnsweredBy+"]")
		for _, id := range answered {
			if ev.QuestionID == id && ev.AnsweredBy == "responder" && (strings.HasSuffix(ev.Result, names[0]) || strings.HasSuffix(ev.Result, names[1])) {
				named++
			}
		}
	}
	r.check("Activity names "+who.title+" for each of its answers", err == nil && len(answered) > 0 && named == len(answered),
		"%v %d of %d: %s", err, named, len(answered), clip(strings.Join(lines, "; "), 400))
}

// runPlaygroundAsks runs examples/rights-playground's App A and the command
// line's rights change against this runtime, with ca as the enrolled agent.
func runPlaygroundAsks(ctx context.Context, e *env, r *result, ca *session) {
	appA, appB := filepath.Join(e.work, "Playground A.exe"), filepath.Join(e.work, "Playground B.exe")
	for _, to := range []string{appA, appB} {
		if err := copyFile(filepath.Join(e.bin, playgroundProgram), to); !r.check("playground app placed: "+filepath.Base(to), err == nil, "%v", err) {
			return
		}
	}
	ready := filepath.Join(e.work, "source-ready.json")
	source := childproc.Hidden(exec.Command(appA, "source", "--ready-file", ready))
	source.Stdout, source.Stderr = mustLog(e.work, "playground-source.log"), mustLog(e.work, "playground-source.err.log")
	if err := source.Start(); !r.check("dummy receiver started", err == nil, "%v", err) {
		return
	}
	e.procs = append(e.procs, source)
	own(source.Process.Pid)
	var origin struct {
		Origin string `json:"origin"`
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if raw, err := os.ReadFile(ready); err == nil && json.Unmarshal(raw, &origin) == nil && origin.Origin != "" {
			break
		}
	}
	if !r.check("dummy receiver ready", origin.Origin != "", "no ready file") {
		return
	}
	connection := filepath.Join(e.work, "connection.json")
	record, _ := json.Marshal(map[string]any{"version": 1, "isolated": e.isolated, "endpoint": e.endpoint, "runtime_program": e.runtime,
		"state_dir": e.state, "app_a": appA, "app_b": appB, "origin": origin.Origin})
	if err := os.WriteFile(connection, record, 0o600); !r.check("playground connection written", err == nil, "%v", err) {
		return
	}
	run := func(exe string, args ...string) (string, error) {
		cmd := childproc.Hidden(exec.CommandContext(ctx, exe, append(args, "--connection", connection)...))
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	// Design A: the command line's rights grant waits for the person's
	// popup; the agent is not offered it and its answer is refused.
	e.answers.pause()
	setupDone := make(chan string, 1)
	go func() {
		out, err := run(appA, "setup")
		setupDone <- fmt.Sprintf("%s %v", out, errString(err))
	}()
	change := waitPanelQuestion(ctx, e, func(q askedRow) bool { return q.Key == "rights.change" }, 45*time.Second)
	if r.check("the command line's rights grant asks the person (design A)", change.ID != "", "no rights.change question within 45 s") {
		agentRefused(ctx, r, ca, change, "the command line's rights change")
	}
	e.answers.resume()
	setup := <-setupDone
	r.check("playground setup completes after the person allows", strings.Contains(setup, "setup complete"), "%s", clip(setup, 400))

	// App A reads the dummy secret: a typed permission request (credentials
	// CRED-S2), on the person's Permissions list.
	e.answers.pause()
	first, ferr := run(appA, "read", "alpha")
	e.logf("App A first read: %s %v", first, ferr)
	read := waitPermissionRequest(ctx, e, "Playground A", 20*time.Second)
	if r.check("App A's read asks the person", read.ID != "", "first read: %s %v", clip(first, 200), ferr) {
		agentRefused(ctx, r, ca, read, "App A's read of the dummy secret")
	}
	e.answers.resume()
	if read.ID != "" {
		e.answers.waitIDs(ctx, []string{read.ID}, 30*time.Second)
	}
	again, aerr := run(appA, "read", "alpha")
	r.check("App A's read proceeds after the person allows", aerr == nil && strings.Contains(again, "Read alpha succeeded"), "%s %v", clip(again, 300), aerr)
	_, _ = run(appA, "reset-secrets")
}

// agentRefused checks that the enrolled agent is not offered q and that its
// answer is refused with the person-only sentence.
func agentRefused(ctx context.Context, r *result, ca *session, q askedRow, what string) {
	_, _, lout, lerr := ca.call(ctx, "oa_asks_list_answerable", map[string]any{"limit": 32})
	offered := false
	for _, rec := range askRecords(lout) {
		offered = offered || rec.ID == q.ID
	}
	r.check("Claude is not offered "+what, lerr == nil && !offered, "%s %s", clip(q.Text, 200), errString(lerr))
	_, text, out, err := ca.call(ctx, "oa_asks_answer", map[string]any{"id": q.ID, "option": "allow"})
	outcome := asksAnswerOutcome(out)
	r.check("Claude's answer to "+what+" is refused with a sentence", err == nil && outcome.Known() && outcome != askswire.OperatorDecisionOutcomeAnswered && strings.Contains(text, "only the person may answer"),
		"%q %s", outcome.String(), clip(firstNonEmpty(text, errString(err)), 300))
}

// waitPermissionRequest waits for a typed permission request on the Panel's
// Permissions list whose instruction names about.
func waitPermissionRequest(ctx context.Context, e *env, about string, limit time.Duration) askedRow {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(250 * time.Millisecond) {
		var page struct {
			Requests []struct {
				ID          string `json:"id"`
				Instruction string `json:"instruction"`
			} `json:"requests"`
		}
		if _, err := e.panel(ctx, "GET", "/rights/requests", nil, &page); err != nil {
			continue
		}
		for _, q := range page.Requests {
			if strings.Contains(q.Instruction, about) {
				e.logf("permission request for the person: %s %q", q.ID, q.Instruction)
				return askedRow{ID: q.ID, Text: q.Instruction}
			}
		}
	}
	return askedRow{}
}

// waitPanelQuestion waits for an unanswered question on the Panel's
// Questions page that match accepts.
func waitPanelQuestion(ctx context.Context, e *env, match func(askedRow) bool, limit time.Duration) askedRow {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(250 * time.Millisecond) {
		var page struct{ Records []askedRow }
		if _, err := e.panel(ctx, "GET", "/questions", nil, &page); err != nil {
			continue
		}
		for _, q := range page.Records {
			if q.Option == "" && match(q) {
				e.logf("question for the person: %s %s %q", q.Key, q.About, clip(q.Text, 200))
				return q
			}
		}
	}
	return askedRow{}
}
