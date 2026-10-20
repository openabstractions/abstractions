package runtimehost

import (
	"context"
	"fmt"
	"os"

	"github.com/openabstractions/abstraction-facade/go/supervision"
)

const executableName = "openabstractions.exe"

type windowsProcess struct{ child *supervision.Process }

func (p *windowsProcess) pid() int         { return p.child.PID() }
func (p *windowsProcess) terminate() error { return p.child.Stop() }
func (p *windowsProcess) wait() error {
	code, err := p.child.Wait()
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("runtimehost: child exit code %d", code)
	}
	return nil
}

func launch(ctx context.Context, o Options) (_ process, _ *os.File, _ *os.File, err error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	defer inR.Close()
	defer func() {
		if err != nil {
			inW.Close()
		}
	}()
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	defer outW.Close()
	defer func() {
		if err != nil {
			outR.Close()
		}
	}()
	child, err := supervision.Start(ctx, supervision.Options{
		Executable: o.Executable,
		Args:       append([]string{"serve", "runtime", "--supervised"}, o.Args...),
		Stdin:      inR, Stdout: outW, Stderr: o.Stderr,
		ForceTimeout: o.ForceTimeout,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return &windowsProcess{child: child}, inW, outR, nil
}
