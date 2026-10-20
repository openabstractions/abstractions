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
| **[DEF-F5]** `binary` on unsupported backends | Arbitrary octets, distinct from UTF-8 `string` and verbatim `json`. Go/C++/Python/Rust/JavaScript support it; unsupported backends refuse it explicitly. See binary values below. |
| **[DEF-F6]** unsupported cross-definition references | Qualified records, field/service enums and scalar typedefs from a direct include are supported. Scalar aliases retain their primitive, declared grammar and value constraints. Unqualified imported names, typedef chains, enum typedefs/maps, multi-hop qualified references, cyclic include graphs and unsupported backends are refused. Imported named grammars require a scalar field or method value; collection-element grammar projection is unsupported. Protocol outcome enums remain local. |
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

**[DEF-E6] `opaque`** — how a writer lays out an opaque value ([DEF-A3]).
Both values keep every string token, escape, number spelling, member and member
order exactly as they arrived, and both drop the whitespace around the value.
`"verbatim"` keeps the value's interior whitespace as the caller holds it.
`"reindent"` writes the value's insignificant whitespace again at the declared
`indent`, one member or element per line, so one value spelled on one line and
on six is written as the same bytes. A definition whose record files are
compared byte for byte between implementations declares `"reindent"`:
`abstraction-job/job.thrift` does, because JOB-J1 compares record bytes and
JOB-J7 leaves a payload's whitespace out. A service frame carries opaque bytes
verbatim ([DEF-S1]). A definition declaring `"reindent"` therefore declares
no service, and no service argument or return value reaches an opaque value
through a record a `"reindent"` definition owns; the generator refuses both.
`gen/job_reference_test.go` holds five generated job encoders to the two
hand-written job references.

**[DEF-E7] `terminator`** — `"newline"` ends the document with one LF,
`"none"` ends it with the closing brace. LF, never CRLF, on every platform.

**[DEF-E8] `duplicate_keys`** — `"refuse"` or `"last"`, what a reader does with a
repeated key of a map or a repeated member of an object inside a `json` value.
Under `"refuse"` either refuses with `duplicate_key`. Under `"last"` a map keeps
the final value, and a `json` value is carried as the bytes it arrived as, its
repeated members included; its consumer's parser decides which member it reads.

**[DEF-E9] `depth_limit`** — 1 to 1000, the deepest nesting a reader accepts,
counted from the document object and *including* nesting inside an opaque value.
A reader with no limit is a denial of service on untrusted input, and a reader
whose limit is its own stack is a limit nobody can write down.

### [DEF-A1] `(document = "true")`

Exactly one struct carries it. It is the type `encode` takes, and the thing the
encoding declaration describes.

### `schema_members = "<min>..<max>"`

A struct may declare JSON Schema property-count bounds with `schema_members`,
for example `(schema_members = "1..1")`. It uses the canonical non-negative
count grammar of [DEF-A11]: either bound may be open, at least one is present,
and min is at most max. The JSON Schema generator emits `minProperties` and
`maxProperties` for the declared bounds, including a referenced record's schema.
The declaration applies only to that struct; `document = "true"` identifies the
encoding root and carries no property-count rule.

This is a schema declaration. Generated codecs retain their existing behavior;
owning readers enforce their contract's member-count rule. FAC-M33 schemas
require a generator supporting this annotation. Earlier generators may ignore it
and provide no proof of the declared count. Fields, enums and other declarations
cannot carry `schema_members`.

### [DEF-A2] `(<language>.name = "...")`

The wire name is the field's name and never moves. This changes only the
identifier the named backend emits — `(cpp.name = "requires_")`, because
`requires` is a keyword from C++20 on and the record has a field called
`requires`. Unrecognised language prefixes are ignored, so adding a backend
cannot invalidate an existing definition.

### [DEF-A3] The `json` type

One syntactically valid JSON value, **validated in full and carried as the bytes
it arrived as**: not re-escaped, its number spellings and its key order
preserved exactly as they arrived. Its whitespace follows the definition's
`opaque` layout ([DEF-E6]). The escape
*policy* in [DEF-E2] applies to strings **this definition owns** and never
inside a `json` field; the JSON *grammar* applies everywhere, at every depth.

So a reader checks escapes, delimiters, separators, literals, number grammar,
control characters, UTF-8 and the declared depth limit inside a `json` value,
and it checks nothing about what the value means. Every restriction the encoding
block declares reaches inside, as [DEF-E8] states for each `duplicate_keys`
policy, because a payload
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

For `i32`, `i64` and `bool`, `omit = "absent"` retains a present `0` or
`false`. Go uses pointers, Python uses `None`, JavaScript uses `undefined`,
Rust uses `Option<T>` and C++ uses
`std::optional<T>`. A missing member decodes as absent; an explicit JSON `null`
is refused like an optional record. Declared range constraints apply to every
present value, including zero, and skip an absent value. Required scalars and
scalars with `omit = "zero"` keep their value representation. JavaScript
encoders refuse a present `null`. Changing a published field's omission rule
requires its service's version window under CONV-V1.

**And a collection field may only be `omit = "zero"`**, which is the mirror
image and was added 2026-09-08 because the profile accepted the other half in
silence. No backend can tell an empty collection from a missing one: there is no
`Option<Vec<T>>` in any of the five and inventing one would put a second
absence in a profile that refuses unions for having one ([DEF-F2]). Before this
rule a `list<string>` marked `omit = "absent"` parsed and emitted a Go presence
test comparing a slice against the empty *string* — a definition the generator
accepted and the compiler had to catch.

### [DEF-A5] Per-member enum annotations, and `(unknown = ...)`

