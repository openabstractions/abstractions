// OpenCode runs each instance's config hook before that instance's init, and
// OpenCode Desktop starts several project instances at once. The plugin's
// hooks return without waiting on OA; the instances of one process share one
// OA start; the last answer lists the models at once on the next start.
import assert from 'node:assert/strict';
import test from 'node:test';
import {mkdtemp, readFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import {tool} from '@opencode-ai/plugin/tool';
import OpenAbstractionsPlugin from '../plugin.js';
import {Availability} from '../availability.js';
import {createStartupMemory, shared, sharedConnection} from '../startup-share.js';

const configured = () => ({provider: {openabstractions: {npm: '@openabstractions/opencode',
  options: {oaSelectionPolicies: {pick: 'saved-policy'}}, models: {kept: {name: 'kept'}}}}});
const connected = {outcome: 'connected', hostRegistered: true, helperEnrolled: true};
const offer = {family: 'fixture', names: ['fixture'], servers: ['local'], context: 32768n,
  contextSource: 'effective', input: 28672n, unknown: []};
const chatPolicy = {outcome: 'found', revision: 'fixture', policy: {
  name: 'oa-chat-default', candidates: [{model: 'fixture', placement: 'local', credential: ''}],
}};

// A slow call whose timer never holds the test process open.
const slowly = (ms, value) => new Promise(resolve => setTimeout(() => resolve(value), ms).unref());

async function until(predicate, ms = 3000) {
  const end = Date.now() + ms;
  while (!predicate() && Date.now() < end) await delay(5);
  assert.ok(predicate(), 'the fixture did not reach the expected point');
}

/** A fake OA whose every call waits ms and is counted. */
function fakeOA(ms = 0, ready = Promise.resolve()) {
  const counts = {connect: 0, check: 0, probe: 0, inventory: 0, capability: 0, policy: 0};
  const counted = (name, reply) => async (...args) => {
    counts[name] += 1;
    await ready;
    return slowly(ms, typeof reply === 'function' ? reply(...args) : reply);
  };
  // The native Connect child ends when its signal aborts, as the Panel's does.
  const native = name => async ({signal}) => {
    counts[name] += 1;
    return Promise.race([ready.then(() => slowly(ms, connected)), new Promise((_resolve, reject) => {
      if (signal?.aborted) reject(new Error('aborted'));
      signal?.addEventListener('abort', () => reject(new Error('aborted')), {once: true});
    })]);
  };
  return {counts, dependencies: {
    connectionRequired: true,
    requestOpenCodeConnection: native('connect'),
    checkOpenCodeConnection: native('check'),
    probeRuntime: counted('probe', true),
    readInventory: counted('inventory', (_options, placement) => ({offers: placement === 'local' ? [offer] : []})),
    readCapability: counted('capability', false),
    readSelectionPolicy: counted('policy', (_options, name) => name === 'oa-chat-default' ? chatPolicy : {outcome: 'unknown'}),
  }};
}

/** OpenCode 1.18's client as the plugin sees it: every session idle, and an
 * instance it can re-create. */
function host() {
  const state = {disposals: 0};
  state.client = {session: {status: async () => ({data: {}})},
    instance: {dispose: async () => { state.disposals += 1; }}};
  return state;
}

function start(directory, oa, client, more = {}) {
  return OpenAbstractionsPlugin({client, directory}, {}, {tool, env: {}, availability: new Availability(),
    startDefaults: new Map(), startupMemory: null, ...oa.dependencies, ...more});
}

test('with no first-answer wait, plugin init and its config hook return at once while every OA call takes five seconds', {timeout: 5000}, async () => {
  const oa = fakeOA(5000);
  const opencode = host();
  const began = performance.now();
  const plugin = await start('slow-project', oa, opencode.client);
  const config = configured();
  await plugin.config(config);
  const elapsed = performance.now() - began;
  try {
    assert.ok(elapsed < 100, `init and config took ${Math.round(elapsed)} ms`);
    // A first start with no saved answer: OA's own chat is listed and chosen,
    // so no request goes to another provider before OA answers.
    assert.equal(config.model, 'openabstractions/oa-chat');
    const heading = config.provider.openabstractions;
    assert.equal(heading.models['oa-chat']?.name, 'Default', 'one Default entry before OA ever answered');
    assert.ok(heading.models.kept, 'the person\'s own entry stays');
    assert.equal(heading.options.oaSelectionPolicies['oa-chat'], 'oa-chat-default',
      'oa-chat is routed by the app\'s own chat policy, which OA resolves at the request');
    assert.equal(heading.options.oaSelectionPolicies.pick, 'saved-policy');
    const listed = await plugin.provider.models({id: 'openabstractions', models: heading.models});
    assert.deepEqual(Object.keys(listed).sort(), ['kept', 'oa-chat'], 'the first listing does not wait on OA');
    assert.equal(oa.counts.connect, 1, 'the native connection is asked in the background');
  } finally {
    await plugin.dispose();
  }
});

test('a model the person chose stays chosen before OA answers', {timeout: 5000}, async () => {
  const opencode = host();
  for (const model of ['deepseek/deepseek-chat', 'openabstractions/kept']) {
    const plugin = await start('chosen-project', fakeOA(5000), opencode.client);
    const config = {...configured(), model};
    try {
      await plugin.config(config);
      assert.equal(config.model, model);
      assert.ok(config.provider.openabstractions.models['oa-chat'], 'oa-chat is still listed to choose');
    } finally {
      await plugin.dispose();
    }
  }
});

test('with no chat chosen in OA the start re-creates the instance on OA\'s default model', {timeout: 5000}, async () => {
  const oa = fakeOA(20);
  oa.dependencies.readSelectionPolicy = async () => ({outcome: 'unknown'});
  const opencode = host();
  const cold = await start('default-project', oa, opencode.client);
  await cold.config(configured());
  await until(() => opencode.disposals === 1);
  await cold.event({event: {type: 'server.instance.disposed', properties: {directory: 'default-project'}}});
  await cold.dispose();
  const warm = await start('default-project', oa, opencode.client);
  try {
    const reopened = configured();
    await warm.config(reopened);
    assert.equal(reopened.model, 'openabstractions/fixture', 'OA\'s default family replaces the placeholder choice');
    assert.ok(!Object.hasOwn(reopened.provider.openabstractions.models, 'oa-chat'), 'an unlisted oa-chat is withdrawn');
    assert.ok(reopened.provider.openabstractions.models.fixture);
  } finally {
    await warm.dispose();
  }
});

test('with no chat chosen in OA a remembered family OA withdrew gives way to a current model', {timeout: 5000}, async () => {
  const memory = createStartupMemory();
  const family = (name) => ({...offer, family: name, names: [name]});
  // OA with no chat chosen and one family; the next start finds it withdrawn.
  const oaWith = (name) => {
    const oa = fakeOA(0);
    oa.dependencies.readSelectionPolicy = async () => ({outcome: 'unknown'});
    oa.dependencies.readInventory = async (_options, placement) => ({offers: placement === 'local' ? [family(name)] : []});
    return oa;
  };
  const remembered = [];
  const keeping = {recall: memory.recall, remember: async (answer) => { remembered.push(answer); await memory.remember(answer); }};
  const first = host();
  const earlier = await start('withdrawn-project', oaWith('gone'), first.client, {startupMemory: keeping});
  await earlier.config(configured());
  await until(() => first.disposals === 1 && remembered.length === 1);
  await earlier.dispose();

  // The next process: a new start whose OA offers another family only. This
  // test process still holds the first start's reload hand-over for its
  // directory, so the next process opens the same config from another one.
  const later = host();
  const oa = oaWith('fresh');
  const cold = await start('withdrawn-next', oa, later.client, {startupMemory: keeping});
  const config = configured();
  await cold.config(config);
  assert.equal(config.model, 'openabstractions/gone', 'the remembered default lists at once');
  await until(() => later.disposals === 1);
  await cold.event({event: {type: 'server.instance.disposed', properties: {directory: 'withdrawn-next'}}});
  await cold.dispose();
  const warm = await start('withdrawn-next', oa, later.client, {startupMemory: keeping});
  try {
    const reopened = configured();
    await warm.config(reopened);
    assert.equal(reopened.model, 'openabstractions/fresh', 'the survey chooses from the person\'s own model, not the cache');
    assert.ok(reopened.provider.openabstractions.models.fresh);
    assert.ok(!Object.hasOwn(reopened.provider.openabstractions.models, 'gone'));
  } finally {
    await warm.dispose();
  }
});

test('the instances of one process share one OA start', {timeout: 5000}, async () => {
  // One instance alone asks OA this much.
  const solo = fakeOA(20);
  const alone = host();
  const one = await start('solo-project', solo, alone.client);
  await one.config(configured());
  await until(() => alone.disposals === 1);
  await one.dispose();

  const {promise: ready, resolve: answerOA} = Promise.withResolvers();
  const oa = fakeOA(20, ready);
  const opencode = host();
  const plugins = await Promise.all(['home', 'Default Project', 'AI abstractions', 'Motivational']
    .map(directory => start(directory, oa, opencode.client)));
  try {
    const configs = plugins.map(() => configured());
    // OA stays unanswered until every instance has applied its first config.
    await Promise.all(plugins.map((plugin, index) => plugin.config(configs[index])));
    for (const config of configs) assert.equal(config.model, 'openabstractions/oa-chat');
    answerOA();
    await until(() => opencode.disposals === plugins.length);
    assert.deepEqual(oa.counts, solo.counts, 'four instances ask OA what one instance asks');
    assert.equal(oa.counts.connect, 1);
    assert.equal(oa.counts.probe, 1);
  } finally {
    answerOA();
    await Promise.all(plugins.map(plugin => plugin.dispose()));
  }
});

test('the re-created instance lists OA\'s models and chat model at once', {timeout: 5000}, async () => {
  const oa = fakeOA(20);
  const opencode = host();
  const cold = await start('reload-project', oa, opencode.client);
  const config = configured();
  await cold.config(config);
  assert.equal(config.model, 'openabstractions/oa-chat');
  assert.equal(config.provider.openabstractions.models.fixture, undefined, 'OA\'s models arrive with the reload');
  await until(() => opencode.disposals === 1);
  // OpenCode announces the disposal and starts the instance again.
  await cold.event({event: {type: 'server.instance.disposed', properties: {directory: 'reload-project'}}});
  await cold.dispose();
  const asked = {...oa.counts};
  const warm = await start('reload-project', oa, opencode.client);
  try {
    const reopened = configured();
    await warm.config(reopened);
    assert.equal(reopened.model, 'openabstractions/fixture', 'OA\'s chosen real model is preselected');
    assert.ok(reopened.provider.openabstractions.models.fixture, 'OA\'s model is listed at the start');
    assert.equal(reopened.provider.openabstractions.models['oa-chat']?.name, 'Default (fixture)',
      'the Default row names the model the Panel\'s default points to');
    await delay(50);
    assert.equal(oa.counts.inventory, asked.inventory, 'the re-created instance does not survey again');
  } finally {
    await warm.dispose();
  }
});

test('the next OpenCode process lists the last answer at once and keeps it when OA agrees', {timeout: 5000}, async () => {
  const dir = await mkdtemp(join(tmpdir(), 'oa-opencode-startup-'));
  try {
    const first = fakeOA(0);
    const firstHost = host();
    const earlier = await start('project', first, firstHost.client, {startupMemory: createStartupMemory({dir})});
    await earlier.config(configured());
    await until(() => firstHost.disposals === 1);
    await earlier.dispose();
    // The answer is written beside the start, so the test reads until it lands.
    let stored = null;
    for (let i = 0; i < 100 && !stored; i++) {
      await delay(10);
      stored = await readFile(join(dir, 'startup.json'), 'utf8').then(JSON.parse, () => null);
    }
    assert.equal(stored?.answers.length, 1);

    // A new process: a new memory over the same folder, and an OA that takes
    // five seconds for everything.
    const later = fakeOA(5000);
    const laterHost = host();
    const began = performance.now();
    const plugin = await start('project', later, laterHost.client, {startupMemory: createStartupMemory({dir})});
    const config = configured();
    await plugin.config(config);
    try {
      assert.ok(performance.now() - began < 100);
      assert.equal(config.model, 'openabstractions/fixture', 'OA\'s chosen real model is preselected at once');
      assert.ok(config.provider.openabstractions.models.fixture, 'OA\'s models are listed at once');
      assert.equal(config.provider.openabstractions.models['oa-chat']?.name, 'Default (fixture)');
      assert.equal(config.provider.openabstractions.models.kept.name, 'kept', 'the person\'s own entry stays');
    } finally {
      await plugin.dispose();
    }

    // When OA answers as before, the instance is not re-created.
    const same = fakeOA(0);
    const sameHost = host();
    const again = await start('project', same, sameHost.client, {startupMemory: createStartupMemory({dir})});
    await again.config(configured());
    await until(() => same.counts.policy > 0 && same.counts.inventory === 2);
    await delay(50);
    assert.equal(sameHost.disposals, 0);
    await again.dispose();
  } finally {
    await rm(dir, {recursive: true, force: true});
  }
});

/** OpenCode Desktop 1.18.34: Config.get merges the cached global config into a
 * new object per instance with remeda mergeDeep, which keeps a key only the
 * global has by reference. Every instance's config.provider is the cached
 * global's provider map, and the picker lists what the instance's config
 * holds when its provider list is first read. */
function desktop(file) {
  const global = JSON.parse(JSON.stringify(file));
  const pristine = JSON.parse(JSON.stringify(file));
  return {global, pristine, config: () => ({...global})};
}
/** OpenCode Desktop's picker rule (its Models context `visible`): with no
 * choice of the person's, a model shows when its release date is not a valid
 * date or it is the latest of its family released in the last six months. */
function shown(entry, now = Date.now()) {
  const date = Date.parse(entry.release_date ?? '');
  if (!/^\d{4}-\d{2}-\d{2}/.test(entry.release_date ?? '') || Number.isNaN(date)) return true;
  return Math.abs(now - date) < 182 * 24 * 3600 * 1000;
}
const picker = (config) => Object.fromEntries(Object.entries(config.provider)
  .filter(([id]) => id.startsWith('openabstractions'))
  .map(([id, provider]) => [id, Object.fromEntries(Object.entries(provider.models ?? {})
    .filter(([, entry]) => shown(entry)).map(([model, entry]) => [model, entry.name]))]));

test('Desktop: OpenCode\'s first start never waits on OA, and the reload lists the real models and hosted group', {timeout: 5000}, async () => {
  const app = desktop({provider: {openabstractions: {npm: '@openabstractions/opencode', name: 'OpenAbstractions',
    options: {scope: 'local'}, models: {}}}});
  const oa = fakeOA(20);
  const deepseek = {...offer, family: 'deepseek-chat', names: ['deepseek-chat'], servers: ['deepseek'], title: 'DeepSeek Chat'};
  oa.dependencies.readInventory = async (_options, placement) =>
    ({offers: placement === 'local' ? [offer] : placement === 'hosted' ? [deepseek] : []});
  const opencode = host();
  const memory = createStartupMemory();
  const directory = 'Default Project (first read)';
  const cold = await start(directory, oa, opencode.client, {startupMemory: memory, env: {OPENCODE_CLIENT: 'desktop'}});
  const config = app.config();
  const began = performance.now();
  await cold.config(config);
  assert.ok(performance.now() - began < 100, 'Desktop\'s start does not wait for OA');
  assert.deepEqual(picker(config), {openabstractions: {'oa-chat': 'Default'}});
  await until(() => opencode.disposals === 1);
  await cold.event({event: {type: 'server.instance.disposed', properties: {directory}}});
  await cold.dispose();
  const warm = await start(directory, oa, opencode.client, {startupMemory: memory, env: {OPENCODE_CLIENT: 'desktop'}});
  try {
    const reopened = app.config();
    await warm.config(reopened);
    assert.deepEqual(picker(reopened), {openabstractions: {'oa-chat': 'Default (fixture)', fixture: 'fixture'},
      'openabstractions-hosted': {'deepseek-chat': 'DeepSeek Chat'}});
    assert.equal(reopened.provider['openabstractions-hosted'].name, 'DeepSeek · OpenAbstractions');
    assert.equal(reopened.provider.openabstractions.models['oa-chat'].limit.context, 32768);
    assert.deepEqual(app.global, app.pristine);
  } finally {
    await warm.dispose();
  }
});


test('no OpenCode client waits for a first answer', {timeout: 5000}, async () => {
  for (const env of [{}, {OPENCODE_CLIENT: 'cli'}, {OPENCODE_CLIENT: 'desktop'}]) {
    const opencode = host();
    const plugin = await start('no-wait-project', fakeOA(5000), opencode.client, {env});
    const config = configured();
    const began = performance.now();
    await plugin.config(config);
    try {
      assert.ok(performance.now() - began < 100, `${JSON.stringify(env)} waited`);
      assert.equal(config.provider.openabstractions.models['oa-chat']?.name, 'Default');
      assert.equal(config.model, 'openabstractions/oa-chat');
    } finally {
      await plugin.dispose();
    }
  }
});

test('a saved answer lists at once in Desktop with a slow OA', {timeout: 5000}, async () => {
  const saved = createStartupMemory();
  let remembered = 0;
  const memory = {recall: saved.recall, remember: async (answer) => { await saved.remember(answer); remembered += 1; }};
  const earlierHost = host();
  const earlier = await start('saved-project', fakeOA(0), earlierHost.client, {startupMemory: memory});
  await earlier.config(configured());
  await until(() => remembered > 0);
  await earlier.dispose();
  const opencode = host();
  const plugin = await start('saved-project-next', fakeOA(5000), opencode.client,
    {startupMemory: memory, env: {OPENCODE_CLIENT: 'desktop'}});
  const config = configured();
  const began = performance.now();
  await plugin.config(config);
  try {
    assert.ok(performance.now() - began < 100, 'the saved answer needs no wait');
    assert.equal(config.model, 'openabstractions/fixture');
    assert.ok(config.provider.openabstractions.models.fixture);
  } finally {
    await plugin.dispose();
  }
});

// R14: an OA update left the helper\'s Connect pending, and the start kept
// the placeholder for good. The model list goes on without Connect.
test('a first start whose Connect never resolves lists Default at once and OA\'s models after the reload', {timeout: 5000}, async () => {
  const oa = fakeOA(0);
  const pending = ({signal}) => new Promise((_resolve, reject) => {
    signal?.addEventListener('abort', () => reject(new Error('aborted')), {once: true});
  });
  oa.dependencies.requestOpenCodeConnection = pending;
  oa.dependencies.checkOpenCodeConnection = pending;
  const opencode = host();
  const memory = createStartupMemory();
  const plugin = await start('connect-project', oa, opencode.client, {env: {OPENCODE_CLIENT: 'desktop'}, startupMemory: memory});
  const config = configured();
  const began = performance.now();
  await plugin.config(config);
  assert.ok(performance.now() - began < 100, 'the start does not wait');
  assert.equal(config.provider.openabstractions.models['oa-chat']?.name, 'Default');
  assert.equal(config.model, 'openabstractions/oa-chat');
  await until(() => opencode.disposals === 1);
  assert.ok(oa.counts.inventory > 0, 'OA is asked for the model list while Connect is pending');
  await plugin.event({event: {type: 'server.instance.disposed', properties: {directory: 'connect-project'}}});
  await plugin.dispose();
  const warm = await start('connect-project', oa, opencode.client, {env: {OPENCODE_CLIENT: 'desktop'}, startupMemory: memory});
  try {
    const reopened = configured();
    await warm.config(reopened);
    assert.equal(reopened.provider.openabstractions.models.fixture?.name, 'fixture');
    assert.equal(reopened.provider.openabstractions.models['oa-chat']?.name, 'Default (fixture)');
    assert.equal(reopened.model, 'openabstractions/fixture');
  } finally {
    await warm.dispose();
  }
});

test('Desktop: when OA answers after the wait, re-created instances list OA\'s real models, and no hook writes the cached global config', {timeout: 5000}, async () => {
  const app = desktop({provider: {openabstractions: {npm: '@openabstractions/opencode', name: 'OpenAbstractions · this computer',
    options: {scope: 'local'}, models: {}}}});
  const oa = fakeOA(10);
  const memory = createStartupMemory();
  const opencode = host();
  const directories = ['C:\\Users\\late-answer', 'Default Project (late answer)'];
  const first = await Promise.all(directories.map(directory => start(directory, oa, opencode.client, {startupMemory: memory})));
  const configs = directories.map(() => app.config());
  await Promise.all(first.map((plugin, i) => plugin.config(configs[i])));
  assert.deepEqual(app.global, app.pristine, 'no hook writes the cached global config');
  for (const config of configs) {
    assert.deepEqual(picker(config), {openabstractions: {'oa-chat': 'Default'}}, 'before OA ever answered');
    assert.equal(config.model, 'openabstractions/oa-chat');
  }
  assert.deepEqual(app.global, app.pristine, 'the cached global config is untouched');
  await until(() => opencode.disposals === directories.length);
  for (const [i, plugin] of first.entries()) {
    await plugin.event({event: {type: 'server.instance.disposed', properties: {directory: directories[i]}}});
    await plugin.dispose();
  }
  // OpenCode re-creates each instance from the same cached global config.
  const second = await Promise.all(directories.map(directory => start(directory, oa, opencode.client, {startupMemory: memory})));
  try {
    const reopened = directories.map(() => app.config());
    await Promise.all(second.map((plugin, i) => plugin.config(reopened[i])));
    for (const config of reopened) {
      assert.deepEqual(picker(config), {openabstractions: {'oa-chat': 'Default (fixture)', fixture: 'fixture'}},
        'the Default row and the real models');
      assert.equal(config.model, 'openabstractions/fixture', 'OA\'s choice is preselected as the real model');
      assert.equal(config.provider.openabstractions.name, 'OpenAbstractions', 'an earlier version\'s heading name reads as today\'s');
    }
    assert.deepEqual(app.global, app.pristine);
    // A session Desktop kept on openabstractions/oa-chat (its own UI and
    // session state) still finds that model, routed to OA's choice.
    for (const config of reopened) {
      const kept = config.provider.openabstractions.models['oa-chat'];
      assert.equal(kept?.name, 'Default (fixture)', 'named after the model the Panel\'s default points to');
      assert.equal(kept?.id, 'Default (fixture)', 'the name OpenCode\'s prompt carries');
      assert.equal(config.provider.openabstractions.options.oaSelectionPolicies['oa-chat'], 'oa-chat-default');
    }
    await delay(50);
    assert.equal(opencode.disposals, directories.length, 'the re-created instances agree with OA and reload no more');
  } finally {
    await Promise.all(second.map(plugin => plugin.dispose()));
  }
});

test('OA\'s preselection follows a change of OA\'s choice, and a person\'s pick survives it', {timeout: 5000}, async () => {
  const choose = (model) => {
    const oa = fakeOA(0);
    oa.dependencies.readInventory = async (_options, placement) => ({offers: placement === 'local'
      ? [offer, {...offer, family: 'other', names: ['other']}] : []});
    oa.dependencies.readSelectionPolicy = async (_options, name) => name === 'oa-chat-default'
      ? {outcome: 'found', revision: model, policy: {name, candidates: [{model, placement: 'local', credential: ''}]}} : {outcome: 'unknown'};
    return oa;
  };
  for (const [picked, expected] of [[undefined, 'openabstractions/other'], ['openabstractions/fixture', 'openabstractions/fixture']]) {
    const memory = createStartupMemory();
    const name = picked ? 'picked' : 'follows';
    const config = () => ({...configured(), ...(picked ? {model: picked} : {})});
    // An earlier process: OA chose fixture.
    const earlierHost = host();
    const earlier = await start(`${name}-earlier`, choose('fixture'), earlierHost.client, {startupMemory: memory});
    await earlier.config(config());
    await until(() => earlierHost.disposals === 1);
    await earlier.dispose();
    // The next process: the remembered answer lists at once, then OA, which
    // now chooses other, answers.
    const opencode = host();
    const plugin = await start(name, choose('other'), opencode.client, {startupMemory: memory});
    const first = config();
    await plugin.config(first);
    assert.equal(first.model, picked ?? 'openabstractions/fixture', 'the last answer preselects at once');
    assert.equal(first.provider.openabstractions.models['oa-chat']?.name, 'Default (fixture)');
    // OA's new choice renames the kept oa-chat route, so the instance
    // reloads; a person's pick stays selected through it.
    await until(() => opencode.disposals === 1);
    await plugin.event({event: {type: 'server.instance.disposed', properties: {directory: name}}});
    await plugin.dispose();
    const warm = await start(name, choose('other'), opencode.client, {startupMemory: memory});
    const reopened = config();
    try {
      await warm.config(reopened);
      assert.equal(reopened.model, expected, 'the preselection follows OA\'s choice');
    } finally {
      await warm.dispose();
    }
  }
});

test('a remembered answer cannot write another package under OA headings', async () => {
  const memory = {remember: async () => {}, recall: async () => ({key: 'any', chat: null, capabilities: [], providers: [
    ['openabstractions', {npm: 'evil-package', models: {stolen: {}}}],
    ['other', {npm: 'evil-package', models: {}}],
  ]})};
  const opencode = host();
  const plugin = await start('guarded', fakeOA(5000), opencode.client, {startupMemory: memory});
  const config = configured();
  try {
    await plugin.config(config);
    assert.deepEqual(config, configured());
  } finally {
    await plugin.dispose();
  }
});

test('a shared call is reused within its window and never after a failure', async () => {
  const owner = {};
  let clock = 0, calls = 0;
  const call = () => { calls += 1; return calls; };
  const how = {reuseMs: 100, now: () => clock};
  assert.equal(await shared(owner, 'k', call, how), 1);
  assert.equal(await shared(owner, 'k', call, how), 1);
  clock = 150;
  assert.equal(await shared(owner, 'k', call, how), 2);
  const failing = {};
  await assert.rejects(shared(failing, 'k', () => { throw new Error('no'); }, how));
  assert.equal(await shared(failing, 'k', () => 'ok', how), 'ok');
});

test('a shared native connection aborts only when every waiting instance is gone', async () => {
  const owner = {};
  let seen;
  let finish;
  const run = signal => { seen = signal; return new Promise(resolve => { finish = resolve; }); };
  const a = new AbortController(), b = new AbortController();
  const first = sharedConnection(owner, 'connect', a.signal, run);
  const second = sharedConnection(owner, 'connect', b.signal, run);
  await delay(0);
  a.abort();
  assert.equal(seen.aborted, false, 'one instance leaving keeps the shared request');
  finish(connected);
  assert.deepEqual(await first, connected);
  assert.deepEqual(await second, connected);
  const c = new AbortController();
  sharedConnection(owner, 'read', c.signal, signal => { seen = signal; return new Promise(() => {}); }).catch(() => {});
  await delay(0);
  c.abort();
  assert.equal(seen.aborted, true);
});

// R14 to R15: every remembered answer was kept under an earlier form or an
// earlier config. The upgraded start lists the latest one under today\'s names
// at once, never the lone Default row.
test('an upgraded start lists the answer an earlier version kept, at once and with a Default row', {timeout: 5000}, async () => {
  const npm = '@openabstractions/opencode';
  const earlier = {key: 'earlier-version-key', chat: {choice: 'openabstractions/fixture', policy: 'oa-chat-default'}, capabilities: [],
    providers: [['openabstractions', {npm, name: 'OpenAbstractions · this computer',
      models: {fixture: {name: 'fixture · 32k', limit: {context: 32768, output: 4096}, tool_call: true}},
      options: {scope: 'local', oaInventoryManagedModels: {fixture: {name: 'fixture · 32k', limit: {context: 32768, output: 4096}, tool_call: true}}}}]]};
  const memory = createStartupMemory();
  await memory.remember(earlier);
  const opencode = host();
  const plugin = await start('upgraded-project', fakeOA(5000), opencode.client, {startupMemory: memory, env: {OPENCODE_CLIENT: 'desktop'}});
  const config = configured();
  const began = performance.now();
  await plugin.config(config);
  try {
    assert.ok(performance.now() - began < 100);
    const models = config.provider.openabstractions.models;
    assert.equal(models.fixture?.name, 'fixture');
    assert.equal(models['oa-chat']?.name, 'Default (fixture)');
    assert.equal(models['oa-chat']?.limit?.context, 32768);
    assert.equal(models.kept.name, 'kept', 'the config\'s own entry stays');
    assert.deepEqual(config.provider.openabstractions.options.oaSelectionPolicies, {'oa-chat': 'oa-chat-default', pick: 'saved-policy'},
      'the config keeps its own options');
    assert.equal(config.model, 'openabstractions/fixture');
  } finally {
    await plugin.dispose();
  }
  // An answer kept for another project session is not used.
  const other = createStartupMemory();
  await other.remember({...earlier, scope: 'another-scope'});
  const second = await start('other-scope-project', fakeOA(5000), host().client, {startupMemory: other});
  const fresh = configured();
  await second.config(fresh);
  try {
    assert.equal(fresh.provider.openabstractions.models['oa-chat']?.name, 'Default');
    assert.equal(fresh.provider.openabstractions.models.fixture, undefined);
  } finally {
    await second.dispose();
  }
});

test('a start with no answer at all reloads only its own project, never every OpenCode instance', {timeout: 5000}, async () => {
  // A global dispose would end a reply streaming in another project, whose
  // sessions this project's idle check never sees.
  const oa = fakeOA(0);
  const memory = createStartupMemory();
  const posts = [];
  const hosts = ['first-start-a', 'first-start-b'].map(() => {
    const state = host();
    state.client._client = {post: async ({url}) => { posts.push(url); return {data: true}; }};
    return state;
  });
  const plugins = await Promise.all(hosts.map((state, i) => start(`first-start-${i}`, oa, state.client, {startupMemory: memory})));
  try {
    await Promise.all(plugins.map(plugin => plugin.config(configured())));
    await until(() => hosts.every(state => state.disposals === 1));
    await delay(50);
    assert.deepEqual(posts, [], 'no global dispose');
  } finally {
    await Promise.all(plugins.map(plugin => plugin.dispose()));
  }
});
