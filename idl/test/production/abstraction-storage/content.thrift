namespace * abstraction.storage.content
// Additive service profile; storage.thrift retains direct provider interfaces.
encoding json { escape="minimal" indent="2" map_keys="utf8-bytes" numbers="integer-decimal" opaque="verbatim" terminator="newline" duplicate_keys="refuse" depth_limit="64" }
refusal {
 1: malformed(stage="grammar")
 2: bad_string(stage="grammar")
 3: number_spelling(stage="grammar")
 4: wrong_type(stage="grammar")
 5: bad_timestamp(stage="grammar")
 6: depth_exceeded(stage="grammar")
 7: duplicate_key(stage="grammar")
 8: duplicate_field(stage="structure")
 9: unknown_field(stage="structure")
 10: missing_field(stage="structure")
 11: bad_binary(stage="structure")
 12: bad_enum(stage="structure")
 13: trailing_bytes(stage="document")
}
typedef string timestamp(write="rfc3339-micros",read="rfc3339-wide")

enum Verification { 1: unverified }(unknown="refuse")
enum OpenOutcome { 1: opened 2: not_found 3: forbidden 4: invalid 5: unsupported 6: unavailable 7: exhausted }(unknown="refuse")
enum ReadOutcome { 1: data 2: gap 3: forbidden 4: invalid 5: unavailable 6: changed }(unknown="refuse")
enum CloseOutcome { 1: closed 2: gap 3: forbidden }(unknown="refuse")
struct Resource {
 1: required string handle
 2: required string digest
 3: required i64 size
 4: required Verification verification
}(unknown_fields="refuse",doc="Opaque resource bound to receiving account and observed program and provider lifetime. Digest is the requested canonical sha256 naming key, not a verified hash. Size is observed and nonnegative. Verification is always unverified; consumer verifies assembled bytes. No private path or immutable-snapshot claim.")
struct OpenResult {
 1: required OpenOutcome outcome
 2: optional Resource resource(omit="absent")
}(document="true",unknown_fields="refuse",doc="Resource present exactly for opened. Authorization precedes lookup. not_found means no known match, not global absence.")
struct Chunk {
 1: required i64 offset
 2: required i64 total
 3: required binary data
 4: required bool eof
}(unknown_fields="refuse",doc="Offset equals requested offset; total equals issued resource size. Length at most max_bytes and offset+length at most total. eof iff offset+length equals total. Non-EOF data is nonempty. Error/absence never means EOF.")
struct ReadResult {
 1: required ReadOutcome outcome
 2: optional Chunk chunk(omit="absent")
}(unknown_fields="refuse",doc="Chunk present exactly for data. changed reports observed mutation and invalidates resource; discard assembly and explicitly reopen. Mutation detection is advisory; verify completed bytes.")
struct CloseResult { 1: required CloseOutcome outcome }(unknown_fields="refuse")
service ContentReader {
 OpenResult Open(1:string digest)(doc="Canonical sha256: plus 64 lowercase hexadecimal digits. Explicit content policy required; same-account identity alone grants no content permission. At most 32 resources globally and 8 per scope; 30 seconds idle expires.")
 ReadResult Read(1:string handle,2:i64 offset,3:i64 max_bytes)(doc="offset>=0; max_bytes is 1..65536. Authority rechecked before bytes. Unknown/expired/closed/restarted resources give gap; another scope's known resource gives forbidden.")
 CloseResult Close(1:string handle)(doc="Release own scoped resource even after content authorization revocation. Provider shutdown closes all owned resources.")
}(wire_name="abstraction.storage/content-reader@1",doc="Read-only bounded access through configured native Store+Local adapters. Naming lookup is unverified; unsupported providers refuse. Writes use the separate content-writer profile. Callbacks are trusted provider code required to honor bounded execution/context.")

