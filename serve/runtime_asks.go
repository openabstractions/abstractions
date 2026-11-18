package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-identity/go/budget"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-identity/remote"
	"github.com/openabstractions/abstraction-inference/adapters/go/gateway"
	inference "github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
)

// firstUseActions are the shared catalogue's supported exact first-use actions:
// spending (research/rights-defaults/DECISION.md §2), lending (VISION 2026-09-22,
// research/model-bridge/DECISION.md rule 3), and named credential Use.
var firstUseActions = grants.FirstUseActions()

// maxFirstUseGenerations bounds how many retired questions one program, action
// and resource may follow with a new one.
const maxFirstUseGenerations = 16

// firstUseRefuseInterval is how long a refuse stands before the next refusal
// of the same program, action and resource asks again (ASK-R2): a refuse
// means "not this time", and the person is asked at most this often.
var firstUseRefuseInterval = grants.FirstUseRefuseInterval

// runtimeAsks composes the asks application book into the runtime: questions
// programs admit under the question.ask rule, the operator profile the
// operator programs answer through, and the runtime's own first-use questions.
type runtimeAsks struct {
	// openAsk orders an open question's pending read and its admission
	// (applicationV3OneCall); it is never held while waiting for an answer.
	openAsk  sync.Mutex
	book     *asks.Book
	endpoint string
	rights   *runtimeRights
	report   func(error)
	scope    string
	display  string
	// applied names the first-use questions whose delegated allow wrote its
	// target permit (ASK-R4 effect applied).
	applied *appliedEffects
	// tierFacts are what the runtime knows about a first-use resource for its
	// tier (grants.FirstUseTier, ASK-R6): which inference servers run here and
	// which registries it lists. The zero value knows nothing, so nothing on
	// a server or a registry is routine.
	tierFacts grants.TierFacts
	// servers lists the router's model servers, local and hosted, for a
	// permission request that joins a first-use question (ASK-A14). Nil
	// lists none.
	servers func() []*router.Host
	// settler withdraws pending typed requests a rights change settled
	// (ASK-A13, RIGHTS-R23).
	settler permissionSettler
	// logs receives the runtime's own diagnostic records; nil writes none.
	logs logging.Sink
}

// maxAppliedEffects bounds the applied-effect record, the same bound as the
// Book's admissions (ASK-P2); the oldest entry leaves first.
const maxAppliedEffects = asks.MaxApplicationAdmissions

// appliedEffects is the runtime's record, beside the Book, of first-use
// questions whose delegated allow applied its target permit. A later
// not_granted refusal for such a question means the permit lapsed, revoked or
// expired, and the question is asked again (ASK-R2). An allow whose effect
// conflicted or failed is absent, so its answered record stays for the
// operator's rule retry. A nil record knows of no applied effect.
//
// The file is read and changed through cas within maxAppliedEffectsBytes. A
// file that does not decode, or exceeds the bound, is reported once and set
// aside beside itself (first-use-effects.json.corrupt), and the record starts
// empty: those questions replay until a person dismisses them, as before
// their effect was recorded.
type appliedEffects struct {
	path   string
	report func(error)
	mu     sync.Mutex
}

// maxAppliedEffectsBytes bounds the file: maxAppliedEffects ids of 32 hex
// characters, indented, with room to spare.
const maxAppliedEffectsBytes = 512 << 10

type appliedEffectsFile struct {
	Applied []string `json:"applied"`
}

// errCorruptEffects marks a record that does not decode or exceeds its bound.
var errCorruptEffects = errors.New("first-use effects record is unreadable")

func decodeAppliedEffects(raw []byte) (appliedEffectsFile, error) {
	var f appliedEffectsFile
	if len(raw) == 0 {
		return f, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return f, fmt.Errorf("%w: %v", errCorruptEffects, err)
	}
	return f, nil
}

// setAside moves an unreadable record out of the way and reports it once.
// Callers hold e.mu.
func (e *appliedEffects) setAside(cause error) error {
	if err := os.Rename(e.path, e.path+".corrupt"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("setting aside %s: %w", e.path, err)
	}
	if e.report != nil {
		e.report(fmt.Errorf("%s set aside as %s.corrupt; questions answered before it ask nothing new until dismissed: %w", e.path, filepath.Base(e.path), cause))
	}
	return nil
}

func (e *appliedEffects) change(edit func([]string) []string) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	write := func() error {
		return cas.ChangeLimit(e.path, maxAppliedEffectsBytes, func(current []byte) ([]byte, error) {
			f, err := decodeAppliedEffects(current)
			if err != nil {
				return nil, err
			}
			f.Applied = edit(f.Applied)
			if len(f.Applied) > maxAppliedEffects {
				f.Applied = f.Applied[len(f.Applied)-maxAppliedEffects:]
			}
			raw, err := json.MarshalIndent(f, "", "  ")
			if err != nil {
				return nil, err
			}
			return append(raw, '\n'), nil
		})
	}
	err := write()
	if errors.Is(err, errCorruptEffects) || errors.Is(err, cas.ErrTooLarge) {
		if err = e.setAside(err); err == nil {
			err = write()
		}
	}
	return err
}

func (e *appliedEffects) add(id string) error {
	return e.change(func(ids []string) []string {
		if slices.Contains(ids, id) {
			return ids
		}
		return append(ids, id)
	})
}

func (e *appliedEffects) remove(id string) error {
	return e.change(func(ids []string) []string {
		return slices.DeleteFunc(ids, func(s string) bool { return s == id })
	})
}

