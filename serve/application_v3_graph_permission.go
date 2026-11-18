package main

import (
	"context"
	"errors"
	"slices"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func workflowApplicationPackage() product.PackageSpec {
	spec, _ := product.PackageDeclaration(product.ExtensionSetupFacts().PackageID)
	return spec
}

func isWorkflowApplication(application string) bool {
	return application != "" && application == workflowApplicationPackage().Registration
}

func keptGraphEdit(application, tool string, class appmodel.CommandClass) bool {
	return tool == grants.GraphEditCommand && class == appmodel.CommandClassEdit && isWorkflowApplication(application)
}

type graphEditEnrollment struct {
	agent         applicationAgentAdmission
	host          catalogueHostRecord
	image, helper *heldApplicationManifest
}

// graphEditHost reads the exact host relation and target lineage together.
// It reads no Asks state and can also recheck underneath a pinned answer.
func (d *applicationDirectory) graphEditHost(ctx context.Context, agent applicationAgentAdmission, target grants.KeptApplicationClassTarget, revision string) (catalogueHostRecord, error) {
	if !d.lock(ctx) {
		return catalogueHostRecord{}, ctx.Err()
	}
	defer d.unlock()
	var host catalogueHostRecord
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		name := agent.Enrollment.Application
		mark := marks[name]
		entry, binding, ok := d.agentEntryLocked(legacy, name)
		if !ok || name != catalogueAgentApplication || target.Enrollment != agent.Enrollment.Revision ||
			agentEnrollmentView(name, entry.Program, binding, d.owner, mark) != agent.Enrollment || mark.ProgramKey != agent.Image.Key || agent.Image.Binding != binding {
			return errors.New("application graph edit: enrollment changed")
		}
		host, err = readCatalogueCompanion(ctx, d.dir, name, mark)
		if err != nil {
			return err
		}
		official, _, ok := d.agentEntryLocked(legacy, catalogueHostParty)
		if host.Reference == "" || !ok || !identity.SameSubjectProgram(official.Program, host.Image.Program) {
			return errors.New("application graph edit: official host registration unavailable")
		}
		if d.v3 == nil {
			return errors.New("application graph edit: target unavailable")
		}
		kept := d.v3.entries[target.Application]
		if !isWorkflowApplication(target.Application) || kept.State != "enabled" || kept.KeepIdentity == "" || kept.KeepIdentity != target.Keep || kept.Revision != revision {
			return errors.New("application graph edit: target Keep changed")
		}
		return nil
	})
	return host, err
}

func (d *applicationDirectory) holdGraphEditEnrollment(ctx context.Context, agent applicationAgentAdmission, target grants.KeptApplicationClassTarget, revision string) (graphEditEnrollment, error) {
	host, err := d.graphEditHost(ctx, agent, target, revision)
	if err != nil {
		return graphEditEnrollment{}, err
	}
	open := d.catalogueHostHold
	if open == nil {
		open = func(ctx context.Context, host catalogueHostRecord) (*heldApplicationManifest, error) {
			local, err := layout.AccountData()
			if err != nil {
				return nil, err
			}
			return holdKeptCatalogueHost(ctx, fileid.Self, catalogueHostParty, local, host.Reference, host, openRegistryProgramImage)
		}
	}
	image, err := open(ctx, host)
	if err != nil {
		return graphEditEnrollment{}, err
	}
	helper, err := openHeldApplicationImage(ctx, fileid.Self, agent.Image.Program)
	if err != nil {
		return graphEditEnrollment{}, errors.Join(err, image.Close())
	}
	if applicationV3HeldKey(helper) != agent.Image.Key || !identity.SameSubjectProgram(applicationV3HeldProgram(helper), agent.Image.Program) {
		return graphEditEnrollment{}, errors.Join(errors.New("application graph edit: connector image changed"), helper.Close(), image.Close())
	}
	return graphEditEnrollment{agent: agent, host: host, image: image, helper: helper}, nil
}

func (s graphEditEnrollment) close() {
	_ = errors.Join(s.image.Close(), s.helper.Close())
}

func (s graphEditEnrollment) recheck(ctx context.Context, d *applicationDirectory, target grants.KeptApplicationClassTarget, revision string) error {
	snapshot := func(ctx context.Context) (keptCatalogueEffectIdentity, error) {
		host, err := d.graphEditHost(ctx, s.agent, target, revision)
		if err != nil || host != s.host {
			return keptCatalogueEffectIdentity{}, errors.Join(err, errors.New("application graph edit: official host changed"))
		}
		return keptCatalogueEffectIdentity{host: host.Reference, enrollment: s.agent.Enrollment.Revision, target: target.Keep}, nil
	}
	return withKeptCatalogueEffect(ctx, s.image, keptCatalogueEffectIdentity{host: s.host.Reference, enrollment: target.Enrollment, target: target.Keep}, snapshot, func() error { return s.helper.Recheck(ctx) })
}

