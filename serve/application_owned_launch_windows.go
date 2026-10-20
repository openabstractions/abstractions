package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"golang.org/x/sys/windows"
)

type ownedApplicationProcess struct {
	mu      sync.Mutex
	handle  windows.Handle
	image   ownedApplicationImage
	binding ownedApplicationBinding
	closed  bool
}

// TOKEN_INFORMATION_CLASS, winnt.h; x/sys does not currently name this value.
const ownedApplicationTokenIsAppContainer = 29

// Uses the ordinary os/exec process/explicit pipe owner. Breakaway is required:
// unlike the old detached runtime start, access denied has no retry fallback.
func startOwnedApplication(ctx context.Context, image ownedApplicationImage, binding ownedApplicationBinding, args []string, authority func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error) {
	transferred := false
	defer func() {
		if !transferred && image.close != nil {
			_ = image.close()
		}
	}()
	if authority == nil {
		return nil, errors.New("application host: no authority recheck")
	}
	if err := image.admit(ctx); err != nil {
		return nil, err
	}
	if err := ownedApplicationLaunchContext(binding); err != nil {
		return nil, err
	}
	if err := authority(ctx, binding); err != nil {
		return nil, err
	}
	command := exec.Command(image.program, args...)
	command.Dir = filepath.Dir(image.program)
	command.Env = childEnvironment(os.Environ(), nil)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createBreakawayFromJob | createNewProcessGroup}
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	in, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := command.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	cleanup := func() { in.Close(); out.Close() }
	// Recheck the live held image immediately before the one launch attempt.
	if err := image.admit(ctx); err != nil {
		cleanup()
		return nil, err
	}
	if err := authority(ctx, binding); err != nil {
		cleanup()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return nil, err
	}
	if err := command.Start(); err != nil {
		cleanup()
		return nil, err
	}
	pin := &ownedApplicationProcess{image: image, binding: binding}
	var duplicateErr error
	err = command.Process.WithHandle(func(handle uintptr) {
		duplicateErr = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(handle), windows.CurrentProcess(), &pin.handle, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, 0)
	})
	err = errors.Join(err, duplicateErr)
	if err == nil && pin.handle == 0 {
		err = errors.New("application host: no retained child handle")
	}
	if err == nil {
		var created, exited, kernel, user windows.Filetime
		err = windows.GetProcessTimes(pin.handle, &created, &exited, &kernel, &user)
		if err == nil {
			pin.binding.pid = command.Process.Pid
			pin.binding.started = time.Unix(0, created.Nanoseconds())
			err = pin.check(ctx, pin.binding)
		}
	}
	if err != nil {
		cleanup()
		pin.close()
		go func() { _ = command.Wait() }()
		return nil, errors.Join(errOwnedApplicationUncertain, err)
	}
	guard := func(ctx context.Context, b ownedApplicationBinding) error {
		if err := pin.check(ctx, b); err != nil {
			return err
		}
		return authority(ctx, b)
	}
	session, err := newOwnedApplicationSession(pin.binding, in, out, guard, pin.close)
	if err != nil {
		cleanup()
		pin.close()
		go func() { _ = command.Wait() }()
		return nil, errors.Join(errOwnedApplicationUncertain, err)
	}
	go func() {
		_ = command.Wait()
		if command.ProcessState != nil {
			_ = session.processExited()
		} else {
			_ = session.Close()
		}
	}()
	transferred = true
	return session, nil
}

func ownedApplicationLaunchContext(binding ownedApplicationBinding) error {
	if err := startGuard(); err != nil {
		return err
	}
	view, err := bootstrap.CurrentProfileView()
	if err != nil {
		return err
	}
	if view.Virtualized {
		return errOwnedApplicationVirtualized
	}
	if err := shellIsThisAccount(&shellFrame{}); err != nil {
		return err
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return err
	}
	if session == 0 || binding.desktop != "windows:"+strconv.FormatUint(uint64(session), 10) {
		return errors.New("application host: wrong desktop session")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if binding.account != user.User.Sid.String() {
		return errors.New("application host: wrong runtime account")
	}
	return ownedApplicationToken(windows.GetCurrentProcessToken(), binding)
}

func ownedApplicationToken(token windows.Token, b ownedApplicationBinding) error {
	if err := ownedApplicationTokenFlags(token, windows.GetTokenInformation); err != nil {
		return err
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	if user.User.Sid.String() != b.account {
		return errors.New("application host: child account moved")
	}
	var session, size uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), 4, &size); err != nil {
		return err
	}
	if size != 4 || session == 0 || b.desktop != "windows:"+strconv.FormatUint(uint64(session), 10) {
		return errors.New("application host: child session moved")
	}
	return nil
}

func ownedApplicationTokenFlags(token windows.Token, inspect func(windows.Token, uint32, *byte, uint32, *uint32) error) error {
	if err := requireUnelevatedShell(token, inspect); err != nil {
		return err
	}
	var tokenType, size uint32
	if err := inspect(token, windows.TokenType, (*byte)(unsafe.Pointer(&tokenType)), 4, &size); err != nil {
		return err
	}
	if size != 4 || tokenType != 1 {
		return errors.New("application host: token is not primary")
	}
	for _, class := range []uint32{windows.TokenVirtualizationEnabled, ownedApplicationTokenIsAppContainer} {
		var value, size uint32
		if err := inspect(token, class, (*byte)(unsafe.Pointer(&value)), 4, &size); err != nil {
			return err
		}
		if size != 4 || value != 0 {
			return errors.New("application host: unsafe token placement")
		}
	}
	return nil
}

func (p *ownedApplicationProcess) check(ctx context.Context, b ownedApplicationBinding) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 || !sameOwnedApplicationProcessBinding(b, p.binding) {
		return errors.New("application host: retired or changed process binding")
	}
	if err := p.image.admit(ctx); err != nil {
		return err
	}
	state, err := windows.WaitForSingleObject(p.handle, 0)
	if err != nil {
		return err
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return errors.New("application host: child ended")
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p.handle, &created, &exited, &kernel, &user); err != nil {
		return err
	}
	if !time.Unix(0, created.Nanoseconds()).Equal(b.started) {
		return errors.New("application host: child creation time moved")
	}
	var token windows.Token
	if err := windows.OpenProcessToken(p.handle, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	if err := ownedApplicationToken(token, b); err != nil {
		return err
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(p.handle, 0, &buffer[0], &size); err != nil {
		return err
	}
	if !samePrograms(windows.UTF16ToString(buffer[:size]), p.image.program) {
		return fmt.Errorf("application host: child image differs from held image")
	}
	return ctx.Err()
}

func (p *ownedApplicationProcess) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	if p.handle != 0 {
		err = windows.CloseHandle(p.handle)
		p.handle = 0
	}
	return errors.Join(err, p.image.close())
}
