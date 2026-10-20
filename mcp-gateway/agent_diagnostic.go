package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/fileid"
)

const agentDiagnosticName = "helper-lifecycle.json"

// Hosts start and end several helpers within one second, for example Claude
// Code reconnecting two sessions while starting a third, and Codex probing its
// catalogue. One write attempt has the ordinary budget; a helper's own end and
// its establishment retry within a longer bound so contention cannot erase them.
const (
	agentDiagnosticAttempt     = 250 * time.Millisecond
	agentDiagnosticFinalBudget = 2 * time.Second
)

type agentDiagnosticKey struct{}

func observeAgentDiagnostic(ctx context.Context, phase string, err error) {
	d, _ := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	if d != nil && d.parent != nil && d.parent.Err() != nil && errors.Is(err, context.Canceled) {
		phase, err = "parent_end", d.parent.Err()
	}
	d.observe(phase, agentLossWord(err))
}

// agentLossWord classifies an end or a loss in the closed cause vocabulary.
// Error text, which may carry paths or service detail, is never recorded.
func agentLossWord(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, io.EOF):
		return "eof"
	}
	var outcome *agentInboxOutcomeError
	if errors.As(err, &outcome) {
		if word := outcome.outcome.String(); validAgentInboxCause(word) {
			return "inbox_" + word
		}
	}
	return "error"
}

// observeAgentLoss records a presence loss the connection recovers from. It
// is not an end: the row keeps its phase, counts the loss and names its cause.
func observeAgentLoss(ctx context.Context, err error, canonical bool) {
	d, _ := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.terminal {
		return
	}
	if d.record.Losses < 1000000 {
		d.record.Losses++
	}
	d.record.LastLoss = agentLossWord(err)
	d.record.LastLossGate = "source"
	if canonical {
		d.record.LastLossGate = "canonical"
	}
	d.persist()
}

// observeAgentReadmitted counts a re-admission of the same context.
func observeAgentReadmitted(ctx context.Context) {
	d, _ := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.terminal {
		return
	}
	if d.record.Readmitted < 1000000 {
		d.record.Readmitted++
	}
	d.persist()
}

// reportAgentPanic writes a recovered panic and its stack to stderr, which the
// host keeps in its MCP server log. Stacks name code, never request contents.
func reportAgentPanic(where string, recovered any) {
	//unchecked: diagnostic on a recovered fault; stderr has no fallback destination
	fmt.Fprintf(os.Stderr, "openabstractions-mcp pid=%d: recovered panic in %s: %v\n%s", os.Getpid(), where, recovered, debug.Stack())
}

// observeAgentEstablished marks a connection that outlived its first presence
// renewal. Host catalogue probes end within seconds and never reach it.
// The mark is written once, off the presence loop, so its bounded write never
// delays a renewal.
func observeAgentEstablished(ctx context.Context) {
	d, _ := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	if d == nil || !d.establishing.CompareAndSwap(false, true) {
		return
	}
	go d.establish()
}

// Every generated Messages outcome names an inbox cause, including held.
func validAgentInboxCause(word string) bool {
	_, ok := messages.ParseOutcome(word)
	return ok
}

// One bounded snapshot, rather than an event log. Missing terminal metadata
// proves no exit cause: the process may have been killed before it could write.
// An established row without an end is a working connection that was killed
// or crashed; an unestablished one is usually a host's short catalogue probe.
type agentDiagnosticRecord struct {
	PID         int                       `json:"pid"`
	Birth       string                    `json:"birth,omitempty"`
	ImageSHA256 string                    `json:"image_sha256"`
	Phase       string                    `json:"phase"`
	Cause       string                    `json:"cause"`
	Established bool                      `json:"established,omitempty"`
	Receiving   *agentReceivingDiagnostic `json:"receiving,omitempty"`
	// ClaudeBinding describes the existing host lifecycle call, never admission
	// or host approval. It retains no launch/session metadata or error text.
	ClaudeBinding *claudeBindingDiagnostic `json:"claude_binding,omitempty"`
	// WatchHealth is untrusted host telemetry for this helper's own context.
	// It supplies no native admission, routing or readiness conclusion.
	WatchHealth *agentWatchHealthDiagnostic `json:"watch_health,omitempty"`
	// Losses counts presence losses the connection survived, Readmitted the
	// re-admissions that followed, and LastLoss names the latest cause.
	Losses     uint32 `json:"losses,omitempty"`
	Readmitted uint32 `json:"readmitted,omitempty"`
	LastLoss   string `json:"last_loss,omitempty"`
	// LastLossGate identifies the failed presence owner. It grants no admission.
	LastLossGate string `json:"last_loss_gate,omitempty"`
	// Gone marks a row whose process ended without recording an end: another
	// helper found its PID gone or reused. The host killed it or it crashed.
	Gone bool `json:"gone,omitempty"`
}

