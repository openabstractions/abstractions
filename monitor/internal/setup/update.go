package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
)

// HealthCheck decides whether a staged package version may be activated. It
// returns an *UnhealthyError for a definitive refusal; any other error leaves
// the version staged for another attempt.
type HealthCheck func(ctx context.Context, componentID string, a Artifact, root string) error

// RetireGuard refuses removal of an earlier version that something still
// uses. The Panel supplies one for ComfyUI, whose registered application entry
// names one version's program.
type RetireGuard func(ctx context.Context, componentID, root string) error

type UnhealthyError struct{ Reason string }

func (e *UnhealthyError) Error() string { return e.Reason }

func unhealthy(format string, args ...any) error {
	return &UnhealthyError{Reason: fmt.Sprintf(format, args...)}
}

// packageUpdateProvider maps a shipped or historical engine component to
// its compiled controller. The compiled choice survives an unavailable
// release pin and remains the owner of retained and retired effects.
var packageUpdateProvider = func() map[string]string {
	providers := map[string]string{}
	for _, choice := range compiledCatalog().Choices {
		if choice.Host != "" && len(choice.Components) == 1 {
			providers[choice.Components[0]] = choice.Host
		}
	}
	return providers
}()

// packageLifecycle defines the provider operations that surround Setup's
// Staged, Checked, Installed and Retired receipt transitions.
type packageLifecycle struct {
	health        func(context.Context, Artifact, string) error
	activate      func(context.Context, Runner, Artifact, Effect) error
	afterActivate string
	retire        func(context.Context, Runner, Artifact, *Artifact) error
}

var providerPackageLifecycles = map[string]packageLifecycle{
	lemonadeProviderID: lemonadePackageLifecycle,
}

func packageLifecycleFor(componentID string) packageLifecycle {
	return providerPackageLifecycles[packageUpdateProvider[componentID]]
}

// firstInstallChecked names the packages that also pass Staged and Checked
// on their first installation.
func firstInstallChecked(componentID string) bool {
	if componentID == retainedArchiveComponent || testPackageKinds[componentID] != nil || packageLifecycleFor(componentID).health != nil {
		return true
	}
	switch packageUpdateProvider[componentID] {
	case theNoiseProviderID, pyTorchProviderID:
		return true
	default:
		return false
	}
}

// retiresAfterRegistration keeps the previous registered build until its
// successor owns registration. Automatic driving and removal share this rule.
func retiresAfterRegistration(componentID string) bool {
	return componentID == retainedArchiveComponent
}

// packageHealth is the health check of each package kind. It reads the
// receipt-verified tree and runs none of the package's programs.
func packageHealth(ctx context.Context, componentID string, a Artifact, root string) error {
	if a.NativeResources != nil {
		installed, err := archiveInstalledContext(ctx, a, root)
		if err != nil {
			return err
		}
		if !installed {
			return errors.New("native logical installation is incomplete")
		}
		return nil
	}
	if componentID == retainedArchiveComponent {
		return comfyHealth(a, root)
	}
	if check := packageLifecycleFor(componentID).health; check != nil {
		return check(ctx, a, root)
	}
	if check := testPackageKinds[componentID]; check != nil {
		return check(a, root)
	}
	return unhealthy("no health check is defined for %s", componentID)
}

