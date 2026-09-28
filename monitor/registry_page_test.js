// Run with node monitor/registry_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/registry_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
// plainScript (monitor/panel_shell.go) is embedded ahead of every page
// fragment in the real document; this isolated test loads it into the same
// vm context first, since registry_page.go's own script calls oaShowError
// and oaPlain.
const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
const plainMarker = 'const plainScript = `';
const plainStart = shellGo.indexOf(plainMarker) + plainMarker.length;
assert(plainStart > plainMarker.length - 1, 'plainScript constant not found in panel_shell.go');
const plainEnd = shellGo.indexOf('`', plainStart);
const plainScript = shellGo.slice(plainStart, plainEnd).split('<script>')[1].split('</script>')[0];
const fields = new Map();
function node() {
 return {value:'', textContent:'', children:[], replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}};
}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
const views = [
 {hosts:{Outcome:'page', Revision:'hosts-v1:r', Hosts:[
   {Entry:{Name:'ollama',Hosted:false,Kind:'ollama',Base:'http://127.0.0.1:11500',Profiles:['chat','embed'],DeclaredBy:'ollama'},Up:true,Why:''},
   {Entry:{Name:'openrouter',Hosted:true,Kind:'openai-compatible',Base:'https://openrouter.ai/api/v1',Profiles:['chat'],DeclaredBy:'operator'},Up:false,Why:'credential:not_found'}]},
  providers:{Outcome:'page', Revision:'providers-v2:r', Declarations:[
   {Declaration:{Name:'local-stores',Role:'provider',Activation:'on_demand',Contracts:['abstraction.storage/inventory-source@1'],Endpoint:'inventoryd-v1',Program:'/opt/inventoryd',Resources:['store:ollama','store:huggingface']},
    DeclaredBy:'/opt/oa/openabstractions',Role:'provider',Readiness:'refused',Why:'program:server program mismatch',Restarts:2,Described:[],Accepted:null},
   {Declaration:{Name:'lab',Role:'remote',Activation:'remote',Contracts:['abstraction.inference/chat@1','abstraction.router/router@1'],Endpoint:'tls://lab.example:8443',Program:'',Resources:['profile:chat'],Remote:{ServerName:'lab.example'}},
    DeclaredBy:'/opt/oa/openabstractions',Role:'remote',Readiness:'ready',Why:'',Restarts:0,Described:[{Contract:'abstraction.inference/chat@1',Readiness:'ready'},{Contract:'abstraction.router/router@1',Readiness:'ready'}],Accepted:null},
   {Declaration:{Name:'ollama-engine',Role:'host',Activation:'attach',Contracts:[],Endpoint:'',Program:'',Resources:['profile:chat'],Host:{Base:'http://127.0.0.1:11500',Kind:'ollama'}},
    DeclaredBy:'installation',Role:'host',Readiness:'ready',Why:'',Restarts:0,Described:[],Accepted:null}]},
  remote:[{Host:'lab/openrouter',Domain:'lab',Hosted:true,Wire:'openai-compatible',DeclaredBy:'operator',Profiles:['chat'],Up:true,Why:''}]},
 {hosts:{Outcome:'forbidden', Hosts:[]}, providers:{Outcome:'page', Declarations:[]}, remote:[], remote_error:'router not resolved: forbidden'},
];
// read() now also runs once, unclicked, as soon as the page's own script
// loads (task 2026-09-23, fifteenth first-time visitor, finding 2), so the
// queue needs a view for that arrival read ahead of the two this test's own
// clicks still expect; a deep clone keeps the arrival read's response
// independent of the one the first click then consumes.
views.unshift(JSON.parse(JSON.stringify(views[0])));
const context = {URLSearchParams, document:{getElementById:element,createElement:node}, location:{search:'?k=test'}, console,
 fetch:async(path,opts)=>{
  assert.strictEqual(path, '/registry/view');
  assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
  return {ok:true, json:async()=>views.shift()};
 }};
