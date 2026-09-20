// Resolution with no runtime fails with the facade's ResolutionError, carrying the
// requested capability and contract, what was looked for, and the transport cause.
// Runs without a runtime: an unused local endpoint and in-process resolver replies.
import assert from 'node:assert/strict';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {Machine, ResolutionError, runtimeUnavailable} from '@openabstractions/facade';
import {PipeTestConnector} from './pipe_connector.mjs';

const nobody = process.platform === 'win32'
  ? `\\\\.\\pipe\\oa-js-absent-${process.pid}-${Date.now()}`
  : join(tmpdir(), `oa-js-absent-${process.pid}-${Date.now()}.sock`);
const contract = 'abstraction.job/acceptance@1';
const passed = [];

async function refused(promise, status, lookedFor) {
  const error = await promise.then(() => assert.fail('resolution succeeded'), (e) => e);
  assert.ok(error instanceof ResolutionError, `expected ResolutionError, got ${error?.name}: ${error?.message}`);
  assert.equal(error.name, 'ResolutionError');
  assert.equal(error.status, status);
  assert.equal(error.capability, 'abstraction.job');
  assert.equal(error.contract, contract);
  assert.equal(error.lookedFor, lookedFor);
  assert.equal(error.message, `service resolution: ${status}: ${contract} (capability abstraction.job) at ${lookedFor}`);
  return error;
}

// A resolver reply as the runtime frames it.
const reply = (value) => new TextEncoder().encode(JSON.stringify(
  {version: 1, service: 'abstraction.facade/resolver@1', method: 'Resolve', ok: true, payload: {value}}));
class Answering {
  constructor(answer) { this.answer = answer; this.endpoints = []; }
  supports(scope, transport) { return scope === 'local' && transport === 'oa-framed-local@1'; }
  runtimeEndpoint() { return 'installed-endpoint'; }
  connect(endpoint) {
    this.endpoints.push(endpoint);
    return {exchangeFrame: async (frame) => {
      const request = JSON.parse(new TextDecoder().decode(frame)).arguments.request;
      return reply(this.answer(request));
    }, writeFrame: async () => {}};
  }
}

// No runtime installed: the connector cannot name an installed runtime.
{
  class NotInstalled extends PipeTestConnector { runtimeEndpoint() { throw new Error('native bootstrap unavailable'); } }
  const machine = new Machine(null, {connector: new NotInstalled(), timeout: 1000});
  const error = await refused(machine.resolveService(contract), runtimeUnavailable, 'the installed runtime');
  assert.equal(error.cause.message, 'native bootstrap unavailable');
  await assert.rejects(machine.resolverBinding(), (e) => e instanceof ResolutionError && e.status === runtimeUnavailable && e.contract === null);
  assert.throws(() => new Machine(null, {connector: {supports() { return true; }, connect() {}}}),
    /installed runtime selection requires connector.runtimeEndpoint/);
  passed.push('no runtime installed');
}

// VISION.md 2026-09-16: an adopted capability with no runtime fails with the facade's
// resolution error, visibly. "A logging seam that quietly writes to stderr instead is the
// same defect" [LOG-S11]. JavaScript adopts logging by resolving the sink contract and
// building SinkClient on the binding; with no runtime there is no binding to build it on.
{
  class NotInstalled extends PipeTestConnector { runtimeEndpoint() { throw new Error('native bootstrap unavailable'); } }
  const sink = 'abstraction.logging/sink@1';
  const error = await new Machine(null, {connector: new NotInstalled(), timeout: 1000}).resolveService(sink)
    .then(() => assert.fail('the logging sink resolved with no runtime'), (e) => e);
  assert.ok(error instanceof ResolutionError, `expected ResolutionError, got ${error?.name}: ${error?.message}`);
  assert.equal(error.status, runtimeUnavailable);
  assert.equal(error.capability, 'abstraction.logging');
  assert.equal(error.contract, sink);
  assert.equal(error.lookedFor, 'the installed runtime');
  assert.equal(error.message, `service resolution: runtime_unavailable: ${sink} (capability abstraction.logging) at the installed runtime`);
  passed.push('adopted logging sink with no runtime');
}

// The installed runtime's endpoint has no listener.
{
  class Installed extends PipeTestConnector { runtimeEndpoint() { return nobody; } }
  const error = await refused(new Machine(null, {connector: new Installed(), timeout: 1000}).resolveService(contract),
    runtimeUnavailable, `the installed runtime at ${nobody}`);
  assert.ok(!(error.cause instanceof ResolutionError) && ['ENOENT', 'ECONNREFUSED'].includes(error.cause.code), String(error.cause));
  passed.push('installed runtime not listening');
}

// An explicit endpoint nobody listens on.
{
  const error = await refused(new Machine(nobody, {connector: new PipeTestConnector(), timeout: 1000}).resolveService(contract),
    runtimeUnavailable, `the explicit endpoint ${nobody}`);
  assert.ok(['ENOENT', 'ECONNREFUSED'].includes(error.cause.code), String(error.cause));
  passed.push('explicit endpoint not listening');
}

// A runtime that is up and lacks the requested service.
{
  const connector = new Answering(() => ({status: 'unavailable'}));
  const explicit = await refused(new Machine('explicit-endpoint', {connector}).resolveService(contract),
    'unavailable', 'the explicit endpoint explicit-endpoint');
  assert.equal(explicit.cause, undefined);
  await refused(new Machine(null, {connector}).resolveService(contract), 'unavailable', 'the installed runtime at installed-endpoint');
  assert.deepEqual(connector.endpoints, ['explicit-endpoint', 'installed-endpoint']);
  passed.push('runtime lacks the service');
}

