# SCOPE

What this is a library of, what qualifies for it, and in what order.

`METHOD.md` says how an interface is drawn. This says which interfaces are ours
to draw at all.

---

## 1. It is not about AI

The directory is called `AI abstractions` because the first adopters were
local-AI tools. That was the sampling frame, not the subject. Those tools are
simply where the pain is loudest right now: they move tens of gigabytes, they
run on somebody's desk, and there are forty of them reimplementing the same
three services badly.

The subject is **abstractions**. Nothing about `job`, `download` or `storage`
mentions a model, and nothing should.

---

## 2. The gap this exists in

Cross-platform runtimes stopped at the **system call**. Files, sockets, threads,
processes, clocks, memory — Java, .NET, Go, Qt and Rust all give you a portable
API for every one of them, and have for twenty years.

None of them gives you a portable answer to:

- what work is in flight on this machine, including work that predates me
- is there something here that fetches bytes better than I can
- who is asking, and may they

Those are **services**, one level up, and nobody standardised them. That level
is the subject.

The reason for the gap is worth stating, because it is also the difficulty: a
portable file API could be specified once, because kernels converged on what a
file is. Services never converged. They arrived per-vendor, per-platform, and
late — and the honest facade over one has to admit that the thing behind it
**may not exist at all**. That admission is the whole design problem.

### The shape the gap actually has

It is sharper than "services are not portable", and the sharper version is the
one that predicts where a layer is worth building.

Platform services are built for **the cooperating process declaring its own
intent**. Every one of them serves that case well. Almost none of them offers a
seat to a third party. Three seats, and the platforms only ever furnish the
first:

| seat | question | provided? |
|---|---|---|
| **self-assertion** | I declare what I want | always |
| **observation** | what is everyone else doing, and why | rarely, and privileged |
| **agency** | act on it, or for someone who never asked | essentially never |

Keep-awake is the clean demonstration. `SetThreadExecutionState`,
`IOPMAssertionCreateWithName` and `systemd-inhibit` all serve seat one
perfectly. Seat two exists but is privileged — `powercfg /requests` **requires
administrator**, so an ordinary application cannot find out what is holding the
machine awake. Seat three barely exists: `powercfg /requestsoverride` can deny a
request, but only by process name, machine-wide, from an elevated command line,
with no API and no way to express "this app may hold wake while it is inferring
and not while it is idle". And nothing on any platform lets you hold wake *for*
a process that never learned the call — which is the case that matters, because
the program keeping your machine busy is usually not the one that thought about
power.

`job` and `download` are already instances of the same gap. Every downloader
serves seat one. This library exists because nothing served seats two and three:
what work is in flight that I did not start, and how do I act on it.

> **A layer is worth building where seats two and three are missing. Wrapping
> seat one is not worth doing — everyone already does it, and adequately.**

---

## 3. Classifying a candidate

A list of candidate services — downloader, service discovery, secret storage,
logging, licensing, rights and ownership, keep-awake — reads like one idea, and
treating it as one is the fastest available way to build nothing.

Classify by **which seat is missing**, never by the noun. An earlier draft of
this file classified by the noun and got two of its three examples wrong, in
opposite directions: it called keep-awake trivial because seat one is universal,
and dismissed the whole of rights because one part of it is a distributed
system. A noun bundles capabilities; a seat does not.

### Kind A — every seat is already furnished

Nothing qualifies today. A capability whose observation and agency the platforms
already provide does not need this library; wrapping the spelling difference is
a portability shim, and there are good ones.

Kept as a category because it is the test a candidate has to fail.

### Kind B — seat one is furnished, two and three are not

**Downloading. Service discovery. Keep-awake. Caller identity.**

This is the whole of the interesting work.

*Downloading:* every platform can fetch bytes; none tells you what transfers
exist that you did not start, or lets you act on one. Built.

*Keep-awake:* `SetThreadExecutionState` and friends serve seat one everywhere.
Observation is admin-only, agency is a name-based global override with no API,
and holding wake **on behalf of a process that never learned the call** exists
nowhere — though that is the common case, because the program pinning your
machine awake rarely thought about power.

*Caller identity:* a local service can already learn who is calling it, and on
every platform the channel itself carries an authoritative answer — the kernel
stamps it, the caller cannot assert it. Where the platforms diverge is **what
kind of identity** is authoritative:

| | which **user** | which **process** | which **application**, verifiably |
|---|---|---|---|
| Linux | `SO_PEERCRED`, stamped by the kernel at `connect()` | `SO_PEERPIDFD` since 6.5 — a race-free handle | **no standard answer** |
| Windows | `ImpersonateNamedPipeClient` → access token | pid + token | package identity for MSIX; a path otherwise |
| macOS | audit token | audit token | code signature / team ID |

