package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// Exit codes of `openabstractions download` and the service-backed `jobs`
// commands. `jobs migrate-legacy` keeps the separate codes declared beside it.
const (
	exitNotResolved = 1   // no runtime resolved, transport failure, inventory gap twice, output error
	exitRefusedCall = 3   // forbidden, invalid, key_conflict, definitely_not_accepted, unsupported, absent id
	exitUnavailable = 4   // unavailable: no effect was established; repeat the same command later
	exitEnded       = 5   // work ended failed or cancelled
	exitUncertain   = 6   // unknown: acceptance or observation is uncertain
	exitWaiting     = 7   // waiting budget expired, or the result is not ready
	exitInterrupted = 130 // interrupted while waiting; the work continues
)

// callBudget bounds one service call. The waiting budget is the caller's
// --timeout and is independent of it.
const callBudget = 30 * time.Second

// pollInterval is how often a wait observes an operation.
const pollInterval = 500 * time.Millisecond

// inventoryPage is the page size for ListWork traversals.
const inventoryPage = 64

// maxAttemptProbe bounds the search for a key's latest attempt. Every attempt is
// an explicit caller retry, so a key with more is refused rather than probed
// without end.
const maxAttemptProbe = 4096

const jobsServiceUsage = `Usage: openabstractions jobs <command>

Commands:
  list                       what this program submitted in this account
  list --all                 what every program submitted in this account;
                             needs the rule abstraction.job/inventory.read
  show <id> | --key K        observe one operation once
  wait <id> | --key K        follow one operation until it ends
  cancel <id> | --key K      record the intent to cancel; JOB-A6 intent, not proof
  cancel --all <id>          cancel another program's operation by its id;
                             needs the rule abstraction.job/acceptance.cancel
  result <id> --out FILE     copy a complete result out of the runtime
  migrate-legacy             convert legacy job records with an operator mapping

Every command accepts --endpoint, --timeout and --json. An operation is named
by its operation id, or by the request identity --key K [--epoch E]
[--attempt N] the submitting command printed. Without --attempt, --key names
the latest attempt of that key (JOB-A7). Scope is this program in this account:
work an application submitted through its own binding is observed by that
application.

list and show print each operation's label: the submitting program's own text,
or one the runtime derived, such as a download's source host and last path
segment, marked "(derived)". A label is display text for a person (JOB-A12).
Unfinished work a condition holds shows why beside it, such as
"(waiting: network:metered)" for a download waiting for an unmetered network
(JOB-A15); --json carries the word as "waiting".

result writes the file --out names. The runtime keeps no file name for a
result, so a directory is refused rather than given an invented name.

Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal, 4 unavailable (repeat later), 5 work failed or was cancelled,
6 acceptance or observation uncertain, 7 still waiting, 130 interrupted.

Run "openabstractions jobs migrate-legacy" for its own commands and codes.
`

// serviceOptions are the flags every service-backed command shares with status.
type serviceOptions struct {
	endpoint string
	budget   time.Duration
	asJSON   bool
	quiet    bool
}

func (o *serviceOptions) bind(flags *flag.FlagSet) {
	flags.StringVar(&o.endpoint, "endpoint", "", "runtime bootstrap endpoint (default: the installed runtime)")
	flags.DurationVar(&o.budget, "timeout", 0, "total waiting budget; zero waits without a deadline")
	flags.BoolVar(&o.asJSON, "json", false, "emit one JSON document on stdout")
}

// machine resolves through installation evidence, the trust path `status` and
// `start` use. An explicit endpoint is a deliberately supplied provider. No
// command reads a store directory or falls back to a local file store.
func (o serviceOptions) machine() *client.Machine {
	if o.endpoint != "" {
		return client.New(o.endpoint)
	}
	return client.Discover()
}

func admission() client.Requirements {
	return client.Requirements{Guarantees: api.AdmissionGuarantees}
}

// notResolved reports a binding failure as the runtime not being resolvable.
// The resolver's typed status is preserved for the person reading it.
func notResolved(command string, err error) error {
	var refusal *client.ResolutionError
	if errors.As(err, &refusal) {
		return &exitError{exitNotResolved, fmt.Errorf("%s: no runtime resolved: %s", command, refusal.Status)}
	}
	return &exitError{exitNotResolved, fmt.Errorf("%s: no runtime resolved: %w", command, err)}
}

