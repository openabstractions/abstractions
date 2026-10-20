import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import OpenAbstractionsPlugin from '../plugin.js';
import {createSourceWatchTool} from '../source-watch.js';

const sourceId = 'fixture/system';
const event = (msg = 'fixture secret message') => ({sourceId, record: {
  schema: 1, time: '2026-09-29T10:11:12.000Z', level: 4, msg, attrs: {unit: 'fixture'}, identity: [],
}, producer: {provider: 'fixture-provider', program: 'fixture'}});

const schema = {optional() { return this; }};
function fakeTool(definition) { return definition; }
fakeTool.schema = {
  string: () => schema,
  number: () => schema,
  boolean: () => schema,
  enum: () => schema,
  array: () => schema,
  object: () => schema,
};

function fixtureReader({pages = [], availability = 'available'} = {}) {
  const calls = [];
  let index = 0;
  return {
    calls,
    async sources() {
      calls.push('sources');
      return {outcome: 'catalog', sources: [{id: sourceId, availability, limitations: ['retention']} ]};
    },
    async startAtEnd(id) {
      calls.push(['startAtEnd', id]);
      return {outcome: 'page', records: [], next: 'opaque:baseline', atEnd: true};
    },
    async read(id, cursor, readRecords, readBytes) {
      calls.push(['read', id, cursor, readRecords, readBytes]);
      return pages[index++] ?? {outcome: 'page', records: [], next: `opaque:empty:${index}`, atEnd: true};
    },
  };
}

function makeTool(reader, calls, extras = {}) {
  let tick = 0;
  return createSourceWatchTool({
    tool: fakeTool,
    resolveReader: async (options) => {
      calls.push(['resolve', options]);
      return reader;
    },
    resolveAuthorization: extras.resolveAuthorization ?? (async () => ({decide: async () => ({outcome: 'permitted'})})),
    now: extras.now ?? (() => tick),
    wait: extras.wait ?? (async (ms) => { tick += ms; }),
  });
}

const args = (extra = {}) => ({sourceId, durationMs: 100, maxRecords: 4, maxBytes: 4096, ...extra});

async function registeredWatch(resolveSourceReader, resolveAuthorization = async () => ({decide: async () => ({outcome: 'permitted'})})) {
  const plugin = await OpenAbstractionsPlugin({client: {}}, {providerId: 'oa-fixture', tools: {watch_logs: true}}, {
    tool: fakeTool,
    readInventory: async () => ({offers: []}),
    resolveSourceReader,
    resolveAuthorization,
    // These cases run as a TUI that shows the bounds (the consent-display draft).
    showsDisclosure: async () => true,
  });
  await plugin.config({provider: {'oa-fixture': {
    npm: '@openabstractions/opencode',
    options: {runtimeEndpoint: 'fixture-runtime', scope: 'local'},
    models: {},
  }}});
  return plugin.tool.openabstractions_watch_logs;
}

function syntheticReader({onCall = () => {}, read} = {}) {
  return {
    async sources() { onCall('sources'); return {outcome: 'catalog', sources: [{id: sourceId, availability: 'available'}]}; },
    async startAtEnd(id) { onCall('startAtEnd', id); return {outcome: 'page', records: [], next: 'fixture:baseline', atEnd: true}; },
    async read(id, cursor, records, bytes) {
      onCall('read', id, cursor, records, bytes);
      return read ? read(id, cursor, records, bytes) : {outcome: 'page', records: [event()], next: 'fixture:after-1', atEnd: true};
    },
  };
}

