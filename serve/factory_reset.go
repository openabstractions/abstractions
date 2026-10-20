package main

import (
	"context"
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

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go-core/powershell"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

const factoryResetUsage = `Usage: openabstractions factory-reset [--all] [--apply | --restore <manifest-file>]

Print the current account's exact settings reset plan. --apply requires OA to
be uninstalled and its runtime and Panel closed. It moves saved configuration,
permissions, questions, registrations and Panel preferences to reversible
backups beside their originals. --restore restores one backup after refusing
occupied destinations. Close OA before restoring.

Default mode preserves models, downloads, content, jobs, outputs, digest caches
and credential values. --all moves all OA-owned current-account local state,
jobs, content, logs, history, caches and credential metadata to reversible
same-volume backups. --all --apply also deletes this account's OA Credential
Manager entries. OS secret deletion is irreversible and can partially succeed;
file backups contain no exported plaintext secrets. External paths referenced
by OA settings, other apps, projects and borrowed/NAS model stores are untouched. Default mode preserves existing provider installations and Setup installation
receipts. --all relocates OA-owned provider state and Setup receipts into backups. Machine configuration and other accounts are outside this reset.
No Python, Go installation, administrator token or network is required.
`

type resetTarget struct {
	Path   string `json:"path"`
	Backup string `json:"backup"`
}
type resetManifest struct {
	Version int           `json:"version"`
	Created string        `json:"created"`
	Entries []resetTarget `json:"entries"`
}
type resetRoots struct{ Local, Roaming string }

// settingsPaths is the sole reset inventory. Provider JSON declarations live
// under runtime-v1/providers; provider content caches outside it are preserved.
func settingsPaths(r resetRoots) ([]string, error) {
	for _, p := range []string{r.Local, r.Roaming} {
		if err := resetSafePath(context.Background(), p); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(p) || filepath.Dir(p) == p {
			return nil, errors.New("factory-reset: absolute account folders required")
		}
	}
	state := filepath.Join(r.Local, layout.Name, "runtime-v1")
	paths := []string{filepath.Join(r.Roaming, "abstraction", "config.json"), filepath.Join(r.Roaming, "Abstraction Panel")}
	for _, name := range []string{"config/config.json", "rights/decisions.json", "rights/defaults-applied", "asks/application.json", "asks/first-use-effects.json", "applications/descriptors.json", "applications/self-registered.json", "applications/" + applicationMessageStartsFile, "applications/" + applicationMessageStartsSchemaFile, "declarations-disabled.json", installedHashesFile, "inference/hosts.json", "inference/selection.json", "inference/gateway.json", pairingStateFile} {
		paths = append(paths, filepath.Join(state, filepath.FromSlash(name)))
	}
	if err := resetSafePath(context.Background(), filepath.Join(state, "providers")); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(state, "providers"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(state, "providers", entry.Name()))
		}
	}
	return paths, nil
}

func resetGuard(ctx context.Context, r resetRoots, shared bool) (func(), error) {
	path := filepath.Join(r.Local, ".openabstractions-settings-lifecycle")
	for _, p := range []string{path, path + ".lock"} {
		if err := resetSafePath(ctx, p); err != nil {
			return nil, err
		}
	}
	return cas.LockContext(ctx, path, shared)
}

// Existing fileid owns refusal of junctions and symlinks in every component.
// Missing targets still require a safe nearest existing parent.
func resetSafePath(ctx context.Context, path string) error {
	return layout.CheckLocalStatePath(ctx, path)
}

// allSettingsRoots is a closed inventory of OA-owned account roots. Moving
// these directory entries does not follow links or read referenced assets.
func allSettingsRoots(r resetRoots) ([]string, error) {
	roots := resetOwnedRootNames(r)
	for _, p := range roots {
		if err := resetSafePath(context.Background(), p); err != nil {
			return nil, err
		}
	}
	return roots, nil
}

// The reset plan and private-state disjointness proof share this closed roster.
// Empty native roots occur only on platforms without account factory-reset.
func resetOwnedRootNames(r resetRoots) []string {
	return layout.AccountStateRootNames(r.Local, r.Roaming)
}

