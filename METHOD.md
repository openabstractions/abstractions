# METHOD

How to tell an abstraction from a layer, and how to test one.

This file is short on purpose, and the cap it used to name was a number nobody
enforced: it said two hundred while the file passed four hundred. The project it came out of
produced two hundred thousand words of specification and one working binary. If this
document ever needs a second volume, it has become the thing it warns about.

---

## 1. The distinction

An **interface** says what crosses a boundary.
An **implementation** says how everything else happens.

That is the whole distinction, and almost every failure is a violation of it in one
direction: an interface that starts specifying *how*.

The clearest case in this project's own history: the hosting interface tried to own
continuous batching, paged KV, and prefix caching. Those are decisions re-made every
forward pass, by code holding live references to the block table and the sampler. They
cannot be owned from outside the process, because they are not *what crosses* — they are
*how the work gets done*. An interface that reaches for them is no longer an interface; it
is a second implementation, written by someone with no access to the first one's state.

The test is mechanical. For every clause in a proposed interface, ask: **would two
competent implementers, given this clause, be free to disagree about it?** If the answer
is no — if the clause forces them to build the same internals — it belongs to the
implementation and must come out.

---

## 2. Why draw a boundary at all

The textbook answer is swappability: define an interface so implementations can be
exchanged. In practice almost nobody exchanges them. Most interfaces have exactly one
implementation for their entire life. If swappability were the justification, most
abstractions would be unjustified — and that conclusion is wrong, so the justification is
wrong.

The real answer:

> A named boundary is the only place where what crosses it can be **seen, recorded, and
> refused.**

A monolith is not bad because you cannot replace its parts. It is bad because things
happen inside it and you cannot observe them, cannot reconstruct them afterwards, and
cannot stop them. Every complaint that started this project has that shape. You cannot
tell which model file you have. You cannot tell what a tool actually did. You cannot tell
where the VRAM went. You cannot tell why the model saw those particular chunks of your
code. Four different domains, one symptom: something happened and there was nowhere to
stand and watch it.

This reframing is not decoration. It changes what you build, because it gives you a design
procedure:

**Write the log line first.**

Before defining an interface, write the record you would want to read at three in the
morning when something has gone wrong. What happened, who asked, what was decided, why,
and what would let you reproduce it. Then look at the fields you wrote down. **Those
fields are the interface's vocabulary.** If you could not write the record — if the honest
version is "something happened" — there is no boundary there and no interface to define.

---

## 3. Five tests

Apply these to a proposed interface. The first two are necessary; failing either kills it.

**1. The log test.** Can you write the record of a crossing, and would you want to read
it? An interface whose audit line carries no information is decorative. Watch for the
degenerate version: this project's permission layer logged its arguments *as hashes*, so
after an incident it could tell you a call happened but not which file was deleted. It
had an audit log and no audit.

**2. The refusal test.** Can the boundary say no, and is anything actually forced through
it? A boundary that can only pass things along is a pipe. Control requires refusal, and
refusal must be explicit — an error, a message, a non-zero exit — never a silent
fallback to permissive behaviour. The counterexample here is instructive: the hosting
component could compute "this does not fit," but no process was obliged to ask it, so it
could not refuse anything. **An authority with no enforcement primitive is a suggestion.**
The model store passes: a file whose bytes do not match its name is refused on read, and
there is no path around it.

**3. The deletion test.** If someone adopts this, can they delete code? An abstraction
lets you remove your private version. A layer only adds. Count it in a diff.

  This test is known to be gameable and must never be used alone. Outsourcing work over a
  socket also produces a negative diff — replacing six hundred lines of download logic
  with `curl` scores exactly as well as replacing it with a well-designed interface. The
  difference is test 1: `curl` deletes code and creates no place to observe or refuse.
  **Deletion without observation is a middleman. Observation without deletion is a
  wiretap. An abstraction is both.**

**4. The variation test.** An interface is a claim about what does *not* vary. So write
the list: what differs between implementations and is hidden by this interface? If the
list is empty, you have a wrapper with a new name. If something on the list leaks in
practice — implementers keep reaching around the interface to get at it — then either the
boundary is in the wrong place, or that thing cannot be normalized and must be carried
opaquely instead (§4).

**5. The two-implementation rule.** You do not know where the seam is until two
independent implementations exist. Not two written by the same author in one afternoon —
two, ideally by people who can annoy each other. The joint is found by the friction of the
second implementation, never by thinking harder about the first. A specification written
before the second implementation is a guess in a confident font.