// refusal maps a contract outcome word to its exit code, printing the word as
// the contract spells it.
func refusal(command, outcome, reason string) error {
	code := exitRefusedCall
	switch outcome {
	case "unavailable":
		code = exitUnavailable
	case "unknown":
		code = exitUncertain
	case "not_ready":
		code = exitWaiting
	case "gap":
		code = exitNotResolved
	}
	if reason == "" {
		return &exitError{code, fmt.Errorf("%s: %s", command, outcome)}
	}
	return &exitError{code, fmt.Errorf("%s: %s: %s", command, outcome, reason)}
}

// waiting carries a bounded wait's own budget alongside the interrupt that
// stops waiting without stopping the work.
type waiting struct {
	ctx      context.Context
	signaled context.Context
	stop     func()
}

// newWaiting bounds a command. Interrupting it ends the wait, never the work:
// the runtime holds `abstraction.job/caller-exit@1`.
func newWaiting(budget time.Duration) *waiting {
	signaled, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	ctx, cancel := signaled, func() {}
	if budget > 0 {
		ctx, cancel = context.WithTimeout(signaled, budget)
	}
	return &waiting{ctx: ctx, signaled: signaled, stop: func() { cancel(); stopSignals() }}
}

// call bounds one service call inside the waiting budget.
func (w *waiting) call() (context.Context, func()) {
	budget := callBudget
	if deadline, ok := w.ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		budget = time.Millisecond
	}
	return context.WithTimeout(w.ctx, budget)
}

// expired names why a wait ended without the work ending.
func (w *waiting) expired(command, id string) error {
	if w.signaled.Err() != nil {
		return &exitError{exitInterrupted, fmt.Errorf("%s: interrupted; the work continues: openabstractions jobs wait %s", command, id)}
	}
	return &exitError{exitWaiting, fmt.Errorf("%s: still running: openabstractions jobs wait %s", command, id)}
}

// sleep waits one poll interval or until the budget ends.
func (w *waiting) sleep() bool {
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()
	select {
	case <-w.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// JSON projections. The generated contract types carry no JSON names, and the
// documented command output uses the contract's own field spelling.
type jsonIdentity struct {
	Key          string `json:"key"`
	HistoryEpoch string `json:"history_epoch"`
	// Attempt numbers explicit retries of one key; zero is the original request (JOB-A7).
	Attempt int64 `json:"attempt"`
}
type jsonReceipt struct {
	Identity           jsonIdentity `json:"identity"`
	LogicalOwner       string       `json:"logical_owner"`
	OperationID        string       `json:"operation_id"`
	AcceptedGuarantees []string     `json:"accepted_guarantees"`
	HistoryRetentionMs int64        `json:"history_retention_ms"`
}
type jsonProgress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}
type jsonFailure struct {
	Classification string `json:"classification"`
	Message        string `json:"message"`
	// The typed cause the contract words, e.g. digest_mismatch. It crosses the
	// wire and is recorded, so a reader that drops it loses why the work ended.
	Cause string `json:"cause,omitempty"`
}
type jsonSnapshot struct {
	Receipt               jsonReceipt  `json:"receipt"`
	State                 string       `json:"state"`
	Progress              jsonProgress `json:"progress"`
	CancellationRequested bool         `json:"cancellation_requested"`
	Failure               *jsonFailure `json:"failure,omitempty"`
	// Label is the operation's display label, the caller's or one the runtime
	// derived when label_derived is true (JOB-A12). Absent when neither exists.
	Label        string `json:"label,omitempty"`
	LabelDerived bool   `json:"label_derived,omitempty"`
	// Waiting is the word naming what holds unfinished work, such as
	// network:metered (JOB-A15). Absent when nothing holds it.
	Waiting string `json:"waiting,omitempty"`
}

func projectReceipt(r api.Receipt) jsonReceipt {
	guarantees := r.AcceptedGuarantees
	if guarantees == nil {
		guarantees = []string{}
	}
	return jsonReceipt{Identity: jsonIdentity{Key: r.Identity.Key, HistoryEpoch: r.Identity.HistoryEpoch, Attempt: r.Identity.Attempt},
		LogicalOwner: r.LogicalOwner, OperationID: r.OperationID, AcceptedGuarantees: guarantees, HistoryRetentionMs: r.HistoryRetentionMs}
}

func projectSnapshot(s *api.OperationSnapshot) *jsonSnapshot {
	if s == nil {
		return nil
	}
	out := &jsonSnapshot{Receipt: projectReceipt(s.Receipt), State: s.State.String(),
		Progress: jsonProgress{Done: s.Progress.Done, Total: s.Progress.Total}, CancellationRequested: s.CancellationRequested,
		Label: s.Label, LabelDerived: s.LabelDerived && s.Label != "", Waiting: describeWaiting(s)}
	if s.Failure != nil {
		out.Failure = &jsonFailure{Classification: s.Failure.Classification.String(), Message: s.Failure.Message, Cause: string(s.Failure.Cause)}
	}
	return out
}

func writeJSON(output io.Writer, document any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(document)
}

// describeFailure is the one-line form of a snapshot's failure, for tables. The
// typed cause leads when there is one: it is the contract's own word for why the
// work ended, and the message only explains it.
func describeFailure(s *api.OperationSnapshot) string {
	if s == nil || s.Failure == nil {
		return "-"
	}
	if s.Failure.Cause != "" {
		return s.Failure.Classification.String() + "/" + string(s.Failure.Cause) + ": " + s.Failure.Message
	}
	return s.Failure.Classification.String() + "/" + s.Failure.Message
}

// describeLabel is the human form of a snapshot's display label (JOB-A12). A
// derived label ends in "(derived)". A label outside the contract's limits,
// which a conforming runtime never sends, is shown as "-" so it cannot reshape
// the terminal output.
func describeLabel(s *api.OperationSnapshot) string {
	text, err := api.NormalizeLabel(s.Label)
	if err != nil || text == "" {
		return "-"
	}
	if s.LabelDerived {
		return text + " (derived)"
	}
	return text
}

// describeWaiting is the snapshot's waiting word (JOB-A15), or "" when nothing
// holds the work. A word outside the contract's alphabet, which a conforming
// runtime never sends, reads as an unnamed wait so it cannot reshape output.
func describeWaiting(s *api.OperationSnapshot) string {
	word := s.Waiting
	if word == "" {
		return ""
	}
	if len(word) > 64 || strings.IndexFunc(word, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("_-.:", c))
	}) >= 0 {
		return "unnamed"
	}
	return word
}

