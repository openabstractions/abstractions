package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	identity "github.com/openabstractions/abstraction-identity"
	providerSetup "github.com/openabstractions/abstraction-provider-lemonade/go/setup"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

const lemonadeProviderID = product.Lemonade

// DefaultLemonadeRoot asks the installed provider for its private state path.
// The provider owns that path; Panel never sends one through a web request.
func DefaultLemonadeRoot() (string, error) {
	root, _, err := defaultLemonadeFolders()
	return root, err
}

// defaultLemonadeFolders asks the installed provider for its private state
// path and the folder Setup downloads model files into.
func defaultLemonadeFolders() (root, downloads string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := readLemonadeInfo(ctx)
	if err != nil {
		return "", "", err
	}
	return info.ProviderRoot, info.DownloadDir, nil
}

// ConfiguredLemonadeProviderID returns the fixed native declaration identity
// only after the provider verifies its app, backend, model and selection.
func ConfiguredLemonadeProviderID(ctx context.Context) (string, error) {
	id, err := LemonadeProviderID(ctx)
	if err != nil {
		return "", err
	}
	verified, err := verifyLemonade(ctx)
	if err != nil {
		return "", err
	}
	if !verified {
		return "", errors.New("Lemonade provider configuration is incomplete")
	}
	return id, nil
}

// LemonadeProviderID reads the installed provider's fixed declaration identity
// without a deep model hash. A current public registry candidate must still be
// checked before saving or using a selection policy.
func LemonadeProviderID(ctx context.Context) (string, error) {
	info, err := readLemonadeInfo(ctx)
	if err != nil {
		return "", err
	}
	return info.ProviderID, nil
}

// ProbeLemonade is a deep recovery check. It may hash the large private model
// snapshot; ordinary setup Status must not call it on every progress poll.
func ProbeLemonade(ctx context.Context, providerRoot string) (bool, error) {
	info, err := readLemonadeInfo(ctx)
	if err != nil {
		return false, err
	}
	if !samePrivatePath(info.ProviderRoot, providerRoot) {
		return false, errors.New("Lemonade provider root changed")
	}
	return verifyLemonade(ctx)
}

// ConfigureLemonade passes four already staged fixed artifacts to the installed
// OA provider CLI. Its private setup code verifies the backend and model pins.
// The provider moves each model download into its models folder by rename;
// progress is indeterminate during that step, then complete only after a
// fresh deep verification. No engine process or shell is launched here.
func ConfigureLemonade(ctx context.Context, providerRoot, mainPath, encoderPath, vaePath, backendArchivePath string,
	progress func(int64, int64) error, replace bool) error {
	info, active, err := readLemonadeBinding(ctx)
	if err != nil {
		return err
	}
	if active == nil {
		return errors.New("Setup has no installed Lemonade version to configure")
	}
	if !samePrivatePath(info.ProviderRoot, providerRoot) {
		return errors.New("Lemonade provider root changed")
	}
	for _, path := range []string{mainPath, encoderPath, vaePath, backendArchivePath} {
		if !filepath.IsAbs(path) {
			return errors.New("Lemonade setup needs absolute verified staging paths")
		}
		stat, err := os.Lstat(path)
		if err != nil || !stat.Mode().IsRegular() {
			return errors.New("Lemonade setup input is missing or is not a regular file")
		}
	}
	if err := lemonadeAppInstalled(*active); err != nil {
		return err
	}
	if info.SelectionPresent && !replace {
		verified, err := verifyLemonade(ctx)
		if err != nil {
			return err
		}
		if verified {
			return nil
		}
		// A verified model and selection whose engine files lack the
		// read-only folder ACL (inference CONTRACT.md INF-F3) verify
		// incomplete. The engine-files step installs them and leaves the
		// private model as it is.
		if _, err := providerSetup.SetupCLI(lemonadeCLI).InstallReviewedEngineFiles(ctx, backendArchivePath); err != nil {
			return err
		}
		verified, err = verifyLemonade(ctx)
		if err != nil {
			return err
		}
		if !verified {
			return errors.New("existing Lemonade selection is incomplete after its engine files were installed (lemonaded configure --activate-reviewed-package --engine-files-only); it needs explicit recovery or replacement")
		}
		if progress != nil {
			return progress(1, 1)
		}
		return nil
	}
	if progress != nil {
		if err := progress(0, 0); err != nil {
			return err
		}
	}
	paths := providerSetup.BF16Files{Main: mainPath, Encoder: encoderPath, VAE: vaePath}
	if _, err := providerSetup.SetupCLI(lemonadeCLI).ConfigureReviewedPicture(ctx, active.Version, paths, backendArchivePath, replace); err != nil {
		return err
	}
	verified, err := verifyLemonade(ctx)
	if err != nil {
		return err
	}
	if !verified {
		return errors.New("Lemonade configure returned without a verified provider setup")
	}
	if progress != nil {
		return progress(1, 1)
	}
	return nil
}

