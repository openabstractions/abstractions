package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-router/go"
	"slices"
	"strings"
	"time"

	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

// Rename allowance, P4, 2026-11-04: schema maxima are64 application
// manifests, each16 model needs +16 choosers +1 host spelling, plus2 servers.
const maxRegistrationSpecBytes = 262144 + 64*(16+16+1) + 2

// providerFacetCompatibility is the boundary to the published declaration and
// router@1 representation. Canonical specs retain literal typed model names.
func providerFacetCompatibility(program string, facet registration.ProviderFacet) providerFile {
	d := providerDeclaration{Name: facet.Name, Program: program, Arguments: slices.Clone(facet.Arguments), Endpoint: facet.Endpoint, Transport: transportNative, Contracts: slices.Clone(facet.Services), Guarantees: slices.Clone(facet.Guarantees), Resources: slices.Clone(facet.Resources), Models: []string{}, Activation: facet.Activation.String()}
	for _, model := range facet.Models {
		resource := "profile:" + model.Modality
		if !slices.Contains(d.Resources, resource) {
			d.Resources = append(d.Resources, resource)
		}
		d.Models = append(d.Models, model.Modality+":"+model.Model)
	}
	for _, extension := range facet.RequiredExtensions {
		d.SupportedExtensions = append(d.SupportedExtensions, providerExtension{Profile: extension.Modality, Key: extension.Key})
	}
	// Clone through the generated codec: the routing holder cannot retain an
	// alias into the caller's or canonical store's slices.
	copy := facet
	if raw, err := registrationEncoded(registration.EncodeProviderFacet, &facet); err == nil {
		if v, err := registration.DecodeProviderFacet(raw); err == nil {
			copy = *v
		}
	}
	return providerFile{Version: providerFileVersion, Declaration: d, ProviderFacet: &copy}
}

func registrationProviderFiles(record registration.Registration) []providerFile {
	out := []providerFile{}
	for _, facet := range record.Spec.Providers {
		out = append(out, providerFacetCompatibility(record.Spec.Program, facet))
	}
	for _, facet := range record.Spec.Servers {
		out = append(out, providerFile{Version: providerFileVersion, Declaration: serverFacetCompatibility(facet)})
	}
	for i := range out {
		out[i].Generation = record.Epoch
		out[i].RegistrationKey = record.Key
		out[i].Source = record.Source.String()
		if record.Source == registration.SourceSelfRegistration {
			out[i].Source = declarationSourceOperator
		}
		if record.Source == registration.SourceProductRecord {
			out[i].Source = declarationSourceProduct
		}
		if stamp, err := time.Parse(time.RFC3339Nano, record.Accepted); err == nil {
			out[i].DeclaredAt = stamp.UnixMilli()
		}
		out[i].Disabled = record.State != registration.StateEnabled || record.Verification == registration.VerificationMissing || record.Verification == registration.VerificationChanged
		out[i].disabledWhy = registrationDisabledWhy(record, record.State == registration.StatePendingApproval)
	}
	return out
}

// registrationDisabledWhy names why a registration reads disabled when the
// runtime, not the person, disabled it (facade FAC-R7); "" otherwise.
func registrationDisabledWhy(record registration.Registration, pending bool) string {
	switch {
	case record.Verification == registration.VerificationMissing:
		return "program:missing"
	case record.Verification == registration.VerificationChanged:
		return programChangedReason
	case pending || record.State == registration.StatePendingApproval:
		return "approval:pending"
	}
	return ""
}

// disabledReason is the why a disabled declaration lists. Installation and
// product names, and a name the person turned off, read operator.
func (f providerFile) disabledReason() string {
	if f.Source == declarationSourceOperator && f.disabledWhy != "" {
		return f.disabledWhy
	}
	return "operator"
}

func registrationSourceWord(source registration.Source) string {
	switch source {
	case registration.SourceInstallation:
		return declarationSourceInstallation
	case registration.SourceProductRecord:
		return declarationSourceProduct
	default:
		return declarationSourceOperator
	}
}

func (p *runtimeProviders) registrationBindingCurrentLocked(name, binding string) bool {
	bindings, scoped := p.registrationBindingsLocked()
	return !scoped || bindings[name] == binding
}

func (p *runtimeProviders) registrationBindingsLocked() (map[string]string, bool) {
	if p.registrationDirectory == nil || p.registrationDirectory() == nil {
		return nil, false
	}
	bindings := map[string]string{}
	files, _, err := p.readCached()
	if err != nil {
		return bindings, true
	}
	for _, file := range servingView(files) {
		if !file.Disabled {
			bindings[file.Declaration.Name] = file.bindingID()
		}
	}
	return bindings, true
}

