import test from 'node:test';
import assert from 'node:assert/strict';
import {createApplicationPresence} from '../application-presence.js';
import {ApplicationsClient} from '../../../openabstractions-flat/abstraction-facade/javascript/js/abstraction/facade/index.mjs';

const event = (type, id) => ({event: {type, properties: {info: {id, directory: 'owned-project'}}}});
function fixture(outcomes = []) {
  const calls = [], timers = new Map();
  let serial = 0;
  const client = {announce: async value => {
    calls.push(['announce', structuredClone(value)]);
    return outcomes.shift() ?? {outcome: 'applied', instance: 'epoch.owned'};
  }, withdraw: async (...args) => { calls.push(['withdraw', ...args]); return {outcome: 'applied'}; }};
  const owner = createApplicationPresence({directory: 'owned-project', resolveClient: async () => client,
    schedule: fn => { const id = ++serial; timers.set(id, fn); return id; },
    unschedule: id => timers.delete(id)});
  return {owner, calls, timers, client};
}

test('two host contexts, no payload identity, one renewal and clean withdrawal', async () => {
  const {owner, calls, timers} = fixture();
  await owner.event(event('session.created', 'ses_one'));
  await owner.event(event('session.updated', 'ses_two'));
  assert.equal(owner.binding('ses_one'), 'epoch.owned');
  assert.equal(owner.binding('forged'), '');
  await owner.event({event: {type: 'message.part.updated', properties: {part: {sessionID: 'forged'}}}});
  await owner.event({event: {type: 'session.created', properties: {info: {id: 'other', directory: 'other-project'}}}});
  assert.deepEqual(calls.at(-1)[1].contexts.map(v => v.name), ['ses_one', 'ses_two']);
  assert.deepEqual(calls.at(-1)[1].interfaces, []);
  assert.equal(timers.size, 1);
  const renew = [...timers.values()][0]; timers.clear(); renew(); await owner.settled();
  assert.equal(calls.at(-1)[1].instance, 'epoch.owned');
  await owner.event(event('session.deleted', 'ses_one'));
  await owner.event({event: {type: 'server.instance.disposed', properties: {directory: 'owned-project'}}});
  assert.deepEqual(calls.at(-1), ['withdraw', 'opencode', 'epoch.owned']);
  assert.equal(timers.size, 0);
  assert.equal(owner.binding('ses_one'), '');
  const n = calls.length;
  await owner.event(event('session.created', 'late'));
  assert.equal(calls.length, n);
});

test('refusal stays absent without retry; stale epoch restarts only on a host event', async () => {
  const {owner, calls, timers} = fixture([{outcome: 'forbidden'}, {outcome: 'applied', instance: 'old'}, {outcome: 'stale'}, {outcome: 'applied', instance: 'new'}]);
  await owner.event(event('session.created', 'one'));
  assert.equal(timers.size, 0);
  await owner.event(event('session.updated', 'one'));
  await owner.event(event('session.updated', 'one'));
  assert.equal(timers.size, 0);
  await owner.event(event('session.updated', 'one'));
  assert.equal(calls.at(-1)[1].instance, '');
  await owner.close();
  assert.deepEqual(calls.at(-1), ['withdraw', 'opencode', 'new']);
});

test('a forbidden announce asks the Panel to register once per owner; stale or applied never asks', async () => {
  const calls = [], outcomes = [{outcome: 'stale'}, {outcome: 'forbidden'}, {outcome: 'forbidden'}, {outcome: 'applied', instance: 'later'}];
  let requests = 0;
  const client = {announce: async value => { calls.push(value); return outcomes.shift() ?? {outcome: 'forbidden'}; },
    withdraw: async () => ({outcome: 'applied'})};
  const owner = createApplicationPresence({directory: 'owned-project', resolveClient: async () => client,
    schedule: () => 1, unschedule: () => {}, refused: async () => { requests += 1; throw new Error('Panel missing'); }});
  await owner.event(event('session.created', 'one'));
  assert.equal(requests, 0, 'a stale epoch is no refusal');
  for (let i = 0; i < 4; i++) await owner.event(event('session.updated', 'one'));
  assert.equal(requests, 1, 'one request however often OpenCode is refused');
  assert.equal(calls.length, 5);
});

test('bounds and teardown during an in-flight announce', async () => {
  const f = fixture();
  for (let i = 0; i < 40; i++) await f.owner.event(event('session.created', 's'+i));
  assert.equal(f.calls.at(-1)[1].contexts.length, 32);
  await f.owner.close();
  const budget = fixture();
  for (let i = 0; i < 32; i++) await budget.owner.event(event('session.created', String(i).padEnd(64, 'x')));
  const bounded = budget.calls.at(-1)[1];
  assert.ok(bounded.contexts.length < 32, 'byte bound applies before count bound');
  assert.ok(Buffer.byteLength(JSON.stringify({...bounded, instance: 'x'.repeat(128), leaseMs: 30000})) <= 2048);
  await budget.owner.close();
  const g = fixture(); let finish;
  g.client.announce = () => new Promise(resolve => { finish = resolve; });
  const first = g.owner.event(event('session.created', 'one'));
  await Promise.resolve();
  g.owner.close(); finish({outcome: 'applied', instance: 'late'}); await first;
  assert.deepEqual(g.calls.at(-1), ['withdraw', 'opencode', 'late']);
  assert.equal(g.timers.size, 0);
});

test('actual generated ApplicationsClient carries the bounded presence frame', async () => {
  const frames = [];
  let renew;
  const generated = new ApplicationsClient({exchangeFrame: async frame => {
    const request = JSON.parse(new TextDecoder().decode(frame)); frames.push(request);
    return new TextEncoder().encode(JSON.stringify({version: 1, service: request.service, method: request.method,
      ok: true, payload: {value: {outcome: 'applied', instance: 'x'.repeat(128), reason: ''}}}));
  }});
  const owner = createApplicationPresence({directory: 'owned-project', resolveClient: async () => generated,
    schedule: fn => { renew = fn; return 1; }, unschedule() {}});
  const native = 'ses_e94dfb2daffemZukxH7YVqW6JH';
  await owner.event(event('session.created', native));
  assert.equal(frames[0].service, 'abstraction.facade/applications@2');
  assert.equal(frames[0].method, 'Announce');
  assert.equal(frames[0].arguments.presence.contexts[0].name, native);
  for (let i = 0; i < 32; i++) await owner.event(event('session.created', String(i).padEnd(64, 'x')));
  renew(); await owner.settled();
  const renewed = frames.at(-1).arguments.presence;
  assert.equal(renewed.instance.length, 128);
  assert.ok(Buffer.byteLength(JSON.stringify(renewed)) <= 2048, 'actual generated decoded presence fits receiving JSON byte bound');
  await owner.close();
  assert.equal(frames.at(-1).method, 'Withdraw');
});
