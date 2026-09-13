# Installed Python client over shared C ABI

Run `python run.py --help`, then `python run.py --run --cmake <cmake-executable>`.
Requires Python 3.10+, pip/setuptools, Go, CMake and a native C++ compiler already
installed. Package installation is offline (`--no-index --no-build-isolation`)
into a temporary prefix. It does not install tools or modify OS registration.

IPC, facade and generated logging protocol wheels come from their actual source
packages. The outside consumer runs with Python `-I`, an
explicit installed module prefix and an empty application directory. The native
library is built and installed separately with `BUILD_SHARED_LIBS=ON`.

An isolated production Go runtime resolves logging and accepts an exact Unicode
record with expected attributes through the generated Sink interface. The sink
is memory-backed test output. Unsupported guarantees refuse resolution; absent
runtime, expired deadline, cancellation and oversized reply controls preserve
explicit errors. Fake resolver references exercise wrong contract, guarantees,
scope and transport before provider connection. Native-handle tests cover
release during open, one close after partial-write failure and pre-open bounds.

All hosts use unique local endpoints, stop through context cancellation and have
bounded cleanup. The application directory must remain empty. No installed
runtime, external model provider, OS service, owner job store or Mac is used.
Current evidence is Windows; other platforms require their own run and identity
scope. This proves a first Python client binding, not complete language parity.
