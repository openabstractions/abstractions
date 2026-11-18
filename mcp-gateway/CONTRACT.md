# OpenAbstractions MCP gateway contract

Binds: none (an MCP projection of other services' contracts)

This page is the contract of `openabstractions-mcp`, the product adapter that
projects OA service calls to an MCP host over stdio under its own
OA identity. It owns the gateway's tool inventory, its principal and the
subject rules of agent hosts that launch it, its bounds, its handle records,
its MCP protocol versions and its command line. The services it calls keep
their own contracts; every OA call it makes uses the transport of the
abstraction-ipc contract, and
its translation of OA outcomes into MCP follows IPC-E2. [README.md](README.md)
is the walkthrough: building it and configuring OpenCode.

Trust domain: one account on one machine. The gateway is one OA client
program; the runtime binds it as the executable that runs it, and every MCP
host on the account that launches that executable shares its subject, the
gateway subject. MCP metadata from the host is untrusted data from inside
that domain.

## Reading this page

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY" and "OPTIONAL" in this page are to be
interpreted as described in RFC 2119 and RFC 8174, when, and only when, they
appear in all capitals, as shown here.

A rule id such as `MCPG-T1` is declared once, in bold brackets before its
title, at the head of the rule it names; tests cite it the same way. A
retired id is never reused. Each rule ends with one of two marks. **Held
by** names the test or check that goes red without the rule. **Untestable
here** gives the reason no test holds it yet and names the package that
brings its scenario.

| letter | topic |
|---|---|
| A | admission: the gateway's principal and the subjects of agent hosts (S12's reserved meaning) |
| E | error and outcome (S12's reserved meaning) |
| T | the tool inventory |
| B | bounds |
| H | handle records and durable work |
| V | MCP protocol versions and the adapter's own version |
| C | the command line |

`X` (extension) is reserved and unused here.

## Tool inventory

The gateway's current tools include native method projections and composed
conveniences. The `class` column fixes each tool's
MCP annotations: `read` sets `readOnlyHint` and `idempotentHint` and
`openWorldHint: false`; `effect` sets `openWorldHint: true` and
`destructiveHint: false`; `submit` adds `idempotentHint` to `effect`;
`cancel` sets `idempotentHint`, `openWorldHint: true` and
`destructiveHint: true`. `command` sets `readOnlyHint: false`,
`idempotentHint: false`, `openWorldHint: true` and `destructiveHint: true`,
conservatively covering every routed native command class.
`read_open` keeps the read-only and idempotent hints with `openWorldHint: true`
for service-filtered questions and choices. `setup` sets `idempotentHint` and
`openWorldHint: true`, with `readOnlyHint: false`; it leaves `destructiveHint`
unspecified, retaining MCP's conservative default of true.
The `help` column says whether `--help` lists the
tool today. `scripts/unit_contracts.py` builds the gateway, reads its
`tools/list` and its `--help`, and fails when either differs from this table.

| tool | OA service | class | bound | help |
|---|---|---|---|---|
| `oa_applications_list` | `abstraction.facade/applications@2` | read | permission-filtered names, titles, instances, interfaces and contexts; 262,144 returned bytes | printed |
| `oa_applications_describe` | `abstraction.facade/applications@3` | read | FAC-M22 ApplicationListing under native caller permissions; 1 MiB native reply | printed |
| `oa_applications_enrollment_read` | `abstraction.facade/applications@3` ReadAgentEnrollment | read | own enrollment or application.read on app:<name>; another app's program is empty; 1 MiB native reply | printed |
| `oa_api_describe` | generated public method metadata | read | projected method summaries or one method's generated argument/result schemas; 1 MiB reply | printed |
| `oa_api_read` | generated Rights, Logging, Router Models/Hosts and operator readonly methods | read | exact native contract/method/arguments; 1 MiB arguments and reply; 25-second native context | printed |
| `oa_applications_commands_list` | `abstraction.facade/applications@3` | read | FAC-M32 CommandListing for one live instance; 1 MiB native reply | printed |
| `oa_applications_command_call` | `abstraction.facade/applications@3` | command | FAC-M32 GatewayCall and exact application answer record; 1 MiB native request/reply | printed |
| `oa_applications_open` | `abstraction.facade/applications@3` Activate | submit | FAC-M32 declared open verb and payload for a kept application; ActivationResult with the instance and a sentence naming the app by its title; 1 MiB native request/reply | printed |
| `oa_models_list` | `abstraction.router/router@1`, `abstraction.model/catalog@1` | read | servable names, the stores that hold each model, and the reviewed models Setup sets up here as `setup_needed` entries with profile, download and preset times | printed |
| `oa_models_connect` | composed `abstraction.asks/permission@1` Request/Observe and `abstraction.router/router@1` Hosts | setup | one fixed catalogue name; native person-only setup, actual submitted question IDs and own-caller servable readback; no key or inference input | printed |
| `oa_setup_fit` | `abstraction.model/catalog@1` | read | one use; the catalogue's ranked entries with fit word, sizes and the pick, without source or digest | printed |
| `oa_setup_prepare` | `abstraction.facade/applications@3` RequestSetup | effect | one use, code, image or video; asked, already_set_up or cannot_run with a reason; the person answers OA's own question and no download decision is reported | printed |
| `oa_inference_complete` | `abstraction.inference/chat@1`; `chat@3` for a labelled local call | effect | an explicit route; 65,536 input bytes; 4,096 output tokens; 262,144 returned bytes; an optional `conversation` label of at most 128 bytes, defaulting to the connection's own chat for a local call, used only for engine slot affinity inside the caller's own cache partition | printed |
| `oa_inference_requests` | `abstraction.inference/operator@1` Requests | read | native audit.read; integer cursor and 1..256 entries with recorded selection and placement | printed |
| `oa_image_generate` | composed `abstraction.job/acceptance@1` and `operations@1` with the inference job document, `abstraction.inference/selection-operator@2` ReadEffective and `abstraction.storage/content-reader@1` | effect | listed only while the caller's `oa-image-default` policy names a local model; `prompt` with optional `size`, `model` or `selectionPolicy`, and `inputImage` with optional `mask` to edit, or `handle` alone; one local picture; at most 50 seconds of waiting; the file is created exclusively in `Pictures/OpenAbstractions` | printed |
| `oa_video_generate` | composed `abstraction.job/acceptance@1` and `operations@1` with the inference job document, `abstraction.inference/selection-operator@2` ReadEffective and `abstraction.storage/content-reader@1` | effect | listed only while the caller's `oa-video-default` policy names a local model; `prompt` with optional `model`, `preset` (`quick` or `quality`), `size` and `duration_ms`, or `handle` alone; one local video; `wait_ms` at most 50,000; answers `path` when the job ends within the wait, else `handle` and `phase`; the clip is created exclusively in `Videos/OpenAbstractions` when the job ends | printed |
| `oa_inference_job_submit` | `abstraction.job/acceptance@1` with the inference job document | submit | listed only while no listed tool serves its profile, which today is never; it stays callable for advanced profiles; a stable request key; a local or hosted video request; an immutable receipt identity; optional `wait_ms` follows the accepted job as status does | printed |
| `oa_inference_job_status` | `abstraction.job/operations@1` and `abstraction.storage/content-reader@1` | read | one opaque handle; optional `wait_ms` waits for the job to end, with progress, within the call's budget; `phase` names what the job does now; a result of at most 65,536 bytes; a finished local video of at most 64 MiB is created exclusively in `Videos/OpenAbstractions` and answered as `path`, with `message` saying why it was not or that it has no audio track (PICTURES-STORAGE ruling 5) | printed |
| `oa_inference_job_diagnose` | `abstraction.job/operations@1` | read | one opaque handle; state, classified failure and fixed guidance, without raw failure text or result content; a submit refused at once answers its refusal word and plain sentence | printed |
| `oa_inference_job_cancel` | `abstraction.job/acceptance@1` | cancel | explicit cancellation intent for one handle | printed |
| `oa_jobs_follow` | `abstraction.job/operations@1` FollowWork | read | native acceptance scope and request identity; opaque cursor at most 256 bytes; wait 0..30000ms; 1 MiB generated reply | printed |
| `oa_logs_follow` | `abstraction.logging/reader@2` and `observer@2` | read | one registered application or this caller's requests; at most 64 records / 65,536 encoded bytes; at most 25 seconds | printed |
| `oa_asks_request_answering` | composed `abstraction.asks/permission@1` Request/ObserveQuestion and `abstraction.asks/responder@1` RequestAnswering | effect | empty input; this native program's enrollment only; person-only approval, with no caller identity input | printed |
| `oa_asks_list_answerable` | `abstraction.asks/responder@1` ListAnswerable | read_open | caller-bound cursor at most 256 bytes; 1..64 currently answerable routine questions | printed |
| `oa_asks_list_choices` | `abstraction.asks/responder@1` ListChoices | read_open | caller-bound cursor at most 256 bytes; 1..64 shared ordinary choices | printed |
| `oa_asks_answer` | `abstraction.asks/responder@1` Answer | effect | exact listed question and option IDs, each 1..128 characters; current native enrollment and party checks | printed |
| `oa_asks_answer_choice` | `abstraction.asks/responder@1` AnswerChoice | effect | exact shared choice and option IDs, each 1..128 characters; no permission effect | printed |

**[MCPG-T1] The current projected set.** The shared generic gateway's `tools/list` MUST equal the
tool inventory above, with each tool's class as its annotations. A call to
any other tool name is refused as an unknown tool. A row whose bound says
"listed only while" names its condition, read once per MCP session at its
first `tools/list` through a read that consults no right: listing tools
MUST NOT raise a question. `oa_image_generate` is listed while the caller's
`oa-image-default` policy (`selection-operator@2` `ReadEffective`) names a
local model; a session that starts before then lists it in the next
session. A read that gets no answer lists it. A call to an unlisted
inventory tool answers for itself. `oa_video_generate` is listed while the
caller's `oa-video-default` policy names a local model, read the same way;
Setup writes that policy when the router serves its video set. The job tools
carry video only. `oa_inference_job_submit` is never listed and stays
callable for advanced profiles; status, diagnose and cancel are always
listed for following a job (architect, 2026-11-12).
Held by:
`mcp-gateway/gateway/server_test.go` (`TestProtocolMatrixAndCuratedToolList`),
`mcp-gateway/gateway/image_test.go` (`TestToolVisibilityFollowsThePicturePolicy`,
`TestToolsListWithNoRulesRaisesNoQuestion`),
`mcp-gateway/gateway/video_watch_test.go` (`TestVideoToolIsListedWhileVideoIsServed`)
and `scripts/unit_contracts.py` (the inventory and its annotations).

The enrollment read uses the generated Applications@3 client and result codec
under this connection's actual native subject. The app itself and the native
operator retain its program path. Another app requires `application.read` on
`app:<name>` and receives no program path. It grants no operator enrollment or
update authority; the service retains typed `forbidden` for those calls.

**[MCPG-T5] Public API projection by default.** Every remote method of a public
OA service MUST be projected to MCP through its generated codecs under the
caller's native subject. Projection grants nothing. Until the emitter lands,
unprojected methods MUST be listed as Not built. An exclusion MUST name an
actual transport reason: a secret argument that cannot enter model context,
or a stream/wait that the carrier cannot bound. Operator authority is never
an exclusion reason; the service returns its typed refusal. Held by:
`scripts/unit_contracts_test.py` (`test_new_method_and_new_not_built_row_fail`) and
`scripts/unit_contracts.py` (the projection section).

The `--mcp` IDL emitter supplies native Go codec companions and machine-readable
method/class metadata. `oa_api_describe` and `oa_api_read` expose the readonly
Rights, Logging and operator profiles listed in `projection.tsv`. Every read
resolves the exact public contract with the connection's native subject and
uses the existing generated client, argument decoder and result encoder.
Native results retain their `value` wrapper and typed outcomes. Native service
errors retain their generated error record. IPC-E2's explicit refusal words
also set `isError` and a top-level structured `outcome`; generated answer text
stays intact. Transport and uncertain errors are
returned without an automatic retry. Generic effects and Config remain Not built in
this first slice. Existing ergonomic tools retain their current behavior.
Held by: `mcp-gateway/gateway/api_test.go`
(`TestAPIReadNativeAdmissionClassesAndExactNumbers`,
`TestAPIPublicReadsUseNativeSubjectAndOperatorRefusals`,
`TestAPIProjectionMatchesGeneratedCatalogue`,
`TestAPIRefusalMappingUsesIPCOutcomeTable`).

`projection.tsv` accounts for every native RPC method selected by the existing
public-definition and generation-target owners. A new unlisted method fails.
Methods in definitions generated only with `--no-ipc` are local primitives,
outside this RPC carrier inventory; they are not transport exclusions.
The `remote=local` exposure annotation still names a native RPC method and is
included. It does not grant a paired-computer or MCP caller authority.

The enrollment and model-setup tools compose narrower native calls. Their
presence does not project the complete Permission Request/Observe/ObserveQuestion, responder
RequestAnswering or Router Hosts APIs; those methods remain Not built in
`projection.tsv`. The four Asks list/answer tools pass every native method
argument and return its generated result under the connecting native principal.

The three AppModel tools use the generated `appmodel.thrift` codecs and native
`applications@3` client. Native mediation owns the kept manifest, class, Rights,
live ownership, inner keys, result bounds and audit. Exact generated answer text
preserves opaque JSON number forms; `structuredContent` provides its parsed
view. Non-success outcomes set `isError` under FAC-M15. Native wrapper refusals
retain their own outcome and reason with `isError: true`.

The adapter associates a caller operation with an opaque native request key
before dispatch using the existing bounded CAS state owner. The association
binds the OS account and executable, application, instance, context, epoch, tool
and caller's inner key. It stores no request body, result or effect state.
At most 1,024 associations fit in one 256 KiB file. Full or uncertain storage
refuses fresh keyed operations. Existing associations survive restarts and
native expiry; they are never evicted or reminted to escape an unknown outcome.
Keyless reads need no association. Native mediation decides validity and class
for keyless commands before effect dispatch. The six-tool message receiver
keeps its restricted inventory.

The private dedicated native-agent composition additionally exposes
`oa_messages_request_access`, `oa_messages_prepare`, `oa_messages_inbox_open`, `oa_messages_inboxes_list`, `oa_messages_send` and
`oa_messages_wait`. While its authenticated application presence does not have
that instance's inbox open, every tool, `oa_messages_inbox_open` included,
MUST answer the typed `unavailable` below and MUST NOT dispatch any other OA
call. The one exception is `oa_messages_inbox_open` reconciling an unknown
status within a current admission that no loss or refusal has ended. Every messaging tool uses its immutable native context
automatically, and `oa_messages_inbox_open` uses it when context is omitted.
Initialize instructions do not carry it, because a host may keep an earlier
connection's instructions after a reconnect. It is a fresh routing label for this child lifetime; vendor-session
mapping, reconnect recovery and idle wake remain separate acceptance.

The private `oa_messages_watch` query returns four core fields (`outcome`,
`context`, `revision`, `pending_until_unix_ms`), with additive optional fields including
`reply_until_unix_ms`, `awaited_replies`, `oldest_wake`, `reason` and `asks_wake`.
Consumers MUST validate known fields and accept unknown additions. An absent
reply hint is zero. `awaited_replies` counts wake-eligible local replies to
questions owned by this exact chat that were open when the reply was accepted
and are within both deadlines. The count supplies no permission, delivery or
acknowledgment. `oldest_wake` is the least opaque key in the same wake-eligible
set whose greatest key is `wake`. `asks_wake` is the ordered
key of the Asks occurrences this connection's enrolled program may answer
(asks ASK-R14); it is empty when none is, and while the program is enrolled a
held watch lasts at most 15 seconds. The Claude Mod and the OpenCode plugin
wake an idle open chat once per greater key with the fixed pointer
`OpenAbstractions: a question is waiting for your answer.`. The optional closed-vocabulary
`health` input is an untrusted claim about this native connection's host
observer. `health_supported: true` advertises that input before a companion uses
it. A claim MUST NOT supply a target, body access, acknowledgment, permission,
routing, readiness, ownership, renewal or lease extension. Only existing
scheduled status upkeep may project the fixed claim text under current native
admission; recording it MUST NOT trigger upkeep. Its local C1 diagnostic remains
an untrusted claim when native projection cannot be confirmed. Host tool denial
MUST be honored, with OA status reported as unconfirmed rather than using an
alternate transport. Companion attempts remain sequential and natively bounded;
availability failures retry with capped backoff until host close, pause or
cancellation. Native refusal and invalid protocol end the receiving attempt;
connection recovery and permanent retirement follow the admission and Codex
receiver rules below.

A model can call every tool its host lists, `oa_messages_watch` included. The
watch inputs `turn`, `reserve_wake` and `wake_ticket` steer this inbox's wake
chain, so the gateway honors them only with `adapter_key`: the key the host
adapter registered for this child before any model turn. The Claude Mod
registers it on `oa_claude_session_start` after a plain start answers
`adapter: supported`; the OpenCode plugin passes it in its child's
`OA_OPENCODE_ADAPTER_KEY` environment, and that child's start answers
`adapter: accepted`. The first key latches for the child's lifetime and a
different key is refused. `turn` without the latched key is refused always;
`reserve_wake` and `wake_ticket` are refused once a key is latched. No listed
tool input may be described as adapter-only (`scripts/unit_contracts.py`
`gateway` section). The wake prompt names a sender by an agent title only for
Setup's connector registration; another kept app reads by a plain-ASCII title
that names no agent, and anything else reads as no sender.

The dedicated native Codex composition alone additionally exposes the internal
`oa_codex_session_start` tool. Its input MUST be an empty object and its schema
MUST refuse arguments. It answers `observed` or `unavailable` and grants no OA
right, enrollment or vendor identity claim. The call lets the existing receiving
middleware observe independently supplied host `_meta.threadId`. The first valid
thread binds the connection. Later missing, invalid or changed thread metadata
MUST end receiving and join its watcher; that connection MUST never restart or
retarget receiving. Tool arguments and `session_id` MUST NOT supply this binding.
The shared generic gateway and other native-agent inventories omit this tool.
Held by: `TestCodexSessionStartProductionCompositionInventory` and
`TestCodexSessionStartPrivateToolUsesOnlyHostMetadata`.

Codex runs SessionStart hooks when a thread's first turn begins, and its
initialize and tools/list requests carry no thread. A restarted Codex Desktop
resumes the open thread and starts the helper with no turn. While no host
metadata has bound the connection, the Windows helper reads which processes
hold the thread writer locks in Codex's thread-writer-locks folder through the
Restart Manager, without opening them. It restores only while its parent
process started before it and at most two minutes before it, as on a restart;
an older parent may have started it for a new chat whose lock is not there yet,
and a younger one is a reused PID. When that parent, identified by PID and
creation time, holds exactly one thread lock, the helper binds that thread as
host correlation. Several, none, or another holder bind nothing. A per-thread
lock in the state folder keeps one helper on a thread: the restoring helper,
or the helper bound by host metadata, which retries it until it holds it. A
restore never replaces a binding, and later host metadata MUST name the same
thread. Native enrollment and message rights keep all authority. The bound
helper names its chat from the newest line of Codex's session index for
the thread and rereads it every 30 seconds. Held by:
`TestRestoreCodexThreadBindsTheParentsOnlyLoadedThread`,
`TestRestoreCodexThreadWaitsWhenTheLoadedThreadIsAmbiguous`,
`TestRestoreCodexThreadNeverReplacesABinding`,
`TestCodexParentEligibleOnlyJustBeforeTheHelper`,
`TestHoldCodexThreadWaitsForTheLockAndKeepsIt`,
`TestCodexLockHoldersNamesTheHoldingProcess` and
`TestFollowCodexThreadTitleCarriesARename`.

A question to a retained Codex chat whose inbox is closed wakes that chat's
own thread. The runtime selects the thread from the chat's retained binding and
passes it on the owned start channel (`oa_receiver_cold` with no launch facts).
The registered `codex-v1` recipe runs `codex queue --thread <thread> --message
<pointer> --resume` only while Codex Desktop runs; the queued turn runs the
thread's hook, the thread's own helper binds that chat, and the turn reads its
inbox. With Codex closed the start refuses (`codex_closed`) before any vendor
call, and the message waits until the person opens the chat. This path never
creates a thread and never runs one headless. Held by:
`TestAgentStartRegisteredCodexRetainedChat`.

A closed Claude Code chat is resumed in place only while no Claude desktop
app or Claude Code CLI process (`claude.exe`) runs in this session: a missing
OA inbox proves no host absence. An unreadable process inventory counts as
running. While one runs, the `claude-v1` recipe answers in a copy: `--resume
<original> --fork-session --session-id <OA UUID>` in the original's folder,
held under the chat's context, whose helper (`--claude-resume-copy <OA UUID>`)
binds and reads the original chat's inbox. Later questions resume the same
copy while the original's session file keeps its change time; a changed
original is copied again. Every spawn names `--resume`. A Claude started
between the choice and an in-place spawn refuses that spawn (`claude_open`).
The resume accepts Claude Code 2.1.317 and every later 2.x release; an older
release or another major refuses with `cold_resume_capability_unavailable`.
The launcher reports the delivery `resumed` or `copy`. Held by:
`TestColdClaudeStartResumesInPlaceOrCopies` and
`TestColdClaudeResumeArgumentsAreOriginalSessionAndFixedReplyOnly`.

C1 installs/removes its exactly owned local SessionStart plugin through vendor
CLI plugin/marketplace commands. The person MUST review and trust it in Codex
`/hooks`. Its separate `session_start` status is `absent`, `installed`,
`unavailable` or `preserved`; hook failures MUST preserve the ordinary MCP
connect/status/disconnect outcome. Status with no hook marker MUST run no plugin
commands. Changed/unowned vendor entries and caches MUST be preserved, including
unknown entries left after factory reset. Startup/resume hooks are best effort;
installed first-start ordering and host metadata remain separate acceptance.

The native chat adapters MUST open their original source through typed
InboxOpenChat with an empty ChatBinding. Independently supplied host chat
metadata binds one canonical inbox scoped by native account, enrolled program
and host chat ID. InboxOpenChat verifies the exact source context and instance
in the same native process, then returns the runtime-allocated canonical context.
The helper declares that context before repeating the claim to open receiving.
A newer claimant ends the prior chat claim and original source occurrence;
other chats sharing its Applications instance remain live. Unbound sources remain send-only.
ApplicationPresence.Interfaces supplies no chat ownership or suppression.
SendChat retains the actual source owner while directing replies to that route.
Invalidation and EOF cancel and join only that helper's receiving association
and withdraw its exact instance. Queues and other occurrences remain intact.
The generic service filters receiving advertisement through the typed current
native instance and context. Same-agent
multiple-context discovery defers default selection to the native service;
distinct applications still require explicit selection. Live names use an
available owning-host descriptor; unavailable names remain unnamed. A second
app-server is never started to poll titles. The native service owns
permission-filtered title selection, including duplicate-title conflicts.
Claude correlates its documented stdio child CLAUDE_CODE_SESSION_ID with the
Mod's independently obtained session ID. A stale child after clear or implicit
resume remains unbound until the host replaces it. Missing titles remain unnamed.
The bound Claude helper also names its chat from its session's transcript in
Claude Code's projects folder: the session's newest custom title, else its
newest Claude-made title, read every 30 seconds when the file changed from
where the last read ended, its tail first. A transcript without title lines is
read once, and afterwards only as it grows. A rename reaches OA's listing
without a person message.
The Mod's lifecycle title is applied when it changes, so its repeated older
title never reverts a rename. Held by
`TestClaudeTranscriptTitlePrefersTheSessionsCustomTitle`,
`TestClaudeTranscriptTitlesReadOnlyNewLines` and
`TestClaudeRenameReachesNativeClaimFromTheTranscript`.
Private Claude lifecycle start returns an opaque child/association generation
from adapter-lifetime memory. The Mod echoes that fixed metadata and the exact
returned launch session ID for scoped end. A session mismatch retires only that
generation and prevents later receiving starts on the child. Ordinary scoped
end permits reopening with a fresh generation. Empty, missing, stale or mismatched
teardown metadata closes nothing; EOF still cleans up the owned child. No
caller-supplied ID establishes a native chat binding. Unknown teardown completion
supplies no proof that the native association ended.
Held by `TestChaosCodexRoutingBoundAdvertisement` and
`TestVersionedReceiverDiscoveryPreservesExactTargetAndAmbiguity`.
The helper lives for its whole MCP stdio connection (owner, 2026-11-01): it
serves MCP from the first byte, and only the host closing stdio, or the process
being stopped, ends it. Its presence lease is 30 seconds, renewed every 10
seconds; closing the MCP connection ends that association and withdraws the
instance. Until the first admission, and after any loss, every tool answers
`isError` with `outcome` `unavailable`, `state` `connecting`, `reconnecting` or
`refused` and a plain `detail`; a call on a supervised connection first waits
up to 10 seconds for re-admission, and a Send or Reply up to 30 seconds. One
deadline covers that call's waits for its chat receiver and for the
connection. While a call waits, the presence owner retries at once and then
every second. Losses include a runtime that is down,
restarting or asleep, a reset pipe, a lapsed lease, a decode refusal and a tool
call's failed inbox proof. A recoverable loss leaves the instance's lease to
lapse, so senders read `receiver_lapsed` and never `receiver_withdrawn` (a
closed chat) while it re-admits, and keeps the same process, MCP connection and
context: a fresh Announce and InboxOpen, after the same service resolution as at
start, open that context under a new instance, retried with backoff from one to
30 seconds. The runtime lets the same process take its context back from its
own older instance at once; another process still reads `held`. A Send or
Reply already admitted runs to the runtime's answer when admission is
suspended meanwhile; the host's own cancellation still ends it. Re-admission
MUST reuse the same application label, context and process; the service MUST
derive the program and its enrollment from that
process, never from the helper. An enrollment refusal (Announce `forbidden` or
`invalid`, or InboxOpen `forbidden` or `invalid` that one immediate fresh
Announce and InboxOpen confirm) withdraws the instance and is final for the
connection: tools answer `state` `refused` until the host closes stdio,
presence is never retried, and a later enrollment does not revive it (the
person can revoke the direction, VISION 2026-10-31). The confirmation exists
because the runtime answers InboxOpen `forbidden` for an instance it does not
know, as after a restart between the two calls. A context another holder
keeps for longer than one lease length, or a runtime answer this helper's
decoder refuses as an unknown field (version skew), is retried every five
minutes with `state` `refused`; a held context is never taken over while it is
held. The Codex wake receiver joins each stopped run and starts again with capped
backoff once native admission is current. A first forbidden header observation
ends that run and requires another observation after admission is current; a
successful admitted page clears this confirmation streak. A second forbidden
observation before a successful page, or final native admission refusal,
permanently retires receiving for this connection. Connection end or missing,
invalid or changed thread metadata also ends receiving. A temporary observation
or queue failure preserves the binding and its queued-or-unknown progress.
Messages output records `Page`, `InboxPage`, `Inbox`, `Header`, `Message`,
`RemoteOrigin`, `InboxChange`, `ChatInboxChange`, `SendResult` and `StopResult`
accept additive fields. Known required fields, types, enums and bounds retain
their validation. Nested `MessageRef` and `OriginClaim`, request, claim,
identity and authority records remain closed. `PrepareResult` stays closed
because its exact `SendRequest` is retained for explicit submission.
Previously compiled closed
Messages readers require a helper update before receiving newer observation
fields; changing the server schema cannot change those binaries.

A connector update publishes over a running helper (owner R16, 2026-11-09).
Windows then names that helper by its moved image, and the runtime denies it
every admission, so its own presence ends refused. On Windows a dedicated
helper whose image Setup moved aside keeps the host's stdio and hands the
connection to the published connector at the path it started from (owner,
2026-11-10: a person never restarts an agent app to fix messaging). The
candidate is opened with links refused and held without write or delete
sharing from its check until the started process is confirmed to run that
same file. The runtime's kept connector image is the authority
(CONNECTOR-HANDOVER-AUTH, ruling c): the helper's `ReadAgentEnrollment` for
its own application must answer `page`, state `enrolled` and a program equal
to the held path. On a signed build, where the moved image carries a trusted
embedded signer, Windows must also verify the candidate signed by the same
publisher key (`product.SameSignerAs`). A candidate failing any step is never
started, and the moved helper then answers each tool call with the restart
sentence. Every connection the moved helper opens reaches the runtime under
the moved path; the runtime answers that caller this one read, for the
application whose image it runs, when the file at the moved path has the key
of the image the person kept before that application's latest committed
update (serve `readMovedConnectorEnrollment`). The successor starts with the arguments and environment
the helper started with, before the OpenCode owner removed its adapter key.
It receives the host's recorded `initialize` and `notifications/initialized`
when the host's protocol has them, and the latest session binding call
(`oa_claude_session_start` or `oa_opencode_session_start`) that the old helper
answered `observed`; `oa_claude_session_end` drops it, and an end with
`retire` stops every later replay. A successor that does not answer the
binding `observed` within 10 seconds is stopped, and the handover is retried
after 30 seconds. The answers to these replays never reach the host. A final
admission refusal, `ended` included, while the helper's image is current is
the runtime's word on that program or chat, and fences every later handover
of that connection. Host tool calls during the switch answer at once
`OpenAbstractions updated this connector and is moving this chat to it. Try
again in a few seconds; nothing was done.`; later host lines go to the
successor, and the old process relays whole lines of at most the stdin line
limit both ways and holds no admission. A Send the old helper answered
uncertain, matched as the gateway reconciles it (a one-call Send by receiver
and content, an explicit Send by `idempotency_key`), is answered by the relay
after the handover with `unknown` and `OpenAbstractions reconnected since this
send, so its outcome cannot be reconciled on this connection. Do not send
again; tell the person the outcome is not known.`; it never reaches the
successor. The successor's own claim of the same chat ends the moved helper's
claim at the runtime (`ended`). When the successor ends, the moved helper ends
its MCP connection; when the host closes stdio, the moved helper closes the
successor's stdin and ends after it. A runtime restart, upgrade or lapsed
lease needs no handover: the supervisor re-admits the same context, as above.
Held by `mcp-gateway/agent_handover_test.go`,
`mcp-gateway/agent_handover_process_windows_test.go` and
`mcp-gateway/product/standing_test.go` (`TestSameSignerAsUsesTheReferenceFile`).

The runtime's opaque ordered Page.wake owns eligibility. Adapters compare its
UTF-8 bytes with a durable per-binding submitted high-water and forward only a
nonempty greater key. Header rows, revisions, outgoing events, terminal states
and elapsed time supply no additional eligibility. Codex retains this receipt
in its protected native helper state; Claude uses documented durable $.store
under its host-session/native-context binding. A final native observation at
submission drops adapter-held stale pointers. The high-water is reserved before
the vendor call so an unknown acceptance cannot be replayed after restart.
Both adapters retain one outstanding pointer per bound thread. Its opaque
adapter-local wake ticket clears only after a successful native body Wait
presents that exact ticket for its bound program/chat/context. Manual reads
clear nothing. A 30-minute lease permits one replacement only for a strictly
higher wake. A definitive Mod admission drop may durably record host-local
knownDrop evidence and retry the same unexpired ticket through native Refresh;
the evidence is cleared before another possibly effective submit. Unknown or
accepted calls retain their slot. Model arguments cannot claim no submission.
The protected ledger bounds receipts to256 and64KiB. At capacity it evicts the
oldest receipt without a live outstanding slot; an evicted binding may wake
once again. Live/unknown slots survive capacity pressure and ordinary helper
exit/restart. Initial receiving allocation awaits the original source's
recoverable admission; final admission still retires it.
A pointer the vendor already accepted before ACK may cause one delayed empty
turn; it says to check the inbox and stop when nothing is new.
Hosts end helpers without closing stdin;
another helper's next write marks such a row `gone`. Held by:
`mcp-gateway/agent_connection_chaos_test.go`,
`mcp-gateway/agent_helper_process_test.go`,
`mcp-gateway/agent_presence_readmit_test.go` and
`mcp-gateway/agent_codex_replay_test.go`.

Every gateway composition, the plain generic gateway included, recovers a
panic in a request handler or middleware (`gateway.GuardServer`): a
`tools/call` answers `isError` with `outcome` `unknown`, another request an
error, and the connection stays open. Every composition reads stdin through
the line guard: a line that is not a JSON-RPC message or batch, or is longer
than 1 MiB, is dropped and reported on stderr, and the connection stays open.
The `state` and `detail` members of the typed `unavailable` answer and the
panic answer's `unknown` outcome are additive public MCP surface (coordinator
disposition, 2026-11-01). Model-turn completion
has no separate lifecycle hook. Explicit host reconnect starts a fresh native
source context; its receiving context remains runtime-owned. A fresh source
supplies no uncertain-send receipt continuity. Unknown-send reconciliation through
an identical key/content also requires the original source context. A closed
connection therefore requires explicit outcome review before any resend.
Held by: `mcp-gateway/gateway/messages_test.go`,
`mcp-gateway/agent_mcp_test.go`, `mcp-gateway/agent_presence_test.go` and
`mcp-gateway/agent_presence_readmit_test.go`.

