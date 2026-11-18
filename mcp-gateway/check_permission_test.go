package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// inventoryQuestion is the runtime's decision on a fresh program's first
// router read: not granted, with the first-use question q1 raised.
type inventoryQuestion struct{}

func (inventoryQuestion) Error() string { return "not granted" }
func (inventoryQuestion) PermissionRefusalReason() string {
	return "rights:not_granted rights:question_pending:q1"
}

// absentPerson is the product backend while nobody answers a popup, as in a
// Sandbox guest: the wait for the person's answer lasts until the call's end.
type absentPerson struct {
	*gateway.OA
	waited chan struct{}
}

func (b *absentPerson) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	close(b.waited)
	<-ctx.Done()
	return "waiting", ctx.Err()
}

// The installed check lists models from a program nobody has allowed yet,
// in a guest where nobody answers. The 2026-11-19 signed MSI guest timed the
// models read out at the check's 17 s bound: the gateway held the call for
// the person's answer to the router's inventory question. The check now
// reads through the shipped gateway server, gets awaiting_permission at once,
// and names it with each read's time.
func TestCheckNamesAPendingModelsPermissionAtOnce(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its existing Darwin ceiling")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixture.NewConfig("check-permission")
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.Upstream = "http://127.0.0.1:1"
	cfg.RouterPolicy = func(context.Context, *identity.Peer, string, string) error { return inventoryQuestion{} }
	run := fixture.Start(t, cfg)
	defer run.Stop(t)
	oa, err := gateway.NewOA(facade.New(cfg.RuntimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &absentPerson{OA: oa, waited: make(chan struct{})}

	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Second)
	defer cancel()
	var output bytes.Buffer
	started := time.Now()
	exit := checkConnection(ctx, &output, func(context.Context) (string, error) { return cfg.RuntimeEndpoint, nil },
		func(ctx context.Context, _, _ string) (checkSession, error) {
			serverSide, clientSide := mcp.NewInMemoryTransports()
			server, err := newGatewayServer(backend, false)
			if err != nil {
				return nil, err
			}
			if _, err := server.Connect(ctx, serverSide, nil); err != nil {
				return nil, err
			}
			return checkClient(ctx, clientSide)
		}, checkTestRoot(t))
	took := time.Since(started)
	t.Logf("check took %v:\n%s", took, output.String())
	select {
	case <-backend.waited:
		t.Fatal("the check's models read waited for the person")
	default:
	}
	if exit == 0 || !strings.Contains(output.String(), "OA models: unavailable (waiting for the person's permission) after ") {
		t.Fatalf("the check did not name the pending permission (exit %d):\n%s", exit, output.String())
	}
	if !strings.Contains(output.String(), "OA applications: available (") || took > 5*time.Second {
		t.Fatalf("the check took %v:\n%s", took, output.String())
	}
}
