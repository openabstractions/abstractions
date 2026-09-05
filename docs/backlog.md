# Backlog

Ideas worth keeping and not worth building yet.

Five lines each. An idea that needs a document is either being decided now — in
which case it gets one — or it is here. This file exists because the alternative
was writing a document per insight, which was consuming the budget and building
inventory ahead of a single external adopter.

Entries are dated. Nothing here is a commitment.

---

## Accessibility: where an artifact actually lives — 2026-09-05

I ask for a model. Do I need it on SSD, or is the NAS fine, or do I want it in
RAM, or compressed in RAM, or in all three at once? Who decides, and who moves
it when the answer changes?

- **The enabling property already exists.** Content addressing means a location
  is not part of an artifact's identity — it is a property of a *copy*. `storage`
  can already hold N copies in N media that are provably the same object.
- **Same shape as `download`.** Download says *I want these bytes, I don't care
  who fetches them.* This says *I want them at latency X, I don't care where they
  sit.* Both declare a requirement instead of a mechanism, which suggests it is a
  real layer rather than a feature.
- **The taxonomy question decides everything** (`SCOPE.md` §3): a facade over
  tiering that already exists — page cache, SSD, NAS, `mmap` — is Kind B. A
  policy engine that *decides* what to promote and evict is a new system, Kind C,
  and much larger than it looks.
- **Shares a prerequisite with `docs/incremental-delivery.md`.** "Half in RAM,
  the rest on the NAS" is only expressible if pieces are the unit. Piecewise
  digests unlock both.
- Prior art to survey before anything: HSM, CDN tiering, ZFS L2ARC, page cache,
  `madvise`, and Nix's substituters — the last being closest, since it answers
  *where can I get this* rather than *here is the file*.

---

## Channel layer: capability vs identity — 2026-09-05

Decided in `research/ipc/SUMMARY.txt`. Recommendation is capability-passing over
platform-native transports, with platform identity used only at bootstrap.
Blocked deliberately by `SCOPE.md` §4 — no new layer until an existing layer has
an adopter who is not us. Open risk: revocation and enumeration.

---

## Progress from a delegate on short transfers — 2026-09-05

The 112 MB NAS run sat at 0% and jumped to 100%: the transfer finished inside
one checkpoint interval, so nothing proves mid-flight progress propagates on
short downloads. Multi-gigabyte runs did show it climbing. Needs a run sized
between the two, or a shorter checkpoint interval while observing.

---

## `test_download_layer` cannot be rebuilt — 2026-08

The binary runs and passes; its source was left in a temp directory that no
longer exists. A test nobody can rebuild is an artifact. Rewrite it or delete
it — currently it is evidence of nothing.
