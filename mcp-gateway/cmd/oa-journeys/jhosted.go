//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	identity "github.com/openabstractions/abstraction-identity"
)

// J-KEY (P-M9, person.md 2.2): the person connects a hosted provider key on
// the Panel with a daily limit; the agent's first hosted completion asks the
// person once, the reply comes from the provider, the spend shows on the
// Panel, the key appears in no agent result or file, and Revoke stops the
// next call before it reaches the provider.
//
// J-CLOUD-LOSS (experience-matrix "Keep coding when cloud access
// disappears"): with a preferred hosted model and a permitted local fallback,
// the hosted provider goes away mid-session; the next request is answered by
// the local model, naming the model and the reason, and with no local model
// left the request is refused clearly.
//
// Both play the hosted provider with a fake OpenAI-compatible upstream on
// 127.0.0.1 and never use a real key.
func init() {
	if len(os.Args) > 1 && os.Args[1] == hostedLocalChatArg {
		os.Exit(runHostedLocalChat(os.Args[2:]))
	}
	register(50, "J-KEY", runHostedKey)
	register(60, "J-CLOUD-LOSS", runCloudLoss)
}

const (
	hostedLocalChatArg = "-journey-local-chat"
	hostedKeyName      = "deepseek"
	hostedKeyModel     = "deepseek-flash"
	hostedKeyTokens    = 200000
	cloudName          = "journey-cloud"
	cloudModel         = "journey-cloud-chat"
	cloudLocalModel    = "journey-local-chat"
	cloudLocalProvider = "journey-local"
	cloudPolicy        = "coding"
	matrixCloudLine    = "research/adoption/experience-matrix-2026-09-29.md:37"
	hostedUsageIn      = 11
	hostedUsageOut     = 7
)

// hostedConsumers are the consumers the Panel's Connect a service applies a
// preset key for (monitor/connect_panel.go presetConsumers).
var hostedConsumers = []string{"abstraction.inference/chat@1", "abstraction.router/router@1"}

// ---- the fake hosted provider ----

type upstreamRequest struct {
	at         time.Time
	method     string
	path       string
	authorized bool
}

// fakeUpstream is an OpenAI-compatible provider on loopback: GET /v1/models
// and POST /v1/chat/completions (streamed, with usage), each requiring the
// test key as its bearer token.
type fakeUpstream struct {
	ln           net.Listener
	srv          *http.Server
	base         string
	key, model   string
	reply        string
	mu           sync.Mutex
	reqs         []upstreamRequest
	stopped      bool
	stoppedAt    time.Time
	lastChatBody string
}

func startUpstream(key, model, reply string) (*fakeUpstream, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	u := &fakeUpstream{ln: ln, key: key, model: model, reply: reply, base: "http://" + ln.Addr().String() + "/v1"}
	u.srv = &http.Server{Handler: http.HandlerFunc(u.handle), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = u.srv.Serve(ln) }()
	return u, nil
}

func (u *fakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	authorized := r.Header.Get("Authorization") == "Bearer "+u.key
	u.mu.Lock()
	u.reqs = append(u.reqs, upstreamRequest{at: time.Now(), method: r.Method, path: r.URL.Path, authorized: authorized})
	u.mu.Unlock()
	if !authorized {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Authentication Fails, Your api key is invalid","type":"authentication_error"}}`)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{{"id": u.model, "object": "model", "owned_by": "journey"}}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		u.mu.Lock()
		u.lastChatBody = string(raw)
		u.mu.Unlock()
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(raw, &body)
		writeChatReply(w, body.Stream, firstNonEmpty(body.Model, u.model), u.reply)
	default:
		http.NotFound(w, r)
	}
}

