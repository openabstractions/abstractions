package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
)

// An open whose application is still starting (architect ruling 2026-11-12,
// adopters/comfyui/connector/README.md, "OA starts a Comfy Desktop install" point 5). The application answers its
// open verb not ready with the reason "starting:<phase>: <sentence>" and
// keeps a context named startup whose entity carries phase and cause
// (adopters/comfyui/node/oa_native_host.py). The gateway then follows the
// start up to the call's wait_ms by reading that context, and answers:
//
//   - opened, with the instance, once phase reads running;
//   - unavailable with the application's plain cause once it reads failed;
//   - starting with the phase and a handle once wait_ms ran out. A later
//     call with that handle, alone or with wait_ms, follows the same start.
//
// The follow reads with oa_applications_describe and app_read only, so it
// asks the person nothing and starts nothing.

const (
	openWaitDefault = 20 * time.Second
	openWaitMax     = 10 * time.Minute
	openPoll        = time.Second
	openHandleLife  = time.Hour
	openHandleLimit = 64
	startupContext  = "startup"
)

// openPhaseWords are the person-facing words of each starting phase.
var openPhaseWords = map[string]string{
	"launching":            "launching",
	"verifying":            "launching",
	"idle":                 "launching",
	"loading_custom_nodes": "loading custom nodes",
	"listening":            "listening",
	"already_running":      "already starting in the person's own app",
}

type openHandle struct {
	application string
	instance    string
	title       string
	phase       string
	expires     time.Time
}

// openHandles are this gateway process's handles of starting opens.
type openHandles struct {
	mu      sync.Mutex
	handles map[string]openHandle
}

func (h *openHandles) add(application, instance, title, phase string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	handle := base64.RawURLEncoding.EncodeToString(raw)
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if h.handles == nil {
		h.handles = map[string]openHandle{}
	}
	for key, value := range h.handles {
		if now.After(value.expires) {
			delete(h.handles, key)
		}
	}
	if len(h.handles) >= openHandleLimit {
		var oldest string
		for key, value := range h.handles {
			if oldest == "" || value.expires.Before(h.handles[oldest].expires) {
				oldest = key
			}
		}
		delete(h.handles, oldest)
	}
	h.handles[handle] = openHandle{application: application, instance: instance, title: title, phase: phase, expires: now.Add(openHandleLife)}
	return handle, nil
}

func (h *openHandles) get(handle string) (openHandle, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	value, ok := h.handles[handle]
	if !ok || time.Now().After(value.expires) {
		return openHandle{}, false
	}
	return value, true
}

func (h *openHandles) note(handle, phase string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if value, ok := h.handles[handle]; ok {
		value.phase = phase
		h.handles[handle] = value
	}
}

type openInputError struct {
	reason  string
	message string
}

func (e openInputError) Error() string { return e.message }

const openVerbsHint = " Open verbs are listed in oa_applications_describe under open_verbs."

var errOpenHandle = openInputError{"invalid_handle", "The open handle is unavailable in this chat connection. Follow the returned handle in the chat connection that received it. If you still need to open the application from here, use its declared verb and payload."}

