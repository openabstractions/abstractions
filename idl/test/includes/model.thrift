// Regression specimen copied from openabstractions-flat/abstraction-model/model.thrift at ce03f298.
// Production evolution is verified separately; this fixture retains the original codec boundary.
namespace * abstraction.model.api

// Shared model concepts; native binding supplements are explicit below.
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
 10: trailing_bytes(stage="document")
}
struct Ref {
 1: required string registry
 2: required string repo
 3: required string revision
 4: required string quant
 5: required string file
}(document="true",unknown_fields="refuse",doc="Native model reference: hf uses repository, revision, optional quant or explicit file; ollama uses repository and tag revision. Empty revision means provider default. Custom registry schemes retain the opaque locator in repo. Parsing and provider validation remain native.")
const list<string> resolver_operations = ["Registry", "Resolve"]
const list<string> registry_operations = ["Add", "SetLocal", "Resolve"]
// Native Resolver.Resolve(context, Ref) returns the existing download.Spec.
// That native type is defined in download/go/spec.go; no duplicate model copy
// or opaque JSON replacement is defined here. A portable Spec descriptor and
// cross-definition type references remain pending. Context and provider composition
// are native supplements. Missing digest must refuse before download.
