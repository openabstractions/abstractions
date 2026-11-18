# OpenCode adopter contract

The supported delivery uses the official OpenCode installer and the OA plugin
(owner decision, 2026-10-06). The chat@3 source slice follows architect Codex
330 ruling B and its Codex 336 clarification, 2026-11-03.

## Process-lifetime chat identity

For each provider invocation, the adapter creates a new RequestIdentity and
retains the HistoryWindow epoch and encoded final request in memory. Separate
invocations, including identical prompts, receive separate keys. A permission
repair before acceptance retains that invocation's exact request.

On a served chat@3 route, the host's `chat.params` sessionID supplies the
caller-local `conversation` label for that session's turns. A subagent uses
its own sessionID. Title calls carry no label. The adapter derives no scope
digest; inference owns CONV-T3 and the attested caller partition. Request
identity and no-retention guarantees keep their existing meanings.

This mapping remains inert while chat@3 is unavailable to OpenCode. chat@2
keeps engine similarity selection without a conversation label; a subagent
with a shared prefix can take the main chat's slot. The V2 context hook uses
its documented SessionRequest sessionID; its separate title hook removes the
conversation marker.

A lost Start is uncertain. The error's explicit `reconcile()` callback can
replay that same encoded request inside the current process. It reads no new
epoch and constructs no replacement body. Nothing calls it automatically.
Diagnostic causes and the copied request identity grant no replay authority.
The retained bytes stay private to the callback's invocation.

Every uncertain Start, relay unknown or post-dispatch observation failure is
non-retryable to OpenCode and says:

> The last request may still have run; send it again if needed.

The host's parser receives no transient status, response body or retry hint
for uncertainty. Its retry policy also checks those fields and message text,
independently of the SDK's retryable flag. Accepted failures never request a
fresh invocation. Only a definite pre-acceptance unavailable or exhausted
refusal is retryable, with its bounded retry_after_ms hint when supplied.

An OpenCode restart discards the callback and its identity. The earlier
uncertain operation remains unknown; the adapter never restores or
automatically resubmits it. Sending again is an explicit new person intent.
An OA runtime restart also fences the saved epoch; unknown never creates a
new key. This is the accepted 0.8.0 limit. The upstream durable invocation
request is a draft in `feedback/2026-10-02-opencode-upstream-drafts.md`.

## Installed connection

Before capability exploration, the plugin requests the installed native
Connect owner once. Launch and process exit are not proof. The held child
returns a closed typed result only after fresh native proof of both canonical
stock OpenCode registration and the current integration helper's enrollment.
A pending request blocks tools. OpenCode's model list and capability reads
run beside it: the runtime authorizes those reads for the stock host itself,
and an OA update that leaves the helper's Connect pending keeps OpenCode's
models listed. A later explicit retry can use
the private read-only native owner to observe completion without another popup.
The connected hint grants no authority; native services still check identity,
enrollment and rights for each operation.

The existing helper Keep may repair absent stock metadata after the canonical
installed-source and held-image checks. It preserves the helper title and
binding, grants the stock host no enrollment or answering rights, and grants
no directional messaging rights. A connected pair is a no-op; changed or
foreign entries retain the existing refusal/update path. Answering and each
messaging direction remain separate first-use decisions.

Source fixtures prove the composition, joined-child result and pending retry.
Fresh installed Connect and ping journeys remain pending qualification.

## Receiving and verification limits

The MCP server declares one `oa_<area>_<verb>` name per tool. Private
OpenCode operations use `oa_image_generate`, `oa_speech_speak`,
`oa_selection_manage`, `oa_logs_watch`, `oa_logs_read_failure`,
`oa_credentials_open`, `oa_tools_list` and the native session's
`oa_project_request_access`. Existing configuration keys and
host approval identifiers retain their behavior. Image generation keeps its
one-image/edit and delivery contract; it has no durable Job-submit alias.

