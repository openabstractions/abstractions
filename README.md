# abstractions

A set of interfaces — not implementations — for three services that local AI
tools currently each rebuild privately: fetching bytes, storing them, and
knowing what work is in flight on the machine.

This repository is the charter: the method, the scope rule, the backlog, and
the transcripts and scripts from the runs that tested the interfaces. It ships
no library code. That lives in one repository per layer — see
[Where the code is](#where-the-code-is).

## The problem

A tool that downloads a model usually owns that download inside its own
process. Where it does, closing the tool stops the transfer; starting it again
leaves any progress still on disk invisible, because the only record of the
work was in memory. A second tool on the same machine cannot see the transfer
at all, and keeps its own copy of the same weights under its own naming scheme.
What individual tools actually do here is surveyed, tool by tool, in
[`research/tools`](https://github.com/openabstractions/research/tree/main/tools)
and [`research/integration`](https://github.com/openabstractions/research/tree/main/integration);
some resume correctly and some do not.

The response here is to move the record of the work out of the process. An
application writes a **job** — a durable record of work someone will do — and
asks for bytes without naming who fetches them. On a machine with a NAS
configured, the NAS fetches them; on a Windows machine without one, the OS
transfer service (BITS) does; with neither, the calling process does. The
application branches on none of it, and holds no path, hostname or flag.

The calling shape, in Go — `a` is the discovered facade, `url` and `dest` are
the caller's own strings, and `ui.Bind` stands for whatever the application
displays a list with:

```go
a, _ := abstraction.Discover()

ui.Bind(a.Download().Jobs())            // everything in flight, including
                                        // work that predates this process
job, _ := a.Download().Get(url, dest)   // returns now; outlives this program
```

The compilable version, with imports and error handling, is in
[`abstraction-facade`](https://github.com/openabstractions/abstraction-facade).

## Where the code is

Every repository below is under [github.com/openabstractions](https://github.com/openabstractions).

| repository | what is in it |
|---|---|
| [`abstraction-job`](https://github.com/openabstractions/abstraction-job) | The job contract and the rules for taking ownership of a job. Go, Python, C++. [`job.thrift`](https://github.com/openabstractions/abstraction-job/blob/main/job.thrift) states the record in a notation that is nobody's language. |
| [`abstraction-download`](https://github.com/openabstractions/abstraction-download) | Bytes that keep arriving after the caller exits. Go, Python, and a C++ reader. HTTP, local file, Windows BITS and NAS behind one interface. |
| [`abstraction-storage`](https://github.com/openabstractions/abstraction-storage) | Where bytes live, addressed by what they are rather than by filename. Go, C++. |
| [`abstraction-facade`](https://github.com/openabstractions/abstraction-facade) | The single entry point: `Discover()` once, then `Jobs()`, `Download()`, `Log()`. Go. |
| [`abstraction-model`](https://github.com/openabstractions/abstraction-model) | Naming and finding model weights across stores that disagree about names. Go. |
| [`abstraction-logging`](https://github.com/openabstractions/abstraction-logging) | The part of a log line that leaves the process. Go, Python. |
| [`abstraction-config`](https://github.com/openabstractions/abstraction-config) | How a machine answers "which store" — discovery, not per-application configuration. Go. |
| [`adopter-comfyui`](https://github.com/openabstractions/adopter-comfyui) | A ComfyUI custom node that puts ComfyUI-Manager's downloads behind a job record. |
| [`research`](https://github.com/openabstractions/research) | Prior art surveyed before designing: transfer engines, IPC, durable execution, and the tools these interfaces are meant to fit. |
| [`abstractions`](https://github.com/openabstractions/abstractions) | This repository. Method, scope, backlog, results, and the test scripts. No library code. |

One adopter is not in the organisation: a fork of Lemonade, on branch
[`ReinisLusis/lemonade@abstraction-job-store`](https://github.com/ReinisLusis/lemonade/tree/abstraction-job-store),
which compiles the C++ job store in and answers `/api/v1/downloads` from
records on disk. What that integration taught the interfaces is written up in
[`docs/integrating.md`](docs/integrating.md).

Command-line programs, each built from the repository named above it:

```
dl        the generic downloader        abstraction-download/go/cmd/dl
jobd      the system downloader         abstraction-download/go/cmd/jobd
jobctl    read and write job records    abstraction-job/go/cmd/jobctl
modelget  the model-aware downloader    abstraction-model/go/cmd/modelget
```

## The layers

| layer | what it is |
|---|---|
| `facade` | The one entry point. Discovered once; an application holds nothing else. |
| `model` | AI-specific: quantisations, published digests, model stores. Builds a download spec and hands it down. |
| `download` | Bytes: artifact, sources, sink. Contains no AI vocabulary. |
| `job` | A handle to work happening somewhere else. |
| `transport` | How a call reaches whoever executes it. This is the **binding**. |

Dependencies point one way: `model` imports `download` imports `job`, and
nothing imports `model`. The rule that keeps the split honest is that a generic
layer must contain no word from a specific one, which is checkable:

```bash
grep -rniE "model|gguf|huggingface|ollama" go --include=*.go
```

Three hits are expected in `download`: `MODELGET_STORE` and `~/.modelget` are
honoured as legacy aliases, because stores exist on disk under those names.

A **binding** is a concrete way of carrying the job record — files in a
directory, a service behind a socket, a map in memory. Nothing above `job` may
name one. Today only the file binding has an implementation outside the test
suite, so `transport` is a declared boundary rather than finished work.

## How to try it

There are no published binaries yet. Build from the layer repositories:

```bash
git clone git@github.com:openabstractions/abstraction-job.git
git clone git@github.com:openabstractions/abstraction-download.git
cd abstraction-download/go && go build ./cmd/dl && go build ./cmd/jobd
```

[`docs/try-it.md`](docs/try-it.md) then runs the same `dl` command three times
against three different fetchers — the calling process, BITS, and a NAS — with
the command string unchanged between them. Part 1 runs anywhere Go does; part 2
needs Windows and part 3 needs a NAS. The scripts that produced the transcripts
below are in [`scripts/`](scripts/) and expect the layer repositories checked
out as siblings.

## What has been tested

Transcripts of every run are in [`docs/results/`](docs/results/), indexed in
[`docs/results/README.md`](docs/results/README.md) with the script that
produced each one. They are our own terminal output, so they are a record of
what happened here, not independent verification.

- **Go and Python finish each other's downloads.** One implementation starts a
  transfer and is killed before it releases its lease or writes a final
  checkpoint; the other resumes from the last proven byte and delivers a file
  whose digest matches. Both directions.
  [`XLANG-DOWNLOAD.txt`](docs/results/XLANG-DOWNLOAD.txt).
- **Go and Python hand job records to each other** — claim, epoch, lease lapse
  and adoption — with no shared code and no generated schema. No bytes move in
  this one. [`XLANG1.txt`](docs/results/XLANG1.txt),
  [`XLANG2.txt`](docs/results/XLANG2.txt).
- **A killed download resumes from the proven prefix, not from the bytes on
  disk.** The dead process had written 61,163,581 bytes and checkpointed
  52,824,170; the difference is discarded because nothing vouches for it.
  [`RESUME1.txt`](docs/results/RESUME1.txt).
- **A supervisor adopts an orphaned job** — owner dead, lease lapsed — and
  delegates it onward. [`SUPERVISOR1.txt`](docs/results/SUPERVISOR1.txt).
- **A 313 MB model fetched by a Synology NAS and delivered to a Windows PC**,
  sha256 matching what HuggingFace published. The NAS runs the same binary over
  the same job store; there is no NAS-specific code in any layer.
  [`DEMO.txt`](docs/results/DEMO.txt).
- **Go, Python and C++ read and write the same job records.**
  [`CONFORM1.txt`](docs/results/CONFORM1.txt).

## Status

Experimental. Nothing here has a released version, an API stability promise, or
a second adopter that is not ours.

What holds up:

- `job` and `download` each have independent implementations that pass the same
  tests and hand work to each other. `job` has three (Go, Python, C++);
  `download` has three, though the C++ one only reads. `logging` has two,
  `storage` has two, and `model` and `config` are Go only.
- The `job` assertions — exclusive claim, rising epoch, stale writes refused,
  intent recorded without a lease, a paused job kept out of sweeps, a successor
  inheriting the proven prefix — run against three bindings: files in a
  directory, a service over a socket, and a map in memory. No line of the
  assertions knows which is underneath.

What does not:

- **Both adopters are ours.** The Lemonade fork and the ComfyUI node were both
  written here, and no PR has been proposed upstream. Until somebody outside
  chooses one of these interfaces, any claim that they are general is a claim
  about our own taste.
- **The layers above `job` have only ever run on the file binding.**
  `download` needs a local area for partials and asks for it through
  `job.Scratch`, which the socket and memory bindings decline. The job contract
  is substitutable; the stack on top of it is not yet.
- **Delegation to a NAS has been proven end to end once.** Progress from a
  delegate has not been shown on transfers short enough to finish inside one
  checkpoint interval — the 112 MB run sat at 0% and jumped to 100%.
- **A delegated transfer is serial.** The NAS fetches the whole artifact before
  the local machine copies any of it, so the wall clock is the sum of both
  legs. The prerequisite for fixing it is piecewise digests:
  [`docs/incremental-delivery.md`](docs/incremental-delivery.md).
- **One C++ test cannot be rebuilt.** `test_download_layer.exe` runs and
  passes, but its source was left in a directory that no longer exists.
- **`model` is barely exercised.** One implementation, one caller.

## Reading order

- [`SCOPE.md`](SCOPE.md) — which services qualify for this library, and in what
  order. Read it before proposing a layer.
- [`METHOD.md`](METHOD.md) — how an interface gets drawn and tested here.
- [`docs/repo-layout.md`](docs/repo-layout.md) — why the repositories are split.
- [`docs/backlog.md`](docs/backlog.md) — ideas not being built yet.
- [`docs/origin.txt`](docs/origin.txt) — the problem as originally stated.
- [`docs/using-other-peoples-code.md`](docs/using-other-peoples-code.md) — what
  licences code may be taken from, and what a contributor must do when they
  copy something. Read before adapting anything from outside this
  organisation.

## Licence

Apache-2.0. See [`LICENSE`](LICENSE). Third-party code, dependencies, and
techniques this project has taken from elsewhere are recorded in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
