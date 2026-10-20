// Command oa-journey-app is the journey suite's stand-in for an installed
// application with an open verb, the way ComfyUI's native launch helper is
// one (release/testing/journeys.py, J-COMFY-OPEN). The driver copies it into
// an install folder and embeds its applications@3 manifest as the
// OA_APPLICATION_MANIFEST resource, so the runtime reads, keeps and starts it
// exactly as it does the real program.
//
//	oa-journey-app --register     self-registration, as Setup runs the helper
//	oa-journey-app register-self  the same, under the ComfyUI connector's verb
//	                              (J-CONNECT places it as comfyui-connector.exe)
//	oa-journey-app owned-host     the manifest's host recipe: the runtime's
//	                              owned launch starts this mode
//
// Both modes find the isolated runtime in journey-app.json beside the
// program (endpoint and runtime program); start_delay_ms there makes
// owned-host mode take that long to come up. Without that file the program
// refuses: it never discovers the person's installed runtime.
//
// In owned-host mode it announces itself through applications@3, renews its
// lease, and serves the owned MCP channel (OA length frames on stdin and
// stdout): server/discover, subscriptions/listen and tools/call for the open
// verb, which answers applied. It also serves the application model the way
// ComfyUI's host adapter does: app_describe and app_contexts, one context of
// saved workflows, and the read commands workflow.list {search?, source?} and
// workflow.inputs {workflow}. It writes every received method and every
// Invocation to journey-app.log and exits after withdrawing when stdin closes
// or a file named stop appears beside it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

const frameLimit = 1 << 20

type config struct {
	Endpoint       string `json:"endpoint"`
	RuntimeProgram string `json:"runtime_program"`
	Application    string `json:"application"`
	// StartDelayMs is how long owned-host mode takes to come up before it
	// announces itself, as a real application's start takes time.
	StartDelayMs int `json:"start_delay_ms"`
	// BackendDelayMs models a backend that starts after the owned host announces.
	BackendDelayMs int `json:"backend_delay_ms"`
}

var logMu sync.Mutex

func logf(dir, format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "journey-app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s pid=%d %s\n", time.Now().Format("15:04:05.000"), os.Getpid(), fmt.Sprintf(format, args...))
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dir := filepath.Dir(exe)
	raw, err := os.ReadFile(filepath.Join(dir, "journey-app.json"))
	var c config
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil || c.Endpoint == "" || c.RuntimeProgram == "" || c.Application == "" {
		fmt.Fprintln(os.Stderr, "oa-journey-app: journey-app.json beside the program names no isolated runtime; refusing")
		os.Exit(2)
	}
	logf(dir, "start %v", os.Args[1:])
	switch {
	case len(os.Args) == 2 && (os.Args[1] == "--register" || os.Args[1] == "register-self"):
		err = register(c, exe, os.Stdout)
	case len(os.Args) == 2 && os.Args[1] == "owned-host":
		err = ownedHost(c, dir)
	default:
		err = errors.New("usage: oa-journey-app --register | register-self | owned-host")
	}
	if err != nil {
		logf(dir, "exit: %v", err)
		fmt.Fprintln(os.Stderr, "oa-journey-app:", err)
		os.Exit(1)
	}
	logf(dir, "exit: ok")
}

func applicationsClient(ctx context.Context, c config) (*applications.ApplicationsClient, error) {
	who, err := identity.Self()
	if err != nil {
		return nil, err
	}
	machine := facade.NewVerified(c.Endpoint, listen.ServerExpectation{Principal: who, Program: identity.CanonicalProgramPath(filepath.Clean(c.RuntimeProgram))})
	binding, err := machine.ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	return applications.NewApplicationsClient(binding.Transport().WithContext(ctx)), nil
}

// register asks the runtime to add this program, as the installed helper does
// when Setup registers it; the person's Keep question decides.
func register(c config, exe string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := applicationsClient(ctx, c)
	if err != nil {
		return err
	}
	change, err := client.Register(applications.RegisterRequest{Program: identity.CanonicalProgramPath(exe)})
	if err != nil {
		return err
	}
	_ = json.NewEncoder(out).Encode(map[string]string{"outcome": change.Outcome.String(), "revision": change.Revision, "reason": change.Reason})
	if change.Outcome != applications.OutcomeApplied {
		return fmt.Errorf("self-registration %s: %s", change.Outcome.String(), change.Reason)
	}
	return nil
}

