namespace * abstraction.model.api
include "../abstraction-download/request.thrift"
include "../abstraction-storage/content.thrift"

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
 10: bad_enum(stage="structure")
 11: trailing_bytes(stage="document")
}
struct Ref {
 1: required string registry
 2: required string repo
 3: required string revision
 4: required string quant
 5: required string file
 6: optional string credential(omit="zero")
}(document="true",unknown_fields="refuse",doc="Native model reference: hf uses repository, revision, optional quant or explicit file; ollama uses repository and tag revision. Empty revision means provider default. Custom registry schemes retain the opaque locator in repo. Parsing and provider validation remain native. credential, when present, names a credential registered with abstraction.credentials/holder@1 in the caller's account. The resolver applies it through abstraction.credentials/applier@1 with consumer abstraction.model/resolver@1 to its registry metadata requests, and every source of the resolved request carries the same name.")
const list<string> resolver_operations = ["Registry", "Resolve"]
const list<string> registry_operations = ["Add", "SetLocal", "Resolve"]
// Registry mutation and explicit local/store adapters remain native provider APIs.
enum LookupOutcome {
 1: resolved
 2: invalid
 3: unavailable
 4: unsupported_mapping
 5: forbidden
}(unknown="refuse")
struct LookupResult {
 1: required LookupOutcome outcome
 2: optional request.Request request (omit="absent")
}(unknown_fields="refuse",doc="Request is present exactly when outcome is resolved. Unavailable reports an unsuccessful lookup without declaring it transient or permanent. Unsupported_mapping preserves the refusal to discard native source metadata, secret values, private locations or destination authority; a Ref credential the applier refuses also reads unsupported_mapping, and an applier that cannot answer reads unavailable. No job is submitted by lookup.")
service ModelResolver {
 LookupResult Resolve(1: Ref ref (rust.name = "reference"))
}(wire_name="abstraction.model/resolver@1",doc="Same-account identity-bound model lookup through explicitly configured registry providers. A resolved request contains a nonempty verified-format digest and a complete HTTP(S) mapping, anonymous or naming the Ref's credential. Providers refuse mappings needing capabilities absent from the portable download request. Local adapters and registry configuration remain explicit provider-side APIs.")

// abstraction.model/manifest@1: a registry reference resolves to a storage
// manifest with a model descriptor (research/content-references/PROPOSAL.md 5.1).
enum ManifestOutcome {
 1: resolved
 2: not_found
 3: invalid
 4: unavailable
 5: unsupported_mapping
 6: forbidden
}(unknown="refuse",reader="act")
struct ManifestResult {
 1: required ManifestOutcome outcome
 2: optional content.Manifest manifest (omit="absent")
 3: required string resolved_revision
}(unknown_fields="refuse",doc="manifest present exactly for resolved. The manifest is content-addressed: a registry answers with digests, and an entry the registry cannot name by digest makes the lookup unsupported_mapping. resolved_revision is the registry's immutable revision the reference resolved to, and empty for every other outcome.")
service ModelManifest {
 ManifestResult Resolve(1: Ref ref (rust.name = "reference"))(doc="A registry reference resolves to one manifest of kind abstraction.model/descriptor@1 with entries by role and a descriptor with family, quant, format, architecture, size_label, finetune, base and source. Portable download requests per entry come from resolver@1. No bytes move.")
}(wire_name="abstraction.model/manifest@1",doc="Same-account identity-bound registry resolution to a storage manifest. The model service owns no bytes and walks no directories; stores and holders belong to the storage inventory.")