// labelWithWaiting is the label cell of `jobs list`: the label, then what
// holds the work when something does.
func labelWithWaiting(s *api.OperationSnapshot) string {
	if word := describeWaiting(s); word != "" {
		return describeLabel(s) + " (waiting: " + word + ")"
	}
	return describeLabel(s)
}

// describeIdentity is the human form of a request identity.
func describeIdentity(id api.RequestIdentity) string {
	return fmt.Sprintf("%s @ %s attempt %d", id.Key, id.HistoryEpoch, id.Attempt)
}

// printSnapshot is the human form of one observed operation.
func printSnapshot(output io.Writer, s *api.OperationSnapshot) error {
	if _, err := fmt.Fprintf(output, "%s\n  job        %s\n  label      %s\n  identity   %s\n  owner      %s\n  progress   %d / %d\n",
		s.State, s.Receipt.OperationID, describeLabel(s), describeIdentity(s.Receipt.Identity), s.Receipt.LogicalOwner,
		s.Progress.Done, s.Progress.Total); err != nil {
		return err
	}
	if word := describeWaiting(s); word != "" {
		if _, err := fmt.Fprintf(output, "  waiting    %s\n", word); err != nil {
			return err
		}
	}
	if s.CancellationRequested {
		if _, err := fmt.Fprintln(output, "  cancel     requested"); err != nil {
			return err
		}
	}
	if s.Failure != nil {
		if _, err := fmt.Fprintf(output, "  failure    %s\n", describeFailure(s)); err != nil {
			return err
		}
	}
	return nil
}

// terminalExit maps an ended state onto its exit code. A live state returns nil.
func terminalExit(command string, s *api.OperationSnapshot) error {
	switch s.State {
	case api.WorkStateComplete:
		return nil
	case api.WorkStateFailed, api.WorkStateCancelled:
		if s.Failure == nil {
			return &exitError{exitEnded, fmt.Errorf("%s: %s", command, s.State)}
		}
		return &exitError{exitEnded, fmt.Errorf("%s: %s: %s", command, s.State, describeFailure(s))}
	}
	return nil
}

// parsePositional accepts flags anywhere among positional arguments, the way
// the commands these replace accepted them. Go's flag package stops at the
// first positional, which would refuse `download <url> --out DIR`.
func parsePositional(flags *flag.FlagSet, args []string) ([]string, error) {
	positional := []string{}
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			return nil, err
		}
		if flags.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
}

// jobSelector is how a person names one operation: the operation id the
// submitting command printed, or the request identity itself.
type jobSelector struct {
	id         string
	key        string
	epoch      string
	attempt    int64
	attemptSet bool
}