func (e *appliedEffects) has(id string) (bool, error) {
	if e == nil {
		return false, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	raw, err := cas.ReadLimit(e.path, maxAppliedEffectsBytes)
	var f appliedEffectsFile
	if err == nil {
		f, err = decodeAppliedEffects(raw)
	}
	if errors.Is(err, errCorruptEffects) || errors.Is(err, cas.ErrTooLarge) {
		return false, e.setAside(err)
	}
	if err != nil {
		return false, err
	}
	return slices.Contains(f.Applied, id), nil
}

func asksEndpoint(options runtimeFlags) (string, error) {
	return options.companionEndpoint("asks-application-v1", "asks", "asks")
}

// withdrawStale withdraws every pending question bound to the previous
// runtime's memory (asks ASK-S1): a system-access question's ticket died with
// that runtime, so it must never be answered after this start. The runtime
// calls it only once it owns its endpoints (host.Listen succeeded) and before
// it serves any request, so a second runtime that cannot bind the endpoints
// leaves the live runtime's questions alone.
func (a *runtimeAsks) withdrawStale() error {
	if a == nil || a.book == nil {
		return nil
	}
	withdrawn, err := a.book.WithdrawRuntimeBound()
	if err != nil {
		return fmt.Errorf("asks: withdraw the previous runtime's system-access questions: %w", err)
	}
	if len(withdrawn) > 0 && a.report != nil {
		a.report(fmt.Errorf("asks: withdrew %d system-access question(s) left pending by the previous runtime", len(withdrawn)))
	}
	return nil
}

// composeAsks opens the application book in the runtime state directory. It
// needs the runtime's rights: asking is decided by a rule, and first-use
// questions follow its decisions.
func composeAsks(options runtimeFlags, r *runtimeRights, report func(error)) (*runtimeAsks, error) {
	if r == nil {
		return nil, nil
	}
	state, err := credentialsState(options)
	if err != nil || state == "" {
		return nil, err
	}
	dir := filepath.Join(state, "asks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	book, err := asks.LoadApplicationBook(filepath.Join(dir, "application.json"))
	if err != nil {
		return nil, err
	}
	if err := keepOpenQuestions(book); err != nil {
		return nil, err
	}
	endpoint, err := asksEndpoint(options)
	if err != nil {
		return nil, err
	}
	// The runtime asks as itself, in the scope the application service would
	// derive for its executable.
	self := r.self()
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("asks: observe runtime executable: %w", err)
	}
	a := &runtimeAsks{book: book, endpoint: endpoint, rights: r, report: report,
		scope: asks.ApplicationOwnerProgramScope(r.kind, self.Account, executable), display: asks.AskerDisplay(executable),
		applied: &appliedEffects{path: filepath.Join(dir, "first-use-effects.json"), report: report}}
	r.firstUse = a.admitFirstUse
	r.awaitQuestions = a.awaitQuestions
	r.pendingQuestions = a.pendingQuestions
	r.pairedInput = a.admitPairedInput
	r.typedFirstUse = a.admitTypedService
	return a, nil
}

// authorizeOperator admits the rights operator programs to answer questions.
func (a *runtimeAsks) authorizeOperator(ctx context.Context, peer *identity.Peer) error {
	if a.rights.authorizeOperator(ctx, peer) != nil {
		return asksservice.ErrOperatorForbidden
	}
	return ctx.Err()
}

// askPolicy refuses the runtime's own question key to applications, then
// decides abstraction.asks/question.ask on account.
func (a *runtimeAsks) askPolicy(decisions host.Decider) asksservice.AskPolicy {
	decide := host.AskPolicyFromRights(decisions)
	return func(ctx context.Context, peer *identity.Peer, key string) error {
		if asks.RuntimeOnlyKey(key) {
			return fmt.Errorf("asks: %s is admitted by the runtime only", key)
		}
		if key == "opencode.project.edit" {
			// The native session owner admits this human-only occurrence. The
			// answer carries no folder authority; the session binds its effect.
			return a.authorizeOperator(ctx, peer)
		}
		return decide(ctx, peer, key)
	}
}

// configure publishes the application and operator profiles on one endpoint
// and registers the asks rights action.
func (a *runtimeAsks) configure(o *host.Options) {
	if a == nil {
		return
	}
	o.QuestionBook, o.QuestionEndpoint, o.QuestionOperator = a.book, a.endpoint, a.authorizeOperator
	o.QuestionPresenter = applicationSession
	o.QuestionAskPolicy = a.askPolicy(a.rights.decider())
	o.QuestionNotificationPolicy, o.QuestionNotificationBlocked = a.notificationPolicy, a.notificationBlocked
	o.QuestionResponder = a.authorizeResponder
	o.QuestionResponderWait = a.responderEnrolled
	o.QuestionChoiceResponder = a.authorizeChoiceResponder
	o.QuestionAnsweringRequest = a.requestAnswering
	o.QuestionPermissionPrepare = a.preparePermission
	o.QuestionPermissionObserve = a.observePermission
	o.RightsFirstUseAllow = a.applyOperatorAllow
	o.RightsPermissionAllow, o.RightsPermissionUndo = a.applyPermissionAllow, a.undoPermissionEffect
	o.RightsActions = append(o.RightsActions, askswire.ResourceActions...)
}

// applyOperatorAllow connects the Book's actual admitted occurrence to its
// complete policy effect. The Book stays pinned until the rights replacement;
// both lock waits share the receiving call's context and allowSaveBudget.
//
// An Allow that is not saved is released, so the app's next first use asks
// afresh. A provider connection keeps its recorded Allow for its own retry.
func (a *runtimeAsks) applyOperatorAllow(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error) (rwire.PolicyEdit, error) {
	key := a.unsavedReaskKey(id)
	result, provider, attempted, err := a.saveOperatorAllow(ctx, by, expected, id, authorize)
	if err == nil && result.Outcome == rwire.PolicyEditOutcomeApplied {
		unsavedReasks.saved(key)
	}
	if !provider && attempted && allowNotSaved(result.Outcome, err) {
		a.releaseUnsavedAllow(id, by, result.Outcome.String(), err, authorize)
	}
	return result, err
}

func (a *runtimeAsks) saveOperatorAllow(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error) (_ rwire.PolicyEdit, provider, attempted bool, _ error) {
	ctx, cancel := context.WithTimeout(ctx, allowSaveBudget)
	defer cancel()
	result := rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeInvalid}
	// Meta-rule and native message permits need eligibility preparation. Identify that from
	// the retained occurrence, release Book, then prepare before the pinned edit.
	meta := false
	var providerPair *asks.PermissionRequest
	_, err := a.book.WithApplicationRecordContext(ctx, id, func(record asks.Record, scope string, _ []string) error {
		if permission, ok := asks.PermissionForRecord(a.rights.owner, record); ok {
			for _, target := range permission.Targets {
				meta = meta || rights.IsMetaRule(target.Action, target.Resource) || target.Action == ActionApplicationMessage
			}
			_, pair := asks.ProviderPermissionName(permission)
			provider = provider || pair
			if pair && !record.Pending() && record.Option == "allow" && scope == a.scope && by.Account == a.rights.owner {
				copy := permission
				copy.Targets = slices.Clone(permission.Targets)
				providerPair = &copy
			}
		}
		return nil
	})
	if err != nil {
		return result, provider, attempted, err
	}
	var providerGuard func() error
	if providerPair != nil {
		if authorize == nil {
			return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeUnavailable}, provider, attempted, nil
		}
		if err := authorize(); err != nil {
			return result, provider, attempted, err
		}
		if applied, err := a.rights.policy.AppliedFirstUseAllow(id); err != nil {
			return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeUnavailable}, provider, attempted, nil
		} else if !applied {
			if a.rights.providerSetupGuard == nil {
				return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeUnavailable}, provider, attempted, nil
			}
			providerGuard, err = a.rights.providerSetupGuard(ctx, *providerPair)
			if err != nil {
				return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeUnavailable}, provider, attempted, nil
			}
		}
	}
	if meta {
		ctx, err = a.rights.policy.PrepareMetaHolderContext(ctx)
		if err != nil {
			return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeUnavailable}, provider, attempted, err
		}
	}
	var providerGuardFailure error
	_, err = a.book.WithApplicationRecordContext(ctx, id, func(record asks.Record, scope string, retained []string) error {
		if scope != a.scope || record.Pending() || record.Option != "allow" || record.AnsweredBy == asks.AnsweredByResponder || by.Account != a.rights.owner {
			return nil
		}
		permission, ok := asks.PermissionForRecord(a.rights.owner, record)
		if !ok || grants.FirstUseMode(permission.Targets[0].Action, permission.Targets[0].Resource) == rwire.PermissionFirstUseNone {
			return nil
		}
		if _, pair := asks.ProviderPermissionName(permission); pair && (providerPair == nil || !slices.Equal(permission.Targets, providerPair.Targets)) {
			return nil
		}
		rules := grants.FirstUsePermissionRules(a.rights.owner, askclient.RecordMetadata{ID: record.ID, Key: record.Key, Option: record.Option}, permission)
		if len(rules) == 0 {
			return nil
		}
		permit := true
		edit := rights.RuleEdit{Permit: &permit, By: by, Why: grants.FirstUseWhy, Exact: true}
		if len(rules) == 1 && rights.IsMetaRule(rules[0].Action, rules[0].Resource) {
			// The enrollment asked at first use is the one the operator UI
			// writes: its lapse and its reason (ASK-R10, RIGHTS-R16).
			program := rules[0].Subject.Program
			edit.TTL, edit.Why = rights.MetaRuleLapse, grants.EnrollmentWhy(strings.TrimSuffix(filepath.Base(program), filepath.Ext(program)))
		}
		var err error
		attempted = true
		result, err = a.rights.policy.ApplyFirstUseAllow(ctx, expected, rules, edit, record.ID, retained, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if providerGuard != nil {
				if err := providerGuard(); err != nil {
					providerGuardFailure = err
					return err
				}
			}
			return authorize()
		})
		return err
	})
	if providerGuardFailure != nil {
		return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeUnavailable}, provider, attempted, nil
	}
	return result, provider, attempted, err
}