An enum declares named members of a wire vocabulary. Every member may carry arbitrary
annotations, and the enum body carries `(unknown = "grant")` or
`(unknown = "refuse")` — what a reader does with a member it has never heard of.

The unknown-member policy applies during encoding and decoding. `grant`
preserves an unknown word; `refuse` rejects it. Authority and supported request
semantics are enforced by the service before it performs work.

The enum body may also carry `(doc = "...")` for descriptive documentation.
The documentation generator links a heading only when that enum's own `doc`
contains a canonical public-definition link with the exact enum name as its
label. The heading retains its ID and escaped symbol text. Documentation changes no member spelling,
unknown-member policy, authority or wire-compatibility identity.
This annotation is supported by the 0.8.0 development profile. Earlier
generators accept only `unknown` and `reader` on an enum body and refuse an
input containing `doc`; existing published archives remain unchanged.

The `reader` annotation records what happens after decoding. `display` carries
unknown words for presentation. `validate` carries them to a service boundary
that must reject unsupported values before effects. Both require
`unknown="grant"`. `act` identifies a word used directly to select behavior and
requires `unknown="refuse"` so an unknown word never selects an action.

Enums generate the language's native vocabulary form, its member list and one
table per annotation key. The unknown-member policy is a property of the
generated type and is stated in its documentation; no constant spells it,
because a constant named like a member compares with a value and is always
false. Go emits a closed vocabulary as a numeric named type whose tags have no
wire meaning; `String`, `WireName` and `Parse<Enum>` map it explicitly to the
member word. An open Go vocabulary remains a string type so a reader can keep a
word it has never heard. Both expose typed member constants,
`<Enum>Values()` returning a new slice of the members, and `Known()`.

A member may set `wire="..."` when its exact JSON string is not a language
identifier:

```thrift
enum Transport {
  1: native(wire="oa-native@1")
}(unknown="refuse")
```

`native` remains the logical member name used to generate constants and enum
variants. The parser, encoder, standard Go text/JSON methods, member value and
enum collections use `oa-native@1`. Without `wire`, the logical name remains
the wire string. Wire spellings must be nonempty and unique within the enum;
the parser rejects duplicate effective spellings before generation. `wire` is
semantic metadata and is displayed in enum documentation rather than emitted
as a member annotation table.

### [DEF-A6] `const list<i32>`, `const list<string>`, `const i32`, `const i64` and `const string`

A closed list, generated once per language instead of transcribed once per
language. The ten status codes that mean *no, permanently* were written by hand
in three languages and disagreed by four rows.

    const i32 ipc_default_call_budget_ms = 5000

A scalar `i32` or `i64` is one canonical decimal within its width, printed as
Go `const IPCDefaultCallBudgetMs int32 = 5000`, Python
`IPC_DEFAULT_CALL_BUDGET_MS = 5000`, C++ `inline constexpr std::int32_t
kIpcDefaultCallBudgetMs = 5000;`, Rust `pub const IPC_DEFAULT_CALL_BUDGET_MS:
i32 = 5000;` and JavaScript `export const ipcDefaultCallBudgetMs = 5000;` (an
`i64` is a `BigInt` there). The transport's status codes, frame bounds and
call budget are declared this way in `abstraction-identity/identity.thrift`,
and every hand-written IPC binding reads them from its generated artefact.

A scalar `string` is one UTF-8 JSON quoted literal, decoded once by the
compiler. Each binding emits that value as its language's immutable string
constant. Escaped quotes, control characters, backslashes and Unicode keep the
same bytes. Existing annotation and list-string literals retain their spelling.

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
follow the opaque-JSON rule ([DEF-A3], [DEF-E6]): string escapes, number
spellings, members and member order are retained byte for byte, whitespace
around the value is dropped, and interior whitespace is kept under
`opaque = "verbatim"` and laid out again under `"reindent"`. Unknown keys use the
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

### [DEF-A11] Declared value constraints: `length`, `range` and `pattern`

    1: required string request_key (length = "1..128", pattern = "no_control")
    2: required i64 wait_ms (range = "0..30000")

A field or a method argument declares the bounds its owner enforces, once, and
every generated reader and writer checks them. The names are Smithy's
constraint traits; the departures are listed in [LINEAGE](LINEAGE.md).

- **`length = "<min>..<max>"`** bounds a `string` in UTF-8 bytes, a `binary`
  in decoded bytes, a `json` value in the bytes of the value as held, and a
  `list` or `map` in entries. Either side may be left open (`"..64"`,
  `"1.."`); both are canonical non-negative decimals and min is at most max.
- **`range = "<min>..<max>"`** bounds an `i32` or
  `i64`, inclusive, in the same spelling with signed decimals inside the
  field's width.
- **`pattern = "<class>"`** requires every character of a `string` to belong
  to a named class: `no_control` refuses U+0000 to U+001F and U+007F to
  U+009F, the Unicode control characters; `ascii_name` admits only ASCII
  letters, digits, `_`, `.` and `-`; `ascii_lower_token` admits lowercase ASCII
  letters, digits, `_`, `.`, `:` and `-`. The empty string belongs to every class.

A required field is always checked. A scalar with `omit = "absent"` is checked
when present, including a present zero, and skips an absent value ([DEF-A4]).
Any other optional field holding its type's zero
(an empty string, binary, list or map, or `0`) carries nothing and is not
checked, which is the one reading Go, Python, C++, JavaScript and Rust share
for an omitted value. A violation reads **`bad_value`**, a structure-stage
word the definition declares in its refusal block whenever one field declares
a constraint; the generator refuses the definition otherwise, and refuses a
constraint the field's type cannot carry. All five readers check at the
record's boundary, after `missing_field` and in field order, so a decoder
refuses an out-of-bound value before a dispatcher calls a handler. All five
writers refuse before they emit the record, so a generated client refuses an
out-of-bound argument before its transport is called (abstraction-ipc
`IPC-A6`): Go panics with `*Refusal` and its clients return it as the call's
error, C++ throws `Refusal`, Python and JavaScript raise or throw `Refusal`,
and Rust's clients return `CallError::Refusal` from the same check its record
writer panics on. A reader's offset is the end of the record and a writer's
is zero, as for required integer equality below. The reference page states each bound beside the field's presence, and the
JSON Schema output states ranges, entry counts, patterns and a string's byte
bound as the character bound it implies.

