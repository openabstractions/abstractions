# Lineage

Where every construct came from, and why we took that one.

Almost nothing here is new. A notation a stranger already reads costs them
nothing to learn, and a citation is enforcement by a reader who already knows
the pattern: a page that says *this is Thrift's annotation syntax* is a claim a
reviewer can check, so a silent departure becomes visible.

**Verified for:** the constructs listed below, as implemented in `gen/` and
exercised by `gen/parse_test.go`, `test/run.ps1` and `test/repeated/run.ps1` on
Go, Python, C++, JavaScript and Rust; and the division of labour between X.680,
X.690 and X.692,
checked against the ITU-T titles and scope statements on 2026-09-08 because the
whole encoding block rested on it. **NOT examined:** every other cited work was
taken from a published specification or from well-documented behaviour of a
widely used implementation, not re-fetched or re-run here, and none of it was
verified against a copy in this repository. Where a claim is *measured* rather
than read, it says so and names what produced it.

---

## The base notation

| construct | ancestor | why this one |
|---|---|---|
| `struct`, `required`/`optional`, field ids | **Apache Thrift IDL** | The file was already in this tree and already cited in every document. Nothing new to learn, and the annotation syntax we need for the additions is Thrift's own. |
| numbered fields | **Thrift**, and **Protobuf**'s field numbers before it, and **ASN.1** tags before that | A number is what survives a rename. All three notations agree, so an adopter reads it without being told. |
| `typedef` | **Thrift**; **C**'s `typedef`; **ASN.1**'s type assignment | Same word, same meaning, three generations. |
| `list<T>`, `map<K,V>` | **Thrift** containers | Same. |
| `(key = "value")` annotations | **Thrift**'s annotation grammar | Every addition below is spelled in it, so the profile adds no punctuation a Thrift reader has to learn. |
| `enum`, `const` | **Thrift** | Same. |

**Why Thrift and not Protobuf.** Both were disqualified as *generators* for the
same reason — their generated code calls a runtime library the adopter must
link. Thrift wins on the syntax question for two reasons that are ours rather
than general: `job/job.thrift` already existed and was already the file people
cited, and Thrift's post-declaration annotation syntax is a natural place to put
per-field policy, where Protobuf's options syntax is heavier and its
`.proto`-file semantics carry a wire format we are not using.

**Why not ASN.1, which solved this first.** DER exists precisely because two
implementations had to produce identical bytes for a signature to verify, and
that is our problem exactly. It fails on availability, not on merit: there is no
generator covering our targets without third-party code, Python has no ASN.1 in
its standard library at all, and an adopter in this space reads Thrift and does
not read ASN.1. **We took its central idea instead — see the encoding block.**

---

## What we forbid, and whose lesson it is

| refused | whose lesson |
|---|---|
| `service`, `exception` | **JSON Schema / OpenAPI**, whose split between shape and behaviour is the one that held when it was tested here: a client generated from a 67-line schema passed 45 behaviour scenarios because every rule stayed in the service. A definition buys the shape and the bytes, never the rules. |
| `union` | **Protobuf 3**'s field-presence rework, which is the same lesson from the other side: one absence mechanism, spelled once. |
| `set<T>` | **CBOR** and **Protobuf** both refuse to promise map or set ordering. We refuse the type rather than promise an order we cannot keep. |
| `double` | **I-JSON (RFC 7493 §2.2)** on interoperable numbers, and **RFC 8785 §3.2.2.3**, which had to specify ECMAScript number formatting exactly because "a JSON number" is not one spelling. We avoid the problem instead of specifying our way through it. |
| `binary` | Ours, and it is a measured lesson rather than a borrowed one. See *the opaque type*. |
| `include` | **JSON Schema**'s `$ref` across documents, which is the feature that makes "which text binds me" unanswerable. |
| default values | **JOSE**, **Protobuf 3** and our own `intent` rule, which all separate *a value a reader supplies* from *a value a writer recorded*. One syntax for both hides the difference. |

---

## The additions, one at a time

### The encoding block — from ASN.1, filled in by JCS and CBOR

    encoding json { escape = ... indent = ... map_keys = ... }

