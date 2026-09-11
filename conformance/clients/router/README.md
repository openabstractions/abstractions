# Router framed-client proof

Run `python run.py --help`, then `python run.py --run` on Windows with Go,
CMake and MSVC available. The shared workspace helper supports the private flat
layout and public sibling checkouts. The runner downloads nothing and stages
CMake packages under `.build/router`; it installs no OS service.

The test builds the real Go service with the existing router provider, feeding
it isolated fake HTTP model hosts. An independently configured C++17 consumer
uses `find_package(abstraction_router)` against installed headers/runtime.
It checks Models/Hosts/Pick, resident and permitted cold choices, host errors,
server-observed caller attribution, audit, absent-versus-empty host allowance,
empty arrays, malformed requests and typed refusal codes. The HTTP trace permits
GET only. After client exit the host remains alive; after host exit a client
fails and its isolated home remains empty.

This is a Windows client-to-service proof of the named subset. It does not prove
GPU cost, the legacy HTTP window, every platform or OS service installation.
