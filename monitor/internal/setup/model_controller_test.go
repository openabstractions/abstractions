package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

type modelTransferFake struct {
	journal   string
	reserved  bool
	submitted bool
	payload   []byte
	malformed bool
	observed  string
}

func (f *modelTransferFake) Reserve(_ context.Context, p PinnedDownload, key string) (WorkIdentity, error) {
	if p.URL == "" || key == "" {
		return WorkIdentity{}, errors.New("missing pin")
	}
	f.reserved = true
	if f.malformed {
		return WorkIdentity{Key: key, HistoryEpoch: "epoch"}, nil
	}
	binding := client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"}
	return WorkIdentity{Key: key, HistoryEpoch: "epoch", Binding: binding, OperationsBinding: binding}, nil
}
func (f *modelTransferFake) Submit(ctx context.Context, _ PinnedDownload, work WorkIdentity) error {
	state, err := (ModelController{JournalPath: f.journal}).load(ctx)
	if err != nil {
		return err
	}
	if state.Effects["main"].Work == nil || state.Effects["main"].Work.Key != work.Key {
		return errors.New("submission preceded saved identity")
	}
	f.submitted = true
	return nil
}
func (f *modelTransferFake) Observe(context.Context, WorkIdentity) (TransferObservation, error) {
	state := f.observed
	if state == "" {
		state = "running"
	}
	return TransferObservation{State: state, Done: 2, Total: 10}, nil
}
func (f *modelTransferFake) Copy(_ context.Context, _ WorkIdentity, dst io.Writer) (int64, error) {
	n, err := dst.Write(f.payload)
	return int64(n), err
}

// Link stands in for the job service (JOB-A17): it keeps the payload as its
// own result file beside the destination and hard-links it there.
func (f *modelTransferFake) Link(_ context.Context, _ WorkIdentity, destination string) (jobapi.LinkOutcome, error) {
	store := destination + ".job-result"
	if err := os.WriteFile(store, f.payload, 0600); err != nil {
		return 0, err
	}
	if err := os.Link(store, destination); err != nil {
		return 0, err
	}
	return jobapi.LinkOutcomeLinked, nil
}