**And by this rule, this project does not yet have two.** Go, Python and C++ were
written by one author in one voice. They are not three implementations; they are
one implementation transcribed three times. Byte-identical conformance output
proves the transcription was faithful — it does not prove the specification is
unambiguous, because ambiguity resolved the same way three times by the same
mind leaves no trace. Correlated error is indistinguishable from agreement when
you only look at the outputs.

The same doubt applies to outside evidence. Another codebase arriving at the
same primitive is convergent design only if it was reached independently; if
both were written with the same assistant, it may be one prior agreeing with
itself, and the stock answer for durable append is a single byte offset in every
textbook. That does not make the finding wrong — a true criticism is true
whoever surfaces it — but it is not corroboration, and it must not be counted as
any.

The remedy is cheap and we have not spent it: hand the written contract to a
second implementer with no sight of the existing code, and let the diff say what
the prose failed to pin down. A second *model* qualifies where a second
transcription does not.

---

## 4. Holes are part of the interface

Sometimes a thing crosses your boundary that the receiver genuinely cannot interpret.

The example that taught this project the lesson: model vendors now emit reasoning blocks
that carry a cryptographic signature, or that are encrypted and bound to a server-side
conversation id. They must be replayed back verbatim or the next request is rejected. A
neutral schema that flattens them into a text field destroys the exact property that makes
them work.

You have three options and only the third is honest:

1. **Normalize it.** You break it, and you break it precisely on the feature people pay
   for.
2. **Refuse it.** Defensible, and it kills the use case.
3. **Carry it verbatim, record where it came from, and declare any projection lossy.**

Take the third — but do not then claim the interface *handles* it. It does not; it
transports it. Write the hole into the interface as a hole.

**An interface that names its own holes is stronger than one that hides them**, because
the hidden hole is discovered by a user in production and the named one is discovered by a
reader in thirty seconds. This also matters for §3.4: a thing you carry opaquely is by
definition *not* part of what the interface abstracts. Do not count it as a win.

---

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

This is the rule that would have prevented everything in `docs/review/`. That project
settled twenty-one decisions and eight specifications by argument, alone, in eleven hours,
and every one of them that reality later touched broke immediately: a real repository URL,
a real model file, a real cross-file reference, a real permission check. Nothing settled by
argument survived contact; everything tested against the world reported back.

The corollary is that **you should be trying to break your own interface, continuously,
with real inputs you did not choose in advance.** An interface with no open questions is
not finished. It is unreviewed. If you have zero open questions after two days, you have
not been outdoors.

---


**Three steps, and the first two are always done in the wrong order.** Draw the
interface from existing standards and from what the field already learned; bring
it to each platform as a binding; then use it in our own products. On 2026-09-07
step one was skipped three times in one day - delegation was unprecedented until
BITS, Cloudreve and the media-automation stack; there was no interface for
long-running generative work until MCP Tasks, `longrunning.Operation` and CWL;
policy belonged in a file until polkit. Each was corrected within the hour. **The
rule is not the problem; the sequencing is**, and the cheap fix is to name the
ancestor before drawing, not after being contradicted.

**Step three is an instrument, not a validation.** Everything found on
2026-09-07 came from our own products using our own layers: `identity` bound at
2 of 14 entry points, found because the router finally called it; a 388 MB
re-fetch, found by a real delegation run and not by forty-six scenarios; a lease
that lapses without waking any worker, found by a worker genuinely waiting on
one; and `dl` unusable while `jobd` runs, found by pointing a camera at it. **A
layer used only by its own harness is untested in the way that matters.**
## 6. What actually crosses languages

The long-term goal is a set of abstractions available in most languages. That goal is
reachable, but not in the form people usually imagine, and the difference matters.

**Why `std` and Boost worked:** they were *extracted*, not designed. `std::vector` is not
someone's clever idea about dynamic arrays; it is what survived a decade of people writing
dynamic arrays badly. Boost's discipline was that a library needed real users before
standardization was even discussed, and most proposals died. The standard was the residue
of practice — the same shape as §5 step 3.

**Why a cross-language *code* library is much harder than it looks.** Languages disagree
about the things an interface must assume:

- **ownership and lifetime** — RAII, borrow checking, garbage collection
- **error signalling** — exceptions, return codes, sum types
- **concurrency** — OS threads, green threads, coloured async functions
- **what is cheap** — allocation, copying, dynamic dispatch