// comfyHealth: the package's provenance names the catalog's paired source
// archive and RECORD-set pin, upstream updaters are absent, and the launch
// entry points the activation recipe names are regular files.
func comfyHealth(a Artifact, root string) error {
	var provenance struct {
		SourceArchiveSHA256 *string `json:"corresponding_comfyui_source_zip_sha256"`
		RecordsSHA256       *string `json:"private_python_records_sha256"`
	}
	if err := readPackageJSON(filepath.Join(root, "OA-PROVENANCE.json"), &provenance); err != nil {
		return unhealthy("ComfyUI package provenance: %v", err)
	}
	if provenance.SourceArchiveSHA256 == nil || *provenance.SourceArchiveSHA256 != a.SourceArchiveSHA256 {
		return unhealthy("the ComfyUI package names a different source archive than its catalog pair")
	}
	if provenance.RecordsSHA256 == nil || *provenance.RecordsSHA256 != a.RecordsSHA256 {
		return unhealthy("the ComfyUI package's private Python differs from its catalog RECORD-set pin")
	}
	portable := filepath.Join(root, comfyPortableDirectory)
	for _, updater := range []string{"update", "ComfyUI/custom_nodes/ComfyUI-Manager"} {
		if _, err := os.Lstat(filepath.Join(portable, filepath.FromSlash(updater))); !errors.Is(err, fs.ErrNotExist) {
			return unhealthy("the ComfyUI package still carries the upstream updater %s", updater)
		}
	}
	entries, err := os.ReadDir(portable)
	if err != nil {
		return unhealthy("the ComfyUI package has no portable folder: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(strings.ToLower(entry.Name()), "update_comfyui") {
			return unhealthy("the ComfyUI package still carries the upstream updater %s", entry.Name())
		}
	}
	for _, entry := range []string{"python_embeded/python.exe", comfyLauncherName, "ComfyUI/main.py"} {
		if err := regularFile(filepath.Join(portable, filepath.FromSlash(entry))); err != nil {
			return unhealthy("the ComfyUI package lacks %s", entry)
		}
	}
	return nil
}

func readPackageJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return errors.New("not a small regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}

// checkChange holds an update to what the installed version required: a new
// folder and the same install handler, and therefore the same caller rights
// (a per-user archive needs no elevation and is activated by the same
// account).
func (r Runner) checkChange(componentID string, a, retained Artifact) error {
	if sameVersionFolder(a, retained) {
		return errors.New("the new package reuses the installed version's folder; a changed package needs a new version")
	}
	if a.Action != retained.Action || a.Action != "archive_per_user" {
		return errors.New("the new package needs different install rights than the installed one; Setup keeps the rights the install required")
	}
	return nil
}

// sameVersionFolder reports whether two pins install into one folder.
// Windows folder names ignore case.
func sameVersionFolder(a, b Artifact) bool {
	return a.InstallIdentity == b.InstallIdentity && foldName(a.Version) == foldName(b.Version)
}

// checkHealth runs the package's health check on a staged update. A refused
// version is retired; the earlier version stays active.
func (r Runner) checkHealth(ctx context.Context, state State, componentID string, a Artifact, effect Effect) (Status, error) {
	root, err := installPath(a)
	if err != nil {
		return r.status(ctx, state), err
	}
	check := r.healthCheck
	provider := packageUpdateProvider[componentID]
	if check == nil && provider == pyTorchProviderID {
		state, err = r.preparePyTorchComponent(ctx, state, a, root)
		if err != nil {
			return r.status(ctx, state), err
		}
		effect = state.Effects[componentID]
		check = func(context.Context, string, Artifact, string) error { return nil }
	}
	if check == nil {
		check = packageHealth
		if provider == theNoiseProviderID {
			check = func(ctx context.Context, _ string, _ Artifact, root string) error {
				return r.installTheNoiseFiles(ctx, a, root, effect.StagedPath)
			}
		}
	}
	err = check(ctx, componentID, a, root)
	var refused *UnhealthyError
	if errors.As(err, &refused) {
		reason := "the new version failed its health check and is removed; " + keptVersion(effect) + ": " + refused.Reason
		return r.rejectStagedUpdate(ctx, state, componentID, effect, reason)
	}
	if err != nil {
		return r.status(ctx, state), fmt.Errorf("health check did not finish; the update stays staged: %w", err)
	}
	if r.crashAfter != nil {
		if err := r.crashAfter("health"); err != nil {
			return Status{}, err
		}
	}
	state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Checked})
	if err != nil {
		return Status{}, err
	}
	return r.status(ctx, state), nil
}

