package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	imageprovider "github.com/openabstractions/abstraction-provider-lemonade/go/server"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
)

const (
	BF16ModelID                = "user.LocalApprovedKlein"
	bf16Revision               = "5f526678002e43af5551dadb73ce2e8c91b43afe"
	bf16License                = "Apache-2.0"
	bf16LicenseURL             = "https://www.apache.org/licenses/LICENSE-2.0"
	backendLicense             = "MIT"
	backendLicenseURL          = "https://opensource.org/license/mit"
	backendExpandedBytes int64 = 114122885
	// Leave headroom for private metadata, partial files and other disk use.
	bf16DiskHeadroom = 2 << 30
)

// ModelAsset is a release-reviewed source, never populated from a browser
// request. The provider stages the four resulting verified files privately.
type ModelAsset struct {
	ID         string         `json:"id"`
	Role       string         `json:"role,omitempty"`
	Path       string         `json:"path"`
	License    string         `json:"license"`
	LicenseURL string         `json:"license_url"`
	Download   PinnedDownload `json:"download"`
}

func (a ModelAsset) reviewedStagePath(root string) string {
	return filepath.Join(root, filepath.FromSlash(a.Path))
}

// DigestStageName is the pinned asset's filename in the digest-based stage.
func (a ModelAsset) DigestStageName() string {
	return a.Download.SHA256 + filepath.Ext(a.Path)
}

func (a ModelAsset) digestStagePath(root string) string {
	return filepath.Join(root, a.DigestStageName())
}

// stagePath keeps the declared layout for plans whose loaders use that
// layout. Other plans have always staged by pinned digest and extension.
func (p ModelPlan) stagePath(root string, asset ModelAsset) string {
	recipe, _ := pictureRecipeFor(p.Engine)
	if recipe.reviewedLayout || p.StagesPictureFilesOnly() {
		return asset.reviewedStagePath(root)
	}
	return asset.digestStagePath(root)
}

// PyTorchModelPlan is the PyTorch engine's starter picture model: FLUX.2
// Klein 4B from its compact files (8.26 GB), which the engine turns to BF16
// at load and ComfyUI's own loaders read through Setup's model-paths file.
func PyTorchModelPlan() ModelPlan {
	// The name is identity (the plan ID hashes it), never shown; Title is.
	plan := pyTorchModelPlanFor(torch.Flux2Klein4BCompactTorch, "FLUX.2 Klein 4B compact on PyTorch",
		"https://huggingface.co/black-forest-labs/FLUX.2-klein-4b-fp8",
		"Black Forest Labs publishes the FP8 model and Comfy-Org the FP8/NVFP4 Qwen3 4B text encoder, VAE and tokenizer under Apache-2.0. The runtime package and these model files require separate approval.")
	plan.Title = "FLUX.2 Klein 4B compact"
	return plan
}

// PyTorchFullModelPlan is FLUX.2 Klein 4B at full precision (16.13 GB), the
// PyTorch starter model up to 2026-11-09. Its identity and journal are kept,
// so a prepared copy stays prepared.
func PyTorchFullModelPlan() ModelPlan {
	// The name is identity (the plan ID hashes it), never shown; Title is.
	plan := pyTorchModelPlanFor(torch.Flux2Klein4BTorch, "FLUX.2 Klein on PyTorch (Windows x64, Strix Halo)",
		"https://huggingface.co/black-forest-labs/FLUX.2-klein-4B",
		"Black Forest Labs and Qwen publish these model and tokenizer files under Apache-2.0. The runtime package and these model files require separate approval.")
	plan.Title = "FLUX.2 Klein 4B full precision"
	return plan
}

// torchRepoPaths are the source-repository paths of PyTorch set files that
// Setup stages under another path, keyed by the staged path: the FP8
// transformer lies at its repository's root and is staged with ComfyUI's
// other diffusion models.
var torchRepoPaths = map[string]string{
	"split_files/diffusion_models/flux-2-klein-4b-fp8.safetensors": "flux-2-klein-4b-fp8.safetensors",
}