// writeChatReply answers one chat completion the way DeepSeek and OpenRouter
// do: server-sent chunks ending in a usage chunk and [DONE], or one JSON body.
func writeChatReply(w http.ResponseWriter, stream bool, model, reply string) {
	usage := map[string]any{"prompt_tokens": hostedUsageIn, "completion_tokens": hostedUsageOut, "total_tokens": hostedUsageIn + hostedUsageOut}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "chatcmpl-journey", "object": "chat.completion", "model": model,
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}}, "usage": usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	send := func(v any) {
		raw, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "chatcmpl-journey", "object": "chat.completion.chunk", "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	send(chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	send(chunk(map[string]any{"content": reply}, nil))
	send(chunk(map[string]any{}, "stop"))
	send(map[string]any{"id": "chatcmpl-journey", "object": "chat.completion.chunk", "model": model, "choices": []any{}, "usage": usage})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// since counts every request and the chat requests that arrived after t.
func (u *fakeUpstream) since(t time.Time) (all, chats, authorizedChats int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, q := range u.reqs {
		if q.at.Before(t) {
			continue
		}
		all++
		if q.path == "/v1/chat/completions" {
			chats++
			if q.authorized {
				authorizedChats++
			}
		}
	}
	return
}

// stop closes the listener, so the next connection is refused as for a
// provider that went away.
func (u *fakeUpstream) stop() {
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		return
	}
	u.stopped, u.stoppedAt = true, time.Now()
	u.mu.Unlock()
	_ = u.ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = u.srv.Shutdown(ctx)
	_ = u.srv.Close()
}

// ---- the person's Panel steps ----

type hostedHost struct {
	Entry struct {
		Name       string
		Hosted     bool
		Kind       string
		Base       string
		Credential string
		Ceiling    *struct{ TokensPerDay, MicrosPerDay int64 }
	}
	Up    bool
	Why   string
	Spend *struct {
		Day                      string
		Tokens, Micros, Requests int64
	}
}

func panelHosts(ctx context.Context, e *env) (string, []hostedHost, error) {
	var page struct {
		Outcome  string
		Revision string
		Hosts    []hostedHost
	}
	_, err := e.panel(ctx, "GET", "/inference/hosts", nil, &page)
	if err == nil && page.Outcome != "page" {
		err = fmt.Errorf("model servers read %q", page.Outcome)
	}
	return page.Revision, page.Hosts, err
}

func panelHost(ctx context.Context, e *env, name string) (*hostedHost, error) {
	_, hosts, err := panelHosts(ctx, e)
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if hosts[i].Entry.Name == name {
			return &hosts[i], nil
		}
	}
	return nil, nil
}

// connectHosted is the person connecting a hosted provider on the Panel:
// Connect a service first; it takes only https addresses, so for the
// loopback test provider the person saves the key on Credentials and adds
// the model server with its daily token limit on Models, the two routes
// Connect itself composes. It waits until the Panel shows the server up.
func connectHosted(ctx context.Context, e *env, r *result, up *fakeUpstream, name string, tokensPerDay int64, tryConnect bool) bool {
	if tryConnect {
		var presets struct {
			Presets []struct {
				ID     string `json:"id"`
				Budget string `json:"budget"`
			} `json:"presets"`
		}
		_, perr := e.panel(ctx, "GET", "/connect", nil, &presets)
		var connected map[string]any
		_, cerr := e.panel(ctx, "POST", "/connect", map[string]any{"edit": "connect", "preset": "openai-compatible", "base": up.base, "secret": up.key, "budget": tokensPerDay}, &connected)
		raw, _ := json.Marshal(connected)
		if cerr == nil && connected["outcome"] == "applied" {
			r.Note = "connected through Connect a service"
			r.check("connected through the Panel's Connect a service", true, "%s", raw)
			return waitHostUp(ctx, e, r, fmt.Sprint(connected["name"]))
		}
		r.Note = fmt.Sprintf("Connect a service lists %d presets (%v) and refuses a loopback http address (%s %v); key saved on Credentials and server added on Models", len(presets.Presets), perr, raw, cerr)
	}
	var stored map[string]any
	_, err := e.panel(ctx, "POST", "/credentials", map[string]any{"action": "add", "name": name, "kind": "bearer",
		"targets": []string{"127.0.0.1"}, "consumers": hostedConsumers, "secret": up.key}, &stored)
	if !r.check("the person saved the key on the Panel", err == nil && stored["Outcome"] == "stored", "%v %v", stored["Outcome"], err) {
		return false
	}
	var change map[string]any
	for attempt := 0; attempt < 2; attempt++ {
		revision, _, herr := panelHosts(ctx, e)
		if herr != nil {
			err = herr
			break
		}
		host := map[string]any{"Name": name, "Hosted": true, "Kind": "openai-compatible", "Base": up.base, "Credential": name}
		if tokensPerDay > 0 {
			host["Ceiling"] = map[string]any{"TokensPerDay": tokensPerDay}
		}
		change = nil
		_, err = e.panel(ctx, "POST", "/inference/hosts", map[string]any{"edit": "add", "revision": revision, "host": host}, &change)
		if err == nil && change["Outcome"] != "conflict" {
			break
		}
	}
	if !r.check("the person added the model server with its daily limit", err == nil && change["Outcome"] == "applied", "%v %v", change, err) {
		return false
	}
	return waitHostUp(ctx, e, r, name)
}