Architect Codex440 (2026-11-04) makes one kept `oa-opencode-integration` helper
child per actual OpenCode host session the public native agent tool owner. The plugin
declares canonical `oa_*` wrappers from the generated native agent catalogue and
forwards their exact arguments over the helper's existing MCP stdio transport.
A wrapper's result carries the helper's structured outcome plus, as `steps`, the
helper's assistant-addressed next steps; untrusted text stays out of `steps`.
By default the plugin declares the wrappers of `defaultHelperTools` in
`tool-config.js`. The `optionalTools` there (API reads, application enrollment
and listing, Asks and Messages watches, video jobs and job following) register
only when the `tools` option enables them, since every registered definition
is prefilled with every model request.
Its idle wake follows the section below. `OpenCode` resolves to the enrolled helper, never
the stock `app:opencode` registration. Provider and private host tools keep their
native caller identity and existing rights.

The generated native agent catalogue carries its actual initialization instructions.
The plugin's system hook uses canonical tool names, including
`oa_messages_prepare` when the person asks to ping an agent. The native service returns a registered
ungranted direction as `forbidden` with `access:<exact registered name>` and no
inbox metadata; request access through the existing native permission owner.
The raw-name marker preserves the existing 64-byte reason bound for versioned
Darwin helpers. An overlong target fails unavailable without truncation. Native
`unavailable` with reason `not_found` projects as MCP `not_found` only for proven
unregistered names; transport loss never proves absence.

Stock OpenCode at `aec0b9a6d8898f68f923aaf08b7306d931fd9d76`,
`packages/opencode/src/mcp/catalog.ts:119`, forms MCP model-facing names as
`sanitize(clientName) + "_" + sanitize(name)`. Its `OpenAbstractions_` prefix is
a host naming limit. OA keeps the declared names canonical and uses no empty
server key or alternate namespace. A server label supplies no publisher or
cryptographic identity proof; unsigned previews retain their actual status.

At the same pinned source, `catalog.ts:convertTool` forwards only the MCP tool
name and arguments plus cancellation/progress options. It passes no per-call
OpenCode session ID to a global MCP server. The native plugin instead receives
the documented ToolContext.sessionID, verifies it through the host session API,
and fixes it once for its own child. The shared generated ChatBinding grammar
validates this opaque correlation; it supplies no vendor authentication.
Only that verified host response supplies the chat title. The child receives
the title at launch beside its fixed session ID; tool arguments and peer
messages supply neither. Host filtering drops unsafe or absent titles; the
generated ChatBinding byte bound drops oversized titles. The existing native
title owner normalizes display. Each session.created, session.updated or
session.status event rereads the host session; a changed title reaches that
chat's open child as `chat_title` on its lifecycle start call, which keeps the
child, its session ID, native canonical inbox and any active Wait. A child that
refuses the argument is restarted with the new title on its next idle call.
Native Connect/read supplies the enrolled helper's executable, and native
process/image/Keep checks retain authority. The existing typed inbox owner
enforces exact binding equality and reopens the same canonical context after a
child restarts. Models cannot select their own chat; destination routing fields
remain unchanged. Session deletion and plugin disposal close owned children;
an idle LRU bounds live children to32. Existing per-call cancellation leaves
other calls independent. The small client implements existing stdio JSON-RPC
framing and cancellation with no additional SDK dependency.

Documented `session.created`, `session.updated` and `session.status` host events
also open that same kept child before a model tool call. OpenCode's pinned
`@opencode-ai/plugin` 1.18.31 event hook uses the root SDK Event types: created
and updated carry `properties.info`; status carries `properties.sessionID`.
Each event still requires an exact `session.get` ID/directory readback and the
native Connect owner's selected enrolled helper. Event titles supply no binding.
A pending Connect is observed read-only on a later genuine event. No session
list is enumerated. A lifecycle observation invokes only the native session
start operation; it starts no model turn or idle wake. Merely viewing an old
conversation has no documented guarantee of emitting one of these events;
receiving after that view remains conditional on a supported host event or the
existing verified tool context.

