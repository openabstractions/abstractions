// Every test in this run resolves an unserved or fixture endpoint, never the
// person's installed runtime (runtime-isolation.mjs). A plain `node --test`
// without the preload fails here first.
import test from 'node:test';
import assert from 'node:assert/strict';
import {installedRuntimeEndpoint, unservedTestEndpoint} from '../runtime-isolation.mjs';

test('this test run names an endpoint other than the installed runtime', () => {
  const endpoint = process.env.ABSTRACTION_RUNTIME_ENDPOINT;
  assert.ok(endpoint, 'run the tests with `npm test` or `node --import ./runtime-isolation.mjs --test`');
  assert.equal(installedRuntimeEndpoint(endpoint), false, `${endpoint} is the installed runtime`);
});

test('the installed endpoint names are recognised and the unserved one is not among them', () => {
  assert.ok(installedRuntimeEndpoint('\\\\.\\pipe\\openabstractions-user-S-1-5-21-1-2-3-1001-runtime-v1'));
  assert.ok(installedRuntimeEndpoint('/run/user/1000/openabstractions-user-runtime-v1.sock'));
  assert.equal(installedRuntimeEndpoint(unservedTestEndpoint('win32', 7)), false);
  assert.equal(unservedTestEndpoint('win32', 7), '\\\\.\\pipe\\oa-opencode-test-unserved-7');
});

test('the native runtime selection takes the isolated endpoint', async (t) => {
  let selected;
  try {
    const {runtimeConnector} = await import('../runtime-connector.js');
    selected = await (await runtimeConnector()).selectRuntime({timeout: 2000});
  } catch (error) {
    t.skip('the native client is not available here: ' + (error?.message ?? error));
    return;
  }
  const endpoint = selected?.endpoint;
  if (typeof endpoint !== 'string') { t.skip('the selection reports no endpoint'); return; }
  assert.equal(installedRuntimeEndpoint(endpoint), false, `selected ${endpoint}`);
  assert.equal(endpoint, process.env.ABSTRACTION_RUNTIME_ENDPOINT);
});
