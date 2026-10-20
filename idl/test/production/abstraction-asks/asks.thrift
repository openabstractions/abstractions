namespace * abstraction.asks.api
include "../abstraction-rights/rights.thrift"

// Shared asks concepts; native binding supplements are explicit below.
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
struct Question {
 1: required string asker
 2: required string key
 3: optional map<string,string> slots(omit="zero")
}(unknown_fields="refuse",doc="[Question](https://openabstractions.github.io/reference.html#concept-asks.question-ask) projects native [Ask](https://openabstractions.github.io/reference.html#concept-asks.question-ask) fields. The service owns question text/options; callers supply a known key and slots. Native Ask.For carries optional peer observation evidence and remains a transport supplement, never caller authority.")
struct Request {
 1: required string op
 2: optional Question ask(omit="absent")
 3: optional bool wait(omit="zero")
 4: optional string id(omit="absent")
 5: optional string option(omit="absent")
 6: optional string admin(omit="absent")
}(document="true",unknown_fields="refuse",doc="Deprecated native question-operation request descriptor. Removal is scheduled for Train B (expected 0.9.0), W2-01a.1, under TD13/TD57's stored-state and declared outside-adopter retirement conditions. Wait cancellation belongs to the native client context. This metadata descriptor does not replace the existing handwritten line transport.")
struct Answer {
 1: required string id
 2: optional bool pending(omit="zero")
 3: optional string option(omit="absent")
 4: optional bool yes(omit="zero")
 5: optional bool kept(omit="zero")
}(unknown_fields="refuse")
struct RecordMetadata {
 1: required string id
 2: required string asker
 3: required string key
 4: optional string about(omit="absent")
 5: required string text
 6: required list<string> options
 7: required string asked
 8: optional string option(omit="absent")
 9: optional string answered(omit="absent")
 10: optional bool kept(omit="zero")
 11: optional bool yes(omit="zero")
}(unknown_fields="refuse",doc="[RecordMetadata](https://openabstractions.github.io/reference.html#concept-asks.question-history-record) projects native question-history fields; timestamps retain existing RFC3339 representation. Native [Record](https://openabstractions.github.io/reference.html#concept-asks.question-history-record) includes Via/For Seen evidence from shared identity. Empty option means pending.")
struct ResponseMetadata {
 1: optional string code(omit="absent")
 2: optional string error(omit="absent")
 3: optional Answer answer(omit="absent")
 4: optional list<RecordMetadata> records(omit="zero")
}(unknown_fields="refuse",doc="Deprecated native question-operation response descriptor. Removal is scheduled for Train B (expected 0.9.0), W2-01a.1, under TD13/TD57's stored-state and declared outside-adopter retirement conditions. Own response projection. Any nonempty code/error refuses, including unknown future codes. Evidence supplement and existing wire handling remain native; no generated complete service interface is claimed.")
const list<string> operations = ["ask", "pending", "answered", "answer", "forget"]
const list<string> refusal_codes = ["internal", "invalid_request", "caller_refused", "unknown_operation", "not_administrator", "withdrawn", "unknown_question", "unknown_option", "bad_slot", "nothing_pending", "no_record"]

enum ObservationOutcome {
 1: pending
 2: answered
 3: unknown
 4: gone
 5: invalid
 6: conflict
 7: forbidden
 8: unavailable
}(unknown="refuse")
struct ApplicationQuestion {
 1: required string request_key
 2: required string key
 3: required map<string,string> slots
}(unknown_fields="refuse",doc="Stable caller-owned request key (1..128 UTF-8 bytes, no control characters), known question key and exactly its declared slots. Each slot is one line of at most 200 Unicode characters. No asker or authority fields are accepted. The receiving account and executable-path proof scope the request. Retry uncertain submission with this same key and unchanged content; a fresh key requests a new admission.")
struct QuestionObservation {
 1: required ObservationOutcome outcome
 2: optional Answer answer(omit="absent")
}(unknown_fields="refuse",doc="Answer is present exactly for pending/answered. Pending has no option/yes/kept decision. Unknown is an unobserved key in this caller's scope. Gone retains a forgotten admission's key and never admits it again. Conflict means the key was previously used with different content. Unavailable includes storage/capacity refusal and establishes no human decision. Answered reports the recorded human choice; it grants no resource authority by itself.")
service QuestionApplication {
 QuestionObservation Ask(1:ApplicationQuestion question)(idempotency_key="question.request_key",result_present="answer",result_when="outcome",result_is="pending,answered",doc="Admit or replay one caller-scoped question. No automatic retry. Identical key/content returns the original admission even after an answer marked once and a service restart. Existing unscoped kept answers are not promoted.")
 QuestionObservation Observe(1:string request_key,2:i64 wait_ms)(readonly="true",result_present="answer",result_when="outcome",result_is="pending,answered",doc="Observe only this caller's admission, optionally waiting 0..30000 milliseconds. Waiting expiry may return pending. Client timeout/cancellation ends waiting and neither answers nor cancels the question. Default SDK calls retain a five-second total waiting budget; callers explicitly requesting a longer budget must supply it through their transport/context API.")
}(wire_name="abstraction.asks/application@1",doc="Application-side human question admission and observation. Server derives caller scope and text, preserves native Via evidence, and owns a versioned bounded [Book](https://openabstractions.github.io/reference.html#concept-asks.book). Operator answering/history use the separately authorized [QuestionOperator](https://openabstractions.github.io/reference.html#concept-asks.question-operator) service; this application service exposes no administrative secret or operator methods.")


