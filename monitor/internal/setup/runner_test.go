package setup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
)

type fakeTransfer struct {
	reserve         int
	submit          int
	copy            int
	observe         int
	failFirstSubmit bool
	state           string
	bytes           []byte
	last            WorkIdentity
}

func (f *fakeTransfer) Reserve(_ context.Context, _ PinnedDownload, key string) (WorkIdentity, error) {
	f.reserve++
	f.last = WorkIdentity{Key: key, HistoryEpoch: "epoch", Binding: client.JobsBinding{Endpoint: "accept", LogicalOwner: "owner"}, OperationsBinding: client.JobsBinding{Endpoint: "operations", LogicalOwner: "owner"}}
	return f.last, nil
}
func (f *fakeTransfer) Submit(_ context.Context, _ PinnedDownload, work WorkIdentity) error {
	f.submit++
	if work.Key != f.last.Key || work.HistoryEpoch != f.last.HistoryEpoch {
		return errors.New("submitted a different public job")
	}
	if f.failFirstSubmit && f.submit == 1 {
		return errors.New("reply lost after submit")
	}
	return nil
}
func (f *fakeTransfer) Observe(_ context.Context, _ WorkIdentity) (TransferObservation, error) {
	f.observe++
	return TransferObservation{State: f.state, Done: int64(len(f.bytes)), Total: int64(len(f.bytes))}, nil
}

func TestPriorCatalogKeepsRequestedWorkPausedAndBlocksPlanReplacement(t *testing.T) {
	ctx := context.Background()
	oldCatalog := fixtureCatalog()
	oldPlan := mustPlan(t, oldCatalog, Lemonade)
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &oldCatalog}
	state, err := journal.SavePlan(ctx, oldPlan)
	if err != nil {
		t.Fatal(err)
	}
	work := &WorkIdentity{Key: "old-request", HistoryEpoch: "old-epoch",
		Binding:           client.JobsBinding{Endpoint: "old-accept", LogicalOwner: "owner"},
		OperationsBinding: client.JobsBinding{Endpoint: "old-operations", LogicalOwner: "owner"}}
	if _, err := journal.Update(ctx, oldPlan.ID, "lemonade-engine", state.Effects["lemonade-engine"], Effect{Phase: Requested, Work: work}); err != nil {
		t.Fatal(err)
	}
	oldPin := *oldPlan.Components[0].Artifact
	newCatalog := fixtureCatalog()
	newCatalog.PreviousInstallPins = []KnownInstallPin{{ComponentID: "lemonade-engine", Artifact: oldPin}}
	for i := range newCatalog.Components {
		if newCatalog.Components[i].ID == "lemonade-engine" {
			pin := *newCatalog.Components[i].Artifact
			pin.Version = "1.2.4"
			pin.SHA256 = strings.Repeat("c", 64)
			newCatalog.Components[i].Artifact = &pin
		}
	}
	journal.catalogOverride = &newCatalog
	transfer := &fakeTransfer{state: "complete"}
	runner := NewRunner(journal, transfer)
	status, err := runner.Status(ctx)
	if err != nil || status.CurrentCatalog || status.Plan.Preparable ||
		status.Components[0].Effect.Phase != Requested || status.Components[0].Transfer != nil ||
		transfer.observe != 0 {
		t.Fatalf("old requested work was not paused: %+v, %+v, %v", status, transfer, err)
	}
	if _, err := runner.Advance(ctx, "lemonade-engine"); err == nil || transfer.submit != 0 {
		t.Fatal("old requested download resumed under a new catalog")
	}
	if _, err := journal.SavePlan(ctx, mustPlan(t, newCatalog, Lemonade)); err == nil {
		t.Fatal("new plan replaced an active old public job")
	}
}
func (f *fakeTransfer) Copy(_ context.Context, _ WorkIdentity, dst io.Writer) (int64, error) {
	f.copy++
	n, err := io.Copy(dst, bytes.NewReader(f.bytes))
	return n, err
}

