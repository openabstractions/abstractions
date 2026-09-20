# OpenAbstractions MCP gateway (development proof)

Give an MCP assistant a small, reviewable set of OA abilities: see the models and
applications it may know about, hold a bounded model conversation, and submit,
recover or cancel long-running image and video work. The assistant receives no
provider key, private application path or general OA administration surface.

The gateway runs locally over stdio and exposes six tools backed by the installed
OpenAbstractions runtime:

| Tool | OA service | Bound |
|---|---|---|
| `oa_applications_list` | `abstraction.facade/applications@1` | permission-filtered local names, titles, instances, interfaces and contexts; 262,144 returned bytes |
| `oa_models_list` | `abstraction.router/router@1` | servable names only |
| `oa_inference_complete` | `abstraction.inference/chat@1` | explicit route, 65,536 input bytes, 4,096 output tokens and 262,144 returned bytes |
| `oa_inference_job_submit` | `abstraction.job/acceptance@1` plus the inference job document | stable request key, hosted image/video request, immutable receipt identity |
| `oa_inference_job_status` | `abstraction.job/operations@1` | one opaque handle, result capped at 65,536 bytes |
| `oa_inference_job_cancel` | `abstraction.job/acceptance@1` | explicit cancellation intent for one handle |

The gateway advertises tools only. It has no roots, resources, prompts,
sampling, elicitation, subscriptions, MCP Apps, remote transport or arbitrary
callback surface. The server accepts MCP `2026-07-28` and `2025-11-25` through
the official Go SDK v1.8.0. The adapter version, MCP protocol version and OA
service contract versions remain separate compatibility facts.

## Authority and durable state

OA observes the gateway executable as the native IPC peer. Operators grant the
gateway executable narrow rights as one integration principal. MCP `clientInfo`,
request `_meta`, parent PID and tool arguments are retained as untrusted protocol
data by the MCP implementation; none selects an OA subject or grants rights.

All OpenCode sessions that launch the same executable intentionally share that
OA integration principal. Two separately authorized principals require two
distinct executable identities that the native OA boundary can distinguish and
separate rights rules for their absolute paths. A second `--state-dir` alone is
an organizational boundary and supplies no OA identity isolation.

The state directory contains random opaque job-handle records with the exact OA
endpoint binding, logical owner, contract, guarantees and request identity. The
records are atomically written and file-synced before submission. Submit requires
a stable caller `request_key`; the same key and immutable specification reconcile
the saved identity, while a changed specification is refused. MCP disconnect
does not cancel accepted work. Cancellation is explicit.

Handle records contain no prompt, result, credential, provider header or secret.
The OA services apply credentials and authorization. Status returns only the
original principal's bounded operation snapshot and typed inference result.
Credential fields are OA credential names, never secret values. Durable
generation currently requires `hosting: "hosted"` and a credential name. Text
inference requires `hosting: "local"`, or hosted plus a credential name. The
server allows eight concurrent calls, gives each a two-minute deadline, and
defaults `max_output` to 1,024 tokens.

Application discovery is read-only. It omits executable paths, start guidance
and activation recipes, and exposes no activation, mutation or generic
application-forwarding tool. OA filters each snapshot for the gateway's native
executable principal; MCP client metadata cannot select another caller.

## Development build and OpenCode configuration

Build the unsigned development command without installing it:

```powershell
go build -o .\out\openabstractions-mcp.exe .
```

Grant only the actions the selected local hosts need to the resulting absolute
executable path. Application discovery needs
`abstraction.facade/application.read` on each exact `app:<name>` that should be
visible. Model inventory needs
`abstraction.router/inventory.read` on `abstraction.router/inventory`. Text and
durable inference need `abstraction.inference/complete` on each selected
`host:<name>`. Durable submission also needs
`abstraction.job/acceptance.submit` on `abstraction.job/acceptance@1`. The
runtime's rights command records exact grants; do not copy the runtime's broad
installation grants to this adapter.

OpenCode 1.18.31 accepts an absolute command and state directory in
`opencode.json` or `opencode.jsonc`:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "openabstractions": {
      "type": "local",
      "command": [
        "C:\\path\\to\\openabstractions-mcp.exe",
        "--state-dir",
        "C:\\path\\to\\oa-mcp-state"
      ],
      "enabled": true
    }
  }
}
```

The opt-in adopter test uses a real OpenCode CLI with isolated XDG config, data,
cache and state directories and local fake model/provider endpoints:

```powershell
$env:OA_OPENCODE_BINARY = 'C:\path\to\opencode.exe'
go test ./gateway -run TestOpenCodeInvokesGatewayInventory -count=1 -v
```

It discovers the gateway, invokes the five inference and model tools, consumes
their structured results, cancels an accepted pending job and surfaces a typed
unknown-handle refusal. It contacts no paid provider. The normal test suite
skips this test when `OA_OPENCODE_BINARY` is absent.

Hosted completion resolves the runtime's remote-execution binding; local
completion resolves its local-execution binding. Both use the same authenticated
native IPC transport to OA. Explicit execution placement is independent of that
local connection. Recovery-file lock waits honor each tool's context.
