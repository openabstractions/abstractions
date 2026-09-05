# Cold Shower #1 — Adversarial Review of the Seven-Seam Thesis

Task: T-074 · Date: 2026-08-15 (UTC) · Reviewer: `opus` (hostile critic, per governing principle #10)
Scope read: `docs/specs/*` (8 specs), `docs/vision/project scope.txt`, `docs/vision/mvp-roadmap.md`,
`docs/governance/DECISIONS.md` (D-001..D-018), `team/model-allocation.md`, `team/tasks.md`,
`team/research-lead/{competition,ecosystem-state}.md`, and the six reference implementations
(~15.5k lines of Go: vault 2.9k, arbiter 3.8k, conduit 1.9k, gate 2.3k, graph 2.5k, client 2.1k).

**Framing: I was told to assume the premise is wrong and prove it. I did. Where the premise
survived the attack, I say so in §2 — those survivals are the only load-bearing parts of this
project.**

---

## 0. The verdict up front

The thesis is: *the AI stack is 44 monoliths privately re-implementing the same seven seams;
distill the seven, spec them, ship reference implementations, inject upstream.*

Four things are true about that thesis and they are fatal in combination:

1. **The seven are not derived, they are the survey's own sort order.** The taxonomy reproduces
   the categories used to file the research, omits the seams production actually bleeds on
   (observability, cost/quota metering, eval, adapter lifecycle), and has already leaked two
   "cross-cutting" appendices (`identity`, `wire`) to stay at seven.
2. **The seams are cut precisely where the value is welded together.** engine/arbiter is cut
   through the middle of the inference loop; graph is cut so that ranking — the only reason
   repo-maps work — falls outside the spec; conduit is cut so that the vendor-specific fields
   that actually earn money (signed thinking blocks, cached prefixes, encrypted reasoning items)
   cannot survive the crossing.
3. **The project owns nothing, and every standard it cites won by owning something.** MCP won
   because Anthropic shipped it inside the client everyone used. HF won because it held the
   weights. OpenAI's format won because it was the only API anyone wanted. This project has no
   model, no users, no registry, no editor, no fleet. Its adoption plan is therefore reduced to
   optional, default-off env vars in other people's binaries (D-015.7).
4. **The epistemology forbids the criticism that would have caught 1–3.** "If a seam admits more
   than one defensible design, we have not found the seam yet" is unfalsifiable: any objection is
   re-labelled as the objector's failure to find the seam. The observable output of that rule is
   five of eight specs declaring `OPEN: none` within two days of first contact with the field.

Everything below is the detail. I found **14 breaks**, **10 survivals**, **7 kill shots**, and a
fix/abandon ledger.

---

## 1. Assumptions and promises that BREAK

### BREAK 1 — The prime principle is unfalsifiable, and the repo already refutes it

> "Abstraction is not a preference... It is the logical solution — derivable by reason... If a seam
> admits more than one defensible design, we have not found the seam yet. We do not pick; we derive."
> — `project scope.txt`, Governing Principles

This is the load-bearing epistemic claim of the whole program, and it is false as stated. Proof by
the project's own artifacts:

- **`/v1/check` vs `/authorize`.** Two defensible designs for one decision, both shipped, both
  "settled" in different specs (`gate spec §4` vs `wire contract spec §3.5`), unresolved and filed
  as T-072. By the prime principle this situation is impossible. It exists.
- **Neutral schema vs OpenAI-compat.** `transport spec §3.1` kills "just speak OpenAI." `wire
  contract spec §3.4` adopts OpenAI-compat and kills "a bespoke completion shape — re-fighting a
  won wire." Both marked settled. They are direct contradictions on the single most consequential
  design question in the project.
- **NDJSON vs SSE.** `transport spec §7h` settles NDJSON. `wire contract spec §4/O3` ships SSE.
  Same data plane, two settled framings.
- **SHA256 fixed, multihash "killed"** (`identity standard §3.4`) with the reason "negotiation with
  no benefit." The entire cryptography profession disagrees: algorithm agility exists because
  hashes get deprecated, and multihash exists for precisely the reason it is dismissed. This is a
  *preference* (simplicity now over migration later) presented as a derivation.
- **Confidence tiers `{exact, heuristic}`** (`graph spec §3.7`) with "Kill: numeric confidence —
  uncalibrated and unactionable." Every retrieval system that works ranks numerically. SCIP, Kythe,
  and Glean each model provenance differently. Three defensible designs; one was picked.
- **Byte offsets, not line/column** (`graph spec §3.8`) — while the enrichment provider it depends
  on (LSP) speaks line/column in UTF-16 code units, forcing a lossy conversion the implementation
  actually performs (`lsp.go: r.convert`). The "durable coordinate" claim survives only until the
  file changes, which is exactly when you needed it.

The structural tell is the rhetoric. Every argued decision has the shape *Decision → one-sentence
reason → `Kill: <alternative>` + one dismissive clause.* The alternatives are never modelled,
costed, or tested; they are named and executed. That is the *form* of an argument with the
*content* removed. It reads as rigour and functions as immunity.

**Consequence:** the prime principle converts disagreement into heresy and produces specs with
zero open questions after two days. A research program with no open questions is not finished; it
is closed to evidence.

### BREAK 2 — The arbiter cannot arbitrate. It is an honour system with a spreadsheet.

The arbiter claims "single authority over hardware," "no app touches the GPU directly — settled"
(`arbiter spec §7b`). Read the implementation:

- `internal/arbiter/budget.go` is pure in-process arithmetic over `Budget{VRAMBytes, RAMBytes}` —
  **configured constants**. There is no NVML, no DXGI/`QueryVideoMemoryInfo`, no Metal residency
  query, no `GlobalMemoryStatusEx`. Grep for `nvml|VRAM query|MemStat`: nothing.
- `splitBudget()` bills everything to VRAM unless dp=="cpu", with the comment "the DP axis is the
  whole story." KV growth, context length, layer offload, activation buffers, fragmentation — all
  unmodelled.
- There is no enforcement primitive anywhere: no cgroups, no `CUDA_MPS`, no MIG, no vGPU, no
  driver hook, no admission gate that anything is *forced* through.

So the "authority" is a book of numbers about a machine it never measures, governing processes
that never agreed to obey it. One `python train.py`, one ComfyUI generation, one game, one browser
tab with WebGPU, and the book is fiction — the arbiter will cheerfully grant a reservation against
VRAM that no longer exists, and the engine OOMs downstream.

**An authority without enforcement is a suggestion.** And the only software that would need to
obey it is every competing AI tool, each of which has exactly zero incentive to hand its memory
budget to a third-party daemon from an unknown vendor.

Worse: this seam is labelled "the biggest unclaimed technical gap" (`the grammar.txt`,
`ecosystem-state.md §2.3`). It is unclaimed at the app layer because it is *unclaimable* at the app
layer. The arbiter already exists and is called *the OS scheduler plus the GPU driver*. NVIDIA
ships MPS/MIG/vGPU, Microsoft ships WDDM memory management, Apple ships Metal residency sets.
Cooperative resource arbitration between mutually hostile userspace applications has been
attempted repeatedly (X11/compositor VRAM etiquette, JVM heap coordination, per-app disk-IO
gentlemen's agreements) and has lost to kernel/driver enforcement every single time.

`arbiter §7b` "no app touches the GPU directly — settled" is not a decision. It is a wish written
in the imperative mood by a party with no power to enforce it.

### BREAK 3 — "Seven seams" is a coincidence of the filing cabinet, not a derivation

`the grammar.txt`: "The field does not have too few abstractions. It has ~44 monoliths, each
privately re-implementing the same ~seven seams." Note the `~`. Then D-002 hardens `~seven` into
exactly seven, canonically named, and `arbiter §3.2` explicitly refuses an eighth seam on the
grounds that "the grammar fixes seven seams." **The number became an axiom because it was written
down, not because it was proven.**

The seven map suspiciously well onto the *research categories* in `project scope.txt` Phase 1
("inference engines, chat UIs, cloud APIs, agentic IDEs, creative/generation, workflow,
protocols"). You sorted 44 tools into buckets and then discovered that there are as many seams as
buckets.

Seams that are unambiguously real in production and appear nowhere in the seven:

| Missing seam | Why it is not optional |
|---|---|
| **Observability / telemetry / tracing** | The #1 operational problem in every LLM deployment. There is no trace id, no span, no metrics surface in any of the eight specs. Seven daemons, zero observability. |
| **Cost / quota / rate-limit metering** | The actual moat of every multi-provider gateway (LiteLLM, Portkey, OpenRouter, Cloudflare AI Gateway). Absent entirely. It is the first thing an enterprise buyer asks for. |
| **Evaluation / regression harness** | The only way anyone decides whether a change helped. Not a seam, not a spec, not a task. |
| **Fine-tune / adapter lifecycle** | LoRA appears once, as a capability *field* on the engine. Adapter provenance, composition, and routing is a real, distinct, unclaimed seam. |
| **Embedding / vector store** | Dismissed as a "consumer" of graph (`graph §2`), while being the highest-volume production AI workload in existence. |
| **Safety / moderation / PII** | Legally mandatory in the enterprise market the scope doc claims ("PRO grade"). Absent. |
| **Prompt / artifact versioning** | The thing teams actually fight over in practice. Absent. |

And two of the seven are not seams:

- **`client`** — its own grammar entry says the client "becomes a thin composition of the other six
  seams." A composition is an *application*, not a seam. It is the consumer, definitionally.
- **`identity` and `wire`** were added later as "cross-cutting" specs. If seven were derived, the
  derivation would not need two appendices to stay at seven. The leak is the disproof.

Falsification test the project never ran: **name one real system that decomposes into exactly these
seven with nothing left over and nothing spanning two.** There isn't one. vLLM alone spans
engine+arbiter+conduit and refuses the split for engineering reasons (see BREAK 4). llama.cpp spans
engine+arbiter+conduit+identity. Ollama spans vault+engine+arbiter+conduit+identity. If every real
system crosses your seam lines, your seam lines are not where the joints are.

### BREAK 4 — The engine/arbiter cut is physically incoherent; the "biggest gap" collapses to nginx

`arbiter §3.2`: continuous batching, paged KV, prefix cache, chunked prefill, multi-LoRA live
**inside the arbiter**; "the engine must be CAPABLE of them (reported via capabilities); the
arbiter owns the policy." `engine §4`: engines sit **behind a process boundary**, spoken to over
**localhost HTTP**, reference engine = `llama-server`.

These two statements cannot both be honoured:

- **Continuous batching *is* the engine's inner loop.** Batch composition is re-decided every
  forward iteration (~10–50 ms) by code holding live references to the KV block table, the
  sequence group state, and the sampler. You cannot own that policy from another process over HTTP.
- **Prefix caching requires token-level block hashes** and the physical block allocator. It is not
  a policy knob; it is the cache.
- **PagedAttention is a CUDA kernel plus a block allocator**, not a scheduling decision.
- **`arbiter §7j`'s build order is backwards.** It settles "prefix cache → continuous batching →
  multi-LoRA → paged KV + chunked prefill last (token-gen micro-optimizations, marginal at
  single-user scale)." Paged KV is the *precondition* for continuous batching at any useful
  concurrency — that is why vLLM built PagedAttention first and named the paper after it. Calling
  it a "micro-optimization" is a category error that reveals the serving plane was specified from
  a literature summary, not from the problem.

What the arbiter can actually do across a process boundary is route whole requests to whole
engines and LRU-unload them. That is **a load balancer with a residency cache** — which Ollama
already ships and LocalAI ships better (VRAM-aware routing + autoscaling + memory reclaimer, per
your own `ecosystem-state.md §2.3`).

Terminal irony: by choosing `llama-server` over localhost HTTP, the arbiter has *already* delegated
batching, KV management, and prefix caching to llama.cpp's own scheduler. The seam has been
conceded in the implementation while the spec still claims it. `arbiterd` additionally wires
`fake.Engine` rather than `llama.New` + vault `Blob` (`mvp-roadmap.md §2.2`), so the one thing that
would have exposed this — a real model under real memory pressure — has never run in the daemon
that claims to own memory.

### BREAK 5 — Lossless OpenAI↔Anthropic normalization is impossible, and the two live formats aren't the field

`transport spec §1`: "One neutral schema... mapping **losslessly** to OpenAI and Anthropic formats."
`§3.2`: "the adapter set is { OpenAI, Anthropic }... two adapters cover the field."

Both claims are false in 2026, and false specifically on the *premium* code paths:

- **Anthropic extended thinking blocks carry a `signature`** and must be replayed verbatim in
  subsequent turns or the API rejects the request. **OpenAI reasoning items are opaque
  `encrypted_content` bound to a `previous_response_id`.** Normalizing both into
  `reasoning { reasoning_content }` (a string part) destroys the cryptographic continuity both
  vendors *require* for multi-turn thinking + tool use. Your neutral schema breaks the exact
  feature it was created to unify. This is not an edge case; it is the flagship 2026 capability.
- **Missing from the part list entirely:** `cache_control` / prompt-cache markers (the single
  largest cost lever in production), citations, server-side tools (web search, code execution),
  structured-output/JSON-schema response formats, logprobs, parallel tool-call ids and ordering,
  MCP-server blocks passed through the API, service tiers, safety identifiers, streaming audio in,
  batch/async job semantics, stop-sequence echo differences, and per-vendor token-counting
  endpoints. Every one of these is load-bearing for someone. A schema that drops them is not
  neutral; it is a lowest-common-denominator that no serious user can adopt.
- **"Two adapters cover the field" ignores Gemini**, which is neither format and is roughly a third
  of the market, plus Bedrock and Vertex envelopes, plus Azure's deployment-name routing. Your own
  `competition.md §2.4` lists Gemini as an incumbent and then the spec excludes it.
- **LiteLLM has ~100 providers not out of incompetence.** The long tail of vendor quirks *is* the
  product. A 1,891-line conduit is not "cleaner" than a decade of accumulated bug-for-bug
  compatibility; it is unfinished, and the missing 99% is the part users hit on day two.

And the killer: `project scope.txt` Governing Principle #2 says *"we win by exposing the universal
standard with a better abstraction — **never by inventing a non-standard and demanding adoption**.
(DeepSeek won by exposing OpenAI's format. Same law.)"* Conduit §3.1 invents a non-standard and
demands adoption. The seam violates the principle in the same document, citing the principle's own
example as support.

### BREAK 6 — The graph seam does not do the thing it exists to do

The claim (`graph §1`, `ecosystem-state.md §2 CONTEXT`, `mvp-roadmap.md §1 row 6, §2.4`): a
structural project model that beats RAG chunk-guessing and can answer *"what calls foo()"* from
structure, across every language, offline, deterministically.

What `abstraction-graph` actually produces:

- `internal/provider/tags.go` emits **only `ref` edges**, bound by **same-file name matching**, and
  explicitly discards anything cross-file: `continue // ref with no same-file definition: cannot
  bind heuristically`.
- **Zero** `calls`, `imports`, `contains`, `inherits`, `type-uses`, or `declaration` edges are ever
  emitted by the default provider. Six of the eight fixed core edge kinds are decorative.
- The LSP enrichment provider is **gopls-only, Go-only**: `Bin: "gopls"`, `Exts: []string{".go"}`,
  and the source comment "Only Go is wired."

So the "one project model for every language" is: **Go via gopls, plus intra-file name matching
everywhere else.** That is `ctags` (1984) minus the cross-file index. It cannot answer "what calls
foo()" for any language, including Go via the default provider. The seam's own done-when is
unmet while the board records T-015/T-026/T-033/T-048 as done.

Two further design faults:

- **Ranking is deliberately out of scope** (`graph §2 OUT`: "retrieval, ranking, and selection...
  are consumers of the graph, not part of it"). But ranking + token-budget selection is *the entire
  reason Aider's repo-map works* — the graph is trivial, the PageRank-under-a-token-budget is the
  invention. Shipping the schema and outsourcing the hard half guarantees every consumer keeps its
  own index, because adopting yours saves them the easy part and leaves the hard part.
- **SCIP is the elephant.** SCIP (permissive, protobuf-schema'd, cross-language, cross-file, with
  real production indexers: scip-go, scip-typescript, scip-java, scip-python, rust-analyzer) is a
  *published standard that already does this properly*, plus LSIF before it and Kythe/Glean beside
  it. `graph §5` mentions SCIP once, as "prior art for symbol schemas." A hostile reviewer's
  question, unanswered anywhere in the repo: **why should anyone adopt a strictly weaker schema
  with one Go-only indexer over SCIP?** There is no argued answer. That omission is the single
  largest research failure in the project, in the seam called "our cleanest win."

### BREAK 7 — Zero dependencies is an accounting trick; the real dependency count is high and undeclared

`project scope.txt` DEPENDENCY POLICY: "ZERO external dependencies... No pro user ever sees an
unaccounted dependency... Every dependency is pinned to an exact version and argued against this
policy before introduction."

Actual runtime dependencies, none pinned, none argued, none in a manifest:

| Dependency | Where | Nature |
|---|---|---|
| `tree-sitter` CLI | `graph/internal/provider/provider.go` (`exec.Command`) | Rust/Node toolchain binary; **requires per-language grammars registered in `~/.config/tree-sitter/config.json`** |
| `gopls` | `graph/internal/provider/lsp` | Go language server, version-sensitive protocol |
| `llama-server` | `arbiter/internal/engine/llama` | C++/CUDA binary, ABI + flag churn |
| `aria2` / magnet | `vault` (T-005, `magnet.go`) | external transfer engine |
| `ssh` | `vault` delegation (D-015, T-021) | remote shell + assumed remote binary |
| Docker/OCI | scope "NOTED MUST-DOS", release packaging T-058 | full container runtime |

The policy is satisfied by declaring `exec.Command` not to be a dependency. That is a definitional
dodge, not an engineering property. A supply-chain attacker does not care whether your
untrusted code arrives via `go.mod` or via `$PATH` — `$PATH` is *worse*, because there is no
lockfile, no checksum, no version pin, and no SBOM.

Two direct value violations fall out:

- **Governing principle #7: "Nothing is written outside the project tree."** `tree-sitter tags`
  needs a user-global grammar config in `~/.config`. The default context provider cannot function
  without violating principle #7.
- **"Offline, deterministic, every language"** (`graph §3.3`) becomes "whatever grammars this
  particular user happened to install, at whatever versions." Non-deterministic across machines by
  construction.

And the cost is paid by the wrong party. Adopters do not care that your `go.mod` is empty; they
care that integrating you costs **seven daemons, seven ports, seven crash domains, seven upgrade
paths, a policy file, and six hand-installed binaries**. Zero-dep optimises the auditability of
*your* tree by maximising the integration cost of *everyone else's*.

### BREAK 8 — Schema-first is claimed while the schemas are prose and the bindings are hand-written

D-005 and Governing Principle #9: "The abstraction is a description — a schema — defined in our own
spec... Every implementation ships a translation tool that derives the wire formats and language
bindings FROM the schema. The abstraction is authored once; JSON, protobuf, and types are generated
— never hand-maintained in parallel. Adoption = run the translator, get your native format."

Reality:

- The eight "schemas" are **ASCII prose in `.txt` files** with indentation as syntax. Nothing can
  consume them.
- Every Go type is **hand-written** (`graph/internal/graph/types.go`, `conduit` schema, `gate`
  grant/decision structs, `wire` shapes). The `wire contract spec §4` even defines itself as "the
  exact bytes `cmd/abstraction` emits and reads" — i.e. the code is the source of truth and the
  spec is documentation *of* it. That is the exact inversion of the principle.
- **The translator does not exist.** JSON-Schema emission is on the post-MVP deferral list
  (`mvp-roadmap.md §2`); protobuf projection likewise. So the one mechanism that makes schema-first
  real is unbuilt, while the principle is cited as settled in D-005.

A project that hand-maintains prose specs and Go structs in parallel, and calls that schema-first,
*is the failure mode its own principle forbids.* And the drift is already measurable: three
settled-but-contradictory pairs (BREAK 1) is precisely what parallel hand-maintenance produces.

### BREAK 9 — The gate is security theatre: bypassable by construction, non-forensic by decision, deferred where it matters

- **Same trust domain.** The gate authorises calls made by the agent it constrains, in the same
  process tree, with no sandbox. `client spec §4` gives the loop a `call {capability, args}` action;
  any agent that can spawn a process or open a socket can talk to the MCP server directly and the
  gate never sees it. **Least privilege enforced by the principal being constrained is not
  enforcement.** Real enforcement needs seccomp/AppContainer/namespaces/hypervisor — which
  `project scope.txt` files under NOTED MUST-DOS, "slotted as a later stage." The security
  architecture defers the only component that provides security, in a project whose operating mode
  claims "Security boundaries, access control, audit... from day one — not retrofitted."
- **The audit log is deliberately useless.** `gate §3.7`: arguments are hashed, never stored. After
  an incident the first question is *"which file did it delete / which URL did it POST to."* A hash
  cannot answer it. Worse, it fails *both* ways: path/env arguments are low-entropy, so the hashes
  are brute-forceable — you get no forensic value and imperfect confidentiality. The correct design
  (encrypt-at-rest with a separate key, or field-level redaction with recoverable detail) was not
  considered; the alternative was `Kill:`-ed in one clause.
- **`revocation effective next invocation`** (`gate §3.5`) means an in-flight `rm -rf` runs to
  completion. That is stated as a feature.
- **The UX will kill it.** Deny-by-default + a five-level lattice + argument-scope grammar is the
  design that sank SELinux, Vista UAC, and every capability system since. Claude Code wins on
  coarse allow/deny because *prompt fatigue* is the real failure mode, and there is no analysis of
  human cost anywhere in the spec. `gate O1` admits you do not know whether five levels are needed.
- **Strategically pre-empted.** Your own `competition.md §2.5` establishes MCP is Linux
  Foundation-governed with "authorization hardening" on the 2026 roadmap, and D-013 accepts it.
  Being "the first-mover implementation of someone else's extension point" is not a standard; it is
  a plugin, and the reference plugin will be written by the people who own the spec and the client.

### BREAK 10 — Vault's differentiators are unshipped, and one of them is physically unfounded

The vault's claimed opening (`the grammar.txt` seam 1, `competition.md §2.1`, `project scope.txt`
layer 1): content-addressed + resumable + **delta/quant-as-patch** + delegatable.

- **Delta / quant-as-patch: zero code.** Grep across `abstraction-vault` for `delta|patch`: two
  incidental hits in `ref.go`. It is the most-repeated differentiator in the pitch and it does not
  exist.
- **Quant-as-patch is not merely unbuilt, it is near-impossible.** Quantisation is not a byte-diff.
  A Q4_K_M GGUF shares essentially no byte runs with the FP16 safetensors it derives from — the
  layout, block structure, and scales are all different. There is no patch to ship. The single most
  novel line in the pitch is founded on a misunderstanding of the artifact format.
- **Whole-file SHA256 is the wrong granularity for the actual win.** HF already publishes per-file
  LFS sha256, so digest-addressing adds little on its own. The genuine prize is chunk-level dedup
  across quants and revisions (CDC/casync/restic/`zstd:chunked` territory) — and `identity §3.4`
  pre-emptively forbids it by fixing "digest = SHA256(whole artifact bytes)" and killing multihash.
  **The identity decision has closed the door on the only technically interesting version of the
  vault, in the name of avoiding "negotiation with no benefit."**
- **The flagship path is broken.** `vault get hf://<repo>#<quant>` fails `manifest.Validate()`
  because `registry.HF.Resolve` never sets `Manifest.Digest` (T-071, flagged in `mvp-roadmap.md
  §2.1`). The provisioning workaround passed the URL and digest by hand. So MVP-1's headline
  done-when — content-addressed fetch from HuggingFace — has never actually worked, on a board
  showing 65 tasks done.
- **Delegation is `ssh://user@nas` plus "auth/TLS later"** (D-015.4: "localhost/trusted-network ok
  for MVP; auth/TLS later"). An unauthenticated network file-transfer service, in the project whose
  scope document promises access control from day one.

### BREAK 11 — The adoption theory inverts its own evidence

`the grammar.txt`: "Standards win by being SMALLER than the problem AND seeded into install bases
that already exist." The first half is right. The second half is where the reasoning fails, because
every cited winner is misdiagnosed:

- **MCP** did not win by being small. Dozens of small tool protocols existed. MCP won because
  Anthropic shipped it inside Claude Desktop and Claude Code — **the vendor owned the demand.**
- **HF Hub** won because it **hosted the weights.** It owned the artifact; the convention was a
  consequence, not a cause.
- **OpenAI's format** won because it was the only API anyone wanted to call. Distribution again.
- **DeepSeek** "won by conforming" — that is a *consumer* strategy for entering someone's install
  base, not a strategy for authoring a standard.

In all four cases the standard was a **byproduct of owning something people needed.** This project
owns nothing: no weights, no users, no registry, no editor, no fleet, no funding, no consortium.
So the plan degenerates into "surgical PRs to Ollama / LocalAI / LibreChat / OpenCode," and the
repo already documents why that fails:

- **D-010** concedes Ollama's pull/serve is too coupled for a rewrite PR — fork first.
- **D-015.7** concedes the injection must be `OLLAMA_VAULT_SERVICE`, **default off**, to remain
  "drop-in vanilla." An opt-in, default-off env var in someone else's binary is not seeding an
  install base. It is a flag nobody flips.
- **The incentive analysis is missing entirely.** Nowhere in the repo does anyone write down *why
  a maintainer would accept this*. Ollama's opaque `:tag` registry is not an oversight the grammar
  can correct; **it is their business model** — the walled garden is the monetisation. They will
  reject the PR for the same reason it was written. Ollama has also been actively *reducing*
  llama.cpp coupling with its own engine, i.e. moving away from the pluggability the engine spec
  wants to impose.

Finally, standing: **a standard requires at least two independent implementations.** There are
zero. One anonymous author, no users, 15.5k lines of unreleased Go, and eight documents calling
themselves standards. D-011's ISO first-mover play is worse — ISO submissions need a national body
sponsor and multi-year committee work; it is a line item, not a plan.

### BREAK 12 — "Multiplatform is not a goal" + native-first is incompatible with Go, with adoption, and with itself

Governing Principle #8: "Language does not matter... wraps the OS's already-solved functions...
**never reaches for portability shims. Multiplatform is not a goal.**" The dependency policy then
prescribes WinHTTP/WinSock/SChannel/BCrypt on Windows and BSD sockets + LibreSSL on Unix.

Then:

- **D-006 picks Go** — a language whose runtime is, precisely and by design, a portability shim over
  sockets, TLS, threads, and the filesystem. The decision cites "rich stdlib (zero deps). Not
  multiplatform — that is not a goal," which is exactly backwards about what Go's stdlib *is*.
- **The board contradicts the principle**: T-064 cross-compile linux/darwin green, T-067 Linux live
  MVP, T-068 CI 3-OS matrix, T-069 script parity. Every action is multiplatform work under a
  principle that rejects multiplatform.
- **The principle is fatal to a standard anyway.** A standard whose only reference implementation
  runs on one OS will not be adopted. And native-first means two implementations (WinHTTP vs BSD
  sockets) to keep in behavioural sync — the hand-maintained parallel projection that Principle #9
  forbids.

Pick one: portable reference implementation (then Go's shims are the *point*, and #8 is wrong), or
native (then abandon adoption). The project currently claims both and does neither.

### BREAK 13 — The process manufactures documentation, not resistance

- **All 18 decisions are dated 2026-08-15.** 65 tasks done, 8 specs, 15.5k lines of Go, and a
  17-worker "organisation" — in roughly two days, produced by one model writing markdown to itself.
  That is not velocity. **It is the absence of resistance.** Five of eight specs record `OPEN: none`.
- **Every point where reality was actually touched, it broke immediately:** a real HF URL (T-071),
  a real GGUF in `arbiterd` (still `fake.Engine`), a real Linux run (T-067, blocked on downloading
  `llama-server`), a real gate path (T-072), a real stream framing (wire O3). The pattern is
  perfect: **everything settled by argument is contested; everything tested against the world
  failed.** That is the signature of a system optimising for internal coherence.
- **The governance record is already desynchronised from the position.** DECISIONS is append-only
  with "never edit" and **no supersede mechanism**. D-001 still records the goal as
  `abstraction.org` while `competition.md`/`domain-research.md` conclude *do not fight for the
  name*. The two most consequential strategic revisions (gate absorbed by MCP; client binding
  claimed by ACP) live in a worker's `context.md`, and appear in DECISIONS only as forward-looking
  D-013/D-014 without retracting anything. **The claim "disk/git is the source of truth" fails when
  the truth is split across a decision log that cannot retract and a context file that nobody
  treats as authoritative.** Append-only without supersession does not preserve history; it
  institutionalises sunk cost.
- **`project scope.txt` still says "Version 0.2 · Status: research phase — surveying the ecosystem
  before any decisions,"** while 18 decisions are locked and an MVP release roadmap is written. The
  Phase 1 exit condition ("confident, evidence-backed answer to which abstractions and why") was
  never formally met or recorded — it was assumed by proceeding.
- **The cold shower is principle #10 — added last, after everything was settled.** Adversarial
  review applied to a closed, append-only, zero-open-questions corpus is an audit of a fait
  accompli. The mechanism was installed after the failure mode it exists to prevent had already
  occurred.
- **`model-allocation.md` is self-refuting.** The table lists the master (deepseek-v4-pro) with
  `Intelligence: —` while asserting opus is "highest," and D-018 says "the master runs on the
  strongest model." Then the routing table sends **architecture, security, and core-thesis work
  away from the master** to opus. If opus is the top tier and those are the top-stakes tasks, the
  master is not on the strongest model for the work that matters — the strongest model is a
  consultant called in after decisions are locked. "The cheapest model that can do the job" is a
  serving-cost heuristic being applied to a program whose only product is judgment.

### BREAK 14 — There is no market, no user, and no buyer for the thing being built

- **No ICP, no user, no design partner, no distribution channel, no monetisation** appears anywhere
  in scope, roadmap, or decisions. The only artefact with an identified buyer is the parked MITM
  appliance (`future-products.md`).
- **Too heavy for the hobbyist.** The addressable local-inference audience uses Ollama/LM Studio
  *specifically because it is one binary and one command*. "Install seven daemons, a tree-sitter
  CLI, gopls, aria2, and a policy file to run a GGUF from your NAS" is a strictly worse product for
  that person, and they are the only person currently described.
- **Too featureless for the enterprise.** The pro buyer wants SSO, cost attribution, quotas, rate
  limits, provider failover, retention policy, forensic audit, and a support SLA. Of those, the
  seven seams contain: a hash-only audit log. Cost metering — the actual reason companies buy
  gateways — is not a seam.
- **The name is occupied by a direct competitor in the seam called "our cleanest win"**
  (`competition.md §1`), and D-001 still records it as the goal.
- **Dogfooding is circular.** "Definition of MVP released" ends with the agent's own endpoint
  migrating to localhost serve. Being your own only user validates that the system runs; it cannot
  validate that anyone wants it.

---

## 2. What HOLDS UP under attack

I tried to break these and could not. They are the project's real assets, and every one of them is
smaller than the framework built around it.

1. **The identity critique and the identity record.** Ollama's `:tag` opacity is a genuine,
   demonstrable defect. `digest + format + revision + quant` as a record, wire form `sha256:<hex>`,
   store key bare hex, **verify-on-open (re-hash, reject on mismatch, "the key is a hint, not
   proof")** — this is correct, cheap, independently adoptable, and better than what the field does.
   The one caveat is granularity (BREAK 10).
2. **Control/data plane separation.** "JSON never carries bulk binary"; bytes stream framed on a
   side path; identity/graph/gate are control-plane-only. Boring, correct, consistently applied
   across all eight specs. This is the most disciplined idea in the repo.
3. **Build-ON discipline, and the fact that it updated on evidence.** D-013 (build on MCP, do not
   compete on permissions) and D-014 (compose on ACP, do not fork) are the project reversing two of
   its own "open field" verdicts in response to external facts. That is the single strongest
   institutional signal here — it proves the program *can* metabolise disconfirmation, which is
   precisely what BREAK 1 says the prime principle forbids. The principle should be replaced with
   whatever process produced D-013/D-014.
4. **`reasoning_content` + `tool_calls` normalization is a real, verified, narrow bug class.** Jan
   crashes; LibreChat and Open WebUI recur on reasoning replay. This is a genuine, checkable,
   valuable gap — as a small library or a set of upstream patches. Not as a seam.
5. **Downloader-as-service delegation.** "Download on the always-on host, not the laptop that
   sleeps" (the Synology pattern) is a real pain, addressed by nobody, and *small*. Content-address
   + resume + queue + priority + delegate is shippable standalone and useful the day it ships, with
   no standards claim attached.
6. **Gate §3.3: risk is host-computed, never server-claimed.** MCP's own spec declares tool
   descriptions and annotations untrusted, and tools *do* self-report `readOnlyHint`. Deriving risk
   from `inputSchema` + host-assigned origin trust + a host-authored manifest is correct security
   reasoning and a real hole in MCP. This is the best single idea in the gate spec and the only part
   worth upstreaming.
7. **Gate §3.4: subtractive subagent inheritance.** Narrow-only derivation makes privilege
   escalation structurally impossible rather than policy-dependent. Correct, and correctly argued.
8. **The client fold.** Append-only accumulator (the trace *is* the state), one step function, a
   **declared** termination predicate, checkpoint = state, media as typed refs so the loop is
   media-blind. This is genuinely cleaner than CrewAI's emergent autonomy and simpler than
   LangGraph for the N=1 case. Nobody will adopt it as a standard, but it is a correct internal
   architecture and the halting argument is real.
9. **Least privilege / no-surprises as an operating value.** "Nothing runs, listens, or reaches the
   network without explicit approval"; resolver rule "never auto-select non-loopback TCP";
   "revocation = absence"; deny-by-default when no gate endpoint is wired (a missed gate is a
   refusal, never a hole). In an ecosystem of `npm start` on 0.0.0.0:8080, this posture is real
   differentiation — and it is a *product* property, not a standards property.
10. **The dogfooding rule** ("a stage is not done until we use it for the next stage"). It is the
    only mechanism in the entire process that generates genuine resistance, and it is the mechanism
    that surfaced every real failure (T-036, T-067, T-071). It should be strengthened until it is
    the *only* gate that matters.

Note what the survivors have in common: **they are all small, local, checkable claims.** Not one of
them requires the seven-seam framework, the standards ambition, the prime principle, or the
17-worker org. The framework is not what is valuable here; it is what is hiding the value.

---

## 3. What a well-funded competitor uses to kill us

Ordered by cost-to-them, ascending. The cheapest is the most likely.

**KILL 1 — Ignore it (cost: zero).** A standard with no users, no second implementer, and no
distribution dies unaided. No competitor needs to act. This is the default outcome and the most
probable one. Everything below is what happens *if* the project starts to matter.

**KILL 2 — Ollama / LM Studio ship the vault in one sprint (cost: days).** Add sha256 digests to
the manifest, expose `--remote-pull` / a delegated pull daemon, and publish per-quant digests. They
own the install base, the registry, and the model lifecycle. The entire acquisition opening
evaporates, and the delta/quant-as-patch differentiator they would still lack does not exist here
either (BREAK 10). Ollama does this for free while continuing to refuse the PR (BREAK 11).

**KILL 3 — Anthropic + LF-MCP publish authorization hardening with a reference host policy engine
(cost: already funded and on the roadmap).** The gate becomes an unofficial alternative to the
official mechanism, written by someone with no seat at the table. Per `competition.md §2.5` this is
scheduled, not speculative. D-013 already accepts that the field is being absorbed; what the repo
has not accepted is that *the implementation slot is also taken* — it will be filled by the party
that owns both the spec and the dominant client.

**KILL 4 — SCIP/Sourcegraph/GitHub end the graph seam by pointing at what exists (cost: a blog
post).** "Here is SCIP: permissive, protobuf-schema'd, cross-language, cross-file, with production
indexers for Go, TypeScript, Java, Python, Rust, and a decade of code-intelligence behind it. Why
would you adopt a strictly weaker schema with one Go-only indexer, no cross-file edges, and ranking
explicitly out of scope?" **The repo has no answer to this question and has not asked it.** Escalation:
GitHub/Microsoft ship a code-graph MCP server and every agent gets structural context for free from
the platform that hosts the code.

**KILL 5 — vLLM / NVIDIA make the arbiter a config flag (cost: a minor release).** Ship
single-GPU/local mode with prefix caching, continuous batching, and idle unload — with real CUDA
kernels and real paged KV. NVIDIA can go further and *actually enforce* at the driver layer
(MPS/MIG/vGPU), which is arbitration a userspace daemon can never provide (BREAK 2). The "biggest
unclaimed technical gap" turns out to be claimed by the people who own the silicon.

**KILL 6 — LiteLLM / OpenRouter / Cloudflare kill conduit by standing still (cost: zero).** They
already have 100+ providers, cost tracking, quotas, retries, failover, key management, and logging.
Conduit has two adapters, no Gemini, no cost metering, and a lossless-normalization promise that is
provably impossible on the premium paths (BREAK 5).

**KILL 7 — Any funded player forks the specs and ships them with distribution (cost: an intern).**
The specs will be permissively licensed. A player with users takes the good parts (identity record,
host-computed risk, subtractive inheritance, the fold), implements them in their own product, and
their version becomes the standard because theirs has users. **Publishing eight specs before having
one adopter is free R&D for whoever has adopters.** This is the specific mechanism by which "the
seam is the product" becomes "the seam is the competitor's product."

Structural exposure underneath all seven: **the project's only defensible asset is judgment, and it
is publishing that asset for free while withholding the only thing that could compound — a user
base.** The strategy has it exactly backwards.

---

## 4. Fix now vs abandon

### FIX NOW (ordered — 1 through 4 gate everything else)

1. **Delete the prime principle as written.** Replace "derivable; one defensible design" with:
   *every decision names the alternatives, the tradeoff accepted, and the observation that would
   reverse it.* Then **reopen `OPEN` in all eight specs** — a spec with no open questions has no
   reviewers. Add a **supersede mechanism to DECISIONS** (`D-0NN supersedes D-0MM because <evidence>`)
   and immediately supersede D-001 (the name) and record the D-013/D-014 revisions as retractions of
   the earlier "open field" verdicts. Without this, every fix below gets argued away.
2. **Resolve the three live contradictions before writing another line of spec.** (a) Neutral schema
   vs OpenAI-compat → **pick OpenAI-compat**; demote the neutral schema to an internal type or delete
   it. (b) NDJSON vs SSE → **pick SSE**. (c) `/v1/check` vs `/authorize` → pick one. Three settled
   contradictions is the measurable proof that the settling process does not work.
3. **Pick ONE seam and ONE user, and get to ten real users before spec v1.** The two candidates the
   evidence supports are **vault delegation** ("download to the NAS, not the laptop") and
   **reasoning/tool_calls normalization** (a verified bug class in named products). Ship as a *tool*.
   Stop using the word "standard" — say "interface" until someone else implements one.
4. **Write the maintainer's incentive, per injection target, on one page.** For Ollama, LocalAI,
   LibreChat, OpenCode: why do *they* merge this? If it cannot be written, there is no adoption path
   and the seam should not be built. (Prediction: for Ollama it cannot be written, because the
   opaque registry is the monetisation.)
5. **Fix the dependency ledger.** Declare every process-boundary dependency — `tree-sitter` CLI,
   `gopls`, `llama-server`, `aria2`, `ssh`, Docker — with pinned versions, checksums, and an
   argument against the policy. The current "zero dependencies" claim is false advertising and the
   first serious reviewer will say so publicly.
6. **Make the arbiter honest.** Either query real hardware (NVML / DXGI `QueryVideoMemoryInfo` /
   Metal residency) and model KV growth properly, **or rename it** to *model residency manager* /
   *local model router* and delete "single authority over hardware" and "no app touches the GPU
   directly." Do not ship a claim of authority backed by a config constant.
7. **Graph: either adopt SCIP as the interchange format, or write the one-page argument why not.**
   Then either emit cross-file `calls`/`imports` edges for more than one language, or remove
   "answer what calls foo()" from the done-when. Also reconsider putting ranking in scope — without
   it there is nothing for a consumer to gain.
8. **Fix the audit record.** Hash-only is the worst of both worlds. Store arguments encrypted at
   rest under a separate key, or field-level redacted with recoverable detail. An audit trail that
   cannot answer "which file" is not an audit trail.
9. **Benchmark the hop tax before it becomes load-bearing.** Measure TTFT and tok/s for
   `client → conduit → arbiter → llama-server` against `llama-server` direct. If the overhead
   exceeds ~10% you have traded the product for the architecture, and that number should decide the
   process model rather than the aesthetics.
10. **Add an observability seam or admit it is a product, not a stack.** Seven daemons with no
    trace id, no metrics, no health, no backpressure, and no supervision is not "PRO grade."
11. **Close the vault's real holes**: T-071 (`hf://` digest), auth/TLS on the delegation path
    (retract D-015.4 — an unauthenticated network file-transfer service contradicts the stated
    security posture), and wire `arbiterd` to `llama.New` + vault `Blob` so the memory authority has
    met a real model at least once.

### ABANDON

1. **Seven seams as a program.** One person cannot ship seven standards. Ship one interface, get
   users, earn the right to the second. The number seven is a documentation artifact (BREAK 3).
2. **The arbiter's serving plane** — continuous batching, paged KV, chunked prefill, multi-LoRA.
   Physically unbuildable across the process boundary you chose (BREAK 4), already built by vLLM and
   llama.cpp, and enforceable only by NVIDIA. This is the largest planned effort with the lowest
   probability of success in the entire roadmap. Killing it recovers more capacity than every other
   item combined.
3. **The neutral conduit schema.** Two settled specs already contradict on it, the market picked
   OpenAI-compat, and lossless dual-mapping is impossible where it matters (signed thinking blocks,
   encrypted reasoning items). Keep only the reasoning/tool_calls normalizer — as a patch to named
   broken products.
4. **Quant-as-patch.** Physically unfounded. Remove it from the pitch. If chunk-level dedup is
   wanted, that requires reopening `identity §3.4` (fixed whole-file SHA256, multihash killed) —
   which is a reason to reopen it.
5. **ISO first-mover (D-011).** No standing, no national-body sponsor, wrong decade. Alignment-only
   at most; drop the submission ambition.
6. **"Multiplatform is not a goal" + native-first-no-shims.** Incompatible with adoption, with
   D-006's choice of Go, and with T-064/T-067/T-068 already on the board. Retract Principle #8 and
   admit the reference implementation is portable, or retract the adoption strategy.
7. **Everything deferred-but-planned that precedes the first user**: protobuf projection,
   shared-memory channel, ACP v2, Rust cross-language graph validation, GGUF-metadata capabilities,
   and the MITM appliance (T-061). All of it is roadmap fiction until someone else runs the code.
8. **The 17-worker org chart.** It produces documentation and mutual agreement, not resistance —
   65 tasks and 18 decisions in two days is the evidence (BREAK 13). Three roles suffice: build,
   break, decide. And move the *architecture and security* work onto the strongest model instead of
   routing it away from the master (BREAK 13, `model-allocation.md`).
9. **The name "abstraction."** Occupied, by a competitor, in the seam called the cleanest win.
   D-001 should be superseded today.

---

## 5. The one paragraph

This is a well-written, internally consistent, professionally argued taxonomy that mistakes its own
coherence for correctness. Its prime principle makes disagreement definitionally impossible, so
nothing in it has ever been contradicted by anything except reality — and every time reality was
consulted (a real HF URL, a real GGUF, a real Linux box, a real cross-file reference, a real
tool-call path) it failed immediately. Two of the seven seams cannot exist as specified: the arbiter
because arbitration without enforcement is a suggestion and because batching cannot be owned across
a process boundary; conduit because lossless OpenAI↔Anthropic normalization destroys the signed and
encrypted reasoning continuity both vendors require. A third, graph, ships `ctags` while claiming to
beat RAG, and has never answered why anyone would prefer it to SCIP. The adoption theory misreads
every standard it cites: MCP, HF, and OpenAI all won by owning something people needed, and this
project owns nothing, which is why its flagship integration is a default-off environment variable
in someone else's binary. What is genuinely good here — the identity record, control/data
separation, host-computed risk, subtractive inheritance, the append-only fold, the no-surprises
security posture, and the build-on reflex that produced D-013/D-014 — is a set of small, local,
checkable ideas, none of which needs the seven-seam framework, the standards ambition, or the
seventeen imaginary employees. **Ship one of them to ten real users, and delete the rest of the
scaffolding. The framework is not the asset; it is the thing hiding the asset.**

---

## Appendix — the contradiction register (each is a concrete, citable defect)

| # | Contradiction | Sources |
|---|---|---|
| C1 | Neutral schema vs OpenAI-compat, both "settled" | `transport spec §3.1` vs `wire contract spec §3.4` |
| C2 | NDJSON vs SSE for the same data plane, both "settled" | `transport spec §7h` vs `wire contract spec §4`, O3 |
| C3 | `/v1/check` vs `/authorize`, both registered | `gate spec §4` vs `wire contract spec §3.5`, T-072 |
| C4 | "Never invent a non-standard and demand adoption" vs inventing a neutral schema | Principle #2 vs `transport §3.1` |
| C5 | "Zero external dependencies" vs 6 undeclared `$PATH` binaries | Dependency policy vs `provider.go`, `lsp.go`, `llama/server.go`, `magnet.go` |
| C6 | "Nothing written outside the project tree" vs `~/.config/tree-sitter` requirement | Principle #7 vs tree-sitter CLI provider |
| C7 | "Schema-first, bindings generated, never hand-maintained" vs prose `.txt` specs + hand-written Go structs + no translator | Principle #9 / D-005 vs `types.go`, `mvp-roadmap.md §2` |
| C8 | "Multiplatform is not a goal / no portability shims" vs Go + cross-compile + 3-OS CI | Principle #8 / D-006 vs T-064, T-067, T-068, T-069 |
| C9 | "Security from day one, not retrofitted" vs "auth/TLS later" on a network transfer service, and sandboxing deferred | Operating mode vs D-015.4, NOTED MUST-DOS |
| C10 | "If a seam admits more than one defensible design we have not found the seam" vs C1–C3 existing | Prime principle vs the repo |
| C11 | "Status: research phase — before any decisions" vs 18 decisions + a release roadmap | `project scope.txt` header vs DECISIONS, `mvp-roadmap.md` |
| C12 | D-001 goal `abstraction.org` vs research conclusion "do not fight for the name" | D-001 vs `competition.md §1.6` |
| C13 | "Master runs on the strongest model" vs architecture/security/thesis routed away from the master; master listed with `Intelligence: —` | D-018 vs `model-allocation.md` tables |
| C14 | Arbiter owns batching policy vs engines behind localhost HTTP running their own schedulers | `arbiter §3.2` vs `engine §4` |
| C15 | Identity fixes whole-file SHA256 / kills multihash vs vault's delta-and-quant-as-patch ambition | `identity §3.4` vs `the grammar.txt` seam 1 |
| C16 | Graph done-when "answer what calls foo()" vs zero `calls` edges emitted, Go-only LSP | `mvp-roadmap.md §1` vs `tags.go`, `lsp.go` |
| C17 | "Append only, never edit... recallable history of why" vs no supersede mechanism, so retractions live outside DECISIONS | `DECISIONS.md` header vs D-013/D-014, `competition.md` |
