// Run with node monitor/integrations_page_test.js. Catalog lists every
// integration (ComfyUI, OpenCode, Claude, Codex) as one row of the same
// shape in each state; Apps lists the ones on this computer and connected,
// in that shape (setup_integrations.go, owner report 2026-11-22).
const fs = require('fs'), assert = require('assert'), vm = require('./panel_record_test_helper.js').vm;
const h = require('./testdata/setup_page_test_helper.js');
const {assertPanelRules} = require('./static/panel_rules.js');
const {allowlistedClasses} = require('./panel_fake_dom_test_helper.js');

const ids = ['comfyui', 'opencode', 'claude', 'codex'];
const titles = {comfyui: 'ComfyUI', opencode: 'OpenCode', claude: 'Claude', codex: 'Codex'};
// integration is one overview row as setupIntegrations writes it.
function integration(id, state) {
  const installed = state !== 'get', connected = state === 'open' || state === 'manage';
  const row = {id, title: titles[id], caption: id === 'comfyui' ? 'Create pictures' : 'Coding assistant', state, installed, connected,
    page: id === 'comfyui' ? '/app/comfyui' : '/connections/' + id};
  if (id === 'comfyui') row.download = 'https://www.comfy.org/download';
  if (id === 'opencode') row.download = 'https://opencode.ai/download';
  if (state === 'open') row.open = '/setup/comfy/open';
  row.registrations = id === 'comfyui' ? ['comfyui'] : ['oa-' + id + '-integration'];
  return row;
}
// pictureFor is ComfyUI's Catalog card for the same state.
const pictureFor = state => h.comfy(state === 'get' ? {} : state === 'connect' ? {state: 'connect'} : {state: 'connected', open: state === 'open'});
// desktopFor is OpenCode's desktop facts for the same state: its
// integration package installed and current (Repair).
const desktopFor = state => state === 'get'
  ? {app: 'opencode', coding: true, installed: false, reason: 'Get opens opencode.ai, where you download and install OpenCode yourself. Then Connect links it to OpenAbstractions.'}
  : {app: 'opencode', coding: true, installed: true, action: 'repair'};
const visibleButtons = row => row.children[0].children.filter(n => n.tagName === 'BUTTON' && !n.hidden);

async function catalogPage(states, extra = {}, reply = () => undefined) {
  const over = h.ownerOverview({picture_application: pictureFor(states.comfyui), desktop_integration: desktopFor(states.opencode),
    integrations: ids.map(id => integration(id, states[id])), ...extra});
  const opened = [];
  const p = await h.page(path => {
    if (path === '/setup/catalog') return h.catalog;
    if (path === '/setup/overview') return over;
    if (path === '/setup/state') return h.ownerState();
    if (path === '/setup/other-copies') return {copies: []};
    if (path === '/setup/modelhost/coding') return {status: {plan: over.coding_model, assets: []}};
    if (path === '/setup/modelhost/chat') return {available: true, feature: {installed: true}, engine: {staged: true}, pin: {}};
    if (path === '/setup/install') return {current_catalog: true, approved: false, install: {}};
    if (path === '/setup/plan') return h.planFor({selected: []}, ['pytorch-engine', 'thenoise-engine']);
    return reply(path);
  }, {open: (url, target) => opened.push([url, target])});
  return {p, opened};
}

const wantCatalog = {
  comfyui: {get: 'Get ComfyUI', connect: 'Connect', open: 'Open', manage: 'Manage'},
  opencode: {get: 'Get OpenCode', connect: 'Connect', manage: 'Manage'},
  claude: {connect: 'Connect', manage: 'Manage'},
  codex: {connect: 'Connect', manage: 'Manage'},
};
const states4 = ['get', 'connect', 'open', 'manage'];

