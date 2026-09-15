package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	facade "github.com/openabstractions/abstraction-facade/go"
	client "github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// serviceRuntime runs a runtime inside this test executable and binds the
// example to it with the account and image of this process as server trust.
func serviceRuntime(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("verified local fixture requires Program proof unavailable on macOS")
	}
	prefix := fmt.Sprintf("pretend-lemonade-%d", time.Now().UnixNano())
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		JobEndpoint: listen.Endpoint(prefix + "j"), JobRoot: filepath.Join(t.TempDir(), "provider"), JobOwner: "pretend-lemonade-owner", JobExecutor: downloadserve.HTTPExecution{}}
	h, err := host.Listen(o)
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
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.User{Kind: "windows", SID: account.Uid, UID: -1, GID: -1}
	if runtime.GOOS != "windows" {
		uid, _ := strconv.Atoi(account.Uid)
		gid, _ := strconv.Atoi(account.Gid)
		principal = identity.User{Kind: "posix", UID: uid, GID: gid}
	}
	server := listen.ServerExpectation{Principal: principal, Program: exe}
	previous := machine
	machine = func() *facade.Machine { return client.NewVerified(o.Endpoint, server) }
	t.Cleanup(func() { machine = previous })
}

// The example is the adopter's shape of the facade. Building it proves nothing;
// a caller that compiles and then waits forever for bytes nobody started is the
// defect this catches.
func TestTheWholeIntegration(t *testing.T) {
	serviceRuntime(t)
	payload := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "weights.bin", time.Now(), bytes.NewReader(payload))
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	args := []string{srv.URL + "/weights.bin", strconv.Itoa(len(payload)), fmt.Sprintf("sha256:%x", sha256.Sum256(payload))}
	if err := run(args, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	final := strings.TrimSpace(strings.TrimPrefix(lastLine(out.String()), "delivered to "))
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("%s: %v\n%s", final, err, out.String())
	}
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Fatalf("%s does not match what the server served", final)
	}
}

// Without a runtime the example reports it and creates no provider files.
func TestAbsenceHasNoLocalFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("ABSTRACTION_STORE", filepath.Join(dir, "must-not-create"))
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", listen.Endpoint(fmt.Sprintf("absent-lemonade-%d", time.Now().UnixNano())))
	if err := run([]string{"http://127.0.0.1:1/x", "1", "sha256:00"}, &bytes.Buffer{}); err == nil {
		t.Fatal("absent runtime accepted work")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("absence created files: %v %v", entries, err)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
