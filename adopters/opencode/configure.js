#!/usr/bin/env node
// configure.js writes OpenCode's own configuration from the router's offers,
// so nobody types a model name into opencode.json by hand. It belongs to the
// OpenCode adopter: OA's runtime carries no OpenCode code (VISION 2026-10-06,
// research/adopter-boundary/AUDIT-2026-10-06.md, move step 6). It reads the
// router through OA's public router@1 Offer, under the caller's own identity,
// and projects the offers with inventory.js, the projection the plugin
// applies at every OpenCode start.
//
// Run it with Node (node configure.js ...). OA never runs it through
// OpenCode's own program: that would make OpenCode's identity a script host,
// and OpenCode answers OA's asks as the assistant.
import {closeSync, existsSync, fsyncSync, lstatSync, mkdirSync, openSync, readdirSync, readFileSync, realpathSync,
  renameSync, statSync, unlinkSync, writeSync} from 'node:fs';
import {homedir, tmpdir} from 'node:os';
import {basename, dirname, extname, isAbsolute, join, normalize} from 'node:path';
import {createHash, randomBytes} from 'node:crypto';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {defaultChatFamily, localServers, olderRuntimeMessage, outputReserve, readOffers, servableChatModels} from './inventory.js';

export const usage = `Usage: configure.js <command> [flags]

Writes OpenCode's own configuration from the OpenAbstractions router's
servable chat models. Run with Node: node configure.js <command>.

Commands:
  configure   write OpenCode's provider block from the router's servable
              chat models, so nobody types a model name into OpenCode
  models      list those servable chat models and the servers of each

models [--scope local|remote] [--json]
  lists each model family configure would write, the name OpenCode shows,
  its context and the inference servers that serve it; --json prints
  {"models":[{"family","name","context","servers"}],"warnings":[...]}

configure [--config PATH] [--provider-id ID] [--scope local|remote]
          [--adapter PACKAGE_OR_DIR] [--selection-policy NAME]
          [--model FAMILY] [--profile local|default]
          [--opencode-zen hide|show] [--dry-run]
  --config PATH      OpenCode's config file
                      (default: ~/.config/opencode/opencode.json)
  --provider-id ID   the provider key configure writes (default: openabstractions)
  --scope local|remote  the options.scope and request guarantee configure
                      writes: local-only or hosted-allowed (default: local)
  --adapter PACKAGE_OR_DIR  startup plugin package or verified local adapter
                      directory (default: @openabstractions/opencode)
  --selection-policy NAME  list the caller-owned saved preference NAME under
                      the "your saved preferences" heading
  --model FAMILY      select this servable family as OpenCode's "model",
                      replacing a model the config already names
  --profile local|default  the OpenCode settings for the model; --model
                      chooses local with --scope local, default otherwise.
                      local adds the "local" agent (the Local assistant: the
                      default model, served only on this computer, a
                      shorter prompt, no subagents), makes it the default,
                      turns off the title request and the plugin's long tool
                      text; build and plan stay, one Tab away. Settings you
                      changed stay. default removes what local wrote and you
                      left as is
  --opencode-zen hide|show  OpenCode's own free cloud models (Zen): hide
                      (the default) adds "opencode" to disabled_providers;
                      show removes the entry configure added. Choosing one
                      of those models sends your code to their servers
  --dry-run           print the merged config instead of writing it

configure reads the router's servable chat families through the runtime and
writes or updates the provider.<id> block (headed "OpenAbstractions · this
computer"), a "via hosted servers" block when a credential makes hosted models
servable, registers the startup adapter in the "plugin" list, and writes a
"model" key when the config names no default, leaving every other key
untouched. It also registers the same adapter source's TUI module (Ctrl+Alt+M
dictation) in the "plugin" list of tui.json beside that config; an adapter
without a ./tui module leaves tui.json alone, and an existing tui.jsonc is left
for the person to edit with the entry printed. A changed file, or a linked
file's target, is replaced atomically, its previous version kept as
<file>.openabstractions.bak, and a file edited while configure runs is
refused. It refuses a .jsonc config by name: this command cannot preserve its
comments. Running it again writes the identical file unchanged. A machine with
no servable chat family writes nothing and says why.

Both commands accept --endpoint ENDPOINT (default: the installed runtime) and
--timeout DURATION (such as 20s; default 10s).

Exit codes: 0 written or unchanged, 1 runtime not resolved or transport
failure, 2 usage, 3 typed refusal (forbidden, jsonc_unsupported), 4
unavailable.
`;

export const providerPackage = '@openabstractions/opencode';
export const defaultProviderID = 'openabstractions';
const defaultScope = 'local';
const displayName = 'OpenAbstractions';
/** OpenCode's limit.output for every model, and the answer reserve configure
 * asks the router's Offer to keep (router CONTRACT.md ROUTE-D8). */