A declaration records a bound the owning service already enforces. A new or
narrower bound on a published field is a wire change and waits for that
service's next version ([DEF-V1]).

A local `typedef string Name (length="1..128", pattern="no_control")`
declares these same constraints once for every scalar use and every element
of an already-supported `list<Name>` or string-map key/value position.
The wire and native carrier remain strings. A scalar field's own length
intersects with the named length; contradictory bounds refuse the definition.
Patterns both apply. Present elements are checked even when empty; an optional
scalar retains the existing zero/absence rule. Typedefs accept only `read`,
`write`, `length` and the existing `pattern` classes; unsupported or malformed
annotations refuse. Qualified scalar typedefs from a direct include retain these
constraints at the same supported positions. Unqualified imported names, typedef
chains, enum typedefs and new container forms remain
outside this profile.

### [DEF-A12] The large-field rule, and `(chunk = "true")`

Bytes do not travel through a record: a large value is a reference to content
a store holds. Every `binary`, `string`, `json`, `list` and `map` field and
method argument declares a `length` ([DEF-A11]); a `string` typedef with a
read and write grammar ([DEF-G1]) is bounded by it, and an enum by its longest
word. From the bounds the generator computes each record's largest encoding,
escapes and indentation included, and refuses a record, or a method's
argument set, whose largest encoding exceeds 1,048,576 bytes, one frame's
default (abstraction-ipc `IPC-F1`), unless the record says `chunk = "true"`:
one bounded piece of a stream a reference names. A record holding a value
without a bound has no largest encoding, and `idl/inventory.py` rule R9 lists
every such field: the list in `idl/contract_rules.recorded` only shrinks, and a
new field without a bound fails the inventory.

