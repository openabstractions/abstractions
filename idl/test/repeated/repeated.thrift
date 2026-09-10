// The shapes job/job.thrift does not have, so that the profile is proved on
// more than the one definition it was written from.
//
// A repeated record [DEF-A9] and a string map [DEF-A10], nested inside each
// other, with a depth limit low enough that a document can exceed it inside a
// repeated element in five lines rather than sixty. Nothing here is a layer:
// it is the smallest definition that reaches every rule the two additions
// touch, and it feeds no committed artefact.

encoding json {
  escape         = "minimal"
  indent         = "2"
  map_keys       = "utf8-bytes"
  numbers        = "integer-decimal"
  opaque         = "verbatim"
  terminator     = "newline"
  duplicate_keys = "refuse"
  depth_limit    = "5"
}

refusal {
   1: malformed        (stage = "grammar")
   2: bad_string       (stage = "grammar")
   3: number_spelling  (stage = "grammar")
   4: wrong_type       (stage = "grammar")
   5: depth_exceeded   (stage = "grammar")
   6: duplicate_key    (stage = "grammar")
   7: duplicate_field  (stage = "structure")
   8: unknown_field    (stage = "structure")
   9: missing_field    (stage = "structure")
  10: trailing_bytes   (stage = "document")
}

struct Source {
  1: required string url
  2: optional i32 priority               (omit = "zero")
  3: optional map<string,string> headers (omit = "zero")
  4: optional json note                  (omit = "zero")
} (unknown_fields = "refuse")

struct Plan {
  1: required string id
  2: required list<Source> sources
  3: optional list<Source> mirrors       (omit = "zero")
  4: optional map<string,string> labels  (omit = "zero")
} (document = "true", unknown_fields = "refuse")
