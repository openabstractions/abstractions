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

	"github.com/openabstractions/abstraction-facade/go-core/redact"
	identity "github.com/openabstractions/abstraction-identity"
)

const lemonadeProviderID = "lemonade-image"
const maxProviderReply = 16 << 10

// lemonadeInfo is the installed provider's setup-info reply, version 2: its
// private root and identity, the Lemonade version its selection binds, and
// every version it can run with the pairing each was tested with.
type lemonadeInfo struct {
	Version          int                    `json:"version"`
	ProviderRoot     string                 `json:"provider_root"`
	ProviderID       string                 `json:"provider_id"`
	SelectionPresent bool                   `json:"selection_present"`
	ActivePackage    *lemonadeActivePackage `json:"active_package,omitempty"`
	KnownPackages    []lemonadeKnownPackage `json:"known_packages"`
	// StagedModels are the picture models the provider's selection serves,
	// the starter model first.
	StagedModels []string `json:"staged_models"`
	// DownloadDir is the provider's folder for Setup's model downloads, on
	// its models folder's volume; the provider publishes each by rename.
	DownloadDir string `json:"download_dir"`
}

type lemonadeActivePackage struct {
	Version string `json:"version"`
	Program string `json:"program"`
}

type lemonadeKnownPackage struct {
	Version              string `json:"version"`
	ArchiveSHA256        string `json:"archive_sha256"`
	ProgramSHA256        string `json:"program_sha256"`
	BackendArchiveSHA256 string `json:"backend_archive_sha256"`
	ModelRevision        string `json:"model_revision"`
}

