// Run with node monitor/panel_rules_page_test.js. Covers static/panel_rules.js,
// the Panel rules page tests apply to the DOM their script rendered: each
// rule on a fake DOM and on a browser-shaped one, and what it leaves alone.
const assert = require('assert');
const { assertPanelRules, panelRuleViolations, rawValues, RULES } = require('./static/panel_rules.js');

assert.deepStrictEqual(RULES, ['raw-value', 'empty-input', 'button-status', 'caption', 'class', 'secret-scope', 'technical-summary']);

// In a browser (no module, no require, as the screenshot tool runs it) the
// class rule says the list is not loaded until setClasses hands it in.
{
 const vm = require('vm'), fs = require('fs');
 const browser = {};
 vm.runInNewContext(fs.readFileSync(__dirname + '/static/panel_rules.js', 'utf8'), browser);
 const rulesInPage = browser.oaPanelRules;
 const tree = { tagName: 'DIV', className: 'oa-row oa-legacy', children: [] };
 assert.deepStrictEqual(Array.from(rulesInPage.panelRuleViolations(tree, { rules: ['class'] }), (v) => v.detail),
  ['the class list (static/panel_classes.json) is not loaded: call setClasses, or pass options.classes']);
 // setClasses refuses anything but the panel_classes.json shape, one case
 // per refusal, and leaves the rule without a list after each.
 assert.throws(() => rulesInPage.setClasses(null), /setClasses needs/, 'no object');
 assert.throws(() => rulesInPage.setClasses('oa-row'), /setClasses needs/, 'a string');
 assert.throws(() => rulesInPage.setClasses({ prefixes: [] }), /classes must be a non-empty array/, 'no classes');
 assert.throws(() => rulesInPage.setClasses({ classes: [], prefixes: [] }), /classes must be a non-empty array/, 'empty classes');
 assert.throws(() => rulesInPage.setClasses({ classes: 'oa-row', prefixes: [] }), /classes must be a non-empty array/, 'classes not an array');
 assert.throws(() => rulesInPage.setClasses({ classes: ['oa-row', 7], prefixes: [] }), /classes must be a non-empty array/, 'a class that is no string');
 assert.throws(() => rulesInPage.setClasses({ classes: ['oa-row', ''], prefixes: [] }), /classes must be a non-empty array/, 'an empty class name');
 assert.throws(() => rulesInPage.setClasses({ classes: ['oa-row'] }), /prefixes must be an array/, 'no prefixes');
 assert.throws(() => rulesInPage.setClasses({ classes: ['oa-row'], prefixes: 'oa-icon-' }), /prefixes must be an array/, 'prefixes not an array');
 assert.throws(() => rulesInPage.setClasses({ classes: ['oa-row'], prefixes: [7] }), /prefixes must be an array/, 'a prefix that is no string');
 assert.throws(() => rulesInPage.setClasses({ classes: ['oa-row'], prefixes: ['oa-icon'] }), /each ending in "-"/, 'a prefix without its trailing "-"');
 assert.throws(() => rulesInPage.setClasses({ classes: ['oa-row'], prefixes: ['-'] }), /after at least one character/, 'a prefix that is only "-"');
 // options.classes is checked the same way, per call.
 const tiny = { tagName: 'DIV', className: 'oa-row', children: [] };
 assert.throws(() => rulesInPage.panelRuleViolations(tiny, { rules: ['class'], classes: { classes: [], prefixes: [] } }), /options\.classes: classes must be a non-empty array/, 'options.classes with no classes');
 assert.throws(() => rulesInPage.panelRuleViolations(tiny, { rules: ['class'], classes: { classes: ['oa-row'], prefixes: ['-'] } }), /options\.classes: prefixes must be/, 'options.classes with a bare "-" prefix');
 assert.throws(() => rulesInPage.panelRuleViolations(tiny, { rules: ['class'], classes: 'oa-row' }), /options\.classes needs/, 'options.classes not an object');
 assert.deepStrictEqual(Array.from(rulesInPage.panelRuleViolations(tiny, { rules: ['class'], classes: { classes: ['oa-row'], prefixes: ['oa-icon-'] } })), [], 'a valid options.classes is used');
 assert.strictEqual(rulesInPage.CLASSES, null, 'a refused list leaves none set');
 rulesInPage.setClasses({ classes: ['oa-row'], prefixes: [] });
 assert.deepStrictEqual(Array.from(rulesInPage.CLASSES.classes), ['oa-row'], 'no prefixes at all is valid');
 rulesInPage.setClasses(JSON.parse(fs.readFileSync(__dirname + '/static/panel_classes.json', 'utf8')));
 assert.deepStrictEqual(Array.from(rulesInPage.panelRuleViolations(tree, { rules: ['class'] }), (v) => v.detail), ['"oa-legacy" on div']);
}