type agentWatchHealthDiagnostic struct {
	Claim   string `json:"claim"`
	Context string `json:"context"`
}

type claudeBindingDiagnostic struct {
	Stage  string `json:"stage"`
	Result string `json:"result"`
	Reason string `json:"reason"`
}

func validClaudeBindingDiagnostic(r *claudeBindingDiagnostic) bool {
	if r == nil {
		return true
	}
	switch r.Stage {
	case "awaiting_start":
		return r.Result == "none" && (r.Reason == "none" || r.Reason == "invalid_child_session")
	case "start":
		if r.Result == "pending" {
			return r.Reason == "none"
		}
		if r.Result == "observed" {
			return r.Reason == "none"
		}
		if r.Result != "unavailable" {
			return false
		}
		switch r.Reason {
		case "invalid_request", "invalid_child_session", "binding_unavailable", "retired", "receiver_completed", "receiver_deadline", "receiver_cancelled", "receiver_eof", "receiver_error":
			return true
		}
		return strings.HasPrefix(r.Reason, "receiver_inbox_") && validAgentInboxCause(strings.TrimPrefix(r.Reason, "receiver_inbox_"))
	case "end":
		return (r.Result == "ended" || r.Result == "retired") && r.Reason == "none" || r.Result == "unavailable" && r.Reason == "metadata_mismatch"
	}
	return false
}

func observeClaudeBinding(ctx context.Context, stage, result, reason string) {
	next := claudeBindingDiagnostic{Stage: stage, Result: result, Reason: reason}
	if !validClaudeBindingDiagnostic(&next) {
		return
	}
	d, _ := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// The first call exposes an in-flight start. Once a start outcome is held,
	// repeated correlation calls retain it until their actual outcome changes.
	if next.Stage == "start" && next.Result == "pending" && d.record.ClaudeBinding != nil && d.record.ClaudeBinding.Stage == "start" && d.record.ClaudeBinding.Result != "pending" {
		return
	}
	if d.terminal || d.record.ClaudeBinding != nil && *d.record.ClaudeBinding == next {
		return
	}
	d.record.ClaudeBinding = &next
	d.persist()
}

func observeMessagesWatchHealth(ctx context.Context, claim, nativeContext string) {
	if ctx.Err() != nil || messagesWatchHealthText(claim) == "" || !agentRoutingContext.MatchString(nativeContext) {
		return
	}
	d, _ := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.terminal {
		return
	}
	next := agentWatchHealthDiagnostic{Claim: claim, Context: nativeContext}
	if d.record.WatchHealth != nil && *d.record.WatchHealth == next {
		return
	}
	d.record.WatchHealth = &next
	d.persist()
}

type agentReceivingDiagnostic struct {
	Stage         string `json:"stage"`
	Reason        string `json:"reason"`
	Thread        string `json:"thread,omitempty"`
	Context       string `json:"context,omitempty"`
	QueueAttempts uint32 `json:"queue_attempts"`
	QueueResult   string `json:"queue_result"`
}

