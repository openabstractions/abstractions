// Run with node monitor/runtime_down_page_test.js; no browser or service is
// started. Covers runtimeScript and the runtime-down path of oaShowError and
// oaSetStatus (monitor/panel_shell.go; owner, 2026-11-09: "Show one
// runtime-down sentence per Panel page"): with the runtime stopped the page
// says so once, in its banner, and the sections that failed with it stay
// empty; once the runtime answers again the banner goes and the page's own
// reread runs without a reload.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const { fakeDocument } = require('./panel_fake_dom_test_helper.js');
const { panelRuleViolations } = require('./static/panel_rules.js');

const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
function scriptConst(name) {
 const marker = 'const ' + name + ' = `', start = shellGo.indexOf(marker);
 assert(start >= 0, name + ' not found in panel_shell.go');
 const raw = shellGo.slice(start + marker.length, shellGo.indexOf('`', start + marker.length));
 return raw.split('<script>')[1].split('</script>')[0];
}
// The banner's markup is a Go string concatenation; its sentences are read
// from it so the fake banner says what the served one says.
const bannerGo = shellGo.slice(shellGo.indexOf('const runtimeBanner = '), shellGo.indexOf('func withRuntimeBanner'));
const labelText = /id="oa-runtime-label">([^<]+)</.exec(bannerGo)[1], captionText = /id="oa-runtime-caption">([^<]+)</.exec(bannerGo)[1];
assert.strictEqual(labelText, 'OpenAbstractions is not running');
assert.strictEqual(captionText, 'Open OpenAbstractions from the Start menu, then check again');

function settle() { return new Promise((resolve) => setImmediate(resolve)); }

function page({ handlers = true } = {}) {
 const doc = fakeDocument();
 doc.readyState = 'complete';
 const banner = doc.element('oa-runtime');banner.hidden = true;
 const row = doc.createElement('div');row.className = 'oa-row';banner.appendChild(row);
 const label = doc.createElement('span');label.id = 'oa-runtime-label';label.className = 'oa-row-label';label.textContent = labelText;row.appendChild(label);
 const caption = doc.createElement('span');caption.id = 'oa-runtime-caption';caption.className = 'oa-row-caption';caption.textContent = captionText;row.appendChild(caption);
 const retry = doc.createElement('button');retry.id = 'oa-runtime-retry';row.appendChild(retry);
 const status = doc.createElement('p');status.id = 'oa-runtime-status';status.className = 'oa-status';status.setAttribute('data-state', 'idle');row.appendChild(status);
 // Questions waiting's three status lines, and one section that writes its
 // own sentence.
 for (const id of ['questions-status', 'permissions-status', 'choices-status', 'own-status']) {
  const p = doc.element(id, 'p');p.className = 'oa-status';p.setAttribute('data-state', 'loading');p.textContent = 'Loading…';
 }
 const server = { runtime: { error: 'open pipe: The system cannot find the file specified.' }, reads: 0 };
 const timers = [];
 const context = {
  document: doc, URLSearchParams, Promise, console,
  location: { search: '?k=key', reloaded: 0, reload() { this.reloaded++; } },
  setTimeout: (fn) => { timers.push(fn); return timers.length; }, clearTimeout: () => {},
  fetch: async (path, init) => {
   assert.strictEqual(path, '/runtime');
   assert.strictEqual(init.headers['X-Panel-Key'], 'key', 'the check carries the Panel key');
   server.reads++;
   return { ok: true, json: async () => server.runtime };
  },
 };
 context.window = context;
 vm.runInNewContext(scriptConst('plainScript') + '\n' + scriptConst('runtimeScript'), context);
 const rereads = [];
 if (handlers) vm.runInNewContext("oaOnRuntimeBack(()=>{rereadLog.push('reread')})", Object.assign(context, { rereadLog: rereads }));
 return { doc, context, server, timers, rereads, banner, retry, status };
}

const sentence = 'OpenAbstractions is not running';
function visibleSentences(doc, down) {
 return Array.from(panelRuleViolations(doc.body, { rules: ['runtime-once'], runtimeDown: down }), (v) => v.detail);
}

