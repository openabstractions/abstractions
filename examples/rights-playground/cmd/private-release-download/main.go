// private-release-download is the labelled P-K1 adopter fixture. It names a
// credential; the selected runtime applies it to HTTP requests.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

const usage = `Labelled private-release download fixture (P-K1).
  --endpoint ENDPOINT --runtime-program ABSOLUTE_FILE --history
  --endpoint ENDPOINT --runtime-program ABSOLUTE_FILE --credential NAME
  --url URL --bytes N --sha256 HEX --out ABSOLUTE_FILE --epoch EPOCH --key KEY [--reconcile]

An operator registers the credential and permits this program separately.
Use --history to read the selected runtime's history epoch. Choose a new key
for a new operation. After an uncertain result, repeat the exact operation with
the same epoch/key and --reconcile. Preserve those inputs until terminal.
Only the runtime receives the credential value. This app has no key option,
stdin key input, credential reader, holder, or applier. It reads no environment.
HTTP is limited to the labelled 127.0.0.1 fixture; ordinary sources use HTTPS.
`

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type options struct {
	endpoint, runtime, credential, source, digest, output, epoch, key string
	size                                                              int64
	history, reconcile                                                bool
}

type jobClient interface {
	GetHistoryWindow(context.Context) (api.HistoryWindow, error)
	Submit(context.Context, api.Submission) (api.AcceptanceResult, error)
	Reconcile(context.Context, api.RequestIdentity) (api.AcceptanceResult, error)
	ObserveWork(context.Context, api.RequestIdentity) (api.ObservationResult, error)
	CopyResult(context.Context, api.RequestIdentity, io.Writer) (int64, error)
}

func parse(args []string, out io.Writer) (options, error) {
	var o options
	f := flag.NewFlagSet("private-release-download", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.endpoint, "endpoint", "", "explicit native endpoint")
	f.StringVar(&o.runtime, "runtime-program", "", "expected runtime executable")
	f.StringVar(&o.credential, "credential", "", "credential name")
	f.StringVar(&o.source, "url", "", "source URL")
	f.StringVar(&o.digest, "sha256", "", "expected SHA256")
	f.StringVar(&o.output, "out", "", "new output file")
	f.StringVar(&o.epoch, "epoch", "", "history epoch")
	f.StringVar(&o.key, "key", "", "stable operation key")
	f.Int64Var(&o.size, "bytes", 0, "expected bytes, at most 16 MiB")
	f.BoolVar(&o.history, "history", false, "read history epoch")
	f.BoolVar(&o.reconcile, "reconcile", false, "observe retained identity without submitting")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, e := io.WriteString(out, usage)
			return o, e
		}
		return o, errors.New("invalid arguments; use --help")
	}
	if len(args) == 0 {
		_, err := io.WriteString(out, usage)
		return o, err
	}
	if f.NArg() != 0 || o.endpoint == "" || !filepath.IsAbs(o.runtime) {
		return o, errors.New("select an endpoint and absolute runtime program")
	}
	if o.history {
		if o.credential != "" || o.source != "" || o.digest != "" || o.output != "" || o.epoch != "" || o.key != "" || o.size != 0 || o.reconcile {
			return o, errors.New("history takes only the runtime selection")
		}
		return o, nil
	}
	u, err := url.Parse(o.source)
	d, decodeErr := hex.DecodeString(o.digest)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" || (u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "")) || len(o.source) > 2048 || !namePattern.MatchString(o.credential) || !namePattern.MatchString(o.key) || len(o.epoch) == 0 || len(o.epoch) > 256 || o.size < 1 || o.size > 16<<20 || decodeErr != nil || len(d) != sha256.Size || !filepath.IsAbs(o.output) {
		return o, errors.New("invalid download metadata; use --help")
	}
	return o, nil
}

func guarantees() []string {
	return append(append([]string(nil), api.AdmissionGuarantees...), request.ScopedCredentialGuarantees...)
}

func submission(o options) api.Submission {
	return api.Submission{Identity: api.RequestIdentity{Key: o.key, HistoryEpoch: o.epoch}, Kind: "download", Label: "Private release fixture", RequiredGuarantees: guarantees(), Spec: request.Encode(&request.Request{Artifact: request.Artifact{Size: o.size, Digest: "sha256:" + o.digest}, Sources: []request.Source{{Scheme: "http", Locator: o.source, Credential: o.credential}}})}
}

