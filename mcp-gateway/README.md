# OpenAbstractions MCP gateway

Codex setup is available from Panel → Apps → Connections → Codex, or through
`openabstractions-mcp connect codex --state-dir ABS --codex ABS_EXE`. It changes
the account's shared Codex configuration for desktop, CLI and IDE. The installed
0.164.0-alpha.11.1 CLI's add/get/remove grammar and selected-entry JSON were
received in an isolated configuration fixture. OA uses the vendor CLI exclusively,
retains positive ownership, and preserves foreign or changed entries. Restart
Codex after adding it; saving configuration does not establish a live connection.
See [Codex MCP](https://learn.chatgpt.com/docs/extend/mcp) and the
[CLI reference](https://learn.chatgpt.com/docs/cli/reference). Native Keep and
directional OA consent remain required; session mapping and idle wake remain
separate work.

Connect also installs the owned `oa-codex-session-start` local plugin through
Codex's plugin/marketplace CLI; disconnect removes only its exact owned entries.
Review and trust the definition once in Codex `/hooks`. Panel shows the hook's
`absent`, `installed`, `unavailable` or `preserved` status separately from MCP
setup. Plugin failures preserve the MCP result and changed/unowned entries.
The startup/resume hook calls the internal `oa_codex_session_start` tool with
`{}`. Only the dedicated native Codex composition exposes it; its schema refuses
arguments. It answers `observed` or `unavailable` and grants nothing. Existing
middleware binds the first valid host `_meta.threadId`; missing, invalid or
changed metadata afterwards ends receiving permanently on that connection.
Arguments and `session_id` supply no binding. Startup delivery is best effort;
host metadata and first-start ordering still require installed proof. See the
[official hook contract](https://learn.chatgpt.com/docs/hooks#mcp-tool-hooks).
Codex runs the hook only when the thread's first turn begins. After a Codex
restart, the Windows helper binds the one thread its parent app-server has
loaded, read from the holders of Codex's thread writer locks, so the reopened
chat receives without the person typing. It does so only when that app-server
started at most two minutes before the helper. Several loaded threads, or a
helper started later, wait for the first turn. The chat's title is the thread's current name in
Codex's session index.

Give an MCP assistant a small, reviewable set of OA abilities: see the models and
applications it may know about, see which catalogue model fits this computer for
a use, ask OA to set that use up, hold a bounded model conversation, and submit,
recover or cancel long-running image and video work. OA services own provider
keys and enforce the caller's rights. Enrollment reads withhold another
application's program path from nonoperators.

The shared gateway runs locally over stdio and exposes the tools below, backed by the installed
OpenAbstractions runtime:

| Tool | OA service | Bound |
|---|---|---|
| `oa_applications_list` | `abstraction.facade/applications@2` | permission-filtered local names, titles, instances, interfaces and contexts; 262,144 returned bytes |
| `oa_applications_describe` | `abstraction.facade/applications@3` | permitted AppModel applications, live instances and contexts; 1 MiB native reply |
| `oa_applications_enrollment_read` | `abstraction.facade/applications@3` | own enrollment or explicitly permitted other enrollment; other program paths withheld |
| `oa_applications_commands_list` | `abstraction.facade/applications@3` | kept-manifest commands for one live instance; 1 MiB native reply |
| `oa_applications_command_call` | `abstraction.facade/applications@3` | one command or binding tool under native caller authority; 1 MiB native request/reply |
| `oa_models_list` | `abstraction.router/router@1` | servable names, plus `servable` and the `held_in` stores of every model on this machine; `served_from` names the served copy when another held copy goes unserved |
| `oa_setup_fit` | `abstraction.model/catalog@1` | one `use` (`chat`, `code`, `vision`, `embed`, `image` or `video`); ranked entries with fit word, sizes, `ready` and the pick, under the router's `inventory.read`; downloads and selects nothing |
| `oa_setup_prepare` | `abstraction.facade/applications@3` RequestSetup | one `use`, `code` or `image`, under the caller's `abstraction.asks/question.ask` right; OA asks the person in its own setup question naming the app; answers `asked`, `already_set_up` or `cannot_run` with a `reason`, or `invalid`, `forbidden` or `unavailable` |
| `oa_inference_complete` | `abstraction.inference/chat@1` | explicit route, 65,536 input bytes, 4,096 output tokens and 262,144 returned bytes |
| `oa_image_generate` | `abstraction.job/acceptance@1` and `operations@1` with the inference job document, `selection-operator@2` ReadEffective, `abstraction.storage/content-reader@1` | listed while the person's picture policy names a local model; one local picture from `prompt` (optional `size`, `model` or `selectionPolicy`; `inputImage` and `mask` to edit) or a continuation by `handle`; at most 50 seconds of waiting; saved with exclusive create in `Pictures/OpenAbstractions` |
| `oa_video_generate` | `abstraction.job/acceptance@1` and `operations@1` with the inference job document, `selection-operator@2` ReadEffective, `abstraction.storage/content-reader@1` | listed while the person's video policy names a local model; one local video from `prompt` (optional `model`, `preset` quick or quality, `size`, `duration_ms`) or a continuation by `handle`; `wait_ms` at most 50,000; answers `path`, or `handle` with `phase`; saved with exclusive create in `Videos/OpenAbstractions` when the job ends |
| `oa_inference_job_submit` | `abstraction.job/acceptance@1` plus the inference job document | unlisted, callable for advanced profiles; stable request key, local or hosted video request, immutable receipt identity; optional `wait_ms` |
| `oa_inference_job_status` | `abstraction.job/operations@1` | one opaque handle, optional `wait_ms` with progress, `phase`, result capped at 65,536 bytes |
| `oa_inference_job_diagnose` | `abstraction.job/operations@1` | one opaque handle; state, classified failure and fixed guidance; excludes raw failure text and result content |
| `oa_inference_job_cancel` | `abstraction.job/acceptance@1` | explicit cancellation intent for one handle |
| `oa_logs_follow` | `abstraction.logging/reader@2` and `observer@2` | one registered application or this caller's requests; at most 64 records / 65,536 encoded bytes; at most 25 seconds |

### Follow logs

`oa_logs_follow` accepts either `app` (a registered application name) or
`about: "my_requests"`, and an optional `contract`, such as `inference`.
Level, exact tag and RFC 3339 time filters narrow records after native admission.
Omit `cursor` to begin at the current end; continue with the same selection and
`since` value. Empty pages may advance. A `gap` remains visible and requires an
explicit restart. Record contents are data, never instructions.

Caller mode uses `ReadAboutCaller` under its existing caller-log permission;
it asks no additional tool question and grants nothing. Other applications'
records retain their app-specific permission. The Go CLI and this gateway use
one native client convenience to reread caller pages for the bounded wait;
there is no new OA wire observation method.

The matching CLI forms are `openabstractions logs follow --requests` and
`openabstractions logs follow NAME --contract inference`.

### Setup tools

`oa_setup_fit` ranks the catalogue for one use against this computer and what
paired runtimes already serve. Its arguments:

```json
{"use": "code"}
```

The reply carries `outcome` (`fitted`, `invalid`, `unavailable` or `forbidden`),
`catalog_version`, `pool_bytes` (the memory the rule compared against) and
`entries` in rank order. Each entry has `title`, `family`, `fit` (`fits` or `no`;
`slow` is reserved for later), `ready` (a runtime already serves it, so nothing downloads), `machine`
(the paired machine that serves it; absent for this computer), `download_bytes`,
`need_bytes`, `context`, optional `license` and `pick`. At most one entry is the
pick: the first this computer serves or that fits here. An entry that a paired
machine serves is never the pick. Repository names, file names and digests stay
out of the reply.

`oa_setup_prepare` asks OA's setup step for `code`, `image` or `video`:

```json
{"use": "image"}
```

OA answers `already_set_up` or `cannot_run` from its catalogue and chat
default. Otherwise, while the setup step is watching for requests (it is waiting
now or ended within 25 seconds), OA keeps one pending request for at most a
minute and answers `asked`; the person answers OA's question naming the app, and
the reply never carries that answer. With no setup step watching, OA keeps
nothing and the call answers `cannot_run`. After the person
answers Not now, OA asks for no app for 24 hours or until the person starts
Setup from Settings, and the app still hears `asked`. A caller with no person
reachable on this runtime hears `forbidden`. Restart the app after the person sets
the use up. Nothing in this call downloads, selects or grants.

`video` sets up LTX-2.5, video with sound (architect ruling 2026-11-11, Setup
use video). The caller's kept manifest or agent release declares a `video`
ModelNeed. OA answers `already_set_up` when its router serves `ltx-2.5` for
video on this computer, and `cannot_run` with one sentence when the memory free
for models is below the set's need of about 53 GB. Otherwise OA asks "OpenCode
asks to set up video with sound on this computer." naming the app, with the
download and the free space it needs; Show details gives the licence, the
Hugging Face sources, the memory need and the preset times. On Set up, OA gets
the pinned engine and every file of the set through its download jobs and
declares the video provider and names it in the person's `oa-video-default`
policy. Agents then make a video with `oa_video_generate`; the clip is saved
in `Videos/OpenAbstractions` when the job ends, whether or not the agent is
still waiting.

The dedicated native agent composition additionally exposes Request Access,
Prepare, Inbox Open, Inboxes List, Send, Reply and Wait after its kept native
application instance opens an inbox, and the five `oa_asks_*` responder tools.
Initialize instructions disclose its actual opaque child-lifetime context.
`oa_messages_inbox_open` uses that context when its `context` argument is
omitted, so an assistant calls it with no arguments once when a chat starts or
resumes. That routing context carries no vendor-session, reconnect or wake guarantee.
The Panel's user-wide setup uses the vendor CLI and requests native Keep separately;
Claude project approval and directional OA consent remain with their owners.

For message receiving, keep the **OpenAbstractions MCP connection** running in
both agent apps. An open window or a new chat alone proves no live connection.
The helper maintains its 30-second native presence every 10 seconds while that
connection lives. Transient receiving uncertainty suspends native tool work
within the original confirmed lease. Fresh proof can resume that same binding;
terminal loss or confirmed expiry stops the server and withdraws the original
instance. Model-turn completion has no separate helper lifecycle hook.
After a connection closes, use the host's supported MCP restart/reconnect or
close and reopen the client, then discover fresh authorized inboxes. This does
not establish automatic reconnect, old-context continuity or idle-model wake.

After a payload update, use the existing Panel connection owner to update a
separately retained helper and complete any fresh native Keep before reopening
its MCP connection. Installing the runtime payload alone neither updates every
retained helper copy nor establishes a live client connection. The update owner
checks positive vendor-entry ownership and native image identity; replacement
failure retains recovery state. Source acceptance and installed receiving remain
separate.

The Claude user connection also has a personal user-scoped native mod
companion. Its manifest starts no MCP server: background timers call the
existing `OpenAbstractions` MCP connection for authorized headers and submit a
fixed plugin-attributed pointer only after a fresh eligible observation. Normal
OA tools read the body and reply. Busy turns finish before the pointer starts
a new turn. Native admission uncertainty can recover only for the same inbox;
unknown prompt submission stops resending. Reload plugins or reopen Claude
Code after the guarded companion installation. Native validation and actual
Desktop idle reception remain separate acceptance. Channels development
source remains unintegrated; no second asynchronous delivery owner is enabled.

The gateway advertises tools only. It has no roots, resources, prompts,
sampling, elicitation, subscriptions, MCP Apps, remote transport or arbitrary
callback surface. The server accepts MCP `2026-07-28` and `2025-11-25` through
the official Go SDK v1.8.0. The adapter version, MCP protocol version and OA
service contract versions remain separate compatibility facts.

## Connecting products

OA's standard connection setup uses each product surface's supported extension
mechanism, with research and receiving for the selected version (owner direction,
27 October 2026). This adapter is the local stdio tool server. The table records
the setup choices and their current scope; installed and release qualification
remain separate.

| Product surface | Supported setup owner | Current scope and source |
|---|---|---|
| Claude Code, including local Code projects in the desktop app | Panel **Add to Claude Code** adds the `OpenAbstractions` MCP server at **user scope**, across projects. No project picker is required. The project plugin remains optional. | User add/exact selected-entry readback/remove received in an isolated Code 2.1.309 fixture. Installed loading and actual vendor tool use remain separate. [User setup](#claude-code-user-connection). |
| OpenCode native adopter | Preserve the adopter's `configure.js` owner for OA providers, models, plugin and local settings. An explicitly selected MCP-only connection uses OpenCode's native MCP configuration or interactive CLI. | The fuller adopter remains in `adopters/opencode`; MCP-only tools are a separate option. Match the selected version's configuration shape. [MCP](https://opencode.ai/docs/mcp-servers), [CLI](https://opencode.ai/docs/cli). |
| Local ChatGPT Work/Codex, Codex CLI and IDE | Native MCP settings or `codex mcp add`; exact readback is available through `mcp get --json`, with native removal. | Shared local configuration can affect multiple clients; scope and trusted-project rules must be shown before setup. No installed receiving is claimed here. [Official OpenAI MCP documentation](https://learn.chatgpt.com/docs/extend/mcp). |
| Claude Desktop chat | Its extension manager and a separately packaged `.mcpb` extension. | Separate surface/package work; the Code CLI commands configure Code. [Desktop extensions](https://support.claude.com/en/articles/10949351-getting-started-with-local-mcp-servers-on-claude-desktop). |
| Hosted ChatGPT | Its supported hosted plugin/remote-MCP route. | Separate implementation and publication work. The local stdio gateway supplies no direct web connection. [Hosted MCP scope](https://learn.chatgpt.com/docs/extend/mcp). |

The optional OA MCP gateway feature is off by default in Setup. The Claude Code
commands below require that executable and a supported installed Code CLI. Setup
does not install another Claude client, choose an authentication profile or grant
OA rights. The adapter currently reports `0.1.0-dev`; the receiving below uses a
source-built development executable.

Keep these facts separate: registration saved, host approval pending, MCP
connected, OA runtime reached, and a particular capability permitted/available.
For example, the received Code CLI health check connected while the installed
router reported unavailable. A permission-filtered applications page with zero
visible rows completed successfully. Neither observation receives agent task
delivery, Messages or Stop wake.

### Claude Code user connection

Panel **Add to Claude Code** uses the native CLI's user scope and the exact name
`OpenAbstractions`. This applies across Claude Code projects, including local
Code sessions in Claude Desktop. It reuses the dedicated integration image and
person-owned native Keep. Reopen Claude Code after setup. A local or project
entry with the same name can take precedence; setup preserves those entries.

```powershell
openabstractions-mcp connect claude --state-dir 'C:\work\oa-mcp-state' --claude ABSOLUTE_CODE_CLI_PATH
openabstractions-mcp disconnect claude --state-dir 'C:\work\oa-mcp-state' --claude ABSOLUTE_CODE_CLI_PATH
```

All mutations use `claude mcp add/remove --scope user`. Code 2.1.309's named
`mcp get` health-connects the configured server and provides no JSON readback.
The explicit setup action instead uses the bounded native file reader to select
only this named entry from the user's configuration, prove exact command,
arguments and environment, and retain positive ownership. Other values stay
opaque and are never printed. Unavailable, malformed, ambiguous, changed or
unowned entries refuse. Virtualized user profiles refuse. The CLI remains the
sole configuration writer. Saved status proves configuration only; it supplies
no live vendor connection, OA permission or tool-use result.

On 28 October 2026, the person reported ordinary Claude Code receiving from the
installed unsigned DEVELOPMENT preview `d83efcee3`: restarted Claude loaded
`OpenAbstractions`; its first model-list call was refused with `router operation
not permitted`; after an explicit access grant, cached and fresh model-list calls
both succeeded with empty inventories. No inference or download was performed.
This records the owner's demonstration, rather than a raw automated tool receipt.
Messages and idle wake retain their separate acceptance.

### Claude Code project registration

Use Panel's user connection for ordinary setup. These explicit commands retain the bare MCP
project registration route and its original ownership/removal behavior.

Choose the project and an absolute private state directory explicitly:

```powershell
openabstractions-mcp connect claude --project 'C:\work\my-project' --state-dir 'C:\work\oa-mcp-state'
openabstractions-mcp disconnect claude --project 'C:\work\my-project' --state-dir 'C:\work\oa-mcp-state'
```

The command uses the installed Code CLI named `claude` on PATH. When it is not
on PATH, pass `--claude ABSOLUTE_CODE_CLI_PATH`. The desktop chat GUI executable
is a separate program. `--name NAME` selects the server alias; its default is
`openabstractions`. Names contain 1–64 ASCII letters, digits, hyphens or underscores.
The scope is always the project selected by the explicit flags. Vendor
add/remove operations are the sole writers of the public `.mcp.json` entry.

Setup reads that selected public record for exact readback and retains a small
ownership marker in the chosen state directory. An exact owned entry can be
reported as already registered. A foreign, changed, identical-but-unowned or
unprovable same-name entry is preserved and refused at preflight. Removal requires
the original marker and matching current entry. Vendor removal is name-based and has
no compare-and-delete operation: a concurrent replacement after validation can
be removed. Avoid concurrent edits while setup runs. It removes that registration;
durable job state, OA grants and other scopes remain with their owners.

After assistant enrollment is revoked, the installed management image can use
`disconnect claude --registered-program ABSOLUTE_CONFIGURED_COMMAND` with the
same project/state/alias. The existing ownership record and current entry must
match. That configured command is never opened or executed by removal. Connect
requires its admitted dedicated image and refuses `--registered-program`.

The vendor commands are `claude mcp add --scope project --transport stdio NAME
-- ABSOLUTE_GATEWAY --state-dir ABSOLUTE_STATE` and `claude mcp remove --scope
project NAME`. Use the named vendor `claude mcp get NAME` for its own status.
Code 2.1.308 has no JSON option for get/list; setup uses the documented public
project record for readback and leaves human status text as human output.
Higher-precedence product entries or real project approval can still affect
the effective connection. A saved project entry may show Pending approval;
approve it through Code's ordinary project controls before expecting tools.

Received on 27 October 2026 with Code 2.1.308: add/get/remove at project scope,
exact public-record readback and removal, then a separately owned local-scope
health connection through the actual CLI. The latter reported Connected and
was removed at its original scope. No model session or agent tool-use/wake was
part of that receiving. Local scope was an isolated test; the OA setup command
supports project scope.

`openabstractions-mcp connections claude --state-dir ABSOLUTE_STATE` reads OA's
existing ownership records and their selected user or project entries. Its bounded
JSON reports saved, changed or absent registrations with their project, helper
and name. It does not inspect agent profiles or check approval, live connection,
OA permission or message delivery. Unknown or incomplete information returns
unavailable. Reading this inventory creates no state or registration.

### Claude Code personal project plugin setup

The dedicated integration image also provides `plugin claude install|update|remove`
with explicit `--project`, `--state-dir` and `--claude` selections supplied by the
setup owner. This personal connection always uses the vendor's **local** scope.
Its marketplace and enablement declarations belong in the project's
`settings.local.json` in its `.claude` folder; the shared `settings.json`
beside it stays unchanged.
The existing bare MCP project-registration commands above remain available.
This optional route includes its MCP command and native session module. Basic
Panel **Add to Claude Code** uses user-wide MCP alone. Saved inventory labels
owned plugins and legacy bare MCP entries separately; removal uses each entry's
existing scoped owner. Saved setup confirms configuration only. Live connection
and session wake require their own receiving.
For a plugin row, the explicit connection update first checks or updates its
native helper and confirms any fresh Keep, then updates the owned plugin payload.
An unchanged helper needs no new Keep. Legacy bare rows keep helper-only updates.
An unconfirmed plugin update reports failure and preserves the person's native
permission decision and any retained helper backup.
The MCP server key is `OpenAbstractions`. The native module addresses it as
`plugin:openabstractions:OpenAbstractions`, using the documented
[`plugin:<plugin-name>:<server-name>` form](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers).
The plugin ID and plugin-origin name remain lowercase.

The locally packaged OA plugin declares the dedicated native MCP command and a
native module entry. The module observes the current admitted connection through
the development-only `oa_messages_watch` adapter and submits a fixed,
mod-attributed pointer. Its native `$.session.id()` stays an in-memory lifetime
fence; it is never sent as an OA context or authentication claim. The adapter
returns only the backend's own context, header revision and pending-question
deadline. It accepts no application, context, instance, body cursor or ACK.

The generic tool inventory is separate. An admitted dedicated
Messages backend additionally exposes this
bounded readonly adapter. User stop pauses observation until engine-stamped composer
input rearms it. The module rechecks its own plugin-origin prompt, current
connection and deadline, submits at most once per minute, and passes no peer
text or `asUser` flag. Reconnect to a different gateway occurrence refuses the
old association. Configuration and inert source receiving establish no actual
Desktop loading, two-session isolation or idle-wake qualification.

Setup retains private payload provenance separately from confirmed marketplace
and plugin ownership. Successful vendor mutation and exact local/native readback
establish ownership. Foreign, changed and unconfirmed configurations are
preserved. Every scope-taking configuration mutation passes `--scope local`,
including marketplace removal. Named marketplace refresh uses the exact
previously verified local source; its vendor command has no scope flag. The
vendor's unscoped remove command would remove declarations from all scopes.

An isolated Code 2.1.308 configuration cycle received local add/install/list,
update from 0.0.1 to 0.0.2, uninstall and marketplace removal. The shared settings
sentinel stayed byte-exact and unrelated local settings survived. No MCP server,
hook, model or session ran in that cycle.

### Native messaging access and discovery

Kept agents can discover permitted conversations and local installed agents
that need the person's permission. Conversation metadata stays behind the
directional grant. The native tools are:

| Tool | Input | Existing owner |
|---|---|---|
| `oa_messages_prepare` | `{"receiver":"Codex","body":"Can you review this change?"}`; optional `context` (an exact context from authorized inbox discovery), `kind` (`question` by default, or `note`), one-line `subject`, `ttl_ms` (600000 by default, 1000..604800000), and question `answer_within_ms` (defaults to its lifetime) | read-only intent construction using current native authorized inbox discovery; sends nothing and grants nothing |
| `oa_messages_request_access` | `{"receiver":"app:oa-claude-integration","idempotency_key":"access-1"}`; the exact program from `needs_permission`, or Claude, Claude Code, Codex or OpenCode, and an explicit operation key (1–128 UTF-8 bytes, no control characters); additional fields refused | caller-bound `abstraction.asks/permission@1 Request`, then up to four seconds of same-key observation and live inbox confirmation, using Facade's exact One `abstraction.facade/named-agent-message@1` profile. A pending question stays open. Resume only on the person's request, with the same receiver and key. |

Guided setup registers Codex as `oa-codex-integration` and Claude as
`oa-claude-integration`. The service validates current native enrollment of both
parties. The tool accepts no inbox context, caller identity or prompt wording.
Keep `idempotency_key` and receiver unchanged when explicitly reconciling an unknown
or lost reply. Reusing a key with changed intent refuses. After Undo or a native
enrollment change, a fresh explicit review uses a new key. Permission's existing
pending-request coalescing and refusal pacing still apply. The adapter performs
no automatic admission retry or regrant. The person can say “message Claude” or
“message Codex”; the model resolves the receiver and authorized live inbox with
the tools and never asks the person for internal application or context names.

1. Call `oa_messages_inboxes_list`. `inboxes` contains conversations this caller
   may message. The first local page may also carry `needs_permission`, with
   only the program and registration title of installed kept agents the caller
   may ask the person to message. Remote and paired pages omit this list.
   When both lists are empty, no agent can be messaged right now. Before telling
   the person anything, call `oa_messages_prepare` with Claude, Codex or OpenCode
   and the person's message. Prepare sends nothing, and its outcome gives the
   next step. Otherwise use the returned program without guessing names.
2. Explicitly call Request Access for that program. It creates no dummy message.
   The active call waits within the existing two-minute gateway budget for the
   same request's decision. Allow and rule application are separate commits;
   the tool confirms current permission and an authorized live inbox before
   returning permitted. A pending/answered result at the deadline supplies no
   permission proof or idle-model wake; any later continuation is explicit.
   The person reviews the native directional consent. Only that person's Allow,
   completed through the existing Rights effect owner, grants the direction.
3. Call `oa_messages_inboxes_list` to discover authorized inbox metadata and the
   receiver's opaque context. Pending or refused access reveals no receiver inboxes.
4. Call `oa_messages_prepare` with the discovered program and message text. It
   selects exactly one authorized live inbox or refuses/asks for clarification.
   It returns a complete `send_arguments` object, including the stable key and
   absolute Unix-millisecond expiry and question answer deadline. Retain that
   object in the conversation before calling `oa_messages_send` with it verbatim.
   No person needs to supply internal IDs or calculate clock values. A program
   name alone selects the program's one live inbox. With several, it returns
   `ambiguous_receiver` with the live chats as choices and their hints, and the
   agent resends with the chosen choice's context and the same
   idempotency key. If there is no live inbox for the authorized program, it
   returns `no_reachable_chat`. A request that
   names a specific chat may return one of these outcomes instead of `prepared`:
   `receiver_withdrawn` (the receiver's last connection closed normally; the
   person can reopen that agent's chat), `receiver_lapsed` (the connection
   stopped renewing without closing; the host ended it or it failed, and it needs
   its MCP server restarted or its chat reopened) and `receiver_unavailable` (no
   authorized live inbox is visible, or the runtime restarted and has recorded no
   end yet). Withdrawn and lapsed appear only to a sender whose message right is
   granted. `ambiguous_receiver` asks for an exact `context` from step 3.
5. After an uncertain Send reply, explicitly reconcile through the **same
   original native MCP connection**, using its `send_arguments` unchanged.
   Receipts bind that source context too. If the connection closed, stop and
   seek explicit outcome review before any resend. Never call Prepare again to recreate that send's
   key, content or expiry. Prepare keeps no dedupe store and creates a new intent
   on each invocation. Send still checks current authority and deadlines.

Prepare's live-inbox requirement makes natural-language routing unambiguous.
The underlying Send service still permits queueing before a named context opens.
An empty inbox list alone therefore does not explain a raw Send `invalid` result.
A question requires a future `answer_by_unix_ms` no later than `expires_unix_ms`; expiry is
future and at most seven days. Subject, body, key and routing grammar still use
the unchanged existing service validation.
   The reverse direction requires its own consent.

This path preserves the existing `application.message` rule, blocks and
registration lifecycle. Native source fixtures receive consent, authorized
discovery, Send/receive and Undo; actual installed Claude association, person
interaction, model delivery and idle wake retain their separate pending proof.

### Reply-only turns

A turn OA starts in a chat because another agent's message arrived is
reply-only until the person allows more (architect ruling 2026-11-22,
`CONTRACT.md` MCPG-R1). The turn may read the message and reply in that thread,
read, list and search files inside the chat's working directory, and use OA's
read-only tools. Everything else is refused with "This turn was started by a
message from <sender>. Ask the person to allow more for this conversation."
Any prompt the OA plugin did not submit itself, the person's included, gives a
normal turn. Panel → Permissions →
Messages between agents has one switch per agent pair, "Allow full tool use
when <sender> messages <receiver>", off by default; every change is an
Activity line.

Claude Code and OpenCode refuse their own tools through the OA plugin's tool
hook. **Codex limit:** OA refuses only OA's own tools in a woken Codex turn.
Codex's built-in tools (shell, file edits, web) follow Codex's own approval
policy, which OA does not change; the pointer OA queues tells the turn it is
reply-only. Keep Codex's approval policy at "on request" or stricter in
projects where other agents may message it. Per-turn approval through
Codex's app-server is planned for 0.9.0.

## Authority and durable state

OA observes the gateway executable as the native IPC peer. Operators grant the
gateway executable narrow rights as one integration principal. MCP `clientInfo`,
request `_meta`, parent PID and tool arguments are retained as untrusted protocol
data by the MCP implementation; none selects an OA subject or grants rights.

All product sessions that launch the same executable intentionally share that
OA integration principal. Two separately authorized principals require two
distinct executable identities that the native OA boundary can distinguish and
separate rights rules for their absolute paths. A second `--state-dir` alone is
an organizational boundary and supplies no OA identity isolation.

The state directory contains random opaque job-handle records with the exact OA
endpoint binding, logical owner, contract, guarantees and request identity. The
records are atomically written and file-synced before submission. Submit requires
a stable caller `request_key`; the same key and immutable specification reconcile
the saved identity, while a changed specification is refused. MCP disconnect
does not cancel accepted work. Cancellation is explicit.

Handle records contain no prompt, result, credential, provider header or secret.
The OA services apply credentials and authorization. Status returns only the
original principal's bounded operation snapshot and typed inference result.
Credential fields are OA credential names, never secret values. Durable
generation takes `hosting: "local"` with no credential, which runs on this
computer's servers under the local-only guarantee, or `hosting: "hosted"`
with a credential name. A local job with a credential, a hosted job without
one, or any other hosting is refused for good with a sentence that says what
to send instead, so a model does not guess credential names. Text inference
requires `hosting: "local"`, or hosted plus a credential name. The
server allows eight concurrent calls, gives each a two-minute deadline, and
defaults `max_output` to 1,024 tokens.

Application discovery is read-only. It omits executable paths, start guidance
and activation recipes, and exposes no activation, mutation or generic
application-forwarding tool. OA filters each snapshot for the gateway's native
executable principal; MCP client metadata cannot select another caller.

## Connection check, development build and OpenCode configuration

Run `openabstractions-mcp --check` for a bounded connection check. It selects
the installed runtime through the normal trusted bootstrap, launches this same
gateway executable over stdio with owned temporary state beneath storage's
admitted transient root, and uses the official
MCP SDK to initialize and list tools. It then calls only applications listing
with empty arguments and models listing with `fresh: false`. Both reads are
attempted even if one fails. The explicit selected endpoint prevents runtime
activation; the child independently verifies the installed server identity.

The check reports MCP connection separately from OA capability availability,
with counts and fixed error categories. It prints no application/model names,
credentials or raw service errors. Exit zero requires both read probes and
child cleanup to succeed. The work and shutdown budget is at most twenty
seconds. It grants no rights, changes no integration settings, starts no
inference or jobs, and leaves the normal stdio/state-directory mode unchanged.

Build the unsigned development command without installing it:

```powershell
go build -o .\out\openabstractions-mcp.exe .
```

Grant only the actions the selected local hosts need to the resulting absolute
executable path. Application discovery needs
`abstraction.facade/application.read` on each exact `app:<name>` that should be
visible. Model inventory needs
`abstraction.router/inventory.read` on `abstraction.router/inventory`; the
`held_in` stores travel on that same listing and need no further rule, because
the runtime composed them into the catalogue before the router answered.
`oa_setup_fit` needs the same `inventory.read` right, because catalog@1 Fit is
gated like the router's Offer. `oa_setup_prepare` needs the caller's
`abstraction.asks/question.ask` right on the account. Text and
durable inference need `abstraction.inference/complete` on each selected
`host:<name>`. Durable submission also needs
`abstraction.job/acceptance.submit` on `abstraction.job/acceptance@1`. The
runtime's rights command records exact grants; do not copy the runtime's broad
installation grants to this adapter.

OpenCode 1.18.31 accepts an absolute command and state directory in
`opencode.json` or `opencode.jsonc`:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "openabstractions": {
      "type": "local",
      "command": [
        "C:\\path\\to\\openabstractions-mcp.exe",
        "--state-dir",
        "C:\\path\\to\\oa-mcp-state"
      ],
      "enabled": true
    }
  }
}
```

The opt-in adopter test uses a real OpenCode CLI with isolated XDG config, data,
cache and state directories and local fake model/provider endpoints:

```powershell
$env:OA_OPENCODE_BINARY = 'C:\path\to\opencode.exe'
go test ./gateway -run TestOpenCodeInvokesGatewayInventory -count=1 -v
```

The recorded demonstration invokes five tools — `oa_models_list`,
`oa_inference_complete`, `oa_inference_job_submit`, `oa_inference_job_status`
and `oa_inference_job_cancel` — consuming their structured results, cancelling
an accepted pending job and surfacing a typed unknown-handle refusal from a
second `oa_inference_job_cancel` call. It leaves `oa_applications_list`
unexercised, contacts no paid provider, and the normal test suite skips it
when `OA_OPENCODE_BINARY` is absent.

The diagnosis tool has focused native-service tests for restart recovery,
cross-principal refusal and sensitive-text exclusion. It diagnoses a known
durable inference job handle. Recent activity, download diagnosis and operating
system logs remain separate capabilities to qualify.

Hosted completion resolves the runtime's remote-execution binding; local
completion resolves its local-execution binding. Both use the same authenticated
native IPC transport to OA. Explicit execution placement is independent of that
local connection. Saving a handle record acquires the recovery file's lock
within the calling tool's own bounded context, so a stuck lock wait times out
with that call's two-minute deadline instead of blocking indefinitely.