enum BeginOutcome { 1: started 2: committed 3: present 4: forbidden 5: invalid 6: conflict 7: too_large 8: busy 9: unsupported 10: unavailable 11: exhausted }(unknown="refuse")
enum AppendOutcome { 1: accepted 2: gap 3: forbidden 4: invalid 5: out_of_order 6: too_large 7: unavailable }(unknown="refuse")
enum CommitOutcome { 1: committed 2: gap 3: forbidden 4: incomplete 5: mismatch 6: unavailable }(unknown="refuse")
enum AbortOutcome { 1: aborted 2: gap 3: forbidden }(unknown="refuse")
// foreign is a digest in another algorithm the store spells, with its prefix
// (gitsha1:, blake3:); none is an object known by size and locator only. The
// writer's Stored emits only hashed and named.
enum Evidence { 1: hashed 2: named 3: foreign 4: none }(unknown="refuse")
struct Upload {
 1: required string handle
 2: required string digest
 3: required i64 size
 4: required i64 received
}(unknown_fields="refuse",doc="Opaque staged upload bound to the receiving account/program scope and provider lifetime. Size is the declared total; received counts bytes accepted in order. Staged bytes are never findable or readable.")
struct Stored {
 1: required string digest
 2: required i64 size
 3: required Evidence evidence
}(unknown_fields="refuse",doc="hashed: the service hashed every byte it accepted into staging, the hash equals digest, and the provider committed that staged object. named: an existing provider naming match was found without hashing; size zero means unknown.")
struct BeginResult {
 1: required BeginOutcome outcome
 2: optional Upload upload(omit="absent")
 3: optional Stored stored(omit="absent")
 4: required i64 limit
}(unknown_fields="refuse",doc="upload present exactly for started. stored present exactly for committed and present. limit is the provider's maximum declared size for evaluated outcomes, and zero for forbidden, invalid and unavailable.")
struct AppendResult {
 1: required AppendOutcome outcome
 2: required i64 received
}(unknown_fields="refuse",doc="For accepted, out_of_order and too_large, received is the next offset the service accepts. Other outcomes carry zero.")
struct CommitResult {
 1: required CommitOutcome outcome
 2: optional Stored stored(omit="absent")
 3: required i64 received
}(unknown_fields="refuse",doc="stored present exactly for committed with hashed evidence. For incomplete, received is the next accepted offset. Other outcomes carry zero.")
struct AbortResult { 1: required AbortOutcome outcome }(unknown_fields="refuse")
service ContentWriter {
 BeginResult Begin(1:string request,2:string digest,3:i64 size)(doc="request is a caller-retained identity of 16..128 characters from A-Z a-z 0-9 _ -, scoped to the receiving account/program. digest is canonical sha256; size is the declared total, 0..limit. Write authorization precedes every lookup and provider effect. The same request with the same digest and size returns its live upload or committed result; different digest or size is conflict. present reports an existing naming match. busy reports another live upload of the same digest. At most 16 uploads globally, 4 per scope and 256 request records.")
 AppendResult Append(1:string handle,2:i64 offset,3:binary data)(doc="data is 1..65536 bytes at offset equal to received. Authority rechecked before bytes are staged. A different offset stages nothing and returns out_of_order with received. Bytes beyond the declared size stage nothing and return too_large. Unknown/expired/aborted/restarted uploads give gap; another scope's upload gives forbidden.")
 CommitResult Commit(1:string handle)(doc="Authority rechecked before visibility. Requires received equal to size and the staged bytes to hash to digest; mismatch discards the upload. Visibility changes in one provider commit step; readers observe no partial content.")
 AbortResult Abort(1:string handle)(doc="Discard own staged upload and its request record, including after write authorization revocation.")
}(wire_name="abstraction.storage/content-writer@1",doc="Bounded authorized writes through configured native Store+Local+Writable adapters. Uploads idle for 30 seconds expire and are discarded. Committed request records are retained for 10 minutes within one provider lifetime. Provider shutdown discards staged uploads.")

