// Run with node monitor/card_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/card_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
// plainScript (monitor/panel_shell.go) is embedded ahead of every page
// fragment in the real document, so this isolated test loads it into the
// same vm context first; card_page.go's own script now calls oaShowError.
const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const plainMarker = 'const plainScript = `';
const plainStart = shellGo.indexOf(plainMarker) + plainMarker.length;
assert(plainStart > plainMarker.length - 1, 'plainScript constant not found in panel_shell.go');
const plainEnd = shellGo.indexOf('`', plainStart);
const plainScript = shellGo.slice(plainStart, plainEnd).split('<script>')[1].split('</script>')[0];

class FakeNode {
 constructor() { this.value = ''; this.textContent = ''; this.className = ''; this.title = ''; this.checked = false; this.disabled = false; this.open = false; this.innerHTML = ''; this.children = []; this.listeners = {}; }
 replaceChildren(...v) { this.children = [...v]; }
 append(...v) { this.children.push(...v); }
 click() { this.onclick && this.onclick(); }
 addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
 dispatch(type) { for (const fn of this.listeners[type] || []) fn(); }
}
const fields = new Map();
function node() { return new FakeNode(); }
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
element('auto').checked = true; // matches the page's <input ... checked>

const calls = [];
const liveIntervals = new Map();
let nextIntervalId = 0;

const cardResources = { resources: [{
 resource: 'card:0', capacity: 25769803776, held: 22795929600, instrument: 'windows-gpu-counters',
 observed: new Date(Date.now() - 2000).toISOString(),
 holders: [
  { program: 'C:\\lms\\llama-server.exe', account: 'S-1-5-21-7-1001', amount: 21247127552, evidence: 'verified', grant: '', since: '2026-09-22T09:00:00.000000Z', detail: '' },
  { program: 'host:comfyui', account: '', amount: 0, evidence: 'claimed', grant: '', since: '', detail: 'sdxl-checkpoint' },
 ] }] };
const workPage = { Outcome: 'page', Complete: true, Snapshots: [{ Label: 'download model.gguf', State: 'accepted', Receipt: { OperationID: 'op-1' } }] };
const awakeUnavailable = { outcome: 'unavailable', holds: [], reason: 'table: dial tcp: no runtime; rights fallback: open admin.secret: no such file or directory' };
const awakeTable = { outcome: 'page', source: 'table', holds: [
 { program: 'C:\\downloader.exe', account: 'S-1-5-21-7-1001', amount: 0, since: '2026-09-22T09:05:00.000000Z', lease: 'awake-1', why: 'a six-hour download' },
] };
const awakeFallback = { outcome: 'page', source: 'rights fallback', holds: [
 { program: 'C:\\comfyui\\main.py', since: '2026-09-22T09:05:00Z', why: 'rendering' },
] };

// The Panel's own account, as /explore reports it; the first holder and the
// table-sourced awake hold below deliberately share it, so the page's "you"
// substitution (task 2026-09-23 finding 2) is exercised against a holder row
// and an awake row, while every other row keeps its own, different account.
const selfAccount = 'S-1-5-21-7-1001';
const exploreSelf = { self: { account: selfAccount, program: 'C:\\Program Files\\OpenAbstractions\\Abstraction Panel.exe' }, probes: [] };

const replies = {
 '/explore': [exploreSelf],
 '/card/table': [cardResources, cardResources, cardResources],
 '/account-work': [workPage],
 '/card/awake': [awakeUnavailable, awakeTable, awakeFallback, { outcome: 'page', source: 'table', holds: [] }],
};

const context = {
 URLSearchParams, console, Date, Math, Number, JSON, String,
 Node: FakeNode,
 document: {
  getElementById: element, createElement: node,
  visibilityState: 'visible',
  addEventListener: (name, fn) => { if (name === 'visibilitychange') context.document.onVisibilityChange = fn; },
 },
 location: { search: '?k=test' },
 setInterval: (fn) => { const id = ++nextIntervalId; liveIntervals.set(id, fn); return id; },
 clearInterval: (id) => { liveIntervals.delete(id); },
 fetch: async (path, opts) => {
  assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
  calls.push(path);
  const base = path.split('?')[0];
  const queue = replies[base];
  if (!queue || !queue.length) throw new Error('no reply queued for ' + path);
  return { ok: true, json: async () => queue.shift() };
 },
};
context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
vm.runInNewContext(plainScript, context);
vm.runInNewContext(script, context);

