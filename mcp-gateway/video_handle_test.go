package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// R35 owner machine, 2026-11-12: oa_video_generate {prompt, model: ltx-2.5}
// answered working with a handle, and every call with that handle answered
// "handle goes alone": the SDK filled the schema default preset quick into
// the continuation. A call without model answered "Name a video model":
// the agent connection's backend did not forward the video model read.
// Through the shipped server and the agent connection's backend, on a
// runtime whose router serves LTX-2.5 for video and with no video policy
// saved: a call without model starts LTX-2.5; the handle continues alone,
// with wait_ms and with the default preset; a prompt beside it is refused.
func TestVideoHandleContinuesAndTheOneServedVideoModelIsTheDefault(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its existing Darwin ceiling")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	declared, err := router.NewNative("modelhost", "unused", listen.ServerExpectation{},
		[]string{"video:ltx-2.5", "chat:qwen3.6-35b-a3b"}, []string{router.ProfileChat, router.ProfileVideo})
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixture.NewConfig("video-handle")
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.Upstream = "http://127.0.0.1:1"
	cfg.RouteJobs = true
	cfg.Hosts = []*router.Host{declared}
	run := fixture.Start(t, cfg)
	t.Cleanup(func() { run.Stop(t) })
	oa, err := gateway.NewOA(facade.New(cfg.RuntimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &messagesBackend{Backend: oa, application: "oa-claude-integration", instance: "video", contextName: "video-context", status: new(messagesInboxStatus)}
	server, err := newGatewayServer(backend, false)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(listed.Tools, func(tool *mcp.Tool) bool { return tool.Name == gateway.ToolVideoGenerate })
	if index < 0 {
		t.Fatal("oa_video_generate is not listed while LTX-2.5 is the one video model served")
	}
	schema, _ := json.Marshal(listed.Tools[index].InputSchema)
	if strings.Contains(string(schema), `"default"`) {
		t.Fatalf("the video schema carries a default a client fills in: %s", schema)
	}

	video := func(args map[string]any) gateway.VideoOutput {
		t.Helper()
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: gateway.ToolVideoGenerate, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var out gateway.VideoOutput
		raw, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	started := video(map[string]any{"prompt": "rain on a tin roof", "wait_ms": 500})
	if started.Handle == "" || started.Outcome == "invalid" || started.Outcome == "not_set_up" {
		t.Fatalf("a video without model did not start LTX-2.5: %+v", started)
	}
	for _, args := range []map[string]any{
		{"handle": started.Handle},
		{"handle": started.Handle, "wait_ms": 500},
		{"handle": started.Handle, "preset": "quick", "wait_ms": 500},
	} {
		if out := video(args); out.Outcome == "invalid" {
			t.Fatalf("%v: %+v", args, out)
		}
	}
	refused := video(map[string]any{"handle": started.Handle, "prompt": "snow on a field"})
	if refused.Outcome != "invalid" || refused.Field != "prompt" || !strings.Contains(refused.Message, "leave out handle") {
		t.Fatalf("a new prompt beside the handle: %+v", refused)
	}
}

// The picture and video tools read their backend's optional methods by type
// assertion; each reaches the runtime backend through the agent connection.
func TestMessagesBackendForwardsThePictureAndVideoReads(t *testing.T) {
	have := reflect.TypeFor[*messagesBackend]()
	for _, name := range []string{"GenerateImage", "ServesPictures", "VideoModel", "JobsFollowWork", "ModelsConnect"} {
		if _, ok := reflect.TypeFor[*gateway.OA]().MethodByName(name); !ok {
			t.Fatalf("the runtime backend has no %s", name)
		}
		if _, ok := have.MethodByName(name); !ok {
			t.Errorf("messagesBackend does not forward %s", name)
		}
	}
}
