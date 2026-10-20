package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
)

func TestFindPictureChoiceKeepsTheControllerEngine(t *testing.T) {
	for _, test := range []struct {
		want       ModelPlan
		engine     AppID
		stagedOnly bool
	}{
		{ZImageComfyModelPlan(), PyTorch, true},
		{PyTorchModelPlan(), PyTorch, false},
		{PictureModelChoices()[0], Lemonade, false},
	} {
		plan, engine, ok := FindPictureChoice(test.want.ID)
		if !ok || plan.ID != test.want.ID || plan.Engine != test.want.Engine || engine != test.engine {
			t.Fatalf("choice %q: plan %q, plan engine %s, controller engine %s, found %v", test.want.ID, plan.ID, plan.Engine, engine, ok)
		}
		if plan.StagesPictureFilesOnly() != test.stagedOnly {
			t.Fatalf("choice %q: staging-only plan = %v", test.want.ID, plan.StagesPictureFilesOnly())
		}
	}
	for _, id := range []string{"", "not-offered", ApprovedBF16Plan().ID} {
		if _, _, ok := FindPictureChoice(id); ok {
			t.Fatalf("unoffered choice resolved: %q", id)
		}
	}
}

func TestPictureChoiceControllerKeepsTheJournalBoundary(t *testing.T) {
	blocked := errors.New("unavailable Setup journal")
	calls := 0
	journal := func() (Journal, error) { calls++; return Journal{}, blocked }
	if _, err := OpenPictureChoiceController(PyTorch, ZImageComfyModelPlan(), journal); !errors.Is(err, blocked) || calls != 1 {
		t.Fatalf("shared-engine journal use: %d, %v", calls, err)
	}
	if _, err := OpenPictureChoiceController(Lemonade, ModelPlan{}, journal); err == nil || errors.Is(err, blocked) || calls != 1 {
		t.Fatalf("fixed provider admission: %d, %v", calls, err)
	}
}

// The PyTorch engine's recommended picture model is the compact FLUX.2
// Klein (8.26 GB of weights plus its tokenizer); the full-precision Klein
// keeps the identity its prepared copies recorded, and Z-Image-Turbo is
// offered for ComfyUI only. Each is its own toggle with its own journal.
func TestPyTorchPictureOffersAreSeparateToggles(t *testing.T) {
	compact, full, zimage := PyTorchModelPlan(), PyTorchFullModelPlan(), ZImageComfyModelPlan()
	if full.ID != "efce852a5085946e4eb4d4f61e520b81e5dbb3bef8c5d3d042f39bb89e572553" {
		t.Fatalf("the full-precision Klein's recorded identity changed: %s", full.ID)
	}
	if compact.Model != torch.Flux2Klein4BCompactTorch.Name || compact.DownloadBytes != 8270932042 || full.DownloadBytes != 16148181284 {
		t.Fatalf("plan sizes %s %d %d", compact.Model, compact.DownloadBytes, full.DownloadBytes)
	}
	if got := compact.Assets[0].Download.URL; got != "https://huggingface.co/black-forest-labs/FLUX.2-klein-4b-fp8/resolve/5b4408e59397a4a37ccb46afe426d8ed86379441/flux-2-klein-4b-fp8.safetensors" ||
		compact.Assets[0].Path != "split_files/diffusion_models/flux-2-klein-4b-fp8.safetensors" {
		t.Fatalf("the FP8 transformer is fetched from %s and staged at %s", got, compact.Assets[0].Path)
	}
	offers := PictureModelOffers(PyTorch)
	if len(offers) != 3 || offers[0].Plan.ID != compact.ID || !offers[0].Recommended || offers[1].Plan.ID != full.ID || offers[1].Recommended ||
		offers[2].Plan.ID != zimage.ID || !offers[2].Plan.StagesPictureFilesOnly() ||
		offers[0].Plan.StagesPictureFilesOnly() || offers[1].Plan.StagesPictureFilesOnly() {
		t.Fatalf("offers %+v", offers)
	}
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	seen := map[string]bool{}
	for _, offer := range offers {
		if offer.Plan.Title == "" || !slicesEqual(offer.Plan.Licenses, []string{"Apache-2.0"}) {
			t.Fatalf("offer %s has no title or another licence", offer.Plan.Model)
		}
		path := PictureModelJournal(journal, offer.Plan)
		if seen[path] {
			t.Fatalf("two offers share the journal %s", path)
		}
		seen[path] = true
		for _, a := range offer.Plan.Assets {
			if !strings.HasPrefix(a.Path, "split_files/") && !strings.HasPrefix(a.Path, "tokenizer/") {
				t.Fatalf("%s stages %s outside ComfyUI's folders", offer.Plan.Model, a.Path)
			}
		}
	}
	// The native attachment keeps its archived plan identity and stage-only
	// readiness even though the title shows the model's name.
	if zimage.Title != "Z-Image-Turbo" || zimage.Engine != ComfyUI {
		t.Fatalf("Z-Image-Turbo's row %q engine %s", zimage.Title, zimage.Engine)
	}
	if lemonade := PictureModelOffers(Lemonade); len(lemonade) != 1 || !lemonade[0].Recommended || lemonade[0].Plan.StagesPictureFilesOnly() {
		t.Fatalf("Lemonade offers %+v", lemonade)
	}
}

