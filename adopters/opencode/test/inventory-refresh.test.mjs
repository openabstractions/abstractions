import {nativeChatFixture} from './native-chat-fixture.mjs';
// F16: OA's model inventory changes while OpenCode runs. The running
// OpenCode picks up the new list without a restart, keeps the person's default
// and custom models, and a request on a new model completes. The router is a
// fixture answering Offer and ObserveOffer from servers whose models change,
// one of them a paired runtime; OpenCode 1.18 is simulated as far as the
// plugin sees it.
import assert from 'node:assert/strict';
import test from 'node:test';
import {PartKind, ReplyOutcome} from '@openabstractions/inference';
import OpenAbstractionsPlugin from '../plugin.js';
import {createOpenAbstractions} from '../index.js';
import {Availability} from '../availability.js';
import {createInventoryWatch, inventoryChangedMessage, offerSignature} from '../inventory-watch.js';
import {inventoryMarkerOption} from '../inventory.js';
import {fastWatch, observable, until} from './fixtures/observe-inventory.mjs';
import {tool as fakeTool} from '@opencode-ai/plugin/tool';
import {newPage} from '@openabstractions/facade/messages';


/** A router fixture: servers with their models and placement, a survey kept
 * in memory as the runtime keeps it, Offer and ObserveOffer by placement.
 * Declaring or withdrawing a server surveys at once, as the runtime does
 * after a registry change; a model pulled on a declared server waits for the
 * runtime's survey clock (survey()). A paired runtime that sleeps drops out
 * of a reading. */
function fixtureRouter() {
  const servers = new Map();
  const asleep = new Set();
  let families = new Map();
  const requests = [];
  const resurvey = () => {
    const next = new Map();
    for (const [server, {models, hosted}] of servers) {
      for (const model of models) {
        const family = next.get(model) ?? {servers: [], hosted: []};
        family.servers.push(server);
        family.hosted.push(hosted);
        next.set(model, family);
      }
    }
    families = next;
  };
  const offersFor = (placement) => [...families].flatMap(([family, entry]) => {
    const at = entry.servers.filter((server, index) => !asleep.has(server) && (placement === 'any'
      || (placement === 'hosted') === entry.hosted[index]));
    if (!at.length) return [];
    return [{family, names: [family], servers: at, context: 32768n, contextSource: 'maximum', input: 28672n, unknown: []}];
  });
  const live = observable(async (_options, placement) => ({offers: offersFor(placement)}));
  const observation = {caller: {}, tookMs: 0n, cacheAgeMs: 0n};
  const router = {
    requests, asleep,
    /** The runtime's survey clock, or any other survey. */
    survey() { resurvey(); live.renew(); },
    declare(server, models, {hosted = false} = {}) { servers.set(server, {models: [...models], hosted}); router.survey(); },
    withdraw(server) { servers.delete(server); router.survey(); },
    pull(server, model) { servers.get(server).models.push(model); },
    offered(model) { return families.has(model); },
    client: {
      async offer(request) {
        requests.push({method: 'Offer', placement: request.placement, fresh: request.fresh});
        if (request.fresh) resurvey();
        return {outcome: 'offered', offers: offersFor(request.placement), observation};
      },
      async observeOffer(request, cursor, waitMs) {
        requests.push({method: 'ObserveOffer', placement: request.placement, fresh: request.fresh, waitMs});
        return live.observe(null, request.placement, cursor, Number(waitMs));
      },
    },
  };
  return router;
}

/** OpenCode 1.18 as the plugin sees it: each instance start reads the config
 * file again and runs the config hook, then lists the providers; a session
 * replies busy or idle; disposing the instance finalizes the plugin, and the
 * TUI's server.instance.disposed handler starts the instance again in the
 * same process, so the plugin module stays loaded. OA Messages and presence
 * answer from fakes of their native services, so an inbox opens through the
 * plugin's own tool. */
