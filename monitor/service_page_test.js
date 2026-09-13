// Run with node monitor/service_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/service_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
const fields = new Map();
function element(id) {
 if (!fields.has(id)) fields.set(id, {value:'', textContent:'', disabled:false, children:[], replaceChildren(){this.children=[]}, append(v){this.children.push(v)}, click(){}});
 return fields.get(id);
}
const values = new Map(); let submitted = 0;
const localStorage = {get length(){return values.size}, key(i){return [...values.keys()][i]}, getItem(k){return values.get(k)||null}, setItem(k,v){values.set(k,v)}};
const context = {URLSearchParams, localStorage, document:{getElementById:element,createElement(){return {click(){}}}}, location:{search:'?k=test'}, console, Blob,
 URL:{createObjectURL(){return 'blob:test'},revokeObjectURL(){}},
 fetch:async(path,opts)=>{
  if(path==='/binding') return {ok:true,json:async()=>({endpoint:'fixed',history:{LogicalOwner:'owner',HistoryEpoch:'epoch'}})};
  assert.equal(path,'/action');const action=JSON.parse(opts.body);
  const stored=[...values.values()].map(x=>JSON.parse(x));
  assert(stored.some(x=>x.identity.Key===action.identity.Key&&x.endpoint==='fixed'&&x.owner==='owner'&&x.identity.HistoryEpoch==='epoch'&&x.digest===action.digest),'submit preceded recovery persistence');
  submitted++;throw Error('reply lost');
 }};
vm.runInNewContext(script,context);
(async()=>{
 element('url').value='http://127.0.0.1/data';element('digest').value='sha256:'+'a'.repeat(64);element('size').value='1';
 for(const key of ['first','second']) {element('requestkey').value=key;await element('submit').onsubmit({preventDefault(){}})}
 assert.equal(submitted,2);
 assert([...values.keys()].some(k=>k.includes('first')),'older recovery record overwritten');
 assert([...values.keys()].some(k=>k.includes('second')));
 assert(element('operation').textContent.includes('Reconcile'));
 element('requestkey').value='first';element('url').value='http://127.0.0.1/different';await element('submit').onsubmit({preventDefault(){}});assert.equal(submitted,2,'changed work reused a saved key');
 localStorage.setItem=()=>{throw Error('storage refused')};
 element('requestkey').value='never-sent';await element('submit').onsubmit({preventDefault(){}});
 assert.equal(submitted,2,'submission continued after recovery persistence failed');
 console.log('PASS: recovery persisted before send, prior keys retained, storage refusal prevents send');
})().catch(e=>{console.error(e);process.exitCode=1});
