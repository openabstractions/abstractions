// Run with node monitor/work_notifications_page_test.js; no browser or service
// starts. Activity's Notifications group (work_page.go): a shown notification
// keeps its actions, one button each (actionable-notifications design §4.9);
// a click sends the action with the token it was shown with to
// /notifications/act, writes one sentence, and an Open Activity action points
// at its job's row. The answered row leaves on the reread and its sentence
// moves to the group.
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
assert(workGo.includes('`<div id="notifications-open"></div>`'), 'the group holds the shown notifications');

class FakeNode {
 constructor(tag) { this.tagName = String(tag).toUpperCase(); this.value = ''; this.textContent = ''; this.className = ''; this.title = ''; this.hidden = false; this.disabled = false; this.children = []; this.attrs = {}; }
 replaceChildren(...v) { this.children = [...v]; for (const c of v) c.parentNode = this; }
 append(...v) { for (const c of v) { if (c && c.parentNode && c.parentNode !== this) c.parentNode.removeChild(c); this.children.push(c); if (c && typeof c === 'object') c.parentNode = this; } }
 removeChild(c) { this.children = this.children.filter(x => x !== c); c.parentNode = null; return c; }
 setAttribute(k, v) { this.attrs[k] = String(v); }
 getAttribute(k) { return this.attrs[k]; }
 querySelector(sel) { return sel === 'tbody' ? this.tbody : null; }
 scrollIntoView() { this.scrolled = true; }
 addEventListener() {}
}
const fields = new Map();
function element(id) {
 if (!fields.has(id)) {
  const n = new FakeNode('div');
  if (id.endsWith('-table')) { n.tagName = 'TABLE'; n.tbody = new FakeNode('tbody'); n.append(n.tbody); n.parentNode = new FakeNode('div'); n.parentNode.className = 'oa-table'; }
  fields.set(id, n);
 }
 return fields.get(id);
}
const now = Date.now();
let record = { id: 'a1', title: 'Download finished', body: 'slow.bin is ready.', name: 'Pretend Lemonade', state: 'posted', at: now - 60e3, token: 't1',
 actions: [{ id: 'copy', title: 'Copy now' }, { id: 'open', title: 'Show in Activity', activity: true }] };
const acts = [];
const job = { Label: 'download slow.bin', State: 'complete', Receipt: { OperationID: 'op-1', LogicalOwner: 'owner', Identity: { Key: 'k', HistoryEpoch: 'e' } } };
const context = {
 URLSearchParams, console, Date, Math, Number, JSON, String, Promise, Set, Map, Error, setImmediate,
 document: { getElementById: element, createElement: t => new FakeNode(t), visibilityState: 'visible', addEventListener() {}, documentElement: { dataset: {} } },
 location: { search: '?k=test', href: '' },
 setInterval() { return 1; }, clearInterval() {},
 fetch: async (path, opts) => {
  const reply = v => ({ ok: true, json: async () => v });
  if (path === '/notifications/act') {
   const body = JSON.parse(opts.body); acts.push(body);
   const action = record.actions.find(a => a.id === body.action);
   record = { ...record, state: 'acted', action: action.title, token: undefined, actions: undefined };
   return reply({ outcome: 'acted', message: 'Chose ' + action.title + '.', operation: action.activity ? 'op-1' : '' });
  }
  if (path === '/notifications') return reply({ outcome: 'page', notifiers: [{ program: 'C:/pl.exe', name: 'Pretend Lemonade', week: 1, last_ms: now, off: false }], notifications: [record] });
  if (path.startsWith('/apps/registrations')) return reply({ outcome: 'page', entries: [] });
  if (path.startsWith('/inference/audit')) return reply({ Outcome: 'page', Entries: [], Next: 0, AtEnd: true });
  if (path.startsWith('/rights/answered')) return reply({ outcome: 'page', answers: [] });
  if (path.startsWith('/account-work')) return reply({ Outcome: 'page', Snapshots: [job], Next: '', Complete: true });
  if (path.startsWith('/inventory')) return reply({ Outcome: 'page', Snapshots: [], Next: '', Complete: true });
  if (path.startsWith('/binding')) return reply({ endpoint: 'e', history: { LogicalOwner: 'owner' } });
  if (path.startsWith('/logging')) return reply({ mode: 'read', outcome: 'page', records: [], atEnd: true, next: '' });
  throw Error('unexpected ' + path);
 },
};
context.window = context;
vm.runInNewContext(plain, context);
vm.runInNewContext(goConst('service_page.go', 'serviceCommonScript') + goConst('work_page.go', 'workScript') + goConst('logging_page.go', 'loggingScript'), context);

(async () => {
 await context.workReady;
 let open = element('notifications-open').children;
 assert.strictEqual(open.length, 1, 'a shown notification is listed with its actions');
 assert.strictEqual(open[0].label.textContent, 'Download finished');
 assert.strictEqual(open[0].caption.textContent, 'Pretend Lemonade · slow.bin is ready.');
 assert.deepStrictEqual(open[0].control.children.map(b => b.textContent), ['Copy now', 'Show in Activity']);
 assertPanelRules({ tagName: 'MAIN', children: [{ tagName: 'SECTION', children: [element('notifications-status'), element('notifications-open')] }] }, 'Activity, notifications');

 // Show in Activity: the action with its token, one sentence, and the job's row.
 await open[0].control.children[1].onclick();
 assert.deepStrictEqual(acts, [{ id: 'a1', action: 'open', token: 't1' }]);
 assert.strictEqual(element('notifications-open').children.length, 0, 'the answered notification leaves the list');
 assert.strictEqual(element('notifications-status').textContent, 'Chose Show in Activity.', 'its sentence moves to the group');
 const jobRow = element('account').children[0];
 assert.strictEqual(jobRow.attrs['aria-current'], 'true');
 assert.strictEqual(jobRow.status.textContent, 'This is the job the notification named.');
 const table = element('notifications-table').tbody.children.map(tr => tr.children.map(td => td.textContent));
 assert.deepStrictEqual(table[0].slice(1), ['Pretend Lemonade', 'Download finished', 'Chosen: Show in Activity']);
 // The reread keeps the action's sentence.
 await context.notifications();
 assert.strictEqual(element('notifications-status').textContent, 'Chose Show in Activity.');
 console.log('PASS: Activity lists a shown notification with its actions; a click sends the action with its token, writes one sentence and points at the job.');
})().catch(e => { console.error(e); process.exitCode = 1; });
