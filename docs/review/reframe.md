# Reframe — the constructive half of Cold Shower #1

Task: reframe after T-074 · Date: 2026-08-15 (UTC) · Author: @architect
Input: `research/cold-shower-1.md` (facts verified by CTO; its *strategic* verdict —
"abandon the abstraction, ship small tools" — treated as one hostile opinion, not binding).

This document does what the review skipped: it keeps the destination and makes the road
walkable. It answers six questions and nothing else. Every claim below is either
measurable or marked as a hypothesis with the observation that kills it.

**Position in one sentence:** the review is right that we have no falsifiable claim, no
first user, and no evidence; it is wrong that the answer is five unrelated tools — the
primitives it praised are eight expressions of *one* invariant, and that invariant is
testable by measuring whether the second adoption costs an adopter less than the first.

---

## 1. The abstraction claim, restated so it can be killed

### 1.1 What we said (retract)

> "Abstraction is not a preference. It is the logical solution — derivable by reason.
> If a seam admits more than one defensible design, we have not found the seam yet.
> We do not pick; we derive." — `project scope.txt`, Prime principle

This is unfalsifiable by construction (every objection is re-labelled as the objector's
failure) and it is already refuted by our own repo (C1–C3, C10). **Retract it.** It is
also the wrong *kind* of claim: it is a claim about our reasoning, not about the world.
A claim about the world can be wrong, which is the only property that makes it useful.

### 1.2 What we say instead

Three claims, in dependency order. Each is about observable code in other people's
repositories, not about our taxonomy.

**Claim S (substitution) — the abstraction claim.**
> For a named function that AI tools today re-implement privately, there exists a
> host-side interface small enough to specify on one page, such that an independent
> implementer can **delete** their private implementation and call the interface instead,
> with no user-visible regression on a published conformance case.

The operative word is **delete**. An abstraction is real when it lets someone remove
code. If integrating us only *adds* — a daemon, a config file, an adapter — we did not
find a seam; we added a layer. That is the difference between an abstraction and a
middleman, and it is countable in a diff.

*Measure:* lines deleted from the adopter's tree minus lines added (including config and
process supervision). Positive = substitution. Non-positive = layer.

**Claim C (composition) — the reason it is one system and not five tools.**
> Adopting a second of our interfaces costs an adopter **less** than adopting the first,
> because all of them share one record shape, one rule (§2.1) and one default (§2.2).

*Measure:* integration cost of adoption #2 (diff size + new config keys + new processes +
new failure modes) against adoption #1, same adopter or two comparable adopters.

**Claim R (the invariant) — what the interfaces have in common.**
> At every boundary where an AI system accepts something it did not produce, the correct
> design is: the **receiver re-derives** its decision from a record it can check, and
> **absent record = refusal**. Systems that instead trust the sender's claim fail in a
> predictable, demonstrable way.

*Measure:* for each boundary, a named, currently-reproducible failure in a shipping
product caused by trusting a sender's claim (we have four; §2.3).

### 1.3 The specific observations that reverse them

| Claim | Reversed by (concrete, dated, recordable) |
|---|---|
| **S** | An independent implementer integrates interface #1 and their net diff is **not negative** — they kept their private downloader/permission model/trace format and wrapped ours. Two such adopters = S is false; we are a middleman. |
| **S** | No second implementer exists 6 months after the interface is published *and* one adopter has been asked directly and declined with a reason that is about the interface (not about time). |
| **C** | Adoption #2 measures **≥** adoption #1 on the cost measure above. Then the primitives do not compose; the hostile verdict wins and we are a tools company (which is an acceptable outcome, just not this one). |
| **R** | A boundary where "receiver re-derives" is *worse* than trusting the sender — i.e. re-derivation is impossible or ruinously expensive and the trusting design has no field failures. (Candidate already found: vendor reasoning blocks. See §2.4 — this observation already fired once, and it corrected the design instead of killing it. That is the pattern we want.) |
| **program** | After three boundaries shipped, the marginal-cost curve (Claim C) is flat or rising. Then the destination does not exist and we stop calling it one. |

