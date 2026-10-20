// Run with node monitor/rights_live_page_test.js. The Permissions page while
// visible: a click changes the fake server's state, the page's own timer
// reads the lists again, and the sentence the click wrote and the rows still
// shown are the same elements as before the reread (the S5 review rule).
const fs=require('fs'),vm=require('./panel_record_test_helper.js').vm,assert=require('assert');
const {assertPanelRules}=require('./static/panel_rules.js');
const {assertLiveUpdateKeepsRow,allowlistedClasses}=require('./panel_fake_dom_test_helper.js');
function constant(file,name){const s=fs.readFileSync(__dirname+'/'+file,'utf8'),m='const '+name+' = `',i=s.indexOf(m)+m.length;assert(i>=m.length,name);return s.slice(i,s.indexOf('`',i))}
const fields=new Map();
function node(tag='div'){return {tag,tagName:tag.toUpperCase(),value:'',textContent:'',className:'',hidden:false,disabled:false,checked:false,attrs:{},children:[],listeners:{},replaceChildren(){this.children=[]},append(...v){this.children.push(...v)},setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return k in this.attrs?this.attrs[k]:null},removeAttribute(k){delete this.attrs[k]},addEventListener(t,f){(this.listeners[t]??=[]).push(f)},querySelector(){return null},querySelectorAll(){return []}}}
function el(id){if(!fields.has(id))fields.set(id,node(/select/.test(id)||id==='rights-action'||id==='rights-permit'||id==='rights-ttl'||id==='rights-scope'||id==='delegation-question'||id==='delegation-option'?'select':'div'));return fields.get(id)}
const page=fs.readFileSync(__dirname+'/rights_page.go','utf8').split('<script>')[1].split('</script>')[0].replace('` + serviceCommonScript + `',constant('service_page.go','serviceCommonScript'));
// The fake server: two waiting questions and one answer given for the person.
const program='C:\\Apps\\Notes\\notes.exe';
let questions=[{ID:'q1',Text:'Notes wants to download.',Options:['allow','refuse'],Option:''},{ID:'q2',Text:'Notes wants to look up models.',Options:['allow','refuse'],Option:''}];
let answers=[{id:'a1',sentence:'Notes wants to read a file.',answerer:program,answererName:'Notes',option:'allow',answered:'2026-10-07T10:00:00Z',state:'in_force'},
 {id:'a2',sentence:'Notes wants to look up models.',answerer:program,answererName:'Notes',option:'allow',answered:'2026-10-07T09:00:00Z',state:'in_force'}];
