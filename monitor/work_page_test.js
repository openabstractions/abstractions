// Run with node monitor/work_page_test.js; no browser or service is started.
// Activity (work_page.go, SPEC §4 S5): a timeline of model calls and answers
// narrowed by quick ranges and a registered-app chooser, downloads and
// background work, this Panel's work, each empty list one sentence, no
// Refresh button, and live rereads while the page is visible that keep each
// row, its status line and its focus.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const { assertPanelRules } = require('./static/panel_rules.js');

function goConst(file, name) {
 const src = fs.readFileSync(__dirname + '/' + file, 'utf8');
 const marker = 'const ' + name + ' = `';
 const start = src.indexOf(marker);
 assert(start >= 0, name + ' not found in ' + file);
 return src.slice(start + marker.length, src.indexOf('`', start + marker.length));
}
const plain = goConst('panel_shell.go', 'plainScript').split('<script>')[1].split('</script>')[0];
const workGo = fs.readFileSync(__dirname + '/work_page.go', 'utf8');
const markup = workGo.slice(workGo.indexOf('var workPage'), workGo.indexOf('const workScript'));
const script = goConst('work_page.go', 'workScript');

assert(!/Refresh|Next page/.test(markup), 'Activity updates live: no Refresh or Next page button');
assert(markup.includes('"What happened, and who allowed it."'), 'the page caption');
assert(markup.includes('oaExpander("Detailed log", loggingSection)'), 'the detailed log sits behind an expander');
assert(markup.indexOf('"timeline"') < markup.indexOf('"account-work"'), 'the timeline comes first');
assert(!/Download a file|id="submit"/.test(workGo), 'no download form on Activity');
assert(!/answerApp|sentence\.includes|const s=String/.test(script), 'answers name their app by program, never by matching the sentence');