Note what changed: the prime principle said disagreement was a failure to think. The new
claims say **the field, not us, decides** — and they name the row in a table where we
write "we were wrong."

### 1.4 The destination, stated concretely

We are not building "seven standards." We are trying to reach a world where a local AI
tool ships **without** a private downloader, **without** a private permission model,
**without** a private trace format, and **without** a private notion of what a model is —
because in each case there is a small host-side interface, and what it returns is
checkable. The destination is measurable in one number: **lines deleted from other
people's repositories.** Today that number is zero. Every stage below exists to move it.

---

## 2. The primitives are one system

### 2.1 One rule

> **The receiver re-derives.** A record crossing a boundary is a hint; the receiving side
> recomputes the property it cares about from bytes or schema it holds, and never accepts
> the sender's assertion of that property.

### 2.2 One default

> **Absent record = refusal.** No digest → do not load. No policy → deny. No grant →
> no capability. No measurement → do not admit. Refusal is the safe direction and it is
> always explicit (exit code + message), never a silent fallback.

### 2.3 Four boundaries (this replaces "seven seams")

A boundary is where something arrives that we did not produce. There are four in a local
AI system, and each has a *verified* failure caused by trusting the sender:

| # | Boundary | What arrives | Verified failure of trusting the sender |
|---|---|---|---|
| **B1** | **bytes** | model artifacts from a registry/peer/NAS | Ollama `:tag` opacity — you cannot tell whether two tags are the same weights; truncated GGUFs crash the loader downstream |
| **B2** | **authority** | tool descriptions and permission requests from MCP servers / subagents | MCP declares tool annotations untrusted, yet tools self-report `readOnlyHint` and hosts use it |
| **B3** | **vendor** | provider responses (reasoning, tool calls, cached prefixes) | Jan crashes on `reasoning_content` + `tool_calls`; LibreChat / Open WebUI recur on reasoning replay |
| **B4** | **time** | the loop's own past (state, memory, termination) | agent loops that ask the model "are you done?" — mutable state re-asserted rather than re-derived from the trace |

Seven was a filing order (BREAK 3). Four is a count of boundaries, and it is falsifiable:
name a fifth place where something arrives that we did not produce and it is not one of
these four. (Candidates we know of and where they land: observability = the *record of*
every crossing, not a fifth boundary; cost/quota = a record at B3; eval = the measurement
harness for all four, not a boundary.) We no longer defend a number.

### 2.4 The eight primitives, mapped

| Primitive | Boundary | Old seam | Its job in the system | Its falsifier |
|---|---|---|---|---|
| **Identity record** (`digest + format + revision + quant`) + **verify-on-open** | B1 | vault / identity | The canonical instance of §2.1: the store key is a hint, the re-hash is the proof. Every other primitive borrows this record's shape. | A user is harmed by re-hash cost (large artifacts, spinning disks) more often than they are saved by a mismatch caught. |
| **Control/data-plane separation** | all | cross-cutting | The *enabling* rule, not a peer primitive. You can only afford verification if decisions travel on a small path and bulk bytes on another. Without it, §2.1 is unaffordable. | A path where merging the planes is measurably faster and no correctness property is lost. |
| **Host-computed risk** | B2 | gate | §2.1 at the authority boundary: risk derived from `inputSchema` + host-assigned origin trust + host manifest, never from the server's self-report. | Host-derived risk disagrees with reality more often than the server's self-report does, on a sample of real MCP servers. |
| **Subtractive inheritance** | B2 | gate | Makes escalation structurally impossible: a child's grant is re-derived from the parent's set by narrowing only. Never a request the child asserts. | A legitimate workflow that cannot be expressed without widening, frequent enough that users route around the gate. |
| **Append-only fold** | B4 | client | §2.1 across time: the trace *is* the state, so each step re-derives state from the record; termination is a **declared predicate** the loop evaluates, not a claim the model makes. | Trace growth (or replay cost) makes long sessions unusable before a mutable-state design does. |
| **No-surprises posture** | all | cross-cutting | §2.2 made operational: missing gate = denial, revocation = absence, never auto-select non-loopback. | Refusal-by-default produces more user harm (blocked work, prompt fatigue) than the surprises it prevents — measured on real users, not asserted. |
| **Downloader-as-service** | B1 | vault | **Not an independent feature — a consequence.** Delegating a fetch to a NAS is only safe because identity is verifiable on arrival. It is the first *product* the invariant buys. | Users want remote fetch but do not care about verification, i.e. the two separate cleanly in the field. |
| **Opaque-record custody** (was: reasoning/tool_calls "normalization") | B3 | conduit | The primitive the review's BREAK 5 forced us to discover. Signed thinking blocks and encrypted reasoning items are records the receiver **cannot** re-derive. §2.1 therefore forbids rewriting them: carry verbatim, attribute provenance, and declare any projection as lossy. | A vendor's opaque block can be safely reconstructed by a third party — then it was never a custody problem, just a schema problem. |