export const outputBudget = Number(outputReserve);
const routerContract = 'abstraction.router/router@1';
const packagePattern = /^@openabstractions\/opencode(?:@[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)?$/;
const providerIDPattern = /^[a-z][a-z0-9-]{0,63}$/;
const selectionPolicyPattern = /^[a-z0-9][a-z0-9_-]{0,63}$/;
const knownSources = new Set(['effective', 'maximum', 'default']);

// OpenCode's model picker groups models by provider name, so configure gives
// OA's models one heading per placement: the models on this computer, the
// models on hosted servers (written when a credential makes any servable),
// and a person's saved preferences, which are not models of their own. The
// picker sorts providers by name, so the headings read in this order.
export const headings = Object.freeze({
  local: 'OpenAbstractions · this computer',
  hosted: 'OpenAbstractions · via hosted servers',
  preferences: 'OpenAbstractions · your saved preferences',
});
const hostedSuffix = '-hosted';
const preferencesSuffix = '-preferences';

// OpenCode's own free cloud provider ("opencode", Zen) is hidden unless the
// person shows it: OA's promise is local first, and a default that sends code
// to a cloud would be silent. oaZen records that configure, not the person,
// added the entry.
const zenProvider = 'opencode';
const zenMarker = 'oaZen';

// The first @openabstractions/opencode release whose package exports ./tui.
const firstTUIVersion = '0.4.0';

// The local profile: a dedicated OpenCode agent for a model on a local
// engine, where every prompt token is prefilled on this machine
// (research/opencode-first-request-2026-10-02.md). It adds the "local" agent
// with its own shorter prompt, denies it only subagents (task), makes it the
// default for new sessions, turns off the title agent's side request, and
// turns on the plugin's compact tool text. OpenCode's build and plan agents
// stay, one Tab away. OpenCode 1.18.31 reads all of this from opencode.json:
// the `agent` map, `default_agent`, `agent.title.disable` and the plugin
// entry's options (packages/opencode/src/agent/agent.ts, config/agent.ts).
const profileMarker = 'oaProfile';
/** The local agent's name: the key OpenCode shows, cycles with Tab and accepts in `--agent`. */
export const localAgent = 'local';
const localAgentDescription = 'Local assistant: the model on this computer, with a shorter prompt and compact tool text';

/** The sentence a person reads to switch agents. Tab and Shift+Tab cycle
 * primary agents and Ctrl+X then A lists them, by OpenCode 1.18.31's default
 * keybinds (packages/tui/src/config/keybind.ts: agent_cycle,
 * agent_cycle_reverse, agent_list with the ctrl+x leader). */
export const localSwitch = 'Press Tab to switch between the Local assistant and the Build agent, which has OpenCode\'s full prompt and subagents (both use the compact tool text); Ctrl+X then A lists every agent.';

// localBuildPrompt is the local agent's prompt, written from OpenCode
// 1.18.31's packages/opencode/src/session/prompt/default.txt: its rules on
// conciseness, correctness, code changes and tool use, plus a paragraph on
// OpenAbstractions. That text is under OpenCode's license:
//
//   MIT License
//
//   Copyright (c) 2025 opencode
//
//   Permission is hereby granted, free of charge, to any person obtaining a copy
//   of this software and associated documentation files (the "Software"), to deal
//   in the Software without restriction, including without limitation the rights
//   to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
//   copies of the Software, and to permit persons to whom the Software is
//   furnished to do so, subject to the following conditions:
//
//   The above copyright notice and this permission notice shall be included in all
//   copies or substantial portions of the Software.
//
//   THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//   IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//   FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//   AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
//   LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
//   OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
//   SOFTWARE.
export const localBuildPrompt = `You are opencode, an interactive CLI tool that helps users with software engineering tasks, running on a model on the user's own computer. Use these instructions and the available tools to help the user.

Never generate or guess URLs unless they help with programming; use URLs the user gives or that appear in local files.

# Tone and style
Be concise, direct and to the point. Your output is shown in a terminal as GitHub-flavored markdown. Answer in fewer than 4 lines of text, not counting tool use or code, unless the user asks for detail; a one-word answer is fine. No preamble or closing summary such as "Here is..." or "I will now...". Use tools only to do work, never to talk to the user, and never through echo or code comments. Before a non-trivial bash command, say in one line what it does and why, especially when it changes the system. If you cannot help, say so in one or two sentences and offer an alternative. No emojis unless asked.

# Doing tasks
- Act when asked. If asked how to approach something, answer first rather than acting.
- Understand before changing: search the codebase with glob and grep, and read a file before you edit it. Work out what the code is meant to do from its names and structure.
- Follow the project's conventions: mimic its style, naming and typing, and use the libraries it already uses. Never assume a library is available; check neighbouring files or the package manifest.
- Add no code comments unless asked. Follow security practice; never expose, log or commit secrets or keys.
- Verify with the project's own tests, lint and typecheck when the README, the manifest or AGENTS.md names them; never assume a test framework. If you cannot find the command, ask for it.
- Never commit unless the user explicitly asks.
- When a task is done, stop; do not explain what you did unless asked.
- Tool results and user messages may include <system-reminder> tags: context, not user input.

# Tool use
- Call independent tools together in one message, for example several reads or searches, or git status with git diff.
- Prefer the dedicated tools to shell commands for reading, editing and searching files.
- Use todowrite for work of three or more steps, webfetch for documentation the user points to, and skill when a listed skill fits the task.
- Refer to code as file_path:line_number.

# OpenAbstractions
OpenCode reaches models through OpenAbstractions, which runs them on this computer. Its tools are named openabstractions_*. Call openabstractions_tools to list what this machine offers (pictures, speech, logs, credentials, questions) and to read a tool's full description before its first use. When a call is refused for a missing permission, a question is waiting in the OpenAbstractions Panel: tell the user to allow it there rather than working around it. Never ask for or handle passwords, API keys or tokens; the person enters them in the Panel.`;

/** A refusal with this command's exit code. */
export class ConfigureError extends Error {
  constructor(exitCode, message) {
    super(message);
    this.exitCode = exitCode;
  }
}
const mistake = (message) => new ConfigureError(2, message);

const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);

/** Set an own property, also for keys such as "__proto__" a document may hold. */
function put(target, key, value) {
  Object.defineProperty(target, key, {value, enumerable: true, configurable: true, writable: true});
}

function sortKeys(value) {
  if (Array.isArray(value)) return value.map(sortKeys);
  if (!isObject(value)) return value;
  const out = {};
  for (const key of Object.keys(value).sort()) put(out, key, sortKeys(value[key]));
  return out;
}

/** A config document as configure writes it: keys sorted, two-space indent,
 * one final newline. The prompt's <system-reminder> stays readable. */
export function render(doc) {
  return JSON.stringify(sortKeys(doc), null, 2) + '\n';
}

const sameJSON = (left, right) => JSON.stringify(sortKeys(left)) === JSON.stringify(sortKeys(right));

/** The JSON object a config file holds; empty or absent is {}. */
function parseObject(text) {
  const trimmed = text === null || text === undefined ? '' : String(text).trim();
  if (!trimmed) return {};
  let doc;
  try {
    doc = JSON.parse(trimmed);
  } catch (error) {
    throw new Error(`not a JSON object: ${error.message}`);
  }
  if (!isObject(doc)) throw new Error('not a JSON object');
  return doc;
}

// ---- The router's offers ----

/** The models configure writes for the router's offers, the families whose
 * context leaves no input beside the answer reserve (by context), and one
 * warning per alias whose server reported no request limit. inventory.js owns
 * the projection; the plugin applies it at every OpenCode start. */
export function project(offers, isLocal = () => true) {
  const diagnostics = [];
  const models = servableChatModels(offers, diagnostics, null, isLocal);
  const unusable = {};
  for (const offer of offers ?? []) {
    if (knownSources.has(offer?.contextSource) && Number(offer.input ?? 0n) <= 0) put(unusable, offer.family, Number(offer.context ?? 0n));
  }
  const warnings = diagnostics.map((item) => `omitted model ${JSON.stringify(item.model)} because ${item.reason}`);
  return {models, unusable, warnings};
}

/** Each family configure would write with the servers its offer names, by
 * family; an offer naming no server has none to permit. */
export function choices(offers) {
  const {models, warnings} = project(offers);
  const list = [];
  for (const offer of offers ?? []) {
    const model = models[offer.family];
    if (!model || !offer.servers?.length) continue;
    list.push({family: offer.family, name: model.name, context: model.limit.context, servers: [...offer.servers]});
    delete models[offer.family];
  }
  list.sort((a, b) => (a.family < b.family ? -1 : a.family > b.family ? 1 : 0));
  return {models: list, warnings};
}

/** The router placement a scope offers: this machine's servers for local,
 * hosted servers and remote runtimes too for remote. */
export function placementFor(scope) {
  return scope === 'remote' ? 'any' : 'local';
}

