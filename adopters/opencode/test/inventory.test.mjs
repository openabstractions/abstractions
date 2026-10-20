import test from 'node:test';
import assert from 'node:assert/strict';
import {resolve} from 'node:path';
import {servableChatModels, refreshProviderInventory, refreshSelectionPolicyModels, inventoryMarkerOption, selectionPolicyMarkerOption, readOffers} from '../inventory.js';
import OpenAbstractionsPlugin from '../plugin.js';
import {Availability} from '../availability.js';
import {readFileSync} from 'node:fs';

const contextTable = JSON.parse(readFileSync(new URL('./fixtures/inventory-context.json', import.meta.url), 'utf8'));

test('router typed refusals never become a successful empty or supplied inventory', async () => {
  for (const outcome of ['not_permitted', 'forbidden', 'unavailable', 'invalid', undefined]) {
    const client = {offer: async () => ({outcome, offers: [{family: 'must not be consumed'}]})};
    await assert.rejects(readOffers(client, 'local'), (error) => error.code === (outcome ?? 'invalid_result'));
  }
  const snapshot = {outcome: 'offered', offers: []};
  assert.equal(await readOffers({offer: async () => snapshot}, 'local'), snapshot);
});

// offer is one family of the router's Offer answer at the plugin's 4,096-token
// reserve (router CONTRACT.md ROUTE-D8), in the router binding's shape.
function offer(family, context = 32768, fields = {}) {
  return {family, names: [family], servers: ['local'], context: BigInt(context), contextSource: 'maximum',
    input: BigInt(Math.max(context - 4096, 0)), unknown: [], ...fields};
}

function unknownOffer(family, unknown, fields = {}) {
  return offer(family, 0, {contextSource: 'unknown', unknown, ...fields});
}

const fakeTool = Object.assign((definition) => definition, {schema: {
  object: (value) => value,
  string: () => ({optional() { return this; }}),
  number: () => ({optional() { return this; }}),
  boolean: () => ({optional() { return this; }}),
  enum: (value) => ({value, optional() { return this; }}),
  array: (value) => ({value, optional() { return this; }}),
}});

test('inventory projects each offer with input under its family, context and place', () => {
  const models = servableChatModels([
    offer('small', 4096, {contextSource: 'effective'}),
    offer('mixed', 8192, {names: ['a-unknown', 'b-loaded'], contextSource: 'effective'}),
    offer('voice', 0, {contextSource: 'none'}),
  ]);

  assert.deepEqual(Object.keys(models), ['mixed']);
  assert.equal(models.mixed.name, 'mixed · 8k · this computer: local');
  assert.deepEqual(models.mixed.limit, {context: 8192, output: 4096});
});

test('an unknown offer withholds its family and names each alias', () => {
  const diagnostics = [];
  const models = servableChatModels([
    unknownOffer('only-lemonade', ['lemonade/lemonade-qwen']),
    unknownOffer('mixed', ['lemonade/lemonade-unknown'], {names: ['lemonade-unknown', 'ollama-known'], servers: ['lemonade', 'ollama']}),
    offer('independent', 16384, {contextSource: 'effective'}),
  ], diagnostics);
  assert.deepEqual(Object.keys(models), ['independent']);
  assert.deepEqual(models.independent.limit, {context: 16384, output: 4096});
  assert.deepEqual(diagnostics.map((item) => item.model), ['lemonade/lemonade-qwen', 'lemonade/lemonade-unknown']);
  assert.match(diagnostics[0].reason, /its server reported no request limit/);
});

test('refresh replaces only untouched managed entries and preserves edits and custom models', () => {
  const old = {name: 'Old', limit: {context: 32768, output: 4096}, tool_call: true};
  const customized = {...old, name: 'My preferred name'};
  const provider = {
    options: {[inventoryMarkerOption]: {old: old, customized: old}},
    models: {old, customized, manual: {name: 'Manual model'}},
  };
  const result = refreshProviderInventory(provider, [
    offer('new'),
    offer('customized', 32768, {names: ['new-custom-name']}),
  ]);

  assert.deepEqual(provider.models, {
    customized,
    manual: {name: 'Manual model'},
    new: {name: 'new · 32k · this computer: local', limit: {context: 32768, output: 4096}, tool_call: true},
  });
  assert.deepEqual(result, {added: 1, removed: 1, preserved: 2, diagnostics: [], warnings: []});
});