(async () => {
 // --- Stopped: one banner, empty sections. ---
 {
  const p = page();
  await p.context.oaRuntimeState.first;
  assert.strictEqual(p.banner.hidden, false, 'the arrival check shows the banner');
  assert.strictEqual(p.doc.documentElement.getAttribute('data-runtime'), 'down');
  const raw = 'service resolution: runtime_unavailable: abstraction.asks/operator@1 (capability abstraction.asks) at the explicit endpoint \\\\.\\pipe\\x: open';
  p.context.oaShowError('questions-status', raw);
  p.context.oaShowError('permissions-status', Error(raw));
  p.context.oaShowError('choices-status', { message: raw });
  p.context.oaSetStatus('own-status', 'error', p.context.oaPlain(raw).line);
  for (const id of ['questions-status', 'permissions-status', 'choices-status', 'own-status']) {
   const el = p.doc.getElementById(id);
   assert.strictEqual(el.textContent, '', id + ' stays empty instead of repeating the sentence');
   assert.strictEqual(el.getAttribute('data-state'), 'idle', id + ' is no error line');
  }
  assert.deepStrictEqual(visibleSentences(p.doc, true), [], 'the sentence appears exactly once on the page');
  assert.ok(p.doc.body.textContent.split(sentence).length === 2, 'the page text holds the sentence once');
  // Every other failure still reads as itself while the runtime is down.
  p.context.oaShowError('own-status', 'forbidden: no rule');
  assert.match(p.doc.getElementById('own-status').textContent, /^OpenAbstractions did not permit this Panel/);
  p.context.oaShowError('questions-status', 'download failed');
  assert.match(p.doc.getElementById('questions-status').textContent, /^This didn't work\./, 'a download failure keeps its sentence');
  assert.strictEqual(p.doc.getElementById('questions-status').getAttribute('data-state'), 'error', 'and stays an error line');
  p.context.oaSetStatus('choices-status', 'error', 'The download failed; nothing was changed.');
  assert.strictEqual(p.doc.getElementById('choices-status').textContent, 'The download failed; nothing was changed.');
  assert.strictEqual(p.doc.getElementById('choices-status').getAttribute('data-state'), 'error');
  // Check again with the runtime still stopped says when it checked.
  await p.retry.onclick();
  assert.match(p.status.textContent, /^Checked .*; still not running\.$/);
  assert.strictEqual(p.banner.hidden, false);
  // The page follows the runtime while it is down.
  assert.ok(p.timers.length >= 1, 'a check is scheduled while the runtime is down');
  console.log('PASS: a stopped runtime is one banner sentence; the sections that failed with it stay empty');

  // --- The runtime comes back: the banner goes and the page rereads. ---
  p.server.runtime = { capabilities: [] };
  const reads = p.server.reads;
  p.timers.shift()();
  await settle();await settle();
  assert.strictEqual(p.server.reads, reads + 1, 'the follow-up check read /runtime');
  assert.strictEqual(p.banner.hidden, true, 'the banner goes once the runtime answers');
  assert.strictEqual(p.doc.documentElement.getAttribute('data-runtime'), null);
  assert.deepStrictEqual(p.rereads, ['reread'], "the page's own reread runs");
  assert.strictEqual(p.context.location.reloaded, 0, 'no reload');
  assert.deepStrictEqual(visibleSentences(p.doc, false), []);
  console.log('PASS: the page recovers through its own reread when the runtime answers again');
 }

 // --- Running: no banner, and a stale runtime_unavailable only asks for a check. ---
 {
  const p = page();
  p.server.runtime = { capabilities: [] };
  await p.context.oaRuntimeState.first;
  assert.strictEqual(p.banner.hidden, true);
  const reads = p.server.reads;
  p.context.oaShowError('questions-status', 'service resolution: runtime_unavailable');
  await settle();await settle();
  assert.strictEqual(p.server.reads, reads + 1, 'a section reporting runtime_unavailable asks for a check');
  assert.strictEqual(p.banner.hidden, true, 'the check decides: a running runtime shows no banner');
  assert.deepStrictEqual(p.rereads, ['reread'], 'the emptied section is read again once');
  assert.strictEqual(p.context.location.reloaded, 0, 'never a reload');
  // The runtime stops: the next section failure shows the banner.
  p.server.runtime = { error: 'gone' };
  p.context.oaShowError('choices-status', 'service resolution: runtime_unavailable');
  await settle();await settle();
  assert.strictEqual(p.banner.hidden, false);
  console.log('PASS: a running runtime shows no banner; a section failure brings it when the runtime stopped');
 }

 // --- A page with no reread of its own reloads once the runtime is back. ---
 {
  const p = page({ handlers: false });
  await p.context.oaRuntimeState.first;
  p.context.oaShowError('questions-status', 'service resolution: runtime_unavailable');
  p.server.runtime = {};
  await p.retry.onclick();
  assert.strictEqual(p.banner.hidden, true);
  assert.strictEqual(p.context.location.reloaded, 1, 'an emptied section with no reread reloads the page');
  console.log('PASS: a page without a reread reloads once to read again');
 }

 // --- A running runtime found after a section emptied never reloads a page without a reread. ---
 {
  const p = page({ handlers: false });
  p.server.runtime = { capabilities: [] };
  await p.context.oaRuntimeState.first;
  p.context.oaShowError('questions-status', 'service resolution: runtime_unavailable');
  await settle();await settle();
  assert.strictEqual(p.banner.hidden, true);
  assert.strictEqual(p.context.location.reloaded, 0);
  console.log('PASS: a running runtime found by a check never reloads');
 }

 // --- oaReady waits for the arrival check, so a screenshot shows the banner. ---
 {
  const p = page();
  let answer;p.context.fetch = () => new Promise((resolve) => { answer = resolve; });
  p.context.oaRuntimeState.checking = null;
  p.context.oaRuntimeState.first = p.context.oaRuntimeCheck();
  p.context.oaReady();
  await settle();
  assert.strictEqual(p.doc.documentElement.dataset.ready, undefined, 'not ready before the check answers');
  answer({ ok: true, json: async () => ({ error: 'gone' }) });
  await settle();await settle();await settle();
  assert.strictEqual(p.doc.documentElement.dataset.ready, '1');
  assert.strictEqual(p.banner.hidden, false);
  console.log('PASS: oaReady waits for the arrival check');
 }

 // --- oaRuntimeEcho: a section's own sentence for a failed read is not
 // written while the runtime is down; one written before the banner shows
 // is emptied when it shows; a later verdict in the same line stays. ---
 {
  const p = page();
  let answer;p.context.fetch = () => new Promise((resolve) => { answer = resolve; });
  p.context.oaRuntimeState.checking = null;
  p.context.oaRuntimeState.first = p.context.oaRuntimeCheck();
  const own = p.doc.getElementById('own-status'), other = p.doc.getElementById('choices-status');
  p.context.oaRuntimeEcho('own-status', () => p.context.oaSetStatus('own-status', 'error', 'Settings could not be refreshed.'));
  p.context.oaRuntimeEcho('choices-status', () => p.context.oaSetStatus('choices-status', 'error', 'OpenAbstractions is not reachable.'));
  assert.strictEqual(own.textContent, 'Settings could not be refreshed.', 'before the check the section says its own sentence');
  p.context.oaSetStatus('choices-status', 'error', 'Not confirmed.');
  answer({ ok: true, json: async () => ({ error: 'gone' }) });
  await settle();await settle();await settle();
  assert.strictEqual(p.banner.hidden, false);
  assert.strictEqual(own.textContent, '', 'the banner empties the echo');
  assert.strictEqual(other.textContent, 'Not confirmed.', 'a later verdict in the same line reads as itself');
  p.context.oaRuntimeEcho('own-status', () => p.context.oaSetStatus('own-status', 'error', 'Model uses are unavailable right now.'));
  assert.strictEqual(own.textContent, '', 'while the runtime is down the echo is not written');
  assert.deepStrictEqual(visibleSentences(p.doc, true), [], 'one sentence: the banner');
  console.log('PASS: oaRuntimeEcho');
 }
})().catch((e) => { console.error(e); process.exit(1); });