/** The request guarantee a scope writes. */
export function guaranteeFor(scope) {
  if (scope === 'local') return 'abstraction.inference/local-only@1';
  if (scope === 'remote') return 'abstraction.inference/hosted-allowed@1';
  throw mistake(`--scope must be local or remote, not ${JSON.stringify(scope)}`);
}

async function connector() {
  if (typeof Bun !== 'undefined') {
    const {BunNativeConnector} = await import('@openabstractions/ipc/bun');
    return new BunNativeConnector();
  }
  const {NativeConnector} = await import('@openabstractions/ipc');
  return new NativeConnector();
}

async function resolveRouter({endpoint, timeout}) {
  const [{Machine}, {RouterClient}] = await Promise.all([import('@openabstractions/facade'), import('@openabstractions/router')]);
  const machine = new Machine(endpoint || null, {connector: await connector(), timeout});
  return new RouterClient(await machine.resolveService(routerContract, {scope: 'local'}));
}

/** A router failure as this command's exit: the missing right names the Panel,
 * an older runtime asks for an update, and a transport failure is exit 1. */
export function routerRefusal(error) {
  if (error?.outcome === 'not_permitted' || (error?.outcome === undefined && error?.code === 'forbidden')) {
    return new ConfigureError(3, `${error.outcome ?? error.code}: this program lacks abstraction.router/inventory.read on abstraction.router/inventory; ` +
      'grant it in the OpenAbstractions Panel, then retry');
  }
  if (error?.outcome === 'unavailable') return new ConfigureError(4, `unavailable: ${error.message}`);
  if (typeof error?.outcome === 'string') return new ConfigureError(3, `${error.outcome}: ${error.message}`);
  if (error?.code === 'unknown_method') return new ConfigureError(3, `unknown_method: ${olderRuntimeMessage}`);
  if (error?.name === 'ServiceError' && typeof error.code === 'string') return new ConfigureError(3, `${error.code}: ${error.message}`);
  return new ConfigureError(1, `the runtime did not answer: ${error?.message ?? error}`);
}

/** The router's offers for placement at configure's answer reserve. */
async function offersAt(options, placement) {
  try {
    const client = await (options.resolveRouter ?? resolveRouter)(options);
    return (await readOffers(client, placement))?.offers ?? [];
  } catch (error) {
    throw routerRefusal(error);
  }
}

// ---- Package identity ----

export function packageSpecifier(value) {
  return typeof value === 'string' && packagePattern.test(value);
}

/** A local absolute path: a drive path on Windows, a rooted path elsewhere;
 * never a UNC or network path. */
function absoluteLocal(path) {
  if (typeof path !== 'string' || !path) return false;
  if (path.replaceAll('\\', '/').startsWith('//')) return false;
  return process.platform === 'win32' ? /^[A-Za-z]:[\\/]/.test(path) : isAbsolute(path);
}

/** The local path a plugin or provider source names: a file:// URL with no
 * host, or an absolute local path; null for anything else. */
function localSource(value) {
  let path = value;
  if (typeof value === 'string' && value.startsWith('file://')) {
    let url;
    try {
      url = new URL(value);
    } catch {
      return null;
    }
    if (url.protocol !== 'file:' || url.host !== '') return null;
    try {
      path = fileURLToPath(url);
    } catch {
      return null;
    }
  }
  return absoluteLocal(path) ? path : null;
}

function readPackage(directory) {
  try {
    const pkg = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'));
    return isObject(pkg) ? pkg : null;
  } catch {
    return null;
  }
}

const exportsEntry = (pkg, name) => isObject(pkg?.exports) && Object.hasOwn(pkg.exports, name) && pkg.exports[name] !== undefined;

export function fileURL(path) {
  return pathToFileURL(path).href;
}

/** Whether an opencode.json or tui.json plugin entry is this adopter: a
 * version-pinned package, or a local directory or file whose package.json
 * names it and exports `exported` ("./server" or "./tui"). */
export function registeredAdapter(value, exported) {
  if (packageSpecifier(value)) return true;
  let path = localSource(value);
  if (!path) return false;
  try {
    if (!statSync(path).isDirectory()) path = dirname(path);
  } catch {
    // A missing path reads no package below.
  }
  const pkg = readPackage(path);
  return pkg?.name === providerPackage && exportsEntry(pkg, exported);
}

/** Whether a provider block's npm names this adopter: the package, or a
 * local entry file whose package.json names it. */
export function compatibleProvider(name) {
  if (packageSpecifier(name)) return true;
  const entry = localSource(name);
  return entry !== null && readPackage(dirname(entry))?.name === providerPackage;
}

/** The provider module for a plugin registration: the package itself, or the
 * local package's index.js as a file URL. */
function providerSpecifier(registration) {
  if (packageSpecifier(registration)) return registration;
  let path = registration;
  if (registration.startsWith('file://')) {
    path = localSource(registration);
    if (!path) return providerPackage;
  }
  try {
    if (!statSync(path).isDirectory()) path = dirname(path);
  } catch {
    // The registration was verified when it was written.
  }
  return fileURL(join(path, 'index.js'));
}

/** The plugin entry and provider module for --adapter. A source adapter is
 * accepted only when its package identity and server export both match. */
export function adapter(value) {
  if (packageSpecifier(value)) return {pluginEntry: value, providerPackage: value};
  if (typeof value !== 'string' || value.includes('://') || !absoluteLocal(value)) {
    throw mistake(`--adapter must be ${JSON.stringify(providerPackage)} or an absolute OpenAbstractions adapter package directory`);
  }
  const directory = normalize(value).replace(/[\\/]+$/, '');
  const pkg = readPackage(directory);
  if (pkg === null && !existsSync(join(directory, 'package.json'))) throw mistake(`--adapter package cannot be read: ${directory}`);
  if (pkg?.name !== providerPackage || !exportsEntry(pkg, './server')) {
    throw mistake(`--adapter must identify ${providerPackage} and export ./server`);
  }
  return {pluginEntry: directory, providerPackage: fileURL(join(directory, 'index.js'))};
}

/** One plugin list entry: a name, or a [name, options] tuple. */
export function pluginEntry(entry) {
  if (typeof entry === 'string' && entry) return {name: entry, options: null};
  if (!Array.isArray(entry) || entry.length !== 2) throw new Error('expected a plugin string or [plugin, options] tuple');
  if (typeof entry[0] !== 'string' || !entry[0]) throw new Error('tuple plugin name must be a nonempty string');
  if (!isObject(entry[1])) throw new Error('tuple plugin options must be an object');
  return {name: entry[0], options: entry[1]};
}

function pluginList(doc) {
  if (!Object.hasOwn(doc, 'plugin') || doc.plugin === null) return [];
  if (!Array.isArray(doc.plugin)) throw new Error('the existing "plugin" key is not a plugin array');
  return [...doc.plugin];
}

