/**
 * OpenCode V2 (`@opencode/cli` 2.x, plugin API `@opencode/plugin`): the same
 * OA features through V2's setup(ctx). V2 loads `./server`'s default export
 * and calls its `setup`; V1 calls its `server` (server.js). This module runs
 * the V1 plugin (plugin.js) with a V2 shim and hands its results to V2's
 * domains:
 * - models: the plugin's startup survey fills OA's providers through
 *   `ctx.provider.transform`, and each later refresh replaces the captured
 *   models and calls `ctx.provider.reload()`;
 * - the language model: `ctx.aisdk.hook('language')` supplies the package's
 *   own AI SDK model (index.js) for OA's providers;
 * - tools: `ctx.tool.transform` adds each OA tool with a JSON Schema built
 *   from the same definitions; V2 passes no approval method to a tool, so a
 *   tool that asks first answers that approval is unavailable;
 * - requests: `ctx.session.hook('context')` carries the Local assistant's
 *   local-only guarantee and the questions notice.
 * V2 offers a server plugin no toast; its notices go to the log.
 * Written against the declarations of @opencode/plugin 2.0.24.
 */
import OpenAbstractionsPlugin from './plugin.js';
import {runtimeUnavailable} from './availability.js';
import {inventoryMarkerOption, nativeProviderPackage, selectionPolicyMarkerOption} from './inventory.js';

export const pluginID = 'openabstractions';

function node(json, optional = false) {
  return {
    json, isOptional: optional,
    optional() { return node(json, true); },
    describe(text) { return node({...json, description: text}, optional); },
    min(value) { return node({...json, [json.type === 'string' ? 'minLength' : json.type === 'array' ? 'minItems' : 'minimum']: value}, optional); },
    max(value) { return node({...json, [json.type === 'string' ? 'maxLength' : json.type === 'array' ? 'maxItems' : 'maximum']: value}, optional); },
    int() { return node({...json, type: 'integer'}, optional); },
    regex(value) { return node({...json, pattern: value.source}, optional); },
    strict() { return node({...json, additionalProperties: false}, optional); },
    passthrough() { return node({...json, additionalProperties: true}, optional); },
    catchall(value) { return node({...json, additionalProperties: value.json}, optional); },
  };
}

function objectSchema(shape = {}) {
  const properties = {};
  const required = [];
  for (const [name, value] of Object.entries(shape)) {
    properties[name] = value?.json ?? {};
    if (!value?.isOptional) required.push(name);
  }
  return {type: 'object', properties, ...(required.length ? {required} : {}), additionalProperties: false};
}

/** The V1 `tool()` helper's shape over plain JSON Schema: the plugin's tool
 * modules build their definitions with it on V2, where V1's helper is absent. */
export const jsonTool = Object.assign((definition) => definition, {
  schema: {
    string: () => node({type: 'string'}),
    number: () => node({type: 'number'}),
    boolean: () => node({type: 'boolean'}),
    unknown: () => node({}),
    enum: (values) => node({type: 'string', enum: [...values]}),
    literal: (value) => node({const:value}),
    array: (item) => node({type: 'array', items: item?.json ?? {}}),
    object: (shape) => node(objectSchema(shape)),
    null: () => node({type: 'null'}),
    union: (items) => node({anyOf: items.map(item => item.json)}),
    toJSONSchema: (value) => value.json,
  },
});

/** A tool definition's input as JSON Schema. */
export function inputSchema(definition) {
  return objectSchema(definition.args ?? {});
}

const flags = (map) => Object.entries(map ?? {}).filter(([, on]) => on === true).map(([kind]) => kind);

/** A V1 Provider.Model (plugin.js providerModelInventory) as V2's Model.Info. */
export function v2Model(providerID, id, model) {
  const capabilities = model.capabilities ?? {};
  const cost = model.cost ?? {};
  return {
    id, modelID: id, providerID, name: model.name ?? id,
    ...(model.family ? {family: model.family} : {}),
    capabilities: {tools: capabilities.toolcall ?? true, input: flags(capabilities.input), output: flags(capabilities.output)},
    variants: [],
    time: {released: model.release_date ?? ''},
    cost: [{input: cost.input ?? 0, output: cost.output ?? 0, cache: {read: cost.cache?.read ?? 0, write: cost.cache?.write ?? 0}}],
    status: model.status ?? 'active',
    enabled: true,
    limit: {context: model.limit?.context ?? 0, ...(model.limit?.input === undefined ? {} : {input: model.limit.input}), output: model.limit?.output ?? 0},
    ...(model.options && Object.keys(model.options).length ? {settings: model.options} : {}),
  };
}

