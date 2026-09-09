# Where this stands

A handoff for a fresh session. Everything below is durable in the
repositories; the conversation that produced it is not needed.

## What this is

A vocabulary for what local software rebuilds on its own, and the corpus that
holds every implementation to it. `README.md` says what a person can take
today; `SCOPE.md` says which layers qualify; `METHOD.md` says how one is
drawn. Adopters depend on a facade, never the reverse.

## Repositories (github.com/openabstractions)

- `abstraction-*` — one layer each, every language inside: job, download,
  storage, cas, watch, config, logging, model, identity, asks, rights, facade
- `service-jobd`, `docker-jobd`, `addon-synology`, `polite-monitor` — things a
  person installs
- `adopter-comfyui` — an integration. The other adopter is a Lemonade fork,
  `ReinisLusis/lemonade@abstraction-job-store`
- `research` — prior art
- `.github` — governance, security policy, the organisation page
- `abstractions` — this repository: method, scope, results, the harness scripts

`openabstractions.org` is registered.

## Open, in order

1. **Which written form of a contract is normative.** Three exist: prose with
   tagged rules on the contract pages, which the scenarios cite; `job.thrift`,
   published and checked against nothing; and a schema that generated passing
   clients and describes a wire rather than the record. `METHOD.md` §6 states
   it as open. Everything built on a schema inherits the answer.
2. **Move the corpus and the harness here**, so a layer does not own the test
   it is judged by and a stranger can run it against their own implementation.
   With it, a `GENERATED` marker, so a generated implementation does not count
   toward the at-least-two bar.
3. **One source for the record**: the schema describes the record and either
   generates `job.thrift` or is generated from it.

Behind those, two things the record cannot express today: a delegate's failure
arriving where the request was made, and a credential refusing a delegate
whose trust posture it cannot vouch for.

## Releases

`vX.Y.Z`; a tag means a release and is cut only for one. The only tag is
`go/v0.1.0`. `layers.lock` records which commits were proved together.

## How to work

- READMEs follow `docs/readme-contract.md`.
- The backlog, `docs/backlog.md`, holds design notes; five lines each.
- A number is claimed only where a transcript in `docs/results/` shows it, and
  a platform not reached is `UNPROVEN`, never a pass.