/** Whether the adapter source opencode.json names ships the TUI module: the
 * unpinned package, a pin at or after 0.4.0 (prereleases included), or a
 * local package whose package.json exports ./tui. */
export function adapterOffersTUI(source) {
  if (!source || source === providerPackage) return true;
  if (packageSpecifier(source)) {
    const version = source.slice(providerPackage.length + 1).split(/[-+]/)[0];
    return versionAtLeast(version, firstTUIVersion);
  }
  return registeredAdapter(source, './tui');
}

function versionAtLeast(version, floor) {
  const have = version.split('.').map(Number);
  const want = floor.split('.').map(Number);
  if (have.length !== 3 || have.some((n) => !Number.isInteger(n))) return false;
  for (let i = 0; i < 3; i++) if (have[i] !== want[i]) return have[i] > want[i];
  return true;
}

// ---- Merging ----

function objectField(current, key, message) {
  if (!Object.hasOwn(current, key)) return {};
  if (!isObject(current[key])) throw new Error(message);
  return {...current[key]};
}

/** Refresh the entries configure owns in one provider block while keeping
 * credentials, options, provider extensions, and model entries a person has
 * edited or added. An absent or malformed marker never claims a model. */
export function mergeProvider(existing, provider) {
  let current = {};
  if (existing !== undefined) {
    if (!isObject(existing)) throw new Error('the existing OpenAbstractions provider is not an object');
    current = {...existing};
    if (typeof current.npm !== 'string' || !compatibleProvider(current.npm)) {
      throw new Error('the OpenAbstractions provider ID is already owned by another package; choose a different --provider-id');
    }
  }
  const oldOptions = objectField(current, 'options', 'the existing OpenAbstractions provider options are not an object');
  const oldModels = objectField(current, 'models', 'the existing OpenAbstractions provider models are not an object');
  const policies = provider.options.selectionPolicies ?? {};
  for (const alias of Object.keys(policies)) {
    if (Object.hasOwn(oldModels, alias)) {
      throw new Error(`the saved preference alias ${JSON.stringify(alias)} conflicts with an existing model; choose another policy name or remove the custom model`);
    }
  }
  const baselines = oldOptions.oaInventoryManagedModels;
  const markerValid = isObject(baselines);
  const models = {};
  for (const [id, value] of Object.entries(oldModels)) {
    if (markerValid && Object.hasOwn(baselines, id) && sameJSON(value, baselines[id])) continue;
    put(models, id, value);
  }
  const managed = {};
  for (const [id, model] of Object.entries(provider.models)) {
    if (Object.hasOwn(models, id)) continue;
    put(models, id, model);
    put(managed, id, model);
  }
  current.models = models;
  // configure's earlier single heading becomes the placement's heading; a
  // name the person chose stays.
  if (current.name === undefined || current.name === null || current.name === displayName) current.name = provider.name;
  if (provider.replacePackage || current.npm === undefined || current.npm === null) current.npm = provider.npm;
  const options = {...oldOptions};
  options.scope = provider.options.scope;
  options.guarantees = provider.options.guarantees;
  options.oaInventoryManagedModels = managed;
  if (Object.keys(policies).length) {
    let selections = {};
    if (Object.hasOwn(oldOptions, 'oaSelectionPolicies')) {
      const prior = oldOptions.oaSelectionPolicies;
      if (!isObject(prior) || !Object.values(prior).every((name) => typeof name === 'string')) {
        throw new Error('the existing oaSelectionPolicies option is not a string map');
      }
      selections = {...prior};
    }
    for (const [alias, name] of Object.entries(policies)) {
      if (Object.hasOwn(selections, alias) && selections[alias] !== name) {
        throw new Error(`the preference alias ${JSON.stringify(alias)} already names a different policy`);
      }
      put(selections, alias, name);
    }
    options.oaSelectionPolicies = selections;
  }
  if (provider.options.runtimeEndpoint) options.runtimeEndpoint = provider.options.runtimeEndpoint;
  if (provider.options.placement) options.oaPlacement = provider.options.placement;
  if (provider.options.inventory !== undefined) options.oaInventory = provider.options.inventory;
  current.options = options;
  return current;
}

/** Apply the provider block, the plugin registration, and a default "model"
 * when the document names none, leaving every other key untouched. Returns
 * the merged document and the adapter source registered, which the TUI
 * registration follows. */
export function mergeConfig(existing, providerID, provider, defaultModel, requested = providerPackage) {
  const doc = parseObject(existing);
  requested ||= providerPackage;
  const plugins = pluginList(doc);
  let registration = requested;
  let registered = false;
  for (let i = 0; i < plugins.length; i++) {
    let entry;
    try {
      entry = pluginEntry(plugins[i]);
    } catch (error) {
      throw new Error(`the existing "plugin" entry ${i} is invalid: ${error.message}`);
    }
    if (!registeredAdapter(entry.name, './server')) continue;
    // An explicit local source is a deliberate development override; an
    // existing OA version pin or verified local source otherwise stays.
    registration = packageSpecifier(requested) ? entry.name : requested;
    const options = {...(entry.options ?? {})};
    options.providerId = providerID;
    plugins[i] = [registration, options];
    registered = true;
    break;
  }
  const block = {...provider, npm: providerSpecifier(registration), replacePackage: !packageSpecifier(requested)};
  const providers = Object.hasOwn(doc, 'provider') ? doc.provider : {};
  if (!isObject(providers)) throw new Error('the existing "provider" key is not a JSON object');
  put(providers, providerID, mergeProvider(Object.hasOwn(providers, providerID) ? providers[providerID] : undefined, block));
  doc.provider = providers;
  if (!registered) plugins.push([registration, {providerId: providerID}]);
  doc.plugin = plugins;
  if (!Object.hasOwn(doc, 'model')) doc.model = defaultModel;
  return {doc, registration};
}

/** The model a config selects under providerID, or ''. */
export function selectedModel(existing, providerID) {
  let doc;
  try {
    doc = parseObject(existing);
  } catch {
    return '';
  }
  const model = doc.model;
  if (typeof model === 'string') return model.startsWith(`${providerID}/`) ? model.slice(providerID.length + 1) : '';
  if (isObject(model) && model.providerID === providerID && typeof model.model === 'string') return model.model;
  return '';
}

/** The hosted heading when there are hosted models, and the saved-preferences
 * heading when a selection policy is named, beside the main provider and with
 * its package. */