class FakeNode {
 constructor(tag) { this.tagName = String(tag).toUpperCase(); this.value = ''; this.textContent = ''; this.className = ''; this.title = ''; this.hidden = false; this.disabled = false; this.children = []; this.attrs = {}; this.listeners = {}; }
 replaceChildren(...v) { this.children = [...v]; for (const c of v) c.parentNode = this; }
 append(...v) { for (const c of v) { if (c && c.parentNode && c.parentNode !== this) c.parentNode.removeChild(c); this.children.push(c); if (c && typeof c === 'object') c.parentNode = this; } }
 removeChild(c) { this.children = this.children.filter(x => x !== c); c.parentNode = null; return c; }
 setAttribute(k, v) { this.attrs[k] = String(v); }
 getAttribute(k) { return this.attrs[k]; }
 querySelector(sel) { return sel === 'tbody' ? this.tbody : null; }
 addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
}
const fields = new Map();
function element(id) {
 if (!fields.has(id)) {
  const n = new FakeNode('div');
  if (id === 'timeline-table') { n.tagName = 'TABLE'; n.tbody = new FakeNode('tbody'); n.append(n.tbody); n.parentNode = new FakeNode('div'); n.parentNode.className = 'oa-table'; }
  fields.set(id, n);
 }
 return fields.get(id);
}
const now = Date.now();
const iso = ms => new Date(ms).toISOString();
const coder = 'C:\\Apps\\Coder\\coder.exe', painter = 'C:\\Apps\\Painter\\painter.exe';
const apps = { outcome: 'page', entries: [
 { role: 'application', program: coder, name: 'Coder' },
 { role: 'application', program: painter, name: 'Painter' },
] };
// The audit holds an old page and a current one: the first pass reads both,
// past the few pages a later pass reads, so Last hour is current on arrival.
const old = n => ({ UnixMs: now - 9 * 86400e3, Program: painter, Model: 'old-' + n, Outcome: 'completed' });
const auditPages = {};
for (let i = 0; i < 10; i++) auditPages[i] = { Outcome: 'page', Entries: [old(i)], Next: i + 1, AtEnd: false };
auditPages[10] = { Outcome: 'page', Entries: [
 { UnixMs: now - 10 * 60e3, Program: coder, Model: 'qwen3-coder', Outcome: 'completed' },
 { UnixMs: now - 3 * 86400e3, Program: painter, Model: 'sdxl', Outcome: 'refused' },
], Next: 11, AtEnd: true };
const auditEmpty = { Outcome: 'page', Entries: [], Next: 11, AtEnd: true };
// /activity/answers pages the answer audit worded by the Panel: an
// assistant's answer, then on the next page the person's own answers.
const answerPages = {
 '': { outcome: 'page', next: 'n1', complete: false, events: [
  { at: now - 5 * 60e3, app: 'Coder', program: coder, what: 'Coder wants to look up models in the model registry.', result: 'Allowed by Helper' },
 ] },
 n1: { outcome: 'page', complete: true, events: [
  { at: now - 2 * 3600e3, app: 'Painter', program: painter, what: 'Asked: “Which size?”', result: 'Large, chosen by you' },
  { at: now - 3 * 86400e3, app: 'Coder', program: coder, what: 'Added itself to OpenAbstractions', result: 'Kept by you' },
 ] },
};
// /activity/starts pages the application start log.
const startsPage = { outcome: 'page', complete: true, events: [
 { at: now - 2 * 60e3, app: 'Coder', program: coder, what: 'App started', result: 'On its own' },
] };
const job = (id, label, state, extra) => ({ Label: label, State: state, Receipt: { OperationID: id, LogicalOwner: 'owner', Identity: { Key: 'k-' + id, HistoryEpoch: 'e' } }, ...extra });
const accountQueue = [
 { Outcome: 'page', Snapshots: [job('op-1', 'download model.gguf', 'running')], Next: 'c2', Complete: false },
];
const accountNext = { Outcome: 'page', Snapshots: [job('op-2', 'second job', 'accepted')], Next: '', Complete: true };
let accountDefault = { Outcome: 'page', Snapshots: [job('op-1', 'download model.gguf', 'running')], Next: 'c2', Complete: false };
const accountReplies = [{ Outcome: 'forbidden' }, { Outcome: 'requested' }, { Outcome: 'already_terminal' }];
const inventoryPage = { Outcome: 'page', Next: '', Complete: true, Snapshots: [
 job('op-a', 'huggingface.co · model.safetensors', 'running', { LabelDerived: true }),
 job('op-b', '', 'pending', { Waiting: 'network:metered' }),
 { ...job('op-c', 'elsewhere', 'complete'), Receipt: { OperationID: 'op-c', LogicalOwner: 'other-owner', Identity: { Key: 'k', HistoryEpoch: 'e' } } },
] };
const actionReplies = [{ Outcome: 'observed', Snapshot: { State: 'running' } }, { Outcome: 'forbidden' }];
const actionCalls = [];
const calls = [];
let intervals = [];
let hold = null; // when set, /account-work waits on this promise
const context = {
 URLSearchParams, console, Date, Math, Number, JSON, String, Promise, Set, Map, Error, setImmediate,
 document: { getElementById: element, createElement: t => new FakeNode(t), visibilityState: 'visible', addEventListener() {}, documentElement: { dataset: {} } },
 location: { search: '?k=test', href: '' },
 setInterval: fn => { intervals.push(fn); return intervals.length; }, clearInterval() {},
 fetch: async (path, opts) => {
  calls.push(path);
  const reply = v => ({ ok: true, json: async () => v });
  if (path.startsWith('/apps/registrations')) return reply(apps);
  if (path.startsWith('/inference/audit')) return reply(auditPages[Number(path.split('=')[1])] || auditEmpty);
  if (path.startsWith('/activity/answers?cursor=')) return reply(answerPages[decodeURIComponent(path.split('=')[1])]);
  if (path.startsWith('/activity/starts?cursor=')) return reply(startsPage);
  if (path.startsWith('/account-work')) {
   if (opts && opts.body) return reply(accountReplies.shift());
   if (hold) await hold;
   return reply(path.endsWith('=c2') ? accountNext : (accountQueue.shift() || accountDefault));
  }
  if (path.startsWith('/inventory')) return reply(inventoryPage);
  if (path.startsWith('/binding')) return reply({ endpoint: 'e', history: { LogicalOwner: 'owner' } });
  if (path.startsWith('/action')) { actionCalls.push(JSON.parse(opts.body)); return reply(actionReplies.shift()); }
  if (path.startsWith('/logging')) return reply({ mode: 'read', outcome: 'page', records: [], atEnd: true, next: '' });
  throw Error('unexpected ' + path);
 },
};
context.window = context;
vm.runInNewContext(plain, context);
vm.runInNewContext(goConst('service_page.go', 'serviceCommonScript') + script + goConst('logging_page.go', 'loggingScript'), context);

