package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// Invoked only by the isolated native Applications recovery fixture, which
// supplies its endpoint/server evidence and admits this actual child image.
// No Panel, vendor command, settings writer or model is started.
func TestClaudeStartupNativeRecoveryChild(t *testing.T) {
	raw := os.Getenv("OA_CLAUDE_STARTUP_RECOVERY_FIXTURE")
	if raw == "" {
		t.Skip("isolated native recovery parent supplies this fixture")
	}
	var input struct {
		Endpoint, Application, ExpectedContext string
		Server                                 *listen.ServerExpectation
	}
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &input) != nil || input.Endpoint == "" || input.Application == "" || input.ExpectedContext == "" || input.Server == nil {
		t.Fatal("invalid native recovery fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	transport := listen.FrameClient{Endpoint: input.Endpoint, Server: input.Server}
	c := &claudePanelConnections{revision: 1}
	c.startupRead = func(ctx context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
		page, err := wire.NewApplicationsClient(transport.WithContext(ctx)).Observe("", 0)
		if err != nil || page.Outcome != wire.ApplicationOutcomePage {
			return wire.ApplicationDescriptor{}, applications.AgentEnrollmentResult{}, errors.New("fixture descriptor unavailable")
		}
		var descriptor wire.ApplicationDescriptor
		for _, entry := range page.Applications {
			if entry.Scope == wire.ScopeLocal && entry.Descriptor.Name == input.Application {
				if descriptor.Name != "" {
					t.Fatal("ambiguous fixture descriptor")
				}
				descriptor = entry.Descriptor
			}
		}
		if descriptor.Name == "" {
			return descriptor, applications.AgentEnrollmentResult{}, errors.New("fixture descriptor absent")
		}
		enrollment, err := applications.NewApplicationsClient(transport.WithContext(ctx)).ReadAgentEnrollment(descriptor.Name)
		return descriptor, enrollment, err
	}
	c.startupSet = func(context.Context, applications.ActivationConfiguration) (wire.ApplicationChange, error) {
		t.Fatal("recovery created a new activation intent")
		return wire.ApplicationChange{}, errors.New("new intent refused")
	}
	c.startupFinish = func(ctx context.Context, expected wire.ApplicationDescriptor) (wire.ApplicationChange, error) {
		return applications.NewApplicationsClient(transport.WithContext(ctx)).ReconcileActivation(expected)
	}
	view, err := c.readStartup(ctx, "1")
	if err != nil || !view.Recovery {
		t.Fatal("fresh controller did not receive interrupted configuration", view, err)
	}
	if err := c.changeStartup(ctx, claudeStartupEdit{Revision: "1", Action: "finish"}); err != nil {
		t.Fatal(err)
	}
	view, err = c.readStartup(ctx, "1")
	if err != nil || view.Recovery || !view.Configured {
		t.Fatal("native recovery did not restore configured enrollment", view, err)
	}
	profile, err := startprofile.Parse(c.startupCapture.descriptor.Activation.Arguments)
	if err != nil || profile.Context != input.ExpectedContext {
		t.Fatal("recovery changed original routing context", profile.Context, err)
	}
	if err := c.changeStartup(ctx, claudeStartupEdit{Revision: "1", Action: "finish"}); err == nil {
		t.Fatal("no-pending recovery claimed another completion")
	}
}