**A struct or document may declare `max_encoded = "<n>"`.** The 2026-11-02
architectural ruling uses this aggregate ceiling for legitimately composed
records whose independent field maxima exceed a frame. `<n>` is a canonical
positive decimal at most 1048576 (IPC-F1's default frame). The definition
declares `encoded_size(stage="structure")`, the typed refusal for exceeding it.

The ceiling counts the record **compact value span**, from its opening `{`
through its closing `}`, excluding insignificant JSON whitespace outside
strings. UTF-8 bytes and whitespace inside strings count. This count is
independent of nesting depth. Writers count newly emitted bytes while writing;
readers count actual input bytes during a pre-scan before decoding members,
without normalizing or re-encoding. Escaped strings retain their actual byte
spelling in the reader's count.

Annotated writers and readers independently enforce a **raw value-span guard**
of 1048576 bytes, including interior whitespace. It bounds reader whitespace
scanning work and keeps successful emitted spans readable. The scan allocates
no decoded content and retains the definition's depth limit. Leading/trailing
document whitespace and the declared LF terminator are outside both spans.
Document wrappers and transports still own their raw-document/whole-frame
bounds, including envelope overhead; a record guard proves no envelope fit.

Only the annotated record suppresses its own static worst-case failure.
Every parent's proof retains each child's full physical worst case at its
actual nesting depth, ignoring child annotations. A parent needs its own
annotation if its composed proof fails. Compact ceilings never substitute for
physical size in that proof. Per-field caps and R9 remain mandatory. A record
cannot combine `max_encoded` with `chunk="true"`; stream chunks keep their
separate semantics. Unannotated oversized records keep the static refusal,
and unannotated generic codecs remain unchanged. A service maps only the typed
`encoded_size` refusal to whole-response
`unavailable` / `bound:reply_bytes`; it never truncates a record. Adding or
narrowing an aggregate ceiling on a published shape requires its successor
window; widening or removing one preserves compatibility. Unannotated
definitions retain their existing hashes and generated bytes.

Restoration of an accounted immutable published shape under [DEF-V1] can expose
existing rows hidden by a rejected narrowing change. An explicit compatibility
disposition records only those inherited rows with publication/source provenance;
the default refusal of new unlisted findings remains. This accounting changes
no decoder acceptance and establishes no bounds compliance.

A `length` on a list or map
counts entries; a local string typedef's finite length bounds its elements or
string-map keys/values. Plain `string`, `json` or `binary` elements stay open. R9 lists each
such collection as `<field>[]` whatever its declared entry count, and a new one
fails the inventory until its element is bounded (W1-23a retains the remaining
element forms). `gen --sizes <definition>` prints each record's largest
encoding, or the first value without a bound.

GeneratedRecords1722 explicitly accounts the private Go legacy/proposal input
profile's like-for-like copies of already recorded Job Record fields and its
existing opaque-value grammar in `contract_rules.recorded`. This migration of
handwritten accepted input adds no RPC root or logical payload capacity. The
recorded provenance names the canonical fields and frozen source; unlisted new
unbounded declarations continue to fail R9.

### [DEF-A13] Secret and local-only field metadata

A field may declare `secret="true"`, and independently
`local="path"`, `local="pid"` or `local="handle"`. Other and empty values
refuse. These are compiler/inventory metadata and change no codec or wire
field. The owning contract declares the semantics; names alone supply none.
R12 applies DEF-S4 to every reachable field, including optional/container
members. Credentials raw secret values and applied headers remain local.

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

## Public surface

A generated module's public surface is what an application or a service
implementation names: records, vocabularies with their wire-name conversions,
declared constants, `Raw`, the errors a call raises (`Refusal`, `ServiceError`,
`DispatchError`), document codecs, service interfaces, clients, dispatchers, and
`service_name` where one namespace holds several services. Everything else the
generator emits is codec machinery and is private: the refusal words in their
ranked order and `refusal_rank`, the timestamp predicates, the vocabulary
`member` predicate, readers, writers and envelopes.

An item code outside the module genuinely needs lives in the language's
explicit internal place, never on the public surface. Two needs exist today:
the cross-file record codecs an including definition calls, and the timestamp
and `member` predicates the conformance drivers under `idl/test` probe.

| Language | Internal place | Today |
|---|---|---|
| C++ | `namespace detail` | `detail::micros_timestamp`, `detail::decode_ref_at` |
| Rust | `pub mod internal`, wrappers over private items | `internal::micros_timestamp`, `internal::member`, `internal::decode_ref_at`, `internal::check_ref` |
| Python | the `_codec` module, under a `_` name the package does not re-export | `_codec._micros_timestamp`, `_codec._decode_ref_at` |
| JavaScript | `internal.mjs` (the `/internal` package subpath), not re-exported by `index.mjs` | `microsTimestamp`, `member`, `decodeRefAt` |
| Go | unexported | `microsTimestamp`; `DecodeRefAt`/`EncodeRefAt` stay exported because Go has no internal visibility across modules, and an including package in another module calls them |

`scripts/idiom_check.py` refuses each private helper name on a public surface,
and the surface snapshots under `idl/gen/testdata/surface/` record the rest.

Every binding that declares the `wrong_type` refusal exports one record codec
(CC-8). It writes one record as JSON laid out as the definition's documents
are (the declared `indent`, opaque values laid out per [DEF-E6]) and without
the document terminator. It reads one record and refuses what the record's
reader refuses, `trailing_bytes` included.

| Language | Write | Read | A value that is not a record of the module |
|---|---|---|---|
| Go | `ToJSON(v any) ([]byte, error)` | `FromJSON(data []byte, v any) error` | `*Refusal` with `wrong_type`, a nil record pointer included |
| Python | `to_json(value) -> bytes` | `from_json(kind, data)` | `TypeError` |
| JavaScript | `toJson(kind, value)` | `fromJson(kind, data)` | `TypeError` |
| Rust | `JsonRecord::to_json(&self)` | `JsonRecord::from_json(data)` | refused at compile time |
| C++ | `to_json(const T&)` | `from_json<T>(data)` | refused at compile time |

Go has no generic record type to check at compile time, so a foreign value
is a refusal there; Python and JavaScript raise the language's own type error.

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
its fixed standard-library imports and explicit included-definition imports.
Go package paths come from build configuration; C++/Python use the included
namespace. JavaScript module specifiers come from explicit `--js-import` mappings.
Rust crate paths come from explicit `--rust-import` mappings. Emitting an undeclared import
fails the generator's own build. A module that cannot name a socket, a clock or
a filesystem cannot reach one, and widening that list is how a backend would
have to admit it was trying to.

## The refusal vocabulary

A decoder's public surface is its refusals, so they are a closed list, and they
are declared in the definition ([DEF-R1]) rather than transcribed into five
backends. Seventeen words; five implementations; the same word at the same byte
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
| `duplicate_key` | grammar | a repeated map key or object member inside a `json` value, under `duplicate_keys = "refuse"` ([DEF-E8]) |
| `duplicate_field` | structure | one field name twice in one struct |
| `unknown_field` | structure | a field name in a scope declared `unknown_fields = "refuse"` ([DEF-A7]) |
| `missing_field` | structure | a `required` field absent |
| `bad_enum` | structure | a word a closed vocabulary does not list, `unknown = "refuse"` (Enum fields below) |
| `bad_binary` | structure | Base64 that is not the canonical padded spelling (Binary values below); `abstraction-cas` and `abstraction-inference` declare it at the grammar stage |
| `bad_value` | structure | a value outside its declared `length`, `range` or `pattern` ([DEF-A11]) |
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
opaque value ([DEF-A3]) the reader validates JSON grammar and the declared
`depth_limit`. Repeated object members follow [DEF-E8]: `refuse` rejects them
and `last` retains their bytes. String escapes and number spellings remain in
the carried value; its consumer reads their meaning.

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
results and required typed arguments.

`(error_codes = "<const>")` on a service names a `const list<string>` whose
distinct snake_case words are the codes its handlers send on the reply error
channel. A result without an `outcome` field refuses through that channel. The
generated `ServiceErrorCode` vocabulary, open, holds the dispatcher's own codes
(`handler_error`, `invalid_result`, `unknown_version`, `unknown_service`,
`unknown_method`, `wrong_mode`) and then every declared code, and Go types
`ServiceError.Code` with it. The name `ServiceErrorCode` is reserved. Omitted `required` is implied for method
arguments. Optional/default arguments and `throws` remain refused explicitly.
Go and C++ generate interfaces, transport-injected clients and pure dispatchers.
Python and Rust generate interfaces and transport-injected clients. Their
generated output contains no server runtime or dispatcher. Rust clients use a
`FrameTransport` trait with an associated transport error; `CallError` retains
transport failures, codec refusals, envelope errors and service code/message.
Transport adapters own waiting budgets and cancellation. Generated calls never
retry, and a transport error leaves receiver acceptance unresolved. One-way
completion confirms local submission only. Rust `--no-ipc` emits capability
traits and record codecs. Rust typed includes reference dependency crates named by `--rust-import`; `--named-codecs` exports `encode_x_document` and `decode_x_document`, and puts `encode_x_at`, `decode_x_at` and `check_x` in the crate's `internal` module.
JavaScript generates typed asynchronous service clients and no dispatcher.
The `jsonschema` output, named explicitly, writes one JSON Schema (draft
2020-12) document per record, `jsonschema/<namespace>/<Record>.schema.json`,
each self-contained with the records it references in `$defs`, and an
`index.json` listing them. It states members, required fields, closed
objects, closed enum words, integer ranges and opaque values as any JSON.
For a declared finite vocabulary it also states each existing derivation
predicate, known critical words and the critical subset rule with standard
conditionals and contains. Unknown noncritical names remain valid, and
strip_critical words keep their reader allowance before the subset check.
The generated readers stay the authority for duplicate members, `depth_limit`,
number spellings and timestamp grammar. Unpaired-surrogate escape spelling
also remains a reader grammar check when a JSON loader repairs it to U+FFFD:
a schema sees the parsed instance and cannot distinguish that repair from a
valid U+FFFD. This exception covers that lost lexical information; valid
paired Unicode and other malformed-string refusals keep their existing rules.
`gen --backends` prints what each output supports:

<!-- gen --backends: begin; regenerate with GOWORK=off go -C idl/gen run . --backends -->
| Backend | Records | Services | Clients | Dispatchers | `--no-ipc` | Includes and `--named-codecs` | `binary` |
|---|---|---|---|---|---|---|---|
| go | yes | yes | yes | yes | yes | yes | yes |
| python | yes | yes | yes | no | yes | yes | yes |
| cpp | yes | yes | yes | yes | yes | yes | yes |
| javascript | yes | yes | yes | no | yes | yes | yes |
| rust | yes | yes | yes | no | yes | yes | yes |
| docs | documented | documented | no | no | documented | documented | documented |
| jsonschema | documented | no | no | no | documented | documented | documented |
<!-- gen --backends: end -->

The `docs` output includes the method signatures and semantics.

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
`go/oa/logging/rec.go`, `cpp/oa/logging/rec.h`, `py/oa/logging/__init__.py`
with its private `_codec.py`, `js/oa/logging/index.mjs` with its `internal.mjs`,
and `rs/oa/logging/rec.rs`. Docs use
`oa.logging.schema.html` from the default namespace. Without a namespace the
outputs sit directly under each language directory: `py/__init__.py`,
`js/index.mjs`, and the `rec` names and paths elsewhere. Namespace choice does
not alter wire bytes. Identifiers follow each language's convention (Python
snake_case members and CapWords classes, JavaScript camelCase members and
PascalCase classes); a field's `python.name` or `javascript.name` annotation
overrides the mapped spelling, and the wire name never changes.
`--namespace=<language>=<dotted.name>` on one generator run places that
artefact's output for one language under another namespace, so a selected
subset of a definition (`-only=`) can live inside the package that uses it:
the IPC bindings carry identity's transport constants under
`abstraction.ipc.constants`.

