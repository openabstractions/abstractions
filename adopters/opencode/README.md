# Use OpenAbstractions inference from OpenCode

Chat from OpenCode with any model the local OA runtime is allowed to serve. An
operator can move future calls between Ollama, Lemonade or a hosted provider
without changing OpenCode's model-facing API. Hosted keys stay in the OA
credential holder, and revocation stops the next request before provider I/O.

This development provider keeps OpenCode's normal provider and model API. OA
handles model routing, program rights, named credentials, streaming,
cancellation and provider substitution through `abstraction.inference/chat@1`.

OpenCode's Bun process loads the shared OA C ABI through `bun:ffi`. The runtime
therefore observes the OpenCode executable as the caller and applies the rights
granted to that exact program.

The [OpenAbstractions MCP gateway](../../mcp-gateway/README.md) is a second,
separate integration path: OpenCode reaches OA capabilities as MCP tools over
a local server process instead of through this native provider.

## Requirements

- OpenCode 1.18.31, the version used by the controlled integration proof.
- A running development OA runtime with at least one model server.
- The shared C ABI library. OpenCode's Bun runtime loads it through `bun:ffi`;
  this path does not load the Node addon. `@openabstractions/ipc` resolves it
  from its platform package, and `ABSTRACTION_IPC_LIBRARY` overrides that with
  an absolute path during development.
- The `@openabstractions/facade`, `@openabstractions/inference` and
  `@openabstractions/ipc` packages, named as dependencies of this package and
  copied beside this provider when it runs from source.
- An installed runtime the shared library selects, or an explicit controlled
  runtime endpoint in `runtimeEndpoint`.

This package is `@openabstractions/opencode`. It is not on npm: publication
needs the owner's npm organization and consent. Until then it is installed from
a tarball the release packaging job produces, or run from source by file URL.

## Configure a local model

Write the provider block from the models the router already lists, so nobody
types a model name into OpenCode's config by hand:

```console
openabstractions opencode configure
```

This reads the router's servable chat families through the runtime and writes
or updates only the `provider.openabstractions` block, and a default `model`
key when OpenCode's config names none, in OpenCode's global config
(`~/.config/opencode/opencode.json` by default; `--config` names another
file). It refuses a `.jsonc` file by name, since it cannot preserve comments;
`--dry-run` prints the block instead of writing it, and a machine with no
servable chat model writes nothing and says why. Run
`openabstractions opencode configure --help` for every flag, including
`--provider-id` and `--scope local|remote`.