test('a manual context limit above the observed context yields one plain warning per model', () => {
  const provider = {
    options: {},
    models: {
      qwen: {name: 'Qwen by hand', limit: {context: 32768, output: 4096}},
      tiny: {name: 'Tiny by hand', limit: {context: 8192, output: 4096}},
      fits: {name: 'Fits', limit: {context: 4096, output: 1024}},
      unlimited: {name: 'No limit'},
      unrelated: {name: 'Not served', limit: {context: 999999, output: 4096}},
    },
  };
  const result = refreshProviderInventory(provider, [
    offer('qwen', 4096, {names: ['Qwen3-0.6B-GGUF'], servers: ['lemonade'], contextSource: 'effective'}),
    offer('tiny', 2048, {contextSource: 'effective'}),
    offer('fits', 8192, {contextSource: 'effective'}),
    offer('unlimited', 8192, {contextSource: 'effective'}),
  ]);
  assert.deepEqual(result.warnings, [
    {model: 'qwen', configured: 32768, observed: 4096,
      reason: 'OpenCode limit.context for qwen is 32,768 tokens, but OpenAbstractions observes 4,096 tokens; lower it to 4,096 or remove the entry so inventory refresh manages it.'},
    {model: 'tiny', configured: 8192, observed: 2048,
      reason: 'OpenCode limit.context for tiny is 8,192 tokens, but OpenAbstractions observes 2,048 tokens; lower it to 2,048 or remove the entry so inventory refresh manages it.'},
  ]);
  assert.deepEqual(provider.models.qwen.limit, {context: 32768, output: 4096}, 'the user entry is reported, never rewritten');
});

test('the plugin logs a stale manual context limit as its own warning', async () => {
  const config = {provider: {openabstractions: {
    npm: '@openabstractions/opencode@0.4.0',
    options: {scope: 'local'},
    models: {qwen: {name: 'Qwen by hand', limit: {context: 32768, output: 4096}}},
  }}};
  const logs = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {}, {
    tool: fakeTool,
    readInventory: async () => ({offers: [offer('qwen', 4096, {servers: ['lemonade'], contextSource: 'effective'})]}),
  });
  await plugin.config(config);
  const warnings = logs.filter((entry) => entry.body.level === 'warn' && entry.body.extra.model === 'qwen');
  assert.equal(warnings.length, 1);
  assert.match(warnings[0].body.message, /limit\.context for qwen is 32,768 tokens, but OpenAbstractions observes 4,096 tokens/);
  assert.deepEqual({configured: warnings[0].body.extra.configured, observed: warnings[0].body.extra.observed}, {configured: 32768, observed: 4096});
});

test('the plugin logs an unknown-context alias by its server and name', async () => {
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models: {}}}};
  const logs = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {}, {
    tool: fakeTool,
    readInventory: async () => ({offers: [unknownOffer('qwen', ['lemonade/qwen-gguf'])]}),
  });
  await plugin.config(config);
  const omitted = logs.filter((entry) => entry.body.message === 'OpenAbstractions model is omitted because its server reported no request limit');
  assert.deepEqual(omitted.map((entry) => entry.body.extra.model), ['lemonade/qwen-gguf']);
  assert.equal(Object.hasOwn(config.provider.openabstractions.models, 'qwen'), false);
});

test('legacy config without a valid marker preserves old entries but owns fresh discoveries', () => {
  const provider = {options: {[inventoryMarkerOption]: 'invalid'}, models: {legacy: {name: 'Legacy'}}};
  refreshProviderInventory(provider, [offer('new')]);
  assert.deepEqual(provider.models.legacy, {name: 'Legacy'});
  assert.ok(provider.models.new);
  assert.ok(Object.hasOwn(provider.options[inventoryMarkerOption], 'new'));
  refreshProviderInventory(provider, [offer('next')]);
  assert.deepEqual(provider.models.legacy, {name: 'Legacy'});
  assert.equal(provider.models.new, undefined);
  assert.ok(provider.models.next);
});

test('prototype-named model and marker IDs stay ordinary own entries', () => {
  const generated = {name: 'before', limit: {context: 32768, output: 4096}, tool_call: true};
  const models = servableChatModels([offer('__proto__', 32768, {names: ['after']})]);
  assert.equal(Object.hasOwn(models, '__proto__'), true);
  assert.equal(Object.getPrototypeOf(models), Object.prototype);
  const provider = {
    options: {[inventoryMarkerOption]: JSON.parse('{"__proto__":' + JSON.stringify(generated) + '}')},
    models: JSON.parse('{"__proto__":' + JSON.stringify(generated) + '}'),
  };
  refreshProviderInventory(provider, [offer('__proto__', 32768, {names: ['after']})]);
  assert.equal(Object.hasOwn(provider.models, '__proto__'), true);
  assert.equal(provider.models.__proto__.name, '__proto__ · 32k · this computer: local');
  assert.equal(Object.getPrototypeOf(provider.models), Object.prototype);
});

