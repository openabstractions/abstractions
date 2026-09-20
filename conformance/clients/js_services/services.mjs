// Installed generated capability clients against the production Go runtime.
// OA_JS_CONNECTOR selects the trusted native connector or the test pipe connector.
import assert from 'node:assert/strict';
import {Machine, ResolutionError} from '@openabstractions/facade';
import * as model from '@openabstractions/model';
import * as request from '@openabstractions/download-request';
import * as jobs from '@openabstractions/job-acceptance';
import * as logging from '@openabstractions/logging';
import * as config from '@openabstractions/config';
import * as rights from '@openabstractions/rights';
import * as router from '@openabstractions/router';
import * as asks from '@openabstractions/asks';
import * as storage from '@openabstractions/storage-content';
import {createHash, randomBytes} from 'node:crypto';
import {writeFileSync} from 'node:fs';

const readAction = 'abstraction.storage/content.read', writeAction = 'abstraction.storage/content.write';

const [endpoint, sourceURL, blockingURL, digest, size, account, program, editPolicyFile, jobPolicyFile] = process.argv.slice(2);
const connector = process.env.OA_JS_CONNECTOR === 'native'
  ? new (await import('@openabstractions/ipc')).NativeConnector()
  : new (await import('./pipe_connector.mjs')).PipeTestConnector();
const machine = new Machine(endpoint, {connector, timeout: 10000});
const bind = async (contract, Client, options) => (await machine.resolveService(contract, options)).client(Client);
// The running runtime serves no watch capability: resolution refuses with the facade's typed error.
await assert.rejects(machine.resolveService('abstraction.watch/watcher@1'), (e) => e instanceof ResolutionError &&
  e.status === 'unavailable' && e.capability === 'abstraction.watch' && e.contract === 'abstraction.watch/watcher@1' &&
  e.lookedFor === `the explicit endpoint ${endpoint}` && e.cause === undefined);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function until(check, what) {
  const deadline = performance.now() + 10000;
  while (!(await check())) {
    assert.ok(performance.now() < deadline, what);
    await sleep(20);
  }
}
const covered = [];
const done = (name) => covered.push(name);

// Model lookup composes the imported download Request and feeds job admission.
const resolver = await bind('abstraction.model/resolver@1', model.ModelResolverClient);
const lookup = await resolver.resolve({...model.newRef(), registry: 'fixture', repo: 'weights'});
assert.equal(lookup.outcome, 'resolved');
assert.deepEqual(lookup.request, {...request.newRequest(), artifact: {...request.newArtifact(), digest, size: BigInt(size)}, sources: [{...request.newSource(), scheme: 'http', locator: sourceURL}]});
const privateLookup = await resolver.resolve({...model.newRef(), registry: 'fixture', repo: 'private'});
assert.equal(privateLookup.outcome, 'unsupported_mapping');
assert.equal(privateLookup.request, null);
await assert.rejects(resolver.resolve({...model.newRef(), repo: 17}), (e) => e instanceof model.Refusal && e.word === 'wrong_type');
// The fixture's lookup policy reads the shared mode file on every Resolve.
writeFileSync(editPolicyFile, 'model-forbidden');
assert.deepEqual(Object.values(await resolver.resolve({...model.newRef(), registry: 'fixture', repo: 'weights'})), ['forbidden', null]);
writeFileSync(editPolicyFile, 'model-unavailable');
assert.deepEqual(Object.values(await resolver.resolve({...model.newRef(), registry: 'fixture', repo: 'weights'})), ['unavailable', null]);
writeFileSync(editPolicyFile, 'permit');
assert.equal((await resolver.resolve({...model.newRef(), registry: 'fixture', repo: 'weights'})).outcome, 'resolved');
done('ModelResolver.Resolve');

