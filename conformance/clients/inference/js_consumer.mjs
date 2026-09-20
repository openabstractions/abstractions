// A JavaScript application resolves abstraction.inference/chat@1 through the
// pure facade over the test-only pipe connector, and calls complete() and the
// async stream().
import assert from 'node:assert/strict';
import {Machine, ResolutionError} from '../../../openabstractions-flat/abstraction-facade/javascript/index.js';
import {resolveInference, DeltaKind, PartKind, ReplyOutcome, RequestGuarantee, Role} from '../../../openabstractions-flat/abstraction-inference/javascript/index.js';
import {PipeTestConnector} from '../js_services/pipe_connector.mjs';

const [endpoint, absent, mode] = process.argv.slice(2);
if (!endpoint || !absent || !mode) {
  console.log('js_consumer.mjs <runtime endpoint> <runtime without inference> refused|served');
  process.exit(endpoint === '--help' ? 0 : 2);
}
const connector = new PipeTestConnector();
const request = (model, text) => ({
  model, messages: [{role: Role.User, parts: [{kind: PartKind.Text, text, digest: '', mediaType: '', callId: '', name: '', arguments: ''}]}],
  tools: [], options: null, extensions: {}, requiredExtensions: [], guarantees: [RequestGuarantee.LocalOnly], credential: '',
});

const absence = await resolveInference(new Machine(absent, {connector, timeout: 10000}), {scope: 'local'}).then(() => null, (e) => e);
assert.ok(absence instanceof ResolutionError, `absence: ${absence}`);
assert.equal(absence.status, 'unavailable');

const chat = await resolveInference(new Machine(endpoint, {connector, timeout: 10000}), {scope: 'local'});
if (mode === 'refused') {
  const reply = await chat.complete(request('fixture-chat:1b', 'hi'));
  assert.equal(reply.outcome, ReplyOutcome.NotPermitted);
  assert.equal(reply.reason, 'rights:not_granted');
  console.log(`PASS javascript refused: absence=unavailable complete=${reply.outcome}`);
  process.exit(0);
}
assert.equal(mode, 'served');
const reply = await chat.complete(request('fixture-chat:1b', 'hi'));
assert.equal(reply.outcome, ReplyOutcome.Completed);
assert.deepEqual(reply.message.parts.map((p) => p.text), ['Hello from the fixture runtime']);
assert.equal(reply.host, 'ollama');
assert.equal(reply.usage.input, 5n);
const started = performance.now();
let first = null, text = '', last = null;
for await (const delta of chat.stream(request('fixture-chat:1b', 'hi'))) {
  if (delta.kind === DeltaKind.Part) {
    first ??= performance.now() - started;
    text += delta.part.text;
  }
  last = delta;
}
assert.equal(text, 'Hello from the fixture runtime');
assert.equal(last.end.outcome, ReplyOutcome.Completed);
for await (const delta of chat.stream(request('fixture-chat:1b', 'HOLD'))) {
  if (delta.kind === DeltaKind.Part) break;
}
const denied = await chat.complete(request('denied-chat', 'hi'));
assert.equal(denied.outcome, ReplyOutcome.NotPermitted);
assert.equal(denied.reason, 'rights:not_granted');
console.log('PASS javascript served: complete, stream, cancel, refusal');
console.log(`FIRST_TOKEN_MS javascript ${first.toFixed(2)}`);
