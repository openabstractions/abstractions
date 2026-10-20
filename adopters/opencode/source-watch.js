const sourceIdPattern = /^[a-z0-9][a-z0-9._/-]{0,127}$/;
const availability = new Set(['available', 'permission_denied', 'unavailable', 'unsupported']);
const catalogOutcomes = new Set(['catalog', 'forbidden', 'not_permitted', 'unavailable', 'invalid']);
const pageOutcomes = new Set(['page', 'gap', 'unavailable', 'invalid', 'record_too_large', 'corrupt', 'unsupported', 'forbidden', 'not_permitted', 'os_authorization_required', 'os_authorization_pending']);
const maxDurationMs = 60_000;
const maxRecords = 128;
const maxBytes = 65_536;
const pageRecords = 256;
const pageBytes = 65_536;
const pollIntervalMs = 500;
const sharingAction = 'opencode/log-source.share';
const decisionTimeoutMs = 5000;
const maxSafeInteger = BigInt(Number.MAX_SAFE_INTEGER);
const minSafeInteger = BigInt(Number.MIN_SAFE_INTEGER);

function jsonInteger(_key, value) {
  if (typeof value !== 'bigint') return value;
  return value >= minSafeInteger && value <= maxSafeInteger ? Number(value) : value.toString();
}

function validateArgs(args) {
  if (!args || typeof args !== 'object' || Array.isArray(args)) throw new TypeError('Provide source-watch arguments.');
  if (typeof args.sourceId !== 'string' || !sourceIdPattern.test(args.sourceId) || args.sourceId.includes('..')
      || args.sourceId.startsWith('/') || args.sourceId.endsWith('/') || args.sourceId.includes('//')) {
    throw new TypeError('Choose one fixed source ID from the source catalogue.');
  }
  if (!Number.isInteger(args.durationMs) || args.durationMs < 1 || args.durationMs > maxDurationMs) {
    throw new TypeError(`durationMs must be from 1 to ${maxDurationMs}.`);
  }
  if (!Number.isInteger(args.maxRecords) || args.maxRecords < 1 || args.maxRecords > maxRecords) {
    throw new TypeError(`maxRecords must be from 1 to ${maxRecords}.`);
  }
  if (!Number.isInteger(args.maxBytes) || args.maxBytes < 1 || args.maxBytes > maxBytes) {
    throw new TypeError(`maxBytes must be from 1 to ${maxBytes}.`);
  }
  if (args.shareEvents !== undefined && typeof args.shareEvents !== 'boolean') {
    throw new TypeError('shareEvents must be a boolean.');
  }
}

function result(value) {
  return JSON.stringify(value, jsonInteger);
}

function recordBytes(record) {
  return new TextEncoder().encode(JSON.stringify(record, jsonInteger)).byteLength;
}

function validRecord(record, sourceId) {
  return record && record.sourceId === sourceId && record.record
    && (record.record.schema === 1 || record.record.schema === 1n)
    && Array.isArray(record.record.identity) && record.record.identity.length === 0;
}

function sleep(ms, signal) {
  return new Promise((resolve) => {
    if (signal?.aborted) return resolve();
    const timer = setTimeout(done, ms);
    function done() {
      clearTimeout(timer);
      signal?.removeEventListener('abort', done);
      resolve();
    }
    signal?.addEventListener('abort', done, {once: true});
  });
}

function permissionStatus(error) {
  if (['not_permitted', 'forbidden', 'os_authorization_required', 'os_authorization_pending'].includes(error?.code)) return error.code;
  return error?.code === 'permission_denied' ? 'permission_denied' : 'unavailable';
}

function askForApproval(ask, request, signal) {
  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      signal?.removeEventListener('abort', onAbort);
      resolve(value);
    };
    const onAbort = () => finish('cancelled');
    if (signal?.aborted) return finish('cancelled');
    signal?.addEventListener('abort', onAbort, {once: true});
    Promise.resolve().then(() => ask(request)).then(
      () => finish('approved'),
      () => finish(signal?.aborted ? 'cancelled' : 'denied'),
    );
  });
}

