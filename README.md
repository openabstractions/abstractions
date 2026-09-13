# OpenAbstractions

Applications adopt common capability APIs while services own execution, shared
state and lifecycle. Contracts, generated clients and shared transports let a
capability be used independently of its provider. Explicit wrappers around
existing engines remain available as an adoption path with declared guarantees.

## Start with a service client

Choose a capability from [the facade](https://github.com/openabstractions/abstraction-facade)
and follow its language package instructions. Go, C++, Python, Rust and JavaScript
have generated service clients; implemented capabilities and transport support
vary by package. Requests carry required guarantees. Missing or incompatible
services produce explicit failures.

Install a compatible runtime from [redist releases](https://github.com/openabstractions/redist/releases),
then use `openabstractions start` and `openabstractions status`. Consult each
release's asset, signing and platform evidence. This page does not announce an
unpublished version. For development, select a coordinated source revision and
independently configure trust for an explicit host.

Go/C++/Python default local bindings verify installed runtime account/program
identity before resolver and provider payloads on Windows and supported Linux.
Rust/JavaScript need explicit independent trust configuration. macOS currently
refuses the local Program proof required by these service profiles. A running
process alone does not establish capability readiness.

Applications retain request identity and owner/binding information for recovery.
Accepted work stays with its original owner; cancelling a wait does not cancel
that work. Services own NAS/backend access and credentials. An unavailable runtime
does not silently become an application file store.

This repository contains the central runtime/CLI, panel, IDL generator, contracts
and conformance tools. Capability and provider packages live in the repositories
below. [The adoption guide](https://openabstractions.org/adopt.html) describes the
ownership model; [CONTRIBUTING.md](CONTRIBUTING.md#adopting) gives revision and
evidence checks for contributors.

## Use a layer

Each capability repository documents its clients, provider scope and package
requirements. Select only the capabilities an application needs:

| you want | repository |
|---|---|
| work that outlives the process that asked for it | [`abstraction-job`](https://github.com/openabstractions/abstraction-job) |
| a download that can be finished by somebody else | [`abstraction-download`](https://github.com/openabstractions/abstraction-download) |
| bytes at rest, named by digest | [`abstraction-storage`](https://github.com/openabstractions/abstraction-storage) |
| compare-and-set over a file, on the kernel's own lock | [`abstraction-cas`](https://github.com/openabstractions/abstraction-cas) |
| bounded observation of changing state | [`abstraction-watch`](https://github.com/openabstractions/abstraction-watch) |
| structured logging and bounded service-owned history | [`abstraction-logging`](https://github.com/openabstractions/abstraction-logging) |
| to know which program is on the other end of a local connection | [`abstraction-identity`](https://github.com/openabstractions/abstraction-identity) |
| to ask this machine what it can do, without naming who answers | [`abstraction-facade`](https://github.com/openabstractions/abstraction-facade) |
| where a machine keeps its answer to "which store" | [`abstraction-config`](https://github.com/openabstractions/abstraction-config) |
| a closed catalogue of questions a machine may ask a person | [`abstraction-asks`](https://github.com/openabstractions/abstraction-asks) |
| to grant, inspect and revoke application permissions | [`abstraction-rights`](https://github.com/openabstractions/abstraction-rights) |
| model weights named across stores that disagree about names | [`abstraction-model`](https://github.com/openabstractions/abstraction-model) |

Running implementations built on those contracts:
[`service-jobd`](https://github.com/openabstractions/service-jobd), a supervisor
that finishes work nobody is watching;
[`addon-synology`](https://github.com/openabstractions/addon-synology), a NAS as
the fetcher for a LAN; [`docker-jobd`](https://github.com/openabstractions/docker-jobd);
[`polite-monitor`](https://github.com/openabstractions/polite-monitor), a Windows
window listing what the machine is doing; and
[`adopter-comfyui`](https://github.com/openabstractions/adopter-comfyui), which
routes ComfyUI-Manager downloads through the durable job service.

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
the commit and the layer trees it measured, and the maintainer gate checks that the grid agrees with its evidence.
The standalone `conformance/` runner and `idl/` generator are the public tools;
private orchestration and publication scripts are not included.

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

Source support, package publication and native installation qualification are
separate results. Use each repository's release metadata and capability contract.
Development APIs may change; coordinated source packages can precede registry
publication. Go source builds require the version declared in their module files.

The evidence below is historical and scoped to its named sources and hosts.
Current Windows/Linux local trust and service clients have focused tests; macOS
native lifecycle measurements preserve the known Program-proof refusal. A build
or a generated client alone does not establish provider or installation behavior.

[`GOVERNANCE.md`](https://github.com/openabstractions/.github/blob/main/GOVERNANCE.md)
says what happens if the maintainer stops;
[`SECURITY.md`](https://github.com/openabstractions/.github/blob/main/SECURITY.md)
says how to report a fault.

## Also here

- [`METHOD.md`](METHOD.md) — how an interface is drawn and tested here, and
  §14 which layers qualify at all. Read §14 before proposing one.
- [`STATE.md`](STATE.md) — what is open, in order.
- [`docs/try-it.md`](docs/try-it.md) — historical legacy-worker demonstration across three fetchers.
- [`docs/integrating.md`](docs/integrating.md) — what adopting these interfaces
  taught them.
- [`docs/using-other-peoples-code.md`](docs/using-other-peoples-code.md) — what
  licences code may be taken from.
- [`research`](https://github.com/openabstractions/research) — the prior art read
  before designing.

## Licence

Apache-2.0. See [`LICENSE`](LICENSE). Code and techniques taken from elsewhere
are recorded in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
