package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// downloadTransferFake stands in for the job service: each completed
// download is its own result file in store, which Link hard-links to the
// destination (JOB-A17). Copy is the stream a model download must not use.
type downloadTransferFake struct {
	store  string
	bytes  map[string][]byte // by SHA-256
	keys   map[string]string // work key to SHA-256
	link   jobapi.LinkOutcome
	copies int
}

func newDownloadTransferFake(store string) *downloadTransferFake {
	return &downloadTransferFake{store: store, bytes: map[string][]byte{}, keys: map[string]string{}, link: jobapi.LinkOutcomeLinked}
}

func (f *downloadTransferFake) Reserve(_ context.Context, p PinnedDownload, key string) (WorkIdentity, error) {
	f.keys[key] = p.SHA256
	binding := client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"}
	return WorkIdentity{Key: key, HistoryEpoch: "epoch", Binding: binding, OperationsBinding: binding}, nil
}
func (f *downloadTransferFake) Submit(context.Context, PinnedDownload, WorkIdentity) error {
	return nil
}
func (f *downloadTransferFake) Observe(_ context.Context, work WorkIdentity) (TransferObservation, error) {
	// The finished download is the service's own result file.
	result := filepath.Join(f.store, work.Key)
	if _, err := os.Lstat(result); os.IsNotExist(err) {
		if err := os.MkdirAll(f.store, 0700); err != nil {
			return TransferObservation{}, err
		}
		if err := os.WriteFile(result, f.bytes[f.keys[work.Key]], 0600); err != nil {
			return TransferObservation{}, err
		}
	}
	return TransferObservation{State: "complete"}, nil
}
func (f *downloadTransferFake) Copy(_ context.Context, work WorkIdentity, w io.Writer) (int64, error) {
	f.copies++
	n, err := w.Write(f.bytes[f.keys[work.Key]])
	return int64(n), err
}
func (f *downloadTransferFake) Link(_ context.Context, work WorkIdentity, destination string) (jobapi.LinkOutcome, error) {
	if f.link != jobapi.LinkOutcomeLinked {
		return f.link, nil
	}
	return jobapi.LinkOutcomeLinked, os.Link(filepath.Join(f.store, work.Key), destination)
}

// pictureFixturePlan is an offered plan of two small pinned assets.
func pictureFixturePlan(transfer *downloadTransferFake) ModelPlan {
	plan := ModelPlan{Model: "user.Z-Image-Turbo", Licenses: []string{bf16License}}
	for _, id := range []string{"main", "vae"} {
		data := []byte("picture model " + id)
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		transfer.bytes[digest] = data
		plan.Assets = append(plan.Assets, ModelAsset{ID: id, Path: id + ".safetensors", License: bf16License,
			Download: PinnedDownload{URL: "https://huggingface.co/x/" + id, SHA256: digest, Bytes: int64(len(data))}})
		plan.DownloadBytes += int64(len(data))
	}
	plan.ID, plan.PeakBytes = "fixture", plan.DownloadBytes
	return plan
}

