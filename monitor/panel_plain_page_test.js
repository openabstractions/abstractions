// Run with node monitor/panel_plain_page_test.js; no browser or service is
// started. Covers plainScript (monitor/panel_shell.go): oaPlain(text), the
// rule-name it appends to a permission denial, and oaShowError(id, err), the
// one control every page calls in place of writing a raw backend error (or a
// raw outcome string) straight into an element (task 2026-09-23,
// first-time-user walkthrough of the Panel).
const fs = require('fs'), vm = require('./panel_record_test_helper.js').vm, assert = require('assert');
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
const { oaPlain, oaShowError, oaShowResult, oaActionFallback } = context;

// oaModelName: a model's friendly name, never a path or a hash.
assert.strictEqual(typeof context.oaModelName, 'function', 'plainScript must define oaModelName');
for (const [raw, want] of [
 // A bare id reads in words (architect ruling 2026-11-09, second block).
 ['qwen3.6-demo', 'Qwen3.6 demo'],
 ['qwen2.5-0.5b', 'Qwen2.5 0.5B'],
 ['smollm2-135m', 'SmolLM2 135M'],
 ['qwen3.6-35b-a3b-npu2', 'Qwen3.6 35B A3B NPU2'],
 ['flux2-klein-4b', 'FLUX.2 klein 4B'],
 ['whisper.cpp-large-v3-turbo', 'Whisper large v3 turbo'],
 ['sentence-transformer-mini', 'Sentence transformer mini'],
 ['qwen3:8b', 'Qwen3 8B'],
 ['qwen2.5-0.5b-instruct-q4_k_m', 'Qwen2.5 0.5B instruct Q4_K_M'],
 ['sdxl-checkpoint', 'SDXL checkpoint'],
 ['z-image-turbo', 'Z-Image turbo'],
 ['z-image', 'Z-Image'],
 ['unsloth/Qwen3-8B-GGUF', 'Qwen3-8B-GGUF'],
 ['C:\\Models\\gguf\\Qwen3-8B-Q4_K_M.gguf', 'Qwen3-8B-Q4_K_M'],
 ['/home/me/models/flux.safetensors', 'FLUX'],
 ['sha256-0123456789abcdef0123456789abcdef', 'Unnamed model'],
 ['0123456789abcdef0123', 'Unnamed model'],
 // The model-label digest guard (owner, 2026-11-01): Setup's download, a
 // digest anywhere in a path, and a Hugging Face cache path with its commit.
 ['55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1.gguf', 'Unnamed model'],
 ['models/' + 'a'.repeat(64) + '/model.gguf', 'Model'],
 ['models--Qwen--x/snapshots/5bc3e238d916f48a861bac2f8a1990a0e9b7e98d/qwen.gguf', 'Qwen'],
 ['', 'Unnamed model'],
 [null, 'Unnamed model'],
]) assert.strictEqual(context.oaModelName(raw), want, 'oaModelName(' + JSON.stringify(raw) + ')');
// oaEngineName: an engine or model server by its upstream name, never an
// internal program name.
for (const [raw, want] of [
 ['modelhost', 'llama.cpp'], ['llama.cpp', 'llama.cpp'], ['pytorch-image', 'Diffusers'], ['pytorchd', 'Diffusers'],
 ['sdcpp', 'stable-diffusion.cpp'], ['whispercpp', 'whisper.cpp'], ['lemonade-image', 'Lemonade'], ['thenoise-image', 'TheNoise'],
 ['comfy-image-1828', 'ComfyUI'], ['deepseek', 'DeepSeek'], ['local-http', 'Local http'], ['My Server', 'My Server'], ['', 'Unnamed engine'],
]) assert.strictEqual(context.oaEngineName(raw), want, 'oaEngineName(' + JSON.stringify(raw) + ')');
// Library treats only this roster as known when old model rows have no
// explicit runs_on list. Resource aliases must not enter that roster.
assert.deepStrictEqual(Object.keys(context.oaEngineTitles).sort(), [
 'comfy-image', 'comfyui', 'deepseek', 'diffusers', 'fastflowlm', 'flm',
 'kokoros', 'lemonade', 'lemonade-image', 'llama.cpp', 'llamacpp',
 'lm-studio', 'lmstudio', 'localai', 'modelhost', 'modelhostd', 'ollama',
 'openai', 'pytorch', 'pytorch-image', 'pytorchd', 'sd-server', 'sd.cpp',
 'sdcpp', 'stable-diffusion.cpp', 'thenoise', 'thenoise-image',
 'vllm', 'whisper.cpp', 'whispercpp',
]);
assert.strictEqual(context.oaEngineName('  PYTORCHD-abcd '), 'Diffusers');
assert.strictEqual(context.oaEngineName('hf'), 'Hf');

