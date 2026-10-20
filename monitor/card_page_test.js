// Run with node monitor/card_page_test.js; no browser or service is started.
// Device (card_page.go, SPEC §4 S6): one caption, no Refresh button, live
// rereads while visible, holders, graphics driver resets and wake apps as
// rows, each wake app with a switch that allows or stops it.
const fs = require('fs'), vm = require('./panel_record_test_helper.js').vm, assert = require('assert');
const go = fs.readFileSync(__dirname + '/card_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
{
 const markup = go.slice(go.indexOf('var cardPage')).split('<script>')[0];
 assert(markup.includes(`"This device's graphics card, sleep and storage."`), 'the page caption');
 assert(!/Refresh|Auto-refresh|id="auto"/.test(markup), 'Device updates live: no Refresh button or auto-refresh switch');
 assert(!markup.includes('id="downloads"'), 'downloads are listed on Activity');
 assert(markup.includes('Apps keep this device awake while they work.'), 'provider-held wake (the 0.4.0 exception) is stated in the section caption');
 assert(!markup.includes('provisional'), 'the section states what happens, with no release-status wording');
 assert(!markup.includes('GiB') && !go.includes("' GiB'"), 'one unit system: the platform\'s GB');
 assert(markup.includes('storageSection'), 'the Storage section is on the page (storage_page.go)');
 assert(markup.includes('id="gpu-faults"'), 'graphics driver resets sit in the graphics card section');
 assert(markup.includes('Checking the log for graphics driver resets…'), 'on arrival the reset line says the log is being read');
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
const wakeUnavailable = { outcome: 'unavailable', apps: [], rules: 'unread', reason: 'table: dial tcp: no runtime' };
const wakeTable = { outcome: 'page', rules: 'read', apps: [
 { program: 'C:\\downloader.exe', account: 'S-1-5-21-7-1001', holding: true, display: false, since: '2026-09-22T09:05:00.000000Z', why: 'a six-hour download', rule: 'none' },
 { program: 'C:\\Lemonade\\lemonaded.exe', account: 'S-1-5-21-7-1001', title: 'Lemonade', holding: true, display: true, since: '2026-09-22T09:00:00Z', why: 'chat', rule: 'allowed' },
 { program: 'C:\\idle.exe', account: 'S-1-5-21-7-1001', holding: false, display: false, rule: 'stopped' },
] };
const wakeChanged = { outcome: 'page', rules: 'forbidden', apps: [{ ...wakeTable.apps[0], why: 'a seven-hour download' }, { program: 'C:\\backup.exe', account: 'S-1-5-21-7-1001', holding: true, since: '2026-09-22T10:00:00Z', rule: 'none' }] };
const fault = { at: '2026-09-22T09:30:00Z', line: 'The graphics driver reset; sd-server was stopped and the request that was running is paused.', by: 'modelhostd' };
const replies = {
 '/card/storage': Array.from({length:4},()=>({classes:[{class:'data',bytes:1000},{class:'tmp',bytes:0},{class:'cache',bytes:0}],unclassified:0,disk:{volume:"C:",free_bytes:45097156608},large:[{name:"Model weights",store:"models",bytes:2000000000}],matches:[{name:"Model weights",bytes:2000000000,locations:2}]})),
 '/card/storage/logs': [{outcome:'available',usage:{retained_bytes:0}},{outcome:'available',usage:{retained_bytes:0,max_use:0,max_retention_ms:0}},{outcome:'unavailable'},{outcome:'unavailable'}],
 '/card/table': [cardResources, cardResources, cardResources, cardResources],
 '/card/wake': [{ outcome: 'page', rules: 'read', apps: [] }, wakeTable, wakeChanged, wakeUnavailable],
 '/card/faults': [{ outcome: 'page', faults: [] }, { outcome: 'reading', faults: [fault] }, { outcome: 'page', faults: [fault] }, { outcome: 'unavailable', faults: [] }],
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
 assert.deepStrictEqual(calls, ['/card/wake', '/card/table?fresh=1', '/card/faults', '/card/storage', '/card/storage/logs'], 'every section reads on arrival, the card fresh');
 assert.match(text('storage-keep-logs'), /^No size limit; no age limit\./, 'absent budgets mean unlimited');
 assert.strictEqual(text('storage-status'),'','loaded sizes add no status sentence');
 assert.strictEqual(element('storage-items').hidden,false);
 assert.strictEqual(text('storage-free'),'42 GB free on C:');
 assert.strictEqual(element('storage-free').hidden,false);
 assert.match(element('storage-large').children[0].children[0].children[1].textContent,/models/);
 assert.match(element('storage-matches').children[0].children[0].children[0].textContent,/Model weights.*2 copies/);
 assert.strictEqual(element('gpu-faults').children.length, 0, 'no reset, no line');
 assert.strictEqual(text('gpu-faults-status'), '', 'a read log with no reset says nothing');
 assert.strictEqual(text('device-answer'), 'Llama server is using 19.8 GB of the graphics card. Llama server holds it outside OpenAbstractions; it may keep it until it closes.');
 assert.strictEqual(context.deviceAnswer([{resource:'card:0', holders:[{program:'C:\\OA\\PictureApp\\python.exe', title:'Picture App', amount:18e9, evidence:'verified', lease:'gpu-7'}, {program:'C:\\lms\\llama-server.exe', amount:2e9}]}]),
  'Picture App is using 16.8 GB of the graphics card. One other app uses it too. OpenAbstractions can ask Picture App to let go when another app needs room.');
 assert.strictEqual(context.deviceAnswer([{resource:'memory', holders:[]}]), 'This device reports no graphics card.');
 assert.strictEqual(context.deviceAnswer([{resource:'card:0',holders:[]}]),'No app is listed as using the graphics card.','an empty list promises no free capacity');
 assert.match(context.deviceAnswer([{resource:'card:0',holders:[{title:'Render Queue',program:'host:opaque-id',amount:1e9,evidence:'claimed'}]}]),/^Render Queue reports using 954 MB/,'an app report is not presented as a measurement');
 assert(!/toLowerCase|toUpperCase/.test(script), 'the page compares no paths itself');
 assert.strictEqual(liveIntervals.size, 1, 'live updates armed on arrival');

 const block = element('resources').children[0];
 assert.strictEqual(block.children[0].label.textContent, 'Graphics card', 'the resource is a row, not a loose heading');
 assert.strictEqual(block.children[0].label.title, 'card:0');
 assert.match(block.children[0].caption.textContent, /^21\.2 GB held of 24 GB\.$/);
 assert.match(block.children[2].children[1].children[0].textContent, /^Measured by Windows GPU counters/, 'how it was measured sits behind Technical details');
 const holders = block.children[1].children;
 assert.strictEqual(holders.length, 2);
 assert.strictEqual(holders[0].attrs['data-evidence'], 'verified');
 const label = holders[0].children[0].children[0];
 assert.strictEqual(label.textContent, 'Llama server', 'a holder no title names reads by its file stem in words');
 assert.strictEqual(label.title, 'C:\\lms\\llama-server.exe', 'the full program sits on the title only');
 assert.match(holders[0].children[0].children[1].textContent, /^19\.8 GB measured · since /);
 // Memory no lease covers carries ruling 5's sentence beside its row.
 assert.match(holders[0].children[0].children[1].textContent, / · Llama server holds 20 GB that OpenAbstractions doesn't manage\. Unload it in Llama server, or close Llama server\.$/);
 assert.strictEqual(context.foreignWords('card:0', { program: 'C:\\Ollama\\ollama.exe', amount: 20 * 2 ** 30, evidence: 'verified' }),
  "Ollama holds 20 GB that OpenAbstractions doesn't manage. Unload it in Ollama, or close Ollama.");
 assert.strictEqual(context.foreignWords('card:0', { program: 'C:\\OA\\comfy\\python.exe', amount: 20 * 2 ** 30, evidence: 'verified', lease: 'card-7' }, 'card-7'), '', 'a leased row is managed');
 assert.strictEqual(context.foreignWords('card:0', { program: 'dwm.exe', amount: 2 ** 30, evidence: 'verified' }), '', 'a bare Windows process is left alone');
 assert.strictEqual(context.foreignWords('card:0', { program: 'C:\\Edge\\msedge.exe', amount: 300 * 2 ** 20, evidence: 'verified' }), '', 'under half a GB reads no rounded amount');
 assert.strictEqual(context.foreignWords('card:0', { program: 'host:pictures', amount: 0, evidence: 'claimed' }), '');
 assert.strictEqual(holders[1].children[0].children[1].textContent.includes('manage'), false, 'a claimed row carries no sentence');
 assert.strictEqual(holders[1].attrs['data-evidence'], 'claimed');
 assert.strictEqual(holders[1].children[0].children[1].textContent, 'reported by app · SDXL checkpoint');
 const missingSample=context.resourceBlock({resource:'card:2',held:0,capacity:0,holders:[]});
 assert(!/undefined|null|Measured by\s*\./.test(missingSample.children[0].caption.textContent),'missing measurement metadata never becomes an empty field');
 assert.strictEqual(missingSample.children[0].caption.textContent,'Not measured.');
 assert.strictEqual(holders[1].children[0].children[0].textContent, 'Pictures', 'an engine host id reads by its engine name (oaEngineName)');
 assert.strictEqual(context.appName({program:'host:pictures', title:'Pictures'}), 'Pictures');
 const details = block.children[2];
 assert.strictEqual(details.children[0].textContent, 'Technical details');
 assert.strictEqual(details.className, 'oa-expander', 'the same expander style as every other Technical details');
 assert.match(details.children[1].children[0].textContent, /"resource": "card:0"/);
 // The empty awake list is one sentence.
 assert.strictEqual(element('awake-empty').hidden, false);
 assert.strictEqual(element('awake').children.length, 0);

 // A live tick reads the standing sample, the resets and the wake list; an
 // opened Technical details stays open.
 details.open = true; details.dispatch('toggle');
 await tick();
 assert.deepStrictEqual(calls.slice(-5), ['/card/table', '/card/faults', '/card/wake', '/card/storage', '/card/storage/logs']);
 assert.match(text('storage-keep-logs'), /^Up to 0 bytes; kept up to 0 days\./, 'present zero limits stay zero');
 replies['/card/storage'].unshift({classes:[],unclassified:1073741824});
 await context.storage();
 assert.strictEqual(text('storage-status'),"1 GB isn't sorted into a type yet.");
 assert.strictEqual(element('storage-status').attrs['data-state'], 'ok', 'a read storage size ends the loading state, so the page settles');
 replies['/card/storage/logs'].unshift({outcome:'available',usage:{retained_bytes:0,oldest:'2026-09-22T09:00:00Z'}});
 await context.storageLogs();
 assert.match(text('storage-keep-logs'),/Oldest log: /,'the retained time uses the plain log label');
 assert.strictEqual(element('resources').children[0].children[2].open, true);
 assert.match(text('table-status'), /^Measured by Windows (\d+ seconds?|less than a minute) ago\.$/, text('table-status'));
 // A reset is one row: when, then the fault's own sentence and its writer.
 const faultRow = element('gpu-faults').children[0];
 assert.strictEqual(element('gpu-faults').children.length, 1);
 assert.match(faultRow.label.textContent, /^Graphics driver reset /);
 assert.strictEqual(faultRow.caption.textContent, fault.line + ' Reported by modelhostd.');
 assert.strictEqual(text('gpu-faults-status'), 'Checking the log for graphics driver resets…', 'a scan still behind says so');
 const wake = element('awake').children;
 assert.strictEqual(wake.length, 3, 'holders and rule-only apps alike');
 assert.strictEqual(element('awake-empty').hidden, true);
 assert.strictEqual(text('awake-status'), '', 'every rule was read');
 assert.strictEqual(wake[0].children[0].children[0].textContent, 'Downloader');
 assert.match(wake[0].children[0].children[1].textContent, /^Keeping this device awake since .+ · a six-hour download$/);
 assert.strictEqual(wake[0].switch.checked, true, 'a holder with no rule reads allowed by its lease');
 assert.strictEqual(wake[0].switch.attrs['aria-label'], 'Allow Downloader to keep this device awake');
 assert.strictEqual(wake[1].children[0].children[0].textContent, 'Lemonade', 'a registered title names the app');
 assert.match(wake[1].children[0].children[1].textContent, /^Keeping this device and its display awake since .+ · Chat$/, 'beside a card lease the display stays on too');
 assert.strictEqual(wake[1].switch.checked, true);
 assert.strictEqual(wake[2].children[0].children[1].textContent, 'Not allowed to keep this device awake.');
 assert.strictEqual(wake[2].switch.checked, false, 'a stopped app reads Off');
 assert(!wake.some(r => /awake-\d|card-\d/.test(r.children[0].children[0].textContent + r.children[0].children[1].textContent)), 'no lease id is page text');
 // Each row links to the app's rules on Permissions, where any rule can be
 // removed; the link writes one sentence.
 const permissions = wake[0].children[1].children[1];
 assert.strictEqual(permissions, wake[0].permissions);
 assert.strictEqual(permissions.textContent, 'Permissions…');
 context.location.href = '';
 permissions.onclick();
 assert.strictEqual(wake[0].status.textContent, 'Opening permissions for Downloader.');
 assert.match(context.location.href, /^\/rights\?k=test&program=C%3A%5Cdownloader\.exe&account=S-1-5-21-7-1001#rights-section$/);
 // The rendered page keeps the Panel rules (static/panel_rules.js).
 {
  const { assertPanelRules } = require('./static/panel_rules.js');
  const section = (...children) => Object.assign(new FakeNode('section'), { children });
  for (const id of ['device-answer', 'table-status', 'awake-status']) element(id).role = 'status';
  assertPanelRules([
   section(element('device-answer'), element('gpu-faults'), element('table-status'), element('resources')),
   section(element('awake-status'), element('awake-empty'), element('awake')),
  ], 'Device', { naming: true, allowClasses: require('./panel_fake_dom_test_helper.js').allowlistedClasses('card') });
 }

 // A live tick where the server state changes (a new reason, a new holder,
 // an app gone) keeps the same row element; holder rows are kept the same
 // way. Rules the Panel may not read are said once in the status line.
 const kept = wake[0], keptSwitch = wake[0].switch, holderBefore = element('resources').children[0].children[1].children[0];
 await tick();
 assert.strictEqual(element('awake').children[0], kept, 'the same row element survives the reread');
 assert.strictEqual(element('awake').children[0].switch, keptSwitch, 'and its switch');
 assert.match(kept.children[0].children[1].textContent, / · a seven-hour download$/, 'the row text follows the server');
 assert.strictEqual(element('awake').children.length, 2, 'a new holder is added and gone apps leave');
 assert.strictEqual(element('awake').children[1].children[0].children[0].textContent, 'Backup');
 assert.strictEqual(text('awake-status'), 'This Panel may not read permissions, so only apps keeping this device awake now are listed.');
 assert.strictEqual(element('gpu-faults').children[0], faultRow, 'the reset row is kept');
 assert.strictEqual(text('gpu-faults-status'), '', 'the scan reached the end');
 assert.strictEqual(kept.status.textContent, 'Opening permissions for Downloader.', 'a live tick keeps the row sentence');
 assert.strictEqual(element('resources').children[0].children[1].children[0], holderBefore, 'holder rows are kept in place');

 // An unavailable table reads a plain sentence; an unreadable log removes
 // no reading.
 await tick();
 assert.strictEqual(text('awake-status'), "That service isn't running on this account right now. Check it on the Apps page.");
 assert.strictEqual(element('gpu-faults').children.length, 0, 'an unavailable log shows no reset');
 assert.strictEqual(text('gpu-faults-status'), 'Graphics driver resets could not be read.');

 // Hidden: the next tick stops the timer; visible again restarts it.
 context.document.visibilityState = 'hidden';
 await tick();
 assert.strictEqual(liveIntervals.size, 0, 'no live reads while hidden');
 assert.strictEqual(context.document.documentElement.dataset.ready, '1', 'Device marks itself ready after its first reads');
 assert(go.includes('data-ready-owner="page"'), 'Device owns its readiness');

 // Both actual table reads preserve missing measurement evidence.
 const unavailable = { resources: [{resource:'card:2',held:0,capacity:0,holders:[]}] };
 replies['/card/table'].push(unavailable, unavailable);
 for (const fresh of [true, false]) {
  await context.table(fresh);
  assert.strictEqual(text('table-status'), 'Not measured.');
  const empty = element('resources').children[0].children[1].children[0];
  assert.strictEqual(empty.textContent, 'No app is listed as holding this resource.');
  assert.strictEqual(element('resources').children[0].children[0].caption.textContent, 'Not measured.');
 }
 replies['/card/table'].push(cardResources);
 await context.table(true);
 assert.match(text('table-status'), /^Measured by Windows .+ ago\.$/);

 // A Mac's instrument measures the card's capacity and no holder: the
 // capacity reads as measured and the holders as reported by their apps.
 const mac = { resources: [{ resource: 'card:0', capacity: 40200896512, held: 0, instrument: 'metal-working-set',
  observed: new Date(Date.now() - 2000).toISOString(), holders: [] }] };
 replies['/card/table'].push(mac);
 await context.table(true);
 assert.match(element('resources').children[0].children[0].caption.textContent,
  /^37\.4 GB total\. Apps report what they hold\.$/);
 assert.match(element('resources').children[0].children[2].children[1].children[0].textContent, /^Capacity from Metal's working-set limit .+; apps report what they hold\./);
 assert.strictEqual(text('table-status'), 'Apps report what they hold.');
 assert.strictEqual(text('device-answer'), 'No app is listed as using the graphics card.');

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
 // The switch writes the app's rule through /card/wake: stop a deny, allow
 // a permit. Each change writes one sentence in the row and rereads the list.
 {
  const { assertLiveUpdateKeepsRow } = require('./panel_fake_dom_test_helper.js');
  const app = { program: 'C:\\downloader.exe', account: 'S-1-5-21-7-1001', holding: true, display: false, since: '2026-09-22T09:05:00Z', why: 'a six-hour download', rule: 'none' };
  const server = { apps: [app], edits: [] };
  const intervals = [];
  const wakeCtx = { ...context, setInterval: fn => { intervals.push(fn); return intervals.length; }, clearInterval() {},
   document: { ...context.document, visibilityState: 'visible', documentElement: { dataset: {} } }, location: { search: '?k=test', href: '' },
   fetch: async (path, opts) => {
    const reply = v => ({ ok: true, json: async () => v });
    assert.strictEqual(opts.headers['X-Panel-Key'], 'test', 'the switch uses the existing Panel key');
    if (path.startsWith('/card/table')) return reply(cardResources);
    if (path.startsWith('/card/faults')) return reply({ outcome: 'page', faults: [] });
    if (path === '/card/wake' && opts.method === 'GET') return reply({ outcome: 'page', rules: 'read', apps: server.apps.map(a => ({ ...a })) });
    if (path === '/card/wake' && opts.method === 'POST') {
     const body = JSON.parse(opts.body); server.edits.push(body);
     if (server.editError) throw Error(server.editError);
     if (server.editReply) return reply(server.editReply);
     if (!body.allow && server.dropOnStop) { server.apps = server.apps.filter(a => a.program !== body.program); return reply({ outcome: 'applied' }); }
     server.apps = server.apps.map(a => a.program === body.program ? { ...a, rule: body.allow ? 'allowed' : 'stopped', holding: body.allow && a.holding } : a);
     return reply({ outcome: 'applied' });
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
  assert.strictEqual(sw.checked, true);
  // Stop: a deny for exactly the row's account and program, one sentence,
  // the row stays and reads Off and not holding; a live tick keeps it.
  const { sentence } = await assertLiveUpdateKeepsRow({
   click: async () => { sw.checked = false; await sw.onchange(); await settle(); },
   change: () => { server.apps[0].why = 'changed'; },
   data: () => server.apps,
   tick: async () => { await intervals[0](); await settle(); },
   row, status: r => r.status, label: 'Device, keep awake',
  });
  assert.strictEqual(sentence, 'Downloader may no longer keep this device awake.');
  assert.deepStrictEqual(server.edits[0], { account: 'S-1-5-21-7-1001', program: 'C:\\downloader.exe', allow: false });
  assert.strictEqual(sw.checked, false);
  assert.strictEqual(row().children[0].children[1].textContent, 'Not allowed to keep this device awake.');
  // Allow writes the permit.
  sw.checked = true; await sw.onchange(); await settle();
  assert.deepStrictEqual(server.edits[1], { account: 'S-1-5-21-7-1001', program: 'C:\\downloader.exe', allow: true });
  assert.strictEqual(row().status.textContent, 'Downloader may keep this device awake.');
  assert.strictEqual(sw.checked, true);
  assert.strictEqual(sw.disabled, false);
  // A refusal changes nothing: the switch goes back and the row says why.
  for (const [outcome, expected] of [['forbidden', /may not change permissions; nothing changed/], ['conflict', /kept changing; nothing changed/],
   ['invalid', /refused this change; nothing changed/], ['unavailable', /could not be reached; nothing changed/], ['expired', /^Nothing changed\. Try again\.$/]]) {
   server.editReply = { outcome };
   const before = server.edits.length;
   sw.checked = false; await sw.onchange(); await settle();
   assert.strictEqual(server.edits.length, before + 1);
   assert.strictEqual(sw.checked, true, outcome + ' leaves the switch as it was');
   assert.strictEqual(sw.disabled, false);
   assert.match(row().status.textContent, expected);
  }
  server.editReply = null;
  // A failed call says so in a plain line and changes nothing.
  server.editError = 'rights: dial tcp: no runtime';
  sw.checked = false; await sw.onchange(); await settle();
  assert.strictEqual(sw.checked, true);
  assert(row().status.textContent && !/dial tcp/.test(row().status.textContent), row().status.textContent);
  server.editError = null;
  // A stop whose reread drops the row keeps its sentence in the section's
  // status line.
  server.dropOnStop = true;
  sw.checked = false; await sw.onchange(); await settle();
  assert.strictEqual(element('awake').children.length, 0);
  assert.strictEqual(element('awake-status').textContent, 'Downloader may no longer keep this device awake.');
  server.dropOnStop = false;
  assert(!/back to the default/.test(script), 'Allow never claims the default; Permissions… is the way back');
  // A row naming no account sends nothing and points at Permissions….
  server.apps = [{ ...app, account: '' }]; await wakeCtx.wake();
  const before = server.edits.length, bare = row();
  bare.switch.checked = false; await bare.switch.onchange();
  assert.strictEqual(server.edits.length, before, 'no edit without an account');
  assert.strictEqual(bare.switch.checked, true);
  assert.strictEqual(bare.status.textContent, 'Downloader names no account here; use Permissions… instead.');
  // OpenAbstractions' own programs share one row (architect ruling
  // 2026-11-09, second block): in the place of the first, on while any may,
  // and its switch writes each program's rule.
  const ownApp = (program, rule) => ({ program, account: 'S-1-5-21-7-1001', title: 'OpenAbstractions', own: true, holding: false, display: false, rule });
  server.apps = [ownApp('C:\\Program Files\\OpenAbstractions\\tools\\openabstractions.exe', 'allowed'), { ...app },
   ownApp('C:\\Users\\p\\AppData\\Local\\Programs\\OpenAbstractions\\tools\\openabstractionsw.exe', 'none'),
   { ...ownApp('C:\\Program Files\\OpenAbstractions\\tools\\Abstraction Panel.exe', 'allowed'), title: 'OpenAbstractions Panel' }];
  await wakeCtx.wake();
  assert.deepStrictEqual(element('awake').children.map(r => r.children[0].children[0].textContent), ['OpenAbstractions', 'Downloader']);
  const ownRow = row(), editsBefore = server.edits.length;
  assert.strictEqual(ownRow.switch.checked, true, 'on while one of them may');
  assert.strictEqual(ownRow.children[0].children[1].textContent, 'Allowed to keep this device awake.');
  ownRow.switch.checked = false; await ownRow.switch.onchange(); await settle();
  assert.deepStrictEqual(server.edits.slice(editsBefore).map(e => [e.program, e.allow]), [
   ['C:\\Program Files\\OpenAbstractions\\tools\\openabstractions.exe', false],
   ['C:\\Users\\p\\AppData\\Local\\Programs\\OpenAbstractions\\tools\\openabstractionsw.exe', false],
   ['C:\\Program Files\\OpenAbstractions\\tools\\Abstraction Panel.exe', false]], 'the switch writes each own program');
  assert.strictEqual(row().status.textContent, 'OpenAbstractions may no longer keep this device awake.');
  assert.strictEqual(row().children[0].children[1].textContent, 'Not allowed to keep this device awake.');
 }
 // The stored-content affordance lives in one row and only its inline
 // confirmation sends the exact observed ticket. Ineligible rows have no Remove button.
 const target=['sha256:'+'a'.repeat(64),'b'.repeat(64),'c'.repeat(32),'2100000000','d'.repeat(64)].join('.');
 const removalRows=element('storage-committed-rows');
 context.storageRemovalRows([{name:'Owned content',bytes:1000,target,ready:true},{name:'Unverified content',bytes:1000,ready:false},
  {name:'Protected content',bytes:1000,needs_administrator:true}]);
 assert.strictEqual(removalRows.children[1].children.length,1);
 assert.deepStrictEqual(removalRows.children[2].children.map(child=>child.tagName),['SPAN','SPAN']);
 assert.strictEqual(removalRows.children[2].children[1].textContent,'Needs administrator');
 const firstRemove=removalRows.children[0];
 const beforeRemoval=calls.length;
 firstRemove.children[1].dispatch('click');
 assert.strictEqual(calls.length,beforeRemoval,'opening confirmation never calls Remove');
 assert.match(firstRemove.children[0].textContent,/^Remove .*It can't be undone/);
 firstRemove.children[2].dispatch('click');
 assert.strictEqual(calls.length,beforeRemoval,'cancelling never calls Remove');
 const refreshed=removalRows.children[0];
 refreshed.children[1].dispatch('click');
 replies['/card/storage/remove']=[{outcome:'held',holders:['Ollama']}];
 replies['/card/storage']=[{classes:[],large:[],matches:[],committed:[]}];
 refreshed.children[1].dispatch('click');
 await new Promise(r=>setTimeout(r,0));
 assert.deepStrictEqual(calls.slice(beforeRemoval),['/card/storage/remove','/card/storage']);
 assert.strictEqual(text('storage-removal-status'),'Used by Ollama');
 context.storageRemovalPage([{name:'First page',bytes:1}], 'cursor-two');
 replies['/card/storage']=[{committed:[{name:'Later page',bytes:1}]}];
 await element('storage-committed-next').onclick();
 assert.strictEqual(calls.at(-1),'/card/storage?committed=cursor-two');
 assert.match(removalRows.children[0].children[0].textContent,/^Later page/);
 assert(element('storage-committed-next').hidden);
 replies['/card/storage']=[{committed:[{name:'First page',bytes:1}],committed_next:'cursor-two'}];
 await element('storage-committed-prev').onclick();
 assert.strictEqual(calls.at(-1),'/card/storage');
 assert.match(removalRows.children[0].children[0].textContent,/^First page/);
 console.log('PASS card page');
})().catch(e => { console.error(e); process.exit(1); });
