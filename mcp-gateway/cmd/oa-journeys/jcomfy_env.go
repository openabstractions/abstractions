//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/profile"
	"github.com/openabstractions/abstraction-identity/listen"
)

// startComfyEnv starts an isolated runtime and its Panel for J-COMFY-OPEN.
//
// supervised: the runtime runs inside the job the installed supervisor gives
// the account's runtime. serve/internal/runtimehost (launch_windows.go)
// starts it through supervision.Start, which creates a kill-on-close job and
// places the child in it in the same CreateProcess; this journey calls
// supervision.Start with the same options. Two
// flags differ from the installed launch: `serve supervisor` always
// supervises the installed runtime on the account's default endpoint, and
// `serve runtime --supervised` refuses --operator-program
// (serve/runtime_credentials.go isolatedOperatorProgramWithState), which the
// driver needs to register its agent. Only --supervised's stdin control pipe
// and READY 1 line are missing; the job is the same.
//
// Unsupervised: started as startEnv starts the suite's runtime, in the
// driver's job, which allows breakaway, as a development runtime runs.
//
// A runtime started from a packaged program's process tree (the Claude
// desktop app is one) sees a virtualized AppData, and the owned launch
// refuses with "virtualized runtime profile" (serve
// application_owned_launch_windows.go ownedApplicationLaunchContext). When
// the driver itself sees a virtualized profile, the runtime gets a private
// LOCALAPPDATA under work, as an installed runtime started by the
// supervisor has a real one; the returned note says so.
func startComfyEnv(ctx context.Context, bin, tools, work string, supervised bool, logf func(string, ...any)) (*env, func(), string, error) {
	if err := os.MkdirAll(work, 0o700); err != nil {
		return nil, nil, "", err
	}
	e := &env{bin: bin, tools: tools, work: work, state: filepath.Join(work, "state"), runtime: filepath.Join(tools, "openabstractions.exe"), logf: logf}
	self, err := os.Executable()
	if err != nil {
		return nil, nil, "", err
	}
	environment := os.Environ()
	note := ""
	if view, err := profile.Current(); err == nil && view.Virtualized {
		local := filepath.Join(work, "localappdata")
		if err := os.MkdirAll(local, 0o700); err != nil {
			return nil, nil, "", err
		}
		kept := environment[:0:0]
		for _, v := range environment {
			if !strings.HasPrefix(strings.ToUpper(v), "LOCALAPPDATA=") {
				kept = append(kept, v)
			}
		}
		environment = append(kept, "LOCALAPPDATA="+local)
		note = "driver runs in package " + view.Family + "; the runtime's LOCALAPPDATA is " + local
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return nil, nil, "", err
	}
	stdout := mustLog(work, "runtime.stdout.log")
	name := "jrn-" + newID()[:10]
	e.isolated = name
	args := []string{"serve", "runtime", "--isolated", name, "--state-dir", e.state, "--operator-program", self}
	var stopRuntime func()
	if supervised {
		// The same options serve/internal/runtimehost/launch_windows.go gives
		// the OA service, including FAC-P5's AllowBreakaway; keep them equal.
		child, err := supervision.Start(ctx, supervision.Options{Executable: e.runtime, Args: args, Env: environment,
			Stdout: stdout, Stderr: errW, ForceTimeout: 5 * time.Second, AllowBreakaway: true})
		errW.Close()
		if err != nil {
			errR.Close()
			return nil, nil, "", fmt.Errorf("supervision.Start: %w", err)
		}
		logf("comfy runtime (supervised) pid %d", child.PID())
		stopRuntime = func() {
			_ = child.Stop()
			done := make(chan struct{})
			go func() { _, _ = child.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				logf("supervised runtime pid %d did not exit within 15 s of Stop", child.PID())
			}
		}
	} else {
		cmd := childproc.Hidden(exec.Command(e.runtime, args...))
		cmd.Env = environment
		cmd.Stdout, cmd.Stderr = stdout, errW
		err := cmd.Start()
		errW.Close()
		if err != nil {
			errR.Close()
			return nil, nil, "", err
		}
		own(cmd.Process.Pid)
		e.runtimeCmd = cmd
		logf("comfy runtime (development) pid %d", cmd.Process.Pid)
		stopRuntime = func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }
	}
	stop := func() { e.close(); stopRuntime() }
	found := make(chan string, 1)
	go scanLines(errR, mustLog(work, "runtime.stderr.log"), func(line string) {
		if m := endpointLine.FindStringSubmatch(line); m != nil {
			select {
			case found <- strings.TrimSpace(m[1]):
			default:
			}
		}
	})
	select {
	case e.endpoint = <-found:
	case <-time.After(60 * time.Second):
		stop()
		return nil, nil, "", errors.New("runtime printed no ABSTRACTION_RUNTIME_ENDPOINT within 60 s (see runtime.stderr.log)")
	}
	who, err := identity.Self()
	if err != nil {
		stop()
		return nil, nil, "", err
	}
	e.machine = facade.NewVerified(e.endpoint, listen.ServerExpectation{Principal: who, Program: identity.CanonicalProgramPath(filepath.Clean(e.runtime))})
	panel := childproc.Hidden(exec.Command(filepath.Join(tools, "Abstraction Panel.exe"), "-native=false", "-open=false", "-addr", "127.0.0.1:0",
		"-runtime-endpoint", e.endpoint, "-runtime-program", e.runtime, "-runtime-state-dir", e.state))
	panel.Env = isolatedEnvironment(e.endpoint)
	out, err := panel.StdoutPipe()
	if err != nil {
		stop()
		return nil, nil, "", err
	}
	panel.Stderr = mustLog(work, "panel.stderr.log")
	if err := panel.Start(); err != nil {
		stop()
		return nil, nil, "", err
	}
	e.procs = append(e.procs, panel)
	own(panel.Process.Pid)
	urls := make(chan []string, 1)
	go scanLines(out, mustLog(work, "panel.stdout.log"), func(line string) {
		if m := panelLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			select {
			case urls <- m[1:]:
			default:
			}
		}
	})
	select {
	case u := <-urls:
		e.panelURL, e.panelKey = strings.TrimRight(u[0], "/"), u[1]
	case <-time.After(60 * time.Second):
		stop()
		return nil, nil, "", errors.New("Panel printed no URL within 60 s (see panel.stderr.log)")
	}
	logf("comfy runtime %s, Panel %s", e.endpoint, e.panelURL)
	return e, stop, note, nil
}