for (const [raw, want] of [
 ['WorkloadsSessionHost', 'Workloads session host'], ['llama-server', 'Llama server'], ['OpenAIServer', 'OpenAIServer'], ['LM Studio', 'LM Studio'],
]) assert.strictEqual(context.oaNameWords(raw), want, 'oaNameWords(' + JSON.stringify(raw) + ')');

// oaLocalTime and oaWhen: "Checked today at 1:10 PM", "yesterday at", a date
// for an older day; never "Checked at today 1:10 PM".
{
 const now = new Date(), at = offset => new Date(now.getFullYear(), now.getMonth(), now.getDate() + offset, 13, 10);
 const clock = d => context.oaClockText(d, false), older = at(-3);
 assert.strictEqual(context.oaLocalTime(at(0)), 'today at ' + clock(at(0)));
 assert.strictEqual(context.oaLocalTime(at(-1).toISOString()), 'yesterday at ' + clock(at(-1)));
 assert.strictEqual(context.oaLocalTime(at(1).getTime()), 'tomorrow at ' + clock(at(1)), 'an expiry reads "Until tomorrow at"');
 assert.strictEqual(context.oaLocalTime(older), older.toLocaleDateString(), 'an older day reads its date');
 assert.strictEqual('Checked ' + context.oaWhen(at(0)), 'Checked today at ' + clock(at(0)));
 assert.strictEqual('Checked ' + context.oaWhen(at(-1)), 'Checked yesterday at ' + clock(at(-1)));
 assert.strictEqual('Checked ' + context.oaWhen(older), 'Checked on ' + older.toLocaleDateString());
 assert.strictEqual(context.oaWhen(''), '');
 assert.strictEqual(context.oaWhen('not a time'), 'not a time');
}

// oaBytes: every size as Windows Explorer counts it, 1024 to a KB, three
// significant digits (architect ruling 2026-11-09).
const KiB = 1024, MiB = KiB * KiB, GiB = MiB * KiB;
for (const [value, want] of [
 [0, '0 bytes'], [1, '1 byte'], [512, '512 bytes'], [999, '999 bytes'], [1000, '0.98 KB'], [1536, '1.5 KB'],
 [1e9, '954 MB'], [8.27 * GiB, '8.27 GB'], [22.9e9, '21.3 GB'], [412 * GiB, '412 GB'], [4 * GiB, '4 GB'],
 [1023.9 * MiB, '1 GB'], [2 * GiB * 1024, '2 TB'], [-5, '0 bytes'], ['x', '0 bytes'],
]) assert.strictEqual(context.oaBytes(value), want, 'oaBytes(' + value + ')');
assert.strictEqual(context.oaBytesOf(6.2e9, 16.1e9), '5.77 of 15 GB', 'progress reads in the unit of its total');
assert.strictEqual(context.oaBytesOf(20e9, 16.1e9), '15 of 15 GB', 'progress never passes its total');
assert.strictEqual(context.oaBytesOf(300, 900), '300 of 900 bytes');
const PERMIT = 'OpenAbstractions did not permit this Panel to do this; nothing was changed.';
const INCOMPATIBLE = "This Panel and OpenAbstractions don't speak the same version of this service; update OpenAbstractions.";
const UNAVAIL = "That service isn't running on this account right now. Check it on the Apps page.";
const GAP = 'The list changed while reading; start again.';
const INVALID = 'OpenAbstractions rejected this request.';
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
const updateSharing = 'integration update stopped. Recovery files preserved (previous image: , staged image: dummy.tmp): current image admission: A file cannot be opened because the share access flags are incompatible.';
assert.strictEqual(oaPlain(updateSharing).line, 'The connection helper is still in use. Fully quit the agent and close other connection setup windows, then check again. Recovery files were preserved.');
assert.strictEqual(oaPlain(updateSharing).details, updateSharing);
assert.strictEqual(oaPlain(updateSharing.replace('current image admission','staged image admission')).line, oaPlain(updateSharing).line);
assert.strictEqual(oaPlain('A file cannot be opened because the share access flags are incompatible.').line, DEFAULT, 'an operating-system error is not a service version verdict');
assert.strictEqual(oaPlain('unavailable').line, UNAVAIL);

