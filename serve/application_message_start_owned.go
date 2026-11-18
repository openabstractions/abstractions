package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

type ownedMessageStart struct {
	ctx       context.Context
	request   applicationMessageStartRequest
	authority func(context.Context) error
	confirmed bool
}

// This is live process custody for already durable admission. Restart never
// reconstructs a child, channel or dispatch from these values.
type ownedMessageStarts struct {
	mu      sync.Mutex
	d       *applicationDirectory
	ctx     context.Context
	rows    map[*ownedApplicationSession]*ownedMessageStart
	pending int
	changed chan struct{}
	open    func(context.Context, applicationMessageStartRequest) (ownedApplicationImage, error)
	start   func(context.Context, ownedApplicationImage, ownedApplicationBinding, []string, func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error)
}

func newOwnedMessageStartLauncher(d *applicationDirectory, lifetime context.Context) func(context.Context, applicationMessageStartRequest, func(context.Context) error) error {
	b := &ownedMessageStarts{d: d, ctx: lifetime, rows: map[*ownedApplicationSession]*ownedMessageStart{}, changed: make(chan struct{}), open: openMessageStartImage, start: startOwnedApplication}
	d.messageStartOwned = b
	return b.launch
}

func (b *ownedMessageStarts) notifyLocked() { close(b.changed); b.changed = make(chan struct{}) }

func messageStartDigest(s string) bool {
	v, err := hex.DecodeString(s)
	return err == nil && len(v) == 32 && hex.EncodeToString(v) == s
}

func (b *ownedMessageStarts) launch(wait context.Context, request applicationMessageStartRequest, authority func(context.Context) error) error {
	return b.run(wait, request, authority, func(ctx context.Context, session *ownedApplicationSession) error {
		return session.dispatchReceiverStart(ctx, authority, request)
	})
}

// list runs the registered launcher once to list the agent's existing
// conversations (receiver_sessions). It needs no question, reservation or
// start permit: the launcher starts nothing. The live enrollment and recipe
// checks guard the invocation like a start's.
func (b *ownedMessageStarts) list(wait context.Context, request applicationMessageStartRequest) ([]byte, error) {
	var page []byte
	authority := func(ctx context.Context) error { return b.d.recheckMessageStart(ctx, request) }
	err := b.run(wait, request, authority, func(ctx context.Context, session *ownedApplicationSession) error {
		var err error
		page, err = session.dispatchReceiverSessions(ctx, authority, request)
		return err
	}, true)
	return page, err
}

func (b *ownedMessageStarts) run(wait context.Context, request applicationMessageStartRequest, authority func(context.Context) error, dispatch func(context.Context, *ownedApplicationSession) error, bounded ...bool) error {
	if authority == nil || b.ctx == nil || b.open == nil || b.start == nil || request.Descriptor.Activation == nil || request.Descriptor.Activation.ReadinessTimeoutMs <= 0 || request.Descriptor.Activation.ReadinessTimeoutMs > 60000 || !messageStartDigest(request.ProfileFingerprint) {
		return errors.New("message start: owned admission unavailable")
	}
	profile, err := startprofile.Parse(request.Descriptor.Activation.Arguments)
	if err != nil || profile.Fingerprint(request.Descriptor.Program) != request.ProfileFingerprint {
		return errors.New("message start: exact recipe unavailable")
	}
	if err := wait.Err(); err != nil {
		return err
	}
	// Accepted work belongs to the runtime and its registered bound, independently
	// of a progress pass's wait deadline or its observation of native availability.
	parent := b.ctx
	if len(bounded) == 1 && bounded[0] {
		// A listing runs within its caller's bound (the discovery refresh's 15
		// seconds) and ends with the host as well.
		parent = wait
	}
	lifetime, cancel := context.WithTimeout(parent, time.Duration(profile.TimeoutMS+uint64(request.Descriptor.Activation.ReadinessTimeoutMs))*time.Millisecond)
	defer context.AfterFunc(b.ctx, cancel)()
	defer cancel()
	if err := authority(lifetime); err != nil {
		return err
	}
	// The woken turn ends at the first refused check: a stop from Activity or
	// a withdrawn start permit.
	if request.Continue != nil {
		defer superviseMessageStartAuthority(lifetime, request.Continue, messageWakeCheck, cancel)()
	}
	b.mu.Lock()
	b.pending++
	b.notifyLocked()
	b.mu.Unlock()
	pending := true
	defer func() {
		if pending {
			b.mu.Lock()
			b.pending--
			b.notifyLocked()
			b.mu.Unlock()
		}
	}()
	image, err := b.open(lifetime, request)
	if err != nil {
		return err
	}
	binding := ownedApplicationBinding{application: request.Application, account: request.Account, desktop: request.Session, runtimeEpoch: b.d.epoch, manifestRevision: request.RegistrationBinding, imageCode: request.ProgramKey}
	session, err := b.start(lifetime, image, binding, append([]string(nil), request.Descriptor.Activation.Arguments...), func(ctx context.Context, _ ownedApplicationBinding) error { return authority(ctx) })
	if err != nil {
		return err
	}
	row := &ownedMessageStart{ctx: lifetime, request: request, authority: authority}
	b.mu.Lock()
	b.rows[session] = row
	b.pending--
	pending = false
	b.notifyLocked()
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.rows, session)
		b.notifyLocked()
		b.mu.Unlock()
		_ = session.Close()
	}()
	if err := session.initialize(lifetime, "2025-11-25"); err != nil {
		return err
	}
	b.mu.Lock()
	confirmed := b.rows[session] == row && row.confirmed
	b.mu.Unlock()
	if !confirmed {
		return errors.New("message start: native invocation unconfirmed")
	}
	return dispatch(lifetime, session)
}

