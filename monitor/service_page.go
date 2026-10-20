package main

import "strings"

// serviceCommonScript is shared by the Status page and the six pages split
// out of it (task 2026-09-23, requirement 1: each of Work, Questions and
// rights, Settings, Logging, Identity and Explore gets its own page instead
// of scrolling one long Status page). Every one of those pages already used
// this same handful of DOM and fetch helpers inline; this is that text once,
// prefixed onto each page's own script tag, so it still reads as one
// self-contained script block, the shape monitor/*_page_test.js expects.
const serviceCommonScript = `const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);function show(id,x){el(id).textContent=typeof x==='string'?x:JSON.stringify(x,null,2)}
function fail(id,e){ if (typeof oaShowError === 'function') oaShowError(id, e.message || String(e)); else show(id, e.message || String(e)) }
async function call(path,body,signal){const r=await fetch(path,{signal,method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function para(parent,text,cls,title){const p=document.createElement('p');p.textContent=text;if(cls)p.className=cls;if(title)p.title=title;parent.append(p);return p}
// statusPara writes one finding as an .oa-status line with data-state (ok,
// error, idle) instead of a page-local class (SPEC §6).
function statusPara(parent,text,state,title){const p=para(parent,text,'',title);p.className='oa-status';if(p.setAttribute)p.setAttribute('data-state',state);else p.dataset=Object.assign(p.dataset||{},{state});return p}
function heading(parent,text){const h=document.createElement('h3');h.textContent=text;parent.append(h)}
function emptyState(parent,title,text){const box=document.createElement('div');box.className='oa-empty';heading(box,title);para(box,text);parent.append(box)}
function shortName(path){return oaShortName(path)}
function capitalize(s){return s?s.charAt(0).toUpperCase()+s.slice(1):s}
`

// servicePage is the Apps page (research/panel-redesign/SPEC.md §4, S2):
// one caption, a health banner shown only while the runtime or one of its
// services fails, and the applications registered with the runtime, each a
// row opening its app page (app_page.go). Saved permissions live on
// Permissions and on each app page; providers on Providers. The banner's row
// sits in a plain wrapper, so hidden wins over the row's grid display.
var servicePage = pageOwnsReady(oaPageHead("status", "Apps", "Apps that use OpenAbstractions.")) +
	`<div id="apps-health" hidden><div class="oa-row"><div class="oa-row-text"><span class="oa-row-label" id="apps-health-label"></span></div>` +
	`<div class="oa-row-control"><button id="apps-health-retry">Check again</button></div>` +
	`<p class="oa-status" id="apps-health-status" role="status" aria-live="polite" data-state="idle"></p></div></div>` +
	oaGroup("service-check", "Your apps", "", oaStatus("apps-status"), `<div id="apps"></div>`) +
	`<script>` + serviceCommonScript + appsPageScript + `</script>`

// pageOwnsReady marks a page head as the page's own readiness owner
// (data-ready-owner="page"): its script calls oaReady once its first reads
// finish, so the screenshot tool waits for that and not for load.
func pageOwnsReady(head string) string {
	return strings.Replace(head, `<header class="oa-pagehead"`, `<header class="oa-pagehead" data-ready-owner="page"`, 1)
}