(async () => {
  // --- Catalog: every integration, each state, one row shape. ---
  for (const state of states4) {
    const states = {};
    for (const id of ids) states[id] = wantCatalog[id][state] ? state : 'connect';
    const {p, opened} = await catalogPage(states);
    assert.deepStrictEqual(p.rowIDs('setup-app-rows'), ids.map(id => 'setup-row-' + id), state + ': Catalog lists all four integrations');
    for (const id of ids) {
      const parts = p.parts(id), buttons = visibleButtons(parts.details), want = wantCatalog[id][states[id]];
      assert.strictEqual(parts.box, null, id + ' has no tick');
      assert.strictEqual(parts.title, titles[id]);
      assert.ok(parts.caption, id + ' has a caption');
      assert.ok(parts.line, id + ' says its state');
      assert.deepStrictEqual(buttons.map(b => b.textContent), [want], id + ' ' + states[id] + ': one state button');
      assert.strictEqual(buttons[0].disabled, false, id + ': the state button acts; the line says the state');
      assert(!/[0-9a-f]{32}|\\|oa-[a-z]+-integration/.test(h.allText(parts.details.children[0])), id + ': no identifier in the row');
    }
    // Each button runs the integration's existing flow.
    for (const id of ['claude', 'codex', 'opencode']) {
      const button = visibleButtons(p.parts(id).details)[0];
      if (states[id] === 'connect' || states[id] === 'manage') {
        await button.onclick({preventDefault() {}});
        assert.strictEqual(p.context.location.href, '/connections/' + id + '?k=panel-key', id + ': ' + states[id] + ' opens its connection page');
      }
      if (states[id] === 'get') {
        await button.onclick({preventDefault() {}});
        assert.deepStrictEqual(opened.at(-1), ['https://opencode.ai/download', '_blank'], 'Get OpenCode opens its publisher\'s download page');
      }
    }
    if (states.comfyui === 'manage') {
      await visibleButtons(p.parts('comfyui').details)[0].onclick({preventDefault() {}});
      assert.strictEqual(p.context.location.href, '/app/comfyui?k=panel-key', 'Manage opens the picture app\'s page');
    }
    assert(!p.posts('/setup/plan').some(r => (r.body.selected || []).some(x => ['claude', 'codex', 'opencode'].includes(x))), 'an integration row never enters a package plan');
    assertPanelRules({tagName: 'MAIN', children: [{tagName: 'SECTION', children: [p.get('setup-app-rows')]}]}, 'Catalog ' + state, {naming: true, allowClasses: allowlistedClasses('setup')});
  }
  // A build without OpenCode's desktop integration still lists OpenCode.
  {
    const {p} = await catalogPage({comfyui: 'get', opencode: 'get', claude: 'connect', codex: 'manage'}, {desktop_integration: {}});
    assert.deepStrictEqual(p.rowIDs('setup-app-rows'), ids.map(id => 'setup-row-' + id));
    assert.strictEqual(p.parts('opencode').line, 'Not installed');
    assert.deepStrictEqual(visibleButtons(p.parts('opencode').details).map(b => b.textContent), ['Get OpenCode']);
    // A connected agent's details open its connection settings.
    const link = p.parts('codex').body.children[0].children[1].children[0];
    assert.strictEqual(link.textContent, 'Connection settings');
    assert.strictEqual(link.href, '/connections/codex?k=panel-key');
  }
  // OpenCode's own flow: with OpenCode on this computer and its integration
  // package missing, Connect runs the package step (an OA download, then the
  // MSI); a current package's Repair sits in the details.
  {
    const step = 'desktop-integration';
    let outcome;
    const {p} = await catalogPage({comfyui: 'get', opencode: 'connect', claude: 'connect', codex: 'connect'}, {desktop_integration: {app: 'opencode', coding: true, installed: true, action: 'install'}},
      path => path.startsWith('/setup/desktop-integration?action=') ? (outcome = {step_id: 'step-1', kind: 'install', failure: 'Downloads are unavailable.'}, {step_id: 'step-1', actions: {}, outcomes: {[step]: outcome}}) : undefined);
    const button = visibleButtons(p.parts('opencode').details);
    assert.deepStrictEqual(button.map(b => b.textContent), ['Connect']);
    assert.strictEqual(button[0].id, 'setup-action-opencode', 'Connect is the integration package step');
    await button[0].onclick({preventDefault() {}});
    await h.settled();
    assert.deepStrictEqual(p.posts('/setup/desktop-integration?action=install').map(r => r.body), [{confirm: true}]);
    const {p: current} = await catalogPage({comfyui: 'get', opencode: 'connect', claude: 'connect', codex: 'connect'});
    const repair = current.parts('opencode').body.children.flatMap(row => (row.children || []).flatMap(part => part.children || [])).find(node => node.id === 'setup-desktop-repair-opencode');
    assert.strictEqual(repair.textContent, 'Repair', 'a current package offers Repair in the details');
  }
  // A failed connection attempt shows on the Catalog row as Needs attention
  // with its reason, in place of a plain Not connected (agent-ux 2026-11-22).
  {
    const sentence = 'Connecting Claude needs the installed OpenAbstractions.';
    const {p} = await catalogPage({comfyui: 'get', opencode: 'get', claude: 'connect', codex: 'connect'}, {integrations: ids.map(id => id === 'claude' ? {...integration(id, 'connect'), attention: sentence} : integration(id, id === 'claude' || id === 'codex' ? 'connect' : 'get'))});
    assert.strictEqual(p.parts('claude').line, 'Needs attention · ' + sentence);
    assert.deepStrictEqual(visibleButtons(p.parts('claude').details).map(b => b.textContent), ['Connect'], 'Connect opens the page that says what to do');
    assert.notStrictEqual(p.parts('codex').line, p.parts('claude').line, 'an agent without a failure keeps its own line');
  }
  console.log('PASS: Catalog lists ComfyUI, OpenCode, Claude and Codex in one row shape in every state');

  // --- Apps: the integrations on this computer and connected. ---
  const source = (file, name) => { const s = fs.readFileSync(__dirname + '/' + file, 'utf8'), m = 'const ' + name + ' = `', i = s.indexOf(m) + m.length; assert(i >= m.length, name); return s.slice(i, s.indexOf('`', i)); };
  const fields = new Map();
  const node = (tag = 'div') => ({tagName: String(tag).toUpperCase(), textContent: '', className: '', hidden: false, disabled: false, attrs: {}, children: [], dataset: {},
    replaceChildren(...v) { this.children = [...v]; }, append(...v) { this.children.push(...v); }, setAttribute(k, v) { this.attrs[k] = String(v); }, getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    addEventListener() {}, removeEventListener() {}});
  const el = id => { if (!fields.has(id)) fields.set(id, node()); return fields.get(id); };
  const registeredTitles = {comfyui: 'ComfyUI', 'oa-claude-integration': 'Claude', notes: 'Notes'};
  async function appsPage(list) {
    fields.clear();
    const posts = [];
    const context = {console, URLSearchParams, AbortController, location: {search: '?k=key'}, setTimeout: () => 0, clearTimeout() {},
      document: {getElementById: el, createElement: node, documentElement: {dataset: {}}, visibilityState: 'visible', addEventListener() {}},
      fetch: async (path, opts = {}) => {
        if (opts.method === 'POST') posts.push([path, JSON.parse(opts.body)]);
        if (path === '/runtime') return {ok: true, json: async () => ({capabilities: []})};
        if (path === '/setup/desktop-integration') return {ok: true, json: async () => ({})};
        if (path === '/setup/integrations') return list instanceof Error ? {ok: false, text: async () => list.message} : {ok: true, json: async () => ({integrations: list})};
        if (path === '/setup/comfy/open') return {ok: true, json: async () => ({opened: true})};
        if (path === '/apps/pending') return {ok: true, json: async () => ({outcome: 'page', entries: []})};
        if (path.startsWith('/apps/registrations?')) {
          const q = new URLSearchParams(path.split('?')[1]); if (q.get('wait_ms') !== '0') return new Promise(() => {});
          const entries = q.get('source') === 'providers' ? [] : ['comfyui', 'oa-claude-integration', 'notes'].map(name => ({name, role: 'application', title: registeredTitles[name], application: {Descriptor: {Name: name, Title: registeredTitles[name]}, Instances: []}}));
          return {ok: true, json: async () => ({outcome: 'page', cursor: 'c', entries})};
        }
        throw Error('unexpected ' + path);
      }};
    context.window = context; vm.createContext(context);
    vm.runInContext(source('panel_shell.go', 'plainScript').split('<script>')[1].split('</script>')[0], context);
    vm.runInContext(source('service_page.go', 'serviceCommonScript') + source('service_page.go', 'appsPageScript'), context);
    await context.appsArrived;
    return {context, rows: () => el('apps-integrations').children, posts};
  }
  for (const state of states4) {
    const states = {};
    for (const id of ids) states[id] = wantCatalog[id][state] ? state : 'manage';
    const {context, rows, posts} = await appsPage(ids.map(id => integration(id, states[id])));
    const connected = ids.filter(id => ['open', 'manage'].includes(states[id]));
    const registered = el('apps').children.map(r => r.label.textContent);
    assert.deepStrictEqual(registered, ['comfyui', 'oa-claude-integration', 'notes'].filter(name => !(name === 'comfyui' && connected.includes('comfyui')) && !(name === 'oa-claude-integration' && connected.includes('claude'))).map(name => registeredTitles[name]).sort(),
      state + ': the row of a connected integration stands in for its registered entries');
    if (!connected.length) assert.strictEqual(rows().length, 0, state + ': nothing connected shows no integration row');
    else assert.deepStrictEqual(rows().map(r => r.label.textContent), connected.map(id => titles[id]), state + ': Apps lists the connected integrations only');
    for (const row of rows().filter(r => r.integration)) {
      const id = row.integration.id;
      assert.ok(row.caption.textContent, id + ' has a caption');
      // The state reads in the Catalog's words (agent-ux 2026-11-22).
      assert.ok(row.caption.textContent.endsWith(' · Connected to OpenAbstractions'), id + ': ' + row.caption.textContent);
      assert.strictEqual(row.button.textContent, states[id] === 'open' ? 'Open' : 'Manage');
      assert.strictEqual(row.button.disabled, false, id + ': the state button acts');
      assert.strictEqual(row.link.href, id === 'comfyui' ? '/app/comfyui?k=key' : '/connections/' + id + '?k=key');
      if (states[id] === 'manage') {
        await row.button.onclick();
        assert.strictEqual(context.location.href, row.link.href, id + ': Manage opens its page');
      }
    }
    const comfy = rows().find(r => r.integration && r.integration.id === 'comfyui');
    if (comfy && states.comfyui === 'open') {
      await comfy.button.onclick();
      assert.deepStrictEqual(posts, [['/setup/comfy/open', {confirm: true}]], 'Open runs the picture app\'s Open route');
    }
    assertPanelRules({tagName: 'MAIN', children: [{tagName: 'SECTION', children: [el('apps-integrations'), el('apps')]}]}, 'Apps ' + state, {naming: true, allowClasses: allowlistedClasses('status')});
    context.appsRun.abort();
  }
  {
    const {context} = await appsPage(new Error('unavailable'));
    assert.strictEqual(el('apps-integrations').children[0].textContent, 'Connected integrations are unknown right now.', 'a failed read says so');
    assert.deepStrictEqual(el('apps').children.map(r => r.label.textContent), ['Claude', 'ComfyUI', 'Notes'], 'a failed read hides no registered app');
    context.appsRun.abort();
  }
  // A failed connection shows on Apps too, in the Catalog's words, with
  // Connect to its page.
  {
    const sentence = 'Connecting Claude needs the installed OpenAbstractions.';
    const list = ids.map(id => id === 'claude' ? {...integration(id, 'connect'), attention: sentence} : integration(id, 'manage'));
    const {context, rows} = await appsPage(list);
    const claude = rows().find(r => r.integration && r.integration.id === 'claude');
    assert(claude, 'a connection that needs attention is listed');
    assert.strictEqual(claude.caption.textContent, 'Coding assistant · Needs attention · ' + sentence);
    assert.strictEqual(claude.button.textContent, 'Connect');
    const {p} = await catalogPage({comfyui: 'get', opencode: 'get', claude: 'connect', codex: 'connect'}, {integrations: list});
    assert.strictEqual(p.parts('claude').line, 'Needs attention · ' + sentence, 'Catalog and Apps say the same state');
    context.appsRun.abort();
  }
  console.log('PASS: Apps lists the connected integrations in the Catalog row shape');
})().catch(error => { console.error(error); process.exitCode = 1; });
