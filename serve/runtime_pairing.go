package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-identity/remote"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	watch "github.com/openabstractions/abstraction-watch/go"
)

const machineControlContract = "abstraction.facade/machines@1"
const pairingOperationLimit = 64

var errPairingConflict = errors.New("pairing: local configuration conflicts")

func pairingStartRefusal(err error) mwire.MachineStartOutcome {
	if errors.Is(err, errPairingConflict) {
		return mwire.MachineStartOutcomeConflict
	}
	if errors.Is(err, errPairingAgreement) {
		return mwire.MachineStartOutcomeInvalid
	}
	return mwire.MachineStartOutcomeUnavailable
}

type runtimePairing struct {
	ctx            context.Context
	cancel         context.CancelFunc
	state          *runtimePairingState
	asks           *runtimeAsks
	rights         *runtimeRights
	services       *runtimeInference
	messageHost    *applicationsHost
	resolution     *pairedResolution
	sink           logging.Sink
	audit          *remoteAudit
	report         func(error)
	options        runtimeFlags
	epoch          string
	started        time.Time
	mu             sync.Mutex
	ops            map[string]*pairingOperation
	active         string
	gate           *watch.Gate
	workers        sync.WaitGroup
	normalMu       sync.Mutex
	normalAddress  string
	normalActive   bool
	stopNormal     func()
	exposureFailed bool
}
type pairingOperation struct {
	id          string
	fingerprint [32]byte
	preparation mwire.MachinePreparation
	expires     int64
	ctx         context.Context
	cancel      context.CancelFunc
	phase       mwire.MachinePhase
	reason      string
	generation  uint64
	preview     *mwire.MachineAgreement
	question    string
	revision    string
	code        string // Secret, returned only by exact admitted StartOffer replay.
	consent     *pairingConsent
	localYes    bool
}

func randomPairingHex() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func validPairingHex(text string, size int) bool {
	b, err := hex.DecodeString(text)
	return err == nil && len(b) == size && hex.EncodeToString(b) == text
}

func composeRuntimePairing(ctx context.Context, options runtimeFlags, a *runtimeAsks, services *runtimeInference, sink logging.Sink, report func(error), messageHosts ...*applicationsHost) (*runtimePairing, error) {
	if a == nil || a.rights == nil || services == nil || services.providers == nil || services.registry == nil || sink == nil {
		return nil, errors.New("pairing: required local owners unavailable")
	}
	root, err := credentialsState(options)
	if err != nil {
		return nil, err
	}
	var state *runtimePairingState
	_, err = os.Lstat(filepath.Join(root, "pairing"))
	if errors.Is(err, os.ErrNotExist) {
		state, err = createRuntimePairingState(ctx, root)
	} else if err == nil {
		state, err = loadRuntimePairingState(ctx, root)
	}
	if err != nil {
		return nil, err
	}
	epoch, err := randomPairingHex()
	if err != nil {
		return nil, err
	}
	owned, cancel := context.WithCancel(ctx)
	p := &runtimePairing{ctx: owned, cancel: cancel, state: state, asks: a, rights: a.rights, services: services, sink: sink, report: report, options: options, epoch: epoch, started: time.Now(), ops: map[string]*pairingOperation{}, gate: watch.NewGate(watch.DefaultWaiters)}
	p.audit = &remoteAudit{sink: sink, slot: make(chan struct{}, 1), report: report}
	if len(messageHosts) == 1 && messageHosts[0] != nil {
		p.messageHost = messageHosts[0]
		p.messageHost.messagePairing = state
		p.rights.messagePairing = state
	}
	state.onChanged = func() {
		services.provider.Recheck(owned)
		p.gate.Advance()
		if p.messageHost != nil && p.messageHost.messages != nil {
			p.messageHost.messages.advance()
		}
	}
	services.providers.setPairedHosts(p.pairedHosts)
	services.providers.mu.Lock()
	services.providers.pairedNameAdmission = p.bindingNameAdmission
	services.providers.mu.Unlock()
	services.registry.pairing = p
	p.resolution = &pairedResolution{p: p, routes: map[string]*pairedResolutionRoute{}}
	if err := p.resolution.sync(); err != nil {
		p.Close()
		return nil, err
	}
	services.providers.mu.Lock()
	services.providers.pairedCandidates = p.resolution.candidates
	services.providers.pairedRefresh = p.resolution.sync
	services.providers.mu.Unlock()
	services.providers.notify()
	p.startVisibility()
	return p, nil
}
func (p *runtimePairing) Close() {
	if p == nil {
		return
	}
	p.cancel()
	if p.resolution != nil {
		p.resolution.close()
	}
	p.mu.Lock()
	for _, op := range p.ops {
		op.cancel()
	}
	p.mu.Unlock()
	p.workers.Wait()
	p.normalMu.Lock()
	stop := p.stopNormal
	p.stopNormal = nil
	p.normalActive = false
	p.normalMu.Unlock()
	if stop != nil {
		stop()
	}
}
func (p *runtimePairing) nowMS() int64 { return time.Since(p.started).Milliseconds() }

