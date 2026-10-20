package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-identity/listen"
)

type ownedCountingWriter struct{ calls atomic.Int32 }

func (w *ownedCountingWriter) Write(p []byte) (int, error) { w.calls.Add(1); return len(p), nil }
func (w *ownedCountingWriter) Close() error                { return nil }

func TestOwnedApplicationSessionIdleReaderFailureRetiresBeforeDispatch(t *testing.T) {
	for _, kind := range []string{"eof", "malformed", "notification-budget"} {
		t.Run(kind, func(t *testing.T) {
			var input bytes.Buffer
			switch kind {
			case "malformed":
				_ = listen.WriteFrameTo(&input, []byte(`{"jsonrpc":`), ownedApplicationFrameLimit)
			case "notification-budget":
				for i := 0; i < 33; i++ {
					_ = listen.WriteFrameTo(&input, []byte(`{"jsonrpc":"2.0","method":"notifications/progress"}`), ownedApplicationFrameLimit)
				}
			}
			binding := ownedApplicationBinding{application: "fixture", instance: "i1", account: "account", desktop: "windows:1", runtimeEpoch: "runtime1", manifestRevision: "manifest1", imageCode: "held-image", pid: 1, started: time.Unix(1, 0)}
			writer := &ownedCountingWriter{}
			var released atomic.Int32
			s, err := newOwnedApplicationSession(binding, writer, io.NopCloser(&input), func(context.Context, ownedApplicationBinding) error { return nil }, func() error { released.Add(1); return nil })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			select {
			case <-s.done:
			case <-time.After(time.Second):
				t.Fatal("idle reader failure not retired")
			}
			s.protocol = "2025-11-25"
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); err == nil {
				t.Fatal("terminal channel dispatched")
			}
			s.Close()
			if writer.calls.Load() != 0 || released.Load() != 1 {
				t.Fatalf("writes %d releases %d", writer.calls.Load(), released.Load())
			}
		})
	}
}

func ownedSessionFixture(t *testing.T, guard func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	binding := ownedApplicationBinding{application: "fixture", instance: "i1", account: "account", desktop: "windows:1", runtimeEpoch: "runtime1", manifestRevision: "manifest1", imageCode: "held-image", pid: 1, started: time.Unix(1, 0)}
	if guard == nil {
		guard = func(context.Context, ownedApplicationBinding) error { return nil }
	}
	session, err := newOwnedApplicationSession(binding, client, client, guard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close(); server.Close() })
	return session, server
}

func ownedFixtureRequest(t *testing.T, r *bufio.Reader) map[string]json.RawMessage {
	t.Helper()
	line, err := listen.ReadFrameFrom(r, ownedApplicationFrameLimit)
	if err != nil {
		return nil
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(line, &request); err != nil {
		return nil
	}
	return request
}

func ownedFixtureReply(w io.Writer, id json.RawMessage, result any) {
	if object, ok := result.(map[string]any); ok {
		object["resultType"] = "complete"
		if raw, ok := object["structuredContent"].(json.RawMessage); ok && object["content"] == nil {
			object["content"] = []map[string]string{{"type": "text", "text": string(raw)}}
		}
	}
	frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	_ = listen.WriteFrameTo(w, frame, ownedApplicationFrameLimit)
}

func TestOwnedApplicationSessionCanonicalTextOwnsReply(t *testing.T) {
	for _, kind := range []string{"authoritative", "missing", "ambiguous", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			s, server := ownedSessionFixture(t, nil)
			r := ownedFixtureInitialize(t, s, server, "2025-11-25")
			go func() {
				request := ownedFixtureRequest(t, r)
				canonical, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeRefused, Reason: "canonical refusal", Effects: `{"n": 9007199254740993}`})
				content := []map[string]string{{"type": "text", "text": string(canonical)}}
				switch kind {
				case "missing":
					content = nil
				case "ambiguous":
					content = append(content, content[0])
				case "malformed":
					content[0]["text"] = `{"outcome":"invented"}`
				}
				ownedFixtureReply(server, request["id"], map[string]any{"structuredContent": map[string]any{"outcome": "accepted"}, "content": content, "isError": true})
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			raw, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize)
			if kind != "authoritative" {
				if !errors.Is(err, errOwnedApplicationUncertain) {
					t.Fatalf("invalid canonical text %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := appmodel.DecodeCommandResult(raw)
			if err != nil || result.Outcome != appmodel.CommandOutcomeRefused || result.Reason != "canonical refusal" || result.Effects != `{"n": 9007199254740993}` {
				t.Fatalf("canonical text lost: %s %v", raw, err)
			}
		})
	}
}

