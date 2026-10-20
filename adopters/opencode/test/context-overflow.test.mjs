import assert from 'node:assert/strict';
import * as oaAvailability from '../availability.js';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {APICallError} from '@ai-sdk/provider';
import {Fold} from '@openabstractions/inference';

// Load the production provider with only its package imports replaced, as
// tool-call-output.test.mjs does, so no OpenAbstractions service is needed.
async function modelWith(chat) {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions, OAInferenceError};`);
  const {createOpenAbstractions: create, OAInferenceError} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => chat,
    async () => { throw new Error('unexpected chat@2 resolution'); }, class {}, {Part: 'part', End: 'end', Usage: 'usage'}, Fold,
    {Text: 'text', ToolCall: 'tool_call'}, {Completed: 'completed'}, {LocalOnly: 'local-only'}, {}, {ToolCalls: 'tool_calls'});
  const model = create({connector: {}}).languageModel('qwen');
  model.chat = async () => chat;
  return {model, OAInferenceError};
}

// OpenCode v1.18.31 classifies an APICallError as ContextOverflowError, which
// it compacts and never retries, when its message matches these patterns
// (packages/llm/src/provider-error.ts at 014614d35b397775e5d397a490fc72368c894ec2,
// MIT licence) or its body carries error.code context_length_exceeded.
const openCodeOverflowPatterns = [
  /prompt is too long/i, /request_too_large/i, /input is too long for requested model/i, /exceeds the context window/i,
  /exceeds (?:the )?(?:model'?s )?maximum context length(?: of [\d,]+ tokens?|\s*\([\d,]+\))/i,
  /input token count.*exceeds the maximum/i, /tokens in request more than max tokens allowed/i, /maximum prompt length is \d+/i,
  /reduce the length of the messages/i, /maximum context length is \d+ tokens/i,
  /exceeds (?:the )?maximum allowed input length of [\d,]+ tokens?/i,
  /input \(\d+ tokens\) is longer than the model'?s context length \(\d+ tokens\)/i, /exceeds the limit of \d+/i,
  /exceeds the available context size/i, /greater than the context length/i, /context window exceeds limit/i,
  /exceeded model token limit/i, /context[_ ]length[_ ]exceeded/i, /request entity too large/i, /context length is only \d+ tokens/i,
  /input length.*exceeds.*context length/i, /prompt too long; exceeded (?:max )?context length/i,
  /too large for model with \d+ maximum context length/i,
  /prompt has [\d,]+ tokens?, but the configured context size is [\d,]+ tokens?/i, /model_context_window_exceeded/i,
  /too many tokens/i, /token limit exceeded/i,
];
const openCodeOverflowExclusions = [/^(throttling error|service unavailable):/i, /rate limit/i, /too many requests/i];

function openCodeSeesOverflow(error) {
  const message = error.message;
  const body = error.responseBody ? JSON.parse(error.responseBody) : undefined;
  return (!openCodeOverflowExclusions.some((pattern) => pattern.test(message)) &&
    openCodeOverflowPatterns.some((pattern) => pattern.test(message))) ||
    error.statusCode === 413 || body?.error?.code === 'context_length_exceeded';
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

test('a first-turn overflow tells OpenCode the loaded context and the counted input', async () => {
  const error = await streamError(refusal('upstream:context_length_exceeded:4096:8219'), {maxOutputTokens: 1024});
  assert.ok(APICallError.isInstance(error));
  assert.equal(error.outcome, 'refused');
  assert.equal(error.reason, 'upstream:context_length_exceeded:4096:8219');
  assert.deepEqual(error.context, {window: 4096, input: 8219, reserve: 1024});
  assert.equal(error.message,
    'OpenAbstractions: this conversation exceeds the available context size of Qwen3-0.6B-GGUF (4,096 tokens). ' +
    'The request had 8,219 input tokens. 1,024 tokens of that context are reserved for the answer, leaving 3,072 for input. ' +
    'Compact or start a new session, remove instructions or tools, or choose a model with a larger context.');
  assert.equal(error.isRetryable, false);
  assert.ok(openCodeSeesOverflow(error));
});

test('the answer reserve comes from the request and a reserve that fills the context says so', async () => {
  const defaulted = await streamError(refusal('upstream:context_length_exceeded:8192:9000'));
  assert.deepEqual(defaulted.context, {window: 8192, input: 9000, reserve: 4096});
  assert.match(defaulted.message, /4,096 tokens of that context are reserved for the answer, leaving 4,096 for input\./);

  const full = await streamError(refusal('upstream:context_length_exceeded:4096:120'), {maxOutputTokens: 4096});
  assert.match(full.message, /The 4,096-token answer reserve fills the whole 4,096-token context; lower this model's output limit\./);
  assert.ok(openCodeSeesOverflow(full));
});

test('an overflow with unknown numbers still reads as an overflow with the same remedy', async () => {
  const error = await streamError(refusal('upstream:context_length_exceeded:0:0', {model: ''}));
  assert.equal(error.message,
    'OpenAbstractions: this conversation exceeds the available context size of the model. ' +
    'Compact or start a new session, remove instructions or tools, or choose a model with a larger context.');
  assert.deepEqual(error.context, {window: 0, input: 0, reserve: 4096});
  assert.ok(openCodeSeesOverflow(error));
});

test('doGenerate surfaces the same overflow message', async () => {
  const {model, OAInferenceError} = await modelWith({complete: async () => refusal('upstream:context_length_exceeded:4096:8219')});
  await assert.rejects(model.doGenerate({prompt: [], maxOutputTokens: 1024}), (error) => {
    assert.ok(error instanceof OAInferenceError);
    assert.equal(error.op, 'generate');
    assert.match(error.message, /exceeds the available context size of Qwen3-0\.6B-GGUF \(4,096 tokens\)/);
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