enum ChangeKind { 1: added 2: removed }(unknown="refuse")
enum ChangePageOutcome { 1: page 2: gap 3: forbidden 4: invalid 5: unavailable }(unknown="refuse")
enum ListingOutcome { 1: page 2: gap 3: forbidden 4: invalid 5: unavailable }(unknown="refuse")
struct Change {
 1: required i64 sequence
 2: required ChangeKind kind
 3: required string digest
 4: required i64 size
}(unknown_fields="refuse",doc="One observed change in provider journal order. Sequence increases within one provider epoch. Digest is a canonical sha256 naming key, not verified content. Size is observed; zero means unknown. A notice grants no access.")
struct ChangePage {
 1: required ChangePageOutcome outcome
 2: required list<Change> changes
 3: required string next
 4: required bool at_end
}(unknown_fields="refuse",doc="page carries at most max_changes entries the caller may read and a next cursor. next advances past every entry examined, including entries the caller may not read, which are omitted without a count. at_end means the journal end was reached during this call. Refusals carry no changes, an unchanged cursor and at_end false. gap requires restarting from List.")
struct ListedObject {
 1: required string digest
 2: required i64 size
}(unknown_fields="refuse")
struct ListingPage {
 1: required ListingOutcome outcome
 2: required list<ListedObject> objects
 3: required string continuation
 4: required bool complete
 5: required string cursor
}(unknown_fields="refuse",doc="page carries at most limit objects the caller may read, in digest order, from one frozen snapshot. cursor is the change cursor at which that snapshot was taken and is identical on every page of it; Observe from it reports every later change. complete means the snapshot is exhausted; otherwise continuation is nonempty. Objects the caller may not read are omitted without a count. Refusals carry no objects, empty continuation and cursor, and complete false.")
service ContentChanges {
 ChangePage Observe(1:string cursor,2:i64 max_changes,3:i64 wait_ms)(doc="Empty cursor starts at the current journal end. Cursors are at most 256 bytes and bind provider epoch and sequence. A cursor from another epoch or older than the retained journal returns gap. max_changes is 1..256; wait_ms is 0..30000 and waits at the current end for an append, then rereads once. Observe authorization is checked on every call and rechecked after a wait; each entry is filtered through the read policy for its digest. A read decision outage returns unavailable without advancing. Bounded waiter exhaustion returns unavailable.")
 ListingPage List(1:string continuation,2:i64 limit)(doc="Empty continuation freezes a new snapshot of the provider's known objects and its change cursor. limit is 1..256. Continuations are at most 256 bytes and name a retained snapshot; an unknown, expired or restarted snapshot returns gap. At most 8 snapshots are retained, each for 30 idle seconds; exhaustion returns unavailable. Observe authorization and per-object read filtering apply as for Observe.")
}(wire_name="abstraction.storage/content-changes@1",doc="Bounded authorized observation of objects a content store gains or loses. The provider journal retains a bounded number of recent changes per lifetime with no per-subscriber queue; a subscriber that falls behind receives gap and rebuilds from List. Service-mediated commits are journaled when they publish. External additions and deletions are journaled when the provider's optional listing capability is polled; changes that cancel out between polls are not reported. removed currently reports only external deletions, because the service has no delete operation. A provider restart starts a new epoch.")

// Manifests, holds, inventory, remover and inventory-source profiles
// (research/content-references/PROPOSAL.md sections 5 and 10). Placement,
// Store.domain, Holder.domain, the lease lifetime and the unavailable outcomes
// carry the remote boundary (section 5.7); remote composition adds no field.
enum Addressing { 1: content 2: name }(unknown="refuse",reader="act")
enum Placement { 1: local 2: remote }(unknown="refuse",reader="act")
enum Lifetime { 1: until_released 2: lease 3: while_present }(unknown="refuse",reader="act")
enum Attestation { 1: declared 2: observed 3: verified }(unknown="grant",reader="display")
enum StoreErrorKind { 1: other 2: malformed_index 3: unreadable_index 4: unreadable_tree 5: unreadable_configuration }(unknown="grant",reader="display")

