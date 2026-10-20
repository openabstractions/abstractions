package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	watch "github.com/openabstractions/abstraction-watch/go"
)

const RegistryV3Contract = "abstraction.facade/registry@3"

func (r *runtimeRegistry) programDirectory() *applicationDirectory {
	if r == nil || r.operator == nil || r.operator.credentials == nil || r.operator.credentials.runtimeRights == nil {
		return nil
	}
	return r.operator.credentials.runtimeRights.applications
}

func (r *runtimeRegistry) registrationGate(ctx context.Context, caller inference.Subject) (registration.Outcome, *rights.RefusalDetail) {
	if r == nil || r.operator == nil || r.operator.credentials == nil || caller.Account == "" || caller.Program == "" || caller.Account != r.operator.credentials.owner {
		return registration.OutcomeForbidden, &rights.RefusalDetail{Source: rights.RefusalSourceIdentity, Decision: rights.RefusalDecisionDenied, Ask: rights.RefusalAskNone, Action: ActionProviderManage}
	}
	decision := r.operator.credentials.runtimeRights.decide(ctx, rights.Subject{Account: caller.Account, Program: caller.Program}, ActionProviderManage, "account")
	switch decision.Outcome {
	case rights.DecisionOutcomePermitted:
		return 0, nil
	case rights.DecisionOutcomeDenied:
		return registration.OutcomeForbidden, &rights.RefusalDetail{Source: rights.RefusalSourceRights, Decision: rights.RefusalDecisionDenied, Ask: rights.RefusalAskNone, Action: ActionProviderManage}
	case rights.DecisionOutcomeNotGranted:
		return registration.OutcomeForbidden, &rights.RefusalDetail{Source: rights.RefusalSourceRights, Decision: rights.RefusalDecisionNotGranted, Ask: rights.RefusalAskAvailable, Action: ActionProviderManage}
	}
	return registration.OutcomeUnavailable, nil
}

