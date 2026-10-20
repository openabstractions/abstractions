import {FoldV3, newRequestV3, newChatOptions, newChatTool} from '@openabstractions/inference';
import {encodeRequestV3, decodeRequestV3} from '@openabstractions/inference/protocol';
import {randomUUID} from 'node:crypto';

export const uncertainChat3Message = 'The last request may still have run; send it again if needed.';
const retainedInvocations = new WeakMap();

function chatError(hooks, reply, source, request = '', {uncertain = false, preaccept = false} = {}) {
  const error = hooks.error(reply, source, request);
  // The pinned host also retries matching messages/status/body when the SDK
  // flag is false. Keep transport diagnostics on cause, outside those fields.
  if (uncertain) {
    error.message = uncertainChat3Message;
    error.statusCode = undefined;
    error.responseBody = undefined;
    error.responseHeaders = undefined;
  }
  error.isRetryable = preaccept && ['unavailable', 'exhausted'].includes(reply?.outcome);
  if (error.isRetryable && Number.isSafeInteger(Number(reply.retryAfterMs)) && Number(reply.retryAfterMs) > 0) {
    error.responseHeaders = {'retry-after-ms': String(reply.retryAfterMs)};
  }
  return error;
}

// While OA starts the model host or loads the model, a Start reads a definite
// refusal: no host took the request. The plugin waits and starts the same
// request again, with backoff, up to limitMs, so the first chat after a cold
// start gets its answer (R28: the 32 s Qwen3.6 load). Tests replace now and
// sleep with a virtual clock.
export const activation = {
  limitMs: 120000,
  firstMs: 1000,
  maxMs: 8000,
  now: () => Date.now(),
  sleep: (ms, signal) => new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(signal.reason); return; }
    const done = () => { signal?.removeEventListener('abort', aborted); resolve(); };
    const timer = setTimeout(done, ms);
    const aborted = () => { clearTimeout(timer); reject(signal.reason); };
    signal?.addEventListener('abort', aborted, {once: true});
  }),
};

function activating(reply) {
  return reply?.outcome === 'unavailable' && /^(activating:|engine:loading$|native:engine:loading$)/.test(reply.reason ?? '');
}

// awaitActivation starts the request again until the host admits it, refuses
// it for another reason, or limitMs passes. It tells the person once.
async function awaitActivation(admission, start, hooks, model, options) {
  const until = activation.now() + activation.limitMs;
  let delay = activation.firstMs, told = false;
  while (activating(admission)) {
    const hinted = Number(admission.retryAfterMs ?? 0);
    const wait = Math.min(Math.max(delay, Number.isFinite(hinted) ? hinted : 0), until - activation.now());
    if (wait <= 0) break;
    if (!told) {
      told = true;
      try {
        hooks.progress?.({phase: 'loading', model: admission.model || model.modelId})?.catch?.(() => {});
      } catch { /* A display hook must not change the request. */ }
    }
    await activation.sleep(wait, options.abortSignal);
    delay = Math.min(delay * 2, activation.maxMs);
    admission = await start();
  }
  return {admission, told};
}

function numeric(value) { return value == null ? undefined : Number(value); }
export function usageChat3(value = {}) {
  const input = numeric(value.input), output = numeric(value.output);
  const read = numeric(value.cacheRead), write = numeric(value.cacheWrite);
  return {
    inputTokens: {total: input, noCache: input !== undefined && read !== undefined && write !== undefined
      ? input - read - write : undefined, cacheRead: read, cacheWrite: write},
    outputTokens: {total: output, text: undefined, reasoning: numeric(value.reasoning)},
    raw: Object.fromEntries(Object.entries(value).filter(([, count]) => count != null).map(([name, count]) => [name, Number(count)])),
  };
}

export function finishChat3(result) {
  if (result.outcome !== 'completed') return {unified: 'error', raw: result.outcome};
  const raw = result.finishReason;
  return {unified: ({stop: 'stop', length: 'length', tool_calls: 'tool-calls', content_filter: 'content-filter',
    model_refusal: 'content-filter'})[raw] ?? 'other', raw};
}

function projected(result, admission) {
  return {...result, reason: result.cause ?? result.reason ?? '', host: result.servedBy?.server ?? admission.server ?? '',
    model: admission.model ?? ''};
}

function acceptedError(hooks, result, admission) {
  const uncertain = result.cause === 'remote:uncertain';
  const error = chatError(hooks, projected(result, admission), undefined, admission.request,
    {uncertain});
  // Accepted work never enters the host's fresh-invocation retry path.
  if (!uncertain && !error.context) error.message = result.cause === 'upstream:reasoning_only'
    ? 'OpenAbstractions: the model spent its whole output limit thinking and wrote no answer.'
    : 'OpenAbstractions: this request ended without an answer.';
  return error;
}