// appsPageScript keeps the Apps list current: it follows the registry's own
// observation (/apps/registrations with a cursor and a wait), and checks the
// runtime (/runtime) on arrival, every half minute while the page is
// visible, and on Check again.
const appsPageScript = `
const appsState={entries:null,cursor:'',failure:'',runtime:''};var appsRun=null;let appsRecovered=false;
// The server titles each entry (appEntryTitle): a reserved title reads as the
// program's stem, and an entry that added itself says so until it is kept.
function appsTitle(e){const d=e.application&&e.application.Descriptor;return e.title||d&&d.Title||e.name}
function appsRunning(e){return !!(e.application&&e.application.Instances&&e.application.Instances.length)}
function appsHref(name){return '/app/'+encodeURIComponent(name)+'?k='+encodeURIComponent(key)}
// Rows are kept by registered name and updated in place, so a reread keeps
// the row a person is on.
let appsRows=new Map();
function appsRow(){
 const a=document.createElement('a');a.className='oa-row oa-row-nav';
 const text=document.createElement('span');text.className='oa-row-text';
 a.label=document.createElement('span');a.label.className='oa-row-label';a.caption=document.createElement('span');a.caption.className='oa-row-caption';
 text.append(a.label,a.caption);const chevron=document.createElement('span');chevron.className='oa-row-chevron';chevron.setAttribute('aria-hidden','true');
 a.append(text,chevron);return a;
}
function renderApps(){
 const out=el('apps');if(appsState.entries===null)return;
 const entries=appsState.entries.filter(e=>e&&e.role==='application'&&e.name).sort((a,b)=>appsTitle(a).localeCompare(appsTitle(b))),next=new Map();
 const rows=entries.map(e=>{const row=appsRows.get(e.name)||appsRow();row.href=appsHref(e.name);row.label.textContent=appsTitle(e);row.caption.textContent=(e.self_registered?'Open to confirm or remove · ':'')+(appsRunning(e)?'Running':'Not running');next.set(e.name,row);return row});
 appsRows=next;
 if(rows.length&&rows.length===out.children.length&&rows.every((row,i)=>out.children[i]===row))return;
 out.replaceChildren();
 if(!rows.length){const p=document.createElement('p');p.className='oa-empty';p.textContent='No apps have registered yet.';out.append(p);return}
 for(const row of rows)out.append(row);
}
// The banner names the first failure: the runtime itself, a service it
// resolves, or the registry read. It stays hidden while everything works.
function renderAppsHealth(){
 const problem=appsState.runtime||appsState.failure,banner=el('apps-health');
 if(problem){banner.hidden=false;el('apps-health-label').textContent=problem;appsRecovered=false;return}
 if(appsRecovered){el('apps-health-label').textContent='OpenAbstractions works again.';return}
 banner.hidden=true;
}
const appsServiceProblem={unavailable:1,incompatible:1};
async function appsCheckRuntime(){
 try{const v=await call('/runtime');
  if(v.error)appsState.runtime='OpenAbstractions is not running. Start it, then check again.';
  else{const failing=(v.capabilities||[]).find(c=>appsServiceProblem[c.status]);appsState.runtime=failing?oaPlain('service resolution: '+failing.status+': '+failing.contract).line:''}
 }catch(e){appsState.runtime='OpenAbstractions is not reachable. Start it, then check again.'}
 renderAppsHealth();
}
async function appsRead(signal,wait){
 const query=new URLSearchParams({source:'applications',cursor:appsState.cursor,wait_ms:String(wait)});
 const page=await call('/apps/registrations?'+query,undefined,signal);
 if(signal&&signal.aborted)return;
 if(page.outcome!=='page'&&page.outcome!=='stale')throw Object.assign(Error('Registered apps: '+page.outcome),{outcome:page.outcome});
 appsState.cursor=page.cursor||'';appsState.entries=page.entries||[];appsState.failure='';
 oaSetStatus('apps-status','idle','');renderApps();renderAppsHealth();
}
function appsPause(signal,ms){return new Promise(resolve=>{if(signal&&signal.aborted){resolve();return}const done=()=>{clearTimeout(timer);if(signal)signal.removeEventListener('abort',done);resolve()},timer=setTimeout(done,ms);if(signal)signal.addEventListener('abort',done,{once:true})})}
let appsFirstReadDone=null;const appsFirstRead=new Promise(resolve=>{appsFirstReadDone=resolve});
async function appsFollow(run){
 let wait=0;
 for(;;){
  if(run&&run.signal.aborted)return;
  try{await appsRead(run&&run.signal,wait);wait=30000;appsFirstReadDone()}
  catch(e){
   if(run&&run.signal.aborted)return;
   appsState.failure=e.outcome==='forbidden'?'This Panel may not read the list of apps.':'The list of apps is unavailable right now.';
   if(appsState.entries===null)oaSetStatus('apps-status','error',appsState.failure);
   renderAppsHealth();appsState.cursor='';wait=0;appsFirstReadDone();
   if(!run)return;await appsPause(run.signal,5000);
  }
  if(!run)return;
 }
}
appsRun=typeof AbortController==='undefined'?null:new AbortController();
var appsReady=appsFollow(appsRun),appsHealthReady=appsCheckRuntime();
// The screenshot tool waits for data-ready once the first reads finished.
var appsArrived=Promise.all([appsFirstRead,appsHealthReady]).then(()=>{if(typeof oaReady==='function')oaReady()});
el('apps-health-retry').onclick=async()=>{
 const before=appsState.runtime||appsState.failure;
 await appsCheckRuntime();
 if(appsState.failure){appsState.cursor='';try{await appsRead(appsRun&&appsRun.signal,0)}catch(e){}}
 const after=appsState.runtime||appsState.failure;
 if(before&&!after){appsRecovered=true;el('apps-health').hidden=false;renderAppsHealth()}
 oaSetStatus('apps-health-status',after?'error':'ok',after?'Checked '+oaLocalTime(new Date())+'; the problem remains.':'Checked '+oaLocalTime(new Date())+'; everything works.');
};
if(typeof window.addEventListener==='function')window.addEventListener('pagehide',()=>{if(appsRun)appsRun.abort()});
if(typeof document.addEventListener==='function'){
 const tick=()=>setTimeout(async()=>{if(document.visibilityState!=='hidden')await appsCheckRuntime();tick()},30000);tick();
 document.addEventListener('visibilitychange',()=>{if(document.visibilityState!=='hidden')appsCheckRuntime()});
}
`

