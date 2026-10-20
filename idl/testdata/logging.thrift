namespace * abstraction.logging

// Service-client record schema. The legacy storage codec is not replaced.
// The framed service accepts generated envelopes; legacy raw-record listeners
// remain a separate explicitly selected adapter.
// Byte spelling and reader
// permissiveness are not promised identical to Go encoding/json.
// LOG-R1/R3/R5/R7 define framing, levels, attributes and timestamps.
// LOG-R2: callers supply schema=1; all generated codecs enforce it.
// Equality is a constraint, not a default. No persistence acknowledgement exists.
encoding json {
  escape = "minimal"
  indent = "2"
  map_keys = "utf8-bytes"
  numbers = "integer-decimal"
  opaque = "verbatim"
  terminator = "newline"
  duplicate_keys = "last"
  depth_limit = "64"
}
refusal {
  1: malformed (stage = "grammar")
  2: bad_string (stage = "grammar")
  3: number_spelling (stage = "grammar")
  4: wrong_type (stage = "grammar")
  5: bad_timestamp (stage = "grammar")
  6: depth_exceeded (stage = "grammar")
  7: duplicate_field (stage = "structure")
  8: unknown_field (stage = "structure")
  9: missing_field (stage = "structure")
 10: bad_schema (stage = "structure")
 11: bad_enum (stage = "structure")
 12: trailing_bytes (stage = "document")
}
// Same built-in grammar as job.thrift, not a logging-specific instant.
typedef string timestamp (write = "rfc3339-micros", read = "rfc3339-wide")
// Preserve the existing ordered logging provenance chain. These are claims;
// decoding them confers no trust or additional identity proof.
struct Attestation {
  1: required string by
  2: required bool verified
  3: required i64 hop
  4: optional string program (omit = "zero")
  5: optional string host (omit = "zero")
  6: optional string user (omit = "zero")
  7: optional string exe (omit = "zero")
  8: required i64 uid
  9: required i64 gid
 10: required i64 pid
 11: optional string key (omit = "zero")
 12: optional string mac (omit = "zero")
} (unknown_fields = "grant", doc = "One hop of the provenance chain. program is the name a party gives itself and appears on the writer's self claim. exe is the executable path the attesting mechanism resolved for the party it established; a receiving service's stamp sets exe and leaves program empty. user is the account name or Windows SID; uid and gid are -1 where unestablished.")
struct Record {
  1: required i64 schema (equals = "1", equals_refusal = "bad_schema")
  2: required timestamp time
  3: required i64 level
  4: required string msg
  5: optional list<Attestation> identity (omit = "zero")
  6: optional string job (omit = "zero")
  7: optional map<string,string> attrs (omit = "zero")
} (document = "true", unknown_fields = "grant")
// The generated service protocol carries version, service, method and typed
// arguments. It is distinct from the legacy raw-record stream. Success means
// local submission, not receiver acceptance or durable storage.
service Sink {
  oneway void Write(1: Record record) (doc="Submit a log record. Completion confirms local transport submission only.")
} (wire_name = "abstraction.logging/sink@1", doc="A sink for structured log records across a service boundary.")


enum PageOutcome {
  1: page
  2: gap
  3: unavailable
  4: invalid_request
  5: record_too_large
  6: corrupt
  7: unsupported
} (unknown = "refuse")
struct Page {
  1: required PageOutcome outcome
  2: required list<Record> records
  3: required string next
  4: required bool at_end
} (unknown_fields = "refuse", doc="A bounded page in provider append order. next is an opaque continuation bound to this history instance. at_end means the observed end during this call; later records may appear. Refusals contain no records and never advance a supplied cursor. gap requires an explicit restart with empty cursor; it never silently restarts a stream.")
// Codes HistoryReader and HistoryObserver handlers send on the reply error
// channel when history access is not authorized, beside the dispatcher's own.
const list<string> history_error_codes = ["caller_unavailable", "identity_required", "wrong_user", "policy_unavailable", "forbidden"]
// Words a caller sends as the cursor of HistoryReader.Read or
// HistoryObserver.Observe in place of a continuation. "end" begins at the
// current end of retained history. A provider never issues a word as next.
const list<string> history_cursor_words = ["end"]

