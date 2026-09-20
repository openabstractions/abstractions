package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
)

// memCredentialBackend is a Backend kept in memory, for tests only: it never
// touches a platform store, so the round trip runs the same on every OS.
type memCredentialBackend struct {
	mu    sync.Mutex
	items map[string][]byte
}

func newMemCredentialBackend() *memCredentialBackend {
	return &memCredentialBackend{items: map[string][]byte{}}
}
func (b *memCredentialBackend) Name() string        { return credentials.StoreFile }
func (b *memCredentialBackend) MaxSecretBytes() int { return credentials.MaxSecretBytes }
func (b *memCredentialBackend) Put(key string, secret []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[key] = append([]byte(nil), secret...)
	return nil
}
func (b *memCredentialBackend) Get(key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.items[key]
	if !ok {
		return nil, credentials.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}
func (b *memCredentialBackend) Delete(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, key)
	return nil
}

// panelCredentialsRuntime hosts a real credentials holder and a real rights
// decision policy the holder's decide function asks directly, the way
// serve/runtime_credentials.go composes the two. The rights operator
// (used by /rights, for allow/deny) admits this test process; the policy
// itself permits this process to manage and read its own credentials, the way
// an installed operator program would after setup.
func panelCredentialsRuntime(t *testing.T) *rights.DecisionPolicy {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "policy.json"),
		[]string{credentials.ActionManage, credentials.ActionRead, credentials.ActionApply})
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	subject := wire.Subject{Account: account.Uid, Program: filepath.Clean(exe)}
	for _, action := range []string{credentials.ActionManage, credentials.ActionRead} {
		if err := policy.Set(subject, action, credentials.ResourceAccount, true); err != nil {
			t.Fatal(err)
		}
	}
	decide := func(ctx context.Context, subj cwire.Subject, action, resource string) (string, error) {
		return policy.Decide(wire.Subject{Account: subj.Account, Program: subj.Program}, action, resource).Outcome.String(), nil
	}
	holderStore, err := credentials.Open(credentials.Config{
		Namespace: fmt.Sprintf("panel-credentials-test-%d", time.Now().UnixNano()), Backend: newMemCredentialBackend(),
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("panel-credentials-%d", time.Now().UnixNano())
	credEndpoint := listen.Endpoint(prefix + "-cred")
	credHost, err := credservice.Listen(credEndpoint, holderStore, decide, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := host.Options{
		Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		Credentials: credHost, CredentialsEndpoint: credEndpoint,
		RightsPolicy: policy, RightsEndpoint: listen.Endpoint(prefix + "r"),
		RightsOperator: func(ctx context.Context, peer *identity.Peer) error {
			process, err := peer.Process.AtLeast(listen.Program.Process)
			if err != nil || process.PID != os.Getpid() {
				return rightsservice.ErrOperatorForbidden
			}
			return ctx.Err()
		},
	}
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
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", o.Endpoint)
	trustPanelRuntime(t, o.Endpoint)
	return policy
}

// TestServicePanelCredentialsRequestValidation checks that a malformed
// request is refused with 400 before the handler ever resolves the holder: no
// trusted runtime is configured, so a request that reached the service would
// read unavailable (503), not 400.
func TestServicePanelCredentialsRequestValidation(t *testing.T) {
	own(t)
	h := (&servicePanel{}).handler("test-key")
	targets, consumers := []string{"huggingface.co"}, []string{"abstraction.download/http-execution@1"}
	longSecret := strings.Repeat("x", credentials.MaxSecretBytes+1)
	bad := []credentialEdit{
		{Action: "add", Name: "bad name", Kind: "bearer", Targets: targets, Consumers: consumers, Secret: "s"},
		{Action: "add", Name: "hf", Kind: "bearer", Targets: nil, Consumers: consumers, Secret: "s"},
		{Action: "add", Name: "hf", Kind: "bearer", Targets: targets, Consumers: nil, Secret: "s"},
		{Action: "add", Name: "hf", Kind: "bearer", Targets: targets, Consumers: consumers, Secret: ""},
		{Action: "add", Name: "hf", Kind: "bearer", Targets: targets, Consumers: consumers, Secret: longSecret},
		{Action: "add", Name: "hf", Kind: "header", Header: "", Targets: targets, Consumers: consumers, Secret: "s"},
		{Action: "add", Name: "hf", Kind: "unsupported", Targets: targets, Consumers: consumers, Secret: "s"},
		{Action: "add", Name: "hf", Kind: "bearer", Targets: targets, Consumers: consumers, Secret: "s", ExpectedRevision: "must-be-empty"},
		{Action: "add", Name: "hf", Kind: "bearer", Targets: targets, Consumers: consumers, Secret: "s", Expires: "not-a-date"},
		{Action: "rotate", Name: "hf", Secret: "s"},
		{Action: "rotate", Name: "hf", ExpectedRevision: "1", Secret: ""},
		{Action: "rotate", Name: "hf", ExpectedRevision: "1", Secret: longSecret},
		{Action: "revoke", Name: "hf"},
		{Action: "revoke", Name: "bad name", ExpectedRevision: "1"},
		{Action: "publish", Name: "hf"},
	}
	for i, edit := range bad {
		r := panelRequest(t, h, "/credentials", edit)
		if r.Code != 400 {
			t.Fatalf("case %d: invalid credential edit %+v reached the service: %d %s", i, edit, r.Code, r.Body.Bytes())
		}
	}
}

// TestServicePanelCredentialsHolder rounds a credential through list, add,
// rotate and revoke at the revision each listing showed, then allows a
// program to apply it through /rights at the listed policy revision. No reply
// along the way carries the secret bytes.
func TestServicePanelCredentialsHolder(t *testing.T) {
	own(t)
	panelCredentialsRuntime(t)
	h := (&servicePanel{}).handler("test-key")
	const secret = "panel-test-secret-should-never-be-echoed-9f2c"
	const name = "hf"

	noSecret := func(r *httptest.ResponseRecorder) {
		t.Helper()
		if bytes.Contains(r.Body.Bytes(), []byte(secret)) {
			t.Fatalf("reply carried the secret: %s", r.Body.Bytes())
		}
	}
	list := func() (cwire.MetadataPage, *httptest.ResponseRecorder) {
		r := panelRequest(t, h, "/credentials?cursor=", nil)
		noSecret(r)
		return decodePanel[cwire.MetadataPage](t, r.Code, r.Body.Bytes()), r
	}
	edit := func(e credentialEdit) (map[string]any, *httptest.ResponseRecorder) {
		r := panelRequest(t, h, "/credentials", e)
		noSecret(r)
		var raw map[string]any
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &raw) != nil {
			t.Fatalf("panel reply %d %s", r.Code, r.Body.Bytes())
		}
		return raw, r
	}

	empty, _ := list()
	if empty.Outcome != cwire.PageOutcomePage || len(empty.Records) != 0 || !empty.Complete {
		t.Fatalf("empty list %+v", empty)
	}

	added, _ := edit(credentialEdit{Action: "add", Name: name, Kind: "bearer", Targets: []string{"huggingface.co"}, Consumers: []string{"abstraction.download/http-execution@1"}, Secret: secret})
	if added["Outcome"] != "stored" {
		t.Fatalf("add %+v", added)
	}
	revision, _ := added["Revision"].(string)
	if revision == "" {
		t.Fatalf("add carried no revision %+v", added)
	}

	dup, _ := edit(credentialEdit{Action: "add", Name: name, Kind: "bearer", Targets: []string{"huggingface.co"}, Consumers: []string{"abstraction.download/http-execution@1"}, Secret: secret})
	if dup["Outcome"] != "conflict" {
		t.Fatalf("duplicate add %+v", dup)
	}

	page, _ := list()
	if page.Outcome != cwire.PageOutcomePage || len(page.Records) != 1 || page.Records[0].Name != name || page.Records[0].Revision != revision {
		t.Fatalf("list after add %+v", page)
	}
	if page.Limits.SecureStore == "" {
		t.Fatalf("limits carried no secure store %+v", page.Limits)
	}

	staleRotate, _ := edit(credentialEdit{Action: "rotate", Name: name, ExpectedRevision: "not-" + revision, Secret: secret + "-rotated"})
	if staleRotate["Outcome"] != "conflict" {
		t.Fatalf("stale rotate %+v", staleRotate)
	}

	rotated, _ := edit(credentialEdit{Action: "rotate", Name: name, ExpectedRevision: revision, Secret: secret + "-rotated"})
	if rotated["Outcome"] != "rotated" {
		t.Fatalf("rotate %+v", rotated)
	}
	newRevision, _ := rotated["Revision"].(string)
	if newRevision == "" || newRevision == revision {
		t.Fatalf("rotate carried no new revision %+v", rotated)
	}

	staleRevoke, _ := edit(credentialEdit{Action: "revoke", Name: name, ExpectedRevision: revision})
	if staleRevoke["Outcome"] != "conflict" {
		t.Fatalf("stale revoke %+v", staleRevoke)
	}

	revoked, _ := edit(credentialEdit{Action: "revoke", Name: name, ExpectedRevision: newRevision})
	if revoked["Outcome"] != "revoked" {
		t.Fatalf("revoke %+v", revoked)
	}

	after, _ := list()
	if after.Outcome != cwire.PageOutcomePage || len(after.Records) != 1 || after.Records[0].State != cwire.StateRevoked {
		t.Fatalf("list after revoke %+v", after)
	}

	// Allow a program to apply a (new) credential through /rights, the same
	// endpoint the rest of the Panel's policy edits use, at the revision a
	// listing showed.
	rr := panelRequest(t, h, "/rights?cursor=", nil)
	noSecret(rr)
	rightsPage := decodePanel[wire.PolicyPage](t, rr.Code, rr.Body.Bytes())
	if rightsPage.Outcome != wire.PolicyPageOutcomePage {
		t.Fatalf("rights list %+v", rightsPage)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	allowed := panelRequest(t, h, "/rights", rightsEdit{Edit: "set", Revision: rightsPage.Revision, Account: account.Uid, Program: filepath.Clean(exe),
		Action: credentials.ActionApply, Resource: credentials.ResourceFor(name), Permit: true})
	noSecret(allowed)
	allowedEdit := decodePanel[wire.PolicyEdit](t, allowed.Code, allowed.Body.Bytes())
	if allowedEdit.Outcome != wire.PolicyEditOutcomeApplied {
		t.Fatalf("allow %+v", allowedEdit)
	}
}