// runtimeSupportScript is the existing runtime diagnostic, presented from
// Settings when requested. It uses the existing guarded /runtime handler.
const runtimeSupportScript = `
function runtimeSentence(v){if(v.explicit)return 'This Panel is connected to the runtime it was started for.';if(v.bootstrap!=='unknown')return 'This Panel is connected to the installed runtime.';return 'This Panel is running from a development build; no installed runtime was found next to it.'}
const capabilityPlainStatus={unavailable:1,forbidden:1,incompatible:1,invalid_request:1};
function capabilityLine(c){if(!capabilityPlainStatus[c.status])return c.name+': '+c.label;return c.name+': '+oaPlain('service resolution: '+c.status+': '+c.contract).line}
function renderRuntime(v){const out=el('runtime');out.replaceChildren();para(out,v.error?'The runtime connection is unavailable. Try again or restart OpenAbstractions.':'Connected to OpenAbstractions. Review an app’s pending request on Permissions if it needs access.');const details=oaTechnical(document.createElement('details'));const summary=document.createElement('summary');summary.textContent='Technical details';details.append(summary);para(details,runtimeSentence(v));if(v.detail&&!v.explicit)para(details,v.detail);for(const c of v.capabilities||[])statusPara(details,capabilityLine(c),c.status==='forbidden'?'error':'idle');const pre=document.createElement('pre');pre.textContent=JSON.stringify(v,null,2);details.append(pre);out.append(details)}
// Check the service used to write nothing a second click could tell apart
// from the first: renderRuntime always rebuilds the same content from the
// same reply, with no line saying a check had actually just run (task
// 2026-09-23, thirteenth first-time visitor, finding 1). One line beside
// the button now names when this click's own answer came back.
// runtimeSupportStart binds Check the service once its row is on the page
// (Settings renders it only when Show developer tools is on).
function runtimeSupportStart(){el('status').onclick=async()=>{try{const v=await call('/runtime');renderRuntime(v);el('check-status').textContent='Checked '+oaLocalTime(new Date())+'.'}catch(e){el('runtime').replaceChildren();fail('check-status',e)}};}
`