// pyTorchModelPlanFor projects one provider-owned fixed set, including the
// tokenizer files. Runtime inputs are approved in the separate package plan.
// Each file is staged at its final locator, below split_files/<category>
// for ComfyUI's loaders; Title is set after the identity, so it names the
// plan without changing a recorded approval.
func pyTorchModelPlanFor(set torch.TorchModelSet, name, source, licenseText string) ModelPlan {
	plan := ModelPlan{Engine: PyTorch, Model: set.Name, Name: name, Source: source, Licenses: []string{bf16License}, LicenseText: licenseText}
	for _, file := range set.Files {
		path := file.Locators[len(file.Locators)-1]
		repoPath := path
		if p, ok := torchRepoPaths[path]; ok {
			repoPath = p
		}
		if file.Role == "flux-transformer" {
			plan.Repository, plan.Revision = strings.TrimPrefix(file.Source, "https://huggingface.co/"), file.Revision
		}
		plan.Assets = append(plan.Assets, ModelAsset{ID: file.Role, Role: file.Role, Path: path,
			License: file.License, LicenseURL: bf16LicenseURL,
			Download: PinnedDownload{URL: strings.TrimSuffix(file.Source, "/") + "/resolve/" + file.Revision + "/" + repoPath,
				SHA256: file.SHA256, Bytes: file.Size, Label: file.Name}})
		plan.DownloadBytes += file.Size
	}
	plan.PeakBytes = plan.DownloadBytes + bf16DiskHeadroom
	encoded, _ := json.Marshal(plan)
	id := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(id[:])
	return plan
}

// The native archive offer stages Z-Image-Turbo in the shared model layout.
var retainedArchiveModelOffer = pictureOfferRecipe{plan: ZImageComfyModelPlan, caption: "Photorealistic", stagedOnly: true}

// The model-paths record exposes
// those files to the native archive loaders. The PyTorch set uses the prepared
// Klein tokenizer. The retained archive registration supplies the engine key
// for the recorded approval and journal. Beside the full-precision Klein it
// adds 12.65 GB, since both read the same Qwen3 4B text encoder.
func ZImageComfyModelPlan() ModelPlan {
	m, ok := reviewedPictureModel(zImageOffer)
	if !ok {
		return ModelPlan{}
	}
	plan := ModelPlan{Engine: AppID(retainedArchivePackage.Registration), Model: "z-image-turbo", Name: "Z-Image-Turbo for " + nativeArchiveAppTitle, Source: m.source, LicenseText: m.licenseText,
		Repository: m.files[0].repository, Revision: m.files[0].revision, Licenses: []string{bf16License}}
	for _, f := range m.files {
		plan.Assets = append(plan.Assets, ModelAsset{ID: f.id, Role: f.id, Path: f.path, License: bf16License, LicenseURL: bf16LicenseURL,
			Download: PinnedDownload{URL: "https://huggingface.co/" + f.repository + "/resolve/" + f.revision + "/" + f.path, SHA256: f.sha256, Bytes: f.bytes, Label: f.label}})
		plan.DownloadBytes += f.bytes
	}
	plan.PeakBytes = plan.DownloadBytes + bf16DiskHeadroom
	encoded, _ := json.Marshal(plan)
	id := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(id[:])
	plan.Title = "Z-Image-Turbo"
	return plan
}

type ModelPlan struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	// Engine binds this review and journal identity to one fixed provider.
	// Empty keeps the existing starter and offered model identities; their
	// engine is PictureEngine's.
	Engine AppID `json:"engine,omitempty"`
	// Name, Source and LicenseText describe an offered model for review: its
	// title, its model card and one sentence on its licence terms. The
	// starter plan leaves them empty, which keeps its ID.
	Name string `json:"name,omitempty"`
	// Title is the short name a toggle shows. It is set after the plan's
	// identity is computed, so it changes no recorded approval.
	Title         string   `json:"title,omitempty"`
	Source        string   `json:"source,omitempty"`
	LicenseText   string   `json:"license_text,omitempty"`
	Repository    string   `json:"repository"`
	Revision      string   `json:"revision"`
	Licenses      []string `json:"licenses"`
	DownloadBytes int64    `json:"download_bytes"`
	// Each download is one file: the job service hard-links its result into
	// the provider's download folder and the provider publishes it by rename
	// (job CONTRACT.md JOB-A17). The backend also expands privately before the
	// archive is freed.
	PeakBytes int64        `json:"peak_bytes"`
	Assets    []ModelAsset `json:"assets"`
	// EngineArgs are the llama-server arguments the plan's model is hosted
	// with, from ModelHost's reviewed list (MODELHOST-L5). They change no
	// download, so they stay out of the plan's identity and journals.
	EngineArgs []string `json:"-"`
}

