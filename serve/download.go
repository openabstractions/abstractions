package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"

	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"github.com/openabstractions/abstraction-facade/go/client"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

const downloadUsage = `Usage: openabstractions download <url> [options]

Submits an HTTP(S) transfer to the runtime's job service and copies the result
out. The runtime owns the transfer, the partial file and the retry:
this command sends no path and keeps no store. Closing it leaves the work
running under abstraction.job/caller-exit@1.

  --sha256 HEX     the artifact's SHA-256; the runtime verifies the bytes
                   against it before the operation is complete
  --size BYTES     the artifact's expected size; oversize ends the work
  --credential NAME
                   a credential registered with openabstractions credentials
                   add; the runtime applies it to each request it sends and
                   this command never sees the secret
  --unmetered      move bytes only while the runtime's network is unmetered.
                   The runtime waits while the connection is metered and
                   resumes with Range afterwards; jobs list shows
                   "(waiting: network:metered)". A runtime with no network
                   cost source on its platform is not resolved
                   (abstraction.download/network-cost@1)
  --out PATH       directory or file the result is copied into (default: .)
  --label TEXT     what jobs list and the Panel show for this work, one line of
                   at most 256 bytes. It is your own text, stored as you give it
                   and shown to whoever lists this account's work. Without it,
                   the runtime shows the URL's host and last path segment. The
                   request key and the work it names stay the same whatever the
                   label (JOB-A12)
  --key KEY        request key; the default is derived from the submission, so
                   repeating the command returns the latest attempt's receipt
                   (JOB-A2)
  --retry          submit the next attempt of the same request. The runtime
                   accepts it only after the latest attempt failed or was
                   definitely not accepted (JOB-A7); nothing retries on its own
  --no-wait        print the receipt and return while the work continues
  --quiet          print no progress
  --endpoint EP    runtime bootstrap endpoint (default: the installed runtime)
  --timeout D      waiting budget; zero waits without a deadline
  --json           emit one JSON document on stdout

Exit codes: 0 done, 1 runtime not resolved or output error, 2 usage,
3 typed refusal, 4 unavailable (repeat later), 5 the work failed or was
cancelled, 6 acceptance uncertain (rerun the same command to reconcile),
7 still waiting, 130 interrupted while waiting.
`

// downloadReport is the --json document.
type downloadReport struct {
	URL      string        `json:"url"`
	Receipt  *jsonReceipt  `json:"receipt,omitempty"`
	Snapshot *jsonSnapshot `json:"snapshot,omitempty"`
	Out      string        `json:"out,omitempty"`
	Bytes    int64         `json:"bytes,omitempty"`
}

