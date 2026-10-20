//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// J-COMFY-OPEN (owner, R20c, 2026-11-10): pressing Open for ComfyUI in the
// Panel raised the blue Allow dialog and then failed with "context deadline
// exceeded"; on installed builds every app open fails with "Access is
// denied", because the supervisor runs the runtime inside a job that forbids
// breakaway while the owned launch asks for breakaway. The owner also expects
// an agent to open ComfyUI.
//
// The journey installs a stand-in for ComfyUI's launch helper
// (cmd/oa-journey-app with an applications@3 manifest declaring the open
// verb, embedded as OA_APPLICATION_MANIFEST), and has it register itself as
// Setup does. Registration asks nothing (architect ruling 2026-11-10): the
// entry waits in Apps until the person acts on it there. The runtime's real
// owned launch starts it; it comes up in comfyStartDelay, inside its
// declared readiness.
//
//  1. The person presses Open on the app's Panel page. Open raises the app's
//     Keep question; the person keeps it and answers the Allow dialog, each
//     after panelPersonDelay.
//  2. The person closes the app. OpenCode's agent opens it with
//     oa_applications_open, following the tool description and results; the
//     person answers the open question after the suite's answer delay.
//  3. The agent lists the open app's commands and calls workflow.list with
//     only its inputs, {"search": "z image"}, as oa_applications_command_call
//     describes (owner R23, architect ruling 2026-11-10). The app receives
//     the Invocation OA built in its current context; workflow.inputs with
//     {} is refused with reason field:workflow:missing and never reaches the
//     app.
//  4. The person closes the app again; the agent opens it and follows the
//     returned handle through a backend startup longer than host readiness.
//
// Each open must start the app's process (found by its program path), answer
// ready within its caller's bound, and show no bare "context deadline
// exceeded" or "Access is denied". It runs twice: first with the runtime in
// the installed supervisor's job (supervision.Start, no breakaway), then as
// a development runtime whose job allows breakaway.
func init() { register(40, "J-COMFY-OPEN", runComfyOpen) }

const comfyOpenApp = "comfyui"

// comfyOpenReadiness is the manifest's readiness_deadline_ms, ComfyUI's own
// value: the runtime's budget for one start after the person answered
// (serve/application_v3_gateway_activate.go activate).
const comfyOpenReadiness = 30 * time.Second

// comfyStartDelay is how long the stand-in takes to come up: inside its
// declared readiness. panelPersonDelay is how long the person reads the
// Allow dialog the Panel's Open raises.
const (
	comfyStartDelay   = 12 * time.Second
	comfyBackendDelay = 32 * time.Second
	panelPersonDelay  = 8 * time.Second
)

// The gateway bounds every native tool call at two minutes
// (mcp-gateway/gateway/server.go bounded); the Panel's Open route at 35 s
// (monitor/app_page.go appStart).
const (
	gatewayBound = 2 * time.Minute
	panelBound   = 35 * time.Second
)

var bareErrors = []string{"context deadline exceeded", "access is denied", "deadline exceeded"}

func bareError(text string) string {
	t := strings.ToLower(text)
	for _, b := range bareErrors {
		if strings.Contains(t, b) {
			return b
		}
	}
	return ""
}

