package setup

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"github.com/openabstractions/abstraction-facade/go/client"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// Transfer is the setup application's use of the public download/job API.
// Its implementation never reaches into the runtime's private work store.
type Transfer interface {
	Reserve(context.Context, PinnedDownload, string) (WorkIdentity, error)
	Submit(context.Context, PinnedDownload, WorkIdentity) error
	Observe(context.Context, WorkIdentity) (TransferObservation, error)
	Copy(context.Context, WorkIdentity, io.Writer) (int64, error)
}

// PinnedDownload is resolved from a compiled package catalog or a separately
// approved model manifest. Browser requests carry stable IDs, never this data.
type PinnedDownload struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Label  string `json:"label"`
}

func (a Artifact) DownloadPin() PinnedDownload {
	return PinnedDownload{URL: a.URL, SHA256: a.SHA256, Bytes: a.Bytes, Label: "Install " + a.InstallIdentity}
}

func (p PinnedDownload) validate() error {
	if p.Bytes <= 0 || len(p.SHA256) != 64 {
		return errors.New("download needs exact positive size and SHA-256")
	}
	if _, err := hex.DecodeString(p.SHA256); err != nil {
		return errors.New("download SHA-256 is not hex")
	}
	if p.Label == "" {
		return errors.New("download label is empty")
	}
	if _, err := api.NormalizeLabel(p.Label); err != nil {
		return err
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("download source must be a clean HTTPS URL")
	}
	switch u.Host {
	case "github.com":
		if !strings.Contains(u.Path, "/releases/download/") {
			return errors.New("GitHub download needs a release asset path")
		}
	case "huggingface.co":
		if !regexp.MustCompile(`/resolve/[0-9a-f]{40}/`).MatchString(u.Path) {
			return errors.New("Hugging Face download needs a full revision pin")
		}
	default:
		return errors.New("download source host is not approved")
	}
	return nil
}

