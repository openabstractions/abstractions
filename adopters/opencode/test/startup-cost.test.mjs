// OpenCode's start waits for OA at most one short probe: a missing or hung
// runtime costs the config hook about startupProbeMs, and nothing else asks OA.
import assert from 'node:assert/strict';
import test from 'node:test';
import OpenAbstractionsPlugin from '../plugin.js';
import {Availability} from '../availability.js';

const optional = () => ({optional() { return this; }});
const fakeTool = Object.assign((definition) => definition, {schema: {
  object: (value) => value, string: optional, number: optional, boolean: optional, enum: optional, array: optional,
}});
const configured = () => ({provider: {openabstractions: {npm: '@openabstractions/opencode',
  options: {oaSelectionPolicies: {pick: 'saved-policy'}}, models: {kept: {name: 'kept'}}}}});
const missing = () => Promise.reject(Object.assign(new Error('no runtime'), {name: 'ResolutionError', status: 'runtime_unavailable'}));

async function start(probeRuntime) {
  const asked = [];
  const toasts = [];
  const plugin = await OpenAbstractionsPlugin({client: {tui: {showToast: async ({body}) => toasts.push(body)}}}, {}, {
    tool: fakeTool, env: {}, availability: new Availability(), probeRuntime,
    readInventory: async () => { asked.push('inventory'); return {offers: []}; },
    readCapability: async (_options, profile) => { asked.push(profile); return false; },
    readSelectionPolicy: async () => { asked.push('policy'); return {outcome: 'unknown'}; },
    readAnswerable: async () => { asked.push('answerable'); return false; },
  });
  const config = configured();
  const began = performance.now();
  await plugin.config(config);
  return {plugin, config, asked, toasts, elapsed: performance.now() - began};
}

test('a missing OA costs the start one probe and asks nothing more', async () => {
  const {plugin, config, asked, toasts, elapsed} = await start(missing);
  assert.ok(elapsed < 1000, `config returned in ${elapsed} ms`);
  assert.deepEqual(asked, [], 'no inventory, capability, policy or questions probe follows');
  assert.deepEqual(Object.keys(plugin.tool).sort(), ['openabstractions_generate_image', 'openabstractions_speak'],
    'the capability tools register guarded and say OA is unavailable');
  assert.equal(toasts.length, 1);
  assert.match(toasts[0].message, /unavailable/);
  const listed = await plugin.provider.models({id: 'openabstractions', models: config.provider.openabstractions.models});
  assert.deepEqual(Object.keys(listed), ['kept'], 'the first listing keeps the configured entries without asking');
  assert.deepEqual(asked, []);
});

test('a hung OA costs the start about one second at most', async () => {
  const {asked, elapsed} = await start(() => new Promise(() => {}));
  assert.ok(elapsed < 1200, `config returned in ${elapsed} ms`);
  assert.ok(elapsed >= 700, 'the probe waits its budget before going on');
  assert.deepEqual(asked, []);
});

test('an OA that answers the probe gets the usual startup survey', async () => {
  let probes = 0;
  const {asked} = await start(async () => { probes += 1; return {}; });
  assert.equal(probes, 1, 'one probe per start');
  assert.ok(asked.includes('inventory'));
  assert.ok(asked.includes('image') && asked.includes('speech'));
  assert.ok(asked.includes('policy'));
});
