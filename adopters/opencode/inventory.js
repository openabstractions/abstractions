import {readFile} from 'node:fs/promises';
import {dirname, isAbsolute, join} from 'node:path';
import {fileURLToPath} from 'node:url';

const providerPackage = '@openabstractions/opencode';
const packageSpecifier = /^@openabstractions\/opencode(?:@[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)?$/;

// Shared package recognition for V1 and V2; plugin.js exports plugin functions.
export async function nativeProviderPackage(provider) {
  if (typeof provider?.npm === 'string' && packageSpecifier.test(provider.npm)) return true;
  if (typeof provider?.npm !== 'string') return false;
  let entry;
  try {
    if (provider.npm.startsWith('file://')) entry = fileURLToPath(provider.npm);
    else if (isAbsolute(provider.npm)) entry = provider.npm;
    else return false;
    const pkg = JSON.parse(await readFile(join(dirname(entry), 'package.json'), 'utf8'));
    return pkg.name === providerPackage;
  } catch {
    return false;
  }
}

const outputBudget = 4096;
// The router's offer sources whose context is a number OpenCode can plan on.
const knownSources = new Set(['effective', 'maximum', 'default']);
const inventoryMarker = 'oaInventoryManagedModels';
const selectionMarker = 'oaSelectionPolicyManagedModels';

/** The picker name of the app's own chat route, which follows the default
 * model chosen in the Panel: "Default (Qwen3.6 35B)". OpenCode also names
 * the model by this text in its system prompt, through the entry's API id
 * (routeModelsOption maps it back to the route). */
export function defaultEntryName(model) {
  return typeof model === 'string' && model ? `Default (${model})` : 'Default';
}

/** The provider option that maps a route entry's API id ("Default (Qwen3.6
 * 35B)") to the model ID whose selection policy serves it ("oa-chat");
 * index.js reads it. */
export const routeModelsOption = 'oaRouteModels';

export const modelHeadings = Object.freeze({
  local: 'OpenAbstractions',
  hosted: 'Hosted · OpenAbstractions',
  preferences: 'OpenAbstractions · your saved preferences',
});

/** The heading names earlier versions wrote, which the plugin shows under
 * today's names. */
export const legacyHeadings = Object.freeze({
  'OpenAbstractions · this computer': 'local',
  'OpenAbstractions · via hosted servers': 'hosted',
});

// How a hosted service's server name reads in a heading.
const serviceNames = Object.freeze({
  anthropic: 'Anthropic', deepseek: 'DeepSeek', gemini: 'Gemini', google: 'Google', groq: 'Groq', mistral: 'Mistral',
  openai: 'OpenAI', openrouter: 'OpenRouter', together: 'Together', xai: 'xAI',
});

function serviceName(server) {
  const known = serviceNames[server.toLowerCase()];
  return known ?? server.charAt(0).toUpperCase() + server.slice(1);
}

/** The hosted heading's name from the services its offers name
 * ("DeepSeek · OpenAbstractions"), which keeps it apart from OpenCode's own
 * group for the same service. */
export function hostedHeading(offers) {
  const services = [];
  for (const offer of offers ?? []) {
    for (const server of offer?.servers ?? []) {
      if (typeof server !== 'string' || !server) continue;
      const name = serviceName(server);
      if (!services.includes(name)) services.push(name);
    }
  }
  return services.length ? `${services.join(', ')} · OpenAbstractions` : modelHeadings.hosted;
}

/** Whether name is a heading name this plugin wrote: today's local or a
 * hosted "… · OpenAbstractions" name, or an earlier version's. */
export function ownHeadingName(name) {
  return typeof name === 'string' && (Object.hasOwn(legacyHeadings, name) || name === modelHeadings.local
    || name.endsWith(' · OpenAbstractions'));
}

/** Add a runtime-only hosted heading from the router's current offers.
 * Existing provider entries remain owned by their configuration. */
export function addHostedProvider(config, providerID, offers) {
  const base = config.provider?.[providerID];
  const id = `${providerID}-hosted`;
  if (!base || Object.hasOwn(config.provider, id)) return null;
  const models = servableChatModels(offers, [], null, () => false);
  if (!Object.keys(models).length) return null;
  const options = {...(base.options?.runtimeEndpoint === undefined ? {} : {runtimeEndpoint:base.options.runtimeEndpoint}),
    ...(base.options?.server === undefined ? {} : {server:base.options.server}),
    scope:'remote', guarantees:['abstraction.inference/hosted-allowed@1'], oaPlacement:'hosted', oaInventory:true,
    [inventoryMarker]: {...models}};
  const provider = {name:hostedHeading(offers), npm:base.npm, options, models};
  setOwn(config.provider, id, provider);
  return {id, provider};
}

function setOwn(target, key, value) {
  Object.defineProperty(target, key, {value, enumerable: true, configurable: true, writable: true});
}

function sameJSON(left, right) {
  if (left === right) return true;
  if (!left || !right || typeof left !== 'object' || typeof right !== 'object') return false;
  if (Array.isArray(left) !== Array.isArray(right)) return false;
  const leftKeys = Object.keys(left).sort();
  const rightKeys = Object.keys(right).sort();
  return leftKeys.length === rightKeys.length && leftKeys.every((key, index) =>
    key === rightKeys[index] && sameJSON(left[key], right[key]));
}

function sameModel(left, right) {
  const withoutProviderIdentity = (value) => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return value;
    const copy = {...value};
    delete copy.id;
    delete copy.providerID;
    return copy;
  };
  return sameJSON(withoutProviderIdentity(left), withoutProviderIdentity(right));
}

