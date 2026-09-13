package remotejobs_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	submission := api.Submission{Identity: id, Kind: "download", Spec: []byte(`{"source":"fixture"}`), RequiredGuarantees: []string{provider.GuaranteeReconciliation, provider.GuaranteeServiceRestart}}
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
		if e != nil || page.Outcome != "page" || len(page.Snapshots) != 1 || !page.Complete || !reflect.DeepEqual(page.Snapshots[0].Receipt, want) {
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
	assertOne(inventory, receipt)
	// Equal certificate display names do not merge host-assigned caller namespaces.
	b, bops, binventory := clients(1)
	hidden, e := bops.ObserveWork(id)
	if e != nil || hidden.Outcome != "unknown" || hidden.Snapshot != nil {
		t.Fatalf("cross scope observation %+v %v", hidden, e)
	}
	other, e := b.Submit(submission)
	if e != nil || other.Outcome != "accepted" || other.Receipt == nil || other.Receipt.OperationId == receipt.OperationId {
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
