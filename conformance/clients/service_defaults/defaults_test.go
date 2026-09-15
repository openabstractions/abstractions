package service_defaults_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	config "github.com/openabstractions/abstraction-config/go"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	download "github.com/openabstractions/abstraction-download/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	logservice "github.com/openabstractions/abstraction-logging/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	model "github.com/openabstractions/abstraction-model/go"
	modelservice "github.com/openabstractions/abstraction-model/go/service"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
	storage "github.com/openabstractions/abstraction-storage/go"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
	oafixture "github.com/openabstractions/abstractions/conformance/clients/fixture"
)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(name, home)
	}
	t.Setenv("ProgramData", filepath.Join(home, "machine"))
	for _, name := range []string{"ABSTRACTION_STORE", "ABSTRACTION_NAS_STORE", "ABSTRACTION_LOG_SERVICE", "ABSTRACTION_LOG"} {
		t.Setenv(name, "")
	}
	return home
}
func serve(t *testing.T, sink logging.Sink, configure ...func(*host.Options)) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof unavailable; no service success claim")
	}
	prefix := fmt.Sprintf("default-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	options := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"), Sink: sink}
	for _, c := range configure {
		c(&options)
	}
	h, err := host.Listen(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime cleanup timed out")
		}
	})
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", options.Endpoint)
	return options.Endpoint
}
func TestLoggingDefaultNeverFallsBackAndKeepsBinding(t *testing.T) {
	home := isolate(t)
	legacy := filepath.Join(home, "legacy", "must-not-create.jsonl")
	t.Setenv(logging.EnvSink, legacy)
	t.Setenv(logging.EnvService, filepath.Join(home, "absent-legacy.sock"))
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", listen.Endpoint(fmt.Sprintf("absent-%d", time.Now().UnixNano())))
	handler := logging.Default("default-test")
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "resolved default", 0)
	if err := handler.Handle(context.Background(), record); err == nil {
		t.Fatal("missing resolver silently accepted logging")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("logging default created shared file", err)
	}
	sink, err := logging.OpenFileSink(filepath.Join(home, "service-owned.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	endpoint := serve(t, sink)
	handler = logging.NewResolvedHandler("default-test", endpoint, fixtureServer(t))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", listen.Endpoint("now-absent-defaults"))
	record.Message = "retained binding"
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal("selected binding changed", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(sink.Path())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "retained binding") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not retain records")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("logging fallback wrote shared file", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := handler.Handle(ctx, record); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestPythonConfigUsesServiceAndPreservesRecords(t *testing.T) {
	python := os.Getenv("OA_SERVICE_DEFAULTS_PYTHON")
	if python == "" {
		t.Skip("set OA_SERVICE_DEFAULTS_PYTHON through run.py for installed Python/native proof")
	}
	home := isolate(t)
	if err := config.Save(config.UserPath(), config.Config{Store: "existing-store", NASStore: "existing-nas", Off: map[string]string{"nas": "maintenance"}}); err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	// consumer.py writes the edit-policy mode; an absent file permits.
	editPolicy := filepath.Join(fixture, "edit-policy")
	book, err := asks.LoadApplicationBook(filepath.Join(fixture, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	pythonPath, err := exec.LookPath(python)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := logging.OpenFileSink(filepath.Join(fixture, "records"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	content, err := storage.NewContentStore("python-fixture", filepath.Join(fixture, "content"))
	if err != nil {
		t.Fatal(err)
	}
	mode := func() string { b, _ := os.ReadFile(editPolicy); return strings.TrimSpace(string(b)) }
	rightsPolicyPath := filepath.Join(fixture, "rights-policy.json")
	rightsPolicy, err := rights.LoadDecisionPolicy(rightsPolicyPath, []string{"fixture.read"})
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	subjectProgram, err := oafixture.Executable(pythonPath)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := serve(t, sink, func(o *host.Options) {
		// The same mode file drives history, storage read and change-observation refusals.
		o.LogHistoryPolicy = func(ctx context.Context, p *identity.Peer) error {
			switch mode() {
			case "history-forbidden":
				return errors.New("fixture history refused")
			case "history-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", logservice.ErrHistoryPolicyUnavailable)
			}
			return ctx.Err()
		}
		o.Storage, o.StorageEndpoint = content, o.Endpoint+"s"
		o.StoragePolicy = func(ctx context.Context, p *identity.Peer, digest string) error {
			if mode() == "storage-unreadable" {
				return errors.New("fixture read refused")
			}
			return ctx.Err()
		}
		o.StorageWritePolicy = func(ctx context.Context, p *identity.Peer, digest string) error { return ctx.Err() }
		o.StorageWriteLimit, o.StorageWriteRecordPath = 1<<20, filepath.Join(fixture, "writer-records.json")
		o.StorageChangesPolicy = func(ctx context.Context, p *identity.Peer, resource string) error {
			switch mode() {
			case "changes-forbidden":
				return errors.New("fixture observation refused")
			case "changes-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", storageservice.ErrPolicyUnavailable)
			}
			return ctx.Err()
		}
		o.StorageChangesInterval, o.StorageChangesCapacity = 25*time.Millisecond, 4
		// The native user rung has no notifier; this fixture invalidates periodically.
		changes, stop := make(chan struct{}, 1), make(chan struct{})
		t.Cleanup(func() { close(stop) })
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
		o.ConfigObservationSource = func(context.Context) (<-chan struct{}, func(), error) { return changes, func() {}, nil }
		o.RightsPolicy, o.RightsEndpoint = rightsPolicy, o.Endpoint+"a"
		// Only the installed Python consumer administers policy; the mode file names outages.
		o.RightsOperator = func(ctx context.Context, p *identity.Peer) error {
			switch mode() {
			case "operator-forbidden":
				return rightsservice.ErrOperatorForbidden
			case "operator-unavailable":
				return errors.New("fixture operator decision unavailable")
			}
			path, err := p.Path.AtLeast(listen.Program.Path)
			if err != nil || !oafixture.SameExecutable(path, pythonPath) {
				return rightsservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}
		o.RightsEnforcer = func(ctx context.Context, p *identity.Peer, action, resource string) bool {
			path, err := p.Path.AtLeast(listen.Program.Path)
			return err == nil && oafixture.SameExecutable(path, pythonPath) && action == "fixture.read" && ctx.Err() == nil
		}
		o.ConfigEditPolicy = func(ctx context.Context, p *identity.Peer) error {
			mode, _ := os.ReadFile(editPolicy)
			switch strings.TrimSpace(string(mode)) {
			case "forbidden":
				return errors.New("fixture edit refused")
			case "unavailable":
				return fmt.Errorf("fixture decision lookup: %w", configservice.ErrEditPolicyUnavailable)
			}
			return ctx.Err()
		}
		// The same mode file drives model lookup and router refusals.
		o.ModelRegistry, o.ModelEndpoint = model.NewServiceRegistry(fixtureRegistry{}), o.Endpoint+"m"
		o.ModelPolicy = func(ctx context.Context, p *identity.Peer, registry string) error {
			mode, _ := os.ReadFile(editPolicy)
			switch strings.TrimSpace(string(mode)) {
			case "model-forbidden":
				return errors.New("fixture lookup refused")
			case "model-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", modelservice.ErrPolicyUnavailable)
			}
			return ctx.Err()
		}
		// The fake model hosts every router proof reads: a resident Lemonade
		// model, cold LM Studio models and an unreachable Ollama host.
		lemonade, studio, closeHosts := oafixture.ModelHosts(nil)
		t.Cleanup(closeHosts)
		live := router.New(router.Lemonade(lemonade), router.LMStudio(studio), router.Ollama(oafixture.UnreachableOllama))
		live.Survey()
		o.Router, o.RouterEndpoint = live, o.Endpoint+"r"
		o.RouterPolicy = func(ctx context.Context, p *identity.Peer, action, resource string) error {
			mode, _ := os.ReadFile(editPolicy)
			switch strings.TrimSpace(string(mode)) {
			case "router-forbidden":
				return errors.New("fixture route refused")
			case "router-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", routerservice.ErrPolicyUnavailable)
			}
			return ctx.Err()
		}
		o.QuestionBook, o.QuestionEndpoint = book, o.Endpoint+"q"
		// Only the installed Python consumer operates questions in this fixture.
		o.QuestionOperator = func(ctx context.Context, p *identity.Peer) error {
			path, err := p.Path.AtLeast(listen.Program.Path)
			if err != nil || !oafixture.SameExecutable(path, pythonPath) {
				return asksservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}
	})
	clientHome := filepath.Join(home, "client")
	if err := os.Mkdir(clientHome, 0700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("consumer.py")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-I", script, os.Getenv("OA_SERVICE_DEFAULTS_PACKAGES"), endpoint, editPolicy)
	cmd.Dir = clientHome
	cmd.Env = append(os.Environ(), "OA_RIGHTS_ACCOUNT="+account.Uid, "OA_RIGHTS_PROGRAM="+subjectProgram, "OA_RIGHTS_POLICY_FILE="+rightsPolicyPath, "HOME="+clientHome, "APPDATA="+clientHome, "USERPROFILE="+clientHome, "XDG_CONFIG_HOME="+clientHome, "ProgramData="+clientHome, "PYTHONDONTWRITEBYTECODE=1")
	out, err := oafixture.Output(ctx, cmd)
	if err != nil {
		t.Fatalf("Python: %s: %v", out, err)
	}
	t.Log(string(out))
	entries, err := os.ReadDir(clientHome)
	if err != nil || len(entries) != 0 {
		t.Fatal("client created provider files", entries, err)
	}
	value := config.LoadWithOverrides(nil)
	if value.Store != "existing-store" || value.NASStore != "existing-nas" || value.Off["nas"] != "maintenance" || value.LogSink != "updated-by-service" {
		t.Fatalf("existing records changed: %+v", value)
	}
}

func TestLoggingDefaultRetainsTypedNotReady(t *testing.T) {
	isolate(t)
	endpoint := serve(t, nil)
	err := logging.NewResolvedHandler("refusal-test", endpoint, fixtureServer(t)).Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "unavailable", 0))
	var refusal *logging.ResolutionError
	if !errors.As(err, &refusal) || refusal.Status != "not_ready" {
		t.Fatalf("typed refusal: %T %v", err, err)
	}
}

type fixtureRegistry struct{}

func (fixtureRegistry) Registry() string { return "fixture" }
func (fixtureRegistry) Resolve(ctx context.Context, ref model.Ref) (download.Spec, error) {
	return download.Spec{Artifact: download.Artifact{Digest: "sha256:" + strings.Repeat("b", 64), Size: 7},
		Sources: []download.Source{{Scheme: "http", Locator: "http://127.0.0.1/python-weights"}}}, ctx.Err()
}

// The host supplies this independently of catalog replies. It runs in this
// isolated test process, so the executable and OS principal are known a priori.
func fixtureServer(t *testing.T) listen.ServerExpectation {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.User{Kind: "posix", UID: os.Getuid()}
	if runtime.GOOS == "windows" {
		u, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		principal = identity.User{Kind: "windows", SID: u.Uid}
	}
	return listen.ServerExpectation{Principal: principal, Program: exe}
}
