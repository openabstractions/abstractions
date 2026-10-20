package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// This is the installer's existing OA component set: OA's programs, then its
// helpers and predecessor supervisor names. A basename only narrows
// inspection; file identity admits a process. Copies outside the installed
// folders are not admitted by name.
var upgradeProcessNames = append(oaProgramFiles(oaProgramStems), upgradeHelperNames...)

var upgradeHelperNames = []string{
	"inventoryd.exe", "modelhostd.exe", "lemonaded.exe", "openabstractions-mcp.exe", "modelpack.exe",
	"openabstractions-system.exe", "thenoise-provider.exe", "pytorchd.exe", "jobd.exe", "jobdw.exe",
}

type upgradeStopProcess interface {
	requestClose() error
	wait(time.Duration) (bool, error)
	terminate() error
	close() error
}

func holdAndStopUpgrade(scope string, folders []string, holder processIdentity, env exclusionEnv,
	stop func(context.Context, []string) error) error {
	return holdAndStopUpgradeAdmission(scope, folders, holder, env, nil, stop)
}

func holdAndStopUpgradeAdmission(scope string, folders []string, holder processIdentity, env exclusionEnv,
	admit, stop func(context.Context, []string) error, chain ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if admit != nil {
		if err := admit(ctx, folders); err != nil {
			return fmt.Errorf("upgrade exclusion stage=read-only-admission scope=%s holder_pid=%d: %w", scope, holder.PID, err)
		}
	}
	if err := env.beginContext(ctx, scope, folders, holder, chain...); err != nil {
		return fmt.Errorf("upgrade exclusion stage=hold scope=%s holder_pid=%d folders=%q: %w", scope, holder.PID, folders, err)
	}
	if err := stop(ctx, folders); err != nil {
		return fmt.Errorf("upgrade exclusion stage=stop scope=%s holder_pid=%d folders=%q: %w", scope, holder.PID, folders, err)
	}
	return nil
}

func stopUpgradeProcesses(ctx context.Context, folders []string, census func([]string) ([]upgradeStopProcess, error)) error {
	// Rescan after exit so a late activation cannot be mistaken for quiescence.
	for pass := 0; pass < 3; pass++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		processes, err := census(folders)
		if err != nil {
			return err
		}
		if len(processes) == 0 {
			return nil
		}
		var failure error
		for _, process := range processes {
			if err := ctx.Err(); err != nil {
				failure = errors.Join(failure, err, process.close())
				continue
			}
			// A cooperative close may be unavailable for a console helper. The
			// same retained handle is the only permitted fallback target.
			_ = process.requestClose()
			exited, err := process.wait(upgradeWait(ctx, 2*time.Second))
			if err == nil && !exited && ctx.Err() == nil {
				err = process.terminate()
				if err == nil {
					exited, err = process.wait(upgradeWait(ctx, 5*time.Second))
				}
			}
			if err == nil && !exited {
				err = errors.New("installed OA process did not exit before file replacement")
			}
			failure = errors.Join(failure, err, process.close())
		}
		if failure != nil {
			return failure
		}
	}
	return errors.New("installed OA processes kept restarting during upgrade exclusion")
}

func upgradeWait(ctx context.Context, maximum time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline); left < maximum {
			if left < 0 {
				return 0
			}
			return left
		}
	}
	return maximum
}

func installedUpgradeImage(image string, folders []string) (bool, error) {
	key, err := fileid.FileKey(image)
	if err != nil {
		return false, fmt.Errorf("read candidate file identity %q: %w", image, err)
	}
	for _, folder := range folders {
		for _, name := range upgradeProcessNames {
			expected := filepath.Join(folder, "tools", name)
			expectedKey, err := fileid.FileKey(expected)
			if errors.Is(err, os.ErrNotExist) {
				continue // Optional or predecessor component absent.
			}
			if err != nil {
				return false, fmt.Errorf("read installed file identity %q: %w", expected, err)
			}
			if key == expectedKey {
				return true, nil
			}
		}
	}
	return false, nil
}

type nativeUpgradeProcess struct {
	handle  windows.Handle
	pid     uint32
	image   string
	release func(windows.Handle) error
}