test('policy models use available fallback candidates and the strictest known context', () => {
  const provider = {options: {oaSelectionPolicies: {'oa-preference-cloud-local': 'cloud-local'}}, models: {manual: {name: 'Manual'}}};
  const result = refreshSelectionPolicyModels(provider, {local: [
    offer('local-model', 8192, {contextSource: 'effective'}),
    offer('local-small', 6144, {contextSource: 'effective'}),
  ], hosted: []}, {'cloud-local': {outcome: 'found', policy: {name: 'cloud-local', candidates: [
    {model: 'hosted-model', placement: 'hosted', credential: 'deepseek'},
    {model: 'local-model', placement: 'local', credential: ''},
    {model: 'local-small', placement: 'local', credential: ''},
  ]}}});
  assert.deepEqual(provider.models['oa-preference-cloud-local'], {
    name: 'Preference: cloud-local', limit: {context: 6144, output: 4096}, tool_call: true,
  });
  assert.deepEqual(provider.models.manual, {name: 'Manual'});
  assert.equal(result.added, 1);
  assert.deepEqual(result.diagnostics, []);
});

test('policy candidates read the offers of their own placement', () => {
  const offersByPlacement = {local: [offer('shared', 65536)], hosted: [offer('shared', 8192)]};
  const reads = (placement) => ({'p': {outcome: 'found', policy: {name: 'p', candidates: [{model: 'shared', placement, credential: ''}]}}});
  const local = {options: {oaSelectionPolicies: {'oa-preference': 'p'}}, models: {}};
  refreshSelectionPolicyModels(local, offersByPlacement, reads('local'));
  assert.equal(local.models['oa-preference'].limit.context, 65536);
  const hosted = {options: {oaSelectionPolicies: {'oa-preference': 'p'}}, models: {}};
  refreshSelectionPolicyModels(hosted, offersByPlacement, reads('hosted'));
  assert.equal(hosted.models['oa-preference'].limit.context, 8192);
});

test('policy models are omitted when an eligible candidate has no input budget', () => {
  const provider = {options: {oaSelectionPolicies: {'oa-preference': 'local-first'}}, models: {}};
  const result = refreshSelectionPolicyModels(provider, {local: [
    offer('local-model', 4096, {contextSource: 'effective'}),
  ]}, {'local-first': {outcome: 'found', policy: {name: 'local-first', candidates: [
    {model: 'local-model', placement: 'local', credential: ''},
  ]}}});
  assert.equal(Object.hasOwn(provider.models, 'oa-preference'), false);
  assert.equal(result.diagnostics.length, 1);
});

test('a policy target matches an offer by family or by any of its names', () => {
  const provider = {options: {oaSelectionPolicies: {'oa-preference': 'local-first'}}, models: {}};
  const result = refreshSelectionPolicyModels(provider, {local: [
    offer('local-model', 8192, {names: ['local-model-loaded', 'local-model-unknown'], contextSource: 'effective'}),
  ]}, {'local-first': {outcome: 'found', policy: {name: 'local-first', candidates: [
    {model: 'local-model-unknown', placement: 'local', credential: ''},
  ]}}});
  assert.equal(provider.models['oa-preference'].limit.context, 8192);
  assert.deepEqual(result.diagnostics, []);
});

test('selection preference omits a family target whose offer has an unknown context', () => {
  const provider = {options: {oaSelectionPolicies: {'oa-preference': 'local-first'}}, models: {}};
  const result = refreshSelectionPolicyModels(provider, {local: [
    unknownOffer('local-model', ['lemonade/lemonade-unknown'], {names: ['lemonade-unknown', 'ollama-known']}),
  ]}, {'local-first': {outcome: 'found', policy: {name: 'local-first', candidates: [
    {model: 'local-model', placement: 'local', credential: ''},
  ]}}});
  assert.equal(Object.hasOwn(provider.models, 'oa-preference'), false);
  assert.match(result.diagnostics[0].reason, /unknown for eligible policy model lemonade\/lemonade-unknown/);
});

