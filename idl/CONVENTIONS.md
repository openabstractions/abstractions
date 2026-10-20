# Conventions every OA contract carries

This page states the wire and behaviour conventions that every OpenAbstractions
service shares: one outcome vocabulary, one refusal record, one versioning
rule, one cancellation and deadline model, one idempotency model, one
observation model, one listing model, one envelope with its actor chain, one
time, evidence and digest grammar, record content as data, declared
capabilities, one code path per platform mechanism, one attention budget, and
Act, tell, Undo. Every page that declares contract rules links here: each
`CONTRACT.md`, job's `SPEC.md` and watch's `README.md`. A service's own normative rule
for a convention lands in that service's next version, with that contract's own
rule id and scenario. The rules on this page bind the definitions in `idl/`,
the generator and those future versions. [LANGUAGE.md](LANGUAGE.md) defines the
definition language the conventions are written in.

## Reading this page

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY" and "OPTIONAL" in this page are to be
interpreted as described in RFC 2119 and RFC 8174, when, and only when, they
appear in all capitals, as shown here.

A rule id such as `CONV-V1` is declared once, in bold brackets before its
title, at the head of the rule it names; tests cite it the same way. A retired
id is never reused. Each rule ends with one of two marks. **Held by** names the
check or scenario that goes red without the rule. **Untestable here** gives the
reason no check on this page can hold it; for a rule that binds a service's
next version, that version's contract carries the rule with its own id and
scenario.

Sections C1 to C14 keep the convention number of the round 4 corpus
(`research/release-0.8.0/round4/FINAL-FORMS.md` section 2), which cites the
sources behind every rule. C15, Act, tell, Undo, is the plan's T13
(`research/release-0.8.0/PLAN.md`), numbered after the corpus. C8, naming, is
the vocabulary decision (`research/vocabulary/DECISION.md`); the
convention-level renames it settles appear under C9.

| letter | meaning |
| --- | --- |
| A | admission: the subject, the envelope and the actor chain (C9) |
| C | content is data (C11) |
| D | deadlines, cancellation and terminal causes (C4) |
| E | error and outcome: the outcome words and the refusal record (C1, C2) |
| I | idempotency and method traits (C5) |
| L | lists (C7) |
| M | one code path per platform mechanism (C13) |
| O | observation (C6) |
| Q | questions and notifications: the attention budget (C14) |
| S | declared support: capabilities and absence (C12) |
| T | time, evidence and digests (C10) |
| U | Act, tell, Undo (C15) |
| V | versions, compatibility and growing vocabularies (C3) |

## Rules

### C1. One outcome vocabulary on two axes

**[CONV-E1] One outcome enum per call.** Every service call MUST return one
outcome enum. A call a policy may gate MUST reserve `forbidden`,
`unavailable` and `invalid`. A conditional write (an `expected_*` argument)
MUST reserve `conflict`. A client decides its next step from the word alone:
fix the request, ask, re-read, retry later, reconcile, or stop. Held by:
`idl/inventory.py` rule R1, tested in `idl/test_inventory.py`; current
violations are the shrink-only backlog in `idl/contract_rules.recorded`.

**[CONV-E2] Absence by provability.** A call addressing a record MUST reserve
`not_found` where the service can prove the record is absent, and `unknown`
where it cannot say whether one existed. An append-only content store and a
registry answer prove absence. An acceptance history past its window, a
private repository the registry does not disclose, and a credential name the
caller may not see cannot. Held by: `idl/inventory.py` rule R1 accepts either
word for a call addressing a record (`idl/test_inventory.py`).

**[CONV-E3] One word per concept.** A service MUST spell each concept below
with its word, and MUST NOT give one word a second meaning:

| concept | word |
| --- | --- |
| malformed request | `invalid` |
| the boundary refused the peer: another account, an unverifiable peer, an operator-only call | `forbidden` |
| a verified subject's rule refused | `not_permitted` |
| the decision point or provider cannot answer now; never a permission | `unavailable` |
| the implementation offers no such operation | `unsupported` |
| stale revision; re-read | `conflict` |
| a rate or capacity bound was reached, with a reason | `exhausted` |
| the addressed record is provably absent | `not_found` |
| no visible record, and the service cannot say whether one existed | `unknown` |
| a handle or upload expired | `expired` |
| a cursor is older than the retained history | `gap` |
| the outcome of a sent call is unknown | the transport's `TIMEOUT`, `DISCONNECTED` or `CANCELLED`, reconciled by identity |
| a server defect | `internal` |

