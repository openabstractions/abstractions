# Cost of one framed call, per client

Run `python conformance/clients/percall/run.py --help`.

`--measure` starts an isolated runtime (`openabstractions serve runtime
--isolated <name> --state-dir <temp>`) and runs one probe process per client
and mode. Each probe makes identical `abstraction.facade/caller@1` `Observe`
calls at the runtime endpoint and prints a `PERCALL` line with the first call,
p50, p90, p99 and maximum in milliseconds, and the code-signature proof the
runtime reported for the probe. `Observe` is the cheapest call every client can
make: the resolver answers from the connection's own binding.

Clients: Go (`listen.FrameClient`), C++, Python and Rust (the shared
`abstraction_ipc` library), JavaScript through the Node-API addon, and
JavaScript through the test-only `node:net` connector. Each runs with a
connection per call and, except the pipe connector, on a session
(`listen/FRAMING.md` "Sessions").

`--breakdown` times each server and client step of a Go call on a fresh
connection, on one bound connection, and on a session (`breakdown/main.go`).

`--gate` fails when a p50 passes its bound. On Windows, Python and the Node
addon on a connection per call stay under max(10 ms, 3 x the faster of Go and
C++); it was added when each call from a signed interpreter re-ran Authenticode
on the caller's image (32 to 165 ms p50). On a session, Go, C++ and Rust stay
under 0.6 ms (Windows) or 1.5 ms (Linux), and Python and the Node addon under
3 ms.

Inside WSL: `python conformance/clients/wsl_run.py percall --measure --node=`.
Results: `research/inference/MEASURED.txt`, "Per-call cost of a framed call".
