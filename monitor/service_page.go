package main

import (
	"strings"
)

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
async function call(path,body,signal){if(body&&body.candidates&&path.split('?')[0]==='/registry/selection')body=await oaPanelRequest(path,body);const r=await fetch(path,{signal,method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok){const text=await r.text();let reply=null;try{reply=JSON.parse(text)}catch(_){}if(reply&&reply.busy){const e=Error('Another Catalog step is running.');e.busy=reply;throw e}throw Error(text)}return oaPanelResponse(path,await r.json())}
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
// one caption, a health banner shown only while one of the runtime's
// services or a list read fails (a stopped runtime is the shell's banner,
// runtimeScript), the apps an installer asked to connect that are Not
// connected (application_pending.go), and the applications registered with the
// runtime under Install apps, which opens Setup, each a row opening its app
// page (app_page.go). Saved permissions live on
// Permissions and on each app page; provider management stays on Providers.
// Real provider declarations also appear here as service-providing software.
// The banner's row
// sits in a plain wrapper, so hidden wins over the row's grid display.
var servicePage = pageOwnsReady(oaPageHead("status", "Apps", "Apps that use OpenAbstractions.")) +
	`<div id="apps-health" hidden><div class="oa-row"><div class="oa-row-text"><span class="oa-row-label" id="apps-health-label"></span><span class="oa-row-caption" id="apps-health-caption" hidden></span></div>` +
	`<div class="oa-row-control"><button id="apps-health-retry">Check again</button></div>` +
	`<p class="oa-status" id="apps-health-status" role="status" aria-live="polite" data-state="idle"></p></div></div>` +
	oaGroup("service-check", "Your apps", "", strings.Replace(oaRowNav("Install apps", "Browse the Catalog", "/setup#setup-choices"), `<a `, `<a id="apps-setup" `, 1), runtimeEcho(oaStatus("apps-status")), `<div id="apps-desktop-reconnect" hidden><div class="oa-row"><div class="oa-row-text"><span id="apps-desktop-reconnect-title" class="oa-row-label"></span></div><div class="oa-row-control"><button id="apps-desktop-reconnect-button" type="button" data-status="apps-desktop-reconnect-status">Reconnect</button></div></div></div>`, oaStatusLine("apps-desktop-reconnect-status", false), `<div id="apps-integrations"></div><div id="apps"></div>`, oaStatusLine("apps-pending-status", false)) +
	oaGroup("apps-services", "Services", "Running in the background for your apps.", runtimeEcho(oaStatus("apps-providers-status")), `<div id="apps-providers"></div>`, strings.Replace(oaRowNav("Manage services", "", "/registry#providers-section"), `<a `, `<a id="apps-manage-providers" `, 1)) +
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
const setupLink=el('apps-setup');if(setupLink)setupLink.href='/setup?k='+encodeURIComponent(key||'')+'#setup-choices';
// Your apps begins with the integrations on this computer and connected, in
// the Catalog's row shape: name, caption and the state button
// (/setup/integrations, setup_integrations.go); Catalog lists every one. An
// integration's row stands in for the entries it registers.
const appsIntegrations={list:null,failed:false};let appsIntegrationRows=new Map();
function appsIntegrationHref(path){const [page,hash]=String(path||'').split('#');return page+'?k='+encodeURIComponent(key||'')+(hash?'#'+hash:'')}
function appsIntegrationRow(){
 const row=document.createElement('div');row.className='oa-row';
 row.link=document.createElement('a');row.link.className='oa-row-text';
 row.label=document.createElement('span');row.label.className='oa-row-label';row.caption=document.createElement('span');row.caption.className='oa-row-caption';
 row.link.append(row.label,row.caption);
 const control=document.createElement('div');control.className='oa-row-control';
 row.button=document.createElement('button');row.button.type='button';
 row.status=document.createElement('p');row.status.className='oa-status';row.status.setAttribute('role','status');row.status.setAttribute('aria-live','polite');row.status.setAttribute('data-state','idle');
 row.button.onclick=()=>appsIntegrationAct(row);control.append(row.button);row.append(row.link,control,row.status);return row;
}
async function appsIntegrationAct(row){const g=row.integration;if(!g)return;
 if(g.state!=='open'||!g.open){location.href=row.link.href;return}
 row.button.disabled=true;appsSay(row,'loading','Opening '+g.title+'…');
 try{await call(g.open,{confirm:true});appsSay(row,'idle','')}catch(e){appsSay(row,'error',oaPlain(e&&e.message||String(e)).line)}
 row.button.disabled=false}
// Apps shows the integrations connected here, and one whose last connection
// attempt needs attention, so its reason is in view.
const appsIntegrationsShown=()=>(appsIntegrations.list||[]).filter(g=>g&&g.id&&g.title&&(g.installed&&g.connected||oaIntegrationNeedsAttention(g)));
function appsIntegrationNames(){const names=new Set();for(const g of appsIntegrationsShown())for(const name of g.registrations||[])names.add(name);return names}
function renderAppsIntegrations(){
 const out=el('apps-integrations');if(!out||appsIntegrations.list===null&&!appsIntegrations.failed)return;
 const shown=appsIntegrationsShown(),next=new Map();
 const rows=shown.map(g=>{const row=appsIntegrationRows.get(g.id)||appsIntegrationRow();row.integration=g;row.link.href=appsIntegrationHref(g.page);row.label.textContent=g.title;row.caption.textContent=[g.caption,oaIntegrationLine(g)].filter(Boolean).join(' · ');
  row.button.textContent=g.state==='open'&&g.open?'Open':g.connected?'Manage':'Connect';row.button.disabled=false;row.button.setAttribute('aria-label',row.button.textContent+' '+g.title);next.set(g.id,row);return row});
 appsIntegrationRows=next;
 renderApps();
 if(rows.length&&rows.length===out.children.length&&rows.every((row,i)=>out.children[i]===row))return;
 out.replaceChildren();
 if(!rows.length&&appsIntegrations.failed){const p=document.createElement('p');p.className='oa-empty';p.textContent='Connected integrations are unknown right now.';out.append(p);return}
 for(const row of rows)out.append(row);
}
async function appsReadIntegrations(signal){
 try{const v=await call('/setup/integrations',undefined,signal);if(signal&&signal.aborted)return;appsIntegrations.list=v.integrations||[];appsIntegrations.failed=false}
 catch(e){if(signal&&signal.aborted)return;appsIntegrations.failed=appsIntegrations.list===null}
 renderAppsIntegrations();
}
const providersLink=el('apps-manage-providers');if(providersLink)providersLink.href='/registry?k='+encodeURIComponent(key||'')+'#providers-section';
const desktopReconnect=el('apps-desktop-reconnect'),desktopReconnectButton=el('apps-desktop-reconnect-button');
let desktopReconnectStep='',desktopReconnectBusy=false;
async function appsReadDesktopReconnect(signal){
 if(!desktopReconnect||!desktopReconnectButton)return;
 try{
  const view=await call('/setup/desktop-integration',undefined,signal);
  if(signal&&signal.aborted)return;
  desktopReconnect.hidden=!view.available&&!view.action;
  desktopReconnectButton.disabled=desktopReconnectBusy||!!view.action;
  el('apps-desktop-reconnect-title').textContent=view.title||'';
  const outcome=view.outcome||{};
  desktopReconnectButton.textContent=!view.action&&outcome.failure?'Retry':'Reconnect';
  if(view.action){desktopReconnectStep=view.action;oaSetStatus('apps-desktop-reconnect-status','loading','Reconnecting…')}
  else if(desktopReconnectStep&&outcome.step_id===desktopReconnectStep){
   desktopReconnectStep='';oaSetStatus('apps-desktop-reconnect-status',outcome.failure?'error':'ok',outcome.failure||'Connection repaired.');
  }
  else if(desktopReconnectStep){desktopReconnectStep='';oaSetStatus('apps-desktop-reconnect-status','idle','Check the connection again in Catalog.')}
  else if(outcome.failure){oaSetStatus('apps-desktop-reconnect-status','error',outcome.failure)}
 }catch(e){if(!desktopReconnectStep)desktopReconnect.hidden=true}
}
if(desktopReconnectButton)desktopReconnectButton.onclick=async()=>{
 if(desktopReconnectBusy||desktopReconnectStep)return;
 desktopReconnectBusy=true;desktopReconnectButton.disabled=true;
 try{
  const accepted=await call('/setup/desktop-integration?reconnect=1',{confirm:true},appsRun&&appsRun.signal);desktopReconnectStep=accepted.step_id;
  oaSetStatus('apps-desktop-reconnect-status','loading','Reconnecting…');
  const until=Date.now()+12*60*1000;
  while(desktopReconnectStep&&appsRun&&!appsRun.signal.aborted&&Date.now()<until){await appsReadDesktopReconnect(appsRun.signal);if(desktopReconnectStep)await appsPause(appsRun.signal,2000)}
  if(desktopReconnectStep&&appsRun&&!appsRun.signal.aborted){desktopReconnectStep='';oaSetStatus('apps-desktop-reconnect-status','idle','Check the connection again in Catalog.')}
 }catch(err){oaSetStatus('apps-desktop-reconnect-status','error',oaPlain(err&&err.message||String(err)).line)}
 finally{desktopReconnectBusy=false;await appsReadDesktopReconnect(appsRun&&appsRun.signal)}
};
const appsState={entries:null,cursor:'',failure:'',runtime:''};var appsRun=null;let appsRecovered=false;
const appsProviders={entries:null,cursor:'',failure:''};
// The server titles each entry (appEntryTitle): a reserved title reads as the
// program's stem, and an entry that added itself says so until it is kept.
function appsTitle(e){const d=e.application&&e.application.Descriptor;return e.title||d&&d.Title||e.name}
function appsRunning(e){return !!(e.application&&e.application.Instances&&e.application.Instances.length)}
function appsHref(name){return '/app/'+encodeURIComponent(name)+'?k='+encodeURIComponent(key)}
// Rows are kept by registered name and updated in place, so a reread keeps
// the row a person is on.
let appsRows=new Map();
let appsProviderRows=new Map();
function appsRow(){
 const a=document.createElement('a');a.className='oa-row oa-row-nav';
 const text=document.createElement('span');text.className='oa-row-text';
 a.label=document.createElement('span');a.label.className='oa-row-label';a.caption=document.createElement('span');a.caption.className='oa-row-caption';
 text.append(a.label,a.caption);const chevron=document.createElement('span');chevron.className='oa-row-chevron';chevron.setAttribute('aria-hidden','true');
 a.append(text,chevron);return a;
}
// appsOpenRow is the row of an app OpenAbstractions can open: its name opens
// the app's page, and Open beside it opens that page with open=1, which
// opens the app there (app_page.go).
function appsOpenRow(){
 const row=document.createElement('div');row.className='oa-row';
 row.link=document.createElement('a');row.link.className='oa-row-text';
 row.label=document.createElement('span');row.label.className='oa-row-label';row.caption=document.createElement('span');row.caption.className='oa-row-caption';
 row.link.append(row.label,row.caption);
 const control=document.createElement('div');control.className='oa-row-control';
 row.open=document.createElement('button');row.open.type='button';row.open.textContent='Open';
 row.status=document.createElement('p');row.status.className='oa-status';row.status.setAttribute('role','status');row.status.setAttribute('aria-live','polite');row.status.setAttribute('data-state','idle');
 row.open.onclick=()=>{appsSay(row,'loading','Opening '+row.label.textContent+'…');location.href=row.link.href+'&open=1'};
 control.append(row.open);row.append(row.link,control,row.status);row.openable=true;return row;
}
function appsCanOpen(e){const d=e.application&&e.application.Descriptor;return !!(d&&d.Activation)}
// An app its installer asked to connect, and that OpenAbstractions did not
// connect, waits as a Not connected row above the registered apps, with
// Connect and Learn more (program, publisher). A name another app holds
// shows, on the first Connect click, the one line naming the app it
// replaces; the button then reads Replace, and only that second click
// replaces it (/apps/pending/connect).
const appsPending={entries:[]};let appsPendingRows=new Map();
function appsSay(row,state,sentence){row.status.textContent=sentence;oaStatusState(row.status,state)}
function appsPendingRow(){
 const row=document.createElement('div');row.className='oa-row';
 const text=document.createElement('div');text.className='oa-row-text';
 row.label=document.createElement('span');row.label.className='oa-row-label';
 row.caption=document.createElement('span');row.caption.className='oa-row-caption';row.caption.textContent='Not connected';
 text.append(row.label,row.caption);
 const control=document.createElement('div');control.className='oa-row-control';
 row.connect=document.createElement('button');row.connect.className='oa-primary';row.connect.textContent='Connect';control.append(row.connect);
 row.replaces=document.createElement('p');row.replaces.className='oa-row-caption';row.replaces.hidden=true;
 row.status=document.createElement('p');row.status.className='oa-status';row.status.setAttribute('role','status');row.status.setAttribute('aria-live','polite');row.status.setAttribute('data-state','idle');
 const more=document.createElement('details');more.className='oa-expander';
 const summary=document.createElement('summary');summary.textContent='Learn more';
 const chevron=document.createElement('span');chevron.className='oa-row-chevron';chevron.setAttribute('aria-hidden','true');summary.append(chevron);
 row.more=document.createElement('div');row.more.className='oa-expander-body';more.append(summary,row.more);
 row.append(text,control,row.replaces,row.status,more);
 row.connect.onclick=()=>appsConnect(row);
 return row;
}
function appsPendingUpdate(row,e){
 if(row.entry&&(row.entry.digest!==e.digest||row.entry.shown!==e.shown)){row.armed=false;row.replaces.hidden=true;row.connect.textContent='Connect'}
 row.entry=e;row.label.textContent=e.title;
 const lines=[e.program?'Program: '+e.program:'',e.publisher].filter(Boolean);
 if(row.lines!==lines.join('\n')){row.lines=lines.join('\n');row.more.replaceChildren();for(const line of lines)para(row.more,line)}
}
async function appsConnect(row){
 const e=row.entry;
 if(e.replaces&&!row.armed){row.armed=true;row.replaces.textContent='This replaces '+e.replaces+' connected now.';row.replaces.hidden=false;row.connect.textContent='Replace';return}
 row.connect.disabled=true;
 try{const v=await call('/apps/pending/connect',{name:e.name,digest:e.digest,shown:e.shown||''});
  appsPending.entries=appsPending.entries.filter(x=>x.name!==e.name);renderApps();oaSetStatus('apps-pending-status','ok',v.message)}
 catch(err){appsSay(row,'error',oaPlain(err&&err.message||String(err)).line);row.connect.disabled=false;row.armed=false;row.replaces.hidden=true;row.connect.textContent='Connect';await appsPendingRead();renderApps()}
}
async function appsPendingRead(signal){
 try{const v=await call('/apps/pending',undefined,signal);if(v.outcome==='page')appsPending.entries=v.entries||[]}catch(e){}
}
function renderApps(){
 const out=el('apps');if(appsState.entries===null)return;
 const waiting=appsPending.entries.filter(e=>e&&e.name&&e.title).sort((a,b)=>a.name<b.name?-1:a.name>b.name?1:0),pendingNext=new Map();
 const pendingRows=waiting.map(e=>{const row=appsPendingRows.get(e.name)||appsPendingRow();appsPendingUpdate(row,e);pendingNext.set(e.name,row);return row});
 appsPendingRows=pendingNext;
 const integrated=appsIntegrationNames(),entries=appsState.entries.filter(e=>e&&e.role==='application'&&e.name&&!integrated.has(e.name)).sort((a,b)=>appsTitle(a).localeCompare(appsTitle(b))),next=new Map();
 const rows=pendingRows.concat(entries.map(e=>{const open=appsCanOpen(e),kept=appsRows.get(e.name),row=kept&&!!kept.openable===open?kept:open?appsOpenRow():appsRow();if(open){row.link.href=appsHref(e.name);row.open.hidden=appsRunning(e);row.open.setAttribute('aria-label','Open '+appsTitle(e))}else row.href=appsHref(e.name);row.label.textContent=appsTitle(e);row.caption.textContent=(e.self_registered?'Review needed · ':'')+(appsRunning(e)?'Running':'Not running');next.set(e.name,row);return row}));
 appsRows=next;
 if(rows.length&&rows.length===out.children.length&&rows.every((row,i)=>out.children[i]===row))return;
 out.replaceChildren();
 if(!rows.length){if(appsIntegrationsShown().length)return;const p=document.createElement('p');p.className='oa-empty';p.textContent='No apps yet.';out.append(p);return}
 for(const row of rows)out.append(row);
}
// Each readiness state in the words of the ruling of 2026-11-09.
const appsProviderStates={ready:'Running',idle:'Starts when needed',starting:'Starting',restarting:'Restarting',refused:'Refused to start',unreachable:'Not responding',not_ready:'Not ready',disabled:'Turned off'};
function appsProviderState(state){return appsProviderStates[state]||'State unknown'}
function renderAppsProviders(){
 const out=el('apps-providers');if(appsProviders.entries===null)return;
 const entries=appsProviders.entries.filter(e=>e&&e.role==='provider'&&e.name&&e.program&&e.provider).sort((a,b)=>a.name.localeCompare(b.name)),next=new Map();
 const rows=entries.map(e=>{const row=appsProviderRows.get(e.name)||appsRow();row.href=providersLink.href;row.label.textContent=e.title||e.name;
  row.caption.textContent=(e.does?e.does+' · ':'')+appsProviderState(e.state);next.set(e.name,row);return row});
 appsProviderRows=next;
 if(rows.length&&rows.length===out.children.length&&rows.every((row,i)=>out.children[i]===row))return;
 out.replaceChildren();
 if(!rows.length){const p=document.createElement('p');p.className='oa-empty';p.textContent='No services yet.';out.append(p);return}
 for(const row of rows)out.append(row);
}
// The banner names the first failure: a service the runtime resolves, or
// the registry read. It stays hidden while everything works.
// appsHealthText writes a sentence as the banner's label, its first
// sentence without the period, and the rest as its caption ("OpenAbstractions
// is not running" / "Open OpenAbstractions from the Start menu, then check
// again"; architect ruling
// 2026-11-09, second block).
function appsHealthText(sentence){const at=sentence.indexOf('. '),label=at>0?sentence.slice(0,at):sentence.replace(/[.]$/,''),caption=at>0?sentence.slice(at+2).replace(/[.]$/,''):'';el('apps-health-label').textContent=label;el('apps-health-caption').textContent=caption;el('apps-health-caption').hidden=!caption}
function renderAppsHealth(){
 const problem=appsState.runtime||appsState.failure||appsProviders.failure,banner=el('apps-health');
 // A stopped runtime is the shell's banner (runtimeScript); every list
 // failing with it is that same news.
 if(typeof oaRuntimeIsDown==='function'&&oaRuntimeIsDown()){banner.hidden=true;return}
 if(problem){banner.hidden=false;appsHealthText(problem);appsRecovered=false;return}
 if(appsRecovered){appsHealthText('OpenAbstractions works again.');return}
 banner.hidden=true;
}
const appsServiceProblem={unavailable:1,incompatible:1};
async function appsCheckRuntime(){
 try{const v=await call('/runtime');
  // Not running is the shell's banner (runtimeScript), as on every page.
  if(typeof oaRuntimeMark==='function')oaRuntimeMark(!!v.error);
  if(v.error)appsState.runtime='';
  else{const failing=(v.capabilities||[]).find(c=>appsServiceProblem[c.status]);appsState.runtime=failing?oaPlain('service resolution: '+failing.status+': '+failing.contract).line:''}
 }catch(e){appsState.runtime='OpenAbstractions is not reachable. '+oaStartAgain()+'.'}
 renderAppsHealth();
}
async function appsRead(signal,wait,source='applications'){
 const state=source==='providers'?appsProviders:appsState,status=source==='providers'?'apps-providers-status':'apps-status';
 const query=new URLSearchParams({source,cursor:state.cursor,wait_ms:String(wait)});
 const page=await call('/apps/registrations?'+query,undefined,signal);
 if(signal&&signal.aborted)return;
 if(page.outcome!=='page'&&page.outcome!=='stale')throw Object.assign(Error('Registered apps: '+page.outcome),{outcome:page.outcome});
 if(source==='applications')await appsPendingRead(signal);
 if(signal&&signal.aborted)return;
 state.cursor=page.cursor||'';state.entries=page.entries||[];state.failure='';
 oaSetStatus(status,'idle','');if(source==='providers')renderAppsProviders();else renderApps();renderAppsHealth();
}
function appsPause(signal,ms){return new Promise(resolve=>{if(signal&&signal.aborted){resolve();return}const done=()=>{clearTimeout(timer);if(signal)signal.removeEventListener('abort',done);resolve()},timer=setTimeout(done,ms);if(signal)signal.addEventListener('abort',done,{once:true})})}
let appsFirstReadDone=null;const appsFirstSources=new Set(),appsFirstRead=new Promise(resolve=>{appsFirstReadDone=source=>{appsFirstSources.add(source);if(appsFirstSources.size===2)resolve()}});
async function appsFollow(run,source='applications'){
 const state=source==='providers'?appsProviders:appsState,status=source==='providers'?'apps-providers-status':'apps-status',label=source==='providers'?'providers':'apps';
 let wait=0;
 for(;;){
  if(run&&run.signal.aborted)return;
  try{await appsRead(run&&run.signal,wait,source);wait=30000;appsFirstReadDone(source)}
  catch(e){
   if(run&&run.signal.aborted)return;
   state.failure=e.outcome==='forbidden'?'This Panel may not read the list of '+label+'.':'The list of '+label+' is unavailable right now.';
   if(state.entries===null)oaSetStatus(status,'error',state.failure);
   renderAppsHealth();state.cursor='';wait=0;appsFirstReadDone(source);
   if(!run)return;await appsPause(run.signal,5000);
  }
  if(!run)return;
 }
}
appsRun=typeof AbortController==='undefined'?null:new AbortController();
var appsReady=appsFollow(appsRun),appsProvidersReady=appsFollow(appsRun,'providers'),appsHealthReady=appsCheckRuntime();
// The screenshot tool waits for data-ready once the first reads finished.
var appsArrived=Promise.all([appsFirstRead,appsHealthReady,appsReadDesktopReconnect(appsRun&&appsRun.signal),appsReadIntegrations(appsRun&&appsRun.signal)]).then(()=>{if(typeof oaReady==='function')oaReady()});
// appsReread checks the runtime and reads again each list that failed: Check
// again, and the shell's call once the runtime answers again.
async function appsReread(){
 await appsCheckRuntime();
 await appsReadDesktopReconnect(appsRun&&appsRun.signal);
 await appsReadIntegrations(appsRun&&appsRun.signal);
 if(appsState.failure){appsState.cursor='';try{await appsRead(appsRun&&appsRun.signal,0)}catch(e){}}
 if(appsProviders.failure){appsProviders.cursor='';try{await appsRead(appsRun&&appsRun.signal,0,'providers')}catch(e){}}
}
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(appsReread);
el('apps-health-retry').onclick=async()=>{
 const before=appsState.runtime||appsState.failure||appsProviders.failure;
 await appsReread();
 const after=appsState.runtime||appsState.failure||appsProviders.failure;
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
function runtimeSentence(v){if(v.explicit)return 'Connected to the OpenAbstractions this Panel was started for.';if(v.bootstrap!=='unknown')return 'Connected to the installed OpenAbstractions.';return 'This Panel is a development build; no installed OpenAbstractions was found next to it.'}
const capabilityPlainStatus={unavailable:1,forbidden:1,incompatible:1,invalid_request:1};
function capabilityLine(c){if(!capabilityPlainStatus[c.status])return c.name+': '+c.label;return c.name+': '+oaPlain('service resolution: '+c.status+': '+c.contract).line}
function renderRuntime(v){const out=el('runtime');out.replaceChildren();para(out,v.error?'OpenAbstractions is not reachable. Try again or restart OpenAbstractions.':'Connected to OpenAbstractions. Review an app’s pending request on Permissions if it needs access.');const details=oaTechnical(document.createElement('details'));const summary=document.createElement('summary');summary.textContent='Technical details';details.append(summary);para(details,runtimeSentence(v));if(v.detail&&!v.explicit)para(details,v.detail);for(const c of v.capabilities||[])statusPara(details,capabilityLine(c),c.status==='forbidden'?'error':'idle');const pre=document.createElement('pre');pre.textContent=JSON.stringify(v,null,2);details.append(pre);out.append(details)}
// Check the service used to write nothing a second click could tell apart
// from the first: renderRuntime always rebuilds the same content from the
// same reply, with no line saying a check had actually just run (task
// 2026-09-23, thirteenth first-time visitor, finding 1). One line beside
// the button now names when this click's own answer came back.
// runtimeSupportStart binds Check the service once its row is on the page
// (Settings renders it only when Show developer tools is on).
function runtimeSupportStart(){el('status').onclick=async()=>{try{const v=await call('/runtime');renderRuntime(v);el('check-status').textContent='Checked '+oaLocalTime(new Date())+'.'}catch(e){el('runtime').replaceChildren();fail('check-status',e)}};}
`
