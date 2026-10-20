const outputBudget = 4096;
// The router's offer sources whose context is a number OpenCode can plan on.
const knownSources = new Set(['effective', 'maximum', 'default']);
const inventoryMarker = 'oaInventoryManagedModels';
const selectionMarker = 'oaSelectionPolicyManagedModels';

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

/** An offer's context when its source is a number to plan on, else null. */
/** The name OpenCode's model picker shows: the family, which is the model's
 * key, its context, and where it runs, so a person always knows whether the
 * conversation stays on this computer ("qwen3.6-35b-a3b · 32k · this
 * computer: lemonade", "deepseek-chat · 64k · hosted: deepseek"). Where it runs comes
 * from the router's placements, never from a name: isLocal says whether the
 * router offered a server at placement local (router offer.go admits).
 * configure.js writes OpenCode's config with this same projection. */
export function modelName(offer, context, isLocal = () => true) {
  const parts = [offer.family, context >= 1024 ? `${Math.floor(context / 1024)}k` : String(context)];
  const servers = (offer.servers ?? []).filter((server) => typeof server === 'string' && server);
  const local = servers.filter((server) => isLocal(server));
  const hosted = servers.filter((server) => !isLocal(server));
  const where = [];
  if (local.length || !hosted.length) where.push(local.length ? `this computer: ${local.join(', ')}` : 'this computer');
  if (hosted.length) where.push(`hosted: ${hosted.join(', ')}`);
  parts.push(where.join('; '));
  return parts.join(' · ');
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
      name: modelName(offer, context, isLocal),
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

/** The router's offers for placement at the plugin's answer reserve. A router
 * client whose package predates Offer has no offer method; that reads as the
 * same older runtime an unknown_method answer reports. */
export async function readOffers(client, placement) {
  if (typeof client?.offer !== 'function') throw Object.assign(new Error(olderRuntimeMessage), {code: 'unknown_method'});
  const snapshot = await client.offer({profile: '', placement, reserve: outputReserve, fresh: true});
  if (snapshot?.outcome !== 'offered') {
    const outcome = typeof snapshot?.outcome === 'string' ? snapshot.outcome : 'invalid_result';
    throw Object.assign(new Error(`OpenAbstractions router offer: ${outcome}`), {code: outcome, outcome});
  }
  return snapshot;
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
    }
    // A stored candidate with no currently offered family is unavailable at
    // this snapshot; another candidate may still make the preference useful.
  }
  if (unknown) return {model: null,
    reason: `effective context is unknown for eligible policy model ${unknown}; its server reported no request limit; configure or load it, then refresh inventory`};
  if (!capacities.length) return {model: null, reason: 'no candidate is currently eligible'};
  const context = Math.min(...capacities);
  if (context <= outputBudget) return {model: null, reason: 'effective context does not exceed the configured output budget'};
  return {model: {name: `Preference: ${policy.name}`, limit: {context, output: outputBudget}, tool_call: true}, reason: ''};
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
    setOwn(next, id, selection.model);
    setOwn(managedNow, id, selection.model);
  }
  provider.models = next;
  if (provider.options && typeof provider.options === 'object') provider.options[selectionMarker] = managedNow;
  return {added: Object.keys(managedNow).length, removed, preserved: Object.keys(preserved).length, diagnostics};
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
 * plugin reads it by name, with no configure step. */
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

/** The model OpenCode's start selects for chat, or '' to keep the configured
 * one. selected is the config's model; listed says the main heading lists the
 * app's own chat policy entry; read is that policy's read; discovered holds
 * the servable families the refresh listed.
 *
 * - A listed chat policy replaces a model under providerID: the person chose
 *   OpenCode's chat model in OpenAbstractions (VISION 2026-10-06, "an app reads
 *   its own selection policy"). A model under another provider stays.
 * - With no policy stored (read unknown), a config that names no model, or
 *   names the policy entry, gets OA's default family.
 * - Any other read (forbidden, unavailable, or a stored policy with no
 *   eligible candidate now) keeps the configured model. */
export function chatModelAtStart({selected, providerID, listed, read, discovered}) {
  const prefix = `${providerID}/`;
  const chosen = prefix + defaultSelectionModel('chat');
  const configured = typeof selected === 'string' ? selected : null;
  if (configured === null) return '';
  if (listed) return (configured === '' || configured.startsWith(prefix)) && configured !== chosen ? chosen : '';
  if (read?.outcome !== 'unknown' || (configured !== '' && configured !== chosen)) return '';
  const family = defaultChatFamily(discovered);
  return family ? prefix + family : '';
}

export const inventoryMarkerOption = inventoryMarker;
/** The answer reserve the plugin asks the router to keep, as Offer's i64. */
export const outputReserve = BigInt(outputBudget);
export const selectionPolicyMarkerOption = selectionMarker;
