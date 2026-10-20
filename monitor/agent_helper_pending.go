package main

import (
	"context"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// agentHelperReading is the Panel's one pending check for an owned connection
// image. State is "update" while the person still has to approve the installed
// connector and "restart" once Setup's question was answered and the running
// agent apps in Restart keep the previous connector in use. Failure carries
// the background retry's reason when an approved update stopped for another
// cause; the state is then "update" again.
type agentHelperReading struct {
	State   string
	Restart []string
	Failure string
}

// agentHelperPending combines the image comparison with the retained native
// update and the background retry's recorded stop for that update. The update
// batch publishes all members together, so Restart names every app in it.
func agentHelperPending(currency string, retained applications.AgentUpdateResult, stopped string) agentHelperReading {
	reading := agentHelperReading{State: currency}
	if currency != "update" || retained.Outcome != applications.OutcomeApplied || retained.Reason != "update_ready" || retained.Intent == "" {
		return reading
	}
	if stopped != "" {
		reading.Failure = stopped
		return reading
	}
	for _, party := range agentHelperParties {
		for _, image := range retained.StagedImages {
			if image.Application == "oa-"+string(party.party)+"-integration" {
				reading.Restart = append(reading.Restart, party.name)
				break
			}
		}
	}
	if len(reading.Restart) == 0 {
		return reading
	}
	reading.State = "restart"
	return reading
}

// panelAgentHelperReading reads the image and, only when it differs from the
// installed connector, the Applications service's retained update intent.
func panelAgentHelperReading(ctx context.Context, party setup.AgentParty, program, key string) agentHelperReading {
	currency := setup.AgentImageCurrency(ctx, panelMachine(), party, program, key)
	if currency != "update" {
		return agentHelperReading{State: currency}
	}
	retained := findRetainedAgentUpdate(ctx, party)
	stopped := ""
	if retained.Reason == "update_ready" {
		stopped, _ = setup.ReadAgentUpdateRetry(retained.Intent)
	}
	return agentHelperPending(currency, retained, stopped)
}

func findRetainedAgentUpdate(ctx context.Context, party setup.AgentParty) applications.AgentUpdateResult {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return applications.AgentUpdateResult{}
	}
	found, err := applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).FindAgentUpdate(applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: "oa-" + string(party) + "-integration"}}})
	if err != nil {
		return applications.AgentUpdateResult{}
	}
	return found
}