func (p *runtimePairing) validatePreparation(ctx context.Context, preparation mwire.MachinePreparation, invitation string, current ...func() error) error {
	if preparation.Request != mwire.MachineScopeKindNone && preparation.Request != mwire.MachineScopeKindOneHostInference {
		return errPairingAgreement
	}
	if !providerName.MatchString(preparation.BindingName) {
		return errPairingAgreement
	}
	if err := p.bindingAvailable(ctx, preparation.BindingName, "", current...); err != nil {
		return err
	}
	switch preparation.Offer {
	case mwire.MachineScopeKindNone:
		if preparation.OfferHost != "" || preparation.NormalServiceAddress != "" {
			return errPairingAgreement
		}
	case mwire.MachineScopeKindOneHostInference:
		if !validPairingNormalAddress(preparation.NormalServiceAddress) || preparation.NormalServiceAddress == invitation {
			return errPairingAgreement
		}
		if !p.localHost(preparation.OfferHost) {
			return errPairingAgreement
		}
		if _, err := pairingRules(preparation.OfferHost); err != nil {
			return err
		}
	default:
		return errPairingAgreement
	}
	return nil
}
func (p *runtimePairing) localHost(name string) bool {
	for _, f := range p.services.providers.hostFiles() {
		if f.Declaration.Name == name && !f.Declaration.remote() && !f.Disabled {
			return true
		}
	}
	for _, s := range p.services.providers.sorted() {
		if s.file.Declaration.Name == name && !s.file.Declaration.remote() && !s.file.Disabled && len(s.file.Declaration.mediatedInferenceProfiles()) != 0 {
			return true
		}
	}
	return false
}
func (p *runtimePairing) bindingAvailable(ctx context.Context, name, key string, current ...func() error) error {
	files, _, err := p.services.providers.read()
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Declaration.Name == name {
			return errPairingConflict
		}
	}
	_, peers, err := p.state.snapshot(ctx, current...)
	if err != nil {
		return err
	}
	for other, peer := range peers {
		if other != key && peer.BindingName == name {
			return errPairingConflict
		}
	}
	return nil
}
func (p *runtimePairing) bindingNameAdmission(name string) error {
	p.mu.Lock()
	active := p.ops[p.active]
	inFlight := active != nil && active.preparation.BindingName == name
	p.mu.Unlock()
	if inFlight {
		return errors.Join(errPairingConflict, errors.New("pairing: binding name reserved by active operation"))
	}
	_, peers, err := p.state.snapshot(p.ctx)
	if err != nil {
		return err
	}
	for _, peer := range peers {
		if peer.BindingName == name {
			return errors.Join(errPairingConflict, errors.New("pairing: binding name already paired"))
		}
	}
	return nil
}
func (p *runtimePairing) reserve(request mwire.MachineRequestIdentity, preparation mwire.MachinePreparation, kind, address, code string) (*pairingOperation, mwire.MachineStartOutcome) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowMS()
	if request.Epoch != p.epoch || !validPairingHex(request.Nonce, 16) || request.ExpiresMs <= now || request.ExpiresMs > now+120000 {
		return nil, mwire.MachineStartOutcomeExpired
	}
	id := p.epoch + "." + request.Nonce
	// Only the digest of Enter's secret participates in retained argument identity.
	secretHash := sha256.Sum256([]byte(code))
	data, err := json.Marshal(struct {
		Preparation   mwire.MachinePreparation
		Kind, Address string
		Expires       int64
		Secret        [32]byte
	}{preparation, kind, address, request.ExpiresMs, secretHash})
	if err != nil {
		return nil, mwire.MachineStartOutcomeInvalid
	}
	fingerprint := sha256.Sum256(data)
	if old := p.ops[id]; old != nil {
		if old.fingerprint != fingerprint {
			return nil, mwire.MachineStartOutcomeConflict
		}
		return old, mwire.MachineStartOutcomeExisting
	}
	for key, old := range p.ops {
		if now > old.expires+120000 && key != p.active {
			delete(p.ops, key)
		}
	}
	if p.active != "" || len(p.ops) >= pairingOperationLimit {
		return nil, mwire.MachineStartOutcomeExhausted
	}
	ctx, cancel := context.WithDeadline(p.ctx, p.started.Add(time.Duration(request.ExpiresMs)*time.Millisecond))
	op := &pairingOperation{id: id, fingerprint: fingerprint, preparation: preparation, expires: request.ExpiresMs, ctx: ctx, cancel: cancel, phase: mwire.MachinePhaseConnecting, generation: 1}
	p.ops[id] = op
	p.active = id
	return op, mwire.MachineStartOutcomeStarted
}
func cloneMachineOperation(op *pairingOperation) *mwire.MachineOperation {
	view := &mwire.MachineOperation{ID: op.id, Phase: op.phase, ExpiresMs: op.expires, Reason: op.reason, QuestionID: op.question, Revision: op.revision}
	if op.preview != nil {
		copy := *op.preview
		view.Agreement = &copy
	}
	return view
}
func (p *runtimePairing) startReply(op *pairingOperation, outcome mwire.MachineStartOutcome, offer bool) mwire.MachineStart {
	if op == nil {
		return mwire.MachineStart{Outcome: outcome}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	reply := mwire.MachineStart{Outcome: outcome, Operation: cloneMachineOperation(op)}
	if offer {
		reply.Code = op.code
	}
	return reply
}
func (p *runtimePairing) set(op *pairingOperation, phase mwire.MachinePhase, reason string) {
	p.mu.Lock()
	op.phase, op.reason = phase, reason
	op.generation++
	p.mu.Unlock()
	p.gate.Advance()
}

func (p *runtimePairing) StartOffer(ctx context.Context, request mwire.MachineRequestIdentity, address string, preparation mwire.MachinePreparation, authorize func() error) mwire.MachineStart {
	if authorize() == nil && p.ctx.Err() == nil {
	} else {
		return mwire.MachineStart{Outcome: mwire.MachineStartOutcomeForbidden}
	}
	at, err := remoteListenAddress(address)
	if err != nil || at.String() != address || !validPairingInvitationAddress(at) {
		return mwire.MachineStart{Outcome: mwire.MachineStartOutcomeInvalid}
	}
	op, outcome := p.reserve(request, preparation, "offer", address, "")
	if outcome != mwire.MachineStartOutcomeStarted {
		return p.startReply(op, outcome, true)
	}
	if err = p.validatePreparation(op.ctx, preparation, address, p.currentOperation(op)); err != nil {
		p.end(op, err)
		return p.startReply(op, pairingStartRefusal(err), false)
	}
	if authorize() != nil {
		p.end(op, errPairingAgreement)
		return p.startReply(op, mwire.MachineStartOutcomeForbidden, false)
	}
	if err = p.ensureNormal(op.ctx, preparation.NormalServiceAddress, authorize, p.currentOperation(op)); err != nil {
		p.end(op, err)
		return p.startReply(op, pairingStartRefusal(err), false)
	}
	if err = ctx.Err(); err != nil {
		p.end(op, err)
		return p.startReply(op, mwire.MachineStartOutcomeUnavailable, false)
	}
	if authorize() != nil {
		p.end(op, errPairingAgreement)
		return p.startReply(op, mwire.MachineStartOutcomeForbidden, false)
	}
	listener, err := listenRuntimeRemote(op.ctx, "tcp", address)
	if err != nil {
		p.end(op, err)
		return p.startReply(op, mwire.MachineStartOutcomeUnavailable, false)
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		listener.Close()
		p.end(op, err)
		return p.startReply(op, mwire.MachineStartOutcomeUnavailable, false)
	}
	code, err := encodePairingInvitation(at, secret)
	if err != nil {
		listener.Close()
		p.end(op, err)
		return p.startReply(op, mwire.MachineStartOutcomeInvalid, false)
	}
	p.mu.Lock()
	op.code = code
	op.phase = mwire.MachinePhaseOffering
	op.generation++
	p.mu.Unlock()
	p.gate.Advance()
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		stop := context.AfterFunc(op.ctx, func() { listener.Close() })
		defer stop()
		defer listener.Close()
		raw, err := listener.Accept()
		listener.Close()
		p.mu.Lock()
		op.code = ""
		p.mu.Unlock()
		if err == nil {
			p.set(op, mwire.MachinePhaseConnecting, "")
			err = p.ceremony(op, raw, secret, false)
		}
		clear(secret[:])
		p.end(op, err)
	}()
	return p.startReply(op, outcome, true)
}
func (p *runtimePairing) Enter(ctx context.Context, request mwire.MachineRequestIdentity, code string, preparation mwire.MachinePreparation, authorize func() error) mwire.MachineStart {
	if authorize() != nil {
		return mwire.MachineStart{Outcome: mwire.MachineStartOutcomeForbidden}
	}
	at, secret, err := decodePairingInvitation(code)
	if err != nil {
		return mwire.MachineStart{Outcome: mwire.MachineStartOutcomeInvalid}
	}
	op, outcome := p.reserve(request, preparation, "enter", at.String(), code)
	if outcome != mwire.MachineStartOutcomeStarted {
		clear(secret[:])
		return p.startReply(op, outcome, false)
	}
	if err = p.validatePreparation(op.ctx, preparation, at.String(), p.currentOperation(op)); err != nil {
		clear(secret[:])
		p.end(op, err)
		return p.startReply(op, pairingStartRefusal(err), false)
	}
	if authorize() != nil {
		clear(secret[:])
		p.end(op, errPairingAgreement)
		return p.startReply(op, mwire.MachineStartOutcomeForbidden, false)
	}
	if err = p.ensureNormal(op.ctx, preparation.NormalServiceAddress, authorize, p.currentOperation(op)); err != nil {
		clear(secret[:])
		p.end(op, err)
		return p.startReply(op, pairingStartRefusal(err), false)
	}
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		raw, err := (&net.Dialer{}).DialContext(op.ctx, "tcp", at.String())
		if err == nil {
			err = p.ceremony(op, raw, secret, true)
		}
		clear(secret[:])
		p.end(op, err)
	}()
	return p.startReply(op, outcome, false)
}

