// Run with node monitor/setup_repair_page_test.js. An installed app whose
// adopter fix-up is gone (OPENCODE-WATCHER-PIN ruling 1) says so on its row
// with one Repair button and never reads Installed. Repair runs as a Setup
// step; a refusal shows its sentence on the row.
const assert = require('assert');
const h = require('./testdata/setup_page_test_helper.js');

const fix = (over = {}) => ({app: 'opencode', name: 'OpenCode', component: 'opencode-official', state: 'needs_repair', repair: true,
  sentence: 'Needs its OpenAbstractions fix again', detail: "An update to OpenCode removed it. It stops a crash that takes OpenCode's server down.",
  versions: ['1.18.34', '1.19.5'], close: 'Close OpenCode, then press Repair.', ...over});
const unknown = () => fix({state: 'unknown', repair: false, sentence: 'Not a version this release supports', detail: 'This release of OpenAbstractions supports OpenCode 1.18.34 and 1.19.5. Setup leaves this OpenCode as it is.'});

// routes answers the page; repair answers POST /setup/repair.
const routes = (world, repair) => (path, body) => {
  if (path === '/setup/catalog') return h.catalog;
  if (path === '/setup/state') return world.state();
  if (path === '/setup/overview') return world.overview();
  if (path === '/setup/other-copies') return {copies: []};
  if (path.startsWith('/setup/feature')) return {installed: true, available: true};
  if (path === '/setup/modelhost/coding') return {status: {plan: world.overview().coding_model, assets: []}};
  if (path === '/setup/modelhost/chat') return {available: true, feature: {installed: true}, engine: {staged: true}, pin: {}};
  if (path === '/setup/install') return {current_catalog: true, approved: false, install: {}};
  if (path === '/setup/plan') return h.planFor(body, ['pytorch-engine', 'thenoise-engine']);
  if (path === '/setup/repair' && body) return repair(body);
  return undefined;
};
const repairButton = p => p.parts('opencode').details.children[0].children.find(c => c.tagName === 'BUTTON' && c.id === 'setup-repair-opencode');