// 1a. No runtime answered (resolution status runtime_unavailable): the
// Apps banner's sentence, naming the service the read needed, and the raw
// text kept as the details (panel audit 2026-11-01: Activity read "The
// runtime is running but doesn't provide inference" with the runtime
// stopped).
{
 const pipe = 'service resolution: runtime_unavailable: abstraction.inference/operator@1 (capability abstraction.inference) at the explicit endpoint \\\\.\\pipe\\openabstractions-user-S-1-5-21-1-2-3-1001-review-runtime: open \\\\.\\pipe\\openabstractions-user-S-1-5-21-1-2-3-1001-review-runtime: The system cannot find the file specified.';
 const stopped = oaPlain(pipe);
 assert.strictEqual(stopped.line, 'OpenAbstractions is not running, so inference cannot be read. Open OpenAbstractions from the Start menu, then check again.');
 assert.strictEqual(stopped.details, pipe, 'the Technical details hold the resolution text');
 assert.strictEqual(oaPlain(pipe.replace('abstraction.inference/operator@1', 'abstraction.facade/messages@1')).line, 'OpenAbstractions is not running, so messages cannot be read. Open OpenAbstractions from the Start menu, then check again.');
 assert.strictEqual(oaPlain(pipe.replace('abstraction.inference/operator@1', 'abstraction.job/operator@1')).line, 'OpenAbstractions is not running, so background work cannot be read. Open OpenAbstractions from the Start menu, then check again.');
 assert.strictEqual(oaPlain(pipe.replace('abstraction.inference/operator@1', 'abstraction.asks/notification-operator@1')).line, 'OpenAbstractions is not running, so notifications cannot be read. Open OpenAbstractions from the Start menu, then check again.');
 assert.strictEqual(oaPlain('service resolution: runtime_unavailable').line, 'OpenAbstractions is not running. Open OpenAbstractions from the Start menu, then check again.');
 assert.strictEqual(oaPlain('service resolution: runtime_unavailable: abstraction.example/none@1 (capability abstraction.example) at the installed runtime: dial').line, 'OpenAbstractions is not running. Open OpenAbstractions from the Start menu, then check again.', 'an unnamed service is left out of the sentence');
 // The page's runtime banner (runtimeScript) says it once; the section
 // that failed with it stays empty.
 oaShowError('status-stopped', new Error(pipe));
 const shown = element('status-stopped');
 assert.strictEqual(shown.textContent, '');
 assert.strictEqual(shown.children.length, 0, 'no sentence and no disclosure in the section');
}

