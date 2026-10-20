package gateway

import (
	"context"
	"encoding/json"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	wire "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

const ToolJobsFollow = "oa_jobs_follow"

type jobsFollowBackend interface {
	JobsFollowWork(context.Context, wire.RequestIdentity, string, int64) (wire.WorkObservation, error)
}

func (o *OA) JobsFollowWork(ctx context.Context, identity wire.RequestIdentity, after string, waitMS int64) (wire.WorkObservation, error) {
	binding, err := o.machine.ResolveService(ctx, facade.JobOperationsContract, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return wire.WorkObservation{}, err
	}
	// Keep the resolved provider's native trust. The ordinary tool budget
	// covers the declared 30-second read, including resolution and transport.
	client, err := facade.NewJobsWithTransport(binding.Transport().WithDefaults(2*time.Minute, 2<<20), facade.JobsOptions{})
	if err != nil {
		return wire.WorkObservation{}, err
	}
	return client.FollowWork(ctx, identity, after, waitMS)
}

type jobsFollowInput struct {
	Identity json.RawMessage `json:"identity"`
	After    string          `json:"after"`
	WaitMS   int64           `json:"wait_ms"`
}

type jobsFollowIdentitySchema struct {
	Key          string `json:"key"`
	HistoryEpoch string `json:"history_epoch"`
	Attempt      int64  `json:"attempt,omitempty"`
}

func jobsFollowSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[jobsFollowInput](nil)
	if err != nil {
		panic(err)
	}
	identity, err := jsonschema.For[jobsFollowIdentitySchema](nil)
	if err != nil {
		panic(err)
	}
	schema.Properties["identity"] = identity
	minimum, maximum, length := float64(0), float64(30000), 256
	schema.Properties["wait_ms"].Minimum = &minimum
	schema.Properties["wait_ms"].Maximum = &maximum
	schema.Properties["after"].MaxLength = &length
	return schema
}

func validJobsFollowInput(input jobsFollowInput, id wire.RequestIdentity) bool {
	if input.WaitMS < 0 || input.WaitMS > 30000 || len(input.After) > 256 || !utf8.ValidString(input.After) ||
		id.Key == "" || id.HistoryEpoch == "" || id.Attempt < 0 || len(id.Key) > 1024 || len(id.HistoryEpoch) > 1024 || !utf8.ValidString(id.Key) || !utf8.ValidString(id.HistoryEpoch) {
		return false
	}
	for _, r := range input.After {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func addJobsFollowTool(server *mcp.Server, backend jobsFollowBackend, bounded func(context.Context, func(context.Context) error) error, read *mcp.ToolAnnotations) {
	mcp.AddTool(server, &mcp.Tool{Name: ToolJobsFollow, Description: "Follow an accepted job. Pass the previous cursor as after, or an empty one for a snapshot.", Annotations: read, InputSchema: jobsFollowSchema()},
		func(ctx context.Context, _ *mcp.CallToolRequest, input jobsFollowInput) (*mcp.CallToolResult, any, error) {
			result := wire.WorkObservation{Outcome: wire.WorkObservationOutcomeInvalid, Mechanism: wire.WorkObservationMechanismPolled}
			err := bounded(ctx, func(ctx context.Context) (err error) {
				var identity wire.RequestIdentity
				if wire.FromJSON(input.Identity, &identity) == nil && validJobsFollowInput(input, identity) {
					result, err = backend.JobsFollowWork(ctx, identity, input.After, input.WaitMS)
				}
				return err
			})
			if err != nil {
				return nil, nil, err
			}
			raw, err := wire.ToJSON(&result)
			if err != nil {
				return nil, nil, err
			}
			answer, err := appModelResult(raw, false)
			if err == nil {
				// Reuse the bounded generated-answer carrier and untrusted text
				// annotations with this method's closed outcome classification.
				answer.IsError = result.Outcome != wire.WorkObservationOutcomeChanged && result.Outcome != wire.WorkObservationOutcomeUnchanged && result.Outcome != wire.WorkObservationOutcomeGap
			}
			return answer, nil, err
		})
}
