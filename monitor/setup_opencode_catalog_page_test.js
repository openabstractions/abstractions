const assert = require('assert');
const h = require('./testdata/setup_page_test_helper.js');

(async () => {
  let outcome;
  const catalog = JSON.parse(JSON.stringify(h.catalog));
  const step = 'desktop-integration';
  const integration = '/setup/desktop-integration?action=';
  const over = h.ownerOverview({desktop_integration: {app: 'opencode', coding: true, installed: true, action: 'install'}});
  const p = await h.page((path, body) => {
    if (path === '/setup/catalog') return catalog;
    if (path === '/setup/overview') return over;
    if (path === '/setup/state') return h.ownerState({catalog, outcomes: outcome ? {[step]: outcome} : {}});
    if (path === '/setup/other-copies') return {copies: []};
    if (path === '/setup/modelhost/coding') return {status: {plan: over.coding_model, assets: []}};
    if (path === '/setup/modelhost/chat') return {available: true, feature: {installed: true}, engine: {staged: true}, pin: {}};
    if (path === '/setup/install') return {current_catalog: true, approved: false, install: {}};
    if (path === '/setup/plan') return h.planFor(body, ['pytorch-engine', 'thenoise-engine']);
    if (path.startsWith(integration)) {
      const action = path.slice(integration.length);
      outcome = {step_id: 'step-exact', kind: action};
      if (action === 'update') over.desktop_integration.action = 'repair';
      return {step_id: 'step-exact', actions: {}, outcomes: {[step]: outcome}};
    }
  });
  const row = p.parts('opencode');
  const button = row.details.children[0].children.find(node => node.id === 'setup-action-opencode');
  assert.strictEqual(row.box, null);
  assert.strictEqual(button.textContent, 'Repair OpenCode connection');
  assert.strictEqual(button.disabled, false);
  await button.onclick();
  await h.settled();
  assert.deepStrictEqual(p.posts(integration + 'install').map(r => r.body), [{confirm: true}]);
  assert.strictEqual(p.requests.some(r => r.path === '/setup/maintenance'), false);
  assert.strictEqual(p.parts('opencode').line, 'Repair finished. Open OpenCode to use the integration.');
  over.desktop_integration.action = 'update';
  await p.context.setupReload();
  assert.strictEqual(button.textContent, 'Update OpenCode connection');
  assert.strictEqual(p.parts('opencode').line, 'Close OpenCode before updating its connection.');
  assert.strictEqual(p.posts(integration + 'update').length, 0, 'an available update waits for the owner’s click');
  await button.onclick();
  await h.settled();
  assert.deepStrictEqual(p.posts(integration + 'update').map(r => r.body), [{confirm: true}]);
  assert.strictEqual(p.parts('opencode').line, 'Update finished. Open OpenCode to use the integration.');
  over.desktop_integration = {app: 'opencode', coding: true, installed: false, reason: 'This build can integrate an existing OpenCode Desktop installation.'};
  await p.context.setupReload();
  assert.strictEqual(button.hidden, true);
  assert.match(p.parts('opencode').line, /existing OpenCode Desktop/);
  // A saved install from an earlier release that ticked OpenCode runs no
  // installer step: its steps start at the coding model.
  const record = {plan_id: 'stopped-plan', opencode: true, coding_model_plan_id: 'coding-plan'};
  assert.deepStrictEqual([...p.context.setupInstallSteps(record, {selected: []})], ['coding']);
  assert.strictEqual(p.requests.some(r => r.path === '/setup/maintenance'), false);

  // Change the published identity. The legacy choice remains in the
  // Catalog, and the new integrated choice is advertised as available.
  catalog.choices.push({id: 'editor-desktop', title: 'Example Desktop', application: true, components: ['editor-extension']});
  catalog.components.push({...catalog.components.find(c => c.id === 'thenoise-engine'), id: 'editor-extension'});
  over.offered.push('editor-desktop');
  outcome = undefined;
  over.desktop_integration = {app: 'editor-desktop', installed: true, action: 'repair'};
  await p.context.setupReload();
  assert.deepStrictEqual(p.rowIDs('setup-app-rows'), ['setup-row-comfyui', 'setup-row-editor-desktop']);
  const editor = p.parts('editor-desktop');
  assert.strictEqual(editor.title, 'Example Desktop');
  assert.strictEqual(editor.box, null);
  const repair = editor.details.children[0].children.find(node => node.id === 'setup-action-editor-desktop');
  assert.strictEqual(repair.textContent, 'Repair Example Desktop connection');
  assert.strictEqual(editor.caption, '');
  over.coding_model.installed = false;
  await p.context.setupReload();
  assert.strictEqual(p.parts('coding').box.checked, false, 'an unrelated desktop app does not recommend a coding model');
  over.desktop_integration.coding = true;
  await p.context.setupReload();
  assert.strictEqual(p.parts('editor-desktop').caption, 'Coding assistant');
  assert.strictEqual(p.parts('coding').box.checked, true);
  assert.match(p.summary().sentence, /the coding model Example Desktop lacks/);
  over.coding_model.installed = true;
  await p.context.setupReload();
  await button.onclick();
  assert.strictEqual(p.requests.filter(r => r.path.startsWith(integration)).length, 2, 'a stale row cannot invoke repair for the new app');
  await p.tick('model:klein-compact');
  const plan = p.posts('/setup/plan').at(-1);
  assert(plan);
  assert(!plan.body.selected.some(id => id === 'opencode' || id === 'editor-desktop'));

  delete over.desktop_integration;
  await p.context.setupReload();
  assert.deepStrictEqual(p.rowIDs('setup-app-rows'), ['setup-row-comfyui']);
  await repair.onclick();
  assert.strictEqual(p.requests.filter(r => r.path.startsWith(integration)).length, 2, 'a removed row cannot invoke repair');
  console.log('setup OpenCode Catalog page: ok');
})().catch(error => { console.error(error); process.exitCode = 1; });
