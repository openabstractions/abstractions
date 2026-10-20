/**
 * The one owner that notices OA's model inventory changing while OpenCode
 * runs. Each OA heading the plugin refreshed is recorded with the router
 * placements it read and the signature of their offers. For each placement
 * the watch holds one router@1 ObserveOffer long poll under the right Offer
 * uses (router CONTRACT.md ROUTE-A2, ROUTE-D11): it answers at once for an
 * empty cursor, then when the router's offers differ from the cursor or its
 * wait ends. The runtime renews what a wait reads on its own one-minute survey
 * clock and at each server or paired-runtime declaration, and asks a paired
 * runtime once per survey however many callers wait. A paired runtime that
 * sleeps drops out of one survey's answer, so what disappears counts once a
 * later survey (`Observation.cacheAgeMs`) still shows it gone; models that
 * only appear, and a runtime that answers again, count at once. A failed read
 * counts once the next read fails the same way. A counted change calls
 * `onChange` with the heading. The host decides how its model list reloads
 * (plugin.js for OpenCode 1.18, v2.js for V2); the heading's recorded state
 * changes only when that reload refreshes it. A failed answer is read again
 * after `retryMs`. A runtime older than ObserveOffer stops the watch
 * (`onOlder`).
 */
import {runtimeUnavailable} from './availability.js';

/** The longest wait of one ObserveOffer call (router.thrift, 0..30000). */
export const observeWaitMs = 30000;
/** How long the watch waits before reading again after a failed answer. */
export const retryMs = 15000;
/** Two answers whose surveys lie closer than this read the same survey. */
export const surveyGapMs = 1000;
/** What a changed inventory tells the person after OpenCode reloads its list. */
export const inventoryChangedMessage = 'OpenAbstractions models changed; OpenCode\'s model list is up to date.';

const knownSources = new Set(['effective', 'maximum', 'default']);

/** A stable signature of the offers a refresh projected, by placement. It
 * holds each family, its names, its servers in preference order and whether
 * the projection lists it (a known context leaving input). The context number
 * itself stays out: another app reloading a model with a different window
 * changes it, and that alone reloads nothing. */
export function offerSignature(offersByPlacement) {
  const placements = Object.keys(offersByPlacement ?? {}).sort();
  return JSON.stringify(placements.map((placement) => [placement,
    [...(offersByPlacement[placement] ?? [])].map((offer) => [
      String(offer?.family ?? ''), [...(offer?.names ?? [])].map(String).sort(),
      [...(offer?.servers ?? [])].map(String),
      knownSources.has(offer?.contextSource) && Number(offer?.input ?? 0) > 0,
      [...(offer?.unknown ?? [])].map(String).sort(),
    ]).sort((a, b) => a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0)]));
}

/** What a signature lists: each placement's family, names and servers and
 * whether it is listed, one entry each. */
function signatureEntries(signature) {
  const entries = new Set();
  let parsed = [];
  try { parsed = JSON.parse(signature || '[]'); } catch { return entries; }
  for (const [placement, offers] of parsed) {
    for (const [family, names, servers, listed] of offers) {
      for (const name of names) entries.add(JSON.stringify([placement, family, listed, 'name', name]));
      for (const server of servers) entries.add(JSON.stringify([placement, family, listed, 'server', server]));
    }
  }
  return entries;
}

/** Whether next keeps everything previous listed: models only appeared. */
export function onlyAdds(previous, next) {
  const after = signatureEntries(next);
  return [...signatureEntries(previous)].every((entry) => after.has(entry));
}

/** The state word a failed read records: the runtime is away, or it answered
 * with a failure. */
export function failureState(error) {
  return runtimeUnavailable(error) ? 'unavailable' : 'failed';
}

function pause(ms, signal) {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve();
    const timer = setTimeout(done, ms);
    timer.unref?.();
    function done() {
      clearTimeout(timer);
      signal.removeEventListener('abort', done);
      resolve();
    }
    signal.addEventListener('abort', done, {once: true});
  });
}

/**
 * observe(options, placement, cursor, waitMs, signal) answers the router's
 * ObserveOffer as {offers, cursor, observation}, or throws. Each heading in
 * `headings` (a Map the plugin keeps) is {options, placements, signature,
 * state}: state is 'answered' after a refresh, or the failure word of the
 * last refresh.
 */