// rejectStagedUpdate keeps the active receipt and retires the candidate
// after a definitive provider or health refusal.
func (r Runner) rejectStagedUpdate(ctx context.Context, state State, componentID string, effect Effect, reason string) (Status, error) {
	state, updateErr := r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Failed, Reason: reason})
	if updateErr != nil {
		return Status{}, updateErr
	}
	if status, retireErr := r.retire(ctx, state, componentID); retireErr != nil {
		return status, fmt.Errorf("%s; %w", reason, retireErr)
	}
	state, _ = r.Journal.Load(ctx)
	return r.status(ctx, state), errors.New(reason)
}

// activate makes the checked version the active one. It rechecks what the
// install required and the staged receipt, since a restart may lie between
// the health check and activation.
func (r Runner) activate(ctx context.Context, state State, componentID string, a Artifact, effect Effect) (Status, error) {
	if effect.Retained != nil {
		if err := r.checkChange(componentID, a, *effect.Retained); err != nil {
			return r.status(ctx, state), err
		}
	} else if a.Action != "archive_per_user" {
		return r.status(ctx, state), errors.New("this package has no fixed installer handler")
	}
	root, err := installPath(a)
	if err != nil {
		return r.status(ctx, state), err
	}
	installed, err := ArchiveInstalled(a, root)
	if err != nil {
		return r.status(ctx, state), err
	}
	if !installed {
		reason := "the checked version changed before activation and is removed; " + keptVersion(effect)
		state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Failed, Reason: reason})
		if err != nil {
			return Status{}, err
		}
		status, retireErr := r.retire(ctx, state, componentID)
		if retireErr != nil {
			return status, fmt.Errorf("%s; %w", reason, retireErr)
		}
		return status, errors.New(reason)
	}
	if packageUpdateProvider[componentID] == pyTorchProviderID {
		_, component, _, _, err := pyTorchFolders(a)
		if err != nil {
			return r.status(ctx, state), err
		}
		ready, err := pyTorchComponentReady(effect, component)
		if err != nil || !ready {
			return r.status(ctx, state), errors.New("the original PyTorch component changed before activation; preserve it for inspection")
		}
		// The current archive must switch to the new component before Setup
		// activates it and retires the preceding version.
		if effect.Retained != nil {
			if err := r.resealCurrentArchiveEngine(ctx, state, pyTorchSitePackages(component), a.Version); err != nil {
				return r.status(ctx, state), err
			}
		}
	}
	// The provider's binding moves before Setup commits Installed. A first
	// install leaves that binding to the provider's normal configuration.
	lifecycle := packageLifecycleFor(componentID)
	if lifecycle.activate != nil && effect.Retained != nil {
		err := lifecycle.activate(ctx, r, a, effect)
		if refused, ok := err.(*UnhealthyError); ok {
			return r.rejectStagedUpdate(ctx, state, componentID, effect, refused.Reason)
		}
		if err != nil {
			return r.status(ctx, state), err
		}
		if r.crashAfter != nil && lifecycle.afterActivate != "" {
			if err := r.crashAfter(lifecycle.afterActivate); err != nil {
				return Status{}, err
			}
		}
	}
	reason := "installed after its health check"
	if effect.Retained != nil {
		reason = "updated; the earlier version is removed next"
	}
	state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Installed, Reason: reason})
	if err != nil {
		return Status{}, err
	}
	return r.status(ctx, state), nil
}

func keptVersion(effect Effect) string {
	if effect.Retained != nil {
		return "the earlier version stays active"
	}
	return "nothing is installed; Retry installs the verified download again"
}