test('selection preference omits a concrete alias target when its family offer has an unknown context', () => {
  const provider = {options: {oaSelectionPolicies: {'oa-preference': 'local-first'}}, models: {}};
  const result = refreshSelectionPolicyModels(provider, {local: [
    unknownOffer('local-model', ['lemonade/lemonade-unknown'], {names: ['lemonade-unknown', 'ollama-known']}),
  ]}, {'local-first': {outcome: 'found', policy: {name: 'local-first', candidates: [
    {model: 'ollama-known', placement: 'local', credential: ''},
  ]}}});
  assert.equal(Object.hasOwn(provider.models, 'oa-preference'), false);
  assert.match(result.diagnostics[0].reason, /unknown for eligible policy model lemonade\/lemonade-unknown/);
});

test('selection preference explains a candidate whose server reported no request limit', () => {
  const provider = {options: {oaSelectionPolicies: {'oa-preference': 'local-first'}}, models: {}};
  const result = refreshSelectionPolicyModels(provider, {local: [
    unknownOffer('local-model', ['lemonade/lemonade-unknown']),
  ]}, {'local-first': {outcome: 'found', policy: {name: 'local-first', candidates: [
    {model: 'local-model', placement: 'local', credential: ''},
  ]}}});
  assert.equal(Object.hasOwn(provider.models, 'oa-preference'), false);
  assert.match(result.diagnostics[0].reason, /its server reported no request limit/);
});

test('policy model collisions and edited managed entries remain user-owned', () => {
  const generated = {name: 'Preference: local-first', limit: {context: 8192, output: 4096}, tool_call: true};
  const provider = {options: {
    oaSelectionPolicies: {collision: 'local-first', edited: 'edited-policy'},
    [selectionPolicyMarkerOption]: {edited: generated},
  }, models: {
    collision: {name: 'Custom model'},
    edited: {...generated, name: 'My named preference'},
  }};
  const result = refreshSelectionPolicyModels(provider, {local: [
    offer('local-model', 8192, {contextSource: 'effective'}),
  ]}, {
    'local-first': {outcome: 'found', policy: {name: 'local-first', candidates: [{model: 'local-model', placement: 'local', credential: ''}]}},
    'edited-policy': {outcome: 'found', policy: {name: 'edited-policy', candidates: [{model: 'local-model', placement: 'local', credential: ''}]}},
  });
  assert.deepEqual(provider.models.collision, {name: 'Custom model'});
  assert.equal(provider.models.edited.name, 'My named preference');
  assert.ok(result.diagnostics.some(({model}) => model === 'collision'));
  assert.equal(result.preserved, 2);
});

test('actual plugin config hook queries once and leaves credentials, defaults, and model selection intact', async () => {
  const old = {name: 'old', limit: {context: 32768, output: 4096}, tool_call: true};
  const config = {
    model: 'openabstractions/gone',
    provider: {openabstractions: {
      npm: '@openabstractions/opencode@0.4.0',
      options: {scope: 'local', credential: 'deepseek', runtimeEndpoint: 'controlled',
        [inventoryMarkerOption]: {gone: old}},
      models: {gone: old},
    }},
  };
  const logs = [];
  let calls = 0;
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {tools: {generate_image: true, speak: true, selection_policy: true, watch_logs: true, open_credentials_panel: true, authorization_questions: true}}, {
    tool: fakeTool,
    readInventory: async (options, placement) => {
      calls++;
      assert.equal(options.credential, 'deepseek');
      assert.equal(options.runtimeEndpoint, 'controlled');
      assert.equal(placement, 'local');
      return {offers: [offer('fresh')]};
    },
  });
  assert.equal(typeof plugin.tool.openabstractions_open_credentials_panel.execute, 'function');
  assert.equal(typeof plugin.tool.openabstractions_watch_logs.execute, 'function');
  await plugin.config(config);

  assert.equal(calls, 1);
  assert.equal(config.model, 'openabstractions/gone');
  assert.equal(config.provider.openabstractions.options.credential, 'deepseek');
  assert.equal(config.provider.openabstractions.models.gone, undefined);
  assert.ok(config.provider.openabstractions.models.fresh);
  assert.ok(logs.some((entry) => entry.body.level === 'warn' && entry.body.extra.model === 'gone'));
});

test('scope remote reads the offers of every placement, and the local placement to name where each runs', async () => {
  const placements = [];
  const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {
    tool: fakeTool,
    readInventory: async (_options, placement) => { placements.push(placement); return {offers: [offer('cloud')]}; },
  });
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'remote'}, models: {}}}};
  await plugin.config(config);
  assert.deepEqual(placements, ['any', 'local']);
  assert.ok(config.provider.openabstractions.models.cloud);
});

