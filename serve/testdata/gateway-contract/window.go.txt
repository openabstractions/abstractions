// Package gateway is the loopback window of abstraction.inference/chat@1: an
// OpenAI chat completions and Anthropic messages surface for programs that only
// know a base URL and an API key. It binds every connection's peer through the
// socket-owner table before reading a byte, requires the local key minted for
// that program before reading a request body, and serves each request through
// the same provider, rights decisions and audit as the native route.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// PeerNeed is the least the window asks of a loopback peer: user, process and
// path bound to the process that opened the connection.
var PeerNeed = identity.Need{User: identity.ProofBound, Process: identity.ProofBound, Path: identity.ProofBound}

// MaxBodyBytes bounds one request body, the size of one chat@1 control frame.
const MaxBodyBytes = 1 << 20

// MaxTranscriptionBodyBytes bounds the multipart upload, including framing.
const MaxTranscriptionBodyBytes = (16 << 20) + (1 << 20)
const MaxImageEditBodyBytes = (40 << 20) + (1 << 20)

// Key outcome words a KeyCheck returns.
const (
	KeyVerified     = "verified"
	KeyUnknown      = "unknown"
	KeyRevoked      = "revoked"
	KeyWrongProgram = "wrong_program"
	KeyUnavailable  = "unavailable"
)

// Chat is the provider the window starts, observes and cancels operations on.
// *inference.Provider satisfies it.
type Chat interface {
	Start(ctx context.Context, subject inference.Subject, req wire.Request) wire.Admission
	Observe(ctx context.Context, subject inference.Subject, id string, cursor, maxDeltas, maxBytes, waitMS int64) wire.DeltaPage
	Cancel(subject inference.Subject, id string) wire.Cancellation
}

// Transcriber is the optional transcription profile on the same endpoint.
type Transcriber interface {
	StartTranscription(context.Context, inference.Subject, wire.TranscriptionRequest) wire.Admission
	ObserveTranscription(context.Context, inference.Subject, string, int64, int64, int64, int64) wire.DeltaPage
	CancelTranscription(inference.Subject, string) wire.Cancellation
}

type SpeechSynthesizer interface {
	StartSpeech(context.Context, inference.Subject, wire.SpeechRequest) wire.Admission
	ObserveSpeech(context.Context, inference.Subject, string, int64, int64, int64, int64) wire.DeltaPage
	CancelSpeech(inference.Subject, string) wire.Cancellation
}

type ImageGenerator interface {
	StartImage(context.Context, inference.Subject, wire.ImageRequest) wire.Admission
	ObserveImage(context.Context, inference.Subject, string, int64, int64, int64, int64) wire.DeltaPage
	CancelImage(inference.Subject, string) wire.Cancellation
}

// LiveProvider is the service-owned live voice profile exposed through the
// compatibility window. The browser-facing WebSocket remains a gateway wire;
// provider state and upstream transport stay behind these native calls.
type LiveProvider interface {
	// PreflightLive decides host routing, wire support and
	// abstraction.inference/complete for the request's model, admitting
	// nothing. The window calls it before it completes the upgrade.
	PreflightLive(context.Context, inference.Subject, wire.LiveRequest) wire.Admission
	StartLive(context.Context, inference.Subject, wire.LiveRequest) wire.Admission
	AppendLive(context.Context, inference.Subject, string, int64, []byte) wire.LiveInputResult
	ObserveLive(context.Context, inference.Subject, string, int64, int64, int64, int64) wire.DeltaPage
	CommitLive(context.Context, inference.Subject, string) wire.LiveInputResult
	CancelLive(inference.Subject, string) wire.Cancellation
}

// ContentWriter authorizes content.write for subject and stores verified bytes
// under digest. The provider separately authorizes content.read during Start.
type ContentWriter func(context.Context, inference.Subject, string, []byte) inference.ContentOutcome

// ContentReader authorizes content.read for subject and resolves complete bytes.
type ContentReader func(context.Context, inference.Subject, string, int64) ([]byte, inference.ContentOutcome)

// Grant is what a presented key lets the window ask for (INF-W5): the hosted
// credential name a minted key's requests may spend under, or none for
// local-only. Declared is a placeholder key's grant: chat and messages reach
// hosted model servers under the credential each server declares, and
// abstraction.credentials/apply for the bound program decides every use.
type Grant struct {
	Credential string
	Declared   bool
}

