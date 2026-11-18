namespace * abstraction.rights.api

// Shared rights concepts; native binding supplements are explicit below.
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
 11: bad_binary(stage="structure")
 12: bad_value(stage="structure")
 13: trailing_bytes(stage="document")
}
struct Request {
 1: required string op
 2: optional string name(omit="absent")
 3: optional list<string> rights(omit="zero")
 4: optional string secret(omit="absent")
 5: optional string token(omit="absent")
 6: optional string right(omit="absent")
 7: optional string why(omit="absent")
 8: optional string app(omit="absent")
 9: optional string admin(omit="absent")
}(document="true",unknown_fields="refuse",doc="Deprecated native token-service request descriptor. Removal is scheduled for Train B (expected 0.9.0), W2-01a.1, under TD13/TD57's stored-state and declared outside-adopter retirement conditions. Secret/token/admin remain credential inputs checked by the service; request text never substitutes for native peer identity. This descriptor does not replace the existing handwritten line transport.")
struct AppMetadata {
 1: required string id
 2: required string name
 3: required list<string> rights
 4: required string registered
}(unknown_fields="refuse",doc="Own application registration fields; registered uses the existing RFC3339 time representation. Native App additionally carries identity.listen.Seen observation evidence.")
struct HoldMetadata {
 1: required string app
 2: required string name
 3: required string right
 4: required string why
 5: required string since
}(unknown_fields="refuse",doc="Own held-right fields; since uses existing RFC3339 time representation. Native Hold also carries Seen. The live [Lease](https://openabstractions.github.io/reference.html#concept-rights.lease) is connection-owned and cannot be recreated from these fields.")
struct ResponseMetadata {
 1: optional string code(omit="absent")
 2: optional string error(omit="absent")
 3: optional string secret(omit="absent")
 4: optional string token(omit="absent")
 5: optional string expires(omit="absent")
 6: optional AppMetadata app(omit="absent")
 7: optional string right(omit="absent")
 8: optional list<AppMetadata> apps(omit="zero")
 9: optional list<HoldMetadata> holds(omit="zero")
}(unknown_fields="refuse",doc="Deprecated native token-service response descriptor. Removal is scheduled for Train B (expected 0.9.0), W2-01a.1, under TD13/TD57's stored-state and declared outside-adopter retirement conditions. Own response fields are projected without native Seen evidence. Any nonempty code/error is refusal, including unknown codes. This is a common descriptor, not a replacement legacy Response codec or generated service client.")
const list<string> operations = ["register", "ask", "hold", "check", "apps", "grant", "revoke", "forget", "holds"]
const list<string> known_rights = ["awake"]
const list<string> refusal_codes = ["internal", "invalid_request", "caller_refused", "unknown_operation", "not_administrator", "pending", "denied", "denied_permanently", "unsupported_hold", "platform_refused", "unknown_app", "unknown_right", "not_granted", "bad_secret", "bad_token"]