Rust, C++ and Go follow the same rule with their own conventions. Rust uses
snake_case functions, methods, fields and parameters, UpperCamelCase types and
enum variants, and SCREAMING_SNAKE_CASE constants. C++ uses lowercase
namespaces, PascalCase types, snake_case functions, methods, parameters and
members, PascalCase enumerators in `enum class`, and `k`-prefixed PascalCase
constants; codec helpers, carriers and envelopes live in `detail`. Go exports
PascalCase names with initialisms in capitals (`OperationID`, `TTLMs`,
`SHA256Digest`), names parameters after the contract in lowerCamelCase, and
leaves helpers and envelopes unexported. A `rust.name`, `cpp.name` or `go.name`
annotation wins over the mapping. A C++ identifier, Go parameter or Rust method
name that maps to a keyword takes a trailing underscore. In a definition with
services, a Rust record field or method argument that maps to a keyword is
refused until it carries `rust.name`. A closed vocabulary (`unknown="refuse"`) is a Rust
`enum`, a C++ `enum class` and a Go numeric named type with explicit wire-name
mapping; an open vocabulary stays a string with named constants for its known
words.

A service requires an explicit nonempty `wire_name` annotation, unique in its
schema. This identity is independent of source namespace or class name, so two
capabilities can both call their API `Sink` without accepting each other's calls.
Optional `doc` annotations on service and method declarations are rendered as
escaped text in generated API reference pages. Unknown service/method
annotations are rejected. Method names are wire operation names.

Go clients receive structural `FrameWriter` and/or `FrameExchanger` interfaces.
C++ clients accept a transport with `write_frame(std::string_view)` and/or
`exchange_frame(std::string_view)` returning `std::string`, as their methods require;
one shared runtime can serve
multiple generated namespaces without handwritten per-capability subclasses.
Direct opaque JSON arguments retain their raw value tokens, including interior
whitespace; the protocol does not compact them. Typed document arguments retain
that document's validation, including vocabulary derivation before dispatch.
The current 32-argument bound follows the generated struct reader's 32-bit
presence mask, not a limit imposed by transport.

An explicit data-only `-only` selection may still target other code backends;
only a selected service requires service-capable generation.

