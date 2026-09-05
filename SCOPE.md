# SCOPE

What this is a library of, what qualifies for it, and in what order.

`METHOD.md` says how an interface is drawn. This says which interfaces are ours
to draw at all.

## 1. Not a library about AI

The first adopters are local-AI tools, because that is where the pain is
loudest: they move tens of gigabytes, they run on somebody's desk, and there
are dozens of them reimplementing the same few services. That is the sampling
frame, not the subject.

Nothing in `job`, `download` or `storage` mentions a model, and nothing should.

## 2. The gap

Cross-platform runtimes stopped at the system call. Files, sockets, threads,
processes, clocks, memory — Java, .NET, Go, Qt and Rust all give you a portable
API for every one of them, and have for twenty years. None of them gives you a
portable answer to:

- what work is in flight on this machine, including work that predates me
- is there something here that fetches bytes better than I can
- who is asking, and may they

Those are **services**, one level up. A portable file API could be specified
once because kernels converged on what a file is; services never converged.
They arrived per-vendor, per-platform and late, and an honest facade over one
has to admit that the thing behind it may not exist at all.

### Three seats

Platform services are built for the cooperating process declaring its own
intent. Almost none of them offers a seat to a third party.

| seat | question | provided by platforms? |
|---|---|---|
| **self-assertion** | I declare what I want | always |
| **observation** | what is everyone else doing, and why | rarely, and privileged |
| **agency** | act on it, or for someone who never asked | essentially never |

Keep-awake demonstrates it. `SetThreadExecutionState`,
`IOPMAssertionCreateWithName` and `systemd-inhibit` all serve seat one. Seat
two exists but is privileged: `powercfg /requests` requires administrator, so
an ordinary application cannot find out what is holding the machine awake. Seat
three barely exists — `powercfg /requestsoverride` denies by process name,
machine-wide, from an elevated command line, with no API — and nothing on any
platform lets you hold wake *for* a process that never learned the call.

`job` and `download` are instances of the same gap. Every downloader serves
seat one. This library exists because nothing served seats two and three.

> **A layer is worth building where seat two or seat three is missing. Wrapping
> seat one is not worth doing.**

## 3. Classifying a candidate

Candidate services — downloader, service discovery, secret storage, logging,
licensing, rights and ownership, keep-awake — read like one idea, and treating
them as one idea is the fastest way to build nothing.

Classify by **which seat is missing**, never by the noun. A noun bundles
capabilities that belong in different kinds; a seat does not.

**Kind A — every seat is already furnished.** Nothing qualifies today. A
capability whose observation and agency the platforms already provide needs a
portability shim, not this library. The category is kept because it is the test
a candidate has to fail.

**Kind B — seat one is furnished, two and three are not.** Downloading,
service discovery, keep-awake, caller identity. This is the whole of the
interesting work.

**Kind C — there is no seat because there is no service.** Federated tokens,
licence validation, revocation. Issuing a credential another *machine* will
honour, and withdrawing it, is an identity provider: its own trust model, key
custody and attack surface. Not rejected — **not this**. Build it as a service
that adopts these abstractions.

### Rights and ownership, split correctly

The most useful thing this section does is refuse to treat "a rights and
ownership service" as one item:

- *which app is asking* — **Kind B**, buildable. Backlog: caller identity.
- *what that app may do here* — **Kind B**, and the strongest case in this
  document. Windows policy, Android permissions, iOS entitlements and macOS TCC
  are four answers to one question, each with its own control panel and none
  reachable from an application asking on its own behalf. That is the situation
  SLF4J was born into: log4j, java.util.logging and logback, three
  configurations for one act.
- *a token another machine will honour* — **Kind C**.

The first two are ours. The third is not, and bundling them is what made the
whole idea look unbuildable.

### The instructive middle

Secret storage is close to Kind A on Windows and macOS — DPAPI and Credential
Manager, Keychain, both furnishing more than seat one — and Kind C on Linux,
where there is no single thing to face. A layer that is nearly-A on two
platforms and C on the third has its scope cut to where it is honest, or is not
taken.

### The rule

> A layer enters this library only if seat two or seat three is missing, and
> the seat can be furnished by something the adopter is able to **carry**.

### What "carry" allows

A daemon has a lifecycle and a library does not, and every lifecycle is a
failure mode: it can be stopped, crash, fail to start, be blocked by policy, or
run twice over one store.

| dependency | may a layer require it? | why |
|---|---|---|
| a library the adopter links or bundles | **yes** | no lifecycle. It is present because the adopter is present. |
| a helper process the library can start itself, user-scope | **yes** | recoverable without privilege; this is what `jobd` is |
| a system service needing admin | **only as an upgrade** | not installable on a locked-down machine, and the adopter cannot fix that |
| a server on another machine | **no** | this is Kind C |

Prefer the library. Require a daemon only for what a library cannot do.

A capability that varies by environment must degrade, because it cannot be
carried — nobody can bundle a NAS. That is why "presence is the configuration"
applies to `download`.

## 4. Small and finished

A standard library of interfaces earns the name by being small. SLF4J is one
interface with five methods. Ten half-built layers destroy the only thing on
offer here, because each one is a place a binding leaked in while attention was
elsewhere.

> **At most one new layer at a time, and a new one does not start until an
> existing layer has a second adopter that is not ours.**

By that rule, today, no new layer has been earned. The ComfyUI node and the
Lemonade fork are both ours.

## 5. Order, when the rule is satisfied

1. **Service discovery** (Kind B). Already half-built inside `job`, where it
   does not belong. Extracting it is the cheapest test of whether the method
   survives being applied to something already written.
2. **Keep-awake** (Kind B). Seat one is a two-day shim; the layer is seats two
   and three, and no platform answers those without privilege. Holding wake on
   behalf of a process that never asked has no prior art anywhere, which makes
   this the sharpest available test of whether the method generalises past
   transfer.
3. **Caller identity** (Kind B). A local service learning who is calling it.
   User and process are authoritative everywhere; verified *application*
   identity is answerable only on macOS and for packaged apps on Windows, so
   this is the first layer whose facade must expose a difference in fidelity
   rather than hide it. Design notes are in `docs/backlog.md`, not here.

Then, and only with an adopter demanding it: *what may this app do here*.

Not yet: federated tokens, licence validation, revocation — anything Kind C.
Secret storage waits behind a decision about Linux.

## 6. Layers are discovered, not designed

Each of the following exists because an adopter forced it, not because anyone
designed it:

- `Intent` — desired state kept separate from observed state — exists because
  a pause button wrote a terminal cancel to a shared record while a delegate
  carried on and finished a model nobody would collect.
- The two-phase `TRANSFERRED` → `COMPLETE` ending exists because a supervisor
  swept a finished job as an orphan and a delegate re-downloaded it on a loop.
- Content sets replaced a version integer because two implementations
  disagreed about a record neither could refuse safely.

> **You may not add a layer you have not been forced into by a real adopter.**

`METHOD.md` §5 says how the drawing is then done.

## 7. What is here now

| layer | implementations | state |
|---|---|---|
| `job` | Go, Python, C++ | one contract, three languages, three bindings |
| `download` | Go, Python, C++ (reader) | delegation to a NAS proven end to end once |
| `storage` | Go, C++ | content-addressed |
| `config` | Go | |
| `logging` | Go, Python | |
| `model` | Go | thinnest; least exercised |

Adopters: a Lemonade fork and a ComfyUI custom node. Both ours — see §4.

`README.md` has the per-layer status and what is missing from it.