That same admitted native composition exposes `oa_asks_request_answering`,
`oa_asks_list_answerable`, `oa_asks_list_choices`, `oa_asks_answer` and
`oa_asks_answer_choice` through the existing `abstraction.asks/responder@1`
client. These tools are also listed by the shared generic gateway, whose
native subject remains subject to MCPG-A3 and the service's typed refusals.
The admitted composition's native caller remains the kept helper. Registration grants no
enrollment; RequestAnswering requests the normal person-only permission.
The shared generic gateway subject remains subject to MCPG-A3; this native
composition uses its separately admitted kept helper subject.
The authenticated principal is that native launcher. The same-account
organisational boundary authenticates no upstream vendor application, model or
parent. R3 applies to the service-proven parties, actual target publisher/file
and verified via metadata; it promises no universal OA-publisher refusal for
all adopters. R7 foreign-code exclusions and person-only enrollment remain.
The service rechecks Rights, enrollment, parties, tier, options and expiry
on each list/answer. Inputs select only bounded cursors, page limits and
record/option identifiers, and accept no authority claims. Returned question
and option text is untrusted. Unsupported services return an error without
fallback or automatic retry. Watching and idle wake remain separate.
Held by: `mcp-gateway/gateway/asks_test.go` and the existing Asks/runtime
responder admission tests.

