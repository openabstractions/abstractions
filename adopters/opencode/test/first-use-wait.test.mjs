import {FixtureMachine, nativeChatFixture} from './native-chat-fixture.mjs';
import assert from 'node:assert/strict';
import * as availability from '../availability.js';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {APICallError} from '@ai-sdk/provider';
import {Fold} from '@openabstractions/inference';
import {waitForPermission, invokeWithPermission, permissionWaitMs} from '../permission-wait.js';

test('the unresolved 90 second permission wait points to Panel Questions without replay', async t => {
  let now=1000,invocations=0;const waits=[];
  t.mock.method(Date,'now',()=>now);
  const result=await invokeWithPermission(async()=>{invocations++;return {outcome:'forbidden',reason:'rights:not_granted rights:question_pending:retained-question'};},{
    client:{async observeQuestion(id,wait){assert.equal(id,'retained-question');waits.push(Number(wait));now+=Number(wait);return {observation:{outcome:'pending'}};}},
  });
  assert.equal(now,1000+permissionWaitMs);
  assert.deepEqual(waits,[25000,25000,25000,15000]);assert.equal(invocations,1);
  assert.equal(result.outcome,'awaiting_permission');assert.deepEqual(result.question_ids,['retained-question']);
  assert.match(result.message,/Questions in the OpenAbstractions Panel/);
});

// Production consumer with inert native calls; no runtime or model is opened.
async function fixture(chat, config = {}, modelId = 'fixture') {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const permissionSource = await readFile(new URL('../permission-wait.js', import.meta.url), 'utf8');
  const body = permissionSource.replace(/^export /gm, '') + '\n' + source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'runtimeConnector', 'DeltaKind', 'Fold', 'PartKind', 'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {create: createOpenAbstractions, resolveObservedChat, questionVerdict};`);
  const {create, resolveObservedChat, questionVerdict} = make(availability.availability, availability.runtimeUnavailable, availability.runtimeUntrusted, availability.unavailableMessage, availability.untrustedMessage, FixtureMachine, APICallError, null, null, null,
    {Part: 'part', End: 'end', Usage: 'usage'}, Fold, {Text: 'text'}, {Completed: 'completed'}, {LocalOnly: 'local-only'}, {}, {End: 'end'});
  const model = create({firstUsePollMs: 1, firstUseWaitMs: 20, ...config}).languageModel(modelId);
  const state = {opens: 0, polls: 0};
  model.chat = async (options) => { state.opens++; state.signal = options.abortSignal; return nativeChatFixture(chat(options)); };
  model.servers = async () => ['fixture'];
  model.decider = async () => async () => { state.polls++; return {outcome: 'permitted'}; };
  return {model, state, resolveObservedChat, questionVerdict};
}
const pending = {outcome: 'not_permitted', reason: 'rights:not_granted', host: '', model: '', usage: {}};
const complete = {outcome: 'completed', stopReason: 'end', usage: {}, message: {parts: [{kind: 'text', text: 'hello'}]}};

