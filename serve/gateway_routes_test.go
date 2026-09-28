package main

// TestGatewayRoutes is the standing test the gateway window's routes lacked:
// research/feedback-archive-2026-09-24/2026-09-22-realtime-window-rights-and-first-real-open.md found
// that GET /v1/realtime had fixture coverage in
// openabstractions-flat/abstraction-inference/adapters/go/gateway/live_test.go for
// three days without ever being opened on a real serve runtime, and that the
// first real open 404'd because the key was issued without --credential (a
// hosted host routes nowhere under local-only@1, INF-W5). This file opens
// every route the window serves on one isolated in-process runtime, with a
// key issued the way a client must: `inference key issue --for <program>
// --credential <name>`.
//
// The window (openabstractions-flat/abstraction-inference/adapters/go/gateway/window.go
// serveHTTP) serves nine routes; this file covers all nine, each as its own
// subtest so `go test -run 'GatewayRoute/<name>'` isolates one:
//   - POST /v1/chat/completions (chat_completions)
//   - POST /v1/messages, the Anthropic dialect of the same chat@1 (messages)
//   - GET  /v1/models (models)
//   - POST /v1/embeddings (embeddings)
//   - POST /v1/audio/transcriptions (audio_transcriptions)
//   - POST /v1/audio/speech (audio_speech)
//   - POST /v1/images/generations (images_generations)
//   - POST /v1/images/edits (images_edits)
//   - GET  /v1/realtime, a WebSocket upgrade (realtime)
//
// There is no /v1/responses route: window.go's switch names exactly the nine
// paths above, and the 404 body it writes lists the same nine.
//
// GET /v1/models writes no audit line: window.go's models handler never calls
// w.record (it has no admission to decide, only a lookup of the models this
// key's rights already let it see), so that subtest checks the reply only.
// Every other subtest checks both the reply and the audit line the route's
// admission produces, by Profile and by a model name unique to that subtest.
//
// One openai-compatible hosted fixture (gwhost) serves chat, messages, models,
// embeddings, transcriptions, speech and images: openai-compatible's default
// paths (abstraction-router/go/hosted.go NewHosted) hang beside the host's
// base with no /v1 prefix (h.Chat = "/chat/completions", so EmbedURL is
// base+"/embeddings", not base+"/v1/embeddings"). A second openai-realtime
// hosted fixture (gwlive) serves realtime; its wire defaults h.Chat to
// "/realtime", the same path serve/runtime_live_voice_measurement_test.go's
// liveEchoFixture already answers, reused here unmodified.
//
// Content-store rights are granted narrowly: output:speech, output:image and
// output:live are static resources a provider's PrepareContentWrite checks
// before admission (speech.go, image.go, live.go); transcription's input
// audio and image edit's uploaded input are stored by the window itself under
// their own sha256 digest (gateway/transcription.go, gateway/image.go), which
// this file grants because it fixes those bytes and can compute the digest
// before the call, and the one generated PNG both images routes return is
// granted once for content.read since generation and edit answer the same
// bytes.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	websocket "github.com/openabstractions/abstraction-inference/adapters/go/testsocket"
	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
)

// gwFixtureSecret is the bearer secret gwhost and gwlive both check, applied
// through the one hosted credential every route's request carries.
const gwFixtureSecret = "gateway-route-secret"

// gwPNG is a fixed image both images routes answer: images_generations and
// images_edits share this exact byte sequence so their output digest, and so
// their content.read grant, is the same for both.
var gwPNG = []byte("\x89PNG\r\n\x1a\ngateway-route-output")

// gwEditInputPNG is a distinct image only images_edits uploads, so its
// content.write grant (on its own digest) is visibly separate from the
// output's content.read grant.
var gwEditInputPNG = []byte("\x89PNG\r\n\x1a\ngateway-route-edit-input")

// gwWAV is a fixed WAV both the transcription input and the speech output
// use: RIFF/WAVE bytes satisfy gateway/transcription.go's audioType sniff and
// the core provider's speech.go WAV-shape check on the way back.
func gwWAV(tag byte) []byte {
	b := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 40)...)
	b[len(b)-1] = tag
	return b
}

func gwDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// gwChatFixture answers every path an openai-compatible hosted host built by
// abstraction-router/go/hosted.go's NewHosted addresses beside its base: no
// /v1 prefix. It checks the Authorization bearer the runtime's credential
// apply attaches, so a request that reaches it without gwFixtureSecret proves
// the credential path, not just the route, is exercised.
type gwChatFixture struct {
	server *httptest.Server
}

func newGwChatFixture(t *testing.T) *gwChatFixture {
	t.Helper()
	f := &gwChatFixture{}
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+gwFixtureSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"gw-chat-model"},{"id":"gw-messages-model"},{"id":"gw-embed-model"},`+
			`{"id":"gw-transcribe-model"},{"id":"gw-speech-model"},{"id":"gw-image-gen-model"},{"id":"gw-image-edit-model"}]}`)
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, word := range strings.SplitAfter("Hello from the gateway route test", " ") {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", word)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":6}}\n\ndata: [DONE]\n\n")
	})
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var in struct {
			Input []string `json:"input"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &in); err != nil || len(in.Input) == 0 {
			var one struct {
				Input string `json:"input"`
			}
			json.Unmarshal(raw, &one)
			in.Input = []string{one.Input}
		}
		data := make([]string, 0, len(in.Input))
		for i := range in.Input {
			data = append(data, fmt.Sprintf(`{"index":%d,"embedding":[0.1,0.2,0.3]}`, i))
		}
		fmt.Fprintf(w, `{"data":[%s],"usage":{"prompt_tokens":%d}}`, strings.Join(data, ","), len(in.Input))
	})
	mux.HandleFunc("/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"language":"en","duration":1.2,"text":"hello gateway","segments":[{"id":0,"start":0,"end":1.2,"text":"hello gateway"}]}`)
	})
	mux.HandleFunc("/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(gwWAV('S'))
	})
	imageReply := func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		io.Copy(io.Discard, io.LimitReader(r.Body, 40<<20))
		fmt.Fprintf(w, `{"data":[{"b64_json":%q}],"usage":{}}`, base64.StdEncoding.EncodeToString(gwPNG))
	}
	mux.HandleFunc("/images/generations", imageReply)
	mux.HandleFunc("/images/edits", imageReply)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// gwLiveFixture is a minimal Realtime-shaped WebSocket upstream: one
// session.update/session.updated handshake, one echoed audio delta per
// input_audio_buffer.append, and response.done "completed" on
// response.create. It answers /models and /realtime exactly as
// abstraction-router/go/hosted.go's NewHosted addresses an openai-realtime
// host (h.Chat = "/realtime"), the same shape
// serve/runtime_live_voice_measurement_test.go's liveEchoFixture answers,
// reproduced here (not imported) because that file is owned by another
// worker on this task and this one only reads it.
type gwLiveFixture struct {
	server  *httptest.Server
	modelID string
}

