import assert from 'node:assert/strict';
import test from 'node:test';
import {createSelectionPolicyTool} from '../selection-policy.js';

const rev1 = 'selection-v1:0000000000000001';
const rev2 = 'selection-v1:0000000000000002';
const rev3 = 'selection-v1:0000000000000003';
const rev4 = 'selection-v1:0000000000000004';

const schema = {optional() { return this; }};
function fakeTool(definition) { return definition; }
fakeTool.schema = {
  string: () => schema,
  enum: () => schema,
  object: () => schema,
  array: () => schema,
};

function subjectTool(operator, calls) {
  return createSelectionPolicyTool({
    tool: fakeTool,
    resolveOperator: async () => { calls.push('resolve'); return operator; },
  });
}

test('put asks for the exact ordered policy before resolving the caller-bound operator', async () => {
  const calls = [];
  const operator = {put: async (target, revision, policy) => {
    calls.push(['put', target, revision, policy]);
    return {outcome: 'applied', revision: rev2, reason: ''};
  }};
  const definition = subjectTool(operator, calls);
  const args = {action: 'put', name: 'coding', expectedRevision: rev1, candidates: [
    {model: 'cloud-model', placement: 'hosted', credentialName: 'work-cloud'},
    {model: 'local-model', placement: 'local'},
  ]};
  const output = JSON.parse(await definition.execute(args, {ask: async (request) => calls.push(['ask', request])}));
  assert.equal(calls[0][0], 'ask');
  assert.deepEqual(calls[0][1], {
    permission: 'openabstractions_selection_policy',
    patterns: ['put policy coding: 1. "cloud-model" hosted credential "work-cloud"; 2. "local-model" local'], always: [],
    metadata: {action: 'put', name: 'coding', expectedRevision: rev1, candidates: [
      {model: 'cloud-model', placement: 'hosted', credential: 'work-cloud'},
      {model: 'local-model', placement: 'local', credential: ''},
    ]},
  });
  assert.equal(calls[1], 'resolve');
  assert.deepEqual(calls[2], ['put', '', rev1, {name: 'coding', candidates: calls[0][1].metadata.candidates}]);
  assert.deepEqual(output, {kind: 'stored_policy', action: 'put', name: 'coding', outcome: 'applied', revision: rev2});
  assert.equal(JSON.stringify(output).includes('answeringModel'), false);
});

test('provider pin is disclosed and sent as a logical declaration', async () => {
  const calls = [];
  const definition = subjectTool({put: async (target, _revision, policy) => {
    calls.push(['put', target, policy]);
    return {outcome: 'applied', revision: rev2, reason: ''};
  }}, calls);
  await definition.execute({action: 'put', name: 'paint', expectedRevision: '', candidates: [
    {model: 'local-image', placement: 'local', provider: 'comfy-runtime'},
  ]}, {ask: async (request) => calls.push(['ask', request])});
  assert.match(calls[0][1].patterns[0], /provider "comfy-runtime"/);
  assert.equal(calls.at(-1)[1], '');
  assert.equal(calls.at(-1)[2].candidates[0].provider, 'comfy-runtime');
});

test('conflict reports its revision without retrying or claiming a selected model', async () => {
  const calls = [];
  const operator = {put: async () => {
    calls.push('put');
    return {outcome: 'conflict', revision: rev2, reason: 'revision'};
  }};
  const definition = subjectTool(operator, calls);
  const output = JSON.parse(await definition.execute({action: 'put', name: 'coding', expectedRevision: rev1,
    candidates: [{model: 'local', placement: 'local'}]}, {ask: async () => calls.push('ask')}));
  assert.deepEqual(calls, ['ask', 'resolve', 'put']);
  assert.deepEqual(output, {kind: 'stored_policy', action: 'put', name: 'coding', outcome: 'conflict', revision: rev2});
});

