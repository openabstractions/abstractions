// Run with node monitor/setup_page_test.js. The one button's work on the
// Setup page (architect rulings 2026-11-08 and 2026-11-09): the click
// approves the summary's plan, its picture models and permissions together;
// every later step runs on its own and shows its progress on the row it
// changes; a failure names its row in one sentence with Try again, the raw
// error going to the log; an unfinished install shows as stopped when the
// page opens and continues only on the person's Install (owner, 2026-11-09).
// ComfyUI's row has no tick (owner, 2026-11-11): Get ComfyUI, Connect and
// Open, and the OA build an earlier release installed is removed from its
// own row.
const assert = require('assert');
const h = require('./testdata/setup_page_test_helper.js');
const panelRules = require('./static/panel_rules.js');
const {allowlistedClasses} = require('./panel_fake_dom_test_helper.js');
const {GB} = h;
let done = false;
process.on('exit', c => { if (!c && !done) { console.error('setup page: did not finish'); process.exitCode = 1; } });
assert.deepStrictEqual(allowlistedClasses('setup'), [], 'Setup has no allowlisted page-local class');

// server is an in-memory Setup service for the owner's computer: the
// packages are installed, the picture model is prepared by its model
// endpoints, and every call is recorded. s.over changes the overview a test
// reads next.
function server(options = {}) {
  const s = {state: h.ownerState(), record: null, configured: new Set(), approved: new Set(), assets: {}, calls: [], granted: false, registered: false, over: {}};
  const modelReply = () => {
    const status = id => s.approved.has(id) ? {approved: true, provider_configured: s.configured.has(id), assets: [{id: 'main', phase: s.assets[id] || 'pending'}]} : {approved: false, assets: []};
    return {engine: 'pytorch', enabled: true, available: true, setup_plan_id: s.state.plan.id, plan: {id: 'klein-compact'}, status: status('klein-compact'), choice_status: {'klein-full': status('klein-full')}};
  };
  s.handle = (path, body) => {
    s.calls.push(path + (body ? ' ' + JSON.stringify(body) : ''));
    if (path === '/setup/catalog') return h.catalog;
    if (path === '/setup/state') return s.state;
    if (path === '/setup/overview') return h.ownerOverview({...(options.overview || {}), ...s.over});
    if (path === '/setup/other-copies') return {copies: []};
    if (path.startsWith('/setup/feature')) return {installed: true, available: true};
    if (path === '/setup/modelhost/coding') return {status: {plan: h.overview().coding_model, assets: []}};
    if (path === '/setup/modelhost/chat') return {available: true, feature: {installed: true}, engine: {staged: true}, pin: {}};
    if (path === '/setup/plan') return h.planFor(body, ['pytorch-engine', 'thenoise-engine']);
    if (path === '/setup/install' && !body) return {...s.state, install: s.record || {}, model_engine: 'pytorch'};
    if (path === '/setup/install') { s.record = {plan_id: body.plan_id, model_plan_id: body.model_plan_id, picture_model_plan_ids: body.picture_model_plan_ids.filter(id => id !== body.model_plan_id), rules: [{action: 'a', resource: 'r'}]}; s.state = {...s.state, plan: {...s.state.plan, id: body.plan_id, selected: body.selected}}; return {...s.state, install: s.record, model_engine: 'pytorch'}; }
    if (path === '/setup/advance-next') return {...s.state, advanced: ''};
    if (path.startsWith('/setup/model?')) return modelReply();
    if (path === '/setup/model/start') { if (options.startFails) return new Error('model start: open C:\\Users\\x\\journal.json: access denied'); s.approved.add(body.plan_id); return modelReply(); }
    if (path === '/setup/model/advance') { s.assets[body.plan_id] = 'verified'; return modelReply(); }
    if (path === '/setup/model/configure') { s.configured.add(body.plan_id); return modelReply(); }
    if (path === '/setup/install/grants') { s.granted = true; return {...s.record, granted: true}; }
    if (path === '/setup/settle-removal') { s.state = {...s.state, components: s.state.components.map(r => r.id === body.component_id ? {...r, effect: {...r.effect, phase: 'installed', removing: false}} : r)}; return s.state; }
    if (path === '/setup/step') return {ready: true, action: 'Check'};
    if (path === '/setup/install/cancel') { s.record = {...s.record, cancelled: true}; return {}; }
    if (path === '/setup/install/continue') { if (!s.record || body.setup_plan_id !== s.record.plan_id) return new Error('the approved installation changed'); const {interrupted, failure, ...rest} = s.record; s.record = rest; return {...s.state, install: s.record, model_engine: 'pytorch'}; }
    if (path === '/setup/install/failed') { const {setup_plan_id, ...failure} = body; s.record = {...s.record, failure}; return s.record; }
    return undefined;
  };
  return s;
}

// action is a ComfyUI row's one button.
const action = (p, id) => p.row(id).children[0].children.find(c => c.tagName === 'BUTTON' && c.id === 'setup-action-' + id);
const press = async (button) => { await button.onclick({preventDefault() {}}); for (let i = 0; i < 3; i++) await h.settled(); };
const rulesHold = p => { const root = h.node('main'); root.children = h.lists.map(id => { const section = h.node('section'); section.children = [p.get(id)]; return section; }).concat([p.get('setup-summary-row')]); panelRules.assertPanelRules(root, 'Setup', {allowClasses: []}); };
const later = ms => new Date(Date.now() + ms).toISOString();