service HistoryReader {
  Page Read(1: string cursor, 2: i64 max_records, 3: i64 max_bytes) (doc="Read retained records through the service. Empty cursor starts at earliest retained history. Cursor end (history_cursor_words) starts at the current end: the page holds no records, at_end is true and next continues from that end. Limits are 1..256 records and 1..65536 encoded-record bytes; a first record that exceeds the byte limit gives record_too_large. A provider without history gives unavailable. Cursors may expire on restart or retention changes, reported as gap. Snapshot reads may be repeated at end; this method promises no subscription or persistence acknowledgement. The receiving service authorizes history access from native caller evidence.")
} (wire_name = "abstraction.logging/reader@1", error_codes = "history_error_codes", doc="Authorized bounded access to retained logging history. Applications receive records and opaque cursors; history location and retention belong to the provider.")

service HistoryObserver {
  Page Observe(1: string cursor, 2: i64 max_records, 3: i64 max_bytes, 4: i64 wait_ms) (doc="Read with the same bounds and cursor semantics as HistoryReader. Cursor end fixes the current end when the call begins and returns only records appended after it. At an empty current end, wait up to wait_ms (0..30000) for a provider notification, then reread once; expiry may return an empty current-end page. Cancellation stops waiting and does not undo logging. Providers without observation return unsupported. Bounded waiter capacity exhaustion returns unavailable. Authorization is rechecked before records return. This is long-poll observation; external file writers have no notification promise.")
} (wire_name = "abstraction.logging/observer@1", error_codes = "history_error_codes", doc="Authorized bounded long-poll history observation with provider-owned notifications.")

// New history services carry business refusals in their typed result.
// The published Page and PageOutcome retain the reader@1/observer@1 shape.
enum HistoryPageOutcome {
  1: page
  2: gap
  3: unavailable
  4: invalid
  5: record_too_large
  6: corrupt
  7: unsupported
  8: forbidden
  9: not_permitted
 10: os_authorization_required
 11: os_authorization_pending
} (unknown="refuse", reader="act")
const list<string> history_identity_error_codes = ["caller_unavailable", "identity_required", "wrong_user"]

enum ApplicationLogSelection { 1: one_app, 2: all_apps } (unknown="refuse", reader="act")
struct ApplicationLogTarget {
  1: required ApplicationLogSelection selection
  2: optional string program (omit="absent")
} (unknown_fields="refuse",doc="One requires an Identity program; all omits program. Account is established by the receiving service. All includes current and future attributed OA emitters in that account, subject to blocks. Native sources and unattributed legacy rows are excluded.")
struct ApplicationLogResource {
  1: required string program
  2: optional string captured_file_key (omit="absent")
} (unknown_fields="refuse",doc="The application-log@1 owning target document. Trusted preparation normalizes the program and captures its key; callers cannot supply a key to a logging read. Historical enforcers use their captured evidence without reopening a stored image.")
struct ApplicationLogPage {
  1: required HistoryPageOutcome outcome
  2: required list<Record> records
  3: required string next
  4: required bool at_end
} (unknown_fields="refuse",doc="Bounded attributed history. Empty advancing non-end pages report finite scan progress. Refusals preserve the supplied cursor and contain no records. Cursors bind caller, selection, captured target, history instance and position; gap requires explicit restart.")
service ApplicationHistoryReader {
  ApplicationLogPage Read(1:ApplicationLogTarget target,2:string cursor,3:i64 max_records,4:i64 max_bytes)(doc="Read one or all emitting programs. Bounds are 1..256 returned records and 1..65536 encoded Record bytes; scan work is independently bounded. Empty cursor starts earliest retained history; end fixes the current physical end. One consistent Rights snapshot governs selection and concrete outgoing emitters. No fallback to account-wide reader@1 is permitted.")
}(wire_name="abstraction.logging/reader@2",error_codes="history_identity_error_codes")
service ApplicationHistoryObserver {
  ApplicationLogPage Observe(1:ApplicationLogTarget target,2:string cursor,3:i64 max_records,4:i64 max_bytes,5:i64 wait_ms)(doc="Read with reader@2 semantics, waiting once at an empty physical end for 0..30000 ms. Unrelated writes can produce filtered progress. Cancellation, fixed deadline and bounded waiter capacity apply. Authorization is reevaluated before delivery; no decision is retained across waits or pages.")
}(wire_name="abstraction.logging/observer@2",error_codes="history_identity_error_codes")

