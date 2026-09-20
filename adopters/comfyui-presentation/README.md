# Preview an assistant's ComfyUI change before applying it

An assistant can find the current ComfyUI workflow, propose changing one numeric
node parameter, and ask ComfyUI to show the exact before and proposed values.
The person reveals the node and applies the change with separate buttons inside
ComfyUI. A graph reset or another edit makes an old proposal stale instead of
retargeting the same node number in a different workflow.

A controlled integration activated ComfyUI, waited for its application
announcement, let OpenCode 1.18.31 propose changing sampler steps from 20 to 24,
and read back the applied value. Rights revocation and workflow replacement
refused subsequent actions. Human usability validation remains incomplete.
The MCP Apps comparison rendered a deterministic card; the live comparison is
still untested. This is an experimental interface specific to this integration.

See the public [application example](https://openabstractions.org/cases.html#interaction)
and [coverage](https://openabstractions.org/coverage.html) for the supported
capabilities and their evidence limits.

The browser-local API is `app.oaPresentationExperiment`:

```js
const proposal = app.oaPresentationExperiment.preview(7, "steps", 24);
app.oaPresentationExperiment.reveal(proposal.token); // Explicit navigation.
// Apply only after a separate user authorization gesture:
const result = app.oaPresentationExperiment.apply(proposal.token);
app.oaPresentationExperiment.outcome(result.operation);
```

Preview returns the node ID/title, parameter name, before and proposed values.
Apply consumes its proposal and checks the original graph, node, widget and
snapshot. Reopening a workflow invalidates pending proposals. A later edit marks
completed evidence historical. A failed callback reports an uncertain outcome.
Reveal preserves selection and keyboard focus; it changes the canvas viewport.
There is no automatic follow mode or model execution.

The development server adds an ordinary MCP context/preview/read connection. It
exposes no reveal or apply tool. The visible panel remains the only effect path.
Fixture mode uses a local bridge credential. OA mode additionally checks the
gateway program's rights and the live ComfyUI application/workflow binding.

Run the dependency-free stale-state tests:

```text
node --test adopters/comfyui-presentation/presentation.test.mjs
```

Run the server tests without installing dependencies:

```text
cd adopters/comfyui-presentation/server
go test ./...
py -3 -m unittest bridge_test.py
```

OA mode currently builds in this source workspace because Applications is an
unreleased facade source contract. Release packaging remains deferred.

For the isolated live fixture, pass `--bridge` to the runner. It generates an
unpredictable token, stages `server/`, configures the exact ephemeral Origin and
prints only the credential file path. Select that file in the panel or give its
contents separately to the MCP child process. Click **Connect local MCP fixture**;
the first browser receives the bounded lease and later registrations are
refused. The token is absent from MCP tool arguments, results and logs. Example
server command after building this directory:

```text
OA_COMFY_BRIDGE_TOKEN=<per-run-secret> comfyui-presentation-server --mode fixture --comfy-url http://127.0.0.1:8188
```

The bridge accepts 16 KiB bodies, one in-flight command and one browser. It
checks the configured browser Origin and expires an abandoned browser lease.
Use loopback in an isolated temporary ComfyUI base.

## Native OA presence

The optional `--oa` runner mode loads the existing OA Python facade and native
IPC library inside the ComfyUI Python process. The operator must first register
an application descriptor named `comfyui` for the exact native program identity
observed for that interpreter and grant `application.announce` on `app:comfyui`.
The custom node calls only `Announce` and `Withdraw`; it cannot register its own
descriptor or grant itself rights.

After ComfyUI's HTTP server is listening, the process announces this exact
interface:

```text
name=presentation protocol=oa-local contract=comfy.presentation@1
```

The initial announcement has no browser context, allowing A2 readiness to mean
that the application-side service is loaded. Browser readiness remains separate.
When the operator connects the presentation panel, the bridge renews the same
OA instance with the current workflow context UUID, display title and revision.
A workflow reset replaces that context. The browser page UUID remains a distinct
bridge binding. `comfy_read_context` receives the OA instance handle from Python
server state; browser input cannot claim it. Preview checks that handle and
removes it before delivering the command to JavaScript.

The OA lease is 10 seconds and renews every 5 seconds. Normal cancellation makes
a best-effort `Withdraw`; process crashes expire through the lease. Every renewal
resolves a fresh applications client because facade clients carry a fixed call
deadline. A refused announcement clears the bridge mapping and exposes no
fallback path.

Use the public [facade](https://github.com/openabstractions/abstraction-facade)
and [identity](https://github.com/openabstractions/abstraction-identity) checkouts
at matching versions for the Python source paths below. The controlled runner
uses existing dependencies and can target an explicit local runtime endpoint:

```text
py -3 adopters/comfyui-presentation/run.py --run --bridge --oa \
  --python <comfy-python> --comfy <comfy-source> --frontend <frontend-static> \
  --oa-library <libabstraction_ipc> \
  --oa-dll-directory <trusted-native-dependency-directory> \
  --oa-python-path <abstraction-facade-checkout>/py \
  --oa-python-path <abstraction-identity-checkout>/py \
  --oa-runtime-endpoint <configured-local-runtime-endpoint> \
  --oa-runtime-program <absolute-runtime-program>
```

The explicit endpoint and program are a required pair. Python binds the resolver
to the current kernel account and that exact runtime program through verified
native IPC. Omitting both uses the installed runtime selector and its verified
identity.

On Windows, each explicit DLL dependency directory is carried inside the private
bootstrap config and installed with `os.add_dll_directory`; its search handle is
retained for the process lifetime. Preparation loads the actual IPC library under
the same search configuration before reporting the activation recipe. The runner
does not modify `PATH`.

Add `--activation-fixture` to stage without starting ComfyUI. The runner prints
the descriptor's measured base-Python program and the literal activation
arguments, then retains the private fixture for the bounded `--seconds` window.
The arguments contain `-I`, the staged bootstrap path and the private config-file
path. The bearer value remains inside that config and the separate browser
credential file. If A2 starts the recipe, the bootstrap writes a private PID
file; the runner verifies the process image before stopping that owned fixture
and removing its temporary base at the deadline.

The bootstrap restores the venv prefix and processes its existing site-packages
before importing ComfyUI. It places the verified Comfy source beside `main.py`
at the front of `sys.path`, matching direct script execution, and writes process
diagnostics to the fixture-private `server.log`. Runner preparation verifies
`torch` and `aiohttp` from the base Python process first. Its JSON config is capped at 16 KiB and accepts
only the paths, literal Comfy arguments and named OA/bridge environment fields
needed by this fixture.

Native program identity is interpreter-granular for this Python adopter. A venv
launcher may resolve to its base interpreter at the native boundary. The operator
must inspect that observed identity before descriptor registration. The account
and OS session checks remain enforced by the Applications service.

Call `comfy_read_context` immediately before `comfy_preview_parameter`. Copy its
opaque `instance`, `context` and `revision` fields into the preview arguments
alongside its server-supplied `application_instance` and the exact `node_id` and
`widget`. A graph reset or revision change
returns `stale`, including when the new graph reuses the same numeric node ID.

## OA integration adapter mode

`--mode oa` is the designated integration adapter. It resolves the generated
local Applications, Rights and Logging clients through the installed OA runtime.
The adapter runs under an operator-approved integration principal. It does not
announce ComfyUI. The ComfyUI Python process announces application `comfyui`,
interface `presentation` / `oa-local` / `comfy.presentation@1` after its server
is ready, and renews the OA-assigned instance handle. Browser connection later
adds the current workflow context and revision.

An operator registers these custom Rights actions and grants the adapter's
exact program principal each action on resource `app:comfyui`:

- `comfy-presentation/read`
- `comfy-presentation/preview`

The operator separately registers the ComfyUI descriptor and activation recipe,
and grants `abstraction.facade/application.read` and, when activation is wanted,
`abstraction.facade/application.activate` on `app:comfyui`. The Python program
receives its own `abstraction.facade/application.announce` grant. Registration
and grants are operator setup; the adapter has no policy administration client.

Run the adapter with the bridge credential held outside MCP input and results:

```text
OA_COMFY_BRIDGE_TOKEN=<per-run-secret> comfyui-presentation-server --mode oa --comfy-url http://127.0.0.1:8188
```

`comfy_read_context` succeeds only when the bridge's server-injected
`application_instance`, browser instance, workflow context and revision match a
current application-directory presence. Copy all four opaque binding fields into
`comfy_preview_parameter`. Every preview rechecks Rights, the live bridge binding
and the application directory, then submits an attributed event before it
delivers the preview. Logging confirms only that the local sink accepted the
submission; it is not a durable receipt. Any denial, stale mapping or immediate
logging failure produces no preview. The Apply button remains a separate gesture
inside ComfyUI and is outside the preview grant.

`comfy_activate` is an explicit tool. It calls the generated Applications
activation method and never previews or applies a parameter. A successful start
becomes ready from the actual Python process's matching announcement. Activation
does not wait for a browser workflow; preview reports stale until one joins.

Apply creates the operation ID later inside the visible panel. To call
`comfy_read_outcome`, first call `comfy_read_context` again after Apply, then copy
that fresh current binding and the panel's operation ID. Apply callbacks may
advance the workflow revision. Readback rechecks the live bridge and directory,
then verifies a non-unknown result carries the same browser instance, context and
revision. A workflow reset makes historical readback stale in OA mode. The
bridge token binds this adapter to one controlled browser relay. A1 metadata
contains no endpoint or secret.

With no runtime flags, OA mode uses trusted installed-runtime discovery. A
controlled source run may instead supply `--oa-endpoint` together with
`--oa-runtime-program <absolute path>`. The pair binds the resolver to the
current OS account and exact runtime program. Supplying either flag alone or a
relative program path is refused; endpoint-only unverified mode is unavailable.

For a controlled frontend experiment, copy this directory into an isolated
ComfyUI base directory's `custom_nodes`. Avoid changing an installed user's
custom-node tree. The extension is inert until a test or user invokes its API.

`py -3 adopters/comfyui-presentation/run.py --help` describes a bounded runner
using existing dependencies and a temporary base directory. It installs nothing.
The runner was exercised with existing dependencies and temporary data. Expand
**OA presentation experiment**, refresh parameters, choose one and preview the
proposed value. Reveal and Apply are separate explicit buttons. No workflow is
executed. The panel is a development fixture; output shows the bounded result.
