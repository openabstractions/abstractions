namespace * abstraction.resource
include "../abstraction-rights/rights.thrift"

// Who holds a scarce resource on this machine, and leases that are asked
// back. The service holds resources, not models (VISION 2026-09-05); a model
// is one thing that occupies them. Proposal and measurement:
// research/resources/PROPOSAL.md, research/resources/MEASUREMENT-2026-09-22.md.
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
 5: bad_timestamp(stage="grammar")
 6: depth_exceeded(stage="grammar")
 7: duplicate_key(stage="grammar")
 8: duplicate_field(stage="structure")
 9: unknown_field(stage="structure")
 10: missing_field(stage="structure")
 11: bad_enum(stage="structure")
 12: bad_value(stage="structure")
 13: trailing_bytes(stage="document")
}
typedef string timestamp(write="rfc3339-micros",read="rfc3339-wide")

// Resource names: card:<n> for an accelerator's memory, memory for system
// memory, awake for the wake hold. On an APU card:<n> and memory report the
// same bytes; a reader never sums them.
const list<string> known_resources = ["card:0","memory","awake"]

// verified: the instrument measured the amount (Windows GPU process counters,
// Linux fdinfo). claimed: the holder or its host adapter asserted it.
enum Evidence {
 1: verified
 2: claimed
}(unknown="refuse",reader="act",doc="[Evidence](https://openabstractions.github.io/reference.html#concept-resource.measurement-evidence): The resource table's accounting class: bytes measured by an instrument are verified, while bytes asserted by a holder or its adapter are claimed.")
enum ResourceCallOutcome {
 1: ok
 2: forbidden
 3: unavailable
 4: invalid
 5: unknown
}(unknown="refuse",reader="act")
struct Holder {
 1: required string program
 2: required string account
 3: required i64 amount
 4: required Evidence evidence
 5: optional string lease(omit="zero")
 6: optional string grant(omit="zero")
 7: optional timestamp since(omit="zero")
 8: optional string detail(omit="zero")
}(unknown_fields="refuse",doc="program and account are the subject as rights names it: an absolute image path or a package family, and a Windows SID or POSIX uid. amount is bytes. lease is set when the hold is a lease of lease@1; grant names the rights rule it sits under and is empty for a holder that never asked. detail is what the holder says it holds, such as a host's model name; it is a claim.")
struct ResourceState {
 1: required string resource
 2: required i64 capacity
 3: required i64 held
 4: required list<Holder> holders
 5: required timestamp observed
 6: required string instrument
}(document="true",unknown_fields="refuse",doc="capacity is bytes, 0 when the instrument cannot say. held is the sum of verified amounts only. instrument names what measured: windows-gpu-counters, linux-fdinfo, metal-working-set, which measures capacity and no holder, or none. Under metal-working-set or none every holder is claimed.")
struct HoldersResult {
 1: required ResourceCallOutcome outcome
 2: optional ResourceState state(omit="absent")
}(unknown_fields="refuse",doc="state is present exactly for ok. A forbidden outcome does not reveal another program's rows.")
struct ResourceList {
 1: required list<string> resources
 2: required timestamp observed
}(unknown_fields="refuse")
struct ResourcesResult {
 1: required ResourceCallOutcome outcome
 2: optional ResourceList list(omit="absent")
}(unknown_fields="refuse",doc="list is present exactly for ok.")
// Codes the Table and Leases handlers send on the reply error channel, beside
// the dispatcher's own.
const list<string> resource_error_codes = ["internal", "invalid_request", "caller_refused", "unknown_resource", "policy_unavailable", "forbidden"]

