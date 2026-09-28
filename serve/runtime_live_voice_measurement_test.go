package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	websocket "github.com/openabstractions/abstraction-inference/adapters/go/testsocket"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
	router "github.com/openabstractions/abstraction-router/go"
)

// The measurement this file runs answers DECISION.md section 4's question for
// abstraction.inference/live@1: the added latency of the OA path (isolated
// runtime, native framed IPC, the real Live provider and its OpenAI Realtime
// backend) over a direct call to the same fixture, per direction, at 20 ms
// chunks and real-time pacing. It gates on OA_LIVE_VOICE_MEASURE so it never
// runs by accident in an ordinary test pass.

// liveChunkBytes is 20 ms of 16-bit mono PCM at 16000 Hz: 0.02 * 16000 * 2.
// LiveRequest.Format only defines pcm16_24000 (inference.thrift LiveFormat);
// that is what every session declares. The byte count sent is independent of
// that label: the fixture and the provider's backend treat Append's payload
// as opaque even-sized bytes (CONTRACT.md "even-sized frames of 1..65536
// bytes"), so a 16 kHz-shaped chunk exercises the same transport path as a
// 24 kHz one without tripping the format validation in live_backend.go.
const liveChunkBytes = 640
const liveChunkInterval = 20 * time.Millisecond
const liveStreamDuration = 30 * time.Second

var liveChunkCount = int(liveStreamDuration / liveChunkInterval)

// liveFixtureSecret is the bearer secret the OA-path credential applies and
// the fixture checks. The direct path dials the fixture itself and carries no
// credential machinery.
const liveFixtureSecret = "fixture-live-voice-secret"

// liveDurations is a percentile-reporting sample set, matching the style of
// conformance/clients/inference/latency_test.go's samples type.
type liveDurations []time.Duration

func (s liveDurations) percentile(p float64) float64 {
	if len(s) == 0 {
		return 0
	}
	sorted := append(liveDurations(nil), s...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	i := int(p * float64(len(sorted)-1))
	return float64(sorted[i].Nanoseconds()) / 1e6
}

func (s liveDurations) line(name string) string {
	return fmt.Sprintf("LIVE %-42s n=%-5d p50=%8.3f p95=%8.3f p99=%8.3f max=%8.3f ms",
		name, len(s), s.percentile(0.5), s.percentile(0.95), s.percentile(0.99), s.percentile(1))
}

// liveEchoFixture is a synthetic Realtime-shaped WebSocket upstream: one
// connection, one model id. It echoes each appended chunk back immediately as
// its own response.output_audio.delta, so a 30 s stream of 20 ms chunks
// produces a continuous train of round trips instead of waiting for
// response.create. A commit still ends the session correctly: on
// response.create it answers response.done "completed", which is what
// go/live.go's runLive needs to reach ReplyOutcomeCompleted and publish the
// delivery. This mirrors go/live_test.go's fixture's handshake and message
// shapes; only the timing of the echo differs.
type liveEchoFixture struct {
	server    *httptest.Server
	modelID   string
	authToken string

	mu         sync.Mutex
	receivedAt []time.Time
	sentAt     []time.Time
}

func newLiveEchoFixture(t *testing.T, modelID, authToken string) *liveEchoFixture {
	t.Helper()
	f := &liveEchoFixture{modelID: modelID, authToken: authToken}
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"id":%q}]}`, modelID)
	})
	mux.HandleFunc("/realtime", func(w http.ResponseWriter, r *http.Request) {
		if f.authToken != "" && r.Header.Get("Authorization") != "Bearer "+f.authToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		if _, _, err := c.Read(ctx); err != nil { // the client's session.update
			return
		}
		if c.Write(ctx, websocket.MessageText, []byte(`{"type":"session.updated"}`)) != nil {
			return
		}
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg struct{ Type, Audio string }
			if json.Unmarshal(b, &msg) != nil {
				return
			}
			switch msg.Type {
			case "input_audio_buffer.append":
				received := time.Now()
				audio, derr := base64.StdEncoding.DecodeString(msg.Audio)
				if derr != nil {
					return
				}
				echo, _ := json.Marshal(struct{ Type, Delta string }{"response.output_audio.delta", base64.StdEncoding.EncodeToString(audio)})
				werr := c.Write(ctx, websocket.MessageText, echo)
				sent := time.Now()
				if werr != nil {
					return
				}
				f.mu.Lock()
				f.receivedAt = append(f.receivedAt, received)
				f.sentAt = append(f.sentAt, sent)
				f.mu.Unlock()
			case "input_audio_buffer.commit":
				// acknowledged implicitly; response.create ends the session.
			case "response.create":
				done, _ := json.Marshal(map[string]any{
					"type": "response.done",
					"response": map[string]any{
						"status": "completed",
						"usage":  map[string]any{"input_tokens": 1, "output_tokens": 1},
					},
				})
				c.Write(ctx, websocket.MessageText, done)
				return
			}
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *liveEchoFixture) wsBase() string { return "ws" + strings.TrimPrefix(f.server.URL, "http") }

func (f *liveEchoFixture) snapshot() (received, sent []time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.receivedAt...), append([]time.Time(nil), f.sentAt...)
}

// directLiveClient dials liveEchoFixture directly, bypassing the runtime, the
// framed IPC and the router entirely: the same protocol shape as
// adapters/go/realtime/backend.go's connector, minus the OA path.
type directLiveClient struct{ conn *websocket.Conn }

func dialDirectLive(ctx context.Context, wsURL string) (*directLiveClient, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(1 << 20)
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.update"}`)); err != nil {
		conn.CloseNow()
		return nil, err
	}
	if _, _, err := conn.Read(ctx); err != nil { // session.updated
		conn.CloseNow()
		return nil, err
	}
	return &directLiveClient{conn: conn}, nil
}