// dispatchReceiverSessions asks the confirmed launcher for its read-only
// listing and returns its JSON page.
func (s *ownedApplicationSession) dispatchReceiverSessions(ctx context.Context, authority func(context.Context) error, request applicationMessageStartRequest) ([]byte, error) {
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { s.turn <- struct{}{} }()
	if s.protocol != "2025-11-25" || s.next != 1 {
		return nil, errors.New("message sessions: one initialized dispatch required")
	}
	binding := startprofile.CustodyBinding{Account: request.Account, ProgramKey: request.ProgramKey, EnrollmentRevision: request.EnrollmentRevision, RecipeDigest: request.ProfileFingerprint}
	if !binding.Valid() {
		return nil, errors.New("message sessions: custody binding unavailable")
	}
	parameters, _ := json.Marshal(map[string]any{"name": "receiver_sessions", "arguments": map[string]any{}, "_meta": binding.Metadata()})
	result, err := s.exchange(ctx, "tools/call", ownedApplicationParams(parameters), func() error { return authority(ctx) })
	if err != nil {
		return nil, err
	}
	var reply struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &reply); err != nil || reply.IsError || len(reply.Content) != 1 || reply.Content[0].Type != "text" {
		return nil, errors.New("message sessions: malformed owned result")
	}
	return []byte(reply.Content[0].Text), nil
}

