package rustconfigasks_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
)

// TestInstalledRustConfigAndAsks drives the outside Rust consumer against a
// private runtime with an explicit edit policy and a probe-bound question operator.
func TestInstalledRustConfigAndAsks(t *testing.T) {
	probe := os.Getenv("OA_RUST_CONFIG_ASKS_PROBE")
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
	// The consumer writes the edit-policy mode; an absent file permits.
	policyFile := filepath.Join(dir, "edit-policy")
	book, err := asks.LoadApplicationBook(filepath.Join(dir, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The native user rung has no notifier; this fixture invalidates periodically.
	changes, stop := make(chan struct{}, 1), make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				select {
				case changes <- struct{}{}:
				default:
				}
			}
		}
	}()
	prefix := fmt.Sprintf("rca-%d", time.Now().UnixNano()%1_000_000_000)
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		QuestionBook: book, QuestionEndpoint: listen.Endpoint(prefix + "q"),
		ConfigObservationSource: func(context.Context) (<-chan struct{}, func(), error) { return changes, func() {}, nil },
		ConfigEditPolicy: func(ctx context.Context, p *identity.Peer) error {
			mode, _ := os.ReadFile(policyFile)
			switch strings.TrimSpace(string(mode)) {
			case "forbidden":
				return errors.New("fixture edit refused")
			case "unavailable":
				return fmt.Errorf("fixture decision lookup: %w", configservice.ErrEditPolicyUnavailable)
			}
			return ctx.Err()
		},
		QuestionOperator: func(ctx context.Context, p *identity.Peer) error {
			path, err := p.Path.AtLeast(listen.Program.Path)
			if err != nil || !fixture.SameExecutable(path, probe) {
				return asksservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	run := func(exe string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, exe, append([]string{o.Endpoint}, args...)...)
		cmd.Dir = t.TempDir()
		out, err := fixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	out := run(probe, "all", policyFile)
	t.Log(out)
	var kept string
	for _, line := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "RETAINED "); ok {
			kept = id
		}
	}
	if kept == "" {
		t.Fatal("consumer did not report the retained question")
	}
	foreign := filepath.Join(dir, "foreign-consumer"+filepath.Ext(probe))
	data, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Log(run(foreign, "operator-forbidden", kept))
	t.Log(run(probe, "still-answered"))
	if entries, err := os.ReadDir(home); err != nil || len(entries) > 1 {
		t.Fatalf("clients created provider files in the isolated home: %v", err)
	}
}