function sealMetadata(part) {
  if (!part.seal) return {};
  return {reasoningSeal: {block: String(part.block), origin: part.seal.origin,
    bytes: Buffer.from(part.seal.bytes).toString('base64')}};
}

function metadata(hooks, result, admission, policy, part) {
  const served = result.servedBy;
  const base = hooks.metadata(projected(result, admission), {...admission, operation: admission.request}, policy,
    served?.place ?? 'unknown');
  const timing = result.timing;
  const chat3Terminal = result.outcome === 'completed' && served?.place === 'local' && served.server &&
    ['cold', 'warm', 'loaded'].includes(timing?.load)
    ? {load: timing.load, ...(timing.loadMs == null ? {} : {loadMs: String(timing.loadMs)}),
      servedBy: {place: served.place, server: served.server}}
    : undefined;
  return {...base, openabstractions: {...base.openabstractions, ...sealMetadata(part ?? {}),
    ...(chat3Terminal ? {chat3Terminal} : {}),
    ...(part?.kind === 'model_refusal' ? {modelRefusal: true} : {})}};
}

// OpenCode's reasoning words: a model variant's options.reasoningEffort
// reaches the provider under its key in providerOptions. OpenAI's minimal
// reads as low and DeepSeek's max and OpenAI's xhigh as high; a word chat@3
// has no member for leaves the model's own default.
const reasoningWords = {none: 'none', minimal: 'low', low: 'low', medium: 'medium', high: 'high', xhigh: 'high', max: 'high'};

/** The reasoning a request asks for, as chat@3 ChatOptions.reasoning: the
 * reasoningEffort (or reasoning_effort) provider option, with an optional
 * reasoningBudget token cap; null when it names none (architect ruling
 * 2026-11-12, reasoning effort). */
export function reasoningOf(options) {
  for (const value of Object.values(options?.providerOptions ?? {})) {
    const word = reasoningWords[value?.reasoningEffort ?? value?.reasoning_effort];
    if (!word) continue;
    const budget = value?.reasoningBudget ?? value?.reasoning_budget;
    return {effort: word, budget: Number.isSafeInteger(budget) && budget > 0 && budget <= 1048576 ? BigInt(budget) : undefined};
  }
  return null;
}

// Public display reasoning is excluded by the existing requestFor owner. Only
// the opaque provider metadata returns through the separate replay field.
export async function requestChat3(model, chat, options, hooks) {
  const policy = hooks.selectionPolicyFor(model.modelId, model.config);
  const old = hooks.requestFor(policy ? '' : model.modelId, options,
    policy ? {...model.config, requestGuarantees: []} : model.config);
  const routed = hooks.routedModelId ? hooks.routedModelId(model.modelId, model.config) : model.modelId;
  const required = model.config.oaChatGuarantees?.[routed] ?? [];
  if (!Array.isArray(required) || required.some(value => typeof value !== 'string')) throw new TypeError('oaChatGuarantees must contain string lists');
  const conversation = policy === 'title' ? '' : Object.values(options.providerOptions ?? {})
    .find(value => typeof value?.oaSessionID === 'string')?.oaSessionID ?? '';
  const window = await chat.getHistoryWindow();
  if (window.outcome !== 'available' || !window.window) throw chatError(hooks, window, undefined, '', {preaccept: true});
  const identity = {key: randomUUID(), historyEpoch: window.window.historyEpoch, attempt: 0n};
  const messages = old.messages.map(message => ({...message, reasoning: []}));
  let at = 0;
  for (const message of options.prompt ?? []) {
    const visible = message.role === 'system' || (message.content ?? []).some(part => ['text', 'tool-call', 'tool-result'].includes(part.type));
    if (!visible) continue;
    const target = messages[at++];
    if (!target || message.role !== 'assistant') continue;
    for (const part of message.content ?? []) {
      const seal = part.providerOptions?.openabstractions?.reasoningSeal;
      if (part.type !== 'reasoning' || !seal) continue;
      const bytes = Buffer.from(seal.bytes, 'base64');
      if (bytes.toString('base64') !== seal.bytes) throw new TypeError('invalid opaque reasoning seal');
      target.reasoning.push({block: BigInt(seal.block), seal: {origin: seal.origin, bytes: new Uint8Array(bytes)}});
    }
  }
  const control = options.toolChoice;
  const toolChoice = control ? {kind: control.type === 'tool' ? 'named' : control.type,
    name: control.type === 'tool' ? control.toolName : ''} : null;
  const functions = (options.tools ?? []).filter(tool => tool.type === 'function');
  const tools = old.tools.map((tool, index) => ({...newChatTool(), ...tool, strict: functions[index]?.strict}));
  return {policy, request: {...newRequestV3(), identity, conversation, ...(policy === 'title' ? {use: 'title'} : {}), target: {model: policy ? '' : model.modelId, selectionPolicy: policy},
    content: {messages, tools, options: {...newChatOptions(), ...old.options,
      toolChoice, parallelTools: options.providerOptions?.openabstractions?.parallelTools, reasoning: reasoningOf(options)},
      extensions: old.extensions, requiredExtensions: old.requiredExtensions,
      guarantees: [...new Set([...old.guarantees, ...required])], credential: policy ? '' : old.credential}}};
}