// answererWhy is the reason a permit an enrolled answerer gave records; the
// Panel shows it attributed and nothing decides by it (RIGHTS-O8).
const answererWhy = "answered for you"

// useTierFacts tells the answerer check which inference servers
// OpenAbstractions runs on this machine and which registries the runtime
// lists (grants.TierFacts). A server is routine only as a native provider
// reached over authenticated local IPC whose declaration has product
// standing (managed); a loopback HTTP endpoint can forward to a paid hosted
// service (ASK-R6).
func (a *runtimeAsks) useTierFacts(hosts func() []*router.Host, managed func(*router.Host) bool, registries []string) {
	if a == nil {
		return
	}
	a.servers = hosts
	listed := slices.Clone(registries)
	a.tierFacts = grants.TierFacts{ListedRegistry: func(name string) bool { return slices.Contains(listed, name) }}
	if hosts != nil && managed != nil {
		a.tierFacts.LocalServer = func(name string) bool {
			return slices.ContainsFunc(hosts(), func(h *router.Host) bool {
				return h != nil && h.Name == name && h.OnThisMachine() && h.Wire == router.WireNative && managed(h)
			})
		}
	}
}

// hosts is how the answerer check reads the router's current hosts for a
// first-use tier; nil without inference.
func (r *runtimeInference) hosts() func() []*router.Host {
	if r == nil || r.router == nil {
		return nil
	}
	return r.router.Hosts
}

// managedHost is how the answerer check reads product standing for a
// router host; nil without inference.
func (r *runtimeInference) managedHost() func(*router.Host) bool {
	if r == nil || r.providers == nil {
		return nil
	}
	return r.providers.managedHost
}

// answerer is who answers: the subject the call bound and its code's
// publisher (ASK-R7).
type answerer struct {
	subject   rwire.Subject
	publisher asks.Publisher
}

// authorizeResponder binds the answering peer and checks it as an answerer.
func (a *runtimeAsks) responderEnrolled(ctx context.Context, peer *identity.Peer) (bool, error) {
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != a.rights.owner {
		return false, asksservice.ErrResponderForbidden
	}
	enrolled, _, err := a.answererGate(ctx, answerer{subject: subject, publisher: peerPublisher(peer)})
	return enrolled, err
}

func (a *runtimeAsks) authorizeResponder(ctx context.Context, peer *identity.Peer, record asks.Record, scope, option string) (asksservice.ResponderEffect, error) {
	responder, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || responder.Account != a.rights.owner {
		return nil, asksservice.ErrResponderForbidden
	}
	return a.authorizeAnswerer(ctx, answerer{subject: responder, publisher: peerPublisher(peer)}, record, scope, option)
}

// authorizeAnswerer is the one answerer check (ASK-R3 to ASK-R7). It decides
// from the question and the answerer alone, with no rights read, before any
// rights read; from the rights policy before it opens a file; and returns the
// effect that writes the target rule only after every check. A program never
// answers a question about itself or one it asked, a question that is not
// routine, or as a script host, whatever its grant. ListAnswerable asks this
// same function, so a refused question is never listed.
func (a *runtimeAsks) authorizeAnswerer(ctx context.Context, who answerer, record asks.Record, scope, option string) (asksservice.ResponderEffect, error) {
	if scope != a.scope {
		return nil, asksservice.ErrResponderForbidden
	}
	if record.TypedPermission != nil {
		return a.authorizeApplicationAnswerer(ctx, who, record, option)
	}
	permission, ok := asks.PermissionForRecord(a.rights.owner, record)
	if !ok {
		return nil, asksservice.ErrResponderForbidden
	}
	if _, providerPair := asks.ProviderPermissionName(permission); providerPair {
		return nil, asksservice.ErrResponderForbidden
	}
	targetRequest := permission.Targets[0]
	program, action, resource := targetRequest.Program, targetRequest.Action, targetRequest.Resource
	if !ok || grants.FirstUseMode(action, resource) == rwire.PermissionFirstUseNone || (option != "allow" && option != "refuse") || !slices.Contains(record.Options, option) {
		return nil, asksservice.ErrResponderForbidden
	}
	// Application command permissions are answered only as the typed
	// FAC-M19 effect above, with all three operations and its party rules;
	// an untyped first-use record never carries one to an answerer.
	if strings.HasPrefix(action, "abstraction.facade/command.") {
		return nil, asksservice.ErrResponderForbidden
	}
	if !grants.FirstUseTier(action, resource, a.tierFacts).Answerable() {
		return nil, asksservice.ErrResponderForbidden
	}
	// An assistant cannot tell who an unsigned program is: only the person
	// answers its first-use questions.
	if a.rights.applications.unsignedProgram(ctx, program) {
		return nil, asksservice.ErrResponderForbidden
	}
	enrolled, _, err := a.answererGate(ctx, who)
	if err != nil {
		return nil, err
	}
	// An enrollment answers; without one, an exact delegation may.
	var exact *exactDelegation
	if !enrolled {
		if exact, err = a.exactDelegation(who.subject, program, action, resource, option); exact == nil {
			return nil, err
		}
	}
	if err := a.checkAnswerParties(ctx, who, asks.FirstUseParties(record, program)); err != nil {
		return nil, err
	}
	target := rwire.Subject{Account: a.rights.owner, Program: program}
	if exact == nil {
		return a.enrolledEffect(who, target, record, action, resource, option), nil
	}
	if option == "refuse" {
		return nil, ctx.Err()
	}
	return a.exactEffect(who, target, record, action, resource, exact), nil
}

// answererRule is the asks answerer rule composed with this runtime's facts:
// its account, its own program and the operator programs (asks
// AnswererRule).
func (a *runtimeAsks) answererRule() asks.AnswererRule {
	return a.rights.answererRule()
}

