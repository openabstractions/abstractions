// OpenCode V2 runs the same plugin through setup(ctx). The fake context
// follows @opencode/plugin 2.0.24's declarations: transforms replay on
// reload, hooks register by name, events arrive as an async iterable.
import assert from 'node:assert/strict';
import test from 'node:test';
import entry from '../server.js';
import OpenAbstractionsPlugin from '../plugin.js';
import {inputSchema, jsonTool, setupV2, v2Model} from '../v2.js';
import {Availability} from '../availability.js';
import {inventoryMarkerOption} from '../inventory.js';
import {fastWatch, observable, until} from './fixtures/observe-inventory.mjs';

function offer(family, context = 32768) {
  return {family, names: [family], servers: ['local'], context: BigInt(context), contextSource: 'maximum',
    input: BigInt(Math.max(context - 4096, 0)), unknown: []};
}

function fakeContext(options = {}, initialProviders = []) {
  const transforms = {provider: [], tool: []};
  const providers = new Map();
  const tools = new Map();
  const hooks = {language: [], context: [], title: []};
  const dispatched = [];
  const dispatchContext = async (event) => {
    for (const {fn, scope} of hooks.context) {
      if (scope?.providerID && scope.providerID !== event.model?.providerID) continue;
      await fn(event);
    }
    dispatched.push(event);
    return event;
  };
  const events = [];
  let wake = null;
  const record = (value) => value && ({provider: value.info,
    models: new Map(value.models.map((model) => [model.modelID, model])),
    ...(value.sourceConnection === undefined ? {} : {sourceConnection: value.sourceConnection})});
  const replay = () => {
    providers.clear();
    for (const provider of initialProviders) providers.set(provider.info.id, provider);
    const editor = {get: (id) => record(providers.get(id)), remove: (id) => providers.delete(id),
      list: () => [...providers.values()].map(record),
      add: ({info, models, sourceConnection}) => providers.set(info.id, {info, models,
        ...(sourceConnection === undefined ? {} : {sourceConnection})})};
    for (const transform of transforms.provider) transform(editor);
  };
  const ctx = {
    options, location: {directory: 'C:\\project'},
    provider: {transform: async (fn) => {
      transforms.provider.push(fn); replay();
      let active = true;
      return {dispose: async () => {
        if (!active) return;
        active = false; transforms.provider.splice(transforms.provider.indexOf(fn), 1);
        replay(); ctx.disposals += 1;
      }};
    }, reload: async () => { replay(); ctx.reloads += 1; }},
    tool: {transform: async (fn) => { transforms.tool.push(fn); fn({add: (tool) => tools.set(tool.name, tool)}); }},
    aisdk: {hook: async (name, fn, scope) => { hooks.language.push({name, fn, scope}); }},
    session: {hook: async (name, fn, scope) => { hooks[name].push({name, fn, scope}); }},
    event: {subscribe: ({signal} = {}) => ({async* [Symbol.asyncIterator]() {
      while (!signal?.aborted) {
        if (events.length) { yield events.shift(); continue; }
        await new Promise((resolve) => { wake = resolve; signal?.addEventListener('abort', resolve, {once: true}); });
      }
    }})},
    reloads: 0, disposals: 0,
  };
  return {ctx, providers, tools, hooks, dispatchContext, dispatched, emit: (event) => { events.push(event); wake?.(); }};
}

test('the ./server entry carries both plugin APIs under one id', () => {
  assert.equal(entry.id, 'openabstractions');
  assert.equal(entry.server, OpenAbstractionsPlugin, 'OpenCode 1.18 calls server');
  assert.equal(typeof entry.setup, 'function', 'OpenCode V2 calls setup');
  assert.deepEqual(Object.keys(entry).sort(), ['id', 'server', 'setup']);
});

test('V2 context uses each host session ID and its title hook swaps the conversation marker for the title marker', async t => {
  const {ctx, dispatchContext, hooks} = fakeContext();
  const cleanup = await setupV2(ctx, {languageModel: () => ({}), pluginDependencies: {
    env: {}, connectionRequired: false, probeRuntime: async () => true,
    readInventory: async () => ({offers: []}), readCapability: async () => false,
    readSelectionPolicy: async () => ({outcome: 'unknown'}),
  }});
  t.after(cleanup);
  for (const [agent, sessionID] of [['build','main-session'],['explore','child-session']]) {
    const event = {agent,sessionID,model:{providerID:'openabstractions',modelID:'m'},options:{keep:true,oaSessionID:'stale'},system:[]};
    await dispatchContext(event);
    assert.equal(event.options.oaSessionID,sessionID);
    assert.equal(event.options.keep,true);
  }
  const title = {sessionID:'main-session',options:{keep:true,oaSessionID:'main-session'}};
  for (const {fn} of hooks.title) await fn(title);
  assert.deepEqual(title.options,{keep:true,oaTitle:true});
});