(async () => {
  // The page displays and executes the picture adapter's own declaration.
  {
    const facts = h.comfy({
      id: 'picture-app', title: 'Picture App', state: 'connect', oa_build: true,
      retained: {id: 'earlier-picture', component: 'earlier-package', title: 'Earlier Pictures', removal_title: 'the earlier picture package'},
      connect_step: 'picture-connect',
      actions: {connect: '/setup/pictures-example/connect', 'remove-oa': '/setup/pictures-example/remove'},
    });
    const s = server({overview: {picture_application: facts, picture_models: [
      {...h.zimage(), required_application: 'picture-app'},
      {...h.full(), required_application: 'another-app'},
    ]}});
    const p = await h.page((path, body) => path.startsWith('/setup/pictures-example/') ? {actions: {}, outcomes: {}} : s.handle(path, body));
    assert.deepStrictEqual(p.rowIDs('setup-app-rows'), ['setup-row-picture-app', 'setup-row-earlier-picture', 'setup-row-opencode']);
    assert.strictEqual(p.parts('earlier-picture').title, 'Earlier Pictures');
    assert.strictEqual(action(p, 'earlier-picture').textContent, 'Remove the earlier picture package');
    assert.strictEqual(require('node:vm').runInContext("setupComponentRow('earlier-package')", p.context), 'earlier-picture');
    assert.strictEqual(require('node:vm').runInContext("setupComponentRow('picture-connect')", p.context), 'picture-app');
    assert(h.facts(p.parts('model:z-image').body).includes('Used by: Picture App'));
    assert(!h.facts(p.parts('model:klein-full').body).some(f => f.startsWith('Used by')));
    await press(action(p, 'picture-app'));
    await press(action(p, 'earlier-picture'));
    assert.deepStrictEqual(p.posts('/setup/pictures-example/connect').map(r => r.body), [{confirm: true}]);
    assert.deepStrictEqual(p.posts('/setup/pictures-example/remove').map(r => r.body), [{confirm: true}]);
    facts.actions.connect = '//external.example/setup/connect';
    await p.context.setupReload();
    await press(action(p, 'picture-app'));
    assert(!p.requests.some(r => r.path === facts.actions.connect), 'picture actions stay within the same-build Setup paths');
  }

  // A failed discovery read must neither add picture work to a desktop
  // install nor hide the workspace of an approved historical picture plan.
  {
    const s = server();
    const plan = {id: 'historical-picture-plan', selected: ['comfyui']};
    const p = await h.page((path, body) => {
      if (path === '/setup/overview') return Error('overview temporarily unavailable');
      if (path === '/setup/install' && !body) {
        return {...s.state, plan, install: {plan_id: plan.id}, workspace: 'no_shortcut'};
      }
      if (path === '/setup/comfy/status') return {registered: true, matching: true, shortcut: false, application: 'comfyui'};
      if (path === '/setup/comfy/register') return {registered: true, matching: true, shortcut: true, application: 'comfyui'};
      return s.handle(path, body);
    });
    assert.deepStrictEqual(Array.from(p.context.setupInstallSteps({}, {selected: ['opencode']})), ['apps']);
    assert.deepStrictEqual(Array.from(p.context.setupInstallSteps({}, plan)), ['feature', 'apps', 'workspace']);
    assert.strictEqual(p.summary().facts, 'The ComfyUI shortcut is missing.');
    await p.click();
    assert.deepStrictEqual(p.posts('/setup/comfy/register').map(r => r.body), [{setup_plan_id: plan.id, confirm: true}]);
  }

  // One click: the owner's computer gets two picture models; the steps run
  // in order with no further click and end in the result sentence.
  {
    const s = server();
    const p = await h.page(s.handle);
    await p.tick('model:klein-compact');
    await p.tick('model:klein-full');
    assert.strictEqual(p.parts('pytorch').box.getAttribute('aria-label'), 'Diffusers', 'each checkbox is named by its row title');
    assert.strictEqual(p.summary().button, 'Install');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    const install = p.posts('/setup/install').at(-1).body;
    assert.deepStrictEqual(install, {selected: ['pytorch', 'thenoise'], coding_model_plan_id: '', picture_model_plan_ids: ['klein-compact', 'klein-full'], plan_id: 'plan-pytorch-thenoise', model_plan_id: 'klein-compact', permissions_id: '', confirm: true},
      'the click approves the summary: its plan and both models');
    const starts = p.posts('/setup/model/start').map(r => r.body.plan_id);
    assert.deepStrictEqual(starts, ['klein-compact', 'klein-full'], 'each ticked model is prepared, the recommended one first');
    assert.deepStrictEqual(p.posts('/setup/model/configure').map(r => r.body.plan_id), ['klein-compact', 'klein-full']);
    assert.strictEqual(p.posts('/setup/comfy/register').length, 0, 'nothing is added to Apps');
    assert.strictEqual(p.summary().sentence, 'Changes applied.');
    assert.strictEqual(p.summary().button, '');
    // Every rule, the class contract included, on the rendered rows.
    rulesHold(p);
  }

  // A step that fails names its row: one sentence on the summary and the
  // row, the raw error in the log only; Try again resumes.
  {
    const s = server({startFails: true});
    const p = await h.page(s.handle);
    await p.tick('model:klein-compact');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.deepStrictEqual(p.summary(), {sentence: 'FLUX.2 Klein 4B compact did not install.', facts: '', button: 'Try again', disabled: false});
    assert.strictEqual(p.parts('model:klein-compact').line, 'The download stopped.');
    assert.strictEqual(p.parts('model:klein-compact').lineState, 'error');
    const notice = p.notices.find(n => n.kind === 'failed');
    assert(notice && notice.detail.includes('access denied'), 'the raw error is logged');
    for (const id of ['comfyui', 'model:klein-compact', 'pytorch']) assert(!p.parts(id).line.includes('\\'), 'no raw error text on a row');
    // The install record keeps the failed component and its reason, so a
    // reloaded page names them again instead of a bare "stopped".
    const kept = p.posts('/setup/install/failed').map(r => r.body);
    assert.strictEqual(kept.length, 1);
    assert.strictEqual(kept[0].component, 'model:klein-compact');
    assert.strictEqual(kept[0].sentence, 'FLUX.2 Klein 4B compact did not install.');
    assert(kept[0].detail.includes('access denied'));
    const reloaded = await h.page(s.handle);
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(reloaded.summary().sentence, 'FLUX.2 Klein 4B compact did not install.', 'the reason survives a reload');
    assert.strictEqual(reloaded.summary().button, 'Install');
  }

  // Owner, 2026-11-09: after an update the page opens on the install the
  // update stopped. It starts nothing; Install continues the same approval
  // and its verified file is prepared with no second download.
  {
    const s = server();
    s.record = {plan_id: 'plan-owner', model_plan_id: 'klein-compact', picture_model_plan_ids: [], rules: [{action: 'a', resource: 'r'}], interrupted: 'update'};
    s.approved.add('klein-compact'); s.assets['klein-compact'] = 'verified';
    const p = await h.page(s.handle);
    for (let i = 0; i < 5; i++) await h.settled();
    const work = ['/setup/install', '/setup/install/continue', '/setup/advance-next', '/setup/model/start', '/setup/model/advance', '/setup/model/configure', '/setup/install/grants', '/setup/retry', '/setup/recover'];
    for (const path of work) assert.strictEqual(p.posts(path).length, 0, 'opening the page posts nothing to ' + path);
    assert.deepStrictEqual(p.summary(), {sentence: 'Finish installing FLUX.2 Klein 4B compact.', facts: 'Downloaded model files are kept.', button: 'Install', disabled: false});
    assert.strictEqual(p.parts('model:klein-compact').line, 'Stopped');
    assert.strictEqual(p.parts('model:klein-compact').bar.hidden, true, 'a stopped row shows no progress bar');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.deepStrictEqual(p.posts('/setup/install/continue').map(r => r.body), [{setup_plan_id: 'plan-owner'}], 'Install continues the stopped approval');
    assert.strictEqual(p.posts('/setup/install').length, 0, 'no second approval');
    assert.strictEqual(p.posts('/setup/model/start').length + p.posts('/setup/model/advance').length, 0, 'the verified file is not downloaded again');
    assert.deepStrictEqual(p.posts('/setup/model/configure').map(r => r.body.plan_id), ['klein-compact']);
    assert.strictEqual(p.summary().sentence, 'Changes applied.');
  }

  // A page closed during the install opens on the same stopped install,
  // in its own words.
  {
    const s = server();
    s.record = {plan_id: 'plan-owner', model_plan_id: 'klein-compact', picture_model_plan_ids: [], rules: [{action: 'a', resource: 'r'}]};
    const p = await h.page(s.handle);
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.summary().sentence, 'Finish installing FLUX.2 Klein 4B compact.');
    assert.strictEqual(p.summary().button, 'Install');
    assert.strictEqual(p.posts('/setup/model/start').length, 0, 'opening the page starts nothing');
  }

  // A finished install's record does not resume: the page arrives idle.
  {
    const s = server({overview: {picture_models: [{...h.compact(), prepared: true}, h.full(), h.zimage()]}});
    s.record = {plan_id: 'plan-owner', model_plan_id: 'klein-compact', picture_model_plan_ids: [], rules: [{action: 'a', resource: 'r'}], granted: true};
    const p = await h.page(s.handle);
    assert.strictEqual(p.posts('/setup/model/start').length, 0);
    assert.strictEqual(p.summary().sentence, 'Tick to install. Clear a tick to remove.');
  }

  // ComfyUI's row (owner, 2026-11-11): no tick, one line and one button in
  // each state; the rendered rows keep every Panel rule.
  for (const [facts, line, button] of [
    [{state: 'get'}, 'Not installed', 'Get ComfyUI'],
    [{state: 'finish', kind: 'comfy-desktop', open: true}, 'Open Comfy Desktop to finish its setup', 'Open'],
    [{state: 'connect', kind: 'desktop', folder: 'C:\\Users\\x\\Documents\\ComfyUI'}, 'Installed · not connected to OpenAbstractions', 'Connect'],
    [{state: 'connected', kind: 'desktop', folder: 'C:\\Users\\x\\Documents\\ComfyUI', open: true}, 'Installed · connected to OpenAbstractions', 'Open'],
    [{state: 'connected', kind: 'desktop', folder: 'C:\\Users\\x\\Documents\\ComfyUI', open: true, update: true}, 'Installed · Update ready', 'Update'],
    [{state: 'connected', kind: 'desktop', open: true, update: true, update_blocked: "this OpenAbstractions build doesn't include the integration update"}, "Installed · update unavailable: this OpenAbstractions build doesn't include the integration update", 'Open'],
    [{state: 'connected', kind: 'desktop', open: true, restart: true}, 'Installed · restart ComfyUI to use the updated integration', 'Open'],
    [{state: 'connected', kind: 'desktop', open: true, changed: true}, 'Installed · Connect again to use it', 'Connect']]) {
    // A prepared model keeps the recommended one clear, so the summary
    // reflects the app row alone.
    const s = server({overview: {picture_application: h.comfy(facts), picture_models: [{...h.compact(), prepared: true}, h.full(), h.zimage()]}});
    const p = await h.page(s.handle);
    const row = p.parts('comfyui');
    assert.deepStrictEqual([row.title, row.caption, row.line, row.box], ['ComfyUI', 'Create pictures', line, null], facts.state);
    const buttons = p.row('comfyui').children[0].children.filter(c => c.tagName === 'BUTTON' && !c.hidden);
    assert.deepStrictEqual(buttons.map(b => [b.textContent, b.disabled]), [[button, false]], 'one button: ' + facts.state);
    if (facts.folder) assert(h.allText(row.body).includes('Technical details   Folder: ' + facts.folder), h.allText(row.body));
    assert.strictEqual(p.summary().sentence, 'Tick to install. Clear a tick to remove.', 'the row changes nothing the summary applies');
    rulesHold(p);
  }

  // Overview names the live row while the retained build keeps its archive identity.
  {
    const opened = [];
    const s = server({overview: {picture_application: h.comfy({id: 'picture-fixture', title: 'Picture Fixture', oa_build: true,
      download_page: 'https://stale.example/download'})}});
    const p = await h.page(s.handle, {open: (url, target) => opened.push([url, target]),
      oaPictureWorkspace: {download: 'https://approved.example/download'}});
    assert(!p.row('comfyui'));
    assert.strictEqual(p.parts('picture-fixture').title, 'Picture Fixture');
    assert.strictEqual(action(p, 'picture-fixture').textContent, 'Get Picture Fixture');
    assert.strictEqual(p.parts('comfyui-oa').title, 'ComfyUI (previous version)');
    await press(action(p, 'picture-fixture'));
    assert.deepStrictEqual(opened, [['https://approved.example/download', '_blank']]);
    s.over = {picture_application: h.comfy({id: 'picture-fixture', title: 'Picture Fixture', state: 'connected', open: true})};
    const connected = await h.page(s.handle);
    await press(action(connected, 'picture-fixture'));
    assert.deepStrictEqual(connected.posts('/setup/comfy/open').map(r => r.body), [{confirm: true}]);
  }

  // Get ComfyUI opens the official download page and posts nothing.
  {
    const opened = [];
    const s = server();
    const p = await h.page(s.handle, {open: (url, target) => opened.push([url, target])});
    await press(action(p, 'comfyui'));
    assert.deepStrictEqual(opened, [['https://www.comfy.org/download', '_blank']]);
    assert.strictEqual(p.requests.filter(r => r.path.startsWith('/setup/comfy/')).length, 0);
  }

  // Connect runs as a Setup step: the summary and the row say so while it
  // runs, and when it ends the page reads ComfyUI's state again.
  {
    const s = server({overview: {picture_application: h.comfy({state: 'connect', kind: 'desktop', folder: 'C:\\Comfy'})}});
    s.handle = (orig => (path, body) => {
      if (path === '/setup/comfy/connect') { s.calls.push(path); s.state = {...s.state, actions: {'comfyui-connect': {kind: 'connect', started_at: new Date().toISOString(), deadline: later(600000), step_id: 'c1'}}}; return {actions: s.state.actions, outcomes: {}, step_id: 'c1'}; }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await press(action(p, 'comfyui'));
    assert.deepStrictEqual(p.posts('/setup/comfy/connect').map(r => r.body), [{confirm: true}]);
    assert.deepStrictEqual([p.summary().sentence, p.summary().button], ['Connecting ComfyUI…', ''], 'Connect has no Cancel');
    assert.strictEqual(p.parts('comfyui').line, 'Connecting');
    assert.strictEqual(action(p, 'comfyui').disabled, true, 'the button waits while the step runs');
    const reads = p.requests.filter(r => r.path === '/setup/overview').length;
    const {actions, ...rest} = s.state;
    s.state = {...rest, outcomes: {'comfyui-connect': {kind: 'connect', step_id: 'c1', ended_at: new Date().toISOString()}}};
    s.over = {picture_application: h.comfy({state: 'connected', kind: 'desktop', folder: 'C:\\Comfy', open: true})};
    await p.runTimers();
    assert(p.requests.filter(r => r.path === '/setup/overview').length > reads, 'the overview is read again');
    assert.strictEqual(p.parts('comfyui').line, 'Installed · connected to OpenAbstractions');
    assert.deepStrictEqual([action(p, 'comfyui').textContent, action(p, 'comfyui').disabled], ['Open', false]);
    // The connected ComfyUI has no model yet: Install offers the
    // recommended one, and the person still clicks Install.
    assert.strictEqual(p.summary().sentence, 'Adds FLUX.2 Klein 4B compact, the picture model ComfyUI lacks.');
    assert.strictEqual(p.posts('/setup/install').length, 0, 'nothing starts before the click');
  }

  // Update on an integration an earlier OpenAbstractions installed runs
  // Connect as a Setup step; the summary and the row say Updating, and when
  // it ends the row asks to restart the ComfyUI that kept running.
  {
    const s = server({overview: {picture_application: h.comfy({state: 'connected', kind: 'desktop', folder: 'C:\\Comfy', open: true, update: true})}});
    s.handle = (orig => (path, body) => {
      if (path === '/setup/comfy/connect') { s.state = {...s.state, actions: {'comfyui-connect': {kind: 'update', started_at: new Date().toISOString(), deadline: later(600000), step_id: 'u1'}}}; return {actions: s.state.actions, outcomes: {}, step_id: 'u1'}; }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    assert.deepStrictEqual([action(p, 'comfyui').textContent, action(p, 'comfyui').disabled], ['Update', false]);
    await press(action(p, 'comfyui'));
    assert.deepStrictEqual(p.posts('/setup/comfy/connect').map(r => r.body), [{confirm: true}]);
    assert.deepStrictEqual([p.summary().sentence, p.summary().button], ['Updating the ComfyUI integration…', '']);
    assert.strictEqual(p.parts('comfyui').line, 'Updating');
    assert.strictEqual(action(p, 'comfyui').disabled, true, 'the button waits while the step runs');
    const {actions, ...rest} = s.state;
    s.state = {...rest, outcomes: {'comfyui-connect': {kind: 'update', step_id: 'u1', ended_at: new Date().toISOString()}}};
    s.over = {picture_application: h.comfy({state: 'connected', kind: 'desktop', folder: 'C:\\Comfy', open: true, restart: true})};
    await p.runTimers();
    assert.strictEqual(p.parts('comfyui').line, 'Installed · restart ComfyUI to use the updated integration');
    assert.deepStrictEqual([action(p, 'comfyui').textContent, action(p, 'comfyui').disabled], ['Open', false]);
    rulesHold(p);
  }

  // An Update that does not finish says why on the row and keeps Update.
  {
    const s = server({overview: {picture_application: h.comfy({state: 'connected', kind: 'desktop', open: true, update: true})}});
    s.handle = (orig => (path, body) => {
      if (path === '/setup/comfy/connect') { s.state = {...s.state, actions: {'comfyui-connect': {kind: 'update', started_at: new Date().toISOString(), deadline: later(600000), step_id: 'u2'}}}; return {actions: s.state.actions, outcomes: {}, step_id: 'u2'}; }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await press(action(p, 'comfyui'));
    const {actions, ...rest} = s.state;
    s.state = {...rest, outcomes: {'comfyui-connect': {kind: 'update', step_id: 'u2', ended_at: new Date().toISOString(),
      failure: 'Windows Installer could not install the ComfyUI integration (Windows Installer code 1603)', detail: 'code 1603'}}};
    await p.runTimers();
    assert.strictEqual(p.parts('comfyui').line, 'Windows Installer could not install the ComfyUI integration (Windows Installer code 1603).');
    assert.deepStrictEqual([action(p, 'comfyui').textContent, action(p, 'comfyui').disabled], ['Update', false]);
    assert(p.notices.some(n => n.sentence === 'The ComfyUI integration was not updated.'), 'the failure is logged');
  }

  // A Connect that does not finish says why on ComfyUI's row; the raw text
  // sits under its Technical details and Connect stays offered.
  {
    const s = server({overview: {picture_application: h.comfy({state: 'connect', kind: 'portable', folder: 'D:\\ComfyUI'})}});
    s.handle = (orig => (path, body) => {
      if (path === '/setup/comfy/connect') { s.state = {...s.state, actions: {'comfyui-connect': {kind: 'connect', started_at: new Date().toISOString(), deadline: later(600000), step_id: 'c2'}}}; return {actions: s.state.actions, outcomes: {}, step_id: 'c2'}; }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await press(action(p, 'comfyui'));
    const {actions, ...rest} = s.state;
    s.state = {...rest, outcomes: {'comfyui-connect': {kind: 'connect', step_id: 'c2', ended_at: new Date().toISOString(),
      failure: 'Windows Installer could not install the ComfyUI integration (Windows Installer code 1603)', detail: 'Windows Installer could not install the ComfyUI integration (Windows Installer code 1603)'}}};
    await p.runTimers();
    assert.strictEqual(p.parts('comfyui').line, 'Windows Installer could not install the ComfyUI integration (Windows Installer code 1603).');
    assert.strictEqual(p.parts('comfyui').lineState, 'error');
    assert(h.allText(p.parts('comfyui').body).includes('Error: Windows Installer could not install'), h.allText(p.parts('comfyui').body));
    assert.deepStrictEqual([action(p, 'comfyui').textContent, action(p, 'comfyui').disabled], ['Connect', false]);
    assert(p.notices.some(n => n.sentence === 'ComfyUI was not connected.'), 'the failure is logged');
  }

  // A build without the integration says so and keeps Connect disabled.
  {
    const s = server({overview: {picture_application: h.comfy({state: 'connect', kind: 'desktop', connectable: false})}});
    const p = await h.page(s.handle);
    assert.strictEqual(p.parts('comfyui').line, "Installed · Update OpenAbstractions to connect it");
    assert.deepStrictEqual([action(p, 'comfyui').textContent, action(p, 'comfyui').disabled], ['Connect', true]);
  }

  // Open starts the connected ComfyUI; a refusal shows its sentence on the row.
  {
    let refuse = false;
    const s = server({overview: {picture_application: h.comfy({state: 'connected', kind: 'desktop', open: true})}});
    s.handle = (orig => (path, body) => path === '/setup/comfy/open' ? (refuse ? new Error('Start ComfyUI from the Start menu.') : {opened: true}) : orig(path, body))(s.handle);
    const p = await h.page(s.handle);
    await press(action(p, 'comfyui'));
    assert.deepStrictEqual(p.posts('/setup/comfy/open').map(r => r.body), [{confirm: true}]);
    assert.strictEqual(p.parts('comfyui').line, 'Installed · connected to OpenAbstractions');
    refuse = true;
    await press(action(p, 'comfyui'));
    assert.strictEqual(p.parts('comfyui').line, 'Start ComfyUI from the Start menu.');
    assert.strictEqual(p.parts('comfyui').lineState, 'error');
  }

  // Comfy Desktop without an installed ComfyUI: Open starts it so the person
  // finishes its setup.
  {
    const s = server({overview: {picture_application: h.comfy({state: 'finish', kind: 'comfy-desktop', open: true})}});
    s.handle = (orig => (path, body) => path === '/setup/comfy/open' ? {opened: true} : orig(path, body))(s.handle);
    const p = await h.page(s.handle);
    await press(action(p, 'comfyui'));
    assert.deepStrictEqual(p.posts('/setup/comfy/open').map(r => r.body), [{confirm: true}]);
    assert.strictEqual(p.parts('comfyui').line, 'Open Comfy Desktop to finish its setup');
  }

  // Choose folder… keeps a portable picture application's folder and reads the row
  // again; a folder without ComfyUI is refused in one sentence on the row.
  {
    let reply = {available: true, chosen: true};
    const s = server();
    s.handle = (orig => (path, body) => {
      if (path === '/setup/comfy/folder') { if (reply instanceof Error) return reply; s.over = {picture_application: h.comfy({state: 'connect', kind: 'portable', folder: 'D:\\ComfyUI_windows_portable\\ComfyUI'})}; return reply; }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    const folder = () => h.facts(p.parts('comfyui').body) && p.parts('comfyui').body.children.find(c => c.className === 'oa-row' && c.children[0].children[0].textContent === 'Portable ComfyUI').children[1].children[0];
    assert.strictEqual(folder().textContent, 'Choose folder…');
    reply = new Error('This folder has no ComfyUI in it. Choose the ComfyUI_windows_portable folder.');
    await press(folder());
    assert.strictEqual(p.parts('comfyui').lineState, 'error');
    assert(/no ComfyUI/.test(p.parts('comfyui').line), p.parts('comfyui').line);
    reply = {available: true, chosen: true};
    await press(folder());
    assert.deepStrictEqual(p.posts('/setup/comfy/folder').map(r => r.body), [{}, {}]);
    assert.strictEqual(p.parts('comfyui').line, 'Installed · not connected to OpenAbstractions');
    assert.strictEqual(action(p, 'comfyui').textContent, 'Connect');
  }

  // The OA build an earlier release installed has its own row: one line,
  // one button that removes it; ComfyUI's row still offers Get ComfyUI.
  {
    const s = server({overview: {picture_application: h.comfy({oa_build: true})}});
    s.state = h.oaBuildState();
    const p = await h.page(s.handle);
    assert.deepStrictEqual(p.rowIDs('setup-app-rows'), ['setup-row-comfyui', 'setup-row-comfyui-oa', 'setup-row-opencode']);
    const row = p.parts('comfyui-oa');
    assert.deepStrictEqual([row.title, row.caption, row.line, row.box], ['ComfyUI (previous version)', 'From an earlier version', 'Installed · removing it keeps your pictures and models', null]);
    assert.deepStrictEqual([action(p, 'comfyui-oa').textContent, action(p, 'comfyui-oa').disabled], ['Remove previous version', false]);
    const body = h.allText(row.body);
    assert(h.facts(row.body).includes('Removed: Its program, Apps entry and Start menu shortcut.') && h.facts(row.body).includes('Kept: Your pictures, models and the files you added.'), body);
    assert.strictEqual(p.parts('comfyui').line, 'Not installed');
    assert.strictEqual(p.summary().sentence, 'Tick to install. Clear a tick to remove.', 'the earlier plan leaves nothing stopped');
    rulesHold(p);
  }

  // Remove the OA build runs as its own step with progress and Cancel; a
  // failure ends as the result with its reason; a removal that finishes
  // drops the row.
  {
    const s = server({overview: {picture_application: h.comfy({oa_build: true})}});
    s.state = h.oaBuildState();
    const now = new Date().toISOString();
    s.handle = (orig => (path, body) => {
      if (path === '/setup/comfy/remove-oa-build') { s.state = {...s.state, actions: {'comfyui-app': {kind: 'remove', started_at: now, deadline: later(7200000), done: 900, total: 39200, progress_at: now, cancellable: true, step_id: 'r1'}}}; return {actions: s.state.actions, outcomes: {}, step_id: 'r1'}; }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await press(action(p, 'comfyui-oa'));
    assert.deepStrictEqual(p.posts('/setup/comfy/remove-oa-build').map(r => r.body), [{confirm: true}]);
    assert.deepStrictEqual(p.summary(), {sentence: 'Removing previous version…', facts: '900 of 39,200 files checked.', button: 'Cancel', disabled: false});
    assert.strictEqual(p.parts('comfyui-oa').line, 'Removing · 900 of 39,200 files checked');
    assert.strictEqual(action(p, 'comfyui-oa').disabled, true);
    const {actions, ...rest} = s.state;
    s.state = {...rest, outcomes: {'comfyui-app': {kind: 'remove', step_id: 'r1', ended_at: new Date().toISOString(), failure: 'the version is in use or locked; close it and retry'}}};
    await p.runTimers();
    assert.deepStrictEqual([p.summary().sentence, p.summary().button], ['ComfyUI (previous version) was not removed. The version is in use or locked.', 'Try again']);
    assert.strictEqual(p.parts('comfyui-oa').lineState, 'error');
    // Pressed again, the removal finishes and the row is gone.
    const again = await h.page(s.handle);
    await press(action(again, 'comfyui-oa'));
    const {actions: running, ...done} = s.state;
    s.state = {...h.ownerState(), outcomes: {'comfyui-app': {kind: 'remove', step_id: 'r1', ended_at: new Date().toISOString()}}};
    s.over = {picture_application: h.comfy()};
    await again.runTimers();
    void running; void done;
    assert.deepStrictEqual(again.rowIDs('setup-app-rows'), ['setup-row-comfyui', 'setup-row-opencode']);
  }

  // A removal of the OA build running when the page opens shows on the
  // summary with Cancel, and Cancel stops it.
  {
    const s = server({overview: {picture_application: h.comfy({oa_build: true, oa_build_removing: true})}});
    const now = new Date().toISOString();
    s.state = h.oaBuildState({components: [h.installedRow('pytorch-engine'), h.installedRow('thenoise-engine'), {id: 'comfyui-app', effect: {phase: 'installed', removing: true}}],
      actions: {'comfyui-app': {kind: 'remove', started_at: now, deadline: later(600000), done: 20700, total: 39200, progress_at: now, cancellable: true}}});
    s.handle = (orig => (path, body) => path === '/setup/cancel-step' ? (s.calls.push(path + ' ' + JSON.stringify(body)), {cancelled: true}) : orig(path, body))(s.handle);
    const p = await h.page(s.handle);
    assert.deepStrictEqual(p.summary(), {sentence: 'Removing previous version…', facts: '20,700 of 39,200 files checked.', button: 'Cancel', disabled: false});
    assert.strictEqual(p.parts('comfyui-oa').line, 'Removing · 20,700 of 39,200 files checked');
    await p.click();
    for (let i = 0; i < 3; i++) await h.settled();
    assert.deepStrictEqual(p.posts('/setup/cancel-step').map(r => r.body), [{component_id: 'comfyui-app'}], 'Cancel stops the step');
  }

  // A removal of the OA build that stopped part way is no stopped install:
  // Install settles nothing, and the row's own button continues it.
  for (const record of [null, {plan_id: 'plan-r27', model_plan_id: '', picture_model_plan_ids: [], rules: [], granted: true, interrupted: 'update'}]) {
    const s = server({overview: {picture_application: h.comfy({oa_build: true, oa_build_removing: true})}});
    s.state = h.oaBuildState({components: [h.installedRow('pytorch-engine'), h.installedRow('thenoise-engine'), {id: 'comfyui-app', effect: {phase: 'installed', removing: true}}]});
    s.record = record;
    const p = await h.page(s.handle);
    assert.strictEqual(p.summary().sentence, 'Tick to install. Clear a tick to remove.', 'record ' + JSON.stringify(record));
    assert.strictEqual(p.posts('/setup/settle-removal').length, 0);
    assert.strictEqual(p.parts('comfyui-oa').line, 'Removing stopped before it finished');
    assert.deepStrictEqual([action(p, 'comfyui-oa').textContent, action(p, 'comfyui-oa').disabled], ['Remove previous version', false]);
  }

  // Journey suite, 2026-11-10: an engine removal refused because another
  // step runs shows that step with its progress and no failure.
  const busyStep = () => { const now = new Date().toISOString(); return {'pytorch-engine': {kind: 'remove', started_at: now, deadline: later(7200000), done: 4000, total: 39200, progress_at: now, cancellable: true, step_id: 'step-first'}}; };
  const busyError = actions => new Error(JSON.stringify({error: 'Another Catalog step is running.', busy: true, actions, outcomes: {}}));
  const busyRemove = async () => {
    const s = server();
    s.handle = (orig => (path, body) => {
      if (path === '/setup/engine/remove') { s.calls.push(path); const actions = busyStep(); s.state = {...s.state, actions}; return busyError(actions); }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await p.tick('thenoise', false);
    await p.click();
    for (let i = 0; i < 3; i++) await h.settled();
    return {s, p};
  };
  {
    const {p} = await busyRemove();
    assert.strictEqual(p.posts('/setup/engine/remove').length, 1);
    assert.deepStrictEqual(p.summary(), {sentence: 'Removing Diffusers…', facts: '4,000 of 39,200 files checked.', button: 'Cancel', disabled: false});
  }
  // Ticks changed while a refused removal waits drop the old click.
  {
    const {s, p} = await busyRemove();
    await p.tick('thenoise', true);
    const {actions, ...rest} = s.state;
    s.state = rest;
    await p.runTimers();
    for (let i = 0; i < 3; i++) await h.settled();
    assert.strictEqual(p.posts('/setup/engine/remove').length, 1, 'no second Remove');
    assert.strictEqual(p.posts('/setup/install').length, 0, 'no install');
  }
  // A busy Install shows the running step and says to apply again.
  {
    const s = server();
    s.handle = (orig => (path, body) => {
      if (path === '/setup/install' && body) { const actions = busyStep(); s.state = {...s.state, actions}; return busyError(actions); }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await p.tick('model:klein-full');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.deepStrictEqual(p.summary(), {sentence: 'Removing Diffusers…', facts: '4,000 of 39,200 files checked. Apply again when it finishes.', button: 'Cancel', disabled: false});
  }

  // A step Cancel cannot stop shows no Cancel.
  {
    const s = server();
    const now = new Date().toISOString();
    s.state = h.ownerState({actions: {'pytorch-engine': {kind: '', started_at: now, deadline: later(600000)}}});
    const p = await h.page(s.handle);
    const {disabled, ...shown} = p.summary();
    assert.deepStrictEqual(shown, {sentence: 'Installing Diffusers…', facts: '', button: ''});
  }

  // R19 render check, 2026-11-09: the update also changed the catalogue,
  // so the install the earlier release approved is no current plan. It
  // still opens stopped with its own ticks; Install settles its unfinished
  // rows and plans the same ticks against this release. A plan the earlier
  // approval covers installs at once; one that adds a package waits for
  // the person's Install on its ordinary summary, with the ticks kept.
  const earlier = (adds) => {
    const s = server();
    const r18 = {id: 'plan-r18', selected: ['pytorch', 'thenoise'], components: h.catalog.components.filter(c => ['pytorch-engine', 'thenoise-engine'].includes(c.id))};
    s.state = {approved: false, current_catalog: false, catalog: h.catalog, plan: r18,
      components: [h.installedRow('pytorch-engine'), {id: 'thenoise-engine', effect: {phase: 'requested'}}]};
    s.record = {plan_id: 'plan-r18', model_plan_id: 'klein-compact', picture_model_plan_ids: [], rules: [], interrupted: 'update'};
    s.handle = (orig => (path, body) => {
      if (path === '/setup/recover') { s.state = {...s.state, components: s.state.components.map(c => c.id === body.component_id ? {...c, effect: {phase: 'failed'}} : c)}; return s.state; }
      if (path === '/setup/plan') return h.planFor(body, ['pytorch-engine'], {components: [...r18.components, ...adds.map(id => ({id}))]});
      if (path === '/setup/install' && body) { const reply = orig(path, body); s.state = {...s.state, approved: true, current_catalog: true, components: s.state.components.map(c => ({...c, effect: {phase: 'installed'}}))}; return {...reply, ...s.state}; }
      return orig(path, body);
    })(s.handle);
    return s;
  };
  {
    const s = earlier([]);
    const p = await h.page(s.handle);
    for (let i = 0; i < 5; i++) await h.settled();
    for (const path of ['/setup/install', '/setup/recover', '/setup/model/start']) assert.strictEqual(p.posts(path).length, 0, 'opening the page posts nothing to ' + path);
    assert.deepStrictEqual(p.summary(), {sentence: 'Finish installing FLUX.2 Klein 4B compact and TheNoise.', facts: '9.37 GB download · needs 12.5 GB · 400 GB free on C: · Downloaded model files are kept.', button: 'Install', disabled: false});
    assert.strictEqual(p.parts('thenoise').box.checked, true, 'the earlier ticks arrive ticked');
    assert.strictEqual(p.parts('thenoise').line, 'Stopped');
    assert.strictEqual(p.parts('model:klein-compact').line, 'Stopped');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.deepStrictEqual(p.posts('/setup/recover').map(r => r.body.component_id), ['thenoise-engine'], 'the earlier unfinished row is settled first');
    const install = p.posts('/setup/install').map(r => r.body);
    assert.strictEqual(install.length, 1, 'a covered plan installs with the one press');
    assert.deepStrictEqual([install[0].plan_id, install[0].model_plan_id, install[0].selected], ['plan-pytorch-thenoise', 'klein-compact', ['pytorch', 'thenoise']]);
    assert.strictEqual(p.summary().sentence, 'Changes applied.');
  }
  {
    const s = earlier(['lemonade-engine']);
    const p = await h.page(s.handle);
    for (let i = 0; i < 5; i++) await h.settled();
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.posts('/setup/install').length, 0, 'a plan with a package the earlier approval lacks waits for the person');
    assert.strictEqual(p.summary().button, 'Install');
    assert.match(p.summary().sentence, /^Adds /, p.summary().sentence);
    assert.strictEqual(p.parts('thenoise').box.checked, true, 'the ticks are kept');
    assert.strictEqual(p.parts('model:klein-compact').box.checked, true, 'the model tick is kept');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.posts('/setup/install').length, 1, 'the person\'s Install approves the new plan');
  }

  // Review B1 with the owner's R27 report (2026-11-11): a package this
  // release pins at another version brings its own size, so the stopped
  // banner shows this release's plan sizes from the start, and the one
  // click on that banner approves exactly that plan. A plan that changes
  // after the banner was drawn waits for the person.
  {
    const s = earlier([]);
    s.handle = (orig => (path, body) => {
      const reply = orig(path, body);
      if (path === '/setup/plan') reply.components = reply.components.map(c => c.id === 'pytorch-engine' ? {...c, artifact: {...c.artifact, version: '2.12.0', sha256: 'd'.repeat(64)}} : c);
      return reply;
    })(s.handle);
    const p = await h.page(s.handle);
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.summary().sentence, 'Finish installing FLUX.2 Klein 4B compact and TheNoise.');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.posts('/setup/install').length, 1, 'one click starts the new pin the banner showed');
  }
  {
    const s = earlier([]);
    let pinned = false;
    s.handle = (orig => (path, body) => {
      const reply = orig(path, body);
      if (path === '/setup/plan' && pinned) reply.components = reply.components.map(c => c.id === 'pytorch-engine' ? {...c, artifact: {...c.artifact, version: '2.12.0', sha256: 'd'.repeat(64)}} : c);
      return reply;
    })(s.handle);
    const p = await h.page(s.handle);
    for (let i = 0; i < 5; i++) await h.settled();
    pinned = true;
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.posts('/setup/install').length, 0, 'a pin that changed after the banner waits for the person');
    assert.match(p.summary().sentence, /^Adds /, p.summary().sentence);
    assert.strictEqual(p.parts('thenoise').box.checked, true, 'the ticks are kept');
  }

  // An install stopped with only its permissions left says so.
  {
    const s = server({overview: {picture_models: [{...h.compact(), prepared: true}, h.full(), h.zimage()]}});
    s.record = {plan_id: 'plan-owner', model_plan_id: 'klein-compact', picture_model_plan_ids: [], rules: [{action: 'a', resource: 'r'}], interrupted: 'update'};
    const p = await h.page(s.handle);
    assert.deepStrictEqual(p.summary(), {sentence: 'Setup paused while OpenAbstractions updated.', facts: 'ComfyUI\'s permissions are not saved yet.', button: 'Install', disabled: false});
  }

  // A step still waiting continues on its timer; Cancel records the cancel
  // and the page leaves the work.
  {
    const s = server();
    s.handle = (orig => (path, body) => path === '/setup/model/advance' ? (s.calls.push(path), {engine: 'pytorch', plan: {id: 'klein-compact'}, status: {approved: true, assets: [{id: 'main', phase: 'requested', transfer: {state: 'running', done: 2 * GB, total: 8.27 * GB}}]}, choice_status: {}}) : orig(path, body))(s.handle);
    const p = await h.page(s.handle);
    await p.tick('model:klein-compact');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.summary().button, 'Cancel');
    assert.match(p.summary().sentence, /^Installing 1 of 1\. Keep this page open\.$/);
    assert.strictEqual(p.parts('model:klein-compact').line, 'Downloading · 2 of 8.27 GB');
    assert.strictEqual(p.parts('model:klein-compact').bar.hidden, false);
    await p.click();
    assert(p.posts('/setup/install/cancel').length === 1, 'Cancel is recorded');
    assert.notStrictEqual(p.summary().button, 'Cancel');
  }

  // The stall clock reads bytes (R17: a 3.8 GB file took five minutes): a
  // download receiving bytes for twelve minutes keeps going; five minutes
  // with no byte, phase or job-state change ends on the model's row.
  {
    let now = Date.parse('2026-11-09T12:00:00Z'), bytes = 0;
    class FakeDate extends Date { static now() { return now; } }
    const s = server();
    const running = () => ({engine: 'pytorch', plan: {id: 'klein-compact'}, choice_status: {},
      status: {approved: true, assets: [{id: 'main', phase: 'requested', transfer: {state: 'running', done: bytes, total: 8.27 * GB}}]}});
    s.handle = (orig => (path, body) => path === '/setup/model/advance' || path.startsWith('/setup/model?') ? running() : orig(path, body))(s.handle);
    const p = await h.page(s.handle, {Date: FakeDate});
    await p.tick('model:klein-compact');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    const pass = async () => { const drive = p.timers.filter(fn => String(fn).includes('setupInstallDrive')).at(-1); assert(drive, 'the install keeps polling'); p.timers.length = 0; drive(); for (let i = 0; i < 5; i++) await h.settled(); };
    for (let i = 1; i <= 3; i++) { now += 240000; bytes = i * GB; await pass(); }
    assert.strictEqual(p.summary().button, 'Cancel', 'a download receiving bytes is no stall');
    assert.strictEqual(p.parts('model:klein-compact').line, 'Downloading · 3 of 8.27 GB');
    now += 240000; await pass(); now += 120000; await pass();
    assert.strictEqual(p.summary().sentence, 'FLUX.2 Klein 4B compact did not install.', 'the stall names the model; the installed engines are no failure');
    assert.strictEqual(p.parts('model:klein-compact').line, 'The download stopped making progress.');
    assert(p.notices.some(n => n.kind === 'stalled'), 'the stall is logged');
  }

  // The owner's compact Klein on 2026-11-09: every file verified,
  // provider_configuring left set by an ended preparation, not configured.
  // The step asks the service to configure; the service resolves the stale
  // marker under its operation lock and prepares the model.
  const preparing = () => ({engine: 'pytorch', plan: {id: 'klein-compact'}, choice_status: {},
    status: {approved: true, provider_configuring: true, provider_configured: false, assets: [{id: 'main', phase: 'verified', transfer: {state: 'complete', done: 8.27 * GB, total: 8.27 * GB}}]}});
  {
    const s = server();
    let prepared = false;
    const configured = () => { const r = preparing(); r.status = {...r.status, provider_configuring: false, provider_configured: true, ready: true}; return r; };
    s.handle = (orig => (path, body) => {
      if (path === '/setup/model/configure') { prepared = true; return configured(); }
      if (path === '/setup/model/start' || path.startsWith('/setup/model?')) return prepared ? configured() : preparing();
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await p.tick('model:klein-compact');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.deepStrictEqual(p.posts('/setup/model/configure').map(r => r.body.plan_id), ['klein-compact'], 'a stale preparation is configured again');
    assert.strictEqual(p.summary().sentence, 'Changes applied.', 'the install finishes');
  }

  // A preparation the service still reports as running after the configure
  // request ends in the stall sentence with Try again.
  {
    let now = Date.parse('2026-11-09T12:00:00Z');
    class FakeDate extends Date { static now() { return now; } }
    const s = server();
    s.handle = (orig => (path, body) => path === '/setup/model/start' || path === '/setup/model/configure' || path.startsWith('/setup/model?') ? preparing() : orig(path, body))(s.handle);
    const p = await h.page(s.handle, {Date: FakeDate});
    await p.tick('model:klein-compact');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.parts('model:klein-compact').line, 'Preparing', 'the row shows the preparation');
    const pass = async () => { const drive = p.timers.filter(fn => String(fn).includes('setupInstallDrive')).at(-1); assert(drive, 'the install keeps polling'); p.timers.length = 0; drive(); for (let i = 0; i < 5; i++) await h.settled(); };
    now += 1000; await pass(); now += 300001; await pass();
    assert.strictEqual(p.summary().sentence, 'FLUX.2 Klein 4B compact did not install.');
    assert.strictEqual(p.summary().button, 'Try again');
    assert.strictEqual(p.parts('model:klein-compact').line, 'Preparing it stopped making progress.');
  }

  // Install on a stopped install counts only what the saved install approved: a model ticked by
  // the page (Qwen3.6 35B for an OpenCode without one) is not part of it.
  {
    const s = server({overview: {coding_model: {...h.overview().coding_model, installed: false}, desktop_integration: {app: 'opencode', coding: true, installed: true}}});
    s.record = {plan_id: 'plan-owner', model_plan_id: 'klein-compact', picture_model_plan_ids: [], rules: [{action: 'a', resource: 'r'}]};
    s.handle = (orig => (path, body) => path === '/setup/model/advance' ? {engine: 'pytorch', plan: {id: 'klein-compact'}, choice_status: {}, status: {approved: true, assets: [{id: 'main', phase: 'requested', transfer: {state: 'running', done: 1e9, total: 8.27 * GB}}]}} : orig(path, body))(s.handle);
    const p = await h.page(s.handle);
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.summary().sentence, 'Finish installing FLUX.2 Klein 4B compact.', 'the stopped install names only its own row');
    assert.strictEqual(p.parts('coding').box.checked, false, 'a stopped install arrives with its own ticks only');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.match(p.summary().sentence, /^Installing 1 of 1\./, p.summary().sentence);
    assert.notStrictEqual(p.parts('coding').line, 'Waiting', 'the pre-ticked coding model is no part of the resumed work');
  }

  // A coding-model failure before the engines install says they were not
  // installed either.
  {
    const s = server();
    s.state = h.freshState();
    s.handle = (orig => (path, body) => {
      if (path === '/setup/overview') return h.overview({desktop_integration: {app: 'opencode', coding: true, installed: true}});
      if (path === '/setup/plan') return h.planFor(body, []);
      if (path === '/setup/modelhost/coding') return body ? new Error('reserve: denied') : {status: {plan: h.overview().coding_model, approved: false, assets: []}};
      if (path === '/setup/install' && body) { s.record = {plan_id: body.plan_id, coding_model_plan_id: body.coding_model_plan_id, rules: []}; return {...s.state, approved: true, plan: {id: body.plan_id, selected: body.selected}, install: s.record}; }
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await p.tick('thenoise');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.match(p.summary().sentence, /^Qwen3\.6 35B did not install; TheNoise was not installed either\.$/, p.summary().sentence);
    assert.strictEqual(p.parts('coding').line, 'The download stopped.');
  }

  // A refused package (409 on advance-next) names its row and gives the
  // service's refusal sentence; the raw text sits under the row's Technical
  // details.
  {
    const s = server();
    s.state = h.freshState();
    const refusal = "the picture-model provider keeps its state in this account's own folders; Setup leaves it alone while the Panel serves a runtime with its own state folder";
    s.handle = (orig => (path, body) => {
      if (path === '/setup/plan') return h.planFor(body, []);
      if (path === '/setup/install' && body) {
        s.record = {plan_id: body.plan_id, rules: []};
        s.state = {...s.state, approved: true, plan: {id: body.plan_id, selected: body.selected, preparable: true, components: h.catalog.components.filter(c => ['pytorch-engine'].includes(c.id))}, components: [{id: 'pytorch-engine', effect: {phase: 'pending'}}]};
        return {...s.state, install: s.record, model_engine: 'pytorch'};
      }
      if (path === '/setup/advance-next') return new Error(refusal);
      return orig(path, body);
    })(s.handle);
    const p = await h.page(s.handle);
    await p.tick('pytorch');
    await p.click();
    for (let i = 0; i < 5; i++) await h.settled();
    assert.strictEqual(p.summary().sentence, "Diffusers did not install. The picture-model provider keeps its state in this account's own folders.");
    assert.strictEqual(p.parts('pytorch').lineState, 'error');
    assert(h.allText(p.parts('pytorch').body).includes('Technical details   Folder: C:\\pt Error: step apps: ' + refusal), h.allText(p.parts('pytorch').body));
  }

  // The model host's pinned ZIP recovery sits in its row (Models links it
  // as #setup-modelhost); an engine not prepared yet says so, with Prepare
  // when OpenAbstractions carries it.
  {
    const s = server();
    let staged = false;
    s.handle = (orig => (path, body) => path === '/setup/modelhost/chat' ? {available: true, plan_id: 'plan-1', feature: {installed: true}, engine: {staged, approval: 'Stage it', approval_digest: 'd', bundled: {available: true, approval_digest: 'b'}}, pin: {archive: 'llama.zip', release: 'b8000', size: 1000, sha256: 'e'.repeat(64)}}
      : path === '/setup/modelhost/engine/bundled' ? (staged = true, {staged: true}) : orig(path, body))(s.handle);
    const p = await h.page(s.handle, {location: {search: '?k=panel-key', hash: '#setup-modelhost'}});
    assert.strictEqual(p.parts('modelhost').line, 'Not prepared yet.');
    assert.strictEqual(p.parts('modelhost').details.open, true, 'the link opens the model host row under the Engines heading');
    const body = JSON.stringify(h.facts(p.parts('modelhost').body));
    assert(body.includes('Choose the engine ZIP…'), body);
    const prepare = p.row('modelhost').children[0].children[2];
    assert.strictEqual(prepare.textContent, 'Prepare');
    assert.strictEqual(prepare.hidden, false, 'Prepare shows while llama.cpp is not prepared');
    await prepare.onclick({preventDefault() {}});
    await h.settled();
    assert.deepStrictEqual(p.posts('/setup/modelhost/engine/bundled').map(r => r.body), [{plan_id: 'plan-1', approval_digest: 'b', confirm: true}]);
    assert.strictEqual(p.parts('modelhost').line, 'Included with OpenAbstractions · Ready');
    assert.strictEqual(prepare.hidden, true);
  }

  // A read that fails says nothing on the row: the runtime-down banner
  // speaks for the page.
  {
    const s = server();
    s.handle = (orig => (path, body) => path === '/setup/modelhost/chat' ? new Error('service resolution: runtime_unavailable') : orig(path, body))(s.handle);
    const p = await h.page(s.handle);
    assert.strictEqual(p.parts('modelhost').line, '');
    assert.strictEqual(p.parts('modelhost').lineState, 'idle');
  }
  done = true;
  console.log('setup page: ok');
})().catch(error => { console.error(error); process.exit(1); });
