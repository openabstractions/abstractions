import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, utimesSync, writeFileSync} from 'node:fs';
import {readFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import * as oaAvailability from '../availability.js';
import OpenAbstractionsPlugin from '../plugin.js';
import {createOpenAbstractions} from '../index.js';
import {keptPrefixes, preloadKeptPrefix, pruneKept, recallPrefix, rememberSessionStart, sessionStartPrefix} from '../preload.js';
import {localBuildPrompt} from '../configure.js';

// The plugin keeps the prefix a Local assistant session start sent and, at
// OpenCode's next start, sends it as preload@1; the model host evaluates it,
// and the next session's first request reuses it only when its tokens are
// the same (inference INF-PL1..PL4; abstraction-provider-modelhost CONTRACT.md
// MODELHOST-W1..W3). These tests hold the OA side of that sameness against
// OpenCode 1.18.31's pinned tool text: the plugin's compact tools and the
// provider's request carry nothing that changes between two conversations of
// one project, and the kept prefix is what the next session start opens
// with.

const pinned = new URL('./fixtures/opencode-1.18.31-tool/', import.meta.url);
const hostFixture = new URL('../../../openabstractions-flat/abstraction-provider-modelhost/go/host/testdata/opencode-1.18.31-local.json', import.meta.url);

const optional = () => ({optional() { return this; }});
const fakeTool = Object.assign((definition) => definition, {schema: {
  object: (value) => value, string: optional, number: optional, boolean: optional, enum: optional, array: optional,
}});

// OpenCode 1.18.31's own text for each core tool the Local assistant sends,
// with the shell's placeholders filled as OpenCode fills them on Windows.
function original(id) {
  if (id !== 'bash') return readFileSync(new URL(`${id}.txt`, pinned), 'utf8');
  return readFileSync(new URL('shell/shell.txt', pinned), 'utf8').replace('${os}', 'win32').replace('${shell}', 'powershell')
    .replace('${tmp}', 'C:\\Users\\person\\AppData\\Local\\Temp\\opencode');
}

const coreTools = ['bash', 'edit', 'glob', 'grep', 'question', 'read', 'todowrite', 'webfetch', 'write'];

/** The tools OpenCode passes the provider after the plugin's tool.definition
 * hook, as AI SDK function tools. */
async function localTools() {
  const plugin = await OpenAbstractionsPlugin({client: {}}, {compactTools: true, tools: {}}, {tool: fakeTool});
  const tools = [];
  for (const id of coreTools) {
    const output = {description: original(id), parameters: {}, jsonSchema: undefined};
    await plugin['tool.definition']({toolID: id}, output);
    tools.push({type: 'function', name: id, description: output.description, inputSchema: output.jsonSchema});
  }
  return tools;
}

/** The provider with its package imports replaced; complete records each
 * chat@1 request it is given. */
async function provider(requests) {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine',
    'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
  const chat = {complete: async (request) => {
    requests.push(request);
    return {outcome: 'completed', stopReason: 'end', usage: {}, host: '', model: 'qwen3.6-35b-a3b',
      message: {parts: [{kind: 'text', text: 'ok'}]}};
  }};
  const {createOpenAbstractions} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted,
    oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, class extends Error {}, async () => chat,
    async () => { throw new Error('unexpected chat@2 resolution'); }, class {}, {}, class {}, {Text: 'text', ToolCall: 'tool_call'},
    {Completed: 'completed'}, {LocalOnly: 'local-only'}, {}, {End: 'end'});
  return createOpenAbstractions({connector: {}}).languageModel('qwen3.6-35b-a3b');
}

const fixture = () => JSON.parse(readFileSync(hostFixture, 'utf8'));

test('the model host fixture holds the 1.18.31 tools the plugin makes today', async () => {
  const tools = await localTools();
  const expected = tools.map((tool) => ({name: tool.name, description: tool.description, parameters: JSON.stringify(tool.inputSchema)}));
  assert.deepEqual(fixture().tools, expected,
    'regenerate abstraction-provider-modelhost/go/host/testdata/opencode-1.18.31-local.json from the plugin\'s compact tools');
  const prompt = localBuildPrompt;
  assert.ok(fixture().system[0].startsWith(prompt + '\n'), 'the fixture\'s system text does not open with the Local prompt');
});