func factoryResetCommand(args []string, out io.Writer) error {
	return factoryResetCommandWithRoots(args, out, func(mutation bool) (resetRoots, error) {
		if mutation {
			return factoryResetRoots()
		}
		return factoryResetAccountRoots()
	})
}

func factoryResetCommandWithRoots(args []string, out io.Writer, roots func(bool) (resetRoots, error)) error {
	if containsHelp(args) {
		_, err := io.WriteString(out, factoryResetUsage)
		return err
	}
	all, apply := false, false
	restore := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			if all {
				return errors.New(factoryResetUsage)
			}
			all = true
		case "--apply":
			if apply {
				return errors.New(factoryResetUsage)
			}
			apply = true
		case "--restore":
			if restore != "" || i+1 >= len(args) {
				return errors.New(factoryResetUsage)
			}
			i++
			restore = args[i]
		default:
			return errors.New(factoryResetUsage)
		}
	}
	if apply && restore != "" {
		return errors.New(factoryResetUsage)
	}
	// A plan reads the calling process's native folder view. Mutation also
	// requires the profile-owner probe, which can create a temporary file.
	r, err := roots(apply || restore != "")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var paths []string
	if all {
		paths, err = allSettingsRoots(r)
	} else {
		paths, err = settingsPaths(r)
	}
	if err != nil {
		return err
	}
	var secretPlan []string
	if all {
		secretPlan, err = factoryResetSecretPlan()
		if err != nil {
			return err
		}
	}
	if !apply && restore == "" {
		for _, path := range paths {
			if err := resetSafePath(ctx, path); err != nil {
				return err
			}
			_, err := os.Lstat(path)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if _, e := fmt.Fprintf(out, "%s  present=%t\n", path, err == nil); e != nil {
				return e
			}
		}
		if all {
			for _, key := range secretPlan {
				if _, err := fmt.Fprintf(out, "IRREVERSIBLE OS credential deletion: %s\n", key); err != nil {
					return err
				}
			}
			_, err = io.WriteString(out, "Plan only. --all --apply moves the listed OA-owned roots to same-volume backups and irreversibly deletes the listed OS credentials. References, links to external/NAS assets and other applications are not traversed.\n")
		} else {
			_, err = io.WriteString(out, "Plan only. Default mode preserves content, jobs, models, downloads and credentials. Use --all to preview all OA-owned local state and OS credential deletion.\n")
		}
		return err
	}
	admission, stop := context.WithTimeout(ctx, 2*time.Second)
	unlock, err := resetGuard(admission, r, false)
	stop()
	if err != nil {
		return fmt.Errorf("factory-reset: runtime or Panel is busy: %w", err)
	}
	defer unlock()
	if err := factoryResetOffline(); err != nil {
		return err
	}
	releaseProfile, err := factoryResetProfileGuard(ctx, r)
	if err != nil {
		return err
	}
	// Windows refuses a directory rename while descendant files are open.
	// Exclusive lifecycle admission prevents a current Panel from reopening
	// the profile after this exact-profile busy check. Legacy versions must
	// already be offline/uninstalled, as required above.
	releaseProfile()
	if restore != "" {
		return restoreSettings(ctx, r, restore, out)
	}
	if all {
		paths, err = allSettingsRoots(r)
	} else {
		paths, err = settingsPaths(r)
	}
	if err != nil {
		return err
	}
	if all {
		secretPlan, err = factoryResetSecretPlan()
		if err != nil {
			return err
		}
	}
	if err := moveSettings(ctx, paths, time.Now().UTC().Format("20060102T150405.000000000Z"), os.Rename, out); err != nil {
		return err
	}
	if all {
		return factoryResetSecrets(secretPlan, out)
	}
	return nil
}