struct Object {
 1: required string locator
 2: required i64 size
 3: required timestamp modified
 4: required bool partial
 5: required string digest
 6: required Evidence evidence
}(unknown_fields="refuse",doc="One stored object, by stat only. locator is opaque provider binding data, as storage Ref.locator; applications never treat it as a path or as authority. partial marks an interrupted transfer the store spells as such. digest is empty exactly when evidence is none.")
struct Entry {
 1: required string role
 2: required string digest
 3: required i64 size
 4: required string media_type
 5: required Evidence evidence
 6: required string locator
 7: optional string manifest(omit="zero")
 8: optional map<string,string> annotations(omit="zero")
}(unknown_fields="refuse",doc="One object of a manifest. role is an open catalogue <owner>/<name>; seeds weights, weights.config, template, params, license, projector, doc, code, dataset. locator names the object in its store and is excluded from the manifest id. manifest, present only in a manifest of kind abstraction.storage/index@1, references another manifest by id in place of an object. digest is empty exactly when evidence is none.")
struct Name { 1: required string scheme 2: required string name }(unknown_fields="refuse",doc="A name a program uses for this manifest, in that program's own vocabulary. scheme is an open catalogue; seeds ollama, hf, lmstudio, comfyui, flm, jan, oci, stabilitymatrix, pinokio, swarmui.")
struct Manifest {
 1: required string id
 2: required Addressing addressing
 3: required string kind
 4: required list<Entry> entries
 5: required list<Name> names
 6: optional map<string,string> descriptor(omit="zero")
 7: required string store
 8: optional string superseded_by(omit="zero")
}(unknown_fields="refuse",doc="addressing is content when every entry carries a hashed or named digest: id is the sha256 of the canonical encoding of kind, the entries' role, digest, size and media_type in declaration order, and descriptor; names, locators, annotations and store are excluded, so one manifest is known under several names in several stores. addressing is name otherwise: id is the sha256 of the canonical encoding of kind, the entries' role, size and media_type, descriptor, and names[0], which is the store's own name for it; two digest-less manifests differ by what their programs call them. When digests later arrive, a content-addressed manifest supersedes the name-addressed one and superseded_by records the new id on the old record. Canonical encoding is this definition's own encoding with indent 0. kind is an open catalogue; seeds abstraction.model/descriptor@1, abstraction.storage/snapshot@1, abstraction.storage/object@1, abstraction.storage/index@1. descriptor is a string map whose keys the kind defines.")