The first two columns are solid everywhere. The third is the real gap, and LWN
put it exactly, of pidfd: *pidfds gave a race-free way to identify a process,
and still no standardized way to figure out what that process actually was.* On
Linux "which application" fragments per sandbox — Flatpak reads
`/proc/$PID/root/.flatpak-info`, Snap is different, Firejail different again.

Two things follow, and both are design input rather than trivia.

**The strongest platform has the sharpest edge.** On macOS the obvious call,
`xpc_connection_get_audit_token`, is not public API and was exploitable by audit
token spoofing; `SecCodeCreateWithXPCMessage` is the supported route. A facade
that makes the easy thing the wrong thing has failed at its only job.

**Do not ask the connection — bind identity when the channel is handed out.**
This is what Wayland and D-Bus concluded after meeting the same wall: give each
application instance its own socket with metadata attached at creation, rather
than interrogating a peer afterwards. It is also the only shape that degrades
honestly, because the after-the-fact question is the one Linux cannot answer.
An earlier draft of this file assumed per-call interrogation; that was wrong.

Under `METHOD.md` §4 the third column is not an embarrassment to paper over. A
caller that needs a verified application identity must be able to learn it
cannot have one here.

### Kind C — there is no seat because there is no service

**Federated tokens. Licence validation. Revocation.**

Issuing a credential another *machine* will honour, and withdrawing it, is not a
facade over anything local. It is an identity provider: its own trust model, key
custody and attack surface.

Not rejected — **not this**. The test is whether the dependency can be carried:
a library or a user-scope helper ships with the adopter, and a server on someone
else's machine cannot. Build it as a service that *adopts* these abstractions,
which is the right relationship anyway.

### Rights and ownership, split correctly

The single most useful thing this section does is refuse to treat "a rights and
ownership service" as one item:

- *which app is asking* — **Kind B**, buildable now, table above.
- *what that app may do here* — **Kind B**, and the strongest case in this
  document. Windows policy, Android permissions, iOS entitlements and macOS TCC
  are four answers to one question, each with its own control panel and none
  reachable from an application asking on its own behalf. That is exactly the
  situation SLF4J was born into: log4j, java.util.logging and logback, three
  configurations for one act.
- *a token another machine will honour* — **Kind C**. See above.

The first two are ours. The third is not, and bundling them is what made the
whole idea look unbuildable.

### The instructive middle

**Secret storage** is close to Kind A on Windows and macOS — DPAPI / Credential
Manager, Keychain, both furnishing more than seat one — and Kind C on Linux,
where there is no single thing to face. A layer that is nearly-A on two
platforms and C on the third has its scope cut to where it is honest, or is not
taken.

### The rule

> A layer enters this library only if seat two or seat three is missing, and
> the seat can be furnished by something the adopter is able to **carry**.

### What "carry" allows, and what it does not

An earlier draft said "without requiring a server to exist", which was too
blunt. A Windows application that needs the C++ runtime does not degrade
gracefully when it is absent — it carries an installer, and nobody calls that a
design failure. This repository already makes that trade: the C++ builds link
`/MT` specifically so the result does not need the redistributable installed.

The distinction that actually matters is **what kind of thing is being
depended on**, because a library and a daemon fail differently:

| dependency | may a layer require it? | why |
|---|---|---|
| a library the adopter links or bundles | **yes** | no lifecycle. It is present because the adopter is present. |
| a helper process the library can start itself, user-scope | **yes** | recoverable without privilege; it is what `jobd` already is |
| a system service needing admin | **only as an upgrade** | not installable on a locked-down machine, and the adopter cannot fix that |
| a server on another machine | **no** | this is Kind C |

A daemon has a lifecycle and a library does not, and every lifecycle is a
failure mode: it can be stopped, crash, fail to start, be blocked by policy, or
be running twice over one store. This project has burned on all but the last —
and on the last too, which is why `nas-deploy.sh` refuses to start a second
supervisor. **Prefer the library. Require the daemon only for what a library
genuinely cannot do**, which for a channel means almost nothing, since the
platforms already provide the transport.

The rule this replaces was protecting something real, and it survives in
narrower form: **a capability that varies by environment must degrade, because
it cannot be carried.** Nobody can bundle a NAS. That is why "presence is the
configuration" applies to `download` and does not apply to a channel
implementation, which ships with whoever ships the client.

### Carried first, shared later — and both, permanently

Carrying a private copy is how you get in the door. A shared system component
installed once is where it should end up. These are not alternatives to choose
between; they are a **sequence**, and getting the order backwards is how a
project like this dies.

