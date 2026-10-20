// Run with node monitor/service_page_test.js; no browser or service is started.
// Covers Apps, the Settings runtime support check and rights_page.go
// (requests and saved permissions); work_page_test.js covers Activity.
// Each page's own
// script is self-contained; loadPageScript splices serviceCommonScript back
// in where a page's source only carries the Go "` + serviceCommonScript + `"
// glue, so every page still runs as the one script monitor/*_page_test.js
// conventionally extracts.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const { assertPanelRules } = require('./static/panel_rules.js');
const { allowlistedClasses } = require('./panel_fake_dom_test_helper.js');

// plainScript (monitor/panel_shell.go) is embedded ahead of every page
// fragment in the real document; the Status page's capability rows now call
// oaPlain (task 2026-09-23, fifth first-time visitor: "Work submission:
// Unavailable" carried no plain reason), so this isolated test loads it into
// the same vm context first, the same way monitor/card_page_test.js and its
// siblings already do.
function plainScript() {
 const shellGo = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
 const marker = 'const plainScript = `';
 const start = shellGo.indexOf(marker) + marker.length;
 assert(start > marker.length - 1, 'plainScript constant not found in panel_shell.go');
 const end = shellGo.indexOf('`', start);
 return shellGo.slice(start, end).split('<script>')[1].split('</script>')[0];
}

function commonScript() {
 const src = fs.readFileSync(__dirname + '/service_page.go', 'utf8');
 const marker = 'const serviceCommonScript = `';
 const start = src.indexOf(marker) + marker.length;
 assert(start > marker.length - 1, 'serviceCommonScript not found in service_page.go');
 return src.slice(start, src.indexOf('`', start));
}
// The Apps page (service_page.go) and the app page (app_page.go) carry
// their script in their own constant after serviceCommonScript.
function constantOf(file, name) {
 const src = fs.readFileSync(__dirname + '/' + file, 'utf8'), marker = 'const ' + name + ' = `', start = src.indexOf(marker) + marker.length;
 assert(start > marker.length - 1, name + ' not found in ' + file);
 return src.slice(start, src.indexOf('`', start));
}
function loadPageScript(file) {
 if (file === 'service_page.go') return commonScript() + constantOf(file, 'appsPageScript');
 const go = fs.readFileSync(__dirname + '/' + file, 'utf8');
 const script = go.split('<script>')[1].split('</script>')[0];
 const glue = '` + serviceCommonScript + `';
 assert(script.includes(glue), file + ': serviceCommonScript glue not found');
 return script.replace(glue, commonScript());
}

const fields = new Map();
function node(tag) {
 return {tag:tag||'', tagName:(tag||'div').toUpperCase(), value:'', textContent:'', disabled:false, open:false, listeners:{}, children:[], replaceChildren(){this.children=[]}, append(...v){this.children.push(...v)}, setAttribute(k,v){(this.attrs=this.attrs||{})[k]=String(v)}, removeAttribute(k){if(this.attrs)delete this.attrs[k]}, click(){}, focus(){this.focused=true}, addEventListener(type,fn){(this.listeners[type]=this.listeners[type]||[]).push(fn)}, dispatch(type){for(const fn of this.listeners[type]||[])fn()}};
}
function descendants(n) { return (n.children||[]).flatMap(child=>[child,...descendants(child)]) }
// Saved permissions read by app: a group per app (its heading carries the
// exact program and account on its title), one oa-row per permission, and
// the permission's Details right after its row.
function permissionRows(container=element('rights')) { return descendants(container).filter(n=>n.className==='oa-row'&&n.permission) }
function permissionGroup(row) { const group=descendants(element('rights')).find(n=>(n.children||[]).includes(row)); if(!group) throw Error('permission row is not displayed'); return group }
function appGroup(row) { return descendants(element('rights')).find(n=>n.subject&&descendants(n).includes(row)) }
function rowProgram(row) { return appGroup(row).children[0].title.split(' \u2014 ')[0] }
function rowLabel(row) { return row.children[0].children[0] }
function rowCaption(row) { return row.children[0].children[1] }
function rowState(row) { return row.children[1].children[0] }
function permissionDetails(row) { const group=permissionGroup(row); return group.children[group.children.indexOf(row)+1] }
function editPermission(row) { return row.children[1].children.find(n=>n.tag==='button') }
function removePermission(row) { return row.remove }
// A rule's Details: plain lines, then the raw record in a nested Technical details.
function detailLines(details) { return detailBody(details).children.filter(n=>n.tag==='p').map(n=>n.textContent) }
function detailRaw(details) { const technical=detailBody(details).children.find(n=>n.tag==='details'); return detailBody(technical).textContent }
// A listed question, answer or app question is a wrapper whose first child
// is its oa-row (label, caption; controls) and whose other children are its
// Technical details expanders.
function itemLabel(box) { return box.children[0].children[0].children[0] }
function itemButtons(box) { return box.children[0].children[1].children.filter(c=>c.onclick) }
function detailBody(details) { return details.children.find(n=>n.className==='oa-expander-body') }
function detailNamed(row,label) { return descendants(permissionDetails(row)).find(n=>n.tag==='details'&&n.children[0]?.textContent===label) }
async function until(predicate){for(let i=0;i<200;i++){if(predicate())return;await new Promise(resolve=>setImmediate(resolve))}throw Error('the page script did not progress')}
function element(id) {
 if (!fields.has(id)) fields.set(id, node());
 return fields.get(id);
}
function withoutRegistrationSources(context) {
 const fetch=context.fetch;
 context.fetch=(path,...args)=>path.startsWith('/apps/registrations?')?Promise.resolve({ok:true,json:async()=>({outcome:'unsupported',entries:[]})}):fetch(path,...args);
 return context;
}