// Router inventory and routing; the fixture's router policy reads the same mode file.
const routes = await bind('abstraction.router/router@1', router.RouterClient);
// Live fake hosts shared with every router proof: resident Lemonade, cold LM Studio, unreachable Ollama.
{
  const models = await routes.models(false), hosts = await routes.hosts(false);
  assert.equal(models.models.length, 2, 'live model families');
  assert.equal(hosts.hosts.length, 3, 'live hosts');
  assert.ok(models.models.some((f) => f.names.some((a) => a.host === 'lemonade' && a.resident && a.servable)), 'resident alias lost');
  assert.ok(hosts.hosts.some((h) => h.host === 'ollama' && !h.up && h.why && h.installed === 0n && h.resident.length === 0), 'host failure hidden');
  const audited = hosts.asked.length, live = 'qwen/qwen3.6-35b-a3b';
  const choose = async (allowed) => (await routes.pick({...router.newPickRequest(), model: live, allowed})).decision;
  const resident = await choose(null);
  assert.deepEqual([resident.verdict, resident.loads, Boolean(resident.endpoint), resident.authorised], ['resident', 0n, true, null]);
  const refused = await choose({...router.newHostAllowance(), hosts: []});
  assert.deepEqual([refused.verdict, refused.endpoint, refused.authorised.hosts, refused.withheld.length > 0], ['unauthorised', '', [], true]);
  const cold = await choose({...router.newHostAllowance(), hosts: ['lmstudio']});
  assert.deepEqual([cold.verdict, cold.loads, cold.host, Boolean(cold.endpoint)], ['would-load', 1n, 'lmstudio', true]);
  const after = await routes.hosts(false);
  assert.equal(after.asked.length, audited + 3, 'audit recorded three picks');
  assert.ok(after.asked.slice(audited).every((a) => a.caller === after.observation.caller.pathDescription && a.user === after.observation.caller.userDescription), 'audit not bound to the JavaScript caller');
}
const pick = () => routes.pick({...router.newPickRequest(), model: 'js-model'});
assert.equal((await pick()).decision.asked, 'js-model');
for (const [mode, code] of [['router-forbidden', 'forbidden'], ['router-unavailable', 'policy_unavailable']]) {
  writeFileSync(editPolicyFile, mode);
  for (const call of [() => routes.models(false), () => routes.hosts(false), pick]) {
    await assert.rejects(call(), (e) => e instanceof router.ServiceError && e.code === code, mode);
  }
}
writeFileSync(editPolicyFile, 'permit');
assert.equal((await pick()).decision.asked, 'js-model');
done('Router.Models');
done('Router.Hosts');
done('Router.Pick');

const acceptance = await bind('abstraction.job/acceptance@1', jobs.RecoverableAcceptanceClient, {maxFrame: 2097152});
const operations = await bind('abstraction.job/operations@1', jobs.OperationControlClient, {maxFrame: 2097152});
const history = await acceptance.getHistoryWindow();
// HTTP execution declares the acceptance window as its result retention [JOB-A11].
assert.equal(history.resultRetentionMs, history.minimumRetentionMs, 'declared result retention');
assert.ok(history.resultRetentionMs > 0n);
async function submit(key, work, label = '') {
  const submission = jobs.newSubmission();
  submission.identity = {...jobs.newRequestIdentity(), key, historyEpoch: history.historyEpoch};
  submission.kind = 'download';
  submission.spec = request.encode(work);
  submission.label = label;
  const accepted = await acceptance.submit(submission);
  assert.equal(accepted.outcome, 'accepted', key);
  return submission.identity;
}
const resolvedJob = await submit('js-model-download', lookup.request);
await until(async () => {
  const observed = await operations.observeWork(resolvedJob);
  assert.equal(observed.outcome, 'observed');
  assert.ok(!['failed', 'cancelled'].includes(observed.snapshot.state), observed.snapshot.state);
  return observed.snapshot.state === 'complete';
}, 'model download completion');
const first = await operations.readResult(resolvedJob, 0n, 65536n);
assert.equal(first.outcome, 'data');
assert.equal(first.chunk.total, BigInt(size));
const blocked = await submit('js-cancel', {...request.newRequest(), sources: [{...request.newSource(), scheme: 'http', locator: blockingURL}]}, 'js fixture · blocked download');
await until(async () => (await operations.observeWork(blocked)).snapshot.state === 'running', 'blocked download running');
assert.equal((await acceptance.cancelWork(blocked)).outcome, 'requested');
await until(async () => (await operations.observeWork(blocked)).snapshot.state === 'cancelled', 'cancellation observed');
assert.equal((await acceptance.cancelWork(blocked)).outcome, 'already_terminal');
assert.equal((await acceptance.cancelWork({...jobs.newRequestIdentity(), key: 'js-never-submitted', historyEpoch: history.historyEpoch})).outcome, 'unknown');
done('RecoverableAcceptance.CancelWork');

