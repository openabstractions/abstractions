import {permissionVerdict, permissionQuestions, permissionWaitMs, waitForPermission, resolveNativePermission} from './permission-wait.js';
import {Machine} from '@openabstractions/facade';
import {APICallError} from '@ai-sdk/provider';
import {
  DeltaKind, Fold, PartKind, ReplyOutcome, RequestGuarantee, Role, StopReason, resolveInference, resolveInferenceV2, resolveInferenceV3,
} from '@openabstractions/inference';
import {generateChat3, streamChat3} from './chat3.js';
import {runtimeConnector} from './runtime-connector.js';
import {availability, runtimeUnavailable, runtimeUntrusted, unavailableMessage, untrustedMessage} from './availability.js';

async function resolveObservedChat(machine, resolve, contract, need) {
  const binding = await machine.resolveService(contract, need);
  const client = await resolve({resolveService: async () => binding}, need);
  // Cancel uses the same proven endpoint and caller with its own bounded
  // wait, after the user's observation AbortSignal has already ended.
  const cancelClient = await resolve({resolveService: async () => binding.withWaiting({deadline: null, cancellation: null})}, need);
  const cancel = cancelClient.cancel.bind(cancelClient);
  return new Proxy(client, {get(target, key) {
    if (key === 'cancel') return cancel;
    const value = Reflect.get(target, key);
    return typeof value === 'function' ? value.bind(target) : value;
  }});
}

function questionVerdict(observed) { return permissionVerdict(observed); }

// The provider owns observation separately from explicit user cancellation.
// A failed wait retains the accepted handle on OAInferenceError; it never
// submits another request or invokes Cancel as an observation side effect.
async function openObserved(chat, request, guarantees, signal) {
  const admission = guarantees === undefined ? await chat.start(request) : await chat.startWithGuarantees(request, guarantees);
  const accepted = admission.outcome === 'accepted';
  let ended = !accepted;
  let cancelPromise;
  const cancel = () => {
    signal?.removeEventListener('abort', onAbort);
    if (!accepted || ended) return Promise.resolve();
    return cancelPromise ??= chat.cancel(admission.operation);
  };
  const onAbort = () => { void cancel().catch(() => undefined); };
  if (accepted) {
    signal?.addEventListener('abort', onAbort, {once: true});
    if (signal?.aborted) onAbort();
  }
  const stream = (async function* () {
    try {
      if (!accepted) {
        yield {kind: DeltaKind.End, end: {...admission, stopReason: 'no_stop', usage: {}, message: {role: 'assistant', parts: []}}};
        return;
      }
      let cursor = 0n;
      for (;;) {
        const page = await chat.observe(admission.operation, cursor, 256n, 65536n, 25000n);
        if (page.outcome !== 'page') throw Object.assign(new Error(`OpenAbstractions observe ${page.outcome}`), {operation: admission.operation, cursor});
        for (const delta of page.deltas) {
          if (delta.kind === DeltaKind.End) ended = true;
          yield delta;
        }
        cursor = page.next;
        if (page.atEnd) { ended = true; return; }
      }
    } finally { signal?.removeEventListener('abort', onAbort); }
  })();
  return {admission, stream, cancel};
}

/** A typed OA terminal outcome surfaced through the AI SDK provider boundary.
 * A not_permitted outcome whose reason is a pending rights question reads as
 * one sentence naming the Panel. A context-window refusal (INF-Q7) reads as
 * the overflow message OpenCode compacts on, with the model's window, the
 * counted input and the request's answer reserve in `context`. Every other
 * outcome keeps its own wording. The typed outcome and reason stay on the
 * error either way. */
export class OAInferenceError extends APICallError {
  constructor(reply, op, source, operation = '', reserve = 0, localOnly = false) {
    const cause = reply ? Object.assign(new Error('OpenAbstractions terminal outcome'), {
      outcome: reply.outcome, reason: reply.reason, host: reply.host, model: reply.model, op, operation,
    }) : source;
    const overflow = contextOverflow(reply, reserve);
    // A lost runtime says OA is away, in the words of the toast; a runtime
    // whose identity did not verify says so.
    const away = !reply && runtimeUnavailable(source);
    const message = !reply && localOnly && source?.code === 'unknown_method'
      ? 'OpenAbstractions: this runtime needs an update to keep a saved preference on this computer. The Local assistant request was refused.'
      : !reply && runtimeUntrusted(source) ? untrustedMessage
      : away ? `${unavailableMessage} This model call could not reach it.`
        : overflow ? overflowMessage(reply, overflow) : refusalMessage(reply, localOnly);
    super({message, url: '',
      requestBodyValues: undefined, isRetryable: false, cause});
    this.name = 'OAInferenceError';
    this.outcome = reply?.outcome ?? 'unknown';
    this.reason = reply?.reason ?? '';
    this.host = reply?.host ?? '';
    this.model = reply?.model ?? '';
    this.op = op;
    this.operation = operation;
    if (overflow) this.context = overflow;
  }
}