**[MCPG-T2] `--help` lists the inventory.** `openabstractions-mcp --help` MUST
list every tool of the shared generic `tools/list` and no other. The private
native-agent composition's additional tools require the admitted lifetime above.
Held by:
`scripts/unit_contracts.py`, which reads the help's tool lines, and
`mcp-gateway/main_test.go` (`TestHelpInventoriesToolsAndAuthorityBoundary`).

**[MCPG-T3] Tools only.** The gateway MUST advertise tools and nothing else:
no roots, resources, prompts, sampling, elicitation, subscriptions, MCP Apps,
remote transport or callback into the host. Held by:
`mcp-gateway/gateway/server_test.go` (`TestProtocolMatrixAndCuratedToolList`).

**[MCPG-T4] One catalogue.** The gateway's tools and the OpenCode plugin's OA
tools MUST come from one generated catalogue, which also writes `--help`, the
README table and the installer's description. Untestable here: the tool list
is hand-written in `gateway/server.go`, `main.go` and `README.md`; CC-26 with
P-J4 generates them and removes the plugin's second catalogue.

## Admission

**[MCPG-A1] The gateway's principal is its executable.** OA binds the
gateway as the program that runs it, through the native transport (IPC-P1).
Its authority is the rights rules written for that executable's absolute
path. MCP `clientInfo`, request `_meta`, the parent process id and tool
arguments MUST NOT select an OA subject or grant a right. Held by:
`mcp-gateway/gateway/server_test.go`
(`TestForgedClientMetadataCannotChangePrincipalOrAuthorizeEffect`).

