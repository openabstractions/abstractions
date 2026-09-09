# Third-party notices

The record of everything in `openabstractions` that originated somewhere else.
Started 2026-09-05, before the first adoption from Chromium, go-winio, or
anything else outside this organisation. If you are about to take something
from another project, read [`docs/using-other-peoples-code.md`](docs/using-other-peoples-code.md)
first — it says what is acceptable to take and what a contributor must do.
This file is where the result gets recorded.

## The three cases, and why they are not the same obligation

**1. Code copied or adapted.** You took text — a function, an algorithm, a
config file, a test fixture — and it now exists, in modified or unmodified
form, inside an `openabstractions` repository. The original author's copyright
still applies to it. Their licence's conditions (attribution, licence-text
inclusion, "state changes you made," whatever it requires) are conditions on
*us* now, not on them. Getting this wrong is the one that can actually get the
project sued or forced to relicense a file. Record: their copyright notice,
their licence (text or a stable link), what was taken, and where it lives now.

**2. A dependency, linked or fetched at build time.** You did not copy
anything. Your `CMakeLists.txt` fetches nlohmann/json; your `go.mod` requires
go-winio. Their code never enters an `openabstractions` repository as source,
but their compiled object code ships inside binaries this project distributes,
and their licence's conditions (again typically attribution, licence-text
inclusion) travel with that binary. Record: name, version, licence, and one
line on why it's acceptable — normally just "permissive," but say so.

**3. A design or technique, learned by reading.** You read another project's
code, issue tracker, or documentation, understood *how* it solved a problem,
and wrote your own implementation of that idea. Ideas, algorithms-in-the-
abstract, and "the shape of a solution" are not copyrightable — this creates
no legal obligation at all. Record it anyway, as a citation: it is honest
about where the design came from, it lets a future reader go verify the
original problem still applies, and it costs nothing.

**Why the distinction matters:** treat case 3 like case 1 and every design
review turns into a copyright audit of things that were never at risk — the
project ships too slowly and contributors learn to stop citing anything.
Treat case 1 like case 3 — a citation instead of a real attribution — and a
line of someone else's code ships with no copyright notice attached to it,
which is the thing licence compliance exists to prevent. Case 2 is its own
category because the obligation is real but it is Not Our Code: nobody needs
to check *how* nlohmann/json parses a string, only that its licence is
permissive and its notice is reproduced.

---

## 1. Code copied or adapted

*(none yet — this project has not adopted from another codebase as of
2026-09-05. When it does, add a row here per file or cluster of files, in
this form:)*

| what | from | licence | taken | now lives at |
|---|---|---|---|---|
| *(example, not yet real)* `resume-range parser` | Chromium, `//components/download` | BSD-3-Clause | the header-parsing state machine for a partial `Content-Range` response | `abstraction-download/go/bits/range.go` |

Each row here must correspond to a provenance header on the file itself
(format in `docs/using-other-peoples-code.md`) and to the original copyright
notice being intact in that file. This table is a finding aid, not the
authoritative record — the file is.

## 2. Dependencies fetched or linked at build time

| component | version | licence | where it is pulled in | why it is acceptable |
|---|---|---|---|---|
| [nlohmann/json](https://github.com/nlohmann/json) | 3.11.3 (pinned by commit `9cca280a4d0ccf0c08f47a99aa71d1b0e52f8d03`) | MIT | `abstraction-job/CMakeLists.txt`, via `FetchContent` (or the caller's own copy, if `find_package` already finds one ≥ 3.9) | Permissive; MIT's only condition is reproducing its copyright and permission notice, satisfied by this entry plus `abstraction-job/NOTICE`. |
| [github.com/Microsoft/go-winio](https://github.com/microsoft/go-winio) | v0.6.2 | MIT | `service-jobd/go.mod`, statically linked into the `jobd` binary (Windows named-pipe support) | Permissive, from Microsoft's own Go team; same MIT condition as above. |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | v0.47.0 | BSD-3-Clause | `service-jobd/go.mod` | Permissive; part of the Go project itself. |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) (`x509roots/fallback`) | pseudo-version `v0.0.0-20260902174831-a6cdac608407` | BSD-3-Clause | `go.mod` in `service-jobd`, `addon-synology`, and `abstraction-download` (root-certificate fallback for TLS on platforms with no usable system store) | Permissive; part of the Go project itself. |

A dependency belongs in this table the moment it is added to a `go.mod`,
`CMakeLists.txt`, `requirements.txt`, or equivalent — not just when someone
remembers to update this file. Whoever adds the dependency adds the row in
the same change.

## 3. Designs and techniques learned by reading

| technique | learned from | applied in |
|---|---|---|
| Resuming a model download from the byte a predecessor process actually wrote, instead of restarting, and never handing a download-in-progress the final filename | ComfyUI-Manager's own issue tracker: [issue #2934](https://github.com/Comfy-Org/ComfyUI-Manager/issues) (a user who lost ~32 GB of partial downloads at 93–98%, filed 2026-05-31) and issue #101 (the same failure mode reported in 2023). Manager's downloader (`torchvision.datasets.utils.download_url`) writes straight to the final name with no temp file, no `Range` request, and no digest — that gap is what `adopter-comfyui` fills. | `adopter-comfyui` — see `__init__.py` for the full account of what was read and why forking was rejected instead. |

Nothing above required permission to write down — no code was taken, only an
observation about where an existing tool fails. That is exactly what belongs
in this section and nothing else does: if you find yourself wanting to write
a licence name next to an entry here, it is probably a case-1 or case-2 entry
instead.

---

## Keeping this current

This file is reviewed whenever:

- a new `go.mod` require, `CMakeLists.txt` fetch, or `requirements.txt` line
  adds a component not already listed under §2;
- a file is added or changed with a provenance header pointing at another
  project (§1);
- a design note or commit message cites another project as the source of an
  approach (§3) — cite it here too, not only in the commit.

It is not reviewed on a schedule; it is reviewed at the moment the fact it
records changes. A stale entry (a version bump nobody recorded, a dependency
removed but never struck from this table) is a bug in this file, filed and
fixed the same way any other inconsistency is.
