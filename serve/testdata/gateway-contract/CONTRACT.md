# abstraction.inference contract

`inference.thrift` defines model calls through `chat@1`, `chat@2`, `embed@1`,
`transcription@1`, `speech@1`, `live@1`, `image@1` and `image@2`; operator
services administer hosts and scoped selection policies. `job.thrift` defines the durable document job
kind `inference` carries for video and image-batch work, executed by the same
provider. `video@1` is the service a native video provider serves to that
executor. This file states the obligations a provider meets.

Binds: `inference.thrift`

The source also defines the unpublished `chat@3` final-form window. The
local application endpoint serves chat@3 beside chat@2 and advertises it once
trusted composition binds the logical owner. A designated one-hop leaf serves
it to its runtime relay. The application endpoint is ordinary, unbound and
local; it retains the shared native caller and admission checks.
Chat@1/@2 receiving is frozen. Chat@3 separates progress,
public reasoning and answer blocks; optional usage preserves absence versus zero.
Its opaque reasoning seal is origin-bound and omitted whole with a typed audit
reason when incompatible or over-bound; it is never truncated into replay data.
Accepted-work failure is terminal failed/cause, distinct from authority refusal.
A non-authority exhausted/unavailable capacity hint is optional, positive and at
most one day; it grants no permission to retry accepted work or duplicate Start.

`RequestV3.conversation` is an optional caller-local label, 1..128 bytes with no
control characters. It grants no authority. The service MUST derive its cache
scope as a CONV-T3 digest of the attested account, attested program and label.
Slot affinity, persisted cache entries and hosted `prompt_cache_key` MUST share
that derived scope. An absent label retains the account/program partition
without chat affinity. Request identities retain Job's acceptance meaning;
Messages `ChatBinding` retains its separate receiving-route meaning.

The private native cache partition separately binds the attested account and
program. The original ingress derives it; a designated native leaf accepts its
reserved extension only through the existing current runtime-peer recheck.
Missing forwarding evidence starts cold. A paired runtime derives its own
partition from its attested caller. OA669 preserves the conversation digest and
keeps audit claims separate. Provider-owned resident hit/miss, closed cause and
one eviction observation reach the existing operation timing record before
finalization. The operation ignores late reports and keeps its monotonic prefill
measurement separate from engine timing claims.

The new identity uses unchanged Job RequestIdentity/HistoryWindow semantics with
the runtime's already-owned persistent Job logical owner, or a designated
leaf's compiled declared provider name, with ephemeral epochs. Modelhost's
logical owner is its declaration's `modelhost` name. Trusted composition binds
that owner once. Before binding, chat@3 is unavailable before reservation or
effects. A different owner cannot replace it; restart keeps the same logical
scope with a fresh volatile epoch. Closing epochs fence unseen keys and retain promised
replay evidence; restart/expired evidence is unknown, never permission to submit
again. Private source implements lifecycle folds in all five clients, actual
loading phase projection, host-visible reasoning with clean history, MCP
opt-in structured reasoning, receiving-side program accounting and GenAI log
attribute mapping. Paired-remote, hosted-allowed, mediated and HTTP gateway
chat@3 remain Not Built, together with host-owned final-request persistence.
Required features whose capability owner has
no declaration refuse unsupported before effects; no capability is inferred
from a model name. Upstream Retry-After maps to the new typed hint without retry.
The existing [reasoning ticket](../../backlog/reasoning-handling-defect-candidate.md)
retains their complete accepted bundle and qualification limits.

An application names a model family and the behavior it needs. The runtime
chooses an allowed local engine such as Ollama or Lemonade, or a hosted
server such as OpenRouter, then applies the named credential inside the
service. [README.md](README.md) holds the walkthrough: setup, a worked chat
call, and the gateway.

## Reading this page