test('an older runtime without router Offer keeps configured entries and asks for an update', async () => {
  const models = {configured: {name: 'Configured', limit: {context: 8192, output: 4096}}};
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models}}};
  const logs = [];
  const older = Object.assign(new Error('unknown method'), {code: 'unknown_method'});
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {}, {
    tool: fakeTool,
    readInventory: async () => { throw older; },
    readCapability: async () => false,
  });
  await plugin.config(config);
  assert.equal(config.provider.openabstractions.models, models);
  const update = contextTable.olderRuntime;
  assert.deepEqual(logs.filter((entry) => entry.body.level === 'warn').map((entry) => entry.body.message), [update]);

  const refreshed = await plugin.provider.models({id: 'openabstractions', options: {scope: 'local'}, models});
  assert.deepEqual(Object.keys(refreshed), ['configured']);
  assert.equal(refreshed.configured.name, 'Configured');
  assert.deepEqual(refreshed.configured.limit, {context: 8192, output: 4096});
  assert.equal(logs.filter((entry) => entry.body.message === update).length, 2);
});

// OpenCode lists providers through this hook and fails every provider when
// it rejects, so a runtime that cannot answer leaves the configured entries in
// the listing; a later refresh rejects, so OpenCode reports the failure.
test('an unavailable runtime keeps the configured entries in the listing and fails a refresh', async () => {
  const models = {configured: {name: 'Configured', limit: {context: 8192, output: 4096}}};
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models}}};
  const logs = [];
  const down = Object.assign(new Error('native operation failed'), {code: 'runtime_unavailable'});
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {}, {
    tool: fakeTool, availability: new Availability(),
    readInventory: async () => { throw down; },
    readCapability: async () => false,
  });
  await plugin.config(config);
  const listing = await plugin.provider.models({id: 'openabstractions', options: {scope: 'local'}, models});
  assert.deepEqual(Object.keys(listing), ['configured']);
  assert.equal(logs.filter((entry) => entry.body.message === 'OpenAbstractions inventory refresh failed; keeping configured model entries').length, 2);
  await assert.rejects(() => plugin.provider.models({id: 'openabstractions', options: {scope: 'local'}, models}), /native operation failed/);
});

test('a router client without offer reads as an older runtime', async () => {
  const models = {configured: {name: 'Configured'}};
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models}}};
  const logs = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {}, {
    tool: fakeTool,
    // A router package published before Offer: its client has models and no offer.
    resolveRouter: async () => ({models: async () => ({models: []})}),
  });
  await plugin.config(config);
  assert.equal(config.provider.openabstractions.models, models);
  assert.deepEqual(logs.filter((entry) => entry.body.level === 'warn').map((entry) => entry.body.message), [contextTable.olderRuntime]);
});

test('source-watch tool resolves a local reader only after approval and binds its cancellation signal', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {}}, {tools: {watch_logs: true}}, {
    tool: fakeTool,
    readInventory: async () => ({offers: []}),
    resolveSourceReader: async (_options, signal) => {
      assert.ok(signal instanceof AbortSignal);
      return {
        sources: async () => ({outcome: 'catalog', sources: [{id: 'runtime/log', availability: 'available'}]}),
        startAtEnd: async (sourceId) => ({sourceId, outcome: 'page', records: [], atEnd: true, next: 'cursor-1'}),
        read: async (sourceId) => ({sourceId, outcome: 'page', records: [
          {sourceId, record: {schema: 1, identity: []}},
        ], atEnd: true, next: 'cursor-2'}),
      };
    },
  });
  const permissions = [];
  const output = await plugin.tool.openabstractions_watch_logs.execute({
    sourceId: 'runtime/log', durationMs: 1000, maxRecords: 1, maxBytes: 4096,
  }, {
    signal: new AbortController().signal,
    ask: async (request) => { permissions.push(request); },
  });
  assert.equal(JSON.parse(output).status, 'limit_reached');
  assert.equal(permissions.length, 1);
  assert.equal(permissions[0].permission, 'openabstractions_log_source_read');
  assert.deepEqual(permissions[0].always, []);
});

