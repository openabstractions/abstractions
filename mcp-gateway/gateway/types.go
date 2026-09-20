package gateway

import "context"

const (
	ToolModels       = "oa_models_list"
	ToolApplications = "oa_applications_list"
	ToolComplete     = "oa_inference_complete"
	ToolJobSubmit    = "oa_inference_job_submit"
	ToolJobStatus    = "oa_inference_job_status"
	ToolJobCancel    = "oa_inference_job_cancel"
)

type ApplicationsInput struct{}

type ApplicationInterface struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Contract string `json:"contract"`
}

type ApplicationContext struct {
	Name     string `json:"name"`
	Title    string `json:"title,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type ApplicationInstance struct {
	Instance      string                 `json:"instance"`
	Interfaces    []ApplicationInterface `json:"interfaces,omitempty"`
	Contexts      []ApplicationContext   `json:"contexts,omitempty"`
	ExpiresUnixMS int64                  `json:"expires_unix_ms"`
}

// Application intentionally omits executable identity, launch recipes and
// inert start guidance. This tool is directory discovery, not activation.
type Application struct {
	Name      string                `json:"name"`
	Title     string                `json:"title,omitempty"`
	Scope     string                `json:"scope"`
	Instances []ApplicationInstance `json:"instances,omitempty"`
}

type ApplicationsOutput struct {
	Outcome        string        `json:"outcome"`
	ObservedUnixMS int64         `json:"observed_unix_ms"`
	Applications   []Application `json:"applications"`
}

type Model struct {
	Family   string   `json:"family"`
	Aliases  []string `json:"aliases"`
	Profiles []string `json:"profiles,omitempty"`
}

type ModelsInput struct {
	Fresh bool `json:"fresh,omitempty" jsonschema:"request a fresh bounded router survey"`
}

type ModelsOutput struct {
	ObservedUnixMS int64   `json:"observed_unix_ms"`
	Models         []Model `json:"models"`
}

type CompleteInput struct {
	Model      string `json:"model" jsonschema:"model family or alias"`
	Prompt     string `json:"prompt" jsonschema:"user prompt, at most 65536 bytes"`
	MaxOutput  int64  `json:"max_output,omitempty" jsonschema:"maximum output tokens, 1 to 4096"`
	Hosting    string `json:"hosting" jsonschema:"local or hosted"`
	Credential string `json:"credential,omitempty" jsonschema:"authorized OA credential name for hosted inference, never a secret value"`
}

type CompleteOutput struct {
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
	Text       string `json:"text,omitempty"`
	Host       string `json:"host,omitempty"`
	Model      string `json:"model,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

type JobSubmitInput struct {
	RequestKey string `json:"request_key" jsonschema:"stable caller-generated idempotency key, at most 128 characters"`
	Profile    string `json:"profile" jsonschema:"image_batch or video"`
	Model      string `json:"model" jsonschema:"model family or alias"`
	Prompt     string `json:"prompt" jsonschema:"generation prompt, at most 65536 bytes"`
	Hosting    string `json:"hosting" jsonschema:"must be hosted for the currently supported durable providers"`
	Credential string `json:"credential" jsonschema:"authorized OA credential name, never a secret value"`
	Size       string `json:"size,omitempty" jsonschema:"provider-supported output size"`
	Count      int64  `json:"count,omitempty" jsonschema:"image count, 1 to 10"`
	DurationMS int64  `json:"duration_ms,omitempty" jsonschema:"video duration, 1 to 600000 milliseconds"`
}

type JobSubmitOutput struct {
	Handle      string   `json:"handle"`
	Outcome     string   `json:"outcome"`
	Reason      string   `json:"reason,omitempty"`
	OperationID string   `json:"operation_id,omitempty"`
	Guarantees  []string `json:"accepted_guarantees,omitempty"`
}

type JobHandleInput struct {
	Handle string `json:"handle" jsonschema:"opaque handle returned by oa_inference_job_submit"`
}

type JobStatusOutput struct {
	Outcome       string           `json:"outcome"`
	ResultOutcome string           `json:"result_outcome,omitempty"`
	State         string           `json:"state,omitempty"`
	Waiting       string           `json:"waiting,omitempty"`
	Progress      map[string]int64 `json:"progress,omitempty"`
	Failure       string           `json:"failure,omitempty"`
	Result        any              `json:"result,omitempty"`
}

type JobCancelOutput struct {
	Outcome string `json:"outcome"`
}

// Backend is one already-authorized OA integration principal. Its identity is
// fixed when the process connects to OA; MCP request metadata never selects it.
type Backend interface {
	Applications(context.Context, ApplicationsInput) (ApplicationsOutput, error)
	Models(context.Context, ModelsInput) (ModelsOutput, error)
	Complete(context.Context, CompleteInput) (CompleteOutput, error)
	Submit(context.Context, JobSubmitInput) (JobSubmitOutput, error)
	Status(context.Context, JobHandleInput) (JobStatusOutput, error)
	Cancel(context.Context, JobHandleInput) (JobCancelOutput, error)
}