**Ancestor: ASN.1, in two halves, and the halves are not the same standard.**
X.680 defines the abstract syntax and states in its scope that the notation
applies *without constraining in any way how the information is encoded* — so a
module names no encoding, and BER, CER and DER are a separate Recommendation,
X.690, chosen outside the module. That separation is fifty years old and it is
the first half.

The second half is the closer ancestor and it is **X.692, the Encoding Control
Notation**: a notation attached to an ASN.1 specification in which a designer
takes control of the bits rather than inheriting a standard rule set. `encoding
json { ... }` is ECN's move in one line — the encoding named where the types are
named, not chosen by whoever links the runtime.

**Why nobody else puts it in the definition file.** Thrift, Protobuf, Cap'n
Proto, Smithy and FlatBuffers all separate interface, protocol and transport *on
purpose*, so the protocol can be swapped. That is a good architecture and it is
why none of them can carry this contract: they put the encoding in the runtime,
and we put the encoding in the contract, because our conformance proof is byte
equality. A notation whose architecture says *the bytes are the protocol's
business* has nowhere to say *these are the bytes*.

**Avro is the exception, and it sharpens the claim rather than weakening it.**
Avro's specification does define its encodings normatively — a binary one and a
JSON one — so *nobody writes the bytes down* is simply not true. The claim that
survives is finer and is the one worth making: Avro fixes the encoding **in the
specification, once, for all Avro**. Ours is declared **in the definition file**
and may differ from one definition to the next, which is what makes
`escape = "minimal"` → `"ascii"` a one-line change that moves five backends
together.

**The values are filled in from the deterministic-encoding literature**, because
everyone who needed byte equality wrote one down: **RFC 8785 (JSON
Canonicalization Scheme)** for the string production, **RFC 8949 §4.2 (CBOR
deterministic encoding)** for key ordering, **RFC 7493 (I-JSON)** for numbers.
Each is cited on the line it governs below.

**Measured reason this exists at all.** The three encoders this project already
ships produce 832, 815 and 802 bytes for one record — Go escapes `& < >`,
Python escapes every non-ASCII codepoint, C++ escapes neither. Each is correct
JSON, each is correct against every rule anyone had written down, and no
single-language test can see it.

### `escape` — RFC 8785 §3.2.2.2, taken as-is

`"minimal"` is JCS's string production: escape `"` and `\`, use the two-
character forms for `\b \f \n \r \t`, `\u00XX` in lower-case hex for the
remaining C0 controls, everything else raw UTF-8. RFC 8259 §7 permits more; JCS
picks the minimum and so do we.

`"ascii"` names the other thing that actually happens in the wild — Python's
`json.dumps(ensure_ascii=True)` default — so a definition can *declare* it
rather than inherit it from whichever library the binding happened to call.

**Declared divergence from JCS:** we take JCS's string production and not the
rest of JCS. JCS output is compact and its object keys are sorted; ours is
indented and its field order is the definition's declaration order. The reason is
that the record is read and diffed by people, which is why it was indented in
the first place. That reason is a *choice about the record*, not a property of
the record's shape — see the honest note under `indent`.

### `indent` — no ancestor anywhere, and that is worth saying

The nearest things are `json.dumps(indent=)` and `json.MarshalIndent`, which are
library options, not something a definition can carry. Nothing in Thrift,
Protobuf, ASN.1, Cap'n Proto or Smithy has a place for it, because in all of them
framing belongs to the protocol.

**And it is here under protest.** A human-readable indented encoding is a chosen
framing for a machine-to-machine record, and it is what makes key order,
whitespace and a trailing newline part of the agreement at all. The profile
carries it because the record on disk is indented and shipped. It is declared as
a *property of the encoding*, deliberately, so that it reads as a choice about
the bytes and never as part of the definition of a job.

### `map_keys` — CBOR §4.2.1, not JCS

Ascending order of the keys' UTF-8 bytes.

**Declared divergence from RFC 8785 §3.2.3**, which sorts by UTF-16 code units
because JCS is defined in ECMAScript terms. **The measurement that decided it:**
of the five targets, Go, C++, Python and Rust get UTF-8 byte order for nothing —
`std::map`, `BTreeMap` and a byte-wise comparison of Go strings all agree, and
UTF-8 is order-preserving, so bytewise comparison gives codepoint order.
JavaScript is the only one that does not, and its generated code carries an
explicit comparator with the reason on it. Taking JCS's rule would have inverted
the cost: one language free and four carrying a workaround, to canonicalise for
a runtime none of them uses. CBOR's rule is the majority-free one and it is a
published deterministic profile, so it is the one cited.

