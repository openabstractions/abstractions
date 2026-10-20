import assert from 'node:assert/strict';
import * as oaAvailability from '../availability.js';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {APICallError} from '@ai-sdk/provider';
import {Fold} from '@openabstractions/inference';
import {installWhere, partPlace, placeText, placeToast} from '../where.js';
import OpenAbstractionsPlugin from '../plugin.js';
import {localServers, modelName, servableChatModels} from '../inventory.js';

// The production provider with its package imports replaced, as in
// tool-call-output.test.mjs; the router's host table is a fake.
async function modelWith(chat, hosts = {ollama: false, deepseek: true}) {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
  const {createOpenAbstractions} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => { throw new Error('unexpected'); },
    async () => { throw new Error('unexpected chat@2 resolution'); }, class {}, {Part: 'part', End: 'end', Usage: 'usage'}, Fold,
    {Text: 'text', ToolCall: 'tool_call'}, {Completed: 'completed'}, {LocalOnly: 'local-only', HostedAllowed: 'hosted-allowed'}, {},
    {ToolCalls: 'tool_calls'});
  const model = createOpenAbstractions({connector: {}, scope: 'remote'}).languageModel('qwen3');
  model.chat = async () => chat;
  model.hostTable = new Map(Object.entries(hosts));
  model.hostTableAt = Date.now();
  return model;
}

const completedBy = (host) => ({outcome: 'completed', stopReason: 'end', usage: {}, host, model: 'm',
  message: {parts: [{kind: 'text', text: 'hello'}]}});

test('each reply part says where it ran, from the router\'s host table', async () => {
  const local = await modelWith({async complete() { return completedBy('ollama'); }});
  const generated = await local.doGenerate({prompt: []});
  assert.equal(generated.content[0].providerMetadata.openabstractions.placement, 'local');
  const hosted = await modelWith({async complete() { return completedBy('deepseek'); }});
  assert.equal((await hosted.doGenerate({prompt: []})).content[0].providerMetadata.openabstractions.placement, 'hosted');
  const remote = await modelWith({async complete() { return completedBy('east/lemonade'); }}, {east: true});
  assert.equal((await remote.doGenerate({prompt: []})).providerMetadata.openabstractions.placement, 'hosted', 'a remote runtime is not this computer');
  const unknown = await modelWith({async complete() { return completedBy('mystery'); }}, {});
  unknown.machine = async () => { throw new Error('no runtime'); };
  assert.equal((await unknown.doGenerate({prompt: []})).providerMetadata.openabstractions.placement, 'unknown', 'never guessed from a name');

  const streamed = await modelWith({async *stream() {
    yield {kind: 'part', index: 0n, part: {kind: 'text', text: 'hi'}};
    yield {kind: 'end', end: {outcome: 'completed', stopReason: 'end', usage: {}, host: 'deepseek', model: 'm'}};
  }});
  const {stream} = await streamed.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.equal(events.find((event) => event.type === 'text-end').providerMetadata.openabstractions.placement, 'hosted');
});

test('a Local assistant request carries the local-only guarantee and its refusal says why', async () => {
  let sent;
  const chat = {async complete(request) { sent = request; return {outcome: 'no_host', reason: 'router:no_host', host: '', model: '', usage: {}}; }};
  const model = await modelWith(chat);
  await assert.rejects(model.doGenerate({prompt: [], providerOptions: {'openabstractions-hosted': {oaLocalOnly: true}}}), (error) => {
    assert.match(error.message, /Local assistant keeps this conversation on this computer/);
    assert.match(error.message, /press Tab for the Build agent/);
    return true;
  });
  assert.deepEqual(sent.guarantees, ['local-only'], 'OA, not the plugin, refuses a hosted route');
  const build = await modelWith({async complete(request) { sent = request; return completedBy('deepseek'); }});
  await build.doGenerate({prompt: []});
  assert.deepEqual(sent.guarantees, ['hosted-allowed'], 'Build keeps the provider\'s own guarantee');
});

test('the plugin marks only the Local assistant\'s requests local-only', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {tool: Object.assign((d) => d, {schema: {}})});
  const local = {options: {keep: 1}};
  await plugin['chat.params']({agent: 'local'}, local);
  assert.deepEqual(local.options, {keep: 1, oaLocalOnly: true});
  const build = {options: {}};
  await plugin['chat.params']({agent: 'build'}, build);
  assert.equal(build.options.oaLocalOnly, undefined);
});