func TestTransferResumesExactPublicJobAfterLostReply(t *testing.T) {
	ctx := context.Background()
	data := []byte("reviewed portable ZIP bytes")
	sum := sha256.Sum256(data)
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID != "lemonade-engine" {
			continue
		}
		c.Components[i].Artifact.SHA256 = hex.EncodeToString(sum[:])
		c.Components[i].Artifact.Bytes = int64(len(data))
		c.Components[i].Artifact.PeakBytes = 2 * int64(len(data))
	}
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	f := &fakeTransfer{failFirstSubmit: true, state: "running", bytes: data}
	r := NewRunner(j, f)
	status, err := r.Start(ctx, []AppID{Lemonade})
	if err != nil || status.Ready || !status.Plan.Preparable {
		t.Fatalf("bad start: %+v, %v", status, err)
	}
	status, err = r.Advance(ctx, "lemonade-engine")
	if err == nil || status.Components[0].Effect.Phase != Requested {
		t.Fatalf("lost reply did not retain exact job: %+v, %v", status, err)
	}
	status, err = r.Advance(ctx, "lemonade-engine")
	if err != nil || f.reserve != 1 || f.submit != 2 || status.Components[0].Effect.Phase != Requested {
		t.Fatalf("resume created another job: %+v, %v, reserve=%d submit=%d", status, err, f.reserve, f.submit)
	}
	f.state = "complete"
	status, err = r.Advance(ctx, "lemonade-engine")
	if err != nil || status.Components[0].Effect.Phase != Verified || f.copy != 1 {
		t.Fatalf("completed public result not staged: %+v, %v", status, err)
	}
	if err := verifyFile(status.Components[0].Effect.StagedPath, *status.Plan.Components[0].Artifact); err != nil {
		t.Fatal(err)
	}
	if status.Ready {
		t.Fatal("downloaded bytes were mistaken for installed app")
	}
}

func TestLemonadeFeatureGatePreventsTransferBeforeCoreProvider(t *testing.T) {
	ctx := context.Background()
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID == "lemonade-engine" {
			c.Components[i].Artifact.InstallIdentity = "lemonade"
		}
	}
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	f := &fakeTransfer{}
	r := NewRunner(j, f)
	r.featureCheck = func() (FeatureStatus, error) {
		return FeatureStatus{Available: true, Reason: "Add the optional Lemonade Image feature"}, nil
	}
	if _, err := r.Start(ctx, []AppID{Lemonade}); err != nil {
		t.Fatal(err)
	}
	status, err := r.Advance(ctx, "lemonade-engine")
	if err == nil || status.Components[0].Effect.Phase != Pending || f.reserve != 0 || f.submit != 0 {
		t.Fatalf("external transfer began before core feature: %+v, %v, reserve=%d submit=%d", status, err, f.reserve, f.submit)
	}
}