// answererGate is the part of the answerer check every question kind shares:
// the asks rule's answerer part (ASK-R3, ASK-R5, ASK-R7, ASK-R9: the
// receiving account, no foreign code, no operator program), then the
// enrollment, which only rights can read. It reports whether that
// enrollment is current; a question kind without exact delegations refuses
// when it is not. The check it returns serves the party rule that follows.
func (a *runtimeAsks) answererGate(ctx context.Context, who answerer) (bool, *asks.AnswerCheck, error) {
	enrolled := a.rights.decide(ctx, who.subject, asks.AnswerAction, rights.MetaScopeApps)
	if enrolled.Outcome == rwire.DecisionOutcomeUnavailable {
		return false, nil, rights.ErrMetaHolderUnavailable
	}
	check, err := a.rights.answererCheck(who.subject.Account, who.subject.Program, who.publisher)
	if err != nil {
		return false, nil, err
	}
	return enrolled.Outcome == rwire.DecisionOutcomePermitted, check, nil
}

// requestAnswering is responder@1's RequestAnswering (asks ASK-R10): it binds
// the peer and asks for its right to answer for the person.
func (a *runtimeAsks) requestAnswering(ctx context.Context, peer *identity.Peer) rwire.Decision {
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return rwire.Decision{Outcome: rwire.DecisionOutcomeForbidden}
	}
	return a.askForAnswering(ctx, answerer{subject: subject, publisher: peerPublisher(peer)})
}

// askForAnswering asks for who's enrollment like any other right: the
// answerer gate refuses a script host, an operator program and another
// account, and the runtime's own program is refused too; a current
// enrollment reads permitted; otherwise the decision is not_granted and the
// runtime admits its first-use question for abstraction.asks/question.answer
// on apps. That question is administrative, so the person answers it and an
// answerer never can (ASK-R6).
func (a *runtimeAsks) askForAnswering(ctx context.Context, who answerer) rwire.Decision {
	decision := a.answeringEligibility(ctx, who)
	if decision.Outcome == rwire.DecisionOutcomeNotGranted && a.rights.firstUse != nil {
		a.rights.firstUse(who.subject, func() asks.Publisher { return who.publisher }, asks.AnswerAction, rights.MetaScopeApps)
	}
	return decision
}

// answeringEligibility is shared by responder enrollment and the exact public
// permission request. Both retain ASK-R7's native caller exclusions.
func (a *runtimeAsks) answeringEligibility(ctx context.Context, who answerer) rwire.Decision {
	_, check, err := a.answererGate(ctx, who)
	switch {
	case errors.Is(err, asksservice.ErrResponderForbidden):
		return rwire.Decision{Outcome: rwire.DecisionOutcomeForbidden}
	case err != nil:
		return rwire.Decision{Outcome: rwire.DecisionOutcomeUnavailable}
	case check.Runtime():
		return rwire.Decision{Outcome: rwire.DecisionOutcomeForbidden}
	}
	return a.rights.decide(ctx, who.subject, asks.AnswerAction, rights.MetaScopeApps)
}

// authorizeChoiceResponder is the answerer check for a shared ordinary
// choice (asks ASK-R9): the same account, no script host, a current
// enrollment, no operator program and no party to the choice, by the one
// party rule first-use answers use. A choice has no tier and no exact
// delegation: its application named the options an assistant may choose.
// The returned effect renews the enrollment after the answer is recorded.
func (a *runtimeAsks) authorizeChoiceResponder(ctx context.Context, peer *identity.Peer, c asks.ChoiceCandidate) (asksservice.ChoiceResponderEffect, error) {
	holder, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return nil, asksservice.ErrResponderForbidden
	}
	return a.authorizeChoiceAnswerer(ctx, answerer{subject: holder, publisher: peerPublisher(peer)}, c)
}

// authorizeChoiceAnswerer is authorizeChoiceResponder for a bound answerer.
func (a *runtimeAsks) authorizeChoiceAnswerer(ctx context.Context, who answerer, c asks.ChoiceCandidate) (asksservice.ChoiceResponderEffect, error) {
	if c.AskerProgram == "" {
		return nil, asksservice.ErrResponderForbidden
	}
	enrolled, _, err := a.answererGate(ctx, who)
	if err != nil {
		return nil, err
	}
	if !enrolled {
		return nil, asksservice.ErrResponderForbidden
	}
	if err := a.checkAnswerParties(ctx, who, asks.ChoiceParties(c)); err != nil {
		return nil, err
	}
	holder := who.subject
	return func(effectCtx context.Context) {
		guard := func() error { return effectCtx.Err() }
		if _, err := a.rights.policy.ChangeRulesUnderAnswerContext(effectCtx, holder, rwire.Subject{}, nil, "", answererWhy, "", guard); err != nil {
			a.report(fmt.Errorf("asks: renewing the answerer's enrollment: %w", err))
		}
	}, nil
}

// exactDelegation is an exact, expiring question.answer permit for one
// target and option (ASK-R3), with the policy revision it was read at.
type exactDelegation struct {
	revision string
	expires  time.Time
}

// exactDelegation reads the answerer's exact delegation for one target and
// option. It is nil with ErrResponderForbidden when there is none, and nil
// with another error when the policy cannot be read.
func (a *runtimeAsks) exactDelegation(responder rwire.Subject, program, action, resource, option string) (*exactDelegation, error) {
	name, err := asks.FirstUseAnswerResource(program, action, resource, option)
	if err != nil {
		return nil, asksservice.ErrResponderForbidden
	}
	grant := a.rights.policy.ReadRule(responder, asks.AnswerAction, name)
	if grant.Outcome == rwire.RuleReadOutcomeUnavailable {
		return nil, errors.New("asks: responder rights unavailable")
	}
	if grant.Outcome != rwire.RuleReadOutcomeFound || grant.Record == nil || !grant.Record.Rule.Permit || grant.Record.Expires == "" ||
		grant.Record.Rule.Resource != name || grant.Record.Rule.Action != asks.AnswerAction || grant.Record.Rule.Subject.Account != responder.Account || !identity.SameSubjectProgram(grant.Record.Rule.Subject.Program, responder.Program) {
		return nil, asksservice.ErrResponderForbidden // a delegation must expire
	}
	expires, err := time.Parse(time.RFC3339Nano, grant.Record.Expires)
	if err != nil || !time.Now().Before(expires) {
		return nil, asksservice.ErrResponderForbidden
	}
	return &exactDelegation{revision: grant.Revision, expires: expires}, nil
}