/** The window and counted input of a refused context overflow, each 0 when
 * the runtime did not know it, and the answer reserve this request asked for. */
function contextOverflow(reply, reserve) {
  // chat@2 refuses an overflow; a chat@3 request the server accepted ends
  // failed with the same reason (the owner's R27 overflow read "ended without
  // an answer" because only refused was taken).
  if (!['refused', 'failed'].includes(reply?.outcome) || typeof reply.reason !== 'string') return null;
  const match = /^upstream:context_length_exceeded:(\d{1,15}):(\d{1,15})$/.exec(reply.reason);
  if (!match) return null;
  return {window: Number(match[1]), input: Number(match[2]), reserve: Number.isSafeInteger(reserve) && reserve > 0 ? reserve : 0};
}

function tokens(value) {
  return value.toLocaleString('en-US');
}

// The first sentence keeps OpenCode's own overflow wording ("exceeds the
// available context size"): OpenCode reads it as a context overflow, compacts
// the conversation and continues, and never retries it unchanged. The counted
// input and the answer reserve stay on the error's `context`.
function overflowMessage(reply, {window, reserve}) {
  const named = typeof reply.model === 'string' && reply.model && !/[0-9a-f]{16,}/i.test(reply.model);
  const model = named ? reply.model : 'the model';
  const sentences = [`OpenAbstractions: this conversation is longer than ${model} can hold.`,
    `It exceeds the available context size${window ? ` of ${tokens(window)} tokens` : ''}.`];
  if (window && reserve && reserve >= window) {
    sentences.push(`The ${tokens(reserve)}-token answer reserve fills the whole context; lower this model's output limit.`);
  }
  sentences.push('Start a new session or compact it.');
  return sentences.join(' ');
}

/** A refusal a thrown error carries in place of a terminal reply: a service
 * error whose code is a refusal outcome and whose message is its reason
 * (ServiceError), or an error with outcome and reason fields. Only a reason
 * made of rights words is taken, so a pending question or a blocked rule
 * reads as refusalMessage words it; null for any other error. */
function thrownRefusal(error) {
  const outcome = typeof error?.outcome === 'string' ? error.outcome : typeof error?.code === 'string' ? error.code : '';
  if (outcome !== 'not_permitted' && outcome !== 'forbidden') return null;
  const reason = typeof error.reason === 'string' && error.reason ? error.reason : typeof error.message === 'string' ? error.message : '';
  if (!/^rights:(not_granted|denied)(?:\s|$)/.test(reason) && !pendingQuestions(reason).length) return null;
  return {outcome: 'not_permitted', reason, host: '', model: ''};
}

/** A transport failure as OpenCode's log prints it: the native stage with
 * its IPC status, "open: ipc status 1", in the words unavailableReason uses,
 * where the stage alone ("Error: open") said nothing (2026-11-01). The status
 * and transferred count stay on it for the classifiers. Any other error is
 * returned as it is. */
function transportCause(error) {
  if (!(error instanceof Error) || typeof error.status !== 'number' || !('transferred' in error)) return error;
  return Object.assign(new Error(`${error.message}: ipc status ${error.status}`, {cause: error}),
    {name: error.name, status: error.status, transferred: error.transferred});
}

function ambiguousError(error, op, operation = '', localOnly = false) {
  if (error instanceof OAInferenceError) return error;
  const refusal = thrownRefusal(error);
  if (refusal) return new OAInferenceError(refusal, op, error, operation, 0, localOnly);
  if (runtimeUnavailable(error)) availability.markDown(error);
  return new OAInferenceError(null, op, transportCause(error), operation, 0, localOnly);
}

/** The sentence for a request the Local assistant sent: OA serves it only on
 * this computer, and a model that runs only on a hosted server is refused. */
