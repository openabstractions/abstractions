namespace * abstraction.facade.appmodel

// The application model: the building blocks an application declares so that
// assistants and OA surfaces can read and drive it through one binding
// (CONTRACT.md "Application model", FAC-M1 to FAC-M33). Records only: this
// definition has no service and no transport. The MCP binding carries these
// records as structuredContent; the OA binding is the gateway's mediation.

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
 4: wrong_type(stage="grammar")
 5: depth_exceeded(stage="grammar")
 6: duplicate_key(stage="grammar")
 7: duplicate_field(stage="structure")
 8: unknown_field(stage="structure")
 9: missing_field(stage="structure")
 10: bad_enum(stage="structure")
 11: bad_value(stage="structure")
 12: encoded_size(stage="structure")
 13: trailing_bytes(stage="document")
}

// The binding version an application serves (FAC-M1). An MCP server carries
// it in initialize and server/discover answers as
// _meta["org.openabstractions/appmodel"] = {"version": ...};
// an announced instance carries it as interface contract
// abstraction.facade/appmodel@1.
const list<string> versions = ["appmodel@1"]

// The optional extensions a description may declare (FAC-M14). A reader
// ignores an extension name it does not know.
const list<string> extensions = ["collection_delta", "undo", "proposal", "async"]

// The binding's own tools (FAC-M15). No tool name OA defines contains the word
// model, which names machine-learning weights (oa_models_list).
const list<string> binding_tools = ["app_describe", "app_contexts", "app_read", "app_changes", "app_reveal", "app_undo", "app_job"]

// The OA gateway's fixed tools for applications (FAC-M15, FAC-M22, FAC-M32).
// oa_applications_open starts or reopens a kept application through its
// declared activation verb (applications@3 Activate).
const list<string> gateway_tools = ["oa_applications_describe", "oa_applications_commands_list", "oa_applications_command_call", "oa_applications_open"]

// The org.openabstractions/ MCP _meta keys the binding and the gateway write
// (FAC-M15, FAC-M25). The io.modelcontextprotocol/ keys are MCP's.
const list<string> meta_keys = ["org.openabstractions/appmodel", "org.openabstractions/untrusted"] (catalogue = "closed", closed_by = "FAC-M15")

// One rights action per command class, decided on resource app:<name>
// (FAC-M19). The definitions carry W2-01b's shape in CONTRACT.md.
const list<string> command_actions = [
 "abstraction.facade/command.read",
 "abstraction.facade/command.reveal",
 "abstraction.facade/command.propose",
 "abstraction.facade/command.edit",
 "abstraction.facade/command.commit",
 "abstraction.facade/command.destroy",
 "abstraction.facade/command.spend",
 "abstraction.facade/command.send"
] (catalogue = "closed", closed_by = "FAC-M19")

// B9. The class of a command is the application's statement about its effect
// (FAC-M9). read: reads model state. reveal: draws attention without a state
// change. propose: evaluates a command without effect. edit: changes model
// state the application's own undo reverses. commit: a reversible or cost-only
// effect outside model state, such as run or queue. destroy: overwrite or
// unrecoverable deletion. spend: money within the application's daily limit.
// send: data leaving the machine.
enum CommandClass {
 1: read
 2: reveal
 3: propose
 4: edit
 5: commit
 6: destroy
 7: spend
 8: send
}(unknown="refuse",reader="act")

// B11. The outcome of a command call, an apply, a reveal, an undo or a job
// read (FAC-M11). failed is only app_job's: accepted work ended without its
// effect.
enum CommandOutcome {
 1: applied
 2: proposed
 3: accepted
 4: conflict
 5: stale
 6: invalid
 7: unsupported
 8: forbidden
 9: not_permitted
 10: refused
 11: exhausted
 12: not_found
 13: unknown
 14: unavailable
 15: failed
}(unknown="refuse",reader="act")

// The outcome of a description, context, snapshot or change read and of the
// gateway's listings (FAC-M5, FAC-M6, FAC-M11). The words mean what they mean
// in CommandOutcome; page is an answer and gap a lost position. A read that
// was sent and lost its connection reads unavailable.
enum ReadingOutcome {
 1: page
 2: gap
 3: conflict
 4: stale
 5: invalid
 6: unsupported
 7: not_permitted
 8: refused
 9: exhausted
 10: not_found
 11: unavailable
}(unknown="refuse",reader="act")