var errUncertain = errors.New("acceptance or effect uncertain; preserve inputs and use the same epoch/key with --reconcile")

// download never prints provider diagnostics or result bytes. Only a fully
// bounded result matching the caller's artifact identity reaches the new file.
func download(ctx context.Context, jobs jobClient, o options, out io.Writer) error {
	s := submission(o)
	var accepted api.AcceptanceResult
	var err error
	direct := false
	if o.reconcile {
		accepted, err = jobs.Reconcile(ctx, s.Identity)
	} else {
		accepted, err = jobs.Submit(ctx, s)
		direct = err == nil
		if err != nil {
			accepted, err = jobs.Reconcile(ctx, s.Identity)
		}
	}
	if err != nil || accepted.Outcome == api.AcceptanceOutcomeUnknown || accepted.Outcome == api.AcceptanceOutcomeUnavailable {
		return errUncertain
	}
	if accepted.Outcome != api.AcceptanceOutcomeAccepted || accepted.Receipt == nil {
		if accepted.Outcome == api.AcceptanceOutcomeDefinitelyNotAccepted || direct && (accepted.Outcome == api.AcceptanceOutcomeForbidden || accepted.Outcome == api.AcceptanceOutcomeInvalid) {
			return fmt.Errorf("download admission: %s", accepted.Outcome)
		}
		return errUncertain
	}
	if accepted.Receipt.Identity != s.Identity {
		return errUncertain
	}
	for _, required := range s.RequiredGuarantees {
		if !slices.Contains(accepted.Receipt.AcceptedGuarantees, required) {
			return errUncertain
		}
	}
	for {
		observed, err := jobs.ObserveWork(ctx, s.Identity)
		if err != nil || observed.Snapshot == nil {
			return errUncertain
		}
		switch observed.Snapshot.State {
		case api.WorkStateComplete:
			var data bytes.Buffer
			written, err := jobs.CopyResult(ctx, s.Identity, &boundedResult{buffer: &data, remaining: o.size})
			if err != nil || written != o.size || int64(data.Len()) != o.size {
				return errors.New("download result unavailable or exceeds expected size")
			}
			hash := sha256.Sum256(data.Bytes())
			if hex.EncodeToString(hash[:]) != o.digest {
				return errors.New("download result digest mismatch")
			}
			file, err := os.OpenFile(o.output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return errors.New("new output file could not be created")
			}
			n, writeErr := file.Write(data.Bytes())
			syncErr, closeErr := file.Sync(), file.Close()
			if writeErr != nil || syncErr != nil || closeErr != nil || int64(n) != o.size {
				return errors.New("output write incomplete; preserve operation identity")
			}
			_, err = fmt.Fprintln(out, "Private release fixture: verified download saved.")
			return err
		case api.WorkStateFailed, api.WorkStateCancelled:
			return fmt.Errorf("download work: %s", observed.Snapshot.State)
		}
		select {
		case <-ctx.Done():
			return errUncertain
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type boundedResult struct {
	buffer    *bytes.Buffer
	remaining int64
}

func (w *boundedResult) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("result exceeds expected size")
	}
	w.remaining -= int64(len(p))
	return w.buffer.Write(p)
}

func run(args []string, out io.Writer) error {
	o, err := parse(args, out)
	if err != nil || len(args) == 0 || o.endpoint == "" {
		return err
	}
	selected, err := bootstrap.SelectDevelopment(o.endpoint, o.runtime)
	if err != nil {
		return errors.New("selected runtime identity unavailable")
	}
	machine := client.NewVerified(selected.Endpoint, selected.Server)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	jobs, err := machine.ResolveJobs(ctx, facade.Requirements{Scope: facade.ScopeLocal, Guarantees: guarantees()})
	if err != nil {
		return errors.New("selected runtime cannot serve scoped credential downloads")
	}
	if o.history {
		window, err := jobs.GetHistoryWindow(ctx)
		if err != nil {
			return errors.New("history unavailable")
		}
		returnValue := struct {
			Epoch string `json:"epoch"`
		}{window.HistoryEpoch}
		_, err = fmt.Fprintf(out, "%s\n", returnValue.Epoch)
		return err
	}
	return download(ctx, jobs, o, out)
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
