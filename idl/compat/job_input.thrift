// Private Go provider input profile. No service or public wire version.
namespace * internal.recordinput
include "../../openabstractions-flat/abstraction-job/job.thrift"
encoding json {
  escape         = "minimal"
  indent         = "2"
  map_keys       = "utf8-bytes"
  numbers        = "integer-decimal"
  opaque         = "reindent"
  terminator     = "newline"
  duplicate_keys = "refuse"
  depth_limit    = "64"
}

// The word a reader says when it refuses, in the order two of them are chosen
// between. The word is the contract; the byte offset beside it is not.
refusal {
   1: malformed        (stage = "grammar")
   2: bad_string       (stage = "grammar")
   3: number_spelling  (stage = "grammar")
   4: wrong_type       (stage = "grammar")
   5: bad_timestamp    (stage = "grammar")
   6: depth_exceeded   (stage = "grammar")
   7: duplicate_key    (stage = "grammar")
   8: duplicate_field  (stage = "structure")
   9: unknown_field    (stage = "structure")
  10: missing_field    (stage = "structure")
  11: trailing_bytes   (stage = "document")
}

// A discriminator only. Unknown values retain shared JSON grammar validation.
struct LegacyHeader {
 1: required i32 schema
}(unknown_fields="preserve")

// Unsubmitted input and historical records share this structural value shape.
// Schema selection, frozen feature mapping and Submit validation stay native.
struct InputRecord {
 1: optional i32 schema(omit="zero")
 2: optional list<string> content(omit="zero")
 3: optional list<string> critical(omit="zero")
 4: optional string id(omit="zero")
 5: optional string kind(omit="zero")
 6: optional job.Envelope envelope(omit="absent")
 7: optional string state(omit="zero")
 8: optional json spec(omit="absent")
 9: optional json checkpoint(omit="absent")
 10: optional job.Progress progress(omit="absent")
 11: optional job.Lease lease(omit="absent")
 12: optional job.Delegation delegation(omit="absent")
 13: optional list<string> requires(omit="zero")
 14: optional string error(omit="zero")
 15: optional job.Intent intent(omit="absent")
 16: optional map<string,json> extensions(omit="zero")
 17: optional job.timestamp created_at(omit="zero")
 18: optional job.timestamp updated_at(omit="zero")
}(document="true",unknown_fields="refuse")

struct Payload {
 1: required json value
}(unknown_fields="refuse")

struct TimestampValue {
 1: required job.timestamp value
}(unknown_fields="refuse")
