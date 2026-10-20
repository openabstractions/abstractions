package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/placetest"
	"github.com/openabstractions/abstraction-identity/remote"
)

func pairingStateFixture(t *testing.T) *runtimePairingState {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := privatedir.Ensure(root); err != nil {
		t.Fatal(err)
	}
	s, err := createRuntimePairingState(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func pairingStatePeer(t *testing.T) (*x509.Certificate, [32]byte) {
	t.Helper()
	_, der, err := newPairingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, sha256.Sum256(der)
}
func pairingStateRead(t *testing.T, s *runtimePairingState) (uint64, map[string]pairingPeerRecord) {
	t.Helper()
	revision, peers, err := s.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return revision, peers
}
func pairingStateAdmission(cert *x509.Certificate) remote.Peer {
	return remote.Peer{Certificate: cert, Key: sha256.Sum256(cert.RawSubjectPublicKeyInfo)}
}

func TestPairingStateOrdinaryParentAndRetainedIdentity(t *testing.T) {
	ctx := context.Background()
	parent := filepath.Join(t.TempDir(), "ordinary-runtime-state")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	s, err := createRuntimePairingState(ctx, parent)
	if err != nil {
		t.Fatalf("ordinary inherited runtime parent: %v", err)
	}
	if err := privatedir.CheckDir(s.root); err != nil {
		t.Fatal(err)
	}
	if err := privatedir.CheckFile(s.path); err != nil {
		t.Fatal(err)
	}
	if _, err = loadRuntimePairingState(ctx, parent); err != nil {
		t.Fatal(err)
	}
	old := parent + ".old"
	if err = os.Rename(parent, old); err != nil {
		if runtime.GOOS == "windows" && os.IsPermission(err) {
			t.Log("Windows native CAS owner prevented moving the held parent; no completed replacement is claimed")
			if _, _, readErr := s.identity(ctx); readErr != nil {
				t.Fatal(readErr)
			}
			return
		}
		t.Fatal(err)
	}
	if err = os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.identity(ctx); err == nil {
		t.Fatal("replacement parent accepted")
	}
}

func TestPairingStateIdentityReuseAndRefusals(t *testing.T) {
	s := pairingStateFixture(t)
	ctx := context.Background()
	first, _, err := s.identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadRuntimePairingState(ctx, filepath.Dir(s.root))
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := loaded.identity(ctx)
	if err != nil || !bytes.Equal(first.Certificate[0], again.Certificate[0]) {
		t.Fatalf("identity changed: %v", err)
	}
	if _, err = createRuntimePairingState(ctx, filepath.Dir(s.root)); err == nil {
		t.Fatal("existing identity silently rotated")
	}
	for _, mode := range []string{"missing", "replace", "in-place-corrupt", "wrong-key", "wrong-account", "unknown-field", "link", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			s := pairingStateFixture(t)
			data, err := os.ReadFile(s.path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				err = os.Remove(s.path)
			case "replace":
				other := s.path + ".new"
				err = os.WriteFile(other, data, 0600)
				if err == nil {
					err = os.Remove(s.path)
				}
				if err == nil {
					err = os.Rename(other, s.path)
				}
			case "link":
				target := s.root + ".saved"
				err = os.Rename(s.root, target)
				if err == nil {
					if runtime.GOOS == "windows" {
						err = placetest.Junction(s.root, target)
					} else {
						err = placetest.FolderSymlink(s.root, target)
					}
				}
			case "hardlink":
				err = os.Link(s.path, s.path+".alias")
			default:
				if mode == "in-place-corrupt" {
					data = []byte("broken")
				} else {
					var d pairingDocument
					if err = json.Unmarshal(data, &d); err != nil {
						t.Fatal(err)
					}
					if mode == "wrong-key" {
						d.PrivateKey, _, err = newPairingIdentity()
					}
					if mode == "wrong-account" {
						d.Account = "foreign"
					}
					data, _ = json.Marshal(d)
					if mode == "unknown-field" {
						data = append(data[:len(data)-1], []byte(`,"unknown":true}`)...)
					}
				}
				if err == nil {
					err = os.WriteFile(s.path, data, 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.identity(ctx); err == nil {
				t.Fatal("changed identity accepted")
			}
			if mode == "wrong-key" || mode == "wrong-account" || mode == "unknown-field" || mode == "in-place-corrupt" || mode == "missing" || mode == "link" || mode == "hardlink" {
				if _, err = loadRuntimePairingState(ctx, filepath.Dir(s.root)); err == nil {
					t.Fatal("invalid initial state accepted")
				}
			}
		})
	}
}

func TestPairingStateLifecycleRequiresTrustedReconciliation(t *testing.T) {
	ctx := context.Background()
	s := pairingStateFixture(t)
	cert, transcript := pairingStatePeer(t)
	peer := pairingStateAdmission(cert)
	key := pairingKey(cert)
	rev, _ := pairingStateRead(t, s)
	rev, err := s.stage(ctx, rev, cert, transcript, "same name", "address hint")
	if err != nil {
		t.Fatal(err)
	}
	if s.admit(peer) == nil {
		t.Fatal("inactive pin admitted")
	}
	// A crash/reopen before the Rights/native reconciliation remains inactive.
	reopened, err := loadRuntimePairingState(ctx, filepath.Dir(s.root))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.admit(peer) == nil {
		t.Fatal("crash activated peer")
	}
	if _, err = s.activate(ctx, rev, key, nil); err == nil {
		t.Fatal("missing authority closure accepted")
	}
	if _, err = s.activate(ctx, rev, key, func(context.Context, pairingPeerRecord) (string, error) {
		return "", errors.New("native/rights denied")
	}); err == nil {
		t.Fatal("denied closure activated")
	}
	current, _ := pairingStateRead(t, s)
	if current != rev {
		t.Fatal("denial changed revision")
	}
	ctxCancelled, cancel := context.WithCancel(ctx)
	cancel()
	var invoked bool
	if _, err = s.activate(ctxCancelled, rev, key, func(context.Context, pairingPeerRecord) (string, error) { invoked = true; return "rights-1", nil }); err == nil || invoked {
		t.Fatal("cancelled activation reached authority")
	}
	if _, err = s.activate(ctx, rev-1, key, func(context.Context, pairingPeerRecord) (string, error) { invoked = true; return "rights-1", nil }); !errors.Is(err, errPairingRevision) || invoked {
		t.Fatal("stale activation reached authority")
	}
	// Conditional Rights may have committed while pin CAS was canceled. A
	// subsequent trusted closure re-observes that exact result, without replay.
	cancelled, cancelCommit := context.WithCancel(ctx)
	rightsObserved := 0
	_, err = s.activate(cancelled, rev, key, func(_ context.Context, p pairingPeerRecord) (string, error) {
		rightsObserved++
		if p.Transcript != transcript {
			t.Fatal("wrong transcript")
		}
		cancelCommit()
		return "rights-1", nil
	})
	if err == nil || s.admit(peer) == nil {
		t.Fatal("failed pin CAS admitted")
	}
	rev, err = s.activate(ctx, rev, key, func(_ context.Context, p pairingPeerRecord) (string, error) {
		if rightsObserved != 1 || p.Transcript != transcript {
			t.Fatal("lost-reply reconciliation")
		}
		return "rights-1", nil
	})
	if err != nil || s.admit(peer) != nil {
		t.Fatalf("activation: %v", err)
	}
	// Re-observe a lost successful response before deciding another operation.
	observed, peers := pairingStateRead(t, s)
	if observed != rev || peers[key].RightsEffect != "rights-1" {
		t.Fatal("activation not durable")
	}
	if _, err = s.activate(ctx, rev, key, func(context.Context, pairingPeerRecord) (string, error) { t.Fatal("effect replay"); return "", nil }); err == nil {
		t.Fatal("active replay allowed")
	}
	rev, err = s.remove(ctx, rev, key, func() error { return nil })
	if err != nil || s.admit(peer) == nil {
		t.Fatalf("removing still admits: %v", err)
	}
	identityBefore, _ := os.Stat(s.path)
	same, err := s.remove(ctx, rev, key, func() error { return nil })
	identityAfter, _ := os.Stat(s.path)
	if err != nil || same != rev || !os.SameFile(identityBefore, identityAfter) {
		t.Fatal("removal no-op rewrote state")
	}
	if _, err = s.stage(ctx, rev, cert, transcript, "same name", ""); err == nil {
		t.Fatal("re-pair before cleanup")
	}
	if _, err = s.finishRemoval(ctx, rev, key, func(context.Context, pairingPeerRecord) error { return errors.New("cleanup unknown") }); err == nil || s.admit(peer) == nil {
		t.Fatal("cleanup failure enabled peer")
	}
	rev, err = s.finishRemoval(ctx, rev, key, func(_ context.Context, p pairingPeerRecord) error {
		if p.State != "removing" || p.RightsEffect != "rights-1" {
			t.Fatal("cleanup evidence")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.stage(ctx, rev, cert, transcript, "same name", "")
	if err != nil || s.admit(peer) == nil {
		t.Fatalf("empty re-pair: %v", err)
	}
	_, peers = pairingStateRead(t, s)
	if peers[key].RightsEffect != "" || peers[key].State != "inactive" {
		t.Fatal("retained grants")
	}
}

func TestPairingStateSameNameDifferentKeysAndBound(t *testing.T) {
	s := pairingStateFixture(t)
	ctx := context.Background()
	rev, _ := pairingStateRead(t, s)
	for i := 0; i < 2; i++ {
		cert, transcript := pairingStatePeer(t)
		var err error
		rev, err = s.stage(ctx, rev, cert, transcript, "same label", "same hint")
		if err != nil {
			t.Fatal(err)
		}
	}
	_, peers := pairingStateRead(t, s)
	if len(peers) != 2 {
		t.Fatal("label conflated keys")
	}
	cert, tr := pairingStatePeer(t)
	if _, err := s.stage(ctx, rev, cert, tr, string(make([]byte, 513)), ""); err == nil {
		t.Fatal("unbounded hint")
	}
	// Oversized input is rejected by the existing CAS owner, leaving the
	// retained identity intact (this is not a second storage algorithm).
	if err := cas.ChangeLimitContext(ctx, s.path, pairingStateLimit, func([]byte) ([]byte, error) { return make([]byte, pairingStateLimit+1), nil }); !errors.Is(err, cas.ErrTooLarge) {
		t.Fatalf("bound: %v", err)
	}
	if _, _, err := s.identity(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPairingStateNormalMutualTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, b := pairingStateFixture(t), pairingStateFixture(t)
	ac, ar, err := a.identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bc, br, err := b.identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	activate := func(s *runtimePairingState, revision uint64, cert *x509.Certificate) {
		t.Helper()
		revision, err := s.stage(ctx, revision, cert, sha256.Sum256(cert.Raw), "peer", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.activate(ctx, revision, pairingKey(cert), func(context.Context, pairingPeerRecord) (string, error) {
			return "fixture actual rights observation", nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	activate(a, ar, bc.Leaf)
	activate(b, br, ac.Leaf)
	serverRoots, clientRoots := x509.NewCertPool(), x509.NewCertPool()
	serverRoots.AddCert(ac.Leaf)
	clientRoots.AddCert(bc.Leaf)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := remote.Server{TLS: &tls.Config{Certificates: []tls.Certificate{bc}, ClientCAs: serverRoots}, Admit: b.admit, Timeout: time.Second, Handler: func(_ context.Context, _ remote.Peer, frame []byte) ([]byte, error) {
		return append([]byte("paired:"), frame...), nil
	}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	defer func() { cancel(); <-done }()
	client, err := remote.Client(listener.Addr().String(), &tls.Config{Certificates: []tls.Certificate{ac}, RootCAs: clientRoots, ServerName: "oa-runtime"}, time.Second, 1024, a.admit)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ExchangeFrame([]byte("q"))
	if err != nil || string(got) != "paired:q" {
		t.Fatalf("actual constructor mutual TLS: %q %v", got, err)
	}
	b.mu.Lock()
	busyAt := time.Now()
	_, busyErr := client.ExchangeFrame([]byte("busy foreign handshake"))
	busyElapsed := time.Since(busyAt)
	b.mu.Unlock()
	if busyErr == nil || busyElapsed >= 500*time.Millisecond {
		t.Fatalf("busy foreign TLS admission: err=%v elapsed=%v", busyErr, busyElapsed)
	}
	rev, _ := pairingStateRead(t, b)
	if _, err = b.remove(ctx, rev, pairingKey(ac.Leaf), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExchangeFrame([]byte("must refuse")); err == nil {
		t.Fatal("removed pin admitted normal TLS")
	}
}

func TestPairingStateResetInventory(t *testing.T) {
	r, _ := resetFixture(t)
	paths, err := settingsPaths(r)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(r.Local, "openabstractions", "runtime-v1", filepath.FromSlash(pairingStateFile))
	if !slices.Contains(paths, want) {
		t.Fatal("key/pin state missing reset inventory")
	}
	roots, err := allSettingsRoots(r)
	if err != nil || !slices.Contains(roots, filepath.Join(r.Local, "openabstractions")) {
		t.Fatal("all reset missing owned root")
	}
	resetWrite(t, want, "fixture key and pins")
	if err = moveSettings(context.Background(), paths, "pairingfixture", os.Rename, bytes.NewBuffer(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(want); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reset retained identity/pins")
	}
}

func TestPairingStateBusyAdmissionAndConditionalFailure(t *testing.T) {
	s := pairingStateFixture(t)
	ctx := context.Background()
	cert, transcript := pairingStatePeer(t)
	peer := pairingStateAdmission(cert)
	rev, _ := pairingStateRead(t, s)
	rev, err := s.stage(ctx, rev, cert, transcript, "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.activate(ctx, rev, pairingKey(cert), func(context.Context, pairingPeerRecord) (string, error) {
			close(entered)
			<-release
			return "", errors.New("conditional Rights result refused")
		})
		done <- err
	}()
	<-entered
	admitted := make(chan error, 1)
	go func() { admitted <- s.admit(peer) }()
	select {
	case err := <-admitted:
		if err == nil {
			t.Error("busy edit admitted")
		}
	case <-time.After(time.Second):
		t.Error("admission waited behind native/Rights closure")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("conditional failure activated peer")
	}
	if s.admit(peer) == nil {
		t.Fatal("failed edit enabled pin")
	}
}

func TestPairingStateBusyReadsAndInvalidStateRemainDistinct(t *testing.T) {
	s := pairingStateFixture(t)
	ctx := context.Background()
	cert, _ := pairingStatePeer(t)
	s.mu.Lock()
	_, _, identityErr := s.identity(ctx)
	_, _, snapshotErr := s.snapshot(ctx)
	_, changeErr := s.change(ctx, 1, func(*pairingDocument) error { t.Error("busy edit ran"); return nil })
	admitErr := s.admit(pairingStateAdmission(cert))
	started := time.Now()
	_, rootsErr := s.clientRoots(ctx)
	_, _, outgoingErr := s.outgoingTLS(ctx, pairingKey(cert))
	_, minimumErr := s.minimum(ctx, pairingStateAdmission(cert))
	if time.Since(started) > 100*time.Millisecond {
		t.Error("foreign handshake waited for busy state")
	}
	s.mu.Unlock()
	for _, err := range []error{identityErr, snapshotErr, changeErr, admitErr, rootsErr, outgoingErr, minimumErr} {
		if !errors.Is(err, errPairingBusy) {
			t.Fatalf("actual lock contention: %v", err)
		}
	}
	if err := os.WriteFile(s.path, []byte("corrupt owned fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.snapshot(ctx)
	if err == nil || errors.Is(err, errPairingBusy) {
		t.Fatalf("invalid state became transient: %v", err)
	}
	if err := s.admit(pairingStateAdmission(cert)); err == nil || errors.Is(err, errPairingBusy) {
		t.Fatalf("invalid admission became transient: %v", err)
	}
}

func TestMachineRemoveRechecksAuthorityAtPinEffect(t *testing.T) {
	ctx := context.Background()
	s := pairingStateFixture(t)
	cert, transcript := pairingStatePeer(t)
	revision, _ := pairingStateRead(t, s)
	revision, err := s.stage(ctx, revision, cert, transcript, "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	revision, err = s.activate(ctx, revision, pairingKey(cert), func(context.Context, pairingPeerRecord) (string, error) { return "controlled existing grant", nil })
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	p := &runtimePairing{state: s}
	result := p.Remove(ctx, fmt.Sprint(revision), pairingKey(cert), func() error {
		checks++
		if checks >= 3 {
			return errors.New("native proof withdrawn before state effect")
		}
		return nil
	})
	if checks != 3 || result.Outcome != mwire.MachineRemoveOutcomeUnavailable {
		t.Fatalf("inner refusal: %d %v", checks, result.Outcome)
	}
	after, err := os.ReadFile(s.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused removal changed state bytes")
	}
	current, _ := pairingStateRead(t, s)
	if current != revision || s.admit(pairingStateAdmission(cert)) != nil {
		t.Fatal("refused removal changed revision or standing pin")
	}
	if _, err = s.remove(ctx, revision, pairingKey(cert), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if s.admit(pairingStateAdmission(cert)) == nil {
		t.Fatal("authorized removal retained pin")
	}
}

func TestPairingStateReplacementDuringTrustedClosure(t *testing.T) {
	for _, operation := range []string{"activate", "cleanup"} {
		for _, kind := range []string{"file", "root"} {
			t.Run(operation+"-"+kind, func(t *testing.T) {
				ctx := context.Background()
				s := pairingStateFixture(t)
				cert, tr := pairingStatePeer(t)
				key := pairingKey(cert)
				rev, _ := pairingStateRead(t, s)
				var err error
				rev, err = s.stage(ctx, rev, cert, tr, "peer", "")
				if err != nil {
					t.Fatal(err)
				}
				if operation == "cleanup" {
					rev, err = s.activate(ctx, rev, key, func(context.Context, pairingPeerRecord) (string, error) { return "observed rights", nil })
					if err != nil {
						t.Fatal(err)
					}
					rev, err = s.remove(ctx, rev, key, func() error { return nil })
					if err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(s.path)
				if err != nil {
					t.Fatal(err)
				}
				replace := func() error {
					if kind == "file" {
						other := s.path + ".new"
						if err := os.WriteFile(other, before, 0600); err != nil {
							return err
						}
						if err := os.Remove(s.path); err != nil {
							return err
						}
						return os.Rename(other, s.path)
					}
					other := s.root + ".new"
					if err := privatedir.Ensure(other); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(other, "state.json"), before, 0600); err != nil {
						return err
					}
					if err := os.Rename(s.root, s.root+".old"); err != nil {
						return err
					}
					return os.Rename(other, s.root)
				}
				var replaced, rootMoveBlocked bool
				if operation == "activate" {
					_, err = s.activate(ctx, rev, key, func(context.Context, pairingPeerRecord) (string, error) {
						if err := replace(); err != nil {
							if kind == "root" && runtime.GOOS == "windows" && os.IsPermission(err) {
								rootMoveBlocked = true
								return "", err
							}
							t.Errorf("actual fixture replacement: %v", err)
							return "", err
						}
						replaced = true
						return "observed rights", nil
					})
				} else {
					_, err = s.finishRemoval(ctx, rev, key, func(context.Context, pairingPeerRecord) error {
						if err := replace(); err != nil {
							if kind == "root" && runtime.GOOS == "windows" && os.IsPermission(err) {
								rootMoveBlocked = true
								return err
							}
							t.Errorf("actual fixture replacement: %v", err)
							return err
						}
						replaced = true
						return nil
					})
				}
				if (!replaced && !rootMoveBlocked) || err == nil {
					t.Fatalf("replacement committed: replaced=%v err=%v", replaced, err)
				}
				after, err := os.ReadFile(s.path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("CAS overwrote replacement: %v", err)
				}
				if s.admit(pairingStateAdmission(cert)) == nil {
					t.Fatal("changed object/anchor admitted")
				}
				if rootMoveBlocked {
					t.Log("Windows CAS lock prevented root rename; unchanged-state refusal only, not a completed root replacement")
				}
			})
		}
	}
}
