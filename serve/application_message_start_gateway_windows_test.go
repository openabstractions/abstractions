//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	rightspolicy "github.com/openabstractions/abstraction-rights/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile/startprofiletest"
)

// This fixture owns no model or vendor settings. Its only child is the actual
// gateway package compiled with an explicit isolated native server fixture.
func TestMessageStartVendorChild(t *testing.T) {
	raw := os.Getenv("OA_RECEIVER_START_VENDOR_FIXTURE")
	if raw == "" {
		return
	}
	var cfg struct{ Gateway, GatewayConfig, AllowedTools, Release string }
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		t.Fatal("fixture config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Gateway, "-test.run=^TestAgentStartGatewayFixture$", "-test.count=1")
	cmd.Env = append(os.Environ(), "OA_RECEIVER_START_GATEWAY_FIXTURE="+cfg.GatewayConfig)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		in.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	enc, dec := json.NewEncoder(in), json.NewDecoder(out)
	call := func(id int, method string, params any) json.RawMessage {
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := dec.Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.ID != id {
				continue
			}
			if len(response.Error) != 0 {
				t.Fatal(string(response.Error))
			}
			return response.Result
		}
	}
	call(1, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "inert-vendor-fixture", "version": "1"}})
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	tool := func(id int, name string, args any) []byte {
		if !strings.Contains(","+cfg.AllowedTools+",", ",mcp__OpenAbstractions__"+name+",") {
			t.Fatal("effective fixed profile forbids instructed tool", name)
		}
		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(call(id, "tools/call", map[string]any{"name": name, "arguments": args}), &result) != nil || result.IsError || len(result.Content) == 0 {
			t.Fatal("fixture tool failed", result)
		}
		return []byte(result.Content[0].Text)
	}
	var page *msg.Page
	for until := time.Now().Add(10 * time.Second); ; {
		pageText := string(tool(2, "oa_messages_wait", map[string]any{"wait_ms": 10000}))
		start := strings.Index(pageText, "\nOA_UNTRUSTED_")
		if start < 0 {
			t.Fatal("missing assistant-content boundary")
		}
		start++
		end := start + strings.Index(pageText[start:], "\n")
		if end <= start {
			t.Fatal("invalid assistant-content boundary")
		}
		boundary := pageText[start:end]
		if !strings.HasSuffix(pageText, boundary) {
			t.Fatal("unterminated assistant content")
		}
		page, err = msg.DecodePage([]byte(pageText[end+1 : len(pageText)-len(boundary)]))
		if err == nil && page.Outcome == msg.OutcomeUnavailable && time.Now().Before(until) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if err != nil || len(page.Messages) != 1 {
			t.Fatal(page, err)
		}
		break
	}
	question := page.Messages[0]
	if question.Header.Kind != msg.KindQuestion || question.Body != "fixture queued task" {
		t.Fatal("wrong canonical question", question)
	}
	reply, err := msg.DecodeSendResult(tool(3, "oa_messages_reply", map[string]any{"message_id": question.Header.MessageID, "body": "fixture completed task"}))
	if err != nil || reply.Outcome != msg.OutcomeSent {
		t.Fatal(reply, err)
	}
	// Keep this verified inbox alive until the fixture parent actually observes
	// readiness and the reply. Scheduling cannot consume a fixed sleep window.
	if !filepath.IsAbs(cfg.Release) || filepath.Dir(cfg.Release) != filepath.Dir(cfg.Gateway) || filepath.Base(cfg.Release) != "inbox-observed.fixture" {
		t.Fatal("private fixture release path")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(cfg.Release); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("parent did not observe native readiness/reply", ctx.Err())
		}
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestMessageStartGatewayQueuedQuestionNativeReply(t *testing.T) {
	messageStartGatewayRun(t, false)
}

// No vendor runs here. The fixture supplies the vendor's actual-session
// observation separately and runs the real gateway against native services.
func TestMessageColdGatewayBindsCanonicalThroughNativeSource(t *testing.T) {
	messageStartGatewayRun(t, false, true)
}

func TestMessageStartGatewayDispatchesRetainedCanonicalChat(t *testing.T) {
	messageStartGatewayRun(t, false, true, true)
}