test('a saved preference routes its named refusal through the bounded permission wait', async (t) => {
  let now = 1000;
  t.mock.method(Date, 'now', () => now);
  const {model} = await fixture(() => ({}), {
    oaSelectionPolicies: {'oa-preference-local': 'local-first'},
  }, 'oa-preference-local');
  const requests = [], waits = [];
  model.chat = async () => { throw new Error('a preference must use policy routing'); };
  model.chatV2 = async () => ({async start(request) {
    requests.push(request);
    return {outcome: 'not_permitted', reason: 'rights:not_granted rights:question_pending:policy-question'};
  }});
  model.awaitQuestions = (ids) => waitForPermission(ids, {
    deadline: now + 20,
    client: {async observeQuestion(id, wait) {
      assert.equal(id, 'policy-question');
      waits.push(wait);
      now += Number(wait);
      return {observation: {outcome: 'pending'}};
    }},
  });
  await assert.rejects(model.doGenerate({prompt: []}), /still waiting for your answer.*Questions in the OpenAbstractions Panel/);
  assert.equal(requests.length, 1);
  assert.deepEqual(requests[0].target, {model: '', selectionPolicy: 'local-first'});
  assert.deepEqual(waits, [20n]);
  assert.equal(now, 1020);
});
test('the Default row routes by its API id to the app\'s own chat policy', async () => {
  const {model} = await fixture(() => ({}), {
    oaSelectionPolicies: {'oa-chat': 'oa-chat-default'},
    oaRouteModels: {'Default (Qwen3.6 35B)': 'oa-chat'},
  }, 'Default (Qwen3.6 35B)');
  const requests = [];
  model.chat = async () => { throw new Error('the Default row must use policy routing'); };
  model.chatV2 = async () => ({async start(request) {
    requests.push(request);
    return {outcome: 'unavailable', reason: 'fixture'};
  }});
  await assert.rejects(model.doGenerate({prompt: []}));
  assert.deepEqual(requests[0].target, {model: '', selectionPolicy: 'oa-chat-default'});
});
test('a Default row renamed after the options were fixed still routes to the app\'s own chat policy', async () => {
  // OpenCode keeps the SDK options from the config pass while a later list
  // read renames the Default row after the Panel's new default.
  const config = {oaSelectionPolicies: {'oa-chat': 'P'}, oaRouteModels: {'Default (qwen)': 'oa-chat'}};
  const {model} = await fixture(() => ({}), config, 'Default (gemma)');
  const requests = [];
  model.chat = async () => { throw new Error('the Default row must use policy routing'); };
  model.chatV2 = async () => ({async start(request) {
    requests.push(request);
    return {outcome: 'unavailable', reason: 'fixture'};
  }});
  await assert.rejects(model.doGenerate({prompt: []}));
  assert.deepEqual(requests[0].target, {model: '', selectionPolicy: 'P'});
  // Its chat@3 guarantees are the route's, looked up through the same mapping.
  const guarded = (await fixture(() => ({}), {...config, oaChatGuarantees: {'oa-chat': ['final']}}, 'Default (gemma)')).model;
  await assert.rejects(guarded.doGenerate({prompt: []}), /requires chat@3 guarantees/);
  // Without the app's own policy a "Default (…)" id names itself.
  const plain = (await fixture(() => ({}), {oaSelectionPolicies: {}}, 'Default (gemma)')).model;
  plain.chatV2 = async () => { throw new Error('a plain model must not use policy routing'); };
  await assert.rejects(plain.doGenerate({prompt: []}), (error) => !/policy routing/.test(String(error?.message)));
});
test('native permission observation agrees with shared gateway corpus', async () => {
  const corpus = JSON.parse(await readFile(new URL('../../../mcp-gateway/gateway/permission_observation_cases.json', import.meta.url), 'utf8'));
  const {questionVerdict} = await fixture(() => ({}));
  for (const row of corpus.observations) {
    const verdict = questionVerdict({outcome: row.outcome, policyRevision: row.revision, answer: {option: row.option, pending: row.answer_pending === true}});
    assert.equal(verdict === 'pending' ? 'waiting' : verdict, row.want, row.name);
  }
});
function deferred() { let resolve; const promise = new Promise((r) => { resolve = r; }); return {promise, resolve}; }
async function collect(stream) { const output = []; for await (const event of stream) output.push(event); return output; }

test('cold load keeps Working until the first actual answer without retry', async () => {
  const loaded = deferred();
  const {model, state} = await fixture(() => ({async *stream() {
    await loaded.promise;
    yield {kind: 'part', index: 0n, part: {kind: 'text', text: 'hello'}};
    yield {kind: 'end', end: complete};
  }}));
  const {stream} = await model.doStream({prompt: []});
  const reader = stream.getReader();
  assert.equal((await reader.read()).value.type, 'stream-start');
  let answered = false;
  const next = reader.read().then((result) => { answered = true; return result; });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(answered, false);
  assert.equal(state.opens, 1);
  loaded.resolve();
  assert.equal((await next).value.type, 'text-start');
  const events = [];
  for (;;) { const result = await reader.read(); if (result.done) break; events.push(result.value); }
  assert.equal(events.find((event) => event.type === 'text-delta').delta, 'hello');
  assert.equal(events.at(-1).type, 'finish');
  assert.equal(state.opens, 1);
  assert.equal(state.polls, 0);
});

test('an observation deadline retains the accepted operation without Cancel or another Start', async () => {
  let starts = 0, cancels = 0;
  const {model} = await fixture(() => ({
    async start() { starts++; return {outcome: 'accepted', operation: 'existing-operation'}; },
    async observe() { throw new Error('observation deadline'); },
    async cancel() { cancels++; },
  }));
  await assert.rejects(model.doGenerate({prompt: []}), (error) => error.operation === 'existing-operation' && !error.isRetryable);
  assert.equal(starts, 1);
  assert.equal(cancels, 0);
});

