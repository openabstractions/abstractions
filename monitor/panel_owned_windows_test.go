//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
)

// This runs in the standing monitor owner as well as the generator's exact
// inventory test, catching canonical SDK changes before native packaging.
func TestPanelOwnedResourceFreshness(t *testing.T) {
	canonicalRoot := privateInput(t, "..", "openabstractions-flat")
	count := 0
	err := fs.WalkDir(panelOwnedResources, "static/native-sdk", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(name, "static/native-sdk/")
		canonical, err := os.ReadFile(filepath.Join(canonicalRoot, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		embedded, err := panelOwnedResources.ReadFile(name)
		if err != nil {
			return err
		}
		if !bytes.Equal(canonical, embedded) {
			t.Errorf("stale canonical native SDK resource %s", rel)
		}
		count++
		return nil
	})
	if err != nil || count != 11 {
		t.Fatalf("selected native SDK closure %d: %v", count, err)
	}
	manifest, err := os.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil || !bytes.Equal(manifest, embedded) {
		t.Fatalf("native resource manifest differs: %v", err)
	}
}

func TestPanelOwnedNativeLifetime(t *testing.T) {
	original := panelCreateNativeModel
	defer func() { panelCreateNativeModel = original }()
	panelCreateNativeModel = func(instance string, manifest []byte) (panelNativeModel, error) {
		if instance != "service-issued" || !bytes.Contains(manifest, []byte(panelOwnedApplication)) {
			t.Fatal("native instance/config binding lost")
		}
		return &panelFixtureNativeModel{}, nil
	}
	for _, failWindow := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF preserves GUI", true: "window failure"}[failWindow], func(t *testing.T) {
			input, writer := io.Pipe()
			defer writer.Close()
			var output bytes.Buffer
			var withdrawn atomic.Int32
			calls := panelOwnedPresenceCalls{
				announce: func(_ context.Context, _ string, in wire.ApplicationPresence) (wire.ApplicationChange, error) {
					if in.Instance != "" || len(in.Interfaces) != 0 {
						t.Fatalf("initial lease claimed readiness %#v", in)
					}
					return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "service-issued"}, nil
				},
				withdraw: func(_ context.Context, app, instance string) error {
					if app != panelOwnedApplication || instance != "service-issued" {
						t.Errorf("foreign withdrawal %s %s", app, instance)
					}
					withdrawn.Add(1)
					return nil
				},
			}
			windowFailure := errors.New("controlled window failure")
			err := runOwnedPanelWith(input, &output, func(context.Context) (panelOwnedPresenceCalls, error) { return calls, nil }, func(run *panelRun, quiet bool) error {
				if !quiet || run.owned == nil || run.owned.instance != "service-issued" {
					t.Fatal("owned native entry missing")
				}
				if failWindow {
					return windowFailure
				}
				resources, _ := run.resources(panelResourceOriginWindows)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				get := func(path string) panelResourceResponse {
					return resources.serve(ctx, panelResourceRequest{URL: panelResourceOriginWindows + path + "?k=" + run.key})
				}
				bootstrap := get("/native-host/bootstrap")
				var decoded struct {
					Instance string `json:"instance"`
				}
				if bootstrap.Status != 200 || json.Unmarshal(bootstrap.Body, &decoded) != nil || decoded.Instance != "service-issued" {
					t.Fatalf("bootstrap %#v", bootstrap)
				}
				asset := resources.serve(ctx, panelResourceRequest{URL: panelResourceOriginWindows + "/native-host/sdk/abstraction-facade/javascript/js/abstraction/facade/applications/index.mjs"})
				if asset.Status != 200 || asset.Header.Get("Content-Type") != "text/javascript; charset=utf-8" || asset.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("native module response %#v", asset.Header)
				}
				shell := get("/native-host/shell")
				policy := shell.Header.Get("Content-Security-Policy")
				if shell.Status != 200 || strings.Contains(policy, "unsafe-inline") || !strings.Contains(policy, "frame-src 'self'") || !strings.Contains(policy, "frame-ancestors 'none'") {
					t.Fatalf("native shell policy %s", policy)
				}
				body := []byte("opaque runtime body")
				go func() { _ = listen.WriteFrameTo(writer, body, panelOwnedFrameLimit) }()
				answer := get("/native-host/read")
				var callback struct {
					ID      string
					Request string
				}
				if answer.Status != 200 || json.Unmarshal(answer.Body, &callback) != nil || callback.ID != "1" || callback.Request != string(body) {
					t.Fatalf("native read %#v", answer)
				}
				answer = resources.serve(ctx, panelResourceRequest{Method: "POST", URL: panelResourceOriginWindows + "/native-host/write?call=1&k=" + run.key, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: []byte("opaque reply")})
				if answer.Status != 204 {
					t.Fatalf("native write %d", answer.Status)
				}
				_ = writer.Close()
				select {
				case <-run.owned.frames.done:
				case <-ctx.Done():
					t.Fatal("EOF did not retire mediation")
				}
				// This callback still owns the GUI until it returns. EOF did not close it.
				if run.owned.frames.ctx.Err() == nil {
					t.Fatal("EOF remained mediated")
				}
				return nil
			})
			if failWindow {
				if !errors.Is(err, windowFailure) {
					t.Fatalf("window refusal %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				body, err := listen.ReadFrameFrom(&output, panelOwnedFrameLimit)
				if err != nil || string(body) != "opaque reply" || output.Len() != 0 {
					t.Fatalf("stdout was not exactly one frame %q %v", body, err)
				}
			}
			if withdrawn.Load() != 1 {
				t.Fatalf("withdrawals %d", withdrawn.Load())
			}
		})
	}
}

type panelFixtureNativeModel struct{}

func (*panelFixtureNativeModel) Receive(body []byte) ([]panelNativeActivation, [][]byte, error) {
	return []panelNativeActivation{{ID: "1", Request: body}}, nil, nil
}
func (*panelFixtureNativeModel) Complete(_ string, body []byte) ([]panelNativeActivation, [][]byte, error) {
	return nil, [][]byte{body}, nil
}
func (*panelFixtureNativeModel) Fault(string) ([]panelNativeActivation, [][]byte, error) {
	return nil, nil, nil
}
func (*panelFixtureNativeModel) Retire() error { return nil }
func (*panelFixtureNativeModel) Close() error  { return nil }
