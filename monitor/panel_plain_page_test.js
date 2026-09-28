// Run with node monitor/panel_plain_page_test.js; no browser or service is
// started. Covers plainScript (monitor/panel_shell.go): oaPlain(text), the
// rule-name it appends to a permission denial, and oaShowError(id, err), the
// one control every page calls in place of writing a raw backend error (or a
// raw outcome string) straight into an element (task 2026-09-23,
// first-time-user walkthrough of the Panel).
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const marker = 'const plainScript = `';
const start = go.indexOf(marker) + marker.length;
assert(start > marker.length - 1, 'plainScript constant not found in panel_shell.go');
const end = go.indexOf('`', start);
const raw = go.slice(start, end);
const script = raw.split('<script>')[1].split('</script>')[0];

// A minimal fake DOM in the same style as the other *_page_test.js files:
// textContent and children are independent properties there too, so
// oaShowError's own explicit replaceChildren() call (not a reliance on
// textContent clearing children, which only real browsers do) is what makes
// a second call idempotent; this node mirrors that shape.
function node() {
 return {
  textContent: '', className: '', children: [],
  replaceChildren() { this.children = []; },
  append(...v) { this.children.push(...v); },
 };
}
const fields = new Map();
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
const context = { document: { getElementById: element, createElement: node }, console };
vm.runInNewContext(script, context);
assert.strictEqual(typeof context.oaPlain, 'function', 'plainScript must define oaPlain');
assert.strictEqual(typeof context.oaShowError, 'function', 'plainScript must define oaShowError');
assert.strictEqual(typeof context.oaActionFallback, 'function', 'plainScript must define oaActionFallback');
const { oaPlain, oaShowError, oaActionFallback } = context;

const PERMIT = 'The runtime did not permit this Panel to do this; nothing was changed.';
const INCOMPATIBLE = "This Panel and the runtime don't speak the same version of this service; update one of them.";
const UNAVAIL = "That service isn't running on this account right now. Start the runtime, or check the service on the Status page.";
const GAP = 'The list changed while reading; start again.';
const INVALID = 'The runtime rejected this request.';
const DEFAULT = "This didn't work.";

// 1. Each of the six buckets, by its trigger substring.
assert.strictEqual(oaPlain('forbidden').line, PERMIT);
assert.strictEqual(oaPlain('this operation is not permitted here').line, PERMIT, '"not permitted" alone triggers the same bucket as "forbidden"');
assert.strictEqual(oaPlain('no rule permits this operation').line, PERMIT, '"no rule" alone triggers the same bucket');
assert.strictEqual(oaPlain('forbidden: history reading not permitted').line, PERMIT, 'the exact Logging-section text from the walkthrough');
// A version mismatch ("incompatible") is worded as its own bucket, not
// folded into "unavailable" (task 2026-09-23 finding 1), and with no
// contract named it keeps the plain fallback sentence.
assert.strictEqual(oaPlain('incompatible').line, INCOMPATIBLE);
assert.strictEqual(oaPlain('unavailable').line, UNAVAIL);

