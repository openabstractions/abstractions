# OpenCode upstream drafts

These patches are proposals for OpenCode upstream, written against the
v1.18.31 source (commit `014614d35b397775e5d397a490fc72368c894ec2`). OA adopts
OpenCode through its official installer and the `@openabstractions/opencode`
plugin (VISION 2026-10-06); OA builds no OpenCode executable and the npm
package carries none of these files. The plugin checks for each feature at
run time (`../opencode-build.js`) and leaves it out on the official build.
The draft texts, their target issues and their disposition are in
`feedback/2026-10-02-opencode-upstream-drafts.md`; posting any of them needs
the owner's word.

| Patch | Draft | Disposition (2026-10-06) |
|---|---|---|
| `v1.18.31-agent-model-pick.patch` | C1 | a comment on issue #47836 |
| `v1.18.31-provider-model-refresh.patch` | C3 | OpenCode V2's `ctx.provider.reload()` solves it; kept for 1.18.31 |
| `v1.18.31-source-watch-consent.patch` | issue #37164 | a generic plugin-supplied permission title and detail |

Apply a draft to a clean checkout of that commit with `git apply --unidiff-zero
<patch>`. The agent model pick's dialog action uses the SDK and toast handles
the provider refresh draft adds, so it compiles only with that draft applied
first.

## Provider model refresh

`v1.18.31-provider-model-refresh.patch` adds a per-provider hot-refresh service method, an HTTP API route, and a TUI model chooser action. The route invokes the plugin's `provider.models` hook and atomically replaces that provider's catalog after success. Removed models disappear from future lookups; an operation that already captured its model and language objects can finish. Refresh failures leave the current catalog untouched, and refresh does not change the configured default or current selection. Overlapping refreshes use latest-started-wins publication, so an older callback cannot roll the catalog back even when a newer callback fails. Successful refresh also clears cached language-model wrappers so later requests pick up same-ID metadata changes.

When a later request explicitly selects a model removed from the refreshed catalog, OpenCode returns its typed `ModelNotFoundError` response (404) with the selected provider/model and suggestions. The request does not fall back to another model or start a backend call.

In the TUI model chooser, select a model and press `Ctrl+Alt+R` to refresh that model's provider. The chooser reloads the provider snapshots afterward. The desktop web picker has no refresh action in this patch.

From `packages/opencode`, run the focused regressions with:

```powershell
bun test test/provider/provider-refresh.test.ts test/server/httpapi-provider.test.ts test/server/httpapi-session.test.ts
```

The OA plugin exposes Router inventory through `provider.models` for the configured OA provider. Its startup hook establishes package ownership, and the callback supplies the live model map used by this patch's refresh action.

## Source-watch consent display

`v1.18.31-source-watch-consent.patch` makes the TUI show the exact bounded description supplied with OpenAbstractions log-source read and event-sharing approvals. A missing or malformed description leaves only Reject available. One-shot approvals with no `always` patterns show Allow once and Reject, so the UI does not suggest a persistent grant. From `packages/tui`, run `bun test test/permission-disclosure.test.ts`.

## Agent model picks

`v1.18.31-agent-model-pick.patch` keeps the model picked for each agent across
restarts and lets the model dialog make a model an agent's default. The TUI
saves each pick in `model.json` under `agent`, with the configured model it
replaced (`over`), and restores it on start. A saved pick yields once the
configuration names a different model for that agent, so a model
`opencode-configure --model` writes later takes effect at the next start. In
the model dialog, select a model and press `Ctrl+S` ("Default for this agent")
to pick it and write `agent.<name>.model` to the global config. The patch
changes `packages/tui` only. From `packages/tui`, run
`bun test test/context/local.test.ts`.

## Moving to a new OpenCode version

The plugin is tested against OpenCode 1.18.31. Before moving to a new
version:

1. Rebase each draft above on the new tag if it is still proposed, and re-run
   its regressions.
2. Re-check the hand-written compact schemas in `../compact-tools.js`
   (`SCHEMAS`) against the new built-in tool parameters: every property,
   type, enum and required list must match, since the model sees these
   schemas while OpenCode validates with its own.
3. Copy the new version's tool text into
   `../test/fixtures/opencode-<version>-tool/` (every `*.txt` of
   `packages/opencode/src/tool/` and its `shell/prompt.ts`, saved as
   `shell/prompt.ts.txt`, with OpenCode's MIT `LICENSE`), re-derive `../test/fixtures/opencode-1.18.31-tool-safety.json`
   from it (rename both for the version) and keep every clause in the compact
   text; `test/compact-tools.test.mjs` fails on a clause the fixture misses
   or the compact text drops.
4. Re-check what `compactShell` reads from the shell description (`Shell:`
   and the temporary-work line) and the shell notes it condenses.
5. Re-check the plugin contract the adapter relies on: a server plugin
   module may export only plugin functions, `tool.definition` and
   `chat.params` hooks, and the TUI slot names it renders into.
6. Re-check `/oa`'s refresh in `../oa-models.js`: it posts to the provider
   refresh route through `api.client.client`, the SDK's internal HTTP client
   (protected in its types), and reads the global config and providers
   through the generated SDK methods. A new SDK can rename or hide that
   field, and the route exists only in a build carrying the provider refresh
   draft (`../opencode-build.js` checks for it).