func (r *runtimeRegistry) registerProgram(ctx context.Context, caller inference.Subject, expected string, create bool, spec registration.RegistrationSpec, peer fileid.Peer) registration.Change {
	if outcome, detail := r.registrationGate(ctx, caller); outcome != 0 {
		return registration.Change{Outcome: outcome, Refusal: detail, Reason: "authority"}
	}
	if reason := registrationSpecValid(spec); reason != "" {
		return registration.Change{Outcome: registration.OutcomeInvalid, Reason: reason}
	}
	if !create && expected == "" {
		return registration.Change{Outcome: registration.OutcomeInvalid, Reason: "revision"}
	}
	if len(spec.Remotes) > 0 || len(spec.Applications) > 0 {
		return registration.Change{Outcome: registration.OutcomeUnsupported, Reason: "unsupported:not_implemented"}
	}
	d := r.programDirectory()
	if d == nil {
		return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "registration_owner"}
	}
	now := registrationTime(d.now())
	candidate := registration.Registration{Spec: spec, Source: registration.SourceOperator, State: registration.StateEnabled, IdentityEvidence: registration.IdentityEvidenceNone, Accepted: now, Verified: now, Verification: registration.VerificationUnverified, VerificationReason: "identity:none", Applications: []registration.ApplicationReading{}, Described: []registration.ServiceState{}}
	var image *applicationV3Source
	var held *heldApplicationManifest
	if len(spec.Providers) > 0 {
		if samePrograms(spec.Program, caller.Program) {
			return registration.Change{Outcome: registration.OutcomeInvalid, Reason: "program:self"}
		}
		if peer == nil {
			return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "identity:caller_view"}
		}
		var err error
		held, err = openRegistryProgramImage(ctx, peer, spec.Program)
		if errors.Is(err, errApplicationManifestUnsupported) {
			return registration.Change{Outcome: registration.OutcomeUnsupported, Reason: "unsupported:not_implemented"}
		}
		if err != nil {
			return registration.Change{Outcome: registration.OutcomeForbidden, Reason: "identity:probe", Refusal: &rights.RefusalDetail{Source: rights.RefusalSourceIdentity, Decision: rights.RefusalDecisionDenied, Ask: rights.RefusalAskAvailable, Action: ActionProviderManage}}
		}
		defer held.Close()
		candidate.Key = identity.NormalizeSubjectProgram(applicationV3HeldProgram(held))
		candidate.Spec.Program = candidate.Key
		key := applicationV3HeldKey(held)
		if key == "" || candidate.Key == "" || !registrationProgramCodeAccepted(held.Code, held.Proof) {
			return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "identity:image"}
		}
		image = &applicationV3Source{Program: candidate.Spec.Program, FileKey: key, Code: held.Code, Proof: held.Proof}
		candidate.IdentityEvidence = registration.IdentityEvidenceProgram
		candidate.Verification = registration.VerificationVerified
		candidate.VerificationReason = ""
		if held.Proof == identity.ProofUnsigned {
			candidate.Verification = registration.VerificationUnverified
			candidate.VerificationReason = "identity:unsigned"
		}
		candidate.Verified = registrationTime(d.now())
		candidate.State = registration.StatePendingApproval
	} else {
		// HTTP has no native program evidence. A name identifies its existing
		// configured model-server registration, and supplies no process identity.
		candidate.Key = spec.Servers[0].Name
	}
	for _, facet := range spec.Providers {
		if reason := validProviderFacet(candidate.Spec.Program, facet); reason != "" {
			return registration.Change{Outcome: registration.OutcomeInvalid, Reason: reason}
		}
	}
	for _, facet := range spec.Servers {
		if reason := validHostDeclaration(serverFacetCompatibility(facet)); reason != "" {
			return registration.Change{Outcome: registration.OutcomeInvalid, Reason: reason}
		}
	}
	if image != nil && managedPyTorchRecipes(candidate.Spec) {
		pin, err := registryHeldImageSHA256(ctx, held)
		if err != nil {
			return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "identity:recipe_image"}
		}
		image.ImageSHA256 = pin
	}
	// Repeat the receiving decision immediately before the conditional commit.
	if outcome, detail := r.registrationGate(ctx, caller); outcome != 0 {
		return registration.Change{Outcome: outcome, Refusal: detail, Reason: "authority"}
	}
	if held != nil && held.Recheck(ctx) != nil {
		return registration.Change{Outcome: registration.OutcomeForbidden, Reason: "identity:changed", Refusal: &rights.RefusalDetail{Source: rights.RefusalSourceIdentity, Decision: rights.RefusalDecisionDenied, Ask: rights.RefusalAskAvailable, Action: ActionProviderManage}}
	}
	// A program registration asks the person to keep it only when an
	// operator program, the Panel or OA's own command line, makes it on the
	// person's action: Setup's model host and the Panel's program review. A
	// program registered by any other program asks nothing and waits for the
	// person's review in the Panel (VISION 2026-10-06, amended 2026-11-10).
	ask := r.operator.credentials.runtimeRights.isOperator(caller.Program)
	out := d.publishProgramRegistrationReview(ctx, expected, create, candidate, image, caller.Program, !ask)
	if out.Outcome == registration.OutcomeApplied && r.providers != nil {
		r.providers.refreshProgramRegistrations(ctx)
	}
	return out
}

func serverFacetCompatibility(f registration.ServerFacet) providerDeclaration {
	host := &legacy.DeclarationHost{Base: f.Base, Kind: f.Kind, Hosted: f.Hosted, Credential: f.Credential, Ceiling: f.Ceiling}
	d := providerDeclaration{Name: f.Name, Transport: transportHTTP, Role: "host", Host: hostOf(host), Activation: "attach", Arguments: []string{}, Contracts: []string{}, Resources: []string{}, Models: []string{}}
	for _, modality := range f.Modalities {
		d.Resources = append(d.Resources, "profile:"+modality)
	}
	return d
}

func (r *runtimeRegistry) changeProgram(ctx context.Context, caller inference.Subject, expected string, facet registration.FacetReference, enabled *bool) registration.Change {
	if outcome, detail := r.registrationGate(ctx, caller); outcome != 0 {
		return registration.Change{Outcome: outcome, Refusal: detail, Reason: "authority"}
	}
	d := r.programDirectory()
	if d == nil {
		return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "registration_owner"}
	}
	out := d.changeProgramFacet(ctx, expected, facet, enabled)
	if out.Outcome == registration.OutcomeApplied && r.providers != nil {
		r.providers.refreshProgramRegistrations(ctx)
	}
	if out.Outcome == registration.OutcomeApplied {
		// A removed or turned-off integration no longer holds the card.
		d.writeKeptHolds(ctx)
	}
	return out
}

func registrationCursor(epoch string, caller inference.Subject, revision string, offset int) string {
	b, _ := json.Marshal([]string{epoch, caller.Account, caller.Program})
	h := sha256.Sum256(b)
	return epoch + "." + hex.EncodeToString(h[:]) + "." + revision + "." + strconv.Itoa(offset)
}