The conventions every OA contract carries are stated once in [CONVENTIONS.md](https://github.com/openabstractions/abstractions/blob/main/idl/CONVENTIONS.md); a rule here that applies one cites its `CONV-` id.

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this
document are to be interpreted as described in BCP 14 [RFC2119] [RFC8174]
when, and only when, they appear in all capitals, as shown here.

The rule ids below, such as `INF-A1`, are what tests and refusals cite. A
retired id is never reused; [HISTORY.md](HISTORY.md) keeps it with the
release it left.

| Letter | Meaning |
| --- | --- |
| A | admission |
| V | image input (vision) |
| C | credentials and spend |
| M | modalities: embeddings, transcription |
| Q | the request lifecycle, including each language client |
| S | speech |
| L | live |
| H | model-server registration |
| K | gateway keys |
| W | the gateway |
| P | the remaining operator calls |
| R | the closed rights-action catalogue |
| F | the files of an engine a provider launches |
| MS | a model's bytes in a memory section a provider host lends to the engines it launches |
| PL | preloading a chat prefix ([Preload](#preload)) |
| G | video generation ([Video generation](#video-generation)); G2 and G4 reserved ([Next](#next-video-generation)) |
| I | image edits ([Image edits](#image-edits)) |
| MP | the encoding units of a model pack and its proof ([Model packs](#model-packs)) |
| VE | the video encoding of an engine's raw frames ([Video encoding](#video-encoding)) |

`E` (error and outcome) and `X` (extension) are the reserved letters of
`research/vocabulary/DECISION.md` S12; this page declares no rule under
either this release.

| Prose | Wire (until its own release) |
| --- | --- |
| model server, `server:<name>` | `host:<name>` in stored policy, and `Hosts`, `AddHost`, `RemoveHost`, `HostEntry`, `HostChange`, `HostList`, `HostState` on `operator@1`; the dual reader lands with rights' stored policy, and the `Servers`/`AddServer`/`RemoveServer` rename with `operator@2`, neither here |
| `inference server add`, `inference server list` | `inference host add`, `inference host list` (CLI, `serve/inference.go`); lands with the Panel and command-line step |
| modality, `modality:<name>` | `profile:<name>` (rights resource), `host_profiles`, `Config.Admit`'s `profile` argument, `--profiles` (CLI); `chat@3` (P-J5b with W2-06, Train F) and `registry@2` carry the rename; `chat@2` went to selection targets |
| request | the `operation` field on [`Admission`](https://openabstractions.github.io/reference.html#concept-inference.operation-admission), `Reply`, `AuditEntry` and elsewhere; `chat@3` (P-J5b with W2-06, Train F) |
| idempotency key | Job's `RequestIdentity`; its wire spelling stays until the owning service's next version (D57, S13) |
| budget | `CeilingLimit`, `host.ceiling`, `Ceilings`, and reason `ceiling:<unit>:<credential>`; `budget_exceeded` and `budget.manage` are already this word on the wire and need no table entry |
| the gateway, gateway key | earlier prose said "gateway window", "window" and "local key"; the wire kind `openabstractions/local-key@1` and the `INF-W` letter stay |
| hosted server | the upstream vendor behind a hosted model server, for example OpenRouter; the rights resource stays `host:<name>` (row 1 above) |
| `inference/chat.complete` | `abstraction.inference/complete`; the stored-policy dual reader lands with rights, not here |

## Admission

A waiting or slow call never delays another caller, app, service, channel or
connection. This includes calls sharing one helper, plugin, connector or IPC
connection. A connection carries concurrent calls without head-of-line
blocking. The IPC transport multiplexes requests by id, or the client opens
one connection per in-flight call. Helpers, plugins and connectors serve tool
calls concurrently. A call may wait for its
own reply within the caller's wait_ms. No lock is held across a wait, an
upstream call, disk or network I/O, or a person's answer. A waiter costs no
CPU while it waits (WATCH-W3). Admission runs once per wake and per call.
Per-caller limits bound waits and queue use.

**[INF-A1] Peer binding.** Every call binds the receiving peer by native
[Program](https://openabstractions.github.io/reference.html#concept-identity.listener-program-need) proof (kernel user and process, bound path). A peer that cannot be
bound, or a peer of another account, reads `forbidden` before the request
is examined further.

**[INF-A2] Evaluation order.** `Start` evaluates in this order and stops at
the first refusal:
1. argument shape and bounds (`invalid`);
2. guarantees and model-server selection through the router (`no_host`,
   reason `router:<verdict>` or `engine:unverified:<host>` when the local
   engine lacks the native program evidence required by its HTTP transport);
3. the chosen model server's features and required extensions
   (`unsupported_feature`, reason `feature:<name>` or `extension:<key>`);
4. the rights decision for `abstraction.inference/complete` on
   `server:<name>` for the bound subject (`not_permitted`, reason
   `rights:<word>`; `unavailable`, reason `rights:unavailable`, when the
   decision point cannot answer);
5. image content resolution under the original caller's rights (INF-V1);
6. the credential's budget, read from the model server's own declaration
   (`budget_exceeded`, reason `ceiling:<unit>:<credential>`);
7. the caller's concurrent requests (`exhausted`, reason `capacity`);
8. for a hosted server, `abstraction.credentials/applier@1` `Apply` with
   consumer the contract of the call (`abstraction.inference/chat@1`,
   `embed@1`, `transcription@1`, `speech@1`, `live@1` or `image@1`; an image
   or video job applies as `image@1`), the bound subject, the server's
   credential name and its lowercase host name (`not_permitted` with
   reason `credential:<outcome>:<name>`; `unavailable` with reason
   `credential:unavailable:<name>`).
9. the provider's admission hook, when configured, before accepted execution
   and after the preceding authority and feature checks. Its model activity
   reservation stays visible until the operation is registered or refused.

No byte leaves the service before every step passes. A decision point that
cannot answer never reads as permission. A provider's own refusal — a
resource lease the pool could not satisfy, a lease request the holder
refused, no engine bound, or an unknown model object — reads `unavailable`
with reason family `lease:<outcome>`, `engine:none` or
`object:unknown:<store>/<id>` (DECISION.md D87); this page carries no
refusal word of its own for those causes.

The request record for `engine:unverified:<host>` names the refused model
server in `unavailable_hosts`. This records the verification refusal and
grants no fallback or permission to send an upstream request.

**[INF-A3] Guarantees select model servers.** A request without
`abstraction.inference/hosted-allowed@1` is served only by model servers on
the receiving machine. A request with it may also be served by a hosted
model server whose credential name equals `Request.credential`, ranked
after every resident and would-load local server (router `Pick`). A native
caller's request on an endpoint that may reach hosted servers and names no
credential reaches each hosted server under the credential its declaration
names, as INF-W5's placeholder key does. On this machine, a program without
its own `complete` rule on a hosted server is decided by its
`abstraction.credentials/apply` on the server's declared credential, so its
first use asks the one question about that account. A
request naming both `local-only@1` and `hosted-allowed@1`, or an unknown
guarantee, is `invalid`.

**[INF-A4] Feature gates.** A model server lacking a feature the request
uses refuses it before spend: `tools` needs
`abstraction.inference/tools@1`, `options.json_schema` needs
`abstraction.inference/json-schema@1`, and an `image` part needs
`abstraction.inference/vision@1` and a content reader. A key in
`required_extensions` the model server does not know is refused with
reason `extension:<key>`, and a key listed there but absent from
`extensions` is `invalid`. For a selected native provider or remote runtime,
the receiving runtime reads the operator-managed extension inventory of that
exact registration generation and profile at step 3 of INF-A2. An absent key
refuses before provider activation. A declared key and its value and required
status reach the selected service, which still validates the value and
model-specific support before accepting work. The reserved caller-claim value
is always derived from the bound original subject when forwarding.
A leaf provider over its own OpenAI-compatible image engine declares the
image extensions it carries and maps their values to request fields
(`Config.ImageFields`), such as the Lemonade provider's `lemonade/seed` to
the engine's integer `seed`. Only declared keys reach the mapping. A value it
refuses is `invalid` with reason `extension:<key>`, a field the image request
already sets is `invalid` with reason `extension:fields`, and both end the
call before the engine sees it.
Extension keys under `openabstractions/` are reserved for OA provenance. A
receiving service removes them from the request after validation and before
any adapter, engine or forwarder sees it; vendor keys and their required
status pass unchanged. A request that lists a reserved key in
`required_extensions` is refused with reason `extension:<key>`. The service
records `openabstractions/claimed-program` as `claim` only from a remote
runtime or from a runtime a leaf provider designates; a claim from any other
caller is discarded.

**[INF-A5] Provider-side admission hooks.** A provider whose own program
has work to do on the picked model server before a call is accepted
declares `Config.Admit`, run for the bounded `Start` operations (chat,
transcription, speech, image and live) and `Embed` after
argument, feature, rights, content, output-write, budget, capacity and
credential preflight applicable to that profile, and by the job executor
for every durable job before any call reaches the job's server, on a first
submission and on work recovered after a restart alike. A job whose hook
answers anything but `not_permitted` waits with the hook's word as its
waiting word (an on-demand provider still starting reads
`activating:<name>`) and is asked again at the next sweep; `not_permitted`
fails it with `admission:not_permitted`. The hook is where a declared
on-demand provider starts (facade CONTRACT.md FAC-R8), so it starts for a job
as for a call. The hook receives the
picked server, the model and, through `AdmissionProfile(ctx)`, the
request's profile, so a hook can refuse a model its server declares for
another profile only (`abstraction-router` CONTRACT.md ROUTE-D9). A false answer refuses the request
`unavailable` with the hook's own reason word, or `not_permitted` when
that word is exactly `not_permitted`. A nil hook changes nothing; the
model host declares one to load its object before execution and keep its
own refusal words (`abstraction-provider-modelhost` CONTRACT.md MODELHOST-H2).

**[INF-A6] Admitted work lives only while its rules do.** INF-A2 decides
`abstraction.inference/complete` and the credential's use once, at `Start`.
A running operation of every profile and a running durable job MUST be
decided again, asking nobody, for `complete` on its model server and, when
this runtime applied a credential for it, for
`abstraction.credentials/apply` on that credential (`Config.Standing`;
abstraction-rights RIGHTS-R23):

- before each live `Append` and `Commit`: a refusal ends the session and the
  input reads `closed`; a decision that cannot be obtained sends nothing and
  the input reads `unavailable`;
- before each poll of a durable job's upstream work: a refusal cancels that
  work through the backend's `Cancel` with the plan's headers, which the
  credential audit records when the credential's use was the refusal
  (`Config.CancelledWith`, credentials CRED-A7), and fails the job with the refusal as its reason; a decision that cannot be obtained
  sends nothing, and the job waits as `rights:unavailable` until a sweep
  plans it again (INF-G9);
- on each committed rights change, before the edit returns, and every five
  seconds on the provider's own clock ([`Provider.Recheck`](https://openabstractions.github.io/reference.html#concept-inference.chat-provider)): each operation
  a rule refuses is stopped, and a decision that cannot be obtained stops
  nothing there.

The additive Go `Provider.RecheckChecked` reports unavailable checks to
checked rights-change composition while preserving those operations. The
published void `Recheck` remains compatible and its periodic outage behavior
is unchanged; cancellation supplies no withdrawal.

A stopped operation ends `cancelled` with the refusal as its reason:
`rights:<word>` for `complete`, `credential:not_permitted` for the
credential. An operation delegated to a remote runtime is decided here on
`complete` for its server, and ending it closes the remote call; that runtime
decides its own credentials. An operation a remote peer started is decided
under the subject its certificate mapped to, so revoking that subject's rule
ends it the same way. The caller's next `Start` is decided, and asked, as a
first one is.

## Image input

**[INF-V1] Digest references.** `vision@1` accepts image parts in user
messages. Each names a canonical lowercase `sha256:` digest and one of
`image/png`, `image/jpeg`, `image/gif` or `image/webp`. The receiving
service reads content under the original bound caller's authority after
the model server's `complete` decision. Storage checks permission before
lookup and while reading. A digest grants no permission, and a grant for
one content action grants nothing under the other: a write grant that
stored the bytes does not authorize reading them back. Request input written
under an input profile is read back under that profile (INF-V4). Authorized missing
content is `invalid` with `content:unknown`; a denied read is `forbidden`
with `content:read:forbidden`; a denied write (the gateway's own storage of
uploaded or generated bytes) is `forbidden` with `content:write:forbidden`;
an unavailable reader is `unavailable` with `content:unavailable`. A denied
write of content a provider generated (`speech@1`, `image@1` or `live@1`
output) is `forbidden` with `output:forbidden`.

**[INF-V2] Verification and bounds.** Before applying credentials or
starting the upstream call, the service verifies the complete bytes
against the digest and checks the detected media type. A mismatch is
`invalid` with `content:digest` or `content:media-type`. The hosted
server's own limits bound each image and the total image payload,
counting every occurrence even when a digest is resolved once. Exceeding
either limit is `invalid` with `content:too-large`. Text and image order
is preserved in the upstream request, including image-only messages.

**[INF-V3] Delegation.** OA calls carry digest references. The upstream
adapter encodes bytes into its vendor format. A remote OA runtime receives
the original references and resolves them in its own authorized store;
delegation does not transfer content. Its admission refusal ends the
delegated request with the existing typed remote refusal.

**[INF-V4] Request input profiles.** Content an application uploads as the
input of a request (an edit input or mask, a vision image, a video's first
frame, audio to transcribe) is written and read under one of two bounded
input profiles: `input:image` for PNG, JPEG, GIF or WebP, and `input:audio`
for the INF-M5 audio types. The application holds a profile with both
`abstraction.storage/content.write` and `abstraction.storage/content.read`
on its resource. The content resolver reads the input back for a request
that same subject starts, under the profile that request consumes:
`input:image` for an image edit, a vision chat or a video job's first
frame, `input:audio` for transcription (`abstraction-storage` STO-W12). The
profile is asked for together with the capability that consumes it: when
a consuming request is refused because its `complete` decision reads
`not_granted` or its input read is refused, the provider tells the runtime
(`Config.AskInput`), which admits the first-use question on
`content.write` for that profile and that proven program. An allow writes
`content.write` and `content.read` on that profile; a never writes both
denials. No upload refusal asks the question by itself. A durable job, an
`image@1` call and a `speech@1` call each decide every grant they need
before they answer: `complete` on the server, the input profile of an edit,
the output profile (INF-S7). Missing several, the request is refused once
with each word in that order separated by a space (for example
`rights:not_granted content:read:forbidden output:forbidden`), and the
first-use question of each is admitted in the same pass, so a person
answers them together; each grant still needs its own answer. Only a
`complete` that reads `not_granted` lets the others be gathered: a denied
`complete` or an unknown action refuses the request with that word alone,
and nothing more is read or asked. A call
refused on `complete` among others reads `not_permitted`, as one refused on
`complete` alone does; a call refused only on its input or output reads
`forbidden`. A durable job refused at submission reads `forbidden` and the
job service seals nothing, so the same key is admitted once the person
answers (`abstraction-job` JOB-A16); a credential the applier refuses for a
reason other than `not_permitted` reads `invalid`. A later check that cannot
answer or finds the request itself wrong reports the grants already refused
instead, since only their answers can let the request run. A decision word
other than `permitted`, `denied`, `not_granted` or `unknown_action` is
`unavailable`. A request that has
not reached its terminal state names its input, and so does an inference
job; the named input stays in the store until then (STO-T7). A job that has
not ended and cannot be decoded names unknown input, which keeps every
object. A refused upload is `forbidden` with `content:write:forbidden`, and
a refused read `content:read:forbidden`, as in INF-V1. Go:
`InputProfileOf`, `InputResource`, `WithInputProfile`, `InputProfileFrom`,
`Provider.NamedInputs`, `JobExecution.NamedInputs`.

**[INF-V5] Generated output is `tmp`.** Content the runtime publishes as
generated output (`output:image`, `output:speech`, `output:live`,
`output:video`) is storage class `tmp` (`abstraction-storage` STO-T1,
STO-T2). Its lifetime rule holds it while the operation or job that
published it is retained (INF-Q4), while a request or job that has not
reached its terminal state names it as input (STO-T7's holders), and while
an exact-digest write marks it. Afterwards `tmp_max_retention` runs from its
last publication. Removal takes the bytes and the provenance record
together and leaves one STO-T6 record with kind `generated-output`. A later
read is `content:unknown` for a reader an exact-digest rule admits; a reader
that held it only through generated-output provenance reads
`content:forbidden`, as for any digest it never made, so a read never shows
whether the bytes existed. A file
an agent tool saved for the person is the person's and is outside this
rule. Until the storage collector exists, the runtime's input retention
loop runs a generated-output pass at most once an hour
(`backlog/PICTURES-STORAGE.md` ruling 1). Provenance written before that
pass has no time; the first pass writes the current time and removes
nothing.

## Credentials and spend

**[INF-C1] Applied once per outgoing request.** The service applies a
hosted model server's credential per outgoing request: the first request
of a call and every retry or reconnect call `Apply` again. Applied headers
are sent once and appear in no reply, delta, log record, error or state
file.

**[INF-C2] Budget.** A budget belongs to a credential name: tokens per UTC
day, spend per UTC day counted in currency millionths, rounded up for each
reported cost, requests per UTC day,
images per UTC day, audio seconds per UTC day, or characters per UTC day,
each independently optional. The provider enforces every unit a budget
record configures above zero, at admission, before an upstream request is
sent; a zero or absent unit enforces nothing. When active declarations share
a credential, the strictest nonzero limit in each unit MUST apply. Removing
or disabling a declaration MUST remove its limits while preserving usage
counts. Requests count one per ended chat call
and one per embed call. `go/ceiling.go`'s `Exceeded` is the measured
behavior: `go/provider_test.go` admits over a tokens budget,
`go/embed_test.go` over a requests budget, `go/image_test.go` over an
images budget, `go/speech_test.go` over a characters budget, and
`go/transcription_test.go` over an audio-seconds budget, each reading
`budget_exceeded` with its own reason unit. The provider counts usage and
cost from provider replies, keeps the counts across its own restart, and
refuses `budget_exceeded` at admission once a count has reached its
configured budget. A request admitted under the budget runs to its end;
its reply counts.

The same Ceilings owner additionally keeps a per-native-program budget for a
credential in its existing persisted usage state. `operator@1.ProgramBudgets`
reads its settings revision and observed usage under `host.manage`;
`SetProgramBudget` conditionally writes the required `ProgramBudgetChange`
request under that same native operator authorization. An absent budget removes
the configured choice and preserves usage. A present all-zero budget records
the person's explicit No limit choice. Usage updates preserve the settings
revision. The native presenter takes the beneficiary from the stored question's
proven account/program target; upstream text and agent arguments supply none.
Every implemented charged chat version, embed, image, speech, transcription,
live and durable image/video job checks the credential and program budgets at
the same admission owner and accounts only its native bound origin program.
Accepted durable jobs retain their existing once-only accounting and continue
their original operation. No unavailable modality is claimed as budget-covered.
An earlier `operator@1` service's typed `unknown_method` is projected by the native client as
`unsupported`, confirming no configured budget or write.

**[INF-C3] One audit record.** One log record per ended call and per
admission refusal: program, model server, model, family, tokens in, tokens
out, wall time, outcome, reason, credential name and budget state. Records
carry names, never header values or message content. A request a named
selection policy routed also records the preferred model, the host that
served it or the hosts known unavailable for it, and the selection reason;
model server and model name the route that ran. Placement is captured from the
admitted host using the same local, hosted or remote classification as
`ServedBy`. An entry recorded before a host is selected has no placement.

`operator@1 Requests(cursor,max_entries)` reads these decisions from the same
journal as `Audit`, under the receiving account's `audit.read` authority. Its
open `RequestEntry` wraps the unchanged published `AuditEntry` and optionally
carries `preferred_model`, `unavailable_hosts`, `selection` and `placement`.
The unavailable hosts are the recorded comma-separated preferred hosts when
the selection reason is `preferred_route_unavailable`; a successful preferred
host is not an unavailable host. The page shares Audit's sequence, cursor,
gap and retention rules. `max_entries` is 1..256. A page exceeding its 1 MiB
encoded bound or the transport's framed reply bound reads `unavailable` with
no entries, the requested cursor and `at_end` false. It never trims a page.
Journal lines written without placement retain its absence. Panel and MCP read
this same service answer; Panel labels absent placement “Not recorded” and
does not infer historical execution from present caller facts or current settings.

## Modalities

`embed@1` is one bounded call; `transcription@1` accepts one canonical
storage digest and returns ordered, timestamped segment or word deltas
through the shared request lifecycle.

**[INF-M1] Embed admission.** `Embed` admits in `Start`'s order for
modality `embed`, without the feature and capacity steps. The steps are
argument shape, model-server selection, the server's wire, the `complete`
decision on `server:<name>`, the credential's budget, and for a hosted
server `Apply` with consumer `abstraction.inference/embed@1`. No byte
leaves the service before every step passes. A credential scoped only to
`chat@1` reads `not_permitted`, reason `credential:<outcome>:<name>`.
`Embed` reaches an OpenAI-compatible server's embeddings endpoint, a
declared native provider's own `embed@1` over the identity-bound
transport, or a remote runtime's `embed@1`; another wire reads
`unsupported_feature`, reason `wire:<name>`. A native provider receives the
request's vendor extensions and the caller claim of INF-A4.

**[INF-M2] Request and reply shape.** A request carries 1..64 texts of
1..32768 bytes and, optionally, dimensions 1..4096. A completed reply
carries one vector per input in input order, each the base64 of
`dimensions` little-endian float32 values, at most 131072 values in all.
The provider accepts an upstream's number arrays and base64 float32 alike
and returns the same bytes for both. An upstream reply of another count,
length or requested dimension reads `unavailable`.

**[INF-M3] Audit and the gateway route.** Every call leaves one audit
record with modality `embed`, the model server, model, input tokens, wall
time, outcome and reason, and never the texts or vectors. The gateway
serves `POST /v1/embeddings`, returning number arrays or, for
`encoding_format` `base64`, the vectors as sent. A remote server receives
the request unchanged except for the model and the claim extension.

**[INF-M4] Transcription admission.** Admission validates the request,
selects a model server that explicitly serves modality `transcription`,
checks `complete` on `server:<name>` for the original bound caller,
resolves the digest under that same caller's content policy, checks
budgets and capacity, and only then applies a credential and sends bytes
upstream. Unknown content is `invalid`, another caller's content is
`forbidden`, and an unavailable store is `unavailable`. None spends an
upstream request.

**[INF-M5] Input bounds.** Input is at most 16 MiB, its bytes must match
its SHA-256 digest and declared WAV, MP3, MP4, WebM, Ogg or FLAC media
type. whisper.cpp accepts WAV only. References cross OA and remote wires;
bytes exist only in the selected backend request, and a remote runtime
resolves the digest in its own authorized store.

**[INF-M6] Segments and accounting.** Segment text is at most 32768 bytes.
Deltas preserve upstream order and millisecond timestamps; malformed
timestamps or oversized text end `unavailable`. Idle, caller and explicit
cancellation close local or delegated upstream work. Audit and budgets
count the terminal duration in rounded-up audio seconds under modality
`transcription`, an enforced unit under INF-C2.

**[INF-M7] Native and engine transcription.** A model server on the
native transport receives the request with the selected model, no
credential, the local-only guarantee and the claim extension of INF-A4.
Before `Start` the runtime stages the caller's audio, resolved and checked
under INF-M4 and INF-M5, at the provider's `content-writer@1` by its
digest, and relays the provider's segments under INF-M6; a lost `Start`
reply is never retried. A provider that composes an explicit transcription
engine for its own host (`providerhost.NewEngine`) runs the engine in place
of an HTTP adapter: the engine names an unsupported feature before rights,
content and spend (`unsupported_feature`, its reason), and each segment it
emits is checked as an upstream's.

## Preload

The first request of a conversation on a local model waits for its whole
prompt to be evaluated. OpenCode's Local assistant's first answer on
Qwen3.6-35B-A3B took 10.6-12 s with its about 3,200-token request, and 3.6 s
with the same request's fixed prefix evaluated ahead
(`research/live-runs/2026-10-02-demo-model-opencode.md`, "Results" and
"Pre-warm"). `preload@1` lets an application have its own prefix evaluated
before it sends that request.

**[INF-PL1] The request is the caller's own prefix.** `Preload` takes a
`chat@1` `Request` whose last message is not a user message: the caller's
next request is these messages and tools followed by its user message. A
last user message reads `invalid`, reason `messages:last_user`; an image
part reads `invalid`, reason `part:image`. The provider and the model
server keep nothing of the request after the call, and nothing one caller
preloaded is evaluated for another.

**[INF-PL2] Admission is Start's, and only a prefix-evaluating server takes
one.** `Preload` admits in `Start`'s order: argument shape, guarantees and
model-server selection for modality `chat`, server support, the `complete`
decision on `server:<name>` for the original bound caller, then the
provider's model load (INF-A5). Speculative preload authorization MUST NOT
admit a first-use question. An ungranted caller receives `not_permitted`
and no engine is loaded. A server takes a preload when the provider
sends to it itself and composes a prefix evaluator for it
(`Config.EvaluatePrefix`), or when it is a declared native provider whose
declaration lists `abstraction.inference/preload@1` beside `chat@1`
(`router.Host.Preloads`); that provider receives the request with the
selected model, its vendor extensions and the caller claim of INF-A4. A
hosted server, a remote runtime and every other server read
`unsupported_feature`, reason `preload`. The evaluator receives the engine
request the chat path makes of the prefix (`openAIBody`), so the server
renders the bytes the following `chat@1` request renders.

A preload is speculative work and holds the lowest standing, the
coordinator's decision of 2026-10-03 under the open arbitration question
of VISION 2026-09-06 ("automatic prioritisation of one process over
another ... is arbitration"), which it settles for nothing else. The
admission marks itself (`PreloadAdmission`), and a provider's `Admit` loads
for it only without asking another holder to yield; otherwise it answers
`busy`, reason `resources:held` (`ReasonResourcesHeld`), and loads nothing.
A preload reserves no model activity for its load or its evaluation, so no
idle clock or yield waits for it. A provider on the resource service's
leases loads through the holder's speculative ensure (resource RES-L6).

**[INF-PL3] The reply says what the server did.** `evaluated` carries the
tokens evaluated; `loaded` means the model is loaded and nothing was
evaluated, because the prefix is shorter than the provider's minimum; `busy`
means other work comes first and nothing was evaluated: the server served
another request since it loaded or during the evaluation (`engine:used`),
another preload runs there (`preload:running`), or loading the model would
ask another holder to yield (`resources:held`); `refused`
carries the server's refusal of the prefix. Every refusal carries the
reason words of `Start`. The call returns when the server has evaluated the
prefix, refused it, or the service's call budget of 35 s ends; a load
longer than that budget reads `unavailable`, reason `caller`, and leaves the
model loaded under its lease.

**[INF-PL4] What the shared server shows.** A preload widens nothing: a
caller can have only its own text evaluated, and a busy server evaluates
nothing. The limit that exists without it remains: a model server's prompt
cache is one per server, and a caller can read from its own request's
`prompt_tokens_details.cached_tokens` how many leading tokens of its prompt
the server already held, whoever sent them. Every audit record of a preload
has modality `preload`, the model server, model, the tokens evaluated,
wall time, outcome and reason, and never the messages or tools.

## Requests

**[INF-Q1] Identity and visibility.** A request is identified by its
opaque `operation` id within the provider epoch, and is visible only to
the account and program that started it. Any other caller reads `unknown`
for `Observe` and `Cancel`.

**[INF-Q2] [Delta](https://openabstractions.github.io/reference.html#concept-inference.operation-delta) paging.** Deltas are numbered from 0 without gaps, and the
last is `end`. `Observe` returns deltas from `cursor`; the newest
[`Admission.retained_deltas`](https://openabstractions.github.io/reference.html#concept-inference.operation-admission) deltas are readable, and an older cursor reads
`gap` with `next` at the oldest retained sequence. `max_bytes` bounds the
compact JSON encoding of the returned deltas. `at_end` is true when the
page holds the end delta or `cursor` is already past it.

**[INF-Q3] Idle cancellation.** A request with no `Observe` in flight and
none received for `Admission.idle_ms` is cancelled with reason `idle`, and
its upstream request is closed within one second after that. A caller
that disconnects in the middle of an `Observe` stops that wait at once, and
its request is cancelled `idle_ms` later. A request whose last `Observe`
returned its page before the end to a connected caller waits five minutes,
or `idle_ms` when that is longer, and stays observable at least as long
after it ends. That caller holds the request as a handle: an agent tool
answers "working" with it and resumes after the agent's turn. A live
session keeps `idle_ms`. An image operation a durable job owns has the
longer `idle_ms` of INF-G5.

**[INF-Q4] Retention.** An ended request stays observable for
`Admission.retention_ms`, then reads `unknown`. `Cancel` of an ended
request reads `ended` with its reply outcome and changes nothing.

**[INF-Q5] No request identity for Start.** A retry of `Start` is a new
request. The contract offers no idempotency key for `Start`: a lost
`Admission` leaves a request that idles out within `idle_ms` unobserved,
at most one request's spend.

**[INF-Q6] Clients.** Each language's client offers `complete()`, which
starts a request, observes it to its end and folds the deltas into a
`Reply`, and `stream()`, which yields deltas in the language's native
iteration form and cancels the request when the iteration is abandoned. A
`Start` refusal is a `Reply` with that outcome and no deltas. Clients
never retry `Start`.

For unpublished `chat@3`, the five language helpers fold typed events into
ordered blocks and the terminal `ChatResult`. Progress stays separate from
content. Answer text includes only text blocks; reasoning, reasoning summaries,
tool calls and model refusals keep their own kinds. Callers explicitly choose
whether to retain public reasoning text. Opaque seal bytes, origins and whole-seal
omission observations remain unchanged. Usage preserves unreported counts and
reported zero. A completed `ChatResult` with an unknown finish reason displays `other`
and implies no action; raw pages retain the original word.

The caller persists the idempotency key (`RequestIdentity`) and history epoch before an
explicit Start. Open sends Start once and retains its admission. Resume accepts
an existing request handle and cursor without sending Start. Session observation
and completion preserve that handle, cursor and fold on a wait deadline,
disconnect, page refusal or gap. Cancellation is explicit. A resumed fold
contains output observed from its supplied cursor; callers retain the preceding
fold when they need the complete local transcript. An actual terminal may leave
partial blocks open; the fold preserves explicit block ends and synthesizes no
closure. A suffix may start with a block end whose content preceded its cursor.
Only a received terminal event completes a fold. These helpers create no listener
or advertisement.

| chat@3 client check | Existing owning checks |
| --- | --- |
| Typed progress/content separation, stable block identities and explicit closures, reasoning retention, seals/omissions, usage, finish and served-by | Go `TestChat3ManagedFold`, JavaScript client tests, Python `ManagedChat3.test_fold`, Rust `managed_tests`, C++ managed consumer, using the same `chat3_managed.jsonl` corpus |
| Retained request/cursor after wait errors and gap; explicit cancellation; unknown admission sends once | Go managed session tests and the same JavaScript/Python/Rust/C++ helper checks |
| Preserve the policy-mode inline local-only narrowing restriction without claiming Facade service scope protects model placement | Native `TestChat3PolicyLocalOnly*` and generated five-language request codecs; authorization and candidate intersection stay server-owned |

**[INF-Q7] Context overflow.** A model server that refuses a request as
larger than its context window ends it `refused` with reason
`upstream:context_length_exceeded:<window>:<input>`, the word OpenAI,
Lemonade and OpenCode already use. This applies to an HTTP error answer
and to an error event inside a stream. The server's error code
(`context_length_exceeded`, `exceed_context_size_error`,
`model_context_window_exceeded`) or its overflow wording identifies the
refusal; a rate or quota message never does, and a 429 keeps its status
reason. `<window>` is the per-request context in tokens the server stated
with the refusal. When it stated none, `<window>` is the router's latest
effective-context observation (router `Alias.effective_context_length`) for
the model server and model that refused. `<input>` is the counted input
tokens the server stated. Each is a decimal and 0 when unknown. The reply,
its deltas and the audit record carry these two numbers only: the upstream
body, message, prompt text and headers are discarded. Parts already
delivered stay delivered. An engine that exits or closes the stream without
a terminal event remains `unavailable` with `upstream:stream`, a stream
error of another kind remains `unavailable` with `upstream:error`, and an
unreachable server remains `upstream:unreachable`.

**[INF-Q8] Reasoning is never answer text.** A model server's reasoning
trace (`reasoning_content` or `reasoning` on the OpenAI-compatible wire,
`thinking` on the Anthropic wire) is never delivered as a text part and
never sent upstream as message `content`. A reply whose only output was
reasoning, with no text and no tool call, ends `refused` with reason
`upstream:reasoning_only`, keeping its usage. `chat@1` has no reasoning
part; clients drop reasoning they hold rather than send it as text.
Before it ends so, the service asks a model server on this machine once
more, on the same conversation with the same output limit and
`reasoning_effort` `none`, when the caller named no reasoning effort. That
reply's parts follow the first attempt's under new block numbers, the
reply's usage and cost add both attempts, and its outcome and reason are
the reply's: only a second failure ends it failed, under its own reason. A
hosted server is asked once, with no reasoning parameter of the service's
own. Modelhost reads `reasoning_effort` `none`, and an output limit below
twice the family's answer reserve, as thinking off
(`chat_template_kwargs.enable_thinking` false); every other request to a
thinking family gets a bounded reasoning budget, the family's default
effort when it names none.

**[INF-Q9] Reasoning echo.** When an OpenAI-compatible model server
answers a tool call with a reasoning trace, the service keeps that trace
for the caller's account and program, the model server, the model and the
call id, and sends it back byte for byte as `reasoning_content` on the
assistant message carrying that call when a later request of the same
caller returns it to the same server and model. A client sends its history
unchanged and needs no field of its own (`deepseek-reasoner` refuses a
tool-call turn without the trace). A delta that carries both
`reasoning_content` and `reasoning` counts `reasoning_content` once. The
service keeps the latest 1024 traces and at most 32 MiB of them in memory;
a trace it no longer holds, or one longer than that, is not sent.

**[INF-Q10] Reasoning effort (architect ruling 2026-11-12).** `chat@3`
`ChatOptions.reasoning` names an effort (`none`, `low`, `medium`, `high`;
`none` turns thinking off) and an optional `budget`, a token cap on
reasoning that wins over the effort where the model takes one. The service
maps it onto the chosen model's own control, read from the model server's
declaration or the hosted catalogue's entry for the model, and never
refuses a call for it:

- a llama.cpp model behind modelhost gets `reasoning_effort` and, for a
  named budget, `reasoning_budget_tokens`; modelhost turns `none` into
  `chat_template_kwargs.enable_thinking` false and bounds every other effort
  with the family's engine-profile budget, ending the thinking with its
  `reasoning_budget_message`;
- a hosted model whose catalogue entry names `reasoning_effort` gets the
  provider's own word for the effort, and an effort read as another word is
  `approximated`. DeepSeek (API docs read 2026-11-12) takes `none`, `low`,
  `high` and `max`, reads `medium` as `high`, defaults to `high` and has no
  budget;
- an Anthropic Messages model whose catalogue entry names thinking budgets
  gets `thinking.budget_tokens` (`none` disables thinking); a budget that
  cannot fit below the output limit is `approximated`;
- every other model gets no reasoning parameter, and the answer says
  `not_supported`.

`ChatResult.reasoning_applied` reports the effort and budget the answer ran
with and `support` (`applied`, `approximated`, `not_supported`,
`overridden`); `budget_support` reports a named budget. A request naming
nothing reasons at the model's catalogue default: bounded on this machine,
and a hosted server's own default with no parameter sent. A reply that
would end `upstream:reasoning_only` gets the one further attempt of INF-Q8
unless the caller chose reasoning itself.

**[INF-Q11] Native extensions are the pass-through.** A model's own
reasoning switches travel as `ChatContent.extensions`
(`llama.cpp/enable_thinking`, `llama.cpp/reasoning_budget`, a host's
`openai/reasoning_effort` with its own words). One the model takes wins over
`ChatOptions.reasoning`, which then reads `overridden`. An extension the
chosen model does not take is dropped and named in
`ChatResult.dropped_extensions`; one named in `required_extensions` refuses
the call before upstream bytes as `unsupported` with reason
`extension:<name>`. The extensions a model takes are its alias's
`capabilities.extensions` (router ROUTE-D15) and the keys its wire maps for
every model. A model choice, such as DeepSeek's model names, is
`ChatTarget.model` and never an extension.

## Speech

**[INF-S6] Speech delivery.** `speech@1` streams bounded audio chunks and
returns its complete bytes through a typed `Delivery`, using the shared
request lifecycle and original-caller authorization.

**[INF-S7] Output authorization.** Generated output (`speech@1`, `live@1`,
`image@1`, `image@2`, batch image work and a video job's clip) requires both
`abstraction.storage/content.write` and `abstraction.storage/content.read`
on its exact `output:speech`, `output:live`, `output:image` or `output:video`
resource before paid work; a video job writes under `output:video` and an
image batch under `output:image` (`OutputVideo`, `OutputImage`). A
synchronous call or a job refused for want of its output profile admits
the paired first-use question on `content.write` for `output:image`,
`output:speech` or `output:video` and that proven program, as INF-V4 does
for input. The runtime admits it where it authorizes the output write
(`Config.PrepareContentWrite`); a host whose preparer does not ask is told
of a refused job through `Config.AskOutput`. The call or job is refused at
once and succeeds on retry after an allow. An allow writes both rights on
that profile, a never both denials. `output:live` pairs nothing. The read it grants reaches only results that
same subject's own work produced, because a result is read back only by the
subject its provenance names (below). The runtime binds this to the original account and proven program,
media type and byte limit, and rechecks both rights at publication. After a
digest-verified commit, the runtime durably records that subject, digest and
profile before returning the result. A generated result is readable only by
that subject while its profile read right remains permitted; an explicit
digest read denial or policy outage cannot be overridden by the profile.
The reader rechecks on each access. Digest-addressed uploads keep their
existing digest policy and gain no read right from writing. Once result
publication starts, cancellation waits for that publication's outcome.

**[INF-S8] Native and engine speech.** A model server on the native
transport receives the request as INF-M7 states and decides its own
formats; the runtime relays its audio chunks as they arrive, reads the
provider-local delivery from the provider's `content-reader@1`, checks it
against the relayed bytes' digest, size and media type, and publishes it
under INF-S7. A provider that composes an explicit speech engine for its own
host runs it in place of an HTTP adapter: the engine names an unsupported
feature (a format or voice) before output authorization and spend, and its
complete audio must be the requested format by content and within the
delivery bound before it is streamed and published.

## Live

**[INF-L1] Live delivery.** `live@1` accepts ordered PCM input, streams
transcript and audio deltas, and commits one final response, using the
shared request lifecycle and original-caller authorization (INF-S7).

**[INF-L2] Input framing.** `Append` uses mono signed 16-bit little-endian
PCM at 24000 Hz, in even-sized frames of 1..65536 bytes. Retrying the most
recently acknowledged identical frame returns `duplicate` and sends no
bytes upstream. Other repeated or skipped sequences return `out_of_order`.
An uncertain send terminates the session; accounting includes the
attempted frame.

**[INF-L3] Commit.** `Commit` needs at least 100 ms of input (4800 bytes),
closes input and requests one final response. Repeated `Commit` is
idempotent. `Observe` retains its cursor/replay semantics after `Commit`.

**[INF-L4] Wire selection.** The OpenAI Realtime wire is explicitly
selected; ordinary OpenAI-compatible model servers do not acquire live
support automatically. The native service and Go client support live
sessions. Service-to-service forwarding uses the authenticated remote
frame transport and attributes final deliveries to the remote trust
domain; remote credentials, output authorization and accounting remain
owned by that runtime.

**[INF-L5] The gateway route.** The optional `/v1/realtime` WebSocket
route supports one turn, under INF-W8. Unsupported session options and
events are refused. The gateway key and peer authority are rechecked
during the session. Loopback protocol fixtures prove this event mapping.

## Model-server registration

Every model server the runtime reaches is a declaration of role `host` in
`abstraction.facade/registry@1` (facade CONTRACT.md FAC-R6, FAC-R7; D10: the
registry's own role value stays `host` on the wire, and prose reads it as a
model server). Three sources declare one: the operator through `AddHost`, a
product from its own record of where it listens, and the installation from a
declaration file beside the runtime executable. `hosts.json` keeps the
hosted server's own bounds and the `declared` switch; its entries became
declarations at the first start on this build (facade CONTRACT.md FAC-R1). A
model server's budget lives in its own declaration alone, never duplicated
in `hosts.json` (inference-facade review finding 2). Provider, inventory-
source and remote-runtime declarations are governed by the facade registry
(facade CONTRACT.md FAC-R1..FAC-R4, FAC-R8), not by this page; delegation to a
remote runtime stays here, under "Remote runtimes".

**[INF-H1] Product declarations.** A product declaration is read from the
product's own record of where it listens, and nothing else. The record supplies
an endpoint candidate. One generic provider-kit observer promotes live model
servers. On supported loopback endpoints it requires a current same-account
listener program and a listing through the existing process-bound HTTP client.
Every response is checked against that process generation; matching before/after
snapshots alone do not qualify. For known unsupported macOS/IPv6 loopback proof,
a successful live listing may be promoted with presence explicitly unverified
and no program claim. Malformed or foreign endpoints and failures of supported
proof never select that path. Registry readback states the observation's
verified or unverified presence through its existing reason fields.
A successful empty listing qualifies. Failed or unreachable candidates create
no registration. The shared product snapshot expires after 30 seconds; reused
readings retain their original evidence time. Verified readings require current
listener process provenance. Explicit operator registrations
remain listed while offline, and installation declarations retain their own
activation and readiness rules. A product that recorded nothing declares no model server. A documented default address
alone MUST NOT create an installation registration for a third-party product. A malformed record is no
declaration, with a logged reason. No probe opens a connection, scans a
port or listens for multicast. A product's declaration lists
`declared_by` as the product's name and shadows the installation's
declaration of that name; the installation's lists `installation`.
`hosts.json`'s `declared` switch turns the product probes off; removing a
product's or the installation's model server through `RemoveHost` disables
that name (facade CONTRACT.md FAC-R7) and leaves the other model servers as
they are. [README.md](README.md) names each supported product's own record
and documented default address (inference-facade review finding 11: this
rule states the promise, not the product table).

**[INF-H2] Modalities per server.** Every model server carries modalities:
those its declaration's `profile:<name>` resources name, or its wire's
default. A local product's models carry their own modalities from its
metadata where it reports them: LM Studio `type` `llm` and `vlm` serve
`chat` and `embeddings` serves `embed`; Ollama `/api/show` capabilities
`completion` serves `chat`, `embedding` `embed` and `image` `image`.
router@1 `Pick` takes the modality beside the model and picks only a name
its server serves for it; with no servable server serving the modality, or
only servers that serve the model for other modalities, the verdict is
`no-host`. chat@1 picks with modality `chat` and refuses such a request
`no_host` with reason `router:no-host`.

**[INF-H3] Hosts, AddHost and RemoveHost.** `Hosts`, `AddHost` and
`RemoveHost` are fronts over the registry's declarations of role `host`,
and keep their own action, `host.manage` on resource `account`. `Hosts`
reads every enabled model-server declaration in name order, with the
router's survey and each hosted credential's spend today; a declaration
an operator disabled is not a model server and is not listed. `AddHost`
writes one declaration of role `host` declared by `operator`, with the
entry's modalities or its wire's default, and its budget in the
declaration alone. `RemoveHost` withdraws one: the operator's own
declaration is deleted, and a product's or the installation's is disabled
by name (facade CONTRACT.md FAC-R7), which answers `applied` with reason
`disabled`. Both are conditional on the registry's revision, which
`Hosts` reports, and a name no enabled declaration holds reads `unknown`.
A declaration of any other role leaves through the registry's own
`Withdraw`. `Hosts` only reads, and INF-P2 names the edit that writes a
rights rule.

## Gateway keys

**[INF-K2] Minting.** `IssueKey` mints 32 random bytes, holds them as a
credential-manager record of kind `openabstractions/local-key@1`, and
returns the key once. The credential manager never applies that kind to
an outgoing request, and `Store` refuses it. A program holds at most one
active key. The gateway decides a key's consumer per request (INF-W5,
inference-facade review finding 8).

## The gateway

The gateway is the runtime's loopback surface for programs that only know a
base URL and an API key: OpenAI `POST /v1/chat/completions` and `GET
/v1/models`, and Anthropic `POST /v1/messages`, with server-sent events on
both. Where the runtime serves the matching contract it also answers
`POST /v1/embeddings`, `POST /v1/audio/transcriptions`, `POST
/v1/audio/speech`, `POST /v1/images/generations`, `POST /v1/images/edits`
(when content storage is configured) and `GET /v1/realtime`
(`adapters/go/gateway/window.go`). It is the same provider, rights decision and audit as the native
route.

**[INF-W1] Listening.** The gateway listens on IPv4 loopback only, and it
is closed by default. It opens from the gateway setting in the runtime
state (`inference/gateway.json`), which operator@1 `SetGateway` writes and
applies to the running runtime, or for one foreground runtime from
`--gateway 127.0.0.1:<port>`, which leaves the setting unchanged. Closing
it stops the listener and every connection before `SetGateway` replies,
and a runtime restart reopens it from the setting.

**[INF-W2] Peer binding.** Each accepted connection's peer is bound
through the socket-owner table (identity `BindLoopback`, ID-T1..T6) before
a byte is read, and must meet user, process and path at `bound` as an
account of the runtime. The binding runs in the connection's own goroutine
on its first read; one program's verification holds no other connection. A
peer that cannot be bound is answered 403 and the connection closed with
its request unread. On macOS every connection is refused this way. The
binding verifies the peer's code signature as the native pipe binding
does, and each rights decision for the connection carries that peer (asks
ASK-A6). A process's first connection pays one verification, and its later
connections reuse the verdict for five minutes (identity ID-W2). A
keep-alive connection whose verdict is older than five minutes is bound
and verified again before its next request is decided, and must answer for
the same program.

**[INF-W3] Rechecked binding.** A request's binding is rechecked after its
head is read and again after its body, before admission. A connection
that no longer answers for the program that opened it is refused with
reason `binding:peer_moved`.

**[INF-W4] Key verification.** The gateway key (`Authorization: Bearer` or
`x-api-key`) is read before the body. A missing key is 401. A key in the
minted gateway-key spelling (`oalk_` and its tag) is verified against the
credential-manager record of kind `openabstractions/local-key@1` in
constant time: an unknown, damaged or revoked one is 401, and one issued to
another program than the bound one is 403 with reason `key:wrong_program`.
Any other non-empty key passes: desktop clients refuse an empty key field,
and INF-W2 already bound the program from its connection. Refusals set
`Connection: close` and read no body.

**[INF-W5] What a key admits.** A key grants nothing by itself. A request
is admitted through `Start`, `Embed` or a live session for the bound
subject. `abstraction.inference/complete` on the model server and, for a
hosted server, `abstraction.credentials/apply` decide it — with consumer
`abstraction.inference/chat@1` for `/v1/chat/completions` and
`/v1/messages`, `embed@1` for `POST /v1/embeddings`, and `live@1` for `GET
/v1/realtime` (inference-facade review finding 8). A key issued with a
credential name adds `hosted-allowed@1` and that name to every request; a
minted key without one adds `local-only@1`. Any other key adds
`hosted-allowed@1` to chat and messages requests, which then reach a hosted
model server under the credential that server's registration names; the
admission, its audit record and INF-C2's budget carry that name, and
`abstraction.credentials/apply` for the bound program decides each use.
Its other routes add `local-only@1`. A wire request cannot ask for the
credential a server's registration names. `GET /v1/models` lists the model
names of each model server on which the bound program holds `complete`,
and of each hosted server naming a credential on which `complete` reads
`not_granted` while first-use questions are asked, so that a client that
picks its model from the list reaches the call that asks (INF-W10). A
listed name grants nothing.

**[INF-W6] Audit route and rung.** Every gateway refusal before admission,
and every admission refusal and ended request of the gateway, is one
audit entry with route `window` and the rung of the bound peer
(`tcp-loopback/<platform> user=<proof> process=<proof> path=<proof>`, or
`... unbound`). Native entries carry route `native` and the pipe or socket
rung.

**[INF-W7] Status mapping.** `Start` refusals map to HTTP statuses:
`invalid` and `unsupported_feature` 400, `not_permitted` and `forbidden`
403, `no_host` 404, `budget_exceeded` and `exhausted` 429, `refused` 502,
others 503. The error body carries `type`, and `error` with `type`,
`message` and `code` (the outcome word), readable by both wires. A
`refused` reply whose reason is a context overflow (INF-Q7) is the client's
invalid request: status 400, `error.type` `invalid_request_error` and
`code` `context_length_exceeded`. Its message names the window and the
input when each is known and carries nothing else from the reason. A reply
that ends other than `completed` in a stream is an error event, then the
stream's end. A request field chat@1 cannot express (`n` other than 1, an
image part, an unknown response format) is `unsupported_feature` before
admission. A client that disconnects cancels its request.

**[INF-W8] Realtime.** `GET /v1/realtime` is the gateway's Realtime
WebSocket, one `live@1` session per connection. The connection URL names
the model (`/v1/realtime?model=<name>`), a connection that names none is
`invalid`, and `abstraction.inference/complete` on the model server that
model routes to is decided for the bound program before the upgrade
completes, answered with INF-W7's status on an unupgraded connection.
`session.update` selects PCM16 at 24000 Hz with manual turn detection and
audio output, and carries the voice; it may repeat the URL's model and may
not name another one. `input_audio_buffer.append` carries one base64
PCM16 chunk of 1..65536 even bytes to `Append`;
`input_audio_buffer.commit` closes input; `response.create` invokes
native `Commit` and begins the turn. `response.output_audio.delta` and
`response.output_audio_transcript.delta` carry the session's deltas, and
`response.done` the terminal outcome and usage. Every refusal after the
upgrade is one `error` event naming the typed outcome, then the close.
The wait budget is INF-Q3's `idle_ms`, reported by the pre-upgrade
decision: a connection that sends no client event for that long is closed
and its session cancelled. Disconnect, a lost binding, a revoked key and
gateway shutdown each cancel the session.

**[INF-W9] Gateway administration.** `Gateway` and `SetGateway` are
decided on `host.manage` on resource `account`. `SetGateway` is
conditional on the setting revision, the digest of the setting file, and
refuses an address other than `127.0.0.1:<port>` as `invalid` with reason
`address`. A setting written whose gateway cannot listen reads
`unavailable` with reason `listen:<detail>`, and `Gateway` reports the
same reason in `why`.

**[INF-W10] A call held for its question.** A request on any gateway
route, including a refusal at the window's upload store, refused `not_permitted` whose refusal admitted, or found pending, a
first-use question (asks ASK-R2) is held open, with nothing written to the
client, for at most two minutes. Once the person allows and the permit is
committed, the request is admitted again with its binding rechecked
(INF-W3); a refuse, a block, an expired question or the end of the wait
answers the refusal with its reason (INF-W7), naming each question still
pending (INF-W11). A held request is admitted
at most four times, one per question it can raise and the retry after it.
Each refused admission is its own audit record (INF-W6). The decision
point names the questions to the window in-process. A revoke removes the program's rule, so its next call reads
`not_granted`, the question is asked again (asks ASK-R2) and the call is
held on it like a first call; a Block reads `denied` and is answered at
once, asking nothing.

**[INF-W11] A refusal names its pending questions.** A refusal whose
grant words a first-use question can lift (`rights:not_granted`,
`content:read:forbidden`, `content:write:forbidden`, `output:forbidden`,
`credential:not_permitted`)
MUST end with one word `rights:question_pending:<id>` for each question it
admitted or found that is still pending about the bound caller's own
program, in admission order. A question about another program, an answered
or expired one, and a question the caller's route cannot name are left
out, so `rights:not_granted` without such a word means nothing is waiting:
no rule, and the person refused recently or no question could be asked. A
native synchronous admission (`Start`, `Preload`, `Embed`) holds for the
answer at most `ipc_default_call_budget_ms` less 1.5 seconds in all, since a
frame carries no client deadline and every generated client's default call
budget is that constant; an Allow within the hold admits the request again
as INF-W10 does, and the end of the hold answers the refusal with these
words. A longer wait is the caller's: it passes the id to asks
`permission@1` `ObserveQuestion` with its own bound and retries the call
once after an Allow (asks ASK-A14). The words grant nothing; a client that
does not know them reads the grant words before them unchanged.

## Operator

**[INF-P1] Rights actions.** `Hosts`, `AddHost` and `RemoveHost` are
decided on `abstraction.inference/host.manage`, `Keys`, `IssueKey` and
`RevokeKey` on `key.issue`, and `Audit` on `audit.read`, each on resource
`account` for the bound caller. The default installation grants them to
the runtime's operator programs.

**[INF-P2] Model-server edits and their rights side effects.** Edits to
model-server declarations are conditional on the registry's revision
(INF-H3). `AddHost` writes the permit rule `complete` on `server:<name>`
for the operator programs and the caller, and for a hosted server with a
credential the runtime program's `abstraction.credentials/apply` on
`credential:<name>`, with why `inference server add`; an existing rule on a
target is left as it is. A hosted server's own survey runs as the
runtime's operator program, the identity `abstraction.router/router@1`
applies every hosted credential under. That pairing's `apply` rule for
the runtime program is written by whichever edit creates it — `AddHost`
when the credential is already registered, or the credential's own
`Store` when it names an already-declared model server — and `Hosts`
only reads that state, naming the program a missing rule belongs to in
its `why` (inference-facade review finding 7, against the former
contradiction with INF-H3). The router reads the new model servers at
once. The stored server records its modalities (the wire's default when
the request names none: `router.DefaultProfiles`' table for the server's
wire, read through the same construction the router routes through —
every seeded modality for `openai-compatible` and the local kinds, `live`
for `openai-realtime`, `transcription` for `deepgram-prerecorded`,
`speech` for `elevenlabs-stream`, `image` for `stability-v2beta`,
`fal-queue` and `replicate-predictions`, `chat` for `anthropic-messages`
and an owned `<owner>/<name>@<n>` kind) and `declared_by` `operator`; a
request that names `declared_by` is `invalid`. The router picks by model
and modality (INF-H2).

**[INF-P4] Audit journal.** The audit journal keeps the newest 4096
entries across runtime restarts, numbered without gaps. Entries carry
names and counts, never a header value, key or message content.

## Resource actions

**[INF-R1] Closed catalogue.** `resource_actions` is closed: `complete` on
`server:<name>`, and `host.manage`, `key.issue`, `audit.read` and
`selection.manage` and `selection.manage_for` on `account`. The default installation grants none of
them to applications. `selection-operator@2` manages policies for the bound
account and OS-observed program. Chat@2 resolves that same scope
for its bound caller and subjects every selected candidate to the existing
`complete` decision and admission gates. Future actions require an explicit
catalogue revision. `selection-operator@2` uses that self scope when
`target_program` is empty or names the bound program. In that self scope
`Read` needs no grant: a program reads its own policies, the ones chat@2 and
image@2 already apply for it. `Put` and `DeletePolicy` keep
`selection.manage`. Another canonical
absolute executable path requires `selection.manage_for` on the bound
account, for `Read` as for the edits. Installation grants that action only to configured operator programs.
`ReadAccountDefault`, `PutAccountDefault` and `DeleteAccountDefault` require
that same action on the bound native account. They share policy records,
conditional revisions and capacity with program policies. A named policy uses
the explicit program policy first and the account default of the same name only
after proven absence. A generic policy name `oa-<profile>-default`, for one of
the seven modalities, then uses the account default named for its profile
(`chat`, the one Setup writes) after the same proven absence, returned under
the requested name with that record's revision. Store failure, corruption, authorization refusal and an
invalid or unavailable pinned candidate never cause account-default fallback.
Raw `Read`, `Put` and `DeletePolicy` keep their program scopes. Explicit model
requests keep their meaning. Choosing a default grants no host, credential,
budget or execution permission.
The target is a policy namespace; it never supplies the requesting identity.
`ReadEffective(name)` reads only the bound native caller's own effective named
policy with that same grant-free own-read authority. It shares inference's
effective-policy owner: the program policy wins, then the same named account
default applies only on proven absence, then a generic name's profile default.
Store errors and corrupt selected
records return unavailable, without a fallback. The returned policy and revision
describe one store snapshot. Reading performs no activation, reservation,
inference or grant, and proves no future route, provider-pin validity or
execution permission. Raw `Read` and privileged `ReadAccountDefault` retain
their existing scopes and authority.
The service resolves policies under the observed caller program, including for
applications whose program was configured before launch or registration.

Version 2 policies add an optional logical provider declaration name to each
candidate. The service requires that declaration to exist in the current
eligible host generation and satisfy the requested profile and placement.
A missing or stale pin refuses selection without trying a different provider.
The model remains a router family or alias. Adapter installation settings
and provenance own exact binary and model artifact hashes. Pinned and unpinned
policies retain the same stored JSON format and revision checks. The unpublished
`selection-operator@1` profile is retired; current discovery and clients use
`selection-operator@2`.

For a named policy target, `RequestContent.guarantees` and `credential` are
empty. The stored candidate supplies local-only or hosted-allowed intent and,
for hosted placement, the credential name. The service derives one concrete
legacy request for that candidate and independently checks current host rights,
credential application, budget and capacity. Nonempty inline choices in policy
mode are invalid. An explicit model target retains chat@1 guarantee and
credential behavior.

`image@2` uses the same target and scoped policy with image request content.
Its explicit model calls retain image@1 validation and admission. A policy
target leaves inline guarantees and credential empty. Each candidate is
filtered for the image profile before the image@1 admission path. Selection
advances only when a route is proven unavailable before admission. An
ambiguous or attempted provider start, rights refusal, feature refusal,
credential refusal or budget refusal ends that call without fallback.

Both profiles additionally expose `StartWithGuarantees(request, guarantees)`.
The separate argument is empty or exactly `local-only`; other values and
duplicates are invalid. This restriction intersects the request and receiving
endpoint's restrictions. A local-only call with hosted-allowed intent, a
credential, or a remote-only endpoint is invalid before effects. Policy content
still leaves inline guarantees and credential empty. A hosted candidate under
caller local-only refuses before its survey, credential application or admission;
this privacy refusal never authorizes another candidate. Empty extra guarantees
retain `Start` behavior. An unsupported method refuses explicitly and callers
never retry the request through unrestricted `Start`.

Unpublished chat@3 carries that same caller narrowing in its existing
`ChatContent.guarantees`: for a policy target the list is empty or contains
distinct `local_only` and `free_capacity` restrictions; credential remains empty.
Duplicates, hosted-allowed and every other inline policy guarantee are invalid. The service
strips this caller restriction from `chat@2` policy content and intersects it
through the existing `callerGuarantees` / `StartV2WithGuarantees` owner. A hosted
candidate under local-only refuses before survey, credential or admission and
never falls back to another candidate. Facade scope selects the service's
placement and alone proves nothing about model placement. Stored policy and
live authorization retain their authority. Chat@1/@2 receiving is unchanged.

Chat@3 `free_capacity` (`abstraction.inference/free-capacity@1`) requires the
serving provider to acquire only currently free capacity through RES-L6
`AcquireFree`, asking no holder to yield. A held answer refuses admission as
`unavailable`, reason `resource:held`, before an engine starts. The same
restriction survives accepted execution preparation, a residency loss and
native or remote forwarding. A direct provider without enforcement answers
`unsupported` before engine or inference execution bytes. Existing read-only
inventory surveys remain discovery. A relay retains the guarantee for its
serving leaf. No published `RequestGuarantee` member or fallback grants this meaning.

## Remote runtimes

`chat@1` names no base URL, header, key, endpoint or local path. A request
carries a credential *name*, the request id is opaque, and `Reply.host` is a
display name. A local runtime can therefore delegate a request unchanged to
another runtime holding the credential through the mutual-TLS remote
transport: the remote binds its own client certificate subject, applies the
name from its own credential manager under its own rights, and the local
service relays deltas and records the delegation as an attributed claim. No
applied header or local pipe proof crosses the network.

A remote runtime is declared in `abstraction.facade/registry@1` (facade
FAC-R4). A chat@1 request the router picks for it is delegated: the request
goes unchanged except for the chosen model name and the extension
`openabstractions/claimed-program` set to the file base name of the bound
program as its display name. Remote chat and embed carry no local program path;
native designated-leaf provenance retains its bound program path. Its deltas are
relayed, and cancelling it cancels the remote request. Local budgets and the
local applier are not used. The receiving runtime maps the admitted client
leaf key through shared identity to `remote:<SPKI SHA-256 hex>`, decides and
applies for that program under its configured receiving account, and records
route `remote`, `domain` the display label and `claim` the claimed display name.
The display values grant no authority. The delegating runtime records
`domain` the remote host. The receiving runtime answers endpoint@1
`Describe` over the remote transport with chat@1, embed@1,
transcription@1, speech@1, image@1, live@1 and, when a required receiving router
policy is configured, router@1. That policy decides each router action through
native `DecideFor` for the canonical remote subject before reading or routing
(`go/service/remote.go`).

The gateway is a loopback surface of one machine and never a remote one: a
peer on another machine has no socket-owner record here, and INF-W1 keeps
the listener on 127.0.0.1. A gateway key minted on one runtime means nothing
to another. The general listener offers chat, embed and router views under
RUN-R9; its selected services remain separate from the retained published remote
handler's modality roster. Incoming execution is leaf: eligible execution
stays at the receiving model server, with no further OA inference hop. Held
by: `go/service/peer_test.go`
(`TestRemoteServicesKeepsSelectedAndLegacySetsSeparate`) and
`serve/runtime_remote_test.go` (`TestRemoteRuntimeMarksProviderExecutionLeaf`).

An unanswered remote execution submission ends `unavailable` with reason
`remote:uncertain` when the application-frame write was attempted or delivery
is unknown. This includes a lost or undecodable Start reply and a local
cancellation while that reply is unknown. Positive transport proof that the
request was never sent keeps `remote:unreachable`; an authoritative service
refusal retains its outcome and a complete transport caller-proof refusal
keeps its existing error type rather than uncertainty. This applies to chat, image,
transcription, speech, live and embedding delegation. `remote:uncertain`
attributes uncertainty to the OA remote hop; it grants neither
`recoverable-upstream@1` nor permission to retry or choose another route.
Selection may still fall back on a known-unavailable listing before dispatch.
Cancelling an uncertain submission reports `ended` with `reply_outcome`
`unavailable`, preserving its uncertain outcome rather than claiming that the
remote execution was cancelled.

## Engine files

The requirements are owned by the [canonical owner](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#authenticated-program-files).
Historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-F1] Full hash. See the [canonical requirement](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#authenticated-program-files).
- Forwarding citation [INF-F2] Metadata check. See the [canonical requirement](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#authenticated-program-files).
- Forwarding citation [INF-F3] Read-only folders. See the [canonical requirement](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#authenticated-program-files).
- Forwarding citation [INF-F4] Held files. See the [canonical requirement](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#authenticated-program-files).
- Forwarding citation [INF-F5] What is guaranteed. See the [canonical requirement](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#authenticated-program-files).
- Forwarding citation [INF-F6] Install from a pinned archive. See the [canonical requirement](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#authenticated-program-files).
- Forwarding citation [INF-F7] Files proved where they lie. See the [canonical requirement](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#authenticated-program-files).

## Model memory

The requirements are owned by the [canonical owner](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
Historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-MS1] Fill without a file. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
- Forwarding citation [INF-MS2] Proof. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
- Forwarding citation [INF-MS3] Read-only. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
- Forwarding citation [INF-MS4] Loan. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
- Forwarding citation [INF-MS5] Free. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
- Forwarding citation [INF-MS6] Platforms and engines. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
- Forwarding citation [INF-MS7] Span record. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
- Forwarding citation [INF-MS8] Fill through a decoder. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).
- Forwarding citation [INF-MS9] Fill from a storage reference. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-memory).

## Engine launch

The requirements are owned by the [canonical owner](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#generic-child-environment).
Historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-EL1] Clean environment. See the [canonical requirement](https://github.com/openabstractions/abstraction-facade/blob/main/CONTRACT.md#generic-child-environment).

The requirements are owned by the [canonical owner](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#engine-launch).
Historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-EL2] No network socket, and the socket's server is the engine. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#engine-launch).
- Forwarding citation [INF-EL3] The GPU library path gate. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#engine-launch).

## Model packs

The requirements are owned by the [canonical owner](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
Historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-MP1] Pack index. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP2] Choice per unit. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP3] The `exp-huffman-v1` unit. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP4] Decoding into memory. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP5] Built and not built. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP6] The OA model archive. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP7] Fill from an archive. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP8] The `exp-fixed3-v1` unit. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP9] The pack command. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP10] A device beside the CPU. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP11] The `exp-ans-v1` unit. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).
- Forwarding citation [INF-MP12] Delta units against a base. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#model-packs).

## llama.cpp

The provider-binding requirements are owned by the [provider kit](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#llamacpp).
These historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-LC1] Forbidden options. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#llamacpp).
- Forwarding citation [INF-LC2] Forbidden environment names. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#llamacpp).
- Forwarding citation [INF-LC3] Chat media. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#llamacpp).

## Video generation

Video is durable work: job kind `inference` with profile `video`
(`job.thrift`), executed by the runtime's job executor. The engine lives
behind a native provider, which serves `video@1` to its designated runtime;
the runtime holds the provider's operation id and nothing of the engine.
`INF-G2` and `INF-G4` stay reserved ([Next](#next-video-generation)).

**[INF-G1] A video request.** A video job carries an image-shaped request
with `count` 1 and `duration_ms` 1..600000: `generate` makes the clip from
the prompt, and `edit` names one authorized PNG, JPEG or WebP initial frame
and carries no mask. The request names a model, whose preset the provider
applies (INF-G8). `chat@1`, `image@1` and `image@2` carry no video.

**[INF-G3] A video result is one local delivery.** The executor copies the
provider's result into the caller's authorized content store and returns one
`Delivery` with location `local`, its SHA-256 digest and size, and media type
`video/mp4`, `video/webm` or `video/avi`. These are the names Go's `net/http`
sniffer gives the bytes, and the declared type MUST equal the sniffed one; a
provider's `video/x-msvideo` is read as `video/avi`. The result is at most the
operation output bound (`MaxImageOutputBytes`, 64 MiB by default) and carries
any audio track inside its container. The clip of an `edit` video job starts
from its one image, which is the first frame (INF-G1). A video job refuses an
image result and an image batch refuses a video result, each failing as
`upstream:output`.

**[INF-G5] A native provider serves video through `video@1`.** A provider
declares router profile `video`, with its models as `video:<model>` entries
(router CONTRACT.md ROUTE-D9), and serves `Video` (`Start`, `Observe`,
`Cancel`). The runtime offers no `video@1` endpoint to applications; they
submit video as a job. The executor tries its backends in order and
takes the first whose profile selects a host: a video job for a model a
native video provider lists goes to that provider, and any other video job or
image batch goes to an image host as before. An image batch on a native image
provider runs through its `image@1` service: the executor stages an edit's
image and mask, sends `Start` with the job's model, prompt, size and count,
reads the operation's deltas from the start on each poll, reports the latest
progress percent as progress of 100, and copies the end's deliveries into the
caller's store under INF-S7. That `Start` carries the reserved extension
`openabstractions/job`, set to the job's operation id. A provider honours it
from its designated runtime or a mapped remote runtime and discards it from
any other caller. It gives the operation an `idle_ms` of 10 minutes, and keeps
the operation observable for at least 10 minutes after it ends; the
admission reports both. A runtime restart shorter than 10 minutes finds the
operation running or ended, and the job completes. A runtime that never
returns leaves the engine busy for at most 10 minutes. A runtime removes every
reserved key from a request it forwards before setting its own. A repeated
`Start` with the same caller and job mark returns the existing unended
operation. `image@1` carries no `request_id`: a lost `Start` reply to a
provider on this machine is sent again and reaches that operation, and one to
a hosted native server stays `upstream:uncertain`. Admission decides
`abstraction.inference/complete` on `host:<name>`, as image work does. The
runtime forwards model, prompt, size, `duration_ms`, the caller's claimed
program and `request_id`, the job's operation id; it stages an initial frame
with the provider's `content-writer@1` before `Start`, and reads the result
with its `content-reader@1`. A provider MUST treat a `Start` repeating a
`request_id` it holds as the same operation: it starts nothing, ignores the
request's frame and returns that operation's admission. For any other
`Start` that names an `image_digest`, a provider MUST refuse the request as
`invalid` with reason `content:unknown` when the digest is not in its
content store, and MUST NOT generate the clip from the prompt alone. A
provider MUST hold
each `request_id` and the operation it names until at least 24 hours after
that operation ends; a restart drops them (INF-G7). A provider that refuses
`Start` ran nothing, and the job fails at once as `native:<outcome>:<reason>`.
A provider that answers `Observe` with `forbidden` ends the job as
`native:forbidden`. The service host serves `video@1` beside the provider's
other profiles and content services when the provider assigns a
`VideoServer`; it binds each call's peer as it binds chat and answers
`forbidden` itself for a caller other than the designated runtime, and for
every caller on an endpoint that designates none.

**[INF-G6] [Progress](https://openabstractions.github.io/reference.html#concept-job.progress) reaches the job snapshot.** While the provider generates,
`OperationSnapshot.progress` is its step of steps; a provider that knows only
a fraction reports it in its own steps. The runtime reads a provider that
reports steps under way every second, and queued or silent work every five
seconds, since `video@1`'s `Observe` has no wait for a change. For a queued
observation, the snapshot's waiting word (JOB-A15) is `queue:<n>`, `1` for
next, or `queued` when the position is unknown. The runtime reports its own
on-demand provider activation as `activating:<name>`. The gateway can show
that the video engine is being prepared during activation and that the work
is waiting for the engine once queued. The published observation reports
queue position or progress in steps. A job this runtime holds back because its
server is busy with the runtime's other jobs shows
`host:busy` before it is sent (INF-G9). A generating or ended job carries no
waiting word. A completed job reads progress 1 of 1.

**[INF-G7] A lost video is sent again once; a lost `Start` reply is
reconciled.** A provider's video operations live in its memory. When
`Observe` answers `unknown` for an operation the job recorded, because the
provider or its engine restarted, the executor sends the work again from the
start with the same `request_id`, once, for a job that spends no credential.
Its progress starts again from zero, its waiting word is
`provider:restarting` until the provider answers, and until the job ends its
snapshot carries the retryable last-attempt failure `provider:lost`. A second
loss, or a loss of a job that spends a credential, fails the job with
`provider:lost`. A native image operation the provider no longer knows is
handled the same way.

Submission and its reconciliation record `upstream:uncertain`. A `Start`
whose reply was lost, or a first-frame transfer that failed after a call
reached the provider or was interrupted, leaves the job waiting as
`upstream:uncertain` under `recoverable-upstream@1`. Only a refusal made
before any call reached the provider fails a first submission at once. The
executor then sends the same `Start`, with the same `request_id`, until a
provider answers, and records the operation the answer names. It waits 1
second before the first resend and doubles the wait after each unanswered
one, up to 60 seconds; an answer resets the wait. The resend stages the frame
again when the caller can still read it, and otherwise names it unstaged. A
provider that held the `request_id` returns the operation it already admitted
and ignores the frame. A provider that restarted before answering holds
nothing and admits the work once as new, since the earlier attempt ended with
its process. When that provider's store no longer holds the frame either, it
refuses the resend and the job fails as `native:invalid:content:unknown`. A
cancel requested while the job is uncertain is carried out after the next
answer: the executor sends `Start` to learn the operation and then calls
`Cancel` on it. When 24 hours have passed since the first `Start` without an
answer, a provider returning later no longer recognises the `request_id`,
and the executor ends a cancelled job locally as `cancelled` with reason
`upstream:unanswered`.

Observation records `upstream:unavailable`. A `Poll` of a recorded operation
that cannot reach the provider leaves the job waiting as
`upstream:unavailable`, and the executor observes it again.

**[INF-G8] The provider owns the preset.** A video provider applies the
preset it keeps for the requested model: sampling steps, guidance scale, flow
shift, scheduler and decoder. No caller field or extension overrides any of
them. A provider refuses a required extension naming one as
`unsupported_feature` and ignores an optional one. A caller that wants
another preset names another model.

**[INF-G9] Jobs take turns at a model server.** The executor runs each
claimed inference job, video or image batch, in its own task under the
job's lease. It executes at most one job at a time at one local model
server and at most four at one hosted service, each counted by the server's
name; `JobExecution.SetHostConcurrency` changes both limits. A newly
submitted job that finds its server's limit reached waits, claimed and
renewing its lease, in the order it was claimed, and its snapshot's waiting
word is `host:busy` (JOB-A15). A cancel requested while it waits ends it
`cancelled` with nothing sent upstream. A job that gets its turn is planned
again before it is sent: rights, credentials and the server are decided
then, and a refusal at that point ends it and passes the turn on. At
shutdown a waiting or running job lets its lease go with the waiting word
`service:stopped`. A recovered job that may already run upstream counts
against its server at once without waiting, and can take the count past the
limit. A job whose lease renewal has not succeeded within the lease's
duration stops, gives its server turn to the next job and lets its lease
go, and another runtime may then acquire it. Every wait on a renewal ends by
that deadline, at shutdown too; a renewal that hangs in the job store is
left to return in the background, one store call per job at a time. The limit counts this runtime's executions. A job that let its lease go
can still run upstream, a native video provider queues work of its own
(INF-G6), and other runtimes count their own jobs. The budget check (INF-C2)
runs for each attempt, and concurrent hosted jobs can overshoot a daily
budget by the number in flight.

**[INF-G10] A job outlives its server's declaration once.** A job that was
sent records its server's binding. When that server's declaration is
replaced (same name, another binding) the server cannot know the job's
operation, and when it is gone or not servable the job waits for it as
`host:unavailable` for at most 30 minutes, counted from the first time it is
found gone in the current outage; a server that answers again ends the
outage, and a later one counts afresh. Then, or at once for a replacement, a job that spends no
credential is planned afresh once: its record is cleared and marked
replanned, it waits as `host:replaced:replanned` or `host:gone:replanned`
until the next sweep, and it is routed and sent again with its same
`request_id` to whatever server serves its model now, its admission, rights,
input and output decided again. When none serves it, the plan fails it with
the router's own reason. A job already planned afresh whose server changes
again, and a job that spends a credential, whose resend could be charged
twice, fail as `host:replaced` or `host:gone` and are never sent again. A
record written before binding support keeps waiting as `host:unbound`. A local
provider that was not stopped when its declaration changed may still run
the first attempt, which is local compute only; the job delivers the second.

## Image output

**[INF-IO1] An engine's raw pixels become the provider's own PNG.** A
provider whose engine returns raw pixels writes the image with the kit's
the provider kit's `rgbpng`, Go's standard library encoder, never with an encoder
the engine bundles. Pixels are RGB24 rows, top to bottom with no padding, of
1 to 8,192 on each side; a buffer of any other length is refused before
anything is written (`ErrPixels`). The PNG is 8-bit truecolour without
alpha and decodes to exactly the pixels given; its media type is
`image/png`. The provider publishes it through its content store's
`DesignatedContent.OutputWriter`, the one preparer of generated-output
commits every provider the kit composes uses: a designated runtime's
subject only, a bound per profile and media type, an image's media type the
one its bytes sniff as.

## Image edits

An `image@1` or `image@2` edit names one input image by digest and may name
one mask by digest (VISION 2026-10-01, request input and mask transparency).

**[INF-I1] Edit mask.** A mask is a PNG of the input image's pixel size. A
fully transparent pixel, alpha 0, marks the area the model may change; every
other pixel marks the area it keeps, whatever its colour or partial
transparency. Once both inputs are read, inference refuses a mask whose
header names another pixel size than the input image's, `invalid` with reason
`mask` at step 5 of INF-A2. It then decodes the mask under the bound of the
route that sends it: Stability's documented edit limit on its route (each side
at least 64 pixels, 4,096 to 9,437,184 pixels, aspect ratio between 1:2.5 and
2.5:1) and stable-diffusion.cpp's (INF-SD2) on an OpenAI-compatible route and
an image engine. A mask beyond the bound is `unsupported_feature` with reason
`size:engine-bound`; one whose pixels do not decode, or that marks no pixel to
change, is `invalid` with reason `mask`. The native relay compares the sizes
only, and the receiving provider decodes the mask under its own route's bound;
the remote relay leaves every check to the runtime that reads the mask. Each
route that carries a mask has one declared form, converted by the provider
kit's `imagemask` package and tested against this rule: an
OpenAI-compatible edit receives the alpha form (alpha 0 on the area to
change, painted white, and every other pixel opaque black, so a server
behind the route that reads luminance, such as `sd-server`, reads the same
area); Stability's inpaint route receives a grayscale mask, white on the area
to change; the native and remote relays pass the contract mask by digest to
the service that converts it; the gateway's OpenAI-style edit upload is
already this form. A provider that sends a mask on converts it to its
engine's form and may check the size again. An application converts its own
mask form, such as ComfyUI's `MASK` where 1 marks the area to change, into
this one.

## stable-diffusion.cpp

The provider-binding requirements are owned by the [provider kit](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).
These historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-SD1] Engine directive. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).
- Forwarding citation [INF-SD2] Image size. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).
- Forwarding citation [INF-SD3] Request form. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).
- Forwarding citation [INF-SD4] Command line. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).
- Forwarding citation [INF-SD5] Environment. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).
- Forwarding citation [INF-SD6] Video request. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).
- Forwarding citation [INF-SD7] Video result. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).
- Forwarding citation [INF-SD8] First frame. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#stable-diffusioncpp).

## Video encoding

The provider-binding requirements are owned by the [provider kit](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#video-encoding).
These historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-VE1] Raw frames become H.264 in MP4 with the platform's encoder. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#video-encoding).

## whisper.cpp

The provider-binding requirements are owned by the [provider kit](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#whispercpp).
These historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-WC1] Options. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#whispercpp).
- Forwarding citation [INF-WC2] Environment. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#whispercpp).
- Forwarding citation [INF-WC3] The request. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#whispercpp).
- Forwarding citation [INF-WC4] The answer. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#whispercpp).

## Kokoros

The provider-binding requirements are owned by the [provider kit](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#kokoros).
These historical identifiers and this section anchor remain forwarding entrypoints.

- Forwarding citation [INF-KO1] Command line. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#kokoros).
- Forwarding citation [INF-KO2] Environment. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#kokoros).
- Forwarding citation [INF-KO3] The request. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#kokoros).
- Forwarding citation [INF-KO4] The answer and a crash. See the [canonical requirement](https://github.com/openabstractions/abstraction-provider-kit/blob/main/CONTRACT.md#kokoros).

## The model name

`Request.model` is a family or alias string, as the router folds names. A
typed `abstraction.model` `Ref` would make every inference client depend on
the model, download request and storage content definitions. Hosted models
are named by their listing ids, which the router folds into families beside
local names. A later `model_ref` field can carry a `Ref` as a base change.

## Outcomes

| Call | Outcome | Meaning |
|---|---|---|
| `Start`, `Embed`, `Speech`, `Live.Append`/`Commit` (as `StartOutcome`) | `accepted` | Every admission step passed; the request runs (INF-A2, M1, M4). |
| `Start`, `Embed`, live and speech admission | `invalid` | Argument shape or a bound is wrong, or a required-extension key is listed but absent (INF-A2 step 1, A4). |
| `Start`, `Embed` | `no_host` | The router found no model server serving the request's model and modality (INF-A2 step 2, H2); reason `router:<verdict>`, or `engine:unverified:<host>` for a local engine whose required native program evidence is absent. |
| `Start`, `Embed` | `unsupported_feature` | The picked model server lacks a used feature, or (embed) speaks another wire (INF-A2 step 3, A4, M1). |
| `Start`, `Embed`, `Speech`, `Live`, the operator actions | `not_permitted` | The rights decision for `complete`, a credentials `Apply`, or a provider hook read anything but `permitted` (INF-A2 steps 4 and 8, A5). |
| `Start`, `Embed`, `Speech`, `Live`, `Transcribe` and the operator actions | `unavailable` | A decision point, a provider hook, the platform's credential store or an upstream provider could not answer; also a provider's own refusal under `lease:<outcome>`, `engine:none` or `object:unknown:<store>/<id>` (INF-A2). |
| `Start`, `Embed`, `Speech`, `Live` | `budget_exceeded` | A configured budget unit reached its daily count (INF-C2); reason `ceiling:<unit>:<credential>`. |
| `Start` | `exhausted` | The caller's concurrent requests are at the provider's bound (INF-A2 step 7). |
| `Start`, `Observe`, `Cancel` and the gateway | `forbidden` | The peer could not be bound (INF-A1), or a content grant was denied (INF-V1). |
| `Observe` (`PageOutcome`) | `gap` | `cursor` is older than the retained deltas; `next` names the oldest retained sequence (INF-Q2). |
| `Observe`, `Cancel` | `unknown` | No request of that id is visible to the caller (INF-Q1, Q4). |
| `Cancel` (`CancelOutcome`) | `cancelled` | This call stopped the request and closed its upstream call. |
| `Cancel` | `ended` | The request had already ended; `reply_outcome` says how (INF-Q4). |
| `Video.Observe` (`VideoState`) | `queued`, `generating` | The video runs; `queue_position`, or `step` of `steps`, when the engine reports it (INF-G6). |
| `Video.Observe` | `completed`, `failed`, `cancelled` | The video ended; only `completed` carries a delivery (INF-G3). |
| `Video.Observe` | `unknown` | No video of that id is visible, including one a provider or engine restart forgot; the executor sends the work again once, and a second loss fails the job as `provider:lost` (INF-G7). |
| `Reply` (`ReplyOutcome`) | `completed` | The assistant message and `stop_reason` are present. |
| `Reply` | `refused` | The model server itself refused the request; reason `upstream:<status>`, or `upstream:context_length_exceeded:<window>:<input>` for a context-window refusal (INF-Q7), or `upstream:reasoning_only` for a reply whose only output was reasoning (INF-Q8). INF-W7 maps this to HTTP 502, and a context-window refusal to HTTP 400 with code `context_length_exceeded`. |
| `Reply` | `cancelled` | Reason `cancel`, `idle` (INF-Q3) or `caller`. |
| `Live.Append` | `duplicate` | A retry of the most recently acknowledged frame; no bytes sent upstream (INF-L2). |
| `Live.Append` | `out_of_order` | A repeated or skipped sequence other than the last acknowledged one (INF-L2). |
| `AddHost`, `RemoveHost`, `SetGateway` (`EditOutcome`) | `applied` | The declaration or setting changed; the reply carries the new revision. |
| `AddHost`, `RemoveHost`, `SetGateway` | `conflict` | `expected_revision` is stale, or `AddHost` named an existing model server. |
| `AddHost`, `RemoveHost` | `unknown` | `RemoveHost` named a model server no enabled declaration holds (INF-H3). |
| `IssueKey` (`EditOutcome`) | `conflict` | The program already holds an active key; revoke it first (INF-K2). |
| `IssueKey` | `no_secure_store` | The runtime has no platform credential store to hold the key. |
| `RevokeKey` | `unknown` | The program holds no active key. |
| `Hosts`, `Keys`, `Audit` (`ListOutcome`) | `page` | One page of the requested records. |

## Bounds

An image is at most the hosted server's own per-image limit, and a request's
total image payload is at most its total limit, both counting every
occurrence of a repeated digest (INF-V2). Embed accepts 1..64 texts of
1..32768 bytes, dimensions 1..4096, and returns at most 131072 float32
values in all (INF-M2). Transcription input is at most 16 MiB and segment
text at most 32768 bytes (INF-M5, M6). A live input frame is 1..65536 even
bytes; `Commit` needs at least 4800 bytes (100 ms) buffered (INF-L2, L3). A
gateway key is 32 random bytes (INF-K2). The audit journal retains its
newest 4096 entries across restarts (INF-P4), and `Audit`'s page is
1..256 entries. A request id is 1..128 bytes (`Admission.operation`).

## Divergences

- **INF-A2** decides features (step 3) and rights (step 4) before budget
(step 6). A caller with no rule on a model server never learns its budget
state. Several hosted gateways (LiteLLM, Portkey) decide a key's budget
before routing. OA routes and checks rights first: "no byte leaves the
service before every step passes" holds for spend as much as for content,
and a local `Pick` answering `no_host` is cheaper than a
spend check against a server the caller could never reach anyway.
- **INF-V1** reads a denied content grant as `forbidden`. The decided
outcome word for a rights refusal is `not_permitted` (D86), and
`research/vocabulary/RENAME-PLAN.md` step 17 moves INF-V1 to it with
`chat@3` (P-J5b with W2-06, Train F).
- **INF-Q5** offers no idempotency key for `Start`. `ARCHITECTURE.md`
requires one, with reconciliation before fresh selection, after a lost
reply. The optional `idempotency_key` that closes this is planned for
`chat@3` (P-J5b with W2-06, Train F).
- **INF-W8** serves one turn per Realtime connection; OpenAI's own Realtime
API keeps a session across turns. The gateway's one-turn shape matches
`live@1`'s own session model (INF-L1) instead of the upstream wire it
imitates.

## Not built

Automatic content transfer to a remote runtime, and image parts in gateway
chat requests, remain future work: INF-V3 requires the caller's own
authorized store, and the gateway's chat routes refuse image input.
`POST /v1/images/edits` takes uploads when content storage is configured.
`inference.thrift`'s `host_profiles` lacks `video`; that IDL repair is a
TODO row. No typed `abstraction.model`
`Ref` exists on `Request.model` (see "The model name"); a caller names a
family or alias string only. `IssueKey` mints a key for one program at a
time.

### Next: video generation

INF-G1, G3, G5, G6, G7, G8 and G9 bind ([Video generation](#video-generation)).
The rest of the first-run decision's video rules
(`research/first-run/ENGINES.md` §8) are not built:

- **INF-G2** (reserved). A model server of kind `graph` registers with its
base URL and a template directory; each template whose files are present
is a servable family in the router (router CONTRACT.md ROUTE-H2, held in
the placement store).
- **INF-G4** (reserved). Rights action `abstraction.inference/generate` on
`server:<name>`; spend counts `seconds` for video and `images` for image.
Until then a video job decides `abstraction.inference/complete`, and a
hosted credential's ceiling counts one image per video.
- A delivery `path` where the caller's placement is this machine (the
second half of the reserved INF-G3). `Delivery` is released and closed.
- `fps` and `seed` from the caller, until a job document version adds them.
`video@1` carries both, and the runtime sends zero (the preset's rate, the
provider's seed) because the released, closed job `Request` has no field for
them.
- The runtime's declaration of a video provider (`openabstractions models
host` writes chat and embed profiles only). The modelhost provider serves
`video@1` with `sd-server` behind it (INF-SD4..INF-SD8).