async function runningOpenCode({file, router, startDefaults = new Map(), directory = 'C:\\project'}) {
  const host = {starts: 0, disposals: 0, failDisposals: 0, statusReads: 0, sessions: {}, toasts: [], logs: [],
    config: null, providers: null, plugin: null, watch: null};
  const client = {
    app: {log: async ({body}) => { host.logs.push(body); }},
    // Only the model-change toasts; the outside-sandbox notice is its own.
    tui: {showToast: async ({body}) => { if (/models changed/.test(body?.message)) host.toasts.push(body); }},
    session: {get: async request => ({data: {id: request.path.id, directory, title: ''}}), status: async ({query} = {}) => {
      assert.equal(query?.directory, directory);
      host.statusReads += 1;
      // A test may act between the plugin's two status reads.
      if (host.onStatus) await host.onStatus(host.statusReads);
      return {data: {...host.sessions}};
    }},
    instance: {dispose: async ({query} = {}) => {
      assert.equal(query?.directory, directory);
      if (host.failDisposals > 0) { host.failDisposals -= 1; throw Object.assign(new Error('dispose failed'), {status: 500}); }
      host.disposals += 1;
      // OpenCode announces the disposal to the instance's plugins, finalizes
      // them, and starts the instance again in the same process.
      await host.plugin.event({event: {type: 'server.instance.disposed', properties: {directory}}});
      await host.plugin.dispose();
      await start();
    }},
  };
  host.inboxOpens = 0; host.withdrawals = 0; host.announced = 0;host.helperStarts=0;host.helperCloses=0;
  const messagesApi = {inboxOpenChat: async (_instance, _source, _status, binding) => {
    if (!binding.chatId) host.inboxOpens += 1;
    return {outcome: 'opened', context: binding.chatId ? `canonical-${binding.chatId}` : ''};
  }, wait: async () => ({...newPage(), outcome: 'page', revision: 'r'})};
  const applications = {announce: async () => { host.announced += 1; return {outcome: 'applied', instance: 'native-epoch'}; },
    withdraw: async () => { host.withdrawals += 1; return {outcome: 'applied'}; }};
  async function start() {
    host.starts += 1;
    host.config = JSON.parse(file.text);
    host.plugin = await OpenAbstractionsPlugin({client, directory}, {}, {
      tool: fakeTool, env: {}, availability: new Availability(), startDefaults,
      requestOpenCodeConnection:async()=>({outcome:'connected',hostRegistered:true,helperEnrolled:true,helperProgram:'C:/kept/oa-opencode-integration.exe'}),
      checkOpenCodeConnection:async()=>({outcome:'connected',hostRegistered:true,helperEnrolled:true,helperProgram:'C:/kept/oa-opencode-integration.exe'}),
      openMessagesHelper:({id})=>{host.helperStarts++;return {initialize:async()=>({}),isClosed:()=>false,close:async()=>{host.helperCloses++;},call:async name=>{
        if(name==='oa_opencode_session_start')return {structuredContent:{outcome:'observed',session_id:id,context:'canonical-'+id}};
        if(name==='oa_messages_inbox_open'){host.inboxOpens++;return {structuredContent:{outcome:'opened'}};}
        return {structuredContent:{outcome:'page',messages:[]}};
      }};},
      resolveRouter: async () => router.client,
      readCapability: async () => false, readAnswerable: async () => false, readMessaging: async () => true,
      resolveMessages: async () => messagesApi, resolveApplications: async () => applications,
      messagesSchedule: () => 0, messagesUnschedule: () => {},
      readSelectionPolicy: async () => ({outcome: 'unknown'}),
      onInventoryWatch: (watch) => { host.watch = watch; }, inventoryWatchTiming: fastWatch,
    });
    await host.plugin.config(host.config);
    host.providers = {};
    for (const [id, provider] of Object.entries(host.config.provider)) {
      host.providers[id] = await host.plugin.provider.models({id, ...provider});
    }
  }
  /** Host lifecycle stays separate from the helper's connection-owned inbox. */
  host.openInbox = async (id = 'ses_inbox') => {
    await host.plugin.event({event: {type: 'session.created', properties: {info: {id, directory}}}});
    assert.equal(JSON.parse(await host.plugin.tool.oa_messages_inbox_open.execute({}, {sessionID:id})).outcome,'opened');
  };
  /** Reads the session's inbox through this instance's own tool. */
  host.waitInbox = async (id = 'ses_inbox') =>
    JSON.parse(await host.plugin.tool.oa_messages_wait.execute({wait_ms:0}, {sessionID:id}));
  host.deleteSession = async (id = 'ses_inbox') => {
    await host.plugin.event({event: {type: 'session.deleted', properties: {info: {id, directory}}}});
  };
  host.reloaded = (count) => until(() => host.disposals >= count, 3000, `OpenCode did not reload ${count} time(s)`);
  host.quiet = () => new Promise((resolve) => setTimeout(resolve, 150));
  host.stop = async () => { await host.plugin.dispose(); };
  await start();
  // The start surveys OA after the config hook returns and re-creates the
  // instance once when OA's answer changes the config (startup-share.test.mjs).
  // These tests follow the running OpenCode from the watch that start leaves.
  await until(() => host.watch, 3000, 'the startup did not settle into a watch');
  host.starts = 1; host.disposals = 0; host.statusReads = 0; host.toasts.length = 0;
  return host;
}

