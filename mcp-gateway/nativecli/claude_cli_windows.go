package nativecli

import (
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

var claudeVersionLine = regexp.MustCompile(`^([1-9][0-9]{0,3}\.[0-9]{1,4}\.[0-9]{1,6}) \(Claude Code\)\r?\n?$`)

var errOtherClaudeImage = errors.New("native image is outside this product selection")
var errProductProcessEnded = errors.New("native product process ended")

// ClaudeCLI retains a current native Code process and its immutable image hold.
// Version is the bounded vendor CLI response, checked while that hold is live.
type ClaudeCLI struct {
	image      nativeProductImage
	version    string
	mu         sync.Mutex
	processes  []claudeProcessImage
	management bool
	closed     bool
}

type nativeProductImage interface {
	Program() string
	FileKey() string
	Check(context.Context) error
	Close() error
}

func (c *ClaudeCLI) Program() string { return c.image.Program() }
func (c *ClaudeCLI) FileKey() string { return c.image.FileKey() }

func (c *ClaudeCLI) Version() string { return c.version }
func (c *ClaudeCLI) Check(ctx context.Context) error {
	if c == nil {
		return errors.New("Code selection unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || (!c.management && len(c.processes) == 0) {
		return errors.New("Code selection is closed")
	}
	if err := c.image.Check(ctx); err != nil {
		return err
	}
	if c.management {
		return ctx.Err()
	}
	for _, p := range c.processes {
		if p.current() == nil {
			return ctx.Err()
		}
	}
	return errors.New("Code is no longer running; open it and retry")
}
func (c *ClaudeCLI) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var errs []error
	for _, p := range c.processes {
		errs = append(errs, p.close())
	}
	c.processes = nil
	if c.image != nil {
		errs = append(errs, c.image.Close())
	}
	return errors.Join(errs...)
}

// SelectClaudeSavedCLI uses the existing C1 ownership locator only to find an
// installed image. Fresh file identity, PE, registered publisher and bounded
// version checks supply command admission. A running Code session is unnecessary.
func SelectClaudeSavedCLI(ctx context.Context, stored []string, probe func(context.Context, string) ([]byte, error)) (*ClaudeCLI, error) {
	return selectClaudeSavedCLI(ctx, stored, probe, followingClaudeImage(holdSignedClaudeImage))
}

// HoldClaudeSavedCLI authenticates and holds the current saved image and its
// registered publisher anchor. It performs no process discovery or invocation.
// Stored IDs locate configured paths; the returned native hold owns identity.
func HoldClaudeSavedCLI(ctx context.Context, stored []string) (*ClaudeCLI, error) {
	return holdClaudeSavedCLI(ctx, stored, followingClaudeImage(holdSignedClaudeImage))
}

// followingClaudeImage follows a saved locator to the newest version Claude
// Code installed in the same package data folder, then holds and authenticates
// that image exactly as the saved one: registered publisher, PE and file identity.
func followingClaudeImage(hold func(context.Context, string) (nativeProductImage, error)) func(context.Context, string) (nativeProductImage, error) {
	return followingImage(FollowClaudeCLI, hold)
}

func holdClaudeSavedCLI(ctx context.Context, stored []string, hold func(context.Context, string) (nativeProductImage, error)) (*ClaudeCLI, error) {
	selected, err := selectSavedImage(ctx, stored, hold)
	if err != nil {
		return nil, err
	}
	return &ClaudeCLI{image: selected, management: true}, nil
}

func selectClaudeSavedCLI(ctx context.Context, stored []string, probe func(context.Context, string) ([]byte, error), hold func(context.Context, string) (nativeProductImage, error)) (*ClaudeCLI, error) {
	if probe == nil {
		return nil, errors.New("saved Code command unavailable")
	}
	result, err := holdClaudeSavedCLI(ctx, stored, hold)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*ClaudeCLI, error) { return nil, errors.Join(err, result.Close()) }
	output, err := probe(ctx, result.Program())
	if err != nil {
		return fail(err)
	}
	if len(output) > 256 {
		return fail(errors.New("Code version response is too large"))
	}
	match := claudeVersionLine.FindSubmatch(output)
	if len(match) != 2 {
		return fail(errors.New("The selected program did not report a valid Code version"))
	}
	result.version = string(match[1])
	if claudeCLIFollowed(ctx, stored, result.FileKey()) && filepath.Base(filepath.Dir(filepath.Dir(result.Program()))) != result.version {
		return fail(errors.New("Code version differs from its version folder"))
	}
	if err := result.Check(ctx); err != nil {
		return fail(err)
	}
	return result, nil
}

// claudeCLIFollowed reports whether none of the saved locators can name the
// held image now, as after following a Claude Code self-update.
func claudeCLIFollowed(ctx context.Context, stored []string, heldKey string) bool {
	for _, raw := range stored {
		id, err := fileid.ParseStoredFileID(raw)
		if err != nil {
			continue
		}
		current, err := fileid.Resolve(ctx, fileid.Self, id.Path(), fileid.Options{})
		if err == nil && current.Key() == heldKey {
			return false
		}
	}
	return true
}

