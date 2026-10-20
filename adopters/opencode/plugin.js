import {readFile} from 'node:fs/promises';
import {dirname, isAbsolute, join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {candidatePlacement, chatModelAtStart, defaultSelectionModel, inventoryMarkerOption, isDefaultSelection, withDefaultSelectionPolicies, localServers,
  olderRuntime, olderRuntimeMessage, readOffers, refreshProviderInventory, refreshSelectionPolicyModels, selectionPolicyMarkerOption} from './inventory.js';
import {createOpenAbstractionsPanelTool} from './panel-launch.js';
import {createSelectionPolicyTool} from './selection-policy.js';
import {createSourceWatchTool} from './source-watch.js';
import {createImageTool} from './image-tool.js';
import {createAuthorizationQuestionsTool} from './authorization-questions.js';
import {createQuestionNotice} from './assistant-answers.js';
import {resolveContentReader} from './content-delivery.js';
import {createSpeakTool, resolveSpeechClient} from './speak.js';
import {compactDefinition, condensedFrom, condensedText} from './compact-tools.js';
import {answered, availability, guardTool, runtimeUnavailable, runtimeUntrusted, untrustedMessage} from './availability.js';
import {capabilityTools, createToolsTool, parametersOf, shortToolText, toolChoices, toolNames} from './tool-config.js';
import {preloadKeptPrefix} from './preload.js';
import {createProjectAccess, projectSessionEnvironment} from './project-access.js';
import {once, outsideSandboxMessage, searchToolMissingMessage} from './opencode-build.js';

const providerPackage = '@openabstractions/opencode';
const packageSpecifier = /^@openabstractions\/opencode(?:@[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)?$/;
const routerContract = 'abstraction.router/router@1';
const selectionContract = 'abstraction.inference/selection-operator@2';
const sourceReaderContract = 'abstraction.logging/source-reader@1';
const authorizationContract = 'abstraction.rights/authorization@1';
const imageContract = 'abstraction.inference/image@1';
const imageV2Contract = 'abstraction.inference/image@2';
const questionResponderContract = 'abstraction.asks/responder@1';
const defaultProviderID = 'openabstractions';
// How long the config hook's survey answers OpenCode's first provider listing.
const startupSurveyReuseMs = 30000;
// How long OpenCode's start waits for OA's first answer before it goes on
// without OA (configure's startup probe).
const defaultStartupProbeMs = 750;
const providerIDPattern = /^[a-z][a-z0-9_-]{0,63}$/;

/** The router placement a provider's inventory reads: its oaPlacement
 * ("hosted" for configure's hosted heading), else any for scope remote and
 * local otherwise. */
function providerPlacement(options) {
  if (['local', 'hosted', 'any'].includes(options?.oaPlacement)) return options.oaPlacement;
  return (options?.scope ?? 'local') === 'remote' ? 'any' : 'local';
}

/** Whether the runtime serves profile on the placement: one offer is enough. */
async function readCapability(options, profile, placement, resolve = resolveRouter) {
  const client = await resolve(options);
  if (typeof client?.offer !== 'function') return false;
  const snapshot = await client.offer({profile, placement, reserve: 0n, fresh: false});
  if (snapshot?.outcome !== 'offered') {
    const outcome = typeof snapshot?.outcome === 'string' ? snapshot.outcome : 'invalid_result';
    throw Object.assign(new Error(`OpenAbstractions router offer: ${outcome}`), {code: outcome, outcome});
  }
  return (snapshot?.offers ?? []).length > 0;
}

/** The router's chat offers for placement (local, hosted or any), surveyed
 * fresh, keeping the plugin's answer reserve (router CONTRACT.md ROUTE-D8). */
async function readInventory(options, placement, resolve = resolveRouter) {
  return readOffers(await resolve(options), placement);
}

async function resolveRouter(options) {
  const [{Machine}, {RouterClient}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/router'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(),
    timeout: 5000,
    server: options.server ?? null,
  });
  const binding = await machine.resolveService(routerContract, {
    scope: 'local',
  });
  return new RouterClient(binding);
}

async function readSelectionPolicy(options, name, connector) {
  return (await resolveSelectionOperator(options, connector)).read('', name);
}

// connector replaces the Bun connector OpenCode's runtime uses; serve's
// TestOpenCodePluginReadsItsOwnChatPolicyUnderInstallationGrants passes the
// Node addon's, so the plugin's own read meets a real runtime.
async function resolveSelectionOperator(options, connector) {
  const [{Machine}, {SelectionOperatorV2Client}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/inference'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: connector ?? new BunNativeConnector(),
    timeout: 5000,
    server: options.server ?? null,
  });
  const binding = await machine.resolveService(selectionContract, {scope: 'local'});
  return new SelectionOperatorV2Client(binding);
}

