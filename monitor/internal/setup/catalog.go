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
	"log"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

type AppID string

// The Get started row uses the application's existing Setup bundle route.
// Its app-owned installer combines the official desktop application and the OA plugin
// integration MSI, then supplies the manifest the Panel registers.
// The release producer pins that package chain. This Panel component remains
// Pending because the ordinary installation journal does not run the bundle;
// its plan is reviewable and not preparable.
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
	Version            string                  `json:"version"`
	SourceCommit       string                  `json:"source_commit"`
	SourceURL          string                  `json:"source_url"`
	Platform           string                  `json:"platform"`
	URL                string                  `json:"url"`
	SHA256             string                  `json:"sha256"`
	Bytes              int64                   `json:"bytes"`
	PeakBytes          int64                   `json:"peak_bytes"`
	License            string                  `json:"license"`
	Publisher          string                  `json:"publisher"`
	Signature          string                  `json:"signature"`
	InstallIdentity    string                  `json:"install_identity"`
	ApplicationProgram string                  `json:"application_program,omitempty"`
	ServiceProgram     string                  `json:"service_program,omitempty"`
	ArchiveToolProgram string                  `json:"archive_tool_program,omitempty"`
	NativeResources    *nativeArchiveResources `json:"native_resources,omitempty"`
	Action             string                  `json:"action"` // closed handler name, never a command line
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
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Dependencies []string `json:"dependencies,omitempty"`
	// DependencyVersions names the exact package version a dependency must
	// have: a plan pinning another version is blocked, and the package waits
	// until that version is installed.
	DependencyVersions map[string]string `json:"dependency_versions,omitempty"`
	Artifact           *Artifact         `json:"artifact,omitempty"`
	Pending            string            `json:"pending,omitempty"`
	// Fixup is the adopter fix-up the component's installed app carries,
	// when it has one (CheckFixups). Not part of the catalog digest a plan
	// binds.
	Fixup *Fixup `json:"-"`
}

type Choice struct {
	Application bool     `json:"application,omitempty"`
	ID          AppID    `json:"id"`
	Title       string   `json:"title"`
	Components  []string `json:"components"`
	// Use is what the setup step gives an integrated app registered under
	// ID (UseCode or UseImage); Engines are the picture engine choices a
	// UseImage app's pictures run in, strongest first: the step takes the
	// first that fits this computer and the last everywhere else
	// (Runner.StepPictureEngine). The step reads both (StepChoices) and names
	// no app itself. Neither changes a package plan, so neither is part of
	// the catalog digest a plan binds.
	Use     string  `json:"-"`
	Engines []AppID `json:"-"`
	// Graphics is the graphics family a choice's packages are built for
	// (graphicsFamilies): the step passes such an engine over on a computer
	// without that card, and a plan with such an app is blocked there. Host is
	// the inference host the engine's provider registers, which a picture
	// app's rules name. Neither is part of the catalog digest a plan binds.
	Graphics string `json:"-"`
	Host     string `json:"-"`
	// Official is the official program an integrated app runs from outside
	// its integration's folder, when it has one: the one program its
	// manifest may name outside {app} (OfficialProgramOf). Not part of the
	// catalog digest a plan binds.
	Official *OfficialProgram `json:"-"`
	// Display is the name a person reads, when it is not Title. Title is
	// part of the catalog digest every plan ID hashes, so a choice's title
	// stays byte for byte while its display name changes (a display string
	// never feeds an identity, architect ruling 2026-11-09).
	Display string `json:"-"`
}

// Name is the choice's name for a person: Display, else Title.
func (c Choice) Name() string {
	if c.Display != "" {
		return c.Display
	}
	return c.Title
}

// PictureEngine is the engine this choice's pictures run in when engine is
// the one that fits this computer: engine when the choice lists it, its
// fallback (the last) otherwise, and "" for a choice without pictures.
func (c Choice) PictureEngine(engine AppID) AppID {
	if len(c.Engines) == 0 {
		return ""
	}
	if slices.Contains(c.Engines, engine) {
		return engine
	}
	return c.Engines[len(c.Engines)-1]
}

