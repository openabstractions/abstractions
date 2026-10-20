// A small application using the facade for durable downloads and its logging
// adapter for attributed events. The runtime owns the download; the application
// keeps the request identity and copies the verified result where it wants it.
// --runtime-program binds development runs to one explicit runtime. --source
// supplies a deterministic loopback fixture for the existing review runner.
//
//	pretend-lemonade URL SIZE sha256:DIGEST
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	logging "github.com/openabstractions/abstraction-logging/go"
	watch "github.com/openabstractions/abstraction-watch/go"
)

// machine selects the installed runtime. Tests supply a verified fixture binding.
var machine = facade.Discover

func main() {
	if ownedHost() {
		return
	}
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

const usage = `Usage: pretend-lemonade [--out DIR] [--ask-before-replace] [--wait-output-choice] [--notify-when-done | --notification] [--runtime-program PATH] URL SIZE sha256:DIGEST
       pretend-lemonade [--runtime-program PATH] --register
       pretend-lemonade [--runtime-program PATH] --read-secret NAME
       pretend-lemonade --source
       pretend-lemonade --program

Downloads verified bytes through the selected runtime. The same URL, size and
digest retain the same request identity. --out chooses where this application
copies the result (default files). --runtime-program verifies the development runtime
the facade selects (FAC-B5) as this account's named executable.
--ask-before-replace presents Replace, Keep or Cancel in this app's terminal.
Type the word or r/k/c, then press Enter. Closing input retains the existing file.
--wait-output-choice waits for an external answer instead of reading terminal input;
it requires --ask-before-replace. Ordinary choices do not open an OA popup.
Each invocation is a new output choice; the completed download is reused.
--notify-when-done posts a notification the runtime shows when the download
ends, then exits; its Copy now and Show in Activity actions work with this app
closed. A later run with --notification reads the recorded choice: Copy now
copies the result, then the app acknowledges it.
--register adds this app to Apps as Pretend Lemonade; OpenAbstractions then
asks whether to keep it.
--read-secret reads one saved secret, a permission each app needs the person's
yes for; it prints only the value's length.
--source serves a deterministic local test file and a slower copy, printing
their addresses, size and digest. It runs until this process is stopped.
`

func run(args []string, out io.Writer) error {
	return runInput(args, out, os.Stdin)
}

func runInput(args []string, out io.Writer, stdin *os.File) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		_, err := io.WriteString(out, usage)
		return err
	}
	if len(args) == 1 && args[0] == "--program" {
		exe, err := os.Executable()
		if err == nil {
			_, err = fmt.Fprintln(out, identity.CanonicalProgramPath(exe))
		}
		return err
	}
	if len(args) == 1 && args[0] == "--source" {
		return serveSource(out)
	}
	flags := flag.NewFlagSet("pretend-lemonade", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	destination := flags.String("out", "files", "directory for copied results")
	askBeforeReplace := flags.Bool("ask-before-replace", false, "ask before replacing an existing copied output")
	waitOutput := flags.Bool("wait-output-choice", false, "wait for an external output-choice answer")
	runtimeProgram := flags.String("runtime-program", "", "verify the explicitly selected runtime program")
	notifyWhenDone := flags.Bool("notify-when-done", false, "post a notification the runtime shows when the download ends, then exit")
	readNotification := flags.Bool("notification", false, "read the notification's recorded choice; Copy now copies the result")
	register := flags.Bool("register", false, "add this app to Apps")
	secret := flags.String("read-secret", "", "read one saved secret's value")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	args = flags.Args()
	if *register || *secret != "" {
		if len(args) != 0 || (*register && *secret != "") {
			return errors.New(usage)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		selected, err := selectMachine(*runtimeProgram)
		if err != nil {
			return err
		}
		if *register {
			return registerSelf(ctx, selected, out)
		}
		return readSecret(ctx, selected, *secret, out)
	}
	if len(args) != 3 {
		return errors.New(usage)
	}
	if *waitOutput && !*askBeforeReplace {
		return errors.New("--wait-output-choice requires --ask-before-replace")
	}
	var input outputChoiceInput
	if *askBeforeReplace && !*waitOutput {
		input = terminalOutputInput(stdin, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	selected, err := selectMachine(*runtimeProgram)
	if err != nil {
		return err
	}
	jobs, err := selected.ResolveJobs(ctx, facade.Requirements{})
	if err != nil {
		return fmt.Errorf("no download service on this machine: %w", err)
	}
	fmt.Fprintf(out, "downloads go to : %s\n", jobs.Endpoint())
	size, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return err
	}
	window, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		return err
	}
	// The same URL, size and digest are the same request: a rerun reconciles
	// with the work already accepted instead of starting it twice.
	requestID := api.RequestIdentity{Key: fmt.Sprintf("pretend-lemonade-%x", sha256.Sum256([]byte(args[0]+"\n"+args[1]+"\n"+args[2]))), HistoryEpoch: window.HistoryEpoch}
	sink, err := selected.ResolveLog(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return fmt.Errorf("logging service: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logger := logging.NewClientHandler(sink, &logging.Options{Program: exe})
	event := func(message, operation string) error {
		record := slog.NewRecord(time.Now(), slog.LevelInfo, message, 0)
		record.AddAttrs(slog.String("component", "pretend-lemonade"), slog.String("request.key", requestID.Key),
			slog.String("operation.id", operation), slog.Int64("expected.bytes", size))
		return logger.Handle(ctx, record)
	}
	if err := event("download requested", ""); err != nil {
		return fmt.Errorf("logging request: %w", err)
	}
	spec := request.Encode(&request.Request{Artifact: request.Artifact{Size: size, Digest: args[2]}, Sources: []request.Source{{Scheme: "http", Locator: args[0]}}})
	accepted, err := jobs.Submit(ctx, api.Submission{Identity: requestID, Kind: "download", Spec: spec})
	if err != nil {
		if accepted, err = jobs.Reconcile(ctx, requestID); err != nil {
			return err
		}
	}
	if accepted.Outcome != api.AcceptanceOutcomeAccepted || accepted.Receipt == nil {
		if err := event("download not accepted: "+accepted.Outcome.String(), ""); err != nil {
			return fmt.Errorf("logging refusal: %w", err)
		}
		return fmt.Errorf("download not accepted: %s %s", accepted.Outcome, accepted.Reason)
	}
	fmt.Fprintf(out, "submitted %s\n", accepted.Receipt.OperationID)
	if err := event("download accepted", accepted.Receipt.OperationID); err != nil {
		return fmt.Errorf("logging accepted work: %w", err)
	}
	refFile := notificationRefFile(*destination, requestID.Key)
	if *notifyWhenDone {
		ref, err := retainedRef(refFile, requestID.Key, true)
		if err != nil {
			return err
		}
		return postDone(ctx, selected, ref, accepted.Receipt.OperationID, downloadName(args[0]), size, out)
	}
	var acknowledge func() error
	if *readNotification {
		ref, err := retainedRef(refFile, requestID.Key, false)
		if err != nil {
			return fmt.Errorf("no notification was posted for this download: %w", err)
		}
		copyNow, ack, err := notificationOutcome(ctx, selected, ref, out)
		if err != nil {
			return err
		}
		if !copyNow {
			if ack != nil {
				return ack()
			}
			return nil
		}
		acknowledge = ack
	}
	follower := watch.Follow(ctx, "", func(ctx context.Context, after string) func(func(api.WorkObservation, error) bool) {
		return jobs.FollowWorkPages(ctx, requestID, after, 1000)
	}, func(page api.WorkObservation) (string, bool) {
		return page.Next, page.Outcome == api.WorkObservationOutcomeGap
	})
	defer follower.Close()
	for {
		observed, err := follower.Next(ctx)
		if err != nil {
			return err
		}
		if observed.Outcome == api.WorkObservationOutcomeUnchanged {
			continue
		}
		if observed.Snapshot == nil {
			return fmt.Errorf("download unobservable: %s", observed.Outcome)
		}
		switch observed.Snapshot.State {
		case api.WorkStateComplete:
			message, copyErr := deliver(ctx, selected, jobs, requestID, args[0], *destination, size, args[2], *askBeforeReplace, out, input)
			if message != "" {
				if err := event(message, accepted.Receipt.OperationID); err != nil {
					return err
				}
			}
			if copyErr == nil && acknowledge != nil {
				// The copy is done; the service no longer needs to replay the choice.
				return acknowledge()
			}
			return copyErr
		case api.WorkStateFailed, api.WorkStateCancelled:
			ended := observed.Snapshot.State.String() + failureDetail(observed.Snapshot)
			if err := event("download ended: "+ended, accepted.Receipt.OperationID); err != nil {
				return err
			}
			return fmt.Errorf("download %s", ended)
		}
	}
}

// failureDetail names a failed job's recorded reason, such as rights:denied.
func failureDetail(snapshot *api.OperationSnapshot) string {
	if snapshot.Failure == nil || snapshot.Failure.Message == "" {
		return ""
	}
	return ": " + snapshot.Failure.Message
}

func selectMachine(program string) (*facade.Machine, error) {
	if program == "" {
		return machine(), nil
	}
	selected, err := bootstrap.SelectDevelopment("", program)
	if err != nil {
		return nil, fmt.Errorf("--runtime-program: %w", err)
	}
	return client.NewVerified(selected.Endpoint, selected.Server), nil
}