func (p *runtimePairing) currentOperation(op *pairingOperation) func() error {
	return func() error {
		if err := op.ctx.Err(); err != nil {
			return err
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.active != op.id || p.ops[op.id] != op {
			return errPairingState
		}
		return op.ctx.Err()
	}
}

func (p *runtimePairing) ceremony(op *pairingOperation, raw net.Conn, secret [32]byte, initiator bool) error {
	cert, _, err := p.state.identity(op.ctx, p.currentOperation(op))
	if err != nil {
		raw.Close()
		return err
	}
	var prepared []byte
	callbacks := remote.PairingCallbacks{
		Prepare: func(ctx context.Context, keys remote.PairKeys) ([]byte, error) {
			if err := p.bindingAvailable(ctx, op.preparation.BindingName, "", p.currentOperation(op)); err != nil {
				return nil, err
			}
			if op.preparation.Offer == mwire.MachineScopeKindOneHostInference && !p.localHost(op.preparation.OfferHost) {
				return nil, errPairingAgreement
			}
			var err error
			prepared, err = preparePairingProposal(op.preparation, keys)
			return bytes.Clone(prepared), err
		},
		Validate: func(ctx context.Context, view remote.PairView) error {
			preview, _, _, err := pairingAgreement(view)
			if err != nil {
				return err
			}
			local := view.InitiatorProposal
			if view.Role == 2 {
				local = view.ResponderProposal
			}
			if !bytes.Equal(local, prepared) {
				return errPairingAgreement
			}
			p.mu.Lock()
			op.preview = &preview
			op.phase = mwire.MachinePhaseChecking
			op.generation++
			p.mu.Unlock()
			p.gate.Advance()
			return ctx.Err()
		},
		Show:    func(ctx context.Context, view remote.PairView) error { return p.show(op, view) },
		Confirm: func(ctx context.Context, view remote.PairView) (bool, error) { return p.confirm(op, view) },
	}
	var candidate remote.PairCandidate
	if initiator {
		candidate, err = remote.PairInitiator(op.ctx, raw, cert, secret, callbacks)
	} else {
		candidate, err = remote.PairResponder(op.ctx, raw, cert, secret, callbacks)
	}
	if err != nil {
		return err
	}
	p.mu.Lock()
	consent := op.consent
	p.mu.Unlock()
	if consent == nil {
		return errPairingAgreement
	}
	revision, _, err := p.state.snapshot(op.ctx, p.currentOperation(op))
	if err != nil {
		return err
	}
	_, err = p.state.stageCompleted(op.ctx, revision, candidate, *consent, p.currentOperation(op))
	if err != nil {
		return err
	}
	p.set(op, mwire.MachinePhaseReconciling, "")
	return p.activate(op, hex.EncodeToString(candidate.View.PeerKey[:]))
}
func (p *runtimePairing) show(op *pairingOperation, view remote.PairView) error {
	preview, _, _, err := pairingAgreement(view)
	if err != nil {
		return err
	}
	snapshot, err := p.rights.policy.OperatorSnapshot()
	if err != nil {
		return err
	}
	slots := pairingQuestionSlots(preview, op.preparation.BindingName, p.started.Add(time.Duration(op.expires)*time.Millisecond).UTC().Format(time.RFC3339))
	requestSum := sha256.Sum256([]byte(op.id + ":" + preview.Transcript))
	record, outcome, err := p.asks.book.AskApplicationAs(p.asks.scope, p.rights.self().Program, "pair:"+hex.EncodeToString(requestSum[:]), asks.PairingKey, slots, listen.Seen{Why: "runtime-owned frozen pairing agreement"}, asks.Publisher{})
	if err != nil || outcome != "pending" {
		return errPairingAgreement
	}
	consent := pairingConsent{Operation: op.id, Question: record.ID, Scope: p.asks.scope, Program: p.rights.self().Program, TextHash: consentTextHash(record.Text), ExpectedRights: snapshot.Revision, Role: view.Role, LocalKey: view.LocalKey, PeerKey: view.PeerKey, Transcript: view.Transcript, Check: view.Check, Initiator: bytes.Clone(view.InitiatorProposal), Responder: bytes.Clone(view.ResponderProposal)}
	revision, _, err := p.state.snapshot(op.ctx, p.currentOperation(op))
	if err == nil {
		_, err = p.state.prepareConsent(op.ctx, revision, consent, p.currentOperation(op))
	}
	if err != nil {
		return err
	}
	p.mu.Lock()
	op.question = record.ID
	op.consent = &consent
	op.phase = mwire.MachinePhaseAwaitingPerson
	op.generation++
	p.mu.Unlock()
	p.gate.Advance()
	return op.ctx.Err()
}

// Display slots retain the existing per-slot bound and draw only from the
// frozen agreement. Splitting the approved prose changes no effect or digest.
func pairingQuestionSlots(preview mwire.MachineAgreement, binding, until string) map[string]string {
	slots := map[string]string{"peer": preview.PeerKey, "check": preview.Check, "binding": binding, "code": preview.Transcript[:16], "until": until}
	for _, entry := range []struct {
		name      string
		direction mwire.MachineDirection
	}{{"given", preview.Given}, {"received", preview.Received}} {
		name, direction := entry.name, entry.direction
		slots[name] = "No access."
		slots[name+"more"] = ""
		slots[name+"replacement"] = ""
		slots[name+"source"] = "No selected model server"
		if direction.Kind != mwire.MachineScopeKindNone {
			slots[name] = "Completion and embedding requests through selected model server " + direction.Host + "."
			slots[name+"more"] = "Names/details of ALL model servers registered for that user account; OA connections needed for those operations."
			slots[name+"replacement"] = "Replacing the model server registered under " + direction.Host + " keeps this permission."
			slots[name+"source"] = direction.Host + " at " + direction.ServiceAddress
		}
	}
	return slots
}
func (p *runtimePairing) confirm(op *pairingOperation, view remote.PairView) (bool, error) {
	p.mu.Lock()
	consent := op.consent
	p.mu.Unlock()
	if consent == nil || consent.Transcript != view.Transcript {
		return false, errPairingAgreement
	}
	decided, stopWaiting := p.asks.book.ObserveQuestionWait(consent.Question)
	select {
	case <-op.ctx.Done():
		stopWaiting()
		return false, op.ctx.Err()
	case <-decided:
		stopWaiting()
	}
	yes := false
	found, err := p.asks.book.WithApplicationRecordContext(op.ctx, consent.Question, func(record asks.Record, scope string, _ []string) error {
		if record.Option != "once" {
			return errPairingAgreement
		}
		if err := p.nativeConsent(record, scope, *consent); err != nil {
			return err
		}
		revision, _, err := p.state.snapshot(op.ctx, p.currentOperation(op))
		if err == nil {
			_, err = p.state.consentYes(op.ctx, revision, *consent, p.currentOperation(op))
		}
		if err != nil {
			return err
		}
		yes = true
		return nil
	})
	if err != nil || !found {
		return false, errPairingAgreement
	}
	if yes {
		p.mu.Lock()
		op.localYes = true
		op.phase = mwire.MachinePhaseConsentSent
		op.generation++
		p.mu.Unlock()
		p.gate.Advance()
	}
	return yes, nil
}
func (p *runtimePairing) nativeConsent(record asks.Record, scope string, exact pairingConsent) error {
	if scope != p.asks.scope || scope != exact.Scope || record.ID != exact.Question || record.Key != asks.PairingKey || record.Option != "once" || !record.Yes || record.Kept || record.AnsweredBy != asks.AnsweredByOperator || record.Answered.IsZero() || record.Pending() || consentTextHash(record.Text) != exact.TextHash || record.AskerProgram != exact.Program || exact.Program != p.rights.self().Program {
		return errPairingAgreement
	}
	return nil
}
func (p *runtimePairing) activate(op *pairingOperation, key string) error {
	p.mu.Lock()
	consent := op.consent
	p.mu.Unlock()
	if consent == nil {
		return errPairingAgreement
	}
	if err := p.bindingAvailable(op.ctx, op.preparation.BindingName, key, p.currentOperation(op)); err != nil {
		return err
	}
	if op.preparation.Offer == mwire.MachineScopeKindOneHostInference && !p.localHost(op.preparation.OfferHost) {
		return errPairingAgreement
	}
	_, local, _, err := pairingAgreement(consent.view())
	if err != nil {
		return err
	}
	revision, _, err := p.state.snapshot(op.ctx, p.currentOperation(op))
	if err != nil {
		return err
	}
	var updated uint64
	found, err := p.asks.book.WithApplicationRecordContext(op.ctx, consent.Question, func(record asks.Record, scope string, retained []string) error {
		if err := p.nativeConsent(record, scope, *consent); err != nil {
			return err
		}
		var err error
		updated, err = p.state.activateConsent(op.ctx, revision, key, func(ctx context.Context, peer pairingPeerRecord) (pairingActivation, error) {
			if peer.Consent == nil || !samePairingConsent(*peer.Consent, *consent) || !peer.Consent.NativeYes || !peer.Consent.TransportComplete {
				return pairingActivation{}, errPairingAgreement
			}
			if len(local.Rules) == 0 {
				return pairingActivation{Kind: "native_no_grant"}, nil
			}
			rules := pairingPolicyRules(p.rights.owner, key, local)
			completed, err := p.rights.policy.AppliedFirstUseAllow(consent.Question)
			if err != nil {
				return pairingActivation{}, err
			}
			if completed {
				for _, rule := range rules {
					if p.rights.policy.Decide(rule.Subject, rule.Action, rule.Resource).Outcome != rwire.DecisionOutcomePermitted {
						return pairingActivation{}, errPairingAgreement
					}
				}
				return p.pairingActivationRevision(ctx, key, consent.Question)
			}
			permit := true
			result, err := p.rights.policy.ApplyRemotePairingAllowContext(ctx, consent.ExpectedRights, rules, rights.RuleEdit{Permit: &permit, By: p.rights.self(), Why: "native pairing agreement"}, consent.Question, retained, func() error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := p.nativeConsent(record, scope, *consent); err != nil {
					return err
				}
				return nil
			})
			if err != nil || result.Outcome != rwire.PolicyEditOutcomeApplied {
				return pairingActivation{}, errPairingAgreement
			}
			return p.pairingActivationRevision(ctx, key, consent.Question)
		}, p.currentOperation(op))
		return err
	})
	if err != nil || !found {
		return errPairingAgreement
	}
	p.mu.Lock()
	op.revision = strconv.FormatUint(updated, 10)
	p.mu.Unlock()
	// Activation is durable. As with removal, a derived host refresh failure
	// belongs to its reporting owner and cannot change the committed outcome.
	if err = p.services.providers.pairedChanged(); err != nil && p.report != nil {
		p.report(err)
	}
	return nil
}

