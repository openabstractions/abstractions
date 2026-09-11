// The job abstraction, stated once, in a language that is nobody's language.
//
// This is the layer's ONE definition, in the profile described by
// ../idl/LANGUAGE.md. It is the input to ../idl/gen, which emits the encoder,
// the decoder and the wire envelope for every language, and the input to
// ../idl/fit and ../idl/test. Nothing here is prose about what a lease MEANS:
// the semantics live in CONTRACT.md, which an implementer reads alongside this.
//
// WHY THRIFT SYNTAX. Not because we intend to run its runtime, but because its
// architecture is the rule this project arrived at independently: an interface,
// a protocol (encoding) and a transport are three orthogonal things, swapped
// separately. Writing the contract in a notation built on that separation makes
// it hard to smuggle a binding back in.
//
// WHAT THIS FILE CANNOT SAY, and therefore what stays hand-written beside the
// generated code: that an epoch only increases, that a claim is exclusive, that
// a successor resumes only from a proven prefix, which content names a record
// derives from its own contents, the grammar a schema identifier is held to,
// and that every action name resolves to a schema the record declares. Those
// are CONTRACT.md's, and the conformance suite's. The last two are reader
// obligations, which is the class ../idl/LINEAGE.md says no schema language has
// a place for: a grammar in this profile means a timestamp and nothing else,
// and widening it is a change to five backends for a rule three hand-written
// readers must state anyway.

// The bytes. Every language writes exactly this or it is not an implementation.
//
// The escape setting below is [JOB-E6]: escape what JSON requires, U+2028 and
// U+2029, and nothing else. Go's default escapes & < > so that output is safe
// to paste inside a <script>; nothing reads a record that way, and while that
// default stood a download URL's query separator was spelled "&" by one
// implementation and "&" by the other two.
//
// The opaque setting is [JOB-E7] and [JOB-E8]: an opaque value is one
// syntactically valid JSON value, validated in full and carried as the bytes it
// arrived as. Validated, because a record is a UTF-8 file a stranger's parser
// reads and one illegal escape three levels down makes the whole file
// unreadable. Carried, because without it 1.50 comes back 1.5 and "a\/b" comes
// back "a/b" — somebody else's value, re-spelled in transit, in the one file
// three implementations compare byte for byte.
//
// Every setting here that restricts the record restricts an opaque value the
// same way, at the same depth: duplicate_keys, depth_limit and the string rules
// reach all the way down. A backend that relaxes one inside a payload has
// invented a second grammar nobody wrote.
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

// The word a reader says when it refuses, in the order two of them are chosen
// between. The word is the contract; the byte offset beside it is not.
refusal {
   1: malformed        (stage = "grammar")
   2: bad_string       (stage = "grammar")
   3: number_spelling  (stage = "grammar")
   4: wrong_type       (stage = "grammar")
   5: bad_timestamp    (stage = "grammar")
   6: depth_exceeded   (stage = "grammar")
   7: duplicate_key    (stage = "grammar")
   8: duplicate_field  (stage = "structure")
   9: unknown_field    (stage = "structure")
  10: missing_field    (stage = "structure")
  11: trailing_bytes   (stage = "document")
  12: unknown_critical (stage = "derivation")
  13: not_a_subset     (stage = "derivation")
  14: content_mismatch (stage = "derivation")
}

// An instant, in UTC with exactly six fractional digits and a trailing Z on
// the way out. Reading uses the profile's bounded RFC 3339 grammar: Gregorian
// dates in years 0000–9999, no leap seconds, and a representable UTC result.
// Writing converts offsets to UTC and truncates fractions beyond microseconds.
//
// Six digits, not nine, because Python's datetime holds microseconds and cannot
// represent nanoseconds: the contract is set by the least precise participant,
// not the most. Pinning the WIDTH is not pedantry — Go trims trailing zeros by
// default, so two conformant implementations wrote different bytes for the same
// instant and a job history stopped being diffable. Nothing failed; it rotted.
//
// Reading wider than we write is deliberate. Refusing to read a job over a
// timezone suffix another implementation chose would be absurd.
typedef string timestamp (write = "rfc3339-micros", read = "rfc3339-wide")

// What an implementation can promise beyond the base, and how a stranger could
// check the promise. An application asks; it does not assume. A pause button
// that silently does nothing is worse than no pause button.
enum Capability {
  1: resume                (assurance = "falsifiable")
  2: survives_process_exit (assurance = "recovered")
  3: verifies_content      (assurance = "checked")
  4: delegates             (assurance = "trusted")
} (unknown = "grant")