func (s *jobSelector) bind(flags *flag.FlagSet) {
	flags.StringVar(&s.key, "key", "", "request key of the operation (skips the inventory scan)")
	flags.StringVar(&s.epoch, "epoch", "", "history epoch of --key (default: the current epoch)")
	flags.Int64Var(&s.attempt, "attempt", 0, "attempt of --key (default: the latest attempt)")
}

func (s *jobSelector) parse(command string, flags *flag.FlagSet, positional []string) error {
	flags.Visit(func(f *flag.Flag) { s.attemptSet = s.attemptSet || f.Name == "attempt" })
	switch len(positional) {
	case 0:
		if s.key == "" {
			return &exitError{exitUsage, fmt.Errorf("%s: name an operation id, or --key K", command)}
		}
	case 1:
		if s.key != "" {
			return &exitError{exitUsage, fmt.Errorf("%s: an operation id and --key name two different things", command)}
		}
		s.id = positional[0]
		if s.id == "" {
			return &exitError{exitUsage, fmt.Errorf("%s: empty operation id", command)}
		}
	default:
		return &exitError{exitUsage, fmt.Errorf("%s: unexpected arguments", command)}
	}
	if s.key == "" && s.epoch != "" {
		return &exitError{exitUsage, fmt.Errorf("%s: --epoch names the epoch of --key", command)}
	}
	if s.key == "" && s.attemptSet {
		return &exitError{exitUsage, fmt.Errorf("%s: --attempt names an attempt of --key", command)}
	}
	if s.attempt < 0 {
		return &exitError{exitUsage, fmt.Errorf("%s: --attempt must not be negative", command)}
	}
	return nil
}

// resolveIdentity turns the selector into the request identity the operations
// contract needs. An operation id is found by traversing this caller's own
// inventory; an id outside this scope is a refusal, never a silent empty result.
// A key without --attempt names its latest attempt.
func resolveIdentity(w *waiting, command string, machine *client.Machine, jobs *client.JobsClient, selector jobSelector) (api.RequestIdentity, error) {
	if selector.key != "" {
		id := api.RequestIdentity{Key: selector.key, HistoryEpoch: selector.epoch, Attempt: selector.attempt}
		if id.HistoryEpoch == "" {
			call, done := w.call()
			window, err := jobs.GetHistoryWindow(call)
			done()
			if err != nil {
				return api.RequestIdentity{}, notResolved(command, err)
			}
			id.HistoryEpoch = window.HistoryEpoch
		}
		if !selector.attemptSet {
			latest, err := latestAttempt(w, command, jobs, id)
			if err != nil {
				return api.RequestIdentity{}, err
			}
			id.Attempt = latest.attempt
		}
		return id, nil
	}
	snapshots, err := listScope(w, command, machine)
	if err != nil {
		return api.RequestIdentity{}, err
	}
	for i := range snapshots {
		if snapshots[i].Receipt.OperationID == selector.id {
			return snapshots[i].Receipt.Identity, nil
		}
	}
	return api.RequestIdentity{}, &exitError{exitRefusedCall,
		fmt.Errorf("%s: invalid: %s names no work this program submitted in this account", command, selector.id)}
}

func resolveOperations(w *waiting, command string, machine *client.Machine) (*client.JobsClient, error) {
	call, done := w.call()
	defer done()
	jobs, err := machine.ResolveJobOperations(call, admission())
	if err != nil {
		return nil, notResolved(command, err)
	}
	return jobs, nil
}

// resolveAcceptance binds the submission contract. The command exits while the
// work continues, so it requires every admission guarantee; a receipt that
// weakens one is refused by the binding rather than accepted quietly.
func resolveAcceptance(w *waiting, command string, machine *client.Machine) (*client.JobsClient, error) {
	call, done := w.call()
	defer done()
	jobs, err := machine.ResolveJobs(call, admission())
	if err != nil {
		return nil, notResolved(command, err)
	}
	return jobs, nil
}

// listScope traverses this caller's inventory completely. A gap restarts the
// traversal once; a second gap is a failure, never a partial list presented as
// the whole.
func listScope(w *waiting, command string, machine *client.Machine) ([]api.OperationSnapshot, error) {
	call, done := w.call()
	inventory, err := machine.ResolveJobInventory(call, admission())
	done()
	if err != nil {
		return nil, notResolved(command, err)
	}
	return listAll(w, command, inventory.ListWork)
}

