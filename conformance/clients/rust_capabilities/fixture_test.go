package rustcapabilities_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
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
	logservice "github.com/openabstractions/abstraction-logging/go/service"
	model "github.com/openabstractions/abstraction-model/go"
	modelservice "github.com/openabstractions/abstraction-model/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
	storage "github.com/openabstractions/abstraction-storage/go"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
)

type fixtureRegistry struct{}

func (fixtureRegistry) Registry() string { return "fixture" }

// The private repository needs a provider-owned sink the portable request cannot express.
func (fixtureRegistry) Resolve(ctx context.Context, ref model.Ref) (download.Spec, error) {
	spec := download.Spec{Artifact: download.Artifact{Digest: "sha256:" + strings.Repeat("c", 64), Size: 9},
		Sources: []download.Source{{Scheme: "http", Locator: "http://127.0.0.1/rust-weights"}}}
	if ref.Repo == "private" {
		spec.Sink.Final = "provider-private-result"
	}
	return spec, ctx.Err()
}

// TestInstalledRustStorageChangesAndRouter drives the outside Rust consumer
// against a private runtime whose storage read, change-observation and router
// policies follow a mode file the consumer writes.
func TestInstalledRustStorageChangesAndRouter(t *testing.T) {
	probe := os.Getenv("OA_RUST_CAPABILITIES_PROBE")
	if probe == "" {
		t.Fatal("installed probe required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(name, home)
	}
	t.Setenv("ProgramData", filepath.Join(home, "machine"))
	dir := t.TempDir()
	// The consumer writes the policy mode; an absent file permits.
	policyFile := filepath.Join(dir, "policy-mode")
	mode := func() string { b, _ := os.ReadFile(policyFile); return strings.TrimSpace(string(b)) }
	content, err := storage.NewContentStore("rust-fixture", filepath.Join(dir, "content"))
	if err != nil {
		t.Fatal(err)
	}
	sink, err := logging.OpenFileSink(filepath.Join(dir, "records"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	rightsPolicyPath := filepath.Join(dir, "rights-policy.json")
	rightsPolicy, err := rights.LoadDecisionPolicy(rightsPolicyPath, []string{"fixture.read"})
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	subjectProgram, err := fixture.Executable(probe)
	if err != nil {
		t.Fatal(err)
	}
	// The fake model hosts every router proof reads: a resident Lemonade model,
	// cold LM Studio models and an unreachable Ollama host.
	lemonade, studio, closeHosts := fixture.ModelHosts(nil)
	defer closeHosts()
	liveRouter := router.New(router.Lemonade(lemonade), router.LMStudio(studio), router.Ollama(fixture.UnreachableOllama))
	liveRouter.Survey()
	prefix := fmt.Sprintf("rcp-%d", time.Now().UnixNano()%1_000_000_000)
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		Sink: sink,
		LogHistoryPolicy: func(ctx context.Context, p *identity.Peer) error {
			switch mode() {
			case "history-forbidden":
				return errors.New("fixture history refused")
			case "history-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", logservice.ErrHistoryPolicyUnavailable)
			}
			return ctx.Err()
		},
		Storage: content, StorageEndpoint: listen.Endpoint(prefix + "s"),
		StoragePolicy: func(ctx context.Context, p *identity.Peer, digest string) error {
			if mode() == "storage-unreadable" {
				return errors.New("fixture read refused")
			}
			return ctx.Err()
		},
		StorageWritePolicy:     func(ctx context.Context, p *identity.Peer, digest string) error { return ctx.Err() },
		StorageWriteLimit:      1 << 20,
		StorageWriteRecordPath: filepath.Join(dir, "writer-records.json"),
		StorageChangesPolicy: func(ctx context.Context, p *identity.Peer, resource string) error {
			switch mode() {
			case "changes-forbidden":
				return errors.New("fixture observation refused")
			case "changes-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", storageservice.ErrPolicyUnavailable)
			}
			return ctx.Err()
		},
		StorageChangesInterval: 25 * time.Millisecond, StorageChangesCapacity: 4,
		ModelRegistry: model.NewServiceRegistry(fixtureRegistry{}), ModelEndpoint: listen.Endpoint(prefix + "m"),
		ModelPolicy: func(ctx context.Context, p *identity.Peer, registry string) error {
			switch mode() {
			case "model-forbidden":
				return errors.New("fixture lookup refused")
			case "model-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", modelservice.ErrPolicyUnavailable)
			}
			return ctx.Err()
		},
		RightsPolicy: rightsPolicy, RightsEndpoint: listen.Endpoint(prefix + "a"),
		// Only the probe administers policy; the mode file names outages.
		RightsOperator: func(ctx context.Context, p *identity.Peer) error {
			switch mode() {
			case "operator-forbidden":
				return rightsservice.ErrOperatorForbidden
			case "operator-unavailable":
				return errors.New("fixture operator decision unavailable")
			}
			path, err := p.Path.AtLeast(listen.Program.Path)
			if err != nil || !fixture.SameExecutable(path, probe) {
				return rightsservice.ErrOperatorForbidden
			}
			return ctx.Err()
		},
		RightsEnforcer: func(ctx context.Context, p *identity.Peer, action, resource string) bool {
			path, err := p.Path.AtLeast(listen.Program.Path)
			return err == nil && fixture.SameExecutable(path, probe) && action == "fixture.read" && ctx.Err() == nil
		},
		Router: liveRouter, RouterEndpoint: listen.Endpoint(prefix + "r"),
		RouterPolicy: func(ctx context.Context, p *identity.Peer, action, resource string) error {
			switch mode() {
			case "router-forbidden":
				return errors.New("fixture route refused")
			case "router-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", routerservice.ErrPolicyUnavailable)
			}
			return ctx.Err()
		}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
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
	cmd := exec.CommandContext(ctx, probe, o.Endpoint, "all", policyFile)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "OA_RIGHTS_ACCOUNT="+account.Uid, "OA_RIGHTS_PROGRAM="+subjectProgram, "OA_RIGHTS_POLICY_FILE="+rightsPolicyPath)
	out, err := fixture.Output(ctx, cmd)
	t.Log(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) > 1 {
		t.Fatalf("clients created provider files in the isolated home: %v", err)
	}
}
