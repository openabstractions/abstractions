package main

import (
	"context"
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

// A program Windows cannot close, such as antivirus, a search indexer or a
// provisioning daemon (provjobd.exe in hosted run 44999333520), can hold a
// file of the installation for a moment. Windows Installer checks holders at
// InstallValidate. A holder that Restart Manager reports as a service or a
// critical process makes it schedule the file for the next restart, and a
// quiet removal then ends with a silent pending reboot. Before that check,
// the removal waits briefly with backoff while such a holder remains. OA's
// own processes are the removal's own to close and never extend the wait.

const (
	removalReleaseBudget  = 20 * time.Second
	removalReleaseFirst   = 250 * time.Millisecond
	removalReleaseLongest = 4 * time.Second
	rmService             = 3
	rmCritical            = 1000
	rmMaxRemovalHolders   = 64
)

type removalHolder struct {
	pid     uint32
	name    string
	appType uint32
	image   string
}

// removalRestartSentence is the plain line a person reads when a holder
// outlasts the wait.
func removalRestartSentence(held []removalHolder) string {
	name := ""
	for _, holder := range held {
		if holder.name != "" {
			name = holder.name
			break
		}
	}
	if name == "" {
		return "Another program is using OpenAbstractions files. Restart your computer to finish removing OpenAbstractions."
	}
	return name + " is using OpenAbstractions files. Restart your computer to finish removing OpenAbstractions."
}

// removalBlockers keeps the holders Restart Manager will not close: services
// and critical processes outside the installation folder, other than this
// process, which Restart Manager reports as critical to itself.
func removalBlockers(holders []removalHolder, folder string) []removalHolder {
	var blockers []removalHolder
	for _, holder := range holders {
		if holder.appType != rmService && holder.appType != rmCritical || holder.pid == uint32(os.Getpid()) {
			continue
		}
		if holder.image != "" && withinFolder(holder.image, folder) {
			continue
		}
		blockers = append(blockers, holder)
	}
	return blockers
}

func withinFolder(path, folder string) bool {
	rel, err := filepath.Rel(filepath.Clean(folder), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// awaitRemovalRelease asks for the holders of files until no blocker remains
// or the budget ends, and returns the blockers still holding files then.
func awaitRemovalRelease(ctx context.Context, folder string, files []string,
	holders func([]string) ([]removalHolder, error), sleep func(context.Context, time.Duration) bool) ([]removalHolder, error) {
	if len(files) == 0 {
		return nil, nil
	}
	wait := removalReleaseFirst
	for {
		found, err := holders(files)
		if err != nil {
			return nil, err
		}
		blockers := removalBlockers(found, folder)
		if len(blockers) == 0 {
			return nil, nil
		}
		if !sleep(ctx, wait) {
			return blockers, nil
		}
		if wait *= 2; wait > removalReleaseLongest {
			wait = removalReleaseLongest
		}
	}
}

// installedFiles lists the regular files under folder. An unreadable entry is
// skipped: the wait is advice, never a refusal.
func installedFiles(folder string) []string {
	var files []string
	//unchecked: the walk callback skips every unreadable entry, and a partial list only shortens the advisory wait
	_ = filepath.WalkDir(folder, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	return files
}

var (
	removalRestartManager = windows.NewLazySystemDLL("rstrtmgr.dll")
	removalRmStart        = removalRestartManager.NewProc("RmStartSession")
	removalRmEnd          = removalRestartManager.NewProc("RmEndSession")
	removalRmRegister     = removalRestartManager.NewProc("RmRegisterResources")
	removalRmGetList      = removalRestartManager.NewProc("RmGetList")
)

type removalRmUniqueProcess struct {
	PID   uint32
	Start windows.Filetime
}

type removalRmProcessInfo struct {
	Process     removalRmUniqueProcess
	AppName     [256]uint16
	ServiceName [64]uint16
	AppType     uint32
	AppStatus   uint32
	Session     uint32
	Restartable int32
}

// restartManagerHolders asks Restart Manager, as Windows Installer does,
// which processes hold any of files.
func restartManagerHolders(files []string) ([]removalHolder, error) {
	var session uint32
	var key [33]uint16
	//unchecked: Restart Manager returns its error code in r, which is checked; err is GetLastError
	if r, _, _ := removalRmStart.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return nil, fmt.Errorf("Restart Manager session: %w", windows.Errno(r))
	}
	defer removalRmEnd.Call(uintptr(session))
	names := make([]*uint16, 0, len(files))
	for _, file := range files {
		name, err := windows.UTF16PtrFromString(file)
		if err != nil {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil
	}
	//unchecked: Restart Manager returns its error code in r, which is checked; err is GetLastError
	if r, _, _ := removalRmRegister.Call(uintptr(session), uintptr(len(names)), uintptr(unsafe.Pointer(&names[0])), 0, 0, 0, 0); r != 0 {
		return nil, fmt.Errorf("Restart Manager registration: %w", windows.Errno(r))
	}
	info := make([]removalRmProcessInfo, rmMaxRemovalHolders)
	var needed, reasons uint32
	count := uint32(len(info))
	//unchecked: Restart Manager returns its error code in r, which is checked; err is GetLastError
	r, _, _ := removalRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)),
		uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&reasons)))
	if r == uintptr(windows.ERROR_MORE_DATA) {
		count = uint32(len(info))
	} else if r != 0 {
		return nil, fmt.Errorf("Restart Manager list: %w", windows.Errno(r))
	}
	if count > uint32(len(info)) {
		count = uint32(len(info))
	}
	holders := make([]removalHolder, 0, count)
	for _, entry := range info[:count] {
		holders = append(holders, removalHolder{pid: entry.Process.PID, name: windows.UTF16ToString(entry.AppName[:]),
			appType: entry.AppType, image: processImage(entry.Process.PID)})
	}
	return holders, nil
}

// processImage is a process's program path, or "" when this caller cannot
// read it.
func processImage(pid uint32) string {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size) != nil {
		return ""
	}
	return windows.UTF16ToString(buffer[:size])
}

var removalMessageBox = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

// serviceAwaitRelease runs before Windows Installer's InstallValidate on a
// removal. It always succeeds: the removal goes on, and when a holder stays
// the person reads that a restart finishes it. A full installer window
// (UILevel 5) shows the sentence; every run writes it to the output and to
// installer-actions.txt.
func serviceAwaitRelease(request serviceRequest, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), removalReleaseBudget)
	defer cancel()
	held, err := awaitRemovalRelease(ctx, request.folder, installedFiles(request.folder), restartManagerHolders, sleepContext)
	if err != nil {
		appendInstallerAction(installerActionsPath, request.scope, time.Now().UTC().Format(time.RFC3339)+
			" service await-release --"+request.scope+": "+strings.Join(strings.Fields(err.Error()), " ")+"\n")
		return nil
	}
	if len(held) == 0 {
		return printLine(output, "no other program holds the installation's files")
	}
	sentence := removalRestartSentence(held)
	appendInstallerAction(installerActionsPath, request.scope, time.Now().UTC().Format(time.RFC3339)+
		" service await-release --"+request.scope+": "+sentence+"\n")
	if request.uiLevel == "5" {
		text, _ := windows.UTF16PtrFromString(sentence)
		title, _ := windows.UTF16PtrFromString("OpenAbstractions")
		//unchecked: the sentence is also printed and written to installer-actions.txt; a box that cannot show loses nothing
		removalMessageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), uintptr(windows.MB_OK|windows.MB_ICONINFORMATION))
	}
	return printLine(output, sentence)
}