// The config configure writes: a heading for this computer with its managed
// entry, a hosted heading, a person's own custom model and their default.
function configuredFile() {
  const managed = {name: 'qwen3:8b', limit: {context: 32768, output: 4096}, tool_call: true};
  return {text: JSON.stringify({
    model: 'openabstractions/qwen3:8b',
    provider: {
      openabstractions: {npm: '@openabstractions/opencode', name: 'OpenAbstractions · this computer',
        options: {scope: 'local', [inventoryMarkerOption]: {'qwen3:8b': managed}},
        models: {'qwen3:8b': managed, 'my-tuned': {name: 'My tuned model', limit: {context: 16384, output: 2048}, tool_call: true}}},
      'openabstractions-hosted': {npm: '@openabstractions/opencode', name: 'OpenAbstractions · via hosted servers',
        options: {scope: 'remote', oaPlacement: 'hosted', guarantees: ['abstraction.inference/hosted-allowed@1'], [inventoryMarkerOption]: {}},
        models: {}},
    },
  })};
}

test('connecting a hosted model creates its heading in a running OpenCode without a configure command', async () => {
  const router=fixtureRouter();router.declare('ollama',['qwen3:8b']);
  const initial=JSON.parse(configuredFile().text);delete initial.provider['openabstractions-hosted'];
  const file={text:JSON.stringify(initial)},original=file.text;
  const host=await runningOpenCode({file,router});
  try {
    assert(!Object.hasOwn(host.providers,'openabstractions-hosted'));
    await until(()=>router.requests.some(r=>r.method==='ObserveOffer'&&r.placement==='hosted'));
    router.declare('deepseek',['deepseek-chat'],{hosted:true});
    await host.reloaded(1);
    await until(()=>!!host.providers['openabstractions-hosted']?.['deepseek-chat']);
    assert.equal(host.providers['openabstractions-hosted']['deepseek-chat'].name,'deepseek-chat');
    assert.deepEqual(host.config.provider['openabstractions-hosted'].options.guarantees,['abstraction.inference/hosted-allowed@1']);
    assert.equal(host.config.model,'openabstractions/qwen3:8b');
    assert.equal(file.text,original,'runtime group creation rewrites no installed configuration or access');
  } finally { await host.stop(); }
});

/** The production provider for one listed model, its chat call answered by
 * the fixture runtime: a model the router no longer offers is refused. */
function languageModel(host, id, modelID, router, sent) {
  const options = host.config.provider[id].options;
  const model = createOpenAbstractions(options).languageModel(modelID);
  model.finalChat = async () => null; // This fixture owns frozen chat@1 receiving.
  model.chat = async () => nativeChatFixture({async complete(request) {
    sent.push(request);
    if (!router.offered(request.model)) return {outcome: 'not_here', reason: 'router:not_here', host: '', model: '', usage: {}};
    return {outcome: ReplyOutcome.Completed, stopReason: 'end', usage: {}, host: 'mac/ollama', model: request.model,
      message: {parts: [{kind: PartKind.Text, text: `answered by ${request.model}`}]}};
  }});
  model.hostTable = new Map([['ollama', false], ['mac', true]]);
  model.hostTableAt = Date.now();
  return model;
}

