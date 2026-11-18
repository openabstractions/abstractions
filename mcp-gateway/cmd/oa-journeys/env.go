//go:build windows

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// env is one isolated runtime with its Panel, owned by this run.
type env struct {
	bin, work, state string
	// tools holds the product programs under test: openabstractions.exe,
	// Abstraction Panel.exe and the providers beside them (an installed
	// build's tools folder, or the build's bin).
	tools      string
	runtime    string // absolute runtime executable
	endpoint   string
	panelURL   string // base URL without query
	panelKey   string
	machine    *facade.Machine
	answers    *answerer
	procs      []*exec.Cmd
	runtimeCmd *exec.Cmd
	agents     map[string]*agent
	isolated   string // the --isolated name
	logf       func(string, ...any)
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

var endpointLine = regexp.MustCompile(`^\s*ABSTRACTION_RUNTIME_ENDPOINT=(.+)$`)
var panelLine = regexp.MustCompile(`^service control panel: (http://[^/?\s]+)[^?\s]*\?k=(\S+)$`)

// startEnv starts the isolated runtime (this driver is its operator program)
// and the Panel beside it, which the runtime admits as an installed sibling.
// The runtime is named isolated, or jrn-<10 hex> when isolated is empty.
func startEnv(ctx context.Context, bin, tools, work, isolated string, logf func(string, ...any)) (*env, error) {
	e := &env{bin: bin, tools: tools, work: work, state: filepath.Join(work, "state"), runtime: filepath.Join(tools, "openabstractions.exe"), logf: logf}
	e.isolated = isolated
	if e.isolated == "" {
		e.isolated = "jrn-" + newID()[:10]
	}
	if err := e.startRuntime(ctx); err != nil {
		e.close()
		return nil, err
	}
	who, err := identity.Self()
	if err != nil {
		e.close()
		return nil, err
	}
	e.machine = facade.NewVerified(e.endpoint, listen.ServerExpectation{Principal: who, Program: identity.CanonicalProgramPath(filepath.Clean(e.runtime))})
	panel := childproc.Hidden(exec.Command(filepath.Join(tools, "Abstraction Panel.exe"), "-native=false", "-open=false", "-addr", "127.0.0.1:0",
		"-runtime-endpoint", e.endpoint, "-runtime-program", e.runtime, "-runtime-state-dir", e.state))
	// A program the Panel starts (a connector's register-self) dials this
	// runtime, never the person's installed one.
	panel.Env = isolatedEnvironment(e.endpoint)
	out, err := panel.StdoutPipe()
	if err != nil {
		e.close()
		return nil, err
	}
	panel.Stderr = mustLog(work, "panel.stderr.log")
	if err := panel.Start(); err != nil {
		e.close()
		return nil, err
	}
	e.procs = append(e.procs, panel)
	own(panel.Process.Pid)
	urls := make(chan []string, 1)
	go scanLines(out, mustLog(work, "panel.stdout.log"), func(line string) {
		if m := panelLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			select {
			case urls <- m[1:]:
			default:
			}
		}
	})
	select {
	case u := <-urls:
		e.panelURL, e.panelKey = strings.TrimRight(u[0], "/"), u[1]
	case <-time.After(60 * time.Second):
		e.close()
		return nil, errors.New("Panel printed no URL within 60 s (see panel.stderr.log)")
	}
	logf("runtime %s, Panel %s", e.endpoint, e.panelURL)
	return e, nil
}

func (e *env) startRuntime(ctx context.Context) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := childproc.Hidden(exec.Command(e.runtime, "serve", "runtime", "--isolated", e.isolated, "--state-dir", e.state, "--operator-program", self))
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if out := mustLog(e.work, "runtime.stdout.log"); out != nil {
		defer out.Close()
		cmd.Stdout = out
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	e.procs = append(e.procs, cmd)
	e.runtimeCmd = cmd
	own(cmd.Process.Pid)
	found := make(chan string, 1)
	go scanLines(errPipe, mustLog(e.work, "runtime.stderr.log"), func(line string) {
		if m := endpointLine.FindStringSubmatch(line); m != nil {
			select {
			case found <- strings.TrimSpace(m[1]):
			default:
			}
		}
	})
	select {
	case endpoint := <-found:
		if e.endpoint != "" && e.endpoint != endpoint {
			return errors.New("isolated runtime restarted on a different endpoint")
		}
		e.endpoint = endpoint
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(60 * time.Second):
		return errors.New("runtime printed no ABSTRACTION_RUNTIME_ENDPOINT within 60 s (see runtime.stderr.log)")
	}
}

