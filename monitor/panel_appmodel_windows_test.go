//go:build windows

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
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/listen"
)

func TestPanelNativeABIAndActualBrowserCallback(t *testing.T) {
	library := os.Getenv("OA_PANEL_APPMODEL_TEST_LIBRARY")
	if library == "" {
		t.Skip("explicit source-owned native library required")
	}
	if unsafe.Sizeof(panelNativeSpan{}) != 16 || unsafe.Sizeof(panelNativeBuffer{}) != 24 || unsafe.Sizeof(panelNativeEvent{}) != 40 {
		t.Fatal("native ABI layout differs")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"wrong-type":         []byte(`[]`),
		"malformed":          []byte(`{"version":`),
		"other-owned-record": applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied}),
	} {
		t.Run(name, func(t *testing.T) {
			model, err := openPanelNativeModel(library, "fixture-original-instance", body)
			var refusal *registration.Refusal
			if model != nil || !errors.As(err, &refusal) {
				t.Fatalf("shared Manifest decoder admitted invalid input: model=%v err=%v", model, err)
			}
		})
	}
	for _, mode := range []string{"ready", "hidden", "unready", "closed", "wrong-page", "wrong-origin", "callback-throw"} {
		t.Run(mode, func(t *testing.T) {
			model, err := openPanelNativeModel(library, "fixture-original-instance", manifest)
			if err != nil {
				t.Fatal(err)
			}
			input, writer := io.Pipe()
			output, reader := io.Pipe()
			frames := newPanelOwnedFrames(input, reader, nil)
			if err := frames.attachModel(model); err != nil {
				t.Fatal(err)
			}
			go frames.read()
			defer frames.close()
			defer writer.Close()
			defer output.Close()
			host := &panelOwnedWindow{frames: frames, instance: "fixture-original-instance", manifest: string(manifest)}
			host.bindVisible(func(context.Context) bool { return mode != "hidden" })
			resources := newPanelResources(panelResourceOriginWindows, host.routes("fixture-key", http.NotFoundHandler()))
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			go func() { <-ctx.Done(); _ = output.Close(); _ = writer.Close() }()
			command := exec.CommandContext(ctx, node, "--experimental-vm-modules", "testdata/native_host_receiver.mjs", "--native-bridge", mode)
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			var responseMu sync.Mutex
			var requests sync.WaitGroup
			browserDone := make(chan bool, 1)
			go func() {
				passed := false
				scanner := bufio.NewScanner(stdout)
				scanner.Buffer(make([]byte, 4096), 2<<20)
				for scanner.Scan() {
					line := scanner.Text()
					if line == "PASS actual native host browser callback" {
						passed = true
						continue
					}
					if !strings.HasPrefix(line, "panel-fixture:") {
						continue
					}
					var request struct {
						ID                int
						URL, Method, Body string
						Headers           map[string]string
					}
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "panel-fixture:")), &request) != nil {
						continue
					}
					requests.Add(1)
					go func() {
						defer requests.Done()
						body, err := base64.StdEncoding.DecodeString(request.Body)
						if err != nil {
							return
						}
						header := http.Header{}
						for name, value := range request.Headers {
							header.Set(name, value)
						}
						result := resources.serve(ctx, panelResourceRequest{Method: request.Method, URL: request.URL, Header: header, Body: body})
						responseMu.Lock()
						_ = json.NewEncoder(stdin).Encode(map[string]any{"id": request.ID, "status": result.Status, "body": base64.StdEncoding.EncodeToString(result.Body)})
						responseMu.Unlock()
					}()
				}
				browserDone <- passed && scanner.Err() == nil
			}()
			request := []byte(`{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"open","arguments":{"page":"credentials"}}}`)
			go func() { _ = listen.WriteFrameTo(writer, request, panelOwnedFrameLimit) }()
			reply, err := listen.ReadFrameFrom(output, panelOwnedFrameLimit)
			_ = writer.Close() // original runtime EOF ends mediation, not the GUI
			if err != nil {
				cancel()
				_ = command.Wait()
				<-frames.done
				t.Fatalf("owned output: %v; native reader: %v; browser: %s", err, frames.readError, stderr.String())
			}
			var rpc struct {
				ID     int
				Result struct{ Content []struct{ Text string } }
				Error  any
			}
			if json.Unmarshal(reply, &rpc) != nil || rpc.ID != 17 {
				t.Fatalf("native reply %s", reply)
			}
			if mode == "callback-throw" {
				failure, ok := rpc.Error.(map[string]any)
				if !ok || failure["code"] != float64(-32603) || len(rpc.Result.Content) != 0 {
					t.Fatalf("callback throw must be native internal fault: %s", reply)
				}
			} else {
				if rpc.Error != nil || len(rpc.Result.Content) != 1 {
					t.Fatalf("native reply %s", reply)
				}
				var result struct{ Outcome, Reason string }
				if json.Unmarshal([]byte(rpc.Result.Content[0].Text), &result) != nil {
					t.Fatalf("native canonical result %s", reply)
				}
				want := "unavailable"
				if mode == "ready" {
					want = "applied"
				}
				if result.Outcome != want || (result.Reason == "") != (mode == "ready") {
					t.Fatalf("native UI result %#v", result)
				}
			}
			passed := <-browserDone
			if err := command.Wait(); err != nil {
				t.Fatalf("browser fixture %v\n%s", err, stderr.String())
			}
			requests.Wait()
			if !passed {
				t.Fatalf("browser callback not received\n%s", stderr.String())
			}
			<-frames.done
			if err := frames.close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPanelNativeMissingRequiredABI2ExportsRefusesBeforeModel(t *testing.T) {
	library := os.Getenv("OA_PANEL_APPMODEL_OLD_TEST_LIBRARY")
	if library == "" {
		t.Skip("explicit old source-owned native library required")
	}
	// Invalid manifest distinguishes export admission from model construction.
	model, err := openPanelNativeModel(library, "fixture-original-instance", []byte("invalid manifest"))
	if model != nil || err == nil || !strings.Contains(err.Error(), "procedure") {
		t.Fatalf("missing required pointer export must refuse before parsing: model=%v error=%v", model, err)
	}
}