const rows = () => element('timeline-table').tbody.children.map(tr => tr.children.map(td => td.textContent));
const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setImmediate(r)); };
(async () => {
 await context.workReady;
 // The first pass read the audit to its end, past eight pages.
 assert(calls.includes('/inference/audit?cursor=10'), 'the first pass reads the audit until AtEnd');
 // Both owners were paged by cursor.
 assert(calls.includes('/activity/answers?cursor=n1'), 'the answer audit is read page by page');
 assert(calls.includes('/activity/starts?cursor='), 'the start log is read');
 assert(!calls.some(p => p.startsWith('/rights/answered')), 'Activity reads the answer audit, not the assistant-only list');
 // Last hour on arrival: the app start, the assistant's answer and the model
 // call, newest first, each naming its app.
 let r = rows();
 assert.strictEqual(r.length, 3, JSON.stringify(r));
 assert.deepStrictEqual(r[0].slice(1), ['Coder', 'App started', 'On its own']);
 assert.deepStrictEqual(r[1].slice(1), ['Coder', 'Coder wants to look up models in the model registry.', 'Allowed by Helper']);
 assert.deepStrictEqual(r[2].slice(1), ['Coder', 'Model call to qwen3-coder', 'Answered']);
 assert.strictEqual(element('timeline-empty').hidden, true);
 assert.deepStrictEqual(element('app-filter').children.map(o => o.textContent), ['All apps', 'Coder', 'Painter']);

 // 7 days brings the older call and the person's own answers in; the button
 // writes one sentence.
 assert.deepStrictEqual(element('range').children.map(b => b.textContent), ['Last hour', 'Today', '7 days']);
 element('range').children[2].onclick();
 assert.strictEqual(rows().length, 6);
 assert.strictEqual(element('range-status').textContent, 'Showing the last 7 days: 6 events.');
 assert(rows().some(x => x[3] === 'Large, chosen by you') && rows().some(x => x[3] === 'Kept by you'), 'the person\'s own answers are listed');
 // Painter: its own answer and its call, and its quiet last hour reads one
 // sentence.
 element('app-filter').value = painter; element('app-filter').onchange();
 assert.deepStrictEqual(rows().map(x => x[2]), ['Asked: “Which size?”', 'Model call to sdxl']);
 assert.strictEqual(element('range-status').textContent, 'Showing Painter: 2 events.');
 element('range').children[0].onclick();
 assert.strictEqual(element('timeline-empty').hidden, false);
 assert.strictEqual(element('timeline-empty').textContent, 'Nothing happened with Painter in the last hour.');
 element('app-filter').value = coder; element('app-filter').onchange();
 assert.deepStrictEqual(rows().map(x => x[2]), ['App started', 'Coder wants to look up models in the model registry.', 'Model call to qwen3-coder'], 'answers and starts match Coder by program');
 const keptRow = element('timeline-table').tbody.children[0];

 // This Panel's work: labels, (derived), waiting, ids on titles only.
 const inv = element('records').children;
 assert.deepStrictEqual(inv.map(x => x.label.textContent), ['huggingface.co · model.safetensors (derived)', 'Unlabelled job (waiting: network:metered)', 'elsewhere']);
 assert.deepStrictEqual(inv.map(x => x.label.title), ['op-a', 'op-b', 'op-c']);
 assert.deepStrictEqual(inv[0].control.children.map(b => b.textContent), ['Check state', 'Cancel', 'Download result']);
 assert(inv[2].control.children.every(b => b.disabled), 'work another runtime holds is read-only');
 assert.strictEqual(inv[2].status.textContent, 'Another runtime holds this work, so it is read-only here.');
 assert(inv[0].control.children.every(b => !b.disabled));
 await inv[0].control.children[0].onclick();
 assert.strictEqual(inv[0].status.textContent, 'State: running.');
 assert.deepStrictEqual(actionCalls[0], { action: 'observe', endpoint: 'e', owner: 'owner', identity: { Key: 'k-op-a', HistoryEpoch: 'e' } });
 await inv[0].control.children[1].onclick();
 assert.strictEqual(inv[0].status.textContent, 'You are not authorized to cancel this; nothing was changed.');
 inv[1].control.children[2].onclick();
 assert.strictEqual(inv[1].status.textContent, 'Downloading the result.');
 assert.match(context.location.href, /^\/result\?k=test&key=k-op-b&epoch=e&owner=owner&endpoint=e$/);
 assert.strictEqual(element('next').hidden, true, 'one page: no Show more');

 // Downloads and background work: Cancel writes into the row; refusals read
 // as sentences; the row and its sentence survive the live reread.
 const account = element('account').children;
 assert.strictEqual(account.length, 1);
 const first = account[0];
 assert.strictEqual(first.label.textContent, 'download model.gguf');
 assert.strictEqual(first.label.title, 'op-1', 'the id sits on the title only');
 await first.control.children[0].onclick();
 assert.strictEqual(first.status.textContent, 'You hold no rule for this; nothing was listed or changed.');
 // The shared live-update check (panel_fake_dom_test_helper.js): Cancel,
 // the job moves on at the server, a live tick; the row element and its
 // sentence stay.
 const { assertLiveUpdateKeepsRow } = require('./panel_fake_dom_test_helper.js');
 await assertLiveUpdateKeepsRow({
  click: () => first.control.children[0].onclick(),
  change: () => {
   context.document.activeElement = first.control.children[0];
   accountDefault = { ...accountDefault, Snapshots: [job('op-1', 'download model.gguf', 'cancelling')] };
  },
  data: () => accountDefault,
  tick: async () => { await intervals[0](); await settle(); },
  row: () => element('account').children[0],
  status: (row) => row.status,
  label: 'Activity, downloads and background work',
 });
 assert.strictEqual(first.status.textContent, 'Cancellation requested; the program that submitted it sees the end.');
 assert.strictEqual(first.parentNode, element('account'), 'and the focused button stays in the page');
 assert.strictEqual(element('records').children[0], inv[0], 'inventory rows are kept too');
 assert.strictEqual(inv[0].status.textContent, 'You are not authorized to cancel this; nothing was changed.');
 await first.control.children[0].onclick();
 assert.strictEqual(first.status.textContent, 'Already finished.');

 // Ticks do not overlap: a tick while a pass is reading is skipped.
 let release; hold = new Promise(res => { release = res; });
 const before = calls.length;
 const pass = intervals[0](); await settle();
 const during = calls.length;
 await intervals[0](); await settle();
 assert.strictEqual(calls.length, during, 'a second tick reads nothing while the first is in flight');
 release(); hold = null; await pass; await settle();
 assert(calls.slice(before).some(p => p.startsWith('/inference/audit')), 'a live tick rereads model calls');
 assert(!calls.slice(before).some(p => p.startsWith('/activity/')), 'answers and starts reread every 30 seconds, not every tick');
 assert.strictEqual(element('timeline-table').tbody.children[0], keptRow, 'a live reread keeps each timeline row');

 // Show more appears past the first page and hides at the end.
 assert.strictEqual(element('account-more').hidden, false);
 await element('account-more').onclick();
 assert.strictEqual(element('account').children.length, 2);
 assert.strictEqual(element('account-more').hidden, true);
 assert.strictEqual(element('account-status').textContent, 'Showing more work.');

 // The rendered page keeps the Panel rules.
 const section = (...children) => Object.assign(new FakeNode('section'), { children });
 for (const id of ['range-status', 'timeline-status', 'account-status', 'inventory-status']) element(id).role = 'status';
 const rangeRow = Object.assign(new FakeNode('div'), { className: 'oa-row', children: [element('range'), element('app-filter'), element('range-status')] });
 assertPanelRules([
  section(rangeRow, element('timeline-status'), element('timeline-empty'), element('timeline-table'), element('timeline-more')),
  section(element('account-status'), element('account-empty'), element('account'), element('account-more')),
  section(element('inventory-status'), element('records-empty'), element('records'), element('next')),
 ], 'Activity', {
  // Known values: testdata/panel_lint.allowlist's known-value rows for
  // Activity, each naming its owning fix.
  knownValues: require('./panel_fake_dom_test_helper.js').knownValues('work'),
  knownExact: true,
 });

 // A forbidden account listing reads one sentence and lists nothing.
 accountDefault = { Outcome: 'forbidden' };
 await intervals[0](); await settle();
 // Show more paged the list, so the live pass leaves it; read it as the
 // page does after a reload.
 await context.accountList(false);
 assert.strictEqual(element('account').children.length, 0);
 assert.strictEqual(element('account-status').textContent, 'You hold no rule for this; nothing was listed or changed.');
 assert.strictEqual(context.document.documentElement.dataset.ready, '1', 'Activity marks itself ready after its first reads');
 assert(workGo.includes('data-ready-owner="page"'), 'Activity owns its readiness');
 console.log('PASS work page');
})().catch(e => { console.error(e); process.exit(1); });