// An exemption applies to its own element and only to the shape it exists
// for: a secret field (a valid data-oa-secret on an input, textarea, code or
// output with no child elements; a password input; a password or
// one-time-code autocomplete) is exempt from empty-input, and its own text
// and placeholder from the hash shape only. data-oa-secret anywhere else is
// a secret-scope violation and exempts nothing.
{
 const node = (tagName, props = {}, ...children) => ({ tagName, textContent: '', className: '', children, ...props });
 const key = 'sk-0123456789abcdef0123456789abcdef';
 const rulesOf = (tree) => panelRuleViolations(tree, { rules: ['empty-input', 'raw-value', 'secret-scope'] }).map((v) => v.rule + ': ' + v.detail);
 const secretTree = node('SECTION', { id: 'keys' },
  node('INPUT', { type: 'text', value: '', 'data-oa-secret': '1', placeholder: key }),
  node('INPUT', { type: 'password', value: '' }),
  node('INPUT', { type: 'text', value: '', autocomplete: 'one-time-code' }),
  node('CODE', { 'data-oa-secret': '1', textContent: key }),
  node('OUTPUT', { 'data-oa-secret': '1', textContent: key }),
 );
 assert.deepStrictEqual(rulesOf(secretTree), []);
 const plainTree = node('SECTION', { id: 'keys' }, node('INPUT', { type: 'text', value: '', placeholder: key }), node('DIV', {}, node('CODE', { textContent: key })));
 assert.deepStrictEqual(rulesOf(plainTree).map((v) => v.split(':')[0]).sort(), ['empty-input', 'raw-value', 'raw-value']);
 // A path, SID or action id inside a valid secret is still caught.
 const inside = rulesOf(node('CODE', { 'data-oa-secret': '1', textContent: 'C:\\Users\\me S-1-5-21-1-2 abstraction.model/lookup ' + key }));
 assert.deepStrictEqual(inside.map((v) => v.split(' ')[1]), ['path', 'SID', 'action']);
 // data-oa-secret on a wrapper is a violation, and its text is not exempt.
 const wrapper = rulesOf(node('DIV', { 'data-oa-secret': '1' }, node('P', { textContent: 'C:\\Users\\me' })));
 assert.deepStrictEqual(wrapper.map((v) => v.split(':')[0]), ['secret-scope', 'raw-value'], JSON.stringify(wrapper));
 assert.deepStrictEqual(rulesOf(node('CODE', { 'data-oa-secret': '1' }, node('SPAN', { textContent: key }))).map((v) => v.split(':')[0]), ['secret-scope', 'raw-value'], 'a code with child elements is no secret field');
 assert.deepStrictEqual(rulesOf(node('DIV', { autocomplete: 'one-time-code', textContent: key })).map((v) => v.split(':')[0]), ['raw-value'], 'an autocomplete exempts only an input or textarea');
}

// Known values (testdata/panel_lint.allowlist known-value rows): exact
// program file names, each allowed its count; one more is caught, and with
// knownExact one fewer is too. Anything but a whole program file name with a
// positive count is refused.
{
 const node = (tagName, props = {}, ...children) => ({ tagName, textContent: '', className: '', children, ...props });
 const page = (n) => node('SECTION', {}, ...Array.from({ length: n }, () => node('P', { textContent: 'coder.exe wants to look up models.' })));
 const rulesWith = (n, exact) => panelRuleViolations(page(n), { rules: ['raw-value'], knownValues: { 'coder.exe': 1 }, knownExact: exact }).map((v) => v.detail);
 assert.deepStrictEqual(rulesWith(1, true), [], 'the counted value passes');
 assert.deepStrictEqual(rulesWith(2, true), ['program "coder.exe" in "coder.exe wants to look up models."'], 'one more is caught');
 assert.deepStrictEqual(rulesWith(0, true), ['known value "coder.exe" allowlisted 1, seen 0: lower or drop its row in testdata/panel_lint.allowlist'], 'one fewer, with knownExact');
 assert.deepStrictEqual(rulesWith(0, false), [], 'without knownExact a missing value is no finding (the screenshot tool)');
 assert.deepStrictEqual(panelRuleViolations(node('P', { textContent: 'C:\\x\\coder.exe' }), { rules: ['raw-value'], knownValues: { 'coder.exe': 1 } }).map((v) => v.detail.split(' in ')[0]), ['path "C:\\\\"'], 'a known program value spares only the program shape');
 for (const [known, why] of [[{ '.exe': 1 }, 'a bare extension'], [{ coder: 1 }, 'a prefix'], [{ 'Abstraction Panel.exe': 1 }, 'two words'], [{ 'coder.exe': 0 }, 'a zero count'], [{ 'coder.exe': 1.5 }, 'a part count'], [['coder.exe'], 'a list']]) {
  assert.throws(() => panelRuleViolations(page(1), { rules: ['raw-value'], knownValues: known }), /options\.knownValues/, why);
 }
 const { knownValues } = require('./panel_fake_dom_test_helper.js');
 assert.deepStrictEqual(knownValues('work'), {}, 'the helper reads the rows: Activity names programs by title and keeps no known file name');
 assert.deepStrictEqual(knownValues('card'), {});
}

