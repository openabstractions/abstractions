# Where this stands

A handoff for a fresh session. The conversation that produced the current state
is not needed; everything below is durable in the repositories.

## What this is

A standard library of interfaces for what local software rebuilds badly:
durable downloading, a record of work in flight, storage, logging. Three
independent implementations (Go, Python, C++) agree on one cross-language
contract, checked by byte-identical conformance harnesses. Adopters depend on
the facade, never the reverse.

## Repositories (github.com/openabstractions)

- abstraction-job, abstraction-download, abstraction-storage, abstraction-config,
  abstraction-logging, abstraction-model, abstraction-facade — the layers
- adopter-comfyui — a ComfyUI custom node, installable by cloning into custom_nodes
- service-jobd — the supervisor daemon
- addon-synology — the NAS backend, with a DSM 7 .spk
- research — the prior-art corpus
- abstractions — this repo: the charter, the contracts, the backlog

## What is proven and durable

- job, download, storage: multiple implementations passing shared conformance
- find-or-create-by-destination (abstraction-download): a stateless caller can
  now resume its own download. Go only; Python port is still owed.
- one clock/lease correctness fix across all three languages
- delegation to a NAS proven end to end through the Lemonade fork

## The highest-leverage open item

The checkpoint is a single integer (a contiguous prefix), so the library can
only describe a single sequential stream. Every serious downloader fetches in
parallel ranges. See docs/checkpoint-ranges.md — the fix is a checkpoint of
ranges, additive, with the prefix as the degenerate case. This unblocks every
parallel-chunk adopter. Do this next.

## Also open, in rough order

- Port find-or-create to Python (the adopters that need it live there)
- docs/incremental-delivery.md — following a delegate's partial mid-transfer
- The Lemonade upstream PR — the one action that could produce an adopter who is
  not us. See the backlog for why it is not ready yet.
- Register openabstractions.org (gates the Flatpak identity)

## How to work

- One task at a time, committed as it goes. Large fan-outs of parallel agents
  lost in-flight work twice and are not worth it.
- The backlog (docs/backlog.md) holds design notes; five lines each.
- READMEs follow docs/readme-contract.md.

## Deferred deliberately

The permission/identity/service-topology thread is parked. It is real work but
it is not what gets an adopter, and the download track is. Pick it up later, on
its own, when the download layers have a user who is not us.