const localOnlyRefusal = 'OpenAbstractions: the Local assistant keeps this conversation on this computer, and no model here '
  + 'serves this choice. Choose a model marked "this computer", or press Tab for the Build agent to use a hosted model.';

/** The first-use questions a refusal names as pending, in order
 * (inference INF-W11): one rights:question_pending:<id> word each. */
export function pendingQuestions(reason) { return permissionQuestions(reason); }

// The sentences for OA's first-use question (asks ASK-A14). The native
// popup asks the person; these say what the chat is waiting for.
export const askingMessage = 'OpenAbstractions is asking whether OpenCode may use this model; answer the question to continue.';
export const waitingMessage = 'OpenAbstractions is still waiting for your answer about OpenCode using this model. Open Questions in the OpenAbstractions Panel to answer the request, then send again.';
export const refusedMessage = 'You chose not to let OpenCode use this model for now; it can ask again in 10 minutes.';
export const blockedMessage = 'OpenAbstractions: this program is blocked from doing that. '
  + 'To change that, allow it on the Permissions page of the OpenAbstractions Panel.';
const notGrantedMessage = 'OpenAbstractions: OpenCode may not use this yet and no question is waiting. '
  + 'Allow it on the Permissions page of the OpenAbstractions Panel.';

// A hosted model's account OpenCode may not use yet: the router withheld the
// hosted server from this request (router:unauthorised), or the server's
// account was not applied for OpenCode (credential:not_permitted). R10
// showed the generic sentence for both (2026-11-08).
export const hostedMessage = 'OpenAbstractions: allow OpenCode to use this hosted service in the OpenAbstractions popup, then send again.';
function hostedRefusal(reply) {
  const reason = typeof reply?.reason === 'string' ? reply.reason : '';
  return reply?.outcome === 'no_host' && /^router:unauthorised(?:\s|$)/.test(reason)
    || reply?.outcome === 'not_permitted' && /^credential:(not_permitted|not_granted)(?::\S*)?(?:\s|$)/.test(reason);
}

function refusalMessage(reply, localOnly = false) {
  if (reply?.outcome === 'unavailable' && ['engine:loading', 'native:engine:loading'].includes(reply.reason)) {
    return 'OpenAbstractions is loading this model; try again shortly.';
  }
  if (reply?.outcome === 'unavailable' && /^activating:/.test(reply.reason ?? '')) {
    return 'OpenAbstractions is starting the program that runs this model; try again shortly.';
  }
  if (localOnly && reply?.outcome === 'no_host') return localOnlyRefusal;
  if (reply?.verdict === 'refused') return refusedMessage;
  if (reply?.verdict === 'blocked') return blockedMessage;
  const permission = reply?.outcome === 'not_permitted' || reply?.outcome === 'forbidden';
  const reason = permission && typeof reply.reason === 'string' ? reply.reason : '';
  if (pendingQuestions(reason).length) return waitingMessage;
  // A denial is the person's own rule, changed on the Panel's Permissions page.
  if (/^rights:denied(?:\s|$)/.test(reason)) return blockedMessage;
  if (reply?.outcome === 'not_permitted' && /^rights:not_granted(?:\s|$)/.test(reason)) return notGrantedMessage;
  if (hostedRefusal(reply)) return hostedMessage;
  return 'OpenAbstractions could not complete this model call. Check the OpenAbstractions Panel for the request outcome.';
}

// How long the router's host table answers where a reply ran before it is read again.
const hostTableTTL = 30000;

// How long a chat waits in all for the person to answer OA's first-use
// question, and the longest single ObserveQuestion wait (asks ASK-A14).
const questionWaitMs = permissionWaitMs;

function textOutput(output) {
  if (!output) return '';
  if (output.type === 'text' || output.type === 'error-text') return output.value;
  if (output.type === 'execution-denied') return output.reason ?? 'Tool execution denied.';
  return JSON.stringify(output.value);
}

function json(value) {
  return typeof value === 'string' ? value : JSON.stringify(value);
}

/** The answer tokens a request reserves: OpenCode's limit.output, else 4096. */
function answerReserve(options) {
  return options.maxOutputTokens ?? 4096;
}

/** Whether OpenCode's chat.params hook marked this request local-only: the
 * plugin sets oaLocalOnly for the Local assistant (plugin.js), and OpenCode
 * passes it under this provider's key in providerOptions. */