An interface that spans all of these must either adopt one language's model, and be alien
everywhere else, or abstract so far that it says nothing. This is why cross-language
*object models* keep failing: CORBA, SOAP, and every "write once" runtime.

**What did cross, every time:** protobuf, JSON, SQL, HTTP, URIs, POSIX file semantics.
Look at what they have in common. Every one of them defines **a record or a protocol** —
what a thing *is* on the wire or on disk, and what exchanges are legal. None of them
defines the shape of your code. Each language binds them however is idiomatic there, and
the binding is nobody else's business.

So the constraint is:

> **Define the record and the protocol. Let each language bind them its own way.**

Which written form of the record is normative is open: the contract pages'
tagged prose, which the scenarios cite; `job.thrift`, published and checked
against nothing; or a schema that generated passing clients. The decision is
the owner's and precedes anything built on a schema.

Which is the same conclusion as §2, arrived at from the other side. The audit thesis says
the valuable thing at a boundary is the record of what crossed. The portability evidence
says the only thing that reliably crosses languages is a record and a protocol. **They are
the same object.** That is the strongest reason to believe the two halves of this idea
belong together — and it is also the correction to what went wrong here: the previous
attempt tried to make seven *daemons* the shared thing. Daemons do not port. Records do.

---

## 7. The failure signature

You have stopped doing engineering and started doing taxonomy when:

- **A specification has no open questions.** Zero open questions means zero reviewers.
- **A count has become an axiom.** "There are seven" was written down once, then defended,
  then protected with appendices so it could stay seven. The appendices were the disproof.
- **Decisions have the form** *Decision → one-sentence reason → "Kill: <alternative>"*,
  where the alternatives are named but never modelled, costed, or tried. That is the form
  of an argument with the content removed. It reads as rigour and functions as immunity.
- **A governing principle cannot be contradicted.** "If it admits more than one defensible
  design, we have not found it yet" makes every objection into the objector's failure. This
  cost more than any technical mistake in the project it came from. If you cannot state
  the observation that would prove your principle wrong, you have a belief, not a method.
- **Documents grow faster than tested code.** Measure the ratio. It is one command.
- **"Done" means a file was updated.** Done means it runs, on a machine you do not own,
  from the written instructions, without you present.
- **You are reviewing a review.** At that point the output has become commentary on itself.

---

## 8. A MUST without a scenario is a wish

Added 2026-09-06, on evidence, after the brakes were shown to have no pads.

**Every rule we write names the instrument that enforces it, or it is deleted.**
Not weakened - deleted. Contract pages, `CLAUDE.md`, this file, a brief, a
convention: all of them.

**Generalised 2026-09-06 from "every MUST on a contract page", which was too
narrow and cost four agents.** Six reached *a rule we wrote has no instrument, so
nobody follows it* independently, and **four of them after this section already
said it** - each one generalising it from contract pages to all rules on their
own, in vocabularies sharing no words. The narrow version was itself a rule that
could not fail outside one file.

Usually that instrument is a scenario. Amended 2026-09-06 the same day it was
written, because an agent produced the counter-case: the rule that an error's
class is attached where the error is defined rather than kept in a list can never
have a scenario - two implementations differing on it produce identical
transcripts forever - and it is correct, load-bearing, and came from a real
failure. A rule enforced by a corpus, a compiler, or a structural check is
enforced. **A rule enforced by nothing is a wish**, and that was always the point. A rule nobody can fail is
not a rule, and leaving it on the page teaches readers that the page is
decorative.

The case: `intent` was written plainly and one implementation ignored it for
months, delivering files people had cancelled. `Record.requires` was written as a
*guarantee* and was never read, so a job demanding survival of process exit ran
on an in-process fetcher. The refusal status set was written nowhere and drifted
by four rows between two languages. In the same period, **every field compared
byte-for-byte has drifted exactly zero times.**

Prose is not enforcement. The page was never the problem.

**And a capability claim is a set of scenarios you are held to, not a gap you are
excused.** `capabilities: store` currently means "do not ask me about transfers"
and it let a reader pass as a third implementation while our conformance results
were quoted as three languages agreeing. Claiming a capability must mean being
run against everything that capability owes.

**New obligations land as a scenario before an implementation.** If the scenario
cannot be written, the obligation is not yet understood well enough to impose on
three languages and a stranger.

