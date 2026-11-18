// Run by TestIntegrationWriterNamesShowEachAppOnce with a JSON file: the
// served /setup/integrations reply and the entries the registration and
// enrollment writers register. Apps shows each connected integration once,
// and Catalog lists each integration once.
const fs = require('fs'), assert = require('assert'), vm = require('./panel_record_test_helper.js').vm;
const h = require('./testdata/setup_page_test_helper.js');

const fixture = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const integrations = fixture.integrations.integrations;
const registered = fixture.registered.map(r => ({name: r.name, role: 'application', title: r.title, application: {Descriptor: {Name: r.name, Title: r.title}, Instances: []}}));

(async () => {
  // Apps.
  const source = (file, name) => { const s = fs.readFileSync(__dirname + '/' + file, 'utf8'), m = 'const ' + name + ' = `', i = s.indexOf(m) + m.length; assert(i >= m.length, name); return s.slice(i, s.indexOf('`', i)); };
  const fields = new Map();
  const node = (tag = 'div') => ({tagName: String(tag).toUpperCase(), textContent: '', className: '', hidden: false, disabled: false, attrs: {}, children: [], dataset: {},
    replaceChildren(...v) { this.children = [...v]; }, append(...v) { this.children.push(...v); }, setAttribute(k, v) { this.attrs[k] = String(v); }, getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    addEventListener() {}, removeEventListener() {}});
  const el = id => { if (!fields.has(id)) fields.set(id, node()); return fields.get(id); };
  const context = {console, URLSearchParams, AbortController, location: {search: '?k=key'}, setTimeout: () => 0, clearTimeout() {},
    document: {getElementById: el, createElement: node, documentElement: {dataset: {}}, visibilityState: 'visible', addEventListener() {}},
    fetch: async path => {
      if (path === '/runtime') return {ok: true, json: async () => ({capabilities: []})};
      if (path === '/setup/desktop-integration') return {ok: true, json: async () => ({})};
      if (path === '/setup/integrations') return {ok: true, json: async () => fixture.integrations};
      if (path === '/apps/pending') return {ok: true, json: async () => ({outcome: 'page', entries: []})};
      if (path.startsWith('/apps/registrations?')) {
        const q = new URLSearchParams(path.split('?')[1]); if (q.get('wait_ms') !== '0') return new Promise(() => {});
        return {ok: true, json: async () => ({outcome: 'page', cursor: 'c', entries: q.get('source') === 'providers' ? [] : registered})};
      }
      throw Error('unexpected ' + path);
    }};
  context.window = context; vm.createContext(context);
  vm.runInContext(source('panel_shell.go', 'plainScript').split('<script>')[1].split('</script>')[0], context);
  vm.runInContext(source('service_page.go', 'serviceCommonScript') + source('service_page.go', 'appsPageScript'), context);
  await context.appsArrived;
  const shown = [...el('apps-integrations').children, ...el('apps').children].map(r => r.label.textContent);
  const connected = integrations.filter(g => g.connected).map(g => g.title);
  assert.deepStrictEqual(connected.sort(), ['Claude', 'Codex', 'ComfyUI', 'OpenCode'], 'the fixture connects every integration');
  for (const title of connected) assert.strictEqual(shown.filter(t => t === title).length, 1, title + ' shows once on Apps: ' + shown.join(', '));
  assert.deepStrictEqual(shown.filter(t => !connected.includes(t)), ['Notes'], 'other registered apps stay');
  context.appsRun.abort();

  // Catalog.
  const over = h.ownerOverview({picture_application: h.comfy({state: 'connected', open: true}), desktop_integration: {app: 'opencode', coding: true, installed: true, action: 'repair'}, integrations});
  const p = await h.page(path => {
    if (path === '/setup/catalog') return h.catalog;
    if (path === '/setup/overview') return over;
    if (path === '/setup/state') return h.ownerState();
    if (path === '/setup/other-copies') return {copies: []};
    if (path === '/setup/modelhost/coding') return {status: {plan: over.coding_model, assets: []}};
    if (path === '/setup/modelhost/chat') return {available: true, feature: {installed: true}, engine: {staged: true}, pin: {}};
    if (path === '/setup/install') return {current_catalog: true, approved: false, install: {}};
    if (path === '/setup/plan') return h.planFor({selected: []}, ['pytorch-engine', 'thenoise-engine']);
  });
  const titles = p.get('setup-app-rows').children.map(r => r.children[0].children[1].children[0].textContent);
  assert.deepStrictEqual(titles, ['ComfyUI', 'OpenCode', 'Claude', 'Codex'], 'Catalog lists each integration once');
  console.log('PASS: the writers\' registered names show each integration once on Apps and Catalog');
})().catch(error => { console.error(error); process.exitCode = 1; });