async function openChat3(model, chat, options, hooks, invocation) {
  const {request: prepared, policy} = invocation ?? await requestChat3(model, chat, options, hooks);
  // The exact final request stays in this invocation's closure. Replay decodes
  // those same bytes, even if caller options or an error identity are edited.
  const bytes = invocation?.bytes ?? encodeRequestV3(prepared);
  const request = decodeRequestV3(bytes);
  const retained = {request, policy, bytes};
  const uncertain = (source, id = '', reply) => {
    const error = Object.assign(chatError(hooks, reply, source, id, {uncertain: true}), {requestIdentity: {...request.identity}});
    retainedInvocations.set(error, retained);
    return error;
  };
  const start = async () => {
    try { return await chat.start(decodeRequestV3(bytes)); }
    catch (source) { throw uncertain(source); }
  };
  let admission = await start();
  hooks.markUp?.();
  let told = false;
  if (activating(admission)) ({admission, told} = await awaitActivation(admission, start, hooks, model, options));
  // JOB-A16 permits a fresh enforcing call after the person's answer only for
  // a preaccept permission refusal. The same identity/body is retained.
  if (admission.outcome === 'unknown') throw uncertain(undefined, admission.request, projected(admission, admission));
  if (admission.outcome !== 'accepted') {
    const verdict = await model.answerFor(projected(admission, admission), options);
    if (verdict === 'allowed') admission = await start();
    else if (verdict) admission = {...admission, verdict};
  }
  if (admission.outcome === 'unknown') throw uncertain(undefined, admission.request, projected(admission, admission));
  if (admission.outcome !== 'accepted') throw chatError(hooks, projected(admission, admission), undefined, '', {preaccept: true});
  let ended = false, cursor = 0n, cancelPromise;
  const cancel = () => {
    options.abortSignal?.removeEventListener('abort', abort);
    return ended ? Promise.resolve() : cancelPromise ??= chat.cancel(admission.request);
  };
  const abort = () => { void cancel().catch(() => undefined); };
  options.abortSignal?.addEventListener('abort', abort, {once: true});
  if (options.abortSignal?.aborted) abort();
  const fold = new FoldV3({retainReasoning: true});
  let firstPart = false, progressPhase = told ? 'loading' : '';
  const events = (async function* () {
    try {
      for (;;) {
        let page;
        try { page = await chat.observe(admission.request, cursor, 256n, 65536n, 25000n); }
        catch (source) { throw Object.assign(uncertain(source, admission.request), {cursor}); }
        if (page.outcome !== 'page') throw Object.assign(uncertain(
          new Error(`OpenAbstractions observe ${page.outcome}`), admission.request), {cursor});
        for (const event of page.events) {
          fold.add(event);
          cursor = event.sequence + 1n;
          if (event.part) firstPart = true;
          const phase = event.progress?.phase;
          if (!firstPart && ['loading', 'warming'].includes(phase) && phase !== progressPhase) {
            progressPhase = phase;
            try {
              hooks.progress?.({phase, model: admission.model || model.modelId})?.catch?.(() => {});
            } catch { /* A display hook must not change accepted work. */ }
          }
          if (event.kind === 'end') ended = true;
          yield event;
        }
        if (page.next !== cursor) throw hooks.error(undefined, new Error('OpenAbstractions cursor mismatch'), admission.request);
        if (page.atEnd) {
          if (!ended) throw hooks.error(undefined, new Error('OpenAbstractions stream ended without its actual terminal'), admission.request);
          return;
        }
      }
    } catch (source) {
      if (retainedInvocations.has(source)) throw source;
      throw Object.assign(uncertain(source, admission.request), {cursor});
    } finally { options.abortSignal?.removeEventListener('abort', abort); }
  })();
  return {request, admission, policy, fold, events, cancel};
}

