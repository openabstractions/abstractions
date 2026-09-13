# Rust shared ABI transport fixture

Run `python run.py --help`, then `python run.py --run --cmake <cmake-executable>`.
Requires already-installed Rust/Cargo, Go, CMake and MSVC. It installs no tools,
uses Cargo offline, and builds the existing C++ client as a static library in a
temporary CMake prefix. The copied Rust crate and outside Cargo consumer link
only that explicit installed prefix. A missing-prefix counterexample must fail.

The native bootstrap agrees with Go's default and preserves an explicit override.
A Go protocol peer tests exact arbitrary bytes, oversized lengths, truncated
headers/bodies, one-way EOF, in-flight cancellation and deadline expiry. An absent
endpoint returns an error. Unit checks refuse invalid endpoint/oversized outbound
data, expired waits and pre-signalled cancellation. No Rust capability JSON is
handwritten; these tests prove framing and native lifetime behavior only.

Peers use unique local endpoints and bounded context shutdown. A peer that has
already exited can close stdin before cleanup flushes; cleanup still waits and
checks its exit. Test process termination on timeout targets only its retained
subprocess object. No installed runtime, OS registration, owner data, Mac or
external network service is contacted. Temporary source, build and client files
are removed after the run; the empty application directory must stay empty.
