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
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// downloadTransferFake stands in for the job service: each completed
// download is its own result file in store, which Link hard-links to the
// destination (JOB-A17). Copy is the stream a model download must not use.
type downloadTransferFake struct {
	store     string
	bytes     map[string][]byte // by SHA-256
	keys      map[string]string // work key to SHA-256
	link      jobapi.LinkOutcome
	afterLink func()
	copies    int
	links     int
}

// Cancellation while verifying a previously published name proves nothing
// about its bytes. Preserve both names of the existing result and do no work.
func TestCancelledModelVerificationPreservesExistingLinkedResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertCancelledModelVerificationPreservesExistingLinkedResult(t, ctx)
}

type modelVerificationCancellation struct {
	context.Context
	cancel context.CancelFunc
	checks int
	at     int
	armed  func() bool
}

func (c *modelVerificationCancellation) Err() error {
	if c.armed != nil && !c.armed() {
		return c.Context.Err()
	}
	c.checks++
	at := c.at
	if at == 0 {
		at = 2
	}
	if c.checks == at {
		c.cancel()
	}
	return c.Context.Err()
}

func TestLinkedModelMismatchCleansOnlyItsOwnCheckedName(t *testing.T) {
	root := t.TempDir()
	transfer := newDownloadTransferFake(filepath.Join(root, "jobs"))
	plan := pictureFixturePlan(transfer)
	a := plan.Assets[0]
	c := ModelController{Plan: &plan, StageDir: filepath.Join(root, "models"), Transfer: transfer, LocalResults: true}
	work, _ := transfer.Reserve(context.Background(), a.Download, "owned-work")
	if _, err := transfer.Observe(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(transfer.store, work.Key)
	if err := os.WriteFile(source, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := c.stage(context.Background(), a, work, nil)
	var mismatch *modelStageMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("actual mismatch lost: %v", err)
	}
	if _, err := os.Lstat(c.modelStagePath(a)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bad owned link remains: %v", err)
	}
	if data, err := os.ReadFile(source); err != nil || !bytes.Equal(data, []byte("bad")) {
		t.Fatalf("service result changed: %v", err)
	}
	if transfer.links != 1 || transfer.copies != 0 {
		t.Fatal("unexpected transfer work")
	}
}

type modelCopyTargetRace struct {
	*downloadTransferFake
	target  string
	foreign []byte
	created os.FileInfo
}

func (f *modelCopyTargetRace) Copy(ctx context.Context, work WorkIdentity, dst io.Writer) (int64, error) {
	n, err := f.downloadTransferFake.Copy(ctx, work, dst)
	if err != nil {
		return n, err
	}
	if f.foreign != nil {
		err = os.WriteFile(f.target, f.foreign, 0600)
	} else {
		err = os.Link(filepath.Join(f.store, work.Key), f.target)
	}
	if err == nil {
		f.created, err = os.Stat(f.target)
	}
	return n, err
}

func TestPostCopyVerificationPreservesRacingTargetAndPrimaryError(t *testing.T) {
	for _, kind := range []string{"cancel", "mismatch"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			transfer := newDownloadTransferFake(filepath.Join(root, "jobs"))
			transfer.link = jobapi.LinkOutcomeRemote
			plan := pictureFixturePlan(transfer)
			a := plan.Assets[0]
			c := ModelController{Plan: &plan, StageDir: filepath.Join(root, "models")}
			work, _ := transfer.Reserve(context.Background(), a.Download, "owned-work")
			if _, err := transfer.Observe(context.Background(), work); err != nil {
				t.Fatal(err)
			}
			race := &modelCopyTargetRace{downloadTransferFake: transfer, target: c.modelStagePath(a)}
			c.Transfer = race
			var ctx context.Context = context.Background()
			if kind == "cancel" {
				base, cancel := context.WithCancel(ctx)
				defer cancel()
				ctx = &modelVerificationCancellation{Context: base, cancel: cancel, at: 1, armed: func() bool { return race.created != nil }}
			} else {
				race.foreign = []byte("foreign target")
			}
			_, err := c.stage(ctx, a, work, nil)
			if kind == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("post-copy cancellation lost: %v", err)
				}
			} else {
				var mismatch *modelStageMismatch
				if !errors.As(err, &mismatch) {
					t.Fatalf("mismatch lost: %v", err)
				}
			}
			want := race.foreign
			if want == nil {
				want = transfer.bytes[a.Download.SHA256]
			}
			if data, readErr := os.ReadFile(race.target); readErr != nil || !bytes.Equal(data, want) {
				t.Fatalf("racing target changed: %v", readErr)
			}
			if info, statErr := os.Stat(race.target); statErr != nil || !os.SameFile(race.created, info) {
				t.Fatalf("racing target identity changed: %v", statErr)
			}
			if entries, err := os.ReadDir(c.StageDir); err != nil || len(entries) != 1 {
				t.Fatalf("owned partial cleanup failed: %v, %d names", err, len(entries))
			}
			if transfer.copies != 1 || transfer.links != 1 {
				t.Fatal("unexpected transfer work")
			}
		})
	}
}