// Updates the existing bounded C1 snapshot. Only closed events and our own
// routing labels are retained; errors, request metadata and contents stay out.
func observeCodexReceiving(ctx context.Context, event, thread, nativeContext string) {
	d, _ := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	if d == nil {
		return
	}
	if thread != "" && !codexQueueThread.MatchString(thread) || nativeContext != "" && !agentRoutingContext.MatchString(nativeContext) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	next := agentReceivingDiagnostic{Stage: "unavailable", Reason: "missing_thread_metadata", QueueResult: "none"}
	if d.record.Receiving != nil {
		next = *d.record.Receiving
	}
	bound := next.Thread != ""
	if thread != "" {
		if next.Thread != "" && next.Thread != thread {
			return
		}
		next.Thread = thread
	}
	if nativeContext != "" {
		if bound && next.Context != "" && next.Context != nativeContext {
			return
		}
		next.Context = nativeContext
	}
	previousStage, previousReason := next.Stage, next.Reason
	switch event {
	case "unavailable_missing_thread_metadata":
		next.Stage, next.Reason = "unavailable", "missing_thread_metadata"
		if next.Thread != "" {
			next.Stage = "stopped"
		}
	case "unavailable_thread_changed":
		next.Stage, next.Reason = "stopped", "thread_changed"
	case "selecting_cli":
		next.Stage, next.Reason = "selecting_cli", "none"
	case "cli_unavailable":
		next.Stage, next.Reason = "stopped", "cli_unavailable"
	case "watching":
		next.Stage, next.Reason = "watching", "none"
	case "observation_unavailable":
		next.Stage, next.Reason = "stopped", "observation_unavailable"
	case "recovering_unknown", "recovering_unavailable":
		next.Stage, next.Reason = "recovering", event
	case "observation_invalid", "observation_forbidden", "observation_recovery_expired", "binding_unavailable":
		next.Stage, next.Reason = "stopped", event
	case "context_changed":
		next.Stage, next.Reason = "stopped", "context_changed"
	case "queue_attempt":
		next.Stage, next.Reason, next.QueueResult = "queue", "queue_attempt", "pending"
		if next.QueueAttempts < 1000000 {
			next.QueueAttempts++
		}
	case "queue_accepted":
		next.Stage, next.Reason, next.QueueResult = "queue", "queue_accepted", "accepted"
	case "queue_unknown":
		next.Stage, next.Reason, next.QueueResult = "stopped", "queue_unknown", "unknown"
	case "unavailable_connection_closed":
		next.Stage, next.Reason = "stopped", "connection_closed"
	case "unavailable_receiver_stopped":
		next.Stage = "stopped"
		if next.Reason == "none" {
			next.Reason = "receiver_stopped"
		}
	case "resuming":
		// The supervisor starts a stopped receiver again on the same thread.
		next.Stage, next.Reason = "recovering", "recovering_unavailable"
		if next.Thread == "" || next.Context == "" {
			return
		}
	default:
		return
	}
	// Cancellation aftermath retains the initiating closed reason. QueueResult
	// still records an in-flight dispatch whose effect became uncertain.
	if previousStage == "stopped" && previousReason != "none" && event != "unavailable_thread_changed" && event != "resuming" {
		next.Stage, next.Reason = previousStage, previousReason
	}
	if !validAgentReceivingDiagnostic(&next) {
		return
	}
	if d.record.Receiving != nil && *d.record.Receiving == next {
		return
	}
	d.record.Receiving = &next
	d.persist()
}

func validAgentReceivingDiagnostic(r *agentReceivingDiagnostic) bool {
	if r == nil {
		return true
	}
	if r.QueueAttempts > 1000000 || r.Thread != "" && !codexQueueThread.MatchString(r.Thread) || r.Context != "" && !agentRoutingContext.MatchString(r.Context) {
		return false
	}
	switch r.Stage {
	case "unavailable", "selecting_cli", "watching", "recovering", "queue", "stopped":
	default:
		return false
	}
	switch r.Reason {
	case "none", "missing_thread_metadata", "thread_changed", "cli_unavailable", "observation_unavailable", "observation_invalid", "observation_forbidden", "observation_recovery_expired", "binding_unavailable", "recovering_unknown", "recovering_unavailable", "context_changed", "queue_attempt", "queue_accepted", "queue_unknown", "connection_closed", "receiver_stopped":
	default:
		return false
	}
	switch r.QueueResult {
	case "none":
	case "pending", "accepted", "unknown":
		if r.QueueAttempts == 0 {
			return false
		}
	default:
		return false
	}
	if (r.Stage == "selecting_cli" || r.Stage == "watching" || r.Stage == "recovering" || r.Stage == "queue") && (r.Thread == "" || r.Context == "") {
		return false
	}
	return true
}

