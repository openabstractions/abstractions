import assert from 'node:assert/strict';
import {tool as hostTool} from '@opencode-ai/plugin/tool';
import {mkdtempSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import test, {after} from 'node:test';
import {Availability, guardTool, returnedMessage, runtimeUnavailable, runtimeUntrusted, unavailableMessage, untrustedMessage} from '../availability.js';
import OpenAbstractionsPlugin from '../plugin.js';
import {FrameError, Status} from '@openabstractions/ipc';

// The native IPC connector's FrameError: a numeric status and a transferred
// count (@openabstractions/ipc); IoError is 3 and Cancelled 7.
// Image calls save default pictures here, never in the person's Pictures.
const testPictures = mkdtempSync(join(tmpdir(), 'oa-availability-pictures-'));
after(() => rmSync(testPictures, {recursive: true, force: true}));
const ipcFailure = () => Object.assign(new Error('native operation failed'), {status: 3, transferred: 0});


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
  assert.equal(runtimeUnavailable(frame(10)), true, 'affirmative initial-open IoError');
  for (const status of [1, 4, 5, 6, 7, 8, 9]) assert.equal(runtimeUnavailable(frame(status)), false, `status ${status}`);
  const resolution = (cause) => Object.assign(new Error('service resolution: runtime_unavailable', cause ? {cause} : undefined),
    {name: 'ResolutionError', status: 'runtime_unavailable'});
  assert.equal(runtimeUnavailable(resolution()), true, 'no runtime to resolve');
  assert.equal(runtimeUnavailable(resolution(frame(3))), true);
  assert.equal(runtimeUnavailable(resolution(frame(10))), true, 'typed open failure survives resolution wrapping');
  assert.equal(runtimeUnavailable({status: 10}), false, 'a numeric property without FrameError shape is insufficient');
  assert.equal(runtimeUnavailable(resolution(frame(8))), false, 'an untrusted runtime is never "unavailable"');
  assert.equal(runtimeUntrusted(resolution(frame(8))), true);
  assert.equal(runtimeUntrusted(frame(3)), false);
  assert.equal(runtimeUnavailable(new Error('outer', {cause: ipcFailure()})), true, 'a wrapped failure counts');
  assert.equal(runtimeUnavailable(Object.assign(new Error('native operation failed'), {code: 'runtime_unavailable'})), false,
    'words alone are not a status');
  assert.equal(runtimeUnavailable(new TypeError('Provide a prompt')), false);
});

test('actual native error projection distinguishes open failure from zero-byte strict and ambiguous errors', () => {
  const open = new FrameError(Status.OpenIoError, 'initial open failed', 0);
  assert.equal(runtimeUnavailable(new Error('helper failed', {cause: open})), true);
  for (const status of [Status.Timeout, Status.InvalidArgument, Status.InternalError,
    Status.Cancelled, Status.Untrusted, Status.ProofUnavailable]) {
    assert.equal(runtimeUnavailable(new FrameError(status, 'zero bytes', 0)), false);
  }
  for (const status of [Status.IoError, Status.Disconnected]) {
    assert.equal(runtimeUnavailable(new FrameError(status, 'write/read acceptance unknown', 0)), true,
      'existing outage presentation does not grant replay or activation');
  }
});

test('an IPC failure at startup keeps OpenCode working, says so once, and registers the tools guarded', async () => {
  const h = harness();
  const models = {configured: {name: 'Configured', limit: {context: 8192, output: 4096}}};
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models}}};
  let reached = 0;
  const plugin = await OpenAbstractionsPlugin({client: h.client}, {}, {
    tool: hostTool, availability: h.state,
    readInventory: async () => { throw new FrameError(Status.OpenIoError, 'initial open failed', 0); },
    readCapability: async () => { throw new FrameError(Status.OpenIoError, 'initial open failed', 0); },
    resolveImageClient: async () => { reached += 1; throw ipcFailure(); },
  });
  await plugin.config(config);
  assert.equal(config.provider.openabstractions.models, models, 'the configured models stay');
  const listing = await plugin.provider.models({id: 'openabstractions', options: {scope: 'local'}, models});
  assert.deepEqual(Object.keys(listing), ['configured'], 'OpenCode can list its providers');
  assert.deepEqual(h.toasts.map((toast) => toast.message), [unavailableMessage], 'one toast for the outage');
  assert.equal(h.toasts[0].variant, 'warning');
  assert.ok(plugin.tool.oa_image_generate, 'the image tool waits for OA');

  // save_to keeps the call in the plugin's own image@1 path, which reaches the runtime.
  const execute = (args = {model: 'm', prompt: 'a fox', size: '512x512', save_to: 'fox.png'}) => plugin.tool.oa_image_generate
    .execute(args, {directory: testPictures, ask: async () => {}});
  assert.equal(await execute(), unavailableMessage, 'tools answer at once while OA is away');
  assert.equal(reached, 0, 'no runtime call before the retry interval');
  h.time.advance(15000);
  assert.match(await execute(), /^OpenAbstractions is unavailable; OA tools are off until it returns\. \(ipc status 3\)$/);
  assert.equal(reached, 1, 'one try after the interval');
  assert.equal(h.toasts.length, 1, 'still one toast');
});

