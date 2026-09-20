package remotejobs_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	holding "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"github.com/openabstractions/abstraction-download/go/netcost"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-identity/remote"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	provider "github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func credentials(t *testing.T) (*tls.Config, []*tls.Config, [][32]byte) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated remote job CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	ca, e = x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	cert := func(id int64, usage x509.ExtKeyUsage) (tls.Certificate, [32]byte) {
		p, k, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		c := &x509.Certificate{SerialNumber: big.NewInt(id), Subject: pkix.Name{CommonName: "same display name"}, DNSNames: []string{"runtime.test"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		b, e := x509.CreateCertificate(rand.Reader, c, ca, p, key)
		if e != nil {
			t.Fatal(e)
		}
		parsed, e := x509.ParseCertificate(b)
		if e != nil {
			t.Fatal(e)
		}
		return tls.Certificate{Certificate: [][]byte{b}, PrivateKey: k}, sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	}
	server, _ := cert(2, x509.ExtKeyUsageServerAuth)
	configs := []*tls.Config{}
	keys := [][32]byte{}
	for i := int64(3); i < 6; i++ {
		c, k := cert(i, x509.ExtKeyUsageClientAuth)
		configs = append(configs, &tls.Config{Certificates: []tls.Certificate{c}, RootCAs: roots, ServerName: "runtime.test"})
		keys = append(keys, k)
	}
	return &tls.Config{Certificates: []tls.Certificate{server}, ClientCAs: roots}, configs, keys
}

