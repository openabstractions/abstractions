package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
)

const usage = "Usage: logging-quickstart [--runtime-program PATH] [RUNTIME_ENDPOINT]\n       logging-quickstart --program"

func main() {
	if ownedHost() {
		return
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run writes one event through the development runtime the facade selects
// [FAC-B5] and reads it back. It returns the exit status: 0 when the event
// was read back, 2 for a usage error and 1 for any other failure.
func run(args []string, stdout, stderr io.Writer) int {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.Abs(exe)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(args) == 1 && args[0] == "--program" {
		// The path the runtime observes, 8.3 short names expanded, so a rights
		// rule written from this output names the same program.
		fmt.Fprintln(stdout, identity.CanonicalProgramPath(exe))
		return 0
	}
	flags := flag.NewFlagSet("logging-quickstart", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runtimeProgram := flags.String("runtime-program", "", "verify the selected runtime as this account's named executable")
	flags.Usage = func() { fmt.Fprintln(stderr, usage) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		flags.Usage()
		return 2
	}
	endpoint, _ := bootstrap.DevelopmentEndpoint(flags.Arg(0))
	if endpoint == "" {
		fmt.Fprintln(stderr, bootstrap.ErrNoDevelopmentRuntime)
		flags.Usage()
		return 2
	}
	machine := client.NewUnverified(endpoint)
	if *runtimeProgram != "" {
		selected, err := bootstrap.SelectDevelopment(endpoint, *runtimeProgram)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		machine = client.NewVerified(selected.Endpoint, selected.Server)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := writeAndReadBack(ctx, machine, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// writeAndReadBack writes one event with a unique event_id and pages the
// retained history until that event appears or ctx ends.
func writeAndReadBack(ctx context.Context, machine *facade.Machine, stdout io.Writer) error {
	need := facade.Requirements{Scope: facade.ScopeLocal}
	sink, err := machine.ResolveLog(ctx, need)
	if err != nil {
		return err
	}
	eventID := fmt.Sprintf("quickstart-%d-%d", time.Now().UTC().UnixNano(), os.Getpid())
	message := "worker started"
	if err := sink.LogContext(ctx, 0, message, map[string]string{
		"component": "quickstart",
		"event_id":  eventID,
	}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote: %s (%s)\n", message, eventID)

	history, err := machine.ResolveLogReader(ctx, need)
	if err != nil {
		return err
	}
	cursor := ""
	for {
		page, err := history.ReadContext(ctx, cursor, 256, 65536)
		if err != nil {
			return err
		}
		if page.Outcome != logwire.PageOutcomePage {
			return fmt.Errorf("history returned %s", page.Outcome)
		}
		for _, record := range page.Records {
			if record.Attrs["event_id"] == eventID {
				fmt.Fprintf(stdout, "read back: %s (component=%s)\n", record.Msg, record.Attrs["component"])
				return nil
			}
		}
		cursor = page.Next
		if !page.AtEnd {
			continue
		}
		select {
		case <-ctx.Done():
			return errors.New("record was not retained before the deadline")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