(async () => {
 // --- Apps: the registered apps, each opening its app page, and a health banner only on failure. ---
 {
  fields.clear();
  const entry=(name,title,instances)=>({program:'C:\\Apps\\'+name+'.exe',name,role:'application',state:instances+' current announcements',application:{Descriptor:{Name:name,Program:'C:\\Apps\\'+name+'.exe',Title:title},Instances:Array.from({length:instances},(_,i)=>({Instance:'i'+i})),Scope:1}});
  let registry={outcome:'page',cursor:'c1',entries:[entry('zed','Zed editor',0),entry('chat','Chat & Code',1),{program:'C:\\P\\provider.exe',name:'provider',role:'provider'}]};
  let runtime={capabilities:[{name:'Logging',contract:'abstraction.logging/sink@1',status:'resolved'}]};
  const reads=[],waits=[];let held=null;
  const context={URLSearchParams,AbortController,document:{getElementById:element,createElement:node},location:{search:'?k=test key'},console,setTimeout,clearTimeout,
   fetch:async(path,opts={})=>{reads.push(path);
    if(path==='/runtime')return {ok:true,json:async()=>runtime};
    if(path.startsWith('/apps/registrations?')){const q=new URLSearchParams(path.split('?')[1]);assert.equal(q.get('source'),'applications');waits.push(q.get('wait_ms'));
     if(q.get('wait_ms')!=='0')return new Promise((resolve,reject)=>{held={resolve:v=>resolve({ok:true,json:async()=>v}),cursor:q.get('cursor')};opts.signal.addEventListener('abort',()=>reject(Object.assign(Error('aborted'),{name:'AbortError'})),{once:true})});
     return {ok:true,json:async()=>registry}}
    throw Error('unexpected '+path)}};
  context.window=context;vm.runInNewContext(plainScript(),context);vm.runInNewContext(loadPageScript('service_page.go'),context);
  await context.appsHealthReady;await until(()=>held);
  const rows=element('apps').children;
  assert.deepEqual(rows.map(a=>a.children[0].children[0].textContent),['Chat & Code','Zed editor'],'registered applications only, by registered title');
  assert.deepEqual(rows.map(a=>a.className),['oa-row oa-row-nav','oa-row oa-row-nav'],'each app is a navigation row');
  assert.deepEqual(rows.map(a=>a.children[0].children[1].textContent),['Running','Not running']);
  assert.equal(rows[0].href,'/app/chat?k=test%20key','the row opens the app page by its registered name with the Panel key');
  assert(!rows.some(a=>JSON.stringify(a).includes('.exe')),'no program path is page text');
  assert.equal(element('apps-health').hidden,true,'no banner while the runtime and its services work');
  assert.equal(held.cursor,'c1','the list then follows the registry from its cursor');assert.deepEqual(waits,['0','30000']);
  registry={outcome:'page',cursor:'c2',entries:[]};held.resolve(registry);held=null;await until(()=>held);
  assert.equal(element('apps').children[0].className,'oa-empty');assert.equal(element('apps').children[0].textContent,'No apps have registered yet.','an empty list shows one sentence');
  runtime={capabilities:[{name:'Work submission',contract:'abstraction.job/acceptance@1',status:'unavailable'}]};
  await context.appsCheckRuntime();
  assert.equal(element('apps-health').hidden,false,'a failing service shows the banner');
  assert.equal(element('apps-health-label').textContent,"The runtime is running but doesn't provide background work on this account.");
  await element('apps-health-retry').onclick();
  assert(/^Checked today at .*; the problem remains\.$/.test(element('apps-health-status').textContent),'Check again writes one sentence');
  runtime={capabilities:[]};await element('apps-health-retry').onclick();
  assert.equal(element('apps-health').hidden,false);assert.equal(element('apps-health-label').textContent,'OpenAbstractions works again.');
  assert(element('apps-health-status').textContent.endsWith('everything works.'));
  runtime={error:'dial failed'};await context.appsCheckRuntime();
  assert.equal(element('apps-health-label').textContent,'OpenAbstractions is not running. Start it, then check again.');
  context.appsRun.abort();
  console.log('PASS: Apps lists registered apps as rows opening their app pages, follows the registry, and shows a health banner only on failure');
 }

 // --- Apps names a registry it cannot read in the banner and keeps trying. ---
 {
  fields.clear();let outcome='forbidden';const timers=[];
  const context={URLSearchParams,AbortController,document:{getElementById:element,createElement:node},location:{search:'?k=test'},console,
   setTimeout(fn){timers.push(fn);return timers.length},clearTimeout(){},
   fetch:async path=>{if(path==='/runtime')return {ok:true,json:async()=>({capabilities:[]})};
    if(path.startsWith('/apps/registrations?'))return {ok:true,json:async()=>outcome==='page'?{outcome,cursor:'c',entries:[]}:{outcome,entries:[]}};throw Error('unexpected '+path)}};
  context.window=context;vm.runInNewContext(plainScript(),context);vm.runInNewContext(loadPageScript('service_page.go'),context);
  await context.appsHealthReady;await until(()=>timers.length);
  assert.equal(element('apps-health').hidden,false);assert.equal(element('apps-health-label').textContent,'This Panel may not read the list of apps.');
  assert.equal(element('apps-status').textContent,'This Panel may not read the list of apps.');
  outcome='page';await element('apps-health-retry').onclick();
  assert.equal(element('apps').children[0].textContent,'No apps have registered yet.');
  assert.equal(element('apps-health-label').textContent,'OpenAbstractions works again.','a retry that clears the failure says so');
  context.appsRun.abort();timers.shift()();
  console.log('PASS: Apps shows an unreadable registry in the banner and recovers through Check again');
 }

 // --- Runtime diagnostics stay available on demand in Settings. ---
 {
  fields.clear();let requests=0;
  const src=fs.readFileSync(__dirname+'/service_page.go','utf8'),marker='const runtimeSupportScript = `',start=src.indexOf(marker)+marker.length;
  const support=src.slice(start,src.indexOf('`',start));
  const context={URLSearchParams,document:{getElementById:element,createElement:node},location:{search:'?k=test'},console,
   fetch:async(path)=>{requests++;assert.equal(path,'/runtime');return {ok:true,json:async()=>({explicit:{program:'/opt/openabstractions/openabstractions',endpoint:'//./pipe/openabstractions-user-S-1-5-21-3833002460-2194338898-3510754826-1001-panel9-runtime'},capabilities:[{name:'Work submission',contract:'abstraction.job/acceptance@1',status:'unavailable',label:'Unavailable'},{name:'Logging',contract:'abstraction.logging/sink@1',status:'resolved',label:'Ready'}]})}}};
  context.window=context;vm.runInNewContext(plainScript(),context);vm.runInNewContext(commonScript()+support,context);
  context.runtimeSupportStart();
  assert.equal(requests,0,'the support check waits for an explicit click');
  await element('status').onclick();
  const shown=element('runtime').children.map(c=>c.textContent||'').join(' | ');
  assert(shown.includes('Connected to OpenAbstractions.'));
  assert(!shown.includes('S-1-5-21')&&!shown.includes('/opt/openabstractions'));
  assert(!shown.includes('Work submission')&&!shown.includes('Logging: Ready'),'service diagnostics stay within support details');
  const details=element('runtime').children.find(c=>c.children&&c.children[0]&&c.children[0].textContent==='Technical details');
  assert(details.children.some(c=>c.textContent.includes('/opt/openabstractions/openabstractions')));
  assert(details.children.some(c=>c.textContent.includes("The runtime is running but doesn't provide background work on this account.")));
  assert(element('check-status').textContent.startsWith('Checked today at '));
  const settings=fs.readFileSync(__dirname+'/settings_page.go','utf8');
  assert(settings.includes('id="runtime-support"')&&settings.includes('runtimeSupportScript + '),'Settings owns the existing diagnostic control');
  console.log('PASS: Runtime support checks only on request and retains the existing plain status and technical evidence');
 }

 // --- Rights page (rights_page.go): questions and rights. ---
 {
  fields.clear();
  const firstUseRule = {Subject:{Account:'alice',Program:'/usr/bin/python3'},Action:'abstraction.job/acceptance.submit',Resource:'abstraction.job/acceptance@1',Permit:true};
  const questionReplies = {answer:[{Outcome:'forbidden'},{Outcome:'answered',Record:{ID:'q1'}},{Outcome:'answered',Record:{ID:'q2'},rule:firstUseRule,edit:{Outcome:'applied',Revision:'rev-2'}},{Outcome:'answered',Record:{ID:'q2'},rule:{...firstUseRule,Permit:false},edit:{Outcome:'conflict',Revision:'rev-3'}},{Outcome:'answered',Record:{ID:'q2'},rule:firstUseRule,edit:{Outcome:'applied',Revision:'rev-4'}}], retire:[{Outcome:'unavailable'},{Outcome:'retired'}]};
  // task 2026-09-23, thirteenth first-time visitor, finding 7: a first-use
  // question's own server-filled Text read as "<full path> wants to
  // <raw action id> on <resource>" unchanged. The listing now carries the
  // native prompt's sentence and button labels (live walkthrough 2026-10-02,
  // D5; rights_page_handler_test.go checks them against the real handler).
  const firstUseProgram = 'C:\\Users\\alice\\AppData\\Local\\Programs\\OpenAbstractions\\tools\\openabstractions.exe';
  const firstUseSentence = 'openabstractions (not registered) wants to look up models in the model registry \u201cnosuchregistry\u201d.';
  let questionPage = {Outcome:'page', Records:[{ID:'q4',Text:'A recorded answer',Options:['allow','refuse'],Option:'allow'}, {ID:'q1', Text:'Allow download?', Options:['once','refuse'], Option:''}, {ID:'q3', Text:firstUseProgram+' wants to abstraction.model/lookup on nosuchregistry', About:firstUseProgram, Options:['allow','refuse'], Option:''}], Next:'', Complete:true,
   sentences:{q3:firstUseSentence}, choices:{q1:[{label:'Allow once',option:'once'},{label:'Refuse',option:'refuse'}], q3:[{label:'Allow',option:'allow'},{label:'Refuse',option:'refuse'}]}};
  const rule = {Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.storage/content.read',Resource:'sha256:x',Permit:true,SetBy:{Account:'alice',Program:'/usr/bin/openabstractions'},SetAt:'2026-09-17T10:00:00.000Z',Expires:'2026-12-01T10:00:00.000Z'};
  const dupA = {Subject:{Account:'alice',Program:'/usr/bin/app'},Action:'abstraction.model/lookup',Resource:'hf',Permit:true,SetBy:{Account:'alice',Program:'/usr/bin/openabstractions'},SetAt:'2026-09-17T10:00:00.000Z'};
  const dupB = {...dupA, Resource:'civitai'};
  // An ordinary operator can enter the same reason used by runtime seeds.
  // Its recorded setter remains visible and the reason conveys no origin.
  const installedA = {Subject:{Account:'root',Program:'/usr/bin/openabstractions'},Action:'abstraction.facade/application.read',Resource:'app:comfyui',Permit:true,Why:'installation',SetBy:{Account:'root',Program:'/usr/bin/ordinary-operator'},SetAt:'2026-09-17T10:00:00.000Z'};
  const installedB = {...installedA, Action:'abstraction.facade/application.activate'};
  // The runtime's other program: OpenAbstractions' own, so it shares the group.
  const installedC = {...installedA, Subject:{Account:'root',Program:'/usr/bin/openabstractionsw'}};
  const exploreSelfReply = {self:{account:'root',program:'/usr/bin/openabstractions'},selfAccountName:'alice',probes:[]};
  let rightsPage = {Outcome:'page', Names:{'/usr/bin/openabstractions':'OpenAbstractions Panel','/usr/bin/openabstractionsw':'OpenAbstractions'}, Revision:'sha256:e3b0c44298fc1c149afbf4c8996fb924', Catalog:['abstraction.storage/content.read','abstraction.model/lookup','abstraction.inference/selection.manage_for'], ActionPlain:{'abstraction.model/lookup':'model resolve'}, Rules:[rule, dupA, dupB, installedA, installedB, installedC], Next:'', Complete:true};
  const rightsCalls = [];
  const allowRules = [{action:'abstraction.job/acceptance.submit',resource:'abstraction.job/acceptance@1'},{action:'abstraction.model/lookup',resource:'hf'}];
  const allowCalls = [], allowReplies = [{rules:allowRules,outcome:'conflict',revision:'rev-9',landed:[allowRules[0]],stopped:allowRules[1]},{rules:allowRules,outcome:'applied',revision:'rev-10',landed:allowRules}];
  const delegationCalls=[];
  const untrustedResource='nosuchregistry<img src=x onerror=alert(1)>';
  const delegationPage={outcome:'page',revision:'delegate-rev-1',account:'alice',questions:[{id:'q3',text:firstUseProgram+' wants to abstraction.model/lookup on '+untrustedResource,program:firstUseProgram,action:'abstraction.model/lookup',resource:untrustedResource,options:['allow','refuse'],sentence:firstUseSentence}]};
  // task 2026-09-23, twelfth first-time visitor, requirement 2: Save
  // rule's own Program path field offers a dropdown of the registered
  // applications and every program a rule already names. The graphics
  // card's holders are running processes, system ones among them, and are
  // never offered (research/panel-redesign/SPEC.md §7).
  const cardTableReply = {resources:[{resource:'card:0', holders:[{program:'C:\\Windows\\System32\\csrss.exe'},{program:'C:\\Windows\\System32\\dwm.exe'}]}]};
  const registeredApps = {outcome:'page',entries:[{program:'/usr/bin/python3',name:'',role:'application'},{program:'C:\\Apps\\Editor.exe',name:'Editor',role:'application'}]};
  let rightsGetCount=0,rightsReadbackFails=false;
  const context = {URLSearchParams, localStorage:{length:0,key(){},getItem(){return null},setItem(){}}, document:{getElementById:element, createElement:node,createTextNode:text=>({textContent:text})}, location:{search:'?k=test'}, console,
   fetch:async(path,opts)=>{
    if(path==='/card/table') return {ok:true,json:async()=>cardTableReply};
    if(path==='/apps/registrations?source=applications&wait_ms=0') return {ok:true,json:async()=>registeredApps};
    if(path==='/explore') return {ok:true,json:async()=>exploreSelfReply};
    if(path==='/rights/allow'){allowCalls.push(JSON.parse(opts.body));return {ok:true,json:async()=>allowReplies.shift()}}
    // Defaults for all apps is its own list, read after each rules read.
    if(path==='/rights/defaults')return {ok:true,json:async()=>({Outcome:'page',Revision:'defaults-rev',Defaults:[]})};
    if(path.startsWith('/rights/requests')) return {ok:true,json:async()=>({outcome:'page',requests:[],complete:true})};
    if(path==='/rights/delegation'){if(opts.body){delegationCalls.push(JSON.parse(opts.body));return {ok:true,json:async()=>({Outcome:'applied',Revision:'delegate-rev-2',Current:{Subject:{Account:'alice',Program:'C:/Tools/OpenCode.exe'},Action:'abstraction.asks/question.answer',Resource:'first-use@1:[scope]',Permit:true}})}}return {ok:true,json:async()=>delegationPage}}
    if(path.startsWith('/rights')){
     if(!opts.body){rightsGetCount++;return {ok:true,json:async()=>rightsReadbackFails?{Outcome:'unavailable'}:rightsPage}}
     const edit=JSON.parse(opts.body);rightsCalls.push(edit);
     const current={Subject:{Account:edit.account,Program:edit.program},Action:edit.action,Resource:edit.resource,Permit:edit.permit};
     rightsPage={...rightsPage,Revision:'rev-next',Rules:rightsPage.Rules.filter(r=>r.Subject.Account!==edit.account||r.Subject.Program!==edit.program||r.Action!==edit.action||r.Resource!==edit.resource)};
     if(edit.edit==='set')rightsPage.Rules.push(current);
     return {ok:true,json:async()=>({Outcome:'applied',Revision:'rev-next',Current:edit.edit==='set'?current:null})};
    }
    if(path.startsWith('/questions')){
     if(!opts.body) return {ok:true,json:async()=>questionPage};
     const action=JSON.parse(opts.body);
     return {ok:true,json:async()=>questionReplies[action.action].shift()};
    }
    throw Error('unexpected request '+path);
   }};
  context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('rights_page.go'), context);
  // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3:
  // Questions and Rules both used to open empty until their own "Show"
  // button was clicked, with the Action dropdown empty and the Program
  // and Account choosers unfed until then too. Both now read on arrival
  // (questionsReady, rightsListReady); "Show questions" and "Show rules"
  // are renamed Refresh.
  await context.questionsReady;
  await context.rightsDeviceHoldersReady;
  await context.rightsSelfReady;
  await context.rightsListReady;
  await context.delegationReady;

  assert.deepEqual(element('rights-action').children.map(o=>o.value),['abstraction.storage/content.read','abstraction.model/lookup','abstraction.inference/selection.manage_for'],'catalogue offers the grant actions on arrival, with no click');
  assert.equal(element('questions-history-list').children.length,1,'answered requests are separate from requests waiting for an answer');
  assert.equal(itemLabel(element('questions-history-list').children[0]).textContent,'A recorded answer');
  const box=element('questions-list').children[0];
  const buttons=itemButtons(box);
  assert.deepEqual(buttons.map(b=>b.textContent),['Allow once','Refuse','Dismiss'],'the pending question is already listed on arrival, with no click, its buttons labelled as the native prompt labels them');
  // task 2026-09-23, thirteenth first-time visitor, finding 7, and live
  // walkthrough 2026-10-02, D5: the first-use question reads in the
  // native prompt's sentence, which the listing carries.
  const firstUseBox = element('questions-list').children[1];
  const firstUseText = itemLabel(firstUseBox);
  assert.equal(firstUseText.textContent.split('\n')[0], firstUseSentence, 'the line is the listing\'s sentence, not the raw path or action id');
  assert.equal(firstUseText.title, firstUseProgram, 'the full path sits on the line\'s own title instead');
  const firstUseDetails = firstUseBox.children[1];
  assert.equal(firstUseDetails.children[0].textContent, 'Technical details');
  assert.equal(detailBody(firstUseDetails).textContent, firstUseProgram+' wants to abstraction.model/lookup on nosuchregistry', 'the server\'s own raw sentence also sits behind Technical details');
  assert.deepEqual(element('delegation-question').children.map(o=>o.value),['','q3'],'the setup offers only a pending first-use request returned by the public question API');
  assert.equal(element('delegation-question').children[1].textContent,firstUseSentence,'the request reads in the native prompt\'s sentence the reply carries (D5)');
  element('delegation-question').value='q3';element('delegation-question').onchange();
  assert(element('delegation-question-details').textContent.includes('<img src=x onerror=alert(1)>'),'request details render untrusted scope as text');
  element('delegation-option').value='allow';element('delegation-option').onchange();
  element('delegation-program').value='C:/Tools/OpenCode.exe';element('delegation-program').oninput();
  element('delegation-confirm').checked=true;element('delegation-confirm').onchange();
  const instructions=fs.readFileSync(__dirname+'/rights_page.go','utf8').split('<script>')[0];
  assert(instructions.includes('<p>One app may give one answer to one waiting request, for five minutes.</p>'),'one-request answering carries one short caption');
  assert(!instructions.includes('select bun.exe'),'the instructions do not recommend an answerer the service refuses');
  assert(!element('delegation-effect').textContent.includes('Bun binary'),'the grant effect does not promise shared script-host answering access');
  await element('delegation-form').onsubmit({preventDefault(){}});
  assert.deepEqual(delegationCalls,[{questionId:'q3',responder:'C:/Tools/OpenCode.exe',option:'allow',revision:'delegate-rev-1',confirmExactUse:true}],'the explicit grant binds the selected executable, exact question and answer, current policy revision, and confirmation');
  assert(element('delegation-status').textContent.includes('responder still must make that answer'),'grant confirmation does not imply the question was already answered');

  // task 2026-09-23, fourteenth first-time visitor, finding 3: answering a
  // question used to say "... List again to refresh." while the list
  // under it kept its stale rows; it now reads the list again itself.
  await buttons[0].onclick();
  assert(element('questions-status').textContent.includes('not authorized'),'forbidden answer is shown as a refusal');
  await buttons[0].onclick();
  assert(element('questions-status').textContent.includes('Answer recorded'));
  assert(!element('questions-status').textContent.includes('List again'), 'the reply no longer tells the person to list again; the list already refreshed itself before this text was shown');
  assert(element('questions-list').children.length>0, 'the list stayed rendered (refreshed, not cleared) after a successful answer');
  // A reread while the page is visible keeps the rows shown and the
  // sentence the click wrote (the S5 review rule); passes never overlap.
  const shownBox=element('questions-list').children[0];await Promise.all([context.rightsLive(),context.questions()]);
  assert.strictEqual(element('questions-list').children[0],shownBox,'an unchanged reread keeps the question rows');
  assert(element('questions-status').textContent.includes('Answer recorded'),'the sentence survives the reread');
  for(const id of ['questions-status','choices-status','rights-status'])element(id).role='status';
  assertPanelRules({tagName:'MAIN',children:[{tagName:'SECTION',children:['questions-status','questions-list','choices-status','choices-list'].map(element)},{tagName:'SECTION',children:['rights-status','rights'].map(element)}]},'Permissions',{allowClasses:allowlistedClasses('rights'),
   // Known values: testdata/panel_lint.allowlist's known-value rows for
   // Permissions, each naming its owning fix.
   knownValues:require('./panel_fake_dom_test_helper.js').knownValues('rights'),
   knownSummaries:require('./panel_fake_dom_test_helper.js').knownSummaries('rights'),knownExact:true});

  // task 2026-09-23, twelfth first-time visitor, finding 6: Save rule's own
  // Account field opened blank and required with no default, while Allow's
  // quick-grant form already defaults its account to this one. It now
  // offers a chooser defaulting to this account, prefilled, on arrival,
  // "Other…" for a different one.
  assert.deepEqual(element('rights-account-select').children.map(o=>[o.value,o.textContent]),[['root','this account, alice'],['__other__','Other…']],'the chooser defaults to this account by name, Other… last');
  assert.equal(element('rights-account-select').children[0].title,'root','the raw account value stays reachable as the option\'s own title');
  assert.equal(element('rights-account').hidden,true,'the typed field stays hidden while the known default is selected');
  assert.equal(element('rights-account').value,'root','the typed field is kept in sync with the known default, so the existing submit handler keeps reading it unchanged');
  element('rights-account-select').value='__other__';
  element('rights-account-select').onchange();
  assert.equal(element('rights-account').hidden,false,'choosing Other… reveals the typed field');
  assert.equal(element('rights-account').value,'','choosing Other… clears the typed field for a fresh entry');
  element('rights-account-select').value='root';
  element('rights-account-select').onchange();
  assert.equal(element('rights-account').hidden,true,'choosing the known default again hides the typed field');
  assert.equal(element('rights-account').value,'root');

  // task 2026-09-23, twelfth first-time visitor, requirement 2: on
  // arrival, Save rule's Program dropdown already offers every program
  // the Device page's own resource table names as a holder merged with
  // every program a loaded rule already names (allowSeen), each shown by
  // file name with the full path on its title, sorted, Other… last.
  assert.deepEqual(element('rights-program-select').children.map(o=>o.value),['/usr/bin/app','C:\\Apps\\Editor.exe','/usr/bin/openabstractionsw','/usr/bin/openabstractions','/usr/bin/python3','__other__'],'the dropdown merges the registered applications and every program a loaded rule already names, on arrival, sorted by name with Other\u2026 last');
  assert.equal(element('rights-program-select').children[1].textContent,'Editor','a registered application reads by its registered name');
  assert.equal(element('rights-program-select').children[4].title,'/usr/bin/python3','the full path stays reachable as the option\'s own title');
  assert(!element('rights-program-select').children.some(o=>/csrss|dwm/i.test(o.value+o.textContent)),'running system processes are never offered');
  element('rights-program-select').value='/usr/bin/app';
  element('rights-program-select').onchange();
  assert.equal(element('rights-program').hidden,true,'choosing a known program keeps the typed field hidden');
  assert.equal(element('rights-program').value,'/usr/bin/app','choosing a known program syncs the typed field\'s own value, so the existing submit handler keeps reading it unchanged');
  element('rights-program-select').value='__other__';
  element('rights-program-select').onchange();
  assert.equal(element('rights-program').hidden,false,'choosing Other\u2026 reveals the typed field');
  assert.equal(element('rights-program').value,'','choosing Other\u2026 clears the typed field for a fresh entry');

  // task 2026-09-23, fifteenth first-time visitor, finding 6: Resource
  // opened as a bare free-text field with no hint what to type. It now
  // offers a dropdown built from the resources rules already loaded here
  // name for whichever action is chosen (rule and dupA/dupB above,
  // Storage content read on sha256:x, Model resolve on hf and civitai).
  element('rights-action').value='abstraction.storage/content.read';
  element('rights-action').onchange();
  assert.deepEqual(element('rights-resource-select').children.map(o=>o.value),['sha256:x','__other__'],'the resource chooser offers only the resources this one action\'s own loaded rules named');
  assert.equal(element('rights-resource').hidden,true,'a known resource keeps the typed field hidden');
  assert.equal(element('rights-resource').value,'sha256:x','the typed field stays in sync with the chosen resource, so the existing submit handler keeps reading it unchanged');
  element('rights-action').value='abstraction.model/lookup';
  element('rights-action').onchange();
  assert.deepEqual(element('rights-resource-select').children.map(o=>o.value),['civitai','hf','__other__'],'a different action offers that action\'s own resources instead, sorted, Other\u2026 last');
  element('rights-resource-select').value='__other__';
  element('rights-resource-select').onchange();
  assert.equal(element('rights-resource').hidden,false,'choosing Other\u2026 reveals the typed field');
  assert.equal(element('rights-resource').value,'','choosing Other\u2026 clears the typed field for a fresh entry');

  const rows=permissionRows().filter(row=>rowProgram(row)==='/usr/bin/app');
  assert.equal(rows.length,3,'every rule for this program is shown');
  assert.equal(rowLabel(rows[0]).textContent,'Storage: content read');
  assert(rowCaption(rows[0]).textContent.startsWith('Stored content \u00b7 Until '),'a content digest reads as stored content');assert.equal(rowState(rows[0]).textContent,'Allowed');
  assert(rowCaption(rows[0]).title.endsWith(rule.Expires),'expiry remains directly available on the row');
  assert(!rowLabel(rows[0]).textContent.includes('abstraction.storage/content.read'),'capabilities retain plain labels');
  assert(rowCaption(rows[1]).textContent.startsWith('hf \u00b7'));assert(rowCaption(rows[2]).textContent.startsWith('civitai \u00b7'),'repeated actions retain distinct exact targets');
  const details=permissionDetails(rows[0]);
  assert.equal(details.tag,'details');assert.equal(details.className,'oa-expander');assert.equal(details.children[0].tag,'summary','the disclosure retains native keyboard controls');
  assert(!descendants(rows[0]).some(n=>n.tag==='details'),'details sit below their row, not inside it');
  // task 2026-09-23, eighth first-time visitor, finding 2: even truncated to
  // its first 8 hex characters, a revision is an identifier; it is no
  // longer visible text at all, only the rules list heading's own title and
  // this section's Technical details.
  const status = element('rights-status');
  assert(!status.children.some(c=>c.className==='oa-muted'&&/^Rules revision/.test(c.textContent)),'no visible line names the revision');
  assert.equal(element('rights-heading').title,'sha256:e3b0c44298fc1c149afbf4c8996fb924','the full revision stays reachable as the rules list heading\'s title');
  const revisionDetails = element('rights-revision');
  assert(!element('rights').children.some(n=>n.revision),'no Technical details inside the list of app groups');
  assert.equal(revisionDetails.hidden,false,'the section\'s Technical details show once the list is read');
  assert(element('rights-revision-body').textContent.includes('sha256:e3b0c44298fc1c149afbf4c8996fb924'),'the full revision also sits behind a Technical details disclosure');
  const sectionMarkup=fs.readFileSync(__dirname+'/rights_page.go','utf8').split('<script>')[0];
  assert(/oaExpander\("Filter by capability"[\s\S]*id="rights-revision"[^>]*>[\s\S]*?<\/details>` \+\n\t`<\/section>`/.test(sectionMarkup),'it is the section\'s last disclosure, after the groups and the section\'s rows');
  assert(!status.children.some(c=>c.tag==='details'),'no Technical details above the rows');
  assert(!status.textContent.includes('More rules remain') && !status.textContent.includes('End of rules'), 'the inert remain/end sentence is dropped; the rights-next button already carries that state');
  assert.equal(element('rights-next').hidden,true,'the list is complete, so Show more is hidden');
  const installedEntry=descendants(element('rights')).find(n=>n.installed);
  assert(installedEntry&&!installedEntry.open,'rules OpenAbstractions set up stay in a closed native disclosure');
  assert.equal(installedEntry.children[0].textContent,'Set up by OpenAbstractions (3)','one disclosure counts the rules set up for every OpenAbstractions program, never the reason code');
  assert.equal(descendants(element('rights')).filter(n=>n.installed).length,1,'OpenAbstractions\' own programs show one Set up disclosure, not one per program');
  for(const details of [installedEntry])assert(details.children[0].children.some(c=>c.className==='oa-row-chevron'),'an expander\'s summary carries the shared chevron: '+details.children[0].textContent);
  const installedGroup=appGroup(permissionRows(installedEntry)[0]);
  assert.equal(installedGroup.children[0].textContent,'OpenAbstractions','OpenAbstractions\' own programs share one group under the product name');
  assert.equal(installedGroup.className,'oa-group','an app group is an oa-group, so every skin draws it as a grouped list');
  assert(installedGroup.children.slice(1).every(n=>n.className==='oa-row'||n.className==='oa-expander'),'its rows and expanders are its direct children');
  assert(installedGroup.children.includes(installedEntry),'the disclosure sits in its app\'s group, below its rows');
  for(const id of ['rights-status'])element(id).role='status';
  assertPanelRules({tagName:'SECTION',children:[element('rights-status'),element('rights')]},'Saved permissions',{allowClasses:allowlistedClasses('rights')});
  assert.equal(permissionRows(installedEntry).length,3,'installation-reason rules remain manageable');
  for(const row of permissionRows(installedEntry)) {
   assert(!/abstraction\./.test(rowLabel(row).textContent),'the capability label stays plain');
   assert(rowCaption(row).textContent.startsWith('app:comfyui'));
   const details=permissionDetails(row),record=detailRaw(details);
   assert.deepEqual(detailLines(details),['For '+(row.permission.Subject.Program==='/usr/bin/openabstractionsw'?'OpenAbstractions':'OpenAbstractions Panel')+' on this account.','Set up by OpenAbstractions.','Stays until removed.'],'Details name the rule\'s own program and account in plain words, never the reason code');
   assert(!details['data-oa-technical']&&!(details.attrs||{})['data-oa-technical'],'the Details expander itself is no technical block');
   assert(record.includes('Via application: /usr/bin/ordinary-operator')&&record.includes('Set at: 2026-09-17T10:00:00.000Z'),'the full receiving setter path and timestamp remain available');
  }

  // task 2026-09-23, eighth first-time visitor, finding 4: Save rule with a
  // blank required field also showed no result at all.
  element('rights-account').dispatch('invalid');
  assert.equal(element('rights-status').textContent,'Account is required.','a blank required Save rule field is named');
  element('rights-program').dispatch('invalid');
  assert.equal(element('rights-status').textContent,'Program path is required.');
  element('rights-resource').dispatch('invalid');
  assert.equal(element('rights-status').textContent,'Resource is required.');

  // task 2026-09-23, fourteenth first-time visitor, findings 2 and 3: Save
  // rule used to refuse ("List rules before editing.") unless Show rules
  // had been clicked first, even though rightsList already read it on
  // arrival above, and its own reply told the person to list again while
  // the table under it kept its stale rows. Neither happens now: the
  // fixture's account/program/action/resource fields are all already
  // filled from the arrival state and the earlier chooser checks.
  const rightsGetCountBeforeEdit=rightsGetCount;
  assert(instructions.indexOf('id="rights"')<instructions.indexOf('id="rights-editor"')&&instructions.indexOf('id="rights-editor"')<instructions.indexOf('id="defaults"'),'the existing saved list and editor share the Saved permissions section');
  assert(instructions.includes('Choose Other… to enter an account, app path or exact resource'),'custom exact targets are explained beside the form');
  assert(instructions.includes('>Advanced editor…</button>')&&instructions.includes('>Advanced permission editor<span'),'the raw exact editor is explicitly administrative, not a normal permission request');
  element('rights-program').value='/usr/bin/newapp';
  element('rights-resource').value='sha256:y';
  await element('rights-grant').onsubmit({preventDefault(){}});
  assert.equal(rightsCalls[0].account,'root');
  assert(!element('rights-status').textContent.includes('List rules before editing'),'no precondition blocked the edit; rightsList already ran on arrival');
  assert(!element('rights-status').textContent.includes('List again'),'the confirmation no longer tells the person to list again');
  assert(element('rights-status').textContent.startsWith('Permission added'),'the edit confirms its authoritative readback');
  assert(permissionRows().some(row=>row.permission.Subject.Program==='/usr/bin/newapp'&&row.permission.Resource==='sha256:y'),'the added rule is visible beside its editor');
  assert.equal(element('rights-submit').textContent,'Update permission','the saved exact key now offers Update');
  assert.equal(rightsGetCount,rightsGetCountBeforeEdit+1,'a successful edit reads the rules list again itself');
  element('rights-permit').value='deny';
  await element('rights-grant').onsubmit({preventDefault(){}});
  assert(element('rights-status').textContent.startsWith('Permission updated'),'editing an existing key confirms Update');
  assert(permissionRows().some(row=>row.permission.Subject.Program==='/usr/bin/newapp'&&!row.permission.Permit),'readback shows the updated decision');
  const desired={Subject:{Account:'root',Program:'/usr/bin/absent'},Action:'notify',Resource:'account',Permit:true};
  assert(!context.rightsReadbackMatches({revision:'saved',complete:true,rules:[]},{Revision:'saved',Current:desired},{edit:'set'}),'an applied reply without its stored rule cannot claim success');
  assert(!context.rightsReadbackMatches({revision:'saved',complete:true,rules:[]},{Revision:'saved'},{edit:'set'}),'an applied set with no authoritative Current cannot claim success');
  assert(!context.rightsReadbackMatches({revision:'different',complete:true,rules:[desired]},{Revision:'saved',Current:desired},{edit:'set'}),'a changed revision cannot confirm the saved result');
  const request={edit:'set',account:'root',program:'/usr/bin/absent',action:'notify',resource:'account',permit:true};
  const confirms=(edit,current,rules=[current],extra={})=>context.rightsReadbackMatches({revision:'saved',complete:true,rules},{Revision:'saved',Current:current,...extra.reply},edit,extra.context||{});
  assert(confirms(request,desired),'exact request/reply/list identity confirms');
  for(const current of [{...desired,Subject:{...desired.Subject,Program:'/another/app'}},{...desired,Permit:false},{},null])assert(!confirms(request,current,current?[current]:[]),'foreign key, wrong choice and malformed Current cannot confirm');
  const profile='abstraction.credentials/named-use@2',action='abstraction.credentials/apply';
  const selection={Permission:{Action:action,ResourceProfile:profile},Scope:'one',Name:'alpha',Target:'alpha'};
  const typed={...desired,Action:action,Resource:'alpha',Selector:selection};
  const typedRequest={...request,action,resource_profile:profile,scope:'one',credential_name:'alpha'};delete typedRequest.resource;
  assert(confirms(typedRequest,typed),'known native selector metadata confirms the requested target');
  for(const selector of [{...selection,Permission:{...selection.Permission,ResourceProfile:'other/profile'}},{...selection,Scope:'all'},{...selection,Name:'beta',Target:'beta'},{...selection,Target:'beta'},{...selection,Origin:{ID:'foreign',BundleRef:'foreign',Digest:'wrong'}}]){
   const wrong={...typed,Selector:selector};assert(!confirms(typedRequest,wrong),'wrong profile/scope/name/target/origin cannot confirm even when reply and list agree');
  }
  assert(!confirms(typedRequest,typed,[{...typed,Selector:{...selection,Target:'beta'}}]),'reply/list target collision is rejected');
  assert(!confirms({...typedRequest,edit:'revoke'},[],[typed]),'typed removal remains unconfirmed while its target is listed');
  assert(confirms({...typedRequest,edit:'revoke'},[],[]),'typed removal confirms complete-list target absence');
  const origin={ID:'original',BundleRef:'fixed-bundle',Digest:'definition'};
  const linked={...typed,Selector:{...selection,Origin:origin}};
  const removeOrigin={edit:'remove_bundle',account:'root',program:'/usr/bin/absent',bundle_origin_id:'original'};
  const originContext={origin,originSubject:typed.Subject};
  assert(!confirms(removeOrigin,[],[linked],{context:originContext}),'bundle removal cannot confirm while any linked original remains');
  assert(confirms(removeOrigin,[],[],{context:originContext}),'bundle removal confirms complete linked-origin absence');
  assert(!confirms(removeOrigin,[],[],{context:{...originContext,origin:{...origin,ID:'wrong'}}}),'removal context binds the selected origin');
  assert(!confirms(removeOrigin,[],[],{context:{...originContext,originSubject:{...typed.Subject,Program:'/other'}}}),'removal origin context binds the requested subject');
  const bundle={BundleRef:'fixed-bundle',DefinitionDigest:'definition',Members:[selection.Permission]};
  const bundleRequest={...typedRequest,bundle_ref:bundle.BundleRef};delete bundleRequest.resource_profile;delete bundleRequest.action;
  assert(confirms(bundleRequest,[linked],[linked],{context:{bundle},reply:{BundleOrigin:origin}}),'fixed bundle member and origin metadata confirm');
  assert(!confirms(bundleRequest,[linked],[linked],{context:{bundle},reply:{BundleOrigin:{...origin,ID:'wrong'}}}),'wrong bundle origin cannot confirm');
  rightsReadbackFails=true;
  await element('rights-grant').onsubmit({preventDefault(){}});
  assert(element('rights-status').textContent.includes('saved state could not be confirmed'),'a successful write with failed readback gets an explicit unconfirmed result');
  rightsReadbackFails=false;await context.rightsList();

  const customAction='opencode/log-source.share',customRule={Subject:{Account:'alice',Program:'/usr/bin/actual-caller'},Action:customAction,Resource:'exact-source',Permit:true};
  const declared={Action:customAction,FeatureApp:{Account:'feature account',Program:'/usr/bin/feature-app'},AppLabel:'OpenCode',Description:'Share event contents <img src=x onerror=alert(1)>',DeclaredRisk:'future-risk'};
  rightsPage={Outcome:'page',Revision:'custom-revision',Catalog:[customAction],ActionPlain:{[customAction]:declared.AppLabel+': '+declared.Description},Descriptions:{[customAction]:{Action:customAction,Description:declared,PublishedBy:{Account:'publisher account',Program:'/usr/bin/publisher'},PublishedAt:'2026-10-05T12:00:00.000Z'}},Rules:[customRule],Next:'',Complete:true};
  await context.rightsList();
  const customRow=permissionRows()[0],descriptionDetails=detailNamed(customRow,'Action description');
  assert.equal(descriptionDetails.children[0].textContent,'Action description');
  const descriptionText=descriptionDetails.children[1].textContent;
  assert(descriptionText.includes('<img src=x onerror=alert(1)>'),'declarations are rendered as text');
  assert(descriptionText.includes('Feature declared for feature-app')&&descriptionText.includes('Published by publisher'),'feature app and established publisher remain separate');
  assert(descriptionText.includes('Declared risk: unknown')&&descriptionText.includes('Options: Allow, Block, Remove rule'),'future risk stays unknown and choices name existing rule operations');
  const writesBeforeRowEdit=rightsCalls.length;
  await editPermission(customRow).onclick();element('rights-permit').value='deny';element('rights-grant').dispatch('input');
  assert.equal(rightsCalls.length,writesBeforeRowEdit,'choosing Block stages a review and makes no policy write');
  assert.equal(element('rights-program').value,'/usr/bin/actual-caller');assert.equal(element('rights-resource').value,'exact-source');
  element('rights-ttl').value='300000';element('rights-why').value='Try blocking this app';element('rights-grant').dispatch('input');
  const preview=detailBody(element('rights-preview').children[1]).textContent;
  assert(preview.includes('Choice: Block')&&preview.includes('Lifetime: Five minutes')&&preview.includes('Reason: Try blocking this app')&&preview.includes('Revision: custom-revision'),'the complete proposed exact effect is visible before Save');
  await element('rights-grant').onsubmit({preventDefault(){}});
  assert.deepEqual(rightsCalls.at(-1),{edit:'set',revision:'custom-revision',account:'alice',program:'/usr/bin/actual-caller',action:customAction,resource:'exact-source',permit:false,ttl_ms:300000,why:'Try blocking this app'});
  const refreshedRow=permissionRows()[0],writesBeforeRemove=rightsCalls.length;
  await removePermission(refreshedRow).onclick();
  assert.equal(rightsCalls.length,writesBeforeRemove,'the first Remove click only asks again');
  assert.equal(refreshedRow.children[2].textContent,'Select Remove again within ten seconds to remove this permission.','the row says the second click removes');
  // The armed state lasts ten seconds: a click after that asks again.
  removePermission(refreshedRow).armed-=11000;
  await removePermission(refreshedRow).onclick();
  assert.equal(rightsCalls.length,writesBeforeRemove,'a second click after ten seconds asks again instead of removing');
  await removePermission(refreshedRow).onclick();
  assert.deepEqual(rightsCalls.at(-1),{edit:'revoke',revision:'rev-next',account:'alice',program:'/usr/bin/actual-caller',action:customAction,resource:'exact-source'},'the descriptor feature app never replaces the rule target or exact resource; removal uses the reread revision');
  assert(element('rights-status').textContent.startsWith('Permission removed'),'removal confirms after absence readback: '+descendants(element('rights-status')).map(n=>n.textContent).join(' | '));
  console.log('PASS: Rights page questions answer with typed refusals; custom descriptions render operator provenance as text, future risk is unknown, and edits retain the original rule target and resource');
 }

 // Typed selector controls render public metadata and stage one conditional
 // edit. Scope matching and all access decisions remain native service work.
 {
  fields.clear();
  const subject={Account:'owner',Program:'/apps/requester'},setter={Account:'owner',Program:'/apps/operator'};
  const use={Action:'abstraction.credentials/apply',ResourceProfile:'abstraction.credentials/named-use@2'};
  const keys=[use,...['read','create','rotate','revoke','metadata.read'].map(op=>({Action:'abstraction.credentials/secret.'+op,ResourceProfile:'abstraction.credentials/named-secret@1'}))];
  const definitions=keys.map((key,i)=>({Definition:{...key,OwnerContract:i?'abstraction.credentials/holder@2':'abstraction.credentials/applier@2'},Label:['Use saved secret','Read saved secret value','Create saved secret','Rotate saved secret value','Revoke saved secret','Read named secret metadata'][i],SupportedSelectors:['one','all'],Collection:'abstraction.credentials/saved-app-secrets@1',AllEffect:'Current and future saved application secrets.',RequestType:'CredentialResource',Editable:true,CodecAvailable:true}));
  const origin={ID:'original-bundle',BundleRef:'abstraction.credentials/full-control@1',Digest:'frozen-six-v1',Original:6,Remaining:5,State:'partial/changed',Members:keys.map(Permission=>({Permission,Scope:'one',Name:'alpha',Target:'alpha',Editable:true})),LinkedMembers:keys.filter(k=>k.Action!=='abstraction.credentials/secret.read').map(Permission=>({Permission,Scope:'one',Name:'alpha',Target:'alpha',Editable:true}))};
  const named={Subject:subject,Action:use.Action,Resource:'alpha',Permit:true,SetBy:setter,SetAt:'2026-10-05T12:00:00Z',Selector:{Permission:use,Scope:'one',Name:'alpha',Target:'alpha',Editable:true,CanGrant:true,Origin:origin}};
  const unknown={...named,Action:'foreign/operation',Resource:'Structured target: profile decoder unavailable',Selector:{Permission:{Action:'foreign/operation',ResourceProfile:'foreign/target@1'},Scope:'one',Target:'Structured target: profile decoder unavailable',Editable:false}};
  const bundle={BundleRef:origin.BundleRef,OwnerContract:'abstraction.credentials/holder@2',Label:'Full control of saved secrets',SupportedSelectors:['one','all'],Members:keys,DefinitionDigest:origin.Digest,Disclosure:'Six frozen named operations.',Editable:true};
  let page={Outcome:'page',Revision:'typed-one',Catalog:[use.Action],ActionPlain:{[use.Action]:'use saved secret'},Rules:[named,unknown,{...named,Resource:'*',Selector:undefined}],Definitions:definitions,Bundles:[bundle],SelectorAvailable:true,Next:'',Complete:true};
  const writes=[],paths=[];
  const context={URLSearchParams,console,location:{search:'?k=test'},localStorage:{length:0,key(){},getItem(){return null},setItem(){}},document:{getElementById:element,createElement:node},fetch:async(path,opts={})=>{
   paths.push(path);if(path.startsWith('/rights?credential_names=1'))return {ok:true,json:async()=>({Outcome:'page',Names:['alpha','beta'],Complete:true})};
   if(path.startsWith('/rights?'))return {ok:true,json:async()=>page};
   if(path==='/rights'&&opts.body){writes.push(JSON.parse(opts.body));return {ok:true,json:async()=>({Outcome:'applied',Revision:page.Revision,Current:[]})}}
   if(path==='/explore')return {ok:true,json:async()=>({self:{account:'owner',program:'/apps/operator'},probes:[]})};
   if(path==='/card/table')return {ok:true,json:async()=>({resources:[]})};
   if(path.startsWith('/questions')||path.startsWith('/choices'))return {ok:true,json:async()=>({Outcome:'page',Records:[],Next:'',Complete:true})};
   if(path.startsWith('/rights/requests')) return {ok:true,json:async()=>({outcome:'page',requests:[],complete:true})};
    if(path==='/rights/delegation')return {ok:true,json:async()=>({outcome:'page',revision:page.Revision,questions:[]})};
   if(path==='/rights/assistant')return {ok:true,json:async()=>({outcome:'page',revision:page.Revision,assistants:[],candidates:[]})};
   if(path==='/rights/answered')return {ok:true,json:async()=>({outcome:'page',answers:[]})};
   throw Error('unexpected '+path);
  }};context.window=context;vm.runInNewContext(plainScript(),context);vm.runInNewContext(loadPageScript('rights_page.go'),context);await context.rightsListReady;
  const row=permissionRows()[0],opaque=permissionRows()[1];
  const provenance=permissionDetails(row);
  assert(detailLines(provenance).some(line=>line.startsWith('Set by operator (not registered) on this account, ')),'a setter matching the Panel still names the recorded app and account in plain words');
  assert(!JSON.stringify(provenance).includes('set by you'),'matching the Panel process cannot assert a human act');
  const exactProvenance=detailRaw(provenance);
  assert(exactProvenance.includes('Recorded account: owner')&&exactProvenance.includes('Via application: /apps/operator')&&exactProvenance.includes('Set at: 2026-10-05T12:00:00Z'),'recorded identities and timestamps stay exact in Details');
  assert(editPermission(opaque).disabled&&removePermission(opaque).disabled,'an unknown profile is view-only');
  element('rights-capability-filter').value=context.selectorActionValue(use);await element('rights-capability-filter').onchange();
  assert.equal(permissionRows().length,1,'capability filtering keeps the full permission key and excludes the separate legacy exact rule');assert.equal(writes.length,0);
  element('rights-capability-filter').value='';await element('rights-capability-filter').onchange();
  await editPermission(row).onclick();element('rights-permit').value='deny';element('rights-grant').dispatch('input');await context.loadRightsSecretNames();
  assert.equal(writes.length,0,'selecting a row and reading name metadata never writes permissions');
  assert.deepEqual(element('rights-secret-names').children.map(o=>o.value),['alpha','beta'],'the picker contains saved application secret metadata and excludes local keys');
  element('rights-scope').value='all';element('rights-scope').onchange();element('rights-grant').dispatch('input');
  const textOf=n=>[n.textContent||'',...(n.children||[]).map(textOf)].join('\n');
  assert(textOf(element('rights-preview')).includes('current and future saved application secrets')&&textOf(element('rights-preview')).includes('Runtime-managed local keys are excluded'),'All previews its real collection and future membership before Save');
  await element('rights-grant').onsubmit({preventDefault(){}});assert.equal(writes.length,0,'broad selection waits for explicit scope confirmation');
  element('rights-broad-confirm').checked=true;for(const listener of element('rights-grant').listeners.input)listener({target:{id:'rights-program'}});assert(!element('rights-broad-confirm').checked,'changing the reviewed effect requires a fresh confirmation');
  await element('rights-grant').onsubmit({preventDefault(){}});assert.equal(writes.length,0);
  element('rights-broad-confirm').checked=true;await element('rights-grant').onsubmit({preventDefault(){}});
  assert.deepEqual(writes[0],{edit:'set',revision:'typed-one',account:'owner',program:'/apps/requester',action:use.Action,resource_profile:use.ResourceProfile,scope:'all',permit:false});
  element('rights-action').value='bundle:'+bundle.BundleRef;element('rights-action').onchange();element('rights-scope').value='all';element('rights-scope').onchange();element('rights-grant').dispatch('input');
  const bundlePreview=textOf(element('rights-preview'));
  for(const key of keys)assert(bundlePreview.includes(key.Action)&&bundlePreview.includes(key.ResourceProfile),'the complete frozen bundle expansion is shown before Apply');
  assert(bundlePreview.includes('frozen-six-v1')&&bundlePreview.includes('excludes secret enumeration')&&bundlePreview.includes('secret bytes'),'bundle version and raw Read effect are disclosed');
  assert.equal(element('rights-permit').value,'permit','full control is a permit proposal, never a guessed multi-rule block');
  await element('rights-grant').onsubmit({preventDefault(){}});assert.equal(writes.length,1);
  element('rights-broad-confirm').checked=true;await element('rights-grant').onsubmit({preventDefault(){}});
  assert.deepEqual(writes[1],{edit:'set',revision:'typed-one',account:'owner',program:'/apps/requester',bundle_ref:bundle.BundleRef,scope:'all',permit:true});
  context.editBundleOrigin(named);assert(textOf(element('rights-preview')).includes('5/6 (partial/changed)'),'remaining bundle removal retains the original expansion and changed state');
  const removedMembers=element('rights-preview').children.find(n=>n.tag==='ul');assert.equal(removedMembers.children.length,5);assert(!textOf(removedMembers).includes('secret.read'),'the complete removal effect excludes the independently edited Read rule');
  assert.equal(writes.length,2);element('rights-broad-confirm').checked=true;await element('rights-grant').onsubmit({preventDefault(){}});
  assert.deepEqual(writes[2],{edit:'remove_bundle',revision:'typed-one',account:'owner',program:'/apps/requester',bundle_origin_id:'original-bundle'});
  const literalRow=permissionRows()[2];await editPermission(literalRow).onclick();assert(textOf(element('rights-preview')).includes('exact resource named *'),'a legacy literal asterisk remains exact');
  page={...page,Definitions:[{...definitions[0],Editable:false,SupportedSelectors:[]}],Bundles:[],Rules:[{...named,Selector:{...named.Selector,Origin:undefined,CanGrant:false}}]};await context.rightsList();
  const retained=permissionRows()[0];assert(!editPermission(retained).disabled&&!removePermission(retained).disabled,'a known but unserved profile keeps retained removal available');
  await editPermission(retained).onclick();assert.equal(element('rights-permit').value,'remove','the retained-only Edit stages removal');assert.equal(element('rights-scope').value,'one');assert(element('rights-scope').disabled&&element('rights-name').readOnly,'retained removal preserves the original canonical name and scope');
  await element('rights-grant').onsubmit({preventDefault(){}});assert.deepEqual(writes.at(-1),{edit:'revoke',revision:'typed-one',account:'owner',program:'/apps/requester',action:use.Action,resource_profile:use.ResourceProfile,scope:'one',credential_name:'alpha'});
  assert(paths.some(p=>p.startsWith('/rights?credential_names=1'))&&!paths.some(p=>p.startsWith('/credentials')),'permissions invoke the names-only projection without a value reader');
  // A permission set's option reads as a phrase: an empty label falls back
  // to the action's plain words, a label with a slash stays; no raw id or
  // version reaches the option text, only its title.
  page={...page,Bundles:[{...bundle,BundleRef:'abstraction.credentials/full-control@1',Label:''},{...bundle,BundleRef:'abstraction.credentials/read-write@1',Label:'Read/write saved secrets'}]};
  await context.rightsList();
  const bundleOptions=element('rights-action').children.filter(o=>String(o.value).startsWith('bundle:'));
  assert.equal(bundleOptions.length,2);
  assert.equal(bundleOptions[1].textContent,'Read/write saved secrets (a set of permissions)','a label with a slash stays as written');
  for(const o of [...bundleOptions,...element('rights-capability-filter').children])assert(!/abstraction\.|@/.test(o.textContent),'no raw id or version in option text: '+o.textContent);
  assert(bundleOptions.every(o=>/^abstraction\.credentials\//.test(o.title)),'the raw id stays on the option title');
  console.log('PASS: Typed targets, fixed bundle expansion and raw Read effects are reviewed before conditional edits; unknown profiles are view-only; legacy * stays exact');
 }

 // task 2026-09-23, fifteenth first-time visitor, finding 3: a rule Save
 // rule or Allow downloads just landed never appeared in the Rights list,
 // not on arrival and not on Refresh, though the confirmation said it was
 // set. This runtime already grants itself dozens of rules at
 // installation, so a freshly named program's own rule almost never sorts
 // onto the one page rightsCursor happened to be on; this fixture reaches
 // the same shape with two pages, the saved rule only on the second, to
 // prove Save rule now walks forward and shows the page it actually
 // landed on instead of leaving it unseen past the first page.
 {
  fields.clear();
  const exploreSelfReply = {self:{account:'root',program:'/usr/bin/openabstractions'},selfAccountName:'alice',probes:[]};
  const pageOneRule = {Subject:{Account:'root',Program:'/usr/bin/alreadyinstalled'},Action:'abstraction.storage/content.read',Resource:'sha256:a',Permit:true};
  const target = {Subject:{Account:'root',Program:'/usr/bin/verylateapp'},Action:'abstraction.model/lookup',Resource:'newregistry',Permit:true};
  const catalog = ['abstraction.storage/content.read','abstraction.model/lookup'];
  const actionPlain = {'abstraction.model/lookup':'model resolve'};
  const pageOne = {Outcome:'page', Revision:'rev-1', Catalog:catalog, ActionPlain:actionPlain, Rules:[pageOneRule], Next:'cursor-2', Complete:false};
  const pageTwo = {Outcome:'page', Revision:'rev-1', Catalog:catalog, ActionPlain:actionPlain, Rules:[target], Next:'', Complete:true};
  const questionsEmpty = {Outcome:'page', Records:[], Next:'', Complete:true};
  const cardTableEmpty = {resources:[]};
  let rightsGetCount=0, rightsGetCursors=[], releaseSecond=null, secondHeld=false;
  const context = {URLSearchParams, localStorage:{length:0,key(){},getItem(){return null},setItem(){}}, document:{getElementById:element, createElement:node}, location:{search:'?k=test'}, console,
   fetch:async(path,opts)=>{
    if(path==='/card/table') return {ok:true,json:async()=>cardTableEmpty};
    if(path==='/explore') return {ok:true,json:async()=>exploreSelfReply};
    if(path.startsWith('/questions')) return {ok:true,json:async()=>questionsEmpty};
    if(path.startsWith('/rights/requests')) return {ok:true,json:async()=>({outcome:'page',requests:[],complete:true})};
    if(path==='/rights/delegation') return {ok:true,json:async()=>({outcome:'page',revision:'delegate-rev',account:'root',questions:[]})};
    // Defaults for all apps is its own list, read after each rules read.
    if(path==='/rights/defaults') return {ok:true,json:async()=>({Outcome:'page',Revision:'defaults-rev',Defaults:[]})};
    if(path.startsWith('/rights')){
     if(!opts.body){
      rightsGetCount++;
      const cursor=path.split('cursor=')[1]||'';
      rightsGetCursors.push(cursor);
      if(cursor&&!secondHeld){secondHeld=true;return new Promise(resolve=>{releaseSecond=()=>resolve({ok:true,json:async()=>pageTwo})})}
      return {ok:true,json:async()=>(cursor?pageTwo:pageOne)};
     }
     pageOne.Revision=pageTwo.Revision='rev-2';
     return {ok:true,json:async()=>({Outcome:'applied',Revision:'rev-2',Current:target})};
    }
    throw Error('unexpected request '+path);
   }};
  context.window = context; // window is the global object here, same as a browser (task 2026-09-23, fifteenth first-time visitor, finding 1)
  vm.runInNewContext(plainScript(), context);
  vm.runInNewContext(loadPageScript('rights_page.go'), context);
  // Saved permissions shows the first page at once and reads the rest in the
  // background: the page is ready after the first page.
  await context.rightsFirstPage;
  assert.deepEqual(permissionRows().map(rowProgram),['/usr/bin/alreadyinstalled'],'the first page is drawn before the rest is read');
  assert.equal(element('rights-next').hidden,true,'Show more stays hidden while the rest is read');
  assert(releaseSecond,'the second page is being read in the background');releaseSecond();
  await context.questionsReady;
  await context.rightsDeviceHoldersReady;
  await context.rightsSelfReady;
  await context.rightsListReady;
  await context.delegationReady;
  await context.assistantReadyPromise;
  await context.answeredReady;

  const rightsGroups=()=>permissionRows();
  assert.equal(rightsGetCount,4,'arrival reads every page of rules, then assistants and answered questions');
  assert.deepEqual(rightsGroups().map(rowProgram),['/usr/bin/alreadyinstalled','/usr/bin/verylateapp'],'both pages are shown on arrival');
  assert.equal(element('rights-next').hidden,true,'no rows remain past the read, so Show more is hidden');

  const rightsGetCountBeforeSave=rightsGetCount;
  element('rights-account').value='root';
  element('rights-program').value='/usr/bin/verylateapp';
  element('rights-action').value='abstraction.model/lookup';
  element('rights-resource').value='newregistry';
  element('rights-permit').value='permit';element('rights-ttl').value='0';
  await element('rights-grant').onsubmit({preventDefault(){}});
  assert.equal(rightsGetCount,rightsGetCountBeforeSave+2,'the save walked forward across both pages to find the rule it just set, not only the first');
  assert.deepEqual(rightsGetCursors.slice(-2),['','cursor-2'],'the walk restarted from the first page and followed Next into the second');
  assert.equal(element('rights-status').textContent.startsWith('Permission updated'),true,'the confirmation follows the complete authoritative readback');
  const groups=rightsGroups();
  assert.equal(groups.length,2,'both pages\' rows are shown together, the one the rule landed on included');
  const programs=groups.map(rowProgram);
  assert(programs.includes('/usr/bin/verylateapp'),'the rule Save rule just set is now visible in the list itself, not only in the confirmation: '+programs.join(', '));
  assert.equal(element('rights-next').disabled,true,'the walk reached the second, complete page, so Next page is now disabled');
  console.log('PASS: Rights page a rule Save rule just set, landing past the first page, is shown in the list itself after the walk that finds it');
 }

 // --- Exact app selection spans pages and never edits permissions on open. ---
 {
  fields.clear();const program='C:/Apps/a & b/assistant.exe',calls=[];
  const chosen={Subject:{Account:'alice',Program:program},Action:'abstraction.model/lookup',Resource:'hf',Permit:true};
  const pages={
   '':{Outcome:'page',Revision:'r1',Catalog:['abstraction.model/lookup'],ActionPlain:{},Rules:[{...chosen,Subject:{Account:'bob',Program:program}}],Next:'second',Complete:false},
   second:{Outcome:'page',Revision:'r1',Catalog:['abstraction.model/lookup'],ActionPlain:{},Rules:[chosen,{...chosen,Subject:{Account:'alice',Program:'C:/Apps/other/assistant.exe'}}],Next:'',Complete:true}};
  const context={URLSearchParams,document:{getElementById:element,createElement:node,createTextNode:text=>({textContent:text}),documentElement:{dataset:{}}},location:{search:'?'+new URLSearchParams({k:'test',account:'alice',program})},console,
   fetch:async(path,opts)=>{
    assert(!opts.body,'opening an app writes no permission');
    if(path.startsWith('/rights?')){const cursor=new URL('http://panel.test'+path).searchParams.get('cursor');calls.push(cursor);return {ok:true,json:async()=>pages[cursor]}}
    if(path==='/explore')return {ok:true,json:async()=>({self:{account:'alice',program:'C:/OA/Panel.exe'},selfAccountName:'Alice'})};
    if(path==='/card/table')return {ok:true,json:async()=>({resources:[]})};
    if(path.startsWith('/rights/requests')) return {ok:true,json:async()=>({outcome:'page',requests:[],complete:true})};
    if(path==='/rights/delegation')return {ok:true,json:async()=>({outcome:'page',account:'alice',questions:[]})};
    if(path==='/rights/assistant')return {ok:true,json:async()=>({outcome:'page',assistants:[],candidates:[],warning:{answers:[],staysWithYou:[]}})};
    if(path==='/rights/answered')return {ok:true,json:async()=>({outcome:'page',answers:[]})};
    if(path.startsWith('/questions?')||path.startsWith('/choices?'))return {ok:true,json:async()=>({Outcome:'page',Records:[],Complete:true,Next:''})};
    throw Error('unexpected '+path);
   }};
  context.window=context;vm.runInNewContext(plainScript(),context);vm.runInNewContext(loadPageScript('rights_page.go'),context);
  await context.rightsListReady;await context.questionsReady;await context.choicesReady;await context.rightsArrived;assert.equal(context.document.documentElement.dataset.ready,'1','Permissions marks itself ready once its first reads finished');await context.assistantReadyPromise;await context.answeredReady;
  assert.deepEqual(calls,['','second'],'app selection reads through the owning permission API\'s pages');
  const groups=permissionRows();
  assert.equal(groups.length,1,'matching basenames and other accounts are excluded');
  assert.equal(rowProgram(groups[0]),program);
  assert.equal(groups[0].children.length,3,'the selected permission is one row: its text, its controls and its status line');
  assert.equal(appGroup(groups[0]).children[0].textContent,'assistant (not registered)','an unregistered program reads as its file stem, not its file name');
  assert.equal(appGroup(groups[0]).children[0].title.split(' \u2014 ')[0],program,'the full path stays on the title');
  assert.equal(element('rights-program').value,program);assert.equal(element('rights-account').value,'alice','the edit form targets the selected subject');
  assert(element('rights-heading').focused,'the Apps link focuses saved permissions after exact subject selection');
  assert(!element('rights-editor').open,'opening saved access leaves the editor collapsed');
  element('rights-add').onclick();assert(element('rights-editor').open&&element('rights-program-select').focused,'Add opens and focuses the existing editor without writing');
  assert.equal(element('rights-filter').hidden,false);assert(element('rights-show-all').href.includes('/rights?k=test#rights-section'));
  assert.equal(element('questions-empty').hidden,false,'no waiting question shows the one empty sentence');assert.equal(element('questions-list').children.length,0);
  assert.equal(element('questions-next').hidden,true,'Show more appears only past a page');
  console.log('PASS: Opening an app selects its exact saved permissions across pages, preserves edit scope and renders a structured pending-request empty state');
 }

 // --- A bounded list scopes its empty state to the pages actually read. ---
 {
  const installed={Subject:{Account:'alice',Program:'C:/OA/Panel.exe'},Action:'abstraction.model/lookup',Resource:'hf',Permit:true,Why:'installation'};
  fields.clear();const program='C:/Apps/OpenCode.exe',reads=[];let selectedPage=20;
  const selected={Subject:{Account:'alice',Program:program},Action:'abstraction.model/lookup',Resource:'hf',Permit:true};
  const context={URLSearchParams,document:{getElementById:element,createElement:node,createTextNode:text=>({textContent:text})},location:{search:'?'+new URLSearchParams({k:'test',account:'alice',program})},console,
   fetch:async(path,opts)=>{
    assert(!opts.body,'reading bounded lists makes no permission edit');
    if(path.startsWith('/rights?')){const cursor=new URL('http://panel.test'+path).searchParams.get('cursor'),page=cursor?Number(cursor):0;reads.push(page);return {ok:true,json:async()=>({Outcome:'page',Revision:'r1',Catalog:['abstraction.model/lookup'],ActionPlain:{},Rules:page===selectedPage?[selected]:[installed],Complete:page===20,Next:page===20?'':String(page+1)})}}
    if(path==='/explore')return {ok:true,json:async()=>({self:{account:'alice',program:'C:/OA/Panel.exe'},selfAccountName:'Alice'})};
    if(path==='/card/table')return {ok:true,json:async()=>({resources:[]})};
    if(path.startsWith('/rights/requests')) return {ok:true,json:async()=>({outcome:'page',requests:[],complete:true})};
    if(path==='/rights/delegation')return {ok:true,json:async()=>({outcome:'page',account:'alice',questions:[]})};
    if(path==='/rights/assistant')return {ok:true,json:async()=>({outcome:'page',assistants:[],candidates:[],warning:{answers:[],staysWithYou:[]}})};
    if(path==='/rights/answered')return {ok:true,json:async()=>({outcome:'page',answers:[]})};
    if(path.startsWith('/questions?'))return {ok:true,json:async()=>({Outcome:'page',Records:[],Complete:true,Next:''})};
    throw Error('unexpected '+path);
   }};
  context.window=context;vm.runInNewContext(plainScript(),context);vm.runInNewContext(loadPageScript('rights_page.go'),context);await context.rightsListReady;
  assert.equal(reads.length,20);
  assert.equal(element('rights').children[0].textContent,'No saved permissions for this app on these pages; choose Show more.','the selected app may have a rule on page 21');
  assert.equal(element('rights-next').disabled,false,'the page limit preserves continuation');
  await element('rights-next').onclick();
  assert.equal(reads.length,21);assert.equal(reads[20],20,'Next page resumes at the unread page');
  const groups=permissionRows();assert.equal(groups.length,1);assert.equal(rowProgram(groups[0]),program,'the existing target rule appears when its page is read');
  assert.equal(element('rights-next').disabled,true);
  selectedPage=0;reads.length=0;await element('rights-capability-filter').onchange();
  assert.equal(reads.length,20);assert.equal(permissionRows().length,1,'the app has saved access on the first page');
  await element('rights-next').onclick();
  assert.equal(reads[20],20);assert.equal(element('rights-next').disabled,true,'the final continuation reaches the end');
  assert.equal(element('rights').children[0].textContent,'No saved permissions for this app on these pages.','an empty final continuation preserves the scope of the current pages');
  await element('rights-capability-filter').onchange();assert.equal(permissionRows().length,1,'Refresh returns to the app permission already seen');
  console.log('PASS: Page limits preserve partial and final continuation wording; selected permissions on page 21 and earlier pages stay reachable');
 }
})().catch(e=>{console.error(e);process.exitCode=1});
