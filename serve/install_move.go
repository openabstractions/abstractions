package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// A person can move OpenAbstractions between its per-machine folder
// (Program Files) and its per-user folder (LocalAppData\Programs). OA's own
// records name OA's tools by absolute path: declarations Setup and the model
// host wrote, the canonical registrations adopted from them and from the
// installation's declarations, and rights rules keyed by program. After the
// move each of them names a file that is gone. The runtime repairs them at
// start (installMove).
//
// The rule is narrow. A path moves only when it names a file directly in an
// OA tools folder (product.InstallFolders or product.UserInstallFolder) that
// differs from the running installation's tools folder, that file is gone,
// and the running installation's tools folder holds a file of the same name
// with product standing: in the per-machine folder by path, in the per-user
// folder by the running program's own signer (product.Shipped). A development
// build runs outside those folders and repairs nothing.
type installMove struct {
	// tools is the running installation's tools folder.
	tools string
	// folders are every OA tools folder on this machine.
	folders []string
	// shipped reports product standing of a program in tools.
	shipped func(string) bool
}

// installMove is the repair rule of this runtime, false when the runtime
// runs outside an OA tools folder.
func (p *runtimeProviders) installMove() (installMove, bool) {
	if p == nil || p.installation == "" {
		return installMove{}, false
	}
	tools := filepath.Dir(p.installation)
	protected := standingInstallFolders()
	folders := slices.Clone(protected)
	if user := standingUserInstallFolder(); user != "" {
		folders = append(folders, user)
	}
	if !product.Protected(tools, folders) {
		return installMove{}, false
	}
	return installMove{tools: tools, folders: folders, shipped: func(program string) bool {
		return product.Shipped(program, protected, []string{tools}, standingSameSigner)
	}}, true
}

// path is program in the running installation's tools folder when program
// names an OA tool that moved there.
func (m installMove) path(program string) (string, bool) {
	if program == "" || !filepath.IsAbs(program) {
		return "", false
	}
	program = filepath.Clean(program)
	dir := filepath.Dir(program)
	if product.SameFolder(dir, m.tools) || !product.Protected(dir, m.folders) {
		return "", false
	}
	if _, err := os.Lstat(program); !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	moved := filepath.Join(m.tools, filepath.Base(program))
	info, err := os.Stat(moved)
	if err != nil || !info.Mode().IsRegular() || m.shipped == nil || !m.shipped(moved) {
		return "", false
	}
	return moved, true
}

// argument moves an argument that is a moved path, or a name=path option.
func (m installMove) argument(arg string) (string, bool) {
	if moved, ok := m.path(arg); ok {
		return moved, true
	}
	if name, value, ok := strings.Cut(arg, "="); ok {
		if moved, ok := m.path(value); ok {
			return name + "=" + moved, true
		}
	}
	return arg, false
}

func (m installMove) arguments(args []string) ([]string, bool) {
	changed := false
	var out []string
	for i, arg := range args {
		if moved, ok := m.argument(arg); ok {
			if !changed {
				out = slices.Clone(args)
				changed = true
			}
			out[i] = moved
		}
	}
	if !changed {
		return args, false
	}
	return out, true
}

// file moves a declaration's program, arguments and declared_by.
func (m installMove) file(f providerFile) (providerFile, bool) {
	changed := false
	if moved, ok := m.path(f.Declaration.Program); ok {
		f.Declaration.Program, changed = moved, true
	}
	if args, ok := m.arguments(f.Declaration.Arguments); ok {
		f.Declaration.Arguments, changed = args, true
	}
	if moved, ok := m.path(f.DeclaredBy); ok {
		f.DeclaredBy, changed = moved, true
	}
	if f.ProviderFacet != nil {
		if args, ok := m.arguments(f.ProviderFacet.Arguments); ok {
			facet := *f.ProviderFacet
			facet.Arguments = args
			f.ProviderFacet, changed = &facet, true
		}
	}
	return f, changed
}

// spec moves a registration spec's program and its providers' arguments.
func (m installMove) spec(spec registration.RegistrationSpec) registration.RegistrationSpec {
	if moved, ok := m.path(spec.Program); ok {
		spec.Program = moved
	}
	providers := slices.Clone(spec.Providers)
	for i := range providers {
		if args, ok := m.arguments(providers[i].Arguments); ok {
			providers[i].Arguments = args
		}
	}
	spec.Providers = providers
	return spec
}

