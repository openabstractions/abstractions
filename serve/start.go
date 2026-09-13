package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	"io"
	"os/exec"
	"time"
)

type startHooks struct {
	guard    func() error
	activate func(context.Context) error
	ready    func(context.Context) (bool, error)
}

func runtimeStart(args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	budget := flags.Duration("timeout", 20*time.Second, "total activation and readiness waiting budget")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *budget <= 0 {
		return errors.New("start: supply valid flags and a positive timeout")
	}
	endpoint, err := resolution.CheckedDefaultEndpoint()
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
		case "resolved":
		case "not_ready", "unavailable":
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
