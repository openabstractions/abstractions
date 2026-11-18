package main

import (
	"context"
	"encoding/hex"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/remote"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	watch "github.com/openabstractions/abstraction-watch/go"
)

func pairingLoopbackAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitMachineOperationFinished(t *testing.T, ctx context.Context, control *client.MachineControl, id string) *mwire.MachineOperation {
	t.Helper()
	after := ""
	for {
		view, err := control.Observe(ctx, id, after, time.Second)
		if err != nil || view.Operation == nil {
			t.Fatalf("operation completion: %+v %v", view, err)
		}
		switch view.Operation.Phase {
		case mwire.MachinePhasePaired, mwire.MachinePhaseRefused, mwire.MachinePhaseExpired, mwire.MachinePhaseCancelled, mwire.MachinePhaseUncertain:
			return view.Operation
		}
		after = view.Cursor
	}
}

func TestMachineControlTerminalWaitsForOwnedCleanup(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		yes   bool
		phase mwire.MachinePhase
	}{
		{"paired", nil, false, mwire.MachinePhasePaired},
		{"refused", errPairingAgreement, false, mwire.MachinePhaseRefused},
		{"expired", context.DeadlineExceeded, false, mwire.MachinePhaseExpired},
		{"cancelled", context.Canceled, false, mwire.MachinePhaseCancelled},
		{"local_yes", context.Canceled, true, mwire.MachinePhaseUncertain},
		{"transport_uncertain", remote.ErrPairingUncertain, false, mwire.MachinePhaseUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			p := &runtimePairing{ctx: ctx, state: pairingStateFixture(t), epoch: strings.Repeat("e", 32), started: time.Now(), ops: map[string]*pairingOperation{}, gate: watch.NewGate(watch.DefaultWaiters), services: &runtimeInference{providers: &runtimeProviders{dir: t.TempDir()}}}
			defer func() { cancel(); p.workers.Wait() }()
			allow := func() error { return nil }
			request := mwire.MachineRequestIdentity{Epoch: p.epoch, ExpiresMs: 120000, Nonce: strings.Repeat("a", 32)}
			next := request
			next.Nonce = strings.Repeat("b", 32)
			preparation := mwire.MachinePreparation{Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindNone, BindingName: "cleanup-peer"}
			address := pairingLoopbackAddress(t)
			op, outcome := p.reserve(request, preparation, "offer", address, "")
			if outcome != mwire.MachineStartOutcomeStarted {
				t.Fatal(outcome)
			}
			op.phase, op.localYes = mwire.MachinePhaseReconciling, test.yes
			entered, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
			p.stopNormal = func() { close(entered); <-release }
			go func() { defer close(ended); p.end(op, test.err) }()
			defer func() { close(release); <-ended }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("cleanup was not entered")
			}
			before := p.Observe(ctx, op.id, "", 0, allow)
			if before.Operation == nil || before.Operation.Phase != mwire.MachinePhaseReconciling {
				t.Fatalf("terminal advertised during owned cleanup: %+v", before)
			}
			if blocked := p.StartOffer(ctx, next, address, preparation, allow); blocked.Outcome != mwire.MachineStartOutcomeExhausted {
				t.Fatalf("active attempt admitted a second start during cleanup: %+v", blocked)
			}
			// Let the existing stop owner complete, then read its terminal publication.
			release <- struct{}{}
			<-ended
			finished := p.Observe(ctx, op.id, before.Cursor, 1000, allow)
			if finished.Operation == nil || finished.Operation.Phase != test.phase || finished.Cursor == before.Cursor {
				t.Fatalf("finished operation: %+v", finished)
			}
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				t.Fatal(err)
			}
			held, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
			if err != nil {
				t.Fatal(err)
			}
			replayed := p.StartOffer(ctx, request, address, preparation, allow)
			if err = held.Close(); err != nil {
				t.Fatal(err)
			}
			if replayed.Outcome != mwire.MachineStartOutcomeExisting || replayed.Operation.Phase != test.phase {
				t.Fatalf("terminal replay reopened or changed the attempt: %+v", replayed)
			}
			started := p.StartOffer(ctx, next, address, preparation, allow)
			if started.Outcome != mwire.MachineStartOutcomeStarted {
				t.Fatalf("finished operation retained active slot: %+v", started)
			}
			p.Cancel(ctx, started.Operation.ID, allow)
		})
	}
}

