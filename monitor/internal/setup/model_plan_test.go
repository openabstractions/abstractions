package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// lemonadePackagingManifest is the Lemonade provider's reviewed packaging
// manifest in the private workspace.
func lemonadePackagingManifest(t *testing.T) string {
	t.Helper()
	return privateInput(t, "..", "..", "..", "openabstractions-flat", "abstraction-provider-lemonade", "packaging", "windows-amd64.json")
}

// The provider's reviewed packaging manifest owns these release pins. The
// Panel's compiled plan is a projection; changing either side requires an
// explicit joint review rather than silently drifting. The manifest ships in
// the provider's repository outside its Go module, so this is a check across
// the two repositories that the private workspace runs.
func TestApprovedImagePlanMatchesProviderPackagingManifest(t *testing.T) {
	raw, err := os.ReadFile(lemonadePackagingManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Schema int `json:"schema"`
		Model  struct {
			ID, Source, Revision, License string
			MainSHA                       string `json:"main_sha256"`
			EncoderSHA                    string `json:"encoder_sha256"`
			VAESHA                        string `json:"vae_sha256"`
			TotalBytes                    int64  `json:"total_bytes"`
		} `json:"qualified_model"`
		Backend struct {
			URL      string `json:"url"`
			SHA      string `json:"archive_sha256"`
			Bytes    int64  `json:"archive_bytes"`
			Expanded int64  `json:"expanded_bytes"`
			License  string `json:"license"`
		} `json:"vulkan_backend"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	plan := ApprovedBF16Plan()
	if manifest.Schema != 1 || plan.Model != manifest.Model.ID || plan.Repository != manifest.Model.Source || plan.Revision != manifest.Model.Revision || plan.Licenses[0] != manifest.Model.License || plan.Licenses[1] != manifest.Backend.License {
		t.Fatal("compiled image plan identity or licenses differ from reviewed provider manifest")
	}
	assets := map[string]ModelAsset{}
	for _, a := range plan.Assets {
		assets[a.ID] = a
	}
	if len(assets) != 4 || assets["main"].Download.SHA256 != manifest.Model.MainSHA || assets["encoder"].Download.SHA256 != manifest.Model.EncoderSHA || assets["vae"].Download.SHA256 != manifest.Model.VAESHA || assets["backend"].Download.URL != manifest.Backend.URL || assets["backend"].Download.SHA256 != manifest.Backend.SHA || assets["backend"].Download.Bytes != manifest.Backend.Bytes || backendExpandedBytes != manifest.Backend.Expanded || plan.DownloadBytes-manifest.Backend.Bytes != manifest.Model.TotalBytes {
		t.Fatal("compiled image artifact pins differ from reviewed provider manifest")
	}
	modelPrefix := "https://huggingface.co/" + manifest.Model.Source + "/resolve/" + manifest.Model.Revision + "/split_files/"
	canonicalPaths := map[string]string{
		"main":    "diffusion_models/flux-2-klein-4b.safetensors",
		"encoder": "text_encoders/qwen_3_4b.safetensors",
		"vae":     "vae/flux2-vae.safetensors",
	}
	for id, relative := range canonicalPaths {
		if assets[id].Path != relative || assets[id].Download.URL != modelPrefix+relative {
			t.Fatalf("compiled %s model URL differs from reviewed repository/revision/path", id)
		}
	}
}

// Approvals record the plan ID, so the starter plan's encoding stays fixed
// while other picture models are offered beside it.
func TestApprovedImagePlanIDIsUnchanged(t *testing.T) {
	if id := ApprovedBF16Plan().ID; id != "11e0b867586694b78900ab8ecba79f824b8370e7dd096a3fa71e1bf107ed5aeb" {
		t.Fatalf("starter plan ID changed to %s; recorded approvals would no longer match", id)
	}
}

// The starter plan reserves one copy of its downloads, the expanded backend
// and headroom at peak: the job service's result and the provider's model
// file are one file (JOB-A17).
func TestStarterPlanReservesOneCopyAtPeak(t *testing.T) {
	p := ApprovedBF16Plan()
	if p.PeakBytes != 18432770815 || p.PeakBytes != p.DownloadBytes+backendExpandedBytes+bf16DiskHeadroom {
		t.Fatalf("starter plan reserves %d bytes at peak for %d bytes of downloads", p.PeakBytes, p.DownloadBytes)
	}
}

// A model journal written by 0.3.0 names the starter plan's ID; the lower
// peak leaves that installation prepared and its approval current.
func TestStarterJournalFromEarlierReleaseStillReadsAsInstalled(t *testing.T) {
	const recorded = "11e0b867586694b78900ab8ecba79f824b8370e7dd096a3fa71e1bf107ed5aeb"
	root := t.TempDir()
	journal := filepath.Join(root, "model.json")
	state := ModelState{PlanID: recorded, Approved: true, ProviderConfigured: true, Effects: map[string]ModelEffect{}}
	for _, a := range ApprovedBF16Plan().Assets {
		state.Effects[a.ID] = ModelEffect{Phase: Verified, StagedPath: filepath.Join(root, a.ID)}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c := ModelController{JournalPath: journal, StageDir: filepath.Join(root, "stage"), ProviderRoot: filepath.Join(root, "provider"),
		FreeBytes: func(string) (uint64, error) { return 19_000_000_000, nil },
		Selected:  func(context.Context) (bool, error) { return true, nil }}
	status, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Approved || !status.ProviderConfigured || !status.Ready || status.Plan.ID != recorded {
		t.Fatalf("earlier journal reads as %+v", status)
	}
	p := ApprovedBF16Plan()
	if _, err := c.Start(context.Background(), ModelApproval{PlanID: p.ID, DownloadBytes: p.DownloadBytes, PeakBytes: p.PeakBytes, Licenses: p.Licenses}); err != nil {
		t.Fatalf("approval of the installed plan refused: %v", err)
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("repeating the approval rewrote the installed journal")
	}
}

// The disk check passes with about 19 GB free for the 16.2 GB starter plan
// and refuses one byte below the stated peak.
func TestStarterDiskCheckFitsNineteenGigabytes(t *testing.T) {
	p := ApprovedBF16Plan()
	root := t.TempDir()
	for _, tc := range []struct {
		free   uint64
		enough bool
	}{{19_000_000_000, true}, {uint64(p.PeakBytes), true}, {uint64(p.PeakBytes) - 1, false}, {18_000_000_000, false}} {
		free := tc.free
		c := ModelController{JournalPath: filepath.Join(root, "model.json"), StageDir: filepath.Join(root, "stage"), ProviderRoot: filepath.Join(root, "provider"),
			FreeBytes: func(string) (uint64, error) { return free, nil }}
		disk, err := c.DiskPreflight()
		if err != nil {
			t.Fatal(err)
		}
		if disk.Enough != tc.enough || disk.RequiredPeakBytes != p.PeakBytes {
			t.Fatalf("%d bytes free: %+v", free, disk)
		}
	}
}

// An offered plan reserves one copy of its downloads at peak: the job
// service's result and the provider's model file are one file (JOB-A17).
func TestOfferedPlansReserveOneCopyAtPeak(t *testing.T) {
	for _, plan := range PictureModelChoices() {
		if plan.PeakBytes != plan.DownloadBytes+bf16DiskHeadroom {
			t.Fatalf("%s reserves %d bytes at peak for %d bytes of downloads", plan.Model, plan.PeakBytes, plan.DownloadBytes)
		}
	}
}

// Setup offers the provider's other declared picture models with their
// pinned files, sizes and licences, each from the reviewed packaging manifest.
func TestPictureModelChoicesMatchProviderPackagingManifest(t *testing.T) {
	raw, err := os.ReadFile(lemonadePackagingManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Models []struct {
			ID, Title, Source, License string
			LicenseURL                 string `json:"license_url"`
			LicenseText                string `json:"license_text"`
			TotalBytes                 int64  `json:"total_bytes"`
			Files                      []struct {
				Role, Repository, Revision, Path, SHA256 string
				Bytes                                    int64
			}
		} `json:"declared_models"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	choices := PictureModelChoices()
	if len(manifest.Models) != 2 || len(choices) != len(manifest.Models) {
		t.Fatalf("manifest declares %d models, Setup offers %d", len(manifest.Models), len(choices))
	}
	assetID := map[string]string{"main": "main", "text_encoder": "encoder", "vae": "vae"}
	for i, m := range manifest.Models {
		plan := choices[i]
		if plan.Model != m.ID || plan.Name != m.Title || plan.Source != m.Source || len(plan.Licenses) != 1 || plan.Licenses[0] != m.License ||
			plan.LicenseText != m.LicenseText || plan.LicenseText == "" || plan.DownloadBytes != m.TotalBytes || plan.PeakBytes <= plan.DownloadBytes ||
			plan.Repository != m.Files[0].Repository || plan.Revision != m.Files[0].Revision || len(plan.Assets) != len(m.Files) || len(plan.ID) != 64 {
			t.Fatalf("choice %d = %+v, manifest %+v", i, plan, m)
		}
		var total int64
		for j, f := range m.Files {
			a := plan.Assets[j]
			want := "https://huggingface.co/" + f.Repository + "/resolve/" + f.Revision + "/" + f.Path
			if a.ID != assetID[f.Role] || a.Path != f.Path || a.License != m.License || a.LicenseURL != m.LicenseURL ||
				a.Download.URL != want || a.Download.SHA256 != f.SHA256 || a.Download.Bytes != f.Bytes || a.Download.Label == "" {
				t.Fatalf("%s asset %d = %+v, manifest %+v", m.ID, j, a, f)
			}
			total += f.Bytes
		}
		if total != m.TotalBytes {
			t.Fatalf("%s manifest total %d, files %d", m.ID, m.TotalBytes, total)
		}
	}
	if choices[0].Model != "user.Z-Image-Turbo" || choices[1].Model != "user.Qwen-Image-2512" {
		t.Fatalf("choices = %s, %s", choices[0].Model, choices[1].Model)
	}
}
