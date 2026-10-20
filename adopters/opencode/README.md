# Use OpenAbstractions inference from OpenCode

What the OA plugin gives a person using OpenCode:

- **Each reply says where it ran**: on this computer, with its engine, or on a
  hosted server, from the router's own placement. A hosted reply also raises a
  toast once.
- **One place for local and hosted models.** `/oa` lists OA's models under
  "this computer", "via hosted servers" and saved preferences; choosing one
  makes it OpenCode's default. Hosted keys stay in OA's credential holder,
  never in OpenCode's config.
- **OpenCode keeps working when OA is down.** OA's tools answer that OA is
  unavailable, one toast says so and another when OA returns, and OpenCode's
  other providers are untouched. A missing or hung OA costs OpenCode's start
  one probe of under a second.

The plugin answers OA's asks, the questions the person let OpenCode answer
(`openabstractions_authorization_questions`). It never answers OpenCode's own
permission prompts, and an OpenCode permission answer grants no OA access.

OA's sandboxed start, `openabstractions opencode open`, confines OpenCode on
Windows only, to the folders the person approves (an AppContainer with
file grants). It is folder confinement. Its launch grants no network
capability, so the internet and loopback stay closed to it; OA's own pipes
are admitted. A live run of the official CLI has yet to measure these. It
does not confine OpenCode started as `opencode`, processes outside its job,
or any run on macOS or Linux.

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

The native sandbox session owner supplies `OA_OPENCODE_SESSION_ENDPOINT`,
`OA_OPENCODE_SESSION_PROGRAM`, `OA_OPENCODE_SESSION_ACCOUNT`,
`OA_OPENCODE_SESSION_ID`, `OA_OPENCODE_RUNTIME_PROGRAM` and
`OA_OPENCODE_RUNTIME_ACCOUNT` together. Only that complete context enables
`openabstractions_project_access`. Its `folder` argument is one exact absolute
local folder. The person answers the OA `opencode.project.edit` question; it
requests read, run, create, edit and delete access throughout that folder and
its subfolders for the sandbox session. The plugin verifies the session host
and selected runtime through shared native IPC.

An approved request returns the typed `oaProjectReceipt` in the completed tool
metadata. The plugin matches the committed completion event against the saved
tool part before asking the native owner to retain that receipt. Follow the
native owner's returned instructions to close OpenCode and run its fresh
`--resume-request` command. The native owner confirms the entire session job
has exited and rechecks the approval before applying access and opening the
same saved conversation. Receipt acknowledgment alone applies no grant.
The plugin keeps ordinary entries available and automatically replays no tool
or prompt. An ambiguous saved part or lost acknowledgment leaves the receipt
unconfirmed and triggers no automatic acknowledgment retry.

## Install

OpenCode comes from its own installer; OA changes none of its shortcuts, its
`opencode` command, `PATH`, updates or file associations. Add this plugin with
OpenCode's plugin command, for every project:

```sh
opencode plugin @openabstractions/opencode --global
```

or, for the current project only, `opencode plugin @openabstractions/opencode`.
OpenCode installs the package and its dependencies under its own cache
(`~/.cache/opencode/packages/`) and adds it to `opencode.json`. On Windows x64
npm brings `@openabstractions/ipc-win32-x64`, the native client
`@openabstractions/ipc` loads; the plugin needs no Node or Bun of its own.

One package serves both OpenCode plugin APIs. `./server` (`server.js`)
default-exports `{id, server, setup}`: OpenCode 1.18 calls `server`
(`plugin.js`), and OpenCode V2 (`@opencode/cli` 2.x) calls `setup`
(`v2.js`). On V2 the plugin adds OA's provider with
`ctx.provider.transform`, supplies its language model through
`ctx.aisdk.hook('language')`, reads OA's models again after a reply at most
once a minute and calls `ctx.provider.reload()`, adds the OA tools with
`ctx.tool.transform`, and sets the Local assistant's local-only option in
`ctx.session.hook('context')`. V2 configures it under `plugins`, with the
provider options under `options.provider`. V2 gives a tool no approval
method, so the log watch and preference edits answer that approval is
unavailable there; dictation, `/oa` and the reply place remain 1.18 TUI
features. The V2 path is tested against a context built from
`@opencode/plugin` 2.0.24's declarations; no V2 binary has run it.

The plugin works in two modes:

- **Outside OA's sandbox**, started as `opencode`: every OA feature except
  folder requests. At its first event OpenCode shows one toast: "OpenCode is
  running outside the OA sandbox. To start it protected, run: node <plugin
  folder>/official-install.js --project <folder> --run"
