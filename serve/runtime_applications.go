package main

import (
	"context"
	"errors"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func composeApplications(options runtimeFlags, policy *runtimeRights, report func(error)) (*applicationsHost, string, error) {
	if policy == nil {
		return nil, "", errors.New("applications: rights policy unavailable")
	}
	state, err := credentialsState(options)
	if err != nil {
		return nil, "", err
	}
	endpoint, err := options.companionEndpoint("applications-v1", "", "applications")
	if err != nil {
		return nil, "", err
	}
	directory, err := openApplications(state, policy.owner, func(ctx context.Context, s rights.Subject, action, resource string) (bool, error) {
		decision := policy.decide(ctx, s, action, resource)
		if decision.Outcome == rights.DecisionOutcomeUnavailable {
			return false, errors.New("applications: policy unavailable")
		}
		return decision.Outcome == rights.DecisionOutcomePermitted, nil
	})
	if err != nil {
		return nil, "", err
	}
	// Only the operator programs read the start log (APPLICATIONS.md).
	directory.operator = policy.isOperator
	// A program that registered itself is listed and started by the
	// operator programs (APPLICATIONS.md): the runtime writes read and
	// activate on its entry for each operator with no rule there, a Block
	// included, and reports a failure without undoing the registration.
	directory.registeredSelf = func(name string) {
		resource := "app:" + name
		rules := []installationRule{{ActionApplicationRead, resource}, {ActionApplicationActivate, resource}}
		if err := policy.permitOperators(rules, registeredSelfWhy); err != nil && report != nil {
			report(err)
		}
	}
	listener, err := listenApplications(endpoint, directory, policy.owner, report)
	return listener, endpoint, err
}
func configureApplications(o *host.Options, h *applicationsHost, endpoint string) {
	if h == nil {
		return
	}
	o.Applications = h
	o.ApplicationsEndpoint = endpoint
	o.RightsActions = append(o.RightsActions, ActionApplicationManage, ActionApplicationRead, ActionApplicationAnnounce, ActionApplicationActivate, ActionApplicationRegisterSelf)
}

// registeredSelfWhy is the reason on the operator rules a self-registration
// writes.
const registeredSelfWhy = "registered by the app"