Idle wake (architect ruling B, 2026-11-08; owner R15 report 2026-11-09). Each
started child holds `oa_messages_watch` open for 25 seconds at a time, which
reports the chat as watching. A page whose wake key is non-empty is eligible
only while the chat is idle: the plugin's own `session.status` tracking says
idle and OpenCode's `GET /session/status` map has no busy or retry entry for
that session. An unreadable map is busy. The plugin then reserves that exact
wake key on the same child; the helper's existing wake ledger returns one
ticket and its fixed pointer, and refuses a new ticket while an earlier pointer
is unread. The plugin submits only text that matches the generated wake
templates, through `POST /session/{id}/prompt_async` for that child's own
session, with the agent and model of the session's last user turn. Peer text
never enters the prompt. Several waiting messages cause one wake; the model
reads them with `oa_messages_wait` and the ticket. A busy chat waits for its
next idle event. An unknown `prompt_async` result keeps the ticket held and
pauses waking for that chat until its next turn runs. A woken turn also shows
the TUI toast "Message from <sender>"; OpenCode Desktop has no plugin-facing
notification API and shows its own response-ready notification when the turn
ends. A chat with no started child is not watched; its messages wait in the
canonical inbox until a later host event starts the child.
If the stdio transport closes while the plugin watches, it cleans up the
owned child and retries a read-only native Connect check with bounded
backoff. A fresh child must prove the same host chat ID and canonical
inbox before the existing watcher resumes. Lifecycle recovery replays no
foreground tool call.

Asks wake (asks ASK-R14, architect ruling 2026-11-12). While the helper's
program holds a current answering enrollment, each watch page carries
`asks_wake`, and a held watch lasts at most 15 seconds. A greater key wakes
one idle open chat once with the fixed pointer `OpenAbstractions: a question is waiting for your answer.`: the
live chat the person used last. Other chats take the key without a turn, and
a chat reached only because it is recent and closed is never woken for a
question. The model lists and answers through `responder@1`.

The plugin projects the full public tool catalogue of the kept native agent
helper, generated through the production gateway's capability shape. Source
parity checks compare complete tool definitions, input schemas and initialization
instructions with the actual Claude, Codex and OpenCode helper compositions.
Internal lifecycle tools remain private. `oa_models_connect` keeps its single
existing stock OpenCode caller's model setup owner; its public name and generated
schema are shared with the native catalogue. Provider and private image, speech,
selection, logs, credentials and folder operations retain their existing owners.

Official OpenCode Desktop/CLI 1.18.34 uses the V1 plugin consumer at the pinned
source above. Its `tool.definition` hook receives the canonical public input
schema through `output.jsonSchema`, including root combinators and conditional
constraints. Compact mode preserves that schema in full. The existing host-owned
empty input of `oa_messages_inbox_open` still prevents caller context injection.
The host's property-shape validator is a compatibility adapter; native MCP owns
execution validation. At the pinned source, `tool/registry.ts` retains the hook's
schema, `tool/json-schema.ts:fromTool` selects it, and `session/tools.ts` passes it
to the model tool's input schema.

The V2 provider and private tool behavior remains supported by its existing
shim. V2 native helper wrappers return unavailable: its metadata shim supplies
no validating host tool API or genuine chat session API. V2 native chat binding
is deferred pending a documented host API.

Installation removes only its previously owned global MCP seed
in the same atomic wrapper plan; a person-owned entry refuses migration unchanged.
An active global entry suppresses wrappers, preventing duplicate model rosters.
Installed Connect/ping and vendor UI behavior remain unqualified.

The source tests exercise canonical codecs, explicit same-process replay,
reasoning/history separation and the pinned stock processor retry closure,
retry policy and provider-error parser at OpenCode
`aec0b9a6d8898f68f923aaf08b7306d931fd9d76`. The processor test uses its exact
Effect 4.0.0-beta.83 runtime with inert display/session services. It requires
Node's TypeScript stripping support and OA_OPENCODE_EFFECT_MODULE pointing to
that selected runtime's dist directory. Ordinary tests without that runtime
report the case as skipped; a source acceptance receipt must execute it.

chat@3 remains unadvertised pending the listener's explicit served set, HTTP
chat@3 ingress and composed generated-green. Source proof supplies no installed
OpenCode, provider, restart journey or release qualification.
