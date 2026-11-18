package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// Windows Installer cannot overwrite an executable while any process runs it.
// It moves that image to Config.Msi instead and requires a restart to delete
// it. Begin-upgrade stops the installation's processes, but an activation can
// load tools\openabstractions.exe again before InstallFiles reaches it: an SDK
// client outside the folder runs `openabstractions.exe start`, which the
// exclusion refuses only after Windows has mapped the image (owner's PC,
// 2026-11-07, R6 to R7: tools\openabstractions.exe "is being held in use"
// 0.6 s after the stop, and the upgrade required a restart).
//
// A running image can still be renamed within its volume. Inside the
// installer transaction, begin-upgrade therefore moves each installed OA
// image into tools\.upgrade-previous\<record>, named for the exclusion record
// that the transaction's commit and rollback read back. Windows Installer then
// writes each new file under a free name, and a launch in the meantime finds
// no file and starts nothing. Commit (end-upgrade) deletes each moved image
// whose name received a new file and returns the others; rollback (start)
// moves every one back. Burn's early begin-upgrade moves nothing, because
// Burn may run it without executing the core package.

const upgradeAsideFolder = ".upgrade-previous"

// Only these predecessor tools components can be mapped by a client
// outside the runtime's process census. They are file roles, never stop targets.
var upgradeAsideLibraries = []string{"abstraction_ipc.dll", "abstraction_facade_appmodel.dll"}

func upgradeAsideRoot(folder string) string {
	return filepath.Join(folder, "tools", upgradeAsideFolder)
}

func upgradeAsideName(record *upgradeExclusion) string {
	return fmt.Sprintf("%d-%d", record.Installer.PID, record.Begun.UnixNano())
}

func moveFile(from, to string, flags uint32) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, flags)
}

func regularFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// moveUpgradeImagesAside settles directories an earlier transaction left,
// then moves every installed OA image of each folder into this record's
// directory. A failure leaves that image in place for Windows Installer's own
// in-use handling and is reported as a note.
func moveUpgradeImagesAside(folders []string, record *upgradeExclusion, ledger *upgradeLibraryLedger, recoverTools *os.File) []string {
	var notes []string
	name := upgradeAsideName(record)
	for _, folder := range folders {
		root := upgradeAsideRoot(folder)
		entries, err := os.ReadDir(root)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			notes = append(notes, fmt.Sprintf("read %s: %v", root, err))
		}
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != name {
				notes = append(notes, settleUpgradeAsideDir(folder, filepath.Join(root, entry.Name()), ledger, recoverTools, false, true)...)
			}
		}
		dir := filepath.Join(root, name)
		for _, image := range upgradeProcessNames {
			installed := filepath.Join(folder, "tools", image)
			if !regularFileExists(installed) {
				continue
			}
			note, directoryFailed := moveUpgradeAsideFile(installed, filepath.Join(dir, image))
			if note != "" {
				notes = append(notes, note)
			}
			if directoryFailed {
				break
			}
		}
	}
	return notes
}

func moveUpgradeAsideFile(installed, aside string) (note string, directoryFailed bool) {
	if _, err := os.Lstat(aside); err == nil {
		return fmt.Sprintf("kept %s in place: %s already holds this transaction's previous image", installed, aside), false
	}
	dir := filepath.Dir(aside)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("create %s: %v", dir, err), true
	}
	if err := moveFile(installed, aside, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Sprintf("move %s aside: %v", installed, err), false
	}
	return "", false
}

// movePredecessorLibrariesAside transfers only a same-root upgrade's
// mapped libraries. The caller holds the exact incoming tools directory.
func movePredecessorLibrariesAside(folder string, tools *os.File, record *upgradeExclusion, mover processIdentity, ledger upgradeLibraryLedger) []string {
	root := upgradeAsideRoot(folder)
	dir := filepath.Join(root, upgradeAsideName(record))
	var notes []string
	for _, name := range upgradeAsideLibraries {
		installed := filepath.Join(folder, "tools", name)
		source, err := openUpgradeLibrary(installed, windows.DELETE|windows.FILE_READ_ATTRIBUTES)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = stageUpgradeLibrary(source, tools, root, dir, name, record, mover, ledger)
			err = errors.Join(err, source.Close())
		}
		if err != nil {
			notes = append(notes, fmt.Sprintf("move %s aside: %v", installed, err))
		}
	}
	return notes
}

