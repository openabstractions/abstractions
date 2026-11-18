import test from 'node:test';
import assert from 'node:assert/strict';
import {resolve as resolvePath} from 'node:path';
import {tool} from '@opencode-ai/plugin/tool';
import {createMessages} from '../messages.js';
import {broadRoot, createReplyOnlyGuard, insideFolder, replyOnlyReason} from '../reply-only.js';
import OpenAbstractionsPlugin from '../plugin.js';

// S5 architect ruling 2026-11-22, OpenCode: a peer message wakes the chat;
// the woken turn's edit and shell are refused with the reason; read and reply
// succeed; the person's pair toggle lifts the limit; the person's own turn is
// unaffected.

const result = value => ({structuredContent: value});
const settle = async (condition, what) => {
  for (const until = Date.now() + 3000; !condition();) {
    if (Date.now() > until) throw Error('timed out waiting for ' + what);
    await new Promise(resolve => setTimeout(resolve, 2));
  }
};
const project = resolvePath('owned-project');
// The host resolves links; one inside the project lands outside it.
const links = {[resolvePath(project, 'link')]: resolvePath('elsewhere')};
const resolveReal = async path => {
  for (const [link, target] of Object.entries(links)) if (path === link || path.startsWith(link + '\\') || path.startsWith(link + '/')) return target + path.slice(link.length);
  return resolvePath(path);
};

function inbox() {
  const state = {revision: 0, wake: '', high: '', outstanding: '', tickets: 0, changed: null, fullTools: false, marked: false};
  return {state,
    deliver() { state.revision++; state.wake = String(state.revision).padStart(8, '0'); state.changed?.(); state.changed = null; },
    async call(name, args, options) {
      if (name === 'oa_messages_wait') { if (args.wake_ticket === state.outstanding || !args.wake_ticket) state.outstanding = ''; return result({outcome: 'page', messages: []}); }
      if (name === 'oa_messages_reply') return result({outcome: 'sent', message_id: 'r1'});
      if (name !== 'oa_messages_watch') return result({outcome: 'page'});
      if (args.turn === 'person') state.marked = false;
      if (args.wait_ms > 0 && args.revision === String(state.revision)) await new Promise(done => { state.changed = done; options.signal.addEventListener('abort', done, {once: true}); });
      const page = {outcome: 'page', context: 'chat-A', revision: String(state.revision), wake: state.wake, pending_until_unix_ms: 0, reply_until_unix_ms: 0};
      if (state.wake) page.wake_sender = 'Claude';
      if (args.reserve_wake && args.reserve_wake === state.wake && state.wake > state.high && !state.outstanding) {
        const ticket = (++state.tickets).toString(16).padStart(8, '0');
        state.high = state.wake; state.outstanding = ticket; state.marked = true;
        page.wake_ticket = ticket;
        page.wake_pointer = `OpenAbstractions: message from Claude. Read it with oa_messages_wait, ticket ${ticket}.`;
      }
      // The helper answers the pair rule live while its turn is marked.
      if (state.marked) page.peer_full_tools = state.fullTools;
      return result(page);
    }};
}

function fixture() {
  const box = inbox(), prompts = [];
  const messages = createMessages({tool, directory: project, environment: {}, helperProgram: () => 'C:/kept/oa-opencode-integration.exe', wake: true,
    schedule: (fn, ms) => setTimeout(fn, Math.min(ms, 5)), unschedule: clearTimeout,
    client: {session: {
      get: async ({path}) => path.id === 'ses_A' ? {data: {id: 'ses_A', directory: project, title: ''}} : {error: 'missing'},
      status: async () => ({data: {}}),
      messages: async () => ({data: [{info: {role: 'user', agent: 'build', model: {providerID: 'p', modelID: 'm'}}}, {info: {role: 'assistant'}}]}),
      promptAsync: async request => { prompts.push(request); return {data: undefined}; },
    }, tui: {showToast: async () => {}}},
    open: ({id}) => {
      let ended = false;
      return {initialize: async () => ({}), isClosed: () => ended,
        async call(name, args, options) {
          if (name === 'oa_opencode_session_start') return result({outcome: 'observed', session_id: id, context: 'chat-A', adapter: 'accepted'});
          return box.call(name, args, options);
        }, async close() { ended = true; }};
    }});
  const status = type => messages.event({event: {type: 'session.status', properties: {sessionID: 'ses_A', status: {type}}}});
  const guard = createReplyOnlyGuard({messages, directory: project, resolveReal});
  const run = async (name, args) => { try { await guard({tool: name, sessionID: 'ses_A', callID: 'c'}, {args}); return 'ran'; } catch (error) { return error.message; } };
  return {box, prompts, messages, status, run};
}

