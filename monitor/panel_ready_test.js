// Run with node --test monitor/panel_ready_test.js; no browser or service is
// started. Covers the shell's readiness fallback, readyScript in
// monitor/panel_shell.go (SPEC §6): after load it sets data-ready="load"
// unless the parsed page declares data-ready-owner="page" or already called
// oaReady (data-ready="1").
const fs = require('fs'), vm = require('vm'), assert = require('assert'), test = require('node:test');
const go = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const marker = 'const readyScript = `';
const start = go.indexOf(marker);
assert(start >= 0, 'readyScript constant not found in panel_shell.go');
const raw = go.slice(start + marker.length, go.indexOf('`', start + marker.length));
const script = raw.split('<script>')[1].split('</script>')[0];

function run(page) {
  const attrs = new Map(page.ready ? [['data-ready', page.ready]] : []);
  const root = {
    hasAttribute: (n) => attrs.has(n),
    setAttribute: (n, v) => attrs.set(n, v),
  };
  let onload = null;
  const context = {
    window: { addEventListener: (ev, fn) => { if (ev === 'load') onload = fn; } },
    document: {
      documentElement: root,
      // The fake answers only for an element the parsed page holds; the
      // page's raw text (comments, script strings) is never consulted.
      querySelector: (sel) => (sel === '[data-ready-owner="page"]' && page.owner ? {} : null),
    },
  };
  vm.runInNewContext(script, context);
  assert.ok(onload, 'readyScript waits for load');
  onload();
  return attrs.get('data-ready');
}

test('a page without its own readiness gets data-ready="load"', () => {
  assert.strictEqual(run({}), 'load');
});

test('a page that declares data-ready-owner="page" is left to oaReady', () => {
  assert.strictEqual(run({ owner: true }), undefined);
});

test('an oaReady mark set before load is never downgraded', () => {
  assert.strictEqual(run({ ready: '1' }), '1');
});

test('the marker is read from the DOM, not from the source text', () => {
  assert.ok(script.includes("querySelector('[data-ready-owner=\"page\"]')"));
  assert.ok(!/indexOf|innerHTML|outerHTML/.test(script));
});