context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
vm.runInNewContext(plainScript, context);
vm.runInNewContext(script, context);
const cells = row => row.children.map(td => td.textContent);
(async () => {
 const settle = () => new Promise(r => setTimeout(r, 0));
 // read() runs once, unclicked, on arrival (finding 2 above); settle it
 // before the test's own clicks, the same way inference_page_test.js
 // settles its own three eager loads.
 await settle(); await settle(); await settle();
 element('registry-read').onclick(); await settle(); await settle();
 // task 2026-09-23, eleventh first-time visitor, finding 5: the State
 // column used to read a raw connection error as visible text; it now
 // reads oaHostState's own plain word ("Up" or "Not reachable"), the raw
 // reason kept on that cell's own title, shared with Inference's own
 // table through the same function.
 assert.deepStrictEqual(cells(element('hosts').children[0]), ['ollama','local: ollama','http://127.0.0.1:11500','ollama','chat, embed','Up']);
 assert.deepStrictEqual(cells(element('hosts').children[1]), ['openrouter','hosted: openai-compatible','https://openrouter.ai/api/v1','operator','chat','Not reachable']);
 assert.equal(element('hosts').children[1].children[5].title, 'credential:not_found', 'the raw reason sits on the State cell\'s own title');
 // Accepted is null on all three declarations, so its column is dropped
 // (task 2026-09-23 finding 4, the same approach card_page.go already takes
 // for the Graphics card's holder columns); Activation and Resources carry a
 // value on every row and stay as plain values, while Contracts, Endpoint,
 // Program and Described each carry a value on only some rows and stay, with
 // a stated absence ("none", "not yet") in the rows that have none.
 // task 2026-09-23, eighth first-time visitor, finding 6: the table itself
 // now sits inside a div.oa-table wrapper, the element that scrolls
 // sideways if the table cannot fit, never the page.
 const providersWrap = element('providers').children[0];
 assert.strictEqual(providersWrap.className, 'oa-table');
 const providersTable = providersWrap.children[0];
 const providersHead = providersTable.children[0].innerHTML;
 assert(!providersHead.includes('>Accepted<'), 'the all-empty Accepted column is not rendered: ' + providersHead);
 for (const label of ['Name', 'Role', 'Activation', 'Services', 'Endpoint', 'Program', 'Engine', 'Resources', 'Readiness', 'Described', 'Restarts', 'Registered by']) {
  assert(providersHead.includes('>' + label + '<'), label + ' column missing: ' + providersHead);
 }
 const providerRows = providersTable.children[1].children;
 assert.deepStrictEqual(cells(providerRows[0]), ['local-stores','managed process','on_demand','abstraction.storage/inventory-source@1','inventoryd-v1','/opt/inventoryd','','store:ollama, store:huggingface','refused: program:server program mismatch','not yet','2','/opt/oa/openabstractions']);
 assert.deepStrictEqual(cells(providerRows[1]), ['lab','remote runtime','remote','abstraction.inference/chat@1, abstraction.router/router@1','tls://lab.example:8443 (lab.example)','none','','profile:chat','ready','abstraction.inference/chat@1 ready, abstraction.router/router@1 ready','0','/opt/oa/openabstractions']);
 assert.equal(providerRows[0].children[1].title, 'provider', 'the wire role sits on the Role cell\'s own title');
 assert.deepStrictEqual(cells(providerRows[2]), ['ollama-engine','model server','attach','none','none','none','ollama http://127.0.0.1:11500','profile:chat','ready','not yet','0','installation']);
 assert.deepStrictEqual(cells(element('remote').children[0]), ['lab/openrouter','lab','openai-compatible','operator','chat','Up']);
 // Hosts (typed Outcome "forbidden") and Remote runtimes (a raw backend
 // string that happens to share the word "forbidden") refuse the same one
 // click; Providers succeeds (an empty page). This lands as one line naming
 // the two refused tables, not three differently worded denials scattered
 // across three lines, with the raw remote text kept behind a disclosure
 // rather than shown outright.
 element('registry-read').onclick(); await settle(); await settle();
 assert.strictEqual(element('hosts').children.length, 0);
 assert.strictEqual(element('hosts-status').textContent, '', 'the refused table\'s own status defers to the one summary line');
 assert.strictEqual(element('providers-status').textContent, 'No providers registered.', 'a table that succeeded is unaffected by the others\' refusal');
 assert.strictEqual(element('remote-status').textContent, '');
 assert.strictEqual(element('registry-status').textContent, 'Model servers, Remote runtimes could not be read. The runtime did not permit this Panel to do this; nothing was changed.');
 const denialDetails = element('registry-status').children[0];
 assert.strictEqual(denialDetails.className, 'oa-error-details');
 assert.strictEqual(denialDetails.children[1].textContent, 'Model servers: forbidden\nRemote runtimes: router not resolved: forbidden', 'the raw per-table reasons stay available behind the disclosure');
 console.log('PASS registry page');
})().catch(e => { console.error(e); process.exit(1); });
