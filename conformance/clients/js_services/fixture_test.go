package jsservices_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	storage "github.com/openabstractions/abstraction-storage/go"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type forged struct{}

func (forged) Resolve(q wire.ResolveRequest) (wire.ResolveResult, error) {
	return wire.ResolveResult{Status: "resolved", Reference: &wire.ServiceReference{Provider: "test", Capability: "wrong", Contract: q.Contracts[0], Scope: "local", Transport: "oa-framed-local@1", Endpoint: "must-not-connect", Guarantees: []string{}}}, nil
}
func TestInstalledJavaScript(t *testing.T) {
	node, script := os.Getenv("OA_JS_NODE"), os.Getenv("OA_JS_CONSUMER")
	if node == "" || script == "" {
		t.Fatal("installed consumer required")
	}
	dir := t.TempDir()
	for _, key := range []string{"HOME", "APPDATA", "XDG_CONFIG_HOME", "ProgramData"} {
		t.Setenv(key, dir)
	}
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-js-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	run := func(t *testing.T, mode, ep string, extra ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, append([]string{script, mode, ep}, extra...)...)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "ABSTRACTION_RUNTIME_ENDPOINT="+ep)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", mode, err, out)
		}
		t.Log(string(out))
	}
	t.Run("runtime", func(t *testing.T) {
		sink, err := logging.OpenFileSink(filepath.Join(dir, "private-records"))
		if err != nil {
			t.Fatal(err)
		}
		defer sink.Close()
		body := bytes.Repeat([]byte("x"), 150000)
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		}))
		defer source.Close()
		content := &fixtureContent{path: filepath.Join(dir, "private-content"), digest: digest}
		if err := os.WriteFile(content.path, body, 0600); err != nil {
			t.Fatal(err)
		}
		ep := endpoint("runtime")
		h, err := host.Listen(host.Options{Endpoint: ep, LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), Sink: sink,
			JobRoot: filepath.Join(dir, "jobs"), JobOwner: "js-fixture-owner", JobEndpoint: endpoint("jobs"), JobExecutor: downloadserve.HTTPExecution{},
			Storage: content, StorageEndpoint: endpoint("storage"), StoragePolicy: func(ctx context.Context, p *identity.Peer, d string) error {
				path, e := p.Path.AtLeast(listen.Program.Path)
				if e != nil || filepath.Clean(path) != filepath.Clean(node) || d != digest {
					return errors.New("fixture content forbidden")
				}
				return ctx.Err()
			}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- h.Serve(ctx) }()
		defer func() {
			cancel()
			h.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				t.Error("runtime drain")
			}
		}()
		run(t, "roundtrip", ep)
		account, e := user.Current()
		if e != nil {
			t.Fatal(e)
		}
		program, e := os.Executable()
		if e != nil {
			t.Fatal(e)
		}
		run(t, "verified", ep, account.Uid, program)
		run(t, "untrusted", ep, account.Uid, program)
		run(t, "job-storage", ep, source.URL, digest)
	})
	for _, mode := range []string{"forged", "oversized", "truncated", "malformed", "cancel", "timeout", "queue"} {
		t.Run(mode, func(t *testing.T) {
			ep := endpoint(mode)
			listener, err := listen.Listen(ep)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
				if err != nil {
					return
				}
				defer call.Close()
				switch mode {
				case "forged":
					d := wire.ResolverDispatcher{Handler: forged{}}
					reply, e := d.ExchangeFrame(call.Frame)
					if e == nil {
						call.Reply(reply)
					}
				case "oversized":
					conn.Write([]byte{0x7f, 0xff, 0xff, 0xff})
				case "truncated":
					conn.Write([]byte{0, 0, 0, 9, 'a'})
				case "malformed":
					call.Reply([]byte("not-json"))
				default:
					<-call.WaitContext().Done()
				}
			}()
			run(t, mode, ep)
			cancel()
			listener.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("peer drain")
			}
		})
	}
	run(t, "absent", endpoint("absent"))
}

// Provider-private fixture allocation stays outside the installed application.
type fixtureContent struct{ path, digest string }

func (*fixtureContent) Name() string { return "js-fixture" }
func (p *fixtureContent) Find(d string) (storage.Ref, bool) {
	return storage.Ref{Store: p.Name(), Digest: d, Size: 150000}, d == p.digest
}
func (*fixtureContent) Place(string, int64) (storage.Ref, error) {
	return storage.Ref{}, storage.ErrReadOnly
}
func (p *fixtureContent) Path(storage.Ref) string { return p.path }