### 2.5 Why that is one system and not five tools

Three shared parts, and they are shared as *code*, not as rhetoric:

1. **One record shape.** Everything above is `{ subject, claim, provenance, verification }`:
   what it is about, what is asserted, who asserted it, and how the receiver checked (or
   why it could not). The identity record is this shape at B1; a grant decision is it at
   B2; a carried reasoning block is it at B3 (verification = *none, opaque, attributed*);
   a fold step is it at B4. One shape means one serializer, one audit format, one test
   harness, and — critically — the second interface an adopter takes reuses the first
   interface's plumbing. **That reuse is Claim C, and it is what makes the abstraction
   a destination rather than a slogan.**
2. **One rule and one default** (§2.1, §2.2), so an adopter learns the semantics once.
   The refusal path, the error shape, and the "hint vs proof" distinction are identical at
   all four boundaries.
3. **One prohibition.** The envelope is *ours, internally*. On the wire we speak the
   incumbent's format — OpenAI-compatible HTTP + SSE at B3, MCP JSON-RPC at B2, SCIP if we
   ever ship B4-context, HF/LFS conventions at B1. This retires C4 (Principle #2 vs a
   neutral schema): **we never ask anyone to adopt a new wire format; we ask them to
   accept a checkable record inside the format they already use.**

### 2.6 What this reframe does to the two broken seams

- **arbiter.** Drop "single authority over hardware" (a config constant — BREAK 2) and drop
  the serving plane (unbuildable across the process boundary we chose — BREAK 4). What
  survives is *exactly* one primitive of this system: **measured admission** — measure
  VRAM/RAM from the platform (NVML / DXGI `QueryVideoMemoryInfo` / Metal residency), and
  **refuse to admit when you cannot measure** (§2.2). The OS and the driver are the
  authority; we are a well-behaved tenant that declines rather than OOMs someone else.
  That is a small, honest, checkable claim, and it is parked behind an evidence gate (§4).
- **graph.** Today it is `ctags` minus the cross-file index (BREAK 6) with SCIP
  unanswered. Under this reframe its only defensible contribution is a *provenance-carrying
  context record* — `{exact, heuristic}` is a verification field, not a confidence score —
  layered **on SCIP as the interchange format**, with ranking in scope because selection
  under a token budget is the receiver's decision and needs cost in the record. Until a
  real user asks for that, graph is parked. If nobody asks, it is dropped, and the honest
  answer to "why not SCIP" is "use SCIP."

---

## 3. First shippable: B1, the bytes boundary — a delegated, verifying model fetcher

### 3.1 Selection test

Four requirements for going first. Only one candidate passes all four.