// enrolledEffect writes an enrolled answerer's answer (ASK-R4, ASK-R5):
// through rights' ChangeRuleUnder, which reads the enrollment inside the
// edit, renews it, and writes each permit set by the answerer and expiring
// with the enrollment. A refuse writes no target rule and renews.
func (a *runtimeAsks) enrolledEffect(who answerer, target rwire.Subject, record asks.Record, action, resource, option string) asksservice.ResponderEffect {
	holder := who.subject
	return func(effectCtx context.Context, recheck func() error, firstAnswer bool) (askswire.ResponderEffectOutcome, string) {
		current := func() string { return a.rights.decide(effectCtx, target, action, resource).Outcome.String() }
		if !firstAnswer {
			return askswire.ResponderEffectOutcomeConflict, current()
		}
		guard := func() error {
			if err := effectCtx.Err(); err != nil {
				return err
			}
			if err := recheck(); err != nil {
				return err
			}
			return a.checkAnswerParties(effectCtx, who, asks.FirstUseParties(record, target.Program))
		}
		if option == "refuse" {
			if _, err := a.rights.policy.ChangeRulesUnderAnswerContext(effectCtx, holder, rwire.Subject{}, nil, "", answererWhy, "", guard); err != nil {
				a.report(fmt.Errorf("asks: renewing the answerer's enrollment: %w", err))
				if errors.Is(err, asks.ErrAnswererForbidden) {
					return askswire.ResponderEffectOutcomeForbidden, current()
				}
				return askswire.ResponderEffectOutcomeUnavailable, current()
			}
			return askswire.ResponderEffectOutcomeNone, current()
		}
		actions := []string{action}
		if grants.Paired(action, resource) {
			// One answer on an input or output profile's write also permits its
			// read for the same program and resource (INF-V4).
			actions = append(actions, contentReadAction)
		}
		edit, err := a.rights.policy.ChangeRulesUnderAnswerContext(effectCtx, holder, target, actions, resource, answererWhy, record.ID, guard)
		switch {
		case errors.Is(err, rights.ErrNoMetaRule) || errors.Is(err, rights.ErrMetaTarget) || errors.Is(err, rights.ErrMetaHolder) || errors.Is(err, asks.ErrAnswererForbidden) || (err == nil && edit.Outcome == rwire.PolicyEditOutcomeForbidden):
			return askswire.ResponderEffectOutcomeForbidden, current()
		case err != nil:
			return askswire.ResponderEffectOutcomeUnavailable, current()
		case edit.Outcome == rwire.PolicyEditOutcomeConflict:
			return askswire.ResponderEffectOutcomeConflict, current()
		case edit.Outcome != rwire.PolicyEditOutcomeApplied:
			return askswire.ResponderEffectOutcomeUnavailable, current()
		}
		// The permit landed: a later refusal of this question means it ended,
		// and asks again (ASK-R2).
		if err := a.applied.add(record.ID); err != nil {
			a.report(err)
		}
		return askswire.ResponderEffectOutcomeApplied, current()
	}
}

// exactEffect writes an exactly delegated allow (ASK-R3, ASK-R4): a target
// permit expiring within five minutes and before the delegation, through
// the revision the delegation was read at.
func (a *runtimeAsks) exactEffect(who answerer, target rwire.Subject, record asks.Record, action, resource string, grant *exactDelegation) asksservice.ResponderEffect {
	responder := who.subject
	expires := grant.expires
	return func(effectCtx context.Context, recheck func() error, firstAnswer bool) (askswire.ResponderEffectOutcome, string) {
		current := func() string { return a.rights.decide(effectCtx, target, action, resource).Outcome.String() }
		if !firstAnswer {
			return askswire.ResponderEffectOutcomeConflict, current()
		}
		if effectCtx.Err() != nil || recheck() != nil || !time.Now().Before(expires) {
			return askswire.ResponderEffectOutcomeForbidden, current()
		}
		remaining := time.Until(expires) - time.Second
		if remaining < time.Millisecond {
			return askswire.ResponderEffectOutcomeForbidden, current()
		}
		if remaining > 5*time.Minute {
			remaining = 5 * time.Minute
		}
		remaining = remaining.Truncate(time.Millisecond)
		permit := true
		rules := []rwire.PolicyRule{{Subject: target, Action: action, Resource: resource, Permit: true}}
		if grants.Paired(action, resource) {
			rules = append(rules, rwire.PolicyRule{Subject: target, Action: contentReadAction, Resource: resource, Permit: true})
		}
		edit, changeErr := a.rights.policy.ChangeRulesContext(effectCtx, grant.revision, rules,
			rights.RuleEdit{Permit: &permit, By: responder, TTL: remaining, Why: "delegated first-use answer", Exact: true, PreserveOtherSetter: true, Deadline: expires, QuestionID: record.ID},
			func() error {
				if err := effectCtx.Err(); err != nil {
					return err
				}
				if err := recheck(); err != nil {
					return err
				}
				if err := a.rights.refuseAnswerer(responder); err != nil {
					return err
				}
				if err := a.checkAnswerParties(effectCtx, who, asks.FirstUseParties(record, target.Program)); err != nil {
					return err
				}
				if !time.Now().Before(expires) {
					return asksservice.ErrResponderForbidden
				}
				return nil
			})
		if changeErr != nil {
			if errors.Is(changeErr, asksservice.ErrResponderForbidden) || errors.Is(changeErr, rights.ErrRuleDeadline) || errors.Is(changeErr, rights.ErrMetaHolder) {
				return askswire.ResponderEffectOutcomeForbidden, current()
			}
			return askswire.ResponderEffectOutcomeUnavailable, current()
		}
		switch edit.Outcome {
		case rwire.PolicyEditOutcomeApplied:
			// The permit landed: a later refusal of this question means it
			// lapsed, and asks again (ASK-R2). A failed record leaves the
			// answered question replaying, as before.
			if err := a.applied.add(record.ID); err != nil {
				a.report(err)
			}
			return askswire.ResponderEffectOutcomeApplied, current()
		case rwire.PolicyEditOutcomeConflict:
			return askswire.ResponderEffectOutcomeConflict, current()
		default:
			return askswire.ResponderEffectOutcomeUnavailable, current()
		}
	}
}

// peerPublisher is the publisher the platform verified for peer's code, as
// the Book would record it; a peer without code proof has none.
func peerPublisher(peer *identity.Peer) asks.Publisher {
	return requesterPublisher(peer)()
}

// admitFirstUse admits one question as the runtime, keyed by program, action
// and resource. A pending or answered question replays without a new one; a
// retired one lets the next generation of the key admit again. Paths or
// complete rights-valid targets are retained independently of display limits.
// publisher is called once,
// only when a new question is admitted, and its evidence is recorded with it
// (ASK-A6). It returns the id of the question pending for the target, or ""
// when none is: the gateway window holds a call open on it (INF-W10).
func (a *runtimeAsks) admitFirstUse(subject rwire.Subject, publisher func() asks.Publisher, action, resource string) string {
	if movedConnectorCaller(subject.Program) {
		return ""
	}
	if strings.HasPrefix(action, "abstraction.facade/command.") {
		return "" // the native owner admits its complete typed occurrence
	}
	// A bare input-profile question is refused here: only a consuming request
	// admits one, through admitPairedInput (INF-V4).
	if grants.FirstUseMode(action, resource) != rwire.PermissionFirstUseExact {
		return ""
	}
	if action == inference.ActionComplete {
		if permission, ok := a.providerCompletionPermission(subject, resource); ok {
			return a.admitPermissionPrepared(subject, publisher, permission)
		}
	}
	return a.admit(subject, publisher, action, resource)
}

// admitPairedInput admits the first-use question on content.write for an
// input profile resource, which a consuming request of the same proven
// program raised, or for an output profile resource, which a call or job of
// that program was refused for want of (INF-V4, INF-S7). Its allow writes content.write and
// content.read on that resource (grants.FirstUseRules, authorizeResponder).
func (a *runtimeAsks) admitPairedInput(subject rwire.Subject, publisher func() asks.Publisher, resource string) string {
	if movedConnectorCaller(subject.Program) {
		return ""
	}
	if !grants.Paired(contentWriteAction, resource) {
		return ""
	}
	return a.admit(subject, publisher, contentWriteAction, resource)
}