function localOnlyRequested(options) {
  return Object.values(options?.providerOptions ?? {}).some((value) => value?.oaLocalOnly === true);
}

/** The project folder OpenCode's chat.params hook named for a Local
 * assistant request (plugin.js), or ''. */
function projectOf(options) {
  for (const value of Object.values(options?.providerOptions ?? {})) {
    if (typeof value?.oaProject === 'string' && value.oaProject) return value.oaProject;
  }
  return '';
}

/** Keeps a Local assistant session start's prefix for the next OpenCode
 * start's preload@1 (preload.js). Loaded on demand so the request path
 * imports nothing more; a failure costs only that preload. */
function rememberPrefix(model, options, request) {
  const project = projectOf(options);
  if (!project) return;
  void import('./preload.js')
    .then(({rememberSessionStart}) => rememberSessionStart({dir: model.config.preloadStateDir ?? undefined, project, model: model.modelId, request}))
    .catch(() => {});
}

function requestFor(modelId, options, config) {
  const messages = [];
  for (const message of options.prompt ?? []) {
    const parts = [];
    if (message.role === 'system') {
      parts.push({kind: PartKind.Text, text: message.content, digest: '', mediaType: '', callId: '', name: '', arguments: ''});
    } else {
      for (const part of message.content ?? []) {
        if (part.type === 'reasoning') {
          // chat@1 has no reasoning part; the trace never becomes answer
          // text (inference INF-Q8).
          continue;
        } else if (part.type === 'text') {
          parts.push({kind: PartKind.Text, text: part.text, digest: '', mediaType: '', callId: '', name: '', arguments: ''});
        } else if (part.type === 'tool-call') {
          parts.push({kind: PartKind.ToolCall, text: '', digest: '', mediaType: '', callId: part.toolCallId,
            name: part.toolName, arguments: json(part.input)});
        } else if (part.type === 'tool-result') {
          parts.push({kind: PartKind.ToolResult, text: textOutput(part.output), digest: '', mediaType: '',
            callId: part.toolCallId, name: '', arguments: ''});
        } else if (part.type === 'tool-approval-response') {
          continue;
        } else {
          throw new TypeError(`OpenAbstractions provider does not support prompt part ${part.type}`);
        }
      }
    }
    if (parts.length) messages.push({role: message.role, parts});
  }
  const tools = (options.tools ?? []).filter((tool) => tool.type === 'function').map((tool) => ({
    name: tool.name,
    description: tool.description ?? '',
    parameters: json(tool.inputSchema ?? {}),
  }));
  const guarantees = localOnlyRequested(options) ? [RequestGuarantee.LocalOnly]
    : config.requestGuarantees ?? [config.scope === 'remote' ? RequestGuarantee.HostedAllowed : RequestGuarantee.LocalOnly];
  if (!Array.isArray(guarantees) || guarantees.some((value) => typeof value !== 'string')) {
    throw new TypeError('requestGuarantees must contain strings');
  }
  return {
    model: modelId,
    messages,
    tools,
    options: {
      maxOutput: BigInt(answerReserve(options)),
      temperature: options.temperature == null ? null : {milli: BigInt(Math.round(options.temperature * 1000))},
      stop: options.stopSequences ?? [],
      jsonSchema: options.responseFormat?.type === 'json' && options.responseFormat.schema
        ? JSON.stringify(options.responseFormat.schema) : '',
    },
    extensions: {},
    requiredExtensions: [],
    guarantees,
    credential: config.credential ?? '',
  };
}

// The Default row's API id ("Default (Qwen3.6 35B)", inventory.js
// defaultEntryName) and the model ID of the app's own chat policy.
const defaultRouteName = /^Default \(.+\)$/;
const defaultChatModel = 'oa-chat';

/** The model ID a request names: a route entry's API id ("Default (Qwen3.6
 * 35B)") maps to its entry ("oa-chat") through oaRouteModels (inventory.js).
 * The Default row keeps its API id from the instance's model list while these
 * options were fixed earlier, so a Default name the options do not hold yet
 * (the Panel's default changed since) is still the app's own chat policy.
 * Every other id names itself. */
