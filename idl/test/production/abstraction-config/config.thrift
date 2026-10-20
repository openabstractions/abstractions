namespace * abstraction.config
include "../abstraction-rights/rights.thrift"

// Configuration services. Run overrides are caller-supplied claims,
// never inferred from the service process environment or used as identity.
encoding json {
  escape = "minimal"
  indent = "2"
  map_keys = "utf8-bytes"
  numbers = "integer-decimal"
  opaque = "verbatim"
  terminator = "newline"
  duplicate_keys = "last"
  depth_limit = "64"
}
refusal {
  1: malformed (stage = "grammar")
  2: bad_string (stage = "grammar")
  3: number_spelling (stage = "grammar")
  4: wrong_type (stage = "grammar")
  5: depth_exceeded (stage = "grammar")
  6: duplicate_field (stage = "structure")
  7: unknown_field (stage = "structure")
  8: missing_field (stage = "structure")
 9: bad_enum (stage = "structure")
 10: bad_value (stage = "structure")
 11: encoded_size (stage = "structure")
 12: trailing_bytes (stage = "document")
}

struct RunOverrides {
  1: required string nas_store
  2: required string store
  3: required string log_sink
  4: required string log_service
} (unknown_fields = "refuse", doc="Existing per-run overrides. Empty strings do not override file values.")
struct Origin {
  1: required string rung
  2: required string path
} (unknown_fields = "refuse", doc="Per-key provenance: machine/user file path, or environment/default with empty path.")
struct Origins {
  1: required Origin nas_store
  2: required Origin store
  3: required Origin log_sink
  4: required Origin log_service
  5: required Origin off
} (unknown_fields = "refuse", doc="Provenance for every configuration key, including default answers.")
struct Snapshot {
  1: required string nas_store
  2: required string store
  3: required string log_sink
  4: required string log_service
  5: required map<string,string> off
  6: required Origins origins
  7: required string stamp
} (document = "true", unknown_fields = "refuse", doc="Existing provider values and provenance. Empty values mean absence. Stamp follows values rather than provenance; paths are diagnostic configuration data, not permission to access a store.")
// Codes ConfigReader handlers send on the reply error channel, beside the
// dispatcher's own.
const list<string> reader_error_codes = ["storage_unavailable", "caller_unavailable", "identity_required", "wrong_user"]

service ConfigReader {
  Snapshot Read(1: RunOverrides overrides) (readonly="true",doc="Read same-user machine/user configuration with explicit caller run overrides. No writes, watch or provider fallback.")
} (wire_name = "abstraction.config/reader@1", error_codes = "reader_error_codes", doc="Per-user configuration read service. Caller identity is checked independently of overrides.")

struct UserSettings {
  1: required string nas_store
  2: required string store
  3: required string log_sink
  4: required string log_service
  5: required map<string,string> off
} (unknown_fields = "refuse", doc="User-rung overrides only. Empty values clear overrides; machine and run values never enter this record. The configuration backing-file path is selected by the service; path-valued settings grant no provider storage authority.")
struct UserSnapshot {
  1: required UserSettings values
  2: required string revision
} (unknown_fields = "refuse", doc="Normalized user-rung content and an opaque content revision. A missing file yields empty values; unreadable or unsupported storage is refused. Revision may recur when identical content is restored.")
enum UserReplaceOutcome {
  1: applied
  2: conflict
  3: forbidden
  4: unavailable
} (unknown = "refuse")
struct UserReplaceResult {
  1: required UserReplaceOutcome outcome
  2: required UserSnapshot snapshot
} (unknown_fields = "refuse", doc="Applied returns the written snapshot. Conflict performs no write and returns the current snapshot. Forbidden reports an evaluated edit-policy refusal; unavailable reports that the edit-policy decision could not be obtained and may be retried. Both perform no storage access and carry empty values with an empty revision. No outcome merges settings implicitly.")
// Codes ConfigEditor handlers send on the reply error channel. Edit-policy
// refusal is the forbidden or unavailable outcome, never one of these.
const list<string> editor_error_codes = ["invalid_revision", "storage_unavailable", "caller_unavailable", "identity_required", "wrong_user"]

service ConfigEditor {
  UserSnapshot ReadUser() (readonly="true",doc="Read only the authenticated service user's persisted rung. No environment or machine merge. Missing storage is empty; corrupt, unsupported or unavailable storage returns storage_unavailable.")
  UserReplaceResult ReplaceUser(1: string expected_revision, 2: UserSettings values) (idempotent="true",doc="Compare the opaque revision and replace atomically through the selected conditional-write store. Stale revision returns conflict. Empty revision is invalid_revision. Storage failures return storage_unavailable. A configured edit policy is evaluated after same-account proof and before storage access: refusal returns forbidden and a failed decision returns unavailable. A canceled wait leaves write outcome unresolved; reread rather than blindly retry.")
} (wire_name = "abstraction.config/editor@1", error_codes = "editor_error_codes", doc="Same-account [Program](https://openabstractions.github.io/reference.html#concept-identity.listener-program-need)-proven user editor on the config endpoint. The service selects its private user path once. Caller claims cannot grant authority; forbidden callers cause no storage access. Existing machine and run rungs are unaffected.")