**[MCPG-A2] The gateway subject.** Every agent application without an
enrolled launcher of its own that starts the same gateway executable shares
one subject, named "gateway subject" in this contract, in rights and in the
Panel. Activity shows the proven program and the `clientInfo` name as "(name
reported by the app)". Two separately authorized principals need two
executables the native boundary can tell apart; a second `--state-dir` alone
gives no identity isolation. Held by, for separate principals:
`mcp-gateway/gateway/server_test.go`
(`TestSeparatePrincipalsRejectCrossScopeHandle`). Untestable here, for the
Activity wording: P-J15 and W1-14 show the gateway subject's rows.

**[MCPG-A3] The gateway subject holds no standing permit.** A call through
the gateway subject MUST NOT match a standing permit for `edit`, `commit` or
`spend`; each such call asks the person. The gateway subject MUST NOT be
enrolled as an answerer, and a question admitted on behalf of a call through
the gateway is answered by the person until the admission records a bound
initiator. No assistant answers a question about its own action. Untestable
here: answerer eligibility moves to asks in W1-14, and its scenario is an
`edit` proposal through the gateway while OpenCode is enrolled, which
OpenCode's `Answer` reads `forbidden` for.

**[MCPG-A4] An agent program gets its own subject.** An agent program whose
own launcher the person kept (P-J15) MUST reach OA as itself, and a standing
permit the person writes for that one program applies to it alone. Held by:
`serve/application_agent_enrollment_windows_test.go`
(`TestApplicationAgentNativePeerUsesCurrentKeptProgramSubject`) and
`serve/application_v3_gateway_windows_test.go`
(`TestApplicationV3GatewayNativeAgentClassRecheckedForReplay`) for native source
receiving, alongside the native Messages inbox lifecycle and original-enrollment
controls. P-J15 owns
installed host binding and receiving; a source fixture supplies no live host or
idle-delivery claim.