// A tools/list_changed during authorization refuses that call unsent; the
// session stays and the next call is sent (FAC-M17).
func TestOwnedApplicationSessionCatalogueChangeDuringAuthorizationWritesNothing(t *testing.T) {
	s, server := ownedSessionFixture(t, nil)
	r := ownedFixtureInitialize(t, s, server, "2025-11-25")
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		_, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, func(context.Context, ownedApplicationBinding, *appmodel.GatewayCall) error {
			close(entered)
			<-release
			return nil
		})
		result <- err
	}()
	<-entered
	if err := listen.WriteFrameTo(server, []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`), ownedApplicationFrameLimit); err != nil {
		t.Fatal(err)
	}
	for s.catalogue.Load() == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("notification not observed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)
	if err := <-result; !errors.Is(err, errOwnedApplicationCatalogue) || errors.Is(err, errOwnedApplicationUncertain) {
		t.Fatalf("prewrite fence %v", err)
	}
	if err := server.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if n, _ := r.Read(b[:]); n != 0 {
		t.Fatal("fenced call wrote bytes")
	}
	if err := server.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	go func() {
		request := ownedFixtureRequest(t, r)
		raw, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeAccepted, Job: "accepted-work", Effects: "{}"})
		ownedFixtureReply(server, request["id"], map[string]any{"structuredContent": json.RawMessage(raw)})
	}()
	if raw, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); err != nil || !strings.Contains(string(raw), "accepted-work") {
		t.Fatalf("the session ended at a catalogue change: %s %v", raw, err)
	}
}

func TestOwnedApplicationSessionRetainsRPCErrorCodeWithoutResend(t *testing.T) {
	for _, body := range []string{`{"code":-32601,"message":"unavailable"}`, `{"code":9007199254740993,"message":"large exact code"}`, `{"code":1.5,"message":"invalid"}`, `{"message":"missing code"}`} {
		t.Run(body, func(t *testing.T) {
			s, server := ownedSessionFixture(t, nil)
			r := ownedFixtureInitialize(t, s, server, "2025-11-25")
			go func() {
				request := ownedFixtureRequest(t, r)
				frame := []byte(`{"jsonrpc":"2.0","id":` + string(request["id"]) + `,"error":` + body + `}`)
				_ = listen.WriteFrameTo(server, frame, ownedApplicationFrameLimit)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize)
			var answer *ownedApplicationRPCError
			if strings.Contains(body, `"invalid"`) || strings.Contains(body, `"missing code"`) {
				if !errors.Is(err, errOwnedApplicationUncertain) {
					t.Fatalf("malformed error %v", err)
				}
			} else {
				if !errors.As(err, &answer) || !strings.Contains(err.Error(), "answer:") {
					t.Fatalf("lost RPC code %v", err)
				}
				want := int64(-32601)
				if strings.Contains(body, "9007199254740993") {
					want = 9007199254740993
				}
				if answer.code != want {
					t.Fatalf("code %d want %d", answer.code, want)
				}
			}
			_ = server.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			var b [1]byte
			if n, _ := server.Read(b[:]); n != 0 {
				t.Fatal("error caused resend")
			}
		})
	}
}

func ownedFixtureInitialize(t *testing.T, s *ownedApplicationSession, server net.Conn, revision string) *bufio.Reader {
	t.Helper()
	r := bufio.NewReader(server)
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := ownedFixtureRequest(t, r)
		var method string
		_ = json.Unmarshal(request["method"], &method)
		if revision == "2026-07-28" {
			if method != "server/discover" || !strings.Contains(string(request["params"]), revision) {
				t.Errorf("discover request %s", request)
			}
			ownedFixtureReply(server, request["id"], map[string]any{"supportedVersions": []string{revision}})
			request = ownedFixtureRequest(t, r)
			if string(request["method"]) != `"subscriptions/listen"` {
				t.Errorf("listen request %s", request)
			}
			var id string
			_ = json.Unmarshal(request["id"], &id)
			ack, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/subscriptions/acknowledged", "params": map[string]any{"notifications": map[string]bool{"toolsListChanged": true, "resourcesListChanged": true}, "_meta": map[string]string{"io.modelcontextprotocol/subscriptionId": id}}})
			_ = listen.WriteFrameTo(server, ack, ownedApplicationFrameLimit)
		} else {
			if method != "initialize" {
				t.Errorf("method %s", method)
			}
			ownedFixtureReply(server, request["id"], map[string]any{"protocolVersion": revision})
			notification := ownedFixtureRequest(t, r)
			if string(notification["method"]) != `"notifications/initialized"` {
				t.Errorf("initialize notification %s", notification)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.initialize(ctx, revision); err != nil {
		t.Fatal(err)
	}
	<-done
	return r
}

func ownedFixtureCall() *appmodel.GatewayCall {
	return &appmodel.GatewayCall{Application: "fixture", Instance: "i1", Tool: "app_job", Arguments: `{"instance":"i1","job":"accepted-work"}`}
}

func ownedFixtureResult(raw []byte) error { _, err := appmodel.DecodeCommandResult(raw); return err }

func TestOwnedApplicationSessionCanonicalReplyAndRevisions(t *testing.T) {
	for _, revision := range []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28"} {
		t.Run(revision, func(t *testing.T) {
			var checked atomic.Int32
			s, server := ownedSessionFixture(t, func(_ context.Context, b ownedApplicationBinding) error {
				if b.runtimeEpoch != "runtime1" || b.manifestRevision != "manifest1" || b.pid != 1 {
					return errors.New("moved binding")
				}
				checked.Add(1)
				return nil
			})
			r := ownedFixtureInitialize(t, s, server, revision)
			go func() {
				request := ownedFixtureRequest(t, r)
				result, err := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeAccepted, Job: "accepted-work", Effects: "{}"})
				if err != nil {
					t.Error(err)
					return
				}
				ownedFixtureReply(server, request["id"], map[string]any{"structuredContent": json.RawMessage(result)})
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize)
			wantChecks := int32(2)
			if revision == "2026-07-28" {
				wantChecks = 3
			}
			if err != nil || !strings.Contains(string(result), "accepted-work") || checked.Load() != wantChecks {
				t.Fatalf("result %s, error %v, checks %d", result, err, checked.Load())
			}
		})
	}
}

func TestOwnedApplicationSessionRechecksBeforeEveryDispatch(t *testing.T) {
	var revoked atomic.Bool
	s, server := ownedSessionFixture(t, func(context.Context, ownedApplicationBinding) error {
		if revoked.Load() {
			return errors.New("authority revoked")
		}
		return nil
	})
	ownedFixtureInitialize(t, s, server, "2025-11-25")
	revoked.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); err == nil || errors.Is(err, errOwnedApplicationUncertain) {
		t.Fatalf("pre-dispatch refusal %v", err)
	}
	if err := server.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := server.Read(b[:]); err == nil {
		t.Fatal("revoked call wrote a byte")
	}
}

func TestOwnedApplicationSessionUncertaintyNeverResends(t *testing.T) {
	// A caller that stops waiting leaves the session open
	// (TestApplicationV3CommandAnswersWithinTheCallersWait).
	for _, response := range []string{"eof", "wrong-id", "duplicate", "bad-payload", "oversized"} {
		t.Run(response, func(t *testing.T) {
			s, server := ownedSessionFixture(t, nil)
			r := ownedFixtureInitialize(t, s, server, "2025-11-25")
			var requests atomic.Int32
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			go func() {
				request := ownedFixtureRequest(t, r)
				requests.Add(1)
				switch response {
				case "eof":
					server.Close()
				case "wrong-id":
					ownedFixtureReply(server, json.RawMessage(`"wrong"`), map[string]any{})
				case "duplicate":
					_ = listen.WriteFrameTo(server, []byte(`{"jsonrpc":"2.0","id":`+string(request["id"])+`,"id":`+string(request["id"])+`,"result":{}}`), ownedApplicationFrameLimit)
				case "bad-payload":
					ownedFixtureReply(server, request["id"], map[string]any{"structuredContent": map[string]any{"outcome": "invented"}})
				case "oversized":
					_ = listen.WriteFrameTo(server, []byte(strings.Repeat("x", ownedApplicationFrameLimit+1)), ownedApplicationFrameLimit+1)
				case "cancel":
					cancel()
				}
			}()
			if _, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); !errors.Is(err, errOwnedApplicationUncertain) {
				t.Fatalf("uncertainty %v", err)
			}
			if _, err := s.call(context.Background(), ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); err == nil {
				t.Fatal("retired session dispatched")
			}
			if requests.Load() != 1 {
				t.Fatalf("requests=%d", requests.Load())
			}
		})
	}
}

// An idle tools/list_changed moves the catalogue count: a call fenced at the
// earlier count is refused unsent. Context list changes move nothing.
func TestOwnedApplicationSessionNotificationMovesCatalogueAndCloseOnlyEndsPipes(t *testing.T) {
	s, server := ownedSessionFixture(t, nil)
	ownedFixtureInitialize(t, s, server, "2025-11-25")
	for _, method := range []string{"notifications/resources/list_changed", "notifications/resources/updated", "notifications/tools/list_changed"} {
		if err := listen.WriteFrameTo(server, []byte(`{"jsonrpc":"2.0","method":"`+method+`"}`), ownedApplicationFrameLimit); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for s.catalogue.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.catalogue.Load() != 1 {
		t.Fatalf("catalogue count %d after one tools/list_changed", s.catalogue.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var before uint64
	if _, err := s.callAt(ctx, &before, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); !errors.Is(err, errOwnedApplicationCatalogue) {
		t.Fatalf("a call checked against the earlier catalogue: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write([]byte("x")); err == nil {
		t.Fatal("pipe still open")
	}
	// The session has no Stop/Kill/process owner: accepted work is not cancelled.
}

func TestOwnedApplicationSessionRefusesMissingBindingsAndForeignCall(t *testing.T) {
	if _, err := newOwnedApplicationSession(ownedApplicationBinding{}, nil, nil, nil); err == nil {
		t.Fatal("missing binding accepted")
	}
	s, _ := ownedSessionFixture(t, nil)
	call := ownedFixtureCall()
	call.Instance = "other"
	if _, err := s.call(context.Background(), call, ownedFixtureResult, ownedFixtureAuthorize); err == nil {
		t.Fatal("foreign instance accepted")
	}
	if err := s.initialize(context.Background(), "future"); err == nil {
		t.Fatal("unknown revision accepted")
	}
}

func ownedFixtureAuthorize(context.Context, ownedApplicationBinding, *appmodel.GatewayCall) error {
	return nil
}

func TestOwnedApplicationSessionPreservesCanonicalRefusal(t *testing.T) {
	s, server := ownedSessionFixture(t, nil)
	r := ownedFixtureInitialize(t, s, server, "2025-11-25")
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		request := ownedFixtureRequest(t, r)
		if request == nil {
			return
		}
		result, err := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeRefused, Reason: "fixture refusal", Effects: "{}"})
		if err != nil {
			return
		}
		ownedFixtureReply(server, request["id"], map[string]any{"isError": true, "structuredContent": json.RawMessage(result)})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize)
	if err != nil {
		t.Fatal(err)
	}
	result, err := appmodel.DecodeCommandResult(raw)
	if err != nil || result.Outcome != appmodel.CommandOutcomeRefused || result.Reason != "fixture refusal" {
		t.Fatalf("refusal %s %v", raw, err)
	}
	<-finished
}

func TestOwnedApplicationSessionPerCallAuthoritySeesOriginalContext(t *testing.T) {
	s, server := ownedSessionFixture(t, nil)
	ownedFixtureInitialize(t, s, server, "2025-11-25")
	call := ownedFixtureCall()
	seen := false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := s.call(ctx, call, ownedFixtureResult, func(_ context.Context, b ownedApplicationBinding, c *appmodel.GatewayCall) error {
		seen = true
		if b.manifestRevision != "manifest1" || c.Arguments != call.Arguments || c.Tool != call.Tool {
			t.Errorf("changed authority input %+v %+v", b, c)
		}
		return errors.New("context revision or permission revoked")
	})
	if !seen || err == nil || errors.Is(err, errOwnedApplicationUncertain) {
		t.Fatalf("per-call gate %v seen%v", err, seen)
	}
	if err := server.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := server.Read(b[:]); err == nil {
		t.Fatal("per-call refusal dispatched")
	}
}

func TestOwnedApplicationSessionPreservesOpaqueArgumentBytes(t *testing.T) {
	for _, opaque := range []string{`{"n":  1,"text":"<>&"}`, "{\"n\":\n\r\t 1}", `{"text":"backslash\\ and quote\""}`, "{\"text\":\"雪🙂\u2028\"}"} {
		t.Run(opaque, func(t *testing.T) {
			s, server := ownedSessionFixture(t, nil)
			r := ownedFixtureInitialize(t, s, server, "2025-11-25")
			original, err := appmodel.ToJSON(&appmodel.Invocation{Context: appmodel.ContextRef{Instance: "i1", Context: "document", Epoch: "epoch1"}, Arguments: opaque, Mode: appmodel.InvokeModeApply, IdempotencyKey: "original-inner-key"})
			if err != nil {
				t.Fatal(err)
			}
			call := &appmodel.GatewayCall{Application: "fixture", Instance: "i1", Tool: "fixture.command", Arguments: string(original)}
			received := make(chan string, 1)
			go func() {
				request := ownedFixtureRequest(t, r)
				if request == nil {
					return
				}
				var params struct {
					Arguments json.RawMessage `json:"arguments"`
				}
				_ = json.Unmarshal(request["params"], &params)
				invocation, err := appmodel.DecodeInvocation(params.Arguments)
				if err != nil {
					received <- err.Error()
				} else {
					received <- invocation.Arguments
				}
				result, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeAccepted, Job: "accepted-work", Effects: opaque})
				frame := append([]byte(`{"jsonrpc":"2.0","id":`), request["id"]...)
				frame = append(frame, `,"result":{"structuredContent":`...)
				frame = append(frame, result...)
				content, _ := json.Marshal([]map[string]string{{"type": "text", "text": string(result)}})
				frame = append(frame, `,"content":`...)
				frame = append(frame, content...)
				frame = append(frame, '}', '}')
				_ = listen.WriteFrameTo(server, frame, ownedApplicationFrameLimit)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			raw, err := s.call(ctx, call, ownedFixtureResult, ownedFixtureAuthorize)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := appmodel.DecodeCommandResult(raw)
			if err != nil || reply.Effects != opaque {
				t.Fatalf("opaque reply changed: %s, %v", raw, err)
			}
			if actual := <-received; actual != opaque {
				t.Errorf("FAC-M29 opaque bytes changed: got %q, want %q", actual, opaque)
			}
		})
	}
}

type ownedPartialWriter struct{ calls int }

func (w *ownedPartialWriter) Write(p []byte) (int, error) { w.calls++; return 1, io.ErrUnexpectedEOF }
func (w *ownedPartialWriter) Close() error                { return nil }

func TestOwnedApplicationSessionCompleteFrameBoundAndPartialWrite(t *testing.T) {
	t.Run("wrapper bound before first byte", func(t *testing.T) {
		s, server := ownedSessionFixture(t, nil)
		ownedFixtureInitialize(t, s, server, "2025-11-25")
		call := ownedFixtureCall()
		call.Arguments = `{"padding":"` + strings.Repeat("x", ownedApplicationFrameLimit-32) + `"}`
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := s.call(ctx, call, ownedFixtureResult, ownedFixtureAuthorize); err == nil {
			t.Fatal("oversized wrapper accepted")
		}
		var b [1]byte
		if n, _ := server.Read(b[:]); n != 0 {
			t.Fatal("oversized wrapper wrote bytes")
		}
		select {
		case <-s.done:
		default:
			t.Fatal("oversized session retained")
		}
	})
	t.Run("partial frame retires once", func(t *testing.T) {
		s, server := ownedSessionFixture(t, nil)
		ownedFixtureInitialize(t, s, server, "2025-11-25")
		w := &ownedPartialWriter{}
		s.in = w
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); !errors.Is(err, errOwnedApplicationUncertain) {
			t.Fatalf("partial delivery %v", err)
		}
		if _, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); err == nil {
			t.Fatal("retired session reused")
		}
		if w.calls != 1 {
			t.Fatalf("writes %d", w.calls)
		}
	})
}