/** Text that carries a content digest or commit: 16 or more hex digits in a
 * row, or a sha256 prefix, anywhere. It names no model to a person. */
const digestName = /[0-9a-f]{16,}|(?:^|[\\/:])sha256[-:]/i;

/** The plain name of an offer for a person, which never contains a digest
 * (owner rule: an id or hash is never visible text): its family, else the
 * first of its names, when no part of it carries one; else the last part of
 * a name without its model file type ("qwen" for a Hugging Face cache path
 * models--Qwen--x/snapshots/<commit>/qwen.gguf), when that part carries
 * none; else "Unnamed model". */
export function plainModelName(offer) {
  if (offer?.title) return offer.title;
  const names = [offer?.family, ...(offer?.names ?? [])].filter((name) => typeof name === 'string' && name);
  for (const name of names) {
    if (!digestName.test(name)) return name;
  }
  for (const name of names) {
    const last = name.split(/[\\/]/).filter(Boolean).pop() ?? '';
    const plain = last.replace(/\.(gguf|safetensors|bin|onnx|ggml)$/i, '');
    if (plain && !digestName.test(plain)) return plain;
  }
  return 'Unnamed model';
}

/** The name OpenCode's model picker shows: the model's plain name
 * ("Qwen3.6 35B"). OpenCode shows the context from limit.context in its own
 * meter, and the heading above it says where the models run
 * ("OpenAbstractions", "DeepSeek · OpenAbstractions"). configure.js writes
 * OpenCode's config with this same projection. */
export function modelName(offer) {
  return plainModelName(offer);
}

/** A server test from the router's local-placement offers: a server is on
 * this computer when the router offered it at placement local. */
export function localServers(localOffers) {
  const names = new Set();
  for (const offer of localOffers ?? []) for (const server of offer.servers ?? []) names.add(server);
  return (server) => names.has(server);
}

function knownContext(offer) {
  return knownSources.has(offer?.contextSource) ? Number(offer.context ?? 0n) : null;
}

/** Project the router's Offer answer (router CONTRACT.md ROUTE-D8) onto
 * OpenCode models. The router folds each family's names and context; this
 * keeps OpenCode's config shape and its answer reserve. A family whose offer
 * leaves input becomes one model under its first name. `observed`, when given,
 * receives each family's known context, including families omitted because
 * that context leaves no input beside the output budget. Each alias whose
 * server reported no request limit gets one diagnostic. */
