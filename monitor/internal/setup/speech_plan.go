package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Speech model ids, as Lemonade names them and the Lemonade provider
// declares them for transcription@1 and speech@1.
const (
	SpeechTranscriptionModelID = "Whisper-Large-v3-Turbo"
	SpeechVoiceModelID         = "kokoro-v1"
)

const (
	speechMITLicense    = "MIT"
	speechMITURL        = "https://opensource.org/license/mit"
	speechApacheLicense = "Apache-2.0"
	speechApacheURL     = "https://www.apache.org/licenses/LICENSE-2.0"
	// Kokoros links espeak-ng statically, so koko.exe and espeak-ng-data/
	// are GPL-3.0-or-later (research/security-reviews/kokoros-b17-win.md,
	// Licence and condition 11).
	speechGPLLicense = "GPL-3.0-or-later"
	speechGPLURL     = "https://www.gnu.org/licenses/gpl-3.0.html"
	whisperRevision  = "5359861c739e955e79d9a303bcbc70fb988958b1"
	kokoroRevision   = "abf6e2a176872a599e992bd9cf38b370d0eea9e3"
	// speechExpandedBytes is what the backends occupy once staged: the six
	// kept whisper.cpp Vulkan files and the 365 Kokoros files.
	speechExpandedBytes int64 = 724992 + 1365504 + 68608 + 565760 + 780288 + 56746496 + 47584067
	speechDiskHeadroom  int64 = 1 << 30
)

// SpeechConsent is the consent text Setup shows before it downloads the
// speech plan. It names each component, its licence and that Kokoros is
// GPL-3.0-or-later with its source location (Kokoros review condition 11).
const SpeechConsent = "Speech downloads, from their publishers: whisper.cpp v1.8.4 (MIT) and Whisper-Large-v3-Turbo (MIT) to listen; " +
	"Kokoros b17 with espeak-ng (GPL-3.0-or-later) and kokoro-v1 (Apache-2.0) to speak. " +
	"Kokoros comes to this account under the GNU General Public License version 3 or later; its source is at https://github.com/lemonade-sdk/Kokoros."

// SpeechPlan is the reviewed speech download plan: the whisper.cpp and
// Kokoros backends and their models, each pinned by size and SHA-256, with
// the licences the person approves. The Lemonade provider stages the files
// (lemonaded configure --activate-reviewed-package --speech-only).
type SpeechPlan struct {
	ID            string       `json:"id"`
	Models        []string     `json:"models"`
	Licenses      []string     `json:"licenses"`
	Consent       string       `json:"consent"`
	DownloadBytes int64        `json:"download_bytes"`
	PeakBytes     int64        `json:"peak_bytes"`
	Assets        []ModelAsset `json:"assets"`
}

// ApprovedSpeechPlan is the fixed speech plan of
// research/security-reviews/whisper.cpp-v1.8.4-win.md and kokoros-b17-win.md.
// Its asset ids name the provider's staging flags.
func ApprovedSpeechPlan() SpeechPlan {
	const whisperHF = "https://huggingface.co/ggerganov/whisper.cpp/resolve/" + whisperRevision + "/"
	const kokoroHF = "https://huggingface.co/mikkoph/kokoro-onnx/resolve/" + kokoroRevision + "/"
	assets := []ModelAsset{
		{ID: "whisper-backend", Path: "whisper-v1.8.4-windows-vulkan-x64.zip", License: speechMITLicense, LicenseURL: speechMITURL, Download: PinnedDownload{
			URL:    "https://github.com/lemonade-sdk/whisper.cpp-rocm/releases/download/v1.8.4/whisper-v1.8.4-windows-vulkan-x64.zip",
			SHA256: "e0d20a0f92e31b98adc0faf71172efc810b701e6391a9d858ca045bff26f77cd", Bytes: 21203995, Label: "Reviewed whisper.cpp Vulkan backend"}},
		{ID: "kokoro-backend", Path: "kokoros-windows-x86_64.tar.gz", License: speechGPLLicense, LicenseURL: speechGPLURL, Download: PinnedDownload{
			URL:    "https://github.com/lemonade-sdk/Kokoros/releases/download/b17/kokoros-windows-x86_64.tar.gz",
			SHA256: "df403e86f13b0f964a5a2d4b8ab4a1a6749c0613bb67e45825a4a8913b29c4ef", Bytes: 19202614, Label: "Kokoros speech engine, GPL-3.0-or-later"}},
		{ID: "whisper-model", Path: "ggml-large-v3-turbo.bin", License: speechMITLicense, LicenseURL: speechMITURL, Download: PinnedDownload{
			URL:    whisperHF + "ggml-large-v3-turbo.bin",
			SHA256: "1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69", Bytes: 1624555275, Label: "Whisper-Large-v3-Turbo"}},
		{ID: "kokoro-index", Path: "index.json", License: speechApacheLicense, LicenseURL: speechApacheURL, Download: PinnedDownload{
			URL:    kokoroHF + "index.json",
			SHA256: "c2fb8a8173680fed91dba2b15d835e5b90630ded75953f40d23f9cefe76ceb75", Bytes: 96, Label: "kokoro-v1 index"}},
		{ID: "kokoro-model", Path: "kokoro-v1.0.onnx", License: speechApacheLicense, LicenseURL: speechApacheURL, Download: PinnedDownload{
			URL:    kokoroHF + "kokoro-v1.0.onnx",
			SHA256: "7d5df8ecf7d4b1878015a32686053fd0eebe2bc377234608764cc0ef3636a6c5", Bytes: 325532387, Label: "kokoro-v1 model"}},
		{ID: "kokoro-voices", Path: "voices-v1.0.bin", License: speechApacheLicense, LicenseURL: speechApacheURL, Download: PinnedDownload{
			URL:    kokoroHF + "voices-v1.0.bin",
			SHA256: "bca610b8308e8d99f32e6fe4197e7ec01679264efed0cac9140fe9c29f1fbf7d", Bytes: 28214398, Label: "kokoro-v1 voices"}},
	}
	plan := SpeechPlan{Models: []string{SpeechTranscriptionModelID, SpeechVoiceModelID},
		Licenses: []string{speechMITLicense, speechGPLLicense, speechApacheLicense}, Consent: SpeechConsent, Assets: assets}
	for _, asset := range assets {
		plan.DownloadBytes += asset.Download.Bytes
	}
	// Public job storage and the provider's file of each download coexist,
	// and the backends expand before the archives are freed.
	plan.PeakBytes = 2*plan.DownloadBytes + speechExpandedBytes + speechDiskHeadroom
	encoded, _ := json.Marshal(plan)
	id := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(id[:])
	return plan
}