enum SourceAvailability {
  1: available
  2: permission_denied
  3: unavailable
  4: unsupported
} (unknown = "refuse", reader="act")
enum SourceLimitation {
  1: retention
  2: native_redaction
  3: collection_policy
  4: native_access
  5: visibility_unknown
} (unknown = "grant", reader="display")
struct SourceDescriptor {
  1: required string id
  2: required SourceAvailability availability
  3: required list<SourceLimitation> limitations
} (unknown_fields = "refuse", doc="A fixed source identifier and current access status. available means the native API can be queried under the granted scope; it does not promise complete capture. limitations states known platform constraints. An empty page establishes no match in the source's visible retained data only.")
enum SourceCatalogOutcome {
  1: catalog
  2: forbidden
  3: not_permitted
  4: unavailable
  5: invalid
} (unknown="refuse", reader="act")
struct SourceCatalog {
  1: required list<SourceDescriptor> sources
  2: required SourceCatalogOutcome outcome
} (unknown_fields = "refuse")
struct ProducerClaim {
  1: optional string provider (omit = "zero")
  2: optional string program (omit = "zero")
  3: optional string exe (omit = "zero")
  4: optional string user (omit = "zero")
  5: optional i64 pid (omit = "zero")
  6: optional i64 uid (omit = "zero")
  7: optional i64 gid (omit = "zero")
} (unknown_fields = "refuse", doc="Untrusted claims copied from a native source row. These values are never verified OA identity.")
struct SourceRecord {
  1: required string source_id
  2: required Record record
  3: optional ProducerClaim producer (omit = "absent")
} (unknown_fields = "refuse", doc="One normalized native source entry. record.identity must be empty; native producer claims remain in producer, separate from authenticated OA callers.")
struct SourcePage {
  1: required HistoryPageOutcome outcome
  2: required list<SourceRecord> records
  3: required string next
  4: required bool at_end
} (unknown_fields = "refuse", doc="A bounded page from exactly one source. Limits count encoded SourceRecord bytes and entries. Cursors bind source ID and reader instance. at_end means the observed end during this call; later native entries may appear. A gap requires an explicit restart with empty cursor; refusal never advances a supplied cursor.")

service SourceReader {
  SourceCatalog Sources() (doc="List the runtime's fixed platform source identifiers and current access status. Source IDs are not paths or query expressions. Per-source OA rights are checked before native probing; a refused source is listed as permission_denied without a native access attempt.")
  SourcePage Read(1: string source_id, 2: string cursor, 3: i64 max_records, 4: i64 max_bytes) (doc="Read one fixed source with bounds of 1..256 entries and 1..65536 encoded SourceRecord bytes. The service checks the exact source-specific right before native access. A provider without a source returns unsupported; OA policy denial returns not_permitted, known native access denial returns forbidden and uncertainty returns unavailable. Invalid semantic bounds return invalid. Refusals contain no records, preserve the supplied cursor and never announce end. Empty cursor starts at the earliest visible retained entry the native API exposes. Cursors bind source ID and reader instance; source mismatch, restart or retention loss returns gap and preserves the cursor. No cross-source ordering, complete-capture claim or observation guarantee is made.")
  SourcePage StartAtEnd(1: string source_id) (doc="Return an opaque baseline cursor at the source's current end without returning records. The exact source-specific right is checked before native access and rechecked before the cursor is returned. A source without a safe current-position operation returns unsupported. Use the returned cursor with Read to request entries that follow this observed end; an empty source produces a valid empty baseline. If no native entry exists at the baseline, a later retention loss before the first read may be undetectable. Cursors bind source ID and reader instance; restart or detectable retention loss returns gap. This is a cursor operation, not a watch or complete-capture guarantee.")
} (wire_name = "abstraction.logging/source-reader@1", error_codes = "history_identity_error_codes", doc="Authorized bounded reads from fixed, read-only operating-system log sources.")