// openArguments takes handle and wait_ms out of an open's arguments. With a
// handle, the other arguments are not needed and are passed over.
func openArguments(raw json.RawMessage) (json.RawMessage, string, time.Duration, error) {
	fields := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, "", 0, openInputError{"invalid_arguments", "Pass oa_applications_open arguments as a JSON object."}
		}
	}
	wait := openWaitDefault
	if value, ok := fields["wait_ms"]; ok {
		var ms int64
		if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &ms) != nil || ms < 0 {
			return nil, "", 0, openInputError{"invalid_wait_ms", "Pass wait_ms as a non-negative whole number of milliseconds, or omit it to wait 20000 milliseconds."}
		}
		wait = time.Duration(min(ms, openWaitMax.Milliseconds())) * time.Millisecond
		delete(fields, "wait_ms")
	}
	var handle string
	if value, ok := fields["handle"]; ok {
		if json.Unmarshal(value, &handle) != nil || strings.TrimSpace(handle) == "" {
			return nil, "", 0, openInputError{"invalid_handle", "Pass handle as the non-empty string returned by oa_applications_open in this chat connection."}
		}
		return nil, handle, wait, nil
	}
	var application string
	value, ok := fields["application"]
	if !ok {
		return nil, "", 0, openInputError{"missing_application", "Pass application as the kept application's name."}
	}
	if json.Unmarshal(value, &application) != nil || strings.TrimSpace(application) == "" {
		return nil, "", 0, openInputError{"invalid_application", "Pass application as a non-empty string."}
	}
	if payload, ok := fields["payload"]; !ok {
		fields["payload"] = json.RawMessage("{}")
	} else if !bytes.HasPrefix(bytes.TrimSpace(payload), []byte("{")) {
		return nil, "", 0, openInputError{"invalid_payload", "Pass payload as a JSON object with the fields required by the declared verb."}
	}
	if value, ok := fields["context"]; ok && !bytes.HasPrefix(bytes.TrimSpace(value), []byte(`"`)) {
		return nil, "", 0, openInputError{"invalid_context", "Pass context as a string, or omit it."}
	}
	var verb string
	if _, ok := fields["default_verb"]; ok {
		return nil, "", 0, openInputError{"invalid_arguments", "Omit verb to use the application's only eligible open verb."}
	}
	if value, ok := fields["verb"]; ok {
		if json.Unmarshal(value, &verb) != nil || strings.TrimSpace(verb) == "" {
			return nil, "", 0, openInputError{"invalid_verb", "Pass verb as a non-empty string."}
		}
	} else {
		fields["default_verb"] = json.RawMessage("true")
	}
	rest, err := json.Marshal(fields)
	return rest, "", wait, err
}

// openStartingPhase is the phase word of an open its application answered
// starting, ok false for any other answer.
func openStartingPhase(out applications.ActivationResult) (string, bool) {
	if out.Outcome != applications.ActivationOutcomeNotReady {
		return "", false
	}
	rest, ok := strings.CutPrefix(out.Reason, "activation:refused: starting:")
	if !ok {
		return "", false
	}
	phase, _, _ := strings.Cut(rest, ":")
	return strings.TrimSpace(phase), true
}

// startupRead is one read of an application's startup context.
type startupRead struct {
	phase string
	cause string
}

// readStartup follows the instance that accepted the original activation.
func readStartup(ctx context.Context, backend AppModelBackend, bounded func(context.Context, func(context.Context) error) error, application, originalInstance string) (startupRead, error) {
	var listing applications.DescribeResult
	if err := bounded(ctx, func(ctx context.Context) (err error) {
		listing, err = backend.ApplicationsDescribe(ctx, appmodel.ApplicationsQuery{})
		return err
	}); err != nil {
		return startupRead{}, err
	}
	if listing.Outcome != applications.OutcomePage || listing.Answer == nil {
		return startupRead{}, nil
	}
	for _, app := range listing.Answer.Applications {
		if app.Application != application {
			continue
		}
		for _, instance := range app.Instances {
			if instance.Instance != originalInstance {
				continue
			}
			for _, entry := range instance.Contexts {
				if entry.Context.Context != startupContext || entry.Context.Instance != originalInstance {
					continue
				}
				var out applications.CallResult
				arguments := appmodel.EncodeReadRequest(&appmodel.ReadRequest{Context: entry.Context, Entities: []string{}, Limit: 16})
				if err := bounded(ctx, func(ctx context.Context) (err error) {
					out, err = backend.ApplicationsCall(ctx, appmodel.GatewayCall{Application: application, Instance: instance.Instance, Tool: "app_read", Arguments: string(arguments), RequireReadPermit: boolp(true)})
					return err
				}); err != nil {
					return startupRead{}, err
				}
				if out.Outcome != applications.OutcomeApplied || out.Answer == nil || out.Answer.Snapshot == nil ||
					out.Answer.Snapshot.Outcome != appmodel.ReadingOutcomePage || out.Answer.Snapshot.Context != entry.Context {
					return startupRead{}, nil
				}
				for _, entity := range out.Answer.Snapshot.Entities {
					var properties struct {
						Phase string `json:"phase"`
						Cause string `json:"cause"`
					}
					if json.Unmarshal([]byte(entity.Properties), &properties) == nil && properties.Phase != "" {
						return startupRead{phase: properties.Phase, cause: properties.Cause}, nil
					}
				}
				return startupRead{}, nil
			}
		}
	}
	return startupRead{}, nil
}