// PictureModelIDs are the models Setup can stage on this machine: the
// starter model and each offered plan's model.
func PictureModelIDs() []string { return pictureModelIDs() }

func pictureModelIDs() []string {
	ids := []string{BF16ModelID}
	for _, plan := range PictureModelChoices() {
		ids = append(ids, plan.Model)
	}
	return ids
}

// LemonadeStagedModels reads the picture models the installed provider has
// staged on this machine, from its setup-info. The provider is the authority
// for that list: a model this Setup does not know, such as one a newer
// provider staged, is left out and logged once per process.
func LemonadeStagedModels(ctx context.Context) ([]string, error) {
	info, err := readLemonadeInfo(ctx)
	if err != nil {
		return nil, err
	}
	known := pictureModelIDs()
	var staged []string
	for _, id := range info.StagedModels {
		if slices.Contains(known, id) {
			staged = append(staged, id)
			continue
		}
		if _, logged := unknownStagedModels.LoadOrStore(id, true); !logged {
			logUnknownModel("Setup leaves out picture model %q, which the Lemonade provider staged and this Setup does not know", id)
		}
	}
	return staged, nil
}

// unknownStagedModels are the unknown staged models already logged.
var unknownStagedModels sync.Map

// logUnknownModel writes one log line; tests replace it.
var logUnknownModel = log.Printf

// StageLemonadeModel adds one offered model to the configured provider with
// its verified downloads (role to absolute path); a role the provider
// already holds by digest passes no file. The provider verifies every pin
// and publishes the model last; a fresh deep verification follows.
func StageLemonadeModel(ctx context.Context, providerRoot, model string, files map[string]string, progress func(int64, int64) error) error {
	info, err := readLemonadeInfo(ctx)
	if err != nil {
		return err
	}
	if !samePrivatePath(info.ProviderRoot, providerRoot) {
		return errors.New("Lemonade provider root changed")
	}
	if !slices.Contains(pictureModelIDs()[1:], model) {
		return fmt.Errorf("%q is no offered picture model", model)
	}
	paths := make(map[string]string, len(files))
	for _, role := range providerSetup.SetupCLIWords().ModelRoles {
		path, ok := files[role]
		if !ok {
			continue
		}
		stat, err := os.Lstat(path)
		if !filepath.IsAbs(path) || err != nil || !stat.Mode().IsRegular() {
			return errors.New("Lemonade model setup needs absolute verified staging paths")
		}
		paths[role] = path
	}
	if len(paths) != len(files) {
		return errors.New("Lemonade model setup names an unknown file role")
	}
	if progress != nil {
		if err := progress(0, 0); err != nil {
			return err
		}
	}
	if _, err := providerSetup.SetupCLI(lemonadeCLI).AddPictureModel(ctx, model, paths); err != nil {
		return err
	}
	verified, err := verifyLemonade(ctx)
	if err != nil {
		return err
	}
	if !verified {
		return errors.New("Lemonade model add returned without a verified provider setup")
	}
	if progress != nil {
		return progress(1, 1)
	}
	return nil
}