async function woken() {
  const f = fixture();
  await f.status('idle');
  f.box.deliver();
  await settle(() => f.prompts.length === 1, 'wake pointer');
  await f.status('busy');
  return f;
}

const reason = replyOnlyReason('Claude');

test('S5 OpenCode: a peer message wakes the chat; edit and bash are refused with the reason; read and reply succeed', async () => {
  const f = await woken();
  try {
    assert.equal(reason, 'This turn was started by a message from Claude. Ask the person to allow more for this conversation.');
    for (const [name, args] of [['edit', {filePath: resolvePath(project, 'main.go'), oldString: 'a', newString: 'b'}], ['bash', {command: 'go build'}],
      ['write', {filePath: resolvePath(project, 'x'), content: 'x'}], ['apply_patch', {patchText: 'x'}], ['webfetch', {url: 'https://example.invalid'}],
      ['websearch', {query: 'q'}], ['task', {prompt: 'p'}], ['other-server_tool', {}], ['oa_messages_send', {receiver: 'Claude', body: 'x'}],
      ['oa_image_generate', {prompt: 'x'}], ['read', {filePath: resolvePath('outside', 'secret.txt')}], ['read', {filePath: '../secret.txt'}],
      ['read', {filePath: resolvePath(project, 'link', 'secret.txt')}], ['glob', {pattern: '../**'}], ['grep', {pattern: 'x', include: '{a,/etc/*}'}],
      ['list', {path: resolvePath('outside')}]]) {
      assert.equal(await f.run(name, args), reason, name + ' was not refused');
    }
    for (const [name, args] of [['read', {filePath: resolvePath(project, 'main.go')}], ['read', {filePath: 'src/app.js'}], ['glob', {pattern: '**/*.go'}],
      ['grep', {pattern: 'func main', include: '*.go'}], ['list', {}], ['oa_messages_wait', {wait_ms: 0}], ['oa_messages_reply', {message_id: 'm1', body: 'answer'}],
      ['oa_models_list', {}]]) {
      assert.equal(await f.run(name, args), 'ran', name + ' was refused');
    }
  } finally { await f.messages.close(); }
});

test('S5 OpenCode: after the pair toggle is turned on, the same message turn may edit', async () => {
  const f = await woken();
  try {
    const edit = ['edit', {filePath: resolvePath(project, 'main.go'), oldString: 'a', newString: 'b'}];
    assert.equal(await f.run(...edit), reason);
    f.box.state.fullTools = true;
    assert.equal(await f.run(...edit), 'ran', 'the toggle did not lift the limit');
    assert.equal(await f.run('bash', {command: 'go build'}), 'ran');
  } finally { await f.messages.close(); }
});

test('S5 OpenCode: the person typing in the chat gives a normal turn', async () => {
  const f = await woken();
  try {
    const edit = ['edit', {filePath: resolvePath(project, 'main.go'), oldString: 'a', newString: 'b'}];
    assert.equal(await f.run(...edit), reason);
    await f.status('idle');
    // The woken turn ended; nothing runs until the person types.
    assert.equal(await f.run(...edit), reason, 'the mark ended before the person typed');
    await f.status('busy');
    assert.equal(await f.run(...edit), 'ran', 'the person turn was restricted');
    assert.equal(await f.run('bash', {command: 'go test'}), 'ran');
  } finally { await f.messages.close(); }
});

