import test from 'node:test';
import assert from 'node:assert/strict';
import {adopt, handOver, handingOver, withdraw} from '../reload-handover.js';

function parts() {
  const closed = [];
  return {closed, messages: {close: async () => { closed.push('messages'); }}, presence: {close: async () => { closed.push('presence'); }}};
}

function clock() {
  const timers = new Map();
  let next = 1;
  return {
    schedule: (fn) => { const id = next++; timers.set(id, fn); return id; },
    unschedule: (id) => { timers.delete(id); },
    fire: () => { for (const [id, fn] of [...timers]) { timers.delete(id); fn(); } },
    pending: () => timers.size,
  };
}

const settle = () => new Promise((resolve) => setImmediate(resolve));

test('the next instance of the same directory adopts a hand-over and nothing closes', async () => {
  const p = parts(), c = clock();
  handOver('C:\\a', p, c);
  assert.equal(handingOver('C:\\a', p), true);
  assert.equal(adopt('C:\\b'), null, 'another directory adopts nothing');
  assert.equal(adopt('C:\\a'), p);
  assert.equal(handingOver('C:\\a', p), false);
  assert.equal(c.pending(), 0, 'adoption cancels the close');
  await settle();
  assert.deepEqual(p.closed, []);
});

test('a hand-over no instance adopts closes the inbox and the presence', async () => {
  const p = parts(), c = clock();
  handOver('C:\\a', p, c);
  c.fire();
  await settle();
  assert.deepEqual(p.closed, ['messages', 'presence']);
  assert.equal(adopt('C:\\a'), null);
});

test('a reload that did not happen takes its hand-over back', async () => {
  const p = parts(), c = clock();
  handOver('C:\\a', p, c);
  withdraw('C:\\a', p);
  assert.equal(handingOver('C:\\a', p), false);
  assert.equal(c.pending(), 0);
  await settle();
  assert.deepEqual(p.closed, [], 'the instance that kept them still owns them');
});