async function resolveSourceReader(options, signal) {
  const [{Machine}, {SourceReaderClient}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/logging'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(),
    timeout: 5000,
    cancellation: signal,
    server: options.server ?? null,
  });
  const binding = await machine.resolveService(sourceReaderContract, {scope: 'local'});
  return new SourceReaderClient(binding);
}

async function resolveAuthorization(options, signal) {
  const [{Machine}, {AuthorizationClient}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/rights'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(), timeout: 5000, cancellation: signal,
    server: options.server ?? null,
  });
  return new AuthorizationClient(await machine.resolveService(authorizationContract, {scope: 'local'}));
}

async function resolveImageClient(options, signal, contract = imageContract) {
  if (contract !== imageContract && contract !== imageV2Contract) throw new TypeError('Unsupported OA image contract');
  const [{Machine}, {ImageClient, ImageV2Client}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/inference/protocol'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(), timeout: 30000, cancellation: signal,
    server: options.server ?? null,
  });
  const binding = await machine.resolveService(contract, {scope: 'local'});
  return contract === imageContract ? new ImageClient(binding) : new ImageV2Client(binding);
}

async function resolveQuestionResponder(options, signal) {
  const [{Machine}, {QuestionResponderClient}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'), import('@openabstractions/asks'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: new BunNativeConnector(), timeout: 5000, cancellation: signal,
    server: options.server ?? null,
  });
  return new QuestionResponderClient(await machine.resolveService(questionResponderContract, {scope: 'local'}));
}

/** Whether the runtime serves responder@1 to this OpenCode: the questions
 * tool then registers without the tools option. Only OpenCode's Bun process
 * reaches the native runtime, so elsewhere nothing is probed. */
async function readAnswerable(options) {
  if (typeof Bun === 'undefined') return false;
  const responder = await resolveQuestionResponder(options, AbortSignal.timeout(2000));
  const page = await responder.listAnswerable('', 1n);
  return page?.outcome === 'page';
}

async function compatiblePackage(provider) {
  if (typeof provider?.npm === 'string' && packageSpecifier.test(provider.npm)) return true;
  if (typeof provider?.npm !== 'string') return false;
  let entry;
  try {
    if (provider.npm.startsWith('file://')) entry = fileURLToPath(provider.npm);
    else if (isAbsolute(provider.npm)) entry = provider.npm;
    else return false;
    const pkg = JSON.parse(await readFile(join(dirname(entry), 'package.json'), 'utf8'));
    return pkg.name === providerPackage;
  } catch {
    return false;
  }
}

async function providerEntries(config, configuredProviderID) {
  const compatible = [];
  const conflicts = [];
  for (const [id, provider] of Object.entries(config.provider ?? {})) {
    const options = provider?.options;
    const marked = Object.hasOwn(options ?? {}, inventoryMarkerOption);
    if (await compatiblePackage(provider)) {
      compatible.push([id, provider]);
      continue;
    }
    // A marker tracks which model entries OA owns. It cannot establish package
    // identity. Keep conflicts visible only for marked entries or the explicitly
    // configured provider ID; unrelated providers are ignored.
    if (marked || id === configuredProviderID) conflicts.push([id, provider]);
  }
  return {compatible, conflicts};
}

async function log(client, level, message, extra = {}) {
  try {
    await client?.app?.log?.({body: {service: 'openabstractions', level, message, extra}});
  } catch {
    // A diagnostic sink must not prevent OpenCode from starting.
  }
}

function failureCode(error) {
  for (const field of ['status', 'code']) {
    const value = error?.[field];
    if (typeof value === 'number' && Number.isInteger(value)) return `${field}:${value}`;
    if (typeof value === 'string' && /^[a-z0-9_.:-]{1,64}$/i.test(value)) return `${field}:${value}`;
  }
  return 'details redacted';
}