**[MCPG-A5] One asking bound per gateway.** The gateway subject's bound on
the questions it may raise MUST be shared by every agent host that launches
the gateway. Untestable here: the per-program asking bound is asks' (CONV-Q1);
W1-14 holds it.

**[MCPG-A6] Published content is marked untrusted.** A content block carrying
text an application published MUST carry
`_meta["org.openabstractions/untrusted"] = true` and
`annotations.audience = ["assistant"]`. A field declared `secret` never
carries a value. Held by: `mcp-gateway/gateway/messages_test.go`
(`TestMessagesToolsOnlyOnOwnedBackend`) for the private Messages consumer's two
marks and random delivery boundary; `mcp-gateway/gateway/appmodel_test.go`
(`TestAppModelCatalogueAndRawRecords`, `TestAppModelMalformedOuterRecordsRemainTyped`,
`TestAppModelResultPreservesMarkedRawJSON`) for AppModel result marks, exact JSON
text, parsed number forms and typed refusals; `serve/application_v3_gateway_windows_test.go`
(`TestApplicationV3GatewayNativeSecretRefusalAndAudit`) for the native AppModel
secret-value refusal. The native Messages adapter uses generated message codecs
and sanitized OA attribution; generic shared backends offer no Messages tools.
Untestable here: broader W3-14 published-content marking remains pending under
the existing "Published content is untrusted" requirement in
`research/release-0.8.0/PLAN.md`. These checks cover the Messages and AppModel
constructors, without a coverage claim for every other published-content path.

