package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// Installation provenance and the marker that records it
// (research/rights-defaults/DECISION.md §2).
const (
	installationWhy       = "installation"
	defaultsMarkerFile    = "defaults-applied"
	defaultsMarkerProfile = "rights-defaults@1"
	maxDefaultsMarker     = 1 << 20
	installationAttempts  = 8
)

// runtimeRights is the runtime's rights decision policy in its state directory:
// the in-process decision point its resource services ask, the operator
// programs that administer it, and the installation rules those programs hold.
type runtimeRights struct {
	policy    *rights.DecisionPolicy
	path      string
	marker    string
	owner     string
	kind      string
	endpoint  string
	operators []string
	// firstUse admits the first-use question for a not_granted spending
	// decision (runtime_asks.go). Nil admits none. Assign before serving.
	firstUse func(subject rwire.Subject, publisher func() asks.Publisher, action, resource string)
	// pairedInput admits the paired input-profile question a consuming request
	// raised (runtime_asks.go, INF-V4). Nil admits none. Assign before serving.
	pairedInput func(subject rwire.Subject, publisher func() asks.Publisher, resource string)
	// typedFirstUse admits a canonical service effect after a quiet missing permit.
	// Assign before serving; the Asks owner owns occurrence and consent lifecycle.
	typedFirstUse func(context.Context, rwire.PermissionEffect)
}

