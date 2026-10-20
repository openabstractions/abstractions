import {Machine} from '@openabstractions/facade';
import {APICallError} from '@ai-sdk/provider';
import {
  DeltaKind, Fold, PartKind, ReplyOutcome, RequestGuarantee, Role, StopReason, resolveInference, resolveInferenceV2,
} from '@openabstractions/inference';
import {NativeConnector} from '@openabstractions/ipc';
import {availability, runtimeUnavailable, runtimeUntrusted, unavailableMessage, untrustedMessage} from './availability.js';

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
    const message = !reply && runtimeUntrusted(source) ? untrustedMessage
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
  if (reply?.outcome !== 'refused' || typeof reply.reason !== 'string') return null;
  const match = /^upstream:context_length_exceeded:(\d{1,15}):(\d{1,15})$/.exec(reply.reason);
  if (!match) return null;
  return {window: Number(match[1]), input: Number(match[2]), reserve: Number.isSafeInteger(reserve) && reserve > 0 ? reserve : 0};
}

function tokens(value) {
  return value.toLocaleString('en-US');
}

// The first sentence keeps OpenCode's own overflow wording ("exceeds the
// available context size"), which it compacts on and never retries.
function overflowMessage(reply, {window, input, reserve}) {
  const model = typeof reply.model === 'string' && reply.model ? reply.model : 'the model';
  const sentences = [`OpenAbstractions: this conversation exceeds the available context size of ${model}` +
    (window ? ` (${tokens(window)} tokens).` : '.')];
  if (input) sentences.push(`The request had ${tokens(input)} input tokens.`);
  if (window && reserve && reserve < window) {
    sentences.push(`${tokens(reserve)} tokens of that context are reserved for the answer, leaving ${tokens(window - reserve)} for input.`);
  } else if (window && reserve) {
    sentences.push(`The ${tokens(reserve)}-token answer reserve fills the whole ${tokens(window)}-token context; lower this model's output limit.`);
  }
  sentences.push('Compact or start a new session, remove instructions or tools, or choose a model with a larger context.');
  return sentences.join(' ');
}

function ambiguousError(error, op, operation = '') {
  if (error instanceof OAInferenceError) return error;
  if (runtimeUnavailable(error)) availability.markDown(error);
  return new OAInferenceError(null, op, error, operation);
}

/** The sentence for a request the Local assistant sent: OA serves it only on
 * this computer, and a model that runs only on a hosted server is refused. */
const localOnlyRefusal = 'OpenAbstractions: the Local assistant keeps this conversation on this computer, and no model here '
  + 'serves this choice. Choose a model marked "this computer", or press Tab for the Build agent to use a hosted model.';

function refusalMessage(reply, localOnly = false) {
  if (localOnly && reply?.outcome === 'no_host') return localOnlyRefusal;
  const reason = reply?.outcome === 'not_permitted' && typeof reply.reason === 'string' ? reply.reason : '';
  // Only not_granted leaves a question pending (asks ASK-R2); a denial is the
  // person's own rule, changed on the Panel's Permissions page.
  if (/^rights:not_granted(?:\s|$)/.test(reason)) {
    return 'OpenAbstractions: this program is not yet permitted to do that. '
      + 'A question is waiting in the OpenAbstractions Panel; allow it there and retry.';
  }
  if (/^rights:denied(?:\s|$)/.test(reason)) {
    return 'OpenAbstractions: this program is blocked from doing that. '
      + 'To change that, allow it on the Permissions page of the OpenAbstractions Panel.';
  }
  return 'OpenAbstractions could not complete this model call. Check the OpenAbstractions Panel for the request outcome.';
}

/** How long the first call refused for a pending first-use question waits
 * for the person's answer, and how often it reads its own decision. */
const firstUseWaitMs = 60000;
const firstUsePollMs = 1000;

/** Whether a refusal leaves a first-use question pending: only a
 * not_granted refusal does (asks ASK-R2); a denied or unknown one has
 * nothing to wait for. */
function awaitsQuestion(reply) {
  return reply?.outcome === 'not_permitted' && typeof reply.reason === 'string'
    && /^rights:not_granted(?:\s|$)/.test(reply.reason);
}

const completeAction = 'abstraction.inference/complete';
// How long the router's host table answers where a reply ran before it is read again.
const hostTableTTL = 30000;