// Display labels through the generated inventory client [JOB-A12]: a caller
// label is listed as sent, and the unlabelled model download carries the host
// the runtime derived from its path-less loopback URL.
const inventory = await bind('abstraction.job/inventory@1', jobs.JobInventoryClient, {maxFrame: 2097152});
const listedJobs = new Map();
for (let cursor = '', page; ; cursor = page.next) {
  page = await inventory.listWork(cursor, 64n);
  assert.equal(page.outcome, 'page');
  for (const s of page.snapshots) listedJobs.set(s.receipt.identity.key, s);
  if (page.complete) break;
}
assert.deepEqual([listedJobs.get('js-cancel').label, listedJobs.get('js-cancel').labelDerived], ['js fixture · blocked download', false]);
assert.deepEqual([listedJobs.get('js-model-download').label, listedJobs.get('js-model-download').labelDerived], [new URL(sourceURL).hostname, true]);
done('JobInventory.ListWork');

// Attempts, typed failure causes and unavailable decisions through the generated
// job client [JOB-A7, JOB-A8, JOB-A9].
const mismatched = {...request.newRequest(), artifact: {...request.newArtifact(), digest: 'sha256:' + '0'.repeat(64), size: BigInt(size)},
  sources: [{...request.newSource(), scheme: 'http', locator: sourceURL}]};
const original = await submit('js-retry', mismatched);
await until(async () => (await operations.observeWork(original)).snapshot.state === 'failed', 'digest mismatch ends the attempt');
const ended = (await operations.observeWork(original)).snapshot;
assert.equal(ended.failure.classification, 'permanent');
assert.equal(ended.failure.cause, 'digest_mismatch');
assert.equal(ended.receipt.identity.attempt, 0n);
const attempt = (n) => ({...jobs.newSubmission(), identity: {...original, attempt: n}, kind: 'download', spec: request.encode(mismatched)});
assert.equal((await acceptance.submit(attempt(3n))).outcome, 'invalid', 'a skipped attempt is ineligible');
const retried = await acceptance.submit(attempt(1n));
assert.equal(retried.outcome, 'accepted');
assert.equal(retried.receipt.identity.attempt, 1n);
assert.notEqual(retried.receipt.operationId, ended.receipt.operationId);
assert.equal((await acceptance.submit(attempt(1n))).receipt.operationId, retried.receipt.operationId, 'a duplicate attempt keeps its operation');
writeFileSync(jobPolicyFile, 'unavailable');
const outage = {...jobs.newSubmission(), identity: {...jobs.newRequestIdentity(), key: 'js-outage', historyEpoch: history.historyEpoch}, kind: 'download', spec: request.encode(lookup.request)};
const refusedByOutage = await acceptance.submit(outage);
assert.equal(refusedByOutage.outcome, 'unavailable');
assert.equal(refusedByOutage.receipt, null);
assert.equal((await operations.observeWork(original)).outcome, 'unavailable');
assert.equal((await acceptance.cancelWork(original)).outcome, 'unavailable');
writeFileSync(jobPolicyFile, '');
assert.equal((await acceptance.reconcile(outage.identity)).outcome, 'definitely_not_accepted', 'an outage recorded no acceptance');
assert.equal((await operations.observeWork(original)).outcome, 'observed');
done('RecoverableAcceptance.Submit attempts and unavailable');

const sink = await bind('abstraction.logging/sink@1', logging.SinkClient);
const observer = await bind('abstraction.logging/observer@1', logging.HistoryObserverClient);
const end = await observer.observe('', 1n, 65536n, 0n);
assert.equal(end.outcome, 'page');
assert.equal(end.atEnd, true);
assert.deepEqual(end.records, []);
const waiting = observer.observe(end.next, 1n, 65536n, 3000n);
await sleep(50);
await sink.write({...logging.newRecord(), schema: 1n, time: '2026-09-15T10:00:00.000000Z', level: 1n, msg: 'observed from JavaScript'});
const seen = await waiting;
assert.equal(seen.outcome, 'page');
assert.equal(seen.records.length, 1);
assert.equal(seen.records[0].msg, 'observed from JavaScript');
assert.equal((await observer.observe('', 0n, 65536n, 0n)).outcome, 'invalid_request');
// History policy refusals arrive as typed service errors with distinct codes.
writeFileSync(editPolicyFile, 'history-forbidden');
await assert.rejects(observer.observe('', 1n, 65536n, 0n), (e) => e instanceof logging.ServiceError && e.code === 'forbidden');
writeFileSync(editPolicyFile, 'history-unavailable');
await assert.rejects(observer.observe('', 1n, 65536n, 0n), (e) => e instanceof logging.ServiceError && e.code === 'policy_unavailable');
writeFileSync(editPolicyFile, 'permit');
assert.equal((await observer.observe('', 1n, 65536n, 0n)).outcome, 'page');
done('HistoryObserver.Observe');

