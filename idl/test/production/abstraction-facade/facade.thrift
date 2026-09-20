namespace * abstraction.facade

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

enum Scope { 1: any 2: local 3: remote }(unknown="refuse")
enum ResolutionStatus {
 1: resolved
 2: unavailable
 3: forbidden
 4: incompatible
 5: unmet_requirements
 6: not_ready
 7: invalid_request
}(unknown="refuse")
struct ResolveRequest {
 1: required string capability
 2: required list<string> contracts
 3: required list<string> guarantees
 4: required Scope scope
}(unknown_fields="refuse",doc="Capability and acceptable contract identities, required guarantees and permitted placement. Contains no caller identity or provider preference. Contract list must be nonempty; guarantees may be empty.")
struct ServiceReference {
 1: required string provider
 2: required string capability
 3: required string contract
 4: required list<string> guarantees
 5: required Scope scope
 6: required string transport
 7: required string endpoint
}(unknown_fields="refuse",doc="A candidate service binding, not acceptance or authority. Provider identifies a logical provider, not a PID. Scope is the concrete execution placement, local or remote; transport and endpoint describe the application connection. Both placements can bind a local OA endpoint whose service enforces that placement. Contract is an exact versioned service identity; endpoint is opaque to application code.")
struct ResolveResult {
 1: required ResolutionStatus status
 2: optional ServiceReference reference(omit="absent")
}(document="true",unknown_fields="refuse",doc="Reference is present exactly when resolved. Failure status describes the first unsatisfied resolution stage without exposing disallowed provider metadata. Service authentication and acceptance remain necessary after resolution.")
service Resolver {
 ResolveResult Resolve(1:ResolveRequest request)(doc="Find an authorized ready candidate satisfying an exact acceptable contract and all required guarantees. Does not submit work, activate an embedded provider, or transfer ownership.")
}(wire_name="abstraction.facade/resolver@1",doc="Common provider resolution vocabulary. Authorization comes from the receiving boundary, never from request fields. See RESOLUTION.md for selection and refusal semantics.")

// Platform evidence and resolver evidence are independent observations.
enum BootstrapState {
 1: unknown
 2: installed
 3: starting
 4: running
 5: unavailable
}(unknown="refuse")
struct BootstrapObservation {
 1: required BootstrapState state
 2: optional string detail(omit="absent")
}(unknown_fields="refuse",doc="Read-only platform registration or supervisor evidence. Unknown means observation is unavailable; unavailable requires actual evidence that the selected registration is absent. Installed and starting require registration or supervisor evidence. Running describes a supervisor process and does not establish capability readiness. A missing endpoint alone establishes none of these states. Detail is diagnostic text, never authority or a recovery instruction.")
struct CapabilityObservation {
 1: required ResolveRequest request
 2: optional ResolveResult result(omit="absent")
}(unknown_fields="refuse",doc="One authorized resolver observation. An absent result means unobserved because the query was not completed; it does not imply unavailable. Existing resolution statuses and disclosure rules apply unchanged. A resolved reference remains a candidate binding, not proof of successful provider calls.")
struct RuntimeObservation {
 1: required BootstrapObservation bootstrap
 2: required list<CapabilityObservation> capabilities
}(unknown_fields="refuse",doc="Client-composed point-in-time diagnostics, with no new service or activation operation. Platform evidence is explicit and may be unknown. Capability queries use one caller waiting budget and authority; observations are sequential and not an atomic snapshot. Transport or cancellation errors are reported separately by the language observation API; unanswered entries retain absent results.")

