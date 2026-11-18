import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {APICallError} from '@ai-sdk/provider';
import * as sdk from '@openabstractions/inference';
import * as availability from '../availability.js';
import {activation, generateChat3, streamChat3, usageChat3, uncertainChat3Message} from '../chat3.js';
import {FixtureMachine, nativeChatFixture} from './native-chat-fixture.mjs';
import OpenAbstractionsPlugin from '../plugin.js';
import {tool} from '@opencode-ai/plugin/tool';
import {openCodeSeesOverflow} from './opencode-overflow.mjs';

function part(block, kind, text = '') {
  return {...sdk.newChatPart(), block, kind, text};
}
function event(sequence, kind, payload) {
  return {...sdk.newChatEvent(), sequence, kind, [({part: 'part', progress: 'progress', block_end: 'blockEnd', end: 'end'})[kind]]: payload};
}
function terminal(finishReason = 'stop', outcome = 'completed', cause = '') {
  return {...sdk.newChatResult(), outcome, cause, finishReason,
    usage: {...sdk.newChatUsage(), output: 0n}, servedBy: {place: 'local', server: 'actual-engine', machine: ''}};
}
function frames() {
  const reasoning = part(0n, 'reasoning', 'thinking');
  reasoning.seal = {origin: 'openai-compatible', bytes: new Uint8Array([115, 105, 103])};
  return [event(0n, 'progress', {...sdk.newChatProgress(), phase: 'metadata'}),
    event(1n, 'part', reasoning), event(2n, 'block_end', {block: 0n}),
    event(3n, 'part', part(1n, 'text', 'answer')), event(4n, 'block_end', {block: 1n}),
    event(5n, 'end', terminal())];
}
function fixtureChat(events = frames()) {
  return {requests: [], cancels: 0, observes: 0,
    async getHistoryWindow() { return {outcome: 'available', window: {historyEpoch: 'epoch'}}; },
    async start(request) { this.requests.push(structuredClone(request)); return {outcome: 'accepted', request: 'one-request', server: 'admitted-engine', model: 'served-model'}; },
    async observe(_id, cursor) { this.observes++; const suffix = events.filter(event => event.sequence >= cursor); return {outcome: 'page', events: suffix, next: cursor + BigInt(suffix.length), atEnd: suffix.at(-1)?.kind === 'end'}; },
    async cancel() { this.cancels++; },
  };
}
async function modelFixture(chat, config = {}, resolveFinal = async () => chat) {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const permissionSource = await readFile(new URL('../permission-wait.js', import.meta.url), 'utf8');
  const body = permissionSource.replace(/^export /gm, '') + '\n' + source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage',
    'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'resolveInferenceV3', 'generateChat3', 'streamChat3',
    'runtimeConnector', 'DeltaKind', 'Fold', 'PartKind', 'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason',
    `${body}\nreturn createOpenAbstractions;`);
  const old = async () => { throw new Error('legacy fallback after chat@3 selection'); };
  const create = make(availability.availability, availability.runtimeUnavailable, availability.runtimeUntrusted,
    availability.unavailableMessage, availability.untrustedMessage, FixtureMachine, APICallError, old, old,
    resolveFinal, generateChat3, streamChat3, async () => ({}), sdk.DeltaKind, sdk.Fold, sdk.PartKind,
    sdk.ReplyOutcome, sdk.RequestGuarantee, sdk.Role, sdk.StopReason);
  return create(config).languageModel('model');
}