## 9. Every harness is handed a broken implementation

Added 2026-09-06, from an agent that ran the experiment instead of proposing it.

**A harness that has never gone red for a reason you planted has not been
tested.** Give it an implementation that exits zero and prints nothing, and one
that refuses everything, and require it to fail loudly for both. Two of ours were
checked this way and caught both; `behaviour-conformance.sh` has not been, and a
fixture server that always returns 200 with the right bytes would pass a harness
that only checks the bytes arrived.

The reason this is a standing rule and not a one-off: **every harness we build
composes the implementations and observes where composition succeeded.** Bytes
need both sides to produce bytes; meaning needs both sides to produce meaning. So
the mechanism requires the thing being measured to have worked, and the failure
half of the subject is outside the reachable half of the instrument. That bias is
structural. It recurred three times in two days - spellings not behaviour,
acceptance not refusal, files not the wire - and it will recur again.

## 10. If you remember two things

1. **No interface until two implementations.** Build it twice, then extract what survived.
2. **No interface change on an argument. Only on a failing real case.**

Everything above is elaboration on those.

## 11. Four jurisdictions, one home per rule

Added 2026-09-08. A specification is four documents, and a rule lives in
exactly one of them. A rule found in two drifts; a rule found in none is what
`job/README.md` had been carrying under the name of a door.

| the rule is about | jurisdiction | the artefact, today |
|---|---|---|
| fields, types, identifiers, structural constraints | schema | the record table in `job/README.md` § The record, enforced by each language's decoder; `job/job.thrift` is a sketch that generates nothing. **Prose, not machine-readable** - the open question in § 6 stands |
| operations, state transitions, cancellation, ownership, retries | behavioural specification | `job/SPEC.md` |
| framing, discovery, authentication, reconnection | transport binding | the file layout in `job/README.md` § Where the files are and `cas/README.md`; the socket in `job/go/wire.go`, shipped to nobody |
| concrete examples and regressions | conformance corpus | `download/testdata/scenarios/`, `download/testdata/verdicts/`, `research/wire-compat/` |

The README is the door - install it, call it, one example - and links into
the four. A tagged rule on the door is a rule in the wrong jurisdiction; the
harness reads tags off the behavioural page once it is pointed there.

## 12. The definition is the source, and every binding is generated from it

Added 2026-09-08 on the owner's direction. Section 11 asks which artefact holds
each rule; this answers what happens to the schema jurisdiction once it has an
answer. **Its artefact stops being one of four documents to keep in agreement
and becomes the thing the bindings are made from.**

That artefact is a **definition**, and it is deliberately not called a schema,
because a schema is a quarter of it: record shape, then the encoding policy that
fixes the bytes, then the obligations it lays on a reader — what may be refused,
what may be demanded, how wide a decoder must be. `idl/LANGUAGE.md` is the whole
list and its rules are tagged `[DEF-*]`.

Twelve layers in three languages is thirty-six hand-written surfaces, and we
hand-wrote every one. The arithmetic is the argument: **a language costs one
backend and yields every layer; a layer costs one definition and yields every
language.** The cost stops being a product and becomes a sum.

**Three constraints decide the design, and they are not negotiable.**

**Generated code imports nothing.** A runtime library an adopter must link is a
dependency we hand to every one of them forever. That single test disqualified
Thrift, Protobuf, Cap'n Proto, FlatBuffers and Smithy — measured, not assumed.

**Generated code reads as native.** `Result` in Rust, an exception in Java, a
returned error in Go; the naming, the namespace and the import a local would
expect. An adopter keeps their engine and takes only our vocabulary, so a
vocabulary that makes them write foreign code is one they decline. **This is the
same rule as naming things the way the industry already names them, applied to
syntax instead of nouns.**

**The notation is a profile of one people already know, and every construct
cites where it came from.** A construct with no ancestor is a claim that needs
defending. `idl/LINEAGE.md` carries that, and it is part of the contract rather
than documentation of it — the first thing a stranger reads.

**The engine is never generated. Only the vocabulary**: the record, the wire,
the status and refusal lists, the capability words.

And what this does *not* buy: **generated implementations agreeing is not
evidence and never claims to be.** It is descent made honest. The evidence
remains the corpus, an implementation somebody else wrote, and a definition
anybody can read.

## 13. A language that fights the definition files a question, not a patch

