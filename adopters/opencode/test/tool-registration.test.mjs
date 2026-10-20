import test from 'node:test';
import assert from 'node:assert/strict';
import OpenAbstractionsPlugin from '../plugin.js';
import {capabilityTools, companionTools, toolChoices, toolNames, toolNameFor} from '../tool-config.js';
import {messageToolNames, messageInstructions} from '../messages.js';
import {tool as fakeTool} from '@opencode-ai/plugin/tool';

const capabilityNames = plugin => Object.keys(plugin.tool).filter(name => !messageToolNames.includes(name)&&name!=='oa_models_connect').sort();
const config = (options = {}) => ({provider: {openabstractions: {npm: '@openabstractions/opencode', options, models: {}}}});

test('private host operations declare one canonical name and retain their option keys', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {}}, {compactTools: true,
    tools: Object.fromEntries(toolNames.map(name => [name, true]))}, {tool: fakeTool});
  try {
    const names = Object.keys(plugin.tool);
    assert.equal(new Set(names).size, names.length);
    assert.ok(names.every(name => /^oa_[a-z]+_[a-z_]+$/.test(name)));
    assert.deepEqual(names.sort(), [...toolNames.map(toolNameFor), 'oa_tools_list'].sort());
    assert.ok(!names.some(name => name.startsWith('openabstractions_')));
  } finally { await plugin.dispose(); }
});

test('installed first use requests the common native connection once across configuration reloads', async () => {
  let connections = 0;
  const order = [];
  const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {tool: fakeTool,
    requestOpenCodeConnection: async () => { connections++; order.push('connect'); return {outcome: 'connected', hostRegistered: true, helperEnrolled: true}; },
    checkOpenCodeConnection: async () => ({outcome: 'connected', hostRegistered: true, helperEnrolled: true}),
    probeRuntime: async () => { order.push('probe'); return true; }, readInventory: async () => ({offers: []}),
    readSelectionPolicy: async () => ({outcome: 'unknown'}), readCapability: async () => false});
  try {
    await plugin.config(config()); await plugin.config(config()); assert.equal(connections, 1);
    assert.ok(order.includes('connect'));
    const output = {system: ['Host instructions']};
    await plugin['experimental.chat.system.transform']({}, output);
    await plugin['experimental.chat.system.transform']({}, output);
    assert.deepEqual(output.system, ['Host instructions', messageInstructions]);
    assert.match(messageInstructions, /message Claude/);
    assert.match(messageInstructions, /oa_messages_prepare/);
  }
  finally { await plugin.dispose(); }
});

test('a fresh installed popup blocks every tool until Connect completes while OpenCode lists OA models', async () => {
  let popups = 0, probes = 0, effects = 0;
  const plugin = await OpenAbstractionsPlugin({client: {}}, {compactTools: true, tools: {generate_image: true}}, {
    tool: fakeTool, connectionRequired: true,
    requestOpenCodeConnection: async () => { popups++; return true; },
    checkOpenCodeConnection: async () => ({outcome: 'connect_pending'}),
    probeRuntime: async () => { probes++; return true; },
    readInventory: async () => { effects++; return {offers: []}; },
    readCapability: async () => { effects++; return true; },
  });
  try {
    await plugin.config(config()); await plugin.config(config());
    for (const definition of Object.values(plugin.tool)) {
      assert.equal(JSON.parse(await definition.execute({}, {})).outcome, 'connect_pending');
    }
    assert.equal(popups, 1);
    // OpenCode's own reads of the model list and capabilities go on: the
    // runtime authorizes them for the stock host, and Connect guards tools.
    assert.equal(probes, 2);
    assert.ok(effects > 0);
    assert.ok(await plugin.provider.models({id: 'openabstractions', models: {}}));
  } finally { await plugin.dispose(); }
});

async function registered(pluginOptions, served = [], providerOptions = {}) {
  const asked = [];
  const logs = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry.body)}}}, pluginOptions, {
    tool: fakeTool,
    probeRuntime: async () => true,
    readInventory: async () => ({offers: []}),
    readCapability: async (options, profile, placement) => {
      asked.push([profile, placement, options.runtimeEndpoint]);
      if (profile === 'fail') throw new Error('unreachable');
      return served.includes(profile);
    },
  });
  await plugin.config(config(providerOptions));
  return {names: capabilityNames(plugin), asked, logs, plugin};
}