func ownedHost(c config, dir string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if c.StartDelayMs > 0 {
		logf(dir, "coming up in %d ms", c.StartDelayMs)
		time.Sleep(time.Duration(c.StartDelayMs) * time.Millisecond)
	}
	startup, stop := context.WithTimeout(ctx, 10*time.Second)
	client, err := applicationsClient(startup, c)
	if err != nil {
		stop()
		return fmt.Errorf("runtime: %w", err)
	}
	key, err := applications.NewRequestKey()
	if err != nil {
		stop()
		return err
	}
	presence := fwire.ApplicationPresence{Application: c.Application, LeaseMs: 30000, Interfaces: []fwire.ApplicationInterface{}, Contexts: []fwire.ApplicationContext{}}
	change, err := client.Announce(key, presence)
	stop()
	if err != nil {
		return fmt.Errorf("announce: %w", err)
	}
	logf(dir, "announce: %s %s instance=%s", change.Outcome, change.Reason, change.Instance)
	if change.Outcome != fwire.ApplicationOutcomeApplied || change.Instance == "" {
		return fmt.Errorf("announce: %s %s", change.Outcome, change.Reason)
	}
	instance := change.Instance
	defer func() {
		withdraw, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if client, err := applicationsClient(withdraw, c); err == nil {
			v, err := client.Withdraw(c.Application, instance)
			logf(dir, "withdraw: %+v %v", v, err)
		}
	}()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		renew := time.Now().Add(10 * time.Second)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
				logf(dir, "stop file")
				cancel()
				os.Stdin.Close()
				return
			}
			if time.Now().After(renew) {
				renew = time.Now().Add(10 * time.Second)
				rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
				client, err := applicationsClient(rctx, c)
				if err == nil {
					presence.Instance = instance
					k, _ := applications.NewRequestKey()
					var v fwire.ApplicationChange
					v, err = client.Announce(k, presence)
					if err == nil && (v.Outcome != fwire.ApplicationOutcomeApplied || v.Instance != instance) {
						err = fmt.Errorf("renewal %s %s", v.Outcome, v.Reason)
					}
				}
				rcancel()
				if err != nil {
					logf(dir, "renew: %v", err)
				}
			}
		}
	}()
	return serve(ctx, dir, instance, time.Duration(c.BackendDelayMs)*time.Millisecond, os.Stdin, os.Stdout)
}

// serve answers the owned MCP channel the runtime opens on the program's
// standard streams.
func serve(ctx context.Context, dir, instance string, backendDelay time.Duration, in io.Reader, out io.Writer) error {
	var backendReady time.Time
	write := func(v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return listen.WriteFrameTo(out, raw, frameLimit)
	}
	for {
		frame, err := listen.ReadFrameFrom(in, frameLimit)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		logf(dir, "method %s %s %s", request.Method, request.Params.Name, string(request.Params.Arguments))
		if len(request.ID) == 0 {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "server/discover":
			reply["result"] = map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"},
				"serverInfo": map[string]string{"name": "oa-journey-app", "version": "1"}, "capabilities": map[string]any{"tools": map[string]any{}}}
		case "subscriptions/listen":
			var id string
			_ = json.Unmarshal(request.ID, &id)
			err = write(map[string]any{"jsonrpc": "2.0", "method": "notifications/subscriptions/acknowledged", "params": map[string]any{
				"notifications": map[string]bool{"toolsListChanged": true, "resourcesListChanged": true},
				"_meta":         map[string]string{"io.modelcontextprotocol/subscriptionId": id}}})
			if err != nil {
				return err
			}
			continue
		case "tools/call":
			if record := appModelAnswer(dir, instance, request.Params.Name, request.Params.Arguments, backendDelay > 0, backendReady); record != nil {
				raw, err := appmodel.ToJSON(record)
				if err != nil {
					return err
				}
				reply["result"] = map[string]any{"resultType": "complete", "structuredContent": json.RawMessage(raw),
					"content": []map[string]string{{"type": "text", "text": string(raw)}}}
				break
			}
			if request.Params.Name != "open" {
				reply["error"] = map[string]any{"code": -32602, "message": "unknown tool " + request.Params.Name}
				break
			}
			if backendReady.IsZero() {
				backendReady = time.Now().Add(backendDelay)
			}
			activation := applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied}
			if time.Now().Before(backendReady) {
				activation = applications.HostActivationResult{Outcome: applications.HostActivationOutcomeRefused,
					Reason: "starting:loading_custom_nodes: ComfyUI's server is loading its custom nodes"}
			}
			canonical := applications.EncodeHostActivationResult(&activation)
			text := string(canonical)
			for len(text) > 0 && text[len(text)-1] == '\n' {
				text = text[:len(text)-1]
			}
			reply["result"] = map[string]any{"resultType": "complete", "structuredContent": json.RawMessage(text),
				"content": []map[string]string{{"type": "text", "text": text}}}
			logf(dir, "opened")
		default:
			reply["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		if err := write(reply); err != nil {
			return err
		}
	}
}

// The stand-in's application model: ComfyUI's saved-workflow commands.
const (
	journeyContext = "workflows"
	journeyEpoch   = "e1"
	journeyRev     = "r1"
)