// B4. The role of one field of a kind (FAC-M4).
enum FieldRole {
 1: ordinary
 2: derived
 3: ephemeral
 4: secret
}(unknown="refuse",reader="act")

// B5. The collection operations (FAC-M7), the words adapters and cores use for
// a collection step. No record carries this enum: a Change's patch carries the
// RFC 6902 op word, and reset travels as replace of the whole array. add,
// remove, move and replace act on one element.
enum CollectionOp {
 1: add
 2: remove
 3: move
 4: replace
 5: reset
}(unknown="refuse",reader="act")

// Whether a command call evaluates without effect or applies (FAC-M10).
enum InvokeMode {
 1: propose
 2: apply
}(unknown="refuse",reader="act")

// B12. How the application draws a reveal (FAC-M12). An application draws a
// style it does not know as highlight.
enum RevealStyle {
 1: highlight
 2: scroll_to
 3: frame
}(unknown="grant",reader="display")

// B1.
struct ContextRef {
 1: required string instance
 2: required string context
 3: required string epoch
}(unknown_fields="refuse",doc="B1. One document, workflow, canvas or view inside one running instance (FAC-M2). instance is 1..128 bytes and names one run of the application, the applications@2 handle through OA or an application-chosen name without OA. A later run MUST NOT reuse an earlier run's name, for example by choosing 128 random bits in hex. context is the application's opaque name for the document, 1..64 bytes. epoch is an opaque 1..64-byte token the application changes whenever the context is replaced, reopened or reloaded; a reference whose epoch differs from the current one reads stale.")

struct ContextEntry {
 1: required ContextRef context
 2: required string title
 3: required string revision
}(unknown_fields="grant",doc="One context an instance offers, with its title and its current revision. title is application-published text, 0..256 bytes, and untrusted (FAC-M25).")

struct ContextPage {
 1: required ReadingOutcome outcome
 2: required list<ContextEntry> contexts
 3: required string reason
}(unknown_fields="refuse",doc="The answer of app_contexts: page carries at most 32 contexts. Other outcomes carry no contexts and a reason.")

// B4.
struct KindField {
 1: required string name
 2: required FieldRole role
}(unknown_fields="refuse",doc="One property of a kind and its role. A field the kind's schema names and this list omits is ordinary. Field names are non-empty and distinct within a kind. A name identifies one literal top-level property, including a name containing a dot; marking an object-valued property secret removes that whole property.")

struct Kind {
 1: required string name
 2: required string title
 3: required json schema
 4: required list<KindField> fields
}(unknown_fields="refuse",doc="B4. The declared shape of an entity kind (FAC-M4). name is 1..64 bytes of a-z 0-9 _ and starts with a letter; it is the prefix of every command the kind owns. schema uses the closed application-model JSON Schema 2020-12 profile in FAC-M29 and describes the properties the application holds, secret and ephemeral included. A secret field's value is never published.")

// B3.
struct Entity {
 1: required string id
 2: required string kind
 3: required string label
 4: optional string parent(omit="zero")
 5: required json properties
}(unknown_fields="refuse",doc="B3. One addressable object in a context: a node, layer, element or row (FAC-M4). id is application-supplied, 1..128 bytes, stable and unique within the context epoch. kind names a declared kind. label is untrusted application text of 0..256 bytes. parent names the containing entity for presentation, empty at the context's root. A core checks no rule of it: absent ids and cycles are allowed; collections carry the checked structure (FAC-M7). properties is the held JSON object with every secret field removed, and with ephemeral fields removed outside a read. The core checks held properties against the kind's schema at each commit point. A reader checks published properties against no schema; a required secret field is absent from every published entity (FAC-M4, FAC-M5).")

// B5.
struct Collection {
 1: required string parent
 2: required string kind
 3: required list<string> ids
}(unknown_fields="refuse",doc="B5. The ordered entity ids of one kind under one parent, empty for the context's root (FAC-M7). It projects at /collections/<parent>/<kind> (FAC-M5).")

