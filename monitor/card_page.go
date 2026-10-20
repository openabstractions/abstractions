package main

// cardPage is the Panel's Device page (research/panel-redesign/SPEC.md §4,
// S6): the graphics card and who holds the device awake. For each resource
// the table reports, one held sentence and one row per holder, verified and
// claimed amounts kept apart and never summed; the raw reading sits behind
// Technical details. Both sections read on arrival and again every few
// seconds while the page is visible, so the page has no Refresh button. Each
// wake hold is a row saying since when, with a Permissions… action; only
// its holder can end a lease (abstraction-resource CONTRACT.md RES-A1). It calls only /card/* with the
// panel key.
var cardPage = oaPageHead("card", "Device", "This device's graphics card and sleep.") +
	oaGroup("holders", "Who uses the graphics card", "",
		`<p id="device-answer" class="oa-device-answer" role="status" aria-live="polite">Reading the graphics card…</p><div id="resources"></div><p id="table-status" class="oa-status" role="status" aria-live="polite"></p><div data-ready-owner="page" hidden></div>`) +
	oaGroup("awake-section", "Keeping this device awake", "",
		oaStatus("awake-status"),
		`<p class="oa-empty" id="awake-empty" hidden>No app is keeping this device awake.</p><div id="awake"></div>`) + `
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);function show(id,x){el(id).textContent=typeof x==='string'?x:JSON.stringify(x,null,2)}
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function short(program){return oaShortName(program)||'(unknown)'}
function age(iso){if(!iso)return '';const ms=Date.now()-new Date(iso).getTime();if(!Number.isFinite(ms)||ms<0)return '';if(ms<1500)return 'just now';if(ms<90000)return Math.round(ms/1000)+'s ago';return Math.round(ms/60000)+'m ago'}
// sampleAgePhrase reads the sample's own age, singular for one second or
// one minute (task 2026-09-23, ninth and twelfth first-time visitors).
function sampleAgePhrase(iso){if(!iso)return '';const ms=Date.now()-new Date(iso).getTime();if(!Number.isFinite(ms)||ms<0)return '';const secs=Math.round(ms/1000);if(secs<60)return secs<1?'less than a minute ago':secs+' second'+(secs===1?'':'s')+' ago';const mins=Math.round(ms/60000);return mins+' minute'+(mins===1?'':'s')+' ago'}
// deviceAnswer answers one question in one line: who holds the graphics
// card, and can another app take it (hostile critic A11). A holder with a
// lease can be asked to let go (RES-L2); any other holds the card outside
// OpenAbstractions. Verified and claimed amounts are never added up, so the
// line names the largest holder. A holder reads by the registered title
// /card/table attaches (card_panel.go titleHolders), else by its file name.
// An engine host's id (host:<name>) is never page text: it reads by the
// title /card/table attached, else as Model server.
function appName(holder){if(holder.title)return holder.title;if(/^host:/.test(String(holder.program||'')))return 'Model server';return short(holder.program).replace(/\.exe$/i,'')}
function gb(bytes){const n=Number(bytes)||0;return (n>=10e9?Math.round(n/1e9):Math.round(n/1e8)/10)+' GB'}
function deviceAnswer(resources){const cards=(resources||[]).filter(r=>/^card:/.test(String(r.resource||'')));if(!cards.length)return 'This device reports no graphics card.';const holders=cards.flatMap(r=>r.holders||[]).filter(h=>Number(h.amount)>0).sort((a,b)=>b.amount-a.amount);if(!holders.length)return 'Nothing is using the graphics card. Any app can take it.';const top=holders[0],name=appName(top),others=new Set(holders.slice(1).map(h=>appName(h)).filter(n=>n!==name));let line=name+' is using '+gb(top.amount)+' of the graphics card.';if(others.size)line+=' '+(others.size===1?'One other app uses':others.size+' other apps use')+' it too.';return line+(top.lease?' OpenAbstractions can ask '+name+' to let go when another app needs room.':' '+name+' holds it outside OpenAbstractions; it may keep it until it closes.')}
function gib(bytes){return bytes?(bytes/(1<<30)).toFixed(2)+' GiB':''}
// instrumentLabel names the instrument in words; an unknown one shows as is.
function instrumentLabel(name){const known={'windows-gpu-counters':'Windows GPU counters','linux-fdinfo':'Linux file descriptor info','none':'no instrument'};return known[name]||name}
// pageRow is one oa-row: label (the full program on its title), caption and
// an optional trailing control.
function pageRow(label,title,caption,control){const row=document.createElement('div');row.className='oa-row';const text=document.createElement('div');text.className='oa-row-text';const l=document.createElement('span');l.className='oa-row-label';const c=document.createElement('span');c.className='oa-row-caption';text.append(l,c);row.append(text);row.label=l;row.caption=c;if(control){const box=document.createElement('div');box.className='oa-row-control';box.append(control);row.append(box)}setRow(row,label,title,caption);return row}
// setRow rewrites a row's label and caption in place, leaving its control
// and status line as they are.
function setRow(row,label,title,caption){row.label.textContent=label;row.label.title=title||'';row.caption.textContent=caption||'';row.caption.hidden=!caption}
// holderRow names the holder's app, then its amount and whether the
// instrument measured it or the holder claimed it, since when and why.
// holderRows keeps one row per holder, keyed by resource, program and lease.
const holderRows=new Map();
function holderRow(resource,h){const verified=h.evidence==='verified';const amount=gib(h.amount);const parts=[amount?amount+(verified?' measured':' claimed'):(verified?'measured':'claimed'),h.since&&'since '+oaLocalTime(h.since),h.detail].filter(Boolean);const id=resource+'\n'+h.program+'\n'+(h.lease||'');let row=holderRows.get(id);if(row)setRow(row,appName(h),h.program,parts.join(' · '));else{row=pageRow(appName(h),h.program,parts.join(' · '));holderRows.set(id,row)}row.setAttribute('data-evidence',verified?'verified':'claimed');return row}
// resourceBlocks keeps one block per resource; a live reread updates its
// sentences and rows in place, and an opened Technical details stays open.
const resourceBlocks=new Map();
function resourceBlock(r){let b=resourceBlocks.get(r.resource);if(!b){b={box:document.createElement('div'),head:pageRow('','',''),rows:document.createElement('div'),none:document.createElement('p'),details:oaTechnical(document.createElement('details')),pre:document.createElement('pre')};b.box.className='resource';b.rows.className='holders';b.none.className='oa-empty';b.none.textContent='Nothing holds this right now.';const summary=document.createElement('summary');summary.textContent='Technical details';b.details.append(summary,b.pre);b.box.append(b.head,b.rows,b.details);resourceBlocks.set(r.resource,b)}
setRow(b.head,oaResourceLabel(r.resource),r.resource,(gib(r.held)||'Nothing')+' held'+(r.capacity?' of '+gib(r.capacity)+'.':'; capacity unknown.')+' Measured by '+instrumentLabel(r.instrument)+' '+(age(r.observed)||r.observed)+'.');
oaPlaceRows(b.rows,r.holders.length?r.holders.map(h=>holderRow(r.resource,h)):[b.none]);b.pre.textContent=JSON.stringify(r,null,2);return b.box}
async function table(fresh){try{const v=await call('/card/table'+(fresh?'?fresh=1':''));show('device-answer',deviceAnswer(v.resources));oaPlaceRows(el('resources'),v.resources.map(resourceBlock));for(const [id,row] of [...holderRows])if(!row.parentNode||!row.parentNode.parentNode)holderRows.delete(id);const sampled=v.resources&&v.resources[0]&&sampleAgePhrase(v.resources[0].observed);show('table-status',fresh?'Read fresh from the instrument.':'Updated live; last sample taken '+(sampled||'just now')+'.')}catch(e){show('device-answer','The graphics card could not be read.');oaShowError('table-status',e.message)}}
// awakeRow is one wake hold: the app and since when, as text. Only the
// holder ends its own lease (RES-A1), so the row's action opens that app's
// permissions, where keeping awake is allowed or removed.
// awakeRows keeps one row per hold, keyed by lease, else by program.
const awakeRows=new Map();
function awakeRow(h){const id=h.lease||h.program;const caption=['Keeping this device awake'+(h.since?' since '+oaLocalTime(h.since):''),h.why].filter(Boolean).join(' · ');let row=awakeRows.get(id);if(row){setRow(row,appName(h),h.program,caption);row.hold=h;return row}
const button=document.createElement('button');button.type='button';button.textContent='Permissions…';const sw=document.createElement('input');sw.type='checkbox';sw.className='switch';sw.checked=true;sw.setAttribute('aria-label','Keep this device awake');row=pageRow(appName(h),h.program,caption,sw);row.children[1].append(button);row.switch=sw;sw.onchange=()=>{if(!sw.checked)return wakeOff(row)};row.hold=h;const status=document.createElement('p');status.className='oa-status';status.setAttribute('role','status');status.setAttribute('aria-live','polite');row.append(status);row.status=status;

button.onclick=()=>{const hold=row.hold;status.textContent='Opening permissions for '+appName(hold)+'.';const q=new URLSearchParams({k:key,program:hold.program||''});if(hold.account)q.set('account',hold.account);location.href='/rights?'+q+'#rights-section'};if(h.lease)row.title='Lease '+h.lease;awakeRows.set(id,row);return row}
// wakeOff revokes the app's "may hold awake" permission; the rights service
// ends its wake leases before it replies (RES-A1, RIGHTS-R13). A revoke that
// matches no saved rule leaves the revision unchanged (RIGHTS-R7) and says so.
const wakeOffText={conflict:'The permissions changed meanwhile; nothing was turned off. Try again.',forbidden:'You hold no rule to change permissions; nothing was turned off.',invalid:'The rights service refused this change; nothing was turned off.',unavailable:'The rights service could not be reached; nothing was turned off.'};
async function wakeOff(row){const h=row.hold,name=appName(h),sw=row.switch;sw.disabled=true;try{if(!h.account){sw.checked=true;row.status.textContent=name+' names no account here; use Permissions… instead.';return}const page=await call('/rights?cursor=');const revision=page.Revision;const r=await call('/rights',{edit:'revoke',revision,account:h.account,program:h.program,action:'abstraction.resource/hold',resource:'awake'});
if(r.Outcome==='applied'&&r.Revision===revision){sw.checked=true;row.status.textContent='Nothing matched: '+name+' holds no saved permission to keep this device awake, so its hold stays until it ends it.';return}
if(r.Outcome!=='applied'){sw.checked=true;row.status.textContent=wakeOffText[r.Outcome]||'The rights service answered '+r.Outcome+'; nothing was turned off.';return}
const sentence=name+' may no longer keep this device awake.';row.status.textContent=sentence;await awake();oaSetStatus('awake-status','ok',sentence)}catch(e){sw.checked=true;row.status.textContent=e.message||String(e)}finally{sw.disabled=false}}
async function awake(){try{const v=await call('/card/awake');if(v.outcome!=='page'){oaPlaceRows(el('awake'),[]);awakeRows.clear();el('awake-empty').hidden=true;oaShowError('awake-status',v.outcome+(v.reason?': '+v.reason:''));return}oaSetStatus('awake-status','ok','');el('awake-empty').hidden=v.holds.length>0;const rows=v.holds.map(awakeRow);oaPlaceRows(el('awake'),rows);for(const [id,row] of [...awakeRows])if(!rows.includes(row))awakeRows.delete(id)}catch(e){oaShowError('awake-status',e.message)}}
// Both sections update live while the page is visible: the first read asks
// the instrument fresh, later ones stand on its last sample.
const liveEveryMs=5000;let liveTimer=null,cardBusy=false;
// livePass skips a tick while the previous pass is still reading.
function livePass(){if(cardBusy)return;cardBusy=true;Promise.all([table(false),awake()]).finally(()=>{cardBusy=false})}
function liveStop(){if(liveTimer){clearInterval(liveTimer);liveTimer=null}}
function liveStart(){if(liveTimer)return;liveTimer=setInterval(()=>{if(document.visibilityState!=='visible'){liveStop();return}livePass()},liveEveryMs)}
document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible'&&!liveTimer){livePass();liveStart()}});
var cardReady=Promise.all([awake(),table(true)]).then(liveStart);
// The page is ready for screenshots once its first reads finish, whether
// they succeeded or failed (oaReady, panel_shell.go).
cardReady.catch(()=>{}).finally(()=>oaReady());
</script>`
