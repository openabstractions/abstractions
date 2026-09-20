# Maintained production inputs

These files let a public checkout test the generator against real OA contracts:
durable job acceptance, service resolution, structured logging and Rust's shared
frame trait. Their canonical sources live in the corresponding capability
repositories. Publication copies the reviewed definitions here so one charter
checkout can run the production-input tests.

From the public charter root, run `go -C idl/gen test ./...`. Python-backed tests
use the `PYTHON` environment variable, or else `python` on PATH, and it must name
a real interpreter: on Windows the Store `python` alias fails those tests, so set
`PYTHON` (for example `%LOCALAPPDATA%\Programs\Python\Python312\python.exe`) or
put that interpreter first on PATH. Node and a C++ compiler (`CXX`) are optional
and skip their tests when absent. Generator production
tests resolve these inputs here. Missing inputs fail the tests. `LANGUAGE.md`
remains at `idl/LANGUAGE.md` and supplies the documentation rules.

Only toolchain availability retains existing individual test skips. A missing
production definition is never treated as an unavailable compiler.
