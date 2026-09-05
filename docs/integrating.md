# What a real application taught the abstraction

Everything below was learned by putting the job layer into Lemonade — 130k lines
of C++ that had never heard of it — over one night. None of it came from
designing. Each item cost hours and at least one wrong turn, so it is written
down in the form that would have saved them.

The work lives in a fork, on a branch:
`github.com/ReinisLusis/lemonade` → `abstraction-job-store`.

---

## 1. Two executors will always disagree

Our sample applications submit a job and a **Runner** owns the transfer: it opens
the partial, resumes from the proven prefix, hashes as it goes, verifies, and
delivers. Resume policy, progress and verification are one thing in one place.

Lemonade's downloader is a synchronous function with libcurl inside it. The first
integration bolted the job record alongside as an *observer* — so libcurl
executed and the record watched.

That is two executors for one transfer, and every bug that night was them
disagreeing:

| | libcurl | our Runner |
|---|---|---|
| decides resume | a `resume_partial` flag | the proven prefix |
| verifies | once, at the end | rolling hash over kept bytes |
| reports progress | a callback | the record |

Trying to switch executor mid-transfer meant re-plumbing every one of those by
hand, and they drifted immediately.

**The rule:** an abstraction either owns the operation or observes it. Owning it
and observing it in alternation is neither.

## 2. One piece of work, one record

The first design gave the application its own store under its cache directory
while the supervisor watched the machine's. Handing a download over then meant
**copying a job from one store to the other** — and a copy is where it broke:

- the new record had no checkpoint, so the supervisor read a proven prefix of
  zero, **truncated the partial it had just been handed**, and re-fetched bytes
  already on disk. Strictly worse than not handing over at all.
- patching that meant carrying the checkpoint across by hand, which is a
  reconciliation problem that should not exist.
- moving the cache directory moved the store with it, and every in-flight
  download became invisible while its bytes sat there.

One store per machine, found by discovery, and the copy disappears along with
everything it dragged in.

## 3. The provider must not change what the operation means

`resume_partial = false` gated only the local path. The supervisor resumed from
the checkpoint and never saw the flag — so the same download restarted when the
application fetched it and resumed when a supervisor did.

That is worse than either behaviour alone, because which one you got depended on
whether a supervisor happened to be running when you clicked.

**A provider that changes the semantics of the operation is not an
implementation of an abstraction. It is a second abstraction wearing the same
name.** Intent has to cross the boundary, or the boundary is not real.

## 4. Hand over when the owner dies, not mid-call

The elegant integration turned out to be *smaller* than the clumsy one, and it
needed no new mechanism at all.

There is no handoff. The application downloads normally and records progress into
the shared store. If it is closed or crashes, its lease lapses and the supervisor
**adopts the orphan** — and may pass it to a NAS. The application's next start
sees it finished, or resumes it.

The two systems never contend over a live transfer. They exchange work only at
the one moment it is unambiguous: **when the owner is gone.** That is what the
lease was for.

Verified with a record shaped exactly like the application writes, lease lapsed:
the supervisor adopted it and delegated it onward without either side knowing
about the other.

## 5. The opaque spec earns its keep across languages

That same record carried five keys Go had never heard of — `group_id`,
`display_name`, `file`, `file_index`, `total_files` — and Go read it, ran it, and
wrote it back untouched. The discipline that keeps the job layer ignorant of what
a download is is what let a C++ application extend the spec without asking
anybody.

## 6. Look for the job engine that is already there

Lemonade has a server-side job system: named steps, a status, a cursor, and a
`pause / interrupt / resume / delete / query` lifecycle **that survives client
disconnect and server restart**. Ops are a pluggable registry, explicitly
documented as extensible.

Downloads are the one operation not modelled in it — they live in a parallel
list with memory-backed state, which is exactly why they are the thing that loses
progress.

Hours went into building a second job system beside the existing one before
anybody looked. **Before integrating, find out what the host already calls a
job.** The right long-term shape is a `download` op in the engine the application
already has, sitting on the durable substrate underneath.

---

## What comes next, and what each part tests

The road ahead is mostly not downloading, which is the point: every item below
exercises the abstraction from a direction downloading never would.

| next | what it tests |
|---|---|
| Windows service | a supervisor that outlives logon, not just the app |
| Installer | discovery with nothing configured by hand — the SLF4J property |
| Linux service | the same supervisor contract on a second OS |
| Improved NAS installers | the third tier, deployed by somebody who is not us |
| Popups / UI | jobs as something a person watches, pauses and resumes |
| Testing | the conformance surface, across three languages and two machines |

`job`, `download` and `store` all get exercised by that list. A store that only
ever holds downloads has not been tested as a store.