// B6.
struct Selection {
 1: required string view
 2: required list<string> entities
 3: optional string focus(omit="zero")
}(unknown_fields="refuse",doc="B6. What the person has selected and focused in one view (FAC-M8). Only the person writes it. A snapshot carries at most one selection per view; the latest selection for a view replaces the earlier one. A core checks no rule of entities or focus: absent ids, repeated ids and a focus outside entities are allowed. Selections count toward snapshot_bytes (FAC-M5).")

// B7.
struct ContextSnapshot {
 1: required ReadingOutcome outcome
 2: required ContextRef context
 3: required string revision
 4: required list<Entity> entities
 5: required list<Collection> collections
 6: required list<Selection> selections
 7: required string cursor
 8: required string reason
}(unknown_fields="refuse",doc="B7. One page of a coherent read of a context at one revision, the answer of app_read (FAC-M5). Every entity, collection and selection on every page of one read belongs to revision. A read of the whole context carries every collection and selection on its first page and entities only on later pages; a read of named entities carries those entities only. cursor is empty on the last page and otherwise continues the read at the same revision; a cursor whose revision has moved reads conflict. Every outcome carries the current revision when its ContextRef resolves (FAC-M6), and an empty revision otherwise, including invalid from decoding or the ContextRef bounds. Outcomes other than page carry no entities, collections, selections or cursor, and a reason. Revision identifies the projection. Ephemeral fields are read at the time of the read and selections are the person's current selection, so equal revisions can carry different live values (FAC-M4, FAC-M5, FAC-M8).")

struct ReadRequest {
 1: required ContextRef context
 2: required list<string> entities
 3: required string cursor
 4: required i64 limit
}(unknown_fields="refuse",doc="The arguments of app_read. entities names at most 256 ids to read, in the request's order, or none for the whole context, whose entities are ordered by id in code-point order (FAC-M5). cursor is empty or a previous page's. limit is 1..1024 entities per page.")

// B8.
struct Change {
 1: required string base_revision
 2: required string revision
 3: required json patch
}(unknown_fields="refuse",doc="B8. One committed step of a context from base_revision to revision (FAC-M6). patch is an RFC 6902 JSON Patch over the context's projection (FAC-M5) that turns the projection at base_revision into the projection at revision.")

struct ChangesRequest {
 1: required ContextRef context
 2: required string after_revision
 3: required i64 wait_ms
}(unknown_fields="refuse",doc="The arguments of app_changes. after_revision is the last revision the reader holds. wait_ms is 0..30000.")

struct ChangeBatch {
 1: required ReadingOutcome outcome
 2: required ContextRef context
 3: required list<Change> changes
 4: required string revision
 5: required string reason
}(unknown_fields="refuse",doc="The answer of app_changes (FAC-M6). page carries the changes after after_revision in commit order, as many as fit: it ends before the first change that would take its encoded ChangeBatch past snapshot_bytes, and always carries the first change. Each change's base_revision is the previous change's revision. revision is the last change it carries, and the reader continues from it; a quiet wait reads page with no changes and revision equal to after_revision. gap means after_revision is no longer retained, is empty, or is not one this context epoch issued: the reader takes a fresh snapshot. A page carries the position stated above. Every other outcome carries the current revision when its ContextRef resolves, gap, invalid and exhausted included. Otherwise revision is empty, including stale, not_found for a context, unavailable, and invalid from decoding or the ContextRef bounds. gap has reason after_revision. unavailable means the application cannot answer, distinct from a quiet wait.")