test('asks before catalog/current-end/read and returns only counts by default', async () => {
  const calls = [];
  const reader = fixtureReader({pages: [{outcome: 'page', records: [event()], next: 'opaque:after-1', atEnd: true}]});
  const definition = makeTool(reader, calls);
  const output = JSON.parse(await definition.execute(args(), {ask: async (request) => calls.push(['ask', request])}));
  assert.equal(calls[0][0], 'ask');
  assert.equal(calls[1][0], 'resolve');
  assert.deepEqual(reader.calls[0], 'sources');
  assert.deepEqual(reader.calls[1], ['startAtEnd', sourceId]);
  assert.equal(reader.calls[2][0], 'read');
  assert.equal(calls[0][1].permission, 'openabstractions_log_source_read');
  assert.match(calls[0][1].patterns[0], /fixture\/system/);
  assert.deepEqual(output, {
    kind: 'source_watch', sourceId, status: 'duration_elapsed', startedAt: output.startedAt,
    endedAt: output.endedAt, pages: 1, records: 1, bytes: output.bytes,
  });
  assert.equal(output.events, undefined);
  assert.equal(JSON.stringify(output).includes('fixture secret message'), false);
});

test('event contents require separate informed approval before any read', async () => {
  const calls = [];
  const reader = fixtureReader({pages: [{outcome: 'page', records: [event()], next: 'opaque:after-1', atEnd: true}]});
  const definition = makeTool(reader, calls);
  const output = JSON.parse(await definition.execute(args({shareEvents: true}), {
    ask: async (request) => calls.push(['ask', request]),
  }));
  assert.equal(calls[0][1].permission, 'openabstractions_log_source_read');
  assert.equal(calls[1][1].permission, 'openabstractions_log_source_share');
  assert.match(calls[1][1].patterns[0], /may use a hosted provider/);
  assert.deepEqual(calls[1][1].metadata, {
    sourceId, durationMs: 100, maxRecords: 4, maxBytes: 4096,
    recipient: 'current assistant model; route may be hosted',
  });
  assert.equal(calls[2][0], 'resolve');
  assert.equal(reader.calls.some((call) => Array.isArray(call) && call[0] === 'read'), true);
  assert.equal(output.events.length, 1);
  assert.equal(output.events[0].record.msg, 'fixture secret message');
  assert.deepEqual(output.events[0].record.identity, []);
  assert.equal(output.events[0].producer.provider, 'fixture-provider');
  assert.match(output.provenance, /producer_fields_are_unverified_claims/);
});

// The official OpenCode's TUI shows an OA approval as "Call tool <name>"
// without its bounds, so the plugin never asks it to approve sharing.
test('on the official OpenCode a sharing request is unavailable before its question; counts still work', async () => {
  const stockClient = {path: {get: async () => ({data: {config: 'C:\\Users\\p\\.config\\opencode', state: 'x'}})}};
  for (const [client, shares] of [[stockClient, false]]) {
    const asked = [];
    const reads = [];
    const plugin = await OpenAbstractionsPlugin({client}, {providerId: 'oa-fixture', tools: {watch_logs: true}}, {
      tool: fakeTool, readInventory: async () => ({offers: []}), env: {},
      resolveSourceReader: async () => syntheticReader({onCall: (name) => reads.push(name)}),
      resolveAuthorization: async () => ({decide: async () => ({outcome: 'permitted'})}),
    });
    const watch = plugin.tool.openabstractions_watch_logs;
    const shared = JSON.parse(await watch.execute(args({durationMs: 20, shareEvents: true}), {ask: async (request) => asked.push(request.permission)}));
    if (shares) {
      assert.deepEqual(asked, ['openabstractions_log_source_read', 'openabstractions_log_source_share']);
      assert.notEqual(shared.status, 'sharing_unavailable');
    } else {
      assert.deepEqual(asked, ['openabstractions_log_source_read'], 'no sharing question reaches the official TUI');
      assert.equal(shared.status, 'sharing_unavailable');
      assert.equal(shared.events, undefined);
      assert.deepEqual(reads, [], 'nothing is read for a refused share');
      const counts = JSON.parse(await watch.execute(args({durationMs: 20}), {ask: async (request) => asked.push(request.permission)}));
      assert.notEqual(counts.status, 'sharing_unavailable');
      assert.ok(reads.includes('sources'), 'a counts-only watch runs on the official build');
    }
  }
});