test('explicit Cancel detaches observation cancellation on the same resolved binding', async () => {
  const {resolveObservedChat} = await fixture(() => ({}));
  const abort = new AbortController();
  let resolutions = 0, cancels = 0;
  const binding = {endpoint: 'same-endpoint', waiting: {cancellation: abort.signal},
    withWaiting(waiting) { return {...this, waiting}; }};
  const machine = {async resolveService() { resolutions++; return binding; }};
  const resolve = async (owner) => {
    const retained = await owner.resolveService('abstraction.inference/chat@1');
    return {async cancel() {
      assert.equal(retained.endpoint, binding.endpoint);
      assert.equal(retained.waiting.cancellation, null);
      cancels++;
    }};
  };
  const chat = await resolveObservedChat(machine, resolve, 'abstraction.inference/chat@1', {scope: 'local'});
  abort.abort();
  await chat.cancel('operation');
  assert.equal(resolutions, 1);
  assert.equal(cancels, 1);
});

test('automatic permission retry requires an affirmative retained observation', async () => {
  const {questionVerdict} = await fixture(() => ({}));
  for (const outcome of ['unknown', 'unavailable', 'gone', 'invalid', 'conflict', 'forbidden']) {
    assert.equal(questionVerdict({outcome, answer: {option: 'allow'}}), 'waiting', outcome);
  }
  assert.equal(questionVerdict({outcome: 'pending', answer: {option: 'allow'}}), 'pending');
  assert.equal(questionVerdict({outcome: 'answered', answer: {option: 'allow'}}), 'waiting');
  assert.equal(questionVerdict({outcome: 'answered', answer: {option: 'refuse'}}), 'refused');
  assert.equal(questionVerdict({outcome: 'answered', answer: {option: 'never'}}), 'blocked');
  assert.equal(questionVerdict({outcome: 'permitted'}), 'waiting');
  assert.equal(questionVerdict({outcome: 'permitted', policyRevision: 'current', answer: {option: 'never'}}), 'allowed');
});

test('a permitted policy observation remains allowed with an answer attached', async () => {
  const {questionVerdict} = await fixture(() => ({}));
  for (const option of ['allow', 'never']) {
    assert.equal(questionVerdict({outcome: 'permitted', policyRevision: 'current', answer: {option}}), 'allowed', option);
  }
});

test('Allow completes the same pending native generate admission', async () => {
  const allow = deferred();
  const {model, state} = await fixture(() => ({async complete() { await allow.promise; return complete; }}));
  let settled = false;
  const result = model.doGenerate({prompt: []}).finally(() => { settled = true; });
  await new Promise((r) => setImmediate(r));
  assert.equal(settled, false);
  allow.resolve();
  assert.equal((await result).content[0].text, 'hello');
  assert.equal(state.opens, 1);
  assert.equal(state.polls, 0);
});

test('native terminal outcomes retain their reason without rights polling or resend', async () => {
  for (const reply of [pending, {...pending, reason: 'rights:denied'}, {...pending, reason: 'rights:unknown_action'}, {...pending, outcome: 'unavailable', reason: 'deadline'}, {...pending, outcome: 'exhausted', reason: 'capacity'}, {...pending, outcome: 'forbidden', reason: 'output:forbidden'}, {...pending, reason: 'credential:not_permitted'}]) {
    const {model, state} = await fixture(() => ({async complete() { return reply; }}));
    await assert.rejects(model.doGenerate({prompt: []}), (e) => e.outcome === reply.outcome && e.reason === reply.reason && e.isRetryable === false);
    assert.equal(state.opens, 1, JSON.stringify(reply));
    assert.equal(state.polls, 0, JSON.stringify(reply));
  }
});

test('caller abort reaches pending generate and ambiguous failure never resends', async () => {
  const entered = deferred();
  const {model, state} = await fixture(({abortSignal}) => ({complete() {
    entered.resolve();
    return new Promise((resolve, reject) => abortSignal.addEventListener('abort', () => reject(new Error('caller closed')), {once: true}));
  }}));
  const controller = new AbortController();
  const result = model.doGenerate({prompt: [], abortSignal: controller.signal});
  await entered.promise;
  controller.abort();
  await assert.rejects(result);
  assert.equal(state.opens, 1);
});