// What the receiving boundary bound for the caller of this connection. The
// proof and ceiling words are the abstraction.identity Proof names; this record
// echoes the caller's own identity to it and confers no authority.
enum CallerOutcome {
 1: observed
 2: forbidden
 3: unavailable
 4: invalid
}(unknown="refuse",reader="act")
struct CallerAttribute {
 1: required string attribute
 2: required string proof
 3: required string ceiling
}(unknown_fields="refuse",doc="One native Peer attribute: user, process, path, package or code, in that order. proof is the abstraction.identity Proof name the receiving boundary established for this connection's caller. ceiling is the best Proof name the receiving platform's transport can reach for that attribute. Both are asserted by the receiving runtime and are display words, never authority.")
struct CallerObservation {
 1: required CallerOutcome outcome
 2: required string mechanism
 3: required string account
 4: required string program
 5: required i64 pid
 6: required list<CallerAttribute> attributes
 7: required string platform
 8: required string transport
 9: required bool bindable
 10: required string stronger
}(unknown_fields="refuse",doc="The receiving runtime's assertion of how it bound the caller. mechanism names the establishing facility as the logging service stamp does, identity/<os>. account is the Windows SID or the decimal POSIX uid, and program the executable path, each at the proof its attribute reports. pid is -1 where unestablished. platform, transport, bindable and stronger restate the receiving platform's identity ceiling: whether a binding pins the caller's process, and which transport would prove more (empty when none). Only observed carries identity; forbidden, unavailable and invalid carry empty strings, pid -1, no attributes, and bindable false.")
service Caller {
 CallerObservation Observe()(doc="Return what the receiving boundary bound for this connection's caller. It is the same Program evidence every runtime-hosted local service requires. unavailable means the binding could not be rechecked; forbidden is reserved for a boundary policy that withholds the echo; invalid for a request this provider cannot interpret. Observe grants, checks and changes nothing.")
}(wire_name="abstraction.facade/caller@1",doc="The runtime resolver endpoint's echo of its bound caller, for diagnostics such as the control panel. Local transport only: a remote service states its own authentication and never inherits these proofs.")

// Every generated endpoint answers abstraction.facade/endpoint@1 beside its own
// services: the Go and C++ generators emit it for every dispatcher, so any
// generated service can be probed with this one call (ENDPOINT-1 to ENDPOINT-3
// in CONTRACT.md).
enum DescriptionOutcome {
 1: described
 2: forbidden
 3: unavailable
 4: invalid
}(unknown="refuse",reader="act")
enum ServiceReadiness {
 1: ready
 2: not_ready
 3: unknown
}(unknown="refuse",reader="act")
struct ServiceState {
 1: required string contract
 2: required ServiceReadiness readiness
 3: required string why
 4: required list<string> guarantees
 5: required map<string,string> capabilities
}(unknown_fields="refuse",doc="One service an endpoint hosts. contract is its wire name. readiness is ready unless the handler's readiness hook reports otherwise; why names the reason when it is not ready and is empty when ready. guarantees are those the provider states for the service. capabilities is an open map of display facts, such as profiles: chat,embed. None of it is authority.")
struct Description {
 1: required DescriptionOutcome outcome
 2: required string program
 3: required string version
 4: required list<ServiceState> services
}(unknown_fields="refuse",doc="described lists every service the endpoint hosts, in the endpoint's order. program and version are the display name and version the provider gives itself, possibly empty, and never authority: the caller binds the server by its own connection proof. forbidden is reserved for a boundary policy that withholds the description, unavailable for an endpoint that cannot describe itself now, and invalid for a request it cannot interpret. Refusals carry empty strings and no services.")
service Endpoint {
 Description Describe()(doc="Describe the services this endpoint hosts and each one's readiness. Grants, checks and changes nothing.")
}(wire_name="abstraction.facade/endpoint@1",doc="The base-protocol description every generated endpoint serves beside its own contracts. A registry probes readiness with it over a connection that requires the declared program as the server. An endpoint built before this service answers unknown_service.")

