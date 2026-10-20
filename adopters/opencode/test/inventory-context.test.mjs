// The router owns the inventory context rule (router CONTRACT.md ROUTE-D8);
// inventory.js projects its Offer answer onto OpenCode models. These
// projection cases are shared with test/configure.test.mjs, which
// holds configure.js to the same table.
import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {servableChatModels, refreshProviderInventory} from '../inventory.js';

const table = JSON.parse(readFileSync(new URL('./fixtures/inventory-context.json', import.meta.url), 'utf8'));

// offers converts the table's offers into the router binding's shape: token
// counts are BigInt and absent fields take the wire defaults.
function offers(rows) {
  return rows.map((row) => ({family: row.family, names: row.names, servers: row.servers,
    context: BigInt(row.context), contextSource: row.contextSource, input: BigInt(row.input), unknown: row.unknown ?? []}));
}

for (const c of table.cases) {
  test(`inventory projection: ${c.name}`, () => {
    const diagnostics = [];
    const observed = {};
    const isLocal = c.localServers ? (server) => c.localServers.includes(server) : undefined;
    const models = servableChatModels(offers(c.offers), diagnostics, observed, isLocal);
    assert.deepEqual(models, c.models);
    assert.deepEqual(observed, c.observed);
    assert.deepEqual(diagnostics.map((item) => item.model).sort(), [...c.withheld].sort());
  });
}

test('the startup refresh warns once about the stale limit configure preserved', () => {
  const c = table.configure;
  const provider = structuredClone(c.configured);
  const result = refreshProviderInventory(provider, offers(c.offers));
  assert.deepEqual(result.warnings.map(({model, configured, observed}) => ({model, configured, observed})), c.warnings);
  assert.deepEqual(provider.models.qwen, c.configured.models.qwen);
});