type TransferObservation struct {
	State   string `json:"state"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Failure string `json:"failure,omitempty"`
}

type PublicTransfer struct{ Machine *client.Machine }

// discoverRuntime selects the installed runtime in production. The package's
// TestMain points it at an endpoint nobody serves, so no test reaches the
// runtime installed on the machine running the tests.
var discoverRuntime = client.Discover

func (t PublicTransfer) machine() *client.Machine {
	if t.Machine != nil {
		return t.Machine
	}
	return discoverRuntime()
}

// Reserve obtains and pins both public service bindings and the history epoch
// before the runner journals this identity and before any submission occurs.
func (t PublicTransfer) Reserve(ctx context.Context, p PinnedDownload, key string) (WorkIdentity, error) {
	if err := p.validate(); err != nil {
		return WorkIdentity{}, err
	}
	if key == "" {
		return WorkIdentity{}, errors.New("download request key is empty")
	}
	machine := t.machine()
	need := client.Requirements{Guarantees: api.AdmissionGuarantees}
	acceptance, err := machine.ResolveJobs(ctx, need)
	if err != nil {
		return WorkIdentity{}, err
	}
	window, err := acceptance.GetHistoryWindow(ctx)
	if err != nil {
		return WorkIdentity{}, err
	}
	operations, err := machine.ResolveJobOperations(ctx, need)
	if err != nil {
		return WorkIdentity{}, err
	}
	other, err := operations.GetHistoryWindow(ctx)
	if err != nil {
		return WorkIdentity{}, err
	}
	if window.HistoryEpoch != other.HistoryEpoch || window.LogicalOwner != other.LogicalOwner {
		return WorkIdentity{}, errors.New("download acceptance and operations resolve to different histories")
	}
	return WorkIdentity{Key: key, HistoryEpoch: window.HistoryEpoch,
		Binding: acceptance.Binding(), OperationsBinding: operations.Binding()}, nil
}

func (t PublicTransfer) Submit(ctx context.Context, p PinnedDownload, work WorkIdentity) error {
	if err := p.validate(); err != nil {
		return err
	}
	parsed, err := url.Parse(p.URL)
	if err != nil {
		return err
	}
	jobs, err := t.machine().RestoreJobs(ctx, work.Binding)
	if err != nil {
		return err
	}
	id := api.RequestIdentity{Key: work.Key, HistoryEpoch: work.HistoryEpoch, Attempt: work.Attempt}
	submission := api.Submission{Identity: id, Kind: download.Kind, RequiredGuarantees: api.AdmissionGuarantees,
		Label: p.Label,
		Spec: request.Encode(&request.Request{Artifact: request.Artifact{Digest: "sha256:" + p.SHA256, Size: p.Bytes},
			Sources: []request.Source{{Scheme: parsed.Scheme, Locator: p.URL}}})}
	result, err := jobs.Submit(ctx, submission)
	if err != nil {
		// A lost reply is not a failed submission. Reconcile the exact saved
		// identity; the caller keeps it in the journal if this remains unknown.
		result, err = jobs.Reconcile(ctx, id)
	}
	if err != nil {
		return fmt.Errorf("download acceptance remains uncertain: %w", err)
	}
	if result.Outcome != api.AcceptanceOutcomeAccepted {
		return fmt.Errorf("download acceptance %s: %s", result.Outcome, result.Reason)
	}
	return nil
}

func (t PublicTransfer) Observe(ctx context.Context, work WorkIdentity) (TransferObservation, error) {
	jobs, err := t.machine().RestoreJobs(ctx, work.OperationsBinding)
	if err != nil {
		return TransferObservation{}, err
	}
	result, err := jobs.ObserveWork(ctx, api.RequestIdentity{Key: work.Key, HistoryEpoch: work.HistoryEpoch, Attempt: work.Attempt})
	if err != nil {
		return TransferObservation{}, err
	}
	if result.Outcome != api.ObservationOutcomeObserved || result.Snapshot == nil {
		return TransferObservation{State: result.Outcome.String()}, nil
	}
	snapshot := result.Snapshot
	observation := TransferObservation{State: snapshot.State.String(), Done: snapshot.Progress.Done, Total: snapshot.Progress.Total}
	if snapshot.Failure != nil {
		observation.Failure = snapshot.Failure.Classification.String() + ": " + snapshot.Failure.Message
	}
	return observation, nil
}

// transferCanceller is the optional explicit cancellation of a public job.
type transferCanceller interface {
	Cancel(context.Context, WorkIdentity) error
}

// Cancel asks the public job service to cancel the exact saved request.
// Requested records intent; the job service owns what happens to the bytes.
func (t PublicTransfer) Cancel(ctx context.Context, work WorkIdentity) error {
	jobs, err := t.machine().RestoreJobs(ctx, work.Binding)
	if err != nil {
		return err
	}
	result, err := jobs.CancelWork(ctx, api.RequestIdentity{Key: work.Key, HistoryEpoch: work.HistoryEpoch, Attempt: work.Attempt})
	if err != nil {
		return err
	}
	switch result.Outcome {
	case api.CancellationOutcomeRequested, api.CancellationOutcomeAlreadyTerminal:
		return nil
	}
	return fmt.Errorf("download cancellation %s", result.Outcome)
}

// ResultLinker is a Transfer whose job service hard-links a completed result
// to a path on this machine (job CONTRACT.md JOB-A17). The model downloads
// use it, so the disk holds one file per download.
type ResultLinker interface {
	Link(ctx context.Context, work WorkIdentity, destination string) (api.LinkOutcome, error)
}

// Link asks the public job service to give the exact saved request's result
// a second name at destination. It never copies; a refusal is its outcome.
func (t PublicTransfer) Link(ctx context.Context, work WorkIdentity, destination string) (api.LinkOutcome, error) {
	jobs, err := t.machine().RestoreJobs(ctx, work.OperationsBinding)
	if err != nil {
		return 0, err
	}
	result, err := jobs.LinkResult(ctx, api.RequestIdentity{Key: work.Key, HistoryEpoch: work.HistoryEpoch, Attempt: work.Attempt}, destination)
	if err != nil {
		return 0, err
	}
	return result.Outcome, nil
}

func (t PublicTransfer) Copy(ctx context.Context, work WorkIdentity, dst io.Writer) (int64, error) {
	jobs, err := t.machine().RestoreJobs(ctx, work.OperationsBinding)
	if err != nil {
		return 0, err
	}
	return jobs.CopyResult(ctx, api.RequestIdentity{Key: work.Key, HistoryEpoch: work.HistoryEpoch, Attempt: work.Attempt}, dst)
}