- **Inside OA's sandbox**, started by OA's own entry point
  `openabstractions opencode open --project <folder> --program <opencode.exe>
  --plugin <plugin folder> --config <opencode.json>`: the same features plus
  `openabstractions_project_access` for further folders. The entry point
  requires all three paths. `official-install.js` finds the official CLI, this
  plugin and OpenCode's config and prints the full command:
  `node <plugin folder>/official-install.js --project <folder>` (`--run`
  starts it).

  Known limitations inside the sandbox (live runs 2026-10-06, OpenCode
  1.18.34):

  - **`grep` and `glob` answer "ripgrep execution failed".** OpenCode's
    search tool needs a program the sandbox does not have and no network to
    download it. OA's policy on third-party software means neither OA nor
    this plugin names, locates or starts one for OpenCode. Inside the
    sandbox, the plugin shows one notice per start: "Search tools aren't
    available inside the OA sandbox yet."
  - **The shell tool (`bash`) hangs.** Its command never starts, and
    OpenCode stops answering until it is interrupted. Every process OpenCode
    starts with piped output hangs inside an AppContainer, which also covers
    formatters and language servers. Bun 1.3.14, which OpenCode 1.18.34
    embeds, uses a libuv whose pipe names an AppContainer may not create, and
    it retries the name forever. Bun 1.4.0 and later carry libuv's fix, so an
    OpenCode release on Bun 1.4 should lift this.

Three TUI features exist only in a build carrying the upstream drafts in
`patches/` (`patches/README.md`). The plugin checks for each and leaves it out
quietly on the official OpenCode: `/oa`'s refresh entry needs the provider
refresh route in OpenCode's OpenAPI document, and a log-sharing approval needs
the consent-display TUI that shows its bounds (the official TUI answers a
sharing request as `sharing_unavailable`; counts-only watches still run).
Per-agent model picks kept in `model.json` exist only with the agent model
pick draft; the plugin does not use them.

Publishing to npm needs the owner's npm organization and consent. Until then
`scripts/npm_packages.py` packs the tarballs, and
`opencode plugin <path to the .tgz>` installs one.

## Requirements

- OpenCode 1.18.31 or a later 1.18 release: the plugin's peer dependency is
  `@opencode-ai/plugin` `^1.18.31`, which npm installs beside it. Tested:
  1.18.31, the controlled integration proof, and the official 1.18.34 CLI in
  OA's sandbox (`research/live-runs/2026-10-06-opencode-sandbox-root-exception.md`).
- A running development OA runtime with at least one model server.
- The shared C ABI library. OpenCode's Bun runtime loads it through `bun:ffi`;
  this path does not load the Node addon. `@openabstractions/ipc` resolves it
  from its platform package, and `ABSTRACTION_IPC_LIBRARY` overrides that with
  an absolute path during development.
- The `@openabstractions/facade`, `@openabstractions/inference`,
  `@openabstractions/ipc`, `@openabstractions/logging` and `@openabstractions/router` packages, named as
  dependencies of this package and copied beside it when it runs from source.
- An installed runtime the shared library selects, or an explicit controlled
  runtime endpoint in `runtimeEndpoint`.

This package is `@openabstractions/opencode`. It is not on npm: publication
needs the owner's npm organization and consent. Until then it is installed from
a tarball the release packaging job produces, or run from source by file URL.
OA builds, packages and installs no OpenCode executable. Its patched
**OpenCode OA Preview** build was retired on 2026-10-07; the source patches it
carried are upstream drafts in `patches/`, outside the npm package.

OA runs its OpenCode scripts (`configure.js`, `official-install.js`) with
Node and never through OpenCode's own program (coordinator decision,
2026-10-07): run as Bun (`BUN_BE_BUN=1`), the program would also be a script
host, and its identity answers OA's asks as the assistant (abstraction-asks
ASK-R7). The plugin and the installer flow run inside OpenCode and need no
`BUN_BE_BUN`.

## Configure a local model

`configure.js`, shipped with the plugin, writes the provider block from the
models the router already lists, so nobody types a model name into OpenCode's
config by hand. It runs under Node and reads the router's public Offer
under the caller's own identity, which needs `abstraction.router/inventory.read`:

```console
node adopters/opencode/configure.js configure
```