func runComfyOpen(ctx context.Context, e *env, o *options, r *result) {
	if _, err := os.Stat(filepath.Join(e.bin, "oa-journey-app.exe")); err != nil {
		r.check("stand-in app built", false, "%s: %v (journeys.py builds ./cmd/oa-journey-app)", filepath.Join(e.bin, "oa-journey-app.exe"), err)
		return
	}
	notes := []string{}
	for _, v := range []struct {
		label      string
		supervised bool
		budget     time.Duration
	}{{"supervised", true, 110 * time.Second}, {"development", false, 4 * time.Minute}} {
		vctx, vcancel := context.WithTimeout(ctx, v.budget)
		ve, stop, note, err := startComfyEnv(vctx, e.bin, e.tools, filepath.Join(e.work, "comfy-open-"+v.label), v.supervised, e.logf)
		name := "runtime started in a job that allows breakaway"
		if v.supervised {
			name = "runtime started in the supervisor's job (no breakaway)"
		}
		if r.check(v.label+": "+name, err == nil, "%s", firstNonEmpty(errString(err), note)) {
			ve.startAnswerer(o.answerDelay)
			asked := comfyOpenVariant(vctx, ve, v.label, o, r)
			for _, a := range append(asked, ve.answers) {
				for _, q := range a.since(0) {
					r.Asked = append(r.Asked, fmt.Sprintf("%s %s %s: %s -> %s", v.label, q.Source, q.Key, clip(q.Text, 160), q.Answer))
				}
			}
			stop()
			notes = append(notes, v.label+" run")
			if note != "" && len(notes) == 1 {
				notes = append(notes, note)
			}
		}
		vcancel()
	}
	r.Note = strings.Join(notes, "; ")
}

// questionKeys lists questions as key=answer.
func questionKeys(questions []asked) string {
	keys := ""
	for _, q := range questions {
		keys += q.Key + "=" + q.Answer + " "
	}
	return strings.TrimSpace(keys)
}

// runtimeCause is the runtime's own log line for the last owned start, which
// the person never sees; check details carry it.
func runtimeCause(e *env) string {
	raw, _ := os.ReadFile(filepath.Join(e.work, "runtime.stderr.log"))
	cause := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "owned start of") {
			cause = strings.TrimSpace(line)
		}
	}
	return cause
}