func (d *directLiveClient) append(ctx context.Context, audio []byte) error {
	payload, _ := json.Marshal(struct{ Type, Audio string }{"input_audio_buffer.append", base64.StdEncoding.EncodeToString(audio)})
	return d.conn.Write(ctx, websocket.MessageText, payload)
}

// readAudio blocks for the next response.output_audio.delta, skipping any
// other message type the fixture might send.
func (d *directLiveClient) readAudio(ctx context.Context) ([]byte, error) {
	for {
		_, b, err := d.conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var msg struct{ Type, Delta string }
		if json.Unmarshal(b, &msg) != nil {
			continue
		}
		if msg.Type == "response.output_audio.delta" {
			return base64.StdEncoding.DecodeString(msg.Delta)
		}
	}
}

func (d *directLiveClient) commit(ctx context.Context) error {
	if err := d.conn.Write(ctx, websocket.MessageText, []byte(`{"type":"input_audio_buffer.commit"}`)); err != nil {
		return err
	}
	return d.conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create"}`))
}

func (d *directLiveClient) close() { d.conn.CloseNow() }

// liveChunk fills a deterministic, non-zero, even-length payload; content
// does not matter for a transport-latency measurement.
func liveChunk(session, index int) []byte {
	b := make([]byte, liveChunkBytes)
	for j := range b {
		b[j] = byte(session*7 + index + j)
	}
	return b
}

