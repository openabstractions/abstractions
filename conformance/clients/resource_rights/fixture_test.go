package resourcerights_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	download "github.com/openabstractions/abstraction-download/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	model "github.com/openabstractions/abstraction-model/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rc "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
)

type registry struct{}

func (registry) Registry() string { return "fixture" }
func (registry) Resolve(ctx context.Context, ref model.Ref) (download.Spec, error) {
	return download.Spec{Artifact: download.Artifact{Digest: "sha256:" + strings.Repeat("b", 64), Size: 7},
		Sources: []download.Source{{Scheme: "http", Locator: "http://127.0.0.1/weights"}}}, ctx.Err()
}

// TestInstalledResourceRights runs the installed aggregate C++ consumer against a
// runtime whose history, model and router services enforce the rights service.
func TestInstalledResourceRights(t *testing.T) {
	probe := os.Getenv("OA_CPP_RESOURCE_RIGHTS_PROBE")
	if probe == "" {
		t.Fatal("installed probe required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-resource-rights-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	actions := []string{host.LogHistoryAction, host.ModelLookupAction, routerservice.ActionInventory, routerservice.ActionRoute}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "rights.json"), actions)
	if err != nil {
		t.Fatal(err)
	}
	// The rights service runs as its own host so the fixture can stop it for the outage case.
	rightsEndpoint := endpoint("rights")
	decider, err := rightsservice.Listen(rightsEndpoint, policy, func(ctx context.Context, p *identity.Peer, a, r string) bool {
		process, e := p.Process.AtLeast(listen.Program.Process)
		return e == nil && process.PID == os.Getpid() && ctx.Err() == nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rightsDone := make(chan error, 1)
	go func() { rightsDone <- decider.Serve(ctx) }()
	defer func() { decider.Close(); <-rightsDone }()

	sink, err := logging.OpenFileSink(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	decisions := rc.New(rightsEndpoint)
	subjects := make(chan rc.Subject, 16)
	history := host.HistoryPolicyFromRights(decisions)
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), Sink: sink,
		LogHistoryPolicy: func(ctx context.Context, p *identity.Peer) error {
			if s, e := rc.SubjectFromPeer(p); e == nil {
				select {
				case subjects <- s:
				default:
				}
			}
			return history(ctx, p)
		},
		ModelRegistry: model.NewServiceRegistry(registry{}), ModelEndpoint: endpoint("model"), ModelPolicy: host.ModelPolicyFromRights(decisions),
		Router: router.New(), RouterEndpoint: endpoint("router"), RouterPolicy: host.RouterPolicyFromRights(decisions)}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime did not drain")
		}
	}()
	run := func(mode string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, probe, o.Endpoint, mode)
		cmd.Dir = t.TempDir()
		out, err := fixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	}
	run("denied")
	var subject rc.Subject
	select {
	case subject = <-subjects:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if filepath.Clean(subject.Program) != filepath.Clean(probe) {
		t.Fatalf("unexpected native subject %q", subject.Program)
	}
	rules := [][2]string{{host.LogHistoryAction, host.LogHistoryResource}, {host.ModelLookupAction, "fixture"}, {routerservice.ActionInventory, routerservice.ResourceInventory}, {routerservice.ActionRoute, "qwen2.5"}}
	set := func(permit bool) {
		for _, r := range rules {
			var err error
			if permit {
				err = policy.Set(subject, r[0], r[1], true)
			} else {
				err = policy.Revoke(subject, r[0], r[1])
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	set(true)
	run("granted")
	set(false)
	run("revoked")
	set(true)
	decider.Close()
	run("outage")
}