// Only an expander whose summary reads "Technical details" may carry
// data-oa-technical (SPEC §6); a counted technical-summary row allows
// another summary exactly that many times.
{
 const node = (tagName, props = {}, ...children) => ({ tagName, textContent: '', className: '', children, ...props });
 const marked = (summary) => node('DETAILS', { 'data-oa-technical': 'true', className: 'oa-expander' }, node('SUMMARY', { textContent: summary }), node('PRE', { textContent: 'x' }));
 const run = (tree, opts = {}) => panelRuleViolations(tree, { rules: ['technical-summary'], ...opts }).map((v) => v.detail);
 assert.deepStrictEqual(run(marked('Technical details')), []);
 assert.deepStrictEqual(run(node('SECTION', {}, marked('Details'))), ['data-oa-technical on an expander whose summary reads "Details": only a "Technical details" expander may carry it']);
 assert.deepStrictEqual(run(node('SECTION', {}, marked('Details'), marked('Details')), { knownSummaries: { Details: 2 }, knownExact: true }), [], 'the counted row');
 assert.deepStrictEqual(run(node('SECTION', {}, marked('Details'), marked('Details'), marked('Details')), { knownSummaries: { Details: 2 } }).length, 1, 'one more is caught');
 assert.deepStrictEqual(run(node('SECTION', {}, marked('Details')), { knownSummaries: { Details: 2 }, knownExact: true }), ['known summary "Details" allowlisted 2, seen 1: lower or drop its row in testdata/panel_lint.allowlist'], 'one fewer, with knownExact');
 assert.deepStrictEqual(run(node('DETAILS', { className: 'oa-error-details' }, node('SUMMARY', { textContent: 'Technical details' }))), [], 'oaShowError details carry no marker attribute');
 for (const bad of [{ 'Technical details': 1 }, { '': 1 }, { ' Details': 1 }, { Details: 0 }]) {
  assert.throws(() => run(marked('Details'), { knownSummaries: bad }), /options\.knownSummaries/, JSON.stringify(bad));
 }
 assert.deepStrictEqual(require('./panel_fake_dom_test_helper.js').knownSummaries('rights'), {}, 'Permissions marks only Technical details expanders');
}

// Expander scope (SPEC §6): an expander's body, open or closed, is judged
// like the page; only a Technical details body (details marked
// data-oa-technical, or .oa-error-details) is spared, and only from
// raw-value. Every summary is visible text.
{
 const node = (tagName, props = {}, ...children) => ({ tagName, textContent: '', className: '', children, ...props });
 const kinds = (tree) => panelRuleViolations(tree, { rules: ['raw-value', 'empty-input'] }).map((v) => v.rule + ': ' + v.detail.split(' in ')[0]);
 for (const open of [true, false]) {
  const tree = node('SECTION', {}, node('DETAILS', { className: 'oa-expander', open },
   node('SUMMARY', { textContent: 'Abstraction Panel.exe: Rules (16)' }),
   node('DIV', {}, node('P', { textContent: 'C:\\Users\\me' }), node('INPUT', { type: 'text', value: '', id: 'folder' }))));
  assert.deepStrictEqual(kinds(tree), ['raw-value: program "Panel.exe"', 'raw-value: path "C:\\\\"', 'empty-input: input id="folder"'], 'an expander body is judged, open = ' + open);
 }
 for (const marker of [{ 'data-oa-technical': '' }, { className: 'oa-error-details' }]) {
  const tree = node('SECTION', {}, node('DETAILS', { className: 'oa-expander', ...marker },
   node('SUMMARY', { textContent: 'Technical details' }),
   node('PRE', { textContent: 'C:\\Users\\me S-1-5-21-1-2 abstraction.model/lookup sha256:0123456789abcdef' }),
   node('INPUT', { type: 'text', value: '', id: 'raw' })));
  if (marker['data-oa-technical'] === '') tree.children[0].hasAttribute = (k) => k === 'data-oa-technical';
  assert.deepStrictEqual(kinds(tree), ['empty-input: input id="raw"'], 'a Technical details body is spared from raw-value only: ' + JSON.stringify(marker));
 }
 // A Technical details summary is still visible text.
 const summaryTree = node('DETAILS', { className: 'oa-error-details' }, node('SUMMARY', { textContent: 'C:\\Users\\me' }));
 assert.deepStrictEqual(kinds(summaryTree), ['raw-value: path "C:\\\\"']);
}