// 1b. When the text names a contract ("abstraction.<layer>/<name>@<n>")
// after "unavailable" or "incompatible", the runtime answered: it either
// lacks that service, or offers it at a version this Panel does not
// understand. Neither sentence repeats the raw contract token itself (task
// 2026-09-23 finding 1: the Graphics card's awake-lease answer must not be
// read as "isn't running" when Status shows every capability Ready).
assert.strictEqual(
 oaPlain('service resolution: unavailable: abstraction.resource/leases@1').line,
 "OpenAbstractions is running but doesn't provide the awake-lease service on this account.",
 'unavailable + a resource/leases contract names the awake-lease service, not "isn\'t running"',
);
assert.strictEqual(
 oaPlain('service resolution: unavailable: abstraction.resource/table@1').line,
 "OpenAbstractions is running but doesn't provide the graphics-card table on this account.",
);
assert.strictEqual(
 oaPlain('service resolution: incompatible: abstraction.config/observer@1').line,
 "OpenAbstractions provides settings watching at a version this Panel doesn't understand; update OpenAbstractions.",
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
  "OpenAbstractions is running but doesn't provide " + name + ' on this account.',
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
assert.strictEqual(oaPlain('forbidden: needs abstraction.resource/table.read').line, PERMIT + ' It needs the “resource table read” permission.');
assert.strictEqual(oaPlain('forbidden: needs host.manage').line, PERMIT + ' It needs the “host manage” permission.');
assert.strictEqual(oaPlain('forbidden').line, PERMIT, 'no rule shape present, no rule sentence appended');
// A trailing sentence period is not swallowed into the rule name.
assert.strictEqual(oaPlain('Seeing this needs the rule abstraction.resource/table.read.').line, DEFAULT, 'sanity: this text alone carries none of the five keywords');
assert.strictEqual(oaPlain('forbidden. Seeing this needs the rule abstraction.resource/table.read.').line, PERMIT + ' It needs the “resource table read” permission.', 'the sentence-ending period after the rule name is not part of it');

// 3. A contract name ("abstraction.<x>/<y>@<n>", a runtime version, never a
//    rule to ask for) is never mistaken for a rule, whole or truncated: the
//    "abstraction.resource" head of "abstraction.resource/table@1" must not
//    leak out as if it were a rule on its own.
assert.strictEqual(oaPlain('forbidden: abstraction.resource/table@1 access denied').line, PERMIT, 'a contract name near a denial is not reported as the rule needed');
assert.strictEqual(oaPlain('forbidden: abstraction.resource/table@1 access denied; needs host.manage').line, PERMIT + ' It needs the “host manage” permission.', 'a genuine rule elsewhere in the same text is still found');

// 4. A path-like bare token (a file, not a rule) is skipped by its
//    extension, and the search continues past it to a genuine rule.
assert.strictEqual(oaPlain('forbidden: open admin.secret; needs key.issue').line, PERMIT + ' It needs the “key issue” permission.');
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
 assert.strictEqual(el.textContent, PERMIT + ' It needs the “host manage” permission.');
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
 assert.strictEqual(oaActionFallback('abstraction.credentials/full-control@1'), 'Credentials: full-control', 'a permission set reads without its version');
 assert.strictEqual(oaActionFallback('not-a-rights-action'), 'This permission', 'text that is not an abstraction.<layer>/<rest> action reads as a generic phrase, never as the raw text');
}

// 11. A status line (oaStatus) starts in its loading state and leaves it
//     with the first sentence: oaSetStatus, oaShowError and oaShowResult
//     each set data-state and clear aria-busy, so a page never shows an
//     error before its data had the chance to arrive.
{
 const attrs = { 'data-state': 'loading', 'aria-busy': 'true' };
 const line = Object.assign(node(), { setAttribute(k, v) { attrs[k] = v }, removeAttribute(k) { delete attrs[k] } });
 line.textContent = 'Loading…';
 fields.set('line', line);
 context.oaSetStatus('line', 'loading');
 assert.strictEqual(line.textContent, 'Loading…');
 assert.strictEqual(attrs['aria-busy'], 'true');
 context.oaSetStatus('line', 'ok', 'Saved.');
 assert.strictEqual(line.textContent, 'Saved.');
 assert.strictEqual(attrs['data-state'], 'ok');
 assert(!('aria-busy' in attrs), 'a sentence ends the loading state');
 oaShowError('line', 'forbidden');
 assert.strictEqual(attrs['data-state'], 'error');
 context.oaShowResult('line', 'Done.');
 assert.strictEqual(attrs['data-state'], 'ok');
 assert.strictEqual(line.textContent, 'Done.');
}

