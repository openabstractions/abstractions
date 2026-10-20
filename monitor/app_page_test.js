// Run with node monitor/app_page_test.js. Drives the app page's own script
// (app_page.go, appPageScript) in a fake DOM against a fake Panel: the
// facts the server filtered for this app (/app/<name>/facts), its answers
// (/rights/answered?app=), permission edits that change the fake server's
// state, Answering for you, Start and the two-step Remove. No browser or
// service starts.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const {assertPanelRules}=require('./static/panel_rules.js');
const {assertLiveUpdateKeepsRow,allowlistedClasses}=require('./panel_fake_dom_test_helper.js');
function constant(file,name){const s=fs.readFileSync(__dirname+'/'+file,'utf8'),m='const '+name+' = `',i=s.indexOf(m)+m.length;assert(i>=m.length,name);return s.slice(i,s.indexOf('`',i))}
const fields=new Map();
function node(tag='div'){return {tag,tagName:tag.toUpperCase(),value:'',textContent:'',className:'',hidden:false,disabled:false,checked:false,dataset:{},attrs:{},children:[],replaceChildren(){this.children=[]},append(...v){this.children.push(...v)},setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return k in this.attrs?this.attrs[k]:null},removeAttribute(k){delete this.attrs[k]},addEventListener(){}}}
function el(id){if(!fields.has(id))fields.set(id,node());return fields.get(id)}
const all=n=>[n,...(n.children||[]).flatMap(all)];
const text=n=>all(n).map(c=>c.textContent).join(' | ');
const account='alice',program='C:/Apps/Example/example.exe';
el('app-page').dataset={name:'example',title:'Example',shown:'digest-1',defaults:'/rights?k=key#defaults'};
const now=Date.now();
// The fake server's state: what /app/example/facts returns is what the
// Go handler filtered (app_facts.go); edits change it.
let revision='r1',rules=[
 {Subject:{Account:account,Program:program},Action:'abstraction.model/lookup',Resource:'hf',Permit:true},
 {Subject:{Account:account,Program:program},Action:'abstraction.inference/complete',Resource:'local',Permit:false,Expires:new Date(now+3600000).toISOString()},
 {Subject:{Account:account,Program:program},Action:'abstraction.storage/content.read',Resource:'sha256:0123456789abcdef0123',Permit:true}];
let calls=Array.from({length:6},(_,i)=>({model:i<4?'C:\\models\\llama.gguf':'qwen',server:i<4?'Engine host':'Model server',unixMs:now-60000*(i+1),outcome:'completed'}));
let assistant={program,name:'Example',expires:''},factsReads=0,scriptHost=false;
// The defaults for all apps the runtime has switched on (rights RIGHTS-R22).
const allDefaults=[{name:'download',label:'Download in the background',action:'abstraction.job/acceptance.submit',resource:'download-jobs'},
 {name:'announce',label:'Say when it is running',action:'abstraction.facade/application.announce',resource:'app:example'}];
const answers=[{id:'x',sentence:'Notes wants to look up models.',answerer:program,option:'allow',answered:new Date(now-1000).toISOString(),state:'in_force'}];
const writes=[],actions=[],assistantStops=[],answeredQueries=[];
const ok=v=>({ok:true,json:async()=>v});
const timers=[];
const context={console,URLSearchParams,AbortController,Date,location:{search:'?k=key'},setTimeout(fn){timers.push(fn);return timers.length},clearTimeout(){},
 document:{getElementById:el,createElement:node,documentElement:{dataset:{}},visibilityState:'visible',addEventListener(){}},
 fetch:async(path,opts={})=>{
  if(path==='/app/example/facts'){factsReads++;
   // The server lists a default only while the app holds no rule on its target (app_facts.go).
   const defaults=scriptHost?[]:allDefaults.filter(d=>!rules.some(r=>r.Action===d.action&&r.Resource===d.resource));
   return ok({outcome:'page',rulesOutcome:'page',revision,actionPlain:{'abstraction.model/lookup':'look up models'},rules,callsOutcome:'page',calls,assistant,assistantRevision:revision,defaultsOutcome:scriptHost?'excluded':'page',defaults,scriptHost,subject:{Account:account,Program:program}})}
  if(path.startsWith('/rights/answered?')){answeredQueries.push(path);return ok({outcome:'page',answers})}
  if(path.startsWith('/rights/observe?'))return new Promise(()=>{});
  if(path==='/rights'&&opts.body){const edit=JSON.parse(opts.body);writes.push(edit);
   const held=rules.some(r=>r.Action===edit.action&&r.Resource===edit.resource);
   rules=edit.edit==='revoke'?rules.filter(r=>!(r.Action===edit.action&&r.Resource===edit.resource)):held?rules.map(r=>r.Action===edit.action&&r.Resource===edit.resource?{...r,Permit:edit.permit}:r):[...rules,{Subject:{Account:edit.account,Program:edit.program},Action:edit.action,Resource:edit.resource,Permit:edit.permit}];
   revision='r'+(writes.length+1);return ok({Outcome:'applied',Revision:revision})}
  if(path==='/rights/assistant'&&opts.body){assistantStops.push(JSON.parse(opts.body));assistant=null;return ok({outcome:'applied',message:'Example can no longer answer new questions for you. Existing permissions keep their expiry.'})}
  if(path.startsWith('/app/example/')){actions.push([path,JSON.parse(opts.body)]);return path.endsWith('/start')?ok({outcome:'ready',message:'Example is running.'}):ok({outcome:'applied',message:'Removed Example from OpenAbstractions.'})}
  throw Error('unexpected '+path);
 }};