enum OperatorPageOutcome {
 1: page
 2: gap
 3: invalid
 4: forbidden
 5: unavailable
}(unknown="refuse")
struct OperatorPage {
 1: required OperatorPageOutcome outcome
 2: required list<RecordMetadata> records
 3: required string next
 4: required bool complete
}(unknown_fields="refuse",doc="Latest-book enumeration for an explicitly authorized operator, including pending and answered questions. Page has at most the requested 1..64 records and 256 KiB per encoded reply, with conservative record, indentation and envelope accounting. Complete means this snapshot was exhausted; otherwise next is nonempty. Refusals contain no records, next or complete flag. Opaque cursors (at most 256 UTF-8 bytes) bind receiving caller scope, host epoch and exact book revision. Change, restart or scope mismatch returns gap: restart from empty cursor. No historical change replay or stable snapshot across edits is promised. Native Via/For evidence stays server-side; display fields confer no authority.")
enum OperatorDecisionOutcome {
 1: answered
 2: conflict
 3: unknown
 4: invalid
 5: forbidden
 6: unavailable
}(unknown="refuse")
struct OperatorDecision {
 1: required OperatorDecisionOutcome outcome
 2: optional RecordMetadata record(omit="absent")
}(unknown_fields="refuse",doc="Record is present exactly for answered. Same ID and option replays the original decision including its timestamp and kept/once meaning; another option conflicts. Unknown means no retained question. Invalid includes an unknown option or invalid ID. Unavailable establishes no decision or noncommit claim: retry the same ID/option or inspect fresh history. Answering records a human choice, never a resource grant.")
enum OperatorRetirementOutcome {
 1: retired
 2: unknown
 3: invalid
 4: forbidden
 5: unavailable
}(unknown="refuse")
struct OperatorRetirement {
 1: required OperatorRetirementOutcome outcome
 2: optional RecordMetadata record(omit="absent")
}(unknown_fields="refuse",doc="Retired removes a retained pending or answered question from the Book. Record carries its last metadata on the retiring call and is absent when replaying an already retired ID. A pending question retired without an answer carries no decision. Admission tombstones remain: application replay and observation of its key report gone, and that key never admits again. Unknown means no retained or retired question. Invalid means a malformed ID. Unavailable establishes no retirement claim: retry the same ID or inspect fresh history.")
enum OperatorQuestionOutcome {
 1: found
 2: unknown
 3: invalid
 4: forbidden
 5: unavailable
}(unknown="refuse",reader="act")
enum Answerer {
 1: operator
 2: responder
}(unknown="refuse",reader="act")
struct OperatorQuestion {
 1: required OperatorQuestionOutcome outcome
 2: optional RecordMetadata record(omit="absent")
 3: optional string publisher(omit="absent")
 4: optional bool publisher_trusted(omit="zero")
 5: optional Answerer answered_by(omit="absent")
 6: optional string program_description(omit="absent")
 7: optional string program_original_name(omit="absent")
}(unknown_fields="refuse",doc="Found carries the retained question's metadata and the requester's publisher as the admitting service recorded it at admission: the code signer's subject and whether the platform trust policy accepted the signature. Absent publisher means none was recorded. Program description and program original name are the FileDescription and OriginalFilename of the requesting program's signed version resource, recorded with a trusted signature only; absent means none was recorded. Answered by names the service through which the recorded answer arrived: operator for QuestionOperator, responder for a delegated QuestionResponder answer. It is absent for a pending question and for an answer recorded before the service kept it. Only the service sets these fields; no caller supplies them. Every other outcome carries no record, publisher, program or answered by. Unknown means no retained question. Invalid means a malformed ID. Unavailable establishes nothing about the question.")
struct AttributedQuestion {
 1: required OperatorQuestion question
 2: optional string answerer(omit="absent")
}(unknown_fields="refuse",doc="Recorded metadata and attribution for a retained question. answerer is the program bound from the answering peer when answered_by is responder, and absent for every other answer and every refusal. The service supplies it; request text never does. This metadata read carries no permission target and remains available when a legacy target cannot be recovered.")
struct PermissionTarget {
 1: required string account
 2: required string program
 3: required string action
 4: required string resource
}(unknown_fields="refuse",doc="An exact service-owned first-use target. Account is the receiving runtime account, 1..128 UTF-8 bytes; program is a normalized subject program up to 4096 bytes; action is 1..128 bytes; resource is 1..1024 bytes. All exclude control characters. Display text supplies no target authority.")
struct PermissionRequest {
 1: required list<PermissionTarget> targets
}(unknown_fields="refuse",doc="The immutable permission effect retained with a trusted runtime occurrence. Exactly one primary target, or its uniquely determined content.read pair for content.write on input:image, input:audio, output:image, output:speech or output:video. A pair has the same account, normalized program and resource; independent secondary targets are refused. Applications cannot admit this record.")
struct QuestionDetails {
 1: required AttributedQuestion question
 2: optional PermissionRequest permission(omit="absent")
}(unknown_fields="refuse",doc="Opt-in structured permission details from the same retained occurrence as question. Permission is present only for a runtime first-use record; canonical legacy records are read through the explicit legacy reader. Other outcomes carry no permission. Existing operator responses retain their closed shapes. All exact target strings remain complete regardless of presentation limits.")
struct QuestionAsker {
 1: required QuestionDetails details
 2: optional string program(omit="absent")
}(unknown_fields="refuse",doc="Trusted permission details and asker attribution from one retained occurrence. program is the bound program the application service derived the question's subject from: the clean absolute executable path the platform proved at admission, up to 4096 bytes. The record's asker is its display name, derived from it. Program is absent for every refusal, for the runtime's own questions and for a question admitted before the service kept it. The service supplies it; request text never does. An unrecoverable legacy permission target returns unavailable without details or program.")
typedef string CatalogueSlotName(length="1..64",pattern="ascii_name")
typedef string CatalogueSlotValue(length="..800",pattern="no_control")
struct CatalogueContent {
 1: required map<CatalogueSlotName,CatalogueSlotValue> slots(length="..32")
 2: optional PermissionRequest permission(omit="absent")
}(unknown_fields="refuse",doc="Immutable catalogue content retained at admission. Slots are admitted display data, never instructions or authority. Permission is present only for a trusted runtime first-use occurrence and retains its existing immutable exact targets; this read neither grants nor applies it. An empty slot map is valid for a structured first-use admission. Missing old content is not reconstructed from rendered text.")
struct QuestionContent {
 1: required QuestionAsker question
 2: optional CatalogueContent content(omit="absent")
}(unknown_fields="refuse",doc="Opt-in content from the same retained occurrence as question. Content is present only when the Book retained it; absence explicitly means unavailable structured content, including legacy occurrences. Every refusal carries no content. Existing QuestionAsker and RecordMetadata stay unchanged.")
service QuestionOperator {
 QuestionContent ReadQuestionContent(1:string id(length="1..128",pattern="no_control"))(remote="local",readonly="true",doc="Read the existing authenticated QuestionAsker projection and optional retained catalogue content for the same occurrence. The same operator authorization and ID bounds apply, with no disclosure on refusal. Legacy absent content stays absent; no rendered-English recovery occurs. An earlier operator@1 service answers unknown_method.")
 AnswerContentPage ListAnswerContents(1:string cursor(length="..256"),2:i64 limit(range="1..64"))(remote="local",readonly="true",doc="Read the existing answer audit with optional immutable catalogue content in each entry, including after the original question is retired. Reuse operator authorization, newest-first audit snapshots, caller/epoch/revision cursor binding, seven-day retention and 1..64/256-KiB encoded page bounds including content. No per-entry follow-up read is needed. Legacy content is absent. An earlier operator@1 service answers unknown_method; ListAnswers keeps its closed result.")
 ChoicePage ListChoices(1:string cursor,2:i64 limit)(readonly="true",doc="Opt-in bounded enumeration of application-authored ordinary choices. Existing ListQuestions returns its unchanged catalogue records. The same operator authorization, caller/epoch/revision cursor binding and 1..64/256-KiB page bounds apply.")
 ChoiceRead ReadChoice(1:string id)(readonly="true",doc="Read one ordinary occurrence and verified requester attribution. Recheck authorization before disclosure. No permission target exists on this record.")
 ChoiceDecision AnswerChoice(1:string id,2:string option,3:bool if_pending)(doc="Record an offered stable option ID for an ordinary choice. Recheck operator authorization inside the edit. if_pending refuses every terminal occurrence with conflict; otherwise the same decision replays unchanged. An answer supplies application intent and no OA permission or policy effect.")
 ChoiceDecision DismissChoice(1:string id)(doc="Close one ordinary choice without selecting an option. The same dismissal replays; an answered choice conflicts. Expired pending choices cannot be answered or dismissed. No application work or resource policy is changed.")
 OperatorPage ListQuestions(1:string cursor,2:i64 limit)(readonly="true",doc="Read bounded current-book pages. Empty cursor starts enumeration; a gap requires an explicit restart. Receiver checks same-account [Program](https://openabstractions.github.io/reference.html#concept-identity.listener-program-need) identity and its configured operator authorization on every call before reading the [Book](https://openabstractions.github.io/reference.html#concept-asks.book). Slow/disconnected callers retain no server enumeration state.")
 OperatorDecision AnswerQuestion(1:string id,2:string option)(doc="Record an explicitly authorized operator's choice using the catalog's existing options. ID and option are 1..128 UTF-8 bytes without control characters. Atomic same-option replay preserves the original decision. No automatic retry and no implicit resource authorization. A missing operator policy refuses; account identity alone is insufficient.")
 OperatorRetirement RetireQuestion(1:string id)(doc="Retire one question for an explicitly authorized operator. ID is 1..128 UTF-8 bytes without control characters. Authorization and context are rechecked inside the atomic edit before removal. Retiring an already retired ID replays retired without a record. No automatic retry; retirement grants and revokes no resource authority.")
 AttributedQuestion ReadAttributedQuestion(1:string id)(readonly="true",doc="Read one retained question's recorded metadata, publisher and bound responder program for an explicitly authorized operator. ID is 1..128 UTF-8 bytes without control characters. Authorization is rechecked before disclosure. This metadata-only read does not recover a permission target. An earlier operator@1 service answers unknown_method.")
 ChoiceDetails ReadChoiceDetails(1:string id)(readonly="true",doc="Read one ordinary occurrence with its bound requester program, the options its application shared with an assistant and the route through which its decision arrived. Authorization is rechecked before disclosure. Only found carries details beyond the read outcome. An earlier operator@1 service answers unknown_method; ReadChoice keeps its closed result.")
 QuestionAsker ReadQuestionAsker(1:string id)(readonly="true",doc="Read one retained occurrence's service-owned structured permission target, answer attribution and bound asking program for an explicitly authorized operator. Authorization is rechecked before disclosure. The receiving account scopes every target. An unrecoverable legacy target returns unavailable. An earlier operator@1 service answers unknown_method; published methods and result shapes remain unchanged.")
 OperatorDecision AnswerQuestionIfPending(1:string id,2:string option)(doc="Record an explicitly authorized operator's choice only while the question is still pending. A question ID names content that never changes, so pending is the whole precondition. An answered question is conflict for every option, the same one included, and is never replayed. Other outcomes follow AnswerQuestion. No automatic retry: after an uncertain reply, inspect history, since an explicit retry of a landed answer reads conflict. Added to operator@1 as a method; an earlier service answers unknown_method.")
 AnswerEntryPage ListAnswers(1:string cursor,2:i64 limit)(readonly="true",doc="Read bounded pages of the retained answers to catalogue questions and ordinary choices that arrived through operator@1, permission-operator@1 or responder@1, newest first. Entries survive release, dismissal, acknowledgement and replay expiry for seven days after the answer, within the Book's answer-audit bound. complete_since_ms names the time from which the list is complete. The same operator authorization, caller/epoch/revision cursor binding and 1..64/256-KiB page bounds apply. An earlier operator@1 service answers unknown_method.")
}(wire_name="abstraction.asks/operator@1",doc="[QuestionOperator](https://openabstractions.github.io/reference.html#concept-asks.question-operator) provides operator history, answering and retirement on the configured application [Book](https://openabstractions.github.io/reference.html#concept-asks.book). [Host](https://openabstractions.github.io/reference.html#concept-asks.application-host) must explicitly authorize the receiving operator; requests contain no credentials, authority claims or provider paths. Authorization callbacks are trusted service configuration and may use the rights service. Retirement and native Forget both retain admission tombstones.")

