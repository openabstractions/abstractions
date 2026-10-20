import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import OpenAbstractionsPlugin from '../plugin.js';
import {createProjectAccess, projectSessionEnvironment, resolveProjectClient} from '../project-access.js';
import * as protocol from '../session-api/js/opencode/session/index.mjs';
import {outsideSandboxMessage, searchToolMissingMessage} from '../opencode-build.js';

const optional = () => ({optional() { return this; }});
const tool = Object.assign((value) => value, {schema: {
  string: optional, object: (value) => value, number: optional, boolean: optional, enum: optional, array: optional,
}});
const env = {OA_OPENCODE_SESSION_ENDPOINT: '\\\\.\\pipe\\oa-opencode-session',
  OA_OPENCODE_SESSION_PROGRAM: 'C:\\OA\\openabstractions.exe', OA_OPENCODE_SESSION_ACCOUNT: 'S-1-5-21-123',
  OA_OPENCODE_SESSION_ID: 'native-owner-session', OA_OPENCODE_RUNTIME_PROGRAM: 'C:\\OA\\runtime.exe',
  OA_OPENCODE_RUNTIME_ACCOUNT: 'S-1-5-21-123'};
const session = projectSessionEnvironment(env);
const requestKey = '6e88c9f9-a01d-4e54-8386-884f3a4a5a50';
const folder = 'C:\\Projects\\selected';
const context = (signal = new AbortController().signal) => ({sessionID: 'ses_current', messageID: 'msg_current', abort: signal,
  ask: async () => { throw new Error('An OpenCode approval cannot grant OA access.'); }});
const receipt = (outcome = 'approved_pending_close', key = requestKey) => ({outcome, requestKey: key,
  receipt: ['approved_pending_close', 'receipt_recorded'].includes(outcome) ? 'owner-receipt' : '',
  questionId: 'question_exact', message: 'Follow the native owner instructions to close and reopen this conversation.'});
const completed = (approved) => ({id: 'part_exact', sessionID: 'ses_current', messageID: 'msg_current', type: 'tool',
  callID: 'call_exact', tool: 'openabstractions_project_access', state: {status: 'completed', input: {folder},
    title: 'Project access', output: 'Approved', metadata: {oaProjectReceipt: approved}, time: {start: 1, end: 1730000000002}}});
const savedMessage = (part) => ({info: {id: 'msg_current', sessionID: 'ses_current', role: 'assistant', time: {created: 1}}, parts: [part]});
const completionEvent = (part) => ({event: {type: 'message.part.updated', properties: {part}}});
const completion = {requestKey, sessionId: 'ses_current', messageId: 'msg_current', partId: 'part_exact',
  toolCallId: 'call_exact', receipt: 'owner-receipt', completedAt: 1730000000002n};

async function approvedFixture(overrides = {}) {
  const notices = [];
  const calls = [];
  const connections = [];
  const approved = receipt();
  const part = completed(approved);
  const api = {session: {message: async (options) => { calls.push(['message', options]); return {data: savedMessage(part)}; },
    messages: async () => { throw new Error('No whole-conversation quiescence read is required.'); },
    status: async () => { throw new Error('No idle claim is made.'); }}};
  const endpoint = {requestProject: async (request) => { calls.push(['request', request]); return approved; },
    acknowledgeReceipt: async (value) => { calls.push(['acknowledge', value]); return receipt('receipt_recorded'); }};
  const access = createProjectAccess({tool, session, client: api, uuid: () => requestKey,
    resolveClient: async (selected, signal) => { connections.push({selected, signal}); return endpoint; },
    announce: async (message) => notices.push(message), ...overrides});
  const output = await access.definition.execute({folder}, context());
  return {access, output, part, api, endpoint, calls, notices, connections};
}

test('the native session activates only with all six valid launch values', () => {
  assert.equal(projectSessionEnvironment({}), null);
  assert.equal(createProjectAccess({tool, session: null}), null);
  for (const field of Object.keys(env)) {
    const missing = {...env}; delete missing[field];
    assert.equal(projectSessionEnvironment(missing), null, field);
    assert.equal(projectSessionEnvironment({...env, [field]: 'bad\0value'}), null, field);
  }
  assert.equal(projectSessionEnvironment({...env, OA_OPENCODE_SESSION_PROGRAM: 'relative.exe'}), null);
  assert.deepEqual(session.server, {principalKind: 1, principal: env.OA_OPENCODE_SESSION_ACCOUNT, program: env.OA_OPENCODE_SESSION_PROGRAM});
  assert.ok(Object.isFrozen(session.server));
});

