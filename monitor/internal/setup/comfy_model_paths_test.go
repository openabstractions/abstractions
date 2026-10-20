package setup

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The PyTorch starter model's folders become ComfyUI's oa-models group once
// the model is prepared and its files lie in them, under the compact
// FLUX.2 Klein's file names; a removed file or an unprepared model leaves
// no file for the launch helper to hold.
func TestComfyModelPathsFollowThePreparedPyTorchModel(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	chosenHome.Store(&homeChoice{home: Home{Config: filepath.Join(root, "setup"), Data: root}})
	t.Cleanup(UseUserState)
	journal, err := DefaultJournal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "OA", "m", "pt", "flux2-klein-4b")
	name := filepath.Join(root, "OpenAbstractions", "ComfyUI", "model-paths.json")
	// The pinned names and folders with small stand-in files.
	plan := PyTorchModelPlan()
	files := map[string]string{}
	for i := range plan.Assets {
		plan.Assets[i].Download.Bytes = int64(i + 1)
	}
	sync := func() map[string]map[string]string {
		t.Helper()
		policy := modelPathsPolicy{root: oaArchiveLayout.DataRoot, file: oaArchiveLayout.ModelFile,
			group: oaArchiveLayout.ModelGroup, categories: oaArchiveLayout.ModelCategories[:]}
		if err := syncModelPaths(ctx, journal, policy, []PictureOffer{{Plan: plan}}, func(Home) string { return stage }); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, asset := range plan.Assets {
		path := filepath.Join(stage, filepath.FromSlash(asset.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(path)
		if err == nil {
			err = errors.Join(f.Truncate(asset.Download.Bytes), f.Close())
		}
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Base(path)] = path
	}
	if got := sync(); got != nil {
		t.Fatalf("an unprepared model reaches ComfyUI: %v", got)
	}
	writeStarterState(t, journal, PyTorch, true)
	got := sync()
	if len(got) != 1 || len(got["oa-models"]) != 3 {
		t.Fatalf("model paths %v", got)
	}
	for category, file := range map[string]string{"diffusion_models": "flux-2-klein-4b-fp8.safetensors", "text_encoders": "qwen_3_4b_fp4_flux2.safetensors", "vae": "flux2-vae.safetensors"} {
		if folder := got["oa-models"][category]; folder != filepath.Dir(files[file]) {
			t.Fatalf("%s folder %q does not hold %s", category, folder, file)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(name), ".*")); len(matches) != 0 {
		t.Fatalf("the replacement left temporary files: %v", matches)
	}
	if err := os.Remove(files["flux2-vae.safetensors"]); err != nil {
		t.Fatal(err)
	}
	if got := sync(); got != nil {
		t.Fatalf("a removed model file stays named: %v", got)
	}
	if err := writeModelPaths(name, oaArchiveLayout.ModelGroup, nil); err != nil {
		t.Fatal("removing an absent file:", err)
	}
}

// writeStarterState writes engine's starter model journal, approved with
// every asset verified and configured as given.
func writeStarterState(t *testing.T, journal Journal, engine AppID, configured bool) {
	t.Helper()
	plan, _ := StarterPictureModel(engine)
	state := ModelState{PlanID: plan.ID, Approved: true, Effects: map[string]ModelEffect{}, ProviderConfigured: configured}
	for _, a := range plan.Assets {
		state.Effects[a.ID] = ModelEffect{Phase: Verified}
	}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(StarterModelJournal(journal, engine), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Only the person's own Setup journal changes the file in ComfyUI's data
// folder: without a prepared model it removes a stale file; another journal
// leaves it as it is.
func TestSyncComfyModelPathsBelongsToTheOwnJournal(t *testing.T) {
	ctx := context.Background()
	config, data := t.TempDir(), t.TempDir()
	chosenHome.Store(&homeChoice{home: Home{Config: config, Data: data}})
	t.Cleanup(UseUserState)
	name := filepath.Join(data, "OpenAbstractions", "ComfyUI", "model-paths.json")
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(`{"oa-models":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	if err := SyncRetainedArchiveModelPaths(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatal("another journal changed ComfyUI's model paths:", err)
	}
	own, err := DefaultJournal()
	if err != nil {
		t.Fatal(err)
	}
	chosenHome.Store(&homeChoice{home: Home{Config: config, Data: data, Isolated: true}})
	if err := SyncRetainedArchiveModelPaths(ctx, own); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(name); err != nil || string(got) != `{"oa-models":{}}` {
		t.Fatalf("isolated home changed the shared file: %q, %v", got, err)
	}
	chosenHome.Store(&homeChoice{home: Home{Config: config, Data: data}})
	if err := SyncRetainedArchiveModelPaths(ctx, own); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a stale model-paths file stayed without a prepared model:", err)
	}
}
