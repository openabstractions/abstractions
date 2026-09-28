# METHOD

How to tell an abstraction from a layer, and how to test one.

The worked examples and dated amendments behind these rules are in
`research/method-history-2026-09-24.md`, under the same section numbers.

## 1. The distinction

An **interface** says what crosses a boundary.
An **implementation** says how everything else happens.

That is the whole distinction, and almost every failure is a violation of it in one
direction: an interface that starts specifying *how*.

The test is mechanical. For every clause in a proposed interface, ask: **would two
competent implementers, given this clause, be free to disagree about it?** If the answer
is no — if the clause forces them to build the same internals — it belongs to the
implementation and must come out.

## 2. Why draw a boundary at all

> A named boundary is the only place where what crosses it can be **seen, recorded, and
> refused.**

**Write the log line first.**

Before defining an interface, write the record you would want to read at three in the
morning when something has gone wrong. What happened, who asked, what was decided, why,
and what would let you reproduce it. Then look at the fields you wrote down. **Those
fields are the interface's vocabulary.** If you could not write the record — if the honest
version is "something happened" — there is no boundary there and no interface to define.

## 3. Five tests

Apply these to a proposed interface. The first two are necessary; failing either kills it.

**1. The log test.** Can you write the record of a crossing, and would you want to read
it? An interface whose audit line carries no information is decorative.

**2. The refusal test.** Can the boundary say no, and is anything actually forced through
it? A boundary that can only pass things along is a pipe. Control requires refusal, and
refusal must be explicit — an error, a message, a non-zero exit — never a silent
fallback to permissive behaviour. **An authority with no enforcement primitive is a
suggestion.**

**3. The deletion test.** If someone adopts this, can they delete code? An abstraction
lets you remove your private version. A layer only adds. Count it in a diff.
This test is known to be gameable and must never be used alone.
**Deletion without observation is a middleman. Observation without deletion is a
wiretap. An abstraction is both.**

**4. The variation test.** An interface is a claim about what does *not* vary. So write
the list: what differs between implementations and is hidden by this interface? If the
list is empty, you have a wrapper with a new name. If something on the list leaks in
practice — implementers keep reaching around the interface to get at it — then either the
boundary is in the wrong place, or that thing cannot be normalized and must be carried
opaquely instead (§4).

**5. The two-implementation rule.** You do not know where the seam is until two
independent implementations exist. The joint is found by the friction of the
second implementation, never by thinking harder about the first. What counts as
independent is `VISION.md` principle 20.

## 4. Holes are part of the interface

Sometimes a thing crosses your boundary that the receiver genuinely cannot interpret.
You have three options and only the third is honest:

1. **Normalize it.** You break it, and you break it precisely on the feature people pay
   for.
2. **Refuse it.** Defensible, and it kills the use case.
3. **Carry it verbatim, record where it came from, and declare any projection lossy.**

Take the third — but do not then claim the interface *handles* it. It does not; it
transports it. Write the hole into the interface as a hole. A thing you carry opaquely is
by definition *not* part of what the interface abstracts. Do not count it as a win.

## 5. How an interface is actually developed

In this order. Skipping a step is the failure.

1. **Implement something that works**, with no interface in mind. Hit a real problem with
   it.
2. **Implement it a second time**, differently — another vendor, another platform, another
   language, another set of constraints.
3. **Extract the interface** from what the two have in common. Not from what you think
   they should have in common. This is the only step where an interface is created, and it
   is a step of subtraction.
4. **Write it down** — if there is a reason to. Most interfaces never need a document.
5. **Port it** — rarely, and last (§6).

And one rule governing all of it:

> **An interface changes on a failing real case. Never on an argument.**

The corollary is that **you should be trying to break your own interface, continuously,
with real inputs you did not choose in advance.** An interface with no open questions is
not finished. It is unreviewed.

Draw the interface from existing standards and from what the field already learned;
bring it to each platform as a binding; then use it in our own products. The cheap fix
is to name the ancestor before drawing, not after being contradicted. **Step three is an
instrument, not a validation.** **A layer used only by its own harness is untested in
the way that matters.**

## 6. What actually crosses languages

**What did cross, every time:** protobuf, JSON, SQL, HTTP, URIs, POSIX file semantics.
Every one of them defines **a record or a protocol** — what a thing *is* on the wire or
on disk, and what exchanges are legal. None of them defines the shape of your code. Each
language binds them however is idiomatic there, and the binding is nobody else's business.