// repairMovedInstall moves the operator declarations and the canonical
// registrations that name OA tools of another installation folder. It writes
// one log line per repair. Nothing it cannot repair stops the runtime.
func (p *runtimeProviders) repairMovedInstall(ctx context.Context) {
	m, ok := p.installMove()
	if !ok {
		return
	}
	p.repairMovedDeclarations(ctx, m)
	p.repairMovedRegistrations(ctx, m)
}

// repairMovedDeclarations rewrites each version 2 declaration file in the
// operator directory whose program, arguments or declared_by moved.
func (p *runtimeProviders) repairMovedDeclarations(ctx context.Context, m installMove) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(p.dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f providerFile
		if json.Unmarshal(raw, &f) != nil || f.Version != providerFileVersion || f.Declaration.Name+".json" != e.Name() {
			continue
		}
		old := f.Declaration.Program
		moved, changed := m.file(f)
		if !changed || validProviderDeclaration(moved.Declaration) != "" {
			continue
		}
		if err := providerMutator(ctx, nil)(path, raw, moved); err != nil {
			p.report(fmt.Errorf("provider %s: installation moved; the declaration still names %s: %w", f.Declaration.Name, old, err))
			continue
		}
		p.report(fmt.Errorf("provider %s: installation moved; repaired its declaration from %s to %s", f.Declaration.Name, old, moved.Declaration.Program))
	}
}

// repairMovedRegistrations re-keys each canonical program registration whose
// program moved to the moved program, in one registration edit. The retained
// image keeps its old file identity under the new path: adoption then reads
// the image as changed and accepts it only through the shipped-image proof
// (registry_shipped_images), or holds it for the person's review, and the
// person's approval stays with the record. When the moved program already has
// a record, such as Setup's own registration made at the running installation,
// that record keeps its spec, image and approval, and receives only the facets
// it lacks, each with its source and condition. A record with an application
// facet or a pending question stays as it is, as does one whose moved program
// has a record that does not serve; each is logged.
func (p *runtimeProviders) repairMovedRegistrations(ctx context.Context, m installMove) {
	if p.registrationDirectory == nil {
		return
	}
	d := p.registrationDirectory()
	if d == nil {
		return
	}
	var repaired, kept []string
	out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, entries map[string]applicationV3Registration) (registration.Change, error) {
		repaired, kept = repaired[:0], kept[:0]
		for _, key := range s.programKeys() {
			record, _ := s.programRecord(key)
			program, ok := m.path(record.Spec.Program)
			if !ok {
				continue
			}
			next := identity.NormalizeSubjectProgram(program)
			spec, err := s.programSpec(ctx, key, entries)
			if err != nil {
				return registration.Change{}, err
			}
			target, exists := s.programRecord(next)
			switch {
			case exists && !registrationServes(s, next, target):
				kept = append(kept, fmt.Sprintf("registration %s: installation moved; %s is registered and waits for the person, so the record for the removed folder stays", record.Spec.Program, program))
				continue
			case len(spec.Applications) > 0:
				kept = append(kept, fmt.Sprintf("registration %s: installation moved; it registers applications, so it stays until the application registers again", record.Spec.Program))
				continue
			case s.programPending(key, record):
				kept = append(kept, fmt.Sprintf("registration %s: installation moved; a question about it is open, so it stays until the person answers", record.Spec.Program))
				continue
			}
			revision, err := applicationToken()
			if err != nil {
				return registration.Change{}, err
			}
			if len(repaired) == 0 {
				s.programs, s.legacyPrograms = maps.Clone(s.programs), maps.Clone(s.legacyPrograms)
			}
			if exists {
				if err := mergeProgramKey(ctx, s, m, key, next, m.spec(spec), entries, revision); err != nil {
					return registration.Change{}, err
				}
				repaired = append(repaired, fmt.Sprintf("registration %s: installation moved; joined to the registration of %s", record.Spec.Program, program))
				continue
			}
			moveProgramKey(s, m, key, next, program, record, m.spec(spec), revision)
			repaired = append(repaired, fmt.Sprintf("registration %s: installation moved; repaired to %s", record.Spec.Program, program))
		}
		if len(repaired) == 0 {
			return registration.Change{Outcome: registration.OutcomeConflict, Reason: "kept"}, nil
		}
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome == registration.OutcomeUnavailable {
		p.report(fmt.Errorf("registrations: installation moved; the records were not repaired: %s", out.Reason))
		return
	}
	lines := kept
	if out.Outcome == registration.OutcomeApplied {
		lines = append(repaired, kept...)
	}
	for _, line := range lines {
		p.report(errors.New(line))
	}
}

