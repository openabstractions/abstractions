package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	configservice "github.com/openabstractions/abstraction-config/go/service"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// errProviderConfigProof is a missing designation (config CFG-X1, CFG-E4).
var errProviderConfigProof = fmt.Errorf("%w: kept native provider image proof required", configservice.ErrKeyNotDesignated)

// This is a transient read of the existing registry payload, not designation
// state. Epoch binds the declaration, and Source binds its captured image.
type providerConfigRegistration struct {
	Key, Epoch, Revision, Facet, ConfigKey string
	Source                                 applicationV3Source
	Managed                                bool
}

func providerConfigKeys(spec registration.RegistrationSpec, known func(string) bool) ([]string, error) {
	keys := map[string]bool{}
	for _, facet := range spec.Providers {
		seen := map[string]bool{}
		for _, key := range facet.ConfigKeys {
			if known == nil || !known(key) || seen[key] {
				return nil, errors.New("registration: unknown or duplicate configuration key")
			}
			seen[key] = true
			keys[key] = true
		}
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

func (d *applicationDirectory) providerConfigCandidates(ctx context.Context, program, key string) ([]providerConfigRegistration, error) {
	rows, err := d.providerConfigCandidatesKeys(ctx, program, []string{key})
	if err != nil {
		return nil, err
	}
	return rows[key], rowsErr(rows[key])
}

func rowsErr(rows []providerConfigRegistration) error {
	if len(rows) == 0 {
		return errProviderConfigProof
	}
	return nil
}

// providerConfigCandidatesKeys reads the registry once for several keys. A
// key no registration designates has no rows; an unreachable registry is an
// error for every key.
func (d *applicationDirectory) providerConfigCandidatesKeys(ctx context.Context, program string, keys []string) (map[string][]providerConfigRegistration, error) {
	out := map[string][]providerConfigRegistration{}
	if d == nil {
		return out, nil
	}
	// A registry the read cannot reach decides nothing: the read is
	// unavailable, never an undesignated refusal (CFG-E4).
	if !d.lock(ctx) {
		return out, errors.Join(configservice.ErrKeyReadPolicyUnavailable, errors.New("config: registration owner unavailable"))
	}
	defer d.unlock()
	if d.v3 == nil || d.programConfigKeyKnown == nil {
		return out, nil
	}
	wanted := map[string]bool{}
	for _, key := range keys {
		if d.programConfigKeyKnown(key) {
			wanted[key] = true
		}
	}
	if len(wanted) == 0 {
		return out, nil
	}
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		for _, name := range d.v3.programKeys() {
			record, ok := d.v3.programRecord(name)
			source, has := d.v3.programSources[name]
			approval := d.v3.programApprovals[name]
			if !ok || !has || record.State != registration.StateEnabled || record.IdentityEvidence != registration.IdentityEvidenceProgram ||
				approval.Pending || approval.Removing || !samePrograms(source.Program, program) || source.FileKey == "" || !registrationProgramCodeAccepted(source.Code, source.Proof) {
				continue
			}
			if record.Verification == registration.VerificationChanged || record.Verification == registration.VerificationMissing {
				continue
			}
			for _, facet := range record.Spec.Providers {
				condition := registrationConditionOf(d.v3, name, registration.FacetReference{Kind: registration.FacetKindProvider, Name: facet.Name})
				if condition.Disabled || condition.Pending {
					continue
				}
				managed := approval.KeptGeneration != record.Epoch
				if managed && (facet.Activation != registration.ActivationOnDemand || d.programConfigManaged == nil || !registrationProtectedSource(registrationFacetSourceOf(d.v3, name, registration.FacetReference{Kind: registration.FacetKindProvider, Name: facet.Name}))) {
					continue
				}
				for _, key := range facet.ConfigKeys {
					if wanted[key] {
						out[key] = append(out[key], providerConfigRegistration{Key: name, Epoch: record.Epoch, Revision: record.Revision, Facet: facet.Name, ConfigKey: key, Source: source, Managed: managed})
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return map[string][]providerConfigRegistration{}, errors.Join(configservice.ErrKeyReadPolicyUnavailable, err)
	}
	return out, nil
}

func (d *applicationDirectory) providerConfigCandidateCurrent(ctx context.Context, row providerConfigRegistration) bool {
	rows, err := d.providerConfigCandidates(ctx, row.Source.Program, row.ConfigKey)
	return err == nil && slices.Contains(rows, row)
}

func (d *applicationDirectory) reconcileProviderConfigAttempt(ctx context.Context) {
	// The attempt may have lost its caller after writing a factory row. Bound
	// cleanup independently so cancellation cannot retain a stale system grant.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := d.reconcileProviderConfigGrants(cleanup); err != nil && d.programConfigReport != nil {
		d.programConfigReport(err)
	}
}

func (d *applicationDirectory) providerConfigLaunchImage(ctx context.Context, f providerFile) (activationImageHold, applicationV3Source, error) {
	var none applicationV3Source
	if f.ProviderFacet == nil || len(f.ProviderFacet.ConfigKeys) == 0 {
		return nil, none, nil
	}
	keys, err := providerConfigKeys(registration.RegistrationSpec{Providers: []registration.ProviderFacet{*f.ProviderFacet}}, d.programConfigKeyKnown)
	if err != nil || len(keys) == 0 || d.programConfigKeep == nil {
		return nil, none, errProviderConfigProof
	}
	rows, err := d.providerConfigCandidates(ctx, f.Declaration.Program, keys[0])
	index := slices.IndexFunc(rows, func(row providerConfigRegistration) bool {
		return row.Key == f.RegistrationKey && row.Epoch == f.Generation && row.Facet == f.ProviderFacet.Name
	})
	if err != nil || index < 0 {
		return nil, none, errProviderConfigProof
	}
	before := rows[index]
	open := d.programKeepImage
	if open == nil {
		open = recaptureProgramKeepImage
	}
	held, err := open(ctx, before.Source)
	if err != nil || held == nil {
		return nil, none, errProviderConfigProof
	}
	refuse := func() (activationImageHold, applicationV3Source, error) {
		return nil, none, errors.Join(errProviderConfigProof, held.Close())
	}
	defer d.reconcileProviderConfigAttempt(ctx)
	if held.Recheck(ctx) != nil || d.programConfigKeep(ctx, before.Source.Program, keys) != nil {
		return refuse()
	}
	if !d.providerConfigCandidateCurrent(ctx, before) || held.Recheck(ctx) != nil {
		return refuse()
	}
	return held, before.Source, nil
}

// managedProviderConfigProof reads the existing live supervisor and its held
// image. Job membership adds process custody; the image and registry checks
// remain required. Retiring, attach-only and unproved processes grant nothing.
func (p *runtimeProviders) managedProviderConfigProof(ctx context.Context, peer *identity.Peer, record providerConfigRegistration) error {
	if p == nil || peer == nil {
		return errProviderConfigProof
	}
	process, err := peer.Process.AtLeast(identity.ProofKernel)
	if err != nil || process.PID <= 0 || process.StartTime.IsZero() {
		return errProviderConfigProof
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errProviderConfigProof
	}
	s := p.supervisors[record.Facet]
	p.mu.Unlock()
	if s == nil {
		return errProviderConfigProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.RegistrationKey != record.Key || s.file.Generation != record.Epoch || s.configSource != record.Source || s.configImage == nil ||
		s.file.ProviderFacet == nil || !slices.Contains(s.file.ProviderFacet.ConfigKeys, record.ConfigKey) || s.configImage.Recheck(ctx) != nil {
		return errProviderConfigProof
	}
	child, ok := s.child.(interface {
		StartTime() time.Time
		Contains(int, time.Time) (bool, error)
	})
	if !ok || child.StartTime().IsZero() {
		return errProviderConfigProof
	}
	belongs, err := child.Contains(process.PID, process.StartTime)
	if err != nil || !belongs {
		return errProviderConfigProof
	}
	return nil
}

func (d *applicationDirectory) providerConfigPeerProgram(peer *identity.Peer) (string, error) {
	if d == nil {
		return "", errProviderConfigProof
	}
	subject, err := identity.SubjectOf(peer, listen.Program)
	if err != nil || subject.Account != d.owner {
		return "", errProviderConfigProof
	}
	return subject.Program, nil
}

// providerConfigReadProof is composed before the config owner's exact key.read
// Rights decision. Its peer is the listener's freshly rechecked native peer;
// neither a request label nor a supervisor PID can supply this evidence.
func (d *applicationDirectory) providerConfigReadProof(ctx context.Context, peer *identity.Peer, key string) error {
	program, err := d.providerConfigPeerProgram(peer)
	if err != nil {
		return err
	}
	rows, err := d.providerConfigCandidates(ctx, program, key)
	if err != nil {
		return err
	}
	for _, before := range rows {
		if d.providerConfigReadCandidate(ctx, peer, before) == nil {
			return nil
		}
	}
	return errProviderConfigProof
}

func (d *applicationDirectory) providerConfigReadCandidate(ctx context.Context, peer *identity.Peer, before providerConfigRegistration) error {
	held, err := openRegistryProgramImage(ctx, peer, before.Source.Program)
	if err != nil {
		return errProviderConfigProof
	}
	defer held.Close()
	if applicationV3HeldKey(held) != before.Source.FileKey || held.Code != before.Source.Code || held.Proof != before.Source.Proof || held.Recheck(ctx) != nil {
		return errProviderConfigProof
	}
	if before.Managed && d.programConfigManaged(ctx, peer, before) != nil {
		return errProviderConfigProof
	}
	if !d.providerConfigCandidateCurrent(ctx, before) || held.Recheck(ctx) != nil {
		return errProviderConfigProof
	}
	return nil
}

// providerConfigReadProofKeys is providerConfigReadProof for one pass over
// several keys: one registry read finds the designations, the peer's image is
// opened and checked once for each registration and held while one more
// registry read confirms them all. Each key keeps its own answer.
func (d *applicationDirectory) providerConfigReadProofKeys(ctx context.Context, peer *identity.Peer, keys []string) map[string]error {
	out := make(map[string]error, len(keys))
	answerAll := func(err error) map[string]error {
		for _, key := range keys {
			out[key] = err
		}
		return out
	}
	program, err := d.providerConfigPeerProgram(peer)
	if err != nil {
		return answerAll(err)
	}
	rows, err := d.providerConfigCandidatesKeys(ctx, program, keys)
	if err != nil {
		return answerAll(err)
	}
	// A registration's image is opened once, whichever keys it designates.
	type image struct {
		held *heldApplicationManifest
		ok   bool
	}
	images := map[applicationV3Source]*image{}
	defer func() {
		for _, i := range images {
			if i.held != nil {
				i.held.Close()
			}
		}
	}()
	proven := map[providerConfigRegistration]bool{}
	for _, key := range keys {
		for _, row := range rows[key] {
			i := images[row.Source]
			if i == nil {
				i = &image{}
				images[row.Source] = i
				held, err := openRegistryProgramImage(ctx, peer, row.Source.Program)
				if err == nil {
					i.held = held
					i.ok = applicationV3HeldKey(held) == row.Source.FileKey && held.Code == row.Source.Code && held.Proof == row.Source.Proof && held.Recheck(ctx) == nil
				}
			}
			if i.ok && (!row.Managed || d.programConfigManaged(ctx, peer, row) == nil) {
				proven[row] = true
			}
		}
	}
	if len(proven) == 0 {
		return answerAll(errProviderConfigProof)
	}
	current, err := d.providerConfigCandidatesKeys(ctx, program, keys)
	if err != nil {
		return answerAll(err)
	}
	for source, i := range images {
		if i.ok && i.held.Recheck(ctx) != nil {
			for row := range proven {
				if row.Source == source {
					delete(proven, row)
				}
			}
		}
	}
	for _, key := range keys {
		out[key] = errProviderConfigProof
		for _, row := range rows[key] {
			if proven[row] && slices.Contains(current[key], row) {
				out[key] = nil
				break
			}
		}
	}
	return out
}
