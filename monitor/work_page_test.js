// Run with node monitor/work_page_test.js; no browser or service is started.
// Activity (work_page.go, SPEC §4 S5): a timeline of model calls and answers
// narrowed by quick ranges and a registered-app chooser, downloads and
// background work, this Panel's work, each empty list one sentence, no
// Refresh button, and live rereads while the page is visible that keep each
// row, its status line and its focus.
const fs = require('fs'), vm = require('./panel_record_test_helper.js').vm, assert = require('assert');
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
const script = goConst('activity_messages_page.go', 'messageActivityScript') +
 goConst('activity_agent_exits.go', 'agentExitActivityScript') + goConst('work_page.go', 'workScript');

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
  if (id === 'timeline-table'||id==='request-table') { n.tagName = 'TABLE'; n.tbody = new FakeNode('tbody'); n.append(n.tbody); n.parentNode = new FakeNode('div'); n.parentNode.className = 'oa-table'; }
  fields.set(id, n);
 }
 return fields.get(id);
}
const now = Date.now();
const iso = ms => new Date(ms).toISOString();
const coder = 'C:\\Apps\\Coder\\coder.exe', painter = 'C:\\Apps\\Painter\\painter.exe';
const apps = { outcome: 'page', entries: [
 { role: 'application', program: coder, name: 'internal-editor-key', title: 'Coder' },
 { role: 'application', program: painter, name: 'internal-painter-key', title: 'Painter' },
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
  { at: now - 2 * 3600e3, app: 'Painter', program: painter, what: 'Asked: “Which size?”', result: 'Large, chosen' },
  { at: now - 3 * 86400e3, app: 'Coder', program: coder, what: 'Added itself to OpenAbstractions', result: 'Kept' },
 ] },
};
// /activity/starts pages the application start log.
const startsPage = { outcome: 'page', complete: true, events: [
 { at: now - 2 * 60e3, app: 'Coder', program: coder, what: 'Became available', result: '' },
] };
// /activity/setup-grants lists the chat permissions Setup wrote; Undo
// removes one (activity_setup_grants.go).
const grantsPage = { outcome: 'page', complete: true, events: [] };
const undoCalls = [];
// /activity/app-requests lists the installer requests and the apps Setup's
// answer kept; a Keep line's Undo asks about those apps again
// (setup_step_keep.go).
const appRequestsPage = { outcome: 'page', complete: true, events: [] };
const elevatedPage = { outcome: 'page', complete: true, events: [] };
const memoryPage = { outcome: 'page', complete: true, events: [] };
const keepUndoCalls = [];
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
 { ...job('op-c', 'Copy to the other computer', 'complete'), Receipt: { OperationID: 'op-c', LogicalOwner: 'other-owner', Identity: { Key: 'k', HistoryEpoch: 'e' } } },
] };
const actionReplies = [{ Outcome: 'observed', Snapshot: { State: 'running' } }, { Outcome: 'forbidden' }];
const actionCalls = [];
const calls = [];
const storagePage={outcome:'page',events:[]}, restoreCalls=[];
let intervals = [];
let hold = null; // when set, /account-work waits on this promise
const context = {
 AbortController,
 URLSearchParams, console, Date, Math, Number, JSON, String, Promise, Set, Map, Error, setImmediate, addEventListener() {},
 document: { getElementById: element, createElement: t => new FakeNode(t), visibilityState: 'visible', addEventListener() {}, documentElement: { dataset: {} } },
 location: { search: '?k=test', href: '' },
 setInterval: fn => { intervals.push(fn); return intervals.length; }, clearInterval() {},
 fetch: async (path, opts) => {
  calls.push(path);
  const reply = v => ({ ok: true, json: async () => v });
  if (path.startsWith('/apps/registrations')) return reply(apps);
  if (path.startsWith('/inference/requests')) {const p=auditPages[Number(path.split('=')[1])] || auditEmpty;return reply({...p,Entries:(p.Entries||[]).map(r=>r.Entry?r:{Entry:r})});}
  if (path.startsWith('/activity/answers?cursor=')) return reply(answerPages[decodeURIComponent(path.split('=')[1])]);
  if (path.startsWith('/activity/starts?cursor=')) return reply(startsPage);
  if (path === '/activity/setup-grants') {
   if (opts && opts.body) { undoCalls.push(JSON.parse(opts.body)); grantsPage.events = []; return reply({ message: 'Undone: the app asks before its next chat.' }); }
   return reply(grantsPage);
  }
  if (path === '/activity/app-requests') {
   if (opts && opts.body) { keepUndoCalls.push(JSON.parse(opts.body)); appRequestsPage.events = []; return reply({ message: 'Undone: OpenAbstractions asks whether to keep OpenCode.' }); }
   return reply(appRequestsPage);
  }
  if (path === '/activity/elevated') return reply(elevatedPage);
  if (path === '/activity/memory') return reply(memoryPage);
  if (path === '/activity/storage') {
   if (opts && opts.body) {restoreCalls.push(JSON.parse(opts.body));delete storagePage.events[0].restore;storagePage.events[0].result='Restoring from the download archive';return reply({outcome:'queued'})}
   return reply(storagePage);
  }
  if (path.startsWith('/activity/messages?cursor=')) return reply({outcome:'page',events:[],next:'',atEnd:true});
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
 assert(calls.includes('/inference/requests?cursor=10'), 'the first pass reads requests until AtEnd');
 // Both owners were paged by cursor.
 assert(calls.includes('/activity/answers?cursor=n1'), 'the answer audit is read page by page');
 assert(calls.includes('/activity/starts?cursor='), 'the start log is read');
 assert(!calls.some(p => p.startsWith('/rights/answered')), 'Activity reads the answer audit, not the assistant-only list');
 // Last hour on arrival: the app start, the assistant's answer and the model
 // call, newest first, each naming its app.
 let r = rows();
 assert.strictEqual(r.length, 3, JSON.stringify(r));
 assert.deepStrictEqual(r[0].slice(1), ['Coder', 'Became available', '']);
 assert.deepStrictEqual(r[1].slice(1), ['Coder', 'Coder wants to look up models in the model registry.', 'Allowed by Helper']);
 assert.deepStrictEqual(r[2].slice(1), ['Coder', 'Model call to Qwen3 coder', 'Answered']);
 assert.strictEqual(element('timeline-empty').hidden, true);
 assert.deepStrictEqual(element('app-filter').children.map(o => o.textContent), ['All apps', 'Coder', 'Painter']);

 // 7 days brings the older call and the person's own answers in; the button
 // writes one sentence.
 assert.deepStrictEqual(element('range').children.map(b => b.textContent), ['Last hour', 'Today', '7 days']);
 element('range').children[2].onclick();
 assert.strictEqual(rows().length, 6);
 assert.strictEqual(element('range-status').textContent, 'Showing the last 7 days: 6 events.');
 assert(rows().some(x => x[3] === 'Large, chosen') && rows().some(x => x[3] === 'Kept'), 'the person\'s own answers are listed');
 // Painter: its own answer and its call, and its quiet last hour reads one
 // sentence.
 element('app-filter').value = painter; element('app-filter').onchange();
 assert.deepStrictEqual(rows().map(x => x[2]), ['Asked: “Which size?”', 'Model call to SDXL']);
 assert.strictEqual(element('range-status').textContent, 'Showing Painter: 2 events.');
 element('range').children[0].onclick();
 assert.strictEqual(element('timeline-empty').hidden, false);
 assert.strictEqual(element('timeline-empty').textContent, 'Nothing happened with Painter in the last hour.');
 element('app-filter').value = coder; element('app-filter').onchange();
 assert.deepStrictEqual(rows().map(x => x[2]), ['Became available', 'Coder wants to look up models in the model registry.', 'Model call to Qwen3 coder'], 'answers and starts match Coder by program');
 const keptRow = element('timeline-table').tbody.children[0];

 // This Panel's work: labels, (derived), waiting, ids on titles only.
 const inv = element('records').children;
 assert.deepStrictEqual(inv.map(x => x.label.textContent), ['huggingface.co · model.safetensors (derived)', 'Unlabelled job (waiting for an unmetered network)', 'Copy to the other computer']);
 assert.deepStrictEqual(inv.map(x => x.caption.textContent), ['Running', 'Waiting to start', 'Done'], 'job states read in words');
 assert.deepStrictEqual(inv.map(x => x.label.title), ['op-a', 'op-b', 'op-c']);
 assert.deepStrictEqual(inv[0].control.children.map(b => b.textContent), ['Check state', 'Cancel', 'Download result']);
 assert(inv[2].control.children.every(b => b.disabled), 'work another runtime holds is read-only');
 assert.strictEqual(inv[2].status.textContent, 'Another runtime holds this work, so it is read-only here.');
 assert(inv[0].control.children.every(b => !b.disabled));
 await inv[0].control.children[0].onclick();
 assert.strictEqual(inv[0].status.textContent, 'State: Running.');
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
 // A finished job offers no Cancel.
 assert.strictEqual(context.accountRow(job('op-done', 'model.gguf', 'complete')).cancel.hidden, true, 'a complete download has no Cancel');
 assert.strictEqual(context.accountRow(job('op-run', 'model.gguf', 'running')).cancel.hidden, false, 'a running download keeps Cancel');

 // Ticks do not overlap: a tick while a pass is reading is skipped.
 let release; hold = new Promise(res => { release = res; });
 const before = calls.length;
 const pass = intervals[0](); await settle();
 const during = calls.length;
 await intervals[0](); await settle();
 assert.strictEqual(calls.length, during, 'a second tick reads nothing while the first is in flight');
 release(); hold = null; await pass; await settle();
 assert(calls.slice(before).some(p => p.startsWith('/inference/requests')), 'a live tick rereads model calls');
 assert(!calls.slice(before).some(p => p.startsWith('/activity/answers') || p.startsWith('/activity/starts')), 'answers and starts reread every 30 seconds, not every tick');
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
  naming: true,
  // Known values: testdata/panel_lint.allowlist's known-value rows for
  // Activity, each naming its owning fix.
  knownValues: require('./panel_fake_dom_test_helper.js').knownValues('work'),
  knownExact: true,
 });

 // A chat permission Setup wrote is one timeline row with Undo (CONV-U1);
 // Undo removes exactly that rule and the row goes with it.
 const setupWhat = 'Setup let it use the coding model on this computer';
 const undo = { program: coder, action: 'abstraction.inference/complete', resource: 'host:modelhost' };
 grantsPage.events = [{ at: now - 60e3, app: 'Coder', program: coder, what: setupWhat, result: 'Allowed', undo }];
 const RealDate = Date;
 context.Date = class extends RealDate { static now() { return RealDate.now() + 31e3; } };
 await intervals[0](); await settle();
 const grantRow = element('timeline-table').tbody.children.find(tr => tr.children[2].textContent === setupWhat);
 assert(grantRow, 'the Setup permission is a timeline row');
 const cell = grantRow.children[3];
 assert.strictEqual(cell.undoText.textContent, 'Allowed ');
 assert.strictEqual(cell.undoButton.textContent, 'Undo');
 await cell.undoButton.onclick(); await settle();
 assert.deepStrictEqual(undoCalls, [undo], 'Undo names exactly that rule');
 assert.strictEqual(element('timeline-status').textContent, 'Undone: the app asks before its next chat.');
 assert(!element('timeline-table').tbody.children.some(tr => tr.children[2].textContent === setupWhat), 'the row goes with the rule');
 // The apps the person's Set up answer kept are one line with Undo; Undo
 // posts that line to its own route and the line loses its Undo.
 const keptWhat = 'Setup kept it when you chose Set up';
 const keepUndo = { at: now - 30e3, apps: ['opencode'] };
 appRequestsPage.events = [{ at: now - 30e3, app: 'OpenCode', program: coder, what: keptWhat, result: 'Kept', undo: keepUndo }];
 context.Date = class extends RealDate { static now() { return RealDate.now() + 62e3; } };
 await intervals[0](); await settle();
 const keepLineRow = element('timeline-table').tbody.children.find(tr => tr.children[2].textContent === keptWhat);
 assert(keepLineRow, 'the Setup Keep is a timeline row');
 assert.strictEqual(keepLineRow.children[3].undoButton.textContent, 'Undo');
 await keepLineRow.children[3].undoButton.onclick(); await settle();
 assert.deepStrictEqual(keepUndoCalls, [keepUndo], 'Undo names the line and its apps');
 assert.deepStrictEqual(undoCalls.length, 1, 'a Keep Undo removes no Setup permission');
 assert.strictEqual(element('timeline-status').textContent, 'Undone: OpenAbstractions asks whether to keep OpenCode.');
 // A program run as administrator is one line: it ran as administrator and
 // OpenAbstractions didn't ask (rights RIGHTS-R27).
 elevatedPage.events = [{ at: now - 20e3, app: 'Tool', program: coder, what: 'Ran as administrator', result: "OpenAbstractions didn't ask" }];
 context.Date = class extends RealDate { static now() { return RealDate.now() + 124e3; } };
 await intervals[0](); await settle();
 const adminRow = element('timeline-table').tbody.children.find(tr => tr.children[2].textContent === 'Ran as administrator');
 assert(adminRow, 'the administrator line is a timeline row');
 assert.strictEqual(adminRow.children[3].textContent, "OpenAbstractions didn't ask");
 // A Memory line is one sentence under the source title Memory; the app
 // filter finds it by either program it names (ruling 3).
 const freed = 'OpenAbstractions freed memory in Painter (Wan 2.2) for LTX-2.5, which Coder asked for.';
 memoryPage.events = [{ at: now - 10e3, app: 'Memory', program: coder, also: painter, what: freed, result: '' }];
 context.Date = class extends RealDate { static now() { return RealDate.now() + 186e3; } };
 await intervals[0](); await settle();
 const memoryRow = element('timeline-table').tbody.children.find(tr => tr.children[2].textContent === freed);
 assert(memoryRow, 'the Memory line is a timeline row');
 assert.strictEqual(memoryRow.children[1].textContent, 'Memory');
 assert.strictEqual(memoryRow.children[3].textContent, '');
 assert.strictEqual(context.timelineItems().filter(i => i.what === freed).length, 1);
 element('app-filter').value = painter; element('app-filter').onchange();
 assert(context.timelineItems().some(i => i.what === freed), 'the holder app filter finds the Memory line');
 element('app-filter').value = ''; element('app-filter').onchange();
 context.Date = RealDate;

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

 // The request view uses these same audited records, with historical machine
 // provenance kept distinct from an incoming remote caller's domain.
 auditPages[11]={Outcome:'page',Entries:[
  {Entry:{Sequence:100,UnixMs:now-1000,Route:'native',Program:coder,Host:'lab',Domain:'Lab workstation',Model:'qwen3',Outcome:'completed'},Placement:'remote'},
  {Entry:{Sequence:101,UnixMs:now-900,Route:'remote',Program:'remote:opaque',Host:'modelhost',Domain:'Caller computer',Claim:'C:\\private\\claim.exe',Model:'qwen3',Outcome:'completed'},Placement:'local'},
  {Sequence:102,UnixMs:now-800,Route:'window',Program:coder,Host:'',Model:'',Outcome:'not_permitted',Reason:'policy:declined'},
  {Sequence:103,UnixMs:now-700,Route:'native',Program:coder,Host:'online-provider',Model:'C:\\private\\0123456789abcdef0123456789abcdef.gguf',Outcome:'unavailable',Reason:'upstream:stream'},
  {Entry:{Sequence:104,UnixMs:now-600,Route:'native',Program:coder,Host:'local',Model:'local-model',Outcome:'completed'},PreferredModel:'cloud-model',UnavailableHosts:['cloud'],Selection:'preferred_route_unavailable',Placement:'local'},
  {Entry:{Sequence:105,UnixMs:now-500,Route:'native',Program:coder,Host:'cloud',Model:'hosted-model',Outcome:'completed'},Placement:'hosted'},
 ],Next:12,AtEnd:true};
 element('app-filter').value='';element('app-filter').onchange();
 await context.timeline();
 const requests=()=>element('request-table').tbody.children.map(tr=>tr.children.map(td=>td.textContent));
 const actual=requests();
 assert(actual.some(row=>row[2]==='Lab workstation'&&row[4]==='Qwen3'&&row[7]==='Answered'),'outbound recorded destination is visible');
 assert(actual.some(row=>row[1]==='Remote app'&&row[2]==='This computer'),'incoming caller domain is never labeled the execution computer');
 assert(actual.some(row=>row[2]==='Not recorded'&&row[4]==='Not recorded'&&row[7]==='Not permitted'),'pre-admission refusal stays visible without invented destination/model');
 assert(actual.some(row=>row[2]==='Not recorded'&&row[4]==='Unnamed model'&&row[7]==='Unavailable'),'unknown placement and hash-only model stay honest');
 assert(actual.some(row=>row[2]==='This computer'&&row[4]==='Local model'&&row[5]==='Cloud model'&&row[6]==='Preferred route unavailable (Cloud)'),'fallback and placement use recorded request metadata');
 assert(actual.some(row=>row[2]==='Hosted'&&row[4]==='Hosted model'),'hosted placement');
 assert(actual.some(row=>row[3]==='llama.cpp'),'a model server reads by its upstream name');
 assert(!actual.flat().some(text=>text.includes('0123456789abcdef')||text.includes('claim.exe')||text==='Caller computer'),'raw identities stay out of ordinary labels');
 assert(element('request-details').textContent.includes('policy:declined'),'exact refusal reason remains inspectable');
 assert(!calls.some(path=>path.startsWith('/inference/hosts')||path.startsWith('/registry/view')),'current configuration is never used to guess past execution placement');
 element('request-outcome').value='not_permitted';element('request-outcome').onchange();
 assert.strictEqual(requests().length,1,'result filter');
 element('request-outcome').value='';element('request-outcome').onchange();
 element('request-machine').value='Lab workstation';element('request-machine').onchange();
 assert.strictEqual(requests().length,1,'computer filter');
 element('request-machine').value='';element('request-machine').onchange();
 element('request-model').value='qwen3';element('request-model').onchange();
 assert.strictEqual(requests().length,2,'model filter');
 assertPanelRules([element('request-table'),element('request-machine'),element('request-model'),element('request-outcome')],'Model requests',{naming:true});
 auditPages[12]={Outcome:'forbidden'};
 await context.timeline();
 assert.strictEqual(requests().length,0,'a refused history read clears prior request disclosure');
 assert.strictEqual(element('request-details').textContent,'','refusal clears raw details too');
 assert.strictEqual(element('request-tech').hidden,true,'no empty technical disclosure');
 assert.strictEqual(element('request-status').textContent,'This Panel may not read model requests.');
 const restarts=calls.filter(path=>path==='/inference/requests?cursor=0').length;
 delete auditPages[12];delete auditPages[11];
 await context.timeline();
 assert(calls.filter(path=>path==='/inference/requests?cursor=0').length>restarts,'permission recovery rereads the retained history');
 auditPages[11]={Outcome:'unavailable',Entries:[],Next:11,AtEnd:false};
 await context.timeline();
 assert.strictEqual(requests().length,0,'whole-page unavailability clears old request disclosure');
 assert.strictEqual(element('request-details').textContent,'','oversize/unavailable page keeps no partial details');
 const selection='a'.repeat(32)+'.'+'b'.repeat(64);
 storagePage.events=[{at:Date.now()-100,app:'Storage',what:'Removed one',result:'Removed',restore:selection},
  {at:Date.now()-200,app:'Storage',what:'Removed another',result:'Removed'}];
 vm.runInNewContext('answersAt=0; appFilter=""',context);
 await context.timeline();
 const storageRows=element('timeline-table').tbody.children.filter(tr=>tr.children[1].textContent==='Storage');
 assert.strictEqual(storageRows.length,2);
 const firstStorage=storageRows.find(tr=>tr.children[2].textContent==='Removed one').children[3];
 const otherStorage=storageRows.find(tr=>tr.children[2].textContent==='Removed another').children[3];
 assert.strictEqual(otherStorage.undoButton,null,'a removal without an archive proof has no action');
 assert.strictEqual(firstStorage.undoButton.textContent,'Restore');
 await firstStorage.undoButton.onclick();
 assert.deepStrictEqual(restoreCalls,[{occurrence:selection}],'only the opaque selection reaches the restore action');
 const restoreResult=()=>element('timeline-table').tbody.children.find(tr=>tr.children[2].textContent==='Removed one').children[3];
 assert.strictEqual(restoreResult().undoButton,null,'the queued restore no longer offers its action');
 assert.strictEqual(restoreResult().textContent,'Restoring from the download archive');
 storagePage.events[0].result='Restored from the download archive';
 vm.runInNewContext('answersAt=0',context);
 await context.timeline();
 assert.strictEqual(restoreResult().textContent,'Restored from the download archive');
 console.log('PASS work page');
})().catch(e => { console.error(e); process.exit(1); });
