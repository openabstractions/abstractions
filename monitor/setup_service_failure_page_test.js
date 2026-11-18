// A Catalog row whose step stopped because OpenAbstractions could not be
// reached says so in one plain sentence with a Retry button; the service
// resolution chain stays in Technical details (owner's PC, 2026-11-22).
const assert = require('assert');
const h = require('./testdata/setup_page_test_helper.js');

const raw = 'downloads are unavailable: service resolution: runtime_unavailable: abstraction.job/acceptance@1 (capability abstraction.job) at the installed runtime: bootstrap: multiple runtime installations require explicit selection';
const twice = 'OpenAbstractions is installed twice on this PC. Remove one copy in Windows Settings, then retry.';
const down = 'OpenAbstractions isn\'t running right now. Retry in a moment.';

const text = node => node ? (node.textContent || '') + (node.children || []).map(text).join('') : '';

(async () => {
  let outcome;
  const catalog = JSON.parse(JSON.stringify(h.catalog));
  const step = 'desktop-integration';
  const over = h.ownerOverview({desktop_integration: {app: 'opencode', coding: true, installed: true, action: 'repair'}});
  const p = await h.page((path, body) => {
    if (path === '/setup/catalog') return catalog;
    if (path === '/setup/overview') return over;
    if (path === '/setup/state') return h.ownerState({catalog, outcomes: outcome ? {[step]: outcome} : {}});
    if (path === '/setup/other-copies') return {copies: []};
    if (path === '/setup/modelhost/coding') return {status: {plan: over.coding_model, assets: []}};
    if (path === '/setup/modelhost/chat') return {available: true, feature: {installed: true}, engine: {staged: true}, pin: {}};
    if (path === '/setup/install') return {current_catalog: true, approved: false, install: {}};
  });
  for (const [failure, detail, expected] of [
    [raw, raw, twice],
    ['service resolution: runtime_unavailable: abstraction.job/acceptance@1 (capability abstraction.job) at the installed runtime: dial: refused', 'service resolution: runtime_unavailable: dial: refused', down],
    [twice, raw, twice],
  ]) {
    outcome = {step_id: 'step-1', kind: 'repair', ended_at: '2026-11-22T13:44:00Z', failure, detail};
    await p.context.setupReload();
    const row = p.parts('opencode');
    assert.strictEqual(row.line, expected);
    assert.strictEqual(row.lineState, 'error');
    const button = row.details.children[0].children.find(node => node.id === 'setup-action-opencode');
    assert.strictEqual(button.textContent, 'Retry');
    assert.strictEqual(button.hidden, false);
    const technical = text(row.details);
    assert(technical.includes('runtime_unavailable') || technical.includes('multiple runtime installations'), 'the raw chain is in Technical details: ' + technical);
    assert(!row.line.includes('runtime_unavailable') && !row.line.includes('abstraction.job'), 'identifiers stay out of the caption');
  }
  assert.strictEqual(p.context.setupReason(raw), twice);
  console.log('setup service failure page: ok');
})().catch(error => { console.error(error); process.exitCode = 1; });