// 1b. When the text names a contract ("abstraction.<layer>/<name>@<n>")
// after "unavailable" or "incompatible", the runtime answered: it either
// lacks that service, or offers it at a version this Panel does not
// understand. Neither sentence repeats the raw contract token itself (task
// 2026-09-23 finding 1: the Graphics card's awake-lease answer must not be
// read as "isn't running" when Status shows every capability Ready).
assert.strictEqual(
 oaPlain('service resolution: unavailable: abstraction.resource/leases@1').line,
 "The runtime is running but doesn't provide the awake-lease service on this account.",
 'unavailable + a resource/leases contract names the awake-lease service, not "isn\'t running"',
);
assert.strictEqual(
 oaPlain('service resolution: unavailable: abstraction.resource/table@1').line,
 "The runtime is running but doesn't provide the graphics-card table on this account.",
);
assert.strictEqual(
 oaPlain('service resolution: incompatible: abstraction.config/observer@1').line,
 "The runtime provides settings watching at a version this Panel doesn't understand; update the runtime or the Panel.",
 'a version mismatch is not read as "not running"',
);
// Every other listed prefix, by contract: job/logging/storage/inference/
// router/credentials/asks/model are named by their bare layer, since every
// contract under that layer means the same thing to the reader; an unlisted
// layer falls back to "that service" rather than invent a name.
const servicePrefixCases = [
 ['abstraction.job/acceptance@1', 'background work'],
 ['abstraction.logging/sink@1', 'the log'],
 ['abstraction.storage/object@1', 'storage'],
 ['abstraction.inference/chat@1', 'inference'],
 ['abstraction.router/router@1', 'the model router'],
 ['abstraction.credentials/holder@1', 'credentials'],
 ['abstraction.asks/application@1', 'questions'],
 ['abstraction.model/descriptor@1', 'model lookup'],
 ['abstraction.example/none@1', 'that service'],
];
for (const [contract, name] of servicePrefixCases) {
 assert.strictEqual(
  oaPlain('service resolution: unavailable: ' + contract).line,
  "The runtime is running but doesn't provide " + name + ' on this account.',
  `contract ${contract} must be named as ${JSON.stringify(name)}`,
 );
}
assert.strictEqual(oaPlain('the listing changed: gap').line, GAP);
assert.strictEqual(oaPlain('invalid request').line, INVALID);
assert.strictEqual(oaPlain('open C:\\Users\\alice\\AppData\\Roaming\\openabstractions\\rights\\admin.secret: The system cannot find the path specified.').line, DEFAULT, 'a raw OS error carries none of the six keywords, so it falls to the plain default rather than showing the path');
assert.strictEqual(oaPlain('').line, DEFAULT, 'empty input still returns a sentence, not a blank line');
assert.strictEqual(oaPlain(null).line, DEFAULT, 'a non-string input is coerced rather than thrown on');

// 2. Rule-name extraction: the full "abstraction.<x>/<y>" form and the bare
//    "<x>.<y>" form are both named, appended to the permission sentence.
assert.strictEqual(oaPlain('forbidden: needs abstraction.resource/table.read').line, PERMIT + ' It needs the rule abstraction.resource/table.read.');
assert.strictEqual(oaPlain('forbidden: needs host.manage').line, PERMIT + ' It needs the rule host.manage.');
assert.strictEqual(oaPlain('forbidden').line, PERMIT, 'no rule shape present, no rule sentence appended');
// A trailing sentence period is not swallowed into the rule name.
assert.strictEqual(oaPlain('Seeing this needs the rule abstraction.resource/table.read.').line, DEFAULT, 'sanity: this text alone carries none of the five keywords');
assert.strictEqual(oaPlain('forbidden. Seeing this needs the rule abstraction.resource/table.read.').line, PERMIT + ' It needs the rule abstraction.resource/table.read.', 'the sentence-ending period after the rule name is not part of it');

// 3. A contract name ("abstraction.<x>/<y>@<n>", a runtime version, never a
//    rule to ask for) is never mistaken for a rule, whole or truncated: the
//    "abstraction.resource" head of "abstraction.resource/table@1" must not
//    leak out as if it were a rule on its own.
assert.strictEqual(oaPlain('forbidden: abstraction.resource/table@1 access denied').line, PERMIT, 'a contract name near a denial is not reported as the rule needed');
assert.strictEqual(oaPlain('forbidden: abstraction.resource/table@1 access denied; needs host.manage').line, PERMIT + ' It needs the rule host.manage.', 'a genuine rule elsewhere in the same text is still found');

// 4. A path-like bare token (a file, not a rule) is skipped by its
//    extension, and the search continues past it to a genuine rule.
assert.strictEqual(oaPlain('forbidden: open admin.secret; needs key.issue').line, PERMIT + ' It needs the rule key.issue.');
assert.strictEqual(oaPlain('forbidden: open admin.secret').line, PERMIT, 'with no genuine rule in the text, none is invented from the file name');