test('native pending stream completes after Allow in one stream', async () => {
  const allow = deferred();
  const {model, state} = await fixture(() => ({async *stream() {
    await allow.promise;
    yield {kind: 'part', index: 0n, part: {kind: 'text', text: 'hello'}};
    yield {kind: 'end', end: complete};
  }}));
  const result = collect((await model.doStream({prompt: []})).stream);
  allow.resolve();
  assert.equal((await result).find((e) => e.type === 'text-delta').delta, 'hello');
  assert.equal(state.opens, 1);
  assert.equal(state.polls, 0);
});

test('native pending refusal ends stream without rights polling or reopen', async () => {
  for (const reply of [pending, {...pending, reason: 'rights:denied'}, {...pending, outcome: 'unavailable', reason: 'deadline'}]) {
    const {model, state} = await fixture(() => ({async *stream() { yield {kind: 'end', end: reply}; }}));
    const output = await collect((await model.doStream({prompt: []})).stream);
    const error = output.find((e) => e.type === 'error').error;
    assert.equal(error.reason, reply.reason);
    assert.equal(error.outcome, reply.outcome);
    assert.equal(state.opens, 1);
    assert.equal(state.polls, 0);
  }
});

test('reader close aborts and joins pending native stream', {timeout: 1000}, async () => {
  for (const rejectOnClose of [false, true]) {
  const entered = deferred();
  let joined = false;
  const {model, state} = await fixture(({abortSignal}) => ({async *stream() {
    try {
      entered.resolve();
      await new Promise((resolve, reject) => abortSignal?.addEventListener('abort', () => rejectOnClose ? reject(new Error('caller closed')) : resolve(), {once: true}));
    }
    finally { joined = true; }
  }}));
  const reader = (await model.doStream({prompt: []})).stream.getReader();
  await reader.read();
  const reading = reader.read();
  await entered.promise;
  await reader.cancel();
  await reading;
  assert.equal(state.signal.aborted, true);
  assert.equal(joined, true);
  assert.equal(state.opens, 1);
  }
});

// Owner, 2026-11-01: a refusal for a pending question on the stream path
// read "could not complete this model call (cause: Error: exchange)". A
// refusal the stream throws, when it opens or mid-stream, keeps its outcome
// and reason and names the Panel's waiting question; a transport failure
// keeps its own wording.
test('a thrown rights refusal on the stream path names the waiting question', async () => {
  const reason = 'rights:not_granted rights:question_pending:q-stream';
  const refusal = () => Object.assign(new Error(reason), {name: 'ServiceError', code: 'not_permitted'});
  const opening = await fixture(() => ({stream() { throw refusal(); }}));
  await assert.rejects(opening.model.doStream({prompt: []}), (e) => /still waiting for your answer/.test(e.message) &&
    !e.message.includes('q-stream') && e.outcome === 'not_permitted' && e.reason === reason && e.isRetryable === false);
  const midStream = await fixture(() => ({async *stream() { throw refusal(); }}));
  const output = await collect((await midStream.model.doStream({prompt: []})).stream);
  const error = output.find((e) => e.type === 'error').error;
  assert.match(error.message, /still waiting for your answer/);
  assert.equal(error.message.includes('q-stream'), false);
  assert.equal(error.reason, reason);
  const noQuestion = await fixture(() => ({stream() { throw Object.assign(new Error('rights:not_granted'), {code: 'not_permitted'}); }}));
  await assert.rejects(noQuestion.model.doStream({prompt: []}), (e) => /no question is waiting/.test(e.message)
    && e.reason === 'rights:not_granted' && e.isRetryable === false);
  const denied = await fixture(() => ({stream() { throw Object.assign(new Error('rights:denied'), {code: 'forbidden'}); }}));
  await assert.rejects(denied.model.doStream({prompt: []}), /Permissions page/);
  const transport = await fixture(() => ({stream() { throw Object.assign(new Error('exchange'), {status: 7}); }}));
  await assert.rejects(transport.model.doStream({prompt: []}), (e) => !/A question is waiting/.test(e.message) && e.reason === '');
});