// KeyCheck verifies a presented key for the bound subject and returns a Key*
// outcome word. A key minted for another program is KeyWrongProgram.
type KeyCheck func(ctx context.Context, subject inference.Subject, key string) (Grant, string)

// Bound is a connection's bound peer.
type Bound interface {
	Subject() inference.Subject
	Rung() string
	// Peer is the identity evidence Subject was bound from, code included,
	// or nil when the binder kept none.
	Peer() *identity.Peer
	// Recheck returns an error once the connection no longer answers for the
	// peer it was bound to.
	Recheck() error
	Close() error
}

// Binder binds the peer of an accepted connection. It runs before any byte is
// read.
type Binder func(c net.Conn, acceptedAt time.Time) (Bound, error)

// Config composes a window. Chat, Keys and Account are required.
type Config struct {
	Chat Chat
	Keys KeyCheck
	// Account is the runtime's account; a peer of another account is refused.
	Account string
	// Models lists the model names the window reports for subject.
	Models       func(ctx context.Context, subject inference.Subject) []string
	StoreContent ContentWriter
	ReadContent  ContentReader
	// Bind replaces the socket-owner binding, for tests.
	Bind Binder
	// Record receives one record per window refusal before admission. Admitted
	// and refused requests are recorded by the provider.
	Record  func(inference.Record)
	OnError func(error)
	// Await holds a call whose refusal admitted first-use questions
	// (INF-W10): chat, messages, images, speech, transcription and
	// embeddings alike. It returns true once a rights change permits what one of
	// the questions asked, and false when every question was answered
	// otherwise or ctx ended. Nil answers every refusal at once.
	Await func(ctx context.Context, subject inference.Subject, questions []string) bool
	// QuestionWait bounds how long one call is held for its questions;
	// zero is DefaultQuestionWait.
	QuestionWait time.Duration
	// Pending returns which of a refusal's questions are still pending about
	// subject when the hold ends; the refusal names each as
	// rights:question_pending:<id> after its grant words (INF-W11). Nil names
	// none.
	Pending func(ctx context.Context, subject inference.Subject, questions []string) []string
}

// DefaultQuestionWait is how long the window holds a call open for the
// first-use questions its refusal admitted (INF-W10): the one 40 s wait every
// OA call that waits on the person uses (backlog/RIGHTS-FLOW.md, Wait policy).
// The person's asks.answer_wait setting, carried as
// PendingQuestion.answer_wait_ms, replaces it once the window reads it.
const DefaultQuestionWait = 40 * time.Second

// maxQuestionRounds bounds the admissions one held call makes: one question
// per right a call needs (complete on the server, then the credential's use)
// and a retry after each answer.
const maxQuestionRounds = 4

// Window is a listening gateway window.
type Window struct {
	cfg      Config
	listener net.Listener
	server   *http.Server
	once     sync.Once
	liveMu   sync.Mutex
	live     map[*liveWindowSession]struct{}
	closed   bool
}

// Listen opens the window on address, which must name 127.0.0.1 and a port.
func Listen(address string, cfg Config) (*Window, error) {
	if cfg.Chat == nil || cfg.Keys == nil || cfg.Account == "" {
		return nil, errors.New("gateway: chat provider, key check and account required")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" {
		return nil, fmt.Errorf("gateway: %q is not 127.0.0.1:<port>; the window listens on IPv4 loopback only", address)
	}
	if cfg.Bind == nil {
		account := cfg.Account
		cfg.Bind = func(c net.Conn, at time.Time) (Bound, error) { return BindLoopback(c, at, account) }
	}
	l, err := net.Listen("tcp4", address)
	if err != nil {
		return nil, err
	}
	w := &Window{cfg: cfg}
	w.listener = &boundListener{Listener: l, window: w}
	w.server = &http.Server{Handler: http.HandlerFunc(w.serveHTTP), ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if b, ok := c.(*boundConn); ok {
				return context.WithValue(ctx, boundKey{}, b)
			}
			return ctx
		}}
	return w, nil
}

// Addr is the listening address.
func (w *Window) Addr() net.Addr { return w.listener.Addr() }

