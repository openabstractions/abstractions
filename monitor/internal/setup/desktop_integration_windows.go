//go:build windows

package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unsafe"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"golang.org/x/sys/windows"
)

func (p DesktopIntegration) publishStaged(ctx context.Context, dir, source string) (err error) {
	digest, err := hex.DecodeString(p.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("invalid desktop integration package digest")
	}
	target := filepath.Join(dir, p.File)
	leaf, err := fileid.ResolveLeaf(ctx, fileid.Self, target)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	out, err := os.CreateTemp(dir, leaf.Name()+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	var written int64
	buffer := make([]byte, archiveChunk)
	if err := copyBounded(ctx, out, in, buffer, &written, p.Bytes); err != nil {
		return errors.Join(err, out.Close())
	}
	if written != p.Bytes {
		return errors.Join(fmt.Errorf("staged desktop installer size = %d, expected %d", written, p.Bytes), out.Close())
	}
	if err := out.Sync(); err != nil {
		return errors.Join(err, out.Close())
	}
	stage, err := fileid.FromFile(ctx, out)
	closeErr := out.Close()
	if err != nil {
		return errors.Join(err, closeErr)
	}
	id := stage.FileID()
	if err := errors.Join(closeErr, stage.Close()); err != nil {
		return err
	}
	return cas.ReplaceStagedContext(ctx, target, out.Name(), id, [sha256.Size]byte(digest), p.Bytes)
}

func (p DesktopIntegration) packageState() (string, bool, error) {
	return p.installerSelection(relatedProducts, getProductInfo)
}

func (p DesktopIntegration) installerSelection(related func(string) ([]string, error), info func(string, string) (string, error)) (action string, reinstall bool, err error) {
	products, err := related(p.UpgradeCode)
	if err != nil {
		return "", false, err
	}
	switch len(products) {
	case 0:
		return "install", false, nil
	case 1:
		installed := products[0]
		assignment, err := info(installed, "AssignmentType")
		if err != nil || assignment != "0" {
			return "", false, errors.Join(err, errors.New("The installed integration is not owned by this account."))
		}
		if installed != p.ProductCode {
			return "update", false, nil
		}
		if p.PackageCode != "" {
			code, err := info(installed, "PackageCode")
			if err != nil || !fixupUpgradeCode.MatchString(code) {
				return "", false, errors.Join(err, errors.New("Windows Installer could not identify the installed integration package."))
			}
			if code != p.PackageCode {
				return "update", true, nil
			}
		}
		return "repair", true, nil
	default:
		return "", false, errors.New("Windows Installer found multiple packages for this integration.")
	}
}

func (p DesktopIntegration) Running() (bool, error) {
	official, found := OfficialProgramOf(p.App)
	if !found {
		return false, errors.New("The desktop integration has no official host in the Catalog.")
	}
	local, err := layout.LocalAppData()
	if err != nil {
		return false, err
	}
	return folderProgramRunning(filepath.Join(local, filepath.Dir(filepath.FromSlash(official.LocalAppData))))
}

func (p DesktopIntegration) runPackage(ctx context.Context, path, action string) error {
	return p.runPackageWithAdmission(ctx, path, action, nil)
}

func (p DesktopIntegration) runPackageWithAdmission(ctx context.Context, path, action string, keep func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	running, err := p.Running()
	if err != nil {
		return err
	}
	if running {
		return &DesktopInstallerFailure{Reason: fmt.Sprintf("Close %s completely, then try again.", p.Title), diagnostic: ErrDesktopIntegrationRunning}
	}
	packagePath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	installerSettingsMu.Lock()
	defer installerSettingsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, reinstall, err := p.packageState()
	if err != nil || current != action {
		return errors.Join(err, errors.New("The desktop integration changed. Check the Catalog again."))
	}
	properties := `ALLUSERS="" REBOOT=ReallySuppress`
	if reinstall {
		// Re-cache the exact held source and replace files whose versions
		// stayed the same, including for an update within one product code.
		properties += ` REINSTALL=ALL REINSTALLMODE=vamus`
	}
	if keep != nil {
		properties += ` OAKEEPMAINTENANCE=1`
	}
	command, _ := windows.UTF16PtrFromString(properties)
	previous, _, _ := setInternalUI.Call(installUILevelNone, 0)
	defer setInternalUI.Call(previous, 0)
	logFile := ""
	diagnostics, projectLog := product.DesktopInstallerLog(string(p.App))
	if projectLog {
		if local, err := layout.LocalAppData(); err == nil {
			if dir, release, err := privateInstallerLogDir(local); err == nil {
				defer release()
				candidate := filepath.Join(dir, "install.log")
				defer os.Remove(candidate)
				name, _ := windows.UTF16PtrFromString(candidate)
				const installLogInfoAndActions = 0x10 | 0x100
				if result, _, _ := enableLog.Call(installLogInfoAndActions, uintptr(unsafe.Pointer(name)), installLogFlushEach); result == msiSuccess {
					logFile = candidate
				}
			}
		}
	}
	logging := logFile != ""
	defer func() {
		if logging {
			enableLog.Call(0, 0, 0)
		}
	}()
	if keep != nil {
		if err := keep(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	code, _, _ := installProduct.Call(uintptr(unsafe.Pointer(packagePath)), uintptr(unsafe.Pointer(command)))
	if logging {
		enableLog.Call(0, 0, 0)
		logging = false
	}
	if err := msiResult(code, "apply the desktop integration"); err != nil {
		if code == 1603 && projectLog {
			log, status := desktopInstallerLog(logFile)
			failure := newInstallerDiagnostics(diagnostics).failure(err, log, status)
			if running, checkErr := p.Running(); checkErr == nil && running {
				return errors.Join(failure, ErrDesktopIntegrationRunning)
			}
			return failure
		}
		return err
	}
	if after, _, err := p.packageState(); err != nil || after != "repair" {
		return errors.Join(err, errors.New("Windows Installer did not confirm the expected desktop integration."))
	}
	return nil
}

// Hold the account-local KnownFolder across creation and lifetime of the
// log directory. Make applies the file-inheritable private DACL atomically.
func privateInstallerLogDir(local string) (string, func(), error) {
	parent, err := programfiles.HoldDirectory(local)
	if err != nil {
		return "", nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, errors.Join(err, parent.Release())
	}
	dir := filepath.Join(local, "oa-msi-"+hex.EncodeToString(random[:]))
	if err := privatedir.Make(dir); err != nil {
		return "", nil, errors.Join(err, parent.Release())
	}
	return dir, func() {
		_ = os.Remove(dir)
		_ = parent.Release()
	}, nil
}

type installerDiagnostics struct {
	title, processAction, settingsAction, settingsConflictExit string
	guard, action, stage, child                                *regexp.Regexp
}

func newInstallerDiagnostics(spec product.DesktopInstallerDiagnostics) installerDiagnostics {
	group := func(names []string) string {
		quoted := make([]string, len(names))
		for i, name := range names {
			quoted[i] = regexp.QuoteMeta(name)
		}
		return "(" + strings.Join(quoted, "|") + ")"
	}
	prefix := "^" + regexp.QuoteMeta(spec.Prefix)
	return installerDiagnostics{
		title: spec.Title, processAction: spec.ProcessAction,
		settingsAction: spec.SettingsAction, settingsConflictExit: spec.SettingsConflictExit,
		guard:  regexp.MustCompile(prefix + ` guard stage=(desktop_process_running|desktop_process_identity_unread) win32=([0-9]{1,10}) hresult=-?[0-9]{1,11} exception=0(?: pid=([1-9][0-9]{0,9}))?$`),
		action: regexp.MustCompile(`^Action ended [0-9]{1,2}:[0-9]{2}:[0-9]{2}: ` + group(spec.Actions) + `\. Return value ([0-8])\.$`),
		stage:  regexp.MustCompile(prefix + ` guard stage=` + group(spec.FailureStages) + ` win32=(-?[0-9]{1,10}) hresult=(-?[0-9]{1,10}) exception=(-?[0-9]{1,10})$`),
		child:  regexp.MustCompile(prefix + ` child stage=` + group(spec.ChildStages) + ` win32=(-?[0-9]{1,10}) wait=(-?[0-9]{1,10}) exit=(-?[0-9]{1,10})$`),
	}
}

func (d installerDiagnostics) settingsConflict(text string) bool {
	if d.settingsAction == "" || d.settingsConflictExit == "" {
		return false
	}
	inSettings, conflict := false, false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Action start ") {
			inSettings = strings.HasSuffix(line, ": "+d.settingsAction+".")
			conflict = false
		} else if inSettings {
			if strings.HasPrefix(line, "Action ended ") {
				if action := d.action.FindStringSubmatch(line); conflict && action != nil && action[1] == d.settingsAction && action[2] == "3" {
					return true
				}
				inSettings = false
			} else if child := d.child.FindStringSubmatch(line); child != nil && child[1] == "configure" &&
				child[2] == "0" && child[3] == "0" && child[4] == d.settingsConflictExit {
				conflict = true
			}
		}
	}
	return false
}

func (d installerDiagnostics) guardMessage(text string) string {
	var message string
	inGuard := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Action start ") && strings.HasSuffix(line, ": "+d.processAction+".") {
			inGuard, message = true, ""
			continue
		}
		if !inGuard {
			continue
		}
		if strings.HasPrefix(line, "Action ended ") {
			if strings.HasSuffix(line, ": "+d.processAction+". Return value 3.") {
				return message
			}
			inGuard, message = false, ""
			continue
		}
		if matched := d.guard.FindStringSubmatch(line); matched != nil {
			if matched[1] == "desktop_process_running" && matched[2] == "170" {
				message = fmt.Sprintf("Close %s completely, then retry from Catalog in Panel.", d.title)
			} else if matched[1] == "desktop_process_identity_unread" && matched[2] != "0" {
				message = fmt.Sprintf("Setup could not verify that %s is closed. Close %s completely, then retry from Catalog in Panel.", d.title, d.title)
				if matched[3] != "" {
					message = fmt.Sprintf("Setup could not verify that %s is closed. Close %s completely. If you recognize process ID %s, close that application too. Then retry from Catalog in Panel.", d.title, d.title, matched[3])
				}
			}
		}
	}
	return ""
}

