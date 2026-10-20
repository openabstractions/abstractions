# Where this stands

Updated 2026-09-29. OpenAbstractions 0.3.0 is available from
[the runtime releases](https://github.com/openabstractions/redist/releases/tag/v0.3.0).
The [installation guide](https://openabstractions.org/adopt.html) describes the
packages and setup. Package signing and platform evidence retain their stated
limits in the release notes and coverage pages.

## Architecture and contracts

Applications use capability interfaces through their language clients. The
local OA runtime owns service state, provider selection, permissions and
communication with remote providers. Providers implement the capabilities;
applications can adopt each capability independently.

Each layer's contract page defines behavioral rules and names the Thrift
definitions it binds. Those definitions supply shared API vocabulary and
generated language interfaces; service definitions also supply IPC bindings.
Conformance scenarios and implementation tests exercise the stated guarantees.
Read the selected layer's contract and versioned API together. Conflicts are
defects to resolve, and a generated binding alone proves no runtime behavior.

For example, [job's contract](https://github.com/openabstractions/abstraction-job/blob/main/CONTRACT.md)
binds `acceptance.thrift` for service acceptance, observation and control.
Its separate `job.thrift` describes the legacy store record. This distinction
replaces this page's earlier unresolved comparison of those definitions.

## Integrations and evidence

The [reference](https://openabstractions.org/reference.html),
[catalogue](https://openabstractions.org/catalogue.html) and
[coverage](https://openabstractions.org/coverage.html) describe capability scope
and evidence. This repository carries the [OpenCode adapter](adopters/opencode/README.md),
[MCP gateway](mcp-gateway/README.md) and
[Comfy interaction example](adopters/comfyui-presentation/README.md).
[The ComfyUI integration](https://github.com/openabstractions/adopter-comfyui)
also demonstrates service-owned model acquisition and observation.

Consult each integration's README for its tested app version, setup and
distribution status. A fixture proof, a real-app proof and an installed package
are separate evidence. Modelhost and modelbridge have tagged Go sources;
their provider-specific documentation describes remaining qualification.

## Versions

Runtime packages use redist release versions. Language modules have their own
tags and declared dependencies; use the selected package's installation guide
and dependency metadata. `layers.lock` retains an early eight-repository
snapshot. It is historical evidence and is not the dependency set for 0.3.0.
Its former `scripts/layers.sh` helper is no longer published here.