func stageUpgradeLibrary(source, tools *os.File, root, dir, name string, record *upgradeExclusion, mover processIdentity, ledger upgradeLibraryLedger) error {
	// Create one level at a time, then retain each parent before using it.
	if err := os.Mkdir(root, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	rootHold, err := programfiles.HoldDirectory(root)
	if err != nil {
		return err
	}
	defer rootHold.Release()
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	aside, release, err := holdUpgradeDirectory(dir)
	if err != nil {
		return err
	}
	defer release()
	toolsKey, err := fileid.FileKeyOf(tools)
	if err != nil {
		return err
	}
	asideKey, err := fileid.FileKeyOf(aside)
	if err != nil {
		return err
	}
	libraryKey, err := fileid.FileKeyOf(source)
	if err != nil {
		return err
	}
	if err := ledger.recordMove(record, mover, toolsKey, asideKey, name, libraryKey); err != nil {
		return err
	}
	return renameUpgradeLibrary(source, aside, name, false)
}

func openUpgradeLibrary(path string, access uint32) (*os.File, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	held, err := windows.CreateFile(wide, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(held, &info)
	if err == nil && (info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 || info.NumberOfLinks != 1) {
		err = errors.New("expected a regular file with one installed name")
	}
	if err != nil {
		return nil, errors.Join(err, windows.CloseHandle(held))
	}
	return os.NewFile(uintptr(held), path), nil
}

// HoldDirectory retains the path and its ancestors. The second handle gives
// the native rename the directory object itself as its destination.
func holdUpgradeDirectory(folder string) (*os.File, func(), error) {
	namespace, err := programfiles.HoldDirectory(folder)
	if err != nil {
		return nil, nil, err
	}
	path, err := windows.UTF16PtrFromString(folder)
	if err != nil {
		namespace.Release()
		return nil, nil, err
	}
	target, err := windows.CreateFile(path, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		namespace.Release()
		return nil, nil, err
	}
	file := os.NewFile(uintptr(target), folder)
	return file, func() {
		_ = file.Close()
		_ = namespace.Release()
	}, nil
}

func renameUpgradeLibrary(source, target *os.File, name string, replace bool) error {
	if err := fileid.SafeName(name); err != nil {
		return err
	}
	leaf, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	leaf = leaf[:len(leaf)-1]
	// As in programfiles' absent archive commit, FileRenameInformation uses
	// the exact source handle and resolves the leaf under the held target.
	type renameInfo struct {
		flags  uint32
		root   windows.Handle
		length uint32
		name   [1]uint16
	}
	var header renameInfo
	offset := unsafe.Offsetof(header.name)
	buffer := make([]byte, int(unsafe.Sizeof(header))+len(leaf)*2)
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	if replace {
		info.flags = 1
	}
	info.root = windows.Handle(target.Fd())
	info.length = uint32(len(leaf) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(leaf)), leaf)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(windows.Handle(source.Fd()), &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}

func settleUpgradeLibrary(asideDir *os.File, installed, name string, ledger *upgradeLibraryLedger, recoverTools *os.File, rollback, stale bool) (failure error) {
	if ledger == nil {
		return errors.New("no upgrade library custody ledger")
	}
	proof, err := ledger.load(filepath.Base(asideDir.Name()))
	if err != nil {
		return err
	}
	if proof == nil || proof.Libraries[name] == "" {
		return errors.New("no matching upgrade library custody proof")
	}
	tools, release, err := holdUpgradeDirectory(filepath.Dir(installed))
	if err != nil {
		return err
	}
	defer release()
	if err := proof.matchDirectories(tools, asideDir); err != nil {
		return err
	}
	abandoned := stale && !proof.Committed
	if abandoned && !canRecoverUpgradeLibrary(proof, ledger, recoverTools) {
		return errors.New("upgrade library recovery requires the original tools directory and a departed installer")
	}
	if !rollback && !stale && !proof.Committed {
		proof.Committed = true
		if err := ledger.store(proof); err != nil {
			return err
		}
	}
	source, err := openUpgradeLibrary(filepath.Join(asideDir.Name(), name), windows.DELETE|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, source.Close()) }()
	key, err := fileid.FileKeyOf(source)
	if err != nil {
		return err
	}
	if proof.Libraries[name] != key {
		return errors.New("upgrade library custody file changed")
	}
	if rollback || abandoned {
		// The old installer can no longer settle. Return its exact original
		// only to a vacant name. A new install may already own that name.
		return renameUpgradeLibrary(source, tools, name, rollback)
	}
	// Read access makes the replacement's share restrictions effective.
	// Keep that exact installed entry in place while retiring the predecessor.
	replacement, err := openUpgradeLibrary(installed, windows.FILE_READ_DATA|windows.FILE_READ_ATTRIBUTES)
	if errors.Is(err, fs.ErrNotExist) && (!stale || canRecoverUpgradeLibrary(proof, ledger, recoverTools)) {
		return renameUpgradeLibrary(source, tools, name, false)
	}
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, replacement.Close()) }()
	var status windows.IO_STATUS_BLOCK
	dispose := byte(1)
	return windows.NtSetInformationFile(windows.Handle(source.Fd()), &status, &dispose, 1, windows.FileDispositionInformation)
}

