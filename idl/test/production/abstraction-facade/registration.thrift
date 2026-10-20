namespace * abstraction.facade.registration
include "facade.thrift"
include "appmodel.thrift"
include "../abstraction-download/request.thrift"
include "../abstraction-rights/rights.thrift"
include "../abstraction-config/config.thrift"

// PRIVATE COMPLETE-SHAPE DRAFT. Runtime migration is not implemented here.
// Higher Facade module only; published facade.thrift/go-core stays unchanged.
encoding json {
 escape="minimal"
 indent="2"
 map_keys="utf8-bytes"
 numbers="integer-decimal"
 opaque="verbatim"
 terminator="newline"
 duplicate_keys="refuse"
 depth_limit="1000"
}
refusal {
 1: malformed(stage="grammar")
 2: bad_string(stage="grammar")
 3: number_spelling(stage="grammar")
 4: bad_timestamp(stage="grammar")
 5: wrong_type(stage="grammar")
 6: depth_exceeded(stage="grammar")
 7: duplicate_key(stage="structure")
 8: duplicate_field(stage="structure")
 9: unknown_field(stage="structure")
 10: missing_field(stage="structure")
 11: bad_enum(stage="structure")
 12: bad_value(stage="structure")
 13: encoded_size(stage="structure")
 14: trailing_bytes(stage="document")
}

typedef string timestamp(write="rfc3339-micros",read="rfc3339-wide")
typedef string Name(length="1..64",pattern="no_control")
typedef string Revision(length="0..256",pattern="no_control")
typedef string ModelName(length="1..256",pattern="no_control")
typedef string Modality(length="1..128",pattern="no_control")
typedef string ServiceName(length="1..128",pattern="no_control")
typedef string ResourceName(length="1..256",pattern="no_control")
typedef string Argument(length="1..4096",pattern="no_control")
typedef string Reason(length="0..256",pattern="no_control")
typedef string CheckKind(length="1..128",pattern="no_control")
typedef string DisplayKey(length="1..64",pattern="no_control")
typedef string DisplayValue(length="0..256")

enum Source {
 1: installation
 2: operator
 3: self_registration
 4: product_record
 5: built_in
}(unknown="refuse",reader="act")
enum State {
 1: pending_approval
 2: enabled
 3: disabled
}(unknown="refuse",reader="act")
enum Verification {
 1: verified
 2: missing
 3: changed
 4: unverified
}(unknown="refuse",reader="act",doc="Runtime probe observation separate from lifecycle intent. Verified means present and matching recorded identity/publisher; missing means absent; changed means a different image/signer; unverified means no platform evidence.")
enum Placement {
 1: local
 2: hosted
 3: any
}(unknown="refuse",reader="act",doc="Same wire values and meanings as the existing Manifest router.Placement. Local shared owner avoids a router/facade include cycle.")
enum Activation {
 1: on_demand
 2: attach
}(unknown="refuse",reader="act")
enum IdentityEvidence {
 1: none
 2: program
 3: remote
}(unknown="refuse",reader="act",doc="Observed identity category, not a new proof ladder. HTTP model servers without native program proof report none; program and remote require independently established subject/key evidence.")
enum FacetKind {
 1: provider
 2: host
 3: remote
 4: application
}(unknown="refuse",reader="act",doc="The host wire spelling remains unchanged; prose calls it a model server.")
enum ProducerCheckState {
 1: current
 2: repaired
 3: stale
 4: refused
 5: unverified
}(unknown="refuse",reader="act")
struct ProducerCheck {
 1: required CheckKind kind
 2: required string copy(length="0..256",pattern="no_control")
 3: required ProducerCheckState state
 4: required Reason reason
 5: required timestamp checked
}(unknown_fields="refuse",doc="Producer assertion validated by the runtime. Kind is the governed open build-time vocabulary <owner>/<name>, not a display string or an authority claim. copy identifies one copy within the kind. reason follows D87 domain:word:detail as open display text. checked is the producer's claimed check time. Custom/unknown copies are unverified; readiness remains independent. No runtime kind-registration method is introduced.")
struct ServiceState {
 1: required ServiceName service
 2: required facade.ServiceReadiness readiness
 3: required Reason reason
 4: required list<ServiceName> guarantees(length="0..64")
 5: required map<DisplayKey,DisplayValue> capabilities(length="0..32")
 6: required string definition_hash(length="71..71",pattern="no_control")
}(unknown_fields="grant",doc="endpoint@2 service observation. definition_hash is sha256: plus64 lowercase hex over this service version's canonical wire form (CONV-V2), verified by the definition-hash owner. reason retains readiness errors and is empty when ready. capabilities remain display-only. Program-level producer checks belong to Description and are independent of service readiness.")
enum DescriptionOutcome {
 1: described
 2: forbidden
 3: unavailable
 4: invalid
 5: unsupported
}(unknown="refuse",reader="act")
struct Description {
 1: required DescriptionOutcome outcome
 2: required string program(length="0..256")
 3: required string version(length="0..256")
 4: required list<ServiceState> services(length="0..64")
 5: optional rights.RefusalDetail refusal(omit="absent")
 6: required Reason reason
 7: optional list<ProducerCheck> checks(omit="zero",length="0..64")
}(max_encoded="1048576",unknown_fields="grant",doc="Complete endpoint@2 description with each advertised service's definition hash and one program-level producer-check list. Checks are independent of service readiness; absence means unobserved, never verified. Forbidden/unavailable authority refusals carry the single Rights-owned RefusalDetail; other outcomes omit it. Unsupported carries the existing bounded reason with D87 unsupported:not_implemented words; no closed staged-reason vocabulary is frozen. Non-described replies disclose no program/version/services/checks. Endpoint@1 and its closed Description/ServiceState remain unchanged. The compact value ceiling excluding insignificant JSON whitespace is IPC-F1; an oversized whole reply is unavailable/bound:reply_bytes without truncation.")
service EndpointV2 {
 Description Describe()(remote="see",readonly="true",doc="Read readiness, definition hashes and optional producer-check observations. Starts, verifies, repairs and grants nothing. Unavailable producer checks stay unverified; a supervised exit may establish refusal but never successful verification.")
}(wire_name="abstraction.facade/endpoint@2",doc="Typed successor description. The complete shape is drafted; automatic dispatcher/runtime support remains pending implementation and review.")

