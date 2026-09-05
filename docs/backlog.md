# Backlog

Ideas worth keeping and not worth building yet. Five lines each: an idea that
needs a document is either being decided now, in which case it gets one, or it
belongs here. Entries are dated. Nothing here is a commitment.

Technical design notes only. Market judgments and adoption-target assessments
belong in the private strategy repository, not here.

---

## Accountability now, enforcement later - 2026-09-05

No platform lets us enforce per-app permissions between processes of one user at
one integrity level. So do not claim it, and do not withhold the feature either.

- **Put the truth in the sentence, not a footnote.** Not "Allow LogViewer to read
  the System log" with an asterisk, but "Anything you run can already read this.
  Recording that LogViewer asked." A red asterisk is the part people do not
  read.
- **What we ship today is accountability, not access control**, and that is not a
  consolation prize - most enterprise security is accountability. Audit logs do
  not stop anyone either. The interface does not change as platforms harden; the
  guarantee behind it strengthens, which is the `Proof` ladder gaining a rung.
- **Users click through prompts without reading, and always will.** The
  conclusion is not that wording is pointless, it is that *the design must not
  depend on them reading*: ask rarely, make "yes" the narrowest grant rather than
  the broadest, and put the truth where it is found later. That is the monitor,
  and it is why the monitor matters more than the prompt.

**Strict mode, for people who want it.** Default permissive; the switch lives in
the monitor, which is where somebody learns enough to want it. What strict mode
enforces is real because it needs no control over the caller, only over
ourselves:

> We cannot stop a process being injected into. We can refuse to talk to
> processes that are easy to inject into.

No verifiable identity, no service - MSIX package identity on Windows, a valid
signature on macOS. No mitigation policies enabled, no service. Unsigned, no
service. And it must say what it refused: "blocked LogViewer: unsigned". A
feature that silently does not work is one people switch off.

That is also the adoption incentive, in its honest form. An app wanting to serve
strict-mode users has to be signed, packaged and hardened - a gate it can walk
through, not a nag screen shaming it. The pressure comes from users who chose
strict, never from us.

---

## A polite monitor - 2026-09-05

An app that calmly answers "what is my computer doing?" - what is holding the
machine awake, what is downloading, what is reading which logs. Existing tools
are developer instruments or security products that alarm you; nothing just
tells you. This is seat two made into a product.

- **It fixes adoption from the other end.** Show system-level truth for
  everything, and richer detail for anything that came through our layers: an
  adopter reads "Qwen2.5, 3.1 GB, 40%, resumable, handed to the NAS" while
  everything else reads "chrome.exe is using the network". The payoff for
  adopting is visible, and it is never a warning about who did not - that is the
  nag screen, and untrue besides.
- **Two products hide here.** What *our* layers know is honest, buildable, and
  currently shows almost nothing. What the *system* knows needs the privileged
  service and inherits every limit in `logs-reading.md`. They are one product;
  the second is what makes the first worth opening.
- **The hardest rule comes from the log survey**: an empty result has nine
  causes. A monitor that shows nothing when it *cannot see* is worse than
  useless. "I cannot see this without administrator" is the only honest answer.
- **It is the first thing a person judges on looks**, and the first sentence
  about this project that someone could repeat to a friend.

Side project until the layers underneath are adoptable. Building the visible
thing on a library that gives a stateless caller zero resume only moves the
problem somewhere more embarrassing.

---

## A destination is a path, and a path is one machine — 2026-09-05

Not speculative: it is why the ComfyUI node cannot delegate. Its own docstring
says a job handed to a NAS would name a directory that exists on the PC and not
on the NAS, so the bytes land nowhere and the caller waits for a file that is
never coming. Every delegation problem traces to sink.final being an absolute
path.

The requester should say what it needs available, never where. Placement is the
store'''s decision -- local disk, the NAS, both -- and a path is materialised only
when something opens a file. Content addressing already makes location a
property of a copy rather than of identity, so the pieces exist. Nix'''s
substituters are the closest prior art: they answer where can I get this, not
here is the file. Folds into [[accessibility]].

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