const overrides = config.newRunOverrides();
const reader = await bind('abstraction.config/reader@1', config.ConfigReaderClient);
const editor = await bind('abstraction.config/editor@1', config.ConfigEditorClient);
const configObserver = await bind('abstraction.config/observer@1', config.ConfigObserverClient);
const initial = await reader.read(overrides);
assert.equal(typeof initial.stamp, 'string');
const user = await editor.readUser();
const values = {...user.values, store: 'js-choice', off: {...user.values.off, 'js-provider': 'paused'}};
const applied = await editor.replaceUser(user.revision, values);
assert.equal(applied.outcome, 'applied');
assert.equal(applied.snapshot.values.store, 'js-choice');
const stale = await editor.replaceUser(user.revision, user.values);
assert.equal(stale.outcome, 'conflict');
assert.equal(stale.snapshot.revision, applied.snapshot.revision);
await assert.rejects(editor.replaceUser('', values), (e) => e instanceof config.ServiceError && e.code === 'invalid_revision');
const effective = await reader.read(overrides);
assert.equal(effective.store, 'js-choice');
assert.equal(effective.off['js-provider'], 'paused');
const snapshot = await configObserver.observe(overrides, '', 0n);
assert.equal(snapshot.outcome, 'snapshot');
assert.equal(snapshot.snapshot.store, 'js-choice');
const later = await editor.replaceUser(applied.snapshot.revision, {...values, store: 'js-latest'});
assert.equal(later.outcome, 'applied');
const changed = await configObserver.observe(overrides, snapshot.cursor, 3000n);
assert.equal(changed.outcome, 'snapshot');
assert.equal(changed.snapshot.store, 'js-latest');
// The fixture's edit policy reads this file on every ReplaceUser.
const beforePolicy = await editor.readUser();
writeFileSync(editPolicyFile, 'forbidden');
const refusedEdit = await editor.replaceUser(beforePolicy.revision, {...beforePolicy.values, store: 'js-refused'});
assert.deepEqual([refusedEdit.outcome, refusedEdit.snapshot.revision, Object.keys(refusedEdit.snapshot.values.off).length, refusedEdit.snapshot.values.store], ['forbidden', '', 0, '']);
writeFileSync(editPolicyFile, 'unavailable');
assert.equal((await editor.replaceUser(beforePolicy.revision, {...beforePolicy.values, store: 'js-refused'})).outcome, 'unavailable');
assert.equal((await editor.readUser()).revision, beforePolicy.revision, 'refused edits changed settings');
writeFileSync(editPolicyFile, 'permit');
covered.push('ConfigReader.Read', 'ConfigEditor.ReadUser', 'ConfigEditor.ReplaceUser', 'ConfigObserver.Observe');

const action = 'fixture.read', resource = 'fixture-resource';
const decisions = await bind('abstraction.rights/authorization@1', rights.AuthorizationClient);
const policy = await bind('abstraction.rights/operator@1', rights.AuthorizationOperatorClient);
assert.equal((await decisions.decide(action, resource)).outcome, 'not_granted');
assert.equal((await decisions.decide('fixture.absent', resource)).outcome, 'unknown_action');
const page = await policy.listPolicy('', 64n);
assert.equal(page.outcome, 'page');
assert.deepEqual([...page.catalog].sort(), [readAction, writeAction, 'abstraction.storage/content.observe', action].sort());
assert.deepEqual(page.rules, []);
const subject = {...rights.newSubject(), account, program};
const rule = {...rights.newPolicyRule(), subject, action, resource, permit: true};
const grant = await policy.setRule(page.revision, rule);
assert.equal(grant.outcome, 'applied');
assert.deepEqual(grant.current, rule);
assert.equal((await policy.setRule(page.revision, rule)).outcome, 'conflict');
assert.equal((await decisions.decide(action, resource)).outcome, 'permitted');
assert.equal((await decisions.decideFor(subject, action, resource)).outcome, 'permitted');
assert.equal((await decisions.decideFor({...subject, program: program + '.other'}, action, resource)).outcome, 'not_granted');
const listed = await policy.listPolicy('', 64n);
assert.deepEqual(listed.rules, [rule]);
const revoked = await policy.revokeRule(grant.revision, subject, action, resource);
assert.equal(revoked.outcome, 'applied');
assert.equal((await decisions.decide(action, resource)).outcome, 'not_granted');
covered.push('Authorization.Decide', 'Authorization.DecideFor', 'AuthorizationOperator.ListPolicy', 'AuthorizationOperator.SetRule', 'AuthorizationOperator.RevokeRule');

