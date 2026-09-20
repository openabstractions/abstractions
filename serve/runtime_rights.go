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

	cas "github.com/openabstractions/abstraction-cas/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
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
	firstUse func(subject rwire.Subject, action, resource string)
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
	operators, err := operatorPrograms()
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
	r.policy, err = rights.LoadDecisionPolicy(r.path, rwire.ResourceActions)
	if r.policy == nil {
		return nil, err
	}
	return r, err
}

// self is the runtime's own subject, the setter of installation rules.
func (r *runtimeRights) self() rwire.Subject {
	return rwire.Subject{Account: r.owner, Program: r.operators[0]}
}

// authorizeOperator admits the operator programs of this account.
func (r *runtimeRights) authorizeOperator(_ context.Context, peer *identity.Peer) error {
	subject, err := credservice.SubjectFromPeer(peer)
	if err != nil || subject.Account != r.owner || !slices.Contains(r.operators, subject.Program) {
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
	_, statErr := os.Stat(r.path)
	existed := statErr == nil
	for _, action := range actions {
		if err := r.policy.RegisterAction(action); err != nil {
			return fmt.Errorf("rights: register %s: %w", action, err)
		}
	}
	marker, err := r.readMarker()
	if err != nil {
		return err
	}
	applied := map[appliedRule]bool{}
	for _, a := range marker.Applied {
		applied[a] = true
	}
	if marker.Profile == "" {
		marker.Profile = defaultsMarkerProfile
		// Before the marker, the runtime granted the operators the credentials
		// rules when it created the policy file. Those count as received.
		if existed {
			for _, program := range r.operators {
				for _, action := range []string{credentials.ActionManage, credentials.ActionRead} {
					applied[appliedRule{program, action, credentials.ResourceAccount}] = true
				}
			}
		}
	}
	var failure error
	for _, program := range r.operators {
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
	marker.Applied = marker.Applied[:0]
	for key := range applied {
		marker.Applied = append(marker.Applied, key)
	}
	slices.SortFunc(marker.Applied, func(a, b appliedRule) int {
		return strings.Compare(a.Program+"\x00"+a.Action+"\x00"+a.Resource, b.Program+"\x00"+b.Action+"\x00"+b.Resource)
	})
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

// setInstallationRule writes one permit rule with why "installation" at the
// current revision, unless a rule on that target already exists. A conflict
// means another edit landed first and nothing was written; the rule is read and
// tried again.
func (r *runtimeRights) setInstallationRule(subject rwire.Subject, rule installationRule) error {
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
			rights.RuleEdit{Permit: &permit, By: r.self(), Why: installationWhy}, func() error { return nil })
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

func (r *runtimeRights) writeMarker(marker defaultsMarker) error {
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return cas.ChangeLimit(r.marker, maxDefaultsMarker, func([]byte) ([]byte, error) { return data, nil })
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
	if o.ModelRegistry != nil {
		o.ModelPolicy = host.ModelPolicyFromRights(decider)
	}
	if o.Router != nil {
		o.RouterPolicy = host.RouterPolicyFromRights(decider)
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
	if o.Credentials != nil {
		rules = append(rules, installationRule{credentials.ActionManage, credentials.ResourceAccount}, installationRule{credentials.ActionRead, credentials.ResourceAccount})
	}
	if o.Inference != nil {
		rules = append(rules, installationRule{inference.ActionHostManage, credentials.ResourceAccount}, installationRule{inference.ActionKeyIssue, credentials.ResourceAccount},
			installationRule{inference.ActionAuditRead, credentials.ResourceAccount})
	}
	if o.Applications != nil {
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
	o.RightsPolicy, o.RightsEndpoint, o.RightsOperator = r.policy, r.endpoint, r.authorizeOperator
	o.RightsActions = append(o.RightsActions, actions...)
}