// runOneDirectSession streams liveChunkCount chunks at real-time pacing over
// one direct WebSocket connection to its own fixture, and returns the
// per-chunk one-way input latency (call start to fixture receipt) and output
// latency (fixture send to client receipt).
func runOneDirectSession(t *testing.T, ctx context.Context, session int) (input, output liveDurations) {
	t.Helper()
	fx := newLiveEchoFixture(t, fmt.Sprintf("direct-model-%d", session), "")
	d, err := dialDirectLive(ctx, fx.wsBase()+"/realtime")
	if err != nil {
		t.Fatalf("direct session %d: dial: %v", session, err)
	}
	defer d.close()

	callStart := make([]time.Time, 0, liveChunkCount)
	arrival := make([]time.Time, 0, liveChunkCount)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < liveChunkCount; i++ {
			if _, err := d.readAudio(ctx); err != nil {
				done <- fmt.Errorf("direct session %d: read %d: %w", session, i, err)
				return
			}
			arrival = append(arrival, time.Now())
		}
		done <- nil
	}()

	next := time.Now()
	for i := 0; i < liveChunkCount; i++ {
		chunk := liveChunk(session, i)
		start := time.Now()
		if err := d.append(ctx, chunk); err != nil {
			t.Fatalf("direct session %d: append %d: %v", session, i, err)
		}
		callStart = append(callStart, start)
		next = next.Add(liveChunkInterval)
		if until := time.Until(next); until > 0 {
			time.Sleep(until)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("direct session %d: timed out draining echoes", session)
	}
	if err := d.commit(ctx); err != nil {
		t.Fatalf("direct session %d: commit: %v", session, err)
	}

	received, sent := fx.snapshot()
	n := min(len(callStart), len(received), len(arrival), len(sent))
	input = make(liveDurations, 0, n)
	output = make(liveDurations, 0, n)
	for i := 0; i < n; i++ {
		input = append(input, received[i].Sub(callStart[i]))
		output = append(output, arrival[i].Sub(sent[i]))
	}
	return input, output
}

// runOneOALiveSession streams liveChunkCount chunks through the resolved Live
// client (the isolated runtime, its router, the real Live provider and
// go/live_backend.go's OpenAI Realtime backend) against its own fixture, and
// returns the same two per-chunk latency samples as runOneDirectSession.
func runOneOALiveSession(t *testing.T, ctx context.Context, live *inferenceclient.Live, fx *liveEchoFixture, session int) (input, output liveDurations) {
	t.Helper()
	req := iwire.LiveRequest{Model: fx.modelID, Voice: "marin", Format: iwire.LiveFormatPcm1624000,
		Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Credential: "live-cred"}
	a, err := live.Start(ctx, req)
	if err != nil || a.Outcome != iwire.StartOutcomeAccepted {
		t.Fatalf("OA session %d: start: %+v %v", session, a, err)
	}

	callStart := make([]time.Time, 0, liveChunkCount)
	arrival := make([]time.Time, 0, liveChunkCount)
	done := make(chan error, 1)
	go func() {
		cursor := int64(0)
		got := 0
		for got < liveChunkCount {
			if ctx.Err() != nil {
				done <- ctx.Err()
				return
			}
			page, err := live.Observe(ctx, a.Operation, cursor, inferenceclient.PageDeltas, inferenceclient.PageBytes, 20)
			if err != nil || page.Outcome != iwire.PageOutcomePage {
				done <- fmt.Errorf("OA session %d: observe: %+v %v", session, page, err)
				return
			}
			now := time.Now()
			for _, d := range page.Deltas {
				if d.Kind == iwire.DeltaKindAudio && d.Audio != nil {
					arrival = append(arrival, now)
					got++
				}
			}
			cursor = page.Next
		}
		done <- nil
	}()

	next := time.Now()
	for i := 0; i < liveChunkCount; i++ {
		chunk := liveChunk(session, i)
		start := time.Now()
		res, err := live.Append(ctx, a.Operation, int64(i), chunk)
		if err != nil || res.Outcome != iwire.LiveInputOutcomeAccepted {
			t.Fatalf("OA session %d: append %d: %+v %v", session, i, res, err)
		}
		callStart = append(callStart, start)
		next = next.Add(liveChunkInterval)
		if until := time.Until(next); until > 0 {
			time.Sleep(until)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("OA session %d: timed out draining echoes", session)
	}
	if res, err := live.Commit(ctx, a.Operation); err != nil || (res.Outcome != iwire.LiveInputOutcomeAccepted && res.Outcome != iwire.LiveInputOutcomeDuplicate) {
		t.Fatalf("OA session %d: commit: %+v %v", session, res, err)
	}
	// Drain to the terminal delta so the operation ends cleanly.
	cursor := int64(0)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		page, err := live.Observe(ctx, a.Operation, cursor, inferenceclient.PageDeltas, inferenceclient.PageBytes, 500)
		if err != nil || page.Outcome != iwire.PageOutcomePage {
			t.Errorf("OA session %d: drain observe: %+v %v", session, page, err)
			break
		}
		ended := false
		for _, d := range page.Deltas {
			if d.LiveEnd != nil {
				if d.LiveEnd.Outcome != iwire.ReplyOutcomeCompleted {
					t.Errorf("OA session %d: end outcome %+v", session, d.LiveEnd)
				}
				ended = true
			}
		}
		cursor = page.Next
		if ended || page.AtEnd {
			break
		}
	}

	received, sent := fx.snapshot()
	n := min(len(callStart), len(received), len(arrival), len(sent))
	input = make(liveDurations, 0, n)
	output = make(liveDurations, 0, n)
	for i := 0; i < n; i++ {
		input = append(input, received[i].Sub(callStart[i]))
		output = append(output, arrival[i].Sub(sent[i]))
	}
	return input, output
}