type agentDiagnostic struct {
	mu           sync.Mutex
	parent       context.Context
	root         fileid.Handle
	record       agentDiagnosticRecord
	terminal     bool
	establishing atomic.Bool
}

func newAgentDiagnostic(state, program string) *agentDiagnostic {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	root, _, err := setupIdentity(ctx, state, true)
	if err != nil {
		return nil
	}
	image, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		root.Close()
		return nil
	}
	defer image.Close()
	if image.FileID().IsDir() {
		root.Close()
		return nil
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(image.File(), (512<<20)+1))
	if err != nil || n > 512<<20 {
		root.Close()
		return nil
	}
	d := &agentDiagnostic{root: root, record: agentDiagnosticRecord{PID: os.Getpid(), Birth: agentDiagnosticBirth(), ImageSHA256: hex.EncodeToString(hash.Sum(nil)), Phase: "started", Cause: "unproved"}}
	d.persist()
	return d
}

func (d *agentDiagnostic) close() {
	if d != nil {
		d.root.Close()
	}
}

func (d *agentDiagnostic) observe(phase, cause string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.terminal {
		return
	}
	d.terminal = true
	d.record.Phase, d.record.Cause = phase, cause
	if !validAgentDiagnosticRows([]agentDiagnosticRecord{d.record}) {
		// Retain the phase when a cause word falls outside the closed vocabulary.
		d.record.Cause = "error"
	}
	d.persistWithin(agentDiagnosticFinalBudget)
}

func (d *agentDiagnostic) establish() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.terminal || d.record.Established {
		return
	}
	d.record.Established = true
	d.persistWithin(agentDiagnosticFinalBudget)
}

func (d *agentDiagnostic) persist() bool {
	ctx, cancel := context.WithTimeout(context.Background(), agentDiagnosticAttempt)
	defer cancel()
	return d.persistContext(ctx) == nil
}