// B9.
struct CommandSpec {
 1: required string name
 2: required string title
 3: required string description
 4: required CommandClass class
 5: required json input_schema
 6: required json output_schema
 7: required list<string> targets
 8: required bool proposable
 9: required bool undoable
 10: required bool idempotent
 11: required bool long_running
}(unknown_fields="refuse",doc="B9. One named operation the application declares (FAC-M9). name is <kind>.<verb>, 3..128 bytes, each part 1..64 bytes of a-z 0-9 _ starting with a letter, and its kind part names a declared kind. title and description are untrusted application text of 1..256 and 0..4096 bytes. class is exactly one command class. input_schema and output_schema are JSON objects using the closed application-model schema profile (FAC-M29). The core checks arguments against input_schema and applied command effects against output_schema; targets adds no check. targets lists declared entity kinds the command acts on, for presentation. A core refuses to register a command, and the gateway refuses a served description, with a targets entry naming an undeclared kind, with reason targets:<name> (FAC-M9, FAC-M17). proposable, allowed only for edit, commit, destroy, spend and send and only with the extension proposal, means mode propose is served. undoable, allowed only for edit, means an applied call that changes the projection yields one undo group (FAC-M13); the flag requires no undo extension. The extension gates app_undo. idempotent means a second apply with equal arguments under a new key leaves the projection as the first left it; it still runs. long_running, allowed only for commit, spend and send and only with the extension async, means apply may answer accepted with a job. A served description that breaks one of these reads invalid with reason <flag>:<name> (FAC-M17). A command of class propose evaluates in both modes without the proposable flag or proposal extension (FAC-M9, FAC-M10).")

struct ContextBounds {
 1: required i64 snapshot_bytes
 2: required i64 change_bytes
 3: required i64 retained_revisions
 4: required i64 pending_proposals
 5: required i64 calls_in_flight
}(unknown_fields="refuse",doc="The bounds a context applies (FAC-M14). snapshot_bytes is the largest encoded ContextSnapshot page, 4096..262144, and the ChangeBatch page bound (FAC-M6). change_bytes is the largest encoded Change, 1024..262144; a larger step is published as gap. retained_revisions is how many recent revisions app_changes can continue from, 16..65536. pending_proposals is 1..64 per context. calls_in_flight is 1..64 per context. FAC-M15's application text view writes each answer-record byte as at most two bytes and its structured view as at most one. A DescriptionResult adds two bytes of indentation per description line. An application's complete answer body is at most 917504 bytes plus its JSON-RPC envelope and fits IPC-F1's 1 MiB body (FAC-M14, FAC-M31). The gateway's agent-facing MCP stdio uses its SDK's HTML-escaping encoder; IPC-F1 bounds the applications@3 reply alone (FAC-M32).")

// B14.
struct Description {
 1: required string version
 2: required list<Kind> kinds
 3: required list<CommandSpec> commands
 4: required list<string> extensions
 5: required ContextBounds bounds
}(unknown_fields="refuse",doc="B14. What an application's contexts offer, the answer of app_describe (FAC-M14). version is a member of versions. kinds holds 1..64 kinds and commands 0..128 commands with distinct names. extensions lists declared members of extensions; a reader ignores one it does not know. The encoded description is at most 262144 bytes.")

struct DescriptionResult {
 1: required ReadingOutcome outcome
 2: optional Description description(omit="absent")
 3: required string reason
}(unknown_fields="refuse",doc="description is present exactly when outcome is page.")

// B9 and B10: one command call. The command is the name of the tool the
// call goes to (FAC-M15), so the record carries no command member.
struct Invocation {
 1: required ContextRef context
 2: required json arguments
 3: required InvokeMode mode
 4: required string expected_revision
 5: required string idempotency_key
 6: optional string proposal(omit="zero")
}(unknown_fields="refuse",doc="The arguments of one command tool: one call of a declared command (FAC-M9, FAC-M10). arguments is a JSON object conforming to the command's input_schema, at most 65536 bytes of its original opaque JSON value span, including interior whitespace and excluding the surrounding Invocation and document terminator; an apply naming a proposal carries the empty object. expected_revision is non-empty for an apply of edit, commit and destroy, and empty or checked for other calls. It has no bound of its own; a value the context epoch never issued reads conflict (FAC-M11). idempotency_key is the caller's own 1..128-byte key, non-empty for an apply of edit, commit, destroy, spend and send: a repeat with the same key is the same call. proposal names a proposal this context epoch issued for the same command; apply with a proposal evaluates that proposal's arguments at its bound revision. An apply naming a proposal of a propose command reads invalid with reason proposal (FAC-M9).")

