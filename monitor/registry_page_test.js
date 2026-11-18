// Run with node monitor/registry_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/registry_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
// The first script is the Providers page's own; the moved model server,
// gateway and key sections (serversScript) run in a second one, covered by
// inference_page_test.js.
assert(go.includes('</script><script>` + serversScript + `</script>'), 'Providers carries the model server, gateway and key sections');
// Owner's Panel rules (2026-10-07): one short caption per section, the
// page caption "Model servers and background services.", and the forms are choosers behind an
// expander (research/panel-redesign/SPEC.md §4, S8).
{
 const markup = go.slice(go.indexOf('var registryPage = '), go.indexOf('<script>'));
 assert(markup.includes('oaPageHead("registry", "Services", "Model servers and background services.")'), 'the page caption');
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
 {hosts:{Outcome:'forbidden', Hosts:[]}, providers:{Outcome:'page', Declarations:[]}, remote:[], remote_error:'router not resolved: forbidden', paired:true},
];
// The model inventory is the Models page's On this computer
// (models_library.go); Providers keeps servers, providers and remotes.
assert(!go.includes('Model inventory') && !go.includes('id="models"'), 'one place for models: the Models page');
// read() now also runs once, unclicked, as soon as the page's own script
// loads (task 2026-09-23, fifteenth first-time visitor, finding 2), so the
// queue needs a view for that arrival read ahead of the two this test's own
// clicks still expect; a deep clone keeps the arrival read's response
// independent of the one the first click then consumes.
views.unshift(JSON.parse(JSON.stringify(views[0])));
views.splice(2,0,JSON.parse(JSON.stringify(views[0])));
let registrationPayload, lastView;
// providerReply is what /registry/providers answers: a JSON change, or
// {status, body} for a failed HTTP reply.
let providerReply = {Outcome:'applied',Revision:'providers-v2:next'};
const context = {URLSearchParams, document:{getElementById:element,createElement:t=>Object.assign(node(),{tagName:String(t).toUpperCase()})}, location:{search:'?k=test'}, console,confirm:()=>{throw Error('the Registry page opened a confirm() dialog')},
 fetch:async(path,opts)=>{
  if(path==='/registry/providers') {assert.strictEqual(opts.method,'POST');registrationPayload=JSON.parse(opts.body);const reply=providerReply;if(reply.status)return {ok:false,status:reply.status,text:async()=>reply.body};return {ok:true,status:200,text:async()=>JSON.stringify(reply),json:async()=>reply}}
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
// Drive both actual page scripts: each successful inventory replaces only
// its own chooser source, including when the two reads finish separately.
async function chooserSnapshots() {
 const fields=new Map();
 const get=id=>{if(!fields.has(id))fields.set(id,{value:'',textContent:'',children:[],hidden:false,checked:false,listeners:{},replaceChildren(...v){this.children=[...v]},append(...v){this.children.push(...v)},focus(){},addEventListener(t,f){this.listeners[t]=f}});return fields.get(id)};
 const make=tag=>Object.assign(get(Symbol()),{tagName:tag});
 assert(go.includes('<input id="host-name" aria-label="Server name" data-oa-other hidden>'));
 get('host-name').hidden=true;get('host-base').hidden=true;
 let added;
 let hostReply={Outcome:'page',Revision:'h1',Hosts:[{Entry:{Name:'kept',Kind:'vllm',Base:'http://127.0.0.1:11501',Profiles:['chat']},Up:false,Why:'paired_name_conflict'}]};
 const provider=()=>({Declaration:{Name:'transient',Host:{Kind:'sglang',Base:'http://127.0.0.1:11502'}},Role:'host',Readiness:'unreachable',Described:[]});
 let view={providers:{Outcome:'page',Revision:'p1',Declarations:[provider()]},models:{Models:[]},remote:[]};
 // Hosted choices come from GET /connect (monitor/hosted_presets.json).
 const connectReply={presets:[{id:'deepseek',api:'openai-compatible',base:'https://api.deepseek.com/v1'},{id:'openrouter',api:'openai-compatible',base:'https://openrouter.ai/api/v1'},{id:'github-download',kind:'download',api:'',base:'https://api.github.com'},{id:'openai-compatible',api:'openai-compatible',base:''}]};
 const c={URLSearchParams,console,location:{search:'?k=chooser'},document:{getElementById:get,createElement:make},oaReady(){},oaSetStatus(id,state,text){get(id).textContent=text;get(id).state=state},fetch:async(path,opts)=>{
  assert.strictEqual(opts.headers['X-Panel-Key'],'chooser');
  if(path==='/inference/hosts'&&opts.method==='POST'){added=JSON.parse(opts.body);return {ok:true,json:async()=>({Outcome:'invalid'})}}
  const reply=path==='/registry/view'?view:path==='/inference/hosts'?hostReply:path.startsWith('/credentials')?{Outcome:'page',Records:[]}:path.startsWith('/apps/registrations')?{outcome:'page',entries:[]}:path==='/inference/keys'?{Outcome:'page',Keys:[]}:path==='/connect'?connectReply:{Outcome:'page'};
  if(reply instanceof Error)return {ok:false,text:async()=>reply.message};
  return {ok:true,json:async()=>reply};
 }};c.window=c;
 vm.runInNewContext(plainScript,c);vm.runInNewContext(script,c);
 const marker='const serversScript = `',start=go.indexOf(marker)+marker.length;
 vm.runInNewContext(go.slice(start,go.indexOf('`',start)),c);
 // With no local kind known yet the form opens on a hosted preset, never
 // on an empty Other… field (Panel rules: choosers and defaults).
 assert.strictEqual(get('host-wire').value,'openai-compatible','no local kind known: a hosted API');
 const settle=()=>new Promise(r=>setTimeout(r,0));
 await settle();await settle();
 assert(get('hosts').children[0].parts.c2.textContent.includes('Name clash with a paired computer'),'the arriving host reading exposes the clash without a click');
 hostReply={...hostReply,Hosts:hostReply.Hosts.map(h=>({...h,Why:'unreachable'}))};await c.serversRefresh.hosts();
 assert(!get('hosts').children[0].parts.c2.textContent.includes('Name clash'),'a new reading clears the resolved clash');
 const names=()=>get('host-name-select').children.map(n=>n.value);
 const bases=()=>get('host-base-select').children.map(n=>n.value);
 assert(names().includes('sglang')&&names().includes('vllm'),'both current sources supply choices, including an explicitly kept unreachable host');
 // Once the runtime reports local kinds, an untouched form moves to the
 // first of them at its own address.
 assert.strictEqual(get('host-wire').value,'');
 assert.strictEqual(get('host-base').value,{sglang:'http://127.0.0.1:11502',vllm:'http://127.0.0.1:11501'}[get('host-name-select').value],'a reported local kind at its own address');
 // An empty or invalid submit names what is missing next to the button and
 // sends nothing.
 get('host-name-select').value='__other__';get('host-name-select').onchange();
 added=undefined;get('host-add').onsubmit({preventDefault(){}});await settle();
 assert.strictEqual(added,undefined,'an empty name reaches no runtime');
 assert.strictEqual(get('host-add-status').textContent,'Choose a server or type its name.');
 get('host-name').value='manual';get('host-base-select').value='__other__';get('host-base-select').onchange();get('host-base').value='http://10.0.0.5:11434';
 get('host-add').onsubmit({preventDefault(){}});await settle();
 assert.strictEqual(added,undefined,'plain http to another computer is refused before sending');
 assert(get('host-add-status').textContent.includes('https://'));
 get('host-base-select').value='http://127.0.0.1:11434';get('host-base-select').onchange();
 get('host-add').onsubmit({preventDefault(){}});await settle();
 assert.strictEqual(added.host.Name,'manual');assert.strictEqual(added.host.Kind,'manual');
 assert.strictEqual(added.host.Base,'http://127.0.0.1:11434','actual submit uses the displayed address');
 assert.strictEqual(added.revision,'h1','actual submit keeps its authoritative revision');
 assert(get('host-add-status').textContent,'a refused add writes its sentence next to the button');
 get('host-name-select').value='vllm';get('host-name-select').onchange();
 assert.strictEqual(get('host-name').value,'vllm');assert.strictEqual(get('host-name').hidden,true);
 get('host-add').onsubmit({preventDefault(){}});await settle();
 assert.strictEqual(added.host.Name,'vllm');assert.strictEqual(added.host.Base,'http://127.0.0.1:11501','selected kind and submitted fields stay coherent');
 // A hosted API offers its common service instead of an empty name.
 get('host-wire').value='openai-compatible';get('host-wire').onchange();
 assert.strictEqual(get('host-name-select').value,'deepseek','the first /connect preset');assert.strictEqual(get('host-base').value,'https://api.deepseek.com/v1');assert.strictEqual(get('host-name').hidden,true,'no empty Other… field');
 assert.deepStrictEqual(names(),['deepseek','openrouter','__other__'],'downloads and presets without an address are no server');
 get('host-wire').value='anthropic-messages';get('host-wire').onchange();
 assert(!names().includes('anthropic'),'no preset the JSON does not hold');
 get('host-wire').value='';get('host-wire').onchange();
 // Other text is explicit input and must survive every inventory refresh.
 get('host-name-select').value='__other__';get('host-name-select').onchange();get('host-name').value='my server';
 get('host-base-select').value='__other__';get('host-base-select').onchange();get('host-base').value='http://127.0.0.1:11503';
 view={...view,providers:{Outcome:'forbidden',Declarations:[]}};await c.read();
 assert(names().includes('sglang'),'a failed declaration read keeps its last successful source');
 assert(get('registry-status').textContent.includes('could not be read'),'a failed read retains the existing error presentation');
 hostReply=new Error('host read unavailable');await c.serversRefresh.hosts();
 assert(names().includes('vllm'),'a failed host read keeps its last successful source');
 assert(get('hosts-status').textContent,'host failure remains visible');
 view={...view,providers:{Outcome:'page',Revision:'p2',Declarations:[]}};await c.read();
 assert(!names().includes('sglang')&&!bases().includes('http://127.0.0.1:11502'),'successful provider removal drops its kind and address');
 assert(names().includes('vllm'),'provider removal keeps the separate host source');
 assert.strictEqual(get('host-name').value,'my server');assert.strictEqual(get('host-base').value,'http://127.0.0.1:11503');
 assert.strictEqual(get('host-name-select').value,'__other__');assert.strictEqual(get('host-base-select').value,'__other__');
 hostReply={Outcome:'page',Revision:'h2',Hosts:[]};await c.serversRefresh.hosts();
 assert(!names().includes('vllm')&&!bases().includes('http://127.0.0.1:11501'),'successful host removal drops its kind and address');
 assert.strictEqual(get('host-name').value,'my server');assert.strictEqual(get('host-base').value,'http://127.0.0.1:11503');
 // A departed automatic option must not silently choose a different server.
 view={...view,providers:{Outcome:'page',Revision:'p3',Declarations:[provider()]}};await c.read();
 get('host-name-select').value='sglang';get('host-name-select').onchange();
 view={...view,providers:{Outcome:'page',Revision:'p4',Declarations:[]}};await c.read();
 assert.strictEqual(get('host-name-select').value,'__other__');assert.strictEqual(get('host-name').value,'');
 assert.strictEqual(get('host-base-select').value,'__other__');assert.strictEqual(get('host-base').value,'');
}
(async () => {
 const settle = () => new Promise(r => setTimeout(r, 0));
 // read() runs once, unclicked, on arrival (finding 2 above); settle it
 // before the test's own clicks, the same way inference_page_test.js
 // settles its own three eager loads.
 await settle(); await settle(); await settle();
 context.read(); await settle(); await settle();
 // Registered providers are plain rows like Windows Settings lists: the
 // name, what it provides and its state; wire words (role, activation,
 // resources, registering program) stay off the page and raw values sit on
 // the label's title.
 const rowText = r => { const t = r.children[0]; return [t.children[0].textContent, t.children[1].textContent] };
 const providerRows = element('providers').children[0].children;
 assert.deepStrictEqual(rowText(providerRows[0]), ['local-stores', 'storage inventory source · Not started']);
 assert(providerRows[0].children[0].children[0].title.includes('abstraction.storage/inventory-source@1'), 'the contract id sits on the title, never the visible text');
 assert(providerRows[0].children[0].children[0].title.includes('program:server program mismatch'), 'the readiness reason sits on the title');
 assert.strictEqual(providerRows[0].children[1].children[0].textContent,'Remove','provider registration exposes a removal action');
 assert.deepStrictEqual(rowText(providerRows[1]), ['lab', 'Remote runtime: inference chat, router · Ready']);
 assert.deepStrictEqual(rowText(providerRows[2]), ['ollama-engine', 'Model server (ollama) · Ready']);
 assert.strictEqual(providerRows[2].children.length, 1, 'a row with no action has no empty action cell');
 for (const r of providerRows) for (const word of ['attach', 'profile:', 'operator', 'managed process']) assert(!rowText(r).join(' ').includes(word), word + ' is not page text');
 assert.deepStrictEqual(rowText(element('remote').children[0]), ['lab/openrouter', 'lab · hosted: openai-compatible · chat · Up']);
 // The first select writes what the second does and changes nothing.
 providerRows[0].children[1].children[0].onclick(); await settle(); await settle();
 assert.strictEqual(registrationPayload, undefined, 'the first select sends nothing');
 assert.strictEqual(element('provider-edit-status').textContent,'Select Remove again to remove local-stores.');
 // A select more than five seconds after the first only asks again.
 {
  const realNow = vm.runInContext('Date.now', context);
  context.__now = () => realNow() + 6000; vm.runInContext('Date.now=__now', context);
  providerRows[0].children[1].children[0].onclick(); await settle(); await settle();
  assert.strictEqual(registrationPayload, undefined, 'an armed select lapses after five seconds');
  context.__now = realNow; vm.runInContext('Date.now=__now', context);
 }
 providerRows[0].children[1].children[0].onclick(); await settle(); await settle();
 assert.deepStrictEqual(registrationPayload,{edit:'withdraw',revision:'providers-v2:r',name:'local-stores'});
 assert.strictEqual(element('provider-edit-status').textContent,'Service removed.');
 // A refused withdrawal shows a plain sentence and its reason behind
 // Technical details (owner report R20c, 2026-11-10: "This didn't work."
 // over "unknown: name").
 {
  const details = () => { const d = element('provider-edit-status').children[0]; return d && d.className === 'oa-error-details' ? d.children[1].textContent : '' };
  providerReply = {Outcome:'unknown',Revision:'providers-v2:r',Reason:'name'};
  views.unshift(JSON.parse(JSON.stringify(views[0])));
  providerRows[0].children[1].children[0].onclick(); providerRows[0].children[1].children[0].onclick(); await settle(); await settle();
  assert.strictEqual(element('provider-edit-status').textContent.split('Technical details')[0], "The service wasn't changed. This provider is no longer registered.");
  assert(!element('provider-edit-status').textContent.includes("didn't work"), 'a known refusal reads as its own sentence');
  assert(details().includes('outcome: unknown') && details().includes('reason: name') && details().includes('provider: local-stores'), 'the refusal keeps its outcome, reason and provider: ' + details());
  // A failed HTTP reply with no body still names its status and provider.
  providerReply = {status:503, body:''};
  views.unshift(JSON.parse(JSON.stringify(views[0])));
  providerRows[0].children[1].children[0].onclick(); providerRows[0].children[1].children[0].onclick(); await settle(); await settle();
  assert(element('provider-edit-status').textContent.startsWith("The service wasn't changed."), element('provider-edit-status').textContent);
  assert(details().includes('HTTP 503') && details().includes('withdraw local-stores'), 'empty reply details: ' + details());
  // A failed reply with a body keeps that body.
  providerReply = {status:503, body:'registry not resolved: unavailable\n'};
  views.unshift(JSON.parse(JSON.stringify(views[0])));
  providerRows[0].children[1].children[0].onclick(); providerRows[0].children[1].children[0].onclick(); await settle(); await settle();
  assert(details().includes('registry not resolved: unavailable'), details());
  providerReply = {Outcome:'applied',Revision:'providers-v2:next'};
 }
 // Remote runtimes (a raw backend string that happens to share the word
 // "forbidden") refuses; Providers succeeds (an empty page). The refusal
 // lands as one line naming the refused table, with the raw remote text
 // kept behind a disclosure rather than shown outright.
 context.read(); await settle(); await settle();
 assert.strictEqual(element('providers-status').textContent, 'No services yet.', 'a table that succeeded is unaffected by the others\' refusal');
 assert.strictEqual(element('remote-status').textContent, '');
 assert.strictEqual(element('registry-status').textContent, "Other PCs' providers could not be read. OpenAbstractions did not permit this Panel to do this; nothing was changed.");
 const denialDetails = element('registry-status').children[0];
 assert.strictEqual(denialDetails.className, 'oa-error-details');
 assert.strictEqual(denialDetails.children[1].textContent, "Other PCs' providers: router not resolved: forbidden",'the raw per-table reasons stay available behind the disclosure');
 // The rendered tables show no path, SID or hash as text (static/panel_rules.js).
 const { assertPanelRules } = require('./static/panel_rules.js');
 assertPanelRules([element('providers'), element('remote')], 'Providers', { rules: ['raw-value', 'class'], allowClasses: require('./panel_fake_dom_test_helper.js').allowlistedClasses('registry') });
 assert.strictEqual(element('remote-section').hidden, false, 'a paired runtime whose read fails keeps its section and error');
 // The owner's registrations (R20c): rows read by the titles the server
 // derives (providerDisplay), the raw name stays on the row's details, and
 // without pairing Other PCs' providers hides with no error.
 {
  const decl=(Name,Contracts)=>({Declaration:{Name,Role:'provider',Activation:'on_demand',Contracts,Endpoint:'e',Program:'/opt/p',Resources:[]},DeclaredBy:'installation',Role:'provider',Readiness:'ready',Why:'',Restarts:0,Described:[],Accepted:null});
  views.push({hosts:{Outcome:'page',Hosts:[]},providers:{Outcome:'page',Revision:'providers-v2:o',Declarations:[decl('oa-setup-chat-467d9c6e43391a07c5853578',['abstraction.inference/chat@1']),decl('comfy-image-1828',['abstraction.inference/image@1']),decl('local-stores',['abstraction.storage/inventory-source@1'])]},
   titles:{'oa-setup-chat-467d9c6e43391a07c5853578':'Chat model','comfy-image-1828':'ComfyUI','local-stores':'Model stores'},paired:false,remote:[],remote_error:'router not resolved: not_found'});
  await context.read(); await settle();
  const rows=element('providers').children[0].children;
  assert.deepStrictEqual(rows.map(r=>rowText(r)[0]),['Chat model','ComfyUI','Model stores'],'titles, never internal names');
  assert(rows[0].children[0].children[0].title.includes('oa-setup-chat-467d9c6e43391a07c5853578'),'the raw name stays on the details');
  assert.strictEqual(element('remote-section').hidden,true,'no rows and no pairing: the section hides');
  assert.strictEqual(element('remote').children.length,0);
  assert.strictEqual(element('registry-status').textContent,'','no pairing: no error');
  assert.strictEqual(element('remote-status').textContent,'');
  assertPanelRules([element('providers'), element('remote')], 'Providers', { rules: ['raw-value', 'class'], allowClasses: require('./panel_fake_dom_test_helper.js').allowlistedClasses('registry') });
  views.push({...lastView,paired:true,remote_error:'',remote:[]});
  await context.read(); await settle();
  assert.strictEqual(element('remote-section').hidden,true,'paired with nothing shared: no empty heading');
 }
 // A sentence a click wrote survives the page's next reading of the same
 // state: rows, status lines and focus stay in place.
 element('provider-edit-status').textContent = 'Provider registration updated.';
 views.push(JSON.parse(JSON.stringify(lastView)));
 await context.read(); await settle();
 assert.strictEqual(element('provider-edit-status').textContent, 'Provider registration updated.', 'the sentence survives a reread');
 await chooserSnapshots();
 console.log('PASS registry page');
})().catch(e => { console.error(e); process.exit(1); });
