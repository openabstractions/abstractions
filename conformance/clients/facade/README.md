# Installed C++ facade through three Go services

Run `python conformance/clients/facade/run.py --help`, then `--run` on Windows.
No arguments prints help. `--toolchain` prints CMake's version without building.
Set `CMAKE` if CMake is not on PATH or in the standard Visual Studio 18 location.

The runner uses the shared `../workspace.py` locator in the private source tree
or sibling public abstraction checkouts. Go and CMake must be available; Go
dependencies must already be cached unless the caller deliberately supplies a
different GOPROXY. Source, staging, consumer and fixture files stay in the short
`.build/facade` directory. No OS installer or global CMake package is installed.

A separately built C++ consumer uses only `find_package(abstraction_facade)`
and its installed transitive packages. Through `facade::Discover()` it calls
Log, Config.Read, and Router.Models/Hosts/Pick. Three Go fixture processes each
compose the corresponding production service and provider on a unique pipe:

- Logging writes the service-owned file and records the kernel-observed client.
- Configuration reads the service's isolated user file, returns provenance and
  notices an owner change across two independently launched facade clients.
- Router uses an explicitly empty provider: empty inventory and typed negative
  decisions are valid responses, distinct from unavailable-service errors.
  Its audit records the client; an unknown method is refused before the provider.

The harness observes the logging output while the C++ process waits, then
allows it to exit and verifies the record persists. This fixture barrier is not
a logging persistence acknowledgment. Router identity descriptions are compared
as diagnostics, never used as authorization or stable identity keys.

After stopping all services, every facade operation must fail. The client home
must remain empty, and provider-owned files must remain unchanged. No actual
model-host discovery, GPU probe, external network or C++ server is used.
`windows.json` describes this focused service-client evidence; record it only
after its source inputs have been committed.