test('rejected source or content approval stops before reader resolution and native calls', async () => {
  const calls = [];
  const reader = fixtureReader();
  const definition = makeTool(reader, calls);
  const sourceDenied = JSON.parse(await definition.execute(args(), {
    ask: async (request) => { calls.push(['ask', request]); throw new Error('denied'); },
  }));
  assert.equal(sourceDenied.status, 'not_approved');
  assert.deepEqual(calls.map((call) => call[0]), ['ask']);

  calls.length = 0;
  let askCount = 0;
  const shareDenied = JSON.parse(await definition.execute(args({shareEvents: true}), {
    ask: async (request) => {
      calls.push(['ask', request]);
      askCount += 1;
      if (askCount === 2) throw new Error('denied');
    },
  }));
  assert.equal(shareDenied.status, 'sharing_not_approved');
  assert.deepEqual(calls.map((call) => call[0]), ['ask', 'ask']);
});

test('cancellation during the approval prompt returns without resolving or probing a source', async () => {
  const controller = new AbortController();
  const calls = [];
  const reader = fixtureReader();
  const definition = makeTool(reader, calls);
  const pending = definition.execute(args(), {
    signal: controller.signal,
    ask: async () => new Promise(() => {}),
  });
  controller.abort();
  const output = JSON.parse(await pending);
  assert.equal(output.status, 'cancelled');
  assert.deepEqual(calls, []);
  assert.deepEqual(reader.calls, []);
});

test('fixed catalog selection and input caps fail before approval', async () => {
  const calls = [];
  const reader = fixtureReader();
  const definition = makeTool(reader, calls);
  for (const invalid of [
    args({sourceId: '../../etc/passwd'}),
    args({sourceId: 'fixture//system'}),
    args({durationMs: 60_001}),
    args({maxRecords: 129}),
    args({maxBytes: 65_537}),
  ]) {
    await assert.rejects(definition.execute(invalid, {ask: async () => calls.push('ask')}));
  }
  assert.deepEqual(calls, []);
});

test('unsupported catalog entries are reported without a baseline or read', async () => {
  const calls = [];
  const reader = fixtureReader({availability: 'unsupported'});
  const output = JSON.parse(await makeTool(reader, calls).execute(args(), {ask: async () => calls.push('ask')}));
  assert.equal(output.status, 'unsupported');
  assert.deepEqual(reader.calls, ['sources']);
});

test('typed source refusals retain policy and native denial without retrying', async () => {
  for (const stage of ['sources', 'startAtEnd', 'read']) {
    const outcomes = ['not_permitted', 'forbidden', 'unavailable', 'invalid'];
    if (stage !== 'sources') outcomes.push('os_authorization_required', 'os_authorization_pending');
    for (const outcome of outcomes) {
      const calls = [];
      const reader = fixtureReader();
      reader.sources = async () => {
        reader.calls.push('sources');
        return stage === 'sources' ? {outcome, sources: []}
          : {outcome: 'catalog', sources: [{id: sourceId, availability: 'available'}]};
      };
      if (stage !== 'sources') reader[stage] = async () => {
        reader.calls.push([stage]);
        return {outcome, records: [], next: stage === 'read' ? 'opaque:baseline' : '', atEnd: false};
      };
      const output = JSON.parse(await makeTool(reader, calls).execute(args({shareEvents: true}), {
        ask: async (request) => calls.push(['ask', request]),
      }));
      assert.equal(output.status, outcome, `${stage}: ${outcome}`);
      assert.equal(output.records, 0);
      assert.equal(output.events, undefined);
      assert.equal(calls.filter((call) => call[0] === 'ask').length, 2, 'only the original local approvals');
      assert.equal(reader.calls.length, ['sources', 'startAtEnd', 'read'].indexOf(stage) + 1, 'no native retry after refusal');
    }
  }
});

test('typed source catalogue requires its success outcome before native reads', async () => {
  for (const outcome of [undefined, 'future']) {
    const reader = fixtureReader();
    reader.sources = async () => {
      reader.calls.push('sources');
      return {outcome, sources: [{id: sourceId, availability: 'available'}]};
    };
    const output = JSON.parse(await makeTool(reader, []).execute(args(), {ask: async () => {}}));
    assert.equal(output.status, 'corrupt');
    assert.deepEqual(reader.calls, ['sources']);
  }
});