// The job service hard-links each finished download into the provider's
// download folder and the provider publishes it by rename: from the
// service's result to the provider's model file it is one file, no copy
// call happens, and the service keeps its own name (VISION 2026-10-01,
// JOB-A17).
func TestEachDownloadIsOneFileFromTransferToProvider(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	provider := filepath.Join(root, "provider")
	transfer := newDownloadTransferFake(filepath.Join(root, "job-results"))
	plan := pictureFixturePlan(transfer)
	identities := map[string]os.FileInfo{}
	c := ModelController{Plan: &plan, JournalPath: filepath.Join(root, "journal", "model.json"),
		StageDir: filepath.Join(provider, "engine-downloads"), ProviderRoot: provider, Transfer: transfer,
		FreeBytes: func(string) (uint64, error) { return 1 << 62, nil },
		StageModel: func(_ context.Context, _ string, _ string, files map[string]string, _ func(int64, int64) error) error {
			for role, path := range files {
				dest := filepath.Join(provider, "engine-models", role)
				if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
					return err
				}
				info, err := os.Stat(path)
				if err != nil || !os.SameFile(info, info) {
					return errors.New("the download is missing at configure")
				}
				identities[role] = info
				if err := os.Rename(path, dest); err != nil {
					return err
				}
			}
			return nil
		}}
	if _, err := c.Start(ctx, approve(plan)); err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Assets {
		for range 2 {
			if _, err := c.Advance(ctx, a.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	status, err := c.Configure(ctx, false, nil)
	if err != nil || !status.ProviderConfigured {
		t.Fatalf("configure %+v %v", status, err)
	}
	if transfer.copies != 0 {
		t.Fatalf("a model download was streamed %d times", transfer.copies)
	}
	for _, a := range plan.Assets {
		published, err := os.Stat(filepath.Join(provider, "engine-models", a.ID))
		if err != nil || !os.SameFile(identities[a.ID], published) {
			t.Fatalf("the provider's %s is not the download: %v", a.ID, err)
		}
	}
	// Every name under the test root holding a download's bytes is one
	// file: the service's result and the provider's model file.
	files := map[string][]os.FileInfo{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil || !os.SameFile(info, info) {
			return err
		}
		for _, want := range transfer.bytes {
			if bytes.Equal(data, want) {
				files[string(want)] = append(files[string(want)], info)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range transfer.bytes {
		names := files[string(data)]
		if len(names) != 2 || !os.SameFile(names[0], names[1]) {
			t.Fatalf("%q is on disk as %d names, not one file under two", data, len(names))
		}
	}
}

// JOB-A17 from Setup's side: a link the service refuses is a refusal with
// what to do, and Setup never falls back to streaming the bytes; a remote
// download service keeps streaming.
func TestARefusedLinkIsNeverReplacedByACopy(t *testing.T) {
	ctx := context.Background()
	for outcome, says := range map[jobapi.LinkOutcome]string{
		jobapi.LinkOutcomeOtherVolume: "one NTFS volume", jobapi.LinkOutcomeNoHardLinks: "NTFS volume",
		jobapi.LinkOutcomeForbidden: "refused", jobapi.LinkOutcomeUnavailable: "did not link",
	} {
		root := t.TempDir()
		transfer := newDownloadTransferFake(filepath.Join(root, "job-results"))
		transfer.link = outcome
		plan := pictureFixturePlan(transfer)
		c := ModelController{Plan: &plan, StageDir: filepath.Join(root, "engine-downloads"), Transfer: transfer}
		a := plan.Assets[0]
		work, _ := transfer.Reserve(ctx, a.Download, "main-key")
		if _, err := transfer.Observe(ctx, work); err != nil {
			t.Fatal(err)
		}
		_, err := c.stage(ctx, a, work, nil)
		if err == nil || !strings.Contains(err.Error(), says) || transfer.copies != 0 {
			t.Fatalf("%s: %v, %d copies", outcome, err, transfer.copies)
		}
		if entries, _ := os.ReadDir(c.StageDir); len(entries) != 0 {
			t.Fatalf("%s left %d files in the download folder", outcome, len(entries))
		}
	}
	root := t.TempDir()
	transfer := newDownloadTransferFake(filepath.Join(root, "job-results"))
	transfer.link = jobapi.LinkOutcomeRemote
	plan := pictureFixturePlan(transfer)
	c := ModelController{Plan: &plan, StageDir: filepath.Join(root, "engine-downloads"), Transfer: transfer}
	work, _ := transfer.Reserve(ctx, plan.Assets[0].Download, "remote-key")
	if path, err := c.stage(ctx, plan.Assets[0], work, nil); err != nil || transfer.copies != 1 || checkModelStage(ctx, path, plan.Assets[0].Download) != nil {
		t.Fatalf("a remote service did not stream: %q %v %d", path, err, transfer.copies)
	}
}

// Setup's default controllers download into the folder the provider names
// in setup-info, inside the provider's root, and a provider that names none
// is refused.
func TestSetupDownloadsIntoTheProviderDownloadFolder(t *testing.T) {
	f := newUpdateFixture(t, "lemonade-engine", packageEntries("lemonade-engine", "2.0.0"))
	withLemonadeJournal(t, f.journal)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cache, "OpenAbstractions", "providers", "lemonade")
	info := lemonadeInfoFor(t, f.v1, root, true)
	cli := lemonadeCLI
	t.Cleanup(func() { lemonadeCLI = cli })
	lemonadeCLI = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "setup-info" {
			return json.Marshal(info)
		}
		return nil, errors.New("unexpected provider command")
	}
	starter, err := DefaultBF16Controller(func(context.Context) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	choice, err := DefaultChoiceController(PictureModelChoices()[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []ModelController{starter, choice} {
		if c.StageDir != info.DownloadDir || c.ProviderRoot != root {
			t.Fatalf("downloads go to %s for provider %s, want %s", c.StageDir, c.ProviderRoot, info.DownloadDir)
		}
	}
	for _, dir := range []string{"", filepath.Join(cache, "elsewhere"), root} {
		info.DownloadDir = dir
		if _, err := DefaultBF16Controller(func(context.Context) (bool, error) { return false, nil }); err == nil {
			t.Fatalf("download folder %q accepted", dir)
		}
	}
}
