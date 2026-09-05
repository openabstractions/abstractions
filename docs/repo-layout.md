# Repository layout

How `openabstractions` is organised, and why. Decided 2026-09-05, during the
move off a single personal repository.

---

## The naming rule

GitHub has no sub-organisations. A prefix is the only namespace available, so it
carries the whole taxonomy:

| prefix | what it is | example |
|---|---|---|
| `abstractions` | the charter. Guidelines, the model, the concept. No code. | `abstractions` |
| `abstraction-*` | one layer: the interface, in every language that implements it | `abstraction-job` |
| `service-*` | a running implementation somebody installs | `service-jobd` |
| `adopter-*` | an integration into somebody else's program | `adopter-comfyui` |
| `research` | prior art, surveyed before designing | `research` |
| *(upstream name)* | a fork we contribute to | `lemonade` |

Reading a repository name tells you what kind of thing it is and whether you
depend on it, run it, or read it.

## Every language lives in its layer's repo

`abstraction-job` holds `go/`, `python/` and `cpp/` together.

The objection is fair: someone who wants Go does not want C++. Two reasons it is
still right here.

**The interop harness has to live somewhere.** The claim being tested is that
three independent implementations agree, and the evidence is a harness that runs
them against each other and compares bytes. Split the languages across
repositories and that harness spans repositories: it needs a checkout of each, a
CI system to coordinate them, and it can drift between releases. gRPC splits per
language and pays for a separate interop suite; that is a reasonable trade at
gRPC's size and an unreasonable one at ours.

**The cost is small.** A layer is a few thousand lines per language, and each
toolchain takes only what it needs anyway: `go get` resolves the `/go`
submodule, pip installs from a subdirectory, and CMake builds `cpp/` and ignores
the rest.

If a language ever grows its own release cadence, split it then.

## Services: one repo per service, platforms inside

`jobd` cross-compiles; its BITS backend is a build-tagged file, not a fork.
Splitting a service by platform is right only when the implementations share
nothing — and at that point they are different services with one interface,
which is a layer, not a split.

## Adopters: one repo each, and for ComfyUI it is forced

ComfyUI-Manager's registry lists custom nodes *by git repository*, and
installing one means cloning it. `adopter-comfyui` cannot be a subdirectory of
anything else and still be installable the ordinary way.

## What this costs, stated plainly

- **Cross-layer changes stop being atomic.** `download` depends on `job`.
  Changing both now means releasing `job`, then updating `download`. During
  development a `go.work` file spans the checkouts; for consumers it means real
  versions. That friction is arguably correct — a facade whose contract is cheap
  to change is not a facade — but it is friction, and it is new.
- **Adopters integrate more pieces.** Lemonade went from one `FetchContent`
  block to one per layer it uses.
- **Releases are ordered.** Leaves first (`job`, `storage`, `config`,
  `logging`), then `download`, then `model` and the facade.

## History

Every repository carries the history of the files in it, extracted with
`git filter-repo`. The migration also removed hardware identifiers that a
transcript had captured and that a later commit had only scrubbed from the tip —
they were still reachable in older commits, which is not scrubbed at all.
