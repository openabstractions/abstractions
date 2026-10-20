package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The @3 owned host uses OA length framing as a private custom MCP transport.
// External standard newline MCP is a separate binding; there is no fallback.
const ownedApplicationFrameLimit = 1 << 20

type ownedApplicationParams []byte

var errOwnedApplicationUncertain = errors.New("application host: delivery uncertain; session retired")

// errOwnedApplicationCatalogue is a call that crossed the application's
// notifications/tools/list_changed: the description it was checked against
// is no longer the served one. The session stays; the next call reads the
// description again (FAC-M17).
var errOwnedApplicationCatalogue = errors.New("application host: the application changed its commands")

// errOwnedApplicationVirtualized is a runtime whose profile view is
// virtualized: it starts no application host (FAC-M18).
var errOwnedApplicationVirtualized = errors.New("application host: virtualized runtime profile")

// errOwnedApplicationAbandoned is a command whose caller stopped waiting
// before the application answered. The session stays: its answer is read and
// dropped before the next call is sent.
var errOwnedApplicationAbandoned = errors.New("application host: the caller stopped waiting; the application still works on the command")

// ownedApplicationDrainLimit bounds the wait for an abandoned command's
// answer. An application that answers no later than this keeps its session.
var ownedApplicationDrainLimit = 10 * time.Minute

// A correlated error reply follows a sent request. The gateway maps this code
// to FAC-M32 command unknown/read unavailable; it never redispatches the call.
type ownedApplicationRPCError struct {
	code    int64
	message string
}

type ownedApplicationInvalidAnswer struct{ cause error }

func (e *ownedApplicationInvalidAnswer) Error() string { return "application host: invalid answer" }
func (e *ownedApplicationInvalidAnswer) Unwrap() error { return e.cause }

func (e *ownedApplicationRPCError) Error() string {
	return fmt.Sprintf("application host: answer:%d: %s", e.code, e.message)
}

// This value is supplied by the native launch owner, never by MCP metadata.
// The live guard must recheck its retained process, image and pipe association.
type ownedApplicationBinding struct {
	application, instance, account, desktop, runtimeEpoch, manifestRevision, imageCode string
	pid                                                                                int
	started                                                                            time.Time
	generation                                                                         uint32
}

type ownedApplicationRequestMatcher func(*identity.Binding, *identity.SealedResourceEvidence) (identity.Code, identity.Proof, error)
type ownedApplicationAnnounceAuthenticator func(context.Context, *identity.Peer, ownedApplicationRequestMatcher) (*identity.Peer, error)

type ownedApplicationMessage struct {
	frame []byte
	err   error
}

// A session owns only these pipe ends. It has no process termination operation.
// A single request is admitted at a time, bounding correlation state to one ID.
type ownedApplicationSession struct {
	// Set before the gateway publishes an agent's authenticated start.
	starter       string
	announceMu    sync.Mutex
	initialized   chan struct{}
	descriptionMu sync.Mutex
	description   *appmodel.Description
	bindMu        sync.RWMutex
	binding       ownedApplicationBinding
	in            io.WriteCloser
	out           io.ReadCloser
	guard         func(context.Context, ownedApplicationBinding) error
	turn          chan struct{}
	messages      chan ownedApplicationMessage
	done          chan struct{}
	once          sync.Once
	release       func() error
	next          uint64
	protocol      string
	// catalogue counts the application's notifications/tools/list_changed.
	// A kept description is current while the count it was read at holds.
	// Context list changes need no count: contexts are read on every call,
	// and an Invocation names its context's epoch.
	catalogue atomic.Uint64
	// descriptionAt is the catalogue count the kept description was read at;
	// descriptionMu guards it with description.
	descriptionAt            uint64
	failure                  atomic.Pointer[ownedApplicationMessage]
	authenticateAnnouncement ownedApplicationAnnounceAuthenticator
	// exited is set by the launch owner once it has waited for the owned
	// process's exit. A closed session whose process exited leaves nothing
	// running, and the next open may start the application again.
	exited atomic.Bool
	// draining closes once the answer to a command its caller stopped
	// waiting for has arrived. It is read and written only by the turn holder.
	draining chan struct{}
}