service Table {
 ResourcesResult Resources()(readonly="true",result_present="list",result_when="outcome",result_is="ok",doc="The resources this machine's instrument and adapters can report.")
 HoldersResult Holders(1:string resource,2:bool fresh)(readonly="true",result_present="state",result_when="outcome",result_is="ok",doc="Who holds resource now. fresh reads the instrument again; otherwise the last sample within its age bound. Gated by abstraction.resource/table.read on resource account; a program always sees its own rows, and capacity, held and observed describe the machine to every caller.")
}(wire_name="abstraction.resource/table@1",error_codes="resource_error_codes",doc="Read-only: who holds how much of a scarce resource on this machine, verified by the instrument or claimed by the holder. No row is invented: a holder the instrument cannot attribute to a program is reported under its process image with evidence verified and no account when the platform gives none. The table changes nothing.")

enum AcquireOutcome {
 1: acquired
 2: insufficient
 3: holders_refused
 4: not_permitted
 5: unavailable
 6: invalid
 7: forbidden
}(unknown="refuse",reader="act")
struct Lease {
 1: required string id
 2: required string resource
 3: required i64 amount
 4: required timestamp since
 5: required timestamp renew_by
}(unknown_fields="refuse",doc="amount is the requested admission reservation, not a physical memory quota. A [Lease](https://openabstractions.github.io/reference.html#concept-resource.lease) not renewed by renew_by is released; the holder's bytes are then reported as a holder without a lease until the instrument stops seeing them.")
struct YieldRecord {
 1: required string holder
 2: required i64 amount
 3: required string answer
 4: required i64 took_ms
}(unknown_fields="refuse",doc="One holder asked during Acquire: yielded, refused, or unanswered within the wait, with the reason a refusal gave.")
struct AcquireResult {
 1: required AcquireOutcome outcome
 2: optional Lease lease(omit="absent")
 3: required list<YieldRecord> asked
}(unknown_fields="refuse",doc="lease is present exactly for acquired. asked lists every holder the service asked to yield for this request, in order, whatever the outcome.")
enum AcquireFreeOutcome {
 1: acquired
 2: insufficient
 3: not_permitted
 4: unavailable
 5: invalid
 6: forbidden
 7: held
}(unknown="refuse",reader="act")
struct AcquireFreeResult {
 1: required AcquireFreeOutcome outcome
 2: optional Lease lease(omit="absent")
 3: required list<YieldRecord> asked
}(unknown_fields="refuse",doc="lease is present exactly for acquired. asked is always empty: AcquireFree asks nobody to yield. Its outcomes belong to this new method; Acquire retains its published closed vocabulary.")
struct YieldRequest {
 1: required string id
 2: required string resource
 3: required i64 amount
 4: required string reason
 5: required timestamp at
 6: required string lease
}(unknown_fields="refuse",doc="A request that this holder give back the named lease of amount bytes of resource. reason names the program that asked, as rights names it.")
struct YieldPage {
 1: required list<YieldRequest> requests
 2: required string next
}(unknown_fields="refuse")
struct ObserveResult {
 1: required ResourceCallOutcome outcome
 2: optional YieldPage page(omit="absent")
}(unknown_fields="refuse",doc="page is present exactly for ok.")
enum YieldAnswer {
 1: yielded
 2: refused
}(unknown="refuse",reader="act")
struct AnswerResult {
 1: required bool recorded
}(unknown_fields="refuse")
struct AnswerCallResult {
 1: required ResourceCallOutcome outcome
 2: optional AnswerResult answer(omit="absent")
}(unknown_fields="refuse",doc="answer is present exactly for ok.")
struct LeaseChange {
 1: required bool applied
 2: optional Lease lease(omit="absent")
}(unknown_fields="refuse")
struct LeaseChangeResult {
 1: required ResourceCallOutcome outcome
 2: optional LeaseChange change(omit="absent")
}(unknown_fields="refuse",doc="change is present exactly for ok; applied false retains the idempotent result for a lease not held by the caller.")
enum ProcessBindOutcome {
 1: bound
 2: unverifiable
 3: refused
 4: invalid
 5: forbidden
 6: unavailable
}(unknown="refuse",reader="act")
struct ProcessBindResult {
 1: required ProcessBindOutcome outcome
}(unknown_fields="refuse",doc="bound permits a verified child process's measured bytes to offset this lease's admission reservation. unverifiable and refused leave the full reservation in place; no process identity is published in the holder table.")
service Leases {
 AcquireResult AcquireFor(1:rights.Subject subject,2:string resource(length="1..1024",pattern="no_control"),3:i64 amount(range="0..9223372036854775807"),4:i64 wait_ms(range="0..120000"))(idempotent="false",result_present="lease",result_when="outcome",result_is="acquired",doc="Acquire under the original authenticated job owner's existing rule. Only an explicitly designated native enforcer of the receiving account may assert subject; this record supplies no proof or authority. The service decides abstraction.resource/hold for that subject, records the lease under it, and permits the acquiring designated process to maintain it through Renew and Release. Ordinary callers, cross-account assertions and invalid native program subjects are refused. Existing Acquire remains bound to its direct caller.")
 AcquireResult Acquire(1:string resource,2:i64 amount,3:i64 wait_ms)(result_present="lease",result_when="outcome",result_is="acquired",doc="Ask to reserve amount bytes for admission of the bound caller, waiting up to wait_ms (0..120000) for holders to yield. The reservation is not a physical usage quota. Order: the rights decision for abstraction.resource/hold on resource (not_permitted, or unavailable when the decision point cannot answer), then the table (insufficient when capacity is known and amount exceeds it), then holders are asked in the service's order and the result is acquired or holders_refused. Nothing is killed.")
 AcquireFreeResult AcquireFree(1:string resource,2:i64 amount)(result_present="lease",result_when="outcome",result_is="acquired",doc="Reserve amount bytes for the bound caller only from what is free now, asking nobody: a request that can take nothing from another holder, such as speculative work (RES-L6). Order: the rights decision for abstraction.resource/hold on resource (not_permitted, or unavailable when the decision point cannot answer), then the table (insufficient when the pool is known and amount exceeds it), then one grant decision under the book's lock: acquired when amount fits what is free, or when the resource has no pool and nothing holds it, otherwise held. asked is always empty: no yield request is ever sent.")
 LeaseChangeResult Renew(1:string lease)(result_present="change",result_when="outcome",result_is="ok",doc="Extend renew_by of the bound caller's own lease.")
 LeaseChangeResult Release(1:string lease)(idempotent="true",result_present="change",result_when="outcome",result_is="ok",doc="End the bound caller's own lease now.")
 ObserveResult Observe(1:string cursor,2:i64 wait_ms)(idempotent="true",result_present="page",result_when="outcome",result_is="ok",doc="Yield requests addressed to the bound caller since cursor, waiting up to wait_ms (0..30000) for one. Each request names the lease to yield. A holder that never observes is recorded as unanswered.")
 AnswerCallResult Answer(1:string request,2:YieldAnswer answer,3:string reason)(result_present="answer",result_when="outcome",result_is="ok",doc="Record the bound caller's answer to one yield request. yielded is checked against the instrument before the asker is told; a refusal carries its reason into the audit.")
 ProcessBindResult BindProcess(1:string lease,2:i64 pid)(doc="Associate the bound caller's own lease with one directly spawned process when its process start identity, parent relation and account can be verified. A failed or unsupported verification returns unverifiable and keeps conservative reservation accounting. One process may bind to one live lease.")
 LeaseChangeResult Label(1:string lease,2:string text(length="0..200",pattern="no_control"))(idempotent="true",result_present="change",result_when="outcome",result_is="ok",doc="Name what the bound caller's own lease holds for a person reading the machine's memory, written as app: model. text is at most 200 bytes on one line. The label is for display and the audit and carries no authority. An empty text clears it, and a reader then shows the program's name. applied false retains the idempotent result for a lease not held by the caller.")
}(wire_name="abstraction.resource/leases@1",error_codes="resource_error_codes",doc="[Leases](https://openabstractions.github.io/reference.html#concept-resource.leases) on a scarce resource, asked back on demand. An app running OA's integration holds leases; memory no lease covers is foreign, measured and never asked to yield. Every ask, yield and refusal is a record with the asker, the holder, the amount and the time. Version 1 kills nothing and boosts nothing.")
