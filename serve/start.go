package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	"io"
	"os/exec"
	"strings"
	"time"
)

const startUsage = `Usage: openabstractions start [options]

Activates this installation's own runtime and waits until it answers ready,
never a different runtime. ABSTRACTION_RUNTIME_ENDPOINT is a client concern:
start ignores it and waits on this installation's own registered endpoint.

Options:
  --timeout D   deadline for activation and readiness (default 20s)

Exit codes: 0 ready, 3 an installer holds the upgrade exclusion, 4 this caller
sees a packaged app's private copy of AppData, 1 otherwise.
`

type startHooks struct {
	guard    func() error
	activate func(context.Context) error
	ready    func(context.Context) (bool, error)
}

func runtimeStart(args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, startUsage)
		return err
	}
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	//unchecked: usage text on a help/error path; a failed write to diagnostics has nothing further to report
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	budget := flags.Duration("timeout", 20*time.Second, "deadline for activation and readiness")
	// Start always refuses an elevated or root token; the installer names the
	// requirement on its activation command line.
	flags.Bool("require-unelevated", false, "refuse an elevated token (always enforced; accepted for the installer's command line)")
	if err := flags.Parse(args); err != nil {
		return badFlag(flags, diagnostics, "start", startUsage, args, err)
	}
	if flags.NArg() != 0 {
		return flagMistake(diagnostics, "start", startUsage, fmt.Sprintf("unexpected argument %q", flags.Arg(0)))
	}
	if *budget <= 0 {
		value := budget.String()
		if typed, ok := flagValueAsTyped(args, "timeout"); ok {
			value = typed
		}
		return flagMistake(diagnostics, "start", startUsage, fmt.Sprintf("--timeout %q is not a positive duration (examples: 5s, 1m30s, 500ms)", value))
	}
	// This installation's own registered endpoint, never
	// ABSTRACTION_RUNTIME_ENDPOINT: that variable is a client concern, and
	// waiting on a name it chose can wait out the whole budget on an
	// installation this process never started (see startUsage).
	endpoint, err := bootstrap.InstalledEndpoint("runtime-v1")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()
	hooks := startHooks{guard: startGuard, activate: activateInstalled, ready: func(ctx context.Context) (bool, error) { return startReady(ctx, endpoint) }}
	if err := startInstalled(ctx, hooks); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "runtime ready")
	return err
}
func startInstalled(ctx context.Context, h startHooks) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.guard(); err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	ready, err := h.ready(probe)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.activate(ctx); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("start readiness: %w", err)
		}
		ready, err := h.ready(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("start readiness: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
func startReady(ctx context.Context, endpoint string) (bool, error) {
	client := resolution.NewClient(endpoint, time.Second)
	if strings.HasPrefix(endpoint, "xpc:") {
		selection, err := bootstrap.SelectInstalled(ctx)
		if err != nil {
			return false, err
		}
		if selection.Endpoint != endpoint {
			return false, errors.New("start: selected runtime endpoint differs from readiness endpoint")
		}
		client = resolution.NewVerifiedClient(endpoint, time.Second, selection.Server)
	}
	for _, item := range runtimeContracts() {
		result, err := client.Resolve(ctx, wire.ResolveRequest{Capability: item[0], Contracts: []string{item[1]}, Scope: wire.ScopeLocal})
		if err != nil {
			var service *wire.ServiceError
			if errors.As(err, &service) {
				return false, err
			}
			return false, nil
		} // An unavailable transport is retried only inside the caller's budget.
		switch result.Status {
		case wire.ResolutionStatusResolved:
		case wire.ResolutionStatusNotReady, wire.ResolutionStatusUnavailable:
			return false, nil
		default:
			return false, fmt.Errorf("start readiness refused: %s: %s", item[0], result.Status)
		}
	}
	return true, nil
}
func startCommand(ctx context.Context, path string, args ...string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	hideStartCommand(cmd)
	// No inherited pipe output can keep Wait alive after the command is killed.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("activation refused: %s %v: %w", path, args, err)
	}
	return nil
}