func waitHostUp(ctx context.Context, e *env, r *result, name string) bool {
	var last string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		h, err := panelHost(ctx, e, name)
		switch {
		case err != nil:
			last = err.Error()
		case h == nil:
			last = "not listed"
		case h.Up:
			return r.check("the Panel shows the provider up", true, "%s %s", h.Entry.Name, h.Entry.Base)
		default:
			last = h.Why
		}
		time.Sleep(time.Second)
	}
	return r.check("the Panel shows the provider up", false, "%s: %s", name, last)
}

// revokeCredential is the person's Revoke on the Credentials page: the
// listing's revision, then revoke.
func revokeCredential(ctx context.Context, e *env, name string) (string, error) {
	var outcome string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var list any
		if _, err = e.panel(ctx, "GET", "/credentials", nil, &list); err != nil {
			return "", err
		}
		revision := credentialRevision(list, name)
		if revision == "" {
			return "unknown", nil
		}
		var result map[string]any
		if _, err = e.panel(ctx, "POST", "/credentials", map[string]any{"action": "revoke", "name": name, "expected_revision": revision}, &result); err != nil {
			return "", err
		}
		outcome = fmt.Sprint(result["Outcome"])
		if outcome != "conflict" {
			return outcome, nil
		}
	}
	return outcome, err
}

func credentialRevision(v any, name string) string {
	switch t := v.(type) {
	case map[string]any:
		if t["Name"] == name {
			if rev, ok := t["Revision"].(string); ok && rev != "" {
				return rev
			}
		}
		for _, c := range t {
			if rev := credentialRevision(c, name); rev != "" {
				return rev
			}
		}
	case []any:
		for _, c := range t {
			if rev := credentialRevision(c, name); rev != "" {
				return rev
			}
		}
	}
	return ""
}

// ---- the agent ----

// hostedAgent records every result the agent saw, for the key search.
type hostedAgent struct {
	s                 *session
	e                 *env
	texts             []string
	refusedAfterAllow string
}

func (a *hostedAgent) call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, string, map[string]any, error) {
	res, text, structured, err := a.s.call(ctx, name, args)
	a.texts = append(a.texts, text)
	if res != nil && res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		a.texts = append(a.texts, string(raw))
	}
	if err != nil {
		a.texts = append(a.texts, err.Error())
	}
	return res, text, structured, err
}

// complete is one oa_inference_complete as the agent makes it. When the
// result says the person is being asked, the agent waits for the answer and
// repeats the call, as the tool description says.
//
// A call refused not_permitted after the person allowed a question asked
// during it is repeated once, as an agent does after the person says they
// allowed it; refusedAfterAllow reports that the first call did not run.
func (a *hostedAgent) complete(ctx context.Context, args map[string]any) (res *mcp.CallToolResult, text string, out map[string]any, err error) {
	a.refusedAfterAllow = ""
	mark := a.e.answers.mark()
	res, text, out, err = a.call(ctx, "oa_inference_complete", args)
	for tries := 0; err == nil && tries < 3; tries++ {
		waiting, _ := out["waiting"].(bool)
		ids := questionIDs(out)
		if waiting || out["outcome"] == "pending" && len(ids) > 0 || permissionPending(text, out) {
			a.e.answers.waitIDs(ctx, ids, 60*time.Second)
		} else if out["outcome"] == "not_permitted" && a.refusedAfterAllow == "" && len(a.e.answers.since(mark)) > 0 {
			a.refusedAfterAllow = clip(text, 300)
			a.e.answers.waitQuiet(ctx, 60*time.Second)
		} else {
			break
		}
		res, text, out, err = a.call(ctx, "oa_inference_complete", args)
	}
	return res, text, out, err
}

