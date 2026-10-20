package main

import (
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go"
)

// The unpublished @3 receiver forwards these through the existing native
// caller/Program-proof owner. The published @2 dispatcher exposes neither.
func (r *applicationsReceiver) ReadAgentEnrollment(application string) (applications.AgentEnrollmentResult, error) {
	return r.host.directory.readAgentEnrollment(r.ctx, r.caller(), application), nil
}
func (r *applicationsReceiver) SetAgentEnrollment(request applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
	if r.call == nil || r.call.Recheck() != nil {
		return agentEnrollmentResult(applications.OutcomeForbidden, "caller"), nil
	}
	ctx := r.ctx
	if request.BindCatalogueHost != nil && *request.BindCatalogueHost {
		peer, err := r.call.Peer()
		if err != nil || peer == nil {
			return agentEnrollmentResult(applications.OutcomeForbidden, "caller"), nil
		}
		ctx = rights.WithBoundPeer(ctx, peer)
	}
	return r.host.directory.setAgentEnrollment(ctx, r.caller(), request), nil
}

func (r *applicationsReceiver) PrepareAgentUpdate(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
	if r.call == nil || r.call.Recheck() != nil {
		return agentUpdateResult(applications.OutcomeForbidden, "caller"), nil
	}
	return r.host.directory.prepareAgentUpdate(r.ctx, r.caller(), request), nil
}
func (r *applicationsReceiver) FindAgentUpdate(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
	return r.host.directory.findAgentUpdate(r.ctx, r.caller(), request), nil
}
func (r *applicationsReceiver) CommitAgentUpdate(intent string) (applications.AgentUpdateResult, error) {
	if r.call == nil || r.call.Recheck() != nil {
		return agentUpdateResult(applications.OutcomeForbidden, "caller"), nil
	}
	return r.host.directory.commitAgentUpdate(r.ctx, r.caller(), intent), nil
}