test('title preference preserves required guarantees and refuses legacy downgrade', async () => {
  const required = [sdk.ChatGuarantee.LocalOnly, sdk.ChatGuarantee.FreeCapacity];
  const config = {oaSelectionPolicies: {model: 'title'}, oaChatGuarantees: {model: required}};
  const chat = fixtureChat();
  const model = await modelFixture(chat, config);
  await model.doGenerate({prompt: [{role: 'user', content: [{type: 'text', text: 'title'}]}]});
  assert.equal(chat.requests[0].target.selectionPolicy, 'title');
  assert.deepEqual(chat.requests[0].content.guarantees, required);
  const unsupported = await modelFixture(fixtureChat(), config, async () => {
    throw Object.assign(new Error('chat@3 unavailable'), {name: 'ResolutionError', status: 'incompatible'});
  });
  await assert.rejects(unsupported.doGenerate({prompt: []}), /chat@3 unavailable/);
  for (const malformed of ['abstraction.inference/free-capacity@1', [1], null]) {
    let resolutions = 0;
    const invalid = await modelFixture(fixtureChat(), {...config, oaChatGuarantees: {model: malformed}}, async () => {
      resolutions++; throw new Error('negotiation must not run');
    });
    await assert.rejects(invalid.doGenerate({prompt: []}), /oaChatGuarantees/);
    assert.equal(resolutions, 0);
  }
});
function options(extra = {}) {
  return {prompt: [{role: 'user', content: [{type: 'text', text: 'prompt'}]}],
    providerOptions: {openabstractions: {}}, ...extra};
}
async function collect(stream) { const out = []; for await (const value of stream) out.push(value); return out; }

test('native runtime composition uses the actual OpenCode adapter',
  {skip: !process.env.OA_CHAT3_NATIVE_COMPOSITION && 'native composition owner supplies the isolated endpoint'}, async t => {
    const run = JSON.parse(process.env.OA_CHAT3_NATIVE_COMPOSITION);
    const {createOpenAbstractions} = await import('../index.js');
    let config = {runtimeEndpoint: run.endpoint, server: run.server, scope: run.mode === 'remote' ? 'remote' : 'local'};
    let modelID = run.model;
    if (['title','unready-title'].includes(run.mode)) {
      const {main} = await import('../configure.js');
      assert.equal(await main(['configure','--config',run.config,'--model',run.model,'--endpoint',run.endpoint],
        {out() {},err(text) { throw Error(text); }}, {resolveRouter: async () => ({offer: async () => ({outcome:'offered',offers:[{
          family:run.model,names:[run.model],servers:['fixture'],context:32768n,contextSource:'effective',input:28672n,
        }]})})}),0);
      const saved = JSON.parse(await readFile(run.config,'utf8'));
      assert.equal(saved.agent.title.disable,run.mode === 'unready-title');
      if (run.mode === 'unready-title') {assert.equal(saved.small_model,undefined);return;}
      assert.equal(saved.small_model,'openabstractions-preferences/oa-preference-title');
      modelID = 'oa-preference-title';
      config = {...saved.provider['openabstractions-preferences'].options,...config};
    } else if (['legacy','remote'].includes(run.mode)) {
      modelID = 'policy';
      config.oaSelectionPolicies = {policy:'local'};
    }
    const model = createOpenAbstractions(config).languageModel(modelID);
    const opts = options({providerOptions:{openabstractions:{oaSessionID:'shared-session'}}});
    if (run.mode === 'remote') {
      assert.equal(await model.finalChat(opts),null);
      assert.ok(await model.chatV2(opts));
      await assert.rejects(model.doGenerate(opts),error => error.outcome === 'no_host');
      return;
    }
    if (run.mode === 'lost-start') {
      const original = model.finalChat.bind(model),admissions=[];
      model.finalChat = async options => {
        const native = await original(options);
        return {getHistoryWindow:()=>native.getHistoryWindow(),observe:(...args)=>native.observe(...args),cancel:(...args)=>native.cancel(...args),
          async start(request){const out=await native.start(request);admissions.push(out.request);if(admissions.length===1)throw Error('inert lost Start reply');return out;}};
      };
      let failure;try {await model.doGenerate(opts);}catch(error){failure=error;}
      assert.equal(failure?.isRetryable,false);
      assert.equal(failure?.message,uncertainChat3Message);
      opts.providerOptions.openabstractions.oaSessionID='edited-after-send';
      await failure.reconcile();
      assert.equal(admissions.length,2);assert.ok(admissions[0]);assert.equal(admissions[0],admissions[1]);
      return;
    }
    const reply = await model.doStream(opts),parts=await collect(reply.stream);
    assert.ok(parts.some(part=>part.type==='text-delta'&&part.delta),JSON.stringify(parts));
    assert.equal(parts.at(-1).type,'finish');
  });