// comfyOpenVariant installs and registers the stand-in in e, opens it from
// the Panel, where the person keeps it, and then from the agent. It returns the answerers it
// retired, whose questions belong to the result.
func comfyOpenVariant(ctx context.Context, e *env, label string, o *options, r *result) []*answerer {
	check := func(name string, pass bool, detail string, args ...any) bool {
		return r.check(label+": "+name, pass, detail, args...)
	}
	var retired []*answerer
	dir := filepath.Join(e.work, "app", "ComfyUI")
	program, err := installComfyStandIn(e, dir)
	if !check("stand-in installed with its manifest resource", err == nil, "%s %v", program, err) {
		return retired
	}
	defer stopComfyStandIn(e, dir, program)

	// Setup registers the installed helper by running it. Registration asks
	// the person nothing; the entry waits in Apps.
	mark := e.answers.mark()
	rctx, rcancel := context.WithTimeout(ctx, 40*time.Second)
	out, err := childproc.Hidden(exec.CommandContext(rctx, program, "--register")).CombinedOutput()
	rcancel()
	if !check("stand-in registered itself", err == nil && strings.Contains(string(out), `"applied"`), "%v %s", err, strings.TrimSpace(string(out))) {
		return retired
	}
	// The answerer reads the Questions page every 250 ms.
	time.Sleep(3 * time.Second)
	check("registration asked the person nothing", len(e.answers.since(mark)) == 0, "%s", questionKeys(e.answers.since(mark)))

	// 1. The person presses Open on the app's page and reads the Allow
	// dialog for panelPersonDelay before answering.
	shown, err := panelShown(ctx, e, comfyOpenApp)
	if check("the app's Panel page shows the entry", err == nil, "%v", err) {
		fast := e.answers
		fast.stop()
		retired = append(retired, fast)
		slow := &answerer{e: e, delay: panelPersonDelay, seen: map[string]*asked{}, done: make(chan struct{})}
		slow.wg.Add(1)
		go slow.run()
		e.answers = slow
		pctx, pcancel := context.WithTimeout(ctx, panelBound+15*time.Second)
		at := time.Now()
		var reply struct{ Outcome, Message string }
		code, body, err := panelPost(pctx, e, "/app/"+comfyOpenApp+"/start", map[string]any{"confirm": true, "shown": shown}, &reply)
		pcancel()
		took := time.Since(at)
		e.logf("%s: Panel Open %d after %.1fs: %v %s", label, code, took.Seconds(), err, clip(body, 400))
		pids := comfyProcesses(program)
		check("the person's Open asked Keep and the person kept it", strings.Contains(questionKeys(slow.since(0)), "application.self_registered=keep"), "%s", questionKeys(slow.since(0)))
		check("Panel Open answers that the app is open", err == nil && code == 200 && reply.Outcome == "ready",
			"HTTP %d after %.1fs (Panel bound %s; person answers after %s, app comes up in %s): %s; runtime: %s",
			code, took.Seconds(), panelBound, panelPersonDelay, comfyStartDelay, clip(firstNonEmpty(body, errString(err)), 300), runtimeCause(e))
		if len(pids) == 0 {
			// The start may still be coming up after the route answered.
			time.Sleep(2 * time.Second)
			pids = comfyProcesses(program)
		}
		check("Panel Open started the app's process", len(pids) > 0, "pids %v; app log: %s", pids, clip(appLog(dir), 300))
		check("no bare error text reaches the Panel page", bareError(body) == "", "%q in %s", bareError(body), clip(body, 300))
		slow.stop()
		retired = append(retired, slow)
		e.startAnswerer(o.answerDelay)
	}

	// 2. The person closes the app; the agent opens it, following the tool
	// description and results.
	stopComfyStandIn(e, dir, program)
	a, err := e.enrollAgent(ctx, "oa-opencode-integration", "OpenCode")
	if !check("agent app kept", err == nil, "%v", err) {
		return retired
	}
	s, err := e.connect(ctx, a, []string{"OA_OPENCODE_HOST_CHAT_ID=" + newUUID()})
	if !check("agent connected", err == nil, "%v", err) {
		return retired
	}
	defer s.Close()
	if !check("oa_applications_open is listed", s.tools["oa_applications_open"] != nil, "tools: %s", toolNames(s.tools)) {
		return retired
	}
	// The person asked to open ComfyUI. The agent looks for its name in the
	// applications list, then in describe.
	seen := []string{}
	discovered := ""
	for _, tool := range []string{"oa_applications_list", "oa_applications_describe"} {
		if _, ok := s.tools[tool]; !ok {
			continue
		}
		_, text, _, err := s.call(ctx, tool, map[string]any{})
		seen = append(seen, text, errString(err))
		if err == nil && strings.Contains(text, `"`+comfyOpenApp+`"`) {
			discovered = tool
			break
		}
		discovered += tool + " -> " + clip(firstNonEmpty(text, errString(err)), 200) + "; "
	}
	check("the agent finds the app's name before opening it", !strings.Contains(discovered, "->"), "%s", discovered)
	opened, instance := agentOpen(ctx, e, s, label, "agent's open", dir, program, false, check)
	seen = append(seen, opened...)
	// 3. The agent calls the open app's commands with their inputs alone.
	seen = append(seen, agentCommandCall(ctx, s, instance, dir, check)...)
	// 4. The person closes the app; the agent opens it again.
	stopComfyStandIn(e, dir, program)
	if !check("the slow backend fixture is configured", writeComfyStandInConfig(e, dir, 0, comfyBackendDelay) == nil, "") {
		return retired
	}
	opened, _ = agentOpen(ctx, e, s, label, "agent's open after the app closed", dir, program, true, check)
	seen = append(seen, opened...)
	r.Calls += s.calls
	all := strings.Join(seen, "\n")
	check("no bare error text reaches the agent", bareError(all) == "", "%q in %s", bareError(all), clip(all, 400))
	return retired
}