func TestMachineControlDomainEffectReplay(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("native Program proof unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	options, _ := isolatedRuntime(t)
	stop := startInferenceRuntime(t, options)
	defer stop()
	control, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveMachineControl(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	page, err := control.List(ctx, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	identity := func() mwire.MachineRequestIdentity {
		t.Helper()
		nonce, e := randomPairingHex()
		if e != nil {
			t.Fatal(e)
		}
		return mwire.MachineRequestIdentity{Epoch: page.Epoch, ExpiresMs: page.RequestExpiresMs, Nonce: nonce}
	}
	preparation := mwire.MachinePreparation{Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindNone, BindingName: "replay-peer"}
	request := identity()
	for _, bad := range []string{"", strings.Repeat("x", 65), "127.0.0.1:1\n"} {
		if _, e := control.StartOffer(ctx, request, bad, preparation); e == nil {
			t.Fatalf("unbounded/control invitation address accepted: %q", bad)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 75), "oa2:\n"} {
		if _, e := control.Enter(ctx, request, bad, preparation); e == nil {
			t.Fatal("invalid code structure reached native operation")
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 81), "op\n"} {
		if _, e := control.Observe(ctx, bad, "", 0); e == nil {
			t.Fatal("invalid Observe identity structure accepted")
		}
		if _, e := control.Cancel(ctx, bad); e == nil {
			t.Fatal("invalid Cancel identity structure accepted")
		}
	}
	for _, bad := range []string{strings.Repeat("x", 161), "cursor\n"} {
		if _, e := control.Observe(ctx, "unknown", bad, 0); e == nil {
			t.Fatal("invalid O1 cursor structure accepted")
		}
		if _, e := control.List(ctx, bad, 64); e == nil {
			t.Fatal("invalid L1 cursor structure accepted")
		}
	}
	for _, bad := range []string{"", strings.Repeat("1", 33), "1\n"} {
		if _, e := control.Remove(ctx, bad, strings.Repeat("a", 64)); e == nil {
			t.Fatal("invalid removal revision structure accepted")
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 65), strings.Repeat("a", 63) + "\n"} {
		if _, e := control.Remove(ctx, "1", bad); e == nil {
			t.Fatal("invalid removal key structure accepted")
		}
	}
	address := pairingLoopbackAddress(t)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	first, err := control.StartOffer(ctx, request, address, preparation)
	if err != nil || first.Outcome != mwire.MachineStartOutcomeStarted || first.Operation == nil {
		t.Fatalf("offer: %+v %v", first, err)
	}
	for n := 0; n < 2; n++ {
		repeated, e := control.StartOffer(ctx, request, address, preparation)
		if e != nil || repeated.Outcome != mwire.MachineStartOutcomeExisting || repeated.Operation.ID != first.Operation.ID {
			t.Fatalf("offer replay: %+v %v", repeated, e)
		}
		abandoned, e := control.Cancel(ctx, first.Operation.ID)
		if e != nil || abandoned.Outcome != mwire.MachineCancelOutcomeUpdated {
			t.Fatalf("cancel replay: %+v %v", abandoned, e)
		}
	}
	waitMachineOperationFinished(t, ctx, control, first.Operation.ID)
	// A finished attempt has completed its owned cleanup; replay cannot bind again.
	var held net.Listener
	held, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatal("cancelled offer did not close its listener")
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	if repeated, e := control.StartOffer(ctx, request, address, preparation); e != nil || repeated.Outcome != mwire.MachineStartOutcomeExisting {
		t.Fatalf("ended offer replay: %+v %v", repeated, e)
	}
	held, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatal("offer replay opened another listener")
	}
	held.Close()
	changed := preparation
	changed.BindingName = "changed-peer"
	if result, e := control.StartOffer(ctx, request, address, changed); e != nil || result.Outcome != mwire.MachineStartOutcomeConflict {
		t.Fatalf("changed offer: %+v %v", result, e)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		raw, e := listener.Accept()
		if e == nil {
			e = raw.Close()
		}
		accepted <- e
	}()
	var secret [32]byte
	secret[0] = 1
	code, err := encodePairingInvitation(netip.MustParseAddrPort(listener.Addr().String()), secret)
	if err != nil {
		t.Fatal(err)
	}
	request = identity()
	entered, err := control.Enter(ctx, request, code, preparation)
	if err != nil || entered.Outcome != mwire.MachineStartOutcomeStarted || entered.Operation == nil {
		t.Fatalf("enter: %+v %v", entered, err)
	}
	select {
	case err = <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("first Enter never dialed")
	}
	// The same live fixture listener observes any accidental replay dial.
	for n := 0; n < 2; n++ {
		repeated, e := control.Enter(ctx, request, code, preparation)
		if e != nil || repeated.Outcome != mwire.MachineStartOutcomeExisting || repeated.Operation.ID != entered.Operation.ID {
			t.Fatalf("enter replay: %+v %v", repeated, e)
		}
	}
	if result, e := control.Enter(ctx, request, code, changed); e != nil || result.Outcome != mwire.MachineStartOutcomeConflict {
		t.Fatalf("changed enter: %+v %v", result, e)
	}
	if err = listener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if raw, e := listener.Accept(); e == nil {
		raw.Close()
		t.Fatal("Enter replay dialed again")
	} else if timeout, ok := e.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal(e)
	}
}

func TestMachineControlAdmissionReplayAndCursorRefusals(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("native Program proof unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	options, _ := isolatedRuntime(t)
	stop := startInferenceRuntime(t, options)
	defer stop()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	control, err := machine.ResolveMachineControl(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	application, err := machine.ResolveAsks(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	forged := askclient.ApplicationQuestion{RequestKey: "forged-pair", Key: asks.PairingKey, Slots: map[string]string{"peer": "fixture", "check": "123456", "binding": "peer", "given": "none", "received": "none", "code": "fixture", "until": "later"}}
	if answer, e := application.AskContext(ctx, forged); e != nil || answer.Outcome.String() != "forbidden" {
		t.Fatalf("external caller forged runtime pairing occurrence: %v %v", answer.Outcome, e)
	}
	page, err := control.List(ctx, "", 64)
	if err != nil || page.Outcome != mwire.MachineListOutcomePage {
		t.Fatal("initial list unavailable")
	}
	prepared := mwire.MachinePreparation{Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindNone, BindingName: "peer"}
	nonce, err := randomPairingHex()
	if err != nil {
		t.Fatal(err)
	}
	request := mwire.MachineRequestIdentity{Epoch: page.Epoch, ExpiresMs: page.RequestExpiresMs, Nonce: nonce}
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	address := occupied.Addr().String()
	for _, kind := range []string{"epoch", "expired", "future"} {
		bad := request
		switch kind {
		case "epoch":
			bad.Epoch = "00000000000000000000000000000000"
		case "expired":
			bad.ExpiresMs = 0
		case "future":
			bad.ExpiresMs += 120001
		}
		result, e := control.StartOffer(ctx, bad, address, prepared)
		if e != nil || result.Outcome != mwire.MachineStartOutcomeExpired {
			t.Fatalf("%s before any bind: %v %v", kind, result.Outcome, e)
		}
	}
	first, err := control.StartOffer(ctx, request, address, prepared)
	if err != nil || first.Outcome != mwire.MachineStartOutcomeUnavailable || first.Operation == nil {
		t.Fatalf("occupied fixture refusal: %v %v", first.Outcome, err)
	}
	if err = occupied.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := control.StartOffer(ctx, request, address, prepared)
	if err != nil || replay.Outcome != mwire.MachineStartOutcomeExisting || replay.Operation.ID != first.Operation.ID || replay.Code != "" {
		t.Fatalf("exact failed operation replay retried: %v %v", replay.Outcome, err)
	}
	changed := prepared
	changed.BindingName = "other"
	conflict, err := control.StartOffer(ctx, request, address, changed)
	if err != nil || conflict.Outcome != mwire.MachineStartOutcomeConflict {
		t.Fatal("same operation arguments were replaced")
	}
	gap, err := control.Observe(ctx, first.Operation.ID, "another-epoch:cursor", 0)
	if err != nil || gap.Outcome != mwire.MachineObserveOutcomeGap {
		t.Fatal("foreign observation cursor accepted")
	}
	unknown, err := control.Observe(ctx, page.Epoch+".11111111111111111111111111111111", "", 0)
	if err != nil || unknown.Outcome != mwire.MachineObserveOutcomeUnknown {
		t.Fatal("unknown operation fabricated")
	}
	badPage, err := control.List(ctx, "another-epoch:cursor", 64)
	if err != nil || badPage.Outcome != mwire.MachineListOutcomeGap {
		t.Fatal("foreign list cursor accepted")
	}
	if listening(address) {
		t.Fatal("failed operation replay opened a listener")
	}
}

// This receiver uses generated native local IPC, actual runtime Book answers,
// the real two-stream /2 ceremony and the ordinary authenticated service path.
// The test operator supplies controlled answers; no person dialog is opened.
func TestMachineControlNativePairAndRemove(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("native Program proof unavailable")
	}
	for _, mode := range []string{"identity_only", "one_direction", "native_no", "request_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			offered := mode != "identity_only" && mode != "request_mismatch"
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
			defer cancel()
			a, _ := isolatedRuntime(t)
			b, _ := isolatedRuntime(t)
			stopA := startInferenceRuntime(t, a)
			defer func() { stopA() }()
			stopB := startInferenceRuntime(t, b)
			defer func() { stopB() }()
			machines := []*client.Machine{client.NewVerified(a.endpoint, runtimeExpectation(t)), client.NewVerified(b.endpoint, runtimeExpectation(t))}
			controls := make([]*client.MachineControl, 2)
			requests := make([]mwire.MachineRequestIdentity, 2)
			for i, m := range machines {
				var err error
				controls[i], err = m.ResolveMachineControl(ctx, local)
				if err != nil {
					t.Fatal(err)
				}
				page, err := controls[i].List(ctx, "", 64)
				// Native admission may observe the provider owner's startup
				// reconciliation holding pairing state. Wait for the first page.
				for err == nil && page.Outcome == mwire.MachineListOutcomeUnavailable && ctx.Err() == nil {
					select {
					case <-ctx.Done():
					case <-time.After(10 * time.Millisecond):
					}
					page, err = controls[i].List(ctx, "", 64)
				}
				if err != nil || page.Outcome != mwire.MachineListOutcomePage {
					t.Fatalf("initial local list: %v %v", page.Outcome, err)
				}
				nonce, err := randomPairingHex()
				if err != nil {
					t.Fatal(err)
				}
				requests[i] = mwire.MachineRequestIdentity{Epoch: page.Epoch, ExpiresMs: page.RequestExpiresMs, Nonce: nonce}
			}
			preparations := []mwire.MachinePreparation{{Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindNone, BindingName: "machine-b"}, {Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindNone, BindingName: "machine-a"}}
			if offered {
				upstream, _ := fakeOllama(t)
				if out, err := runInference(t, "host", "add", "ollama", "--base", upstream.URL, "--endpoint", b.endpoint, "--timeout", "30s"); err != nil {
					t.Fatalf("owned registered host: %v %s", err, out)
				}
				preparations[0].Request = mwire.MachineScopeKindOneHostInference
				preparations[1].Offer = mwire.MachineScopeKindOneHostInference
				preparations[1].OfferHost = "ollama"
				preparations[1].NormalServiceAddress = pairingLoopbackAddress(t)
			}
			if mode == "request_mismatch" {
				preparations[0].Request = mwire.MachineScopeKindOneHostInference
			}
			offer, err := controls[1].StartOffer(ctx, requests[1], pairingLoopbackAddress(t), preparations[1])
			if err != nil || offer.Outcome != mwire.MachineStartOutcomeStarted || offer.Code == "" {
				t.Fatalf("start offer: %v %v", offer.Outcome, err)
			}
			entered, err := controls[0].Enter(ctx, requests[0], offer.Code, preparations[0])
			if err != nil || entered.Outcome != mwire.MachineStartOutcomeStarted {
				t.Fatalf("enter: %v %v", entered.Outcome, err)
			}
			ids := []string{entered.Operation.ID, offer.Operation.ID}
			if mode == "request_mismatch" {
				for i := range controls {
					for {
						v, e := controls[i].Observe(ctx, ids[i], "", 0)
						if e != nil {
							t.Fatal(e)
						}
						if v.Operation.Reason != "" {
							if v.Operation.QuestionID != "" {
								t.Fatal("mismatched directions reached native consent")
							}
							break
						}
						select {
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						case <-time.After(10 * time.Millisecond):
						}
					}
					page, e := controls[i].List(ctx, "", 64)
					if e != nil || len(page.Machines) != 0 {
						t.Fatal("mismatched directions produced a pin")
					}
				}
				return
			}
			views := make([]mwire.MachineObservation, 2)
			for i := range controls {
				for {
					views[i], err = controls[i].Observe(ctx, ids[i], "", 0)
					if err != nil {
						t.Fatal(err)
					}
					if views[i].Operation != nil && views[i].Operation.Phase == mwire.MachinePhaseAwaitingPerson {
						break
					}
					if views[i].Operation != nil && views[i].Operation.Reason != "" {
						t.Fatalf("before native consent: %s", views[i].Operation.Reason)
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			if views[0].Operation.Agreement.Check != views[1].Operation.Agreement.Check || views[0].Operation.Agreement.Transcript != views[1].Operation.Agreement.Transcript {
				t.Fatal("two native previews disagree")
			}
			for i, m := range machines {
				questions, err := m.ResolveAsksOperator(ctx, local)
				if err != nil {
					t.Fatal(err)
				}
				page, err := questions.ListQuestionsContext(ctx, "", 64)
				if err != nil || len(page.Records) != 1 || page.Records[0].ID != views[i].Operation.QuestionID {
					t.Fatalf("single-attempt native question: %+v %v", page, err)
				}
				option := "once"
				if mode == "native_no" && i == 1 {
					option = "refuse"
				}
				answer, err := questions.AnswerQuestionIfPendingContext(ctx, views[i].Operation.QuestionID, option)
				if err != nil || answer.Outcome.String() != "answered" {
					t.Fatalf("actual local operator answer: %v %v", answer.Outcome, err)
				}
			}
			if mode == "native_no" {
				for i := range controls {
					for {
						v, e := controls[i].Observe(ctx, ids[i], "", 0)
						if e != nil {
							t.Fatal(e)
						}
						if v.Operation.Reason != "" {
							break
						}
						select {
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						case <-time.After(10 * time.Millisecond):
						}
					}
					page, e := controls[i].List(ctx, "", 64)
					if e != nil {
						t.Fatal(e)
					}
					for _, peer := range page.Machines {
						if peer.State == "active" {
							t.Fatal("No before both accepts activated a pin")
						}
					}
				}
				page, e := controls[0].List(ctx, "", 64)
				if e != nil || len(page.Machines) != 1 || page.Machines[0].State != "uncertain" {
					t.Fatalf("lost local Yes not retained: %+v %v", page, e)
				}
				removed, e := controls[0].Remove(ctx, page.Revision, page.Machines[0].PeerKey)
				if e != nil || removed.Outcome != mwire.MachineRemoveOutcomeRemoved {
					t.Fatalf("explicit uncertain cleanup: %v %v", removed.Outcome, e)
				}
				if listening(preparations[1].NormalServiceAddress) {
					t.Fatal("refused preparation retained transient receiving listener")
				}
				return
			}
			for i := range controls {
				for {
					v, err := controls[i].Observe(ctx, ids[i], "", 0)
					if err != nil {
						t.Fatal(err)
					}
					if v.Operation.Phase == mwire.MachinePhasePaired {
						break
					}
					if v.Operation.Reason != "" {
						t.Fatalf("after native consent: %s", v.Operation.Reason)
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			root, err := credentialsState(a)
			if err != nil {
				t.Fatal(err)
			}
			for _, options := range []runtimeFlags{a, b} {
				peerRoot, e := credentialsState(options)
				if e != nil {
					t.Fatal(e)
				}
				peerState, e := loadRuntimePairingState(ctx, peerRoot)
				if e != nil {
					t.Fatal(e)
				}
				_, pins, e := peerState.snapshot(ctx)
				if e != nil || len(pins) != 1 {
					t.Fatalf("single-attempt pins: %+v %v", pins, e)
				}
				for _, pin := range pins {
					if pin.State != "active" {
						t.Fatal("single-attempt pin remained inactive")
					}
				}
			}
			state, err := loadRuntimePairingState(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			_, peers, err := state.snapshot(ctx)
			if err != nil || len(peers) != 1 {
				t.Fatalf("active exact pin: %v", err)
			}
			var peerKey string
			var retainedNamed *routerwire.RouterClient
			for key, peer := range peers {
				peerKey = key
				if peer.State != "active" {
					t.Fatal("inactive candidate admitted")
				}
				if peer.ActivationKind != "native_no_grant" || peer.RightsEffect != "" {
					t.Fatal("empty receiving direction wrote a grant")
				}
			}
			if offered {
				binding, e := machines[0].ResolveServiceOnMachine(ctx, "abstraction.router/router@1", client.Requirements{Scope: client.ScopeRemote}, "machine-b")
				if e != nil {
					t.Fatalf("named remote Router: %v", e)
				}
				if binding.Reference().Provider != "machine-b" || binding.Reference().Scope != fwire.ScopeRemote {
					t.Fatalf("named reference %+v", binding.Reference())
				}
				if _, e = routerwire.NewRouterClient(binding.Transport()).Modalities("", 64); e != nil {
					t.Fatalf("named remote Modalities: %v", e)
				}
				retainedNamed = routerwire.NewRouterClient(binding.Transport())
				for _, contract := range []string{"abstraction.inference/chat@1", "abstraction.inference/embed@1", "abstraction.facade/messages@1"} {
					selected, e := machines[0].ResolveServiceOnMachine(ctx, contract, client.Requirements{}, "machine-b")
					if e != nil || selected.Reference().Provider != "machine-b" {
						t.Fatalf("named %s: %+v %v", contract, selected.Reference(), e)
					}
				}
				if _, e := machines[0].ResolveServiceOnMachine(ctx, "abstraction.router/router@1", client.Requirements{}, "missing-machine"); e == nil {
					t.Fatal("missing machine fell back")
				}
				for i, control := range controls {
					page, e := control.List(ctx, "", 64)
					if e != nil || len(page.Machines) != 1 || page.Machines[0].BindingName != preparations[i].BindingName {
						t.Fatalf("public peer-book name: %+v %v", page, e)
					}
					want := ""
					if preparations[i].Offer == mwire.MachineScopeKindOneHostInference {
						want = preparations[i].NormalServiceAddress
					}
					if page.LocalServiceAddress != want {
						t.Fatalf("actual current receiver: got %q want %q", page.LocalServiceAddress, want)
					}
				}
				ownCert, _, e := state.identity(ctx)
				if e != nil {
					t.Fatal(e)
				}
				operator, e := machines[1].ResolveRightsOperator(ctx, local)
				if e != nil {
					t.Fatal(e)
				}
				keyA := pairingKey(ownCert.Leaf)
				for _, rule := range []pairingProposalRule{{remoteServeAction, "account", true}} {
					read, e := operator.ReadRuleContext(ctx, rwire.Subject{Account: probeSelf().Account, Program: "remote:" + keyA}, rule.Action, rule.Resource)
					if e != nil || read.Record == nil || !read.Record.Rule.Permit {
						t.Fatalf("actual conditional receiving rule: %+v %v", read, e)
					}
				}
				config, address, err := state.outgoingTLS(ctx, peerKey)
				if err != nil {
					t.Fatal(err)
				}
				transport, err := remote.Client(address, config, 5*time.Second, 0, func(p remote.Peer) error {
					if hex.EncodeToString(p.Key[:]) != peerKey {
						return remote.ErrTrust
					}
					return state.admitContext(t.Context(), p)
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = fwire.NewEndpointClient(transport).Describe(); err != nil {
					t.Fatalf("normal authenticated Describe: %v", err)
				}
				if _, err = routerwire.NewRouterClient(transport).Hosts(false); err != nil {
					t.Fatalf("normal authenticated router inventory: %v", err)
				}
			}
			if mode == "one_direction" {
				// No declaration changes trigger the restored requester view.
				stopA()
				stopB()
				stopB = startInferenceRuntime(t, b)
				stopA = startInferenceRuntime(t, a)
				for i, m := range machines {
					controls[i], err = m.ResolveMachineControl(ctx, local)
					if err != nil {
						t.Fatal(err)
					}
				}
				assertHosts := func(want bool) {
					t.Helper()
					routes, e := machines[0].ResolveRouter(ctx, local)
					if e != nil {
						t.Fatal(e)
					}
					v, e := routes.HostsContext(ctx, false)
					if e != nil {
						t.Fatal(e)
					}
					found := false
					for _, h := range v.Hosts {
						found = found || h.Host == "machine-b"
					}
					if found != want {
						t.Fatalf("restored paired host=%v want=%v: %+v", found, want, v.Hosts)
					}
				}
				assertHosts(true)
				state, err = loadRuntimePairingState(ctx, root)
				if err != nil {
					t.Fatal(err)
				}
				tlsConfig, _, e := state.outgoingTLS(ctx, peerKey)
				if e != nil {
					t.Fatal(e)
				}
				transport, e := remote.Client(preparations[1].NormalServiceAddress, tlsConfig, 5*time.Second, 1<<20, func(peer remote.Peer) error { return state.admitContext(t.Context(), peer) })
				if e != nil {
					t.Fatal(e)
				}
				if _, e = fwire.NewEndpointClient(transport).Describe(); e != nil {
					t.Fatalf("restored committed receiving endpoint: %v", e)
				}
				// A separately restored local declaration must never shadow the
				// exact peer mapping. Reuse the real host record format offline.
				stopA()
				registry, e := machines[1].ResolveRegistryV3(ctx, local)
				if e != nil {
					t.Fatal(e)
				}
				page, e := registry.Registrations(ctx, "", 64)
				if e != nil || page.Outcome.String() != "page" {
					t.Fatalf("registered collision source: %+v %v", page, e)
				}
				var declaration providerFile
				for _, record := range page.Registrations {
					for _, server := range record.Spec.Servers {
						if server.Name == "ollama" {
							declaration = providerFile{Version: providerFileVersion, Declaration: serverFacetCompatibility(server)}
						}
					}
				}
				if declaration.Declaration.Name != "ollama" {
					t.Fatal("registry lost the registered collision source")
				}
				declaration.Declaration.Name = "machine-b"
				declaration.Declaration.Host.Hosted = true
				declaration.Declaration.Host.Kind = "openai-compatible"
				if field := validProviderDeclaration(declaration.Declaration); field != "" {
					t.Fatalf("restored collision fixture: %s", field)
				}
				collision := filepath.Join(root, providersDir, "machine-b.json")
				if e = writeAtomically(collision, declaration); e != nil {
					t.Fatal(e)
				}
				stopA = startInferenceRuntime(t, a)
				assertHosts(false)
				for _, contract := range []string{"abstraction.router/router@1", "abstraction.inference/chat@1", "abstraction.facade/messages@1"} {
					if _, e := machines[0].ResolveServiceOnMachine(ctx, contract, client.Requirements{}, "machine-b"); e == nil {
						t.Fatalf("ambiguous named registration resolved %s", contract)
					}
				}
				// Startup adopted the restored declaration into registry@3.
				// Remove that authoritative facet before deleting its old input.
				localRegistry, e := machines[0].ResolveRegistryV3(ctx, local)
				if e != nil {
					t.Fatal(e)
				}
				registered, e := localRegistry.Registrations(ctx, "", 64)
				if e != nil || registered.Outcome != registration.OutcomePage {
					t.Fatalf("restored collision registration: %+v %v", registered, e)
				}
				collisionRevision := ""
				for _, record := range registered.Registrations {
					for _, server := range record.Spec.Servers {
						if server.Name == "machine-b" {
							collisionRevision = record.Revision
						}
					}
				}
				if collisionRevision == "" {
					t.Fatal("registry lost the restored collision")
				}
				cleared, e := localRegistry.Remove(ctx, collisionRevision, registration.FacetReference{Kind: registration.FacetKindHost, Name: "machine-b"})
				if e != nil || cleared.Outcome != registration.OutcomeApplied {
					t.Fatalf("remove restored collision: %+v %v", cleared, e)
				}
				stopA()
				if e = os.Remove(collision); e != nil {
					t.Fatal(e)
				}
				stopA = startInferenceRuntime(t, a)
				assertHosts(true)
				controls[0], err = machines[0].ResolveMachineControl(ctx, local)
				if err != nil {
					t.Fatal(err)
				}
			}
			page, err := controls[0].List(ctx, "", 64)
			if err != nil {
				t.Fatal(err)
			}
			removed, err := controls[0].Remove(ctx, page.Revision, peerKey)
			if err != nil || removed.Outcome != mwire.MachineRemoveOutcomeRemoved {
				t.Fatalf("Remove: %v %v", removed.Outcome, err)
			}
			if retainedNamed != nil {
				if page, e := retainedNamed.Modalities("", 64); e == nil {
					t.Fatalf("retained removed peer disclosed %+v", page)
				}
				if _, e := machines[0].ResolveServiceOnMachine(ctx, "abstraction.router/router@1", client.Requirements{}, "machine-b"); e == nil {
					t.Fatal("removed machine fell back")
				}
			}
			afterRemove, err := controls[0].List(ctx, "", 64)
			if err != nil {
				t.Fatal(err)
			}
			beforeState, err := os.ReadFile(state.path)
			if err != nil {
				t.Fatal(err)
			}
			operator, err := machines[0].ResolveRightsOperator(ctx, local)
			if err != nil {
				t.Fatal(err)
			}
			beforePolicy, err := operator.ListPolicyContext(ctx, "", 64)
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				replayed, e := controls[0].Remove(ctx, page.Revision, peerKey)
				if e != nil || replayed.Outcome != mwire.MachineRemoveOutcomeConflict || replayed.Revision != afterRemove.Revision {
					t.Fatalf("old removal replay: %+v %v", replayed, e)
				}
			}
			afterState, err := os.ReadFile(state.path)
			if err != nil || string(beforeState) != string(afterState) {
				t.Fatal("old removal replay edited the pin state")
			}
			afterPolicy, err := operator.ListPolicyContext(ctx, "", 64)
			if err != nil || beforePolicy.Revision != afterPolicy.Revision {
				t.Fatal("old removal replay edited Rights")
			}
			if _, _, err = state.outgoingTLS(ctx, peerKey); err == nil {
				t.Fatal("removed pin retained outgoing access")
			}
			replay, e := controls[0].Enter(ctx, requests[0], offer.Code, preparations[0])
			wantReplay := mwire.MachineStartOutcomeExisting
			if mode == "one_direction" {
				wantReplay = mwire.MachineStartOutcomeExpired
			}
			if e != nil || replay.Outcome != wantReplay {
				t.Fatalf("lost-reply replay changed the attempt: %v %v", replay.Outcome, e)
			}
			// The runtime owns subsequent CAS replacements. The independently
			// loaded pre-removal handle must reject its changed anchor; observe
			// current membership through the actual local service owner.
			remaining, e := controls[0].List(ctx, "", 64)
			if e != nil || remaining.Outcome != mwire.MachineListOutcomePage || len(remaining.Machines) != 0 {
				t.Fatal("completed occurrence regranted a removed pin")
			}
			other, e := controls[1].List(ctx, "", 64)
			if e != nil || len(other.Machines) != 1 || other.Machines[0].State != "active" {
				t.Fatal("local Remove implied reciprocal removal")
			}
			keyA := other.Machines[0].PeerKey
			removed, e = controls[1].Remove(ctx, other.Revision, keyA)
			if e != nil || removed.Outcome != mwire.MachineRemoveOutcomeRemoved {
				t.Fatalf("receiving Remove: %v %v", removed.Outcome, e)
			}
			receiverOperator, e := machines[1].ResolveRightsOperator(ctx, local)
			if e != nil {
				t.Fatal(e)
			}
			receiverBefore, e := receiverOperator.ListPolicyContext(ctx, "", 64)
			if e != nil {
				t.Fatal(e)
			}
			for n := 0; n < 2; n++ {
				replayed, e := controls[1].Remove(ctx, other.Revision, keyA)
				if e != nil || replayed.Outcome != mwire.MachineRemoveOutcomeConflict || replayed.Revision != removed.Revision {
					t.Fatalf("receiving removal replay: %+v %v", replayed, e)
				}
			}
			receiverAfter, e := receiverOperator.ListPolicyContext(ctx, "", 64)
			if e != nil || receiverBefore.Revision != receiverAfter.Revision {
				t.Fatal("receiving removal replay edited Rights")
			}
			if offered {
				operator, e := machines[1].ResolveRightsOperator(ctx, local)
				if e != nil {
					t.Fatal(e)
				}
				read, e := operator.ReadRuleContext(ctx, rwire.Subject{Account: probeSelf().Account, Program: "remote:" + keyA}, remoteServeAction, "account")
				if e != nil || read.Outcome != rwire.RuleReadOutcomeUnknown || read.Record != nil {
					t.Fatal("receiving Remove retained its exact remote grant")
				}
				if listening(preparations[1].NormalServiceAddress) {
					t.Fatal("last receiving peer retained the normal listener")
				}
			}
		})
	}
}