(async () => {
  // The stock file is back: the row says so, offers Repair and never reads
  // Installed; its details say what the fix does.
  {
    const world = {fixes: [fix()], state: () => h.ownerState(), overview: () => h.ownerOverview({fixes: world.fixes})};
    const p = await h.page(routes(world, () => { throw Error('no repair'); }));
    const row = p.parts('opencode');
    assert.strictEqual(row.line, 'Needs its OpenAbstractions fix again', 'the Repair button says the action');
    assert(!/Installed/.test(row.line), 'the stock app never reads Installed');
    const button = repairButton(p);
    assert(button && !button.hidden && !button.disabled, 'Repair is offered');
    assert.strictEqual(button.textContent, 'Repair');
    assert(h.facts(row.body).includes('OpenAbstractions fix: '), h.facts(row.body).join(' | '));
    assert(h.allText(row.body).includes("An update to OpenCode removed it. It stops a crash that takes OpenCode's server down."), h.allText(row.body));
    assert.strictEqual(p.parts('comfyui').details.children[0].children.some(c => c.tagName === 'BUTTON' && c.id === 'setup-repair-comfyui'), false, 'an app without a fix-up has no Repair');
  }
  // The fix is in place: no Repair, the row reads Installed as before.
  {
    const world = {state: () => h.ownerState(), overview: () => h.ownerOverview({fixes: [fix({state: 'current', repair: false, sentence: '', detail: ''})]})};
    const p = await h.page(routes(world, () => { throw Error('no repair'); }));
    assert.strictEqual(p.parts('opencode').line, 'Installed · uses Qwen3.6 35B · remove in Windows Settings');
    assert(!repairButton(p) || repairButton(p).hidden, 'no Repair for a fixed app');
  }
  // A release or file Setup does not know: the row names the supported
  // versions, its details say more, and it offers no Repair.
  {
    const world = {state: () => h.ownerState(), overview: () => h.ownerOverview({fixes: [unknown()]})};
    const p = await h.page(routes(world, () => { throw Error('no repair'); }));
    assert.strictEqual(p.parts('opencode').line, 'Not a version this release supports · Supports 1.18.34 and 1.19.5');
    assert(h.allText(p.parts('opencode').body).includes('Setup leaves this OpenCode as it is.'));
    assert(!/changed outside/.test(h.allText(p.parts('opencode').details)));
    assert(!repairButton(p) || repairButton(p).hidden, 'no Repair for an unknown release');
  }
  // A supported release whose file Setup does not know: the fix can't be
  // checked, and the row says how to put it back; no Repair.
  {
    const world = {state: () => h.ownerState(), overview: () => h.ownerOverview({fixes: [fix({state: 'unknown', repair: false, supported: true, sentence: "The OpenAbstractions fix can't be checked",
      detail: 'OpenCode 1.19.5 is a supported version. The file the fix replaces is missing or changed. Reinstall OpenCode with OpenAbstractions to put the fix back.'})]})};
    const p = await h.page(routes(world, () => { throw Error('no repair'); }));
    assert.strictEqual(p.parts('opencode').line, "The OpenAbstractions fix can't be checked · Reinstall OpenCode with OpenAbstractions");
    assert(h.allText(p.parts('opencode').body).includes('OpenCode 1.19.5 is a supported version.'));
    assert(!/Not a version/.test(h.allText(p.parts('opencode').details)));
    assert(!repairButton(p) || repairButton(p).hidden, 'no Repair for an unknown file');
  }
  // A Panel serving an isolated runtime shows the fix-up without Repair.
  {
    const world = {state: () => h.ownerState(), overview: () => h.ownerOverview({fixes: [fix({repair: false})]})};
    const p = await h.page(routes(world, () => { throw Error('no repair'); }));
    assert.strictEqual(p.parts('opencode').line, 'Needs its OpenAbstractions fix again');
    assert(!repairButton(p) || repairButton(p).hidden, 'no Repair while isolated');
  }
  // OpenCode is open: the row says to close it first, and a Repair the
  // Panel refuses shows the Panel's sentence on the row.
  {
    const world = {state: () => h.ownerState(), overview: () => h.ownerOverview({fixes: [fix({running: true})]})};
    const p = await h.page(routes(world, () => Error('Close OpenCode, then press Repair.\n')));
    assert.strictEqual(p.parts('opencode').line, 'Needs its OpenAbstractions fix again · Close OpenCode to repair');
    await repairButton(p).onclick();
    await h.settled();
    assert.strictEqual(p.parts('opencode').line, 'Close OpenCode, then press Repair.');
    assert.strictEqual(p.parts('opencode').lineState, 'error');
    assert.deepStrictEqual(p.posts('/setup/repair').map(r => r.body), [{component_id: 'opencode-official', confirm: true}]);
  }
  // Repair runs as a step: the row and the summary say Repairing while it
  // runs, then the page reads the rows again and the fix is in place.
  {
    let running = false, ended = null;
    const world = {fixes: [fix()],
      state: () => h.ownerState({actions: running ? {'opencode-official': {kind: 'repair', step_id: 's1', started_at: new Date().toISOString()}} : {}, outcomes: ended ? {'opencode-official': ended} : {}}),
      overview: () => h.ownerOverview({fixes: world.fixes})};
    const p = await h.page(routes(world, () => { running = true; return {step_id: 's1', actions: {'opencode-official': {kind: 'repair', step_id: 's1', started_at: new Date().toISOString()}}}; }));
    await repairButton(p).onclick();
    await h.settled();
    assert.strictEqual(p.parts('opencode').line, 'Repairing');
    assert.strictEqual(p.summary().sentence, 'Repairing OpenCode…');
    assert.strictEqual(repairButton(p).disabled, true, 'Repair waits while a step runs');
    running = false; ended = {kind: 'repair', step_id: 's1', ended_at: new Date().toISOString()};
    world.fixes = [fix({state: 'current', repair: false, sentence: '', detail: ''})];
    await p.runTimers(1);
    assert.strictEqual(p.parts('opencode').line, 'Installed · uses Qwen3.6 35B · remove in Windows Settings', 'the page checked again');
    assert(repairButton(p).hidden, 'Repair is gone once the fix is back');
  }
  // A Repair that fails says why on its row and Repair stays offered.
  {
    let running = false, ended = null;
    const world = {state: () => h.ownerState({actions: running ? {'opencode-official': {kind: 'repair', step_id: 's2', started_at: new Date().toISOString()}} : {}, outcomes: ended ? {'opencode-official': ended} : {}}),
      overview: () => h.ownerOverview({fixes: [fix()]})};
    const p = await h.page(routes(world, () => { running = true; return {step_id: 's2', actions: {'opencode-official': {kind: 'repair', step_id: 's2', started_at: new Date().toISOString()}}}; }));
    await repairButton(p).onclick();
    await h.settled();
    running = false; ended = {kind: 'repair', step_id: 's2', ended_at: new Date().toISOString(), failure: 'Repair did not finish.', detail: 'Windows Installer did not repair {X} (Windows Installer code 1603)'};
    await p.runTimers(1);
    assert.strictEqual(p.parts('opencode').line, 'Repair did not finish.');
    assert(h.allText(p.parts('opencode').body).includes('Error: Windows Installer did not repair {X} (Windows Installer code 1603)'), 'the code is in the technical details');
    assert.strictEqual(repairButton(p).hidden, false);
    assert.strictEqual(repairButton(p).disabled, false);
  }
  // Another Setup step runs: Repair is refused at once and the row says so.
  {
    const world = {state: () => h.ownerState(), overview: () => h.ownerOverview({fixes: [fix()]})};
    const busy = JSON.stringify({error: 'Another Catalog step is running.', busy: true, actions: {'pytorch-engine': {kind: 'remove', step_id: 's3', started_at: new Date().toISOString()}}});
    const p = await h.page(routes(world, () => Error(busy)));
    await repairButton(p).onclick();
    await h.settled();
    assert.strictEqual(p.parts('opencode').line, 'Another Catalog step is running.');
    assert.strictEqual(repairButton(p).disabled, true, 'Repair waits for the running step');
  }
  console.log('setup repair page tests passed');
})().catch(e => { console.error(e); process.exit(1); });
