import assert from 'node:assert/strict';
import test from 'node:test';
import {chosenText, installModels, modelOptions, oaProvider, personPick} from '../oa-models.js';
import {tuiFeatures} from '../tui.js';

const providers = [
  {id: 'anthropic', name: 'Anthropic', models: {'claude-x': {name: 'Claude X'}}},
  {id: 'openabstractions-hosted', name: 'DeepSeek · OpenAbstractions',
    models: {'deepseek-chat': {name: 'DeepSeek Chat'}}},
  {id: 'openabstractions', name: 'OpenAbstractions',
    models: {'qwen3.6-35b-a3b': {name: 'Qwen3.6 35B'}, 'qwen3-0.6b': {name: 'Qwen3 0.6B'}}},
];

// A fake TUI: the registered command, the dialog it opens, toasts.
function fakeApi() {
  const api = {commands: [], toasts: [], dialogs: [], cleared: 0,
    keymap: {registerLayer(layer) { api.commands.push(...layer.commands); return () => {}; }},
    lifecycle: {onDispose() {}},
    ui: {toast: (toast) => api.toasts.push(toast), DialogSelect: (props) => props,
      dialog: {replace: (render) => api.dialogs.push(render()), clear: () => { api.cleared += 1; }}}};
  return api;
}

test('/oa lists only OA models, this computer first, under their headings', () => {
  assert.equal(oaProvider('openabstractions'), true);
  assert.equal(oaProvider('openabstractions-preferences'), true);
  assert.equal(oaProvider('openai'), false);
  assert.equal(oaProvider('oa-custom-hosted', 'oa-custom'), true);
  const entries = modelOptions(providers);
  assert.deepEqual(entries.map((entry) => [entry.category, entry.value, entry.title]), [
    ['OpenAbstractions', 'openabstractions/qwen3.6-35b-a3b', 'Qwen3.6 35B'],
    ['OpenAbstractions', 'openabstractions/qwen3-0.6b', 'Qwen3 0.6B'],
    ['DeepSeek · OpenAbstractions', 'openabstractions-hosted/deepseek-chat', 'DeepSeek Chat'],
  ]);
  assert.equal(personPick(undefined), '');
  assert.equal(personPick('openabstractions/oa-chat'), '', 'the policy route is no pick');
  assert.equal(personPick('anthropic/claude-x'), 'anthropic/claude-x');
});

async function chooseWith(ownSetting, value) {
  const api = fakeApi();
  const saved = [];
  const written = [];
  api.client = {global: {config: {update: async (body) => { written.push(body); }}}};
  await installModels(api, {}, {
    readProviders: async () => ({providers, selected: 'openabstractions/qwen3.6-35b-a3b', ownSetting}),
    writeChatChoice: async (candidate) => saved.push(candidate),
  });
  assert.deepEqual(api.commands.map((command) => [command.name, command.slashName]), [['openabstractions.models', 'oa']]);
  await api.commands[0].run();
  const dialog = api.dialogs.at(-1);
  assert.equal(dialog.current, 'openabstractions/qwen3.6-35b-a3b', 'the tick is the model OpenCode selected');
  await dialog.onSelect(dialog.options.find((entry) => entry.value === value));
  return {api, saved, written};
}