// agentOpen is the agent opening the app with oa_applications_open, as its
// description and results say. The agent resends the original request while
// consent is pending and follows a starting backend by its returned handle.
func agentOpen(ctx context.Context, e *env, s *session, label, name, dir, program string, slowBackend bool, check func(string, bool, string, ...any) bool) ([]string, string) {
	var seen []string
	before, opened := len(appStarts(dir)), strings.Count(appLog(dir), " opened")
	started := time.Now()
	var text string
	var structured map[string]any
	ready := false
	calls := 0
	follows := 0
	var handle, timeoutReason string
	for calls < 4 && follows < 2 && ctx.Err() == nil {
		calls++
		cctx, ccancel := context.WithTimeout(ctx, gatewayBound+10*time.Second)
		at := time.Now()
		var cerr error
		args := map[string]any{"application": comfyOpenApp, "verb": "open", "payload": map[string]any{}}
		if handle != "" {
			args = map[string]any{"handle": handle}
			follows++
		} else if slowBackend {
			args["wait_ms"] = 0
		}
		_, text, structured, cerr = s.call(cctx, "oa_applications_open", args)
		ccancel()
		seen = append(seen, text, errString(cerr))
		e.logf("%s: %s call %d after %.1fs: %v %s", label, name, calls, time.Since(at).Seconds(), cerr, clip(text, 400))
		if cerr != nil {
			break
		}
		outcome, _ := structured["outcome"].(string)
		if outcome == "ready" {
			ready = true
			break
		}
		if outcome != "not_ready" {
			break
		}
		if structured["status"] == "starting" {
			returned, _ := structured["handle"].(string)
			if returned == "" || (handle != "" && returned != handle) {
				break
			}
			if follows > 0 {
				timeoutReason, _ = structured["reason"].(string)
			}
			handle = returned
			continue
		}
		if structured["reason"] != "consent" || handle != "" {
			break
		}
		e.answers.waitQuiet(ctx, 30*time.Second)
	}
	elapsed := time.Since(started)
	check(name+" answers ready", ready, "%d open calls, %.1fs: %s; runtime: %s", calls, elapsed.Seconds(), clip(text, 300), runtimeCause(e))
	check(name+" returns within the gateway's bound", elapsed < gatewayBound, "%.1fs (gateway bound %s, readiness %s, app comes up in %s)", elapsed.Seconds(), gatewayBound, comfyOpenReadiness, comfyStartDelay)
	pids := comfyProcesses(program)
	check(name+" started the app's process", len(pids) > 0 && len(appStarts(dir)) > before, "pids %v, %d starts before, %d after", pids, before, len(appStarts(dir)))
	check(name+" delivered the open verb to the app", strings.Count(appLog(dir), " opened") > opened, "%s", clip(appLog(dir), 300))
	if slowBackend {
		check(name+" follows the same start by its returned handle", follows == 2 && len(appStarts(dir))-before == 1 &&
			strings.Count(appLog(dir), " opened")-opened == 1, "%d follow calls, %d process starts, %d deliveries",
			follows, len(appStarts(dir))-before, strings.Count(appLog(dir), " opened")-opened)
		check(name+" reports the startup wait reason", strings.HasPrefix(timeoutReason, "activation:refused: starting:loading_custom_nodes"),
			"%q", timeoutReason)
		check(name+" waits beyond host readiness for the backend", ready && elapsed > comfyOpenReadiness,
			"%.1fs elapsed, host readiness %s", elapsed.Seconds(), comfyOpenReadiness)
	}
	instance, _ := structured["instance"].(string)
	return seen, instance
}

