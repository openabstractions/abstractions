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

## Requirements

- OpenCode 1.18.31, the version used by the controlled integration proof.
- A running development OA runtime with at least one inference host.
- The built shared C ABI library and Bun native addon.
- The generated `@openabstractions/facade`, `@openabstractions/inference` and
  `@openabstractions/ipc` packages beside this provider during development.
- An explicit controlled runtime endpoint.

This provider is source for an adopter proof. It is not a published OpenCode
package.

## Configure a local model

Point OpenCode at this module's absolute file URL and name a model served by the
runtime:

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
openabstractions inference host add openrouter \
  --base https://openrouter.ai/api/v1 \
  --wire openai-compatible \
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
- cancellation reaching the accepted provider;
- provider substitution with unchanged OpenCode configuration;
- named credential application by the real Holder and Applier;
- revocation preventing later hosted requests;
- original OpenCode executable attribution in inference and credential audits;
- absence of credential values from OpenCode results, audits and runtime logs.

The provider fixtures and hosted endpoint run locally. These tests establish no
paid-provider interoperability result.

## Current limits

- The native Bun connector requires an explicit controlled runtime endpoint.
- Installed runtime selection and configured server-expectation verification
  return `ProofUnavailable`.
- The packaged dependency layout and release installation remain unfinished.
- Native program attribution distinguishes executable identities. Multiple
  OpenCode sessions using the same executable share that program principal.

The development tests stage the provider, generated packages and addon into a
temporary package tree. Set `ABSTRACTION_IPC_NODE` and
`ABSTRACTION_IPC_LIBRARY` to absolute paths for the built artifacts. The public
[coverage page](https://openabstractions.org/coverage.html) records current
checks; the limits above describe this development integration.