// processExited marks the owned process as waited for, then closes the
// session. Only the launch owner that started the process calls it.
func (s *ownedApplicationSession) processExited() error {
	s.exited.Store(true)
	return s.Close()
}

func newOwnedApplicationSession(binding ownedApplicationBinding, in io.WriteCloser, out io.ReadCloser, guard func(context.Context, ownedApplicationBinding) error, release ...func() error) (*ownedApplicationSession, error) {
	if binding.started.IsZero() || binding.generation != 0 {
		return nil, errors.New("application host: incomplete owned process occurrence")
	}
	return newOwnedApplicationSessionState(binding, in, out, guard, nil, release...)
}

// Platform owners must establish the actual process occurrence before calling
// this shared state initializer. Authentication is installed before read starts.
func newOwnedApplicationSessionState(binding ownedApplicationBinding, in io.WriteCloser, out io.ReadCloser, guard func(context.Context, ownedApplicationBinding) error, authenticate ownedApplicationAnnounceAuthenticator, release ...func() error) (*ownedApplicationSession, error) {
	if in == nil || out == nil || guard == nil || binding.pid <= 0 || binding.application == "" || binding.account == "" || binding.desktop == "" || binding.runtimeEpoch == "" || binding.manifestRevision == "" || binding.imageCode == "" {
		return nil, errors.New("application host: incomplete owned binding")
	}
	s := &ownedApplicationSession{binding: binding, in: in, out: out, guard: guard, turn: make(chan struct{}, 1), messages: make(chan ownedApplicationMessage), done: make(chan struct{}), initialized: make(chan struct{}), authenticateAnnouncement: authenticate}
	if len(release) > 0 {
		s.release = release[0]
	}
	s.turn <- struct{}{}
	go s.read()
	return s, nil
}

func (s *ownedApplicationSession) bindingValue() ownedApplicationBinding {
	s.bindMu.RLock()
	defer s.bindMu.RUnlock()
	return s.binding
}

func (s *ownedApplicationSession) startedFor(c applicationV3Caller) bool {
	if s == nil || s.starter == "" || s.starter != c.subject.Program || c.peer == nil {
		return false
	}
	b := s.bindingValue()
	desktop, ok := applicationSession(c.peer)
	return ok && b.account == c.subject.Account && b.desktop == desktop
}

// The first authenticated empty-instance Announce returns the directory's handle.
// Binding freezes that returned handle before the application's Core is created.
// The process is already held by the launch owner. Program spelling alone is
// insufficient, and a pending channel cannot send application tools/call.
func (s *ownedApplicationSession) bindAnnouncedInstance(ctx context.Context, peer *identity.Peer, application, instance string, recheck func() error, matchers ...ownedApplicationRequestMatcher) error {
	if peer == nil || recheck == nil || instance == "" || len(instance) > 128 || !utf8.ValidString(instance) {
		return errors.New("application host: unbound announcement")
	}
	if err := recheck(); err != nil {
		return err
	}
	var matcher ownedApplicationRequestMatcher
	if len(matchers) > 0 {
		matcher = matchers[0]
	}
	var err error
	peer, err = s.announcementPeer(ctx, peer, matcher)
	if err != nil {
		return err
	}
	if peer == nil {
		return errors.New("application host: announcement identity unavailable")
	}
	process, err := peer.Process.AtLeast(identity.ProofBound)
	if err != nil {
		return err
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return err
	}
	desktop, ok := applicationSession(peer)
	if !ok {
		return errors.New("application host: no bound desktop session")
	}
	b := s.bindingValue()
	if subject.Account != b.account || desktop != b.desktop || application != b.application || !s.matchesAnnouncementProcess(b, process) {
		return errors.New("application host: announcement from another process")
	}
	if err := s.guard(ctx, b); err != nil {
		return err
	}
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	if !sameOwnedApplicationProcessBinding(b, s.binding) {
		return errors.New("application host: owned binding changed")
	}
	b = s.binding
	if b.instance != "" && b.instance != instance {
		return errors.New("application host: instance already bound")
	}
	select {
	case <-s.done:
		return s.closedError()
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := recheck(); err != nil {
		return err
	}
	s.binding.instance = instance
	return nil
}

func (s *ownedApplicationSession) announcementPeer(ctx context.Context, peer *identity.Peer, matcher ownedApplicationRequestMatcher) (*identity.Peer, error) {
	if s.authenticateAnnouncement != nil {
		if matcher == nil {
			return nil, errors.New("application host: request matcher unavailable")
		}
		return s.authenticateAnnouncement(ctx, peer, matcher)
	}
	return peer, nil
}

func (s *ownedApplicationSession) matchesAnnouncementProcess(b ownedApplicationBinding, p identity.Process) bool {
	if p.Recycled || p.PID != b.pid {
		return false
	}
	if s.authenticateAnnouncement != nil {
		return b.generation != 0 && b.started.IsZero() && p.Generation == b.generation && p.StartTime.IsZero()
	}
	return b.generation == 0 && !p.StartTime.IsZero() && p.StartTime.Equal(b.started)
}

func sameOwnedApplicationProcessBinding(a, b ownedApplicationBinding) bool {
	// Instance is frozen by the session's one-time Announce transition. It is
	// not a kernel process attribute; all actual process/authority facts remain
	// exactly equal to the launch owner's immutable held binding.
	a.instance = ""
	b.instance = ""
	return a == b
}

func (s *ownedApplicationSession) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = errors.Join(s.in.Close(), s.out.Close())
		if s.release != nil {
			err = errors.Join(err, s.release())
		}
	})
	return err
}