// livePassResult is one pass's four sample sets: the OA path and the direct
// baseline, each split into input (Append/send) and output (Observe/receive)
// direction. inputWindow holds the same input direction measured through the
// gateway window's Realtime WebSocket instead of the native Live client, and
// turnWindow the one wait from response.create to the turn's first audio
// delta. The window's output direction has no per-chunk counterpart: the
// single-turn route observes only after response.create, so the fixture's
// echoes arrive as one backlog rather than as round trips.
type livePassResult struct {
	inputOA, outputOA, inputDirect, outputDirect liveDurations
	inputWindow, turnWindow                      liveDurations
}

// windowLiveEvent is one Realtime event's envelope and the fields this
// measurement reads from it.
type windowLiveEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Response *struct {
		Status string `json:"status"`
	} `json:"response"`
}

func writeWindowLive(ctx context.Context, conn *websocket.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

func readWindowLive(ctx context.Context, conn *websocket.Conn) (windowLiveEvent, error) {
	var event windowLiveEvent
	_, payload, err := conn.Read(ctx)
	if err != nil {
		return event, err
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return event, err
	}
	if event.Type == "error" && event.Error != nil {
		return event, fmt.Errorf("realtime error %s: %s", event.Error.Code, event.Error.Message)
	}
	return event, nil
}

// runOneWindowLiveSession streams liveChunkCount chunks through the gateway
// window's GET /v1/realtime WebSocket, which maps the Realtime events onto one
// live@1 session in the same isolated runtime the native pass uses. It returns
// the per-chunk input-direction samples (the fixture's receipt of one
// input_audio_buffer.append's bytes minus the client's timestamp of starting
// that WebSocket write, the same definition as the native and direct paths)
// and the one turn wait from response.create to the first audio delta.
func runOneWindowLiveSession(t *testing.T, ctx context.Context, gateway, key string, fx *liveEchoFixture, session int) (input, turn liveDurations) {
	t.Helper()
	dial, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	conn, response, err := websocket.Dial(dial, "ws://"+gateway+"/v1/realtime?model="+fx.modelID,
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + key}}})
	if err != nil {
		if response != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			response.Body.Close()
			t.Fatalf("window session %d: dial status %s: %v\n%s", session, response.Status, err, body)
		}
		t.Fatalf("window session %d: dial: %v", session, err)
	}
	conn.SetReadLimit(1 << 20)
	defer conn.CloseNow()

	if event, err := readWindowLive(ctx, conn); err != nil || event.Type != "session.created" {
		t.Fatalf("window session %d: session.created: %+v %v", session, event, err)
	}
	update := map[string]any{"type": "session.update", "session": map[string]any{
		"type": "realtime", "model": fx.modelID, "output_modalities": []string{"audio"},
		"audio": map[string]any{
			"input":  map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}, "turn_detection": nil},
			"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}, "voice": "marin"},
		},
	}}
	if err := writeWindowLive(ctx, conn, update); err != nil {
		t.Fatalf("window session %d: session.update: %v", session, err)
	}
	if event, err := readWindowLive(ctx, conn); err != nil || event.Type != "session.updated" {
		t.Fatalf("window session %d: session.updated: %+v %v", session, event, err)
	}

	callStart := make([]time.Time, 0, liveChunkCount)
	next := time.Now()
	for i := 0; i < liveChunkCount; i++ {
		chunk := liveChunk(session, i)
		start := time.Now()
		if err := writeWindowLive(ctx, conn, map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(chunk)}); err != nil {
			t.Fatalf("window session %d: append %d: %v", session, i, err)
		}
		callStart = append(callStart, start)
		next = next.Add(liveChunkInterval)
		if until := time.Until(next); until > 0 {
			time.Sleep(until)
		}
	}
	if err := writeWindowLive(ctx, conn, map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		t.Fatalf("window session %d: commit: %v", session, err)
	}
	if event, err := readWindowLive(ctx, conn); err != nil || event.Type != "input_audio_buffer.committed" {
		t.Fatalf("window session %d: committed: %+v %v", session, event, err)
	}
	created := time.Now()
	if err := writeWindowLive(ctx, conn, map[string]any{"type": "response.create"}); err != nil {
		t.Fatalf("window session %d: response.create: %v", session, err)
	}
	if event, err := readWindowLive(ctx, conn); err != nil || event.Type != "response.created" {
		t.Fatalf("window session %d: response.created: %+v %v", session, event, err)
	}
	drain, stopDrain := context.WithTimeout(ctx, 2*time.Minute)
	defer stopDrain()
	audio, first := 0, time.Time{}
	for {
		event, err := readWindowLive(drain, conn)
		if err != nil {
			t.Fatalf("window session %d: drain: %v", session, err)
		}
		if event.Type == "response.output_audio.delta" {
			if audio == 0 {
				first = time.Now()
			}
			audio++
		}
		if event.Type == "response.done" {
			if event.Response == nil || event.Response.Status != "completed" {
				t.Fatalf("window session %d: terminal response %+v", session, event.Response)
			}
			break
		}
	}
	if audio != liveChunkCount {
		t.Errorf("window session %d: audio deltas = %d, want %d", session, audio, liveChunkCount)
	}

	received, _ := fx.snapshot()
	n := min(len(callStart), len(received))
	input = make(liveDurations, 0, n)
	for i := 0; i < n; i++ {
		input = append(input, received[i].Sub(callStart[i]))
	}
	return input, liveDurations{first.Sub(created)}
}

