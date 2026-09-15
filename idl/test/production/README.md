# Maintained production inputs

The public charter publishes the acceptance, facade and logging definitions plus
Rust's shared framing trait here through scripts/split.manifest in the maintainer
source. The files come directly from their capability sources during publication;
do not maintain edited copies in this test directory.

From the public charter root, run `go -C idl/gen test ./...`. Python-backed tests
use the `PYTHON` environment variable, or else `python` on PATH, and it must name
a real interpreter: on Windows the Store `python` alias fails those tests, so set
`PYTHON` (for example `%LOCALAPPDATA%\Programs\Python\Python312\python.exe`) or
put that interpreter first on PATH. Node and a C++ compiler (`CXX`) are optional
and skip their tests when absent. Generator production
tests resolve these inputs here. In the private development tree they resolve the
same inputs under openabstractions-flat instead. Missing inputs fail the tests.
LANGUAGE.md remains at idl/LANGUAGE.md and supplies the documentation rules.

Only toolchain availability retains existing individual test skips. A missing
production definition is never treated as an unavailable compiler.
