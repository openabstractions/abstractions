// Time N identical caller@1 Observe calls from one Node process at a runtime
// resolver endpoint: single opens a connection per call, session keeps one.
//
//   js_percall.mjs native|pipe <runtime endpoint> <calls> <warmup> single|session
//
// native uses the Node-API addon over the shared abstraction_ipc library
// (ABSTRACTION_IPC_NODE names the built .node file); pipe uses the test-only
// node:net connector. Prints one PERCALL line; see percall/README.md.
import {CallerClient, CallerOutcome} from '../../../openabstractions-flat/abstraction-facade/javascript/js/abstraction/facade/index.mjs';

const [mode, endpoint, count, warm, reuse] = process.argv.slice(2);
if (!['native', 'pipe'].includes(mode) || !endpoint || !count || warm === undefined || !['single', 'session'].includes(reuse) || (mode === 'pipe' && reuse === 'session')) {
  console.log('js_percall.mjs native|pipe <runtime endpoint> <calls> <warmup> single|session (pipe: single)');
  process.exit(mode === '--help' ? 0 : 2);
}
const connector = mode === 'native'
  ? new (await import('../../../openabstractions-flat/abstraction-identity/javascript/index.js')).NativeConnector()
  : new (await import('../js_services/pipe_connector.mjs')).PipeTestConnector();
// A connection binding per call, as the facade's Binding does: the test pipe
// connector fixes its deadline when connect() is called.
const options = mode === 'native' ? {timeout: 10000, sessions: reuse === 'session'} : {timeout: 10000};
const client = {observe: () => new CallerClient(connector.connect(endpoint, options)).observe()};
const calls = Number(count), warmup = Number(warm);
const samples = [];
let first = 0, code = '';
for (let i = 0; i < warmup + calls; i++) {
  const began = performance.now();
  const observed = await client.observe();
  const took = performance.now() - began;
  if (observed.outcome !== CallerOutcome.Observed) throw new Error(`observe outcome: ${observed.outcome}`);
  if (i === 0) {
    first = took;
    code = observed.attributes.find((a) => a.attribute === 'code')?.proof ?? '';
  }
  if (i >= warmup) samples.push(took);
}
samples.sort((a, b) => a - b);
const rank = (p) => samples[Math.max(0, Math.ceil(samples.length * p) - 1)];
console.log(`PERCALL javascript-${mode}${reuse === 'session' ? '-session' : ''} calls=${samples.length} first=${first.toFixed(3)} p50=${rank(0.5).toFixed(3)} ` +
  `p90=${rank(0.9).toFixed(3)} p99=${rank(0.99).toFixed(3)} max=${samples[samples.length - 1].toFixed(3)} code=${code}`);