type lemonadeProof struct {
	Version  int  `json:"version"`
	Verified bool `json:"verified"`
}

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
		if _, err := lemonadeCLI(ctx, "configure", "--activate-reviewed-package", "--engine-files-only",
			"--vulkan-backend-archive", backendArchivePath); err != nil {
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
	// The first configuration selects the version Setup's journal names as
	// installed; package activate moves the selection afterwards.
	args := []string{"configure", "--activate-reviewed-package", "--package", active.Version, "--bf16-main", mainPath,
		"--bf16-encoder", encoderPath, "--bf16-vae", vaePath,
		"--vulkan-backend-archive", backendArchivePath}
	if replace {
		// An explicit replacement replaces the backends and the model;
		// the provider names each separately.
		args = append(args, "--replace-backend", "--replace-private-model")
	}
	if _, err := lemonadeCLI(ctx, args...); err != nil {
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
	args := []string{"model", "add", "--id", model}
	for _, role := range []string{"main", "text_encoder", "vae"} {
		path, ok := files[role]
		if !ok {
			continue
		}
		stat, err := os.Lstat(path)
		if !filepath.IsAbs(path) || err != nil || !stat.Mode().IsRegular() {
			return errors.New("Lemonade model setup needs absolute verified staging paths")
		}
		args = append(args, "--file", role+"="+path)
	}
	if len(args) != 4+2*len(files) {
		return errors.New("Lemonade model setup names an unknown file role")
	}
	if progress != nil {
		if err := progress(0, 0); err != nil {
			return err
		}
	}
	if _, err := lemonadeCLI(ctx, args...); err != nil {
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
	active, err := activeArtifact(state, "lemonade-engine")
	if err != nil {
		return nil, nil
	}
	if !journal.catalog().knownInstallArtifact("lemonade-engine", active) {
		return nil, errors.New("the installed Lemonade pin is not known to this release")
	}
	return &active, nil
}

func readLemonadeInfo(ctx context.Context) (lemonadeInfo, error) {
	info, _, err := readLemonadeBinding(ctx)
	return info, err
}

// readLemonadeBinding reads the provider's setup-info and checks the version
// its selection binds against the one Setup's journal names.
func readLemonadeBinding(ctx context.Context) (lemonadeInfo, *Artifact, error) {
	raw, err := lemonadeCLI(ctx, "setup-info", "--json")
	if err != nil {
		return lemonadeInfo{}, nil, err
	}
	active, err := activeLemonade(ctx)
	if err != nil {
		return lemonadeInfo{}, nil, err
	}
	info, err := parseLemonadeInfo(raw, active)
	if err != nil {
		return lemonadeInfo{}, nil, err
	}
	return info, active, nil
}

// parseLemonadeInfo accepts a reply version 2 whose active package is the
// version Setup's journal names as in use: its program lies inside that
// version's install folder, and the provider's table entry for it carries the
// pin's archive and pairing. The provider and the bridge come from one
// release (DECISION 2026-10-01, point 6), so no other reply version is read.
func parseLemonadeInfo(raw []byte, active *Artifact) (lemonadeInfo, error) {
	var info lemonadeInfo
	if err := decodeProviderReply(raw, &info); err != nil {
		return lemonadeInfo{}, err
	}
	if info.Version != 2 || info.ProviderID != lemonadeProviderID ||
		!filepath.IsAbs(info.ProviderRoot) || len(info.ProviderRoot) > 4096 || len(info.KnownPackages) == 0 ||
		info.SelectionPresent != (info.ActivePackage != nil) {
		return lemonadeInfo{}, errors.New("installed Lemonade provider returned invalid setup identity")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return lemonadeInfo{}, err
	}
	if !insidePath(cache, info.ProviderRoot) {
		return lemonadeInfo{}, errors.New("installed Lemonade provider root is outside this user's cache")
	}
	if !filepath.IsAbs(info.DownloadDir) || !insidePath(info.ProviderRoot, info.DownloadDir) {
		return lemonadeInfo{}, errors.New("installed Lemonade provider names no download folder inside its root")
	}
	if info.ActivePackage == nil {
		return info, nil
	}
	if active == nil {
		return lemonadeInfo{}, errors.New("the Lemonade provider runs a version Setup has not installed")
	}
	program := info.ActivePackage.Program
	if !filepath.IsAbs(program) || len(program) > 4096 || info.ActivePackage.Version != active.Version {
		return lemonadeInfo{}, fmt.Errorf("the Lemonade provider runs version %s and Setup's installed version is %s", info.ActivePackage.Version, active.Version)
	}
	destination, err := installPath(*active)
	if err != nil {
		return lemonadeInfo{}, err
	}
	if !insidePath(destination, program) || !identity.SamePathName(filepath.Base(program), "lemond.exe") {
		return lemonadeInfo{}, errors.New("installed Lemonade provider package path differs from Setup's installed version")
	}
	if err := knownLemonadePackage(info, *active); err != nil {
		return lemonadeInfo{}, err
	}
	return info, nil
}

// knownLemonadePackage compares a Lemonade pin with the provider's table
// entry of the same version: the archive SHA-256 and, where the pin carries
// them, the tested backend archive and model revision.
func knownLemonadePackage(info lemonadeInfo, a Artifact) error {
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
	raw, err := lemonadeCLI(ctx, "verify-setup", "--json")
	if err != nil {
		return false, err
	}
	var proof lemonadeProof
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
		return errors.New("installed Lemonade provider reply has invalid size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("installed Lemonade provider reply: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("installed Lemonade provider returned trailing data")
	}
	return nil
}

// lemonadeCLI and lemonadeAppInstalled are the installed provider and the
// reviewed app check; tests replace them.
var (
	lemonadeCLI          = runLemonadeCLI
	lemonadeAppInstalled = reviewedLemonadeArchive
)

// lemonadeHomeAllowed refuses every provider verb unless Setup keeps the
// person's own state: the installed provider's folders are the person's,
// whichever runtime the Panel serves (live walkthrough 2026-10-02, D6).
func lemonadeHomeAllowed() error {
	home, err := CurrentHome()
	if err != nil {
		return err
	}
	if home.Isolated {
		return errIsolatedProvider
	}
	return nil
}

// lemonadeExit is a failed provider verb: its exit code and stderr. The
// package verbs exit 2 for a definitive refusal and 3 while a launch holds
// the version.
type lemonadeExit struct {
	Code   int
	Reason string
}

func (e *lemonadeExit) Error() string { return "installed Lemonade setup failed: " + e.Reason }

func lemonadeExitCode(err error) int {
	var exit *lemonadeExit
	if errors.As(err, &exit) {
		return exit.Code
	}
	return 0
}

func runLemonadeCLI(ctx context.Context, args ...string) ([]byte, error) {
	if err := lemonadeHomeAllowed(); err != nil {
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
	command := exec.CommandContext(ctx, program, args...)
	var output, failure cappedReply
	command.Stdout = &output
	command.Stderr = &failure
	if err := command.Run(); err != nil {
		return nil, lemonadeFailure(failure.String(), err)
	}
	return output.Bytes(), nil
}

// lemonadeFailure is a failed verb's exit: its code and its stderr as the
// panel may show it, through redact.Line, since the provider's words can
// quote what Lemonade printed.
func lemonadeFailure(stderr string, err error) *lemonadeExit {
	reason := redact.Line(stderr, 0)
	if reason == "" {
		reason = err.Error()
	}
	code := 1
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		code = exited.ExitCode()
	}
	return &lemonadeExit{Code: code, Reason: reason}
}

type cappedReply struct{ bytes.Buffer }

func (reply *cappedReply) Write(data []byte) (int, error) {
	if reply.Len()+len(data) > maxProviderReply {
		return 0, errors.New("installed Lemonade provider output exceeded its limit")
	}
	return reply.Buffer.Write(data)
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