test('gap stops polling without cursor restart or event disclosure', async () => {
  const calls = [];
  const reader = fixtureReader({pages: [{outcome: 'gap', records: [], next: 'ignored', atEnd: false}]});
  const output = JSON.parse(await makeTool(reader, calls).execute(args(), {ask: async () => {}}));
  assert.equal(output.status, 'gap');
  assert.equal(output.records, 0);
  assert.equal(output.events, undefined);
  assert.equal(reader.calls.filter((call) => Array.isArray(call) && call[0] === 'read').length, 1);
});

test('repeated continuation after events stops as duplicate_cursor', async () => {
  const calls = [];
  const reader = fixtureReader({pages: [{outcome: 'page', records: [event()], next: 'opaque:baseline', atEnd: true}]});
  const output = JSON.parse(await makeTool(reader, calls).execute(args({shareEvents: true}), {
    ask: async () => {},
  }));
  assert.equal(output.status, 'duplicate_cursor');
  assert.equal(output.records, 0);
  assert.equal(output.events, undefined);
});

test('watch stops at its record and byte caps and respects abort during polling', async () => {
  const calls = [];
  const reader = fixtureReader({pages: [{outcome: 'page', records: [event('x')], next: 'opaque:after-1', atEnd: true}]});
  const recordLimited = JSON.parse(await makeTool(reader, calls).execute(args({maxRecords: 1}), {ask: async () => {}}));
  assert.equal(recordLimited.status, 'limit_reached');
  assert.equal(recordLimited.records, 1);

  const byteCalls = [];
  const byteReader = fixtureReader({pages: [{outcome: 'page', records: [event('x'.repeat(200))], next: 'opaque:after-1', atEnd: true}]});
  const byteLimited = JSON.parse(await makeTool(byteReader, byteCalls).execute(args({maxBytes: 128}), {ask: async () => {}}));
  assert.equal(byteLimited.status, 'limit_reached');
  assert.equal(byteLimited.records, 0);

  const controller = new AbortController();
  const cancelCalls = [];
  const cancelReader = fixtureReader();
  let tick = 0;
  const cancelled = JSON.parse(await makeTool(cancelReader, cancelCalls, {
    now: () => tick,
    wait: async (_ms, signal) => { tick += 10; controller.abort(); assert.equal(signal.aborted, true); },
  }).execute(args({durationMs: 100}), {signal: controller.signal, ask: async () => {}}));
  assert.equal(cancelled.status, 'cancelled');
  assert.equal(cancelReader.calls.filter((call) => Array.isArray(call) && call[0] === 'read').length, 1);
});

test('duration aborts a blocked reader operation', async () => {
  let passedSignal;
  const definition = createSourceWatchTool({tool: fakeTool, resolveReader: async ({signal}) => {
    passedSignal = signal;
    return {
      async sources() { return {outcome: 'catalog', sources: [{id: sourceId, availability: 'available'}]}; },
      async startAtEnd() { return {outcome: 'page', records: [], next: 'opaque:baseline', atEnd: true}; },
      async read() {
        return new Promise((_resolve, reject) => {
          signal.addEventListener('abort', () => reject(Object.assign(new Error('aborted'), {name: 'AbortError'})), {once: true});
        });
      },
    };
  }});
  const started = performance.now();
  const output = JSON.parse(await definition.execute(args({durationMs: 25}), {ask: async () => {}}));
  assert.equal(output.status, 'duration_elapsed');
  assert.equal(passedSignal.aborted, true);
  assert.ok(performance.now() - started < 500, 'duration did not cancel blocked source read promptly');
});