test('tool clients use the configured provider endpoint when multiple OA providers exist', async () => {
  const resolved = [];
  const plugin = await OpenAbstractionsPlugin({client: {}}, {providerId: 'oa-custom', tools: {generate_image: true, speak: true, selection_policy: true, watch_logs: true, open_credentials_panel: true, authorization_questions: true}}, {
    tool: fakeTool,
    readInventory: async () => ({offers: []}),
    resolveSelectionOperator: async (options) => {
      resolved.push(['policy', options.runtimeEndpoint]);
      return {read: async () => ({outcome: 'unknown', revision: ''})};
    },
    resolveSourceReader: async (options, signal) => {
      resolved.push(['source', options.runtimeEndpoint]);
      assert.ok(signal instanceof AbortSignal);
      return {
        sources: async () => ({outcome: 'catalog', sources: [{id: 'runtime/log', availability: 'unsupported'}]}),
        startAtEnd: async () => { throw new Error('unsupported source must not establish a cursor'); },
        read: async () => { throw new Error('unsupported source must not read'); },
      };
    },
  });
  await plugin.config({provider: {
    'oa-custom': {npm: '@openabstractions/opencode', options: {runtimeEndpoint: 'custom-endpoint'}, models: {}},
    openabstractions: {npm: '@openabstractions/opencode', options: {runtimeEndpoint: 'other-endpoint'}, models: {}},
  }});
  const policyReply = await plugin.tool.openabstractions_selection_policy.execute({action: 'read', name: 'local-first'});
  assert.equal(JSON.parse(policyReply).outcome, 'unknown');
  const watchReply = await plugin.tool.openabstractions_watch_logs.execute({
    sourceId: 'runtime/log', durationMs: 1000, maxRecords: 1, maxBytes: 4096,
  }, {ask: async () => {}});
  assert.equal(JSON.parse(watchReply).status, 'unsupported');
  assert.deepEqual(resolved, [['policy', 'custom-endpoint'], ['source', 'custom-endpoint']]);
});

test('provider.models refresh accepts public Provider.Info after config verifies package identity', async () => {
  const logs = [];
  const calls = [];
  let inventoryRevision = 0;
  let clock = 0;
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {providerId: 'oa-custom'}, {
    tool: fakeTool, now: () => clock,
    readInventory: async (options, placement) => {
      calls.push({kind: 'inventory', options, placement});
      if (placement === 'hosted') return {offers: []};
      inventoryRevision++;
      const model = `inventory-${inventoryRevision}`;
      return {offers: [offer(model, 8192, {contextSource: 'effective'})]};
    },
    readSelectionPolicy: async (options, name) => {
      // The app's own chat policy does not exist here (its own test below).
      if (name === 'oa-chat-default') return {outcome: 'unknown'};
      calls.push({kind: 'policy', options, name});
      return {outcome: 'found', policy: {name, candidates: [
        {model: 'hosted-away', placement: 'hosted', credential: 'deepseek'},
        {model: `inventory-${inventoryRevision}`, placement: 'local', credential: ''},
      ]}};
    },
  });
  const provider = {id: 'oa-custom', options: {
    scope: 'local', runtimeEndpoint: 'local-endpoint', credential: 'deepseek',
    oaSelectionPolicies: {'oa-preference-cloud-local': 'cloud-local'},
  }, models: {manual: {name: 'Manual'}}};
  const auth = {key: 'auth-secret-must-not-be-forwarded'};
  await plugin.config({provider: {'oa-custom': {
    npm: '@openabstractions/opencode@0.4.0', options: {
      ...provider.options, [inventoryMarkerOption]: {},
    }, models: provider.models,
  }}});
  // A listing more than 30 s after the config hook surveys again.
  clock += 31000;
  const first = await plugin.provider.models(provider, {auth});
  assert.equal(plugin.provider.id, 'oa-custom');
  const discovered = first['inventory-2'];
  assert.ok(discovered);
  assert.deepEqual(Object.keys(discovered).sort(), [
    'api', 'capabilities', 'cost', 'family', 'headers', 'id', 'limit', 'name',
    'options', 'providerID', 'release_date', 'status', 'variants',
  ]);
  assert.equal(discovered.id, 'inventory-2');
  assert.equal(discovered.providerID, 'oa-custom');
  assert.deepEqual(discovered, {
    id: 'inventory-2', providerID: 'oa-custom',
    api: {id: 'inventory-2', url: '', npm: '@openabstractions/opencode@0.4.0'},
    name: 'inventory-2 · 8k · this computer: local', family: '', status: 'active',
    capabilities: {
      temperature: false, reasoning: false, attachment: false, toolcall: true,
      input: {text: true, audio: false, image: false, video: false, pdf: false},
      output: {text: true, audio: false, image: false, video: false, pdf: false},
      interleaved: false,
    },
    cost: {input: 0, output: 0, cache: {read: 0, write: 0}},
    limit: {context: 8192, output: 4096},
    options: {}, headers: {}, release_date: '', variants: {},
  });
  assert.equal(first['oa-preference-cloud-local'].name, 'Preference: cloud-local');
  assert.deepEqual(first['oa-preference-cloud-local'].limit, {context: 8192, output: 4096});
  assert.equal(first['oa-preference-cloud-local'].capabilities.toolcall, true);
  assert.equal(first.manual.name, 'Manual');
  // Each refresh reads the provider's placement, the saved preference, and
  // the hosted offers its hosted candidate needs.
  assert.deepEqual(calls.map((call) => call.kind === 'inventory' ? `inventory:${call.placement}` : call.kind), [
    'inventory:local', 'policy', 'inventory:hosted', // verified startup config refresh
    'inventory:local', 'policy', 'inventory:hosted', // public Provider.Info refresh
  ]);
  assert.equal(calls[3].options.runtimeEndpoint, 'local-endpoint');
  assert.equal(calls[4].options.credential, 'deepseek');
  assert.equal(calls[4].name, 'cloud-local');
  assert.equal(JSON.stringify(first).includes('auth-secret-must-not-be-forwarded'), false);
  assert.equal(JSON.stringify(logs).includes('auth-secret-must-not-be-forwarded'), false);

  // The running OpenCode provider adds these identity fields after taking the
  // callback's model map. A second refresh still removes stale untouched IDs.
  const secondInput = {...provider, models: Object.fromEntries(Object.entries(first).map(([id, model]) => [id, {
    ...model, id, providerID: 'oa-custom',
  }]))};
  const second = await plugin.provider.models(secondInput, {auth});
  assert.equal(second['inventory-2'], undefined);
  assert.ok(second['inventory-3']);
  assert.ok(second['oa-preference-cloud-local']);
  assert.equal(second.retired, undefined);
  assert.equal(second.manual.name, 'Manual');
});