var journeyWorkflows = []string{"z image turbo", "flux schnell", "z image upscale"}

// appModelAnswer answers an application-model tool, or nil for another
// tool. A command receives the Invocation the gateway built; the stand-in
// logs it and answers in its context only.
func appModelAnswer(dir, instance, tool string, arguments json.RawMessage, slowBackend bool, backendReady time.Time) any {
	startup := appmodel.ContextRef{Instance: instance, Context: "startup", Epoch: journeyEpoch}
	switch tool {
	case "app_describe":
		kind := appmodel.Kind{Name: "workflow", Title: "Workflow", Schema: `{"type":"object"}`, Fields: []appmodel.KindField{}}
		spec := func(name, title, input string) appmodel.CommandSpec {
			return appmodel.CommandSpec{Name: name, Title: title, Class: appmodel.CommandClassRead, InputSchema: input, OutputSchema: `{"type":"object"}`, Targets: []string{"workflow"}}
		}
		kinds := []appmodel.Kind{kind}
		if slowBackend {
			kinds = append(kinds, appmodel.Kind{Name: "backend", Title: "Backend", Schema: `{"type":"object"}`, Fields: []appmodel.KindField{}})
		}
		return &appmodel.DescriptionResult{Outcome: appmodel.ReadingOutcomePage, Description: &appmodel.Description{Version: "appmodel@1", Kinds: kinds,
			Commands: []appmodel.CommandSpec{
				spec("workflow.list", "List workflows", `{"type":"object","properties":{"search":{"type":"string"},"source":{"type":"string","enum":["saved","template"]}},"additionalProperties":false}`),
				spec("workflow.inputs", "Workflow inputs", `{"type":"object","properties":{"workflow":{"type":"string"}},"required":["workflow"],"additionalProperties":false}`),
			},
			Extensions: []string{},
			Bounds:     appmodel.ContextBounds{SnapshotBytes: 65536, ChangeBytes: 16384, RetainedRevisions: 16, PendingProposals: 1, CallsInFlight: 4}}}
	case "app_contexts":
		contexts := []appmodel.ContextEntry{{Context: appmodel.ContextRef{Instance: instance, Context: journeyContext, Epoch: journeyEpoch}, Title: "Saved workflows", Revision: journeyRev}}
		if slowBackend {
			contexts = append(contexts, appmodel.ContextEntry{Context: startup, Title: "Backend startup", Revision: journeyRev})
		}
		return &appmodel.ContextPage{Outcome: appmodel.ReadingOutcomePage, Contexts: contexts}
	case "app_read":
		request, err := appmodel.DecodeReadRequest(arguments)
		if err != nil || request.Context != startup {
			return &appmodel.ContextSnapshot{Outcome: appmodel.ReadingOutcomeStale}
		}
		phase, revision := "loading_custom_nodes", journeyRev
		if !backendReady.IsZero() && !time.Now().Before(backendReady) {
			phase, revision = "running", "r2"
		}
		return &appmodel.ContextSnapshot{Outcome: appmodel.ReadingOutcomePage, Context: startup, Revision: revision,
			Entities: []appmodel.Entity{{ID: "backend", Kind: "backend", Label: "ComfyUI backend", Properties: `{"phase":"` + phase + `","cause":""}`}}}
	case "workflow.list", "workflow.inputs":
	default:
		return nil
	}
	invocation, err := appmodel.DecodeInvocation(arguments)
	if err != nil {
		logf(dir, "invocation %s refused: %v", tool, err)
		return &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeInvalid, Reason: "invocation"}
	}
	logf(dir, "invocation %s context=%s epoch=%s mode=%s expected_revision=%s key=%s arguments=%s", tool, invocation.Context.Context, invocation.Context.Epoch, invocation.Mode, invocation.ExpectedRevision, invocation.IdempotencyKey, string(invocation.Arguments))
	if invocation.Context != (appmodel.ContextRef{Instance: instance, Context: journeyContext, Epoch: journeyEpoch}) {
		return &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeStale, Reason: "context", Revision: journeyRev}
	}
	if tool == "workflow.inputs" {
		return &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Revision: journeyRev, Effects: `{"inputs":[]}`}
	}
	var input struct {
		Search string `json:"search"`
	}
	_ = json.Unmarshal([]byte(invocation.Arguments), &input)
	found := []map[string]string{}
	for _, name := range journeyWorkflows {
		if input.Search == "" || strings.Contains(name, input.Search) {
			found = append(found, map[string]string{"name": name, "source": "saved"})
		}
	}
	effects, _ := json.Marshal(map[string]any{"workflows": found})
	return &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Revision: journeyRev, Effects: appmodel.Raw(effects)}
}