test('registered plugin tool asks before fixture probes and defaults to count-only output', async () => {
  const calls = [];
  const watch = await registeredWatch(async (options, signal) => {
    calls.push(['resolve', options.runtimeEndpoint]);
    assert.ok(signal instanceof AbortSignal);
    return syntheticReader({onCall: (...call) => calls.push(call)});
  });
  const reply = await watch.execute({sourceId, durationMs: 1000, maxRecords: 1, maxBytes: 4096}, {
    ask: async (request) => calls.push(['ask', request]),
  });
  const output = JSON.parse(reply);
  assert.deepEqual(calls.map((call) => call[0]), ['ask', 'resolve', 'sources', 'startAtEnd', 'read']);
  assert.equal(calls[1][1], 'fixture-runtime');
  assert.equal(calls[0][1].permission, 'openabstractions_log_source_read');
  assert.equal(output.records, 1);
  assert.equal(output.events, undefined);
  assert.equal(JSON.stringify(output).includes('fixture secret message'), false);
});

test('registered plugin tool requires hosted-aware share approval before returning fixture events', async () => {
  const calls = [];
  const watch = await registeredWatch(async (options, signal) => {
    calls.push(['resolve', options.runtimeEndpoint]);
    assert.ok(signal instanceof AbortSignal);
    return syntheticReader({onCall: (...call) => calls.push(call)});
  });
  const reply = await watch.execute({sourceId, durationMs: 1000, maxRecords: 1, maxBytes: 4096, shareEvents: true}, {
    ask: async (request) => calls.push(['ask', request]),
  });
  const output = JSON.parse(reply);
  assert.deepEqual(calls.map((call) => call[0]), ['ask', 'ask', 'resolve', 'sources', 'startAtEnd', 'read']);
  assert.equal(calls[0][1].permission, 'openabstractions_log_source_read');
  assert.equal(calls[1][1].permission, 'openabstractions_log_source_share');
  assert.match(calls[1][1].patterns[0], /may use a hosted provider/);
  assert.equal(output.events[0].record.msg, 'fixture secret message');
  assert.match(output.provenance, /producer_fields_are_unverified_claims/);
});

test('registered plugin tool propagates cancellation and stops on source gap', async () => {
  const controller = new AbortController();
  let enteredRead;
  const waitingRead = new Promise((resolve) => { enteredRead = resolve; });
  let sourceSignal;
  const watch = await registeredWatch(async (_options, signal) => {
    sourceSignal = signal;
    return syntheticReader({read: async () => new Promise((resolve) => {
      enteredRead();
      signal.addEventListener('abort', () => resolve({outcome: 'unavailable', records: [], next: '', atEnd: false}), {once: true});
    })});
  });
  const pending = watch.execute({sourceId, durationMs: 60_000, maxRecords: 10, maxBytes: 4096}, {
    signal: controller.signal, ask: async () => {},
  });
  await waitingRead;
  controller.abort();
  assert.equal(sourceSignal.aborted, true);
  assert.equal(JSON.parse(await pending).status, 'cancelled');

  const calls = [];
  const gapWatch = await registeredWatch(async () => syntheticReader({
    onCall: (...call) => calls.push(call),
    read: async () => ({outcome: 'gap', records: [], next: 'fixture:baseline', atEnd: false}),
  }));
  const gapReply = JSON.parse(await gapWatch.execute({sourceId, durationMs: 1000, maxRecords: 10, maxBytes: 4096}, {
    ask: async () => {},
  }));
  assert.equal(gapReply.status, 'gap');
  assert.equal(calls.filter((call) => call[0] === 'read').length, 1);
});

test('generated i64 records accept schema BigInt and share wide values as exact decimal strings', async () => {
  const native = event('fixture wide integer event');
  native.record.schema = 1n;
  native.record.level = 8n;
  native.producer.pid = 9007199254740993n;
  native.producer.uid = 42n;
  const reader = fixtureReader({pages: [{outcome: 'page', records: [native], next: 'opaque:wide', atEnd: true}]});
  const tool = makeTool(reader, []);
  const output = JSON.parse(await tool.execute(args({maxRecords: 1, shareEvents: true}), {ask: async () => {}}));
  assert.equal(output.status, 'limit_reached');
  assert.equal(output.events[0].record.schema, 1);
  assert.equal(output.events[0].record.level, 8);
  assert.equal(output.events[0].producer.pid, '9007199254740993');
  assert.equal(output.events[0].producer.uid, 42);
  assert.equal(output.bytes, new TextEncoder().encode(JSON.stringify(output.events[0])).byteLength);
});

