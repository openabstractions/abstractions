package main

import (
	"bytes"
	"context"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	manifestowner "github.com/openabstractions/abstraction-facade/go/manifest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPanelOwnedShellActualSharedSession(t *testing.T) {
	runPanelShellReceiver(t)
}

func TestPanelOwnedShellForwardsCallbackFault(t *testing.T) {
	runPanelShellReceiver(t, "--fault-only")
}

func runPanelShellReceiver(t *testing.T, mode ...string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the native shell receiver")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := append([]string{"--experimental-vm-modules", "testdata/native_host_receiver.mjs"}, mode...)
	output, err := exec.CommandContext(ctx, node, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("native shell receiver: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestPanelOwnedManifestRecipe(t *testing.T) {
	body, err := os.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifestowner.ParseApplicationManifest(body, manifestowner.ManifestOwners{}); err == nil {
		t.Fatal("manifest supplied its own server authority")
	}
	serverCalls := 0
	manifest, err := manifestowner.ParseApplicationManifest(body, manifestowner.ManifestOwners{ValidateServer: func(m *registrationwire.Manifest) error { serverCalls++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if serverCalls != 1 || manifest.Name != panelOwnedApplication || manifest.ScriptHost ||
		!reflect.DeepEqual(manifest.Host.Arguments, []string{"-owned-host", "-native", "-quiet", "-page", "credentials"}) ||
		len(manifest.Verbs) != 1 || manifest.Verbs[0].Name != "open" {
		t.Fatalf("unexpected native recipe %#v", manifest)
	}
	// This callback is only a controlled syntax/semantic fixture. Real receiving
	// authenticates the actual held signed PE through its normal resource owner.
}

func TestPanelOwnedResourceScopeAndVisibility(t *testing.T) {
	frames := newPanelOwnedFrames(io.NopCloser(bytes.NewReader(nil)), io.Discard, nil)
	defer frames.close()
	host := &panelOwnedWindow{frames: frames, instance: "service-issued", manifest: "fixture"}
	resources := newPanelResources(panelResourceOriginWindows, host.routes("fixture-key", http.NotFoundHandler()))
	shell := resources.serve(context.Background(), panelResourceRequest{URL: "https://panel.invalid/native-host/shell?k=fixture-key"})
	if shell.Status != http.StatusOK || !strings.Contains(string(shell.Body), `"@openabstractions/rights/internal":"/native-host/sdk/abstraction-rights/javascript/js/abstraction/rights/api/internal.mjs"`) {
		t.Fatalf("shared registration codec import map missing: status %d", shell.Status)
	}
	for _, tc := range []struct {
		url  string
		want int
	}{
		{"https://foreign.invalid/native-host/bootstrap?k=fixture-key", 403},
		{"https://panel.invalid/native-host/bootstrap?k=foreign", 403},
		{"https://panel.invalid/native-host/bootstrap?k=fixture-key", 200},
		{"https://panel.invalid/native-host/visible?k=fixture-key", 503},
	} {
		out := resources.serve(context.Background(), panelResourceRequest{URL: tc.url})
		if out.Status != tc.want {
			t.Fatalf("%s got%d want%d", tc.url, out.Status, tc.want)
		}
	}
	host.bindVisible(func(context.Context) bool { return true })
	out := resources.serve(context.Background(), panelResourceRequest{URL: "https://panel.invalid/native-host/visible?k=fixture-key"})
	if out.Status != 204 {
		t.Fatalf("visible %d", out.Status)
	}
	// Reloading the retained root cannot construct a replacement Core under the
	// same directory handle. The existing GUI remains independent of retirement.
	out = resources.serve(context.Background(), panelResourceRequest{URL: "https://panel.invalid/native-host/bootstrap?k=fixture-key"})
	if out.Status != 410 || frames.ctx.Err() == nil {
		t.Fatalf("second Core bootstrap %d", out.Status)
	}
	frames.retire()
	out = resources.serve(context.Background(), panelResourceRequest{URL: "https://panel.invalid/native-host/visible?k=fixture-key"})
	if out.Status != 503 {
		t.Fatalf("retired visible %d", out.Status)
	}
	// Normal HTTP routes never receive the native-only wrapper.
	run, err := newPanelRun("credentials")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	run.panel.routes(run.key).ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/native-host/bootstrap?k="+run.key, nil))
	if w.Code != 404 {
		t.Fatalf("ordinary HTTP exposed native callbacks: %d", w.Code)
	}
}
