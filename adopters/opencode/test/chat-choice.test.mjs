// The chat model OpenCode uses at start: the one the person chose for it in
// OpenAbstractions (its own oa-chat-default selection policy, written by the
// Panel's generic per-app chooser), or OA's default when none is chosen
// (VISION 2026-10-06, adopter-boundary move plan step 5).
import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {chatModelAtStart, defaultChatFamily, defaultSelectionPolicies} from '../inventory.js';
import OpenAbstractionsPlugin from '../plugin.js';

function offer(family, context = 32768) {
  return {family, names: [family], servers: ['local'], context: BigInt(context), contextSource: 'effective',
    input: BigInt(Math.max(context - 4096, 0)), unknown: []};
}

const fakeTool = Object.assign((definition) => definition, {schema: {
  object: (value) => value,
  string: () => ({optional() { return this; }}),
  number: () => ({optional() { return this; }}),
  boolean: () => ({optional() { return this; }}),
  enum: (value) => ({value, optional() { return this; }}),
  array: (value) => ({value, optional() { return this; }}),
}});

const found = (name) => ({outcome: 'found', revision: 'r1', policy: {name, candidates: [{model: 'qwen', placement: 'local', credential: ''}]}});

/** Runs the plugin's start with the given config model and policy read;
 * returns the config after the hook and the plugin's log entries. */
async function start(model, read) {
  const logs = [];
  const asked = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async ({body}) => logs.push(body)}}}, {}, {
    tool: fakeTool, readCapability: async () => false,
    readInventory: async () => ({offers: [offer('qwen'), offer('gemma')]}),
    readSelectionPolicy: async (_options, name) => { asked.push(name); return typeof read === 'function' ? read(name) : read; },
  });
  const config = {...(model === undefined ? {} : {model}),
    provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models: {}}}};
  await plugin.config(config);
  return {config, logs, asked};
}

test('chatModelAtStart: a listed chat policy replaces a model under the OA provider only', () => {
  const discovered = {qwen: {}, gemma: {}};
  const base = {providerID: 'openabstractions', listed: true, read: found('oa-chat-default'), discovered};
  assert.equal(chatModelAtStart({...base, selected: 'openabstractions/qwen'}), 'openabstractions/oa-chat');
  assert.equal(chatModelAtStart({...base, selected: ''}), 'openabstractions/oa-chat');
  assert.equal(chatModelAtStart({...base, selected: 'openabstractions/oa-chat'}), '', 'already selected');
  assert.equal(chatModelAtStart({...base, selected: 'anthropic/claude'}), '', 'another provider\'s model stays');
  assert.equal(chatModelAtStart({...base, selected: {providerID: 'x'}}), '', 'a model the plugin cannot read stays');
});

test('chatModelAtStart: with no policy stored, OA\'s default fills an empty or vanished choice', () => {
  const discovered = {qwen: {}, gemma: {}};
  const base = {providerID: 'openabstractions', listed: false, read: {outcome: 'unknown'}, discovered};
  assert.equal(defaultChatFamily(discovered), 'gemma', 'the first family by name, as configure.js writes it');
  assert.equal(chatModelAtStart({...base, selected: ''}), 'openabstractions/gemma');
  assert.equal(chatModelAtStart({...base, selected: 'openabstractions/oa-chat'}), 'openabstractions/gemma');
  assert.equal(chatModelAtStart({...base, selected: 'openabstractions/qwen'}), '', 'the app\'s own choice stays');
  assert.equal(chatModelAtStart({...base, selected: '', discovered: {}}), '', 'no servable family, no default');
});

test('chatModelAtStart: an unreadable or ineligible policy keeps the configured model', () => {
  const base = {providerID: 'openabstractions', listed: false, discovered: {qwen: {}}};
  for (const outcome of ['forbidden', 'unavailable', 'invalid', 'found']) {
    assert.equal(chatModelAtStart({...base, read: {outcome}, selected: ''}), '', outcome);
    assert.equal(chatModelAtStart({...base, read: {outcome}, selected: 'openabstractions/oa-chat'}), '', outcome);
  }
  assert.equal(chatModelAtStart({...base, read: null, selected: ''}), '');
});

test('at start OpenCode uses the chat model the person chose for it in OpenAbstractions', async () => {
  const {config, logs, asked} = await start('openabstractions/qwen', found);
  assert.deepEqual(asked, ['oa-chat-default'], 'the plugin reads its own policy through the selection service');
  assert.equal(config.model, 'openabstractions/oa-chat');
  assert.equal(config.provider.openabstractions.options.oaSelectionPolicies['oa-chat'], 'oa-chat-default',
    'requests on that entry name the policy, so the runtime applies the stored candidates');
  assert.ok(logs.some((entry) => entry.level === 'info' && entry.message === 'OpenCode uses the chat model chosen for it in OpenAbstractions'));
  assert.equal(logs.some((entry) => entry.message.includes('no longer in the current inventory')), false);
});

test('at start a model under another provider stays when a chat policy is stored', async () => {
  const {config} = await start('anthropic/claude', found);
  assert.equal(config.model, 'anthropic/claude');
  assert.ok(config.provider.openabstractions.models['oa-chat'], 'the chosen model is still listed');
});

test('at start with no chat policy OpenCode keeps its own choice, or takes OA\'s default when it has none', async () => {
  const unknown = {outcome: 'unknown'};
  assert.equal((await start('openabstractions/qwen', unknown)).config.model, 'openabstractions/qwen');
  assert.equal((await start(undefined, unknown)).config.model, 'openabstractions/gemma');
  const vanished = await start('openabstractions/oa-chat', unknown);
  assert.equal(vanished.config.model, 'openabstractions/gemma', 'the choice was removed in OpenAbstractions');
  assert.ok(vanished.logs.some((entry) => entry.message.includes('OpenCode uses OpenAbstractions\' default')));
});

test('at start an unreadable chat policy keeps the configured model and says why in the log', async () => {
  const {config, logs} = await start('openabstractions/qwen', {outcome: 'forbidden', revision: ''});
  assert.equal(config.model, 'openabstractions/qwen');
  const entry = logs.find((item) => item.message.startsWith('OpenAbstractions could not read the chat model chosen for OpenCode'));
  assert.ok(entry && entry.level === 'info', 'one info line, never a warning or toast');
  assert.equal(entry.extra.reason, 'forbidden');
  assert.equal(entry.extra.policy, 'oa-chat-default');
});

test('the chat policy the plugin reads is the one the Panel\'s generic chooser writes', () => {
  const panel = readFileSync(new URL('../../../monitor/selection_page.go', import.meta.url), 'utf8');
  assert.ok(panel.includes(`chat:'${defaultSelectionPolicies.chat}'`), 'monitor/selection_page.go policyNames.chat');
});