func mergeDurations(sets ...liveDurations) liveDurations {
	var out liveDurations
	for _, s := range sets {
		out = append(out, s...)
	}
	return out
}

// runLivePass builds sessions concurrent OA sessions (each against its own
// fixture, host and model id, all sharing one credential) through one
// isolated runtime subprocess, and sessions concurrent direct sessions
// against their own fixtures with no runtime at all, then merges each
// direction's samples across sessions. windowSessions further sessions run
// the same stream through that runtime's gateway window, each on its own
// fixture and host, and open the window for the pass.
func runLivePass(t *testing.T, ctx context.Context, bin, self, uid string, sessions, windowSessions int) livePassResult {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	total := sessions + windowSessions

	fixtures := make([]*liveEchoFixture, total)
	hosted := make([]string, total)
	for i := range fixtures {
		fixtures[i] = newLiveEchoFixture(t, fmt.Sprintf("live-voice-model-%d-%d", sessions, i), liveFixtureSecret)
		// profiles is explicit: the registration-migration default in
		// serve/runtime_inference_operator.go's defaultProfiles only
		// special-cases the openai-compatible wire and otherwise falls back to
		// ["chat"], so an openai-realtime host declared without an explicit
		// profile list would migrate as chat-only and the router would answer
		// live@1 requests router:not-here. Flagged in feedback/.
		hosted[i] = fmt.Sprintf(`{"name":"live%d","base":%q,"wire":%q,"credential":"live-cred","profiles":[%q]}`,
			i, fixtures[i].server.URL, router.WireOpenAIRealtime, router.ProfileLive)
	}
	config := fmt.Sprintf(`{"local":[],"hosted":[%s]}`, strings.Join(hosted, ","))
	for _, file := range []struct{ path, body string }{
		{filepath.Join(state, "credentials", credentialsBackendFile), "file-0600\n"},
		{filepath.Join(state, "inference", inferenceHostsFile), config},
	} {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.path, []byte(file.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	name := fmt.Sprintf("oa-live-voice-%d-%d", sessions, time.Now().UnixNano()%1e9)
	args := []string{"serve", "runtime", "--isolated", name, "--state-dir", state}
	gateway := ""
	if windowSessions > 0 {
		gateway = freeLoopbackPort(t)
		args = append(args, "--gateway", gateway)
	}
	serve := exec.CommandContext(ctx, bin, args...)
	stderr, err := serve.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serve.Process.Kill(); serve.Wait() })
	endpoints := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		sent := false
		for scanner.Scan() {
			line := scanner.Text()
			t.Logf("runtime(%s): %s", name, line)
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "ABSTRACTION_RUNTIME_ENDPOINT="); ok && !sent {
				endpoints <- value
				sent = true
			}
		}
		io.Copy(io.Discard, stderr)
	}()
	var endpoint string
	select {
	case endpoint = <-endpoints:
	case <-time.After(60 * time.Second):
		t.Fatal("the isolated runtime printed no endpoint")
	}

	cli := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, bin, append(args, "--endpoint", endpoint, "--timeout", "30s")...).CombinedOutput()
		if err != nil {
			t.Fatalf("openabstractions %v: %v\n%s", args, err, out)
		}
		return out
	}
	// --use-by names both self (the client dialing the runtime directly, as
	// Live's own credential apply binds the calling subject) and bin (the
	// runtime subprocess's own executable, which the router's survey applies
	// the credential as: runtime_inference.go composeInferenceProvider reads
	// hosted listings "as the runtime's own program"). Without bin, the
	// survey's GET /models is refused credential:not_permitted and every host
	// reads down, unlike an in-process test runtime where both identities
	// happen to be the same running process.
	credAdd := exec.CommandContext(ctx, bin, "credentials", "add", "live-cred", "--target", "127.0.0.1",
		"--for", inference.LiveContract, "--for", "abstraction.router/router@1", "--from-stdin",
		"--use-by", self, "--use-by", bin, "--endpoint", endpoint, "--timeout", "30s")
	credAdd.Stdin = strings.NewReader(liveFixtureSecret + "\n")
	if out, err := credAdd.CombinedOutput(); err != nil {
		t.Fatalf("credentials add: %v\n%s", err, out)
	}
	for i := range fixtures {
		cli("rights", "grant", "--program", self, "--account", uid, "--action", inference.ActionComplete,
			"--resource", inference.ResourceHost(fmt.Sprintf("live%d", i)), "--runtime-program", bin)
	}
	cli("rights", "grant", "--program", self, "--account", uid, "--action", "abstraction.storage/content.write",
		"--resource", "output:live", "--runtime-program", bin)

	call, stop := context.WithTimeout(context.Background(), 5*time.Second)
	result, err := resolution.NewUnverifiedClient(endpoint, 5*time.Second).Resolve(call, fwire.ResolveRequest{
		Capability: "abstraction.inference", Contracts: []string{inference.LiveContract},
		Guarantees: []string{inference.GuaranteeHosted}, Scope: fwire.ScopeRemote})
	stop()
	if err != nil || result.Status != fwire.ResolutionStatusResolved {
		t.Fatalf("resolve live@1: %+v %v", result, err)
	}
	bound, err := resolution.BindLocal(ctx, *result.Reference, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	live := inferenceclient.NewLiveWithTransport(bound)

	t.Logf("host list:\n%s", cli("inference", "host", "list"))

	// One short warm-up round on session 0's host, discarded, so the first
	// named-pipe connection's cost (MEASURED.txt's "first call") does not
	// land inside the timed samples.
	warm, err := live.Start(ctx, iwire.LiveRequest{Model: fixtures[0].modelID, Voice: "marin", Format: iwire.LiveFormatPcm1624000,
		Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Credential: "live-cred"})
	if err != nil || warm.Outcome != iwire.StartOutcomeAccepted {
		t.Fatalf("warm-up start: %+v %v", warm, err)
	}
	if _, err := live.Cancel(ctx, warm.Operation); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	oaIn := make([]liveDurations, sessions)
	oaOut := make([]liveDurations, sessions)
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			oaIn[i], oaOut[i] = runOneOALiveSession(t, ctx, live, fixtures[i], i)
		}(i)
	}
	wg.Wait()

	directIn := make([]liveDurations, sessions)
	directOut := make([]liveDurations, sessions)
	var wg2 sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			directIn[i], directOut[i] = runOneDirectSession(t, ctx, i)
		}(i)
	}
	wg2.Wait()

	windowIn := make([]liveDurations, windowSessions)
	windowTurn := make([]liveDurations, windowSessions)
	if windowSessions > 0 {
		// The window binds the peer that opened the connection and requires
		// the local key minted for that program, so the key is issued for
		// this test binary, the program that dials it. --credential names
		// the hosted credential the window's requests spend under: a key
		// without one carries local-only@1 (INF-W5) and the fixture hosts,
		// which are hosted, would route nowhere.
		issued := cli("inference", "key", "issue", "--for", self, "--credential", "live-cred", "--json")
		var key struct{ Key string }
		document := issued[strings.Index(string(issued), "{"):]
		if err := json.Unmarshal(document, &key); err != nil || !strings.HasPrefix(key.Key, localKeyPrefix) {
			t.Fatalf("key issue: %v\n%s", err, issued)
		}
		var wg3 sync.WaitGroup
		for i := 0; i < windowSessions; i++ {
			wg3.Add(1)
			go func(i int) {
				defer wg3.Done()
				windowIn[i], windowTurn[i] = runOneWindowLiveSession(t, ctx, gateway, key.Key, fixtures[sessions+i], sessions+i)
			}(i)
		}
		wg3.Wait()
	}

	return livePassResult{
		inputOA: mergeDurations(oaIn...), outputOA: mergeDurations(oaOut...),
		inputDirect: mergeDurations(directIn...), outputDirect: mergeDurations(directOut...),
		inputWindow: mergeDurations(windowIn...), turnWindow: mergeDurations(windowTurn...),
	}
}

