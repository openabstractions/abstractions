import assert from 'node:assert/strict';
import * as oaAvailability from '../availability.js';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {APICallError} from '@ai-sdk/provider';
import {Fold} from '@openabstractions/inference';

// Load the production provider with only its package imports replaced. This
// exercises both v3 result paths without a running OpenAbstractions service.
async function modelWith(chat, APICallError = class extends Error {}) {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions, OAInferenceError};`);
  const PartKind = {Text: 'text', ToolCall: 'tool_call'};
  const DeltaKind = {Part: 'part', End: 'end', Usage: 'usage'};
  const ReplyOutcome = {Completed: 'completed'};
  const StopReason = {ToolCalls: 'tool_calls'};
  const {createOpenAbstractions: create, OAInferenceError} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => chat,
    async () => { throw new Error('unexpected chat@2 resolution'); }, class {}, DeltaKind, Fold, PartKind, ReplyOutcome,
    {LocalOnly: 'local-only'}, {}, StopReason);
  const model = create({connector: {}}).languageModel('local-model');
  model.chat = async () => chat;
  return {model, OAInferenceError, PartKind, DeltaKind, ReplyOutcome, StopReason};
}

function sdkV3ToolInput(part) {
  assert.equal(part.type, 'tool-call');
  assert.equal(typeof part.input, 'string');
  return JSON.parse(part.input.trim());
}

test('doGenerate leaves OA tool-call JSON text for the AI SDK v3 parser', async () => {
  const argumentsText = '{"command":"echo hello"}';
  const reply = {outcome: 'completed', stopReason: 'tool_calls', usage: {}, message: {parts: [
    {kind: 'tool_call', callId: 'call-1', name: 'bash', arguments: argumentsText},
  ]}};
  const {model} = await modelWith({complete: async () => reply});
  const result = await model.doGenerate({prompt: []});
  assert.deepEqual(sdkV3ToolInput(result.content[0]), {command: 'echo hello'});
  assert.equal(result.content[0].input, argumentsText);
});

test('doStream leaves assembled OA tool-call JSON text for the AI SDK v3 parser', async () => {
  const reply = {outcome: 'completed', stopReason: 'tool_calls', usage: {}};
  const chat = {async *stream() {
    yield {kind: 'part', index: 0n, part: {kind: 'tool_call', callId: 'call-1', name: 'bash', arguments: '{"command":'}};
    yield {kind: 'part', index: 0n, part: {kind: 'tool_call', callId: 'call-1', name: 'bash', arguments: '"echo hello"}'}};
    yield {kind: 'end', end: reply};
  }};
  const {model} = await modelWith(chat);
  const {stream} = await model.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  const call = events.find((event) => event.type === 'tool-call');
  assert.deepEqual(sdkV3ToolInput(call), {command: 'echo hello'});
  assert.equal(call.input, '{"command":"echo hello"}');
});

// OpenCode v1.18.31's retryable APIError branch checks all four signals.
// A status, message or body can override isRetryable:false.
function pinnedOpenCodeWouldRetry(error) {
  const retryWords = /429|500|502|503|504|524|exhausted|unavailable|internal error|server error|try again|retry your request/i;
  return error.isRetryable || (error.statusCode !== undefined && error.statusCode >= 500)
    || retryWords.test(error.message) || retryWords.test(error.responseBody ?? '');
}

test('typed OA refusals are recognized and nonretryable even when raw reasons resemble retry cues', async () => {
  for (const reason of ['selection:unavailable', 'ceiling:resource_exhausted', 'upstream:500']) {
    const reply = {outcome: 'unavailable', reason, host: 'local', model: 'm'};
    const {model, OAInferenceError} = await modelWith({complete: async () => reply}, APICallError);
    await assert.rejects(model.doGenerate({prompt: []}), (error) => {
      assert.ok(error instanceof OAInferenceError);
      assert.ok(APICallError.isInstance(error));
      assert.equal(error.isRetryable, false);
      assert.equal(error.statusCode, undefined);
      assert.equal(error.responseBody, undefined);
      assert.equal(error.reason, reason);
      assert.equal(error.message.includes(reason), false);
      assert.equal(error.outcome, 'unavailable');
      assert.equal(error.host, 'local');
      assert.equal(error.model, 'm');
      assert.equal(error.op, 'generate');
      assert.equal(error.cause.reason, reason);
      assert.equal(error.cause.op, 'generate');
      assert.equal(pinnedOpenCodeWouldRetry(error), false);
      return true;
    });
  }
});

test('stream refusal carries the real nonretryable API error and fixed Panel guidance', async () => {
  const reply = {outcome: 'not_permitted', reason: 'rights:unknown', host: '', model: '', usage: {}};
  const {model} = await modelWith({async *stream() { yield {kind: 'end', end: reply}; }}, APICallError);
  const {stream} = await model.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  const error = events.find((event) => event.type === 'error')?.error;
  assert.ok(APICallError.isInstance(error));
  assert.equal(error.isRetryable, false);
  assert.equal(error.op, 'stream');
  assert.equal(error.reason, 'rights:unknown');
  assert.equal(error.cause.reason, 'rights:unknown');
  assert.match(error.message, /OpenAbstractions Panel/);
  assert.equal(pinnedOpenCodeWouldRetry(error), false);
});

test('ambiguous completion and stream failures remain nonretryable after possible dispatch', async () => {
  const upstream = new Error('upstream:500 unavailable');
  const {model: generate} = await modelWith({complete: async () => { throw upstream; }}, APICallError);
  await assert.rejects(generate.doGenerate({prompt: []}), (error) => {
    assert.ok(APICallError.isInstance(error));
    assert.equal(error.cause, upstream);
    assert.equal(error.outcome, 'unknown');
    assert.equal(error.op, 'generate');
    assert.equal(pinnedOpenCodeWouldRetry(error), false);
    return true;
  });

  const {model: streamModel} = await modelWith({async *stream() {
    yield {kind: 'part', index: 0n, part: {kind: 'text', text: 'partial'}};
    throw upstream;
  }}, APICallError);
  const {stream} = await streamModel.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.ok(events.some((event) => event.type === 'text-delta' && event.delta === 'partial'));
  const error = events.find((event) => event.type === 'error')?.error;
  assert.ok(APICallError.isInstance(error));
  assert.equal(error.cause, upstream);
  assert.equal(error.outcome, 'unknown');
  assert.equal(error.op, 'stream');
  assert.equal(pinnedOpenCodeWouldRetry(error), false);
});

test('stream EOF without a terminal reply is a typed nonretryable ambiguous failure', async () => {
  const {model} = await modelWith({async *stream() {
    yield {kind: 'part', index: 0n, part: {kind: 'text', text: 'partial'}};
  }}, APICallError);
  const {stream} = await model.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.ok(events.some((event) => event.type === 'text-delta' && event.delta === 'partial'));
  const error = events.find((event) => event.type === 'error')?.error;
  assert.ok(APICallError.isInstance(error));
  assert.equal(error.isRetryable, false);
  assert.equal(error.outcome, 'unknown');
  assert.equal(error.op, 'stream');
  assert.equal(pinnedOpenCodeWouldRetry(error), false);
});

test('stream terminal refusal never releases an assembled tool call for execution', async () => {
  for (const outcome of ['unavailable', 'cancelled']) {
    const {model} = await modelWith({async *stream() {
      yield {kind: 'part', index: 0n, part: {kind: 'text', text: 'partial'}};
      yield {kind: 'part', index: 1n, part: {kind: 'tool_call', callId: 'call-1', name: 'bash', arguments: '{"command":'}};
      yield {kind: 'part', index: 1n, part: {kind: 'tool_call', callId: 'call-1', name: 'bash', arguments: '"echo unsafe"}'}};
      yield {kind: 'end', end: {outcome, reason: 'upstream:500', usage: {}}};
    }}, APICallError);
    const {stream} = await model.doStream({prompt: []});
    const events = [];
    for await (const event of stream) events.push(event);
    assert.ok(events.some((event) => event.type === 'text-delta' && event.delta === 'partial'));
    assert.ok(events.some((event) => event.type === 'tool-input-end' && event.id === 'call-1'));
    assert.equal(events.some((event) => event.type === 'tool-call'), false);
    const error = events.find((event) => event.type === 'error')?.error;
    assert.ok(APICallError.isInstance(error));
    assert.equal(error.outcome, outcome);
    assert.equal(error.reason, 'upstream:500');
    assert.equal(error.isRetryable, false);
    assert.equal(pinnedOpenCodeWouldRetry(error), false);
  }
});

function policyEnd(outcome = 'completed', reason = '') {
  return {sequence: 0n, kind: 'end', index: 0n, end: {
    outcome, reason, host: 'ollama-workstation', model: 'qwen-local:latest',
    message: {role: 'assistant', parts: [{kind: 'text', text: 'answered', digest: '', mediaType: '', callId: '', name: '', arguments: ''}]},
    stopReason: 'end', usage: {input: 8n, output: 2n, cached: 0n},
  }};
}

test('named policy selection uses chat@2 once, preserves request content and attributes the actual answer', async () => {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
  const PartKind = {Text: 'text', ToolCall: 'tool_call'};
  const DeltaKind = {Part: 'part', End: 'end', Usage: 'usage'};
  const ReplyOutcome = {Completed: 'completed'};
  const StopReason = {End: 'end', NoStop: 'no_stop', ToolCalls: 'tool_calls'};
  let request;
  let startCount = 0;
  const chat = {async startStream(value) {
    startCount++;
    request = value;
    return {admission: {outcome: 'accepted', operation: 'accepted-operation', host: 'preferred-host', model: 'preferred-model',
      preferredModel: 'qwen-local:latest', selectionReason: 'preferred'},
    stream: (async function* () { yield policyEnd(); })()};
  }};
  const {createOpenAbstractions: create} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => { throw new Error('chat@1 must not resolve'); },
    async (_machine, options) => { assert.equal(options.scope, 'local'); return chat; }, class {}, DeltaKind, Fold, PartKind,
    ReplyOutcome, {HostedAllowed: 'hosted-allowed'}, {}, StopReason);
  const provider = create({scope: 'local', credential: 'must-not-cross', requestGuarantees: ['local-only'],
    oaSelectionPolicies: {'oa-preference-cloud-local': 'preferredcloud-allowedlocal'}});
  const model = provider.languageModel('oa-preference-cloud-local');
  const result = await model.doGenerate({prompt: [{role: 'user', content: [{type: 'text', text: 'hello'}]}], maxOutputTokens: 1200,
    tools: [{type: 'function', name: 'bash', inputSchema: {type: 'object'}}]});
  assert.equal(startCount, 1);
  assert.deepEqual(request.target, {model: '', selectionPolicy: 'preferredcloud-allowedlocal'});
  assert.deepEqual(request.content.guarantees, []);
  assert.equal(request.content.credential, '');
  assert.equal(request.content.messages[0].parts[0].text, 'hello');
  assert.equal(request.content.tools[0].name, 'bash');
  assert.equal(request.content.options.maxOutput, 1200n);
  assert.deepEqual(result.providerMetadata.openabstractions, {
    outcome: 'completed', reason: '', host: 'ollama-workstation', placement: 'unknown', model: 'qwen-local:latest',
    operation: 'accepted-operation', policy: 'preferredcloud-allowedlocal',
    preferredModel: 'qwen-local:latest', selectionReason: 'preferred',
  });
});

test('named policy stream carries accepted operation and actual host/model in provider metadata', async () => {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
  const PartKind = {Text: 'text', ToolCall: 'tool_call'};
  const DeltaKind = {Part: 'part', End: 'end', Usage: 'usage'};
  const ReplyOutcome = {Completed: 'completed'};
  const StopReason = {End: 'end', NoStop: 'no_stop', ToolCalls: 'tool_calls'};
  let request;
  let startCount = 0;
  const chat = {async startStream(value) {
    startCount++;
    request = value;
    return {admission: {outcome: 'accepted', operation: 'stream-operation', host: 'ollama-workstation', model: 'qwen-local:latest',
      preferredModel: 'qwen-local:latest', selectionReason: 'preferred'},
    stream: (async function* () { yield policyEnd(); })()};
  }};
  const {createOpenAbstractions: create} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => { throw new Error('chat@1 must not resolve'); },
    async () => chat, class {}, DeltaKind, Fold, PartKind, ReplyOutcome, {}, {}, StopReason);
  const model = create({oaSelectionPolicies: {'oa-preference-local': 'local-first'}}).languageModel('oa-preference-local');
  const {stream} = await model.doStream({prompt: []});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.equal(startCount, 1);
  assert.deepEqual(request.target, {model: '', selectionPolicy: 'local-first'});
  assert.deepEqual(request.content.guarantees, []);
  assert.equal(request.content.credential, '');
  const finish = events.find((event) => event.type === 'finish');
  assert.deepEqual(finish.providerMetadata.openabstractions, {
    outcome: 'completed', reason: '', host: 'ollama-workstation', placement: 'unknown', model: 'qwen-local:latest',
    operation: 'stream-operation', policy: 'local-first', preferredModel: 'qwen-local:latest', selectionReason: 'preferred',
  });
});

async function policyStreamWith(chat) {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
  const create = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => { throw new Error('chat@1 must not resolve'); },
    async () => chat, class {}, {Part: 'part', End: 'end', Usage: 'usage'}, Fold,
    {Text: 'text', ToolCall: 'tool_call'}, {Completed: 'completed'}, {}, {},
    {End: 'end', NoStop: 'no_stop', ToolCalls: 'tool_calls'}).createOpenAbstractions;
  return create({oaSelectionPolicies: {'oa-preference-cloud-local': 'cloud-first-local-fallback'}})
    .languageModel('oa-preference-cloud-local');
}

test('policy fallback streams a tool call with actual local route attribution', async () => {
  let starts = 0;
  let request;
  const model = await policyStreamWith({async startStream(value) {
    starts++;
    request = value;
    return {admission: {outcome: 'accepted', operation: 'fallback-operation', host: 'ollama', model: 'local-model',
      preferredModel: 'cloud-model', selectionReason: 'preferred_route_unavailable'},
    stream: (async function* () {
      yield {kind: 'part', index: 1n, part: {kind: 'tool_call', callId: 'call-1', name: 'bash', arguments: '{"command":'}};
      yield {kind: 'part', index: 1n, part: {kind: 'tool_call', callId: 'call-1', name: 'bash', arguments: '"echo local"}'}};
      yield {kind: 'end', end: {outcome: 'completed', reason: '', host: 'ollama', model: 'local-model',
        stopReason: 'tool_calls', usage: {input: 8n, output: 2n, cached: 0n}}};
    })()};
  }});
  const {stream} = await model.doStream({prompt: [{role: 'user', content: [{type: 'text', text: 'use bash'}]}],
    tools: [{type: 'function', name: 'bash', inputSchema: {type: 'object'}}]});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.equal(starts, 1);
  assert.deepEqual(request.target, {model: '', selectionPolicy: 'cloud-first-local-fallback'});
  assert.equal(request.content.tools[0].name, 'bash');
  assert.equal(request.content.credential, '');
  assert.deepEqual(request.content.guarantees, []);
  const call = events.find((event) => event.type === 'tool-call');
  assert.deepEqual(sdkV3ToolInput(call), {command: 'echo local'});
  assert.equal(events.filter((event) => event.type === 'tool-call').length, 1);
  assert.deepEqual(events.find((event) => event.type === 'finish').providerMetadata.openabstractions, {
    outcome: 'completed', reason: '', host: 'ollama', placement: 'unknown', model: 'local-model', operation: 'fallback-operation',
    policy: 'cloud-first-local-fallback', preferredModel: 'cloud-model', selectionReason: 'preferred_route_unavailable',
  });
});

test('policy stream failure after partial tool output never releases a call or retries', async () => {
  let starts = 0;
  const model = await policyStreamWith({async startStream() {
    starts++;
    return {admission: {outcome: 'accepted', operation: 'dispatched-cloud', host: 'cloud', model: 'cloud-model',
      preferredModel: 'cloud-model', selectionReason: 'preferred'},
    stream: (async function* () {
      yield {kind: 'part', index: 1n, part: {kind: 'tool_call', callId: 'call-2', name: 'bash', arguments: '{"command":'}};
      yield {kind: 'part', index: 1n, part: {kind: 'tool_call', callId: 'call-2', name: 'bash', arguments: '"echo unsafe"}'}};
      yield {kind: 'end', end: {outcome: 'unavailable', reason: 'upstream:500', host: 'cloud', model: 'cloud-model',
        usage: {input: 8n, output: 2n, cached: 0n}}};
    })()};
  }});
  const {stream} = await model.doStream({prompt: [], tools: [{type: 'function', name: 'bash', inputSchema: {type: 'object'}}]});
  const events = [];
  for await (const event of stream) events.push(event);
  assert.equal(starts, 1);
  assert.equal(events.some((event) => event.type === 'tool-call'), false);
  const error = events.find((event) => event.type === 'error')?.error;
  assert.ok(APICallError.isInstance(error));
  assert.equal(error.operation, 'dispatched-cloud');
  assert.equal(error.reason, 'upstream:500');
  assert.equal(error.isRetryable, false);
  assert.equal(pinnedOpenCodeWouldRetry(error), false);
});

test('accepted policy failures retain the actual operation and never start a second candidate', async () => {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
  const PartKind = {Text: 'text', ToolCall: 'tool_call'};
  const DeltaKind = {Part: 'part', End: 'end', Usage: 'usage'};
  const ReplyOutcome = {Completed: 'completed'};
  const StopReason = {End: 'end', NoStop: 'no_stop', ToolCalls: 'tool_calls'};
  let startCount = 0;
  const chat = {async startStream() {
    startCount++;
    return {admission: {outcome: 'accepted', operation: 'accepted-failed-operation', host: 'ollama-workstation', model: 'qwen-local:latest',
      preferredModel: 'deepseek-chat-v4', selectionReason: 'preferred'},
    stream: (async function* () { yield policyEnd('unavailable', 'upstream:error'); })()};
  }};
  const {createOpenAbstractions: create} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => { throw new Error('must not retry chat@1'); },
    async () => chat, class {}, DeltaKind, Fold, PartKind, ReplyOutcome, {}, {}, StopReason);
  const model = create({oaSelectionPolicies: {'oa-preference-cloud-local': 'cloud-first-local-fallback'}})
    .languageModel('oa-preference-cloud-local');
  await assert.rejects(model.doGenerate({prompt: []}), (error) => {
    assert.equal(error.operation, 'accepted-failed-operation');
    assert.equal(error.cause.operation, 'accepted-failed-operation');
    assert.equal(error.reason, 'upstream:error');
    return true;
  });
  assert.equal(startCount, 1);
});

test('canceling an accepted policy stream before its first pull cancels without observing', async () => {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
  const PartKind = {Text: 'text', ToolCall: 'tool_call'};
  const DeltaKind = {Part: 'part', End: 'end', Usage: 'usage'};
  const ReplyOutcome = {Completed: 'completed'};
  const StopReason = {End: 'end', NoStop: 'no_stop', ToolCalls: 'tool_calls'};
  let startCount = 0;
  let observeCount = 0;
  let cancelCount = 0;
  const chat = {async startStream() {
    startCount++;
    return {admission: {outcome: 'accepted', operation: 'never-observed-operation', host: 'local', model: 'm'},
      async cancel() { cancelCount++; },
      stream: (async function* () { observeCount++; yield policyEnd(); })()};
  }};
  const {createOpenAbstractions: create} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => { throw new Error('must not resolve chat@1'); },
    async () => chat, class {}, DeltaKind, Fold, PartKind, ReplyOutcome, {}, {}, StopReason);
  const model = create({oaSelectionPolicies: {'oa-preference-local': 'local-first'}}).languageModel('oa-preference-local');
  const {stream} = await model.doStream({prompt: []});
  const reader = stream.getReader();
  await reader.cancel();
  assert.equal(startCount, 1);
  assert.equal(observeCount, 0);
  assert.equal(cancelCount, 1);
});

test('canceling an accepted policy stream sends Cancel before waiting for a blocked Observe', async () => {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
  const PartKind = {Text: 'text', ToolCall: 'tool_call'};
  const DeltaKind = {Part: 'part', End: 'end', Usage: 'usage'};
  const ReplyOutcome = {Completed: 'completed'};
  const StopReason = {End: 'end', NoStop: 'no_stop', ToolCalls: 'tool_calls'};
  let observeStarted;
  const observed = new Promise((resolve) => { observeStarted = resolve; });
  let resolveObserve;
  const observeGate = new Promise((resolve) => { resolveObserve = resolve; });
  let cancelSent;
  const cancelled = new Promise((resolve) => { cancelSent = resolve; });
  const order = [];
  const chat = {async startStream() {
    return {admission: {outcome: 'accepted', operation: 'blocked-observe-operation', host: 'local', model: 'm'},
      async cancel() { order.push('cancel'); cancelSent(); resolveObserve(); },
      stream: (async function* () { order.push('observe'); observeStarted(); await observeGate; yield policyEnd(); })()};
  }};
  const {createOpenAbstractions: create} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => { throw new Error('must not resolve chat@1'); },
    async () => chat, class {}, DeltaKind, Fold, PartKind, ReplyOutcome, {}, {}, StopReason);
  const model = create({oaSelectionPolicies: {'oa-preference-local': 'local-first'}}).languageModel('oa-preference-local');
  const {stream} = await model.doStream({prompt: []});
  const reader = stream.getReader();
  await reader.read(); // stream-start
  const reading = reader.read();
  await observed;
  const cancellation = reader.cancel();
  await cancelled;
  assert.deepEqual(order, ['observe', 'cancel']);
  await cancellation;
  await reading;
});

test('unmapped or inherited model names stay explicit chat@1 model targets', async () => {
  for (const modelId of ['openrouter/name', 'toString']) {
    let request;
    const chat = {async complete(value) {
      request = value;
      return {outcome: 'completed', stopReason: 'end', usage: {}, message: {parts: []}};
    }};
    const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
    const body = source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
    const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
      'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {createOpenAbstractions};`);
    const {createOpenAbstractions: create} = make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, class {}, APICallError, async () => chat,
      async () => { throw new Error('unmapped target must not resolve chat@2'); }, class {}, {Part: 'part', End: 'end', Usage: 'usage'}, Fold,
      {Text: 'text', ToolCall: 'tool_call'}, {Completed: 'completed'}, {LocalOnly: 'local-only'}, {}, {End: 'end'});
    const model = create({oaSelectionPolicies: {'oa-preference-local': 'local-first'}}).languageModel(modelId);
    await model.doGenerate({prompt: []});
    assert.equal(request.model, modelId);
    assert.equal(Object.hasOwn(request, 'target'), false);
  }
});