test('V2 gets OA\'s models, the language model, the tools and the local-only request option', async (t) => {
  let offers = [offer('qwen3')];
  const languageModels = [];
  let clock = 1000;
  let watch;
  const read = async (_options, placement) => ({offers: placement === 'hosted' ? [] : offers});
  const live = observable(read);
  const {ctx, providers, tools, hooks, dispatchContext, dispatched} = fakeContext({tools: {selection_policy: true}, localAgent: 'local'});
  const cleanup = await setupV2(ctx, {
    now: () => clock,
    languageModel: (modelID) => { languageModels.push(modelID); return {modelId: modelID}; },
    pluginDependencies: {env: {}, availability: new Availability(), now: () => clock,
      onInventoryWatch: (value) => { watch = value; }, inventoryWatchTiming: fastWatch, observeInventory: live.observe,
      probeRuntime: async () => true, readInventory: read, readCapability: async () => false, readSelectionPolicy: async () => ({outcome: 'unknown'})},
  });
  t.after(cleanup);
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

  const policy = tools.get('oa_selection_manage');
  assert.equal(policy.input.type, 'object');
  assert.ok(policy.input.properties.action.enum.includes('read'));
  assert.ok(policy.input.required.includes('action'));

  const request = {agent: 'local', model: provider.models.find((model) => model.modelID === 'qwen3'), options: {}, system: []};
  await dispatchContext(request);
  assert.equal(request.options.oaLocalOnly, true);
  const before = dispatched.length;
  await assert.rejects(dispatchContext({agent: 'local', model: {providerID: 'foreign', api: {npm: '@ai-sdk/openai-compatible'}}, options: {}, system: []}), /Local assistant/);
  await assert.rejects(dispatchContext({agent: 'local', model: {...request.model, api: {npm: '@ai-sdk/openai-compatible'}}, options: {}, system: []}), /Local assistant/, 'explicit foreign SDK metadata cannot borrow the registered native provider');
  await assert.rejects(dispatchContext({agent: 'local', options: {}, system: []}), /Local assistant/, 'the shim never invents a selected model');
  assert.equal(dispatched.length, before, 'Local failures precede all scoped SDK dispatch');
  const build = {agent: 'build', model: {providerID: 'openabstractions'}, options: {}, system: []};
  await dispatchContext(build);
  assert.equal(build.options.oaLocalOnly, undefined);
  const foreignBuild = {agent: 'build', model: {providerID: 'foreign'}, options: {keep: true}, system: []};
  await dispatchContext(foreignBuild);
  assert.deepEqual(foreignBuild.options, {keep: true}, 'Build keeps its actual foreign selection');

  // An unchanged inventory reloads nothing; a model the router's survey
  // adds is read again and V2's providers reload with the new list.
  const quiet = () => new Promise((resolve) => setTimeout(resolve, 100));
  await quiet();
  assert.equal(ctx.reloads, 0);
  offers = [offer('qwen3'), offer('gemma4', 8192)];
  live.renew();
  await until(() => ctx.reloads === 1);
  assert.ok(providers.get('openabstractions').models.some((model) => model.modelID === 'gemma4'));
  live.renew();
  await quiet();
  assert.equal(ctx.reloads, 1, 'a reloaded inventory is the new baseline');
  await cleanup();
  assert.equal(watch.stopped, true, 'cleanup stops the watch');
});

test('a V2 tool runs the same definition and answers without an approval method', async () => {
  const {ctx, tools} = fakeContext({tools: {watch_logs: true}});
  await setupV2(ctx, {languageModel: () => ({}),
    pluginDependencies: {env: {}, availability: new Availability(), probeRuntime: async () => true, readInventory: async () => ({offers: []}), readCapability: async () => false}});
  const watch = tools.get('oa_logs_watch');
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
    probeRuntime: async () => true, readInventory: async () => { throw Object.assign(new Error('away'), {name: 'ResolutionError', status: 'runtime_unavailable'}); },
    readCapability: async () => false}});
  assert.deepEqual(providers.get('openabstractions').models, []);
});