The negative control was measured before this repository existed and is not
re-run here: replacing that comparator with `Object.keys(m).sort()` changes the
bytes, and a comparison across languages catches it.

### `numbers` — I-JSON, narrowed

**RFC 7493 §2.2** says an interoperable number stays inside IEEE-754 double
range; **JCS §3.2.2.3** goes further and pins the exact ECMAScript spelling.

**Declared divergence: we narrow to integers and drop the float entirely.** The
constraint that forced it is in the layer, not the notation — a fractional byte
offset is refused rather than rounded, because JSON has one number type and a
byte count is not a real number. Having no float in the definition means the
spelling question never arises for anything the definition owns, and JCS's
ECMAScript number production is not needed.

### The `json` type and `opaque = "verbatim"` — Protobuf `Any` and CBOR tag 24, both extended

**Ancestors.** Protobuf's `Any` is a payload carried by a type name it does not
interpret. CBOR tag 24 is an encoded CBOR data item embedded as bytes. ASN.1 has
the open type. All three say *this is a payload*.

**None of them says the two things we need**, and this is the sharpest gap in the
whole document:

1. **Carry it unopened and unreformatted.** `Any` says what the payload *is*; it
   does not forbid a well-meaning implementation from reparsing and re-emitting
   it. Our rule is that number spellings and key order inside an opaque document
   survive exactly, and the escape policy of the outer definition does not reach
   inside it.
2. **Who is licensed to open it.** In the record, a second field says which
   layer may read `spec`. `Any` names a type; it does not name a reader.

**The measured reason.** Declaring these fields `binary` in the old Thrift sketch
is exactly what let Go's `encoding/json` apply HTML escaping *inside* `spec`
while C++ passed the same bytes through untouched. A document the contract calls
opaque did not survive a round trip through the three implementations
— measured, on the same record and the same three encoders. The type exists so that the
generator emits a passthrough rather than a call to a JSON library.

Point 2 — the licensing field — is **not implemented**. The type is here; the
field that scopes who may read it is not expressible yet.

### `terminator` — POSIX, and NDJSON for the other case

A POSIX text line ends with a newline, which is why one trailing LF is the
default and why a file that omits it is the odd one. `"none"` exists for the
document that is one line of a stream, which is the **NDJSON** convention the
log record already uses one directory away. Two record types in one system have
two framings; the framing belongs to the document type, and this is where it is
said.

LF is named explicitly because two of the three original target languages
default to something else on Windows, and a text-mode write turns every LF into
CRLF invisibly.

### `omit = "zero"` — Go's `encoding/json`

Literally Go's `json:",omitempty"`, under a name that says what it does. It is
in the profile because the shipped record depends on it, and because *optional*
in Thrift and its relatives means *absent*, which is a different rule.

### `omit = "absent"` — Thrift's own `optional`

Kept as a separate word rather than reusing `optional`, so that the two rules
are visibly two rules and neither can be read as the default. **serde**'s
`skip_serializing_if` and **Protobuf 3**'s field presence are the same
distinction drawn by two other communities.

### `list<Struct>` — Protobuf's `repeated`, in its JSON mapping

The syntax is Thrift's container syntax and the spelling is proto3's canonical
JSON: a repeated message is a JSON array whose members are the message's own
JSON objects. Nothing else in this space spells it differently and there was
nothing to invent, which is the point — an adopter who has ever read a
`.proto` knows what `list<Source>` puts on the wire before reading [DEF-A9].