// asksAgain decides, for an answered first-use question met by a fresh
// not_granted refusal, whether it is asked again (ASK-R2):
//   - allow, when its delegated effect applied or its complete operator effect
//     has durable rights-owned proof: the
//     refusal means the permit lapsed, revoked or expired. An allow whose
//     effect conflicted or failed keeps its record for explicit rule Retry.
//   - refuse, once firstUseRefuseInterval has passed since the answer: a
//     refuse means "not this time".
//   - never: not asked again; it stands as its deny rule.
func (a *runtimeAsks) asksAgain(record asks.Record) (bool, error) {
	switch record.Option {
	case "allow":
		if record.AnsweredBy != asks.AnsweredByResponder && a.rights != nil && a.rights.policy != nil {
			return a.rights.policy.AppliedFirstUseAllow(record.ID)
		}
		return a.applied.has(record.ID)
	case "refuse":
		return !record.Answered.IsZero() && time.Since(record.Answered) >= firstUseRefuseInterval, nil
	}
	return false, nil
}

// stillNotGranted reads the current decision for the question's program,
// action and resource. A paired input or output question's allow wrote both
// content.write and content.read (INF-V4), so both are read: it is asked
// again when either reads not_granted and the other permitted or
// not_granted. Any other outcome, a denial or an unavailable read included,
// keeps the answered question.
func (a *runtimeAsks) permissionStillNotGranted(permission asks.PermissionRequest) bool {
	if a.rights == nil || a.rights.policy == nil {
		return false
	}
	lapsed := false
	for _, target := range permission.Targets {
		subject := rwire.Subject{Account: target.Account, Program: target.Program}
		switch a.rights.decide(context.Background(), subject, target.Action, target.Resource).Outcome {
		case rwire.DecisionOutcomeNotGranted:
			lapsed = true
		case rwire.DecisionOutcomePermitted:
		default:
			return false
		}
	}
	return lapsed
}

func (a *runtimeAsks) admit(subject rwire.Subject, publisher func() asks.Publisher, action, resource string) string {
	if action == ActionApplicationMessage {
		if a.rights == nil || a.rights.policy == nil || a.rights.applications == nil {
			return ""
		}
		ctx, err := a.rights.policy.PrepareMetaHolderContext(context.Background())
		if err != nil {
			return ""
		}
		d := a.rights.applications
		if !d.lock(ctx) {
			return ""
		}
		defer d.unlock()
		name, ok := messageApplicationResource(resource)
		roles := d.roles.Load()
		if !ok || roles == nil || roles.messaging == nil || roles.messaging.current(ctx) != nil {
			return ""
		}
		found := false
		for _, agent := range roles.messaging.agents {
			if agent.name == name {
				found = true
			}
		}
		if !found {
			return ""
		}
	}
	return a.admitPrepared(subject, publisher, action, resource)
}

// firstUseKey is the admission key of the runtime's first-use question for
// one program, action and resource (ASK-R2).
func firstUseKey(program, action, resource string) string {
	sum := sha256.Sum256([]byte(program + "\x00" + action + "\x00" + resource))
	return "first-use:" + hex.EncodeToString(sum[:20])
}

// firstUseGenerationKey is key for a question asked again after its earlier
// generations were retired.
func firstUseGenerationKey(key string, generation int) string {
	if generation > 0 {
		return key + "." + strconv.Itoa(generation)
	}
	return key
}

func (a *runtimeAsks) admitPrepared(subject rwire.Subject, publisher func() asks.Publisher, action, resource string) string {
	permission, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: subject.Account, Program: subject.Program, Action: action, Resource: resource})
	if err != nil || a.rights == nil || subject.Account != a.rights.owner {
		a.report(fmt.Errorf("asks: first-use target refused: %v", err))
		return ""
	}
	return a.admitPermissionPrepared(subject, publisher, permission)
}

func (a *runtimeAsks) admitPermissionPrepared(subject rwire.Subject, publisher func() asks.Publisher, permission asks.PermissionRequest) string {
	if !asks.ValidPermissionRequest(permission) || a.rights == nil || subject.Account != a.rights.owner || movedConnectorCaller(subject.Program) {
		return ""
	}
	for _, target := range permission.Targets {
		if target.Account != subject.Account || !identity.SameSubjectProgram(target.Program, subject.Program) {
			return ""
		}
	}
	subject.Program = permission.Targets[0].Program
	key := firstUsePermissionKey(permission)
	via := listen.Seen{Why: "admitted by the runtime after a not_granted decision"}
	for generation := 0; generation < maxFirstUseGenerations; generation++ {
		requestKey := firstUseGenerationKey(key, generation)
		record, outcome, err := a.book.ObserveApplication(a.scope, requestKey)
		if err == nil && outcome == "answered" {
			var ask bool
			// The refusal that called admit was decided before this
			// observation; an allow may have landed in between. The decision
			// is read again, and only a current not_granted releases.
			if ask, err = a.asksAgain(record); err == nil && ask && a.permissionStillNotGranted(permission) {
				if _, err = a.book.ReleaseAnsweredApplicationID(a.scope, requestKey, record.ID, record.Option); err == nil {
					if err = a.applied.remove(record.ID); err == nil {
						record, outcome, err = a.book.ObserveApplication(a.scope, requestKey)
					}
				}
			}
		}
		if err == nil && outcome == "unknown" && unsavedReasks.held(unsavedTargetKey(permission)) {
			return ""
		}
		if err == nil && outcome == "unknown" {
			evidence := asks.Publisher{}
			if publisher != nil {
				evidence = publisher()
			}
			record, outcome, err = a.book.AskPermissionFrom(a.scope, a.display, requestKey, permission, via, evidence)
		}
		if err != nil {
			a.report(err)
			return ""
		}
		if outcome != "gone" {
			if outcome == "invalid" || outcome == "conflict" {
				a.report(fmt.Errorf("asks: first-use admission %s", outcome))
				return ""
			}
			if !record.Pending() {
				return ""
			}
			return record.ID
		}
	}
	return ""
}

// awaitQuestions holds a gateway window call whose refusal admitted the
// first-use questions ids (INF-W10). It returns true once every target of a
// pending or allowed question reads permitted, and false on a Block, once
// each question is answered otherwise or gone, or ctx ends. An answer and each committed
// rights change read the questions again; nothing polls. A question about
// another program than subject is not waited on.
func (a *runtimeAsks) awaitQuestions(ctx context.Context, subject rwire.Subject, ids []string) bool {
	return a.awaitQuestionsChecking(ctx, subject, ids, nil)
}

