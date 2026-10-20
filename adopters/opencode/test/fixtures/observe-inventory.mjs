// A router@1 ObserveOffer stand-in over a test's own offer reader: an empty
// cursor answers at once; otherwise the answer waits until renew() (a router
// survey) finds different offers, or the wait ends. Each answer's
// observation.cacheAgeMs is the time since the last survey. A reader that
// throws answers that error. Cursors are the offers' JSON, as a digest stands
// in.
export function observable(read) {
  const waiters = new Set();
  let surveyAt = Date.now();
  const cursorOf = (offers) => JSON.stringify(offers, (_key, value) => typeof value === 'bigint' ? `${value}n` : value);
  const answer = (offers, cursor) => ({outcome: 'offered', offers, cursor,
    observation: {caller: {}, tookMs: 0n, cacheAgeMs: BigInt(Math.max(0, Date.now() - surveyAt))}});
  async function observe(options, placement, cursor, waitMs, signal) {
    for (;;) {
      const offers = (await read(options, placement))?.offers ?? [];
      const current = cursorOf(offers);
      if (!cursor || current !== cursor) return answer(offers, current);
      const renewed = await new Promise((resolve) => {
        const finish = (value) => { clearTimeout(timer); waiters.delete(wake); signal?.removeEventListener('abort', aborted); resolve(value); };
        const wake = () => finish(true);
        const aborted = () => finish(false);
        const timer = setTimeout(() => finish(false), waitMs);
        waiters.add(wake);
        signal?.addEventListener('abort', aborted, {once: true});
      });
      if (signal?.aborted) throw Object.assign(new Error('observation cancelled'), {name: 'AbortError'});
      if (!renewed) return answer(offers, current);
    }
  }
  return {
    observe,
    /** A router survey: every waiting observer reads again. */
    renew() {
      surveyAt = Date.now();
      for (const wake of [...waiters]) wake();
    },
  };
}

/** Resolves once condition() holds, polling briefly; fails after timeoutMs. */
export async function until(condition, timeoutMs = 3000, message = 'condition not met') {
  const start = Date.now();
  while (!condition()) {
    if (Date.now() - start > timeoutMs) throw new Error(message);
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}

/** Short watch timing for tests: waits, retries and survey spacing in ms. */
export const fastWatch = Object.freeze({waitMs: 200, retryAfterMs: 20, sameSurveyMs: 5});