struct Holder {
 1: required string account
 2: required string program
 3: required string instance
 4: required string established_by
 5: required string domain
}(unknown_fields="refuse",doc="domain is the trust domain in which the holder was bound: empty for this service's own local boundary, otherwise the scope this service mapped a remote peer's certificate identity to, as the remote job provider maps keys to namespaces. A relayed hold keeps the domain of the boundary that established it; no domain inherits another's proofs. program is the holder's name in the holder vocabulary (ollama, lemonade, comfyui). instance distinguishes installations of one program: for declared holds the bound peer's normalized executable identity as rights Subject.program spells it; for observed holds the source's opaque identity for that installation (a Stability Matrix package name, a Pinokio app, an install locator). For declared holds account and instance are the receiving boundary's bound peer, asserted by the service; for observed holds they are the source's words and established_by is the source program the service bound. Never a caller-supplied claim.")
struct Basis {
 1: required string index
 2: required string key
 3: optional string role(omit="zero")
}(unknown_fields="refuse",doc="The index entry that proves an observed hold: index is the opaque locator of the program's own index (a manifest file, a registry, a configuration), key is the entry inside it in the program's own words, role is the program's role word for the target inside that entry (Lemonade main, mmproj, draft, npu_cache). Verify re-reads this. Holds sharing holder, index and key are one grouping in the holder's terms. Absent on declared holds, whose basis is the retained request identity.")
struct Hold {
 1: required string id
 2: required Holder holder
 3: required string target
 4: required string purpose
 5: required Lifetime lifetime
 6: optional timestamp expires(omit="absent")
 7: required Attestation attestation
 8: required timestamp observed_at
 9: optional Basis basis(omit="absent")
}(unknown_fields="refuse",doc="target is a manifest id or a canonical digest. purpose is an open catalogue; seeds fetched, installed, pinned, cache. expires is present exactly for lease. basis is present exactly for observed and verified holds. A hold designates dependence; it grants no access to bytes.")
struct Dangling {
 1: required Holder holder
 2: required Basis basis
 3: required string reference
 4: optional string expected_digest(omit="zero")
 5: optional i64 expected_size(omit="zero")
 6: required timestamp observed_at
}(unknown_fields="refuse",doc="An index entry naming content the store does not hold, in the program's own reference spelling. expected_digest and expected_size are what the index itself spells for the absent content (a download manifest's sha256), a claim by the holder and never evidence about any present object. A dangling reference is a want: the download request that would satisfy it has this digest and size.")
struct StoreError {
 1: required StoreErrorKind kind
 2: required string locator
 3: required string detail
}(unknown_fields="refuse",doc="One failure inside one store. A store with errors still reports everything else it read. kind is display-grade; other carries an unlisted cause.")
struct Store {
 1: required string name
 2: required string program
 3: required string locator
 4: required string rule
 5: required bool present
 6: required list<StoreError> errors
 7: required Placement placement
 8: required string domain
}(unknown_fields="refuse",doc="placement is local when the store is read by a source bound on this service's own boundary, and remote when its records arrive from a remote service over the service-owned authenticated transport; domain is that remote service's mapped scope, empty for local. A remote store's locators are opaque here, and a shared-filesystem view of another machine's store is never a store. One store a source reads. locator is the store's opaque provider binding. rule says how the store was found, in the source's own words (a default location, an environment variable, a program's configuration, the operator). present false means the discovery rule named a store that does not exist; present true with no objects is an empty store. A source with no store for a program reports nothing for it.")

enum HoldOutcome { 1: held 2: unknown_target 3: conflict 4: forbidden 5: invalid 6: exhausted 7: unavailable }(unknown="refuse",reader="act")
struct HoldResult { 1: required HoldOutcome outcome 2: optional Hold hold(omit="absent") }(unknown_fields="refuse",doc="hold present exactly for held.")
enum ReleaseOutcome { 1: released 2: unknown 3: forbidden 4: invalid 5: unavailable }(unknown="refuse",reader="act")
struct ReleaseResult { 1: required ReleaseOutcome outcome }(unknown_fields="refuse")
enum RenewOutcome { 1: renewed 2: unknown 3: expired 4: forbidden 5: invalid 6: unavailable }(unknown="refuse",reader="act")
struct RenewResult { 1: required RenewOutcome outcome 2: optional Hold hold(omit="absent") }(unknown_fields="refuse",doc="hold present exactly for renewed.")
service Holds {
 HoldResult Hold(1:string request,2:string target,3:string purpose,4:Lifetime lifetime,5:i64 lease_ms)(doc="request is caller-retained, 16..128 characters from A-Z a-z 0-9 _ -, scoped to the receiving account and program; the same request with the same arguments returns the existing hold, different arguments conflict. Only declared holds are created here; the holder is the bound peer. lease_ms is 1..2592000000 for lease and zero otherwise. At most 4096 holds per program; exhausted otherwise.")
 ReleaseResult Release(1:string hold)(doc="Release an own declared hold. Another program's hold, a released hold past retention and an unknown id are unknown.")
 RenewResult Renew(1:string hold,2:i64 lease_ms)(doc="Extend an own lease by lease_ms from the service's time. An expired lease is expired and is not revived.")
}(wire_name="abstraction.storage/holds@1",doc="Declared holds of the bound program. Gated by abstraction.storage/holds.manage, whose contract default permits the bound program on resource self and nothing else. Holds are retained until released, expired, or the program is removed under the uninstall policy; released and expired holds read unknown after audit_retention_ms, readable on an inventory page.")