func canRecoverUpgradeLibrary(proof *upgradeLibraryProof, ledger *upgradeLibraryLedger, recoverTools *os.File) bool {
	if recoverTools == nil || ledger.env.ended == nil {
		return false
	}
	key, err := fileid.FileKeyOf(recoverTools)
	return err == nil && key == proof.Tools && ledger.env.ended(proof.Mover) &&
		(proof.Mover == proof.Installer || ledger.env.ended(proof.Installer))
}

func finishUpgradeAsideDir(folder, dir string) []string {
	return settleUpgradeAsideDir(folder, dir, nil, nil, false, false)
}

func settleUpgradeAsideDir(folder, dir string, ledger *upgradeLibraryLedger, recoverTools *os.File, rollback, stale bool) []string {
	asideDir, release, err := holdUpgradeDirectory(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []string{fmt.Sprintf("hold %s: %v", dir, err)}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		release()
		return []string{fmt.Sprintf("read %s: %v", dir, err)}
	}
	var notes []string
	for _, entry := range entries {
		aside := filepath.Join(dir, entry.Name())
		installed := filepath.Join(folder, "tools", entry.Name())
		if isUpgradeAsideLibrary(entry.Name()) {
			if err := settleUpgradeLibrary(asideDir, installed, entry.Name(), ledger, recoverTools, rollback, stale); err != nil {
				notes = append(notes, fmt.Sprintf("settle predecessor library %s: %v", aside, err))
			}
			continue
		}
		if !slices.Contains(upgradeProcessNames, entry.Name()) {
			notes = append(notes, fmt.Sprintf("left %s: outside the upgrade image roles", aside))
			continue
		}
		switch {
		case !entry.Type().IsRegular():
			notes = append(notes, fmt.Sprintf("left %s: not a regular file", aside))
		case rollback:
			if err := moveFile(aside, installed, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
				notes = append(notes, fmt.Sprintf("restore %s: %v", installed, err))
			}
		case regularFileExists(installed):
			if err := os.Remove(aside); err != nil {
				notes = append(notes, fmt.Sprintf("delete previous image %s: %v", aside, err))
			}
		default:
			if err := moveFile(aside, installed, windows.MOVEFILE_WRITE_THROUGH); err != nil {
				notes = append(notes, fmt.Sprintf("return %s: %v", installed, err))
			}
		}
	}
	// Open the held tools directory before releasing the transaction folders.
	// Best-effort empty-directory cleanup stays inside that tools directory.
	toolsPath := filepath.Join(folder, "tools")
	prune := ledger != nil && ledger.ownsDirectories(toolsPath, asideDir)
	tools, err := os.OpenRoot(toolsPath)
	release()
	if err == nil {
		if err := tools.Remove(filepath.Join(upgradeAsideFolder, filepath.Base(dir))); err == nil && prune {
			ledger.remove(filepath.Base(dir))
		}
		_ = tools.Remove(upgradeAsideFolder)
		_ = tools.Close()
	}
	return notes
}

// moveUpgradeImagesAsideFor names the directory by the scope's stored record,
// which a joined Burn chain keeps from its first holder, so commit and
// rollback find the same directory.
func moveUpgradeImagesAsideFor(env exclusionEnv, scope string, folders []string, tools *os.File, caller processIdentity) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path, unlock, err := env.lockScope(ctx, scope)
	if err != nil {
		return []string{fmt.Sprintf("upgrade library custody lock: %v", err)}
	}
	defer unlock()
	ledger := upgradeLibraryLedger{env: env, scope: scope, path: path}
	record, _, err := env.load(scope)
	if err != nil {
		return []string{fmt.Sprintf("previous images of the %s upgrade stay in place: %v", scope, err)}
	}
	if record == nil {
		return []string{fmt.Sprintf("previous images of the %s upgrade stay in place: no exclusion record", scope)}
	}
	admitted, err := env.admittedUpgradeInstaller(path, record)
	if err != nil || !sameUpgradeInstaller(admitted, caller) {
		return []string{fmt.Sprintf("previous images of the %s upgrade stay in place: installer admission changed", scope)}
	}
	unverified := tools != nil && caller.Created == 0
	if tools != nil && (unverified || !containsFolder(record.Folders, filepath.Dir(tools.Name())) || !env.alive(caller, record.Begun)) {
		tools = nil
	}
	notes := moveUpgradeImagesAside(folders, record, &ledger, tools)
	if unverified {
		notes = append(notes, "keeping predecessor DLLs at their installed names: the MSI parent's creation time is unavailable")
	}
	if tools != nil {
		notes = append(notes, movePredecessorLibrariesAside(filepath.Dir(tools.Name()), tools, record, caller, ledger)...)
	}
	return notes
}