function routedModelId(modelId, config) {
  const routes = config?.oaRouteModels;
  const known = routes && typeof routes === 'object' && !Array.isArray(routes) && Object.hasOwn(routes, modelId);
  if (known && typeof routes[modelId] === 'string' && routes[modelId]) return routes[modelId];
  const selections = config?.oaSelectionPolicies;
  if (!known && typeof modelId === 'string' && defaultRouteName.test(modelId) && selections
      && typeof selections === 'object' && Object.hasOwn(selections, defaultChatModel)) return defaultChatModel;
  return modelId;
}

function selectionPolicyFor(modelId, config) {
  const selections = config.oaSelectionPolicies;
  const id = routedModelId(modelId, config);
  if (!selections || typeof selections !== 'object' || Array.isArray(selections) ||
      !Object.hasOwn(selections, id)) return '';
  const name = selections[id];
  return typeof name === 'string' && name.length > 0 ? name : '';
}

function requestV2For(policyName, options, config) {
  const request = requestFor('', options, {...config, requestGuarantees: []});
  return {
    target: {model: '', selectionPolicy: policyName},
    content: {
      messages: request.messages,
      tools: request.tools,
      options: request.options,
      extensions: request.extensions,
      requiredExtensions: request.requiredExtensions,
      guarantees: [],
      credential: '',
    },
  };
}

function answeringMetadata(reply, admission, policyName, placement = 'unknown') {
  return {openabstractions: {
    outcome: reply.outcome,
    reason: reply.reason,
    host: reply.host,
    placement,
    model: reply.model,
    operation: admission?.operation ?? '',
    policy: policyName,
    preferredModel: admission?.preferredModel ?? '',
    selectionReason: admission?.selectionReason ?? '',
  }};
}

function finishReason(reply) {
  if (reply.outcome !== ReplyOutcome.Completed) return {unified: 'error', raw: reply.outcome};
  switch (reply.stopReason) {
    case StopReason.End: return {unified: 'stop', raw: reply.stopReason};
    case StopReason.MaxOutput: return {unified: 'length', raw: reply.stopReason};
    case StopReason.ToolCalls: return {unified: 'tool-calls', raw: reply.stopReason};
    case StopReason.ContentFilter: return {unified: 'content-filter', raw: reply.stopReason};
    default: return {unified: 'other', raw: reply.stopReason};
  }
}

function usage(value) {
  const input = Number(value?.input ?? 0n);
  const output = Number(value?.output ?? 0n);
  const cached = Number(value?.cached ?? 0n);
  return {
    inputTokens: {total: input, noCache: input - cached, cacheRead: cached, cacheWrite: undefined},
    outputTokens: {total: output, text: undefined, reasoning: undefined},
    raw: {input, output, cached},
  };
}

class OALanguageModel {
  specificationVersion = 'v3';
  supportsStructuredOutputs = true;
  supportedUrls = {};

  constructor(modelId, config) {
    this.modelId = modelId;
    this.provider = 'openabstractions';
    this.config = config;
  }

  async chat(options) {
    const connector = await runtimeConnector(this.config.connector || undefined);
    const machine = new Machine(this.config.runtimeEndpoint ?? null, {
      connector,
      timeout: this.config.timeout ?? 30000,
      cancellation: options.abortSignal ?? null,
      server: this.config.server ?? null,
    });
    return resolveObservedChat(machine, resolveInference, 'abstraction.inference/chat@1', {
      scope: this.config.scope ?? 'local',
      guarantees: this.config.guarantees ?? [this.config.scope === 'remote' ? RequestGuarantee.HostedAllowed : RequestGuarantee.LocalOnly],
    });
  }

  async chatV2(options) {
    const connector = await runtimeConnector(this.config.connector || undefined);
    const machine = new Machine(this.config.runtimeEndpoint ?? null, {
      connector,
      timeout: this.config.timeout ?? 30000,
      cancellation: options.abortSignal ?? null,
      server: this.config.server ?? null,
    });
    return resolveObservedChat(machine, resolveInferenceV2, 'abstraction.inference/chat@2', {scope: this.config.scope ?? 'local'});
  }

