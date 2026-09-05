# METHOD

How to work on an interface in this organisation: how to tell one from a layer,
how to test a proposal, and the rules a change has to satisfy.

## 1. Interface and implementation

An **interface** says what crosses a boundary. An **implementation** says how
everything else happens.

The test is mechanical. For every clause in a proposed interface, ask: **would
two competent implementers, given this clause, be free to disagree about it?**
If the answer is no — if the clause forces them to build the same internals —
it belongs to the implementation and comes out.

Worked example. An interface for model hosting cannot own continuous batching,
paged KV or prefix caching. Those decisions are re-made every forward pass by
code holding live references to the block table and the sampler. They are not
what crosses the boundary; they are how the work gets done.

## 2. Write the log line first

Before defining an interface, write the record you would want to read at three
in the morning when something has gone wrong: what happened, who asked, what
was decided, why, and what would let you reproduce it.

The fields you wrote down are the interface's vocabulary. If you could not
write the record — if the honest version is "something happened" — there is no
boundary there and nothing to define.

This follows from what a boundary is for. Swappability is the textbook answer
and it does not hold up: most interfaces have exactly one implementation for
their entire life. A named boundary is useful because it is the only place
where what crosses it can be seen, recorded, and refused.

## 3. Five tests for a proposal

The first two are necessary. Failing either kills the proposal.

**1. The log test.** Can you write the record of a crossing, and would you want
to read it? An interface whose audit line carries no information is
decorative. The degenerate version to watch for is an audit log that records
arguments as hashes: after an incident it can tell you a call happened but not
which file was deleted.

**2. The refusal test.** Can the boundary say no, and is anything actually
forced through it? A boundary that can only pass things along is a pipe.
Refusal must be explicit — an error, a message, a non-zero exit — never a
silent fallback to permissive behaviour. A component that can compute "this
does not fit" but that no process is obliged to consult has not passed: an
authority with no enforcement primitive is a suggestion. `storage` passes: a
file whose bytes do not match its name is refused on read, and there is no path
around it.

**3. The deletion test.** If someone adopts this, can they delete code? An
abstraction lets an adopter remove their private version; a layer only adds.
Count it in a diff.

This test is gameable and must never be used alone. Outsourcing work over a
socket also produces a negative diff — replacing six hundred lines of download
logic with `curl` scores as well as replacing it with a designed interface. The
difference is test 1: `curl` deletes code and creates no place to observe or
refuse.

**4. The variation test.** An interface is a claim about what does *not* vary,
so write the list: what differs between implementations and is hidden here? If
the list is empty, it is a wrapper with a new name. If something on the list
leaks — implementers keep reaching around the interface to get at it — either
the boundary is in the wrong place, or that thing cannot be normalised and must
be carried opaquely (§4).

**5. The two-implementation rule.** You do not know where the seam is until two
independent implementations exist, and not two written by the same author in
one afternoon. The joint is found by the friction of the second implementation.
A specification written before the second implementation is a guess.

## 4. Holes are part of the interface

Sometimes a thing crosses the boundary that the receiver cannot interpret.
Signed or encrypted reasoning blocks are the case that forced this: they must
be replayed back verbatim or the next request is rejected, and a neutral schema
that flattens them into a text field destroys the property that makes them
work.

Three options, and only the third is honest:

1. **Normalise it** — you break it, precisely on the feature people pay for.
2. **Refuse it** — defensible, and it kills the use case.
3. **Carry it verbatim, record where it came from, and declare any projection
   lossy.**

Take the third, and do not then claim the interface *handles* it. It transports
it. Write the hole into the interface as a hole, and do not count it against
the variation test: a thing carried opaquely is by definition not part of what
the interface abstracts.

## 5. How an interface gets built here

In this order. Skipping a step is the failure.

1. **Implement something that works**, with no interface in mind. Hit a real
   problem with it.
2. **Implement it a second time**, differently — another vendor, platform,
   language, or set of constraints.
3. **Extract the interface** from what the two have in common, not from what
   you think they should have in common. This is a step of subtraction.
4. **Write it down**, if there is a reason to. Most interfaces never need a
   document.
5. **Port it** — rarely, and last.

## 6. What crosses languages

The things that have crossed language boundaries reliably — protobuf, JSON,
SQL, HTTP, URIs, POSIX file semantics — all define **a record or a protocol**:
what a thing is on the wire or on disk, and which exchanges are legal. None of
them defines the shape of your code. Each language binds them however is
idiomatic there.

Cross-language *code* libraries have a harder problem, because languages
disagree about ownership and lifetime, error signalling, concurrency, and what
is cheap. An interface spanning all of those either adopts one language's model
and is alien everywhere else, or abstracts so far that it says nothing.

> **Define the record and the protocol. Let each language bind them its own
> way.**

This is the same conclusion as §2 from the other side: the valuable thing at a
boundary is the record of what crossed, and a record is what ports.

## 7. Rules for a change

- **No interface until two implementations exist** — and find the second one in
  the world rather than inventing it.
- **Prefer adopting or wrapping over building.** Say so plainly when the honest
  answer is "write it". BITS and `slog` were adopted; `aria2` was refused on
  licence, and the reason is written down.
- **An interface changes on a failing real case, never on an argument.**
  `Finalize` gained a parameter because a NAS could not discover a local
  destination from a handle.
- **A proposal needs prior art, not permission.** What it owes is evidence that
  independent systems already converged on the shape — BitTorrent v2 and
  HuggingFace Xet both describe a transfer as a content identity plus places to
  get it; Temporal hands a retried worker its predecessor's checkpoint; BITS
  forces two-phase completion. Convergence argues that a design was not
  invented. Adoption is a separate thing and is not claimed.
- **A generic layer contains no word from a specific one.** Check it before
  opening a PR: `grep -rniE "model|gguf|huggingface|ollama" go --include=*.go`.
- **Done means it runs on a machine you do not own**, from the written
  instructions, without you present.
- **Every task ends in a transcript** committed under `docs/results/` and
  indexed in `docs/results/README.md`. A failure is a result. Scrub usernames,
  machine names and local paths before committing.
- **Documentation does not grow faster than tested code.** Measure the ratio
  before adding a document.

## 8. If you remember two things

1. No interface until two implementations. Build it twice, then extract what
   survived.
2. No interface change on an argument. Only on a failing real case.
