// Headings share one short probe per configured runtime identity. A missing
// runtime costs about startupProbeMs, and its headings ask nothing further.
import assert from 'node:assert/strict';
import test from 'node:test';
import {setTimeout as delay} from 'node:timers/promises';
import OpenAbstractionsPlugin from '../plugin.js';
import {Availability} from '../availability.js';
import {tool} from '@opencode-ai/plugin/tool';
import {defaultHelperTools} from '../tool-config.js';

const configured = () => ({provider: {openabstractions: {npm: '@openabstractions/opencode',
  options: {oaSelectionPolicies: {pick: 'saved-policy'}}, models: {kept: {name: 'kept'}}}}});
const missing = () => Promise.reject(Object.assign(new Error('no runtime'), {name: 'ResolutionError', status: 'runtime_unavailable'}));

async function start(probeRuntime) {
  const asked = [];
  const toasts = [];
  const plugin = await OpenAbstractionsPlugin({client: {tui: {showToast: async ({body}) => toasts.push(body)}}}, {}, {
    tool, env: {}, availability: new Availability(), probeRuntime,
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
  assert.deepEqual(Object.keys(plugin.tool).sort(), [...defaultHelperTools,
    'oa_logs_read_failure', 'oa_image_generate', 'oa_speech_speak', 'oa_video_generate'].sort(),
    'canonical Messages and guarded capability tools remain registered');
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

test('headings share their own bounded probe without suppressing a healthy different runtime', async () => {
  const probes = [];
  const reads = [];
  const outcomes = new Map();
  const provider = (runtimeEndpoint) => ({npm: '@openabstractions/opencode', options: {runtimeEndpoint}, models: {}});
  const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {tool, env: {},
    availability: new Availability(), startupProbeMs: 30,
    probeRuntime: async (options) => {
      probes.push(options.runtimeEndpoint);
      if (options.runtimeEndpoint === 'stopped') return new Promise(() => {});
    },
    onInventoryRefresh: (id, error) => outcomes.set(id, error),
    readInventory: async (options) => { reads.push(options.runtimeEndpoint); return {offers: []}; },
  });
  const began = performance.now();
  await plugin.config({provider: {openabstractions: provider('stopped'),
    'openabstractions-same-runtime': provider('stopped'), 'openabstractions-hosted': provider('healthy')}});
  assert.ok(performance.now() - began < 1000);
  assert.deepEqual(probes, ['stopped', 'healthy']);
  assert.deepEqual(reads, ['healthy']);
  assert.equal(outcomes.get('openabstractions').status, 'runtime_unavailable');
  assert.equal(outcomes.get('openabstractions-same-runtime'), outcomes.get('openabstractions'));
  assert.equal(outcomes.get('openabstractions-hosted'), null);
});

test('synthetic startup timing separates catalogue waits, background warmup and the kept helper', {timeout: 5000}, async (t) => {
  const phases = [];
  const timed = (phase, reply) => async () => {
    const began = performance.now();
    await delay(12);
    phases.push([phase, Math.round(performance.now() - began)]);
    return reply;
  };
  const withoutPluginStart = performance.now();
  await Promise.resolve(configured());
  const withoutPluginMs = Math.round(performance.now() - withoutPluginStart);
  const directory = 'fixture-project';
  const session = 'ses_fixture';
  const offer = {family: 'fixture', names: ['fixture'], servers: ['local'], context: 32768n,
    contextSource: 'effective', input: 28672n, unknown: []};
  const policy = {outcome: 'found', revision: 'fixture', policy: {
    name: 'oa-chat-default', candidates: [{model: 'fixture', placement: 'local', credential: ''}],
  }};
  let warmStarted = false;
  let finishWarm;
  const warm = new Promise(resolve => { finishWarm = resolve; });
  let closed = 0;
  const client = {
    app: {log: async ({body}) => {
      if (body.message === 'OpenCode uses the chat model chosen for it in OpenAbstractions') {
        await timed('model-selection-log')();
      }
    }},
    session: {get: timed('session-readback', {data: {id: session, directory}})},
  };
  const began = performance.now();
  const plugin = await OpenAbstractionsPlugin({client, directory}, {compactTools: true}, {
    tool, env: {}, availability: new Availability(), connectionRequired: true,
    requestOpenCodeConnection: timed('connect', {outcome: 'connect_pending'}),
    checkOpenCodeConnection: timed('connect-read', {
      outcome: 'connected', hostRegistered: true, helperEnrolled: true, helperProgram: 'fixture-helper',
    }),
    probeRuntime: timed('router-probe', true),
    readCapability: timed('capability', false),
    readInventory: async (_options, placement) => timed('inventory', {offers: placement === 'local' ? [offer] : []})(),
    readSelectionPolicy: async (_options, name) => timed('selection-policy', name === 'oa-chat-default' ? policy : {outcome: 'unknown'})(),
    preloadKeptPrefix: () => { warmStarted = true; return warm; },
    resolveApplications: async () => ({announce: async () => ({outcome: 'applied', instance: 'fixture'}),
      withdraw: async () => ({outcome: 'applied'})}),
    openMessagesHelper: ({id}) => ({
      initialize: timed('helper-initialize'),
      isClosed: () => false,
      call: async name => {
        assert.equal(name, 'oa_opencode_session_start');
        return timed('helper-session-start', {structuredContent: {outcome: 'observed', session_id: id, context: 'fixture'}})();
      },
      close: async () => { closed += 1; },
    }),
  });
  try {
    const config = {...configured(), agent: {local: {}}};
    await plugin.config(config);
    const withPluginMs = Math.round(performance.now() - began);
    assert.equal(config.model, 'openabstractions/fixture');
    assert.equal(warmStarted, true, 'config returns even while preload remains unsettled');
    // The first-use Connect runs beside the model list and never gates it.
    const connectPhases = new Set(['connect', 'connect-read']);
    const catalogue = () => phases.map(([phase]) => phase).filter(phase => !connectPhases.has(phase));
    const startupPhases = ['router-probe', 'capability', 'capability', 'capability',
      'inventory', 'inventory', 'selection-policy', 'selection-policy', 'model-selection-log'];
    assert.deepEqual(catalogue(), startupPhases);
    const models = await plugin.provider.models({id: 'openabstractions', models: config.provider.openabstractions.models});
    assert.equal(models['oa-chat']?.name, 'Default (fixture)');
    assert.ok(models.fixture);
    assert.deepEqual(catalogue(), startupPhases, 'first list reuses its own completed survey');
    const eventStart = performance.now();
    await plugin.event({event: {type: 'session.created', properties: {info: {id: session, directory}}}});
    const sessionEventMs = Math.round(performance.now() - eventStart);
    assert.deepEqual(catalogue(), [...startupPhases,
      'session-readback', 'helper-initialize', 'helper-session-start']);
    assert.deepEqual(phases.map(([phase]) => phase).filter(phase => connectPhases.has(phase)), ['connect', 'connect-read']);
    t.diagnostic(JSON.stringify({synthetic: true, withoutPluginMs, withPluginMs, sessionEventMs, phases}));
  } finally {
    finishWarm(null);
    await plugin.dispose();
  }
  assert.equal(closed, 1);
});

const connected = {outcome: 'connected', hostRegistered: true, helperEnrolled: true};
const projectContext = (id) => ({
  OA_OPENCODE_SESSION_ENDPOINT: 'fixture-session',
  OA_OPENCODE_SESSION_PROGRAM: 'C:/fixture/session.exe',
  OA_OPENCODE_SESSION_ACCOUNT: 'fixture-account',
  OA_OPENCODE_SESSION_ID: id,
  OA_OPENCODE_RUNTIME_PROGRAM: 'C:/fixture/runtime.exe',
  OA_OPENCODE_RUNTIME_ACCOUNT: 'fixture-account',
});

async function until(predicate) {
  for (let i = 0; i < 100 && !predicate(); i++) await delay(5);
  assert.ok(predicate(), 'fixture did not reach the expected startup boundary');
}

async function ownedStartup(id, counters, {request, check, status = async () => ({data: {}}), actor = id,
  resolveApplications = async () => null, now, preload} = {}) {
  const offer = {family: 'fixture', names: ['fixture'], servers: ['local'], context: 32768n,
    contextSource: 'effective', input: 28672n, unknown: []};
  const client = {session: {status: async () => { counters.status += 1; return status(); }},
    instance: {dispose: async () => { counters.reload += 1; }}};
  return OpenAbstractionsPlugin({client, directory: id}, {compactTools: Boolean(preload)}, {
    tool, env: projectContext(actor), availability: new Availability(), connectionRequired: true, firstAnswerMs: 0,
    requestOpenCodeConnection: async () => { counters.connect += 1; return request ? request() : connected; },
    checkOpenCodeConnection: async () => { counters.check += 1; return check ? check() : connected; },
    probeRuntime: async () => true, readCapability: async () => false,
    readInventory: async (_options, placement) => {
      counters.inventory += 1;
      return {offers: placement === 'local' ? [offer] : []};
    },
    readSelectionPolicy: async () => ({outcome: 'unknown'}),
    resolveApplications,
    now, preloadKeptPrefix: preload,
    openMessagesHelper: () => { throw new Error('startup must not open a session helper'); },
  });
}

// What the config hook writes before OA answers: OA's own chat entry, which
// a config naming no model selects (plugin.js placeOwnChat).
function withOwnChat(config) {
  const expected = structuredClone(config);
  const provider = expected.provider.openabstractions;
  const entry = {name: 'Default', limit: {context: 0, output: 4096}, tool_call: true};
  provider.models['oa-chat'] = entry;
  provider.options = {...provider.options, oaSelectionPolicies: {'oa-chat': 'oa-chat-default', ...provider.options.oaSelectionPolicies},
    oaSelectionPolicyManagedModels: {'oa-chat': structuredClone(entry)}};
  if (expected.model === undefined) expected.model = 'openabstractions/oa-chat';
  return expected;
}

const counts = () => ({connect: 0, check: 0, inventory: 0, status: 0, reload: 0});
const selectedConfig = () => ({...configured(), model: 'another-provider/explicit'});
const idle = {event: {type: 'session.status', properties: {sessionID: 'ses_fixture', status: {type: 'idle'}}}};

test('cold startup returns the configured model before Connect and hands off once after two idle checks', {timeout: 5000}, async (t) => {
  const metrics = counts();
  let complete;
  const connect = new Promise(resolve => { complete = resolve; });
  const replies = ['idle', 'busy', null];
  const plugin = await ownedStartup('reload-fixture', metrics, {
    request: () => connect, status: async () => {
      const type = replies.shift();
      return type === null ? {} : {data: {session: {type: type ?? 'idle'}}};
    },
  });
  const cold = selectedConfig();
  let warm;
  try {
    const began = performance.now();
    await plugin.config(cold);
    const coldConfigMs = Math.round(performance.now() - began);
    assert.deepEqual(cold, withOwnChat(selectedConfig()), 'pending native work adds only the own chat entry');
    assert.equal(metrics.connect, 1);
    assert.equal(metrics.inventory, 0);
    const first = await plugin.provider.models({id: 'openabstractions', models: cold.provider.openabstractions.models});
    assert.deepEqual(Object.keys(first), ['kept', 'oa-chat']);
    assert.equal(metrics.check, 0, 'first listing does not open a second child');
    complete(connected);
    await until(() => metrics.status === 2);
    assert.equal(metrics.reload, 0, 'a session became busy at the second check');
    await plugin.event(idle);
    await until(() => metrics.status === 3);
    await delay(0);
    assert.equal(metrics.reload, 0, 'an ambiguous session status cannot authorize a reload');
    await Promise.all([plugin.event(idle), plugin.event(idle)]);
    await until(() => metrics.reload === 1);
    assert.deepEqual(cold, withOwnChat(selectedConfig()), 'background never mutates the config already read by the host');
    await plugin.dispose();
    warm = await ownedStartup('reload-fixture', metrics);
    const reopened = selectedConfig();
    await warm.config(reopened);
    assert.equal(reopened.model, cold.model);
    assert.ok(reopened.provider.openabstractions.models.fixture);
    await until(() => metrics.check === 1);
    assert.equal(metrics.connect, 1, 'warm instance proves readiness read-only, without another Connect');
    assert.equal(metrics.inventory, 2, 'warm handover does not repeat the placement survey');
    assert.equal(metrics.reload, 1);
    t.diagnostic(JSON.stringify({synthetic: true, coldConfigMs, connectPendingAtReturn: true,
      connectCalls: metrics.connect, inventoryCalls: metrics.inventory, reloads: metrics.reload}));
  } finally {
    complete(connected);
    await (warm ?? plugin).dispose();
  }
});

async function checkChangedStartupScope(changedScope) {
  const metrics = counts();
  let announced = 0;
  let oldWithdrawals = 0;
  let wrongWithdrawals = 0;
  const directory = 'handover-fixture-' + changedScope;
  const configWithEndpoint = (runtimeEndpoint) => {
    const config = selectedConfig();
    config.provider.openabstractions.options.runtimeEndpoint = runtimeEndpoint;
    return config;
  };
  const plugin = await ownedStartup(directory, metrics, {
    resolveApplications: async () => ({
      announce: async () => { announced += 1; return {outcome: 'applied', instance: 'fixture'}; },
      withdraw: async () => { oldWithdrawals += 1; },
    }),
  });
  let other;
  try {
    await plugin.event({event: {type: 'session.created', properties: {info: {id: 'ses_fixture', directory}}}});
    await until(() => announced === 1);
    await plugin.config(configWithEndpoint('old-fixture-runtime'));
    await until(() => metrics.reload === 1);
    await plugin.dispose();
    other = await ownedStartup(directory, metrics, {
      actor: changedScope === 'native session' ? 'different-native-session' : directory,
      request: async () => ({outcome: 'not_connected'}),
      check: async () => ({outcome: 'not_connected'}),
      resolveApplications: async () => ({withdraw: async () => { wrongWithdrawals += 1; }}),
    });
    assert.equal(oldWithdrawals, 0, 'presence remains held without rebind before config scope is known');
    const endpoint = changedScope === 'native session' ? 'old-fixture-runtime' : 'other-fixture-runtime';
    const config = configWithEndpoint(endpoint);
    await other.config(config);
    assert.deepEqual(config, withOwnChat(configWithEndpoint(endpoint)));
    await until(() => metrics.check === 1);
    await until(() => oldWithdrawals === 1);
    assert.equal(wrongWithdrawals, 0);
    assert.equal(metrics.connect, 2);
    // The changed scope surveys afresh; its pending Connect gates no model list.
    await until(() => metrics.inventory === 4);
    await until(() => metrics.reload === 2);
  } finally {
    await (other ?? plugin).dispose();
  }
}

for (const changedScope of ['native session', 'configured runtime endpoint']) {
  test(`the same directory cannot adopt startup presence across a changed ${changedScope}`, {timeout: 5000},
    () => checkChangedStartupScope(changedScope));
}

// R14: an OA update left the helper's Connect pending, and the plugin kept
// the placeholder for good. OpenCode's model list never waits on Connect.
test('a pending Connect still lists OA models and the Default row after the startup reload', {timeout: 5000}, async () => {
  const metrics = counts();
  const plugin = await ownedStartup('pending-connect-fixture', metrics, {
    request: async () => ({outcome: 'connect_pending'}), check: async () => ({outcome: 'connect_pending'}),
    status: async () => ({data: {}}),
  });
  let warm;
  try {
    const cold = {...configured()};
    await plugin.config(cold);
    assert.equal(cold.provider.openabstractions.models['oa-chat'].name, 'Default');
    await until(() => metrics.reload === 1);
    assert.equal(metrics.inventory, 2);
    await plugin.dispose();
    warm = await ownedStartup('pending-connect-fixture', metrics, {
      request: async () => ({outcome: 'connect_pending'}), check: async () => ({outcome: 'connect_pending'}),
    });
    const reopened = {...configured()};
    await warm.config(reopened);
    const models = reopened.provider.openabstractions.models;
    assert.equal(models.fixture?.limit?.context, 32768);
    assert.equal(models['oa-chat'], undefined, 'no chat policy is stored, so no Default row');
  } finally {
    await (warm ?? plugin).dispose();
  }
});

test('an expired busy startup projection resamples once on a later verified idle event', {timeout: 5000}, async () => {
  const metrics = counts();
  let clock = 10;
  let idleNow = false;
  const plugin = await ownedStartup('expiry-fixture', metrics, {
    now: () => clock, status: async () => ({data: {session: {type: idleNow ? 'idle' : 'busy'}}}),
  });
  let warm;
  try {
    await plugin.config(selectedConfig());
    await until(() => metrics.status === 1);
    clock += 31000;
    await plugin.event(idle);
    await until(() => metrics.status === 2);
    assert.equal(metrics.inventory, 2, 'an idle event cannot consume the resurvey while another session is busy');
    idleNow = true;
    await Promise.all([plugin.event(idle), plugin.event(idle)]);
    await until(() => metrics.reload === 1);
    assert.equal(metrics.inventory, 4);
    assert.equal(metrics.connect, 1);
    await plugin.dispose();
    warm = await ownedStartup('expiry-fixture', metrics, {now: () => clock});
    const config = selectedConfig();
    await warm.config(config);
    assert.ok(config.provider.openabstractions.models.fixture);
    await until(() => metrics.check === 1);
    assert.equal(metrics.inventory, 4);
  } finally {
    await (warm ?? plugin).dispose();
  }
});

test('a hidden startup survey leaves Local preload to the instance that adopts it', {timeout: 5000}, async () => {
  const metrics = counts();
  let preloads = 0;
  const preload = async () => { preloads += 1; };
  const config = () => ({...configured(), model: 'openabstractions/fixture', agent: {local: {}}});
  const plugin = await ownedStartup('preload-fixture', metrics, {preload});
  let warm;
  try {
    await plugin.config(config());
    await until(() => metrics.reload === 1);
    assert.equal(preloads, 0);
    await plugin.dispose();
    warm = await ownedStartup('preload-fixture', metrics, {preload});
    await warm.config(config());
    await until(() => preloads === 1);
    assert.equal(metrics.inventory, 2);
    assert.equal(metrics.reload, 1);
  } finally {
    await (warm ?? plugin).dispose();
  }
});

test('three projects retain separate Connect decisions and each lists its models', {timeout: 5000}, async (t) => {
  const projects = [];
  try {
    for (let i = 0; i < 3; i++) {
      const metrics = counts();
      const plugin = await ownedStartup('separate-fixture-' + i, metrics, {
        request: async () => i === 2 ? {outcome: 'not_connected'} : connected,
        check: async () => ({outcome: 'not_connected'}),
        status: async () => ({data: {session: {type: 'busy'}}}),
      });
      projects.push({plugin, metrics});
    }
    const began = performance.now();
    await Promise.all(projects.map(({plugin}) => plugin.config(selectedConfig())));
    const configMs = Math.round(performance.now() - began);
    await until(() => projects[0].metrics.status === 1 && projects[1].metrics.status === 1
      && projects[2].metrics.check === 1);
    assert.deepEqual(projects.map(({metrics}) => metrics.connect), [1, 1, 1]);
    // A project whose Connect is refused still lists OA's models: the
    // runtime answers OpenCode's own read, and Connect guards the tools.
    await until(() => projects[2].metrics.inventory === 2);
    assert.deepEqual(projects.map(({metrics}) => metrics.inventory), [2, 2, 2]);
    assert.deepEqual(projects.map(({metrics}) => metrics.reload), [0, 0, 0]);
    t.diagnostic(JSON.stringify({synthetic: true, projects: 3, configMs,
      connectCalls: projects.map(({metrics}) => metrics.connect),
      inventoryCalls: projects.map(({metrics}) => metrics.inventory)}));
  } finally {
    await Promise.all(projects.map(({plugin}) => plugin.dispose()));
  }
});