// persistWithin retries until the snapshot holds this record or the budget
// ends. Each failed write changed nothing, so a later attempt is safe.
func (d *agentDiagnostic) persistWithin(budget time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	for {
		if d.persistContext(ctx) == nil {
			return true
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (d *agentDiagnostic) persistContext(ctx context.Context) error {
	if !setupIdentitiesCurrent(ctx, []setupIdentityRef{d.root}) {
		return errors.New("helper lifecycle state changed")
	}
	// CAS captures/refuses link and replacement races at its native boundary.
	// A bounded recent-process array also preserves concurrent helper outcomes.
	return cas.ChangeLimitContext(ctx, filepath.Join(d.root.FileID().Path(), agentDiagnosticName), 16<<10, func(raw []byte) ([]byte, error) {
		// Only this build's own row must be valid. A row from another build of
		// the helper, for example one with a newer cause word during an
		// upgrade, stays byte for byte as that build wrote it.
		if !validAgentDiagnosticRows([]agentDiagnosticRecord{d.record}) {
			return nil, errors.New("invalid helper lifecycle metadata")
		}
		var rows []json.RawMessage
		if len(raw) != 0 {
			if err := json.Unmarshal(raw, &rows); err != nil {
				return nil, err
			}
		}
		own, err := json.Marshal(d.record)
		if err != nil {
			return nil, err
		}
		decoded := make([]agentDiagnosticRecord, len(rows))
		mine := -1
		for i, row := range rows {
			// A row that does not decode keeps zero identity and never matches.
			_ = json.Unmarshal(row, &decoded[i])
			if decoded[i].PID == d.record.PID && decoded[i].Birth == d.record.Birth && decoded[i].ImageSHA256 == d.record.ImageSHA256 {
				mine = i
			}
		}
		markGoneAgentRows(rows, decoded, d.record.PID)
		if mine >= 0 {
			rows[mine] = own
			return json.Marshal(rows)
		}
		for len(rows) >= 16 {
			victim := agentDiagnosticVictim(decoded)
			rows = append(rows[:victim:victim], rows[victim+1:]...)
			decoded = append(decoded[:victim:victim], decoded[victim+1:]...)
		}
		return json.Marshal(append(rows, own))
	})
}

// markGoneAgentRows marks rows still at started whose process has ended, so a
// helper the host killed, or one that crashed, no longer reads as running.
// Only rows this build validates are rewritten; others stay byte for byte.
func markGoneAgentRows(rows []json.RawMessage, decoded []agentDiagnosticRecord, self int) {
	for i := range rows {
		row := decoded[i]
		if row.Phase != "started" || row.Gone || row.PID == self || row.Birth == "" || !validAgentDiagnosticRows([]agentDiagnosticRecord{row}) {
			continue
		}
		if !agentProcessGone(row.PID, row.Birth) {
			continue
		}
		row.Gone = true
		if raw, err := json.Marshal(row); err == nil {
			rows[i], decoded[i] = raw, row
		}
	}
}

// agentDiagnosticVictim drops, oldest first: a gone row that never
// established, which is usually a host's catalogue probe the host killed; an
// ended row that never established; an unestablished row without an end. A
// live connection that worked survives a burst of probes; otherwise the
// oldest row goes.
func agentDiagnosticVictim(rows []agentDiagnosticRecord) int {
	for _, wanted := range []func(agentDiagnosticRecord) bool{
		func(row agentDiagnosticRecord) bool { return row.Gone && !row.Established },
		func(row agentDiagnosticRecord) bool { return row.Phase != "started" && !row.Established },
		func(row agentDiagnosticRecord) bool { return row.Gone },
		func(row agentDiagnosticRecord) bool { return row.Phase == "started" && !row.Established },
	} {
		for i, row := range rows {
			if wanted(row) {
				return i
			}
		}
	}
	return 0
}

// readAgentDiagnostics returns the rows this build understands. A row another
// build wrote with a newer word is left out rather than hiding every row.
func readAgentDiagnostics(ctx context.Context, state string) []agentDiagnosticRecord {
	root, _, err := setupIdentity(ctx, state, true)
	if err != nil {
		return nil
	}
	defer root.Close()
	raw, err := cas.ReadLimitContext(ctx, filepath.Join(root.FileID().Path(), agentDiagnosticName), 16<<10)
	if err != nil || !setupIdentitiesCurrent(ctx, []setupIdentityRef{root}) {
		return nil
	}
	var stored []json.RawMessage
	if json.Unmarshal(raw, &stored) != nil || len(stored) > 16 {
		return nil
	}
	var rows []agentDiagnosticRecord
	for _, one := range stored {
		var row agentDiagnosticRecord
		if json.Unmarshal(one, &row) == nil && validAgentDiagnosticRows([]agentDiagnosticRecord{row}) {
			rows = append(rows, row)
		}
	}
	return rows
}

func validAgentDiagnosticRows(rows []agentDiagnosticRecord) bool {
	if len(rows) > 16 {
		return false
	}
	for _, row := range rows {
		if !validClaudeBindingDiagnostic(row.ClaudeBinding) {
			return false
		}
		if row.WatchHealth != nil && (messagesWatchHealthText(row.WatchHealth.Claim) == "" || !agentRoutingContext.MatchString(row.WatchHealth.Context)) {
			return false
		}
		if !validAgentReceivingDiagnostic(row.Receiving) {
			return false
		}
		if row.PID <= 0 || len(row.ImageSHA256) != 64 {
			return false
		}
		if _, err := hex.DecodeString(row.ImageSHA256); err != nil {
			return false
		}
		if row.Birth != "" {
			if _, err := time.Parse(time.RFC3339Nano, row.Birth); err != nil {
				return false
			}
		}
		switch row.Phase {
		case "started", "stdio_end", "presence_end", "parent_end", "admission_end", "connection_end":
		default:
			return false
		}
		if !validAgentCauseWord(row.Cause) || row.LastLoss != "" && (row.LastLoss == "unproved" || !validAgentCauseWord(row.LastLoss)) {
			return false
		}
		switch row.LastLossGate {
		case "", "source", "canonical":
		default:
			return false
		}
		if row.Losses > 1000000 || row.Readmitted > 1000000 {
			return false
		}
	}
	return true
}

func validAgentCauseWord(word string) bool {
	switch word {
	case "unproved", "completed", "error", "deadline", "cancelled", "eof":
		return true
	}
	inbox, ok := strings.CutPrefix(word, "inbox_")
	return ok && validAgentInboxCause(inbox)
}
