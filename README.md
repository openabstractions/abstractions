# OpenAbstractions

OpenAbstractions lets an application ask this computer to do useful work: keep a
download running after the application closes, call a model without handling its
API key, read shared content, or ask a person for a decision. The person running
the computer chooses which local program, background service or remote provider
does that work.

The application receives clear results and failures through one API for each
kind of work. The service keeps shared state, credentials and recovery data. An
operator can replace the program doing future work without rewriting the
application.

This repository is the public development charter. It contains the runtime,
control panel, conformance runner, examples and recorded evidence. Capability
contracts and language packages live in the linked repositories.
[Release notes](https://github.com/openabstractions/redist/releases/latest) name
the available packages, included features and platform verification.

## What you can build

| Need | Application API | Current provider path |
| --- | --- | --- |
| Durable downloads | [`abstraction-download`](https://github.com/openabstractions/abstraction-download) through durable jobs | Runtime-owned HTTP execution and configured native providers |
| Durable work | [`abstraction-job`](https://github.com/openabstractions/abstraction-job) acceptance and operation clients | Service-owned acceptance, recovery and results |
| Shared content | [`abstraction-storage`](https://github.com/openabstractions/abstraction-storage) content and change clients | Runtime content store or an explicitly selected storage provider |
| Configuration | [`abstraction-config`](https://github.com/openabstractions/abstraction-config) reader, editor and observer | Service-owned machine and user settings |
| Authorization | [`abstraction-rights`](https://github.com/openabstractions/abstraction-rights) decision and operator clients | Exact subject, action and resource rules |
| Human decisions | [`abstraction-asks`](https://github.com/openabstractions/abstraction-asks) application and operator clients | Bounded question and answer service |
| Model resolution | [`abstraction-model`](https://github.com/openabstractions/abstraction-model) resolver | Authorized storage manifests and registries |
| Provider routing | [`abstraction-router`](https://github.com/openabstractions/abstraction-router) inventory and selection | Service-owned model and host catalogue |
| Model inference | `abstraction.inference` development contracts | Local OpenAI-compatible hosts, selected hosted hosts and native providers |
| Named credentials | `abstraction.credentials` development contracts | Platform secure store or an explicit development backend |
| Application presence | Facade application directory and activation clients | Registered programs with leased live instances and contexts |
| Structured events | [`abstraction-logging`](https://github.com/openabstractions/abstraction-logging) sinks and history clients | Identity-attributed service collection and observation |

The [facade](https://github.com/openabstractions/abstraction-facade) is the
normal application entrypoint. It resolves a service from requirements such as
contract version, placement, guarantees and readiness. Each capability call is
authorized again at the service boundary.

```text
application -> facade resolver -> generated capability client -> service -> provider
                                                            \-> service-owned state
```

Accepted work stays with the service that accepted it. A caller timeout cancels
the wait. Explicit cancellation is a separate operation. Durable clients retain
the original binding and request identity to reconcile a lost reply without
creating duplicate work.

## Connect tools people already use

An operator can connect an existing downloader, model engine or remote service.
The application keeps asking for a download or model call in the same way. OA
checks access, applies named credentials, limits the request and records which
provider accepted it. Removing that provider stops new work from going there;
work it already accepted keeps the same owner through completion.

An assistant can find a supported editor or ComfyUI workflow, open the
application when it is closed, and propose a change to the item the person is
viewing. The target application shows what would change. The person applies it
there, and the assistant can read back the observed result. In the controlled
ComfyUI example, OpenCode proposed changing a sampler's steps from 20 to 24;
ComfyUI showed the preview, applied it separately, and rejected the old request
after the workflow changed.

Provider generations keep accepted work with one owner. Application leases
remove closed instances. Instance, context and revision checks stop a request
from landing in a different document or workflow. These are development proofs;
release packaging remains under qualification.

## Try the development runtime

Build one fixed executable so exact-program grants remain attached to the same
program identity. Requirements are Go 1.26 or newer and any platform tools named
by the capability you exercise.

```console
go build -o ./out/openabstractions ./serve
./out/openabstractions --help
./out/openabstractions serve runtime --isolated quickstart --state-dir /absolute/path/to/oa-state
```

The runtime prints an `ABSTRACTION_RUNTIME_ENDPOINT`. Keep it running, set that
value in another terminal, and inspect readiness:

```console
./out/openabstractions status --json
./out/openabstractions probe --json
```

`probe` performs bounded capability reads and preserves typed unavailable or
forbidden outcomes. Operator commands include durable downloads and jobs,
credentials, rights, inference hosts, providers and applications. Run
`<command> --help` before changing machine state.

The control panel in [`monitor/`](monitor/) uses the same generated clients for
configuration, rights, applications and service status. It is the current OA
panel. The separate `polite-monitor` repository is a retained legacy program.

## Use it from an application

Choose the facade package for your language: [Go](https://github.com/openabstractions/abstraction-facade#go),
[C++17](https://github.com/openabstractions/abstraction-facade#c17),
[Python](https://github.com/openabstractions/abstraction-facade#python),
[Rust](https://github.com/openabstractions/abstraction-facade#rust), or
[JavaScript](https://github.com/openabstractions/abstraction-facade#javascript).
Each generated client uses the shared OA service envelope and preserves typed
outcomes. Package availability and provider support vary by language and
capability; use the package README and its recorded evidence.

Pin the exact package revision you test. A coordinated source build proves
source compatibility for that revision. Release pages state which independently
published packages and native artifacts are available.

## Current limits

- Package availability, signing and installation verification are recorded per release.
- macOS native IPC lacks the program proof required by protected service calls,
  and those calls fail closed.
- JavaScript/Bun native clients require an explicit controlled endpoint.
  Installed selection and configured server-expectation verification return
  `ProofUnavailable`.
- Remote services and compatibility HTTP windows have narrower trust evidence
  than native local IPC.
- Hosted-provider qualification is separate from controlled fixtures. Recorded
  tests make no paid-provider claim.

The [recorded results](docs/results/README.md) name the revision, platform and
scope behind each claim. They are historical observations from project-owned
machines. A build or generated client alone does not establish runtime,
provider or installation behavior.

## Judge an implementation

The standalone [`conformance/`](conformance/README.md) runner checks another
implementation through its public behavior. Supply a program that applies one
scenario and prints what it observed:

```console
sh conformance/run.sh -- ./my-driver
```

The runner reports passed, failed and out-of-reach scenarios separately. Each
scenario cites the tagged rule from the capability repository's `CONTRACT.md`.
The [coverage grid](docs/results/MATRIX.txt) summarizes committed transcripts;
it does not run implementations while rendering the table.

## Repository map

- [`serve/`](serve/) — runtime and operator command source.
- [`monitor/`](monitor/) — current control panel source.
- [`examples/`](examples/) — small integration examples.
- [`conformance/`](conformance/README.md) — independent behavior runner.
- [`docs/results/`](docs/results/README.md) — historical test transcripts and
  their environments.
- [`METHOD.md`](METHOD.md) — criteria for adding or changing an abstraction.
- [`docs/REMOVED.md`](docs/REMOVED.md) — retired entrypoints and replacements.

Contributors should read [CONTRIBUTING.md](CONTRIBUTING.md). Governance and
security reporting live in the organization profile repositories. Code and
techniques taken from elsewhere are recorded in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

Apache-2.0. See [`LICENSE`](LICENSE).
