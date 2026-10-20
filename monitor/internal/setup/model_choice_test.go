package setup

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// choiceTransferFake records the downloads a controller reserves.
type choiceTransferFake struct{ reserved []PinnedDownload }

func (f *choiceTransferFake) Reserve(_ context.Context, p PinnedDownload, key string) (WorkIdentity, error) {
	f.reserved = append(f.reserved, p)
	binding := client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"}
	return WorkIdentity{Key: key, HistoryEpoch: "epoch", Binding: binding, OperationsBinding: binding}, nil
}
func (f *choiceTransferFake) Submit(context.Context, PinnedDownload, WorkIdentity) error { return nil }
func (f *choiceTransferFake) Observe(context.Context, WorkIdentity) (TransferObservation, error) {
	return TransferObservation{State: "running"}, nil
}
func (f *choiceTransferFake) Copy(context.Context, WorkIdentity, io.Writer) (int64, error) {
	return 0, errors.New("unused")
}
func (f *choiceTransferFake) Link(context.Context, WorkIdentity, string) (jobapi.LinkOutcome, error) {
	return 0, errors.New("unused")
}

func choiceController(t *testing.T, plan ModelPlan, staged []string, transfer Transfer) ModelController {
	t.Helper()
	root := t.TempDir()
	return ModelController{Plan: &plan, JournalPath: filepath.Join(root, "model-choice.json"), StageDir: filepath.Join(root, "stage"),
		ProviderRoot: filepath.Join(root, "provider"), Transfer: transfer,
		FreeBytes:    func(string) (uint64, error) { return 1 << 62, nil },
		StagedModels: func(context.Context) ([]string, error) { return staged, nil }}
}

func approve(plan ModelPlan) ModelApproval {
	return ModelApproval{PlanID: plan.ID, DownloadBytes: plan.DownloadBytes, PeakBytes: plan.PeakBytes, Licenses: slices.Clone(plan.Licenses)}
}

// Each offered plan starts only its own pinned files: its approval is its
// own, another plan's approval is refused, and advancing every asset
// reserves exactly the plan's downloads.
func TestEachListedPlanStartsOnlyItsOwnPinnedFiles(t *testing.T) {
	ctx := context.Background()
	choices := PictureModelChoices()
	for i, plan := range choices {
		transfer := &choiceTransferFake{}
		c := choiceController(t, plan, nil, transfer)
		other := choices[(i+1)%len(choices)]
		for _, wrong := range []ModelApproval{approve(other), approve(ApprovedBF16Plan()), {PlanID: "unknown"}} {
			if _, err := c.Start(ctx, wrong); err == nil {
				t.Fatalf("%s accepted the approval of %s", plan.Model, wrong.PlanID)
			}
		}
		status, err := c.Start(ctx, approve(plan))
		if err != nil {
			t.Fatal(err)
		}
		if status.Plan.ID != plan.ID || len(status.Assets) != len(plan.Assets) {
			t.Fatalf("%s status %+v", plan.Model, status)
		}
		for _, a := range plan.Assets {
			if _, err := c.Advance(ctx, a.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := c.Advance(ctx, "backend"); err == nil {
			t.Fatalf("%s advanced the starter plan's backend", plan.Model)
		}
		var want []PinnedDownload
		for _, a := range plan.Assets {
			want = append(want, a.Download)
		}
		if !slices.Equal(transfer.reserved, want) {
			t.Fatalf("%s reserved %+v, want %+v", plan.Model, transfer.reserved, want)
		}
	}
}

// A file the provider already holds for a staged model is not downloaded
// again: Z-Image-Turbo's text encoder is the starter model's file, so with the
// starter model staged its asset starts verified and shared, and the
// provider's staging step receives no file for that role.
func TestASharedFileIsStagedOnce(t *testing.T) {
	ctx := context.Background()
	z := PictureModelChoices()[0]
	transfer := &choiceTransferFake{}
	c := choiceController(t, z, []string{BF16ModelID}, transfer)
	var staged struct {
		model string
		files map[string]string
	}
	c.StageModel = func(_ context.Context, root, model string, files map[string]string, _ func(int64, int64) error) error {
		staged.model, staged.files = model, maps.Clone(files)
		return nil
	}
	status, err := c.Start(ctx, approve(z))
	if err != nil {
		t.Fatal(err)
	}
	phases := map[string]Phase{}
	for _, a := range status.Assets {
		phases[a.ID] = a.Phase
	}
	if phases["encoder"] != Verified || phases["main"] != Pending || phases["vae"] != Pending {
		t.Fatalf("phases %v", phases)
	}
	if _, err := c.Advance(ctx, "encoder"); err != nil {
		t.Fatal(err)
	}
	if len(transfer.reserved) != 0 {
		t.Fatalf("the shared encoder was downloaded: %+v", transfer.reserved)
	}
	for _, id := range []string{"main", "vae"} {
		path := filepath.Join(c.StageDir, id)
		if err := os.MkdirAll(c.StageDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(id), 0600); err != nil {
			t.Fatal(err)
		}
		if err := c.change(ctx, id, ModelEffect{Phase: Pending}, ModelEffect{Phase: Verified, StagedPath: path}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := c.Configure(ctx, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ProviderConfigured || staged.model != "user.Z-Image-Turbo" || len(staged.files) != 2 || staged.files["main"] == "" || staged.files["vae"] == "" {
		t.Fatalf("staged %+v, status %+v", staged, after)
	}
	// Without the starter model staged, the same plan downloads the encoder.
	alone := choiceController(t, z, nil, &choiceTransferFake{})
	status, err = alone.Start(ctx, approve(z))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range status.Assets {
		if a.Phase != Pending {
			t.Fatalf("%s starts %s with nothing staged", a.ID, a.Phase)
		}
	}
}
