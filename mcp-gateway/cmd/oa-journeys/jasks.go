//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	identity "github.com/openabstractions/abstraction-identity"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// J-ANSWERER (experience-matrix "Let OpenCode answer routine permissions, then
// Stop or Undo" and HP8/HP9 "Who allowed this, and can I take it back?";
// round5/person.md 2.7 and section 3; asks ASK-R3..R7, ASK-R10; rights
// RIGHTS-R16, O8, O9). The person enrolls OpenCode's helper for routine
// questions through its own request. While the person is away, Codex's
// helper starts a picture job; OpenCode lists the routine first-use
// questions and answers them, a question that stays with the person is
// refused to it, and the Panel shows each answer with its setter. Stop ends
// future answers and leaves the permits; Undo of one answer removes only the
// permit that answer wrote, and Codex's fresh retry asks again.
func init() { register(80, "J-ANSWERER", runAnswerer) }

const (
	answererBase = "oa-opencode-integration"
	askerBase    = "oa-codex-integration"
)

// pictureToolName is the gateway's picture tool (mcp-gateway/gateway/image.go).
const pictureToolName = "oa_image_generate"

type submitOutcome struct {
	res  *mcp.CallToolResult
	text string
	st   map[string]any
	err  error
}

func runAnswerer(ctx context.Context, e *env, o *options, r *result) {
	if !ensureJourneyImageProvider(ctx, e, r) {
		return
	}
	// The person's picture model is the test provider's, as Setup writes it;
	// the picture tool is listed and Codex names only prompt and size.
	if err := writePicturePolicy(ctx, e, testImageModel); !r.check("picture policy written", err == nil, "%v", err) {
		return
	}
	ans, err := e.enrollAgent(ctx, answererBase, "OpenCode")
	if !r.check("answering app kept", err == nil, "%v", err) {
		return
	}
	asker, err := e.enrollAgent(ctx, askerBase, "Codex")
	if !r.check("asking app kept", err == nil, "%v", err) {
		return
	}
	sa, err := e.connect(ctx, ans, []string{"OA_OPENCODE_HOST_CHAT_ID=" + newUUID()})
	if !r.check("answering app connected", err == nil, "%v", err) {
		return
	}
	defer sa.Close()
	sb, err := e.connect(ctx, asker, nil)
	if !r.check("asking app connected", err == nil, "%v", err) {
		return
	}
	defer sb.Close()
	defer func() { r.Calls = sa.calls + sb.calls }()
	for _, name := range []string{"oa_asks_request_answering", "oa_asks_list_answerable", "oa_asks_answer"} {
		if !r.check("tool "+name+" listed", sa.tools[name] != nil, "tools: %s", toolNames(sa.tools)) {
			return
		}
	}

	// 1. Enrollment: OpenCode asks; only the person can answer.
	mark := e.answers.mark()
	_, text, out, err := sa.call(ctx, "oa_asks_request_answering", map[string]any{})
	enrolled := err == nil && enrollmentPermitted(out)
	if !enrolled && err == nil {
		e.answers.waitAnswered(ctx, mark, 60*time.Second)
		_, text, out, err = sa.call(ctx, "oa_asks_request_answering", map[string]any{})
		enrolled = err == nil && enrollmentPermitted(out)
	}
	asked := ""
	for _, q := range e.answers.since(mark) {
		if strings.Contains(q.Text, "question.answer") {
			asked = q.Key + ": " + q.Text + " -> " + q.Answer
		}
	}
	r.check("the person was asked to let OpenCode answer", asked != "", "%s", clip(asked, 300))
	r.check("enrollment request permitted after the person's yes", enrolled, "%s", clip(firstNonEmpty(text, errString(err)), 400))
	if !enrolled {
		// The Panel's own Let it answer is the person's other route (ASK-R5);
		// the rest of the journey runs on it so its checks still report.
		if perr := panelEnroll(ctx, e, ans.program); !r.check("Panel enrollment as the fallback route", perr == nil, "%v", perr) {
			return
		}
	}
	listed, revision, aerr := panelAssistants(ctx, e)
	r.check("the Panel lists OpenCode as answering for the person", aerr == nil && containsProgram(listed, ans.program), "%v %s", aerr, strings.Join(listed, "; "))

	// 2. The person is away: Codex starts a picture job and waits; OpenCode
	// watches what it may answer.
	e.answers.pause()
	defer e.answers.resume()
	// Pictures go through oa_image_generate, which takes size (the job tools
	// carry video only); each call makes its own request key.
	if !r.check("Codex lists the picture tool", sb.tools[pictureToolName] != nil, "tools: %s", toolNames(sb.tools)) {
		return
	}
	args := map[string]any{"prompt": "a lighthouse at dusk", "size": "auto"}
	type answerRec struct{ id, text string }
	var answered []answerRec
	done := map[string]bool{}
	refusedToAnswerer, personOnly := "", ""
	notListed := map[string]int{}
	accepted, sealed := false, false
	var last string
	for attempt := 0; attempt < 5 && !accepted; attempt++ {
		started := time.Now()
		ch := make(chan submitOutcome, 1)
		go func(args map[string]any) {
			res, text, st, err := sb.call(ctx, pictureToolName, args)
			ch <- submitOutcome{res, text, st, err}
		}(cloneArgs(args))
		var got submitOutcome
	wait:
		for {
			select {
			case got = <-ch:
				break wait
			case <-time.After(1500 * time.Millisecond):
			case <-ctx.Done():
				got = submitOutcome{err: ctx.Err()}
				break wait
			}
			_, _, lout, lerr := sa.call(ctx, "oa_asks_list_answerable", map[string]any{"limit": 32})
			if lerr != nil {
				continue
			}
			listedNow := map[string]bool{}
			for _, rec := range askRecords(lout) {
				listedNow[rec.ID] = true
				if done[rec.ID] {
					continue
				}
				done[rec.ID] = true
				option := pick(rec.Options, false)
				_, atext, aout, aerr := sa.call(ctx, "oa_asks_answer", map[string]any{"id": rec.ID, "option": option})
				effectText, _ := firstOf(aout, "effect", "Effect").(string)
				effect, _ := askswire.ParseResponderEffectOutcome(effectText)
				r.check("OpenCode's answer applied its permission: "+questionAction(rec.Text), aerr == nil &&
					asksAnswerOutcome(aout) == askswire.OperatorDecisionOutcomeAnswered && effect == askswire.ResponderEffectOutcomeApplied,
					"%s", clip(firstNonEmpty(atext, errString(aerr)), 300))
				answered = append(answered, answerRec{id: rec.ID, text: rec.Text})
			}
			// What the person sees waiting from Codex that OpenCode was not
			// offered stays with the person.
			for _, q := range panelPending(ctx, e, asker.program) {
				if listedNow[q.ID] || done[q.ID] {
					continue
				}
				if notListed[q.ID]++; notListed[q.ID] < 3 {
					continue
				}
				done[q.ID] = true
				_, atext, aout, aerr := sa.call(ctx, "oa_asks_answer", map[string]any{"id": q.ID, "option": "allow"})
				outcome := asksAnswerOutcome(aout)
				personOnly = questionAction(q.Text)
				if aerr == nil && outcome.Known() && outcome != askswire.OperatorDecisionOutcomeAnswered {
					refusedToAnswerer = outcome.String()
				} else {
					refusedToAnswerer = ""
					r.check("OpenCode was refused "+personOnly, false, "%s", clip(firstNonEmpty(atext, errString(aerr)), 300))
				}
				// The person comes back for it.
				e.answers.resume()
				e.answers.waitIDs(ctx, []string{q.ID}, 60*time.Second)
				e.answers.pause()
			}
		}
		if got.err != nil {
			last = got.err.Error()
			break
		}
		last = fmt.Sprintf("after %.1fs: %s", time.Since(started).Seconds(), got.text)
		outcome, _ := got.st["outcome"].(string)
		switch {
		case permissionPending(got.text, got.st):
			// The person's or OpenCode's answers landed after the call's
			// budget; the agent repeats the call.
		case outcome == "definitely_not_accepted" || outcome == "not_accepted":
			if !sealed {
				sealed = true
				r.check("the repeated picture call after the answers is accepted", false, "%s", clip(got.text, 300))
			}
		default:
			accepted = got.res != nil && !got.res.IsError && pictureStarted(got.text, got.st)
			if !accepted {
				attempt = 5
			}
		}
	}
	r.check("Codex's picture job accepted after OpenCode answered", accepted, "%s", clip(last, 400))
	r.check("OpenCode answered a routine first-use question", len(answered) > 0, "%d answers", len(answered))
	r.check("a question that stays with the person was refused to OpenCode", refusedToAnswerer != "", "%s -> %q", firstNonEmpty(personOnly, "(no person-only question arose)"), refusedToAnswerer)

	// 3. The person reads what was answered for them.
	page, perr := panelAnswered(ctx, e)
	inForce := 0
	for _, a := range page {
		if sameJourneyProgram(a.Answerer, ans.program) && a.State == "in_force" {
			inForce++
		}
	}
	r.check("Answered for you lists OpenCode's answers in force with OpenCode as setter", perr == nil && inForce >= len(answered) && len(answered) > 0,
		"%v %d in force of %d answered: %s", perr, inForce, len(answered), answeredSummary(page))

	// 4. Stop: future answers end, existing permits stay.
	if _, revision, aerr = panelAssistants(ctx, e); aerr == nil {
		var stop map[string]any
		_, serr := e.panel(ctx, "POST", "/rights/assistant", map[string]any{"action": "stop", "program": ans.program, "revision": revision}, &stop)
		r.check("Stop applied", serr == nil && stop["outcome"] == "applied", "%v %v", serr, stop)
		r.check("Stop says existing permissions keep their expiry", strings.Contains(fmt.Sprint(stop["message"]), "keep their expiry"), "%v", stop["message"])
	} else {
		r.check("Stop applied", false, "%v", aerr)
	}
	listedAfter, _, _ := panelAssistants(ctx, e)
	r.check("OpenCode no longer listed as answering", !containsProgram(listedAfter, ans.program), "%s", strings.Join(listedAfter, "; "))
	page, perr = panelAnswered(ctx, e)
	still := 0
	for _, a := range page {
		if sameJourneyProgram(a.Answerer, ans.program) && a.State == "in_force" {
			still++
		}
	}
	r.check("existing permits stay after Stop", perr == nil && still == inForce && still > 0, "%v %d in force after Stop (before %d)", perr, still, inForce)

	// 5. Undo of one answer removes only its own still-current permit.
	if len(answered) == 0 {
		r.check("Undo of one answer", false, "no answer to undo")
		return
	}
	undo := answered[0]
	var undone map[string]any
	_, uerr := e.panel(ctx, "POST", "/rights/answered", map[string]any{"id": undo.id}, &undone)
	r.check("Undo applied", uerr == nil && undone["outcome"] == "applied", "%v %v", uerr, undone)
	page, _ = panelAnswered(ctx, e)
	undoneState, others := "", 0
	for _, a := range page {
		if a.ID == undo.id {
			undoneState = a.State
		} else if sameJourneyProgram(a.Answerer, ans.program) && a.State == "in_force" {
			others++
		}
	}
	r.check("the undone answer's permit ended: "+questionAction(undo.text), undoneState == "ended", "state %q (%s)", undoneState, answeredSummary(page))
	r.check("the other answers' permits stay", others == inForce-1, "%d other in force (expected %d)", others, inForce-1)

	// 6. Codex's fresh retry asks again; the stopped OpenCode is not offered
	// it and its answer reads forbidden; the person answers.
	ch := make(chan submitOutcome, 1)
	go func(args map[string]any) {
		res, text, st, err := sb.call(ctx, pictureToolName, args)
		ch <- submitOutcome{res, text, st, err}
	}(cloneArgs(args))
	var fresh []askedRow
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && len(fresh) == 0 {
		time.Sleep(time.Second)
		fresh = panelPending(ctx, e, asker.program)
	}
	r.check("Codex's fresh retry asks again after Undo", len(fresh) > 0, "%d questions waiting", len(fresh))
	if len(fresh) > 0 {
		_, ltext, lout, lerr := sa.call(ctx, "oa_asks_list_answerable", map[string]any{"limit": 32})
		offered := false
		for _, rec := range askRecords(lout) {
			offered = offered || rec.ID == fresh[0].ID
		}
		r.check("the stopped OpenCode is not offered it", lerr == nil && !offered, "%s", clip(firstNonEmpty(ltext, errString(lerr)), 200))
		_, atext, aout, aerr := sa.call(ctx, "oa_asks_answer", map[string]any{"id": fresh[0].ID, "option": "allow"})
		ao := asksAnswerOutcome(aout)
		r.check("the stopped OpenCode's answer reads forbidden (ASK-R5)", aerr == nil && ao == askswire.OperatorDecisionOutcomeForbidden,
			"%q %s", ao.String(), clip(firstNonEmpty(atext, errString(aerr)), 200))
	}
	e.answers.resume()
	select {
	case got := <-ch:
		outcome, _ := got.st["outcome"].(string)
		r.check("the person's answer lets the retry through", got.err == nil && got.res != nil && !got.res.IsError && pictureStarted(got.text, got.st) && outcome != "definitely_not_accepted" && !permissionPending(got.text, got.st),
			"%s", clip(firstNonEmpty(got.text, errString(got.err)), 300))
	case <-ctx.Done():
		r.check("the person's answer lets the retry through", false, "%v", ctx.Err())
	}
}