## Errors and outcomes

**[MCPG-E1] OA outcomes are tool results.** An OA refusal or outcome word MUST
reach the host as a tool result with `isError: true`, the word in its
structured content as `outcome`, and the refusal record when the refusal has
one (IPC-E2). A JSON-RPC error is reserved for the gateway's own failures.
Untestable here: today a refusal travels as tool error text; CC-26 moves the
gateway onto IPC-E2's table.

## Bounds

**[MCPG-B1] Bounded calls.** The gateway MUST run at most eight calls at once
and refuse a ninth at once. Each call has a two-minute deadline. Text
inference takes at most 65,536 input bytes and 4,096 output tokens, with
`max_output` defaulting to 1,024. Held by: `mcp-gateway/gateway/oa_test.go`
(`TestFoldCompletionBoundsAllRetainedFields`,
`TestFoldCompletionStopsAndCancelsOversizedIteratorEarly`) for the output
bounds. Untestable here, for the eight-call and deadline bounds: no test
saturates the server; CC-26's catalogue declares the bounds as data and its
test holds them.

**[MCPG-B2] Curated listings.** Application discovery is read-only and omits
executable paths, start guidance and activation recipes. OA filters each
snapshot for the gateway's own principal. Held by:
`mcp-gateway/gateway/applications_test.go`
(`TestApplicationsOutputIsCuratedAndBounded`) and
`mcp-gateway/gateway/server_test.go`
(`TestApplicationDirectoryUsesFixedPrincipalAndHonorsCancellation`).