func (p *nativeUpgradeProcess) problem(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("upgrade census stage=%s pid=%d image=%q: %w", stage, p.pid, p.image, err)
}
func (p *nativeUpgradeProcess) close() error {
	close := p.release
	if close == nil {
		close = windows.CloseHandle
	}
	return p.problem("close-handle", close(p.handle))
}
func (p *nativeUpgradeProcess) terminate() error {
	return p.problem("terminate-owned", windows.TerminateProcess(p.handle, 0))
}
func (p *nativeUpgradeProcess) wait(budget time.Duration) (bool, error) {
	status, err := windows.WaitForSingleObject(p.handle, uint32(budget/time.Millisecond))
	if err != nil {
		return false, p.problem("wait-owned", err)
	}
	if status != windows.WAIT_OBJECT_0 && status != uint32(windows.WAIT_TIMEOUT) {
		return false, p.problem("wait-owned", fmt.Errorf("installed OA process wait returned %d", status))
	}
	return status == windows.WAIT_OBJECT_0, nil
}
func (p *nativeUpgradeProcess) requestClose() error {
	enumerate := user32.NewProc("EnumWindows")
	windowPID := user32.NewProc("GetWindowThreadProcessId")
	var failure error
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		windowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == p.pid {
			if exited, err := p.wait(0); err != nil {
				failure = err
			} else if !exited {
				if ok, _, err := postMessage.Call(hwnd, wmClose, 0, 0); ok == 0 {
					failure = err
				}
			}
		}
		return 1
	})
	if ok, _, err := enumerate.Call(callback, 0); ok == 0 {
		return err
	}
	return failure
}

type upgradeCandidate struct {
	PID  uint32 `json:"pid"`
	Name string `json:"snapshot_name"`
}

type upgradeCensusObservation struct {
	upgradeCandidate
	Image   string `json:"image,omitempty"`
	Created int64  `json:"created,omitempty"`
	Stage   string `json:"stage"`
	Error   string `json:"error,omitempty"`
}

type earlyUpgradeCensus struct {
	named, admitted, unrelated, exited int
}

func (c *earlyUpgradeCensus) observe(row upgradeCensusObservation) {
	if !identity.SamePathName(row.Name, "openabstractions.exe") {
		return
	}
	c.named++
	switch row.Stage {
	case "installed-stop-admitted":
		c.admitted++
	case "unrelated-file":
		c.unrelated++
	default:
		if row.Error == "" && (row.Stage == "gone-before-query" || strings.HasPrefix(row.Stage, "exited-")) {
			c.exited++
		}
	}
}

type earlyUpgradeStop struct {
	folders, scans int
	holderPID      uint32
	guard          string
	admission      earlyUpgradeCensus
	held           earlyUpgradeCensus
}

func (r earlyUpgradeStop) summary() string {
	return fmt.Sprintf("folders=%d holder_pid=%d guard=%s held_scans=%d "+
		"admission_core_named=%d admission_core_admitted=%d admission_core_unrelated=%d admission_core_exited=%d "+
		"held_core_named=%d held_core_admitted=%d held_core_unrelated=%d held_core_exited=%d",
		r.folders, r.holderPID, r.guard, r.scans,
		r.admission.named, r.admission.admitted, r.admission.unrelated, r.admission.exited,
		r.held.named, r.held.admitted, r.held.unrelated, r.held.exited)
}

func earlyUpgradeGuard(env exclusionEnv, scope string, folders []string) (string, uint32) {
	record, _, err := env.load(scope)
	if err != nil {
		return "unavailable", 0
	}
	if record == nil {
		return "absent", 0
	}
	for _, folder := range folders {
		if !containsFolder(record.Folders, folder) {
			return "incomplete", record.Installer.PID
		}
	}
	if !env.alive(record.Installer, record.Begun) {
		return "inactive", record.Installer.PID
	}
	return "active", record.Installer.PID
}

type upgradeCensusOps struct {
	list       func() ([]upgradeCandidate, error)
	open       func(uint32, uint32) (windows.Handle, error)
	image      func(windows.Handle) (string, error)
	created    func(windows.Handle) (int64, error)
	exited     func(windows.Handle) (bool, error)
	sameObject func(windows.Handle, windows.Handle) (bool, error)
	close      func(windows.Handle) error
	classify   func(string, []string) (bool, error)
}