test('provider.models refuses an unverified or differently named public provider', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {}}, {providerId: 'oa-custom'}, {
    tool: fakeTool, readInventory: async () => ({offers: []}),
  });
  await assert.rejects(() => plugin.provider.models({id: 'oa-custom', options: {}, models: {}}), /unverified/);
  await plugin.config({provider: {'oa-custom': {npm: '@other/provider', options: {}, models: {}}}});
  await assert.rejects(() => plugin.provider.models({id: 'oa-custom', options: {}, models: {}}), /unverified/);
  await plugin.config({provider: {'oa-custom': {npm: '@openabstractions/opencode', options: {}, models: {}}}});
  await assert.rejects(() => plugin.provider.models({id: 'different-provider', options: {}, models: {}}), /unverified/);
});

test('config verifies a local OA package without treating the inventory marker as identity', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {}}, {providerId: 'oa-custom'}, {
    tool: fakeTool,
    readInventory: async () => ({offers: [offer('fresh-local')]}),
  });
  const configuredProvider = {
    npm: new URL('../index.js', import.meta.url).href,
    options: {scope: 'local'},
    models: {manual: {name: 'Manual model'}},
  };
  await plugin.config({provider: {'oa-custom': configuredProvider}});
  assert.ok(Object.hasOwn(configuredProvider.options, inventoryMarkerOption));
  assert.equal(configuredProvider.models.manual.name, 'Manual model');

  // This is the public Provider.Info shape passed to provider.models; it has
  // no npm field, so the hook must rely on the earlier verified config entry.
  const publicProviderInfo = {
    id: 'oa-custom', name: 'OA Custom', source: 'config', env: [],
    options: configuredProvider.options, models: configuredProvider.models,
  };
  const models = await plugin.provider.models(publicProviderInfo);
  assert.equal(models['fresh-local'].id, 'fresh-local');
  assert.equal(models['fresh-local'].providerID, 'oa-custom');
  assert.equal(models['fresh-local'].api.npm, configuredProvider.npm);
  assert.equal(typeof models['fresh-local'].capabilities.input.text, 'boolean');
});

test('refresh failure leaves provider config intact and is logged', async () => {
  const models = {configured: {name: 'Configured'}};
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {}, models}}};
  const logs = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {}, {
    tool: fakeTool,
    readInventory: async () => { throw new Error('inventory refused'); },
  });
  await plugin.config(config);
  assert.equal(config.provider.openabstractions.models, models);
  assert.ok(logs.some((entry) => entry.body.level === 'warn' && entry.body.extra.reason === 'details redacted'));
});