function extraProviders(doc, app) {
  if (!Object.keys(app.hostedModels ?? {}).length && !app.selectionPolicy) return;
  const providers = doc.provider;
  const npm = providers[app.providerID]?.npm;
  if (typeof npm !== 'string' || !npm) throw new Error('the OpenAbstractions provider block names no package');
  const existing = (id) => (Object.hasOwn(providers, id) ? providers[id] : undefined);
  if (Object.keys(app.hostedModels ?? {}).length) {
    const id = app.providerID + hostedSuffix;
    try {
      put(providers, id, mergeProvider(existing(id), {name: headings.hosted, npm, models: app.hostedModels, options: {
        runtimeEndpoint: app.runtimeEndpoint, scope: 'remote', guarantees: [guaranteeFor('remote')], placement: 'hosted',
      }}));
    } catch (error) {
      throw new Error(`hosted heading: ${error.message}`);
    }
  }
  if (app.selectionPolicy) {
    const id = app.providerID + preferencesSuffix;
    try {
      put(providers, id, mergeProvider(existing(id), {name: headings.preferences, npm, models: {}, options: {
        runtimeEndpoint: app.runtimeEndpoint, scope: app.scope, guarantees: [app.guarantee], inventory: false,
        selectionPolicies: {[selectionAlias(app.selectionPolicy)]: app.selectionPolicy},
      }}));
    } catch (error) {
      throw new Error(`saved preferences heading: ${error.message}`);
    }
  }
}

export function selectionAlias(name) {
  return `oa-preference-${name}`;
}

/** The object at path, creating missing ones when create is set; null when
 * one is missing and create is not. A non-object on the way is the person's
 * own setting and an error. */
function objectAt(doc, create, ...path) {
  let current = doc;
  for (let i = 0; i < path.length; i++) {
    const name = path[i];
    const next = Object.hasOwn(current, name) ? current[name] : undefined;
    if (next === undefined || next === null) {
      if (!create) return null;
      const made = {};
      put(current, name, made);
      current = made;
      continue;
    }
    if (!isObject(next)) throw new Error(`${path.slice(0, i + 1).join('.')} is not an object`);
    current = next;
  }
  return current;
}

/** Hide or show OpenCode's own free cloud models through disabled_providers,
 * keeping every other entry. Showing removes "opencode" only when configure
 * added it; a person's own entry stays. Returns the note configure prints. */
export function applyZen(doc, providerID, zen) {
  if (!zen) return '';
  const options = objectAt(doc, true, 'provider', providerID, 'options');
  const present = Object.hasOwn(doc, 'disabled_providers');
  const disabled = present && Array.isArray(doc.disabled_providers) ? doc.disabled_providers : [];
  if (present && !Array.isArray(doc.disabled_providers)) return 'keeps your own disabled_providers, which is not a list';
  const listed = disabled.includes(zenProvider);
  const added = options[zenMarker] === 'hide';
  if (zen === 'hide') {
    if (!listed) {
      doc.disabled_providers = [...disabled, zenProvider];
      options[zenMarker] = 'hide';
    }
    return 'OpenCode\'s own free cloud models are hidden; --opencode-zen show lists them (your code is sent to their servers)';
  }
  if (zen !== 'show') throw mistake('--opencode-zen must be hide or show');
  if (listed && added) {
    const kept = disabled.filter((item) => item !== zenProvider);
    if (kept.length) doc.disabled_providers = kept;
    else delete doc.disabled_providers;
    delete options[zenMarker];
    return 'OpenCode\'s own free cloud models are listed; choosing one sends your code to their servers';
  }
  if (listed) return 'keeps your own disabled_providers entry for OpenCode\'s free cloud models';
  delete options[zenMarker];
  return 'OpenCode\'s own free cloud models are listed; choosing one sends your code to their servers';
}

// The local agent follows the default "model", which --model sets, so
// switching agents never switches models silently. Retired settings are ones
// an earlier local profile wrote and this one no longer does; a run removes
// each that still holds its value.
const retiredSettings = [`agent.${localAgent}.model`];

function localSettings() {
  const agent = (...field) => ['agent', localAgent, ...field];
  return [
    [agent('mode'), 'primary'],
    [agent('description'), localAgentDescription],
    [agent('prompt'), localBuildPrompt],
    [agent('color'), 'success'],
    [agent('permission', 'task'), 'deny'],
    // OpenCode denies question to an agent it does not ship; build allows it.
    [agent('permission', 'question'), 'allow'],
    [['default_agent'], localAgent],
    [['agent', 'title', 'disable'], true],
    [['plugin', 'compactTools'], true],
  ];
}

/** The options object of the plugin entry bound to providerID; null when the
 * config has no such tuple. */
function pluginOptions(doc, providerID) {
  for (const entry of Array.isArray(doc.plugin) ? doc.plugin : []) {
    if (Array.isArray(entry) && entry.length === 2 && isObject(entry[1]) && entry[1].providerId === providerID) return entry[1];
  }
  return null;
}

function settingParent(doc, plugin, path, create) {
  const name = path[path.length - 1];
  if (path[0] === 'plugin') return {parent: plugin, name};
  return {parent: objectAt(doc, create, ...path.slice(0, -1)), name};
}

function pruneEmpty(doc, ...path) {
  let parent;
  try {
    parent = objectAt(doc, false, ...path.slice(0, -1));
  } catch {
    return;
  }
  const name = path[path.length - 1];
  if (parent && isObject(parent[name]) && Object.keys(parent[name]).length === 0) delete parent[name];
}

/** Write or remove the local profile. local sets each setting a person has
 * not set, replaces one that still holds the value the profile last wrote,
 * and keeps one a person changed, naming it in the notes; default removes
 * each setting that still holds the value the profile wrote. Every other key
 * stays, OpenCode's build and plan agents included. Returns the notes. */
export function applyProfile(doc, providerID, profile) {
  if (!profile) return [];
  const options = objectAt(doc, true, 'provider', providerID, 'options');
  const plugin = pluginOptions(doc, providerID);
  const marker = options[profileMarker];
  const record = isObject(marker) && isObject(marker.wrote) ? marker.wrote : {};
  const notes = [];
  if (profile === 'local') {
    const wrote = {};
    for (const key of retiredSettings) {
      if (!Object.hasOwn(record, key)) continue;
      try {
        const {parent, name} = settingParent(doc, plugin, key.split('.'), false);
        if (parent && Object.hasOwn(parent, name) && sameJSON(parent[name], record[key])) delete parent[name];
      } catch {
        // A person's own non-object setting stays.
      }
    }
    for (const [path, value] of localSettings()) {
      const key = path.join('.');
      let parent;
      let name;
      try {
        ({parent, name} = settingParent(doc, plugin, path, true));
      } catch {
        parent = null;
      }
      if (!parent) {
        notes.push(`keeps ${key} as you set it`);
        continue;
      }
      const has = Object.hasOwn(parent, name);
      if (!has || (Object.hasOwn(record, key) && sameJSON(parent[name], record[key]))) {
        put(parent, name, value);
        put(wrote, key, value);
      } else if (sameJSON(parent[name], value)) {
        put(wrote, key, value);
      } else {
        notes.push(`keeps your own ${key}`);
      }
    }
    options[profileMarker] = {name: 'local', wrote};
  } else if (profile === 'default') {
    for (const [key, value] of Object.entries(record)) {
      let parent;
      let name;
      try {
        ({parent, name} = settingParent(doc, plugin, key.split('.'), false));
      } catch {
        continue;
      }
      if (!parent || !Object.hasOwn(parent, name)) continue;
      if (sameJSON(parent[name], value)) delete parent[name];
      else notes.push(`keeps your own ${key}`);
    }
    pruneEmpty(doc, 'agent', localAgent, 'permission');
    pruneEmpty(doc, 'agent', localAgent);
    pruneEmpty(doc, 'agent', 'title');
    pruneEmpty(doc, 'agent');
    delete options[profileMarker];
  } else {
    throw mistake('--profile must be local or default');
  }
  return notes;
}