test('V2 refreshes every verified native heading, marks stopped catalogues stale, and preserves the chosen model', async (t) => {
  const base = 'oa-custom';
  const raw = (id) => ({name: id, limit: {context: 8192, output: 4096}, tool_call: true});
  const source = (id, placement, model) => ({info: {id, name: placement, activation: 'enabled',
    package: '@openabstractions/opencode', settings: {oaPlacement: placement, runtimeEndpoint: `${placement}-runtime`,
      [inventoryMarkerOption]: {[model]: raw(model)}}}, models: [v2Model(id, model, {...raw(model),
    capabilities: {toolcall: true, input: {text: true}, output: {text: true}}})]});
  const foreign = {info: {id: `${base}-foreign`, name: 'foreign', package: '@ai-sdk/openai-compatible'},
    models: [v2Model(`${base}-foreign`, 'external', {name: 'external'})]};
  const generic = {info: {id: `${base}-generic`, name: 'generic', package: '@openabstractions/inference'}, models: []};
  const local = source(base, 'local', 'local-old');
  const manual = {...v2Model(base, 'manual', {name: 'My model'}), variants: ['person-variant'],
    cost: [{input: 1, output: 2}, {input: 3, output: 4}], settings: {personSetting: true}};
  local.models.push(manual);
  const hosted = source(`${base}-hosted`, 'hosted', 'hosted-old');
  const connection = Object.freeze({id: 'bound-account-fixture', integrationID: 'fixture'});
  hosted.sourceConnection = connection;
  const {ctx, providers, emit, dispatchContext} = fakeContext({providerId: base, localAgent: 'local'}, [
    local, hosted, foreign, generic]);
  const chosen = Object.freeze({providerID: base, modelID: 'local-old'});
  ctx.model = {default: async () => chosen, transform: () => { throw Error('refresh must not set a default'); }};
  ctx.session.switchModel = () => { throw Error('refresh must not change the chosen model'); };
  let clock = 1000;
  let failure = null;
  let revision = 'old';
  const reads = [];
  const logs = [];
  const read = async (options, placement) => {
    reads.push([options.runtimeEndpoint, placement]);
    if (failure) throw failure;
    return {offers: [offer(`${placement}-${revision}`, 8192)]};
  };
  const live = observable(read);
  const cleanup = await setupV2(ctx, {now: () => clock, languageModel: () => ({}),
    client: {app: {log: async ({body}) => logs.push(body)}},
    pluginDependencies: {env: {}, availability: new Availability(), now: () => clock,
      inventoryWatchTiming: fastWatch, observeInventory: live.observe,
      readCapability: async () => false, readSelectionPolicy: async () => ({outcome: 'unknown'}),
      probeRuntime: async () => true, readInventory: read}});
  t.after(cleanup);
  assert.deepEqual(reads.slice(0, 3), [['local-runtime', 'local'], ['local-runtime', 'hosted'], ['hosted-runtime', 'hosted']]);
  assert.equal(ctx.disposals, 1, 'the snapshot registration is disposed before publishing sources');
  assert.equal(providers.get(hosted.info.id).sourceConnection, connection);
  assert.deepEqual(providers.get(foreign.info.id), foreign);
  assert.deepEqual(providers.get(generic.info.id), generic);
  assert.deepEqual(await ctx.model.default(), chosen);
  // The router's survey wakes the watch; a failure counts at its second read
  // and a runtime that answers again at once.
  const surveyed = async (condition) => {
    live.renew();
    await until(condition, 3000, 'the watch did not reload the V2 providers');
    assert.equal(providers.get(hosted.info.id).sourceConnection, connection, 'each replay retains the exact account binding');
  };
  failure = Object.assign(new Error('native initial open failed'), {status: 10, transferred: 0});
  await surveyed(() => [base, `${base}-hosted`].every((id) => /unavailable/.test(providers.get(id).info.name)));
  for (const id of [base, `${base}-hosted`]) {
    assert.match(providers.get(id).info.name, /unavailable \(last known models\)/);
    assert.ok(providers.get(id).models.every((model) => model.enabled === false));
  }
  assert.deepEqual(await ctx.model.default(), chosen);
  failure = null; revision = 'new';
  await surveyed(() => providers.get(base).info.name === 'local' && providers.get(`${base}-hosted`).info.name === 'hosted');
  for (const [id, placement] of [[base, 'local'], [`${base}-hosted`, 'hosted']]) {
    const provider = providers.get(id);
    assert.equal(provider.info.name, placement);
    assert.deepEqual(provider.models.map((model) => model.modelID).sort(),
      (placement === 'local' ? ['manual', 'local-new'] : ['hosted-new']).sort());
    assert.ok(provider.models.every((model) => model.enabled));
  }
  assert.deepEqual(providers.get(base).models.find((model) => model.modelID === 'manual'), manual);
  for (const error of [Object.assign(new Error('proof refused'), {status: 8, transferred: 0}),
    new TypeError('malformed catalogue'), new Error('pin verification refused')]) {
    failure = error;
    await surveyed(() => /refresh failed \(last known models\)/.test(providers.get(base).info.name));
    assert.match(providers.get(base).info.name, /refresh failed \(last known models\)/);
    assert.doesNotMatch(providers.get(base).info.name, /unavailable/);
    assert.ok(providers.get(base).models.every((model) => !model.enabled));
  }
  assert.ok(logs.some((entry) => entry.level === 'error' && /refresh failed/.test(entry.message)));
  assert.deepEqual(await ctx.model.default(), chosen);
  const build = {agent: 'build', model: {providerID: foreign.info.id, modelID: 'external'}, options: {}, system: []};
  await dispatchContext(build);
  assert.equal(build.model.modelID, 'external');
  await assert.rejects(dispatchContext({agent: 'local', model: foreign.models[0], options: {}, system: []}), /Local assistant/);
  await cleanup();
});

