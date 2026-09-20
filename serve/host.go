package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The host owns the lifetime of one supervised runtime child. It launches the
// child, restarts it after failure with a bounded backoff and exits with
// failure when the backoff is used up, so the operating system's own restart
// policy (SCM recovery, the next activation or sign-in) takes over.
//
// Linux and macOS need no host: systemd and launchd run `serve runtime`
// directly with their own restart policy. The Windows command is `serve host`.

// hostRestartDelays is the wait before each successive restart of a failed
// child. A fourth failure ends the host.
var hostRestartDelays = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}

// hostHealthyRun is how long a child must have run for its failure to count as
// the first one again. A host that lives for weeks is not ended by three
// failures spread across them.
const hostHealthyRun = 5 * time.Minute

type runtimeChild interface {
	PID() int
	Wait() error
	Close() error
}

type hostPlan struct {
	// start launches the child and returns once it reported readiness.
	start func(context.Context) (runtimeChild, error)
	// served reports whether a runtime already answers this user's endpoint.
	// The host then has nothing to do: one listener serves each endpoint.
	served func(context.Context) bool
	// guard refuses a launch while an installer replaces this installation.
	guard   func() error
	delays  []time.Duration
	healthy time.Duration
	logf    func(format string, args ...any)
	now     func() time.Time
	sleep   func(context.Context, time.Duration) bool
}

var errRuntimeChildExited = errors.New("runtime child exited unexpectedly")

// run supervises until ctx ends, which is a clean stop and returns nil, or
// until the restart budget is used up.
func (p hostPlan) run(ctx context.Context) error {
	delays := p.delays
	if delays == nil {
		delays = hostRestartDelays
	}
	healthy := p.healthy
	if healthy == 0 {
		healthy = hostHealthyRun
	}
	now := p.now
	if now == nil {
		now = time.Now
	}
	sleep := p.sleep
	if sleep == nil {
		sleep = sleepContext
	}
	logf := p.logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		if p.guard != nil {
			if err := p.guard(); err != nil {
				return err
			}
		}
		if p.served != nil && p.served(ctx) {
			logf("a runtime already serves this user's endpoint; this host has nothing to run")
			return nil
		}
		began := now()
		child, err := p.start(ctx)
		if err == nil {
			logf("runtime child %d ready", child.PID())
			err = child.Wait()
			if ctx.Err() != nil {
				if err != nil {
					logf("runtime child %d stopped: %v", child.PID(), err)
				}
				return nil
			}
			if err == nil {
				err = errRuntimeChildExited
			}
			err = fmt.Errorf("runtime child %d: %w", child.PID(), err)
		}
		if ctx.Err() != nil {
			return nil
		}
		if now().Sub(began) >= healthy {
			failures = 0
		}
		if failures >= len(delays) {
			logf("runtime failed %d times in a row; the host exits: %v", failures+1, err)
			return fmt.Errorf("runtime failed %d times in a row: %w", failures+1, err)
		}
		delay := delays[failures]
		failures++
		logf("runtime failed: %v; restart %d of %d in %s", err, failures, len(delays), delay)
		if !sleep(ctx, delay) {
			return nil
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
