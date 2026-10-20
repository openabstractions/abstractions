// Run with node monitor/selection_page_test.js; no browser or service is started.
// Covers the Models page's choice group (selection_page.go): its markup holds
// choosers only (no textarea, no typed program or policy name), the fallback
// order sits behind an expander, and the group reads as one sentence when
// the registered apps cannot be read or none are registered. The choosers'
// reads and writes are driven in inference_page_test.js.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/selection_page.go', 'utf8');
const inference = fs.readFileSync(__dirname + '/inference_page.go', 'utf8');
const markup = go.slice(go.indexOf('var selectionPolicySection'), go.indexOf('const selectionPolicyScript'));
assert(inference.includes('selectionPolicySection +') && inference.includes('inferenceScript + selectionPolicyScript'), 'the Models page carries the choice group and runs its script after its own');
assert(!fs.readFileSync(__dirname + '/registry_page.go', 'utf8').includes('selection-section'), 'the Providers page does not carry it');
assert(!/<textarea|<input/.test(markup), 'the choice group has no text field');
assert(!/selection-program|selection-name|selection-candidates|This Panel/.test(go), 'no typed program path, policy name or candidate list');
assert(/<div id="selection-fallback" hidden>` \+ oaExpander\("Fallback order"/.test(markup), 'the fallback order sits behind an expander, shown once an app is listed');
assert(markup.includes('<option value="chat" selected>Chat</option>'), 'chat is the use on arrival');
assert(go.includes("policyNames={chat:'oa-chat-default',image:'oa-image-default'}"), 'the policy names apps read stay the same');

const shell = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const plain = shell.slice(shell.indexOf('const plainScript = `') + 21).split('<script>')[1].split('</script>')[0];
function goScript(source, name) { const marker = 'const ' + name + ' = `<script>', start = source.indexOf(marker); return source.slice(start + marker.length, source.indexOf('</script>`', start)) }
function node(tag) {
 return {tagName: String(tag || '').toUpperCase(), value: '', textContent: '', className: '', hidden: false, disabled: false, dataset: {}, attrs: {}, children: [],
  append(...v) { this.children.push(...v) }, replaceChildren(...v) { this.children = v }, setAttribute(k, v) { this.attrs[k] = v }, removeAttribute(k) { delete this.attrs[k] }};
}
function run(registrations) {
 const fields = new Map();
 const element = id => { if (!fields.has(id)) fields.set(id, node('div')); return fields.get(id) };
 element('models-profile').value = 'chat';
 const reads = [];
 const context = {URLSearchParams, console, document: {getElementById: element, createElement: node}, location: {search: '?k=test'},
  fetch: async (path, opts) => {
   assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
   if (path.startsWith('/apps/registrations?')) return registrations();
   if (path === '/registry/selection') { reads.push(JSON.parse(opts.body)); return {ok: true, json: async () => ({read: {Outcome: 'unknown'}})} }
   if (path.startsWith('/inference/models')) return {ok: true, json: async () => ({Offers: []})};
   return {ok: true, json: async () => ({available: false, Outcome: 'page', Entries: [], AtEnd: true, Hosts: []})};
  }};
 vm.runInNewContext(plain, context);
 vm.runInNewContext(goScript(inference, 'inferenceScript'), context);
 vm.runInNewContext(goScript(go, 'selectionPolicyScript'), context);
 return {element, reads};
}
const settle = async () => { for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 0)) };
(async () => {
 let page = run(() => ({ok: false, text: async () => 'forbidden'}));
 await settle();
 assert.strictEqual(page.element('selection-status').textContent, 'The runtime did not permit this Panel to do this; nothing was changed.');
 assert.strictEqual(page.reads.length, 0, 'no policy is read without an app');
 page = run(() => ({ok: true, json: async () => ({outcome: 'page', entries: []})}));
 await settle();
 assert.strictEqual(page.element('selection-apps').children[0].textContent, 'No apps are registered yet.');
 assert.strictEqual(page.element('selection-apps').children[0].className, 'oa-empty');
 assert.strictEqual(page.element('selection-fallback').hidden, true, 'no fallback order without an app');
 console.log('selection_page_test: ok');
})().catch((error) => { console.error(error); process.exit(1); });