  async finalChat(options) {
    // Negotiate the exact version before any inference request is sent.
    const map = this.config.oaChatGuarantees;
    if (map !== undefined && (!map || typeof map !== 'object' || Array.isArray(map))) {
      throw new TypeError('oaChatGuarantees must map aliases to string lists');
    }
    const required = map?.[routedModelId(this.modelId, this.config)];
    if (required !== undefined && (!Array.isArray(required) || required.some(word => typeof word !== 'string'))) {
      throw new TypeError('oaChatGuarantees must contain string lists');
    }
    const requiresFinal = Array.isArray(required) && required.length > 0;
    if (typeof resolveInferenceV3 !== 'function') {
      if (requiresFinal) throw new Error('OpenAbstractions: this saved preference requires chat@3 guarantees.');
      return null;
    }
    const connector = await runtimeConnector(this.config.connector || undefined);
    const machine = new Machine(this.config.runtimeEndpoint ?? null, {
      connector, timeout: this.config.timeout ?? 30000,
      cancellation: options.abortSignal ?? null, server: this.config.server ?? null,
    });
    try {
      return await resolveObservedChat(machine, resolveInferenceV3, 'abstraction.inference/chat@3', {
        scope: this.config.scope ?? 'local',
      });
    } catch (error) {
      if (!requiresFinal && error?.name === 'ResolutionError' && ['incompatible', 'unavailable'].includes(error.status)) return null;
      throw error;
    }
  }

  chat3Hooks(options) {
    return {
      requestFor, selectionPolicyFor, routedModelId,
      markUp: () => availability.markUp(),
      progress: ({phase, model}) => availability.say('info', phase === 'loading'
        ? `Loading ${model} on this computer` : `Getting ${model} ready`),
      error: (reply, source, request = '') => new OAInferenceError(reply, 'chat', source, request,
        answerReserve(options), localOnlyRequested(options)),
      metadata: (reply, admission, policy, placement) => answeringMetadata(reply, admission, policy, placement),
    };
  }

  async machine(options) {
    const connector = await runtimeConnector(this.config.connector || undefined);
    return new Machine(this.config.runtimeEndpoint ?? null, {
      connector, timeout: 5000, cancellation: options.abortSignal ?? null, server: this.config.server ?? null,
    });
  }

  /** Where a reply ran, from the router's host table (HostState.hosted):
   * 'local' for a server on this computer, 'hosted' for a hosted server or a
   * remote runtime, 'unknown' when the router does not say. A remote
   * runtime's server names start with the runtime's own host name. */
  async placementOf(host, options) {
    if (typeof host !== 'string' || !host) return 'unknown';
    const look = (table) => table?.get(host.split('/')[0]);
    try {
      // A server can move between this computer and a hosted runtime, so the
      // table is read again once it is older than hostTableTTL, and whenever
      // it does not name the server.
      const fresh = typeof this.hostTableAt === 'number' && Date.now() - this.hostTableAt <= hostTableTTL;
      let known = fresh ? look(this.hostTable) : undefined;
      if (known === undefined) {
        this.hostTable = await this.readHostTable(options);
        this.hostTableAt = Date.now();
        known = look(this.hostTable);
      }
      if (known === undefined) return 'unknown';
      return known ? 'hosted' : 'local';
    } catch {
      return 'unknown';
    }
  }

  /** What waiting for the person decided about a refusal that names its
   * pending first-use questions (inference INF-W11, asks ASK-A14): 'allowed'
   * for the one enforcing retry, 'waiting', 'refused' or 'blocked' for the sentence, and '' for a
   * reply that names no question. One toast tells the person what the chat
   * waits for; the native popup asks. */
  async answerFor(reply, options) {
    const permission = reply?.outcome === 'not_permitted' || reply?.outcome === 'forbidden';
    const ids = permission ? pendingQuestions(reply.reason) : [];
    if (!ids.length) return '';
    availability.say('info', askingMessage);
    try {
      return await this.awaitQuestions(ids, options);
    } catch {
      return 'waiting';
    }
  }

  /** Wait up to questionWaitMs in all for the person's answer to each
   * question, through asks permission@1 ObserveQuestion. */
  async awaitQuestions(ids, options) {
    return waitForPermission(ids, {resolvePermission: ({signal}) => resolveNativePermission(this.config, signal),
      signal: options.abortSignal, deadline: Date.now() + (this.config.questionWaitMs ?? questionWaitMs)});
  }

  /** The router's host table: each server's name and whether it is hosted. */
  async readHostTable(options) {
    const {RouterClient} = await import('@openabstractions/router');
    const machine = await this.machine(options);
    const router = new RouterClient(await machine.resolveService('abstraction.router/router@1', {scope: 'local'}));
    const snapshot = await router.hosts(false);
    return new Map((snapshot?.hosts ?? []).map((state) => [state.host, state.hosted === true]));
  }