enum ConfigObservationOutcome {
  1: snapshot
  2: unchanged
  3: gap
  4: unavailable
  5: unsupported
  6: invalid
} (unknown = "refuse")
struct ConfigObservation {
  1: required ConfigObservationOutcome outcome
  2: required string cursor
  3: optional Snapshot snapshot (omit = "absent")
} (unknown_fields = "refuse", doc="Latest effective configuration, with an opaque cursor bound to provider instance and explicit run overrides. snapshot carries a snapshot and new cursor; unchanged carries the original cursor and no snapshot. Other outcomes carry no snapshot and preserve the supplied cursor. Intermediate revisions may be coalesced; this is not an event history. A restarted provider or changed override binding gives gap and requires an explicit empty-cursor restart.")
service ConfigObserver {
  ConfigObservation Observe(1: RunOverrides overrides, 2: string cursor, 3: i64 wait_ms) (readonly="true",result_present="snapshot",result_when="outcome",result_is="snapshot",observe="cursor", observe_next="cursor", doc="Empty cursor reads the current snapshot. At the same revision, wait at most wait_ms (0..30000) for provider invalidation; reread on expiry. Cursor is at most 512 UTF-8 bytes and each override at most 4096 bytes. A slow consumer receives the latest revision; revisions include values and provenance. Missing change notification support gives unsupported. Bounded waiter exhaustion or provider notification failure gives unavailable. Caller cancellation stops observation waiting only; setting mutations retain their own lifetimes. Receiving account authority is rechecked before returning data.")
} (wire_name = "abstraction.config/observer@1", doc="Same-account latest-snapshot long-poll observation over a provider's explicit change notification source.")

