# Installed C++ rights enforcement at history, model and router

Run `python conformance/clients/resource_rights/run.py --help`, then `--run`
with `--cmake` naming a CMake that has a C++ toolchain (add `--race` for the Go
race detector). The runner installs a temporary aggregate facade SDK, builds an
outside consumer through `find_package(abstraction_facade)` and runs the Go
fixture.

The fixture hosts the generated rights authorization service as its own host
and a runtime whose logging history, model lookup and router services each use
the runtime's rights policy helpers. The consumer resolves the history reader,
model resolver and router through the aggregate `Machine` and checks four
stages:

- `denied`: history `forbidden`, model `forbidden`, router inventory and routing
  `forbidden`, before any grant;
- `granted`: after exact rules for `abstraction.logging/history.read`,
  `abstraction.model/lookup` on registry `fixture`,
  `abstraction.router/inventory.read` and `abstraction.router/route` on model
  `qwen2.5`, every call succeeds;
- `revoked`: every call is refused again;
- `outage`: with the rights service stopped, history and router report
  `policy_unavailable` and model lookup reports `unavailable`.

The native subject granted is the consumer executable observed by the history
service. No owner service, registry or installed runtime is used.