| Requirement | vault delegation (B1) | reasoning custody (B3) | gate (B2) |
|---|---|---|---|
| Pain verified outside our repo | yes (sleeping-laptop/NAS pattern, unaddressed by everyone) | yes (named crashes) | partly (fragmented, but MCP hardening is coming) |
| We ship the **whole** fix, not half | yes — standalone useful day one | **no** — must land inside someone else's client to matter | no — needs a host and a sandbox we defer |
| Needs **zero** cooperation from an incumbent | yes | no (a merged PR is the deliverable) | no |
| Exercises the invariant end-to-end (record + verify + refusal + delegation) | yes, all of it | no (touches custody only) | partial |

**B1 goes first.** It is also the only one where our artifact is consumable by tools that
have never heard of us (§3.4) — which is the on-ramp the review correctly says we lack.

### 3.2 Who, and what hurts

**Who:** one person who runs local models on a laptop and owns a box that never sleeps —
a Synology/UGREEN NAS, a home server, or an old desktop. Secondary: a two-or-three person
team sharing that box. This person exists in numbers and is served by nobody: LM Studio and
Ollama both assume the machine downloading is the machine running.

**What hurts, precisely (three pains, all mechanical):**

1. **The 20–50 GB fetch dies.** The laptop sleeps, the VPN flaps, the hotel wifi drops.
   Their tool restarts from zero, or worse leaves a truncated file that fails hours later
   inside the loader with a message about tensors.
2. **They cannot tell if two files are the same weights.** Three machines, three tool-local
   names, three copies, no way to answer "is the one on the NAS the same as the one here?"
3. **They cannot say "fetch it over there."** No local tool accepts "download on the box
   that is awake, I will use it from my laptop."

### 3.3 What they run (exact commands, on surfaces that mostly exist)

On the always-on box:

    vaultd -addr :7777 -store /volume1/models

On the laptop:

    vault get hf://TheBloke/Mistral-7B-Instruct-v0.2-GGUF#Q4_K_M --service http://nas:7777
    vault list   --service http://nas:7777
    vault verify sha256:<hex> --service http://nas:7777

The fetch now runs on the box that is awake. `get` returns immediately with a job id; the
laptop can close. `verify` re-hashes the stored bytes and tells them the truth about pain 1
and pain 2. The digest is the answer to "same weights?" across all three machines.

### 3.4 The on-ramp (the part the specs missed, and the reason this seam is first)

A content-addressed store is unusable by a human's existing tool config: nobody points
`llama-server -m` at `a3f9c1…`. The store needs a **name-shaped projection** of itself —
the same relationship git has between its object store and a worktree:

    vault link sha256:<hex> /volume1/models/mistral-7b-q4_k_m.gguf     # hardlink/symlink, verified first

Then the user's existing tool consumes our bytes with **zero cooperation from its
vendor**:

    llama-server -m /volume1/models/mistral-7b-q4_k_m.gguf            # on the box
    curl -o model.gguf http://nas:7777/v1/artifacts/<hex>             # or pull to the laptop

This is the whole adoption theory for stage 1, and it needs no PR, no env var, and no
maintainer's permission: **the record is ours, the view is theirs.** It also gives the
first real test of Claim S — an adopter who later deletes their own downloader in favour of
`POST /v1/jobs` produces our first negative diff.

### 3.5 What must be fixed before a stranger can run this

Small, known, and all of it is repo-verified debt, not new invention:

1. **`hf://` digest resolution is broken** (T-071): `registry.HF.Resolve` never sets
   `Manifest.Digest`, so the headline command in §3.3 has never worked. This is stage 1's
   only true blocker.
2. **`vault link`** (name-shaped projection) does not exist. §3.4 is the on-ramp; without it
   there is no on-ramp.
3. **Auth on the delegation path.** Retract D-015.4 ("auth/TLS later"). A shared token +
   loopback-or-allowlist default + explicit refusal when unset (§2.2). An unauthenticated
   file-transfer service on a home LAN is the surprise our own posture forbids.