// listAccount traverses every program's work in this account through the job
// operator profile, which the runtime decides by the inventory.read rule.
func listAccount(w *waiting, command string, machine *client.Machine) ([]api.OperationSnapshot, error) {
	call, done := w.call()
	operator, err := machine.ResolveJobOperator(call, admission())
	done()
	if err != nil {
		return nil, notResolved(command, err)
	}
	return listAll(w, command, operator.ListAccountWork)
}

type pageReader func(ctx context.Context, cursor string, limit int64) (api.InventoryPage, error)

func listAll(w *waiting, command string, inventory pageReader) ([]api.OperationSnapshot, error) {
	for attempt := 0; attempt < 2; attempt++ {
		snapshots, restart, err := traverse(w, command, inventory)
		if err != nil {
			return nil, err
		}
		if !restart {
			return snapshots, nil
		}
	}
	return nil, &exitError{exitNotResolved, fmt.Errorf("%s: gap: the inventory changed under two complete traversals", command)}
}

func traverse(w *waiting, command string, inventory pageReader) ([]api.OperationSnapshot, bool, error) {
	var snapshots []api.OperationSnapshot
	seen := map[string]bool{}
	cursor := ""
	for {
		call, done := w.call()
		page, err := inventory(call, cursor, inventoryPage)
		done()
		if err != nil {
			return nil, false, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		switch page.Outcome {
		case api.InventoryOutcomePage:
		case api.InventoryOutcomeGap:
			return nil, true, nil
		default:
			return nil, false, refusal(command, page.Outcome.String(), "")
		}
		for i := range page.Snapshots {
			if id := page.Snapshots[i].Receipt.OperationID; !seen[id] {
				seen[id] = true
				snapshots = append(snapshots, page.Snapshots[i])
			}
		}
		if page.Complete {
			return snapshots, false, nil
		}
		cursor = page.Next
	}
}

// observe reads one operation's current state. Observation seals nothing, so it
// is safe to repeat.
func observe(w *waiting, command string, jobs *client.JobsClient, id api.RequestIdentity) (*api.OperationSnapshot, error) {
	call, done := w.call()
	result, err := jobs.ObserveWork(call, id)
	done()
	if err != nil {
		if w.ctx.Err() != nil {
			return nil, w.expired(command, id.Key)
		}
		return nil, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	if result.Outcome != api.ObservationOutcomeObserved {
		return nil, refusal(command, result.Outcome.String(), "the runtime holds no observable acceptance for this identity")
	}
	return result.Snapshot, nil
}

// attemptEvidence is what the runtime holds for the newest attempt of a key.
type attemptEvidence struct {
	found    bool
	attempt  int64
	sealed   bool                   // definitely_not_accepted
	snapshot *api.OperationSnapshot // present when the attempt was accepted
}

// describe names the attempt's state for a person deciding whether to retry.
func (e attemptEvidence) describe() string {
	switch {
	case !e.found:
		return "no attempt exists"
	case e.sealed:
		return fmt.Sprintf("attempt %d was definitely not accepted", e.attempt)
	}
	return fmt.Sprintf("attempt %d is %s", e.attempt, e.snapshot.State)
}

// latestAttempt finds the newest attempt of id's key and epoch the runtime holds
// evidence for (JOB-A7), starting at attempt zero. ObserveWork never seals an
// identity, so probing an absent attempt leaves it presentable; Reconcile would
// seal it and is never used here. The provider stays the judge of eligibility:
// evidence read here only chooses which attempt to present.
func latestAttempt(w *waiting, command string, jobs *client.JobsClient, id api.RequestIdentity) (attemptEvidence, error) {
	var latest attemptEvidence
	for n := int64(0); n < maxAttemptProbe; n++ {
		id.Attempt = n
		call, done := w.call()
		result, err := jobs.ObserveWork(call, id)
		done()
		if err != nil {
			if w.ctx.Err() != nil {
				return latest, w.expired(command, id.Key)
			}
			return latest, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		switch result.Outcome {
		case api.ObservationOutcomeObserved:
			latest = attemptEvidence{found: true, attempt: n, snapshot: result.Snapshot}
		case api.ObservationOutcomeDefinitelyNotAccepted:
			latest = attemptEvidence{found: true, attempt: n, sealed: true}
		case api.ObservationOutcomeUnknown:
			return latest, nil
		default:
			return latest, refusal(command, result.Outcome.String(), fmt.Sprintf("observing attempt %d of %s", n, id.Key))
		}
	}
	return latest, &exitError{exitNotResolved, fmt.Errorf("%s: %s has more than %d attempts", command, id.Key, maxAttemptProbe)}
}

// follow observes until the work ends or the waiting budget does. Reporting
// progress is the caller's, through report.
func follow(w *waiting, command string, jobs *client.JobsClient, id api.RequestIdentity, report func(*api.OperationSnapshot) error) (*api.OperationSnapshot, error) {
	var last *api.OperationSnapshot
	for {
		snapshot, err := observe(w, command, jobs, id)
		if err != nil {
			return last, err
		}
		if report != nil && (last == nil || last.State != snapshot.State || last.Progress != snapshot.Progress) {
			if err := report(snapshot); err != nil {
				return snapshot, err
			}
		}
		last = snapshot
		switch snapshot.State {
		case api.WorkStateComplete, api.WorkStateFailed, api.WorkStateCancelled:
			return snapshot, nil
		}
		if !w.sleep() {
			return snapshot, w.expired(command, snapshot.Receipt.OperationID)
		}
	}
}

// copyResult writes a complete result to the chosen sink through ReadResult.
// The runtime owns the transfer; no sink path is ever sent to it.
// verify, when supplied, inspects the partial file before it replaces the sink.
func copyResult(w *waiting, command string, jobs *client.JobsClient, id api.RequestIdentity, sink string, verify func(string) error) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(sink), 0o700); err != nil {
		return 0, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	partial := sink + ".part"
	file, err := os.Create(partial)
	if err != nil {
		return 0, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	call, done := w.call()
	written, copyErr := jobs.CopyResult(call, id, file)
	done()
	closeErr := file.Close()
	if copyErr != nil {
		os.Remove(partial)
		var service *api.ServiceError
		if errors.As(copyErr, &service) {
			return written, resultRefusal(command, string(service.Code))
		}
		return written, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, copyErr)}
	}
	if closeErr != nil {
		os.Remove(partial)
		return written, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, closeErr)}
	}
	if verify != nil {
		if err := verify(partial); err != nil {
			os.Remove(partial)
			return written, err
		}
	}
	if err := os.Rename(partial, sink); err != nil {
		os.Remove(partial)
		return written, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	return written, nil
}

