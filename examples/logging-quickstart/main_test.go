package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// With no endpoint named, the example stops with the facade's
// no-development-runtime error and its usage, before any dial [FAC-B5].
func TestNoDevelopmentRuntimeStopsAtUsage(t *testing.T) {
	t.Setenv(bootstrap.RuntimeEndpointVariable, "")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), bootstrap.ErrNoDevelopmentRuntime.Error()) || !strings.Contains(stderr.String(), "Usage: logging-quickstart") {
		t.Fatalf("stderr lacks the no-development-runtime error and usage: %s", stderr.String())
	}
}

// The example binds a runtime started in a temporary folder through the
// facade's development selection: the printed endpoint in the variable and
// --runtime-program naming the runtime's executable. It writes one event and
// reads the same event back from the runtime's history.
func TestWritesAndReadsBackThroughDevelopmentRuntime(t *testing.T) {
	skipWithoutProgramProof(t)
	t.Setenv("OA_TEST_ALLOW_INSTALLED_RUNTIME", "")
	prefix := fmt.Sprintf("logging-quickstart-%d", os.Getpid())
	sink, err := logging.OpenFileSink(filepath.Join(t.TempDir(), "history"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	h, err := host.Listen(host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		ConfigStore: casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}, ConfigUserKey: filepath.Join(t.TempDir(), "config"), ConfigWithoutMachine: true, Sink: sink})
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
			t.Error("runtime failed to stop")
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(bootstrap.RuntimeEndpointVariable, listen.Endpoint(prefix))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--runtime-program", filepath.Join(t.TempDir(), "other-runtime.exe")}, &stdout, &stderr); code != 1 {
		t.Fatalf("a runtime other than the named program was bound: exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--runtime-program", exe}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "wrote: worker started") || !strings.Contains(stdout.String(), "read back: worker started (component=quickstart)") {
		t.Fatalf("missing write or readback: %s", stdout.String())
	}
	page, err := sink.Read("", 16, 65536)
	if err != nil || len(page.Records) != 1 || page.Records[0].Attrs["component"] != "quickstart" {
		t.Fatalf("runtime did not retain the event: %+v %v", page, err)
	}
}

// skipWithoutProgramProof skips where the verified local fixture cannot
// prove the server's program: macOS.
func skipWithoutProgramProof(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("verified local fixture requires Program proof unavailable on macOS")
	}
}