// OpenCode's provider.models hook is typed as Record<string, Provider.Model],
// while the shared inventory builder intentionally emits config-shaped
// metadata. Materialize the pinned Provider.Model contract at this boundary.
function providerModelInventory(models, providerID, packageSpecifier) {
  const result = Object.create(null);
  for (const [id, model] of Object.entries(models ?? {})) {
    const capabilities = model.capabilities ?? {};
    const input = capabilities.input ?? {};
    const output = capabilities.output ?? {};
    const cost = model.cost ?? {};
    const cache = cost.cache ?? {};
    const limit = model.limit ?? {};
    const api = model.api ?? {};
    const normalized = {
      id: model.id ?? id,
      providerID: model.providerID ?? providerID,
      api: {
        id: api.id ?? model.id ?? id,
        url: api.url ?? '',
        npm: api.npm ?? packageSpecifier,
      },
      name: model.name ?? id,
      family: model.family ?? '',
      status: model.status ?? 'active',
      capabilities: {
        temperature: capabilities.temperature ?? false,
        reasoning: capabilities.reasoning ?? false,
        attachment: capabilities.attachment ?? false,
        toolcall: capabilities.toolcall ?? model.tool_call ?? true,
        input: {
          text: input.text ?? true,
          audio: input.audio ?? false,
          image: input.image ?? false,
          video: input.video ?? false,
          pdf: input.pdf ?? false,
        },
        output: {
          text: output.text ?? true,
          audio: output.audio ?? false,
          image: output.image ?? false,
          video: output.video ?? false,
          pdf: output.pdf ?? false,
        },
        interleaved: capabilities.interleaved ?? false,
      },
      cost: {
        input: cost.input ?? 0,
        output: cost.output ?? 0,
        cache: {read: cache.read ?? cost.cache_read ?? 0, write: cache.write ?? cost.cache_write ?? 0},
      },
      limit: {
        context: limit.context ?? 0,
        ...(limit.input === undefined ? {} : {input: limit.input}),
        output: limit.output ?? 0,
      },
      options: model.options ?? {},
      headers: model.headers ?? {},
      release_date: model.release_date ?? '',
      variants: model.variants ?? {},
    };
    Object.defineProperty(result, id, {value: normalized, enumerable: true, configurable: true, writable: true});
  }
  return result;
}

