package main

// cardPage is the Panel's Device page (research/panel-redesign/SPEC.md §4,
// S6): the graphics card, who keeps the device awake, and storage. For each
// resource the table reports, one held sentence and one row per holder,
// verified and claimed amounts kept apart and never summed; the raw reading
// sits behind Technical details. Each graphics driver reset of the last week
// is one row with its fault's sentence (RES-G6, device_faults.go). The wake
// list is one row per app that holds the wake lease or has a rule on it,
// with a switch that allows or stops it (device_wake.go). The sections read
// on arrival and again every few seconds while the page is visible, so the
// page has no Refresh button. Storage is one row per class with its keep
// rule (storage_page.go). Reads use /card/* with the panel key.
var cardPage = oaPageHead("card", "Device", "This device's graphics card, sleep and storage.") +
	runtimeGroup(oaGroup("holders", "Who uses the graphics card", "",
		`<p id="device-answer" role="status" aria-live="polite">Reading the graphics card…</p><div id="gpu-faults"></div><p id="gpu-faults-status" class="oa-status" role="status" aria-live="polite">Checking the log for graphics driver resets…</p><div id="resources"></div><p id="table-status" class="oa-status" role="status" aria-live="polite"></p><div data-ready-owner="page" hidden></div>`)) +
	runtimeGroup(oaGroup("awake-section", "Keeping this device awake", "Apps keep this device awake while they work.",
		oaStatus("awake-status"),
		`<p class="oa-empty" id="awake-empty" hidden>No app is keeping this device awake.</p><div id="awake"></div>`)) +
	runtimeGroup(storageSection) + `
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);function show(id,x){el(id).textContent=typeof x==='string'?x:JSON.stringify(x,null,2)}
async function call(path,body){if(body&&body.candidates&&path.split('?')[0]==='/registry/selection')body=await oaPanelRequest(path,body);const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return oaPanelResponse(path,await r.json())}
function short(program){return oaShortName(program)||'(unknown)'}
function age(iso){if(!iso)return '';const ms=Date.now()-new Date(iso).getTime();if(!Number.isFinite(ms)||ms<0)return '';if(ms<1500)return 'just now';if(ms<90000)return Math.round(ms/1000)+'s ago';return Math.round(ms/60000)+'m ago'}
// sampleAgePhrase reads the sample's own age, singular for one second or
// one minute (task 2026-09-23, ninth and twelfth first-time visitors).
function sampleAgePhrase(iso){if(!iso)return '';const ms=Date.now()-new Date(iso).getTime();if(!Number.isFinite(ms)||ms<0)return '';const secs=Math.round(ms/1000);if(secs<60)return secs<1?'less than a minute ago':secs+' second'+(secs===1?'':'s')+' ago';const mins=Math.round(ms/60000);return mins+' minute'+(mins===1?'':'s')+' ago'}
// deviceAnswer answers one question in one line: who holds the graphics
// card, and can another app take it (hostile critic A11). A holder with a
// lease can be asked to let go (RES-L2); any other holds the card outside
// OpenAbstractions. Verified and claimed amounts are never added up, so the
// line names the largest holder. A holder reads by the name /card/table
// attaches (card_panel.go titleHolders: a title, else the signed file
// description), else by its file stem in words ("Llama server"; architect
// ruling 2026-11-09, second block). An engine host's id (host:<name>) is
// never page text: it reads by the title /card/table attached, else as
// Model server.
function appName(holder){if(holder.title)return holder.title;const host=/^host:(.*)$/.exec(String(holder.program||''));if(host)return oaEngineName(host[1]);return stemWords(holder.program)}
function stemWords(program){return oaNameWords(oaShortName(program))||'Unknown program'}
// detailWords reads a holder's detail or a wake reason as words: a use by
// its Panel word, a single id by oaModelName ("SDXL checkpoint"), and a
// sentence as written.
const useWords={chat:'Chat',image:'Pictures',embed:'Embeddings',transcription:'Transcription',speech:'Speech',live:'Live audio',video:'Video'};
function detailWords(text){const t=String(text||'').trim();if(!/^[a-z0-9][a-z0-9_.:-]*$/.test(t))return t;return Object.prototype.hasOwnProperty.call(useWords,t)?useWords[t]:oaModelName(t)}
function deviceAnswer(resources){const cards=(resources||[]).filter(r=>/^card:/.test(String(r.resource||'')));if(!cards.length)return 'This device reports no graphics card.';const holders=cards.flatMap(r=>r.holders||[]).filter(h=>Number(h.amount)>0).sort((a,b)=>b.amount-a.amount);if(!holders.length)return 'No app is listed as using the graphics card.';const top=holders[0],name=appName(top),others=new Set(holders.slice(1).map(h=>appName(h)).filter(n=>n!==name));let line=name+(top.evidence==='verified'?' is using ':' reports using ')+oaBytes(top.amount)+' of the graphics card.';if(others.size)line+=' '+(others.size===1?'One other app uses':others.size+' other apps use')+' it too.';return line+(top.lease?' OpenAbstractions can ask '+name+' to let go when another app needs room.':' '+name+' holds it outside OpenAbstractions; it may keep it until it closes.')}
function size(bytes){return bytes?oaBytes(bytes):''}
// instrumentLabel names the instrument in words; an unknown one shows as is.
function instrumentLabel(name){const known={'windows-gpu-counters':'Windows GPU counters','linux-fdinfo':'Linux file descriptor info'};return known[name]||''}
// measuredBy names the system that measured, for the one-line status.
function measuredBy(name){const known={'windows-gpu-counters':'Windows','linux-fdinfo':'Linux'};return known[name]||''}
// capacityLabel names an instrument that measures the capacity and no
// holder; every holder under it is reported by its app.
function capacityLabel(name){const known={'metal-working-set':"Metal's working-set limit"};return known[name]||''}
// pageRow is one oa-row: label (the full program on its title), caption and
// an optional trailing control.
function pageRow(label,title,caption,control){const row=document.createElement('div');row.className='oa-row';const text=document.createElement('div');text.className='oa-row-text';const l=document.createElement('span');l.className='oa-row-label';const c=document.createElement('span');c.className='oa-row-caption';text.append(l,c);row.append(text);row.label=l;row.caption=c;if(control){const box=document.createElement('div');box.className='oa-row-control';box.append(control);row.append(box)}setRow(row,label,title,caption);return row}
// setRow rewrites a row's label and caption in place, leaving its control
// and status line as they are.
function setRow(row,label,title,caption){row.label.textContent=label;row.label.title=title||'';row.caption.textContent=caption||'';row.caption.hidden=!caption}
// holderRow names the holder's app, then its amount and whether the
// instrument measured it or the holder claimed it, since when and why.
// A measured graphics card row OpenAbstractions doesn't manage (held is
// empty) also carries ruling 5's sentence (architect ruling 2026-11-12) in
// whole GB of 2^30, as the load refusal words it. It needs a program with a
// path, which the person can close, and at least half a GB, so the rounded
// amount is true. The program reads by appName: its registered title, else
// its verified file description (/card/table), else its file stem in words.
// holderRows keeps one row per holder, keyed by resource, program and lease.
const holderRows=new Map();
function foreignWords(resource,h,held){if(!/^card:/.test(String(resource||''))||h.evidence!=='verified'||held||!/[\\/]/.test(String(h.program||''))||!(Number(h.amount)>=2**29))return '';const name=appName(h),gb=Math.max(1,Math.floor((Number(h.amount)+2**29)/2**30));return name+' holds '+gb+" GB that OpenAbstractions doesn't manage. Unload it in "+name+', or close '+name+'.'}
function holderRow(resource,h){const verified=h.evidence==='verified',held=h.lease||'';const amount=size(h.amount);const parts=[amount?amount+(verified?' measured':' reported by app'):(verified?'measured':'reported by app'),h.since&&'since '+oaLocalTime(h.since),detailWords(h.detail),foreignWords(resource,h,held)].filter(Boolean);const id=resource+'\n'+h.program+'\n'+held;let row=holderRows.get(id);if(row)setRow(row,appName(h),h.program,parts.join(' · '));else{row=pageRow(appName(h),h.program,parts.join(' · '));holderRows.set(id,row)}row.setAttribute('data-evidence',verified?'verified':'claimed');return row}
// resourceBlocks keeps one block per resource; a live reread updates its
// sentences and rows in place, and an opened Technical details stays open.
const resourceBlocks=new Map();
function resourceBlock(r){let b=resourceBlocks.get(r.resource);if(!b){b={box:document.createElement('div'),head:pageRow('','',''),rows:document.createElement('div'),none:document.createElement('p'),details:oaTechnical(document.createElement('details')),pre:document.createElement('pre')};b.details.className='oa-expander';b.box.className='resource';b.rows.className='holders';b.none.className='oa-empty';b.none.textContent='No app is listed as holding this resource.';const summary=document.createElement('summary');summary.textContent='Technical details';const chevron=document.createElement('span');chevron.className='oa-row-chevron';chevron.setAttribute('aria-hidden','true');summary.append(chevron);const body=document.createElement('div');body.className='oa-expander-body';body.append(b.pre);b.details.append(summary,body);b.box.append(b.head,b.rows,b.details);resourceBlocks.set(r.resource,b)}
const instrument=instrumentLabel(r.instrument),capacityBy=r.capacity?capacityLabel(r.instrument):'',sample=age(r.observed);const caption=instrument?(size(r.held)||'Nothing')+' held'+(r.capacity?' of '+size(r.capacity):'')+'.':r.capacity?size(r.capacity)+' total. Apps report what they hold.':'Not measured.';
// How it was measured, and when, sits behind Technical details.
const how=instrument?'Measured by '+instrument+(sample?' '+sample:'')+'.':capacityBy?'Capacity from '+capacityBy+(sample?' '+sample:'')+'; apps report what they hold.':'Not measured.';
setRow(b.head,oaResourceLabel(r.resource),r.resource,caption);
oaPlaceRows(b.rows,r.holders.length?r.holders.map(h=>holderRow(r.resource,h)):[b.none]);b.record=r;b.pre.textContent=how+'\n\n'+JSON.stringify(r,null,2);oaRecordDetails(r).then(raw=>{if(b.record===r)b.pre.textContent=how+'\n\n'+raw});return b.box}
async function table(fresh){try{const v=await call('/card/table'+(fresh?'?fresh=1':''));show('device-answer',deviceAnswer(v.resources));oaPlaceRows(el('resources'),v.resources.map(resourceBlock));for(const [id,row] of [...holderRows])if(!row.parentNode||!row.parentNode.parentNode)holderRows.delete(id);const rows=v.resources||[],measured=rows.length>0&&rows.every(r=>instrumentLabel(r.instrument)),capacityOnly=!measured&&rows.length>0&&rows.every(r=>capacityLabel(r.instrument)),sampled=(measured||capacityOnly)&&rows.every(r=>sampleAgePhrase(r.observed))&&sampleAgePhrase(rows[0].observed);show('table-status',capacityOnly?'Apps report what they hold.':!measured?'Not measured.':'Measured by '+(measuredBy(rows[0].instrument)||instrumentLabel(rows[0].instrument))+(sampled?' '+sampled:'')+'.')}catch(e){show('device-answer','The graphics card could not be read.');oaShowError('table-status',e.message)}}
// faultRows keeps one row per graphics driver reset, keyed by its time and
// sentence (RES-G6): the label says when, the caption is the fault's own
// sentence and who reported it.
const faultRows=new Map();
function faultRow(f){const id=f.at+'\n'+f.line;let row=faultRows.get(id);const label='Graphics driver reset '+oaLocalTime(f.at),caption=f.line+(f.by?' Reported by '+f.by+'.':'');if(row)setRow(row,label,'',caption);else{row=pageRow(label,'',caption);faultRows.set(id,row)}return row}
// faults reads the resets of the last seven days. While the log is still
// being read its status line says so, so an empty list never reads as "no
// resets" too early; once read, none shows nothing. A log that cannot be
// read says so in one sentence and leaves the card's own reading in place.
const faultText={reading:'Checking the log for graphics driver resets…'};
async function faults(){let outcome='unavailable',list=[];try{const v=await call('/card/faults');outcome=v.outcome;list=v.faults||[]}catch(e){}const rows=list.map(faultRow);oaPlaceRows(el('gpu-faults'),rows);for(const [id,row] of [...faultRows])if(!rows.includes(row))faultRows.delete(id);show('gpu-faults-status',outcome==='page'?'':faultText[outcome]||'Graphics driver resets could not be read.')}
// wakeRows keeps one row per app, keyed by account and program. The label
// is the app; the caption says whether it keeps the device (and, beside a
// graphics card lease, the display) awake now, else whether it may; the
// switch allows or stops it (device_wake.go).
const wakeRows=new Map();
function wakeAllowed(a){return a.rule==='allowed'||a.rule==='none'&&a.holding}
function wakeCaption(a){if(a.holding)return [(a.display?'Keeping this device and its display awake':'Keeping this device awake')+(a.since?' since '+oaLocalTime(a.since):''),detailWords(a.why)].filter(Boolean).join(' · ');return a.rule==='stopped'?'Not allowed to keep this device awake.':'Allowed to keep this device awake.'}
// wakeFold shows OpenAbstractions' own programs as one row (architect
// ruling 2026-11-09, second block), in the place of the first of them: it
// holds when any holds, its switch is on when any may, and it changes each.
function wakeFold(apps){const own=apps.filter(a=>a.own);if(!own.length)return apps;const holding=own.filter(a=>a.holding),first=holding[0]||own[0],rule=own.some(a=>a.rule==='allowed')?'allowed':own.some(a=>a.rule==='none')?'none':'stopped';
 const merged={program:first.program,account:first.account,title:'OpenAbstractions',own:true,programs:own,holding:holding.length>0,display:holding.some(a=>a.display),since:holding.length?first.since:'',why:holding.length?first.why:'',rule};
 const at=apps.indexOf(own[0]);return apps.filter((a,i)=>!a.own||i===at).map(a=>a.own?merged:a)}
function wakeRow(a){const id=a.own?'own':(a.account||'')+'\n'+a.program,name=appName(a);let row=wakeRows.get(id);if(row)setRow(row,name,a.program,wakeCaption(a));else{const sw=document.createElement('input');sw.type='checkbox';sw.className='switch';row=pageRow(name,a.program,wakeCaption(a),sw);row.switch=sw;sw.onchange=()=>wakeSet(row,sw.checked);const button=document.createElement('button');button.type='button';button.textContent='Permissions…';row.children[1].append(button);row.permissions=button;button.onclick=()=>wakePermissions(row);const status=document.createElement('p');status.className='oa-status';status.setAttribute('role','status');status.setAttribute('aria-live','polite');row.append(status);row.status=status;wakeRows.set(id,row)}
row.app=a;row.switch.setAttribute('aria-label','Allow '+name+' to keep this device awake');if(!row.busy)row.switch.checked=wakeAllowed(a);return row}
// wakePermissions opens the app's rules on Permissions, where its wake rule
// and every other one can be changed or removed.
function wakePermissions(row){const a=row.app;row.status.textContent='Opening permissions for '+appName(a)+'.';const q=new URLSearchParams({k:key,program:a.program||''});if(a.account)q.set('account',a.account);location.href='/rights?'+q+'#rights-section'}
// wakeSet allows (a permit) or stops (a deny) one app; the runtime ends a
// stopped app's wake leases (RES-A1). Permissions… is the way back to the
// default. The row says what changed in one sentence, or that nothing did;
// a row the reread drops leaves its sentence in the section's status line.
// A row naming no account or program changes nothing and points at
// Permissions….
const wakeText={conflict:'The permissions kept changing; nothing changed. Try again.',forbidden:'This Panel may not change permissions; nothing changed.',invalid:'The rights service refused this change; nothing changed.',unavailable:'The rights service could not be reached; nothing changed.'};
async function wakeSet(row,allow){const a=row.app,name=appName(a),sw=row.switch,targets=(a.programs||[a]).filter(x=>x.account&&x.program);if(!targets.length){sw.checked=!allow;row.status.textContent=name+' names no account here; use Permissions… instead.';return}row.busy=true;sw.disabled=true;try{let r={outcome:'unchanged'};for(const x of targets){r=await call('/card/wake',{account:x.account,program:x.program,allow});if(r.outcome!=='applied'&&r.outcome!=='unchanged')break}if(r.outcome==='applied'||r.outcome==='unchanged'){const sentence=allow?name+' may keep this device awake.':name+' may no longer keep this device awake.';row.status.textContent=sentence;row.busy=false;await wake();if(![...wakeRows.values()].includes(row))oaSetStatus('awake-status','ok',sentence)}else{sw.checked=!allow;row.status.textContent=wakeText[r.outcome]||'Nothing changed. Try again.'}}catch(e){sw.checked=!allow;row.status.textContent=oaPlain(e.message||String(e)).line}finally{row.busy=false;sw.disabled=false}}
// wakeRulesText says why the list names only the apps holding wake now.
const wakeRulesText={forbidden:'This Panel may not read permissions, so only apps keeping this device awake now are listed.',unavailable:'Permissions could not be read, so only apps keeping this device awake now are listed.'};
async function wake(){try{const v=await call('/card/wake');if(v.outcome!=='page'){oaPlaceRows(el('awake'),[]);wakeRows.clear();el('awake-empty').hidden=true;oaShowError('awake-status',v.outcome+(v.reason?': '+v.reason:''));return}oaSetStatus('awake-status','ok',v.rules==='read'?'':wakeRulesText[v.rules]||wakeRulesText.unavailable);el('awake-empty').hidden=v.apps.length>0;const rows=wakeFold(v.apps).map(wakeRow);oaPlaceRows(el('awake'),rows);for(const [id,row] of [...wakeRows])if(!rows.includes(row))wakeRows.delete(id)}catch(e){oaShowError('awake-status',e.message)}}
// Both sections update live while the page is visible: the first read asks
// the instrument fresh, later ones stand on its last sample.
function storageItems(id,items,empty){
 const rows=(items||[]).map(item=>{
  const row=document.createElement('div'),text=document.createElement('div'),name=document.createElement('span'),caption=document.createElement('span'),size=document.createElement('span');
  row.className='oa-row';text.className='oa-row-text';name.className='oa-row-label';caption.className='oa-row-caption';size.className='oa-row-control';
  name.textContent=item.name+(item.locations?' · '+item.locations+' copies':'');
  const places=item.places?.join(', ')||item.store||'';
  const holders=item.listed_by?.length?'Listed by '+item.listed_by.join(', '):'';
  caption.textContent=[places,holders].filter(Boolean).join(' · ');
  size.textContent=oaBytes(item.bytes)+(item.locations?' each':'');
  text.append(name);if(caption.textContent)text.append(caption);row.append(text,size);return row;
 });
 if(!rows.length){const row=document.createElement('p');row.textContent=empty;rows.push(row)}
 el(id).replaceChildren(...rows)
}
let storageRemovalTarget='',storageRemovalPending=false;
const storageRemovalCursors=[''];
let storagePaging=false;
function storageRemovalPage(items,next){
 const previous=el('storage-committed-prev'),forward=el('storage-committed-next');
 const canPage=()=>!storageRemovalTarget&&!storageRemovalPending&&!storagePaging;
 const showPage=async()=>{storagePaging=true;try{await storage()}finally{storagePaging=false}};
 previous.hidden=storageRemovalCursors.length===1;
 previous.onclick=()=>{if(canPage()&&storageRemovalCursors.length>1){storageRemovalCursors.pop();return showPage()}};
 forward.hidden=!next;
 forward.onclick=()=>{if(canPage()&&next){storageRemovalCursors.push(next);return showPage()}};
 storageRemovalRows(items);
}
function storageRemovalRows(items){
 const section=el('storage-committed');
 if(!section)return;
 const list=el('storage-committed-rows');
 section.hidden=!(items||[]).length&&storageRemovalCursors.length===1;
 if(storageRemovalTarget||storageRemovalPending)return;
 list.replaceChildren(...(items||[]).map(item=>{
  const row=document.createElement('div'),label=document.createElement('span'),control=document.createElement('button');
  row.className='oa-row';label.textContent=item.name+' - '+oaBytes(item.bytes);row.append(label);
  if(item.needs_administrator){const status=document.createElement('span');status.textContent='Needs administrator';row.append(status)}
  if(item.ready&&!item.needs_administrator){control.type='button';control.textContent='Remove';control.addEventListener('click',()=>{
   if(storageRemovalTarget||storageRemovalPending)return;
   storageRemovalTarget=item.target;
   const confirm=document.createElement('button'),cancel=document.createElement('button');
   label.textContent='Remove '+oaBytes(item.bytes)+"? It can't be undone.";
   confirm.type=cancel.type='button';confirm.textContent='Remove';cancel.textContent='Cancel';
   cancel.addEventListener('click',()=>{if(storageRemovalPending)return;storageRemovalTarget='';storageRemovalRows(items)});
   confirm.addEventListener('click',async()=>{
    if(storageRemovalPending||storageRemovalTarget!==item.target)return;
    storageRemovalPending=true;confirm.disabled=cancel.disabled=true;
    try{
     const removal=await call('/card/storage/remove',{target:item.target});
     const status={removed:'Removed '+item.name+' ('+oaBytes(item.bytes)+')',held:'Used by '+(removal.holders||[]).join(', '),busy:'Open in a running program',unavailable:'Still checking. Try again in a minute.',not_found:'The item is gone.',forbidden:'Removal was refused.',unsupported:'This item cannot be removed here.'};
     show('storage-removal-status',status[removal.outcome]||'Removal was refused.');
    }catch(e){show('storage-removal-status',e.message)}finally{storageRemovalTarget='';storageRemovalPending=false;await storage()}
   });
   row.replaceChildren(label,confirm,cancel);
  });row.append(control)}
  return row;
 }));
}
async function storage(){const cursor=storageRemovalCursors.at(-1);try{const v=await call('/card/storage'+(cursor?'?committed='+encodeURIComponent(cursor):''));for(const row of v.classes||[])show('storage-size-'+row.class,oaBytes(row.bytes));storageItems('storage-large',v.large,'No large items reported.');storageItems('storage-matches',v.matches,'No verified matches reported.');if(cursor===storageRemovalCursors.at(-1))storageRemovalPage(v.committed,v.committed_next);el('storage-items').hidden=false;const disk=v.disk;el('storage-free').hidden=!disk;show('storage-free',disk?oaBytes(disk.free_bytes)+' free on '+disk.volume:'');oaSetStatus('storage-status','ok',v.unclassified?oaBytes(v.unclassified)+" isn't sorted into a type yet.":'')}catch(e){el('storage-items').hidden=true;el('storage-free').hidden=true;for(const name of ['data','tmp','cache'])show('storage-size-'+name,'Size unavailable');oaShowError('storage-status',e.message)}}
async function storageLogs(){try{const v=await call('/card/storage/logs');if(v.outcome!=='available'||!v.usage)throw Error('Log size and limits are unavailable.');const u=v.usage;show('storage-size-logs',oaBytes(u.retained_bytes));show('storage-keep-logs',(u.max_use!==undefined?'Up to '+oaBytes(u.max_use):'No size limit')+(u.max_retention_ms!==undefined?'; kept up to '+(u.max_retention_ms/86400000).toLocaleString(undefined,{maximumSignificantDigits:4})+' days.':'; no age limit.')+(u.oldest?' Oldest log: '+oaLocalTime(u.oldest)+'.':' No logs.'))}catch(e){show('storage-size-logs','Size unavailable');show('storage-keep-logs','Kept within the configured size and age limits.')}}
const liveEveryMs=5000;let liveTimer=null,cardBusy=false;
// livePass skips a tick while the previous pass is still reading.
function livePass(){if(cardBusy)return;cardBusy=true;Promise.all([table(false),faults(),wake(),storage(),storageLogs()]).finally(()=>{cardBusy=false})}
function liveStop(){if(liveTimer){clearInterval(liveTimer);liveTimer=null}}
function liveStart(){if(liveTimer)return;liveTimer=setInterval(()=>{if(document.visibilityState!=='visible'){liveStop();return}livePass()},liveEveryMs)}
document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible'&&!liveTimer){livePass();liveStart()}});
var cardReady=Promise.all([wake(),table(true),faults(),storage(),storageLogs()]).then(liveStart);
// The page is ready for screenshots once its first reads finish, whether
// they succeeded or failed (oaReady, panel_shell.go).
cardReady.catch(()=>{}).finally(()=>oaReady());
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(livePass);
</script>`
