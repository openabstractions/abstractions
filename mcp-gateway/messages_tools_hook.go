package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	facade "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/claude-plugin/hooks"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

var messagesHookPointer = func() string {
	templates, err := hooks.WakePrompt()
	if err != nil {
		return ""
	}
	return templates.Unticketed
}()

func sanitizedWakeSender(value string) string {
	var out []rune
	for _, r := range value {
		if unicode.IsSpace(r) {
			r = ' '
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == ' ' || r == '-' || r == '_' {
			out = append(out, r)
		}
		if len(out) == 32 {
			break
		}
	}
	return strings.TrimSpace(string(out))
}

func messagesTicketPointer(ticket, sender string, repeated bool) (string, error) {
	if !validCodexWakeTicket(ticket) {
		return "", errors.New("invalid wake ticket")
	}
	templates, err := hooks.WakePrompt()
	if err != nil {
		return "", err
	}
	template := templates.First
	sender = sanitizedWakeSender(sender)
	if sender == "" {
		template = templates.Anonymous
	}
	if repeated {
		template = templates.Repeat
	}
	if len(ticket) == 64 {
		template = templates.Legacy
	}
	return strings.ReplaceAll(strings.ReplaceAll(template, "{ticket}", ticket), "{sender}", sender), nil
}

// Persisted text is custody metadata, never an arbitrary instruction source.
func validWakePointer(text, ticket string, repeated bool) bool {
	if text == "" {
		return true
	}
	expected, err := messagesTicketPointer(ticket, "", repeated)
	if err != nil {
		return false
	}
	if text == expected {
		return true
	}
	if repeated || len(ticket) == 64 {
		return false
	}
	templates, err := hooks.WakePrompt()
	if err != nil {
		return false
	}
	prefix, suffix, ok := strings.Cut(strings.ReplaceAll(templates.First, "{ticket}", ticket), "{sender}")
	if !ok || !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		return false
	}
	sender := strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)
	return sender != "" && sanitizedWakeSender(sender) == sender && utf8.RuneCountInString(sender) <= 32
}

// Only a local OA-derived application resource may select its already
// authorized registered title. Paired claims and peer text never supply names.
func (b *messagesBackend) wakeSender(ctx context.Context, headers []wire.Header) string {
	if b.Backend == nil {
		return ""
	}
	names := map[string]bool{}
	now := time.Now().UnixMilli()
	for _, h := range headers {
		if h.RemoteOrigin != nil || h.To != "app:"+b.application || h.Context != b.contextName || h.Refused || h.ExpiresUnixMs <= now {
			continue
		}
		// Architect ruling B (2026-11-08): a note wakes like a question.
		eligible := h.Kind == wire.KindReply || h.Kind == wire.KindNote || h.Kind == wire.KindQuestion && h.TaskState != nil && *h.TaskState == wire.TaskStateSubmitted && h.AnswerByUnixMs > now
		if eligible && strings.HasPrefix(h.From, "app:") {
			names[strings.TrimPrefix(h.From, "app:")] = true
		}
	}
	if len(names) == 0 {
		return ""
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	page, err := b.Backend.Applications(call, gateway.ApplicationsInput{})
	if err != nil || page.Outcome != "page" {
		return ""
	}
	for _, app := range page.Applications {
		if app.Scope == "local" && names[app.Name] {
			if name := wakeSenderTitle(app); name != "" {
				return name
			}
		}
	}
	return ""
}

// wakeSenderTitle names a sender in the wake prompt, which the model reads as
// host text (AB-1s F5, 2026-11-09). An agent's title ("Claude", "Codex",
// "OpenCode") names only Setup's connector registration: the connector's
// routing name, kept, with that connector's image in this connector's own
// folder as its program. Any other kept app reads by its registered title
// when the title is plain ASCII, names no agent and claims no OA name.
// Everything else gets the prompt that names no sender.
func wakeSenderTitle(app gateway.Application) string {
	if app.Scope != "local" || app.SelfRegistered {
		return ""
	}
	if base := nativecli.AgentApplicationBase(app.Name); base != "" {
		title, _ := nativecli.AgentTitle(base)
		dir := wakeConnectorFolder()
		leaf := app.Program[strings.LastIndexAny(app.Program, `\/`)+1:]
		if dir == "" || nativecli.AgentApplicationName(leaf) != app.Name || !identity.SameSubjectProgram(filepath.Join(dir, leaf), app.Program) {
			return ""
		}
		return title
	}
	name := sanitizedWakeSender(app.Title)
	for i := 0; i < len(name); i++ {
		if name[i] >= utf8.RuneSelf {
			return ""
		}
	}
	if name == "" || client.ReservedApplicationTitle(name) {
		return ""
	}
	folded := wakeTitleFold(name)
	for _, base := range nativecli.AgentApplicationBases() {
		if title, _ := nativecli.AgentTitle(base); strings.Contains(folded, wakeTitleFold(title)) {
			return ""
		}
	}
	return name
}

// wakeTitleFold is the form an ASCII title is compared in: lower case,
// letters and digits only, with the digits and letters that read as o and l
// mapped to them ("C1aude", "CIaude" and "Cl aude" all fold to "claude").
func wakeTitleFold(title string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r == '0':
			out.WriteRune('o')
		case r == '1' || r == 'i' || r == '|':
			out.WriteRune('l')
		case r >= 'a' && r <= 'z' || r >= '2' && r <= '9':
			out.WriteRune(r)
		}
	}
	return out.String()
}