export function servableChatModels(offers, diagnostics = [], observed = null, isLocal = () => true) {
  const models = {};
  for (const offer of offers ?? []) {
    const context = knownContext(offer);
    if (context === null) {
      if (offer.contextSource === 'none') continue;
      // An unknown offer names its aliases; one naming none, or a source word
      // this projection has never heard, is withheld under its family.
      const withheld = offer.unknown?.length ? offer.unknown : [offer.family];
      for (const alias of withheld) {
        diagnostics.push({model: alias, reason: 'its server reported no request limit; configure or load the model, then refresh inventory'});
      }
      continue;
    }
    if (observed) setOwn(observed, offer.family, context);
    if (Number(offer.input ?? 0n) <= 0 || !offer.names?.length) continue;
    setOwn(models, offer.family, {
      name: modelName(offer),
      limit: {context, output: outputBudget},
      tool_call: true,
    });
  }
  return models;
}

/** The sentence the plugin logs, and configure.js refuses with, when the
 * runtime's router predates Offer. */
export const olderRuntimeMessage = 'This OpenAbstractions runtime is older than the router Offer method; update OpenAbstractions.';

/** True when a router call failed because the runtime predates the method. */
export function olderRuntime(error) {
  return error?.code === 'unknown_method';
}

/** The router's offers for placement at the plugin's answer reserve, from a
 * fresh survey unless `fresh` is false (the router's latest survey, whose age
 * the observation carries). A router client whose package predates Offer has
 * no offer method; that reads as the same older runtime an unknown_method
 * answer reports. */
export async function readOffers(client, placement, {fresh = true} = {}) {
  if (typeof client?.offer !== 'function') throw Object.assign(new Error(olderRuntimeMessage), {code: 'unknown_method'});
  const snapshot = await client.offer({profile: '', placement, reserve: outputReserve, fresh: fresh !== false});
  if (snapshot?.outcome !== 'offered') {
    const outcome = typeof snapshot?.outcome === 'string' ? snapshot.outcome : 'invalid_result';
    throw Object.assign(new Error(`OpenAbstractions router offer: ${outcome}`), {code: outcome, outcome});
  }
  return snapshot;
}

/** The router's offers for placement once they differ from cursor, or when
 * waitMs ends (router@1 ObserveOffer). An empty cursor answers at once. A
 * router client or runtime without the method reads as an older runtime. */
export async function observeOffers(client, placement, cursor, waitMs) {
  if (typeof client?.observeOffer !== 'function') throw Object.assign(new Error(olderRuntimeMessage), {code: 'unknown_method'});
  const observation = await client.observeOffer({profile: '', placement, reserve: outputReserve, fresh: false}, cursor, BigInt(waitMs));
  if (observation?.outcome !== 'offered') {
    const outcome = typeof observation?.outcome === 'string' ? observation.outcome : 'invalid_result';
    throw Object.assign(new Error(`OpenAbstractions router offer: ${outcome}`), {code: outcome, outcome});
  }
  return observation;
}

function managedBaselines(options) {
  const value = options?.[inventoryMarker];
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  return value;
}

/** The placement whose offers a saved candidate reads: hosted, or local. */
export function candidatePlacement(candidate) {
  return candidate?.placement === 'hosted' ? 'hosted' : 'local';
}

/** A preference's model from the router's offers for each candidate's
 * placement: the smallest context across the candidates' families. */
function selectionModelFor(offersByPlacement, policy) {
  if (!policy || !Array.isArray(policy.candidates)) return {model: null, reason: 'invalid policy'};
  const capacities = [];
  let firstOffered = null;
  let unknown = '';
  for (const candidate of policy.candidates) {
    for (const offer of offersByPlacement?.[candidatePlacement(candidate)] ?? []) {
      if (candidate.model !== offer.family && !(offer.names ?? []).includes(candidate.model)) continue;
      const context = knownContext(offer);
      if (context === null) {
        if (offer.contextSource !== 'none') unknown = offer.unknown?.[0] ?? offer.family;
        continue;
      }
      capacities.push(context);
      firstOffered ??= offer;
    }
    // Offers supply display context for this saved choice. They do not
    // establish that its pinned provider can execute a future request.
  }
  if (unknown) return {model: null,
    reason: `display context is unknown for saved preference model ${unknown}; its server reported no request limit; configure or load it, then refresh inventory`};
  if (!capacities.length) return {model: null, reason: 'no matching model context is currently offered for this saved preference'};
  const context = Math.min(...capacities);
  if (context <= outputBudget) return {model: null, reason: 'effective context does not exceed the configured output budget'};
  const name = policy.name === 'oa-chat-default' ? plainModelName(firstOffered) : `Saved preference: ${policy.name}`;
  return {model: {name, limit: {context, output: outputBudget}, tool_call: true}, reason: ''};
}