struct Subject {
 1: required string account
 2: required string program
}(unknown_fields="refuse",doc="[Subject](https://openabstractions.github.io/reference.html#concept-rights.subject): Receiving policy account and program target. Native programs use the shared absolute-path/MSIX grammar; the distinct remote kind is remote:<64 lowercase hex leaf-SPKI-SHA256> from the shared live-pin mapping. Remote targets match exact account/token/action/resource only, with no defaults, wildcard selectors or native operator authority. This serialized subject is an assertion by an explicitly authorized native enforcement point, contains no proof and cannot authorize its own use. Direct decisions derive native receiving [Program](https://openabstractions.github.io/reference.html#concept-identity.listener-program-need) evidence. Authority setters and actors remain native.")
enum DecisionOutcome {
 1: permitted
 2: denied
 3: not_granted
 4: unknown_action
 5: invalid
 6: forbidden
 7: unavailable
}(unknown="refuse",reader="act")
struct Decision {
 1: required DecisionOutcome outcome
 2: optional string policy_revision(omit="absent")
}(unknown_fields="refuse",doc="A point-in-time policy decision. Only permitted allows an enforcement point to proceed. denied is an explicit exact deny; not_granted has no unexpired exact rule; unknown_action is outside the current catalogue. Evaluated permitted/denied/not_granted/unknown_action decisions carry an opaque revision observed with that decision. Errors carry none. No lease, token, cached permission lifetime or human consent is conveyed.")
struct RevisionDecision {
 1: required Decision decision
 2: optional string observed_revision(omit="absent",length="..128",pattern="no_control")
}(unknown_fields="refuse",doc="A decision and bounded opaque freshness observation from the same state read. Evaluated decisions carry observed_revision. Refusals carry none. This observation conveys no permission lifetime.")
enum RefusalSource {
 1: rights
 2: credentials
 3: license
 4: terms
 5: identity
 6: os
}(unknown="grant",reader="display")
enum RefusalDecision {
 1: denied
 2: not_granted
 3: person_only
}(unknown="refuse",reader="act")
enum RefusalAsk {
 1: available
 2: none
}(unknown="refuse",reader="act")
struct RefusalDetail {
 1: required RefusalSource source
 2: required RefusalDecision decision
 3: required RefusalAsk ask
 4: optional string action(omit="absent",length="1..128")
 5: optional string question(omit="absent",length="1..128")
 6: optional string credential(omit="absent",length="1..64")
 7: optional i64 retry_after_ms(omit="zero",range="0..86400000")
}(unknown_fields="grant",doc="The one refusal record every OA service carries beside a refusing outcome (RIGHTS-E3; conventions CONV-E4). source names the authority that refused: rights, credentials, license, terms, identity or os. decision is denied for an explicit refusal, not_granted where no rule permits, and person_only where only the person may answer the question. ask says whether asking can change the answer: available, or none. action is the exact registered <owner>/<name>; question the pending question's id; credential the credential's name; retry_after_ms the earliest useful retry in milliseconds, omitted when zero. Stop asking is denied with ask none. Current carriers include config@2, messages@1, registry@3, endpoint@2 and chat@3, declared in config.thrift, messages.thrift, registration.thrift and inference.thrift. Other services adopt it in their scheduled versions.")
service Authorization {
 RevisionDecision DecideForAtLeastRevision(1:Subject subject,2:string action(length="1..128",pattern="no_control"),3:string resource(length="1..1024",pattern="no_control"),4:string at_least_revision(length="..128",pattern="no_control"))(readonly="true",doc="Evaluate through the existing explicitly admitted enforcer boundary. Empty minimum requests an observation. A nonempty minimum is mandatory: an older or incomparable state refuses unavailable. Unknown method never permits fallback dropping the minimum. Both observations and minima are bounded opaque values, not permission leases.")
 DescriptionPage ListActionDescriptions(1:string cursor,2:i64 limit)(readonly="true",doc="Read published descriptive metadata for the current catalogue under same-account receiving [Program](https://openabstractions.github.io/reference.html#concept-identity.listener-program-need) admission. No rules are exposed. Pages carry one durable revision; continuations bind subject, host epoch and revision and return gap after change/restart. Requested limit is 1..64 and encoded replies are at most 256 KiB. Recheck admission before returning. An earlier authorization@1 service returns unknown_method.")
 Decision Decide(1:string action,2:string resource)(readonly="true",doc="Evaluate the bound receiving account/program against an exact action/resource rule. This result is advisory to the caller; resource services query the decision point themselves.")
 Decision DecideFor(1:Subject subject,2:string action,3:string resource)(readonly="true",doc="Evaluate a subject assertion only when the receiving peer is explicitly authorized as an enforcement point for this action/resource. Same account alone grants no relay authority. The designated enforcer is trusted to supply its actual bound subject. Nil or refusing enforcer policy yields forbidden before policy lookup.")
}(wire_name="abstraction.rights/authorization@1",doc="General exact-rule authorization decision point with a bounded, registrable catalogue and persistence. Unknown, absent, unspecified, expired, failed and refused decisions never permit. Explicitly authorized operator configuration registers actions and grants/revokes exact rules; no serialized awake lease is introduced.")
// The OA seed of the action catalogue. A policy host starts from the seed it is
// configured with; resource services register further <owner>/<name> actions
// through RegisterAction or the native registration API.
const list<string> resource_actions = ["abstraction.storage/content.read", "abstraction.storage/content.write", "abstraction.job/acceptance.submit", "abstraction.job/acceptance.cancel", "abstraction.config/user.replace", "abstraction.logging/history.read", "abstraction.model/lookup", "abstraction.router/inventory.read", "abstraction.router/route", "abstraction.storage/content.observe", "abstraction.storage/content.remove"](catalogue="open")