func TestRemoteJobRecoveryScopesAndPolicy(t *testing.T) {
	server, configs, keys := credentials(t)
	root := filepath.Join(t.TempDir(), "private-provider")
	var drop, revoked, denyMutation atomic.Bool
	var dropped atomic.Int32
	drop.Store(true)
	authorize := func(ctx context.Context, p remote.Peer) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if p.Key == keys[0] && !revoked.Load() {
			return "tenant-a/app", nil
		}
		if p.Key == keys[1] {
			return "tenant-b/app", nil
		}
		return "", errors.New("unmapped or revoked key")
	}
	policy := func(ctx context.Context, _ remote.Peer, service, method string) error {
		if denyMutation.Load() && service == "abstraction.job/acceptance@1" && (method == "Submit" || method == "Reconcile" || method == "CancelWork") {
			return errors.New("method refused")
		}
		return ctx.Err()
	}
	address := "127.0.0.1:0"
	start := func() func() {
		guard, e := provider.AcquireHost(root)
		if e != nil {
			t.Fatal(e)
		}
		p, e := provider.Open(root, "remote-logical-owner")
		if e != nil {
			guard.Close()
			t.Fatal(e)
		}
		listener, e := net.Listen("tcp", address)
		if e != nil {
			guard.Close()
			t.Fatal(e)
		}
		address = listener.Addr().String()
		handler := p.RemoteHandler(authorize, policy)
		wrapped := func(ctx context.Context, peer remote.Peer, frame []byte) ([]byte, error) {
			response, e := handler(ctx, peer, frame)
			// Drop only the first Submit reply, after the real dispatcher returns it.
			if e == nil && drop.Load() {
				name, e := api.ServiceName(frame)
				if e == nil && name == "abstraction.job/acceptance@1" {
					// Exact sequencing arms this only after GetHistoryWindow, immediately
					// before Submit. Reconciliation below proves admission committed.
					if drop.CompareAndSwap(true, false) {
						dropped.Add(1)
						return nil, errors.New("fixture loses accepted reply")
					}
				}
			}
			return response, e
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- (remote.Server{TLS: server, Handler: wrapped, Timeout: 2 * time.Second, MaxFrame: provider.MaxFrameBytes, MaxConnections: 8}).Serve(ctx, listener)
		}()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case e := <-done:
					if !errors.Is(e, context.Canceled) {
						t.Error(e)
					}
				case <-time.After(3 * time.Second):
					t.Error("remote handler join timed out")
				}
				if e := p.CloseInventory(); e != nil {
					t.Error(e)
				}
				if e := guard.Close(); e != nil {
					t.Error(e)
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	drop.Store(false)
	stop := start()
	clients := func(index int) (*api.RecoverableAcceptanceClient, *api.OperationControlClient, *api.JobInventoryClient) {
		c, e := remote.Client(address, configs[index], time.Second, provider.MaxFrameBytes)
		if e != nil {
			t.Fatal(e)
		}
		return api.NewRecoverableAcceptanceClient(c), api.NewOperationControlClient(c), api.NewJobInventoryClient(c)
	}
	a, ops, inventory := clients(0)
	h, e := a.GetHistoryWindow()
	if e != nil {
		t.Fatal(e)
	}
	id := api.RequestIdentity{Key: "caller-persisted-key", HistoryEpoch: h.HistoryEpoch}
	const label = "remote fixture · go client"
	submission := api.Submission{Identity: id, Kind: "download", Spec: []byte(`{"source":"fixture"}`), RequiredGuarantees: []string{provider.GuaranteeReconciliation, provider.GuaranteeServiceRestart}, Label: label}
	drop.Store(true)
	if result, e := a.Submit(submission); e == nil {
		t.Fatalf("lost reply returned acceptance: %+v", result)
	}
	if dropped.Load() != 1 {
		t.Fatal("reply was not dropped after dispatch")
	}
	stop()
	start()
	a, ops, inventory = clients(0)
	recovered, e := a.Reconcile(id)
	if e != nil || recovered.Outcome != "accepted" || recovered.Receipt == nil {
		t.Fatalf("recovery: %+v %v", recovered, e)
	}
	receipt := *recovered.Receipt
	if receipt.LogicalOwner != h.LogicalOwner || receipt.Identity != id {
		t.Fatalf("owner/identity changed: %+v", receipt)
	}
	for _, g := range submission.RequiredGuarantees {
		found := false
		for _, v := range receipt.AcceptedGuarantees {
			found = found || v == g
		}
		if !found {
			t.Fatal("lost guarantee", g)
		}
	}
	assertOne := func(inv *api.JobInventoryClient, want api.Receipt) {
		t.Helper()
		page, e := inv.ListWork("", 8)
		// The Go client reads the stored caller label from ListWork (JOB-A12).
		if e != nil || page.Outcome != "page" || len(page.Snapshots) != 1 || !page.Complete || !reflect.DeepEqual(page.Snapshots[0].Receipt, want) || page.Snapshots[0].Label != label || page.Snapshots[0].LabelDerived {
			t.Fatalf("inventory: %+v %v", page, e)
		}
	}
	assertOne(inventory, receipt)
	observed, e := ops.ObserveWork(id)
	if e != nil || observed.Outcome != "observed" || observed.Snapshot == nil || !reflect.DeepEqual(observed.Snapshot.Receipt, receipt) {
		t.Fatalf("observation %+v %v", observed, e)
	}
	changed := submission
	changed.Spec = []byte(`{"source":"different"}`)
	conflict, e := a.Submit(changed)
	if e != nil || conflict.Outcome != "key_conflict" || conflict.Receipt != nil {
		t.Fatalf("conflict: %+v %v", conflict, e)
	}
	// A different label is outside equality: the original receipt and the
	// stored label return (JOB-A2, JOB-A12).
	relabelled := submission
	relabelled.Label = "a different label"
	replayed, e := a.Submit(relabelled)
	if e != nil || replayed.Outcome != "accepted" || replayed.Receipt == nil || !reflect.DeepEqual(*replayed.Receipt, receipt) {
		t.Fatalf("relabelled replay: %+v %v", replayed, e)
	}
	assertOne(inventory, receipt)
	// Equal certificate display names do not merge host-assigned caller namespaces.
	b, bops, binventory := clients(1)
	hidden, e := bops.ObserveWork(id)
	if e != nil || hidden.Outcome != "unknown" || hidden.Snapshot != nil {
		t.Fatalf("cross scope observation %+v %v", hidden, e)
	}
	other, e := b.Submit(submission)
	if e != nil || other.Outcome != "accepted" || other.Receipt == nil || other.Receipt.OperationID == receipt.OperationID {
		t.Fatalf("scope collision %+v %v", other, e)
	}
	assertOne(binventory, *other.Receipt)
	assertOne(inventory, receipt)
	// A trusted CA with an unmapped key grants no provider scope.
	unknown, _, _ := clients(2)
	refused, e := unknown.Submit(submission)
	if e != nil || refused.Outcome != "forbidden" || refused.Receipt != nil {
		t.Fatalf("unmapped %+v %v", refused, e)
	}
	denyMutation.Store(true)
	deniedID := api.RequestIdentity{Key: "denied-then-admitted", HistoryEpoch: h.HistoryEpoch}
	denied := submission
	denied.Identity = deniedID
	for _, call := range []func() (api.AcceptanceResult, error){func() (api.AcceptanceResult, error) { return a.Submit(denied) }, func() (api.AcceptanceResult, error) { return a.Reconcile(deniedID) }} {
		r, e := call()
		if e != nil || r.Outcome != "forbidden" || r.Receipt != nil {
			t.Fatalf("policy: %+v %v", r, e)
		}
	}
	assertOne(inventory, receipt)
	cancelled, e := a.CancelWork(id)
	if e != nil || cancelled.Outcome != "forbidden" {
		t.Fatalf("denied cancellation %+v %v", cancelled, e)
	}
	still, e := ops.ObserveWork(id)
	if e != nil || still.Snapshot == nil || still.Snapshot.CancellationRequested {
		t.Fatalf("denied cancellation mutated operation %+v %v", still, e)
	}
	denyMutation.Store(false)
	admitted, e := a.Submit(denied)
	if e != nil || admitted.Outcome != "accepted" {
		t.Fatalf("denial mutated/sealed identity: %+v %v", admitted, e)
	}
	revoked.Store(true)
	r, e := a.Reconcile(id)
	if e != nil || r.Outcome != "forbidden" || r.Receipt != nil {
		t.Fatalf("revoked %+v %v", r, e)
	}
	deniedObservation, e := ops.ObserveWork(id)
	if e != nil || deniedObservation.Outcome != "forbidden" || deniedObservation.Snapshot != nil {
		t.Fatalf("revoked observation %+v %v", deniedObservation, e)
	}
	revoked.Store(false)
	r, e = a.Reconcile(id)
	if e != nil || !reflect.DeepEqual(r.Receipt, &receipt) {
		t.Fatalf("revocation damaged receipt %+v %v", r, e)
	}
	page, e := inventory.ListWork("", 8)
	if e != nil || page.Outcome != "page" || len(page.Snapshots) != 2 || !page.Complete {
		t.Fatalf("duplicate work: %+v %v", page, e)
	}
	t.Log("PASS lost reply and restart recovery; fixed owner, independent key scopes, policy denial before seal/admission, revocation, bounded inventory; no duplicate")
}

