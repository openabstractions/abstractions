package main

// workPage is the Panel's Activity page (research/panel-redesign/SPEC.md §4,
// S5): what happened, and who allowed it. A timeline of model calls
// (/inference/audit), answers to apps' questions, the person's own and an
// assistant's (/activity/answers, the asks answer audit), and app starts
// (/activity/starts, the application start log; activity_panel.go), the
// Memory lines (/activity/memory, the lease audit; activity_memory.go), narrowed
// by a quick range and a chooser of registered apps; downloads and other
// background work every app started (/account-work, with Cancel); the work
// this Panel started (/inventory); and the runtime's detailed log behind an
// expander (logging_page.go). Everything reads on arrival and again every
// few seconds while the page is visible, so the page has no Refresh button.
// "Show more" appears only past a page. Section ids account-work and work
// keep their reference anchors (help.go). Its rows say job (D1) and the wire
// keeps RequestIdentity until job/acceptance@2,
// research/vocabulary/DECISION.md §4.
var workPage = oaPageHead("work", "Activity", "What happened, and who allowed it.") +
	runtimeGroup(oaGroup("timeline", "Timeline", "",
		`<div data-ready-owner="page" hidden></div>`,
		oaRow("Show", "", `<span id="range" role="group" aria-label="Range"></span><select id="app-filter" aria-label="App"><option value="">All apps</option></select>`, "range-status"),
		oaStatus("timeline-status"),
		`<p class="oa-empty" id="timeline-empty" hidden></p>`,
		oaTable("timeline-table", "Time", "App", "What happened", "Outcome"),
		`<div class="oa-toolbar"><button id="timeline-more" type="button" data-status="timeline-status" hidden>Show more</button></div>`)) +
	runtimeGroup(oaGroup("model-requests", "Model requests", "Model calls from your apps.",
		oaRow("Filter requests", "", `<select id="request-machine" aria-label="Computer"><option value="">All computers</option></select><select id="request-model" aria-label="Model"><option value="">All models</option></select><select id="request-outcome" aria-label="Outcome"><option value="">All results</option></select>`, "request-filter-status"),
		libraryReadingStatus("request-status", "Reading model requests…"), `<p class="oa-empty" id="request-empty" hidden></p>`,
		oaTable("request-table", "Time", "App", "Computer", "Model server", "Model", "Preferred model", "Selection", "Outcome"),
		`<button id="request-more" type="button" data-status="request-status" hidden>Show more requests</button>`,
		`<div id="request-tech" hidden>`+oaTechnicalDetails(`<pre id="request-details"></pre>`)+`</div>`)) +
	messageActivitySection +
	agentExitActivitySection +
	runtimeGroup(oaGroup("account-work", "Downloads and background work", "",
		oaStatus("account-status"),
		`<p class="oa-empty" id="account-empty" hidden>No downloads or background work right now.</p><div id="account"></div>`,
		`<div class="oa-toolbar"><button id="account-more" type="button" data-status="account-status" hidden>Show more</button></div>`)) +
	oaGroup("notifications", "Notifications", "Apps that notified you this week, and the switch that stops each one.",
		oaStatus("notifications-status"),
		`<p class="oa-empty" id="notifiers-empty" hidden>No app has notified you this week.</p><div id="notifiers"></div>`,
		// A shown notification keeps its actions here, so one the person
		// missed stays answerable (actionable-notifications design §4.9).
		`<div id="notifications-open"></div>`,
		oaTable("notifications-table", "Time", "App", "Notification", "State")) +
	runtimeGroup(oaGroup("work", "Started from this Panel", "",
		oaStatus("inventory-status"),
		`<p class="oa-empty" id="records-empty" hidden>This Panel has started no work.</p><div id="records"></div>`,
		`<div class="oa-toolbar"><button id="next" type="button" data-status="inventory-status" hidden>Show more</button></div>`)) +
	oaExpander("Detailed log", loggingSection) +
	`<script>` + serviceCommonScript + messageActivityScript + agentExitActivityScript + workScript + loggingScript + `</script>`