> **Define the record and the protocol. Let each language bind them its own way.**

## 7. The failure signature

You have stopped doing engineering and started doing taxonomy when:

- **A specification has no open questions.** Zero open questions means zero reviewers.
- **A count has become an axiom.**
- **Decisions have the form** *Decision → one-sentence reason → "Kill: <alternative>"*,
  where the alternatives are named but never modelled, costed, or tried.
- **A governing principle cannot be contradicted.** If you cannot state the observation
  that would prove your principle wrong, you have a belief, not a method.
- **Documents grow faster than tested code.** Measure the ratio. It is one command.
- **"Done" means a file was updated.** Done means it runs, on a machine you do not own,
  from the written instructions, without you present.
- **You are reviewing a review.** At that point the output has become commentary on itself.

## 8. A MUST without a scenario is a wish

**Every rule we write names the instrument that enforces it, or it is deleted.**
Usually that instrument is a scenario. A rule enforced by a corpus, a compiler, or a
structural check is enforced. **A rule enforced by nothing is a wish.**

**And a capability claim is a set of scenarios you are held to, not a gap you are
excused.** Claiming a capability must mean being run against everything that capability
owes.

**New obligations land as a scenario before an implementation.** If the scenario
cannot be written, the obligation is not yet understood well enough to impose on
three languages and a stranger.

## 9. Every harness is handed a broken implementation

**A harness that has never gone red for a reason you planted has not been
tested.** Give it an implementation that exits zero and prints nothing, and one
that refuses everything, and require it to fail loudly for both (`VISION.md`
principles 20 and 21).

## 10. If you remember two things

1. **No interface until two implementations.** Build it twice, then extract what survived.
2. **No interface change on an argument. Only on a failing real case.**

## 11. Four jurisdictions, one home per rule

A specification is four documents, and a rule lives in exactly one of them. A rule found
in two drifts.

| the rule is about | jurisdiction | the artefact |
|---|---|---|
| fields, types, identifiers, structural constraints | definition | Each abstraction's Thrift definition; generated language representations and API reference derive from it |
| operations, state transitions, cancellation, ownership, retries | behavioural specification | The abstraction's CONTRACT.md and its tagged behavioral rules |
| framing, discovery, authentication, reconnection | transport binding | Shared transport specifications and the capability's binding contract; service-owned file layout is not the client transport |
| concrete examples and regressions | conformance corpus | Independently authored scenarios and fixtures, plus service-client and installed-lifecycle checks for the claims they cover |

The README is the door - install it, call it, one example - and links into
the four. A tagged rule on the door is a rule in the wrong jurisdiction.

## 12. The definition is the source, and every binding is generated from it

That artefact is a **definition**: record shape, then the encoding policy that fixes the
bytes, then the obligations it lays on a reader — what may be refused, what may be
demanded, how wide a decoder must be. `idl/LANGUAGE.md` is the whole list and its rules
are tagged `[DEF-*]`; `idl/LINEAGE.md` cites where each construct came from. **A language
costs one backend and yields every layer; a layer costs one definition and yields every
language.** What the generated code must be (usable without a service runtime, native in
each language, never the provider engine) is `VISION.md` principle 4.

## 13. A language that fights the definition files a question, not a patch

Friction in one language is evidence: either the concept is shaped by the language it
was born in, ours to fix, or the language lacks the primitive, a cost to record and pay.
It is filed as a question against the layer, and a change is judged across languages
(`VISION.md` principle 5).

## 14. Which interfaces are ours to draw at all

No interface in this vocabulary names a model, except `model`'s, which is the one
layer that is about them.

| seat | question | provided by platforms? |
|---|---|---|
| **self-assertion** | I declare what I want | always |
| **observation** | what is everyone else doing, and why | rarely, and privileged |
| **agency** | act on it, or for someone who never asked | essentially never |

> **A layer enters this vocabulary only if seat two or seat three is missing,
> and the seat can be furnished by something the adopter is able to carry.**

Classify by which seat is missing, never by the noun. What an adopter can carry: a
library it links, or the shared user runtime; a system service needing an administrator
only as an upgrade; a remote service with its availability explicit
(`ARCHITECTURE.md`, "Runtime and installation"). A layer is admitted the way an
interface is changed (§5): on a real case that forced it, never on an argument.
