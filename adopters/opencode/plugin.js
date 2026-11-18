import {createHash} from 'node:crypto';
import {runtimeConnector} from './runtime-connector.js';
import {createApplicationPresence, resolveApplications} from './application-presence.js';
import {createMessages, messageToolNames, messageInstructions, agentToolInputSchema} from './messages.js';
import {createReplyOnlyGuard} from './reply-only.js';
import {createModelsConnect} from './models-connect.js';
import {addHostedProvider, currentRememberedProviders, defaultEntryName, candidatePlacement, chatChoiceModel, chatModelAtStart, hostedHeading, legacyHeadings, modelHeadings, ownHeadingName, defaultSelectionModel, inventoryMarkerOption, isDefaultSelection, withDefaultSelectionPolicies, localServers,
  nativeProviderPackage, olderRuntime, olderRuntimeMessage, observeOffers, readOffers, refreshProviderInventory, refreshSelectionPolicyModels, routeModelsOption, selectionPolicyMarkerOption} from './inventory.js';
import {createInventoryWatch, failureState, inventoryChangedMessage, observeWaitMs, offerSignature} from './inventory-watch.js';
import {adopt as adoptHandover, handOver, handingOver, withdraw as withdrawHandover} from './reload-handover.js';
import {createOpenAbstractionsPanelTool, installedRegistrationManifest, requestOpenCodeConnection} from './panel-launch.js';
import {createConnection} from './connection.js';
import {createSelectionPolicyTool} from './selection-policy.js';
import {createSourceWatchTool} from './source-watch.js';
import {createFailureLogsTool, createFailureRecord} from './failure-logs.js';
import {createImageTool} from './image-tool.js';
import {resolveNativePermission} from './permission-wait.js';
import {resolveContentReader} from './content-delivery.js';
import {createSpeakTool, resolveSpeechClient} from './speak.js';
import {compactDefinition, condensedFrom, condensedText} from './compact-tools.js';
import {answered, availability, guardTool, runtimeUnavailable, runtimeUntrusted, untrustedMessage} from './availability.js';
import {capabilityTools, companionTools, createToolsTool, parametersOf, shortToolText, toolChoices, toolNames, toolNameFor} from './tool-config.js';
import {createReminderKeeper, preloadKeptPrefix} from './preload.js';
import {createProjectAccess, projectSessionEnvironment} from './project-access.js';
import {once, outsideSandboxMessage, searchToolMissingMessage} from './opencode-build.js';
import {createStartupMemory, forget, shared, sharedConnection, startupStateDir} from './startup-share.js';

const routerContract = 'abstraction.router/router@1';
const selectionContract = 'abstraction.inference/selection-operator@2';
const sourceReaderContract = 'abstraction.logging/source-reader@1';
const authorizationContract = 'abstraction.rights/authorization@1';
const imageContract = 'abstraction.inference/image@1';
const imageV2Contract = 'abstraction.inference/image@2';
const applicationHistoryContract = 'abstraction.logging/reader@2';
const permissionContract = 'abstraction.asks/permission@1';
const defaultProviderID = 'openabstractions';
// How long the config hook's survey answers OpenCode's first provider listing.
const startupSurveyReuseMs = 30000;
// How long OpenCode's start waits for OA's first answer before it goes on
// without OA (configure's startup probe).
const defaultStartupProbeMs = 750;
const providerIDPattern = /^[a-z][a-z0-9_-]{0,63}$/;
// OpenCode 1.18 re-creates its instance in the same process after a reload,
// and this module stays loaded: the default model the start chose when the
// config names none is kept here, so a reload never moves it to another model.
const startDefaults = new Map();
// The form of a remembered startup answer; an answer of another form is a
// first start (version 2 carries the chat policy's real model as chat.choice;
// version 4 lists the policy route as the picker's "Default (…)" row).
const startupAnswerFormat = 4;
// The installed plugin's last startup answer, shared by every instance of
// this process and kept in OpenCode's data folder for the next process.
let installedStartupMemory = null;
function processStartupMemory(env) {
  if (!installedRegistrationManifest()) return null;
  installedStartupMemory ??= createStartupMemory({dir: startupStateDir(env)});
  return installedStartupMemory;
}

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
 * fresh unless `how.fresh` is false, keeping the plugin's answer reserve
 * (router CONTRACT.md ROUTE-D8). */
async function readInventory(options, placement, resolve = resolveRouter, how = {}) {
  return readOffers(await resolve(options), placement, how);
}

async function resolveRouter(options, {timeout = 5000, signal = null, guarantees = []} = {}) {
  const [{Machine}, {RouterClient}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/router'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: await runtimeConnector(),
    timeout,
    ...(signal ? {cancellation: signal} : {}),
    server: options.server ?? null,
  });
  const binding = await machine.resolveService(routerContract, {
    scope: 'local',
    guarantees,
  });
  return new RouterClient(binding);
}

async function readSelectionPolicy(options, name, connector) {
  return (await resolveSelectionOperator(options, connector)).readEffective(name);
}

// connector replaces the runtime-selected connector; serve's
// TestOpenCodePluginReadsItsOwnChatPolicyUnderInstallationGrants passes the
// Node addon's, so the plugin's own read meets a real runtime.
async function resolveSelectionOperator(options, connector) {
  const [{Machine}, {SelectionOperatorV2Client}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/inference'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: await runtimeConnector(connector),
    timeout: 5000,
    server: options.server ?? null,
  });
  const binding = await machine.resolveService(selectionContract, {scope: 'local'});
  return new SelectionOperatorV2Client(binding);
}

async function resolveSourceReader(options, signal) {
  const [{Machine}, {SourceReaderClient}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/logging'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: await runtimeConnector(),
    timeout: 5000,
    cancellation: signal,
    server: options.server ?? null,
  });
  const binding = await machine.resolveService(sourceReaderContract, {scope: 'local'});
  return new SourceReaderClient(binding);
}

async function resolveAuthorization(options, signal) {
  const [{Machine}, {AuthorizationClient}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/rights'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: await runtimeConnector(), timeout: 5000, cancellation: signal,
    server: options.server ?? null,
  });
  return new AuthorizationClient(await machine.resolveService(authorizationContract, {scope: 'local'}));
}