// Registry owns the declared engine assertion; Router owns its live projection
// with independently supplied source provenance. These are intentionally unequal.
struct EngineFact {
 1: required Name id
 2: required string name(length="1..256")
 3: optional string version(omit="zero",length="0..256")
}(unknown_fields="refuse",doc="Provider assertion about its selected engine. Runtime supplies registration provenance when projecting to router.Engine. No general format acceptance, executable path, readiness or grant is asserted.")
struct RecipeObject {
 1: required string role(length="1..64",pattern="no_control")
 2: required string digest(length="0..135",pattern="no_control")
 3: required string store(length="0..256",pattern="no_control")
 4: required string object(length="0..1024",pattern="no_control")
}(unknown_fields="refuse",doc="Exactly one identity alternative: canonical algorithm:lowercase-hex digest, or both nonempty store and opaque object. Reject bare/uppercase digest and partial pairs before lookup, as Target's exactly-one precedent. Every listed object is required. Digest association requires hashed storage evidence; a filename/family alone proves no association.")
struct ModelRecipe {
 1: required ModelName model
 2: required Name engine
 3: required list<Modality> modalities(length="1..16")
 4: required list<RecipeObject> objects(length="1..16")
}(unknown_fields="refuse",doc="Exact configured model/engine association. Engine references a declared EngineFact and model/modalities stay within this facet's explicit allowlist. All companions must be identified before router projection. No optional companion or capability inference.")
struct ScopedModel {
 1: required Modality modality
 2: required ModelName model
}(unknown_fields="refuse",doc="A typed modality/model pair. Colons in model names are literal and need no escaping; old profile:model packing exists only in published compatibility projections.")
struct RequiredExtension {
 1: required Modality modality
 2: required string key(length="1..128",pattern="no_control")
}(unknown_fields="refuse")
struct ProviderFacet {
 1: required Name name
 2: required list<Argument> arguments(length="0..64")
 3: required string endpoint(length="1..64",pattern="no_control")
 4: required list<ServiceName> services(length="1..16")
 5: required list<ServiceName> guarantees(length="0..64")
 6: required Activation activation
 7: required list<ResourceName> resources(length="0..64")
 8: required list<ScopedModel> models(length="0..64")
 9: required list<RequiredExtension> required_extensions(length="0..64")
 10: required list<EngineFact> engines(length="0..64")
 11: required list<ModelRecipe> recipes(length="0..64")
 12: optional list<config.ConfigKey> config_keys(omit="zero",length="0..64")
}(max_encoded="262144",unknown_fields="refuse",doc="Native oa-native@1 provider facet. Literal arguments retain {endpoint} substitution only. Modalities/models/extensions are validated as one registration epoch. Its producer owns argument interpretation and recipe selection; runtime projects typed facts without importing provider code. Activation uses the existing registry on_demand/attach modes; remote facets use their separate record. config_keys declares unique exact configuration catalogue keys read by this provider. At Keep, the native owner captures that list with held-image FileKey and program evidence and writes only its exact key.read rules; Remove retires those rules. A declaration alone grants nothing, and a read must recheck the actual native peer against the kept image.")
struct ServerFacet {
 1: required Name name
 2: required string base(length="1..4096",pattern="no_control")
 3: required string kind(length="1..128",pattern="no_control")
 4: required bool hosted
 5: required list<Modality> modalities(length="1..16")
 6: required string credential(length="0..64",pattern="no_control")
 7: optional facade.DeclarationCeiling ceiling(omit="absent")
}(unknown_fields="refuse",doc="Foreign HTTP model server. Native program proof is absent and identity_evidence is none. Existing URL, credential-host and ceiling owners validate configuration. Local servers carry no credential or ceiling. The host facet wire spelling is retained.")
struct RemoteFacet {
 1: required Name name
 2: required string endpoint(length="1..4096",pattern="no_control")
 3: required string server_name(length="1..253",pattern="no_control")
 4: required string server_key_sha256(length="64..64",pattern="no_control")
 5: required string credential(length="0..64",pattern="no_control")
 6: required list<Modality> modalities(length="1..16")
}(unknown_fields="refuse",doc="Paired remote facet. Pin is64 lowercase hex SPKI SHA256; endpoint/server name use existing C-69 validators. Runtime pairing owns its client machine key. No PEM roots, certificate or key paths travel here. Remote subject key is remote:<spki>; no caller/process authority is inferred from configuration.")