// verdict reports the added p99 per direction against DECISION.md section 4's
// 200 ms threshold.
func printLivePass(pass string, r livePassResult) {
	fmt.Println(r.inputOA.line(pass + " input OA"))
	fmt.Println(r.inputDirect.line(pass + " input direct"))
	fmt.Printf("LIVE %-42s added p50=%8.3f p95=%8.3f p99=%8.3f ms\n", pass+" input added(OA-direct)",
		r.inputOA.percentile(0.5)-r.inputDirect.percentile(0.5),
		r.inputOA.percentile(0.95)-r.inputDirect.percentile(0.95),
		r.inputOA.percentile(0.99)-r.inputDirect.percentile(0.99))
	fmt.Println(r.outputOA.line(pass + " output OA"))
	fmt.Println(r.outputDirect.line(pass + " output direct"))
	fmt.Printf("LIVE %-42s added p50=%8.3f p95=%8.3f p99=%8.3f ms\n", pass+" output added(OA-direct)",
		r.outputOA.percentile(0.5)-r.outputDirect.percentile(0.5),
		r.outputOA.percentile(0.95)-r.outputDirect.percentile(0.95),
		r.outputOA.percentile(0.99)-r.outputDirect.percentile(0.99))
	if len(r.inputWindow) > 0 {
		fmt.Println(r.inputWindow.line(pass + " input window"))
		fmt.Printf("LIVE %-42s added p50=%8.3f p95=%8.3f p99=%8.3f ms\n", pass+" input added(window-direct)",
			r.inputWindow.percentile(0.5)-r.inputDirect.percentile(0.5),
			r.inputWindow.percentile(0.95)-r.inputDirect.percentile(0.95),
			r.inputWindow.percentile(0.99)-r.inputDirect.percentile(0.99))
		fmt.Printf("LIVE %-42s %8.3f ms\n", pass+" window turn (create to first audio)", r.turnWindow.percentile(0.5))
	}
}