func (s *ownedApplicationSession) read() {
	notifications := 0
	for {
		line, err := listen.ReadFrameFrom(s.out, ownedApplicationFrameLimit)
		if err == nil {
			err = ownedApplicationJSON(line)
			if err == nil {
				var notification struct {
					Version string          `json:"jsonrpc"`
					ID      json.RawMessage `json:"id"`
					Method  string          `json:"method"`
					Result  json.RawMessage `json:"result"`
					Error   json.RawMessage `json:"error"`
				}
				err = json.Unmarshal(line, &notification)
				if err == nil && notification.Version == "2.0" && len(notification.ID) == 0 && notification.Method != "" && len(notification.Result) == 0 && len(notification.Error) == 0 {
					notifications++
					if notification.Method == "notifications/tools/list_changed" {
						s.catalogue.Add(1)
					}
					if notifications <= 32 && notification.Method != "notifications/subscriptions/acknowledged" {
						continue
					}
					if notifications > 32 {
						err = errors.New("application host: notification budget exceeded")
					}
				}
			}
		}
		notifications = 0
		message := ownedApplicationMessage{frame: bytes.Clone(line), err: err}
		if err != nil {
			s.failure.Store(&message)
			s.Close()
			return
		}
		select {
		case s.messages <- message:
		case <-s.done:
			return
		}
	}
}

func (s *ownedApplicationSession) closedError() error {
	if terminal := s.failure.Load(); terminal != nil {
		return errors.Join(errOwnedApplicationUncertain, terminal.err)
	}
	return io.ErrClosedPipe
}

func (s *ownedApplicationSession) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.closedError()
	case <-s.turn:
	}
	if d := s.draining; d != nil {
		select {
		case <-d:
			s.draining = nil
		case <-ctx.Done():
			s.turn <- struct{}{}
			return ctx.Err()
		case <-s.done:
			s.turn <- struct{}{}
			return s.closedError()
		}
	}
	return nil
}

// abandon hands the outstanding request id to a reader that drops its
// answer. The caller holding the turn returns at once; the next turn holder
// waits for the answer. A missing or uncorrelated answer retires the session.
func (s *ownedApplicationSession) abandon(id string, cause error) error {
	drained := make(chan struct{})
	s.draining = drained
	go func() {
		defer close(drained)
		limit := time.NewTimer(ownedApplicationDrainLimit)
		defer limit.Stop()
		retire := func(why string) {
			failure := ownedApplicationMessage{err: errors.New("application host: " + why)}
			s.failure.CompareAndSwap(nil, &failure)
			s.Close()
		}
		for {
			select {
			case <-s.done:
				return
			case <-limit.C:
				retire("an abandoned command never answered")
				return
			case message := <-s.messages:
				if message.err != nil || ownedApplicationJSON(message.frame) != nil {
					retire("invalid MCP response")
					return
				}
				var reply struct {
					Version string          `json:"jsonrpc"`
					ID      json.RawMessage `json:"id"`
					Method  string          `json:"method"`
					Result  json.RawMessage `json:"result"`
					Error   json.RawMessage `json:"error"`
				}
				if json.Unmarshal(message.frame, &reply) != nil || reply.Version != "2.0" {
					retire("invalid MCP response")
					return
				}
				if len(reply.ID) == 0 && reply.Method != "" && len(reply.Result) == 0 && len(reply.Error) == 0 {
					continue
				}
				var gotID string
				if json.Unmarshal(reply.ID, &gotID) != nil || gotID != id || reply.Method != "" {
					retire("uncorrelated MCP response")
				}
				return
			}
		}
	}()
	return errors.Join(errOwnedApplicationAbandoned, cause)
}