func messageStartGatewayRun(t *testing.T, attach bool, coldMode ...bool) {
	dispatchCold := len(coldMode) > 1 && coldMode[1]
	// The inert vendor fixture owns no provider settings; a check started from
	// a Claude session would otherwise exercise the gateway refusal.
	startprofiletest.ClearProviderSelectors(t)
	run, _ := messages1608Fixture(t)
	run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	helper := filepath.Join(folder, "oa-claude-integration.exe")
	build := exec.Command("go", "test", "-c", "-o", helper, ".")
	build.Dir = filepath.Join(root, "mcp-gateway")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual gateway source fixture: %v\n%s", err, output)
	}
	vendor := ""
	if len(coldMode) == 0 || dispatchCold {
		vendor = filepath.Join(folder, "inert-vendor.test.exe")
		// This stand-in runs inside the profile's 256 MiB process limit.
		// Build it separately even when the serving test binary runs under -race.
		buildCtx, cancelBuild := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancelBuild()
		buildVendor := exec.CommandContext(buildCtx, "go", "test", "-c", "-race=false", "-o", vendor, ".")
		if output, err := buildVendor.CombinedOutput(); err != nil {
			t.Fatalf("build inert vendor source fixture: %v\n%s", err, output)
		}
	}
	p := startprofile.Profile{Name: "claude-v1", Context: "fixture-inbox", Folder: folder, StateDir: folder, CLIFileID: "inert-fixture-only", Model: "fixture-model", TimeoutMS: 30000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	// The fixture's `auth status` reports folder as the configuration folder,
	// so an attach run discovers an inert stand-in session file there.
	inboxContext, personSession, personFile, argvMarker, personText := p.Context, "", "", "", ""
	if attach {
		personSession = "2b7c1d3e-4f50-4a61-8b72-9c83d4e5f601"
		digest := sha256.Sum256([]byte("claude\x00" + personSession))
		inboxContext = "attach-" + hex.EncodeToString(digest[:12])
		// The session is filed the way Claude Code files it: under its own
		// working folder's project name, with that folder as its cwd. The
		// copy runs there.
		work := filepath.Join(folder, "fixture-work")
		if err := os.Mkdir(work, 0o700); err != nil {
			t.Fatal(err)
		}
		project := strings.Map(func(r rune) rune {
			if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				return r
			}
			return '-'
		}, work)
		sessions := filepath.Join(folder, "projects", project)
		if err := os.MkdirAll(sessions, 0o700); err != nil {
			t.Fatal(err)
		}
		cwd, _ := json.Marshal(work)
		personText = `{"type":"user","cwd":` + string(cwd) + `,"message":"inert transcript"}` + "\n"
		personFile = filepath.Join(sessions, personSession+".jsonl")
		if err := os.WriteFile(personFile, []byte(personText), 0o600); err != nil {
			t.Fatal(err)
		}
		argvMarker = filepath.Join(folder, "argv.fixture")
	}
	descriptor := wire.ApplicationDescriptor{Name: "oa-claude-integration", Program: identity.NormalizeSubjectProgram(helper), Title: "Fixture receiver", Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 15000}}
	initialDescriptor := descriptor
	initialDescriptor.Activation = nil
	if got := run.d.register(run.ctx, run.f.panel, initialDescriptor); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	agent1478Set(t, run, descriptor.Name, "", true)
	q := agent1478Question(t, run, descriptor.Name)
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, descriptor.Name, "enrolled")
	run.d.messageStartProfile = messageStartProfileResolver(func(_ context.Context, d wire.ApplicationDescriptor, profile startprofile.Profile) error {
		if d.Name != descriptor.Name || profile != p {
			return errors.New("fixture approval mismatch")
		}
		return nil
	})
	policy := run.f.a.rights
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{ApplicationLogs: true})
	if err != nil {
		t.Fatal(err)
	}
	policy.policy.Definitions = catalog
	for _, action := range []string{ActionApplicationMessage, grants.ApplicationMessageStartAction, ActionApplicationRead} {
		if err := policy.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []struct{ program, to string }{{program, descriptor.Name}, {helper, "native-agent"}} {
		if err := policy.policy.Set(rights.Subject{Account: policy.owner, Program: identity.NormalizeSubjectProgram(rule.program)}, ActionApplicationMessage, "app:"+rule.to, true); err != nil {
			t.Fatal(err)
		}
	}
	// The real retained owner and shared typed policy admit only this helper's
	// own named exit record. A write-only sink cannot prove retained completion.
	if err := policy.policy.RegisterAction(logging.ApplicationLogReadAction); err != nil {
		t.Fatal(err)
	}
	logTarget, err := logclient.ApplicationLogPermissionTarget(descriptor.Program)
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := policy.policy.SelectorSnapshotContext(run.ctx)
	if err != nil {
		t.Fatal(err)
	}
	logPermit, err := policy.policy.SetSelectorRuleContext(run.ctx, revision, rights.SelectorRule{Subject: rights.Subject{Account: policy.owner, Program: descriptor.Program}, Selection: rights.PermissionSelection{Permission: rights.PermissionKey{Action: logging.ApplicationLogReadAction, ResourceProfile: logging.ApplicationLogResourceProfile}, Selector: rights.ResourceSelector{Kind: rights.ResourceSelectorKindOne, Target: logTarget}}, Permit: true}, rightspolicy.RuleEdit{By: run.f.panel, Why: "owned fixture exit readback", Exact: true}, func() error { return nil })
	if err != nil || logPermit.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatal("named helper log grant", logPermit, err)
	}
	endpointID := fmt.Sprintf("%d-%d", os.Getpid(), endpointSeq.Add(1))
	endpoint := fullEndpoint(t, "receiver-start-applications-"+endpointID)
	apps, err := listenApplications(endpoint, run.d, policy.owner, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	apps.messages, err = openMessageStore(run.state, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	apps.messageRights = policy
	apps.v3 = &applicationV3Gateway{directory: run.d, epoch: run.d.epoch}
	runtimeEndpoint := fullEndpoint(t, "receiver-start-runtime-"+endpointID)
	retained, err := logging.OpenRetainedFileSink(filepath.Join(t.TempDir(), "exit.jsonl"), logging.LogsRetention{MaxUse: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := retained.Close(); err != nil {
			t.Error(err)
		}
	})
	host, err := facaderuntime.Listen(facaderuntime.Options{Endpoint: runtimeEndpoint, LogEndpoint: fullEndpoint(t, "receiver-start-log-"+endpointID), ConfigEndpoint: fullEndpoint(t, "receiver-start-config-"+endpointID), ConfigWithoutMachine: true, Sink: retained, LogApplicationHistoryPolicy: facaderuntime.ApplicationHistoryPolicyFromRights(policy.policy), Applications: apps, ApplicationsEndpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(run.ctx, 40*time.Second)
	hostDone := make(chan error, 1)
	var observedPermission atomic.Pointer[string]
	a := run.d.agentQuestions.a
	policy.firstUse = func(subject rights.Subject, publisher func() asks.Publisher, action, resource string) string {
		permissionID := a.admit(subject, publisher, action, resource)
		if permissionID != "" {
			observedPermission.Store(&permissionID)
		}
		return permissionID
	}
	go func() { hostDone <- host.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		host.Close()
		if err := <-hostDone; err != nil {
			t.Error(err)
		}
	})
	if dispatchCold {
		personSession = "11111111-1111-4111-8111-111111111111"
		launch := &msg.ChatLaunch{Session: personSession, Origin: msg.ChatOriginDesktop, Cwd: folder, Mode: msg.ChatModeAcceptEdits}
		inboxContext, err = apps.messages.contextForChat(ctx, policy.owner, descriptor.Program, personSession, &msg.ChatBinding{ChatID: personSession, Launch: launch})
		if err != nil {
			t.Fatal(err)
		}
		argvMarker = filepath.Join(folder, "cold-argv.fixture")
	}
	if len(coldMode) != 0 && coldMode[0] && !dispatchCold {
		launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Origin: msg.ChatOriginDesktop, Cwd: folder, Mode: msg.ChatModeAcceptEdits}
		claim := &msg.ChatBinding{ChatID: launch.Session, Launch: launch}
		canonical, err := apps.messages.contextForChat(ctx, policy.owner, descriptor.Program, launch.Session, claim)
		if err != nil {
			t.Fatal(err)
		}
		cwdKey, err := identity.FileKey(folder)
		if err != nil {
			t.Fatal(err)
		}
		// Keep execution gated off. Queue through the ordinary admitted native
		// sender, then consume and reply through the configured cold gateway.
		run.d.messageStartProfile = nil
		senderCall, _ := messages1608Call(t)
		senderReceiver := &messagesReceiver{host: apps, call: senderCall, ctx: ctx}
		senderInstance, sender := messagesStateOpen(t, &messagesObserveRun{run: run}, senderReceiver, "native-agent", "cold-sender")
		sent, err := sender.Send(senderInstance, "cold-sender", msg.SendRequest{To: "app:" + descriptor.Name, Context: canonical, Kind: msg.KindQuestion, Body: "queued cold fixture", IdempotencyKey: "cold-binding-question", ExpiresUnixMs: time.Now().Add(2 * time.Minute).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()})
		if err != nil || sent.Outcome != msg.OutcomeSent {
			t.Fatal("queue native cold question", sent, err)
		}
		raw, _ := json.Marshal(struct {
			Endpoint, State, Application, Context string
			Server                                listen.ServerExpectation
			ColdBinding, ColdCwdKey, ColdContext  string
		}{runtimeEndpoint, folder, descriptor.Name, "native-cold-source", runtimeExpectation(t), string(msg.EncodeChatBinding(claim)), cwdKey, canonical})
		connect := func(binary, actualSession string, directories ...string) *mcp.ClientSession {
			t.Helper()
			command := exec.CommandContext(ctx, binary, "-test.run=^TestAgentStartGatewayFixture$", "-test.count=1")
			command.Dir = folder
			if len(directories) != 0 {
				command.Dir = directories[0]
			}
			command.Env = append(os.Environ(), "OA_RECEIVER_START_GATEWAY_FIXTURE="+string(raw), "CLAUDE_CODE_SESSION_ID="+actualSession)
			command.Stderr = os.Stderr
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "inert-cold-host", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cs.Close() })
			return cs
		}
		cs := connect(helper, launch.Session)
		var page struct {
			Outcome string `json:"outcome"`
		}
		until := time.Now().Add(10 * time.Second)
		for {
			out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_wait", Arguments: map[string]any{"wait_ms": 0}})
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(out.StructuredContent)
			_ = json.Unmarshal(encoded, &page)
			if !out.IsError && page.Outcome == "page" {
				body, err := msg.DecodePage(encoded)
				if err != nil || len(body.Messages) != 1 || body.Messages[0].Header.MessageID != sent.MessageID || body.Messages[0].Body != "queued cold fixture" {
					t.Fatal("cold canonical body", body, err)
				}
				break
			}
			if time.Now().After(until) {
				t.Fatalf("cold native source stayed unavailable: %s", encoded)
			}
			time.Sleep(20 * time.Millisecond)
		}
		replied, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_reply", Arguments: map[string]any{"message_id": sent.MessageID, "body": "cold fixture reply"}})
		if err != nil {
			t.Fatal(err)
		}
		replyJSON, _ := json.Marshal(replied.StructuredContent)
		reply, err := msg.DecodeSendResult(replyJSON)
		if err != nil || replied.IsError || reply.Outcome != msg.OutcomeSent {
			t.Fatal("cold sealed reply", reply, err)
		}
		returned, err := sender.Wait(senderInstance, "cold-sender", msg.ViewBody, "", "", 0)
		if err != nil || !messagesReceiverHasReply(returned, "cold fixture reply") {
			t.Fatal("original native sender did not receive reply", returned, err)
		}
		if out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "oa_claude_session_start", Arguments: map[string]any{}}); err == nil && !out.IsError {
			t.Fatal("model created private lifecycle")
		}
		rows, err := apps.messages.retainedChats(ctx, policy.owner, descriptor.Program)
		if err != nil || len(rows) != 1 || rows[0].Launch == nil || *rows[0].Launch != *launch {
			t.Fatal("cold plan overwrote original launch", rows, err)
		}
		cs.Close()
		wrong := connect(helper, "22222222-2222-4222-8222-222222222222")
		out, err := wrong.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_wait", Arguments: map[string]any{"wait_ms": 0}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(out.StructuredContent)
		_ = json.Unmarshal(encoded, &page)
		if page.Outcome != "unavailable" {
			t.Fatal("wrong actual vendor session bound canonical", string(encoded))
		}
		wrong.Close()
		wrongCwd := connect(helper, launch.Session, t.TempDir())
		out, err = wrongCwd.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_wait", Arguments: map[string]any{"wait_ms": 0}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ = json.Marshal(out.StructuredContent)
		_ = json.Unmarshal(encoded, &page)
		if page.Outcome != "unavailable" {
			t.Fatal("wrong native cwd bound canonical", string(encoded))
		}
		wrongCwd.Close()
		foreign := filepath.Join(t.TempDir(), "oa-claude-integration.exe")
		image, err := os.ReadFile(helper)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(foreign, image, 0600); err != nil {
			t.Fatal(err)
		}
		unregistered := connect(foreign, launch.Session)
		out, err = unregistered.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_wait", Arguments: map[string]any{"wait_ms": 0}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ = json.Marshal(out.StructuredContent)
		_ = json.Unmarshal(encoded, &page)
		if page.Outcome != "unavailable" {
			t.Fatal("unregistered native image bound canonical", string(encoded))
		}
		unregistered.Close()
		return
	}
	gatewayConfig, err := json.Marshal(struct {
		Endpoint, State, Application, Context string
		Server                                listen.ServerExpectation
	}{runtimeEndpoint, folder, descriptor.Name, inboxContext, runtimeExpectation(t)})
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(folder, "inbox-observed.fixture")
	vendorConfig, err := json.Marshal(struct{ Gateway, GatewayConfig, Release string }{helper, string(gatewayConfig), release})
	if err != nil {
		t.Fatal(err)
	}
	probeMarker := filepath.Join(folder, "preflight.fixture")
	ownedConfig, err := json.Marshal(struct {
		Endpoint, Vendor, VendorConfig, Context, ProbeMarker, ArgvMarker string
		Server                                                           listen.ServerExpectation
		Cold                                                             bool
	}{runtimeEndpoint, vendor, string(vendorConfig), inboxContext, probeMarker, argvMarker, runtimeExpectation(t), dispatchCold})
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "OA_RECEIVER_START_OWNED_FIXTURE="+string(ownedConfig))
	if dispatchCold {
		// The real vendor spawn must drop this enclosing session. Only the inert
		// vendor below supplies its independently observed resumed ID to the child.
		for _, name := range []string{"CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CLAUDE_CODE_HOST_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_PID", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH", "CLAUDE_AGENT_SDK_FIXTURE", "CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_ORGANIZATION_UUID", "CLAUDE_CODE_USER_EMAIL", "CLAUDE_CODE_SUBSCRIPTION_TYPE", "CLAUDE_CODE_RATE_LIMIT_TIER", "CLAUDE_CODE_OAUTH_SCOPES"} {
			environment = append(environment, name+"=inert-enclosing-session-fixture")
		}
	}
	if err := policy.policy.Set(rights.Subject{Account: policy.owner, Program: descriptor.Program}, ActionApplicationRead, "app:"+descriptor.Name, true); err != nil {
		t.Fatal(err)
	}
	// Configure the existing kept helper through the actual generated @3 RPC.
	// The test executable is this isolated runtime's explicit native operator.
	priorOperator := run.d.operator
	run.d.operator = func(candidate string) bool { return samePrograms(candidate, program) || priorOperator(candidate) }
	if err := policy.policy.Set(rights.Subject{Account: policy.owner, Program: identity.NormalizeSubjectProgram(program)}, ActionApplicationManage, "account", true); err != nil {
		t.Fatal(err)
	}
	beforeEnrollment := run.d.currentAgentEnrollment(ctx, descriptor.Name)
	configuration := applications.NewApplicationsClient(listen.FrameClient{Endpoint: endpoint}.WithContext(ctx))
	if changed, err := configuration.SetActivation(applications.ActivationConfiguration{Expected: initialDescriptor, Activation: descriptor.Activation}); err != nil || changed.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(changed, err)
	}
	afterEnrollment := run.d.currentAgentEnrollment(ctx, descriptor.Name)
	if !reflect.DeepEqual(beforeEnrollment, afterEnrollment) {
		t.Fatal("configuration changed kept enrollment", beforeEnrollment, afterEnrollment)
	}
	ownedLaunch := newOwnedMessageStartLauncher(run.d, apps.ctx)
	dispatchDone := make(chan error, 1)
	run.d.launchMessageStart = func(ctx context.Context, request applicationMessageStartRequest, authority func(context.Context) error) error {
		err := ownedLaunch(ctx, request, authority)
		_, markerErr := os.Stat(probeMarker)
		t.Logf("owned fixture dispatch: err=%v preflight=%t", err, markerErr == nil)
		if err != nil {
			if body, readErr := os.ReadFile(probeMarker + "-child-error"); readErr == nil {
				t.Logf("inert child: %s", body)
			}
		}
		dispatchDone <- err
		return err
	}
	run.d.messageStartOwned.open = openReceiverSourceFixtureImage
	var launched atomic.Int32
	var helperMu sync.Mutex
	var helperProcess *os.Process
	helperDone := make(chan error, 1)
	var helperJoined bool
	run.d.messageStartOwned.start = func(ctx context.Context, image ownedApplicationImage, binding ownedApplicationBinding, args []string, authority func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error) {
		if !samePrograms(image.program, descriptor.Program) || !reflect.DeepEqual(args, p.Arguments()) {
			return nil, errors.New("nonregistered launch")
		}
		launched.Add(1)
		return startReceiverSourceFixture(ctx, image, binding, args, authority, environment, helperDone, func(process *os.Process) { helperMu.Lock(); helperProcess = process; helperMu.Unlock() })
	}
	t.Cleanup(func() {
		if launched.Load() != 0 && !helperJoined {
			select {
			case err := <-helperDone:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				helperMu.Lock()
				if helperProcess != nil {
					helperProcess.Kill()
				}
				helperMu.Unlock()
				<-helperDone
				t.Error("owned source fixture did not join")
			}
		}
	})
	// The same kept binary and full registered literal recipe carry no permit
	// when an ordinary application starts a separate native occurrence.
	direct := exec.CommandContext(ctx, helper, p.Arguments()...)
	direct.Env = environment
	output, directErr := direct.CombinedOutput()
	var directExit *exec.ExitError
	if !errors.As(directErr, &directExit) || directExit.ExitCode() != 3 || len(output) != 0 {
		t.Fatal("ordinary helper invocation not refused", directErr, string(output))
	}
	if _, err := os.Stat(probeMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unowned invocation reached vendor preflight", err)
	}
	call, _ := messages1608Call(t)
	reader := &messagesReceiver{host: apps, call: call, ctx: ctx}
	f := &messagesObserveRun{run: run}
	instance, sender := messagesStateOpen(t, f, reader, "native-agent", "source")
	if err := run.d.refreshMessageParties(ctx); err != nil {
		t.Fatal(err)
	}
	// The recipe's chat has no live inbox, so a program-only send answers
	// no_reachable_chat; the sender names it by its context (architect
	// ruling 2026-11-12, rule 5).
	addressed := inboxContext
	if attach {
		// A fixture stands in for the launcher's read-only session listing.
		updated := time.Now().UnixMilli()
		run.d.messageAttachSessions = func(_ context.Context, application string) ([]applicationAttachSession, error) {
			if application != descriptor.Name {
				return nil, nil
			}
			return []applicationAttachSession{{Context: inboxContext, Folder: "C--fixture-work", Updated: updated}}, nil
		}
		addressed = inboxContext
	}
	// The registered recipe and an existing session both have no live inbox;
	// a question starts or wakes each of them.
	listed, err := messagesBaseListing(reader, msg.InboxQuery{})
	registered, existing := false, !attach
	for _, inbox := range listed.Inboxes {
		starts := inbox.Program == "app:"+descriptor.Name && !inbox.Watching && inbox.Reach != nil && *inbox.Reach == msg.InboxReachStartsOnQuestion
		registered = registered || starts && inbox.Context == p.Context && inbox.Status == "Inactive registered receiver"
		existing = existing || starts && inbox.Context == inboxContext && inbox.Status == "Existing session in C--fixture-work"
	}
	if err != nil || listed.Outcome != msg.OutcomePage || !registered || !existing {
		t.Fatal("inactive receivers not listed as starting on a question", listed, err)
	}
	prepared, err := sender.Prepare(instance, "source", msg.PrepareRequest{Receiver: "app:" + descriptor.Name, Context: addressed, Subject: "fixture question", Body: "fixture queued task", TTLMs: 60000, AnswerWithinMs: 30000})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil || prepared.SendRequest.Context != inboxContext || launched.Load() != 0 {
		t.Fatal("inactive registered default preparation", prepared, err)
	}
	request := *prepared.SendRequest
	if attach {
		gone := request
		gone.Context, gone.IdempotencyKey = "attach-ffffffffffffffffffffffff", "deleted-session"
		if got, err := sender.Send(instance, "source", gone); err != nil || got.Outcome != msg.OutcomeUnavailable || got.Reason != "session_unknown" {
			t.Fatal("undiscovered session queued", got, err)
		}
	}
	sent, err := sender.Send(instance, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	actor, _, _, ok := reader.owner(instance, "source")
	if !ok {
		t.Fatal("native source")
	}
	key := apps.messages.key(actor, request.IdempotencyKey)
	record, err := apps.messages.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := apps.progressMessageStartSettled(ctx, key, record); err != nil {
		t.Fatal(err)
	}
	permission := observedPermission.Load()
	if permission == nil || launched.Load() != 0 {
		t.Fatal("start preceded native person approval", permission)
	}
	permissionID := *permission
	if _, outcome, err := a.book.AnswerApplication(permissionID, "allow"); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
	snapshot, err := a.rights.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := a.applyOperatorAllow(ctx, a.rights.self(), snapshot.Revision, permissionID, func() error { return nil }); err != nil || out.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatal(out, err)
	}
	if err := apps.progressMessageStartSettled(ctx, key, record); err != nil {
		t.Fatal(err)
	}
	// The host's own progress pass may have claimed the start first; this
	// call then returns at once while that pass waits for readiness. Wait for
	// the claimed start's outcome rather than reading the claim (architect
	// review 41).
	current, err := apps.messages.readRecord(ctx, key)
	for settle := time.Now().Add(30 * time.Second); err == nil && current != nil && current.Start != nil && current.Start.State == "unknown" && time.Now().Before(settle); {
		time.Sleep(50 * time.Millisecond)
		current, err = apps.messages.readRecord(ctx, key)
	}
	if err != nil || current.Start == nil || current.Start.State != "ready" {
		state := "missing"
		if current != nil && current.Start != nil {
			state = current.Start.State
		}
		t.Fatal("actual native inbox not ready", state, err)
	}
	page, err := sender.Wait(instance, "source", msg.ViewBody, "", "", 10000)
	if err != nil || !messagesReceiverHasReply(page, "fixture completed task") {
		t.Fatal(page, err)
	}
	if page.Messages[len(page.Messages)-1].Header.ContextID != sent.ContextID {
		t.Fatal("canonical correlation changed")
	}
	if err := os.WriteFile(release, []byte("observed"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-dispatchDone:
		if err != nil {
			t.Fatal("owned activation did not report retained completion", err)
		}
	case <-ctx.Done():
		t.Fatal("owned completion did not join", ctx.Err())
	}
	select {
	case err := <-helperDone:
		helperJoined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not exit")
	}
	if dispatchCold {
		raw, err := os.ReadFile(argvMarker)
		var flags []string
		if err != nil || json.Unmarshal(raw, &flags) != nil || !reflect.DeepEqual(flags, []string{"--resume", personSession}) {
			t.Fatal("cold dispatch forked or selected another session", string(raw), err)
		}
		rows, err := apps.messages.retainedChats(ctx, policy.owner, descriptor.Program)
		if err != nil || len(rows) != 1 || rows[0].Launch == nil || rows[0].Launch.Session != personSession || rows[0].Launch.Mode != msg.ChatModeAcceptEdits {
			t.Fatal("cold execution replaced historical launch", rows, err)
		}
	}
	if attach {
		raw, err := os.ReadFile(argvMarker)
		var flags []string
		if err != nil || json.Unmarshal(raw, &flags) != nil || len(flags) != 5 || flags[0] != "--resume" || flags[1] != personSession || flags[2] != "--fork-session" || flags[3] != "--session-id" || flags[4] == personSession {
			t.Fatal("launcher did not fork the addressed session", string(raw), err)
		}
		if after, err := os.ReadFile(personFile); err != nil || string(after) != personText {
			t.Fatal("person's session file changed", err)
		}
	}
	// The next receiver occurrence uses the same persisted recipe context.
	// Preparing after this occurrence exited creates no second launch.
	if err := run.d.refreshMessageParties(ctx); err != nil {
		t.Fatal(err)
	}
	prepared, err = sender.Prepare(instance, "source", msg.PrepareRequest{Receiver: "app:" + descriptor.Name, Context: p.Context, Body: "next task"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil || prepared.SendRequest.Context != p.Context || launched.Load() != 1 {
		t.Fatal("default context changed after receiver exit", prepared, err, launched.Load())
	}
	t.Log(fmt.Sprintf("registered fixed recipe -> fixture vendor -> native admitted gateway -> canonical queue -> reply %s", sent.MessageID))
}