/** The profile configure applies: the one named, else for a chosen model the
 * local profile with --scope local and the default one otherwise; without a
 * model or a profile, none. */
export function profileFor(profile, model, scope) {
  if (profile || !model) return profile;
  return scope === defaultScope ? 'local' : 'default';
}

/** Register the adapter in tui.json's "plugin" list, the only list OpenCode's
 * TUI loads plugins from, keeping every other key and plugin. An existing OA
 * entry takes the source opencode.json names and keeps its options. An entry
 * naming the OA package with a non-version source is a foreign package under
 * the OA name and refused. */
export function mergeTUIConfig(existing, registration = providerPackage) {
  registration ||= providerPackage;
  let doc;
  try {
    doc = parseObject(existing);
  } catch {
    throw new Error('not a JSON object');
  }
  const plugins = pluginList(doc);
  let registered = false;
  for (let i = 0; i < plugins.length; i++) {
    let entry;
    try {
      entry = pluginEntry(plugins[i]);
    } catch (error) {
      throw new Error(`the existing "plugin" entry ${i} is invalid: ${error.message}`);
    }
    if (entry.name.startsWith(`${providerPackage}@`) && !packageSpecifier(entry.name)) {
      throw new Error(`the plugin ${JSON.stringify(entry.name)} names ${providerPackage} from a source other than a version; remove it, then retry`);
    }
    if (!registeredAdapter(entry.name, './tui') || registered) continue;
    registered = true;
    if (entry.name === registration) continue;
    plugins[i] = entry.options === null ? registration : [registration, entry.options];
  }
  if (!registered) plugins.push(registration);
  doc.plugin = plugins;
  return render(doc);
}

// ---- Files ----

/** path, or the file a symbolic link at path names: configure reads and
 * replaces that file, which keeps the link in place. */
function resolveLink(path) {
  let info;
  try {
    info = lstatSync(path);
  } catch {
    return path;
  }
  if (!info.isSymbolicLink()) return path;
  try {
    return realpathSync(path);
  } catch (error) {
    throw new Error(`resolve the link ${path}: ${error.message}`);
  }
}

function readOrNull(path) {
  try {
    return readFileSync(path);
  } catch (error) {
    if (error.code === 'ENOENT') return null;
    throw error;
  }
}

/** A config document through its link target; null when absent. */
export function readConfig(path) {
  return readOrNull(resolveLink(path));
}

const sameBytes = (left, right) => (left === null) === (right === null) && (left === null || left.equals(right));

function pause(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

function alive(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error.code === 'EPERM';
  }
}

/** The lock folder: one small file per config path, named by its digest, in
 * this account's temporary folder, never in OpenCode's config folder. */
export const lockFolder = () => join(tmpdir(), 'openabstractions-opencode-configure');

/** Hold the lock for target: an exclusively created file naming this process.
 * A lock whose process is gone is taken over. The lock is named by target's
 * folder as the file system spells it, and target's own name. */
function lock(target) {
  const folder = lockFolder();
  mkdirSync(folder, {recursive: true});
  const key = join(realpathSync.native(dirname(target)), basename(target));
  const file = join(folder, `${createHash('sha256').update(key).digest('hex').slice(0, 32)}.lock`);
  const deadline = Date.now() + 10000;
  for (;;) {
    try {
      const fd = openSync(file, 'wx', 0o600);
      try {
        writeSync(fd, String(process.pid));
      } finally {
        closeSync(fd);
      }
      return () => {
        try {
          unlinkSync(file);
        } catch {
          // A lock file left behind is taken over by the next run.
        }
      };
    } catch (error) {
      if (error.code !== 'EEXIST') throw error;
    }
    let holder = NaN;
    let age = 0;
    try {
      holder = Number(readFileSync(file, 'utf8'));
      age = Date.now() - statSync(file).mtimeMs;
    } catch {
      continue;
    }
    // A holder that has not written its process yet is alive for a moment.
    const stale = Number.isSafeInteger(holder) && holder > 0 ? !alive(holder) : age > 5000;
    if (stale) {
      try {
        unlinkSync(file);
      } catch {
        // Another run took it over first.
      }
      continue;
    }
    if (Date.now() > deadline) throw new Error(`another configure run is writing ${target}; retry when it ends`);
    pause(50);
  }
}

// A staged replacement is <file>.openabstractions-<hex>.tmp: a name only
// configure.js gives, so the sweep never touches a person's own .tmp file.
const stagedMarker = 'openabstractions-';
const stagedPattern = (base) => new RegExp(`^${base.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\.${stagedMarker}[0-9a-f]+\\.tmp$`);

/** Remove the staged files a killed run left beside target. It runs under the
 * lock, and a live run stages under the lock, so every one found is dead. */
function sweep(target) {
  const pattern = stagedPattern(basename(target));
  let names = [];
  try {
    names = readdirSync(dirname(target));
  } catch {
    return;
  }
  for (const name of names) {
    if (!pattern.test(name)) continue;
    try {
      unlinkSync(join(dirname(target), name));
    } catch {
      // A file another process holds stays until the next run.
    }
  }
}

function renameOver(from, to) {
  for (let attempt = 0; ; attempt++) {
    try {
      renameSync(from, to);
      return;
    } catch (error) {
      // Windows denies a rename for an instant while a reader holds the file.
      if (attempt >= 40 || !['EPERM', 'EACCES', 'EBUSY'].includes(error.code)) throw error;
      pause(25);
    }
  }
}

/** Stage data beside target, flush it and rename it over target: a reader or
 * an interruption sees the whole old or the whole new file. */
function writeWhole(target, data) {
  const staged = join(dirname(target), `${basename(target)}.${stagedMarker}${randomBytes(8).toString('hex')}.tmp`);
  const fd = openSync(staged, 'wx', 0o600);
  try {
    writeSync(fd, data);
    fsyncSync(fd);
  } catch (error) {
    closeSync(fd);
    try {
      unlinkSync(staged);
    } catch {
      // Swept by the next run.
    }
    throw error;
  }
  closeSync(fd);
  try {
    renameOver(staged, target);
  } catch (error) {
    try {
      unlinkSync(staged);
    } catch {
      // Swept by the next run.
    }
    throw error;
  }
}