test('outside the sandbox every tool but folder requests registers and one toast names the OA start', async () => {
  const toasts = [];
  const logs = [];
  const client = {tui: {showToast: async ({body}) => toasts.push(body)}, app: {log: async ({body}) => logs.push(body)}};
  const plugin = await OpenAbstractionsPlugin({client}, {tools: {watch_logs: true, selection_policy: true}}, {tool, env: {}});
  assert.deepEqual(Object.keys(plugin.tool).sort(), ['openabstractions_selection_policy', 'openabstractions_watch_logs']);
  for (const name of ['chat.message', 'command.execute.before', 'shell.env', 'tool.execute.before', 'dispose']) {
    assert.equal(plugin[name], undefined, name);
  }
  assert.equal(toasts.length, 0, 'nothing is said before OpenCode delivers its first event');
  await plugin.event({event: {type: 'server.connected', properties: {}}});
  await plugin.event({event: {type: 'session.updated', properties: {}}});
  await plugin.event(completionEvent(completed(receipt())));
  assert.deepEqual(toasts, [{title: 'OpenAbstractions', message: outsideSandboxMessage, variant: 'info'}],
    'outside the sandbox the search notice never shows');
  assert.equal(outsideSandboxMessage, 'OpenCode is running outside the OA sandbox. To start it protected, run: node <plugin folder>/official-install.js --project <folder> --run');
  assert.deepEqual(logs.map((entry) => entry.message), [outsideSandboxMessage]);
  const quiet = await OpenAbstractionsPlugin({client: {tui: {showToast: async () => { throw new Error('no TUI'); }}}}, {}, {tool, env: {}});
  await quiet.event({event: {type: 'server.connected'}});
  const partial = await OpenAbstractionsPlugin({client: {}}, {}, {tool, env: {...env, OA_OPENCODE_RUNTIME_ACCOUNT: ''}});
  assert.deepEqual(Object.keys(partial.tool), []);
  assert.equal(typeof partial.event, 'function', 'a partial session environment counts as outside');
});

test('inside the sandbox the project-access tool registers and the one notice says search is unavailable', async () => {
  const toasts = [];
  const logs = [];
  const client = {tui: {showToast: async ({body}) => toasts.push(body)}, app: {log: async ({body}) => logs.push(body)}};
  const native = await OpenAbstractionsPlugin({client}, {}, {tool, env});
  assert.deepEqual(Object.keys(native.tool), ['openabstractions_project_access']);
  assert.equal(typeof native.event, 'function');
  assert.equal(toasts.length, 0, 'nothing is said before OpenCode delivers its first event');
  await native.event({event: {type: 'server.connected', properties: {}}});
  await native.event({event: {type: 'session.updated', properties: {}}});
  assert.deepEqual(toasts, [{title: 'OpenAbstractions', message: searchToolMissingMessage, variant: 'warning'}],
    'once per start, and never the outside-the-sandbox toast');
  assert.equal(searchToolMissingMessage, 'Search tools aren\'t available inside the OA sandbox yet.');
  assert.deepEqual(logs.map((entry) => [entry.level, entry.message]), [['warn', searchToolMissingMessage]]);
  for (const name of ['chat.message', 'command.execute.before', 'shell.env', 'tool.execute.before', 'dispose']) {
    assert.equal(native[name], undefined, name);
  }
  const quiet = await OpenAbstractionsPlugin({client: {tui: {showToast: async () => { throw new Error('no TUI'); }}}}, {}, {tool, env});
  await quiet.event({event: {type: 'server.connected'}});
});

