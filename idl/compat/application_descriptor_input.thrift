// Private file input profile: documented omitted guidance becomes empty.
// RPC ApplicationDescriptor remains its published required-field profile.
namespace * internal.descriptorinput
include "../../openabstractions-flat/abstraction-facade/facade.thrift"
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

struct DescriptorInput {
 1: required string name(length="0..8192")
 2: required string program(length="0..8192")
 3: required string title(length="0..8192")
 4: optional string start_guidance(omit="zero",length="0..8192")
 5: optional facade.ApplicationActivationRecipe activation(omit="absent")
}(document="true",unknown_fields="refuse")