// Owner, 2026-11-01: OpenCode's log read "cause: Error: open" for title calls
// refused before their exchange. The cause names the native stage and its
// IPC status, and keeps the status for the classifiers.
test('a transport failure names its stage and IPC status', async () => {
  const frame = () => Object.assign(new Error('open'), {name: 'FrameError', status: 1, transferred: 0});
  const {model} = await fixture(() => ({stream() { throw frame(); }}));
  await assert.rejects(model.doStream({prompt: []}), (e) => e.cause?.message === 'open: ipc status 1' &&
    e.cause.status === 1 && e.cause.transferred === 0 && e.cause.cause?.message === 'open' && e.reason === '');
});

test('pending, blocked and unknown reasons keep existing guidance', async () => {
  for (const [reason, pattern] of [['rights:not_granted', /no question is waiting/], ['rights:denied', /Permissions page/], ['rights:unknown_action', /request outcome/]]) {
    const {model} = await fixture(() => ({async complete() { return {...pending, reason}; }}));
    await assert.rejects(model.doGenerate({prompt: []}), pattern);
  }
});

const asking = {...pending, reason: 'rights:not_granted rights:question_pending:q1'};

test('an accepted terminal refusal retains its operation after a permission observation', async () => {
  for (const verdict of ['allowed']) {
    let replies = [asking, complete];
    const {model, state} = await fixture(() => ({async complete() { return replies.shift(); }}));
    const waited = [];
    model.awaitQuestions = async (ids) => { waited.push(ids); return verdict; };
    await assert.rejects(model.doGenerate({prompt: []}), /still waiting for your answer/);
    assert.deepEqual(waited, [['q1']]);
    assert.equal(state.opens, 1);

    replies = [asking, complete];
    const streamed = await fixture(() => ({async *stream() { yield {kind: 'end', end: replies.shift()}; }}));
    streamed.model.awaitQuestions = async () => verdict;
    const output = await collect((await streamed.model.doStream({prompt: []})).stream);
    assert.equal(output.some((e) => e.type === 'error'), true, verdict);
    assert.equal(streamed.state.opens, 1);
  }
});

test('a question still unanswered, refused or blocked ends the call with one sentence and no resend', async () => {
  for (const [verdict, pattern] of [['waiting', /still waiting for your answer/], ['retry', /still waiting for your answer/], ['refused', /ask again in 10 minutes/], ['blocked', /Permissions page/]]) {
    const {model, state} = await fixture(() => ({async complete() { return asking; }}));
    model.awaitQuestions = async () => verdict;
    await assert.rejects(model.doGenerate({prompt: []}), (e) => pattern.test(e.message) && e.reason === asking.reason);
    assert.equal(state.opens, 1, verdict);

    const streamed = await fixture(() => ({async *stream() { yield {kind: 'end', end: asking}; }}));
    streamed.model.awaitQuestions = async () => verdict;
    const output = await collect((await streamed.model.doStream({prompt: []})).stream);
    assert.match(output.find((e) => e.type === 'error').error.message, pattern);
    assert.equal(streamed.state.opens, 1, verdict);
  }
});

test('a failed wait reads as still waiting', async () => {
  const {model} = await fixture(() => ({async complete() { return asking; }}));
  model.awaitQuestions = async () => { throw new Error('exchange'); };
  await assert.rejects(model.doGenerate({prompt: []}), /still waiting for your answer/);
});

test('closing the stream stops the wait for the person', {timeout: 2000}, async () => {
  const {model, state} = await fixture(() => ({async *stream() { yield {kind: 'end', end: asking}; }}));
  const waiting = deferred();
  let stopped = false;
  model.awaitQuestions = (ids, options) => new Promise((resolve) => {
    waiting.resolve();
    options.abortSignal.addEventListener('abort', () => { stopped = true; resolve('waiting'); }, {once: true});
  });
  const reader = (await model.doStream({prompt: []})).stream.getReader();
  await reader.read();
  const reading = reader.read();
  await waiting.promise;
  await reader.cancel();
  await reading;
  assert.equal(stopped, true);
  assert.equal(state.opens, 1);
});
test('a shared load has one plain sentence and keeps its typed reason', async () => {
  for (const reason of ['engine:loading', 'native:engine:loading']) {
    const {model} = await fixture(() => ({async complete() { return {outcome: 'unavailable', reason}; }}));
    await assert.rejects(model.doGenerate({prompt: []}), (error) => {
      assert.equal(error.message, 'OpenAbstractions is loading this model; try again shortly.');
      assert.equal(error.reason, reason);
      return true;
    });
  }
});