struct PolicyRule {
 1: required Subject subject
 2: required string action
 3: required string resource
 4: required bool permit
}(unknown_fields="refuse",doc="One exact policy rule. Subject is the administrative target, never caller proof. Account is 1..128 UTF-8 bytes; program is a normalized absolute path up to 4096 bytes; action is 1..128 bytes and belongs to the current catalogue; resource is 1..1024 bytes. All strings exclude control characters. permit=false is an explicit deny; revocation removes the exact rule and restores not_granted.")
enum PolicyPageOutcome {
 1: page
 2: gap
 3: invalid
 4: forbidden
 5: unavailable
}(unknown="refuse",reader="act")
struct PolicyPage {
 1: required PolicyPageOutcome outcome
 2: required string revision
 3: required list<string> catalog
 4: required list<PolicyRule> rules
 5: required string next
 6: required bool complete
}(unknown_fields="refuse",doc="Latest-policy enumeration: 1..64 requested rules and at most 256 KiB encoded reply, including catalogue, cursor and indentation. Catalogue is the configured seed plus registered actions, sorted, bounded to 64 actions of 128 bytes. Rules are every retained rule, including expired rules the next write removes; ReadRule reports expiry and provenance. Page carries the exact durable content revision. A noncomplete page has a nonempty next cursor. Refusals have empty revision/catalog/rules/next and complete=false. Cursors are at most 256 UTF-8 bytes, bind receiving account/program and host epoch/revision, and return gap after change/restart/scope mismatch. Restart from empty cursor. No immutable multipage snapshot or historical change replay is promised.")
enum PolicyObservationOutcome {
 1: changed
 2: unchanged
 3: gap
 4: invalid
 5: forbidden
 6: unavailable
}(unknown="refuse",reader="act")
enum PolicyObservationMechanism {
 1: native
}(unknown="refuse",reader="act")
struct PolicyObservation {
 1: required PolicyObservationOutcome outcome
 2: required string next(length="0..256",pattern="no_control")
 3: required string revision(length="0..128",pattern="no_control")
 4: required PolicyObservationMechanism mechanism
}(unknown_fields="refuse",doc="Read-only policy invalidation. changed carries a new nonempty opaque cursor and current durable revision; an empty after requests this snapshot. unchanged carries the confirmed after cursor and current revision. Expiry changes the observation cursor while durable revision stays unchanged; ReadRule confirms expiry. gap carries after and empty revision: restart from empty after. invalid/forbidden/unavailable carry empty next/revision. Cursors bind receiving Program, host epoch and expiry-sensitive state, never enumerate rules or grant a permission lease. native declares policy-owner commit callbacks plus expiry deadlines. At most 256 cursor bytes and 128 revision bytes; no rule contents, replay or second policy store.")
enum PolicyEditOutcome {
 1: applied
 2: conflict
 3: invalid
 4: forbidden
 5: unavailable
}(unknown="refuse",reader="act")
struct PolicyEdit {
 1: required PolicyEditOutcome outcome
 2: required string revision
 3: optional PolicyRule current(omit="absent")
}(unknown_fields="refuse",doc="Applied/conflict carry the revision and optional exact current rule observed inside the conditional edit; absent current means no exact rule. Other outcomes carry no revision/current. A stale expected revision always conflicts, including when the desired state happens to match. No-op edits at the matching revision preserve it without writing. A lost reply is uncertain: retrying the same expected revision cannot overwrite a later edit, and may conflict after a successful original change. Reconcile the returned current state or fresh history before choosing another edit. This is optimistic concurrency, not an exactly-once mutation journal.")
struct RuleRecord {
 1: required PolicyRule rule
 2: required Subject set_by
 3: required string set_at
 4: required string why
 5: required string expires
}(unknown_fields="refuse",doc="One exact rule with its provenance. set_by is the subject the rights service established for the party that set the rule: the operator's receiving Program evidence, or the service's own account and executable for native edits. The request never supplies it. set_at is the service's UTC edit time as RFC 3339 with milliseconds. why is the operator's reason of 0..256 UTF-8 bytes without control characters. expires is empty for a rule without expiry, or the UTC instant, in the same format, from which the rule decides nothing.")
enum RuleReadOutcome {
 1: found
 2: expired
 3: unknown
 4: invalid
 5: forbidden
 6: unavailable
}(unknown="refuse",reader="act")
struct RuleRead {
 1: required RuleReadOutcome outcome
 2: required string revision
 3: optional RuleRecord record(omit="absent")
}(unknown_fields="refuse",doc="found and expired carry the revision and the exact record. expired names a retained rule at or past its expiry; it decides nothing and the next policy write removes it. unknown carries the revision and no record. invalid, forbidden and unavailable carry neither.")
struct CatalogEntry {
 1: required string action
 2: required Subject registered_by
 3: required string registered_at
}(unknown_fields="refuse",doc="One registered catalogue action. Seeded actions have no entry. registered_by is the subject the rights service established for the registering party: the operator's receiving Program evidence, or the service's own account and executable for native registration. registered_at is the service's UTC time as RFC 3339 with milliseconds.")
struct ActionDescription {
 1: required string action
 2: required Subject feature_app
 3: required string app_label
 4: required string description
 5: required string declared_risk
}(unknown_fields="refuse",doc="Operator-declared description of a stable app feature. feature_app names the described app, never the grant/decision subject. app_label and description are trimmed control-free UTF-8 text of 1..128 and 1..512 bytes. declared_risk is trimmed 1..32 bytes of display metadata: routine, sensitive, administrative and unknown align existing words; unfamiliar words have unknown interpretation. Risk text changes no authorization, question tier or answerer authority. Metadata establishes no code behavior or vendor trust.")
enum PermissionFirstUse {
 1: none
 2: exact
 3: consuming_paired
}(unknown="refuse",reader="act")
struct PermissionDefinition {
 1: required string action
 2: required string owner_contract
 3: required list<string> operations
 4: required string resource_profile
 5: required string resource_type
 6: required list<string> enforced_by
 7: required string disclosure
 8: required string lifecycle
 9: required PermissionFirstUse first_use
 10: required bool served
}(unknown_fields="refuse",doc="Service-owned definition metadata for one stable exact action. Operations identify owning IDL methods and existing domain effects. resource_profile is an immutable namespaced profile@version bound to a compiled owning validator by trusted composition; unknown profiles/versions are refused. Legacy exact-string profiles retain bounded exact bytes, including literal '*', and describe their resource owner's existing validation. served distinguishes implemented receiving enforcement from defined/unserved operations; unserved definitions admit no first-use request. first_use names only existing exact or consuming paired admission; dynamic tier/answerer facts remain separately service-owned. Disclosure and lifecycle explain data/effects and account, deletion and name-reuse boundaries. Metadata conveys no proof, grant, callback or scope selector. Changed authority requires a new action identity/version. This additive record is available to generated consumers; no service method publishes or registers definitions in this slice.")
struct DescribedAction {
 1: required string action
 2: required list<string> choices
 3: optional ActionDescription description(omit="absent")
 4: optional Subject published_by(omit="absent")
 5: optional string published_at(omit="absent")
}(unknown_fields="refuse",doc="One current catalogue action. choices is exactly allow, block, remove_rule, naming existing SetRule permit/deny and RevokeRule choices without authority to invoke them. Description and publication fields occur together or are all absent. description.action equals action. published_by is the operator subject established for publication; feature_app is its administrative declaration, and the imported descriptor's author/vendor remain unverified. published_at is service UTC RFC 3339 with milliseconds. Undescribed names carry no invented app information.")
enum ActionEditOutcome {
 1: applied
 2: conflict
 3: unknown
 4: invalid
 5: exhausted
 6: forbidden
 7: unavailable
}(unknown="refuse",reader="act")
struct ActionEdit {
 1: required ActionEditOutcome outcome
 2: required string revision
 3: optional CatalogEntry current(omit="absent")
}(unknown_fields="refuse",doc="applied, conflict and unknown carry the revision observed inside the conditional edit. applied and conflict carry the action's registration entry when one exists; a seeded or retired action has none. unknown means RetireAction named an action outside the catalogue. exhausted means the catalogue already holds 64 actions. invalid, exhausted, forbidden and unavailable carry no revision or entry. A stale expected revision always conflicts.")
struct DescriptionEdit {
 1: required ActionEditOutcome outcome
 2: required string revision
 3: optional DescribedAction current(omit="absent")
}(unknown_fields="refuse",doc="applied/conflict carry the revision and current described catalogue action when present; all other outcomes carry neither. A stale expected revision always conflicts. Identical metadata at the current revision is a no-op. A valid correction changes descriptor publication provenance/time and revision, preserving all grants, denies, targets, expiry and private answer receipts. Registration alone grants nothing.")
struct DescriptionPage {
 1: required PolicyPageOutcome outcome
 2: required string revision
 3: required list<DescribedAction> actions
 4: required string next
 5: required bool complete
}(unknown_fields="refuse",doc="Sorted current catalogue descriptions, including undescribed names, under one durable revision. 1..64 requested entries and at most 256 KiB encoded reply. Noncomplete pages have a nonempty next cursor and at least one entry; cursors are at most 256 UTF-8 bytes. Refusals have empty revision/actions/next and complete=false. No retained enumeration snapshot or historical replay.")
struct AccountDefault {
 1: required string name
 2: required string action
 3: required string resource
 4: required bool every_resource
 5: required bool on
 6: optional Subject set_by(omit="absent")
 7: optional string set_at(omit="absent")
}(unknown_fields="refuse",doc="One account-wide default the receiving runtime configures: a permit on action for every program of the runtime's own account that has no live rule of its own on the queried target. name is 1..64 bytes of lowercase ASCII letters, digits, '.', '_' and '-'. resource is the one exact resource the default covers; with every_resource it is empty and the default covers every resource of the action. on is the switch: true after a fresh install, until an operator turns it off. set_by and set_at occur together, name the operator subject the rights service established for the last switch edit and its service UTC time, and are absent while the switch was never turned. A per-program rule, a Block included, decides before any default; a configured excluded program, such as a script host, and every other account receive no default. A default writes no rule.")
struct DefaultPage {
 1: required PolicyPageOutcome outcome
 2: required string revision
 3: required list<AccountDefault> defaults
}(unknown_fields="refuse",doc="Every configured account-wide default, sorted by name, under one durable revision; at most 32 entries. Refusals have an empty revision and no defaults.")
struct DefaultEdit {
 1: required PolicyEditOutcome outcome
 2: required string revision
 3: optional AccountDefault current(omit="absent")
}(unknown_fields="refuse",doc="applied and conflict carry the revision and the named default's current state observed inside the conditional edit. Other outcomes carry neither. A stale expected revision always conflicts. Setting a switch to its current state at the matching revision is a no-op that keeps the revision.")
enum DecisionSource {
 1: none
 2: own_rule
 3: typed_block
 4: account_default
 5: default_excluded
}(unknown="refuse",reader="act")
struct DecisionExplanation {
 1: required DecisionOutcome outcome
 2: optional string policy_revision(omit="absent")
 3: optional DecisionSource source(omit="absent")
 4: optional string default_name(omit="absent")
}(unknown_fields="refuse",doc="One exact decision for a subject with the step that decided it, from the same evaluation a decision makes. own_rule is the subject's own live exact rule, matched by program identity; typed_block a live typed Block that covers the target; account_default the named account-wide default that permits; default_excluded a named default switched on for the target that the configured exclusion, such as the script-host rule, keeps from this program; none no rule and no default. outcome and policy_revision are those the decision carries. Refusals (invalid, forbidden, unavailable) carry no revision, source or name.")
enum ChangeKind {
 1: set_rule
 2: revoke_rule
 3: set_default
 4: register_action
 5: retire_action
 6: declaration
}(unknown="refuse",reader="act")
struct PolicyChange {
 1: required ChangeKind kind
 2: required string expected_revision(length="..128",pattern="no_control")
 3: optional PolicyRule rule(omit="absent")
 4: optional i64 ttl_ms(omit="zero",range="0..31536000000")
 5: optional string why(omit="zero",length="..256",pattern="no_control")
 6: optional string name(omit="zero",length="..64",pattern="no_control")
 7: optional bool on(omit="zero")
 8: optional string action(omit="zero",length="..128",pattern="no_control")
 9: optional string program(omit="zero",length="..4096",pattern="no_control")
 10: optional list<PolicyRule> rules(omit="zero",length="..64")
 11: optional string address(omit="zero",length="..2048",pattern="no_control")
}(unknown_fields="refuse",doc="One typed rights change an operator program requests (RIGHTS-O17). set_rule carries rule, and optionally ttl_ms and why with SetRuleFor's bounds; revoke_rule carries rule, whose permit is ignored; set_default carries name and on; register_action and retire_action carry action. declaration is a provider declaration or inference host add the registry or inference service admitted for the caller: name is the declared name, program its program (empty for a hosted server), address the server address it reaches (a hosted server's base URL or a remote runtime's endpoint; empty for a local program) and rules the exact rule set it writes on Allow; RequestChange refuses it as invalid. Other fields are absent. An empty expected_revision applies at the policy revision current when the change is applied; a nonempty one conflicts when the policy changed. The request is never free text: the service words the person's question from its own catalogue.")
enum ChangeRequestOutcome {
 1: applied
 2: pending
 3: refused
 4: expired
 5: conflict
 6: unknown
 7: invalid
 8: forbidden
 9: unavailable
}(unknown="refuse",reader="act")
struct ChangeRequestState {
 1: required ChangeRequestOutcome outcome
 2: required string id(length="..128",pattern="no_control")
 3: required string revision(length="..128",pattern="no_control")
 4: optional PolicyChange change(omit="absent")
 5: required string expires(length="..64",pattern="no_control")
 6: required bool exempt
}(unknown_fields="refuse",doc="One change request as the service holds it (RIGHTS-O17). pending waits for the person's answer in the service's popup until expires, a UTC RFC 3339 instant ten minutes after the request. applied means the service applied exactly this change, once, and carries the policy revision after it; exempt marks a change applied without a question for an elevated caller. refused is the person's Not now; expired is a request nobody answered in time, which changes nothing; conflict is an expected revision the policy no longer had. unknown, invalid, forbidden and unavailable carry no id, change or revision.")
struct ChangeRequestPage {
 1: required PolicyPageOutcome outcome
 2: required list<ChangeRequestState> requests(length="..64")
}(unknown_fields="refuse",doc="The requests this operator program made that the service still holds, newest first, at most 64. Refusals carry none.")
service AuthorizationOperator {
 PolicyObservation ObservePolicy(1:string after(length="0..256",pattern="no_control"),2:i64 wait_ms(range="0..30000"))(readonly="true",observe="after",doc="Under existing operator authority, wait 0..30000ms for policy invalidation through watch.Gate (RIGHTS-O16). Every read, wake and response rechecks receiving Program proof and explicit operator authority. The configured policy owner's OnChange callbacks and service expiry deadlines provide native invalidation. Invalid bounds are refused before data; stale/foreign epoch or receiving scope is gap. Cancellation ends only this read. A prior operator@1 service returns unknown_method; no listing-cursor or polling fallback.")
 DescriptionEdit RegisterDescribedAction(1:string expected_revision,2:ActionDescription description)(idempotent="true",doc="Under existing explicit operator authority, atomically register an action and publish/correct its descriptive metadata at the expected durable revision. The service records the receiving publisher and service time. Recheck context and authority after lock waiting. A metadata edit changes no rule, expiry or completed-answer receipt, including first publication on an action with existing rules. A stable action identifies its existing feature; actual changed semantics require a new action/version at the app owner. Metadata is no proof of behavior. An earlier operator@1 service returns unknown_method; never fall back to name-only registration while claiming published description.")
 PolicyEdit ApplyFirstUseAllow(1:string expected_revision,2:string question_id)(idempotent="true",doc="Apply the complete exact permit or paired permits derived by trusted runtime composition from this retained operator Allow and its runtime admission. No caller supplies targets or rules. Authorization, question occurrence and revision are checked while the [Book](https://openabstractions.github.io/reference.html#concept-asks.book) is pinned and the rights edit is locked. Commit complete permits and private durable success proof in the same rights replacement; a first receipt-only commit changes revision when permits already match. A previously completed ID conflicts without rewriting rights, including after revocation or expiry. Failed writes retain the answer for explicit Retry. Missing composition is unavailable; missing, pending, foreign, responder or non-Allow records are invalid. Both IDs and revision are bounded to 128 UTF-8 bytes. An earlier operator@1 service returns unknown_method; callers must not fall back to an untracked permit write.")
 PolicyPage ListPolicy(1:string cursor,2:i64 limit)(readonly="true",doc="Read bounded current rules and catalogue under explicit operator authorization. Every continuation rechecks authorization; no enumeration session is retained for disconnected/slow callers.")
 PolicyEdit SetRule(1:string expected_revision,2:PolicyRule rule)(idempotent="true",doc="Atomically compare the expected durable revision and set one exact permit/deny rule with no expiry and an empty reason. Revision is bounded to 128 UTF-8 bytes. Operator context and authority are checked again inside the atomic edit after lock waiting. The rule records the operator's established subject and the edit time. An unexpired rule with the same permit is a no-op that keeps its record. Uncatalogued actions and malformed targets are invalid; no resource permission is inferred from the operator request itself.")
 PolicyEdit RevokeRule(1:string expected_revision,2:Subject subject,3:string action,4:string resource)(idempotent="true",doc="Atomically remove one exact rule if the revision matches. The next fresh decision observes successful revocation. A failed/uncertain reply claims no successful revocation; inspect current state before another intent. It does not cancel an existing connection-owned awake lease.")
 PolicyEdit StopAnswerer(1:string expected_revision,2:Subject answerer,3:bool also_revoke_grants)(idempotent="true",doc="Atomically remove an answerer's question.answer/apps enrollment if the revision matches. When also_revoke_grants is true, remove current permits set by that answerer in the same edit; denies and rules set by another program stay. With false, prior permits retain their stored expiry. Stop ends future answers and never exits the answerer's process. Authorization and revision are rechecked after lock waiting.")
 PolicyEdit RevokeAnswerPermits(1:string expected_revision,2:Subject answerer,3:Subject target,4:list<string> actions,5:string resource,6:string question_id)(idempotent="true",doc="Atomically remove any still-current permit among one or two exact targets whose private question ID and setter match this retained first-use answer. Earlier or later grants, person edits, blocks and expired effects stay; zero matches or a revision change conflicts. The asks answer record stays; this method is the permission effect of the Panel's Undo. Authorization is rechecked after lock waiting.")
 PolicyEdit SetRuleFor(1:string expected_revision,2:PolicyRule rule,3:i64 ttl_ms,4:string why)(idempotent="true",doc="As SetRule, recording why and an expiry ttl_ms after the service's edit time. ttl_ms 0 sets no expiry; otherwise it is 1..31536000000. why is 0..256 UTF-8 bytes without control characters. A retained rule with the same permit, reason and expiry is a no-op; any other difference writes, including a new expiry.")
 PolicyEdit SetRulesFor(1:string expected_revision,2:list<PolicyRule> rules,3:i64 ttl_ms,4:string why)(idempotent="true",doc="Atomically set one or two exact rules with the same target subject, resource and permit, and distinct catalogued actions. The operator's authorized intent may replace a previous opposite rule. All members are validated before one revision and authority check under the rights edit lock; the batch writes all or none with one setter, time, reason and expiry. ttl_ms and why have SetRuleFor's bounds. A prior operator@1 service returns unknown_method.")
 RuleRead ReadRule(1:Subject subject,2:string action,3:string resource)(readonly="true",doc="Read one exact rule with its provenance and expiry under operator authorization, rechecked after the read.")
 ActionEdit RegisterAction(1:string expected_revision,2:string action)(idempotent="true",doc="Add an action named <owner>/<name> to the catalogue. owner is 1..64 bytes and name 1..63 bytes of lowercase ASCII letters, digits, '.', '_' and '-', each beginning with a letter or digit. Registering an action already in the catalogue applies without writing. A registration records the operator's established subject and the service time. Registration grants nothing: decisions on the action read not_granted until a rule exists.")
 ActionEdit RetireAction(1:string expected_revision,2:string action)(idempotent="true",doc="Remove a registered action and every rule naming it. Retiring a seeded action is invalid. Retiring an action outside the catalogue is unknown. Decisions on a retired action read unknown_action.")
 DecisionExplanation ExplainFor(1:Subject subject,2:string action,3:string resource)(readonly="true",doc="Under operator authorization, rechecked before returning, evaluate subject on one exact action/resource exactly as a decision does and report the step that decided it (RIGHTS-O14). It asks nobody, records nothing and changes no rule or cache. An earlier operator@1 service returns unknown_method.")
 DefaultPage ListDefaults()(readonly="true",doc="Read every configured account-wide default and its switch under operator authorization, rechecked before returning. An earlier operator@1 service returns unknown_method.")
 ChangeRequestState RequestChange(1:PolicyChange change,2:i64 wait_ms(range="0..30000"))(idempotent="true",doc="Request one typed rights change (RIGHTS-O17). The Panel window applies its changes through the direct methods. Any other operator program's change becomes a pending request the person confirms in a person-only popup the service words; the call waits up to wait_ms for the answer and then returns pending with the request id. A request expires ten minutes after it was made. Allow applies exactly that change, once. A caller whose own connection-bound token is elevated is exempt while the elevated-callers default is on: its change applies at once. Every applied change writes an Activity line with Undo. An earlier operator@1 service returns unknown_method; a caller must not fall back to a direct edit.")
 ChangeRequestState ObserveChangeRequest(1:string id(length="1..128",pattern="no_control"),2:i64 wait_ms(range="0..30000"))(readonly="true",doc="Wait up to wait_ms for one request this operator program made to settle and report its state (RIGHTS-O17). A request of another program, or one the service no longer holds, is unknown.")
 ChangeRequestPage ListChangeRequests()(readonly="true",doc="List the change requests this operator program made that the service still holds (RIGHTS-O17).")
 DefaultEdit SetDefault(1:string expected_revision,2:string name,3:bool on)(idempotent="true",doc="Atomically compare the expected durable revision and turn one configured account-wide default on or off, recording the operator subject and service time. An unconfigured name is invalid. Authorization and revision are rechecked after lock waiting. The next fresh decision observes the switch. No rule is written or removed. An earlier operator@1 service returns unknown_method.")
}(wire_name="abstraction.rights/operator@1",doc="Explicitly authorized administration of the configured decision policy: catalogue registration and retirement, exact rules with expiry and provenance. Receiving same-account Program proof plus a trusted typed-peer operator callback is required; nil refuses. Policy denial is forbidden; callback/storage failure is unavailable. No caller credential, verified flag or provider path is accepted. Resource services remain the receiving enforcement points. Legacy bearer and awake lease behavior is separate.")