/** Startup inventory refresh. OpenCode invokes config before Provider snapshots it. */
async function OpenAbstractionsPlugin({client, directory}, _options = {}, dependencies = {}) {
  const configuredProviderID = typeof _options.providerId === 'string' && providerIDPattern.test(_options.providerId)
    ? _options.providerId : defaultProviderID;
  const runtimeBaselines = new Map();
  const runtimeOptions = new Map();
  const projectSession = projectSessionEnvironment(dependencies.env ?? process.env);
  const optionsFor = (id) => runtimeOptions.get(id) ?? {runtimeEndpoint: undefined,
    ...(projectSession ? {server: projectSession.runtimeServer} : {})};
  const rememberOptions = (id, options) => runtimeOptions.set(id, {runtimeEndpoint: options.runtimeEndpoint,
    ...(options.server === undefined ? {} : {server: options.server})});
  const selectedOptions = (options) => projectSession ? {...options, server: projectSession.runtimeServer} : options;
  // Each provider's placement: the questions notice reaches only a model on
  // this machine, since question text names local programs.
  const providerPlacements = new Map();
  // OpenCode's provider.models hook receives public Provider.Info, which
  // intentionally omits the configured npm package. Bind refresh to the
  // provider ID whose package was verified in the preceding config hook.
  let verifiedConfiguredPackage = '';
  // Whether OpenCode has listed this provider's models once (see provider.models).
  let listed = false;
  // When the config hook last surveyed each provider: OpenCode lists the
  // providers right after it, on the first request's path, and a survey that
  // fresh answers that listing without another (startupSurveyReuseMs).
  const surveyed = new Map();
  const now = dependencies.now ?? (() => Date.now());
  const startupProbeMs = dependencies.startupProbeMs ?? defaultStartupProbeMs;
  // Whether OA's runtime answers (availability.js): one toast when it goes
  // away and one when it returns, through OpenCode's TUI.
  const state = dependencies.availability ?? availability;
  state.onChange(async ({variant, message, reason}) => {
    await log(client, variant === 'warning' ? 'warn' : 'info', message, reason ? {reason} : {});
    await client?.tui?.showToast?.({body: {title: 'OpenAbstractions', message, variant}});
  });
  // A lost runtime marks OA away. A runtime whose identity did not verify is
  // said once as its own error; anything else is the caller's.
  let untrustedTold = false;
  const noteFailure = (error) => {
    if (runtimeUnavailable(error)) state.markDown(error);
    if (runtimeUntrusted(error) && !untrustedTold) {
      untrustedTold = true;
      void log(client, 'error', untrustedMessage, {reason: 'ipc status 8'});
      void Promise.resolve(client?.tui?.showToast?.({body: {title: 'OpenAbstractions', message: untrustedMessage, variant: 'error'}})).catch(() => {});
    }
  };
  // No released OpenCode TUI shows a sharing approval's bounds (the
  // consent-display draft in patches/ would); tests supply their own answer.
  const disclosure = once(dependencies.showsDisclosure ?? (() => false));
  const openCodeTool = dependencies.tool ?? (await import('@opencode-ai/plugin/tool')).tool;
  const choices = toolChoices(_options.tools);
  // OpenCode reads this object after every config hook has run, so the
  // config hook adds the capability tools it finds served.
  const tools = {};
  // Every tool but the Panel launcher calls the runtime, so a transport
  // failure answers the model with one sentence instead of failing the call.
  const register = (name) => {
    const definition = toolFactories[name]();
    tools[`openabstractions_${name}`] = name === 'open_credentials_panel' ? definition : guardTool(definition, state);
  };
  const toolFactories = {
    open_credentials_panel: () => createOpenAbstractionsPanelTool({tool: openCodeTool}),
    selection_policy: () => createSelectionPolicyTool({
      tool: openCodeTool,
      resolveOperator: async () => answered((dependencies.resolveSelectionOperator ?? resolveSelectionOperator)(
        optionsFor(configuredProviderID),
      ), state),
    }),
    watch_logs: () => createSourceWatchTool({
      tool: openCodeTool,
      resolveReader: async ({signal}) => answered((dependencies.resolveSourceReader ?? resolveSourceReader)(
        optionsFor(configuredProviderID), signal,
      ), state),
      resolveAuthorization: async ({signal}) => answered((dependencies.resolveAuthorization ?? resolveAuthorization)(
        optionsFor(configuredProviderID), signal,
      ), state),
      // The official TUI shows no sharing approval's bounds, so a sharing
      // request answers as unavailable.
      showsDisclosure: disclosure,
    }),
    generate_image: () => createImageTool({
      tool: openCodeTool,
      resolveImage: async ({signal, contract}) => answered((dependencies.resolveImageClient ?? resolveImageClient)(
        optionsFor(configuredProviderID), signal, contract,
      ), state),
      resolveReader: async ({signal}) => answered((dependencies.resolveContentReader ?? resolveContentReader)(
        optionsFor(configuredProviderID), signal,
      ), state),
    }),
    speak: () => createSpeakTool({
      tool: openCodeTool,
      resolveSpeech: async ({signal}) => answered((dependencies.resolveSpeechClient ?? resolveSpeechClient)(
        optionsFor(configuredProviderID), signal,
      ), state),
      resolveReader: async ({signal}) => answered((dependencies.resolveContentReader ?? resolveContentReader)(
        optionsFor(configuredProviderID), signal,
      ), state),
    }),
    authorization_questions: () => createAuthorizationQuestionsTool({
      tool: openCodeTool,
      resolveResponder: async ({signal}) => answered((dependencies.resolveQuestionResponder ?? resolveQuestionResponder)(
        optionsFor(configuredProviderID), signal,
      ), state),
      // Every answer is shown to the person: a toast in the TUI and a log
      // line, beside the tool's own result in the session.
      announce: async ({message, variant}) => {
        await log(client, 'info', message);
        await client?.tui?.showToast?.({body: {title: 'OpenAbstractions', message, variant}});
      },
    }),
  };
  // The questions waiting for this session, read before a model request.
  const questionNotice = createQuestionNotice({now,
    resolveResponder: ({signal}) => (dependencies.resolveQuestionResponder ?? resolveQuestionResponder)(
      optionsFor(configuredProviderID), signal)});
  for (const name of toolNames) if (choices.enabled.has(name)) register(name);
  const projectAccess = createProjectAccess({tool: openCodeTool, session: projectSession, client,
    ...(dependencies.resolveProjectClient ? {resolveClient: dependencies.resolveProjectClient} : {}),
    announce: async (message) => {
      await log(client, 'warn', message);
      await client?.tui?.showToast?.({body: {title: 'OpenAbstractions project access', message, variant: 'warning'}});
    }});
  if (projectAccess) tools.openabstractions_project_access = projectAccess.definition;
  // Without the native session every OA feature but folder requests runs,
  // and the first event of this start shows one toast naming OA's start.
  let outsideTold = false;
  const outsideHooks = projectAccess ? {} : {
    event: async () => {
      if (outsideTold) return;
      outsideTold = true;
      await log(client, 'info', outsideSandboxMessage);
      try {
        await client?.tui?.showToast?.({body: {title: 'OpenAbstractions', message: outsideSandboxMessage, variant: 'info'}});
      } catch {
        // A toast must not stop OpenCode's event delivery.
      }
    },
  };
  // In OA's sandbox OpenCode's grep and glob have no search tool, so the first
  // event of this start shows one notice; outside it, this notice never shows.
  let searchTold = false;
  const sessionHooks = projectAccess ? {...projectAccess.hooks, event: async (input) => {
    if (!searchTold) {
      searchTold = true;
      await log(client, 'warn', searchToolMissingMessage);
      try {
        await client?.tui?.showToast?.({body: {title: 'OpenAbstractions', message: searchToolMissingMessage, variant: 'warning'}});
      } catch {
        // A toast must not stop OpenCode's event delivery.
      }
    }
    await projectAccess.hooks.event(input);
  }} : outsideHooks;
  // Compact mode (configure's local profile): short core tool text through the
  // tool.definition hook, one line per OA tool, and the discovery tool.
  const compact = _options.compactTools === true;
  let compactOff = false;
  // The agent the local profile of configure.js writes.
  const localAgent = typeof _options.localAgent === 'string' && _options.localAgent ? _options.localAgent : 'local';
  if (compact) {
    tools.openabstractions_tools = createToolsTool({tool: openCodeTool, registered: () => tools,
      full: (name) => {
        const definition = tools[name];
        return definition ? {description: definition.description, parameters: parametersOf(openCodeTool, definition)} : null;
      }});
  }
  if (choices.unknown.length) {
    await log(client, 'warn', 'OpenAbstractions ignored tools options it does not know', {names: choices.unknown});
  }
  async function refresh(provider, idHint = configuredProviderID) {
    const id = provider.id ?? idHint;
    const prior = runtimeBaselines.get(id);
    const options = {...selectedOptions(provider.options ?? {})};
    rememberOptions(id, options);
    providerPlacements.set(id, providerPlacement(options));
    if (prior) {
      options[inventoryMarkerOption] = {...(options[inventoryMarkerOption] ?? {}), ...prior.inventory};
      options[selectionPolicyMarkerOption] = {...(options[selectionPolicyMarkerOption] ?? {}), ...prior.selections};
    }
    const working = {...provider, options, models: {...(provider.models ?? {})}};
    const read = dependencies.readInventory ??
      ((readOptions, readPlacement) => readInventory(readOptions, readPlacement, dependencies.resolveRouter ?? resolveRouter));
    // Scope local offers this machine's servers; scope remote offers hosted
    // servers and remote runtimes too.
    const placement = providerPlacement(options);
    const offersByPlacement = {[placement]: (await read(options, placement)).offers ?? []};
    // Where each model runs comes from the router's placements: everything at
    // local is on this computer, nothing at hosted is, and at any the local
    // placement's servers are.
    let isLocal = () => placement === 'local';
    if (placement === 'any') {
      offersByPlacement.local = (await read(options, 'local')).offers ?? [];
      isLocal = localServers(offersByPlacement.local);
    }
    // oaInventory false: the saved-preferences heading lists no router model.
    const inventoryResult = refreshProviderInventory(working, options.oaInventory === false ? [] : offersByPlacement[placement], isLocal);
    // The main heading lists the app's own profile policies beside any the
    // configuration names; OpenCode reads them by name (VISION 2026-10-06).
    if (id === configuredProviderID) options.oaSelectionPolicies = withDefaultSelectionPolicies(options);
    const defaults = new Set(Object.entries(options.oaSelectionPolicies ?? {})
      .filter(([model, name]) => isDefaultSelection(model, name)).map(([, name]) => name));
    const names = [...new Set(Object.values(options.oaSelectionPolicies && typeof options.oaSelectionPolicies === 'object'
      ? options.oaSelectionPolicies : {}).filter((name) => typeof name === 'string' && name.length > 0))];
    const reads = Object.create(null);
    for (const name of names) {
      try {
        reads[name] = await (dependencies.readSelectionPolicy ?? ((o, n) => readSelectionPolicy(o, n, dependencies.connector)))(options, name);
      } catch (error) {
        reads[name] = {outcome: 'unavailable'};
        // The app's own policy is quiet until the person chooses a model.
        if (defaults.has(name)) continue;
        await log(client, 'warn', 'OpenAbstractions preference is unavailable to this OpenCode identity', {
          provider: id, policy: name, reason: failureCode(error),
        });
      }
    }
    // A saved preference's candidates read the offers of their own placement.
    for (const result of Object.values(reads)) {
      if (result?.outcome !== 'found' || !Array.isArray(result.policy?.candidates)) continue;
      for (const candidate of result.policy.candidates) {
        const wanted = candidatePlacement(candidate);
        if (!offersByPlacement[wanted]) offersByPlacement[wanted] = (await read(options, wanted)).offers ?? [];
      }
    }
    const policyResult = refreshSelectionPolicyModels(working, offersByPlacement, reads);
    const markerInventory = working.options[inventoryMarkerOption];
    const markerSelections = working.options[selectionPolicyMarkerOption];
    runtimeBaselines.set(id, {
      inventory: markerInventory && typeof markerInventory === 'object' ? markerInventory : {},
      selections: markerSelections && typeof markerSelections === 'object' ? markerSelections : {},
    });
    for (const diagnostic of inventoryResult.diagnostics ?? []) {
      await log(client, 'warn', 'OpenAbstractions model is omitted because its server reported no request limit', {
        provider: id, model: diagnostic.model, reason: diagnostic.reason,
      });
    }
    for (const warning of inventoryResult.warnings ?? []) {
      await log(client, 'warn', warning.reason, {
        provider: id, model: warning.model, configured: warning.configured, observed: warning.observed,
      });
    }
    for (const diagnostic of policyResult.diagnostics) {
      if (isDefaultSelection(diagnostic.model, options.oaSelectionPolicies?.[diagnostic.model])) continue;
      await log(client, 'warn', 'OpenAbstractions preference is not shown in the current model inventory', {
        provider: id, model: diagnostic.model, reason: diagnostic.reason,
      });
    }
    provider.models = working.models;
    provider.options = working.options;
    // The app's own chat policy: whether the heading lists it, its read, and
    // the servable families OA's default is taken from.
    const chatID = defaultSelectionModel('chat');
    const chat = {
      listed: Boolean(markerSelections && typeof markerSelections === 'object' && Object.hasOwn(markerSelections, chatID)),
      policy: options.oaSelectionPolicies?.[chatID] ?? '',
      read: reads[options.oaSelectionPolicies?.[chatID]] ?? null,
      discovered: markerInventory && typeof markerInventory === 'object' ? markerInventory : {},
    };
    return {models: working.models, inventoryResult, policyResult, chat};
  }
  // At start OpenCode uses the chat model the person chose for it in
  // OpenAbstractions, or OA's default when none is chosen (chatModelAtStart).
  async function applyChatChoice(config, id, chat) {
    const selected = config.model === undefined ? '' : config.model;
    const model = chatModelAtStart({selected, providerID: id, ...chat});
    if (!model) {
      if (chat.read && !['found', 'unknown'].includes(chat.read.outcome)) {
        await log(client, 'info', 'OpenAbstractions could not read the chat model chosen for OpenCode; OpenCode keeps its configured model', {
          provider: id, policy: chat.policy, reason: String(chat.read.outcome ?? ''),
        });
      }
      return;
    }
    config.model = model;
    await log(client, 'info', chat.listed ? 'OpenCode uses the chat model chosen for it in OpenAbstractions'
      : 'No chat model is chosen for OpenCode in OpenAbstractions; OpenCode uses OpenAbstractions\' default', {provider: id, model});
  }
  async function registerCapabilities(options) {
    const placement = providerPlacement(options);
    for (const [name, profile] of Object.entries(capabilityTools)) {
      if (choices.enabled.has(name) || choices.disabled.has(name)) continue;
      try {
        if (await (dependencies.readCapability ?? ((o, p, where) => readCapability(o, p, where, dependencies.resolveRouter ?? resolveRouter)))(
          options, profile, placement)) register(name);
      } catch (error) {
        // With the runtime away the tool is registered, guarded: it says OA
        // is unavailable until OA returns, then works or is refused there.
        if (runtimeUnavailable(error)) {
          state.markDown(error);
          register(name);
          continue;
        }
        await log(client, 'warn', 'OpenAbstractions could not tell whether a tool\'s capability is served; the tool is not registered', {
          tool: `openabstractions_${name}`, profile, reason: failureCode(error),
        });
      }
    }
    // The questions tool registers when the runtime serves responder@1 to
    // this program; the runtime decides what it may answer (asks ASK-R5).
    if (!choices.enabled.has('authorization_questions') && !choices.disabled.has('authorization_questions')) {
      try {
        if (await (dependencies.readAnswerable ?? readAnswerable)(options)) register('authorization_questions');
      } catch {
        // No responder here: the tool stays unregistered.
      }
    }
  }
  return {
    ...sessionHooks,
    provider: {
      id: configuredProviderID,
      models: async (provider, _context = {}) => {
        if (provider?.id !== configuredProviderID || !verifiedConfiguredPackage) {
          throw new Error('OpenAbstractions provider model refresh refused an unverified provider package');
        }
        // OpenCode calls this once while it lists providers, then for each
        // refresh. The listing keeps the configured entries when the runtime
        // cannot answer, because a rejection there fails every provider; a
        // refresh rejects, so OpenCode reports it and keeps its catalog.
        const listing = !listed;
        listed = true;
        const age = now() - (surveyed.get(configuredProviderID) ?? -Infinity);
        if (listing && age >= 0 && age < startupSurveyReuseMs) {
          return providerModelInventory(provider.models ?? {}, configuredProviderID, verifiedConfiguredPackage);
        }
        let result;
        try {
          result = await refresh(provider);
          state.markUp();
        } catch (error) {
          noteFailure(error);
          const older = olderRuntime(error);
          if (!older && !listing) throw error;
          // A runtime without router Offer keeps the configured entries.
          await log(client, 'warn', older ? olderRuntimeMessage : 'OpenAbstractions inventory refresh failed; keeping configured model entries',
            older ? {provider: configuredProviderID} : {provider: configuredProviderID, reason: failureCode(error)});
          return providerModelInventory(provider.models ?? {}, configuredProviderID, verifiedConfiguredPackage);
        }
        await log(client, 'info', 'OpenAbstractions model inventory refreshed', {
          provider: configuredProviderID,
          discovered: Object.keys(result.models).length,
          added: result.inventoryResult.added,
          removed: result.inventoryResult.removed,
          preserved: result.inventoryResult.preserved,
        });
        const models = providerModelInventory(result.models, configuredProviderID, verifiedConfiguredPackage);
        const baseline = runtimeBaselines.get(configuredProviderID);
        if (baseline) {
          runtimeBaselines.set(configuredProviderID, {
            inventory: providerModelInventory(baseline.inventory, configuredProviderID, verifiedConfiguredPackage),
            selections: providerModelInventory(baseline.selections, configuredProviderID, verifiedConfiguredPackage),
          });
        }
        return models;
      },
    },
    tool: tools,
    // The Local assistant never reaches a hosted server: its requests carry
    // OA's local-only guarantee, so OA refuses rather than falls back.
    // OpenCode runs these hooks inside every request, where a throw fails the
    // request; each keeps OpenCode's own values when it cannot do its part.
    'chat.params': async (input, output) => {
      if (input?.agent !== localAgent || !output) return;
      // oaProject names the project whose session start the provider keeps
      // for the next start's preload (preload.js).
      output.options = {...(output.options ?? {}), oaLocalOnly: true,
        ...(typeof directory === 'string' && directory ? {oaProject: directory} : {})};
    },
    // Questions the person let OpenCode answer reach a model on this machine
    // as data in its system text (assistant-answers.js).
    'experimental.chat.system.transform': async (input, output) => {
      if (!tools.openabstractions_authorization_questions || !Array.isArray(output?.system)) return;
      if (input?.model?.providerID !== configuredProviderID || providerPlacements.get(configuredProviderID) !== 'local') return;
      try {
        const text = await questionNotice.notice();
        if (text) output.system.push(text);
      } catch {
        // The request goes on without the notice.
      }
    },
    'tool.definition': async (input, output) => {
      if (!compact || compactOff) return;
      // Another OpenCode's core tool text may carry rules the condensed text
      // lacks: compact mode turns itself off for the rest of this start.
      if (!condensedText(input?.toolID, output?.description)) {
        compactOff = true;
        await log(client, 'warn', `OpenAbstractions turned compact tool text off: OpenCode's tool text differs from OpenCode ${condensedFrom}, which it was condensed from`,
          {tool: String(input?.toolID ?? '')});
        return;
      }
      try {
        compactDefinition(input?.toolID, output, shortToolText);
      } catch (error) {
        await log(client, 'warn', 'OpenAbstractions kept a tool\'s full text', {tool: String(input?.toolID ?? ''), reason: failureCode(error)});
      }
    },
    config: async (config) => {
      try {
        await configure(config);
      } catch (error) {
        noteFailure(error);
        await log(client, 'warn', 'OpenAbstractions could not read its configuration; OpenCode starts without its changes', {
          reason: failureCode(error),
        });
      }
    },
  };

  // At OpenCode's start the Local assistant's model is loaded and evaluates
  // the prefix its last conversation in this project opened with
  // (preload.js; inference preload@1). It runs beside the startup and never
  // delays it: OpenCode's first request may arrive while it runs, and OA
  // serves that request either way.
  function preloadLocal(config, options) {
    const agent = config?.agent?.[localAgent];
    if (!compact || !agent || typeof directory !== 'string' || !directory) return;
    const selected = typeof agent.model === 'string' ? agent.model : typeof config.model === 'string' ? config.model : '';
    if (!selected.startsWith(`${configuredProviderID}/`)) return;
    const model = selected.slice(configuredProviderID.length + 1);
    void (dependencies.preloadKeptPrefix ?? preloadKeptPrefix)({dir: dependencies.preloadStateDir, project: directory, model, options})
      .then(async (reply) => {
        if (!reply) return;
        await log(client, 'info', 'OpenAbstractions preloaded the Local assistant\'s model', {
          provider: configuredProviderID, model, outcome: String(reply.outcome), reason: reply.reason, tokens: String(reply.tokens),
        });
      })
      .catch(async (error) => {
        await log(client, 'info', 'OpenAbstractions did not preload the Local assistant\'s model', {
          provider: configuredProviderID, model, reason: failureCode(error),
        });
      });
  }

  // Whether OA answers one router resolution within startupProbeMs. Only
  // OpenCode's Bun process reaches the native runtime, so elsewhere (and when
  // a test supplies the runtime calls) nothing is probed.
  async function probeStartup(options) {
    const probe = dependencies.probeRuntime
      ?? (typeof Bun === 'undefined' ? null : (o) => (dependencies.resolveRouter ?? resolveRouter)(o));
    if (!probe) return true;
    let timer;
    const late = new Promise((_resolve, reject) => {
      timer = setTimeout(() => reject(Object.assign(new Error('OpenAbstractions did not answer at OpenCode\'s start'),
        {name: 'ResolutionError', status: 'runtime_unavailable'})), startupProbeMs);
      timer.unref?.();
    });
    try {
      await Promise.race([Promise.resolve().then(() => probe(options)), late]);
      return true;
    } catch (error) {
      noteFailure(error);
      await log(client, 'warn', 'OpenAbstractions did not answer at startup; OpenCode starts with its configured models', {
        reason: failureCode(error),
      });
      return false;
    } finally {
      clearTimeout(timer);
    }
  }

  async function configure(config) {
    const {compatible, conflicts} = await providerEntries(config, configuredProviderID);
    const configured = compatible.find(([id]) => id === configuredProviderID);
    verifiedConfiguredPackage = configured?.[1]?.npm ?? (configured ? providerPackage : '');
    for (const [id] of conflicts) {
      await log(client, 'warn', 'OpenAbstractions inventory refresh skipped because this provider ID belongs to another package', {provider: id});
    }
    if (!compatible.length) return;
    // One short probe decides whether the startup asks OA anything more: an
    // OA that is missing or hangs costs OpenCode's start at most startupProbeMs.
    const probeOptions = selectedOptions((configured ?? compatible[0])[1].options ?? {});
    const answered = await probeStartup(probeOptions);
    if (!answered) {
      if (configured) {
        rememberOptions(configuredProviderID, probeOptions);
        // The capability tools register guarded, as for any runtime that is
        // away: they say OA is unavailable until it returns.
        for (const name of Object.keys(capabilityTools)) {
          if (!choices.enabled.has(name) && !choices.disabled.has(name)) register(name);
        }
      }
      // OpenCode's first provider listing keeps the configured entries.
      for (const [id] of compatible) surveyed.set(id, now());
      return;
    }
    let preloadOptions = null;
    if (configured) {
      const options = selectedOptions(configured[1].options ?? {});
      rememberOptions(configuredProviderID, options);
      await registerCapabilities(options);
      preloadOptions = options;
    }
    for (const [id, provider] of compatible) {
      try {
        const result = await refresh(provider, id);
        surveyed.set(id, now());
        state.markUp();
        if (id === configuredProviderID) await applyChatChoice(config, id, result.chat);
        await log(client, 'info', 'OpenAbstractions model inventory refreshed at startup', {
          provider: id,
          discovered: Object.keys(result.models).length,
          added: result.inventoryResult.added,
          removed: result.inventoryResult.removed,
          preserved: result.inventoryResult.preserved,
        });
        const selected = typeof config.model === 'string' ? config.model : '';
        if (selected.startsWith(`${id}/`) && !Object.hasOwn(provider.models ?? {}, selected.slice(id.length + 1))) {
          await log(client, 'warn', 'The selected OpenAbstractions model is no longer in the current inventory', {
            provider: id,
            model: selected.slice(id.length + 1),
          });
        }
      } catch (error) {
        noteFailure(error);
        if (olderRuntime(error)) {
          await log(client, 'warn', olderRuntimeMessage, {provider: id});
          continue;
        }
        await log(client, 'warn', 'OpenAbstractions inventory refresh failed; keeping configured model entries', {
          provider: id,
          reason: failureCode(error),
        });
      }
    }
    // The preload reads the chat model the start selected above.
    if (preloadOptions) preloadLocal(config, preloadOptions);
  }
}

export default OpenAbstractionsPlugin;