SLF4J is the evidence. It won because a single application could adopt it
*alone* — add one jar, keep whatever logging was already there, no coordination
with anybody. Had adoption required a system-wide install first, nobody would
have taken the first step, and there would have been no second. **The shared
install is what you get after you have won, not what you ask for before.**

The C++ redistributable is the model for the destination: not copied into every
application, but a versioned side-by-side component that each installer puts
there if it is absent, installs only if newer, and never removes. .NET is the
model for the whole shape, because it ships *both* — a shared framework and
self-contained deployment — and keeps the second permanently, precisely because
the shared assumption fails in environments nobody controls.

So: **both modes, forever.** Self-contained is the ice-breaker and the fallback
for locked-down machines; shared is the optimisation once the framework is
common enough to assume. An adopter switches between them without changing a
line, or the abstraction has failed at the thing it exists for.

Three questions that must be answered before the first release, not after,
because each one is a decision the wire format encodes:

- **Version skew.** Two adopters carrying different copies will still have to
  talk. That is DLL hell rebuilt inside the abstraction. The record format
  already answered this with content sets — declared and checked, never assumed
  — and a carried layer needs the same answer.
- **Ownership of the shared copy.** If Lemonade's installer put it there and
  Lemonade is uninstalled, ComfyUI must not break. The known answers are
  reference counting or install-and-never-remove; pick one deliberately.
- **Discovering which mode is in play.** An adopter must be able to find the
  shared framework and fall back to its carried copy without asking the user,
  which is the same discovery problem `job` already solves for the store.

---

## 4. Small is the point

"There is no end to the concepts that can sprout from lower-level building
blocks" is true, and it is the **risk**, not the promise.

A standard library of interfaces earns the name by being small and finished.
SLF4J is one interface with five methods, and it won. The value on offer here is
subtractive: the discipline of refusing to name the binding. Ten half-built
layers destroy exactly that, because each one is a place a binding leaked in
while attention was elsewhere.

> **At most one new layer at a time, and a new one does not start until an
> existing layer has a second adopter that is not ours.**

By that rule, today, we have not earned a new layer. The ComfyUI node and the
Lemonade fork are both ours.

---

## 5. Order, when the rule is satisfied

1. **service discovery** (Kind B). Already half-built inside `job`, which is
   where it does not belong. Extracting it is the honest move, not a new idea,
   and it is the cheapest way to find out whether the method survives being
   applied to something already written.
2. **keep-awake** (Kind B). Not the warm-up an earlier draft called it. Seat one
   is a two-day shim; the layer is seats two and three — *who is holding this
   machine awake, may they, and can I hold it for a process that never asked* —
   and none of the three platforms answers those without privilege. The third
   question is the one with no prior art anywhere, which makes this the sharpest
   available test of whether the method generalises past transfer.
3. **caller identity** (Kind B). The narrow, buildable half of rights: a local
   service learning who is calling it. User and process are authoritative
   everywhere; *verified application* is answerable only on macOS and for
   packaged apps on Windows. So it is the first layer whose honest facade must
   expose a difference in fidelity rather than hide it — and the first whose
   shape is decided by that: identity gets bound when a channel is handed out,
   not asked for per call.

Then, and only with an adopter demanding it: *what may this app do here*.

Deliberately not yet: federated tokens, licence validation, revocation —
anything Kind C. Secret storage waits behind a decision about Linux.

---

## 6. Layers are discovered, not designed

The strongest claim in this project is that implementing a layer inside somebody
else's real program is what crystallises it. That is not aspiration. It is what
the record here already shows:

- `Intent` — desired state separate from observed — does not exist because
  anyone designed it. It exists because Lemonade's pause button wrote a terminal
  cancel to a shared record while a NAS carried on and finished a 3.1 GB model
  nobody would collect. Three participants, one store, three answers.
- The two-phase `TRANSFERRED` → `COMPLETE` ending exists because a supervisor
  swept a finished job as an orphan and a NAS re-downloaded 313 MB every thirty
  seconds, indefinitely.
- Content sets replaced a version integer because two implementations
  disagreed about a record neither could refuse safely.

None of those were foreseen. Each was found by an adopter and cost real bytes.

This sets the entry bar as well as the method:

> **You may not add a layer you have not been forced into by a real adopter.**

See `METHOD.md` §5 for how the drawing is then done.

---

## 7. What is here now

| layer | implementations | state |
|---|---|---|
| `job` | Go, Python, C++ | contract proven across three languages and two bindings |
| `download` | Go, Python, C++ (reader) | delegation to a NAS proven end to end |
| `storage` | Go, C++ | content-addressed |
| `config` | Go | |
| `logging` | Go, Python | |
| `model` | Go | thinnest; least exercised |

Adopters: a Lemonade fork and a ComfyUI custom node. **Both ours** — see §4.

`README.md` holds the honest state of each in detail.