  async doGenerate(options) {
    const final = await this.finalChat(options);
    if (final) return generateChat3(this, final, options, this.chat3Hooks(options));
    const policyName = selectionPolicyFor(this.modelId, this.config);
    const request = policyName ? requestV2For(policyName, options, this.config)
      : requestFor(this.modelId, options, this.config);
    if (!policyName) rememberPrefix(this, options, request);
    const attempt = async () => {
      let admission = null;
      try {
        if (policyName) {
          const chat = await this.chatV2(options);
          const opened = await openObserved(chat, request, policyName && localOnlyRequested(options) ? [RequestGuarantee.LocalOnly] : undefined, options.abortSignal);
          admission = opened.admission;
          const fold = new Fold();
          for await (const delta of opened.stream) fold.add(delta);
          const reply = fold.reply();
          if (!reply) throw new Error('OpenAbstractions stream ended without a terminal reply');
          return {reply, admission};
        }
        const chat = await this.chat(options);
        const opened = await openObserved(chat, request, undefined, options.abortSignal);
        admission = opened.admission;
        const fold = new Fold();
        for await (const delta of opened.stream) fold.add(delta);
        const reply = fold.reply();
        if (!reply) throw new Error('OpenAbstractions stream ended without a terminal reply');
        return {reply, admission};
      } catch (error) {
        throw ambiguousError(error, 'generate', admission?.operation ?? '', localOnlyRequested(options));
      }
    };
    let {reply, admission} = await attempt();
    availability.markUp();
    const verdict = await this.answerFor(reply, options);
    if (verdict === 'allowed' && admission?.outcome !== 'accepted' && !admission?.operation) ({reply, admission} = await attempt());
    else if (verdict) reply = {...reply, verdict};
    if (reply.outcome !== ReplyOutcome.Completed) {
      throw new OAInferenceError(reply, 'generate', undefined, admission?.operation ?? '', answerReserve(options),
        localOnlyRequested(options));
    }
    // Each part carries where the reply ran, which OpenCode keeps on its text
    // part; the OA TUI module shows it.
    const metadata = answeringMetadata(reply, admission, policyName, await this.placementOf(reply.host, options));
    const content = reply.message.parts.flatMap((part) => {
      if (part.kind === PartKind.Text) return [{type: 'text', text: part.text, providerMetadata: metadata}];
      if (part.kind === PartKind.ToolCall) return [{type: 'tool-call', toolCallId: part.callId,
        toolName: part.name, input: part.arguments, providerMetadata: metadata}];
      return [];
    });
    return {content, finishReason: finishReason(reply), usage: usage(reply.usage), warnings: [],
      providerMetadata: metadata};
  }