func downloadCommand(args []string, output, diagnostics io.Writer) error {
	const command = "download"
	if len(args) == 0 || isHelp(args[0]) {
		_, err := io.WriteString(output, downloadUsage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		if _, err := fmt.Fprint(diagnostics, downloadUsage); err == nil {
			flags.PrintDefaults()
		}
	}
	var options serviceOptions
	options.bind(flags)
	digest := flags.String("sha256", "", "the artifact's SHA-256 as 64 hex characters")
	size := flags.Int64("size", 0, "the artifact's expected size in bytes")
	credential := flags.String("credential", "", "name of a registered credential the runtime applies")
	unmetered := flags.Bool("unmetered", false, "move bytes only while the runtime's network is unmetered")
	out := flags.String("out", ".", "directory or file the result is copied into")
	label := flags.String("label", "", "display label: your own text, shown wherever this work is listed")
	key := flags.String("key", "", "request key; the default is derived from the submission")
	retry := flags.Bool("retry", false, "submit the next attempt after the latest attempt failed")
	noWait := flags.Bool("no-wait", false, "return after the receipt while the work continues")
	flags.BoolVar(&options.quiet, "quiet", false, "print no progress")
	positional, err := parsePositional(flags, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &exitError{exitUsage, fmt.Errorf("%s: %w", command, err)}
	}
	if len(positional) != 1 {
		return &exitError{exitUsage, errors.New("download: name exactly one URL")}
	}
	locator := positional[0]
	source, name, err := downloadSource(locator)
	if err != nil {
		return err
	}
	artifact, err := downloadArtifact(*digest, *size)
	if err != nil {
		return err
	}
	required := slices.Clone(api.AdmissionGuarantees)
	if *credential != "" {
		if !validCredentialName(*credential) {
			return &exitError{exitUsage, fmt.Errorf("download: --credential %q must be 1-64 characters from A-Z, a-z, 0-9, _, - and .", *credential)}
		}
		source.Credential = *credential
		required = append(required, request.CredentialGuarantees...)
	}
	var constraints *request.Constraints
	if *unmetered {
		constraints = &request.Constraints{Network: request.NetworkUnmetered}
		required = append(required, request.NetworkCostGuarantees...)
	}
	if options.budget < 0 {
		return &exitError{exitUsage, errors.New("download: --timeout must not be negative")}
	}
	text, err := api.NormalizeLabel(*label)
	if err != nil {
		return &exitError{exitUsage, fmt.Errorf("download: --label: %w", err)}
	}
	// The submission is built and validated before anything is sent, so a usage
	// refusal never leaves an identity behind. The label is outside the request
	// key: relabelling the same download finds the same work (JOB-A2).
	payload := request.Encode(&request.Request{Artifact: artifact, Sources: []request.Source{source}, Constraints: constraints})
	submission := api.Submission{Kind: download.Kind, Spec: payload, RequiredGuarantees: required, Label: text}
	w := newWaiting(options.budget)
	defer w.stop()
	machine := options.machine()
	// Resolution names every guarantee the submission requires, so a runtime
	// that cannot keep one is refused as unmet before an identity exists.
	jobs, err := resolveAcceptanceRequiring(w, command, machine, required)
	if err != nil {
		return err
	}
	call, done := w.call()
	window, err := jobs.GetHistoryWindow(call)
	done()
	if err != nil {
		return notResolved(command, err)
	}
	submission.Identity = api.RequestIdentity{HistoryEpoch: window.HistoryEpoch, Key: *key}
	if submission.Identity.Key == "" {
		submission.Identity.Key = submissionKey(submission)
	}
	// Observation and the result read are the operations contract, bound
	// separately from the acceptance contract that takes the submission.
	operations, err := resolveOperations(w, command, machine)
	if err != nil {
		return err
	}
	// The command keeps no state between runs, so it asks the runtime which
	// attempt of this request is the latest. Without --retry it presents that
	// attempt again and receives its receipt; with --retry it presents the next
	// one, and the runtime judges whether that attempt is eligible (JOB-A7).
	latest, err := latestAttempt(w, command, operations, submission.Identity)
	if err != nil {
		return err
	}
	submission.Identity.Attempt = latest.attempt
	if *retry && latest.found {
		submission.Identity.Attempt++
	}
	receipt, err := submit(w, command, jobs, submission)
	if err != nil {
		return retryAdvice(err, locator, *retry, latest)
	}
	report := downloadReport{URL: locator, Receipt: &receipt}
	if !options.quiet && !options.asJSON {
		if err := printReceipt(output, locator, receipt); err != nil {
			return err
		}
	}
	if *noWait {
		if options.asJSON {
			return writeJSON(output, report)
		}
		return nil
	}
	progress := func(*api.OperationSnapshot) error { return nil }
	if !options.quiet && !options.asJSON {
		progress = func(s *api.OperationSnapshot) error {
			waiting := ""
			if word := describeWaiting(s); word != "" {
				waiting = "  waiting: " + word
			}
			_, err := fmt.Fprintf(output, "%-10s %s / %s%s\n", s.State, humanBytes(s.Progress.Done), humanBytes(s.Progress.Total), waiting)
			return err
		}
	}
	snapshot, err := follow(w, command, operations, submission.Identity, progress)
	if snapshot != nil {
		report.Snapshot = projectSnapshot(snapshot)
	}
	if err != nil {
		return err
	}
	if err := terminalExit(command, snapshot); err != nil {
		if snapshot.State == api.WorkStateFailed {
			// A failed attempt stays failed; the next one is the caller's decision.
			return &exitError{exitEnded, fmt.Errorf("%w; openabstractions download %s --retry submits attempt %d", err, locator, snapshot.Receipt.Identity.Attempt+1)}
		}
		return err
	}
	sink, err := sinkPath(command, *out, name)
	if err != nil {
		return err
	}
	// The runtime already verified the bytes it holds. This guards the local
	// copy: a partial that does not hash to the artifact is never renamed over
	// the destination.
	var verify func(string) error
	if *digest != "" {
		verify = func(partial string) error { return verifyDigest(command, partial, *digest) }
	}
	written, err := copyResult(w, command, operations, submission.Identity, sink, verify)
	if err != nil {
		return err
	}
	report.Out, report.Bytes = sink, written
	if options.asJSON {
		return writeJSON(output, report)
	}
	verified := ""
	if *digest != "" {
		verified = ", sha256 verified"
	}
	_, err = fmt.Fprintf(output, "complete   %s%s\n  %s\n", humanBytes(written), verified, sink)
	return err
}

// resolveAcceptanceRequiring binds the submission contract with the admission
// guarantees and every execution guarantee the submission requires.
func resolveAcceptanceRequiring(w *waiting, command string, machine *client.Machine, required []string) (*client.JobsClient, error) {
	call, done := w.call()
	defer done()
	jobs, err := machine.ResolveJobs(call, client.Requirements{Guarantees: required})
	if err != nil {
		return nil, notResolved(command, err)
	}
	return jobs, nil
}

// downloadSource validates the URL against what the runtime's execution profile
// accepts: anonymous HTTP(S) with a host. Anything else is refused here, before
// an identity exists, rather than sealed as definitely_not_accepted.
func downloadSource(raw string) (request.Source, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return request.Source{}, "", &exitError{exitUsage, fmt.Errorf("download: %q is not a URL", raw)}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return request.Source{}, "", &exitError{exitUsage, fmt.Errorf("download: %q is not an http:// or https:// URL", raw)}
	}
	if parsed.Host == "" {
		return request.Source{}, "", &exitError{exitUsage, fmt.Errorf("download: %q names no host", raw)}
	}
	if parsed.User != nil {
		return request.Source{}, "", &exitError{exitUsage, errors.New("download: a URL carrying credentials is refused; register the secret with openabstractions credentials add and pass --credential NAME")}
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" || name == "" {
		name = "download.bin"
	}
	return request.Source{Scheme: parsed.Scheme, Locator: raw}, name, nil
}