test('a hosted reply shows a toast once and the prompt line says where replies run', async () => {
  const handlers = {};
  const toasts = [];
  const shown = [];
  const api = {event: {on: (type, handler) => { handlers[type] = handler; return () => {}; }},
    ui: {toast: (toast) => toasts.push(toast)}, lifecycle: {onDispose() {}}};
  await installWhere(api, {}, {slot: async () => (where) => shown.push(where)});
  const part = (placement, host, messageID = 'm1') => ({properties: {part: {type: 'text', sessionID: 's1', messageID,
    metadata: {openabstractions: {placement, host}}}}});
  handlers['message.part.updated'](part('local', 'ollama'));
  assert.equal(toasts.length, 0);
  handlers['message.part.updated'](part('hosted', 'deepseek', 'm2'));
  handlers['message.part.updated'](part('hosted', 'deepseek', 'm2'));
  assert.equal(toasts.length, 1, 'one toast per hosted reply');
  assert.equal(toasts[0].variant, 'warning');
  assert.match(toasts[0].message, /hosted server \(deepseek\): the conversation left this computer/);
  assert.deepEqual(shown.map((where) => placeText(where)), ['replies: this computer', 'replies: hosted (deepseek)', 'replies: hosted (deepseek)']);
  assert.equal(placeToast({placement: 'local'}), null);
});

test('picker names say where a model runs, from the router\'s placements', () => {
  const offer = {family: 'qwen3.6-35b-a3b', names: ['qwen'], servers: ['lemonade'], context: 32768, contextSource: 'effective', input: 28672};
  assert.equal(modelName(offer, 32768), 'qwen3.6-35b-a3b · 32k · this computer: lemonade');
  assert.equal(modelName({...offer, family: 'deepseek-chat', servers: ['deepseek']}, 65536, () => false), 'deepseek-chat · 64k · hosted: deepseek');
  const mixed = {...offer, servers: ['ollama', 'east/lemonade']};
  const isLocal = localServers([{servers: ['ollama']}]);
  assert.equal(servableChatModels([mixed], [], null, isLocal)['qwen3.6-35b-a3b'].name,
    'qwen3.6-35b-a3b · 32k · this computer: ollama; hosted: east/lemonade');
  // A server named like a local engine is still hosted when the router says so.
  assert.equal(modelName({...offer, servers: ['ollama-cloud']}, 32768, () => false), 'qwen3.6-35b-a3b · 32k · hosted: ollama-cloud');
});

test('the hosted heading reads hosted offers; the preferences heading lists no router model', async () => {
  const reads = [];
  const offers = {
    hosted: [{family: 'deepseek-chat', names: ['deepseek-chat'], servers: ['deepseek'], context: 65536, contextSource: 'maximum', input: 61440}],
    local: [{family: 'qwen', names: ['qwen'], servers: ['ollama'], context: 32768, contextSource: 'effective', input: 28672}],
  };
  const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {
    tool: Object.assign((d) => d, {schema: {}}), readCapability: async () => false,
    readInventory: async (_options, placement) => { reads.push(placement); return {offers: offers[placement] ?? []}; },
  });
  const config = {provider: {
    openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models: {}},
    'openabstractions-hosted': {npm: '@openabstractions/opencode', options: {scope: 'remote', oaPlacement: 'hosted'}, models: {}},
    'openabstractions-preferences': {npm: '@openabstractions/opencode', options: {scope: 'local', oaInventory: false}, models: {}},
  }};
  await plugin.config(config);
  assert.deepEqual(Object.values(config.provider.openabstractions.models).map((m) => m.name), ['qwen · 32k · this computer: ollama']);
  assert.deepEqual(Object.values(config.provider['openabstractions-hosted'].models).map((m) => m.name), ['deepseek-chat · 64k · hosted: deepseek']);
  assert.deepEqual(config.provider['openabstractions-preferences'].models, {});
  assert.ok(reads.includes('hosted'));
});

function whereHarness() {
  const handlers = {};
  const toasts = [];
  const shown = [];
  const api = {event: {on: (type, handler) => { handlers[type] = handler; return () => {}; }},
    ui: {toast: (toast) => toasts.push(toast)}, lifecycle: {onDispose() {}}};
  const part = (placement, host, messageID) => handlers['message.part.updated']({properties: {part: {type: 'text', sessionID: 's1',
    messageID, metadata: {openabstractions: {placement, host}}}}});
  const finished = (id, extra = {}) => handlers['message.updated']({properties: {sessionID: 's1', info: {id, sessionID: 's1',
    role: 'assistant', time: {created: 1, completed: 2}, ...extra}}});
  return {api, toasts, shown, part, finished, text: () => placeText(shown.at(-1))};
}

