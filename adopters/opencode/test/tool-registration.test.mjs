import test from 'node:test';
import assert from 'node:assert/strict';
import OpenAbstractionsPlugin from '../plugin.js';
import {capabilityTools, toolChoices, toolNames} from '../tool-config.js';

const optional = () => ({optional() { return this; }});
const fakeTool = Object.assign((definition) => definition, {schema: {
  object: (value) => value, string: optional, number: optional, boolean: optional, enum: optional, array: optional,
}});
const config = (options = {}) => ({provider: {openabstractions: {npm: '@openabstractions/opencode', options, models: {}}}});

async function registered(pluginOptions, served = [], providerOptions = {}) {
  const asked = [];
  const logs = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry.body)}}}, pluginOptions, {
    tool: fakeTool,
    readInventory: async () => ({offers: []}),
    readCapability: async (options, profile, placement) => {
      asked.push([profile, placement, options.runtimeEndpoint]);
      if (profile === 'fail') throw new Error('unreachable');
      return served.includes(profile);
    },
  });
  await plugin.config(config(providerOptions));
  return {names: Object.keys(plugin.tool).sort(), asked, logs, plugin};
}

test('with no tools option only the served capabilities register their tools', async () => {
  assert.deepEqual((await registered({})).names, []);
  assert.deepEqual((await registered({}, ['image'])).names, ['openabstractions_generate_image']);
  assert.deepEqual((await registered({}, ['speech'])).names, ['openabstractions_speak']);
  const both = await registered({}, ['image', 'speech'], {runtimeEndpoint: 'controlled'});
  assert.deepEqual(both.names, ['openabstractions_generate_image', 'openabstractions_speak']);
  assert.deepEqual(both.asked, [['image', 'local', 'controlled'], ['speech', 'local', 'controlled']]);
  assert.deepEqual(capabilityTools, {generate_image: 'image', speak: 'speech'});
});

test('a remote-scope provider looks for capabilities on any placement', async () => {
  const {asked} = await registered({}, ['image'], {scope: 'remote'});
  assert.deepEqual(asked.map(([, placement]) => placement), ['any', 'any']);
});

test('tools that open the Panel, answer questions or edit preferences register only when enabled', async () => {
  const opt = {selection_policy: true, watch_logs: true, open_credentials_panel: true, authorization_questions: true};
  const {names, plugin} = await registered({tools: opt}, ['image', 'speech']);
  assert.deepEqual(names, ['openabstractions_authorization_questions', 'openabstractions_generate_image',
    'openabstractions_open_credentials_panel', 'openabstractions_selection_policy', 'openabstractions_speak',
    'openabstractions_watch_logs']);
  assert.equal(typeof plugin.tool.openabstractions_open_credentials_panel.execute, 'function');
});

test('an explicit tools option registers before startup and overrides the capability check either way', async () => {
  const forced = await registered({tools: {generate_image: true}}, []);
  assert.deepEqual(forced.names, ['openabstractions_generate_image']);
  assert.deepEqual(forced.asked.map(([profile]) => profile), ['speech'], 'an enabled tool is not checked');
  const off = await registered({tools: {generate_image: false, speak: false}}, ['image', 'speech']);
  assert.deepEqual(off.names, []);
  assert.deepEqual(off.asked, [], 'a disabled tool is not checked');
  const early = await OpenAbstractionsPlugin({client: {}}, {tools: {watch_logs: true}}, {tool: fakeTool});
  assert.deepEqual(Object.keys(early.tool), ['openabstractions_watch_logs'], 'enabled tools exist before the config hook');
});

test('an unreadable capability registers nothing and says so; unknown options are ignored with a warning', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async () => {}}}}, {}, {
    tool: fakeTool, readInventory: async () => ({offers: []}),
    readCapability: async () => { throw Object.assign(new Error('down'), {code: 'unavailable'}); },
  });
  await plugin.config(config());
  assert.deepEqual(Object.keys(plugin.tool), []);
  const {names, logs} = await registered({tools: {shell: true, speak: 'yes', generate_image: true}});
  assert.deepEqual(names, ['openabstractions_generate_image']);
  assert.ok(logs.some((entry) => entry.level === 'warn' && JSON.stringify(entry.extra.names) === '["shell","speak"]'));
  assert.deepEqual(toolChoices([]).unknown, ['tools']);
  assert.equal(toolNames.length, 6);
});

test('a typed refused router capability registers no supplied models as tools', async () => {
  for (const outcome of ['not_permitted', 'forbidden', 'unavailable', 'invalid', undefined]) {
    const logs = [];
    const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry.body)}}}, {}, {
      tool: fakeTool, readInventory: async () => ({offers: []}),
      resolveRouter: async () => ({offer: async () => ({outcome, offers: [{family: 'must not be consumed'}]})}),
    });
    await plugin.config(config());
    assert.deepEqual(Object.keys(plugin.tool), []);
    assert.ok(logs.some((entry) => entry.level === 'warn' && entry.extra?.reason === `code:${outcome ?? 'invalid_result'}`));
  }
});

test('without a configured OA provider no capability is checked', async () => {
  const asked = [];
  const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {
    tool: fakeTool, readCapability: async (...args) => { asked.push(args); return true; },
  });
  await plugin.config({provider: {}});
  assert.deepEqual(Object.keys(plugin.tool), []);
  assert.deepEqual(asked, []);
});

// OpenCode 1.18.31 loads every export of a server plugin module as a plugin
// and refuses the module when one is not ("Plugin export is not a function",
// packages/opencode/src/plugin/index.ts getLegacyPlugins). plugin.js exports
// its default alone; the tool choices live in tool-config.js.
test('the server plugin module exports only its plugin function', async () => {
  const module = await import('../plugin.js');
  assert.deepEqual(Object.keys(module), ['default']);
  assert.equal(typeof module.default, 'function');
});
