package gateway

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

// routerQuestionRefusal is the runtime rights decision of a fresh program's
// first router read: not granted, with first-use question q1 raised.
type routerQuestionRefusal struct{}

func (routerQuestionRefusal) Error() string { return "not granted" }
func (routerQuestionRefusal) PermissionRefusalReason() string {
	return "rights:not_granted rights:question_pending:q1"
}

// allowingBackend answers Allow for the person while the call is held.
type allowingBackend struct {
	*OA
	allow *atomic.Bool
	waits int
	ids   []string
}

func (b *allowingBackend) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	b.waits++
	b.ids = ids
	b.allow.Store(true)
	return "allowed", ctx.Err()
}

// A fresh program's first oa_models_list raises the router's inventory
// question. The refusal names it, the call holds for the person, and after
// Allow the same first call returns the models (RIGHTS-FLOW rulings 1, 2).
func TestFirstModelsListWaitsForAllowAndSucceeds(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its existing Darwin ceiling")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Bool
	var decisions atomic.Int64
	cfg := fixture.NewConfig("permission-models")
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.Upstream = "http://127.0.0.1:1"
	cfg.RouterPolicy = func(context.Context, *identity.Peer, string, string) error {
		decisions.Add(1)
		if !allowed.Load() {
			return routerQuestionRefusal{}
		}
		return nil
	}
	hostRun := fixture.Start(t, cfg)
	defer hostRun.Stop(t)
	oa, err := NewOA(facade.New(cfg.RuntimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &allowingBackend{OA: oa, allow: &allowed}
	_, session := connected(t, b, "permission-models", "2025-11-25")
	out, models := call[ModelsOutput](t, session, ToolModels, map[string]any{})
	if out.IsError || out.Meta[permissionMetaKey] != nil || models.ObservedUnixMS == 0 {
		t.Fatal("first models list did not succeed after Allow", out.Content, models)
	}
	if b.waits != 1 || len(b.ids) != 1 || b.ids[0] != "q1" || decisions.Load() != 2 {
		t.Fatal("the refusal did not name its question or the call was not retried once", b.waits, b.ids, decisions.Load())
	}
}