Each model's `limit.context` is the host's own reported context window (LM
Studio's `max_context_length`, Ollama's `model_info` context length,
Lemonade's `max_context_window`) when the router reports one, and the
documented 32768 default otherwise.

Naming the package and a model by hand is the same shape configure writes.
Omitting `runtimeEndpoint` reaches the installed runtime through the shared
library's own selection.

The provider reads two option fields on every call. `scope` (`"local"` or
`"remote"`) selects which OA execution binding it asks for and is read
directly on each request; it defaults to `"local"` when omitted. `guarantees`
is the explicit request-guarantee list OA checks before running the call;
when omitted, the provider derives it from `scope` (`abstraction.inference/
local-only@1` for `"local"`, `abstraction.inference/hosted-allowed@1` for
`"remote"`), so a configuration naming only `scope` still works:

Point OpenCode at this module's absolute file URL and name an explicit
controlled endpoint:

```json
{
  "provider": {
    "openabstractions": {
      "name": "OpenAbstractions",
      "npm": "file:///absolute/path/to/adopters/opencode/index.js",
      "options": {
        "runtimeEndpoint": "an explicit controlled OA runtime endpoint",
        "scope": "local",
        "guarantees": ["abstraction.inference/local-only@1"]
      },
      "models": {
        "local/chat": {
          "name": "OA local chat",
          "limit": {"context": 32768, "output": 4096},
          "tool_call": true
        }
      }
    }
  },
  "model": "openabstractions/local/chat"
}
```

Once the package is on npm, `"npm"` names `@openabstractions/opencode` instead
of the file URL, and a fixed install can drop `runtimeEndpoint` and rely on the
shared library's own runtime selection:

```json
{
  "provider": {
    "openabstractions": {
      "name": "OpenAbstractions",
      "npm": "@openabstractions/opencode",
      "options": {
        "scope": "local",
        "guarantees": ["abstraction.inference/local-only@1"]
      },
      "models": {
        "local/chat": {
          "name": "OA local chat",
          "limit": {"context": 32768, "output": 4096},
          "tool_call": true
        }
      }
    }
  },
  "model": "openabstractions/local/chat"
}
```

Grant the OpenCode executable access to the selected OA host:

```console
openabstractions rights grant --for inference \
  --program /absolute/path/to/opencode \
  --host <oa-host-name>
```

OpenCode conversations then use the same OA provider API for text, tool calls
and tool results. Cancelling an AI SDK stream closes the OA iterator and sends
Cancel for an accepted operation that has not reached a terminal delta.

## Use a hosted credential

Register the value through the real Holder API. The value enters on standard
input and is granted to the exact OpenCode executable:

```console
openabstractions credentials add openrouter \
  --target openrouter.ai \
  --for abstraction.inference/chat@1 \
  --for abstraction.router/router@1 \
  --use-by /absolute/path/to/opencode \
  --from-stdin
```

Declare the host and grant inference rights:

```console
openabstractions inference server add openrouter \
  --base https://openrouter.ai/api/v1 \
  --api openai-compatible \
  --credential openrouter
openabstractions rights grant --for inference \
  --program /absolute/path/to/opencode \
  --host openrouter \
  --credential openrouter
```

The OpenCode options retain the name alone:

```json
{
  "scope": "remote",
  "guarantees": ["abstraction.inference/hosted-allowed@1"],
  "credential": "openrouter"
}
```

The runtime checks the OpenCode program's complete right. Its Applier then
checks that program's credential right, consumer contract and target before
adding the provider header. `openabstractions credentials revoke openrouter`
destroys the stored value and stops later OpenCode calls before hosted I/O.

Keep credential values out of OpenCode configuration, prompts, MCP arguments,
model input and command arguments.

## What is verified

Controlled integration tests use the official portable OpenCode 1.18.31 binary
with isolated config, data, cache and state directories. They cover:

- a real OpenCode conversation through native OA IPC;
- tool-call and tool-result forwarding;
- typed refusal from a provider that cannot serve tools;
- cancellation reaching the accepted provider, through a separate Node
  consumer and, through OpenCode's own HTTP session API, with the cancelled
  call attributed to the OpenCode executable itself
  (`research/adoption/opencode-cancel-2026-09-22.md`,
  `TestOpenCodeServerAPICancelsMidStreamAndAttributesToOpenCode`);
- provider substitution with unchanged OpenCode configuration;
- named credential application by the real Holder and Applier;
- revocation preventing later hosted requests;
- original OpenCode executable attribution in inference and credential audits;
- absence of credential values from OpenCode results, audits and runtime logs.

The provider fixtures and hosted endpoint run locally. These tests establish no
paid-provider interoperability result.

## Current limits

- An unresolvable shared library stops the request before IPC; Bun never falls
  back to the Node addon. A relative `ABSTRACTION_IPC_LIBRARY` is refused, and a
  platform with no published package is refused by name.
- The Bun connector selects the installed runtime and enforces a configured
  server expectation through the shared library. Bun 1.4.2 ran selection, the
  endpoint query and both an accepted and an Untrusted-refused Describe
  against an isolated runtime under
  [`serve/bun_native_binding_test.go`](../../serve/bun_native_binding_test.go)
  on Windows and Linux. On Linux, no installed product meant `selectRuntime`
  measured the `Status.Untrusted` refusal branch instead. macOS local sockets
  prove a non-installed, ad-hoc-signed test binary's identity only at the
  process-ID level; the gated test's own precondition needs more and skips
  before any Bun process starts there. XPC, which does carry macOS
  code-signature identity, remains unexercised. Full evidence:
  `research/adoption/opencode-bun-binding/RESULTS.md`, a private research note
  held in this project's own tree.
- No package is on npm. The release packaging job packs the tarballs and
  retains them as artifacts; publication awaits the owner's npm organization
  and consent.
- Platform packages exist for `win32-x64`, `linux-x64`, `darwin-x64` and
  `darwin-arm64`. Assembly and installation from the packed tarballs ran on
  Windows x64; Linux and macOS assembly is unrecorded, and no release workflow
  run has produced these packages yet.
- Native program attribution distinguishes executable identities. Multiple
  OpenCode sessions using the same executable share that program principal.
- A `not_permitted` refusal whose reason is a pending rights question
  (`rights:...`) reads to OpenCode as one sentence: the program is not yet
  permitted, a question is waiting in the Abstraction Panel, allow it there
  and retry. `OAInferenceError`'s typed `outcome` and `reason` still carry
  the original values for code that wants them.

The development tests stage the provider and generated packages into a
temporary package tree. Set `ABSTRACTION_IPC_LIBRARY` to the absolute path of
the built shared library for OpenCode when running from source.
`ABSTRACTION_IPC_NODE` is used by the separate Node cancellation helper and
Node consumers. The public [coverage
page](https://openabstractions.org/coverage.html) records current checks; the
limits above describe this development integration.