func TestApprovedBF16PlanAndConsent(t *testing.T) {
	p := ApprovedBF16Plan()
	if len(p.Assets) != 4 || p.DownloadBytes != 16171164282 || p.PeakBytes != p.DownloadBytes+backendExpandedBytes+bf16DiskHeadroom || len(p.Licenses) != 2 || p.Licenses[0] != "Apache-2.0" || p.Licenses[1] != "MIT" {
		t.Fatalf("unexpected fixed plan: %+v", p)
	}
	for _, a := range p.Assets {
		if err := a.Download.validate(); err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
	}
	root := t.TempDir()
	f := &modelTransferFake{journal: filepath.Join(root, "model.json")}
	c := ModelController{JournalPath: f.journal, StageDir: filepath.Join(root, "stage"), ProviderRoot: filepath.Join(root, "provider"), Transfer: f, FreeBytes: func(string) (uint64, error) { return uint64(p.PeakBytes) - 1, nil }}
	disk, err := c.DiskPreflight()
	if err != nil {
		t.Fatal(err)
	}
	if disk.Enough || disk.RequiredPeakBytes != p.PeakBytes || disk.StageAvailableBytes != uint64(p.PeakBytes)-1 || disk.ProviderRequiredBytes != p.DownloadBytes+backendExpandedBytes {
		t.Fatalf("unexpected read-only disk preflight: %+v", disk)
	}
	approval := ModelApproval{PlanID: p.ID, DownloadBytes: p.DownloadBytes, PeakBytes: p.PeakBytes, Licenses: p.Licenses}
	if _, err := c.Start(context.Background(), approval); err == nil {
		t.Fatal("insufficient disk accepted")
	}
	if _, err := os.Stat(f.journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disk refusal created approval journal")
	}
	c.FreeBytes = func(string) (uint64, error) { return uint64(p.PeakBytes), nil }
	bad := approval
	bad.Licenses = []string{"Apache-2.0", "other"}
	if _, err := c.Start(context.Background(), bad); err == nil {
		t.Fatal("different license accepted")
	}
	if _, err := c.Start(context.Background(), approval); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Advance(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	if !f.reserved || !f.submitted {
		t.Fatal("public transfer was not used")
	}
	state, err := c.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Effects["main"].Phase != Requested || state.Effects["main"].Work == nil {
		t.Fatal("identity not retained")
	}
	if _, err := c.Advance(context.Background(), "arbitrary"); err == nil {
		t.Fatal("arbitrary model asset accepted")
	}
}

func TestModelRejectsIncompletePublicIdentityBeforeSubmission(t *testing.T) {
	root := t.TempDir()
	p := ApprovedBF16Plan()
	f := &modelTransferFake{journal: filepath.Join(root, "model.json"), malformed: true}
	c := ModelController{JournalPath: f.journal, StageDir: root, ProviderRoot: filepath.Join(root, "provider"), Transfer: f, FreeBytes: func(string) (uint64, error) { return uint64(p.PeakBytes), nil }}
	ctx := context.Background()
	if _, err := c.Start(ctx, ModelApproval{PlanID: p.ID, DownloadBytes: p.DownloadBytes, PeakBytes: p.PeakBytes, Licenses: p.Licenses}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Advance(ctx, "main"); err == nil {
		t.Fatal("incomplete public identity accepted")
	}
	if f.submitted {
		t.Fatal("submitted with incomplete public identity")
	}
	state, err := c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Effects["main"].Phase != Pending {
		t.Fatal("malformed identity persisted")
	}
}

func TestModelStatusJSONKeepsInternalWorkAndStagePrivate(t *testing.T) {
	root := t.TempDir()
	p := ApprovedBF16Plan()
	f := &modelTransferFake{journal: filepath.Join(root, "model.json"), observed: "running"}
	c := ModelController{JournalPath: f.journal, StageDir: filepath.Join(root, "stage"), ProviderRoot: filepath.Join(root, "provider"), Transfer: f,
		FreeBytes: func(string) (uint64, error) { return uint64(p.PeakBytes), nil }}
	ctx := context.Background()
	if _, err := c.Start(ctx, ModelApproval{PlanID: p.ID, DownloadBytes: p.DownloadBytes, PeakBytes: p.PeakBytes, Licenses: p.Licenses}); err != nil {
		t.Fatal(err)
	}
	const secretEndpoint = "secret-internal-job-endpoint"
	const secretStage = "secret-internal-staged-path"
	binding := client.JobsBinding{Endpoint: secretEndpoint, LogicalOwner: "owner"}
	work := &WorkIdentity{Key: "secret-work-key", HistoryEpoch: "epoch", Binding: binding, OperationsBinding: binding}
	if err := c.change(ctx, "main", ModelEffect{Phase: Pending}, ModelEffect{Phase: Requested, Work: work}); err != nil {
		t.Fatal(err)
	}
	if err := c.change(ctx, "encoder", ModelEffect{Phase: Pending}, ModelEffect{Phase: Verified, StagedPath: secretStage}); err != nil {
		t.Fatal(err)
	}
	status, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretEndpoint, secretStage, "secret-work-key", "history_epoch", "staged_path"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("status JSON leaked internal %q", secret)
		}
	}
	if !strings.Contains(string(raw), `"state":"running"`) || !strings.Contains(string(raw), `"done":2`) || !strings.Contains(string(raw), `"total":10`) {
		t.Fatalf("status JSON lost public progress: %s", raw)
	}
}

