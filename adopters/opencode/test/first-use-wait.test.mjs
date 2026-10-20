import assert from 'node:assert/strict';
import * as oaAvailability from '../availability.js';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {APICallError} from '@ai-sdk/provider';
import {Fold} from '@openabstractions/inference';

// The production provider with its package imports replaced, as in
// tool-call-output.test.mjs; the rights decision point and the router's
// servers for the model are fakes.
// decisions repeat their last answer, or, with exhausted set, answer
// exhausted once they run out, so a loop that keeps asking ends.
async function modelWith(chats, decisions, servers = ['ollama'], exhausted = null, serverSnapshot = null) {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '')
    .replaceAll("await import('@openabstractions/router')", '({RouterClient: FakeRouterClient})');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', 'FakeRouterClient', `${body}\nreturn {createOpenAbstractions};`);
  const {createOpenAbstractions} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => { throw new Error('unexpected'); },
    async () => { throw new Error('unexpected chat@2 resolution'); }, class {}, {Part: 'part', End: 'end', Usage: 'usage'}, Fold,
    {Text: 'text', ToolCall: 'tool_call'}, {Completed: 'completed'}, {LocalOnly: 'local-only'}, {}, {ToolCalls: 'tool_calls'},
    class { async offer() { return serverSnapshot; } });
  const model = createOpenAbstractions({connector: {}, firstUsePollMs: 1, firstUseWaitMs: 2000}).languageModel('qwen3');
  const asked = [];
  let opened = 0;
  model.chat = async () => chats[Math.min(opened++, chats.length - 1)];
  if (serverSnapshot === null) model.servers = async () => servers;
  else model.machine = async () => ({resolveService: async () => ({})});
  model.decider = async () => async (action, resource) => {
    asked.push([action, resource]);
    if (exhausted !== null) return {outcome: decisions.length ? decisions.shift() : exhausted, policyRevision: 'r'};
    return {outcome: decisions.length > 1 ? decisions.shift() : decisions[0], policyRevision: 'r'};
  };
  return {model, asked, opens: () => opened};
}

// A refusal names no host or model (inference: every refusal's are empty).
const pendingQuestion = {outcome: 'not_permitted', reason: 'rights:not_granted', host: '', model: '', usage: {}};
const completed = {outcome: 'completed', stopReason: 'end', usage: {}, message: {parts: [{kind: 'text', text: 'hello'}]}};

test('server discovery checks the typed router outcome before considering supplied servers', async () => {
  const offers = [{family: 'qwen3', names: ['qwen3'], servers: ['must not be consumed']}];
  for (const outcome of ['not_permitted', 'forbidden', 'unavailable', 'invalid', undefined]) {
    const {model} = await modelWith([], [], [], null, {outcome, offers});
    await assert.rejects(model.servers({}), (error) => error.code === (outcome ?? 'invalid_result'));
  }
  const {model} = await modelWith([], [], [], null, {outcome: 'offered', offers});
  assert.deepEqual(await model.servers({}), ['must not be consumed']);
});

test('a call refused for a pending question waits, then completes once the person allows it', async () => {
  let calls = 0;
  const chat = {async complete() { return calls++ === 0 ? pendingQuestion : completed; }};
  const {model, asked} = await modelWith([chat], ['not_granted', 'not_granted', 'permitted']);
  const result = await model.doGenerate({prompt: []});
  assert.deepEqual(result.content.map(({type, text}) => ({type, text})), [{type: 'text', text: 'hello'}]);
  assert.equal(calls, 2, 'one call after the answer, no retry by the person');
  assert.deepEqual(asked[0], ['abstraction.inference/complete', 'host:ollama']);
  assert.equal(asked.length, 3, 'the wait reads only its own decision until it permits');
});

test('the wait ends at its bound with the existing refusal, and the call is not repeated', async () => {
  let calls = 0;
  const chat = {async complete() { calls++; return pendingQuestion; }};
  const {model} = await modelWith([chat], ['not_granted']);
  model.config.firstUseWaitMs = 30;
  const started = Date.now();
  await assert.rejects(model.doGenerate({prompt: []}), (error) => {
    assert.match(error.message, /A question is waiting in the OpenAbstractions Panel; allow it there and retry\./);
    assert.equal(error.reason, 'rights:not_granted');
    return true;
  });
  assert.equal(calls, 1);
  assert.ok(Date.now() - started >= 25, 'the call waited for the bound');
});

test('a denied or unknown refusal, or a model no server offers, does not wait', async () => {
  for (const reply of [{...pendingQuestion, reason: 'rights:denied'}, {...pendingQuestion, reason: 'rights:unknown'},
    {...pendingQuestion, outcome: 'unavailable'}]) {
    const {model, asked} = await modelWith([{async complete() { return reply; }}], ['permitted']);
    await assert.rejects(model.doGenerate({prompt: []}));
    assert.equal(asked.length, 0, `waited on ${JSON.stringify(reply)}`);
  }
  const {model, asked} = await modelWith([{async complete() { return pendingQuestion; }}], ['permitted'], []);
  await assert.rejects(model.doGenerate({prompt: []}), /allow it there and retry/);
  assert.equal(asked.length, 0, 'waited with no server to wait on');
});