type claudeProcessImage struct {
	image   nativeProductImage
	current func() error
	close   func() error
}

// SelectClaudeCLI reads current native image metadata only. It never reads
// command lines, process memory, session files, PATH or profile directories.
// versionProbe is the caller's existing bounded --version process owner.
func SelectClaudeCLI(ctx context.Context, versionProbe func(context.Context, string) ([]byte, error)) (*ClaudeCLI, error) {
	rows, err := currentClaudeImages(ctx)
	if err != nil {
		return nil, err
	}
	return selectClaudeImages(ctx, rows, versionProbe)
}

func selectClaudeImages(ctx context.Context, rows []claudeProcessImage, probe func(context.Context, string) ([]byte, error)) (*ClaudeCLI, error) {
	closeRows := func() error {
		var errs []error
		for _, p := range rows {
			if p.close != nil {
				errs = append(errs, p.close())
			}
		}
		return errors.Join(errs...)
	}
	fail := func(err error) (*ClaudeCLI, error) { return nil, errors.Join(err, closeRows()) }
	if len(rows) == 0 {
		return fail(errors.New("Code is unavailable; open it and retry"))
	}
	if len(rows) > 64 || probe == nil {
		return fail(errors.New("Code selection is unavailable"))
	}
	keys := map[string]bool{}
	for _, p := range rows {
		if p.image == nil || p.current == nil || p.close == nil {
			return fail(errors.New("Code native image evidence is incomplete"))
		}
		if err := p.current(); err != nil {
			return fail(err)
		}
		if err := p.image.Check(ctx); err != nil {
			return fail(err)
		}
		keys[p.image.FileKey()] = true
	}
	if len(keys) != 1 {
		return fail(errors.New("Several Code images are running; close the other one and retry"))
	}
	output, err := probe(ctx, rows[0].image.Program())
	if err != nil {
		return fail(err)
	}
	if len(output) > 256 {
		return fail(errors.New("Code version response is too large"))
	}
	match := claudeVersionLine.FindSubmatch(output)
	if len(match) != 2 {
		return fail(errors.New("The running program did not report a valid Code version"))
	}
	result := &ClaudeCLI{image: rows[0].image, version: string(match[1]), processes: rows}
	if err := result.Check(ctx); err != nil {
		return fail(err)
	}
	return result, nil
}

func currentClaudeImages(ctx context.Context) ([]claudeProcessImage, error) {
	return currentSignedClaudeImages(ctx)
}

func currentProductImages(ctx context.Context, productFamily, executable string, subsystem uint16) ([]claudeProcessImage, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	var ownSession uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &ownSession); err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snapshot, &entry)
	var candidates []uint32
	for count := 0; err == nil; count++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if count >= 4096 {
			return nil, errors.New("Code process inventory exceeded its bound")
		}
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), executable) {
			candidates = append(candidates, entry.ProcessID)
			if len(candidates) > 64 {
				return nil, errors.New("Too many Code process candidates")
			}
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return admitClaudeCandidates(ctx, candidates, func(pid uint32) (claudeProcessImage, error) {
		return holdProductProcess(ctx, pid, ownSession, user.User.Sid, productFamily, executable, subsystem)
	})
}

// Known other-account/session/product/GUI images may be excluded. An unknown
// query or image failure makes the whole inventory unavailable, including when
// one otherwise usable image exists; it cannot disappear as verified absence.
func admitClaudeCandidates(ctx context.Context, pids []uint32, hold func(uint32) (claudeProcessImage, error)) ([]claudeProcessImage, error) {
	var rows []claudeProcessImage
	var unknown error
	if len(pids) > 64 || hold == nil {
		return nil, errors.New("Code process candidates unavailable")
	}
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			unknown = errors.Join(unknown, err)
			break
		}
		row, err := hold(pid)
		if err == nil {
			rows = append(rows, row)
		} else if !errors.Is(err, errOtherClaudeImage) {
			unknown = errors.Join(unknown, err)
		}
	}
	if unknown != nil {
		for _, row := range rows {
			if row.close != nil {
				unknown = errors.Join(unknown, row.close())
			}
		}
		return nil, fmt.Errorf("Code native evidence is unavailable: %w", unknown)
	}
	return rows, nil
}