// Both dialect generations are already served by the canonical JS/Python host.
// A session pins one exact revision, without negotiation retries.
func (s *ownedApplicationSession) initialize(ctx context.Context, revision string) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer func() { s.turn <- struct{}{} }()
	if s.protocol != "" {
		return errors.New("application host: already initialized")
	}
	params := map[string]any{}
	method := "initialize"
	switch revision {
	case "2026-07-28":
		method = "server/discover"
		params["_meta"] = ownedApplicationMeta(revision)
	case "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05":
		params["protocolVersion"] = revision
		params["capabilities"] = map[string]any{}
		params["clientInfo"] = map[string]string{"name": "OpenAbstractions", "version": "0.8"}
	default:
		return errors.New("application host: unsupported MCP revision")
	}
	result, err := s.exchange(ctx, method, params)
	if err != nil {
		return err
	}
	var reply struct {
		Protocol  string   `json:"protocolVersion"`
		Supported []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		s.Close()
		return err
	}
	valid := reply.Protocol == revision
	if method == "server/discover" {
		valid = false
		for _, v := range reply.Supported {
			if v == revision {
				valid = true
			}
		}
	}
	if !valid {
		s.Close()
		return errors.New("application host: MCP revision mismatch")
	}
	if method == "server/discover" {
		_, err := s.exchange(ctx, "subscriptions/listen", map[string]any{"_meta": ownedApplicationMeta(revision), "notifications": map[string]bool{"toolsListChanged": true, "resourcesListChanged": true}})
		if err != nil {
			s.Close()
			return err
		}
	}
	if method == "initialize" {
		finished := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				select {
				case <-finished:
					return
				default:
					s.Close()
				}
			case <-finished:
			case <-s.done:
			}
		}()
		line := "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n"
		err := listen.WriteFrameTo(s.in, []byte(line), ownedApplicationFrameLimit)
		close(finished)
		if err != nil {
			s.Close()
			return errors.Join(errOwnedApplicationUncertain, err, io.ErrShortWrite)
		}
	}
	s.protocol = revision
	close(s.initialized)
	return nil
}