test('non-messaging capabilities register their tools only when served', async () => {
  assert.deepEqual((await registered({})).names, []);
  assert.deepEqual((await registered({}, ['image'])).names, ['oa_logs_read_failure', 'oa_image_generate'].sort());
  assert.deepEqual((await registered({}, ['speech'])).names, ['oa_speech_speak']);
  const both = await registered({}, ['image', 'speech'], {runtimeEndpoint: 'controlled'});
  assert.deepEqual(both.names, ['oa_logs_read_failure', 'oa_image_generate', 'oa_speech_speak'].sort());
  assert.deepEqual(both.asked, [['image', 'local', 'controlled'], ['speech', 'local', 'controlled'], ['video', 'local', 'controlled']]);
  assert.deepEqual(capabilityTools, {generate_image: 'image', speak: 'speech', oa_video_generate: 'video'});
  assert.deepEqual(companionTools, {generate_image: ['failure_logs']});
  const without = await registered({tools: {failure_logs: false}}, ['image']);
  assert.deepEqual(without.names, ['oa_image_generate'], 'the tools option turns the companion off');
});

test('a remote-scope provider looks for capabilities on any placement', async () => {
  const {asked} = await registered({}, ['image'], {scope: 'remote'});
  assert.deepEqual(asked.map(([, placement]) => placement), ['any', 'any', 'any']);
});

test('tools that open the Panel or edit preferences register only when enabled', async () => {
  const opt = {selection_policy: true, watch_logs: true, open_credentials_panel: true};
  const {names, plugin} = await registered({tools: opt}, ['image', 'speech']);
  assert.deepEqual(names, ['oa_logs_read_failure', 'oa_image_generate',
    'oa_credentials_open', 'oa_selection_manage', 'oa_speech_speak',
    'oa_logs_watch'].sort());
  assert.equal(typeof plugin.tool.oa_credentials_open.execute, 'function');
});

test('an explicit tools option registers before startup and overrides the capability check either way', async () => {
  const forced = await registered({tools: {generate_image: true}}, []);
  assert.deepEqual(forced.names, ['oa_logs_read_failure', 'oa_image_generate'].sort());
  assert.deepEqual(forced.asked.map(([profile]) => profile), ['speech', 'video'], 'an enabled tool is not checked');
  const off = await registered({tools: {generate_image: false, speak: false, oa_video_generate: false}}, ['image', 'speech', 'video']);
  assert.deepEqual(off.names, []);
  assert.deepEqual(off.asked, [], 'a disabled tool is not checked');
  const early = await OpenAbstractionsPlugin({client: {}}, {tools: {watch_logs: true}}, {tool: fakeTool});
  assert.deepEqual(Object.keys(early.tool), ['oa_logs_watch'], 'enabled tools exist before the config hook');
});

test('an unreadable capability registers nothing and says so; unknown options are ignored with a warning', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async () => {}}}}, {}, {
    tool: fakeTool, probeRuntime: async () => true, readInventory: async () => ({offers: []}),
    readCapability: async () => { throw Object.assign(new Error('down'), {code: 'unavailable'}); },
  });
  await plugin.config(config());
  assert.deepEqual(capabilityNames(plugin), []);
  const {names, logs} = await registered({tools: {shell: true, speak: 'yes', generate_image: true}});
  assert.deepEqual(names, ['oa_logs_read_failure', 'oa_image_generate'].sort());
  assert.ok(logs.some((entry) => entry.level === 'warn' && JSON.stringify(entry.extra.names) === '["shell","speak"]'));
  assert.deepEqual(toolChoices([]).unknown, ['tools']);
  assert.deepEqual(toolNames, ['generate_image', 'speak', 'selection_policy', 'watch_logs',
    'open_credentials_panel', 'failure_logs',...messageToolNames,'oa_models_connect']);
});

test('canonical Asks wrappers retain the enrolled helper boundary and reject obsolete option names', async () => {
  const {names, logs} = await registered({tools: {authorization_questions: true, oa_asks_answer: true}});
  assert.deepEqual(names, []);
  assert.ok(logs.some(entry => entry.extra?.names?.includes('authorization_questions')));
  assert.ok(!logs.some(entry => entry.extra?.names?.includes('oa_asks_answer')));
});

test('a typed refused router capability registers no supplied models as tools', async () => {
  for (const outcome of ['not_permitted', 'forbidden', 'unavailable', 'invalid', undefined]) {
    const logs = [];
    const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry.body)}}}, {}, {
      tool: fakeTool, readInventory: async () => ({offers: []}),
      resolveRouter: async () => ({offer: async () => ({outcome, offers: [{family: 'must not be consumed'}]})}),
    });
    await plugin.config(config());
    assert.deepEqual(capabilityNames(plugin), []);
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
