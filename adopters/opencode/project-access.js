import {randomUUID} from 'node:crypto';
import {isAbsolute, win32} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';

const toolName = 'openabstractions_project_access';
const outcomes = new Set(['pending', 'denied', 'expired', 'conflict', 'approved_pending_close', 'receipt_recorded', 'unavailable']);
const text = (value, limit = 4096) => typeof value === 'string' && value.trim().length > 0 && value.length <= limit && !/[\x00-\x1f]/.test(value);
const absolute = (value) => text(value) && (/^[A-Za-z]:[\\/]/.test(value) || (process.platform !== 'win32' && isAbsolute(value)));

/** Only the native session owner supplies this complete, immutable launch context. */
export function projectSessionEnvironment(env = process.env) {
  const endpoint = env.OA_OPENCODE_SESSION_ENDPOINT;
  const program = env.OA_OPENCODE_SESSION_PROGRAM;
  const account = env.OA_OPENCODE_SESSION_ACCOUNT;
  const id = env.OA_OPENCODE_SESSION_ID;
  const runtimeProgram = env.OA_OPENCODE_RUNTIME_PROGRAM;
  const runtimeAccount = env.OA_OPENCODE_RUNTIME_ACCOUNT;
  if (!text(endpoint) || !absolute(program) || !text(account, 256) || !text(id, 256)
      || !absolute(runtimeProgram) || !text(runtimeAccount, 256)) return null;
  return Object.freeze({endpoint, id,
    server: Object.freeze({principalKind: 1, principal: account, program}),
    runtimeServer: Object.freeze({principalKind: 1, principal: runtimeAccount, program: runtimeProgram})});
}

/** A generated private client on the shared native transport, with mandatory host proof. */
export async function resolveProjectClient(session, signal, dependencies = {}) {
  if (!text(session?.endpoint) || session?.server?.principalKind !== 1
      || !text(session.server.principal, 256) || !absolute(session.server.program)) {
    throw new TypeError('The native OpenCode session host identity is unavailable.');
  }
  const {BunNativeConnector} = dependencies.ipc ?? await import('@openabstractions/ipc/bun');
  const {SessionProjectClient} = dependencies.protocol ?? await import('./session-api/js/opencode/session/index.mjs');
  const connector = new BunNativeConnector();
  return new SessionProjectClient(connector.connect(session.endpoint, {
    timeout: 5000, cancellation: signal ?? null, server: session.server,
  }));
}

function receiptMatches(receipt, expected) {
  return receipt && Object.keys(expected).every((key) => receipt[key] === expected[key])
    && Object.keys(receipt).length === Object.keys(expected).length;
}

function checkedReceipt(receipt, requestKey) {
  if (!receipt || !outcomes.has(receipt.outcome) || receipt.requestKey !== requestKey
      || typeof receipt.receipt !== 'string' || typeof receipt.questionId !== 'string'
      || typeof receipt.message !== 'string' || Object.keys(receipt).some((key) =>
        !['outcome', 'requestKey', 'receipt', 'questionId', 'message'].includes(key))) {
    throw new TypeError('The project access reply is ambiguous; access remains unchanged.');
  }
  if (['approved_pending_close', 'receipt_recorded'].includes(receipt.outcome)
      && (!text(receipt.receipt) || !text(receipt.questionId))) {
    throw new TypeError('The project approval receipt is incomplete; access remains unchanged.');
  }
  return Object.freeze({...receipt});
}

function completedPart(part, request) {
  return part?.type === 'tool' && part.tool === toolName && part.sessionID === request.sessionId
    && part.messageID === request.messageId && text(part.id, 256) && text(part.callID, 256)
    && part.state?.status === 'completed' && part.state.input?.folder === request.folder
    && Object.keys(part.state.input).length === 1
    && Number.isSafeInteger(part.state.time?.end) && part.state.time.end > 0
    && receiptMatches(part.state.metadata?.oaProjectReceipt, request.receipt);
}

function sameCompletion(part, completion) {
  return part.id === completion.partId && part.callID === completion.toolCallId
    && BigInt(part.state.time.end) === completion.completedAt;
}

