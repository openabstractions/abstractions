package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

type nativeArchiveOperation uint8

const (
	nativeArchivePlan nativeArchiveOperation = iota + 1
	nativeArchiveSeal
	nativeArchiveCheck
	nativeArchiveInstalledPlan
	nativeArchiveRegister
)

// This closed command owner never accepts an argv tail, environment, resource
// path or Python program. The held installed image owns its resource decoding.
var nativeArchiveCommand = runNativeArchiveCommand

var nativeArchiveInstalledCommand = func(ctx context.Context, pin sevenZipPin, root string) ([]byte, error) {
	return runNativeArchiveCommandWith(ctx, pin, nativeArchiveInstalledPlan, root, "")
}

// The seal and installed-plan operations name the shared PyTorch component's
// site-packages for oa-pytorch.pth (--pytorch-site-packages).
var nativeArchiveSealCommand = func(ctx context.Context, pin sevenZipPin, site string) ([]byte, error) {
	return runNativeArchiveCommandWith(ctx, pin, nativeArchiveSeal, "", site)
}

var nativeArchiveInstalledSiteCommand = func(ctx context.Context, pin sevenZipPin, root, site string) ([]byte, error) {
	return runNativeArchiveCommandWith(ctx, pin, nativeArchiveInstalledPlan, root, site)
}

func runNativeArchiveCommand(ctx context.Context, pin sevenZipPin, operation nativeArchiveOperation) (raw []byte, err error) {
	return runNativeArchiveCommandAt(ctx, pin, operation, "")
}

func runNativeArchiveCommandAt(ctx context.Context, pin sevenZipPin, operation nativeArchiveOperation, installedRoot string) ([]byte, error) {
	return runNativeArchiveCommandWith(ctx, pin, operation, installedRoot, "")
}

func runNativeArchiveCommandWith(ctx context.Context, pin sevenZipPin, operation nativeArchiveOperation, installedRoot, site string) (raw []byte, err error) {
	var verb string
	var arguments []string
	bound := int64(1 << 20)
	duration := 10 * time.Minute
	switch operation {
	case nativeArchivePlan:
		verb = "native-install-plan"
		bound = 16 << 20
		duration = 30 * time.Second
	case nativeArchiveInstalledPlan:
		if !filepath.IsAbs(installedRoot) || filepath.Clean(installedRoot) != installedRoot || identity.LocalPath(installedRoot) != nil {
			return nil, errors.New("installed plan requires the original local code root")
		}
		verb = "native-install-plan"
		bound = 16 << 20
		duration = 30 * time.Second
		arguments = []string{"--installed-root", installedRoot}
	case nativeArchiveSeal:
		verb = "seal-native-install"
	case nativeArchiveCheck:
		verb = "check-native-install"
	case nativeArchiveRegister:
		// The app's own self-registration request; the person's Keep admits it.
		verb = "register-self"
		duration = time.Minute
	default:
		return nil, errors.New("unsupported native archive operation")
	}
	if operation != nativeArchiveInstalledPlan && installedRoot != "" {
		return nil, errors.New("unexpected installed root metadata")
	}
	if site != "" {
		if operation != nativeArchiveSeal && operation != nativeArchiveInstalledPlan || !filepath.IsAbs(site) || filepath.Clean(site) != site || identity.LocalPath(site) != nil {
			return nil, errors.New("the PyTorch component site-packages must be a local absolute path")
		}
		arguments = append(arguments, "--pytorch-site-packages", site)
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	work, err := os.MkdirTemp(filepath.Dir(pin.Path), ".native-check-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	if err = privatedir.Tighten(work); err != nil {
		return nil, err
	}
	if err := verifyFileContext(ctx, pin.Path, Artifact{Bytes: pin.Bytes, SHA256: pin.SHA256}); errors.Is(err, errPinnedSize) || errors.Is(err, errPinnedDigest) {
		return nil, errNativeImagePin
	} else if err != nil {
		return nil, err
	}
	hold, err := programfiles.NewVerifier(programfiles.Options{}).LaunchPlaced(ctx, "native-install-owner", []programfiles.Placed{{Path: pin.Path, Size: pin.Bytes, SHA256: pin.SHA256}}, filepath.Join(work, "image.json"))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, hold.Release()) }()
	program := hold.Paths()[0]
	code, proof, err := identity.VerifyFile(program, nil)
	if err != nil {
		return nil, err
	}
	if err := nativeImageAdmitted(program, code, proof); err != nil {
		return nil, err
	}
	env, err := nativeArchiveEnvironment(work)
	if err != nil {
		return nil, err
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer outRead.Close()
	defer outWrite.Close()
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer errRead.Close()
	defer errWrite.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	p, err := supervision.Start(ctx, supervision.Options{Executable: program, Args: append([]string{verb}, arguments...), Dir: work, Env: env, Stdin: input, Stdout: outWrite, Stderr: errWrite, ForceTimeout: 3 * time.Second, ProcessMemoryLimit: 1 << 30})
	if err != nil {
		return nil, err
	}
	_ = outWrite.Close()
	_ = errWrite.Close()
	type result struct {
		raw []byte
		err error
	}
	output, diagnostic := make(chan result, 1), make(chan result, 1)
	read := func(f *os.File, limit int64, ch chan result) {
		b, e := io.ReadAll(io.LimitReader(f, limit+1))
		if int64(len(b)) > limit {
			e = errors.New("native archive output exceeds bound")
			cancel()
		}
		ch <- result{b, e}
	}
	go read(outRead, bound, output)
	go read(errRead, 1<<20, diagnostic)
	status, waitErr := p.Wait()
	if waitErr != nil {
		_ = outRead.Close()
		_ = errRead.Close()
	}
	out, failure := <-output, <-diagnostic
	if err = errors.Join(waitErr, out.err, failure.err, ctx.Err()); err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, nativeArchiveRefusal(operation, verb, status, out.raw, failure.raw)
	}
	exit, known := p.Exit()
	if !known || exit.Unknown != "" || exit.MemoryLimitHit {
		return nil, errors.New("native archive process completion is unproven or resource-limited")
	}
	return out.raw, nil
}

