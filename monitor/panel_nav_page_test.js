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

// The eight items as panelNavItems lists them, each at its own path; the
// server lit "status" for a request to "/".
function pane(activeID) {
 return [
  item('status', '/', activeID === 'status'), item('work', '/work', activeID === 'work'),
  item('card', '/card', activeID === 'card'), item('inference', '/inference', activeID === 'inference'),
  item('registry', '/registry', activeID === 'registry'), item('credentials', '/credentials-page', activeID === 'credentials'),
  item('rights', '/rights', activeID === 'rights'), item('settings', '/settings', activeID === 'settings'),
 ];
}
const lit = (items) => items.filter((i) => i.active).map((i) => i.id);

// 1. Landing on the Apps page keeps Status lit.
let items = pane('status');
run(items, '/', '');
assert.deepStrictEqual(lit(items), ['status']);
assert.strictEqual(items[0].current, 'page');

// 2. Each other page, server-lit at load, stays lit: the script never finds
//    an item on the current path whose (empty) hash matches a hash the
//    location does not carry, so it falls through to the one plain item on
//    that path, the server's own mark.
for (const id of ['work', 'rights', 'settings', 'card', 'inference', 'registry', 'credentials']) {
 items = pane(id);
 const path = items.find((i) => i.id === id).pathname;
 run(items, path, '');
 assert.deepStrictEqual(lit(items), [id], id + ' stays lit on its own path');
}

// 3. A stray hash on any page (a browser artifact, or a link into this page
//    from elsewhere) changes nothing: no item carries a hash to match, so
//    the plain item for the current path is still the one lit.
items = pane('settings');
run(items, '/settings', '#anything');
assert.deepStrictEqual(lit(items), ['settings']);

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

// 6. navSearchScript: the search field filters page labels and section
//    titles from the navigation table, Enter opens the first match, Escape
//    clears, and the Permissions badge reads /panel/nav.json.
(async () => {
 const searchMarker = 'const navSearchScript = `';
 const searchStart = go.indexOf(searchMarker) + searchMarker.length;
 assert(searchStart > searchMarker.length - 1, 'navSearchScript constant not found in panel_shell.go');
 const searchRaw = go.slice(searchStart, go.indexOf('`', searchStart));
 const searchScript = searchRaw.split('<script>')[1].split('</script>')[0];
 function node(search, href) {
  const attrs = { 'data-search': search };
  return { hidden: false, href: href || '', textContent: '', getAttribute: (k) => attrs[k], setAttribute: (k, v) => { attrs[k] = v }, attrs };
 }
 const navItems = [node('get started review and install', '/setup'), node('apps your apps', '/'), node('Permissions Pending requests Saved permissions', '/rights'), node('device who uses the graphics card keeping this device awake', '/card')];
 const hits = [node('pending requests', '/rights#questions'), node('saved permissions', '/rights#rights-section'), node('keeping this device awake', '/card#awake-section')];
 hits.forEach((h) => { h.hidden = true });
 const sep = node(''), hitBox = node(''), none = node(''), badge = node('');
 hitBox.hidden = true; none.hidden = true; badge.hidden = true;
 const listeners = {};
 const input = { value: '', addEventListener: (name, fn) => { listeners[name] = fn } };
 const selectors = {
  '.oa-nav .oa-nav-item': navItems, '.oa-nav .oa-nav-hit': hits, '.oa-nav .oa-nav-sep': [sep], '.oa-nav .oa-nav-hits': [hitBox],
  '.oa-nav .oa-nav-nohits': [none], '.oa-nav .oa-nav-item, .oa-nav .oa-nav-hit': navItems.concat(hits), '.oa-nav [data-badge="rights"]': [badge],
 };
 let fetched = '';
 const context = {
  document: {
   getElementById: (id) => { assert.strictEqual(id, 'oa-nav-search'); return input },
   querySelectorAll: (sel) => { if (sel.startsWith('.oa-nav [data-badge=')) return selectors[sel] || []; assert(selectors[sel], 'unexpected selector ' + sel); return selectors[sel] },
   addEventListener: () => {}, visibilityState: 'visible',
  },
  location: { search: '?k=secret', pathname: '/card', href: '' },
  URLSearchParams,
  fetch: (url) => { fetched = url; return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [{ id: 'status' }, { id: 'rights', badge: 2 }], selected: 'status' }) }) },
  setInterval: () => 0,
 };
 vm.createContext(context);
 vm.runInContext(searchScript, context);
 const type = (text) => { input.value = text; listeners.input() };
 const shown = (list) => list.filter((n) => !n.hidden).map((n) => n.href);

 type('awake');
 assert.deepStrictEqual(shown(navItems), ['/card'], 'a section title finds its page');
 assert.deepStrictEqual(shown(hits), ['/card#awake-section'], 'and lists the section itself');
 assert(sep.hidden && !hitBox.hidden && none.hidden);

 type('PERMISSIONS ');
 assert.deepStrictEqual(shown(navItems), ['/rights'], 'search ignores case and surrounding spaces');
 assert.deepStrictEqual(shown(hits), ['/rights#rights-section']);
 let prevented = false;
 listeners.keydown({ key: 'Enter', preventDefault: () => { prevented = true } });
 assert(prevented && context.location.href === '/rights', 'Enter opens the first match');

 type('zzz');
 assert.deepStrictEqual(shown(navItems).concat(shown(hits)), []);
 assert(!none.hidden && hitBox.hidden, 'no match says so in one sentence');

 listeners.keydown({ key: 'Escape' });
 assert.strictEqual(input.value, '');
 assert.strictEqual(shown(navItems).length, navItems.length, 'Escape shows every page again');
 assert(!sep.hidden && hitBox.hidden && none.hidden && hits.every((h) => h.hidden));

 await new Promise((r) => setImmediate(r));
 assert.strictEqual(fetched, '/panel/nav.json?k=secret&path=%2Fcard', 'the poll names this page, so nav.json reads no landing state');
 assert(!badge.hidden && badge.textContent === '2', 'the badge shows the waiting count');
 console.log('panel_nav_page_test: ok');
})().catch((e) => { console.error(e); process.exit(1) });