typedef string HostArgument(length="1..4096",pattern="no_control")
typedef string NetworkHost(length="1..253",pattern="no_control")
typedef string FeatureName(length="1..128",pattern="no_control")
typedef string InstanceName(length="1..128",pattern="no_control")
struct HostRecipe {
 1: required list<HostArgument> arguments(length="0..64")
 2: required i32 readiness_deadline_ms
}(unknown_fields="refuse",doc="Start the held manifest-owning image with at most64 literal arguments of1..4096 bytes and a100..30000ms readiness budget. Runtime holds private stdio using the existing OA length-framed carrier with MCP JSON-RPC bodies. No payload becomes argv. Caller cancellation ends its wait; accepted work retains its owner.")
struct ActivationVerb {
 1: required string name(length="1..64",pattern="no_control")
 2: required appmodel.CommandClass class
 3: required json input_schema(length="2..65536")
}(unknown_fields="refuse",doc="A unique application-local name: lowercase letter followed by lowercase letters, digits or underscore; names in appmodel.binding_tools are reserved and refuse manifest validation. class reuses appmodel effect classes. input_schema is a JSON Schema object of at most65536 encoded bytes, validated by the existing schema owner before handler dispatch; it imports no kind.verb constraint. The existing tools/call name is this verb and arguments preserve the original validated payload bytes. Initial activation needs no ContextRef; HostActivationResult is the dedicated host delivery answer.")
struct RequestedPermission {
 1: required string action(length="1..128",pattern="no_control")
 2: required json resource(length="1..16384")
 3: required string purpose(length="0..4096")
}(unknown_fields="refuse",doc="Untrusted declaration. The registered action selects its existing resource profile/schema and actual input preparation owner. Unknown action/profile, unsupported input or unavailable validation refuses registration. No prepared resource, selector evidence, approval or grant is conveyed. Sensitive requests require nonempty purpose.")
struct NetworkRequirement {
 1: required list<NetworkHost> hosts(length="0..64")
}(unknown_fields="refuse",doc="At most64 unique lowercase hostnames, each1..253 bytes, validated by the existing credential holder host grammar. No URL, port or wildcard. Use still requires existing reach and credential policy.")
struct ModelNeed {
 1: required string modality(length="1..128",pattern="no_control")
 2: required list<FeatureName> required_features(length="0..64")
 3: required i64 minimum_context
 4: required Placement placement
 5: required i64 reserve
}(unknown_fields="refuse",doc="Modality and feature requirements use the existing router and inference catalogues. minimum_context is a nonnegative TOTAL window compared with Offer.context; reserve is nonnegative and separate from Offer.input. Unknown/none cannot meet a positive minimum. Features require advertised/verified capability evidence; unknown support leaves this need unmet. No model-name inference or authority grant.")
struct Needs {
 1: required list<request.Request> files(length="0..64")
 2: required list<ModelNeed> models(length="0..16")
}(unknown_fields="refuse",doc="At most64 file and16 model needs. Files reuse the provider-path-free download admission request and require a canonical known digest; size0 remains unknown. Provider owns its sink. Unsupported sources or unavailable owners leave the need unmet; no implicit download or credential permission.")
struct Chooser {
 1: required string name(length="1..64",pattern="no_control")
 2: required string modality(length="1..128",pattern="no_control")
}(unknown_fields="refuse",doc="One application-owned selection-policy name and its modality. name uses the existing selection grammar [a-z0-9][a-z0-9_-]{0,63}; modality is a known inference modality. At most16 unique names per manifest. This is chooser metadata; it creates no policy, grant or invocation capability. Selection state remains scoped to the actual receiving account and native program.")
struct Choosers {
 1: required list<Chooser> entries(length="0..16")
}(unknown_fields="refuse",doc="The explicitly declared chooser list. Empty means no chooser. An absent Manifest.choosers preserves the existing legacy convention until its separately reviewed migration.")
// Rename allowance, P4, 2026-11-04: 262144 +16 model needs +16 chooser
// entries (profile -> modality) +1 host deadline spelling =262177.
struct Manifest {
 1: required string version(length="1..32",pattern="no_control")
 2: required string name(length="1..64",pattern="no_control")
 3: required string title(length="1..256")
 4: required string description(length="0..4096")
 5: required appmodel.CommandDeclaration offers
 6: required HostRecipe host
 7: required bool script_host
 8: required list<RequestedPermission> requests(length="0..128")
 9: required NetworkRequirement network
 10: required Needs needs
 11: required string purpose(length="0..4096")
 12: required list<ActivationVerb> verbs(length="0..128")
 13: optional Choosers choosers(omit="absent")
}(document="true",max_encoded="262177",unknown_fields="refuse",doc="choosers absent preserves legacy chooser conventions; present empty declares no chooser. Present entries replace those conventions for the actual program. Both the actual embedded raw document and its generated canonical encoding are at most262177 bytes, semantically checked before persistence, start or effect. version is applications@3. Decode structure, check declaration version, ordered name/duplicate/class, first129th name:<name>, then actual owning signature/shared-host server. At most128 requests and128 verbs; independent member maxima are subordinate to the whole-document cap. Imported legacy version/digest values retain their existing semantic owner validators. Whole invalid lists refuse. The codec ceiling is262177 compact value bytes, excluding insignificant JSON whitespace outside strings independent of nesting. Readers count during pre-scan before member decoding without re-encoding; writers count while encoding and refuse encoded_size. Raw document and physical canonical-document limits remain separately enforced. Independent member caps remain in force. The runtime extracts this resource from the actual held signed executable; publisher text and script_host never grant authority.")