// resultRefusal words a result read's outcome. `unavailable` after a completed
// operation does not establish that the result is gone (JOB-A10).
func resultRefusal(command, outcome string) error {
	switch outcome {
	case api.ResultOutcomeNotReady.String():
		return &exitError{exitWaiting, fmt.Errorf("%s: not_ready: the work has not produced a result yet", command)}
	case api.ResultOutcomeUnavailable.String():
		return &exitError{exitUnavailable, fmt.Errorf("%s: unavailable: the result could not be read; repeat this command later", command)}
	}
	return refusal(command, outcome, "the runtime refused the result read")
}

// jobsServiceCommand dispatches the service-backed subcommands. migrate-legacy
// keeps its own parsing, output and exit codes.
func jobsServiceCommand(sub string, args []string, output, diagnostics io.Writer) error {
	command := "jobs " + sub
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		if _, err := fmt.Fprint(diagnostics, jobsServiceUsage); err == nil {
			flags.PrintDefaults()
		}
	}
	var options serviceOptions
	options.bind(flags)
	var selector jobSelector
	var sink *string
	var all *bool
	if sub != "list" {
		selector.bind(flags)
	}
	if sub == "list" || sub == "cancel" {
		all = flags.Bool("all", false, "every program's work in this account, decided by a rights rule")
	}
	if sub == "result" {
		sink = flags.String("out", "", "directory or file the result is copied into")
	}
	positional, err := parsePositional(flags, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &exitError{exitUsage, fmt.Errorf("%s: %w", command, err)}
	}
	if options.budget < 0 {
		return &exitError{exitUsage, fmt.Errorf("%s: --timeout must not be negative", command)}
	}
	if sub == "list" {
		if len(positional) != 0 {
			return &exitError{exitUsage, fmt.Errorf("%s: unexpected arguments", command)}
		}
		return jobsList(options, *all, output)
	}
	if sub == "cancel" && *all {
		attempt := false
		flags.Visit(func(f *flag.Flag) { attempt = attempt || f.Name == "attempt" })
		if len(positional) != 1 || selector.key != "" || selector.epoch != "" || attempt {
			return &exitError{exitUsage, fmt.Errorf("%s --all: name exactly one operation id; another program's request key means nothing here", command)}
		}
		return jobsCancelOperation(options, positional[0], output)
	}
	if err := selector.parse(command, flags, positional); err != nil {
		return err
	}
	var file string
	if sub == "result" {
		if file, err = resultFile(command, *sink); err != nil {
			return err
		}
	}
	w := newWaiting(options.budget)
	defer w.stop()
	machine := options.machine()
	jobs, err := resolveOperations(w, command, machine)
	if err != nil {
		return err
	}
	id, err := resolveIdentity(w, command, machine, jobs, selector)
	if err != nil {
		return err
	}
	switch sub {
	case "show":
		return jobsShow(w, command, jobs, id, options, output)
	case "wait":
		return jobsWait(w, command, jobs, id, options, output)
	case "cancel":
		return jobsCancel(w, command, jobs, id, options, output)
	default:
		return jobsResult(w, command, jobs, id, file, options, output)
	}
}