// abstraction.facade/registry@1: the provider declarations a person placed in
// the runtime, which feed its resolution catalogue (REG-1 to REG-5 in
// CONTRACT.md).
// The transports a declaration names: oa-native@1 is the shared framed IPC
// every generated dispatcher serves at a local endpoint name, and oa-remote@1
// another runtime over mutual TLS at tls://<host>:<port>.
enum DeclarationTransport {
 1: native(wire="oa-native@1")
 2: remote(wire="oa-remote@1")
}(unknown="refuse",reader="act")
// The resource kinds a declaration's resources name, as <kind>:<name>. Each
// capability reads its own acceptance rule for them: storage accepts
// store:<name> under abstraction.storage/inventory.provide; host:<name> and
// profile:<name> are recorded for listing.
const list<string> declaration_resource_kinds = ["store", "host", "profile"]
// Rights actions the registry enforces, on resource account.
const list<string> registry_actions = ["abstraction.facade/provider.manage"] (catalogue = "closed", closed_by = "REG-5")
enum Activation {
 1: on_demand
 2: attach
 3: remote
}(unknown="refuse",reader="act")
struct RemoteTrust {
 1: required string server_name
 2: required string roots
 3: required string certificate
 4: required string key
 5: optional string credential(omit="zero")
}(unknown_fields="refuse",doc="The explicit mutual-TLS trust of a remote runtime. server_name is the name its certificate must carry. roots, certificate and key are absolute paths of PEM files on this machine: the roots trusted for that server, and this runtime's client certificate and private key, which the remote maps to its own caller. credential, when present, names the abstraction.credentials record the remote holds and applies to requests delegated to it. The paths are configuration; no key material travels in registry@1.")
struct Declaration {
 1: required string name
 2: required string program
 3: required list<string> arguments
 4: required string endpoint
 5: required DeclarationTransport transport
 6: required list<string> contracts
 7: optional list<string> guarantees(omit="zero")
 8: optional list<string> resources(omit="zero")
 9: required Activation activation
 10: optional RemoteTrust remote(omit="absent")
 11: optional list<string> models(omit="zero")
}(unknown_fields="refuse",doc="One provider outside the runtime. name is 1..64 bytes of a-z 0-9 _ - and unique. program is the absolute executable path the runtime launches and requires of the process serving endpoint; empty for a remote runtime. arguments are 0..64 strings of 1..4096 bytes; the argument {endpoint} is replaced by endpoint. endpoint is a local endpoint name of 1..64 bytes of a-z 0-9 _ . - for oa-native@1, and tls://<host>:<port> for oa-remote@1. transport is a DeclarationTransport member. contracts holds 1..16 distinct wire names of generated services the provider serves. guarantees holds 0..16 distinct names its candidates advertise. resources holds 0..64 distinct <kind>:<name> of declaration_resource_kinds, name 1..64 bytes of a-z 0-9 _ . -. models is the 0..64 distinct model names a native inference provider is trusted to serve, each 1..256 UTF-8 bytes without controls. on_demand launches program as a supervised child when a resolution first needs it; attach reads a provider something else started; remote is exactly the oa-remote@1 activation, and remote is present exactly then.")
enum DeclarationReadiness {
 1: ready
 2: idle
 3: starting
 4: restarting
 5: refused
 6: unreachable
 7: not_ready
}(unknown="refuse",reader="act")
struct DeclarationState {
 1: required Declaration declaration
 2: required string declared_by
 3: required i64 declared_unix_ms
 4: required DeclarationReadiness readiness
 5: required string why
 6: required i64 restarts
 7: required list<ServiceState> described
 8: optional list<string> accepted(omit="zero")
}(unknown_fields="refuse",doc="A declaration and the runtime's latest reading of it. declared_by is the operator program that declared it. ready means endpoint@1 Describe, over a connection requiring program as the server, listed every declared contract ready. idle is an on_demand provider nothing has needed yet; starting a launched child not yet ready; restarting a child that exited and waits out its backoff; refused a process at endpoint running another program (why program:<detail>); unreachable a provider whose Describe failed (why describe:<code or detail>); not_ready a provider whose Describe lists a declared contract not ready or absent (why contract:<wire name>:<reason>). described is the last Description's services. accepted holds the resources a capability accepted at the last reading, such as store:<name> described by an inventory source and permitted by inventory.provide. restarts counts launches after the first.")
enum DeclarationListOutcome {
 1: page
 2: invalid
 3: forbidden
 4: unavailable
}(unknown="refuse",reader="act")
struct DeclarationList {
 1: required DeclarationListOutcome outcome
 2: required string revision
 3: required list<DeclarationState> declarations
}(unknown_fields="refuse",doc="page carries every declaration in name order, at most 64, and the declarations' revision Declare and Withdraw take. invalid is reserved for a request the runtime cannot interpret. Refusals carry an empty revision and no declarations.")
enum DeclarationEditOutcome {
 1: applied
 2: conflict
 3: unknown
 4: invalid
 5: forbidden
 6: unavailable
}(unknown="refuse",reader="act")
struct DeclarationChange {
 1: required DeclarationEditOutcome outcome
 2: required string revision
 3: required string reason
}(unknown_fields="refuse",doc="applied carries the new revision; the runtime supervises, withdraws or stops the provider at once. reason is empty, or rules:<detail> when an acceptance rule could not be written. conflict means expected_revision is not current (reason revision), or Declare named an existing name or endpoint (reason name or endpoint), and carries the current revision. unknown means Withdraw named no declaration. invalid carries the field in reason, and program:self when the calling program declares itself. Other outcomes carry an empty revision.")
struct DeclarationObservation {
 1: required DeclarationListOutcome outcome
 2: required string cursor
 3: required list<DeclarationState> declarations
}(unknown_fields="refuse",doc="page carries every declaration and cursor, a digest of the declarations and their readings. It answers once cursor differs from the cursor given, or when wait_ms ends. invalid means wait_ms is outside 0..30000. Refusals carry an empty cursor and no declarations.")
service Registry {
 DeclarationList Declarations()(doc="Read every declaration and its reading. Gated by abstraction.facade/provider.manage on resource account.")
 DeclarationChange Declare(1:string expected_revision,2:Declaration declaration)(doc="Conditionally add one declaration, kept as providers/<name>.json in the runtime state. Gated by provider.manage. A program cannot declare itself (invalid, program:self). A store:<name> resource writes the permit rule abstraction.storage/inventory.provide on store:<name> for program, and a remote declaration writes abstraction.inference/complete on host:<name> for the runtime's operator programs and the caller; an existing rule on a target is left as it is. A declaration grants nothing else.")
 DeclarationChange Withdraw(1:string expected_revision,2:string name)(doc="Conditionally remove one declaration: the runtime withdraws its candidates and hosts and ends a launched child. Gated by provider.manage. Rules are left as they are.")
 DeclarationObservation Observe(1:string cursor,2:i64 wait_ms)(doc="Wait up to wait_ms milliseconds for the declarations or their readings to differ from cursor, then read them. An empty cursor answers at once. Gated by provider.manage.")
}(wire_name="abstraction.facade/registry@1",doc="The runtime's provider declarations, for operator tools. Applications never read it; they resolve. Each call is a rights decision for the bound operator subject; same-account identity alone grants nothing. A decision point that cannot answer reads unavailable. The registry is local: a remote runtime's services come from its own endpoint@1 Describe, and no registry is read across machines.")