// retire removes the package files of the first version awaiting removal.
func (r Runner) retire(ctx context.Context, state State, componentID string) (Status, error) {
	effect := state.Effects[componentID]
	if len(effect.Retired) == 0 {
		return r.status(ctx, state), nil
	}
	// One held version, such as a ComfyUI version its registration still
	// starts, must not keep the others from being removed.
	var first error
	for _, retired := range effect.Retired {
		kept, root, err := r.removeRetired(ctx, state, componentID, retired)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		reason := ""
		if kept > 0 {
			reason = fmt.Sprintf("kept %d files at %s that were not part of the removed package", kept, root)
		}
		state, err = r.Journal.ForgetRetired(ctx, state.Plan.ID, componentID, effect, retired, reason)
		if err != nil {
			return Status{}, err
		}
		return r.status(ctx, state), nil
	}
	return r.status(ctx, state), first
}

func (r Runner) removeRetired(ctx context.Context, state State, componentID string, retired Artifact) (int, string, error) {
	effect := state.Effects[componentID]
	root, err := installPath(retired)
	if err != nil {
		return 0, "", err
	}
	if active, err := activeArtifact(state, componentID); err == nil && sameArtifact(&active, &retired) {
		return 0, root, errors.New("the version awaiting removal is the active one; it was preserved")
	}
	component, _ := planComponent(state.Plan, componentID)
	neverActive := effect.Phase == Failed && component.Artifact != nil && sameArtifact(&retired, component.Artifact)
	if retire := packageLifecycleFor(componentID).retire; retire != nil {
		// A candidate that never became active in the journal may have
		// been selected by the provider. Pass the recorded active pin
		// so the provider can reselect it before removing the candidate.
		var reselect *Artifact
		if active, err := activeArtifact(state, componentID); neverActive && err == nil {
			reselect = &active
		}
		if err := retire(ctx, r, retired, reselect); err != nil {
			return 0, root, err
		}
	}
	switch {
	case r.RetireGuard != nil:
		if err := r.RetireGuard(ctx, componentID, root); err != nil {
			return 0, root, err
		}
	case retiresAfterRegistration(componentID) && !neverActive:
		return 0, root, errors.New("register the updated ComfyUI before its earlier version is removed")
	}
	kept, err := removePackageTree(retired, root)
	if err != nil {
		return 0, root, fmt.Errorf("the earlier version at %s stays: %w", root, err)
	}
	return kept, root, nil
}

// removePackageTree deletes the files of one installed version that still
// match its receipt and returns how many other files it kept. The tree first
// moves to a sibling name, which Windows refuses while a program in it runs.
// Files the person added or changed stay, restored to the version's folder.
// An interrupted removal resumes from the sibling.
func removePackageTree(a Artifact, root string) (int, error) {
	return removePackageTreeContext(context.Background(), a, root)
}

// renameAsideWaits are the pauses between rename attempts. Antivirus and
// indexers open a new folder's files for a moment; a program running from
// the version holds them for longer than these 1.55 seconds.
var renameAsideWaits = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
	400 * time.Millisecond, 800 * time.Millisecond}

// renameAside moves a version's folder to its removal sibling, trying again
// with backoff while something holds a file in it.
func renameAside(ctx context.Context, root, aside string) error {
	err := os.Rename(root, aside)
	for _, wait := range renameAsideWaits {
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		err = os.Rename(root, aside)
	}
	return err
}