test('host session and subagent IDs label only served chat@3 turns', async t => {
  const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {tool, env: {}, connectionRequired: false,
    readInventory: async () => ({offers: []}), readCapability: async () => false,
    probeRuntime: async () => true, readSelectionPolicy: async () => ({outcome: 'unknown'})});
  t.after(() => plugin.dispose());
  await plugin.config({provider: {openabstractions: {npm: '@openabstractions/opencode', models: {}}}});
  const chosen = {providerID: 'openabstractions', id: 'model', api: {npm: '@openabstractions/opencode'}};
  const chat = fixtureChat(), model = await modelFixture(chat, {oaChatGuarantees: {model: [sdk.ChatGuarantee.NoRetention]}});
  for (const [agent, sessionID] of [['local', 'main-session'], ['build', 'main-session'], ['explore', 'subagent-session']]) {
    const params = {options: {keep: true, oaSessionID: 'stale'}};
    await plugin['chat.params']({agent, sessionID, parentSessionID: 'must-not-inherit', model: chosen}, params);
    assert.equal(params.options.oaSessionID, sessionID);
    assert.equal(params.options.keep, true);
    const opts = options({providerOptions: {openabstractions: params.options}});
    if (agent === 'explore') await collect((await model.doStream(opts)).stream);
    else await model.doGenerate(opts);
    const request = chat.requests.at(-1);
    assert.equal(request.conversation, sessionID);
    assert.equal(request.use ?? null, null);
    assert.ok(request.content.guarantees.includes(sdk.ChatGuarantee.NoRetention));
  }
  assert.equal(new Set(chat.requests.map(request => request.identity.key)).size, 3);
  for (const input of [{agent: 'title', sessionID: 'main-session'}, {agent: 'build'}]) {
    const params = {options: {oaSessionID: 'stale', oaTitle: true}};
    await plugin['chat.params']({...input, model: chosen}, params);
    assert.equal(params.options.oaSessionID, undefined);
    assert.equal(params.options.oaTitle, input.agent === 'title' ? true : undefined);
  }
  // A title the person routes to a concrete model is still a title: no
  // conversation, use title, and the engine keeps the chat's slot.
  const titleParams = {options: {}};
  await plugin['chat.params']({agent: 'title', sessionID: 'main-session', model: chosen}, titleParams);
  await model.doGenerate(options({providerOptions: {openabstractions: titleParams.options}}));
  assert.equal(chat.requests.at(-1).conversation, '');
  assert.equal(chat.requests.at(-1).use, 'title');
  assert.equal(chat.requests.at(-1).target.model, 'model');
  const titleChat = fixtureChat(), title = await modelFixture(titleChat, {oaSelectionPolicies: {model: 'title'}});
  await title.doGenerate(options({providerOptions: {openabstractions: {oaSessionID: 'main-session'}}}));
  assert.equal(titleChat.requests[0].conversation, '');
  assert.equal(titleChat.requests[0].use, 'title');
});

test('conversation labels use generated bounds and chat@2 keeps its unlabeled fallback', async () => {
  for (const label of ['x'.repeat(129), 'unsafe\nlabel']) {
    const chat = fixtureChat(), model = await modelFixture(chat);
    await assert.rejects(model.doGenerate(options({providerOptions: {openabstractions: {oaSessionID: label}}})), error => error.name === 'Refusal');
    assert.equal(chat.requests.length, 0);
  }
  const model = await modelFixture(fixtureChat(), {oaSelectionPolicies: {model: 'local'}}, async () => {
    throw Object.assign(new Error('chat@3 unavailable'), {name: 'ResolutionError', status: 'incompatible'});
  });
  const requests = [];
  model.chatV2 = async () => nativeChatFixture({async complete(request) {
    requests.push(request);
    return {outcome: 'completed', stopReason: 'end', usage: {}, host: 'local', model: 'model', message: {parts: [{kind: 'text', text: 'answer'}]}};
  }});
  model.placementOf = async () => 'local';
  for (const label of ['main-session', 'subagent-session']) await model.doGenerate(options({providerOptions: {openabstractions: {oaSessionID: label}}}));
  assert.equal(requests.length, 2);
  assert.deepEqual(requests[0], requests[1]);
  assert.equal(Object.hasOwn(requests[0], 'conversation'), false);
  assert.deepEqual(requests[0].content.extensions, {});
});

