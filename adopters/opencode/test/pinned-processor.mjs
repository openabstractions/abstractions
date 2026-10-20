// Runs the pinned processor's unchanged process closure and retry policy.
// Session display/event services are inert; Effect owns retries and delays.
import {readFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {stripTypeScriptTypes} from 'node:module';
import {STATUS_CODES} from 'node:http';

const root = new URL('./fixtures/opencode-aec0b9/', import.meta.url);
function javascript(source) {
  return stripTypeScriptTypes(source.replace(/^import .*\n/gm, '').replace(/^export \* as .*\n/gm, '')
    .replace(/^export /gm, ''), {mode: 'transform'});
}
export async function pinnedProcessor(APICallError) {
  const pins = JSON.parse(await readFile(new URL('source.json', root), 'utf8'));
  const sources = {};
  for (const name of ['retry.ts', 'provider-error.ts', 'processor.ts']) {
    const bytes = await readFile(new URL(name, root));
    if (createHash('sha256').update(bytes).digest('hex') !== pins.files[name].sha256) throw new Error('pinned host source changed');
    sources[name] = bytes.toString('utf8');
  }
  const base = new URL(process.env.OA_OPENCODE_EFFECT_MODULE);
  const runtime = JSON.parse(await readFile(new URL('../package.json', base), 'utf8'));
  if (runtime.name !== 'effect' || runtime.version !== '4.0.0-beta.83') throw new Error('pinned processor requires Effect 4.0.0-beta.83');
  const modules = Object.fromEntries(await Promise.all(['Cause', 'Clock', 'Duration', 'Effect', 'Schedule', 'Stream']
    .map(async name => [name, await import(new URL(`${name}.js`, base))])));
  const {Effect, Stream, Cause, Clock, Duration, Schedule} = modules;
  const SessionV1 = {APIError: {isInstance: e => e?.name === 'APIError'}, ContextOverflowError: {isInstance: e => e?.name === 'ContextOverflowError'}};
  const iife = fn => fn(), isRecord = value => value !== null && typeof value === 'object';
  const ProviderError = new Function('APICallError', 'STATUS_CODES', 'iife', 'isContextOverflow',
    `${javascript(sources['provider-error.ts'])}\nreturn {parseAPICallError};`)(APICallError, STATUS_CODES, iife, () => false);
  const SessionRetry = new Function('SessionV1', 'Cause', 'Clock', 'Duration', 'Effect', 'Schedule', 'MessageV2', 'iife', 'isRecord',
    `${javascript(sources['retry.ts'])}\nreturn {policy, retryable, delay};`)(SessionV1, Cause, Clock, Duration, Effect, Schedule, {}, iife, isRecord);
  const full = sources['processor.ts'];
  const begin = full.indexOf('      const process = Effect.fn("SessionProcessor.process")');
  const end = full.indexOf('\n\n      return {', begin);
  if (begin < 0 || end < 0) throw new Error('pinned processor process span missing');
  const processBody = javascript(full.slice(begin, end));
  return {SessionRetry, ProviderError, async run(model, options) {
    const statuses = [], ctx = {sessionID: 'session', assistantMessage: {id: 'assistant'}};
    const parse = error => ({name: 'APIError', data: ProviderError.parseAPICallError({providerID: 'openabstractions', error})});
    const halt = error => Effect.sync(() => { ctx.assistantMessage.error = parse(error); });
    const llm = {stream: () => Stream.fromAsyncIterable((async function* () {
      const opened = await model.doStream(options);
      for await (const part of opened.stream) {
        if (part.type === 'error') throw part.error;
        yield part;
      }
    })(), error => error)};
    const process = new Function('Effect', 'Stream', 'Cause', 'SessionRetry', 'input', 'ctx', 'config', 'status', 'llm', 'parse', 'halt', 'cleanup', 'handleEvent',
      `let aborted = false; ${processBody}\nreturn process;`)(Effect, Stream, Cause, SessionRetry,
      {sessionID: ctx.sessionID, assistantMessage: ctx.assistantMessage, model: {providerID: 'openabstractions'}}, ctx,
      {get: () => Effect.succeed({})}, {set: (_id, state) => Effect.sync(() => statuses.push(state))}, llm, parse, halt,
      () => Effect.void, () => Effect.void);
    const result = await Effect.runPromise(process({}));
    return {result, statuses, error: ctx.assistantMessage.error};
  }};
}
