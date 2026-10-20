import {FixtureMachine, nativeChatFixture} from './native-chat-fixture.mjs';
import assert from 'node:assert/strict';
import * as oaAvailability from '../availability.js';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {APICallError} from '@ai-sdk/provider';
import {Fold} from '@openabstractions/inference';
import {openCodeSeesOverflow} from './opencode-overflow.mjs';

// Load the production provider with only its package imports replaced, as
// tool-call-output.test.mjs does, so no OpenAbstractions service is needed.
async function modelWith(chat) {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const permissionSource = await readFile(new URL('../permission-wait.js', import.meta.url), 'utf8');
  const body = permissionSource.replace(/^export /gm, '') + '\n' + source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions, OAInferenceError};`);
  const {createOpenAbstractions: create, OAInferenceError} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, FixtureMachine, APICallError, async () => nativeChatFixture(chat),
    async () => { throw new Error('unexpected chat@2 resolution'); }, class {}, {Part: 'part', End: 'end', Usage: 'usage'}, Fold,
    {Text: 'text', ToolCall: 'tool_call'}, {Completed: 'completed'}, {LocalOnly: 'local-only'}, {}, {ToolCalls: 'tool_calls'});
  const model = create({connector: {}}).languageModel('qwen');
  model.chat = async () => nativeChatFixture(chat);
  return {model, OAInferenceError};
}

function refusal(reason, fields = {}) {
  return {outcome: 'refused', reason, host: 'lemonade', model: 'Qwen3-0.6B-GGUF', usage: {input: 0n, output: 0n, cached: 0n}, ...fields};
}

async function streamError(reply, options = {}) {
  const {model} = await modelWith({async *stream() { yield {kind: 'end', end: reply}; }});
  const {stream} = await model.doStream({prompt: [], ...options});
  const events = [];
  for await (const event of stream) events.push(event);
  return events.find((event) => event.type === 'error')?.error;
}

test('a first-turn overflow says so plainly and keeps the loaded context and the counted input', async () => {
  const error = await streamError(refusal('upstream:context_length_exceeded:4096:8219'), {maxOutputTokens: 1024});
  assert.ok(APICallError.isInstance(error));
  assert.equal(error.outcome, 'refused');
  assert.equal(error.reason, 'upstream:context_length_exceeded:4096:8219');
  assert.deepEqual(error.context, {window: 4096, input: 8219, reserve: 1024});
  assert.equal(error.message,
    'OpenAbstractions: this conversation is longer than Qwen3-0.6B-GGUF can hold. ' +
    'It exceeds the available context size of 4,096 tokens. Start a new session or compact it.');
  assert.equal(error.isRetryable, false);
  assert.ok(openCodeSeesOverflow(error));
});

test('the answer reserve comes from the request and a reserve that fills the context says so', async () => {
  const defaulted = await streamError(refusal('upstream:context_length_exceeded:8192:9000'));
  assert.deepEqual(defaulted.context, {window: 8192, input: 9000, reserve: 4096});
  assert.doesNotMatch(defaulted.message, /reserve/);

  const full = await streamError(refusal('upstream:context_length_exceeded:4096:120'), {maxOutputTokens: 4096});
  assert.match(full.message, /The 4,096-token answer reserve fills the whole context; lower this model's output limit\./);
  assert.ok(openCodeSeesOverflow(full));
});

test('an overflow with unknown numbers still reads as an overflow with the same remedy', async () => {
  const error = await streamError(refusal('upstream:context_length_exceeded:0:0', {model: ''}));
  assert.equal(error.message,
    'OpenAbstractions: this conversation is longer than the model can hold. ' +
    'It exceeds the available context size. Start a new session or compact it.');
  assert.deepEqual(error.context, {window: 0, input: 0, reserve: 4096});
  assert.ok(openCodeSeesOverflow(error));
});

test('doGenerate surfaces the same overflow message', async () => {
  const {model, OAInferenceError} = await modelWith({complete: async () => refusal('upstream:context_length_exceeded:4096:8219')});
  await assert.rejects(model.doGenerate({prompt: [], maxOutputTokens: 1024}), (error) => {
    assert.ok(error instanceof OAInferenceError);
    assert.equal(error.op, 'generate');
    assert.match(error.message, /longer than Qwen3-0\.6B-GGUF can hold\. It exceeds the available context size of 4,096 tokens\./);
    assert.ok(openCodeSeesOverflow(error));
    return true;
  });
});

test('overflow errors carry no upstream detail, status or body', async () => {
  const error = await streamError(refusal('upstream:context_length_exceeded:4096:8219'));
  assert.equal(error.statusCode, undefined);
  assert.equal(error.responseBody, undefined);
  assert.equal(error.message.includes('upstream:'), false);
  assert.equal(error.cause.reason, 'upstream:context_length_exceeded:4096:8219');
  for (const malformed of ['upstream:context_length_exceeded:4096', 'upstream:context_length_exceeded:4096:8219:x', 'upstream:context_length_exceeded:-1:5']) {
    const other = await streamError(refusal(malformed));
    assert.equal(other.context, undefined);
    assert.equal(openCodeSeesOverflow(other), false);
  }
});

test('an engine that dies mid-reply stays a generic failure OpenCode does not compact', async () => {
  for (const reply of [
    {outcome: 'unavailable', reason: 'upstream:stream', host: 'lemonade', model: 'Qwen3-0.6B-GGUF', usage: {}},
    {outcome: 'unavailable', reason: 'upstream:error', host: 'lemonade', model: 'Qwen3-0.6B-GGUF', usage: {}},
    {outcome: 'refused', reason: 'upstream:400', host: 'lemonade', model: 'Qwen3-0.6B-GGUF', usage: {}},
  ]) {
    const error = await streamError(reply);
    assert.equal(error.context, undefined);
    assert.match(error.message, /Check the OpenAbstractions Panel/);
    assert.equal(openCodeSeesOverflow(error), false);
  }
});
