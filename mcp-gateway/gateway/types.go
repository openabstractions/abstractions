package gateway

import "context"

const (
	ToolModels       = "oa_models_list"
	ToolApplications = "oa_applications_list"
	ToolComplete     = "oa_inference_complete"
	ToolJobSubmit    = "oa_inference_job_submit"
	ToolJobStatus    = "oa_inference_job_status"
	ToolJobDiagnose  = "oa_inference_job_diagnose"
	ToolJobCancel    = "oa_inference_job_cancel"
	ToolSetupFit     = "oa_setup_fit"
	ToolSetupPrepare = "oa_setup_prepare"
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

// Application's tool output omits executable identity, launch recipes and
// inert start guidance. This tool is directory discovery, not activation.
// Program and SelfRegistered stay in process for the gateway's own sender
// title rule (wakeSender) and are never serialized.
type Application struct {
	Name           string                `json:"name"`
	Title          string                `json:"title,omitempty"`
	Scope          string                `json:"scope"`
	Instances      []ApplicationInstance `json:"instances,omitempty"`
	Program        string                `json:"-"`
	SelfRegistered bool                  `json:"-"`
}

type ApplicationsOutput struct {
	Outcome        string        `json:"outcome"`
	ObservedUnixMS int64         `json:"observed_unix_ms"`
	Applications   []Application `json:"applications"`
}

type Model struct {
	Family string `json:"family"`
	// Name is how to name the model to a person: the catalogue's title;
	// else the family, else the first alias, when no part of it carries a
	// digest; else the plain last part of one; else the architecture its
	// file's header states; else "Unnamed model", with a log line. A model
	// host serving a store's object under a name joins that object's family,
	// so the name a person gave it with --as names it. Family and Aliases
	// stay the identifiers a call sends back. A store's file named by its
	// SHA-256 has a family that is that digest, which is never shown (owner,
	// 2026-11-01).
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Profiles []string `json:"profiles,omitempty"`
	// Servable is false for a model this machine holds that no host serves.
	// Such a model carries no aliases to ask for and is listed so the caller
	// sees the whole machine.
	Servable bool `json:"servable"`
	// HeldIn names the storage inventory stores holding this model, empty
	// when no store reports it. Reading it changes nothing.
	HeldIn []string `json:"held_in,omitempty"`
	// ServedFrom names the stores whose copy this machine's engines serve,
	// listed only when HeldIn names another copy no engine here serves.
	ServedFrom []string `json:"served_from,omitempty"`
	// SetupNeeded marks a model OpenAbstractions Setup sets up on this
	// computer that nothing here serves or holds yet; it is never servable.
	// SetupUse is the use oa_setup_prepare and Setup take for it,
	// DownloadBytes its approximate download, and PresetTimes its measured
	// preset runs when known.
	// Capabilities is one line per alias serving this model, prefixed by
	// its host ("local" for this machine, else the hosted provider's
	// name): its profiles, input and output modalities, tools, structured
	// output, context and reasoning controls, from that alias's own
	// capabilities. Two hosts of one family are two lines, never merged.
	Capabilities  []string     `json:"capabilities,omitempty"`
	SetupNeeded   bool         `json:"setup_needed,omitempty"`
	SetupUse      string       `json:"setup_use,omitempty"`
	DownloadBytes int64        `json:"download_bytes,omitempty"`
	PresetTimes   []PresetTime `json:"preset_times,omitempty"`
}

type ModelsInput struct {
	Fresh bool `json:"fresh,omitempty" jsonschema:"Survey the servers again."`
}

type ModelsOutput struct {
	ObservedUnixMS int64   `json:"observed_unix_ms"`
	Models         []Model `json:"models"`
}

type CompleteInput struct {
	IncludeReasoning bool   `json:"include_reasoning,omitempty" jsonschema:"Return public reasoning; needs request_key."`
	RequestKey       string `json:"request_key,omitempty" jsonschema:"Key for include_reasoning."`
	Model            string `json:"model,omitempty" jsonschema:"Model family or alias."`
	Prompt           string `json:"prompt,omitempty" jsonschema:"At most 65536 bytes."`
	MaxOutput        int64  `json:"max_output,omitempty" jsonschema:"1 to 4096 tokens."`
	ReasoningEffort  string `json:"reasoning_effort,omitempty" jsonschema:"Default: the model's."`
	ReasoningBudget  int64  `json:"reasoning_budget,omitempty" jsonschema:"Thinking token cap."`
	Image            string `json:"image,omitempty" jsonschema:"OA image digest."`
	ImagePath        string `json:"image_path,omitempty" jsonschema:"Image file to show."`
	Hosting          string `json:"hosting,omitempty" jsonschema:"local or hosted."`
	Credential       string `json:"credential,omitempty" jsonschema:"OA credential name for hosted; never a secret."`
	Conversation     string `json:"conversation,omitempty" jsonschema:"Prompt-cache label; default this chat."`
	OperationID      string `json:"operation_id,omitempty" jsonschema:"Resume handle."`
	Cursor           *int64 `json:"cursor,omitempty" jsonschema:"Resume cursor."`
	WaitMS           int64  `json:"wait_ms,omitempty" jsonschema:"Default 20000."`
}

type CompleteOutput struct {
	Reasoning    []CompleteReasoning `json:"reasoning,omitempty"`
	Progress     *CompleteProgress   `json:"progress,omitempty"`
	RetryAfterMS *int64              `json:"retry_after_ms,omitempty"`
	ModelRefusal string              `json:"model_refusal,omitempty"`
	OperationID  string              `json:"operation_id,omitempty"`
	Cursor       int64               `json:"cursor"`
	State        string              `json:"state,omitempty"`
	Outcome      string              `json:"outcome"`
	Reason       string              `json:"reason,omitempty"`
	Text         string              `json:"text,omitempty"`
	Host         string              `json:"host,omitempty"`
	Model        string              `json:"model,omitempty"`
	StopReason   string              `json:"stop_reason,omitempty"`
	// ReasoningApplied is the reasoning the answer ran with, and
	// DroppedExtensions the request extensions the model does not take.
	ReasoningApplied *CompleteReasoningApplied `json:"reasoning_applied,omitempty"`
	// QuestionIDs names the first-use questions a refusal is waiting on
	// (inference INF-W11); Waiting is true while one is unanswered, and
	// Message is one sentence for the person.
	QuestionIDs []string `json:"question_ids,omitempty"`
	Waiting     bool     `json:"waiting,omitempty"`
	Message     string   `json:"message,omitempty"`
}

type CompleteReasoningApplied struct {
	Effort        string `json:"effort,omitempty"`
	Budget        int64  `json:"budget,omitempty"`
	Support       string `json:"support"`
	BudgetSupport string `json:"budget_support,omitempty"`
}

type CompleteReasoning struct {
	Block int64  `json:"block"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
}
type CompleteProgress struct {
	Phase string `json:"phase"`
	Done  *int64 `json:"done,omitempty"`
	Total *int64 `json:"total,omitempty"`
	Unit  string `json:"unit,omitempty"`
}

type JobSubmitInput struct {
	RequestKey string `json:"request_key" jsonschema:"Stable idempotency key, at most 128 characters."`
	Profile    string `json:"profile" jsonschema:"video"`
	Model      string `json:"model" jsonschema:"model family or alias"`
	Prompt     string `json:"prompt" jsonschema:"At most 65536 bytes."`
	Hosting    string `json:"hosting" jsonschema:"local or hosted."`
	Credential string `json:"credential,omitempty" jsonschema:"OA credential name for hosted; never a secret."`
	Size       string `json:"size,omitempty" jsonschema:"auto (default): the model's own size; or WIDTHxHEIGHT such as 512x512"`
	Count      int64  `json:"count,omitempty" jsonschema:"image count, 1 to 10"`
	DurationMS int64  `json:"duration_ms,omitempty" jsonschema:"1 to 600000 milliseconds."`
	// An edit's inputs, which only oa_image_generate sends: the job submit
	// schema leaves them out.
	InputImage     string `json:"input_image,omitempty"`
	InputMediaType string `json:"input_media_type,omitempty"`
	Mask           string `json:"mask,omitempty"`
	MaskMediaType  string `json:"mask_media_type,omitempty"`
	// WaitMS follows the accepted job as oa_inference_job_status does; it is
	// no part of the job's specification.
	WaitMS int64 `json:"wait_ms,omitempty" jsonschema:"Milliseconds to follow the accepted job, with progress, before answering; default 0 answers at once."`
}

type JobSubmitOutput struct {
	Handle      string   `json:"handle"`
	Outcome     string   `json:"outcome"`
	Reason      string   `json:"reason,omitempty"`
	OperationID string   `json:"operation_id,omitempty"`
	Guarantees  []string `json:"accepted_guarantees,omitempty"`
	// Status is the job's state when the call followed it with wait_ms.
	Status *JobStatusOutput `json:"status,omitempty"`
	// QuestionIDs, Waiting and Message are CompleteOutput's (inference INF-W11).
	QuestionIDs []string `json:"question_ids,omitempty"`
	Waiting     bool     `json:"waiting,omitempty"`
	Message     string   `json:"message,omitempty"`
}

type JobHandleInput struct {
	Handle string `json:"handle" jsonschema:"Handle from oa_video_generate or oa_inference_job_submit."`
}

// JobStatusInput is one oa_inference_job_status call: the handle, and how
// long to follow the job before answering.
type JobStatusInput struct {
	Handle string `json:"handle" jsonschema:"Handle from oa_video_generate or oa_inference_job_submit."`
	WaitMS int64  `json:"wait_ms,omitempty" jsonschema:"Milliseconds to wait for the job to end, with progress, before answering; default 0 answers at once."`
}

type JobStatusOutput struct {
	Outcome       string           `json:"outcome"`
	ResultOutcome string           `json:"result_outcome,omitempty"`
	State         string           `json:"state,omitempty"`
	Waiting       string           `json:"waiting,omitempty"`
	Progress      map[string]int64 `json:"progress,omitempty"`
	Failure       string           `json:"failure,omitempty"`
	Result        any              `json:"result,omitempty"`
	// Path is where a finished video was saved: the person's Videos
	// folder's OpenAbstractions folder. Message says why one was not.
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
	// Phase is one short line of what the job does now: starting the video
	// engine, loading the model (N GB), generating: step x of y, saving.
	Phase string `json:"phase,omitempty"`
}

// JobDiagnosisOutput is a bounded, handle-scoped explanation. It deliberately
// carries typed failure fields and guidance, never failure text or job content.
type JobDiagnosisOutput struct {
	Scope        string           `json:"scope"`
	Outcome      string           `json:"outcome"`
	State        string           `json:"state,omitempty"`
	Waiting      bool             `json:"waiting"`
	Progress     map[string]int64 `json:"progress,omitempty"`
	FailureClass string           `json:"failure_class,omitempty"`
	FailureCause string           `json:"failure_cause,omitempty"`
	// Reason is the runtime's refusal word for a submit it refused at once;
	// Guidance then is its plain sentence.
	Reason   string `json:"reason,omitempty"`
	Guidance string `json:"guidance"`
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
	Diagnose(context.Context, JobHandleInput) (JobDiagnosisOutput, error)
	Cancel(context.Context, JobHandleInput) (JobCancelOutput, error)
}