test('two Local session starts of one project send the same prefix; only the first words differ', async () => {
  const tools = await localTools();
  const system = fixture().system[0];
  const requests = [];
  const model = await provider(requests);
  const start = (words) => model.doGenerate({tools, prompt: [
    {role: 'system', content: system},
    {role: 'user', content: [{type: 'text', text: words}]},
  ]});
  await start('hi');
  await start('Write a fizzbuzz function in fizzbuzz.py and add a short test.');
  await model.doGenerate({tools, prompt: [
    {role: 'system', content: system},
    {role: 'user', content: [{type: 'text', text: 'hi'}]},
    {role: 'assistant', content: [{type: 'text', text: 'Hello.'}]},
    {role: 'user', content: [{type: 'text', text: 'more'}]},
  ]});
  const [first, second, turn] = requests;
  // The session start's shape: the system message, then exactly one user message.
  assert.deepEqual(first.messages.map((m) => m.role), ['system', 'user']);
  const prefix = (request) => JSON.stringify({model: request.model, tools: request.tools, system: request.messages[0],
    options: {...request.options, maxOutput: String(request.options.maxOutput)}, extensions: request.extensions, guarantees: request.guarantees});
  assert.equal(prefix(second), prefix(first), 'a second conversation changed the prefix the warm learned');
  assert.equal(prefix(turn), prefix(first), 'a later turn changed the prefix');
  assert.notDeepEqual(second.messages[1], first.messages[1]);
  // The tools reach the engine as the plugin left them: each schema is the
  // JSON text the model host's fixture holds, byte for byte.
  assert.deepEqual(first.tools.map((tool) => tool.parameters), fixture().tools.map((tool) => tool.parameters));
});

function tempDir(t) {
  const dir = mkdtempSync(join(tmpdir(), 'oa-prefix-'));
  t.after(() => rmSync(dir, {recursive: true, force: true}));
  return dir;
}

async function until(condition) {
  for (let i = 0; i < 200 && !condition(); i++) await new Promise((resolve) => setTimeout(resolve, 10));
  assert.ok(condition(), 'the condition never held');
}

test('a session start has a prefix; a later turn and a bare question have none', () => {
  const system = {role: 'system', parts: [{kind: 'text', text: 'rules'}]};
  const user = {role: 'user', parts: [{kind: 'text', text: 'hi'}]};
  const assistant = {role: 'assistant', parts: [{kind: 'text', text: 'Hello.'}]};
  assert.deepEqual(sessionStartPrefix({model: 'm', messages: [system, user], tools: []}), {model: 'm', messages: [system], tools: []});
  assert.equal(sessionStartPrefix({model: 'm', messages: [system, user, assistant, user]}), null);
  assert.equal(sessionStartPrefix({model: 'm', messages: [user]}), null);
  assert.equal(sessionStartPrefix({model: 'm', messages: [system]}), null);
});

// INF-PL1: the request the provider keeps from session one is, field for
// field, session two's request without its user message, which is what
// preload@1 is sent at the next start. Because the kept prefix is the bytes
// OpenCode itself sent, a new OpenCode version, agent or project is kept
// again from its first session start.
test('the kept prefix is the next session start without its user message', async (t) => {
  const dir = tempDir(t);
  const tools = await localTools();
  const system = fixture().system[0];
  const requests = [];
  const model = createOpenAbstractions({connector: {}, preloadStateDir: dir}).languageModel('qwen3.6-35b-a3b');
  model.chat = async () => ({complete: async (request) => {
    requests.push(request);
    return {outcome: 'completed', stopReason: 'end', usage: {}, host: '', model: 'qwen3.6-35b-a3b', message: {parts: [{kind: 'text', text: 'ok'}]}};
  }});
  const project = 'C:\\Users\\person\\projects\\wordstat';
  const start = (words) => model.doGenerate({tools, providerOptions: {openabstractions: {oaLocalOnly: true, oaProject: project}}, prompt: [
    {role: 'system', content: system},
    {role: 'user', content: [{type: 'text', text: words}]},
  ]});
  await start('hi');
  let kept = null;
  for (let i = 0; i < 200 && !(kept = await recallPrefix({dir, project, model: 'qwen3.6-35b-a3b'})); i++) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.ok(kept, 'the provider kept no prefix');
  await start('Write a fizzbuzz function in fizzbuzz.py and add a short test.');
  const next = requests[1];
  assert.deepEqual(kept, {...next, messages: next.messages.slice(0, -1)});
  assert.equal(typeof kept.options.maxOutput, 'bigint');
  assert.equal(await recallPrefix({dir, project: 'C:\\other', model: 'qwen3.6-35b-a3b'}), null, 'a prefix leaked into another project');
});

test('a request without the Local assistant\'s project keeps nothing', async (t) => {
  const dir = tempDir(t);
  const model = createOpenAbstractions({connector: {}, preloadStateDir: dir}).languageModel('m');
  model.chat = async () => ({complete: async () => ({outcome: 'completed', stopReason: 'end', usage: {}, host: '', model: 'm', message: {parts: []}})});
  await model.doGenerate({prompt: [{role: 'system', content: 'rules'}, {role: 'user', content: [{type: 'text', text: 'hi'}]}]});
  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.deepEqual(readdirSync(dir), [], 'a request with no project was kept');
});