test('the generated client sends both exact typed methods over mandatory native server proof', async () => {
  const calls = [];
  const methods = [];
  class BunNativeConnector {
    connect(endpoint, options) {
      calls.push([endpoint, options]);
      return {exchangeFrame: async (bytes) => {
        const frame = JSON.parse(new TextDecoder().decode(bytes));
        assert.equal(frame.service, 'openabstractions.opencode/session-project@1');
        methods.push(frame.method);
        if (frame.method === 'RequestProject') {
          assert.deepEqual(frame.arguments, {request: {request_key: requestKey, folder, session_id: 'ses_current', message_id: 'msg_current'}});
        } else {
          assert.equal(frame.method, 'AcknowledgeReceipt');
          assert.deepEqual(frame.arguments, {completion: {request_key: requestKey, session_id: 'ses_current', message_id: 'msg_current',
            part_id: 'part_exact', tool_call_id: 'call_exact', receipt: 'owner-receipt', completed_at: 1730000000002}});
        }
        return new TextEncoder().encode(JSON.stringify({version: 1, service: frame.service, method: frame.method, ok: true,
          payload: {value: {outcome: frame.method === 'RequestProject' ? 'approved_pending_close' : 'receipt_recorded',
            request_key: requestKey, receipt: 'owner-receipt', question_id: 'question_exact', message: 'Native instructions'}}}));
      }};
    }
  }
  const abort = new AbortController().signal;
  const endpoint = await resolveProjectClient(session, abort, {ipc: {BunNativeConnector}, protocol});
  assert.equal((await endpoint.requestProject({requestKey, folder, sessionId: 'ses_current', messageId: 'msg_current'})).outcome, 'approved_pending_close');
  assert.equal((await endpoint.acknowledgeReceipt(completion)).outcome, 'receipt_recorded');
  assert.deepEqual(methods, ['RequestProject', 'AcknowledgeReceipt']);
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], env.OA_OPENCODE_SESSION_ENDPOINT);
  assert.deepEqual(calls[0][1].server, session.server);
  assert.equal(calls[0][1].cancellation, abort);
  await assert.rejects(resolveProjectClient({...session, server: null}, abort, {ipc: {BunNativeConnector}, protocol}), /identity is unavailable/);
  assert.equal(calls.length, 1, 'missing proof must refuse before connecting');
});

test('native server-proof refusal sends no request retry or receipt acknowledgment', async () => {
  let calls = 0;
  class BunNativeConnector {
    connect() { return {exchangeFrame: async () => { calls++; throw Object.assign(new Error('Untrusted host'), {status: 8}); }}; }
  }
  const access = createProjectAccess({tool, session, uuid: () => requestKey,
    resolveClient: (s, signal) => resolveProjectClient(s, signal, {ipc: {BunNativeConnector}, protocol})});
  await assert.rejects(access.definition.execute({folder}, context()), {status: 8});
  assert.equal(calls, 1);
});

test('pending retries retain the same UUID and exact folder/conversation tuple', async () => {
  let generated = 0;
  const requests = [];
  const access = createProjectAccess({tool, session, uuid: () => { generated++; return requestKey; }, wait: async () => {},
    resolveClient: async () => ({requestProject: async (request) => {
      requests.push(request); return receipt(requests.length < 3 ? 'pending' : 'denied');
    }})});
  const result = await access.definition.execute({folder}, context());
  assert.equal(generated, 1);
  assert.equal(requests.length, 3);
  assert.equal(requests[0], requests[2]);
  assert.deepEqual(requests[0], {requestKey, folder, sessionId: 'ses_current', messageId: 'msg_current'});
  assert.equal(result.metadata.oaProjectOutcome, 'denied');
  assert.equal(result.metadata.oaProjectReceipt, undefined);
});

test('pending wait honors abort and never starts another tuple', async () => {
  const controller = new AbortController();
  let calls = 0;
  const access = createProjectAccess({tool, session, uuid: () => requestKey,
    resolveClient: async () => ({requestProject: async () => { calls++; return receipt('pending'); }}),
    wait: async (signal) => { assert.equal(signal, controller.signal); controller.abort(); }});
  await assert.rejects(access.definition.execute({folder}, context(controller.signal)), {name: 'AbortError'});
  assert.equal(calls, 1);
});

test('denied, expired, conflict and unavailable results never become approval receipts', async () => {
  for (const outcome of ['denied', 'expired', 'conflict', 'unavailable']) {
    let calls = 0;
    const access = createProjectAccess({tool, session, uuid: () => requestKey,
      resolveClient: async () => ({requestProject: async () => { calls++; return receipt(outcome); }})});
    const result = await access.definition.execute({folder}, context());
    assert.equal(result.metadata.oaProjectOutcome, outcome);
    assert.equal(result.metadata.oaProjectReceipt, undefined);
    assert.equal(calls, 1);
  }
});

test('relative folders, extra fields and missing context refuse before IPC', async () => {
  let calls = 0;
  const access = createProjectAccess({tool, session, resolveClient: async () => { calls++; }});
  for (const args of [{folder: 'relative'}, {folder: '\\\\server\\share'}, {folder, access: 'all'}, {folder: folder+'\0'}]) {
    await assert.rejects(access.definition.execute(args, context()), /exact absolute local folder/);
  }
  await assert.rejects(access.definition.execute({folder}, {...context(), messageID: ''}), /context/);
  assert.equal(calls, 0);
});

