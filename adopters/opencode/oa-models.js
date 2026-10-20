/**
 * `/oa` in OpenCode's TUI, a terminal convenience beside the Panel's per-app
 * chat choice (Panel → Applications → OpenCode), which is the path for every
 * client: OpenAbstractions' models in one list, under the headings the plugin
 * shows ("OpenAbstractions", "DeepSeek · OpenAbstractions"). Choosing one
 * saves that same per-app chat choice (the `oa-chat-default` policy). It
 * never writes OpenCode's `model`: the plugin preselects OA's choice at
 * OpenCode's start unless OpenCode's own setting names a model, which is the
 * person's pick and stays.
 */
import {defaultSelectionModel, defaultSelectionPolicies, readOffers} from './inventory.js';
import {runtimeConnector} from './runtime-connector.js';

const selectionContract = 'abstraction.inference/selection-operator@2';
const routerContract = 'abstraction.router/router@1';

async function machine(endpoint) {
  const {Machine} = await import('@openabstractions/facade');
  return new Machine(endpoint, {connector: await runtimeConnector(), timeout: 5000});
}

/** OA's selection operator for this OpenCode program. */
export async function resolveOperator({endpoint = null} = {}) {
  const {SelectionOperatorV2Client} = await import('@openabstractions/inference');
  return new SelectionOperatorV2Client(await (await machine(endpoint)).resolveService(selectionContract, {scope: 'local'}));
}

/** OA's router, which lists the hosted offers and the hosts behind them. */
export async function resolveRouter({endpoint = null} = {}) {
  const {RouterClient} = await import('@openabstractions/router');
  return new RouterClient(await (await machine(endpoint)).resolveService(routerContract, {scope: 'local'}));
}

/** The credential name a hosted model's offer runs under: the credential the
 * hosted host behind its first server declares, or ''. */
export async function hostedCredential(router, model) {
  const offered = (await readOffers(router, 'hosted', {fresh: false})).offers ?? [];
  const offer = offered.find((entry) => entry.family === model || (entry.names ?? []).includes(model));
  if (!offer) return '';
  const hosts = (await router.hosts(false))?.hosts ?? [];
  for (const server of offer.servers ?? []) {
    const host = hosts.find((entry) => entry?.host === server && typeof entry.credential === 'string' && entry.credential);
    if (host) return host.credential;
  }
  return '';
}

/** Whether id is one of OA's providers: the configured ID or a heading beside it. */
export function oaProvider(id, base = 'openabstractions') {
  return id === base || (typeof id === 'string' && id.startsWith(`${base}-`));
}

/** The dialog's entries: every OA model under its provider's heading, the
 * models on this computer first and the other headings by name. */
export function modelOptions(providers, base = 'openabstractions') {
  const options = [];
  const rank = (provider) => [provider.id === base ? 0 : 1, provider.name ?? provider.id];
  const own = (providers ?? []).filter((provider) => oaProvider(provider.id, base)).sort((a, b) => {
    const [left, right] = [rank(a), rank(b)];
    return left[0] - right[0] || (left[1] < right[1] ? -1 : left[1] > right[1] ? 1 : 0);
  });
  for (const provider of own) {
    for (const [id, model] of Object.entries(provider.models ?? {})) {
      options.push({title: model.name ?? id, value: `${provider.id}/${id}`, category: provider.name ?? provider.id});
    }
  }
  return options;
}

/** The model OpenCode's own setting names, when it is the person's pick:
 * any model but none and OA's policy route. */
export function personPick(model, base = 'openabstractions') {
  return typeof model === 'string' && model !== '' && model !== `${base}/${defaultSelectionModel('chat')}` ? model : '';
}

/** The sentence a saved choice shows. With a person's pick in OpenCode's own
 * setting, it says how to use the choice; a hosted model also says the Local
 * assistant, which stays on this computer, refuses it. */