4. **Declared process dependencies.** `aria2`, `ssh` — pinned versions, checksums, and a
   clear refusal message when absent. And the honest sentence in the README (§5).
5. **Resume proof under real failure.** Kill the network mid-fetch, sleep the laptop, pull
   the plug on the box; `verify` must be right in all three cases. This is the conformance
   case Claim S is measured against.

Everything else in the vault spec — magnet, quant-as-patch, chunked dedup, the registry
abstraction — is out of stage 1. Quant-as-patch is deleted from the pitch outright: a
Q4_K_M GGUF shares no byte runs with the FP16 safetensors it came from.

---

## 4. Sequencing, and the evidence that earns each step

The rule: **a boundary is not started until the previous boundary has produced users who
name the next pain.** No stage is started to complete a taxonomy.

| Stage | Boundary | Ships as | Earned by (evidence, from people who are not us) | Kill/skip condition |
|---|---|---|---|---|
| **1** | **B1 bytes** | `vault` + `vaultd`: verifying, delegating fetcher with a name-shaped view | — (starts now) | 8 weeks after publishing, nobody outside can install and use it → the pain was not real or the on-ramp is too heavy. Then stop, do not proceed. |
| **2** | **B3 vendor** | a small library + a patch to **one named product** where the bug reproduces (Jan / LibreChat / Open WebUI), plus opaque-record custody in `conduit` | ≥10 stage-1 users not us, on ≥3 OSes; ≥1 bug report we did not write about the digest/verify path; and ≥3 of those users name a client where reasoning replay or `tool_calls` breaks, reproducible on a currently shipping version | If stage-1 users mostly serve models locally and never hit reasoning replay, skip to stage 3. If the vendor fixes it first, we are done and we say so. |
| **3** | **B2 authority** | host-side risk derivation + subtractive inheritance, as a component for a host that already runs MCP | a stage-2 patch **merged** in someone else's tree (proves we can land code where we do not own the repo) **and** ≥1 user running MCP servers they do not trust | If LF-MCP ships a reference host policy engine with host-computed risk first, we contribute to theirs (D-013's build-ON reflex), we do not ship a rival. |
| **4** | **B4 time** | the fold — **internal architecture only**, never pitched as a standard | our own dogfooding, nothing else | Never externalised unless an adopter asks for the trace format. |
| **P** | arbiter *measured admission* | a measured fit-check + explicit refusal | hop-tax benchmark (client→conduit→arbiter→llama-server vs direct, TTFT + tok/s) under 10% **and** a real OOM/contention report from a stage-1/2/3 user | Parked. Serving plane: abandoned, not parked. |
| **P** | graph | SCIP-in, provenance-out, ranking in scope | a real user asks for structural context and rejects SCIP-as-is for a stated reason | Parked. If nobody asks in two stages: dropped, and we recommend SCIP. |

**The measurement that matters across stages** is Claim C: record the integration cost of
each adoption in a single file (`docs/governance/ADOPTIONS.md`) — adopter, boundary, diff
lines added, diff lines deleted, new config keys, new processes, new failure modes. Three
rows is enough to see the curve. A falling curve is the abstraction existing. A flat curve
is five tools, and we will say so in the same file.

**When "standard" becomes allowed:** when one record has **two independent
implementations** — one of them not ours. Until then, "interface".

---

## 5. Words we stop using

Language is not cosmetics here: five of the seventeen contradictions in the register are
produced purely by over-claiming words. Each swap below is enforceable — the old word is a
review failure.