**Two divergences, both declared.** Protobuf's canonical JSON is unindented and
ours is indented by declaration ([DEF-E3]), which is [DEF-A9]'s only original
content and follows from the encoding block rather than from the container.
And **Thrift's own `TJSONProtocol` writes a list as `[<element type>, <count>,
…]`** — the element type and the length repeated on the wire, so that a decoder
with no schema can walk it. We write neither, because our decoder is generated
from the schema and can count; carrying them would be two more things three
languages must agree to spell identically for no reader that exists.

**What forced it.** Measured, 2026-09-08: the profile encoded exactly one nested
struct and no repeated one, so a definition of the `download` layer could not be
written at all — `sources[]` is the field that layer has the most rules about
(`research/dlgen81/STAGES.md`). The proxy that had priced the gap counted the
*representations* the notation could not name and never asked whether it could
hold the *container*, and under that proxy every layer with a repeated record
scores zero mismatch and is equally impossible.

### `map<string,string>` — Thrift's container, and the half `map<string,json>` cannot be

Same syntax, and it exists as a second map rather than as a use of the first
because the two differ in who owns the value. `map<string,json>` carries values
the layer must not read ([DEF-A3]); `map<string,string>` carries values the
definition escapes, orders and *refuses* — a header whose value is a number is
`wrong_type`, which an opaque map has no way to say. **Protobuf** draws the same
line between `map<string, string>` and `map<string, google.protobuf.Value>`, and
**CBOR**'s tag 24 draws it between a nested item and an embedded byte string.

### `(<language>.name = "...")` — Facebook Thrift's `cpp.name`

Apache Thrift has language-scoped annotations (`go.tag` and friends) and
Facebook Thrift added `(cpp.name = "...")` for exactly this: the wire name is
fixed and the identifier is not. **Protobuf**'s `json_name`, **serde**'s
`#[serde(rename)]` and Go's struct tags are the same idea in three other
notations.

**Why we need it, measured:** `requires` is a field name in the record and a
keyword from C++20 on. In the earlier spike this was solved with a keyword table
inside the C++ backend — which meant adding a language could force a change to a
*shared* part of the generator. Declaring it in the definition instead is what
made adding Rust cost one file and nothing else.

### `(document = "true")` — XML Schema's global element, JSON Schema's root

Thrift has no notion of a document root because Thrift's entry points are
services. XML Schema's global element declaration and JSON Schema's root schema
are the ancestors: a definition file with many types needs one that names the
thing being encoded.

### Per-member enum annotations — Thrift's syntax, Smithy's idea

**Smithy traits** are the closest existing thing: typed metadata attached to a
shape. Protobuf enum options are the near-miss — they exist, but they are
untyped for this purpose and the documentation warns against depending on enum
ordering, which one of our vocabularies does.

We need the enum member to carry *what a false claim of it costs*, *whether it
may ever be marked critical*, and in one case *an order that is compared with
`>=`*. Thrift's annotation syntax carries it without a new grammar; the meaning
is Smithy's.

### `duplicate_keys` and `depth_limit` — RFC 8259 §4, and every parser CVE

**RFC 8259 §4** says the names in an object SHOULD be unique and then says
implementations differ, naming three behaviours it has seen. That is a
specification recording a disagreement rather than settling it, which is exactly
the seat: *which* of the three is a term of our contract, and it is written in
the definition. **RFC 7493 (I-JSON) §2.3** takes the stricter half and is the
citation for `"refuse"`.

`depth_limit` has no schema-language ancestor and a very long implementation
one — every JSON parser that has ever had a stack-overflow advisory grew a limit
afterwards, and each picked its own number privately. **Ours is declared**,
because a limit two implementations do not share is a document one accepts and
the other refuses, which is the defect the whole repository exists to prevent.

### `unknown_fields` per struct — Protobuf's unknown-field retention, inverted

**Protobuf** keeps unknown fields and re-emits them, which is the right answer
for a wire format that must survive a proxy. **JSON Schema**'s
`additionalProperties` is the shape we took: a per-object policy, expressed in
the schema.

**Declared divergence: we refuse rather than retain**, and the reason is
`omit`. A retained unknown field has to be written back somewhere, and where it
goes depends on the omission rule of a field the reader does not know. Refusing
is the only answer that cannot silently change the bytes.

### `(write = ..., read = ...)` — Postel's law, made checkable

The principle is everywhere and the construct is nowhere. **HTML5** is the
closest honest ancestor: it states authoring conformance requirements that are
much narrower than its parsing algorithm, and states both, in one specification.
**Protobuf's JSON mapping** does the same in prose — it emits one spelling of an
enum and accepts two. **RFC 7493 (I-JSON)** is the same move pointed at what you
send.

