package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
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
	endpoint := options.endpoint + "-applications"
	if options.endpoint == "" {
		endpoint, err = bootstrap.Endpoint("applications-v1")
		if err != nil {
			return nil, "", err
		}
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
	listener, err := listenApplications(endpoint, directory, policy.owner, report)
	return listener, endpoint, err
}
func configureApplications(o *host.Options, h *applicationsHost, endpoint string) {
	if h == nil {
		return
	}
	o.Applications = h
	o.ApplicationsEndpoint = endpoint
	o.RightsActions = append(o.RightsActions, ActionApplicationManage, ActionApplicationRead, ActionApplicationAnnounce, ActionApplicationActivate)
}
