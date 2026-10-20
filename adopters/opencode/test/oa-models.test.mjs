import assert from 'node:assert/strict';
import test from 'node:test';
import {chosenText, installModels, modelOptions, oaProvider} from '../oa-models.js';
import {servesProviderRefresh} from '../opencode-build.js';
import {tuiFeatures} from '../tui.js';

const providers = [
  {id: 'anthropic', name: 'Anthropic', models: {'claude-x': {name: 'Claude X'}}},
  {id: 'openabstractions-hosted', name: 'OpenAbstractions · via hosted servers',
    models: {'deepseek-chat': {name: 'deepseek-chat · 64k · hosted: deepseek'}}},
  {id: 'openabstractions', name: 'OpenAbstractions · this computer',
    models: {'qwen3.6-35b-a3b': {name: 'qwen3.6-35b-a3b · 32k · this computer: lemonade'}}},
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

test('/oa lists only OA models, this computer first, with the default marked', () => {
  assert.equal(oaProvider('openabstractions'), true);
  assert.equal(oaProvider('openabstractions-preferences'), true);
  assert.equal(oaProvider('openai'), false);
  assert.equal(oaProvider('oa-custom-hosted', 'oa-custom'), true);
  const entries = modelOptions(providers, 'openabstractions/qwen3.6-35b-a3b');
  assert.equal(entries[0].title, 'Refresh the list from OpenAbstractions');
  assert.deepEqual(entries.slice(1).map((entry) => [entry.category, entry.value, entry.description]), [
    ['OpenAbstractions · this computer', 'openabstractions/qwen3.6-35b-a3b', 'default'],
    ['OpenAbstractions · via hosted servers', 'openabstractions-hosted/deepseek-chat', undefined],
  ]);
  assert.match(entries[2].title, /hosted: deepseek/, 'names say where a model runs');
});

test('choosing a model makes it OpenCode\'s default and says what that changes', async () => {
  const api = fakeApi();
  const saved = [];
  await installModels(api, {}, {readProviders: async () => ({providers, defaultModel: 'openabstractions/qwen3.6-35b-a3b'}),
    setDefault: async (model) => saved.push(model)});
  assert.deepEqual(api.commands.map((command) => [command.name, command.slashName]), [['openabstractions.models', 'oa']]);
  await api.commands[0].run();
  const dialog = api.dialogs.at(-1);
  assert.equal(dialog.current, 'openabstractions/qwen3.6-35b-a3b');
  const hosted = dialog.options.find((entry) => entry.value === 'openabstractions-hosted/deepseek-chat');
  await dialog.onSelect(hosted);
  assert.deepEqual(saved, ['openabstractions-hosted/deepseek-chat']);
  assert.equal(api.cleared, 1);
  assert.equal(api.toasts.at(-1).variant, 'success');
  assert.equal(api.toasts.at(-1).message, chosenText(hosted.title, true));
  assert.match(api.toasts.at(-1).message, /keeps it until you pick again/);
  assert.match(api.toasts.at(-1).message, /Local assistant stays on this computer and refuses it/);
  await api.commands[0].run();
  await api.dialogs.at(-1).onSelect(api.dialogs.at(-1).options.find((entry) => entry.value === 'openabstractions/qwen3.6-35b-a3b'));
  assert.doesNotMatch(api.toasts.at(-1).message, /refuses/, 'a model on this computer needs no warning');
});

// OpenCode refreshes only the provider the OA plugin registers its model
// hook for; the headings beside it are read when OpenCode starts.
test('refresh asks OA again through the plugin\'s provider and reopens the list', async () => {
  const api = fakeApi();
  const refreshed = [];
  let reads = 0;
  await installModels(api, {}, {readProviders: async () => { reads += 1; return {providers, defaultModel: ''}; },
    servesRefresh: async () => true, refreshProvider: async (id) => refreshed.push(id), setDefault: async () => assert.fail('refresh sets nothing')});
  await api.commands[0].run();
  await api.dialogs.at(-1).onSelect(api.dialogs.at(-1).options[0]);
  assert.deepEqual(refreshed, ['openabstractions']);
  assert.equal(reads, 2, 'the list is read again after the refresh');
  assert.equal(api.dialogs.length, 2);
  assert.equal(api.toasts.at(-1).variant, 'success');
});

test('a failed refresh, read or save says so and changes nothing', async () => {
  const api = fakeApi();
  await installModels(api, {}, {readProviders: async () => ({providers, defaultModel: ''}), servesRefresh: async () => true,
    refreshProvider: async () => { throw new Error('this OpenCode cannot refresh a provider\'s models'); },
    setDefault: async () => { throw new Error('config is read-only'); }});
  await api.commands[0].run();
  await api.dialogs.at(-1).onSelect(api.dialogs.at(-1).options[0]);
  assert.match(api.toasts.find((toast) => toast.variant === 'error').message, /could not refresh the models: this OpenCode cannot refresh/);
  await api.dialogs.at(-1).onSelect(api.dialogs.at(-1).options[1]);
  assert.match(api.toasts.at(-1).message, /did not save the default model: config is read-only/);

  const empty = fakeApi();
  await installModels(empty, {}, {readProviders: async () => ({providers: [providers[0]], defaultModel: ''})});
  await empty.commands[0].run();
  assert.match(empty.toasts[0].message, /no OpenAbstractions model/);
  const down = fakeApi();
  await installModels(down, {}, {readProviders: async () => { throw new Error('server stopped'); }});
  await down.commands[0].run();
  assert.equal(down.dialogs.length, 0);
  assert.match(down.toasts[0].message, /could not read OpenCode's models: server stopped/);
});

// The refresh route belongs to OA's provider refresh patch. The official
// OpenCode's OpenAPI document lacks it, so /oa lists models only.
test('on the official OpenCode /oa lists no refresh entry and says nothing about it', async () => {
  const stock = {paths: {'/provider': {}, '/provider/auth': {}, '/config/providers': {}}};
  const patched = {paths: {...stock.paths, '/provider/{providerID}/models/refresh': {post: {}}}};
  const asked = [];
  const http = (doc) => ({client: {get: async (options) => { asked.push(options.url); return {data: doc, response: {ok: true}}; }}});
  assert.equal(await servesProviderRefresh(http(stock)), false);
  assert.equal(await servesProviderRefresh(http(patched)), true);
  assert.deepEqual(asked, ['/doc', '/doc']);
  assert.equal(await servesProviderRefresh({client: {get: async () => { throw new Error('no server'); }}}), false);
  assert.equal(await servesProviderRefresh({client: {get: async () => ({error: {}, response: {ok: false, status: 404}})}}), false);
  assert.equal(await servesProviderRefresh(undefined), false);

  const api = {...fakeApi(), client: http(stock)};
  await installModels(api, {}, {readProviders: async () => ({providers, defaultModel: ''}),
    refreshProvider: async () => assert.fail('the official OpenCode is never asked to refresh')});
  await api.commands[0].run();
  await api.commands[0].run();
  const entries = api.dialogs.at(-1).options;
  assert.deepEqual(entries.map((entry) => entry.value), ['openabstractions/qwen3.6-35b-a3b', 'openabstractions-hosted/deepseek-chat']);
  assert.equal(api.toasts.length, 0);
  assert.equal(asked.filter((url) => url === '/doc').length, 3, 'the route is looked up once per TUI start');
  assert.deepEqual(modelOptions(providers, '', 'openabstractions', false).map((entry) => entry.value),
    entries.map((entry) => entry.value));

  const empty = {...fakeApi(), client: http(stock)};
  await installModels(empty, {}, {readProviders: async () => ({providers: [providers[0]], defaultModel: ''})});
  await empty.commands[0].run();
  assert.match(empty.toasts[0].message, /no OpenAbstractions model/);
});

test('the TUI module installs /oa beside its other features', () => {
  assert.equal(typeof tuiFeatures.models, 'function');
});