export function createInventoryWatch({headings, observe, onChange, onRead = () => {}, onError = () => {},
  onOlder = () => {}, onSettle = () => {}, isOlder = () => false, waitMs = observeWaitMs, retryAfterMs = retryMs,
  sameSurveyMs = surveyGapMs, now = () => Date.now(), delay = pause} = {}) {
  if (!(headings instanceof Map) || typeof observe !== 'function' || typeof onChange !== 'function') {
    throw new TypeError('An inventory watch needs its headings, an observer and a change handler.');
  }
  const abort = new AbortController();
  let started = false;
  // Each heading's latest answer by placement, and a change waiting for a
  // later survey or read to count: {key, surveyAt}.
  const latest = new Map();
  const seen = new Map();
  // Change handling runs one at a time, at most once per heading at a time,
  // and once per change a handler acted on.
  let acting = Promise.resolve();
  const handling = new Set();
  const acted = new Map();

  function evaluate(id) {
    const heading = headings.get(id);
    const answers = latest.get(id);
    if (!heading || !answers || heading.placements.some((placement) => !answers.has(placement))) return;
    const failure = heading.placements.map((placement) => answers.get(placement).state).find((state) => state !== 'answered');
    const state = failure ?? 'answered';
    const observed = state === 'answered'
      ? offerSignature(Object.fromEntries(heading.placements.map((placement) => [placement, answers.get(placement).offers])))
      : '';
    const changed = state !== heading.state || (state === 'answered' && observed !== heading.signature);
    const key = JSON.stringify([state, observed]);
    if (!changed) {
      seen.delete(id);
      acted.delete(id);
      try { onSettle(id); } catch { /* a settle note must not stop the watch */ }
      return;
    }
    if (acted.get(id) === key) return;
    const surveyAt = Math.max(...heading.placements.map((placement) => answers.get(placement).surveyAt ?? 0));
    const immediate = state === 'answered' && (heading.state !== 'answered' || onlyAdds(heading.signature, observed));
    if (!immediate) {
      const pending = seen.get(id);
      // A failure counts at its second read; a removal at a later survey.
      const counted = pending?.key === key && (state !== 'answered' || surveyAt > pending.surveyAt + sameSurveyMs);
      if (!counted) {
        if (pending?.key !== key) seen.set(id, {key, surveyAt});
        return;
      }
    }
    if (handling.has(id)) return;
    handling.add(id);
    acting = acting.then(async () => {
      try {
        if (abort.signal.aborted) return;
        // A handler that answers false did not act and is asked again at the
        // next answer; any other answer handles this change once.
        if (await onChange({id, heading: headings.get(id) ?? heading, state, observed: key}) !== false) acted.set(id, key);
      } catch (error) {
        try { onError(error); } catch { /* reporting must not stop the watch */ }
      } finally {
        handling.delete(id);
      }
    });
    return acting;
  }

  async function follow(id, placement) {
    let cursor = '';
    while (!abort.signal.aborted) {
      const heading = headings.get(id);
      if (!heading) return;
      if (!latest.has(id)) latest.set(id, new Map());
      let failed = false;
      try {
        const answer = await observe(heading.options, placement, cursor, cursor === '' ? 0 : waitMs, abort.signal);
        if (abort.signal.aborted) return;
        cursor = typeof answer?.cursor === 'string' ? answer.cursor : '';
        const age = Number(answer?.observation?.cacheAgeMs ?? 0);
        latest.get(id).set(placement, {state: 'answered', offers: answer?.offers ?? [],
          surveyAt: now() - (Number.isFinite(age) ? age : 0)});
        onRead(null);
      } catch (error) {
        if (abort.signal.aborted) return;
        if (isOlder(error)) {
          stop();
          onOlder(error);
          return;
        }
        cursor = '';
        failed = true;
        latest.get(id).set(placement, {state: failureState(error), surveyAt: now()});
        onRead(error);
      }
      await evaluate(id);
      if (failed) await delay(retryAfterMs, abort.signal);
    }
  }

  function stop() { abort.abort(); }

  return {
    /** Follows every recorded heading's placements. */
    start() {
      if (started || abort.signal.aborted) return;
      started = true;
      for (const [id, heading] of headings) {
        for (const placement of heading.placements) void follow(id, placement);
      }
    },
    stop,
    /** Resolves once every change handler queued so far has run. */
    settled() { return acting; },
    get stopped() { return abort.signal.aborted; },
  };
}
