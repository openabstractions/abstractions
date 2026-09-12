# The language

A profile of Apache Thrift's IDL. Thrift's grammar, our backend, and no Apache
Thrift code fetched, compiled or linked at any point.

A profile is a **restriction plus a small addition**. Everything a reader who
knows Thrift already knows still means what it meant; the additions say things
Thrift has no word for, in Thrift's own annotation syntax. Where a
construct's ancestor is not Thrift, [LINEAGE.md](LINEAGE.md) names it, and every
divergence from an ancestor is recorded there with what forced it.

Rules carry tags. A tag is what a scenario, a test or a review cites.

---

## 1. Taken from Thrift, unchanged

**[DEF-T1]** `struct Name { <id>: required|optional <type> <name> }`. Field ids
are positive, unique within the struct, and never reused.

**[DEF-T2]** `typedef <scalar> <alias>`.

**[DEF-T3]** `list<string>` and `map<string,json>`. The container syntax is
Thrift's; two further instantiations of it carry an encoding rule Thrift has no
word for and are declared as additions instead — `list<Struct>` ([DEF-A9]) and
`map<string,string>` ([DEF-A10]).

**[DEF-T4]** Annotation syntax: `(key = "value", key2 = "value2")`, after a
field, after an enum member, or after a struct or enum body.

**[DEF-T5]** `enum Name { <id>: member }` and `const <type> NAME = [...]`.

**[DEF-T6]** `//`, `#` and `/* */` comments; `namespace <language> <name>`.

## 2. Forbidden — the restriction half

Each is refused by name, with the reason in the error.

| refused | why |
|---|---|
| **[DEF-F1]** `exception`, `throws` | Behaviour is not in the schema. An epoch that only increases, a lease that is exclusive, a successor that resumes only from a proven prefix — none of that is expressible and none of it belongs here. It lives in the contract page and the scenario corpus. |
| **[DEF-F2]** `union` | A union's absent arm and an absent field are two spellings of one thing, and the record already spells absence two ways ([DEF-A4]). |
| **[DEF-F3]** `set<T>` | A set has no order that three languages share. Use `list`. |
| **[DEF-F4]** `double`, and any float | A float has more than one spelling. `1.50` and `1.5` are one value and two records. Numbers inside an *opaque* field keep whatever spelling they arrived with ([DEF-E5]); the definition itself has no float. |
| **[DEF-F5]** `binary` on unsupported backends | Arbitrary octets, distinct from UTF-8 `string` and verbatim `json`. Go/C++/Python support it; Rust/JavaScript currently refuse it explicitly. See binary values below. |
| **[DEF-F6]** `include` | One file, one normative text. A transitive definition graph has no single thing to point an adopter at. |
| **[DEF-F7]** default values (`1: optional i32 n = 3`) | A default written back is not an absence, and the difference is load-bearing. A reader that supplies a default on read and a writer that records one are doing different things, and one syntax for both hides it. |
| **[DEF-F8]** `i8`, `i16`, `byte` | Integer width is pinned per field and we need two widths. Offering more invites a third. |
| **[DEF-F9]** an `optional` field with no `omit` | See [DEF-A4]. |

## 3. Added

### [DEF-E1] The encoding declaration

Exactly one per definition, and a definition without one is refused: a record
shape with no declared encoding has no legal spelling, and three implementations
will each pick a defensible different one.

    encoding json {
      escape         = "minimal"
      indent         = "2"
      map_keys       = "utf8-bytes"
      numbers        = "integer-decimal"
      opaque         = "verbatim"
      terminator     = "newline"
      duplicate_keys = "refuse"
      depth_limit    = "64"
    }

Every key is required. Every value is checked against a closed list. The
declaration is not decorative: changing `escape` alone changes the bytes every
backend produces, and `gen/parse_test.go` fails if any backend ignores it.

