import {Machine} from '@openabstractions/facade';
import {
  DeltaKind, PartKind, ReplyOutcome, RequestGuarantee, Role, StopReason, resolveInference,
} from '@openabstractions/inference';
import {NativeConnector} from '@openabstractions/ipc';

/** A typed OA terminal outcome surfaced through the AI SDK provider boundary. */
export class OAInferenceError extends Error {
  constructor(reply) {
    super(`OpenAbstractions inference ${reply.outcome}${reply.reason ? `: ${reply.reason}` : ''}`);
    this.name = 'OAInferenceError';
    this.outcome = reply.outcome;
    this.reason = reply.reason;
    this.host = reply.host;
    this.model = reply.model;
  }
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

function parsedJSON(value) {
  try { return JSON.parse(value); } catch { return value; }
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
  const guarantees = config.requestGuarantees ?? [config.scope === 'remote' ? RequestGuarantee.HostedAllowed : RequestGuarantee.LocalOnly];
  if (!Array.isArray(guarantees) || guarantees.some((value) => typeof value !== 'string')) {
    throw new TypeError('requestGuarantees must contain strings');
  }
  return {
    model: modelId,
    messages,
    tools,
    options: {
      maxOutput: BigInt(options.maxOutputTokens ?? 4096),
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
      if (globalThis.Bun && process.env.ABSTRACTION_IPC_LIBRARY) {
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

  async doGenerate(options) {
    const chat = await this.chat(options);
    const reply = await chat.complete(requestFor(this.modelId, options, this.config));
    if (reply.outcome !== ReplyOutcome.Completed) throw new OAInferenceError(reply);
    const content = reply.message.parts.flatMap((part) => {
      if (part.kind === PartKind.Text) return [{type: 'text', text: part.text}];
      if (part.kind === PartKind.ToolCall) return [{type: 'tool-call', toolCallId: part.callId,
        toolName: part.name, input: parsedJSON(part.arguments)}];
      return [];
    });
    return {content, finishReason: finishReason(reply), usage: usage(reply.usage), warnings: []};
  }

  async doStream(options) {
    const chat = await this.chat(options);
    const iterator = chat.stream(requestFor(this.modelId, options, this.config))[Symbol.asyncIterator]();
    const pending = [{type: 'stream-start', warnings: []}];
    const text = new Set();
    const calls = new Map();
    let latestUsage = null;
    let terminal = false;

    const stream = new ReadableStream({
      async pull(controller) {
        while (pending.length === 0 && !terminal) {
          const next = await iterator.next();
          if (next.done) {
            pending.push({type: 'error', error: new Error('OpenAbstractions stream ended without a terminal reply')});
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
          if (delta.kind === DeltaKind.End && delta.end) {
            for (const id of text) pending.push({type: 'text-end', id});
            for (const call of calls.values()) {
              pending.push({type: 'tool-input-end', id: call.id});
              pending.push({type: 'tool-call', toolCallId: call.id, toolName: call.name,
                input: parsedJSON(call.arguments)});
            }
            if (delta.end.outcome !== ReplyOutcome.Completed) {
              pending.push({type: 'error', error: new OAInferenceError(delta.end)});
            }
            pending.push({type: 'finish', finishReason: finishReason(delta.end),
              usage: usage(latestUsage ?? delta.end.usage), providerMetadata: {openabstractions: {
                outcome: delta.end.outcome, reason: delta.end.reason, host: delta.end.host, model: delta.end.model,
              }}});
            terminal = true;
          }
        }
        if (pending.length) controller.enqueue(pending.shift());
        else if (terminal) controller.close();
      },
      async cancel() {
        terminal = true;
        await iterator.return?.();
      },
    });
    return {stream, request: {body: requestFor(this.modelId, options, this.config)}, response: {headers: {}}};
  }
}

/** OpenCode loads the first create* export from a configured provider package. */
export function createOpenAbstractions(config = {}) {
  return {
    languageModel(modelId) { return new OALanguageModel(modelId, config); },
  };
}
