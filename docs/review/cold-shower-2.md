# Cold Shower #2 — Adversarial Review of the REFRAME

Task: T-078 (id collides with the board's `vault link` task — see D-9) · Date: 2026-08-16 (UTC)
Target: `research/reframe-architect.md`, `docs/governance/{FALSIFIERS,DECISIONS,ADOPTIONS,DEPENDENCIES}.md`,
D-019/D-020/D-021, and the state of `docs/vision/*` after the reframe.
Stance: hostile. Nothing here is charitable. Every accusation carries a file, a line, or a command.

---

## 0. The verdict up front

Cold shower #1 said: no falsifiable claim, no user, no distribution, no evidence, and a
governance layer manufacturing documentation instead of resistance.

The reframe answered by producing **more governance**. It replaced one unfalsifiable
principle with one unfalsifiable invariant, replaced "seven seams" with "four boundaries"
(two of which fail the reframe's own definition of a boundary), invented three claims whose
falsifiers cannot fire for structural reasons, and then declared the *previous* falsification
event a *virtue* — in writing, in `FALSIFIERS.md`, as a NOTE.

Meanwhile, in the same commit (`ebdfc5e`), the number of lines of Go changed was **zero**:

    git log -3 --name-only | grep '\.go$'     # empty

1,124 lines of adversarial/reframe prose since the last line of shipped code. The single
hard blocker the reframe identifies (`hf://` digest resolution, `internal/registry/hf.go:70-81`)
was already named in `docs/vision/mvp-roadmap.md:51-56` **before** cold shower #1, was named
again by cold shower #1, was named again by the reframe (§3.5.1), and is now named a fourth
time on the board as T-071 P0. Four documents, one 10-line bug, still broken.

And the retraction that the whole reframe rests on did not happen. `D-019` states, in prose,
"retracts the prime principle in project scope.txt". `docs/vision/project scope.txt:124-128`
still reads:

> Prime principle: abstraction is not a preference … We do not pick; we derive.

A retraction that exists only inside the document claiming the retraction is not a
retraction. It is a second, contradictory source of truth — which is *precisely* the C10-class
defect the reframe was written to eliminate. A stranger cloning this repo reads the old thesis
as governing law, in a file whose Status line still says "research phase — surveying the
ecosystem before any decisions" (`project scope.txt:5`).

**Summary judgment: the reframe is a better-written version of the same failure mode. The
thesis got harder to disprove, not easier. That is the opposite of the stated goal.**

---

## 1. "The receiver re-derives" is the new dogma, and it is worse than the old one

### 1.1 The falsifier is a conjunction whose second term can never be satisfied

`FALSIFIERS.md:26-30`:

> R reverses if: a boundary exists where re-derivation is worse (impossible or ruinously
> expensive) **AND** the trusting design has **no field failures**.

Name a single non-trivial design in shipping software with *no* field failures. There is
none. The second conjunct is not an observation, it is a null set. R is therefore protected
by construction: any candidate counterexample is disqualified because the trusting design it
uses also has bugs. The old prime principle was unfalsifiable because every objection was
re-labelled as the objector's failure. R is unfalsifiable because every counterexample is
re-labelled as the counterexample's failure. **Same trick, new sentence.**

### 1.2 The falsifier already fired, and the reframe canonised the escape hatch

`FALSIFIERS.md:31-33`:

> NOTE: fired once already — vendor reasoning blocks … cannot be re-derived -> became
> "opaque-record custody", not a kill. That correction is the desired pattern.

Read that again. The claim's own reversing observation occurred, and the response was to
redefine the claim so the observation no longer counts — and then to write down that this
manoeuvre is *the desired pattern*. This is a textbook degenerating research programme: the
theory's content shrinks with each observation while its verbal scope stays constant.

It is worse than a normal ad-hoc rescue, because the rescue is now **policy**. Every future
boundary where re-derivation fails will be absorbed as "custody". The rule "the receiver
re-derives" now covers both re-deriving and not re-deriving. A rule that covers a behaviour
and its negation forbids nothing and predicts nothing.

### 1.3 `verification: none, opaque, attributed` is "trust the sender" with better manners

`reframe-architect.md:140-147` defines the shared record shape
`{ subject, claim, provenance, verification }`, and then §2.4 (line 134) assigns B3 the
verification value *"none, opaque, attributed"*.

So the universal record shape admits a member whose verification field is `none`. Any
trusting design in the industry can be re-described in this shape in five minutes: the
sender's assertion is the `claim`, the sender is the `provenance`, and `verification: none`.
MCP's `readOnlyHint` — the reframe's own B2 villain (`reframe-architect.md:114`) — is a
perfectly well-formed record under this shape. The envelope does not discriminate between the
designs the invariant praises and the designs it condemns. It is a JSON object, not a thesis.

And §2.2's default ("no measurement → do not admit") is **violated by the reframe's own B3
design**: an opaque reasoning block has no verification, therefore absent record → refusal,
therefore refuse it. The design instead carries it verbatim. Both "shared parts" that make
this "one system and not five tools" (§2.5.1, §2.5.2) are contradicted at one of the four
boundaries by the reframe's own §2.4 row. The unification is asserted in §2.5 and refuted in
§2.4, thirteen lines earlier.

### 1.4 Two of the four boundaries are not boundaries, by the reframe's own definition

`reframe-architect.md:106-107` defines a boundary: "where something arrives that we did not
produce."

- **B4 (time)** is "the loop's own past (state, memory, termination)" — line 115. Our own past
  is, definitionally, something we produced. B4 fails the definition in the same table that
  states the definition. B4 is not a boundary; it is a data-structure preference
  (append-only log vs mutable struct), and the reframe already concedes it will never be
  externalised (§4, stage 4: "internal architecture only, never pitched as a standard"). A
  boundary that is never crossed by anyone else is not evidence for a cross-vendor invariant.
- **B2 (authority)**: "host-computed risk" is not re-derivation. You cannot *derive* whether a
  tool mutates state from its `inputSchema`; you can *guess* from parameter names and a
  hand-written manifest. The reframe knows this — the primitive's own falsifier
  (`FALSIFIERS.md:42-43`) is "host-derived risk disagrees with reality **more often** than
  server self-report", i.e. it is admitted to be a competing heuristic with an error rate, not
  a proof. Calling a heuristic "re-derivation" is the same category error as calling a config
  constant "single authority over hardware" (BREAK 2). We were told that lesson four days ago.

So: at B3 the rule is *inverted*; at B4 there is no boundary; at B2 the rule is a *heuristic
analogy*. The rule literally holds at exactly one boundary: **B1, where "re-derive" means
"run sha256".**

### 1.5 Which means the invariant, stripped of metaphor, is: checksum your downloads

That is not an abstraction. It is `sha256sums` (1990s), Debian's `Release` files, git's object
names, OCI content-addressing, Nix store paths, LFS oids, and Xet. The reframe's own §2.4
calls the identity record "the canonical instance of §2.1" and says "every other primitive
borrows this record's shape" — i.e. the entire system is a metaphor extended from
`sha256sum -c` to three domains where it does not literally apply.

An invariant that is universally true (checksums are good) is not a product thesis; it is
hygiene. An invariant that is *specifically* true would forbid something a competent engineer
would otherwise do. Name it. Currently R forbids nothing: not `readOnlyHint` (that is a record
with `verification: none`), not mutable agent state (that is a "declared-lossy projection"),
not trusting HuggingFace's LFS oid (see 1.6).

### 1.6 The one place R holds is the one place it is circular

At B1 the receiver re-hashes the bytes — and compares them to **a digest supplied by the same
sender**. `internal/registry/hf.go` fetches the file list from `huggingface.co`; the fix for
T-071 will fetch the LFS `oid` from `huggingface.co`; the bytes come from
`huggingface.co`. The "receiver re-derives" ceremony detects **truncation and bit rot**, which
TLS + `Content-Length` largely already do, and detects **nothing** about authenticity, because
the reference value has the same origin as the payload.

So at its canonical boundary, the invariant is trust-on-first-use with extra steps, and the
sender is trusted for the only value that matters. Until vault ships an independent trust root
(user-pinned digests, a signed second source, a transparency log), "we never accept the
sender's assertion of that property" (§2.1) is **false in our own code**: we accept the
sender's assertion of the digest and then check the bytes against it.

### 1.7 Where R would become concretely refutable

Three forms that a hostile reviewer could actually kill. Adopt one or drop the claim:

1. **Prohibition form.** "No interface we publish will ever accept a property assertion it
   cannot recompute; where it cannot recompute, it **refuses** rather than carries."
   Refuted by: B3 opaque custody, today. (Which is why the current wording exists.)
2. **Cost form.** "At each boundary, the receiver-side check costs < 5% of the operation it
   guards." Refuted by: re-hashing a 40 GB GGUF on a Synology DS220+ (~110 MB/s SHA256 on a
   Celeron J4025) = ~6 minutes per open, against a load of ~40 s from cache. That is 900%, not
   5%. Measure it; the identity-record falsifier (`FALSIFIERS.md:38-39`) is already about this
   and has never been run. One command, one afternoon, and it may well fire.
3. **Prediction form.** "Any local-AI product that trusts a sender's property assertion at
   B1/B2/B3 will ship a CVE or a corruption bug within 12 months; products that re-derive will
   not." Dated, public, checkable, and it can embarrass us. That is the point.

**Verdict on question 1: yes, R is the new dogma.** Its falsifier contains an impossible
conjunct, it has already survived its own falsification by redefinition, its record shape
admits the designs it condemns, and it is literally true at one of four boundaries — where it
reduces to checksumming against a digest we take on trust from the sender.

---

## 2. Claims S and C are not falsifiable in practice

### 2.1 S cannot be measured today, on either of its two terms

S (`FALSIFIERS.md:11-18`) requires (a) a net-negative diff from an independent implementer and
(b) "no user-visible regression on a **published conformance case**". The conformance case does
not exist — it is T-081, priority **P1**, unstarted (`team/tasks.md:16`). So the claim that
replaced the prime principle is currently unmeasurable because its measuring instrument is
scheduled behind three P0s.

### 2.2 "Net-negative diff" is satisfied by a middleman — the exact thing it was built to exclude

`reframe-architect.md:41-48` says: deletion distinguishes an abstraction from a layer, and it
is "countable in a diff".

It is not. Consider the honest baseline: a client with 600 lines of retry/resume/progress
downloader code replaces it with 40 lines of `POST /v1/jobs` polling. Net −560. Now do the same
against `curl`, `aria2c --continue`, or a 30-line shell script. Also net −560. The metric
scores **"did we outsource work over a socket"**, which every middleman in history scores well
on. An nginx cache scores −560. A SaaS scores −5,000.

Worse, the cost side is systematically undercounted. §1.2 says "including config and process
supervision" — in *lines*. A whole new daemon that the end user must install, start, secure,
back up, upgrade, and debug across a LAN costs five lines of `docker-compose.yml`. The metric
therefore assigns the largest real cost of adopting vault a value near zero. **The measure is
biased toward confirming S**, which is the one property a falsifier must not have.

### 2.3 The evidence standard is asymmetric — by a factor of two, in writing

`FALSIFIERS.md:15-18`: "**two** such adopters => S is false". One adopter with a negative diff
is treated as support. Confirmation needs n=1; refutation needs n=2. That asymmetry is not
argued anywhere. It is a thumb on the scale, written into the file that exists to prevent
thumbs on scales.

### 2.4 The timeout clause has a filter that discards all real-world evidence

Reversal (b) requires that after 6 months "one adopter **declined with a reason that is about
the interface (not about time)**" (`reframe-architect.md:71`).

Maintainers do not decline that way. They say "interesting, we're swamped", or they say
nothing. The reframe pre-emptively rules the only decline you will ever receive **inadmissible**.
Combine with the 6-month clock and S has no reachable reversal path: silence is not a decline,
"no time" is not a decline, one wrapper-adopter is not enough, and the conformance case that
would let anyone try does not exist.

### 2.5 The specific number that would reverse S — since the reframe never names one

Here is one. Adopt it verbatim or admit S is decorative:

> **S-test v1.** Target: `janhq/jan`, `LostRuins/koboldcpp`, `oobabooga/text-generation-webui`,
> or `mudler/LocalAI` — each has a private model downloader. Baseline: LOC of the downloader
> module measured by `cloc --include-lang=Go,Python,TypeScript <path>` at a named commit,
> recorded in `ADOPTIONS.md` **before** the PR.
> **S is confirmed** only by a PR that is **merged** in that repo, which removes **≥ 300 LOC**
> from that module, adds **≤ 100 LOC** total, adds **0 required runtime processes** for the
> end user (a vault daemon is optional or absent), keeps CI green, and passes the published
> resume conformance case (T-081).
> **S is reversed** by any of: net LOC ≥ 0; a required new process (score it as **+1,000 LOC**,
> not 5 lines of YAML); the PR closed unmerged for any reason including silence after 90 days;
> or the module surviving alongside ours as a fallback path.
> Deadline: 2027-02-16. Owner: named human. Recorded in `ADOPTIONS.md`, row 1.

Note what this exposes: under any honest scoring of "new required process", **vault as
currently designed cannot pass S**, because its value proposition *is* a daemon on another
machine. The reframe's stage 1 and the reframe's headline claim are mutually exclusive. See
§3.6.

### 2.6 Claim C is not a test of the abstraction; it is a test of the learning curve

C (`FALSIFIERS.md:20-24`): adoption #2 costs less than adoption #1.

Four fatal problems:

1. **No control.** Second adoptions are cheaper *for every tool family that has ever existed* —
   familiarity with the docs, the error style, the maintainer, the CI. To attribute the fall
   to "one record shape", you need a control arm: the same adopter integrating two *unrelated*
   tools, or two adopters where one gets a deliberately divergent second interface. Nobody
   proposed a control. Without one, C is confirmed by ordinary human learning and tells us
   nothing about whether the abstraction exists.
2. **Incommensurable units.** Adoption #1 is "replace a file downloader" (B1). Adoption #2 is
   "carry vendor reasoning blocks verbatim" (B3). These differ in difficulty by an order of
   magnitude for reasons that have nothing to do with our record shape. The comparison is
   between two different tasks in two different codebases. `n=2`, no control, no normalisation,
   and the direction of the result is decided by which two tasks you happen to pick — and
   §1.2 lets us pick ("same adopter **or two comparable adopters**").
3. **It is unmeasurable before the program's own kill date.** C needs two outside adoptions of
   two different interfaces. Stage 2 is gated on ≥10 outside stage-1 users on ≥3 OSes plus an
   unsolicited bug report (§4). Stage 1 self-terminates at 8 weeks if nobody installs it. So
   the first row of `ADOPTIONS.md` requires surviving a gate that has no distribution plan
   behind it, and the *second* row — the one that actually tests C — requires a merged PR in a
   stranger's repo. **The central measurable proposition of the reframe cannot be measured for
   at least a year, and most probably never.** `ADOPTIONS.md` is an empty table with four
   columns and a comment admitting it is empty. It is a monument to a measurement, not a
   measurement.
4. **The program-level falsifier is unreachable by the same argument.** "After three boundaries
   shipped, the curve is flat" (`reframe-architect.md:74`) — three boundaries shipped is a
   two-year milestone for a team with zero users. A falsifier that cannot fire inside the
   project's own expected lifetime is decoration.

### 2.7 Six of the eight primitive falsifiers are unfireable by policy

`FALSIFIERS.md:38-54`. Count the ones that require **field frequency data**: identity record
("more often than"), host-computed risk ("more often than"), subtractive inheritance
("frequent enough that users route around"), append-only fold ("before a mutable design
does"), no-surprises posture ("measured on real users, not asserted"), downloader-as-service
("separate cleanly in the field"). Six of eight.

Now recall that the project has **no telemetry, no metrics, no traces, no health endpoints
anywhere** (cold shower #1, BREAK 13 / "PRO grade") and a governing principle that nothing is
written outside the project tree and nothing reaches the network without approval
(`project scope.txt:147-149`). We have simultaneously required frequency evidence and
forbidden the instrument that collects it. These six falsifiers cannot fire — not because the
claims are strong, but because we have arranged never to look.

There is no `owner` column, no `date recorded`, no `checked by`, no expiry, and no
`where recorded` column — all of which `reframe-architect.md:346-348` explicitly promised
("claim, **owner**, the observation that reverses it, **where that observation would be
recorded**"). The file that was supposed to prove the reframe shipped, itself does not meet
the reframe's own spec for that file. Item 1 of 8 in §6.2, half-delivered, in the commit that
declares the stage done.

**Verdict on question 2: no.** S is unmeasurable today and biased toward confirmation; its
only reachable reversal is filtered out by an inadmissibility clause. C is a learning-curve
measurement with n=2, no control, and no data before the program's own kill date. Six of eight
primitive falsifiers require data we have forbidden ourselves to collect.

---

## 3. Stage 1 has no user, and the on-ramp is the weakest part of it

### 3.1 The persona dissolves on contact

§3.2: "one person who runs local models on a laptop and owns a box that never sleeps."

If the box never sleeps and can pull 40 GB, **it can also serve**. `OLLAMA_HOST=0.0.0.0` on the
NAS, `OLLAMA_HOST=http://nas:11434` on the laptop — one env var, zero new binaries, and the
model bytes never need to reach the laptop at all. LM Studio ships a headless/server mode and
`lms` CLI; llama.cpp's `llama-server` is one binary with `--host`.

The persona that survives is narrower: *the always-on box has bandwidth and uptime but no
usable GPU, while the laptop has the GPU, and the model must therefore physically move.*
NAS + gaming laptop. That is a slice of a slice, and it is a hobbyist slice — the reframe's
§3.2 claim "this person exists in numbers and is served by nobody" is asserted with no count,
no forum thread, no issue link, no survey. Cold shower #1's BREAK 14 was "no market, no user,
no buyer". The reframe's answer is a paragraph of confident prose containing **zero external
evidence** and no citation. The defect was restated, not fixed.

### 3.2 Each of the three pains already has a shipped answer on the same hardware

- **Pain 1 (the 20–50 GB fetch dies).** `huggingface-cli`/`hf download` resumes by default and
  has for years (and `hf_transfer` for speed). `ollama pull` resumes. `aria2c --continue`
  resumes. Synology **Download Station** — the very "Synology torrent pattern" the reframe cites
  as its inspiration (`project scope.txt:61`) — is a queued, resumable, delegated HTTP/BT
  downloader that is already installed on the box, has a web UI, and needs no Go binary. The
  analogy used to justify the product is a product that already exists, on the same box, and
  already solves pains 1 and 3.
- **Pain 2 ("cannot tell if two files are the same weights").** `sha256sum` / `Get-FileHash`,
  on any machine, today. And specifically for the reframe's flagship B1 failure — "Ollama
  `:tag` opacity … you cannot tell whether two tags are the same weights"
  (`reframe-architect.md:112`) — check it before publishing it:

      dir  $env:USERPROFILE\.ollama\models\blobs                      # blobs are sha256-<hex>
      cat  $env:USERPROFILE\.ollama\models\manifests\registry.ollama.ai\library\<model>\<tag>

  Ollama's on-disk store is content-addressed and the manifest lists layer digests. If that
  holds, the headline "verified failure" at B1 is **largely false**, and what remains is the
  much smaller, non-architectural complaint that remote tags are mutable and the registry is
  not browsable — a registry UX gripe, not a boundary failure. The reframe demands that every
  claim be measured or deleted (§6.1.6). This claim was measured by nobody and is load-bearing
  for the choice of stage 1.
- **Pain 3 ("cannot say fetch it over there").** `ssh nas 'hf download … --local-dir /volume1/models'`.
  One line. No daemon, no token, no port.

Three pains, three incumbent answers, all free, all installed. The reframe never compares
against any of them. A one-page competitive check ("what does a competent user do today?") is
missing from a document whose entire purpose was to find a real user.

### 3.3 Nobody switches from LM Studio / Ollama, and §3.4 admits it

The on-ramp (§3.4) is: keep your tool, we hardlink a name into your model directory. That is
not adoption; that is **coexistence**. The user's stack after adopting vault is:
LM Studio/llama.cpp **plus** `vaultd` **plus** `vault` **plus** a shared token **plus** a
hardlink whose invariants they must now understand. Nothing was removed. The user-side net diff
is strictly **positive** — which is the reframe's own definition of "we added a layer, not an
abstraction" (§1.2). **Stage 1's on-ramp fails Claim S's test at the human level by
construction.**

And the incumbent already closed the gap the on-ramp opens: `llama-server -hf <repo>:<quant>`
downloads from HuggingFace itself, with caching and ETag validation. The exact command in
§3.4 (`llama-server -m /volume1/models/mistral-7b-q4_k_m.gguf`) is the *old* way of driving
llama.cpp; the current way needs no store, no link, and no vault.

### 3.4 The commands in §3.3 are not runnable by the person in §3.2

- `vaultd -addr :7777 -store /volume1/models` on a Synology: DSM is not a general Linux box for
  a normal user. You need Docker/Container Manager (not available on all models), or Entware,
  or an SSH session and a cross-compiled `linux/arm64` binary (`GOARCH` for Realtek/Marvell
  units), plus a way to keep it alive across DSM updates (`systemd` is not the answer on DSM).
  There is no `.spk`, no Docker image published, no install doc for the target device. The
  board's answer to this is T-080, **P2**.
- `-addr :7777` binds **all interfaces**. Verified in code: `cmd/vaultd/main.go:262-263`
  defaults to `":7777"`, and `handler()` (line 49-55) registers `/v1/jobs`,
  `/v1/artifacts` — including `PUT` (line 189-190) — with **no authentication of any kind**.
  D-020 says "Auth on the delegation path NOW". The code says otherwise, four days later.
  So the published headline command for stage 1 instructs a stranger to expose an
  **unauthenticated, writable artifact service on their home LAN** from a NAS holding their
  data. Against `project scope.txt:147` ("Nothing runs, listens, or reaches the network without
  explicit approval") and against the reframe's own §2.2. This is not a to-do; it is the
  security posture of the thing we are about to publish.
- `vault link` **does not exist**: `cmd/vault/main.go:53` — `usage: vault <get|list|verify>`.
  The "whole adoption theory for stage 1" (§3.4) is currently a sentence in a research
  document.
- Hardlinks require the same filesystem/volume (a NAS with `/volume1` and `/volume2` breaks
  it); symlinks on Windows need Developer Mode or admin; on DSM they behave differently across
  shares and are invisible to some SMB clients. `vault link` is a portability project, not a
  one-liner — and it must also answer: what happens when the user edits or moves the linked
  file, and what happens to the store's refcount when they delete it? Nobody has specified
  this. It is a P0 with no spec, one line on the board (T-078).

### 3.5 The 8-week gate is a marketing experiment misreported as a pain experiment

§4, stage 1: "8 weeks after publishing, nobody outside can install and use it → the pain was
not real or the on-ramp is too heavy. Then stop."

- **Publish where?** The repo is private by decision (D-008). No release, no binaries, no
  install page, no README claim set, no launch venue, no named community (r/synology,
  r/LocalLLaMA, HN, the Ollama Discord). Zero of the reframe's 8 "stage done" items is a
  distribution plan. Cold shower #1 said there is no distribution path; the reframe's answer is
  the word "publishing".
- **The result is confounded and the misreading is pre-written.** If nobody installs it, the
  cause will be "nobody heard of it", and the reframe has already committed us to record it as
  "the pain was not real". We would kill a possibly-correct hypothesis on evidence about our
  own marketing. That is not a falsifier; it is a coin flip with an interpretation attached.
- **And the threshold is "nobody"** — so *one* friend, one colleague, one Reddit reply is a
  PASS. Compare stage 2's gate: "≥10 users, ≥3 OSes, ≥1 unsolicited bug report, ≥3 naming a
  client". The loosest gate in the document is the one that decides whether the whole program
  continues; the strict gates guard the stages we will never reach. That ordering is not an
  accident, it is motivated reasoning.

### 3.6 The structural contradiction: stage 1 was chosen to avoid the thing S requires

§3.1's selection table gives B1 the win on the criterion "**Needs zero cooperation from an
incumbent**", and marks B3 as losing because "a merged PR is the deliverable".

But Claim S — the abstraction claim, the one that replaced the prime principle — can *only* be
tested by an independent implementer **deleting their code**. That requires exactly the
incumbent cooperation that B1 was selected for avoiding. §3.4 admits it in one sentence: "an
adopter who later deletes their own downloader in favour of `POST /v1/jobs` produces our first
negative diff" — "later", i.e. never in stage 1.

So the sequencing is: **do the stage that cannot test the central claim first, and gate the
stage that can test it behind a 10-user threshold with no distribution channel.** Meanwhile
the strength of the R evidence runs in the opposite direction: B3 has named, reproducible
crashes in shipping products (Jan, LibreChat, Open WebUI); B1's flagship failure is a
mutable-tag UX complaint that may be factually wrong (§3.2). **We ship the boundary with the
weakest evidence and the least ability to falsify anything, and we call the choice
"evidence-gated".**

**Verdict on question 3: we re-picked a seam with no distribution path.** The user is asserted,
not found; each pain has a free incumbent answer on the same hardware; the on-ramp is pure
addition (positive user diff = "layer" by our own definition); the headline command exposes an
unauthenticated writable service; `vault link` does not exist and is unspecified; and the gate
that decides the program's future is a marketing test whose negative result we have pre-agreed
to misattribute.

---

## 4. "Park, don't delete" preserved the scaffolding exactly as the first review predicted

The reframe names this as its one deliberate divergence from cold shower #1
(`reframe-architect.md:365-369`): "Parked ≠ deleted … if Claim C holds at B1 and B3, the parked
pieces are the destination." Note the conditional: the parked pieces are justified by a claim
that §2.6 above shows cannot be measured for at least a year. **We kept the scaffolding on the
strength of a measurement that will never take a row.**

### 4.1 The old grand system is still the repo's operative description

| Where | What it still says | Contradicts |
|---|---|---|
| `project scope.txt:124-128` | "**Prime principle**: … we do not pick; we derive." | D-019, which claims to have retracted it |
| `project scope.txt:5` | "Status: research phase — surveying the ecosystem before any decisions" | 67 done tasks, 5 repos, a live run |
| `project scope.txt:38-43` | "ship … standards across **EVERY** layer … what Anthropic did with MCP but across the whole stack" | §5's word ban ("standard" → "interface"), D-019 |
| `project scope.txt:49-92` | "SCOPE — THE **SEVEN** LAYERS" | four boundaries (D-019) |
| `project scope.txt:53-54` | "quantization as **patches**" | D-021 abandoned it as physically unfounded |
| `project scope.txt:151-153` | "Multiplatform is **not** a goal … no portability shims" | reframe §5, D-006, T-064/067/080 |
| `project scope.txt:154-162` | "Schema-first … every implementation ships a translation tool" | BREAK 8; reframe §5 ("spec-and-code, hand-reconciled") |
| `project scope.txt:209` | "PRO grade" | reframe §5 (banned until telemetry) |
| `docs/vision/mvp-roadmap.md` (whole file) | seven seams, seven daemons, 14-task priority order through graph/gate/conduit/arbiter, and "**Definition of MVP released**" = all seven specs v0.1 | D-019, D-020, D-021, and reframe §5 ("MVP released" → "first outside user") |
| `docs/specs/*.txt` (all 8) | `SETTLED:` blocks everywhere; `client spec.txt:157` and `graph spec.txt:207` still say **"OPEN: none"** | reframe §6.2.7: every spec re-opened, ≥1 open question or marked `unreviewed` |
| `arbiter spec.txt:116,126` | serving plane decisions "settled", build order settled | D-021 **abandoned** the serving plane |
| `docs/specs/the grammar.txt:6` | "the argued minimal set of seams" (seven) | D-019 |

Eleven files/locations still describe the grand system. The reframe's §6.2 declared the stage
"done when all of the following exist **on disk** (not when this document is merged)" — and
then the commit that recorded D-019/D-020/D-021 delivered items 1, 4, 6 (partially: see §2.7)
and item 2, while items **3, 5, 7 were not done at all**, and item 2's own headline retraction
(the prime principle) was not applied to the file it names. By its own definition, the stage
is **not done** — yet the board records T-077 as `done` (`team/tasks.md:58`) and the sprint
line reads "done 67 · in-progress 0".

**"Done" was redefined in §6.1 with six conditions, and the very next task recorded as done
meets neither the old nor the new definition.** That is the whole indictment of the process in
one line.

### 4.2 The organisation did not shrink; only the board did

- **18 worker directories** under `team/` (architect, cto, 6 engineers, infra, iso-agent,
  ollama-integration, qa, research-lead, 2 researchers, scrum, security, standards). Six open
  tasks (`team/tasks.md:13-18`), of which three belong to `@engineer-vault`.
- **`iso-agent` still exists** after D-021 abandoned the ISO submission and T-031 was marked
  superseded. So does `ollama-integration`, whose entire premise (inject into Ollama) is now
  gated behind a maintainer-incentive page that the reframe itself predicts **cannot be
  written** (`reframe-architect.md:399-400`).
- Every one of those 18 workers maintains a `context.md` and a `log.md`. `architect/context.md`
  is now **276 lines** and contains the full text of both the critique and the reframe as
  bullet points. The documentation-manufacturing engine BREAK 13 identified is running at full
  staffing on a project with one shippable binary and one open bug.
- **The parked tasks have no expiry.** T-083's gate is a hop-tax benchmark that has never been
  run *plus* "a real contention report from a stage-1/2/3 user" — users who do not exist.
  T-084's gate is "a user who rejects SCIP for a stated reason" with a fallback of "two
  stages", where stages 2 and 3 are themselves unreachable (§2.6). T-085 is internal-only
  forever. Parked with an unreachable gate and no date is a **drawer**, and the governance
  layer now provides a respectable word for leaving things in it.

### 4.3 The abstraction is still hiding behind governance documents — and the hiding place got nicer

Score the last four units of work: cold shower #1 (718 lines), reframe (406 lines), four
governance files (~85 lines), this document. Lines of production code changed: **0**. Bugs
fixed: **0**. The one blocker: still open.

The reframe's most quotable move — "the retraction is the credential"
(`reframe-architect.md:404-405`) — is the tell. It converts admitting error into a **deliverable**.
Core value #10 (`project scope.txt:164-169`) institutionalises adversarial self-review as a
standing practice. The org can now generate, indefinitely, high-quality documents about its own
epistemic virtue, each one recorded as a `done` task, each one increasing the volume of prose
per line of code. This document is the fourth in that series, and its existence is itself
evidence for the charge it is making.

**Verdict on question 4: yes.** The scaffolding is intact, the grand narrative is still the
repo's operative text in eleven places, the retraction exists only inside the document
announcing it, three of eight "stage done" items were skipped while the task was marked done,
the 18-worker org is untouched, and the parked items are parked behind gates that cannot open.

---

## 5. What a well-funded competitor uses to kill this version

### 5.1 The cheapest kill shot: nothing. Wait 8 weeks.

The reframe wrote our own termination condition and attached it to an event we cannot cause:
outside installs, from a private repo, with no distribution plan, in 56 days. A competitor
spends **$0 and takes no action**; the program self-terminates on its own gate and records the
result as "the pain was not real". No prior version of this project handed an opponent a
scheduled suicide clause. This one does, in `reframe-architect.md:275`.

### 5.2 The $0 technical kill shot: a gist, not a company

A 30-line post — `systemd` unit (or DSM Task Scheduler entry) + `aria2c --continue` +
`sha256sum` + an `rsync`/SMB share — titled "delegate your model downloads to your NAS".
Solves pains 1–3, installs nothing, has no daemon, no token, no port, no `link` semantics, no
cross-compile. Publishable this afternoon by anyone. Nobody installs a Go daemon pair to get
what a shell script gives them, and the "abstraction" cannot be tested by a product a shell
script replaces.

### 5.3 The incumbents' one-line kills (each is a small PR to a repo with distribution)

- **llama.cpp** already has `-hf <repo>` with caching/ETag. Add `--verify-sha256` or read the
  LFS oid: stage 1's differentiator evaporates inside the loader everyone already runs. ~30
  lines in a 70k-star repo.
- **HuggingFace** owns the bytes. Xet-backed storage is *already* content-defined chunking with
  dedup — i.e. the registry is becoming content-addressed and dedup'd at the source, by the
  party with the distribution. Expose the oid prominently, keep improving `hf download`
  resume, and both the identity record and the (already-abandoned) dedup story are redundant.
- **Ollama** stores blobs as `sha256-<hex>` with digest-listing manifests today (§3.2) and
  resumes pulls. Add remote-pull-to-a-shared-store, or just document `OLLAMA_MODELS` on an NFS
  mount + `OLLAMA_HOST`, and the persona is served by the tool they already have.
- **LM Studio** ships a headless server and CLI: "run it on the NAS" is a docs page, not a
  product.

### 5.4 The framing kill: there is nothing to defend

The reframe deliberately surrendered every source of leverage: no new wire format (§2.5.3), the
record is internal, the view belongs to the incumbent (§3.4), "standard" is forbidden until two
independent implementations (§5), and B2/B3 have explicit **"if they do it first, we stop"**
clauses (§4, stage 2 and stage 3 skip conditions; D-013 already conceded the permission seam
to LF-MCP). So:

- Of four boundaries, **two carry a written surrender clause**, one (B4) is internal-only and
  never externalised, and one (B1) is a fetcher. **The entire external program is a model
  downloader.** A competitor does not need to attack the thesis; the thesis has already agreed
  to withdraw from three quarters of its own territory.
- And "the receiver re-derives" is a sentence. It cannot be owned, patented, or defended. If it
  is good, it will be adopted without us and without attribution — which the reframe would
  count as *success of the invariant* and would still leave us with no product, no users, and
  no revenue. A thesis whose victory condition is indistinguishable from our irrelevance is not
  a business.

### 5.5 The reputational kill shot (cheapest per dollar of damage)

One security writeup: *"`vaultd` — unauthenticated artifact PUT/GET on the default port, from a
project whose stated principle is 'nothing listens without explicit approval'."* Verifiable in
`cmd/vaultd/main.go` in 60 seconds. The target is not the code (it is a 40-line fix); the
target is the credibility of the entire governance apparatus — 21 decision records, four
governance files, a redefined "done", and a shipped default that violates the project's own
founding principle. That story writes itself, and it is currently true.

### 5.6 The talent/political kill

Stage 3 is already pre-conceded to LF-MCP (D-013, and the stage-3 skip condition). Stage 2's
deliverable is a bug fix in someone else's client — a bug that project's own maintainers will
eventually fix, at which point the reframe says "we are done and we say so"
(`reframe-architect.md:276`). A funded competitor kills stage 2 by **fixing their own bug** and
kills stage 3 by **shipping the reference policy engine**. Both are things they were already
going to do.

---

## 6. What would actually change my mind (in order, none of them a document)

1. **`hf://` digest resolution fixed and `vault get hf://…` working end-to-end on a machine
   that is not the author's.** One bug, four documents, still open. Until this is fixed, no
   claim in this repo has standing.
2. **Auth on `vaultd`, default loopback, explicit refusal when no token is set** — because the
   published command currently instructs strangers to expose a writable service. This is
   D-020, unimplemented.
3. **The re-hash cost measurement** (§1.7 form 2) on real hardware, published with the machine
   named. It may fire the identity-record falsifier. Running an experiment that can hurt is the
   only act in this repo that would distinguish it from the last four documents.
4. **The Ollama-opacity claim checked** with two `cat`s (§3.2), and B1's justification rewritten
   or withdrawn accordingly.
5. **S-test v1 (§2.5) pasted into `FALSIFIERS.md` with a named adopter, a named human, and a
   date** — or Claim S struck from the file.
6. **`project scope.txt` and `mvp-roadmap.md` rewritten or marked `SUPERSEDED` at the top.** A
   retraction that lives only in the retracting document is a contradiction, not a correction.
   This is 20 minutes of work and it has not been done, which tells you what the governance
   layer is actually for.
7. **The org cut to the work.** Six open tasks do not need 18 persistent workers with 18
   context files. Archive the ones with no stage-1 role (iso-agent first, then the five
   engineers on parked boundaries). Every retained worker is a daily prose tax.

---

## 7. Defect register (each item is checkable in under two minutes)

- **D-1** `project scope.txt:124-128` — prime principle intact; D-019:82 claims it was
  retracted. Two contradictory sources of truth about the project's governing epistemology.
- **D-2** `FALSIFIERS.md:26-30` — R's falsifier contains the conjunct "the trusting design has
  no field failures", which is never satisfiable. R is protected by construction.
- **D-3** `FALSIFIERS.md:31-33` — R's falsifier already fired and was absorbed by redefinition;
  the absorption is recorded as "the desired pattern". Degenerating-programme marker, in writing.
- **D-4** `reframe-architect.md:134` vs `:140-152` — B3's design (carry verbatim,
  `verification: none`) violates both §2.1 (receiver re-derives) and §2.2 (absent record =
  refusal), the two properties that make the four boundaries "one system".
- **D-5** `reframe-architect.md:106-107` vs `:115` — B4 ("the loop's own past") fails the
  document's own definition of a boundary ("something we did not produce"), in the same table.
- **D-6** `FALSIFIERS.md:15-18` — refutation of S requires two adopters; confirmation requires
  one. Unargued asymmetry.
- **D-7** `reframe-architect.md:71` — S's timeout reversal excludes declines that cite time,
  i.e. all real declines. No reachable reversal path.
- **D-8** `FALSIFIERS.md:38-54` — six of eight primitive falsifiers require field frequency
  data; the project has no telemetry and a principle against collecting it. Unfireable by policy.
- **D-9** `FALSIFIERS.md` — no owner, no date, no "where recorded" columns, contrary to
  `reframe-architect.md:346-348` which specified them. Item 1 of the "stage done" list,
  half-delivered in the commit that declared the stage done.
- **D-10** `team/tasks.md:14` uses **T-078** for `vault link`; this review was also dispatched
  as **T-078**. The board's own rule is "Stable IDs (T-###), **never reused**" (`tasks.md:4`),
  broken in the same commit that introduced the governance rigour.
- **D-11** `cmd/vaultd/main.go:49-55,189-190,262-263` — no auth on any route, `PUT` included,
  default bind `:7777` on all interfaces. Violates D-020 ("auth NOW"), `project scope.txt:147`,
  and reframe §2.2.
- **D-12** `cmd/vault/main.go:53` — `vault link` absent; the "whole adoption theory for stage 1"
  is unimplemented and unspecified (no answer for cross-volume hardlinks, Windows symlink
  privilege, refcounting, or user edits to the linked file).
- **D-13** `internal/registry/hf.go:70-81` — `Manifest.Digest` never set; with
  `manifest.Validate()` requiring it (`manifest.go:59-61`), the headline command in reframe
  §3.3 has never worked. Known since `mvp-roadmap.md:51-56`, before cold shower #1.
- **D-14** `internal/acquire/acquire.go:75,96` — `if m.Digest != ""` is a fail-open branch:
  absent digest ⇒ bytes accepted. "Absent record = refusal" is not the behaviour of the code
  that implements the canonical instance of the invariant.
- **D-15** `internal/store/store.go:148-150` — `Open()` does not re-hash. "Verify **on open**"
  (`identity standard.txt:67-68,102-103`) is unimplemented in the store; verification happens
  at write time only. The invariant's flagship primitive is missing at the point it is named for.
- **D-16** `DEPENDENCIES.md:13-19` — every version pin and checksum is `TBD`, deferred to "first
  release". The reframe demanded pinned versions + checksums as a *condition of done* (§6.1.4).
  A ledger with no numbers is a promise.
- **D-17** `client spec.txt:157`, `graph spec.txt:207` — still "OPEN: none"; all eight specs
  still carry `SETTLED:` blocks. Reframe §6.2.7 (every spec re-opened) not done; the specs
  should therefore be marked `unreviewed` by the reframe's own rule.
- **D-18** `arbiter spec.txt:116,126` — settled decisions about a serving plane that D-021
  abandoned. Spec and decision record now disagree.
- **D-19** `docs/vision/mvp-roadmap.md` — entire document still specifies seven seams, seven
  daemons, and "MVP released"; no supersede banner. It is the most likely document a new
  reader treats as the plan.
- **D-20** `team/tasks.md:58,9` — T-077 recorded `done` though §6.2 items 3, 5, 7 were not
  delivered and item 2's headline retraction was not applied. The redefinition of "done" (§6.1)
  was violated by the first task completed after it was written.
- **D-21** `reframe-architect.md:275` — stage-1 kill gate is "nobody can install it in 8 weeks",
  with no distribution channel, a private repo (D-008), and a pass threshold of one person. A
  confounded experiment with a pre-written misinterpretation.
- **D-22** `reframe-architect.md:112` — B1's "verified failure" (Ollama tag opacity) is
  contradicted by Ollama's digest-named blob store and digest-listing manifests. Unchecked,
  load-bearing for the choice of stage 1.
- **D-23** `reframe-architect.md:186` vs `FALSIFIERS.md:11-18` — stage 1 was selected for
  needing "zero cooperation from an incumbent"; Claim S can only be tested by an incumbent
  deleting code. The first stage cannot test the central claim.
- **D-24** `ADOPTIONS.md:11` — empty, and structurally unfillable before the program's own kill
  date (§2.6). The measurement that makes the abstraction "a destination rather than a slogan"
  has no path to a first row.

---

## 8. The one paragraph

The first review said: you have no falsifiable claim, no user, and no distribution, and your
process makes documents instead of resistance. The reframe replied with three claims that
cannot be measured (S needs a conformance case that is P1 and an adopter its own stage-1
strategy avoids; C needs two adoptions that its own kill gate precludes; R is guarded by an
impossible conjunct and has already survived its own falsification by redefinition), one user
who is asserted with no citation and whose three pains each have a free incumbent answer on the
same box, an on-ramp that adds a daemon and deletes nothing — failing, at the human level, the
exact deletion test the document uses to distinguish an abstraction from a middleman — and a
"park, don't delete" clause that kept the seven-seam system alive in eleven files, including
the scope document that still names the retracted prime principle as governing law. Nothing was
falsified, because nothing can be. Nothing was deleted, because everything was parked. Nothing
shipped, because the one blocking bug has now been described in four documents and fixed in
none. The cheapest way for anyone to kill this version is to wait 56 days and let it invoke its
own kill clause — and the second cheapest is to post a shell script. The reframe is better
prose than the thesis it replaced, and that is the problem: it made the belief more
sophisticated instead of making the test possible.