func removePackageTreeContext(ctx context.Context, a Artifact, root string) (int, error) {
	aside := filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".removing")
	if _, err := os.Lstat(root); err == nil {
		if _, err := os.Lstat(aside); !errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("%s holds files from an unfinished removal of this version, and the version's folder exists again; move what you need out of %s, delete it, then retry", aside, aside)
		}
		if _, err := matchingReceiptContext(ctx, a, root); errors.Is(err, fs.ErrNotExist) {
			// An earlier removal finished and restored only kept files.
			return countFiles(root)
		} else if err != nil {
			return 0, err
		}
		if err := renameAside(ctx, root, aside); err != nil {
			return 0, fmt.Errorf("the version is in use or locked; close it and retry: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	} else if info, err := os.Lstat(aside); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	} else if info.Mode().Type() != fs.ModeDir {
		// A junction or link is no folder this removal moved aside.
		return 0, fmt.Errorf("%s is not a folder from this removal; remove it, then retry", aside)
	}
	// An engine folder a provider installed in the version, such as
	// Lemonade's package folder, is read-only for the account (inference
	// CONTRACT.md INF-F3); it is given back before its files go. The move to
	// the sibling name proved that nothing in the version ran.
	receipt, err := matchingReceiptContext(ctx, a, aside)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	if err := programfiles.UnlockWithin(aside); err != nil {
		return 0, fmt.Errorf("the engine folders in %s could not be made removable: %w", aside, err)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// The receipt goes last; only kept files remain.
	case err != nil:
		return 0, err
	default:
		reportProgress(ctx, 0, len(receipt.Files))
		for i, file := range receipt.Files {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if i%100 == 0 {
				reportProgress(ctx, i, len(receipt.Files))
			}
			if verifyInstalledPath(aside, file.Path) != nil {
				continue
			}
			target := filepath.Join(aside, filepath.FromSlash(file.Path))
			if same, err := fileMatches(target, file); err != nil || !same {
				continue
			}
			if err := os.Remove(target); err != nil {
				return 0, err
			}
		}
		reportProgress(ctx, len(receipt.Files), len(receipt.Files))
		removeEmptyDirectories(aside)
		if err := os.Remove(filepath.Join(aside, archiveMarker)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
	}
	removeEmptyDirectories(aside)
	kept, err := countFiles(aside)
	if err != nil {
		return 0, err
	}
	if kept == 0 {
		if err := os.Remove(aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		return 0, nil
	}
	if err := os.Rename(aside, root); err != nil {
		return kept, fmt.Errorf("%d files you added or changed are in %s and could not move back to %s; move what you need out of %s, delete it, then retry: %w",
			kept, aside, root, aside, err)
	}
	return kept, nil
}

func countFiles(root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}

func matchingReceipt(a Artifact, root string) (archiveReceipt, error) {
	return matchingReceiptContext(context.Background(), a, root)
}

func matchingReceiptContext(ctx context.Context, a Artifact, root string) (archiveReceipt, error) {
	markerPath := filepath.Join(root, archiveMarker)
	info, err := os.Lstat(markerPath)
	if err != nil {
		return archiveReceipt{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return archiveReceipt{}, errors.New("the version's install receipt is not a regular file; nothing was removed")
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return archiveReceipt{}, err
	}
	var receipt archiveReceipt
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.Format != 2 ||
		!strings.EqualFold(receipt.ArtifactSHA256, a.SHA256) || receipt.Version != a.Version || receipt.InstallIdentity != a.InstallIdentity {
		return archiveReceipt{}, errors.New("the folder's install receipt names another package; nothing was removed")
	}
	if err := receiptHasExpectedRoster(ctx, a, receipt); err != nil {
		return archiveReceipt{}, err
	}
	for _, file := range receipt.Files {
		if !safeMemberPath(file.Path) || foldName(file.Path) == foldName(archiveMarker) {
			return archiveReceipt{}, errors.New("the folder's install receipt is unsafe; nothing was removed")
		}
	}
	if a.NativeResources != nil {
		return nativeRemovalReceipt(ctx, a, root, receipt)
	}
	return receipt, nil
}

func fileMatches(path string, record archiveFileRecord) (bool, error) {
	input, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != record.Size {
		return false, err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, input); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(sum.Sum(nil)), record.SHA256), nil
}

// removeEmptyDirectories removes empty directories below root, deepest first,
// and never follows a link.
func removeEmptyDirectories(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() && current != root {
			dirs = append(dirs, current)
		}
		return nil
	})
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	for _, dir := range dirs {
		_ = os.Remove(dir) // fails, and keeps the directory, while it holds anything
	}
}