const list<string> default_runtime_contracts = [
 "abstraction.logging/sink@1",
 "abstraction.config/reader@1",
 "abstraction.job/acceptance@1",
 "abstraction.job/operations@1",
 "abstraction.config/editor@1"
]

enum ApplicationOutcome {
 1: applied
 2: page
 3: unknown
 4: stale
 5: conflict
 6: invalid
 7: forbidden
 8: unavailable
}(unknown="refuse",reader="act")
enum ApplicationActivationOutcome {
 1: ready
 2: unknown
 3: disabled
 4: forbidden
 5: invalid
 6: launch_refused
 7: identity_refused
 8: not_ready
 9: unavailable
}(unknown="refuse",reader="act")
struct ApplicationInterface {
 1: required string name
 2: required string protocol
 3: required string contract
}(unknown_fields="refuse",doc="An application's claimed interface metadata. name identifies it within one instance; protocol and contract describe claimed support. No connection address or executable authority is conveyed. Invocation requires an independently authorized OA mediation binding.")
struct ApplicationActivationRecipe {
 1: required list<string> arguments
 2: required ApplicationInterface readiness
 3: required i64 readiness_timeout_ms
}(unknown_fields="refuse",doc="Operator-trusted recipe for one bounded start of descriptor.program in the owning user's current runtime session. arguments contains at most 64 literal arguments of 1..4096 bytes. readiness identifies the interface a descriptor-program-and-session-bound announcement must supply. readiness_timeout_ms is one 100..30000 budget across executable inspection, launch and readiness. The recipe grants no document-open, focus, installation, termination or restart authority.")
// A descriptor is bounded to 8192 JSON-encoded bytes in addition to its field limits.
struct ApplicationDescriptor {
 1: required string name
 2: required string program
 3: required string title
 4: required string start_guidance
 5: optional ApplicationActivationRecipe activation(omit="absent")
}(unknown_fields="refuse",doc="Operator-approved installed application. name is a stable 1..64-byte identifier; program is its normalized absolute native program identity. title and start_guidance are attributed display text, bounded to 256 and 4096 bytes. Guidance is inert. activation is a separately typed operator-trusted recipe; absence disables activation. Registration grants no invocation or execution authority. This descriptor survives an application's exit.")
struct ApplicationContext {
 1: required string name
 2: required string title
 3: required string revision
}(unknown_fields="refuse",doc="A bounded application-owned context identifier, display title and revision claim. It is scoped to the service-assigned instance epoch; a reused context name after restart is a different object. A revision claim supplies no authorization or atomic mutation guarantee.")
struct ApplicationPresence {
 1: required string application
 2: required string instance
 3: required list<ApplicationInterface> interfaces
 4: required list<ApplicationContext> contexts
 5: required i64 lease_ms
}(unknown_fields="refuse",doc="Announce or renew the bound program's own instance in the runtime's verified local session. Empty instance requests a new opaque handle; a nonempty handle renews exactly its owner and session in the current runtime epoch. At most 16 distinct interfaces and 32 distinct contexts, each identifier 1..64 bytes. The complete JSON-encoded presence is at most 2048 bytes. lease_ms is 1000..60000. Expiration establishes absence of fresh evidence, not a clean exit.")
struct ApplicationInstance {
 1: required string instance
 2: required list<ApplicationInterface> interfaces
 3: required list<ApplicationContext> contexts
 4: required i64 expires_unix_ms
}(unknown_fields="refuse",doc="A current caller-visible instance and its attributed metadata. Handle ownership is enforced by OA. Possession of the handle grants no rights.")
struct ApplicationEntry {
 1: required ApplicationDescriptor descriptor
 2: required list<ApplicationInstance> instances
 3: required Scope scope
}(unknown_fields="refuse",doc="One installed application's descriptor with its currently leased instances. scope records directory provenance; this local profile supplies local and never imports remote announcements. An empty instance list makes no claim about whether an unregistered process is running.")
struct ApplicationChange {
 1: required ApplicationOutcome outcome
 2: required string instance
 3: required string reason
}(unknown_fields="refuse",doc="applied confirms the change; an announcement returns its opaque instance. stale means the instance expired or belongs to an earlier runtime epoch. Refusals carry no instance and never launch an app.")
struct ApplicationPage {
 1: required ApplicationOutcome outcome
 2: required string cursor
 3: required list<ApplicationEntry> applications
}(unknown_fields="refuse",doc="A complete bounded permission-filtered snapshot: at most 64 descriptors and 128 total live instances. Cursor is bound to caller, visible state and runtime epoch. Observe rechecks permissions before every answer. A cursor from another runtime epoch returns stale with a fresh full snapshot; no incremental continuity is claimed. Outcomes other than page or stale carry no cursor or entries.")
struct ApplicationActivationResult {
 1: required ApplicationActivationOutcome outcome
 2: required string instance
 3: required bool started
 4: required string reason
}(unknown_fields="refuse",doc="ready identifies a descriptor-program-and-session-bound presence whose claimed interface matches the recipe; it is not a protocol probe or process-lineage proof. started says the platform launcher accepted the recipe during this shared attempt; the returned instance may be a fresh announcement from an already-running matching program. false means an existing matching instance was reused before launch. Cancellation returns unavailable to that waiter and leaves the bounded attempt and user application alive. Failure outcomes carry no instance.")
service Applications {
 ApplicationChange Register(1:ApplicationDescriptor descriptor)(doc="Add an installed descriptor. Requires application.manage on account; existing names conflict. Does not launch or grant use. Programs cannot self-register.")
 ApplicationChange Remove(1:string application)(doc="Remove an installed descriptor and its presence. Requires application.manage on account. Does not terminate user applications.")
 ApplicationChange Announce(1:ApplicationPresence presence)(doc="Publish or renew presence. Requires application.announce on app:<name>, a bound native program and kernel session matching this runtime, and current owner/session/epoch for renewal. Claims never alter descriptors or grants.")
 ApplicationChange Withdraw(1:string application,2:string instance)(doc="Withdraw only the bound program and session's own current instance. Requires application.announce on app:<name>. Does not terminate the application or remove its installed descriptor.")
 ApplicationPage Observe(1:string cursor,2:i64 wait_ms)(doc="Read or wait for a caller-filtered complete snapshot. Requires application.read on each returned app:<name>; hidden entries and changes remain undisclosed. Empty cursor answers immediately. wait_ms is 0..30000; permissions and lease expiry are rechecked while waiting. No activation occurs.")
 ApplicationActivationResult Activate(1:string application)(doc="Reuse a matching descriptor-program-bound presence or share one bounded authorized start attempt in this local runtime session. Requires application.activate on app:<name>. Only the operator-registered typed recipe executes; guidance and announcements remain inert. Caller cancellation stops its wait and never terminates or restarts the application.")
}(wire_name="abstraction.facade/applications@1",doc="Experimental application-presence profile owned by the OA service. Persistent operator descriptors and transient app announcements have distinct authority and lifetime. This is a local directory; MCP/UI metadata are claims, and invocation remains behind OA capability mediation. Native callers retain their actual account/program identity. Unsupported platform identity refuses access.")
