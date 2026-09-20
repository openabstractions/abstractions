namespace * abstraction.config

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
 10: trailing_bytes (stage = "document")
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
  Snapshot Read(1: RunOverrides overrides) (doc="Read same-user machine/user configuration with explicit caller run overrides. No writes, watch or provider fallback.")
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
  UserSnapshot ReadUser() (doc="Read only the authenticated service user's persisted rung. No environment or machine merge. Missing storage is empty; corrupt, unsupported or unavailable storage returns storage_unavailable.")
  UserReplaceResult ReplaceUser(1: string expected_revision, 2: UserSettings values) (doc="Compare the opaque revision and replace atomically through the selected conditional-write store. Stale revision returns conflict. Empty revision is invalid_revision. Storage failures return storage_unavailable. A configured edit policy is evaluated after same-account proof and before storage access: refusal returns forbidden and a failed decision returns unavailable. A canceled wait leaves write outcome unresolved; reread rather than blindly retry.")
} (wire_name = "abstraction.config/editor@1", error_codes = "editor_error_codes", doc="Same-account Program-proven user editor on the config endpoint. The service selects its private user path once. Caller claims cannot grant authority; forbidden callers cause no storage access. Existing machine and run rungs are unaffected.")


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
  ConfigObservation Observe(1: RunOverrides overrides, 2: string cursor, 3: i64 wait_ms) (doc="Empty cursor reads the current snapshot. At the same revision, wait at most wait_ms (0..30000) for provider invalidation; reread on expiry. Cursor is at most 512 UTF-8 bytes and each override at most 4096 bytes. A slow consumer receives the latest revision; revisions include values and provenance. Missing change notification support gives unsupported. Bounded waiter exhaustion or provider notification failure gives unavailable. Caller cancellation stops observation waiting only; setting mutations retain their own lifetimes. Receiving account authority is rechecked before returning data.")
} (wire_name = "abstraction.config/observer@1", doc="Same-account latest-snapshot long-poll observation over a provider's explicit change notification source.")
