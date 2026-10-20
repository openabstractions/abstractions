// Run with node monitor/card_page_test.js; no browser or service is started.
// Device (card_page.go, SPEC §4 S6): one caption, no Refresh button, live
// rereads while visible, holders and wake holds as rows, wake holds with a
// switch.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/card_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
{
 const markup = go.slice(go.indexOf('var cardPage')).split('<script>')[0];
 assert(markup.includes(`"This device's graphics card and sleep."`), 'the page caption');
 assert(!/Refresh|Auto-refresh|id="auto"/.test(markup), 'Device updates live: no Refresh button or auto-refresh switch');
 assert(!markup.includes('id="downloads"'), 'downloads are listed on Activity');
}
const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const plainMarker = 'const plainScript = `';
const plainStart = shellGo.indexOf(plainMarker) + plainMarker.length;
const plainScript = shellGo.slice(plainStart, shellGo.indexOf('`', plainStart)).split('<script>')[1].split('</script>')[0];

class FakeNode {
 constructor(tag) { this.tagName = String(tag).toUpperCase(); this.value = ''; this.textContent = ''; this.className = ''; this.title = ''; this.checked = false; this.disabled = false; this.hidden = false; this.open = false; this.children = []; this.attrs = {}; this.listeners = {}; }
 replaceChildren(...v) { this.children = [...v]; for (const c of v) c.parentNode = this; }
 append(...v) { for (const c of v) { if (c && c.parentNode && c.parentNode !== this) c.parentNode.removeChild(c); this.children.push(c); if (c && typeof c === "object") c.parentNode = this; } }
 removeChild(c) { this.children = this.children.filter(x => x !== c); c.parentNode = null; return c; }
 setAttribute(k, v) { this.attrs[k] = String(v); }
 getAttribute(k) { return this.attrs[k]; }
 addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
 dispatch(type) { for (const fn of this.listeners[type] || []) fn(); }
}
const fields = new Map();
function element(id) { if (!fields.has(id)) fields.set(id, new FakeNode('div')); return fields.get(id); }

const calls = [];
const liveIntervals = new Map();
let nextIntervalId = 0;
const cardResources = { resources: [{
 resource: 'card:0', capacity: 25769803776, held: 22795929600, instrument: 'windows-gpu-counters',
 observed: new Date(Date.now() - 2000).toISOString(),
 holders: [
  { program: 'C:\\lms\\llama-server.exe', account: 'S-1-5-21-7-1001', amount: 21247127552, evidence: 'verified', since: '2026-09-22T09:00:00.000000Z', detail: '' },
  { program: 'host:pictures', account: '', amount: 0, evidence: 'claimed', since: '', detail: 'sdxl-checkpoint' },
 ] }] };
const awakeUnavailable = { outcome: 'unavailable', holds: [], reason: 'table: dial tcp: no runtime' };
const awakeTable = { outcome: 'page', source: 'table', holds: [
 { program: 'C:\\downloader.exe', account: 'S-1-5-21-7-1001', since: '2026-09-22T09:05:00.000000Z', lease: 'awake-1', why: 'a six-hour download' },
] };
const awakeChanged = { outcome: 'page', source: 'table', holds: [{ ...awakeTable.holds[0], why: 'a seven-hour download' }, { program: 'C:\\backup.exe', lease: 'awake-2', since: '2026-09-22T10:00:00Z' }] };
const replies = {
 '/card/table': [cardResources, cardResources, cardResources, cardResources],
 '/card/awake': [{ outcome: 'page', source: 'table', holds: [] }, awakeTable, awakeChanged, awakeUnavailable],
};
const context = {
 URLSearchParams, console, Date, Math, Number, JSON, String, Promise, Set,
 document: {
  getElementById: element, createElement: t => new FakeNode(t), visibilityState: 'visible', documentElement: { dataset: {} },
  addEventListener: (name, fn) => { if (name === 'visibilitychange') context.document.onVisibilityChange = fn; },
 },
 location: { search: '?k=test' },
 setInterval: (fn) => { const id = ++nextIntervalId; liveIntervals.set(id, fn); return id; },
 clearInterval: (id) => { liveIntervals.delete(id); },
 fetch: async (path, opts) => {
  assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
  calls.push(path);
  const queue = replies[path.split('?')[0]];
  if (!queue || !queue.length) throw new Error('no reply queued for ' + path);
  return { ok: true, json: async () => queue.shift() };
 },
};
context.window = context;
vm.runInNewContext(plainScript, context);
vm.runInNewContext(script, context);