func jobsList(options serviceOptions, all bool, output io.Writer) error {
	const command = "jobs list"
	w := newWaiting(options.budget)
	defer w.stop()
	list := listScope
	if all {
		list = listAccount
	}
	snapshots, err := list(w, command, options.machine())
	if err != nil {
		return err
	}
	if options.asJSON {
		document := struct {
			Snapshots []*jsonSnapshot `json:"snapshots"`
		}{Snapshots: []*jsonSnapshot{}}
		for i := range snapshots {
			document.Snapshots = append(document.Snapshots, projectSnapshot(&snapshots[i]))
		}
		return writeJSON(output, document)
	}
	return printSnapshots(output, snapshots)
}

// printSnapshots is the human table of `jobs list`. LABEL names what each
// operation is fetching; a label the runtime derived ends in "(derived)", and
// work held by a condition adds "(waiting: <word>)" (JOB-A15).
func printSnapshots(output io.Writer, snapshots []api.OperationSnapshot) error {
	if len(snapshots) == 0 {
		_, err := fmt.Fprintln(output, "no work submitted by this program in this account.")
		return err
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "OPERATION\tSTATE\tPROGRESS\tATTEMPT\tLABEL\tKEY\tFAILURE"); err != nil {
		return err
	}
	for i := range snapshots {
		s := &snapshots[i]
		if _, err := fmt.Fprintf(table, "%s\t%s\t%d/%d\t%d\t%s\t%s\t%s\n", s.Receipt.OperationID, s.State,
			s.Progress.Done, s.Progress.Total, s.Receipt.Identity.Attempt, labelWithWaiting(s), s.Receipt.Identity.Key, describeFailure(s)); err != nil {
			return err
		}
	}
	return table.Flush()
}

func jobsShow(w *waiting, command string, jobs *client.JobsClient, id api.RequestIdentity, options serviceOptions, output io.Writer) error {
	snapshot, err := observe(w, command, jobs, id)
	if err != nil {
		return err
	}
	if options.asJSON {
		return writeJSON(output, struct {
			Snapshot *jsonSnapshot `json:"snapshot"`
		}{projectSnapshot(snapshot)})
	}
	// show reports what it observed whatever the state; the state is the answer.
	return printSnapshot(output, snapshot)
}

func jobsWait(w *waiting, command string, jobs *client.JobsClient, id api.RequestIdentity, options serviceOptions, output io.Writer) error {
	report := func(s *api.OperationSnapshot) error { return nil }
	if !options.asJSON {
		report = func(s *api.OperationSnapshot) error {
			_, err := fmt.Fprintf(output, "%-10s %d / %d\n", s.State, s.Progress.Done, s.Progress.Total)
			return err
		}
	}
	snapshot, err := follow(w, command, jobs, id, report)
	if err != nil {
		return err
	}
	if options.asJSON {
		if err := writeJSON(output, struct {
			Snapshot *jsonSnapshot `json:"snapshot"`
		}{projectSnapshot(snapshot)}); err != nil {
			return err
		}
	}
	return terminalExit(command, snapshot)
}