// Restart the real isolated runtime while the Panel and both agents keep their
// existing connections. The persisted state and endpoint remain the same.
func (e *env) restartRuntime(ctx context.Context) error {
	previous := e.runtimeCmd
	if err := previous.Process.Kill(); err != nil {
		return err
	}
	_, _ = previous.Process.Wait()
	return e.startRuntime(ctx)
}

// isolatedEnvironment is this driver's environment with the runtime
// endpoint variable naming the isolated runtime.
func isolatedEnvironment(endpoint string) []string {
	out := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(v), "ABSTRACTION_RUNTIME_ENDPOINT=") {
			out = append(out, v)
		}
	}
	return append(out, "ABSTRACTION_RUNTIME_ENDPOINT="+endpoint)
}

func mustLog(work, name string) *os.File {
	f, err := os.OpenFile(filepath.Join(work, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return f
}

func scanLines(r io.Reader, copyTo *os.File, each func(string)) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), 1<<20)
	for s.Scan() {
		line := s.Text()
		if copyTo != nil {
			fmt.Fprintln(copyTo, line)
		}
		each(line)
	}
}

func (e *env) close() {
	if e.answers != nil {
		e.answers.stop()
	}
	// The providers the runtime started (a model host and its engine) end
	// with it, so the next journey meets a free card.
	if e.runtimeCmd != nil && e.runtimeCmd.Process != nil {
		killDescendants(e.runtimeCmd.Process.Pid, e.logf)
	}
	for i := len(e.procs) - 1; i >= 0; i-- {
		p := e.procs[i]
		if p.Process != nil {
			_ = p.Process.Kill()
			_, _ = p.Process.Wait()
		}
	}
	removeIsolatedCredentials(e.isolated, e.logf)
}

// panel calls one Panel route with the page's key header.
func (e *env) panel(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, e.panelURL+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Panel-Key", e.panelKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
		}
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp.StatusCode, nil
}

// cli runs the runtime's own command line against the isolated runtime.
func (e *env) cli(ctx context.Context, args ...string) (string, error) {
	args = append(args, "--endpoint", e.endpoint, "--runtime-program", e.runtime)
	cmd := childproc.Hidden(exec.CommandContext(ctx, e.runtime, args...))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// asked is one question the person saw, as the Panel lists it.
type asked struct {
	Source string    // questions | permissions
	ID     string    `json:"ID"`
	Key    string    `json:"Key"`
	About  string    `json:"About"`
	Text   string    `json:"Text"`
	Seen   time.Time `json:"-"`
	Answer string
	Result string
}

// answerer plays the person: it reads the Panel's Questions and Permissions
// lists, waits delay after a question first appears (the person reading the
// popup), then answers it.
type answerer struct {
	e     *env
	delay time.Duration
	mu    sync.Mutex
	seen  map[string]*asked
	log   []*asked
	done  chan struct{}
	wg    sync.WaitGroup
	// refuse lists question keys or about-substrings the person refuses.
	refuse []string
}

func (e *env) startAnswerer(delay time.Duration) *answerer {
	a := &answerer{e: e, delay: delay, seen: map[string]*asked{}, done: make(chan struct{})}
	e.answers = a
	a.wg.Add(1)
	go a.run()
	return a
}

func (a *answerer) stop() {
	select {
	case <-a.done:
	default:
		close(a.done)
	}
	a.wg.Wait()
}

// mark returns how many questions were seen so far; since(mark) lists later ones.
func (a *answerer) mark() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.log) }

func (a *answerer) since(mark int) []asked {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []asked{}
	for _, q := range a.log[mark:] {
		out = append(out, *q)
	}
	return out
}

