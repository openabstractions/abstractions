# MCP Apps comparison host

Compare two ways an assistant can show the same proposed ComfyUI edit: an
ordinary MCP result and a visual MCP Apps card. The card asks a plain question
such as “Change KSampler node 2's steps from 20 to 24?” and says that previewing
has changed nothing. Reveal and Apply stay in ComfyUI, where the person can see
the real workflow.

This development adapter presents the existing `comfy_preview_parameter` result
without creating another proposal or state store. It launches the ordinary Go
server over stdio, calls its tool, and returns the same `CallToolResult`. The
connected ComfyUI browser remains the proposal owner.

The view renders the exact instance, context, revision, node, widget, before
value and proposed value. Fixture responses may also carry an opaque proposal
token. The caller supplies the first
three values unchanged from the ordinary server's immediately preceding
`comfy_read_context` result, which is available through the same adapter. The
adapter also preserves the optional OA `application_instance` supplied by that
context result and never synthesizes a binding. The view keeps those opaque
fields and any proposal token in collapsed Technical details. OA responses omit
the application-local effect handle. It has no button or App-to-server
tool call. Reveal and apply remain explicit actions in the connected ComfyUI
panel. The adapter enforces 16 KiB request-body and result bounds, a cap of four
active requests, a 4 KiB credential-file bound and a 64-event negotiation-history bound.
It binds HTTP to loopback and rejects each present Origin other than the exact
configured loopback host Origin. Requests without an Origin are accepted for
the reference host's server-side proxy and native MCP clients; this development
fixture treats those local callers as trusted.

## Pinned reference host

The deterministic host test proves Apps negotiation, resource association and
card rendering against a checked-in result. A live credential-backed comparison
with the ComfyUI bridge was not run because the authorized Node launch was
blocked before process start. The experiment makes no human-comprehension claim.

The comparison uses the official
[`modelcontextprotocol/ext-apps`](https://github.com/modelcontextprotocol/ext-apps)
`examples/basic-host` at tag `v2.0.0`, commit
`352f6ced4d80772e92b4e7a311854481a8d65b04`. The corresponding stable Apps
specification is `2026-01-26`. SDK packages are pinned to `2.0.0`; Node 20 or
newer is required.

The tagged basic host renders Apps but omits the required
`io.modelcontextprotocol/ui` client capability from its initialize request.
[official-basic-host-v2.0.0.patch](official-basic-host-v2.0.0.patch) adds the
explicit `text/html;profile=mcp-app` capability. It also lets Node execute the
host's TypeScript server from a path containing spaces and serves the sandbox
from bounded in-memory bytes. This is a recorded local patch to pinned upstream
source; it is not an unmodified-host result.

Prepare the reference host in ignored scratch state:

```powershell
git clone --branch v2.0.0 --depth 1 https://github.com/modelcontextprotocol/ext-apps.git .forks/ext-apps-v2.0.0
git -C .forks/ext-apps-v2.0.0 apply --unidiff-zero ../../adopters/comfyui-presentation/apps-host/official-basic-host-v2.0.0.patch
npm --prefix .forks/ext-apps-v2.0.0 ci
npm --prefix .forks/ext-apps-v2.0.0 run --workspace examples/basic-host build
```

The official repository's `npm ci` installs an npm-local `bun` package for its
own build scripts. The host server in this comparison runs on Node directly;
there is no global Bun installation or runtime dependency:

```powershell
$env:SERVERS='["http://127.0.0.1:3001/mcp"]'
$env:HOST_PORT='8080'
$env:SANDBOX_PORT='8081'
node --experimental-strip-types .forks/ext-apps-v2.0.0/examples/basic-host/serve.ts
```

## Adapter run

Install and build in this directory. This installs no global packages:

```powershell
npm ci
npm run build
npm test
```

The deterministic reference-host check uses the checked-in fake ordinary
server, patched pinned host and a system Edge executable. It starts all servers
inside one foreground process and closes them after the assertion:

```powershell
$env:OA_EXT_APPS_CHECKOUT = (Resolve-Path .forks/ext-apps-v2.0.0)
$env:OA_BROWSER_EXECUTABLE = 'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe'
$env:OA_APPS_SCREENSHOT = (Join-Path (Get-Location) 'adopters\comfyui-presentation\apps-host\evidence\reference-host-render.png')
$env:OA_APPS_CARD_SCREENSHOT = (Join-Path (Get-Location) 'adopters\comfyui-presentation\apps-host\evidence\proposal-card.png')
npm run test:reference-host
```

This check uses no ComfyUI instance or credential. It proves negotiation,
resource association and inner-view rendering with a deterministic record.

Build the existing ordinary server separately. Start the adapter with the
operator-created per-run credential file. The token is read into the child
environment and is never printed or placed in an MCP argument or result:

```powershell
npm start -- `
  --ordinary-command C:\path\to\comfy-presentation-server.exe `
  --ordinary-arg --comfy-url `
  --ordinary-arg http://127.0.0.1:8188 `
  --ordinary-token-file C:\path\to\bridge-token.txt `
  --host-origin http://127.0.0.1:8080 `
  --port 3001
```

Open `http://127.0.0.1:8080` and call `comfy_read_context` through this adapter.
Select `comfy_preview_parameter` and enter that exact binding plus the requested
parameter, for example `{"instance":"...","context":"...","revision":"...","node_id":2,"widget":"steps","value":28}`.
Include `"application_instance":"..."` unchanged when the context result
supplies it; the OA-backed ordinary server requires and verifies that field.
The adapter logs whether initialize negotiated
`io.modelcontextprotocol/ui`. A successful iframe and resource load are still
insufficient evidence of negotiation unless that log records
`"negotiated":true`.

Stop both local servers with Ctrl+C first. The following commands remove their
disposable project-local dependencies and build output:

```powershell
Remove-Item -Recurse -Force .forks/ext-apps-v2.0.0
Remove-Item -Recurse -Force adopters/comfyui-presentation/apps-host/node_modules
Remove-Item -Recurse -Force adopters/comfyui-presentation/apps-host/dist
```

The comparison adds no user setting, global package, installed application or
network provider. The reference host has rendered a deterministic proposal card;
live credential-backed comparison and human usability validation remain unproven.