struct PermissionKey {
  1: required string action
  2: required string resource_profile
}(unknown_fields="refuse")
enum ResourceSelectorKind { 1: one, 2: all }(unknown="refuse",reader="act")
struct ResourceSelector {
  1: required ResourceSelectorKind kind
  2: optional binary target(omit="absent")
}(unknown_fields="refuse",doc="One has one bounded canonical owning-IDL document. All omits target and selects the fixed profile collection within the rule subject's account/runtime. Operator inputs are prepared by the trusted owning profile; arbitrary captured identity evidence is refused.")
struct PermissionSelection {
  1: required PermissionKey permission
  2: required ResourceSelector selector
}(unknown_fields="refuse")
struct ConcreteResource {
  1: required PermissionKey permission
  2: required binary target
}(unknown_fields="refuse",doc="A concrete resource, never a grant selector. Historical captured evidence is accepted only from an explicitly authorized receiving enforcer; no direct caller obtains effect authority from this assertion.")
struct CheckBatchRequest {
  1: optional string expected_revision(omit="absent")
  2: optional PermissionKey collection_admission(omit="absent")
  3: required list<ConcreteResource> resources
}(unknown_fields="refuse")
enum BatchCheckOutcome {
  1: checked
  2: conflict
  3: invalid
  4: forbidden
  5: unavailable
}(unknown="refuse",reader="act")
struct BatchDecision {
  1: required BatchCheckOutcome outcome
  2: required string revision
  3: optional DecisionOutcome collection_outcome(omit="absent")
  4: required list<DecisionOutcome> outcomes
}(unknown_fields="refuse",doc="checked has one revision and ordered evaluated outcomes from one immutable policy snapshot and one clock sample. conflict has the observed revision and no outcomes. Other refusals have empty revision and no outcomes. A batch never mixes revisions or partially succeeds. No authority lease is issued.")