// moveProgramKey moves every per-program record of key to next.
func moveProgramKey(s *applicationV3Registrations, m installMove, key, next, program string, record registration.Registration, spec registration.RegistrationSpec, revision string) {
	compatibility := make([]providerFile, 0, len(s.programCompatibility[key]))
	for _, f := range s.programCompatibility[key] {
		f, _ = m.file(f)
		compatibility = append(compatibility, f)
	}
	delete(s.programCompatibility, key)
	s.programCompatibility[next] = compatibility
	delete(s.programs, key)
	delete(s.legacyPrograms, key)
	record.Key, record.Revision = next, revision
	record.Spec.Program = program
	s.keepProgramShape(next, record, spec)
	if image, ok := s.programSources[key]; ok {
		delete(s.programSources, key)
		image.Program = next
		s.programSources[next] = image
	}
	if ref, ok := s.programFiles[key]; ok {
		delete(s.programFiles, key)
		s.programFiles[next] = ref
	}
	if refs, ok := s.legacyProgramFiles[key]; ok {
		delete(s.legacyProgramFiles, key)
		s.legacyProgramFiles[next] = refs
	}
	if declarer, ok := s.programDeclarers[key]; ok {
		delete(s.programDeclarers, key)
		if moved, ok := m.path(declarer); ok {
			declarer = moved
		}
		s.programDeclarers[next] = declarer
	}
	if approval, ok := s.programApprovals[key]; ok {
		delete(s.programApprovals, key)
		if moved, ok := m.path(approval.ServedBy); ok {
			approval.ServedBy = moved
		}
		s.programApprovals[next] = approval
	}
	moveKey(s.programInputDigests, key, next)
	moveKey(s.programFacetSources, key, next)
	moveKey(s.programFacetDigests, key, next)
	moveKey(s.programFacetConditions, key, next)
}

func moveKey[V any](m map[string]V, key, next string) {
	if m == nil {
		return
	}
	if v, ok := m[key]; ok {
		delete(m, key)
		m[next] = v
	}
}

// installMovedWhy is the why of a rule the runtime carried to a moved program.
const installMovedWhy = "installation moved"

// repairMovedInstall carries each permanent rule of the runtime's account
// whose program is an OA tool that moved (installMove) to the moved program,
// with the same permit or deny. Rights rules identify a program by its file,
// and the removed file identifies nothing, so the moved program holds no rule
// until this copy. A target that already has a rule keeps it, and a rule with
// an expiry is left to lapse. The old rule stays: it names a file that is
// gone and decides nothing. One log line is written per carried rule.
func (r *runtimeRights) repairMovedInstall(m installMove, report func(error)) {
	if r == nil || r.policy == nil {
		return
	}
	snapshot, err := r.policy.OperatorSnapshot()
	if err != nil {
		report(fmt.Errorf("rights: installation moved; the rules were not read: %w", err))
		return
	}
	for _, rule := range snapshot.Rules {
		if rule.Subject.Account != r.owner {
			continue
		}
		program, ok := m.path(rule.Subject.Program)
		if !ok {
			continue
		}
		old := r.policy.ReadRule(rule.Subject, rule.Action, rule.Resource)
		if old.Outcome != rwire.RuleReadOutcomeFound || old.Record == nil || old.Record.Expires != "" {
			continue
		}
		target := rwire.Subject{Account: r.owner, Program: program}
		written, err := r.setMovedRule(target, rule.Action, rule.Resource, rule.Permit)
		if err != nil {
			report(err)
			continue
		}
		if !written {
			continue
		}
		report(fmt.Errorf("rights: installation moved; carried %s on %s from %s to %s", rule.Action, rule.Resource, rule.Subject.Program, program))
	}
}