// 11a. oaResourceLabel reads a content digest as "Stored content", never
//      the hash itself; a store, wake and a bare name read in words
//      (architect ruling 2026-11-09); other resources keep their own words.
{
 const { oaResourceLabel } = context;
 assert.deepStrictEqual(Object.keys(context.oaResourceTitles).sort(), [
  'civitai', 'comfyui', 'deepseek', 'fastflowlm', 'hf', 'huggingface', 'jan',
  'lemonade', 'lmstudio', 'modelhost', 'oa-chat', 'ollama',
 ]);
 assert.strictEqual(oaResourceLabel('store:lm-studio'), 'Lm studio models');

 assert.strictEqual(oaResourceLabel('sha256:0123456789abcdef0123456789abcdef'), 'Stored content');
 assert.strictEqual(oaResourceLabel('card:0'), 'Graphics card');
 assert.strictEqual(oaResourceLabel('card:1'), 'Graphics card 2');
 assert.strictEqual(oaResourceLabel('abstraction.job/acceptance@1'), 'this service');
 assert.strictEqual(oaResourceLabel('huggingface'), 'Hugging Face', 'a bare name reads as its app');
 assert.strictEqual(oaResourceLabel('hf'), 'Hugging Face');
 assert.strictEqual(oaResourceLabel('store:modelhost-abcd'), 'llama.cpp models');
 assert.strictEqual(oaResourceLabel('lemonade'), 'Lemonade');
 assert.strictEqual(oaResourceLabel('thenoise-image'), 'Thenoise image');
 assert.strictEqual(oaResourceLabel('my-registry'), 'My registry', 'an unknown bare name reads in words');
 assert.strictEqual(oaResourceLabel('store:lmstudio'), 'LM Studio models');
 assert.strictEqual(oaResourceLabel('store:new-app'), 'New app models');
 assert.strictEqual(oaResourceLabel('store:oa-chat-467d9c6e43391a07c5853578'), 'Setup chat models', 'a digest suffix names no product');
 assert.strictEqual(oaResourceLabel('awake'), 'Keeping this device awake');
 assert.strictEqual(oaResourceLabel('Already Named'), 'Already Named');
}

// 11b. oaSetAccent: a host pushes the accent and its two shades onto the
//      root, each only as a #rrggbb colour.
{
 const set = {};
 const accentContext = { document: { documentElement: { dataset: {}, style: { setProperty: (k, v) => { set[k] = v } } } } };
 vm.runInNewContext(script, accentContext);
 accentContext.oaSetAccent('#A1B2C3', 'red;}', '#102030');
 assert.deepStrictEqual(set, { '--oa-accent': '#A1B2C3', '--oa-accent-dark1': '#102030' });
 // oaReady marks the root settled for the screenshot tool.
 accentContext.oaReady();
 assert.strictEqual(accentContext.document.documentElement.dataset.ready, '1');
}

// 11c. oaKeyedRows keeps one row per key: a kept key's row is the same
//      element, updated in place with its status sentence; a new key gets a
//      created row; a gone key's row leaves; the order follows the items;
//      an unchanged list is not touched.
{
 const { fakeDocument } = require('./panel_fake_dom_test_helper.js');
 const doc = fakeDocument();
 const keyedContext = { document: doc };
 vm.runInNewContext(script, keyedContext);
 const list = doc.element('jobs');
 let created = 0;
 const create = (item) => { created++; const r = doc.createElement('div'); r.className = 'oa-row'; const label = doc.createElement('span'); const status = doc.createElement('p'); status.className = 'oa-status'; r.append(label, status); return r; };
 const update = (r, item) => { r.children[0].textContent = item.name + ' · ' + item.state; };
 const render = (items) => keyedContext.oaKeyedRows(list, items, (it) => it.id, create, update);
 render([{ id: 'a', name: 'A', state: 'running' }, { id: 'b', name: 'B', state: 'queued' }]);
 assert.strictEqual(created, 2);
 const [a, b] = list.children;
 assert.strictEqual(a.dataset.key, 'a');
 a.children[1].textContent = 'Cancellation requested.';
 let replaced = 0;
 const replaceChildren = list.replaceChildren.bind(list);
 list.replaceChildren = (...rows) => { replaced++; replaceChildren(...rows); };
 render([{ id: 'a', name: 'A', state: 'cancelling' }, { id: 'b', name: 'B', state: 'queued' }]);
 assert.strictEqual(replaced, 0, 'an unchanged set and order leaves the list alone');
 assert.strictEqual(list.children[0], a, 'a kept key keeps its row element');
 assert.strictEqual(a.children[0].textContent, 'A · cancelling', 'and the row takes the new state in place');
 assert.strictEqual(a.children[1].textContent, 'Cancellation requested.', 'and keeps its sentence');
 render([{ id: 'c', name: 'C', state: 'new' }, { id: 'a', name: 'A', state: 'cancelling' }]);
 assert.strictEqual(created, 3, 'a new key gets one created row');
 assert.deepStrictEqual(Array.from(list.children).map((r) => r.dataset.key), ['c', 'a'], 'the order follows the items and the gone key left');
 assert.strictEqual(list.children[1], a);
 assert(!Array.from(list.children).includes(b));
 render([]);
 assert.strictEqual(list.children.length, 0);
}