| Stop saying | Say instead | Because |
|---|---|---|
| "standard" | "interface" (spec, contract) | A standard requires ≥2 independent implementations. We have 0. Reinstated when the count is 2. |
| "derive", "the logical solution", "prove not pick" | "decided: X. Alternatives: Y, Z. Tradeoff accepted: … Reverses if: …" | BREAK 1 / C10. Every decision record carries a falsifier or it does not merge. |
| "settled", "OPEN: none" | "decided 2026-08-15 · reverses on \<observation\>" | Five of eight specs with no open questions means no reviewers. Every spec must carry ≥1 open question and ≥1 falsifier, or it is marked `unreviewed`. |
| "single authority over hardware", "no app touches the GPU directly" | "measured admission control; the OS and driver are the authority; we refuse when we cannot measure" | BREAK 2. Authority without an enforcement primitive is a suggestion. |
| "lossless" (normalization) | "verbatim custody with provenance" + "declared-lossy projection" | BREAK 5. Signed thinking blocks and encrypted reasoning items cannot be re-derived. |
| "neutral schema" (as a wire format) | "internal record envelope" — the wire is OpenAI-compatible | C1, C4. We never demand a new wire format. |
| "zero dependencies" | "no library dependencies; N declared process dependencies, pinned with version + checksum" (publish the ledger) | BREAK 7 / C5. `$PATH` is worse than `go.mod`: no lockfile, no checksum, no SBOM. |
| "seven seams", "the eighth seam" | "four boundaries, one invariant"; a seam is "a hypothesis under test" | BREAK 3 / C15. The number was an axiom because it was written down. |
| "schema-first" | "spec-and-code, hand-reconciled; a generator when there is one" | C7. The translator does not exist; claiming it caused the drift. |
| "multiplatform is not a goal", "no portability shims" | "portable reference implementation; Go's stdlib *is* the platform layer" | C8 / BREAK 12. Retract Principle #8. |
| "quant-as-patch", "delta" | delete; "chunk-level dedup — unproven, would require reopening `identity §3.4`" | BREAK 10. Physically unfounded as stated. |
| "inject upstream" | "contribute, with the maintainer's incentive written down first" | BREAK 11. If the incentive cannot be written, the work is not started. |
| "PRO grade" | "no-surprises defaults" (and nothing else until there is telemetry) | Seven daemons with no trace id, metrics, or health is not pro grade. |
| "the seam is the product" | "the record is the product" | The seam is our name for a boundary; what an adopter receives is a checkable record. |
| "MVP released" | "first outside user" | BREAK 14. Releasing to ourselves is not evidence. |
| "abstraction" as the product/umbrella name | the tool's plain name (`vault` ships as a verifying model fetcher) | C12 / BREAK 14. The word stays as the internal name of the *destination thesis*, not on a download page. |
| "done" (as "task checked") | see §6 | 65 done tasks with a broken headline path is the measurement of the old definition. |

---

## 6. "Done", redefined for this stage

### 6.1 Done, for any unit of work from now on

Six conditions, all six required:

1. **It runs on a machine we do not own**, from the published install instructions, without
   the author present.
2. **One person who is not the author used it for their own purpose** and reported what
   broke. (Silence is not a pass.)
3. **Its falsifier is written down** in `docs/governance/FALSIFIERS.md` and has not fired.
4. **Every process dependency is declared** with a pinned version and a checksum, and the
   tool refuses clearly — exit code + message naming the missing binary — when one is absent.
5. **Its spec carries ≥1 open question.** Zero open questions = `unreviewed`, not finished.
6. **Every claim in its README is measured** (with the number and the machine it was
   measured on) **or deleted.**

Green builds, passing tests, and spec versions are necessary and are no longer *sufficient*
for the word.

### 6.2 Done, for this reframe stage specifically

This stage is done when all of the following exist on disk (not when this document is
merged):

1. **`docs/governance/FALSIFIERS.md`** — one row per live claim: claim, owner, the
   observation that reverses it, where that observation would be recorded. Seeded with
   Claims S, C, R (§1.2) and the eight primitive falsifiers (§2.4).