// The helper's native request proves its actual occurrence. The digest is an
// equality input, never an invocation token or authority supplied by its parent.
func (d *applicationDirectory) confirmMessageStart(ctx context.Context, caller applicationV3Caller, digest string) wire.ApplicationChange {
	if ctx.Err() != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "invocation")
	}
	if !messageStartDigest(digest) {
		return appChange(wire.ApplicationOutcomeInvalid, "recipe")
	}
	b := d.messageStartOwned
	if b == nil || b.ctx == nil || b.ctx.Err() != nil || !caller.valid(d.owner) {
		return appChange(wire.ApplicationOutcomeForbidden, "")
	}
	for {
		b.mu.Lock()
		rows := make(map[*ownedApplicationSession]*ownedMessageStart, len(b.rows))
		for session, row := range b.rows {
			rows[session] = row
		}
		pending, changed := b.pending, b.changed
		b.mu.Unlock()
		for session, row := range rows {
			if row.ctx == nil || row.ctx.Err() != nil {
				continue
			}
			select {
			case <-session.done:
				continue
			default:
			}
			r := row.request
			if digest != r.ProfileFingerprint || caller.subject.Account != r.Account || !samePrograms(caller.subject.Program, r.Program) {
				continue
			}
			peer, err := session.announcementPeer(ctx, caller.peer, caller.matcher)
			if err != nil || peer == nil {
				continue
			}
			process, err := peer.Process.AtLeast(identity.ProofBound)
			desktop, ok := applicationSession(peer)
			binding := session.bindingValue()
			if err != nil || !ok || desktop != r.Session || binding.account != r.Account || binding.application != r.Application || binding.runtimeEpoch != d.epoch || binding.manifestRevision != r.RegistrationBinding || binding.imageCode != r.ProgramKey || !session.matchesAnnouncementProcess(binding, process) {
				continue
			}
			if row.authority == nil || row.authority(ctx) != nil || session.guard(ctx, binding) != nil || caller.recheck() != nil || ctx.Err() != nil || row.ctx.Err() != nil || b.ctx.Err() != nil {
				return appChange(wire.ApplicationOutcomeForbidden, "")
			}
			b.mu.Lock()
			current := b.rows[session] == row
			if current {
				row.confirmed = true
			}
			b.mu.Unlock()
			if current {
				return appChange(wire.ApplicationOutcomeApplied, "")
			}
			return appChange(wire.ApplicationOutcomeUnavailable, "invocation")
		}
		if pending == 0 {
			return appChange(wire.ApplicationOutcomeForbidden, "")
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return appChange(wire.ApplicationOutcomeUnavailable, "invocation")
		case <-b.ctx.Done():
			return appChange(wire.ApplicationOutcomeUnavailable, "invocation")
		}
	}
}

// Bootstrap uses the existing owned MCP carrier without fabricating an
// AppModel instance. Only this private one-shot lifecycle verb is dispatched.
func (s *ownedApplicationSession) dispatchReceiverStart(ctx context.Context, authority func(context.Context) error, request applicationMessageStartRequest) error {
	if authority == nil {
		return errors.New("message start: authority unavailable")
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer func() { s.turn <- struct{}{} }()
	if s.protocol != "2025-11-25" || s.next != 1 {
		return errors.New("message start: one initialized dispatch required")
	}
	binding := startprofile.CustodyBinding{Account: request.Account, ProgramKey: request.ProgramKey, EnrollmentRevision: request.EnrollmentRevision, RecipeDigest: request.ProfileFingerprint}
	if !binding.Valid() {
		return errors.New("message start: custody binding unavailable")
	}
	meta := binding.Metadata()
	if request.Cold != nil {
		if !validMessageStartSelectedTarget(request) {
			return errors.New("message start: cold target invalid")
		}
		meta[startprofile.ColdResumeMetaKey] = request.Cold.Metadata()
		if request.Wake != "" {
			meta[startprofile.WakeMetaKey] = request.Wake
		}
	}
	if request.Target != "" {
		if !validMessageStartSelectedTarget(request) {
			return errors.New("message start: attach target invalid")
		}
		meta[startprofile.TargetMetaKey] = request.Target
	}
	parameters, _ := json.Marshal(map[string]any{"name": "receiver_start", "arguments": map[string]any{}, "_meta": meta})
	result, err := s.exchange(ctx, "tools/call", ownedApplicationParams(parameters), func() error { return authority(ctx) })
	if err != nil {
		return err
	}
	var reply struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool           `json:"isError"`
		Meta    map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(result, &reply); err != nil || reply.IsError || len(reply.Content) != 1 || reply.Content[0].Type != "text" {
		return errors.Join(errOwnedApplicationUncertain, errors.New("message start: malformed owned result"))
	}
	answer, err := client.ParseHostActivationResult([]byte(reply.Content[0].Text))
	if err != nil {
		return errors.Join(errOwnedApplicationUncertain, err)
	}
	// An addressed session's delivery is shown in Activity, whatever the
	// outcome: a turn queued into the running Codex app is the app's own.
	if mode, ok := reply.Meta[messageStartDeliveryKey].(string); ok && request.Target != "" && request.Delivered != nil && validMessageStartDelivery(mode) {
		request.Delivered(mode)
	}
	if answer.Outcome != applications.HostActivationOutcomeApplied {
		return errors.New("message start: program reported no completion")
	}
	return nil
}
