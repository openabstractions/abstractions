package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-identity/remote"
	watch "github.com/openabstractions/abstraction-watch/go"
)

func TestPairingOwnedCeremonyWaitsForReconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	makeSide := func(id, binding string) (*runtimePairing, *pairingOperation) {
		a, _ := firstUseBook(t)
		a.rights = testRights(t, t.TempDir())
		p := &runtimePairing{ctx: ctx, state: pairingStateFixture(t), rights: a.rights, asks: a,
			services: &runtimeInference{providers: &runtimeProviders{dir: t.TempDir(), changes: make(chan struct{})}},
			started:  time.Now(), ops: map[string]*pairingOperation{}, gate: watch.NewGate(watch.DefaultWaiters)}
		opCtx, opCancel := context.WithCancel(ctx)
		t.Cleanup(opCancel)
		op := &pairingOperation{id: id, ctx: opCtx, cancel: opCancel, expires: 120000,
			preparation: mwire.MachinePreparation{BindingName: binding, Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindNone}}
		p.ops[id], p.active = op, id
		return p, op
	}
	a, ao := makeSide("owned-a", "peer-b")
	b, bo := makeSide("owned-b", "peer-a")
	left, right := net.Pipe()
	t.Cleanup(func() { left.Close(); right.Close() })
	secret := sha256.Sum256([]byte("one accepted fixture invitation"))
	done := make(chan error, 2)
	// Hold the same owner lock reconciliation uses across both first reads.
	a.state.mu.Lock()
	b.state.mu.Lock()
	go func() { err := a.ceremony(ao, left, secret, true); a.end(ao, err); done <- err }()
	go func() { err := b.ceremony(bo, right, secret, false); b.end(bo, err); done <- err }()
	select {
	case err := <-done:
		a.state.mu.Unlock()
		b.state.mu.Unlock()
		t.Fatalf("owned ceremony refused reconciliation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	a.state.mu.Unlock()
	b.state.mu.Unlock()
	for _, side := range []struct {
		p  *runtimePairing
		op *pairingOperation
	}{{a, ao}, {b, bo}} {
		for {
			side.p.mu.Lock()
			question := side.op.question
			side.p.mu.Unlock()
			if question != "" {
				history, _, err := side.p.asks.book.ApplicationHistoryScoped()
				if err != nil || len(history) != 1 || history[0].Record.ID != question {
					t.Fatalf("one owned question: %+v %v", history, err)
				}
				if _, outcome, err := side.p.asks.book.AnswerApplication(question, "once"); err != nil || outcome != "answered" {
					t.Fatalf("native fixture answer: %s %v", outcome, err)
				}
				break
			}
			select {
			case err := <-done:
				t.Fatalf("ceremony ended before its question: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if ao.preview.Check != bo.preview.Check || ao.preview.Transcript != bo.preview.Transcript {
		t.Fatal("single-attempt native checks differ")
	}
	for _, p := range []*runtimePairing{a, b} {
		_, peers, err := p.state.snapshot(ctx)
		if err != nil || len(peers) != 1 {
			t.Fatalf("owned pins: %+v %v", peers, err)
		}
		for _, peer := range peers {
			if peer.State != "active" {
				t.Fatal("owned pin remained inactive")
			}
		}
	}
}

func TestPairingCancelledOwnedWaitLeavesStateUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &runtimePairing{ctx: context.Background(), state: pairingStateFixture(t), ops: map[string]*pairingOperation{}, gate: watch.NewGate(watch.DefaultWaiters)}
	op := &pairingOperation{id: "cancelled-owned-wait", ctx: ctx, cancel: cancel}
	p.ops[op.id], p.active = op, op.id
	before, err := os.ReadFile(p.state.path)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer right.Close()
	p.state.mu.Lock()
	done := make(chan error, 1)
	ended := make(chan struct{})
	go func() {
		err := p.ceremony(op, left, [32]byte{}, true)
		done <- err
		p.end(op, err)
		close(ended)
	}()
	select {
	case err := <-done:
		p.state.mu.Unlock()
		t.Fatalf("owned wait ended before cancellation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			p.state.mu.Unlock()
			t.Fatalf("cancelled wait: %v", err)
		}
	case <-time.After(time.Second):
		p.state.mu.Unlock()
		t.Fatal("cancelled operation waited for reconciliation release")
	}
	// The acquisition is cancelled. The worker still owes its existing owned
	// cleanup before publishing a terminal observation.
	select {
	case <-ended:
		p.state.mu.Unlock()
		t.Fatal("cancelled terminal preceded owned cleanup")
	case <-time.After(50 * time.Millisecond):
	}
	p.state.mu.Unlock()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("cancelled worker did not finish after owned cleanup")
	}
	after, err := os.ReadFile(p.state.path)
	if err != nil || string(after) != string(before) || op.phase != mwire.MachinePhaseCancelled {
		t.Fatalf("cancelled wait changed state: phase=%s err=%v", op.phase, err)
	}
}

// The fixture uses the actual local Book/native answer and durable pin owner.
// The transport completion is supplied by the separate authenticated /2 owner.
func TestPairingCommittedRefreshPreservesPaired(t *testing.T) {
	for _, mode := range []string{"busy_refresh", "busy_stage", "clean_refresh", "changed_native_consent"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			state := pairingStateFixture(t)
			rights := testRights(t, t.TempDir())
			asks, book := firstUseBook(t)
			asks.rights = rights
			providers := &runtimeProviders{dir: t.TempDir(), changes: make(chan struct{})}
			var reports []error
			p := &runtimePairing{ctx: ctx, state: state, rights: rights, asks: asks, services: &runtimeInference{providers: providers}, report: func(err error) { reports = append(reports, err) }, started: time.Now(), ops: map[string]*pairingOperation{}, gate: watch.NewGate(watch.DefaultWaiters)}
			providers.setPairedHosts(p.pairedHosts)
			localTLS, _, err := state.identity(ctx)
			if err != nil {
				t.Fatal(err)
			}
			localCert, err := x509.ParseCertificate(localTLS.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			peerCert, transcript := pairingStatePeer(t)
			keys := remote.PairKeys{Role: 1, LocalKey: sha256.Sum256(localCert.RawSubjectPublicKeyInfo), PeerKey: sha256.Sum256(peerCert.RawSubjectPublicKeyInfo)}
			preparation := mwire.MachinePreparation{Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindNone, BindingName: "peer"}
			initiator, err := preparePairingProposal(preparation, keys)
			if err != nil {
				t.Fatal(err)
			}
			responder, err := preparePairingProposal(mwire.MachinePreparation{Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindNone, BindingName: "local"}, remote.PairKeys{Role: 2, LocalKey: keys.PeerKey, PeerKey: keys.LocalKey})
			if err != nil {
				t.Fatal(err)
			}
			view := remote.PairView{Role: 1, LocalKey: keys.LocalKey, PeerKey: keys.PeerKey, Transcript: transcript, Check: "123456", InitiatorProposal: initiator, ResponderProposal: responder}
			opCtx, opCancel := context.WithCancel(ctx)
			defer opCancel()
			op := &pairingOperation{id: "committed-refresh", ctx: opCtx, cancel: opCancel, preparation: preparation, expires: 120000}
			p.ops[op.id], p.active = op, op.id
			if err = p.show(op, view); err != nil {
				t.Fatal(err)
			}
			if _, outcome, err := book.AnswerApplication(op.question, "once"); err != nil || outcome != "answered" {
				t.Fatalf("native answer: %s %v", outcome, err)
			}
			if yes, err := p.confirm(op, view); err != nil || !yes {
				t.Fatalf("native confirmation: %v %v", yes, err)
			}
			revision, _ := pairingStateRead(t, state)
			candidate := remote.PairCandidate{Certificate: peerCert, View: view}
			if mode == "busy_stage" {
				state.mu.Lock()
				staged := make(chan struct {
					revision uint64
					err      error
				}, 1)
				go func() {
					v, e := state.stageCompleted(ctx, revision, candidate, *op.consent, p.currentOperation(op))
					staged <- struct {
						revision uint64
						err      error
					}{v, e}
				}()
				select {
				case result := <-staged:
					state.mu.Unlock()
					t.Fatalf("owned stage refused reconciliation: %v", result.err)
				case <-time.After(50 * time.Millisecond):
				}
				state.mu.Unlock()
				result := <-staged
				revision, err = result.revision, result.err
			} else {
				revision, err = state.stageCompleted(ctx, revision, candidate, *op.consent)
			}
			if err != nil {
				t.Fatal(err)
			}
			key := pairingKey(peerCert)
			calls := 0
			lockBusy := mode == "busy_refresh"
			providers.remotesChanged = func() error {
				calls++
				current, peers, err := state.snapshot(ctx)
				if err != nil || peers[key].State != "active" || current != revision+1 {
					t.Fatalf("refresh preceded durable activation: %d %+v %v", current, peers[key], err)
				}
				if lockBusy {
					// Reproduce a concurrent TLS client-roots read holding the pair mutex
					// only after activation committed, across the actual host aggregation.
					state.mu.Lock()
					defer state.mu.Unlock()
				}
				_, err = routerHosts(providers)
				return err
			}
			if mode == "changed_native_consent" {
				op.consent.TextHash = strings.Repeat("f", 64)
			}
			err = p.activate(op, key)
			p.end(op, err)
			current, peers, readErr := state.snapshot(ctx)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "changed_native_consent" {
				if err == nil || op.phase != mwire.MachinePhaseUncertain || peers[key].State != "inactive" || current != revision || calls != 0 {
					t.Fatalf("precommit failure changed authority: err=%v phase=%s peer=%+v revision=%d calls=%d", err, op.phase, peers[key], current, calls)
				}
				return
			}
			if err != nil || op.phase != mwire.MachinePhasePaired || op.reason != "" {
				t.Fatalf("durably active pair lost its terminal result: err=%v phase=%s reason=%s", err, op.phase, op.reason)
			}
			if current != revision+1 || op.revision != strconv.FormatUint(current, 10) || peers[key].State != "active" || calls != 1 {
				t.Fatalf("activation commit/revision: %d %+v calls=%d", current, peers[key], calls)
			}
			if mode == "busy_refresh" && (len(reports) != 1 || !errors.Is(reports[0], errPairingBusy)) {
				t.Fatalf("derived refresh failure unreported: %v", reports)
			}
			if mode == "clean_refresh" && len(reports) != 0 {
				t.Fatalf("clean refresh reported failure: %v", reports)
			}
			lockBusy = false
			if err := providers.pairedChanged(); err != nil || calls != 2 {
				t.Fatalf("existing refresh owner could not retry committed state: calls=%d err=%v", calls, err)
			}
			if err = p.activate(op, key); err == nil {
				t.Fatal("terminal activation replay committed twice")
			}
			after, _ := pairingStateRead(t, state)
			if after != current || calls != 2 {
				t.Fatal("activation replay changed state or refreshed again")
			}
		})
	}
}
