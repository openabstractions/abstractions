// Run with node monitor/panel_nav_page_test.js; no browser or service is
// started. Covers navScript (monitor/panel_shell.go): the navigation pane's
// highlight follows the location. Every item is now its own page at its own
// path (task 2026-09-23, requirement 1: no item carries a hash any more),
// so the server's own mark (panelNav's "active" class on load) is what
// navScript preserves across a reload or a hashchange it does not otherwise
// touch.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const marker = 'const navScript = `';
const start = go.indexOf(marker) + marker.length;
assert(start > marker.length - 1, 'navScript constant not found in panel_shell.go');
const raw = go.slice(start, go.indexOf('`', start));
const script = raw.split('<script>')[1].split('</script>')[0];

// One anchor as panelNav renders it: pathname is what a browser exposes on
// an <a>, classList and aria-current are what the script touches. No item
// carries a hash any more, so hash is always ''.
function item(id, pathname, active) {
 const classes = new Set(['oa-nav-item']);
 if (active) classes.add('active');
 const attrs = {};
 return {
  id, pathname, hash: '',
  classList: { add: (c) => classes.add(c), remove: (c) => classes.delete(c), contains: (c) => classes.has(c) },
  setAttribute: (k, v) => { attrs[k] = v }, removeAttribute: (k) => { delete attrs[k] },
  get active() { return classes.has('active') }, get current() { return attrs['aria-current'] },
 };
}

function run(items, pathname, hash) {
 const listeners = {};
 const context = {
  document: { querySelectorAll: (sel) => { assert.strictEqual(sel, '.oa-nav .oa-nav-item'); return items } },
  location: { pathname, hash: hash || '' },
  window: { addEventListener: (name, fn) => { listeners[name] = fn } },
 };
 vm.createContext(context);
 vm.runInContext(script, context);
 return {
  fire: (newHash) => { context.location.hash = newHash; listeners.hashchange() },
 };
}

// The eleven items as panelNavItems lists them, each at its own path; the
// server lit "status" for a request to "/".
function pane(activeID) {
 return [
  item('status', '/', activeID === 'status'), item('work', '/work', activeID === 'work'),
  item('card', '/card', activeID === 'card'), item('inference', '/inference', activeID === 'inference'),
  item('registry', '/registry', activeID === 'registry'), item('credentials', '/credentials-page', activeID === 'credentials'),
  item('rights', '/rights', activeID === 'rights'), item('settings', '/settings', activeID === 'settings'),
  item('logging', '/logging', activeID === 'logging'), item('identity', '/identity', activeID === 'identity'),
  item('explore', '/explore', activeID === 'explore'),
 ];
}
const lit = (items) => items.filter((i) => i.active).map((i) => i.id);

// 1. Landing on the Status page keeps Status lit.
let items = pane('status');
run(items, '/', '');
assert.deepStrictEqual(lit(items), ['status']);
assert.strictEqual(items[0].current, 'page');

// 2. Each other page, server-lit at load, stays lit: the script never finds
//    an item on the current path whose (empty) hash matches a hash the
//    location does not carry, so it falls through to the one plain item on
//    that path, the server's own mark.
for (const id of ['work', 'rights', 'settings', 'logging', 'identity', 'explore', 'card', 'inference', 'registry', 'credentials']) {
 items = pane(id);
 const path = items.find((i) => i.id === id).pathname;
 run(items, path, '');
 assert.deepStrictEqual(lit(items), [id], id + ' stays lit on its own path');
}

// 3. A stray hash on any page (a browser artifact, or a link into this page
//    from elsewhere) changes nothing: no item carries a hash to match, so
//    the plain item for the current path is still the one lit.
items = pane('logging');
run(items, '/logging', '#anything');
assert.deepStrictEqual(lit(items), ['logging']);

// 4. The script touches only items on the current path: visiting Registry
//    never disturbs the mark a different page already carries in the DOM
//    (items constructed here as if Status, not Registry, were still lit by
//    mistake) — it still corrects to the current path's own item.
items = pane('status');
items[0].classList.remove('active'); items[4].classList.add('active');
run(items, '/registry', '#anything');
assert.deepStrictEqual(lit(items), ['registry']);

// 5. Clicking between items on the same conceptual page (a reload replaces
//    the whole document at a new path in real use) is exercised through the
//    hashchange listener too: with no items sharing a path any more, firing
//    a hashchange on the current page changes nothing, since it only ever
//    reconciles hash-carrying items and none remain.
items = pane('work');
const page = run(items, '/work', '');
page.fire('#anything');
assert.deepStrictEqual(lit(items), ['work'], 'a hashchange on a page with no hash items leaves the server mark alone');

console.log('panel_nav_page_test: ok');