// listedAlias reads the agent's model list until a servable alias carrying
// model appears; the agent asks for a fresh survey after a server change.
func (a *hostedAgent) listedAlias(ctx context.Context, model string) (string, []map[string]any, string) {
	var last string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		res, text, out, err := a.call(ctx, "oa_models_list", map[string]any{"fresh": true})
		last = firstNonEmpty(text, errString(err))
		if err == nil && res != nil && !res.IsError {
			models, _ := out["models"].([]any)
			var entries []map[string]any
			for _, m := range models {
				entry, _ := m.(map[string]any)
				if entry == nil {
					continue
				}
				aliases, _ := entry["aliases"].([]any)
				for _, al := range aliases {
					if s, _ := al.(string); s != "" && strings.Contains(s, model) {
						return s, append(entries, entry), last
					}
				}
				entries = append(entries, entry)
			}
		} else {
			a.e.answers.waitQuiet(ctx, 30*time.Second)
		}
		time.Sleep(time.Second)
	}
	return "", nil, last
}

func (e *env) hostedAgentSession(ctx context.Context, r *result) (*agent, *hostedAgent, bool) {
	ag, err := e.enrollAgent(ctx, "oa-opencode-integration", "OpenCode")
	if !r.check("agent app kept", err == nil, "%v", err) {
		return nil, nil, false
	}
	s, err := e.connect(ctx, ag, []string{"OA_OPENCODE_HOST_CHAT_ID=" + newUUID()})
	if !r.check("agent connected", err == nil, "%v", err) {
		return nil, nil, false
	}
	return ag, &hostedAgent{s: s, e: e}, true
}

// completed reports a finished reply carrying want.
func completed(res *mcp.CallToolResult, text string, out map[string]any, err error, want string) bool {
	return err == nil && res != nil && !res.IsError && out["outcome"] == "completed" && strings.Contains(text, want)
}

// sentence is a refusal a person can read: a message or error with words,
// not only a code.
func sentence(res *mcp.CallToolResult, text string, out map[string]any) string {
	for _, key := range []string{"message", "detail", "error"} {
		if s, _ := out[key].(string); len(strings.Fields(s)) >= 4 {
			return s
		}
	}
	if res != nil && res.IsError && !strings.HasPrefix(strings.TrimSpace(text), "{") && len(strings.Fields(text)) >= 4 {
		return text
	}
	return ""
}

// filesHolding lists the files under the roots whose bytes contain secret.
func filesHolding(secret []byte, roots ...string) []string {
	var hits []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, ierr := d.Info(); ierr != nil || info.Size() > 64<<20 {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr == nil && bytes.Contains(raw, secret) {
				hits = append(hits, path)
			}
			return nil
		})
	}
	return hits
}

// ---- J-KEY ----

