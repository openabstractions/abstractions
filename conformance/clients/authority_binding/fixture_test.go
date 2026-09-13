package authoritybinding_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rc "github.com/openabstractions/abstraction-rights/go/client"
	storage "github.com/openabstractions/abstraction-storage/go"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstalledAuthority(t *testing.T) {
	probe := os.Getenv("OA_CPP_AUTHORITY_PROBE")
	if probe == "" {
		t.Fatal("installed probe required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bookPath := filepath.Join(dir, "questions.json")
	book, err := asks.LoadApplicationBook(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "rights.json"), []string{"fixture.read", "abstraction.storage/content.read"})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-authority-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	subjects := make(chan rc.Subject, 10)
	var operatorAllowed atomic.Bool
	var rightsOperatorAllowed atomic.Bool
	body := bytes.Repeat([]byte("x"), 150000)
	provider := &authorityContent{path: filepath.Join(dir, "private-content"), digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body))}
	if err := os.WriteFile(provider.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), QuestionEndpoint: endpoint("asks"), QuestionBook: book, QuestionOperator: func(ctx context.Context, p *identity.Peer) error {
		subject, err := rc.SubjectFromPeer(p)
		if err != nil || filepath.Clean(subject.Program) != filepath.Clean(probe) || !operatorAllowed.Load() {
			return asksservice.ErrOperatorForbidden
		}
		return ctx.Err()
	}, RightsEndpoint: endpoint("rights"), RightsPolicy: policy, RightsEnforcer: func(ctx context.Context, p *identity.Peer, a, r string) bool {
		s, e := rc.SubjectFromPeer(p)
		if e == nil && a == "fixture.read" {
			subjects <- s
		}
		if a == "abstraction.storage/content.read" {
			process, err := p.Process.AtLeast(listen.Program.Process)
			return err == nil && process.PID == os.Getpid() && r == provider.digest && ctx.Err() == nil
		}
		return false
	}}
	o.RightsOperator = func(ctx context.Context, peer *identity.Peer) error {
		subject, err := rc.SubjectFromPeer(peer)
		if err != nil || filepath.Clean(subject.Program) != filepath.Clean(probe) || !rightsOperatorAllowed.Load() {
			return rightsservice.ErrOperatorForbidden
		}
		return ctx.Err()
	}
	o.Storage = provider
	o.StorageEndpoint = endpoint("storage")
	o.StoragePolicy = host.ContentPolicyFromRights(rc.New(o.RightsEndpoint), "abstraction.storage/content.read")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	start := func() func() {
		h, e := host.Listen(o)
		if e != nil {
			t.Fatal(e)
		}
		child, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- h.Serve(child) }()
		return func() {
			stop()
			h.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("runtime drain timeout")
			}
		}
	}
	run := func(mode, expected string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, probe, append([]string{o.Endpoint, mode, expected}, args...)...)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "OA_AUTHORITY_ACCOUNT="+account.Uid, "OA_AUTHORITY_PROGRAM="+filepath.Clean(probe))
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s/%s %s: %v", mode, expected, out, e)
		}
		return strings.TrimSpace(string(out))
	}
	var id string
	func() {
		stop := start()
		defer stop()
		id = run("questions", "pending")
		run("operator", "forbidden", id)
		operatorAllowed.Store(true)
		run("operator", "answered", id)
		operatorAllowed.Store(false)
		run("operator", "forbidden", id)
		run("rights", "not_granted")
		var subject rc.Subject
		select {
		case subject = <-subjects:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if filepath.Clean(subject.Program) != filepath.Clean(probe) {
			t.Fatalf("unexpected native subject %q", subject.Program)
		}
		if e := policy.Set(subject, "fixture.read", "resource", true); e != nil {
			t.Fatal(e)
		}
		revision := run("rights", "permitted")
		if e := policy.Revoke(subject, "fixture.read", "resource"); e != nil {
			t.Fatal(e)
		}
		run("rights", "not_granted", revision)
		run("rights-operator", "forbidden", provider.digest)
		if provider.finds.Load() != 0 {
			t.Fatal("denied read reached provider")
		}
		rightsOperatorAllowed.Store(true)
		run("rights-operator", "apply", provider.digest)
		rightsOperatorAllowed.Store(false)
		run("rights-operator", "forbidden", provider.digest)
	}()
	book, err = asks.LoadApplicationBook(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	o.QuestionBook = book
	stop := start()
	defer stop()
	run("questions", "answered", id)
	run("questions", "answered", id)
	if e := book.Forget(id); e != nil {
		t.Fatal(e)
	}
	run("questions", "gone")
	t.Log("PASS generated rights operator enforces bounded content; pending/authorized generated operator answer/restart/replay/gone; grant/revoke; refused relay; bounded wait/cancel")
}

type authorityContent struct {
	path, digest string
	finds        atomic.Int32
}

func (*authorityContent) Name() string { return "authority-fixture" }
func (p *authorityContent) Find(d string) (storage.Ref, bool) {
	p.finds.Add(1)
	return storage.Ref{Store: p.Name(), Digest: d, Size: 150000}, d == p.digest
}
func (*authorityContent) Place(string, int64) (storage.Ref, error) {
	return storage.Ref{}, storage.ErrReadOnly
}
func (p *authorityContent) Path(storage.Ref) string { return p.path }
