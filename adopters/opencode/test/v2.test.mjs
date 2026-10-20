// OpenCode V2 runs the same plugin through setup(ctx). The fake context
// follows @opencode/plugin 2.0.24's declarations: transforms replay on
// reload, hooks register by name, events arrive as an async iterable.
import assert from 'node:assert/strict';
import test from 'node:test';
import entry from '../server.js';
import OpenAbstractionsPlugin from '../plugin.js';
import {inputSchema, jsonTool, setupV2, v2Model} from '../v2.js';
import {Availability} from '../availability.js';

function offer(family, context = 32768) {
  return {family, names: [family], servers: ['local'], context: BigInt(context), contextSource: 'maximum',
    input: BigInt(Math.max(context - 4096, 0)), unknown: []};
}

function fakeContext(options = {}) {
  const transforms = {provider: [], tool: []};
  const providers = new Map();
  const tools = new Map();
  const hooks = {language: [], context: []};
  const events = [];
  let wake = null;
  const replay = () => {
    providers.clear();
    const editor = {get: (id) => providers.get(id), remove: (id) => providers.delete(id),
      add: ({info, models}) => providers.set(info.id, {info, models})};
    for (const transform of transforms.provider) transform(editor);
  };
  const ctx = {
    options, location: {directory: 'C:\\project'},
    provider: {transform: async (fn) => { transforms.provider.push(fn); replay(); }, reload: async () => { replay(); ctx.reloads += 1; }},
    tool: {transform: async (fn) => { transforms.tool.push(fn); fn({add: (tool) => tools.set(tool.name, tool)}); }},
    aisdk: {hook: async (name, fn, scope) => { hooks.language.push({name, fn, scope}); }},
    session: {hook: async (name, fn, scope) => { hooks.context.push({name, fn, scope}); }},
    event: {subscribe: ({signal} = {}) => ({async* [Symbol.asyncIterator]() {
      while (!signal?.aborted) {
        if (events.length) { yield events.shift(); continue; }
        await new Promise((resolve) => { wake = resolve; signal?.addEventListener('abort', resolve, {once: true}); });
      }
    }})},
    reloads: 0,
  };
  return {ctx, providers, tools, hooks, emit: (event) => { events.push(event); wake?.(); }};
}

test('the ./server entry carries both plugin APIs under one id', () => {
  assert.equal(entry.id, 'openabstractions');
  assert.equal(entry.server, OpenAbstractionsPlugin, 'OpenCode 1.18 calls server');
  assert.equal(typeof entry.setup, 'function', 'OpenCode V2 calls setup');
  assert.deepEqual(Object.keys(entry).sort(), ['id', 'server', 'setup']);
});

test('V2 gets OA\'s models, the language model, the tools and the local-only request option', async () => {
  let offers = [offer('qwen3')];
  const languageModels = [];
  let clock = 1000;
  const {ctx, providers, tools, hooks, emit} = fakeContext({tools: {selection_policy: true}, localAgent: 'local'});
  const cleanup = await setupV2(ctx, {
    now: () => clock,
    languageModel: (modelID) => { languageModels.push(modelID); return {modelId: modelID}; },
    pluginDependencies: {env: {}, availability: new Availability(), now: () => clock,
      readInventory: async () => ({offers}), readCapability: async () => false, readSelectionPolicy: async () => ({outcome: 'unknown'})},
  });
  const provider = providers.get('openabstractions');
  assert.equal(provider.info.package, '@openabstractions/opencode');
  assert.equal(provider.info.activation, 'enabled');
  assert.ok(provider.models.some((model) => model.modelID === 'qwen3' && model.providerID === 'openabstractions'));
  const qwen = provider.models.find((model) => model.modelID === 'qwen3');
  assert.equal(qwen.limit.context, 32768);
  assert.equal(qwen.enabled, true);

  assert.deepEqual(hooks.language.map(({name, scope}) => [name, scope]), [['language', {providerID: 'openabstractions'}]]);
  const event = {model: {modelID: 'qwen3', providerID: 'openabstractions'}};
  hooks.language[0].fn(event);
  assert.deepEqual(event.language, {modelId: 'qwen3'});

  const policy = tools.get('openabstractions_selection_policy');
  assert.equal(policy.input.type, 'object');
  assert.ok(policy.input.properties.action.enum.includes('read'));
  assert.ok(policy.input.required.includes('action'));

  const request = {agent: 'local', model: {providerID: 'openabstractions', id: 'qwen3'}, options: {}, system: []};
  await hooks.context[0].fn(request);
  assert.equal(request.options.oaLocalOnly, true);
  const build = {agent: 'build', model: {providerID: 'openabstractions'}, options: {}, system: []};
  await hooks.context[0].fn(build);
  assert.equal(build.options.oaLocalOnly, undefined);

  // A reply within a minute of the last read changes nothing; a later one
  // reads OA again and reloads V2's providers with the new list.
  offers = [offer('qwen3'), offer('gemma4', 8192)];
  emit({type: 'session.idle'});
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(ctx.reloads, 0);
  clock += 61000;
  emit({type: 'session.updated'});
  emit({type: 'session.idle'});
  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.equal(ctx.reloads, 1);
  assert.ok(providers.get('openabstractions').models.some((model) => model.modelID === 'gemma4'));
  await cleanup();
});

test('a V2 tool runs the same definition and answers without an approval method', async () => {
  const {ctx, tools} = fakeContext({tools: {watch_logs: true}});
  await setupV2(ctx, {languageModel: () => ({}),
    pluginDependencies: {env: {}, availability: new Availability(), readInventory: async () => ({offers: []}), readCapability: async () => false}});
  const watch = tools.get('openabstractions_watch_logs');
  const result = await watch.execute({sourceId: 'fixture/system', durationMs: 10, maxRecords: 1, maxBytes: 64},
    {sessionID: 's', messageID: 'm', agent: 'build', id: 'c', signal: new AbortController().signal, progress: async () => {}});
  assert.match(result.content, /approval_unavailable/);
});

test('JSON Schema from the V1 tool helper shape', () => {
  const s = jsonTool.schema;
  const definition = jsonTool({args: {name: s.string(), count: s.number().optional(), kind: s.enum(['a', 'b']),
    rows: s.array(s.object({id: s.string(), on: s.boolean().optional()})).optional()}});
  assert.deepEqual(inputSchema(definition), {type: 'object', additionalProperties: false, required: ['name', 'kind'], properties: {
    name: {type: 'string'}, count: {type: 'number'}, kind: {type: 'string', enum: ['a', 'b']},
    rows: {type: 'array', items: {type: 'object', additionalProperties: false, required: ['id'], properties: {id: {type: 'string'}, on: {type: 'boolean'}}}}}});
  assert.deepEqual(v2Model('oa', 'm', {name: 'M', capabilities: {toolcall: true, input: {text: true, image: true}, output: {text: true}},
    limit: {context: 100, output: 10}, cost: {input: 0, output: 0}}).capabilities, {tools: true, input: ['text', 'image'], output: ['text']});
});

test('without OA the V2 start adds the provider with no models and goes on', async () => {
  const {ctx, providers} = fakeContext();
  await setupV2(ctx, {languageModel: () => ({}), pluginDependencies: {env: {}, availability: new Availability(),
    readInventory: async () => { throw Object.assign(new Error('away'), {name: 'ResolutionError', status: 'runtime_unavailable'}); },
    readCapability: async () => false}});
  assert.deepEqual(providers.get('openabstractions').models, []);
});
