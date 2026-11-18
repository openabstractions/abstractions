// The Setup page's test harness: it runs the served page script (the
// shell's plain script, serviceCommonScript and setupPageBody's script)
// against a fake DOM and in-memory replies, without a browser or service.
const fs = require('fs'), vm = require('../panel_record_test_helper.js').vm, assert = require('assert');

function source() {
  const go = fs.readFileSync(__dirname + '/../setup_page.go', 'utf8');
  const marker = 'const setupPageBody = `', start = go.indexOf(marker) + marker.length;
  assert(start >= marker.length, 'setupPageBody not found');
  let script = go.slice(start, go.indexOf('</script>`', start)).split('<script>')[1];
  const glue = '` + serviceCommonScript + `';
  assert(script.includes(glue), 'serviceCommonScript glue not found');
  const common = fs.readFileSync(__dirname + '/../service_page.go', 'utf8'), commonMarker = 'const serviceCommonScript = `';
  const commonStart = common.indexOf(commonMarker) + commonMarker.length;
  script = script.replace(glue, common.slice(commonStart, common.indexOf('`', commonStart)));
  const shell = fs.readFileSync(__dirname + '/../panel_shell.go', 'utf8'), plainMarker = 'const plainScript = `';
  const plainStart = shell.indexOf(plainMarker) + plainMarker.length;
  const plain = shell.slice(plainStart, shell.indexOf('`', plainStart)).split('<script>')[1].split('</script>')[0];
  return plain + '\n' + script;
}

// node is a fake element: attributes are kept as attributes and properties,
// so the page and the Panel rules read them either way.
function node(tag) {
  const n = {tagName: tag ? String(tag).toUpperCase() : undefined, textContent: '', className: '', hidden: false, disabled: false, checked: false,
    dataset: {}, children: [], attrs: {},
    append(...items) { this.children.push(...items); }, replaceChildren(...items) { this.children = [...items]; },
    setAttribute(name, value) { this.attrs[name] = String(value); this[name] = String(value); },
    getAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attrs, name) ? this.attrs[name] : null; },
    hasAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attrs, name); },
    removeAttribute(name) { delete this.attrs[name]; delete this[name]; }};
  return n;
}
const settled = async () => { for (let i = 0; i < 80; i++) await new Promise(resolve => setImmediate(resolve)); };
// A GB is 1024³ bytes, as oaBytes and Windows Explorer count it.
const GB = 1024 ** 3;
const lists = ['setup-app-rows', 'setup-chat-rows', 'setup-picture-rows', 'setup-engine-rows', 'setup-oa-rows'];

// Fixtures: this release's catalogue on a Strix Halo computer. The picture
// engines are pinned; Lemonade, OpenCode's own package and the OA build of
// ComfyUI are not (OpenAbstractions builds no ComfyUI, owner 2026-11-11).
const catalog = {choices: [{id: 'comfyui', application: true, title: 'ComfyUI', components: ['comfyui-app']}, {id: 'opencode', application: true, title: 'OpenCode', components: ['opencode-official']},
  {id: 'lemonade', title: 'Lemonade (OA build)', components: ['lemonade-engine']}, {id: 'thenoise', title: 'TheNoise', components: ['thenoise-engine']}, {id: 'pytorch', title: 'PyTorch', components: ['pytorch-engine']}],
  components: [{id: 'comfyui-app', title: 'ComfyUI with OpenAbstractions image tools', pending: 'OpenAbstractions no longer builds ComfyUI'},
    {id: 'lemonade-engine', pending: 'Not available in this release yet'}, {id: 'opencode-official', pending: 'Install OpenCode through OpenAbstractions Setup'},
    {id: 'thenoise-engine', title: 'TheNoise', artifact: {version: '0.9.1', sha256: 'a'.repeat(64), bytes: 1.1 * GB, publisher: 'lemonade-sdk', license: 'Apache-2.0 and bundled dependency licenses'}},
    {id: 'pytorch-engine', title: 'PyTorch', artifact: {version: '2.11.0', sha256: 'b'.repeat(64), bytes: 1.2 * GB, publisher: 'openabstractions', license: 'PSF-2.0'}}]};
const model = (id, title, bytes, extra = {}) => ({id, title, name: title, model: id, repository: 'black-forest-labs/' + id, revision: 'f'.repeat(40), source: 'https://huggingface.co/black-forest-labs/' + id,
  licenses: ['Apache-2.0'], download_bytes: bytes, peak_bytes: bytes + 2 * GB, assets: [{id: 'main', license_url: 'https://www.apache.org/licenses/LICENSE-2.0', download: {bytes, label: 'Main weights'}}], recommended: false, prepared: false, selected: false, caption: 'Full precision, larger', ...extra});
