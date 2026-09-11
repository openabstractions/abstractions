# abstraction-idl

One definition of a record; for every language, an encoder that writes the same
bytes, a decoder that refuses the same inputs with the same word, and the
envelope those records travel in. The notation is a profile of Apache Thrift's
IDL — see [LANGUAGE.md](LANGUAGE.md) for what it takes, forbids and adds, and
[LINEAGE.md](LINEAGE.md) for where each construct came from and why.

**Nothing about delivering a message is generated.** Framing, connection
lifetime, reconnection and the socket belong to the transport binding and are
written by hand once per binding; a generator that opens a socket has left its
jurisdiction. LANGUAGE.md § *What the generator does not emit* is the list, and
the generator fails its own build if a backend emits an import that could reach
one.

Generated code **links nothing**. Not this repository, not a runtime, not a
serialisation library — at most the target language's own standard library.

## Install

Nothing to install. The generator is one Go module with no dependencies:

    git clone https://github.com/openabstractions/abstractions.git   # idl/ lives here
    cd abstractions/idl/gen
    go run . <definition.thrift> <outdir> [language ...]

Languages: `go` `python` `cpp` `javascript` `rust`, and `docs`, which emits the
reference page rather than an encoder. Omit the list for all of them. There is
no tagged release yet, so `go run <module>@<version>` does not resolve.

Measured on the example definition: Go, Python and JavaScript import nothing at
all; C++ includes eight standard headers; Rust uses one,
`std::collections::BTreeMap`.

## One example that runs

    go -C gen run . ../testdata/job.thrift /tmp/out
    powershell -File test/run.ps1 -Scratch /tmp/idl
    powershell -File test/repeated/run.ps1 -Scratch /tmp/repeated

`test/run.ps1` generates from `testdata/job.thrift`, builds a driver in every
language present on the machine, and checks four things:

- the same two records **encode** to the same bytes in every language;
- each language **decodes its own output and re-encodes it identically**, which
  is where an opaque payload proves it survived the reader;
- every input in `test/corpus` produces the same verdict in every language, with
  the same refusal word at the same byte offset — including the inputs that
  break two rules at once, which is what holds the declared order of the words;
- changing one line of the definition — `escape = "minimal"` to
  `escape = "ascii"` — moves every backend together.

Transport interoperability is checked separately in the maintainer source.

`test/repeated/run.ps1` is the third, and it exists because `testdata/job.thrift` is
not a sample of one — it is the specimen. It runs the same comparison over
`test/repeated/repeated.thrift`, a definition carrying the two shapes the job
record does not have: a repeated record and a string map. See
[test/repeated/README.md](test/repeated/README.md).

A toolchain that is missing is reported `UNPROVEN`, never skipped.

## Taking only part of a definition

    go -C gen run . ../testdata/job.thrift /tmp/out -only=Request,Response,Verdict,Store go

emits the envelope and nothing else — about half the Go the whole definition
produces. A surface is one declaration: a struct, an enum, a constant, the
vocabulary, the protocol. The encoding, the refusal words and the typedefs bind
everything and cannot be withheld. Every run reports what it carried.

The list is the artefact's whole contents rather than a set of roots, so a
selection that does not close is refused and the refusal names what to add. Two
edges are not obvious: the document needs its vocabulary, and an envelope struct
needs its protocol. `idl/LANGUAGE.md` says why.

Maintainers regenerate committed bindings from their definitions and compare
the bytes. The generator and tests here run independently of that workflow.

## The page

    go -C gen run . ../testdata/job.thrift /tmp/idl-docs docs

writes `/tmp/idl-docs/schema.html`: every struct, field, id, type, omission rule, enum
member, constant, vocabulary term, refusal word, encoding setting and protocol
operation the definition declares, and nothing else. It is a whole page rather
than a fragment because a spliced file has two authors and drifts on the seam;
the hand-written prose it cannot express — what a lease *means*, which pattern
each name descends from, an example that runs — stays on `openabstractions.github.io/reference.html`,
which links to it.

Every rule tag the page cites (`[DEF-A8]`) must be declared in `LANGUAGE.md`,
`abstraction-job/CONTRACT.md`, `abstraction-job/SPEC.md` or `abstraction-download/CONTRACT.md`, found by walking up
from the definition. For contract-aware documentation checks, clone the named
layer repositories beside the `abstractions` checkout; the test definition and
record fixtures themselves are included in `idl/testdata/`. A citation to a rule that does not exist fails the build,
and so does a declared name the page leaves out.

## The refusal corpus

`test/corpus/<word>.<case>.json` is refused with `<word>`; `accept.<case>.json`
is accepted. The words are declared in the definition's `refusal` block, in the
order two of them are chosen between, and a backend that says a word the
definition does not declare — or cannot say one it does — fails the build.
Adding a case is adding a file: the drivers read the directory and the
expectation is the filename, so a sixth implementation is checked against the
same corpus without touching any of the first five.

## What may break

- **A generated reader refuses a terminal record written before
  `terminal@1` existed.** The definition now declares every name in
  `abstraction-job/CONTRACT.md`'s table (`terminal@1` over the state word, `recall@1` over
  `lease.recall`, `step@1` over `progress.step`, `ranges@1` over
  `checkpoint.verified`), and a derived list must equal its predicate, so a
  `complete` record that does not declare `terminal@1` — which is every
  terminal record a `go/v0.1.0` store wrote — is `content_mismatch` to a
  generated peer while the shipped layer reads it and re-derives on write.
  Eight of the twenty-nine records under `abstraction-download/testdata/records`, measured
  2026-09-09; before that day five were refused for the opposite reason. The envelope is unaffected.

  ~~The definition has no term for `step@1`, `terminal@1` or `recall@1`~~
  2026-09-09: a term's predicate is now a path, a member of an opaque value, or
  membership in listed words ([DEF-A8]), and all three are declared.

  ~~The definition and the shipped job layer disagree about which content names
  may be marked critical, and the disagreement is open.~~ 2026-09-08: closed.
  The definition marked `intent@1` and `delegation@1` never-critical where
  `abstraction-job/CONTRACT.md` says both are critical whenever present, and it was the
  definition that was inverted. `never_critical` left the profile with it.
- **Refusal offsets agree today and are not part of the contract.** The word is.
  Five independent implementations happen to report the same byte because they
  were generated from one algorithm; a sixth, written by hand from
  `LANGUAGE.md`, would not be wrong to differ.
- The profile encodes `bool i32 i64 string json list<string> map<string,json>
  map<string,string>`, structs of those, and lists of those structs. Anything
  else is refused by name, including `double`, `set<T>`, `binary` and `union`.
- `test/run.ps1` and `test/wire.ps1` are Windows-only. The generator is not.