function pause(ms, signal) {
  return new Promise((resolve) => {
    const done = () => {
      clearTimeout(timer);
      signal?.removeEventListener?.('abort', done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal?.addEventListener?.('abort', done, {once: true});
  });
}

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
        if (part.type === 'text' || part.type === 'reasoning') {
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

function selectionPolicyFor(modelId, config) {
  const selections = config.oaSelectionPolicies;
  if (!selections || typeof selections !== 'object' || Array.isArray(selections) ||
      !Object.hasOwn(selections, modelId)) return '';
  const name = selections[modelId];
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
      guarantees: localOnlyRequested(options) ? [RequestGuarantee.LocalOnly] : [],
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
    let connector = this.config.connector;
    if (!connector) {
      if (typeof globalThis.Bun !== 'undefined') {
        const {BunNativeConnector} = await import('@openabstractions/ipc/bun');
        connector = new BunNativeConnector();
      } else {
        connector = new NativeConnector();
      }
    }
    const machine = new Machine(this.config.runtimeEndpoint ?? null, {
      connector,
      timeout: this.config.timeout ?? 30000,
      cancellation: options.abortSignal ?? null,
      server: this.config.server ?? null,
    });
    return resolveInference(machine, {
      scope: this.config.scope ?? 'local',
      guarantees: this.config.guarantees ?? [this.config.scope === 'remote' ? RequestGuarantee.HostedAllowed : RequestGuarantee.LocalOnly],
    });
  }

  async chatV2(options) {
    let connector = this.config.connector;
    if (!connector) {
      if (typeof globalThis.Bun !== 'undefined') {
        const {BunNativeConnector} = await import('@openabstractions/ipc/bun');
        connector = new BunNativeConnector();
      } else {
        connector = new NativeConnector();
      }
    }
    const machine = new Machine(this.config.runtimeEndpoint ?? null, {
      connector,
      timeout: this.config.timeout ?? 30000,
      cancellation: options.abortSignal ?? null,
      server: this.config.server ?? null,
    });
    return resolveInferenceV2(machine, {scope: this.config.scope ?? 'local'});
  }

  async machine(options) {
    let connector = this.config.connector;
    if (!connector) {
      if (typeof globalThis.Bun !== 'undefined') {
        const {BunNativeConnector} = await import('@openabstractions/ipc/bun');
        connector = new BunNativeConnector();
      } else {
        connector = new NativeConnector();
      }
    }
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

  /** The router's host table: each server's name and whether it is hosted. */
  async readHostTable(options) {
    const {RouterClient} = await import('@openabstractions/router');
    const machine = await this.machine(options);
    const router = new RouterClient(await machine.resolveService('abstraction.router/router@1', {scope: 'local'}));
    const snapshot = await router.hosts(false);
    return new Map((snapshot?.hosts ?? []).map((state) => [state.host, state.hosted === true]));
  }

  /** This program's own rights decision point (rights RIGHTS-A1): it decides
   * for the calling program only and answers nothing. */
  async decider(options) {
    const {AuthorizationClient} = await import('@openabstractions/rights');
    const machine = await this.machine(options);
    const client = new AuthorizationClient(await machine.resolveService('abstraction.rights/authorization@1', {scope: 'local'}));
    return (action, resource) => client.decide(action, resource);
  }

  /** The model servers the router offers this model on. A refusal names no
   * host (inference: every refusal's host is empty), so these are the hosts
   * whose complete rule the refused call may be waiting for. */
  async servers(options) {
    const {RouterClient} = await import('@openabstractions/router');
    const machine = await this.machine(options);
    const router = new RouterClient(await machine.resolveService('abstraction.router/router@1', {scope: 'local'}));
    const placement = (this.config.scope ?? 'local') === 'remote' ? 'any' : 'local';
    const snapshot = await router.offer({profile: '', placement, reserve: BigInt(answerReserve(options)), fresh: false});
    if (snapshot?.outcome !== 'offered') {
      const outcome = typeof snapshot?.outcome === 'string' ? snapshot.outcome : 'invalid_result';
      throw Object.assign(new Error(`OpenAbstractions router offer: ${outcome}`), {code: outcome, outcome});
    }
    const name = this.config.oaInventoryManagedModels?.[this.modelId]?.name;
    const servers = new Set();
    for (const offer of snapshot?.offers ?? []) {
      if (offer.family === this.modelId || (offer.names ?? []).includes(this.modelId) || (name && (offer.names ?? []).includes(name))) {
        for (const server of offer.servers ?? []) servers.add(server);
      }
    }
    return [...servers];
  }

  /** After a refusal for a pending first-use question, waits up to
   * firstUseWaitMs for the person to allow it in the Panel, reading only
   * this program's own decisions for complete on each server the router
   * offers the model on; true once one permits. The plugin never sees or
   * answers the question: the runtime admits it as itself (asks ASK-R2),
   * Observe reaches only the caller's own questions (ASK-R1), and answering
   * it here would be self-approval (ASK-R3). */
  async awaitFirstUse(reply, options, cancelled = null) {
    if (!awaitsQuestion(reply)) return false;
    const signals = [options.abortSignal, cancelled].filter(Boolean);
    const signal = signals.length > 1 ? AbortSignal.any(signals) : signals[0];
    const deadline = Date.now() + (this.config.firstUseWaitMs ?? firstUseWaitMs);
    const interval = this.config.firstUsePollMs ?? firstUsePollMs;
    let decide;
    let hosts;
    try {
      hosts = reply.host ? [reply.host] : await this.servers(options);
      if (hosts.length === 0) return false;
      decide = await this.decider(options);
    } catch {
      return false;
    }
    while (Date.now() < deadline && !signal?.aborted) {
      await pause(Math.min(interval, Math.max(0, deadline - Date.now())), signal);
      if (signal?.aborted) return false;
      let waiting = false;
      for (const host of hosts) {
        let decision;
        try {
          decision = await decide(completeAction, `host:${host}`);
        } catch {
          return false;
        }
        if (decision?.outcome === 'permitted') return true;
        if (decision?.outcome === 'not_granted') waiting = true;
      }
      if (!waiting) return false;
    }
    return false;
  }

  async doGenerate(options) {
    const policyName = selectionPolicyFor(this.modelId, this.config);
    const request = policyName ? requestV2For(policyName, options, this.config)
      : requestFor(this.modelId, options, this.config);
    if (!policyName) rememberPrefix(this, options, request);
    const attempt = async () => {
      let admission = null;
      try {
        if (policyName) {
          const chat = await this.chatV2(options);
          const opened = await chat.startStream(request);
          admission = opened.admission;
          const fold = new Fold();
          for await (const delta of opened.stream) fold.add(delta);
          const reply = fold.reply();
          if (!reply) throw new Error('OpenAbstractions stream ended without a terminal reply');
          return {reply, admission};
        }
        const chat = await this.chat(options);
        return {reply: await chat.complete(request), admission};
      } catch (error) {
        throw ambiguousError(error, 'generate', admission?.operation ?? '');
      }
    };
    let {reply, admission} = await attempt();
    availability.markUp();
    if (reply.outcome !== ReplyOutcome.Completed && await this.awaitFirstUse(reply, options)) {
      ({reply, admission} = await attempt());
    }
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
    const policyName = selectionPolicyFor(this.modelId, this.config);
    const request = policyName ? requestV2For(policyName, options, this.config)
      : requestFor(this.modelId, options, this.config);
    if (!policyName) rememberPrefix(this, options, request);
    const open = async () => {
      try {
        if (policyName) {
          const chat = await this.chatV2(options);
          const opened = await chat.startStream(request);
          return {iterator: opened.stream[Symbol.asyncIterator](), admission: opened.admission, cancelAccepted: opened.cancel};
        }
        const chat = await this.chat(options);
        return {iterator: chat.stream(request)[Symbol.asyncIterator](), admission: null, cancelAccepted: null};
      } catch (error) {
        throw ambiguousError(error, 'stream');
      }
    };
    let {iterator, admission, cancelAccepted} = await open();
    availability.markUp();
    // A refusal for a pending first-use question ends a stream before any
    // part; the stream then waits for the person's answer and opens once
    // more. Cancelling the stream ends the wait.
    const waiting = new AbortController();
    let waited = false;
    const model = this;
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
            pending.push({type: 'error', error: ambiguousError(error, 'stream', admission?.operation ?? '')});
            terminal = true;
            break;
          }
          if (next.done) {
            pending.push({type: 'error', error: ambiguousError(new Error('OpenAbstractions stream ended without a terminal reply'), 'stream', admission?.operation ?? '')});
            terminal = true;
            break;
          }
          const delta = next.value;
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
          if (delta.kind === DeltaKind.End && delta.end && !waited && text.size === 0 && calls.size === 0
              && delta.end.outcome !== ReplyOutcome.Completed && awaitsQuestion(delta.end)) {
            waited = true;
            if (await model.awaitFirstUse(delta.end, options, waiting.signal)) {
              await iterator.return?.();
              try {
                ({iterator, admission, cancelAccepted} = await open());
              } catch (error) {
                pending.push({type: 'error', error});
                terminal = true;
                break;
              }
              continue;
            }
            if (waiting.signal.aborted) return;
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
        waiting.abort();
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