**[MCPG-B3] Hosting is explicit.** Durable generation takes `hosting:
"local"` with no credential, or `hosting: "hosted"` with a credential name.
Every other combination is refused for good with a sentence that says what
to send instead. Text inference takes `hosting: "local"`, or hosted with a
credential name. Held by: `mcp-gateway/gateway/job_routing_test.go`
(`TestJobRequestRoutesLocalWithoutCredentialAndHostedWithIt`,
`TestJobRoutingRefusalsSayWhatToSend`).

## Handle records and durable work

**[MCPG-H1] Handle records.** Submission MUST write a random opaque handle
record, atomically and synced to disk, before it submits. The record holds
the exact OA endpoint binding, logical owner, contract, guarantees and
request identity, and no prompt, result, credential, provider header or
secret. Held by: `mcp-gateway/gateway/native_integration_test.go`
(`TestNativePrincipalsAndDurableRecoveryThroughMCP`).

**[MCPG-H2] A stable request key.** Submission MUST take a caller-chosen
`request_key`. The same key with the same specification reconciles the saved
identity; the same key with a changed specification is refused. Held by:
`mcp-gateway/gateway/native_integration_test.go`
(`TestNativePrincipalsAndDurableRecoveryThroughMCP`).

**[MCPG-H3] Disconnecting cancels nothing.** An MCP disconnect MUST leave
accepted work running; cancellation is the explicit cancel tool. A handle
from another principal is refused. Held by:
`mcp-gateway/gateway/server_test.go`
(`TestAcceptedJobSurvivesMCPDisconnectAndCancelIsExplicit`,
`TestSeparatePrincipalsRejectCrossScopeHandle`).

**[MCPG-H4] Diagnosis reads one handle.** The diagnosis tool reads one known
handle under the gateway's own authorization and returns its state, a
classified failure and fixed guidance. It returns no prompt, result,
credential name or free-text failure message, and changes nothing. Held by:
`mcp-gateway/gateway/oa_diagnose_test.go`
(`TestDiagnosisExposesTypedFailureOnly`,
`TestDiagnosisMapsUnknownProviderCauseToOther`).

**[MCPG-H6] One picture tool.** `oa_image_generate` MUST make or edit one
local picture as a durable job (`MCPG-H1`, `MCPG-H2`, with a gateway-made request
key that the permission retry reuses) and save it under the person's account
in `<Pictures>/OpenAbstractions/image-<local time>.<ext>`. The Pictures folder
comes from storage `layout.ResolvePerson` (STO-L2, STO-L5); a refusal is
answered with its reason, and nothing is asked or started. With no `model`
the first candidate of the caller's effective `selectionPolicy`, by default
`oa-image-default`, is used. An edit names `inputImage` and an optional PNG
`mask`; their media types come from their first bytes, read through the
caller's content reader, and the job runs in edit mode; a missing policy answers `Pictures are not set up yet.` and a hosted
first candidate `Pictures are set to a hosted model, which this tool does not
use yet.` The call follows the job for at most 50 seconds and its whole
budget, the person's first-use answer included, is 55 seconds. A finished
picture is read by digest, verified by size, SHA-256 and media type, and
created exclusively; a taken name gains `-2` … `-20`, and no file is
replaced. The answer is `Saved to <path>. To edit it, pass inputImage
<digest>.` with `{path, digest}`; a picture still being made answers `Still
making the picture. Call oa_image_generate with handle "<h>" to wait for
it.` The same handle after its save answers the file already saved while it
holds the picture. A wrong argument is refused at once with `outcome: invalid`, `field`
and `allowed`. `oa_inference_job_submit` refuses a picture with `Pictures use
oa_image_generate.` Held by: `mcp-gateway/gateway/image_test.go` and
`mcp-gateway/gateway/job_routing_test.go` (`TestJobSubmitRefusesAPicture`).

**[MCPG-H5] The state folder.** Handle records live in the storage data class
location for the gateway. Untestable here: the default is still
`os.UserConfigDir()/OpenAbstractions/mcp-gateway` (Divergences); P2-5 moves
it and reconciles handles written under the old path, with its test.

## Versions

**[MCPG-V1] MCP protocol versions.** The gateway MUST accept MCP `2026-07-28`
and `2025-11-25`, and answer an offer of any other version with one of those.
Held by: `mcp-gateway/gateway/server_test.go`
(`TestProtocolMatrixAndCuratedToolList`,
`TestUnknownProtocolOfferNegotiatesExplicitSupportedVersion`).

**[MCPG-V2] The adapter's version is the release's.** The implementation
version the gateway reports MUST equal the release version it ships in. The
adapter version, the MCP protocol version and the OA service versions are
separate facts. Untestable here: the gateway reports `0.1.0-dev`
(Divergences); the release check that compares it belongs to the release
tooling, outside this package.

## Command line

`plugin claude install|update|remove --project ABS --state-dir ABS --claude ABS`
delegates personal setup for that project to the native Claude Code CLI's LOCAL
plugin/marketplace modes. Its selected local settings and private ownership
record must match before update or removal; shared project settings are preserved.
This opt-in package lifecycle supplies no session, permission, message-delivery
or wake proof. Held by: `mcp-gateway/claude_plugin_test.go`.

The Codex companion uses the selected native CLI's user-scoped MCP configuration,
shared by desktop, CLI and IDE. `connect codex --state-dir ABS --codex ABS_EXE`,
`disconnect codex` and `connections codex` read only the named entry through
`mcp get NAME --json`. OA never edits TOML or lists the account's other entries.
Positive private ownership and exact readback are required for idempotence and
removal; unknown, foreign or changed entries are preserved. Removal uses the
installed management image and can name a revoked/deleted saved target without
executing it. Vendor remove is name-based and cannot atomically exclude an
external edit after preflight. Saved setup supplies no connection, permission or
wake proof. Held by: `mcp-gateway/codex_setup_test.go`.