const desktopInstallLogLimit = 256 << 10

func desktopInstallerLog(path string) (text, status string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "log unavailable"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", "log unavailable"
	}
	var bom [2]byte
	_, _ = f.ReadAt(bom[:], 0)
	unicode := bom == [2]byte{0xff, 0xfe}
	start := int64(0)
	if unicode {
		start = 2
	}
	if info.Size()-start > desktopInstallLogLimit {
		start = info.Size() - desktopInstallLogLimit
		if unicode && start%2 != 0 {
			start++
		}
		status = "log tail only"
	}
	raw, err := io.ReadAll(io.NewSectionReader(f, start, info.Size()-start))
	if err != nil {
		return "", "log unavailable"
	}
	text = decodeMSIText(raw, unicode)
	if status != "" {
		// The first line may have started before the bounded tail.
		_, text, _ = strings.Cut(text, "\n")
	}
	return text, status
}

func (d installerDiagnostics) failure(failure error, text, status string) error {
	reason := d.guardMessage(text)
	switch {
	case reason != "":
		failure = errors.New(reason)
	case d.settingsConflict(text):
		reason = fmt.Sprintf("Couldn't update %s's settings. Close %s and back up its settings files. Remove only the OpenAbstractions entry from each affected plugin list, then select Retry.", d.title, d.title)
	default:
		reason = fmt.Sprintf("Couldn't finish setting up %s. Try again from the Panel.", d.title)
	}
	// Keep only the installer's fixed event names and codes in the diagnostic.
	const limit = 8
	var details []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		var detail string
		if action := d.action.FindStringSubmatch(line); action != nil {
			detail = fmt.Sprintf("%s return=%s", action[1], action[2])
		} else if guard := d.guard.FindStringSubmatch(line); guard != nil {
			detail = fmt.Sprintf("guard %s win32=%s", guard[1], guard[2])
		} else if stage := d.stage.FindStringSubmatch(line); stage != nil {
			detail = fmt.Sprintf("guard %s win32=%s hresult=%s exception=%s", stage[1], stage[2], stage[3], stage[4])
		} else if child := d.child.FindStringSubmatch(line); child != nil {
			detail = fmt.Sprintf("child %s win32=%s wait=%s exit=%s", child[1], child[2], child[3], child[4])
		}
		if detail != "" {
			if len(details) == limit {
				copy(details, details[1:])
				details = details[:limit-1]
			}
			details = append(details, detail)
		}
	}
	if len(details) == 0 && status != "log unavailable" {
		details = append(details, "no recognized events")
	}
	if status != "" {
		details = append(details, status)
	}
	return &DesktopInstallerFailure{
		Reason:     reason,
		diagnostic: fmt.Errorf("%w\n%s MSI: %s", failure, d.title, strings.Join(details, "; ")),
	}
}