test('wrong request, incomplete approval and premature or unknown acknowledgment replies refuse', async () => {
  for (const reply of [receipt('approved_pending_close', 'another-key'), {...receipt(), receipt: ''},
    receipt('receipt_recorded'), receipt('unrecognized'), {...receipt(), extra: 'ambiguous'}]) {
    const access = createProjectAccess({tool, session, uuid: () => requestKey,
      resolveClient: async () => ({requestProject: async () => reply})});
    await assert.rejects(access.definition.execute({folder}, context()), /ambiguous|incomplete|cannot confirm/);
  }
});

test('approval exposes only the typed receipt and native close/reopen instructions without freezing entries', async () => {
  const fixture = await approvedFixture();
  assert.deepEqual(fixture.output.metadata, {oaProjectReceipt: receipt()});
  assert.equal(fixture.output.output, receipt().message);
  assert.deepEqual(Object.keys(fixture.access.hooks), ['event']);
  assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request']);
});

test('the committed completed receipt is acknowledged only after its exact persisted tool part is read', async () => {
  const fixture = await approvedFixture();
  await fixture.access.hooks.event(completionEvent(fixture.part));
  assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request', 'message', 'acknowledge']);
  assert.deepEqual(fixture.calls[1][1], {path: {id: 'ses_current', messageID: 'msg_current'}, throwOnError: true});
  assert.deepEqual(fixture.calls[2][1], completion);
  assert.deepEqual(fixture.notices, [receipt('receipt_recorded').message]);
  assert.equal(fixture.connections.length, 2);
  assert.equal(fixture.connections[1].selected, session);
  assert.ok(fixture.connections[1].signal instanceof AbortSignal);
});

test('idle and pending/running/error tool parts do not trigger receipt acknowledgment', async () => {
  const fixture = await approvedFixture();
  await fixture.access.hooks.event({event: {type: 'session.idle', properties: {sessionID: 'ses_current'}}});
  for (const status of ['pending', 'running', 'error', 'interrupted']) {
    await fixture.access.hooks.event(completionEvent({...fixture.part, state: {...fixture.part.state, status}}));
  }
  assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request']);
  assert.deepEqual(fixture.notices, []);
});

test('completion must match exact tool, folder, request, receipt, conversation, part IDs and integer timestamp', async () => {
  const state = completed(receipt()).state;
  const changes = [{tool: 'other'}, {sessionID: 'other'}, {messageID: 'other'}, {id: ''}, {callID: ''},
    {state: {...state, input: {folder: 'C:\\other'}}}, {state: {...state, input: {folder, extra: true}}},
    {state: {...state, time: {end: NaN}}}, {state: {...state, time: {end: 1.5}}}, {state: {...state, time: {end: 0}}},
    {state: {...state, metadata: {oaProjectReceipt: {...receipt(), receipt: 'other'}}}},
    {state: {...state, metadata: {oaProjectReceipt: {...receipt(), requestKey: 'other'}}}}];
  for (const change of changes) {
    const fixture = await approvedFixture();
    await fixture.access.hooks.event(completionEvent({...fixture.part, ...change}));
    assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request']);
  }
});

test('missing, changed or duplicated saved parts and wrong saved message refuse without retry', async () => {
  const state = completed(receipt()).state;
  for (const change of [
    (part) => ({data: {...savedMessage(part), info: {id: 'other', sessionID: 'ses_current', role: 'assistant'}}}),
    (part) => ({data: {...savedMessage(part), info: {id: 'msg_current', sessionID: 'other', role: 'assistant'}}}),
    (part) => ({data: {...savedMessage(part), info: {id: 'msg_current', sessionID: 'ses_current', role: 'user'}}}),
    () => ({error: {message: 'Unavailable'}}), () => ({data: {}}),
    (part) => ({data: {...savedMessage(part), parts: []}}),
    (part) => ({data: {...savedMessage(part), parts: [part, part]}}),
    (part) => ({data: savedMessage({...part, callID: 'other'})}),
    (part) => ({data: savedMessage({...part, state: {...state, status: 'running'}})}),
    (part) => ({data: savedMessage({...part, state: {...state, time: {end: state.time.end + 1}}})}),
    (part) => ({data: savedMessage({...part, state: {...state, metadata: {oaProjectReceipt: {...receipt(), questionId: 'other'}}}})}),
  ]) {
    const fixture = await approvedFixture();
    fixture.api.session.message = async () => { fixture.calls.push(['message']); return change(fixture.part); };
    await fixture.access.hooks.event(completionEvent(fixture.part));
    await fixture.access.hooks.event(completionEvent(fixture.part));
    assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request', 'message']);
    assert.equal(fixture.notices.length, 1);
    assert.match(fixture.notices[0], /could not be confirmed/);
  }
});