func holdSameUpgradeTools(incoming string, predecessors []relatedProduct, folders []string) (*os.File, func()) {
	folder, err := userInstallFolder(incoming)
	if err != nil || !containsFolder(folders, folder) {
		return nil, nil
	}
	tools := filepath.Join(folder, "tools")
	toolsDir, release, err := holdUpgradeDirectory(tools)
	if err != nil {
		return nil, nil
	}
	for _, predecessor := range predecessors {
		if predecessor.location == "" {
			continue
		}
		prior, err := userInstallFolder(predecessor.location)
		if err != nil || !containsFolder(folders, prior) {
			continue
		}
		priorTools := filepath.Join(prior, "tools")
		priorNamespace, err := programfiles.HoldDirectory(priorTools)
		if err != nil {
			continue
		}
		same, err := fileid.SameFile(tools, priorTools)
		priorNamespace.Release()
		if err == nil && same {
			return toolsDir, release
		}
	}
	release()
	return nil, nil
}

func isUpgradeAsideLibrary(name string) bool {
	for _, library := range upgradeAsideLibraries {
		if name == library {
			return true
		}
	}
	return false
}

// Resolve, settle and release one admitted installer occurrence under the same
// scope lock. A delayed action cannot reopen or release a successor's record.
func settleUpgradeAsideOperation(env exclusionEnv, scope string, rollback bool, caller processIdentity, release bool) (notes, releaseNotes []string, err error) {
	// A completed removal may already have removed the scope directory. An
	// absent record has no custody to settle; do not recreate its admission lock.
	record, _, err := env.load(scope)
	if err != nil {
		return nil, nil, fmt.Errorf("previous images of the %s upgrade: %w", scope, err)
	}
	if record == nil {
		if release {
			releaseNotes = []string{fmt.Sprintf("no %s upgrade exclusion was held", scope)}
		}
		return nil, releaseNotes, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path, unlock, err := env.lockScope(ctx, scope)
	if err != nil {
		return nil, nil, fmt.Errorf("upgrade library custody lock: %w", err)
	}
	defer unlock()
	ledger := upgradeLibraryLedger{env: env, scope: scope, path: path}
	record, _, err = env.load(scope)
	if err != nil {
		return nil, nil, fmt.Errorf("previous images of the %s upgrade: %w", scope, err)
	}
	if record == nil {
		if release {
			releaseNotes = []string{fmt.Sprintf("no %s upgrade exclusion was held", scope)}
		}
		return nil, releaseNotes, nil
	}
	admitted, err := env.admittedUpgradeInstaller(path, record)
	if err != nil {
		return nil, nil, err
	}
	if !sameUpgradeInstaller(admitted, caller) && !env.canRollbackJoinedUpgrade(record, admitted, caller, rollback) {
		return nil, nil, fmt.Errorf("installer occurrence no longer holds the %s upgrade exclusion", scope)
	}
	for _, folder := range record.Folders {
		dir := filepath.Join(upgradeAsideRoot(folder), upgradeAsideName(record))
		custody := &ledger
		if caller.Created == 0 {
			custody = nil // Unverified legacy MSI parents never acquire DLL custody.
		}
		notes = append(notes, settleUpgradeAsideDir(folder, dir, custody, nil, rollback, false)...)
	}
	if release {
		releaseNotes, err = env.removeLocked(path, scope)
	}
	return notes, releaseNotes, err
}

func sameUpgradeInstaller(admitted, caller processIdentity) bool {
	// For a creation-time-zero parent, both snapshots supply the literal
	// executable leaf. Only the exact nonempty leaf can carry legacy custody.
	return admitted.PID != 0 && admitted.PID == caller.PID && admitted.Created == caller.Created &&
		(admitted.Created > 0 || (admitted.Image != "" && admitted.Image == caller.Image))
}

// reportUpgradeAside prints each note and keeps it in installer-actions.txt,
// because Windows Installer discards a custom action's output. A note never
// changes the action's exit status.
func reportUpgradeAside(output io.Writer, scope, command string, notes []string) {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, note := range notes {
		appendInstallerAction(installerActionsPath, scope, fmt.Sprintf("%s %s: %s\n", now, command, note))
		//unchecked: a windowless installer child can have an invalid stdout handle, and the note is already in installer-actions.txt
		_ = printLine(output, note)
	}
}