  async doStream(options) {
    const final = await this.finalChat(options);
    if (final) return streamChat3(this, final, options, this.chat3Hooks(options));
    const policyName = selectionPolicyFor(this.modelId, this.config);
    const request = policyName ? requestV2For(policyName, options, this.config)
      : requestFor(this.modelId, options, this.config);
    if (!policyName) rememberPrefix(this, options, request);
    // Native admission holds at most briefly for the person (INF-W11). Reader close cancels that
    // same call, including the interval before an operation is accepted.
    const lifetime = new AbortController();
    const callOptions = {...options, abortSignal: options.abortSignal
      ? AbortSignal.any([options.abortSignal, lifetime.signal]) : lifetime.signal};
    const open = async () => {
      try {
        if (policyName) {
          const chat = await this.chatV2(callOptions);
          const opened = await openObserved(chat, request, policyName && localOnlyRequested(options) ? [RequestGuarantee.LocalOnly] : undefined, callOptions.abortSignal);
          return {iterator: opened.stream[Symbol.asyncIterator](), admission: opened.admission, cancelAccepted: opened.cancel};
        }
        const chat = await this.chat(callOptions);
        const opened = await openObserved(chat, request, undefined, callOptions.abortSignal);
        return {iterator: opened.stream[Symbol.asyncIterator](), admission: opened.admission, cancelAccepted: opened.cancel};
      } catch (error) {
        throw ambiguousError(error, 'stream', '', localOnlyRequested(options));
      }
    };
    let {iterator, admission, cancelAccepted} = await open();
    // One wait for the person's answer to a refusal's first-use questions and
    // one retry after an Allow (asks ASK-A14).
    let asked = false;
    availability.markUp();
    const model = this;
    // OpenCode keeps its Working… state after stream-start. Loading and
    // reasoning wait for actual native parts; no timer manufactures text.
    const pending = [{type: 'stream-start', warnings: []}];
    const text = new Set();
    const calls = new Map();
    let latestUsage = null;
    let terminal = false;

    const stream = new ReadableStream({
      async pull(controller) {
        while (pending.length === 0 && !terminal) {
          let next;
          try {
            next = await iterator.next();
          } catch (error) {
            if (terminal) return;
            pending.push({type: 'error', error: ambiguousError(error, 'stream', admission?.operation ?? '')});
            terminal = true;
            break;
          }
          if (terminal) return;
          if (next.done) {
            pending.push({type: 'error', error: ambiguousError(new Error('OpenAbstractions stream ended without a terminal reply'), 'stream', admission?.operation ?? '')});
            terminal = true;
            break;
          }
          let delta = next.value;
          if (delta.kind === DeltaKind.Usage && delta.usage) latestUsage = delta.usage;
          if (delta.kind === DeltaKind.Part && delta.part) {
            const id = String(delta.index);
            if (delta.part.kind === PartKind.Text) {
              if (!text.has(id)) {
                text.add(id);
                pending.push({type: 'text-start', id});
              }
              if (delta.part.text) pending.push({type: 'text-delta', id, delta: delta.part.text});
            } else if (delta.part.kind === PartKind.ToolCall) {
              let call = calls.get(id);
              if (!call) {
                call = {id: delta.part.callId || id, name: delta.part.name, arguments: ''};
                calls.set(id, call);
                pending.push({type: 'tool-input-start', id: call.id, toolName: call.name});
              }
              call.arguments += delta.part.arguments;
              if (delta.part.arguments) pending.push({type: 'tool-input-delta', id: call.id, delta: delta.part.arguments});
            }
          }
          if (delta.kind === DeltaKind.End && delta.end && delta.end.outcome !== ReplyOutcome.Completed
              && !asked && text.size === 0 && calls.size === 0) {
            asked = true;
            const verdict = await model.answerFor(delta.end, callOptions);
            if (terminal) return;
            if (verdict === 'allowed' && admission?.outcome !== 'accepted' && !admission?.operation) {
              try {
                ({iterator, admission, cancelAccepted} = await open());
              } catch (error) {
                pending.push({type: 'error', error});
                terminal = true;
                break;
              }
              continue;
            }
            if (verdict) delta = {...delta, end: {...delta.end, verdict}};
          }
          if (delta.kind === DeltaKind.End && delta.end) {
            // Where the reply ran rides on its text part, which OpenCode
            // keeps; the OA TUI module shows it.
            const placement = delta.end.outcome === ReplyOutcome.Completed
              ? await model.placementOf(delta.end.host, options) : 'unknown';
            const metadata = answeringMetadata(delta.end, admission, policyName, placement);
            for (const id of text) pending.push({type: 'text-end', id, providerMetadata: metadata});
            for (const call of calls.values()) {
              pending.push({type: 'tool-input-end', id: call.id});
              if (delta.end.outcome === ReplyOutcome.Completed) {
                pending.push({type: 'tool-call', toolCallId: call.id, toolName: call.name,
                  input: call.arguments, providerMetadata: metadata});
              }
            }
            if (delta.end.outcome !== ReplyOutcome.Completed) {
              pending.push({type: 'error', error: new OAInferenceError(delta.end, 'stream', undefined, admission?.operation ?? '',
                answerReserve(options), localOnlyRequested(options))});
            }
            pending.push({type: 'finish', finishReason: finishReason(delta.end),
              usage: usage(latestUsage ?? delta.end.usage), providerMetadata: metadata});
            terminal = true;
          }
        }
        if (pending.length) controller.enqueue(pending.shift());
        else if (terminal) controller.close();
      },
      async cancel() {
        terminal = true;
        lifetime.abort();
        const canceling = cancelAccepted?.();
        await iterator.return?.();
        await canceling;
      },
    }, {highWaterMark: 0});
    return {stream, request: {body: request}, response: {headers: {}}};
  }
}

/** OpenCode loads the first create* export from a configured provider package. */
export function createOpenAbstractions(config = {}) {
  return {
    languageModel(modelId) { return new OALanguageModel(modelId, config); },
  };
}