**[DEF-E2] `escape`** — `"minimal"` escapes `"` and `\`, uses `\b \f \n \r \t`
for those five controls, `\u00XX` in lower-case hex for every other C0
character, and emits everything else as raw UTF-8. `"ascii"` additionally
escapes every codepoint above U+007F as `\uXXXX`, surrogate-paired above
U+FFFF. Nothing else is legal, and in particular the target language's own
default is never legal, because there are three of those and they disagree.

**[DEF-E3] `indent`** — a width from 0 to 8, applied per nesting level. `0` is
compact.

**[DEF-E4] `map_keys = "utf8-bytes"`** — a map's keys are written in ascending
order of their UTF-8 bytes. Declared because it is the one ordering rule a
target language can get silently wrong: three of the five targets inherit it
free from their own ordered container, and JavaScript sorts by UTF-16 code unit,
which puts U+1D11E before U+FFFD where the others put it after.

**[DEF-E5] `numbers = "integer-decimal"`** — every number the definition owns is
an integer, written in plain decimal with no sign on zero, no exponent and no
leading zero. Numbers inside an opaque field are not the definition's and are
carried verbatim.

**[DEF-E6] `opaque = "verbatim"`** — see [DEF-A3].

**[DEF-E7] `terminator`** — `"newline"` ends the document with one LF,
`"none"` ends it with the closing brace. LF, never CRLF, on every platform.

**[DEF-E8] `duplicate_keys`** — `"refuse"` or `"last"`, what a reader does with a
map key it has already seen. The writer cannot produce one, so this is a rule
about reading only, and it is here because a rule nobody states is decided
five times by five libraries: `encoding/json` keeps the last, Python's `json`
keeps the last, and neither says so where a contract could cite it.

**[DEF-E9] `depth_limit`** — 1 to 1024, the deepest nesting a reader accepts,
counted from the document object and *including* nesting inside an opaque value.
A reader with no limit is a denial of service on untrusted input, and a reader
whose limit is its own stack is a limit nobody can write down.

### [DEF-A1] `(document = "true")`

Exactly one struct carries it. It is the type `encode` takes, and the thing the
encoding declaration describes.

### [DEF-A2] `(<language>.name = "...")`

The wire name is the field's name and never moves. This changes only the
identifier the named backend emits — `(cpp.name = "requires_")`, because
`requires` is a keyword from C++20 on and the record has a field called
`requires`. Unrecognised language prefixes are ignored, so adding a backend
cannot invalidate an existing definition.

### [DEF-A3] The `json` type

One syntactically valid JSON value, **validated in full and carried as the bytes
it arrived as**: not re-indented by its own rules, not re-escaped, its number
spellings and its key order preserved exactly as they arrived. The escape
*policy* in [DEF-E2] applies to strings **this definition owns** and never
inside a `json` field; the JSON *grammar* applies everywhere, at every depth.

So a reader checks escapes, delimiters, separators, literals, number grammar,
control characters, UTF-8 and the declared depth limit inside a `json` value,
and it checks nothing about what the value means. Every restriction the encoding
block declares reaches inside — `duplicate_keys` included — because a payload
that only our reader can parse is not a payload anybody can hand on. And no
number is converted to a host type in order to be validated: an integer past
every float and every 64-bit width is legal JSON, and a reader that widened it
to check it would lose it.

This is the single most load-bearing type in the profile, and the one no other
schema language has. `spec` and `checkpoint` are opaque to the layer that
carries them, and a layer that cannot read one must still write it back byte for
byte.

### [DEF-A4] `(omit = "zero")` and `(omit = "absent")`

Every `optional` field says which, and a `required` field may say neither.

- `omit = "zero"` — written unless the value is the type's zero: an empty
  string, `0`, `false`, an empty list, an empty map. Go's `omitempty`.
- `omit = "absent"` — written unless the value is absent. Thrift's `optional`.

They are different rules and the shipped record uses both in one struct: a step
count of `0` is omitted, and a step that has not started is omitted. A struct
field may only be `omit = "absent"` — a struct has no zero to omit on.

**And a collection field may only be `omit = "zero"`**, which is the mirror
image and was added 2026-09-08 because the profile accepted the other half in
silence. No backend can tell an empty collection from a missing one: there is no
`Option<Vec<T>>` in any of the five and inventing one would put a second
absence in a profile that refuses unions for having one ([DEF-F2]). Before this
rule a `list<string>` marked `omit = "absent"` parsed and emitted a Go presence
test comparing a slice against the empty *string* — a definition the generator
accepted and the compiler had to catch.

### [DEF-A5] Per-member enum annotations, and `(unknown = ...)`

An enum is a closed vocabulary of wire names. Every member may carry arbitrary
annotations, and the enum body carries `(unknown = "grant")` or
`(unknown = "refuse")` — what a reader does with a member it has never heard of.

The direction is the point and it is not one policy per document. A vocabulary
that can only **grant** fails open, because a word we cannot read grants
nothing. A vocabulary that **constrains** fails closed, because a closed
vocabulary that fails open is not closed.

Enums generate a name list, the unknown-member policy, and one table per
annotation key. They are not generated as language enums, because a wire name
outlives any one language's spelling of it.

### [DEF-A6] `const list<i32>` and `const list<string>`

A closed list, generated once per language instead of transcribed once per
language. The ten status codes that mean *no, permanently* were written by hand
in three languages and disagreed by four rows.

### [DEF-A7] `(unknown_fields = "refuse" | "grant" | "preserve")`

Every struct says it, and a struct that does not is refused. `refuse` rejects the
whole document on a field name the reader has never heard of; `grant` skips the
value and carries on. `preserve` retains that value for subsequent writes.

**It is per struct and not per document**, which is the whole point: in the
shipped record every part of the record refuses, and the service envelope around
it grants, because an older peer answering a newer peer's request must not stop
over a field it was not asked to read. One policy per document cannot say that,
and a reader written by hand says it seven times in one language and once in
another.

~~`Delegation` describes a system somebody else runs and may carry fields we do
not model~~ 2026-09-08: it was `grant` in the shipped definition and
`abstraction-job/CONTRACT.md` [JOB-F1] had always named `delegation` among the scopes that
refuse. The example was the violation.

**`grant` skips and drops; it does not keep.** A newer writer's addition to a `grant` scope is gone
the first time an older reader writes the value back. That is proto3 before
3.5.0, and it is the reason `grant` belongs only where nothing is written back.
A declared extension map also carries values this definition does not model;
those values remain preserved by the existing opaque-JSON behavior.

**`preserve` is an explicit alternative for an open struct**, not a change to
`grant` or `refuse`. It stores unknown decoded field names and their raw JSON
values in a generated `extras` map (`Extras` in Go). This is host-side storage;
there is no additional wrapper key on the wire. The name `extras` and its
backend member spellings, including annotation renames, are reserved in a
preserving struct. A wire key named `extras` is itself an ordinary unknown key
and may be held inside that map. JavaScript uses a null-prototype map so keys
such as `__proto__` remain data.

The writer emits known fields in definition order, then extra fields sorted by
the UTF-8 bytes of their decoded names. It emits every extra value, including
null, false, zero, empty strings, empty arrays and empty objects. Extra values
follow the existing opaque-JSON rule: string escapes, number spellings and member
order are retained; surrounding whitespace may be reflowed. Unknown keys use the
declared string escaping policy. **This guarantees unknown value preservation,
not whole-object byte identity:** promoting an unknown field to a declared field
can change its placement and apply that field's type/omission rules. Adopters
must consider that separately before replacing an existing opaque representation.

Decoded duplicate unknown names use `duplicate_key` when `duplicate_keys` is
`refuse`; with `last`, the final value wins. Known repeated fields retain their
`duplicate_field` rule. A caller-constructed extras entry colliding with **any**
declared wire field is refused as `duplicate_field`, even if that declared field
would otherwise be omitted. Extra keys must be valid Unicode strings. Raw values
are syntax-, string-, duplicate- and depth-validated by the same JSON scanner at
their actual enclosing depth before emission. Multiple values or trailing data
produce `trailing_bytes`; malformed, duplicate or overly deep values retain
their existing refusal words. Invalid direct writes throw (C++/Python/JavaScript)
or panic (Go/Rust), as the infallible encoder interfaces already require.

The mode applies independently to nested and repeated structs. Protocol envelopes
continue to require `grant` under DEF-P1; this addition does not change them.

### [DEF-A8] `vocabulary` — a declaration derived from the instance

    vocabulary Content {
      1: "abstraction.job/base@1"        (when = "always")
      2: "abstraction.job/intent@1"      (when = "intent")
      3: "abstraction.download/ranges@1" (when = "checkpoint.verified", strip_critical = "true")
      4: "abstraction.job/terminal@1"    (when = "state", is = "complete,failed,cancelled")
      5: "abstraction.job/recall@1"      (when = "lease.recall")
    } (of = "Record", names = "content", critical = "critical")

Two `list<string>` fields of the document carry a set of feature names. `names`
is **derived**: for every term, its presence in that list must equal its
predicate over the instance, and a name outside the vocabulary in that list is
granted and ignored. `critical` is the subset the reader **must** understand —
a name in it that the reader does not know refuses the document rather than
warning about it.

A term takes `when`, it may take `is`, and it may take `strip_critical =
"true"`. Anything else on a term is refused: a flag the profile does not read
is a rule nobody enforces.

**The predicate is one of three shapes, and there is no fourth.** A definition
language that can express anything is one nobody can generate from, so the set
is closed and each shape says what absent, null and empty mean:

- `when = "always"` — present in every instance.
- `when = "<path>"`, **presence**. The path walks required struct fields from
  the document and ends at an optional field, whose own `omit` rule decides:
  `omit = "absent"` is present when the value is there, `omit = "zero"` when it
  is non-zero — a non-empty string, list or map, a non-zero number, `true`. A
  `null` where a struct or scalar is expected is `wrong_type` at the grammar
  stage and never reaches the predicate; an empty object is present (its own
  required fields decide whether it is legal). A required field cannot end a
  path — it is always present, and a term that is always present says so.
- `when = "<path>.<key>"` where the path ends at an opaque `json` field,
  **presence of a member**. The term is present when the opaque value is a JSON
  object with a member of that name whose value is not `null`. A `null`
  member, an empty object `{}`, an absent field, and a value that is not an
  object (`null`, `[]`, `7`) are all absent; `[]`, `{}`, `""`, `0` and `false`
  as the member's value are present, because the reader judges that a value is
  there and never what it is. The key is decoded before comparison, so
  `"verified"` and `"verif\u0069ed"` are one key. Exactly one key, and no
  deeper: a reader that walks further is parsing a payload the definition does
  not own ([DEF-A3]).
- `when = "<path>", is = "<word>,<word>,..."`, **membership**. The path ends at
  a `string` field with no grammar, required or optional, and the term is
  present when the field holds one of the words, compared byte for byte. An
  absent optional field, the empty string, and any word not listed are all not
  present; the predicate does not validate the field, so a word outside the
  list is a legal value that declares nothing. `null` is `wrong_type` before
  the predicate runs. Words are non-empty and distinct; membership never reads
  inside an opaque value.

Refused by name: a path through an optional struct, through a scalar or a
list, two keys into an opaque value, membership on a number, a bool, a
timestamp or an opaque member, `is` on `when = "always"`, and a term derived
from the list that carries it. No negation, no conjunction, no comparison, no
expression: the four names `abstraction-job/job.thrift` could not declare before this set
existed needed exactly these three shapes and nothing more, and each addition
after this one is a decision about five backends, not a convenience.

The generated reader exposes the member test as `Member`/`member` beside the
codec, because it is what a writer needs to declare a term over a value it
must not read: a store that keeps a checkpoint opaque can still say whether one
names `verified`.

Three things the generated reader does with `critical`, in this order:

- a name the definition marks `strip_critical` — **removed from the list**, so
  the reader carries on and writes the record back without it
- a critical name outside the vocabulary — `unknown_critical`
- a critical name absent from `names` — `not_a_subset`

**`strip_critical` is the sharp one and it is a writer obligation with a reader
remedy, not a refusal.** A feature whose own definition says an ignoring reader
is still correct cannot be made mandatory by whoever wrote the record: marking
one tells a stranger to refuse work over a decoration, and the layer removes the
marking rather than relaying it. Its ancestor is RFC 5280 §4.2 — *"Conforming
CAs MUST mark this extension as non-critical"*, with no matching instruction to
reject a certificate whose issuer did.

~~a critical name the definition marks `never_critical` — `never_critical`~~
2026-09-08: the refusal word is gone from the profile. It had no ancestor, it
contradicted the rule it was written to serve, and the set it enforced in the
shipped definition was the inverse of the contract's. It was never published as
an error identifier — `abstraction-job` at `main`, `go/v0.1.0` and `go/v0.2.0`
carries no generated reader at all — so it was removed rather than deprecated.

Exactly one vocabulary per definition, over the document struct. Two derivations
of one document are two truths.

### [DEF-A9] `list<Struct>` — the repeated record

    2: required list<Source> sources

**One legal spelling, and it is the whole conformance argument.** A repeated
record is a JSON array of the element's own objects, in the order the writer
holds them:

    "sources": [
      {
        "url": "https://a/x",
        "priority": 1
      },
      {
        "url": "https://b/x"
      }
    ]

The array opens on the field's line. Each element sits one indentation level in
from the array ([DEF-E3]), the separator is a comma written after the previous
element's closing brace, and the closing bracket sits at the array's own level —
the same three rules `list<string>` already obeys, with a struct where the
string was. **An empty repeated record is `[]`**, those two bytes, exactly as
every other empty container in the profile; there is no second spelling and no
`null`.

An element is encoded and decoded by the element struct's own generated codec,
so nothing about a struct changes for being repeated: field order is declaration
order, `omit` decides presence, `unknown_fields` decides what an unheard-of key
does. **The array is one nesting level and each element is another**, so
[DEF-E9] `depth_limit` counts both, and every grammar and structure rule reaches
inside an element — a field name twice in one element is `duplicate_field`, a
map key twice inside one element is `duplicate_key` ([DEF-E8]), and an element
that is not an object is `wrong_type` at that element's first byte.

**Ancestor: protobuf's `repeated`, in its canonical JSON mapping** — a repeated
message is a JSON array whose members are the message's own JSON objects, which
is what a reader in this space already expects. Two deliberate divergences,
recorded in [LINEAGE.md](LINEAGE.md): protobuf's JSON is unindented and ours is
indented by declaration, and Thrift's own `TJSONProtocol` writes a list as
`[<element type>, <count>, …]` while we write neither, because a definition that
names the element type and a reader that can count do not need the wire to
repeat them.

**A repeated record is not an envelope field** ([DEF-P1]): an envelope is a
parameter list and a list of records is a tree.

### [DEF-A10] `map<string,string>`

    3: optional map<string,string> headers (omit = "zero")

Keys and values are both strings **this definition owns**: escaped by [DEF-E2],
ordered by ascending UTF-8 bytes of the key ([DEF-E4]), a repeated key decided by
[DEF-E8], and empty written `{}`. It is the only map whose values the profile
reads, which is the reason it is not `map<string,json>` wearing a different
name: a value that is not a JSON string is `wrong_type` at that value's first
byte, and that is a refusal an opaque map cannot make. Headers and labels are
strings we escape; a payload is bytes we carry ([DEF-A3]), and one type cannot
be both.

### [DEF-R1] `refusal` — the words a decoder may say, in the order it says them

    refusal {
       1: malformed        (stage = "grammar")
       ...
      15: content_mismatch (stage = "derivation")
    }

Exactly one per definition, and a definition without one is refused: a decoder's
public surface is the word it says no with, and a surface nobody declared is
fifteen string literals in five backends.

**The declaration order is the answer to the question the word list leaves
open.** An input can break two rules at once, and two readers can refuse the
identical set of inputs and still disagree on the word for half of it. The
earlier a word is declared, the earlier it is reached: given two violations in
one document, the reported word is the one with the lower id.

`stage` is the reader pass the word belongs to, and it exists to make that order
checkable rather than remembered. The four stages are ordered
`grammar`, `structure`, `document`, `derivation`, and a word declared after a
later stage is refused — you cannot write down an order a reader cannot walk.
Within one stage the declaration order is the whole rule and nothing checks it
but the corpus.

Every backend is checked against this block as it is emitted: a word a backend
refuses with that the definition does not declare, and a word the definition
declares that a backend can never say, are both build failures. That is what
makes the block the source rather than a sixth copy of one.

### [DEF-P1] `protocol` — the envelope

    protocol Store {
       1: submit
       ...
      11: write
    } (request = "Request", response = "Response", operation = "op",
       verdict = "kind", verdicts = "Verdict", unknown_operation = "unknown_op")

At most one per definition. It names two ordinary structs as the request and the
response, the request field carrying the operation name, the response field
carrying the verdict, the enum the verdicts are drawn from, and the verdict a
peer answers an operation it has never heard of with.

Six obligations the profile enforces, each because the alternative is three
peers inventing the same thing differently:

- **An envelope is not the document.** Neither struct may carry
  `(document = "true")`; an envelope carries a document and is not one.
- **An envelope grants.** Both structs must be
  `(unknown_fields = "grant")` ([DEF-A7]). A peer that refuses a field it has
  never heard of cannot be extended without breaking every peer not yet
  upgraded, which is the property a request envelope exists to have.
- **An envelope is a parameter list, not a tree.** Its fields are scalars,
  `list<string>`, `map<string,json>`, `map<string,string>`, `json` and
  `list<json>` — never a struct and never a repeated record ([DEF-A9]).
  A structured argument travels as `json`, opaque, exactly as [DEF-A3] carries
  the document itself.
- **`operation` is required and a string.** A request that does not say what it
  asks for is not a request.
- **`verdict` is `(omit = "zero")`.** An answer that succeeded says nothing, so
  a peer never has to know that present-and-empty means yes.
- **`unknown_operation` is a member of the verdict enum**, and that enum is
  `(unknown = "grant")` — a peer must be able to carry a verdict it does not
  know back to its caller.

`list<json>` exists for this construct and is legal nowhere else: a document
holding a list of documents is a document, and the profile has one of those.

**An envelope is written compact whatever `indent` says** ([DEF-E3]), and any
`json` it carries is compacted with it. The indentation of a document is for
whoever reads it and an envelope has no such reader. This is the one place the
profile reflows a `json` value, and it is safe for the reason [DEF-A3] cares
about: whitespace between tokens is the only thing removed, so the document
decodes to what it was and re-encodes to the bytes it arrived as. A peer that
compares two documents compares them decoded, never as they came off the wire.

### [DEF-G1] `(write = ..., read = ...)` on a typedef, and `rfc3339-wide`

    typedef string timestamp (write = "rfc3339-micros", read = "rfc3339-wide")

Two grammars for one type, each drawn from a closed list. **The width between
them is the interoperability guarantee**, and both are named so it can be read
off the definition instead of discovered.

Naming one and not the other is refused. So is a `read` narrower than its
`write`: a reader that refuses what its own writer emits is not interoperable,
and the profile knows which of its grammars contains which.

`read = "rfc3339-wide"` is what a decoder accepts: no fraction or one to nine
fractional digits, either case of the `T` and `Z` separators, and a numeric
offset in place of `Z`. Anything else is [`bad_timestamp`](#the-refusal-vocabulary).

The calendar is proleptic Gregorian, with four-digit years `0000` through
`9999`, real month/day combinations, hours `00`–`23`, and minutes and seconds
`00`–`59`. Numeric offset hours are `00`–`23` and minutes `00`–`59`.
Leap-second `:60` is refused: these codecs do not invent a mapping to a different
instant. The offset-normalized UTC date must also stay within the four-digit
year range. `-00:00` is accepted and canonicalized to UTC, as in the existing
semantic timestamp bindings. These are the raw codec's bounds; an application's
native time type may have a smaller range (Python datetime, for example, has no
year zero). A binding must report that limitation rather than alter the instant.

### [DEF-G2] `rfc3339-micros`, the write grammar

`write = "rfc3339-micros"` is what an encoder emits: exactly six fractional
digits, upper-case `T` and `Z`, UTC. One spelling, so the same instant is the
same bytes in five languages.

It is a separate rule from [DEF-G1] because it is a separate obligation — a
generated encoder must produce it, a generated decoder must not require it — and
because the two are cited separately by the code that implements them.

The encoder converts the numeric offset to UTC, pads a missing or short fraction
with zeros, and truncates digits beyond six without rounding. It applies this
rule to timestamp fields only; timestamp-looking strings inside opaque JSON
remain untouched. A decoder validates the instant but retains the input string;
canonicalization happens on write. Already canonical values remain byte-identical.
An invalid timestamp supplied directly to an encoder is a programming error:
the existing infallible Go/Rust encoding APIs panic, while C++/Python/JavaScript
throw; the diagnostic is `bad_timestamp`. Wire readers return their normal
`bad_timestamp` refusal. No replacement instant or empty string is emitted.

---

## What the generator emits

Vocabulary, never engine: the record's types, its encoder, its decoder, the enum
name lists with their per-member tables, the const lists, the refusal words in
their declared order, and the envelope of [DEF-P1] — its two structs, the
operation names, the verdict names and the unknown-operation verdict. No state
machine, no validation of any rule that is not a shape or an obligation declared
above.

The same list, in a second form: the `docs` backend emits the reference page
from exactly those declarations, and each construct on it cites the rule above
that admits it. **The page is therefore bounded by this section** — it can carry
no meaning the definition does not hold. Service/method `doc` annotations carry
API intent; additional interpretation, which pattern
it descends from, and any example that runs are written by hand elsewhere and
linked to.

## How much of it an artefact carries

An adopter that takes the envelope should not receive the record codec. So the
generator takes a selection:

    go run . ../testdata/job.thrift <outdir> -only=Request,Response,Verdict,Store

A **surface** is one declaration — a struct, an enum, a constant, the
vocabulary, the protocol. The encoding block, the refusal words and the
typedefs are not surfaces and cannot be withheld: they bind everything a
definition emits, so there is no artefact that carries one and not the others.

The list is the artefact's whole contents, not a set of roots. **A selection
that does not close is refused, naming what is missing and what needed it**, so
one run produces the line that works. Three kinds of edge, and the two that
surprise people:

- a struct needs every struct its fields carry;
- **the document needs its vocabulary** — a record codec that decodes the
  fields and skips the derivation accepts records the contract refuses, and
  compiles perfectly while doing it;
- **an envelope struct needs its protocol** — the protocol is what makes it an
  envelope, and without one the same struct is emitted as a document, under the
  same name, with a different encoder.

Withholding a surface may not weaken a check. Whether a backend can say every
refusal word is asked of that backend over the *whole* definition, never over
the artefact, so a selection cannot answer it by removing the code. The checks
that do narrow — every declared name is on the page — narrow to the surface
list in `scripts/generate.targets`, which is compared against the artefact byte
for byte, so a scope cannot shrink without the declaration shrinking with it.

## What the generator does not emit, and will not

**A generator that opens a socket has left its jurisdiction.** The definition
declares the bytes of a message. Everything about *delivering* one belongs to
the transport binding, is written by hand once per binding, and is not derivable
from any definition — because the same definition is meant to travel over a
pipe, a socket, a queue and a file, and each answers these differently:

| not generated | whose it is |
|---|---|
| framing — length prefix, delimiter, or a stream that ends | the binding |
| connection lifetime — one exchange per connection, or many | the binding |
| reconnection, retry, backoff, deadlines | the binding |
| the socket, the address, the listener, the dial | the binding |
| concurrency — who may have a request in flight | the binding |
| authentication, encryption, compression | the binding |
| which operation does what, and to which store | the implementation |

`test/wire` is the worked example: two Go peers and a Python one, all three
carrying the same generated envelope, and every line about connecting, framing
written by hand in each. That older envelope-only example predates the
service binding in DEF-S1; service dispatch is now generated.

**The enforceable form of this rule is the import list.** Each backend declares
the imports it may emit — Go, Python and JavaScript declare none, Rust declares
`BTreeMap`, C++ declares eight standard headers — and emitting anything else
fails the generator's own build. A module that cannot name a socket, a clock or
a filesystem cannot reach one, and widening that list is how a backend would
have to admit it was trying to.

## The refusal vocabulary

A decoder's public surface is its refusals, so they are a closed list, and they
are declared in the definition ([DEF-R1]) rather than transcribed into five
backends. Fifteen words; five implementations; the same word at the same byte
offset for the same input, checked by `test/corpus` on every run.

**The rows are in the order two of them are chosen between.** An input that
breaks two rules is refused with the earlier word, and `test/corpus` carries a
case for each pair that can arise.

| word | stage | when |
|---|---|---|
| `malformed` | grammar | JSON this grammar does not admit |
| `bad_string` | grammar | a raw control character, a bad escape, an unpaired surrogate, or bytes that are not UTF-8 |
| `number_spelling` | grammar | a leading zero, `-0`, a fraction, an exponent, or out of range for the declared width |
| `wrong_type` | grammar | a value whose JSON type is not the field's |
| `bad_timestamp` | grammar | a string outside the declared read grammar ([DEF-G1]) |
| `depth_exceeded` | grammar | nesting past `depth_limit` ([DEF-E9]) |
| `duplicate_key` | grammar | one map key twice, under `duplicate_keys = "refuse"` ([DEF-E8]) |
| `duplicate_field` | structure | one field name twice in one struct |
| `unknown_field` | structure | a field name in a scope declared `unknown_fields = "refuse"` ([DEF-A7]) |
| `missing_field` | structure | a `required` field absent |
| `trailing_bytes` | document | anything but whitespace after the document |
| `unknown_critical` | derivation | a critical name the reader does not know ([DEF-A8]) |
| `not_a_subset` | derivation | a critical name absent from the derived list |
| `content_mismatch` | derivation | a derived list that disagrees with the instance |

The stages are the reader's own passes and they are why the order is a rule
rather than a preference: a value is refused where it is read, so a bad string
is reached before the missing field two hundred bytes later, and no reader can
report a derivation that it never got far enough to compute. **Within a stage
the order is declaration order and nothing recovers it but the corpus** — which
is why the corpus is where a pair goes, not a comment.

**What a decoder does not judge is as declared as what it refuses.** Inside an
opaque value ([DEF-A3]) the reader validates the shape of the JSON and nothing
else: escapes are not decoded, number spellings are not checked, and repeated
keys are not refused, because a document we are carrying is not a document we
are reading. `depth_limit` is the one rule that reaches inside, because it is
about the reader's own stack and not about the payload's meaning.

**The read grammar is wider than the write grammar everywhere, not only for
timestamps.** A generated decoder accepts any JSON whitespace, any field order,
and a document with or without the declared terminator. It writes exactly one of
those. Nothing is gained by refusing a record we can read, and refusing one is
how two correct implementations stop talking to each other.

### [DEF-S1] Service interfaces and protocol

Standard Thrift syntax declares named methods, independently of transport:

```thrift
service Events {
  oneway void Write(1: Record record)
  oneway void Ping()
  Record Read(1: string name)
} (wire_name="example.events/events@1", doc="Submit application events.")
```

The binding supports multiple services, one-way methods, typed request-response
results and required typed arguments. Omitted `required` is implied for method
arguments. Optional/default arguments and `throws` remain refused explicitly.
Go and C++ generate interfaces, transport-injected clients and pure dispatchers.
Python generates interfaces and clients; it does not generate a server runtime
or dispatcher. Rust and JavaScript refuse service-bearing definitions rather
than silently emit just data codecs.
The docs backend includes the method signatures and semantics.

Each call is JSON bytes, possibly containing whitespace/newlines and without a
transport delimiter:
`{"version":1,"service":"example.events/events@1","method":"Write","arguments":{"record":{...}}}`.
The generated binding owns these fields and the typed argument encoding. A
shared transport carries a complete opaque frame (length framing or equivalent); it supplies stream framing,
connections, deadlines and peer binding. It must consume or copy frame bytes
before returning. A client never opens a socket or a store.

For `oneway void`, success means local submission only, not remote acceptance or
durability. No response is awaited. Dispatch rejects unknown version, service
or method and decodes all arguments before calling any handler. Unknown envelope
and argument fields are refused. Duplicate keys follow the definition's policy;
duplicate declared fields are refused. Empty strings, false and zero remain
present arguments. Handler errors are local to the receiver; a transport must
not turn these into an implicit acknowledgement protocol.

### Namespaces

`namespace * oa.logging` supplies a common default; explicit language namespaces
override it. Dot-separated portable identifiers map to Go's final package
component and nested C++ namespaces. Namespaced outputs use paths such as
`go/oa/logging/rec.go`, `cpp/oa/logging/rec.h`, `py/oa/logging/rec.py`,
`js/oa/logging/rec.mjs`, and `rs/oa/logging/rec.rs`. Docs use
`oa.logging.schema.html` from the default namespace. No namespace preserves the
legacy `rec` names and paths. Namespace choice does not alter wire bytes.

A service requires an explicit nonempty `wire_name` annotation, unique in its
schema. This identity is independent of source namespace or class name, so two
capabilities can both call their API `Sink` without accepting each other's calls.
Optional `doc` annotations on service and method declarations are rendered as
escaped text in generated API reference pages. Unknown service/method
annotations are rejected. Method names are wire operation names.

Go clients receive structural `FrameWriter` and/or `FrameExchanger` interfaces.
C++ clients accept a transport with `WriteFrame(std::string_view)` and/or
`ExchangeFrame(std::string_view)` returning `std::string`, as their methods require;
one shared runtime can serve
multiple generated namespaces without handwritten per-capability subclasses.
Direct opaque JSON arguments retain their raw value tokens, including interior
whitespace; the protocol does not compact them. Typed document arguments retain
that document's validation, including vocabulary derivation before dispatch.
The current 32-argument bound follows the generated struct reader's 32-bit
presence mask, not a limit imposed by transport.

An explicit data-only `-only` selection may still target other code backends;
only a selected service requires service-capable generation.

Nested opaque JSON inside structured service arguments/results (and raw-value maps) is
currently rejected: the legacy document encoders reflow it despite DEF-A3.
Direct `json` arguments are supported with a service-specific verbatim carrier.
This restriction prevents silent mutation until nested opaque encoding is fixed
consistently; record-only selections keep their existing codec behavior.

### Request-response methods

A method without `oneway` uses `ExchangeFrame` and returns its declared type,
or no value for `void`. The transport associates exactly one response with each
exchange; it must serialize exchanges or provide equivalent correlation. There
is no request ID in this protocol. The binding does not implement retry,
cancellation, authorization or persistence guarantees.

The reply envelope is
`{"version":1,"service":"example.events/events@1","method":"Read","ok":true,"payload":{"value":{...}}}`.
Void success uses an empty payload object. Error replies set `ok:false` and use
`{"code":"denied","message":""}` as payload. Code must be nonempty; unknown codes
and empty messages survive roundtrip. Go returns `*ServiceError`; C++ throws
`ServiceError`. Providers may intentionally supply a code and public diagnostic.
Unexpected handler failures become `handler_error` with generic message
`handler failed`; invalid return values become `invalid_result`. Invalid error
diagnostics remain local codec refusals rather than being emitted.

Clients check version, service, method and the complete typed result before
exposing it. Wrong reply identity is `mismatched_response`; empty error code is
`invalid_error`. Dispatch validates arguments before invocation. Methods sent
through the wrong transport operation produce `wrong_mode`. Unparseable requests
and unknown envelope versions return local errors; associated request refusals
can be represented by error replies. Transport errors remain transport errors.
One-way requests retain their original wire format and have no acknowledgement.

Direct `json` results preserve raw tokens, as direct arguments do. Generated C++
dispatchers are pure codec/test infrastructure; no C++ IPC listening server is
supplied. Cross-language tests exchange complete buffers between a C++ client and
Go dispatcher; they do not prove an installed service.

### Required integer equality

A required `i32` or `i64` field may declare
`(equals="1", equals_refusal="bad_schema")`. The literal must be canonical signed
decimal within that integer type's range. The named refusal must be declared at
the `structure` stage. Both annotations are required together; other types and
optional fields are refused by the generator.

All five readers validate integer spelling/type/range and required presence
before checking equality at the struct boundary. Missing input remains
`missing_field`; an out-of-range integer remains `number_spelling`. A supplied,
well-typed unequal value uses the declared equality refusal. Nested records use
the same checks. Equality never supplies a default or fills an omitted field.

All five encoders refuse an unequal in-memory value before emitting that struct.
Go panics with `*Refusal`, C++ throws `Refusal`, Python and JavaScript raise/throw
`Refusal`, and Rust panics with a typed `Refusal` payload. Encoding offsets are
zero because there is no input byte position. Dynamic Python booleans are not
integers for this constraint. Generated API reference includes the literal and
refusal alongside field presence. Definitions without equality retain their
existing codec behavior and output.

### Python service clients

Python preserves schema method names (`Write`, `Echo`) and honors parameter
`python.name` aliases. `SinkClient(transport)` implements the generated `Sink`
interface. Its transport supplies `write_frame(frame: bytes)` for one-way calls
and/or `exchange_frame(frame: bytes) -> bytes` for request-response calls. No
native I/O or server is generated. An exchange must return its associated reply;
framing, concurrency, deadlines and connection identity remain transport duties.

Clients preserve direct opaque JSON tokens and zero/false/empty values, validate
arguments before calling transport, and validate reply identity and typed result
before exposing it. Raw results are bytes. `ServiceError.code` and `.message`
preserve unknown nonempty codes and empty diagnostics; malformed replies use
`Refusal`, identity/version errors use `DispatchError.code`, and transport
exceptions propagate unchanged. Python's coercions (such as bool-as-int or
fraction truncation) are not accepted as typed service arguments.

Python keywords, duplicate aliases and generated-name collisions are refused at
backend preflight before any output. Internal local names avoid parameter names.
Record-only selection retains the standalone codec output; support for zero-field
argument/result carriers also corrects previously invalid empty Python classes.
Tests exchange Python frames with a Go dispatcher over process buffers, including
logging's one-way Write; this does not claim a deployed IPC service.

### Binary values

`binary` is arbitrary octets: Go `[]byte`, C++ `std::vector<std::uint8_t>`,
and Python `bytes`. JSON represents it as an RFC 4648 standard-alphabet Base64
string with required padding and no whitespace. Readers reject malformed
alphabet/padding and nonzero padding bits with `bad_binary`; encoders emit the
canonical padded spelling. Empty binary encodes as `""`; JSON null is not binary.

For `optional binary data (omit="absent")`, missing and empty stay distinct:
Go nil versus non-nil empty slice, C++ optional vector, Python None versus b''.
A required binary value is always emitted, including empty. Required-field
absence remains `missing_field`. Binary is not a replacement for opaque JSON:
bytes have no internal document semantics. Binary collections are not yet in
the supported profile. Rust/JavaScript binary generation fails before output.


### Enum fields

A declared enum may be used directly as a required or optional record field,
including nested records, repeated records, service arguments and results. The
schema and API reference retain the enum's name. Codecs use string carriers:
Go `string`, C++ `std::string`, Python `str`, JavaScript string and Rust `String`.
JSON contains the exact member name, never the numeric member ID.

`unknown="refuse"` rejects unlisted names as `bad_enum` on decoding and encoding;
declare that refusal at the structure stage. `unknown="grant"` preserves any
string, including empty and future names. Non-string JSON remains `wrong_type`;
missing required fields remain `missing_field`. Validation follows required
presence at each record boundary. Encoding failures use the same language
mechanism and zero offsets as integer equality constraints.

`omit="absent"` retains absence separately from an empty string, using Go
`*string`, C++ `std::optional<std::string>`, Python `None`, JavaScript `null`,
and Rust `Option<String>`. An explicitly supplied JSON null is refused.
`omit="zero"` omits the empty carrier on output; an explicitly supplied name on
input is still validated. Enum collections themselves remain outside the profile;
use repeated records containing enum fields. Selections must include each enum
referenced by their selected records or services.

Existing vocabulary constants retain their names and bytes. When a Go enum
member owns the generated `EnumUnknown` name (for example member `unknown`),
policy metadata uses `EnumUnknownPolicy`, appending `Policy` until no member
collides. The member constant retains its exact wire name.