// TestLiveVoiceMeasurement runs the measurement DECISION.md section 4 and
// section 7 step 7 call for before live@1's transport window is built: the
// added latency of the OA path (an isolated serve runtime subprocess, its
// router, the real Live provider and its OpenAI Realtime backend) over a
// direct call to a fixture upstream, per direction, at 20 ms chunks and
// real-time pacing for 30 s, at one session and at five concurrent sessions.
// It runs only with OA_LIVE_VOICE_MEASURE set, and never touches an installed
// runtime: every runtime this test starts is its own --isolated subprocess
// with a private --state-dir, killed through its own *os.Process handle.
func TestLiveVoiceMeasurement(t *testing.T) {
	if os.Getenv("OA_LIVE_VOICE_MEASURE") == "" {
		t.Skip("set OA_LIVE_VOICE_MEASURE=1 to measure")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "openabstractions")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build openabstractions: %v\n%s", err, out)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = filepath.Clean(self)
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}

	fmt.Printf("LIVE platform %s/%s chunk %d bytes every %v for %v (%d chunks)\n",
		runtime.GOOS, runtime.GOARCH, liveChunkBytes, liveChunkInterval, liveStreamDuration, liveChunkCount)

	single := runLivePass(t, ctx, bin, self, account.Uid, 1, 1)
	printLivePass("one session", single)

	concurrent := runLivePass(t, ctx, bin, self, account.Uid, 5, 0)
	printLivePass("five concurrent sessions", concurrent)

	const thresholdMS = 200.0
	worst := 0.0
	for _, pair := range []struct {
		name       string
		oa, direct liveDurations
	}{
		{"one session input", single.inputOA, single.inputDirect},
		{"one session output", single.outputOA, single.outputDirect},
		{"five concurrent input", concurrent.inputOA, concurrent.inputDirect},
		{"five concurrent output", concurrent.outputOA, concurrent.outputDirect},
		{"window input", single.inputWindow, single.inputDirect},
	} {
		if len(pair.oa) == 0 {
			continue
		}
		added := pair.oa.percentile(0.99) - pair.direct.percentile(0.99)
		if added > worst {
			worst = added
		}
		fmt.Printf("LIVE verdict %-32s p99 added %8.3f ms (threshold %v ms)\n", pair.name, added, thresholdMS)
	}
	if worst < thresholdMS {
		fmt.Printf("LIVE VERDICT pass: worst p99 added %.3f ms < %v ms\n", worst, thresholdMS)
	} else {
		fmt.Printf("LIVE VERDICT fail: worst p99 added %.3f ms >= %v ms\n", worst, thresholdMS)
	}
}
