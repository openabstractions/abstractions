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
} (unknown_fields = "grant")
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
service HistoryReader {
  Page Read(1: string cursor, 2: i64 max_records, 3: i64 max_bytes) (doc="Read retained records through the service. Empty cursor starts at earliest retained history. Limits are 1..256 records and 1..65536 encoded-record bytes; a first record that exceeds the byte limit gives record_too_large. A provider without history gives unavailable. Cursors may expire on restart or retention changes, reported as gap. Snapshot reads may be repeated at end; this method promises no subscription or persistence acknowledgement. The receiving service authorizes history access from native caller evidence.")
} (wire_name = "abstraction.logging/reader@1", doc="Authorized bounded access to retained logging history. Applications receive records and opaque cursors; history location and retention belong to the provider.")

service HistoryObserver {
  Page Observe(1: string cursor, 2: i64 max_records, 3: i64 max_bytes, 4: i64 wait_ms) (doc="Read with the same bounds and cursor semantics as HistoryReader. At an empty current end, wait up to wait_ms (0..30000) for a provider notification, then reread once; expiry may return an empty current-end page. Cancellation stops waiting and does not undo logging. Providers without observation return unsupported. Bounded waiter capacity exhaustion returns unavailable. Authorization is rechecked before records return. This is long-poll observation; external file writers have no notification promise.")
} (wire_name = "abstraction.logging/observer@1", doc="Authorized bounded long-poll history observation with provider-owned notifications.")