struct RequestRef {
 1: required string key
 2: required i64 replay_until_ms
}(unknown_fields="refuse",doc="Caller-created immutable ordinary request identity: key is 1..128 UTF-8 bytes without controls and replay_until_ms is an absolute Unix millisecond deadline. Create it once and retain both fields unchanged for all retries. The initial provider admits at most seven days ahead. The same key with a different replay deadline is deliberate new intent. Effective ordinary time is the maximum of service UTC and a private durably recorded ordinary time floor. An observed expiry is committed before success and never reopens after rollback, collection or restart. A forward clock jump can expire choices permanently.")
struct ChoiceOption {
 1: required string id
 2: required string label
}(unknown_fields="refuse",doc="Stable application option ID, 1..64 UTF-8 bytes without controls, and a nonblank single-line label of at most 80 Unicode characters. Labels do not determine selection or authority.")
struct ChoiceRequest {
 1: required RequestRef request_ref
 2: required string title
 3: required list<ChoiceOption> options
 4: required i64 pending_until_ms
}(unknown_fields="refuse",doc="Application-authored ordinary choice. Title is nonblank, single-line and at most 80 Unicode characters. Exactly 1..3 distinct stable IDs are offered. pending_until_ms is an explicit absolute deadline after service time and no later than replay_until_ms. Full ref and request content are immutable: identical retry replays its occurrence; changed content under that full ref conflicts. The service derives requester attribution. This request carries no permission or configuration authority.")
enum ChoiceState {
 1: pending
 2: answered
 3: dismissed
 4: expired
}(unknown="refuse",reader="act")
struct ChoiceAnswer {
 1: required string id
 2: optional string option(omit="absent")
 3: required i64 answered_ms
 4: required string answered_by
}(unknown_fields="refuse",doc="Exact recorded occurrence and decision time. answered_by is the full valid subject program of the proven answering operator or owning requester, derived by the service. It proves program attribution, not human presence. An answered choice carries its offered option ID. A dismissal carries no option. This decision proves no application effect or resource permission.")
enum ChoiceOutcome {
 1: pending
 2: answered
 3: dismissed
 4: expired
 5: gone
 6: unknown
 7: conflict
 8: invalid
 9: exhausted
 10: forbidden
 11: unavailable
}(unknown="refuse",reader="act")
struct ChoiceObservation {
 1: required ChoiceOutcome outcome
 2: optional string id(omit="absent")
 3: optional ChoiceAnswer answer(omit="absent")
}(unknown_fields="refuse",doc="Pending, answered and dismissed carry the admitted occurrence ID. Exactly answered/dismissed carry its exact decision. Other outcomes carry neither. Expired means the pending or replay deadline elapsed, and creates no new occurrence. Gone is an acknowledged admission. Unknown means this full ref has no retained admission. Conflict is changed immutable content. Exhausted is bounded ordinary capacity; unavailable is an uncertain storage/service result. All establish no resource authority.")
enum ChoiceAckOutcome {
 1: acknowledged
 2: conflict
 3: unknown
 4: expired
 5: invalid
 6: forbidden
 7: unavailable
}(unknown="refuse",reader="act")
struct ChoiceAcknowledgement {
 1: required ChoiceAckOutcome outcome
}(unknown_fields="refuse")
service ChoiceApplication {
 ChoiceObservation Ask(1:ChoiceRequest request)(idempotency_key="request.request_ref.key",doc="Admit or replay one caller-scoped ordinary choice. No automatic retry. Reuse the entire reference and unchanged request after an uncertain reply. A missing catalogue entry is irrelevant: application text and offered IDs are explicit.")
 ChoiceObservation Observe(1:RequestRef request_ref,2:i64 wait_ms)(readonly="true",doc="Observe only this caller's full reference, optionally waiting 0..30000 milliseconds. Wait cancellation or timeout changes no decision. Recheck pending/replay deadlines and binding after waking. SDKs never manufacture a fresh deadline on retry.")
 ChoiceDecision Answer(1:RequestRef request_ref,2:string option,3:bool if_pending)(idempotency_key="request_ref.key",doc="Record an offered option for this proven caller's own ordinary full reference. Requester scope and reference are looked up inside the atomic edit after binding recheck; no operator grant or ask-admission policy is required. if_pending conflicts with every terminal decision; false explicitly reconciles the same recorded answer with its original actor and time. No automatic retry, human-presence claim or permission effect. An older service returns unknown_method.")
 ChoiceDecision Dismiss(1:RequestRef request_ref)(idempotency_key="request_ref.key",doc="Dismiss only this proven caller's own ordinary full reference, rechecking binding and ownership inside the atomic edit. The same dismissal replays with its original actor and time; another terminal decision conflicts. No operator grant or ask-admission policy is required. Expired choices cannot be changed. An older service returns unknown_method.")
 ChoiceObservation ShareChoice(1:ChoiceRequest request,2:list<string> assistant_options)(idempotency_key="request.request_ref.key",doc="Admit or replay one caller-scoped ordinary choice as Ask does, sharing it with an assistant the person enrolled. assistant_options lists 1..3 distinct offered option IDs the assistant may choose; every other option and dismissal stay with the person and the application. The request and assistant_options together are the immutable content of the reference: Ask and ShareChoice under one reference, or another option list, conflict. Sharing conveys no OA permission. An older service returns unknown_method; a client then decides explicitly whether to Ask unshared.")
 ChoiceAcknowledgement Acknowledge(1:RequestRef request_ref,2:ChoiceAnswer answer)(idempotency_key="request_ref.key",doc="Acknowledge the exact recorded answer only once the caller no longer requires full service answer replay and has retained the answer/result required by its own recovery guarantees. Ack relinquishes full service replay and proves no application effect. Crash or relaunch recovery that still needs the answer must durably retain it or leave the occurrence unacknowledged. A one-shot caller with a completed durable result and no same-intent recovery after restart needs no separate answer receipt. For any explicit retry after an uncertain Ack reply, retain and use the exact recorded answer. Replaces the full record with a small immutable fingerprint/decision tombstone until replay_until_ms. The same acknowledgement safely replays after a lost reply. Later Ask/Observe reports gone; expiration never creates a fresh occurrence.")
}(wire_name="abstraction.asks/application@2",doc="Caller-bound ordinary application choices with explicit pending and replay deadlines, verified attribution and isolated bounded retention. The application owns ordinary presentation and may answer or dismiss its own occurrence through this service. Account-wide answering uses opt-in operator@1 methods. An enrolled assistant answers only choices the application shared through ShareChoice, and only their shared options (responder@1 ListChoices, AnswerChoice); exact ordinary delegation is a separate future profile. Legacy application@1 wire and lifetime behavior remains unchanged.")
struct ChoiceRecord {
 1: required string id
 2: required string asker
 3: required ChoiceRequest request
 4: required ChoiceState state
 5: required i64 asked_ms
 6: optional ChoiceAnswer answer(omit="absent")
 7: optional string publisher(omit="absent")
 8: optional bool publisher_trusted(omit="zero")
 9: optional string program_description(omit="absent")
 10: optional string program_original_name(omit="absent")
}(unknown_fields="refuse",doc="An ordinary occurrence with immutable application-authored request and service-recorded requester attribution. Exactly answered/dismissed carry a decision. Pending and expired select no option. Display text and option IDs carry no permission effect; no rights target is accepted or returned.")
struct ChoicePage {
 1: required OperatorPageOutcome outcome
 2: required list<ChoiceRecord> records
 3: required string next
 4: required bool complete
}(unknown_fields="refuse")
struct ChoiceRead {
 1: required OperatorQuestionOutcome outcome
 2: optional ChoiceRecord record(omit="absent")
}(unknown_fields="refuse",doc="Exactly found carries one ordinary record. Other outcomes carry none.")
enum ChoiceDecisionOutcome {
 1: answered
 2: dismissed
 3: expired
 4: gone
 5: unknown
 6: conflict
 7: invalid
 8: forbidden
 9: unavailable
}(unknown="refuse",reader="act")
struct ChoiceDecision {
 1: required ChoiceDecisionOutcome outcome
 2: optional ChoiceRecord record(omit="absent")
}(unknown_fields="refuse",doc="Exactly answered/dismissed carry the exact recorded ordinary decision. These outcomes record application intent only. Expired, gone, unknown, conflict, invalid, forbidden and unavailable carry no record or effect.")
enum ChoiceRoute {
 1: requester
 2: operator
 3: responder
}(unknown="refuse",reader="act")
struct ChoiceDetails {
 1: required ChoiceRead choice
 2: optional string asker_program(omit="absent")
 3: required list<string> assistant_options
 4: optional ChoiceRoute answered_via(omit="absent")
}(unknown_fields="refuse",doc="Opt-in details of a retained ordinary occurrence. choice is the same closed response ReadChoice returns. asker_program is the bound program the application service derived the requester's scope from, up to 4096 bytes; it is absent for refusals and for a choice admitted before the service kept it. assistant_options are the options the application shared with an assistant, empty for an unshared choice. answered_via names the service route of the recorded decision: requester for the application's own application@2 decision, operator for operator@1 and responder for an enrolled assistant's responder@1 answer. It is absent while pending, after expiry and for a decision recorded before the service kept it. Only the service sets these fields.")
enum AnswerKind {
 1: question
 2: choice
}(unknown="refuse",reader="act")
struct AnswerEntry {
 1: required string id
 2: required AnswerKind kind
 3: required string key
 4: required string asker
 5: optional string asker_program(omit="absent")
 6: optional string publisher(omit="absent")
 7: optional bool publisher_trusted(omit="zero")
 8: required string about
 9: required string title
 10: required string option
 11: required string label
 12: required Answerer answered_by
 13: optional string answerer(omit="absent")
 14: required i64 answered_ms
}(unknown_fields="refuse",doc="One answer the service recorded through operator@1, permission-operator@1 or responder@1, as it recorded it. kind question is a catalogue question: key is its catalogue key, about its About slot, title its text, and label its option. kind choice is an ordinary choice: key and about are empty, title is the application's title and label the chosen option's label. asker, asker_program and publisher come from the admission. answered_by names the route; answerer is the bound program of a responder and absent for an operator. Title and label may be application-authored text and decide nothing.")
struct AnswerEntryPage {
 1: required OperatorPageOutcome outcome
 2: required list<AnswerEntry> answers
 3: required string next
 4: required bool complete
 5: required i64 complete_since_ms
}(unknown_fields="refuse",doc="Exactly page carries answers, newest first. complete_since_ms is zero while every retained answer is listed; otherwise answers recorded at or before it may have been evicted by the Book's bound. Other outcomes carry no answers.")
struct AnswerContent {
 1: required AnswerEntry answer
 2: optional CatalogueContent content(omit="absent")
}(unknown_fields="refuse",doc="One unchanged answer audit entry with optional content retained with that answer. Content survives retirement of the original question under the same audit lifetime and byte bounds. Absence explicitly means unavailable content; it is never reconstructed from title text. Ordinary choices carry no catalogue content.")
struct AnswerContentPage {
 1: required OperatorPageOutcome outcome
 2: required list<AnswerContent> answers(length="..64")
 3: required string next(length="..256")
 4: required bool complete
 5: required i64 complete_since_ms
}(unknown_fields="refuse",doc="The existing bounded answer-audit page semantics with content included. Exactly page carries entries; every refusal has no entries or cursor. The completeness time, authorization and cursor binding retain ListAnswers semantics.")
struct AnswerableChoice {
 1: required string id
 2: required string asker
 3: required string title
 4: required list<ChoiceOption> options
 5: required i64 pending_until_ms
 6: optional string publisher(omit="absent")
 7: optional bool publisher_trusted(omit="zero")
}(unknown_fields="refuse",doc="A pending shared ordinary choice as an enrolled assistant may answer it: options holds only the shared options, in the order the application offered them. The title and labels are application-authored data and carry no instruction or authority.")
struct AnswerableChoicePage {
 1: required OperatorPageOutcome outcome
 2: required list<AnswerableChoice> choices
 3: required string next
 4: required bool complete
}(unknown_fields="refuse",doc="Exactly page carries choices. Other outcomes carry none.")

