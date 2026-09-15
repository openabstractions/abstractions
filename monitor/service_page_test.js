// Run with node monitor/service_page_test.js; no browser or service is started.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const go = fs.readFileSync(__dirname + '/service_page.go', 'utf8');
const script = go.split('<script>')[1].split('</script>')[0];
const fields = new Map();
function node() {
 return {value:'', textContent:'', disabled:false, children:[], replaceChildren(){this.children=[]}, append(...v){this.children.push(...v)}, click(){}};
}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
const values = new Map(); let submitted = 0; const questionCalls = [];
const questionReplies = {answer:[{Outcome:'forbidden'},{Outcome:'answered',Record:{Id:'q1'}}], retire:[{Outcome:'unavailable'},{Outcome:'retired'}]};
let questionPage = {Outcome:'page', Records:[{Id:'q1', Text:'Allow download?', Options:['once','refuse'], Option:''}], Next:'', Complete:true};
const rule = {Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.storage/content.read',Resource:'sha256:x',Permit:true};
let rightsPage = {Outcome:'page', Revision:'rev-1', Catalog:['abstraction.storage/content.read'], Rules:[rule], Next:'', Complete:true};
const rightsCalls = [];
const rightsReplies = [{Outcome:'forbidden',Revision:''},{Outcome:'conflict',Revision:'rev-2',Current:{...rule,Permit:false}},{Outcome:'unavailable',Revision:''},{Outcome:'applied',Revision:'rev-3',Current:{...rule,Resource:'sha256:y'}}];
const localStorage = {get length(){return values.size}, key(i){return [...values.keys()][i]}, getItem(k){return values.get(k)||null}, setItem(k,v){values.set(k,v)}};
const context = {URLSearchParams, localStorage, document:{getElementById:element,createElement:node}, location:{search:'?k=test'}, console, Blob,
 URL:{createObjectURL(){return 'blob:test'},revokeObjectURL(){}},
 fetch:async(path,opts)=>{
  if(path==='/binding') return {ok:true,json:async()=>({endpoint:'fixed',history:{LogicalOwner:'owner',HistoryEpoch:'epoch'}})};
  if(path.startsWith('/rights')){
   if(!opts.body) return {ok:true,json:async()=>rightsPage};
   const edit=JSON.parse(opts.body);rightsCalls.push(edit);
   return {ok:true,json:async()=>rightsReplies.shift()};
  }
  if(path.startsWith('/questions')){
   if(!opts.body) return {ok:true,json:async()=>questionPage};
   const action=JSON.parse(opts.body);questionCalls.push(action);
   return {ok:true,json:async()=>questionReplies[action.action].shift()};
  }
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

 await element('questions-start').onclick();
 const box=element('questions').children[0];
 const buttons=box.children.filter(c=>c.onclick);
 assert.deepEqual(buttons.map(b=>b.textContent),['once','refuse','Retire'],'pending question offers its options and retirement');
 assert(box.children[0].textContent.includes('Pending'));
 await buttons[0].onclick();
 assert.deepEqual(questionCalls[0],{action:'answer',id:'q1',option:'once'});
 assert(element('questions-status').textContent.includes('not authorized'),'forbidden answer is shown as a refusal');
 await buttons[2].onclick();
 assert.deepEqual(questionCalls[1],{action:'retire',id:'q1'});
 assert(element('questions-status').textContent.includes('no decision is assumed'),'unavailable retirement is not shown as success');
 await buttons[0].onclick();
 assert(element('questions-status').textContent.includes('Answer recorded'));
 await buttons[2].onclick();
 assert(element('questions-status').textContent.includes('Question retired'));
 questionPage={Outcome:'forbidden', Records:[], Next:'', Complete:false};
 await element('questions-start').onclick();
 assert.equal(element('questions').children.length,0,'refused list shows no stale questions');
 assert(element('questions-status').textContent.includes('not authorized'));

 await element('rights-grant').onsubmit({preventDefault(){}});
 assert.equal(rightsCalls.length,0,'edit without a listed revision reached the service');
 assert(element('rights-status').textContent.includes('List policy'));
 await element('rights-start').onclick();
 assert.deepEqual(element('rights-action').children.map(o=>o.value),['abstraction.storage/content.read'],'catalogue offers the grant actions');
 const ruleBox=element('rights').children[0];
 assert(ruleBox.children[0].textContent.includes('permit abstraction.storage/content.read on sha256:x'),'rule shows its condition');
 assert(ruleBox.children[0].textContent.includes('alice running /usr/bin/app'));
 assert(element('rights-status').textContent.includes('rev-1'));
 await ruleBox.children[1].onclick();
 assert.deepEqual(rightsCalls[0],{edit:'revoke',account:'alice',program:'/usr/bin/app',action:'abstraction.storage/content.read',resource:'sha256:x',revision:'rev-1'});
 assert(element('rights-status').textContent.includes('not authorized'),'forbidden revoke is shown as a refusal');
 element('rights-account').value='alice';element('rights-program').value='/usr/bin/app';element('rights-action').value='abstraction.storage/content.read';element('rights-resource').value='sha256:x';element('rights-permit').value='deny';
 await element('rights-grant').onsubmit({preventDefault(){}});
 assert.deepEqual(rightsCalls[1],{edit:'set',account:'alice',program:'/usr/bin/app',action:'abstraction.storage/content.read',resource:'sha256:x',permit:false,revision:'rev-1'});
 assert(element('rights-status').textContent.includes('nothing was changed'),'conflict is not shown as success');
 assert(element('rights-status').textContent.includes('Current rule: deny'),'conflict shows the observed rule');
 await element('rights-grant').onsubmit({preventDefault(){}});
 assert(element('rights-status').textContent.includes('no change is assumed'),'unavailable edit is not shown as success');
 element('rights-resource').value='sha256:y';element('rights-permit').value='permit';
 await element('rights-grant').onsubmit({preventDefault(){}});
 assert(element('rights-status').textContent.includes('Rule set: permit'),'applied grant is reported');
 await element('rights-grant').onsubmit({preventDefault(){}});
 assert.equal(rightsCalls.length,4,'applied edit left a stale revision for the next edit');
 rightsPage={Outcome:'unavailable', Revision:'', Catalog:[], Rules:[], Next:'', Complete:false};
 await element('rights-start').onclick();
 assert.equal(element('rights').children.length,0,'refused list shows no stale rules');
 assert(element('rights-status').textContent.includes('no change is assumed'));
 console.log('PASS: recovery persisted before send, prior keys retained, storage refusal prevents send; questions answer/retire with typed refusals; rights grant/revoke at listed revision with typed refusals');
})().catch(e=>{console.error(e);process.exitCode=1});
