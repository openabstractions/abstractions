// The Local assistant's prefix preload (inference preload@1, INF-PL1..INF-PL4;
// modelhost MODELHOST-W1..W3).
//
// A Local assistant conversation opens with the same system message and tool
// definitions every time in one project on one day. The provider keeps the
// last session start's request without its user message here, in OpenCode's
// own data folder, as this application's own data. When OpenCode starts, the
// plugin sends it to OA as preload@1, so the model is loaded and has
// evaluated that prefix before the person's first message. OA keeps nothing
// of it. The stored prefix is the bytes OpenCode itself last sent, so a new
// OpenCode version, agent or project is stored again by its first session
// start; until then a preload of the older prefix shares only what still
// matches.
import {createHash} from 'node:crypto';
import {mkdir, readdir, readFile, rename, rm, stat, writeFile} from 'node:fs/promises';
import {homedir} from 'node:os';
import {join} from 'node:path';

export const preloadContract = 'abstraction.inference/preload@1';

/** Retention of kept prefixes: the most recent project and model pairs, and
 * none older than keptDays. */
export const keptPrefixes = 20;
export const keptDays = 30;

/** The folder this plugin keeps prefixes in: OpenCode's data folder
 * ($XDG_DATA_HOME/opencode, else ~/.local/share/opencode, as OpenCode's own
 * Global.Path.data reads it), under openabstractions/prefix. */
export function prefixStateDir(env = process.env) {
  const data = env.XDG_DATA_HOME && env.XDG_DATA_HOME.trim() ? env.XDG_DATA_HOME : join(homedir(), '.local', 'share');
  return join(data, 'opencode', 'openabstractions', 'prefix');
}

/** The prefix of an OA chat@1 request that opens a conversation: its system
 * messages and then exactly one user message. The prefix is the request
 * without that user message; any other request has none. */
export function sessionStartPrefix(request) {
  const messages = request?.messages;
  if (!Array.isArray(messages) || messages.length < 2) return null;
  const last = messages[messages.length - 1];
  if (last?.role !== 'user' || messages.slice(0, -1).some((message) => message?.role !== 'system')) return null;
  return {...request, messages: messages.slice(0, -1)};
}

function fileFor(dir, project, model) {
  const key = createHash('sha256').update(`${project}\u0000${model}`).digest('hex').slice(0, 32);
  return join(dir, `${key}.json`);
}

const encodeValue = (_key, value) => (typeof value === 'bigint' ? {$bigint: value.toString()} : value);
const decodeValue = (_key, value) => (value && typeof value === 'object' && typeof value.$bigint === 'string'
  && Object.keys(value).length === 1 ? BigInt(value.$bigint) : value);

/** Keeps request's prefix for project and model when it opens a
 * conversation; resolves false when it has none. The file is replaced in one
 * rename, so a reader never sees half of it. */
export async function rememberSessionStart({dir = prefixStateDir(), project, model, request}) {
  const prefix = sessionStartPrefix(request);
  if (!prefix || typeof project !== 'string' || !project || typeof model !== 'string' || !model) return false;
  await mkdir(dir, {recursive: true});
  const file = fileFor(dir, project, model);
  const temporary = `${file}.${process.pid}.${Date.now()}.tmp`;
  try {
    await writeFile(temporary, JSON.stringify({project, model, request: prefix}, encodeValue), {mode: 0o600});
    await rename(temporary, file);
  } catch (error) {
    await rm(temporary, {force: true});
    throw error;
  }
  await pruneKept(dir);
  return true;
}

/** Removes kept prefixes past retention: every one older than keptDays,
 * and all but the keptPrefixes most recently written. A file another
 * OpenCode removes first is skipped. */
export async function pruneKept(dir, now = Date.now()) {
  const kept = [];
  for (const name of await readdir(dir)) {
    if (!/^[0-9a-f]{32}\.json$/.test(name)) continue;
    try {
      kept.push({file: join(dir, name), written: (await stat(join(dir, name))).mtimeMs});
    } catch {
      // Removed meanwhile.
    }
  }
  kept.sort((a, b) => b.written - a.written);
  const oldest = now - keptDays * 24 * 60 * 60 * 1000;
  for (const [index, entry] of kept.entries()) {
    if (index >= keptPrefixes || entry.written < oldest) await rm(entry.file, {force: true});
  }
}

/** The prefix last kept for project and model, or null. */
export async function recallPrefix({dir = prefixStateDir(), project, model}) {
  if (typeof project !== 'string' || !project || typeof model !== 'string' || !model) return null;
  let stored;
  try {
    stored = JSON.parse(await readFile(fileFor(dir, project, model), 'utf8'), decodeValue);
  } catch {
    return null;
  }
  if (stored?.project !== project || stored?.model !== model || !Array.isArray(stored?.request?.messages)) return null;
  return stored.request;
}

/** Resolves preload@1 on the runtime the provider options name. Loading the
 * model and evaluating the prefix can take tens of seconds; the service's own
 * call budget bounds it. */
export async function resolvePreloader(options, signal) {
  const [{Machine}, {resolvePreload}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/inference'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options?.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(), timeout: 60000, cancellation: signal ?? null,
    server: options?.server ?? null,
  });
  return resolvePreload(machine, {scope: 'local'});
}

/** Sends the prefix kept for project and model as preload@1 and resolves its
 * reply, or null when none is kept (the first conversation ever, or one in a
 * project this OpenCode has not opened). */
export async function preloadKeptPrefix({dir, project, model, resolve = resolvePreloader, options, signal}) {
  const request = await recallPrefix({dir, project, model});
  if (!request) return null;
  const preloader = await resolve(options, signal);
  return preloader.preload(request);
}