// PictureEngine is the engine choice the plan's model is prepared in: its
// Engine, or the starter's for a plan that names none.
func (p ModelPlan) PictureEngine() AppID {
	if p.Engine != "" {
		return p.Engine
	}
	return Lemonade
}

// TheNoiseModelPlan reuses the reviewed Klein files. The engine archive is
// approved through the package plan, separately from these model files.
func TheNoiseModelPlan() ModelPlan {
	plan := ApprovedBF16Plan()
	if plan.ID == "" {
		return plan
	}
	plan.ID = ""
	plan.Engine, plan.Model = TheNoise, "flux_klein"
	plan.Name = "FLUX.2 Klein on TheNoise (processor)"
	plan.Assets = plan.Assets[1:]
	plan.Licenses = []string{bf16License}
	plan.DownloadBytes = 0
	for _, asset := range plan.Assets {
		plan.DownloadBytes += asset.Download.Bytes
	}
	plan.PeakBytes = plan.DownloadBytes + bf16DiskHeadroom
	encoded, _ := json.Marshal(plan)
	id := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(id[:])
	return plan
}

const pictureBackendAssetID = "backend"

func ApprovedBF16Plan() ModelPlan {
	m, ok := reviewedPictureModel(pictureOfferSpec{BF16ModelID, [3]string{"FLUX.2 Klein BF16 main weights", "FLUX.2 Klein text encoder", "FLUX.2 Klein VAE"}})
	if !ok || m.files[0].revision != bf16Revision {
		return ModelPlan{}
	}
	assets := []ModelAsset{
		{ID: pictureBackendAssetID, Path: "sd-master-462d675-bin-win-vulkan-x64.zip", License: backendLicense, LicenseURL: backendLicenseURL, Download: PinnedDownload{URL: "https://github.com/leejet/stable-diffusion.cpp/releases/download/master-843-462d675/sd-master-462d675-bin-win-vulkan-x64.zip", SHA256: "b9248856707de2e125538ede8a7dc71a9d800bba67e469e60881dae2a5a1c0a2", Bytes: 38865230, Label: "Reviewed stable-diffusion.cpp Vulkan backend"}},
	}
	for _, file := range m.files {
		stage, ok := strings.CutPrefix(file.path, "split_files/")
		if !ok || file.repository != m.files[0].repository || file.revision != bf16Revision {
			return ModelPlan{}
		}
		assets = append(assets, ModelAsset{ID: file.id, Path: stage, License: bf16License, LicenseURL: bf16LicenseURL,
			Download: PinnedDownload{URL: "https://huggingface.co/" + file.repository + "/resolve/" + file.revision + "/" + file.path,
				SHA256: file.sha256, Bytes: file.bytes, Label: file.label}})
	}
	plan := ModelPlan{Model: BF16ModelID, Repository: m.files[0].repository, Revision: bf16Revision,
		Licenses: []string{bf16License, backendLicense}, Assets: assets}
	for _, asset := range assets {
		plan.DownloadBytes += asset.Download.Bytes
	}
	// Installed model journals name the starter plan's ID, and releases up to
	// 0.3.0 hashed a peak of three download copies into it. The ID hashes that
	// recorded peak, so one asset set keeps one ID; the plan states the peak
	// the disk check uses.
	plan.PeakBytes = 3*plan.DownloadBytes + backendExpandedBytes + bf16DiskHeadroom
	encoded, _ := json.Marshal(plan)
	id := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(id[:])
	plan.PeakBytes = plan.DownloadBytes + backendExpandedBytes + bf16DiskHeadroom
	return plan
}

// pictureModelFile is one pinned weight file of an offered picture model.
type pictureModelFile struct {
	id, label, repository, revision, path, sha256 string
	bytes                                         int64
}

// A Setup offer keeps its reviewed model ID, file roles and display labels.
// The installed provider owns the source pins and the exact licence copy.
type pictureModel struct {
	id, name, source, licenseText string
	files                         []pictureModelFile
}