func (r *runtimeRegistry) programPage(ctx context.Context, caller inference.Subject, cursor string, limit int64, observe bool, wait int64) registration.Page {
	refuse := func(outcome registration.Outcome, reason string) registration.Page {
		return registration.Page{Outcome: outcome, Reason: reason, Registrations: []registration.Registration{}, Removals: []registration.RemovalReceipt{}}
	}
	if limit < 1 || limit > 64 || wait < 0 || wait > 30000 || len(cursor) > 256 {
		return refuse(registration.OutcomeInvalid, "bounds")
	}
	var denied registration.Page
	recheck := func() error {
		if outcome, detail := r.registrationGate(ctx, caller); outcome != 0 {
			denied = refuse(outcome, "authority")
			denied.Refusal = detail
			return registryRefusal("authority")
		}
		return nil
	}
	if recheck() != nil {
		return denied
	}
	d := r.programDirectory()
	if d == nil {
		return refuse(registration.OutcomeUnavailable, "registration_owner")
	}
	if !observe {
		wait = 0
	}
	var page registration.Page
	outcome, err := d.gate.Wait(ctx, time.Duration(wait)*time.Millisecond, watch.Probe{
		Read: func() (bool, error) {
			var answer bool
			page, answer = r.programPageNow(ctx, d, caller, cursor, limit, observe)
			return answer, nil
		},
		Recheck: recheck,
	})
	if denied.Outcome != 0 {
		return denied
	}
	if err != nil {
		return refuse(registration.OutcomeUnavailable, "cancelled")
	}
	if outcome == watch.Busy || outcome == watch.Failed {
		return refuse(registration.OutcomeUnavailable, "registration_owner")
	}
	return page
}

// programPageNow retains registry cursor, gap and reply bounds. The directory's
// shared gate is advanced by both registration edits and provider observations.
func (r *runtimeRegistry) programPageNow(ctx context.Context, d *applicationDirectory, caller inference.Subject, cursor string, limit int64, observe bool) (registration.Page, bool) {
	refuse := func(outcome registration.Outcome, reason string) registration.Page {
		return registration.Page{Outcome: outcome, Reason: reason, Registrations: []registration.Registration{}, Removals: []registration.RemovalReceipt{}}
	}
	records, removals, _, revision, _, err := d.programRegistrations(ctx)
	if err != nil {
		if errors.Is(err, errRegistrationEncodedSize) {
			return refuse(registration.OutcomeUnavailable, errRegistrationEncodedSize.Error()), true
		}
		return refuse(registration.OutcomeUnavailable, "registration_owner"), true
	}
	if revision == "" {
		revision = d.epoch
	}
	observed, _ := json.Marshal(struct {
		Records  []registration.Registration
		Removals []registration.RemovalReceipt
	}{records, removals})
	digest := sha256.Sum256(observed)
	observation := hex.EncodeToString(digest[:])
	base := registrationCursor(d.epoch, caller, observation, 0)
	offset := 0
	if cursor != "" {
		parts := strings.Split(cursor, ".")
		baseParts := strings.Split(base, ".")
		if len(parts) != 4 || parts[0] != baseParts[0] || parts[1] != baseParts[1] {
			return refuse(registration.OutcomeGap, "cursor"), true
		}
		if parts[2] != observation {
			if !observe {
				return refuse(registration.OutcomeGap, "cursor"), true
			}
			cursor = ""
		} else {
			offset, err = strconv.Atoi(parts[3])
			if err != nil || offset < 0 || offset > len(records)+len(removals) {
				return refuse(registration.OutcomeGap, "cursor"), true
			}
		}
	}
	page := refuse(registration.OutcomePage, "")
	page.Revision = revision
	end := offset
	for end < len(records)+len(removals) && end-offset < int(limit) {
		trial := page
		if end < len(records) {
			trial.Registrations = append(slicesCloneRecords(page.Registrations), records[end])
		} else {
			trial.Removals = append(append([]registration.RemovalReceipt{}, page.Removals...), removals[end-len(records)])
		}
		trial.Cursor = registrationCursor(d.epoch, caller, observation, end+1)
		if end+1 == len(records)+len(removals) {
			trial.Cursor = ""
			if observe {
				trial.Cursor = base
			}
		}
		if !registryV3ReplyFits(trial, observe) {
			if end == offset {
				return refuse(registration.OutcomeUnavailable, "bound:reply_bytes"), true
			}
			break
		}
		page = trial
		end++
	}
	if end == len(records)+len(removals) {
		page.Cursor = ""
		if observe {
			page.Cursor = base
		}
	} else {
		page.Cursor = registrationCursor(d.epoch, caller, observation, end)
	}
	if !registryV3ReplyFits(page, observe) {
		return refuse(registration.OutcomeUnavailable, "bound:reply_bytes"), true
	}
	return page, cursor == "" || !observe || cursor != base
}