test('plugin does not attach OA inventory to a provider ID owned by another package', async () => {
  const models = {custom: {name: 'Custom provider model'}};
  const config = {provider: {openabstractions: {
    npm: '@other/provider',
    options: {[inventoryMarkerOption]: {}},
    models,
  }}};
  const logs = [];
  let calls = 0;
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {}, {
    tool: fakeTool,
    readInventory: async () => { calls++; return {offers: []}; },
  });
  await plugin.config(config);
  assert.equal(calls, 0);
  assert.equal(config.provider.openabstractions.models, models);
  assert.ok(logs.some((entry) => entry.body.message.includes('another package')));
});

test('plugin rejects a forged local adopter path suffix without package identity', async () => {
  const models = {custom: {name: 'Custom provider model'}};
  const config = {provider: {openabstractions: {
    npm: resolve('missing/adopters/opencode/index.js'),
    options: {[inventoryMarkerOption]: {}},
    models,
  }}};
  let calls = 0;
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async () => {}}}}, {}, {
    tool: fakeTool,
    readInventory: async () => { calls++; return {offers: []}; },
  });
  await plugin.config(config);
  assert.equal(calls, 0);
  assert.equal(config.provider.openabstractions.models, models);
});

test('the plugin lists its own chat selection policy with no configure step', async () => {
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models: {}}}};
  const asked = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async () => {}}}}, {}, {
    tool: fakeTool, readCapability: async () => false,
    readInventory: async () => ({offers: [offer('qwen', 32768, {contextSource: 'effective'})]}),
    readSelectionPolicy: async (_options, name) => {
      asked.push(name);
      return {outcome: 'found', policy: {name, candidates: [{model: 'qwen', placement: 'local', credential: ''}]}};
    },
  });
  await plugin.config(config);
  assert.deepEqual(asked, ['oa-chat-default']);
  assert.equal(config.provider.openabstractions.options.oaSelectionPolicies['oa-chat'], 'oa-chat-default');
  assert.equal(config.provider.openabstractions.models['oa-chat'].name, 'Preference: oa-chat-default');
});

test('the app\'s own policy is quiet until it exists, and a configured entry under its ID wins', async () => {
  const unknown = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models: {}}}};
  const logs = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async (entry) => logs.push(entry)}}}, {}, {
    tool: fakeTool, readCapability: async () => false,
    readInventory: async () => ({offers: []}),
    readSelectionPolicy: async () => ({outcome: 'unknown'}),
  });
  await plugin.config(unknown);
  assert.equal(Object.hasOwn(unknown.provider.openabstractions.models, 'oa-chat'), false);
  assert.deepEqual(logs.filter((entry) => entry.body.level === 'warn' && /preference/i.test(entry.body.message)), []);

  const configured = {provider: {openabstractions: {npm: '@openabstractions/opencode',
    options: {scope: 'local', oaSelectionPolicies: {'oa-chat': 'my-chat'}}, models: {}}}};
  const asked = [];
  const second = await OpenAbstractionsPlugin({client: {app: {log: async () => {}}}}, {}, {
    tool: fakeTool, readCapability: async () => false,
    readInventory: async () => ({offers: []}),
    readSelectionPolicy: async (_options, name) => { asked.push(name); return {outcome: 'unknown'}; },
  });
  await second.config(configured);
  assert.deepEqual(asked, ['my-chat']);
});

// OpenCode lists the providers right after the config hook, on the first
// request's path; that listing reuses the hook's survey instead of surveying
// again (demo-model run: up to 3.3 s on the first request).
test('the first listing right after startup reuses the startup survey', async () => {
  let reads = 0;
  let clock = 1000;
  const models = {configured: {name: 'Configured', limit: {context: 8192, output: 4096}}};
  const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {
    tool: fakeTool, now: () => clock, availability: new Availability(),
    readInventory: async () => { reads += 1; return {offers: [offer('fresh', 8192, {contextSource: 'effective'})]}; },
    readCapability: async () => false,
  });
  const config = {provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models}}};
  await plugin.config(config);
  assert.equal(reads, 1);
  const surveyed = config.provider.openabstractions.models;
  clock += 2000;
  const listing = await plugin.provider.models({id: 'openabstractions', options: {scope: 'local'}, models: surveyed});
  assert.equal(reads, 1, 'the listing surveys nothing');
  assert.ok(listing.fresh, 'the startup survey answers it');
  await plugin.provider.models({id: 'openabstractions', options: {scope: 'local'}, models: surveyed});
  assert.equal(reads, 2, 'a refresh always surveys');
});
