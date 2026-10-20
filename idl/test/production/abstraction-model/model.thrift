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
 11: bad_value(stage="structure")
 12: trailing_bytes(stage="document")
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
 LookupResult Resolve(1: Ref ref (rust.name = "reference"))(readonly="true",result_present="request",result_when="outcome",result_is="resolved")
}(wire_name="abstraction.model/resolver@1",doc="Same-account identity-bound model lookup through explicitly configured registry providers. A resolved request contains a nonempty verified-format digest and a complete HTTP(S) mapping, anonymous or naming the [Ref](https://openabstractions.github.io/reference.html#concept-model.ref)'s credential. Providers refuse mappings needing capabilities absent from the portable download request. Local adapters and registry configuration remain explicit provider-side APIs.")

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
 ManifestResult Resolve(1: Ref ref (rust.name = "reference"))(readonly="true",result_present="manifest",result_when="outcome",result_is="resolved",doc="A registry reference resolves to one manifest of kind abstraction.model/descriptor@1 with entries by role and a descriptor with family, quant, format, architecture, size_label, finetune, base, source and role. Portable download requests per entry come from resolver@1. No bytes move. role marks a [component](https://openabstractions.github.io/reference.html#concept-model-identity.component) (MODEL-C3): weights for the servable artifact, or the closed vocabulary a storage inventory source's own classifier answers (projector, vae) for a part of one. A component's base then names the [family](https://openabstractions.github.io/reference.html#concept-model.family-identity) it belongs to when its own name derives one, in place of base's other meaning of the model a weights file was fine-tuned from.")
}(wire_name="abstraction.model/manifest@1",doc="Same-account identity-bound registry resolution to a storage manifest. The model service owns no bytes and walks no directories; stores and holders belong to the storage inventory.")

// abstraction.model/catalog@1: the release-curated model catalogue and its fit
// rule for this machine (research/first-run/DECISION.md §3 and §4; MODEL-K1,
// MODEL-K2, MODEL-K12). Prose says catalogue; identifiers say catalog
// (research/vocabulary/DECISION.md V-22).
enum CatalogUse {
 1: chat
 2: code
 3: vision
 4: embed
 5: image
 6: video
}(unknown="refuse",reader="act",doc="What a person or application wants a model for. Each use has its own ranked list and context length.")
enum Fit {
 1: fits
 2: slow
 3: no
}(unknown="grant",reader="display",doc="How an entry fits this machine by the fit rule: fits when its need is within the free pool, slow when it fits and its speed reference for this machine's class is under five tokens a second, no otherwise.")
enum Availability {
 1: download
 2: served
}(unknown="refuse",reader="act",doc="download: no runtime serves the entry and its file must be fetched. served: this runtime or a paired one already serves the entry's family for the use, with nothing to download; machine names which.")
enum FitOutcome {
 1: fitted
 2: invalid
 3: unavailable
 4: forbidden
}(unknown="refuse",reader="act")
struct FitEntry {
 1: required string title(length="1..64")
 2: required string family(length="1..128")
 3: required Fit fit
 4: required Availability availability
 5: required string machine(length="0..128")
 6: required i64 download_bytes
 7: required i64 need_bytes
 8: required i64 context
 9: required string license(length="0..128")
 10: required Ref source
 11: required string digest(length="0..71")
}(unknown_fields="grant",doc="One ranked catalogue entry for the asked use. title is the catalogue's display name. family is the model family a runtime's router names. need_bytes is size plus the KV cache at context plus the recorded startup residual plus 1 GiB (MODEL-K2's fit rule). The residual is a curated upper bound for the pinned engine and catalogue context/profile, excluding weights, the main/draft KV term and the separate 1 GiB margin; a missing private catalogue fact contributes zero and preserves the prior estimate. context is the use's context length in tokens (MODEL-K12). machine is empty for this computer and names the paired runtime that serves the entry otherwise; it is empty for download. download_bytes is 0 when served. license is an SPDX license expression. source and digest (sha256:<hex>) name the one file to fetch (MODEL-K4); a source is never a path.")
struct FitResult {
 1: required FitOutcome outcome
 2: required string catalog_version(length="0..32")
 3: required i64 pool_bytes
 4: required list<FitEntry> entries(length="0..64")
}(unknown_fields="grant",doc="fitted carries the catalogue version and every entry of the use in rank order, strongest first; the first entry that fits or is served is the pick. pool_bytes is what a new card:0 lease could take now without a yield: the lease book's free amount (RES-L2), net of measured use and the unoccupied part of every live lease. A card carved from memory, or one the instrument cannot read, takes the machine's memory pool. Every other outcome carries an empty version, a zero pool and no entries. invalid is a use the catalogue does not list; unavailable is a pool or offer read that could not answer; forbidden is a receiving boundary or policy refusal.")
service ModelCatalog {
 FitResult Fit(1: CatalogUse use (rust.name = "model_use"))(remote="local",readonly="true",doc="Rank the catalogue's entries for use against this machine and the families runtimes already serve. Reads abstraction.resource/table@1 and abstraction.router/router@1 Offer, which include paired runtimes. Reads no weight file, sends no request to a registry, submits no job and changes nothing.")
}(wire_name="abstraction.model/catalog@1",doc="The release-curated model catalogue, compiled into the runtime, and its fit rule. Same-account, and gated like router@1 Offer by abstraction.router/inventory.read on abstraction.router/inventory, since a served entry discloses what runtimes offer. Choosing an entry, downloading it and selecting it are separate public operations.")