/** One native session-local request tool and the paired application's committed part event. */
export function createProjectAccess({tool, session, client, resolveClient = resolveProjectClient,
  uuid = randomUUID, wait = (signal) => delay(250, undefined, {signal}), announce = async () => {}}) {
  if (!session) return null;
  const requests = new Map();
  const refusal = async (request) => {
    if (request.told) return;
    request.told = true;
    try {
      await announce('The saved project approval receipt could not be confirmed. Access remains unchanged; no automatic acknowledgment retry will run.');
    } catch { /* a notice cannot confirm native receipt persistence */ }
  };
  const definition = tool({
    description: 'Ask the person through OpenAbstractions for permission to read, run, create, edit and delete files throughout one exact absolute folder and its subfolders. After approval, follow the native owner’s instructions to close this session and reopen the saved conversation. OpenCode permission answers grant no OpenAbstractions access.',
    args: {folder: tool.schema.string()},
    async execute(args, context) {
      if (!args || Object.keys(args).some((name) => name !== 'folder') || !absolute(args.folder)
          || args.folder.startsWith('\\\\') || (win32.isAbsolute(args.folder) && !/^[A-Za-z]:[\\/]/.test(args.folder))
          || !text(context?.sessionID, 256) || !text(context?.messageID, 256) || !context.abort) {
        throw new TypeError('Provide one exact absolute local folder and the current OpenCode conversation context.');
      }
      context.abort.throwIfAborted();
      const request = Object.freeze({requestKey: uuid(), folder: args.folder, sessionId: context.sessionID, messageId: context.messageID});
      const endpoint = await resolveClient(session, context.abort);
      for (;;) {
        context.abort.throwIfAborted();
        const receipt = checkedReceipt(await endpoint.requestProject(request), request.requestKey);
        context.abort.throwIfAborted();
        if (receipt.outcome === 'pending') {
          await wait(context.abort);
          continue;
        }
        if (receipt.outcome === 'approved_pending_close') {
          requests.set(request.requestKey, {...request, receipt});
          return {title: 'Project access awaits close and reopen',
            output: receipt.message, metadata: {oaProjectReceipt: receipt}};
        }
        if (receipt.outcome === 'receipt_recorded') throw new Error('A request reply cannot confirm its completed tool receipt. Access remains unchanged.');
        return {title: 'Project access request', output: receipt.message || 'Project access '+receipt.outcome+'.',
          metadata: {oaProjectOutcome: receipt.outcome, oaProjectRequestKey: request.requestKey}};
      }
    },
  });
  const hooks = {
    event: async ({event} = {}) => {
      if (event?.type !== 'message.part.updated') return;
      const part = event.properties?.part;
      const request = requests.get(part?.state?.metadata?.oaProjectReceipt?.requestKey);
      if (!request || !completedPart(part, request)) return;
      if (request.completion) {
        if (!sameCompletion(part, request.completion)) {
          request.ambiguous = true;
          await refusal(request);
        }
        return;
      }
      // Claim the single attempt before asynchronous work. Duplicate events and
      // lost replies never cause an automatic acknowledgment retry.
      const completion = Object.freeze({requestKey: request.requestKey, sessionId: request.sessionId,
        messageId: request.messageId, partId: part.id, toolCallId: part.callID,
        receipt: request.receipt.receipt, completedAt: BigInt(part.state.time.end)});
      request.completion = completion;
      try {
        const saved = await client.session.message({path: {id: request.sessionId, messageID: request.messageId}, throwOnError: true});
        const info = saved?.data?.info;
        const parts = saved?.data?.parts;
        if (saved?.error || info?.id !== request.messageId || info?.sessionID !== request.sessionId
            || info?.role !== 'assistant' || !Array.isArray(parts)) throw new Error('The saved message is unavailable.');
        const matches = parts.filter((candidate) => candidate?.id === completion.partId
          || candidate?.state?.metadata?.oaProjectReceipt?.requestKey === request.requestKey);
        if (matches.length !== 1 || !completedPart(matches[0], request) || !sameCompletion(matches[0], completion)
            || request.ambiguous) throw new Error('The saved completed tool receipt is ambiguous.');
        const endpoint = await resolveClient(session, AbortSignal.timeout(5000));
        if (request.ambiguous) throw new Error('The completed tool receipt changed.');
        const recorded = checkedReceipt(await endpoint.acknowledgeReceipt(completion), request.requestKey);
        if (recorded.outcome !== 'receipt_recorded' || recorded.receipt !== request.receipt.receipt
            || recorded.questionId !== request.receipt.questionId || request.ambiguous) {
          throw new Error('The native receipt acknowledgment is ambiguous.');
        }
        // This acknowledgment records a saved tool part only. The native owner
        // applies access after explicit user close and a separate fresh launch.
        try { await announce(recorded.message); } catch { /* persistence does not depend on a notice */ }
      } catch {
        await refusal(request);
      }
    },
  };
  return {definition, hooks};
}