It projects the offers with `inventory.js`, the projection the plugin applies
at every OpenCode start. It reads the router's servable chat families, registers
the startup plugin, and writes or updates only the `provider.openabstractions`
block and a default `model` key when OpenCode's config names none, in
OpenCode's global config (`~/.config/opencode/opencode.json` by default;
`--config` names another file). It adds the same adapter source's TUI module
to the `plugin` list of the `tui.json` in the same directory and changes
nothing else there; when only a `tui.jsonc` exists, it prints the entry to add
by hand. Each changed file, or a linked file's target, is staged beside it,
flushed and renamed over the old one; the previous version stays as
`<file>.openabstractions.bak`, a person's own `<file>.bak` is never touched,
and a file edited while configure runs is refused. The lock files live in
`openabstractions-opencode-configure` under the account's temporary folder,
and a staged file a killed run left is removed by the next run. It refuses a
`.jsonc` config by name, since it cannot preserve comments; `--dry-run` prints
the merged config instead of writing either file, and a machine with no
servable chat model writes nothing and says why. Run
`node configure.js --help` for every flag, including `--provider-id`,
`--scope local|remote`, `--adapter` and `--selection-policy`. `--model FAMILY`
selects one servable family as the `model` key even when the config names
another, and `node configure.js models` lists those families with the servers
of each. OpenCode asks for its chat rights at first use, and its chat model
is the `oa-chat-default` policy described below.

After publication, the default `--adapter @openabstractions/opencode` registers
the npm package in OpenCode's `plugin` list. While running from a source
checkout, pass the absolute package directory instead, for example:

```console
node adopters/opencode/configure.js configure --adapter "C:/path/to/AI abstractions/adopters/opencode"
```

Configure checks that this directory's `package.json` names
`@openabstractions/opencode` and exports `./server`, then records it in the
plugin list. Existing plugin entries, including a version-pinned OA package,
are preserved and not duplicated. An explicit local adapter selection replaces
the existing OA registration and points the provider at that package's
`index.js`; otherwise a configured exact OA package version remains pinned. The plugin reads fresh router
inventory during OpenCode instance setup and updates the configured provider's
visible model choices. Configure preserves credential names, custom options,
the selected default, unmarked custom models, and model entries a person has
edited. It marks its own generated entries in
`provider.<id>.options.oaInventoryManagedModels`; older configurations without
that marker keep their existing model entries until configure is run once to
record ownership. Inventory is visibility only: the router's per-program
inference and credential rights still decide each request.

The plugin reads its own chat policy, `oa-chat-default`
(`defaultSelectionPolicies` in `inventory.js`), through OA's selection
service at each start, with no configure step. The person chooses OpenCode's
chat model on the Panel's Models page, in the "Chat model for OpenCode" row
of Chosen models; that generic per-app chooser stores `oa-chat-default` for
OpenCode's program. Once that policy exists, the main heading lists it as
`Preference: oa-chat-default` (model ID `oa-chat`), and at start the plugin
makes it OpenCode's model when the config names a model under the OA provider
or none. Each request on it names the policy, and the runtime applies its
candidates in order. A config model under another provider stays. With no
policy stored, OpenCode keeps the model its config names; a config that names
none, or names `oa-chat`, gets OA's default, the first servable family by name
(the model configure writes). A program reads its own policies with no grant
(inference CONTRACT.md INF-R1). A policy the runtime cannot read keeps the
configured model, with one info line in OpenCode's log. An `oaSelectionPolicies` entry that names another
policy under `oa-chat` takes its place.