// reviewedLemonadeArchive checks the receipt and files of the Lemonade
// version Setup's journal names as the one in use.
func reviewedLemonadeArchive(active Artifact) error {
	destination, err := installPath(active)
	if err != nil {
		return err
	}
	installed, err := ArchiveInstalled(active, destination)
	if err != nil {
		return err
	}
	if !installed {
		return errors.New("reviewed Lemonade app archive is not installed")
	}
	return nil
}

// lemonadeJournal is Setup's plan journal; tests replace it.
var lemonadeJournal = DefaultJournal

// activeLemonade is the Lemonade version Setup's journal names as the one in
// use (journal.go activeArtifact): the plan's package once installed, the
// retained version while an update proceeds. Nil when none is installed.
func activeLemonade(ctx context.Context) (*Artifact, error) {
	journal, err := lemonadeJournal()
	if err != nil {
		return nil, err
	}
	state, err := journal.Load(ctx)
	if err != nil {
		return nil, err
	}
	active, err := journal.cataloguedActiveArtifact(state, firstPortableEngine.Component)
	if errors.Is(err, errUnknownInstalledPin) {
		return nil, errors.New("the installed Lemonade pin is not known to this release")
	}
	if err != nil {
		return nil, nil
	}
	return &active, nil
}

func portablePictureRuntime() starterPictureRecipe {
	return starterPictureRecipe{
		engine: Lemonade, plan: ApprovedBF16Plan, fixedController: panelPictureController,
		starterJournal: portableStarterModelJournal, providerChoices: PictureModelChoices, recordInstalled: RecordInstalledLemonade,
	}
}

// One successful setup-info settles the backfill for this process.
var lemonadeRecord struct {
	sync.Mutex
	done, running bool
	failed        time.Time
}

// Backfill failures wait for the next retry window. Tests can advance the clock.
var (
	lemonadeRecordRetry = time.Minute
	lemonadeNow         = time.Now
)

// RecordInstalledLemonade runs the installed Lemonade provider's setup-info
// once per process. A provider whose state folder has no installed.json
// writes one before it exits 0 (lemonaded writeSetupInfo), and the runtime
// lists that folder's models from it. Without an installed provider, or
// while another call runs, it does nothing. After a failure it waits at
// least a minute before it runs the provider again.
func RecordInstalledLemonade(ctx context.Context) error {
	if providerHomeAllowed() != nil {
		return nil
	}
	lemonadeRecord.Lock()
	if lemonadeRecord.done || lemonadeRecord.running ||
		!lemonadeRecord.failed.IsZero() && lemonadeNow().Sub(lemonadeRecord.failed) < lemonadeRecordRetry {
		lemonadeRecord.Unlock()
		return nil
	}
	lemonadeRecord.running = true
	lemonadeRecord.Unlock()
	err := runLemonadeSetupInfo(ctx)
	lemonadeRecord.Lock()
	defer lemonadeRecord.Unlock()
	lemonadeRecord.running = false
	if err != nil {
		lemonadeRecord.failed = lemonadeNow()
	}
	return err
}

// runLemonadeSetupInfo runs setup-info when Setup's journal names an
// installed Lemonade, and marks the record done when it exits 0.
func runLemonadeSetupInfo(ctx context.Context) error {
	active, err := activeLemonade(ctx)
	if err != nil || active == nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := providerSetup.SetupCLI(lemonadeCLI).ReadSetupInfo(ctx); err != nil {
		return err
	}
	lemonadeRecord.Lock()
	lemonadeRecord.done = true
	lemonadeRecord.Unlock()
	return nil
}

func readLemonadeInfo(ctx context.Context) (providerSetup.SetupInfoReply, error) {
	info, _, err := readLemonadeBinding(ctx)
	return info, err
}

