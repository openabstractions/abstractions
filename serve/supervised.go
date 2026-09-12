package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// superviseRuntime owns no global control endpoint. The installed parent keeps
// the sole write end of stdin open and closes it to request cooperative shutdown.
// Child output is READY 1\n after every configured listener has initialized.
// The parent must also observe process exit and bound startup/shutdown waits.
func superviseRuntime(parent context.Context, options runtimeFlags, input *os.File, output io.Writer) error {
	info, err := input.Stat()
	if err != nil {
		return fmt.Errorf("runtime control pipe: %w", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("runtime: --supervised requires a private stdin pipe")
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	go func() {
		var one [1]byte
		n, err := input.Read(one[:])
		if n != 0 {
			cancel(errors.New("runtime: unexpected control bytes"))
			return
		}
		if err == nil {
			err = errors.New("runtime: control read made no progress")
		}
		cancel(err)
	}()
	// Do not join the stdin reader: SIGTERM must complete while the parent still
	// holds its writer. Process exit releases the inherited read handle.
	err = runRuntimeReady(ctx, options, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.WriteString(output, "READY 1\n")
		if err == nil && n != len("READY 1\n") {
			err = io.ErrShortWrite
		}
		if err != nil {
			cancel(err)
			return fmt.Errorf("runtime readiness: %w", err)
		}
		return nil
	})
	cause := context.Cause(ctx)
	if errors.Is(cause, io.EOF) && (err == nil || errors.Is(err, context.Canceled)) {
		return nil
	}
	if cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return err
}