// agentCommandCall is the agent calling the open app's commands as the tool
// descriptions say: it lists the commands, then calls one with only the
// command's inputs. It returns every text the agent read.
func agentCommandCall(ctx context.Context, s *session, instance, dir string, check func(string, bool, string, ...any) bool) []string {
	var seen []string
	if !check("the agent's open named the instance", instance != "", "no instance in the open result") {
		return seen
	}
	call := func(tool string, args map[string]any) (string, map[string]any) {
		cctx, ccancel := context.WithTimeout(ctx, gatewayBound+10*time.Second)
		defer ccancel()
		_, text, structured, err := s.call(cctx, tool, args)
		seen = append(seen, text, errString(err))
		return firstNonEmpty(text, errString(err)), structured
	}
	text, listing := call("oa_applications_commands_list", map[string]any{"application": comfyOpenApp, "instance": instance})
	if !check("oa_applications_commands_list lists workflow.list", listing["outcome"] == "page" && strings.Contains(text, `"workflow.list"`), "%s", clip(text, 300)) {
		return seen
	}
	before := strings.Count(appLog(dir), "invocation workflow.list")
	text, result := call("oa_applications_command_call", map[string]any{"application": comfyOpenApp, "instance": instance, "tool": "workflow.list", "arguments": map[string]any{"search": "z image"}})
	key, _ := result["idempotency_key"].(string)
	check("workflow.list with its inputs alone is applied", result["outcome"] == "applied" && strings.Contains(text, "z image turbo") && !strings.Contains(text, "flux"), "%s", clip(text, 300))
	check("the result echoes the key OA generated", len(key) == 32, "idempotency_key %q", key)
	log := appLog(dir)
	sent := strings.Count(log, "invocation workflow.list") == before+1 &&
		strings.Contains(log, "invocation workflow.list context=workflows epoch=e1 mode=apply expected_revision=r1 key="+key+` arguments={"search":"z image"}`)
	check("the app received the Invocation OA built in its current context", sent, "%s", clip(log[max(0, strings.LastIndex(log, "invocation")):], 300))
	text, result = call("oa_applications_command_call", map[string]any{"application": comfyOpenApp, "instance": instance, "tool": "workflow.inputs", "arguments": map[string]any{}})
	check("a missing input is refused by its field", result["outcome"] == "invalid" && result["reason"] == "field:workflow:missing", "%s", clip(text, 300))
	check("the refused call never reached the app", !strings.Contains(appLog(dir), "invocation workflow.inputs"), "%s", clip(appLog(dir), 300))
	return seen
}

// appStarts lists the stand-in's owned-host starts from its log.
func appStarts(dir string) []string {
	var starts []string
	for _, line := range strings.Split(appLog(dir), "\n") {
		if strings.Contains(line, "start [owned-host]") {
			starts = append(starts, line)
		}
	}
	return starts
}

// installComfyStandIn copies the stand-in into dir under the helper's name
// with its applications@3 manifest resource and the isolated runtime's
// address beside it.
func installComfyStandIn(e *env, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(e.bin, "oa-journey-app.exe"))
	if err != nil {
		return "", err
	}
	manifest := `{"version":"applications@3","name":"` + comfyOpenApp + `","title":"ComfyUI — OA build","description":"Journey stand-in for the ComfyUI launch helper.",` +
		`"offers":{"version":"appmodel@1","commands":[{"name":"workflow.list","class":"read"},{"name":"workflow.inputs","class":"read"}]},"host":{"arguments":["owned-host"],"readiness_deadline_ms":` + fmt.Sprint(comfyOpenReadiness.Milliseconds()) + `},` +
		`"script_host":false,"requests":[],"network":{"hosts":[]},"needs":{"files":[],"models":[]},"purpose":"",` +
		`"verbs":[{"name":"open","class":"reveal","input_schema":{"type":"object","properties":{},"additionalProperties":false}}],"choosers":{"entries":[]}}`
	image, err := withManifestResource(raw, "OA_APPLICATION_MANIFEST", []byte(manifest))
	if err != nil {
		return "", err
	}
	program := filepath.Join(dir, "comfyui-launch.exe")
	if err := os.WriteFile(program, image, 0o700); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(dir, "stop"))
	return program, writeComfyStandInConfig(e, dir, comfyStartDelay, 0)
}

