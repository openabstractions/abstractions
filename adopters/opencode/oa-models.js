/**
 * `/oa` in OpenCode's TUI: OpenAbstractions' models in one list, under the
 * headings configure writes (this computer, via hosted servers, your saved
 * preferences), each named with where it runs. Choosing one makes it
 * OpenCode's default `model` through OpenCode's own config API. On OA's
 * patched build, whose OpenAPI document lists the provider refresh route, the
 * first entry asks OA again for the models on this computer: OpenCode refreshes
 * only the provider the OA plugin registers its model hook for, so the
 * hosted heading's list is read when OpenCode starts. A plugin cannot
 * select the model of a running session (OpenCode's TUI plugin API has no
 * model setter), so an agent with a model picked in this session keeps it
 * until the person picks again.
 */

import {once, servesProviderRefresh} from './opencode-build.js';

const refreshValue = Symbol('refresh');

/** Whether id is one of OA's providers: the configured ID or a heading beside it. */
export function oaProvider(id, base = 'openabstractions') {
  return id === base || (typeof id === 'string' && id.startsWith(`${base}-`));
}

/** The dialog's entries: refresh first when this OpenCode serves the
 * provider refresh route, then every OA model under its provider's heading,
 * the providers in the picker's own order (by name). */
export function modelOptions(providers, defaultModel, base = 'openabstractions', refreshable = true) {
  const options = refreshable ? [{title: 'Refresh the list from OpenAbstractions', value: refreshValue,
    description: 'Asks OpenAbstractions for the models it serves now'}] : [];
  const own = (providers ?? []).filter((provider) => oaProvider(provider.id, base))
    .sort((a, b) => (a.name ?? a.id) < (b.name ?? b.id) ? -1 : (a.name ?? a.id) > (b.name ?? b.id) ? 1 : 0);
  for (const provider of own) {
    for (const [id, model] of Object.entries(provider.models ?? {})) {
      const key = `${provider.id}/${id}`;
      options.push({title: model.name ?? id, value: key, category: provider.name ?? provider.id,
        description: key === defaultModel ? 'default' : undefined});
    }
  }
  return options;
}

/** The sentence a choice writes; a hosted model also says the Local
 * assistant, which stays on this computer, will refuse it. */
export function chosenText(title, hosted = false) {
  const text = `${title} is now OpenCode's default model. An agent with a model picked in this session keeps it until you pick again (Ctrl+X M).`;
  return hosted ? `${text} The Local assistant stays on this computer and refuses it; press Tab for the Build agent.` : text;
}

/** OpenCode's providers and default model, read fresh from the server. */
export async function readProviders(api) {
  const [providers, config] = await Promise.all([
    api.client.config.providers({}, {throwOnError: true}),
    api.client.global.config.get({throwOnError: true}),
  ]);
  return {providers: providers.data?.providers ?? [], defaultModel: config.data?.model};
}

/** Ask OpenCode to refresh one provider's models (the provider refresh
 * patch's route); a stock OpenCode answers 404. */
export async function refreshProvider(api, providerID) {
  const response = await api.client.client.post({url: `/provider/${encodeURIComponent(providerID)}/models/refresh`});
  if (response.error || (response.response && !response.response.ok)) {
    const status = response.response?.status;
    const said = response.error?.data?.message;
    if (status === 404 && !said) throw new Error('this OpenCode cannot refresh a provider\'s models; restart OpenCode to read the list again');
    throw new Error(typeof said === 'string' && said ? said : `refresh failed (${status ?? 'no answer'})`);
  }
}

/**
 * Register `/oa` (command `openabstractions.models`). `dependencies` replaces
 * the server reads and writes in tests.
 */
export async function installModels(api, options = {}, dependencies = {}) {
  const base = typeof options.providerId === 'string' && options.providerId ? options.providerId : 'openabstractions';
  const read = dependencies.readProviders ?? (() => readProviders(api));
  const refresh = dependencies.refreshProvider ?? ((id) => refreshProvider(api, id));
  // The refresh route belongs to OA's provider refresh patch; the official
  // OpenCode lists no refresh entry and reads the models at its start.
  const refreshable = once(dependencies.servesRefresh ?? (() => servesProviderRefresh(api.client)));
  const setDefault = dependencies.setDefault ??
    ((model) => api.client.global.config.update({config: {model}}, {throwOnError: true}));
  const toast = (variant, message) => { try { api.ui.toast({variant, message}); } catch { /* a toast must not break /oa */ } };

  async function open() {
    let listing;
    try {
      listing = await read();
    } catch (error) {
      toast('error', `OpenAbstractions could not read OpenCode's models: ${error?.message ?? error}`);
      return;
    }
    const canRefresh = await refreshable();
    const entries = modelOptions(listing.providers, listing.defaultModel, base, canRefresh);
    if (entries.length === (canRefresh ? 1 : 0)) toast('warning', 'OpenCode lists no OpenAbstractions model. Run Setup\'s "Choose a chat model" first.');
    api.ui.dialog.replace(() => api.ui.DialogSelect({
      title: 'OpenAbstractions models',
      options: entries,
      current: listing.defaultModel,
      onSelect: (option) => choose(option, listing),
    }));
  }

  async function choose(option, listing) {
    if (option.value === refreshValue) {
      try {
        await refresh(base);
        toast('success', 'OpenAbstractions models refreshed');
      } catch (error) {
        toast('error', `OpenAbstractions could not refresh the models: ${error?.message ?? error}`);
      }
      await open();
      return;
    }
    api.ui.dialog.clear();
    try {
      await setDefault(option.value);
      toast('success', chosenText(option.title, String(option.value).startsWith(`${base}-hosted/`)));
    } catch (error) {
      toast('error', `OpenCode did not save the default model: ${error?.message ?? error}`);
    }
  }

  const removeLayer = api.keymap?.registerLayer?.({
    commands: [{
      name: 'openabstractions.models', title: 'OpenAbstractions models', category: 'OpenAbstractions', namespace: 'palette',
      slashName: 'oa', run: open,
    }],
  });
  api.lifecycle?.onDispose?.(() => removeLayer?.());
  return {open, choose};
}