func systemUpgradeCensusOps() upgradeCensusOps {
	return upgradeCensusOps{
		list: upgradeProcessSnapshot,
		open: func(pid, access uint32) (windows.Handle, error) { return windows.OpenProcess(access, false, pid) },
		image: func(handle windows.Handle) (string, error) {
			buffer := make([]uint16, windows.MAX_LONG_PATH)
			size := uint32(len(buffer))
			if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
				return "", err
			}
			return windows.UTF16ToString(buffer[:size]), nil
		},
		created: handleCreated,
		exited: func(handle windows.Handle) (bool, error) {
			state, err := windows.WaitForSingleObject(handle, 0)
			if err != nil {
				return false, err
			}
			if state != windows.WAIT_OBJECT_0 && state != uint32(windows.WAIT_TIMEOUT) {
				return false, fmt.Errorf("unexpected wait state %d", state)
			}
			return state == windows.WAIT_OBJECT_0, nil
		},
		sameObject: func(a, b windows.Handle) (bool, error) {
			compare := windows.NewLazySystemDLL("kernelbase.dll").NewProc("CompareObjectHandles")
			if err := compare.Find(); err != nil {
				return false, err
			}
			same, _, _ := compare.Call(uintptr(a), uintptr(b))
			return same != 0, nil
		},
		close:    windows.CloseHandle,
		classify: installedUpgradeImage,
	}
}

func upgradeProcessSnapshot() (result []upgradeCandidate, failure error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("upgrade census stage=snapshot: %w", err)
	}
	defer func() {
		if err := windows.CloseHandle(snapshot); err != nil {
			failure = errors.Join(failure, fmt.Errorf("upgrade census stage=close-snapshot: %w", err))
		}
	}()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		result = append(result, upgradeCandidate{PID: entry.ProcessID, Name: windows.UTF16ToString(entry.ExeFile[:])})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("upgrade census stage=enumerate: %w", err)
	}
	return result, nil
}

func nativeUpgradeCensus(folders []string) ([]upgradeStopProcess, error) {
	return censusUpgradeProcesses(folders, systemUpgradeCensusOps(), nil)
}

func censusUpgradeProcesses(folders []string, ops upgradeCensusOps, observe func(upgradeCensusObservation)) (result []upgradeStopProcess, failure error) {
	return censusUpgradeProcessesContext(context.Background(), folders, ops, observe)
}

func censusUpgradeProcessesContext(ctx context.Context, folders []string, ops upgradeCensusOps, observe func(upgradeCensusObservation)) (result []upgradeStopProcess, failure error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidates, err := ops.list()
	if err != nil {
		return nil, fmt.Errorf("upgrade census stage=snapshot: %w", err)
	}
	for _, candidate := range candidates {
		if candidate.PID == uint32(os.Getpid()) {
			continue
		}
		matched := false
		for _, name := range upgradeProcessNames {
			matched = matched || identity.SamePathName(candidate.Name, name)
		}
		if !matched {
			continue
		}
		process, row, err := admitUpgradeCandidateContext(ctx, candidate, folders, ops)
		if err != nil {
			row.Error = err.Error()
			failure = errors.Join(failure, err)
		}
		if observe != nil {
			observe(row)
		}
		if process != nil {
			result = append(result, process)
		}
	}
	if failure != nil {
		for _, process := range result {
			failure = errors.Join(failure, process.close())
		}
		return nil, failure
	}
	return result, nil
}

func admitUpgradeCandidate(candidate upgradeCandidate, folders []string, ops upgradeCensusOps) (process *nativeUpgradeProcess, row upgradeCensusObservation, failure error) {
	return admitUpgradeCandidateContext(context.Background(), candidate, folders, ops)
}