// 11d. Focus survives a live reread: a row inserted ahead of a focused
//      button's row detaches nothing, so the button keeps focus; a reorder
//      that has to move the focused row puts focus back without scrolling;
//      Device and Activity place their rows with the same oaPlaceRows.
{
 const { fakeDocument } = require('./panel_fake_dom_test_helper.js');
 const doc = fakeDocument();
 const focusContext = { document: doc };
 vm.runInNewContext(script, focusContext);
 const list = doc.element('rows');
 const create = (item) => { const r = doc.createElement('div'); r.className = 'oa-row'; const b = doc.createElement('button'); b.textContent = item.name; r.append(b); return r; };
 const render = (names) => focusContext.oaKeyedRows(list, names.map((name) => ({ name })), (it) => it.name, create, () => {});
 render(['a', 'b']);
 const button = list.children[1].children[0];
 button.focus();
 assert.strictEqual(doc.activeElement, button);
 render(['new', 'a', 'b']);
 assert.deepStrictEqual(Array.from(list.children).map((r) => r.dataset.key), ['new', 'a', 'b']);
 assert.strictEqual(doc.activeElement, button, 'a row inserted ahead keeps the focused button focused');
 assert.strictEqual(button.focusCount, 1, 'without detaching it, so nothing had to focus it again');
 render(['b', 'new', 'a']);
 assert.deepStrictEqual(Array.from(list.children).map((r) => r.dataset.key), ['b', 'new', 'a']);
 assert.strictEqual(doc.activeElement, button, 'a reorder that moves the focused row gives focus back');
 assert.strictEqual(button.focusOptions.preventScroll, true, 'without scrolling');
 assert.strictEqual(button.focusCount, 2);
 render(['new', 'a']);
 assert.strictEqual(doc.activeElement, doc.body, 'a gone row takes its focus with it');
 // oaPlaceRows with cached rows, as Device and Activity call it: kept rows
 // stay attached, gone children leave, new rows go in their place.
 const box = doc.element('placed');
 const one = create({ name: 'one' }), two = create({ name: 'two' }), three = create({ name: 'three' });
 focusContext.oaPlaceRows(box, [one, two]);
 two.children[0].focus();
 focusContext.oaPlaceRows(box, [three, one, two]);
 assert.deepStrictEqual(Array.from(box.children).map((r) => r.children[0].textContent), ['three', 'one', 'two']);
 assert.strictEqual(doc.activeElement, two.children[0]);
 focusContext.oaPlaceRows(box, [two]);
 assert.deepStrictEqual(Array.from(box.children).map((r) => r.children[0].textContent), ['two']);
 assert.strictEqual(doc.activeElement, two.children[0]);
}

// 11e. A permission refusal names the action by the shared plain-phrase
//      table (oaActionPlainTable, actionPlainScript), else by its words,
//      never by its id; the rendered refusal passes the Panel rules, with
//      the id kept behind Technical details.
{
 const { fakeDocument } = require('./panel_fake_dom_test_helper.js');
 const { assertPanelRules } = require('./static/panel_rules.js');
 const doc = fakeDocument();
 const refusal = { document: doc, oaActionPlainTable: { 'abstraction.router/inventory.read': 'router servers', 'abstraction.inference/key.issue': 'inference key issue' } };
 vm.runInNewContext(script, refusal);
 const status = doc.element('models-status');
 refusal.oaShowError('models-status', new Error('forbidden: abstraction.router/inventory.read not permitted'));
 assert.strictEqual(status.textContent.split('Technical details')[0], PERMIT + ' It needs the “router servers” permission.');
 assertPanelRules(doc.body, 'refusal');
 // The same refusal worded with the id fails the rules: the test can see it.
 const { panelRuleViolations } = require('./static/panel_rules.js');
 const old = doc.createElement('p'); old.textContent = PERMIT + ' It needs the rule abstraction.router/inventory.read.';
 assert(panelRuleViolations(old, { rules: ['raw-value'] }).length > 0, 'an id in the sentence is a raw-value violation');
 assert.strictEqual(refusal.oaActionPhrase('key.issue'), 'inference key issue', 'a bare tail reads its one table action');
 assert.strictEqual(refusal.oaActionPhrase('abstraction.storage/content.observe'), 'storage content observe', 'an action the table lacks reads its words');
 assert.strictEqual(refusal.oaActionPhrase('abstraction.inference/selection.manage_for'), 'inference selection manage for');
}