test('S5 OpenCode: the plugin registers the guard, and a chat no peer woke is unaffected', async () => {
  const plugin = await OpenAbstractionsPlugin({client: {}, directory: project}, {}, {tool, env: {}, connectionRequired: false, messagesWake: false, resolveReal});
  try {
    assert.equal(typeof plugin['tool.execute.before'], 'function');
    await plugin['tool.execute.before']({tool: 'bash', sessionID: 'ses_unwoken', callID: 'c'}, {args: {command: 'ls'}});
  } finally { await plugin.dispose(); }
  assert.equal(await insideFolder(project, resolvePath(project, 'a', 'b.txt'), resolveReal), true);
  assert.equal(await insideFolder(project, '\\\\server\\share\\x', resolveReal), false);
  assert.equal(await insideFolder(project, 'D:relative', resolveReal), false);
});

test('S5 review OpenCode: full use is asked again after every read', async () => {
  const f = await woken();
  try {
    const edit = ['edit', {filePath: resolvePath(project, 'main.go'), oldString: 'a', newString: 'b'}];
    f.box.state.fullTools = true;
    assert.equal(await f.run(...edit), 'ran');
    // The next read brings a message from a sender whose pair is off.
    assert.equal(await f.run('oa_messages_wait', {wait_ms: 0}), 'ran');
    f.box.state.fullTools = false;
    assert.equal(await f.run(...edit), reason, 'a cached full use outlived the read');
    // The person switches the pair on and then off: the next read applies it.
    f.box.state.fullTools = true;
    assert.equal(await f.run(...edit), 'ran');
    f.box.state.fullTools = false;
    await f.run('oa_messages_wait', {wait_ms: 0});
    assert.equal(await f.run('bash', {command: 'ls'}), reason, 'switching the pair off did not apply at the next read');
  } finally { await f.messages.close(); }
});

test('S5 review OpenCode: a drive root or a home folder is no project, and the refusal says why', async () => {
  for (const root of ['C:\\', 'C:', 'D:/', 'C:\\Users\\someone', 'c:/users/someone/', '/', '/home/someone', '/Users/someone']) {
    assert.equal(broadRoot(root), true, root);
  }
  for (const root of ['C:\\Users\\someone\\project', '/home/someone/project', 'D:\\work']) {
    assert.equal(broadRoot(root), false, root);
  }
  const mark = {sender: 'Claude', full: false};
  const home = process.platform === 'win32' ? 'C:\\Users\\someone' : '/home/someone';
  const guard = createReplyOnlyGuard({messages: {replyOnly: () => mark, peerFullTools: async () => false}, directory: home, resolveReal: async path => resolvePath(path)});
  for (const [name, args] of [['read', {filePath: 'notes.txt'}], ['glob', {pattern: '**/*'}], ['grep', {pattern: 'password'}], ['list', {}]]) {
    await assert.rejects(guard({tool: name, sessionID: 's', callID: 'c'}, {args}),
      {message: reason + ' File reads are off because this chat\'s folder is a whole drive or a home folder.'}, name);
  }
  await guard({tool: 'oa_messages_reply', sessionID: 's', callID: 'c'}, {args: {message_id: 'm1', body: 'x'}});
});

test('S5 decision OpenCode: only the plugin\'s own pointer submission marks a turn', async () => {
  // A turn the plugin did not start, whatever its text (an imitated pointer
  // typed or sent by another program included), is a normal turn.
  const f = fixture();
  try {
    await f.status('idle');
    await f.status('busy');
    assert.equal(await f.run('bash', {command: 'ls'}), 'ran', 'a turn the plugin did not start was marked');
  } finally { await f.messages.close(); }
  const w = await woken();
  try {
    assert.equal(await w.run('bash', {command: 'ls'}), reason);
    await w.status('idle');
    await w.status('busy');
    assert.equal(await w.run('bash', {command: 'ls'}), 'ran', 'the next turn the plugin did not start kept the mark');
  } finally { await w.messages.close(); }
});