test('chat@3 generate keeps reasoning separate and uses actual terminal placement and optional usage', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat);
  const out = await model.doGenerate(options());
  assert.deepEqual(out.content.map(part => [part.type, part.text]), [['reasoning', 'thinking'], ['text', 'answer']]);
  assert.equal(out.content[0].providerMetadata.openabstractions.reasoningSeal.bytes, 'c2ln');
  assert.equal(out.providerMetadata.openabstractions.host, 'actual-engine');
  assert.equal(out.providerMetadata.openabstractions.placement, 'local');
  assert.equal(out.usage.inputTokens.total, undefined);
  assert.equal(out.usage.outputTokens.total, 0);
  assert.equal(chat.requests.length, 1);
});

test('a model variant reasoningEffort reaches chat@3 ChatOptions.reasoning', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat);
  await model.doGenerate(options({providerOptions: {openabstractions: {reasoningEffort: 'low', reasoningBudget: 512}}}));
  await model.doGenerate(options({providerOptions: {openabstractions: {reasoningEffort: 'max'}}}));
  await model.doGenerate(options());
  assert.deepEqual(chat.requests.map(request => request.content.options.reasoning),
    [{effort: 'low', budget: 512n}, {effort: 'high', budget: undefined}, null]);
});

test('chat@3 public reasoning stays out of history; opaque seal uses only its separate replay field', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat);
  const prompt = [{role: 'assistant', content: [
    {type: 'reasoning', text: 'never put this trace in answer history', providerOptions: {openabstractions: {
      reasoningSeal: {block: '0', origin: 'openai-compatible', bytes: 'c2ln'}}}},
    {type: 'text', text: 'previous answer'}]}, {role: 'user', content: [{type: 'text', text: 'next'}]}];
  await model.doGenerate(options({prompt}));
  const message = chat.requests[0].content.messages[0];
  assert.equal(message.parts.length, 1);
  assert.equal(message.parts[0].text, 'previous answer');
  assert.equal(message.reasoning.length, 1);
  assert.deepEqual([...message.reasoning[0].seal.bytes], [115, 105, 103]);
});

test('chat@3 stream emits actual progress and visible reasoning parts before clean answer', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat);
  const out = await model.doStream(options({includeRawChunks: true}));
  const parts = await collect(out.stream);
  assert.equal(parts[0].type, 'stream-start');
  assert.equal(parts.find(part => part.type === 'raw').rawValue.phase, 'metadata');
  assert.equal(parts.find(part => part.type === 'reasoning-delta').delta, 'thinking');
  assert.equal(parts.find(part => part.type === 'text-delta').delta, 'answer');
  const end = parts.find(part => part.type === 'text-end');
  assert.equal(end.providerMetadata.openabstractions.host, 'actual-engine');
  assert.equal(parts.at(-1).type, 'finish');
  assert.equal(chat.requests.length, 1);
  assert.equal(chat.cancels, 0);
});

test('chat@3 carries only its original local terminal timing to the exported text metadata', async () => {
  const local = terminal();
  local.timing = {load: 'cold', loadMs: 9007199254740993n};
  const missing = {...local, servedBy: null};
  const remote = {...local, servedBy: {place: 'remote', server: 'remote-engine', machine: 'paired'}};
  const noTiming = {...local, timing: null};
  for (const observed of [local, missing, remote, noTiming]) {
    const events = [...frames().slice(0, -1), event(5n, 'end', observed)];
    const chat = fixtureChat(events), model = await modelFixture(chat);
    const parts = await collect((await model.doStream(options())).stream);
    const metadata = parts.find(part => part.type === 'text-end').providerMetadata.openabstractions;
    assert.equal(metadata.operation, 'one-request');
    assert.deepEqual(metadata.chat3Terminal, observed === local
      ? {load: 'cold', loadMs: '9007199254740993', servedBy: {place: 'local', server: 'actual-engine'}}
      : undefined);
  }
});