// How a call failed, in one word a caller can branch on. This replaces the
// seven `exception` types the sketch that preceded this file declared: a
// binding that carried them as text lost the only part of an error that
// mattered, which is the part `errors.Is` reads.
//
// `transcript` is how a conformance driver spells the same refusal in the
// transcript conformance/DRIVER.md defines. It is declared and not derived:
// four members answer `refused`, the transcript's word for a refusal with no
// word of its own, and `unknown_schema` answers `unknown-model`, so no rule
// over a member's spelling could produce this column. Deriving it is how the
// two vocabularies came to disagree in one member while agreeing in six.
enum Verdict {
   1: not_found      (transcript = "not-found")
   2: lease_held     (transcript = "lease-held")
   3: stale_epoch    (transcript = "stale-epoch")
   4: conflict       (transcript = "refused")
   5: lease_expired  (transcript = "lease-expired")
   6: terminal       (transcript = "terminal")
   7: invalid        (transcript = "invalid")
   8: unknown_schema (transcript = "unknown-model")
   9: unknown_op     (transcript = "refused")
  10: not_supported  (transcript = "refused")
  11: other          (transcript = "refused")
} (unknown = "grant")

// The data models a record can carry, named in `content`, and the subset a
// reader must understand or refuse the record in `critical`.
//
// A name is namespaced and versioned, so an incompatible change to one model is
// a NEW name that old readers fail to recognise while every other model in the
// record stays readable. This replaced an integer version field, which could
// only say that something changed, never which part a reader was missing.
//
// `intent@1` and `delegation@1` are critical whenever present, because a reader
// that ignores an intent it did not understand has granted itself permission to
// keep working on a job somebody asked to stop [JOB-I7], [JOB-I9].
//
// `envelope@1` is critical for the same shape of reason from the other side. A
// reader that ignored it would carry on, and then write the record back without
// it, deleting the kind's own declaration of what may be asked of it — a loss
// nobody could see afterwards, because the thing destroyed was the description.
// The envelope is optional to HAVE and not optional to UNDERSTAND [JOB-V5].
//
// `strip_critical` marks the names a reader must not be stopped by. The
// checkpoint's proven-ranges model and the display step are advisory: a reader
// that knows nothing about ranges resumes from the prefix and re-fetches the
// rest, one that knows nothing about steps shows less, and CONTRACT.md's table
// marks both "never" critical, which [JOB-D10] says a reader STRIPS rather than
// refuses. So the marking leaves `critical` on read, before the subset and
// unknown checks, and it is not a refusal: an unknown critical name still
// refuses the record. A writer that marked it was wrong, and that is the
// writer's diagnostic.
//
// WHAT THIS BLOCK CAN SAY is when a name is present, in three shapes and no
// fourth: always; a path of required structs ending at an optional field, or
// one key into an opaque value; a path ending at a string field, with the
// words that make the name present. That covers every name in CONTRACT.md's
// table. `terminal@1` is `state` holding one of three words, `recall@1` is
// `lease.recall`, `step@1` is `progress.step`, and `ranges@1` is a `verified`
// member of the checkpoint - one key in, and no further into a value this
// layer does not own [JOB-K1]. Until the three shapes existed this block
// declared five names, said in this comment that it could not declare the
// other three, and every generated reader refused a page-conforming terminal
// record: measured against a generated Rust reader, not argued.
//
// WHAT IT STILL CANNOT SAY: that a marking is required. The page makes
// `terminal@1` and `recall@1` critical whenever present; a record carrying
// either in `content` alone is accepted here, because the writer owes the
// marking and no refusal word names its absence. And a reader that recognises
// `terminal@1` has learned a declaration, not a behaviour: whether a store
// refuses its own holder's update on a finished job is judged by the
// conformance scenarios and by nothing generated from this file.
vocabulary Content {
  1: "abstraction.job/base@1"        (when = "always")
  2: "abstraction.job/intent@1"      (when = "intent")
  3: "abstraction.download/ranges@1" (when = "checkpoint.verified", strip_critical = "true")
  4: "abstraction.job/delegation@1"  (when = "delegation")
  5: "abstraction.job/envelope@1"    (when = "envelope")
  6: "abstraction.job/terminal@1"    (when = "state", is = "complete,failed,cancelled")
  7: "abstraction.job/recall@1"      (when = "lease.recall")
  8: "abstraction.job/step@1"        (when = "progress.step", strip_critical = "true")
} (of = "Record", names = "content", critical = "critical")