enum DescribeOutcome { 1: described 2: unknown 3: forbidden 4: invalid 5: unavailable }(unknown="refuse",reader="act")
struct DescribeResult { 1: required DescribeOutcome outcome 2: optional Manifest manifest(omit="absent") }(unknown_fields="refuse",doc="manifest present exactly for described.")
enum PublishOutcome { 1: published 2: present 3: incomplete 4: conflict 5: forbidden 6: invalid 7: exhausted 8: unavailable }(unknown="refuse",reader="act")
struct PublishResult {
 1: required PublishOutcome outcome
 2: optional Manifest manifest(omit="absent")
 3: required list<string> missing
}(unknown_fields="refuse",doc="manifest present exactly for published and present. missing lists the digests of entries the service cannot find; it is empty for every outcome except incomplete.")
service Manifests {
 DescribeResult Describe(1:string id)
 PublishResult Publish(1:string request,2:Manifest manifest)(doc="Record a content-addressed manifest whose hashed or named entries are all findable, or an index manifest whose referenced manifests are all known. request is a caller-retained identity as in Holds. present reports an identical manifest already recorded. The publishing program receives a declared hold with purpose fetched and a 24-hour lease; it upgrades it through Holds. Name-addressed manifests are published only by designated sources.")
}(wire_name="abstraction.storage/manifests@1",doc="Manifests the service knows: published by programs, imported from designated sources, or created by delivery. Gated by abstraction.storage/holds.manage for Publish and by abstraction.storage/inventory.read for Describe of another program's manifest.")

struct ManifestHolders { 1: required Manifest manifest 2: required list<Hold> holds }(unknown_fields="refuse")
struct ObjectHolders { 1: required Object object 2: required string store 3: required list<Hold> holds }(unknown_fields="refuse",doc="An object no manifest entry names, with any holds on its digest.")
enum InventoryOutcome { 1: page 2: gap 3: forbidden 4: invalid 5: unavailable }(unknown="refuse",reader="act")
struct InventoryPage {
 1: required InventoryOutcome outcome
 2: required list<Store> stores
 3: required list<ManifestHolders> manifests
 4: required list<ObjectHolders> objects
 5: required list<Dangling> dangling
 6: required string continuation
 7: required bool complete
 8: required string cursor
 9: required i64 grace_ms
 10: required i64 audit_retention_ms
}(unknown_fields="refuse",doc="One frozen snapshot across every configured store and designated source; cursor is the content-changes cursor at which it was taken. stores is complete on the first page and empty afterwards. objects carries only objects no manifest entry names. Manifests and objects the caller may not read are omitted without a count. Observed holds carry their observation time and basis, never a promise of current truth. Refusals carry empty lists, empty continuation and cursor, and complete false.")
service Inventory {
 InventoryPage List(1:string continuation,2:i64 limit)(doc="limit 1..256 manifests plus objects per page. Gated by abstraction.storage/inventory.read; each manifest and object is filtered through content.read for its digests.")
 InventoryPage Holders(1:string target)(doc="Every hold on one manifest id or digest, re-observed at call time through each source's Verify: observed holds return as verified or are omitted; declared holds return as declared. Dangling references naming the target's names are included.")
 InventoryPage Unheld(1:string continuation,2:i64 limit)(doc="Manifests and objects with no declared hold and no verified observed hold at two observations at least grace_ms apart, oldest first.")
 InventoryPage Find(1:string digest)(doc="Manifests and objects carrying this canonical digest with evidence hashed or named, and dangling references whose expected_digest equals it.")
}(wire_name="abstraction.storage/inventory@1",doc="What the machine holds and who depends on it, composed from the service's own tables and every designated source.")

