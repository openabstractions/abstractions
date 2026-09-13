namespace * abstraction.job.acceptance

// Existing admission promises, advertised only when the selected provider
// supports them. These identifiers describe accepted work surviving caller exit,
// service restart and recoverable acceptance reconciliation; execution effects
// require their own negotiated guarantees.
const list<string> admission_guarantees = [
  "abstraction.job/caller-exit@1",
  "abstraction.job/service-restart@1",
  "abstraction.job/reconciliation@1"
]

// Additive service vocabulary; job.thrift's tagged Record is unchanged.
encoding json {
  escape = "minimal"
  indent = "2"
  map_keys = "utf8-bytes"
  numbers = "integer-decimal"
  opaque = "verbatim"
  terminator = "newline"
  duplicate_keys = "refuse"
  depth_limit = "64"
}
refusal {
  1: malformed (stage = "grammar")
  2: bad_string (stage = "grammar")
  3: number_spelling (stage = "grammar")
  4: wrong_type (stage = "grammar")
  5: depth_exceeded (stage = "grammar")
  6: duplicate_key (stage = "grammar")
  7: duplicate_field (stage = "structure")
  8: unknown_field (stage = "structure")
  9: missing_field (stage = "structure")
 10: bad_binary (stage = "structure")
 11: bad_enum (stage = "structure")
 12: trailing_bytes (stage = "document")
}
struct RequestIdentity {
  1: required string key
  2: required string history_epoch
} (unknown_fields = "refuse", doc="Stable SDK key in an owner-issued history epoch. Scope is authenticated caller plus this service contract, never a caller-provided principal. Persist before send for restart recovery.")
struct Submission {
  1: required RequestIdentity identity
  2: required string kind
  3: required binary spec
  4: required list<string> required_guarantees
} (unknown_fields = "refuse", doc="Opaque kind-specific specification bytes, not a second tagged job Record. Equality includes kind, exact spec bytes and the set of required guarantees. Credentials are supplied at the authorized service boundary.")
struct Receipt {
  1: required RequestIdentity identity
  2: required string logical_owner
  3: required string operation_id
  4: required list<string> accepted_guarantees
  5: required i64 history_retention_ms
} (unknown_fields = "refuse", doc="Recoverable acceptance evidence. Retention is a minimum duration from original acceptance, never renewed by replay. Expiry does not end work, transfer ownership or authorize duplicate execution. IDs confer no authority.")
enum Outcome {
  1: accepted
  2: definitely_not_accepted
  3: unknown
  4: key_conflict
  5: forbidden
  6: invalid
} (unknown = "refuse")
struct AcceptanceResult {
  1: required Outcome outcome
  2: optional Receipt receipt (omit = "absent")
  3: required string reason
} (document = "true", unknown_fields = "refuse", doc="Accepted requires a receipt; other outcomes forbid one. Definite nonacceptance requires authoritative sealed evidence preventing any delayed acceptance of this identity. Absence, timeout, expired history and access denial are insufficient.")
struct HistoryWindow {
  1: required string logical_owner
  2: required string history_epoch
  3: required i64 minimum_retention_ms
} (unknown_fields = "refuse", doc="Owner-issued acceptance epoch and minimum reconciliation retention. After closing an epoch the owner fences all its submissions, including delayed ones. This does not assert that old unknown work was never accepted.")
enum CancellationOutcome {
  1: requested
  2: already_terminal
  3: unknown
  4: forbidden
  5: unsupported
} (unknown = "refuse")
struct CancellationResult {
  1: required CancellationOutcome outcome
} (unknown_fields = "refuse", doc="Requested acknowledges cancellation intent, not stopped effects. Completion may win the race; observe the existing operation for its terminal result.")
service RecoverableAcceptance {
  HistoryWindow GetHistoryWindow() (doc="Obtain the logical owner and history epoch before first submission; creates no work.")
  AcceptanceResult Submit(1: Submission submission) (doc="Atomically associate authenticated request identity, arguments, operation and guarantees before acknowledging. Duplicate equal arguments recover the original receipt. No weaker provider fallback on unknown.")
  AcceptanceResult Reconcile(1: RequestIdentity identity) (doc="Authorized lookup at the original logical owner after lost request, lost reply or caller restart. A definite negative seals the identity against delayed submissions. Unavailable evidence gives unknown.")
  CancellationResult CancelWork(1: RequestIdentity identity) (doc="Authorized explicit work cancellation. Cancelling a transport wait is never this operation and never relinquishes accepted work ownership.")
} (wire_name = "abstraction.job/acceptance@1", doc="Version 1 recoverable acceptance vocabulary, local or remote. Providers must implement atomic recovery and downstream deduplication before advertising those guarantees. Legacy Store is not implicitly upgraded.")