function emptySummary(sourceId, status, startedAt, endedAt, pages = 0, records = 0, bytes = 0) {
  return {kind: 'source_watch', sourceId, status, startedAt, endedAt, pages, records, bytes};
}

// Each decision has its own bounded call. The final call uses callerSignal,
// independently of the collection timer. Only the exact permitted outcome
// allows disclosure; a resolver/client ignoring cancellation cannot stall it.
async function sharingDecision(resolveAuthorization, sourceId, parentSignal) {
  if (typeof resolveAuthorization !== 'function') return 'sharing_unavailable';
  const controller = new AbortController();
  const onAbort = () => controller.abort();
  parentSignal?.addEventListener('abort', onAbort, {once: true});
  if (parentSignal?.aborted) controller.abort();
  const timeout = setTimeout(onAbort, decisionTimeoutMs);
  let rejectAbort;
  const aborted = new Promise((_resolve, reject) => { rejectAbort = reject; });
  const onDecisionAbort = () => rejectAbort(Object.assign(new Error('Decision cancelled'), {name: 'AbortError'}));
  controller.signal.addEventListener('abort', onDecisionAbort, {once: true});
  try {
    if (controller.signal.aborted) return 'sharing_unavailable';
    const decision = await Promise.race([aborted, (async () => {
      const client = await resolveAuthorization({signal: controller.signal});
      if (controller.signal.aborted) throw Object.assign(new Error('Decision cancelled'), {name: 'AbortError'});
      return client.decide(sharingAction, sourceId);
    })()]);
    if (controller.signal.aborted) return 'sharing_unavailable';
    if (decision?.outcome === 'permitted') return '';
    if (decision?.outcome === 'denied' || decision?.outcome === 'forbidden') return 'sharing_permission_denied';
    if (decision?.outcome === 'not_granted') return 'sharing_not_granted';
    if (decision?.outcome === 'unknown_action') return 'sharing_unknown_action';
    return 'sharing_unavailable';
  } catch {
    return 'sharing_unavailable';
  } finally {
    clearTimeout(timeout);
    parentSignal?.removeEventListener('abort', onAbort);
    controller.signal.removeEventListener('abort', onDecisionAbort);
  }
}

/** Build an explicit, bounded OpenCode source watch tool. resolveReader receives
 * the OpenCode AbortSignal and returns the generated SourceReaderClient. */