struct SelectorPermissionDefinition {
  1: required PermissionDefinition definition
  2: required string label
  3: required list<ResourceSelectorKind> supported_selectors
  4: required list<ResourceSelectorKind> first_use_selectors
  5: required string collection
  6: required string all_effect
  7: required string request_type
}(unknown_fields="refuse",doc="Full key is definition.action plus definition.resource_profile. Service-owned collection/all_effect state membership and present/future meaning. Only actually served receiving selectors are advertised. Existing PermissionDefinition.first_use remains the legacy projection; new selector first use uses first_use_selectors.")
struct PermissionDefinitionPage {
  1: required PolicyPageOutcome outcome
  2: required string revision
  3: required list<SelectorPermissionDefinition> definitions
  4: required string next
  5: required bool complete
}(unknown_fields="refuse")
struct PermissionBundleDefinition {
  1: required string bundle_ref
  2: required string owner_contract
  3: required string label
  4: required string target_type
  5: required list<ResourceSelectorKind> supported_selectors
  6: required list<PermissionKey> members
  7: required string disclosure
  8: required string definition_digest
}(unknown_fields="refuse",doc="Immutable finite expansion identified by namespaced bundle@version and deterministic content digest. This slice permits only member profiles with the same owning target document and collection. Each member validates independently. No mapping language, live role reference, future operation or permission-administration effect is implied.")
struct PermissionBundlePage {
  1: required PolicyPageOutcome outcome
  2: required string revision
  3: required list<PermissionBundleDefinition> bundles
  4: required string next
  5: required bool complete
}(unknown_fields="refuse")
struct BundleExpansion {
  1: required string bundle_ref
  2: required string definition_digest
  3: required ResourceSelector selector
  4: required list<PermissionSelection> selections
}(unknown_fields="refuse",doc="The exact original finite expansion, retained independently of later catalogue changes. All selected member profiles must accept the same canonical target/collection. A new version requires new consent.")
struct PermissionEffect {
  1: required Subject subject
  2: required list<PermissionSelection> selections
  3: optional BundleExpansion bundle(omit="absent")
}(unknown_fields="refuse",doc="Complete typed permit proposal retained by a trusted Asks admission owner. When bundle is present its selections equal this complete list. Native preparation establishes subject and target evidence. Text, a serialized subject or an app-supplied effect grants no authority.")
struct SelectorRule {
  1: required Subject subject
  2: required PermissionSelection selection
  3: required bool permit
}(unknown_fields="refuse")
struct BundleOrigin {
  1: required string id
  2: required BundleExpansion expansion
}(unknown_fields="refuse",doc="Rights-established historical application identity and immutable expansion. An origin explains how rules arose; it does not assert that all original members remain current.")
struct SelectorRuleRecord {
  1: required SelectorRule rule
  2: required Subject set_by
  3: required string set_at
  4: required string why
  5: required string expires
  6: optional BundleOrigin bundle_origin(omit="absent")
}(unknown_fields="refuse")
struct SelectorRulePage {
  1: required PolicyPageOutcome outcome
  2: required string revision
  3: required list<SelectorRuleRecord> records
  4: required string next
  5: required bool complete
}(unknown_fields="refuse")
struct SelectorRuleRead {
  1: required RuleReadOutcome outcome
  2: required string revision
  3: optional SelectorRuleRecord record(omit="absent")
}(unknown_fields="refuse")
struct SelectorEdit {
  1: required PolicyEditOutcome outcome
  2: required string revision
  3: required list<SelectorRuleRecord> current
  4: optional BundleOrigin bundle_origin(omit="absent")
}(unknown_fields="refuse",doc="Applied/conflict carry the locked observed revision and complete bounded affected state. Stale expected revision always conflicts, including matching desired state. Other refusals carry empty revision/current. Atomic edits have no per-member success. Individual replacement removes that row's active bundle origin; remaining rows keep historical origin.")