func runHostedKey(ctx context.Context, e *env, o *options, r *result) {
	key := "sk-journey-" + newID() + newID()
	marker := "hosted reply " + newID()[:8]
	up, err := startUpstream(key, hostedKeyModel, marker)
	if !r.check("fake provider listening on loopback", err == nil, "%v", err) {
		return
	}
	defer up.stop()
	revoked := false
	defer func() {
		if !revoked {
			// The isolated runtime keeps the test key in its own namespace;
			// remove it whatever happened above.
			cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = revokeCredential(cctx, e, hostedKeyName)
		}
	}()
	if !connectHosted(ctx, e, r, up, hostedKeyName, hostedKeyTokens, true) {
		return
	}
	if h, _ := panelHost(ctx, e, hostedKeyName); h != nil {
		limit := int64(0)
		if h.Entry.Ceiling != nil {
			limit = h.Entry.Ceiling.TokensPerDay
		}
		r.check("the Panel shows the daily limit", limit == hostedKeyTokens, "tokens per day %d", limit)
	}
	_, agent, ok := e.hostedAgentSession(ctx, r)
	if !ok {
		return
	}
	defer agent.s.Close()
	defer func() { r.Calls = agent.s.calls }()
	alias, entries, listed := agent.listedAlias(ctx, hostedKeyModel)
	if !r.check("the agent's model list shows the hosted model", alias != "", "%s", clip(listed, 400)) {
		return
	}
	named := false
	for _, entry := range entries {
		raw, _ := json.Marshal(entry)
		if strings.Contains(string(raw), `"credential"`) || strings.Contains(string(raw), `"hosting"`) || strings.Contains(string(raw), `"hosted"`) {
			named = true
		}
	}
	r.check("the model list tells the agent the hosting and credential to use", named,
		"oa_models_list entries carry family/name/aliases/profiles/servable only (mcp-gateway/gateway/types.go:57-73); oa_inference_complete requires hosting and a credential name (types.go:91-92, oa.go:166); the agent uses the provider name the person said: %s", clip(listed, 300))
	mark := e.answers.mark()
	args := map[string]any{"model": alias, "prompt": "Say hello in five words.", "hosting": "hosted", "credential": hostedKeyName}
	started := time.Now()
	res, text, out, err := agent.complete(ctx, args)
	asked := e.answers.since(mark)
	r.check("the first hosted use asked the person", len(asked) > 0, "%d questions: %s", len(asked), askedSummary(asked))
	r.check("the first hosted call runs after the person's Allow", agent.refusedAfterAllow == "",
		"the call returned %s after the Allow (openabstractions-flat/abstraction-inference/go/service/host.go:416-419 ends the hold with a bare rights:not_granted when the re-attempt gathers no new question; mcp-gateway/gateway/permission_middleware.go:136-151 retries only with ids); the agent repeated it", agent.refusedAfterAllow)
	limitWorded := false
	for _, q := range asked {
		if strings.Contains(strings.ToLower(q.Text), "limit") || strings.Contains(q.Text, fmt.Sprint(hostedKeyTokens)) || strings.Contains(q.Text, "200,000") {
			limitWorded = true
		}
	}
	r.check("the first-use question names the daily limit", limitWorded, "person.md 2.2 \"word it with the limit\": %s", askedSummary(asked))
	_, chats, authorized := up.since(started)
	r.check("the reply came from the provider", completed(res, text, out, err, marker) && authorized > 0,
		"%d chat requests (%d with the key) after %.1fs: %s", chats, authorized, time.Since(started).Seconds(), clip(firstNonEmpty(text, errString(err)), 400))
	r.check("the result names the provider and model", out["host"] == hostedKeyName && strings.Contains(fmt.Sprint(out["model"]), hostedKeyModel), "host=%v model=%v", out["host"], out["model"])
	// Spend appears on the Panel's model server row.
	var spend string
	spent := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !spent {
		h, herr := panelHost(ctx, e, hostedKeyName)
		if herr != nil || h == nil || h.Spend == nil {
			spend = fmt.Sprintf("%v %+v", herr, h)
		} else {
			spend = fmt.Sprintf("day %s tokens %d requests %d", h.Spend.Day, h.Spend.Tokens, h.Spend.Requests)
			spent = h.Spend.Tokens >= hostedUsageIn+hostedUsageOut
		}
		if !spent {
			time.Sleep(time.Second)
		}
	}
	r.check("the Panel shows today's spend", spent, "%s (provider reported %d tokens)", spend, hostedUsageIn+hostedUsageOut)
	// The key stays with OA.
	secret := []byte(key)
	inResults := 0
	for _, t := range agent.texts {
		if strings.Contains(t, key) {
			inResults++
		}
	}
	r.check("the key appears in no agent result", inResults == 0, "%d of %d results", inResults, len(agent.texts))
	agentFiles := filesHolding(secret, filepath.Join(e.work, "gateway-state"), filepath.Join(e.work, "agents"), filepath.Join(e.work, "oa-opencode-integration.stderr.log"))
	r.check("the key appears in no file of the agent's helper", len(agentFiles) == 0, "%v", agentFiles)
	runtimeFiles := filesHolding(secret, e.state, filepath.Join(e.work, "runtime.stderr.log"), filepath.Join(e.work, "runtime.stdout.log"),
		filepath.Join(e.work, "panel.stderr.log"), filepath.Join(e.work, "panel.stdout.log"), filepath.Join(e.work, "journeys.log"))
	r.check("the key appears in no runtime or Panel file", len(runtimeFiles) == 0, "%v", runtimeFiles)
	// Revoke on the Panel; the next call is refused before the provider.
	outcome, err := revokeCredential(ctx, e, hostedKeyName)
	revoked = err == nil && outcome == "revoked"
	if !r.check("the person revoked the key on the Panel", revoked, "%s %v", outcome, err) {
		return
	}
	after := time.Now()
	res, text, out, err = agent.complete(ctx, args)
	refusal := sentence(res, text, out)
	r.check("the next call is refused with a sentence", err == nil && out["outcome"] != "completed" && !strings.Contains(text, marker) && refusal != "",
		"%s", clip(firstNonEmpty(text, errString(err)), 400))
	time.Sleep(3 * time.Second)
	all, chats, _ := up.since(after)
	r.check("the provider receives no request after Revoke", all == 0, "%d requests (%d chat) after Revoke", all, chats)
}