// @2 uses catalogue entries.
typedef string ConfigKey(length="1..64",pattern="ascii_name")
typedef string ConfigText(length="0..4096",pattern="no_control")
typedef string ConfigReason(length="0..512",pattern="no_control")
typedef string ConfigName(length="1..128",pattern="no_control")
typedef string ConfigRevision(length="0..64",pattern="ascii_name")
// Published @1 records and service shapes above
// remain the legacy projection of the same service-owned conditional store.
enum ValueKind {
 1: boolean
 2: integer
 3: bytes
 4: duration
 5: text
 6: choice
 7: text_list
 8: text_map
 9: unlimited
}(unknown="refuse",reader="act")
struct Value {
 1: required ValueKind kind
 2: optional bool boolean(omit="zero")
 3: optional i64 integer(omit="zero")
 4: optional i64 bytes(omit="zero")
 5: optional i64 duration_ms(omit="zero")
 6: optional ConfigText text(omit="zero")
 7: optional ConfigKey choice(omit="zero")
 8: optional list<ConfigName> text_list(omit="zero",length="0..128")
 9: optional map<ConfigKey,ConfigReason> text_map(omit="zero",length="0..8")
}(unknown_fields="refuse",max_encoded="262144",doc="The tag carries value presence, including false, zero and empty text/list/map. Every payload other than the field selected by kind is zero. The runtime validates this invariant on changes and stored values; generated generic client parsers apply the same invariant. Unset is an absent Entry.value or Change.set, never a zero payload.")
enum Location {
 2: machine
 3: user
 4: process
}(unknown="refuse",reader="act")
enum KeyState {
 1: set
 2: default
 3: locked
 4: ignored
 5: pending
}(unknown="grant",reader="display")
struct ConfigSource {
 1: required Location location
 2: required ConfigText path
}(unknown_fields="grant")
struct KeyOrigin {
 1: optional Location location(omit="zero")
 2: required ConfigText path
 3: required KeyState state
 4: optional ConfigReason reason(omit="zero")
 5: optional list<ConfigSource> shadowed(omit="zero",length="0..3")
}(unknown_fields="grant",doc="Per-key effective location and state. Paths are diagnostic and grant no storage access. A lockable key set in a trusted machine file is locked and wins over user and process values. Shadowed lists farther settings that also supplied the key.")
struct Entry {
 1: required ConfigKey key
 2: optional Value value(omit="absent")
 3: required KeyOrigin origin
}(unknown_fields="grant",doc="A known key. Absent value means unset, with its catalogue default applying when present. Unknown or unauthorized keys occur only in ReadResult.refused.")
enum ApplyMode {
 1: live
 2: restart
}(unknown="refuse",reader="act")
struct ValueRange {
 1: required i64 min
 2: required i64 max
}(unknown_fields="grant")
struct KeyDescriptor {
 1: required ConfigKey key
 2: required ValueKind kind
 3: optional Value default(omit="absent")
 4: optional ValueRange range(omit="absent")
 5: optional list<ConfigKey> choices(omit="zero",length="0..16")
 6: optional list<Value> presets(omit="zero",length="0..16")
 7: required ConfigReason description
 8: required ConfigKey group
 9: required ApplyMode apply
 10: required bool lockable
 11: required Location lowest_location
 12: optional ConfigReason deprecated(omit="zero")
 13: optional ConfigReason default_description(omit="zero")
 14: required bool allows_unlimited
}(unknown_fields="grant",max_encoded="262144",doc="Definition-owned key metadata from keys.json, validated by this record's generated schema and emitted as one bounded catalogue constant. Range applies to integer, bytes and duration; choices to choice. No key is a wire record field. default_description explains an existing provider-computed default.")
struct Change {
 1: required ConfigKey key
 2: optional Value set(omit="absent")
}(unknown_fields="refuse",max_encoded="65536",doc="Set one typed user value or unset it when set is absent. A batch applies all changes or none under its compared user-content revision.")
enum KeyRefusalReason {
 1: not_granted
 2: unknown_key
}(unknown="refuse",reader="act")
struct KeyRefusal {
 1: required ConfigKey key
 2: required KeyRefusalReason reason
 3: optional rights.RefusalDetail refusal(omit="absent")
}(unknown_fields="grant")
struct KeyProblem {
 1: required ConfigKey key
 2: required ConfigReason reason
}(unknown_fields="grant",doc="A bounded key-specific diagnostic. It contains no supplied value or credential. Locked edits use locked:machine; invalid edits name unknown_key, wrong_kind, out_of_range or invalid_value.")
enum ReadOutcome {
 1: read
 2: corrupt
 3: not_permitted
 4: unavailable
 5: invalid
 6: forbidden
}(unknown="refuse",reader="act")
enum IgnoredFieldReason {
 1: unknown_key
 2: invalid_name
}(unknown="grant",reader="display")
struct IgnoredField {
 1: required i64 offset(range="0..262144")
 2: required string name_excerpt(length="0..64",pattern="no_control")
 3: required IgnoredFieldReason reason
 4: required ConfigSource source
}(unknown_fields="grant",doc="A bounded display-only excerpt of an ignored stored member name. The zero-based offset locates its original bytes; the excerpt is sanitized UTF-8, at most 64 bytes, with an ellipsis when cut. It is never a key, selector or authority.")
struct ReadResult {
 1: required ReadOutcome outcome
 2: required list<Entry> entries(length="0..64")
 3: required ConfigRevision revision
 4: optional list<KeyRefusal> refused(omit="zero",length="0..64")
 5: optional KeyProblem problem(omit="absent")
 6: optional i64 corrupt_offset(omit="zero",range="0..262144")
 7: optional list<IgnoredField> ignored_fields(omit="zero",length="0..16")
 8: optional i64 ignored_omitted(omit="zero",range="0..262144")
 9: optional ConfigSource corrupt_source(omit="absent")
 10: optional rights.RefusalDetail refusal(omit="absent")
}(unknown_fields="grant",max_encoded="262144",doc="Successful entries and per-key refusals are disjoint. revision is the effective-content digest including provenance/state. A malformed known stored value is ignored with invalid_value and the default value. An unparseable record gives corrupt with its zero-based first failing byte offset; outcome carries offset presence including zero. Storage I/O gives unavailable. Non-read outcomes disclose no entries or revision.")
struct UserSnapshotV2 {
 1: required list<Entry> entries(length="0..64")
 2: required ConfigRevision revision
}(unknown_fields="grant",max_encoded="262144",doc="Only persisted user values, with a normalized-content revision distinct from the effective revision. Legacy @1-only fields are preserved in the same store and are absent from this projection.")
struct UserReadResultV2 {
 1: required ReadOutcome outcome
 2: optional UserSnapshotV2 snapshot(omit="absent")
 3: optional KeyProblem problem(omit="absent")
 4: optional i64 corrupt_offset(omit="zero",range="0..262144")
 5: optional rights.RefusalDetail refusal(omit="absent")
}(unknown_fields="grant")
enum ReplaceOutcome {
 1: applied
 2: conflict
 3: not_permitted
 4: invalid
 5: unavailable
 6: corrupt
 7: forbidden
}(unknown="refuse",reader="act")
struct UserReplaceResultV2 {
 1: required ReplaceOutcome outcome
 2: optional UserSnapshotV2 snapshot(omit="absent")
 3: optional list<KeyProblem> refused(omit="zero",length="0..64")
 4: optional i64 corrupt_offset(omit="zero",range="0..262144")
 5: optional rights.RefusalDetail refusal(omit="absent")
}(unknown_fields="grant",max_encoded="262144",doc="Applied and conflict carry the current user snapshot; every other outcome omits snapshot and discloses no user state. A locked key is not_permitted with locked:machine. Unknown/wrong-kind/out-of-range values are invalid. Refusal writes nothing. A canceled wait leaves outcome unresolved; reread before retrying.")
enum ObservationOutcomeV2 {
 1: changed
 2: unchanged
 3: gap
 4: unavailable
 5: unsupported
 6: invalid
 7: not_permitted
 8: corrupt
 9: forbidden
 10: failed
}(unknown="refuse",reader="act")
struct ConfigObservationV2 {
 1: required ObservationOutcomeV2 outcome
 2: required string cursor(length="0..512",pattern="no_control")
 3: optional ReadResult snapshot(omit="absent")
 4: optional KeyProblem problem(omit="absent")
 5: optional i64 corrupt_offset(omit="zero",range="0..262144")
 6: optional ConfigSource corrupt_source(omit="absent")
 7: optional rights.RefusalDetail refusal(omit="absent")
 8: optional i64 age_ms(omit="absent",range="0..9223372036854775807")
 9: optional ConfigReason cause(omit="zero")
}(unknown_fields="grant",max_encoded="262144",doc="One latest effective snapshot. Values, lock state and restart-pending state invalidate the revision. No process overrides cross the wire or bind this cursor. Restart gives gap. Unchanged carries the original cursor and no snapshot.")
service ConfigReaderV2 {
 ReadResult Read(1:list<ConfigKey> names(length="0..64"))(readonly="true",result_present="corrupt_source",result_when="outcome",result_is="corrupt",doc="Read named keys; an empty list asks for every key this caller may read. The runtime-designated provider's exact key grants are checked from rechecked native identity. No self-asserted role or name grants a read. A missing authority callback refuses. Process overrides are overlaid by the generated client only.")
 DescribeResult Describe()(readonly="true",doc="Read the definition-owned validated key catalogue, bounded to 64 descriptors (CFG-S3). No values, identity claims or permission grants are carried.")
}(wire_name="abstraction.config/reader@2",doc="Typed catalogue settings and effective provenance. Uses the same service-owned user/machine store as @1. No provider variable pass-through or additional policy store.")
service ConfigEditorV2 {
 UserReadResultV2 ReadUser()(readonly="true",result_present="snapshot",result_when="outcome",result_is="read")
 UserReplaceResultV2 ReplaceUser(1:string expected_revision(length="64..64",pattern="ascii_name"),2:list<Change> changes(length="0..16"))(idempotent="true",doc="Atomically set/unset the whole batch under the existing conditional-write owner. Compare user-content revision, validate every change and machine lock before writing. Preserve all @1-only fields. Unknown, duplicate, wrong-kind and out-of-range changes refuse as one batch. No implicit rebase or retry.")
}(wire_name="abstraction.config/editor@2",doc="Same-account Program-proven user editor, narrowed by the current native edit policy. Machine locks remain authoritative and cannot be overridden by process values.")
service ConfigObserverV2 {
 ConfigObservationV2 Observe(1:string cursor(length="0..512",pattern="no_control"),2:i64 wait_ms(range="0..30000"))(readonly="true",observe="cursor",observe_next="cursor",doc="Observe effective values and lock/pending state through the existing invalidation source. changed carries a fresh snapshot; unavailable may carry the last authorized good snapshot with age_ms, absent when none exists. failed ends observation with cause. wait_ms is 0..30000 and cursor at most 512 bytes. Cancellation ends the wait only. Observation remains caller-authorized and keeps no process override binding.")
}(wire_name="abstraction.config/observer@2",doc="Latest effective settings observation through the existing native source and bounded watch gate; no polling loop.")

struct DescribeResult {
 1: required ReadOutcome outcome
 2: required list<KeyDescriptor> descriptors(length="0..64")
 3: optional rights.RefusalDetail refusal(omit="absent")
}(unknown_fields="grant",max_encoded="262144",doc="read carries the complete bounded catalogue; every refusal carries an empty list. CFG-S3 declares the bounded-catalogue paging divergence.")
const list<string> resource_actions = ["abstraction.config/key.read", "abstraction.config/user.replace"](catalogue="closed",closed_by="CFG-A1")