// jobsCancelOperation records cancellation intent on any program's operation
// through the job operator profile, which the runtime decides by the
// acceptance.cancel rule (JOB-A13). The submitter observes the result.
func jobsCancelOperation(options serviceOptions, operationID string, output io.Writer) error {
	const command = "jobs cancel --all"
	w := newWaiting(options.budget)
	defer w.stop()
	call, done := w.call()
	operator, err := options.machine().ResolveJobOperator(call, admission())
	done()
	if err != nil {
		return notResolved(command, err)
	}
	call, done = w.call()
	result, err := operator.CancelOperation(call, operationID)
	done()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	if options.asJSON {
		if err := writeJSON(output, struct {
			Operation string `json:"operation"`
			Outcome   string `json:"outcome"`
		}{operationID, result.Outcome.String()}); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(output, "%s %s\n", operationID, result.Outcome); err != nil {
		return err
	}
	switch result.Outcome {
	case api.OperatorCancellationOutcomeRequested, api.OperatorCancellationOutcomeAlreadyTerminal:
		return nil
	}
	return refusal(command, result.Outcome.String(), "the runtime recorded no cancellation intent")
}

// jobsCancel records intent. Completion may win the race, so the command
// observes once afterwards and prints what it found (JOB-A6).
func jobsCancel(w *waiting, command string, jobs *client.JobsClient, id api.RequestIdentity, options serviceOptions, output io.Writer) error {
	call, done := w.call()
	result, err := jobs.CancelWork(call, id)
	done()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	switch result.Outcome {
	case api.CancellationOutcomeRequested, api.CancellationOutcomeAlreadyTerminal:
	default:
		return refusal(command, result.Outcome.String(), "the runtime recorded no cancellation intent")
	}
	snapshot, observeErr := observe(w, command, jobs, id)
	if options.asJSON {
		if err := writeJSON(output, struct {
			Outcome  string        `json:"outcome"`
			Snapshot *jsonSnapshot `json:"snapshot,omitempty"`
		}{result.Outcome.String(), projectSnapshot(snapshot)}); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(output, "%s\n", result.Outcome); err != nil {
			return err
		}
		if snapshot != nil {
			if err := printSnapshot(output, snapshot); err != nil {
				return err
			}
		}
	}
	return observeErr
}

func jobsResult(w *waiting, command string, jobs *client.JobsClient, id api.RequestIdentity, sink string, options serviceOptions, output io.Writer) error {
	snapshot, err := observe(w, command, jobs, id)
	if err != nil {
		return err
	}
	switch snapshot.State {
	case api.WorkStateComplete:
	case api.WorkStateFailed, api.WorkStateCancelled:
		return terminalExit(command, snapshot)
	default:
		return &exitError{exitWaiting, fmt.Errorf("%s: not complete: openabstractions jobs wait %s", command, snapshot.Receipt.OperationID)}
	}
	written, err := copyResult(w, command, jobs, id, sink, nil)
	if err != nil {
		return err
	}
	if options.asJSON {
		return writeJSON(output, struct {
			Snapshot *jsonSnapshot `json:"snapshot"`
			Out      string        `json:"out"`
			Bytes    int64         `json:"bytes"`
		}{projectSnapshot(snapshot), sink, written})
	}
	_, err = fmt.Fprintf(output, "complete   %d bytes\n  %s\n", written, sink)
	return err
}

// resultFile checks that `jobs result --out` names a file. A job result carries
// no file name: the name belongs to whoever binds the bytes to a file, and a
// command that did not submit the work does not know it. A directory is refused
// before anything is sent, rather than filled with an invented name.
func resultFile(command, out string) (string, error) {
	if out == "" {
		return "", &exitError{exitUsage, fmt.Errorf("%s: --out is required", command)}
	}
	if isDirectory(out) {
		return "", &exitError{exitUsage, fmt.Errorf("%s: --out %s is a directory; the runtime keeps no file name for a result, so name the file", command, out)}
	}
	absolute, err := filepath.Abs(out)
	if err != nil {
		return "", &exitError{exitUsage, fmt.Errorf("%s: --out: %w", command, err)}
	}
	return absolute, nil
}

// isDirectory reports whether --out names a directory: ".", "..", a trailing
// separator or an existing directory.
func isDirectory(out string) bool {
	if out == "." || out == ".." || strings.HasSuffix(out, string(os.PathSeparator)) || strings.HasSuffix(out, "/") {
		return true
	}
	info, err := os.Stat(out)
	return err == nil && info.IsDir()
}

// sinkPath turns `download --out` into the file the result is written to. A
// directory takes the name the command derived from the URL it submitted.
func sinkPath(command, out, name string) (string, error) {
	if out == "" {
		out = "."
	}
	if name == "" {
		name = "download.bin"
	}
	if isDirectory(out) {
		out = filepath.Join(out, name)
	}
	absolute, err := filepath.Abs(out)
	if err != nil {
		return "", &exitError{exitUsage, fmt.Errorf("%s: --out: %w", command, err)}
	}
	return absolute, nil
}
