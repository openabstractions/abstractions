package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
)

func TestNativeOwnedChildUsesReturnedInstanceAndSharedFrames(t *testing.T) {
	var output bytes.Buffer
	bridge := newNativeAsksBridge(context.Background(), &output)
	child := newNativePanelOwnedChild(bridge)
	bridge.owned = child
	if err := bridge.accept(nativeAsksReply{Kind: "owned-bootstrap", ID: "runtime-returned-instance"}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.frames.close(); <-child.frames.done })
	if bridge.panel == nil || !strings.Contains(output.String(), "/native-host/shell?k=") {
		t.Fatal("persistent document not configured")
	}
	var startLine string
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.HasPrefix(line, "service control panel: ") {
			startLine = strings.TrimPrefix(line, "service control panel: ")
		}
	}
	start, err := url.Parse(strings.TrimSpace(startLine))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := bridge.panel.resources.serve(context.Background(), panelResourceRequest{
		Method: http.MethodGet, URL: panelResourceOriginMacOS + "/native-host/bootstrap", Header: http.Header{"X-Panel-Key": {start.Query().Get("k")}},
	})
	var bound struct {
		Instance string `json:"instance"`
	}
	if err := json.Unmarshal(bootstrap.Body, &bound); err != nil || bootstrap.Status != 200 || bound.Instance != "runtime-returned-instance" {
		t.Fatalf("bootstrap %d %s %v", bootstrap.Status, bootstrap.Body, err)
	}
	body := []byte("{\n\r\"opaque\": \"<>&\\\"é\"}")
	var input bytes.Buffer
	if err := listen.WriteFrameTo(&input, body, panelOwnedFrameLimit); err != nil {
		t.Fatal(err)
	}
	if err := bridge.accept(nativeAsksReply{Kind: "owned-input", Body: base64.StdEncoding.EncodeToString(input.Bytes())}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	forwarded := false
	for time.Now().Before(deadline) {
		bridge.outMu.Lock()
		text := output.String()
		bridge.outMu.Unlock()
		for _, line := range strings.Split(text, "\n") {
			if !strings.HasPrefix(line, nativeAsksPrefix) {
				continue
			}
			var frame nativeAsksFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, nativeAsksPrefix)), &frame) == nil && frame.Kind == "owned-core-input" {
				data, err := base64.StdEncoding.DecodeString(frame.Body)
				if err != nil || !bytes.Equal(data, body) {
					t.Fatal("signed parent input changed")
				}
				forwarded = true
			}
		}
		if forwarded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !forwarded || len(child.frames.frames) != 0 {
		t.Fatal("MCP input did not stay in signed native host")
	}
	if err := bridge.accept(nativeAsksReply{Kind: "owned-core-output", Body: base64.StdEncoding.EncodeToString(body)}, nil); err != nil {
		t.Fatal(err)
	}
	bridge.outMu.Lock()
	var framed bytes.Buffer
	for _, line := range strings.Split(output.String(), "\n") {
		if !strings.HasPrefix(line, nativeAsksPrefix) {
			continue
		}
		var frame nativeAsksFrame
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, nativeAsksPrefix)), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Kind == "owned-output" {
			data, err := base64.StdEncoding.DecodeString(frame.Body)
			if err != nil {
				t.Fatal(err)
			}
			framed.Write(data)
		}
	}
	bridge.outMu.Unlock()
	got, err := listen.ReadFrameFrom(&framed, panelOwnedFrameLimit)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("opaque output changed: %q %v", got, err)
	}
	if err := bridge.accept(nativeAsksReply{Kind: "owned-end"}, nil); err != nil {
		t.Fatal(err)
	}
	if child.frames.ctx.Err() == nil {
		t.Fatal("EOF did not retire mediation")
	}
	if bridge.panel == nil {
		t.Fatal("retirement removed user document resources")
	}
	if err := child.frames.write(body); err == nil {
		t.Fatal("write after retirement")
	}
}

func TestNativeOwnedChildRefusesRebindingAndUnconfiguredControl(t *testing.T) {
	bridge := newNativeAsksBridge(context.Background(), io.Discard)
	if err := bridge.accept(nativeAsksReply{Kind: "owned-bootstrap", ID: "forged"}, nil); err == nil {
		t.Fatal("ordinary asks accepted native bootstrap")
	}
	child := newNativePanelOwnedChild(bridge)
	bridge.owned = child
	if err := bridge.accept(nativeAsksReply{Kind: "owned-bootstrap", ID: "first"}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.frames.close(); <-child.frames.done })
	first := bridge.panel
	if err := bridge.accept(nativeAsksReply{Kind: "owned-bootstrap", ID: "different"}, nil); err != nil {
		t.Fatal(err)
	}
	if bridge.panel != first || child.frames.ctx.Err() == nil {
		t.Fatal("duplicate bootstrap replaced the document or retained mediation")
	}
	if _, err := child.Write([]byte("late")); err == nil {
		t.Fatal("retired child emitted bytes")
	}
}

func TestNativeOwnedChildBoundsBeforeSharedFraming(t *testing.T) {
	for _, body := range []string{"", "%%%", base64.StdEncoding.EncodeToString(make([]byte, nativeOwnedChunk+1))} {
		bridge := newNativeAsksBridge(context.Background(), io.Discard)
		child := newNativePanelOwnedChild(bridge)
		bridge.owned = child
		_ = bridge.accept(nativeAsksReply{Kind: "owned-input", Body: body}, nil)
		if child.frames.ctx.Err() == nil {
			t.Fatalf("invalid pre-bootstrap chunk retained channel: %d", len(body))
		}
	}
}

type nativeOwnedShortWriter struct{}

func (nativeOwnedShortWriter) Write(value []byte) (int, error) { return len(value) - 1, nil }

func TestNativeAsksShortWriteIsUncertain(t *testing.T) {
	bridge := newNativeAsksBridge(context.Background(), nativeOwnedShortWriter{})
	if err := bridge.write(nativeAsksFrame{Kind: "owned-end"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
}