func (p *runtimePairing) pairingActivationRevision(ctx context.Context, key, question string) (pairingActivation, error) {
	// The incoming decision fence is the Rights owner's ordered observation,
	// distinct from an operator's conditional-edit revision token.
	observed := p.rights.policy.DecideForAtLeastRevisionContext(ctx, rwire.Subject{Account: p.rights.owner, Program: "remote:" + key}, remoteServeAction, "account", "")
	if observed.Decision.Outcome != rwire.DecisionOutcomePermitted || observed.ObservedRevision == "" {
		return pairingActivation{}, errPairingAgreement
	}
	return pairingActivation{Kind: "rights_applied", Receipt: question, Revision: observed.ObservedRevision}, nil
}

func (p *runtimePairing) end(op *pairingOperation, err error) {
	phase, reason := mwire.MachinePhasePaired, ""
	if err != nil {
		p.mu.Lock()
		yes := op.localYes
		p.mu.Unlock()
		phase = mwire.MachinePhaseRefused
		reason = "refused"
		if yes || errors.Is(err, remote.ErrPairingUncertain) {
			phase = mwire.MachinePhaseUncertain
			reason = "outcome_unknown"
		} else if errors.Is(err, context.DeadlineExceeded) {
			phase = mwire.MachinePhaseExpired
			reason = "expired"
		} else if errors.Is(err, context.Canceled) {
			phase = mwire.MachinePhaseCancelled
			reason = "cancelled"
		}
	}
	op.cancel()
	p.mu.Lock()
	op.code = ""
	question := op.question
	p.mu.Unlock()
	if question != "" {
		record, found, readErr := p.asks.book.ApplicationRecord(question)
		if readErr == nil && found && record.Pending() {
			_, _, retireErr := p.asks.book.RetireApplicationAuthorized(question, nil)
			if retireErr != nil && p.report != nil {
				p.report(errors.New("pairing: pending question retirement unavailable"))
			}
		}
	}
	p.releaseUncommittedNormal()
	p.mu.Lock()
	// Terminal observation and admission of another attempt share one boundary,
	// after the existing question and receiving owners have completed cleanup.
	op.phase, op.reason = phase, reason
	op.generation++
	if p.active == op.id {
		p.active = ""
	}
	p.mu.Unlock()
	p.gate.Advance()
}