/** Add currently readable policy models while preserving user-owned and
 * edited entries. offersByPlacement maps local and hosted to the router's
 * offers for that placement. */
export function refreshSelectionPolicyModels(provider, offersByPlacement, reads) {
  const options = provider?.options ?? {};
  const prior = options[selectionMarker];
  const baselines = prior && typeof prior === 'object' && !Array.isArray(prior) ? prior : null;
  const existing = provider?.models && typeof provider.models === 'object' ? provider.models : {};
  const preserved = {};
  let removed = 0;
  for (const [id, value] of Object.entries(existing)) {
    const managed = baselines !== null && Object.hasOwn(baselines, id) && sameModel(value, baselines[id]);
    if (managed) removed++;
    else setOwn(preserved, id, value);
  }
  const next = {};
  for (const [id, value] of Object.entries(preserved)) setOwn(next, id, value);
  const managedNow = {};
  const routes = {};
  const diagnostics = [];
  const mapping = options.oaSelectionPolicies;
  for (const [id, name] of Object.entries(mapping && typeof mapping === 'object' && !Array.isArray(mapping) ? mapping : {})) {
    if (Object.hasOwn(preserved, id)) {
      diagnostics.push({model: id, reason: 'model ID is already used by a configured or user-edited model'});
      continue;
    }
    const read = reads?.[name];
    if (!read || read.outcome !== 'found' || read.policy?.name !== name) {
      diagnostics.push({model: id, reason: read?.outcome ?? 'policy could not be read'});
      continue;
    }
    const selection = selectionModelFor(offersByPlacement, read.policy);
    if (!selection.model) {
      diagnostics.push({model: id, reason: selection.reason});
      continue;
    }
    // The app's own policy is the picker's "Default (Qwen3.6 35B)" row: it
    // follows the Panel's default and carries that model's context. Its API
    // id is the same name, which OpenCode's prompt names; the route option
    // maps it back to this entry's policy.
    let model = selection.model;
    if (isDefaultSelection(id, name)) {
      const shown = defaultEntryName(selection.model.name);
      model = {...selection.model, id: shown, name: shown};
      setOwn(routes, shown, id);
    }
    setOwn(next, id, model);
    setOwn(managedNow, id, {...model});
  }
  provider.models = next;
  if (provider.options && typeof provider.options === 'object') {
    provider.options[selectionMarker] = managedNow;
    if (Object.keys(routes).length) provider.options[routeModelsOption] = routes;
    else delete provider.options[routeModelsOption];
  }
  return {added: Object.keys(managedNow).length, removed, preserved: Object.keys(preserved).length, diagnostics};
}

/** A remembered answer's providers as today's picker lists them, for an
 * answer an earlier version kept: headings under today's names, each model
 * this plugin listed under its plain name (earlier names carried " · 32k" or
 * " · hosted: deepseek"), and the app's own chat route as the "Default (…)"
 * row of the model OA chose, with that model's context. Entries the person
 * owns stay as they were. chat is the answer's chat facts; its choice names
 * the model OA chose ("openabstractions/qwen3.6-35b-a3b"). */
