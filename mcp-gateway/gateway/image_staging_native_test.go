package gateway_test

import (
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
	storage "github.com/openabstractions/abstraction-storage/go"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestNativeCompletionImageStagingRefusal(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof is deliberately unavailable on Darwin")
	}
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skipf("native Program proof unavailable: %v", err)
	}

	dir := t.TempDir()
	binary := fixture.BuildGateway(t, dir, "image-staging-gateway")
	path := filepath.Join(dir, "image.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewNRGBA(image.Rect(0, 0, 544, 544))
	noise := rand.NewChaCha8([32]byte{1})
	if _, err := noise.Read(picture.Pix); err != nil {
		t.Fatal(err)
	}
	for i := 3; i < len(picture.Pix); i += 4 {
		picture.Pix[i] = 255
	}
	if err := png.Encode(file, picture); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var records atomic.Int64
	cfg := fixture.NewConfig("image-staging")
	cfg.Allowed = []string{binary}
	cfg.ChatV3 = true
	cfg.JobRoot = filepath.Join(dir, "jobs")
	cfg.Record = func(inference.Record) { records.Add(1) }
	runtime := fixture.Start(t, cfg)
	defer runtime.Stop(t)
	result, out := callFixture[gateway.CompleteOutput](t, binary, cfg.RuntimeEndpoint, filepath.Join(dir, "state"), "oa-opencode-native-plugin", gateway.ToolComplete, gateway.CompleteInput{Model: "text-only", Prompt: "Describe this image", Hosting: "local", ImagePath: path})
	if result.IsError || len(result.Content) != 1 || out.State != "ended" || out.Outcome != "unavailable" || out.Reason != "image:staging:resolve" {
		t.Fatalf("expected typed pre-Start storage failure: %+v, %+v", result, out)
	}
	body, ok := result.Content[0].(*mcp.TextContent)
	if !ok || body.Text != out.Message || !strings.Contains(out.Message, "could not stage the image") || strings.Contains(out.Message, path) {
		t.Fatalf("expected safe staging guidance, got %+v", result.Content[0])
	}
	if records.Load() != 0 {
		t.Fatal("inference started")
	}
}

func TestNativeCompletionRefusesImageForSelectedTextOnlyModel(t *testing.T) {
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skipf("native Program proof unavailable: %v", err)
	}
	dir := t.TempDir()
	binary := fixture.BuildGateway(t, dir, "image-capability-gateway")
	path := filepath.Join(dir, "image.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded := png.Encode(file, image.NewNRGBA(image.Rect(0, 0, 2, 2)))
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if encoded != nil {
		t.Fatal(encoded)
	}
	var posts atomic.Int64
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"text-fixture:1b"},{"name":"vision-fixture:2b"},{"name":"unknown-fixture:3b"}]}`)
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["completion"]}`)
		case "/api/ps":
			_, _ = io.WriteString(w, `{"models":[]}`)
		case "/v1/chat/completions":
			posts.Add(1)
			http.Error(w, "fixture has no engine", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer engine.Close()
	host := router.Ollama(engine.URL)
	host.ReportCapabilities(func(context.Context, []string) map[string]*router.ModelCapabilities {
		return map[string]*router.ModelCapabilities{"text-fixture:1b": {Input: []string{"text"}}, "vision-fixture:2b": {Input: []string{"text", "image"}}}
	})
	cfg := fixture.NewConfig("capability")
	cfg.JobRoot, cfg.ChatV3, cfg.Allowed = filepath.Join(dir, "jobs"), true, []string{binary}
	cfg.Hosts = []*router.Host{host}
	var requestedRoutes atomic.Int64
	var denyInventory, denyHost atomic.Bool
	cfg.CompletePolicy = func(_ inference.Subject, resource string) bool {
		return resource == inference.ResourceHost(host.Name) && !denyHost.Load()
	}
	cfg.RouterPolicy = func(_ context.Context, _ *identity.Peer, action, _ string) error {
		if action == routerservice.ActionRoute {
			requestedRoutes.Add(1)
			return errors.New("route unavailable to the fixture gateway")
		}
		if denyInventory.Load() {
			return errors.New("inventory unavailable to the fixture gateway")
		}
		return nil
	}
	var selectedModel atomic.Value
	cfg.Record = func(r inference.Record) {
		if r.Reason == "feature:"+inference.FeatureVision {
			selectedModel.Store(r.Model)
		}
	}
	cfg.Storage, err = storage.NewContentStore("image-fixture", filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatal(err)
	}
	rt := fixture.Start(t, cfg)
	defer rt.Stop(t)
	for _, tc := range []struct {
		name, target, outcome, reason string
		denyInventory, denyHost       bool
	}{
		{"known", "TEXT-FIXTURE:1B", "unsupported", "feature:" + inference.FeatureVision, false, false},
		{"unknown", "unknown-fixture:3b", "unavailable", "content:unavailable", false, false},
		{"without inventory", "TEXT-FIXTURE:1B", "unsupported", "feature:" + inference.FeatureVision, true, false},
		{"denied known", "TEXT-FIXTURE:1B", "not_permitted", "rights:not_granted", false, true},
		{"denied unknown", "unknown-fixture:3b", "not_permitted", "rights:not_granted", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			denyInventory.Store(tc.denyInventory)
			denyHost.Store(tc.denyHost)
			result, out := callFixture[gateway.CompleteOutput](t, binary, cfg.RuntimeEndpoint, filepath.Join(dir, "state"), "oa-opencode-native-plugin", gateway.ToolComplete, gateway.CompleteInput{Model: tc.target, Prompt: "Describe this image", Hosting: "local", ImagePath: path})
			if out.State != "ended" || out.Outcome != tc.outcome || out.Reason != tc.reason || out.OperationID != "" {
				t.Fatalf("completion: %+v, result: %+v, content: %s", out, result, nativeToolText(result))
			}
			if tc.denyHost && (out.Message != "" || out.Model != "" || out.Host != "") {
				t.Fatalf("denied host exposed model details: %+v", out)
			}
			if tc.outcome == "unsupported" {
				if !strings.Contains(out.Message, "does not accept image input") || strings.Contains(out.Message, "vision-fixture:2b") == tc.denyInventory || out.Model != "" || out.Host != "" {
					t.Fatalf("unexpected refusal guidance: %+v", out)
				}
			}
		})
	}
	if posts.Load() != 0 || requestedRoutes.Load() != 0 {
		t.Fatal("gateway picked a route or inference sent an image to an engine")
	}
	if selectedModel.Load() != "text-fixture:1b" {
		t.Fatalf("internal refusal record model: %v", selectedModel.Load())
	}
}

func nativeToolText(r *mcp.CallToolResult) string {
	if len(r.Content) > 0 {
		if body, ok := r.Content[0].(*mcp.TextContent); ok {
			return body.Text
		}
	}
	return ""
}
