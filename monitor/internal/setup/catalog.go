// Package setup plans the Panel's fixed beginner installation choices.
// Download work belongs to the public OA job service; this package owns only
// the application installation intent and its effect journal.
package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strings"

	thenoise "github.com/openabstractions/abstraction-provider-thenoise/go"
)

type AppID string

// The Get started page's assistant row plans this one component, its seam:
// the app installs through its own official installer and the OA plugin (its
// adopter folder's official-install.js finds that route), and the download
// starts only on the person's Install click. Until Setup has a closed action
// handler for the route the component stays Pending, so a plan with it is
// reviewable and not preparable. OA builds and pins no package for it.
const OpenCodeOfficialComponent = "opencode-official"

// testChoices and testPackageKinds hold the neutral fixture choice and
// package kind this package's tests add (setup_test.go). The release catalog
// has neither: Validate accepts a choice past the fixed ones only from
// testChoices, and packageHealth checks a kind outside its own list only
// from testPackageKinds.
var (
	testChoices      = map[AppID]bool{}
	testPackageKinds = map[string]func(Artifact, string) error{}
)

const (
	ComfyUI  AppID = "comfyui"
	OpenCode AppID = "opencode"
	Lemonade AppID = "lemonade"
	TheNoise AppID = "thenoise"
	PyTorch  AppID = "pytorch"
)

