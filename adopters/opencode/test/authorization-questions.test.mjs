import assert from 'node:assert/strict';
import test from 'node:test';
import {createAuthorizationQuestionsTool} from '../authorization-questions.js';
import OpenAbstractionsPlugin from '../plugin.js';

function fakeTool(definition) { return definition; }
const optional = {optional() { return this; }};
fakeTool.schema = {string: () => optional, enum: () => optional,
  object: () => optional, array: () => optional, number: () => optional, boolean: () => optional};
const question = {id: 'q-one', text: 'The editor wants to use a local model', about: 'editor',
  options: ['allow', 'refuse'], asked: '2026-09-30T12:00:00Z'};
function fixture(overrides = {}) {
  const calls = [];
  const responder = {
    async listAnswerable(cursor, limit) {
      calls.push(['list', cursor, limit]);
      return {outcome: 'page', records: [question], next: '', complete: true};
    },
    async answer(id, option) {
      calls.push(['answer', id, option]);
      return {outcome: 'answered', effect: 'applied', targetOutcome: 'permitted'};
    },
    ...overrides,
  };
  return {calls, tool: createAuthorizationQuestionsTool({tool: fakeTool,
    resolveResponder: async () => responder})};
}

test('lists only runtime-filtered questions with a bounded page', async () => {
  const {calls, tool} = fixture();
  const result = JSON.parse(await tool.execute({action: 'list'}, {}));
  assert.deepEqual(result.records, [question]);
  assert.deepEqual(calls, [['list', '', 16n]]);
});

test('answers only a freshly listed option under the Panel enrollment and tells the person', async () => {
  const {calls, tool} = fixture();
  const result = JSON.parse(await tool.execute({action: 'answer', id: 'q-one', option: 'allow'},
    {ask: async () => { throw new Error('per-call approval must not be used'); }}));
  assert.deepEqual(calls.map((call) => call[0]), ['list', 'answer']);
  assert.deepEqual({outcome: result.outcome, effect: result.effect}, {outcome: 'answered', effect: 'applied'});
  assert.match(result.shownToPerson, /OpenCode answered Allow/);
  assert.match(result.shownToPerson, /Undo it in the OpenAbstractions Panel/);
});

test('unlisted option and question never invoke Answer', async () => {
  const {calls, tool} = fixture();
  assert.equal(JSON.parse(await tool.execute({action: 'answer', id: 'q-one', option: 'never'},
    {ask: async () => { throw new Error('unexpected ask'); }})).outcome, 'not_answerable');
  assert.equal(JSON.parse(await tool.execute({action: 'answer', id: 'elsewhere', option: 'allow'},
    {ask: async () => { throw new Error('unexpected ask'); }})).outcome, 'not_answerable');
  assert.equal(calls.some((call) => call[0] === 'answer'), false);
});

test('cancellation stops before Answer', async () => {
  const {calls, tool} = fixture();
  const controller = new AbortController();
  controller.abort();
  assert.equal(await tool.execute({action: 'answer', id: 'q-one', option: 'allow'},
    {abort: controller.signal}), 'Authorization answer cancelled.');
  assert.equal(calls.some((call) => call[0] === 'answer'), false);
});

test('notification failure does not replay a recorded answer', async () => {
  const calls = [];
  const wrapped = createAuthorizationQuestionsTool({tool: fakeTool,
    resolveResponder: async () => ({
      async listAnswerable() { return {outcome: 'page', records: [question], next: '', complete: true}; },
      async answer(id, option) { calls.push([id, option]); return {outcome: 'answered', effect: 'applied'}; },
    }),
    announce: async () => { throw new Error('toast unavailable'); },
  });
  const result = JSON.parse(await wrapped.execute({action: 'answer', id: 'q-one', option: 'allow'}, {}));
  assert.equal(result.outcome, 'answered');
  assert.deepEqual(calls, [['q-one', 'allow']]);
});

test('tool rejects caller-supplied target authority and does not retry an uncertain answer', async () => {
  const {calls, tool} = fixture({async answer() { calls.push(['answer']); throw new Error('transport uncertain'); }});
  await assert.rejects(tool.execute({action: 'answer', id: 'q-one', option: 'allow', targetProgram: 'someone'},
    {ask: async () => {}}), /Unsupported/);
  await assert.rejects(tool.execute({action: 'answer', id: 'q-one', option: 'allow'},
    {ask: async () => {}}), /transport uncertain/);
  assert.equal(calls.filter((call) => call[0] === 'answer').length, 1);
});

test('recorded answer with a conflicting rights effect stays visibly partial', async () => {
  const {tool} = fixture({async answer() {
    return {outcome: 'answered', effect: 'conflict', targetOutcome: 'not_granted'};
  }});
  const result = JSON.parse(await tool.execute({action: 'answer', id: 'q-one', option: 'allow'},
    {ask: async () => {}}));
  assert.deepEqual({outcome: result.outcome, effect: result.effect, rightsEffectApplied: result.rightsEffectApplied,
    targetOutcome: result.targetOutcome},
  {outcome: 'answered', effect: 'conflict', rightsEffectApplied: false, targetOutcome: 'not_granted'});
});

test('registered OpenCode tool binds responder to configured OA runtime', async () => {
  const calls = [];
  const plugin = await OpenAbstractionsPlugin({client: {}}, {tools: {authorization_questions: true}}, {
    tool: fakeTool, readInventory: async () => ({offers: []}),
    resolveQuestionResponder: async (options, signal) => {
      calls.push([options.runtimeEndpoint, signal]);
      return {async listAnswerable() { return {outcome: 'page', records: [question], next: '', complete: true}; }};
    },
  });
  await plugin.config({provider: {openabstractions: {
    npm: '@openabstractions/opencode', options: {runtimeEndpoint: 'isolated-hp4', scope: 'local'}, models: {},
  }}});
  const controller = new AbortController();
  const result = JSON.parse(await plugin.tool.openabstractions_authorization_questions.execute({action: 'list'},
    {abort: controller.signal}));
  assert.equal(result.records[0].id, 'q-one');
  assert.deepEqual(calls, [['isolated-hp4', controller.signal]]);
});