async function resolveImageClient(options, signal, contract = imageContract) {
  if (contract !== imageContract && contract !== imageV2Contract) throw new TypeError('Unsupported OA image contract');
  const [{Machine}, {ImageClient, ImageV2Client}] = await Promise.all([
    import('@openabstractions/facade'),
    import('@openabstractions/inference/protocol'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: await runtimeConnector(), timeout: 30000, cancellation: signal,
    server: options.server ?? null,
  });
  const binding = await machine.resolveService(contract, {scope: 'local'});
  return contract === imageContract ? new ImageClient(binding) : new ImageV2Client(binding);
}

// What OA recorded about this OpenCode's requests (logging LOG-P9).
async function resolveApplicationHistory(options, signal) {
  const [{Machine}, {ApplicationHistoryReaderClient}] = await Promise.all([
    import('@openabstractions/facade'), import('@openabstractions/logging'),
  ]);
  const machine = new Machine(options.runtimeEndpoint ?? null, {
    connector: await runtimeConnector(), timeout: 5000, cancellation: signal,
    server: options.server ?? null,
  });
  return new ApplicationHistoryReaderClient(await machine.resolveService(applicationHistoryContract, {scope: 'local'}));
}

// OA's own permission prompt for an explicit consent request.
async function resolvePermission(options, signal) {
  return resolveNativePermission(options, signal);
}

async function encodeCallerLogTarget(value) {
  const {encodeApplicationLogResource} = await import('@openabstractions/logging');
  return encodeApplicationLogResource(value);
}

async function providerEntries(config, configuredProviderID) {
  const compatible = [];
  const conflicts = [];
  for (const [id, provider] of Object.entries(config.provider ?? {})) {
    const options = provider?.options;
    const marked = Object.hasOwn(options ?? {}, inventoryMarkerOption);
    if (await nativeProviderPackage(provider)) {
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

async function showRuntimeNotice(client, {variant, message, reason}) {
  // Issue the notice before awaiting diagnostics, including during a cold load.
  // Display failures leave both logging and accepted chat work independent.
  let displayed;
  try {
    displayed = Promise.resolve(client?.tui?.showToast?.({body: {title: 'OpenAbstractions', message, variant}})).catch(() => {});
  } catch { /* The host's display is optional. */ }
  await log(client, variant === 'warning' ? 'warn' : 'info', message, reason ? {reason} : {});
  await displayed;
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
  const env = dependencies.env ?? process.env;
  const now = dependencies.now ?? (() => Date.now());
  // Every instance of this process shares one native Connect or read while
  // it runs, and a connected answer for the startup reuse window.
  const share = {reuseMs: startupSurveyReuseMs, now: () => now()};
  const connectOwner = dependencies.requestOpenCodeConnection ?? requestOpenCodeConnection;
  const readOwner = dependencies.checkOpenCodeConnection ?? requestOpenCodeConnection;
  const connection = createConnection({
    required: dependencies.connectionRequired ?? (!!dependencies.requestOpenCodeConnection || !!installedRegistrationManifest()),
    request: signal => sharedConnection(connectOwner, 'connect', signal, joint => connectOwner({env, signal: joint}), share),
    check: (signal, fresh) => sharedConnection(readOwner, 'read', signal, joint => readOwner({env, signal: joint, readOnly: true}), fresh ? {...share, reuseMs: 0} : share),
  });
  const startupMemory = dependencies.startupMemory !== undefined ? dependencies.startupMemory : processStartupMemory(env);
  const configuredProviderID = typeof _options.providerId === 'string' && providerIDPattern.test(_options.providerId)
    ? _options.providerId : defaultProviderID;
  const runtimeBaselines = new Map();
  const runtimeOptions = new Map();
  const projectSession = projectSessionEnvironment(dependencies.env ?? process.env);
  const startupProject = projectSession && createHash('sha256').update(JSON.stringify([
    projectSession, (dependencies.env ?? process.env).ABSTRACTION_RUNTIME_ENDPOINT,
  ])).digest('hex');
  const optionsFor = (id) => runtimeOptions.get(id) ?? {runtimeEndpoint: undefined,
    ...(projectSession ? {server: projectSession.runtimeServer} : {})};
  const rememberOptions = (id, options) => runtimeOptions.set(id, {runtimeEndpoint: options.runtimeEndpoint,
    ...(options.server === undefined ? {} : {server: options.server})});
  const selectedOptions = (options) => projectSession ? {...options, server: projectSession.runtimeServer} : options;
  // Every OA heading a refresh read: its options, the placements it read, the
  // signature of their offers and whether the runtime answered. The inventory
  // watch (inventory-watch.js) compares the router's current answer with it.
  const headings = new Map();
  let watch = null;
  const readOffersFor = dependencies.readInventory ??
    ((readOptions, readPlacement, how) => readInventory(readOptions, readPlacement, dependencies.resolveRouter ?? resolveRouter, how));
  // The watch follows the router this plugin reads; a supplied inventory
  // reader without an observer follows nothing.
  // One router binding per runtime the headings name, kept across long
  // polls and dropped after a failed one.
  const observeBindings = new Map();
  const observeFor = dependencies.observeInventory ?? (dependencies.readInventory ? null
    : async (o, placement, cursor, waitMs, signal) => {
      const key = JSON.stringify([o.runtimeEndpoint ?? null, o.server ?? null]);
      if (!observeBindings.has(key)) {
        observeBindings.set(key, (dependencies.resolveRouter ?? resolveRouter)(o, {timeout: observeWaitMs + 10000, signal}));
      }
      try {
        return await observeOffers(await observeBindings.get(key), placement, cursor, waitMs);
      } catch (error) {
        observeBindings.delete(key);
        throw error;
      }
    });
  // A failed refresh keeps the heading's last answer and records why.
  const markHeading = (id, options, error) => {
    if (olderRuntime(error)) { headings.delete(id); return; }
    const prior = headings.get(id);
    const placement = providerPlacement(options);
    headings.set(id, {options: prior?.options ?? {...options},
      placements: prior?.placements ?? (placement === 'any' ? ['any', 'local'] : [placement]),
      signature: prior?.signature ?? '', state: failureState(error)});
  };
  // Private V2 bridge: report the exact result from this existing owner,
  // independently of the shared tool-availability notification state.
  const inventoryStatus = (id, error = null, options = null) => {
    if (error) markHeading(id, options ?? selectedOptions(optionsFor(id)), error);
    dependencies.onInventoryRefresh?.(id, error);
  };
  // OpenCode's provider.models hook receives public Provider.Info, which
  // intentionally omits the configured npm package. Bind refresh to the
  // provider ID whose package was verified in the preceding config hook.
  // The config hook verifies package ownership for refresh and Local requests.
  const verifiedProviderPackages = new Map();
  // Whether OpenCode has listed this provider's models once (see provider.models).
  const listed = new Set();
  // When the config hook last surveyed each provider: OpenCode lists the
  // providers right after it, on the first request's path, and a survey that
  // fresh answers that listing without another (startupSurveyReuseMs).
  const surveyed = new Map();
  // The startup survey's OA reads, shared by every instance of this process
  // (startup-share.js): instances that start together ask OA once.
  const startupOwners = {
    probe: dependencies.probeRuntime ?? dependencies.resolveRouter ?? resolveRouter,
    offers: dependencies.readInventory ?? dependencies.resolveRouter ?? resolveRouter,
    capability: dependencies.readCapability ?? dependencies.resolveRouter ?? resolveRouter,
    policy: dependencies.readSelectionPolicy ?? dependencies.connector ?? readSelectionPolicy,
  };
  const runtimeKey = (o) => JSON.stringify([o?.runtimeEndpoint ?? null, o?.server ?? null]);
  const sharedRead = (kind, key, call) => shared(startupOwners[kind], JSON.stringify([kind, ...key]), call, share);
  const forgetStartupReads = () => { for (const owner of Object.values(startupOwners)) forget(owner); };
  const startupProbeMs = dependencies.startupProbeMs ?? defaultStartupProbeMs;
  // Whether OA's runtime answers (availability.js): one toast when it goes
  // away and one when it returns, through OpenCode's TUI.
  const state = dependencies.availability ?? availability;
  state.onChange(notice => showRuntimeNotice(client, notice));
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
  // consent-display draft in
  // feedback/2026-10-02-opencode-upstream-drafts.md would); tests supply their own answer.
  const disclosure = once(dependencies.showsDisclosure ?? (() => false));
  const openCodeTool = dependencies.tool ?? (await import('@opencode-ai/plugin/tool')).tool;
  const choices = toolChoices(_options.tools);
  // OpenCode reads this object after every config hook has run, so the
  // config hook adds the capability tools it finds served.
  const tools = {};
  const presenceBinding = {resolveClient: signal =>
    (dependencies.resolveApplications ?? resolveApplications)(optionsFor(configuredProviderID), signal),
    // Not registered: the installed Panel records the request and asks once.
    refused: () => connection.start()};
  const messagesBinding = {tool:openCodeTool,client,helperProgram:()=>connection.helperProgram(),
    environment:dependencies.env??process.env,...(dependencies.openMessagesHelper?{open:dependencies.openMessagesHelper}:{}),
    ...(dependencies.messagesHelperLimit?{limit:dependencies.messagesHelperLimit}:{})};
  let helperMCPConflict = false;
  // A reload this plugin asked for hands the Messages and presence owners to
  // this instance (reload-handover.js), so an open OA inbox stays open.
  const adopted = adoptHandover(directory);
  let applicationPresence = adopted?.presence ?? createApplicationPresence({directory, ...presenceBinding});
  const keepReminders = createReminderKeeper();
  const messages = createMessages({directory, presence: applicationPresence,
    ready:async()=>{if(helperMCPConflict)return false;await connection.refresh();return !connection.result();},
    reconnect: async()=>{if(helperMCPConflict)return '';await connection.refresh(true);return connection.helperProgram();},
    schedule: dependencies.messagesSchedule ?? setTimeout, unschedule: dependencies.messagesUnschedule ?? clearTimeout,
    wake: dependencies.messagesWake ?? true, ...messagesBinding});
  if (adopted) {
    applicationPresence.rebind(presenceBinding);
  }
  const inboxParts = {presence: applicationPresence};
  let startupDeferred = false;
  let startupCandidate = null;
  let startupResurvey = null;
  let startupReloading = false;
  let startupClosed = false;
  let startupGeneration = 0;
  let reloadRequested = false;
  // Every tool but the Panel launcher calls the runtime, so a transport
  // failure answers the model with one sentence instead of failing the call.
  // Failed OA operations this plugin reported, for oa_logs_read_failure.
  const failures = dependencies.failures ?? createFailureRecord();
  const register = (name) => {
    let definition = toolFactories[name]();
    const messagesTool = messageToolNames.includes(name)||name==='oa_models_connect';
    if(messagesTool){const original=definition;definition={...original,execute:(...args)=>helperMCPConflict?JSON.stringify({outcome:'unavailable',reason:'mcp_connection_conflict'}):original.execute(...args)};}
    tools[toolNameFor(name)] = connection.guard(
      name === 'open_credentials_panel' || messagesTool ? definition : guardTool(definition, state));
    for (const companion of companionTools[name] ?? []) {
      if (!choices.disabled.has(companion) && !tools[toolNameFor(companion)]) register(companion);
    }
  };
  const toolFactories = {
    oa_models_connect:()=>createModelsConnect({tool:openCodeTool,
      resolvePermission:({signal})=>(dependencies.resolvePermission??(how=>resolveNativePermission(optionsFor(configuredProviderID),how.signal)))({signal}),
      resolveRouter:async({signal,guarantees})=>(dependencies.resolveRouter??resolveRouter)(optionsFor(configuredProviderID),{signal,guarantees})}),
    ...Object.fromEntries(messageToolNames.map(name => [name, () => { if (name === 'oa_messages_wait') messages.enable(); return messages.definitions[name]; }])),
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
    failure_logs: () => createFailureLogsTool({
      tool: openCodeTool,
      failures,
      resolveHistory: async ({signal}) => answered((dependencies.resolveApplicationHistory ?? resolveApplicationHistory)(
        optionsFor(configuredProviderID), signal,
      ), state),
      resolvePermission: async ({signal}) => answered((dependencies.resolvePermission ?? resolvePermission)(
        optionsFor(configuredProviderID), signal,
      ), state),
      encodeTarget: dependencies.encodeCallerLogTarget ?? encodeCallerLogTarget,
      // A paired machine's binding is a router host with a remote domain.
      isPairedHost: async ({host}) => {
        const router = await answered((dependencies.resolveRouter ?? resolveRouter)(optionsFor(configuredProviderID)), state);
        const snapshot = await router.hosts(false);
        return (snapshot?.hosts ?? []).some((entry) => entry?.host === host && typeof entry.domain === 'string' && entry.domain !== '');
      },
      resolveAuthorization: async ({signal}) => answered((dependencies.resolveAuthorization ?? resolveAuthorization)(
        optionsFor(configuredProviderID), signal,
      ), state),
      showsDisclosure: disclosure,
    }),
    generate_image: () => createImageTool({
      tool: openCodeTool,
      failures,
      resolvePermission: ({signal}) => (dependencies.resolvePermission ?? resolvePermission)(optionsFor(configuredProviderID), signal),
      resolveImage: async ({signal, contract}) => answered((dependencies.resolveImageClient ?? resolveImageClient)(
        optionsFor(configuredProviderID), signal, contract,
      ), state),
      resolveReader: async ({signal}) => answered((dependencies.resolveContentReader ?? resolveContentReader)(
        optionsFor(configuredProviderID), signal,
      ), state),
      delegate: (args, context) => messages.call('oa_image_generate', args, context),
    }),
    speak: () => createSpeakTool({
      tool: openCodeTool,
      resolvePermission: ({signal}) => (dependencies.resolvePermission ?? resolvePermission)(optionsFor(configuredProviderID), signal),
      resolveSpeech: async ({signal}) => answered((dependencies.resolveSpeechClient ?? resolveSpeechClient)(
        optionsFor(configuredProviderID), signal,
      ), state),
      resolveReader: async ({signal}) => answered((dependencies.resolveContentReader ?? resolveContentReader)(
        optionsFor(configuredProviderID), signal,
      ), state),
    }),
  };
  for (const name of toolNames) if (choices.enabled.has(name)) register(name);
  const projectAccess = createProjectAccess({tool: openCodeTool, session: projectSession, client,
    ...(dependencies.resolveProjectClient ? {resolveClient: dependencies.resolveProjectClient} : {}),
    announce: async (message) => {
      await log(client, 'warn', message);
      await client?.tui?.showToast?.({body: {title: 'OpenAbstractions project access', message, variant: 'warning'}});
    }});
  if (projectAccess) tools[toolNameFor('project_access')] = connection.guard(projectAccess.definition);
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
    tools.oa_tools_list = connection.guard(createToolsTool({tool: openCodeTool, registered: () => tools,
      full: (name) => {
        const definition = tools[name];
        return definition ? {description: definition.description, parameters: parametersOf(openCodeTool, definition)} : null;
      }}));
  }
  if (choices.unknown.length) {
    await log(client, 'warn', 'OpenAbstractions ignored tools options it does not know', {names: choices.unknown});
  }
  async function refresh(provider, idHint = configuredProviderID, startup = false) {
    const id = provider.id ?? idHint;
    const prior = runtimeBaselines.get(id);
    const options = {...selectedOptions(provider.options ?? {})};
    rememberOptions(id, options);
    if (prior) {
      options[inventoryMarkerOption] = {...(options[inventoryMarkerOption] ?? {}), ...prior.inventory};
      options[selectionPolicyMarkerOption] = {...(options[selectionPolicyMarkerOption] ?? {}), ...prior.selections};
    }
    const working = {...provider, options, models: {...(provider.models ?? {})}};
    // The startup survey shares its reads with every instance of this process.
    const read = (readOptions, readPlacement) => startup
      ? sharedRead('offers', [runtimeKey(readOptions), readPlacement], () => readOffersFor(readOptions, readPlacement, {fresh: true}))
      : readOffersFor(readOptions, readPlacement, {fresh: true});
    const readPolicy = (name) => {
      const call = () => (dependencies.readSelectionPolicy ?? ((o, n) => readSelectionPolicy(o, n, dependencies.connector)))(options, name);
      return startup ? sharedRead('policy', [runtimeKey(options), name], call) : call();
    };
    // Scope local offers this machine's servers; scope remote offers hosted
    // servers and remote runtimes too.
    const placement = providerPlacement(options);
    const placements = [placement];
    // Keep the hosted placement observed even before its first credential
    // makes a model available; that change uses the existing inventory reload.
    if (id === configuredProviderID && placement === 'local') {
      placements.push('hosted');
    }
    // Where each model runs comes from the router's placements: everything at
    // local is on this computer, nothing at hosted is, and at any the local
    // placement's servers are.
    if (placement === 'any') placements.push('local');
    // Each call still resolves and authorizes its own placement. Concurrent
    // fresh calls let the router's existing survey owner join the host reads.
    const receive = async where => [where, (await read(options, where)).offers ?? []];
    const offersByPlacement = {};
    if (startup) {
      Object.assign(offersByPlacement, Object.fromEntries(await Promise.all(placements.map(receive))));
    } else {
      for (const where of placements) {
        const [key, offers] = await receive(where);
        offersByPlacement[key] = offers;
      }
    }
    const isLocal = placement === 'any' ? localServers(offersByPlacement.local) : () => placement === 'local';
    // A heading this plugin named reads as today's name: the hosted one names
    // its services ("DeepSeek · OpenAbstractions").
    if (legacyHeadings[working.name] === 'local') working.name = modelHeadings.local;
    if (placement === 'hosted' && ownHeadingName(working.name) && (offersByPlacement.hosted ?? []).length) {
      working.name = hostedHeading(offersByPlacement.hosted);
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
    // At startup the policies are read together; a refresh reads them in turn.
    const pending = startup ? new Map(names.map(name => [name, readPolicy(name)])) : null;
    pending?.forEach(promise => promise.catch(() => {}));
    for (const name of names) {
      try {
        reads[name] = await (pending ? pending.get(name) : readPolicy(name));
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
    headings.set(id, {options: {...options}, placements: Object.keys(offersByPlacement),
      signature: offerSignature(offersByPlacement), state: 'answered'});
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
    if (working.name !== undefined) provider.name = working.name;
    provider.options = working.options;
    // The app's own chat policy: the real model it chooses, its read, and
    // the servable families OA's default is taken from.
    const chatID = defaultSelectionModel('chat');
    const discovered = markerInventory && typeof markerInventory === 'object' ? markerInventory : {};
    const chatRead = reads[options.oaSelectionPolicies?.[chatID]] ?? null;
    const chat = {
      choice: chatChoiceModel(id, chatRead, offersByPlacement, discovered),
      policy: options.oaSelectionPolicies?.[chatID] ?? '',
      read: chatRead, discovered,
    };
    return {models: working.models, inventoryResult, policyResult, chat, offersByPlacement};
  }
  // At start OpenCode uses the chat model the person chose for it in
  // OpenAbstractions, or OA's default when none is chosen (chatModelAtStart).
  async function applyChatChoice(config, id, chat, {keep = true} = {}) {
    const selected = config.model === undefined ? '' : config.model;
    let model = chatModelAtStart({selected, providerID: id, ...chat});
    // OA's default for a config naming no model stays the one this OpenCode
    // process started with while that model is still offered.
    const defaults = dependencies.startDefaults ?? startDefaults;
    const key = `${typeof directory === 'string' ? directory : ''}\0${id}`;
    const kept = defaults.get(key);
    if (model && !chat.choice && kept && kept !== model && Object.hasOwn(chat.discovered ?? {}, kept.slice(id.length + 1))) model = kept;
    if (model && !chat.choice && keep) defaults.set(key, model);
    if (!model) {
      if (chat.read && !['found', 'unknown'].includes(chat.read.outcome)) {
        await log(client, 'info', 'OpenAbstractions could not read the chat model chosen for OpenCode; OpenCode keeps its configured model', {
          provider: id, policy: chat.policy, reason: String(chat.read.outcome ?? ''),
        });
      }
      return;
    }
    config.model = model;
    await log(client, 'info', chat.choice ? 'OpenCode uses the chat model chosen for it in OpenAbstractions'
      : 'No chat model is chosen for OpenCode in OpenAbstractions; OpenCode uses OpenAbstractions\' default. Change the model OpenAbstractions picks for OpenCode in the Panel → Applications → OpenCode.', {provider: id, model});
  }
  async function registerCapabilities(options) {
    const placement = providerPlacement(options);
    const readOne = dependencies.readCapability ?? ((o, p, where) => readCapability(o, p, where, dependencies.resolveRouter ?? resolveRouter));
    // Every capability is asked at once, shared with this process's instances.
    const asked = new Map(Object.entries(capabilityTools)
      .filter(([name]) => !choices.enabled.has(name) && !choices.disabled.has(name))
      .map(([name, profile]) => [name, sharedRead('capability', [runtimeKey(options), profile, placement],
        () => readOne(options, profile, placement))]));
    asked.forEach(promise => promise.catch(() => {}));
    for (const [name, profile] of Object.entries(capabilityTools)) {
      if (!asked.has(name)) continue;
      try {
        if (await asked.get(name)) register(name);
      } catch (error) {
        // With the runtime away the tool is registered, guarded: it says OA
        // is unavailable until OA returns, then works or is refused there.
        if (runtimeUnavailable(error)) {
          state.markDown(error);
          register(name);
          continue;
        }
        await log(client, 'warn', 'OpenAbstractions could not tell whether a tool\'s capability is served; the tool is not registered', {
          tool: toolNameFor(name), profile, reason: failureCode(error),
        });
      }
    }
  }
  function publicInventory(models, id, packageName) {
    const result = providerModelInventory(models, id, packageName);
    const baseline = runtimeBaselines.get(id);
    if (baseline) {
      runtimeBaselines.set(id, {
        inventory: providerModelInventory(baseline.inventory, id, packageName),
        selections: providerModelInventory(baseline.selections, id, packageName),
      });
    }
    return result;
  }
  // S5 (2026-11-22): a turn a peer message started is reply-only.
  const replyOnlyGuard = createReplyOnlyGuard({messages, directory, resolveReal: dependencies.resolveReal});
  return {
    ...sessionHooks,
    'tool.execute.before': async (input, output) => {
      await sessionHooks['tool.execute.before']?.(input, output);
      await replyOnlyGuard(input, output);
    },
    'experimental.chat.messages.transform': async (input,output) => {
      await sessionHooks['experimental.chat.messages.transform']?.(input,output);
      messages.transform(output);
      // Last: the keeper records every reminder appended above (preload.js).
      keepReminders(output);
    },
    event: async input => {
      if (input?.event?.type === 'server.instance.disposed' && input.event.properties?.directory === directory) {
        startupClosed = true;
      }
      // Presence is optional and never blocks host startup or event delivery.
      const retiring = ['session.deleted', 'server.instance.disposed'].includes(input?.event?.type)
        || input?.event?.type === 'session.error' && input.event.properties?.error?.name === 'MessageAbortedError';
      // An instance re-created for a reload leaves the inbox to the next one.
      if (input?.event?.type === 'server.instance.disposed' && handingOver(directory, inboxParts)) {
        await messages.close();
        await sessionHooks.event?.(input);
        return;
      }
      if (retiring) {
        try { await messages.event(input); }
        finally { await applicationPresence.event(input); }
      }
      else { void applicationPresence.event(input); await messages.event(input); }
      // A host event shows OpenCode's server running: the project's recent
      // closed chats become reachable once. A failed reach (OA not ready, the
      // session list or a helper failing) retries at a later event.
      if (!startupClosed && !helperMCPConflict) void messages.reachDormantDue?.();
      await sessionHooks.event?.(input);
      if (input?.event?.type === 'session.status' && input.event.properties?.status?.type === 'idle') {
        void reloadStartup();
      }
    },
    dispose: async () => {
      startupClosed = true;
      watch?.stop();
      try { await messages.close(); }
      finally { await connection.close();if(!handingOver(directory,inboxParts))await applicationPresence.close(); }
    },
    provider: {
      id: configuredProviderID,
      models: async (provider, _context = {}) => {
        const id = provider?.id;
        const verifiedConfiguredPackage = verifiedProviderPackages.get(id);
        if (!verifiedConfiguredPackage) {
          throw new Error('OpenAbstractions provider model refresh refused an unverified provider package');
        }
        if (startupDeferred && !listed.has(id)) {
          listed.add(id);
          return publicInventory(provider.models ?? {}, id, verifiedConfiguredPackage);
        }
        // The model list is OpenCode's own read: the runtime authorizes it
        // for the registered host, and the helper's Connect guards the tools.
        // OpenCode calls this once while it lists providers, then for each
        // refresh. The listing keeps the configured entries when the runtime
        // cannot answer, because a rejection there fails every provider; a
        // refresh rejects, so OpenCode reports it and keeps its catalog.
        const listing = !listed.has(id);
        listed.add(id);
        const age = now() - (surveyed.get(id) ?? -Infinity);
        if (listing && age >= 0 && age < startupSurveyReuseMs) {
          return publicInventory(provider.models ?? {}, id, verifiedConfiguredPackage);
        }
        let result;
        try {
          result = await refresh(provider);
          inventoryStatus(id);
          state.markUp();
        } catch (error) {
          inventoryStatus(id, error, selectedOptions(provider.options ?? {}));
          noteFailure(error);
          const older = olderRuntime(error);
          if (!older && !listing) {
            if (runtimeUnavailable(error)) {
              throw new Error('OpenAbstractions is unavailable; the model list was not refreshed. Start the runtime and try again.', {cause: error});
            }
            throw error;
          }
          // A runtime without router Offer keeps the configured entries.
          await log(client, 'warn', older ? olderRuntimeMessage : 'OpenAbstractions inventory refresh failed; keeping configured model entries',
            older ? {provider: id} : {provider: id, reason: failureCode(error)});
          return publicInventory(provider.models ?? {}, id, verifiedConfiguredPackage);
        }
        await log(client, 'info', 'OpenAbstractions model inventory refreshed', {
          provider: id,
          discovered: Object.keys(result.models).length,
          added: result.inventoryResult.added,
          removed: result.inventoryResult.removed,
          preserved: result.inventoryResult.preserved,
        });
        return publicInventory(result.models, id, verifiedConfiguredPackage);
      },
    },
    tool: tools,
    'experimental.chat.system.transform': async (_input, output) => {
      if (typeof messageInstructions === 'string' && messageToolNames.some(name=>tools[name])
        && Array.isArray(output?.system) && !output.system.includes(messageInstructions)) output.system.push(messageInstructions);
    },
    // The Local assistant never reaches a hosted server: its requests carry
    // OA's local-only guarantee, so OA refuses rather than falls back.
    // OpenCode runs these hooks inside every request, where a throw fails the
    // request; each keeps OpenCode's own values when it cannot do its part.
    'chat.params': async (input, output) => {
      if (!output) return;
      const selectedPackage = verifiedProviderPackages.get(input?.model?.providerID);
      // V1 reports the chosen model's SDK package. V2 has no Model.api;
      // its shim supplies the existing registered native Provider.Info.
      const routedPackage = input?.model?.api?.npm ??
        (input?.provider?.info?.id === input?.model?.providerID ? input?.provider?.info?.package : undefined);
      if (selectedPackage && routedPackage === selectedPackage) {
        // The host supplies each session's own ID, including a subagent's.
        // Titles carry no conversation label and are marked as titles, so
        // the engine runs them beside the chat's prompt cache. Never reuse
        // an earlier marker.
        output.options = {...(output.options ?? {})};
        delete output.options.oaSessionID;
        delete output.options.oaTitle;
        if (input?.agent === 'title') output.options.oaTitle = true;
        else if (typeof input?.sessionID === 'string' && input.sessionID) {
          output.options.oaSessionID = input.sessionID;
        }
      }
      if (input?.agent !== localAgent) return;
      if (!selectedPackage || routedPackage !== selectedPackage) {
        throw new Error('Local assistant keeps this conversation on this computer through OpenAbstractions. Choose an OpenAbstractions model, or press Tab for the Build agent.');
      }
      // oaProject names the project whose session start the provider keeps
      // for the next start's preload (preload.js).
      output.options = {...(output.options ?? {}), oaLocalOnly: true,
        ...(typeof directory === 'string' && directory ? {oaProject: directory} : {})};
    },
    'tool.definition': async (input, output) => {
      const nativeSchema=tools[input?.toolID]&&agentToolInputSchema(input.toolID);
      if(nativeSchema&&output)output.jsonSchema=nativeSchema;
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
      } finally {
        if(nativeSchema&&output)output.jsonSchema=nativeSchema;
      }
    },
    config: async (config) => {
      helperMCPConflict=!!config?.mcp?.OpenAbstractions&&config.mcp.OpenAbstractions.enabled!==false;
      if(helperMCPConflict){for(const name of [...messageToolNames,'oa_models_connect'])delete tools[name];await messages.suspend();}
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
    void (dependencies.preloadKeptPrefix ?? preloadKeptPrefix)({dir: dependencies.preloadStateDir, project: directory, model, options, warmIfMissing: true})
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

  // Whether OA answers one router resolution within startupProbeMs.
  async function probeStartup(options) {
    const probe = dependencies.probeRuntime ?? (o => (dependencies.resolveRouter ?? resolveRouter)(o));
    let timer;
    const late = new Promise((_resolve, reject) => {
      timer = setTimeout(() => reject(Object.assign(new Error('OpenAbstractions did not answer at OpenCode\'s start'),
        {name: 'ResolutionError', status: 'runtime_unavailable'})), startupProbeMs);
      timer.unref?.();
    });
    try {
      await Promise.race([sharedRead('probe', [runtimeKey(options)], () => probe(options)), late]);
      return {answered: true};
    } catch (error) {
      noteFailure(error);
      await log(client, 'warn', 'OpenAbstractions did not answer at startup; OpenCode starts with its configured models', {
        reason: failureCode(error),
      });
      return {answered: false, error};
    } finally {
      clearTimeout(timer);
    }
  }

  async function configure(config) {
    const generation = ++startupGeneration;
    // Another config pass of this same instance asks OA afresh.
    if (generation > 1) forgetStartupReads();
    startupCandidate = null;
    startupResurvey = null;
    // OpenCode Desktop gives every instance the provider map of its cached
    // global config by reference (remeda mergeDeep keeps a key only one side
    // has). This instance writes its own map and its own OA headings, so no
    // write reaches the cache that every later instance, including a reloaded
    // one, starts from.
    if (config?.provider && typeof config.provider === 'object') config.provider = {...config.provider};
    const found = await providerEntries(config, configuredProviderID);
    const conflicts = found.conflicts;
    const compatible = found.compatible.map(([id, provider]) => {
      const own = {...provider};
      if (legacyHeadings[own.name] === 'local') own.name = modelHeadings.local;
      config.provider[id] = own;
      return [id, own];
    });
    verifiedProviderPackages.clear();
    for (const [id, provider] of compatible) verifiedProviderPackages.set(id, provider.npm);
    const configured = compatible.find(([id]) => id === configuredProviderID);
    for (const [id] of conflicts) {
      await log(client, 'warn', 'OpenAbstractions inventory refresh skipped because this provider ID belongs to another package', {provider: id});
    }
    const handover = adoptHandover(directory, {startup: true});
    // OpenCode runs this hook inside its instance's start, before init, so
    // OA's answers are asked in the background wherever OpenCode can reload
    // the instance once they differ (deferStartup). An instance this plugin
    // re-created for a model list change asks OA here, as that change needs.
    const deferred = configured && !dependencies.onInventoryChange && !adopted
      && typeof client?.instance?.dispose === 'function' && typeof client?.session?.status === 'function';
    if (handover && !deferred) void handover.presence.close().catch(() => {});
    if (!compatible.length) return;
    const memoryKey = answerKey(config);
    const original = new Set(Object.keys(config.provider ?? {}));
    if (configured) {
      rememberOptions(configuredProviderID, selectedOptions(configured[1].options ?? {}));
      for (const name of [...messageToolNames,'oa_models_connect']) {
        // A helper capability tool registers with its served profile.
        if (Object.hasOwn(capabilityTools, name)) continue;
        if (!helperMCPConflict&&!choices.enabled.has(name) && !choices.disabled.has(name)) register(name);
      }
      if (deferred) {
        // The last startup answer is a local read, never a call to OA.
        // A warm hand-over wins over it; an expired or mismatched one reads it.
        // With none for this config, the answer kept last (an earlier
        // version's or an earlier config's) lists OA's models until OA answers.
        const remembered = !startupMemory ? null
          : await startupMemory.recall(memoryKey).catch(() => null) ?? await latestAnswer(config).catch(() => null);
        await deferStartup(config, compatible, generation, handover, remembered, memoryKey, original);
        return;
      }
      // The first-use Connect for the helper's tools runs beside the model
      // list, which the runtime answers for the registered host alone.
      void connection.start().then(() => connection.refresh()).catch(() => {});
    }
    const facts = {};
    if (await surveyStartup(config, compatible, generation, true, facts)) {
      void startupMemory?.remember(startupAnswer(memoryKey, config, original, new Set(compatible.map(([id]) => id)), facts))
        .catch(() => {});
    }
  }

  function startupKey(config) {
    return createHash('sha256').update(JSON.stringify([
      startupProject, configuredProviderID, _options, typeof config.model, config.model, config.provider, config.agent,
    ])).digest('hex');
  }

  // The key of the last startup answer: the config without the model, which
  // the answer's chat facts choose again for the config at hand.
  function answerKey(config) {
    return createHash('sha256').update(JSON.stringify([
      startupAnswerFormat, startupProject, configuredProviderID, _options, config.provider,
    ])).digest('hex');
  }

  // What a verified startup survey found, kept for the next start: the
  // headings it wrote, the capability tools it registered and the chat facts
  // the default model is chosen from.
  function startupAnswer(key, config, original, ids, facts) {
    return {
      key, scope: answerScope(), chat: facts.chat ?? null,
      providers: Object.entries(config.provider).filter(([id]) => ids.has(id) || !original.has(id)),
      capabilities: Object.keys(capabilityTools).filter(name => Object.hasOwn(tools, toolNameFor(name))),
    };
  }

  // The project session and heading an answer belongs to; an answer from
  // another scope never lists here.
  function answerScope() {
    return createHash('sha256').update(JSON.stringify([startupProject, configuredProviderID])).digest('hex');
  }

  // The answer kept last, for a start with none of its own: an earlier
  // version's answer under today's names and Default row
  // (currentRememberedProviders). The config's own heading keeps its options
  // and name; only its models and OA's markers come from the answer. An answer
  // from another scope, or an earlier version's inside a project session, is
  // not used.
  async function latestAnswer(config) {
    if (typeof startupMemory?.latest !== 'function') return null;
    const latest = await startupMemory.latest();
    if (!latest || (latest.scope ?? (startupProject ? null : answerScope())) !== answerScope()) return null;
    const main = config.provider?.[configuredProviderID];
    const ownOptions = main?.options && typeof main.options === 'object' ? main.options : {};
    const providers = currentRememberedProviders(latest.providers, latest.chat, configuredProviderID).map(([id, provider]) => {
      if (id === configuredProviderID && main) {
        const options = {...ownOptions};
        for (const name of [inventoryMarkerOption, selectionPolicyMarkerOption, routeModelsOption]) {
          if (Object.hasOwn(provider.options ?? {}, name)) options[name] = provider.options[name];
        }
        options.oaSelectionPolicies = withDefaultSelectionPolicies(ownOptions);
        // The config's own entries stay; the answer adds the models OA listed.
        const listed = {...(provider.options?.[inventoryMarkerOption] ?? {}), ...(provider.options?.[selectionPolicyMarkerOption] ?? {})};
        const models = {...(main.models ?? {})};
        for (const [model, entry] of Object.entries(provider.models ?? {})) {
          if (Object.hasOwn(listed, model) && !Object.hasOwn(models, model)) models[model] = entry;
        }
        return [id, {...main, models, options}];
      }
      // A heading the answer added runs on the config's own runtime.
      const options = {...(provider.options ?? {})};
      delete options.runtimeEndpoint;
      delete options.server;
      if (ownOptions.runtimeEndpoint !== undefined) options.runtimeEndpoint = ownOptions.runtimeEndpoint;
      if (ownOptions.server !== undefined) options.server = ownOptions.server;
      return [id, {...provider, options}];
    });
    return {...latest, providers};
  }

  // Applies the last startup answer to the config OpenCode is starting with.
  // Only this plugin's own package is written, under the headings the config
  // verified or the new headings the answer added.
  async function applyRemembered(config, answer, ids) {
    const npm = verifiedProviderPackages.get(configuredProviderID);
    for (const [id, provider] of answer.providers) {
      if (provider?.npm !== npm || !providerIDPattern.test(id)) continue;
      if (!ids.has(id) && Object.hasOwn(config.provider, id)) continue;
      config.provider[id] = structuredClone(provider);
      verifiedProviderPackages.set(id, npm);
    }
    // A remembered default is no default this process started with.
    if (answer.chat) await applyChatChoice(config, configuredProviderID, answer.chat, {keep: false});
    for (const name of answer.capabilities) {
      if (Object.hasOwn(capabilityTools, name) && !choices.disabled.has(name)) register(name);
    }
  }

  // OA's own chat before OA has ever answered: the main heading lists one
  // entry, "Default", under the `oa-chat` route that OA resolves through the
  // app's own chat policy when a request arrives, and a config that names no
  // model selects it. Its context is unknown (0) until OA answers. The entry
  // is marked as OA's, and OA's first answer, applied by the background
  // reload and remembered for every later start, replaces it with the real
  // models and "Default (<the Panel's default>)".
  function placeOwnChat(config, {entry: placeEntry = true} = {}) {
    const provider = config.provider?.[configuredProviderID];
    if (!provider || typeof provider !== 'object') return null;
    const chatID = defaultSelectionModel('chat');
    const models = provider.models && typeof provider.models === 'object' ? provider.models : {};
    const options = provider.options && typeof provider.options === 'object' ? provider.options : {};
    let placed = false;
    if (placeEntry && !Object.hasOwn(models, chatID)) {
      const entry = {name: defaultEntryName(''), limit: {context: 0, output: 4096}, tool_call: true};
      const markers = options[selectionPolicyMarkerOption];
      const owned = markers && typeof markers === 'object' && !Array.isArray(markers) ? markers : {};
      config.provider[configuredProviderID] = {...provider, models: {...models, [chatID]: entry},
        options: {...options, oaSelectionPolicies: withDefaultSelectionPolicies(options),
          [selectionPolicyMarkerOption]: {...owned, [chatID]: structuredClone(entry)}}};
      placed = true;
    }
    const listed = placed || Object.hasOwn(models, chatID);
    if (listed && (config.model === undefined || config.model === '')) config.model = `${configuredProviderID}/${chatID}`;
  }

  async function deferStartup(config, compatible, generation, handover, remembered, memoryKey, original) {
    startupDeferred = true;
    const key = startupKey(config);
    const adoptedSnapshot = handover?.startupSnapshot;
    const warm = adoptedSnapshot?.key === key && now() < adoptedSnapshot.expires;
    const ids = new Set(compatible.map(([id]) => id));
    // The person's own model: the survey chooses again from it whenever the
    // plugin set the model from a remembered answer or OA's own chat.
    const before = config.model;
    if (handover && !warm) void handover.presence.close().catch(() => {});
    if (warm) {
      void applicationPresence.close().catch(() => {});
      applicationPresence = handover.presence;
      inboxParts.presence = applicationPresence;
      applicationPresence.rebind(presenceBinding);
      for (const [id, provider] of adoptedSnapshot.providers) {
        config.provider[id] = structuredClone(provider);
        verifiedProviderPackages.set(id, provider.npm);
      }
      if (adoptedSnapshot.model !== undefined) config.model = adoptedSnapshot.model;
      for (const name of adoptedSnapshot.capabilities) register(name);
    } else if (remembered) {
      await applyRemembered(config, remembered, ids);
    }
    // With no answer yet, OA's own chat is the default from the first
    // request (placeOwnChat); the survey below starts from the config as it was.
    if (!warm) placeOwnChat(config, {entry: !remembered});
    const baseline = {...config, provider: structuredClone(config.provider)};
    const current = () => !startupClosed && startupGeneration === generation;
    async function survey() {
      const working = {...baseline, provider: structuredClone(baseline.provider)};
      if (!warm && baseline.model !== before) {
        if (before === undefined) delete working.model;
        else working.model = before;
      }
      const entries = [...ids].map(id => [id, working.provider[id]]);
      const facts = {};
      const verified = await surveyStartup(working, entries, generation, false, facts);
      if (!current()) return null;
      if (verified && startupMemory) {
        void startupMemory.remember(startupAnswer(memoryKey, working, original, ids, facts)).catch(() => {});
      }
      if (!verified || startupKey(working) === startupKey(baseline)) {
        if (verified) preloadLocal(working, optionsFor(configuredProviderID));
        if (headings.size) startWatch();
        return null;
      }
      return {
        key, expires: now() + startupSurveyReuseMs, model: working.model,
        providers: Object.entries(working.provider).filter(([id]) => ids.has(id) || !original.has(id)),
        headings: structuredClone([...headings]),
        capabilities: Object.keys(capabilityTools).filter(name => Object.hasOwn(tools, toolNameFor(name))),
      };
    }
    // A warm handover carries data only. This new project instance proves
    // its own native readiness for the helper's tools, using a read before
    // another Connect prompt. The model list goes on beside it: the runtime
    // answers it for the registered host, and it never waits on the person.
    void (async () => {
      if (warm) await connection.refresh();
      if (!current()) return;
      await connection.start();
      await connection.refresh();
    })().catch(() => {});
    void (async () => {
      if (!current()) return;
      if (warm) {
        for (const [id, heading] of adoptedSnapshot.headings) headings.set(id, heading);
        preloadLocal(baseline, optionsFor(configuredProviderID));
        if (headings.size) startWatch();
        return;
      }
      const snapshot = await survey();
      if (!current()) return;
      startupCandidate = snapshot;
      startupResurvey = snapshot ? survey : null;
      await reloadStartup();
    })().catch(error => {
      if (!current()) return;
      noteFailure(error);
      void log(client, 'warn', 'OpenAbstractions could not refresh its startup catalogue', {reason: failureCode(error)});
    });
  }

  async function reloadStartup() {
    if (!startupCandidate || startupReloading || startupClosed) return;
    startupReloading = true;
    try {
      let snapshot = startupCandidate;
      if (now() >= snapshot.expires) {
        const resurvey = startupResurvey;
        const generation = startupGeneration;
        if (!await allIdle(true) || startupClosed || startupGeneration !== generation) return;
        startupCandidate = startupResurvey = null;
        snapshot = resurvey ? await resurvey() : null;
        if (startupClosed || startupGeneration !== generation || !snapshot) return;
        startupCandidate = snapshot;
      }
      if (await reloadInstance({id: configuredProviderID, state: 'answered'}, snapshot) !== false) {
        startupCandidate = startupResurvey = null;
      }
    } catch (error) {
      await log(client, 'warn', 'OpenAbstractions could not check whether startup models can reload', {reason: failureCode(error)});
    } finally {
      startupReloading = false;
    }
  }

  async function surveyStartup(config, compatible, generation, watchAfter = true, facts = {}) {
    const current = () => !startupClosed && startupGeneration === generation;
    const configured = compatible.find(([id]) => id === configuredProviderID);
    // Headings sharing one endpoint/trust expectation share its short probe.
    // Distinct configured runtimes probe within the same startup time bound.
    const probes = new Map();
    const prepared = await Promise.all(compatible.map(async ([id, provider]) => {
      const options = selectedOptions(provider.options ?? {});
      const key = JSON.stringify([options.runtimeEndpoint ?? null, options.server ?? null]);
      if (!probes.has(key)) probes.set(key, probeStartup(options));
      return {id, provider, options, ...await probes.get(key)};
    }));
    if (!current()) return false;
    let preloadOptions = null;
    if (configured) {
      const {options, answered} = prepared.find(({id}) => id === configuredProviderID);
      rememberOptions(configuredProviderID, options);
      // Both native connectors authenticate the actual calling program. Keep
      // the shared catalogue available through startup outages; each explicit
      // call checks current host context, presence and native admission.
      if (answered) {
        await registerCapabilities(options);
        preloadOptions = options;
      } else {
        // Tools remain guarded until this runtime returns.
        for (const name of Object.keys(capabilityTools)) {
          if (!choices.enabled.has(name) && !choices.disabled.has(name)) register(name);
        }
      }
    }
    let verified = true;
    for (const {id, provider, options, answered, error} of prepared) {
      if (!current()) return false;
      if (!answered) {
        verified = false;
        inventoryStatus(id, error, options);
        surveyed.set(id, now());
        continue;
      }
      try {
        const result = await refresh(provider, id, true);
        if (!current()) return false;
        if (id === configuredProviderID && providerPlacement(options) === 'local') {
          const hosted = addHostedProvider(config, id, result.offersByPlacement.hosted);
          if (hosted && watchAfter) {
            verifiedProviderPackages.set(hosted.id, hosted.provider.npm);
          }
        }
        inventoryStatus(id);
        surveyed.set(id, now());
        state.markUp();
        if (id === configuredProviderID) {
          facts.chat = result.chat;
          await applyChatChoice(config, id, result.chat);
        }
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
        verified = false;
        inventoryStatus(id, error, selectedOptions(provider.options ?? {}));
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
    if (!current()) return false;
    // The preload reads the chat model the start selected above.
    if (watchAfter && preloadOptions) preloadLocal(config, preloadOptions);
    if (watchAfter && headings.size) startWatch();
    return verified;
  }

  // OA's inventory changing while OpenCode runs (inventory-watch.js). V2
  // reloads its providers itself (v2.js passes onInventoryChange); OpenCode
  // 1.18 snapshots its providers per instance, so the plugin asks OpenCode to
  // re-create this instance once every session is idle. The new instance runs
  // this config hook again with the current inventory, and OpenCode's TUI and
  // Desktop reload their lists on its server.instance.disposed event. An open
  // OA inbox and its presence are handed to the new instance in the same
  // process (reload-handover.js), so the inbox stays open across the reload.
  function startWatch() {
    if (watch || dependencies.inventoryWatch === false || !observeFor) return;
    // Bindings carry the stopped watch's cancellation; a new watch binds anew.
    observeBindings.clear();
    const timing = dependencies.inventoryWatchTiming ?? {};
    watch = createInventoryWatch({headings, observe: observeFor, isOlder: olderRuntime,
      onChange: dependencies.onInventoryChange ?? reloadInstance,
      onRead: (error) => { if (error) noteFailure(error); else state.markUp(); },
      onError: (error) => log(client, 'warn', 'OpenAbstractions could not act on a model list change', {reason: failureCode(error)}),
      onOlder: () => log(client, 'info', 'This OpenAbstractions runtime cannot report model list changes; OpenCode reloads its model list at its next start'),
      ...timing});
    dependencies.onInventoryWatch?.(watch);
    watch.start();
  }

  async function toast(message) {
    try {
      await client?.tui?.showToast?.({body: {title: 'OpenAbstractions', message, variant: 'info'}});
    } catch {
      // A toast must not change the reload's outcome.
    }
  }

  // Answers false when it waited (a busy session or an open inbox), so the
  // watch asks again at its next answer.
  function projectQuery() {
    return typeof directory === 'string' && directory ? {query: {directory}} : {};
  }

  async function allIdle(strict = false) {
    try {
      const data = (await client.session.status(projectQuery()))?.data;
      if (strict) return data && typeof data === 'object' && !Array.isArray(data)
        && Object.values(data).every(session => session?.type === 'idle');
      return !Object.values(data ?? {}).some(session => session?.type && session.type !== 'idle');
    } catch {
      return false;
    }
  }

  async function reloadInstance({id, state: answer}, startupSnapshot = null) {
    // A runtime that went away keeps the models OpenCode lists; the
    // availability toast says so. Once it answers, a changed list reloads.
    if (answer !== 'answered' || startupClosed || reloadRequested) return true;
    if (typeof client?.instance?.dispose !== 'function' || typeof client?.session?.status !== 'function') {
      watch?.stop();
      await log(client, 'info', 'OpenAbstractions models changed; this OpenCode reloads its model list at its next start', {provider: id});
      return true;
    }
    // Re-creating the instance would end a reply in progress: wait for idle.
    if (!await allIdle(Boolean(startupSnapshot))) return false;
    // A second status read right before dispose narrows the window in which
    // a starting reply would be ended to one round trip; 1.18 has no atomic
    // "dispose when idle".
    if (!await allIdle(Boolean(startupSnapshot))) return false;
    if (startupClosed || reloadRequested
        || startupSnapshot && (startupCandidate !== startupSnapshot || now() >= startupSnapshot.expires)) return false;
    reloadRequested = true;
    watch?.stop();
    // The next instance adopts the open inbox and presence; this one's
    // dispose leaves them open (reload-handover.js).
    if (startupSnapshot) inboxParts.startupSnapshot = startupSnapshot;
    // The re-created instance asks OA again rather than reuse the start's answers.
    if (!startupSnapshot) forgetStartupReads();
    handOver(directory, inboxParts, {schedule: dependencies.handoverSchedule, unschedule: dependencies.handoverUnschedule});
    // Only this project is re-created: a global dispose would also end a
    // reply streaming in another project, whose sessions allIdle never sees.
    try {
      await client.instance.dispose(projectQuery());
    } catch (error) {
      withdrawHandover(directory, inboxParts);
      delete inboxParts.startupSnapshot;
      reloadRequested = false;
      await log(client, 'warn', 'OpenAbstractions could not reload OpenCode\'s model list; it tries again at the next change it sees', {
        provider: id, reason: failureCode(error),
      });
      watch = null;
      startWatch();
      return true;
    }
    await log(client, 'info', inventoryChangedMessage, {provider: id});
    await toast(inventoryChangedMessage);
  }
}

export default OpenAbstractionsPlugin;
