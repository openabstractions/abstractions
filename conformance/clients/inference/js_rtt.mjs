// Time one Node process's chat@1 calls at a service endpoint over the
// test-only pipe connector.
//
//   js_rtt.mjs <service endpoint> <model> <calls>
//
// Prints RTT lines for start, an observe of a retained delta, and the first
// part of a stream.
import {Binding} from '../../../openabstractions-flat/abstraction-facade/javascript/index.js';
import {Chat, DeltaKind, PartKind, RequestGuarantee, Role} from '../../../openabstractions-flat/abstraction-inference/javascript/index.js';
import {PipeTestConnector} from '../js_services/pipe_connector.mjs';

const [endpoint, model, count] = process.argv.slice(2);
if (!endpoint || !model || !count) {
  console.log('js_rtt.mjs <service endpoint> <model> <calls>');
  process.exit(endpoint === '--help' ? 0 : 2);
}
const chat = new Chat(new Binding(new PipeTestConnector(), endpoint, {timeout: 10000}));
const request = {
  model, messages: [{role: Role.User, parts: [{kind: PartKind.Text, text: 'rtt', digest: '', mediaType: '', callId: '', name: '', arguments: ''}]}],
  tools: [], options: null, extensions: {}, requiredExtensions: [], guarantees: [RequestGuarantee.LocalOnly], credential: '',
};
const starts = [], observes = [], firsts = [];
for (let i = 0; i < Number(count); i++) {
  let began = performance.now();
  const admission = await chat.start(request);
  starts.push(performance.now() - began);
  await chat.observe(admission.operation, 0n, 1n, 65536n, 5000n);
  began = performance.now();
  await chat.observe(admission.operation, 0n, 1n, 65536n, 0n);
  observes.push(performance.now() - began);
  await chat.cancel(admission.operation);
  began = performance.now();
  for await (const delta of chat.stream(request)) {
    if (delta.kind === DeltaKind.Part) { firsts.push(performance.now() - began); break; }
  }
}
const median = (v) => [...v].sort((a, b) => a - b)[Math.floor((v.length - 1) / 2)];
for (const [name, values] of [['start', starts], ['observe', observes], ['first token', firsts]]) {
  console.log(`RTT javascript ${name.padEnd(11)} p50=${median(values).toFixed(3)} max=${Math.max(...values).toFixed(3)} ms`);
}
