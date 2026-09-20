package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	facade "github.com/openabstractions/abstraction-facade/go/client"
)

const maxApplicationsOutputBytes = 256 << 10

// Applications reads one complete, permission-filtered local directory page.
// The native Applications service binds Observe to the gateway executable; MCP
// metadata cannot select a different subject.
func (o *OA) Applications(ctx context.Context, _ ApplicationsInput) (ApplicationsOutput, error) {
	c, err := o.machine.ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return ApplicationsOutput{}, err
	}
	page, err := c.Observe(ctx, "", 0)
	if err != nil {
		return ApplicationsOutput{}, err
	}
	return applicationsOutput(page)
}

func applicationsOutput(page wire.ApplicationPage) (ApplicationsOutput, error) {
	out := ApplicationsOutput{
		Outcome: page.Outcome.String(), ObservedUnixMS: time.Now().UnixMilli(),
		Applications: make([]Application, 0, len(page.Applications)),
	}
	for _, entry := range page.Applications {
		application := Application{
			Name: entry.Descriptor.Name, Title: entry.Descriptor.Title,
			Scope: entry.Scope.String(), Instances: make([]ApplicationInstance, 0, len(entry.Instances)),
		}
		for _, source := range entry.Instances {
			instance := ApplicationInstance{
				Instance: source.Instance, ExpiresUnixMS: source.ExpiresUnixMs,
				Interfaces: make([]ApplicationInterface, 0, len(source.Interfaces)),
				Contexts:   make([]ApplicationContext, 0, len(source.Contexts)),
			}
			for _, iface := range source.Interfaces {
				instance.Interfaces = append(instance.Interfaces, ApplicationInterface{Name: iface.Name, Protocol: iface.Protocol, Contract: iface.Contract})
			}
			for _, context := range source.Contexts {
				instance.Contexts = append(instance.Contexts, ApplicationContext{Name: context.Name, Title: context.Title, Revision: context.Revision})
			}
			application.Instances = append(application.Instances, instance)
		}
		out.Applications = append(out.Applications, application)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return ApplicationsOutput{}, err
	}
	if len(encoded) > maxApplicationsOutputBytes {
		return ApplicationsOutput{}, errors.New("gateway: application directory exceeds 262144-byte bound")
	}
	return out, nil
}
