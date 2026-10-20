package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

// A route that panics answers 500 with one log line, and the process goes on
// to answer the next request.
func TestPanelResourcePanicAnswers500(t *testing.T) {
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previous)
	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("half a body"))
		panic("route failure")
	})
	mux.HandleFunc("/fine", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("fine")) })
	resources := newPanelResources(testResourceOrigin, mux)
	got := resources.serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/boom"})
	if got.Status != http.StatusInternalServerError || got.Aborted || strings.Contains(string(got.Body), "half a body") {
		t.Fatalf("panic answered %d aborted=%v %q", got.Status, got.Aborted, got.Body)
	}
	if lines := strings.Count(logged.String(), "\n"); lines != 1 || !strings.Contains(logged.String(), "/boom") {
		t.Fatalf("log %q", logged.String())
	}
	if next := resources.serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/fine"}); next.Status != 200 || string(next.Body) != "fine" {
		t.Fatalf("after a panic: %d %q", next.Status, next.Body)
	}
}

// http.ErrAbortHandler reports the request aborted, with no status or body.
func TestPanelResourceAbortHandlerReportsAborted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/abort", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("half a body"))
		panic(http.ErrAbortHandler)
	})
	got := newPanelResources(testResourceOrigin, mux).serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/abort"})
	if !got.Aborted || got.Status != 0 || len(got.Body) != 0 {
		t.Fatalf("abort answered %+v", got)
	}
}

// On macOS an aborted request is one chunk-0 frame carrying aborted and no
// body; a panicking route is a 500.
func TestNativePanelAbortAndPanicFrames(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/abort", func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) })
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) { panic("route failure") })
	previous := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previous)
	bridge, out, cancel := nativePanelTestBridge(t, mux)
	defer cancel()
	lines := `{"kind":"panel-request","id":"a","method":"GET","url":"oa-panel://panel/abort"}` + "\n" +
		`{"kind":"panel-request","id":"b","method":"GET","url":"oa-panel://panel/boom"}` + "\n"
	if err := bridge.read(strings.NewReader(lines), nil); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	waitFor(t, "both answers", func() bool {
		frames := out.frames(t)
		_, _, _, b := assembled(t, frames, "b")
		for _, f := range frames {
			if f.ID == "a" {
				return b
			}
		}
		return false
	})
	for _, f := range out.frames(t) {
		if f.ID == "a" && (!f.Aborted || f.Seq != 0 || f.Status != 0 || f.Body != "" || f.More) {
			t.Fatalf("abort frame %+v", f)
		}
	}
	if status, _, _, _ := assembled(t, out.frames(t), "b"); status != http.StatusInternalServerError {
		t.Fatalf("panic answered %d", status)
	}
}

// An answer over 64 MiB is refused with the one sentence, and the recorder
// never holds more than the cap while the route writes 512 MiB.
func TestPanelResourceCapsTheAnswer(t *testing.T) {
	chunk := make([]byte, 1<<20)
	var refusedAt int
	mux := http.NewServeMux()
	mux.HandleFunc("/huge", func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 512; i++ {
			if _, err := w.Write(chunk); err != nil {
				if refusedAt == 0 {
					refusedAt = i
				}
			}
		}
	})
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := newPanelResources(testResourceOrigin, mux).serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/huge"})
	runtime.ReadMemStats(&after)
	if got.Status != http.StatusInternalServerError || string(got.Body) != panelResourceTooLarge+"\n" {
		t.Fatalf("over the cap: %d %q", got.Status, got.Body)
	}
	if !strings.Contains(panelResourceTooLarge, "openabstractions jobs result <id>") {
		t.Fatalf("the sentence names no command: %q", panelResourceTooLarge)
	}
	if refusedAt != 64 {
		t.Fatalf("first refused write was chunk %d, want 64", refusedAt)
	}
	// A buffer grown to 64 MiB allocates under 256 MiB in all; holding the
	// 512 MiB the route wrote would need more.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 256<<20 {
		t.Fatalf("serving allocated %d MiB", allocated>>20)
	}
	// Exactly the cap still answers.
	mux.HandleFunc("/cap", func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 64; i++ {
			_, _ = w.Write(chunk)
		}
	})
	if got := newPanelResources(testResourceOrigin, mux).serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/cap"}); got.Status != 200 || len(got.Body) != maxPanelResourceResponse {
		t.Fatalf("at the cap: %d, %d bytes", got.Status, len(got.Body))
	}
}

// A route that writes past the cap and then aborts, as a result download
// does on the refused write, gets the cap's sentence rather than "aborted".
func TestPanelResourceOverflowThenAbortGetsTheSentence(t *testing.T) {
	chunk := make([]byte, 1<<20)
	mux := http.NewServeMux()
	mux.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		for {
			if _, err := w.Write(chunk); err != nil {
				panic(http.ErrAbortHandler)
			}
		}
	})
	got := newPanelResources(testResourceOrigin, mux).serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/result"})
	if got.Aborted || got.Status != http.StatusInternalServerError || string(got.Body) != panelResourceTooLarge+"\n" {
		t.Fatalf("overflow then abort answered aborted=%v %d %q", got.Aborted, got.Status, got.Body)
	}
}

// The macOS frames for a capped answer still each fit the channel.
func TestNativePanelCappedAnswerFrames(t *testing.T) {
	out := &syncBuffer{}
	server := &nativePanelServer{write: newNativeAsksBridge(context.Background(), out).write}
	server.respond("x", panelResourceResponse{Status: 200, Body: make([]byte, maxPanelResourceResponse)})
	frames := out.frames(t)
	_, _, body, done := assembled(t, frames, "x")
	if !done || len(body) != maxPanelResourceResponse || len(frames) != maxPanelResourceResponse/nativePanelChunk {
		t.Fatalf("%d frames, %d bytes, done %v", len(frames), len(body), done)
	}
}
