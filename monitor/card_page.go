package main

// cardPage is the Panel's card view: for each resource the table reports,
// capacity, held, instrument, observed age, and the holder rows, verified and
// claimed kept visually apart and never summed; below it, what is being
// fetched and who holds the machine awake. It renders every value as text and
// calls only /card/*, /account-work with the panel key.
const cardPage = `<h1 id="holders">Device</h1><p class="oa-note">The graphics card, downloads in progress, and what keeps this device awake.</p><p>Who's using a limited resource on this device right now: the app, the account, how much, and whether it was measured or just claimed. Verified rows are measured. A claimed row's amount is 0 unless the row is a lease, and then it is the grant the runtime wrote; a claimed amount is never added to a verified total. On a computer whose graphics card shares the main memory, the same bytes can appear under both. The numbers are not added up. Seeing this needs the rule <span title="abstraction.resource/table.read">resource table read</span>.</p>
<div><button id="refresh">Refresh</button><label><input type="checkbox" class="switch" id="auto" checked> Auto-refresh while this page is visible</label></div><p id="table-status" class="oa-note"></p>
<div id="resources"></div>
<section id="downloads"><h2>Downloads in progress</h2><p>Background work every app on this account has started. You can't cancel it here; use Work started by apps under Work.</p><button id="work-list">Show downloads</button><pre id="work-status"></pre><div id="work"></div></section>
<section id="awake-section"><h2>Keeping this device awake</h2><p>Apps and accounts keeping this device from sleeping, with how much, since when, and the lease ID. If nothing reports this directly, older account info is shown instead, labeled as such.</p><button id="awake-list">Show</button><pre id="awake-status"></pre><div id="awake"></div></section>
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);function show(id,x){el(id).textContent=typeof x==='string'?x:JSON.stringify(x,null,2)}
async function call(path){const r=await fetch(path,{headers:{'X-Panel-Key':key}});if(!r.ok)throw Error(await r.text());return r.json()}
function short(program){const parts=String(program||'').split(/[\\/]/);return parts[parts.length-1]||program||'(unknown)'}
function age(iso){if(!iso)return '';const ms=Date.now()-new Date(iso).getTime();if(!Number.isFinite(ms)||ms<0)return '';if(ms<1500)return 'just now';if(ms<90000)return Math.round(ms/1000)+'s ago';return Math.round(ms/60000)+'m ago'}
// sampleAgePhrase reads the table's own read caption from the sample's age,
// instead of naming the bound the sample is read within without saying how
// old it actually is (task 2026-09-23, ninth first-time visitor, finding 4).
// sampleAgePhrase used to read "1 seconds ago" for the one value where a
// plural reads wrong (task 2026-09-23, twelfth first-time visitor, minor);
// the same singular case applies to a one-minute age too.
function sampleAgePhrase(iso){if(!iso)return '';const ms=Date.now()-new Date(iso).getTime();if(!Number.isFinite(ms)||ms<0)return '';const secs=Math.round(ms/1000);if(secs<60)return secs<1?'less than a minute ago':secs+' second'+(secs===1?'':'s')+' ago';const mins=Math.round(ms/60000);return mins+' minute'+(mins===1?'':'s')+' ago'}
function gib(bytes){return bytes?(bytes/(1<<30)).toFixed(2)+' GiB':'-'}
function td(content){const cell=document.createElement('td');if(content instanceof Node)cell.append(content);else cell.textContent=content==null||content===''?'-':String(content);return cell}
// selfAccount is this Panel's own account (loadSelf), so a holder or an
// awake-hold row that names this same account reads as "you" rather than a
// Windows security identifier nobody recognizes (task 2026-09-23, first-time
// visitor: "S-1-5-21-..." read as broken).
let selfAccount=null;
function accountLabel(account){return account&&selfAccount&&account===selfAccount?'you':account}
// instrumentLabel names the instrument that measured a resource in words
// (abstraction-resource/go/instrument/instrument.go's NameWindowsGPUCounters,
// NameLinuxFdinfo, NameNone); an instrument this Panel does not yet know
// still shows, unchanged, the same way storeLabel (credentials_page.go)
// treats an unrecognized platform store word.
function instrumentLabel(name){const known={'windows-gpu-counters':'Windows GPU counters','linux-fdinfo':'Linux file descriptor info','none':'no instrument'};return known[name]||name}
// holderRow renders Grant, Since and Detail only for the columns cols marks
// as carrying a value in at least one row of this resource (resourceBlock
// decides cols); a column every row leaves empty is dropped instead of
// printing a dash in every cell (task 2026-09-23: "most columns ... are
// dashes for every row"). Evidence carries no column of its own: every row
// this table reports is verified or claimed (never neither), so the column
// was never actually empty of data, only of visible text, since only its CSS
// class painted a badge and no cell ever held a word a reader (or the
// naive-reader walkthrough) could see (task 2026-09-23, fourth first-time
// visitor: "the Evidence column is empty for every row"). The distinction
// still shows: the Amount cell itself carries the verified/claimed class, so
// a measured amount and a claimed one read in the same color the badge used.
function holderRow(h,cols){const tr=document.createElement('tr');
const program=document.createElement('span');program.className='program';program.textContent=short(h.program);program.title=h.program;
const amount=document.createElement('span');amount.className=h.evidence==='verified'?'verified':'claimed';amount.textContent=gib(h.amount);
const cells=[td(program),td(accountLabel(h.account)),td(amount)];
if(cols.grant)cells.push(td(h.grant));
if(cols.since)cells.push(td(h.since));
if(cols.detail)cells.push(td(h.detail));
tr.append(...cells);
return tr}
// resourceBlock's held line reads as a sentence: the amount held, whether
// the capacity is known, and which instrument measured it and when (task
// 2026-09-23, fourth first-time visitor: "held 1.68 GiB of unknown capacity
// instrument windows-gpu-counters observed just now" read as a field dump,
// not a sentence).
// resourceBlock's heading names the resource through oaResourceLabel, a
// plain phrase naming the hardware, never the raw resource id ("card:0");
// the raw id lives in the heading's title and in this card's own Technical
// details (task 2026-09-23, eighth first-time visitor, finding 5).
// The holders table used to sit beside the Technical details disclosure as
// its own sibling, always visible; toggling Technical details opened and
// closed only the raw JSON beside it, doing nothing the person could see
// happen to the table itself. The details element now wraps the table too,
// so the one disclosure actually controls what it names (task 2026-09-23,
// tenth first-time visitor, finding 5).
// resourceDetailsOpen remembers which resource's own Technical details a
// person opened, by the resource's own raw id: table() rebuilds every
// resourceBlock from scratch on each auto-refresh tick, a fresh <details>
// element each time with no memory of its own, so an opened disclosure
// closed itself again every five seconds with nothing the person did
// (task 2026-09-23, thirteenth first-time visitor, finding 9).
const resourceDetailsOpen=new Set();
function resourceBlock(r){const box=document.createElement('div');box.className='resource';const h=document.createElement('h3');h.textContent=oaResourceLabel(r.resource);h.title=r.resource;const p=document.createElement('p');p.textContent=gib(r.held)+' held'+(r.capacity?' of '+gib(r.capacity)+'.':'; capacity unknown.')+' Measured by '+instrumentLabel(r.instrument)+' '+(age(r.observed)||r.observed)+'.';box.append(h,p);
const details=document.createElement('details');details.open=resourceDetailsOpen.has(r.resource);details.addEventListener('toggle',()=>{if(details.open)resourceDetailsOpen.add(r.resource);else resourceDetailsOpen.delete(r.resource)});const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=JSON.stringify(r,null,2);details.append(summary,pre);
if(!r.holders.length){const none=document.createElement('p');none.textContent='no holder';details.append(none);box.append(details);return box}
const cols={grant:r.holders.some(x=>x.grant),since:r.holders.some(x=>x.since),detail:r.holders.some(x=>x.detail)};
const headers=['Program','Account','Amount'];if(cols.grant)headers.push('Grant');if(cols.since)headers.push('Since');if(cols.detail)headers.push('Detail');
const table=document.createElement('table');const thead=document.createElement('thead');thead.innerHTML='<tr>'+headers.map(x=>'<th>'+x+'</th>').join('')+'</tr>';const tbody=document.createElement('tbody');for(const holder of r.holders)tbody.append(holderRow(holder,cols));table.append(thead,tbody);const wrap=document.createElement('div');wrap.className='oa-table';wrap.append(table);details.append(wrap);box.append(details);return box}
// loadSelf reads this Panel's own account through /explore, the same probe
// endpoint oaGrant already calls, once at load; a page that never gets an
// answer just shows accounts as-is, since accountLabel only substitutes "you"
// once selfAccount is set.
async function loadSelf(){try{const v=await call('/explore');selfAccount=(v.self&&v.self.account)||null}catch(e){}}
async function table(fresh){try{const v=await call('/card/table'+(fresh?'?fresh=1':''));const list=el('resources');list.replaceChildren();for(const r of v.resources)list.append(resourceBlock(r));const sampled=v.resources&&v.resources[0]&&sampleAgePhrase(v.resources[0].observed);show('table-status',fresh?'Read fresh from the instrument.':'Read from the last sample'+(sampled?', taken '+sampled+'.':'.'))}catch(e){oaShowError('table-status',e.message)}}
// Refresh also refreshes the grant widget above the table, the same
// fetch its own arrival check makes, so a rule granted since this page
// opened shows up here without a reload (task 2026-09-23, fifteenth
// first-time visitor, finding 1). The auto-refresh timer below calls
// table() directly, not through here, so it does not also re-check the
// grant widget every five seconds.
el('refresh').onclick=()=>{if(window.oaGrantRefresh)oaGrantRefresh();return table(true)};
let autoTimer=null,autoTicks=0;const autoMax=120,autoEveryMs=5000;
function autoStop(){if(autoTimer){clearInterval(autoTimer);autoTimer=null}}
function autoStart(){autoStop();if(!el('auto').checked)return;autoTicks=0;autoTimer=setInterval(()=>{if(document.visibilityState!=='visible')return;if(++autoTicks>autoMax){autoStop();show('table-status','Auto-refresh stopped after '+autoMax+' reads; use Refresh to continue.');return}table(false)},autoEveryMs)}
el('auto').onchange=autoStart;
document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible'&&el('auto').checked&&!autoTimer)autoStart()});
loadSelf().then(()=>{table(true);autoStart()});
const workText={forbidden:'This panel is not authorized to list the jobs on this account; nothing was shown.',unavailable:'The runtime could not answer; try again.',gap:'The jobs changed while listing; start the list again.'};
async function work(){try{const p=await call('/account-work');el('work').replaceChildren();if(p.Outcome!=='page'){show('work-status',workText[p.Outcome]||oaPlain(p.Outcome).line);return}show('work-status',p.Snapshots.length?(p.Complete?'End of jobs.':'More jobs remain; list again for later pages.'):'Nothing is being fetched.');for(const s of p.Snapshots){const row=document.createElement('p');row.textContent=(s.Label||('Unlabelled job '+s.Receipt.OperationID))+' - '+s.State;el('work').append(row)}}catch(e){oaShowError('work-status',e.message)}}
el('work-list').onclick=work;
function awakeRow(h){const tr=document.createElement('tr');
const program=document.createElement('span');program.className='program';program.textContent=short(h.program);program.title=h.program;
tr.append(td(program),td(accountLabel(h.account)),td(gib(h.amount)),td(h.since),td(h.lease),td(h.why));
return tr}
// The empty case used to read "Source: table." — naming the instrument
// that answered, not the fact the person actually wants: nothing is
// keeping the device awake right now (task 2026-09-23, eleventh
// first-time visitor, minor finding).
async function awake(){try{const v=await call('/card/awake');const box=el('awake');box.replaceChildren();if(v.outcome!=='page'){oaShowError('awake-status',v.outcome+(v.reason?': '+v.reason:''));return}show('awake-status',v.holds.length?'Source: '+v.source+'.':'No program holds this right now.');if(!v.holds.length)return;const table=document.createElement('table');const thead=document.createElement('thead');thead.innerHTML='<tr><th>Program</th><th>Account</th><th>Amount</th><th>Since</th><th>Lease</th><th>Why</th></tr>';const tbody=document.createElement('tbody');for(const h of v.holds)tbody.append(awakeRow(h));table.append(thead,tbody);box.append(table)}catch(e){oaShowError('awake-status',e.message)}}
el('awake-list').onclick=awake;
</script>`
