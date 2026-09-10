# abstractions

**In development.** No tagged release; the scope, method and results below are
current, but nothing here has shipped as a stable version.

Contracts for capabilities that applications keep rebuilding privately —
durable work, downloads, storage, logging, local caller identity — each written
so more than one implementation can satisfy it, and a conformance suite that
tests whether an implementation actually does.

Every platform already provides these capabilities, in a different shape, to its
own programs. Windows has a transfer service that outlives the process that asked
(BITS); Linux and macOS have neither it nor each other's answer. An application
that wants the capability on all three either picks one platform or writes its
own, and writing its own is what everybody does. Three shapes and no common call
is the seat SLF4J found in logging. [`METHOD.md`](METHOD.md) §14 says which
seats qualify and which do not.

This repository is the parent: the scope rules, the method, the measured results,
and the suite. It is a test suite, not a library — the code is in the layer
repositories listed below.

**Deciding whether to adopt any of this?**
[What adopting involves](https://openabstractions.org/adopt.html) — including
where the honest answer is "no install line is printed here, because none has
been run from a clean machine" — and
[what is proven and what is not](https://openabstractions.org/coverage.html).
[CONTRIBUTING.md § Adopting](CONTRIBUTING.md#adopting) is the same ground with
every link in one place.

## Use a layer

Nothing here is installed. Each layer is its own repository with its own README,
install command and example:

| you want | repository |
|---|---|
| work that outlives the process that asked for it | [`abstraction-job`](https://github.com/openabstractions/abstraction-job) |
| a download that can be finished by somebody else | [`abstraction-download`](https://github.com/openabstractions/abstraction-download) |
| bytes at rest, named by digest | [`abstraction-storage`](https://github.com/openabstractions/abstraction-storage) |
| compare-and-set over a file, on the kernel's own lock | [`abstraction-cas`](https://github.com/openabstractions/abstraction-cas) |
| to be told a record changed, without polling | [`abstraction-watch`](https://github.com/openabstractions/abstraction-watch) |
| a log line that survives the process that wrote it | [`abstraction-logging`](https://github.com/openabstractions/abstraction-logging) |
| to know which program is on the other end of a local connection | [`abstraction-identity`](https://github.com/openabstractions/abstraction-identity) |
| to ask this machine what it can do, without naming who answers | [`abstraction-facade`](https://github.com/openabstractions/abstraction-facade) |
| where a machine keeps its answer to "which store" | [`abstraction-config`](https://github.com/openabstractions/abstraction-config) |
| a closed catalogue of questions a machine may ask a person | [`abstraction-asks`](https://github.com/openabstractions/abstraction-asks) |
| to say which programs may hold a machine awake | [`abstraction-rights`](https://github.com/openabstractions/abstraction-rights) |
| model weights named across stores that disagree about names | [`abstraction-model`](https://github.com/openabstractions/abstraction-model) |

Running implementations built on those contracts:
[`service-jobd`](https://github.com/openabstractions/service-jobd), a supervisor
that finishes work nobody is watching;
[`addon-synology`](https://github.com/openabstractions/addon-synology), a NAS as
the fetcher for a LAN; [`docker-jobd`](https://github.com/openabstractions/docker-jobd);
[`polite-monitor`](https://github.com/openabstractions/polite-monitor), a Windows
window listing what the machine is doing; and
[`adopter-comfyui`](https://github.com/openabstractions/adopter-comfyui), which
puts ComfyUI-Manager's downloads behind a job record.

A layer repository holds every language for that one contract, because the
conformance proof compares bytes across languages
([`docs/repo-layout.md`](docs/repo-layout.md)).

## Judge an implementation, including one of ours

You need [`conformance/`](conformance/), a POSIX shell, and a program of your own
that applies a scenario and prints what it saw
([`conformance/DRIVER.md`](conformance/DRIVER.md) is the whole contract that
program keeps). Our source tree, our build and Go are not required.

    sh conformance/run.sh -- ./my-driver

Before trusting it, check the runner against toy drivers that are wrong in three
different ways:

    $ sh conformance/selftest.sh
    conformance selftest
      ok    a driver that declares nothing is not set up (exit 3)
      ok    a driver that answers ok to everything fails (exit 1)
      ok    a rule out of reach is incomplete, never a pass (exit 2)
      ok    a superset, a stray field and a negation are all refused (3 refused, 1 held)
      RESULT: the runner distinguishes passed, failed and out of reach

A capability you have not implemented, a fixture that would not start, a scenario
out of reach: each is counted and named, and the run exits 2. Out of reach is
never a pass. A partial implementation declares what it can do and is scored on
that.

The rules themselves are tagged — `[DL-R28]`, `[JOB-L5]` — on each layer's
`CONTRACT.md`, and every scenario cites the tag it tests. The pages are not
copied here on purpose: a normative page in two repositories is a reader who
cannot tell which one binds them.

## Measured

**The coverage grid** answers "is any of this real" better than this page can:
one row per layer, one column per language, one verdict per cell —
[`docs/results/MATRIX.txt`](docs/results/MATRIX.txt), and
[the same grid on the web](https://openabstractions.org/coverage.html). It reads
what is committed in this repository and the transcripts below, and runs no
implementation: it reports what was recorded, never what would happen if you ran
it now.

It uses five verdicts because four kinds of gap are not one gap. `PASS` and
`FAIL` mean a recorded run reached the cell. `UNPROVEN` means it could not be
checked, and names why. `ABSENT` means it was checked and the thing is not
there. `—` means there is no implementation at all — not an untested one and not
a finished one — and each `—` carries whether that gap is declared deliberate or
simply unexplained. No count from the grid is repeated on this page: it names
the commit and the layer trees it measured, and `sh scripts/matrix.sh --check`
refuses a grid the evidence no longer produces, so one that has gone stale fails
the gate instead of reading as coverage.

Transcripts of every run are in [`docs/results/`](docs/results/), indexed in
[`docs/results/README.md`](docs/results/README.md) with the script that produced
each and the state of the machine. They are our own output on our own machines,
so read them as a record of what happened once, not as independent verification.
A pass by absence is a defect here.

- A killed download resumes from the proven prefix; bytes written past the last
  checkpoint are discarded. [`RESUME1.txt`](docs/results/RESUME1.txt).
- Go, Python and C++ replay the whole scenario corpus and produce identical
  transcripts. [`BEHAVIOUR1.txt`](docs/results/BEHAVIOUR1.txt).
- Go and Python finish each other's downloads, both directions, digests matching.
  [`XLANG-DOWNLOAD.txt`](docs/results/XLANG-DOWNLOAD.txt).
- A NAS finishes and verifies a 386 MB file after every process of ours on the PC
  was killed. DSM 6.2.4 with the package scripts run by hand; Package Center and
  DSM 7 `UNPROVEN`. [`NAS1.txt`](docs/results/NAS1.txt).
- Six writers in three languages, one file, no lost update. Windows; macOS
  `UNPROVEN`. [`CAS-MIXED1.txt`](docs/results/CAS-MIXED1.txt).
- `curl`, which links nothing of ours, is refused from a listed host by the
  platform packet filter with our reason in the kernel log. Linux; the Windows
  rule text is rendered and unapplied, `UNPROVEN`.
  [`REACH1.txt`](docs/results/REACH1.txt).
- The machine is held awake for the life of a lease, read back from the kernel's
  own execution state. Windows and Linux; macOS `UNPROVEN`.
  [`AWAKE1.txt`](docs/results/AWAKE1.txt).

## Status

Experimental, Apache-2.0, and one maintainer. Nothing carries an API stability
promise, and no version number is typed on this page: each repository's tag list
is the answer to "which release", because a tag is the only thing that cannot
drift. **Go 1.26 or later is required.** Go is also the only language with a
tagged release anywhere: every Python implementation is on no package index and
is adopted by pinning a commit — each layer's `python/README.md` says what to
install, what to import and shows one example that runs — and no C++
implementation has a tagged release at all.

The only adopters are ours, so nobody outside has yet had to live with these
names. Every published transcript was produced on Windows or Linux; macOS is
`UNPROVEN` throughout. Read a repository's Status section before depending on it.

[`GOVERNANCE.md`](https://github.com/openabstractions/.github/blob/main/GOVERNANCE.md)
says what happens if the maintainer stops;
[`SECURITY.md`](https://github.com/openabstractions/.github/blob/main/SECURITY.md)
says how to report a fault.

## Also here

- [`METHOD.md`](METHOD.md) — how an interface is drawn and tested here, and
  §14 which layers qualify at all. Read §14 before proposing one.
- [`STATE.md`](STATE.md) — what is open, in order.
- [`docs/try-it.md`](docs/try-it.md) — one `dl` command across three fetchers.
- [`docs/integrating.md`](docs/integrating.md) — what adopting these interfaces
  taught them.
- [`docs/using-other-peoples-code.md`](docs/using-other-peoples-code.md) — what
  licences code may be taken from.
- [`research`](https://github.com/openabstractions/research) — the prior art read
  before designing.

## Licence

Apache-2.0. See [`LICENSE`](LICENSE). Code and techniques taken from elsewhere
are recorded in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