// Content writer: the JavaScript rights operator grants the node program, the
// runtime's storage service enforces it, and a separate reader verifies bytes.
async function edit(change) {
  const current = await policy.listPolicy('', 64n);
  assert.equal(current.outcome, 'page');
  const result = await change(current.revision);
  assert.equal(result.outcome, 'applied');
}
const allow = (actionName, resourceName) => edit((revision) => policy.setRule(revision, {...rights.newPolicyRule(), subject, action: actionName, resource: resourceName, permit: true}));
const revoke = (actionName, resourceName) => edit((revision) => policy.revokeRule(revision, subject, actionName, resourceName));
const sha = (bytes) => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const writer = await bind('abstraction.storage/content-writer@1', storage.ContentWriterClient);
const contentReader = await bind('abstraction.storage/content-reader@1', storage.ContentReaderClient);
const limit = 1048576n, chunk = 65536;
const body = Uint8Array.from({length: 150000}, (_, i) => (i * 7 + 3) % 251);
const bodyDigest = sha(body);
const writeRequest = 'js-writer-' + randomBytes(8).toString('hex');
const denied = await writer.begin(writeRequest,bodyDigest, BigInt(body.length));
assert.deepEqual([denied.outcome, denied.upload, denied.stored, denied.limit], ['forbidden', null, null, 0n]);
await allow(readAction, bodyDigest);
assert.equal((await writer.begin(writeRequest,bodyDigest, BigInt(body.length))).outcome, 'forbidden', 'read grant admits no write');
await allow(writeAction, bodyDigest);
const begun = await writer.begin(writeRequest,bodyDigest, BigInt(body.length));
assert.equal(begun.outcome, 'started');
assert.equal(begun.limit, limit);
const upload = begun.upload;
assert.deepEqual([upload.digest, upload.size, upload.received], [bodyDigest, BigInt(body.length), 0n]);
const empty = await writer.append(upload.handle, 0n, new Uint8Array(0));
assert.deepEqual([empty.outcome, empty.received], ['invalid', 0n]);
let offset = 0;
while (offset < body.length) {
  const part = body.subarray(offset, Math.min(offset + chunk, body.length));
  const appended = await writer.append(upload.handle, BigInt(offset), part);
  assert.deepEqual([appended.outcome, appended.received], ['accepted', BigInt(offset + part.length)]);
  offset += part.length;
  if (offset === chunk) {
    const replay = await writer.append(upload.handle, 0n, body.subarray(0, 10));
    assert.deepEqual([replay.outcome, replay.received], ['out_of_order', BigInt(chunk)]);
    assert.equal((await contentReader.open(bodyDigest)).outcome, 'not_found', 'partial upload invisible');
    const resumed = await writer.begin(writeRequest,bodyDigest, BigInt(body.length));
    assert.deepEqual([resumed.outcome, resumed.upload.handle, resumed.upload.received], ['started', upload.handle, BigInt(chunk)]);
  }
}
const committed = await writer.commit(upload.handle);
assert.equal(committed.outcome, 'committed');
assert.deepEqual([committed.stored.digest, committed.stored.size, committed.stored.evidence, committed.received], [bodyDigest, BigInt(body.length), 'hashed', 0n]);
const duplicate = await writer.begin(writeRequest,bodyDigest, BigInt(body.length));
assert.deepEqual([duplicate.outcome, duplicate.stored.evidence, duplicate.stored.size], ['committed', 'hashed', BigInt(body.length)]);
const opened = await contentReader.open(bodyDigest);
assert.equal(opened.outcome, 'opened');
const parts = [];
for (let at = 0n; ;) {
  const read = await contentReader.read(opened.resource.handle, at, 65536n);
  assert.equal(read.outcome, 'data');
  parts.push(read.chunk.data);
  at += BigInt(read.chunk.data.length);
  if (read.chunk.eof) break;
}
assert.deepEqual(Buffer.concat(parts), Buffer.from(body), 'separate reader observes exact written bytes');
assert.equal((await contentReader.close(opened.resource.handle)).outcome, 'closed');

