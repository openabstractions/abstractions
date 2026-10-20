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

export const pluginID = 'openabstractions';
// The least time between two model refreshes after replies.
const refreshEveryMs = 60000;

function node(json, optional = false) {
  return {
    json, isOptional: optional,
    optional() { return node(json, true); },
    describe(text) { return node({...json, description: text}, optional); },
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
    enum: (values) => node({type: 'string', enum: [...values]}),
    array: (item) => node({type: 'array', items: item?.json ?? {}}),
    object: (shape) => node(objectSchema(shape)),
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

/** V2's setup. `dependencies` replaces the V1 plugin and the language model in tests. */
export async function setupV2(ctx, dependencies = {}) {
  const options = {...(ctx.options ?? {})};
  const providerID = typeof options.providerId === 'string' && options.providerId ? options.providerId : pluginID;
  const providerOptions = {...(options.provider ?? {})};
  const plugin = await (dependencies.plugin ?? OpenAbstractionsPlugin)(
    {client: dependencies.client ?? shimClient(), directory: ctx.location?.directory},
    {...options, providerId: providerID},
    {tool: jsonTool, ...(dependencies.pluginDependencies ?? {})});
  const config = {provider: {[providerID]: {npm: '@openabstractions/opencode', options: providerOptions, models: {}}}};
  await plugin.config(config);
  const entry = config.provider[providerID];
  let models = Object.entries(entry.models ?? {}).map(([id, model]) => v2Model(providerID, id, {...model, capabilities: {
    toolcall: model.tool_call ?? true, input: {text: true}, output: {text: true}}}));
  let settings = entry.options ?? providerOptions;
  const name = typeof options.providerName === 'string' && options.providerName ? options.providerName : 'OpenAbstractions';

  await ctx.provider.transform((editor) => {
    if (editor.get(providerID)) editor.remove(providerID);
    editor.add({info: {id: providerID, name, activation: 'enabled', package: '@openabstractions/opencode'}, models});
  });
  // The provider module loads OA's clients, so it is imported only on V2.
  const create = dependencies.languageModel ? null : (await import('./index.js')).createOpenAbstractions;
  const languageModel = dependencies.languageModel ?? ((modelID) => create(settings).languageModel(modelID));
  await ctx.aisdk?.hook?.('language', (event) => {
    event.language = languageModel(event.model?.modelID ?? event.model?.id);
  }, {providerID});

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

  await ctx.session?.hook?.('context', async (event) => {
    const params = {options: {}};
    await plugin['chat.params']?.({agent: event.agent}, params);
    Object.assign(event.options, params.options);
    const system = [];
    await plugin['experimental.chat.system.transform']?.({model: {providerID: event.model?.providerID}}, {system});
    for (const text of system) event.system.push({type: 'text', text});
  }, {providerID});

  // After a reply, at most once a minute, OA's models are read again.
  let refreshed = (dependencies.now ?? Date.now)();
  async function refresh() {
    const listed = await plugin.provider.models({id: providerID, models: Object.fromEntries(
      models.map((model) => [model.modelID, {name: model.name, limit: model.limit}]))});
    models = Object.entries(listed).map(([id, model]) => v2Model(providerID, id, model));
    settings = entry.options ?? settings;
    await ctx.provider.reload?.();
  }
  const abort = new AbortController();
  void (async () => {
    try {
      for await (const event of ctx.event?.subscribe?.({signal: abort.signal}) ?? []) {
        if (event?.type !== 'session.idle') continue;
        const now = (dependencies.now ?? Date.now)();
        if (now - refreshed < refreshEveryMs) continue;
        refreshed = now;
        await refresh().catch(() => {});
      }
    } catch {
      // The subscription ends with the plugin.
    }
  })();
  return () => abort.abort();
}
