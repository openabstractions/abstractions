// An application that knows nothing about NASes, BITS, shares or job stores.
//
// This is the entire integration a fork of Lemonade would carry: one facade
// import, no path, no hostname, no flag, and no branch on who does the work.
// The installed runtime owns the download; the application keeps the request
// identity and copies the verified result where it wants it.
//
//	pretend-lemonade URL SIZE sha256:DIGEST
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	facade "github.com/openabstractions/abstraction-facade/go"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// machine selects the installed runtime. Tests supply a verified fixture binding.
var machine = facade.Discover

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	jobs, err := machine().ResolveJobs(ctx, facade.Requirements{})
	if err != nil {
		return fmt.Errorf("no download service on this machine: %w", err)
	}
	fmt.Fprintf(out, "downloads go to : %s\n", jobs.Endpoint())
	if len(args) == 0 {
		return nil
	}
	if len(args) != 3 {
		return errors.New("usage: pretend-lemonade URL SIZE sha256:DIGEST")
	}
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
	identity := api.RequestIdentity{Key: fmt.Sprintf("pretend-lemonade-%x", sha256.Sum256([]byte(args[0]+"\n"+args[1]+"\n"+args[2]))), HistoryEpoch: window.HistoryEpoch}
	spec := request.Encode(&request.Request{Artifact: request.Artifact{Size: size, Digest: args[2]}, Sources: []request.Source{{Scheme: "http", Locator: args[0]}}})
	accepted, err := jobs.Submit(ctx, api.Submission{Identity: identity, Kind: "download", Spec: spec})
	if err != nil {
		if accepted, err = jobs.Reconcile(ctx, identity); err != nil {
			return err
		}
	}
	if accepted.Outcome != api.AcceptanceOutcomeAccepted || accepted.Receipt == nil {
		return fmt.Errorf("download not accepted: %s %s", accepted.Outcome, accepted.Reason)
	}
	fmt.Fprintf(out, "submitted %s\n", accepted.Receipt.OperationID)
	for {
		observed, err := jobs.ObserveWork(ctx, identity)
		if err != nil {
			return err
		}
		if observed.Snapshot == nil {
			return fmt.Errorf("download unobservable: %s", observed.Outcome)
		}
		switch observed.Snapshot.State {
		case api.WorkStateComplete:
			return deliver(ctx, jobs, identity, args[0], out)
		case api.WorkStateFailed, api.WorkStateCancelled:
			return fmt.Errorf("download %s", observed.Snapshot.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func deliver(ctx context.Context, jobs *facade.JobsClient, identity api.RequestIdentity, source string, out io.Writer) error {
	name := path.Base(source)
	if name == "." || name == "/" {
		name = "download"
	}
	where, err := filepath.Abs(filepath.Join("files", name))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(where), 0o755); err != nil {
		return err
	}
	f, err := os.Create(where)
	if err != nil {
		return err
	}
	if _, err := jobs.CopyResult(ctx, identity, f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(out, "delivered to %s\n", where)
	return nil
}