func TestModelMismatchCleanupPreservesReplacedIdentityAndReadFailure(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "model")
	data := []byte("valid tiny model")
	sum := sha256.Sum256(data)
	pin := PinnedDownload{Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	if err := os.WriteFile(target, bytes.Repeat([]byte{'x'}, len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	failure := checkModelStage(context.Background(), target, pin)
	var mismatch *modelStageMismatch
	if !errors.As(failure, &mismatch) {
		t.Fatal(failure)
	}
	if err := os.Rename(target, target+".old"); err != nil {
		t.Fatal(err)
	}
	foreign := bytes.Repeat([]byte{'y'}, len(data))
	if err := os.WriteFile(target, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, mismatch.file.ModTime(), mismatch.file.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := removeMismatchedModelStage(context.Background(), target, failure); err != failure {
		t.Fatalf("primary mismatch changed: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("replacement removed: %v", err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	readFailure := checkModelStage(context.Background(), directory, PinnedDownload{Bytes: info.Size(), SHA256: pin.SHA256})
	if readFailure == nil || errors.As(readFailure, &mismatch) {
		t.Fatalf("read uncertainty became content mismatch: %v", readFailure)
	}
	if err := removeMismatchedModelStage(context.Background(), directory, readFailure); err != readFailure {
		t.Fatalf("read failure changed: %v", err)
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("unreadable target removed: %v", err)
	}
}

func TestModelVerificationCancellationDuringReadPreservesExistingResult(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &modelVerificationCancellation{Context: base, cancel: cancel}
	assertCancelledModelVerificationPreservesExistingLinkedResult(t, ctx)
	if ctx.checks < 2 {
		t.Fatal("verification never reached its read cancellation check")
	}
}

func TestModelVerificationCancellationAfterLinkPreservesResult(t *testing.T) {
	root := t.TempDir()
	transfer := newDownloadTransferFake(filepath.Join(root, "jobs"))
	plan := pictureFixturePlan(transfer)
	a := plan.Assets[0]
	c := ModelController{Plan: &plan, StageDir: filepath.Join(root, "models"), Transfer: transfer, LocalResults: true}
	work, err := transfer.Reserve(context.Background(), a.Download, "owned-work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.Observe(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(transfer.store, work.Key), c.modelStagePath(a)
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	transfer.afterLink = cancel
	ctx := base
	_, err = c.stage(ctx, a, work, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("post-link cancellation became %v", err)
	}
	for _, path := range []string{source, target} {
		info, statErr := os.Stat(path)
		if statErr != nil || !os.SameFile(before, info) {
			t.Errorf("linked identity lost: %s %v", path, statErr)
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(data, transfer.bytes[a.Download.SHA256]) {
			t.Errorf("linked bytes changed: %v", readErr)
		}
	}
	if transfer.links != 1 || transfer.copies != 0 {
		t.Fatalf("unexpected transfer work: links %d copies %d", transfer.links, transfer.copies)
	}
}

func assertCancelledModelVerificationPreservesExistingLinkedResult(t *testing.T, ctx context.Context) {
	t.Helper()
	root := t.TempDir()
	transfer := newDownloadTransferFake(filepath.Join(root, "jobs"))
	plan := pictureFixturePlan(transfer)
	a := plan.Assets[0]
	c := ModelController{Plan: &plan, StageDir: filepath.Join(root, "models"), Transfer: transfer, LocalResults: true}
	work, err := transfer.Reserve(context.Background(), a.Download, "owned-work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.Observe(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.StageDir, 0700); err != nil {
		t.Fatal(err)
	}
	target := c.modelStagePath(a)
	source := filepath.Join(transfer.store, work.Key)
	if err := os.Link(source, target); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	transfer.link = jobapi.LinkOutcomeForbidden
	_, err = c.stage(ctx, a, work, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation became %v", err)
	}
	for _, path := range []string{source, target} {
		info, statErr := os.Stat(path)
		if statErr != nil || !os.SameFile(before, info) {
			t.Errorf("existing result identity lost at %s: %v", path, statErr)
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(data, transfer.bytes[a.Download.SHA256]) {
			t.Errorf("existing bytes changed: %v", readErr)
		}
	}
	if transfer.copies != 0 {
		t.Fatal("cancelled verification copied model bytes")
	}
	if transfer.links != 0 {
		t.Fatal("cancelled verification requested a new result link")
	}
}

func TestModelStageReplacesOnlyProvenContentMismatch(t *testing.T) {
	for _, kind := range []string{"size", "digest", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			transfer := newDownloadTransferFake(filepath.Join(root, "jobs"))
			plan := pictureFixturePlan(transfer)
			a := plan.Assets[0]
			c := ModelController{Plan: &plan, StageDir: filepath.Join(root, "models"), Transfer: transfer, LocalResults: true}
			work, err := transfer.Reserve(context.Background(), a.Download, "owned-work")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := transfer.Observe(context.Background(), work); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(c.StageDir, 0700); err != nil {
				t.Fatal(err)
			}
			target := c.modelStagePath(a)
			if kind == "directory" {
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				data := []byte("wrong size")
				if kind == "digest" {
					data = bytes.Repeat([]byte{'x'}, int(a.Download.Bytes))
				}
				if err := os.WriteFile(target, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = c.stage(context.Background(), a, work, nil)
			if kind == "directory" {
				if err == nil {
					t.Fatal("foreign directory accepted")
				}
				info, statErr := os.Stat(target)
				if statErr != nil || !info.IsDir() || transfer.links != 0 {
					t.Fatal("unproven target was removed or relinked")
				}
			} else if err != nil || checkModelStage(context.Background(), target, a.Download) != nil || transfer.links != 1 {
				t.Fatalf("proven content mismatch was not replaced: %v, links %d", err, transfer.links)
			}
			if transfer.copies != 0 {
				t.Fatal("replacement copied model bytes")
			}
		})
	}
}

func newDownloadTransferFake(store string) *downloadTransferFake {
	return &downloadTransferFake{store: store, bytes: map[string][]byte{}, keys: map[string]string{}, link: jobapi.LinkOutcomeLinked}
}

func TestCodingLocalPublicationKeepsOneFileAndRefusesRemoteCopy(t *testing.T) {
	for _, outcome := range []jobapi.LinkOutcome{jobapi.LinkOutcomeLinked, jobapi.LinkOutcomeRemote, jobapi.LinkOutcomeOtherVolume, jobapi.LinkOutcomeNoHardLinks} {
		root := t.TempDir()
		transfer := newDownloadTransferFake(filepath.Join(root, "jobs"))
		transfer.link = outcome
		plan := pictureFixturePlan(transfer)
		plan.Assets = plan.Assets[:1]
		c := ModelController{Plan: &plan, StageDir: filepath.Join(root, "models"), Transfer: transfer, LocalResults: true}
		a := plan.Assets[0]
		work, _ := transfer.Reserve(context.Background(), a.Download, "owned-work")
		if _, err := transfer.Observe(context.Background(), work); err != nil {
			t.Fatal(err)
		}
		path, err := c.stage(context.Background(), a, work, nil)
		if outcome == jobapi.LinkOutcomeLinked {
			if err != nil {
				t.Fatal(err)
			}
			original, _ := os.Stat(filepath.Join(transfer.store, work.Key))
			published, _ := os.Stat(path)
			if !os.SameFile(original, published) {
				t.Fatal("publication duplicated model bytes")
			}
			// Reusing the verified published file performs neither a link nor copy.
			transfer.link = jobapi.LinkOutcomeForbidden
			if _, err := c.stage(context.Background(), a, work, nil); err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatalf("outcome %v silently published", outcome)
		}
		if transfer.copies != 0 {
			t.Fatal("coding path copied a result")
		}
	}
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
	f.links++
	if f.link != jobapi.LinkOutcomeLinked {
		return f.link, nil
	}
	err := os.Link(filepath.Join(f.store, work.Key), destination)
	if err == nil && f.afterLink != nil {
		f.afterLink()
	}
	return jobapi.LinkOutcomeLinked, err
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
	cache, err := layout.AccountData()
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
	generic, err := DefaultBF16Controller(nil)
	if err != nil || generic.Selected != nil {
		t.Fatalf("generic preparation acquired an application observer: %v", err)
	}
	choice, err := DefaultChoiceController(PictureModelChoices()[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []ModelController{starter, generic, choice} {
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