func slicesCloneRecords(v []registration.Registration) []registration.Registration {
	return append([]registration.Registration{}, v...)
}

type registryV3Receiver struct{ *registryReceiver }

// Generated dispatch has already validated the frame and complete Spec. Its
// compact ceiling excludes formatting, so enforce the separate physical input
// bound on the original embedded value before entering the persistence owner.
func (r *registryV3Receiver) rawSpecRefusal() *registration.Change {
	var frame struct {
		Arguments struct {
			Registration json.RawMessage `json:"registration"`
		} `json:"arguments"`
	}
	if r.call != nil && json.Unmarshal(r.call.Frame, &frame) == nil && len(frame.Arguments.Registration) > 0 && len(frame.Arguments.Registration) <= maxRegistrationSpecBytes {
		return nil
	}
	// Match registerProgram's receiving authority precedence even for an
	// oversized generated-valid argument; raw formatting grants no read scope.
	if outcome, detail := r.host.registry.registrationGate(r.ctx, r.caller()); outcome != 0 {
		return &registration.Change{Outcome: outcome, Refusal: detail, Reason: "authority"}
	}
	return &registration.Change{Outcome: registration.OutcomeInvalid, Reason: "encoded_size"}
}

func (r *registryV3Receiver) Registrations(cursor string, limit int64) (registration.Page, error) {
	return r.host.registry.programPage(r.ctx, r.caller(), cursor, limit, false, 0), nil
}
func (r *registryV3Receiver) Register(spec registration.RegistrationSpec) (registration.Change, error) {
	if refused := r.rawSpecRefusal(); refused != nil {
		return *refused, nil
	}
	peer, _ := r.call.Peer()
	return r.host.registry.registerProgram(r.ctx, r.caller(), "", true, spec, peer), nil
}
func (r *registryV3Receiver) Replace(expected string, spec registration.RegistrationSpec) (registration.Change, error) {
	if refused := r.rawSpecRefusal(); refused != nil {
		return *refused, nil
	}
	peer, _ := r.call.Peer()
	return r.host.registry.registerProgram(r.ctx, r.caller(), expected, false, spec, peer), nil
}
func (r *registryV3Receiver) Remove(expected string, facet registration.FacetReference) (registration.Change, error) {
	return r.host.registry.changeProgram(r.ctx, r.caller(), expected, facet, nil), nil
}
func (r *registryV3Receiver) SetEnabled(expected string, facet registration.FacetReference, enabled bool) (registration.Change, error) {
	return r.host.registry.changeProgram(r.ctx, r.caller(), expected, facet, &enabled), nil
}
func (r *registryV3Receiver) Observe(cursor string, wait int64) (registration.Page, error) {
	return r.host.registry.programPage(r.ctx, r.caller(), cursor, 64, true, wait), nil
}

type registryV3BoundReceiver struct {
	registration.RegistryV3
	page registration.Page
}

func (r *registryV3BoundReceiver) Registrations(string, int64) (registration.Page, error) {
	return r.page, nil
}
func (r *registryV3BoundReceiver) Observe(string, int64) (registration.Page, error) {
	return r.page, nil
}

type registryV3BoundTransport struct {
	dispatcher *registration.RegistryV3Dispatcher
	size       int
}

func (t *registryV3BoundTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	b, err := t.dispatcher.ExchangeFrame(frame)
	t.size = len(b)
	return b, err
}
func registryV3ReplyFits(page registration.Page, observe bool) (fits bool) {
	defer func() {
		if recover() != nil {
			fits = false
		}
	}()
	transport := &registryV3BoundTransport{dispatcher: &registration.RegistryV3Dispatcher{Handler: &registryV3BoundReceiver{page: page}}}
	client := registration.NewRegistryV3Client(transport)
	var err error
	if observe {
		_, err = client.Observe("", 0)
	} else {
		_, err = client.Registrations("", 1)
	}
	return err == nil && transport.size <= 1<<20
}