func askedSummary(asked []asked) string {
	var parts []string
	for _, q := range asked {
		parts = append(parts, fmt.Sprintf("[%s %s] %s -> %s", q.Source, q.Key, clip(q.Text, 200), q.Answer))
	}
	return strings.Join(parts, "; ")
}

// ---- J-CLOUD-LOSS ----

func runCloudLoss(ctx context.Context, e *env, o *options, r *result) {
	key := "sk-journey-" + newID() + newID()
	cloudMarker := "cloud reply " + newID()[:8]
	up, err := startUpstream(key, cloudModel, cloudMarker)
	if !r.check("fake provider listening on loopback", err == nil, "%v", err) {
		return
	}
	defer up.stop()
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = revokeCredential(cctx, e, cloudName)
	}()
	if !connectHosted(ctx, e, r, up, cloudName, 0, false) {
		return
	}
	localMarker, ok := startCloudLocal(ctx, e, r)
	if !ok {
		return
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = e.cli(cctx, "provider", "remove", cloudLocalProvider)
	}()
	ag, agent, ok := e.hostedAgentSession(ctx, r)
	if !ok {
		return
	}
	defer agent.s.Close()
	defer func() { r.Calls = agent.s.calls }()
	// The person saves the preferred hosted model and the permitted local
	// fallback for this app on the Panel.
	var put struct {
		Change string `json:"change"`
	}
	_, err = e.panel(ctx, "POST", "/registry/selection", map[string]any{"edit": "put", "target_program": identity.CanonicalProgramPath(ag.program),
		"name": cloudPolicy, "revision": "", "candidates": []map[string]any{
			{"model": cloudModel, "placement": "hosted", "credential": cloudName},
			{"model": cloudLocalModel, "placement": "local"}}}, &put)
	r.check("the person saved a preferred hosted model with a local fallback", err == nil && strings.Contains(put.Change, `"applied"`), "%s %v", put.Change, err)
	cloudAlias, _, listed := agent.listedAlias(ctx, cloudModel)
	if !r.check("the agent's model list shows the hosted model", cloudAlias != "", "%s", clip(listed, 300)) {
		return
	}
	localAlias, _, listed := agent.listedAlias(ctx, cloudLocalModel)
	r.check("the agent's model list shows the local model", localAlias != "", "%s", clip(listed, 300))
	cloudArgs := map[string]any{"model": cloudAlias, "prompt": "Explain this stack trace in one line.", "hosting": "hosted", "credential": cloudName}
	res, text, out, err := agent.complete(ctx, cloudArgs)
	r.check("the hosted model answers", completed(res, text, out, err, cloudMarker), "%s", clip(firstNonEmpty(text, errString(err)), 300))
	if localAlias != "" {
		res, text, out, err = agent.complete(ctx, map[string]any{"model": localAlias, "prompt": "Explain this stack trace in one line.", "hosting": "local"})
		r.check("the first local call runs after the person's Allow", agent.refusedAfterAllow == "",
			"the call returned %s after the Allow (openabstractions-flat/abstraction-inference/go/service/host.go:416-419 ends the hold with a bare rights:not_granted when the re-attempt gathers no new question); the agent repeated it", agent.refusedAfterAllow)
		r.check("the local model answers when named", completed(res, text, out, err, localMarker), "%s", clip(firstNonEmpty(text, errString(err)), 300))
	}
	// Cloud access goes away mid-session.
	up.stop()
	res, text, out, err = agent.complete(ctx, cloudArgs)
	gap := "oa_inference_complete takes an explicit model and hosting and names no selection policy (mcp-gateway/gateway/types.go:85-95, oa.go:183 Target{Model}); chat@2 falls back only under a named policy (abstraction-inference/go/selection_start.go:33-35); " + matrixCloudLine
	fellBack := completed(res, text, out, err, localMarker)
	reason := ""
	for _, k := range []string{"selection_reason", "reason", "message"} {
		if s, _ := out[k].(string); s != "" {
			reason = s
		}
	}
	// backlog/JOURNEY-SCOPE.md: MCP fallback through a saved selection
	// policy is deferred to 0.9.0; until then OA refuses with a sentence.
	built := fellBack && strings.Contains(fmt.Sprint(out["model"]), cloudLocalModel) && reason != ""
	r.deferred("the next request is answered by the local model and names the model and reason (E4)", "0.9.0", built, sentence(res, text, out),
		"%s — %s", clip(firstNonEmpty(text, errString(err)), 300), gap)
	// No eligible local model left: a clear refusal.
	removed, rerr := e.cli(ctx, "provider", "remove", cloudLocalProvider)
	r.check("the local model is withdrawn", rerr == nil, "%v %s", rerr, strings.TrimSpace(removed))
	waitModelGone(ctx, agent, cloudLocalModel)
	res, text, out, err = agent.complete(ctx, cloudArgs)
	refusal := sentence(res, text, out)
	r.check("with no eligible local model the request is refused clearly", err == nil && out["outcome"] != "completed" && refusal != "",
		"%s", clip(firstNonEmpty(text, errString(err)), 400))
}