// prepareGraphEditAllow is the save side of the same scope. An operator can
// answer only a retained typed occurrence, whose parties are re-proved before
// Rights sees the Allow. Undo can remove the old exact rule even after rebind.
// The requester's image is verified in full, as a message permission's Allow
// verifies its parties.
func (d *applicationDirectory) prepareGraphEditAllow(ctx context.Context, effect rights.PermissionEffect, target grants.KeptApplicationClassTarget) (func(context.Context) error, func(), error) {
	var image applicationAgentImage
	current := d.currentAgentEnrollmentVerified(ctx, catalogueAgentApplication, &image)
	if current.Outcome != applications.OutcomePage || current.Enrollment == nil || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled || current.Enrollment.Revision != target.Enrollment {
		return nil, nil, errors.New("application graph edit: requester enrollment changed")
	}
	view, found, err := d.applicationV3Declaration(ctx, target.Application)
	if err != nil || !found || view.KeepIdentity != target.Keep {
		return nil, nil, errors.Join(err, errors.New("application graph edit: target Keep changed"))
	}
	scope, err := d.holdGraphEditEnrollment(ctx, applicationAgentAdmission{Enrollment: *current.Enrollment, Image: image}, target, view.Revision)
	if err != nil {
		return nil, nil, err
	}
	if effect.Subject.Account != d.owner || !identity.SameSubjectProgram(effect.Subject.Program, scope.host.Image.Program) {
		scope.close()
		return nil, nil, errors.New("application graph edit: permission host changed")
	}
	app, err := d.revalidateApplicationV3(ctx, fileid.Self, view.Name, view.Revision)
	if err != nil {
		scope.close()
		return nil, nil, err
	}
	return func(ctx context.Context) error {
		return errors.Join(scope.recheck(ctx, d, target, view.Revision), app.Recheck(ctx))
	}, func() { _ = app.Close(); scope.close() }, nil
}

func (g *applicationV3Gateway) graphEditAuthorization(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, v applicationV3Invocation, call appmodel.GatewayCall, key string, body []byte, replay bool) applicationV3ConsentResult {
	locked := func(ctx context.Context) error {
		if !c.valid(g.directory.owner) {
			return errors.New("application graph edit: caller changed")
		}
		if err := k.recheck(ctx); err != nil {
			return err
		}
		action, _ := grants.ApplicationCommandAction(v.class.String(), "app:"+k.manifest.Name)
		decision := g.policy.decide(ctx, c.subject, action, "app:"+k.manifest.Name)
		if !rightsclient.PermittedOrNotGranted(decision.Outcome) {
			return errors.New("application graph edit: class blocked")
		}
		return ctx.Err()
	}
	r := applicationV3ConsentRequest{caller: c, application: k.manifest.Name, title: k.manifest.Title, program: k.program, revision: k.revision, instance: call.Instance, tool: call.Tool, key: key, bodyDigest: applicationV3BodyDigest(body), method: "Call", class: v.class, context: v.context, replay: replay, recheck: locked}
	if k.keepIdentity == "" {
		return g.asks.applicationV3OneCall(ctx, r)
	}
	agent, ok, err := g.directory.captureApplicationAgent(ctx, c.peer, catalogueAgentApplication)
	if err != nil || !ok {
		return g.asks.applicationV3OneCall(ctx, r)
	}
	target := grants.KeptApplicationClassTarget{Application: k.manifest.Name, Keep: k.keepIdentity, Command: call.Tool, Class: v.class.String(), Enrollment: agent.Enrollment.Revision}
	scope, err := g.directory.holdGraphEditEnrollment(ctx, agent, target, k.revision)
	if err != nil {
		return g.asks.applicationV3OneCall(ctx, r)
	}
	r.recheck = func(ctx context.Context) error {
		if err := locked(ctx); err != nil {
			return err
		}
		return scope.recheck(ctx, g.directory, target, k.revision)
	}
	code := scope.host.Image.Code
	publisher := asks.Publisher{Subject: code.Subject, Trusted: code.Trusted, Description: code.Description, OriginalName: code.OriginalName}
	subject := rights.Subject{Account: c.subject.Account, Program: scope.host.Image.Program}
	result := g.asks.graphEditConsent(ctx, r, subject, publisher, target)
	if quiet := result.recheck; quiet != nil {
		result.recheck = func(ctx context.Context) error {
			current, ok, err := g.directory.captureApplicationAgent(ctx, c.peer, catalogueAgentApplication)
			if err != nil || !ok || current.Enrollment != agent.Enrollment || current.Image.Key != agent.Image.Key {
				return errors.Join(err, errors.New("application graph edit: requester enrollment changed"))
			}
			return quiet(ctx)
		}
	}
	result.close = scope.close
	return result
}