service SelectorAuthorization {
  PermissionDefinitionPage ListPermissionDefinitions(1:string cursor,2:i64 limit)(readonly="true",doc="List the typed permission definitions the catalogue serves: each action with its resource profile and the selectors it supports. Callers of the runtime's own account only. A page binds one policy revision; a cursor from another revision reads gap.")
  PermissionBundlePage ListPermissionBundles(1:string cursor,2:i64 limit)(readonly="true",doc="List the named permission bundles an operator can apply as one consent, each an immutable version with its member permissions. Callers of the runtime's own account only. A page binds one policy revision.")
  BatchDecision CheckBatch(1:CheckBatchRequest request)(readonly="true",doc="Decide the calling program's own typed permissions for 1..256 resources and an optional All collection admission, from one policy snapshot (RIGHTS-R19). Each target is the owning capability's document for the resource, which this service prepares; a caller supplies no captured evidence. The result is advisory to the caller: a resource service decides with CheckBatchFor. Asks nobody and writes nothing.")
  BatchDecision CheckBatchFor(1:Subject subject,2:CheckBatchRequest request)(readonly="true",doc="Decide a subject's typed permissions as CheckBatch does. The receiving host must designate the calling program as the enforcer of every requested permission and of the collection admission; otherwise forbidden. Subject and caller belong to the runtime's own account, and subject is the enforcer's assertion. Asks nobody and writes nothing.")
  Decision CheckLegacyOperationFor(1:Subject subject,2:LegacyOperationRequest request)(readonly="true",doc="Decide one exact authorization@1 action and resource for a subject with live typed blocks carried across known equivalence (RIGHTS-R20). The caller enforces the legacy action, and the concrete operation when one is supplied. It grants no typed permit.")
}(wire_name="abstraction.rights/authorization@2",doc="Typed authorization: permissions keyed by action and resource profile, decided for One resources and All collections from one snapshot. authorization@1 decides exact action and resource strings; a program uses this service for typed selectors. The Go client type rights.Client serves both services: ResolveRights binds authorization@1 for Decide, DecideFor, DecideForAtLeastRevision, Require and ListActionDescriptions, and ResolveRightsSelector binds this service for ListPermissionDefinitions, ListPermissionBundles, CheckBatch, CheckBatchFor and CheckLegacyOperationFor. A method of the other service answers unknown_method.")
struct LegacyOperationRequest {
  1: required string legacy_action
  2: required string legacy_resource
  3: optional ConcreteResource concrete_operation(omit="absent")
}(unknown_fields="refuse",doc="Old exact authority plus an optional receiver-known concrete effect. Trusted compiled capability equivalence validates the supplied effect against this old operation and its fixed collection. Original legacy authority and live equivalent typed restrictions are evaluated at one snapshot. Missing concrete detail on a coarse view uses conservative collection intersection. This method imports explicit blocks only; it grants no typed permit.")
service SelectorAuthorizationOperator {
  SelectorRulePage ListSelectorRules(1:string cursor,2:i64 limit)(readonly="true",doc="List every typed rule with its setter, time, reason, expiry and bundle origin. Operator programs only. A page binds one policy revision.")
  SelectorRuleRead ReadSelectorRule(1:Subject subject,2:PermissionSelection selection)(readonly="true",doc="Read the one rule for a subject and an exact permission selection, addressed by the selection's retained bytes with no new target preparation. Operator programs only.")
  SelectorEdit SetSelectorRule(1:string expected_revision,2:SelectorRule rule,3:i64 ttl_ms,4:string why)(idempotent="true",doc="Set one typed permit or block at expected_revision; a stale revision conflicts. ttl_ms 0 keeps the rule until removed, and at most one year bounds it. Replacing a row detaches its bundle origin (RIGHTS-O12). The Panel window's edit only (RIGHTS-O17).")
  SelectorEdit RemoveSelectorRule(1:string expected_revision,2:Subject subject,3:PermissionSelection selection)(idempotent="true",doc="Remove the rule for a subject and an exact permission selection at expected_revision. The Panel window's edit only (RIGHTS-O17).")
  SelectorEdit ApplyBundle(1:string expected_revision,2:Subject subject,3:string bundle_ref,4:ResourceSelector selector,5:i64 ttl_ms,6:string why)(idempotent="true",doc="Expand one immutable bundle version into explicit permits for a subject, atomically, with one setter and expiry and a retained bundle origin (RIGHTS-O12). New members of a later version need new consent. The Panel window's edit only (RIGHTS-O17).")
  SelectorEdit RemoveBundle(1:string expected_revision,2:Subject subject,3:string origin_id)(idempotent="true",doc="Remove a subject's rules that still carry bundle origin origin_id. Rows replaced since keep their later independent edits. The Panel window's edit only (RIGHTS-O17).")
  SelectorEdit ApplyPermissionAllow(1:string expected_revision,2:string question_id)(idempotent="true",doc="Write the typed effect pinned by the asks permission@1 question question_id after an operator Allow on it. The effect comes from the question, never from the caller. The Panel window and the Asks presenter only (RIGHTS-O17). A revoked or expired occurrence refuses replay.")
  SelectorEdit UndoPermissionEffect(1:string expected_revision,2:string question_id)(idempotent="true",doc="Remove the still-current rules that question_id's Allow wrote, keeping later edits and blocks. The Panel window's edit only (RIGHTS-O17).")
}(wire_name="abstraction.rights/operator@2",doc="Existing explicit operator admission, receiving provenance and in-lock authority recheck apply. First-use methods accept no caller-supplied targets: trusted composition resolves and pins the new typed Book occurrence. Existing operator@1 completion records are preserved with their original semantics.")