// waitModelGone reads the agent's list until model is no longer servable.
func waitModelGone(ctx context.Context, a *hostedAgent, model string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		_, text, _, err := a.s.call(ctx, "oa_models_list", map[string]any{"fresh": true})
		if err == nil && !strings.Contains(text, model) {
			return
		}
		time.Sleep(time.Second)
	}
}

// startCloudLocal declares the local fallback: a copy of this driver run as
// a native chat@1 leaf whose router serves one model through a loopback
// OpenAI-compatible server inside that process. It returns the reply marker
// the local model answers with.
func startCloudLocal(ctx context.Context, e *env, r *result) (string, bool) {
	self, err := os.Executable()
	if err != nil {
		r.check("local model host declared", false, "%v", err)
		return "", false
	}
	dir := filepath.Join(e.work, "local-chat")
	_ = os.MkdirAll(dir, 0o700)
	program := filepath.Join(dir, "oa-journey-local-chat.exe")
	if err := copyFile(self, program); err != nil {
		r.check("local model host declared", false, "%v", err)
		return "", false
	}
	marker := "local reply from " + cloudLocalModel
	out, err := e.cli(ctx, "provider", "add", cloudLocalProvider, "--program", program,
		"--provider-endpoint", "journey-local-"+newID()[:6],
		"--contract", "abstraction.inference/chat@1", "--profiles", "chat", "--model", cloudLocalModel,
		"--arg", hostedLocalChatArg, "--arg", "-endpoint", "--arg", "{endpoint}", "--arg", "-runtime", "--arg", e.runtime, "--arg", "-model", "--arg", cloudLocalModel)
	if !r.check("local model host declared", err == nil, "%v %s", err, strings.TrimSpace(out)) {
		return "", false
	}
	return marker, true
}

// runHostedLocalChat runs in the local fallback's process.
func runHostedLocalChat(args []string) int {
	var endpoint, runtimeProgram, model string
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "-endpoint":
			endpoint = args[i+1]
		case "-runtime":
			runtimeProgram = args[i+1]
		case "-model":
			model = args[i+1]
		}
	}
	if endpoint == "" || runtimeProgram == "" || model == "" {
		fmt.Fprintln(os.Stderr, "oa-journey-local-chat: -endpoint, -runtime and -model are required")
		return 2
	}
	if err := serveHostedLocalChat(endpoint, runtimeProgram, model); err != nil {
		fmt.Fprintln(os.Stderr, "oa-journey-local-chat:", err)
		return 1
	}
	return 0
}
