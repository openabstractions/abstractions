package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// An upgrade exclusion keeps the installation being replaced from activating
// while a Windows Installer transaction owns it. The incoming package writes
// the record early in its script, removes it when the transaction commits, and
// releases it during rollback before the rollback activation. Every activation
// path of the covered folders refuses with exit status 3 while the record is
// live: `openabstractions start`, `serve host` (per-user and under the SCM)
// and the SDK's on-demand activation, which runs `start`.
//
// The record names its holder by the installer process's PID and creation
// time, so a transaction that ended without committing or rolling back stops
// excluding when that process is gone. No OS lock outlives the short custom
// action processes that write it.
//
// A per-user upgrade run by a standard account executes begin-upgrade
// impersonated, and its parent is the SYSTEM-owned msiexec server, which that
// account cannot open. The writer then takes the parent's PID and image name
// from the process snapshot and records creation time 0, meaning "unreadable
// when written". Every reader treats such a holder the same way: a PID that is
// gone ends the exclusion, and a PID that is present, whether or not this
// reader can open it, is honoured only until exclusionUnverifiedLimit after the
// record began. When the parent is readable, the record carries its creation
// time and full verification applies.
//
// The record holds no list of stopped processes: the package stops nothing,
// and every restart path is an idempotent activation. A 0.1.7 reader accepts
// this record, which only omits the two lists it declared.

var errUpgradeInProgress = errors.New("an upgrade of this installation is in progress")

const (
	exclusionVersion  = 1
	exclusionMaxBytes = 64 << 10
	// A holder whose identity cannot be read (access denied) is honoured for
	// at most this long after the record was written.
	exclusionUnverifiedLimit = time.Hour
)

type processIdentity struct {
	PID     uint32 `json:"pid"`
	Created int64  `json:"created"`
	Image   string `json:"image"`
}

type upgradeExclusion struct {
	Version   int             `json:"version"`
	Scope     string          `json:"scope"`
	Folders   []string        `json:"folders"`
	Installer processIdentity `json:"installer"`
	Begun     time.Time       `json:"begun"`
}

