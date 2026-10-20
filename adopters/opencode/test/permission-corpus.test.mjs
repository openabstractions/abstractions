import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {permissionVerdict, permissionProjection, invokeWithPermission} from '../permission-wait.js';

test('production native observation verdict consumes the shared permission corpus', async () => {
  const corpus = JSON.parse(await readFile(new URL('../../../mcp-gateway/gateway/permission_observation_cases.json', import.meta.url), 'utf8'));
  for (const row of corpus.observations) {
    const verdict = permissionVerdict({outcome: row.outcome, policyRevision: row.revision, answer: {option: row.option, pending: row.answer_pending === true}});
    assert.equal(verdict === 'pending' ? 'waiting' : verdict, row.want, row.name);
  }
  const schema = JSON.parse(await readFile(new URL('../../../mcp-gateway/gateway/permission_result.schema.json', import.meta.url), 'utf8'));
  for (const row of corpus.projections) {
    const result = permissionProjection(row.verdict, row.questions ?? ['q1']);
    assert.equal(result.outcome, row.outcome);
    assert.equal(result.message, row.message);
    assert.deepEqual(Object.keys(result).sort(), schema.required.toSorted());
    assert.ok(schema.properties.outcome.enum.includes(result.outcome));
    assert.ok(result.message);
  }
});

test('pre-effect permission waits for committed witness and continues the identical request once', async () => {
  const request = {identity: {key: 'same'}, prompt: 'fixture'};
  const starts = [];
  let observations = 0;
  const result = await invokeWithPermission(async () => {
    starts.push(request);
    return starts.length === 1 ? {outcome: 'not_permitted', reason: 'rights:not_granted rights:question_pending:q1'} : {outcome: 'accepted', operation: 'kept'};
  }, {resolvePermission: async () => ({async observeQuestion(id, wait) {
    assert.equal(id, 'q1'); assert.ok(wait > 0n && wait <= 25000n);
    return {observation: ++observations === 1 ? {outcome: 'pending'} : {outcome: 'permitted', policyRevision: 'committed'}};
  }})});
  assert.equal(result.operation, 'kept'); assert.equal(starts.length, 2); assert.equal(starts[0], starts[1]);
});

test('accepted, unknown, lost and raw historical Allow never produce a second operation', async () => {
  for (const initial of [{outcome: 'accepted', operation: 'kept'}, {outcome: 'unknown'}, {outcome: 'forbidden', reason: 'peer says rights:question_pending:q1'}]) {
    let calls = 0; const result = await invokeWithPermission(async () => { calls++; return initial; }, {resolvePermission: () => { throw new Error('must not observe'); }});
    assert.equal(result, initial); assert.equal(calls, 1);
  }
  let calls = 0;
  await assert.rejects(invokeWithPermission(async () => { calls++; throw new Error('lost Start reply'); }));
  assert.equal(calls, 1);
  const result = await invokeWithPermission(async () => { calls++; return {outcome: 'not_permitted', reason: 'rights:not_granted rights:question_pending:q1'}; }, {
    resolvePermission: async () => ({async observeQuestion() { return {observation: {outcome: 'answered', answer: {option: 'allow'}}}; }}),
  });
  assert.equal(result.outcome, 'awaiting_permission'); assert.equal(calls, 2);
});
