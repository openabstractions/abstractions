package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// Only source fixtures supply synthetic code trust. The image hold, launched
// process handle, native caller, PID and birth are real and remain checked by
// the production ownedApplicationProcess/session owner. No installed image is used.
func openReceiverSourceFixtureImage(ctx context.Context, r applicationMessageStartRequest) (ownedApplicationImage, error) {
	h, err := fileid.Open(ctx, fileid.Self, r.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return ownedApplicationImage{}, err
	}
	var once sync.Once
	var closeErr error
	close := func() error { once.Do(func() { closeErr = h.Close() }); return closeErr }
	check := func(ctx context.Context) error {
		current, err := fileid.Resolve(ctx, fileid.Self, r.Program, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return err
		}
		if !current.Equal(h.FileID()) {
			return errors.New("fixture held image changed")
		}
		return nil
	}
	return ownedApplicationImage{program: r.Program, code: identity.Code{Trusted: true}, proof: identity.ProofBound, recheck: check, close: close}, nil
}

// Source checks keep this child inside the existing 24GiB owner job. Production
// uses its unchanged installed-context/breakaway adapter. Both adapters use the
// same retained Windows process check and OA owned-session framing.
func startReceiverSourceFixture(ctx context.Context, image ownedApplicationImage, binding ownedApplicationBinding, args []string, authority func(context.Context, ownedApplicationBinding) error, env []string, done chan<- error, started func(*os.Process)) (_ *ownedApplicationSession, returned error) {
	transferred := false
	defer func() {
		if !transferred {
			image.close()
			done <- returned
		}
	}()
	if err := image.admit(ctx); err != nil {
		return nil, err
	}
	if err := authority(ctx, binding); err != nil {
		return nil, err
	}
	command := exec.Command(image.program, args...)
	command.Env = env
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
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
	if err := authority(ctx, binding); err != nil {
		cleanup()
		return nil, err
	}
	if err := command.Start(); err != nil {
		cleanup()
		return nil, err
	}
	started(command.Process)
	pin := &ownedApplicationProcess{image: image, binding: binding}
	var duplicateErr error
	err = command.Process.WithHandle(func(handle uintptr) {
		duplicateErr = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(handle), windows.CurrentProcess(), &pin.handle, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, 0)
	})
	err = errors.Join(err, duplicateErr)
	if err == nil {
		var created, exited, kernel, user windows.Filetime
		err = windows.GetProcessTimes(pin.handle, &created, &exited, &kernel, &user)
		if err == nil {
			pin.binding.pid = command.Process.Pid
			pin.binding.started = time.Unix(0, created.Nanoseconds())
			err = pin.check(ctx, pin.binding)
		}
	}
	fail := func(err error) (*ownedApplicationSession, error) {
		cleanup()
		command.Process.Kill()
		command.Wait()
		pin.close()
		return nil, err
	}
	if err != nil {
		return fail(err)
	}
	guard := func(ctx context.Context, b ownedApplicationBinding) error {
		if err := pin.check(ctx, b); err != nil {
			return err
		}
		return authority(ctx, b)
	}
	session, err := newOwnedApplicationSession(pin.binding, in, out, guard, pin.close)
	if err != nil {
		return fail(err)
	}
	go func() { err := command.Wait(); session.Close(); done <- err }()
	transferred = true
	return session, nil
}
