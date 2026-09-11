namespace * abstraction.logging

// Service-client record schema. The legacy storage codec is not replaced.
// Existing servers accept the Record payloads, not the new service envelope.
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
 11: trailing_bytes (stage = "document")
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
