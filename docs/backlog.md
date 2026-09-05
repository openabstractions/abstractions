# Backlog

Ideas worth keeping and not worth building yet. Five lines each: an idea that
needs a document is either being decided now, in which case it gets one, or it
belongs here. Entries are dated. Nothing here is a commitment.

---

## Accessibility: where an artifact actually lives — 2026-09-05

Ask for a model and get it at a stated latency — RAM, SSD, NAS — without saying
where it sits. Content addressing already makes location a property of a copy
rather than of the artifact, so `storage` can hold N provably identical copies.
Kind B if it faces tiering that exists; Kind C if it decides what to evict.
Prior art first: HSM, CDN tiering, ZFS L2ARC, `madvise`, Nix substituters.

## Caller identity: what a platform can prove about a caller — 2026-09-05

Which *user* and which *process* are authoritative everywhere (`SO_PEERCRED`,
`SO_PEERPIDFD`, `ImpersonateNamedPipeClient`, macOS audit token). Which
*application* has no answer on Linux, is packaged-only on Windows, and needs
`SecCodeCreateWithXPCMessage` on macOS. Bind identity when the channel is handed
out, as Wayland and D-Bus do; interrogating a live peer is the unanswerable one.

## Carried copy and shared install — 2026-09-05

Ship both, permanently: self-contained gets the first adopter in, a shared
side-by-side component is where it should end up. Three questions the wire
format encodes and that must be answered before a first release: version skew
between adopters carrying different copies, who owns the shared copy when its
installer is uninstalled, and how an adopter discovers which mode is in play.

## Channel layer: capability vs identity — 2026-09-05

Decided in [`research/ipc/SUMMARY.txt`](https://github.com/openabstractions/research/blob/main/ipc/SUMMARY.txt).
Recommendation is capability-passing over platform-native transports, with
platform identity used only at bootstrap. Blocked by `SCOPE.md` §4 — no new
layer until an existing layer has an adopter who is not us. Open risk:
revocation and enumeration.

## Progress from a delegate on short transfers — 2026-09-05

The 112 MB NAS run sat at 0% and jumped to 100%: the transfer finished inside
one checkpoint interval, so nothing proves mid-flight progress propagates on
short downloads. Multi-gigabyte runs did show it climbing. Needs a run sized
between the two, or a shorter checkpoint interval while observing.

## `test_download_layer` cannot be rebuilt — 2026-08

The binary runs and passes; its source was left in a temporary directory that no
longer exists. A test nobody can rebuild is an artifact. Rewrite it or delete
it — as it stands it is evidence of nothing.