func cloneArgs(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// questionAction is the "wants to <action> on <resource>" part of a first-use
// question's text.
func questionAction(text string) string {
	if i := strings.Index(text, " wants to "); i >= 0 {
		return clip(text[i+1:], 120)
	}
	return clip(text, 120)
}

// ensureJourneyImageProvider declares the test picture provider J-PIC
// declares, once per isolated runtime.
func ensureJourneyImageProvider(ctx context.Context, e *env, r *result) bool {
	listed, _ := e.cli(ctx, "provider", "list")
	if strings.Contains(listed, "journey-image") {
		return true
	}
	store := filepath.Join(e.work, "image-provider")
	out, err := e.cli(ctx, "provider", "add", "journey-image", "--program", filepath.Join(e.bin, "journey-image.exe"),
		"--provider-endpoint", "journey-image-"+newID()[:6],
		"--contract", "abstraction.inference/image@1", "--contract", "abstraction.storage/content-reader@1", "--contract", "abstraction.storage/content-writer@1",
		"--profiles", "image", "--model", testImageModel,
		"--arg", "-endpoint", "--arg", "{endpoint}", "--arg", "-runtime", "--arg", e.runtime, "--arg", "-store", "--arg", store)
	return r.check("picture provider declared", err == nil, "%v %s", err, strings.TrimSpace(out))
}

func enrollmentPermitted(out map[string]any) bool {
	word, _ := firstOf(out, "outcome", "Outcome").(string)
	outcome, _ := rightswire.ParseDecisionOutcome(word)
	return outcome == rightswire.DecisionOutcomePermitted
}

func asksAnswerOutcome(out map[string]any) askswire.OperatorDecisionOutcome {
	word, _ := firstOf(out, "outcome", "Outcome").(string)
	outcome, _ := askswire.ParseOperatorDecisionOutcome(word)
	return outcome
}

// askRecord is one question an answerer reads in oa_asks_list_answerable.
type askRecord struct {
	ID, Key, About, Text string
	Options              []string
}

// askRecords finds the question records in a native asks page, whatever the
// wire spelling of its field names.
func askRecords(v any) []askRecord {
	var out []askRecord
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			id, _ := firstOf(t, "ID", "id").(string)
			opts, hasOpts := firstOf(t, "Options", "options").([]any)
			if hasOpts && id != "" {
				rec := askRecord{ID: id}
				rec.Key, _ = firstOf(t, "Key", "key").(string)
				rec.About, _ = firstOf(t, "About", "about").(string)
				rec.Text, _ = firstOf(t, "Text", "text").(string)
				for _, o := range opts {
					if s, ok := o.(string); ok {
						rec.Options = append(rec.Options, s)
					}
				}
				out = append(out, rec)
				return
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
	return out
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

type askedRow struct{ ID, Key, About, Text, Option string }

// panelPending is what the person sees waiting on the Questions page about
// one program.
func panelPending(ctx context.Context, e *env, program string) []askedRow {
	var page struct{ Records []askedRow }
	if _, err := e.panel(ctx, "GET", "/questions", nil, &page); err != nil {
		return nil
	}
	var out []askedRow
	for _, rec := range page.Records {
		if rec.Option == "" && sameJourneyProgram(rec.About, program) {
			out = append(out, rec)
		}
	}
	return out
}

func panelAssistants(ctx context.Context, e *env) ([]string, string, error) {
	var page struct {
		Outcome    string `json:"outcome"`
		Revision   string `json:"revision"`
		Assistants []struct {
			Program string `json:"program"`
			Expires string `json:"expires"`
		} `json:"assistants"`
	}
	if _, err := e.panel(ctx, "GET", "/rights/assistant", nil, &page); err != nil {
		return nil, "", err
	}
	var programs []string
	for _, a := range page.Assistants {
		programs = append(programs, a.Program)
	}
	return programs, page.Revision, nil
}

func panelEnroll(ctx context.Context, e *env, program string) error {
	_, revision, err := panelAssistants(ctx, e)
	if err != nil {
		return err
	}
	var out map[string]any
	if _, err := e.panel(ctx, "POST", "/rights/assistant", map[string]any{"action": "enroll", "program": program, "revision": revision, "confirm": true}, &out); err != nil {
		return err
	}
	if out["outcome"] != "applied" {
		return fmt.Errorf("enroll: %v", out)
	}
	return nil
}

type panelAnswer struct {
	ID           string `json:"id"`
	Sentence     string `json:"sentence"`
	Option       string `json:"option"`
	Answerer     string `json:"answerer"`
	AnswererName string `json:"answererName"`
	Program      string `json:"program"`
	State        string `json:"state"`
}

func panelAnswered(ctx context.Context, e *env) ([]panelAnswer, error) {
	var page struct {
		Outcome string        `json:"outcome"`
		Answers []panelAnswer `json:"answers"`
	}
	if _, err := e.panel(ctx, "GET", "/rights/answered", nil, &page); err != nil {
		return nil, err
	}
	if page.Outcome != "page" {
		return page.Answers, fmt.Errorf("answered outcome %s", page.Outcome)
	}
	return page.Answers, nil
}

func answeredSummary(page []panelAnswer) string {
	var parts []string
	for _, a := range page {
		parts = append(parts, fmt.Sprintf("%s [%s by %s] %s", clip(a.Sentence, 90), a.State, a.AnswererName, a.Option))
	}
	raw, _ := json.Marshal(parts)
	return string(raw)
}

func containsProgram(list []string, program string) bool {
	for _, p := range list {
		if sameJourneyProgram(p, program) {
			return true
		}
	}
	return false
}

func sameJourneyProgram(a, b string) bool {
	return identity.SameSubjectProgram(a, b)
}

// pause stops the person from answering until resume: the person is away
// while an enrolled answerer works.
func (a *answerer) pause() { a.stop() }

// resume lets the person answer again after pause.
func (a *answerer) resume() {
	select {
	case <-a.done:
	default:
		return // running
	}
	a.done = make(chan struct{})
	a.wg.Add(1)
	go a.run()
}
