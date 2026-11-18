import assert from 'node:assert/strict';
import test from 'node:test';
import {tool} from '@opencode-ai/plugin/tool';
import OpenAbstractionsPlugin from '../plugin.js';
import {createReminderKeeper} from '../preload.js';

// OpenCode 1.18 appends this synthetic part to the last user message of every
// plan-agent request (session/reminders.ts) and drops it from that message
// once a newer user message exists.
const plan = '<system-reminder>\n# Plan Mode - System Reminder\n\nCRITICAL: Plan mode ACTIVE - you are in READ-ONLY phase.\n</system-reminder>';
const user = (id, text, ...reminders) => ({info: {id, role: 'user', sessionID: 'ses_A'},
  parts: [{id: `prt_${id}`, type: 'text', text}, ...reminders.map((reminder, index) => ({id: `prt_${id}_r${index}`, type: 'text', text: reminder, synthetic: true}))]});
const assistant = (id, output) => ({info: {id, role: 'assistant', sessionID: 'ses_A', agent: 'plan'},
  parts: [{id: `prt_${id}`, type: 'tool', state: {status: 'completed', output}}, {id: `prt_${id}_t`, type: 'text', text: 'Read it.'}]});
// The order and text of what the engine reads, as OpenCode renders history.
const render = messages => messages.map(message => `<${message.info.role}>` + message.parts.map(part => part.text ?? part.state?.output ?? '').join('\n')).join('\n');
const shared = (a, b) => {
  let n = 0;
  while (n < a.length && n < b.length && a[n] === b[n]) n++;
  return n;
};
const toolOutput = 'line of a file the plan agent read\n'.repeat(600);

// The two requests OpenCode sends: turn one's last step, then turn two's
// first. Turn one's prompt must be a prefix of turn two's, or a hybrid model
// reads turn one again from its newest checkpoint before the change.
function turns() {
  const first = [user('msg_1', 'Plan a cache fix.', plan), assistant('msg_2', toolOutput)];
  const second = [user('msg_1', 'Plan a cache fix.'), assistant('msg_2', toolOutput), user('msg_3', 'Now the tests.', plan)];
  return {first, second};
}

test('the plugin keeps OpenCode\'s plan reminder where the engine read it', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {}, directory: 'owned'}, {}, {tool, env: {}, connectionRequired: false, messagesWake: false});
  try {
    const {first, second} = turns();
    const unchanged = shared(render(first), render(second));
    await plugin['experimental.chat.messages.transform']({}, {messages: first});
    const stored = second[0];
    const sent = {messages: second};
    await plugin['experimental.chat.messages.transform']({}, sent);
    const before = render(first);
    const kept = shared(before, render(sent.messages));
    assert.equal(kept, before.length, 'turn two repeats turn one byte for byte');
    assert.ok(unchanged < 30, `without the keeper the requests part after ${unchanged} characters`);
    assert.equal(sent.messages[0].parts.at(-1).text, plan);
    assert.equal(sent.messages[0].parts.at(-1).synthetic, true);
    assert.notEqual(sent.messages[0], stored);
    assert.equal(stored.parts.length, 1, 'the stored message stays as OpenCode keeps it');
    assert.equal(sent.messages[2].parts.filter(part => part.text === plan).length, 1);
    console.log(`[reminders-prefix] shared prefix ${unchanged} -> ${kept} of ${before.length} characters`);
  } finally {
    await plugin.dispose();
  }
});

test('the keeper adds nothing twice, keeps order, and forgets beyond its bound', () => {
  const keep = createReminderKeeper(2);
  const hint = 'You have 2 new messages.';
  keep({messages: [user('msg_1', 'one', plan, hint)]});
  // A later step of the same turn: nothing to restore, nothing duplicated.
  const again = {messages: [user('msg_1', 'one', plan, hint), assistant('msg_2', 'out')]};
  keep(again);
  assert.deepEqual(again.messages[0].parts.map(part => part.text), ['one', plan, hint]);
  // The next turn: OpenCode dropped both; they return in their order.
  const next = {messages: [user('msg_1', 'one'), assistant('msg_2', 'out'), user('msg_3', 'two')]};
  keep(next);
  assert.deepEqual(next.messages[0].parts.map(part => part.text), ['one', plan, hint]);
  assert.deepEqual(next.messages[2].parts.map(part => part.text), ['two']);
  // A message holding one of the two keeps it and gets the other.
  const partial = {messages: [user('msg_1', 'one', plan), user('msg_3', 'two')]};
  keep(partial);
  assert.deepEqual(partial.messages[0].parts.map(part => part.text), ['one', plan, hint]);
  // Two newer messages push msg_1 out of a keeper bounded at two.
  keep({messages: [user('msg_4', 'three')]});
  keep({messages: [user('msg_5', 'four')]});
  const late = {messages: [user('msg_1', 'one'), user('msg_6', 'five')]};
  keep(late);
  assert.deepEqual(late.messages[0].parts.map(part => part.text), ['one']);
  keep({});
  keep({messages: 'not a list'});
});