test('/oa with no person pick saves OA\'s choice and never writes OpenCode\'s model', async () => {
  const {api, saved, written} = await chooseWith(undefined, 'openabstractions/qwen3-0.6b');
  assert.deepEqual(saved, [{model: 'qwen3-0.6b', placement: 'local', credential: ''}]);
  assert.deepEqual(written, [], 'config.model is left unwritten');
  assert.equal(api.cleared, 1);
  assert.equal(api.toasts.at(-1).variant, 'success');
  assert.equal(api.toasts.at(-1).message, chosenText('Qwen3 0.6B'));
  assert.match(api.toasts.at(-1).message, /OpenAbstractions' choice for OpenCode/);
});

/** OA's selection service as /oa meets it, with the service's candidate rule
 * (abstraction-inference/go/selection.go validSelectionCandidate): a local
 * candidate names no credential and a hosted one names its credential. */
function selectionService() {
  const service = {stored: null, revision: '', puts: []};
  service.operator = {
    read: async (_program, name) => service.stored
      ? {outcome: 'found', revision: service.revision, policy: service.stored} : {outcome: 'unknown', revision: ''},
    put: async (_program, expected, policy) => {
      service.puts.push(policy);
      if (expected !== service.revision) return {outcome: 'conflict', revision: service.revision};
      const valid = policy.candidates.every((candidate) => candidate.model && ['local', 'hosted'].includes(candidate.placement)
        && (candidate.placement === 'local' ? candidate.credential === '' : candidate.credential !== ''));
      if (!valid) return {outcome: 'invalid', revision: service.revision};
      service.stored = policy;
      service.revision = `r${service.puts.length}`;
      return {outcome: 'applied', revision: service.revision};
    },
  };
  return service;
}

// The router's hosted offer for DeepSeek Chat and the host behind it, which
// declares the credential the person saved in OA.
const router = {
  offer: async ({placement}) => ({outcome: 'offered', offers: placement === 'hosted'
    ? [{family: 'deepseek-chat', names: ['deepseek-chat'], servers: ['deepseek'], context: 65536n, contextSource: 'maximum', input: 61440n}] : []}),
  hosts: async () => ({hosts: [{host: 'ollama', credential: ''}, {host: 'deepseek', hosted: true, credential: 'deepseek-api'}]}),
};

async function chooseThroughService(service, value, routerClient = router) {
  const api = fakeApi();
  await installModels(api, {}, {
    readProviders: async () => ({providers, selected: 'openabstractions/qwen3.6-35b-a3b', ownSetting: undefined}),
    resolveOperator: async () => service.operator,
    resolveRouter: async () => routerClient,
  });
  await api.commands[0].run();
  const dialog = api.dialogs.at(-1);
  await dialog.onSelect(dialog.options.find((entry) => entry.value === value));
  return api;
}

test('/oa saves a DeepSeek choice as hosted under the credential its host declares', async () => {
  const service = selectionService();
  const api = await chooseThroughService(service, 'openabstractions-hosted/deepseek-chat');
  assert.deepEqual(service.stored, {name: 'oa-chat-default',
    candidates: [{model: 'deepseek-chat', placement: 'hosted', credential: 'deepseek-api'}]});
  assert.equal(api.toasts.at(-1).variant, 'success');
  assert.match(api.toasts.at(-1).message, /Local assistant stays on this computer and refuses it/);

  const local = await chooseThroughService(service, 'openabstractions/qwen3-0.6b');
  assert.deepEqual(service.stored.candidates, [{model: 'qwen3-0.6b', placement: 'local', credential: ''}],
    'the second save replaces the first over its revision');
  assert.equal(local.toasts.at(-1).variant, 'success');

  const unnamed = selectionService();
  const refused = await chooseThroughService(unnamed, 'openabstractions-hosted/deepseek-chat',
    {...router, hosts: async () => ({hosts: [{host: 'deepseek', hosted: true}]})});
  assert.equal(unnamed.puts.length, 0, 'a hosted choice without a credential is never sent');
  assert.match(refused.toasts.at(-1).message, /deepseek-chat names no hosted credential in OpenAbstractions/);
});

test('/oa with a person pick saves OA\'s choice, leaves the pick and says how to use the choice', async () => {
  const {api, saved, written} = await chooseWith('anthropic/claude-x', 'openabstractions/qwen3-0.6b');
  assert.deepEqual(saved, [{model: 'qwen3-0.6b', placement: 'local', credential: ''}]);
  assert.deepEqual(written, [], 'the person\'s pick is unchanged');
  assert.equal(api.toasts.at(-1).message,
    'OpenCode\'s own setting picks Claude X. Choose Qwen3 0.6B in the model menu to use it.');
  const same = await chooseWith('openabstractions/qwen3-0.6b', 'openabstractions/qwen3-0.6b');
  assert.equal(same.api.toasts.at(-1).message, chosenText('Qwen3 0.6B'), 'a pick of the same model needs no hint');
});

test('a failed read or save says so and changes nothing', async () => {
  const api = fakeApi();
  await installModels(api, {}, {readProviders: async () => ({providers, ownSetting: ''}),
    writeChatChoice: async () => { throw new Error('the chat choice was not saved (forbidden)'); }});
  await api.commands[0].run();
  await api.dialogs.at(-1).onSelect(api.dialogs.at(-1).options[0]);
  assert.match(api.toasts.at(-1).message, /did not save OpenCode's chat choice: the chat choice was not saved \(forbidden\)/);

  const empty = fakeApi();
  await installModels(empty, {}, {readProviders: async () => ({providers: [providers[0]], ownSetting: ''})});
  await empty.commands[0].run();
  assert.match(empty.toasts[0].message, /no OpenAbstractions model/);
  const down = fakeApi();
  await installModels(down, {}, {readProviders: async () => { throw new Error('server stopped'); }});
  await down.commands[0].run();
  assert.equal(down.dialogs.length, 0);
  assert.match(down.toasts[0].message, /could not read OpenCode's models: server stopped/);
});

test('the TUI module installs /oa beside its other features', () => {
  assert.equal(typeof tuiFeatures.models, 'function');
});
