import {FixtureMachine, nativeChatFixture} from './native-chat-fixture.mjs';
import assert from 'node:assert/strict';
import * as oaAvailability from '../availability.js';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {APICallError} from '@ai-sdk/provider';
import {Fold} from '@openabstractions/inference';

// Load the production provider with only its package imports replaced, as
// context-overflow.test.mjs does, so no OpenAbstractions service is needed.
async function errorClass() {
  const source = await readFile(new URL('../index.js', import.meta.url), 'utf8');
  const permissionSource = await readFile(new URL('../permission-wait.js', import.meta.url), 'utf8');
  const body = permissionSource.replace(/^export /gm, '') + '\n' + source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, '');
  const make = new Function('availability', 'runtimeUnavailable', 'runtimeUntrusted', 'unavailableMessage', 'untrustedMessage', 'Machine', 'APICallError', 'resolveInference', 'resolveInferenceV2', 'NativeConnector', 'DeltaKind', 'Fold', 'PartKind',
    'ReplyOutcome', 'RequestGuarantee', 'Role', 'StopReason', `${body}\nreturn {OAInferenceError, hostedMessage};`);
  return make(oaAvailability.availability, oaAvailability.runtimeUnavailable, oaAvailability.runtimeUntrusted, oaAvailability.unavailableMessage, oaAvailability.untrustedMessage, FixtureMachine, APICallError, async () => nativeChatFixture({}),
    async () => { throw new Error('unexpected chat@2 resolution'); }, class {}, {Part: 'part', End: 'end', Usage: 'usage'}, Fold,
    {Text: 'text', ToolCall: 'tool_call'}, {Completed: 'completed'}, {LocalOnly: 'local-only'}, {}, {ToolCalls: 'tool_calls'});
}

// R10 (2026-11-08): a DeepSeek call refused as router:unauthorised read
// "Check the OpenAbstractions Panel for the request outcome." The refusal now
// says in one sentence what to do.
test('a hosted refusal says to allow the service in the popup', async () => {
  const {OAInferenceError, hostedMessage} = await errorClass();
  for (const reply of [
    {outcome: 'no_host', reason: 'router:unauthorised', host: '', model: ''},
    {outcome: 'not_permitted', reason: 'credential:not_permitted:oa-panel-deepseek', host: 'deepseek', model: 'deepseek-v4-pro'},
  ]) {
    const error = new OAInferenceError(reply, 'start');
    assert.equal(error.message, hostedMessage, reply.reason);
    assert.match(error.message, /^OpenAbstractions: allow OpenCode to use this hosted service in the OpenAbstractions popup/);
    assert.doesNotMatch(error.message, /oa-panel|Check the OpenAbstractions Panel/);
  }
  const other = new OAInferenceError({outcome: 'no_host', reason: 'router:no-host', host: '', model: ''}, 'start');
  assert.notEqual(other.message, hostedMessage);
});