func (p *runtimePairing) ensureNormal(ctx context.Context, address string, authorize func() error, current ...func() error) error {
	if address == "" {
		return nil
	}
	if p.options.remoteConfiguration != nil {
		return errPairingConflict
	}
	if !validPairingNormalAddress(address) {
		return errPairingAgreement
	}
	p.normalMu.Lock()
	defer p.normalMu.Unlock()
	if p.exposureFailed {
		return errRemoteExposure
	}
	if p.normalAddress != "" {
		if p.normalAddress != address {
			return errPairingConflict
		}
		return nil
	}
	if err := p.state.acquire(ctx, current); err != nil {
		return err
	}
	doc, _, err := p.state.read(ctx)
	p.state.mu.Unlock()
	if err != nil {
		return err
	}
	if doc.ReceivingAddress != "" && doc.ReceivingAddress != address {
		return errPairingConflict
	}
	identity, _, err := p.state.identity(ctx, current...)
	if err != nil {
		return err
	}
	config := &runtimeRemoteConfig{Listen: true, Address: address, TLS: &tls.Config{Certificates: []tls.Certificate{identity}, ClientCAs: x509.NewCertPool()}, ClientRoots: p.state.clientRoots, AdmitContext: p.state.admitContext, standingAdmission: p.state.probeStanding, Minimum: p.state.minimum, audit: p.audit, observeCall: p.state.observeCall}
	options := p.options
	options.remoteConfiguration = func(context.Context) (*runtimeRemoteConfig, error) { return config, nil }
	prepared, err := prepareRuntimeRemote(ctx, options, p.rights, p.services, p.sink, p.report)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if authorize != nil {
		if err := authorize(); err != nil {
			return err
		}
	}
	stop, err := prepared.start(p.ctx, func(err error) {
		p.normalMu.Lock()
		p.normalActive = false
		if errors.Is(err, errRemoteExposure) {
			p.exposureFailed = true
		}
		p.normalMu.Unlock()
		if p.report != nil {
			p.report(errors.New("pairing: normal receiving unavailable"))
		}
	}, func(network, address string) (net.Listener, error) {
		return listenRuntimeRemote(p.ctx, network, address)
	})
	if err != nil {
		if errors.Is(err, errRemoteExposure) {
			p.exposureFailed = true
		}
		return err
	}
	p.normalAddress, p.stopNormal = address, stop
	p.normalActive = true
	return nil
}
func (p *runtimePairing) releaseUncommittedNormal() {
	p.state.mu.Lock()
	doc, _, err := p.state.read(p.ctx)
	p.state.mu.Unlock()
	// An unreadable book disables trust through live Admit. Keep its listener
	// owner until shutdown so cleanup cannot misclassify a committed endpoint.
	if err != nil {
		return
	}
	active := false
	for _, peer := range doc.Peers {
		active = active || peer.State == "active"
	}
	if active && doc.ReceivingAddress != "" {
		return
	}
	p.mu.Lock()
	preparing := ""
	if op := p.ops[p.active]; op != nil && op.ctx.Err() == nil {
		preparing = op.preparation.NormalServiceAddress
	}
	p.mu.Unlock()
	p.normalMu.Lock()
	if preparing != "" && preparing == p.normalAddress {
		p.normalMu.Unlock()
		return
	}
	stop := p.stopNormal
	p.stopNormal = nil
	p.normalAddress = ""
	p.normalActive = false
	p.normalMu.Unlock()
	if stop != nil {
		stop()
	}
}
func (p *runtimePairing) restoreNormal() error {
	// Composition attaches the durable paired source after initial host
	// publication. Refresh that same owner even for outgoing-only pairs.
	if err := p.services.providers.pairedChanged(); err != nil {
		return err
	}
	p.state.mu.Lock()
	doc, _, err := p.state.read(p.ctx)
	p.state.mu.Unlock()
	if err != nil {
		return err
	}
	active := false
	for _, peer := range doc.Peers {
		active = active || peer.State == "active"
	}
	if !active || doc.ReceivingAddress == "" {
		return nil
	}
	return p.ensureNormal(p.ctx, doc.ReceivingAddress, nil)
}