// Serve accepts connections until Close or ctx ends.
func (w *Window) Serve(ctx context.Context) error {
	//unchecked: best-effort shutdown triggered asynchronously by context cancellation; Serve already returns its own error and has no way to receive a second one from this later callback
	stop := context.AfterFunc(ctx, func() { w.Close() })
	defer stop()
	err := w.server.Serve(w.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops accepting and closes every connection; admitted operations with
// no observer idle out.
func (w *Window) Close() error {
	var err error
	w.once.Do(func() {
		w.liveMu.Lock()
		w.closed = true
		sessions := make([]*liveWindowSession, 0, len(w.live))
		for session := range w.live {
			sessions = append(sessions, session)
		}
		w.liveMu.Unlock()
		for _, session := range sessions {
			session.stop()
		}
		err = w.server.Close()
	})
	return err
}

func (w *Window) record(r inference.Record) {
	r.Route = inference.RouteWindow
	if w.cfg.Record != nil {
		w.cfg.Record(r)
	}
}

func (w *Window) report(err error) {
	if err != nil && w.cfg.OnError != nil {
		w.cfg.OnError(err)
	}
}

type boundKey struct{}

// boundConn is an accepted connection whose peer is bound once, on first use:
// its first Read, or the handler's first call to bound. Both run in the
// connection's own goroutine, so one program's code verification never holds
// the accept loop, and no byte is read before the peer is bound.
type boundConn struct {
	net.Conn
	window     *Window
	acceptedAt time.Time
	bindOnce   sync.Once

	mu       sync.Mutex
	b        Bound
	bindErr  error
	closed   bool
	closeErr error
}

// bound binds the connection's peer on first use and returns the binding or
// the reason it failed. A failed binding has been recorded and answered 403.
func (c *boundConn) bound() (Bound, error) {
	c.bindOnce.Do(c.bind)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b, c.bindErr
}

func (c *boundConn) bind() {
	b, err := c.window.cfg.Bind(c.Conn, c.acceptedAt)
	c.mu.Lock()
	closed := c.closed
	switch {
	case closed && err == nil:
		c.bindErr = net.ErrClosed
	case closed:
		c.bindErr = err
	default:
		c.b, c.bindErr = b, err
	}
	c.mu.Unlock()
	switch {
	case closed && b != nil:
		//unchecked: release of a binding that finished after the connection was closed; nothing is left to report it to
		b.Close()
	case closed:
	case err != nil:
		word := BindingWord(err)
		c.window.record(inference.Record{Rung: identity.TransportLoopback + "/" + runtime.GOOS + " unbound", Outcome: wire.ReplyOutcomeForbidden.String(), Reason: "binding:" + word})
		refuseConnection(c.Conn, word, err)
	}
}

// Read binds the peer before the first byte is read. A failed binding has
// already been answered; it reads as a failed read, which net/http closes
// without writing a reply of its own.
func (c *boundConn) Read(p []byte) (int, error) {
	if _, err := c.bound(); err != nil {
		return 0, &net.OpError{Op: "read", Net: "tcp", Source: c.LocalAddr(), Addr: c.RemoteAddr(), Err: err}
	}
	return c.Conn.Read(p)
}

func (c *boundConn) Close() error {
	c.mu.Lock()
	if c.closed {
		defer c.mu.Unlock()
		return c.closeErr
	}
	c.closed = true
	b := c.b
	c.mu.Unlock()
	var err error
	if b != nil {
		err = b.Close()
	}
	err = errors.Join(err, c.Conn.Close())
	c.mu.Lock()
	c.closeErr = err
	c.mu.Unlock()
	return err
}

// boundListener hands each accepted connection to net/http at once, wrapped so
// that its peer is bound before net/http reads from it. A connection whose peer
// cannot be bound is answered 403 and closed without reading its request.
type boundListener struct {
	net.Listener
	window *Window
}

func (l *boundListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &boundConn{Conn: c, window: l.window, acceptedAt: time.Now()}, nil
}

// refuseConnection answers before reading: the peer's request stays unread in
// the socket, and closing discards it.
func refuseConnection(c net.Conn, word string, cause error) {
	//unchecked: errorBody returns a map of plain strings; json.Marshal on plain data types cannot fail
	body, _ := json.Marshal(errorBody(http.StatusForbidden, "forbidden", "the window could not bind the program on this connection ("+word+"): "+cause.Error()))
	//unchecked: best-effort raw refusal write before closing; this helper returns nothing and there is no caller left to report a failed deadline to
	c.SetWriteDeadline(time.Now().Add(time.Second))
	//unchecked: best-effort raw refusal write before closing; this helper returns nothing and there is no caller left to report a failed write to
	fmt.Fprintf(c, "HTTP/1.1 403 Forbidden\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	//unchecked: best-effort close after the refusal write; this helper returns nothing and there is no caller left to report the failure to
	c.Close()
}

// BindingWord names a binding failure in an audit reason.
func BindingWord(err error) string {
	switch {
	case errors.Is(err, identity.ErrPeerMoved):
		return "peer_moved"
	case errors.Is(err, identity.ErrPeerGone):
		return "peer_gone"
	case errors.Is(err, identity.ErrRemotePeer):
		return "remote_peer"
	case errors.Is(err, identity.ErrNoBinding):
		return "no_binding"
	case errors.Is(err, identity.ErrNotProven):
		return "not_proven"
	case errors.Is(err, errOtherAccount):
		return "other_account"
	}
	return "unbound"
}

var errOtherAccount = errors.New("gateway: the peer runs as another account")

// clock is the time a binding's evidence lifetime is measured against; tests
// replace it.
var clock = time.Now

type loopbackBound struct {
	conn       net.Conn
	acceptedAt time.Time

	mu      sync.Mutex
	binding *identity.Binding
	peer    *identity.Peer
	subject inference.Subject
	rung    string
}

// Subject is fixed at bind time; a re-verification that names another subject
// is refused.
func (b *loopbackBound) Subject() inference.Subject { return b.subject }

func (b *loopbackBound) Rung() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rung
}

func (b *loopbackBound) Peer() *identity.Peer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peer
}

// Close releases the binding; the lock keeps it from racing a Recheck that
// reads or replaces it.
func (b *loopbackBound) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.binding.Close()
}