assert.strictEqual(context.sampleAgePhrase(new Date(Date.now() - 1000).toISOString()), '1 second ago');
assert.strictEqual(context.sampleAgePhrase(new Date(Date.now() - 60000).toISOString()), '1 minute ago');
const text = id => element(id).textContent;
const tick = async () => { for (const fn of liveIntervals.values()) fn(); await new Promise(r => setTimeout(r, 0)); await new Promise(r => setTimeout(r, 0)); };

(async () => {
 await context.cardReady;
 assert.deepStrictEqual(calls, ['/card/awake', '/card/table?fresh=1'], 'both sections read on arrival, the card fresh');
 assert.strictEqual(text('device-answer'), 'llama-server is using 21 GB of the graphics card. llama-server holds it outside OpenAbstractions; it may keep it until it closes.');
 assert.strictEqual(context.deviceAnswer([{resource:'card:0', holders:[{program:'C:\\OA\\PictureApp\\python.exe', title:'Picture App', amount:18e9, lease:'gpu-7'}, {program:'C:\\lms\\llama-server.exe', amount:2e9}]}]),
  'Picture App is using 18 GB of the graphics card. One other app uses it too. OpenAbstractions can ask Picture App to let go when another app needs room.');
 assert.strictEqual(context.deviceAnswer([{resource:'memory', holders:[]}]), 'This device reports no graphics card.');
 assert(!/toLowerCase|toUpperCase/.test(script), 'the page compares no paths itself');
 assert.strictEqual(liveIntervals.size, 1, 'live updates armed on arrival');

 const block = element('resources').children[0];
 assert.strictEqual(block.children[0].label.textContent, 'Graphics card', 'the resource is a row, not a loose heading');
 assert.strictEqual(block.children[0].label.title, 'card:0');
 assert.match(block.children[0].caption.textContent, /^21\.23 GiB held of 24\.00 GiB\. Measured by Windows GPU counters/);
 const holders = block.children[1].children;
 assert.strictEqual(holders.length, 2);
 assert.strictEqual(holders[0].attrs['data-evidence'], 'verified');
 const label = holders[0].children[0].children[0];
 assert.strictEqual(label.textContent, 'llama-server', 'a holder reads by its app name');
 assert.strictEqual(label.title, 'C:\\lms\\llama-server.exe', 'the full program sits on the title only');
 assert.match(holders[0].children[0].children[1].textContent, /^19\.79 GiB measured · since /);
 assert.strictEqual(holders[1].attrs['data-evidence'], 'claimed');
 assert.strictEqual(holders[1].children[0].children[1].textContent, 'claimed · sdxl-checkpoint');
 assert.strictEqual(holders[1].children[0].children[0].textContent, 'Model server', 'an engine host id is never page text');
 assert.strictEqual(context.appName({program:'host:pictures', title:'Pictures'}), 'Pictures');
 const details = block.children[2];
 assert.strictEqual(details.children[0].textContent, 'Technical details');
 assert.match(details.children[1].textContent, /"resource": "card:0"/);
 // The empty awake list is one sentence.
 assert.strictEqual(element('awake-empty').hidden, false);
 assert.strictEqual(element('awake').children.length, 0);

 // A live tick reads the standing sample and the awake list; an opened
 // Technical details stays open.
 details.open = true; details.dispatch('toggle');
 await tick();
 assert.deepStrictEqual(calls.slice(-2), ['/card/table', '/card/awake']);
 assert.strictEqual(element('resources').children[0].children[2].open, true);
 assert.match(text('table-status'), /^Updated live; last sample taken (\d+ seconds?|less than a minute) ago\.$/, text('table-status'));
 const wake = element('awake').children;
 assert.strictEqual(wake.length, 1);
 assert.strictEqual(element('awake-empty').hidden, true);
 assert.strictEqual(wake[0].children[0].children[0].textContent, 'downloader');
 assert.match(wake[0].children[0].children[1].textContent, /^Keeping this device awake since .+ · a six-hour download$/);
 assert.strictEqual(wake[0].switch.checked, true, 'a wake hold reads On');
 const permissions = wake[0].children[1].children[1];
 assert.strictEqual(permissions.textContent, 'Permissions…');
 context.location.href = '';
 permissions.onclick();
 assert.strictEqual(wake[0].status.textContent, 'Opening permissions for downloader.');
 assert.match(context.location.href, /^\/rights\?k=test&program=C%3A%5Cdownloader\.exe&account=S-1-5-21-7-1001#rights-section$/);
 // The rendered page keeps the Panel rules (static/panel_rules.js).
 {
  const { assertPanelRules } = require('./static/panel_rules.js');
  const section = (...children) => Object.assign(new FakeNode('section'), { children });
  for (const id of ['device-answer', 'table-status', 'awake-status']) element(id).role = 'status';
  assertPanelRules([
   section(element('device-answer'), element('table-status'), element('resources')),
   section(element('awake-status'), element('awake-empty'), element('awake')),
  ], 'Device', { allowClasses: require('./panel_fake_dom_test_helper.js').allowlistedClasses('card') });
 }
 assert.strictEqual(wake[0].title, 'Lease awake-1', 'the lease id stays behind the row title');

 // A live tick where the server state changes (a new reason, a second hold)
 // keeps the clicked row, its button and its Permissions… sentence; holder
 // rows are kept the same way.
 const clicked = wake[0], holderBefore = element('resources').children[0].children[1].children[0];
 await tick();
 assert.strictEqual(element('awake').children[0], clicked, 'the same row element survives the reread');
 assert.strictEqual(clicked.status.textContent, 'Opening permissions for downloader.', 'and its sentence');
 assert.strictEqual(clicked.children[1].children[1], permissions, 'and its button');
 assert.match(clicked.children[0].children[1].textContent, / · a seven-hour download$/, 'the row text follows the server');
 assert.strictEqual(element('awake').children.length, 2, 'a new hold is added');
 assert.strictEqual(element('awake').children[1].children[0].children[0].textContent, 'backup');
 assert.strictEqual(element('resources').children[0].children[1].children[0], holderBefore, 'holder rows are kept in place');

 // An unavailable table reads a plain sentence.
 await tick();
 assert.strictEqual(text('awake-status'), "That service isn't running on this account right now. Start the runtime, or check the service on the Apps page.");

 // Hidden: the next tick stops the timer; visible again restarts it.
 context.document.visibilityState = 'hidden';
 await tick();
 assert.strictEqual(liveIntervals.size, 0, 'no live reads while hidden');
 assert.strictEqual(context.document.documentElement.dataset.ready, '1', 'Device marks itself ready after its first reads');
 assert(go.includes('data-ready-owner="page"'), 'Device owns its readiness');

 // An owner page whose first reads fail still marks itself ready, so a
 // screenshot of the failure is taken instead of waiting.
 {
  const failing = { ...context, fetch: async () => { throw new Error('dial: no runtime'); }, setInterval: () => 0,
   document: { ...context.document, documentElement: { dataset: {} } } };
  fields.clear();
  failing.window = failing;
  vm.createContext(failing);
  vm.runInContext(plainScript, failing);
  vm.runInContext(script, failing);
  await failing.cardReady.catch(() => {});
  await new Promise(r => setTimeout(r, 0));
  assert.strictEqual(failing.document.documentElement.dataset.ready, '1', 'a failed first read still sets ready');
  assert.strictEqual(text('device-answer'), 'The graphics card could not be read.');
 }
 // Off revokes the app's "may hold awake" permission, then rereads the list.
 {
  const { assertLiveUpdateKeepsRow } = require('./panel_fake_dom_test_helper.js');
  const hold = { program: 'C:\\downloader.exe', account: 'S-1-5-21-7-1001', since: '2026-09-22T09:05:00Z', lease: 'awake-1', why: 'a six-hour download' };
  const server = { holds: [hold], revision: 'r1', revokes: [], matched: false };
  const intervals = [];
  const wakeCtx = { ...context, setInterval: fn => { intervals.push(fn); return intervals.length; }, clearInterval() {},
   document: { ...context.document, visibilityState: 'visible', documentElement: { dataset: {} } }, location: { search: '?k=test', href: '' },
   fetch: async (path, opts) => {
    const reply = v => ({ ok: true, json: async () => v });
    if (path.startsWith('/card/table')) return reply(cardResources);
    if (path.startsWith('/card/awake')) return reply({ outcome: 'page', source: 'table', holds: server.holds.map(h => ({ ...h })) });
    if (path.startsWith('/rights?')) return reply({ Outcome: 'page', Revision: server.revision, Rules: [] });
    if (path === '/rights' && opts.method === 'POST') {
     const body = JSON.parse(opts.body); server.revokes.push(body);
     if (!server.matched) return reply({ Outcome: 'applied', Revision: server.revision });
     server.revision = 'r2'; server.holds = [];
     return reply({ Outcome: 'applied', Revision: 'r2' });
    }
    throw Error('unexpected ' + path);
   } };
  fields.clear();
  wakeCtx.window = wakeCtx;
  vm.createContext(wakeCtx);
  vm.runInContext(plainScript, wakeCtx);
  vm.runInContext(script, wakeCtx);
  await wakeCtx.cardReady;
  const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)); };
  const row = () => element('awake').children[0];
  const sw = row().switch;
  assert.strictEqual(sw.className, 'switch');
  // Nothing matched: the revoke left the revision as it was; the switch
  // stays On, the row says so, and a live tick keeps that sentence.
  const { sentence } = await assertLiveUpdateKeepsRow({
   click: async () => { sw.checked = false; await sw.onchange(); },
   change: () => { server.holds[0].why = 'a seven-hour download'; },
   data: () => server.holds,
   tick: async () => { await intervals[0](); await settle(); },
   row, status: r => r.status, label: 'Device, keep awake',
  });
  assert.strictEqual(sentence, 'Nothing matched: downloader holds no saved permission to keep this device awake, so its hold stays until it ends it.');
  assert.strictEqual(sw.checked, true, 'nothing matched leaves the switch On');
  assert.deepStrictEqual(server.revokes[0], { edit: 'revoke', revision: 'r1', account: 'S-1-5-21-7-1001', program: 'C:\\downloader.exe', action: 'abstraction.resource/hold', resource: 'awake' });
  // A matching revoke ends the hold: one sentence, then the reread drops the row.
  server.matched = true;
  sw.checked = false; await sw.onchange(); await settle();
  assert.strictEqual(server.revokes.length, 2);
  assert.deepStrictEqual(server.revokes[1], { edit: 'revoke', revision: 'r1', account: 'S-1-5-21-7-1001', program: 'C:\\downloader.exe', action: 'abstraction.resource/hold', resource: 'awake' });
  assert.strictEqual(element('awake').children.length, 0, 'the row goes away after the reread');
  assert.strictEqual(element('awake-empty').hidden, false);
  assert.strictEqual(element('awake-status').textContent, 'downloader may no longer keep this device awake.');
 }
 console.log('PASS card page');
})().catch(e => { console.error(e); process.exit(1); });