func (a *answerer) run() {
	defer a.wg.Done()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-a.done:
			return
		case <-tick.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		a.poll(ctx)
		cancel()
	}
}

func (a *answerer) poll(ctx context.Context) {
	var page struct {
		Records []struct {
			ID, Key, About, Text, Option string
			Options                      []string
		}
		// Sentences is how the Panel words each question for the person.
		Sentences map[string]string `json:"sentences"`
	}
	if _, err := a.e.panel(ctx, "GET", "/questions", nil, &page); err == nil {
		for _, r := range page.Records {
			if r.Option != "" {
				continue
			}
			text := r.Text
			if s := page.Sentences[r.ID]; s != "" {
				text = s + "\n" + r.Text
			}
			q := a.note("questions", r.ID, r.Key, r.About, text)
			if q == nil || time.Since(q.Seen) < a.delay {
				continue
			}
			option := pick(r.Options, a.refuses(r.Key+" "+r.About))
			var result map[string]any
			_, err := a.e.panel(ctx, "POST", "/questions", map[string]string{"action": "answer", "id": r.ID, "option": option}, &result)
			a.answered(q, option, err, result)
		}
	}
	var perms struct {
		Requests []struct {
			ID          string `json:"id"`
			Instruction string `json:"instruction"`
			Content     string `json:"content"`
			Digest      string `json:"digest"`
		} `json:"requests"`
	}
	if _, err := a.e.panel(ctx, "GET", "/rights/requests", nil, &perms); err == nil {
		for _, r := range perms.Requests {
			q := a.note("permissions", r.ID, "permission", r.Instruction, r.Content)
			if q == nil || time.Since(q.Seen) < a.delay {
				continue
			}
			option := "allow"
			if a.refuses(r.Instruction + " " + r.Content) {
				option = "refuse"
			}
			var result map[string]any
			_, err := a.e.panel(ctx, "POST", "/rights/requests", map[string]string{"id": r.ID, "option": option, "digest": r.Digest}, &result)
			a.answered(q, option, err, result)
		}
	}
}

func (a *answerer) refuses(text string) bool {
	for _, r := range a.refuse {
		if strings.Contains(text, r) {
			return true
		}
	}
	return false
}

// pick chooses the answer a person gives to get on with the task.
func pick(options []string, refuse bool) string {
	prefer := []string{"keep", "allow", "always", "yes", "once"}
	if refuse {
		prefer = []string{"refuse", "no", "never"}
	}
	for _, want := range prefer {
		for _, o := range options {
			if o == want {
				return o
			}
		}
	}
	if len(options) > 0 {
		return options[0]
	}
	return "allow"
}

// note records a waiting question once and returns it while unanswered.
func (a *answerer) note(source, id, key, about, text string) *asked {
	a.mu.Lock()
	defer a.mu.Unlock()
	q, ok := a.seen[source+"/"+id]
	if !ok {
		q = &asked{Source: source, ID: id, Key: key, About: about, Text: text, Seen: time.Now()}
		a.seen[source+"/"+id] = q
		a.log = append(a.log, q)
		a.e.logf("question %s %s %q: %s", source, key, about, text)
	}
	if q.Answer != "" {
		return nil
	}
	return q
}

func (a *answerer) answered(q *asked, option string, err error, result map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	q.Answer = option
	raw, _ := json.Marshal(result)
	q.Result = string(raw)
	if err != nil {
		q.Result = err.Error()
	}
	a.e.logf("answered %s %s with %s: %s", q.Source, q.Key, option, q.Result)
}

// agent is one agent helper image registered and kept in the isolated runtime.
type agent struct {
	base, title, program string
}

// enrollAgent places the gateway under its fixed helper name, registers it
// the way the Panel's connection setup does and requests Keep; the person's
// Keep question is answered by the answerer.
func (e *env) enrollAgent(ctx context.Context, base, title string) (*agent, error) {
	if a, ok := e.agents[base]; ok {
		return a, nil
	}
	a, err := e.enrollAgentOnce(ctx, base, title)
	if err == nil {
		if e.agents == nil {
			e.agents = map[string]*agent{}
		}
		e.agents[base] = a
	}
	return a, err
}