// B10 and B11.
struct CommandResult {
 1: required CommandOutcome outcome
 2: required string reason
 3: required string revision
 4: optional string proposal(omit="zero")
 5: optional string summary(omit="zero")
 6: optional string job(omit="zero")
 7: optional json effects(omit="absent")
 8: optional string undo_group(omit="zero")
 9: optional string idempotency_key(omit="zero",length="..128")
}(unknown_fields="refuse",doc="B11. The typed result of a command call (FAC-M11). applied carries the revision after the call, the observed effects as a JSON object conforming to the command's output_schema, and an undo_group when the command is undoable and its call changed the projection. proposed carries the proposal id, a plain summary of 1..1024 bytes of untrusted application text, and revision, the revision the proposal is bound to. accepted carries a job id and the revision after acceptance; app_job answers accepted while the job runs, applied with its effects once it finished, and failed with a reason when it ended without its effect, each with the current revision of the context that accepted the job while that context epoch holds and an empty revision after it (FAC-M11). conflict carries the current revision. Every other outcome carries a reason and the current revision, or an empty revision when none is known. The record is at most 262144 encoded bytes. An applied command whose effects fail output_schema, or an applied call whose result exceeds that size, answers unknown with reason internal; its steps stay. An output-schema failure issues no undo group. app_job, app_undo and app_reveal effects are a JSON object checked against no schema. A core refuses a finished job's effects whose result would exceed the size limit when the application reports completion. idempotency_key is the key of the Invocation the gateway sent for a command call of oa_applications_command_call, the caller's own or one the gateway generated (FAC-M32); the gateway writes it on every answer to such a call that names a key, and an application's own answer carries none. proposal and summary are carried with proposed alone; job with accepted and every app_job answer about a job the instance holds; effects with applied alone; undo_group with an applied undoable call that changed the projection. Readers read each member only with an outcome that carries it; the gateway adds no refusal for another member and checks effects for secrets whatever the outcome (FAC-M5, FAC-M11). A refusal after a changed own step answers unknown/internal, keeps its steps and issues no undo group; a propose evaluation committing a changed own step answers unknown/internal and records no proposal (FAC-M10, FAC-M11).")

// B12.
struct Reveal {
 1: required ContextRef context
 2: required list<string> entities
 3: required RevealStyle style
}(unknown_fields="refuse",doc="B12. A request to draw attention to 1..256 entities (FAC-M12). It changes no selection and no revision.")

// B13.
struct UndoRequest {
 1: required ContextRef context
 2: required string undo_group
 3: required string expected_revision
 4: required string idempotency_key
}(unknown_fields="refuse",doc="The arguments of app_undo, served under the undo extension (FAC-M13). It reverses exactly the named undo group of an agent's applied call. expected_revision is the current revision and idempotency_key is non-empty.")

struct JobRequest {
 1: required string instance
 2: required string job
}(unknown_fields="refuse",doc="The arguments of app_job: read the state of a job an accepted call returned (FAC-M11). A job belongs to the instance that accepted it, outlives an epoch change of its context (FAC-M2) and ends with its instance; a job of another instance reads stale.")

// The command declaration an application manifest carries (FAC-M17).
struct DeclaredCommand {
 1: required string name
 2: required CommandClass class
}(unknown_fields="refuse",doc="One command a kept application manifest declares: its name and its class. Command names and classes come only from the kept manifest (FAC-M17, FAC-M18).")

struct CommandDeclaration {
 1: required string version
 2: required list<DeclaredCommand> commands
}(unknown_fields="refuse",doc="The application manifest's command declaration, carried in the offers of the applications@3 manifest (W2-09a). version is a member of versions. commands holds 0..128 distinct names. A declared command whose class is missing or unrecognised refuses the whole manifest at validation.")

// The OA gateway's tools (FAC-M32). Each tool's arguments and its
// structuredContent are records of this definition.
struct ApplicationsQuery {
 1: optional string application(omit="zero")
}(unknown_fields="refuse",doc="The arguments of oa_applications_describe (FAC-M22, FAC-M32). Empty lists every application for which the caller's subject holds a read permit, and for a kept agent each application it may open, by name and title with no instances; it raises no question. application names one application, the name of resource app:<name>: the listing then holds that application alone, an unregistered name answers not_found, and naming one with no stored rule raises its first-use question.")