// The class rule reads the one class list (static/panel_classes.json, the
// file panel_shell.go embeds): a function-built class outside it is caught,
// hidden or not; a completed listed prefix and an allowlisted class pass.
{
 const { CLASSES } = require('./static/panel_rules.js');
 assert.deepStrictEqual(CLASSES, require('./static/panel_classes.json'));
 const built = (kind) => ({ tagName: 'SPAN', className: 'oa-row-label ' + kind, textContent: 'x', children: [] });
 const tree = { tagName: 'DIV', className: 'oa-row', children: [built('oa-legacy'), { tagName: 'I', className: 'oa-icon-card', children: [] }, { tagName: 'P', className: 'oa-note', hidden: true, children: [] }] };
 assert.deepStrictEqual(panelRuleViolations(tree, { rules: ['class'] }).map((v) => v.detail), ['"oa-legacy" on span "x"', '"oa-note" on p']);
 assert.deepStrictEqual(panelRuleViolations(tree, { rules: ['class'], allowClasses: ['oa-legacy', 'oa-note'] }), []);
 assert.deepStrictEqual(panelRuleViolations({ tagName: 'B', className: 'oa-icon-', children: [] }, { rules: ['class'] }).length, 1, 'a bare prefix is no class');
}
assert.deepStrictEqual(rawValues('Program C:\\Users\\me\\app.exe for S-1-5-21-1-2-3, digest 0123456789abcdef0123, /home/me/x').map((v) => v.shape), ['path', 'path', 'SID', 'hash', 'program']);
assert.deepStrictEqual(rawValues('llama-server on the graphics card, 12 GB, today at 6:14 PM'), []);
assert.deepStrictEqual(rawValues('Abstraction Panel.exe and Notes.app use kernel32.dll').map((v) => v.match), ['Panel.exe', 'Notes.app', 'kernel32.dll'], 'a program file name is no visible text');
// An address is no program: a match followed by "/" or by ":" and a digit,
// or right after "://", is skipped; a bare host name still matches, and so
// does a file name ending a sentence's clause before ": ".
for (const address of ['https://example.web.app', 'example.web.app/path', 'example.web.app:8443', 'http://notes.app/x']) {
 assert.deepStrictEqual(rawValues(address).filter((v) => v.shape === 'program'), [], address);
}
assert.deepStrictEqual(rawValues('see example.web.app').map((v) => v.match), ['example.web.app']);
assert.deepStrictEqual(rawValues('Abstraction Panel.exe: Rules (16)').map((v) => v.match), ['Panel.exe']);
assert.deepStrictEqual(rawValues('It needs abstraction.model/lookup and abstraction.job/acceptance@1.').map((v) => v.shape + ' ' + v.match), ['action abstraction.model/lookup', 'action abstraction.job/acceptance@1'], 'a sentence\'s full stop is no part of the id');
assert.deepStrictEqual(rawValues('Model lookup, the abstraction layer'), []);