// Recheck confirms the connection still answers for its peer. Once the
// binding's evidence has outlived its lifetime (identity ID-W2: a code verdict
// is reused for five minutes), the peer is bound again through the identity
// layer before the next decision, and the new binding must name the same
// subject.
func (b *loopbackBound) Recheck() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.binding.Peer(); err != nil {
		return err
	}
	if until := b.binding.EvidenceValidUntil(); until.IsZero() || clock().Before(until) {
		return nil
	}
	fresh, err := identity.BindLoopback(b.conn, &identity.Options{ConnectedAt: b.acceptedAt})
	if err != nil {
		return err
	}
	peer, subject, rung, err := subjectOf(fresh)
	if err == nil && subject != b.subject {
		err = fmt.Errorf("%w: the connection now answers for %s", identity.ErrPeerMoved, subject.Program)
	}
	if err != nil {
		//unchecked: cleanup of a binding this recheck is abandoning before returning the more specific error
		fresh.Close()
		return err
	}
	//unchecked: release of the binding this recheck replaced; the connection answers through the fresh one from here on
	b.binding.Close()
	b.binding, b.peer, b.rung = fresh, peer, rung
	return nil
}

// BindLoopback binds c's peer through the socket-owner table and requires
// PeerNeed and account. It verifies the peer's code signature as the native
// pipe does: a first-use question the window's refusal admits names the
// publisher from it (ASK-A6). A process's verdict is reused for five minutes
// by its later connections (identity ID-W2), and Recheck verifies a
// connection again once its verdict is older than that.
func BindLoopback(c net.Conn, at time.Time, account string) (Bound, error) {
	b, err := identity.BindLoopback(c, &identity.Options{ConnectedAt: at})
	if err != nil {
		return nil, err
	}
	peer, subject, rung, err := subjectOf(b)
	if err != nil {
		//unchecked: cleanup of a binding this function is abandoning before returning subjectOf's more specific error
		b.Close()
		return nil, err
	}
	if subject.Account != account {
		//unchecked: cleanup of a binding this function is abandoning before returning the more specific errOtherAccount
		b.Close()
		return nil, errOtherAccount
	}
	return &loopbackBound{conn: c, acceptedAt: at, binding: b, peer: peer, subject: subject, rung: rung}, nil
}

func subjectOf(b *identity.Binding) (*identity.Peer, inference.Subject, string, error) {
	peer, err := b.Peer()
	if err != nil {
		return nil, inference.Subject{}, "", err
	}
	subject, err := LoopbackSubject(peer)
	if err != nil {
		return nil, inference.Subject{}, peer.Rung(), err
	}
	return peer, subject, peer.Rung(), nil
}