struct ListedInstance {
 1: required string instance
 2: required string interface
 3: required list<ContextEntry> contexts
 4: required string reason
}(unknown_fields="refuse",doc="One live instance of an application (FAC-M22): its applications@2 instance handle, the contract it serves, abstraction.facade/appmodel@1, and at most 32 contexts with their current revisions, the most recently used first. An instance serving a version outside versions has an empty interface, no contexts and reason version:<served> (FAC-M1); one whose server the gateway could not start or whose description it refused has an empty interface, no contexts and the reason of that refusal (FAC-M17, FAC-M18).")

struct ListedApplication {
 1: required string application
 2: required string title
 3: required list<ListedInstance> instances
}(unknown_fields="refuse",doc="One application in the gateway's listing (FAC-M22). application is the registered name of resource app:<name>. title is the kept manifest's title. instances holds its live instances, each with the version it serves.")

struct ListedOpenVerb {
 1: required string name(length="1..64",pattern="no_control")
 2: required string description(length="1..256",pattern="no_control")
 3: required json input_schema(length="2..65536")
}(unknown_fields="refuse",doc="Quiet kept-manifest activation metadata. description is an OA sentence naming the declared verb. input_schema is the exact declared JSON Schema for the payload. Neither member conveys authority.")

struct ApplicationOpenVerbs {
 1: required string application(length="1..64",pattern="no_control")
 2: required list<ListedOpenVerb> verbs(length="1..128")
}(max_encoded="294912",unknown_fields="refuse",doc="Open metadata for one visible application in ApplicationListing.")

struct ApplicationListing {
 1: required ReadingOutcome outcome
 2: required list<ListedApplication> applications
 3: required string reason
 4: optional list<ApplicationOpenVerbs> open_verbs(length="1..64",omit="zero")
}(max_encoded="1048576",unknown_fields="grant",doc="The answer of oa_applications_describe (FAC-M22). page lists at most 64 applications in name order: those whose command.read the caller's subject is permitted and, for a kept agent, those it may open, shown with name and title and no instances (FAC-M22). A query naming one application answers not_found, not_permitted or refused for it with no applications and a reason. Other outcomes carry no applications and a reason.")

struct CommandsQuery {
 1: required string application
 2: required string instance
}(unknown_fields="refuse",doc="The arguments of oa_applications_commands_list (FAC-M32): one application and one of its live instances.")

struct CommandListing {
 1: required ReadingOutcome outcome
 2: required string application
 3: required string instance
 4: required list<CommandSpec> commands
 5: required string reason
}(unknown_fields="grant",doc="The answer of oa_applications_commands_list (FAC-M32). page carries the instance's served CommandSpecs that the kept manifest declares, at most 128, each with the manifest's class; a served command the manifest lacks or serves with another class is absent. The binding tools are not listed: binding_tools names them. Other outcomes carry no commands and a reason.")

struct GatewayCall {
 1: required string application
 2: required string instance
 3: required string tool
 4: required json arguments
 5: optional InvokeMode mode(omit="absent")
 6: optional string expected_revision(omit="zero",length="..262144")
 7: optional string idempotency_key(omit="zero",length="..128")
 8: optional string context(omit="zero",length="..64")
 9: optional string proposal(omit="zero",length="..262144")
 10: optional bool require_read_permit(omit="absent")
}(unknown_fields="refuse",doc="The arguments of oa_applications_command_call (FAC-M32). tool is a command the kept manifest declares or a member of binding_tools. For a command, arguments is the command's input_schema object exactly as oa_applications_commands_list shows it, and the gateway builds the Invocation the application's tool takes (FAC-M9, FAC-M10): mode defaults to apply and is propose when the command's class is propose; context names one context of instance and defaults to the instance's current context, the first app_contexts lists, whose epoch the gateway reads there; expected_revision defaults to that context's current revision; idempotency_key is the caller's key, and the gateway generates one when it is empty and echoes the key in the CommandResult; proposal names a proposal to apply, with arguments the empty object. The gateway checks arguments against input_schema before it sends the call and answers invalid with reason field:<path>:<missing|unknown|type|bound>. For a binding tool, arguments is that tool's own record, decoded with the generated codec: app_read a ReadRequest, app_changes a ChangesRequest, app_reveal a Reveal, app_undo an UndoRequest and app_job a JobRequest; for app_describe and app_contexts the empty object. Members 5 to 9 are empty for a binding tool. require_read_permit is accepted only for app_read. When true it requires a current read permit at authorization and dispatch without admitting a consent question. Only the opaque members inside arguments stay unread. Every instance a ContextRef or JobRequest in arguments names equals instance.")