// installationRule is one exact rule an installation writes for each operator
// program.
type installationRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// appliedRule is one installation rule the marker records as written once.
type appliedRule struct {
	Program  string `json:"program"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

type defaultsMarker struct {
	Profile string        `json:"profile"`
	Applied []appliedRule `json:"applied"`
}

// composeRights opens the decision policy in the runtime's state directory, or
// returns nil when the runtime selected explicit endpoints without one. Every
// gated action is then unavailable: an absent policy is never permission.
func composeRights(options runtimeFlags) (*runtimeRights, error) {
	state, err := credentialsState(options)
	if err != nil || state == "" {
		return nil, err
	}
	if !filepath.IsAbs(state) {
		return nil, errors.New("rights: absolute state directory required")
	}
	_, endpoint, _, err := credentialsEndpoints(options)
	if err != nil {
		return nil, err
	}
	account, err := user.Current()
	if err != nil || account.Uid == "" {
		return nil, fmt.Errorf("rights: runtime account unavailable: %v", err)
	}
	operators, err := operatorPrograms(options)
	if err != nil {
		return nil, err
	}
	r := &runtimeRights{owner: account.Uid, kind: "posix", endpoint: endpoint, operators: operators,
		path: filepath.Join(state, "rights", "decisions.json"), marker: filepath.Join(state, "rights", defaultsMarkerFile)}
	if runtime.GOOS == "windows" {
		r.kind = "windows"
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return nil, err
	}
	// A policy file that cannot be read keeps the policy: every decision reads
	// unavailable until the file is repaired, and the error is reported.
	catalog, definitionErr := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if definitionErr != nil {
		return nil, definitionErr
	}
	r.policy, err = rights.LoadDecisionPolicyWithDefinitions(r.path, rwire.ResourceActions, catalog)
	if r.policy == nil {
		return nil, err
	}
	r.policy.MetaHolder = r.refuseAnswerer
	// Account-wide defaults (rights RIGHTS-R22, VISION 2026-10-07): every
	// program of this account may register itself, ask, announce, notify and
	// download unless its own rule or the switch says otherwise. A script host
	// never receives one; a remote peer's mapped namespace is another account.
	if configureErr := r.policy.ConfigureDefaults(r.owner, grants.RuntimeAccountDefaults(), defaultExcluded); configureErr != nil {
		return nil, configureErr
	}
	return r, err
}

// defaultExcluded is the one program rule for account-wide defaults: a script
// host (asks ASK-A8), whose grant would cover every script it runs, never
// receives one.
func defaultExcluded(s rwire.Subject) bool {
	return asks.ScriptHostSubject(s, asks.Publisher{})
}

// refuseAnswerer refuses enrolling an operator program or a script host as
// the answerer for every app (rights RIGHTS-R16, asks ASK-R7): optional Stop
// cleanup takes permits the answerer set, and an operator sets the person's own
// rules; every script a script host runs would answer.
func (r *runtimeRights) refuseAnswerer(s rwire.Subject) error {
	if s.Account != r.owner {
		return errors.New("an answerer answers for this account only")
	}
	if r.isOperator(s.Program) {
		return errors.New("an operator program is no answerer")
	}
	if asks.ScriptHostSubject(s, asks.Publisher{}) {
		return errors.New("a script host is no answerer")
	}
	return nil
}

// self is the runtime's own subject, the setter of installation rules.
func (r *runtimeRights) self() rwire.Subject {
	return rwire.Subject{Account: r.owner, Program: r.operators[0]}
}

// isOperator compares program with each operator program the way subject
// programs compare everywhere else (identity.SameSubjectProgram): by file
// identity when both exist.
func (r *runtimeRights) isOperator(program string) bool {
	return slices.ContainsFunc(r.operators, func(operator string) bool { return identity.SameSubjectProgram(operator, program) })
}

// authorizeOperator admits the operator programs of this account.
func (r *runtimeRights) authorizeOperator(_ context.Context, peer *identity.Peer) error {
	subject, err := credservice.SubjectFromPeer(peer)
	if err != nil || subject.Account != r.owner || !r.isOperator(subject.Program) {
		return rightsservice.ErrOperatorForbidden
	}
	return nil
}

// decider is the in-process decision point, or nil without a policy. It
// decides through the policy and admits first-use questions (Require).
func (r *runtimeRights) decider() host.Decider {
	if r == nil {
		return nil
	}
	return r
}

// install registers the composed capabilities' actions, then writes each
// installation rule an operator program has not received before and records it
// in the marker. A rule the person revoked is never written again, and an
// existing rule on the same target, a deny included, is left as it is. From
// then on the policy file is required: a removed file is an outage.
func (r *runtimeRights) install(actions []string, rules []installationRule) error {
	// The marker exists before this installation can create the policy file
	// (RegisterAction creates it), so a concurrent installation never reads
	// that new file as one made before the marker.
	marker, markerErr := r.startMarker()
	// Without a marker file (an I/O or lock failure) nothing is registered:
	// a policy file created now would read at the next start as one made
	// before the marker, crediting credentials rules nobody wrote. An
	// invalid marker exists, so the actions are still registered.
	var invalid invalidMarkerError
	if markerErr != nil && !errors.As(markerErr, &invalid) {
		return markerErr
	}
	for _, action := range actions {
		if err := r.policy.RegisterAction(action); err != nil {
			return errors.Join(markerErr, fmt.Errorf("rights: register %s: %w", action, err))
		}
	}
	if markerErr != nil {
		return markerErr
	}
	applied := map[appliedRule]bool{}
	for _, a := range marker.Applied {
		applied[a] = true
	}
	failure := r.apply(r.operators, rules, applied)
	marker.Applied = marker.Applied[:0]
	for key := range applied {
		marker.Applied = append(marker.Applied, key)
	}
	if err := r.writeMarker(marker); err != nil {
		return errors.Join(failure, err)
	}
	if failure != nil {
		return failure
	}
	if _, err := os.Stat(r.path); err == nil {
		r.policy.StateRequired = true
	}
	return nil
}

// installFor writes the same rules for programs that are not operators: a
// declared provider's own program, which needs a rule of its own because the
// runtime launches it and it then acts as itself. A rule the person revoked is
// never written again, and an existing rule on the same target is left as it
// is.
func (r *runtimeRights) installFor(programs []string, rules []installationRule) error {
	if r == nil || len(programs) == 0 || len(rules) == 0 {
		return nil
	}
	// As in install: the marker exists before a rule written here can
	// create the policy file.
	marker, err := r.startMarker()
	if err != nil {
		return err
	}
	applied := map[appliedRule]bool{}
	for _, a := range marker.Applied {
		applied[a] = true
	}
	failure := r.apply(programs, rules, applied)
	marker.Applied = marker.Applied[:0]
	for key := range applied {
		marker.Applied = append(marker.Applied, key)
	}
	if err := r.writeMarker(marker); err != nil {
		return errors.Join(failure, err)
	}
	return failure
}

// apply writes each rule a program has not received before and records it.
func (r *runtimeRights) apply(programs []string, rules []installationRule, applied map[appliedRule]bool) error {
	var failure error
	for _, program := range programs {
		for _, rule := range rules {
			key := appliedRule{program, rule.Action, rule.Resource}
			if applied[key] {
				continue
			}
			if failure = r.setInstallationRule(rwire.Subject{Account: r.owner, Program: program}, rule); failure != nil {
				break
			}
			applied[key] = true
		}
		if failure != nil {
			break
		}
	}
	return failure
}

// setInstallationRule writes one permit rule with why "installation" at the
// current revision, unless a rule on that target already exists. A conflict
// means another edit landed first and nothing was written; the rule is read and
// tried again. A file changed outside the policy's lock reads as a conflict
// too (rights DecisionPolicy.change).
func (r *runtimeRights) setInstallationRule(subject rwire.Subject, rule installationRule) error {
	return r.setRuleOnce(subject, rule, installationWhy)
}

// permitOperators writes each rule for every operator program that has no rule
// on its target, a deny included, with why. Nothing is recorded in the
// installation marker: a later event that calls it again writes the rule
// again only where none exists.
func (r *runtimeRights) permitOperators(rules []installationRule, why string) error {
	var errs []error
	for _, program := range r.operators {
		for _, rule := range rules {
			errs = append(errs, r.setRuleOnce(rwire.Subject{Account: r.owner, Program: program}, rule, why))
		}
	}
	return errors.Join(errs...)
}

// setRuleOnce writes one permit rule with why at the current revision, unless a
// rule on that target already exists.
func (r *runtimeRights) setRuleOnce(subject rwire.Subject, rule installationRule, why string) error {
	permit := true
	for attempt := 0; attempt < installationAttempts; attempt++ {
		existing := r.policy.ReadRule(subject, rule.Action, rule.Resource)
		switch existing.Outcome {
		case rwire.RuleReadOutcomeFound, rwire.RuleReadOutcomeExpired:
			return nil
		case rwire.RuleReadOutcomeUnknown:
		default:
			return fmt.Errorf("rights: installation rule %s on %s for %s: read %s", rule.Action, rule.Resource, subject.Program, existing.Outcome)
		}
		edit, err := r.policy.ChangeRule(existing.Revision, subject, rule.Action, rule.Resource,
			rights.RuleEdit{Permit: &permit, By: r.self(), Why: why}, func() error { return nil })
		if err != nil {
			return fmt.Errorf("rights: installation rule %s on %s for %s: %w", rule.Action, rule.Resource, subject.Program, err)
		}
		switch edit.Outcome {
		case rwire.PolicyEditOutcomeApplied:
			return nil
		case rwire.PolicyEditOutcomeConflict:
			continue
		}
		return fmt.Errorf("rights: installation rule %s on %s for %s: %s", rule.Action, rule.Resource, subject.Program, edit.Outcome)
	}
	return fmt.Errorf("rights: installation rule %s on %s for %s: policy kept changing", rule.Action, rule.Resource, subject.Program)
}

func (r *runtimeRights) readMarker() (defaultsMarker, error) {
	data, err := cas.ReadLimit(r.marker, maxDefaultsMarker)
	if err != nil {
		return defaultsMarker{}, fmt.Errorf("rights: %s: %w", defaultsMarkerFile, err)
	}
	if data == nil {
		return defaultsMarker{}, nil
	}
	var marker defaultsMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.Profile != defaultsMarkerProfile {
		return defaultsMarker{}, fmt.Errorf("rights: %s is not %s; no installation rule is written until it is repaired or removed", r.marker, defaultsMarkerProfile)
	}
	return marker, nil
}

// writeMarker adds the rules marker records to the marker file. The marker
// only grows: it merges with the file as it is under the file's lock, so a
// concurrent installation (the runtime's own, installFor for a declared
// provider, or a second runtime on the same state) never drops a rule another
// one recorded, and a rule the person revoked is never written again.
func (r *runtimeRights) writeMarker(marker defaultsMarker) error {
	return cas.ChangeLimit(r.marker, maxDefaultsMarker, func(cur []byte) ([]byte, error) {
		merged := map[appliedRule]bool{}
		if cur != nil {
			var current defaultsMarker
			if err := json.Unmarshal(cur, &current); err != nil || current.Profile != defaultsMarkerProfile {
				return nil, fmt.Errorf("rights: %s is not %s; no installation rule is recorded until it is repaired or removed", r.marker, defaultsMarkerProfile)
			}
			for _, a := range current.Applied {
				merged[a] = true
			}
		}
		for _, a := range marker.Applied {
			merged[a] = true
		}
		return markerBytes(merged)
	})
}

// startMarker reads the marker, creating it under its lock when absent. A
// policy file that exists while no marker does was made before the marker:
// the runtime then granted the operators the credentials rules when it
// created the file, and those count as received. The check and the creation
// share the marker's lock, and every installation creates the marker before
// it can create the policy file, so concurrent first installations on one
// state (two runtime starts) credit nothing that was never written.
func (r *runtimeRights) startMarker() (defaultsMarker, error) {
	var marker defaultsMarker
	var invalid error
	err := cas.ChangeLimit(r.marker, maxDefaultsMarker, func(cur []byte) ([]byte, error) {
		if cur != nil {
			if err := json.Unmarshal(cur, &marker); err != nil || marker.Profile != defaultsMarkerProfile {
				invalid = invalidMarkerError{path: r.marker}
				return nil, invalid
			}
			return cur, nil
		}
		applied := map[appliedRule]bool{}
		if _, err := os.Stat(r.path); err == nil {
			for _, program := range r.operators {
				for _, action := range []string{credentials.ActionManage, credentials.ActionRead} {
					applied[appliedRule{program, action, credentials.ResourceAccount}] = true
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("rights: %w", err)
		}
		data, err := markerBytes(applied)
		if err != nil {
			return nil, err
		}
		marker = defaultsMarker{Profile: defaultsMarkerProfile}
		for a := range applied {
			marker.Applied = append(marker.Applied, a)
		}
		return data, nil
	})
	if invalid != nil {
		return defaultsMarker{}, invalid
	}
	if err != nil {
		return defaultsMarker{}, fmt.Errorf("rights: %s: %w", defaultsMarkerFile, err)
	}
	return marker, nil
}

// invalidMarkerError is a marker file that exists but is not this profile's.
type invalidMarkerError struct{ path string }

func (e invalidMarkerError) Error() string {
	return fmt.Sprintf("rights: %s is not %s; no installation rule is written until it is repaired or removed", e.path, defaultsMarkerProfile)
}

// markerBytes is the marker file recording applied, in a stable order.
func markerBytes(applied map[appliedRule]bool) ([]byte, error) {
	next := defaultsMarker{Profile: defaultsMarkerProfile, Applied: make([]appliedRule, 0, len(applied))}
	for a := range applied {
		next.Applied = append(next.Applied, a)
	}
	slices.SortFunc(next.Applied, func(a, b appliedRule) int {
		return strings.Compare(a.Program+"\x00"+a.Action+"\x00"+a.Resource, b.Program+"\x00"+b.Action+"\x00"+b.Resource)
	})
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// gateByRights has each composed resource service decide its gated actions in
// the runtime's own process (research/rights-defaults/DECISION.md §1): config
// ReplaceUser, logging history reads and observation, model lookup per
// registry, router inventory and routes, and job Submit. Config reads, log
// writes and a caller's own work stay open. A nil decider makes each gated
// call unavailable: an absent policy is never permission.
func gateByRights(o *host.Options, decider host.Decider) {
	o.ConfigEditPolicy = host.ConfigEditPolicyFromRights(decider)
	o.LogHistoryPolicy = host.HistoryPolicyFromRights(decider)
	o.LogApplicationHistoryPolicy = host.ApplicationHistoryPolicyFromRights(decider)
	o.LogSourcePolicy = host.SourcePolicyFromRights(decider)
	if o.ModelRegistry != nil {
		o.ModelPolicy = host.ModelPolicyFromRights(decider)
	}
	if o.Router != nil {
		o.RouterPolicy = host.RouterPolicyFromRights(decider)
	}
	if o.ResourceTable != nil {
		o.ResourceTablePolicy = host.ResourceTablePolicyFromRights(decider)
	}
	if o.JobRoot != "" {
		o.JobMethodPolicy = host.JobPolicyFromRights(decider, host.JobRightsActions)
	}
}

// installationRules are the exact rules each operator program holds by
// installation for the capabilities this composition serves
// (research/rights-defaults/DECISION.md §2). Model lookup has one rule per
// composed registry.
func installationRules(o host.Options, registries []string) []installationRule {
	rules := []installationRule{
		{host.ConfigEditAction, host.ConfigEditResource},
		{host.LogHistoryAction, host.LogHistoryResource},
	}
	if o.JobRoot != "" {
		for _, action := range []string{host.JobSubmitAction, host.JobCancelAction, host.JobInventoryAction} {
			rules = append(rules, installationRule{action, host.JobResource})
		}
	}
	for _, registry := range registries {
		rules = append(rules, installationRule{host.ModelLookupAction, registry})
	}
	if o.Router != nil {
		rules = append(rules, installationRule{routerservice.ActionInventory, routerservice.ResourceInventory}, installationRule{routerservice.ActionRoute, host.RoutesResource})
	}
	if o.ResourceTable != nil {
		rules = append(rules, installationRule{host.ResourceTableReadAction, host.ResourceTableReadResource})
	}
	if o.ResourceLeases != nil {
		rules = append(rules, resourceRules()...)
	}
	if o.Credentials != nil {
		rules = append(rules, installationRule{credentials.ActionManage, credentials.ResourceAccount}, installationRule{credentials.ActionRead, credentials.ResourceAccount})
	}
	if o.Inference != nil {
		rules = append(rules, installationRule{inference.ActionHostManage, credentials.ResourceAccount}, installationRule{inference.ActionKeyIssue, credentials.ResourceAccount},
			installationRule{inference.ActionAuditRead, credentials.ResourceAccount}, installationRule{inference.ActionSelectionManage, credentials.ResourceAccount},
			installationRule{inference.ActionSelectionManageFor, credentials.ResourceAccount})
	}
	if o.Applications != nil {
		// Operators manage the directory. Reading and starting an entry
		// follows its registration: the Panel's confirmed Register writes
		// its own rules, and a self-registration has the runtime write them
		// (APPLICATIONS.md); installation names no app.
		rules = append(rules, installationRule{ActionApplicationManage, credentials.ResourceAccount})
	}
	if o.Registry != nil {
		rules = append(rules, installationRule{ActionProviderManage, credentials.ResourceAccount})
	}
	return rules
}

// configure publishes the decision point, its operator profile and every
// action the composed capabilities register.
func (r *runtimeRights) configure(o *host.Options, actions []string) {
	if r == nil {
		return
	}
	// Freeze actual receiving presence once, before any composed host serves.
	// Known target grammar was loaded earlier, even when a receiver is absent.
	credentialReady := false
	if scoped, ok := o.Credentials.(interface{ ScopedAvailable() bool }); ok {
		credentialReady = scoped.ScopedAvailable()
	}
	logReady := false
	if provider, ok := o.Sink.(interface{ ApplicationHistoryAvailable() bool }); ok {
		logReady = provider.ApplicationHistoryAvailable() && o.LogApplicationHistoryPolicy.Check != nil && o.LogApplicationHistoryPolicy.Fits != nil
	}
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: credentialReady, ApplicationLogs: logReady})
	if err != nil {
		if o.OnError != nil {
			o.OnError(err)
		}
		return
	}
	r.policy.Definitions = catalog
	o.RightsPolicy, o.RightsEndpoint, o.RightsOperator = r.policy, r.endpoint, r.authorizeOperator
	o.RightsActions = append(o.RightsActions, actions...)
}

// These receiving paths are always quiet and preserve the shared owner snapshot.
// Captured targets come from the receiving capability and are never re-prepared.
func (r *runtimeRights) CheckBatchContext(ctx context.Context, subject rwire.Subject, request rwire.CheckBatchRequest) rwire.BatchDecision {
	if r == nil || r.policy == nil {
		return rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeUnavailable, Outcomes: []rwire.DecisionOutcome{}}
	}
	if subject.Account != r.owner {
		return rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeForbidden, Outcomes: []rwire.DecisionOutcome{}}
	}
	ctx, cancel := context.WithTimeout(ctx, decisionBudget)
	defer cancel()
	return r.policy.CheckBatchContext(ctx, subject, request)
}
func (r *runtimeRights) CheckLegacyOperationContext(ctx context.Context, subject rwire.Subject, request rwire.LegacyOperationRequest) rwire.Decision {
	if r == nil || r.policy == nil {
		return rwire.Decision{Outcome: rwire.DecisionOutcomeUnavailable}
	}
	if subject.Account != r.owner {
		return rwire.Decision{Outcome: rwire.DecisionOutcomeForbidden}
	}
	ctx, cancel := context.WithTimeout(ctx, decisionBudget)
	defer cancel()
	return r.policy.CheckLegacyOperationContext(ctx, subject, request)
}
