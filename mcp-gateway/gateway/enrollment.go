package gateway

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

const ToolApplicationsEnrollmentRead = "oa_applications_enrollment_read"

type enrollmentBackend interface {
	ApplicationsEnrollmentRead(context.Context, string) (applications.AgentEnrollmentResult, error)
}

func (o *OA) ApplicationsEnrollmentRead(ctx context.Context, application string) (applications.AgentEnrollmentResult, error) {
	client, err := o.appModelClient(ctx)
	if err != nil {
		return applications.AgentEnrollmentResult{}, err
	}
	return client.ReadAgentEnrollment(application)
}

type enrollmentReadInput struct {
	Application string `json:"application"`
}

func addEnrollmentReadTool(server *mcp.Server, backend enrollmentBackend, bounded func(context.Context, func(context.Context) error) error, read *mcp.ToolAnnotations) {
	mcp.AddTool(server, &mcp.Tool{Name: ToolApplicationsEnrollmentRead, Description: "Read an application's enrollment state; empty application reads your own.", Annotations: read},
		func(ctx context.Context, _ *mcp.CallToolRequest, input enrollmentReadInput) (*mcp.CallToolResult, any, error) {
			var result applications.AgentEnrollmentResult
			err := bounded(ctx, func(ctx context.Context) (err error) {
				result, err = backend.ApplicationsEnrollmentRead(ctx, input.Application)
				return err
			})
			if err != nil {
				return nil, nil, err
			}
			raw, err := applications.ToJSON(&result)
			if err != nil {
				return nil, nil, err
			}
			answer, err := appModelResult(raw, result.Outcome != applications.OutcomePage)
			return answer, nil, err
		})
}
