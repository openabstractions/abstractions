package main

// workPage is the Panel's Work page: downloads and other work this Panel
// started, the same work every other app on this account started, and
// starting a download directly (task 2026-09-23, requirement 1: split out of
// the Status page, its own page at /work). Its rows say job (D1) and its key
// field is the idempotency key (D57), research/vocabulary/DECISION.md §4;
// the headings "Work started from this Panel" and "Work started by apps"
// stay, and the wire keeps RequestIdentity until job/acceptance@2.
const workPage = `<h1>Work</h1><p class="oa-note">Downloads and other work started from this Panel or by other apps.</p>
<section id="work"><h2>Work started from this Panel</h2><button id="restart">Show</button><button id="next" disabled>Next page</button><p id="inventory-status"></p><div id="records"></div></section>
<section id="account-work"><h2>Work started by apps</h2><p>Background work every app on this account started. You need the rule <span title="abstraction.job/inventory.read">jobs all</span> to see it, and <span title="abstraction.job/acceptance.cancel">jobs cancel</span> to cancel another app's job.</p><button id="account-start">Show all apps' work</button><button id="account-next" disabled>Next page</button><pre id="account-status"></pre><div id="account"></div></section>
<section id="download"><h2>Download a file</h2><p>The file is checked against the size and hash you enter. If the reply gets lost, the saved record lets you finish or cancel it.</p>
<form id="submit"><input id="url" type="url" required placeholder="File URL"><input id="digest" required placeholder="Hash (sha256:…)"><input id="size" type="number" min="0" required placeholder="Size in bytes"><input id="requestkey" type="hidden"><button id="requestkey-new" type="button">New key</button><span id="requestkey-note"></span><button>Download</button></form><p class="oa-note">Your own key; a retry with the same key is the same job.</p>
<select id="saved" aria-label="Saved downloads"></select><button id="reconcile">Continue saved download</button><button id="export">Save download record</button><pre id="operation"></pre></section>
<script>` + serviceCommonScript + `
let cursor='',binding=null;const savedKey='openabstractions-panel-request-v1';
async function bind(){if(!binding)binding=await call('/binding');return binding}
// recover's own reason ('continue' or 'save') names which button called it,
// so oaPlain's table (panel_shell.go) can read a plain sentence fitting that
// button instead of the one technical sentence both used to show unchanged
// (task 2026-09-23, ninth first-time visitor, finding 2).
function recover(reason){const value=localStorage.getItem(el('saved').value||savedKey);if(!value)throw Error('No saved request to '+reason+'. Preserve key, epoch, owner, endpoint and request before submission.');return JSON.parse(value)}
// submitOutcomeText names /action submit's own AcceptanceOutcome vocabulary
// (abstraction-job/go's acceptance.rec.go). A download once printed the raw
// {"Outcome":"definitely_not_accepted","Receipt":null,"Reason":"..."} object
// straight into the status line; submitResultLine now reads one sentence
// from it, the server's own Reason after a colon, with the full object kept
// behind Technical details, oaShowResult's own shape (task 2026-09-23, tenth
// first-time visitor, finding 1).
const submitOutcomeText={accepted:'Accepted; the runtime queued this download.',definitely_not_accepted:'Not accepted',unknown:'The runtime does not recognize this request.',key_conflict:'This idempotency key already names a different job.',forbidden:'You are not authorized to submit this; nothing was changed.',invalid:'The runtime rejected this request as invalid.',unavailable:'The job service could not accept this; try again.'};
// A refusal's own Reason used to end the sentence with no next step
// ("Not accepted: configured executor refused this work."); the previous
// pass read that one exact reason as meaning this runtime holds no
// download executor at all, and said so. That reading was wrong: the
// acceptance provider (openabstractions-flat/abstraction-job/go/
// acceptanceprovider/provider.go) only reaches this Reason from inside
// its own "if p.executor != nil" branch, when the configured executor's
// own Prepare rejected this specific request (a bad digest, a negative
// size, an unsupported guarantee); a runtime with no executor at all
// never refuses here, it accepts the work and never runs it. This exact
// runtime was started with --jobs-run-downloads and other downloads
// completed, which is what caught the false claim (task 2026-09-23,
// thirteenth first-time visitor, finding 3). submitReasonExplain is kept
// for a future Reason this runtime's own jobs executor documents as
// genuinely meaning no download execution is configured; none does
// today, so every refusal reason falls to the generic sentence below,
// naming the download executor rather than the runtime as a whole.
const submitReasonExplain={};
// An accepted download's own Reason is boilerplate ("original acceptance",
// "idempotent replay"), redundant beside the sentence above it; only a
// refusal's Reason carries information this sentence does not already say.
function submitResultLine(r){if(r.Outcome==='definitely_not_accepted'&&r.Reason){const explain=submitReasonExplain[r.Reason];return explain?'Not accepted: '+r.Reason+'. '+explain:'The download executor refused this work. Check that the file URL carries no username or password, and that the hash and size are exactly right.'}return (submitOutcomeText[r.Outcome]||'Job service outcome: '+r.Outcome)+(r.Reason&&r.Outcome!=='accepted'?': '+r.Reason+'.':'')}
function jobWaiting(s){return s.Waiting?' (waiting: '+(/^[a-z0-9_.:-]{1,64}$/.test(s.Waiting)?s.Waiting:'unnamed')+')':''}
function jobLabel(s){return (s.Label?s.Label+(s.LabelDerived?' (derived)':''):'Unlabelled job '+s.Receipt.OperationID)+jobWaiting(s)+' - '+s.State}
// jobActionSentence turns a cancel or observe call's typed result into one
// plain sentence, instead of the raw result object show('operation',...)
// used to write (task 2026-09-23, seventh first-time visitor, finding 4).
// A cancel against a job that already finished used to look identical to a
// successful cancel, both a raw {"Outcome":"already_terminal"} dump; it now
// reads "Nothing to cancel; this work has finished."
const cancelOutcomeText={requested:'Cancellation requested; the program that submitted it sees the end.',already_terminal:'Nothing to cancel; this job has finished.',forbidden:'You are not authorized to cancel this; nothing was changed.',unavailable:'The job service could not complete this; try again.',unsupported:'This runtime does not support canceling this way.',unknown:'No job has this id.'};
function jobActionSentence(action,r){if(action==='cancel')return cancelOutcomeText[r.Outcome]||'Job service outcome: '+r.Outcome;if(action==='observe'){if(r.Outcome!=='observed'||!r.Snapshot)return cancelOutcomeText[r.Outcome]||'Job service outcome: '+r.Outcome;return 'State: '+jobLabel(r.Snapshot)+'.'}return 'Job service outcome: '+(r.Outcome||JSON.stringify(r))}
async function inventory(){try{const p=await call('/inventory?cursor='+encodeURIComponent(cursor));if(p.Outcome!=='page'){show('inventory-status',p.Outcome==='gap'?'Cursor expired. Start inventory explicitly.':'Inventory: '+p.Outcome);el('next').disabled=true;return}cursor=p.Next;el('next').disabled=p.Complete;
// Next page used to replace the list with this page's own snapshots
// unconditionally; a click past the last real page (Complete but with
// nothing new) wiped the rows already shown and left only a small status
// change, reading as doing nothing (task 2026-09-23, twelfth first-time
// visitor, finding 7). The button is already disabled once Complete is
// true, so this only guards the one click that lands exactly there.
if(!p.Snapshots.length&&el('records').children.length){show('inventory-status','No more jobs to show.');return}
show('inventory-status',p.Complete?"That's every job so far. Click Show again to see anything started later.":'More pages remain; some may be empty.');el('records').replaceChildren();const b=await bind();for(const s of p.Snapshots){const box=document.createElement('div');const title=document.createElement('h3');title.textContent=jobLabel(s);const text=document.createElement('pre');text.textContent=JSON.stringify(s,null,2);box.append(title,text);const same=s.Receipt.LogicalOwner===b.history.LogicalOwner;for(const action of ['observe','cancel']){const button=document.createElement('button');button.textContent=action;button.disabled=!same;button.onclick=async()=>{try{show('operation',jobActionSentence(action,await call('/action',{action,endpoint:b.endpoint,owner:b.history.LogicalOwner,identity:s.Receipt.Identity})))}catch(e){fail('operation',e)}};box.append(button)}const result=document.createElement('button');result.textContent='Download completed bytes';result.disabled=!same;result.onclick=()=>{const q=new URLSearchParams({k:key,key:s.Receipt.Identity.Key,epoch:s.Receipt.Identity.HistoryEpoch,owner:b.history.LogicalOwner,endpoint:b.endpoint});location.href='/result?'+q};box.append(result);if(!same){const note=document.createElement('p');note.textContent='Read-only: this inventory owner differs from the selected action provider.';box.append(note)}el('records').append(box)}}catch(e){fail('inventory-status',e)}}
// savedLabel used to be the saved chooser's whole job: it read a saved
// record's raw request key, a UUID with no meaning to read (task
// 2026-09-23, thirteenth first-time visitor, finding 4). It now reads the
// file name and host out of the record's own URL, with the time it was
// saved; the raw key still sits on the option's own title, for Reconcile
// and Save to keep reading unchanged. A URL this can't parse (should not
// happen; every saved record was itself a validated submission) falls
// back to the raw key rather than show nothing.
function savedLabel(record){try{const u=new URL(record.url);const file=u.pathname.split('/').filter(Boolean).pop()||u.hostname;return file+' · '+u.hostname+(record.savedAt?' · '+oaClockText(new Date(record.savedAt),false):'')}catch(e){return record.identity&&record.identity.Key||'saved download'}}
// savedFileName reads just the file name savedLabel already extracts, for
// a sentence rather than a chooser option ("Saved the record for
// rfc2119.txt."). A URL this can't parse falls back to the raw request
// key, the same way savedLabel does.
function savedFileName(record){try{const u=new URL(record.url);return u.pathname.split('/').filter(Boolean).pop()||u.hostname}catch(e){return record.identity&&record.identity.Key||'this download'}}
function savedRequests(selected){el('saved').replaceChildren();for(let i=0;i<localStorage.length;i++){const k=localStorage.key(i);if(!k.startsWith(savedKey+':'))continue;const o=document.createElement('option');o.value=k;try{const record=JSON.parse(localStorage.getItem(k));o.textContent=savedLabel(record);o.title=record.identity.Key}catch{continue}el('saved').append(o)}if(selected)el('saved').value=selected}try{savedRequests()}catch(e){show('operation','Recovery storage unavailable: '+e.message)};
el('restart').onclick=()=>{cursor='';inventory()};el('next').onclick=inventory;
// Every required download field now names itself when left blank (task
// 2026-09-23, seventh first-time visitor, finding 4, the Issue key
// pattern): a browser that blocks the submit event for a failed required
// field never runs this form's own onsubmit at all, but always fires
// "invalid" on the field that failed, submit or not.
for(const id of ['url','digest','size']){const label={url:'File URL',digest:'Hash',size:'Size in bytes'}[id];el(id).addEventListener('invalid',()=>show('operation',label+' is required to submit a download.'))}
// Request key opened blank, one more field to fill in before the form
// could even be tried; it now generates its own key on arrival and offers
// a fresh one on click, the same way a person expects a "New" control to
// behave (task 2026-09-23, eleventh first-time visitor, requirement 3).
function newRequestKey(){try{return crypto.randomUUID()}catch(e){return 'req-'+Date.now().toString(36)+'-'+Math.random().toString(36).slice(2,10)}}
// The generated key used to sit in a visible, required text field, a raw
// UUID with no meaning to read (task 2026-09-23, thirteenth first-time
// visitor, finding 2). New key is now the control; the key itself is a
// hidden field the submit handler still reads unchanged, with the plain
// sentence in its place and the raw value kept reachable in Technical
// details.
function showRequestKey(){el('requestkey-note').replaceChildren();const line=document.createElement('span');line.textContent='An idempotency key was generated for this download.';el('requestkey-note').append(line);const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent='Idempotency key: '+el('requestkey').value;details.append(summary,pre);el('requestkey-note').append(details)}
el('requestkey-new').onclick=()=>{el('requestkey').value=newRequestKey();showRequestKey()};
el('requestkey').value=newRequestKey();showRequestKey();
// A refused download used to read "configured executor refused this
// work" with no next step, and no way to tell whether the problem was
// this Panel's own request or something the runtime alone knows. The
// wire carries no more detail than that one string for a genuine
// executor refusal: abstraction-job/go's acceptanceprovider (provider.go,
// the "if err != nil" branch that sets Reason to this exact string)
// discards the executor's own error unconditionally, every time, for
// every configured executor, before it ever reaches this Panel; nothing
// short of a change there could pass the real reason through (task
// 2026-09-23, fifteenth first-time visitor, finding 5, confirmed again
// by reading serve/'s own executor, abstraction-download/go/serve/
// execution.go's HTTPExecution.PrepareScoped). Three of its own refusal
// shapes a caller can check itself, before ever sending the request: the
// hash must read sha256:<64 hex characters>, the URL must be an http(s)
// address naming a host, and that address must carry no username or
// password (PrepareScoped requires an anonymous source; a credential
// belongs in Credentials, not the URL) — this last one reproduced
// "configured executor refused this work" through this exact form (task
// 2026-09-23, fourteenth and fifteenth first-time visitor, finding 4 and
// finding 5). Nothing else PrepareScoped can refuse is reachable from
// this form: it takes no credential reference and asks for no execution
// guarantee, so the remaining fallback sentence names what a person here
// can still check, rather than repeating the one word the server itself
// already gave no more meaning than.
const submitDigestPattern=/^sha256:[0-9a-f]{64}$/i;
function submitURLProblem(u){let parsed;try{parsed=new URL(u)}catch(e){return 'File URL must be a full address, starting with http:// or https://.'}if(parsed.protocol!=='http:'&&parsed.protocol!=='https:')return 'File URL must start with http:// or https://.';if(!parsed.hostname)return 'File URL must name a host.';if(parsed.username||parsed.password)return 'File URL must not include a username or password; the download executor requires a plain, anonymous address.';return ''}
el('submit').onsubmit=async e=>{e.preventDefault();try{const b=await bind();const a={action:'submit',endpoint:b.endpoint,owner:b.history.LogicalOwner,identity:{Key:el('requestkey').value,HistoryEpoch:b.history.HistoryEpoch},url:el('url').value,digest:el('digest').value,size:Number(el('size').value)};if(!Number.isSafeInteger(a.size)||a.size<0)throw Error('Size must be a nonnegative safe integer');if(!submitDigestPattern.test(a.digest))throw Error('Hash must read sha256: followed by 64 hex characters.');const urlProblem=submitURLProblem(a.url);if(urlProblem)throw Error(urlProblem);const recordKey=savedKey+':'+JSON.stringify([a.endpoint,a.owner,a.identity.HistoryEpoch,a.identity.Key]);const previousRaw=localStorage.getItem(recordKey);const previous=previousRaw?JSON.parse(previousRaw):null;
// The stored record now also carries savedAt, for savedLabel's own use;
// the equality check below still compares only the fields /action itself
// sees, so a savedAt this key already carries never counts as "different
// work" and never blocks a genuine reconcile-by-resubmit.
if(previous){const{savedAt,...core}=previous;if(JSON.stringify(core)!==JSON.stringify(a))throw Error('This key already names a different job. Reconcile it; choose a new key only for a new job.')}const stored={...a,savedAt:(previous&&previous.savedAt)||new Date().toISOString()};localStorage.setItem(recordKey,JSON.stringify(stored));localStorage.setItem(savedKey,JSON.stringify(stored));savedRequests(recordKey);const r=await call('/action',a);oaShowResult('operation',submitResultLine(r),r)}catch(e){show('operation',e.message+' Retain the saved request; use Reconcile before any new submission.')}};
// Continue saved download used to read jobActionSentence's own generic
// fallback, "Job service outcome: definitely_not_accepted", with no reason
// and no way to see the raw reply. Reconcile answers with the same
// AcceptanceOutcome vocabulary submit does (both ride the one /action
// endpoint), so it now reads through submitResultLine the same way
// (task 2026-09-23, eleventh first-time visitor, finding 3).
// Continue saved download used to send the saved record's own savedAt
// field straight to /action along with action, endpoint, owner, identity,
// url, digest and size; the server's own decoder refuses any field it
// does not name (service_panel.go's serviceAction, DisallowUnknownFields),
// so every Continue read "invalid action" from the server, generalized by
// oaPlain to "The runtime rejected this request." with no way to tell the
// two apart from the request that had actually just been refused for a
// different, real reason (task 2026-09-23, fifteenth first-time visitor,
// finding 5). savedAt is this Panel's own bookkeeping, for savedLabel's
// sake; the submit handler above already strips it the same way before
// comparing a saved record against a new one.
el('reconcile').onclick=async()=>{try{const{savedAt,...saved}=recover('continue');const r=await call('/action',{...saved,action:'reconcile'});oaShowResult('operation',submitResultLine(r),r)}catch(e){fail('operation',e)}};
// export used to write nothing on success: the file saved, but the status
// line stayed exactly as the previous click left it, so a click read as
// producing no result (task 2026-09-23, seventh first-time visitor, finding
// 4).
// Save download record used to read "Saved operation-recovery.json for
// request 55219ba1-…", the file this Panel wrote and the raw request key
// both visible text; it now names the download itself by its file name,
// the file this Panel wrote and the raw key kept in Technical details
// (task 2026-09-23, fourteenth first-time visitor, finding 5).
el('export').onclick=()=>{try{const saved=recover('save');const url=URL.createObjectURL(new Blob([JSON.stringify(saved,null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download='operation-recovery.json';a.click();URL.revokeObjectURL(url);oaShowResult('operation','Saved the record for '+savedFileName(saved)+'.',saved,'file: operation-recovery.json; idempotency key: '+saved.identity.Key)}catch(e){fail('operation',e)}};
// accountList's row used to read "<operation id> - <state> - <label>", the
// id first and the label an afterthought; the same download reads
// "<label> - <state>" on the Device page (card_page.go's work()), no id in
// the line at all (task 2026-09-23, sixth first-time visitor, finding 3).
// This row now reads the same way Device's does. The id is never visible
// page text (task 2026-09-23, seventh first-time visitor, finding 2); it
// sits on the line's own title attribute instead, reachable on hover, the
// same way this Panel now keeps every other raw id off the page.
let accountCursor='';const accountText={forbidden:'You hold no rule for this; nothing was listed or changed.',unavailable:'The runtime could not obtain a decision; nothing was listed or changed. Try again.',gap:'The jobs changed while listing; start the list again.',invalid:'The job service rejected this request.',unknown:'No job has this id.'};
// accountCancel used to name the operation by its raw id ("<id>: already
// ended."); it now names it by its label, the same one the row itself
// shows, and never the id (task 2026-09-23, eighth first-time visitor,
// finding 3).
async function accountCancel(id,label){try{const r=await call('/account-work',{operation:id});show('account-status',label+': '+(r.Outcome==='requested'?'cancellation requested; the program that submitted it sees the end.':r.Outcome==='already_terminal'?'already finished.':(accountText[r.Outcome]||'Job service outcome: '+r.Outcome)))}catch(e){fail('account-status',e)}}
// accountList also refreshes the grant widget above it, the same fetch
// its own arrival check makes, so a rule granted since this page opened
// shows up here without a reload (task 2026-09-23, fifteenth first-time
// visitor, finding 1).
async function accountList(){if(window.oaGrantRefresh)oaGrantRefresh();try{const p=await call('/account-work?cursor='+encodeURIComponent(accountCursor));if(p.Outcome!=='page'){el('account').replaceChildren();show('account-status',accountText[p.Outcome]||'Job service outcome: '+p.Outcome);el('account-next').disabled=true;return}accountCursor=p.Next;el('account-next').disabled=p.Complete;
// Next page used to clear this list before knowing whether the new page
// held anything, so a click past the last real page wiped the rows
// already shown and showed nothing in their place (task 2026-09-23,
// twelfth first-time visitor, finding 7).
if(!p.Snapshots.length&&el('account').children.length){show('account-status','No more jobs to show.');return}
show('account-status',p.Snapshots.length?(p.Complete?'End of jobs.':'More jobs remain.'):'No jobs on this page.');el('account').replaceChildren();for(const s of p.Snapshots){const box=document.createElement('div');const label=s.Label||'Unlabelled job';const text=document.createElement('span');text.textContent=label+' - '+s.State;text.title=s.Receipt.OperationID;const cancel=document.createElement('button');cancel.textContent='Cancel';cancel.onclick=()=>accountCancel(s.Receipt.OperationID,label);box.append(text,cancel);el('account').append(box)}}catch(e){fail('account-status',e)}}
el('account-start').onclick=()=>{accountCursor='';return accountList()};el('account-next').onclick=accountList;
</script>`