struct ApplicationFacet {
 1: required Name name
 2: required Manifest manifest
}(unknown_fields="refuse",doc="Application facet over the shared signed Manifest. Receiver independently extracts/proves the actual image's signature-covered manifest. Submitted bytes never supply publisher, approval, native identity or grants.")
// Rename allowance, P4, 2026-11-04: 262144 +64 application manifests *33
// bytes +2 (hosts -> servers) =264258; every content-field bound is unchanged.
struct RegistrationSpec {
 1: required string program(length="0..4096",pattern="no_control")
 2: required list<ProviderFacet> providers(length="0..64")
 3: required list<ServerFacet> servers(length="0..64")
 4: required list<RemoteFacet> remotes(length="0..64")
 5: required list<ApplicationFacet> applications(length="0..64")
}(max_encoded="264258",unknown_fields="refuse",doc="One input channel, at most64 total facets with unique names per kind, at most64 recipes across provider facets, and at least one facet. Native facets require program locator; HTTP-only facets have no program. Remote and native identity kinds cannot be mixed. Entire compact encoded spec and raw input are each at most264258 bytes; reject whole before persistence if either bound is exceeded. Compact count excludes insignificant JSON whitespace outside strings; the raw input bound retains formatting bytes. Imported closed Manifest records retain their owning shape and semantic validators. No source/state/publisher/check fields are input authority.")
struct Publisher {
 1: required string key(length="1..256",pattern="no_control")
 2: required string subject(length="0..1024")
 3: required string issuer(length="0..1024")
 4: required timestamp checked
}(unknown_fields="refuse",doc="Optional observed projection of Identity Code's existing PublisherKey/PublisherDN/IssuerDN evidence (P-06). Only independently verified native evidence creates it. Unsigned/unproven publisher is absent. This is not a second publisher matcher; Rights remains its owner. Changed signer refuses eligibility with publisher:changed.")
struct ApplicationReading {
 1: required Name name
 2: required string manifest_digest(length="1..135",pattern="no_control")
 3: required list<facade.ApplicationInstance> instances(length="0..128")
 4: required list<InstanceName> owned_instances(length="0..128")
}(unknown_fields="refuse",doc="Runtime-held presence/image reading for one application facet; original legacy instance record is unchanged. Actual final reply cap governs its imported legacy values. Presence is not process ownership or command authority.")
struct FacetReference {
 1: required FacetKind kind
 2: required Name name
}(unknown_fields="refuse",doc="The exact kind and registered name of one facet. Names are unique per kind, so both are required outside a kind-specific service. Data only, never authority; existing receiving kind/name validators and conditional revision govern lookup. No packed kind:name spelling.")
struct Registration {
 1: required string key(length="1..4096",pattern="no_control")
 2: required RegistrationSpec spec
 3: required Revision revision
 4: required string epoch(length="1..256",pattern="no_control")
 5: required Source source
 6: required string product_name(length="0..256")
 7: required State state
 8: required IdentityEvidence identity_evidence
 9: required Reason reason
 10: optional Publisher publisher(omit="absent")
 11: optional FacetReference shadowed_by(omit="absent")
 12: required list<ApplicationReading> applications(length="0..64")
 13: required list<ServiceState> described(length="0..64")
 14: required timestamp accepted
 15: required Verification verification
 16: required Reason verification_reason
 17: required timestamp verified
 18: optional list<ProducerCheck> checks(omit="zero",length="0..64")
}(max_encoded="1048576",unknown_fields="refuse",doc="One program record, store, revision and epoch shared by registry@3/applications@3. Native key is Identity's rights Subject.program key; remote is remote:<spki>; HTTP without program evidence is keyed by registered name and identity_evidence none. Source/state/identity_evidence/publisher are receiving-service observations, never request assertions. Missing requires Identity image-file check at probe. Each read/projection enforces caller scope before packing complete records within IPC-F1; no partial record or truncation. checks is the same-epoch receiving-authority projection of one Description.checks list, preserving kind/copy provenance. Absence means unobserved and grants nothing; readiness remains independent. Producer check times and receiver acceptance time remain distinct (CONV-T1).")
enum CleanupState {
 1: done
 2: pending
 3: failed
}(unknown="refuse",reader="act")
struct Cleanup {
 1: required string owner(length="1..128",pattern="no_control")
 2: required CleanupState state
 3: required Reason reason
}(unknown_fields="refuse",doc="Per-owner observed cleanup. An unimplemented step is pending/unsupported, never done. Each existing owner retains its authority, resource/data and audit semantics.")
struct RemovalReceipt {
 1: required string id(length="1..128",pattern="no_control")
 2: required FacetReference facet
 3: required Revision revision
 4: required list<Cleanup> cleanup(length="0..64")
 5: required timestamp accepted
}(unknown_fields="refuse",doc="Eligibility stops at removal commit. Receipt remains observable through Observe while cleanup progresses. Last-facet removal removes the program record. Disable is a separate lifecycle edit; no user process or independently accepted work is killed by a display receipt. Grace/Undo remains with D26.")
enum Outcome {
 1: applied
 2: page
 3: not_found
 4: conflict
 5: invalid
 6: forbidden
 7: unavailable
 8: unsupported
 9: gap
}(unknown="refuse",reader="act")
struct Change {
 1: required Outcome outcome
 2: required Revision revision
 3: required Reason reason
 4: optional RemovalReceipt removal(omit="absent")
 5: optional rights.RefusalDetail refusal(omit="absent")
}(unknown_fields="refuse",doc="Applied is a committed conditional edit, not completed cleanup. Unsupported has the bounded D87 reason unsupported:not_implemented and no effect. Authority refusal carries original Rights RefusalDetail; no success/refusal metadata is guessed. Lost edit answer requires current revision/identity reconciliation, never blind replay against a new revision.")
struct Page {
 1: required Outcome outcome
 2: required Revision revision
 3: required string cursor(length="0..256",pattern="no_control")
 4: required list<Registration> registrations(length="0..64")
 5: required list<RemovalReceipt> removals(length="0..64")
 6: required Reason reason
 7: optional rights.RefusalDetail refusal(omit="absent")
}(max_encoded="1048576",unknown_fields="refuse",doc="Pack complete authorized entries/receipts into the actual generated IPC-F1 reply limit including its envelope, never truncate. The record-value ceiling alone does not prove the whole generated reply fits. One oversized visible record returns unavailable/bound:reply_bytes and empty lists. Cursor binds receiving subject, epoch and committed revision; expired/foreign cursor yields gap without hidden names. Unsupported carries D87 reason unsupported:not_implemented. Observe includes cleanup and typed endpoint@2 producer checks from the same observed epoch. Older descriptions leave checks absent/unobserved.")
service RegistryV3 {
 Page Registrations(1:string cursor(length="0..256",pattern="no_control"),2:i64 limit(range="1..64"))(remote="local",readonly="true",doc="Read the operator directory under provider.manage, with one revision and exact caller-bound continuation. Starts and repairs nothing.")
 Change Register(1:RegistrationSpec registration)(remote="local",idempotent="true",doc="Create only; an existing registration returns conflict. No constant expected-revision argument is transmitted. Revalidate operator/native image/publisher/manifest and entire spec before one atomic epoch/revision commit. Source is derived from actual installation/operator/self context. A declaration grants no ungoverned rights. Unsupported facets return unsupported before effects.")
 Change Replace(1:string expected_revision(length="1..256",pattern="no_control"),2:RegistrationSpec registration)(remote="local",idempotent="true",doc="Replace only at current nonempty revision. Existing owner rechecks program/install/publisher/authority and invalidates old-epoch readiness/checks together. Source precedence and disable/shadow rules remain enforced. Producer refreshes after its pin/config change through this same method.")
 Change Remove(1:string expected_revision(length="1..256",pattern="no_control"),2:FacetReference facet)(remote="local",idempotent="true",doc="Conditionally remove the exact typed facet under provider.manage. Stop eligibility at commit and return its cleanup receipt. Last facet removes the program record. Installation/product_record sources conflict with source:<word>; disable them through SetEnabled. Existing resource owners perform audited cleanup; unbuilt steps remain pending/unsupported. Facet data grants no authority.")
 Change SetEnabled(1:string expected_revision(length="1..256",pattern="no_control"),2:FacetReference facet,3:bool enabled)(remote="local",idempotent="true",doc="Directory-owned conditional lifecycle edit for the exact facet's program under provider.manage. False narrows access; true on pending_approval refuses forbidden with person_only RefusalDetail, because person Keep owns approval. Spec/Replace never carries lifecycle intent. Applications@3 adds no duplicate method. Eligibility requires enabled and verification verified or unverified; changed/missing refuse through identity RefusalDetail denied/ask available until current image evidence and any required fresh Keep agree.")
 Page Observe(1:string cursor(length="0..256",pattern="no_control"),2:i64 wait_ms(range="0..30000"))(remote="local",readonly="true",doc="Observe current registrations, producer checks and removal receipts under provider.manage. No presence lease is renewed. Empty cursor answers now; otherwise wait boundedly for a changed observation. Refusal discloses no rows.")
}(wire_name="abstraction.facade/registry@3",doc="Complete Facade program-directory successor DRAFT. Published registry@1/@2 projections and closed records retain exact shape and behavior. Runtime store/producer/client migration remains pending implementation, review and the dated F-window gate.")
