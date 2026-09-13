# C++ downstream recovery requirement

The application links installed `abstraction::facade_jobs` and
`abstraction::download_request` targets. It submits generated request bytes with
`kDownstreamRecoveryGuarantees[0]` in the existing required-guarantees field.
It receives only a runtime endpoint and caller-owned receipt identity. The Go
fixture owns its temporary provider store and downstream adapter configuration.

The fixture runs production runtime admission, `DelegatedExecution`, runner
reconciliation and result delivery. Its external adapter deliberately accepts a
Start while losing the response. A stopped and reopened runtime must locate that
same external operation. A new C++ process reconciles its receipt and copies the
result. Exactly one Start and one delivery are required. A provider unable to
honor the execution guarantee refuses resolution/admission. A configured
recovery executor with an incapable downstream adapter retains pending work and
reports refusal before any Start.

The recoverable case uses an actual loopback TLS backend with a service-only
endpoint, trusted test certificate and random bearer credential. A remote Start
commits its effect and closes the socket without a reply. The service observes an
unknown Locate response before shutdown; a fresh service transport then recovers
the same external ID and reads the result over the network. Requests without the
credential are refused. Application source and IPC API remain unchanged. This
controlled backend protocol is a test adapter, with no NAS or installed service.

Use an existing installed prefix containing `abstraction_facade`,
`abstraction_ipc`, `abstraction_job_acceptance` and
`abstraction_download_request`. Request-only download installation uses
`ABSTRACTION_DOWNLOAD_BUILD_LEGACY=OFF`; facade jobs-only installation uses
`ABSTRACTION_FACADE_BUILD_AGGREGATE=OFF`. No provider library is linked into C++.

```
python conformance/clients/delegation/run.py --help
python conformance/clients/delegation/run.py --run --prefix .build/cx/prefix --race
```

Pass `--cmake` when CMake is outside PATH. The race option requires a working Go
race C compiler. The runner uses the shared conformance workspace locator and
never clones sources or installs tools. Package preparation is separate from
fixture execution. The Windows run uses private named pipes; no installed
service or user configuration is changed. macOS currently cannot supply the
required Program proof and this runner refuses that conformance claim.

## Local package provenance

The measured prefix was `.build/cx/prefix` in the private source worktree.
It was prepared from facade jobs-only sources and the request-only download
package, using the existing `.build/cx/facade` and `.build/cx/request` CMake
caches. These commands reproduce those configurations and refresh installed
headers from the current source (substitute the full CMake path as needed):

```sh
cmake -S openabstractions-flat/abstraction-facade/cpp -B .build/cx/facade -DABSTRACTION_FACADE_BUILD_AGGREGATE=OFF -DABSTRACTION_FACADE_BUILD_JOBS=ON -DABSTRACTION_JOB_BUILD_LEGACY=OFF
cmake --build .build/cx/facade --config Release --parallel 4
cmake --install .build/cx/facade --config Release --prefix .build/cx/prefix
cmake -S openabstractions-flat/abstraction-download/cpp -B .build/cx/request -DABSTRACTION_DOWNLOAD_BUILD_LEGACY=OFF
cmake --install .build/cx/request --config Release --prefix .build/cx/prefix
```

The Windows caches use Visual Studio 18 2026, MSVC 19.51.36256.0 and CMake
4.3.1-msvc1. The request package was refreshed after schema `0321800d`, whose
installed header supplies `kDownstreamRecoveryGuarantees`. The fixture used
production commits `6999396`, `ad2f791a`, `d046dd2b` and `cb4faea0` atop main
`601581f5` (equivalent cherry-picks in the agent worktree). No public package
version or release was used to establish this new guarantee. A caller-supplied
`--prefix` is the caller's package provenance responsibility.