export function currentRememberedProviders(providers, chat, providerID) {
  const plain = (name) => (typeof name === 'string' ? name.split(' · ')[0] : name);
  const result = [];
  const byID = new Map();
  for (const [id, source] of providers ?? []) {
    const provider = structuredClone(source);
    if (legacyHeadings[provider.name] === 'local') provider.name = modelHeadings.local;
    else if (legacyHeadings[provider.name] === 'hosted') provider.name = modelHeadings.hosted;
    if (!provider.options || typeof provider.options !== 'object' || Array.isArray(provider.options)) provider.options = {};
    if (!provider.models || typeof provider.models !== 'object' || Array.isArray(provider.models)) provider.models = {};
    const managed = managedBaselines(provider.options);
    for (const [model, entry] of Object.entries(provider.models)) {
      if (!managed || !Object.hasOwn(managed, model) || !sameModel(entry, managed[model])) continue;
      const next = {...entry, name: plain(entry.name)};
      setOwn(provider.models, model, next);
      setOwn(managed, model, {...next});
    }
    result.push([id, provider]);
    byID.set(id, provider);
  }
  const main = byID.get(providerID);
  if (!main) return result;
  const routeID = defaultSelectionModel('chat');
  const markers = main.options[selectionMarker] && typeof main.options[selectionMarker] === 'object' ? main.options[selectionMarker] : {};
  const route = main.models[routeID];
  if (route && !(Object.hasOwn(markers, routeID) && sameModel(route, markers[routeID]))) return result;
  const choice = typeof chat?.choice === 'string' ? chat.choice : '';
  const slash = choice.indexOf('/');
  const chosen = slash > 0 ? byID.get(choice.slice(0, slash))?.models?.[choice.slice(slash + 1)] : null;
  const name = chosen?.name ?? (route ? plain(String(route.name ?? '').replace(/^Default · /, '').replace(/^Default \((.*)\)$/, '$1')) : '');
  const context = chosen?.limit?.context ?? route?.limit?.context;
  if (!name || !Number.isSafeInteger(context)) {
    if (route) delete main.models[routeID];
    return result;
  }
  const shown = defaultEntryName(name);
  const row = {id: shown, name: shown, limit: {context, output: outputBudget}, tool_call: true};
  setOwn(main.models, routeID, row);
  main.options[selectionMarker] = {...markers, [routeID]: {...row}};
  main.options[routeModelsOption] = {[shown]: routeID};
  main.options.oaSelectionPolicies = withDefaultSelectionPolicies(main.options);
  return result;
}

/** Replace untouched managed entries with the projection of offers, the
 * router's Offer answer for the provider's placement. */
export function refreshProviderInventory(provider, offers, isLocal = () => true) {
  if (!provider || typeof provider !== 'object') return {added: 0, removed: 0, preserved: 0};
  const baselines = managedBaselines(provider.options);
  const existing = provider.models && typeof provider.models === 'object' ? provider.models : {};
  const preserved = {};
  let removed = 0;
  for (const [id, value] of Object.entries(existing)) {
    const managed = baselines !== null && Object.hasOwn(baselines, id);
    const baseline = managed ? baselines[id] : undefined;
    if (managed && baseline && sameModel(value, baseline)) removed++;
    else setOwn(preserved, id, value);
  }
  const diagnostics = [];
  const observed = {};
  const discovered = servableChatModels(offers, diagnostics, observed, isLocal);
  const warnings = staleContextWarnings(preserved, observed);
  const next = {};
  for (const [id, value] of Object.entries(preserved)) setOwn(next, id, value);
  let added = 0;
  for (const [id, value] of Object.entries(discovered)) {
    if (Object.hasOwn(preserved, id)) continue;
    setOwn(next, id, value);
    added++;
  }
  provider.models = next;
  if (!provider.options || typeof provider.options !== 'object' || Array.isArray(provider.options)) provider.options = {};
  // Track only entries this refresh produced. Existing legacy/custom values
  // remain unclaimed and therefore survive later refreshes.
  provider.options[inventoryMarker] = discovered;
  return {added, removed, preserved: Object.keys(preserved).length, diagnostics, warnings};
}

/** One warning for each user-owned entry whose limit.context exceeds the
 * context OA observes for the family of the same ID. The entry is kept. */
function staleContextWarnings(preserved, observed) {
  const warnings = [];
  for (const [id, value] of Object.entries(preserved)) {
    if (!Object.hasOwn(observed, id)) continue;
    const configured = value?.limit?.context;
    const seen = observed[id];
    if (!Number.isSafeInteger(configured) || configured <= seen) continue;
    const count = (n) => n.toLocaleString('en-US');
    warnings.push({model: id, configured, observed: seen,
      reason: `OpenCode limit.context for ${id} is ${count(configured)} tokens, but OpenAbstractions observes ${count(seen)} tokens; ` +
        `lower it to ${count(seen)} or remove the entry so inventory refresh manages it.`});
  }
  return warnings;
}