// The gateway's audit record of one routed call (FAC-M24).
struct CallRecord {
 1: required string subject
 2: required string client_label
 3: required string application
 4: required string instance
 5: optional ContextRef context(omit="absent")
 6: required string command
 7: optional CommandClass class(omit="absent")
 8: required string revision_before
 9: required string revision_after
 10: required string outcome
 11: required string reason
 12: required string idempotency_key
 13: required string arguments_digest
 14: required string result_digest
}(unknown_fields="refuse",doc="One record per call of oa_applications_command_call (FAC-M24). subject is the rights subject the call was decided for: the agent program's own subject or the gateway subject. client_label is MCP clientInfo name and version, a label the agent application reports and never identity. instance is the instance the call named, present for every call. context is present when the call names a context, and its instance equals instance; app_describe, app_contexts and app_job name none. command is the tool the call named: a command or a binding tool. class is the class the gateway decided the call by: propose for a call in mode propose, the kept manifest's for another command call and FAC-M15's for a binding tool; it is absent when the manifest declares no such command (FAC-M17) or the tool did not decode. client_label joins the name and the version with one space, is the name alone without a version, and is empty without clientInfo (FAC-M23, FAC-M24). revision_before is the call's expected_revision, or after_revision for app_changes, and empty otherwise; revision_after is the answer's revision, empty when the answer carries none. outcome is the CommandOutcome or ReadingOutcome word. reason is the reason the gateway wrote when it answered the call itself or refused the application's answer, and empty when the application's answer passed. arguments_digest is sha256:<lowercase hex> of GatewayCall.arguments' original bytes and result_digest of the encoded answer record; neither value is stored. A GatewayCall the codec refuses leaves a record whose unread members are empty, with outcome invalid. Logging carries the record with the gateway as emitter and the application's program as regarding.")

// One envelope of the application model. Exactly one member is present
// (FAC-M33): the definition language has no union (idl/LANGUAGE.md DEF-F2),
// so a reader checks the member count after the codec accepts a document, and
// the JSON Schema of this document states it as minProperties = maxProperties
// = 1.
struct Envelope {
 1: optional DescriptionResult description(omit="absent")
 2: optional ContextPage contexts(omit="absent")
 3: optional ReadRequest read(omit="absent")
 4: optional ContextSnapshot snapshot(omit="absent")
 5: optional ChangesRequest changes_request(omit="absent")
 6: optional ChangeBatch changes(omit="absent")
 7: optional Invocation invocation(omit="absent")
 8: optional CommandResult result(omit="absent")
 9: optional Reveal reveal(omit="absent")
 10: optional UndoRequest undo(omit="absent")
 11: optional JobRequest job(omit="absent")
 12: optional CommandDeclaration declaration(omit="absent")
 13: optional CallRecord record(omit="absent")
 14: optional ApplicationsQuery applications_query(omit="absent")
 15: optional ApplicationListing applications(omit="absent")
 16: optional CommandsQuery commands_query(omit="absent")
 17: optional CommandListing commands(omit="absent")
 18: optional GatewayCall call(omit="absent")
}(document="true",schema_members="1..1",unknown_fields="refuse",doc="One application-model record. Exactly one member is present; a reader refuses a document with none or more than one with the word member_count (FAC-M33). The MCP binding and the gateway carry the member record itself, so no tool reads an envelope; the envelope names which record a document is in the conformance session's transcript and in fixtures (FAC-M30).")
