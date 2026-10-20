import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import OpenAbstractionsPlugin from '../plugin.js';
import {Machine} from '../../../openabstractions-flat/abstraction-facade/javascript/index.js';
import {SourceReaderClient} from '../../../openabstractions-flat/abstraction-logging/javascript/js/abstraction/logging/index.mjs';
import {AuthorizationClient, AuthorizationOperatorClient} from '../../../openabstractions-flat/abstraction-rights/javascript/js/abstraction/rights/api/index.mjs';
import {BunNativeConnector} from '../../../openabstractions-flat/abstraction-identity/javascript/bun.js';

const endpoint = process.argv[2];
const mode = process.argv[3];
const sourceId = process.argv[4];
const rightsEndpoint = process.env.OA_SOURCE_WATCH_RIGHTS_ENDPOINT;
assert.equal(endpoint, process.env.OA_SOURCE_WATCH_FIXTURE_ENDPOINT);
assert.ok(endpoint && rightsEndpoint);
assert.ok(['allow', 'deny', 'not-granted', 'share-denied', 'wrong-resource', 'wrong-program', 'revoke', 'outage', 'cancel', 'partial-error'].includes(mode));

assert.ok(['windows/application', 'linux/system-journal'].includes(sourceId));
const resolverEndpoint = 'fixture-resolver';
const encoder = new TextEncoder();
const decoder = new TextDecoder();
const native = new BunNativeConnector();

// An ordinary application can inspect the operator's actual native publication.
// Its declared feature app and receiving publisher are distinct subjects.
const declaration = JSON.parse(await readFile(new URL('../rights-action-description.template.json', import.meta.url), 'utf8'));
const published = await new AuthorizationClient(native.connect(rightsEndpoint, {})).listActionDescriptions('', 64n);
assert.equal(published.outcome, 'page');
assert.equal(published.complete, true);
const described = published.actions.find((entry) => entry.action === declaration.action);
assert.ok(described);
assert.deepEqual(described.choices, ['allow', 'block', 'remove_rule']);
assert.deepEqual(described.description, {action: declaration.action,
  featureApp: {account: process.env.OA_SOURCE_WATCH_FEATURE_ACCOUNT, program: process.env.OA_SOURCE_WATCH_FEATURE_PROGRAM},
  appLabel: declaration.app_label, description: declaration.description, declaredRisk: declaration.declared_risk});
assert.deepEqual(described.publishedBy, {account: process.env.OA_SOURCE_WATCH_PUBLISHER_ACCOUNT,
  program: process.env.OA_SOURCE_WATCH_PUBLISHER_PROGRAM});
assert.notDeepEqual(described.publishedBy, described.description.featureApp);
assert.match(described.publishedAt, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);

class FixtureConnector {
  supports(scope, transport) { return native.supports(scope, transport); }
  connect(target, options) {
    if (target !== resolverEndpoint) return native.connect(target, options);
    return {
      async exchangeFrame(frame) {
        const request = JSON.parse(decoder.decode(frame));
        assert.equal(request.service, 'abstraction.facade/resolver@1');
        assert.equal(request.method, 'Resolve');
        const {request: selection} = request.arguments;
        const isRights = selection.capability === 'abstraction.rights';
        assert.equal(selection.capability, isRights ? 'abstraction.rights' : 'abstraction.logging');
        const contract = isRights ? 'abstraction.rights/authorization@1' : 'abstraction.logging/source-reader@1';
        assert.deepEqual(selection.contracts, [contract]);
        assert.equal(selection.scope, 'local');
        return encoder.encode(JSON.stringify({version: 1, service: request.service, method: request.method, ok: true,
          payload: {value: {status: 'resolved', reference: {
            provider: isRights ? 'fixture.rights' : 'fixture.logging', capability: selection.capability,
            contract, guarantees: [], scope: 'local',
            transport: 'oa-framed-local@1', endpoint: isRights ? rightsEndpoint : endpoint,
          }}}}));
      },
      async writeFrame() {},
    };
  }
}

