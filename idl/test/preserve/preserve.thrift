// Test-only extension scopes. No production schema opts into preservation here.
encoding json {
  escape = "minimal"
  indent = "2"
  map_keys = "utf8-bytes"
  numbers = "integer-decimal"
  opaque = "verbatim"
  terminator = "newline"
  duplicate_keys = "refuse"
  depth_limit = "5"
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
 10: trailing_bytes (stage = "document")
}
struct Closed {
  1: required string fixed
} (unknown_fields = "refuse")
struct Dropped {
  1: required string fixed
} (unknown_fields = "grant")
struct Child {
  1: required string name
  2: optional i32 count (omit = "zero")
} (unknown_fields = "preserve")
struct Empty {
} (unknown_fields = "preserve")
struct Doc {
  1: required string id
  2: optional Child child (omit = "absent")
  3: optional list<Child> children (omit = "zero")
  4: optional Empty empty (omit = "absent")
  5: optional Closed closed (omit = "absent")
  6: optional Dropped dropped (omit = "absent")
} (document = "true", unknown_fields = "preserve")
