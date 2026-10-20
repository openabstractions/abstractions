import assert from 'node:assert/strict';
import test from 'node:test';
import {Availability, guardTool, returnedMessage, runtimeUnavailable, runtimeUntrusted, unavailableMessage, untrustedMessage} from '../availability.js';
import OpenAbstractionsPlugin from '../plugin.js';

// The native IPC connector's FrameError: a numeric status and a transferred
// count (@openabstractions/ipc); IoError is 3 and Cancelled 7.
const ipcFailure = () => Object.assign(new Error('native operation failed'), {status: 3, transferred: 0});
const fakeTool = Object.assign((definition) => definition, {schema: {
  object: (value) => value,
  string: () => ({optional() { return this; }}),
  number: () => ({optional() { return this; }}),
  boolean: () => ({optional() { return this; }}),
  enum: (value) => ({value, optional() { return this; }}),
  array: (value) => ({value, optional() { return this; }}),
}});

function clock() {
  let now = 1000;
  return {now: () => now, advance: (ms) => { now += ms; }};
}

function harness(fields = {}) {
  const time = clock();
  const state = new Availability({retryMs: 15000, now: time.now});
  const toasts = [];
  const logs = [];
  const client = {tui: {showToast: async ({body}) => toasts.push(body)}, app: {log: async (entry) => logs.push(entry.body)}};
  return {time, state, toasts, logs, client, ...fields};
}

test('only a lost connection counts as OA away; every other IPC status stays its own error', () => {
  const frame = (status) => Object.assign(new Error('native operation failed'), {status, transferred: 0});
  assert.equal(runtimeUnavailable(frame(2)), true, 'Disconnected');
  assert.equal(runtimeUnavailable(frame(3)), true, 'IoError');
  for (const status of [1, 4, 5, 6, 7, 8, 9]) assert.equal(runtimeUnavailable(frame(status)), false, `status ${status}`);
  const resolution = (cause) => Object.assign(new Error('service resolution: runtime_unavailable', cause ? {cause} : undefined),
    {name: 'ResolutionError', status: 'runtime_unavailable'});
  assert.equal(runtimeUnavailable(resolution()), true, 'no runtime to resolve');
  assert.equal(runtimeUnavailable(resolution(frame(3))), true);
  assert.equal(runtimeUnavailable(resolution(frame(8))), false, 'an untrusted runtime is never "unavailable"');
  assert.equal(runtimeUntrusted(resolution(frame(8))), true);
  assert.equal(runtimeUntrusted(frame(3)), false);
  assert.equal(runtimeUnavailable(new Error('outer', {cause: ipcFailure()})), true, 'a wrapped failure counts');
  assert.equal(runtimeUnavailable(Object.assign(new Error('native operation failed'), {code: 'runtime_unavailable'})), false,
    'words alone are not a status');
  assert.equal(runtimeUnavailable(new TypeError('Provide a prompt')), false);
});

test('an IPC failure at startup keeps OpenCode working, says so once, and registers the tools guarded', async () => {
  const h = harness();
  const models = {configured: {name: 'Configured', limit: {context: 8192, output: 4096}}};
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models}}};
  let reached = 0;
  const plugin = await OpenAbstractionsPlugin({client: h.client}, {}, {
    tool: fakeTool, availability: h.state,
    readInventory: async () => { throw ipcFailure(); },
    readCapability: async () => { throw ipcFailure(); },
    resolveImageClient: async () => { reached += 1; throw ipcFailure(); },
  });
  await plugin.config(config);
  assert.equal(config.provider.openabstractions.models, models, 'the configured models stay');
  const listing = await plugin.provider.models({id: 'openabstractions', options: {scope: 'local'}, models});
  assert.deepEqual(Object.keys(listing), ['configured'], 'OpenCode can list its providers');
  assert.deepEqual(h.toasts.map((toast) => toast.message), [unavailableMessage], 'one toast for the outage');
  assert.equal(h.toasts[0].variant, 'warning');
  assert.ok(plugin.tool.openabstractions_generate_image, 'the image tool waits for OA');

  const execute = (args = {model: 'm', prompt: 'a fox', size: '512x512'}) => plugin.tool.openabstractions_generate_image
    .execute(args, {ask: async () => {}});
  assert.equal(await execute(), unavailableMessage, 'tools answer at once while OA is away');
  assert.equal(reached, 0, 'no runtime call before the retry interval');
  h.time.advance(15000);
  assert.match(await execute(), /^OpenAbstractions is unavailable; OA tools are off until it returns\. \(ipc status 3\)$/);
  assert.equal(reached, 1, 'one try after the interval');
  assert.equal(h.toasts.length, 1, 'still one toast');
});

