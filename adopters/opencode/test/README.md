# Node tests never reach the installed runtime

`npm test` loads `../runtime-isolation.mjs` before any test file, and
`run_node_tests.py --run` gives every test child the same environment: unless
it names a fixture, `ABSTRACTION_RUNTIME_ENDPOINT` names an endpoint no process
serves, and one naming the installed per-account runtime refuses the run.
Several tests build the plugin with partial fakes, so any client they leave
unfaked resolves that endpoint. On 2026-11-01 a run without it reached the
owner's installed runtime and raised a real first-use question for node.exe.
Run a single file as `node --import ./runtime-isolation.mjs --test <file>`;
`test/runtime-isolation.test.mjs` fails a run started without the preload.

# Black-box Go harnesses

Run from the repository root with `GOWORK=off`, using the existing gateway
module and its declared released dependencies:

```text
go -C mcp-gateway test -count=1 ../adopters/opencode/test/opencode_gateway_test.go ../adopters/opencode/test/opencode_source_share_test.go ../adopters/opencode/test/opencode_native_adopter_test.go ../adopters/opencode/test/opencode_panel_credentials_test.go ../adopters/opencode/test/opencode_credentials_adopter_test.go ../adopters/opencode/test/opencode_chat_policy_adopter_test.go
```

The six explicit files form one test package. They use public service clients,
the shipped runtime/Panel command lines and the gateway's shared test fixtures.
Pre-release projection checks use the normal staged module proxy built from the
same projected trees. No private `go.work` or sibling commit pin is required.

Real adopter receiving is opt-in: `OA_OPENCODE_BINARY` names a separately
verified OpenCode executable; source-watch uses `OA_SOURCE_WATCH_BUN_EXECUTABLE`
and `ABSTRACTION_IPC_LIBRARY`. Native provider cases also use `OA_IPC_LIBRARY`
and, where needed, `OA_IPC_NODE`. The tests can build and start owned runtime,
provider and Panel fixtures. Prepare their isolated inputs and process lifetime
before running them. Missing input skips and compile-only checks provide no real
adopter receiving. Add `-c -o <owned-test-binary>` to compile without running it.

An offline real run can supply already verified bootstrap trees through
`OA_OPENCODE_BOOTSTRAP_CONFIG_SOURCE` and
`OA_OPENCODE_BOOTSTRAP_CACHE_SOURCE`. These name the contents of the prepared
`opencode` config and cache directories. The fixture copies them into its own
XDG directories, including installed modules, and refuses linked inputs.
Preparation owns their provenance and pins; this helper downloads nothing.

For the vendor busy wake fixture, narrow the same Go command with
`-run '^TestOpenCodeVendorBusyWake$'` before the file list. Use the verified
`OA_OPENCODE_BINARY`, `OA_OPENCODE_BOOTSTRAP_CONFIG_SOURCE`, and
`OA_OPENCODE_BOOTSTRAP_CACHE_SOURCE` in an isolated
runner with external network access disabled and loopback enabled. The config
bootstrap holds the verified package manifest, lockfile, and installed plugin
closure for OpenCode 1.18.34. The staged
adopter also needs the existing locked Node dependencies. This case starts
OpenCode with fixture-owned config and a local model stub, and uses an inert
watch peer. It checks the actual next model request and the request following
the inbox read.