func (p *runtimePairing) Observe(ctx context.Context, id, after string, waitMS int64, authorize func() error) mwire.MachineObservation {
	if waitMS < 0 || waitMS > 30000 {
		return mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeInvalid}
	}
	if authorize() != nil {
		return mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeForbidden}
	}
	var result mwire.MachineObservation
	outcome, err := p.gate.Wait(ctx, time.Duration(waitMS)*time.Millisecond, watch.Probe{Read: func() (bool, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		op := p.ops[id]
		if op == nil {
			result = mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeUnknown}
			return true, nil
		}
		cursor := p.epoch + ":" + op.id + ":" + strconv.FormatUint(op.generation, 10)
		if after != "" && after != cursor {
			prefix := p.epoch + ":" + op.id + ":"
			number, e := strconv.ParseUint(strings.TrimPrefix(after, prefix), 10, 64)
			if !strings.HasPrefix(after, prefix) || e != nil || number > op.generation {
				result = mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeGap}
				return true, nil
			}
		}
		result = mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeChanged, Cursor: cursor, Operation: cloneMachineOperation(op)}
		if after == cursor {
			result.Outcome = mwire.MachineObserveOutcomeUnchanged
			return false, nil
		}
		return true, nil
	}, Recheck: authorize})
	if err != nil {
		return mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeForbidden}
	}
	if outcome == watch.Busy {
		return mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeExhausted}
	}
	if outcome == watch.Failed {
		return mwire.MachineObservation{Outcome: mwire.MachineObserveOutcomeFailed}
	}
	return result
}
func (p *runtimePairing) Cancel(ctx context.Context, id string, authorize func() error) mwire.MachineCancellation {
	if authorize() != nil {
		return mwire.MachineCancellation{Outcome: mwire.MachineCancelOutcomeForbidden}
	}
	p.mu.Lock()
	op := p.ops[id]
	if op == nil {
		p.mu.Unlock()
		return mwire.MachineCancellation{Outcome: mwire.MachineCancelOutcomeUnknown}
	}
	op.cancel()
	view := cloneMachineOperation(op)
	p.mu.Unlock()
	return mwire.MachineCancellation{Outcome: mwire.MachineCancelOutcomeUpdated, Operation: view}
}
func (p *runtimePairing) List(ctx context.Context, cursor string, limit int64, authorize func() error) mwire.MachinePage {
	refuse := func(outcome mwire.MachineListOutcome) mwire.MachinePage {
		return mwire.MachinePage{Outcome: outcome, Machines: []mwire.MachineRecord{}}
	}
	if limit < 1 || limit > 64 {
		return refuse(mwire.MachineListOutcomeInvalid)
	}
	if authorize() != nil {
		return refuse(mwire.MachineListOutcomeForbidden)
	}
	if !p.state.mu.TryLock() {
		return refuse(mwire.MachineListOutcomeUnavailable)
	}
	doc, _, err := p.state.read(ctx)
	p.state.mu.Unlock()
	if err != nil {
		return refuse(mwire.MachineListOutcomeUnavailable)
	}
	revision := strconv.FormatUint(doc.Revision, 10)
	prefix := p.epoch + ":" + revision + ":"
	offset := 0
	if cursor != "" {
		if !strings.HasPrefix(cursor, prefix) {
			return refuse(mwire.MachineListOutcomeGap)
		}
		offset, err = strconv.Atoi(strings.TrimPrefix(cursor, prefix))
		if err != nil || offset < 0 {
			return refuse(mwire.MachineListOutcomeGap)
		}
	}
	keys := make([]string, 0, len(doc.Peers))
	if doc.Pending != nil && doc.Pending.NativeYes {
		key := hex.EncodeToString(doc.Pending.PeerKey[:])
		if _, exists := doc.Peers[key]; !exists {
			doc.Peers[key] = pairingPeerRecord{State: "uncertain"}
		}
	}
	for key := range doc.Peers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if offset > len(keys) {
		return refuse(mwire.MachineListOutcomeGap)
	}
	end := min(len(keys), offset+int(limit))
	rows := make([]mwire.MachineRecord, 0, end-offset)
	for _, key := range keys[offset:end] {
		peer := doc.Peers[key]
		rows = append(rows, mwire.MachineRecord{PeerKey: key, State: peer.State, ServiceAddress: peer.PeerServiceAddress, LastCallUnixMs: p.state.lastCall(key, peer), BindingName: peer.BindingName})
	}
	if authorize() != nil {
		return refuse(mwire.MachineListOutcomeForbidden)
	}
	next := ""
	if end < len(keys) {
		next = prefix + strconv.Itoa(end)
	}
	p.normalMu.Lock()
	localAddress := p.normalAddress
	if !p.normalActive || p.stopNormal == nil || p.exposureFailed || p.ctx != nil && p.ctx.Err() != nil {
		localAddress = ""
	}
	p.normalMu.Unlock()
	return mwire.MachinePage{Outcome: mwire.MachineListOutcomePage, Epoch: p.epoch, RequestExpiresMs: p.nowMS() + 120000, Revision: revision, NextCursor: next, Machines: rows, LocalServiceAddress: localAddress}
}
func (p *runtimePairing) Remove(ctx context.Context, expected, key string, authorize func() error) mwire.MachineRemoval {
	if authorize() != nil {
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeForbidden}
	}
	revision, err := strconv.ParseUint(expected, 10, 64)
	if err != nil || !validPairingHex(key, 32) {
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeInvalid}
	}
	current, peers, err := p.state.snapshot(ctx)
	if err != nil {
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeUnavailable}
	}
	if current != revision {
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeConflict, Revision: strconv.FormatUint(current, 10)}
	}
	if _, ok := peers[key]; !ok {
		if !p.state.mu.TryLock() {
			return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeUnavailable}
		}
		doc, _, readErr := p.state.read(ctx)
		p.state.mu.Unlock()
		if readErr != nil {
			return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeUnavailable}
		}
		if doc.Revision != revision {
			return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeConflict, Revision: strconv.FormatUint(doc.Revision, 10)}
		}
		if doc.Pending == nil || hex.EncodeToString(doc.Pending.PeerKey[:]) != key {
			return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeNotFound, Revision: expected}
		}
		found := false
		updated, clearErr := p.state.change(ctx, revision, func(doc *pairingDocument) error {
			if err := authorize(); err != nil {
				return err
			}
			if doc.Pending != nil && hex.EncodeToString(doc.Pending.PeerKey[:]) == key {
				// No Rights edit precedes stageCompleted. A lost terminal exchange
				// remains inactive until this explicit local removal clears it.
				doc.Pending = nil
				found = true
			}
			if !found {
				return errPairingState
			}
			return nil
		})
		if clearErr != nil {
			return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeUnavailable}
		}
		if found {
			return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeRemoved, Revision: strconv.FormatUint(updated, 10)}
		}
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeNotFound, Revision: strconv.FormatUint(updated, 10)}
	}
	if authorize() != nil {
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeForbidden}
	}
	revision, err = p.state.remove(ctx, revision, key, authorize)
	if err != nil {
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeUnavailable}
	}
	// Pin denial precedes the standing recheck and conditional Rights cleanup.
	p.services.provider.Recheck(ctx)
	if err = p.services.providers.pairedChanged(); err != nil && p.report != nil {
		p.report(err)
	}
	snapshot, err := p.rights.policy.OperatorSnapshot()
	if err != nil {
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeCleanupPending, Revision: strconv.FormatUint(revision, 10)}
	}
	revision, err = p.state.finishRemoval(ctx, revision, key, func(ctx context.Context, _ pairingPeerRecord) error {
		if err := authorize(); err != nil {
			return err
		}
		result, err := p.rights.policy.RemoveRemoteSubjectContext(ctx, snapshot.Revision, rwire.Subject{Account: p.rights.owner, Program: "remote:" + key}, authorize)
		if err != nil || result.Outcome != rwire.PolicyEditOutcomeApplied {
			return errPairingAgreement
		}
		return nil
	})
	if err != nil {
		current, _, readErr := p.state.snapshot(ctx)
		if readErr != nil {
			return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeUnavailable}
		}
		return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeCleanupPending, Revision: strconv.FormatUint(current, 10)}
	}
	p.releaseUncommittedNormal()
	p.gate.Advance()
	return mwire.MachineRemoval{Outcome: mwire.MachineRemoveOutcomeRemoved, Revision: strconv.FormatUint(revision, 10)}
}