// readLemonadeBinding reads the provider's setup-info and checks the version
// its selection binds against the one Setup's journal names.
func readLemonadeBinding(ctx context.Context) (providerSetup.SetupInfoReply, *Artifact, error) {
	raw, err := providerSetup.SetupCLI(lemonadeCLI).ReadSetupInfo(ctx)
	if err != nil {
		return providerSetup.SetupInfoReply{}, nil, err
	}
	active, err := activeLemonade(ctx)
	if err != nil {
		return providerSetup.SetupInfoReply{}, nil, err
	}
	info, err := parseLemonadeInfo(raw, active)
	if err != nil {
		return providerSetup.SetupInfoReply{}, nil, err
	}
	return info, active, nil
}

// parseLemonadeInfo accepts a reply version 2 whose active package is the
// version Setup's journal names as in use: its program lies inside that
// version's install folder, and the provider's table entry for it carries the
// pin's archive and pairing. The provider and the bridge come from one
// release (DECISION 2026-10-01, point 6), so no other reply version is read.
func parseLemonadeInfo(raw []byte, active *Artifact) (providerSetup.SetupInfoReply, error) {
	var info providerSetup.SetupInfoReply
	if err := decodeProviderReply(raw, &info); err != nil {
		return providerSetup.SetupInfoReply{}, err
	}
	if info.Version != 2 || info.ProviderID != lemonadeProviderID ||
		!filepath.IsAbs(info.ProviderRoot) || len(info.ProviderRoot) > 4096 || len(info.KnownPackages) == 0 ||
		info.SelectionPresent != (info.ActivePackage != nil) {
		return providerSetup.SetupInfoReply{}, errors.New("installed Lemonade provider returned invalid setup identity")
	}
	data, err := layout.AccountData()
	if err != nil {
		return providerSetup.SetupInfoReply{}, err
	}
	if !insidePath(data, info.ProviderRoot) {
		return providerSetup.SetupInfoReply{}, errors.New("installed Lemonade provider root is outside this account's data folder")
	}
	if !filepath.IsAbs(info.DownloadDir) || !insidePath(info.ProviderRoot, info.DownloadDir) {
		return providerSetup.SetupInfoReply{}, errors.New("installed Lemonade provider names no download folder inside its root")
	}
	if info.ActivePackage == nil {
		return info, nil
	}
	if active == nil {
		return providerSetup.SetupInfoReply{}, errors.New("the Lemonade provider runs a version Setup has not installed")
	}
	program := info.ActivePackage.Program
	if !filepath.IsAbs(program) || len(program) > 4096 || info.ActivePackage.Version != active.Version {
		return providerSetup.SetupInfoReply{}, fmt.Errorf("the Lemonade provider runs version %s and Setup's installed version is %s", info.ActivePackage.Version, active.Version)
	}
	destination, err := installPath(*active)
	if err != nil {
		return providerSetup.SetupInfoReply{}, err
	}
	if !insidePath(destination, program) || !identity.SamePathName(filepath.Base(program), "lemond.exe") {
		return providerSetup.SetupInfoReply{}, errors.New("installed Lemonade provider package path differs from Setup's installed version")
	}
	if err := knownLemonadePackage(info, *active); err != nil {
		return providerSetup.SetupInfoReply{}, err
	}
	return info, nil
}

// knownLemonadePackage compares a Lemonade pin with the provider's table
// entry of the same version: the archive SHA-256 and, where the pin carries
// them, the tested backend archive and model revision.
func knownLemonadePackage(info providerSetup.SetupInfoReply, a Artifact) error {
	for _, known := range info.KnownPackages {
		if known.Version != a.Version {
			continue
		}
		if !strings.EqualFold(known.ArchiveSHA256, a.SHA256) {
			return fmt.Errorf("the Lemonade provider's %s is another archive than Setup's pin", a.Version)
		}
		if a.TestedBackendSHA256 != "" && !strings.EqualFold(known.BackendArchiveSHA256, a.TestedBackendSHA256) ||
			a.TestedModelRevision != "" && known.ModelRevision != a.TestedModelRevision {
			return fmt.Errorf("the Lemonade provider pairs %s with another backend or model than Setup's pin", a.Version)
		}
		return nil
	}
	return fmt.Errorf("the installed Lemonade provider cannot run version %s", a.Version)
}