**In all three the width lives in prose and in the implementation, never in the
type.** Ours is on the typedef, both grammars are named, and the profile refuses
a definition whose `read` does not contain its `write`. That last part is what
makes it a construct rather than a comment: an unchecked promise to be liberal
is how a reader ends up liberal in a different direction from its peer.

### `(unknown = "grant" | "refuse")` — **no ancestor**

The nearest relatives are JSON Schema's `additionalProperties` and JOSE's
`crit`, and neither is this. Both are one policy per object. Ours is one policy
per *vocabulary*, chosen by the direction the vocabulary points: a word that can
only **grant** fails open, because a capability we cannot read grants nothing; a
word that **constrains** fails closed, because a closed vocabulary that fails
open is not closed.

This is one of the two constructs in this file that an adopter has to be taught
rather than reminded of, and it is a tax on adoption. It is here because both
directions already exist in the shipped code, half a repository apart, with the
asymmetry stated in neither contract page.

### The `vocabulary` block — four near-ancestors, and the axis none of them has

This is the construct the profile most needs to justify, so here is the reading
before the claim.

**X.509 critical extensions (RFC 5280 §4.2).** Every extension carries a boolean;
an implementation meeting a critical extension it does not recognise MUST reject
the certificate. This is the oldest and most widely deployed form of the idea and
it is the direct ancestor of what `critical` *means*. **LDAP controls
(RFC 4511 §4.1.11)** are the same design, per control.

Where it stops: the flag is set **per instance by the writer**, and the ASN.1
module says nothing about which extensions may carry it. RFC 5280 does have the
rule we want — *basicConstraints* MUST be critical in a CA certificate,
*authorityKeyIdentifier* SHOULD NOT be — but it says it **in prose**, so no tool
enforces it and every implementation re-reads it. Our `never_critical` is that
same rule moved into the module, where a generator can emit it and a definition
can be diffed.

**JOSE `crit` (RFC 7515 §4.1.11)** is closer in shape than X.509: a *list* of
header parameter names the recipient must understand, exactly as ours is a list
rather than a flag per item. It also carries a per-name restriction — a
recipient must reject a `crit` that names a parameter defined by the JWS
specification itself. **That is `never_critical`, for one fixed set, in prose.**
There is no place to declare a vocabulary, no second list, and no subset.

**PNG chunk criticality (ISO 15948 §5.4)** is the fourth and it is the one that
locates us. A chunk type's first letter says whether a decoder must understand
it: criticality is a **property of the name**, fixed once by the registry, not a
choice the encoder makes. So the design space has two occupied corners —
**per-name and fixed** (PNG), **per-instance and unrestricted** (X.509, LDAP,
JOSE) — and ours is the third: **per-instance, restricted per name.** A record
chooses what it demands of its reader, from a set the definition has already
decided may be demanded.

**JSON Schema `$vocabulary` (2020-12 core §8.1.2)** is the nearest thing in a
schema language and it is genuinely the same true/false distinction. It binds the
wrong party: it says which keyword semantics a *schema processor* must implement
to use a meta-schema, not which features a *document* requires of its reader. It
is also written by hand and derived from nothing.

**The derived half has a partial ancestor and we should say so.** JSON Schema's
`dependentRequired`, and `if`/`then` over `contains`, can express *this name is
in that list exactly when this field is present* — awkwardly, one clause per
term, but it can. **Zip's "version needed to extract"** and **PDF's `/Version`**
are the same idea collapsed to a scalar the writer computes from what it used.
What none of them has is the pairing: a derived list, a subset of it that is
mandatory, and a per-name veto on being in that subset. That triple is ours.

**And the honest cost.** Two constructs in this profile now have to be taught
rather than recalled, and this is the second. It is here because both halves are
already in the shipped record, and until now neither contract page said which
names may be demanded of a reader — which means five implementations were each
about to decide it.

### `const list<i32>` — Thrift's `const`, and one measured drift

Nothing novel. It is here because the ten HTTP status codes that mean *no,
permanently* were transcribed by hand into three languages, in three notations,
and disagreed by four rows until somebody diffed them. One closed list, one
place, five languages. This is the cheapest argument for generation in the
project and it needs no new syntax at all.

### The `refusal` block — POSIX `errno` and gRPC's codes, ordered