enum ResponderEffectOutcome {
 1: none
 2: applied
 3: conflict
 4: forbidden
 5: unavailable
}(unknown="refuse",reader="act")
struct ResponderDecision {
 1: required OperatorDecisionOutcome outcome
 2: optional RecordMetadata record(omit="absent")
 3: required ResponderEffectOutcome effect
 4: optional string target_outcome(omit="absent")
}(unknown_fields="refuse",doc="Answered means the question choice is recorded, including an idempotent same-option replay. Effect reports the separate exact resource-rule write: none for a refusal, applied for a successful bounded rule, conflict for a policy revision change or an already-answered replay, forbidden for expired or revoked delegation, unavailable for an unconfirmed policy write. A recorded answer may have an unapplied effect. An answered replay never writes target policy: inspect target outcome and reconcile through an explicit operator edit. Target outcome is the current rights decision word when a fresh read succeeds. No caller-supplied target or rule is accepted.")
service QuestionResponder {
 OperatorPage ListAnswerable(1:string cursor,2:i64 limit)(readonly="true",doc="List at most 1..64 pending runtime first-use questions this proven caller may currently answer: routine ones under its enrollment (question.answer on apps, ASK-R5), and routine ones an exact option-specific responder grant names (ASK-R3). A question that is not routine is never listed (ASK-R6). A returned record contains only the options this caller may answer. Cursors bind caller subject, host epoch and Book revision; gap requires a restart. Recheck grants before returning a page. No general question history is exposed.")
 ResponderDecision Answer(1:string id,2:string option)(doc="Answer a routine runtime first-use question only when this caller holds a current enrollment (ASK-R5) or a current exact, expiring, option-specific responder grant (ASK-R3), is no party to it and is no script host (ASK-R7). Every other question is refused to every answerer (ASK-R6). An allow under an enrollment lasts until the enrollment ends (ASK-R4). Recheck the grant under the Book edit lock and before any target rule write. Same-ID/option replay preserves the answer and never writes policy; a different delegated choice conflicts, while a choice without a grant is forbidden. Refuse stale, foreign, forged or out-of-scope questions. A first allow creates a bounded target permission when its guarded policy edit applies; refusal writes no target rule.")
 AnswerableChoicePage ListChoices(1:string cursor,2:i64 limit)(readonly="true",doc="List at most 1..64 pending ordinary choices their applications shared with an assistant, which this proven caller may currently answer under its enrollment (ASK-R9). Each carries only its shared options. A choice whose requester, publisher or bound peer is this caller, or a caller the host refuses, is never listed. Cursors bind caller subject, host epoch and ordinary revision; gap requires a restart. Recheck the answerer before returning a page.")
 rights.Decision RequestAnswering()(doc="Ask for the right to answer for the person: the enrollment, abstraction.asks/question.answer on apps (ASK-R10). The receiving account and program are bound from the peer; a script host, an operator program or another account reads forbidden. A current enrollment reads permitted. Otherwise the decision is not_granted and the runtime admits its ordinary first-use question for that right, which the person answers in the native permission prompt and an answerer never can; an Allow writes the same enrollment the operator UI writes. A refused Answer or AnswerChoice for want of an enrollment asks the same way. An earlier responder@1 service answers unknown_method.")
 ChoiceDecision AnswerChoice(1:string id,2:string option)(doc="Record a shared option for a pending shared ordinary choice under the caller's current enrollment (ASK-R9). The check is repeated inside the Book edit. The same answerer repeating the same option replays answered with the original actor and time; every other terminal state conflicts. An unshared choice or option is forbidden. There is no responder dismissal. An answer supplies application intent and no OA permission or policy effect.")
}(wire_name="abstraction.asks/responder@1",doc="Narrow delegated first-use answering, and answering of ordinary choices applications share with an assistant. The host derives question provenance and the target from its own Book, binds the responder from native same-account Program proof, and consults the current rights policy. It accepts no authority, target, program, resource or expiry claim from the caller. An operator remains separately privileged.")