let assistants=[{program,name:'Notes',expires:''}],assistantReads=0;
let assistantRevision='r1',assistantCandidates=[];
let assistantWarning={answers:[],staysWithYou:[],caution:'',ends:''};
// One typed permission request waiting, as the native dialog words it
// (asks ASK-A10, ASK-O10).
let typed=[{id:'p1',instruction:'Allow Notes access?',content:'Publisher: not verified.\n\nOne: mirror-token\n• Read a saved secret value',warning:'',details:'Program: C:\\Apps\\Notes\\notes.exe\nQuestion: p1',choices:[{label:'Allow',option:'allow'},{label:"Don't allow",option:'refuse'}],digest:'sha256:effect'}],typedPosts=[];
let choices=[];
const policy={Outcome:'page',Revision:'r1',Catalog:[],ActionPlain:{},Rules:[],Next:'',Complete:true};
const ok=v=>({ok:true,json:async()=>v});
const timers=[];
const waitingFailures=new Map();
const waitingPagination=new Map(),waitingReads=[];
const context={console,URLSearchParams,Date,location:{search:'?k=key'},setTimeout(fn){timers.push(fn);return timers.length},clearTimeout(){},
 document:{getElementById:el,createElement:node,createTextNode:t=>({textContent:t}),documentElement:{dataset:{}},visibilityState:'visible',addEventListener(){}},
 fetch:async(path,opts={})=>{
  if(!opts.body){const source=path.startsWith('/rights/requests?')?'permissions':path.startsWith('/questions?')?'questions':path.startsWith('/choices?')?'choices':'';if(source)waitingReads.push({source,cursor:new URLSearchParams(path.split('?')[1]).get('cursor')});const failure=waitingFailures.get(source);if(failure==='transport')throw Error('fixture connection unavailable');if(failure)return ok(source==='permissions'?{outcome:failure}:{Outcome:failure});}
  if(path.startsWith('/questions')&&opts.body){const a=JSON.parse(opts.body);questions=questions.map(q=>q.ID===a.id?{...q,Option:a.option}:q);return ok({Outcome:'answered',Record:{ID:a.id}})}
  if(path.startsWith('/questions'))return ok({Outcome:'page',Records:questions,Next:'',Complete:true,...waitingPagination.get('questions')});
  if(path.startsWith('/choices'))return ok({Outcome:'page',Records:choices,Next:'',Complete:true,...waitingPagination.get('choices')});
  if(path==='/rights/requests'&&opts.body){const a=JSON.parse(opts.body);typedPosts.push(a);typed=typed.filter(r=>r.id!==a.id);return ok({outcome:'answered',message:'Allowed. The app may try again.'})}
  if(path.startsWith('/rights/requests'))return ok({outcome:'page',requests:typed,complete:true,...waitingPagination.get('permissions')});
  if(path==='/rights/answered'&&opts.body){const id=JSON.parse(opts.body).id;answers=answers.map(a=>a.id===id?{...a,state:'ended'}:a);return ok({outcome:'applied',message:'Undone: Notes may no longer read a file.'})}
  if(path==='/rights/answered')return ok({outcome:'page',answers});
  if(path.startsWith('/rights?'))return ok(policy);
  if(path.startsWith('/rights/observe?'))return new Promise(()=>{});
  if(path==='/rights/delegation')return ok({outcome:'page',revision:'r1',account:'alice',questions:[]});
  if(path==='/rights/assistant'&&opts.body){assistants=[];return ok({outcome:'applied',message:'Notes can no longer answer new questions for you. Existing permissions keep their expiry.'})}
  if(path==='/rights/assistant'){assistantReads++;return ok({outcome:'page',revision:assistantRevision,assistants,candidates:assistantCandidates,warning:assistantWarning})}
  if(path==='/explore')return ok({self:{account:'alice',program:'C:\\OA\\Panel.exe'},selfAccountName:'Alice'});
  if(path.startsWith('/apps/registrations?'))return ok({outcome:'page',entries:[]});
  throw Error('unexpected '+path);
 }};