test('with several servers the wait ends when any permits, and ends at once when every one is denied', async () => {
  let calls = 0;
  const chat = {async complete() { return calls++ === 0 ? pendingQuestion : completed; }};
  const allowed = await modelWith([chat], ['not_granted', 'permitted'], ['lemonade', 'ollama']);
  assert.deepEqual((await allowed.model.doGenerate({prompt: []})).content.map(({type, text}) => ({type, text})), [{type: 'text', text: 'hello'}]);
  assert.deepEqual(allowed.asked, [['abstraction.inference/complete', 'host:lemonade'], ['abstraction.inference/complete', 'host:ollama']]);
  const denied = await modelWith([{async complete() { return pendingQuestion; }}], ['denied'], ['lemonade', 'ollama']);
  await assert.rejects(denied.model.doGenerate({prompt: []}), /allow it there and retry/);
  assert.equal(denied.asked.length, 2);
});

test('a denial during the wait ends it at once with the existing refusal', async () => {
  const {model, asked} = await modelWith([{async complete() { return pendingQuestion; }}], ['not_granted', 'denied']);
  await assert.rejects(model.doGenerate({prompt: []}), /allow it there and retry/);
  assert.equal(asked.length, 2);
});

test('cancelling OpenCode ends the wait without another call', async () => {
  let calls = 0;
  const {model} = await modelWith([{async complete() { calls++; return pendingQuestion; }}], ['not_granted']);
  model.config.firstUseWaitMs = 60000;
  model.config.firstUsePollMs = 50;
  const controller = new AbortController();
  setTimeout(() => controller.abort(), 20);
  const started = Date.now();
  await assert.rejects(model.doGenerate({prompt: [], abortSignal: controller.signal}), /allow it there and retry/);
  assert.equal(calls, 1);
  assert.ok(Date.now() - started < 5000);
});

test('a stream refused for a pending question opens again once allowed and streams the answer', async () => {
  const refused = {async *stream() { yield {kind: 'end', end: pendingQuestion}; }};
  const answered = {async *stream() {
    yield {kind: 'part', index: 0n, part: {kind: 'text', text: 'hel'}};
    yield {kind: 'part', index: 0n, part: {kind: 'text', text: 'lo'}};
    yield {kind: 'end', end: {outcome: 'completed', stopReason: 'end', usage: {}}};
  }};
  const {model, opens} = await modelWith([refused, answered], ['not_granted', 'permitted']);
  const {stream} = await model.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.equal(opens(), 2);
  assert.equal(events.find((event) => event.type === 'error'), undefined);
  assert.equal(events.filter((event) => event.type === 'text-delta').map((event) => event.delta).join(''), 'hello');
});

test('a stream whose wait times out streams the original refusal', async () => {
  const refused = {async *stream() { yield {kind: 'end', end: pendingQuestion}; }};
  const {model, opens} = await modelWith([refused], ['not_granted']);
  model.config.firstUseWaitMs = 20;
  const {stream} = await model.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.equal(opens(), 1);
  assert.match(events.find((event) => event.type === 'error').error.message, /A question is waiting/);
});

test('cancelling a stream ends its wait without opening it again', async () => {
  const refused = {async *stream() { yield {kind: 'end', end: pendingQuestion}; }};
  const {model, opens, asked} = await modelWith([refused], ['not_granted']);
  model.config.firstUseWaitMs = 60000;
  model.config.firstUsePollMs = 20;
  const {stream} = await model.doStream({prompt: []});
  const reader = stream.getReader();
  await reader.read(); // stream-start
  const reading = reader.read();
  while (asked.length === 0) await new Promise((resolve) => setTimeout(resolve, 5));
  const started = Date.now();
  await reader.cancel();
  await reading;
  assert.ok(Date.now() - started < 5000);
  assert.equal(opens(), 1);
});

test('only a not_granted refusal says a question is waiting; a denial names the Permissions page', async () => {
  const words = async (reason) => {
    const {model} = await modelWith([{async complete() { return {...pendingQuestion, reason}; }}], ['denied'], []);
    return model.doGenerate({prompt: []}).then(() => '', (error) => error.message);
  };
  assert.match(await words('rights:not_granted'), /A question is waiting in the OpenAbstractions Panel; allow it there and retry\./);
  const denied = await words('rights:denied');
  assert.doesNotMatch(denied, /question is waiting/);
  assert.match(denied, /blocked from doing that\. To change that, allow it on the Permissions page of the OpenAbstractions Panel\./);
  const unknown = await words('rights:unknown_action');
  assert.doesNotMatch(unknown, /question is waiting|Permissions page/);
});

// The decisions run out into a denial and the test has a deadline, so a
// resend that turned into a loop fails here instead of hanging npm test.
test('a call refused again after the allow is not sent a third time', {timeout: 10000}, async () => {
  let calls = 0;
  const chat = {async complete() { calls++; return pendingQuestion; }};
  const {model, asked} = await modelWith([chat], ['not_granted', 'permitted'], ['ollama'], 'denied');
  await assert.rejects(model.doGenerate({prompt: []}), /A question is waiting in the OpenAbstractions Panel; allow it there and retry\./);
  assert.equal(calls, 2, 'one call, the wait, one call after the allow, and no third');
  assert.equal(asked.length, 2, 'the second refusal does not wait again');

  const refused = {async *stream() { yield {kind: 'end', end: pendingQuestion}; }};
  const streamed = await modelWith([refused, refused, refused], ['not_granted', 'permitted'], ['ollama'], 'denied');
  const {stream} = await streamed.model.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.equal(streamed.opens(), 2, 'the stream opens once more after the allow, and no third time');
  assert.match(events.find((event) => event.type === 'error').error.message, /A question is waiting/);
});