// A submission reaching a remote job service carries its network constraint in
// the unchanged request bytes. The remote executor evaluates its own network
// cost, and its waiting word reaches the remote caller verbatim through the TLS
// binding (download CONTRACT DL-N7, job CONTRACT JOB-A15).
func TestRemoteWaitingWordIsRelayed(t *testing.T) {
	server, configs, keys := credentials(t)
	metered := netcost.NewFake(netcost.Metered)
	executor := downloadserve.HTTPExecution{NetworkCost: func() (netcost.Source, error) { return metered, nil }}
	p, e := provider.OpenWithExecutor(filepath.Join(t.TempDir(), "remote-executor"), "remote-executing-owner", executor)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	executed := make(chan error, 1)
	go func() { executed <- p.Execute(ctx) }()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	authorize := func(_ context.Context, peer remote.Peer) (string, error) {
		if peer.Key == keys[0] {
			return "tenant-a/app", nil
		}
		return "", errors.New("unmapped key")
	}
	served := make(chan error, 1)
	go func() {
		served <- (remote.Server{TLS: server, Handler: p.RemoteHandler(authorize, nil), Timeout: 2 * time.Second, MaxFrame: provider.MaxFrameBytes, MaxConnections: 8}).Serve(ctx, listener)
	}()
	t.Cleanup(func() {
		cancel()
		<-served
		<-executed
		p.CloseInventory()
	})
	c, e := remote.Client(listener.Addr().String(), configs[0], time.Second, provider.MaxFrameBytes)
	if e != nil {
		t.Fatal(e)
	}
	a, ops := api.NewRecoverableAcceptanceClient(c), api.NewOperationControlClient(c)
	h, e := a.GetHistoryWindow()
	if e != nil {
		t.Fatal(e)
	}
	spec := request.Encode(&request.Request{Sources: []request.Source{{Scheme: "http", Locator: "http://127.0.0.1:9/never-fetched"}}, Constraints: &request.Constraints{Network: request.NetworkUnmetered}})
	id := api.RequestIdentity{Key: "remote-waiting", HistoryEpoch: h.HistoryEpoch}
	accepted, e := a.Submit(api.Submission{Identity: id, Kind: "download", Spec: spec, RequiredGuarantees: request.NetworkCostGuarantees})
	if e != nil || accepted.Outcome != "accepted" {
		t.Fatalf("remote submit: %+v %v", accepted, e)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		relayed, e := ops.ObserveWork(id)
		if e != nil || relayed.Snapshot == nil {
			t.Fatalf("remote observation: %+v %v", relayed, e)
		}
		local, e := p.BindOperations("tenant-a/app").ObserveWork(id)
		if e != nil || local.Snapshot == nil {
			t.Fatalf("provider observation: %+v %v", local, e)
		}
		if relayed.Snapshot.Waiting == "network:metered" && relayed.Snapshot.State == "pending" {
			if !reflect.DeepEqual(relayed.Snapshot, local.Snapshot) {
				t.Fatalf("relayed %+v, provider %+v", relayed.Snapshot, local.Snapshot)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no waiting word relayed: %+v", relayed.Snapshot)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Log("PASS remote submission carries its unmetered constraint; the remote waiting word is relayed verbatim")
}

// remoteCredentialChildDir names the directory a child remote runtime shares
// with its parent: the client certificate it issues, and nothing secret.
const remoteCredentialChildDir = "OA_REMOTE_CREDENTIAL_CHILD"

// memoryStore is the remote runtime's platform store for the fixture.
type memoryStore struct {
	mu    sync.Mutex
	items map[string][]byte
}

func (m *memoryStore) Name() string        { return "memory-fixture" }
func (m *memoryStore) MaxSecretBytes() int { return holding.MaxSecretBytes }
func (m *memoryStore) Put(key string, secret []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = bytes.Clone(secret)
	return nil
}
func (m *memoryStore) Get(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[key]
	if !ok {
		return nil, holding.ErrNotFound
	}
	return bytes.Clone(item), nil
}
func (m *memoryStore) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, key)
	return nil
}

// scopedApplier applies the remote runtime's own credentials for the scope its
// host mapped from the verified client certificate. The decision subject is
// the mapped account and this runtime's program (rights-defaults DECISION §4).
type scopedApplier struct {
	holder  *holding.Holder
	program string
	decide  holding.Decider
}

func (a scopedApplier) use(scope, name, host string) cwire.Use {
	return cwire.Use{Subject: cwire.Subject{Account: scope, Program: a.program}, Consumer: downloadserve.CredentialConsumer, Name: name, Target: host}
}

func (a scopedApplier) ApplyCredential(ctx context.Context, scope, name, host string) (map[string]string, error) {
	result := a.holder.Apply(ctx, a.use(scope, name, host), a.decide)
	if result.Outcome != cwire.ApplyOutcomeApplied {
		return nil, download.CredentialRefusal(name, string(result.Outcome))
	}
	return result.Headers, nil
}

func (a scopedApplier) CheckCredential(ctx context.Context, scope, name, host string) error {
	if result := a.holder.Check(ctx, a.use(scope, name, host), a.decide); result.Outcome != cwire.ApplyOutcomeApplied {
		return download.CredentialRefusal(name, string(result.Outcome))
	}
	return nil
}

func writePEM(t *testing.T, path, kind string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRemoteCredentialRuntimeChild is the remote runtime of
// TestRemoteDelegationAppliesTheRemoteCredential, run in its own process. It
// issues the parent's client certificate, registers its own secret, serves the
// gated origin and a TLS job service with HTTP execution and its applier, and
// reveals the secret only after the parent has finished looking for it.
func TestRemoteCredentialRuntimeChild(t *testing.T) {
	dir := os.Getenv(remoteCredentialChildDir)
	if dir == "" {
		t.Skip("child process of TestRemoteDelegationAppliesTheRemoteCredential")
	}
	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "remote runtime CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(caDER)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, ed25519.PrivateKey, [32]byte) {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "remote fixture"}, DNSNames: []string{"runtime.test"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := x509.ParseCertificate(der)
		return der, key, sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	}
	serverDER, serverKey, _ := issue(2, x509.ExtKeyUsageServerAuth)
	clientDER, clientKey, clientID := issue(3, x509.ExtKeyUsageClientAuth)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, filepath.Join(dir, "ca.pem"), "CERTIFICATE", caDER)
	writePEM(t, filepath.Join(dir, "client.pem"), "CERTIFICATE", clientDER)
	writePEM(t, filepath.Join(dir, "client.key"), "PRIVATE KEY", pkcs8)

	var secretBytes [24]byte
	if _, err := rand.Read(secretBytes[:]); err != nil {
		t.Fatal(err)
	}
	secret := "remote-only-" + hex.EncodeToString(secretBytes[:])
	var authorized, other atomic.Int32
	body := []byte("bytes behind the remote runtime's credential")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			other.Add(1)
			http.Error(w, "credential required", http.StatusUnauthorized)
			return
		}
		authorized.Add(1)
		w.Write(body)
	}))
	defer origin.Close()

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const mapped = "tenant-a"
	holder, err := holding.Open(holding.Config{Backend: &memoryStore{items: map[string][]byte{}}, Namespace: "oa-remote-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	stored := holder.Store(cwire.Subject{Account: mapped, Program: program}, "", cwire.Registration{Name: "hf", Kind: "bearer",
		Scope: cwire.Scope{Targets: []string{"127.0.0.1"}, Consumers: []string{downloadserve.CredentialConsumer}}, Secret: []byte(secret)})
	if stored.Outcome != cwire.StoreOutcomeStored {
		t.Fatalf("store: %+v", stored)
	}
	// The remote host's rights: the mapped scope, as this runtime, may apply hf.
	decide := func(_ context.Context, subject cwire.Subject, action, resource string) (string, error) {
		if subject.Account == mapped && subject.Program == program && action == holding.ActionApply && resource == holding.ResourceFor("hf") {
			return "permitted", nil
		}
		return "not_granted", nil
	}
	executor := downloadserve.HTTPExecution{OnError: func(err error) { fmt.Fprintln(os.Stderr, "remote execution:", err) }, Credentials: scopedApplier{holder: holder, program: program, decide: decide}}
	p, err := provider.OpenWithExecutor(filepath.Join(t.TempDir(), "remote-runtime"), "remote-runtime-owner", executor)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executed := make(chan error, 1)
	go func() { executed <- p.Execute(ctx) }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	authorize := func(_ context.Context, peer remote.Peer) (string, error) {
		if peer.Key == clientID {
			return mapped, nil
		}
		return "", errors.New("unmapped key")
	}
	server := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}}, ClientCAs: roots}
	served := make(chan error, 1)
	go func() {
		served <- (remote.Server{TLS: server, Handler: p.RemoteHandler(authorize, nil), Timeout: 5 * time.Second, MaxFrame: provider.MaxFrameBytes, MaxConnections: 8}).Serve(ctx, listener)
	}()
	fmt.Printf("READY %s %s sha256:%x %d\n", listener.Addr(), origin.URL, sha256.Sum256(body), len(body))
	// The parent closes stdin once it has searched its own process's evidence.
	io.Copy(io.Discard, os.Stdin)
	fmt.Printf("ORIGIN %d %d\n", authorized.Load(), other.Load())
	fmt.Printf("SECRET %s\n", secret)
	cancel()
	<-served
	<-executed
	p.CloseInventory()
}