test('a reply that did not come through OA says so instead of keeping the last place', async () => {
  const h = whereHarness();
  await installWhere(h.api, {}, {slot: async () => (where) => h.shown.push(where)});
  h.part('local', 'ollama', 'm1');
  h.finished('m1');
  assert.equal(h.text(), 'replies: this computer', 'an OA reply keeps its own place');
  h.finished('m2');
  assert.equal(h.text(), 'replies: not through OpenAbstractions');
  h.finished('m2');
  assert.equal(h.shown.length, 2, 'a finished reply updated again is settled once');
  h.part('local', 'ollama', 'm3');
  h.finished('m4', {error: {name: 'APIError'}});
  assert.equal(h.text(), 'replies: this computer', 'a failed reply answered nothing');
  h.finished('m5', {time: {created: 1}});
  assert.equal(h.text(), 'replies: this computer', 'a reply still running is not settled');
  assert.equal(h.toasts.length, 0);
});

test('a reply the router could not place says place unknown and raises a toast', async () => {
  const h = whereHarness();
  await installWhere(h.api, {}, {slot: async () => (where) => h.shown.push(where)});
  h.part('local', 'ollama', 'm1');
  h.part('unknown', 'mystery', 'm2');
  h.part('unknown', 'mystery', 'm2');
  h.finished('m2');
  assert.equal(h.text(), 'replies: place unknown');
  assert.equal(h.toasts.length, 1, 'one toast per reply');
  assert.equal(h.toasts[0].variant, 'warning');
  assert.match(h.toasts[0].message, /could not tell where this reply ran \(mystery\); it may have left this computer/);
  assert.equal(partPlace({type: 'text', metadata: {openabstractions: {placement: 'elsewhere'}}}).placement, 'unknown');
});

test('the router host table is read again once it is older than its expiry', async () => {
  const model = await modelWith({async complete() { return completedBy('lemonade'); }}, {lemonade: false});
  let reads = 0;
  model.readHostTable = async () => { reads += 1; return new Map([['lemonade', true]]); };
  const placement = async () => (await model.doGenerate({prompt: []})).providerMetadata.openabstractions.placement;
  assert.equal(await placement(), 'local');
  assert.equal(reads, 0, 'a fresh table answers');
  model.hostTableAt = Date.now() - 31000;
  assert.equal(await placement(), 'hosted', 'the server moved to a hosted runtime');
  assert.equal(reads, 1);
  assert.equal(await placement(), 'hosted');
  assert.equal(reads, 1, 'the new table is fresh again');
  model.readHostTable = async () => { throw new Error('runtime stopped'); };
  model.hostTableAt = Date.now() - 31000;
  assert.equal(await placement(), 'unknown', 'a stale table is never trusted');
});

test('a model call that cannot reach OA says OA is unavailable and marks it away', async () => {
  const told = [];
  oaAvailability.availability.onChange(({variant, message}) => told.push([variant, message]));
  try {
    const down = await modelWith({async complete() { throw Object.assign(new Error('native operation failed'), {status: 3, transferred: 0}); }});
    await assert.rejects(down.doGenerate({prompt: []}), (error) => {
      assert.equal(error.message, `${oaAvailability.unavailableMessage} This model call could not reach it.`);
      assert.equal(error.isRetryable, false);
      return true;
    });
    assert.deepEqual(told, [['warning', oaAvailability.unavailableMessage]]);
    const back = await modelWith({async complete() { return completedBy('ollama'); }});
    await back.doGenerate({prompt: []});
    assert.deepEqual(told.at(-1), ['success', oaAvailability.returnedMessage], 'an answer turns OA tools on again');
  } finally {
    oaAvailability.availability.onChange(null);
    oaAvailability.availability.markUp();
  }
});

test('a model call to a runtime that did not prove its identity says so, and OA stays marked available', async () => {
  const untrusted = await modelWith({async complete() { throw Object.assign(new Error('native operation failed'), {status: 8, transferred: 0}); }});
  await assert.rejects(untrusted.doGenerate({prompt: []}), (error) => error.message === oaAvailability.untrustedMessage);
  assert.equal(oaAvailability.availability.down, false);
});