test('F16: a running OpenCode picks up a paired runtime\'s model and a pulled model, keeps the default and custom model, and completes a request', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  const host = await runningOpenCode({file: configuredFile(), router});
  try {
    assert.deepEqual(Object.keys(host.providers.openabstractions).sort(), ['my-tuned', 'qwen3:8b']);
    assert.deepEqual(Object.keys(host.providers['openabstractions-hosted']), []);
    await host.quiet();
    assert.equal(host.disposals, 0, 'an unchanged inventory reloads nothing');
    const watching = router.requests.filter((request) => request.method === 'ObserveOffer');
    assert.ok(watching.length >= 2, 'each heading holds an ObserveOffer long poll');
    assert.ok(watching.every((request) => request.fresh === false), 'the watch forces no survey');

    // The person pairs the Mac; its runtime serves a model. The runtime
    // surveys on the declaration. A reply is in progress: re-creating the
    // instance would end it, so the reload waits.
    host.sessions = {busy: {type: 'busy'}};
    router.declare('mac/ollama', ['llama3.3:70b'], {hosted: true});
    await host.quiet();
    assert.equal(host.disposals, 0, 'a busy session defers the reload');
    assert.deepEqual(host.toasts, []);
    host.sessions = {busy: {type: 'idle'}};
    await host.reloaded(1);
    assert.equal(host.starts, 2, 'the instance restarts inside the same OpenCode process');
    await until(() => host.toasts.length === 1);
    assert.deepEqual(host.toasts.map((toast) => toast.message), [inventoryChangedMessage], 'the toast follows a reload that happened');
    const paired = host.providers['openabstractions-hosted']['llama3.3:70b'];
    assert.equal(paired.name, 'llama3.3:70b');
    assert.equal(host.config.model, 'openabstractions/qwen3:8b', 'the default model is kept');
    assert.deepEqual(Object.keys(host.providers.openabstractions).sort(), ['my-tuned', 'qwen3:8b']);
    assert.equal(host.providers.openabstractions['my-tuned'].name, 'My tuned model', 'the custom model is kept');

    // A model pulled on this computer's server announces nothing: the
    // runtime's survey clock finds it.
    router.pull('ollama', 'gemma4:12b');
    await host.quiet();
    assert.equal(host.disposals, 1, 'nothing changes before a survey');
    router.survey();
    await host.reloaded(2);
    assert.deepEqual(Object.keys(host.providers.openabstractions).sort(), ['gemma4:12b', 'my-tuned', 'qwen3:8b']);
    assert.equal(host.providers.openabstractions['gemma4:12b'].name, 'gemma4:12b');
    assert.equal(host.config.model, 'openabstractions/qwen3:8b');
    assert.equal(host.providers.openabstractions['my-tuned'].limit.context, 16384);

    // A request on the paired runtime's model, through the production
    // provider with the refreshed heading's options, completes there.
    const sent = [];
    const reply = await languageModel(host, 'openabstractions-hosted', 'llama3.3:70b', router, sent)
      .doGenerate({prompt: [{role: 'user', content: [{type: 'text', text: 'hello'}]}]});
    assert.equal(sent[0].model, 'llama3.3:70b');
    assert.equal(reply.content[0].text, 'answered by llama3.3:70b');
    assert.equal(reply.providerMetadata.openabstractions.placement, 'hosted');

    // Unpairing withdraws the model. What disappears counts once a later
    // survey still shows it gone; the list follows and the default stays.
    router.withdraw('mac/ollama');
    await host.quiet();
    assert.equal(host.disposals, 2, 'one survey without the Mac reloads nothing yet');
    router.survey();
    await host.reloaded(3);
    assert.deepEqual(Object.keys(host.providers['openabstractions-hosted']), []);
    assert.equal(host.config.model, 'openabstractions/qwen3:8b');
    assert.equal(router.requests.filter((request) => request.method === 'Offer' && request.fresh === false).length, 0,
      'the plugin reads no unforced Offer between starts');
  } finally {
    await host.stop();
  }
  assert.equal(host.watch.stopped, true);
});

