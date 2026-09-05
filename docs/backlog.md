# Backlog

Ideas worth keeping and not worth building yet. Five lines each: an idea that
needs a document is either being decided now, in which case it gets one, or it
belongs here. Entries are dated. Nothing here is a commitment.

---

## User interface: web first, native last — 2026-09-05

Web UI until the contract underneath is proven, then native per platform, thin
and signed. Deferring costs nothing in security: no platform lets a third party
draw a prompt a user can distinguish from a fake, so trust comes from caller
identity and the ACL'''d channel, never from the pixels. Little Snitch reached the
same conclusion and ships a local web page on Linux rather than faking native
chrome. Native is polish, and polish is last.

---

## The checkpoint is one integer, and that is the ceiling — 2026-09-05

`Checkpoint` holds `VerifiedPrefix int64`. That expresses a contiguous prefix
and nothing else, so the library can only ever describe a single sequential
stream. Every serious downloader fetches in parallel ranges — aria2,
hf_transfer, s5cmd, and Ollama, whose sixteen concurrent parts land at scattered
offsets in a sparse file. "Parts 0, 2 and 5 done; 1, 3 and 4 at forty percent"
has no representation here.

An investigation of Ollama as an adopter concluded that taking this library
would mean deleting their parallelism, which no maintainer would accept. Not an
impedance mismatch at the edges: the central structure is one number where the
adopter needs a vector.

Three independent findings now converge on the same fix, which is the strongest
signal this project has produced about its own design:

- `docs/incremental-delivery.md` — following a delegate's partial needs pieces
- `research/transfer/libtorrent.txt`, written weeks earlier, called a manifest
  of independently verifiable pieces "the data model this abstraction needs"
- Ollama — parallel ranges cannot be a prefix

**The change**: a checkpoint expresses a set of verified ranges, with the single
prefix as the degenerate case so nothing already written breaks. It is additive,
it is a new content model rather than a format break, and it unblocks piecewise
digests, following a delegate mid-transfer, and every parallel-chunk adopter at
once.

Until it lands, this library fits single-stream callers only, and the README
should say so rather than letting adopters discover it by trying.

---

## Ollama is not the adopter, and the pain was smaller than reported — 2026-09-05

Six issues over 28 months, all closed, three reactions between them, and three
of the six share one root cause already fixed in main by a one-hour grace period
before pruning. Ollama has had resume since 2023 and maintainers have said so
consistently; a pull request proposing it would be closed as already done.

Their real remaining gap is that a checkpoint is written once per chunk attempt,
so a network error discards bytes already on disk and the progress bar runs
backwards. The honest fix is theirs and is thirty to fifty lines in
`server/download.go`. It needs no dependency, and it is the shape of change they
demonstrably merge — an outside contributor landed a fix in that exact file in
two days.

Reachability, measured: 331 merged pull requests in four and a half months, of
which seventeen were from genuine outsiders. Roughly seven percent. The ones
that land are small, tested, and narrow.

---

## A stateless caller gets no resume at all — 2026-09-05

Found by trying to adopt the library into a one-shot CLI. It models resume as
job-record continuity, keyed by job id. A CLI models it as file continuity,
keyed by destination. Every invocation called `submit()`, got a new record with
`verified_prefix` of zero, and sent no Range request — measured, not inferred.

The proof it belongs in the library: both of our own adopters already wrote the
missing piece themselves. The ComfyUI node has `_resume_or_submit` ("two runs
fetching the same file are the same work") and the Lemonade fork has the same
logic in C++. Two adopters reinventing one function is the function's
specification. **Find-or-create by destination must be a library operation.**

Four more from the same attempt, each an adoption barrier rather than a bug:

- **Atomic delivery is unreachable.** Temp file and rename is the single thing
  that adopter genuinely lacked, and it is private to `Runner`. The most
  valuable primitive we have cannot be used without taking the whole store.
- **We impose our HTTP stack.** The library fetches with `urllib`, so a caller
  built on `requests` gets different exception types and its test mocks stop
  intercepting. The transport must be injectable.
- **No integrity without a digest.** Our only guarantee is a caller-supplied
  `sha256:`, and most APIs do not hand one back. The adopter had built
  ETag/If-Range checking precisely to catch a remote object changing between
  attempts; we cannot detect that at all.
- **It cannot be vendored.** `from abstraction_job import ...` is flat and
  absolute, so dropping the files under a namespaced path requires editing them.

**Verdict from the attempt: not ready for an outside adopter, and not close.**
That maintainer had spent five weeks hardening the same code by hand with
nineteen tests pinning its semantics. No PR was opened, correctly.

---

## The lease rests on a primitive tested once — 2026-09-05

Mutual exclusion is an `O_EXCL` claim token, which `store.go` calls atomic on
NTFS and POSIX. NFS is the documented exception and a network share is the
binding this exists for; it has been checked against one Synology over SMB. A
survey of server operators found the same assumption failing in production one
layer up — 125 Kubernetes workers, `flock` over NFS, each holding its own
"exclusive" lock, about 30% of datapoints lost. The fix is not a cleverer
primitive: a store should prove exclusivity on the filesystem it is on, by
racing creators for one name at setup, and refuse to be shared when it cannot.

---

## Keep-awake: what an existing Windows tool already knows — 2026-09-05

Delegation destroys the OS guarantee that a dying process releases its lock, so
a TTL is mandatory — the lease, reached independently in unrelated code.
`PowerRequestExecutionRequired` cannot be granted on behalf of another process
at all, which rules out the design a specification would have promised. `system`
and `execution` intent must stay separate. Adopting a facade here is a shim
until a second platform implements the same delegated-hold contract.

---

## Server operators: one gap, and it is ours — 2026-09-05

Retry and caching are solved for this audience by `hf_transfer`, aria2, s5cmd
and registries; Dragonfly owns fleet-scale dedup. What remains is coordinating
one download across machines sharing a destination, so a second node does not
restart a fetch already in flight. That is the job store. Concentrated in
small-to-mid ML platforms on Kubernetes. Pursue after desktop, not before.

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