test('chat@3 loading notices reach the plugin without raw chunks and stop at the first part', async (t) => {
  const progress = phase => event(0n, 'progress', {...sdk.newChatProgress(), phase});
  const events = [progress('queued'), progress('loading'), progress('loading'), progress('warming'),
    ...frames().slice(1, 3), progress('loading'), ...frames().slice(3)]
    .map((value, index) => ({...value, sequence: BigInt(index)}));
  const notices = [];
  availability.availability.onChange(value => notices.push(value.message));
  t.after(() => availability.availability.onChange(null));
  const model = await modelFixture(fixtureChat(events));
  const out = await model.doStream(options());
  const parts = await collect(out.stream);
  assert.deepEqual(notices, ['Loading served-model on this computer', 'Getting served-model ready']);
  assert.equal(parts.some(part => part.type === 'raw'), false);
  assert.equal(parts.find(part => part.type === 'text-delta').delta, 'answer');
  notices.length = 0;
  const warm = frames().slice(1).map((value, index) => ({...value, sequence: BigInt(index)}));
  await (await modelFixture(fixtureChat(warm))).doGenerate(options());
  assert.deepEqual(notices, []);
  const cold = [progress('loading'), progress('warming'), ...frames().slice(1)]
    .map((value, index) => ({...value, sequence: BigInt(index)}));
  await (await modelFixture(fixtureChat(cold))).doGenerate(options());
  assert.deepEqual(notices, ['Loading served-model on this computer', 'Getting served-model ready']);
});

test('chat@3 accepted observation failure retains handle and never cancels or retries', async () => {
  const chat = fixtureChat(); chat.observe = async () => { throw new Error('wait expired'); };
  const model = await modelFixture(chat);
  await assert.rejects(model.doGenerate(options()), error => error.operation === 'one-request' && error.cursor === 0n && !error.isRetryable);
  assert.equal(chat.requests.length, 1);
  assert.equal(chat.cancels, 0);
});

test('chat@3 lost Start is uncertain and never retried', async () => {
  const chat = fixtureChat(); chat.start = async function(request) { this.requests.push(request); throw new Error('reply lost'); };
  const model = await modelFixture(chat);
  await assert.rejects(model.doGenerate(options()), error => error.requestIdentity.historyEpoch === 'epoch' &&
    error.message === uncertainChat3Message && !error.isRetryable);
  assert.equal(chat.requests.length, 1);
  assert.equal(chat.cancels, 0);
});

test('chat@3 process invocation mints a separate key for identical intent bodies and sessions', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat);
  const opts = options();
  opts.providerOptions.openabstractions.requestIdentity = {key: 'previous-process-key', historyEpoch: 'old', attempt: 0n};
  await Promise.all([model.doGenerate(opts), model.doGenerate(opts), model.doGenerate(options())]);
  assert.equal(new Set(chat.requests.map(request => request.identity.key)).size, 3);
  assert.ok(chat.requests.every(request => request.identity.key !== 'previous-process-key'));
  assert.ok(chat.requests.every(request => request.identity.historyEpoch === 'epoch' && request.identity.attempt === 0n));
});

test('chat@3 explicit in-process lost Start reconciliation replays exact bytes after caller edits', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat), opts = options();
  opts.providerOptions.openabstractions.oaSessionID = 'original-session';
  const start = chat.start.bind(chat);
  let failure;
  chat.start = async function(request) {
    if (this.requests.length === 0) { this.requests.push(structuredClone(request)); throw new Error('503 network unavailable'); }
    return start(request);
  };
  try { await model.doGenerate(opts); } catch (error) { failure = error; }
  assert.equal(chat.requests.length, 1);
  assert.equal(failure.message, uncertainChat3Message);
  opts.prompt[0].content[0].text = 'edited body';
  opts.providerOptions.openabstractions.oaSessionID = 'edited-session';
  failure.requestIdentity.key = 'edited-key';
  chat.getHistoryWindow = async () => { throw new Error('replay must retain its old epoch'); };
  assert.equal((await failure.reconcile()).content.at(-1).text, 'answer');
  assert.deepEqual(chat.requests[0], chat.requests[1]);
  assert.equal(chat.requests[1].conversation, 'original-session');
});

