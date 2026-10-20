package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

type permissionJobPolicyError struct{}

func (permissionJobPolicyError) Error() string { return "missing permission" }
func (permissionJobPolicyError) PermissionRefusalReason() string {
	return "rights:not_granted rights:question_pending:q1"
}

type permissionJobBackend struct {
	*OA
	allow       *atomic.Bool
	waits       int
	before      handleRecord
	handle      string
	prepares    *atomic.Int64
	loseMapping bool
}

func (b *permissionJobBackend) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	b.waits++
	if b.prepares.Load() != 0 {
		return "", errors.New("permission refusal prepared an effect")
	}
	mapping, err := loadRequest(b.requestPath("permission-once"))
	if err != nil {
		return "", err
	}
	b.handle = mapping.Handle
	b.before, err = b.load(mapping.Handle)
	if err != nil {
		return "", err
	}
	if b.loseMapping {
		if err := os.Remove(b.requestPath("permission-once")); err != nil {
			return "", err
		}
	}
	b.allow.Store(true)
	return "allowed", ctx.Err()
}

// waitingJobBackend leaves the person's question open for the whole call, so
// the tool returns awaiting_permission; the person answers afterwards.
type waitingJobBackend struct {
	*OA
	waits int
}

func (b *waitingJobBackend) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	b.waits++
	return "waiting", ctx.Err()
}

// The agent repeats a submit that returned awaiting_permission, with the same
// request key, after the person allowed. The repeat is admitted under the
// same handle and identity, and the work runs once (JOB-A16).
func TestRepeatedJobSubmitAfterAwaitingPermissionIsAccepted(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its existing Darwin ceiling")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var allowed, enabled atomic.Bool
	var prepares atomic.Int64
	cfg := fixture.NewConfig("permission-repeat")
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.Upstream = "http://127.0.0.1:1"
	cfg.Prepares = &prepares
	cfg.ExecutorEnabled = &enabled
	cfg.JobMethodPolicy = func(_ context.Context, _ *identity.Peer, _ string, method string) error {
		if method == "Submit" && !allowed.Load() {
			return permissionJobPolicyError{}
		}
		return nil
	}
	hostRun := fixture.Start(t, cfg)
	defer hostRun.Stop(t)
	oa, err := NewOA(facade.New(cfg.RuntimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &waitingJobBackend{OA: oa}
	_, session := connected(t, b, "permission-repeat", "2025-11-25")
	args := map[string]any{"request_key": "repeat-after-allow", "profile": "video", "model": "fixture/video", "prompt": "draw", "hosting": "local", "duration_ms": 1000}
	_, pending := call[map[string]any](t, session, ToolJobSubmit, args)
	if pending["outcome"] != "awaiting_permission" || b.waits != 1 || prepares.Load() != 0 {
		t.Fatal("first submit did not wait for the person", pending, b.waits, prepares.Load())
	}
	mapping, err := loadRequest(oa.requestPath("repeat-after-allow"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := oa.load(mapping.Handle)
	if err != nil {
		t.Fatal(err)
	}
	allowed.Store(true) // The person answers Allow after the call returned.
	_, result := call[JobSubmitOutput](t, session, ToolJobSubmit, args)
	if result.Outcome != "accepted" || result.Handle != mapping.Handle || result.OperationID == "" || b.waits != 1 || prepares.Load() == 0 {
		t.Fatal("repeated submit after Allow was not admitted", result, b.waits, prepares.Load())
	}
	after, err := oa.load(result.Handle)
	if err != nil || !reflect.DeepEqual(after.Identity, before.Identity) {
		t.Fatal("repeated submit rotated identity", after, before, err)
	}
	_, again := call[JobSubmitOutput](t, session, ToolJobSubmit, args)
	if again.Outcome != "accepted" || again.OperationID != result.OperationID || again.Handle != result.Handle {
		t.Fatal("a third submit did not return the same job", again, result)
	}
	entries, err := os.ReadDir(filepath.Join(cfg.JobRoot, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			jobs++
		}
	}
	if jobs != 1 {
		t.Fatal("the effect ran more than once", jobs)
	}
}

func TestPermissionMiddlewareJobAllowRetainsHandleIdentityAndOneEffect(t *testing.T) {
	testPermissionJobContinuation(t, false)
}

func TestPermissionMiddlewareJobLostMappingDoesNotRotateIdentity(t *testing.T) {
	testPermissionJobContinuation(t, true)
}

func testPermissionJobContinuation(t *testing.T, loseMapping bool) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its existing Darwin ceiling")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var allowed, enabled atomic.Bool
	var prepares atomic.Int64
	cfg := fixture.NewConfig("permission-job")
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.Upstream = "http://127.0.0.1:1"
	cfg.Prepares = &prepares
	cfg.ExecutorEnabled = &enabled
	cfg.JobMethodPolicy = func(_ context.Context, _ *identity.Peer, _ string, method string) error {
		if method == "Submit" && !allowed.Load() {
			return permissionJobPolicyError{}
		}
		return nil
	}
	hostRun := fixture.Start(t, cfg)
	defer hostRun.Stop(t)
	oa, err := NewOA(facade.New(cfg.RuntimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &permissionJobBackend{OA: oa, allow: &allowed, prepares: &prepares, loseMapping: loseMapping}
	_, session := connected(t, b, "permission-job", "2025-11-25")
	args := map[string]any{"request_key": "permission-once", "profile": "video", "model": "fixture/video", "prompt": "draw", "hosting": "local", "duration_ms": 1000}
	out, result := call[JobSubmitOutput](t, session, ToolJobSubmit, args)
	if loseMapping {
		if !out.IsError || prepares.Load() != 0 || b.waits != 1 {
			t.Fatal("missing mapping created a fresh effect", out, b.waits, prepares.Load())
		}
		return
	}
	if out.IsError {
		t.Fatal(out)
	}
	prepared := prepares.Load()
	entries, err := os.ReadDir(filepath.Join(cfg.JobRoot, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			jobs++
		}
	}
	if result.Outcome != "accepted" || result.Handle != b.handle || result.OperationID == "" || b.waits != 1 || prepared == 0 || jobs != 1 {
		t.Fatal(result, b.waits, prepares.Load())
	}
	after, err := oa.load(result.Handle)
	if err != nil || !reflect.DeepEqual(after.Identity, b.before.Identity) {
		t.Fatal("permission retry rotated identity", after, b.before, err)
	}
	_, again := call[JobSubmitOutput](t, session, ToolJobSubmit, args)
	if again.Outcome != "accepted" || again.OperationID != result.OperationID || again.Handle != result.Handle || b.waits != 1 {
		t.Fatal("ordinary reconciliation repeated effect", again, b.waits, prepares.Load())
	}
}