2. **A supersede mechanism in `DECISIONS.md`** (`D-0NN supersedes D-0MM because <evidence>`),
   used immediately to: retract the prime principle, supersede D-001 (the name), retract
   D-015.4 (auth later), retract Principle #8 (multiplatform), and record D-013/D-014 as
   retractions of the earlier "open field" verdicts rather than as forward-looking notes.
3. **C1–C3 resolved by decision records that name the reversing observation** —
   recommendation unchanged: OpenAI-compat wire, SSE, `/v1/check`; the neutral schema is
   demoted to the internal envelope (§2.5.3).
4. **A dependency ledger** (`docs/governance/DEPENDENCIES.md`): `tree-sitter`, `gopls`,
   `llama-server`, `aria2`, `ssh`, Docker — version, checksum, why, and the refusal
   behaviour when missing.
5. **Stage 1 frozen to one page** — the §3 user, the §3.3 commands, the five blockers in
   §3.5, and nothing else. Anything not on that page is parked with its evidence gate.
6. **`docs/governance/ADOPTIONS.md`** created empty with its four columns, so Claim C has
   somewhere to be measured and its emptiness is visible.
7. **Every spec re-opened**: each carries ≥1 open question and ≥1 falsifier, and every
   "settled" is rewritten as "decided · reverses on …".
8. **Parked ≠ deleted.** The arbiter's measured admission, graph-on-SCIP, and B4 stay in
   the tree with their evidence gate written next to them. This is the one place we
   deliberately diverge from the hostile review: it says delete the scaffolding, we say
   park it behind a gate — because if Claim C holds at B1 and B3, the parked pieces are the
   destination, and if it does not, we will have shipped a useful fetcher and lost nothing.

### 6.3 The honest summary

The review's factual findings stand and are absorbed above. Its strategic conclusion is
rejected on one specific ground: it evaluated the primitives as a list and found them
unrelated. They are not a list — they are one rule (§2.1) and one default (§2.2) at four
boundaries (§2.3), and that proposition is now measurable (Claim C, §1.2) with a written
kill condition. We ship one boundary, to one real user, and we let the marginal-cost curve
decide whether the abstraction exists. That is the difference between believing a thesis
and testing one.

---

## Appendix — suggested tasks arising (CTO triages)

- **P0** Create `FALSIFIERS.md`, `DEPENDENCIES.md`, `ADOPTIONS.md`; add the supersede
  mechanism to `DECISIONS.md` (§6.2 items 1, 2, 4, 6).
- **P0** Fix `hf://` digest resolution (T-071) — stage 1's only hard blocker.
- **P0** Specify + build `vault link` (name-shaped projection of the store) — the on-ramp.
- **P0** Auth on the delegation path; retract D-015.4.
- **P0** Rewrite all eight specs' status lines (`decided · reverses on …`) and re-open ≥1
  question each; retract the prime principle from `project scope.txt`.
- **P1** Resume/failure conformance case: kill network, sleep laptop, unplug host; `verify`
  correct in all three. This is stage 1's published conformance case.
- **P1** Delete quant-as-patch and "delta" from all pitch documents; leave one line naming
  chunk-level dedup as unproven and gated on reopening `identity §3.4`.
- **P1** Rename in copy: retire "standard" (→ interface), "zero dependencies" (→ declared
  ledger), "single authority over hardware" (→ measured admission).
- **P1** Write the maintainer-incentive page per contribution target, *before* any B2/B3
  patch work. Prediction on record: for Ollama it cannot be written, because the opaque
  registry is the monetisation.
- **P2** Park with gates: arbiter measured admission (gate = hop-tax <10% + a real
  contention report); graph-on-SCIP (gate = a user who rejects SCIP for a stated reason).
  Abandon outright: the serving plane, ISO submission (D-011), the MITM appliance.
- **P2** Publish the four-boundary framing in `the grammar.txt` as a *revision*, keeping the
  old seven-seam text visible with a note on what disproved it — the retraction is the
  credential.
