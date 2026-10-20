import test from 'node:test';
import assert from 'node:assert/strict';
import {createConnection} from '../connection.js';

test('helper launch path is retained only from a ready native result and unavailable after close',async()=>{
  let reply={outcome:'connect_pending',helperProgram:'C:/staged/oa-opencode-integration.exe'};
  const connection=createConnection({required:true,request:async()=>reply,check:async()=>reply});
  await connection.start();assert.equal(connection.helperProgram(),'');
  reply={outcome:'connected',hostRegistered:true,helperEnrolled:true,helperProgram:'C:/kept/oa-opencode-integration.exe'};
  await connection.guard({execute:async()=>''}).execute();assert.equal(connection.helperProgram(),reply.helperProgram);
  await connection.close();assert.equal(connection.helperProgram(),'');
});

test('popup launch retains pending and stops capability effects across a tool chain', async () => {
  let popups = 0, effects = 0;
  const connection = createConnection({required: true, request: async () => { popups++; return true; }});
  const tool = connection.guard({execute: async () => { effects++; return 'effect'; }});
  for (let i = 0; i < 3; i++) assert.equal(JSON.parse(await tool.execute()).outcome, 'connect_pending');
  assert.equal(popups, 1);
  assert.equal(effects, 0);
});

test('readiness needs both native host registration and helper enrollment', async () => {
  for (const reply of [{outcome: 'connected', helperEnrolled: true},
    {outcome: 'connected', hostRegistered: true}, {outcome: 'not_connected'}]) {
    const connection = createConnection({required: true, request: async () => reply});
    await connection.start();
    assert.equal(connection.result().outcome, 'not_connected');
  }
  const connection = createConnection({required: true,
    request: async () => ({outcome: 'connected', hostRegistered: true, helperEnrolled: true})});
  assert.equal(await connection.guard({execute: async () => 'effect'}).execute(), 'effect');
});

test('failed native connection stays unconnected with no automatic popup replay', async () => {
  let popups = 0;
  const connection = createConnection({required: true, request: async () => { popups++; throw Error('private detail'); }});
  await connection.start();
  await connection.start();
  assert.equal(connection.result().outcome, 'not_connected');
  assert.match(connection.result().guidance, /Connecting grants no permission to message/);
  assert.equal(popups, 1);
});

test('a later explicit tool call rereads native completion without another Connect popup', async () => {
  let ready = false, popups = 0, reads = 0, effects = 0;
  const connection = createConnection({required: true, request: async () => { popups++; return {outcome: 'connect_pending'}; },
    check: async () => { reads++; return ready ? {outcome: 'connected', hostRegistered: true, helperEnrolled: true} : {outcome: 'connect_pending'}; }});
  const tool = connection.guard({execute: async () => { effects++; return 'effect'; }});
  assert.equal(JSON.parse(await tool.execute()).outcome, 'connect_pending');
  ready = true;
  assert.equal(await tool.execute(), 'effect');
  assert.equal(popups, 1);
  assert.equal(reads, 2);
  assert.equal(effects, 1);
  await connection.close();
  assert.equal(JSON.parse(await tool.execute()).outcome, 'not_connected');
  assert.equal(effects, 1);
});