test('chat@3 reader close explicitly cancels the same accepted request once', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat);
  const {stream} = await model.doStream(options());
  const reader = stream.getReader();
  await reader.read(); await reader.cancel();
  assert.equal(chat.requests.length, 1);
  assert.equal(chat.cancels, 1);
});

test('chat@3 preaccept permission repair reuses identity; accepted failure never retries', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat);
  const start = chat.start.bind(chat);
  chat.start = async function(request) { if (this.requests.length === 0) { this.requests.push(structuredClone(request)); return {outcome: 'not_permitted', reason: 'rights:not_granted'}; } return start(request); };
  model.answerFor = async () => 'allowed';
  await model.doGenerate(options());
  assert.equal(chat.requests.length, 2);
  assert.deepEqual(chat.requests[0], chat.requests[1]);
  const failed = fixtureChat([event(0n, 'end', terminal(undefined, 'failed', 'rights:not_granted'))]);
  const other = await modelFixture(failed); other.answerFor = async () => { throw new Error('accepted work requested a new admission'); };
  await assert.rejects(other.doGenerate(options()));
  assert.equal(failed.requests.length, 1);
});

test('chat@3 policy local-only remains an inline narrowing restriction', async () => {
  const chat = fixtureChat(), model = await modelFixture(chat, {credential: 'must-not-cross', oaSelectionPolicies: {model: 'chosen-policy'}});
  const opts = options(); opts.providerOptions.openabstractions.oaLocalOnly = true;
  await model.doGenerate(opts);
  const request = chat.requests[0];
  assert.equal(request.target.selectionPolicy, 'chosen-policy');
  assert.equal(request.content.credential, '');
  assert.deepEqual(request.content.guarantees, [sdk.RequestGuarantee.LocalOnly]);
});

test('chat@3 unknown finish completes as other and cannot produce a tool action', async () => {
  const tool = {...part(0n, 'tool_call'), callId: 'call', name: 'tool', arguments: '{}'};
  const chat = fixtureChat([event(0n, 'part', tool), event(1n, 'block_end', {block: 0n}), event(2n, 'end', terminal('future'))]);
  const model = await modelFixture(chat);
  const out = await model.doGenerate(options());
  assert.equal(out.finishReason.unified, 'other');
  assert.equal(out.content.some(part => part.type === 'tool-call'), false);
});

test('chat@3 usage never adds reasoning to total output', () => {
  const out = usageChat3({output: 10n, reasoning: 7n});
  assert.equal(out.outputTokens.total, 10);
  assert.equal(out.outputTokens.reasoning, 7);
  assert.equal(out.inputTokens.total, undefined);
});