// Change observation: observe grant, per-object read filtering, gap and snapshot recovery.
{
const observeAction = 'abstraction.storage/content.observe', changesResource = 'abstraction.storage/changes';
const changes = await bind('abstraction.storage/content-changes@1', storage.ContentChangesClient);
const snapshot = async (limit) => {
  const objects = [];
  let continuation = '', cursor = '';
  for (;;) {
    const page = await changes.list(continuation, limit);
    assert.equal(page.outcome, 'page');
    assert.ok(!cursor || page.cursor === cursor, 'snapshot cursor stays fixed across pages');
    cursor = page.cursor;
    objects.push(...page.objects.map((o) => o.digest));
    if (page.complete) return {objects, cursor};
    continuation = page.continuation;
  }
};
const store = async (text, readable) => {
  const bytes = new TextEncoder().encode(text), digest = sha(bytes);
  await allow(writeAction, digest);
  if (readable) await allow(readAction, digest);
  const started = await writer.begin('js-changes-' + randomBytes(8).toString('hex'), digest, BigInt(bytes.length));
  assert.equal(started.outcome, 'started');
  assert.equal((await writer.append(started.upload.handle, 0n, bytes)).outcome, 'accepted');
  assert.equal((await writer.commit(started.upload.handle)).outcome, 'committed');
  return digest;
};
assert.deepEqual(Object.values(await changes.observe('', 16n, 0n)).slice(0, 2), ['forbidden', []]);
assert.deepEqual([(await changes.list('', 16n)).outcome, (await changes.list('', 16n)).cursor], ['forbidden', '']);
await allow(observeAction, changesResource);
let view = await snapshot(1n);
assert.ok(view.objects.includes(bodyDigest), 'snapshot lists the committed readable object');
const observedDigest = await store('javascript observed object', true);
let changed = await changes.observe(view.cursor, 16n, 5000n);
assert.equal(changed.outcome, 'page');
assert.deepEqual(changed.changes.map((c) => [c.kind, c.digest]), [['added', observedDigest]]);
const hiddenDigest = await store('javascript object without read grant', false);
const hidden = await changes.observe(changed.next, 16n, 300n);
assert.equal(hidden.outcome, 'page');
assert.ok(!hidden.changes.some((c) => c.digest === hiddenDigest), 'unreadable object reported');
assert.notEqual(hidden.next, changed.next, 'skipped change advanced the cursor');
const stale = hidden.next;
const burst = [];
for (let i = 0; i < 5; i++) burst.push(await store('javascript burst object ' + i, true));
const gap = await changes.observe(stale, 16n, 0n);
assert.deepEqual([gap.outcome, gap.changes.length, gap.next], ['gap', 0, stale]);
view = await snapshot(2n);
for (const d of [bodyDigest, observedDigest, ...burst]) assert.ok(view.objects.includes(d), 'snapshot recovers ' + d);
assert.ok(!view.objects.includes(hiddenDigest), 'snapshot filters unreadable objects');
const settled = await changes.observe(view.cursor, 16n, 0n);
assert.deepEqual([settled.outcome, settled.changes.length, settled.atEnd], ['page', 0, true]);
await revoke(observeAction, changesResource);
assert.equal((await changes.observe(view.cursor, 16n, 0n)).outcome, 'forbidden');
covered.push('ContentChanges.Observe', 'ContentChanges.List');
}
const other = new TextEncoder().encode('different content under one JavaScript request identity');
const otherDigest = sha(other);
await allow(writeAction, otherDigest);
assert.equal((await writer.begin(writeRequest,otherDigest, BigInt(other.length))).outcome, 'conflict');
const oversized = await writer.begin('js-oversize-' + randomBytes(8).toString('hex'), otherDigest, limit + 1n);
assert.deepEqual([oversized.outcome, oversized.limit], ['too_large', limit]);
const partialRequest = 'js-partial-' + randomBytes(8).toString('hex');
const partial = (await writer.begin(partialRequest, otherDigest, BigInt(other.length))).upload;
assert.equal((await writer.append(partial.handle, 0n, other.subarray(0, 10))).outcome, 'accepted');
const early = await writer.commit(partial.handle);
assert.deepEqual([early.outcome, early.stored, early.received], ['incomplete', null, 10n]);
const beyond = await writer.append(partial.handle, 10n, Uint8Array.from([...other.subarray(10), 120]));
assert.deepEqual([beyond.outcome, beyond.received], ['too_large', 10n]);
await allow(readAction, otherDigest);
assert.equal((await contentReader.open(otherDigest)).outcome, 'not_found');
await revoke(writeAction, otherDigest);
assert.deepEqual(Object.values(await writer.append(partial.handle, 10n, other.subarray(10))), ['forbidden', 0n]);
assert.deepEqual([(await writer.commit(partial.handle)).outcome], ['forbidden']);
assert.equal((await contentReader.open(otherDigest)).outcome, 'not_found', 'revoked commit invisible');
assert.equal((await writer.abort(partial.handle)).outcome, 'aborted');
assert.equal((await writer.abort(partial.handle)).outcome, 'gap');
assert.equal((await writer.begin(partialRequest, otherDigest, BigInt(other.length))).outcome, 'forbidden');
const negative = await writer.begin(writeRequest,bodyDigest, -1n);
assert.deepEqual([negative.outcome, negative.limit], ['invalid', 0n]);
covered.push('ContentWriter.Begin', 'ContentWriter.Append', 'ContentWriter.Commit', 'ContentWriter.Abort');