func (a *runtimeAsks) awaitQuestionsChecking(ctx context.Context, subject rwire.Subject, ids []string, typed func(asks.TypedPermission) (bool, bool)) bool {
	if a == nil || a.rights == nil || a.rights.policy == nil || subject.Account != a.rights.owner || len(ids) == 0 {
		return false
	}
	wake := make(chan struct{}, 1)
	signal := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	stop := a.rights.policy.OnChange(signal)
	defer stop()
	for {
		if ctx.Err() != nil {
			return false
		}
		open := false
		var pending []string
		for _, id := range ids {
			record, found, err := a.book.ApplicationRecord(id)
			if err != nil {
				a.report(err)
				return false
			}
			if !found {
				continue
			}
			permission, ok := asks.PermissionForRecord(a.rights.owner, record)
			isTyped := record.TypedPermission != nil && typed != nil && record.TypedPermission.Effect.Subject == subject
			if !isTyped && (!ok || !samePrograms(permission.Targets[0].Program, subject.Program)) {
				continue
			}
			if record.Pending() || record.Option == "allow" {
				var permitted, denied bool
				if isTyped {
					permitted, denied = typed(*record.TypedPermission)
				} else {
					permitted, denied = a.permissionWaitDecision(ctx, permission)
				}
				if ctx.Err() != nil {
					return false
				}
				if permitted {
					return true
				}
				if denied {
					return false
				}
				// An unanswered question may be settled by a saved rule/default;
				// an Allow's effect may still be waiting for its rights commit.
				open = true
				if record.Pending() {
					pending = append(pending, id)
				}
			}
		}
		if !open {
			return false
		}
		done := make(chan struct{})
		stops := make([]func(), 0, len(pending))
		for _, id := range pending {
			ch, stopWaiting := a.book.ObserveQuestionWait(id)
			stops = append(stops, stopWaiting)
			go func() {
				select {
				case <-ch:
					signal()
				case <-done:
				}
			}()
		}
		select {
		case <-wake:
		case <-ctx.Done():
		}
		close(done)
		for _, stop := range stops {
			stop()
		}
	}
}

// pendingQuestions returns, in order, the ids among ids whose first-use
// question is still pending and asks about subject's own program on this
// runtime's account (INF-W11). A question about another program, an
// answered or expired one, and an unknown id are left out.
func (a *runtimeAsks) pendingQuestions(subject rwire.Subject, ids []string) ([]string, error) {
	if a == nil || a.rights == nil || subject.Account != a.rights.owner {
		return nil, nil
	}
	var pending []string
	for _, id := range ids {
		if slices.Contains(pending, id) {
			continue
		}
		record, found, err := a.book.ApplicationRecord(id)
		if err != nil {
			return nil, err
		}
		if !found || !record.Pending() {
			continue
		}
		// A typed service question (decideTypedAsking) asks about its
		// effect's subject.
		if typed := record.TypedPermission; typed != nil {
			if typed.Effect.Subject.Account == a.rights.owner && samePrograms(typed.Effect.Subject.Program, subject.Program) {
				pending = append(pending, id)
			}
			continue
		}
		permission, ok := asks.PermissionForRecord(a.rights.owner, record)
		if !ok || !samePrograms(permission.Targets[0].Program, subject.Program) {
			continue
		}
		pending = append(pending, id)
	}
	return pending, nil
}

// permissionWaitDecision reads the complete current decision without asking.
// A saved allow/default releases the wait; an explicit Block ends it at once.
func (a *runtimeAsks) permissionWaitDecision(ctx context.Context, permission asks.PermissionRequest) (permitted, denied bool) {
	permitted = true
	for _, t := range permission.Targets {
		switch a.rights.decide(ctx, rwire.Subject{Account: t.Account, Program: t.Program}, t.Action, t.Resource).Outcome {
		case rwire.DecisionOutcomePermitted:
		case rwire.DecisionOutcomeDenied:
			return false, true
		default:
			permitted = false
		}
	}
	return permitted, false
}

// requesterPublisher is the evidence of who signed the requesting program: the
// code the runtime bound for peer (ASK-A6). A decision that carries no peer
// records no publisher. The file at the program path is not evidence about
// the caller, which can place a signed file there for the moment of its
// refusal.
func requesterPublisher(peer *identity.Peer) func() asks.Publisher {
	return func() asks.Publisher {
		if peer == nil {
			return asks.Publisher{}
		}
		code, proof := peer.Code.Get()
		if proof <= identity.ProofNone {
			return asks.Publisher{}
		}
		return asks.CodePublisher(code)
	}
}

// decisionBudget is the least time one in-process decision has, and its
// bound when the caller set no deadline. A caller deadline with more time
// remaining bounds the decision instead (budget.Within), so a person's call
// is decided under load. A policy read normally takes milliseconds.
// The config and logging Go clients wait their DefaultTimeout, two seconds,
// by default, and the budget
// leaves the service time to answer unavailable inside that wait.
const decisionBudget = rights.DecisionReadBudget

// allowSaveBudget bounds the save of a person's answer: its Book and policy
// lock waits, the operator recheck and the policy write. The person already
// answered, and an Allow that misses its budget is released and asked again
// (runtime_permission_unsaved.go). A busy disk, such as an installer build
// running beside the runtime, takes longer than decisionBudget; the save
// waits for it. It stays inside the Panel's and the popup's five-second call
// (monitor panelCall, trayAnswerBudget).
const allowSaveBudget = 3 * time.Second

// decide reads one decision within the caller's deadline, at least decisionBudget. A
// decision that does not read in time is unavailable. Nothing is cached.
func (r *runtimeRights) decide(ctx context.Context, subject rwire.Subject, action, resource string) rwire.Decision {
	if movedConnectorCaller(subject.Program) {
		return rwire.Decision{Outcome: rwire.DecisionOutcomeDenied}
	}
	ctx, cancel := budget.Within(ctx, decisionBudget, decisionBudget)
	defer cancel()
	ctx, note := r.bindCaller(ctx, subject)
	decision := r.policy.DecideContext(ctx, subject, action, resource)
	r.admitted(note, decision.Outcome == rwire.DecisionOutcomePermitted, subject, action, resource)
	return decision
}

// Require decides through the in-process policy, within decisionBudget. A
// not_granted decision on a spending action also admits the first-use
// question; the call is refused all the same, at once.
func (r *runtimeRights) Require(ctx context.Context, peer *identity.Peer, action, resource string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx = rights.WithBoundPeer(ctx, peer)
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return err
	}
	decision, question := r.decideAskingForQuestion(ctx, subject, peer, action, resource)
	inference.AskedFrom(ctx).Add(question)
	if decision.Outcome != rwire.DecisionOutcomePermitted {
		return pendingFirstUseRefusal(&rightsclient.DecisionError{Outcome: decision.Outcome.String(), PolicyRevision: decision.PolicyRevision}, action, question)
	}
	return nil
}

var _ host.QuietDecider = (*runtimeRights)(nil)

// RequireQuietly is Require without the first-use question: a decision read
// only to find a deny (host.QuietDecider) asks the person nothing.
func (r *runtimeRights) RequireQuietly(ctx context.Context, peer *identity.Peer, action, resource string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx = rights.WithBoundPeer(ctx, peer)
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return err
	}
	decision := r.decide(ctx, subject, action, resource)
	if decision.Outcome != rwire.DecisionOutcomePermitted {
		return &rightsclient.DecisionError{Outcome: decision.Outcome.String(), PolicyRevision: decision.PolicyRevision}
	}
	return nil
}

// decideAsking is decide for a subject a service already bound, admitting the
// first-use question on not_granted. The question's publisher is the code of
// the peer the service bound subject from, which ctx carries
// (inference.WithPeer). A decision without that peer records no publisher.
func (r *runtimeRights) decideAsking(ctx context.Context, subject rwire.Subject, action, resource string) rwire.Decision {
	return r.decideAskingFor(ctx, subject, boundPeer(ctx, subject), action, resource)
}