var nativeArchiveHelperTitle = "the " + nativeArchiveAppTitle + " launch helper"

var errNativeImagePin = errors.New(nativeArchiveHelperTitle + " differs from its package pin, so Setup did not run it")

// nativeImageAdmitted decides whether Setup runs the held launch helper. The
// catalogue pins the host package and every member's size and SHA-256, and
// the held file was proven against that pin: the pin is the package's trust
// root. An unsigned helper runs on that pin and registers through the
// person's Keep. A signed helper must also carry a trusted signature; a
// signature Windows refuses, and a shared interpreter, are refused.
func nativeImageAdmitted(program string, code identity.Code, proof identity.Proof) error {
	if identity.SharedInterpreterProgram(program, code) {
		return errors.New(nativeArchiveHelperTitle + " is a shared interpreter, so Setup did not run it")
	}
	if proof == identity.ProofUnsigned && !code.Trusted {
		return nil
	}
	if proof == identity.ProofBound && code.Trusted {
		return nil
	}
	return errors.New(nativeArchiveHelperTitle + " carries a signature Windows does not trust, so Setup did not run it")
}

// ErrRegistrationIdentity is the service's conflict "identity" to an app's
// register-self: its path holds an image without the entry's recorded signer
// or image pin. The entry stays as it was.
var ErrRegistrationIdentity = errors.New("conflict: identity")

// nativeArchiveRefusal is the sentence for a launch helper operation that
// exited unsuccessfully. register-self reports the service's outcome and
// reason on stdout; every operation's own error is its last stderr line.
func nativeArchiveRefusal(operation nativeArchiveOperation, verb string, status int, stdout, stderr []byte) error {
	reason, identityConflict := "", false
	if operation == nativeArchiveRegister {
		if outcome, detail, valid := registrationAnswerFields(bytes.TrimSpace(stdout)); valid && outcome != "" {
			reason = outcome
			if detail != "" {
				reason += ": " + detail
			}
			identityConflict = outcome == "conflict" && detail == "identity"
		}
	}
	if identityConflict {
		return fmt.Errorf("%s's %s stopped (exit %d): %w", nativeArchiveHelperTitle, verb, status, ErrRegistrationIdentity)
	}
	if reason == "" {
		lines := strings.Split(strings.TrimSpace(strings.ToValidUTF8(string(stderr), "?")), "\n")
		reason = strings.TrimSpace(lines[len(lines)-1])
	}
	if len(reason) > 400 {
		cut := 400
		for cut > 0 && !utf8.RuneStart(reason[cut]) {
			cut--
		}
		reason = reason[:cut] + "…"
	}
	if reason == "" {
		return fmt.Errorf("%s's %s stopped (exit %d)", nativeArchiveHelperTitle, verb, status)
	}
	return fmt.Errorf("%s's %s stopped (exit %d): %s", nativeArchiveHelperTitle, verb, status, reason)
}

// nativeArchiveProfileNames are the person's folders every launch helper
// operation needs: its bootstrap reads its profile view from the local application data folder,
// and register-self discovers the runtime under the person's profile. The
// builder computes each from the token and registry; none is inherited.
var nativeArchiveProfileNames = layout.PersonProfileVariables()

// nativeArchiveEnvironment is the clean environment of every launch helper
// operation Setup runs (plan, seal, check, register-self).
func nativeArchiveEnvironment(work string) ([]string, error) {
	return (supervision.CleanEnvironment{Temp: work, Names: nativeArchiveProfileNames}).Build()
}