func TestPanelNativeFaultCompletionRetiresIssuedCall(t *testing.T) {
	library := os.Getenv("OA_PANEL_APPMODEL_TEST_LIBRARY")
	if library == "" {
		t.Skip("explicit source-owned ABI2 library required")
	}
	manifest, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		body        []byte
		fault, late bool
		status      int
	}{
		{"malformed", []byte("{"), false, false, http.StatusNoContent},
		{"empty", nil, false, false, http.StatusNoContent},
		{"applied-reason", []byte(`{"outcome":"applied","reason":"forged"}`), false, false, http.StatusNoContent},
		{"unknown-field", []byte(`{"outcome":"applied","reason":"","extra":true}`), false, false, http.StatusNoContent},
		{"oversized", bytes.Repeat([]byte("x"), panelOwnedFrameLimit+1), false, false, http.StatusRequestEntityTooLarge},
		{"explicit", nil, true, false, http.StatusNoContent},
		{"late", nil, true, true, http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, err := openPanelNativeModel(library, "fault-fixture", manifest)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			frames := newPanelOwnedFrames(io.NopCloser(bytes.NewReader(nil)), &output, nil)
			defer frames.close()
			if err := frames.attachModel(model); err != nil {
				t.Fatal(err)
			}
			if err := frames.receive([]byte(`{"jsonrpc":"2.0","id":31,"method":"tools/call","params":{"name":"open","arguments":{"page":"credentials"}}}`)); err != nil {
				t.Fatal(err)
			}
			var id string
			for key := range frames.pending {
				id = key
			}
			if id == "" || len(frames.pending) != 1 {
				t.Fatal("native issued callback required")
			}
			host := &panelOwnedWindow{frames: frames}
			resources := newPanelResources(panelResourceOriginWindows, host.routes("original-key", http.NotFoundHandler()))
			post := func(key string) panelResourceResponse {
				mode := ""
				if test.fault {
					mode = "&fault=1"
				}
				return resources.serve(context.Background(), panelResourceRequest{Method: http.MethodPost, URL: panelResourceOriginWindows + "/native-host/write?call=" + id + "&k=" + key + mode, Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: test.body})
			}
			if got := post("stale-key"); got.Status != http.StatusForbidden || len(frames.pending) != 1 {
				t.Fatal("stale key altered original callback")
			}
			if test.late {
				frames.retire()
			}
			if got := post("original-key"); got.Status != test.status {
				t.Fatalf("completion status %d", got.Status)
			}
			if len(frames.pending) != 0 || model.(*panelWindowsModel).invoke("pending", model.(*panelWindowsModel).model) != 0 {
				t.Fatal("fault left native callback pending")
			}
			if test.late {
				if output.Len() != 0 {
					t.Fatal("late fault emitted retired wire")
				}
			} else {
				body, err := listen.ReadFrameFrom(&output, panelOwnedFrameLimit)
				if err != nil {
					t.Fatal(err)
				}
				var reply struct {
					ID     int
					Error  struct{ Code int }
					Result any
				}
				if json.Unmarshal(body, &reply) != nil || reply.ID != 31 || reply.Error.Code != -32603 || reply.Result != nil {
					t.Fatalf("fault projection %s", body)
				}
			}
			if got := post("original-key"); got.Status != http.StatusGone && got.Status != http.StatusRequestEntityTooLarge {
				t.Fatalf("duplicate fault status %d", got.Status)
			}
			if output.Len() != 0 {
				t.Fatal("duplicate fault emitted a second reply")
			}
			if err := frames.close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPanelNativeActivationCatalogueBelongsToItsSession(t *testing.T) {
	library := os.Getenv("OA_PANEL_APPMODEL_TEST_LIBRARY")
	if library == "" {
		t.Skip("explicit source-owned ABI2 library required")
	}
	manifest, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	model, err := openPanelNativeModel(library, "session-fixture", manifest)
	if err != nil {
		t.Fatal(err)
	}
	native := model.(*panelWindowsModel)
	var other uintptr
	defer func() {
		if other != 0 {
			native.invoke("session_destroy", other)
		}
		_ = model.Close()
	}()
	if status := native.spans("session_create", []any{native.model}, []any{&other}, []byte("separate-carrier"), []byte("0.8.0")); status != 0 {
		t.Fatal(status)
	}
	request := []byte(`{"jsonrpc":"2.0","id":41,"method":"tools/list"}`)
	actions, replies, err := model.Receive(request)
	if err != nil || len(actions) != 0 || len(replies) != 1 {
		t.Fatalf("selected carrier: %v %d", err, len(replies))
	}
	var selected struct {
		Result struct{ Tools []struct{ Name string } }
	}
	if json.Unmarshal(replies[0], &selected) != nil {
		t.Fatal(string(replies[0]))
	}
	found := false
	for _, tool := range selected.Result.Tools {
		found = found || tool.Name == "open"
	}
	if !found {
		t.Fatal("selected carrier lost configured activation")
	}
	var responding int32
	if status := native.spans("session_receive", []any{other, native.invoke("request_id", native.model)}, []any{&responding}, request); status != 0 {
		t.Fatal(status)
	}
	// tools/list submits app_describe through the same model event pump.
	// Select the fixture's second carrier for that existing pump, then restore
	// the production carrier; no catalogue or MCP behavior is copied here.
	original := native.session
	native.session = other
	_, isolatedReplies, err := native.pump()
	native.session = original
	if err != nil || len(isolatedReplies) != 1 {
		t.Fatalf("second carrier reply: %v %d", err, len(isolatedReplies))
	}
	body := isolatedReplies[0]
	var isolated struct {
		Result struct{ Tools []struct{ Name string } }
	}
	if json.Unmarshal(body, &isolated) != nil {
		t.Fatal(string(body))
	}
	for _, tool := range isolated.Result.Tools {
		if tool.Name == "open" {
			t.Fatal("activation leaked to another carrier")
		}
	}
	if status := native.invoke("session_destroy", other); status != 0 {
		t.Fatal(status)
	}
	other = 0
	if err := model.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPanelNativeBusyCloseRetainsOriginalCallbackOwner(t *testing.T) {
	library := os.Getenv("OA_PANEL_APPMODEL_TEST_LIBRARY")
	if library == "" {
		t.Skip("explicit source-owned native library required")
	}
	manifest, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	model, err := openPanelNativeModel(library, "original-callback-owner", manifest)
	if err != nil {
		t.Fatal(err)
	}
	native := model.(*panelWindowsModel)
	actions, output, err := model.Receive([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"open","arguments":{"page":"credentials"}}}`))
	if err != nil || len(actions) != 1 || len(output) != 0 {
		t.Fatalf("native admission %v %d %d", err, len(actions), len(output))
	}
	if err := model.Close(); err == nil {
		t.Fatal("pending callback destroyed its owner")
	}
	if native.session == 0 || native.model == 0 || native.library == 0 {
		t.Fatal("BUSY discarded the original native session/model/library")
	}
	_, output, err = model.Complete(actions[0].ID, []byte(`{"outcome":"unavailable","reason":"controlled page refusal"}`))
	if err != nil || len(output) != 0 {
		t.Fatalf("original callback did not complete after retired mediation: %v %d", err, len(output))
	}
	if err := model.Close(); err != nil {
		t.Fatal(err)
	}
	if native.session != 0 || native.model != 0 || native.library != 0 {
		t.Fatal("completed native storage still retained")
	}
}

func TestPanelNativeSchemaAndOriginalWindowKeyScope(t *testing.T) {
	library := os.Getenv("OA_PANEL_APPMODEL_TEST_LIBRARY")
	if library == "" {
		t.Skip("explicit source-owned native library required")
	}
	manifest, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	model, err := openPanelNativeModel(library, "current-native-instance", manifest)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	frames := newPanelOwnedFrames(io.NopCloser(bytes.NewReader(nil)), &output, nil)
	defer frames.close()
	if err := frames.attachModel(model); err != nil {
		t.Fatal(err)
	}
	if err := frames.receive([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"open","arguments":{"page":"work"}}}`)); err != nil {
		t.Fatal(err)
	}
	refusal, err := listen.ReadFrameFrom(&output, panelOwnedFrameLimit)
	var invalid struct{ Error struct{ Code int } }
	if err != nil || json.Unmarshal(refusal, &invalid) != nil || invalid.Error.Code != -32602 || len(frames.frames) != 0 {
		t.Fatalf("native schema did not refuse before UI: %s %v", refusal, err)
	}
	payload := `{  "page" : "credentials"  }`
	if err := frames.receive([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"open","arguments":` + payload + `}}`)); err != nil {
		t.Fatal(err)
	}
	var callback struct{ ID, Request string }
	if json.Unmarshal(<-frames.frames, &callback) != nil || !strings.Contains(callback.Request, payload) {
		t.Fatal("native activation payload lost its original span")
	}
	host := &panelOwnedWindow{frames: frames, instance: "current-native-instance", manifest: string(manifest)}
	resources := newPanelResources(panelResourceOriginWindows, host.routes("new-window-key", http.NotFoundHandler()))
	post := func(key, id string) panelResourceResponse {
		return resources.serve(context.Background(), panelResourceRequest{Method: http.MethodPost, URL: panelResourceOriginWindows + "/native-host/write?call=" + url.QueryEscape(id) + "&k=" + url.QueryEscape(key), Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: []byte(`{"outcome":"applied","reason":""}`)})
	}
	if got := post("old-window-key", callback.ID); got.Status != 403 || output.Len() != 0 || frames.ctx.Err() != nil {
		t.Fatal("foreign retained window completed current callback")
	}
	if got := post("new-window-key", "ffffffffffffffff"); got.Status != 410 || output.Len() != 0 {
		t.Fatal("unknown callback ticket reached native completion")
	}
	// The original issued callback stays completable after input retirement;
	// its canonical wire output is suppressed by the native retired session.
	if got := post("new-window-key", callback.ID); got.Status != 204 || output.Len() != 0 {
		t.Fatalf("original late callback: %d", got.Status)
	}
	if err := frames.close(); err != nil {
		t.Fatal(err)
	}
}