Create a caller-scoped ordered preference with the
`openabstractions_selection_policy` tool (enabled through the plugin's `tools` option, see "Tools and the local profile"), granting the OpenCode executable
`abstraction.inference/selection.manage` for that account. Then register its
model choice explicitly:

```console
node adopters/opencode/configure.js configure --selection-policy cloud-local
```

OpenCode shows that choice as `Preference: cloud-local` (model ID
`oa-preference-cloud-local`). Configure keeps the existing default model. The
plugin asks the runtime for the named policy under OpenCode's own caller
identity and lists the preference only when the router's offers for its
candidates' placements leave input budget at the smallest offered context.
Each request admits one policy
candidate through the runtime; the visible inventory does not promise future
availability or backend tool support.

When enabled, the plugin offers an explicit `openabstractions_watch_logs` tool. Each
watch asks before reading one local source for a bounded interval and record
budget. It returns counts and status by default. Sharing event contents with
the current assistant model requires a second approval because that model route
may use a hosted provider. The reader call uses the same local runtime selected
for the plugin and binds OpenCode cancellation to its IPC requests.

Event sharing also requires an OpenAbstractions exact permit for the receiving
OpenCode executable, action `opencode/log-source.share`, and the selected source
ID, such as `windows/application` or `linux/system-journal`. The tool checks
Rights before collecting a share buffer, while collecting, and immediately
before returning events. A refusal, Rights outage, source error or cancellation
returns counts and status and discards buffered contents. Normal duration or
limit completion can return events after a fresh bounded check. Each decision
samples current policy; revocation after returned bytes cannot recall them.
The share permit leaves logging's separate `source.read` permission unchanged.
Count-only watches retain their default and require no share permit.

The package ships `rights-action-description.template.json`. Copy it and replace
both `feature_app` placeholders with the actual account ID and final installed
OpenCode executable's absolute path, using proper JSON escaping for Windows
backslashes. The unfilled template is not a publishable descriptor. Its app and
risk labels are declarations; the service separately records the actual
publishing operator. `feature_app` describes whose feature is published and
supplies no authorization identity. The receiving program determines direct
Rights decisions.

Using an OpenAbstractions version supporting described actions, the person
explicitly publishes the filled file through the existing operator CLI:

```text
openabstractions rights register-action --description-file opencode-share.json
```

Publication grants no permission. A separate operator command grants the exact
source for the final OpenCode program (add `--account ID` for another account):

```text
openabstractions rights grant --program <absolute OpenCode executable> --action opencode/log-source.share --resource windows/application
```

Use that same exact subject/action/resource with `rights grant --deny` to block
sharing or `rights revoke` to remove its permit. On Linux, use
`linux/system-journal` as the resource. Installation, configuration and plugin
startup perform no publication or grant. Older services without described-action
support report publication unavailable. Both explicit local OpenCode approvals
are still required for each watch, including after an OA permit is granted.

OpenCode 1.18.31 calls the plugin `config` hook before it constructs its
provider snapshot, so this refresh applies when a new OpenCode instance starts.
The provider service caches that snapshot. The companion source patch in
`patches/` adds a TUI model chooser refresh action (`Ctrl+Alt+R`) for a running
instance; it refreshes the selected provider and leaves the current selection
and default untouched. The patch targets exactly OpenCode v1.18.31 and does not
add a refresh action to the desktop web picker.

Each model's `limit.context` is the context the router's `Offer` answers for
that family (router CONTRACT.md ROUTE-D8). The router counts the family's
chat aliases on the servers the scope admits (this machine for `local`,
hosted servers and remote runtimes too for `remote`) and takes the smallest
bound across them: each alias's per-request limit when its server reports one
(`effective_context_length`, such as Lemonade's loaded or configured context),
else the model maximum (`context_length`), else the 32768-token chat default.
A family with an alias on a server that fixes its window at load and reported
none (Lemonade today) has an unknown context; it is omitted, and the plugin
logs each such alias as `<server>/<name>`. The plugin and configure both ask
`Offer` to keep the 4096-token output budget, and a family that leaves no input
beside it is omitted with a diagnostic; configure does not invent an
input/output split. These are inventory-time observations, not a guarantee
about a later router-selected host or model. A runtime or router package
older than `Offer` leaves the configured entries in place; the plugin logs,
and configure refuses with, "This OpenAbstractions runtime is older than the
router Offer method; update OpenAbstractions."

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
      "guarantees": ["abstraction.inference/local-only@1"],
      "oaInventoryManagedModels": {
        "local/chat": {
          "name": "OA local chat",
          "limit": {"context": 32768, "output": 4096},
          "tool_call": true
        }
      }
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
        "guarantees": ["abstraction.inference/local-only@1"],
        "oaInventoryManagedModels": {
          "local/chat": {
            "name": "OA local chat",
            "limit": {"context": 32768, "output": 4096},
            "tool_call": true
          }
        }
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

## Tools and the local profile

OpenCode sends every registered tool's description and schema with every
model request, so the plugin registers a tool only when it is wanted:

| Tool | Registers |
|---|---|
| `openabstractions_generate_image` | when the runtime serves an image model at OpenCode's startup, on the provider's placement (local, or any for `scope: remote`) |
| `openabstractions_speak` | when the runtime serves a speech model at startup |
| `openabstractions_selection_policy`, `openabstractions_watch_logs`, `openabstractions_open_credentials_panel`, `openabstractions_authorization_questions` | only when enabled |

The plugin entry's `tools` option overrides either way, by tool name
without the prefix: `true` registers the tool, `false` never registers it,
and an unknown name or a non-boolean value is ignored with a warning.

```json
{"plugin": [["@openabstractions/opencode", {"providerId": "openabstractions",
  "tools": {"watch_logs": true, "speak": false}}]]}
```

A capability found after startup registers its tool at the next OpenCode
start. Configure keeps the plugin entry's options, `tools` included.

### The Local assistant

`configure.js configure --model FAMILY` also writes a profile.
With `--scope local`, the default, the model runs on an engine on this
machine, where every prompt token is prefilled locally, and configure writes
the **local profile**, all in opencode.json:

- an agent named `local`, the Local assistant: `mode: primary`, no model of
  its own (it follows the default `model`, which `--model` and Setup set, so
  switching agents never switches models), a prompt of about 640 tokens written from OpenCode's `default.txt`
  (correctness, code changes, tool use, conciseness) with a paragraph on
  OpenAbstractions, `task` denied (subagents queue on one local slot) and
  `question` allowed as `build` allows it. Every other tool stays: `bash`,
  the file tools, `webfetch`, `skill`, `todowrite` and the OA tools;
- `default_agent: "local"`, so new sessions start on it;
- `agent.title.disable`, since OpenCode's title request is global and would
  delay the first answer on the same slot;
- `compactTools: true` in the OA plugin entry: through the `tool.definition`
  hook the core tools get short descriptions that keep their usage rules
  (the shell's from the shell OpenCode chose, Windows PowerShell 5.1
  included), every schema loses `$schema` and the ±2^53 integer bounds, each
  OA tool gets one line, and `openabstractions_tools` lists the OA tools and
  returns a tool's full description and parameters on request. OpenCode's
  hook names only the tool, never the session's agent, so the compact text
  applies to every agent while the option is set.

OpenCode's `build` and `plan` agents stay as they are, with OpenCode's full
prompt and subagents; while `compactTools` is set they use the compact tool
text too. Press Tab to switch between the Local assistant and Build
(Shift+Tab goes back);
Ctrl+X then A, or `/agents`, lists every agent; `opencode run --agent build`
picks one for a single run. The prompt line shows the current agent's name,
"Local", in green. A setting you already chose stays and configure says so;
one the profile wrote and you left as is follows a new choice, a new model
included; a model an earlier profile pinned on the agent goes. The plugin
marks the Local assistant's requests local-only (OpenCode's `chat.params`
hook), so OA serves them on this computer or refuses with "the Local
assistant keeps this conversation on this computer"; it never falls back to
a hosted server. With `--scope remote` configure writes the **default profile**,
which removes each setting the local profile wrote that still holds its
value, so a hosted model keeps OpenCode's own defaults. `--profile
local|default` chooses explicitly; without `--model` or `--profile`, configure
leaves the agent settings alone. The record of what the local profile wrote
is `provider.<id>.options.oaProfile`.

Measured first requests on the TUI's server path with an AGENTS.md project,
counted with Qwen3.6-35B-A3B's tokenizer (image and speech registered): 8,849
tokens with OpenCode's defaults, 5,805 on Build with compact tools, and 3,753
on the Local assistant, with no title request.

### Picking a model

OpenCode's model picker groups models by provider name, so configure writes
one heading per place:

- **OpenAbstractions · this computer**: the router's models at placement
  local, each named `<family> · <context> · this computer: <servers>`, the
  family being the model's key (`qwen3.6-35b-a3b · 32k · this computer:
  lemonade`);
- **OpenAbstractions · via hosted servers**, written when a credential
  makes a hosted model servable: the models at placement hosted (hosted
  servers and remote runtimes), named `· hosted: <servers>`, with OA's
  hosted-allowed guarantee;
- **OpenAbstractions · your saved preferences**, for `--selection-policy`:
  the preferences alone (`oaInventory: false`), out of the model lists.

`/oa` (the OA TUI module, command `openabstractions.models`) lists only
OA's models, under the same headings with the default marked, and makes the
chosen one OpenCode's default `model` through OpenCode's config API. An agent
with a model picked in the current session keeps it until the person picks
again, since a TUI plugin cannot select a session's model. Choosing a hosted
model says that the Local assistant will refuse it. The list's first entry
refreshes the models on this computer through the provider refresh patch's
route; the hosted heading's list is read when OpenCode starts.

The picker lists Zen first when it is shown, then every provider by name, so
the three headings appear in this order, after providers whose names sort
before "OpenAbstractions". Recent and favourite models come above them all.

Where a model runs comes from the router's placements (`HostState.hosted`),
never from a server's name. OpenCode's own free cloud models (the `opencode`
provider, Zen) are hidden by default: configure adds `opencode` to
`disabled_providers`, and `--opencode-zen show` lists them again.

Each reply says where it actually ran: the provider puts the router's word
for the answering server on the reply's parts
(`providerMetadata.openabstractions.placement`, `local`, `hosted` or
`unknown`), read from a host table at most 30 seconds old. Right of the
prompt, the OA TUI module shows the latest finished reply's place: "replies:
this computer", "replies: hosted (<server>)", "replies: place unknown", or,
for a reply from a provider other than OA, "replies: not through
OpenAbstractions". A reply from a hosted server, or one the router could not
place, also raises a warning toast. A failed reply leaves the line as it was.

### When OpenAbstractions is unavailable

A lost runtime (the native IPC connector's Disconnected or IoError, or no
runtime to resolve) never fails OpenCode. `availability.js` marks OA away and
the TUI shows one toast, "OpenAbstractions is unavailable; OA tools are off
until it returns." OpenCode keeps working with its other providers:
- OpenCode's first provider listing keeps the configured OA models; a later
  listing, which is a refresh, still fails so OpenCode reports it and keeps
  its catalog;
- each OA tool answers that sentence at once, and tries the runtime again
  every 15 seconds;
- an OA model call fails with the same sentence, for that call only;
- the hooks OpenCode runs inside each request (`chat.params`,
  `tool.definition`, `config`) keep OpenCode's values when they cannot do
  their part.

Every other IPC status stays the call's own error. A runtime that did not
prove its identity (Untrusted) is never "unavailable": the call fails with
"OpenAbstractions stopped this call: the runtime it reached did not prove it
is this computer's OpenAbstractions." and the TUI shows it once.

A call that gets an answer from the runtime (a resolved client, a model
reply, an inventory survey) turns the tools on again, with the toast
"OpenAbstractions is back; OA tools are on again." A call that ends before
reaching the runtime, such as a refused `save_to`, changes nothing. A
capability tool whose capability could not be read at start is registered
anyway and answers the same way until OA returns.

## Use a hosted credential

To add a hosted provider's key from OpenCode, request the
`openabstractions_open_credentials_panel` tool. After OpenCode's per-call
launch approval, the tool resolves the installed runtime through the shared
native selector and opens that installation's Panel at **Credentials**. The
tool accepts only the provider choice `credentials`; it has no key, URL, or
executable-path input. Enter the API key in the Panel, then separately allow
that exact OpenCode executable to use the named credential; that is a separate
permission choice. Register the provider's model server on the Panel's Models
page, then refresh the model chooser with `Ctrl+Alt+R` in a TUI built with the
provider refresh draft, or restart OpenCode.

The launcher currently opens the Windows Panel from beside the runtime selected
by the shared native selector. That installation must include both
`tools/openabstractions.exe` and `tools/Abstraction Panel.exe`. The launcher
does not search `PATH`; if the selected installation or sibling Panel is
unavailable, it gives manual setup guidance. Launch approval starts the Panel
only; it does not grant credential or inference rights. The account owner
should review provider usage limits before sending paid requests.

Manual CLI enrollment is an alternative to the Panel. It stores the value through the Holder API from standard input and can
scope it to the exact OpenCode executable:

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

## Dictate into the prompt

The package's TUI module (`exports["./tui"]`, `tui.js`) adds the command
`openabstractions.listen`: `Ctrl+Alt+M` or `/listen` starts recording, and a
second press or 30 seconds stops it. The prompt shows `● listening 0:03` while
it records. OpenCode records 16 kHz mono PCM16 from the default Windows input
through `winmm.dll` with `bun:ffi` (`winmm-capture.js`), uploads the WAV
through `abstraction.storage/content-writer@1` under its own program identity,
and starts `transcription@1` with `Whisper-Large-v3-Turbo`, no timestamps and
the local-only guarantee. The text lands in the prompt for review; Enter sends
it. The recording is request input: OpenCode needs content write and read on
`input:audio`, and OA removes the recording once the transcription has ended
and ten minutes have passed since the upload.
Recording refuses to start when OpenCode cannot show the prompt indicator. A missing microphone, Windows privacy silence, an OA refusal or a missing
engine each read as one toast. `configure.js configure`
registers the module in the `plugin` list of the `tui.json` beside the config
it writes, keeping every other TUI plugin and an existing entry's options. An
entry such as `["<adapter>", {"keybind": "ctrl+alt+l"}]` changes the key, and
`runtimeEndpoint` in the same options object names a controlled runtime;
without it the installed runtime serves the call. Capture runs on Windows only.

## What is verified

`openabstractions_generate_image` is an explicit image tool. It asks before
starting one `image@1` explicit-model or `image@2` saved-policy generation,
then returns a verified local PNG, JPEG or WebP result as an OpenCode image
attachment. Its inputs are exactly one model or `selectionPolicy`, a prompt and
size (`auto` or `WIDTHxHEIGHT`). An `inputImage` digest of an OA image the
caller may read, such as an earlier result's SHA-256, makes the call an
`edit`, with an optional `mask` digest: a PNG of the input's pixel size whose
fully transparent pixels mark the area to change (inference CONTRACT.md
INF-I1). The tool reads each input's first
bytes for its media type and the runtime verifies the bytes before the
engine sees them. The edit path has boundary tests with fake OA clients only.
`save_to`, a path relative to the session's project directory, also writes
the image there; the approval names the file. Before anything is generated
the tool refuses an absolute path, any colon (a drive or an NTFS alternate
data stream), a path that leaves the project (after links and junctions), an
extension other than `.png`, `.jpg`, `.jpeg` or `.webp`, and an existing file
unless `overwrite` is true. Right before the write it checks the folder again,
so a link or junction swapped in during the approval writes nothing. A new
file is created only if absent; a replacement is a new file renamed onto the
name, so a hard link to a file outside the project is never written through.
An image whose type does not match the extension is returned and not saved.
A result above 8 MiB, a remote-only delivery,
or an unavailable authorized content reader yields an explicit error. The tool
does not accept engine URLs, arbitrary workflows or vendor settings. A caller
abort cancels an accepted operation. This path has boundary tests with fake OA
clients and one real OpenCode HTTP session that returned a verified 512×512 PNG
attachment through a shared OA image provider. Browser display remains to be
qualified. A
saved policy and its optional logical provider pin are managed through OA
`selection-operator@2`; the image call carries only the policy name.

Speech. The package's TUI module (`exports["./tui"]`, `tui.js`) adds the
command **Speak replies** (`openabstractions.speak`, slash `/speak`), kept on
or off in OpenCode's TUI store. With it on, each `session.idle` speaks the
last answer: fenced and inline code, paths, URLs and Markdown markers are left
out, the first 600 characters are sent to OA `speech@1` with `kokoro-v1`,
voice `af_sky`, format `wav`, and the delivery is read back through the
content reader and checked by size, SHA-256 and WAV header before OpenTUI's
`Audio` plays it. A newer utterance stops the one playing. The server tool
`openabstractions_speak` reads a chosen text aloud (1..4096 characters, an
optional English Kokoro voice); like the image tool it asks before each call,
verifies the delivery and returns its digest, and the TUI module plays it.
OpenCode loads the TUI module when `tui.json` lists `@openabstractions/opencode`.
The speech path has boundary tests with fake OA clients and a fake audio
device; playback in a live OpenCode TUI remains to be qualified.

`openabstractions_authorization_questions` lists only questions the OA runtime
currently lets the observed OpenCode executable answer. The person enrolls
OpenCode once in the Panel's Assistants section, after reading what routine
questions it may answer. Enrollment ends after 30 days without an answer or
when the person presses Stop. A script host cannot be enrolled. Before a local
OA model request, the plugin adds a short list of pending questions to its
system text as data; text another app wrote is never an instruction. The
model can use the tool to answer a freshly listed ID with one listed option.
The person sees each answer in OpenCode and in the Panel's Answered for you
list, with Undo. The runtime checks enrollment, the routine tier, the
question's parties and the program's identity again inside the answer edit.
There is no approval prompt for each answer after enrollment. The tool
reports the recorded answer and rights effect separately;
an answered question with a conflicting or unavailable rights edit is not a
grant. It cannot name another target, grant itself rights, or call the broader
question/rights operator. The public responder service and a fresh installed
OpenCode authorization session remain to be qualified.

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
- `package-lock.json` pins the third-party closure of `@ai-sdk/provider` and
  `@opencode-ai/plugin` (33 packages) with registry integrity. The unpublished
  OA packages have no entries in it. npm never packs a `package-lock.json`; a
  published tarball carries these pins as `npm-shrinkwrap.json`.
  `scripts/npm_packages.py` runs `shrinkwrap.js` in its staged copy, which
  writes the file from `package-lock.json` at the packed version and refuses a
  lock entry without a sha512 integrity or outside the npm registry. It then
  packs with `--ignore-scripts` and checks that the tarball's shrinkwrap names
  the packed version and exactly the lock's packages. The OA packages,
  unpublished, stay out of the shrinkwrap and resolve by their exact version.
- The shrinkwrap pins only consumers that install the published tarball from
  the registry; a `file:` or linked install of this directory reads no
  shrinkwrap. npm installs a shrinkwrapped package's closure nested under the
  adopter (`node_modules/@openabstractions/opencode/node_modules`) instead of
  sharing it with the application's own copies of the same packages.
- Platform packages exist for `win32-x64`, `linux-x64`, `darwin-x64` and
  `darwin-arm64`. Assembly and installation from the packed tarballs ran on
  Windows x64; Linux and macOS assembly is unrecorded, and no release workflow
  run has produced these packages yet.
- Native program attribution distinguishes executable identities. Multiple
  OpenCode sessions using the same executable share that program principal.
- A `not_permitted` refusal whose reason is a pending rights question
  (`rights:not_granted`) reads to OpenCode as one sentence: the program is not
  yet permitted, a question is waiting in the Abstraction Panel, allow it there
  and retry. A `rights:denied` refusal says the program is blocked and that the
  Panel's Permissions page changes that; any other refusal points at the
  Panel's request outcome. `OAInferenceError`'s typed `outcome` and `reason` still carry
  the original values for code that wants them. Before that sentence, a
  `rights:not_granted` refusal waits up to 60 seconds: once a second the
  call reads this program's own decision for
  `abstraction.inference/complete` on each server the router offers the
  model on (rights `Decide`; a refusal names no host), and when the person
  allows the question it sends the call once more, so the person never
  retries by hand. A denial on every server, a cancelled call or the end of
  the wait refuses with the sentence. The plugin never reads or answers the question
  itself: the runtime asks it as itself (asks ASK-R2), and an application's
  `Observe` reaches only its own questions (ASK-R1). The image tool's
  refusal for a `rights:not_granted` start names the waiting question in
  the same words and does not wait.
- A context-window refusal (`refused`, reason
  `upstream:context_length_exceeded:<window>:<input>`) reads as one message
  naming the model's context, the counted input and the request's answer reserve,
  with the remedies: compact or start a new session, remove instructions or
  tools, or choose a model with a larger context. The wording matches OpenCode 1.18.31's
  overflow classifier, which compacts the session and never retries.
  `OAInferenceError.context` carries `{window, input, reserve}`. An engine
  that exits mid-reply keeps the generic Panel message.
- Inventory refresh logs one warning for each user-edited model entry whose
  `limit.context` exceeds the context OA observes for that family. The entry
  is kept unchanged.

The package test command uses the exact SDK dependency declared in
`package.json`; repository qualification restores the OpenAbstractions modules
from their checked-out source before running it. The development tests stage
the provider and generated packages into a temporary package tree. Set
`ABSTRACTION_IPC_LIBRARY` to the absolute path of
the built shared library for OpenCode when running from source.
`ABSTRACTION_IPC_NODE` is used by the separate Node cancellation helper and
Node consumers. The public [coverage
page](https://openabstractions.org/coverage.html) records current checks; the
limits above describe this development integration.

## Run the tests locally

The adopter's Node tests (`npm test`, which runs `node --test`) need the eight
`@openabstractions` packages from this checkout and the third-party closure from
`package-lock.json`. The OA packages are not on npm, so `npm ci` alone fails
with a 404. `test/run_node_tests.py --run` installs them as links together with
the locked closure when `node_modules` lacks them, without rewriting
`package.json` or the lock, and then runs `npm test`; its `--help` names the
`npm install` command it runs. Neither step needs Bun. The one Bun-only test,
`test/winmm-binding.test.mjs`, then runs under the pinned Bun when
`.forks/bun-1.3.14/` holds it (see below) and is skipped with the reason
otherwise:

```console
py -3.12 adopters/opencode/test/run_node_tests.py --run
py -3.12 -m unittest discover -s adopters/opencode/test -p "test_*.py"
```

The second line runs the Python helper tests (the Node-API build inputs and
the node test runner). `scripts/gate_select.py` selects both for any change
under `adopters/opencode/`.

On Windows the links are directory junctions into the checkout. Remove them
with `rmdir` before deleting a copied tree; a recursive delete in Windows
PowerShell 5.1 can follow a junction into the source.

The Bun-only plugin test and the upstream drafts' tests run under the pinned
Bun 1.3.14, the version v1.18.31 builds with. `get-pinned-bun.ps1` downloads
`bun-v1.3.14/bun-windows-x64.zip` from oven-sh/bun's GitHub releases into the
checkout's ignored `.forks/bun-1.3.14/` (`-Forks` names another `.forks`),
checks it against the SHA-256 the script pins and extracts `bun.exe` there; it
prints the `bun.exe` path. This is the Bun for the tests:

```powershell
powershell -NoProfile -File adopters/opencode/get-pinned-bun.ps1
```

Install the pinned source's dependencies first with
`bun install --frozen-lockfile --ignore-scripts`, and point `XDG_CACHE_HOME`,
`XDG_DATA_HOME`, `XDG_CONFIG_HOME` and `XDG_STATE_HOME` at scratch directories;
`patches/README.md` lists the commands.