Opaque JSON nested inside structured service arguments and results (including
raw-value maps) travels verbatim, as direct `json` arguments do. Under
`opaque = "verbatim"` every record encoder (document, `to_json` and service
frame) writes each opaque value's bytes as the caller holds them, at any record
depth ([DEF-A3], [DEF-E6]): interior whitespace, escapes, number spellings,
members and member order are kept, and whitespace around the value is dropped.
A `"reindent"` definition carries no service. Every reader still validates the value in full.
Under `duplicate_keys = "refuse"`, a duplicate member at any depth is refused;
nesting past `depth_limit` is refused under either policy, before a request
leaves and when a reply arrives, identically in Go, C++, Python, Rust and
JavaScript. A definition whose `depth_limit` is past 256 gets a
Python reader that walks opaque values with an explicit stack, so the refusal
is `depth_exceeded` and never Python's recursion limit. A record another
`"verbatim"` definition owns carries its opaque values the same way; one a
`"reindent"` definition owns is refused in a service, because its encoder lives
in that definition's generated package and lays the value out again.

### [DEF-S2] The endpoint description every dispatcher answers

Every definition with a request-response service also emits, in Go and C++,
the base-protocol service `abstraction.facade/endpoint@1` `Describe`, declared
in `abstraction-facade/facade.thrift`. A dispatcher that exchanges frames
answers a Describe frame itself, listing its own service. An endpoint hosting
several services answers it with Go `DescribeEndpoint(frame, program, version,
dispatchers...)` or C++ `describe_endpoint(frame, program, version,
dispatchers...)`, which accept dispatchers of any generated package in the
order given. Each dispatcher reports `DescribeService()` (C++
`describe_service()`): its wire name, and `ready` unless its handler has the
optional hook `Ready() (bool, string)` (C++ `ready()` returning a pair of bool
and string on the handler's own type) reporting otherwise with a reason. The
reply is written with the shape facade.thrift declares, so no generated package
depends on the facade's. `guarantees` and `capabilities` are empty in generated
replies. A frame for any other undeclared service still reads
`unknown_service`, and a non-empty argument object reads `unknown_field`.

A Go host serving several services routes each request-response frame with
`ServeEndpoint(frame, program, version, dispatchers...)`. It answers Describe
for all of them, hands any other frame to the dispatcher whose
`ServiceContract()` equals the frame's service, reads `wrong_mode` for a
dispatcher without request-response methods, and reads `unknown_service` for a
service it does not host. A host that picks a dispatcher by name by hand must
still send `abstraction.facade/endpoint@1` frames to one of these calls. The
names `EndpointContract`, `DescribedService`, `DescribeEndpoint`,
`ServedService` and `ServeEndpoint` are reserved. Python, Rust and JavaScript generate no dispatcher; they reach the
service through the facade definition's generated `Endpoint` client.

### [DEF-S3] Method traits

    Record Read(1: string id) (readonly = "true")
    Receipt Submit(1: Job job) (idempotency_key = "job.key")

A method says how a repeat of it recovers after a lost reply
(abstraction-ipc `IPC-I1`, CONVENTIONS.md `CONV-I2`): `readonly = "true"`
changes no requested domain state, `idempotent = "true"` has the first call's effect when
repeated with equal arguments, and `idempotency_key = "<argument>"` or
`"<argument>.<field>"` names the caller-owned key, a required string, that
makes a repeat safe. A method declares one trait.
`idempotent = "false"` explicitly declares no idempotency or replay guarantee;
it has the same unsafe runtime trait as an undeclared method and cannot be
combined with another trait. It authorizes no automatic retry. The generator refuses a
trait it does not define and a key that names no required string argument or
record field; the reference page states each trait. Generated clients retry
nothing whatever the trait says (`IPC-I2`). `idl/inventory.py` rule R10 holds
every method to a trait: the methods that declared none when the rule came are
its shrink-only list in `idl/contract_rules.recorded`.

A caller MAY repeat a `readonly` call after an unknown outcome, and an
`idempotent` call with the same arguments when no later call of its own
changed the same state (`IPC-I2`). A keyed call is reconciled through its key
and never blindly resent.

Readonly describes the requested domain effect. Incidental read-audit,
diagnostic recording and cache maintenance are permitted. Model/provider
actuation, registry/policy/grant/budget mutation, accepted work, leases and
operation-lifetime changes are excluded. Per-request logging follows the
safe-method distinction in [RFC 9110 sections 9.2.1–9.2.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.1).
This permits Router.Pick's ROUTE-U1 audit; it does not cover [Chat.Observe](https://openabstractions.github.io/reference.html#concept-inference.chat-service)'s
explicit idle protection. Generated clients continue to retry nothing.

### [DEF-S4] Remote method metadata

`remote="local|see|call|delegate|person"` belongs to a method or service.
The method overrides its service default; absence means `local`. Empty,
unknown and duplicate explicit values refuse. The words belong to this IDL
annotation family (C-69), and grant no authority. Rights and the receiving
transport remain responsible for admission. `see` requires `readonly`;
`call` promises no replay guarantee and retains R10's existing trait debt;
`delegate` requires keyed recovery and a required return-value path to the actual
Job owner's `acceptance.thrift` [Receipt](https://openabstractions.github.io/reference.html#concept-job.acceptance-receipt) identity through a required scalar
return-value path, including required record wrappers. Existing typedefs rename
scalars; a typedef of a Receipt record remains a compiler refusal. Optional-only
and collection-only receipts do not establish that identity. The receipt
binds RequestIdentity, logical_owner and operation_id; a same-named foreign
record does not qualify. No delegate/person method is promoted here.

R12 follows every argument and return value through owner-qualified includes,
typedefs, all record fields including optional fields, lists and map keys
and values. It refuses every nonlocal method reaching a field marked
`secret="true"` or `local="path|pid|handle"`, reporting the full chain.
Cycles do not hide sibling fields. These markers state owning-contract
semantics; arbitrary message text and opaque bytes receive no inferred
classification from their contents or field names.

Generated Go dispatchers offer optional `RemoteMethodProfile` metadata;
the lookup returns local for missing/unknown metadata. Required ServedService
and closed endpoint@1 [Description](https://openabstractions.github.io/reference.html#concept-facade.endpoint-description)/[ServiceState](https://openabstractions.github.io/reference.html#concept-facade.endpoint-service-state) remain unchanged, as do their
wire hashes. Generated source must be regenerated and rebuilt. Wire policy
descriptions require their separate IPC-V3 version window.

### Request-response methods

A method without `oneway` uses the exchange operation (Go `ExchangeFrame`, C++
`exchange_frame`) and returns its declared type,
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

### Service reply postconditions

A reply method may declare `(result_present="state", result_when="outcome",
result_is="ok")`. The reply value must be a record. `result_present` names one
optional `omit="absent"` field; `result_when` names a required sibling enum;
`result_is` is one declared member's exact wire spelling, or a comma-separated
set of declared wire spellings, such as `"pending,answered"`. An exact declared
wire spelling takes precedence, preserving existing single-member definitions
whose spelling contains a comma. Empty, duplicate
and undeclared members are refused. The set lowers in enum declaration order.
All three annotations
are required together. The payload is present **if and only if** the enum holds
any member of that set. No paths, expressions, defaults or implicit retries are supported.

The compiler resolves this relation once and emits it at RPC reply boundaries
in all five languages. A client validates after structural reply decoding and
before return; generated Go/C++ dispatchers validate a successful handler reply
before sending it. A mismatch is the existing generated `invalid_result` service
error. Ordinary structural/enum failures retain their existing handling.

Before that client semantic refusal, generated clients clear the freshly decoded
mutable buffers of logical `binary` fields, including existing nested records,
record lists and optional values. Successful returned values keep their bytes.
Input/request and transport-frame ownership stays with its existing caller or
transport owner. Dispatcher handler results retain provider ownership and are
never cleared by this client-only operation. Immutable Python bytes and logical
JSON/string fields are excluded. Other heap copies may remain after this
mutable-buffer clearing operation.

This is non-wire method behavior metadata, like observation traits. Standalone
record readers/writers keep their structural accepted language and refusal
tables. The canonical wire definition hash and shape compatibility exclude this
metadata; source/producer qualification binds the generated behavioral change.
Field names/order/types, method signatures and valid reply bytes are unchanged.

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

Python spells schema methods in snake_case (`Write` is `write`, `DecideFor` is
`decide_for`) while the frame keeps the schema method name, and honors parameter
`python.name` aliases. Records are keyword-only dataclasses and closed
vocabularies are string-valued `enum` members. `SinkClient(transport)` implements
the generated `Sink` interface. Its transport supplies `write_frame(frame: bytes)` for one-way calls
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

Duplicate aliases and generated-name collisions are refused at
backend preflight before any output. Internal local names avoid parameter names.
Record-only selection retains the standalone codec output; support for zero-field
argument/result carriers also corrects previously invalid empty Python classes.
Tests exchange Python frames with a Go dispatcher over process buffers, including
logging's one-way write; this does not claim a deployed IPC service.

### Binary values

`binary` is arbitrary octets: Go `[]byte`, C++ `std::vector<std::uint8_t>`,
Python `bytes`, Rust `Vec<u8>`, and JavaScript `Uint8Array` (including Node Buffer). JSON represents it as an RFC 4648 standard-alphabet Base64
string with required padding and no whitespace. Readers reject malformed
alphabet/padding and nonzero padding bits with `bad_binary`; encoders emit the
canonical padded spelling. Empty binary encodes as `""`; JSON null is not binary.

For `optional binary data (omit="absent")`, missing and empty stay distinct:
Go nil versus non-nil empty slice, C++ optional vector, Python None versus b'',
Rust Option versus an empty vector, and JavaScript null/undefined versus an empty
Uint8Array. JavaScript constructors use null for absent values; invalid binary
carriers throw `Refusal` with `wrong_type` before transport submission.
A required binary value is always emitted, including empty. Required-field
absence remains `missing_field`. Binary is not a replacement for opaque JSON:
bytes have no internal document semantics. Binary collections are not yet in
the supported profile. JavaScript binary codecs use portable byte operations and
canonical Base64 without requiring Node or browser-specific encoding helpers.


### Enum fields

A declared enum may be used directly as a required or optional record field,
including nested records, repeated records, service arguments and results. The
schema and API reference retain the enum's name. Codecs use string carriers:
Go `string`, C++ `std::string`, Python `str`, JavaScript string and Rust `String`.
JSON contains the member's exact wire spelling, never the numeric member ID.

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
input is still validated. `list<Enum>` carries an ordered JSON array of the exact
member strings. Its public type is `[]Enum` in Go, `std::vector<Enum>` for a
closed C++ enum, `Vec<Enum>` for a closed Rust enum, `list[Enum]` in Python and
`Enum[]` in TypeScript. Open C++ and Rust lists retain string elements; Python
and TypeScript expose the known member type plus future strings. Each element
uses the enum's unknown policy, so a closed list refuses an unlisted word as
`bad_enum` and an open list round-trips it. Collections use `omit="zero"`, and
selections must include each enum referenced by their selected records or
services.

Member constants retain their wire names. A Go member spelled `Values` is
refused, because `<Enum>Values` names the generated member list.


## Typed cross-definition records

`include "relative/model.thrift"` binds the filename stem `model`. A field or
service signature uses `model.Ref`; an unqualified `Ref` remains local. Paths
resolve from the including source file. The loader refuses missing files,
unresolved types, duplicate aliases and cyclic include graphs. Canonical file
paths identify memoized dependencies; include nesting is bounded at 64 definitions. The supported
subset imports record, enum and qualified direct-include scalar typedefs, including optional and repeated fields
and service arguments/results. Each imported enum keeps its owner's wire words
and unknown-member policy; native closed-enum types are aliases to that owner,
and open string carriers retain unfamiliar words. A selected artifact names
its local declarations; their imported record/enum dependencies stay imported.
Scalar aliases retain their owner's primitive, grammar and constraints; imported
named grammars require a scalar field or method value. Unqualified imported
names, typedef chains, enum typedefs/maps, multi-hop qualified references and
recursive record layouts remain unsupported and are refused. Protocol outcome
enums remain declared in the protocol's own definition.
Includes sharing a target namespace are ambiguous and refused.

Each included record keeps its generated language identity, encoding policy,
unknown-field behavior and refusal words. Generated bridges delegate to the
included codec, retaining raw extension values. They translate a dependency's
refusal into the enclosing codec's refusal carrier with its original word and
absolute input offset. Nesting depth and the tighter enclosing/included limit
continue across the boundary. Named encoding checks its output through the same
record decoder. The ordinary document encoder of an including definition uses
that validation too. Python named codecs check field types before encoding. Unknown fields are judged by the definition owning that record.

Generate each dependency with `--named-codecs` before compiling its consumers.
This exports `EncodeRef`/`DecodeRef` in Go and `encode_ref`/`decode_ref` in C++ and
Python for a record named Ref, alongside the existing document codecs. The `At`
(Go) or `_at` entry points compose records with explicit depth and limit;
decoding returns the consumed byte count. They exist for including generated
code and sit in each language's internal place (see "Public surface"): C++
`detail::decode_ref_at`, Python `_codec._decode_ref_at`, Rust
`internal::decode_ref_at`, the JavaScript `internal.mjs` subpath; Go exports
`DecodeRefAt`. Named top-level codecs retain
the definition's terminator. Consumers with includes also emit named codecs.
The generator emits each source independently; it does not copy dependency
records into the consumer or infer dependency versions.

For example, generate `model.thrift` and `request.thrift` with
`--named-codecs go cpp python docs`, then generate their consumer with:

```
gen resolver.thrift out --go-import=model=example.org/model/abstraction/model/api --go-import=request=example.org/download/abstraction/download/request go cpp python docs
```

Go mappings are explicit build metadata and supply the package identity for a
included definition without a namespace. C++ consumers add the generated `cpp/`
roots to their include paths; Python consumers install the generated `py/`
namespace trees. A selection lists local surfaces; imported record dependencies
remain linked through their owning modules. `--no-ipc` retains typed interfaces
and record codecs.

The current IPC envelope pre-scans payload JSON. An envelope declaring
`duplicate_keys="refuse"` cannot safely embed a dependency allowing last-wins
keys; generation refuses that combination with an explicit `--no-ipc` option.
Compatible policies support generated IPC. This restriction also covers
transitive dependencies. Direct record codecs can compose different policies.
Definitions without includes or `--named-codecs` retain their generated bytes.

## Versions

A service version is its `wire_name`. These two rules hold the versioning
convention every contract carries
([CONVENTIONS.md, C3](CONVENTIONS.md#c3-one-breaking-window-per-service)).

### [DEF-V1] Compatibility between a published definition and a candidate

    gen --compat <published.thrift> <candidate.thrift>

compares every service the published definition declares with the candidate's
service of the same `wire_name`, over the methods and every struct and enum
those methods reach, through nested records, repeated records and included
definitions. It refuses, naming the service, the declaration and the member:

- a published method that was removed or changed its arguments, their order,
  its result or `oneway`;
- a member added to a closed struct (`unknown_fields = "refuse"`) or a closed
  enum (`unknown = "refuse"`);
- a required member added to an open struct;
- a member removed or changed in any reached struct or enum, an enum member's
  wire spelling included;
- two members of any reached struct that changed their relative order, since a
  writer emits members in declaration order and a reader reports refusals
  within one stage in that order;
- a published error code removed from the service's `error_codes`;
- a changed unknown-member policy, document flag or encoding declaration;
- a first-added or narrowed `length` or `range` ([DEF-A11]) on a published
  member, argument or service answer, or a first-added or changed `pattern` class;
  inherited named-string constraints participate at the same scalar or
  collection-element position.

A first declaration adds a generated refusal for bytes a published writer
could send. It is narrowing even when an application previously enforced a
similar rule itself. A widened or removed bound is compatible.

A method added to a kept service version, an optional member added to an open
struct at any position, a member added to an open enum and an error code added
to `error_codes` are compatible. An error code joins the open
`ServiceErrorCode` vocabulary, whose reader carries a code it never saw. The
order of an enum's members is compatible, since an enum is written as its
member's word. A service the candidate no longer declares under the published
`wire_name` is not compared: it was retired or replaced by a new version. When
nothing is refused, the command prints each candidate service's definition
hash ([DEF-V2]) and exits 0.

### [DEF-V2] The definition hash

    gen --hash <definition.thrift>

prints one line per service, `<wire_name> sha256:<lowercase hex>`, in
declaration order. The hash is SHA-256 over the service's canonical wire form:
its `wire_name`, the encoding declaration, its error codes in sorted order, its
methods by name with their arguments in declaration order, and every struct and
enum the methods reach, by name. A struct's members appear in declaration
order and an enum's members by name, each with its presence, omission, grammar
and equality rules, unknown-member policy and wire spelling, and with its
declared `length`, `range` and `pattern` when it declares one; a definition
that declares none hashes as it did before [DEF-A11]. Comments, `doc`
annotations, language-name annotations, field ids, the order of declarations
and the order of an enum's members are outside it. Two definitions with the
same hash for a service put the same bytes on the wire for every call of it.