func writeComfyStandInConfig(e *env, dir string, hostDelay, backendDelay time.Duration) error {
	cfg, _ := json.Marshal(map[string]any{"endpoint": e.endpoint, "runtime_program": e.runtime, "application": comfyOpenApp,
		"start_delay_ms": hostDelay.Milliseconds(), "backend_delay_ms": backendDelay.Milliseconds()})
	return os.WriteFile(filepath.Join(dir, "journey-app.json"), cfg, 0o600)
}
func appLog(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, "journey-app.log"))
	return string(raw)
}

// stopComfyStandIn asks the running stand-in to withdraw and exit, then
// stops any of its processes still running. Only processes whose image is
// this run's own copy of the stand-in are touched.
func stopComfyStandIn(e *env, dir, program string) {
	image, err := fileid.Open(context.Background(), fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return
	}
	defer image.Close()
	expected := image.FileID()
	if len(comfyProcessIDs(expected)) == 0 {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "stop"), nil, 0o600)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(comfyProcessIDs(expected)) > 0 {
		time.Sleep(200 * time.Millisecond)
	}
	visitComfyProcesses(expected, windows.PROCESS_TERMINATE, func(pid int, process windows.Handle) {
		if windows.TerminateProcess(process, 1) == nil {
			e.logf("stopped stand-in pid %d", pid)
		}
	})
	_ = os.Remove(filepath.Join(dir, "stop"))
}

// comfyProcesses lists the running processes whose image is program.
func comfyProcesses(program string) []int {
	image, err := fileid.Open(context.Background(), fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil
	}
	defer image.Close()
	return comfyProcessIDs(image.FileID())
}

func comfyProcessIDs(expected fileid.FileID) []int {
	var pids []int
	visitComfyProcesses(expected, 0, func(pid int, _ windows.Handle) {
		pids = append(pids, pid)
	})
	return pids
}

// visitComfyProcesses keeps the process and file observations open while visit
// runs. Forced cleanup acts through the very process handle it checked.
func visitComfyProcesses(expected fileid.FileID, access uint32, visit func(int, windows.Handle)) {
	if expected.IsDir() || expected.Links() != 1 {
		return
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	buf := make([]uint16, windows.MAX_LONG_PATH)
	name := filepath.Base(expected.Path())
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		// The pinned executable name filters the snapshot. FileID decides.
		if !fileid.SamePathName(windows.UTF16ToString(entry.ExeFile[:]), name) {
			continue
		}
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|access, false, entry.ProcessID)
		if err != nil {
			continue
		}
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(process, 0, &buf[0], &n) == nil {
			image, err := fileid.Open(context.Background(), fileid.Self,
				windows.UTF16ToString(buf[:n]), fileid.Options{Links: fileid.RefuseLinks})
			if err == nil {
				if image.FileID().Links() == 1 && image.FileID().Equal(expected) {
					visit(int(entry.ProcessID), process)
				}
				image.Close()
			}
		}
		windows.CloseHandle(process)
	}
}

var shownAttr = regexp.MustCompile(`data-shown="([0-9a-f]{64})"`)

// panelShown reads the app page the person sees and the entry digest its Open
// button sends.
func panelShown(ctx context.Context, e *env, name string) (string, error) {
	code, body, err := panelGet(ctx, e, "/app/"+name)
	if err != nil {
		return "", err
	}
	m := shownAttr.FindStringSubmatch(body)
	if code != 200 || m == nil {
		return "", fmt.Errorf("GET /app/%s: %d, no data-shown: %s", name, code, clip(body, 300))
	}
	return m[1], nil
}

func panelGet(ctx context.Context, e *env, path string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", e.panelURL+path+"?k="+e.panelKey, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("X-Panel-Key", e.panelKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, string(raw), err
}

// panelPost presses a Panel button: it returns the status and the raw body
// the page receives.
func panelPost(ctx context.Context, e *env, path string, body any, out any) (int, string, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", e.panelURL+path, strings.NewReader(string(raw)))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("X-Panel-Key", e.panelKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err == nil && out != nil {
		_ = json.Unmarshal(got, out)
	}
	return resp.StatusCode, string(got), err
}