func decodeExclusion(data []byte) (*upgradeExclusion, error) {
	if len(data) > exclusionMaxBytes {
		return nil, errors.New("upgrade exclusion record is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record upgradeExclusion
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("upgrade exclusion record: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("upgrade exclusion record has trailing data")
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *upgradeExclusion) validate() error {
	if r.Version != exclusionVersion {
		return fmt.Errorf("upgrade exclusion record version %d is not %d", r.Version, exclusionVersion)
	}
	if r.Scope != "user" && r.Scope != "machine" {
		return fmt.Errorf("upgrade exclusion scope %q is not user or machine", r.Scope)
	}
	if len(r.Folders) == 0 {
		return errors.New("upgrade exclusion record names no folder")
	}
	for _, folder := range r.Folders {
		if !filepath.IsAbs(folder) || filepath.Clean(folder) != folder || filepath.Dir(folder) == folder {
			return fmt.Errorf("upgrade exclusion folder %q is not a clean absolute non-root path", folder)
		}
	}
	// Created 0 is a holder that was unreadable when the record was written.
	if r.Installer.PID == 0 || r.Installer.Created < 0 {
		return errors.New("upgrade exclusion record names no installer process")
	}
	if r.Begun.IsZero() {
		return errors.New("upgrade exclusion record has no start time")
	}
	return nil
}

func (r *upgradeExclusion) covers(image string) bool {
	return insideAnyFolder(longPath(image), r.Folders)
}

// exclusionEnv is the record's storage, trust and liveness. Tests substitute
// each part; systemExclusionEnv is the installed behaviour.
type exclusionEnv struct {
	path    func(scope string) (string, error)
	read    func(path string) ([]byte, error)
	write   func(path, scope string, data []byte) error
	remove  func(path string) error
	trusted func(path, scope string) error
	alive   func(holder processIdentity, begun time.Time) bool
	now     func() time.Time
}

func systemExclusionEnv() exclusionEnv {
	return exclusionEnv{
		path:    exclusionPath,
		read:    readExclusionFile,
		write:   writeExclusionFile,
		remove:  os.Remove,
		trusted: trustedExclusionFile,
		alive:   func(holder processIdentity, begun time.Time) bool { return processAlive(holder, begun, time.Now()) },
		now:     time.Now,
	}
}

func exclusionPath(scope string) (string, error) {
	dir, err := upgradeDirectory(scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "exclusion.json"), nil
}

// load returns nil when no record exists. An error means a record exists and
// cannot be trusted or read.
func (e exclusionEnv) load(scope string) (*upgradeExclusion, string, error) {
	path, err := e.path(scope)
	if err != nil {
		return nil, "", err
	}
	data, err := e.read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, path, nil
	}
	if err != nil {
		return nil, path, err
	}
	if err := e.trusted(path, scope); err != nil {
		return nil, path, err
	}
	record, err := decodeExclusion(data)
	if err != nil {
		return nil, path, err
	}
	if record.Scope != scope {
		return nil, path, fmt.Errorf("upgrade exclusion record %s is for scope %s", path, record.Scope)
	}
	return record, path, nil
}

func (e exclusionEnv) store(scope string, record *upgradeExclusion) error {
	if err := record.validate(); err != nil {
		return err
	}
	path, err := e.path(scope)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return e.write(path, scope, append(data, '\n'))
}

// active is the live record covering image, if any. Records that cannot be
// trusted or read are reported in notes and exclude nothing.
func (e exclusionEnv) active(image string) (*upgradeExclusion, []string) {
	var notes []string
	for _, scope := range []string{"user", "machine"} {
		record, path, err := e.load(scope)
		if err != nil {
			notes = append(notes, fmt.Sprintf("ignored upgrade exclusion %s: %v", path, err))
			continue
		}
		if record == nil || !record.covers(image) {
			continue
		}
		if !e.alive(record.Installer, record.Begun) {
			notes = append(notes, fmt.Sprintf("ignored upgrade exclusion %s: installer process %d is gone", path, record.Installer.PID))
			continue
		}
		return record, notes
	}
	return nil, notes
}

func (e exclusionEnv) begin(scope string, folders []string, holder processIdentity) error {
	existing, _, err := e.load(scope)
	if err == nil && existing != nil && existing.Installer != holder && e.alive(existing.Installer, existing.Begun) {
		return fmt.Errorf("installer process %d already holds the %s upgrade exclusion for %s",
			existing.Installer.PID, scope, strings.Join(existing.Folders, "; "))
	}
	return e.store(scope, &upgradeExclusion{Version: exclusionVersion, Scope: scope, Folders: folders, Installer: holder, Begun: e.now().UTC()})
}

// release removes the record. Rollback releases before activating, so the
// activation is not refused by its own record.
func (e exclusionEnv) release(scope string) []string {
	var notes []string
	path, err := e.path(scope)
	if err != nil {
		return []string{fmt.Sprintf("no %s upgrade exclusion path: %v", scope, err)}
	}
	if err := e.remove(path); errors.Is(err, fs.ErrNotExist) {
		notes = append(notes, fmt.Sprintf("no %s upgrade exclusion was held", scope))
	} else if err != nil {
		notes = append(notes, fmt.Sprintf("could not remove upgrade exclusion %s: %v", path, err))
	}
	return notes
}

func (e exclusionEnv) refusal(image string) error {
	record, _ := e.active(image)
	if record == nil {
		return nil
	}
	return fmt.Errorf("%w: %s is being replaced by installer process %d since %s; activation resumes when that installation finishes",
		errUpgradeInProgress, strings.Join(record.Folders, "; "), record.Installer.PID, record.Begun.Format(time.RFC3339))
}

// refuseDuringUpgrade is the activation gate for this program's own folder.
// The refusal carries exit status 3.
func refuseDuringUpgrade() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot check the upgrade exclusion without this program's path: %w", err)
	}
	return upgradeRefusal(systemExclusionEnv().refusal(self))
}

func upgradeRefusal(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errUpgradeInProgress) {
		return &exitError{code: exitUpgradeInProgress, err: err}
	}
	return err
}

// Storage, trust and liveness on Windows.

func readExclusionFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("upgrade exclusion %s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, exclusionMaxBytes+1))
}

func writeExclusionFile(path, scope string, data []byte) error {
	dir := filepath.Dir(path)
	if scope == "machine" {
		if err := secureMachineDirectories(dir); err != nil {
			return err
		}
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "exclusion-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		var from, to *uint16
		if from, err = windows.UTF16PtrFromString(name); err == nil {
			if to, err = windows.UTF16PtrFromString(path); err == nil {
				err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
			}
		}
	}
	if err != nil {
		if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary exclusion %s: %w", name, removeErr))
		}
	}
	return err
}

// trustedExclusionFile accepts a machine record only from SYSTEM or
// Administrators, and a user record from those or the current user. A record a
// standard user wrote cannot block another account's activation.
func trustedExclusionFile(path, scope string) error {
	owner, err := pathOwner(path)
	if err != nil {
		return err
	}
	if administrativeSID(owner) {
		return nil
	}
	if scope == "user" {
		sid, err := currentUserSID()
		if err == nil && strings.EqualFold(owner.String(), sid) {
			return nil
		}
	}
	return fmt.Errorf("owned by %s, which cannot hold the %s upgrade exclusion", owner, scope)
}