func TestModelRetryRequiresConfirmedTerminalPublicAttempt(t *testing.T) {
	root := t.TempDir()
	p := ApprovedBF16Plan()
	ctx := context.Background()
	f := &modelTransferFake{journal: filepath.Join(root, "model.json")}
	c := ModelController{JournalPath: f.journal, StageDir: root, ProviderRoot: filepath.Join(root, "provider"), Transfer: f, FreeBytes: func(string) (uint64, error) { return uint64(p.PeakBytes), nil }}
	if _, err := c.Start(ctx, ModelApproval{PlanID: p.ID, DownloadBytes: p.DownloadBytes, PeakBytes: p.PeakBytes, Licenses: p.Licenses}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Advance(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	state, err := c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prior := state.Effects["main"]
	if err := c.change(ctx, "main", prior, ModelEffect{Phase: Failed, Work: prior.Work}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Retry(ctx, "main"); err == nil {
		t.Fatal("retried unknown-running public work")
	}
	f.observed = "failed"
	if _, err := c.Retry(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	state, err = c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Effects["main"].Phase != Requested || state.Effects["main"].Work.Attempt != 1 {
		t.Fatal("retry did not save next exact attempt")
	}
}

func TestModelStageRejectsWrongBytes(t *testing.T) {
	root := t.TempDir()
	payload := []byte("small verified model fixture")
	sum := sha256.Sum256(payload)
	asset := ModelAsset{ID: "fixture", Download: PinnedDownload{Bytes: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])}}
	f := &modelTransferFake{payload: payload}
	c := ModelController{StageDir: root, Transfer: f}
	path, err := c.stage(context.Background(), asset, WorkIdentity{Key: "fixture"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkModelStage(context.Background(), path, asset.Download); err != nil {
		t.Fatal(err)
	}
	archive := asset
	archive.ID = "backend"
	archive.Path = "backend.zip"
	archivePath, err := c.stage(context.Background(), archive, WorkIdentity{Key: "fixture"}, nil)
	if err != nil || filepath.Ext(archivePath) != ".zip" {
		t.Fatalf("reviewed backend stage failed: %q %v", archivePath, err)
	}
	f.payload = []byte("wrong")
	if _, err := c.stage(context.Background(), ModelAsset{ID: "different", Download: PinnedDownload{Bytes: 5, SHA256: asset.Download.SHA256}}, WorkIdentity{Key: "fixture"}, nil); err == nil {
		t.Fatal("wrong bytes accepted")
	}
}

func TestModelReadyRequiresProviderAndPublicSelection(t *testing.T) {
	root := t.TempDir()
	plan := ApprovedBF16Plan()
	selected := false
	stageCalls := 0
	c := ModelController{
		JournalPath: filepath.Join(root, "model.json"), StageDir: filepath.Join(root, "stage"), ProviderRoot: filepath.Join(root, "provider"),
		FreeBytes: func(string) (uint64, error) { return uint64(plan.PeakBytes), nil },
		Selected:  func(context.Context) (bool, error) { return selected, nil },
		Stage: func(_ context.Context, _ string, files BF16StageFiles, _ func(int64, int64) error, replace bool) error {
			stageCalls++
			if files.Main == "" || files.Encoder == "" || files.VAE == "" || files.BackendArchive == "" || replace {
				return errors.New("wrong provider inputs")
			}
			return nil
		},
	}
	ctx := context.Background()
	if _, err := c.Start(ctx, ModelApproval{PlanID: plan.ID, DownloadBytes: plan.DownloadBytes, PeakBytes: plan.PeakBytes, Licenses: plan.Licenses}); err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Assets {
		path := filepath.Join(root, a.ID)
		if err := os.WriteFile(path, []byte(a.ID), 0600); err != nil {
			t.Fatal(err)
		}
		if err := c.change(ctx, a.ID, ModelEffect{Phase: Pending}, ModelEffect{Phase: Verified, StagedPath: path}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Ready {
		t.Fatal("ready before provider configuration")
	}
	after, err := c.Configure(ctx, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stageCalls != 1 || !after.ProviderConfigured || after.Ready {
		t.Fatal("provider-only configuration reported ready")
	}
	selected = true
	after, err = c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Ready {
		t.Fatal("verified provider and public selection did not report ready")
	}
}

func TestModelConfigureAmbiguousOutcomeNeedsExactProbe(t *testing.T) {
	root := t.TempDir()
	p := ApprovedBF16Plan()
	ctx := context.Background()
	c := ModelController{JournalPath: filepath.Join(root, "model.json"), StageDir: filepath.Join(root, "stage"), ProviderRoot: filepath.Join(root, "provider"), FreeBytes: func(string) (uint64, error) { return uint64(p.PeakBytes), nil }}
	if _, err := c.Start(ctx, ModelApproval{PlanID: p.ID, DownloadBytes: p.DownloadBytes, PeakBytes: p.PeakBytes, Licenses: p.Licenses}); err != nil {
		t.Fatal(err)
	}
	for _, a := range p.Assets {
		path := filepath.Join(root, a.ID)
		if err := os.WriteFile(path, []byte(a.ID), 0600); err != nil {
			t.Fatal(err)
		}
		if err := c.change(ctx, a.ID, ModelEffect{Phase: Pending}, ModelEffect{Phase: Verified, StagedPath: path}); err != nil {
			t.Fatal(err)
		}
	}
	c.Stage = func(context.Context, string, BF16StageFiles, func(int64, int64) error, bool) error {
		return errors.New("reply lost")
	}
	if _, err := c.Configure(ctx, false, nil); err == nil {
		t.Fatal("ambiguous stage reported success")
	}
	state, err := c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.ProviderConfiguring || state.ProviderConfigured {
		t.Fatal("ambiguous stage was not retained")
	}
	c.Probe = func(context.Context, string) (bool, error) { return false, errors.New("partial private config") }
	if _, err := c.ReconcileProvider(ctx); err == nil {
		t.Fatal("partial private state treated as absent")
	}
	state, err = c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.ProviderConfiguring {
		t.Fatal("partial private state lost uncertainty")
	}
	c.Probe = func(context.Context, string) (bool, error) { return true, nil }
	status, err := c.ReconcileProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ProviderConfigured || status.Ready {
		t.Fatal("probe failed to reconcile provider independently of selection")
	}
}

func TestModelReconcileWaitsForActivePrivateStage(t *testing.T) {
	root := t.TempDir()
	p := ApprovedBF16Plan()
	ctx := context.Background()
	c := ModelController{JournalPath: filepath.Join(root, "model.json"), StageDir: filepath.Join(root, "stage"), ProviderRoot: filepath.Join(root, "provider"), FreeBytes: func(string) (uint64, error) { return uint64(p.PeakBytes), nil }}
	if _, err := c.Start(ctx, ModelApproval{PlanID: p.ID, DownloadBytes: p.DownloadBytes, PeakBytes: p.PeakBytes, Licenses: p.Licenses}); err != nil {
		t.Fatal(err)
	}
	for _, a := range p.Assets {
		path := filepath.Join(root, a.ID)
		if err := os.WriteFile(path, []byte(a.ID), 0600); err != nil {
			t.Fatal(err)
		}
		if err := c.change(ctx, a.ID, ModelEffect{Phase: Pending}, ModelEffect{Phase: Verified, StagedPath: path}); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	c.Stage = func(context.Context, string, BF16StageFiles, func(int64, int64) error, bool) error {
		close(entered)
		<-release
		return nil
	}
	c.Probe = func(context.Context, string) (bool, error) { return false, nil }
	go func() { _, err := c.Configure(ctx, false, nil); done <- err }()
	<-entered
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := c.ReconcileProvider(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reconcile entered active stage: %v", err)
	}
	state, err := c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.ProviderConfiguring {
		t.Fatal("active stage marker was cleared")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
