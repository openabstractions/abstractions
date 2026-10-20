package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
)

const (
	BF16ModelID                = "user.LocalApprovedKlein"
	bf16Revision               = "5f526678002e43af5551dadb73ce2e8c91b43afe"
	bf16Repository             = "Comfy-Org/vae-text-encorder-for-flux-klein-4b"
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

// PyTorchModelPlan projects the provider-owned fixed set, including the
// tokenizer files. Runtime inputs are approved in the separate package plan.
func PyTorchModelPlan() ModelPlan {
	set := torch.Flux2Klein4BTorch
	plan := ModelPlan{Engine: PyTorch, Model: set.Name, Name: "FLUX.2 Klein on PyTorch (Windows x64, Strix Halo)",
		Source: "https://huggingface.co/black-forest-labs/FLUX.2-klein-4B", Repository: bf16Repository, Revision: bf16Revision,
		Licenses: []string{bf16License}, LicenseText: "Black Forest Labs and Qwen publish these model and tokenizer files under Apache-2.0. The runtime package and these model files require separate approval."}
	for _, file := range set.Files {
		// The provider's final locator is the pinned source-repository path.
		path := file.Locators[len(file.Locators)-1]
		plan.Assets = append(plan.Assets, ModelAsset{ID: file.Role, Role: file.Role, Path: path,
			License: file.License, LicenseURL: bf16LicenseURL,
			Download: PinnedDownload{URL: strings.TrimSuffix(file.Source, "/") + "/resolve/" + file.Revision + "/" + path,
				SHA256: file.SHA256, Bytes: file.Size, Label: file.Name}})
		plan.DownloadBytes += file.Size
	}
	plan.PeakBytes = plan.DownloadBytes + bf16DiskHeadroom
	encoded, _ := json.Marshal(plan)
	id := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(id[:])
	return plan
}

type ModelPlan struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	// Engine binds this review and journal identity to one fixed provider.
	// Empty keeps the existing Lemonade starter and offered model identities.
	Engine AppID `json:"engine,omitempty"`
	// Name, Source and LicenseText describe an offered model for review: its
	// title, its model card and one sentence on its licence terms. The
	// starter plan leaves them empty, which keeps its ID.
	Name          string   `json:"name,omitempty"`
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
}