enum RemoveOutcome { 1: removed 2: not_found 3: held 4: busy 5: unsupported 6: forbidden 7: invalid 8: unavailable }(unknown="refuse",reader="act")
struct RemoveResult { 1: required RemoveOutcome outcome 2: required list<Holder> holders }(unknown_fields="refuse",doc="held carries the holders whose declared or verified holds prevented removal; other outcomes carry none.")
service ContentRemover {
 RemoveResult Remove(1:string target)(doc="Remove an unheld manifest or object from a Removable store, journaled as removed. Gated by abstraction.storage/content.remove on resource store:<name>. A target only observed holds ever named is removable after a recorded asks answer. A target in a store whose source does not list remove among its capabilities is unsupported.")
}(wire_name="abstraction.storage/content-remover@1")

// Provider side, served by inventoryd and by any third-party source, read by the runtime.
const list<string> inventory_source_error_codes = ["caller_unavailable", "identity_required"]
enum SourceDescriptionOutcome { 1: described 2: forbidden 3: invalid 4: unavailable }(unknown="refuse",reader="act")
struct SourceDescription {
 1: required SourceDescriptionOutcome outcome
 2: required string name
 3: required list<Store> stores
 4: required list<string> schemes
 5: required list<string> capabilities
}(unknown_fields="refuse",doc="Refusals carry an empty name and empty lists. capabilities is an open list; seeds snapshot, observe, verify, remove. stores carries every store the source's discovery rules produced, present or not, with discovery errors.")
enum SourceChangeKind { 1: added 2: removed 3: held 4: released 5: store_changed }(unknown="refuse",reader="act")
struct SourceChange {
 1: required i64 sequence
 2: required SourceChangeKind kind
 3: required string target
 4: optional Hold hold(omit="absent")
 5: optional Store store(omit="absent")
}(unknown_fields="refuse",doc="target is a manifest id, an object locator or a store name. hold present exactly for held and released; store present exactly for store_changed.")
enum SourcePageOutcome { 1: page 2: gap 3: forbidden 4: invalid 5: unavailable }(unknown="refuse",reader="act")
struct SourcePage {
 1: required SourcePageOutcome outcome
 2: required list<Store> stores
 3: required list<ManifestHolders> manifests
 4: required list<ObjectHolders> objects
 5: required list<Dangling> dangling
 6: required list<SourceChange> changes
 7: required string continuation
 8: required bool complete
 9: required string cursor
}(unknown_fields="refuse",doc="Snapshot pages carry stores on the first page, then manifests, stray objects and dangling references, and an empty changes list; Observe pages carry changes only. Every hold a source reports has attestation observed and a basis; the runtime rewrites established_by from the bound source program and refuses any other value. A store's errors travel on its Store record; a page never fails for one store's error.")
service InventorySource {
 SourceDescription Describe()
 SourcePage Snapshot(1:string continuation,2:i64 limit)(doc="Frozen listing of every manifest, stray object and dangling reference the source observes, in store then id order. limit 1..256. No hashing; sizes come from stat and digests only from the program's own index or names.")
 SourcePage Observe(1:string cursor,2:i64 max_changes,3:i64 wait_ms)(doc="Changes since cursor in journal order; empty cursor starts at the end; gap after restart or an aged cursor. max_changes 1..256; wait_ms 0..30000.")
 SourcePage Verify(1:string target)(doc="Re-read one target's holders and dangling references now, through their bases.")
 RemoveResult Remove(1:string target)(doc="Only when capabilities lists remove: remove through the owning program's own semantics and update its index. Otherwise unsupported.")
}(wire_name="abstraction.storage/inventory-source@1",error_codes="inventory_source_error_codes",doc="A provider the runtime reads. The source listens on the shared identity-bound transport and serves only its own account. A call whose peer identity could not be obtained fails with the error code caller_unavailable, and one on a transport that bound no identity with identity_required; a bound peer of another account reads forbidden. The runtime accepts a source only under an abstraction.storage/inventory.provide rule for the bound program and the named stores. Sources never talk to applications.")