func downloadArtifact(digest string, size int64) (request.Artifact, error) {
	artifact := request.Artifact{Size: size}
	if size < 0 {
		return artifact, &exitError{exitUsage, errors.New("download: --size must not be negative")}
	}
	if digest == "" {
		return artifact, nil
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(decoded) != 32 {
		return artifact, &exitError{exitUsage, errors.New("download: --sha256 takes 64 hex characters")}
	}
	artifact.Digest = "sha256:" + strings.ToLower(strings.TrimPrefix(digest, "sha256:"))
	return artifact, nil
}

// submissionKey derives the default request key from the submission itself: the
// kind, the exact encoded payload and the sorted guarantee names. The encoding
// is deterministic, so the same command line names the same identity and a
// second run returns the original receipt (JOB-A2).
func submissionKey(s api.Submission) string {
	guarantees := slices.Clone(s.RequiredGuarantees)
	slices.Sort(guarantees)
	sum := sha256.New()
	sum.Write([]byte(s.Kind))
	sum.Write([]byte{0})
	sum.Write(s.Spec)
	sum.Write([]byte{0})
	for _, g := range guarantees {
		sum.Write([]byte(g))
		sum.Write([]byte{0})
	}
	return "download-" + hex.EncodeToString(sum.Sum(nil))[:32]
}

// submit sends the submission once. A transport failure is not a negative
// (JOB-A4): the same identity is reconciled within the call budget, and a fresh
// identity is never substituted for one whose fate is unknown.
func submit(w *waiting, command string, jobs *client.JobsClient, submission api.Submission) (jsonReceipt, error) {
	call, done := w.call()
	result, err := jobs.Submit(call, submission)
	done()
	if err != nil {
		call, done := w.call()
		result, err = jobs.Reconcile(call, submission.Identity)
		done()
		if err != nil {
			return jsonReceipt{}, &exitError{exitUncertain,
				fmt.Errorf("%s: unknown: acceptance of %s is uncertain; rerun the same command to reconcile it: %w", command, submission.Identity.Key, err)}
		}
	}
	switch result.Outcome {
	case api.AcceptanceOutcomeAccepted:
		return projectReceipt(*result.Receipt), nil
	case api.AcceptanceOutcomeUnknown:
		return jsonReceipt{}, &exitError{exitUncertain,
			fmt.Errorf("%s: unknown: acceptance of %s is uncertain; rerun the same command to reconcile it: %s", command, submission.Identity.Key, result.Reason)}
	}
	return jsonReceipt{}, refusal(command, result.Outcome.String(), result.Reason)
}

// retryAdvice adds what the latest attempt was to a refusal of the attempt the
// command presented, so a person can tell why a retry was ineligible or that a
// sealed request needs one.
func retryAdvice(err error, locator string, retry bool, latest attemptEvidence) error {
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitRefusedCall || !latest.found {
		return err
	}
	if retry {
		return &exitError{exit.code, fmt.Errorf("%w (%s)", err, latest.describe())}
	}
	if latest.sealed {
		return &exitError{exit.code, fmt.Errorf("%w; openabstractions download %s --retry submits attempt %d", err, locator, latest.attempt+1)}
	}
	return err
}

func printReceipt(output io.Writer, source string, receipt jsonReceipt) error {
	short := make([]string, 0, len(receipt.AcceptedGuarantees))
	for _, g := range receipt.AcceptedGuarantees {
		short = append(short, strings.TrimSuffix(strings.TrimPrefix(g, "abstraction.job/"), "@1"))
	}
	id := api.RequestIdentity{Key: receipt.Identity.Key, HistoryEpoch: receipt.Identity.HistoryEpoch, Attempt: receipt.Identity.Attempt}
	_, err := fmt.Fprintf(output, "%s\n  job        %s\n  identity   %s\n  owner      %s\n  guarantees %s\n",
		source, receipt.OperationID, describeIdentity(id), receipt.LogicalOwner, strings.Join(short, ", "))
	return err
}

// verifyDigest hashes what was actually written before it replaces anything.
func verifyDigest(command, partial, want string) error {
	file, err := os.Open(partial)
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != strings.ToLower(strings.TrimPrefix(want, "sha256:")) {
		return &exitError{exitNotResolved, fmt.Errorf("%s: the copied bytes hash to sha256:%s, not the artifact that was asked for", command, got)}
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exponent := float64(n)/unit, 0
	for value >= unit && exponent < 4 {
		value /= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGTP"[exponent])
}