// TheNoiseModelPlan reuses the reviewed Klein files. The engine archive is
// approved through the package plan, separately from these model files.
func TheNoiseModelPlan() ModelPlan {
	plan := ApprovedBF16Plan()
	plan.ID = ""
	plan.Engine, plan.Model = TheNoise, "flux_klein"
	plan.Name = "FLUX.2 Klein on TheNoise (CPU; GPU qualification pending)"
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

func ApprovedBF16Plan() ModelPlan {
	const prefix = "https://huggingface.co/" + bf16Repository + "/resolve/" + bf16Revision + "/split_files/"
	assets := []ModelAsset{
		{ID: "backend", Path: "sd-master-462d675-bin-win-vulkan-x64.zip", License: backendLicense, LicenseURL: backendLicenseURL, Download: PinnedDownload{URL: "https://github.com/leejet/stable-diffusion.cpp/releases/download/master-843-462d675/sd-master-462d675-bin-win-vulkan-x64.zip", SHA256: "b9248856707de2e125538ede8a7dc71a9d800bba67e469e60881dae2a5a1c0a2", Bytes: 38865230, Label: "Reviewed stable-diffusion.cpp Vulkan backend"}},
		{ID: "main", Path: "diffusion_models/flux-2-klein-4b.safetensors", License: bf16License, LicenseURL: bf16LicenseURL, Download: PinnedDownload{URL: prefix + "diffusion_models/flux-2-klein-4b.safetensors", SHA256: "ec3d4e733a771f61c052fb4856c48b336c55eaf2c65487c2a1faeb9bbda7a343", Bytes: 7751105712, Label: "FLUX.2 Klein BF16 main weights"}},
		{ID: "encoder", Path: "text_encoders/qwen_3_4b.safetensors", License: bf16License, LicenseURL: bf16LicenseURL, Download: PinnedDownload{URL: prefix + "text_encoders/qwen_3_4b.safetensors", SHA256: "6c671498573ac2f7a5501502ccce8d2b08ea6ca2f661c458e708f36b36edfc5a", Bytes: 8044982048, Label: "FLUX.2 Klein text encoder"}},
		{ID: "vae", Path: "vae/flux2-vae.safetensors", License: bf16License, LicenseURL: bf16LicenseURL, Download: PinnedDownload{URL: prefix + "vae/flux2-vae.safetensors", SHA256: "868fe7b343cc8f3a19dbcfcafbc3d5f888802be3f89bd81b65b3621a066ce8f3", Bytes: 336211292, Label: "FLUX.2 Klein VAE"}},
	}
	plan := ModelPlan{Model: BF16ModelID, Repository: bf16Repository, Revision: bf16Revision,
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

// pictureModel is one picture model the Lemonade provider declares beyond the
// starter model: the same pins, licence and card as its packaging manifest
// (declared_models) and server.ImageModels.
type pictureModel struct {
	id, name, source, licenseText string
	files                         []pictureModelFile
}

var pictureModels = []pictureModel{
	{id: "user.Z-Image-Turbo", name: "Z-Image-Turbo", source: "https://huggingface.co/Tongyi-MAI/Z-Image-Turbo",
		licenseText: "Tongyi-MAI publishes Z-Image-Turbo, Black Forest Labs the FLUX.1 autoencoder and Qwen the Qwen3-4B text encoder, each under the Apache License 2.0; Comfy-Org repackages the three files. The licence permits commercial use, changes and redistribution with the licence and notices kept.",
		files: []pictureModelFile{
			{id: "main", label: "Z-Image-Turbo BF16 main weights", repository: "Comfy-Org/z_image_turbo", revision: "6fc90a3b1b653e935a0d175e260736de25b84df5", path: "split_files/diffusion_models/z_image_turbo_bf16.safetensors", bytes: 12309866400, sha256: "2407613050b809ffdff18a4ac99af83ea6b95443ecebdf80e064a79c825574a6"},
			{id: "encoder", label: "Z-Image-Turbo text encoder", repository: "Comfy-Org/z_image_turbo", revision: "6fc90a3b1b653e935a0d175e260736de25b84df5", path: "split_files/text_encoders/qwen_3_4b.safetensors", bytes: 8044982048, sha256: "6c671498573ac2f7a5501502ccce8d2b08ea6ca2f661c458e708f36b36edfc5a"},
			{id: "vae", label: "Z-Image-Turbo VAE", repository: "Comfy-Org/z_image_turbo", revision: "6fc90a3b1b653e935a0d175e260736de25b84df5", path: "split_files/vae/ae.safetensors", bytes: 335304388, sha256: "afc8e28272cd15db3919bacdb6918ce9c1ed22e96cb12c4d5ed0fba823529e38"},
		}},
	{id: "user.Qwen-Image-2512", name: "Qwen-Image-2512", source: "https://huggingface.co/Qwen/Qwen-Image-2512",
		licenseText: "Qwen publishes Qwen-Image-2512, its autoencoder and the Qwen2.5-VL-7B-Instruct text encoder under the Apache License 2.0; Unsloth quantises the model and the encoder to GGUF, and Comfy-Org repackages the autoencoder. The licence permits commercial use, changes and redistribution with the licence and notices kept.",
		files: []pictureModelFile{
			{id: "main", label: "Qwen-Image-2512 Q5_0 main weights", repository: "unsloth/Qwen-Image-2512-GGUF", revision: "1626d7531f84b4d2ea1cd6d2e69f41ec027dd354", path: "qwen-image-2512-Q5_0.gguf", bytes: 14400813600, sha256: "c077d5a3f867b6441235d071d76912ce6575db9832651e8b1769e8329d33d798"},
			{id: "encoder", label: "Qwen2.5-VL-7B-Instruct text encoder", repository: "unsloth/Qwen2.5-VL-7B-Instruct-GGUF", revision: "68bb8bc4b7df5289c143aaec0ab477a7d4051aab", path: "Qwen2.5-VL-7B-Instruct-UD-Q4_K_XL.gguf", bytes: 4785159040, sha256: "8edd10e3ae170ab8b8d114e70763ff63a220f16deec9a56acf9330034efc0bb5"},
			{id: "vae", label: "Qwen-Image VAE", repository: "Comfy-Org/Qwen-Image_ComfyUI", revision: "1f12b17be14c89b026c51a91d67c32f84bb047bc", path: "split_files/vae/qwen_image_vae.safetensors", bytes: 253806246, sha256: "a70580f0213e67967ee9c95f05bb400e8fb08307e017a924bf3441223e023d1f"},
		}},
}

// PictureModelChoices are the reviewed plans of the picture models offered
// beside the starter model, for Setup to show with their sizes and licences.
// Each runs on the starter plan's stable-diffusion.cpp backend. Setup offers
// them once the starter model is prepared and stages each through the
// provider's model add verb.
func PictureModelChoices() []ModelPlan {
	plans := make([]ModelPlan, 0, len(pictureModels))
	for _, m := range pictureModels {
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