test('concurrent duplicate completion events send one persisted read and one acknowledgment', async () => {
  const fixture = await approvedFixture();
  await Promise.all([fixture.access.hooks.event(completionEvent(fixture.part)), fixture.access.hooks.event(completionEvent(fixture.part))]);
  await fixture.access.hooks.event(completionEvent(fixture.part));
  assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request', 'message', 'acknowledge']);
});

test('a conflicting completed part during the saved read refuses before acknowledgment', async () => {
  const fixture = await approvedFixture();
  fixture.api.session.message = async () => {
    fixture.calls.push(['message']);
    await fixture.access.hooks.event(completionEvent({...fixture.part, id: 'another_part'}));
    return {data: savedMessage(fixture.part)};
  };
  await fixture.access.hooks.event(completionEvent(fixture.part));
  assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request', 'message']);
  assert.equal(fixture.connections.length, 1);
  assert.equal(fixture.notices.length, 1);
});

test('lost and ambiguous native acknowledgment replies remain unconfirmed without automatic retry', async () => {
  for (const value of [null, receipt('denied'), receipt('receipt_recorded', 'other'),
    {...receipt('receipt_recorded'), receipt: 'other'}, {...receipt('receipt_recorded'), questionId: 'other'},
    {...receipt('receipt_recorded'), extra: true}]) {
    const fixture = await approvedFixture();
    fixture.endpoint.acknowledgeReceipt = async (record) => {
      fixture.calls.push(['acknowledge', record]);
      if (!value) throw new Error('Reply lost after durable save');
      return value;
    };
    await fixture.access.hooks.event(completionEvent(fixture.part));
    await fixture.access.hooks.event(completionEvent(fixture.part));
    assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request', 'message', 'acknowledge']);
    assert.equal(fixture.notices.length, 1);
    assert.match(fixture.notices[0], /could not be confirmed/);
  }
});

test('receipt acknowledgment requires no idle or finished assistant claim and a failed notice causes no retry', async () => {
  let notices = 0;
  const fixture = await approvedFixture({announce: async () => { notices++; throw new Error('Notice unavailable'); }});
  fixture.api.session.message = async () => {
    fixture.calls.push(['message']);
    return {data: {...savedMessage(fixture.part), parts: [fixture.part,
      {id: 'other_tool', type: 'tool', state: {status: 'running'}}]}};
  };
  await fixture.access.hooks.event(completionEvent(fixture.part));
  await fixture.access.hooks.event(completionEvent(fixture.part));
  assert.deepEqual(fixture.calls.map(([kind]) => kind), ['request', 'message', 'acknowledge']);
  assert.equal(notices, 1);
});