test('V2 startup uses each heading\'s typed result and keeps a healthy sibling enabled', async () => {
  const errors = [Object.assign(new Error('native proof refused'), {status: 8, transferred: 0}),
    new TypeError('malformed catalogue'), new Error('pin verification refused'), new Error('runtime_unavailable text'),
    Object.assign(new Error('stopped runtime'), {status: 2, transferred: 0}),
    Object.assign(new Error('initial open failed'), {status: 10, transferred: 0})];
  for (const error of errors) for (const duringProbe of [false, true]) {
    let localProbes = 0, localReads = 0;
    const source = (id, placement) => {
      const cached = {name: `${placement}-cached`, limit: {context: 8192, output: 4096}, tool_call: true};
      return {info: {id, name: placement, package: '@openabstractions/opencode', activation: 'enabled',
        settings: {runtimeEndpoint: `${placement}-runtime`, oaPlacement: placement,
          [inventoryMarkerOption]: {[cached.name]: cached}}}, models: [v2Model(id, cached.name, {...cached,
        capabilities: {toolcall: true, input: {text: true}, output: {text: true}}})]};
    };
    const {ctx, providers} = fakeContext({providerId: 'oa-custom'}, [
      source('oa-custom', 'local'), source('oa-custom-hosted', 'hosted')]);
    const cleanup = await setupV2(ctx, {languageModel: () => ({}), client: {app: {log: async () => {}}},
      pluginDependencies: {env: {}, availability: new Availability(),
        probeRuntime: async (options) => {
          if (options.runtimeEndpoint === 'local-runtime') {
            localProbes++;
            if (duringProbe) throw error;
          }
          return true;
        }, readInventory: async (_options, placement) => {
          if (placement === 'local') {
            localReads++;
            if (!duringProbe) throw error;
          }
          return {offers: [offer('hosted-live')]};
        }, readCapability: async () => false, readSelectionPolicy: async () => ({outcome: 'unknown'})}});
    const local = providers.get('oa-custom');
    assert.match(local.info.name, [2, 10].includes(error.status) ? /unavailable \(last known models\)/ : /refresh failed \(last known models\)/);
    assert.ok(local.models.every((model) => !model.enabled));
    const hosted = providers.get('oa-custom-hosted');
    assert.equal(hosted.info.name, 'hosted');
    assert.ok(hosted.models.every((model) => model.enabled));
    assert.ok(hosted.models.some((model) => model.modelID === 'hosted-live'));
    assert.ok(localProbes > 0, 'the local runtime probe ran');
    assert.equal(localReads > 0, !duringProbe, 'only a successful probe reaches local inventory');
    await cleanup();
  }
});

test('V2 never replaces a configured foreign or generic package under its primary provider ID', async () => {
  for (const packageName of ['@ai-sdk/openai-compatible', '@openabstractions/inference']) {
    const foreign = {info: {id: 'oa-custom', name: 'person-owned', package: packageName}, models: []};
    const {ctx, providers, dispatchContext} = fakeContext({providerId: 'oa-custom', localAgent: 'local'}, [foreign]);
    let reads = 0;
    const cleanup = await setupV2(ctx, {languageModel: () => { throw Error('foreign SDK hook must stay absent'); },
      pluginDependencies: {env: {}, availability: new Availability(),
        probeRuntime: async () => true, readInventory: async () => { reads++; return {offers: []}; }}});
    assert.deepEqual(providers.get('oa-custom'), foreign);
    assert.equal(reads, 0);
    await assert.rejects(dispatchContext({agent: 'local', model: {providerID: 'oa-custom'}, options: {}, system: []}), /Local assistant/);
    await cleanup();
  }
});