/** The selection policies this app reads for its profiles, profile to
 * policy name (VISION 2026-10-06). The person's model choice in
 * OpenAbstractions writes a policy of this name for OpenCode's program; the
 * plugin reads the effective policy by name, including the same named account
 * default when its program policy is absent, with no configure step. */
export const defaultSelectionPolicies = Object.freeze({chat: 'oa-chat-default'});

/** The model ID OpenCode lists for a profile's own selection policy; the
 * plugin makes it OpenCode's model at start when the policy is stored. */
export function defaultSelectionModel(profile) {
  return `oa-${profile}`;
}

/** The provider's oaSelectionPolicies with the app's own profile policies
 * added under their model IDs. An entry the configuration names under the
 * same model ID wins. This returns a new mapping; the plugin's refresh stores
 * it in the main heading's options, which OpenCode then passes to each
 * request. */
export function withDefaultSelectionPolicies(options) {
  const configured = options?.oaSelectionPolicies;
  const merged = {};
  for (const [profile, name] of Object.entries(defaultSelectionPolicies)) setOwn(merged, defaultSelectionModel(profile), name);
  if (configured && typeof configured === 'object' && !Array.isArray(configured)) {
    for (const [id, name] of Object.entries(configured)) setOwn(merged, id, name);
  }
  return merged;
}

/** Whether a model ID and policy name are one of the app's own defaults. */
export function isDefaultSelection(id, name) {
  return Object.entries(defaultSelectionPolicies).some(([profile, policy]) => defaultSelectionModel(profile) === id && policy === name);
}

/** OA's default chat model among servable families: the first by name. The
 * plugin and configure.js both use it when no model is chosen. */
export function defaultChatFamily(models) {
  return Object.keys(models && typeof models === 'object' ? models : {}).sort()[0] ?? '';
}

/** The real model the app's own chat policy chooses for OpenCode
 * (VISION 2026-10-06, "an app reads its own selection policy"): the first
 * candidate the router offers, as the picker lists it. A local candidate is
 * one of the main heading's families (discovered); a hosted one is listed
 * under the hosted heading beside it. '' when no candidate is offered. */
export function chatChoiceModel(providerID, read, offersByPlacement, discovered) {
  if (read?.outcome !== 'found' || !Array.isArray(read.policy?.candidates)) return '';
  for (const candidate of read.policy.candidates) {
    const placement = candidatePlacement(candidate);
    for (const offer of offersByPlacement?.[placement] ?? []) {
      if (candidate.model !== offer.family && !(offer.names ?? []).includes(candidate.model)) continue;
      if (placement === 'local' && Object.hasOwn(discovered ?? {}, offer.family)) return `${providerID}/${offer.family}`;
      if (placement === 'hosted' && Object.keys(servableChatModels([offer], [], null, () => false)).length) {
        return `${providerID}-hosted/${offer.family}`;
      }
    }
  }
  return '';
}

/** The model OpenCode's start selects for chat, or '' to keep the configured
 * one. selected is the config's model; choice is the real model the app's own
 * chat policy chooses (chatChoiceModel); discovered holds the servable
 * families the refresh listed.
 *
 * - A model the config names is the person's pick and stays, except the
 *   app's own policy route (`oa-chat`), which names no picker entry.
 * - Otherwise OA's choice is selected, and with none OA's default family. */
export function chatModelAtStart({selected, providerID, choice, discovered}) {
  const prefix = `${providerID}/`;
  const configured = typeof selected === 'string' ? selected : null;
  if (configured === null) return '';
  if (configured !== '' && configured !== prefix + defaultSelectionModel('chat')) return '';
  if (typeof choice === 'string' && choice) return choice;
  const family = defaultChatFamily(discovered);
  return family ? prefix + family : '';
}

export const inventoryMarkerOption = inventoryMarker;
/** The answer reserve the plugin asks the router to keep, as Offer's i64. */
export const outputReserve = BigInt(outputBudget);
export const selectionPolicyMarkerOption = selectionMarker;