test('source record schema accepts only numeric one or BigInt one', async () => {
  const invalid = event();
  invalid.record.schema = '1';
  const reader = fixtureReader({pages: [{outcome: 'page', records: [invalid], next: 'opaque:invalid-schema', atEnd: true}]});
  const tool = makeTool(reader, []);
  const output = JSON.parse(await tool.execute(args({maxRecords: 1}), {ask: async () => {}}));
  assert.equal(output.status, 'corrupt');
  assert.equal(output.records, 0);
});

function noContents(output) {
  assert.equal(output.events, undefined);
  assert.equal(output.provenance, undefined);
  assert.equal(JSON.stringify(output).includes('fixture secret'), false);
  assert.equal(JSON.stringify(output).includes('fixture-provider'), false);
}

function decisions(outcomes, observed = []) {
  let index = 0;
  return async ({signal}) => ({
    async decide(action, resource) {
      observed.push({action, resource, signal});
      const value = outcomes[Math.min(index++, outcomes.length - 1)];
      if (value instanceof Error) throw value;
      return {outcome: value};
    },
  });
}

test('only an exact permitted sharing decision proceeds past both local approvals', async () => {
  for (const outcome of ['denied', 'not_granted', 'unknown_action', 'invalid', 'forbidden', 'unavailable', 'future', undefined]) {
    const calls = [];
    const observed = [];
    const reader = fixtureReader();
    const output = JSON.parse(await makeTool(reader, calls, {resolveAuthorization: decisions([outcome], observed)})
      .execute(args({shareEvents: true}), {ask: async (request) => calls.push(['ask', request])}));
    assert.deepEqual(calls.map((call) => call[0]), ['ask', 'ask']);
    assert.deepEqual(reader.calls, []);
    assert.equal(observed.length, 1);
    assert.equal(observed[0].action, 'opencode/log-source.share');
    assert.equal(observed[0].resource, sourceId);
    assert.ok(observed[0].signal instanceof AbortSignal);
    assert.match(output.status, /^sharing_/);
    noContents(output);
  }
});

test('count-only mode makes no sharing decision and missing Rights fails closed', async () => {
  let calls = 0;
  const reader = fixtureReader();
  const resolveAuthorization = async () => { calls += 1; throw new Error('fixture secret outage'); };
  const tool = makeTool(reader, [], {resolveAuthorization});
  const countOnly = JSON.parse(await tool.execute(args(), {ask: async () => {}}));
  assert.equal(calls, 0);
  noContents(countOnly);
  const unavailable = JSON.parse(await tool.execute(args({shareEvents: true}), {ask: async () => {}}));
  assert.equal(calls, 1);
  assert.equal(unavailable.status, 'sharing_unavailable');
  noContents(unavailable);
  const missing = createSourceWatchTool({tool: fakeTool, resolveReader: async () => reader});
  assert.equal(JSON.parse(await missing.execute(args({shareEvents: true}), {ask: async () => {}})).status, 'sharing_unavailable');
});

test('registered tool resolves Rights with selected runtime and requires direct exact decisions before read and return', async () => {
  const calls = [];
  const watch = await registeredWatch(async () => syntheticReader({onCall: (name) => calls.push(name)}), async (options, signal) => {
    assert.equal(options.runtimeEndpoint, 'fixture-runtime');
    assert.ok(signal instanceof AbortSignal);
    return {async decide(action, resource) {
      assert.equal(action, 'opencode/log-source.share');
      assert.equal(resource, sourceId);
      calls.push('decide');
      return {outcome: 'permitted'};
    }, async decideFor() { assert.fail('must use the receiving subject'); }};
  });
  const output = JSON.parse(await watch.execute(args({maxRecords: 1, shareEvents: true}), {ask: async () => calls.push('ask')}));
  assert.deepEqual(calls, ['ask', 'ask', 'decide', 'sources', 'startAtEnd', 'decide', 'read', 'decide']);
  assert.equal(output.events.length, 1);
});

