package storagebinding_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rc "github.com/openabstractions/abstraction-rights/go/client"
	storage "github.com/openabstractions/abstraction-storage/go"
	oafixture "github.com/openabstractions/abstractions/conformance/clients/fixture"
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
		out, err := oafixture.Output(ctx, cmd)
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

type writable struct {
	*storage.ContentStore
	finds, places atomic.Int32
}

func (s *writable) Find(d string) (storage.Ref, bool) {
	s.finds.Add(1)
	return s.ContentStore.Find(d)
}
func (s *writable) Place(d string, n int64) (storage.Ref, error) {
	s.places.Add(1)
	return s.ContentStore.Place(d, n)
}

// TestInstalledStorageWriter drives the installed C++ writer and a separate
// C++ reader process through the real runtime and generated rights enforcement.
func TestInstalledStorageWriter(t *testing.T) {
	const readAction, writeAction = "abstraction.storage/content.read", "abstraction.storage/content.write"
	const limit = 1 << 20
	probe := os.Getenv("OA_CPP_STORAGE_BINDING_PROBE")
	if probe == "" {
		t.Fatal("installed probe required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "provider-private")
	content, err := storage.NewContentStore("isolated", root)
	if err != nil {
		t.Fatal(err)
	}
	provider := &writable{ContentStore: content}
	fixture := func(name string, body []byte) (string, string) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		return path, fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	}
	bodyFile, digest := fixture("body", bytes.Repeat([]byte("installed writer bytes "), 6600))
	otherFile, otherDigest := fixture("other", []byte("different content under one request identity"))
	objects := func() int {
		entries, err := os.ReadDir(filepath.Join(root, "blobs"))
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	staged := func(d string) string { return filepath.Join(root, "incoming", "sha256-"+d[7:]) }
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "rights.json"), []string{readAction, writeAction})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-storage-write-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), StorageEndpoint: endpoint("storage"), Storage: provider, RightsEndpoint: endpoint("rights"), RightsPolicy: policy}
	// The isolated runtime designates only its own process as storage enforcer.
	o.RightsEnforcer = func(ctx context.Context, p *identity.Peer, a, r string) bool {
		process, err := p.Process.AtLeast(listen.Program.Process)
		return err == nil && process.PID == os.Getpid() && (a == readAction || a == writeAction) && ctx.Err() == nil
	}
	decisions := rc.New(o.RightsEndpoint)
	o.StoragePolicy = host.ContentPolicyFromRights(decisions, readAction)
	enforceWrite := host.ContentPolicyFromRights(decisions, writeAction)
	subjects := make(chan rc.Subject, 64)
	o.StorageWritePolicy = func(ctx context.Context, p *identity.Peer, d string) error {
		if s, err := rc.SubjectFromPeer(p); err == nil {
			select {
			case subjects <- s:
			default:
			}
		}
		return enforceWrite(ctx, p, d)
	}
	o.StorageWriteLimit = limit
	o.StorageWriteRecordPath = filepath.Join(dir, "writer-records.json")
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	run := func(d, mode string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, probe, append([]string{o.Endpoint, d, mode}, args...)...)
		cmd.Dir = t.TempDir()
		out, err := oafixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("%s: %s %v", mode, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	const request, conflicting, oversized, interrupted = "installed-writer-0001", "installed-writer-0001", "installed-oversize-1", "installed-partial-01"

	run(digest, "write-denied", request)
	if provider.finds.Load() != 0 || provider.places.Load() != 0 {
		t.Fatal("unauthorized writer reached provider")
	}
	var subject rc.Subject
	select {
	case subject = <-subjects:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if filepath.Clean(subject.Program) != filepath.Clean(probe) {
		t.Fatalf("unexpected native subject %q", subject.Program)
	}
	if err := policy.Set(subject, readAction, digest, true); err != nil {
		t.Fatal(err)
	}
	run(digest, "write-denied", request)
	if err := policy.Set(subject, writeAction, digest, true); err != nil {
		t.Fatal(err)
	}
	t.Log(run(digest, "write", request, bodyFile))
	t.Log(run(digest, "read-bytes", bodyFile))
	run(digest, "write-duplicate", request, bodyFile)
	if objects() != 1 {
		t.Fatal("duplicate request produced another object")
	}
	if err := policy.Set(subject, writeAction, otherDigest, true); err != nil {
		t.Fatal(err)
	}
	placed := provider.places.Load()
	run(otherDigest, "write-conflict", conflicting, otherFile)
	run(otherDigest, "write-oversized", oversized, fmt.Sprint(limit+1))
	if provider.places.Load() != placed || objects() != 1 {
		t.Fatal("conflicting or oversized write had effects")
	}
	handle := run(otherDigest, "write-partial", interrupted, otherFile)
	if info, err := os.Stat(staged(otherDigest)); err != nil || info.Size() != 10 {
		t.Fatalf("interrupted staging %v", err)
	}
	if err := policy.Set(subject, readAction, otherDigest, true); err != nil {
		t.Fatal(err)
	}
	run(otherDigest, "read-missing")
	if err := policy.Revoke(subject, writeAction, otherDigest); err != nil {
		t.Fatal(err)
	}
	run(otherDigest, "write-revoked", handle, otherFile)
	run(otherDigest, "read-missing")
	if _, err := os.Stat(staged(otherDigest)); !errors.Is(err, os.ErrNotExist) || objects() != 1 {
		t.Fatalf("revoked upload left effects: %v objects=%d", err, objects())
	}
	run(otherDigest, "write-denied", interrupted)
	if provider.places.Load() != placed+1 {
		t.Fatal("revoked writer reached provider placement")
	}
	t.Log("PASS installed C++ writer: rights-enforced write, separate exact read, duplicate/conflict identity, oversized/interrupted/revoked without visible content")
}

// TestInstalledStorageChanges drives the installed C++ change observer against a
// runtime polling a native content store under generated rights enforcement.
func TestInstalledStorageChanges(t *testing.T) {
	const readAction, observeAction = "abstraction.storage/content.read", "abstraction.storage/content.observe"
	probe := os.Getenv("OA_CPP_STORAGE_BINDING_PROBE")
	if probe == "" {
		t.Fatal("installed probe required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "provider-private")
	content, err := storage.NewContentStore("isolated", root)
	if err != nil {
		t.Fatal(err)
	}
	blob := func(body []byte, present bool) string {
		d := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
		path := filepath.Join(root, "blobs", "sha256-"+d[7:])
		if present {
			err = os.WriteFile(path, body, 0600)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "rights.json"), []string{readAction, observeAction})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-storage-changes-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), StorageEndpoint: endpoint("storage"), Storage: content, RightsEndpoint: endpoint("rights"), RightsPolicy: policy,
		StorageChangesInterval: 25 * time.Millisecond, StorageChangesCapacity: 2}
	o.RightsEnforcer = func(ctx context.Context, p *identity.Peer, a, r string) bool {
		process, err := p.Process.AtLeast(listen.Program.Process)
		return err == nil && process.PID == os.Getpid() && (a == readAction || a == observeAction) && ctx.Err() == nil
	}
	decisions := rc.New(o.RightsEndpoint)
	o.StoragePolicy = host.ContentPolicyFromRights(decisions, readAction)
	observe := host.ContentPolicyFromRights(decisions, observeAction)
	subjects := make(chan rc.Subject, 16)
	o.StorageChangesPolicy = func(ctx context.Context, p *identity.Peer, resource string) error {
		if s, err := rc.SubjectFromPeer(p); err == nil {
			select {
			case subjects <- s:
			default:
			}
		}
		return observe(ctx, p, resource)
	}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	run := func(d, mode string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, probe, append([]string{o.Endpoint, d, mode}, args...)...)
		cmd.Dir = t.TempDir()
		out, err := oafixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("%s: %s %v", mode, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	field := func(out, name string) string {
		fields := strings.Fields(out)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == name {
				return fields[i+1]
			}
		}
		t.Fatalf("%s missing in %q", name, out)
		return ""
	}
	added := []byte("delivered by another adopter to the shared store")
	addedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(added))

	t.Log(run(addedDigest, "changes-forbidden"))
	var subject rc.Subject
	select {
	case subject = <-subjects:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if filepath.Clean(subject.Program) != filepath.Clean(probe) {
		t.Fatalf("unexpected native subject %q", subject.Program)
	}
	for _, r := range [][2]string{{observeAction, "abstraction.storage/changes"}, {readAction, addedDigest}} {
		if err := policy.Set(subject, r[0], r[1], true); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := run(addedDigest, "changes-snapshot")
	if field(snapshot, "PRESENT") != "no" {
		t.Fatalf("snapshot before delivery: %s", snapshot)
	}
	cursor := field(snapshot, "CURSOR")
	blob(added, true)
	next := field(run(addedDigest, "changes-await", cursor, "added"), "NEXT")
	if present := run(addedDigest, "changes-snapshot"); field(present, "PRESENT") != "yes" {
		t.Fatalf("snapshot after delivery: %s", present)
	}
	blob(added, false)
	next = field(run(addedDigest, "changes-await", next, "removed"), "NEXT")
	for i := 0; i < 3; i++ {
		blob([]byte(fmt.Sprintf("overflow %d", i)), true)
		time.Sleep(100 * time.Millisecond)
	}
	t.Log(run(addedDigest, "changes-gap", next))
	if err := policy.Revoke(subject, observeAction, "abstraction.storage/changes"); err != nil {
		t.Fatal(err)
	}
	t.Log(run(addedDigest, "changes-forbidden"))
	t.Log("PASS installed C++ changes: refused observer, snapshot, external add and delete by polling, gap after overflow, revocation")
}
