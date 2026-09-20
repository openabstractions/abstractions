# Inference chat@1, resolved through the runtime

Run `python conformance/clients/inference/run.py --help`.

`--unit` runs the inference Go module tests (the provider against a fake
upstream with a request log, and the service over framed IPC) and the Rust
facade crate's fold and page tests.

`--run` composes the facade runtime with the production inference provider and
service over two fake local model hosts: `ollama` lists `fixture-chat:1b` and
streams a reply one word per event, and `lmstudio` lists `denied-chat`. The
provider decides `abstraction.inference/complete` through a rights decision
policy. A second runtime publishes no inference.

Each application runs twice:

1. **refused.** Resolving chat@1 on the runtime without inference fails with
   status `unavailable`. `complete()` before any rule reads `not_permitted`
   with reason `rights:not_granted`. The fixture then grants `complete` on
   `host:ollama` to the one program the service bound during that run.
2. **served.** `complete()` returns the folded reply from `ollama` with its
   usage. `stream()` yields the parts and the end delta, and the time to the
   first part is printed as `FIRST_TOKEN_MS <language> <ms>`. A stream the
   application leaves after its first part cancels the operation, and the
   fixture requires the held upstream request to close. `complete()` on
   `denied-chat` reads `not_permitted`.

The Go client runs in the fixture process. The applications are an installed
C++ consumer (`find_package(abstraction_facade)` and
`find_package(abstraction_inference_api)`, `resolve_inference`), a JavaScript
consumer (`resolveInference` over the test-only pipe connector), a Python
consumer (`Machine.resolve_inference` over the shared `abstraction_ipc`
library) and a Rust consumer (`InferenceMachine` over
`abstraction-facade-native`). `--no-cpp`, `--node ""`, `--no-python` and
`--no-rust` skip one. `--latency FILE` appends the first-token lines with the
platform.

When the C++ or Python consumer runs, `--run` then builds `openabstractions`
from `serve/` and starts it as an isolated runtime with `--gateway` on a free
loopback port and a local `ollama` host over the fake upstream
(`audit_test.go`). The built command line grants `audit.read` to the fixture,
the C++ consumer and Python, grants Python `complete` on `host:ollama` and
issues Python a local key. The fixture calls chat@1 natively without a rule
(an audit entry with route `native`), and the llm-style Python client from
`abstraction-inference/go/gateway/testdata` streams through the window (route
`window`, rung `tcp-loopback/<platform> ...`). The Go operator client, the C++
consumer (`inference_consumer <runtime> audit`) and the Python consumer
(`py_consumer.py <runtime> audit`) then print the whole audit, and the three
outputs must be identical.

`--window` runs the loopback socket-owner binding tests of
`abstraction-identity` (including the handle-passing attack) and the gateway
window tests (llm-style, aider-style and Anthropic-style fake Python clients,
unbound and wrong-program peers refused before the body) with `-race`. From
Windows, `python conformance/clients/wsl_run.py inference --window` runs the
Linux binding path.

On Windows the C++ proof certifies MSVC: run the runner from a `.bat` that calls
`vcvars64.bat` first. From Windows, `python conformance/clients/wsl_run.py
inference --run --node ""` runs the Linux leg.

`--measure` runs `latency_test.go`: the production provider and service on
this host's native IPC with the Go client in the same process, over a fake
emitter that flushes 50 tokens at 20, 5, 2 and 0 ms. It prints `LATENCY` lines
for Start, first token, token delivery after each flush, one Observe round
trip and pages per token, and `RTT` lines from the JavaScript and Python
per-call probes (`js_rtt.mjs`, `py_rtt.py`). A reachable local Ollama with a
model replaces the emitter. `--latency FILE` appends the lines. The recorded
measurement is `research/inference/MEASURED.txt`.

The fixture composes the runtime in its own process rather than starting
`openabstractions serve runtime`: the rights rules must name each application's
bound program, and the installed runtime has no command that sets an inference
rule yet. `serve` `TestRuntimeInferenceAppliesAHostedCredential` covers the
installed composition with a hosted credential.