// processAlive reports whether holder still names the same running process. A
// holder recorded with creation time 0 is never compared: it is honoured while
// its PID exists and the record is younger than exclusionUnverifiedLimit.
func processAlive(holder processIdentity, begun, now time.Time) bool {
	withinLimit := now.Sub(begun) < exclusionUnverifiedLimit
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, holder.PID)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false
	}
	if err != nil {
		return withinLimit
	}
	defer windows.CloseHandle(h)
	if holder.Created == 0 {
		state, err := windows.WaitForSingleObject(h, 0)
		return withinLimit && err == nil && state == uint32(windows.WAIT_TIMEOUT)
	}
	created, err := handleCreated(h)
	if err != nil {
		return withinLimit
	}
	if created != holder.Created {
		return false
	}
	state, err := windows.WaitForSingleObject(h, 0)
	return err == nil && state == uint32(windows.WAIT_TIMEOUT)
}

// identityOps are the process queries installerIdentity makes. Tests substitute
// them; systemIdentityOps is the installed behaviour.
type identityOps struct {
	self func() uint32
	// parent returns the parent PID and image name from the process snapshot.
	parent func(pid uint32) (uint32, string, error)
	// created is this process's creation time.
	created func() (int64, error)
	// inspect opens pid and returns its creation time and full image path. An
	// OpenProcess failure is returned as *openProcessError.
	inspect func(pid uint32) (int64, string, error)
}

type openProcessError struct {
	pid uint32
	err error
}

func (e *openProcessError) Error() string {
	return fmt.Sprintf("open parent process %d: %v", e.pid, e.err)
}
func (e *openProcessError) Unwrap() error { return e.err }

func systemIdentityOps() identityOps {
	return identityOps{
		self:    func() uint32 { return uint32(os.Getpid()) },
		parent:  parentProcess,
		created: func() (int64, error) { return handleCreated(windows.CurrentProcess()) },
		inspect: inspectProcess,
	}
}

func installerIdentity() (processIdentity, error) {
	return installerIdentityWith(systemIdentityOps())
}

// installerIdentityWith is this custom action's parent: the Windows Installer
// process running the transaction script. A parent created after this process
// means its PID was reused, and is refused. A parent this account may not open
// is named by its snapshot PID and image with creation time 0.
func installerIdentityWith(ops identityOps) (processIdentity, error) {
	parent, name, err := ops.parent(ops.self())
	if err != nil {
		return processIdentity{}, err
	}
	own, err := ops.created()
	if err != nil {
		return processIdentity{}, err
	}
	created, image, err := ops.inspect(parent)
	var denied *openProcessError
	if errors.As(err, &denied) && errors.Is(denied.err, windows.ERROR_ACCESS_DENIED) {
		return processIdentity{PID: parent, Created: 0, Image: name}, nil
	}
	if err != nil {
		return processIdentity{}, err
	}
	if created <= 0 {
		return processIdentity{}, fmt.Errorf("parent process %d reported no creation time", parent)
	}
	if created > own {
		return processIdentity{}, fmt.Errorf("parent process %d was created after this process; its PID was reused", parent)
	}
	return processIdentity{PID: parent, Created: created, Image: image}, nil
}

func inspectProcess(pid uint32) (int64, string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, "", &openProcessError{pid: pid, err: err}
	}
	defer windows.CloseHandle(h)
	created, err := handleCreated(h)
	if err != nil {
		return 0, "", err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return 0, "", err
	}
	return created, windows.UTF16ToString(buf[:size]), nil
}

func parentProcess(pid uint32) (uint32, string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == pid {
			if entry.ParentProcessID == 0 {
				return 0, "", fmt.Errorf("process %d has no parent", pid)
			}
			parent := entry.ParentProcessID
			for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
				if entry.ProcessID == parent {
					return parent, windows.UTF16ToString(entry.ExeFile[:]), nil
				}
			}
			return 0, "", fmt.Errorf("parent process %d of %d is not in the process snapshot", parent, pid)
		}
	}
	return 0, "", fmt.Errorf("process %d is not in the process snapshot", pid)
}

func handleCreated(h windows.Handle) (int64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return created.Nanoseconds(), nil
}

func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func insideAnyFolder(image string, folders []string) bool {
	for _, folder := range folders {
		if insideFolder(image, folder) {
			return true
		}
	}
	return false
}

func insideFolder(image, folder string) bool {
	image = filepath.Clean(image)
	prefix := strings.TrimRight(filepath.Clean(folder), `\/`) + `\`
	return len(image) > len(prefix) && strings.EqualFold(image[:len(prefix)], prefix)
}

func longPath(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n > uint32(len(buf)) {
		return path
	}
	return windows.UTF16ToString(buf[:n])
}