// wakeConnectorFolder is Setup's connector folder as this gateway knows it:
// the folder of its own image when that image is a connector. A test
// replaces it.
var wakeConnectorFolder = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir, leaf := filepath.Split(exe)
	if nativecli.AgentApplicationName(leaf) == "" {
		return ""
	}
	return dir
}

// Only availability/lost-read uncertainty may enter native observation recovery.
// Classify affirmative typed failures before any enclosing availability status.
func messagesWatchFailure(err error, uncertain string) string {
	var refusal *wire.Refusal
	var dispatch wire.DispatchError
	var service *wire.ServiceError
	var resolverRefusal *facade.Refusal
	var resolverDispatch facade.DispatchError
	var resolverService *facade.ServiceError
	if errors.Is(err, listen.ErrServerUntrusted) || errors.Is(err, context.Canceled) ||
		errors.As(err, &refusal) || errors.As(err, &dispatch) || errors.As(err, &service) ||
		errors.As(err, &resolverRefusal) || errors.As(err, &resolverDispatch) || errors.As(err, &resolverService) {
		return "invalid"
	}
	var resolution *client.ResolutionError
	if errors.As(err, &resolution) {
		if status, ok := resolution.Refusal(); ok && status == facade.ResolutionStatusForbidden {
			return "forbidden"
		}
		switch resolution.Status {
		case client.RuntimeUnavailable, client.UpgradeInProgress, client.ResolutionErrorStatus(facade.ResolutionStatusUnavailable.String()):
			return uncertain
		default:
			return "invalid"
		}
	}
	return uncertain
}

// One bounded headers-only observation, not a wake loop. The authenticated
// factory must belong to the enrolled native hook image. It creates no presence
// and has no body cursor or acknowledgment path. Native hints own eligibility.
func waitMessagesHook(ctx context.Context, native messagesClientFactory, application, contextName, revision string, waitMs int64, now func() time.Time) (string, error) {
	if native == nil || now == nil || application == "" || contextName == "" || waitMs < 0 || waitMs > 30000 {
		return "", errors.New("messages: invalid headers wait")
	}
	call, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	c, err := native(call)
	if err != nil {
		return "", err
	}
	page, err := c.Wait("", contextName, wire.ViewHeaders, "", revision, waitMs)
	if err != nil {
		return "", err
	}
	if page.Outcome != wire.OutcomePage {
		return "", errors.New("messages: headers unavailable")
	}
	if validMessageWake(page.Wake) && page.Wake != "" {
		return messagesHookPointer, nil
	}
	return "", nil
}