func (e *env) enrollAgentOnce(ctx context.Context, base, title string) (*agent, error) {
	dir := filepath.Join(e.work, "agents", base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	program := filepath.Join(dir, base+".exe")
	if err := copyFile(filepath.Join(e.bin, "gateway.test.exe"), program); err != nil {
		return nil, err
	}
	req := facade.Requirements{Scope: facade.ScopeLocal}
	directory, err := e.machine.ResolveApplications(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("resolve applications: %w", err)
	}
	change, err := directory.Register(ctx, fwire.ApplicationDescriptor{Name: base, Program: identity.CanonicalProgramPath(program), Title: title})
	if err != nil {
		return nil, fmt.Errorf("register %s: %w", base, err)
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied {
		return nil, fmt.Errorf("register %s: %s %s", base, change.Outcome, change.Reason)
	}
	binding, err := e.machine.ResolveService(ctx, "abstraction.facade/applications@3", req)
	if err != nil {
		return nil, err
	}
	client := applications.NewApplicationsClient(binding.Transport().WithContext(ctx))
	read, err := client.ReadAgentEnrollment(base)
	if err != nil || read.Enrollment == nil {
		return nil, fmt.Errorf("read enrollment %s: %v %+v", base, err, read)
	}
	set, err := client.SetAgentEnrollment(applications.AgentEnrollmentRequest{Application: base, ExpectedRevision: read.Enrollment.Revision, Enabled: true})
	if err != nil {
		return nil, fmt.Errorf("request Keep for %s: %w", base, err)
	}
	if set.Outcome != applications.OutcomeApplied {
		return nil, fmt.Errorf("request Keep for %s: %s %s", base, set.Outcome, set.Reason)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		read, err = applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).ReadAgentEnrollment(base)
		if err == nil && read.Enrollment != nil && read.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
			return &agent{base: base, title: title, program: program}, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, fmt.Errorf("%s was not kept within 60 s (%+v %v)", base, read, err)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// session is one MCP connection an agent app holds to its OA helper.
type session struct {
	*mcp.ClientSession
	name  string
	calls int
	tools map[string]*mcp.Tool
	logf  func(string, ...any)
}

// connect launches the helper the way the agent app does: as a stdio MCP
// child with the host's chat environment.
func (e *env) connect(ctx context.Context, a *agent, hostEnv []string) (*session, error) {
	state := filepath.Join(e.work, "gateway-state", a.base+"-"+newID()[:6])
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	cfg, _ := json.Marshal(map[string]any{"endpoint": e.endpoint, "runtime_program": e.runtime, "args": []string{"--state-dir", state}})
	cmd := childproc.Hidden(exec.Command(a.program, "-test.run=^$"))
	cmd.Env = append(append(os.Environ(), "OA_JOURNEY_GATEWAY="+string(cfg)), hostEnv...)
	cmd.Stderr = mustLog(e.work, a.base+".stderr.log")
	client := mcp.NewClient(&mcp.Implementation{Name: a.title, Version: "journey"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", a.base, err)
	}
	e.procs = append(e.procs, cmd)
	if cmd.Process != nil {
		own(cmd.Process.Pid)
	}
	s := &session{ClientSession: cs, name: a.title, tools: map[string]*mcp.Tool{}, logf: e.logf}
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, t := range list.Tools {
		s.tools[t.Name] = t
	}
	return s, nil
}

// call is one tool call as the agent makes it; it returns the result's text
// and structured content.
func (s *session) call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, string, map[string]any, error) {
	s.calls++
	start := time.Now()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.logf("%s -> %s %v: error %v (%.1fs)", s.name, name, args, err, time.Since(start).Seconds())
		return nil, "", nil, err
	}
	text := resultText(res)
	structured := map[string]any{}
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &structured)
	} else {
		_ = json.Unmarshal([]byte(text), &structured)
	}
	s.logf("%s -> %s %v: isError=%v %s (%.1fs)", s.name, name, args, res.IsError, clip(text, 600), time.Since(start).Seconds())
	return res, text, structured, nil
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
