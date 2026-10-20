// Run with node monitor/registry_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/registry_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
// The first script is the Providers page's own; the moved model server,
// gateway and key sections (serversScript) run in a second one, covered by
// inference_page_test.js.
assert(go.includes('</script><script>` + serversScript + `</script>'), 'Providers carries the model server, gateway and key sections');
// Owner's Panel rules (2026-10-07): one short caption per section, the
// page caption "Where models run.", and the forms are choosers behind an
// expander (research/panel-redesign/SPEC.md §4, S8).
{
 const markup = go.slice(go.indexOf('var registryPage = '), go.indexOf('<script>'));
 assert(markup.includes('oaPageHead("registry", "Providers", "Where models run.")'), 'the page caption');
 assert(!markup.includes('provider-register'), 'the typed managed-provider form is gone');
 assert(!/<input(?![^>]*(\bhidden\b|type="checkbox"))/.test(markup), 'every text input waits behind Other…');
 assert(!/<p[^>]*>[^<]{120,}<\/p>/.test(markup), 'each Providers section carries one short caption');
 assert(!markup.includes('<code>') && !markup.includes('<h3>'), 'no code ids or sub-headings as page text');
 for (const form of ['host-add', 'key-issue']) {
  const at = markup.indexOf('<form id="' + form + '"');
  assert(at > 0 && markup.lastIndexOf('oaExpander(', at) > markup.lastIndexOf('</form>', at), form + ' sits behind an expander');
 }
}
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
 return {value:'', textContent:'', children:[], elements:{}, hidden:false, checked:false, replaceChildren(...v){this.children=[...v]}, append(...v){this.children.push(...v)}};
}
function element(id) {
 if (!fields.has(id)) {
  const n=node();
  if(id==='provider-register') for(const name of ['name','program','endpoint','arguments','contracts','guarantees','resources','models','activation','authorize_startup']) n.elements[name]=node();
  fields.set(id,n);
 }
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
 {hosts:{Outcome:'forbidden', Hosts:[]}, providers:{Outcome:'page', Declarations:[]}, models:{Models:[]}, models_error:'router inventory not permitted', remote:[], remote_error:'router not resolved: forbidden'},
];
views[0].models={Models:[
 {Family:'qwen/tiny',HeldIn:['lmstudio'],Names:[{Name:'qwen/tiny',Host:'ollama',Servable:true,Resident:false,Hosted:false,HeldIn:['lmstudio'],Profiles:['chat']}]},
 {Family:'diffusion_models/wan.safetensors',HeldIn:['comfyui'],Names:[{Name:'diffusion_models/wan.safetensors',Host:'',Servable:false,Resident:false,HeldIn:['comfyui']}],Components:[{Role:'projector',Name:'<img src=x>',Store:'comfyui'}]},
 {Family:'engine-cache/model.gguf',HeldIn:['engine-cache'],Names:[{Name:'engine-cache/model.gguf',Host:'special-engine',Servable:false,Resident:true,HeldIn:['engine-cache']}]},
]};
views[0].models_error='';
// read() now also runs once, unclicked, as soon as the page's own script
// loads (task 2026-09-23, fifteenth first-time visitor, finding 2), so the
// queue needs a view for that arrival read ahead of the two this test's own
// clicks still expect; a deep clone keeps the arrival read's response
// independent of the one the first click then consumes.
views.unshift(JSON.parse(JSON.stringify(views[0])));
views.splice(2,0,JSON.parse(JSON.stringify(views[0])));
let registrationPayload, lastView;
const context = {URLSearchParams, document:{getElementById:element,createElement:t=>Object.assign(node(),{tagName:String(t).toUpperCase()})}, location:{search:'?k=test'}, console,confirm:()=>true,
 fetch:async(path,opts)=>{
  if(path==='/registry/providers') {assert.strictEqual(opts.method,'POST');registrationPayload=JSON.parse(opts.body);return {ok:true,json:async()=>({Outcome:'applied',Revision:'providers-v2:next'})}}
  if(path==='/registry/selection') {const payload=JSON.parse(opts.body);selectionCalls.push(payload);if(payload.edit==='read')return {ok:true,json:async()=>({read:selectionExists?{Outcome:'found',Revision:selectionRevision,Policy:{Name:payload.name,Candidates:[{Model:'qwen/tiny',Placement:'local',Credential:'',Provider:'native-runtime'}]}}:{Outcome:'unknown',Revision:'',Policy:null}})};if(payload.edit==='put'){selectionExists=true;selectionRevision='selection-v1:8';return {ok:true,json:async()=>({change:{Outcome:'applied',Revision:selectionRevision,Reason:''}})}}selectionExists=false;return {ok:true,json:async()=>({change:{Outcome:'applied',Revision:'selection-v1:9',Reason:''}})}}
  assert.strictEqual(path, '/registry/view');
  assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
  return {ok:true, json:async()=>(lastView=views.shift())};
 }};