func moveSettings(ctx context.Context, paths []string, stamp string, move func(string, string) error, out io.Writer) error {
	manifest := resetManifest{Version: 1, Created: stamp}
	// Every selected item gets its own same-parent backup. A move never crosses
	// volumes, including accounts whose roaming folder is relocated.
	for _, path := range paths {
		if err := resetSafePath(ctx, path); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() && !slices.Contains([]string{"Abstraction Panel", "openabstractions", "OpenAbstractions", "abstraction"}, filepath.Base(path)) {
			return fmt.Errorf("factory-reset: expected settings file %s", path)
		}
		backup := path + ".oa-reset-" + stamp
		if _, err := os.Lstat(backup); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("factory-reset: backup occupied: %s", backup)
		}
		manifest.Entries = append(manifest.Entries, resetTarget{path, backup})
	}
	if len(manifest.Entries) == 0 {
		_, err := io.WriteString(out, "No saved settings found.\n")
		return err
	}
	// One manifest per original parent supports independent same-volume restore.
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	journal := manifest.Entries[0].Backup + ".manifest.json"
	f, err := os.OpenFile(journal, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	moved := []resetTarget{}
	rollback := func(cause error) error {
		var rollbackErr error
		for i := len(moved) - 1; i >= 0; i-- {
			rollbackErr = errors.Join(rollbackErr, os.Rename(moved[i].Backup, moved[i].Path))
		}
		if rollbackErr == nil {
			rollbackErr = os.Remove(journal)
		}
		return errors.Join(cause, rollbackErr)
	}
	for _, entry := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		if err := resetSafePath(ctx, entry.Path); err != nil {
			return rollback(err)
		}
		if err := move(entry.Path, entry.Backup); err != nil {
			return rollback(err)
		}
		moved = append(moved, entry)
	}
	_, err = fmt.Fprintf(out, "Listed OA state moved to reversible backups. OS secret deletion, when requested, is reported separately and cannot be undone by file restore.\nRestore manifest: %s\nPowerShell: openabstractions factory-reset --restore %s\n", journal, resetPowerShellLiteral(journal))
	return err
}

func resetPowerShellLiteral(path string) string {
	return powershell.Literal(path)
}

func restoreSettings(ctx context.Context, r resetRoots, journal string, out io.Writer) error {
	if err := resetSafePath(ctx, journal); err != nil {
		return err
	}
	file, err := os.Open(journal)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("factory-reset: manifest too large")
	}
	var m resetManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m.Version != 1 || len(m.Entries) == 0 || m.Created == "" {
		return errors.New("factory-reset: invalid manifest")
	}
	allowed, err := settingsPaths(r)
	if err != nil {
		return err
	}
	roots, err := allSettingsRoots(r)
	if err != nil {
		return err
	}
	allowed = append(allowed, roots...)
	// Declaration filenames moved out of providers are absent from the current
	// directory: admit only direct .json children of that exact owner directory.
	providerDir := filepath.Join(r.Local, layout.Name, "runtime-v1", "providers")
	seen := map[string]bool{}
	for _, e := range m.Entries {
		permitted := slices.Contains(allowed, e.Path) || (filepath.Dir(e.Path) == providerDir && strings.HasSuffix(filepath.Base(e.Path), ".json"))
		if !permitted || seen[e.Path] || e.Backup != e.Path+".oa-reset-"+m.Created || strings.ContainsAny(m.Created, "/\\:") {
			return errors.New("factory-reset: manifest target outside inventory")
		}
		for other := range seen {
			if resetPathsOverlap(e.Path, other) {
				return errors.New("factory-reset: overlapping manifest targets")
			}
		}
		seen[e.Path] = true
		for _, p := range []string{e.Path, e.Backup} {
			if err := resetSafePath(ctx, p); err != nil {
				return err
			}
		}
		if _, err := os.Lstat(e.Path); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("factory-reset: restore destination occupied: %s", e.Path)
		}
		if _, err := os.Lstat(e.Backup); err != nil {
			return err
		}
	}
	restored := []resetTarget{}
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			for i := len(restored) - 1; i >= 0; i-- {
				err = errors.Join(err, os.Rename(restored[i].Path, restored[i].Backup))
			}
			return err
		}
		if err := os.Rename(e.Backup, e.Path); err != nil {
			for i := len(restored) - 1; i >= 0; i-- {
				err = errors.Join(err, os.Rename(restored[i].Path, restored[i].Backup))
			}
			return err
		}
		restored = append(restored, e)
	}
	_, err = io.WriteString(out, "Saved settings restored. Backup manifest retained.\n")
	return err
}

func resetPathsOverlap(a, b string) bool {
	for p := a; ; p = filepath.Dir(p) {
		if identity.SamePathName(p, b) {
			return true
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	for p := b; ; p = filepath.Dir(p) {
		if identity.SamePathName(p, a) {
			return true
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return false
}
