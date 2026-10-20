package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/huggingface"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/service"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	localsource "github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// hubCache lays out a Hugging Face hub cache under a stand-in home with the
// plan's repository, revision and file holding payload, as a plain file in
// its snapshot with no blob: the layout of every repository in the owner's
// cache (no symlink privilege, Lemonade's downloader).
func hubCache(t *testing.T, plan ModelPlan, payload []byte) (home, snap string) {
	t.Helper()
	home = t.TempDir()
	repo := filepath.Join(home, ".cache", "huggingface", "hub", "models--"+strings.ReplaceAll(plan.Repository, "/", "--"))
	snap = filepath.Join(repo, "snapshots", plan.Revision)
	for path, body := range map[string][]byte{filepath.Join(repo, "refs", "main"): []byte(plan.Revision), filepath.Join(snap, plan.Assets[0].Path): payload} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, snap
}

// inventoryFind is the step's lookup over the inventory owner's own source
// for home: Find by entry digest, and for same-size candidates with no
// digest, the source's Verify on each id, which hashes it on demand (LS-13).
// The fake waits for that hash, so the next lookup sees its result.
func inventoryFind(t *testing.T, home string) func(context.Context, ModelPlan) (StepLookup, error) {
	t.Helper()
	env := localsource.Env{GOOS: "test", Home: home, Getenv: func(string) string { return "" }, Digests: localsource.NewDigests("")}
	adapters := []localsource.Adapter{huggingface.New()}
	src := service.New(func() snapshot.Result { return snapshot.Take(env, adapters) })
	src.Hash = env.Digests.Request
	return func(_ context.Context, plan ModelPlan) (StepLookup, error) {
		page := snapshot.Take(env, adapters).Page
		var found []content.ManifestHolders
		for _, item := range page.Manifests {
			for _, entry := range item.Manifest.Entries {
				if entry.Digest == "sha256:"+plan.Assets[0].Download.SHA256 {
					found = append(found, item)
					break
				}
			}
		}
		if local := PinnedObject(plan, found); local != nil {
			return StepLookup{Local: local}, nil
		}
		candidates := SameSizeCandidates(plan, page.Manifests)
		for _, id := range candidates {
			if _, err := src.Verify(id); err != nil {
				return StepLookup{}, err
			}
		}
		env.Digests.Wait()
		return StepLookup{Hashing: len(candidates) > 0}, nil
	}
}

func (w *stepWorld) plannedFixture() {
	// The coding fixture plan names a hub repository the reader accepts.
	w.plan.Repository, w.plan.Revision = "fixture/coder", strings.Repeat("5", 40)
	w.plan.Assets[0].Path = "coder-Q4_K_M.gguf"
}

// preview-1031j: the pinned coding model already sat in the owner's Hugging
// Face cache and the question offered a 23 GB download. The inventory lists
// it with no digest; the step asks it to hash that one same-size candidate,
// says it is checking, finds it by digest, says nothing is downloaded and
// hosts that inventory object in place.
func TestStepAdoptsThePinnedFileTheInventoryLists(t *testing.T) {
	w := newStepWorld(t)
	w.plannedFixture()
	home, _ := hubCache(t, w.plan, w.transfer.payload)
	s := w.step()
	s.Owners.Local = inventoryFind(t, home)
	var adopted []StepLocal
	s.Owners.Adopt = func(_ context.Context, local StepLocal, h StepHosting) error {
		adopted = append(adopted, local)
		w.hostedNow, w.hosting = true, h
		return nil
	}
	outcome, err := s.Run(context.Background())
	if err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if q := w.questions[0]; q.Instruction != "Nothing to download." || !strings.HasPrefix(q.Details, "OpenCode gets Qwen3.6 35B, which is already on this computer. ") ||
		!strings.Contains(q.Details, "uses the copy already on this computer") {
		t.Fatalf("question = %+v", q)
	}
	checks := 0
	for _, n := range w.notices {
		if n.Body == "Checking a copy already on this computer." {
			checks++
		}
	}
	if checks != 1 {
		t.Fatalf("%d progress notices while the inventory hashed the candidate on request", checks)
	}
	if w.transfer.reserves != 0 || w.hosted != 0 || len(adopted) != 1 || adopted[0].Store == "" || adopted[0].Object == "" || w.selects != 1 {
		t.Fatalf("reserves %d hosted %d adopted %+v selects %d", w.transfer.reserves, w.hosted, adopted, w.selects)
	}
	state, _ := w.step().Load(context.Background())
	if state.Adopted == nil || *state.Adopted != adopted[0] || !state.Ready() || !state.Hosted {
		t.Fatalf("journal = %+v", state)
	}
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepAlreadySetUp || len(adopted) != 1 || w.transfer.reserves != 0 {
		t.Fatalf("rerun = %q, %v adopted %d reserves %d", outcome, err, len(adopted), w.transfer.reserves)
	}
}