// Test real resolver bodies with the imported constructors replaced. No native
// library is loaded, and every affected Machine option is observed directly.
async function resolverFixture(file, exports) {
  const source = await readFile(new URL(`../${file}`, import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export default .*;\r?$/gm, '').replace(/^export /gm, '')
    .replace(/import\('([^']+)'\)/g, (_, name) => `Promise.resolve(modules[${JSON.stringify(name)}])`);
  return new Function('modules', `${body}\nreturn {${exports.join(',')}};`);
}

test('content, speech, preload, dictation and all six plugin Machine constructors retain selected runtime proof', async () => {
  const waiting = [];
  class Machine {
    constructor(endpoint, options) { waiting.push({endpoint, options}); }
    async resolveService() { return {}; }
  }
  class Client { constructor(binding) { this.binding = binding; } }
  const modules = {'@openabstractions/facade': {Machine}, '@openabstractions/ipc/bun': {BunNativeConnector: class {}},
    '@openabstractions/inference': {SelectionOperatorV2Client: Client, resolvePreload: async () => ({})},
    '@openabstractions/inference/protocol': {ImageClient: Client, ImageV2Client: Client, SpeechClient: Client},
    '@openabstractions/storage-content': {ContentReaderClient: Client}, '@openabstractions/router': {RouterClient: Client},
    '@openabstractions/logging': {SourceReaderClient: Client}, '@openabstractions/rights': {AuthorizationClient: Client},
    '@openabstractions/asks': {QuestionResponderClient: Client}};
  const options = {runtimeEndpoint: 'exact-runtime', server: session.runtimeServer};
  const signal = new AbortController().signal;
  for (const [file, name] of [['content-delivery.js', 'resolveContentReader'], ['speak.js', 'resolveSpeechClient'], ['preload.js', 'resolvePreloader']]) {
    await (await resolverFixture(file, [name]))(modules)[name](options, signal);
  }
  const names = ['resolveRouter', 'resolveSelectionOperator', 'resolveSourceReader', 'resolveAuthorization', 'resolveImageClient', 'resolveQuestionResponder'];
  const dictation = (await resolverFixture('listen.js', ['resolveService']))(modules);
  await dictation.resolveService(options, 'abstraction.inference/transcription@1', signal, async () => Client);
  const plugin = (await resolverFixture('plugin.js', names))(modules);
  for (const name of names) await plugin[name](options, signal);
  assert.equal(waiting.length, 10);
  for (const entry of waiting) {
    assert.equal(entry.endpoint, 'exact-runtime');
    assert.equal(entry.options.server, session.runtimeServer);
  }
  waiting.length = 0;
  await plugin.resolveRouter({runtimeEndpoint: 'ordinary'});
  assert.equal(waiting[0].options.server, null, 'ordinary selection retains its existing default');
});

test('plugin surveys and runtime tool resolvers retain the launch-selected server', async () => {
  const observed = [];
  const plugin = await OpenAbstractionsPlugin({client: {}}, {tools: {selection_policy: true}}, {tool, env,
    readInventory: async (options) => { observed.push(options); return {offers: []}; },
    readCapability: async (options) => { observed.push(options); return false; },
    readAnswerable: async (options) => { observed.push(options); return false; },
    resolveSelectionOperator: async (options) => { observed.push(options); throw new Error('end at resolver'); }});
  await plugin.config({provider: {openabstractions: {npm: '@openabstractions/opencode',
    options: {runtimeEndpoint: 'exact-runtime', server: {principalKind: 1, principal: 'other', program: 'C:\\other.exe'}}, models: {}}}});
  await assert.rejects(plugin.tool.openabstractions_selection_policy.execute({action: 'read', name: 'selected'}, {abort: new AbortController().signal}), /end at resolver/);
  assert.ok(observed.length >= 4);
  for (const options of observed) assert.deepEqual(options.server, session.runtimeServer);
});

test('TUI speech keeps the selected runtime server when reconstructing resolver options', async () => {
  const source = await readFile(new URL('../speak.js', import.meta.url), 'utf8');
  const body = source.slice(source.indexOf('export async function installSpeak')).replace(/^export /, '');
  const observed = [];
  const install = new Function('resolveSpeechClient', 'resolveContentReader', 'openTuiAudio', 'lastReply',
    'readSpeechDelivery', 'synthesizeSpeech', 'speechVoice', 'digestPattern', 'mediaTypes', 'maxSpeechBytes',
    'readDelivery', 'speakToggle', 'spokenText', `${body}\nreturn installSpeak;`)(
      async (options) => { observed.push(options); throw new Error('end at speech resolution'); },
      async (options) => { observed.push(options); throw new Error('end at content resolution'); },
      async () => {}, () => '', async () => {}, async () => {}, 'af_sky', /^sha256:[0-9a-f]{64}$/,
      new Set(['audio/wav']), 1000, async () => {}, 'speak', (value) => value);
  let listener;
  const api = {kv: {get: () => false}, command: {register: () => {}}, event: {on: (_name, callback) => { listener = callback; }},
    ui: {toast: async () => {}}, state: {session: {}}, log: {error: () => {}}};
  await install(api, {runtimeEndpoint: 'exact-runtime', server: session.runtimeServer});
  // The runtime options are constructed before registration; the resolver
  // closures are exercised by the actual completed tool event below.
  assert.equal(typeof listener, 'function');
  listener({properties: {part: {type: 'tool', tool: 'openabstractions_speak', state: {status: 'completed', metadata: {
    operation: 'op', mediaType: 'audio/wav', digest: 'sha256:'+ '0'.repeat(64), bytes: 4}}}}});
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(observed.length, 1);
  assert.deepEqual(observed[0], {runtimeEndpoint: 'exact-runtime', server: session.runtimeServer});
});