test('OA tools turn on again only when the runtime answers, with one toast', async () => {
  const h = harness();
  let reached = 0;
  let down = true;
  const image = {async start() { return {outcome: 'not_permitted', reason: 'rights:denied host:fixture'}; }};
  const plugin = await OpenAbstractionsPlugin({client: h.client}, {tools: {generate_image: true}}, {
    tool: fakeTool, availability: h.state,
    resolveImageClient: async () => { reached += 1; if (down) throw ipcFailure(); return image; },
  });
  const execute = (args) => plugin.tool.openabstractions_generate_image.execute(
    {model: 'm', prompt: 'a fox', size: '512x512', ...args}, {directory: process.cwd(), ask: async () => {}});
  assert.match(await execute(), /unavailable/);
  down = false;
  h.time.advance(15000);
  // A call that ends before reaching the runtime proves nothing.
  assert.match(await execute({save_to: '../away.png'}), /stay inside the project/);
  assert.equal(h.state.down, true, 'a refused save_to is no answer from the runtime');
  assert.deepEqual(h.toasts.map((toast) => toast.variant), ['warning']);
  h.time.advance(15000);
  assert.match(await execute(), /Image generation refused: not_permitted/);
  assert.equal(h.state.down, false, 'the resolver reached the runtime');
  assert.deepEqual(h.toasts.map((toast) => [toast.variant, toast.message]), [['warning', unavailableMessage], ['success', returnedMessage]]);
  assert.equal(reached, 2);
  await assert.rejects(guardTool({execute: async () => { throw new TypeError('bad arguments'); }}, h.state).execute({}, {}), /bad arguments/,
    'a tool\'s own error stays the tool\'s');
});

test('an untrusted runtime fails the call with its own sentence and never reads as away', async () => {
  const h = harness();
  const untrusted = Object.assign(new Error('service resolution: runtime_unavailable', {
    cause: Object.assign(new Error('native operation failed'), {status: 8, transferred: 0})}), {name: 'ResolutionError', status: 'runtime_unavailable'});
  const plugin = await OpenAbstractionsPlugin({client: h.client}, {tools: {generate_image: true}}, {
    tool: fakeTool, availability: h.state, resolveImageClient: async () => { throw untrusted; },
  });
  await assert.rejects(plugin.tool.openabstractions_generate_image.execute({model: 'm', prompt: 'a fox', size: 'auto'}, {ask: async () => {}}),
    (error) => error.message === untrustedMessage);
  assert.equal(h.state.down, false);
  assert.deepEqual(h.toasts, [], 'no outage toast');
});

test('hooks OpenCode runs inside each request never throw into it', async () => {
  const h = harness();
  const plugin = await OpenAbstractionsPlugin({client: h.client}, {compactTools: true}, {tool: fakeTool, availability: h.state});
  await plugin['chat.params']({agent: 'local'}, undefined);
  await plugin['tool.definition']({toolID: 'bash'}, null);
  await plugin.config({get provider() { throw ipcFailure(); }});
  assert.deepEqual(h.toasts.map((toast) => toast.message), [unavailableMessage]);
  assert.ok(h.logs.some((entry) => /OpenCode starts without its changes/.test(entry.message)));
});