// OfficialProgram is an integrated app's official program as this compiled
// catalog knows it: its path below the account's local application data
// folder, and the Authenticode signer its file must carry (the subject
// identity.VerifyFile reports). The Panel verifies both itself; nothing an
// installer or another process writes can add or change one.
type OfficialProgram = product.OfficialProgram

// OfficialProgramOf is the official program the compiled catalog names for
// the integrated app registered under app, if any.
func OfficialProgramOf(app AppID) (OfficialProgram, bool) {
	return product.OfficialAgentProgram(string(app))
}

// ModelRequirement is the plan configuration an engine choice's picture
// model preparation completes (Plan.Configuration).
func ModelRequirement(engine AppID) string {
	return string(engine) + "-model"
}

// StepChoices are the integrated apps the setup step serves, each with its
// use and, for pictures, its engine.
func (c Catalog) StepChoices() []Choice {
	var out []Choice
	for _, choice := range c.Choices {
		if choice.Application && choice.Use != "" {
			out = append(out, choice)
		}
	}
	return out
}

type Catalog struct {
	archiveCacheRoot  string
	discovery         map[AppID]json.RawMessage
	connectorPackages map[string]admittedPackage
	Choices           []Choice    `json:"choices"`
	Components        []Component `json:"components"`
	// PreviousInstallPins are exact artifacts carried forward by a release so
	// already installed app trees remain recognizable. They are never included
	// in a new plan or accepted as authority for a download or installation.
	PreviousInstallPins []KnownInstallPin `json:"-"`
	// LegacyConnectorPin retains the single historical connector MSI for
	// Connect. It is outside the install plan and grants no package admission.
	LegacyConnectorPin *ConnectorPackagePin `json:"-"`
	// DesktopIntegration pins the per-user package the Catalog can apply to
	// the existing official desktop app, independently of the core bundle.
	DesktopIntegration *DesktopIntegration `json:"-"`
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

// ApplicationName reads the compiled display name independently of package availability.
func ApplicationName(id AppID) string {
	for _, choice := range compiledCatalog().Choices {
		if choice.Application && choice.ID == id {
			return choice.Name()
		}
	}
	return string(id)
}

var embeddedDiscoveryCatalog = func() map[AppID]json.RawMessage {
	var catalog map[AppID]json.RawMessage
	if err := json.Unmarshal([]byte(product.DiscoveryCatalog()), &catalog); err != nil {
		log.Printf("setup: discovery catalogue is unavailable: %v", err)
		return nil
	}
	return catalog
}()

var (
	firstPortableEngine = product.SetupEngine(lemonadeProviderID)
	legacyCPUEngine     = product.SetupEngine(theNoiseProviderID)
	optimizedEngine     = product.SetupEngine(pyTorchProviderID)
)

func setupEngineChoice(app AppID, provider, graphics string) Choice {
	engine := product.SetupEngine(provider)
	display, _ := product.EngineTitle(provider)
	return Choice{ID: app, Title: engine.ChoiceTitle, Display: display, Components: []string{engine.Component}, Host: provider, Graphics: graphics}
}

func compiledCatalog() Catalog {
	editor, _ := product.OfficialAgentProgram(product.InstalledAgentConnectionParty())
	return Catalog{
		discovery: embeddedDiscoveryCatalog,
		Choices: []Choice{
			// Pictures run in the optimized engine where it fits (VISION
			// 2026-10-30, fast engines ship) and in the last one elsewhere.
			// The person installs ComfyUI from its maker and Setup connects
			// it (comfy_official.go, owner 2026-11-11); its one component is
			// the OA build earlier releases installed, which Setup only
			// removes.
			{ID: ComfyUI, Title: "ComfyUI", Application: true, Components: []string{retainedArchiveComponent}, Use: UseImage, Engines: []AppID{PyTorch, Lemonade}},
			{ID: OpenCode, Title: product.AgentConnectionTitle(string(OpenCode)), Application: true, Components: []string{OpenCodeOfficialComponent}, Use: UseCode, Official: &editor},
			// Titles are identity (the catalog digest); Display is shown.
			setupEngineChoice(Lemonade, lemonadeProviderID, ""),
			setupEngineChoice(TheNoise, theNoiseProviderID, ""),
			setupEngineChoice(PyTorch, pyTorchProviderID, "gfx1151"),
		},
		Components: []Component{
			// The OA build of ComfyUI: no release pins it, so no plan
			// installs it; Setup removes an earlier release's copy
			// through the declared retained archive binding.
			{ID: retainedArchiveComponent, Title: "ComfyUI with OpenAbstractions image tools", Pending: "OpenAbstractions no longer builds ComfyUI"},
			{ID: firstPortableEngine.Component, Title: firstPortableEngine.ComponentTitle, Pending: "Not available in this release yet"},
			// The CPU-only choice stays so a saved plan keeps its blocker.
			{ID: legacyCPUEngine.Component, Title: legacyCPUEngine.ComponentTitle, Pending: "Not shipped in this release"},
			{ID: OpenCodeOfficialComponent, Title: "OpenCode from its official installer", Pending: "Install OpenCode through OpenAbstractions Setup", Fixup: installerFixup(OpenCodeOfficialComponent)},
			{ID: optimizedEngine.Component, Title: optimizedEngine.ComponentTitle, Pending: "Not available in this release yet"},
		},
	}
}

func catalogForTarget(goos, goarch string) Catalog {
	c := compiledCatalog()
	if goos == "windows" && goarch == "amd64" {
		for i := range c.Components {
			component := &c.Components[i]
			if component.ID == firstPortableEngine.Component {
				pin := reviewedEngineArchive(firstPortableEngine.Package)
				component.Pending = ""
				component.Artifact = &pin
			}
		}
	}
	if err := projectAdopterAssets(&c, goos, goarch, adopterAssetsJSON); err != nil {
		panic("invalid embedded adopter asset pins: " + err.Error())
	}
	if local := localPackageAssets(); local != nil {
		// A refused document leaves the embedded pins in place.
		_ = projectAdopterAssets(&c, goos, goarch, local)
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
	// A picture app's engine is its required companion (architect ruling
	// 2026-11-08): with no engine chosen, the plan includes the one that
	// fits this computer, as a person's own choice would.
	families := machineFamilies()
	for _, choice := range c.Choices {
		if wanted[choice.ID] && len(choice.Engines) > 0 && !c.engineChosen(wanted) {
			wanted[c.pictureEngineFor(choice, families)] = true
		}
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
		download, peak, err := c.componentSizes(*component.Artifact)
		if err != nil {
			return Plan{}, err
		}
		plan.DownloadBytes += download
		plan.PeakBytes += peak
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
	plan.Blockers = append(plan.Blockers, c.graphicsBlockers(wanted, families)...)
	plan.Blockers = append(plan.Blockers, dependencyVersionBlockers(plan.Components)...)
	// Authorization compares the whole saved plan. Keep captions and order.
	for _, option := range []struct {
		app                AppID
		component, caption string
	}{
		{Lemonade, firstPortableEngine.Component, firstPortableEngine.ModelRequirement},
		{TheNoise, legacyCPUEngine.Component, legacyCPUEngine.ModelRequirement},
		{PyTorch, optimizedEngine.Component, optimizedEngine.ModelRequirement},
	} {
		if needed[option.component] {
			plan.Configuration = append(plan.Configuration, Requirement{ID: ModelRequirement(option.app), Title: option.caption})
		}
	}
	plan.Preparable = len(plan.Blockers) == 0
	// A plan alone never proves packages installed or app configuration done.
	plan.Ready = false
	return plan, nil
}

// componentSizes is what one pinned package downloads and the space it
// takes while it is installed: its archive, and for a native application
// also its backend archive and expanded peak.
func (c Catalog) componentSizes(a Artifact) (download, peak int64, err error) {
	download, peak = a.Bytes, a.PeakBytes
	if a.NativeResources != nil {
		download += a.NativeResources.Backend.Archive.Bytes
		if peak, err = nativeArchivePeak(a, c.archiveCacheRoot); err != nil {
			return 0, 0, err
		}
	}
	return download, peak, nil
}

// AddedComponent is one package a plan still installs on this computer: its
// download and the space it takes while it is installed. Update marks a
// package whose earlier version is installed.
type AddedComponent struct {
	ComponentID   string `json:"component_id"`
	DownloadBytes int64  `json:"download_bytes"`
	DiskBytes     int64  `json:"disk_bytes"`
	Update        bool   `json:"update,omitempty"`
}

// AddedComponents are the packages plan still installs: its pinned packages
// without those already installed at their pinned version (installed, from
// Journal.InstalledVersions). A package installed at another version is an
// update. The Setup page's rows and its total both read this one list.
func (c Catalog) AddedComponents(plan Plan, installed map[string]Artifact) ([]AddedComponent, error) {
	var added []AddedComponent
	for _, component := range plan.Components {
		if component.Artifact == nil {
			continue
		}
		have, ok := installed[component.ID]
		if ok && reflect.DeepEqual(have, *component.Artifact) {
			continue
		}
		d, p, err := c.componentSizes(*component.Artifact)
		if err != nil {
			return nil, err
		}
		added = append(added, AddedComponent{ComponentID: component.ID, DownloadBytes: d, DiskBytes: p, Update: ok})
	}
	return added, nil
}

// AddedSizes is what plan still downloads and the space it still takes on
// this computer: the sum of AddedComponents.
func (c Catalog) AddedSizes(plan Plan, installed map[string]Artifact) (download, peak int64, err error) {
	added, err := c.AddedComponents(plan, installed)
	if err != nil {
		return 0, 0, err
	}
	for _, component := range added {
		download += component.DownloadBytes
		peak += component.DiskBytes
	}
	return download, peak, nil
}

// Available is whether choice id is offered on this computer: every one of
// its packages is pinned in this release and built for graphics this
// computer has. The Setup page shows no row for another choice.
func (c Catalog) Available(id AppID) bool {
	families := machineFamilies()
	for _, choice := range c.Choices {
		if choice.ID != id {
			continue
		}
		if choice.Graphics != "" && !slices.Contains(families, choice.Graphics) {
			return false
		}
		for _, componentID := range choice.Components {
			component, ok := c.component(componentID)
			if !ok || component.Artifact == nil {
				return false
			}
		}
		return len(choice.Components) > 0
	}
	return false
}

// component is the catalog component with id.
func (c Catalog) component(id string) (Component, bool) {
	for _, component := range c.Components {
		if component.ID == id {
			return component, true
		}
	}
	return Component{}, false
}

// engineChosen is whether wanted names a picture engine choice: one whose
// provider registers an inference host.
func (c Catalog) engineChosen(wanted map[AppID]bool) bool {
	for _, choice := range c.Choices {
		if choice.Host != "" && wanted[choice.ID] {
			return true
		}
	}
	return false
}

// pictureEngineFor is a picture app's engine on a computer with families:
// the first of its engines whose packages fit the graphics card, the last
// (the fallback) otherwise.
func (c Catalog) pictureEngineFor(choice Choice, families []string) AppID {
	for _, engine := range choice.Engines[:len(choice.Engines)-1] {
		if c.engineFits(engine, families) {
			return engine
		}
	}
	return choice.Engines[len(choice.Engines)-1]
}

// PictureEngine is the engine a plan for app includes when the person
// chooses none, on this computer; "" for an app without pictures.
func (c Catalog) PictureEngine(app AppID) AppID {
	for _, choice := range c.Choices {
		if choice.ID == app && len(choice.Engines) > 0 {
			return c.pictureEngineFor(choice, machineFamilies())
		}
	}
	return ""
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
		if component.Fixup != nil {
			if err := validateFixup(component.Fixup); err != nil {
				return fmt.Errorf("component %q: %w", component.ID, err)
			}
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
		for dependency, version := range component.DependencyVersions {
			if !slices.Contains(component.Dependencies, dependency) || version == "" {
				return fmt.Errorf("component %q pins a version of %q it does not depend on", component.ID, dependency)
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
		for _, engine := range choice.Engines {
			if !slices.ContainsFunc(c.Choices, func(other Choice) bool { return other.ID == engine && !other.Application }) {
				return fmt.Errorf("choice %q names unknown picture engine %q", choice.ID, engine)
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
		if component.ID == componentID && component.Artifact != nil && reflect.DeepEqual(*component.Artifact, artifact) {
			return true
		}
	}
	for _, pin := range c.PreviousInstallPins {
		if pin.ComponentID == componentID && reflect.DeepEqual(pin.Artifact, artifact) {
			return true
		}
	}
	return false
}

func (a Artifact) validate() error {
	if err := a.validateNativeResources(); err != nil {
		return err
	}
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
	if a.NativeResources != nil {
		return a.validateNativeResources()
	}
	paired := a.SourceArchiveSHA256 != ""
	switch componentID {
	case retainedArchiveComponent:
		if !paired || a.RecordsSHA256 == "" || a.TestedBackendSHA256 != "" {
			return errors.New("ComfyUI package needs its source archive and RECORD-set pins")
		}
	case firstPortableEngine.Component:
		if paired || a.RecordsSHA256 != "" {
			return errors.New("the upstream Lemonade archive carries no OA source or RECORD pin")
		}
		return lemonadePairing(a)
	case legacyCPUEngine.Component:
		if a != theNoiseArtifact() {
			return errors.New("TheNoise requires the release's exact reviewed upstream archive")
		}
	case optimizedEngine.Component:
		if !paired || a.InstallIdentity != "pt" || a.Platform != "windows/amd64" || a.RecordsSHA256 != "" || a.TestedBackendSHA256 != "" {
			return errors.New("PyTorch requires a reviewed Windows x64 input package paired with its corresponding source")
		}
	}
	return nil
}

var legacyCPUArchive = reviewedEngineArchive(legacyCPUEngine.Package)

func theNoiseArtifact() Artifact { return legacyCPUArchive }

func reviewedEngineArchive(declaration string) Artifact {
	var pin Artifact
	if err := json.Unmarshal([]byte(declaration), &pin); err != nil {
		panic(fmt.Errorf("decode reviewed engine archive: %w", err))
	}
	return pin
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

// dependencyVersionBlockers names each planned package whose dependency is
// pinned at another version than the one it was built against.
func dependencyVersionBlockers(components []PlannedComponent) []string {
	versions := map[string]string{}
	for _, planned := range components {
		if planned.Artifact != nil {
			versions[planned.ID] = planned.Artifact.Version
		}
	}
	var blockers []string
	for _, planned := range components {
		for _, dependency := range planned.Dependencies {
			want := planned.DependencyVersions[dependency]
			if have, ok := versions[dependency]; want != "" && ok && have != want {
				blockers = append(blockers, fmt.Sprintf("%s needs %s %s; this release pins %s", planned.Title, dependency, want, have))
			}
		}
	}
	return blockers
}

// InstallDisk is the drive Setup installs packages on, such as "C:", and
// the space free there for this account.
func InstallDisk() (volume string, free uint64, err error) {
	home, err := CurrentHome()
	if err != nil {
		return "", 0, err
	}
	free, err = modelDiskFree(home.Data)
	if err != nil {
		return "", 0, err
	}
	volume = filepath.VolumeName(home.Data)
	if volume == "" {
		volume = filepath.Dir(home.Data)
	}
	return volume, free, nil
}