const questions = await bind('abstraction.asks/application@1', asks.QuestionApplicationClient);
const answers = await bind('abstraction.asks/operator@1', asks.QuestionOperatorClient);
const question = {...asks.newApplicationQuestion(), requestKey: 'js-question', key: 'download.reach', slots: {host: 'example.com'}};
const admitted = await questions.ask(question);
assert.equal(admitted.outcome, 'pending');
assert.ok(admitted.answer.id);
assert.equal((await questions.ask(question)).answer.id, admitted.answer.id);
assert.equal((await questions.ask({...question, slots: {host: 'other.example'}})).outcome, 'conflict');
assert.equal((await questions.observe(question.requestKey, 0n)).outcome, 'pending');
const book = await answers.listQuestions('', 8n);
assert.equal(book.outcome, 'page');
assert.deepEqual(book.records.map((record) => record.id), [admitted.answer.id]);
const decision = await answers.answerQuestion(admitted.answer.id, 'once');
assert.equal(decision.outcome, 'answered');
assert.equal(decision.record.option, 'once');
assert.deepEqual(await answers.answerQuestion(admitted.answer.id, 'once'), decision);
assert.equal((await answers.answerQuestion(admitted.answer.id, 'refuse')).outcome, 'conflict');
const answered = await questions.observe(question.requestKey, 0n);
assert.equal(answered.outcome, 'answered');
assert.equal(answered.answer.option, 'once');
const retiring = await questions.ask({...question, requestKey: 'js-retired', slots: {host: 'retired.example'}});
assert.equal(retiring.outcome, 'pending');
const retired = await answers.retireQuestion(retiring.answer.id);
assert.equal(retired.outcome, 'retired');
assert.equal(retired.record.id, retiring.answer.id);
assert.ok(!retired.record.option, 'pending retirement carries no decision');
const retiredReplay = await answers.retireQuestion(retiring.answer.id);
assert.equal(retiredReplay.outcome, 'retired');
assert.ok(retiredReplay.record == null, 'retirement replay carries no record');
assert.equal((await questions.observe('js-retired', 0n)).outcome, 'gone');
assert.equal((await questions.ask({...question, requestKey: 'js-retired', slots: {host: 'retired.example'}})).outcome, 'gone');
assert.equal((await answers.retireQuestion('js-never-admitted')).outcome, 'unknown');
assert.equal((await questions.observe(question.requestKey, 0n)).outcome, 'answered', 'retiring another question kept this answer');
covered.push('QuestionApplication.Ask', 'QuestionApplication.Observe', 'QuestionOperator.ListQuestions', 'QuestionOperator.AnswerQuestion', 'QuestionOperator.RetireQuestion');

console.log(`PASS ${process.env.OA_JS_CONNECTOR === 'native' ? 'native' : 'test pipe'} connector: ${covered.length} generated methods against the production Go runtime: ${covered.join(', ')}`);
