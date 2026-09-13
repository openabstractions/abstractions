package storagebinding_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	storage "github.com/openabstractions/abstraction-storage/go"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type local struct {
	path, digest string
	finds        atomic.Int32
}

func (*local) Name() string { return "isolated" }
func (s *local) Find(d string) (storage.Ref, bool) {
	s.finds.Add(1)
	return storage.Ref{Store: s.Name(), Digest: d, Size: 21}, d == s.digest
}
func (*local) Place(string, int64) (storage.Ref, error) { return storage.Ref{}, storage.ErrReadOnly }
func (s *local) Path(storage.Ref) string                { return s.path }
func TestInstalledStorageBinding(t *testing.T) {
	probe := os.Getenv("OA_CPP_STORAGE_BINDING_PROBE")
	if probe == "" {
		t.Fatal("installed probe required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	body := []byte("content fixture bytes")
	provider := &local{path: filepath.Join(dir, "private"), digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body))}
	if err := os.WriteFile(provider.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-storage-bind-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	var allowed atomic.Bool
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), StorageEndpoint: endpoint("storage"), Storage: provider, StoragePolicy: func(ctx context.Context, p *identity.Peer, d string) error {
		if !allowed.Load() || d != provider.digest {
			return errors.New("content denied")
		}
		return ctx.Err()
	}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("host did not drain")
		}
	}()
	run := func(mode string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, probe, append([]string{o.Endpoint, provider.digest, mode}, args...)...)
		cmd.Dir = t.TempDir()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %s %v", mode, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("denied")
	if provider.finds.Load() != 0 {
		t.Fatal("denied lookup reached provider")
	}
	allowed.Store(true)
	t.Log(run("roundtrip"))
	resource := strings.Fields(run("open"))
	if len(resource) != 2 {
		t.Fatal(resource)
	}
	allowed.Store(false)
	run("revoked", resource...)
}