export function createSourceWatchTool({tool, resolveReader, resolveAuthorization, now = () => performance.now(), wait = sleep,
  showsDisclosure = async () => true} = {}) {
  if (typeof tool !== 'function' || typeof resolveReader !== 'function') {
    throw new TypeError('A tool helper and source-reader resolver are required.');
  }
  const schema = tool.schema;
  return tool({
    description: 'After explicit approval, watch one fixed OpenAbstractions system-log source for at most 60 seconds. The default result contains counts and status only. Sharing event contents with this assistant, including a hosted model, requires a separate explicit approval and an exact OpenAbstractions sharing permit. Shared i64 values are JSON numbers when safe and exact decimal strings when wider than JavaScript’s safe integer range.',
    args: {
      sourceId: schema.string(),
      durationMs: schema.number(),
      maxRecords: schema.number(),
      maxBytes: schema.number(),
      shareEvents: schema.boolean().optional(),
    },
    async execute(args, context) {
      validateArgs(args);
      const shareEvents = args.shareEvents === true;
      const callerSignal = context?.signal ?? context?.abortSignal ?? null;
      const startedAt = new Date().toISOString();
      let watchStartedAt = startedAt;
      let pages = 0;
      let records = 0;
      let bytes = 0;
      const sharedRecords = [];
      const finish = async (status, normalCompletion = false) => {
        let disclose = shareEvents && normalCompletion && !callerSignal?.aborted;
        if (disclose) {
          const refused = await sharingDecision(resolveAuthorization, args.sourceId, callerSignal);
          if (refused) { status = refused; disclose = false; }
        }
        if (callerSignal?.aborted) { status = 'cancelled'; disclose = false; }
        const summary = emptySummary(args.sourceId, status, watchStartedAt, new Date().toISOString(), pages, records, bytes);
        if (disclose) {
          summary.provenance = 'native_source_records; producer_fields_are_unverified_claims; record_identity_is_empty';
          summary.events = sharedRecords;
        }
        const output = result(summary);
        sharedRecords.length = 0;
        return output;
      };
      if (callerSignal?.aborted) return finish('cancelled');
      if (typeof context?.ask !== 'function') {
        return finish('approval_unavailable');
      }
      const sourcePattern = `Read source ${JSON.stringify(args.sourceId)} for up to ${args.durationMs} ms, at most ${args.maxRecords} records and ${args.maxBytes} bytes. The watch returns counts and status unless event sharing is separately approved.`;
      const sourceApproval = await askForApproval(context.ask, {
        permission: 'openabstractions_log_source_read', patterns: [sourcePattern], always: [], metadata: {
          sourceId: args.sourceId, durationMs: args.durationMs, maxRecords: args.maxRecords, maxBytes: args.maxBytes,
        },
      }, callerSignal);
      if (sourceApproval !== 'approved') return finish(sourceApproval === 'cancelled' ? 'cancelled' : 'not_approved');
      if (callerSignal?.aborted) return finish('cancelled');
      if (shareEvents) {
        // A TUI that cannot show the sharing bounds gets no sharing question.
        let shown = false;
        try { shown = (await showsDisclosure()) === true; } catch { shown = false; }
        if (!shown) return finish('sharing_unavailable');
        if (callerSignal?.aborted) return finish('cancelled');
        const sharePattern = `Share native event messages, attributes and unverified producer claims from source ${JSON.stringify(args.sourceId)} observed during the next ${args.durationMs} ms, up to ${args.maxRecords} records and ${args.maxBytes} bytes, with this assistant's model. The model route may use a hosted provider.`;
        const shareApproval = await askForApproval(context.ask, {
          permission: 'openabstractions_log_source_share', patterns: [sharePattern], always: [], metadata: {
            sourceId: args.sourceId, durationMs: args.durationMs, maxRecords: args.maxRecords, maxBytes: args.maxBytes,
            recipient: 'current assistant model; route may be hosted',
          },
        }, callerSignal);
        if (shareApproval !== 'approved') return finish(shareApproval === 'cancelled' ? 'cancelled' : 'sharing_not_approved');
        const refused = await sharingDecision(resolveAuthorization, args.sourceId, callerSignal);
        if (refused) return finish(refused);
      }
      if (callerSignal?.aborted) return finish('cancelled');

      watchStartedAt = new Date().toISOString();
      const deadline = now() + args.durationMs;
      const watchController = new AbortController();
      let timedOut = false;
      const onCallerAbort = () => watchController.abort();
      callerSignal?.addEventListener('abort', onCallerAbort, {once: true});
      const timeout = setTimeout(() => {
        timedOut = true;
        watchController.abort();
      }, args.durationMs);
      const signal = watchController.signal;
      const stopStatus = () => {
        if (callerSignal?.aborted) return 'cancelled';
        if (timedOut || now() >= deadline) {
          timedOut = true;
          watchController.abort();
          return 'duration_elapsed';
        }
        return '';
      };
      let reader;
      try {
        const initialStop = stopStatus();
        if (initialStop) return finish(initialStop);
        reader = await resolveReader({signal});
        const stoppedAfterResolve = stopStatus();
        if (stoppedAfterResolve) return finish(stoppedAfterResolve);
        const catalog = await reader.sources();
        const stoppedAfterCatalog = stopStatus();
        if (stoppedAfterCatalog) return finish(stoppedAfterCatalog);
        if (!catalog || !catalogOutcomes.has(catalog.outcome) || !Array.isArray(catalog.sources)) return finish('corrupt');
        if (catalog.outcome !== 'catalog') return finish(catalog.outcome);
        const descriptor = catalog.sources.find((item) => item?.id === args.sourceId);
        if (!descriptor) return finish('unsupported');
        if (!availability.has(descriptor.availability)) return finish('unavailable');
        if (descriptor.availability !== 'available') return finish(descriptor.availability);

        const baseline = await reader.startAtEnd(args.sourceId);
        const stoppedAfterBaseline = stopStatus();
        if (stoppedAfterBaseline) return finish(stoppedAfterBaseline);
        if (!baseline || !pageOutcomes.has(baseline.outcome)) return finish('corrupt');
        if (baseline.outcome !== 'page') return finish(baseline.outcome);
        if (!Array.isArray(baseline.records) || baseline.records.length !== 0 || baseline.atEnd !== true
            || typeof baseline.next !== 'string' || baseline.next.length === 0) return finish('corrupt');
        let cursor = baseline.next;
        const seenCursors = new Set([cursor]);

        while (now() < deadline) {
          const stoppedBeforeRead = stopStatus();
          if (stoppedBeforeRead) return finish(stoppedBeforeRead, stoppedBeforeRead === 'duration_elapsed');
          if (shareEvents) {
            const refused = await sharingDecision(resolveAuthorization, args.sourceId, signal);
            if (refused) return finish(refused);
            const stoppedAfterDecision = stopStatus();
            if (stoppedAfterDecision) return finish(stoppedAfterDecision, stoppedAfterDecision === 'duration_elapsed');
          }
          const remainingRecords = args.maxRecords - records;
          const remainingBytes = args.maxBytes - bytes;
          if (remainingRecords <= 0 || remainingBytes <= 0) return finish('limit_reached', true);
          const page = await reader.read(args.sourceId, cursor,
            BigInt(Math.min(pageRecords, remainingRecords)), BigInt(Math.min(pageBytes, remainingBytes)));
          const stoppedAfterRead = stopStatus();
          // An interrupted read has no validated page completion. Preserve
          // counts and discard content, including earlier buffered records.
          if (stoppedAfterRead) return finish(stoppedAfterRead);
          if (!page || !pageOutcomes.has(page.outcome)) return finish('corrupt');
          if (page.outcome !== 'page') return finish(page.outcome);
          if (!Array.isArray(page.records) || typeof page.next !== 'string' || page.next.length === 0
              || typeof page.atEnd !== 'boolean' || page.records.length > remainingRecords) return finish('corrupt');
          const advances = page.records.length > 0;
          if (advances && (page.next === cursor || seenCursors.has(page.next))) return finish('duplicate_cursor');

          let pageBytesUsed = 0;
          for (const record of page.records) {
            if (!validRecord(record, args.sourceId)) return finish('corrupt');
            const size = recordBytes(record);
            if (size > remainingBytes - pageBytesUsed) return finish('limit_reached', true);
            pageBytesUsed += size;
          }
          pages += 1;
          records += page.records.length;
          bytes += pageBytesUsed;
          if (shareEvents) sharedRecords.push(...page.records);
          cursor = page.next;
          if (advances) seenCursors.add(cursor);
          if (records >= args.maxRecords || bytes >= args.maxBytes) return finish('limit_reached', true);
          if (!page.atEnd && page.records.length === 0) return finish('corrupt');
          if (page.atEnd) await wait(Math.min(pollIntervalMs, Math.max(0, deadline - now())), signal);
        }
        return finish('duration_elapsed', true);
      } catch (error) {
        if (callerSignal?.aborted || error?.name === 'AbortError' && !timedOut) return finish('cancelled');
        if (timedOut || now() >= deadline) return finish('duration_elapsed');
        return finish(permissionStatus(error));
      } finally {
        clearTimeout(timeout);
        callerSignal?.removeEventListener('abort', onCallerAbort);
      }
    },
  });
}