const schemaValue = () => ({optional() { return this; }});
const schema = {
  string: schemaValue,
  number: schemaValue,
  boolean: schemaValue,
  enum: schemaValue,
  array: schemaValue,
  object: schemaValue,
};
const tool = Object.assign((definition) => definition, {schema});
const caller = new AbortController();
let sharingDecisions = 0;
let sharing = false;
let sourceReads = 0;
async function operatorMutation(kind) {
  // Test control only: the parent Go operator edits policy through its public
  // client. The application has no operator authority or policy-writing code.
  console.log(`OA_SOURCE_WATCH_FIXTURE_MUTATE=${kind}`);
  const response = await new Promise((resolve) => process.stdin.once('data', resolve));
  process.stdin.pause();
  assert.equal(response.toString().trim(), 'changed');
}
const plugin = await OpenAbstractionsPlugin({client: {}}, {tools: {watch_logs: true}}, {
  tool,
  readInventory: async () => ({offers: []}),
  resolveAuthorization: async (_options, signal) => {
    if (++sharingDecisions === 3) {
      if (mode === 'revoke' || mode === 'outage') await operatorMutation(mode);
      if (mode === 'cancel') caller.abort();
    }
    const machine = new Machine(resolverEndpoint, {connector: new FixtureConnector(), cancellation: signal});
    return new AuthorizationClient(await machine.resolveService('abstraction.rights/authorization@1', {scope: 'local'}));
  },
  resolveSourceReader: async (_options, signal) => {
    try {
      const machine = new Machine(resolverEndpoint, {connector: new FixtureConnector(), cancellation: signal});
      const binding = await machine.resolveService('abstraction.logging/source-reader@1', {scope: 'local'});
      const client = new SourceReaderClient(binding);
      for (const method of ['sources', 'startAtEnd', 'read']) {
        const call = client[method].bind(client);
        client[method] = async (...args) => {
          if (method === 'read' && sharing && mode === 'partial-error' && ++sourceReads === 2) {
            throw new Error('synthetic fixture event source failed after buffering');
          }
          try {
            const result = await call(...args);
            if (method === 'read' && result.records.length > 0) {
              assert.equal(result.records[0].record.schema, 1n);
              assert.equal(result.records[0].record.level, 9007199254740993n);
              assert.equal(result.records[0].producer.pid, 9007199254740995n);
              if (sharing && mode === 'partial-error') result.atEnd = false;
            }
            return result;
          }
          catch (error) { console.error(`source-reader ${method} failed:`, error); throw error; }
        };
      }
      return client;
    } catch (error) { console.error('facade resolution failed:', error); throw error; }
  },
});
await plugin.config({provider: {openabstractions: {
  npm: '@openabstractions/opencode', options: {runtimeEndpoint: endpoint, scope: 'local'}, models: {},
}}});

const asks = [];
const ask = async (request) => { asks.push(request); };
const execute = async (shareEvents = false) => {
  sharing = shareEvents;
  sourceReads = 0;
  return JSON.parse(await plugin.tool.openabstractions_watch_logs.execute({
    sourceId, durationMs: 1000, maxRecords: mode === 'partial-error' && shareEvents ? 2 : 1, maxBytes: 4096, shareEvents,
  }, {ask, signal: caller.signal}));
};
const reply = await execute();
let shared;
if (mode !== 'deny') {
  assert.equal(reply.kind, 'source_watch');
  assert.equal(reply.sourceId, sourceId);
  assert.equal(reply.records, 1, JSON.stringify(reply));
  assert.equal(reply.events, undefined);
  assert.equal(reply.status, 'limit_reached');
  assert.equal(sharingDecisions, 0);
  // The ordinary app is refused by the actual public operator endpoint.
  const operator = new AuthorizationOperatorClient(native.connect(rightsEndpoint, {}));
  assert.equal((await operator.listPolicy('', 1n)).outcome, 'forbidden');
  shared = await execute(true);
  if (mode === 'allow') {
    assert.equal(shared.status, 'limit_reached');
    assert.equal(shared.records, 1);
    assert.equal(shared.events[0].record.schema, 1);
    assert.equal(shared.events[0].record.level, '9007199254740993');
    assert.equal(shared.events[0].producer.pid, '9007199254740995');
    assert.equal(shared.events[0].record.attrs.source_field, 'fixture-value');
    assert.equal(shared.events[0].record.attrs.API_KEY, undefined);
    assert.match(shared.provenance, /producer_fields_are_unverified_claims/);
    assert.deepEqual(shared.events[0].record.identity, []);
    assert.equal(sharingDecisions, 3);
  } else {
    const statuses = {'not-granted': 'sharing_not_granted', 'share-denied': 'sharing_permission_denied',
      'wrong-resource': 'sharing_not_granted', 'wrong-program': 'sharing_not_granted',
      revoke: 'sharing_not_granted', outage: 'sharing_unavailable', cancel: 'cancelled', 'partial-error': 'unavailable'};
    assert.equal(shared.status, statuses[mode], JSON.stringify(shared));
    assert.equal(shared.records, ['revoke', 'outage', 'cancel', 'partial-error'].includes(mode) ? 1 : 0);
    assert.equal(shared.events, undefined);
    assert.equal(shared.provenance, undefined);
    assert.equal(JSON.stringify(shared).includes('synthetic fixture event'), false);
    assert.equal(JSON.stringify(shared).includes('fixture-writer'), false);
  }
  assert.deepEqual(asks.map((request) => request.permission), [
    'openabstractions_log_source_read',
    'openabstractions_log_source_read',
    'openabstractions_log_source_share',
  ]);
  assert.match(asks[2].patterns[0], /may use a hosted provider/);
} else {
  assert.equal(reply.status, 'permission_denied', JSON.stringify(reply));
  assert.equal(reply.records, 0);
  assert.deepEqual(asks.map((request) => request.permission), ['openabstractions_log_source_read']);
}
console.log(`OA_SOURCE_WATCH_FIXTURE_RESULT=${JSON.stringify({mode, reply, shared, asks: asks.map((request) => request.permission)})}`);