// setMovedRule writes one rule at target unless a rule on it exists, and
// reports whether it wrote one.
func (r *runtimeRights) setMovedRule(target rwire.Subject, action, resource string, permit bool) (bool, error) {
	for attempt := 0; attempt < installationAttempts; attempt++ {
		existing := r.policy.ReadRule(target, action, resource)
		switch existing.Outcome {
		case rwire.RuleReadOutcomeFound, rwire.RuleReadOutcomeExpired:
			return false, nil
		case rwire.RuleReadOutcomeUnknown:
		default:
			return false, fmt.Errorf("rights: installation moved; %s on %s for %s: read %s", action, resource, target.Program, existing.Outcome)
		}
		edit, err := r.policy.ChangeRule(existing.Revision, target, action, resource,
			rights.RuleEdit{Permit: &permit, By: r.self(), Why: installMovedWhy}, func() error { return nil })
		if err != nil {
			return false, fmt.Errorf("rights: installation moved; %s on %s for %s: %w", action, resource, target.Program, err)
		}
		switch edit.Outcome {
		case rwire.PolicyEditOutcomeApplied:
			return true, nil
		case rwire.PolicyEditOutcomeConflict:
			continue
		}
		return false, fmt.Errorf("rights: installation moved; %s on %s for %s: %s", action, resource, target.Program, edit.Outcome)
	}
	return false, fmt.Errorf("rights: installation moved; %s on %s for %s: policy kept changing", action, resource, target.Program)
}

// registrationServes reports a record that serves now: no question pending
// about it, and an image that is present and unchanged.
func registrationServes(s *applicationV3Registrations, key string, record registration.Registration) bool {
	return !s.programPending(key, record) && record.Verification != registration.VerificationChanged && record.Verification != registration.VerificationMissing
}

// mergeProgramKey joins the moved record of key into the record at next. The
// record at next keeps its facets, image, approval, declarer and generation;
// each facet of key it lacks joins it with that facet's source, digest and
// condition. The record of key is removed.
func mergeProgramKey(ctx context.Context, s *applicationV3Registrations, m installMove, key, next string, moved registration.RegistrationSpec, entries map[string]applicationV3Registration, revision string) error {
	target, _ := s.programRecord(next)
	spec, err := s.programSpec(ctx, next, entries)
	if err != nil {
		return err
	}
	sources := maps.Clone(s.programFacetSources[next])
	if sources == nil {
		sources = map[registration.FacetReference]registration.Source{}
	}
	digests := maps.Clone(s.programFacetDigests[next])
	if digests == nil {
		digests = map[registration.FacetReference]string{}
	}
	conditions := maps.Clone(s.programFacetConditions[next])
	if conditions == nil {
		conditions = map[registration.FacetReference]registrationFacetCondition{}
	}
	for _, facet := range registrationReferences(spec) {
		sources[facet] = registrationFacetSourceOf(s, next, facet)
		conditions[facet] = registrationConditionOf(s, next, facet)
	}
	var joined []registration.FacetReference
	for _, facet := range registrationReferences(moved) {
		if registrationHasFacet(spec, facet) {
			continue
		}
		spec = registrationReplaceFacet(spec, moved, facet)
		sources[facet] = registrationFacetSourceOf(s, key, facet)
		conditions[facet] = registrationConditionOf(s, key, facet)
		if digest, ok := s.programFacetDigests[key][facet]; ok {
			digests[facet] = digest
		}
		joined = append(joined, facet)
	}
	compatibility := slices.Clone(s.programCompatibility[next])
	for _, f := range s.programCompatibility[key] {
		if !slices.ContainsFunc(joined, func(facet registration.FacetReference) bool { return facet.Name == f.Declaration.Name }) {
			continue
		}
		f, _ = m.file(f)
		compatibility = append(compatibility, f)
	}
	delete(s.programCompatibility, key)
	delete(s.programSources, key)
	delete(s.programFiles, key)
	delete(s.legacyProgramFiles, key)
	delete(s.programDeclarers, key)
	delete(s.programApprovals, key)
	delete(s.programInputDigests, key)
	delete(s.programFacetSources, key)
	delete(s.programFacetDigests, key)
	delete(s.programFacetConditions, key)
	delete(s.programs, key)
	delete(s.legacyPrograms, key)
	s.programCompatibility[next] = compatibility
	s.programFacetSources[next] = sources
	s.programFacetDigests[next] = digests
	s.programFacetConditions[next] = conditions
	target.Revision = revision
	s.keepProgramShape(next, target, spec)
	return nil
}