test('a paired runtime missing from one survey reloads nothing', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  router.declare('mac/ollama', ['llama3.3:70b'], {hosted: true});
  const host = await runningOpenCode({file: configuredFile(), router});
  try {
    assert.ok(host.providers['openabstractions-hosted']['llama3.3:70b']);
    await host.quiet();
    router.asleep.add('mac/ollama');
    router.survey();
    await host.quiet();
    router.asleep.delete('mac/ollama');
    router.survey();
    await host.quiet();
    assert.equal(host.disposals, 0, 'one survey without the Mac is no change');
    router.asleep.add('mac/ollama');
    router.survey();
    await host.quiet();
    assert.equal(host.disposals, 0, 'the first survey without the Mac waits for a later one');
    router.survey();
    await host.reloaded(1);
    assert.deepEqual(Object.keys(host.providers['openabstractions-hosted']), [], 'a Mac gone across two surveys is a change');
  } finally {
    await host.stop();
  }
});

test('a model added while an OA inbox is open shows after a reload that keeps the inbox open', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  const host = await runningOpenCode({file: configuredFile(), router});
  try {
    await host.openInbox();
    assert.equal((await host.waitInbox()).outcome, 'page');
    const announcedBefore = host.announced;
    // The runtime's reconciler hosts a model the person added in another app.
    router.declare('ollama', ['qwen3:8b', 'phi4:14b']);
    await host.reloaded(1);
    assert.ok(host.providers.openabstractions['phi4:14b'], 'the new model is in the reloaded list');
    assert.deepEqual(host.toasts.map((toast) => toast.message), [inventoryChangedMessage]);
    // The new instance adopted the inbox: the same session reads it without
    // opening it again, and the presence epoch was never withdrawn.
    assert.equal((await host.waitInbox()).outcome, 'page', 'reload rebinds the genuine helper for this same chat');
    assert.equal(host.inboxOpens, 1, 'no repeated model inbox-open call');
    assert.equal(host.helperCloses,1,'plugin disposal closed the old child');
    assert.equal(host.helperStarts,2,'the new instance respawned this chat helper');
    assert.equal(host.withdrawals, 0, 'the presence was not withdrawn across the reload');
    assert.ok(host.announced >= announcedBefore);
  } finally {
    await host.stop();
  }
  assert.equal(host.withdrawals, 1, 'stopping OpenCode withdraws the adopted presence');
});

test('a reply that starts between the two status reads keeps the instance; an inbox opened then is handed over', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  const host = await runningOpenCode({file: configuredFile(), router});
  try {
    // The first read finds every session idle; a reply starts before the
    // second read, which finds it busy.
    let reads = 0;
    host.onStatus = async () => {
      reads += 1;
      if (reads === 2) host.sessions = {late: {type: 'busy'}};
    };
    router.declare('ollama', ['qwen3:8b', 'phi4:14b']);
    await until(() => reads >= 2);
    await host.quiet();
    assert.equal(host.disposals, 0, 'a reply started between the reads is not ended');
    // Idle again; this time an inbox opens between the two reads.
    host.onStatus = null;
    reads = 0;
    host.sessions = {};
    host.onStatus = async () => {
      reads += 1;
      if (reads === 2) await host.openInbox();
    };
    // An inbox opened between the reads is handed to the new instance.
    await host.reloaded(1);
    host.onStatus = null;
    assert.ok(host.providers.openabstractions['phi4:14b']);
    assert.equal((await host.waitInbox()).outcome, 'page', 'host lifecycle supplies no stock Messages authority');
  } finally {
    await host.stop();
  }
});

test('a failed dispose restarts the watch, which reloads at its next answer', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  const host = await runningOpenCode({file: configuredFile(), router});
  try {
    const first = host.watch;
    host.failDisposals = 1;
    router.declare('ollama', ['qwen3:8b', 'phi4:14b']);
    await host.reloaded(1);
    assert.equal(host.failDisposals, 0, 'the first reload failed');
    assert.equal(first.stopped, true);
    assert.ok(host.logs.some((entry) => entry.level === 'warn' && /could not reload/.test(entry.message)));
    assert.ok(host.providers.openabstractions['phi4:14b']);
    await until(() => host.toasts.length >= 1);
    await host.quiet();
    assert.deepEqual(host.toasts.map((toast) => toast.message), [inventoryChangedMessage], 'only the reload that happened is announced');
  } finally {
    await host.stop();
  }
});