context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
vm.runInNewContext(plainScript, context);
vm.runInNewContext(script, context);
assert(!go.includes('data-grant-rules'), 'the Providers page asks for no rule for this Panel');
const cells = row => row.children.map(td => td.textContent);
(async () => {
 const settle = () => new Promise(r => setTimeout(r, 0));
 // read() runs once, unclicked, on arrival (finding 2 above); settle it
 // before the test's own clicks, the same way inference_page_test.js
 // settles its own three eager loads.
 await settle(); await settle(); await settle();
 context.read(); await settle(); await settle();
 const modelRows=element('models').children;
 assert.strictEqual(modelRows.length,3,'served, stored-only and unservable loaded families are shown');
 assert.deepStrictEqual(cells(modelRows[0]), ['qwen/tiny','qwen/tiny — ollama (local, available)','Available from provider','lmstudio','none']);
 assert.deepStrictEqual(cells(modelRows[1]), ['diffusion_models/wan.safetensors','diffusion_models/wan.safetensors — no model server (stored, not served)','Stored only; no server can answer','comfyui','projector (<img src=x>) in comfyui']);
 assert.deepStrictEqual(cells(modelRows[2]), ['engine-cache/model.gguf','engine-cache/model.gguf — special-engine (local, not served, provider reports loaded)','Stored; a provider reports loaded but cannot serve','engine-cache','none']);
 assert.strictEqual(modelRows[1].children[4].children.length,0,'untrusted model/component names render as text, not markup');
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
 assert.deepStrictEqual(cells(providerRows[0]), ['local-stores','managed process','on_demand','storage inventory source','inventoryd-v1','inventoryd','','store:ollama, store:huggingface','refused','not yet','2','openabstractions','']);
 assert.strictEqual(providerRows[0].children[8].title, 'program:server program mismatch', 'the readiness reason sits on the cell title');
 assert.strictEqual(providerRows[0].children[3].title, 'abstraction.storage/inventory-source@1', 'the contract id sits on the cell title, never the visible text');
 assert.strictEqual(providerRows[0].children[12].children[0].textContent,'Withdraw','provider registration exposes a withdrawal action');
 assert.deepStrictEqual(cells(providerRows[1]), ['lab','remote runtime','remote','inference chat, router','tls://lab.example:8443 (lab.example)','none','','profile:chat','ready','inference chat ready, router ready','0','openabstractions','']);
 assert.equal(providerRows[0].children[1].title, 'provider', 'the wire role sits on the Role cell\'s own title');
 assert.deepStrictEqual(cells(providerRows[2]), ['ollama-engine','model server','attach','none','none','none','ollama http://127.0.0.1:11500','profile:chat','ready','not yet','0','installation','']);
 assert.deepStrictEqual(cells(element('remote').children[0]), ['lab/openrouter','lab','openai-compatible','operator','chat','Up']);
 assert.strictEqual(providerRows[0].children[5].title,'/opt/inventoryd','the program path sits on the cell title');
 assert.strictEqual(providerRows[0].children[11].title,'/opt/oa/openabstractions','the registering program path sits on the cell title');
 providerRows[0].children[12].children[0].onclick(); await settle(); await settle();
 assert.deepStrictEqual(registrationPayload,{edit:'withdraw',revision:'providers-v2:r',name:'local-stores'});
 assert.strictEqual(element('provider-edit-status').textContent,'Provider registration updated.');
 // Models (a router inventory refusal) and Remote runtimes (a raw backend
 // string that happens to share the word "forbidden") refuse the same one
 // click; Providers succeeds (an empty page). This lands as one line naming
 // the two refused tables, not three differently worded denials scattered
 // across three lines, with the raw remote text kept behind a disclosure
 // rather than shown outright.
 context.read(); await settle(); await settle();
 assert.strictEqual(element('models-status').textContent, '', 'the refused model table defers to the one summary line');
 assert.strictEqual(element('providers-status').textContent, 'No providers registered.', 'a table that succeeded is unaffected by the others\' refusal');
 assert.strictEqual(element('remote-status').textContent, '');
 assert.strictEqual(element('registry-status').textContent, 'Models, Remote runtimes could not be read. The runtime did not permit this Panel to do this; nothing was changed.');
 const denialDetails = element('registry-status').children[0];
 assert.strictEqual(denialDetails.className, 'oa-error-details');
 assert.strictEqual(denialDetails.children[1].textContent, 'Models: router inventory not permitted\nRemote runtimes: router not resolved: forbidden', 'the raw per-table reasons stay available behind the disclosure');
 // The rendered tables show no path, SID or hash as text (static/panel_rules.js).
 const { assertPanelRules } = require('./static/panel_rules.js');
 assertPanelRules([element('providers'), element('models'), element('remote')], 'Providers', { rules: ['raw-value', 'class'], allowClasses: require('./panel_fake_dom_test_helper.js').allowlistedClasses('registry') });
 // A sentence a click wrote survives the page's next reading of the same
 // state: rows, status lines and focus stay in place.
 element('provider-edit-status').textContent = 'Provider registration updated.';
 views.push(JSON.parse(JSON.stringify(lastView)));
 await context.read(); await settle();
 assert.strictEqual(element('provider-edit-status').textContent, 'Provider registration updated.', 'the sentence survives a reread');
 console.log('PASS registry page');
})().catch(e => { console.error(e); process.exit(1); });
