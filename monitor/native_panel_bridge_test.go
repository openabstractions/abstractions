package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer collects the child's output lines from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) frames(t *testing.T) []nativeAsksFrame {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []nativeAsksFrame
	scanner := bufio.NewScanner(bytes.NewReader(s.buf.Bytes()))
	scanner.Buffer(make([]byte, 4096), maxNativeAsksLine)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, nativeAsksPrefix) {
			t.Fatalf("unexpected line %q", line)
		}
		if len(line) >= maxNativeAsksLine {
			t.Fatalf("line of %d bytes exceeds the channel", len(line))
		}
		var frame nativeAsksFrame
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, nativeAsksPrefix)), &frame); err != nil {
			t.Fatal(err)
		}
		out = append(out, frame)
	}
	return out
}

// assembled joins one request's chunks, checking their order.
func assembled(t *testing.T, frames []nativeAsksFrame, id string) (int, map[string]string, []byte, bool) {
	t.Helper()
	var body []byte
	status, headers, seq, done := 0, map[string]string(nil), 0, false
	for _, f := range frames {
		if f.Kind != "panel-response" || f.ID != id {
			continue
		}
		if f.Seq != seq || done {
			t.Fatalf("chunk %d out of order (want %d, done %v)", f.Seq, seq, done)
		}
		if seq == 0 {
			status, headers = f.Status, f.Headers
		}
		chunk, err := base64.StdEncoding.DecodeString(f.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, chunk...)
		seq++
		done = !f.More
	}
	return status, headers, body, done
}

func nativePanelTestBridge(t *testing.T, mux *http.ServeMux) (*nativeAsksBridge, *syncBuffer, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	bridge := newNativeAsksBridge(ctx, out)
	bridge.servePanel(newNativePanelServer(ctx, newPanelResources(panelResourceOriginMacOS, mux), bridge.write))
	return bridge, out, cancel
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A request line reaches the route with its method, headers and body; a
// large answer returns in chunks that each fit the channel.
func TestNativePanelRequestAndChunkedResponse(t *testing.T) {
	large := bytes.Repeat([]byte("0123456789abcdef"), 80000) // 1.28 MB: three chunks
	var seen string
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = r.Method + " " + r.Header.Get("X-Panel-Key") + " " + r.Header.Get("Content-Type") + " " + string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/large", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(large) })
	bridge, out, cancel := nativePanelTestBridge(t, mux)
	defer cancel()
	lines := `{"kind":"panel-request","id":"1","method":"POST","url":"oa-panel://panel/echo","headers":{"X-Panel-Key":"k","Content-Type":"application/json"},"body":"` +
		base64.StdEncoding.EncodeToString([]byte(`{"a":"wörld"}`)) + `"}` + "\n" +
		`{"kind":"panel-request","id":"2","method":"GET","url":"oa-panel://panel/large"}` + "\n"
	if err := bridge.read(strings.NewReader(lines), nil); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	waitFor(t, "both answers", func() bool {
		_, _, _, a := assembled(t, out.frames(t), "1")
		_, _, _, b := assembled(t, out.frames(t), "2")
		return a && b
	})
	frames := out.frames(t)
	status, headers, body, _ := assembled(t, frames, "1")
	if status != 200 || headers["Content-Type"] != "application/json" || string(body) != `{"ok":true}` || seen != `POST k application/json {"a":"wörld"}` {
		t.Fatalf("echo: %d %v %q; route saw %q", status, headers, body, seen)
	}
	status, _, body, _ = assembled(t, frames, "2")
	chunks := 0
	for _, f := range frames {
		if f.ID == "2" {
			chunks++
		}
	}
	if status != 200 || !bytes.Equal(body, large) || chunks != 3 {
		t.Fatalf("large: %d, %d bytes in %d chunks", status, len(body), chunks)
	}
}

// panel-cancel ends a waiting route and sends no answer for it.
func TestNativePanelCancelEndsLongPoll(t *testing.T) {
	ended := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/wait", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(ended)
	})
	bridge, out, cancel := nativePanelTestBridge(t, mux)
	defer cancel()
	in, feed := io.Pipe()
	go func() { _ = bridge.read(in, nil) }()
	_, _ = io.WriteString(feed, `{"kind":"panel-request","id":"7","method":"GET","url":"oa-panel://panel/wait"}`+"\n")
	time.Sleep(50 * time.Millisecond)
	_, _ = io.WriteString(feed, `{"kind":"panel-cancel","id":"7"}`+"\n")
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not end the route")
	}
	time.Sleep(50 * time.Millisecond)
	_ = feed.Close()
	if frames := out.frames(t); len(frames) != 0 {
		t.Fatalf("an abandoned request was answered: %+v", frames)
	}
}

// Before the window opened a request is refused and the channel stays; a
// malformed request or a dialog reply carrying request fields ends it.
func TestNativePanelRequestValidation(t *testing.T) {
	out := &syncBuffer{}
	bridge := newNativeAsksBridge(context.Background(), out)
	if err := bridge.read(strings.NewReader(`{"kind":"panel-request","id":"1","method":"GET","url":"oa-panel://panel/"}`+"\n"), nil); !errors.Is(err, io.EOF) {
		t.Fatalf("unopened Panel: %v", err)
	}
	if status, _, _, done := assembled(t, out.frames(t), "1"); status != http.StatusServiceUnavailable || !done {
		t.Fatalf("unopened Panel answered %d", status)
	}
	bridge, _, cancel := nativePanelTestBridge(t, http.NewServeMux())
	defer cancel()
	for _, line := range []string{
		`{"kind":"panel-request","method":"GET","url":"oa-panel://panel/"}`,
		`{"kind":"panel-request","id":"1","method":"TRACE","url":"oa-panel://panel/"}`,
		`{"kind":"panel-request","id":"1","method":"GET"}`,
		`{"kind":"panel-request","id":"1","method":"GET","url":"oa-panel://panel/","headers":{"Cookie":"a=b"}}`,
		`{"kind":"panel-request","id":"1","method":"GET","url":"oa-panel://panel/","option":"allow"}`,
		`{"kind":"panel-cancel","id":"1","url":"oa-panel://panel/"}`,
		`{"kind":"answer","id":"1","option":"allow","url":"oa-panel://panel/"}`,
		`{"kind":"open-panel","page":"overview","method":"GET"}`,
	} {
		if err := bridge.read(strings.NewReader(line+"\n"), func(string) error { return nil }); err == nil || errors.Is(err, io.EOF) {
			t.Errorf("accepted %s", line)
		}
	}
}

// The launcher's window origin is the only one its requests may name.
func TestNativePanelRefusesOtherOrigins(t *testing.T) {
	reached := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { reached = true })
	bridge, out, cancel := nativePanelTestBridge(t, mux)
	defer cancel()
	if err := bridge.read(strings.NewReader(`{"kind":"panel-request","id":"1","method":"GET","url":"http://127.0.0.1:8734/"}`+"\n"), nil); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	waitFor(t, "the refusal", func() bool { _, _, _, done := assembled(t, out.frames(t), "1"); return done })
	if status, _, _, _ := assembled(t, out.frames(t), "1"); status != http.StatusForbidden || reached {
		t.Fatalf("status %d, reached %v", status, reached)
	}
}
