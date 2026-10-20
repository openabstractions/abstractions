package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"golang.org/x/sys/windows"
)

// ownImagePath is the path Windows names this process by now: after Setup
// moves the running image aside, the moved file.
func ownImagePath() string {
	return processImagePath(windows.CurrentProcess())
}

func processImagePath(process windows.Handle) string {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(process, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// ownImageMoved reports whether Setup moved this process's image aside.
func ownImageMoved() bool {
	_, moved := nativecli.MovedAgentImage(ownImagePath())
	return moved
}

// ownMovedImageSentence answers tool calls once this process's image is a
// connector copy Setup moved aside. A dedicated helper hands its connection
// to the published connector and says to try again; when that is refused or
// fails, the call names the restart. "" while the image is current.
func ownMovedImageSentence() string {
	title, moved := nativecli.MovedAgentImage(ownImagePath())
	if !moved {
		return ""
	}
	if sentence := handOverNow(currentProcessAgentStdio()); sentence != "" {
		return sentence
	}
	return nativecli.MovedAgentImageSentence(title)
}

// agentImageSigned reports a signed build: this process's moved image
// carries a trusted embedded signer. A test replaces it.
var agentImageSigned = product.Signed

// agentSuccessorAuthentic decides, on a signed build, whether the held
// candidate is signed by the same publisher key as reference, this process's
// moved image. A test replaces it.
var agentSuccessorAuthentic = product.SameSignerAs

// agentSuccessorBeforeStart runs after the candidate is verified and held,
// before it starts; a test tries to replace the file there.
var agentSuccessorBeforeStart = func(string) {}

// agentChildImageKey is the file key of a started process's image; a test
// replaces it.
var agentChildImageKey = func(pid int) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)
	path := processImagePath(process)
	if path == "" {
		return "", errors.New("handover: successor image unknown")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return "", err
	}
	return id.Key(), nil
}

// agentSuccessorLauncher starts the published connector at program, the path
// this process started from, with the arguments and environment this process
// started with, in folder. The candidate is opened with links refused and
// held without write or delete sharing from its verification until the
// started process is confirmed to run that same file; nothing reaches it
// before. It returns nil when program is not a connector.
func agentSuccessorLauncher(program, folder string, args, env []string, read agentEnrollmentRead) func() (*agentSuccessor, error) {
	application := dedicatedAgentApplication(program)
	if application == "" || folder == "" || read == nil {
		return nil
	}
	args, env = append([]string(nil), args...), append([]string(nil), env...)
	return func() (*agentSuccessor, error) {
		moved := ownImagePath()
		if moved == "" || identity.SamePathName(filepath.Clean(moved), filepath.Clean(program)) {
			return nil, errors.New("handover: this process still runs the published connector")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		held, err := nativecli.OpenSource(ctx, program)
		if err != nil {
			return nil, err
		}
		defer held.Close()
		key, err := fileid.FileKeyOf(held)
		if err != nil {
			return nil, err
		}
		candidate := held.Name()
		if info, err := held.Stat(); err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("handover: no published connector")
		}
		// The runtime's kept image is the authority (CONNECTOR-HANDOVER-AUTH,
		// ruling c): it reads enrolled only while the file at the enrolled
		// path carries the key it recorded at Keep or at the update's commit.
		if err := agentEnrollmentHolds(ctx, read, application, candidate); err != nil {
			return nil, err
		}
		if agentImageSigned(moved) && !agentSuccessorAuthentic(candidate, moved) {
			return nil, errors.New("handover: the published connector is not signed by this connector's publisher")
		}
		agentSuccessorBeforeStart(candidate)
		// The helper has no console of its own; its successor opens none.
		cmd := childproc.Hidden(exec.Command(candidate, args...))
		cmd.Dir = folder
		cmd.Env = env
		cmd.Stderr = os.Stderr
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			in.Close()
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		stop := func() {
			in.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if started, err := agentChildImageKey(cmd.Process.Pid); err != nil || started != key {
			stop()
			return nil, errors.New("handover: the started successor is not the verified connector")
		}
		return &agentSuccessor{in: in, out: out, stop: stop}, nil
	}
}