// boundPeer is the peer ctx carries when it binds subject. A peer that binds
// another subject, or none, is not evidence about subject. The subject a peer
// binds is its route's: the gateway window's for a loopback peer, the native
// service's for any other.
func boundPeer(ctx context.Context, subject rwire.Subject) *identity.Peer {
	peer := inference.PeerFromContext(ctx)
	if peer == nil {
		return nil
	}
	bindSubject := inferenceservice.SubjectFromPeer
	if peer.Transport == identity.TransportLoopback {
		bindSubject = gateway.LoopbackSubject
	}
	bound, err := bindSubject(peer)
	if err != nil || bound.Account != subject.Account || bound.Program != subject.Program {
		return nil
	}
	return peer
}

// decideAskingFor is decideAsking with the peer the subject was bound from,
// whose code is the question's publisher.
func (r *runtimeRights) decideAskingFor(ctx context.Context, subject rwire.Subject, peer *identity.Peer, action, resource string) rwire.Decision {
	decision, question := r.decideAskingForQuestion(ctx, subject, peer, action, resource)
	inference.AskedFrom(ctx).Add(question)
	return decision
}

func (r *runtimeRights) decideAskingForQuestion(ctx context.Context, subject rwire.Subject, peer *identity.Peer, action, resource string) (rwire.Decision, string) {
	decision := r.decide(ctx, subject, action, resource)
	if r.firstUse != nil && decision.Outcome == rwire.DecisionOutcomeNotGranted {
		return decision, r.firstUse(subject, requesterPublisher(peer), action, resource)
	}
	return decision, ""
}

// askInput is inference.Config.AskInput: a consuming request of s (image edit,
// vision or a video job for input:image; transcription for input:audio) was
// refused for want of a grant. When the program holds neither a denial nor
// both rights on that input profile, the paired first-use question is
// admitted for the same program; its publisher is the code of the peer the
// request was bound from (INF-V4). An unavailable decision asks nothing.
func (r *runtimeRights) askInput(ctx context.Context, s inference.Subject, profile string) {
	r.askPaired(ctx, s, inference.InputResource(profile))
}

// askOutput is called by runtimeContent.prepareWrite: a call or job of s was
// refused for want of writing and reading back its output under an output
// profile (image, speech or video). When the program holds neither a denial nor both rights on
// output:<profile>, the paired first-use question is admitted for the same
// program; its answer covers only outputs the program's own work produced,
// since a generated output is read back only by the account and program its
// provenance names (INF-V4, INF-S7).
func (r *runtimeRights) askOutput(ctx context.Context, s inference.Subject, profile string) {
	r.askPaired(ctx, s, "output:"+profile)
}

// askPaired admits the paired first-use question on resource for s, unless
// s already holds both rights on it or a denial of either; an unavailable
// decision asks nothing.
func (r *runtimeRights) askPaired(ctx context.Context, s inference.Subject, resource string) {
	inference.AskedFrom(ctx).Add(r.askPairedQuestion(ctx, s, resource))
}

func (r *runtimeRights) askPairedQuestion(ctx context.Context, s inference.Subject, resource string) string {
	if r == nil || r.pairedInput == nil || !grants.Paired(contentWriteAction, resource) {
		return ""
	}
	subject := rwire.Subject{Account: s.Account, Program: s.Program}
	granted := 0
	for _, action := range []string{contentWriteAction, contentReadAction} {
		switch r.decide(ctx, subject, action, resource).Outcome {
		case rwire.DecisionOutcomePermitted:
			granted++
		case rwire.DecisionOutcomeNotGranted:
		default:
			return ""
		}
	}
	if granted == 2 {
		return ""
	}
	return r.pairedInput(subject, requesterPublisher(boundPeer(ctx, subject)), resource)
}

// nativeAdmissionQuestions recognizes the private collector/frame association
// made by the native service. Job preparation and gateway collectors own other
// waits; a deadline, copied peer or collector alone cannot transfer ownership.
func (r *runtimeRights) nativeAdmissionQuestions(ctx context.Context, subject rwire.Subject) *inference.Asked {
	if r == nil || r.awaitQuestions == nil || ctx.Err() != nil || ctx.Done() == nil {
		return nil
	}
	asked := inference.AskedFrom(ctx)
	if asked == nil {
		return nil
	}
	frame, ok := ctx.Value(asked).(*listen.FramedCall)
	if !ok || frame == nil || ctx.Done() != frame.WaitContext().Done() {
		return nil
	}
	peer := boundPeer(ctx, subject)
	if peer == nil || peer.Transport == identity.TransportLoopback {
		return nil
	}
	bound, err := frame.Peer()
	if err != nil {
		return nil
	}
	actual, err := inferenceservice.SubjectFromPeer(bound)
	if err != nil || actual.Account != subject.Account || actual.Program != subject.Program {
		return nil
	}
	return asked
}

// decideInference is inference.Config.Decide. A native or window caller's ctx
// carries the peer its route bound. The remote route carries none.
func (r *runtimeRights) decideInference(ctx context.Context, s inference.Subject, action, resource string) (string, error) {
	if call, ok := ctx.Value(incomingRemoteKey{}).(incomingRemoteCall); ok {
		if r == nil || !call.matchesSubject(r.owner, s.Account, s.Program) {
			return rwire.DecisionOutcomeForbidden.String(), nil
		}
		return r.decideRemote(ctx, call.peer, call.admission(ctx), action, resource, call.minimum).Outcome.String(), nil
	}
	if remote.ValidSubjectProgram(s.Program) {
		return rwire.DecisionOutcomeUnavailable.String(), nil
	}
	subject := rwire.Subject{Account: s.Account, Program: s.Program}
	if inference.PreloadAdmission(ctx) {
		return r.decide(ctx, subject, action, resource).Outcome.String(), nil
	}
	return r.decideAsking(ctx, subject, action, resource).Outcome.String(), nil
}

// awaitNativeQuestions is composed only on the native service host. The
// collector and exact framed lifetime remain the authority to own this wait.
func (r *runtimeRights) awaitNativeQuestions(ctx context.Context, s inference.Subject, ids []string) (bool, error) {
	subject := rwire.Subject{Account: s.Account, Program: s.Program}
	// The binding is the admitting frame's; ctx is the service's bounded
	// hold within it (INF-W11).
	admitting := inferenceservice.AdmissionContext(ctx)
	if r.nativeAdmissionQuestions(admitting, subject) == nil {
		return false, errors.New("native admission binding unavailable")
	}
	retry := r.awaitQuestions(ctx, subject, ids)
	if err := admitting.Err(); err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return false, nil
	}
	if r.nativeAdmissionQuestions(admitting, subject) == nil {
		return false, errors.New("native admission binding unavailable")
	}
	return retry, nil
}

// pendingNativeQuestions is composed only on the native service hosts. It
// names the refused admission's questions still pending about the caller
// once the bounded hold released no retry; the caller waits for the person
// through asks permission@1 ObserveQuestion (INF-W11).
func (r *runtimeRights) pendingNativeQuestions(ctx context.Context, s inference.Subject, ids []string) ([]string, error) {
	subject := rwire.Subject{Account: s.Account, Program: s.Program}
	if r.nativeAdmissionQuestions(ctx, subject) == nil {
		return nil, errors.New("native admission binding unavailable")
	}
	if r.pendingQuestions == nil {
		return nil, nil
	}
	return r.pendingQuestions(subject, ids)
}