**[MCPG-C1] Explicit command modes.** The default `openabstractions-mcp`
invocation serves stdio, with the existing `--state-dir ABSOLUTE_PATH` option.
`--help` prints usage, the tool inventory, authority boundary and supported MCP
versions to standard error and exits 0. `--check` is an opt-in bounded diagnostic:
it reports MCP connection separately from the read-only applications and cached
models probes, uses owned transient state and a trusted explicit endpoint without
activation, and exits within twenty seconds. Its zero exit requires both reads
and confirmed child cleanup; output contains counts and fixed error categories.

The additive `connect claude` and `disconnect claude` modes require `--project
ABSOLUTE_PATH --state-dir ABSOLUTE_PATH`, with optional `--claude
ABSOLUTE_CODE_CLI_PATH` and `--name NAME` (default `openabstractions`). They manage
only project-scoped Claude Code registration through its supported vendor CLI.
Disconnect also accepts `--registered-program ABSOLUTE_PATH`: a trusted management
image removes only the exact command owned by the existing marker/current entry,
without opening or executing that configured target. Connect refuses this flag.
The vendor CLI is the public configuration writer. Exact public project-record
readback and a bounded private ownership marker guard idempotence and removal;
foreign, changed or unowned same-name entries are preserved at preflight. The
vendor's name-based removal has no compare-and-delete: a replacement after
validation can be removed. Setup provides no atomic guarantee against external
concurrent configuration edits. Registration saved,
host approval/connection and OA capability access are separate facts. Removing
registration leaves job state and OA grants intact.

`connections claude --state-dir ABSOLUTE_PATH` reads the existing private setup
markers and their selected public project records. It emits bounded JSON saved
registration facts, including exact project/program file keys and saved names;
those keys are observations, not identity grants. It admits at most 128 entries
in that one state directory (one extra detects overflow), and returns at most
64 connections and 1 MiB of JSON.
Missing, changed, corrupt or over-bound ownership is unavailable, never a partial
empty-success claim. It creates no state, runs no vendor command and scans no
projects or agent profiles. It makes no host approval, live connection, permission
or Messages claim. A complete page exits 0; unavailable exits 1 with no current
connection claims. Held by: `mcp-gateway/claude_connections_test.go`.

An unavailable target image leaves its owned configuration visible with an empty
program key. That row supplies configuration-removal provenance only; it supplies
no native image identity, enrollment or execution authority. The installed
management gateway performs the existing positive-marker removal without opening
or executing that target. Held by: `mcp-gateway/claude_connections_test.go` and
`mcp-gateway/claude_setup_management_test.go`.

Unknown flags or arguments outside these explicit modes exit 2; recognized help
exits 0. A state folder that cannot be created, failed server or failed setup
operation exits 1. The default stdio form and existing state-directory behavior
remain compatible. Held by: `mcp-gateway/main_test.go`
(`TestHelpInventoriesToolsAndAuthorityBoundary`), `mcp-gateway/check_test.go`
and `mcp-gateway/claude_setup_test.go`.

## Divergences

The IDL-to-MCP emitter and generic `oa_api_describe`, `oa_api_read` and
`oa_api_call` tools are not built. The existing bounded legacy adapters in the
tool table transform or restrict native arguments/results; they do not claim
complete generated method projection. `projection.tsv` lists those unbuilt
method projections honestly as `not_built`, rather than transport exclusions.
Its Not built rows reference `backlog/MCP-API-EMITTER.md` (0.9.0) and are
shrink-only against the existing merge-base owner: a new method cannot acquire
a new Not built allowance. Initial adoption is bounded by the public native
RPC methods already present at that merge base. Current exact generated
projections and real transport exclusions remain separately classified.

`oa_messages_prepare` is a private read-only MCP intent constructor for a new
question or note. It resolves friendly receiver labels through current native
authorized inbox discovery, refuses missing/ambiguous/refused discovery, and
constructs one complete existing Send input. A lifetime defaults to 600 seconds
and is bounded to 1..604800 seconds. Questions receive a future absolute
Unix-millisecond answer deadline no later than expiry. The existing random-key
and clock primitives are sampled once for the returned intent. The model retains
`send_arguments` before Send and passes that object verbatim, including on
explicit unknown-outcome reconciliation. Prepare sends nothing, grants nothing,
keeps no intent/dedupe store, and supplies no receipt or permission lease. A new
Prepare invocation constructs a new intent; it cannot reconstruct a previous
send after uncertainty. Existing Send validation, queue-before-open semantics,
native authority and receipt replay remain unchanged. When no inbox of the
receiver is live, Prepare's `reason` names the receiver context's most recent
presence end, `receiver_withdrawn` or `receiver_lapsed`, and only to a sender
whose message right is granted. The runtime keeps these ends in memory: after a
runtime restart a sender reads `receiver_unavailable` until a new end is
recorded.

`oa_messages_send` also takes a one-call form: `receiver` and `body`, with
optional `context`, `kind` and `subject` and no other field. The gateway runs
the same native Prepare on this connection and sends its exact request. A
Prepare outcome other than prepared returns unchanged, with `next` stating that
nothing was sent. When that Send's outcome is `unknown` or `unavailable`, the
connection keeps the prepared request, and the same one-call Send again
reconciles with those identical arguments; a settled outcome drops it. Every
uncertain Send result carries one assistant line naming that reconciliation.
Prepare's prepared result carries `next`, the one call to make now.

The dedicated native Messages access tool admits one stable-key permission
request and observes that admission within the active MCP call's existing
two-minute budget. It never resubmits admission while waiting. An Allow answer
is separate from its rule effect: reporting permitted requires a current Rights
decision and a live receiver inbox returned by the enforcing Messages owner.
Refusal, unknown/unavailable, caller cancellation and deadline retain their
meaning; a retained pending/answered result at the deadline supplies no grant,
delivery or idle-model wake. Friendly Claude/Codex labels select existing
registered receivers; native services retain all identity/enrollment authority.
The model discovers authorized inbox/context values through tools and never
asks the person for internal application names or connection tokens.

- The gateway reports version `0.1.0-dev` (`MCPG-V2`).
- The default state folder is under `os.UserConfigDir()` (`MCPG-H5`).
- The tool list is written by hand in three places (`MCPG-T4`).
- Product connection setup follows the selected host's supported mechanism;
  version-specific receiving and installed qualification remain separate.

## Not built

- The emitter, the three generic tools and every `not_built` method row in
  `projection.tsv`, tracked by `backlog/MCP-API-EMITTER.md` for 0.9.0.

- The generated tool catalogue shared with the OpenCode plugin (`MCPG-T4`;
  CC-26 with P-J4).
- The read-only help-desk tools `oa_runtime_status`, `oa_diagnose(ref)` and
  `oa_can(action, resource)` (P-J4). Each joins the inventory with its row.
- Per-program subjects for agent programs with their own launcher
  (`MCPG-A4`; P-J15).
- The untrusted-content marks (`MCPG-A6`; W3-14).