test('preload sends the kept prefix, and nothing when none is kept', async (t) => {
  const dir = tempDir(t);
  let resolved = 0;
  const sent = [];
  const resolve = async () => { resolved++; return {preload: async (request) => { sent.push(request); return {outcome: 'evaluated', reason: '', tokens: 2294n}; }}; };
  assert.equal(await preloadKeptPrefix({dir, project: 'p', model: 'm', resolve}), null);
  assert.equal(resolved, 0, 'the runtime was asked with nothing kept');
  const request = {model: 'm', messages: [{role: 'system', parts: [{kind: 'text', text: 'rules'}]}, {role: 'user', parts: [{kind: 'text', text: 'hi'}]}],
    tools: [], options: {maxOutput: 4096n, temperature: null, stop: [], jsonSchema: ''}, extensions: {}, requiredExtensions: [], guarantees: ['abstraction.inference/local-only@1'], credential: ''};
  assert.equal(await rememberSessionStart({dir, project: 'p', model: 'm', request}), true);
  const reply = await preloadKeptPrefix({dir, project: 'p', model: 'm', resolve});
  assert.equal(reply.outcome, 'evaluated');
  assert.deepEqual(sent, [{...request, messages: request.messages.slice(0, 1)}]);
});

test('at start the plugin preloads the Local assistant\'s model with the project\'s kept prefix, beside the startup', async () => {
  const calls = [];
  const logged = [];
  const client = {app: {log: async ({body}) => { logged.push(body); }}};
  const plugin = await OpenAbstractionsPlugin({client, directory: 'C:\\work\\wordstat'}, {compactTools: true, tools: {}}, {
    tool: fakeTool,
    preloadStateDir: 'unused',
    preloadKeptPrefix: async (args) => { calls.push(args); return {outcome: 'evaluated', reason: '', tokens: 2294n}; },
    readCapability: async () => false,
    readInventory: async () => ({offers: []}),
  });
  await plugin.config({model: 'openabstractions/qwen3.6-35b-a3b', agent: {local: {mode: 'primary'}},
    provider: {openabstractions: {npm: '@openabstractions/opencode', options: {runtimeEndpoint: 'x'}, models: {}}}});
  await until(() => calls.length === 1);
  assert.equal(calls[0].project, 'C:\\work\\wordstat');
  assert.equal(calls[0].model, 'qwen3.6-35b-a3b');
  assert.equal(calls[0].options.runtimeEndpoint, 'x');
  const params = {options: {}};
  await plugin['chat.params']({agent: 'local'}, params);
  assert.deepEqual(params.options, {oaLocalOnly: true, oaProject: 'C:\\work\\wordstat'});

  const other = [];
  const build = await OpenAbstractionsPlugin({client, directory: 'C:\\work'}, {tools: {}}, {
    tool: fakeTool, preloadKeptPrefix: async (args) => { other.push(args); return null; },
    readCapability: async () => false, readInventory: async () => ({offers: []}),
  });
  await build.config({model: 'openabstractions/m', agent: {local: {mode: 'primary'}},
    provider: {openabstractions: {npm: '@openabstractions/opencode', options: {}, models: {}}}});
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(other.length, 0, 'a configuration without the Local profile preloaded');
});

const sessionStart = (text) => ({model: 'm', messages: [{role: 'system', parts: [{kind: 'text', text}]}, {role: 'user', parts: [{kind: 'text', text: 'hi'}]}],
  tools: [], options: {maxOutput: 4096n}, extensions: {}, requiredExtensions: [], guarantees: [], credential: ''});

test('kept prefixes are bounded: the 20 most recent pairs, none older than 30 days', async (t) => {
  const dir = tempDir(t);
  for (let i = 0; i < keptPrefixes + 2; i++) {
    await rememberSessionStart({dir, project: `p${i}`, model: 'm', request: sessionStart(`rules ${i}`)});
    // Distinct write times, oldest first.
    await new Promise((resolve) => setTimeout(resolve, 15));
  }
  assert.equal(readdirSync(dir).length, keptPrefixes);
  assert.equal(await recallPrefix({dir, project: 'p0', model: 'm'}), null, 'the oldest pair was kept');
  assert.ok(await recallPrefix({dir, project: `p${keptPrefixes + 1}`, model: 'm'}), 'the newest pair was dropped');

  const stale = join(dir, `${'a'.repeat(32)}.json`);
  writeFileSync(stale, '{}');
  const old = (Date.now() - 31 * 24 * 60 * 60 * 1000) / 1000;
  utimesSync(stale, old, old);
  const other = join(dir, 'notes.txt');
  writeFileSync(other, 'not ours');
  await pruneKept(dir);
  assert.ok(!readdirSync(dir).includes(`${'a'.repeat(32)}.json`), 'a prefix older than 30 days was kept');
  assert.ok(readdirSync(dir).includes('notes.txt'), 'a file that is no kept prefix was removed');
});

test('a write that fails leaves no temporary file', async (t) => {
  const dir = tempDir(t);
  await rememberSessionStart({dir, project: 'p', model: 'm', request: sessionStart('rules')});
  const [kept] = readdirSync(dir);
  rmSync(join(dir, kept));
  mkdirSync(join(dir, kept));
  writeFileSync(join(dir, kept, 'inside'), 'x');
  await assert.rejects(rememberSessionStart({dir, project: 'p', model: 'm', request: sessionStart('rules')}));
  assert.deepEqual(readdirSync(dir).filter((name) => name.endsWith('.tmp')), []);
});