// Same-size bytes that are not the release: the inventory lists their own
// digest, Find for the pin answers nothing, and the step offers the download.
func TestStepDownloadsWhenTheListedFileDiffers(t *testing.T) {
	w := newStepWorld(t)
	w.plannedFixture()
	home, _ := hubCache(t, w.plan, []byte(strings.Repeat("x", len(w.transfer.payload))))
	s := w.step()
	s.Owners.Local = inventoryFind(t, home)
	s.Owners.Adopt = func(context.Context, StepLocal, StepHosting) error {
		t.Fatal("a differing file was adopted")
		return nil
	}
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if q := w.questions[0]; !strings.Contains(q.Instruction, "to download") || w.transfer.reserves != 1 {
		t.Fatalf("question %+v reserves %d", q, w.transfer.reserves)
	}
}

// A snapshot entry linking into the repository's blobs carries its blob's
// digest from the reader, so the same Find adopts it.
func TestStepAdoptsALinkedSnapshot(t *testing.T) {
	w := newStepWorld(t)
	w.plannedFixture()
	home, snap := hubCache(t, w.plan, w.transfer.payload)
	file := filepath.Join(snap, w.plan.Assets[0].Path)
	blob := filepath.Join(filepath.Dir(filepath.Dir(snap)), "blobs", w.plan.Assets[0].Download.SHA256)
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file, blob); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "blobs", w.plan.Assets[0].Download.SHA256), file); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	lookup, err := inventoryFind(t, home)(context.Background(), w.plan)
	if err != nil || lookup.Local == nil {
		t.Fatalf("linked snapshot = %+v, %v", lookup, err)
	}
}

// PinnedObject takes only a model whose one weights entry has the pinned
// digest and size.
func TestPinnedObjectNeedsDigestAndSize(t *testing.T) {
	w := newStepWorld(t)
	pin := w.plan.Assets[0].Download
	model := func(store, digest string, size int64) content.ManifestHolders {
		return content.ManifestHolders{Manifest: content.Manifest{Store: store, ID: store + "-object", Kind: "abstraction.model/descriptor@1",
			Entries: []content.Entry{{Role: "weights", Digest: digest, Size: size}}}}
	}
	found := []content.ManifestHolders{model("short", "sha256:"+pin.SHA256, pin.Bytes-1), model("other", "sha256:"+strings.Repeat("0", 64), pin.Bytes),
		model("good", "sha256:"+pin.SHA256, pin.Bytes)}
	if local := PinnedObject(w.plan, found); local == nil || local.Store != "good" {
		t.Fatalf("picked %+v", local)
	}
	if local := PinnedObject(w.plan, found[:2]); local != nil {
		t.Fatalf("picked %+v from no matching object", local)
	}
}