func (p *runtimePairing) pairedHosts() ([]*router.Host, error) {
	_, peers, err := p.state.snapshot(p.ctx)
	if err != nil {
		return nil, err
	}
	files, _, err := p.services.providers.read()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(peers))
	for key := range peers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var hosts []*router.Host
	for _, key := range keys {
		peer := peers[key]
		if peer.State != "active" || peer.Consent == nil || peer.PeerServiceAddress == "" {
			continue
		}
		for _, file := range files {
			if file.Declaration.Name == peer.BindingName {
				return nil, &pairedNameClash{name: peer.BindingName}
			}
		}
		config, address, err := p.state.outgoingTLS(p.ctx, key)
		if err != nil {
			return nil, err
		}
		selected := key
		selectedName := peer.BindingName
		admit := func(ctx context.Context, evidence remote.Peer) error {
			if hex.EncodeToString(evidence.Key[:]) != selected {
				return remote.ErrTrust
			}
			current, _, err := p.services.providers.read()
			if err != nil {
				return err
			}
			for _, file := range current {
				if file.Declaration.Name == selectedName {
					return &pairedNameClash{name: selectedName}
				}
			}
			return p.state.admitContext(ctx, evidence)
		}
		host, err := router.NewRemoteWithDialer(peer.BindingName, address, "", remote.Dialer{TLS: config, AdmitContext: admit})
		if err != nil {
			return nil, err
		}
		host.BindingID = "paired:" + key + ":" + hex.EncodeToString(peer.Transcript[:])
		host.DeclaredBy = p.rights.self().Program
		host.Profiles = []string{router.ProfileChat, router.ProfileEmbed}
		hosts = append(hosts, host)
	}
	return hosts, nil
}