struct PermissionIntent {
 1: required string request_key
 2: optional rights.PermissionSelection selection(omit="absent")
 3: optional string bundle_ref(omit="absent")
 4: optional rights.ResourceSelector bundle_selector(omit="absent")
 5: optional string purpose(omit="absent")
}(unknown_fields="refuse",doc="One selection OR a registered immutable bundle reference and selector. The receiving native peer supplies the subject. Trusted capability preparation canonicalizes owner-IDL targets and admits only served first-use selectors; caller FileKey/subject assertions grant no authority. Purpose is attributed display text. A stable key replays the frozen original occurrence, never a later bundle expansion.")
enum PermissionOutcome {
 1: pending
 2: answered
 3: unknown
 4: gone
 5: invalid
 6: conflict
 7: forbidden
 8: unavailable
 9: permitted
 10: denied
}(unknown="refuse",reader="act")
struct PermissionObservation {
 1: required PermissionOutcome outcome
 2: optional Answer answer(omit="absent")
 3: required string policy_revision
}(unknown_fields="refuse",doc="Pending/answered carries the retained answer. Permitted/denied is an admission-time current-policy result. An Allow answer supplies no proof of a committed rule effect; retry the enforcing capability. Observe uses only caller-owned admission and has no rule editing authority.")
struct TypedPermissionDetails {
 1: required AttributedQuestion question
 2: optional rights.PermissionEffect effect(omit="absent")
 3: required string effect_digest
 4: required string origin
 5: required string purpose
 6: optional PermissionDisplay display(omit="absent")
}(unknown_fields="refuse",doc="Found carries the complete immutable trusted effect, its digest, app/service origin and separately attributed purpose. Other outcomes have no effect and empty strings. Existing exact details methods retain their original meaning and refuse typed entries.")
struct PendingQuestion {
 1: required PermissionObservation observation
 2: optional i64 pending_until_ms(omit="zero")
}(unknown_fields="refuse",doc="observation reads pending or answered with the question's id, or a refusal with no answer. pending_until_ms is the Unix millisecond time a pending question expires (ASK-A7) and is zero for every other outcome.")
service PermissionApplication {
 PermissionObservation Request(1:PermissionIntent request)(result_present="answer",result_when="outcome",result_is="pending,answered",idempotency_key="request.request_key")
 PermissionObservation Observe(1:string request_key,2:i64 wait_ms)(result_present="answer",result_when="outcome",result_is="pending,answered",readonly="true")
 PendingQuestion ObserveQuestion(1:string id(length="1..128",pattern="no_control"),2:i64 wait_ms(range="0..30000"))(remote="local",readonly="true",doc="Wait up to wait_ms for the person's answer to one first-use question about this proven caller's own program, named by a refusal (inference INF-W11) or by Request. Any other id, another program's question included, reads unknown and discloses nothing. Pending at the end of the wait is not consent; an Allow is no proof of a committed rule, so retry the enforcing call once. Grants, answers and admits nothing (ASK-A14). An earlier permission@1 service answers unknown_method.")
}(wire_name="abstraction.asks/permission@1",doc="Native caller-bound OA permission requests under compiled served definitions and supported first-use selectors, current explicit-block checks and Book quota/replay/refusal pacing. This capability-owned consent admission requires no ordinary question.ask permit. Trusted service missing-permit admission remains separate. Neither admission nor application prose grants Rights authority. This profile exposes no application answering or operator methods.")
service PermissionOperator {
 OperatorPage ListPermissions(1:string cursor,2:i64 limit)(readonly="true")
 TypedPermissionDetails ReadPermissionDetails(1:string id)(readonly="true")
 OperatorDecision AnswerPermissionIfPending(1:string id,2:string option)
}(wire_name="abstraction.asks/permission-operator@1",doc="Explicitly authorized operator reading and conditional answering of immutable typed OA permission occurrences. This slice grants no delegated responder authority. Rights operator@2 completes or undoes the retained effect by occurrence ID; an answer and its effect remain separate commits.")