context.window=context;vm.createContext(context);
vm.runInContext(constant('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0],context);
vm.runInContext(constant('service_page.go','serviceCommonScript')+constant('app_page.go','appPageScript'),context);
// tick runs the page's half-minute reread now, as the timer would.
async function tick(){const fn=timers.shift();assert(fn,'the page keeps a reread timer while visible');await fn();await context.appFactsReady}
(async()=>{
 await context.appArrived;
 assert.equal(context.document.documentElement.dataset.ready,'1','oaReady marks the page ready once its first reads finished');
 for(const id of ['app-models-status','app-activity-status','app-permissions-status'])assert.equal(el(id).attrs['data-state'],'idle','a successful first read clears Loading: '+id);
 assert.deepEqual(answeredQueries,['/rights/answered?app=example'],'the answers are filtered for this app on the server');
 // Models it uses: the model's name, never a path, and its server's
 // registered title or "Model server", never a raw host.
 const models=el('app-models-list').children;
 assert.deepEqual(models.map(r=>r.label.textContent),['llama','qwen'],'the shared oaModelName names each model');
 assert(models[0].caption.textContent.startsWith('Last used today')&&models[0].caption.textContent.endsWith(' on Engine host'));
 assert(models[1].caption.textContent.endsWith(' on Model server'));
 // Recent activity: a page of five, Show more past it.
 assert.equal(el('app-activity-list').children.length,5);assert.equal(el('app-activity-more').hidden,false);
 assert.equal(el('app-activity-list').children[0].label.textContent,'Notes wants to look up models.','the newest event comes first');
 el('app-activity-more').onclick();
 assert.equal(el('app-activity-list').children.length,7);assert.equal(el('app-activity-more').hidden,true);
 assert.equal(el('app-activity-status').textContent,'Showing all 7 recent events.');
 // Permissions in plain words; a content digest reads as stored content.
 let rows=el('app-permissions-list').children;
 assert.deepEqual(rows.map(r=>r.label.textContent),['Look up models','Inference: complete','Storage: content read','Download in the background','Say when it is running'],'its own rules, then the defaults that apply');
 assert.equal(rows[2].caption.textContent,'Stored content');
 assert(rows[1].caption.textContent.startsWith('local · Until '));
 // Live: the click changes the server's state, the page reads it again,
 // then a timer reread runs; the row element and its sentence survive both.
 const row=rows[0];assert.equal(row.state.textContent,'Allowed');
 const reads=factsReads;
 await assertLiveUpdateKeepsRow({
  click:async()=>{row.toggle.checked=false;await row.toggle.onchange()},
  change:()=>{rules=rules.map(r=>r.Action==='abstraction.model/lookup'?{...r,Expires:new Date(now+7200000).toISOString()}:r)},data:()=>rules,
  tick,row:()=>el('app-permissions-list').children[0],status:r=>r.status,label:'App page, permissions'});
 assert.deepEqual(writes[0],{edit:'set',permit:false,revision:'r1',account,program,action:'abstraction.model/lookup',resource:'hf'});
 assert.equal(rules[0].Permit,false,'the server state changed');assert.equal(factsReads,reads+2,'the click and the timer read the facts again');
 assert.equal(row.state.textContent,'Blocked');assert.equal(row.status.textContent,'Look up models is blocked.','the switch writes one sentence into its row');
 assert(row.caption.textContent.startsWith('hf · Until '),'the reread updated the row in place');
 const activityRow=el('app-activity-list').children[0];await tick();
 assert.strictEqual(el('app-activity-list').children[0],activityRow,'an unchanged reread leaves the activity rows alone');
 assert.equal(el('app-activity-status').textContent,'Showing all 7 recent events.','the Show more sentence survives the reread');
 const expiring=el('app-permissions-list').children[1];expiring.toggle.checked=true;await expiring.toggle.onchange();
 assert(writes[1].ttl_ms>3500000&&writes[1].ttl_ms<=3600000,'a switch keeps the rule\'s remaining lifetime');
 await el('app-permissions-list').children[0].remove.onclick();
 assert.equal(writes[2].edit,'revoke');assert.equal(el('app-permissions-list').children.length,4);
 assert.equal(el('app-permissions-status').textContent,'Look up models was removed.','Remove writes one sentence once its row is gone');
 // A default for all apps that applies: named as such, its switch reachable,
 // and Block saves this app's own block, which wins over the default.
 let perms=el('app-permissions-list').children;
 const download=perms.find(r=>r.label.textContent==='Download in the background');
 assert(download,'a default that lets the app act is listed: '+perms.map(r=>r.label.textContent));
 assert.equal(download.caption.textContent,'Allowed for all apps');assert.equal(download.state.textContent,'Allowed');
 const link=download.children[1].children.find(c=>c.tag==='a');assert.equal(link.href,'/rights?k=key#defaults','the switch of the default is one click away');
 await download.block.onclick();
 assert.deepEqual(writes[3],{edit:'set',permit:false,revision:'r4',account,program,action:'abstraction.job/acceptance.submit',resource:'download-jobs'});
 perms=el('app-permissions-list').children;
 assert(!perms.some(r=>r.caption.textContent==='Allowed for all apps'&&r.label.textContent==='Download in the background'),'the default no longer applies once blocked');
 const blocked=perms.find(r=>r.rule&&r.rule.Action==='abstraction.job/acceptance.submit');
 assert(blocked&&blocked.state.textContent==='Blocked','the block of this app shows with its switch and Remove');
 assert.equal(blocked.status.textContent,'Download in the background is blocked for Example.','Block writes one sentence into the new row of this app');
 assert(perms.some(r=>r.label.textContent==='Say when it is running'&&r.caption.textContent==='Allowed for all apps'),'an every-resource default applies on the own entry of this app');
 // A script host: no default applies, and the page says so in one row.
 scriptHost=true;await tick();
 perms=el('app-permissions-list').children;
 assert(!perms.some(r=>r.caption.textContent==='Allowed for all apps'),'a script host receives no default');
 assert(perms.some(r=>r.label.textContent==='Defaults for all apps'&&r.caption.textContent==='They never apply to this app: it runs scripts.'));
 scriptHost=false;await tick();
 // Answering for you: shown while this app answers, reread live; Revoke
 // keeps the section and its sentence.
 assert.equal(el('app-answering').hidden,false);
 assert.deepEqual(el('app-answered-list').children.map(r=>r.label.textContent),['Notes wants to look up models.']);
 await el('app-answering-revoke').onclick();
 assert.deepEqual(assistantStops,[{action:'stop',program,revision:'r5',alsoRevokeGrants:false}]);
 assert.equal(el('app-answering').hidden,false,'the section stays with its sentence after a revoke');
 assert(el('app-answering-status').textContent.startsWith('Example can no longer answer'));
 assert.equal(el('app-answering-revoke').disabled,true,'the reread shows it no longer answers');
 await tick();assert(el('app-answering-status').textContent.startsWith('Example can no longer answer'),'the sentence survives the reread');
 const section=(...ids)=>({tagName:'SECTION',children:ids.map(el)});
 assertPanelRules({tagName:'MAIN',children:[section('app-models-status','app-models-list'),section('app-permissions-status','app-permissions-list'),section('app-answering-status','app-answered-list'),section('app-activity-status','app-activity-list')]},'The app page',{allowClasses:allowlistedClasses('status')});
 // Start and Remove act on the entry the page showed.
 el('app-start').textContent='Start';await el('app-start').onclick();
 assert.deepEqual(actions[0],['/app/example/start',{confirm:true,shown:'digest-1'}]);assert.equal(el('app-start-status').textContent,'Example is running.');
 await el('app-remove').onclick();
 assert.equal(actions.length,1,'the first Remove click only asks again');assert.equal(el('app-remove-status').textContent,'Select Remove Example again to remove it.');
 await el('app-remove').onclick();
 assert.deepEqual(actions[1],['/app/example/remove',{confirm:true,shown:'digest-1'}]);assert.equal(el('app-remove-status').textContent,'Removed Example from OpenAbstractions.');
 assert(el('app-start').disabled&&el('app-remove').disabled,'a removed app offers no further action');
 if(context.appRun)context.appRun.abort();
 console.log('PASS: The app page shows server-filtered models (names and server titles only), its own permissions with switches and Remove, the defaults that apply with Block, a note for a script host, all surviving rereads, live answering access, activity with Show more, Start and a two-step Remove');
})().catch(e=>{console.error(e);process.exitCode=1});