A closed, named set of ways to say no is old and uncontroversial: POSIX `errno`,
HTTP's status registry, and gRPC's `google.rpc.Code` are all one list every
implementation shares, and all three exist for the reason ours does — a caller
branches on the name and cannot branch on prose.

**The divergence is the order, and it is the whole reason the block exists.**
POSIX says that when a call could fail several ways, any one of the applicable
errors may be returned; HTTP leaves the choice among applicable statuses to the
server; gRPC says nothing about it at all. Every one of them declares the
vocabulary and leaves the precedence to the implementation, which is affordable
when there is one implementation per platform and is not affordable when the
conformance proof is *five implementations refuse the same input with the same
word*. Our corpus found this the ordinary way: an input that is both a bad
string and a missing field has one right answer, and nothing in the definition
said which.

The `stage` axis is the compiler's, not the protocol's: lexical before
syntactic before semantic is the oldest reason one diagnostic wins over another,
and it is here because it makes the order *derivable* for the cases that span a
stage rather than merely *agreed*.

### `protocol` — JSON-RPC's envelope, without JSON-RPC's transport

JSON-RPC 2.0 is the direct ancestor and the resemblance is deliberate: a request
object naming a `method`, a response object carrying either a result or an
error, and a reserved answer for a method the peer does not have — our
`operation`, `verdict` and `unknown_operation`. It is cited rather than adopted
because JSON-RPC fixes the field names and we generate an envelope whose field
names the definition chooses, which is what lets an existing hand-written wire
format be described rather than replaced.

**Two divergences, both from the same decision.** Thrift's `service` and
Protobuf's `service` both generate a client, a server skeleton and a transport;
that is why [DEF-F1] forbids `service` and why this construct is not it.
`protocol` declares the bytes of two messages and the names that travel in them,
and stops — no stub, no dispatcher, no connection. The second is that JSON-RPC
carries an error *object* with a numeric code; we carry a verdict *name* drawn
from a declared enum, for the reason [DEF-A5] gives: a wire name outlives any
one language's spelling of it, and a number outlives nothing a reader can read.

The measurement that justified stopping there: `test/wire` puts a generated peer
and a hand-written one on both ends of the same exchange in both directions. The
envelope is the same bytes; the framing, the connection lifetime and the
dispatch are written by hand in each peer and are different lengths in each.
There was nothing to generate.

---

## What has no ancestor at all

Every construct here is something an adopter must be *taught* rather than
*reminded of*, which is a real cost and worth being able to justify out loud.
Four are in the profile. One is still not.

**In the profile:**

1. **`unknown = grant | refuse` pointed by direction.** Above.
2. **`opaque = "verbatim"` in the *carry it unreformatted* sense.** Every
   ancestor says "payload"; none forbids the round trip that changes its bytes.
3. **`vocabulary` — per-instance criticality, restricted per name, over a list
   derived from the instance.** Above, with the four near-ancestors and the axis
   each of them is missing.
4. **A refusal vocabulary with a declared precedence.** The list has three
   ancestors and the order has none: every one of them declares the words and
   leaves the choice between two applicable ones to whoever is implementing.

Two more were on this list and have come off it, because the thing they lacked
was a place to be said rather than an ancestor. **`(write = ..., read = ...)`**
is Postel's law with a width relation the profile checks, and
**`unknown_fields`** is `additionalProperties` scoped per struct. Both were
previously filed as *not expressible*, which was a statement about our notation
and got mistaken for a statement about the world.

**Not in the profile, and named so nobody thinks it was forgotten:**

5. **A vocabulary name that denotes a rule about writes rather than a field.**
   In the record, one content name means *a terminal record refuses its own
   holder's update*. `vocabulary` can now carry the name, its criticality and
   its derivation — but `when` derives a term from a *field's presence*, and a
   rule about what the holder may do next is not a field. JSON Schema's
   `$vocabulary` is still the nearest and it names keywords with semantics,
   which is about the document rather than about what a holder may do to it.

**The general shape, which is the finding rather than the list.** Every ancestor
citation above covers a statement about *what the document contains*. Everything
on this list is a statement about *what a reader must do* — a reader obligation.
Schema languages describe documents, so nobody has these, because nobody else's
conformance proof is *two implementations produce the same bytes and refuse the
same inputs*. Everyone else's is *the runtime handles it*, and a runtime does not
need to be told in the schema what it already does.