struct PermissionDisplaySelection {
 1: required string operation
 2: required string target
 3: required string collection
 4: required string all_effect
 5: required string disclosure
}(unknown_fields="refuse",doc="Trusted capability-authored explanation aligned in order with the frozen effect selections. Monitor never parses target binary or caller prose into this projection.")
struct PermissionDisplay {
 1: required list<PermissionDisplaySelection> selections
 2: required string bundle_label
 3: required string bundle_disclosure
 4: required bool requires_os_authorization
}(unknown_fields="refuse",doc="Immutable trusted display metadata pinned with the complete effect. Breadth follows each typed selector independently of OS authorization and answering tier.")

enum NotificationActivation {
 1: app
 2: open_activity
}(unknown="refuse",reader="act")
struct NotificationAction {
 1: required string id
 2: required string title
 3: required NotificationActivation activation
}(unknown_fields="refuse",doc="Stable application action ID, 1..64 UTF-8 bytes without controls, and a nonblank single-line title of at most 40 Unicode characters. app records application intent for the poster to observe. open_activity also opens the person's Activity view at the request's own operation and requires operation_id. Titles do not determine the recorded action or any authority (ASK-N2).")
enum NotificationDelivery {
 1: now
 2: operation_end
}(unknown="refuse",reader="act")
struct NotificationRequest {
 1: required RequestRef request_ref
 2: required string title
 3: optional string body(omit="absent")
 4: required list<NotificationAction> actions
 5: required i64 expires_ms
 6: optional string operation_id(omit="absent")
 7: optional string tag(omit="absent")
 8: required NotificationDelivery delivery
}(unknown_fields="refuse",doc="Application-authored one-way notification (ASK-N1, ASK-N2). Title is nonblank, single-line and at most 80 Unicode characters; body is 1..240 Unicode characters without control characters other than line feed. 0..2 distinct actions. expires_ms is an absolute deadline after service time and no later than replay_until_ms. operation_id names one job operation the same caller scope accepted, 1..128 lowercase ASCII letters, digits and '-'; open_activity and operation_end require it. A tag of 1..64 UTF-8 bytes replaces this caller's live notification with the same tag. operation_end holds the notification scheduled until the trusted runtime observes that operation end. Full ref and request content are immutable. The service derives the sender; no request field names a program, publisher or icon.")
enum NotificationState {
 1: scheduled
 2: posted
 3: acted
 4: dismissed
 5: withdrawn
 6: expired
}(unknown="refuse",reader="act")
struct NotificationResult {
 1: required string id
 2: required NotificationState state
 3: optional string action(omit="absent")
 4: required i64 decided_ms
 5: optional string decided_by(omit="absent")
}(unknown_fields="refuse",doc="The exact terminal outcome of one occurrence: acted, dismissed or withdrawn. Exactly acted carries the offered action ID. decided_by is the full subject program of the proven operator or posting caller that recorded it, derived by the service. It proves program attribution, not human presence, and no application effect.")
enum NotificationOutcome {
 1: scheduled
 2: posted
 3: acted
 4: dismissed
 5: withdrawn
 6: expired
 7: gone
 8: unknown
 9: conflict
 10: invalid
 11: suppressed
 12: exhausted
 13: forbidden
 14: unavailable
}(unknown="refuse",reader="act")
struct NotificationObservation {
 1: required NotificationOutcome outcome
 2: optional string id(omit="absent")
 3: optional NotificationResult result(omit="absent")
}(unknown_fields="refuse",doc="Scheduled, posted, acted, dismissed and withdrawn carry the admitted occurrence ID; exactly acted, dismissed and withdrawn carry the result. Expired means the expiry or replay deadline elapsed with no recorded outcome, or the replay deadline elapsed; it carries nothing. Gone is an acknowledged occurrence. Unknown means this full ref has no retained admission. Conflict is changed immutable content. Suppressed is a live explicit block on this program; nothing was stored or shown. Exhausted is a rate, live or capacity bound; nothing was stored. Unavailable is an uncertain storage or decision result. None establishes resource authority.")
service NotificationApplication {
 NotificationObservation Post(1:NotificationRequest request)(idempotency_key="request.request_ref.key",doc="Admit or replay one caller-scoped notification (ASK-N1). No automatic retry: reuse the entire reference and unchanged request after an uncertain reply. A blocked program receives suppressed; rate, live and capacity bounds return exhausted. Neither stores anything.")
 NotificationObservation Observe(1:RequestRef request_ref,2:i64 wait_ms)(readonly="true",doc="Observe only this caller's full reference, optionally waiting 0..30000 milliseconds for an outcome. Waiting changes nothing. Recheck deadlines and binding after waking.")
 NotificationObservation Withdraw(1:RequestRef request_ref)(idempotent="true",doc="Withdraw this proven caller's own scheduled or posted notification. A presenter removes its native item. The same withdrawal replays; another terminal outcome is returned unchanged.")
 ChoiceAcknowledgement Acknowledge(1:RequestRef request_ref,2:NotificationResult result)(idempotency_key="request_ref.key",doc="Acknowledge the exact recorded result once the caller no longer requires service replay (ASK-N5). Replaces the record with a small fingerprint tombstone until replay_until_ms; later Post/Observe report gone. The same acknowledgement replays after a lost reply.")
}(wire_name="abstraction.asks/notification@1",doc="Caller-bound application notifications whose actions the service holds after the poster exits (ASK-N1..N7). Admission is decided by abstraction.asks/notification.post on account; a live explicit block suppresses. The session presenter shows posted notifications natively through notification-operator@1. A notification grants no authority and collects no text.")

