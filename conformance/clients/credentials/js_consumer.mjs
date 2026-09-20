// A JavaScript application resolves the credentials holder and applier through
// the pure facade and the generated clients, over the test-only pipe connector.
// It holds no holder.read rule and is no designated enforcer: both calls read
// forbidden, and no reply carries a secret.
import assert from 'node:assert/strict';
import {Machine} from '../../../openabstractions-flat/abstraction-facade/javascript/index.js';
import {HolderClient, ApplierClient, PageOutcome, ApplyOutcome, newUse} from '../../../openabstractions-flat/abstraction-credentials/javascript/js/abstraction/credentials/api/index.mjs';
import {PipeTestConnector} from '../js_services/pipe_connector.mjs';

const [endpoint, account, ...secrets] = process.argv.slice(2);
if (!endpoint || !account) {
  console.log('js_consumer.mjs <runtime endpoint> <account> [secret ...]');
  process.exit(endpoint === '--help' ? 0 : 2);
}
const machine = new Machine(endpoint, {connector: new PipeTestConnector(), timeout: 10000});
const holder = (await machine.resolveService('abstraction.credentials/holder@1', {scope: 'local'})).client(HolderClient);
const applier = (await machine.resolveService('abstraction.credentials/applier@1', {scope: 'local'})).client(ApplierClient);
const page = await holder.list('', 64n);
assert.equal(page.outcome, PageOutcome.Forbidden);
assert.equal(page.records.length, 0);
const usage = {...newUse(), subject: {account, program: process.execPath}, consumer: 'abstraction.download/http-execution@1', name: 'hf', target: 'huggingface.co'};
const applied = await applier.apply(usage);
assert.equal(applied.outcome, ApplyOutcome.Forbidden);
assert.equal(Object.keys(applied.headers ?? {}).length, 0);
const replies = JSON.stringify([page, applied], (_, v) => typeof v === 'bigint' ? v.toString() : v);
for (const secret of secrets) assert.ok(!replies.includes(secret), 'a reply carries a secret');
console.log(`PASS javascript: list=${page.outcome} apply=${applied.outcome}`);