// A phase of a multi-phase job, for display only.
//
// It is in the record rather than in the kind's checkpoint because a checkpoint
// is opaque: only a reader that knows the kind could render one, and progress
// is the one thing a GENERIC reader has to be able to show. ADVISORY: nothing
// may decide anything on a step. `name` is opaque here, like kind and spec.
struct Step {
  1: required string name
  2: required i32 ordinal
  3: optional i32 of      (omit = "zero")
  // This phase's own units, which need not be the job's: hashing counts the
  // same bytes a second time, and the job's totals must not double for it.
  4: optional i64 done    (omit = "zero")
  5: optional i64 total   (omit = "zero")
} (unknown_fields = "refuse")

// Best effort, in units the kind defines, and explicitly NOT monotonic: a job
// resuming from a checkpoint may legitimately report a smaller done than it did
// before. Nothing may make a decision on it.
struct Progress {
  1: required i64 done
  2: optional i64 total          (omit = "zero")
  3: required timestamp updated_at
  4: optional Step step          (omit = "absent")
} (unknown_fields = "refuse")

// The issuer's demand about the resource, as against intent, which is the
// user's wish about the job. Addressed to one holding: a claim replaces it with
// nothing. The lease lapses at `until` whether or not the holder yielded — that
// lapse is the eviction, and renew never extends past it. `reason` is opaque
// here, chosen by the issuer for the holder's kind; a recall without one is a
// cancel wearing the wrong field.
struct Recall {
  1: required string reason
  2: optional string by          (omit = "zero")
  3: required timestamp at
  4: required timestamp until
} (unknown_fields = "refuse")

// The right to work on a job, held for a bounded time.
//
// epoch is the part that matters: it increases by one on every claim, and every
// write must present the epoch it holds. A process that was asleep when its
// lease lapsed has its writes refused rather than accepted on top of bytes a
// different owner has since written. It fences a DIFFERENT owner and nothing
// else — an epoch does not move when its holder writes, so what stops two
// writers under one lease overwriting each other is the write's `base`.
struct Lease {
  1: required string owner
  2: required i64 epoch
  3: required timestamp expires_at
  4: optional Recall recall      (omit = "absent")
} (unknown_fields = "refuse")

// The work has been handed to something outside this process entirely — a
// system service, a daemon on another machine — which is now doing it. When
// this is set, progress is a CACHE of what that system last reported. It is the
// truth; we are not. `delivered` is the second phase BITS calls Complete().
//
// It refuses unknown fields like every other part of the record [JOB-F1]. A
// newer writer's addition here is a change to who owns the work, and a reader
// that skipped it would carry on against a system it half understood. Anything
// a delegating participant needs to carry that is not modelled goes in
// `extensions`, which is preserved [JOB-F2] and has a criticality channel.
struct Delegation {
  1: required string system
  2: required string external_id
  3: optional bool delivered     (omit = "zero")
} (unknown_fields = "refuse")

// What somebody WANTS to happen, as opposed to what is happening.
//
// Every write to a record requires the lease, and the party who wants a change
// is almost never the party holding it: a person clicks cancel in an
// application while a service on another machine moves the bytes. This is the
// single field exempt from the lease, and that exemption is the whole point.
// `want` is one of "run", "pause", "cancel"; an unrecognised word is refused
// rather than treated as run, because guessing means carrying on with a job
// somebody asked to stop. `by` is not decoration — "which process asked for
// this" is the first question anyone has.
struct Intent {
  1: required string want
  2: optional string by          (omit = "zero")
  3: optional timestamp at       (omit = "zero")
} (unknown_fields = "refuse")

// The base envelope: which schema this kind's opaque data follows, and which
// actions may be asked of a job of that kind.
//
// `kind` says WHO may read `spec` and `checkpoint`. It does not say WHAT they
// are, and it is not versioned, so a supervisor that did not create a job could
// do exactly two things with it: find it orphaned and reclaim it. This is the
// missing half, spelled like a content name so the record has one grammar for
// names and not two.
//
// A SCHEMA IDENTIFIER IS A NAME AND NOT AN ADDRESS. Nothing in this layer
// resolves one, in any binding, ever. The grammar [JOB-V1] admits no scheme, no
// authority, no percent-escape and at most one `/`, so a URL is not a legal
// value and a record carrying one is refused before any reader sees it; that is
// structure rather than a documented wish. An identifier a reader does not know
// is `unknown_schema` and the job is left alone. The field is `string` and not
// `json` for the same reason — an opaque value is somewhere for a schema to
// carry its own instructions.
//
// `actions` ARE NAMES A SUPERVISOR MATCHES against what it already implements.
// Nothing here can say how to do anything, and this layer never performs one; a
// name it does not implement is `not_supported`. A bare name belongs to the
// base schema, and anyone else's carries the schema that declares it, because
// two vendors will otherwise both define `cancel` [JOB-V2], [JOB-V3].
//
// TWO FIELDS AND NO THIRD, and the missing third is the point. An envelope is a
// property of the KIND, so nothing in it may say what is true of this instance
// now: a supervisor that read `pause` here and concluded this job can be paused
// at this moment would be reading live state off a static declaration. It sits
// beside `spec` in the half of the record a lease holder may not write, and the
// stores refuse an update that moves it [JOB-V4].
struct Envelope {
  1: required string schema
  2: optional list<string> actions (omit = "zero")
} (unknown_fields = "refuse")