export function chosenText(title, {hosted = false, pick = ''} = {}) {
  const text = pick
    ? `OpenCode's own setting picks ${pick}. Choose ${title} in the model menu to use it.`
    : `${title} is now OpenAbstractions' choice for OpenCode. OpenCode selects it from its next start.`;
  return hosted ? `${text} The Local assistant stays on this computer and refuses it; press Tab for the Build agent.` : text;
}

/** OpenCode's providers, the model this instance selected and the model
 * OpenCode's own setting names, read fresh from the server. */
export async function readProviders(api) {
  const [providers, instance, global] = await Promise.all([
    api.client.config.providers({}, {throwOnError: true}),
    api.client.config.get({}, {throwOnError: true}),
    api.client.global.config.get({throwOnError: true}),
  ]);
  return {providers: providers.data?.providers ?? [], selected: instance.data?.model, ownSetting: global.data?.model};
}

/** Saves OA's per-app chat choice for this OpenCode program: the one
 * candidate, over the revision it read. */
export async function writeChatChoice(candidate, operator) {
  const name = defaultSelectionPolicies.chat;
  const read = await operator.read('', name);
  if (read?.outcome !== 'found' && read?.outcome !== 'unknown') {
    throw new Error(`the chat choice could not be read (${read?.outcome ?? 'no answer'})`);
  }
  const change = await operator.put('', read.outcome === 'found' ? read.revision : '', {name, candidates: [candidate]});
  if (change?.outcome !== 'applied') throw new Error(`the chat choice was not saved (${change?.outcome ?? 'no answer'})`);
}

/**
 * Register `/oa` (command `openabstractions.models`). `dependencies` replaces
 * the server reads and OA's write in tests.
 */
export async function installModels(api, options = {}, dependencies = {}) {
  const base = typeof options.providerId === 'string' && options.providerId ? options.providerId : 'openabstractions';
  const read = dependencies.readProviders ?? (() => readProviders(api));
  // A hosted candidate runs under its host's declared credential; OA's
  // selection service refuses a hosted candidate without one.
  const save = dependencies.writeChatChoice ?? (async (candidate) => {
    let credential = '';
    if (candidate.placement === 'hosted') {
      credential = await hostedCredential(await (dependencies.resolveRouter ?? resolveRouter)(), candidate.model);
      if (!credential) throw new Error(`${candidate.model} names no hosted credential in OpenAbstractions`);
    }
    await writeChatChoice({...candidate, credential}, await (dependencies.resolveOperator ?? resolveOperator)());
  });
  const toast = (variant, message) => { try { api.ui.toast({variant, message}); } catch { /* a toast must not break /oa */ } };

  async function open() {
    let listing;
    try {
      listing = await read();
    } catch (error) {
      toast('error', `OpenAbstractions could not read OpenCode's models: ${error?.message ?? error}`);
      return;
    }
    const entries = modelOptions(listing.providers, base);
    if (entries.length === 0) toast('warning', 'OpenCode lists no OpenAbstractions model. Run Setup\'s "Choose a chat model" first.');
    api.ui.dialog.replace(() => api.ui.DialogSelect({
      title: 'OpenAbstractions models',
      options: entries,
      current: listing.selected,
      onSelect: (option) => choose(option, listing),
    }));
  }

  async function choose(option, listing) {
    api.ui.dialog.clear();
    const value = String(option.value);
    const hosted = value.startsWith(`${base}-hosted/`);
    const model = value.slice(value.indexOf('/') + 1);
    try {
      await save({model, placement: hosted ? 'hosted' : 'local', credential: ''});
    } catch (error) {
      toast('error', `OpenAbstractions did not save OpenCode's chat choice: ${error?.message ?? error}`);
      return;
    }
    const pick = personPick(listing.ownSetting, base);
    const picked = pick && pick !== value ? titleOf(listing.providers, pick) : '';
    toast(picked ? 'info' : 'success', chosenText(option.title, {hosted, pick: picked}));
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

/** The name the picker shows for a provider/model key, or the key. */
function titleOf(providers, key) {
  const slash = key.indexOf('/');
  const provider = (providers ?? []).find((entry) => entry.id === key.slice(0, slash));
  return provider?.models?.[key.slice(slash + 1)]?.name ?? key;
}