// LoopbackSubject is the subject the window binds from a loopback peer: its
// account and program, which PeerNeed requires at bound (identity.SubjectOf).
func LoopbackSubject(peer *identity.Peer) (inference.Subject, error) {
	s, err := identity.SubjectOf(peer, PeerNeed)
	if err != nil {
		return inference.Subject{}, err
	}
	return inference.Subject{Account: s.Account, Program: s.Program}, nil
}

// routed is ctx for one of b's provider calls: the window route and rung for
// the audit, and the peer b was bound from for the rights decision
// (inference.WithPeer).
func routed(ctx context.Context, b Bound) context.Context {
	return inference.WithPeer(inference.WithRoute(ctx, inference.RouteWindow, b.Rung()), b.Peer())
}

// wireKind is the vendor wire a request path speaks.
type wireKind int

const (
	wireOpenAI wireKind = iota
	wireAnthropic
)

// presentedKey reads the key an OpenAI client sends as a bearer token or an
// Anthropic client sends as x-api-key.
func presentedKey(r *http.Request) string {
	if key := r.Header.Get("X-Api-Key"); key != "" {
		return key
	}
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// embeds reports whether the provider serves embeddings.
func (w *Window) embeds() bool {
	_, ok := w.cfg.Chat.(Embedder)
	return ok
}

func (w *Window) transcribes() bool {
	_, ok := w.cfg.Chat.(Transcriber)
	return ok && w.cfg.StoreContent != nil
}

func (w *Window) speaks() bool { _, ok := w.cfg.Chat.(SpeechSynthesizer); return ok }
func (w *Window) images() bool {
	_, ok := w.cfg.Chat.(ImageGenerator)
	return ok && w.cfg.ReadContent != nil
}
func (w *Window) lives() bool { _, ok := w.cfg.Chat.(LiveProvider); return ok }

type verifiedKeyContext struct{}

// serveHTTP checks the binding, the route and the key before the body is read.
func (w *Window) serveHTTP(rw http.ResponseWriter, r *http.Request) {
	conn, _ := r.Context().Value(boundKey{}).(*boundConn)
	if conn == nil {
		writeError(rw, http.StatusForbidden, "forbidden", "unbound connection")
		return
	}
	b, err := conn.bound()
	if err != nil {
		word := BindingWord(err)
		rw.Header().Set("Connection", "close")
		writeError(rw, http.StatusForbidden, "forbidden", "the window could not bind the program on this connection ("+word+"): "+err.Error())
		return
	}
	subject := b.Subject()
	refuse := func(status int, word, reason, message string) {
		w.record(inference.Record{Rung: b.Rung(), Account: subject.Account, Program: subject.Program, Outcome: word, Reason: reason})
		rw.Header().Set("Connection", "close")
		writeError(rw, status, word, message)
	}
	if err := b.Recheck(); err != nil {
		refuse(http.StatusForbidden, "forbidden", "binding:"+BindingWord(err), "the connection no longer answers for the program that opened it: "+err.Error())
		return
	}
	var route func(http.ResponseWriter, *http.Request, Bound, Grant)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		route = w.openAIChat
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
		route = w.anthropicMessages
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		route = w.models
	case r.Method == http.MethodPost && r.URL.Path == "/v1/embeddings" && w.embeds():
		route = w.openAIEmbeddings
	case r.Method == http.MethodPost && r.URL.Path == "/v1/audio/transcriptions" && w.transcribes():
		route = w.openAITranscription
	case r.Method == http.MethodPost && r.URL.Path == "/v1/audio/speech" && w.speaks():
		route = w.openAISpeech
	case r.Method == http.MethodPost && r.URL.Path == "/v1/images/generations" && w.images():
		route = w.openAIImageGeneration
	case r.Method == http.MethodPost && r.URL.Path == "/v1/images/edits" && w.images() && w.cfg.StoreContent != nil:
		route = w.openAIImageEdit
	case r.Method == http.MethodGet && r.URL.Path == "/v1/realtime" && w.lives():
		route = w.openAIRealtime
	default:
		rw.Header().Set("Connection", "close")
		writeError(rw, http.StatusNotFound, "not_found", "the window serves POST /v1/chat/completions, POST /v1/messages, POST /v1/embeddings, POST /v1/audio/transcriptions, POST /v1/audio/speech, POST /v1/images/generations, POST /v1/images/edits, GET /v1/realtime and GET /v1/models")
		return
	}
	key := presentedKey(r)
	if key == "" {
		refuse(http.StatusUnauthorized, "forbidden", "key:missing", "a key is required: any non-empty text, or a gateway key from openabstractions inference key issue --for <program>")
		return
	}
	grant, word := w.cfg.Keys(r.Context(), subject, key)
	switch word {
	case KeyVerified:
	case KeyWrongProgram:
		refuse(http.StatusForbidden, "forbidden", "key:"+word, "this key was issued to another program")
		return
	case KeyUnavailable:
		refuse(http.StatusServiceUnavailable, "unavailable", "key:"+word, "the key could not be verified")
		return
	default:
		refuse(http.StatusUnauthorized, "forbidden", "key:"+word, "the key is not a local key issued to this program")
		return
	}
	route(rw, r.WithContext(context.WithValue(r.Context(), verifiedKeyContext{}, key)), b, grant)
}