func admitUpgradeCandidateContext(ctx context.Context, candidate upgradeCandidate, folders []string, ops upgradeCensusOps) (process *nativeUpgradeProcess, row upgradeCensusObservation, failure error) {
	row.upgradeCandidate = candidate
	stage := func(name string, err error) error {
		row.Stage = name
		return fmt.Errorf("upgrade census stage=%s pid=%d snapshot=%q image=%q: %w", name, candidate.PID, candidate.Name, row.Image, err)
	}
	// A failed initial open has no retained handle. Only a fresh snapshot with
	// this PID absent can establish that the candidate is gone.
	query, err := ops.open(candidate.PID, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE)
	if err != nil {
		fresh, snapshotErr := ops.list()
		if snapshotErr == nil {
			gone := true
			for _, entry := range fresh {
				gone = gone && entry.PID != candidate.PID
			}
			if gone {
				row.Stage = "gone-before-query"
				return nil, row, nil
			}
		}
		failure = stage("open-query", errors.Join(err, snapshotErr))
		return nil, row, failure
	}
	defer func() {
		if err := ops.close(query); err != nil {
			failure = errors.Join(failure, stage("close-query", err))
		}
		if failure != nil && process != nil {
			failure = errors.Join(failure, process.close())
			process = nil
		}
	}()
	// A classification/read/right-acquisition race can be skipped only when
	// the original retained process object is signalled, never by error code.
	raceFailure := func(name string, err error) error {
		exited, waitErr := ops.exited(query)
		if waitErr == nil && exited {
			row.Stage = "exited-during-" + name
			return nil
		}
		return stage(name, errors.Join(err, waitErr))
	}
	// Image metadata may become unavailable while Windows is still completing
	// process termination. Keep this exact query object and await metadata or its
	// exit signal within the caller's existing action budget. An error alone
	// never establishes that a process is gone or belongs to this installation.
	for _, metadata := range []struct {
		name string
		read func() error
	}{
		{"query-birth", func() error { var err error; row.Created, err = ops.created(query); return err }},
		{"query-image", func() error { var err error; row.Image, err = ops.image(query); return err }},
	} {
		exited, err := awaitUpgradeMetadata(ctx, query, ops, metadata.read)
		if exited {
			row.Stage = "exited-during-" + metadata.name
			return nil, row, nil
		}
		if err != nil {
			failure = stage(metadata.name, err)
			return nil, row, failure
		}
	}
	admitted, err := ops.classify(row.Image, folders)
	if err != nil {
		failure = raceFailure("classify-file", err)
		return nil, row, failure
	}
	if !admitted {
		row.Stage = "unrelated-file"
		return nil, row, nil
	}
	if exited, err := ops.exited(query); err != nil {
		failure = stage("wait-before-stop-rights", err)
		return nil, row, failure
	} else if exited {
		row.Stage = "exited-before-stop-rights"
		return nil, row, nil
	}
	// Stop rights are requested only for an installed file identity. Keep the
	// query handle alive through PID/birth/object revalidation of the new handle.
	if err := ctx.Err(); err != nil {
		failure = stage("before-stop-rights", err)
		return nil, row, failure
	}
	stop, err := ops.open(candidate.PID, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE)
	if err != nil {
		failure = raceFailure("open-stop-rights", err)
		return nil, row, failure
	}
	keep := false
	defer func() {
		if !keep {
			if err := ops.close(stop); err != nil {
				failure = errors.Join(failure, stage("close-stop", err))
			}
		}
	}()
	created, err := ops.created(stop)
	if err != nil {
		failure = raceFailure("stop-birth", err)
		return nil, row, failure
	}
	if created != row.Created {
		failure = stage("stop-birth", errors.New("process birth changed after classification"))
		return nil, row, failure
	}
	same, err := ops.sameObject(query, stop)
	if err != nil {
		failure = stage("stop-object", err)
		return nil, row, failure
	}
	if !same {
		failure = stage("stop-object", errors.New("process object changed after classification"))
		return nil, row, failure
	}
	if exited, err := ops.exited(query); err != nil {
		failure = stage("wait-after-stop-rights", err)
		return nil, row, failure
	} else if exited {
		row.Stage = "exited-after-stop-rights"
		return nil, row, nil
	}
	row.Stage = "installed-stop-admitted"
	keep = true
	return &nativeUpgradeProcess{handle: stop, pid: candidate.PID, image: row.Image, release: ops.close}, row, nil
}

func awaitUpgradeMetadata(ctx context.Context, query windows.Handle, ops upgradeCensusOps, read func() error) (bool, error) {
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return false, errors.Join(lastErr, err)
		}
		err := read()
		if err == nil {
			return false, nil
		}
		lastErr = err
		exited, waitErr := ops.exited(query)
		if waitErr != nil {
			return false, errors.Join(err, waitErr)
		}
		if exited {
			return true, nil
		}
		_, bounded := ctx.Deadline()
		if !bounded || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return false, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}