// The caller's own cancellation stays the connector's error.
{
  const controller = new AbortController();
  const cancelled = new Error('waiting cancelled');
  const connector = {supports: () => true, connect: () => ({exchangeFrame: async () => { controller.abort(); throw cancelled; }, writeFrame: async () => {}})};
  await assert.rejects(new Machine('explicit-endpoint', {connector, cancellation: controller.signal}).resolveService(contract), (e) => e === cancelled);
  passed.push('caller cancellation');
}

// Success is unchanged for an explicit endpoint, the installed runtime and a call scope.
{
  const connector = new Answering((request) => ({status: 'resolved', reference: {provider: 'p', capability: request.capability,
    contract: request.contracts[0], guarantees: request.guarantees, scope: 'local', transport: 'oa-framed-local@1', endpoint: 'provider'}}));
  for (const machine of [new Machine('explicit-endpoint', {connector}), new Machine(null, {connector}), new Machine(null, {connector}).callScope()]) {
    const binding = await machine.resolveService(contract, {guarantees: ['g'], scope: 'local'});
    assert.equal(binding.endpoint, 'provider');
    assert.deepEqual(binding.reference.guarantees, ['g']);
  }
  assert.equal((await new Machine('explicit-endpoint', {connector}).resolverBinding()).endpoint, 'explicit-endpoint');
  assert.equal((await new Machine(null, {connector}).resolverBinding()).endpoint, 'installed-endpoint');
  assert.ok(Number.isFinite((await new Machine(null, {connector}).callScope().resolverBinding()).waiting.deadline));
  const unsupported = new Answering((request) => ({status: 'resolved', reference: {provider: 'p', capability: request.capability,
    contract: request.contracts[0], guarantees: [], scope: 'local', transport: 'other', endpoint: 'provider'}}));
  await refused(new Machine('explicit-endpoint', {connector: unsupported}).resolveService(contract), 'unsupported_transport', 'the explicit endpoint explicit-endpoint');
  passed.push('success unchanged');
}

// The installed runtime's identity is selected first and every connection of the
// resolution requires it, as the Go, C++ and Python defaults do.
{
  const installed = {principalKind: 2, principal: '1000', program: '/installed/openabstractions'};
  class Selecting extends Answering {
    constructor(select) {
      super((request) => ({status: 'resolved', reference: {provider: 'p', capability: request.capability,
        contract: request.contracts[0], guarantees: [], scope: 'local', transport: 'oa-framed-local@1', endpoint: 'provider'}}));
      this.select = select; this.steps = [];
    }
    async selectRuntime(options) {
      this.steps.push('select');
      assert.ok(Number.isFinite(options.deadline) && options.deadline > performance.now());
      return this.select(options);
    }
    runtimeEndpoint() { this.steps.push('endpoint'); return 'installed-endpoint'; }
    connect(endpoint, options) { this.steps.push(`connect ${endpoint} as ${options.server?.program ?? 'unverified'}`); return super.connect(endpoint); }
  }
  const connector = new Selecting(async () => installed);
  const binding = await new Machine(null, {connector}).resolveService(contract);
  assert.deepEqual(connector.steps, ['select', 'endpoint', 'connect installed-endpoint as /installed/openabstractions']);
  for (const view of [binding, binding.withWaiting(), binding.callScope()]) assert.equal(view.waiting.server.program, installed.program);
  assert.equal((await new Machine(null, {connector}).resolverBinding()).waiting.server.program, installed.program);

  // An explicit endpoint is the application's choice and selects nothing.
  const explicit = new Selecting(async () => assert.fail('an explicit endpoint selected the installation'));
  assert.equal((await new Machine('explicit-endpoint', {connector: explicit}).resolveService(contract)).waiting.server, undefined);
  assert.deepEqual(explicit.steps, ['connect explicit-endpoint as unverified']);

  // A configured server expectation replaces selection.
  const configured = {principalKind: 2, principal: '1000', program: '/configured/runtime'};
  const given = new Selecting(async () => assert.fail('a configured expectation selected the installation'));
  assert.equal((await new Machine(null, {connector: given, server: configured}).resolveService(contract)).waiting.server.program, configured.program);
  assert.deepEqual(given.steps, ['endpoint', 'connect installed-endpoint as /configured/runtime']);

  // No installation: runtime_unavailable with the selector's error, and nothing contacted.
  const untrusted = Object.assign(new Error('select'), {status: 8});
  const absent = new Selecting(async () => { throw untrusted; });
  const error = await refused(new Machine(null, {connector: absent}).resolveService(contract), runtimeUnavailable, 'the installed runtime');
  assert.equal(error.cause, untrusted);
  assert.deepEqual(absent.steps, ['select']);

  // The caller's own cancellation during selection stays the connector's error.
  const controller = new AbortController();
  const stopped = Object.assign(new Error('select'), {status: 7});
  const cancelling = new Selecting(async () => { controller.abort(); throw stopped; });
  await assert.rejects(new Machine(null, {connector: cancelling, cancellation: controller.signal}).resolveService(contract), (e) => e === stopped);
  assert.deepEqual(cancelling.steps, ['select']);
  passed.push('installed identity selected and required');
}

console.log('PASS resolution absence: ' + passed.join(', '));
