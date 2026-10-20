// The chat model OpenCode uses at start: the one the person chose for it in
// OpenAbstractions (its own oa-chat-default selection policy, written by the
// Panel's generic per-app chooser), or OA's default when none is chosen
// (VISION 2026-10-06, adopter-boundary move plan step 5).
import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {chatChoiceModel, chatModelAtStart, defaultChatFamily, defaultSelectionPolicies} from '../inventory.js';
import OpenAbstractionsPlugin from '../plugin.js';
import {tool as hostTool} from '@opencode-ai/plugin/tool';

function offer(family, context = 32768) {
  return {family, names: [family], servers: ['local'], context: BigInt(context), contextSource: 'effective',
    input: BigInt(Math.max(context - 4096, 0)), unknown: []};
}

const found = (name) => ({outcome: 'found', revision: 'r1', policy: {name, candidates: [{model: 'qwen', placement: 'local', credential: ''}]}});

/** Runs the plugin's start with the given config model and policy read;
 * returns the config after the hook and the plugin's log entries. */
async function start(model, read) {
  const logs = [];
  const asked = [];
  const plugin = await OpenAbstractionsPlugin({client: {app: {log: async ({body}) => logs.push(body)}}}, {}, {
    tool: hostTool, probeRuntime: async () => true, readCapability: async () => false,
    readInventory: async () => ({offers: [offer('qwen'), offer('gemma')]}),
    readSelectionPolicy: async (_options, name) => { asked.push(name); return typeof read === 'function' ? read(name) : read; },
  });
  const config = {...(model === undefined ? {} : {model}),
    provider: {openabstractions: {npm: '@openabstractions/opencode', options: {scope: 'local'}, models: {}}}};
  await plugin.config(config);
  return {config, logs, asked};
}

test('chatModelAtStart: OA\'s choice fills a config that names no model, and a person\'s pick stays', () => {
  const discovered = {qwen: {}, gemma: {}};
  const base = {providerID: 'openabstractions', choice: 'openabstractions/qwen', discovered};
  assert.equal(chatModelAtStart({...base, selected: ''}), 'openabstractions/qwen', 'OA\'s choice is the real model');
  assert.equal(chatModelAtStart({...base, selected: 'openabstractions/oa-chat'}), 'openabstractions/qwen',
    'the policy route names no picker entry');
  assert.equal(chatModelAtStart({...base, selected: 'openabstractions/gemma'}), '', 'a person\'s pick stays');
  assert.equal(chatModelAtStart({...base, selected: 'anthropic/claude'}), '', 'another provider\'s model stays');
  assert.equal(chatModelAtStart({...base, selected: {providerID: 'x'}}), '', 'a model the plugin cannot read stays');
});

test('chatModelAtStart: with no choice OA\'s default fills an empty or vanished choice', () => {
  const discovered = {qwen: {}, gemma: {}};
  const base = {providerID: 'openabstractions', choice: '', discovered};
  assert.equal(defaultChatFamily(discovered), 'gemma', 'the first family by name, as configure.js writes it');
  assert.equal(chatModelAtStart({...base, selected: ''}), 'openabstractions/gemma');
  assert.equal(chatModelAtStart({...base, selected: 'openabstractions/oa-chat'}), 'openabstractions/gemma');
  assert.equal(chatModelAtStart({...base, selected: 'openabstractions/qwen'}), '', 'a person\'s pick stays');
  assert.equal(chatModelAtStart({...base, selected: '', discovered: {}}), '', 'no servable family, no default');
});

test('chatChoiceModel: the first offered candidate, as the picker lists it', () => {
  const offers = {local: [offer('qwen'), offer('gemma')], hosted: [{...offer('deepseek-chat'), servers: ['deepseek']}]};
  const discovered = {qwen: {}, gemma: {}};
  const policy = (...candidates) => ({outcome: 'found', policy: {name: 'oa-chat-default', candidates}});
  assert.equal(chatChoiceModel('openabstractions', policy({model: 'gone', placement: 'local'}, {model: 'gemma', placement: 'local'}),
    offers, discovered), 'openabstractions/gemma');
  assert.equal(chatChoiceModel('openabstractions', policy({model: 'deepseek-chat', placement: 'hosted'}), offers, discovered),
    'openabstractions-hosted/deepseek-chat');
  assert.equal(chatChoiceModel('openabstractions', {outcome: 'unknown'}, offers, discovered), '');
});

test('at start OpenCode preselects the real model chosen for it in OpenAbstractions', async () => {
  const {config, logs, asked} = await start(undefined, found);
  assert.deepEqual(asked, ['oa-chat-default'], JSON.stringify(logs));
  assert.equal(config.model, 'openabstractions/qwen');
  assert.equal(config.provider.openabstractions.options.oaSelectionPolicies['oa-chat'], 'oa-chat-default',
    'the policy route stays for requests that name it');
  const route = config.provider.openabstractions.models['oa-chat'];
  assert.equal(route.name, 'Default (qwen)', 'the policy route is the Default row of the picker');
  assert.equal(route.id, 'Default (qwen)', 'the OpenCode prompt names the Default row');
  assert.equal(route.release_date, undefined);
  assert.deepEqual(config.provider.openabstractions.options.oaRouteModels, {'Default (qwen)': 'oa-chat'});
  assert.deepEqual(Object.keys(config.provider.openabstractions.models).sort(), ['gemma', 'oa-chat', 'qwen']);
  assert.ok(logs.some((entry) => entry.level === 'info' && entry.message === 'OpenCode uses the chat model chosen for it in OpenAbstractions'));
  assert.equal(logs.some((entry) => entry.message.includes('no longer in the current inventory')), false);
});

test('at start a model the person picked stays when a chat policy is stored', async () => {
  assert.equal((await start('anthropic/claude', found)).config.model, 'anthropic/claude');
  assert.equal((await start('openabstractions/gemma', found)).config.model, 'openabstractions/gemma');
  assert.equal((await start('openabstractions/oa-chat', found)).config.model, 'openabstractions/qwen',
    'the policy route is no pick');
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