test('revocation and Rights failure in the final check remove a buffered event from returned JSON', async () => {
  for (const final of ['denied', 'not_granted', 'unknown_action', new Error('fixture secret final outage')]) {
    const reader = fixtureReader({pages: [{outcome: 'page', records: [event()], next: 'opaque:after-1', atEnd: true}]});
    const observed = [];
    const output = JSON.parse(await makeTool(reader, [], {resolveAuthorization: decisions(['permitted', 'permitted', final], observed)})
      .execute(args({shareEvents: true, maxRecords: 1}), {ask: async () => {}}));
    assert.equal(observed.length, 3);
    assert.equal(output.records, 1);
    assert.match(output.status, /^sharing_/);
    noContents(output);
  }
});

test('rechecking during collection refuses a second read and clears previously buffered contents', async () => {
  const reader = fixtureReader({pages: [{outcome: 'page', records: [event()], next: 'opaque:after-1', atEnd: false}]});
  const output = JSON.parse(await makeTool(reader, [], {resolveAuthorization: decisions(['permitted', 'permitted', 'denied'])})
    .execute(args({shareEvents: true}), {ask: async () => {}}));
  assert.equal(output.status, 'sharing_permission_denied');
  assert.equal(output.records, 1);
  assert.equal(reader.calls.filter((call) => Array.isArray(call) && call[0] === 'read').length, 1);
  noContents(output);
});

test('every partial source error/gap/corrupt branch suppresses already buffered events', async () => {
  for (const ending of ['gap', 'unavailable', 'invalid', 'record_too_large', 'corrupt', 'unsupported', 'forbidden', 'not_permitted', 'os_authorization_required', 'os_authorization_pending', 'throw', 'duplicate', 'wrong-source']) {
    let reads = 0;
    const reader = syntheticReader({read: async () => {
      if (++reads === 1) return {outcome: 'page', records: [event()], next: 'after-1', atEnd: false};
      if (ending === 'throw') throw new Error('fixture secret source failure');
      if (ending === 'duplicate') return {outcome: 'page', records: [event()], next: 'after-1', atEnd: false};
      if (ending === 'wrong-source') return {outcome: 'page', records: [{...event(), sourceId: 'other/source'}], next: 'after-2', atEnd: true};
      return {outcome: ending, records: [], next: '', atEnd: false};
    }});
    const output = JSON.parse(await makeTool(reader, []).execute(args({shareEvents: true}), {ask: async () => {}}));
    assert.equal(output.records, 1, ending);
    noContents(output);
  }
});

test('normal duration expiry uses a fresh final signal and caller cancellation during that check suppresses events', async () => {
  for (const cancel of [false, true]) {
    const caller = new AbortController();
    let decisionCount = 0;
    let finalSignal;
    let sourceSignal;
    const reader = fixtureReader({pages: [{outcome: 'page', records: [event()], next: 'after-1', atEnd: true}]});
    const tool = makeTool(reader, [], {
      // Let the real collection timer expire after its first buffered page.
      now: () => 0,
      wait: async (_ms, signal) => { sourceSignal = signal; await new Promise((resolve) => signal.addEventListener('abort', resolve, {once: true})); },
      resolveAuthorization: async ({signal}) => ({async decide() {
        if (++decisionCount === 3) {
          finalSignal = signal;
          assert.equal(sourceSignal.aborted, true);
          assert.equal(finalSignal.aborted, false);
          if (cancel) caller.abort();
        }
        return {outcome: 'permitted'};
      }}),
    });
    const output = JSON.parse(await tool.execute(args({durationMs: 20, shareEvents: true}), {signal: caller.signal, ask: async () => {}}));
    assert.notEqual(finalSignal, sourceSignal);
    if (cancel) { assert.equal(output.status, 'cancelled'); noContents(output); }
    else { assert.equal(output.status, 'duration_elapsed'); assert.equal(output.events.length, 1); }
  }
});