test('pinned processor never retries uncertain chat3 dispatch; definite preaccept refusals can retry',
  {skip: !process.env.OA_OPENCODE_EFFECT_MODULE && 'selected Effect 4.0.0-beta.83 runtime is required'}, async () => {
    const {pinnedProcessor} = await import('./pinned-processor.mjs');
    const processor = await pinnedProcessor(APICallError);
    const transient = new APICallError({message: '503 service unavailable', statusCode: 503, responseBody: 'resource exhausted',
      url: '', requestBodyValues: undefined, isRetryable: false});
    assert.ok(processor.SessionRetry.retryable({name: 'APIError', data: processor.ProviderError.parseAPICallError({
      providerID: 'openabstractions', error: transient})}, 'openabstractions'), 'pinned host overrides the false flag');
    for (const scenario of ['lost-start', 'unknown-admission', 'relay-unknown', 'lost-observe', 'accepted-failed']) {
      const chat = fixtureChat();
      let admissions = 0;
      const start = chat.start.bind(chat);
      chat.start = async function(request) {
        admissions++;
        if (scenario === 'lost-start') throw Object.assign(new Error('503 service unavailable network error'),
          {statusCode: 503, responseBody: 'resource exhausted', isRetryable: true});
        if (scenario === 'unknown-admission') return {outcome: 'unknown', reason: '503 service unavailable'};
        return start(request);
      };
      if (scenario === 'lost-observe') chat.observe = async () => { throw new Error('503 connection lost'); };
      if (scenario === 'relay-unknown') chat.observe = async () => ({outcome: 'page', events: [event(0n, 'end', terminal('other', 'failed', 'remote:uncertain'))], next: 1n, atEnd: true});
      if (scenario === 'accepted-failed') chat.observe = async () => ({outcome: 'page', events: [event(0n, 'end', terminal('other', 'failed', 'upstream:unavailable'))], next: 1n, atEnd: true});
      const outcome = await processor.run(await modelFixture(chat), options());
      assert.equal(outcome.result, 'stop', scenario);
      assert.equal(admissions, 1, scenario);
      assert.equal(outcome.statuses.some(status => status.type === 'retry'), false, scenario);
      assert.equal(outcome.error.data.isRetryable, false, scenario);
      if (scenario === 'relay-unknown') assert.equal(chat.cancels, 0, scenario);
      if (scenario !== 'accepted-failed') assert.equal(outcome.error.data.message, uncertainChat3Message, scenario);
    }
    for (const outcome of ['unavailable', 'exhausted']) {
      const chat = fixtureChat(), start = chat.start.bind(chat);
      let attempts = 0;
      chat.start = async request => ++attempts === 1 ? {outcome, reason: 'capacity', retryAfterMs: 1n} : start(request);
      const result = await processor.run(await modelFixture(chat), options());
      assert.equal(result.result, 'continue', outcome);
      assert.equal(attempts, 2, outcome);
      assert.equal(chat.requests.length, 1, 'one accepted upstream admission');
      assert.equal(result.statuses.filter(status => status.type === 'retry').length, 1, outcome);
    }
    assert.equal(processor.SessionRetry.delay(1, {data: {responseHeaders: {'retry-after-ms': '17'}}}), 17);
  });

test('chat@3 generate reports actual relay uncertain terminal without another Start or Cancel', async () => {
  const chat = fixtureChat([event(0n, 'end', terminal('other', 'failed', 'remote:uncertain'))]);
  const model = await modelFixture(chat);
  await assert.rejects(model.doGenerate(options()), error => error.message === uncertainChat3Message && !error.isRetryable);
  assert.equal(chat.requests.length, 1);
  assert.equal(chat.cancels, 0);
});

// A virtual clock for the activation wait: sleep advances it at once.
function virtualClock(t) {
  const saved = {...activation};
  let now = 0;
  const slept = [];
  Object.assign(activation, {now: () => now, sleep: async ms => { slept.push(ms); now += ms; }});
  t.after(() => Object.assign(activation, saved));
  return {now: () => now, slept};
}

// R28: the owner's first OpenCode chat after a cold start met the model host
// while it loaded Qwen3.6 for 32 s. The plugin waits and starts the same
// request again until the host admits it, says once that the model is
// loading, and returns the answer with no uncertain sentence.
test('chat@3 Start onto a host activating for 40 s waits and returns the answer', async t => {
  const clock = virtualClock(t);
  for (const generate of [true, false]) {
    const chat = fixtureChat();
    const accept = chat.start.bind(chat);
    chat.start = async function(request) {
      if (clock.now() < 40000 * (generate ? 1 : 2)) { this.requests.push(structuredClone(request)); return {outcome: 'unavailable', reason: 'activating:modelhost'}; }
      return accept(request);
    };
    const said = [];
    const saySpy = availability.availability.say;
    availability.availability.say = (level, text) => { said.push(text); };
    t.after(() => { availability.availability.say = saySpy; });
    const model = await modelFixture(chat);
    let text;
    if (generate) {
      const result = await model.doGenerate(options());
      text = result.content.filter(part => part.type === 'text').map(part => part.text).join('');
    } else {
      const {stream} = await model.doStream(options());
      const parts = await collect(stream);
      assert.equal(parts.find(part => part.type === 'error'), undefined);
      text = parts.filter(part => part.type === 'text-delta').map(part => part.delta).join('');
    }
    availability.availability.say = saySpy;
    assert.equal(text, 'answer', `generate=${generate}`);
    assert.ok(!said.includes(uncertainChat3Message));
    assert.deepEqual(said.filter(line => /Loading/.test(line)), ['Loading model on this computer'], `generate=${generate}`);
    const keys = new Set(chat.requests.map(request => request.identity.key));
    assert.equal(keys.size, 1, 'every Start repeats the same request identity');
    assert.ok(chat.requests.length >= 5, `${chat.requests.length} Starts`);
  }
  assert.ok(clock.slept.every(ms => ms <= activation.maxMs), `backoff ${clock.slept}`);
});