func TestRetryRequiresObservedTerminalFailure(t *testing.T) {
	ctx := context.Background()
	c := fixtureCatalog()
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	f := &fakeTransfer{state: "failed"}
	r := NewRunner(j, f)
	if _, err := r.Start(ctx, []AppID{Lemonade}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(ctx, "lemonade-engine"); err != nil {
		t.Fatal(err)
	}
	status, err := r.Advance(ctx, "lemonade-engine")
	if err != nil || status.Components[0].Effect.Phase != Failed {
		t.Fatalf("failed transfer not retained: %+v, %v", status, err)
	}
	f.state = "unknown"
	if _, err := r.Retry(ctx, "lemonade-engine"); err == nil {
		t.Fatal("retry proceeded without confirmed terminal failure")
	}
	f.state = "failed"
	status, err = r.Retry(ctx, "lemonade-engine")
	if err != nil || status.Components[0].Effect.Work.Attempt != 1 || f.reserve != 1 {
		t.Fatalf("explicit attempt retry failed: %+v, %v", status, err)
	}
}

func TestPinnedModelDownloadNeedsExactRevisionAndNoBrowserHost(t *testing.T) {
	pin := PinnedDownload{URL: "https://huggingface.co/Comfy-Org/example/resolve/" + strings.Repeat("a", 40) + "/model.safetensors",
		SHA256: strings.Repeat("b", 64), Bytes: 100, Label: "Image model"}
	if err := pin.validate(); err != nil {
		t.Fatal(err)
	}
	pin.URL = "https://huggingface.co/Comfy-Org/example/resolve/main/model.safetensors"
	if err := pin.validate(); err == nil {
		t.Fatal("moving model revision accepted")
	}
	pin.URL = "https://github.com:444/openabstractions/x/releases/download/v1/a.zip"
	if err := pin.validate(); err == nil {
		t.Fatal("source host with an unexpected port accepted")
	}
}

func TestRetryRefusesUnpreparableChangedPlan(t *testing.T) {
	ctx := context.Background()
	c := fixtureCatalog()
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	f := &fakeTransfer{state: "failed"}
	r := NewRunner(j, f)
	if _, err := r.Start(ctx, []AppID{Lemonade}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(ctx, "lemonade-engine"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(ctx, "lemonade-engine"); err != nil {
		t.Fatal(err)
	}
	changed := fixtureCatalog()
	changed.Components[0].Artifact = nil
	changed.Components[0].Pending = "Comfy build not reviewed"
	j.catalogOverride = &changed
	r.Journal = j
	newPlan := mustPlan(t, changed, ComfyUI)
	if _, err := j.SavePlan(ctx, newPlan); err != nil {
		t.Fatal(err)
	}
	before := f.submit
	if _, err := r.Retry(ctx, "lemonade-engine"); err == nil || f.submit != before {
		t.Fatalf("retry submitted through unreviewed plan: %v, submits %d -> %d", err, before, f.submit)
	}
}

func TestStageRejectsOversizeBeforeWritingExcess(t *testing.T) {
	ctx := context.Background()
	c := fixtureCatalog()
	a := c.Components[1].Artifact // lemonade-engine
	a.Bytes = 3
	a.PeakBytes = 6
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	f := &fakeTransfer{state: "complete", bytes: []byte("too much")}
	r := NewRunner(j, f)
	if _, err := r.Start(ctx, []AppID{Lemonade}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(ctx, "lemonade-engine"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(ctx, "lemonade-engine"); err == nil {
		t.Fatal("oversized service result was staged")
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(j.Path), "downloads"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversized result left staged bytes: %v, %v", entries, err)
	}
}

func TestPortablePackageCompletesThroughPublicTransferAndArchive(t *testing.T) {
	standInHome(t)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("lemonade/lemond.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("fixed executable bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	sum := sha256.Sum256(data)
	c := fixtureCatalog()
	a := c.Components[1].Artifact // lemonade-engine
	a.SHA256 = hex.EncodeToString(sum[:])
	a.Bytes = int64(len(data))
	a.PeakBytes = 1024
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	f := &fakeTransfer{state: "complete", bytes: data}
	r := NewRunner(j, f)
	// A first Lemonade installation passes the provider's package check; a
	// first installation moves no provider binding.
	var checked []string
	r.healthCheck = func(_ context.Context, componentID string, _ Artifact, root string) error {
		checked = append(checked, componentID+" "+root)
		return nil
	}
	ctx := context.Background()
	if _, err := r.Start(ctx, []AppID{Lemonade}); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 5; step++ {
		if _, err := r.Advance(ctx, "lemonade-engine"); err != nil {
			t.Fatal(err)
		}
	}
	if len(checked) != 1 {
		t.Fatalf("the first Lemonade installation ran %d health checks", len(checked))
	}
	status, err := r.Status(ctx)
	if err != nil || status.Components[0].Effect.Phase != Installed || status.Ready {
		t.Fatalf("portable package did not reach confirmed installed state: %+v, %v", status, err)
	}
	destination, err := installPath(*a)
	if err != nil {
		t.Fatal(err)
	}
	if installed, err := ArchiveInstalled(*a, destination); err != nil || !installed {
		t.Fatalf("installed identity not observed: %t, %v", installed, err)
	}
	if f.reserve != 1 || f.copy != 1 {
		t.Fatalf("unexpected public transfer count: reserve=%d copy=%d", f.reserve, f.copy)
	}
	state, err := j.ConfirmConfiguration(ctx, status.Plan.ID, "lemonade-model") // model controller confirms its separate pinned work
	if err != nil || !state.Ready() {
		t.Fatalf("installed engine plus confirmed model not ready: %+v, %v", state, err)
	}
}