func ownedApplicationMeta(revision string) map[string]any {
	return map[string]any{"io.modelcontextprotocol/protocolVersion": revision, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
}

// call uses the canonical GatewayCall codec for the original argument bytes.
// validate is the gateway's generated decoder for the declared reply record.
// No error after attempting a write permits automatic redispatch. The call is
// fenced against the catalogue count current when it takes its turn.
func (s *ownedApplicationSession) call(ctx context.Context, call *appmodel.GatewayCall, validate func([]byte) error, authorize func(context.Context, ownedApplicationBinding, *appmodel.GatewayCall) error) ([]byte, error) {
	return s.callAt(ctx, nil, call, validate, authorize)
}

// callAt is call fenced against the catalogue count at, the count the
// caller's kept description was read at. A tools/list_changed before the
// write refuses the call unsent with errOwnedApplicationCatalogue; one during
// the dispatch makes the answer uncertain. Neither retires the session.
func (s *ownedApplicationSession) callAt(ctx context.Context, at *uint64, call *appmodel.GatewayCall, validate func([]byte) error, authorize func(context.Context, ownedApplicationBinding, *appmodel.GatewayCall) error) ([]byte, error) {
	if call == nil || validate == nil || authorize == nil {
		return nil, errors.New("application host: missing canonical call or reply decoder")
	}
	raw, err := appmodel.ToJSON(call)
	if err != nil {
		return nil, err
	}
	checked, err := appmodel.DecodeGatewayCall(raw)
	if err != nil {
		return nil, err
	}
	binding := s.bindingValue()
	if binding.instance == "" || checked.Application != binding.application || checked.Instance != binding.instance {
		return nil, errors.New("application host: call names another owned instance")
	}
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { s.turn <- struct{}{} }()
	if s.protocol == "" {
		return nil, errors.New("application host: initialize first")
	}
	epoch := s.catalogue.Load()
	if at != nil {
		epoch = *at
	}
	fence := func() error {
		if s.catalogue.Load() != epoch {
			return errOwnedApplicationCatalogue
		}
		return nil
	}
	if err := fence(); err != nil {
		return nil, err
	}
	name, _ := json.Marshal(checked.Tool)
	params := append([]byte(`{"name":`), name...)
	params = append(params, `,"arguments":`...)
	params = append(params, checked.Arguments...)
	if s.protocol == "2026-07-28" {
		meta, _ := json.Marshal(ownedApplicationMeta(s.protocol))
		params = append(params, `,"_meta":`...)
		params = append(params, meta...)
	}
	params = append(params, '}')
	result, err := s.exchangeWith(ctx, true, fence, "tools/call", ownedApplicationParams(params), func() error { return authorize(ctx, binding, checked) })
	if err != nil {
		return nil, err
	}
	var reply struct {
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &reply); err != nil || len(reply.Structured) == 0 || len(reply.Content) != 1 || reply.Content[0].Type != "text" {
		s.Close()
		return nil, errors.Join(errOwnedApplicationUncertain, &ownedApplicationInvalidAnswer{cause: errors.New("malformed")})
	}
	canonical := []byte(reply.Content[0].Text)
	if err := ownedApplicationJSON(canonical); err != nil {
		s.Close()
		return nil, errors.Join(errOwnedApplicationUncertain, &ownedApplicationInvalidAnswer{cause: err})
	}
	if err := validate(canonical); err != nil {
		s.Close()
		return nil, errors.Join(errOwnedApplicationUncertain, &ownedApplicationInvalidAnswer{cause: err})
	}
	if fence() != nil {
		// The answer was read whole; the stream stays usable.
		return nil, errors.Join(errOwnedApplicationUncertain, errOwnedApplicationCatalogue)
	}
	return bytes.Clone(canonical), nil
}

// Caller holds turn through the live authority check and exact dispatch.
func (s *ownedApplicationSession) exchange(ctx context.Context, method string, params any, checks ...func() error) ([]byte, error) {
	return s.exchangeWith(ctx, false, nil, method, params, checks...)
}

// exchangeWith sends one request. With abandonable, a caller that stops
// waiting after the request was written leaves the session open: the answer
// is drained before the next request (abandon). Without it, the session
// retires, as an initialization or one-shot dispatch requires. fence, when
// set, is checked again at the dispatch fence, after checks and the guard.
func (s *ownedApplicationSession) exchangeWith(ctx context.Context, abandonable bool, fence func() error, method string, params any, checks ...func() error) ([]byte, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, errors.New("application host: dispatch requires a caller deadline")
	}
	select {
	case <-s.done:
		return nil, s.closedError()
	default:
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.next++
	id := fmt.Sprintf("oa-%d", s.next)
	var encoded []byte
	var err error
	if raw, ok := params.(ownedApplicationParams); ok {
		encoded = raw
	} else {
		encoded, err = json.Marshal(params)
	}
	idJSON, _ := json.Marshal(id)
	methodJSON, _ := json.Marshal(method)
	frame := append([]byte(`{"jsonrpc":"2.0","id":`), idJSON...)
	frame = append(frame, `,"method":`...)
	frame = append(frame, methodJSON...)
	frame = append(frame, `,"params":`...)
	frame = append(frame, encoded...)
	frame = append(frame, '}')
	if err != nil || len(frame) > ownedApplicationFrameLimit {
		s.Close()
		return nil, errors.New("application host: invalid or oversized MCP call")
	}
	// Authorization may block while the reader observes a catalogue change.
	// Recheck the live binding and observed invalidation at the dispatch fence.
	if err := s.guard(ctx, s.bindingValue()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fence != nil {
		if err := fence(); err != nil {
			return nil, err
		}
	}
	select {
	case <-s.done:
		return nil, s.closedError()
	default:
	}
	finished := make(chan struct{})
	defer close(finished)
	written := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if abandonable {
				select {
				case <-written:
					// The read loop below abandons the answer.
					return
				default:
				}
			}
			select {
			case <-finished:
				return
			default:
				// A partly written frame leaves the stream unusable.
				s.Close()
			}
		case <-finished:
		case <-s.done:
		}
	}()
	uncertain := func(cause error) ([]byte, error) {
		s.Close()
		return nil, errors.Join(errOwnedApplicationUncertain, cause)
	}
	if err := listen.WriteFrameTo(s.in, frame, ownedApplicationFrameLimit); err != nil {
		return uncertain(err)
	}
	close(written)
	for notifications := 0; notifications <= 32; notifications++ {
		var message ownedApplicationMessage
		select {
		case <-ctx.Done():
			if abandonable {
				return nil, s.abandon(id, ctx.Err())
			}
			return uncertain(ctx.Err())
		case <-s.done:
			return uncertain(s.closedError())
		case message = <-s.messages:
		}
		if message.err != nil {
			return uncertain(message.err)
		}
		if err := ownedApplicationJSON(message.frame); err != nil {
			return uncertain(err)
		}
		var reply struct {
			Version string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(message.frame, &reply); err != nil || reply.Version != "2.0" {
			return uncertain(errors.New("application host: invalid MCP response"))
		}
		if method == "subscriptions/listen" && reply.Method == "notifications/subscriptions/acknowledged" && len(reply.ID) == 0 {
			var ack struct {
				Notifications struct {
					Tools     bool `json:"toolsListChanged"`
					Resources bool `json:"resourcesListChanged"`
				} `json:"notifications"`
				Meta map[string]string `json:"_meta"`
			}
			if json.Unmarshal(reply.Params, &ack) != nil || ack.Meta["io.modelcontextprotocol/subscriptionId"] != id || !ack.Notifications.Tools || !ack.Notifications.Resources {
				return uncertain(errors.New("application host: invalid subscription acknowledgement"))
			}
			return bytes.Clone(reply.Params), nil
		}
		if len(reply.ID) == 0 && reply.Method != "" && len(reply.Result) == 0 && len(reply.Error) == 0 {
			continue
		}
		var gotID string
		if json.Unmarshal(reply.ID, &gotID) != nil || gotID != id || reply.Method != "" || (len(reply.Result) == 0) == (len(reply.Error) == 0) {
			return uncertain(errors.New("application host: uncorrelated MCP response"))
		}
		if len(reply.Error) != 0 {
			var failure struct {
				Code    *int64  `json:"code"`
				Message *string `json:"message"`
			}
			if json.Unmarshal(reply.Error, &failure) != nil || failure.Code == nil || failure.Message == nil {
				return uncertain(errors.New("application host: malformed MCP error"))
			}
			return nil, &ownedApplicationRPCError{code: *failure.Code, message: *failure.Message}
		}
		if method == "subscriptions/listen" {
			return uncertain(errors.New("application host: missing subscription acknowledgement"))
		}
		if method == "server/discover" || s.protocol == "2026-07-28" {
			var complete struct {
				Type string `json:"resultType"`
			}
			if json.Unmarshal(reply.Result, &complete) != nil || complete.Type != "complete" {
				return uncertain(errors.New("application host: incomplete MCP result"))
			}
		}
		return bytes.Clone(reply.Result), nil
	}
	return uncertain(errors.New("application host: notification budget exceeded"))
}

// The MCP envelope has no generated OA record. Bound and reject duplicate
// members before decoding it; payload semantics remain with generated codecs.
func ownedApplicationJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("application host: invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 1024 {
			return errors.New("application host: JSON nesting limit")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return errors.New("application host: invalid JSON delimiter")
			}
			seen := map[string]bool{}
			for d.More() {
				if delim == '{' {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return errors.New("application host: duplicate JSON member")
					}
					seen[name] = true
				}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
		}
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("application host: trailing JSON")
	}
	return nil
}