const compact = () => model('klein-compact', 'FLUX.2 Klein 4B compact', 8.27 * GB, {recommended: true, selected: true, caption: 'Recommended for this computer'});
const full = () => model('klein-full', 'FLUX.2 Klein 4B full precision', 16.1 * GB);
const zimage = () => model('z-image', 'Z-Image-Turbo', 20.7 * GB, {required_application: 'comfyui', caption: 'Photorealistic, for ComfyUI'});
// comfy is the overview's ComfyUI row: the person's own ComfyUI and the OA
// build an earlier release installed.
const comfy = (over = {}) => ({
  id: 'comfyui', title: 'ComfyUI', state: 'get', connectable: true, open: false, oa_build: false,
  download_page: 'https://www.comfy.org/download',
  retained: {id: 'comfyui-oa', component: 'comfyui-app', title: 'ComfyUI (previous version)', removal_title: 'previous version'},
  connect_step: 'comfyui-connect',
  actions: {connect: '/setup/comfy/connect', update: '/setup/comfy/connect', open: '/setup/comfy/open', folder: '/setup/comfy/folder',
    'remove-oa': '/setup/comfy/remove-oa-build'},
  publisher: 'Comfy Org', license: 'GPL-3.0', source: 'https://github.com/comfyanonymous/ComfyUI', finish: 'Open Comfy Desktop to finish its setup',
  ...over,
});
const overview = (over = {}) => ({version: '0.8.0', offered: ['thenoise', 'pytorch'], picture_engine: 'pytorch', picture_application: comfy(),
  installed: {}, picture_models: [compact(), full(), zimage()],
  coding_model: {...model('qwen', 'Qwen3.6 35B', 22.9 * GB), repository: 'unsloth/Qwen3.6-35B', source: 'https://huggingface.co/unsloth/Qwen3.6-35B', installed: false, used_by: []},
  desktop_integration: {app: 'opencode', coding: true, installed: false, reason: 'This build can integrate an existing OpenCode Desktop installation.'}, engines: engines(), ...over});
// engines are the overview's Engines rows on a Strix Halo computer, as
// setupEngines writes them; withLemonade adds the core's Lemonade provider.
const engines = (lemonade = null) => [{id: 'modelhost', title: 'llama.cpp', caption: 'Chat and embeddings', kind: 'included'}, ...(lemonade ? [{id: 'lemonade', title: 'Lemonade', caption: 'Pictures, speech and transcription', kind: 'provider', ready: !!lemonade.ready}] : []),
  {id: 'pytorch', title: 'Diffusers', caption: 'Pictures and video on AMD graphics', kind: 'package', can_remove: true}, {id: 'thenoise', title: 'TheNoise', caption: 'Pictures on the processor', kind: 'package', can_remove: true}];
const installedRow = id => ({id, effect: {phase: 'installed'}});
// ownerState: the owner's computer: the picture engine and TheNoise
// installed under this release's plan, OpenCode with Qwen3.6 35B, no
// picture model.
const ownerState = (over = {}) => ({approved: true, current_catalog: true, catalog, plan: {id: 'plan-owner', selected: ['pytorch', 'thenoise'], components: catalog.components.filter(c => ['pytorch-engine', 'thenoise-engine'].includes(c.id))},
  components: [installedRow('pytorch-engine'), installedRow('thenoise-engine')], ...over});
// oaBuildState: the owner after an update, with the OA build of ComfyUI an
// earlier release installed still recorded under the earlier plan.
const oaBuildState = (over = {}) => ownerState({current_catalog: false, plan: {id: 'plan-r27', selected: ['comfyui', 'pytorch', 'thenoise'], components: catalog.components.filter(c => ['comfyui-app', 'pytorch-engine', 'thenoise-engine'].includes(c.id))},
  components: [installedRow('pytorch-engine'), installedRow('thenoise-engine'), installedRow('comfyui-app')], ...over});
const ownerOverview = (over = {}) => overview({installed: {'pytorch-engine': {bytes: 1.67 * GB, folder: 'C:\\pt'}, 'thenoise-engine': {bytes: 4.82 * GB, folder: 'C:\\tn'}},
  coding_model: {...overview().coding_model, installed: true, used_by: ['OpenCode']}, desktop_integration: {app: 'opencode', coding: true, installed: true}, ...over});