// base64Run finds base64 text long enough to hold a request.
var base64Run = regexp.MustCompile(`[A-Za-z0-9+/]{16,}={0,2}`)

// recordingTransport keeps every frame the local runtime exchanged with the
// remote job service, in both directions.
type recordingTransport struct {
	inner  api.FrameExchanger
	mu     sync.Mutex
	frames [][]byte
}

func (r *recordingTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	reply, err := r.inner.ExchangeFrame(frame)
	r.mu.Lock()
	r.frames = append(r.frames, bytes.Clone(frame), bytes.Clone(reply))
	r.mu.Unlock()
	return reply, err
}

// A local runtime delegates a download naming a credential to a remote runtime
// in another process, by name [DL-K3]. The remote applies its own credential
// in the scope its host maps from the local runtime's client certificate; the
// origin receives the remote's header and the bytes arrive locally, verified.
// A name the remote scope does not hold is refused at the remote admission,
// and the local operation ends with that reason and cause credential. The
// secret appears in no frame the local runtime exchanged and in no file of its
// store; the local runtime has no applier at all.
func TestRemoteDelegationAppliesTheRemoteCredential(t *testing.T) {
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestRemoteCredentialRuntimeChild$", "-test.v")
	child.Env = append(os.Environ(), remoteCredentialChildDir+"="+dir)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); child.Process.Kill(); child.Wait() })
	lines := bufio.NewScanner(stdout)
	next := func(prefix string) []string {
		t.Helper()
		for lines.Scan() {
			if fields := strings.Fields(lines.Text()); len(fields) > 0 && fields[0] == prefix {
				return fields[1:]
			}
		}
		t.Fatalf("child ended before %s: %v", prefix, lines.Err())
		return nil
	}
	ready := next("READY")
	address, originURL, digest := ready[0], ready[1], ready[2]
	var size int64
	fmt.Sscan(ready[3], &size)

	roots := x509.NewCertPool()
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil || !roots.AppendCertsFromPEM(caPEM) {
		t.Fatalf("ca: %v", err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "client.pem"), filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := remote.Client(address, &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: roots, ServerName: "runtime.test"}, 5*time.Second, provider.MaxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingTransport{inner: c}
	delegate := &downloadserve.RemoteJobDelegator{Name: "fixture", Transport: recorder}
	localRoot := filepath.Join(t.TempDir(), "local-runtime")
	local, err := provider.OpenWithExecutor(localRoot, "local-runtime-owner", downloadserve.DelegatedExecution{HTTPExecution: downloadserve.HTTPExecution{OnError: func(err error) { t.Log("local execution:", err) }}, Delegators: []download.Delegator{delegate}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	executed := make(chan error, 1)
	go func() { executed <- local.Execute(ctx) }()
	t.Cleanup(func() { cancel(); <-executed; local.CloseInventory() })

	app := local.Bind("local-app")
	window, err := app.GetHistoryWindow()
	if err != nil {
		t.Fatal(err)
	}
	submit := func(key, credential, digest string) api.RequestIdentity {
		t.Helper()
		spec := request.Encode(&request.Request{Artifact: request.Artifact{Digest: digest, Size: size}, Sources: []request.Source{{Scheme: "http", Locator: originURL + "/" + key, Credential: credential}}})
		id := api.RequestIdentity{Key: key, HistoryEpoch: window.HistoryEpoch}
		result, err := app.Submit(api.Submission{Identity: id, Kind: "download", Spec: spec,
			RequiredGuarantees: []string{string(download.CapRecoverableSubmission), downloadserve.CredentialGuarantee}})
		if err != nil || result.Outcome != "accepted" {
			t.Fatalf("local submit %s: %+v %v", key, result, err)
		}
		return id
	}
	ended := func(id api.RequestIdentity) *api.OperationSnapshot {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			observed, err := local.BindOperations("local-app").ObserveWork(id)
			if err != nil || observed.Snapshot == nil {
				t.Fatalf("observe %s: %+v %v", id.Key, observed, err)
			}
			if s := observed.Snapshot; s.State == "complete" || s.State == "failed" || s.State == "cancelled" {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not end: %+v", id.Key, observed.Snapshot)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	gated := submit("gated", "hf", digest)
	if s := ended(gated); s.State != "complete" {
		t.Fatalf("delegated download: %+v %+v", s, s.Failure)
	}
	var result []byte
	for {
		read, err := local.BindOperations("local-app").ReadResult(gated, int64(len(result)), provider.MaxResultBytes)
		if err != nil || read.Outcome != "data" {
			t.Fatalf("local result: %+v %v", read, err)
		}
		result = append(result, read.Chunk.Data...)
		if read.Chunk.EOF {
			break
		}
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(result)) != digest {
		t.Fatal("local result differs from the origin bytes")
	}

	// Another artifact: bytes this store already proved are never delegated again.
	unknown := submit("unknown-name", "not-registered", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("another artifact"))))
	s := ended(unknown)
	if s.State != "failed" || s.Failure == nil || s.Failure.Cause != api.FailureCauseCredential || s.Failure.Classification != "permanent" || s.Failure.Message != "download attempt failed: credential:unknown:not-registered" {
		t.Fatalf("remote admission refusal: %+v %+v", s, s.Failure)
	}

	// Everything the local runtime exchanged and stored, before the secret is known.
	recorder.mu.Lock()
	exchanged := bytes.Join(recorder.frames, []byte{0})
	recorder.mu.Unlock()
	// Opaque request bytes travel base64-encoded; search them decoded as well.
	for _, run := range base64Run.FindAll(exchanged, -1) {
		if decoded, err := base64.StdEncoding.DecodeString(string(run)); err == nil {
			exchanged = append(append(exchanged, 0), decoded...)
		}
	}
	if !bytes.Contains(exchanged, []byte(`"credential":"hf"`)) && !bytes.Contains(exchanged, []byte(`"credential": "hf"`)) {
		t.Fatal("the credential name did not cross to the remote")
	}
	var stored [][]byte
	err = filepath.WalkDir(localRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		stored = append(stored, data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	stdin.Close()
	origin := next("ORIGIN")
	secret := next("SECRET")[0]
	if origin[0] == "0" || origin[1] != "0" {
		t.Fatalf("origin authorized %s and refused %s requests", origin[0], origin[1])
	}
	for _, needle := range [][]byte{[]byte(secret), []byte(base64.StdEncoding.EncodeToString([]byte(secret)))} {
		if bytes.Contains(exchanged, needle) {
			t.Fatal("the remote secret crossed to the local runtime")
		}
		for _, data := range stored {
			if bytes.Contains(data, needle) {
				t.Fatal("the local store holds the remote secret")
			}
		}
		if strings.Contains(strings.Join(os.Environ(), "\n"), string(needle)) {
			t.Fatal("the local environment holds the remote secret")
		}
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("remote runtime: %v", err)
	}
	t.Logf("PASS remote runtime applied its own credential by name (%s authorized origin requests); %d frames and the local store carry no secret; unknown name refused at remote admission", origin[0], len(recorder.frames))
}