context.window=context;vm.createContext(context);
vm.runInContext(constant('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0],context);
vm.runInContext(page,context);
async function tick(){const fn=timers.shift();assert(fn,'the page keeps a reread timer while visible');await fn()}
const label=box=>box.children[0].children[0].children[0].textContent;
(async()=>{
 await context.rightsArrived;
 assert.equal(context.document.documentElement.dataset.ready,'1');
 // The person's yes belongs to the displayed program and warning. A new
 // program or changed class disclosure needs a fresh confirmation.
 el('assistant-program').value=program;el('assistant-program').oninput();
 el('assistant-confirm').checked=true;context.assistantReady();
 assert.equal(el('assistant-save').disabled,false);
 el('assistant-program').value='C:\\Apps\\Other\\other.exe';el('assistant-program').oninput();
 assert.equal(el('assistant-confirm').checked,false,'a typed program change clears the earlier yes');
 assert.equal(el('assistant-save').disabled,true);
 el('assistant-confirm').checked=true;
 el('assistant-program-select').value=program;el('assistant-program-select').onchange();
 for(const changed of el('assistant-program-select').listeners.change||[])changed();
 assert.equal(el('assistant-confirm').checked,false,'a chosen program change clears the earlier yes');
 el('assistant-confirm').checked=true;context.assistantReady();
 assistantWarning={...assistantWarning,answers:['look up a model in a registry OpenAbstractions lists']};
 await context.loadAssistants();
 assert.equal(el('assistant-confirm').checked,false,'a changed class disclosure clears the earlier yes');
 assert.equal(el('assistant-save').disabled,true);
 el('assistant-confirm').checked=true;context.assistantReady();
 assistantRevision='r2';await context.loadAssistants();
 assert.equal(el('assistant-confirm').checked,false,'a changed policy revision clears the earlier yes');
 assert.equal(el('assistant-save').disabled,true);
 el('assistant-confirm').checked=true;context.assistantReady();
 assistantCandidates=[program];await context.loadAssistants();
 assert.equal(el('assistant-confirm').checked,false,'a changed candidate list clears the earlier yes');
 assert.equal(el('assistant-save').disabled,true);
 assert.deepEqual(el('questions-list').children.map(label),['Notes wants to download','Notes wants to look up models']);
 // Answer the first question: the server records it, the page rereads.
 const kept=el('questions-list').children[1];
 await el('questions-list').children[0].children[0].children[1].children[0].onclick();
 assert.equal(questions[0].Option,'allow','the server state changed');
 assert(el('questions-status').textContent.startsWith('Answer recorded.'),'the click wrote its sentence');
 assert.strictEqual(el('questions-list').children[0],kept,'the question still waiting keeps its row element');
 await tick();
 assert(el('questions-status').textContent.startsWith('Answer recorded.'),'the sentence survives the timer reread');
 assert.strictEqual(el('questions-list').children[0],kept,'an unchanged reread keeps the row element');
 // A typed permission request waits in the same list, worded as the native
 // dialog words it; an unchanged reread keeps its row; Allow answers it with
 // the effect digest the row showed, and the row leaves.
 assert.deepEqual(el('permissions-list').children.map(label),['Allow Notes access?']);
 const typedRow=el('permissions-list').children[0];
 assert.equal(typedRow.children[0].children[0].children[1].textContent,typed[0].content,'the request shows the dialog content');
 assert.deepEqual(typedRow.children[0].children[1].children.map(b=>b.textContent),['Allow',"Don't allow"]);
 await tick();
 assert.strictEqual(el('permissions-list').children[0],typedRow,'an unchanged reread keeps the request row element');
 el('permissions-status').role='status';
 assertPanelRules({tagName:'SECTION',children:[el('permissions-status'),el('permissions-list')]},'Permissions, typed requests',{naming:true,allowClasses:allowlistedClasses('rights')});
 await typedRow.children[0].children[1].children[0].onclick();
 assert.deepEqual(typedPosts,[{id:'p1',option:'allow',digest:'sha256:effect'}],'Allow names the request and the effect it showed');
 assert.equal(el('permissions-status').textContent,'Allowed. The app may try again.');
 assert.equal(el('permissions-list').children.length,0,'the answered request leaves the list');
 await tick();
 assert.equal(el('permissions-status').textContent,'Allowed. The app may try again.','the sentence survives the timer reread');
 // Undo an answer given for the person: the same.
 // Undo one answer; the answers themselves then change on the server (the
 // other one ends too); the timer reread updates both rows in place.
 const second=el('answered-list').children[1];
 await assertLiveUpdateKeepsRow({
  click:()=>el('answered-list').children[0].row.children[1].children[0].onclick(),
  change:()=>{answers=answers.map(a=>a.id==='a2'?{...a,state:'changed'}:a)},
  data:()=>answers,
  tick,row:()=>el('answered-list').children[0],status:()=>el('answered-status'),label:'Permissions, answered for you'});
 assert.equal(answers[0].state,'ended','the server state changed');
 assert.equal(el('answered-status').textContent,'Undone: Notes may no longer read a file.');
 assert.strictEqual(el('answered-list').children[1],second,'the other answer keeps its row element');
 assert(second.row.children[0].children[1].textContent.endsWith('This permission changed since.'),'its row reads the server change in place');
 assert.equal(second.undo.hidden,true,'Undo hides once the answer is no longer in force');
 assert.equal(el('answered-list').children[0].undo.hidden,true);
 // The apps that answer for you are read again by the live tick: Revoke
 // writes its sentence, the server drops the assistant, the tick shows it.
 assert.equal(el('assistant-list').children[0].row.children[0].children[0].textContent,'Notes');assert.equal(el('assistant-list').children[0].row.children[0].children[1].textContent,'Answers routine questions for you');
 const reads=assistantReads;
 await tick();assert(assistantReads>reads,'the live tick reads the answering apps again');
 assistants=[{program,name:'Notes',expires:'2099-11-01T00:00:00Z'}];
 const assistantRow=el('assistant-list').children[0];await tick();
 assert.strictEqual(el('assistant-list').children[0],assistantRow,'a changed answering app keeps its row element');
 assert(assistantRow.row.children[0].children[1].textContent.startsWith('Answers routine questions for you · Expires '),'the row reads the change in place');
 await assistantRow.row.children[1].children[1].onclick();
 assert(el('assistant-status').textContent.startsWith('Notes can no longer answer'));
 assert.equal(el('assistant-list').children[0].textContent,'You answer questions yourself.','the list reads the revoke');
 await tick();assert(el('assistant-status').textContent.startsWith('Notes can no longer answer'),'the sentence survives the reread');
 const section=(...ids)=>({tagName:'SECTION',children:ids.map(el)});
 for(const id of ['questions-status','choices-status','answered-status','assistant-status'])el(id).role='status';
 assertPanelRules({tagName:'MAIN',children:[section('questions-status','questions-list'),section('answered-status','answered-list'),section('assistant-status','assistant-list')]},'Permissions',{naming:true,allowClasses:allowlistedClasses('rights')});
 // Empty is a successful observation of every waiting source. A failed
 // refresh must withdraw that claim and stale answer controls, then recover
 // through the same list readers used by the visible-page refresh owner.
 questions=[];typed=[];
 const readers={questions:()=>context.questions(),choices:()=>context.choicesList(),permissions:()=>context.permissionsList()};
 await Promise.all(Object.values(readers).map(read=>read()));
 assert.equal(el('questions-empty').hidden,false);
 for(const [source,read] of Object.entries(readers)){
  for(const failure of ['unavailable','forbidden','transport']){
   waitingFailures.set(source,failure);await read();
   assert.equal(el('questions-empty').hidden,true,source+' '+failure+' is not an empty snapshot');
   assert.equal(el(source+'-list').children.length,0,'failed refresh clears old answer controls');
   assert.equal(el('questions-next').hidden,true,'failed refresh offers no stale next page');
   assert(el(source+'-status').textContent,source+' shows its read failure');
   waitingFailures.delete(source);await tick();
   assert.equal(el('questions-empty').hidden,false,source+' recovery restores the observed empty state');
   assert.equal(el(source+'-status').textContent,'',source+' recovery clears only its failed-read status');
  }
 }
 waitingFailures.set('permissions','unavailable');await readers.permissions();
 waitingFailures.set('permissions','unsupported');await readers.permissions();
 assert.equal(el('questions-empty').hidden,false,'an explicitly unsupported legacy source has no waiting rows');
 assert.equal(el('permissions-status').textContent,'','unsupported does not retain an earlier outage');
 waitingFailures.delete('permissions');await tick();
 questions=[{ID:'new-q',Text:'A newly waiting question.',Options:['allow','refuse'],Option:''}];
 typed=[{id:'new-p',instruction:'A newly waiting permission.',content:'Current service wording.',choices:[{label:'Allow',option:'allow'}],digest:'sha256:new-effect'}];
 choices=[{ID:'new-c',Asker:'Notes',State:'pending',Request:{Title:'A newly waiting app choice.',Options:[{ID:'one',Label:'One'}],PendingUntilMs:Date.now()+60000,RequestRef:{ReplayUntilMs:Date.now()+60000}}}];
 await Promise.all(Object.values(readers).map(read=>read()));
 for(const [source,read] of Object.entries(readers)){
  assert.equal(el(source+'-list').children.length,1);
  waitingFailures.set(source,'transport');await read();assert.equal(el(source+'-list').children.length,0);
  waitingFailures.delete(source);await tick();assert.equal(el(source+'-list').children.length,1,source+' fresh refresh restores current controls');
 }
 // Each pass reads every page up to the bound (architect ruling 2026-11-09,
 // second block): a source past it shows the one Show more, a record seen
 // on several pages shows once, and a failed read clears the button.
 for(const [source,read] of Object.entries(readers)){
  waitingPagination.set(source,source==='permissions'?{next:'continuation',complete:false}:{Next:'continuation',Complete:false});
  const before=waitingReads.length;await read();
  const pass=waitingReads.slice(before).filter(r=>r.source===source);
  assert.equal(pass[0].cursor,'',source+' starts at the first page');assert(pass.slice(1).every(r=>r.cursor==='continuation'),source+' follows the continuation');
  assert.equal(pass.length,context.waitingPageLimit,source+' stops at the bound');
  assert.equal(el('questions-next').hidden,false,'one Show more past the bound');assert.equal(el(source+'-list').children.length,1,'a record on every page shows once');
  waitingFailures.set(source,'transport');await el('questions-next').onclick();
  assert.equal(el('questions-next').hidden,true);assert.equal(el(source+'-list').children.length,0);
  waitingFailures.delete(source);waitingPagination.delete(source);await tick();
  assert.equal(waitingReads.filter(r=>r.source===source).at(-1).cursor,'',source+' recovers from its first page through the live refresh');
  assert.equal(el(source+'-list').children.length,1,source+' recovers its rows');assert.equal(el('questions-next').hidden,true,source+' complete again: no Show more');
 }
 // A waiting question past the first page counts: "No questions are
 // waiting." stays hidden.
 {
  const waitingQuestion=questions[0];questions=[];waitingPagination.set('questions',{Next:'second',Complete:false});
  const firstPage=context.fetch;context.fetch=async(path,opts)=>path.startsWith('/questions?')&&path.includes('cursor=second')?ok({Outcome:'page',Records:[waitingQuestion],Next:'',Complete:true}):firstPage(path,opts);
  await readers.questions();
  assert.equal(el('questions-list').children.length,1,'the second page shows its waiting question');assert.equal(el('questions-empty').hidden,true);
  context.fetch=firstPage;waitingPagination.delete('questions');questions=[waitingQuestion];
 }
 context.stopRightsWatch();
 // A refused Saved permissions read still settles the first reads, so the
 // page says it is ready with its refusal shown.
 {
  fields.clear();
  const refused={console,URLSearchParams,Date,location:{search:'?k=key'},setTimeout(){return 0},clearTimeout(){},
   document:{getElementById:el,createElement:node,createTextNode:t=>({textContent:t}),documentElement:{dataset:{}},visibilityState:'hidden',addEventListener(){}},
   fetch:async(path,opts)=>path.startsWith('/rights?')?ok({Outcome:'forbidden'}):context.fetch(path,opts)};
  refused.window=refused;vm.createContext(refused);
  vm.runInContext(constant('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0],refused);vm.runInContext(page,refused);
  const settled=await Promise.race([refused.rightsArrived.then(()=>true),new Promise(resolve=>setTimeout(()=>resolve(false),2000))]);
  assert(settled,'a refused rules read settles the first reads');
  assert.equal(refused.document.documentElement.dataset.ready,'1','the page marks itself ready with the refusal shown');
  assert(el('rights-status').textContent,'the refusal is said');
  refused.stopRightsWatch();
 }
 console.log('PASS: Permissions: a click changes the server, the timer rereads, the sentence and the rows still shown survive');
})().catch(e=>{console.error(e);process.exitCode=1});
