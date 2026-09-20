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
const firstUseRule = {Subject:{Account:'alice',Program:'/usr/bin/python3'},Action:'abstraction.job/acceptance.submit',Resource:'abstraction.job/acceptance@1',Permit:true};
const questionReplies = {answer:[{Outcome:'forbidden'},{Outcome:'answered',Record:{ID:'q1'}},{Outcome:'answered',Record:{ID:'q2'},rule:firstUseRule,edit:{Outcome:'applied',Revision:'rev-2'}},{Outcome:'answered',Record:{ID:'q2'},rule:{...firstUseRule,Permit:false},edit:{Outcome:'conflict',Revision:'rev-3'}},{Outcome:'answered',Record:{ID:'q2'},rule:firstUseRule,edit:{Outcome:'applied',Revision:'rev-4'}}], retire:[{Outcome:'unavailable'},{Outcome:'retired'}]};
let questionPage = {Outcome:'page', Records:[{ID:'q1', Text:'Allow download?', Options:['once','refuse'], Option:''}], Next:'', Complete:true};
const rule = {Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.storage/content.read',Resource:'sha256:x',Permit:true};
let rightsPage = {Outcome:'page', Revision:'rev-1', Catalog:['abstraction.storage/content.read'], Rules:[rule], Next:'', Complete:true};
const rightsCalls = [];
const accountPages = [{Outcome:'forbidden', Snapshots:[], Next:'', Complete:false}, {Outcome:'page', Next:'', Complete:true, Snapshots:[{Receipt:{OperationID:'op-other'}, State:'running', Label:'another program'}]}];
const allowRules = [{action:'abstraction.job/acceptance.submit',resource:'abstraction.job/acceptance@1'},{action:'abstraction.model/lookup',resource:'hf'}];
const allowCalls = [], allowReplies = [{rules:allowRules,outcome:'conflict',revision:'rev-9',landed:[allowRules[0]],stopped:allowRules[1]},{rules:allowRules,outcome:'applied',revision:'rev-10',landed:allowRules}];
const accountCalls = [], accountReplies = [{Outcome:'forbidden'},{Outcome:'requested'}];
const jobReceipt = id => ({OperationID:id, LogicalOwner:'owner', Identity:{Key:id, HistoryEpoch:'epoch'}});
const inventoryPage = {Outcome:'page', Next:'', Complete:true, Snapshots:[
 {Receipt:jobReceipt('op-derived'), State:'running', Label:'huggingface.co · model.safetensors', LabelDerived:true},
 {Receipt:jobReceipt('op-caller'), State:'complete', Label:'org/tiny@abc · unet/model.safetensors', LabelDerived:false},
 {Receipt:jobReceipt('op-absent'), State:'pending', Label:'', LabelDerived:false, Waiting:'network:metered'}]};
const rightsReplies = [{Outcome:'forbidden',Revision:''},{Outcome:'conflict',Revision:'rev-2',Current:{...rule,Permit:false}},{Outcome:'unavailable',Revision:''},{Outcome:'applied',Revision:'rev-3',Current:{...rule,Resource:'sha256:y'}}];
const localStorage = {get length(){return values.size}, key(i){return [...values.keys()][i]}, getItem(k){return values.get(k)||null}, setItem(k,v){values.set(k,v)}};
const context = {URLSearchParams, localStorage, document:{getElementById:element,createElement:node}, location:{search:'?k=test'}, console, Blob,
 URL:{createObjectURL(){return 'blob:test'},revokeObjectURL(){}},
 fetch:async(path,opts)=>{
  if(path==='/binding') return {ok:true,json:async()=>({endpoint:'fixed',history:{LogicalOwner:'owner',HistoryEpoch:'epoch'}})};
  if(path.startsWith('/inventory')) return {ok:true,json:async()=>inventoryPage};
  if(path.startsWith('/account-work')){
   if(!opts.body) return {ok:true,json:async()=>accountPages.shift()};
   accountCalls.push(JSON.parse(opts.body));
   return {ok:true,json:async()=>accountReplies.shift()};
  }
  if(path==='/rights/allow'){allowCalls.push(JSON.parse(opts.body));return {ok:true,json:async()=>allowReplies.shift()}}
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

 await element('next').onclick();
 const titles=element('records').children.map(box=>box.children[0].textContent);
 assert.deepEqual(titles,['huggingface.co · model.safetensors (derived) - running','org/tiny@abc · unet/model.safetensors - complete','Unlabelled operation op-absent (waiting: network:metered) - pending'],'job list shows each label, derived ones marked, and what holds waiting work');

 await element('account-start').onclick();
 assert.equal(element('account').children.length,0,'a refused account listing shows no work');
 assert(element('account-status').textContent.includes('no rule'),'forbidden account listing names the missing rule');
 await element('account-start').onclick();
 const other=element('account').children[0];
 assert(other.children[0].textContent.includes('op-other - running - another program'),'account work shows every program\'s operation');
 await other.children[1].onclick();
 assert.deepEqual(accountCalls[0],{operation:'op-other'});
 assert(element('account-status').textContent.includes('nothing was listed or changed'),'forbidden cancellation is a refusal');
 await other.children[1].onclick();
 assert(element('account-status').textContent.includes('cancellation requested'),'permitted cancellation is reported');

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
 await buttons[0].onclick();
 assert(element('questions-status').textContent.includes('Rule written: permit abstraction.job/acceptance.submit on abstraction.job/acceptance@1 for /usr/bin/python3 (asked at first use)'),'a first-use answer reports the rule it wrote');
 await buttons[0].onclick();
 assert(element('questions-status').textContent.includes('The rule was not written (conflict): deny'),'a first-use rule that did not apply is not shown as written');
 questionPage={Outcome:'page', Records:[{ID:'q2', Text:'/usr/bin/python3 wants to abstraction.job/acceptance.submit on abstraction.job/acceptance@1', Options:['allow','refuse','never'], Option:'allow'}], Next:'', Complete:true, ruleStates:{q2:'not_written'}};
 await element('questions-start').onclick();
 const answeredBox=element('questions').children[0];
 assert(answeredBox.children[0].textContent.includes('Answered, rule not written.'),'an answer whose rule was not written says so');
 const retryRule=answeredBox.children.find(c=>c.textContent==='Retry rule');
 assert(retryRule&&!answeredBox.children.some(c=>c.textContent==='allow'),'an answered question offers only the rule retry');
 await retryRule.onclick();
 assert.deepEqual(questionCalls.at(-1),{action:'answer',id:'q2',option:'allow'},'the retry answers the same option again');
 assert(element('questions-status').textContent.includes('Rule written: permit'),'the retry reports the rule it wrote');
 questionPage.ruleStates={q2:'written'};
 await element('questions-start').onclick();
 assert(element('questions').children[0].children[0].textContent.includes('Rule written.')&&!element('questions').children[0].children.some(c=>c.textContent==='Retry rule'),'a written rule offers no retry');
 questionPage={Outcome:'forbidden', Records:[], Next:'', Complete:false};
 await element('questions-start').onclick();
 assert.equal(element('questions').children.length,0,'refused list shows no stale questions');
 assert(element('questions-status').textContent.includes('not authorized'));

 await element('rights-allow').onsubmit({preventDefault(){}});
 assert.equal(allowCalls.length,0,'allow without a listed revision reached the service');
 await element('rights-grant').onsubmit({preventDefault(){}});
 assert.equal(rightsCalls.length,0,'edit without a listed revision reached the service');
 assert(element('rights-status').textContent.includes('List policy'));
 await element('rights-start').onclick();
 assert.deepEqual(element('rights-action').children.map(o=>o.value),['abstraction.storage/content.read'],'catalogue offers the grant actions');
 assert.deepEqual(element('allow-programs').children.map(o=>o.value),['/usr/bin/app'],'the program picker offers the listed programs');
 element('allow-for').value='downloads';element('allow-program').value='/usr/bin/app';element('allow-names').value='hf, ';element('allow-credentials').value='';
 await element('rights-allow').onsubmit({preventDefault(){}});
 assert.deepEqual(allowCalls[0],{for:'downloads',revision:'rev-1',account:'alice',program:'/usr/bin/app',registries:['hf'],hosts:[],credentials:[]});
 assert(element('rights-status').textContent.includes('Stopped at abstraction.model/lookup on hf; 1 of 2 rules landed'),'a stopped bundle names what landed');
 await element('rights-allow').onsubmit({preventDefault(){}});
 assert.equal(allowCalls.length,1,'a stopped bundle left its revision for another edit');
 await element('rights-start').onclick();
 await element('rights-allow').onsubmit({preventDefault(){}});
 assert(element('rights-status').textContent.includes('Allowed downloads for /usr/bin/app'),'an applied bundle is reported');
 assert(element('rights-status').textContent.includes('permit abstraction.model/lookup on hf'));
 await element('rights-start').onclick();
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
 console.log('PASS: job list shows labels, derived ones marked; recovery persisted before send, prior keys retained, storage refusal prevents send; questions answer/retire with typed refusals; rights grant/revoke at listed revision with typed refusals');
})().catch(e=>{console.error(e);process.exitCode=1});
