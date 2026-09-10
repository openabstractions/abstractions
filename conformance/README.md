# Conformance

Judge your implementation of the abstraction contracts against the rules on the
contract pages. You need this directory, a POSIX shell, your own program, and
the contract pages themselves, fetched by the commands below, which live in the
layer repositories rather than here — `contracts.list` beside `run.sh` is the
list of them, and it is the one the runner reads. You do not need our source
tree, our build, or Go. The scenarios that go over HTTP also want a
Python 3 to run `fixture.py`, the small server that answers them; without one
they report unreachable and say so.

## Run it

Write a driver — a program that applies a scenario to your implementation and
prints what an observer saw. [DRIVER.md](DRIVER.md) is the whole contract it
must keep; it is about a page of behaviour.

    sh run.sh -- ./my-driver

The runner prints one line per scenario and then the rules:

    PASS  lease: 8 expectations held
    FAIL  already-here: step 4
            expected: done=64 err=none
            observed: transfer-failed state=failed ...
    ----  wire-plain: UNREACHABLE — the driver does not declare wire

**Out of reach is never a pass.** A scenario your driver cannot run, a fixture
that would not start, a capability you have not implemented — each is counted
and named, and the run exits 2. A suite that goes green because something was
absent is the defect this exists to prevent.

| exit | meaning |
|---|---|
| 0 | every rule this suite can reach, your implementation keeps |
| 1 | a rule failed |
| 2 | incomplete: something could not be reached, or a contract page was missing |
| 3 | the suite could not be set up |

## Score honestly on a partial implementation

Most implementations are partial, and that is fine. Your driver declares what it
can do:

    ./my-driver --capabilities
    transfer wire

Each token names a group of operations — `transfer` moves bytes, `wire` does it
over HTTP against the fixture, `store` keeps records. [DRIVER.md](DRIVER.md)
lists them all. Scenarios needing anything you did not declare are reported
unreachable, with
the missing capability named. About half the download rules presuppose a job
store; a transfer-only implementation can now say so instead of failing rules
that do not apply to it. `run.sh` prints the count.

What a scenario needs is read from the operations in it, not only from the
`# requires:` line it declares — that line is a floor. A scenario that fetches
over HTTP needs `wire`, and one that submits a record and takes a lease needs
`store`, whether or not it says so. `capabilities.list` beside `run.sh` is the
closed set that places every operation under a capability; an operation it
places under none is unreachable by name, never guessed at and never a pass.
[DRIVER.md](DRIVER.md) is where a layer joining the suite adds its line.

The scenarios are grouped by layer, a directory each: `scenarios/` holds the
download and job rules, `identity/` the identity ones. `run.sh` reads one
directory per run, so ask for the layer you came for:

    sh run.sh --scenarios identity -- ./my-driver

## The contract pages

The rules have tags — `[DL-R28]`, `[JOB-L5]` — and scenarios cite the tag they
test. **Every expectation carries one**, so a run without the pages compares
steps and judges no rule. Fetch them into `contracts/`:

    mkdir -p contracts
    curl -L -o contracts/download.md https://raw.githubusercontent.com/openabstractions/abstraction-download/main/CONTRACT.md
    curl -L -o contracts/identity.md https://raw.githubusercontent.com/openabstractions/abstraction-identity/main/CONTRACT.md
    curl -L -o contracts/job.md      https://raw.githubusercontent.com/openabstractions/abstraction-job/main/CONTRACT.md
    curl -L -o contracts/logging.md  https://raw.githubusercontent.com/openabstractions/abstraction-logging/main/CONTRACT.md
    curl -L -o contracts/watch.md    https://raw.githubusercontent.com/openabstractions/abstraction-watch/main/README.md

`contracts.list` holds the same lines in a form the runner reads, so you
do not have to work out which page a missing tag came from: a page the scenarios
you selected cite and the run does not have is named, with the command that
fetches it, and the run exits 2. A tag an expectation cites and no page you
supplied declares is named with its scenario and step — usually it means a page
is missing, and once it does not, it means a scenario is testing a rule nobody
wrote down. With the pages in place the runner also reports what fraction of the
written contract these scenarios reach, and which rules no scenario names.

The pages are not copied into this directory on purpose. A normative page living
in two repositories is a reader who cannot tell which one binds them, and
`contracts.list` is a pointer rather than a second copy.

## Wire scenarios

Half the scenarios are about HTTP: a range honoured, a range ignored, a 416, a
`Content-Range` that lies about the body, a version that changed under a resume.
`fixture.py` serves those answers deterministically — Python 3, standard library
only, listening on a loopback port it prints on stdout. `run.sh` starts it when
a wire scenario is selected and your driver declares `wire`.

No Python? Pass `--no-fixture`, or point at your own server with
`--fixture-url`. Every wire scenario then reports unreachable, which is the
honest answer.

## Options

    sh run.sh [--scenarios DIR] [--contracts DIR] [--only NAME]...
              [--fixture-url URL | --no-fixture] -- <driver command ...>

## Check the runner itself

    sh selftest.sh

Five toy drivers. One declares nothing, one answers `ok` to everything, and one
admits it has no HTTP: the runner must call them not set up, failed, and
incomplete. A fourth answers real bodies, and the runner must refuse a superset
of one, a stray field, a negation and a reordering, and must report an operation
no capability places as out of reach rather than running it. A fifth declares
`identity` alone, and must be judged on identity alone and told by name what it
lacks for the scenario it cannot run. If the runner calls any of these green, do
not trust it.

## What this proves, and what it does not

A passing run says your implementation keeps the rules these scenarios reach. It
does not say the contract is complete, and it does not say our reference
implementation is right — if your driver fails a rule you believe you keep, that
is worth telling us, because a suite only we can run cannot distinguish a
correct contract from one design transcribed three times.

Scenarios that assert nothing are reported as such rather than counted. They pin
what somebody's implementation happened to do on the day they were written,
which is worth having and is not conformance.