func verifyLemonade(ctx context.Context) (bool, error) {
	raw, err := providerSetup.SetupCLI(lemonadeCLI).VerifySetup(ctx)
	if err != nil {
		return false, err
	}
	var proof providerSetup.VerificationReply
	if err := decodeProviderReply(raw, &proof); err != nil {
		return false, err
	}
	if proof.Version != 1 {
		return false, errors.New("installed Lemonade provider returned unknown verification version")
	}
	return proof.Verified, nil
}

func decodeProviderReply(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxProviderReply {
		return fmt.Errorf("installed %s reply has invalid size", portableProviderTitle)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("installed %s reply: %w", portableProviderTitle, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("installed %s returned trailing data", portableProviderTitle)
	}
	return nil
}

// lemonadeCLI and lemonadeAppInstalled are the installed provider and the
// reviewed app check; tests replace them.
var (
	lemonadeCLI          = runLemonadeCLI
	lemonadeAppInstalled = reviewedLemonadeArchive
)

func runLemonadeCLI(ctx context.Context, args ...string) ([]byte, error) {
	if err := providerHomeAllowed(); err != nil {
		return nil, err
	}
	status, err := LemonadeFeatureStatus()
	if err != nil {
		return nil, err
	}
	if !status.Installed {
		return nil, errors.New(status.Reason)
	}
	panel, err := os.Executable()
	if err != nil {
		return nil, err
	}
	program := filepath.Join(filepath.Dir(panel), "lemonaded.exe")
	command := childproc.Hidden(exec.CommandContext(ctx, program, args...))
	var output, failure cappedReply
	command.Stdout = &output
	command.Stderr = &failure
	if err := command.Run(); err != nil {
		return nil, providerFailure(portableProviderTitle, failure.String(), err)
	}
	return output.Bytes(), nil
}

// samePrivatePath reports whether two folders are one: by file identity
// (identity.SameFile) when both exist, else by their exact cleaned spelling.
func samePrivatePath(first, second string) bool {
	if first == "" || second == "" {
		return false
	}
	if same, err := identity.SameFile(first, second); err == nil {
		return same
	}
	return filepath.Clean(first) == filepath.Clean(second)
}

// insidePath reports whether child lies inside root (withinPath).
func insidePath(root, child string) bool { return withinPath(root, child) }

var portableProviderTitle, _ = product.EngineTitle(lemonadeProviderID)

const portableStarterModelJournal = "model.json"

var lemonadePackageLifecycle = packageLifecycle{
	health:        checkPortablePicturePackage,
	activate:      activatePortablePicturePackage,
	afterActivate: "lemonade-activate",
	retire:        retirePortablePicturePackage,
}

func checkPortablePicturePackage(ctx context.Context, a Artifact, root string) error {
	raw, err := providerSetup.SetupCLI(lemonadeCLI).CheckPackage(ctx, a.Version, root)
	if providerExitCode(err) == providerSetup.SetupCLIWords().RefusedExit {
		return unhealthy("the OpenAbstractions %s refused the staged version: %v", portableProviderTitle, err)
	}
	if err != nil {
		return err
	}
	var reply providerSetup.PackageCheckReply
	if err := decodeProviderReply(raw, &reply); err != nil {
		return err
	}
	if reply.Version != 1 || !reply.OK {
		return fmt.Errorf("installed %s returned an unknown package check reply", portableProviderTitle)
	}
	if a.TestedBackendSHA256 == "" || !strings.EqualFold(reply.BackendArchiveSHA256, a.TestedBackendSHA256) || reply.ModelRevision != a.TestedModelRevision {
		return unhealthy("the OpenAbstractions %s pairs %s with engine archive %s and model %s; the catalog's pin was tested with %s and %s",
			portableProviderTitle, a.Version, reply.BackendArchiveSHA256, reply.ModelRevision, a.TestedBackendSHA256, a.TestedModelRevision)
	}
	return nil
}

func selectPortablePicturePackage(ctx context.Context, r Runner, a Artifact) error {
	archive := stagedPortableBackendArchive(ctx, filepath.Join(filepath.Dir(r.Journal.Path), portableStarterModelJournal))
	raw, err := providerSetup.SetupCLI(lemonadeCLI).ActivatePackage(ctx, a.Version, archive)
	if err != nil {
		return err
	}
	var reply providerSetup.PackageActivationReply
	if err := decodeProviderReply(raw, &reply); err != nil {
		return err
	}
	if reply.Version != 1 || !reply.OK || reply.ActiveVersion != a.Version {
		return fmt.Errorf("installed %s did not confirm the activation", portableProviderTitle)
	}
	return nil
}

func activatePortablePicturePackage(ctx context.Context, r Runner, a Artifact, effect Effect) error {
	err := selectPortablePicturePackage(ctx, r, a)
	if providerExitCode(err) == providerSetup.SetupCLIWords().RefusedExit {
		return unhealthy("the %s refused to activate %s, which is removed; %s: %v", portableProviderTitle, a.Version, keptVersion(effect), err)
	}
	if err != nil {
		return fmt.Errorf("the %s did not activate %s; the earlier version stays active: %w", portableProviderTitle, a.Version, err)
	}
	// Retirement can reselect the retained version without invalidating its
	// model records. Only activation marks them for a fresh provider check.
	if err := clearPortableModelRecords(ctx, filepath.Dir(r.Journal.Path)); err != nil {
		return fmt.Errorf("the %s activated %s; its picture model record could not be updated: %w", portableProviderTitle, a.Version, err)
	}
	return nil
}

func retirePortablePicturePackage(ctx context.Context, r Runner, retired Artifact, reselect *Artifact) error {
	if reselect != nil {
		if err := selectPortablePicturePackage(ctx, r, *reselect); err != nil {
			return fmt.Errorf("Setup stopped: the %s cannot select %s again, so %s stays on disk; if the private model was replaced, install a %s version tested with it, then retry: %w",
				portableProviderTitle, reselect.Version, retired.Version, portableProviderTitle, err)
		}
	}
	_, err := providerSetup.SetupCLI(lemonadeCLI).RetirePackage(ctx, retired.Version)
	if providerExitCode(err) == providerSetup.SetupCLIWords().HeldExit {
		return fmt.Errorf("the earlier %s version is in use; it is removed once the image engine stops: %w", portableProviderTitle, err)
	}
	return err
}

func stagedPortableBackendArchive(ctx context.Context, journal string) string {
	plan := ApprovedBF16Plan()
	controller := ModelController{Plan: &plan, JournalPath: journal}
	state, err := controller.load(ctx)
	if err != nil {
		return ""
	}
	backend, ok := controller.asset(pictureBackendAssetID)
	effect := state.Effects[pictureBackendAssetID]
	if !ok || effect.Phase != Verified || effect.StagedPath == "" || checkModelStage(ctx, effect.StagedPath, backend.Download) != nil {
		return ""
	}
	return effect.StagedPath
}

func clearPortableModelRecords(ctx context.Context, dir string) error {
	starter := ApprovedBF16Plan()
	controllers := []ModelController{{Plan: &starter, JournalPath: filepath.Join(dir, portableStarterModelJournal)}}
	for _, plan := range PictureModelChoices() {
		controllers = append(controllers, ModelController{Plan: &plan, JournalPath: filepath.Join(dir, "model-"+strings.TrimPrefix(plan.Model, "user.")+".json")})
	}
	for _, c := range controllers {
		if _, err := os.Stat(c.JournalPath); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := c.withProviderOperation(ctx, func() error { return c.clearConfigured(ctx) }); err != nil {
			return err
		}
	}
	return nil
}