// The product-specific callers supply fixed package-family/executable metadata.
// File identity and process lifetime remain owned by the native holds below.
func holdProductProcess(ctx context.Context, pid, session uint32, user *windows.SID, productFamily, executable string, subsystem uint16) (claudeProcessImage, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return claudeProcessImage{}, fmt.Errorf("native process hold: %w", err)
	}
	defer windows.CloseHandle(process)
	fail := func(err error) (claudeProcessImage, error) { return claudeProcessImage{}, err }
	var actualSession uint32
	if err := windows.ProcessIdToSessionId(pid, &actualSession); err != nil {
		return fail(err)
	}
	if actualSession != session {
		return fail(errOtherClaudeImage)
	}
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return fail(fmt.Errorf("native process account: %w", err))
	}
	actualUser, err := token.GetTokenUser()
	token.Close()
	if err != nil {
		return fail(err)
	}
	if !actualUser.User.Sid.Equals(user) {
		return fail(errOtherClaudeImage)
	}
	var path [32768]uint16
	n := uint32(len(path))
	if err := windows.QueryFullProcessImageName(process, 0, &path[0], &n); err != nil {
		return fail(err)
	}
	program := windows.UTF16ToString(path[:n])
	if !strings.EqualFold(filepath.Base(program), executable) {
		return fail(errOtherClaudeImage)
	}
	if productFamily == claudeProductFamily && subsystem == pe.IMAGE_SUBSYSTEM_WINDOWS_CUI {
		return holdSignedClaudeProcess(ctx, pid, process, program)
	}
	family, err := claudeProcessPackage(process)
	if err != nil {
		return fail(err)
	}
	if !strings.EqualFold(family, productFamily) {
		return fail(errOtherClaudeImage)
	}
	selected, err := holdSelectedProductImage(ctx, productFamily, program, subsystem)
	if err != nil {
		return fail(err)
	}
	return retainProductProcess(ctx, pid, process, selected)
}

// Product selection uses native account/session metadata above, not a Rights
// subject. Both product image qualifiers share this retained lifetime owner.
func retainProductProcess(ctx context.Context, pid uint32, query windows.Handle, selected nativeProductImage) (claudeProcessImage, error) {
	fail := func(err error) (claudeProcessImage, error) {
		return claudeProcessImage{}, errors.Join(err, selected.Close())
	}
	var birth, exit, kernel, used windows.Filetime
	if err := windows.GetProcessTimes(query, &birth, &exit, &kernel, &used); err != nil {
		return fail(err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return fail(err)
	}
	cleanup := func(err error) (claudeProcessImage, error) {
		return fail(errors.Join(err, windows.CloseHandle(process)))
	}
	if err := selected.Check(ctx); err != nil {
		return cleanup(err)
	}
	var now windows.Filetime
	if err := windows.GetProcessTimes(process, &now, &exit, &kernel, &used); err != nil {
		return cleanup(err)
	}
	if now != birth {
		return cleanup(errors.New("native product process identity changed during hold"))
	}
	current := func() error {
		status, err := windows.WaitForSingleObject(process, 0)
		if err != nil {
			return err
		}
		if status == uint32(windows.WAIT_OBJECT_0) {
			return errProductProcessEnded
		}
		if status != uint32(windows.WAIT_TIMEOUT) {
			return errors.New("native product process state unavailable")
		}
		var now windows.Filetime
		if err := windows.GetProcessTimes(process, &now, &exit, &kernel, &used); err != nil {
			return err
		}
		if now != birth {
			return errors.New("Code process identity changed")
		}
		return nil
	}
	if err := current(); err != nil {
		return cleanup(err)
	}
	if err := selected.Check(ctx); err != nil {
		return cleanup(err)
	}
	return claudeProcessImage{image: selected, current: current, close: func() error { return errors.Join(selected.Close(), windows.CloseHandle(process)) }}, nil
}

func productSubsystem(file *os.File, subsystem uint16) error {
	actual, err := productImageSubsystem(file)
	if err != nil {
		return err
	}
	if actual != subsystem {
		return errOtherClaudeImage
	}
	return nil
}

func productImageSubsystem(file *os.File) (uint16, error) {
	image, err := pe.NewFile(file)
	if err != nil {
		return 0, err
	}
	switch h := image.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		return h.Subsystem, nil
	case *pe.OptionalHeader64:
		return h.Subsystem, nil
	}
	return 0, errors.New("native PE optional header unavailable")
}

func holdSelectedProductImage(ctx context.Context, family, program string, subsystem uint16) (nativeProductImage, error) {
	if family == codexProductFamily {
		image, match, err := packaged.MatchImage(ctx, family, "app/ChatGPT.exe", program)
		if err != nil {
			return nil, err
		}
		if !match {
			return nil, errors.New("Codex Desktop registered image changed; restart it and retry")
		}
		if err := productSubsystem(image.File(), subsystem); err != nil {
			return nil, errors.Join(err, image.Close())
		}
		return image, nil
	}
	directory, err := programfiles.HoldDirectory(filepath.Dir(program))
	if err != nil {
		return nil, err
	}
	held, err := openArchiveSource(ctx, program)
	if err != nil {
		return nil, errors.Join(err, directory.Release())
	}
	fail := func(err error) (nativeProductImage, error) {
		return nil, errors.Join(err, held.Close(), directory.Release())
	}
	if err := productSubsystem(held, subsystem); err != nil {
		return fail(err)
	}
	key, err := fileid.FileKeyOf(held)
	if err != nil {
		return fail(err)
	}
	image := &Image{file: held, directory: directory, program: program, key: key}
	if err := image.Check(ctx); err != nil {
		return fail(err)
	}
	return image, nil
}