func slicesEqual(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// writePictureState writes plan's model journal, approved with every asset
// verified and configured as given.
func writePictureState(t *testing.T, journal Journal, plan ModelPlan, configured bool) {
	t.Helper()
	state := ModelState{PlanID: plan.ID, Approved: true, Effects: map[string]ModelEffect{}, ProviderConfigured: configured}
	for _, a := range plan.Assets {
		state.Effects[a.ID] = ModelEffect{Phase: Verified}
	}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(PictureModelJournal(journal, plan), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A prepared full-precision Klein counts as the engine's picture model, and
// its files reach ComfyUI from
// the same three folders as the compact set's.
func TestPreparedFullKleinIsServedAndReachesComfyUI(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	journal := Journal{Path: filepath.Join(root, "setup", "plan.json")}
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if has, err := PictureEngineHasModel(ctx, journal, PyTorch); err != nil || has {
		t.Fatalf("an empty engine has a model: %v %v", has, err)
	}
	full := PyTorchFullModelPlan()
	writePictureState(t, journal, full, true)
	if has, err := PictureEngineHasModel(ctx, journal, PyTorch); err != nil || !has {
		t.Fatalf("a prepared full Klein is no model: %v %v", has, err)
	}
	if configured, err := starterPictureConfigured(ctx, journal, PyTorch); err != nil || configured {
		t.Fatalf("the full Klein's journal reads as the compact starter's: %v %v", configured, err)
	}
	stage := filepath.Join(root, "OA", "m", "pt", "flux2-klein-4b")
	for i := range full.Assets {
		full.Assets[i].Download.Bytes = int64(i + 1)
		path := filepath.Join(stage, filepath.FromSlash(full.Assets[i].Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(path)
		if err == nil {
			err = errors.Join(f.Truncate(full.Assets[i].Download.Bytes), f.Close())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	folders, err := preparedModelFolders(ctx, journal, stage, full, oaArchiveLayout.ModelCategories[:])
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"diffusion_models": filepath.Join(stage, "split_files", "diffusion_models"),
		"text_encoders": filepath.Join(stage, "split_files", "text_encoders"), "vae": filepath.Join(stage, "split_files", "vae")}
	if len(folders) != 3 {
		t.Fatalf("folders %v", folders)
	}
	for k, v := range want {
		if folders[k] != v {
			t.Fatalf("%s folder %q, want %q", k, folders[k], v)
		}
	}
	for _, plan := range []ModelPlan{PyTorchModelPlan(), ZImageComfyModelPlan()} {
		c := ModelController{Plan: &plan, StageDir: stage}
		for _, a := range plan.Assets {
			got := filepath.Dir(c.modelStagePath(a))
			if kind := filepath.Base(got); want[kind] != "" && got != want[kind] {
				t.Fatalf("%s stages %s in %s", plan.Model, a.Path, got)
			}
		}
	}
}

// The picture default is the account's: the first prepared Klein writes
// it, a later compact Klein beside a served full-precision one leaves it,
// and the full-precision Klein writes it when its set becomes the served
// one. An Install record naming the full Klein, the PyTorch starter up to
// 2026-11-09, keeps its engine.
func TestPictureDefaultIsWrittenOncePerAccount(t *testing.T) {
	ctx := context.Background()
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	compact, full := PyTorchModelPlan(), PyTorchFullModelPlan()
	if due, err := PictureDefaultDue(ctx, journal, compact, compact.Model); err != nil || !due {
		t.Fatalf("the first Klein: %v %v", due, err)
	}
	writePictureState(t, journal, full, true)
	if err := MarkPictureDefaultWritten(ctx, journal, full); err != nil {
		t.Fatal(err)
	}
	if due, err := PictureDefaultDue(ctx, journal, compact, full.Model); err != nil || due {
		t.Fatalf("a compact Klein beside the served full one rewrote the default: %v %v", due, err)
	}
	if due, err := PictureDefaultDue(ctx, journal, full, full.Model); err != nil || !due {
		t.Fatalf("the newly served full Klein: %v %v", due, err)
	}
	if StarterPictureEngine(full.ID) != PyTorch || StarterPictureEngine(compact.ID) != PyTorch ||
		StarterPictureEngine(ApprovedBF16Plan().ID) != Lemonade || StarterPictureEngine("") != "" {
		t.Fatal("an Install record's picture engine")
	}
}