/** Replace the config document at path, which held previous when configure
 * read it (null when absent), with data. It first keeps previous as
 * <file>.openabstractions.bak, OA's own backup name, so a person's <file>.bak
 * is never touched; a file changed since configure read it is refused. */
export function replaceConfig(path, previous, data) {
  const target = resolveLink(path);
  mkdirSync(dirname(target), {recursive: true});
  const release = lock(target);
  try {
    sweep(target);
    if (previous !== null && previous.toString('utf8').trim() !== '') {
      const backup = `${target}.openabstractions.bak`;
      const current = readOrNull(backup);
      if (current === null || !current.equals(previous)) {
        sweep(backup);
        writeWhole(backup, previous);
      }
    }
    if (!sameBytes(readOrNull(target), previous)) throw new Error(`${target} changed while configure ran; run configure again`);
    writeWhole(target, Buffer.from(data, 'utf8'));
  } finally {
    release();
  }
}

export function defaultConfigPath() {
  return join(homedir(), '.config', 'opencode', 'opencode.json');
}

// ---- Applying ----

function contextWarning(app) {
  const families = Object.keys(app.unusableModels).sort();
  return `skipped model(s) with reported context no larger than the ${outputBudget}-token output limit: ` +
    `${families.map((family) => `${family}=${app.unusableModels[family]} tokens`).join(', ')}; ` +
    'choose another model or adjust the host context or output budget before configuring';
}

function heading(scope) {
  return scope === defaultScope ? headings.local : displayName;
}

/** Print why when there is nothing to write, print the merged config for
 * --dry-run, or merge it into the config file at app.path and report what
 * changed. It never opens a file for --dry-run and never writes a file
 * byte-identical to what is already there. io.out and io.err take text;
 * deps.replace stands in for replaceConfig in tests. */
export function apply(app, io, deps = {}) {
  const replace = deps.replace ?? replaceConfig;
  const unusable = app.unusableModels ?? {};
  app = {...app, unusableModels: unusable, profile: app.profile ?? '', zen: app.zen ?? ''};
  for (const warning of app.inventoryWarnings ?? []) io.err(`${app.command}: ${warning}\n`);
  const families = Object.keys(app.models).sort();
  if (!families.length && Object.keys(unusable).length) throw new ConfigureError(3, contextWarning(app));
  if (!families.length) {
    io.out(`${app.command}: no servable chat model on this machine; add a model server (openabstractions inference server add) ` +
      'and grant this program abstraction.router/inventory.read and abstraction.inference/complete, then retry\n');
    return;
  }
  const requested = app.pluginEntry || providerPackage;
  const provider = {name: heading(app.scope), npm: app.providerPackage || providerPackage, models: app.models,
    options: {runtimeEndpoint: app.runtimeEndpoint, scope: app.scope, guarantees: [app.guarantee]}};
  if (Object.keys(unusable).length) io.err(`${app.command}: ${contextWarning(app)}\n`);

  let existing;
  try {
    existing = readConfig(app.path);
  } catch (error) {
    throw mistake(`read ${app.path}: ${error.message}`);
  }
  const selected = selectedModel(existing, app.providerID);
  if (selected && !Object.hasOwn(app.models, selected) && Object.hasOwn(unusable, selected)) throw new ConfigureError(3, contextWarning(app));
  let defaultModel = `${app.providerID}/${defaultChatFamily(app.models)}`;
  if (app.chosenModel) {
    if (!Object.hasOwn(app.models, app.chosenModel)) {
      throw new ConfigureError(4, `--model ${JSON.stringify(app.chosenModel)} is not a servable chat model on this machine now; list them with configure.js models`);
    }
    defaultModel = `${app.providerID}/${app.chosenModel}`;
  }
  let merged;
  let source;
  let zenNote;
  let profileNotes;
  try {
    ({doc: merged, registration: source} = mergeConfig(existing, app.providerID, provider, defaultModel, requested));
    if (app.chosenModel) merged.model = defaultModel;
    extraProviders(merged, app);
    zenNote = applyZen(merged, app.providerID, app.zen);
    profileNotes = applyProfile(merged, app.providerID, app.profile);
  } catch (error) {
    if (error instanceof ConfigureError) throw error;
    throw mistake(`${app.path}: ${error.message}`);
  }
  const text = render(merged);

  // The TUI registration follows the adapter source opencode.json now names,
  // and is planned before either file is written, so a refused tui.json
  // leaves opencode.json untouched too.
  const tuiPath = join(dirname(app.path), 'tui.json');
  const tuiJSONC = join(dirname(app.path), 'tui.jsonc');
  let tuiExisting = null;
  let tuiText = null;
  let handEdit = '';
  if (adapterOffersTUI(source)) {
    try {
      tuiExisting = readConfig(tuiPath);
    } catch (error) {
      throw mistake(`read ${tuiPath}: ${error.message}`);
    }
    if (tuiExisting === null && existsSync(tuiJSONC)) {
      // configure edits no JSONC file, since it would lose its comments; the
      // person adds the entry instead.
      handEdit = `${app.command}: ${tuiJSONC}: add ${JSON.stringify(source)} to its "plugin" list to enable Ctrl+Alt+M dictation; ` +
        'configure does not edit JSONC files, whose comments it would lose\n';
    } else {
      try {
        tuiText = mergeTUIConfig(tuiExisting, source);
      } catch (error) {
        throw mistake(`${tuiPath}: ${error.message}`);
      }
    }
  }
  if (zenNote && !app.dryRun) io.out(`${app.command}: ${zenNote}\n`);
  for (const note of profileNotes) io.err(`${app.command}: the ${app.profile} profile ${note}\n`);
  if (app.dryRun) {
    io.out(text);
    return;
  }
  if (app.profile === 'local') {
    io.out(`${app.command}: local profile: the Local assistant agent for faster local answers, the default for new sessions. ${localSwitch}\n`);
  } else if (app.profile === 'default') {
    io.out(`${app.command}: default profile: OpenCode's own agents, title request and tool text\n`);
  }
  if (handEdit) io.err(handEdit);
  const trimmed = (buffer) => (buffer === null ? '' : buffer.toString('utf8').trim());
  if (trimmed(existing) === text.trim()) {
    io.out(`${app.command}: ${app.path} already carries this provider block; unchanged\n`);
  } else {
    try {
      replace(app.path, existing, text);
    } catch (error) {
      throw mistake(error.message);
    }
    io.out(`${app.command}: configured provider ${JSON.stringify(app.providerID)} and startup adapter in ${app.path}\n`);
  }
  if (tuiText === null) return;
  if (trimmed(tuiExisting) === tuiText.trim()) {
    io.out(`${app.command}: ${tuiPath} already registers the TUI module; unchanged\n`);
    return;
  }
  try {
    replace(tuiPath, tuiExisting, tuiText);
  } catch (error) {
    throw mistake(error.message);
  }
  io.out(`${app.command}: registered the TUI module (Ctrl+Alt+M dictation) in ${tuiPath}\n`);
}

