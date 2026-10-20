namespace * abstraction.sizes.api

// The R9 / gen --sizes cross-check's probe (idl/test_inventory.py
// test_r9_agrees_with_gen_sizes_on_every_target). It carries the shapes no
// target declares: a counted list of a grammar typedef, whose elements no
// grammar bounds; a counted list of records, each holding an unbounded
// string; and an unbounded method argument. It feeds no committed artefact.
encoding json {
 escape="minimal"
 indent="2"
 map_keys="utf8-bytes"
 numbers="integer-decimal"
 opaque="verbatim"
 terminator="newline"
 duplicate_keys="refuse"
 depth_limit="64"
}
refusal {
 1: malformed(stage="grammar")
 2: bad_string(stage="grammar")
 3: number_spelling(stage="grammar")
 4: wrong_type(stage="grammar")
 5: depth_exceeded(stage="grammar")
 6: duplicate_key(stage="grammar")
 7: duplicate_field(stage="structure")
 8: unknown_field(stage="structure")
 9: missing_field(stage="structure")
 10: bad_value(stage="structure")
 11: trailing_bytes(stage="document")
}
typedef string timestamp(write="rfc3339-micros",read="rfc3339-wide")
struct Record {
 1: required string name
}(unknown_fields="refuse")
struct Times {
 1: required list<timestamp> at(length="..4")
}(unknown_fields="refuse")
struct Records {
 1: required list<Record> items(length="..4")
}(document="true",unknown_fields="refuse")
service Probe {
 Times Read(1:string cursor)(readonly="true")
}(wire_name="abstraction.sizes/probe@1")