func (w *Window) models(rw http.ResponseWriter, r *http.Request, b Bound, _ Grant) {
	var names []string
	if w.cfg.Models != nil {
		names = w.cfg.Models(r.Context(), b.Subject())
	}
	rw.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Anthropic-Version") != "" {
		data := make([]map[string]any, 0, len(names))
		for _, name := range names {
			data = append(data, map[string]any{"type": "model", "id": name, "display_name": name, "created_at": "1970-01-01T00:00:00Z"})
		}
		out := map[string]any{"data": data, "has_more": false, "first_id": nil, "last_id": nil}
		if len(names) > 0 {
			out["first_id"], out["last_id"] = names[0], names[len(names)-1]
		}
		//unchecked: terminal write of the response; headers and status are already committed and this package has no logger to report a write failure to (matches writeError and the other Encode calls in this package)
		json.NewEncoder(rw).Encode(out)
		return
	}
	data := make([]map[string]any, 0, len(names))
	for _, name := range names {
		data = append(data, map[string]any{"id": name, "object": "model", "created": 0, "owned_by": "openabstractions"})
	}
	//unchecked: terminal write of the response; headers and status are already committed and this package has no logger to report a write failure to (matches writeError and the other Encode calls in this package)
	json.NewEncoder(rw).Encode(map[string]any{"object": "list", "data": data})
}

// readBody reads a bounded request body after the key was verified, and
// rechecks the binding before anything is admitted.
func (w *Window) readBody(rw http.ResponseWriter, r *http.Request, b Bound, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, MaxBodyBytes))
	if err := dec.Decode(into); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid", "the request body is not a JSON object within 1 MiB: "+err.Error())
		return false
	}
	if err := b.Recheck(); err != nil {
		subject := b.Subject()
		w.record(inference.Record{Rung: b.Rung(), Account: subject.Account, Program: subject.Program, Outcome: "forbidden", Reason: "binding:" + BindingWord(err)})
		rw.Header().Set("Connection", "close")
		writeError(rw, http.StatusForbidden, "forbidden", "the connection no longer answers for the program that opened it: "+err.Error())
		return false
	}
	return true
}

// start admits a chat request for the bound subject under the key's grant,
// held for its first-use questions (holdForQuestions).
func (w *Window) start(r *http.Request, b Bound, grant Grant, req wire.Request) wire.Admission {
	ctx := routed(r.Context(), b)
	switch {
	case grant.Credential != "":
		req.Guarantees, req.Credential = []wire.RequestGuarantee{wire.RequestGuaranteeHostedAllowed}, grant.Credential
	case grant.Declared:
		req.Guarantees = []wire.RequestGuarantee{wire.RequestGuaranteeHostedAllowed}
		ctx = inference.WithDeclaredCredential(ctx)
	default:
		req.Guarantees = []wire.RequestGuarantee{wire.RequestGuaranteeLocalOnly}
	}
	return w.startHeld(r, ctx, b, req.Model, "", func(ctx context.Context) wire.Admission {
		return w.cfg.Chat.Start(ctx, b.Subject(), req)
	})
}

// startHeld admits one request of any profile through holdForQuestions. A
// refused admission's reason names each question still pending; a connection
// that stopped answering for its program is refused forbidden.
func (w *Window) startHeld(r *http.Request, ctx context.Context, b Bound, model, profile string, start func(context.Context) wire.Admission) wire.Admission {
	admission, _ := w.startStored(r, ctx, b, model, profile, nil, start)
	return admission
}

// storeRefusedReason is the reason of an upload the content store refused for
// want of a grant (INF-V1).
const storeRefusedReason = "content:write:forbidden"

