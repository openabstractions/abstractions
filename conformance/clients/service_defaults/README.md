# Default-client shared-state migration proof

No arguments prints help:

```sh
python conformance/clients/service_defaults/run.py --help
python conformance/clients/service_defaults/run.py --run --cmake /absolute/path/to/cmake
```

The runner builds the current shared native client library, installs four actual
Python source packages into a temporary prefix offline, and runs isolated Go
runtime fixtures with an outside Python process. It requires existing Go,
Python/setuptools/pip and CMake/compiler installations. It removes the fixture
prefix on success or failure. It does not install an OS service or package.

The Go handler test sets both legacy logging endpoints, starts with no resolver,
and requires an error with no file creation. The fixture then supplies its known
process principal and executable through NewResolvedHandler; it does not register
an OS installation. An actual service accepts the record through that verified
resolver; removing the resolver selection does not change the retained binding.
Cancellation and typed not_ready remain errors. The Python application receives
only an installed package prefix and runtime bootstrap. It reads existing config,
applies a revision, sees stale-write conflict and changed values, and refuses
an absent resolver. Its isolated home remains empty. Provider setup and private
record verification occur only in the Go fixture.

For a quick Go-only check:

```sh
go test -count=1 conformance/clients/service_defaults/defaults_test.go -run TestLoggingDefault
```

Python/native execution is enabled by the runner, and is explicitly skipped by
a Go-only invocation. Native macOS Program proof remains unavailable; this is
currently a Windows/compatible Linux fixture, with Windows evidence recorded in
feedback/shared-state-migration-acceptance.md. Existing C++ installed facade and
Go dependency graph checks remain in ../facade; this fixture does not rebuild
those unrelated packages.