test('a config naming no model keeps the default this OpenCode started with when a model sorts before it', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  const file = configuredFile();
  const config = JSON.parse(file.text);
  delete config.model;
  file.text = JSON.stringify(config);
  const host = await runningOpenCode({file, router});
  try {
    assert.equal(host.config.model, 'openabstractions/qwen3:8b');
    router.declare('lemonade', ['gemma4:12b']);
    await host.reloaded(1);
    assert.ok(host.providers.openabstractions['gemma4:12b']);
    assert.equal(host.config.model, 'openabstractions/qwen3:8b', 'a reload never moves OA\'s default to the first model by name');
    router.withdraw('ollama');
    await host.quiet();
    router.survey();
    await host.reloaded(2);
    assert.equal(host.config.model, 'openabstractions/gemma4:12b', 'a default no longer offered gives way to OA\'s default');
  } finally {
    await host.stop();
  }
});

test('OpenCode started without OA picks up OA\'s models once the runtime answers', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  const away = () => Object.assign(new Error('runtime away'), {name: 'ResolutionError', status: 'runtime_unavailable'});
  let up = false;
  const {offer, observeOffer} = router.client;
  router.client.offer = async (...args) => { if (!up) throw away(); return offer(...args); };
  router.client.observeOffer = async (...args) => { if (!up) throw away(); return observeOffer(...args); };
  const host = await runningOpenCode({file: configuredFile(), router});
  try {
    // OA's own chat is listed from the start; its requests say OA is away.
    assert.deepEqual(Object.keys(host.providers.openabstractions).sort(), ['my-tuned', 'oa-chat', 'qwen3:8b'], 'configured entries stay while OA is away');
    await host.quiet();
    assert.equal(host.disposals, 0, 'OA still away changes nothing');
    up = true;
    router.declare('ollama', ['qwen3:8b', 'phi4:14b']);
    await host.reloaded(1);
    assert.ok(host.providers.openabstractions['phi4:14b']);
    assert.ok(host.providers.openabstractions['my-tuned']);
  } finally {
    await host.stop();
  }
});

test('a runtime going away while OpenCode runs keeps its list and reloads nothing', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  const host = await runningOpenCode({file: configuredFile(), router});
  try {
    const gone = async () => { throw Object.assign(new Error('gone'), {name: 'ResolutionError', status: 'runtime_unavailable'}); };
    router.client.offer = gone;
    router.client.observeOffer = gone;
    router.survey();
    await host.quiet();
    await host.quiet();
    assert.equal(host.disposals, 0);
    assert.equal(host.watch.stopped, false, 'the watch keeps reading for the runtime\'s return');
  } finally {
    await host.stop();
  }
});

test('an OpenCode without the instance reload route says the list reloads at its next start and stops watching', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  const logs = [];
  let watch;
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async ({body}) => logs.push(body)}}, directory: 'C:\\p'}, {}, {
    tool: fakeTool, env: {}, availability: new Availability(), resolveRouter: async () => router.client,
    readCapability: async () => false, readAnswerable: async () => false, readMessaging: async () => false,
    readSelectionPolicy: async () => ({outcome: 'unknown'}),
    onInventoryWatch: (value) => { watch = value; }, inventoryWatchTiming: fastWatch,
  });
  await plugin.config(JSON.parse(configuredFile().text));
  router.declare('ollama', ['qwen3:8b', 'phi4:14b']);
  await until(() => watch.stopped, 3000, 'the watch did not stop');
  assert.ok(logs.some((entry) => /reloads its model list at its next start/.test(entry.message)));
});

test('a runtime without ObserveOffer stops the watch and says when the list reloads', async () => {
  const router = fixtureRouter();
  router.declare('ollama', ['qwen3:8b']);
  delete router.client.observeOffer;
  const host = await runningOpenCode({file: configuredFile(), router});
  try {
    await until(() => host.watch.stopped, 3000, 'the watch did not stop');
    assert.ok(host.logs.some((entry) => /cannot report model list changes/.test(entry.message)));
    assert.equal(router.requests.filter((request) => request.method === 'Offer' && !request.fresh).length, 0, 'no polling stands in');
  } finally {
    await host.stop();
  }
});