// openFollow follows a starting open until it is opened, fails or deadline
// passes. phase is the last phase known.
func openFollow(ctx context.Context, backend AppModelBackend, bounded func(context.Context, func(context.Context) error) error, handles *openHandles,
	handle string, value openHandle, deadline time.Time) (*mcp.CallToolResult, error) {
	application := value.application
	phase := value.phase
	for {
		read, err := readStartup(ctx, backend, bounded, application, value.instance)
		if err != nil && ctx.Err() != nil {
			return nil, err
		}
		if err == nil {
			switch {
			case read.phase == "running" || read.phase == "browser_failed":
				return openAnswer(application, applications.ActivationResult{Outcome: applications.ActivationOutcomeReady, Instance: value.instance, Started: true, Title: value.title}, "", "")
			case read.phase == "failed" || read.phase == "exited" || read.phase == "exit_unknown":
				cause := read.cause
				if cause == "" {
					cause = map[string]string{"failed": "it did not start", "exited": "it stopped while it started", "exit_unknown": "its start could not be confirmed"}[read.phase]
				}
				return openAnswer(application, applications.ActivationResult{Outcome: applications.ActivationOutcomeUnavailable, Reason: "activation:unavailable: " + cause, Title: value.title}, "", "")
			case read.phase == "desktop_idle":
				cause := read.cause
				if cause == "" {
					cause = "it waits for the person to start it"
				}
				return openAnswer(application, applications.ActivationResult{Outcome: applications.ActivationOutcomeNotReady, Reason: "activation:refused: " + cause, Title: value.title}, "", "")
			case read.phase != "":
				phase = read.phase
				handles.note(handle, phase)
			}
		}
		if !time.Now().Before(deadline) {
			return openAnswer(application, applications.ActivationResult{Outcome: applications.ActivationOutcomeNotReady, Reason: "activation:refused: starting:" + phase, Title: value.title}, handle, phase)
		}
		wait := min(openPoll, time.Until(deadline))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// openAnswer is an open's tool result: the native answer with status
// (opened, starting or the outcome), and for a start in progress its phase
// and handle, then the sentence the agent relays.
func openAnswer(application string, out applications.ActivationResult, handle, phase string) (*mcp.CallToolResult, error) {
	var record map[string]any
	if err := json.Unmarshal(applications.EncodeActivationResult(&out), &record); err != nil {
		return nil, err
	}
	status := out.Outcome.String()
	if out.Outcome == applications.ActivationOutcomeReady {
		status = "opened"
	}
	if handle != "" {
		status = "starting"
		record["phase"] = openPhaseWord(phase)
		record["handle"] = handle
	}
	record["status"] = status
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	result, err := appModelResult(raw, out.Outcome != applications.ActivationOutcomeReady)
	if err != nil {
		return nil, err
	}
	// A start in progress is no failure: the agent follows it by handle.
	result.IsError = result.IsError && handle == ""
	sentence := appModelOpenSentence(application, out)
	if handle != "" {
		app := application
		if out.Title != "" {
			app = out.Title
		}
		arguments, err := json.Marshal(map[string]string{"application": application, "handle": handle})
		if err != nil {
			return nil, err
		}
		sentence = app + " is starting (" + openPhaseWord(phase) + "). To wait for this start, call " + ToolApplicationsOpen + " with " + string(arguments) + ". This asks the person no question."
	}
	result.Content = append(result.Content, &mcp.TextContent{Text: sentence, Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}})
	return result, nil
}

func openPhaseWord(phase string) string {
	if word, ok := openPhaseWords[phase]; ok {
		return word
	}
	return "starting"
}