func (p *runtimeProviders) registrationAdmit(ctx context.Context, host *router.Host, model string) (string, bool) {
	if p == nil || host == nil {
		return "", true
	}
	current := func() bool {
		if p.registrationDirectory == nil || p.registrationDirectory() == nil || host.BindingID == "" || strings.HasPrefix(host.BindingID, "paired:") {
			return true
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.registrationBindingCurrentLocked(host.Name, host.BindingID)
	}
	if !current() {
		return p.registrationReviewRefusal(host.Name), false
	}
	why, ok := p.nativeAdmit(ctx, host, model)
	if ok && !current() {
		return p.registrationReviewRefusal(host.Name), false
	}
	return why, ok
}

// approvalPendingReason is why a model waits: a provider declaration that
// names it waits for the person's Keep in the OpenAbstractions question.
const approvalPendingReason = "approval:pending"

// waitingReason answers the inference provider's Waiting: approval:pending
// for a model for profile that only a declaration waiting for the person's
// Keep names, the added part of a served one included; "" otherwise.
func (p *runtimeProviders) waitingReason(_ context.Context, model, profile string) string {
	if p == nil || model == "" {
		return ""
	}
	files, _, err := p.readCached()
	if err != nil {
		return ""
	}
	for _, f := range files {
		var waiting []string
		why := approvalPendingReason
		switch {
		case f.serving != nil:
			waiting = f.serving.waiting
		case f.Disabled && f.disabledWhy == approvalPendingReason:
			waiting = f.Declaration.Models
		case f.Disabled && f.disabledWhy == programChangedReason:
			waiting, why = f.Declaration.Models, programChangedReason
		}
		for _, declared := range waiting {
			modality, name, _ := strings.Cut(declared, ":")
			if strings.EqualFold(name, model) && (profile == "" || modality == profile) {
				// This need raises the question the person answers again when
				// the one asked expired: the Keep of a waiting change, or the
				// program review of a changed program image.
				if why == programChangedReason {
					if p.programReviewNeed != nil {
						p.programReviewNeed(f.RegistrationKey)
					}
				} else if p.registrationDirectory != nil {
					p.registrationDirectory().reaskKeep(f.RegistrationKey)
				}
				return why
			}
		}
	}
	return ""
}

// programChangedReason is why a model waits whose provider program image
// changed and waits for the person's review in the OpenAbstractions question.
const programChangedReason = "program:changed"

func (p *runtimeProviders) registrationReviewRefusal(name string) string {
	if p.programReviewProviderQuestion != nil {
		if question := p.programReviewProviderQuestion(name); question != "" {
			return "registration:changed; review open OpenAbstractions question " + question
		}
	}
	return "registration:changed"
}

func (p *runtimeProviders) refreshProgramRegistrations(ctx context.Context) {
	// Publication removes eligibility immediately through the directory read.
	// FAC-R8 ends the supervised child at retirement. Accepted operation
	// records remain reachable through the runtime's mediation owner.
	type retirement struct {
		name      string
		old, next *providerSupervisor
	}
	pending := []retirement{}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	files, _, err := p.readCached()
	if err != nil {
		p.mu.Unlock()
		p.report(err)
		return
	}
	selected := map[string]providerFile{}
	for _, f := range servingView(files) {
		if !f.Disabled && f.Declaration.role() != legacy.DeclarationRoleHost {
			selected[f.Declaration.Name] = f
		}
	}
	for name, old := range p.supervisors {
		next, ok := selected[name]
		if ok && old.file.bindingID() == next.bindingID() {
			delete(selected, name)
			continue
		}
		retired := p.beginRetirementLocked(name)
		var replacement *providerSupervisor
		if ok {
			replacement = newProviderSupervisor(p, next)
			p.supervisors[name] = replacement
		}
		delete(selected, name)
		pending = append(pending, retirement{name: name, old: retired, next: replacement})
	}
	for name, f := range selected {
		if p.retiring[name] != nil {
			continue
		}
		next := newProviderSupervisor(p, f)
		p.supervisors[name] = next
		pending = append(pending, retirement{name: name, next: next})
	}
	p.mu.Unlock()
	p.notify()
	for _, retired := range pending {
		p.finishRetirement(retired.name, retired.old, retired.next)
	}
	// A kept declaration's program may have gone or come back since its
	// supervisor read it; each reconcile reads it again.
	p.recheckPrograms()
}

// decodeProviderRegistration receives the generated spec channel used by
// installed declarations and producers. Observed authority fields never enter
// through this channel; the registration receiver owns their publication.
func decodeProviderRegistration(raw []byte) (registration.RegistrationSpec, error) {
	if len(raw) > maxRegistrationSpecBytes {
		return registration.RegistrationSpec{}, errors.New("registration: spec bytes")
	}
	spec, err := registration.DecodeRegistrationSpec(raw)
	if err != nil {
		return registration.RegistrationSpec{}, err
	}
	if reason := registrationSpecValid(*spec); reason != "" {
		return registration.RegistrationSpec{}, errors.New("registration: " + reason)
	}
	return *spec, nil
}

func validProviderFacet(program string, facet registration.ProviderFacet) string {
	projection := providerFacetCompatibility(program, facet).Declaration
	// The legacy packed spelling can exceed the old input's model-string cap.
	// The new generated codec owns each typed member's independent bound.
	projection.Models = nil
	projection.Guarantees = nil
	projection.Resources = slices.Clone(facet.Resources)
	projection.SupportedExtensions = nil
	if reason := validProviderDeclaration(projection); reason != "" {
		return reason
	}
	models := map[registration.ScopedModel]bool{}
	for _, model := range facet.Models {
		if models[model] || profileContracts[model.Modality] == "" || !slices.Contains(facet.Services, profileContracts[model.Modality]) {
			return "models"
		}
		models[model] = true
	}
	guarantees := map[string]bool{}
	for _, guarantee := range facet.Guarantees {
		if guarantees[guarantee] {
			return "guarantees"
		}
		guarantees[guarantee] = true
	}
	extensions := map[registration.RequiredExtension]bool{}
	for _, extension := range facet.RequiredExtensions {
		if extensions[extension] || profileContracts[extension.Modality] == "" || !slices.Contains(facet.Services, profileContracts[extension.Modality]) {
			return "required_extensions"
		}
		extensions[extension] = true
	}
	for _, resource := range facet.Resources {
		if strings.HasPrefix(resource, "profile:") {
			return "resources"
		}
	}
	return ""
}