// The whole job. Everything a different process — in a different language,
// after a reboot — needs in order to continue this work is in here, because
// nothing else survives.
//
// `spec` and `checkpoint` are OPAQUE, and that is the most load-bearing
// decision in this file. The job layer must never parse them, so a download can
// grow mirrors and chunk manifests without forcing a schema change on three
// languages. `kind` says who is allowed to read them; a reader that does not
// know a kind leaves that job alone rather than guessing. `envelope` says which
// schema they follow and what may be asked of a job of that kind, which is the
// difference between leaving a stranger's job alone and being able to act on it
// without ever opening it.
//
// `state` is one of "pending", "running", "transferred", "complete", "failed",
// "cancelled". It is a string rather than an enum because that is what all
// three implementations write and have always written; the sketch that preceded
// this file declared integers and said in its own header that they were
// notional.
//
// `extensions` is data this layer does not understand, keyed by a name that
// says who does. A reader that cannot read one MUST preserve it on write —
// dropping it destroys another participant's data invisibly. Nothing generic
// may branch on a value, and nothing a stranger MUST obey may live here; that
// is what `intent` is for.
struct Record {
  1:  required list<string> content
  2:  optional list<string> critical       (omit = "zero")
  3:  required string id
  4:  required string kind
  17: optional Envelope envelope           (omit = "absent")
  5:  required string state
  6:  required json spec
  7:  optional json checkpoint             (omit = "absent")
  8:  required Progress progress
  9:  required Lease lease
  10: optional Delegation delegation       (omit = "absent")
  11: optional list<string> requires       (omit = "zero", cpp.name = "requires_")
  12: optional string error                (omit = "zero")
  13: optional Intent intent               (omit = "absent")
  14: optional map<string,json> extensions (omit = "zero")
  15: required timestamp created_at
  16: required timestamp updated_at
} (document = "true", unknown_fields = "refuse")

// The service binding's envelope. One request, one response, one line each.
//
// `base` carries the whole record a write was computed from rather than a
// version number, because the file binding's condition IS the previous bytes —
// it compares them before it renames — and a version number would be a second,
// weaker mechanism for three languages to agree about. A digest would be
// smaller and is not available: the C++ standard library has no hash a Go or
// Python peer could reproduce.
struct Request {
   1: required string op
   2: optional string id      (omit = "zero")
   3: optional string owner   (omit = "zero")
   4: optional i64 epoch      (omit = "zero")
   5: optional i64 ttl_ms     (omit = "zero")
   6: optional string want    (omit = "zero")
   7: optional string by      (omit = "zero")
   8: optional string reason  (omit = "zero")
   9: optional json record    (omit = "zero")
  10: optional json base      (omit = "zero")
} (unknown_fields = "grant")

// `unreadable` is the ids a sweep could not decode, sent WITH `records` rather
// than instead of them. `kind` stays absent: a partial answer is not a failed
// call, and a client that treated it as one would lose the records it can still
// act on.
struct Response {
  1: optional string kind            (omit = "zero")
  2: optional string error           (omit = "zero")
  3: optional string id              (omit = "zero")
  4: optional json record            (omit = "zero")
  5: optional list<json> records     (omit = "zero")
  6: optional bool bool              (omit = "zero", cpp.name = "bool_")
  7: optional list<string> unreadable (omit = "zero")
} (unknown_fields = "grant")

// Every operation a store answers. Deliberately absent, and the reasons:
//
//   - capabilities(). Declared by the sketch that preceded this file and
//     implemented in no language.
//   - a `pause` and a `resume` operation. Pausing and cancelling are the same
//     act — telling an owner what you want when you are not the owner and
//     cannot become one — so both go through set_intent.
//   - root() and work_path(). A local filesystem area is a property of ONE
//     binding; it leaked into nine public signatures before anyone noticed.
//   - anything naming a file, a path, a directory, an encoding or a port.
protocol Store {
   1: submit
   2: load
   3: list
   4: orphans
   5: claimable
   6: claim
   7: renew
   8: release
   9: set_intent
  10: recall
  11: write
} (request = "Request", response = "Response", operation = "op",
   verdict = "kind", verdicts = "Verdict", unknown_operation = "unknown_op")