const workScript = `
let binding=null;
async function bind(){if(!binding)binding=await call('/binding');return binding}
// workRow is one oa-row built in the page: a label, an optional caption, the
// row's buttons and its own status line each button writes one sentence to.
function workRow(label,caption,title){const row=document.createElement('div');row.className='oa-row';const text=document.createElement('div');text.className='oa-row-text';const l=document.createElement('span');l.className='oa-row-label';text.append(l);const c=document.createElement('span');c.className='oa-row-caption';text.append(c);const control=document.createElement('div');control.className='oa-row-control';const status=document.createElement('p');status.className='oa-status';status.setAttribute('role','status');status.setAttribute('aria-live','polite');row.append(text,control,status);row.status=status;row.control=control;row.label=l;row.caption=c;setRowText(row,label,caption,title);return row}
// setRowText rewrites a row's label and caption without touching its
// buttons or status line.
function setRowText(row,label,caption,title){row.label.textContent=label;row.label.title=title||'';row.caption.textContent=caption||'';row.caption.hidden=!caption}
function rowButton(row,text,onclick){const b=document.createElement('button');b.type='button';b.textContent=text;b.onclick=onclick;row.control.append(b);return b}

// --- Timeline: model calls, answers and app starts, newest first. ---
const timelineRanges=[['hour','Last hour',3600e3],['today','Today','today'],['week','7 days',7*86400e3]];
const rangeSentence={hour:'the last hour',today:'today',week:'the last 7 days'};
const timelinePage=50;
let rangeKey='hour',appFilter='',timelineShown=timelinePage,auditCursor=0,auditEntries=[],answers=[],starts=[],setupGrants=[],appRequestLines=[],elevatedLines=[],memoryLines=[],storageLines=[],ownerSince=0,registered=[];
function rangeSince(key){const r=timelineRanges.find(x=>x[0]===key);if(r[2]==='today'){const d=new Date();d.setHours(0,0,0,0);return d.getTime()}return Date.now()-r[2]}
const callResult={completed:'Answered',cancelled:'Cancelled',refused:'Refused',not_permitted:'Not permitted',budget_exceeded:'Budget exceeded',unsupported_feature:'Unsupported feature',no_host:'No model server',unavailable:'Unavailable',invalid:'Invalid',forbidden:'Forbidden',exhausted:'Limit reached'};
// sameFile matches a model call's program to a registered app's program as
// the runtime reports both; the page folds no case itself.
function sameFile(a,b){return !!a&&a===b}
// appOf names a model call's program by its registered app's name, or by its
// file name when no registered app matches.
function activityAppName(app){return app.title||oaShortName(app.program)||'Unknown app'}
function appOf(program){const app=registered.find(a=>sameFile(a.program,program));return app?activityAppName(app):oaShortName(program)||'Unknown app'}
// Answers and app starts come worded from their owners (activity_panel.go):
// the app by the shared naming rule, what was asked or done, and who decided.
function timelineItems(){const since=rangeSince(rangeKey),chosen=registered.find(a=>a.program===appFilter);const items=[];
 for(const r of auditEntries){const e=r.Entry;if(!e.UnixMs||e.UnixMs<since)continue;if(chosen&&!sameFile(chosen.program,e.Program))continue;items.push({at:e.UnixMs,app:appOf(e.Program),what:'Model call'+(e.Model?' to '+oaModelName(e.Model):''),result:callResult[e.Outcome]||String(e.Outcome||'unknown').replace(/_/g,' ')})}
 for(const e of answers.concat(starts,setupGrants,appRequestLines,elevatedLines,memoryLines,storageLines)){if(!e.at||e.at<since)continue;if(chosen&&!sameFile(chosen.program,e.program)&&!sameFile(chosen.program,e.also))continue;items.push({at:e.at,app:e.app||appOf(e.program),what:e.what,result:e.result,undo:e.undo,restore:e.restore})}
 return items.sort((x,y)=>y.at-x.at)}
// timelineKeys keys each row by what it shows, so a live reread keeps the
// rows that stay (oaKeyedRows); equal rows are told apart by their order.
function timelineKeys(items){const seen=new Map();return items.map(i=>{const k=i.at+'\n'+i.app+'\n'+i.what+'\n'+i.result,n=seen.get(k)||0;seen.set(k,n+1);return k+'\n'+n})}
function timelineRow(){const tr=document.createElement('tr');for(let i=0;i<4;i++)tr.append(document.createElement('td'));return tr}
// timelineLast writes a row's last cell, its outcome in words (said); a
// Setup permission's row also carries Undo (CONV-U1), which removes exactly
// that rule.
function timelineLast(cell,item,said){if(item.restore){if(!cell.undoButton){const text=document.createElement('span'),button=document.createElement('button');button.type='button';button.textContent='Restore';cell.replaceChildren(text,button);cell.undoText=text;cell.undoButton=button}cell.undoText.textContent=said+' ';cell.undoButton.onclick=()=>restoreStorage(item.restore,cell.undoButton);return}if(!item.undo){if(cell.undoButton||cell.textContent!==said){cell.undoButton=null;cell.textContent=said}return}if(!cell.undoButton){const text=document.createElement('span'),button=document.createElement('button');button.type='button';button.textContent='Undo';button.setAttribute('data-status','timeline-status');cell.replaceChildren(text,button);cell.undoText=text;cell.undoButton=button}cell.undoText.textContent=said+' ';cell.undoButton.onclick=()=>undoSetupGrant(item.undo)}
async function restoreStorage(occurrence,button){button.disabled=true;try{const r=await call('/activity/storage',{occurrence});answersAt=0;await timeline();const message={queued:'Restore queued.',restored:'Restored from the download archive.'};oaSetStatus('timeline-status',message[r.outcome]?'ok':'error',message[r.outcome]||'This archive restore is unavailable.')}catch(e){oaShowError('timeline-status',e)}finally{button.disabled=false}}
async function undoSetupGrant(undo){try{const r=await call(undo.line?'/activity/rights-changes':undo.apps?'/activity/app-requests':'/activity/setup-grants',undo);answersAt=0;await timeline();oaSetStatus('timeline-status','ok',r.message)}catch(e){oaShowError('timeline-status',e)}}
function renderTimeline(){const items=timelineItems(),shown=items.slice(0,timelineShown),keys=timelineKeys(shown);
 renderRequests();
 oaKeyedRows(el('timeline-table').querySelector('tbody'),shown,(item,i)=>keys[i],timelineRow,(tr,item)=>{const values=[oaLocalTime(new Date(item.at).toISOString()),item.app,item.what,item.result];values.slice(0,3).forEach((v,i)=>{if(tr.children[i].textContent!==v)tr.children[i].textContent=v});timelineLast(tr.children[3],item,values[3])});
 const chosen=registered.find(a=>a.program===appFilter);const empty=el('timeline-empty');empty.hidden=items.length>0;empty.textContent='Nothing happened'+(chosen?' with '+activityAppName(chosen):'')+' in '+rangeSentence[rangeKey]+'.';
 el('timeline-table').parentNode.hidden=!items.length;el('timeline-more').hidden=items.length<=timelineShown;return items.length}
function renderRange(){const out=el('range');out.replaceChildren();for(const[k,label]of timelineRanges){const b=document.createElement('button');b.type='button';b.textContent=label;b.setAttribute('aria-pressed',String(k===rangeKey));b.onclick=()=>{rangeKey=k;timelineShown=timelinePage;renderRange();const n=renderTimeline();show('range-status','Showing '+rangeSentence[k]+': '+n+(n===1?' event.':' events.'))};out.append(b)}}
async function loadApps(){registered=await oaRegisteredApps();const select=el('app-filter'),current=select.value;select.replaceChildren();const all=document.createElement('option');all.value='';all.textContent='All apps';select.append(all);for(const a of registered){const o=document.createElement('option');o.value=a.program;o.textContent=activityAppName(a);o.title=a.program;select.append(o)}select.value=registered.some(a=>a.program===current)?current:''}
el('app-filter').onchange=()=>{appFilter=el('app-filter').value;timelineShown=timelinePage;const n=renderTimeline();const chosen=registered.find(a=>a.program===appFilter);show('range-status','Showing '+(chosen?activityAppName(chosen):'all apps')+': '+n+(n===1?' event.':' events.'))};
el('timeline-more').onclick=()=>{timelineShown+=timelinePage;renderTimeline();oaSetStatus('timeline-status','ok','Showing older events.')};
// readAudit reads model calls forward from the last cursor. The first pass
// reads to the end, keeping the newest 2,000, so Last hour is current on
// arrival; later passes read a few pages. A gap continues from the oldest
// call still retained.
let auditRead=false;
async function readAudit(){for(let i=0;!auditRead||i<8;i++){const p=await call('/inference/requests?cursor='+auditCursor);if(p.Outcome==='gap'){auditCursor=p.Next;continue}if(p.Outcome!=='page')return p.Outcome;auditEntries.push(...(p.Entries||[]));if(auditEntries.length>2000)auditEntries=auditEntries.slice(-2000);auditCursor=p.Next;if(p.AtEnd||!(p.Entries||[]).length){auditRead=true;break}}return 'page'}
// This is the same service-owned audit, with no separate history or current
// host settings used as historical proof. Incoming remote domain names
// identify the caller, so they never become the execution computer here.
let requestShown=timelinePage,requestHistoryAvailable=false;
function requestName(value,fallback){const name=String(value||'');return !name||/[\\/]|[0-9a-f]{16,}|^computer-[0-9a-f]{8}$|^sha256:|^S-1-|^abstraction\.|\.(exe|app|dll)$/i.test(name)?fallback:name.replace(/[_-]+/g,' ')}
// requestServer names a model server by its upstream project (oaEngineName).
function requestServer(value,fallback){return requestName(value,'')?oaEngineName(value):fallback}
function requestMachine(r){const e=r.Entry;if(r.Placement==='local')return {key:'local',label:'This computer'};if(r.Placement==='hosted')return {key:'hosted',label:'Hosted'};if(r.Placement==='remote')return {key:e.Domain||'remote',label:requestName(e.Domain,'Paired computer')};return {key:'unreported',label:'Not recorded'}}
function requestRows(){const since=rangeSince(rangeKey),chosen=registered.find(a=>a.program===appFilter);return auditEntries.filter(r=>r.Entry.UnixMs>=since&&(!chosen||sameFile(chosen.program,r.Entry.Program))).slice().sort((a,b)=>b.Entry.UnixMs-a.Entry.UnixMs)}
function requestOptions(id,all,rows,key,label){const select=el(id),prior=select.value,options=new Map();for(const row of rows)options.set(key(row),label(row));select.replaceChildren();const first=document.createElement('option');first.value='';first.textContent=all;select.append(first);for(const [value,text]of options){const option=document.createElement('option');option.value=value;option.textContent=text;select.append(option)}select.value=options.has(prior)?prior:''}
function renderRequests(){const rows=requestRows();requestOptions('request-machine','All computers',rows,r=>requestMachine(r).key,r=>requestMachine(r).label);requestOptions('request-model','All models',rows,r=>r.Entry.Model||'unreported',r=>r.Entry.Model?oaModelName(r.Entry.Model):'Not recorded');requestOptions('request-outcome','All results',rows,r=>r.Entry.Outcome||'unknown',r=>callResult[r.Entry.Outcome]||'Other outcome');
 const filtered=rows.filter(r=>(!el('request-machine').value||requestMachine(r).key===el('request-machine').value)&&(!el('request-model').value||(r.Entry.Model||'unreported')===el('request-model').value)&&(!el('request-outcome').value||(r.Entry.Outcome||'unknown')===el('request-outcome').value)),shown=filtered.slice(0,requestShown);
 oaKeyedRows(el('request-table').querySelector('tbody'),shown,r=>String(r.Entry.Sequence)+'\n'+r.Entry.UnixMs+'\n'+r.Entry.Operation,()=>{const row=document.createElement('tr');for(let i=0;i<8;i++)row.append(document.createElement('td'));return row},(row,r)=>{const e=r.Entry,app=registered.find(a=>sameFile(a.program,e.Program)),selection=r.Selection==='preferred_route_unavailable'?'Preferred route unavailable'+((r.UnavailableHosts||[]).length?' ('+r.UnavailableHosts.map(h=>requestServer(h,'Model server')).join(', ')+')':''):r.Selection==='preferred'?'Preferred model':r.Selection?String(r.Selection).replace(/_/g,' '):'Not recorded',values=[oaLocalTime(new Date(e.UnixMs).toISOString()),app?activityAppName(app):e.Route==='remote'?'Remote app':'Unregistered app',requestMachine(r).label,requestServer(e.Host,'Not recorded'),e.Model?oaModelName(e.Model):'Not recorded',r.PreferredModel?oaModelName(r.PreferredModel):'Not recorded',selection,callResult[e.Outcome]||'Other outcome'];values.forEach((value,i)=>{row.children[i].textContent=value})});
 el('request-table').parentNode.hidden=!shown.length;el('request-empty').hidden=shown.length>0;el('request-empty').textContent=requestHistoryAvailable?'No model requests match these filters.':'Model requests are unavailable right now.';el('request-more').hidden=filtered.length<=requestShown;el('request-tech').hidden=!shown.length;el('request-details').textContent=shown.length?JSON.stringify(shown,null,2):'';
}
for(const id of ['request-machine','request-model','request-outcome'])el(id).onchange=()=>{requestShown=timelinePage;renderRequests()};
el('request-more').onclick=()=>{requestShown+=timelinePage;renderRequests()};
// readOwner reads one owner's events from its first page until they reach
// back 7 days or the owner has no more, at most 8 pages of 64. A list that
// changed while paging (gap, stale) reads once more from the start. An
// owner too old to keep the list (unsupported) lists nothing.
async function readOwner(path){const back=Date.now()-7*86400e3;for(let attempt=0;attempt<2;attempt++){let cursor='',events=[],changed=false;
 for(let i=0;i<8;i++){const p=await call(path+'?cursor='+encodeURIComponent(cursor));if(p.outcome==='gap'||p.outcome==='stale'){changed=true;break}if(p.outcome==='unsupported')return {outcome:'page',events:[],since:0};if(p.outcome!=='page')return {outcome:p.outcome};const page=p.events||[];events.push(...page);if(p.complete||!p.next||!page.length||page[page.length-1].at<back)return {outcome:'page',events,since:p.completeSince||0};cursor=p.next}
 if(!changed)return {outcome:'page',events,since:0}}return {outcome:'gap'}}
// Answers and app starts change rarely: they reread every 30 seconds, model
// calls every pass.
const answersEveryMs=30000;let answersAt=0;
async function readAnswers(){if(answersAt&&Date.now()-answersAt<answersEveryMs)return 'page';answersAt=Date.now();const a=await readOwner('/activity/answers'),s=await readOwner('/activity/starts');
 if(a.outcome==='page')answers=a.events;if(s.outcome==='page')starts=s.events;ownerSince=Math.max(a.since||0,s.since||0);
 const g=await call('/activity/setup-grants');if(g.outcome==='page')setupGrants=g.events||[];
 const q=await call('/activity/app-requests');if(q.outcome==='page')appRequestLines=q.events||[];
 try{const v=await call('/activity/elevated');if(v.outcome==='page')elevatedLines=v.events||[]}catch(e){elevatedLines=[]}
 try{const v=await call('/activity/memory');if(v.outcome==='page')memoryLines=v.events||[]}catch(e){memoryLines=[]}
 try{const v=await call('/activity/storage');storageLines=v.outcome==='page'?v.events||[]:[]}catch(e){storageLines=[]}
 return a.outcome!=='page'?a.outcome:s.outcome}
const timelineOutcome={forbidden:'You hold no rule to read this; nothing was listed.',unavailable:'The runtime could not answer right now.',gap:'The list kept changing; it reads again shortly.'};
async function timeline(){const problems=[];for(const read of [readAudit,readAnswers]){try{const o=await read();if(read===readAudit){requestHistoryAvailable=o==='page';if(!requestHistoryAvailable){auditEntries=[];auditCursor=0;auditRead=false;oaSetStatus('request-status','error',o==='forbidden'?'This Panel may not read model requests.':'Model requests are unavailable right now.')}else oaSetStatus('request-status','idle','')}if(o!=='page')problems.push(timelineOutcome[o]||'The runtime answered '+String(o).replace(/_/g,' ')+'.')}catch(e){if(read===readAudit){requestHistoryAvailable=false;auditEntries=[];auditCursor=0;auditRead=false;oaShowError('request-status',e)}problems.push(e.message||String(e))}}renderTimeline();if(problems.length)oaShowError('timeline-status',problems.join(' '));else oaSetStatus('timeline-status','ok',ownerSince>rangeSince(rangeKey)?'Answers and app starts before '+oaLocalTime(new Date(ownerSince).toISOString())+' are no longer kept.':'')}

// --- Downloads and background work every app started. ---
let accountCursor='',accountPages=1;const accountText={forbidden:'You hold no rule for this; nothing was listed or changed.',unavailable:'The runtime could not obtain a decision; nothing was listed or changed. Try again.',gap:'The jobs changed while listing; they read again shortly.',invalid:'The job service rejected this request.',unknown:'No job has this id.'};
async function accountCancel(row,id){try{const r=await call('/account-work',{operation:id});row.status.textContent=r.Outcome==='requested'?'Cancellation requested; the program that submitted it sees the end.':r.Outcome==='already_terminal'?'Already finished.':(accountText[r.Outcome]||'The job service answered '+r.Outcome+'.')}catch(e){row.status.textContent=e.message||String(e)}}
const accountRows=new Map();
// jobFinished is a job state that has nothing left to cancel.
function jobFinished(s){return ['complete','failed','cancelled'].includes(String(s.State||''))}
// jobStateText reads a job's state in words (architect ruling 2026-11-09,
// second block); a state this page does not know reads in words with a
// leading capital.
const jobStates={pending:'Waiting to start',running:'Running',transferred:'Done; result not collected',complete:'Done',failed:'Failed',cancelled:'Cancelled',accepted:'Accepted',cancelling:'Cancelling'};
function jobStateText(state){const word=String(state||'');return Object.prototype.hasOwnProperty.call(jobStates,word)?jobStates[word]:oaNameWords(word)}
function accountRow(s){const id=s.Receipt.OperationID,label=s.Label||'Unlabelled job',state=jobStateText(s.State);let row=accountRows.get(id);if(!row){row=workRow(label,state,id);row.cancel=rowButton(row,'Cancel',()=>accountCancel(row,id));accountRows.set(id,row)}else setRowText(row,label,state,id);row.cancel.hidden=jobFinished(s);return row}
// accountList reads the first page (more=false, live) or the next one
// (more=true, Show more).
async function accountList(more){try{const p=await call('/account-work?cursor='+encodeURIComponent(more?accountCursor:''));if(p.Outcome!=='page'){if(!more){oaPlaceRows(el('account'),[]);accountRows.clear()}oaSetStatus('account-status','ok',accountText[p.Outcome]||'The job service answered '+p.Outcome+'.');el('account-more').hidden=true;el('account-empty').hidden=true;return}accountCursor=p.Next;const rows=(p.Snapshots||[]).map(accountRow);if(more){el('account').append(...rows);accountPages++;oaSetStatus('account-status','ok',rows.length?'Showing more work.':'No more work to show.')}else{oaPlaceRows(el('account'),rows);for(const id of [...accountRows.keys()])if(!rows.includes(accountRows.get(id)))accountRows.delete(id);accountPages=1;oaSetStatus('account-status','ok','')}
 el('account-empty').hidden=el('account').children.length>0;el('account-more').hidden=!!p.Complete||!p.Snapshots||!p.Snapshots.length}catch(e){fail('account-status',e)}}
el('account-more').onclick=()=>accountList(true);

// --- Work this Panel started. ---
let cursor='',inventoryPages=1;
// jobWaiting names what a job waits for in words; a reason this page does
// not know reads "waiting".
const jobWaits={'network:metered':'waiting for an unmetered network'};
function jobWaiting(s){return s.Waiting?' ('+(Object.prototype.hasOwnProperty.call(jobWaits,s.Waiting)?jobWaits[s.Waiting]:'waiting')+')':''}
function jobLabel(s){return (s.Label?s.Label+(s.LabelDerived?' (derived)':''):'Unlabelled job')+jobWaiting(s)}
const cancelOutcomeText={requested:'Cancellation requested; the program that submitted it sees the end.',already_terminal:'Nothing to cancel; this job has finished.',forbidden:'You are not authorized to cancel this; nothing was changed.',unavailable:'The job service could not complete this; try again.',unsupported:'This runtime does not support canceling this way.',unknown:'No job has this id.'};
function jobActionSentence(action,r){if(action==='cancel')return cancelOutcomeText[r.Outcome]||'The job service answered '+r.Outcome+'.';if(r.Outcome!=='observed'||!r.Snapshot)return cancelOutcomeText[r.Outcome]||'The job service answered '+r.Outcome+'.';return 'State: '+jobStateText(r.Snapshot.State)+'.'}
const inventoryRows=new Map();
function inventoryRow(s,b){const id=s.Receipt.OperationID,same=s.Receipt.LogicalOwner===b.history.LogicalOwner;let row=inventoryRows.get(id);if(!row){row=workRow('','',id);row.buttons=[];
 for(const action of ['observe','cancel'])row.buttons.push(rowButton(row,action==='observe'?'Check state':'Cancel',async()=>{const snap=row.snapshot;try{row.status.textContent=jobActionSentence(action,await call('/action',{action,endpoint:b.endpoint,owner:b.history.LogicalOwner,identity:snap.Receipt.Identity}))}catch(e){row.status.textContent=e.message||String(e)}}));
 row.buttons.push(rowButton(row,'Download result',()=>{const snap=row.snapshot;row.status.textContent='Downloading the result.';const q=new URLSearchParams({k:key,key:snap.Receipt.Identity.Key,epoch:snap.Receipt.Identity.HistoryEpoch,owner:b.history.LogicalOwner,endpoint:b.endpoint});location.href='/result?'+q}));
 if(!same)row.status.textContent='Another runtime holds this work, so it is read-only here.';inventoryRows.set(id,row)}
 row.snapshot=s;setRowText(row,jobLabel(s),jobStateText(s.State),id);for(const button of row.buttons)button.disabled=!same;row.buttons[1].hidden=jobFinished(s);return row}
async function inventory(more){try{const p=await call('/inventory?cursor='+encodeURIComponent(more?cursor:''));if(p.Outcome!=='page'){oaSetStatus('inventory-status','ok',p.Outcome==='gap'?'The list changed while reading; it reads again shortly.':'The job service answered '+p.Outcome+'.');el('next').hidden=true;return}cursor=p.Next;const b=await bind();const rows=(p.Snapshots||[]).map(s=>inventoryRow(s,b));if(more){el('records').append(...rows);inventoryPages++;oaSetStatus('inventory-status','ok',rows.length?'Showing more work.':'No more work to show.')}else{oaPlaceRows(el('records'),rows);for(const id of [...inventoryRows.keys()])if(!rows.includes(inventoryRows.get(id)))inventoryRows.delete(id);inventoryPages=1;oaSetStatus('inventory-status','ok','')}
 el('records-empty').hidden=el('records').children.length>0;el('next').hidden=!!p.Complete||!rows.length}catch(e){fail('inventory-status',e)}}
el('next').onclick=()=>inventory(true);

// Everything reads on arrival and again while the page is visible. A list
// the person paged through with Show more keeps its pages until reload.
const workEveryMs=5000;
// --- Notifications: per-app counts beside the switch (ASK-N6), and the
// notifications the runtime holds now. ---
const notifierRows=new Map();
const notificationStates={scheduled:'Waiting for its job',posted:'Shown',acted:'Chosen',dismissed:'Closed',withdrawn:'Withdrawn',expired:'Expired'};
// notifierFold shows OpenAbstractions' own programs as one row (architect
// ruling 2026-11-09, second block): their week's count summed, off only
// when every one is off, and its switch changes each.
function notifierFold(list){const own=list.filter(n=>n.own);if(!own.length)return list;const merged={key:'own',program:own[0].program,name:'OpenAbstractions',own:true,programs:own,week:own.reduce((sum,n)=>sum+(n.week||0),0),off:own.every(n=>n.off)};const at=list.indexOf(own[0]);return list.filter((n,i)=>!n.own||i===at).map(n=>n.own?merged:n)}
async function notifierSwitch(row,n){const on=n.off;try{for(const one of n.programs||[n])await call('/notifications',{program:one.program,on});row.status.textContent=on?'Notifications from '+n.name+' are on.':'Notifications from '+n.name+' are off; its shown notifications were withdrawn.';await notifications()}catch(e){row.status.textContent=e.message||String(e)}}
function notifierRow(n){const key=n.key||n.program;let row=notifierRows.get(key);const caption=(n.week===1?'1 notification':n.week+' notifications')+' this week'+(n.off?'; turned off':'');if(!row){row=workRow(n.name,caption,n.program);row.button=rowButton(row,'',()=>notifierSwitch(row,row.n));notifierRows.set(key,row)}else setRowText(row,n.name,caption,n.program);row.n=n;row.button.textContent=n.off?'Turn on':'Turn off';return row}
// A shown notification's actions: one button each, sent with the token it was
// shown with through the native click's path (/notifications/act). The
// sentence goes to the row, or to the group once the answered row is gone.
const notificationOpenRows=new Map();
let notificationsFailed=false,notificationsRead=false;
function notificationOpenRow(n){let row=notificationOpenRows.get(n.id);const caption=[n.name,n.body].filter(Boolean).join(' · ');if(!row){row=workRow(n.title,caption,'');notificationOpenRows.set(n.id,row)}else setRowText(row,n.title,caption,'');row.n=n;const actions=JSON.stringify(n.actions);if(row.actions!==actions){row.actions=actions;row.control.replaceChildren();for(const a of n.actions)rowButton(row,a.title,()=>notificationAct(row,a))}return row}
async function notificationAct(row,a){const n=row.n;for(const b of row.control.children)b.disabled=true;let line='',state='ok',operation='';
 try{const r=await call('/notifications/act',{id:n.id,action:a.id,token:n.token});line=r.message;operation=r.operation||''}catch(e){line=oaPlain(e&&e.message||e).line;state='error'}
 await notifications();const now=notificationOpenRows.get(n.id);
 if(now){now.status.textContent=line;for(const b of now.control.children)b.disabled=false}else oaSetStatus('notifications-status',state,line);
 if(operation)focusJob(operation)}
async function notifications(){try{const v=await call('/notifications');if(v.outcome!=='page'){oaSetStatus('notifications-status','ok','The notification service answered '+v.outcome+'.');return}
 const rows=notifierFold(v.notifiers||[]).map(notifierRow);oaPlaceRows(el('notifiers'),rows);for(const k of [...notifierRows.keys()])if(!rows.includes(notifierRows.get(k)))notifierRows.delete(k);el('notifiers-empty').hidden=rows.length>0;
 const open=(v.notifications||[]).filter(n=>n.actions&&n.actions.length).map(notificationOpenRow);oaPlaceRows(el('notifications-open'),open);for(const k of [...notificationOpenRows.keys()])if(!open.includes(notificationOpenRows.get(k)))notificationOpenRows.delete(k);
 const body=el('notifications-table').querySelector('tbody');body.replaceChildren();for(const n of v.notifications||[]){const tr=document.createElement('tr');const state=(notificationStates[n.state]||n.state)+(n.action?': '+n.action:'');for(const value of [n.at?oaLocalTime(new Date(n.at).toISOString()):'',n.name,n.title,state]){const td=document.createElement('td');td.textContent=value;tr.append(td)}body.append(tr)}
 el('notifications-table').hidden=!(v.notifications||[]).length;
 // A reread clears only a failure's sentence, so an action's sentence stays.
 if(notificationsFailed||!notificationsRead){notificationsFailed=false;notificationsRead=true;oaSetStatus('notifications-status','ok','')}}catch(e){notificationsFailed=true;fail('notifications-status',e)}}
// A notification's Open Activity action opens this page at its job's row,
// from a native click (the operation query) or from its button here.
const focusOperation=new URLSearchParams(location.search).get('operation')||'';
let focused=false;
function focusJob(operation){const row=accountRows.get(operation);if(!row){oaSetStatus('account-status','ok','The job this notification named is not in the current list.');return false}row.setAttribute('aria-current','true');row.scrollIntoView({block:'center'});row.status.textContent='This is the job the notification named.';return true}
function focusRow(){if(!focusOperation||focused)return;focused=focusJob(focusOperation)}
// workRefresh skips a tick while the previous pass is still reading.
let workBusy=false;
async function workRefresh(){if(workBusy)return;workBusy=true;try{await timeline();await readMessageActivity();await refreshAgentExits();if(accountPages===1)await accountList(false);if(inventoryPages===1)await inventory(false);await notifications()}finally{workBusy=false}}
renderRange();
workBusy=true;
var workReady=loadApps().then(()=>Promise.all([timeline(),readMessageActivity(),refreshAgentExits(),accountList(false).then(focusRow),inventory(false),notifications()])).finally(()=>{workBusy=false});
// The page is ready for screenshots once its first reads finish, whether
// they succeeded or failed (oaReady, panel_shell.go).
workReady.catch(()=>{}).finally(()=>oaReady());
setInterval(()=>{if(document.visibilityState==='visible')workRefresh()},workEveryMs);
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(workRefresh);
`
