package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
)

func TestPanelOwnedFramesOpaqueRoundTrip(t *testing.T) {
	in, writer := io.Pipe()
	var out bytes.Buffer
	p := newPanelOwnedFrames(in, &out, nil)
	go p.read()
	t.Cleanup(func() { _ = p.close(); _ = writer.Close(); <-p.done })
	body := []byte(`{"opaque":"shared SDK owns syntax"}`)
	go func() { _ = listen.WriteFrameTo(writer, body, panelOwnedFrameLimit) }()
	routes := p.callbacks("fixture", http.NotFoundHandler())
	r := httptest.NewRequest(http.MethodGet, "https://panel.invalid/native-host/read?k=fixture", nil)
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("read %d %q", w.Code, w.Body.Bytes())
	}
	r = httptest.NewRequest(http.MethodPost, "https://panel.invalid/native-host/write?k=fixture", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/octet-stream")
	w = httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("write %d", w.Code)
	}
	got, err := listen.ReadFrameFrom(&out, panelOwnedFrameLimit)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("frame %q %v", got, err)
	}
}

func TestPanelOwnedFramesBoundsAndRetirement(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, contentType, body string
		want                                  int
	}{
		{"key", "GET", "/native-host/read", "", "", 403},
		{"method", "POST", "/native-host/read?k=fixture", "", "", 405},
		{"type", "POST", "/native-host/write?k=fixture", "text/plain", "x", 415},
		{"large", "POST", "/native-host/write?k=fixture", "application/octet-stream", strings.Repeat("x", panelOwnedFrameLimit+1), 413},
		{"other", "GET", "/elsewhere?k=fixture", "", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPanelOwnedFrames(io.NopCloser(bytes.NewReader(nil)), io.Discard, nil)
			defer p.close()
			r := httptest.NewRequest(tc.method, "https://panel.invalid"+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			p.callbacks("fixture", http.NotFoundHandler()).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
	retired := 0
	p := newPanelOwnedFrames(io.NopCloser(bytes.NewReader(nil)), io.Discard, func() { retired++ })
	p.read()
	p.retire()
	if retired != 1 || p.ctx.Err() == nil {
		t.Fatalf("retired %d", retired)
	}
	if err := p.write([]byte("late")); err == nil {
		t.Fatal("write after EOF")
	}
	w := httptest.NewRecorder()
	p.callbacks("fixture", http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", "https://panel.invalid/native-host/read?k=fixture", nil))
	if w.Code != 410 {
		t.Fatalf("EOF returned %d", w.Code)
	}
}

func TestPanelOwnedFramesQueueAndCancelledPoll(t *testing.T) {
	var wire bytes.Buffer
	for n := 0; n < panelOwnedFrameQueue+1; n++ {
		if err := listen.WriteFrameTo(&wire, []byte("x"), panelOwnedFrameLimit); err != nil {
			t.Fatal(err)
		}
	}
	p := newPanelOwnedFrames(io.NopCloser(&wire), io.Discard, nil)
	p.read()
	if p.ctx.Err() == nil || len(p.frames) != panelOwnedFrameQueue {
		t.Fatal("queue did not fail closed")
	}
	p = newPanelOwnedFrames(io.NopCloser(bytes.NewReader(nil)), io.Discard, nil)
	defer p.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "https://panel.invalid/native-host/read?k=fixture", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	start := time.Now()
	p.callbacks("fixture", http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 408 || time.Since(start) > time.Second {
		t.Fatalf("cancel %d", w.Code)
	}
}