const freshState = () => ({approved: false, current_catalog: true, catalog, plan: {}, components: []});
// plan is the server's review of a request: added packages are those not
// installed at their pins, the picture models those it names.
function planFor(body, installed = [], over = {}) {
  const offers = [compact(), full(), zimage()].map(m => ({...m, selected: (body.picture_model_plan_ids || []).includes(m.id)}));
  const added = [];
  for (const id of body.selected || []) for (const c of (catalog.choices.find(x => x.id === id) || {}).components || []) {
    if (installed.includes(c)) continue; const a = (catalog.components.find(x => x.id === c) || {}).artifact || {};
    added.push({component_id: c, download_bytes: Number(a.bytes || 0) + Number((((a.native_resources || {}).backend || {}).archive || {}).bytes || 0), disk_bytes: 2 * Number(a.bytes || 0)});
  }
  const coding = body.coding_model_plan_id ? overview().coding_model : null;
  return {id: (body.selected || []).length ? 'plan-' + body.selected.join('-') : 'coding-model-qwen', selected: body.selected || [], preparable: true, blockers: [], added,
    add_download_bytes: added.reduce((n, a) => n + a.download_bytes, 0), add_disk_bytes: added.reduce((n, a) => n + a.disk_bytes, 0),
    picture_models: (body.selected || []).length ? offers : [], picture_model: offers.find(m => m.selected && m.recommended) || null, coding_model: coding,
    permissions: (body.selected || []).includes('comfyui') ? [{app: 'comfyui', title: 'ComfyUI', id: 'perm-1', sentences: ['Make pictures on this computer.']}] : [],
    disk: {volume: 'C:', free_bytes: 400 * GB}, ...over};
}

// page starts the page with handle(path, body) answering each request and
// returns the fake DOM, every request and every notice.
async function page(handle, extra = {}) {
  const fields = new Map(), get = id => { if (!fields.has(id)) fields.set(id, node()); return fields.get(id); };
  const requests = [], notices = [], timers = [];
  const context = {
    document: {documentElement: {dataset: {}}, getElementById: get, createElement: tag => node(tag), querySelectorAll: () => []},
    location: {search: '?k=panel-key', hash: ''}, URLSearchParams, console, Promise,
    oaPictureWorkspace: {application: 'comfyui', title: 'ComfyUI', download: 'https://www.comfy.org/download', status: '/setup/comfy/status', register: '/setup/comfy/register'},
    setTimeout: fn => { timers.push(fn); return timers.length; }, clearTimeout: () => {},
    fetch: async (path, options) => {
      const body = options && options.body ? JSON.parse(options.body) : null; requests.push({path, body});
      if (path === '/setup/notice') { notices.push(body); return {ok: true, json: async () => ({logged: true})}; }
      const reply = await handle(path, body);
      if (reply === undefined) return {ok: false, text: async () => 'unexpected request ' + path};
      if (reply instanceof Error) return {ok: false, text: async () => reply.message};
      return {ok: true, json: async () => JSON.parse(JSON.stringify(reply))};
    },
    ...extra,
  };
  vm.runInNewContext(source(), context);
  await settled();
  const row = id => { const key = 'setup-row-' + id.replace(/[^a-z0-9-]/gi, '-'); for (const list of lists) { const hit = get(list).children.find(c => c.id === key); if (hit) return hit; } return undefined; };
  const parts = id => { const r = row(id); assert(r, 'no row ' + id); const summary = r.children[0], box = summary.children[0].tagName === 'INPUT' ? summary.children[0] : null, text = summary.children[1];
    return {details: r, box, title: text.children[0].textContent, caption: text.children[1].textContent, line: text.children[2].textContent, lineState: text.children[2].attrs['data-state'], bar: text.children[3], body: r.children[1]}; };
  const tick = async (id, on = true) => { const b = parts(id).box; b.checked = on; b.onchange(); await settled(); };
  const click = async () => { await get('setup-install').onclick(); await settled(); };
  const summary = () => ({sentence: get('setup-status').textContent, facts: get('setup-facts').textContent, button: get('setup-install').hidden ? '' : get('setup-install').textContent, disabled: get('setup-install').disabled});
  const rowIDs = list => get(list).children.map(c => c.id);
  // runTimers fires the page's pending timers n times, settling after each.
  // It fails when no timer was pending: the test never drove the page.
  const runTimers = async (n = 1) => { let fired = 0; for (let i = 0; i < n; i++) { const pending = timers.splice(0); fired += pending.length; for (const fn of pending) fn(); for (let j = 0; j < 5; j++) await settled(); } assert(fired > 0, 'runTimers: no timer was pending'); };
  return {get, row, parts, tick, click, summary, rowIDs, requests, notices, timers, runTimers, context, posts: path => requests.filter(r => r.path === path && r.body)};
}
// allText is every text under node, for a page-wide wording check.
function allText(n) { return [n.textContent || '', ...(n.children || []).map(allText)].join(' '); }
// facts lists a row's detail facts as "label: value" lines.
function facts(body) { return body.children.filter(c => c.tagName === 'DIV' && c.className === 'oa-row').map(c => c.children[0].children[0].textContent + ': ' + c.children[1].children.map(x => x.textContent).join(' ')); }

module.exports = {allText, comfy, oaBuildState, engines, page, settled, node, GB, catalog, overview, ownerOverview, ownerState, freshState, planFor, compact, full, zimage, facts, installedRow, lists};