// 12. oaProgramChooser lists the applications registered with the runtime
//     (/apps/registrations), by registered name, plus the programs a page
//     names from its saved rules; never running processes. Fed from the
//     graphics card's holders it offered csrss and dwm
//     (research/panel-redesign/SPEC.md §1, §7).
(async () => {
 const requests = [];
 const chooserFields = new Map();
 const chooserNode = () => Object.assign(node(), { value: '', hidden: false, title: '' });
 const get = (id) => { if (!chooserFields.has(id)) chooserFields.set(id, chooserNode()); return chooserFields.get(id) };
 const replies = {
  '/apps/registrations?source=applications&wait_ms=0': { outcome: 'page', entries: [
   { program: 'C:\\Program Files\\Editor\\editor.exe', name: 'Editor', role: 'application' },
   { program: 'C:\\Tools\\agent.exe', name: '', role: 'application' },
  ] },
  '/card/table': { resources: [{ resource: 'card:0', holders: [{ program: 'C:\\Windows\\System32\\csrss.exe' }, { program: 'C:\\Windows\\System32\\dwm.exe' }] }] },
 };
 const chooserContext = {
  document: { getElementById: get, createElement: chooserNode }, console, URLSearchParams, location: { search: '?k=chooser-key' },
  fetch: async (path, opts) => { requests.push([path, opts.headers['X-Panel-Key']]); return { ok: true, json: async () => replies[path] } },
 };
 vm.runInNewContext(script, chooserContext);
 const chooser = chooserContext.oaProgramChooser('pick', 'pick-other', () => ['/usr/bin/ruled']);
 await chooser.ready;
 const options = get('pick').children;
 assert.deepStrictEqual(requests, [['/apps/registrations?source=applications&wait_ms=0', 'chooser-key']], 'the chooser reads the registry with the Panel key, and nothing else');
 assert.deepStrictEqual(options.map((o) => o.textContent), ['agent', 'Editor', 'ruled', 'Other…'], 'registered applications by name, saved-rule programs by file name without .exe, sorted, Other… last');
 assert.strictEqual(options[1].title, 'C:\\Program Files\\Editor\\editor.exe', 'the full path stays on the option title');
 assert(!options.some((o) => /csrss|dwm/i.test(o.value + o.textContent)), 'a running system process is never offered');
 assert.strictEqual(get('pick').value, '__other__');
 get('pick').value = options[1].value; get('pick').onchange();
 assert.strictEqual(get('pick-other').hidden, true);
 assert.strictEqual(get('pick-other').value, 'C:\\Program Files\\Editor\\editor.exe');

 // A registry that cannot be read leaves Other… and the page's own
 // programs; a later chooser reads it again.
 const failing = { ...chooserContext, fetch: async () => ({ ok: false, status: 503, json: async () => ({}) }) };
 vm.runInNewContext(script, failing);
 const fallback = failing.oaProgramChooser('pick2', 'pick2-other');
 await fallback.ready;
 assert.deepStrictEqual(Array.from(get('pick2').children).map((o) => o.value), ['__other__']);
 console.log('PASS panel plain-language error control');
})().catch((e) => { console.error(e); process.exit(1) });

for(const empty of ['', '  \n ', {}, []]){oaShowResult('status-empty','Saved.',empty);assert.strictEqual(element('status-empty').children.length,0,'empty result has no Technical details');}
oaShowError('status-empty','  ');assert.strictEqual(element('status-empty').children.length,0,'blank error has no Technical details');
