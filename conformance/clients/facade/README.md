# Installed C++ facade through resolution and three Go services

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
Log, Config.Read, ConfigEditor.ReadUser/ReplaceUser, and Router.Models/Hosts/Pick. The client receives only a runtime
bootstrap address. A production Go resolver selects registered provider endpoints;
the conventional capability endpoint overrides initially name unavailable pipes.
Three Go fixture processes each
compose the corresponding production service and provider on a unique pipe:

- Logging writes the service-owned file and records the kernel-observed client.
- Configuration reads the service's isolated user file, returns provenance and
  notices an owner change across two independently launched facade clients.
  The resolved editor applies a revision-checked update, refuses a stale update,
  agrees with the reader and restores the fixture through the same API.
- Router uses an explicitly empty provider: empty inventory and typed negative
  decisions are valid responses, distinct from unavailable-service errors.
  Its audit records the client. Protocol-level malformed-method checks belong to
  the router tests; this application uses the typed facade throughout.

The harness observes the logging output while the C++ process waits, then
allows it to exit and verifies the record persists. This fixture barrier is not
a logging persistence acknowledgment. Router identity descriptions are compared
as diagnostics, never used as authorization or stable identity keys.

After stopping only the resolver, conventional overrides are pointed at the still
running providers. Every facade operation must fail. After stopping all services,
the absence checks run again. The client home
must remain empty, and provider-owned files must remain unchanged. No actual
model-host discovery, GPU probe, external network or C++ server is used.
`windows.json` describes this focused service-client evidence; record it only
after its source inputs have been committed.

`python conformance/clients/facade/dependencies.py --check` checks the actual Go
dependency closure of the primary facade and `/client`. It refuses provider
implementations and entry into the explicit legacy package. Run
`python conformance/clients/facade/test_dependencies.py` for local Go-module
counterexamples, including an indirect provider import. The normal gate runs both.

## Explicit adoption wrapper

`go test -race -count=1 -v conformance/clients/facade/adoption_test.go` runs the
focused Go adoption proof from the workspace root. It supplies the existing
HTTPExecution engine through generated acceptance/operation dispatchers and
clients, with no local IPC service. Application helpers receive generated
interfaces; fixture composition owns provider directories and engine lifecycle.

The wrapper rejects an unsupported downstream-recovery promise before issuing
HTTP. A supported operation remains accepted by its original owner while an
isolated production runtime and resolver become available. A new binding selects
that service for future work through JobsClient.WithContext, retaining facade
owner and guarantee validation behind the generated interfaces. Both results reproduce loopback HTTP bytes, with
exactly one HTTP request per accepted operation and an unchanged original receipt.
The test uses temporary data and endpoints. This exercises provider availability,
not package installation. Current macOS service identity cannot supply the
required Program proof, so that platform is explicitly excluded from this fixture.