test('the watch answers at once, waits with its cursor, counts an addition at once and a removal at a later survey', async () => {
  const calls = [];
  let offers = [];
  const live = observable(async () => ({offers}));
  const headings = new Map([['oa', {options: {runtimeEndpoint: 'e'}, placements: ['any', 'local'],
    signature: offerSignature({any: [], local: []}), state: 'answered'}]]);
  const changes = [];
  const qwen = {family: 'qwen', names: ['qwen'], servers: ['ollama'], context: 32768n, contextSource: 'maximum', input: 28672n, unknown: []};
  const full = offerSignature({any: [qwen], local: [qwen]});
  const watch = createInventoryWatch({headings, ...fastWatch,
    observe: async (options, placement, cursor, waitMs, signal) => {
      calls.push([options.runtimeEndpoint, placement, cursor === '' ? 'empty' : 'cursor', waitMs]);
      return live.observe(options, placement, cursor, waitMs, signal);
    },
    // The host's refresh records the list it read, as the plugin's does.
    onChange: async (change) => {
      changes.push(change);
      headings.set('oa', {...headings.get('oa'), signature: JSON.parse(change.observed)[1], state: change.state});
    }});
  watch.start();
  try {
    await until(() => calls.filter(([, , cursor]) => cursor === 'cursor').length >= 2);
    assert.deepEqual(calls.slice(0, 2).map(([e, p, c, w]) => [e, p, c, w]).sort(), [['e', 'any', 'empty', 0], ['e', 'local', 'empty', 0]]);
    assert.ok(calls.filter(([, , cursor]) => cursor === 'cursor').every(([, , , wait]) => wait === fastWatch.waitMs));
    offers = [qwen];
    live.renew();
    await until(() => headings.get('oa').signature === full, 3000, 'an addition did not count at once');
    assert.ok(changes.every((change) => change.id === 'oa' && change.state === 'answered'));
    const added = changes.length;
    offers = [];
    live.renew();
    await new Promise((resolve) => setTimeout(resolve, 100));
    assert.equal(changes.length, added, 'one survey without qwen is no change yet');
    await new Promise((resolve) => setTimeout(resolve, 10));
    live.renew();
    await until(() => changes.length === added + 1, 3000, 'a later survey did not count the removal');
    await new Promise((resolve) => setTimeout(resolve, 250));
    assert.equal(changes.length, added + 1, 'a counted change is handled once');
  } finally {
    watch.stop();
  }
  assert.equal(watch.stopped, true);
});

test('a failed answer is read again after the retry delay, and a failing handler is reported', async () => {
  const errors = [];
  let attempts = 0;
  const delays = [];
  const headings = new Map([['oa', {options: {}, placements: ['local'], signature: 'old', state: 'answered'}]]);
  const watch = createInventoryWatch({headings, ...fastWatch,
    delay: async (ms) => { delays.push(ms); await new Promise((resolve) => setTimeout(resolve, 5)); },
    observe: async () => { attempts += 1; throw Object.assign(new Error('refused'), {code: 'not_permitted'}); },
    onError: (error) => errors.push(error.message),
    onChange: async () => { throw new Error('handler failed'); }});
  watch.start();
  try {
    await until(() => errors.length >= 2);
    assert.ok(attempts >= 3);
    assert.ok(delays.every((ms) => ms === fastWatch.retryAfterMs));
    assert.ok(errors.every((message) => message === 'handler failed'));
  } finally {
    watch.stop();
  }
});

test('the signature follows models, servers and whether a model is listed, and ignores the context number and order', () => {
  const offer = (family, servers = ['ollama'], context = 32768n, fields = {}) => ({family, names: [family], servers, context,
    contextSource: 'maximum', input: context - 4096n, unknown: [], ...fields});
  const base = offerSignature({local: [offer('a'), offer('b')]});
  assert.equal(offerSignature({local: [offer('b'), offer('a')]}), base);
  assert.notEqual(offerSignature({local: [offer('a')]}), base);
  assert.notEqual(offerSignature({local: [offer('a', ['mac/ollama']), offer('b')]}), base);
  assert.equal(offerSignature({local: [offer('a', ['ollama'], 8192n, {contextSource: 'effective'}), offer('b')]}), base,
    'another app reloading a model with a different window reloads nothing');
  assert.notEqual(offerSignature({local: [offer('a', ['ollama'], 4096n), offer('b')]}), base, 'a model leaving no input is no longer listed');
});