export async function generateChat3(model, chat, options, hooks, invocation) {
  let opened;
  try { opened = await openChat3(model, chat, options, hooks, invocation); }
  catch (error) {
    if (retainedInvocations.has(error)) error.reconcile = () => generateChat3(model, chat, options, hooks, retainedInvocations.get(error));
    throw error;
  }
  for await (const _ of opened.events) { /* the shared fold owns correlation */ }
  const reply = opened.fold.reply();
  if (!reply) throw hooks.error(undefined, new Error('OpenAbstractions stream ended without its actual terminal'), opened.admission.request);
  if (reply.result.outcome !== 'completed') throw acceptedError(hooks, reply.result, opened.admission);
  const info = metadata(hooks, reply.result, opened.admission, opened.policy);
  const content = reply.blocks.flatMap(part => {
    const providerMetadata = metadata(hooks, reply.result, opened.admission, opened.policy, part);
    if (part.kind === 'text' || part.kind === 'model_refusal') return [{type: 'text', text: part.text, providerMetadata}];
    if (part.kind === 'reasoning' || part.kind === 'reasoning_summary') return [{type: 'reasoning', text: part.text, providerMetadata}];
    if (part.kind === 'tool_call' && reply.result.finishReason === 'tool_calls') return [{type: 'tool-call', toolCallId: part.callId, toolName: part.name, input: part.arguments, providerMetadata}];
    return [];
  });
  return {content, finishReason: finishChat3(reply.result), usage: usageChat3(reply.result.usage), warnings: [], providerMetadata: info};
}

export async function streamChat3(model, chat, options, hooks, invocation) {
  let opened;
  try { opened = await openChat3(model, chat, options, hooks, invocation); }
  catch (error) {
    if (retainedInvocations.has(error)) error.reconcile = () => streamChat3(model, chat, options, hooks, retainedInvocations.get(error));
    throw error;
  }
  const iterator = opened.events[Symbol.asyncIterator]();
  const pending = [{type: 'stream-start', warnings: []}];
  const blocks = new Map();
  let terminal = false;
  const closeBlock = (block, result = {}) => {
    if (!block || block.closed) return;
    if (!result.outcome && (block.part.kind === 'text' || block.part.kind === 'model_refusal')) return;
    block.closed = true;
    const providerMetadata = metadata(hooks, result, opened.admission, opened.policy, block.part);
    if (block.part.kind === 'tool_call') pending.push({type: 'tool-input-end', id: block.callId});
    else pending.push({type: block.part.kind === 'reasoning' || block.part.kind === 'reasoning_summary' ? 'reasoning-end' : 'text-end', id: block.id, providerMetadata});
  };
  const stream = new ReadableStream({
    async pull(controller) {
      while (pending.length === 0 && !terminal) {
        let next;
        try { next = await iterator.next(); }
        catch (error) { pending.push({type: 'error', error}); terminal = true; break; }
        if (next.done) { pending.push({type: 'error', error: hooks.error(undefined, new Error('OpenAbstractions stream ended without its actual terminal'), opened.admission.request)}); terminal = true; break; }
        const event = next.value;
        if (event.progress && options.includeRawChunks) pending.push({type: 'raw', rawValue: {type: 'openabstractions.progress', ...event.progress}});
        if (event.part) {
          const part = event.part, id = String(part.block);
          let block = blocks.get(id);
          if (!block) {
            block = {id, part: {...part}, callId: part.callId || id, arguments: '', closed: false};
            blocks.set(id, block);
            if (part.kind === 'tool_call') pending.push({type: 'tool-input-start', id: block.callId, toolName: part.name});
            else pending.push({type: part.kind === 'reasoning' || part.kind === 'reasoning_summary' ? 'reasoning-start' : 'text-start', id});
          }
          if (part.seal) block.part.seal = part.seal;
          if (part.kind === 'tool_call') {
            block.arguments += part.arguments ?? '';
            if (part.arguments) pending.push({type: 'tool-input-delta', id: block.callId, delta: part.arguments});
          } else if (part.text) pending.push({type: part.kind === 'reasoning' || part.kind === 'reasoning_summary' ? 'reasoning-delta' : 'text-delta', id, delta: part.text});
        }
        if (event.blockEnd) closeBlock(blocks.get(String(event.blockEnd.block)));
        if (event.end) {
          const reply = opened.fold.reply(), result = reply.result;
          for (const block of blocks.values()) closeBlock(block, result);
          if (result.outcome !== 'completed') pending.push({type: 'error', error: acceptedError(hooks, result, opened.admission)});
          if (result.outcome === 'completed' && result.finishReason === 'tool_calls') for (const block of blocks.values()) {
            if (block.part.kind === 'tool_call') pending.push({type: 'tool-call', toolCallId: block.callId,
              toolName: block.part.name, input: block.arguments, providerMetadata: metadata(hooks, result, opened.admission, opened.policy)});
          }
          pending.push({type: 'finish', finishReason: finishChat3(result), usage: usageChat3(result.usage),
            providerMetadata: metadata(hooks, result, opened.admission, opened.policy)});
          terminal = true;
        }
      }
      if (pending.length) controller.enqueue(pending.shift());
      else if (terminal) controller.close();
    },
    async cancel() { terminal = true; const canceling = opened.cancel(); await iterator.return?.(); await canceling; },
  }, {highWaterMark: 0});
  return {stream, request: {body: opened.request}, response: {headers: {}}};
}