test('chat@3 Start onto a host still launching after the wait says so in one sentence and stays retryable', async t => {
  const clock = virtualClock(t);
  const chat = fixtureChat();
  chat.start = async function(request) { this.requests.push(structuredClone(request)); return {outcome: 'unavailable', reason: 'activating:modelhost'}; };
  const model = await modelFixture(chat);
  await assert.rejects(model.doGenerate(options()), error => {
    assert.equal(error.message, 'OpenAbstractions is starting the program that runs this model; try again shortly.');
    assert.notEqual(error.message, uncertainChat3Message);
    assert.equal(error.isRetryable, true);
    return true;
  });
  assert.equal(clock.now(), activation.limitMs);
});

test('chat@3 Start refused for another reason is not waited on', async t => {
  const clock = virtualClock(t);
  const chat = fixtureChat();
  chat.start = async function(request) { this.requests.push(structuredClone(request)); return {outcome: 'unavailable', reason: 'capacity'}; };
  const model = await modelFixture(chat);
  await assert.rejects(model.doGenerate(options()), error => error.isRetryable === true);
  assert.equal(chat.requests.length, 1);
  assert.equal(clock.now(), 0);
});

test('chat@3 reasoning-only terminal names the spent output limit', async () => {
  const chat = fixtureChat([event(0n, 'part', part(0n, 'reasoning', 'thinking')), event(1n, 'block_end', {block: 0n}),
    event(2n, 'end', terminal('length', 'failed', 'upstream:reasoning_only'))]);
  const model = await modelFixture(chat);
  await assert.rejects(model.doGenerate(options()), error =>
    error.message === 'OpenAbstractions: the model spent its whole output limit thinking and wrote no answer.' && !error.isRetryable);
  assert.equal(chat.requests.length, 1);
});

// The owner's R27 chat: llama.cpp refused "request (35676 tokens) exceeds the
// available context size (32768 tokens)", the runtime ended the accepted
// chat@3 request failed with upstream:context_length_exceeded:32768:35676,
// and OpenCode showed "this request ended without an answer". The overflow
// now reads as one plain statement OpenCode classifies as a context overflow,
// which it answers by compacting the conversation.
test('chat@3 context overflow says the conversation is too long and OpenCode compacts on it', async () => {
  // The owner's model host serves the model under its file digest; a digest
  // is no name for a person.
  const digest = '55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1.gguf';
  for (const [generate, served, shown] of [[false, 'served-model', 'served-model'], [true, 'served-model', 'served-model'], [false, digest, 'the model']]) {
    const chat = fixtureChat([event(0n, 'end', terminal('other', 'failed', 'upstream:context_length_exceeded:32768:35676'))]);
    const start = chat.start.bind(chat);
    chat.start = async request => ({...await start(request), model: served});
    const model = await modelFixture(chat);
    let error;
    if (generate) {
      await assert.rejects(model.doGenerate(options()), caught => { error = caught; return true; });
    } else {
      const {stream} = await model.doStream(options());
      error = (await collect(stream)).find(part => part.type === 'error')?.error;
    }
    assert.ok(error, `generate=${generate}`);
    assert.equal(error.message, `OpenAbstractions: this conversation is longer than ${shown} can hold. ` +
      'It exceeds the available context size of 32,768 tokens. Start a new session or compact it.');
    assert.equal(error.context.window, 32768);
    assert.equal(error.context.input, 35676);
    assert.equal(error.isRetryable, false);
    assert.ok(openCodeSeesOverflow(error), 'OpenCode classifies it as a context overflow');
    assert.equal(chat.requests.length, 1);
  }
});
