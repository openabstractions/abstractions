//go:build darwin && cgo

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
)

type ownedApplicationProcess struct {
	mu      sync.Mutex
	pin     *identity.Binding
	image   ownedApplicationImage
	binding ownedApplicationBinding
}

func ownedApplicationLaunchContext(b ownedApplicationBinding) error {
	u, err := identity.Self()
	if err != nil {
		return err
	}
	account, err := u.Account()
	if err != nil {
		return err
	}
	if u.AuditSessionID == 0 || b.account != account || b.desktop != "darwin:"+strconv.FormatUint(uint64(u.AuditSessionID), 10) {
		return errors.New("application host: unknown or different runtime audit session")
	}
	return nil
}

// A normal direct child may execute before capture. Protected OA dispatch is
// admitted only after its retained occurrence and sealed main image match.
func startOwnedApplication(ctx context.Context, image ownedApplicationImage, binding ownedApplicationBinding, args []string, authority func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error) {
	transferred := false
	defer func() {
		if !transferred && image.close != nil {
			_ = image.close()
		}
	}()
	if authority == nil || image.sealed == nil {
		return nil, errors.New("application host: missing native image authority")
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
	cmd := exec.Command(image.program, args...)
	cmd.Dir = filepath.Dir(image.program)
	cmd.Env = childEnvironment(os.Environ(), nil)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	cleanup := func() { _ = in.Close(); _ = out.Close() }
	if err = image.admit(ctx); err == nil {
		err = authority(ctx, binding)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cleanup()
		return nil, err
	}
	// No Wait or Release may race capture of this caller-owned child.
	pin, err := identity.BindStartedProcess(cmd.Process)
	p := &ownedApplicationProcess{pin: pin, image: image, binding: binding}
	if err == nil {
		var peer *identity.Peer
		peer, err = pin.Peer()
		if err == nil {
			process, processErr := peer.Process.AtLeast(identity.ProofBound)
			if processErr != nil {
				err = processErr
			} else {
				p.binding.pid = process.PID
				p.binding.generation = process.Generation
				p.binding.started = time.Time{}
				err = p.check(ctx, p.binding)
			}
		}
	}
	if err != nil {
		cleanup()
		_ = p.close()
		go func() { _ = cmd.Wait() }()
		return nil, errors.Join(errOwnedApplicationUncertain, err)
	}
	guard := func(ctx context.Context, b ownedApplicationBinding) error {
		if err := p.check(ctx, b); err != nil {
			return err
		}
		return authority(ctx, b)
	}
	authenticate := func(ctx context.Context, requestPeer *identity.Peer, match ownedApplicationRequestMatcher) (*identity.Peer, error) {
		if requestPeer == nil || match == nil {
			return nil, identity.ErrNotProven
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.pin == nil {
			return nil, identity.ErrPeerGone
		}
		if err := p.checkLocked(ctx, p.binding); err != nil {
			return nil, err
		}
		code, proof, err := match(p.pin, p.image.sealed)
		if err != nil {
			return nil, err
		}
		if !code.Trusted || proof != identity.ProofBound {
			return nil, identity.ErrNotProven
		}
		peer, err := p.pin.Peer()
		if err != nil {
			return nil, err
		}
		requestProcess, requestErr := requestPeer.Process.AtLeast(identity.ProofKernel)
		process, processErr := peer.Process.AtLeast(identity.ProofBound)
		if requestErr != nil || processErr != nil || requestProcess.PID != process.PID {
			return nil, identity.ErrPeerMoved
		}
		if err := p.checkLocked(ctx, p.binding); err != nil {
			return nil, err
		}
		return peer, nil
	}
	s, err := newDarwinOwnedApplicationSession(p.binding, in, out, guard, authenticate, p.close)
	if err != nil {
		cleanup()
		_ = p.close()
		go func() { _ = cmd.Wait() }()
		return nil, errors.Join(errOwnedApplicationUncertain, err)
	}
	go func() {
		_ = cmd.Wait()
		if cmd.ProcessState != nil {
			_ = s.processExited()
		} else {
			_ = s.Close()
		}
	}()
	transferred = true
	return s, nil
}

func newDarwinOwnedApplicationSession(b ownedApplicationBinding, in io.WriteCloser, out io.ReadCloser, guard func(context.Context, ownedApplicationBinding) error, auth ownedApplicationAnnounceAuthenticator, release ...func() error) (*ownedApplicationSession, error) {
	if b.pid <= 0 || b.generation == 0 || !b.started.IsZero() || auth == nil {
		return nil, errors.New("application host: incomplete native occurrence")
	}
	if err := ownedApplicationLaunchContext(b); err != nil {
		return nil, err
	}
	return newOwnedApplicationSessionState(b, in, out, guard, auth, release...)
}

func (p *ownedApplicationProcess) check(ctx context.Context, b ownedApplicationBinding) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checkLocked(ctx, b)
}

func (p *ownedApplicationProcess) checkLocked(ctx context.Context, b ownedApplicationBinding) error {
	expected := p.binding
	// The service-issued instance is frozen by the shared Announce owner.
	expected.instance = b.instance
	if p.pin == nil || b != expected || b.pid <= 0 || b.generation == 0 || !b.started.IsZero() {
		return errors.New("application host: changed native occurrence binding")
	}
	if err := p.image.admit(ctx); err != nil {
		return err
	}
	live, err := p.pin.Alive()
	if err != nil {
		return err
	}
	if !live {
		return identity.ErrPeerGone
	}
	peer, err := p.pin.Peer()
	if err != nil {
		return err
	}
	u, err := peer.User.AtLeast(identity.ProofKernel)
	if err != nil {
		return err
	}
	process, err := peer.Process.AtLeast(identity.ProofBound)
	if err != nil {
		return err
	}
	account, err := u.Account()
	if err != nil {
		return err
	}
	session, ok := applicationSession(peer)
	if !ok || account != b.account || session != b.desktop || process.PID != b.pid || process.Generation != b.generation || !process.StartTime.IsZero() {
		return identity.ErrPeerMoved
	}
	code, proof, err := p.image.sealed.MatchStartedProcess(p.pin)
	if err != nil {
		return err
	}
	if !code.Trusted || proof != identity.ProofBound {
		return identity.ErrNotProven
	}
	return ctx.Err()
}

func (p *ownedApplicationProcess) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pin == nil {
		return nil
	}
	err := errors.Join(p.pin.Close(), p.image.close())
	p.pin = nil
	return err
}
