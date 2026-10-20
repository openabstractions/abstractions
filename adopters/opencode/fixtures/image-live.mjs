// Opt-in HP4 probe: run the actual OpenCode plugin tool against one OA runtime.
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import OpenAbstractionsPlugin from '../plugin.js';

if (process.argv.includes('--help')) {
  process.stdout.write('Set OA_HP4_IMAGE_ARGS JSON {model or selectionPolicy,prompt,size} and ABSTRACTION_RUNTIME_ENDPOINT, then run with Bun. This is a standalone helper.\n');
  process.exit(0);
}
const args = JSON.parse(process.env.OA_HP4_IMAGE_ARGS || 'null');
assert.ok(args && typeof args === 'object', 'OA_HP4_IMAGE_ARGS is required');
let providerPin = '';
if (args.selectionPolicy) {
  const [{Machine}, {SelectionOperatorV2Client}, {BunNativeConnector}] = await Promise.all([
    import('@openabstractions/facade'), import('@openabstractions/inference/protocol'),
    import('@openabstractions/ipc/bun'),
  ]);
  const machine = new Machine(null, {connector: new BunNativeConnector(), timeout: 5000});
  const binding = await machine.resolveService('abstraction.inference/selection-operator@2', {scope: 'local'});
  const policy = await new SelectionOperatorV2Client(binding).read('', args.selectionPolicy);
  assert.equal(policy.outcome, 'found');
  assert.equal(policy.policy?.candidates?.length, 1, 'live shared-provider probe requires one pinned candidate');
  providerPin = policy.policy.candidates[0].provider;
  assert.ok(providerPin, 'live shared-provider probe requires a logical provider pin');
}
const schemaValue = () => ({optional() { return this; }});
const tool = Object.assign((definition) => definition, {schema: {
  string: schemaValue, number: schemaValue, boolean: schemaValue,
  enum: schemaValue, array: schemaValue, object: schemaValue,
}});
const plugin = await OpenAbstractionsPlugin({client: {}}, {}, {
  tool,
  readInventory: async () => ({offers: []}),
});
await plugin.config({provider: {openabstractions: {
  npm: '@openabstractions/opencode',
  options: {scope: 'local'},
  models: {},
}}});
const result = await plugin.tool.openabstractions_generate_image.execute(args, {
  ask: async () => {}, abort: new AbortController().signal,
});
assert.equal(typeof result, 'object', String(result));
assert.equal(result.attachments?.length, 1);
const attachment = result.attachments[0];
const prefix = `data:${attachment.mime};base64,`;
assert.ok(attachment.url.startsWith(prefix));
const bytes = Buffer.from(attachment.url.slice(prefix.length), 'base64');
const digest = `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
assert.equal(digest, result.metadata.digest);
process.stdout.write(JSON.stringify({caller: 'standalone-bun-plugin-helper', host: result.metadata.host,
  model: result.metadata.model, operation: result.metadata.operation,
  providerPin, digest, mediaType: attachment.mime, bytes: bytes.length}) + '\n');