Added 2026-09-08 on the owner's direction. Section 12 makes the definition the
source; this is how it is allowed to change.

**Friction in one language is evidence, and it points two opposite ways.**
Either the concept is shaped by the language it was born in — ours to fix — or
the language genuinely lacks the primitive, which is a cost to record and pay.
**Those need opposite responses, so the friction is filed as a question against
the layer rather than a patch against the notation**, and answered on evidence
like anything else. A contract amended on every complaint becomes the union of
every language's habits and means nothing anywhere.

**And a change is judged across languages, not in the one that prompted it.**
A change may lower one language's fitness if it raises the whole, and the whole
is not a sum of a popularity table: **populations overlap.** C with C++,
JavaScript with TypeScript, Java with Kotlin, C# with F#, Objective-C with
Swift. **Summing correlated languages counts the same programmer twice and
rewards whichever family is most fragmented.** Maturity is a second axis — a
small expert population that would *implement* a binding can be worth more than
a large one that will only *consume* one.

**The score is a target the moment it exists.** So the rule is not *maximise the
number*. It is: **a regression is named out loud with the language it hurts, and
the trade is argued in words with the number as evidence rather than as the
verdict.** A design fitted to a weighting is a design fitted to a table somebody
assembled.

The procedure and the population model live in `research/language-score/`.

## 14. Which interfaces are ours to draw at all

Added 2026-09-10. Everything above says how an interface is drawn. This says
which ones we are entitled to draw.

**The sampling frame is not the subject.** The first adopters are local-AI
tools because that is where the pain is loudest — tens of gigabytes, on
somebody's desk, dozens of programs reimplementing the same few services. No
interface in this vocabulary names a model, except `model`'s, which is the one
layer that is about them.

### Three seats

Platform services are built for the cooperating process declaring its own
intent. Almost none of them offers a seat to a third party.

| seat | question | provided by platforms? |
|---|---|---|
| **self-assertion** | I declare what I want | always |
| **observation** | what is everyone else doing, and why | rarely, and privileged |
| **agency** | act on it, or for someone who never asked | essentially never |

Keep-awake demonstrates it. `SetThreadExecutionState`,
`IOPMAssertionCreateWithName` and `systemd-inhibit` all serve seat one.
`powercfg /requests` is seat two and needs administrator, so an ordinary
application cannot find out what is holding the machine awake. Seat three
barely exists — `powercfg /requestsoverride` denies by process name,
machine-wide, from an elevated command line, with no API — and no platform lets
you hold wake *for* a process that never learned the call.

> **A layer enters this vocabulary only if seat two or seat three is missing,
> and the seat can be furnished by something the adopter is able to carry.**

Wrapping seat one is not worth doing. **Classify by which seat is missing,
never by the noun**: a noun bundles capabilities that belong to different
kinds, and treating "a rights and ownership service" as one item is what made
the whole idea look unbuildable. *Which app is asking* and *what it may do
here* are ours. *A token another machine will honour* is an identity provider —
its own trust model, key custody and attack surface — and is built as a service
that adopts these abstractions, never as a layer of one. A capability the
platforms already observe and act on wants a portability shim, not this
library. A capability nearly furnished on two platforms with nothing to face on
the third has its scope cut to where it is honest, or is not taken.

What follows from the test is in `CLAUDE.md` § Prior art; it is sharpened in
`VISION.md` 2026-09-07 *The seat test, sharpened by an agent*.

### What "carry" allows

A daemon has a lifecycle and a library does not, and every lifecycle is a
failure mode: it can be stopped, crash, fail to start, be blocked by policy, or
run twice over one store.

| dependency | may a layer require it? | why |
|---|---|---|
| a library the adopter links or bundles | **yes** | no lifecycle. It is present because the adopter is present |
| a helper process the library can start itself, user-scope | **yes** | recoverable without privilege; this is what `jobd` is |
| a system service needing admin | **only as an upgrade** | not installable on a locked-down machine, and the adopter cannot fix that |
| a server on another machine | **no** | it cannot be carried at all. Build it as a service that adopts these abstractions |

Prefer the library. Require a daemon only for what a library cannot do.

**A capability that varies by environment must degrade, because it cannot be
carried — nobody can bundle a NAS.** That is why *presence is the
configuration* applies to `download`.

And a layer is admitted the way an interface is changed (§5): on a real case
that forced it — an adopter, or our own products using it — never on an
argument.