// A fake DOM as page tests build it: own text beside children, no parent.
const el = (tagName, props = {}, ...children) => ({ tagName, textContent: '', className: '', children, ...props });
const fake = [
 el('SECTION', { id: 'a' },
  el('P', { textContent: 'One caption.' }),
  el('P', { textContent: 'A second caption.' }),
  el('P', { role: 'status', textContent: '' }),
  el('DIV', {}, el('SPAN', { textContent: 'C:\\Users\\me\\app.exe', title: 'kept' }), el('SPAN', { textContent: 'app', title: 'C:\\Users\\me\\app.exe' })),
  el('DETAILS', { className: 'oa-error-details' }, el('PRE', { textContent: 'S-1-5-21-9 C:\\x abcdefabcdefabcdefab' })),
  el('INPUT', { type: 'text', value: '', id: 'empty' }),
  el('INPUT', { type: 'password', value: '' }),
  el('INPUT', { type: 'text', value: '', hidden: true }),
  el('INPUT', { type: 'search', value: '' }),
  el('INPUT', { type: 'text', value: 'kept' }),
  el('BUTTON', { textContent: 'Save' }),
 ),
 el('SECTION', { id: 'b' }, el('BUTTON', { textContent: 'Lost' })),
 el('SECTION', { id: 'c' }, el('DIV', { className: 'oa-row' }, el('BUTTON', { textContent: 'Row' }), el('P', { className: 'oa-status' }))),
 el('DIV', {}, el('BUTTON', { textContent: 'Outside' })),
];
const rules = (found) => found.map((v) => v.rule);
const found = panelRuleViolations(fake);
assert.deepStrictEqual(rules(found).sort(), ['button-status', 'button-status', 'caption', 'empty-input', 'raw-value', 'raw-value'], JSON.stringify(found, null, 1));
assert(found.some((v) => v.rule === 'empty-input' && v.detail.includes('"empty"')));
assert.deepStrictEqual(found.filter((v) => v.rule === 'button-status').map((v) => v.detail), ['button "Lost"', 'button "Outside"'], 'a button with a status line in its row or section passes');
assert.deepStrictEqual(found.filter((v) => v.rule === 'caption').map((v) => v.detail), ['section "a": "A second caption."']);
assert.deepStrictEqual(rules(panelRuleViolations(fake, { rules: ['raw-value'] })), ['raw-value', 'raw-value'], 'the path and the program file name it ends in');
assert.throws(() => assertPanelRules(fake, 'Fixture'), /Fixture breaks the Panel rules:\n  /);
assert.doesNotThrow(() => assertPanelRules(el('SECTION', {}, el('P', { textContent: 'One caption.' }), el('P', { role: 'status', textContent: 'Saved.' }), el('BUTTON', { textContent: 'Save' }))));

// A browser-shaped DOM: text in text child nodes, attributes by getAttribute.
function element(name, attrs, ...childNodes) {
 const children = childNodes.filter((c) => c.nodeType === 1);
 return { nodeType: 1, tagName: name.toUpperCase(), childNodes, children, hidden: 'hidden' in attrs, value: attrs.value || '',
  get textContent() { return childNodes.map((c) => c.nodeType === 3 ? c.data : c.textContent).join('') },
  getAttribute: (k) => (k in attrs ? attrs[k] : null), hasAttribute: (k) => k in attrs };
}
const text = (data) => ({ nodeType: 3, data });
const page = element('main', {},
 element('section', { id: 's' },
  element('p', {}, text('Apps that use OpenAbstractions.')),
  element('p', { role: 'status' }, text('Loading…')),
  element('div', {}, element('span', { title: 'C:\\Tools\\agent.exe' }, text('agent')), element('details', { 'data-oa-technical': '' }, element('summary', {}, text('Technical details')), element('pre', {}, text('C:\\Tools\\agent.exe'))))),
 element('section', { id: 't', hidden: '' }, element('p', {}, text('/home/me/hidden'))),
);
assert.deepStrictEqual(panelRuleViolations(page), [], 'titles, disclosures and hidden sections are no visible text');
page.children[0].children[2].childNodes.push(text('C:\\Tools\\agent.exe'));
assert.deepStrictEqual(rules(panelRuleViolations(page)), ['raw-value', 'raw-value'], 'a path written as text is caught, and the program file name it ends in');

