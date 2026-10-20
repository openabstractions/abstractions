// One OpenAbstractions start per OpenCode process. OpenCode creates one
// plugin instance per project directory it opens (OpenCode Desktop opens
// several at once) and runs each instance's config hook before that
// instance's init. The OA answers a start needs are the same for every
// instance of one process, so the instances share them here:
//
// - shared(): one call per owner and key while it runs, and its answer for
//   reuseMs after it settles. A failure is never reused.
// - sharedConnection(): one native Connect or read per process while it runs,
//   cancelled only when every instance waiting on it is gone; a connected
//   answer is reused for reuseMs.
// - createStartupMemory(): the last startup answer per config key, so a
//   config hook applies it at once while the background start checks it.
//   The answer is kept in this process and, for an installed plugin, in
//   OpenCode's data folder for the next process.
import {mkdir, readFile, writeFile} from 'node:fs/promises';
import {homedir} from 'node:os';
import {join} from 'node:path';

const calls = new WeakMap();

/** The answer of call() for owner and key, shared by every caller while it
 * runs and for reuseMs after it succeeds. */
export function shared(owner, key, call, {reuseMs = 0, now = Date.now} = {}) {
  if (!calls.has(owner)) calls.set(owner, new Map());
  const entries = calls.get(owner);
  const entry = entries.get(key);
  if (entry && (!entry.settled || now() - entry.settled < reuseMs)) return entry.promise;
  const next = {settled: 0, promise: null};
  next.promise = Promise.resolve().then(call).then(value => {
    next.settled = Math.max(1, now());
    if (reuseMs <= 0 && entries.get(key) === next) entries.delete(key);
    return value;
  }, error => {
    if (entries.get(key) === next) entries.delete(key);
    throw error;
  });
  entries.set(key, next);
  return next.promise;
}

/** Drops owner's shared answers, so its next call asks again. */
export function forget(owner) {
  calls.delete(owner);
}

const connections = new WeakMap();
const connected = reply => reply?.outcome === 'connected' && reply.hostRegistered === true && reply.helperEnrolled === true;

/** The native connection answer of run(signal) for owner and kind ('connect'
 * or 'read'), shared by every instance that asks while it runs. Its signal
 * aborts once every waiting instance's own signal has aborted. A connected
 * answer from either kind answers both for reuseMs. */
export function sharedConnection(owner, kind, signal, run, {reuseMs = 0, now = Date.now} = {}) {
  if (!connections.has(owner)) connections.set(owner, {connected: null, at: 0, flights: new Map()});
  const state = connections.get(owner);
  if (state.connected && now() - state.at < reuseMs) return Promise.resolve(state.connected);
  let flight = state.flights.get(kind);
  if (!flight) {
    const controller = new AbortController();
    flight = {controller, waiters: 0, promise: null};
    flight.promise = Promise.resolve().then(() => run(controller.signal)).then(reply => {
      if (connected(reply)) { state.connected = reply; state.at = now(); }
      return reply;
    }).finally(() => { if (state.flights.get(kind) === flight) state.flights.delete(kind); });
    state.flights.set(kind, flight);
  }
  const joined = flight;
  joined.waiters += 1;
  let left = false;
  const leave = () => {
    if (left) return;
    left = true;
    joined.waiters -= 1;
    if (joined.waiters === 0 && state.flights.get(kind) === joined) {
      state.flights.delete(kind);
      joined.controller.abort();
    }
  };
  if (signal?.aborted) leave();
  else signal?.addEventListener?.('abort', leave, {once: true});
  return joined.promise.finally(() => signal?.removeEventListener?.('abort', leave));
}

/** The folder an installed plugin keeps its last startup answer in: OpenCode's
 * data folder, as preload.js reads it, under openabstractions. */
export function startupStateDir(env = process.env) {
  const data = env.XDG_DATA_HOME && env.XDG_DATA_HOME.trim() ? env.XDG_DATA_HOME : join(homedir(), '.local', 'share');
  return join(data, 'opencode', 'openabstractions');
}

/** How many config keys the startup file keeps. */
export const keptStartups = 8;
const fileName = 'startup.json';
const encodeValue = (_key, value) => (typeof value === 'bigint' ? {$bigint: value.toString()} : value);
const decodeValue = (_key, value) => (value && typeof value === 'object' && typeof value.$bigint === 'string'
  && Object.keys(value).length === 1 ? BigInt(value.$bigint) : value);

function validAnswer(value) {
  return value && typeof value === 'object' && typeof value.key === 'string'
    && Array.isArray(value.providers) && value.providers.every(entry => Array.isArray(entry) && entry.length === 2
      && typeof entry[0] === 'string' && entry[1] && typeof entry[1] === 'object')
    && Array.isArray(value.capabilities) && value.capabilities.every(name => typeof name === 'string')
    && (value.chat === null || value.chat && typeof value.chat === 'object');
}

/** The last startup answers ({key, providers, capabilities, chat}) by config
 * key. dir null keeps them in this process only. */
export function createStartupMemory({dir = null} = {}) {
  const answers = new Map();
  let loaded = null;
  let writing = Promise.resolve();
  async function load() {
    if (!dir) return;
    try {
      const stored = JSON.parse(await readFile(join(dir, fileName), 'utf8'), decodeValue);
      for (const answer of Array.isArray(stored?.answers) ? stored.answers : []) {
        if (validAnswer(answer) && !answers.has(answer.key)) answers.set(answer.key, answer);
      }
    } catch {
      // A missing or damaged file is a first start.
    }
  }
  // The file is a hint: a torn or damaged file reads as a first start, and
  // the background start writes it again.
  async function save() {
    try {
      await mkdir(dir, {recursive: true});
      await writeFile(join(dir, fileName),
        JSON.stringify({version: 1, answers: [...answers.values()].slice(-keptStartups)}, encodeValue), {mode: 0o600});
    } catch {
      // The next start asks OA in the background as on a first start.
    }
  }
  return {
    /** The last answer for key, or null. Reads the file once per process. */
    async recall(key) {
      loaded ??= load();
      await loaded;
      const answer = answers.get(key);
      return answer ? structuredClone(answer) : null;
    },
    /** The answer kept last for any key, or null: an earlier version's or an
     * earlier config's answer, which a start with none of its own lists until
     * OA answers. */
    async latest() {
      loaded ??= load();
      await loaded;
      const answer = [...answers.values()].pop();
      return answer ? structuredClone(answer) : null;
    },
    /** Keeps answer as the last one for its key. */
    async remember(answer) {
      if (!validAnswer(answer)) return;
      const kept = structuredClone(answer);
      loaded ??= load();
      await loaded;
      answers.delete(kept.key);
      answers.set(kept.key, kept);
      while (answers.size > keptStartups) answers.delete(answers.keys().next().value);
      if (!dir) return;
      writing = writing.then(save);
      await writing;
    },
  };
}