struct NotificationRecord {
 1: required string id
 2: required string asker
 3: required string program
 4: required NotificationRequest request
 5: required NotificationState state
 6: required i64 admitted_ms
 7: optional i64 posted_ms(omit="zero")
 8: optional NotificationResult result(omit="absent")
 9: optional string publisher(omit="absent")
 10: optional bool publisher_trusted(omit="zero")
 11: optional string program_description(omit="absent")
 12: optional string program_original_name(omit="absent")
 13: optional string presentation(omit="absent")
}(unknown_fields="refuse",doc="One occurrence with its immutable application-authored request and service-recorded sender: the bound program (a clean absolute path up to 4096 bytes), its display name and the publisher evidence recorded at admission. posted_ms is set once the notification is posted. Exactly acted, dismissed and withdrawn carry a result. presentation is present only in operator reads: a staleness token bound to this record and the current host epoch, which Act requires.")
struct NotificationPage {
 1: required OperatorPageOutcome outcome
 2: required list<NotificationRecord> records
 3: required string next
 4: required bool complete
}(unknown_fields="refuse",doc="Retained occurrences in admission order under the operator page and cursor rules of ListQuestions: 1..64 records, 256 KiB, cursors bound to the caller, host epoch and revision. Refusals carry no records.")
struct NotificationRead {
 1: required OperatorQuestionOutcome outcome
 2: optional NotificationRecord record(omit="absent")
}(unknown_fields="refuse",doc="Exactly found carries one record.")
enum NotificationDecisionOutcome {
 1: acted
 2: dismissed
 3: withdrawn
 4: expired
 5: gone
 6: unknown
 7: conflict
 8: stale
 9: invalid
 10: forbidden
 11: unavailable
}(unknown="refuse",reader="act")
struct NotificationDecision {
 1: required NotificationDecisionOutcome outcome
 2: optional NotificationRecord record(omit="absent")
 3: optional i64 count(omit="zero")
}(unknown_fields="refuse",doc="Acted, dismissed and withdrawn carry the record as recorded; a replay of the same outcome returns the original actor and time. Conflict means another terminal outcome stands. Stale means the presentation token belongs to another showing or host epoch; nothing was recorded. Expired, gone, unknown, invalid, forbidden and unavailable carry no record. count is the number of notifications a WithdrawProgram call withdrew.")
struct NotifierDay {
 1: required i64 day_ms
 2: required i64 posts
}(unknown_fields="refuse",doc="Admitted posts on the service UTC day starting at day_ms.")
struct NotifierCount {
 1: required string program
 2: required string asker
 3: required list<NotifierDay> days
 4: required i64 last_posted_ms
}(unknown_fields="refuse",doc="Admitted posts for one bound program on each of the last seven service UTC days that had any, oldest first, and its last admission time. Counts include notifications later withdrawn or expired.")
struct NotifierPage {
 1: required OperatorPageOutcome outcome
 2: required list<NotifierCount> notifiers
 3: required string next
 4: required bool complete
}(unknown_fields="refuse")
service NotificationOperator {
 NotificationPage ListNotifications(1:string cursor,2:i64 limit)(readonly="true",doc="List retained notification occurrences for an explicitly authorized operator, each with a presentation token. Authorization is rechecked before the page is returned.")
 NotificationRead ReadNotification(1:string id)(readonly="true",doc="Read one occurrence with its presentation token.")
 NotificationDecision Act(1:string id,2:string action,3:string presentation)(doc="Record that the person chose one offered action on a posted notification (ASK-N4). Inside the atomic edit the service rechecks operator authorization, the presentation token, state posted, service time before expires_ms, the offered action and the live block on the poster. The first recorded action wins; the same action replays; any failed check records nothing.")
 NotificationDecision Dismiss(1:string id)(doc="Record that the person dismissed one scheduled or posted notification without an action. The same dismissal replays; another terminal outcome conflicts.")
 NotificationDecision WithdrawProgram(1:string program)(doc="Withdraw every scheduled and posted notification of one bound program, for example after the person turned its notifications off. count reports how many changed.")
 NotifierPage ListNotifiers(1:string cursor,2:i64 limit)(readonly="true",doc="List per-program post counts for the last seven days under the same page rules (ASK-N6).")
}(wire_name="abstraction.asks/notification-operator@1",doc="Explicitly authorized presentation, action recording, dismissal and audit of application notifications. The session presenter and the Panel are its clients. A recorded action is application intent only.")

// The rights actions an asks service enforces (ASK-R1, ASK-R3, ASK-R5):
// question.ask gates admission on account; question.answer gates one exact
// delegated first-use target and option, or, on the resource "apps", every
// app's routine questions. Absent means no. notification.post gates
// notification admission on account (ASK-N1, ASK-N6); a live block suppresses.
const list<string> resource_actions = ["abstraction.asks/question.ask", "abstraction.asks/question.answer", "abstraction.asks/notification.post"](catalogue="closed",closed_by="ASK-R1")