function shimClient() {
  return {app: {log: async ({body} = {}) => {
    console.log(`[openabstractions] ${body?.level ?? 'info'}: ${body?.message ?? ''}`);
  }}};
}

function nativeModel(providerID, id, model) {
  return v2Model(providerID, id, {...model, capabilities: model.capabilities ?? {
    toolcall: model.tool_call ?? true, input: {text: true}, output: {text: true}}});
}

// The native editor exposes immutable V2 source records. Convert them and the
// config owner's managed baselines together before its ordinary refresh.
function configuredModel(model) {
  const cost = model.cost?.[0] ?? {};
  return {name: model.name, family: model.family ?? '', status: model.status ?? 'active',
    limit: {...model.limit}, options: model.settings ?? {},
    capabilities: {toolcall: model.capabilities?.tools ?? true,
      input: Object.fromEntries((model.capabilities?.input ?? []).map((kind) => [kind, true])),
      output: Object.fromEntries((model.capabilities?.output ?? []).map((kind) => [kind, true]))},
    cost: {input: cost.input ?? 0, output: cost.output ?? 0,
      cache: {read: cost.cache?.read ?? 0, write: cost.cache?.write ?? 0}}};
}

/** V2's setup. `dependencies` replaces the V1 plugin and the language model in tests. */
export async function setupV2(ctx, dependencies = {}) {
  const options = {...(ctx.options ?? {})};
  const providerID = typeof options.providerId === 'string' && options.providerId ? options.providerId : pluginID;
  const providerOptions = {...(options.provider ?? {})};
  const client = dependencies.client ?? shimClient();
  const inventoryStates = new Map();
  // The plugin's inventory watch calls this when OA's models change; it runs
  // only after setup has published the sources it refreshes.
  let ready = false;
  const plugin = await (dependencies.plugin ?? OpenAbstractionsPlugin)(
    {client, directory: ctx.location?.directory},
    {...options, providerId: providerID},
    {tool: jsonTool, ...(dependencies.pluginDependencies ?? {}),
      onInventoryRefresh: (id, error) => inventoryStates.set(id, error),
      onInventoryChange: async () => { if (ready) await refreshLogged(); }});
  const sources = new Map();
  let registered;
  // Capture existing configured headings through the native provider editor.
  // Subsequent replays publish only the captured, verified OA source values.
  const snapshot = await ctx.provider.transform((editor) => {
    registered ??= editor.list?.() ?? [];
  });
  const config = {provider: {}};
  if (!registered.some((record) => record.provider?.id === providerID)) {
    config.provider[providerID] = {npm: '@openabstractions/opencode', options: providerOptions, models: {}};
  }
  const information = new Map();
  const connections = new Map();
  const originals = new Map();
  for (const record of registered) {
    const info = record.provider;
    if (!info?.id || !await nativeProviderPackage({npm: info.package})) continue;
    information.set(info.id, info);
    connections.set(info.id, record.sourceConnection);
    const settings = {...(info.settings ?? {})};
    for (const marker of [inventoryMarkerOption, selectionPolicyMarkerOption]) {
      if (settings[marker] && typeof settings[marker] === 'object') {
        settings[marker] = Object.fromEntries(Object.entries(settings[marker])
          .map(([id, model]) => [id, configuredModel(nativeModel(info.id, id, model))]));
      }
    }
    const models = new Map([...record.models].map(([id, model]) => [id, {model, configured: configuredModel(model)}]));
    originals.set(info.id, models);
    config.provider[info.id] = {npm: info.package, options: settings,
      models: Object.fromEntries([...models].map(([id, value]) => [id, value.configured]))};
  }
  await plugin.config(config);
  const name = typeof options.providerName === 'string' && options.providerName ? options.providerName : 'OpenAbstractions';
  for (const [id, entry] of Object.entries(config.provider)) {
    const info = information.get(id) ?? {id, name, activation: 'enabled', package: entry.npm};
    const preserved = new Map([...(originals.get(id) ?? [])]
      .filter(([modelID, value]) => entry.models?.[modelID] === value.configured)
      .map(([modelID, value]) => [modelID, value.model]));
    const listed = await plugin.provider.models({id, ...entry});
    const error = inventoryStates.has(id) ? inventoryStates.get(id) : new Error('Provider inventory was not surveyed');
    const unavailable = Boolean(error && runtimeUnavailable(error));
    sources.set(id, {entry: {...entry, models: listed}, info: error ? {...info,
      name: `${info.name} · ${unavailable ? 'unavailable' : 'refresh failed'} (last known models)`} : info,
      unavailable, failed: Boolean(error && !unavailable), originalInfo: info, preserved,
      sourceConnection: connections.get(id),
      models: Object.entries(listed).map(([modelID, model]) => preserved.get(modelID) ?? nativeModel(id, modelID, model))});
  }
  await snapshot?.dispose?.();
  await ctx.provider.transform((editor) => {
    for (const [id, source] of sources) {
      if (editor.get(id)) editor.remove(id);
      editor.add({info: source.info,
        ...(source.sourceConnection === undefined ? {} : {sourceConnection: source.sourceConnection}),
        models: source.unavailable || source.failed
        ? source.models.map((model) => ({...model, enabled: false})) : source.models});
    }
  });
  // The provider module loads OA's clients, so it is imported only on V2.
  const create = dependencies.languageModel ? null : (await import('./index.js')).createOpenAbstractions;
  const languageModel = dependencies.languageModel ?? ((modelID, settings) => create(settings).languageModel(modelID));
  for (const [id, source] of sources) {
    await ctx.aisdk?.hook?.('language', (event) => {
      event.language = languageModel(event.model?.modelID ?? event.model?.id, source.entry.options);
    }, {providerID: id});
  }

  const tools = Object.entries(plugin.tool ?? {});
  if (tools.length && ctx.tool?.transform) {
    await ctx.tool.transform((editor) => {
      for (const [toolName, definition] of tools) {
        editor.add({
          name: toolName,
          description: definition.description,
          input: inputSchema(definition),
          execute: async (input, context) => {
            const output = await definition.execute(input, {sessionID: context.sessionID, messageID: context.messageID,
              agent: context.agent, callID: context.id, abort: context.signal, signal: context.signal});
            return {content: typeof output === 'string' ? output : (output?.output ?? JSON.stringify(output)),
              ...(output && typeof output === 'object' && output.metadata ? {metadata: output.metadata} : {})};
          },
        });
      }
    });
  }

  // Local validation covers every selected provider. Provider-scoping this
  // hook would let an external Local selection bypass the native-route guard.
  await ctx.session?.hook?.('context', async (event) => {
    const params = {options: {}};
    await plugin['chat.params']?.({sessionID: event.sessionID, agent: event.agent, model: event.model,
      provider: {info: sources.get(event.model?.providerID)?.info}}, params);
    Object.assign(event.options, params.options);
    const system = [];
    await plugin['experimental.chat.system.transform']?.({model: {providerID: event.model?.providerID}}, {system});
    for (const text of system) event.system.push({type: 'text', text});
  });
  // SessionTitle is a separate documented request hook, with the same host ID.
  // Its one-off request must not inherit the session conversation marker.
  await ctx.session?.hook?.('title', async (event) => {
    delete event.options.oaSessionID;
  });

  // When OA's models change, the plugin's inventory watch reads them again and
  // V2 reloads its providers (inventory-watch.js).
  async function refresh() {
    const failures = [];
    for (const [id, source] of sources) {
      try {
        const provider = {id, ...source.entry};
        const listed = await plugin.provider.models(provider);
        source.entry = {...provider, models: listed};
        source.models = Object.entries(listed).map(([modelID, model]) => source.preserved.get(modelID) ?? nativeModel(id, modelID, model));
        source.info = source.originalInfo;
        source.unavailable = false;
        source.failed = false;
      } catch (error) {
        if (!runtimeUnavailable(error)) {
          source.failed = true;
          source.info = {...source.originalInfo, name: `${source.originalInfo.name} · refresh failed (last known models)`};
          failures.push(error);
          continue;
        }
        source.unavailable = true;
        source.info = {...source.originalInfo, name: `${source.originalInfo.name} · unavailable (last known models)`};
      }
    }
    await ctx.provider.reload?.();
    if (failures.length) throw failures[0];
  }
  async function refreshLogged() {
    await refresh().catch(async (error) => {
      try {
        await client.app?.log?.({body: {service: 'openabstractions', level: 'error',
          message: 'OpenAbstractions model refresh failed; last known models were retained without enabling them',
          extra: {name: error?.name, status: error?.status}}});
      } catch { /* A diagnostic sink must not stop later provider retries. */ }
    });
  }
  ready = true;
  return () => plugin.dispose?.();
}
