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

	config "github.com/openabstractions/abstraction-config/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
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
func serve(t *testing.T, sink logging.Sink) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof unavailable; no service success claim")
	}
	prefix := fmt.Sprintf("default-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	options := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"), Sink: sink}
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
	endpoint := serve(t, nil)
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
	cmd := exec.CommandContext(ctx, python, "-I", script, os.Getenv("OA_SERVICE_DEFAULTS_PACKAGES"), endpoint)
	cmd.Dir = clientHome
	cmd.Env = append(os.Environ(), "HOME="+clientHome, "APPDATA="+clientHome, "USERPROFILE="+clientHome, "XDG_CONFIG_HOME="+clientHome, "ProgramData="+clientHome, "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
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