// startStored is startHeld for a request whose input the window stores
// first. store and start run as one attempt inside one hold, so an upload
// refused for want of its input profile holds for the input question the
// store admitted, as a refused start holds for its own (INF-W10). When the
// last attempt stopped at the store, stored is the store's outcome and, for
// ContentForbidden, the admission's reason is storeRefusedReason followed by
// one rights:question_pending:<id> word for each question still pending
// (INF-W11). Otherwise stored is ContentResolved. A nil store stores nothing.
func (w *Window) startStored(r *http.Request, ctx context.Context, b Bound, model, profile string, store func(context.Context) inference.ContentOutcome, start func(context.Context) wire.Admission) (admission wire.Admission, stored inference.ContentOutcome) {
	reason, bound := w.holdForQuestions(r, ctx, b, model, profile, func(ctx context.Context) (bool, string) {
		stored = inference.ContentResolved
		if store != nil {
			if stored = store(ctx); stored != inference.ContentResolved {
				return stored == inference.ContentForbidden, storeRefusedReason
			}
		}
		admission = start(ctx)
		return admission.Outcome == wire.StartOutcomeNotPermitted || admission.Outcome == wire.StartOutcomeForbidden, admission.Reason
	})
	if !bound {
		return wire.Admission{Outcome: wire.StartOutcomeForbidden, Reason: reason}, inference.ContentResolved
	}
	if stored != inference.ContentResolved {
		return wire.Admission{Outcome: wire.StartOutcomeForbidden, Reason: reason}, stored
	}
	admission.Reason = reason
	return admission, stored
}

// holdForQuestions runs attempt with a fresh first-use collector on ctx. An
// attempt refused for want of a grant that admitted or found first-use
// questions holds the call open until they are answered or QuestionWait
// ends, and a rights change that answered one runs attempt again (INF-W10).
// Every route that admits a request holds through it. It returns the last
// attempt's reason followed by one rights:question_pending:<id> word for each
// of its questions still pending about the caller (INF-W11). bound is false
// when the connection stopped answering for its program before a retry;
// reason is then that refusal's binding word. Nothing has been written to the
// client before it returns.
func (w *Window) holdForQuestions(r *http.Request, ctx context.Context, b Bound, model, profile string, attempt func(context.Context) (grantRefused bool, reason string)) (reason string, bound bool) {
	wait := w.cfg.QuestionWait
	if wait <= 0 {
		wait = DefaultQuestionWait
	}
	held, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	for round := 1; ; round++ {
		asked := &inference.Asked{}
		refused, reason := attempt(inference.WithAsked(ctx, asked))
		questions := asked.IDs()
		if !refused || len(questions) == 0 {
			return reason, true
		}
		if w.cfg.Await == nil || round == maxQuestionRounds {
			return w.namePending(ctx, b.Subject(), questions, reason), true
		}
		allowed := w.cfg.Await(held, b.Subject(), questions)
		// The connection must still answer for the program it was bound to
		// after the hold, before the call is admitted again or its refusal
		// names the pending questions (INF-W3).
		if err := b.Recheck(); err != nil {
			subject := b.Subject()
			reason := "binding:" + BindingWord(err)
			w.record(inference.Record{Profile: profile, Rung: b.Rung(), Account: subject.Account, Program: subject.Program, Model: model, Outcome: "forbidden", Reason: reason})
			return reason, false
		}
		if !allowed {
			return w.namePending(ctx, b.Subject(), questions, reason), true
		}
	}
}

// namePending appends one rights:question_pending:<id> word to a refusal
// reason for each of its questions still pending about subject (INF-W11).
func (w *Window) namePending(ctx context.Context, subject inference.Subject, questions []string, reason string) string {
	if w.cfg.Pending == nil || len(questions) == 0 {
		return reason
	}
	for _, id := range w.cfg.Pending(ctx, subject, questions) {
		word := "rights:question_pending:" + id
		if id != "" && !strings.ContainsAny(id, " \t\r\n") && !slices.Contains(strings.Fields(reason), word) {
			reason += " " + word
		}
	}
	return reason
}