func newGwLiveFixture(t *testing.T, modelID string) *gwLiveFixture {
	t.Helper()
	f := &gwLiveFixture{modelID: modelID}
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"id":%q}]}`, modelID)
	})
	mux.HandleFunc("/realtime", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+gwFixtureSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		if _, _, err := c.Read(ctx); err != nil { // session.update
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
				audio, derr := base64.StdEncoding.DecodeString(msg.Audio)
				if derr != nil {
					return
				}
				echo, _ := json.Marshal(struct{ Type, Delta string }{"response.output_audio.delta", base64.StdEncoding.EncodeToString(audio)})
				if c.Write(ctx, websocket.MessageText, echo) != nil {
					return
				}
			case "input_audio_buffer.commit":
			case "response.create":
				done, _ := json.Marshal(map[string]any{"type": "response.done",
					"response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}})
				c.Write(ctx, websocket.MessageText, done)
				return
			}
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}
func (f *gwLiveFixture) wsBase() string { return "ws" + strings.TrimPrefix(f.server.URL, "http") }

// windowLiveEvent, writeWindowLive and readWindowLive are defined once in
// serve/runtime_live_voice_measurement_test.go (same package) and reused
// here unmodified.

// runRights runs the rights command in-process, matching runInference's
// (runtime_inference_gateway_test.go) and runCredentials's
// (credentials_test.go) pattern.
func runRights(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	err := rightsCommand(args, &out, &diagnostics)
	return out.String() + diagnostics.String(), err
}

// gwGrant grants one exact permit rule to self, the in-process test binary
// that is both the CLI caller and the HTTP/WebSocket client below (there is
// no separate runtime subprocess: runRuntimeReady runs the runtime in this
// same process, so "the runtime's own program" and "the command line" host
// add's help text names are this same executable).
func gwGrant(t *testing.T, options runtimeFlags, self, action, resource string) {
	t.Helper()
	out, err := runRights(t, "grant", "--program", self, "--action", action, "--resource", resource,
		"--endpoint", options.endpoint, "--timeout", "30s")
	if err != nil {
		t.Fatalf("rights grant --action %s --resource %s: %v\n%s", action, resource, err, out)
	}
}

// gwAuditCompleted asserts the audit carries one "completed" entry the
// window's route wrote for this call: RouteWindow, the profile the route's
// admission records (embed.go's comment: "" for chat), and a model name
// unique to the calling subtest so it cannot match another route's line.
func gwAuditCompleted(t *testing.T, options runtimeFlags, self, profile, model string) {
	t.Helper()
	out, err := runInference(t, "audit", "--json", "--endpoint", options.endpoint, "--timeout", "30s")
	if err != nil {
		t.Fatalf("audit: %v\n%s", err, out)
	}
	var audit struct {
		Entries []struct{ Route, Rung, Program, Profile, Model, Outcome, Reason string }
	}
	if err := json.Unmarshal([]byte(out), &audit); err != nil {
		t.Fatalf("audit json: %v\n%s", err, out)
	}
	for _, e := range audit.Entries {
		if e.Route == inference.RouteWindow && e.Profile == profile && e.Model == model && e.Outcome == "completed" && samePrograms(e.Program, self) {
			return
		}
	}
	t.Fatalf("no completed %s window audit line for model %q:\n%s", profileLabel(profile), model, out)
}
func profileLabel(profile string) string {
	if profile == "" {
		return "chat"
	}
	return profile
}

func TestGatewayRoutes(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("this platform cannot bind a loopback peer")
	}

	chatFixture := newGwChatFixture(t)
	liveFixture := newGwLiveFixture(t, "gw-live-model")

	options, _ := isolatedRuntime(t)
	options.gateway = freeLoopbackPort(t)
	hosts := fmt.Sprintf(`{"local":[],"hosted":[`+
		`{"name":"gwhost","base":%q,"wire":%q,"credential":"gw-cred","profiles":["chat","embed","transcription","speech","image"]},`+
		`{"name":"gwlive","base":%q,"wire":%q,"credential":"gw-cred","profiles":["live"]}`+
		`]}`, chatFixture.server.URL, router.WireOpenAICompatible, liveFixture.server.URL, router.WireOpenAIRealtime)
	for _, file := range []struct{ path, body string }{
		{filepath.Join(options.stateDir, "credentials", credentialsBackendFile), "file-0600\n"},
		{filepath.Join(options.stateDir, "inference", inferenceHostsFile), hosts},
	} {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.path, []byte(file.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = filepath.Clean(self)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}

	// The hosted credential every route below spends under. --for lists every
	// contract a route in this file calls; --use-by self covers both the
	// runtime's own host survey and this process's own window requests,
	// because both run as this one executable in-process.
	out, diagnostics, err := runCredentials(t, gwFixtureSecret+"\n", append([]string{"add", "gw-cred", "--target", "127.0.0.1",
		"--for", inference.Contract, "--for", inference.EmbedContract, "--for", inference.TranscriptionContract,
		"--for", inference.SpeechContract, "--for", inference.ImageContract, "--for", inference.LiveContract,
		"--for", "abstraction.router/router@1", "--from-stdin", "--use-by", self}, endpoint...)...)
	requireUserScopeAdd(t, err, out+diagnostics)

	gwGrant(t, options, self, inference.ActionComplete, inference.ResourceHost("gwhost"))
	gwGrant(t, options, self, inference.ActionComplete, inference.ResourceHost("gwlive"))
	gwGrant(t, options, self, contentWriteAction, speechOutputResource)
	gwGrant(t, options, self, contentWriteAction, "output:image")
	gwGrant(t, options, self, contentWriteAction, "output:live")
	// Transcription and image edit carry their input by digest
	// (wire.TranscriptionRequest.AudioDigest, wire.ImageRequest.ImageDigest);
	// the core provider reads it back with cfg.ResolveContent
	// (transcription.go, image.go) before sending it upstream, so each needs
	// both grants: content.write for the window's own StoreContent call and
	// content.read for the provider's ResolveContent call, on the same digest.
	transcribeAudio := gwWAV('T')
	gwGrant(t, options, self, contentWriteAction, gwDigest(transcribeAudio))
	gwGrant(t, options, self, contentReadAction, gwDigest(transcribeAudio))
	gwGrant(t, options, self, contentWriteAction, gwDigest(gwEditInputPNG))
	gwGrant(t, options, self, contentReadAction, gwDigest(gwEditInputPNG))
	gwGrant(t, options, self, contentReadAction, gwDigest(gwPNG))

	out, err = runInference(t, append([]string{"key", "issue", "--for", self, "--credential", "gw-cred", "--json"}, endpoint...)...)
	if err != nil {
		requireUserScopeAdd(t, err, out)
	}
	var issued struct{ Key string }
	if err := json.Unmarshal([]byte(out), &issued); err != nil || !strings.HasPrefix(issued.Key, localKeyPrefix) {
		t.Fatalf("key issue: %v\n%s", err, out)
	}
	key := issued.Key

	client := &http.Client{Timeout: 20 * time.Second}
	post := func(t *testing.T, path, contentType string, body []byte) (int, string, http.Header) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, "http://"+options.gateway+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw), resp.Header
	}

	// chat_completions and messages run first: the first chat@1 admission
	// surveys gwhost's /models, which the models subtest below depends on
	// having already happened (window.go's models route lists only models
	// the router's latest survey has seen).
	t.Run("chat_completions", func(t *testing.T) {
		started := time.Now()
		code, body, _ := post(t, "/v1/chat/completions", "application/json",
			[]byte(`{"model":"gw-chat-model","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		var reply struct {
			Choices []struct {
				Message      struct{ Content string }
				FinishReason string `json:"finish_reason"`
			}
			Usage struct {
				CompletionTokens int64 `json:"completion_tokens"`
			}
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil || len(reply.Choices) == 0 {
			t.Fatalf("malformed reply: %v\n%s", err, body)
		}
		if !strings.Contains(reply.Choices[0].Message.Content, "Hello from the gateway route test") {
			t.Fatalf("reply content: %q", reply.Choices[0].Message.Content)
		}
		if reply.Choices[0].FinishReason == "" {
			t.Fatalf("missing finish_reason: %s", body)
		}
		gwAuditCompleted(t, options, self, "", "gw-chat-model")
		t.Logf("chat_completions: %v", time.Since(started))
	})

	t.Run("messages", func(t *testing.T) {
		started := time.Now()
		code, body, _ := post(t, "/v1/messages", "application/json",
			[]byte(`{"model":"gw-messages-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		var reply struct {
			Type    string
			Role    string
			Content []struct{ Type, Text string }
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil || reply.Type != "message" || reply.Role != "assistant" || len(reply.Content) == 0 {
			t.Fatalf("malformed reply: %v\n%s", err, body)
		}
		found := false
		for _, block := range reply.Content {
			if block.Type == "text" && strings.Contains(block.Text, "Hello from the gateway route test") {
				found = true
			}
		}
		if !found {
			t.Fatalf("no matching text block: %s", body)
		}
		gwAuditCompleted(t, options, self, "", "gw-messages-model")
		t.Logf("messages: %v", time.Since(started))
	})

	t.Run("models", func(t *testing.T) {
		started := time.Now()
		req, _ := http.NewRequest(http.MethodGet, "http://"+options.gateway+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /v1/models: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		var list struct {
			Object string
			Data   []struct{ ID string }
		}
		if err := json.Unmarshal(raw, &list); err != nil || list.Object != "list" {
			t.Fatalf("malformed reply: %v\n%s", err, raw)
		}
		found := false
		for _, m := range list.Data {
			if m.ID == "gw-chat-model" {
				found = true
			}
		}
		if !found {
			t.Fatalf("gw-chat-model missing from the surveyed list: %s", raw)
		}
		// models writes no audit line: window.go's models handler has no
		// admission to decide, so there is nothing to check beyond the reply.
		t.Logf("models: %v", time.Since(started))
	})

	t.Run("embeddings", func(t *testing.T) {
		started := time.Now()
		code, body, _ := post(t, "/v1/embeddings", "application/json",
			[]byte(`{"model":"gw-embed-model","input":["hello gateway"]}`))
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		var reply struct {
			Data []struct {
				Embedding []float64
			}
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil || len(reply.Data) != 1 || len(reply.Data[0].Embedding) == 0 {
			t.Fatalf("malformed reply: %v\n%s", err, body)
		}
		gwAuditCompleted(t, options, self, "embed", "gw-embed-model")
		t.Logf("embeddings: %v", time.Since(started))
	})

	t.Run("audio_transcriptions", func(t *testing.T) {
		started := time.Now()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "clip.wav")
		if err != nil {
			t.Fatal(err)
		}
		part.Write(transcribeAudio)
		mw.WriteField("model", "gw-transcribe-model")
		mw.WriteField("response_format", "verbose_json")
		mw.Close()
		code, body, _ := post(t, "/v1/audio/transcriptions", mw.FormDataContentType(), buf.Bytes())
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		var reply struct {
			Text     string
			Language string
			Segments []struct{ Text string }
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil || reply.Text != "hello gateway" || reply.Language != "en" {
			t.Fatalf("malformed reply: %v\n%s", err, body)
		}
		gwAuditCompleted(t, options, self, "transcription", "gw-transcribe-model")
		t.Logf("audio_transcriptions: %v", time.Since(started))
	})

	t.Run("audio_speech", func(t *testing.T) {
		started := time.Now()
		code, body, headers := post(t, "/v1/audio/speech", "application/json",
			[]byte(`{"model":"gw-speech-model","voice":"alloy","input":"hello","response_format":"wav"}`))
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		if headers.Get("Content-Type") != "audio/wav" {
			t.Fatalf("content-type %q", headers.Get("Content-Type"))
		}
		if !strings.HasPrefix(body, "RIFF") || len(body) < 12 || body[8:12] != "WAVE" {
			t.Fatalf("not a WAV reply: %q", body)
		}
		gwAuditCompleted(t, options, self, "speech", "gw-speech-model")
		t.Logf("audio_speech: %v", time.Since(started))
	})

	t.Run("images_generations", func(t *testing.T) {
		started := time.Now()
		code, body, _ := post(t, "/v1/images/generations", "application/json",
			[]byte(`{"model":"gw-image-gen-model","prompt":"a lighthouse","n":1,"size":"512x512","response_format":"b64_json"}`))
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		var reply struct {
			Data []struct {
				B64JSON string `json:"b64_json"`
			}
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil || len(reply.Data) != 1 {
			t.Fatalf("malformed reply: %v\n%s", err, body)
		}
		if reply.Data[0].B64JSON != base64.StdEncoding.EncodeToString(gwPNG) {
			t.Fatalf("image bytes did not round-trip")
		}
		gwAuditCompleted(t, options, self, "image", "gw-image-gen-model")
		t.Logf("images_generations: %v", time.Since(started))
	})

	t.Run("images_edits", func(t *testing.T) {
		started := time.Now()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("model", "gw-image-edit-model")
		mw.WriteField("prompt", "add a boat")
		mw.WriteField("n", "1")
		mw.WriteField("size", "512x512")
		mw.WriteField("response_format", "b64_json")
		part, err := mw.CreateFormFile("image", "input.png")
		if err != nil {
			t.Fatal(err)
		}
		part.Write(gwEditInputPNG)
		mw.Close()
		code, body, _ := post(t, "/v1/images/edits", mw.FormDataContentType(), buf.Bytes())
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		var reply struct {
			Data []struct {
				B64JSON string `json:"b64_json"`
			}
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil || len(reply.Data) != 1 {
			t.Fatalf("malformed reply: %v\n%s", err, body)
		}
		if reply.Data[0].B64JSON != base64.StdEncoding.EncodeToString(gwPNG) {
			t.Fatalf("image bytes did not round-trip")
		}
		gwAuditCompleted(t, options, self, "image", "gw-image-edit-model")
		t.Logf("images_edits: %v", time.Since(started))
	})

	t.Run("realtime", func(t *testing.T) {
		started := time.Now()
		dial, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		conn, resp, err := websocket.Dial(dial, "ws://"+options.gateway+"/v1/realtime?model=gw-live-model",
			&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + key}}})
		if err != nil {
			if resp != nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				t.Fatalf("dial status %s: %v\n%s", resp.Status, err, body)
			}
			t.Fatalf("dial: %v", err)
		}
		conn.SetReadLimit(1 << 20)
		defer conn.CloseNow()
		ctx := context.Background()

		if event, err := readWindowLive(ctx, conn); err != nil || event.Type != "session.created" {
			t.Fatalf("session.created: %+v %v", event, err)
		}
		update := map[string]any{"type": "session.update", "session": map[string]any{
			"type": "realtime", "model": "gw-live-model", "output_modalities": []string{"audio"},
			"audio": map[string]any{
				"input":  map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}, "turn_detection": nil},
				"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}, "voice": "marin"},
			},
		}}
		if err := writeWindowLive(ctx, conn, update); err != nil {
			t.Fatalf("session.update: %v", err)
		}
		if event, err := readWindowLive(ctx, conn); err != nil || event.Type != "session.updated" {
			t.Fatalf("session.updated: %+v %v", event, err)
		}
		// live.go's Provider.liveInput refuses a commit under 4800 input bytes
		// (INF's 100 ms minimum turn), so this sends 8 chunks of 640 bytes.
		chunk := make([]byte, 640)
		for i := range chunk {
			chunk[i] = byte(i)
		}
		encoded := base64.StdEncoding.EncodeToString(chunk)
		for i := 0; i < 8; i++ {
			if err := writeWindowLive(ctx, conn, map[string]any{"type": "input_audio_buffer.append", "audio": encoded}); err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}
		if err := writeWindowLive(ctx, conn, map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if event, err := readWindowLive(ctx, conn); err != nil || event.Type != "input_audio_buffer.committed" {
			t.Fatalf("committed: %+v %v", event, err)
		}
		if err := writeWindowLive(ctx, conn, map[string]any{"type": "response.create"}); err != nil {
			t.Fatalf("response.create: %v", err)
		}
		if event, err := readWindowLive(ctx, conn); err != nil || event.Type != "response.created" {
			t.Fatalf("response.created: %+v %v", event, err)
		}
		drain, stopDrain := context.WithTimeout(ctx, 15*time.Second)
		defer stopDrain()
		audioDeltas := 0
		for {
			event, err := readWindowLive(drain, conn)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if event.Type == "response.output_audio.delta" {
				audioDeltas++
			}
			if event.Type == "response.done" {
				if event.Response == nil || event.Response.Status != "completed" {
					t.Fatalf("terminal response: %+v", event.Response)
				}
				break
			}
		}
		if audioDeltas == 0 {
			t.Fatal("no audio deltas before response.done")
		}
		gwAuditCompleted(t, options, self, "live", "gw-live-model")
		t.Logf("realtime: %v", time.Since(started))
	})
}
