import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import test from 'node:test';
import {setTimeout as delay} from 'node:timers/promises';
import {fileURLToPath} from 'node:url';

async function fixture(t) {
  const path = fileURLToPath(new URL('../fixtures/refresh-model-fixture.mjs', import.meta.url));
  const child = spawn(process.execPath, [path], {env: {...process.env, OA_MODEL_FIXTURE_PORT: '0'}, stdio: ['ignore', 'pipe', 'pipe']});
  t.after(async () => {
    child.kill();
    if (child.exitCode === null) await once(child, 'exit');
  });
  const port = await Promise.race([
    new Promise((resolve, reject) => {
      child.stdout.once('data', (data) => {
        const value = Number(String(data).trim());
        if (Number.isInteger(value) && value > 0) resolve(value);
        else reject(new Error(`invalid fixture port: ${data}`));
      });
      child.once('exit', (code) => reject(new Error(`fixture exited before startup: ${code}`)));
    }),
    delay(5000, undefined, {ref: false}).then(() => { throw new Error('fixture startup timed out'); }),
  ]);
  const base = `http://127.0.0.1:${port}`;
  return {
    base,
    async post(path, body) {
      return fetch(`${base}${path}`, {method: 'POST', headers: {'content-type': 'application/json'}, body: JSON.stringify(body)});
    },
    async state() { return (await fetch(`${base}/control/state`)).json(); },
  };
}

async function waitFor(fixture, count) {
  for (let i = 0; i < 100; i++) {
    const state = await fixture.state();
    if (state.requests.length >= count) return state;
    await delay(10);
  }
  throw new Error(`fixture did not receive request ${count}`);
}

function toolCallId(sse) {
  for (const line of sse.split('\n')) {
    if (!line.startsWith('data: ') || line === 'data: [DONE]') continue;
    const chunk = JSON.parse(line.slice(6));
    const call = chunk.choices?.[0]?.delta?.tool_calls?.[0];
    if (call?.id) {
      assert.equal(call.function.name, 'openabstractions_watch_logs');
      return call.id;
    }
  }
  throw new Error('fixture did not emit a tool call');
}

test('title and unrelated requests preserve the queued call and its exact follow-up', async (t) => {
  const f = await fixture(t);
  const queued = await f.post('/control/tool-call', {sourceId: 'fixture/system', durationMs: 1,
    maxRecords: 1, maxBytes: 4096});
  assert.equal(queued.status, 200);

  const title = f.post('/v1/chat/completions', {model: 'fixture', messages: [{role: 'user', content: 'title'}], tools: []});
  let state = await waitFor(f, 1);
  assert.equal(state.requests[0].branch, 'default');
  assert.equal(state.queuedToolCall, true);
  assert.equal((await title).status, 200);

  const watch = await f.post('/v1/chat/completions', {model: 'fixture', messages: [{role: 'user', content: 'watch'}],
    tools: [{type: 'function', function: {name: 'openabstractions_watch_logs'}}]});
  assert.equal(watch.status, 200);
  const id = toolCallId(await watch.text());
  state = await f.state();
  assert.equal(state.requests[1].branch, 'tool-call');
  assert.equal(state.pendingToolCallId, id);

  const unrelated = f.post('/v1/chat/completions', {model: 'fixture', messages: [{role: 'user', content: 'other'}], tools: []});
  state = await waitFor(f, 3);
  assert.equal(state.requests[2].branch, 'default');
  assert.equal((await unrelated).status, 200);
  assert.equal((await f.state()).pendingToolCallId, id);

  const wrong = await f.post('/v1/chat/completions', {model: 'fixture', messages: [
    {role: 'tool', tool_call_id: 'wrong-id', content: JSON.stringify({kind: 'source_watch', status: 'complete', records: 1})},
  ]});
  assert.equal(wrong.status, 409);
  assert.equal((await f.state()).pendingToolCallId, id);

  const reply = await f.post('/v1/chat/completions', {model: 'fixture', messages: [
    {role: 'assistant', tool_calls: [{id, type: 'function', function: {name: 'openabstractions_watch_logs', arguments: '{}'}}]},
    {role: 'tool', tool_call_id: id, content: JSON.stringify({kind: 'source_watch', status: 'complete', records: 1})},
  ]});
  assert.equal(reply.status, 200);
  assert.match(await reply.text(), /synthetic source watch ended with status complete/);
  state = await f.state();
  assert.equal(state.pendingToolCallId, null);
  assert.deepEqual(state.requests.map((request) => [request.branch, request.status]), [
    ['default', 200], ['tool-call', 200], ['default', 200], ['tool-reply', 409], ['tool-reply', 200],
  ]);
  assert.equal(JSON.stringify(state.requests).includes('title'), false);
  assert.equal(JSON.stringify(state.requests).includes('source_watch'), false);
});

test('inventory controls still change the models served to refresh callers', async (t) => {
  const f = await fixture(t);
  const before = await (await fetch(`${f.base}/api/tags`)).json();
  assert.deepEqual(before.models.map((model) => model.name), ['qwen3:8b', 'phi4:14b']);
  const changed = await f.post('/control/models', {models: ['llama3:8b'], lengths: {'llama3:8b': 8192}});
  assert.equal(changed.status, 200);
  const after = await (await fetch(`${f.base}/api/tags`)).json();
  assert.deepEqual(after.models.map((model) => model.name), ['llama3:8b']);
});

test('ordinary refresh fixture stream remains gated until explicitly released', async (t) => {
  const f = await fixture(t);
  let settled = false;
  const chat = f.post('/v1/chat/completions', {model: 'fixture', messages: [{role: 'user', content: 'wait'}]})
    .then((response) => { settled = true; return response; });
  await waitFor(f, 1);
  await delay(20);
  assert.equal(settled, false);
  await f.post('/control/release-chat', {});
  assert.equal((await chat).status, 200);
});