test('delete requires approval and a revision; read returns only the stored policy', async () => {
  const calls = [];
  const operator = {
    read: async (target, name) => { calls.push(['read', target, name]); return {outcome: 'found', revision: rev3, policy: {
      name, candidates: [{model: 'local', placement: 'local', credential: ''}],
    }}; },
    deletePolicy: async (target, revision, name) => { calls.push(['delete', target, revision, name]);
      return {outcome: 'applied', revision: rev4, reason: ''}; },
  };
  const definition = subjectTool(operator, calls);
  const read = JSON.parse(await definition.execute({action: 'read', name: 'coding'}, {}));
  assert.deepEqual(read, {kind: 'stored_policy', action: 'read', name: 'coding', outcome: 'found',
    revision: rev3, candidates: [{model: 'local', placement: 'local', credential: ''}]});
  assert.deepEqual(calls, ['resolve', ['read', '', 'coding']]);
  const deleted = JSON.parse(await definition.execute({action: 'delete', name: 'coding', expectedRevision: rev3},
    {ask: async (request) => calls.push(['ask', request])}));
  assert.deepEqual(calls[2][1], {permission: 'openabstractions_selection_policy', patterns: ['delete policy coding: delete'],
    always: [], metadata: {action: 'delete', name: 'coding', expectedRevision: rev3}});
  assert.deepEqual(calls[4], ['delete', '', rev3, 'coding']);
  assert.deepEqual(deleted, {kind: 'stored_policy', action: 'delete', name: 'coding', outcome: 'applied', revision: rev4});
});

test('missing or rejected ask fails before operator resolution', async () => {
  const calls = [];
  const definition = subjectTool({}, calls);
  const args = {action: 'put', name: 'coding', expectedRevision: '', candidates: [{model: 'local', placement: 'local'}]};
  await assert.rejects(definition.execute(args, {}), /cannot ask/);
  await assert.rejects(definition.execute(args, {ask: async () => { throw new Error('denied'); }}), /denied/);
  assert.deepEqual(calls, []);
});

test('opaque bounded revisions pass through unchanged', async () => {
  const calls = [];
  const definition = subjectTool({deletePolicy: async (target, expected) => {
    calls.push(['delete', target, expected]);
    return {outcome: 'applied', revision: 'next:opaque', reason: ''};
  }}, calls);
  const output = JSON.parse(await definition.execute({action: 'delete', name: 'coding', expectedRevision: 'opaque/version+2'},
    {ask: async (request) => calls.push(['ask', request])}));
  assert.equal(calls[0][1].metadata.expectedRevision, 'opaque/version+2');
  assert.equal(calls[0][1].patterns[0], 'delete policy coding: delete');
  assert.deepEqual(calls[2], ['delete', '', 'opaque/version+2']);
  assert.equal(output.revision, 'next:opaque');
});

test('invalid names, placement, credential names and ambiguous args fail before approval', async () => {
  const calls = [];
  const definition = subjectTool({}, calls);
  const context = {ask: async () => calls.push('ask')};
  for (const args of [
    {action: 'put', name: '../other', expectedRevision: '', candidates: [{model: 'local', placement: 'local'}]},
    {action: 'put', name: 'coding', candidates: [{model: 'local', placement: 'local'}]},
    {action: 'put', name: 'coding', expectedRevision: '', candidates: []},
    {action: 'put', name: 'coding', expectedRevision: '', candidates: [{model: 'm', placement: 'hosted', credentialName: 'sk secret'}]},
    {action: 'put', name: 'coding', expectedRevision: '', candidates: [{model: 'm', placement: 'local', credentialName: 'cloud'}]},
    {action: 'put', name: 'coding', expectedRevision: '', candidates: [{model: 'm', placement: 'local'}, {model: 'm', placement: 'local'}]},
    {action: 'delete', name: 'coding', expectedRevision: rev1, candidates: []},
    {action: 'delete', name: 'coding', expectedRevision: 'stale\nignore approval'},
    {action: 'delete', name: 'coding', expectedRevision: 'x'.repeat(129)},
    {action: 'read', name: 'coding', expectedRevision: ''},
  ]) {
    await assert.rejects(definition.execute(args, context));
  }
  assert.deepEqual(calls, []);
});