test('a later model refresh explains only a typed stopped runtime and preserves the chosen model', async () => {
  const h = harness();
  const config = {model: 'oa-custom/chosen', provider: {
    'oa-custom': {npm: '@openabstractions/opencode', models: {}},
    'oa-custom-hosted': {npm: '@openabstractions/opencode', options: {oaPlacement: 'hosted'}, models: {}},
  }};
  let error = null;
  const plugin = await OpenAbstractionsPlugin({client: h.client}, {providerId: 'oa-custom'}, {
    tool: hostTool, availability: h.state, now: h.time.now,
    readInventory: async (_options, placement) => {
      if (error) throw error;
      return {offers: [{family: placement, names: [placement], servers: ['fixture'], context: 8192n,
        contextSource: 'effective', input: 4096n, unknown: []}]};
    }, readSelectionPolicy: async () => ({outcome: 'unknown'}),
  });
  await plugin.config(config);
  const providers = Object.entries(config.provider).map(([id, provider]) => ({id, ...provider}));
  for (const provider of providers) provider.models = await plugin.provider.models(provider);
  const before = JSON.stringify(providers.map((provider) => provider.models));
  error = new FrameError(Status.OpenIoError, 'initial open failed', 0);
  for (const provider of providers) {
    await assert.rejects(plugin.provider.models(provider), (failure) => {
      assert.match(failure.message, /OpenAbstractions is unavailable; the model list was not refreshed/);
      assert.equal(runtimeUnavailable(failure), true, 'the native cause remains typed');
      return true;
    });
  }
  assert.equal(JSON.stringify(providers.map((provider) => provider.models)), before);
  assert.equal(config.model, 'oa-custom/chosen');
  for (const failure of [Object.assign(new Error('untrusted'), {status: 8, transferred: 0}),
    new TypeError('malformed catalogue'), new Error('runtime_unavailable is only text')]) {
    error = failure;
    await assert.rejects(plugin.provider.models(providers[0]), (actual) => actual === failure);
  }
  assert.equal(config.model, 'oa-custom/chosen');
});

test('OA tools turn on again only when the runtime answers, with one toast', async () => {
  const h = harness();
  let reached = 0;
  let down = true;
  const image = {async start() { return {outcome: 'not_permitted', reason: 'rights:denied host:fixture'}; }};
  const plugin = await OpenAbstractionsPlugin({client: h.client}, {tools: {generate_image: true}}, {
    tool: hostTool, availability: h.state,
    resolveImageClient: async () => { reached += 1; if (down) throw ipcFailure(); return image; },
  });
  const execute = (args) => plugin.tool.oa_image_generate.execute(
    {model: 'm', prompt: 'a fox', size: '512x512', save_to: 'fox.png', ...args}, {directory: testPictures, ask: async () => {}});
  assert.match(await execute(), /unavailable/);
  down = false;
  h.time.advance(15000);
  // A call that ends before reaching the runtime proves nothing.
  // Use a syntax refusal here: filesystem containment has its own native
  // path-owner tests and must not be required by this transport-only fixture.
  assert.match(await execute({save_to: 'away:stream.png'}), /must not contain a colon/);
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
    tool: hostTool, availability: h.state, resolveImageClient: async () => { throw untrusted; },
  });
  await assert.rejects(plugin.tool.oa_image_generate.execute({model: 'm', prompt: 'a fox', size: 'auto', save_to: 'fox.png'}, {directory: testPictures, ask: async () => {}}),
    (error) => error.message === untrustedMessage);
  assert.equal(h.state.down, false);
  assert.deepEqual(h.toasts, [], 'no outage toast');
});

test('hooks OpenCode runs inside each request never throw into it', async () => {
  const h = harness();
  const plugin = await OpenAbstractionsPlugin({client: h.client}, {compactTools: true}, {tool: hostTool, availability: h.state});
  await plugin['chat.params']({agent: 'local'}, undefined);
  await plugin['tool.definition']({toolID: 'bash'}, null);
  await plugin.config({get provider() { throw ipcFailure(); }});
  assert.deepEqual(h.toasts.map((toast) => toast.message), [unavailableMessage]);
  assert.ok(h.logs.some((entry) => /OpenCode starts without its changes/.test(entry.message)));
});