// The shared fake DOM (panel_fake_dom_test_helper.js) as a page test uses
// it: markup elements by id, a script building rows, a row replaced in
// place, querySelector by class and attribute, and the rules over the body.
{
 const { fakeDocument } = require('./panel_fake_dom_test_helper.js');
 const doc = fakeDocument();
 const section = doc.element('apps-section', 'section');
 doc.element('apps-status', 'p', section).setAttribute('role', 'status');
 const list = doc.element('apps', 'div', section);
 function row(name, program, evidence) {
  const r = doc.createElement('div'); r.className = 'oa-row'; r.dataset.evidence = evidence;
  const label = doc.createElement('span'); label.className = 'oa-row-label'; label.textContent = name; label.title = program;
  const remove = doc.createElement('button'); remove.textContent = 'Remove';
  const status = doc.createElement('p'); status.className = 'oa-status';
  r.append(label, remove, status);
  return r;
 }
 list.append(row('Editor', 'C:\\Apps\\editor.exe', 'verified'), row('agent', 'C:\\Tools\\agent.exe', 'claimed'));
 assert.strictEqual(doc.getElementById('apps').children.length, 2);
 assert.strictEqual(doc.querySelectorAll('#apps .oa-row[data-evidence="claimed"] .oa-row-label')[0].textContent, 'agent');
 assert.strictEqual(list.firstElementChild.getAttribute('data-evidence'), 'verified', 'dataset and data-* attributes are one');
 assert.doesNotThrow(() => assertPanelRules(doc.body, 'Apps fixture'));
 // An in-place update: the second row is replaced, the first kept.
 const first = list.children[0];
 list.replaceChild(row('C:\\Tools\\agent.exe', 'C:\\Tools\\agent.exe', 'verified'), list.children[1]);
 assert.strictEqual(list.children[0], first);
 assert.deepStrictEqual(rules(panelRuleViolations(doc.body)), ['raw-value', 'raw-value'], 'a path written into a row label is caught, and its program file name');
 list.removeChild(list.children[1]);
 doc.getElementById('apps-status').textContent = 'Removed.';
 assert.strictEqual(section.querySelector('[role="status"]').textContent, 'Removed.');
 assert.deepStrictEqual(panelRuleViolations(doc.body), []);
}

// The shared live-update check passes a page that updates its rows in place
// and fails one that rebuilds them or rewrites the row's sentence.
(async () => {
 const { fakeDocument, assertLiveUpdateKeepsRow } = require('./panel_fake_dom_test_helper.js');
 function page(rebuild, rewrite) {
  const doc = fakeDocument(), list = doc.element('jobs');
  const server = { state: 'running' };
  const rows = new Map();
  function render() {
   if (rebuild) { list.replaceChildren(); rows.clear(); }
   let r = rows.get('op-1');
   if (!r) {
    r = doc.createElement('div'); r.className = 'oa-row';
    const label = doc.createElement('span'), button = doc.createElement('button'), status = doc.createElement('p');
    label.className = 'oa-row-label'; status.className = 'oa-status'; button.textContent = 'Cancel';
    button.onclick = () => { status.textContent = 'Cancellation requested.'; };
    r.append(label, button, status); list.appendChild(r); rows.set('op-1', r);
   }
   r.querySelector('.oa-row-label').textContent = 'download · ' + server.state;
   if (rewrite) r.querySelector('.oa-status').textContent = 'State: ' + server.state + '.';
  }
  render();
  return { doc, server, render };
 }
 const check = (p) => assertLiveUpdateKeepsRow({
  click: () => p.doc.querySelector('#jobs button').onclick(),
  change: () => { p.server.state = 'cancelling'; },
  data: () => p.server,
  tick: async () => p.render(),
  row: () => p.doc.getElementById('jobs').children[0],
  status: (row) => row.querySelector('.oa-status'),
  label: 'fixture',
 });
 const kept = page(false, false);
 const { sentence } = await check(kept);
 assert.strictEqual(sentence, 'Cancellation requested.');
 assert.strictEqual(kept.doc.querySelector('.oa-row-label').textContent, 'download · cancelling', 'the row itself took the new state');
 await assert.rejects(check(page(true, false)), /replaced the row element/);
 await assert.rejects(check(page(false, true)), /rewrote the row sentence/);
 // A change that leaves the list's data as it was proves nothing and fails.
 const idle = page(false, false);
 await assert.rejects(assertLiveUpdateKeepsRow({
  click: () => idle.doc.querySelector('#jobs button').onclick(),
  change: () => { idle.server.state = 'running'; },
  data: () => idle.server,
  tick: async () => idle.render(),
  row: () => idle.doc.getElementById('jobs').children[0],
  status: (row) => row.querySelector('.oa-status'),
  label: 'fixture',
 }), /left the list's data as it was/);
 await assert.rejects(assertLiveUpdateKeepsRow({ change: () => {}, tick: async () => {}, row: () => null, status: () => null }), /pass data/);
 console.log('PASS panel rules on rendered DOM');
})().catch((e) => { console.error(e); process.exit(1); });