// sampleAgePhrase used to read "1 seconds ago" for the one value where a
// plural reads wrong (task 2026-09-23, twelfth first-time visitor, minor).
// Called directly with synthetic timestamps so this holds regardless of
// how long the surrounding test run itself takes.
assert.strictEqual(context.sampleAgePhrase(new Date(Date.now() - 1000).toISOString()), '1 second ago', 'exactly one second reads singular');
assert.strictEqual(context.sampleAgePhrase(new Date(Date.now() - 3000).toISOString()), '3 seconds ago', 'several seconds still reads plural');
assert.strictEqual(context.sampleAgePhrase(new Date(Date.now() - 60000).toISOString()), '1 minute ago', 'exactly one minute reads singular too');
assert.strictEqual(context.sampleAgePhrase(new Date(Date.now() - 120000).toISOString()), '2 minutes ago');
const text = id => element(id).textContent;
const cells = row => row.children.map(td => (td.children && td.children.length ? td.children[0].textContent : td.textContent));

(async () => {
 const settle = () => new Promise(r => setTimeout(r, 0));
 await settle(); await settle(); await settle();

 // The page reads its own account first (loadSelf), then reads fresh on
 // load and starts auto-refresh (checked by default).
 assert.deepStrictEqual(calls, ['/explore', '/card/table?fresh=1']);
 assert.strictEqual(liveIntervals.size, 1, 'auto-refresh should have armed one interval on load');
 assert.strictEqual(element('resources').children.length, 1);
 const block = element('resources').children[0];
 // task 2026-09-23, eighth first-time visitor, finding 5: "card:0" was the
 // heading's only name; it now reads "Graphics card" (the first one, so no
 // number), with the raw id kept as the heading's title and in the card's
 // own Technical details.
 assert.strictEqual(block.children[0].textContent, 'Graphics card');
 assert.strictEqual(block.children[0].title, 'card:0');
 // The held line reads as a sentence naming the instrument in words (task
 // 2026-09-23, fourth first-time visitor); the age that follows is not
 // pinned down to one exact wording here.
 assert.match(block.children[1].textContent, /^21\.23 GiB held of 24\.00 GiB\. Measured by Windows GPU counters/);
 const details = block.children[2];
 assert.strictEqual(details.children[0].textContent, 'Technical details');
 assert.match(details.children[1].textContent, /"resource": "card:0"/);
 // task 2026-09-23, eighth first-time visitor, finding 6: the table itself
 // now sits inside a div.oa-table wrapper, the element that scrolls
 // sideways if the table cannot fit, never the page.
 // task 2026-09-23, tenth first-time visitor, finding 5: the wrapper (and
 // the table inside it) now sits inside the same details as the raw JSON,
 // so toggling Technical details actually shows or hides the table too,
 // instead of the table always being visible beside an inert disclosure.
 assert.strictEqual(block.children.length, 3, 'nothing sits outside the details besides the heading and the held sentence');
 const wrap = details.children[2];
 assert.strictEqual(wrap.className, 'oa-table');
 const table = wrap.children[0];
 // Grant is empty for every holder of this resource, so its column is
 // dropped rather than printed as a dash in every row (task 2026-09-23
 // finding 2); Since and Detail each carry a value on at least one row and
 // stay. Evidence carries no column of its own: every row is verified or
 // claimed, so the column was never actually empty of data, only of visible
 // text (a CSS-only badge, no cell text a reader could see); the distinction
 // now colors the Amount cell instead (task 2026-09-23, fourth first-time
 // visitor: "the Evidence column is empty for every row").
 const theadHTML = table.children[0].innerHTML;
 assert(!theadHTML.includes('>Grant<'), 'the all-empty Grant column is not rendered: ' + theadHTML);
 assert(!theadHTML.includes('>Evidence<'), 'Evidence is no longer its own column: ' + theadHTML);
 for (const label of ['Program', 'Account', 'Amount', 'Since', 'Detail']) assert(theadHTML.includes('>' + label + '<'), label + ' column missing: ' + theadHTML);
 const rows = table.children[1].children; // table > tbody > rows
 assert.strictEqual(rows.length, 2);
 // Program cell shortens to the file name and keeps the full path as a hover title.
 const programCell = rows[0].children[0].children[0];
 assert.strictEqual(programCell.textContent, 'llama-server.exe');
 assert.strictEqual(programCell.title, 'C:\\lms\\llama-server.exe');
 assert.strictEqual(rows[0].children[2].children[0].className, 'verified');
 // The first holder's account is this Panel's own account; it reads as "you".
 assert.deepStrictEqual(cells(rows[0]).slice(1), ['you', '19.79 GiB', '2026-09-22T09:00:00.000000Z', '-']);
 assert.strictEqual(rows[1].children[2].children[0].className, 'claimed', 'the claimed row is styled distinctly from the verified row');
 // The second holder names no account at all; it stays a dash, not "you".
 assert.deepStrictEqual(cells(rows[1]).slice(1), ['-', '-', '-', 'sdxl-checkpoint'], 'a claimed row carries no amount');

 // Refresh always asks for a fresh read.
 element('refresh').click(); await settle(); await settle();
 assert.deepStrictEqual(calls.slice(-1), ['/card/table?fresh=1']);

 // task 2026-09-23, thirteenth first-time visitor, finding 9: Technical
 // details closed itself again on every auto-refresh tick, since table()
 // rebuilds a fresh <details> element from scratch each time with no
 // memory of its own that it had been opened.
 details.open = true;
 details.dispatch('toggle');

 // A tick of the auto-refresh timer reads the standing sample, not a fresh one.
 for (const fn of liveIntervals.values()) fn();
 await settle(); await settle();
 assert.deepStrictEqual(calls.slice(-1), ['/card/table']);
 const detailsAfterRefresh = element('resources').children[0].children[2];
 assert.strictEqual(detailsAfterRefresh.open, true, 'an opened Technical details stays open across an auto-refresh tick');
 // task 2026-09-23, ninth first-time visitor, finding 4: the caption named
 // the age bound the sample is read within, never how old the sample
 // actually is. It now reads the sample's own age; the fixture's
 // observed timestamp is a couple of seconds old when the file loads, and
 // stays under a minute for the whole test run.
 assert.match(text('table-status'), /^Read from the last sample, taken (\d+ seconds|less than a minute) ago\.$/, text('table-status'));

 // Turning auto-refresh off clears the timer; a later tick fetches nothing new.
 element('auto').checked = false;
 element('auto').onchange();
 assert.strictEqual(liveIntervals.size, 0, 'auto-refresh should be cleared once unchecked');
 const before = calls.length;
 for (const fn of liveIntervals.values()) fn();
 await settle();
 assert.strictEqual(calls.length, before, 'no interval remained live to tick');

 // What is being fetched.
 element('work-list').click(); await settle();
 assert.match(text('work-status'), /More jobs remain|End of jobs/);
 assert.strictEqual(element('work').children.length, 1);
 assert.match(element('work').children[0].textContent, /download model\.gguf - accepted/);

 // Who holds the machine awake: unavailable first (both sources failed).
 // The status line is a plain sentence, never the raw "open
 // C:\...\admin.secret: ..." chain; that chain is still readable, tucked
 // behind a "Technical details" disclosure. Then a table-sourced hold, then
 // a rights-fallback hold; the status line always says which source
 // answered.
 element('awake-list').click(); await settle();
 assert.strictEqual(text('awake-status'), "That service isn't running on this account right now. Start the runtime, or check the service on the Status page.");
 const awakeDetails = element('awake-status').children[0];
 assert.strictEqual(awakeDetails.className, 'oa-error-details');
 assert.match(awakeDetails.children[1].textContent, /rights fallback: open admin\.secret/, 'the raw reason stays available behind the disclosure');
 assert.strictEqual(element('awake').children.length, 0);

 element('awake-list').click(); await settle();
 assert.strictEqual(text('awake-status'), 'Source: table.');
 let awakeRows = element('awake').children[0].children[1].children; // table > tbody > rows
 assert.strictEqual(awakeRows.length, 1);
 const awakeProgramCell = awakeRows[0].children[0].children[0];
 assert.strictEqual(awakeProgramCell.textContent, 'downloader.exe');
 assert.strictEqual(awakeProgramCell.title, 'C:\\downloader.exe');
 // This hold's account is also this Panel's own account: "you" here too.
 assert.deepStrictEqual(cells(awakeRows[0]).slice(1), ['you', '-', '2026-09-22T09:05:00.000000Z', 'awake-1', 'a six-hour download']);

 element('awake-list').click(); await settle();
 assert.strictEqual(text('awake-status'), 'Source: rights fallback.');
 awakeRows = element('awake').children[0].children[1].children;
 assert.strictEqual(awakeRows.length, 1);
 assert.strictEqual(awakeRows[0].children[0].children[0].textContent, 'main.py');
 assert.deepStrictEqual(cells(awakeRows[0]).slice(1), ['-', '-', '2026-09-22T09:05:00Z', '-', 'rendering']);

 // task 2026-09-23, eleventh first-time visitor, minor finding: the empty
 // case used to read "Source: table.", naming the instrument that
 // answered rather than the fact nothing is holding the device awake.
 element('awake-list').click(); await settle();
 assert.strictEqual(text('awake-status'), 'No program holds this right now.');
 assert.strictEqual(element('awake').children.length, 0);

 console.log('PASS card page');
})().catch(e => { console.error(e); process.exit(1); });
