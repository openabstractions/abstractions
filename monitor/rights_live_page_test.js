// Run with node monitor/rights_live_page_test.js. The Permissions page while
// visible: a click changes the fake server's state, the page's own timer
// reads the lists again, and the sentence the click wrote and the rows still
// shown are the same elements as before the reread (the S5 review rule).
const fs=require('fs'),vm=require('vm'),assert=require('assert');
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
// One typed permission request waiting, as the native dialog words it
// (asks ASK-A10, ASK-O10).
let typed=[{id:'p1',instruction:'Allow Notes access?',content:'Publisher: not verified.\n\nOne: mirror-token\n• Read a saved secret value',warning:'',details:'Program: C:\\Apps\\Notes\\notes.exe\nQuestion: p1',choices:[{label:'Allow',option:'allow'},{label:"Don't allow",option:'refuse'}],digest:'sha256:effect'}],typedPosts=[];
const policy={Outcome:'page',Revision:'r1',Catalog:[],ActionPlain:{},Rules:[],Next:'',Complete:true};
const ok=v=>({ok:true,json:async()=>v});
const timers=[];
const context={console,URLSearchParams,Date,location:{search:'?k=key'},setTimeout(fn){timers.push(fn);return timers.length},clearTimeout(){},
 document:{getElementById:el,createElement:node,createTextNode:t=>({textContent:t}),documentElement:{dataset:{}},visibilityState:'visible',addEventListener(){}},
 fetch:async(path,opts={})=>{
  if(path.startsWith('/questions')&&opts.body){const a=JSON.parse(opts.body);questions=questions.map(q=>q.ID===a.id?{...q,Option:a.option}:q);return ok({Outcome:'answered',Record:{ID:a.id}})}
  if(path.startsWith('/questions'))return ok({Outcome:'page',Records:questions,Next:'',Complete:true});
  if(path.startsWith('/choices'))return ok({Outcome:'page',Records:[],Next:'',Complete:true});
  if(path==='/rights/requests'&&opts.body){const a=JSON.parse(opts.body);typedPosts.push(a);typed=typed.filter(r=>r.id!==a.id);return ok({outcome:'answered',message:'Allowed. The app may try again.'})}
  if(path.startsWith('/rights/requests'))return ok({outcome:'page',requests:typed,complete:true});
  if(path==='/rights/answered'&&opts.body){const id=JSON.parse(opts.body).id;answers=answers.map(a=>a.id===id?{...a,state:'ended'}:a);return ok({outcome:'applied',message:'Undone: Notes may no longer read a file.'})}
  if(path==='/rights/answered')return ok({outcome:'page',answers});
  if(path.startsWith('/rights?'))return ok(policy);
  if(path.startsWith('/rights/observe?'))return new Promise(()=>{});
  if(path==='/rights/delegation')return ok({outcome:'page',revision:'r1',account:'alice',questions:[]});
  if(path==='/rights/assistant'&&opts.body){assistants=[];return ok({outcome:'applied',message:'Notes can no longer answer new questions for you. Existing permissions keep their expiry.'})}
  if(path==='/rights/assistant'){assistantReads++;return ok({outcome:'page',revision:'r1',assistants,candidates:[],warning:{answers:[],staysWithYou:[],caution:'',ends:''}})}
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
 assert.deepEqual(el('questions-list').children.map(label),['Notes wants to download.','Notes wants to look up models.']);
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
 assertPanelRules({tagName:'SECTION',children:[el('permissions-status'),el('permissions-list')]},'Permissions, typed requests',{allowClasses:allowlistedClasses('rights')});
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
 assert(second.row.children[0].children[1].textContent.endsWith('You changed this permission since.'),'its row reads the server change in place');
 assert.equal(second.undo.hidden,true,'Undo hides once the answer is no longer in force');
 assert.equal(el('answered-list').children[0].undo.hidden,true);
 // The apps that answer for you are read again by the live tick: Revoke
 // writes its sentence, the server drops the assistant, the tick shows it.
 assert.equal(el('assistant-list').children[0].row.children[0].children[0].textContent,'Notes answers routine questions for you.');
 const reads=assistantReads;
 await tick();assert(assistantReads>reads,'the live tick reads the answering apps again');
 assistants=[{program,name:'Notes',expires:'2026-11-01T00:00:00Z'}];
 const assistantRow=el('assistant-list').children[0];await tick();
 assert.strictEqual(el('assistant-list').children[0],assistantRow,'a changed answering app keeps its row element');
 assert(assistantRow.row.children[0].children[0].textContent.startsWith('Notes answers routine questions for you (expires '),'the row reads the change in place');
 await assistantRow.row.children[1].children[1].onclick();
 assert(el('assistant-status').textContent.startsWith('Notes can no longer answer'));
 assert.equal(el('assistant-list').children[0].textContent,'You answer questions yourself.','the list reads the revoke');
 await tick();assert(el('assistant-status').textContent.startsWith('Notes can no longer answer'),'the sentence survives the reread');
 const section=(...ids)=>({tagName:'SECTION',children:ids.map(el)});
 for(const id of ['questions-status','choices-status','answered-status','assistant-status'])el(id).role='status';
 assertPanelRules({tagName:'MAIN',children:[section('questions-status','questions-list'),section('answered-status','answered-list'),section('assistant-status','assistant-list')]},'Permissions',{allowClasses:allowlistedClasses('rights')});
 context.stopRightsWatch();
 console.log('PASS: Permissions: a click changes the server, the timer rereads, the sentence and the rows still shown survive');
})().catch(e=>{console.error(e);process.exitCode=1});