// Observation and result access retain the original authenticated acceptance scope.
enum WorkState {
  1: pending
  2: running
  3: transferred
  4: complete
  5: failed
  6: cancelled
} (unknown = "refuse")
struct WorkProgress {
  1: required i64 done
  2: required i64 total
} (unknown_fields = "refuse", doc="Advisory nonnegative progress. Zero total means unknown. Progress does not authorize delivery or imply completion.")
enum FailureClass {
  1: retryable
  2: permanent
  3: unknown
} (unknown = "refuse")
struct WorkFailure {
  1: required FailureClass classification
  2: required string message
} (unknown_fields = "refuse", doc="Last-attempt failure. Unknown classification remains unknown; do not infer it from message text. Retryable failure can coexist with pending work.")
struct OperationSnapshot {
  1: required Receipt receipt
  2: required WorkState state
  3: required WorkProgress progress
  4: required bool cancellation_requested
  5: optional WorkFailure failure (omit = "absent")
} (unknown_fields = "refuse", doc="Receipt binds original request and logical owner. Cancellation requested is intent, not stopped effects. Progress and last-attempt failure are advisory; no provider paths are exposed.")
enum ObservationOutcome {
  1: observed
  2: unknown
  3: forbidden
  4: invalid
  5: definitely_not_accepted
} (unknown = "refuse")
struct ObservationResult {
  1: required ObservationOutcome outcome
  2: optional OperationSnapshot snapshot (omit = "absent")
} (unknown_fields = "refuse", doc="Exactly observed carries a snapshot; all other outcomes forbid it. Absent identities are unknown and observation never seals them. Definite nonacceptance requires an existing authoritative seal. Already accepted journals may be recovered.")
enum ResultOutcome {
  1: data
  2: not_ready
  3: unavailable
  4: unsupported
  5: unknown
  6: forbidden
  7: invalid
} (unknown = "refuse")
struct ResultChunk {
  1: required Receipt receipt
  2: required i64 offset
  3: required i64 total
  4: required binary data
  5: required bool eof
} (unknown_fields = "refuse", doc="Complete immutable result bytes bound to the original receipt. Offset equals requested nonnegative offset and is at most nonnegative total. Data length is at most requested max_bytes and total-offset. EOF is true exactly when offset plus data length equals total, including an empty complete result. Data is nonempty unless offset equals total and EOF is true. Missing data and errors never imply EOF.")
struct ResultRead {
  1: required ResultOutcome outcome
  2: optional ResultChunk chunk (omit = "absent")
} (unknown_fields = "refuse", doc="Exactly data carries a chunk; other outcomes forbid it. Only complete immutable results produce data. Incomplete work is not_ready; missing completed bytes are unavailable. Unsupported access, unknown identity, forbidden access and invalid bounds remain distinct.")
service OperationControl {
  ObservationResult ObserveWork(1: RequestIdentity identity) (doc="Observe in the original authenticated acceptance scope. Absent identities remain unsealed; may recover an already accepted journal.")
  ResultRead ReadResult(1: RequestIdentity identity, 2: i64 offset, 3: i64 max_bytes) (doc="Read complete immutable bytes without exposing paths. Offset must be nonnegative; max_bytes must be in 1..65536. Data obeys ResultChunk bounds. Absence, failure, incomplete work and transport errors are never EOF. This read never seals an absent identity.")
} (wire_name = "abstraction.job/operations@1", doc="Observation and result access use the same authenticated caller scope and request identity as RecoverableAcceptance despite their separate service wire identity. Existing acceptance methods and wire contract remain unchanged.")


enum InventoryOutcome {
  1: page
  2: gap
  3: forbidden
  4: invalid
  5: unavailable
} (unknown = "refuse")
struct InventoryPage {
  1: required InventoryOutcome outcome
  2: required list<OperationSnapshot> snapshots
  3: required string next
  4: required bool complete
} (unknown_fields = "refuse", doc="Caller-scoped accepted-operation observations without paths. Only page carries snapshots. A noncomplete page always has a next cursor, even when no own operations were scanned. Complete pages have empty next. Refusals carry no snapshots, empty next and complete=false. Directory traversal is a weak live view: concurrent insertions/removals can be omitted or repeated, not a stable transaction snapshot. An unchanged tree is fully traversable. Large failure diagnostics may be replaced by a bounded generic message.")
service JobInventory {
  InventoryPage ListWork(1: string cursor, 2: i64 limit) (doc="List only the authenticated acceptance scope. Empty cursor starts a bounded enumeration; limit is 1..64 snapshots. Opaque cursors are bound to this provider instance and caller. Sessions expire after 30 seconds idle and restart yields gap; at most 32 sessions exist. Each call scans at most 256 directory entries and 16MiB provider bytes, with a 512KiB compact-JSON snapshot accounting budget (frame encoding overhead is additional). Empty scan pages still advance. Repeating the latest input cursor with the same limit replays its page; older cursors return gap. A lost initial empty-cursor reply can be restarted explicitly; no global scan or cross-caller authority is implied. Partial reads may return unavailable rather than falsely complete. No subscription or retention protocol is supplied.")
} (wire_name = "abstraction.job/inventory@1", doc="Bounded own-scope inventory on the acceptance provider endpoint. Authorization is independent of cursor text and uses the existing native caller scope. Listing does not authorize cross-scope Observe/Cancel or expose private provider paths.")