// observe yields each page of an admitted operation until its end. It cancels
// the operation when the client leaves or emit fails, and returns the reply.
func (w *Window) observe(r *http.Request, b Bound, operation string, emit func(wire.Delta) bool) (*wire.Reply, bool) {
	subject := b.Subject()
	cursor := int64(0)
	for {
		page := w.cfg.Chat.Observe(r.Context(), subject, operation, cursor, 256, 65536, 25000)
		if r.Context().Err() != nil {
			w.cfg.Chat.Cancel(subject, operation)
			return nil, false
		}
		if page.Outcome != wire.PageOutcomePage {
			w.report(fmt.Errorf("gateway: observe %s: %s", operation, page.Outcome))
			w.cfg.Chat.Cancel(subject, operation)
			return &wire.Reply{Outcome: wire.ReplyOutcomeUnavailable, Reason: "observe:" + page.Outcome.String()}, true
		}
		for _, d := range page.Deltas {
			if d.Kind == wire.DeltaKindEnd && d.End != nil {
				if !emit(d) {
					return nil, false
				}
				return d.End, true
			}
			if !emit(d) {
				w.cfg.Chat.Cancel(subject, operation)
				return nil, false
			}
		}
		cursor = page.Next
	}
}

// refusal is the HTTP answer to a refused admission or a reply that ended
// other than completed (INF-W7). Every route answers through refusalOf.
type refusal struct {
	status        int
	code, message string
}

// overflowReason starts the reason of a reply the model server refused as
// larger than its context window (INF-Q7).
const overflowReason = "upstream:context_length_exceeded:"

// refusalOf answers outcome and its reason. A context overflow is the
// client's own invalid request: 400 with the code context_length_exceeded
// that OpenAI-compatible and Anthropic clients read, and a message naming the
// window and the input when they are known.
func refusalOf(outcome, reason string) refusal {
	if outcome == "refused" {
		if window, input, ok := contextOverflow(reason); ok {
			return refusal{status: http.StatusBadRequest, code: "context_length_exceeded", message: overflowMessage(window, input)}
		}
	}
	message := outcome
	if reason != "" {
		message += ": " + reason
	}
	status := http.StatusServiceUnavailable
	switch outcome {
	case "invalid", "unsupported_feature":
		status = http.StatusBadRequest
	case "not_permitted", "forbidden":
		status = http.StatusForbidden
	case "no_host":
		status = http.StatusNotFound
	case "budget_exceeded", "exhausted":
		status = http.StatusTooManyRequests
	case "refused":
		status = http.StatusBadGateway
	}
	return refusal{status: status, code: outcome, message: message}
}

// contextOverflow reads the window and the counted input from an INF-Q7
// reason, each 0 when unknown.
func contextOverflow(reason string) (window, input int64, ok bool) {
	rest, found := strings.CutPrefix(reason, overflowReason)
	if !found {
		return 0, 0, false
	}
	w, i, found := strings.Cut(rest, ":")
	if !found {
		return 0, 0, false
	}
	wn, werr := strconv.ParseUint(w, 10, 63)
	in, ierr := strconv.ParseUint(i, 10, 63)
	if werr != nil || ierr != nil {
		return 0, 0, false
	}
	return int64(wn), int64(in), true
}

// overflowMessage names what is known of a context overflow in words that
// OpenCode's overflow classifier also recognises.
func overflowMessage(window, input int64) string {
	const exceeds = "the request exceeds the context window"
	switch {
	case window > 0 && input > 0:
		return fmt.Sprintf("%s: the input is %d tokens and the context window is %d tokens", exceeds, input, window)
	case window > 0:
		return fmt.Sprintf("%s of %d tokens", exceeds, window)
	case input > 0:
		return fmt.Sprintf("%s: the input is %d tokens", exceeds, input)
	}
	return exceeds
}

// body is the error body of the refusal, readable by both wires.
func (f refusal) body() map[string]any { return errorBody(f.status, f.code, f.message) }

// write answers the request with the refusal.
func (f refusal) write(rw http.ResponseWriter) { writeError(rw, f.status, f.code, f.message) }

func errorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	}
	return "api_error"
}

// errorBody is readable by both wires: Anthropic reads type and error.type,
// OpenAI reads error.message and error.code.
func errorBody(status int, code, message string) map[string]any {
	return map[string]any{"type": "error", "error": map[string]any{"type": errorType(status), "message": message, "code": code}}
}

func writeError(rw http.ResponseWriter, status int, code, message string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	//unchecked: terminal write of the response; headers and status are already committed and this package has no logger to report a write failure to
	json.NewEncoder(rw).Encode(errorBody(status, code, message))
}