func (a *runtimeAsks) graphEditConsent(ctx context.Context, r applicationV3ConsentRequest, subject rights.Subject, publisher asks.Publisher, target grants.KeptApplicationClassTarget) applicationV3ConsentResult {
	refuse := func(out, why string) applicationV3ConsentResult {
		return applicationV3ConsentResult{outcome: out, reason: why}
	}
	if a == nil || a.book == nil || a.rights == nil || a.rights.policy == nil || a.rights.policy.Definitions == nil {
		return refuse("unsupported", "presenter")
	}
	p, err := grants.KeptGraphEditPermission(subject, target, workflowApplicationPackage().Title)
	if err != nil || !a.rights.policy.Definitions.ValidPermissionEffect(p.Effect) || r.recheck(ctx) != nil {
		return refuse("unavailable", "rights")
	}
	decision := a.permissionDecision(ctx, p.Effect, "")
	requestKey, _ := applicationV3OnceRequestKey(r)
	quiet := func(ctx context.Context) error {
		if err := r.recheck(ctx); err != nil {
			return err
		}
		if permissionDisposition(a.permissionDecision(ctx, p.Effect, ""), 1) != askswire.PermissionOutcomePermitted {
			return errors.New("application graph edit: permission changed")
		}
		return nil
	}
	once := func(id string) applicationV3ConsentResult {
		check := func(ctx context.Context) error {
			if err := r.recheck(ctx); err != nil {
				return err
			}
			found, err := a.book.WithApplicationRecordContext(ctx, id, func(record asks.Record, scope string, _ []string) error {
				if scope != a.scope || record.Key != asks.TypedPermissionKey || record.TypedPermission == nil || record.TypedPermission.Origin != "service" ||
					record.TypedPermission.Digest != p.Digest || record.About != subject.Program || !slices.Contains(record.Options, "once") ||
					record.Pending() || record.Option != "once" || !record.Yes || record.AnsweredBy != asks.AnsweredByOperator {
					return errors.New("application graph edit: exact once answer required")
				}
				state := permissionDisposition(a.permissionDecision(ctx, p.Effect, ""), 1)
				if state != askswire.PermissionOutcomePending && state != askswire.PermissionOutcomePermitted {
					return errors.New("application graph edit: permission changed")
				}
				return r.recheck(ctx)
			})
			if !found || err != nil {
				return errors.Join(err, errors.New("application graph edit: once answer unavailable"))
			}
			return nil
		}
		if id == "" || check(ctx) != nil {
			return refuse("refused", "consent")
		}
		return applicationV3ConsentResult{recheck: check}
	}
	switch permissionDisposition(decision, 1) {
	case askswire.PermissionOutcomePermitted:
		return applicationV3ConsentResult{recheck: quiet}
	case askswire.PermissionOutcomeDenied:
		return refuse("not_permitted", "rights")
	case askswire.PermissionOutcomePending:
		if r.replay {
			retained, status, err := a.book.ReplayTypedPermission(a.scope, requestKey, p)
			if err != nil || status != "answered" || retained.Option != "once" {
				return refuse("not_permitted", "rights")
			}
			return once(retained.ID)
		}
	default:
		return refuse("unavailable", "rights")
	}
	record, outcome, err := a.book.AskTypedPermissionWithOnceAuthorizedFrom(a.scope, subject.Program, requestKey, p,
		listen.Seen{Path: subject.Program, Bound: true, Why: "verified enrolled connector and its official host"}, publisher, func() error {
			if err := r.recheck(ctx); err != nil {
				return err
			}
			if permissionDisposition(a.permissionDecision(ctx, p.Effect, decision.Revision), 1) != askswire.PermissionOutcomePending {
				return errors.New("application graph edit: decision changed")
			}
			return nil
		})
	if err != nil || (outcome != "pending" && outcome != "answered") {
		return refuse("unavailable", "consent")
	}
	defer func() {
		if _, _, err := a.book.RetirePendingApplicationAuthorized(record.ID, func() error { return nil }); err != nil {
			a.reportPermission(err)
		}
	}()
	a.awaitQuestionsChecking(ctx, subject, []string{record.ID}, func(retained asks.TypedPermission) (bool, bool) {
		if retained.Digest != p.Digest || retained.Origin != "service" || retained.Purpose != "" || r.recheck(ctx) != nil {
			return false, true
		}
		state := permissionDisposition(a.permissionDecision(ctx, p.Effect, ""), 1)
		return state == askswire.PermissionOutcomePermitted, state == askswire.PermissionOutcomeDenied
	})
	if quiet(ctx) != nil {
		return once(record.ID)
	}
	return applicationV3ConsentResult{recheck: quiet}
}
