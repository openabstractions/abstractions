// Run with node monitor/registry_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/registry_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
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
   {Declaration:{Name:'local-stores',Activation:'on_demand',Contracts:['abstraction.storage/inventory-source@1'],Endpoint:'inventoryd-v1',Program:'/opt/inventoryd',Resources:['store:ollama','store:huggingface']},
    DeclaredBy:'/opt/oa/openabstractions',Readiness:'refused',Why:'program:server program mismatch',Restarts:2,Described:[],Accepted:null},
   {Declaration:{Name:'lab',Activation:'remote',Contracts:['abstraction.inference/chat@1','abstraction.router/router@1'],Endpoint:'tls://lab.example:8443',Program:'',Resources:['profile:chat'],Remote:{ServerName:'lab.example'}},
    DeclaredBy:'/opt/oa/openabstractions',Readiness:'ready',Why:'',Restarts:0,Described:[{Contract:'abstraction.inference/chat@1',Readiness:'ready'},{Contract:'abstraction.router/router@1',Readiness:'ready'}],Accepted:null}]},
  remote:[{Host:'lab/openrouter',Domain:'lab',Hosted:true,Wire:'openai-compatible',DeclaredBy:'operator',Profiles:['chat'],Up:true,Why:''}]},
 {hosts:{Outcome:'forbidden', Hosts:[]}, providers:{Outcome:'page', Declarations:[]}, remote:[], remote_error:'router not resolved: forbidden'},
];
const context = {URLSearchParams, document:{getElementById:element,createElement:node}, location:{search:'?k=test'}, console,
 fetch:async(path,opts)=>{
  assert.strictEqual(path, '/registry/view');
  assert.strictEqual(opts.headers['X-Panel-Key'], 'test');
  return {ok:true, json:async()=>views.shift()};
 }};
vm.runInNewContext(script, context);
const cells = row => row.children.map(td => td.textContent);
(async () => {
 const settle = () => new Promise(r => setTimeout(r, 0));
 element('registry-read').onclick(); await settle(); await settle();
 assert.deepStrictEqual(cells(element('hosts').children[0]), ['ollama','local','ollama','http://127.0.0.1:11500','ollama','chat, embed','up']);
 assert.deepStrictEqual(cells(element('hosts').children[1]), ['openrouter','hosted','openai-compatible','https://openrouter.ai/api/v1','operator','chat','down: credential:not_found']);
 assert.deepStrictEqual(cells(element('providers').children[0]), ['local-stores','on_demand','abstraction.storage/inventory-source@1','inventoryd-v1','/opt/inventoryd','store:ollama, store:huggingface','refused: program:server program mismatch','','2','','/opt/oa/openabstractions']);
 assert.deepStrictEqual(cells(element('providers').children[1]), ['lab','remote','abstraction.inference/chat@1, abstraction.router/router@1','tls://lab.example:8443 (lab.example)','','profile:chat','ready','abstraction.inference/chat@1 ready, abstraction.router/router@1 ready','0','','/opt/oa/openabstractions']);
 assert.deepStrictEqual(cells(element('remote').children[0]), ['lab/openrouter','lab','openai-compatible','operator','chat','up']);
 element('registry-read').onclick(); await settle(); await settle();
 assert.strictEqual(element('hosts').children.length, 0);
 assert.match(element('hosts-status').textContent, /did not permit/);
 assert.strictEqual(element('providers-status').textContent, 'No providers declared.');
 assert.strictEqual(element('remote-status').textContent, 'router not resolved: forbidden');
 console.log('PASS registry page');
})().catch(e => { console.error(e); process.exit(1); });