Outcomes split into evaluated answers and failures to evaluate. Rights keeps
`unknown_action` as an evaluated outcome. A credential that needs the person to
sign in or unlock its store answers `needs_person`. Untestable here: a word's
meaning is judged per service when its next version lands.

### C2. One refusal record

**[CONV-E4] A refusal carries the refusal record.** An outcome that ends in a
refusal by rights, credentials, a license, terms, identity or the operating
system MUST carry rights' refusal record beside the outcome word
([RIGHTS-E3](https://github.com/openabstractions/abstraction-rights/blob/main/CONTRACT.md)).
The record's fields:

| field | values |
| --- | --- |
| `source` | `rights`, `credentials`, `license`, `terms`, `identity`, `os`; an open vocabulary |
| `decision` | `denied`, `not_granted`, `person_only` |
| `ask` | `available`, `none` |
| `action` | the exact registered `<owner>/<name>`, when one applies |
| `question` | the pending question's id, when one is pending |
| `credential` | the credential's name, when one applies |
| `retry_after_ms` | the earliest useful retry, omitted when zero |

`ask` tells a client whether asking can change the answer. A person who chose
to stop being asked reads `decision: denied` with `ask: none`. `person_only`
names a question only the person may answer. Every other failure carries a
stable reason and domain beside free message text, and a client parses no
message text. A non-authority `exhausted` or `unavailable` MAY carry
`optional i64 retry_after_ms (omit="absent", range="1..86400000")` beside
its reason; absent means no hint, and it MUST NOT carry the refusal record.
The refusal record's own hint remains for authority refusals that lift with
time. A hint never authorizes retry after acceptance or partial output, or a
duplicate Start. Each service adopts this field in its own window.
Held by, for the record's shape: the generated record's round
trip in Go, Python, JavaScript, Rust and C++ (`scripts/refusal_record_test.py`
in the maintainer source). Current carriage is declared by `config@2` in
[`config.thrift`](../openabstractions-flat/abstraction-config/config.thrift),
messages@1 in [`messages.thrift`](../openabstractions-flat/abstraction-facade/messages.thrift),
registry@3 and endpoint@2 in
[`registration.thrift`](../openabstractions-flat/abstraction-facade/registration.thrift),
and chat@3 in [`inference.thrift`](../openabstractions-flat/abstraction-inference/inference.thrift).
Each adopting service owns its refusing-outcome rule and scenario; this
convention's record-shape test supplies no whole-service carriage proof.

`RefusalDetail` is open with `unknown_fields="grant"`: a reader skips unknown
extra detail members. `RefusalDecision` and `RefusalAsk` remain closed with
`reader="act"`; a reader refuses an unknown `decision` or `ask` word.
`RefusalSource` remains open with `reader="display"`, preserving unknown source
words. Extra display details convey no authority; behavior follows only the
known `decision` and `ask` words. The registered names are settled in vocabulary
DECISION.md section 9.3/J-09. Carriage joins each service's approved successor
window and opens no window by itself.

**[CONV-E5] The refusal record has one owner.** The record MUST be declared
once, in `abstraction-rights/rights.thrift` as `RefusalDetail` with its enums
`RefusalSource`, `RefusalDecision` and `RefusalAsk`. Another definition
includes it. The type is named `RefusalDetail` because generated codecs
already name their decode error `Refusal`. Held by:
`idl/inventory.py` rule R8, tested in `idl/test_inventory.py`.

### C3. One breaking window per service

**[CONV-V1] A published service version keeps its shape.** The version lives
in `wire_name` (`abstraction.x/y@n`) and the package path only. Within one
service version a method MAY be added. A closed struct
(`unknown_fields = "refuse"`) or a closed enum (`unknown = "refuse"`) that the
version reaches MUST NOT gain a member. A published method, struct or enum
MUST NOT lose or change a member. A struct the version reaches MUST keep the
order of its members, since a writer emits them in that order. A published
error code MUST stay declared. Each of these changes is a new service version.
An open struct MAY gain an optional member, an open enum MAY gain a member, and
a service MAY declare a new error code, since `ServiceErrorCode` is open. A
struct or enum no published release carries may still change. Each service
takes its renames and closed-member additions together in one next version.
Held by:
`gen --compat` ([DEF-V1](LANGUAGE.md#def-v1-compatibility-between-a-published-definition-and-a-candidate)),
tested in `idl/gen/compat_test.go`, and `idl/compat_published.py`, which runs it
for every public definition against the private revision `published.tsv`
records, tested in `idl/test_compat_published.py`.

**[CONV-V2] Each service version has a definition hash.** The hash is
`sha256:` and lowercase hex over the version's canonical wire form: its wire
name, encoding, error codes, methods and every declaration they reach. Docs,
comments, language names, the order of declarations and the order of an enum's
members leave it unchanged. The
endpoint description carries it from the endpoint's next version. Held by:
`gen --hash` ([DEF-V2](LANGUAGE.md#def-v2-the-definition-hash)), tested in
`idl/gen/compat_test.go`.

**[CONV-V3] A vocabulary that grows is a catalogue.** A vocabulary that must
grow (finish reasons, waiting words, causes) MUST be declared `catalogue =
"open"` with a `Register*` method, or be an enum read with `reader =
"display"` or `"validate"` that carries unknown words. A cause enum carries
the member `other`. Held by: `idl/inventory.py` rules R3, R4 and R7, tested in
`idl/test_inventory.py`.

**[CONV-V4] Retired names stay retired.** A retired wire name, member name or
rule id MUST NOT be reused. Held by: the contract-tags rule of
`scripts/check.sh`, which fails a rule id a `HISTORY.md` retired; retired wire
names are untestable here until a registry of them exists.

### C4. Cancellation and deadlines

**[CONV-D1] Cancel records intent.** A cancel MUST record intent. A terminal
state that arrives first wins. Stopping a wait MUST leave the work running.
Untestable here: each service's version carries the rule.

**[CONV-D2] A deadline is the remaining budget.** A call MUST carry its
remaining budget as `deadline_ms` in the envelope ([CONV-A2]). A receiver
clamps it to its own limit and passes the remainder downstream. Untestable
here: the envelope arrives with the transport contract's first version.

**[CONV-D3] A cancelled call holds its slot.** A cancelled or closed call MUST
keep its admission slot until its handler returns. Untestable here: the
transport's own rule and its open-close test hold it.

**[CONV-D4] Long work states its time bounds as data.** Job states
`run_deadline_ms` and ends in cause `deadline_exceeded`. Download states a
no-progress deadline and a budget. A resource yield states its `deadline`.
Untestable here: each service's version carries the rule.

**[CONV-D5] One terminal cause.** Every grant or subscription MUST end with
exactly one terminal cause visible to its holder. A lease ends in the
`LeaseEnd` causes `released`, `lapsed`, `revoked`, `yielded`, `lost` or
`platform`. A subscription ends `closed` by its consumer or `failed` by its
source. A job ends `succeeded`, `failed` or `cancelled` with a cause.
Untestable here: each service's version carries the rule.

### C5. Idempotency and method traits

**[CONV-I1] A state change is idempotent or keyed.** A state-changing method
MUST be idempotent by declaration or carry a client-minted key, scoped to the
transport-attested subject and persisted by the client before it sends. An
equal key with an equal body replays the receipt. An equal key with a
different body answers `conflict`. An expired key answers `unknown`.
Untestable here: each service's version carries the rule.

**[CONV-I2] Methods declare their traits.** A method declares `idempotent`,
`readonly`, or `idempotency_key = "<field>"` naming the caller-owned key. The
definition checks MUST refuse a state-changing method that declares none
while `idl/contract_rules.recorded` holds an R10 entry. The generator takes
over that refusal once the list holds no R10 entries. Held by:
`idl/inventory.py` rule R10, tested in `idl/test_inventory.py`, which fails a
method without a trait unless that shrink-only list records it. The generator
also refuses a trait it does not define and a key that names no required string
([DEF-S3](LANGUAGE.md#def-s3-method-traits), tested in `idl/gen/traits_test.go`).

Readonly describes the requested domain effect. Incidental read-audit,
diagnostic recording and cache maintenance are permitted. Model/provider
actuation, registry/policy/grant/budget mutation, accepted work, leases and
operation-lifetime changes are excluded. Per-request logging follows the
safe-method distinction in [RFC 9110 sections 9.2.1–9.2.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.1).
This permits Router.Pick's ROUTE-U1 audit; it does not cover [Chat.Observe](https://openabstractions.github.io/reference.html#concept-inference.chat-service)'s
explicit idle protection. Generated clients continue to retry nothing.

**[CONV-I3] Facades submit durably.** A facade offers a durable submit in
every language that generates, persists and reconciles the key. Untestable
here: it changes no wire, and the job contract carries it.

### C6. Observation

**[CONV-O1] One observe model.** Every observable state MUST be read as a
snapshot with an opaque cursor bound to an epoch. `Observe(after, wait_ms)`
waits at most 30 000 ms and answers `changed` with a new cursor, `unchanged`
with the confirmed cursor, or a refusal. A foreign or expired cursor answers
`gap`, recovered by a fresh snapshot. A source that cannot read keeps its last
good snapshot and reports `unavailable` with its age. A source that can no
longer observe ends once with `failed` and a cause. `closed` is the consumer's
word. The waiter MUST recheck authority after every wake and before it returns
data. Untestable here: each service's version carries the rule.

**[CONV-O2] One waiter and one follower.** Services MUST wait through the one
waiter and follow through the one follower of `abstraction-watch`, and clients
read observations through the generated `(observe = "cursor")` construct.
Every source declares its mechanism: `native`, `polled` with its interval, or
`none`. The generated construct is first received against ConfigObserver in
Go, Python, C++, JavaScript and Rust. The shared follower and remaining service
adoption retain their existing obligations.

Compiler-only observation metadata maps existing input/output cursor fields.
`observe`
names its required string cursor argument; `observe_next` names its required
string reply field and defaults to `next`. The generator validates both
fields and the existing `i64 wait_ms` argument. This mapping reuses published
wire shapes such as ConfigObserver's `cursor` reply field. It preserves the
records, outcomes, method meaning and receiving authority. IPC-O1's `next` describes the continuation
role; the explicit mapping may name an existing field carrying that role.

### C7. Lists

**[CONV-L1] Every list is paged.** A list MUST be cursor-paged with a limit.
Cursors are opaque and bound to an epoch, and `gap` forces a re-list. A list
MUST filter by the caller's own visibility before it cuts the page. A list
takes a typed filter where a consumer filters. An operator listing that spans
programs is its own method under its own rights. Untestable here: each
service's version carries the rule.

### C9. Admission, the envelope and the actor chain

**[CONV-A1] The subject comes from the transport.** The subject of every
decision MUST come from the transport. A payload field never names it.
Untestable here: each service's admission rule holds it.

**[CONV-A2] One closed envelope.** The envelope carries `deadline_ms`, `call`
and `traceparent`, and the transport contract owns it. `call` is at most 64
bytes, is echoed in the reply, grants nothing and is distinct from an
idempotency key. `traceparent` is the W3C trace context, read by job and
logging from the envelope. `call` and `traceparent` MUST stay in the envelope.
Untestable here: the envelope arrives with the transport contract's first
version.

**[CONV-A3] One actor chain.** A service that performs an effect for a caller
MUST decide as that caller. Every downstream OA call for that effect carries
the record `Attribution{subject, actors}`, with the original caller as
`subject` and each relaying service in `actors`. Only the runtime fills
`actors`, from its own bound peers; an application sets none of it. A relay
MUST NOT add its own grants. A field named `on_behalf_of` in resource or rights
is `Attribution.subject`. Prose says "actor chain" and "on behalf of".
Untestable here: `Attribution` arrives with the transport contract's first
version.

### C10. Time, evidence and digests

**[CONV-T1] Two times.** Every attributed record carries the writer's claimed
event time and the receiver's established acceptance time, each in RFC 3339
with six fractional digits and `Z`. Untestable here: each service's version
carries the rule.

**[CONV-T2] Evidence names what was observed.** An evidence grade MUST NOT
state more than the code observed. Untestable here: each owner's truth pass
holds it.

**[CONV-T3] One digest grammar.** A digest is `algorithm ":" lowercase-hex`.
`sha256` is required and `sha512` is the one permitted addition. `gitsha1` is
foreign evidence only. A reader MUST refuse bare hex and uppercase hex before
any lookup or path. The algorithm's identity is the RFC 9530 token
(`sha-256`, `sha-512`), and a `Repr-Digest` header is an early mismatch signal
that decides nothing. Held by, for the refusal of uppercase hex before any
lookup: inference's image admission, which answers `invalid` to an uppercase
digest without resolving it
(`abstraction-inference/go/content_test.go`,
`TestVisionDigestMediaTypeAndBoundsAreCheckedBeforeSpend`). Untestable here,
for bare hex and every other store: each store's reader carries its own rule
in its next version.

### C11. Record content is data

**[CONV-C1] Record content is data.** No service, provider, bridge or reader
MAY evaluate content it carries: no lookup, substitution, template expansion
with external resolution, deserialization into objects, or code loading from
a record field. Relays carry `arguments` and `payload` as bounded opaque
bytes, and only the final dispatcher decodes them. Server-supplied text
(`Describe`, registry records, error messages) is never opened, executed or
dialled. A record carrying `${jndi:ldap://x/a}`, `%n`, `{0}` or `$(id)` reads
back byte-identical. Held by: the `${jndi:...}` round trip of the generated
refusal record in five languages (`scripts/refusal_record_test.py` in the
maintainer source); logging's content scenario holds LOG-R9 for its service.

### C12. Declared capabilities and declared absence

**[CONV-S1] Capabilities are declared data.** What a provider, executor, store
or model can do MUST be declared data checked at admission. An undeclared
capability MUST be refused before spend or effect. A limit is a value
(`unsupported`, `polled`). Untestable here: each service's version carries the
rule.

### C13. One code path per platform mechanism

**[CONV-M1] One caller per platform facility.** Each platform facility MUST
have one caller in OA: the power request in resource, directory notification
in watch, secure-store selection in the credentials provider, supervision and
containment in the facade, and small-file atomic writes in atomic-file.
Untestable here: each owner's drift check holds it.

### C14. The person's attention is one budget

**[CONV-Q1] OA's own questions share one budget per program.** The questions
OA itself raises MUST be coalesced in one budget per program. Identical intent
coalesces, live pending questions and hourly admissions are bounded, and each
partition keeps a per-program reserve. A refusal says through `ask`
([CONV-E4]) whether asking is possible. Untestable here: the questions
service's version carries the rule.

**[CONV-Q2] Notifications follow the platform.** Notifications MUST follow the
platform's Focus and per-app settings. Notification levels belong to the
person per declared category. Popups stay the main channel for asking,
confirming and telling. Untestable here: each presenter's version carries the
rule.

### C15. Act, tell, Undo

**[CONV-U1] A default acts, tells and offers Undo.** A reversible effect a
default performs MUST be told once in Activity with Undo. The service holds
the action, and rights records the Undo as removal of that rule's effect. A
notification is posted only when the person waits on work no open application
shows, when money or data left the device, or when OA stopped work someone
asked for. A progress notification appears only after the requesting
application closed. A notification carries an action the person takes.
Untestable here: each product surface's version carries the rule.

## Divergences

- Rights, Asks, Resource and Router's published @1 error channels retain
  `invalid_request` where [CONV-E3] says `invalid`. Native error descriptors
  and generated observe pagers keep the served version's error-channel word;
  a pager uses `invalid` only when that service declares it in its error codes.
- Today's IPC reply code for an unexpected handler failure is `handler_error`;
  [CONV-E3] spells a server defect `internal` from the transport contract's
  first version.
- Storage spells an expired upload or handle `gap` where [CONV-E3] says
  `expired`, until storage's next version.
- `config@2`, messages@1, registry@3, endpoint@2 and chat@3 carry the shared
  refusal record ([CONV-E4]) in their definitions linked above. Other service
  versions retain their scheduled adoption work and existing refusal channels.
- The generated type of the refusal record is `RefusalDetail` ([CONV-E5]).

The focused gate selects `idl/compat_published.py` for definition, published
baseline and generator changes through `scripts/gate_select.py` ([CONV-V1]).

## Not built

- The closed envelope, `Attribution` and the transport contract that owns them
  ([CONV-A2], [CONV-A3], [CONV-D2]). chat@3 uses the attested native subject
  and the authenticated remote peer with a labelled claimed program.
  Codex 268 (2026-11-02) keeps the envelope as this common transport-version
  prerequisite; chat reserves no actor-chain placeholder and needs no new
  service version when the transport supplies it.
- A safe trait on every state-changing method: missing or explicitly false
  trait debt remains in `idl/contract_rules.recorded` under R10 ([CONV-I2]).
- The remaining waiter and follower adoption obligations ([CONV-O2]).
- The definition hash in the endpoint description ([CONV-V2]).