func (b *messagesBackend) messagesWatch(ctx context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
	if receiver, ok := b.chatBodyBackend(ctx); !ok {
		return gateway.MessagesWatchResult{Page: wire.Page{Outcome: wire.OutcomeUnavailable}, WatchMetadata: gateway.WatchMetadata{HealthSupported: true}}, nil
	} else if receiver != b {
		return receiver.MessagesWatch(ctx, in)
	}
	out := gateway.MessagesWatchResult{Page: wire.Page{Outcome: wire.OutcomeInvalid}, WatchMetadata: gateway.WatchMetadata{HealthSupported: true}}
	if !validMessageWake(in.ReserveWake) || in.WakeTicket != "" && (!validCodexWakeTicket(in.WakeTicket) || in.ReserveWake == "") {
		return out, nil
	}
	if in.Turn != "" && in.Turn != "ended" && in.Turn != "person" {
		return out, nil
	}
	// A model calls this same listed tool. Only the host adapter's key may
	// end the turn chain or hold a wake reservation (AB-1s F2).
	if (in.AdapterKey != "" || in.ReserveWake != "" || in.WakeTicket != "" || in.Turn != "") && !b.adapter.admits(in.AdapterKey, in.Turn != "") {
		out.Reason = "adapter_key"
		return out, nil
	}
	caller := agentAdmissionCaller(ctx)
	// The external adapter accepts at most256 characters, while the existing
	// generated Wait revision grammar is at most128 UTF-8 bytes. Refuse its
	// narrower native boundary before client encoding or any IPC dispatch.
	if b.native == nil || b.application == "" || b.inboxInstance() == "" || b.contextName == "" || in.WaitMs < 0 || in.WaitMs > 25000 || !utf8.ValidString(in.Revision) || len(in.Revision) > 128 || in.Health != "" && messagesWatchHealthText(in.Health) == "" {
		return out, nil
	}
	if in.Health != "" {
		observeMessagesWatchHealth(ctx, in.Health, b.contextName)
	}
	if b.status != nil {
		call, done, admitted := b.status.admission.begin(ctx)
		defer done()
		if !admitted {
			out.Outcome = wire.OutcomeUnavailable
			return out, nil
		}
		ctx = call
	}
	if in.Health != "" && b.status != nil && b.status.admission != nil {
		b.status.recordWatchHealth(ctx, in.Health)
	}
	call, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	failure := func(err error, uncertain string) string {
		// Preserve the initiating local cause across a faster fresh confirmation.
		// Only a pure owned cancellation is recoverable; joined trust/service
		// failures, outer cancellation, retirement and lease expiry retain refusal.
		if b.status != nil && b.status.admission != nil && caller.Err() == nil && context.Cause(call) == errAgentAdmissionSuspended && agentPresenceCompletion(err, true) == nil {
			a := b.status.admission
			a.mu.Lock()
			current := !a.closed && time.Now().Before(a.deadline)
			a.mu.Unlock()
			if current {
				return "unavailable"
			}
		}
		return messagesWatchFailure(err, uncertain)
	}
	c, err := b.native(call)
	if err != nil {
		out.Outcome, _ = wire.ParseOutcome(failure(err, "unavailable"))
		return out, nil
	}
	if in.Turn != "" {
		// The adapter's turn signal ends this inbox's woken turn chain.
		b.turnWake(c, "")
	}
	page, err := c.Wait("", b.contextName, wire.ViewHeaders, "", in.Revision, in.WaitMs)
	if err == nil && page.Outcome == wire.OutcomeGap && in.Revision != "" {
		// Headers acknowledge no bodies. A native owner restart invalidates
		// the old observation revision; restart once in this exact inbox.
		page, err = c.Wait("", b.contextName, wire.ViewHeaders, "", "", 0)
	}
	if err != nil {
		out.Outcome, _ = wire.ParseOutcome(failure(err, "unknown"))
		if out.Outcome == wire.OutcomeUnknown {
			out.Reason = messageReplyCause("Wait", err)
		}
		return out, nil
	}
	out.Outcome = page.Outcome
	if page.Outcome != wire.OutcomePage {
		return out, nil
	}
	if !validMessageWake(page.Wake) || page.Cursor != "" || len(page.Messages) != 0 || len(page.Headers) > 50 || page.PendingUntilUnixMs < 0 || page.ReplyUntilUnixMs < 0 || !utf8.ValidString(page.Revision) || len(page.Revision) > 128 {
		out.Outcome = wire.OutcomeInvalid
		return out, nil
	}
	out.Context = b.contextName
	out.Page = page
	out.Headers = nil
	if page.Wake != "" {
		out.WakeSender = b.wakeSender(ctx, page.Headers)
	}
	if in.ReserveWake != "" && in.ReserveWake == out.Wake {
		b.chatMu.Lock()
		reserve := b.wakeReserve
		pointer := b.wakePointer
		b.chatMu.Unlock()
		if reserve == nil {
			return gateway.MessagesWatchResult{Page: wire.Page{Outcome: wire.OutcomeUnavailable}, WatchMetadata: gateway.WatchMetadata{HealthSupported: true}}, nil
		}
		ticket, err := reserve(ctx, out.Wake, in.WakeTicket)
		if errors.Is(err, errCodexWakeBusy) {
			out.Reason = "receiver:busy"
			b.publishWakeBusy(ctx, true)
			return out, nil
		}
		if err != nil {
			return gateway.MessagesWatchResult{Page: wire.Page{Outcome: wire.OutcomeUnavailable}, WatchMetadata: gateway.WatchMetadata{HealthSupported: true}}, nil
		}
		out.WakeTicket = ticket
		if ticket != "" && pointer != nil {
			out.WakePointer, err = pointer(ctx, ticket, out.WakeSender)
			if err != nil || out.WakePointer == "" {
				return gateway.MessagesWatchResult{Page: wire.Page{Outcome: wire.OutcomeUnavailable}, WatchMetadata: gateway.WatchMetadata{HealthSupported: true}}, nil
			}
		}
	}
	return out, nil
}