// 5. No raw path, pipe name, security identifier, or contract name survives
//    into any line this function returns, across every example text from
//    the first-time-user walkthrough (task 2026-09-23).
const walkthroughTexts = [
 'service resolution: unavailable: abstraction.resource/table@1 at the installed runtime at \\\\.\\pipe\\openabstractions-user-S-1-5-21-1001',
 'service resolution: incompatible: abstraction.config/observer@1 at the installed runtime at \\\\.\\pipe\\openabstractions-user-S-1-5-21-1001',
 'open C:\\Users\\alice\\AppData\\Roaming\\openabstractions\\rights\\admin.secret: The system cannot find the path specified.',
 'forbidden: history reading not permitted',
 'The runtime did not permit this Panel to read this.',
 'The runtime did not permit this Panel to read this.',
 'router operation not permitted',
];
for (const t of walkthroughTexts) {
 const line = oaPlain(t).line;
 for (const leak of ['\\\\.\\pipe', 'C:\\', 'S-1-5', '@1']) {
  assert(!line.includes(leak), `oaPlain(${JSON.stringify(t)}).line must not contain ${JSON.stringify(leak)}, got ${JSON.stringify(line)}`);
 }
 assert.strictEqual(oaPlain(t).details, t, 'details always carries the original text unchanged, for the disclosure');
}

// 6. oaShowError writes the line as the element's text and, since the raw
//    text almost never equals the plain sentence word for word, appends a
//    Technical-details disclosure holding it; a second, different call
//    replaces the first disclosure's content rather than stacking a second
//    one alongside it.
{
 oaShowError('status-a', 'forbidden: needs host.manage');
 const el = element('status-a');
 assert.strictEqual(el.textContent, PERMIT + ' It needs the rule host.manage.');
 assert.strictEqual(el.children.length, 1, 'the raw text differs from the line, so a disclosure is appended');
 let details = el.children[0];
 assert.strictEqual(details.className, 'oa-error-details');
 assert.strictEqual(details.children[0].textContent, 'Technical details');
 assert.strictEqual(details.children[1].textContent, 'forbidden: needs host.manage', 'the original text is preserved verbatim behind the disclosure');

 // Idempotence: a second, different call replaces the line and the
 // disclosure's content; it does not stack a second disclosure alongside
 // the first.
 oaShowError('status-a', 'gap');
 assert.strictEqual(el.textContent, GAP);
 assert.strictEqual(el.children.length, 1, 'still exactly one disclosure, not two');
 details = el.children[0];
 assert.strictEqual(details.children[1].textContent, 'gap', 'the disclosure now holds only the second call\'s raw text, not both calls\' text');
}

// 7. The one case where oaPlain's line and details coincide (the raw text
//    is itself exactly the default sentence, carrying none of the five
//    keywords) needs no disclosure at all: nothing new to reveal.
{
 oaShowError('status-b', DEFAULT);
 const el = element('status-b');
 assert.strictEqual(el.textContent, DEFAULT);
 assert.strictEqual(el.children.length, 0, 'the raw text equals the line here, so no disclosure is appended');
}

// 8. oaShowError also accepts a caught Error object directly (its .message
//    is used), matching how every page's own catch(e) block already holds
//    one.
{
 oaShowError('status-c', new Error('unavailable: dial tcp: no runtime'));
 assert.strictEqual(element('status-c').textContent, UNAVAIL);
}

// 9. Idempotence the other direction: a call that leaves a disclosure
//    behind, followed by a call that needs none, must not leave the first
//    disclosure stranded on the element.
{
 oaShowError('status-d', 'forbidden: needs key.issue');
 assert.strictEqual(element('status-d').children.length, 1);
 oaShowError('status-d', DEFAULT);
 assert.strictEqual(element('status-d').textContent, DEFAULT);
 assert.strictEqual(element('status-d').children.length, 0, 'the earlier disclosure is gone, not left stacked alongside the new line');
}

// 10. oaActionFallback (task 2026-09-23, sixth first-time visitor, finding
//     2): an action id rulePlain (rights_page.go) and gPlainPhrase
//     (panel_shell.go) do not find in rightsActionPlain reads as
//     "<Layer>: <rest, dots turned to spaces>", never the bare id; both
//     callers append the id itself in parentheses after this, so
//     oaActionFallback's own return carries no parenthesized id.
{
 assert.strictEqual(oaActionFallback('abstraction.storage/content.observe'), 'Storage: content observe');
 assert.strictEqual(oaActionFallback('abstraction.resource/hold'), 'Resource: hold', 'a single word after the slash, no dot to turn into a space, still reads as a sentence');
 assert.strictEqual(oaActionFallback('abstraction.facade/application.activate'), 'Facade: application activate');
 assert.strictEqual(oaActionFallback('not-a-rights-action'), 'not-a-rights-action', 'text that is not an abstraction.<layer>/<rest> action falls back to itself, unchanged, rather than mangle it');
}

console.log('PASS panel plain-language error control');
