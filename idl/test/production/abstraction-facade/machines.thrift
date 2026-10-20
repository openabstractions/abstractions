namespace * abstraction.facade.machines

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

enum MachineScopeKind { 1: none 2: one_host_inference }(unknown="refuse",reader="act")
struct MachinePreparation {
 1: required MachineScopeKind request
 2: required MachineScopeKind offer
 3: required string offer_host(length="0..64")
 4: required string normal_service_address(length="0..64")
 5: required string binding_name(length="1..64")
}(unknown_fields="refuse",doc="Explicit local request and receiving configuration. none requires empty offer_host and normal_service_address. A nonempty offer selects one currently registered local host and one numeric nonzero normal service address distinct from invitation. Configuration authority belongs to the bound native operator; remote.serve grants no configuration authority. Exact directional effects are frozen before native consent. No granting default.")
struct MachineRequestIdentity {
 1: required string epoch(length="32..32")
 2: required i64 expires_ms
 3: required string nonce(length="32..32")
}(unknown_fields="refuse",doc="One local operation identity. epoch and monotonic expires_ms come from List; nonce is random lowercase hex. Prior epoch, expired or excessively future intent refuses before effects. A reply loss reuses this exact identity and arguments; it never causes automatic redial.")
enum MachinePhase {
 1: offering 2: connecting 3: checking 4: awaiting_person
 5: consent_sent 6: reconciling 7: paired 8: refused
 9: cancelled 10: expired 11: uncertain
}(unknown="refuse",reader="act")
struct MachineDirection {
 1: required MachineScopeKind kind
 2: required string host(length="0..64")
 3: required string service_address(length="0..64")
}(unknown_fields="refuse",doc="One receiver's frozen offer. Nonempty means remote.serve/account, complete on the selected host-name slot including Embed, and account-wide router inventory, until revoked. Host-slot reuse and later registered models retain existing resource semantics. This record describes scope and confers no authority.")
struct MachineAgreement {
 1: required string peer_key(length="64..64")
 2: required string check(length="6..6")
 3: required string transcript(length="64..64")
 4: required string semantics(length="64..64")
 5: required MachineDirection given
 6: required MachineDirection received
}(unknown_fields="refuse",doc="Read-only final directional preview bound by the pairing transcript. Changes require a fresh attempt and fresh native consent. Transport keys and proposal data establish no pin or Rights effect.")
struct MachineOperation {
 1: required string id(length="1..80")
 2: required MachinePhase phase
 3: required i64 expires_ms
 4: required string reason(length="0..64")
 5: optional MachineAgreement agreement(omit="absent")
 6: optional string question_id(omit="zero",length="0..128")
 7: optional string revision(omit="zero",length="0..32")
}(unknown_fields="refuse",doc="Current local operation snapshot. paired requires actual local native occurrence, completed transport and conditional state/Rights reconciliation. uncertain preserves possible irreversible local Yes. No code or private key appears here.")
enum MachineStartOutcome { 1: started 2: existing 3: forbidden 4: unavailable 5: invalid 6: conflict 7: exhausted 8: expired }(unknown="refuse",reader="act")
struct MachineStart {
 1: required MachineStartOutcome outcome
 2: optional MachineOperation operation(omit="absent")
 3: optional string code(omit="zero",secret="true",length="0..74")
}(document="true",unknown_fields="refuse",doc="Only StartOffer returns a secret code to its admitted operator. Enter and all observations omit it. An existing exact request reuses its retained operation without starting another attempt.")
enum MachineObserveOutcome { 1: changed 2: unchanged 3: gap 4: unknown 5: forbidden 6: unavailable 7: invalid 8: exhausted 9: failed }(unknown="refuse",reader="act")
struct MachineObservation {
 1: required MachineObserveOutcome outcome
 2: required string cursor(length="0..160")
 3: optional MachineOperation operation(omit="absent")
}(unknown_fields="refuse",doc="O1 epoch-bound snapshot; foreign/expired cursor is gap and requires a fresh observation. Wait is bounded by30000ms and rechecks operator authority after wake. Unknown operation after restart remains unknown. Mechanism native through the runtime watch gate.")
enum MachineCancelOutcome { 1: updated 2: unknown 3: forbidden 4: unavailable 5: invalid }(unknown="refuse",reader="act")
struct MachineCancellation {
 1: required MachineCancelOutcome outcome
 2: optional MachineOperation operation(omit="absent")
}(unknown_fields="refuse",doc="Explicit abandonment. Reply cancellation alone does not abandon. A possible local Yes remains uncertain; it is never reported as reciprocal No.")
struct MachineRecord {
 1: required string peer_key(length="64..64")
 2: required string state(length="1..16")
 3: required string service_address(length="0..64")
 4: optional i64 last_call_unix_ms(omit="zero")
 5: optional string binding_name(length="0..64",omit="zero")
}(unknown_fields="refuse",doc="One current paired key and lifecycle. Inactive/removing pins deny. Service address is bound connection data; a display label supplies no identity. last_call_unix_ms is the latest authenticated incoming RPC for this exact current peer that passed the offered-method/profile and remote.serve/account ingress gate during this runtime epoch, as Unix milliseconds. Handshakes, bad peers and ingress refusals do not count. Later validation, resource authorization, capacity or upstream failure may refuse work. This contact observation guarantees no permission, effect or success and requires no RPC response. Absence means no observed value. Metadata clears on removal/replacement/restart and changes no pairing revision.")
enum MachineListOutcome { 1: page 2: gap 3: forbidden 4: unavailable 5: invalid }(unknown="refuse",reader="act")
struct MachinePage {
 1: required MachineListOutcome outcome
 2: required string epoch(length="0..32")
 3: required i64 request_expires_ms
 4: required string revision(length="0..32")
 5: required string next_cursor(length="0..160")
 6: required list<MachineRecord> machines(length="0..64")
 7: optional string local_service_address(length="0..64",omit="zero")
}(unknown_fields="refuse",doc="L1 page pinned to current state revision and runtime epoch. Changed revision or foreign cursor requires relisting. At most64 rows, filtered for the native operator before paging. No credential or invitation secrets.")
enum MachineRemoveOutcome { 1: removed 2: cleanup_pending 3: conflict 4: not_found 5: forbidden 6: unavailable 7: invalid }(unknown="refuse",reader="act")
struct MachineRemoval {
 1: required MachineRemoveOutcome outcome
 2: required string revision(length="0..32")
}(unknown_fields="refuse",doc="Disable the local pin before exact-subject Rights cleanup and admitted-work termination. A failed cleanup leaves removing and blocks re-pair. No offline peer deletion, distributed rollback or completed-work undo is claimed.")
service MachineControl {
 MachineStart StartOffer(1:MachineRequestIdentity request, 2:string invitation_address(length="1..64",pattern="no_control"), 3:MachinePreparation preparation)(remote="local",idempotent="true",doc="Reserve one operation before effects; create one numeric invitation listener and return its code. The first accepted connection consumes the offer, including failed handshake.")
 MachineStart Enter(1:MachineRequestIdentity request, 2:string code(secret="true",length="1..74",pattern="no_control"), 3:MachinePreparation preparation)(remote="local",idempotent="true",doc="Reserve and dial exactly one code. CLI supplies code through stdin/terminal, never argv. No retry or inferred normal-service address.")
 MachineObservation Observe(1:string operation_id(length="1..80",pattern="no_control"), 2:string after(length="0..160",pattern="no_control"), 3:i64 wait_ms)(remote="local",readonly="true",doc="Read or wait for one current operation using O1. Cancelled reply does not cancel operation.")
 MachineCancellation Cancel(1:string operation_id(length="1..80",pattern="no_control"))(remote="local",idempotent="true",doc="Explicitly abandon an operation while preserving post-Yes uncertainty and actual reconciled completion.")
 MachinePage List(1:string cursor(length="0..160",pattern="no_control"), 2:i64 limit)(remote="local",readonly="true",doc="List the operator-visible current pair book through L1. Returns the epoch/expiry admission inputs for a new operation.")
 MachineRemoval Remove(1:string expected_revision(length="1..32",pattern="no_control"), 2:string peer_key(length="64..64",pattern="no_control"))(remote="local",idempotent="true",doc="Conditional local removal. Disable first, clean exact subject through Rights, then remove. Recheck native operator before each effect.")
}(wire_name="abstraction.facade/machines@1",doc="Native operator-only machine control on the normal local Facade registry endpoint. Every method requires live local Program proof and operator Recheck. This service is never served through normal remote dispatch or the pairing-only transport. Native questions exclusively own Yes; no confirmation boolean is exposed.")
