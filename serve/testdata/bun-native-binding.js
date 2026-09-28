// Exercises @openabstractions/ipc/bun under a real Bun runtime against the
// shared C ABI library named by ABSTRACTION_IPC_LIBRARY. The Go test that runs
// it supplies the isolated runtime's endpoint and the expectation it must
// satisfy, and reads one JSON object from standard output.
import {BunNativeConnector} from '@openabstractions/ipc/bun';
import {FrameError} from '@openabstractions/ipc';
import {Machine} from '@openabstractions/facade';

const endpoint = process.env.OA_PROBE_ENDPOINT;
const principalKind = Number(process.env.OA_PROBE_PRINCIPAL_KIND);
const principal = process.env.OA_PROBE_PRINCIPAL;
const program = process.env.OA_PROBE_PROGRAM;
const wrongProgram = process.env.OA_PROBE_WRONG_PROGRAM;

const connector = new BunNativeConnector();
const steps = {};

function failed(error) {
  if (error instanceof FrameError) {
    return {ok: false, status: error.status, transferred: error.transferred, message: error.message};
  }
  return {ok: false, status: null, transferred: null, message: String(error)};
}

// The shared installed-runtime selector, through the Bun worker.
try {
  const selected = await connector.selectRuntime({timeout: 5000});
  steps.select = {ok: true, ...selected};
} catch (error) {
  steps.select = failed(error);
}

// The shared bootstrap endpoint query, on the calling thread.
try {
  steps.endpoint = {ok: true, value: connector.runtimeEndpoint()};
} catch (error) {
  steps.endpoint = failed(error);
}

// One abstraction.facade/endpoint@1 Describe exchange over the supplied endpoint.
async function describe(server) {
  const waiting = {connector, timeout: 10000};
  if (server !== null) waiting.server = server;
  const description = await new Machine(endpoint, waiting).describeEndpoint(endpoint);
  return {ok: true, outcome: description.outcome, program: description.program,
    services: description.services.map((service) => service.contract)};
}

for (const [name, server] of [
  ['verified', {principalKind, principal, program}],
  ['wrongProgram', {principalKind, principal, program: wrongProgram}],
  ['unverified', null],
]) {
  try {
    steps[name] = await describe(server);
  } catch (error) {
    steps[name] = failed(error);
  }
}

console.log(JSON.stringify(steps));