// ---- Command line ----

const valueFlags = new Set(['config', 'provider-id', 'scope', 'adapter', 'selection-policy', 'model', 'profile', 'opencode-zen',
  'endpoint', 'timeout']);
const boolFlags = new Set(['dry-run', 'json']);

function parseFlags(command, args, allowed) {
  const flags = {};
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (!arg.startsWith('--')) throw mistake('takes no arguments');
    const [name, inline] = arg.slice(2).split(/=(.*)/s, 2);
    if (!allowed.has(name)) throw mistake(`flag provided but not defined: --${name}`);
    if (boolFlags.has(name)) {
      if (inline !== undefined && inline !== 'true' && inline !== 'false') throw mistake(`--${name} takes no value`);
      flags[name] = inline !== 'false';
      continue;
    }
    if (inline !== undefined) flags[name] = inline;
    else if (i + 1 < args.length) flags[name] = args[++i];
    else throw mistake(`flag needs an argument: --${name}`);
  }
  return flags;
}

/** A duration such as 20s, 1m30s or 500ms, in milliseconds. */
export function parseDuration(text) {
  if (/^-/.test(text)) throw mistake('--timeout must not be negative');
  const parts = [...text.matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h)/g)];
  if (!parts.length || parts.map((part) => part[0]).join('') !== text) throw mistake(`--timeout ${JSON.stringify(text)} is not a duration such as 20s`);
  const unit = {ms: 1, s: 1000, m: 60000, h: 3600000};
  return parts.reduce((total, [, amount, name]) => total + Number(amount) * unit[name], 0);
}

function runtimeOptions(flags, deps) {
  return {endpoint: flags.endpoint || null, timeout: flags.timeout === undefined ? 10000 : parseDuration(flags.timeout),
    resolveRouter: deps.resolveRouter};
}

async function modelsCommand(args, io, deps) {
  const flags = parseFlags('models', args, new Set(['scope', 'endpoint', 'timeout', 'json']));
  const scope = flags.scope ?? defaultScope;
  guaranteeFor(scope);
  const options = runtimeOptions(flags, deps);
  const {models, warnings} = choices(await offersAt(options, placementFor(scope)));
  if (flags.json) {
    io.out(`${JSON.stringify({models, warnings})}\n`);
    return;
  }
  for (const warning of warnings) io.err(`models: ${warning}\n`);
  if (!models.length) {
    io.out('models: no servable chat model on this machine\n');
    return;
  }
  for (const choice of models) io.out(`${choice.family}\t${choice.name}\tcontext ${choice.context}\ton ${choice.servers.join(', ')}\n`);
}

async function configureCommand(args, io, deps) {
  const command = 'configure';
  const flags = parseFlags(command, args, new Set([...valueFlags, 'dry-run']));
  const providerID = flags['provider-id'] ?? defaultProviderID;
  const scope = flags.scope ?? defaultScope;
  const chosen = flags.model ?? '';
  let profile = flags.profile ?? '';
  const zen = flags['opencode-zen'] ?? 'hide';
  const selectionPolicy = flags['selection-policy'] ?? '';
  const options = runtimeOptions(flags, deps);
  if (chosen && (chosen.length > 256 || /[\r\n\t"]/.test(chosen))) throw mistake('--model must name one model family');
  if (!['', 'local', 'default'].includes(profile)) throw mistake('--profile must be local or default');
  if (zen !== 'hide' && zen !== 'show') throw mistake('--opencode-zen must be hide or show');
  profile = profileFor(profile, chosen, scope);
  if (!providerIDPattern.test(providerID)) throw mistake('--provider-id must be a lowercase identifier starting with a letter');
  if (selectionPolicy && !selectionPolicyPattern.test(selectionPolicy)) {
    throw mistake('--selection-policy must be a lowercase name containing letters, numbers, hyphens or underscores');
  }
  const {pluginEntry: entry, providerPackage: npm} = adapter(flags.adapter ?? providerPackage);
  const guarantee = guaranteeFor(scope);
  const path = flags.config || defaultConfigPath();
  // A .jsonc spelled another way and holding comments fails to parse below,
  // which refuses it too.
  if (['.jsonc', '.JSONC'].includes(extname(path))) {
    throw new ConfigureError(3, `jsonc_unsupported: refuses ${path}: this command cannot preserve JSONC comments; keep an opencode.json, or add the provider block to it by hand`);
  }
  const offers = await offersAt(options, placementFor(scope));
  // With --scope local, models on hosted servers (those a credential makes
  // servable) get their own heading in OpenCode's picker.
  let hostedModels = {};
  if (scope === defaultScope) hostedModels = project(await offersAt(options, 'hosted'), () => false).models;
  let isLocal = () => true;
  // --scope remote offers hosted servers too: each model's place comes from
  // the router's local-placement offers.
  if (scope !== defaultScope) isLocal = localServers(await offersAt(options, 'local'));
  const {models, unusable, warnings} = project(offers, isLocal);
  if (selectionPolicy && Object.hasOwn(models, selectionAlias(selectionPolicy))) {
    throw mistake(`--selection-policy alias ${JSON.stringify(selectionAlias(selectionPolicy))} conflicts with a discovered model family`);
  }
  apply({command, path, providerID, scope, guarantee, pluginEntry: entry, providerPackage: npm, runtimeEndpoint: options.endpoint ?? '',
    selectionPolicy, chosenModel: chosen, profile, zen, hostedModels, dryRun: Boolean(flags['dry-run']), models,
    unusableModels: unusable, inventoryWarnings: warnings}, io, deps);
}

/** Run one command line; returns the exit code. deps.resolveRouter stands in
 * for the runtime in tests. */
export async function main(args, io = {out: (text) => process.stdout.write(text), err: (text) => process.stderr.write(text)}, deps = {}) {
  const [command, ...rest] = args;
  if (!command || ['-h', '--help', 'help'].includes(command) || rest.some((arg) => arg === '-h' || arg === '--help')) {
    io.out(usage);
    return 0;
  }
  try {
    if (command === 'configure') await configureCommand(rest, io, deps);
    else if (command === 'models') await modelsCommand(rest, io, deps);
    else throw mistake(`no command called ${JSON.stringify(command)}; run configure.js --help`);
    return 0;
  } catch (error) {
    if (!(error instanceof ConfigureError)) throw error;
    const label = ['configure', 'models'].includes(command) ? command : 'configure.js';
    io.err(`${label}: ${error.message}\n`);
    if (error.exitCode === 2) io.err('run configure.js --help for usage\n');
    return error.exitCode;
  }
}

const invoked = (() => {
  if (typeof import.meta.main === 'boolean') return import.meta.main;
  try {
    return Boolean(process.argv[1]) && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
})();

if (invoked) process.exitCode = await main(process.argv.slice(2));