// Artifact is a release-owned pin. A web request may select an AppID, never
// provide any of these fields or an executable command.
type Artifact struct {
	Version         string `json:"version"`
	SourceCommit    string `json:"source_commit"`
	SourceURL       string `json:"source_url"`
	Platform        string `json:"platform"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
	Bytes           int64  `json:"bytes"`
	PeakBytes       int64  `json:"peak_bytes"`
	License         string `json:"license"`
	Publisher       string `json:"publisher"`
	Signature       string `json:"signature"`
	InstallIdentity string `json:"install_identity"`
	Action          string `json:"action"` // closed handler name, never a command line
	// SourceArchive* pin the corresponding-source ZIP an OA-built adopter
	// package is published with. The package's health check compares its own
	// provenance record with this pin.
	SourceArchiveURL    string `json:"source_archive_url,omitempty"`
	SourceArchiveSHA256 string `json:"source_archive_sha256,omitempty"`
	SourceArchiveBytes  int64  `json:"source_archive_bytes,omitempty"`
	// RecordsSHA256 is a ComfyUI package's private-Python RECORD-set digest,
	// private_python_records_sha256 in its OA-PROVENANCE.json. The activation
	// recipe passes it to the launcher as --records.
	RecordsSHA256 string `json:"records_sha256,omitempty"`
	// TestedBackendSHA256 and TestedModelRevision name the image backend
	// archive and model revision an engine package was tested with.
	TestedBackendSHA256 string `json:"tested_backend_sha256,omitempty"`
	TestedModelRevision string `json:"tested_model_revision,omitempty"`
}

type Component struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Dependencies []string  `json:"dependencies,omitempty"`
	Artifact     *Artifact `json:"artifact,omitempty"`
	Pending      string    `json:"pending,omitempty"`
}

type Choice struct {
	ID         AppID    `json:"id"`
	Title      string   `json:"title"`
	Components []string `json:"components"`
}

type Catalog struct {
	Choices    []Choice    `json:"choices"`
	Components []Component `json:"components"`
	// PreviousInstallPins are exact artifacts carried forward by a release so
	// already installed app trees remain recognizable. They are never included
	// in a new plan or accepted as authority for a download or installation.
	PreviousInstallPins []KnownInstallPin `json:"-"`
}

type KnownInstallPin struct {
	ComponentID string   `json:"component_id"`
	Artifact    Artifact `json:"artifact"`
}

// NewCatalog is intentionally closed. Release tooling replaces Pending with
// exact artifact pins after each adopter package has been built and reviewed.
func NewCatalog() Catalog {
	return catalogForTarget(runtime.GOOS, runtime.GOARCH)
}

func catalogForTarget(goos, goarch string) Catalog {
	c := Catalog{
		Choices: []Choice{
			{ID: ComfyUI, Title: "Create pictures", Components: []string{"comfyui-app", "lemonade-engine"}},
			{ID: OpenCode, Title: "Use an assistant", Components: []string{OpenCodeOfficialComponent}},
			{ID: Lemonade, Title: "Run a local image engine", Components: []string{"lemonade-engine"}},
			{ID: TheNoise, Title: "Run TheNoise pictures locally (CPU)", Components: []string{"thenoise-engine"}},
			{ID: PyTorch, Title: "Run PyTorch pictures locally (Strix Halo)", Components: []string{"pytorch-engine"}},
		},
		Components: []Component{
			{ID: "comfyui-app", Title: "ComfyUI with OpenAbstractions image tools", Pending: "A reviewed Windows application package is required"},
			{ID: "lemonade-engine", Title: "Lemonade local image engine", Pending: "A reviewed Lemonade image-engine package for this operating system and processor is required"},
			{ID: "thenoise-engine", Title: "TheNoise local image engine (CPU qualified; GPU pending)", Pending: "The reviewed TheNoise engine requires Windows x64"},
			{ID: OpenCodeOfficialComponent, Title: "OpenCode from its official installer", Pending: "Setup does not run OpenCode's official installer yet"},
			{ID: "pytorch-engine", Title: "PyTorch 2.11.0 with ROCm 7.13.0 and CPython 3.12.10 (Windows x64, gfx1151)", Pending: "A reviewed runtime input bundle and genuine native producer receipt are required"},
		},
	}
	if goos == "windows" && goarch == "amd64" {
		c.Components[2].Pending = ""
		pin := theNoiseArtifact()
		c.Components[2].Artifact = &pin
		c.Components[1].Pending = ""
		c.Components[1].Artifact = &Artifact{
			Version: "v2026.39.1", SourceCommit: "6ce82052b69c5ffd9157ed102b739d180cdc24a5",
			SourceURL: "https://github.com/lemonade-sdk/lemonade/commit/6ce82052b69c5ffd9157ed102b739d180cdc24a5",
			Platform:  "windows/amd64",
			URL:       "https://github.com/lemonade-sdk/lemonade/releases/download/v2026.39.1/lemonade-embeddable-2026.39.1-windows-x64.zip",
			SHA256:    "235c2361be3a9729a06b92c97e40a9530ebb8e5088bd3334524472d6da7fbbca",
			Bytes:     5357376, PeakBytes: 30000000, License: "Apache-2.0", Publisher: "lemonade-sdk",
			Signature: "unsigned", InstallIdentity: "lemonade", Action: "archive_per_user",
			// The provider qualification ran this package with the reviewed
			// stable-diffusion.cpp Vulkan backend and FLUX.2 Klein revision.
			TestedBackendSHA256: "b9248856707de2e125538ede8a7dc71a9d800bba67e469e60881dae2a5a1c0a2",
			TestedModelRevision: "5f526678002e43af5551dadb73ce2e8c91b43afe",
		}
	}
	if err := projectAdopterAssets(&c, goos, goarch, adopterAssetsJSON); err != nil {
		panic("invalid embedded adopter asset pins: " + err.Error())
	}
	previous, err := previousInstallPins(c, goos, goarch, installPinsJSON)
	if err != nil {
		panic("invalid embedded install pin record: " + err.Error())
	}
	c.PreviousInstallPins = previous
	return c
}

type PlannedComponent struct {
	Component
	Ready bool `json:"ready"`
}

type Plan struct {
	ID            string             `json:"id"`
	CatalogSHA256 string             `json:"catalog_sha256"`
	Selected      []AppID            `json:"selected"`
	Components    []PlannedComponent `json:"components"`
	DownloadBytes int64              `json:"download_bytes"`
	PeakBytes     int64              `json:"peak_bytes"`
	Licenses      []string           `json:"licenses"`
	Blockers      []string           `json:"blockers"`
	Configuration []Requirement      `json:"configuration"`
	Preparable    bool               `json:"preparable"`
	Ready         bool               `json:"ready"`
}

// Requirements are completed through public OA IPC and app-owned settings,
// after the necessary packages are installed. No model or provider is guessed.
type Requirement struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (c Catalog) Plan(selected []AppID) (Plan, error) {
	if err := c.Validate(); err != nil {
		return Plan{}, err
	}
	known := make(map[AppID]Choice, len(c.Choices))
	for _, choice := range c.Choices {
		known[choice.ID] = choice
	}
	wanted := make(map[AppID]bool, len(selected))
	for _, id := range selected {
		if _, ok := known[id]; !ok {
			return Plan{}, fmt.Errorf("unknown setup choice %q", id)
		}
		wanted[id] = true
	}
	if len(wanted) == 0 {
		return Plan{}, errors.New("select at least one setup choice")
	}
	ids := make([]AppID, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	componentByID := make(map[string]Component, len(c.Components))
	for _, component := range c.Components {
		componentByID[component.ID] = component
	}
	needed := map[string]bool{}
	var include func(string)
	include = func(id string) {
		if needed[id] {
			return
		}
		needed[id] = true
		for _, dependency := range componentByID[id].Dependencies {
			include(dependency)
		}
	}
	for _, id := range ids {
		for _, component := range known[id].Components {
			include(component)
		}
	}
	encoded, _ := json.Marshal(c) // Validate guarantees a serializable catalog.
	checksum := sha256.Sum256(encoded)
	plan := Plan{CatalogSHA256: hex.EncodeToString(checksum[:]), Selected: ids, Licenses: []string{}, Blockers: []string{}}
	licenseSet := map[string]bool{}
	for _, component := range c.Components { // catalog order is dependency order
		if !needed[component.ID] {
			continue
		}
		ready := component.Artifact != nil
		plan.Components = append(plan.Components, PlannedComponent{Component: component, Ready: ready})
		if !ready {
			plan.Blockers = append(plan.Blockers, component.ID+": "+component.Pending)
			continue
		}
		plan.DownloadBytes += component.Artifact.Bytes
		plan.PeakBytes += component.Artifact.PeakBytes
		if !licenseSet[component.Artifact.License] {
			licenseSet[component.Artifact.License] = true
			plan.Licenses = append(plan.Licenses, component.Artifact.License)
		}
	}
	key, _ := json.Marshal(struct {
		Catalog  string  `json:"catalog"`
		Selected []AppID `json:"selected"`
	}{plan.CatalogSHA256, ids})
	planID := sha256.Sum256(key)
	plan.ID = hex.EncodeToString(planID[:])
	if wanted[ComfyUI] {
		plan.Configuration = append(plan.Configuration, Requirement{ID: "comfyui-image-policy", Title: "Choose an image model and save the ComfyUI image policy"})
	}
	if needed["lemonade-engine"] {
		plan.Configuration = append(plan.Configuration, Requirement{ID: "lemonade-model", Title: "Download and verify an image model, then configure Lemonade for local pictures"})
	}
	if needed["thenoise-engine"] {
		plan.Configuration = append(plan.Configuration, Requirement{ID: "thenoise-model", Title: "Review the separate picture model and configure TheNoise for CPU pictures"})
	}
	if needed["pytorch-engine"] {
		plan.Configuration = append(plan.Configuration, Requirement{ID: "pytorch-model", Title: "Review the separate picture model and configure PyTorch on Strix Halo"})
	}
	plan.Preparable = len(plan.Blockers) == 0
	// A plan alone never proves packages installed or app configuration done.
	plan.Ready = false
	return plan, nil
}

func (c Catalog) Validate() error {
	fixed := []AppID{ComfyUI, OpenCode, Lemonade, TheNoise, PyTorch}
	if len(c.Choices) < len(fixed) {
		return errors.New("setup catalog must contain the fixed app and engine choices")
	}
	for i, choice := range c.Choices {
		if i < len(fixed) && choice.ID != fixed[i] || i >= len(fixed) && !testChoices[choice.ID] {
			return errors.New("setup catalog must contain the fixed app and engine choices")
		}
	}
	seen := map[string]bool{}
	for _, component := range c.Components {
		if component.ID == "" || seen[component.ID] {
			return fmt.Errorf("missing or duplicate component %q", component.ID)
		}
		seen[component.ID] = true
		if (component.Artifact == nil) == (component.Pending == "") {
			return fmt.Errorf("component %q needs exactly one artifact or pending reason", component.ID)
		}
		if component.Artifact != nil {
			if err := component.Artifact.validate(); err != nil {
				return fmt.Errorf("component %q: %w", component.ID, err)
			}
			if err := packageRequirements(component.ID, *component.Artifact); err != nil {
				return fmt.Errorf("component %q: %w", component.ID, err)
			}
		}
	}
	for _, component := range c.Components {
		for _, dependency := range component.Dependencies {
			if !seen[dependency] || dependency == component.ID {
				return fmt.Errorf("component %q has invalid dependency %q", component.ID, dependency)
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	byID := make(map[string]Component, len(c.Components))
	for _, component := range c.Components {
		byID[component.ID] = component
	}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("component dependency cycle at %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range byID[id].Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for _, component := range c.Components {
		if err := visit(component.ID); err != nil {
			return err
		}
	}
	for _, choice := range c.Choices {
		if len(choice.Components) == 0 {
			return fmt.Errorf("choice %q has no components", choice.ID)
		}
		for _, component := range choice.Components {
			if !seen[component] {
				return fmt.Errorf("choice %q has unknown component %q", choice.ID, component)
			}
		}
	}
	for _, pin := range c.PreviousInstallPins {
		if !seen[pin.ComponentID] || pin.Artifact.Action != "archive_per_user" {
			return fmt.Errorf("unknown historical app pin %q", pin.ComponentID)
		}
		if err := pin.Artifact.validate(); err != nil {
			return fmt.Errorf("historical app pin %q: %w", pin.ComponentID, err)
		}
	}
	return nil
}

// knownInstallArtifact recognizes a release-owned pin only for inspecting an
// existing installation. Execute paths continue to use Journal.Authorize.
func (c Catalog) knownInstallArtifact(componentID string, artifact Artifact) bool {
	for _, component := range c.Components {
		if component.ID == componentID && component.Artifact != nil && *component.Artifact == artifact {
			return true
		}
	}
	for _, pin := range c.PreviousInstallPins {
		if pin.ComponentID == componentID && pin.Artifact == artifact {
			return true
		}
	}
	return false
}

func (a Artifact) validate() error {
	if a.Version == "" || len(a.SourceCommit) != 40 || a.SourceURL == "" || a.Platform == "" || a.Bytes <= 0 || a.PeakBytes < a.Bytes ||
		a.License == "" || a.Publisher == "" || a.Signature == "" || a.InstallIdentity == "" {
		return errors.New("incomplete version, source, size, license, publisher or install identity")
	}
	if _, err := hex.DecodeString(a.SourceCommit); err != nil {
		return errors.New("source commit is not hex")
	}
	source, err := url.Parse(a.SourceURL)
	if err != nil || source.Scheme != "https" || source.Host != "github.com" || source.User != nil ||
		source.RawQuery != "" || source.Fragment != "" ||
		!regexp.MustCompile(`^/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/commit/[0-9a-f]{40}$`).MatchString(source.Path) ||
		!strings.HasSuffix(source.Path, "/commit/"+a.SourceCommit) {
		return errors.New("source URL must bind an immutable GitHub commit matching the source pin")
	}
	if !regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`).MatchString(a.Platform) {
		return errors.New("artifact platform is not a target OS/architecture")
	}
	if len(a.SHA256) != 64 {
		return errors.New("sha256 must be 64 hex characters")
	}
	if _, err := hex.DecodeString(a.SHA256); err != nil {
		return errors.New("sha256 is not hex")
	}
	// The version names a folder: no trailing dot or space, no reserved name.
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(a.InstallIdentity) ||
		!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`).MatchString(a.Version) || !safeMemberPath(a.Version) {
		return errors.New("install identity or version is not a safe path segment")
	}
	parsed, err := url.Parse(a.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!strings.Contains(parsed.Path, "/releases/download/") {
		return errors.New("artifact URL must be a pinned github.com release download")
	}
	switch a.Action {
	case "archive_per_user", "msi_per_user":
	default:
		return fmt.Errorf("unsupported install action %q", a.Action)
	}
	if a.SourceArchiveURL != "" || a.SourceArchiveSHA256 != "" || a.SourceArchiveBytes != 0 {
		archive, err := url.Parse(a.SourceArchiveURL)
		if err != nil || archive.Scheme != "https" || archive.Host != "github.com" || archive.User != nil ||
			archive.RawQuery != "" || archive.Fragment != "" || path.Dir(archive.Path) != path.Dir(parsed.Path) ||
			archive.Path == parsed.Path {
			return errors.New("source archive must be another asset of the package's own release")
		}
		if !sha256Hex.MatchString(a.SourceArchiveSHA256) || a.SourceArchiveBytes <= 0 {
			return errors.New("source archive needs an exact size and SHA-256")
		}
	}
	if a.RecordsSHA256 != "" && !sha256Hex.MatchString(a.RecordsSHA256) {
		return errors.New("RECORD-set pin must be 64 lowercase hex characters")
	}
	if (a.TestedBackendSHA256 == "") != (a.TestedModelRevision == "") ||
		a.TestedBackendSHA256 != "" && (!sha256Hex.MatchString(a.TestedBackendSHA256) || !commitHex.MatchString(a.TestedModelRevision)) {
		return errors.New("tested backend and model pins must both be exact")
	}
	return nil
}

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitHex = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// packageRequirements holds each package kind to the pins its update
// transition checks: an OA-built app is paired with its source archive,
// ComfyUI carries its RECORD-set pin, and the image engine names the backend
// and model it was tested with, which must be the release's model plan.
func packageRequirements(componentID string, a Artifact) error {
	paired := a.SourceArchiveSHA256 != ""
	switch componentID {
	case "comfyui-app":
		if !paired || a.RecordsSHA256 == "" || a.TestedBackendSHA256 != "" {
			return errors.New("ComfyUI package needs its source archive and RECORD-set pins")
		}
	case "lemonade-engine":
		if paired || a.RecordsSHA256 != "" {
			return errors.New("the upstream Lemonade archive carries no OA source or RECORD pin")
		}
		return lemonadePairing(a)
	case "thenoise-engine":
		if a != theNoiseArtifact() {
			return errors.New("TheNoise requires the release's exact reviewed upstream archive")
		}
	case "pytorch-engine":
		if !paired || a.InstallIdentity != "pt" || a.Platform != "windows/amd64" || a.RecordsSHA256 != "" || a.TestedBackendSHA256 != "" {
			return errors.New("PyTorch requires a reviewed Windows x64 input package paired with its corresponding source")
		}
	}
	return nil
}

func theNoiseArtifact() Artifact {
	return Artifact{Version: "0.9.1", SourceCommit: "5e6218c3a546b124a514d32e505a5f1a064d36c9",
		SourceURL: "https://github.com/lemonade-sdk/thenoise/commit/5e6218c3a546b124a514d32e505a5f1a064d36c9", Platform: "windows/amd64",
		URL:    "https://github.com/lemonade-sdk/thenoise/releases/download/thenoise-0.9.1-rocm10.1.0/thenoise-0.9.1-rocm10.1.0-gfx1151-win-x64.zip",
		SHA256: thenoise.EngineZIPSHA256, Bytes: thenoise.EngineZIPBytes, PeakBytes: 7 << 30,
		License: "Apache-2.0 and bundled dependency licenses", Publisher: "lemonade-sdk", Signature: "unsigned", InstallIdentity: "tn", Action: "archive_per_user"}
}

// lemonadePairing refuses a Lemonade package that was not tested with the
// release's reviewed image backend and model revision.
func lemonadePairing(a Artifact) error {
	plan := ApprovedBF16Plan()
	backend := ""
	for _, asset := range plan.Assets {
		if asset.ID == "backend" {
			backend = asset.Download.SHA256
		}
	}
	if a.TestedBackendSHA256 == "" || a.TestedBackendSHA256 != backend || a.TestedModelRevision != plan.Revision {
		return errors.New("this Lemonade package was not tested with the release's image backend and model; refusing an untested pairing")
	}
	return nil
}