func reviewedPictureModel(offer pictureOfferSpec) (pictureModel, bool) {
	model, ok := imageprovider.FindImageModel(offer.id)
	if !ok || len(model.Files) != len(offer.labels) {
		return pictureModel{}, false
	}
	roles := [3]string{"main", "text_encoder", "vae"}
	assetIDs := [3]string{"main", "encoder", "vae"}
	m := pictureModel{id: model.ID, name: model.Title, source: model.Source, licenseText: model.License.Text}
	for i, file := range model.Files {
		if file.Role != roles[i] {
			return pictureModel{}, false
		}
		m.files = append(m.files, pictureModelFile{id: assetIDs[i], label: offer.labels[i], repository: file.Repository,
			revision: file.Revision, path: file.Path, sha256: file.SHA256, bytes: file.Bytes})
	}
	return m, true
}

type pictureOfferSpec struct {
	id     string
	labels [3]string
}

var zImageOffer = pictureOfferSpec{
	id:     "user.Z-Image-Turbo",
	labels: [3]string{"Z-Image-Turbo BF16 main weights", "Z-Image-Turbo text encoder", "Z-Image-Turbo VAE"},
}

var pictureOffers = []pictureOfferSpec{
	zImageOffer,
	{"user.Qwen-Image-2512", [3]string{"Qwen-Image-2512 Q5_0 main weights", "Qwen2.5-VL-7B-Instruct text encoder", "Qwen-Image VAE"}},
}

// PictureModelChoices are the reviewed plans of the picture models offered
// beside the starter model, for Setup to show with their sizes and licences.
// Each runs on the starter plan's stable-diffusion.cpp backend. Setup offers
// them once the starter model is prepared and stages each through the
// provider's model add verb.
func PictureModelChoices() []ModelPlan {
	plans := make([]ModelPlan, 0, len(pictureOffers))
	for _, offer := range pictureOffers {
		m, ok := reviewedPictureModel(offer)
		if !ok {
			continue
		}
		plan := ModelPlan{Model: m.id, Name: m.name, Source: m.source, LicenseText: m.licenseText,
			Repository: m.files[0].repository, Revision: m.files[0].revision, Licenses: []string{bf16License}}
		for _, f := range m.files {
			plan.Assets = append(plan.Assets, ModelAsset{ID: f.id, Path: f.path, License: bf16License, LicenseURL: bf16LicenseURL,
				Download: PinnedDownload{URL: "https://huggingface.co/" + f.repository + "/resolve/" + f.revision + "/" + f.path, SHA256: f.sha256, Bytes: f.bytes, Label: f.label}})
			plan.DownloadBytes += f.bytes
		}
		plan.PeakBytes = plan.DownloadBytes + bf16DiskHeadroom
		encoded, _ := json.Marshal(plan)
		id := sha256.Sum256(encoded)
		plan.ID = hex.EncodeToString(id[:])
		plans = append(plans, plan)
	}
	return plans
}

// CodingModelPlan is the base coding model retained in first-run/ENGINES.md
// and the 2026-10-03 rank-one benchmark. License classification is source
// attributed; no license-body digest or installed speed verdict is asserted.
func CodingModelPlan() ModelPlan {
	const repo = "unsloth/Qwen3.6-35B-A3B-MTP-GGUF"
	const revision = "5bc3e238d916f48a861bac2f8a1990a0e9b7e98d"
	const file = "Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf"
	const size int64 = 22853663008
	p := ModelPlan{Model: "Qwen3.6-35B-A3B", Name: "Recommended coding model", Repository: repo, Revision: revision,
		Source: "https://huggingface.co/" + repo, LicenseText: "Retained first-run source records classify this model as Apache-2.0. Download approval does not approve running it or establish installed performance.",
		Licenses: []string{"Apache-2.0"}, DownloadBytes: size, PeakBytes: size + bf16DiskHeadroom,
		Assets: []ModelAsset{{ID: "coding", Role: "coding", Path: file, License: "Apache-2.0", LicenseURL: bf16LicenseURL,
			Download: PinnedDownload{URL: "https://huggingface.co/" + repo + "/resolve/" + revision + "/" + file,
				SHA256: "55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1", Bytes: size, Label: "Recommended coding model"}}}}
	encoded, _ := json.Marshal(p)
	sum := sha256.Sum256(encoded)
	p.ID = hex.EncodeToString(sum[:])
	// Set after the identity, as the picture plans' titles are.
	p.Title = "Qwen3.6 35B"
	return p
}