// Coordinator decision 3: the step never offers the download while a
// same-size candidate is still being hashed. It posts one progress line,
// waits for the hash and then uses the copy, or offers the download once
// the hash shows other bytes or the bound ends.
func TestStepWaitsForACandidateStillBeingHashed(t *testing.T) {
	cases := []struct {
		name     string
		answers  []StepLookup
		adopted  bool
		download bool
	}{
		{"the hash matches", []StepLookup{{Hashing: true}, {Hashing: true}, {Local: &StepLocal{Store: "hub", Object: "o"}}}, true, false},
		{"the hash differs", []StepLookup{{Hashing: true}, {}}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStepWorld(t)
			s := w.step()
			asked := 0
			s.Owners.Local = func(context.Context, ModelPlan) (StepLookup, error) {
				answer := c.answers[min(asked, len(c.answers)-1)]
				asked++
				return answer, nil
			}
			adopted := false
			s.Owners.Adopt = func(context.Context, StepLocal, StepHosting) error { adopted = true; return nil }
			if outcome, err := s.Run(context.Background()); err != nil || outcome != stepSetUp {
				t.Fatalf("run = %q, %v", outcome, err)
			}
			checks := 0
			for _, n := range w.notices {
				if n.Body == "Checking a copy already on this computer." {
					checks++
				}
			}
			if checks != 1 || asked != len(c.answers) || adopted != c.adopted || (w.transfer.reserves > 0) != c.download {
				t.Fatalf("checks %d asked %d adopted %v reserves %d", checks, asked, adopted, w.transfer.reserves)
			}
			nothing := strings.Contains(w.questions[0].Instruction, "Nothing to download.")
			if nothing != c.adopted {
				t.Fatalf("question = %+v", w.questions[0])
			}
		})
	}
	// A hash that never ends is waited for within the bound, then the
	// download is offered.
	w := newStepWorld(t)
	s := w.step()
	clock := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	s.Owners.Now = func() time.Time { return clock }
	s.Owners.Local = func(context.Context, ModelPlan) (StepLookup, error) { return StepLookup{Hashing: true}, nil }
	waits := 0
	s.Owners.Wait = func(context.Context) error {
		waits++
		clock = clock.Add(10 * time.Second)
		w.transfer.state = "complete"
		return nil
	}
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepSetUp || w.transfer.reserves != 1 {
		t.Fatalf("run = %q, %v reserves %d", outcome, err, w.transfer.reserves)
	}
	if bound := localCheckBound(25_000_000_000); bound < 170*time.Second || bound > 190*time.Second {
		t.Fatalf("bound for 25 GB = %v", bound)
	}
	if waits < 3 || waits > 10 {
		t.Fatalf("waited %d rounds for a tiny file's 30 s bound", waits)
	}
}

// Coordinator decision: a copy of the pinned file in a place other accounts
// can change is not adopted. The step offers the download, and Show details
// says why in one sentence.
func TestStepDownloadsWhenTheCopyIsWritableByOthers(t *testing.T) {
	w := newStepWorld(t)
	s := w.step()
	s.Owners.Local = func(context.Context, ModelPlan) (StepLookup, error) { return StepLookup{Untrusted: true}, nil }
	s.Owners.Adopt = func(context.Context, StepLocal, StepHosting) error {
		t.Fatal("an untrusted copy was adopted")
		return nil
	}
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	q := w.questions[0]
	if !strings.Contains(q.Instruction, "to download") || w.transfer.reserves != 1 ||
		!strings.HasSuffix(q.Details, "Other accounts can change the copy here. OpenAbstractions downloads its own.") {
		t.Fatalf("question %+v reserves %d", q, w.transfer.reserves)
	}
	// A trusted copy is adopted (TestStepAdoptsThePinnedFileTheInventoryLists);
	// a download with no copy found says nothing about one.
	w2 := newStepWorld(t)
	if _, err := w2.step().Run(context.Background()); err != nil || strings.Contains(w2.questions[0].Details, "Other accounts can change") {
		t.Fatalf("plain download details %q, %v", w2.questions[0].Details, err)
	}
}