test('a blocked fresh final decision has a bounded deadline even when the client ignores its signal', async () => {
  let count = 0;
  const reader = fixtureReader({pages: [{outcome: 'page', records: [event()], next: 'after-1', atEnd: true}]});
  const tool = makeTool(reader, [], {resolveAuthorization: async () => ({async decide() {
    if (++count === 3) return new Promise(() => {});
    return {outcome: 'permitted'};
  }})});
  const started = performance.now();
  const output = JSON.parse(await tool.execute(args({shareEvents: true, maxRecords: 1}), {ask: async () => {}}));
  assert.equal(output.status, 'sharing_unavailable');
  assert.equal(output.records, 1);
  assert.ok(performance.now() - started < 6500, 'final Rights deadline did not bound the tool');
  noContents(output);
});

test('caller cancellation after buffering or while a fresh resolver is blocked clears events promptly', async () => {
  for (const duringFinal of [false, true]) {
    const caller = new AbortController();
    let count = 0;
    const reader = fixtureReader({pages: [{outcome: 'page', records: [event()], next: 'after-1', atEnd: true}]});
    const tool = makeTool(reader, [], {
      wait: async () => caller.abort(),
      resolveAuthorization: async ({signal}) => {
        if (++count === 3 && duringFinal) {
          setTimeout(() => caller.abort(), 10);
          await new Promise(() => {});
        }
        return {decide: async () => ({outcome: 'permitted'})};
      },
    });
    const started = performance.now();
    const output = JSON.parse(await tool.execute(args({shareEvents: true, maxRecords: duringFinal ? 1 : 4}), {signal: caller.signal, ask: async () => {}}));
    assert.equal(output.status, 'cancelled');
    assert.equal(output.records, 1);
    assert.ok(performance.now() - started < 500);
    noContents(output);
  }
});

test('package includes the coordinated closed action description as an explicitly unfilled operator template', async () => {
  const file = 'rights-action-description.template.json';
  const descriptor = JSON.parse(await readFile(new URL(`../${file}`, import.meta.url), 'utf8'));
  const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'));
  assert.ok(pkg.files.includes(file));
  assert.deepEqual(Object.keys(descriptor).sort(), ['action', 'app_label', 'declared_risk', 'description', 'feature_app']);
  assert.deepEqual(Object.keys(descriptor.feature_app).sort(), ['account', 'program']);
  assert.equal(descriptor.action, 'opencode/log-source.share');
  assert.equal(descriptor.app_label, 'OpenCode');
  assert.equal(descriptor.description, "Share event contents from the named log source with OpenCode's assistant model; that model may be hosted");
  assert.equal(descriptor.declared_risk, 'sensitive');
  assert.match(descriptor.feature_app.account, /^<.+>$/);
  assert.match(descriptor.feature_app.program, /^<.+>$/);
});

test('duration expiring during an unfinished or failing source read suppresses previously buffered contents', async () => {
  for (const outcome of ['corrupt-page', 'unavailable', 'abort-error']) {
    let reads = 0;
    let tick = 0;
    const reader = syntheticReader({read: async () => {
      if (++reads === 1) return {outcome: 'page', records: [event()], next: 'after-1', atEnd: false};
      tick = 100;
      if (outcome === 'abort-error') throw Object.assign(new Error('fixture secret failure'), {name: 'AbortError'});
      if (outcome === 'corrupt-page') return {outcome: 'page', records: undefined, next: 'after-2', atEnd: true};
      return {outcome, records: [], next: '', atEnd: false};
    }});
    const output = JSON.parse(await makeTool(reader, [], {now: () => tick}).execute(args({shareEvents: true}), {ask: async () => {}}));
    assert.equal(output.status, outcome === 'abort-error' ? 'cancelled' : 'duration_elapsed');
    assert.equal(output.records, 1);
    noContents(output);
  }
});
