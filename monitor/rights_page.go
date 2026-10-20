package main

// rightsPage is the Permissions page (research/panel-redesign/SPEC.md §4,
// S3): what your apps may do. Its words follow
// research/vocabulary/DECISION.md: permissions, Allow and Block (D53);
// Dismiss for a question closed without an answer (D50), while the wire
// action stays "retire" until asks/operator@2. Sections in order: Questions
// waiting (first-use requests and app questions in one list; the Panel's
// navigation badge counts them), Saved permissions by app, Defaults for all
// apps (rights_defaults.go), Answering access,
// Advanced (one-request answering and the Panel's own
// administrative access), then Past answers until Activity carries them.
// The page is presentation only: every read and write goes through the
// existing handlers (rights_panel.go, questions_panel.go, rights_assistant.go,
// rights_delegation.go) unchanged. Every list updates while the page is
// visible; "Show more" appears only past a page.
var rightsPage = pageOwnsReady(oaPageHead("rights", "Permissions", "What your apps may do.")) +
	`<section id="questions" class="oa-group"><h2>Questions waiting</h2><p class="oa-caption">Questions your apps are waiting on.</p>` +
	`<p class="oa-status" id="questions-status" role="status" aria-live="polite" data-state="idle"></p><div id="questions-list"></div>` +
	`<p class="oa-status" id="permissions-status" role="status" aria-live="polite" data-state="idle"></p><div id="permissions-list"></div>` +
	`<p class="oa-status" id="choices-status" role="status" aria-live="polite" data-state="idle"></p><div id="choices-list"></div>` +
	`<p class="oa-empty" id="questions-empty" hidden>No questions are waiting.</p>` +
	`<button id="questions-next" data-status="questions-status" hidden>Show more</button><button id="choices-next" data-status="choices-status" hidden>Show more</button><button id="permissions-next" data-status="permissions-status" hidden>Show more</button></section>` +
	`<section id="rights-section" class="oa-group"><h2 id="rights-heading" tabindex="-1">Saved permissions</h2><p class="oa-caption">What each app may do.</p>` +
	`<div id="rights-filter" hidden><p id="rights-filter-label"></p><a id="rights-show-all" href="#">Show all apps</a></div>` +
	`<div id="rights"></div>` +
	`<button id="rights-next" data-status="rights-status" hidden>Show more</button>` +
	oaRow("Advanced permissions", "Exact rules for administrators.", `<button id="rights-add" data-status="rights-status">Advanced editor…</button>`, "") +
	`<details id="rights-editor" class="oa-expander"><summary id="rights-editor-heading">Advanced permission editor<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><form id="rights-grant"><p class="oa-caption">Choose Other… to enter an account, app path or exact resource that is not listed. All three are required.</p>
<label>Account <select id="rights-account-select" aria-label="Account"></select><input id="rights-account" required hidden data-oa-other></label>
<label>App <select id="rights-program-select" aria-label="App"></select><input id="rights-program" required hidden data-oa-other></label>
<label>Capability <select id="rights-action" aria-label="Capability"></select></label>
<label id="rights-exact-target">Exact resource <select id="rights-resource-select" aria-label="Resource"></select><input id="rights-resource" required hidden data-oa-other></label>
<label id="rights-scope-field" hidden>Target scope <select id="rights-scope"><option value="one">One saved secret name</option><option value="all">All saved application secrets</option></select></label>
<label id="rights-name-field" hidden>Saved secret name <input id="rights-name" list="rights-secret-names"><datalist id="rights-secret-names"></datalist><small id="rights-names-status"></small></label>
<label>Choice <select id="rights-permit" aria-label="Permission choice"><option value="permit">Allow</option><option value="deny">Block</option><option value="remove">Remove rule</option></select></label>
<label>Lifetime <select id="rights-ttl" required aria-label="Lifetime"><option value="">Choose a lifetime</option><option value="0" selected>Until removed</option><option value="300000">Five minutes</option><option value="3600000">One hour</option><option value="86400000">One day</option></select></label>
<input id="rights-why" type="hidden"><input id="rights-origin-id" type="hidden"><div id="rights-preview" aria-live="polite"></div>
<label id="rights-broad-confirm-field" hidden><input id="rights-broad-confirm" type="checkbox"> I have reviewed the complete scope and effects above.</label>
<div><button id="rights-submit" data-status="rights-status">Add permission</button> <button type="button" id="rights-review-grant" data-status="rights-status" hidden>Review refreshed permissions</button></div></form></div></details>` +
	`<div id="rights-status" class="oa-status" role="status" aria-live="polite" data-state="idle"></div>` +
	oaExpander("Filter by capability", `<label>Capability <select id="rights-capability-filter"><option value="">All capabilities</option></select></label>`) +
	`<details id="rights-revision" class="oa-expander" data-oa-technical hidden><summary>Technical details<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body" id="rights-revision-body"></div></details>` +
	`</section>` +
	`<section id="defaults" class="oa-group"><h2>Defaults for all apps</h2><p class="oa-caption">Every app may do these unless you block that app.</p>` +
	`<p class="oa-status" id="defaults-status" role="status" aria-live="polite" data-state="idle"></p><div id="defaults-list"></div></section>` +
	`<section id="assistant" class="oa-group"><h2>Answering access</h2><p class="oa-caption">The app that answers routine questions for you.</p>` +
	`<div id="assistant-list"></div><p class="oa-status" id="assistant-status" role="status" aria-live="polite" data-state="idle"></p>` +
	`<details class="oa-expander"><summary>Choose an app that answers<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><form id="assistant-form"><label for="assistant-program-select">App that answers</label> <select id="assistant-program-select"></select><input id="assistant-program" hidden data-oa-other><div id="assistant-warning"></div><label><input id="assistant-confirm" type="checkbox"> I have read this.</label> <button id="assistant-save" data-status="assistant-status" disabled>Let it answer</button></form></div></details>` +
	`<details class="oa-expander"><summary>Answered for you<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><div id="answered-list"></div></div></details><p class="oa-status" id="answered-status" role="status" aria-live="polite" data-state="idle"></p></section>` +
	`<section id="answer-delegation" class="oa-group"><h2>Advanced</h2><p class="oa-caption">Exact permissions and one-time answers.</p>` +

	`<details class="oa-expander"><summary>Let an app answer one request<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><p>One app may give one answer to one waiting request, for five minutes.</p><form id="delegation-form"><label for="delegation-question">Waiting request</label> <select id="delegation-question" required><option value="">Choose a waiting request</option></select><details class="oa-expander"><summary>Technical details<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body" id="delegation-question-details"></div></details><label for="delegation-option">Answer it may give</label> <select id="delegation-option" required><option value="">Select a request first</option></select><label for="delegation-program-select">App that answers</label> <select id="delegation-program-select"></select><input id="delegation-program" hidden data-oa-other><p id="delegation-effect"></p><label><input id="delegation-confirm" type="checkbox" required> I understand this is a five-minute grant for this executable, request and answer. An Allow answer can give the requesting app that exact permission for up to five minutes.</label> <button id="delegation-save" data-status="delegation-status" disabled>Grant this answer</button> <button type="button" id="rights-review-delegation" data-status="delegation-status" hidden>Review refreshed permissions and request</button></form></div></details>` +
	`<p class="oa-status" id="delegation-status" role="status" aria-live="polite" data-state="idle"></p>` +
	`<details id="rights-admin-access" class="oa-expander" hidden><summary>Panel administrative access<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><p>This Panel manages the connected runtime's permissions through the runtime's configuration, separately from saved app permissions. Removing a saved app permission leaves it in place.</p></div></details>` +
	`<details class="oa-expander"><summary>Capabilities no permission names<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><div id="rights-unruled"></div></div></details></section>` +
	`<section id="app-questions" class="oa-group"><h2>Past answers</h2><p class="oa-caption">Questions you answered or that closed.</p>` +
	`<details class="oa-expander"><summary>Show past answers<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><div id="questions-history-list"></div><div id="choices-history-list"></div></div></details></section>` +
	`<script>` + serviceCommonScript + `

// One empty sentence for the two waiting lists together, and one for any
// other empty list (the owner's Panel rules).
let questionsWaiting=null,choicesWaiting=null,permissionsWaiting=null;
function renderQuestionsEmpty(){el('questions-empty').hidden=!(questionsWaiting===0&&choicesWaiting===0&&permissionsWaiting===0)}
function emptyLine(parent,sentence){const p=document.createElement('p');p.className='oa-empty';p.textContent=sentence;parent.append(p)}
// rightsItem is one listed item as the shared components (SPEC §6): an
// oa-row with its label, one caption and its controls, a status line when
// it has controls, then each raw text below it in a Technical details
// expander. The box is a plain wrapper; box.row is the row.
function rightsItem(o){const box=document.createElement('div'),row=document.createElement('div'),text=document.createElement('div'),label=document.createElement('span'),control=document.createElement('div');row.className='oa-row';text.className='oa-row-text';label.className='oa-row-label';control.className='oa-row-control';label.textContent=o.label;if(o.title)label.title=o.title;text.append(label);if(o.caption){const c=document.createElement('span');c.className='oa-row-caption';c.textContent=o.caption;text.append(c)}for(const c of o.controls||[])control.append(c);row.append(text,control);if((o.controls||[]).length){const st=document.createElement('p');st.className='oa-status';st.setAttribute('role','status');st.setAttribute('aria-live','polite');st.setAttribute('data-state','idle');row.append(st);row.status=st}box.append(row);for(const d of o.details||[])box.append(rightsExpander(d.summary,d.text,d.title));box.row=row;return box}
// rightsKeep reuses the item built for the same data on the last pass, so
// a reread keeps a row a person is on, its focus and its status line.
function rightsKeep(cache,key,build){const box=cache.last.get(key)||build();cache.next.set(key,box);return box}
function rightsKept(cache){cache.last=cache.next;cache.next=new Map()}
// rightsFill refreshes an item's texts in place (rightsItem's label,
// caption and title), leaving its controls and status line alone.
function rightsFill(box,label,caption,title){const text=box.row.children[0],l=text.children[0];if(l.textContent!==label)l.textContent=label;l.title=title||'';let c=text.children[1];if(caption&&!c){c=document.createElement('span');c.className='oa-row-caption';text.append(c)}if(c&&c.textContent!==(caption||''))c.textContent=caption||'';if(c)c.hidden=!caption}
// rightsRows draws a keyed list: the same key keeps its item, updated by
// fill; the list is redrawn only when its keys or their order changed.
function rightsRows(list,state,items,keyOf,build,fill,empty){const next=new Map(),boxes=items.map(item=>{const k=keyOf(item),box=state.rows.get(k)||build(item);fill(box,item);next.set(k,box);return box});const same=state.drawn&&boxes.length===state.order.length&&boxes.every((b,i)=>state.order[i]===b);state.rows=next;state.order=boxes;if(same)return;state.drawn=true;list.replaceChildren();if(!boxes.length){emptyLine(list,empty);return}for(const b of boxes)list.append(b)}
const assistantRows={rows:new Map(),order:[],drawn:false},answeredRows={rows:new Map(),order:[],drawn:false};
const questionItems={last:new Map(),next:new Map()},choiceItems={last:new Map(),next:new Map()},permissionItems={last:new Map(),next:new Map()};
// rightsSummary writes an expander's summary as oaExpander draws it: its
// text, then the shared chevron.
function rightsSummary(node,text){node.textContent=text;const c=document.createElement('span');c.className='oa-row-chevron';c.setAttribute('aria-hidden','true');node.append(c);return node}
// rightsBody turns a text body into one paragraph per line inside an
// expander body; the composed text stays the element's own textContent.
function rightsBody(body){const t=body.textContent;if(body.replaceChildren)body.replaceChildren();for(const line of String(t).split('\n')){const p=document.createElement('p');p.textContent=line;body.append(p)}body.textContent=t;body.className='oa-expander-body';return body}
function rightsExpander(summary,text,title){const details=summary==='Technical details'?oaTechnical(document.createElement('details')):document.createElement('details'),s=document.createElement('summary'),body=document.createElement('div');details.className='oa-expander';rightsSummary(s,summary);body.textContent=text;if(title)body.title=title;details.append(s,rightsBody(body));return details}
let choiceCursor='';
// Preserve app-owned text exactly, exposing control/format characters. The
// generic error translator must never reinterpret an app's title or labels.
function choiceDisplay(text){return String(text??'').replace(/[\p{Cc}\p{Cf}]/gu,c=>c==='\n'||c==='\r'||c==='\t'?c:'\\u'+c.codePointAt(0).toString(16).toUpperCase().padStart(4,'0'))}
const choiceText={unsupported:'This runtime does not support ordinary app questions. Permission requests remain available.',forbidden:'This Panel is not authorized to answer app questions.',unavailable:'App questions are unavailable. No decision is assumed.',gap:'The app questions changed. Refresh them to continue.',invalid:'The service rejected this app question action.',unknown:'This app request is no longer retained.',gone:'The app has acknowledged this request.',expired:'This app request has expired; no option was selected.',conflict:'This app request was already decided or changed.'};
function choiceStatus(outcome){return choiceText[outcome]||'App question service outcome: '+outcome}
async function choiceAct(action,shown,option){try{const r=await call('/choices',{action,shown,option:option||''});if(r.Outcome==='answered'||r.Outcome==='dismissed'){choiceCursor='';await choicesList();show('choices-status',r.Outcome==='answered'?'Selection recorded for the app.':'Request closed without selecting an option.')}else show('choices-status',choiceStatus(r.Outcome))}catch(e){fail('choices-status',e)}}
// The waiting lists are read again while the page is visible. Passes run
// one at a time; a pass that reads what is already shown keeps the rows,
// their focus and the status sentence the last click wrote.
let choicesChain=Promise.resolve(),choicesShown='',choicesFailed=false;
function choiceLive(record){return record.State==='pending'&&Date.now()<record.Request.PendingUntilMs&&Date.now()<record.Request.RequestRef.ReplayUntilMs}
function choicesList(){const run=choicesChain.then(choicesPass);choicesChain=run.catch(()=>{});return run}
async function choicesPass(){try{
 const page=await call('/choices?cursor='+encodeURIComponent(choiceCursor)),pending=el('choices-list'),history=el('choices-history-list');
 if(page.Outcome!=='page'){pending.replaceChildren();history.replaceChildren();choicesShown='';choicesFailed=true;show('choices-status',choiceStatus(page.Outcome));el('choices-next').disabled=true;el('choices-next').hidden=true;return}
 choiceCursor=page.Next;el('choices-next').disabled=page.Complete;el('choices-next').hidden=page.Complete;if(choicesFailed){show('choices-status','');choicesFailed=false}
 const sharedIds=new Set(page.Shared||[]);const shown=JSON.stringify([page.Records,page.Records.map(choiceLive),page.Shared||[]]);if(shown===choicesShown)return;choicesShown=shown;
 pending.replaceChildren();history.replaceChildren();let waiting=0,past=0;
 for(const record of page.Records){
  const live=choiceLive(record);
  const shared=live&&sharedIds.has(record.ID);
  const box=rightsKeep(choiceItems,JSON.stringify([record,live,shared]),()=>{
  const answer=record.Answer,offered=answer&&record.Request.Options.find(o=>o.ID===answer.Option);
  const who='Requested by '+choiceDisplay(record.PublisherTrusted&&record.ProgramDescription?record.ProgramDescription:record.Asker)+' · Publisher '+choiceDisplay(record.PublisherTrusted?(record.Publisher||'verified, no name recorded'):'not verified');
  const state=live?'Waiting for your answer':record.State==='answered'?'Selected: '+choiceDisplay(offered?offered.Label:answer.Option):record.State==='dismissed'?'Closed without an option':'Expired without an option';
  const controls=[];if(live){for(const option of record.Request.Options){const b=document.createElement('button');b.textContent=choiceDisplay(option.Label);b.onclick=()=>choiceAct('answer',record,option.ID);controls.push(b)}const close=document.createElement('button');close.textContent='Close';close.onclick=()=>choiceAct('dismiss',record);controls.push(close)}
  return rightsItem({label:choiceDisplay(record.Request.Title),caption:who+' · '+state+(shared?' · Your assistant may answer this':'')+(answer?' · '+new Date(answer.AnsweredMs).toLocaleString():''),controls,details:[{summary:'Technical details',text:choiceDisplay(JSON.stringify(record,null,2))}]});});
  if(live){waiting++;pending.append(box)}else{past++;history.append(box)}
 }
 rightsKept(choiceItems);
 choicesWaiting=waiting;renderQuestionsEmpty();if(!past)emptyLine(history,'No past app questions.');
}catch(e){choicesShown='';choicesFailed=true;fail('choices-status',e)}}
el('choices-next').onclick=choicesList;var choicesReady=choicesList();
// Typed permission requests (asks ASK-A10, ASK-O10): the native dialog's
// words and answers. The answer names the effect digest the row showed; the
// server re-reads the request and answers only while it still matches.
let permissionCursor='',permissionNext='',permissionsChain=Promise.resolve(),permissionsShown='';
const permissionText={unsupported:'This runtime does not list permission requests here.',forbidden:'This Panel is not authorized to answer permission requests.',unavailable:'Permission requests are unavailable. No decision is assumed.',gap:'The permission requests changed. Refresh them to continue.',invalid:'The service rejected this permission request action.'};
function permissionStatus(outcome){return permissionText[outcome]||'Permission request outcome: '+outcome}
function permissionsList(){const run=permissionsChain.then(permissionsPass);permissionsChain=run.catch(()=>{});return run}
async function permissionAct(request,option){try{const r=await call('/rights/requests',{id:request.id,option,digest:request.digest});permissionCursor='';await permissionsList();show('permissions-status',r.message)}catch(e){fail('permissions-status',e)}}
async function permissionsPass(){try{
 const page=await call('/rights/requests?cursor='+encodeURIComponent(permissionCursor)),list=el('permissions-list');
 if(page.outcome!=='page'){list.replaceChildren();permissionsShown='';permissionsWaiting=page.outcome==='unsupported'?0:null;renderQuestionsEmpty();if(page.outcome!=='unsupported')show('permissions-status',permissionStatus(page.outcome));el('permissions-next').hidden=true;return}
 el('permissions-next').hidden=page.complete;permissionNext=page.next||'';
 const shown=JSON.stringify(page.requests);if(shown===permissionsShown)return;permissionsShown=shown;
 list.replaceChildren();
 for(const request of page.requests){
  const box=rightsKeep(permissionItems,JSON.stringify(request),()=>{
   const controls=request.choices.map(c=>{const b=document.createElement('button');b.textContent=c.label;b.onclick=()=>permissionAct(request,c.option);return b});
   return rightsItem({label:request.instruction,caption:request.warning?request.content+'\n'+request.warning:request.content,controls,details:[{summary:'Technical details',text:request.details}]})});
  list.append(box)}
 rightsKept(permissionItems);permissionsWaiting=page.requests.length;renderQuestionsEmpty();
}catch(e){permissionsShown='';fail('permissions-status',e)}}
el('permissions-next').onclick=()=>{permissionCursor=permissionNext;permissionsList()};var permissionsReady=permissionsList();
let questionCursor='';const questionText={forbidden:'This Panel is not authorized as an operator of this runtime; nothing was changed.',unavailable:'The question service could not complete this; no decision is assumed. Retry or list again.',gap:'The question list changed; start the list again.',invalid:'The question service rejected this request as invalid.',unknown:'That question is no longer retained.',conflict:'A different answer is already recorded.'};
function questionStatus(o){return questionText[o]||'Question service outcome: '+o}
function ruleExpiryText(value,expired){const at=new Date(value);if(!value||Number.isNaN(at.getTime()))return '';return ((expired===undefined?at.getTime()<=Date.now():expired)?' (expired ':' (expires ')+at.toLocaleString()+')'}
function questionRule(r){if(r.ruleOutcome==='delegated')return '\nA delegated application gave this answer under a time-limited permission, so no permanent rule was written. Set a rule under Saved permissions to allow it permanently.';if(!r.rule)return r.partialError?'\nThe answer was recorded, but the permission rule needs review: '+r.partialError:'';const what=(r.rule.Permit?'allow ':'block ')+r.rule.Action+(r.paired?' and '+r.paired.Action:'')+' on '+r.rule.Resource+' for '+r.rule.Subject.Program;if(r.edit&&r.edit.Outcome==='applied'&&(!r.paired||(r.pairedEdit&&r.pairedEdit.Outcome==='applied')))return '\nRule written: '+what+' (asked at first use).';if(r.partialError)return '\nThe answer was recorded, but the rule may not be written: '+what+'. '+r.partialError+' List the questions and choose Retry rule.';return '\nThe rule was not written ('+(r.edit?r.edit.Outcome:r.ruleOutcome||'no decision')+'): '+what+'. List the questions and choose Retry rule.'}
const ruleStateText={written:'Rule written.',not_written:'Answered, rule not written.',delegated:'Answered by a delegated application; its permission was time-limited and is not made permanent here.'};
// questionAct's own reply used to say "... List again to refresh." while
// the list under it kept its stale rows; it now refreshes the list itself
// before showing the confirmation, the same fix every other list-changing
// write on this page and elsewhere gets (task 2026-09-23, fourteenth
// first-time visitor, findings 2 and 3).
async function questionAct(a){try{const r=await call('/questions',a);if(r.Outcome==='answered'||r.Outcome==='retired'){const line=(a.action==='answer'?'Answer recorded':a.action==='rule'?'Rule retry':'Question dismissed')+'.'+questionRule(r);questionCursor='';await questions();show('questions-status',line)}else show('questions-status',questionStatus(r.Outcome))}catch(e){fail('questions-status',e)}}
// A first-use question's own Text is a server-filled template,
// "<program's full path> wants to <raw action id> on <resource>". The
// listing words each first-use question in sentence (the listing's
// sentences entry), the sentence the native prompt shows, built by one Go
// function, firstUseSentence (monitor/tray_question.go; live walkthrough
// 2026-10-02, D5). The raw Text sits behind Technical details; a question
// with no sentence (every other key) reads unchanged. A program whose
// trusted signature the runtime recorded (prog, the listing's programs
// entry) adds its publisher line and, for a script host, the sentence asks
// gives for it (asks ASK-A6, ASK-A8; VISION 2026-10-01). The full path,
// description and original file name sit in the line's title. A question
// whose bound asker program the service recorded (asker, the listing's
// askers entry; asks ASK-A2, ASK-O11) names the program by the display name
// derived from it, with the full program in the title and Technical details,
// so two programs with one file name read apart.
function questionSentence(q,prog,sentence,asker){
 if(!sentence)return asker?{line:q.Text+'\nRequested by '+asker.name,title:asker.program,raw:'Program: '+asker.program}:{line:q.Text,title:'',raw:null};
 const signed=prog?'\nPublisher: '+(prog.publisher?prog.publisher+' (signature verified when the application asked).':'signature verified when the application asked; no publisher name was recorded.')+(prog.scriptHost?'\n'+prog.scriptHost:''):'';
 return{line:sentence+signed,title:(q.About||'')+(prog&&prog.description?'; '+prog.description:'')+(prog&&prog.originalName?' ('+prog.originalName+')':''),raw:q.Text};
}
// questionChoiceList is a question's options with the native prompt's
// button labels (the listing's choices entry, questionChoices in Go).
function questionChoiceList(p,q){return (p.choices||{})[q.ID]||q.Options.map(option=>({label:option,option}))}
// questions used to run independently of rightsList, so a first-use
// question could render before rightsList had ever populated
// rightsActionPlainCache, falling back to the generic action phrase
// instead of the catalogue's own curated one. It now waits for
// rightsListReady first (assigned once, near the bottom of this script,
// before questions is actually called), so the two loads still both
// happen once on arrival, in sequence rather than racing.
let questionsChain=Promise.resolve(),questionsShown='',questionsFailed=false;
function questions(){const run=questionsChain.then(questionsPass);questionsChain=run.catch(()=>{});return run}
async function questionsPass(){
 try{
  await rightsListReady;
  const firstPage=!questionCursor,p=await call('/questions?cursor='+encodeURIComponent(questionCursor));
  const pending=el('questions-list'),history=el('questions-history-list');
  if(p.Outcome!=='page'){pending.replaceChildren();history.replaceChildren();questionsShown='';questionsFailed=true;show('questions-status',questionStatus(p.Outcome));el('questions-next').disabled=true;el('questions-next').hidden=true;return}
  questionCursor=p.Next;el('questions-next').disabled=p.Complete;el('questions-next').hidden=p.Complete;if(questionsFailed){show('questions-status','');questionsFailed=false}
  const shown=JSON.stringify([firstPage,p.Records,p.sentences,p.choices,p.ruleStates,p.programs,p.askers]);if(shown===questionsShown)return;questionsShown=shown;
  pending.replaceChildren();history.replaceChildren();
  let pendingCount=0,answeredCount=0;
  for(const q of p.Records){
   const box=rightsKeep(questionItems,JSON.stringify([q,(p.programs||{})[q.ID],(p.sentences||{})[q.ID],(p.askers||{})[q.ID],(p.choices||{})[q.ID],(p.ruleStates||{})[q.ID]]),()=>{
   const s=questionSentence(q,(p.programs||{})[q.ID],(p.sentences||{})[q.ID],(p.askers||{})[q.ID]),choices=questionChoiceList(p,q);
   const answered=(choices.find(c=>c.option===q.Option)||{label:q.Option}).label,state=(p.ruleStates||{})[q.ID];
   const lines=s.line.split('\n'),status=(q.Option?'Answered: '+answered:'Waiting for your answer')+(state?' · '+(ruleStateText[state]||'Rule state: '+state):'');
   const controls=[];if(!q.Option)for(const c of choices){const b=document.createElement('button');b.textContent=c.label;b.onclick=()=>questionAct({action:'answer',id:q.ID,option:c.option});controls.push(b)}
   if(state==='not_written'){const retry=document.createElement('button');retry.textContent='Retry rule';retry.onclick=()=>questionAct({action:'rule',id:q.ID});controls.push(retry)}
   const dismiss=document.createElement('button');dismiss.textContent='Dismiss';dismiss.onclick=()=>questionAct({action:'retire',id:q.ID});controls.push(dismiss);
   return rightsItem({label:lines[0],caption:lines.slice(1).concat([status]).join(' · '),title:s.title,controls,details:s.raw?[{summary:'Technical details',text:s.raw}]:[]});});
   if(q.Option){answeredCount++;history.append(box)}else{pendingCount++;pending.append(box)}
  }
  rightsKept(questionItems);
  questionsWaiting=pendingCount;renderQuestionsEmpty();
  if(!answeredCount)emptyLine(history,'No past answers.');
 }catch(e){questionsShown='';questionsFailed=true;fail('questions-status',e)}
}
el('questions-next').onclick=questions;
let rightsCursor='',rightsRevision='',exploreSelf=null,rightsActionPlainCache=null,rightsDescriptionsCache={},rightsDefinitions=[],rightsBundles=[],rightsSelectorAvailable=false,rightsSelectorStatus='',rightsOriginDraft=null;const rightsText={forbidden:'This Panel is not authorized as an operator of this runtime; nothing was changed.',unavailable:'The rules service could not complete this; no change is assumed. List again before another edit.',gap:'The rules changed while listing; start the list again.',invalid:'The rules service rejected this rule as invalid.',conflict:'The rules changed since it was listed; nothing was changed. List again and reconcile before another edit.'};
function actionDescriptionText(action){const item=rightsDescriptionsCache[action],d=item&&item.Description;if(!d)return '';const risk=['routine','sensitive','administrative','unknown'].includes(d.DeclaredRisk)?d.DeclaredRisk:'unknown';return d.AppLabel+': '+d.Description+'\nFeature declared for '+rightsAppName(d.FeatureApp.Program)+' — '+accountLabel(d.FeatureApp.Account)+'.\nDeclared risk: '+risk+'. Options: Allow, Block, Remove rule.\nPublished by '+rightsAppName(item.PublishedBy.Program)+' — '+accountLabel(item.PublishedBy.Account)+'. This operator declaration does not verify the descriptor author or app behavior.'}
function appendActionDescription(box,action){const sentence=actionDescriptionText(action);if(!sentence)return;const details=document.createElement('details'),summary=document.createElement('summary'),text=document.createElement('div');rightsSummary(summary,'Action description');text.textContent=sentence;text.title=action;details.className='oa-expander';details.append(summary,rightsBody(text));box.append(details)}
const rightsQuery=new URLSearchParams(location.search);
let rightsAppFilter=rightsQuery.get('account')&&rightsQuery.get('program')?{account:rightsQuery.get('account'),program:rightsQuery.get('program')}:null;
let rightsFilterApplied=false;
if(rightsAppFilter){el('rights-filter').hidden=false;el('rights-filter-label').textContent='Permissions for '+rightsAppName(rightsAppFilter.program);el('rights-filter-label').title=rightsAppFilter.program+' — '+rightsAppFilter.account;el('rights-show-all').href='/rights?k='+encodeURIComponent(key)+'#rights-section'}
function rightsStatus(o){return rightsText[o]||'Rules service outcome: '+o}
let rightsListedRules=[];
let rightsAvailable=false,rightsListGeneration=0,rightsExpiringAt='',rightsWatchRun=null;
const rightsDrafts={grant:{dirty:false,revision:'',stale:false},delegation:{dirty:false,revision:'',stale:false}};
const rightsDraftFields=['rights-account-select','rights-account','rights-program-select','rights-program','rights-action','rights-resource-select','rights-resource','rights-scope','rights-name','rights-broad-confirm','rights-origin-id','rights-permit','rights-ttl','rights-why','delegation-question','delegation-option','delegation-program','delegation-confirm'];
function rightsDraftValues(){return rightsDraftFields.map(id=>({id,value:el(id).value,checked:el(id).checked,hidden:el(id).hidden}))}
function restoreRightsDraftValues(values){for(const f of values){const n=el(f.id);if(n.tagName==='SELECT'&&f.value&&!Array.from(n.options).some(o=>o.value===f.value)){const o=document.createElement('option');o.value=f.value;o.textContent='Previously selected';n.append(o)}n.value=f.value;n.checked=f.checked;n.hidden=f.hidden}}
function markRightsDraft(name){const d=rightsDrafts[name];if(!d.dirty){d.dirty=true;d.revision=name==='delegation'&&delegationSnapshot?delegationSnapshot.revision:rightsRevision;d.stale=d.revision!==rightsRevision}updateRightsDraftControls()}
function resetRightsDraft(name){if(name)rightsDrafts[name]={dirty:false,revision:'',stale:false};if(name==='grant'){rightsOriginDraft=null;el('rights-origin-id').value='';el('rights-broad-confirm').checked=false}updateRightsDraftControls()}
function invalidateRightsDrafts(revision,expiry){for(const d of Object.values(rightsDrafts))if(d.dirty&&(expiry||d.revision!==revision))d.stale=true;updateRightsDraftControls()}
function updateRightsDraftControls(){el('rights-admin-access').hidden=!rightsAvailable;el('rights-add').disabled=!rightsAvailable;for(const[name,form]of[['grant','rights-grant'],['delegation','delegation-form']]){const d=rightsDrafts[name],review=el('rights-review-'+name);review.hidden=!d.stale;review.disabled=!rightsAvailable;const save=el(form).querySelector&&el(form).querySelector('button:not([type="button"])');if(save)if(name!=='delegation')save.disabled=!rightsAvailable||d.stale||d.pending}if(el('rights').querySelectorAll)for(const button of el('rights').querySelectorAll('button'))button.disabled=!rightsAvailable||button.viewOnly===true;rightsGrantPreview();delegationEffect()}
function rightsDraftRevision(name){const d=rightsDrafts[name];if(!rightsAvailable){show('rights-status',rightsStatus('unavailable'));return ''}if(d.pending)return '';if(d.stale){show('rights-status','Permissions changed. Your entries are kept. Review refreshed permissions before saving.');return ''}return d.dirty?d.revision:name==='delegation'&&delegationSnapshot?delegationSnapshot.revision:rightsRevision}
function beginRightsDraftEdit(name){const revision=rightsDraftRevision(name);if(revision){rightsDrafts[name].pending=true;updateRightsDraftControls()}return revision}
function endRightsDraftEdit(name){if(name)rightsDrafts[name].pending=false;updateRightsDraftControls()}
async function reviewRightsDraft(name){if(!rightsAvailable)return;if(name==='delegation'){const values=rightsDraftValues();await loadDelegation();restoreRightsDraftValues(values);if(!delegationSnapshot||delegationSnapshot.outcome!=='page'||delegationSnapshot.revision!==rightsRevision){show('delegation-status','Refresh permissions and review the current request before saving.');return}delegationEffect()}const d=rightsDrafts[name];d.revision=name==='delegation'?delegationSnapshot.revision:rightsRevision;d.stale=false;updateRightsDraftControls()}
function rightsRetryPause(signal){return new Promise(resolve=>{if(signal.aborted){resolve();return}const done=()=>{clearTimeout(timer);signal.removeEventListener('abort',done);resolve()},timer=setTimeout(done,2000);signal.addEventListener('abort',done,{once:true})})}
function stopRightsWatch(){if(rightsWatchRun)rightsWatchRun.abort();rightsWatchRun=null;rightsListGeneration++}
async function followRights(){
 if(typeof AbortController==='undefined')return;
 stopRightsWatch();const run=new AbortController();rightsWatchRun=run;
 try{
  while(rightsWatchRun===run&&!run.signal.aborted){
   try{
    const revision=rightsRevision,query=new URLSearchParams({revision});
    if(rightsExpiringAt)query.set('refresh_at',rightsExpiringAt);
    const o=await call('/rights/observe?'+query,undefined,run.signal);
    if(rightsWatchRun!==run||run.signal.aborted)return;
    if(o.Outcome==='changed'||o.Outcome==='unchanged'){
     if(o.Outcome==='changed'||!rightsAvailable){
      invalidateRightsDrafts(o.Revision,o.Outcome==='changed'&&o.Revision===revision);
      const oldHint=rightsExpiringAt;rightsCursor='';
      await rightsList(undefined,true,run.signal);
      if(o.Revision===revision&&oldHint&&rightsExpiringAt===oldHint)await rightsRetryPause(run.signal);
     }
    }else{
     rightsAvailable=false;invalidateRightsDrafts('',false);updateRightsDraftControls();
     show('rights-status',rightsStatus(o.Outcome));await rightsRetryPause(run.signal);
    }
   }catch(err){
    if(run.signal.aborted||rightsWatchRun!==run)return;
    rightsAvailable=false;invalidateRightsDrafts('',false);updateRightsDraftControls();
    show('rights-status',rightsStatus('unavailable'));await rightsRetryPause(run.signal);
   }
  }
 }finally{if(rightsWatchRun===run)rightsWatchRun=null}
}
// ruleText used to read the raw action id and the program's full path
// straight into a confirmation sentence ("permit abstraction.asks/
// question.ask on * for reinis running C:\...\my-program.exe."); it now
// reads the action through the same plain-phrase dropdown Save rule
// itself builds from (rulePlain, rightsActionPlainCache) and names the
// program and account the same way the rules list below already does
// ("<file name> — <account phrase>", accountLabel), the same words instead
// of a second one for the same thing (task 2026-09-23, eleventh first-time
// visitor, finding 4; twelfth first-time visitor, finding 3, which also
// found the raw account SID visible and "List again before another edit."
// run into the sentence above it with no period between them).
function ruleText(r){return (r.Permit?'allow ':'block ')+rulePlain(rightsActionPlainCache,r.Action)+' on '+oaResourceLabel(r.Resource)+' for '+rightsAppName(r.Subject.Program)+' — '+accountLabel(r.Subject.Account)+'.'}
// rightsEdit used to refuse before rightsList had ever been read ("List
// rules before editing.") and, once applied, tell the person to list
// again while the table under it kept its stale rows; rightsList already
// runs on arrival (below) and now runs again on a successful edit, so the
// precondition and the "list again" instruction both go (task 2026-09-23,
// fourteenth first-time visitor, findings 2 and 3).
function rightsReadbackMatches(listed,reply,edit,context={}){
 if(!listed||!Array.isArray(listed.rules)||!listed.revision||listed.revision!==reply.Revision)return false;
 const valid=r=>r&&r.Subject&&typeof r.Subject.Account==='string'&&typeof r.Subject.Program==='string'&&typeof r.Action==='string'&&typeof r.Resource==='string'&&typeof r.Permit==='boolean'&&(r.Selector==null||(r.Selector.Permission&&typeof r.Selector.Permission.Action==='string'&&typeof r.Selector.Permission.ResourceProfile==='string'&&typeof r.Selector.Scope==='string'&&typeof r.Selector.Target==='string'));
 const subject=r=>valid(r)&&r.Subject.Account===edit.account&&r.Subject.Program===edit.program;
 const expected=Array.isArray(reply.Current)?reply.Current:reply.Current?[reply.Current]:[];
 if(!expected.every(valid)||!listed.rules.every(valid))return false;
 const selectorKey=s=>s&&s.Permission&&JSON.stringify([s.Permission.Action,s.Permission.ResourceProfile,s.Scope,s.Name||'',s.Target,s.Origin?.ID||'',s.Origin?.BundleRef||'',s.Origin?.Digest||'']);
 const same=(a,b)=>a.Subject.Account===b.Subject.Account&&a.Subject.Program===b.Subject.Program&&a.Action===b.Action&&a.Resource===b.Resource&&a.Permit===b.Permit&&(a.Expires||'')===(b.Expires||'')&&selectorKey(a.Selector)===selectorKey(b.Selector);
 const typed=(r,key)=>{
  const s=r.Selector;
  if(!subject(r)||!s||!s.Permission||s.Permission.Action!==key.Action||s.Permission.ResourceProfile!==key.ResourceProfile||r.Action!==key.Action||s.Scope!==edit.scope)return false;
  if(!['abstraction.credentials/named-use@2','abstraction.credentials/named-secret@1'].includes(key.ResourceProfile))return false;
  return edit.scope==='one'?s.Name===edit.credential_name&&s.Target===s.Name&&r.Resource===s.Target:edit.scope==='all'&&!s.Name&&s.Target===selectorScopeLabel('all')&&r.Resource===s.Target;
 };
 const exact=r=>subject(r)&&!r.Selector&&r.Action===edit.action&&r.Resource===edit.resource;
 if(edit.edit!=='set'){
  if(expected.length||!listed.complete)return false;
  if(edit.bundle_origin_id)return context.origin?.ID===edit.bundle_origin_id&&context.originSubject?.Account===edit.account&&context.originSubject?.Program===edit.program&&!listed.rules.some(r=>subject(r)&&r.Selector?.Origin?.ID===edit.bundle_origin_id);
  if(edit.bundle_ref)return false;
  if(edit.resource_profile){
   if(!['abstraction.credentials/named-use@2','abstraction.credentials/named-secret@1'].includes(edit.resource_profile)||!['one','all'].includes(edit.scope)||(edit.scope==='one'&&!edit.credential_name))return false;
   return !listed.rules.some(r=>subject(r)&&r.Selector?.Permission?.Action===edit.action&&r.Selector.Permission.ResourceProfile===edit.resource_profile&&r.Selector.Scope===edit.scope&&(r.Selector.Name||'')===(edit.scope==='one'?edit.credential_name:''));
  }
  return !listed.rules.some(exact);
 }
 if(!expected.length||expected.some(r=>!subject(r)||r.Permit!==edit.permit||(!edit.ttl_ms&&r.Expires)||(edit.ttl_ms&&(!r.Expires||!Number.isFinite(Date.parse(r.Expires))))))return false;
 if(edit.bundle_ref){
  const b=context.bundle,o=reply.BundleOrigin;
  if(!b||b.BundleRef!==edit.bundle_ref||!Array.isArray(b.Members)||!o?.ID||o.BundleRef!==b.BundleRef||o.Digest!==b.DefinitionDigest||expected.length!==b.Members.length)return false;
  if(!b.Members.every(k=>expected.filter(r=>typed(r,k)&&r.Selector.Origin?.ID===o.ID&&r.Selector.Origin?.BundleRef===b.BundleRef&&r.Selector.Origin?.Digest===b.DefinitionDigest).length===1))return false;
 }else if(expected.length!==1||!(edit.resource_profile?typed(expected[0],{Action:edit.action,ResourceProfile:edit.resource_profile})&&!expected[0].Selector.Origin:exact(expected[0])))return false;
 return expected.every(rule=>listed.rules.some(r=>same(r,rule)));
}
async function rightsEdit(e,name){
 const revision=name?beginRightsDraftEdit(name):rightsAvailable?rightsRevision:'';if(!revision)return;
 const operation=e.edit==='set'?(name==='grant'?rightsGrantOperation():'Update'):'Remove';
 const originRule=e.bundle_origin_id&&rightsListedRules.find(r=>r.Subject.Account===e.account&&r.Subject.Program===e.program&&r.Selector?.Origin?.ID===e.bundle_origin_id);
 const confirmation=JSON.parse(JSON.stringify({bundle:e.bundle_ref&&rightsBundles.find(b=>b.BundleRef===e.bundle_ref),origin:originRule?.Selector?.Origin,originSubject:originRule?.Subject}));
 try{
  const r=await call('/rights',{...e,revision});
  if(r.Outcome==='applied'){
   resetRightsDraft(name);const values=name==='grant'?rightsDraftValues():null;rightsCursor='';el('rights-capability-filter').value='';
   const selector=e.resource_profile||e.bundle_ref||e.bundle_origin_id;
   const listed=await rightsList({account:e.account,program:e.program,...(selector?{selector:true}:{action:e.action,resource:e.resource})});
   if(values){restoreRightsDraftValues(values);updateRightsDraftControls()}
   if(!rightsReadbackMatches(listed,r,e,confirmation)){show('rights-status','The service accepted the change, but its saved state could not be confirmed. Check Saved permissions before trying again.');return}
   const line=operation==='Remove'?'Permission removed. Saved permissions refreshed.':(operation==='Add'?'Permission added':'Permission updated')+' for '+rightsAppName(e.program)+'. Saved permissions refreshed.';
   oaShowResult('rights-status',line,r.Current||undefined);return;
  }
  if(name&&r.Outcome==='conflict'){rightsDrafts[name].stale=true;await rightsList(undefined,true);updateRightsDraftControls()}
  show('rights-status',rightsStatus(r.Outcome)+(r.Outcome==='conflict'&&r.Current&&!Array.isArray(r.Current)?'\nCurrent rule: '+ruleText(r.Current):''));
 }catch(err){fail('rights-status',err)}finally{endRightsDraftEdit(name)}
}
function accountLabel(account){return exploreSelf&&account===exploreSelf.account?'this account':account}
// Save rule's Account field used to open blank and required, with no
// default, while Allow's own quick-grant form already defaults its account
// to this one and says so ("Account (optional)"). ensureSelf now also keeps
// Save rule's own Account chooser in sync as soon as this Panel's own
// account resolves, the same "this account, <name>" wording Explore's own
// arrival sentence uses, with Other… for a different one (task 2026-09-23,
// twelfth first-time visitor, finding 6).
let rightsSelfAccountName='';
async function ensureSelf(rebuild=true){if(!exploreSelf){try{const v=await call('/explore');exploreSelf=v.self;rightsSelfAccountName=v.selfAccountName||''}catch(e){}}if(rebuild)rightsAccountChooser.rebuild();return exploreSelf}
// rulePlain names one action for the catalogue dropdown and the "Actions no
// rule names yet" list: a known action (ActionPlain, rights_panel.go's
// rightsActionPlain) shows its plain phrase; an action this map does not
// carry a phrase for falls back to oaActionFallback's generic one. Neither
// ever shows the raw id itself: every caller already keeps it as the
// element's own title attribute (task 2026-09-23, seventh first-time
// visitor, finding 2: an id is never visible page text).
function rulePlain(map,action){const phrase=map&&map[action];return phrase?capitalize(phrase):oaActionFallback(action)}
const rightsLifetimeLabels={'0':'Until removed','300000':'Five minutes','3600000':'One hour','86400000':'One day'};
function selectorActionValue(key){return 'selector:'+JSON.stringify([key.Action,key.ResourceProfile])}
function chosenSelectorCapability(){const value=el('rights-action').value;return rightsDefinitions.find(d=>selectorActionValue({Action:d.Definition.Action,ResourceProfile:d.Definition.ResourceProfile})===value)||rightsBundles.find(b=>'bundle:'+b.BundleRef===value)}
function selectorScopeLabel(scope,name){return scope==='all'?'All saved application secrets in this account':name||'one exact saved secret name'}
function updateSelectorControls(){const capability=chosenSelectorCapability(),origin=!!el('rights-origin-id').value,typed=!!capability||origin,selectors=capability?.SupportedSelectors||[],retainedRemoval=capability&&!capability.Editable&&capability.CodecAvailable&&el('rights-permit').value==='remove';el('rights-scope').disabled=!!retainedRemoval;el('rights-name').readOnly=!!retainedRemoval;for(const option of Array.from(el('rights-scope').options||el('rights-scope').children||[]))option.disabled=typed&&!selectors.includes(option.value);if(typed&&!origin&&!retainedRemoval&&!selectors.includes(el('rights-scope').value))el('rights-scope').value=selectors[0]||'';const all=el('rights-scope').value==='all';el('rights-exact-target').hidden=typed;el('rights-resource').required=!typed;el('rights-scope-field').hidden=!typed||origin;el('rights-name-field').hidden=!typed||all||origin;el('rights-name').required=typed&&!all&&!origin;el('rights-broad-confirm-field').hidden=!typed||(!all&&!capability?.BundleRef&&!origin);el('rights-broad-confirm').required=!el('rights-broad-confirm-field').hidden;el('rights-permit').disabled=origin||!!capability?.BundleRef||!!retainedRemoval;if(capability?.BundleRef)el('rights-permit').value='permit';el('rights-ttl').disabled=origin||el('rights-permit').value==='remove';el('rights-why').disabled=el('rights-ttl').disabled}
function rebuildPermissionChooser(catalog){const select=el('rights-action');select.replaceChildren();for(const action of catalog){const option=document.createElement('option');option.value=action;option.textContent=rulePlain(rightsActionPlainCache,action)+' (exact resource)';option.title=action;select.append(option)}for(const d of rightsDefinitions){const option=document.createElement('option');option.value=selectorActionValue({Action:d.Definition.Action,ResourceProfile:d.Definition.ResourceProfile});option.textContent=permissionLabel(d.Label,d.Definition.Action)+' ('+profilePhrase(d.Definition.ResourceProfile)+(d.Editable?'':', view-only')+')';option.title=d.Definition.Action+' — '+d.Definition.ResourceProfile;option.disabled=!d.Editable;select.append(option)}for(const bundle of rightsBundles){const option=document.createElement('option');option.value='bundle:'+bundle.BundleRef;option.textContent=permissionLabel(bundle.Label,bundle.BundleRef)+' (a set of permissions'+(bundle.Editable?'':', view-only')+')';option.title=bundle.BundleRef;option.disabled=!bundle.Editable;select.append(option)}}
function rebuildCapabilityFilter(){const select=el('rights-capability-filter'),value=select.value;select.replaceChildren();const all=document.createElement('option');all.value='';all.textContent='All capabilities';select.append(all);for(const source of Array.from(el('rights-action').options||el('rights-action').children||[])){if(source.value.startsWith('bundle:'))continue;const option=document.createElement('option');option.value=source.value;option.textContent=source.textContent;option.title=source.title||'';select.append(option)}select.value=Array.from(select.options||select.children||[]).some(o=>o.value===value)?value:''}
el('rights-capability-filter').onchange=()=>{rightsCursor='';return rightsList(undefined,true)};
let rightsNamesPromise=null;
async function loadRightsSecretNames(){if(rightsNamesPromise)return rightsNamesPromise;rightsNamesPromise=(async()=>{const names=new Set();let cursor='';try{for(let pages=0;pages<64;pages++){const page=await call('/rights?credential_names=1&cursor='+encodeURIComponent(cursor));if(page.Outcome!=='page'){show('rights-names-status','Saved names are unavailable. Enter the exact name.');return}for(const name of page.Names||[])names.add(name);if(page.Complete){show('rights-names-status','Suggestions list saved names in this Panel account. For another account, enter its exact name.');break}if(!page.Next||page.Next===cursor)throw Error('Credential metadata listing stopped.');cursor=page.Next;if(pages===63)show('rights-names-status','Suggestions are partial. You can enter another exact name.')}const list=el('rights-secret-names');list.replaceChildren();for(const name of [...names].sort()){const option=document.createElement('option');option.value=name;list.append(option)}}catch(e){show('rights-names-status','Saved names are unavailable. Enter the exact name.')}})();return rightsNamesPromise}
function appendSelectorDetails(box,r){if(!r.Selector)return;const s=r.Selector;const details=document.createElement('details'),summary=document.createElement('summary'),text=document.createElement('div');rightsSummary(summary,'Scope and origin');text.textContent='Profile: '+s.Permission.ResourceProfile+'\nScope: '+s.Scope+'\nTarget: '+s.Target+'\nEditing: '+(s.Editable?'owning target codec available':'view-only: profile decoder unavailable');if(s.Origin){const origin=s.Origin;text.textContent+='\nBundle: '+origin.BundleRef+'\nDefinition digest: '+origin.Digest+'\nOriginal grant: '+origin.ID+'\nCurrent original members: '+origin.Remaining+'/'+origin.Original+' ('+origin.State+')\nOriginal expansion:\n'+origin.Members.map(m=>m.Permission.Action+' | '+m.Permission.ResourceProfile+' | '+m.Scope+' | '+m.Target).join('\n');const button=document.createElement('button');button.textContent='Remove remaining bundle rules';button.onclick=()=>editBundleOrigin(r);if(s.Editable&&origin.BundleRef==='abstraction.credentials/full-control@1'&&origin.Members.every(m=>m.Editable))box.append(button)}details.className='oa-expander';details.append(summary,rightsBody(text));box.append(details)}
function editBundleOrigin(r){editListedRule(r,'remove');rightsOriginDraft=r.Selector.Origin;el('rights-origin-id').value=rightsOriginDraft.ID;rightsSummary(el('rights-editor-heading'),'Review remaining bundle rules for '+rightsAppName(r.Subject.Program));el('rights-broad-confirm').checked=false;rightsGrantPreview()}
function rightsGrantPayload(){const subject={account:el('rights-account').value,program:el('rights-program').value},capability=chosenSelectorCapability();if(el('rights-origin-id').value){if(!el('rights-broad-confirm').checked){show('rights-status','Review the original bundle and confirm removing its remaining rules.');return null}return {edit:'remove_bundle',...subject,bundle_origin_id:el('rights-origin-id').value}}const base=capability?{...subject,scope:el('rights-scope').value,...(el('rights-scope').value==='one'?{credential_name:el('rights-name').value}:{})}: {...subject,action:el('rights-action').value,resource:el('rights-resource').value};if(capability){if(!rightsSelectorAvailable||!(el('rights-permit').value==='remove'?capability.CodecAvailable:capability.Editable)||!(el('rights-permit').value==='remove'?['one','all']:capability.SupportedSelectors).includes(base.scope)){show('rights-status','This permission profile is view-only or no longer available.');return null}if((base.scope==='all'||capability.BundleRef)&&!el('rights-broad-confirm').checked){show('rights-status','Review the complete scope and effects, then confirm before saving.');return null}if(capability.BundleRef)base.bundle_ref=capability.BundleRef;else{base.action=capability.Definition.Action;base.resource_profile=capability.Definition.ResourceProfile}}else if(/^(selector|bundle):/.test(base.action)){show('rights-status','This permission profile is no longer available.');return null}if(el('rights-permit').value==='remove')return {edit:'revoke',...base};const lifetime=el('rights-ttl').value;if(!Object.hasOwn(rightsLifetimeLabels,lifetime)){show('rights-status','Choose a lifetime before saving this permission.');return null}const edit={edit:'set',...base,permit:el('rights-permit').value==='permit'};if(Number(lifetime))edit.ttl_ms=Number(lifetime);if(el('rights-why').value)edit.why=el('rights-why').value;return edit}
// permissionLabel is the one source of a permission's words: a definition's
// or bundle's label when it reads as a phrase, else the action's plain
// phrase (rulePlain). An id-shaped label ("abstraction.<layer>/…") never
// becomes page text; the raw value stays on the option's title.
function permissionLabel(label,action){return label&&!/^abstraction\.[a-z0-9_-]+\//.test(label)?label:rulePlain(rightsActionPlainCache,action)}
// profilePhrase names a resource profile's target kind in words.
function profilePhrase(profile){return profile==='abstraction.rights/legacy-exact-string@1'?'exact resource':/^abstraction\.credentials\//.test(profile)?'saved secrets':'typed target'}
function selectorPermissionLabel(key){const d=rightsDefinitions.find(d=>d.Definition.Action===key.Action&&d.Definition.ResourceProfile===key.ResourceProfile);return permissionLabel(d&&d.Label,key.Action)}
function selectorGrantPreview(capability){
 const out=el('rights-preview'),origin=el('rights-origin-id').value?rightsOriginDraft:null,scope=origin?origin.Members[0]?.Scope:el('rights-scope').value,name=origin?origin.Members[0]?.Name:el('rights-name').value,remove=!!origin||el('rights-permit').value==='remove';
 const keys=origin?(origin.LinkedMembers||[]).map(m=>m.Permission):capability.BundleRef?capability.Members:[{Action:capability.Definition.Action,ResourceProfile:capability.Definition.ResourceProfile}];
 const words=document.createElement('p');words.textContent=(remove?'Remove ':el('rights-permit').value==='deny'?'Block ':'Allow ')+(origin?'the remaining rules of this set of permissions':permissionLabel(capability.Label,capability.BundleRef||capability.Definition.Action))+' for '+(el('rights-program').value?rightsAppName(el('rights-program').value):'the selected application')+' on '+selectorScopeLabel(scope,name)+'.'+(remove?' Other saved permissions remain unchanged.':' Lifetime: '+(rightsLifetimeWords[rightsLifetimeLabels[el('rights-ttl').value]]||'choose a lifetime')+'.');out.append(words);
 if(scope==='all'){const warning=document.createElement('p');warning.textContent='Broad scope: this affects current and future saved application secrets in this account. Runtime-managed local keys are excluded. '+(capability?.AllEffect||'');out.append(warning)}
 if(capability?.BundleRef||origin){const warning=document.createElement('p');warning.textContent='Full control is a fixed set of six saved-secret operations: Use, read value, create, rotate, revoke and named metadata inspection. It excludes secret enumeration, audit, permission administration, runtime-managed local keys and future operations. A later bundle version requires another grant.';out.append(warning);if(capability?.Disclosure)para(out,capability.Disclosure)}
 if(keys.some(k=>k.Action==='abstraction.credentials/secret.read'))para(out,'Read value returns the secret bytes to this application. It can copy or use the value elsewhere. Website and consumer restrictions on Use do not limit bytes after they are returned.');
 if(origin)para(out,'Current original grants: '+origin.Remaining+' of '+origin.Original+' ('+origin.State+'). Remove these '+keys.length+' currently linked rules, including retained expired rules:');
 const members=document.createElement('ul');for(const key of keys){const item=document.createElement('li');item.textContent=selectorPermissionLabel(key)+' — '+selectorScopeLabel(scope,name);members.append(item)}out.append(members);
 if(origin){const original=document.createElement('details'),heading=document.createElement('summary'),list=document.createElement('div');rightsSummary(heading,'Immutable original bundle expansion');list.textContent=origin.Members.map(m=>'Action: '+m.Permission.Action+'\nProfile: '+m.Permission.ResourceProfile+'\nTarget: '+selectorScopeLabel(m.Scope,m.Name)).join('\n\n');original.className='oa-expander';original.append(heading,rightsBody(list));out.append(original)}
 const details=document.createElement('details'),summary=document.createElement('summary'),text=document.createElement('div');rightsSummary(summary,'Complete permission effect');text.textContent='Account: '+el('rights-account').value+'\nProgram: '+el('rights-program').value+'\nScope: '+scope+'\nTarget: '+selectorScopeLabel(scope,name)+'\nChoice: '+(remove?'Remove':el('rights-permit').value==='deny'?'Block':'Allow')+'\nLifetime: '+(remove?'existing rules removed':rightsLifetimeLabels[el('rights-ttl').value]||'choose a lifetime')+'\nReason: '+(remove?'none':el('rights-why').value)+'\nRevision: '+(rightsDrafts.grant.dirty?rightsDrafts.grant.revision:rightsRevision)+'\n'+keys.map(k=>'Action: '+k.Action+'\nProfile: '+k.ResourceProfile).join('\n');if(capability?.BundleRef||origin)text.textContent+='\nBundle: '+(origin?origin.BundleRef:capability.BundleRef)+'\nDefinition digest: '+(origin?origin.Digest:capability.DefinitionDigest)+(origin?'\nOriginal grant: '+origin.ID+'\nCurrent original members: '+origin.Remaining+'/'+origin.Original+' ('+origin.State+')':'');else text.textContent+='\nOwner contract: '+capability.Definition.OwnerContract+'\nCollection: '+capability.Collection+'\nTarget type: '+capability.RequestType+'\nReceiving operations: '+(capability.Definition.Operations||[]).join(', ')+'\nEnforced by: '+(capability.Definition.EnforcedBy||[]).join(', ')+'\nLifecycle: '+(capability.Definition.Lifecycle||'')+'\nDisclosure: '+(capability.Definition.Disclosure||'')+'\nSupported selectors: '+capability.SupportedSelectors.join(', ');details.className='oa-expander';details.append(summary,rightsBody(text));out.append(details);
}
// The lifetime as it reads inside a sentence.
var rightsLifetimeWords={'Until removed':'until removed','Five minutes':'five minutes','One hour':'one hour','One day':'one day','Choose a lifetime':'choose a lifetime'};
function rightsGrantOperation(){if(el('rights-permit').value==='remove'||el('rights-origin-id').value)return 'Remove';const capability=chosenSelectorCapability();return rightsListedRules.some(r=>r.Subject.Account===el('rights-account').value&&r.Subject.Program===el('rights-program').value&&(capability?r.Selector&&selectorActionValue(r.Selector.Permission)===el('rights-action').value&&r.Selector.Scope===el('rights-scope').value&&(r.Selector.Name||'')===(el('rights-scope').value==='one'?el('rights-name').value:''):!r.Selector&&r.Action===el('rights-action').value&&r.Resource===el('rights-resource').value))?'Update':'Add'}
function rightsGrantPreview(){el('rights-submit').textContent=rightsGrantOperation()+' permission';updateSelectorControls();const out=el('rights-preview');out.replaceChildren();const capability=chosenSelectorCapability();if(capability||el('rights-origin-id').value){selectorGrantPreview(capability);return}const choice=el('rights-permit').value,remove=choice==='remove',program=el('rights-program').value,resource=el('rights-resource').value,action=el('rights-action').value,lifetime=rightsLifetimeLabels[el('rights-ttl').value]||'Choose a lifetime';el('rights-ttl').disabled=remove;el('rights-why').disabled=remove;const words=document.createElement('p');words.textContent=remove?'Remove the saved exact rule for '+(program?rightsAppName(program):'the selected application')+' on '+(resource||'the selected resource')+'. Other permissions remain unchanged.':(choice==='deny'?'Block ':'Allow ')+(program?rightsAppName(program):'the selected application')+' to '+rulePlain(rightsActionPlainCache,action||'the selected action')+' on '+(resource||'the selected resource')+'. Lifetime: '+(rightsLifetimeWords[lifetime]||'choose a lifetime')+'.';out.append(words);const details=oaTechnical(document.createElement('details')),summary=document.createElement('summary'),text=document.createElement('div');rightsSummary(summary,'Technical details');text.textContent='Account: '+el('rights-account').value+'\nProgram: '+program+'\nAction: '+action+'\nExact resource: '+resource+'\nChoice: '+(remove?'Remove rule':choice==='deny'?'Block':'Allow')+(remove?'':'\nLifetime: '+lifetime+'\nReason: '+el('rights-why').value)+'\nRevision: '+(rightsDrafts.grant.dirty?rightsDrafts.grant.revision:rightsRevision);details.className='oa-expander';details.append(summary,rightsBody(text));out.append(details);if(resource==='*'){const note=document.createElement('p');note.textContent='This is an exact resource named *. It does not select all resources.';out.append(note)}}
function editListedRule(r,choice){if(!rightsAvailable||(r.Selector&&(!r.Selector.Editable||(choice!=='remove'&&!r.Selector.CanGrant))))return;resetRightsDraft('grant');rightsProgramsSeen.add(r.Subject.Program);rightsProgramChooser.rebuild();el('rights-program-select').value=r.Subject.Program;el('rights-program-select').onchange();el('rights-program').value=r.Subject.Program;el('rights-account-select').value=exploreSelf&&r.Subject.Account===exploreSelf.account?r.Subject.Account:'__other__';el('rights-account-select').onchange();el('rights-account').value=r.Subject.Account;el('rights-action').value=r.Selector?selectorActionValue(r.Selector.Permission):r.Action;if(r.Selector){el('rights-scope').value=r.Selector.Scope;el('rights-name').value=r.Selector.Name||'';loadRightsSecretNames()}rememberResource(r.Action,r.Resource);rebuildResourceChooser();el('rights-resource-select').value=r.Resource;resourceChooserSync();el('rights-permit').value=choice;el('rights-ttl').value=r.Expires&&!r.Expired?'':'0';el('rights-why').value=r.Why||'';rightsSummary(el('rights-editor-heading'),'Review permission for '+rightsAppName(r.Subject.Program));markRightsDraft('grant');openRightsEditor()}
function openRightsEditor(){const editor=el('rights-editor');editor.open=true;const field=el('rights-program-select');if(editor.scrollIntoView)editor.scrollIntoView({behavior:'smooth',block:'nearest'});if(field.focus)field.focus()}
el('rights-add').onclick=()=>{if(!rightsAvailable)return;if(!rightsDrafts.grant.dirty)rightsSummary(el('rights-editor-heading'),'Advanced permission editor');openRightsEditor()};
// appendRuleDetails is a rule's Details below its row: who it is for and who
// set it, in plain words, then the raw record in a nested Technical details.
function rightsAccountWords(account){return exploreSelf&&account===exploreSelf.account?'this account':'another account'}
function appendRuleDetails(box,r){const details=document.createElement('details');details.className='oa-expander';const summary=document.createElement('summary'),body=document.createElement('div');rightsSummary(summary,'Details');body.className='oa-expander-body';
 const lines=['For '+rightsAppName(r.Subject.Program)+' on '+rightsAccountWords(r.Subject.Account)+'.',
  r.Why==='installation'?'Set up by OpenAbstractions.':r.SetBy?'Set by '+rightsAppName(r.SetBy.Program)+' on '+rightsAccountWords(r.SetBy.Account)+(r.SetAt?', '+new Date(r.SetAt).toLocaleString():'')+'.':'',
  r.Why&&r.Why!=='installation'?'Reason: '+r.Why:'',r.Expires?(r.Expired?'Expired ':'Ends ')+new Date(r.Expires).toLocaleString()+'.':'Stays until removed.'];
 for(const line of lines)if(line)para(body,line);
 body.append(rightsExpander('Technical details','Account: '+r.Subject.Account+'\nProgram: '+r.Subject.Program+'\nAction: '+r.Action+'\n'+(r.Selector?'Target: ':'Exact resource: ')+r.Resource+'\nSaved choice: '+(r.Permit?'Allow':'Block')+'\nRecorded account: '+(r.SetBy?r.SetBy.Account:'unavailable')+'\nVia application: '+(r.SetBy?r.SetBy.Program:'unavailable')+'\nSet at: '+(r.SetAt||'unavailable')+'\nReason: '+(r.Why||'none')+'\nExpires: '+(r.Expires||'until removed')+'\nRevision: '+rightsRevision));
 details.append(summary,body);appendSelectorDetails(details,r);appendActionDescription(details,r.Action);box.append(details)}
// Saved permissions read by app: the app's registered name (its program's
// file name when it has none) as a heading, then one row per permission
// with its state, Edit, and its Details with Remove rule below it. The full
// program, account, action and resource sit on titles and in Details.
// A content digest names stored content; the digest stays on the title.
function rightsTargetLabel(resource){return /^sha256:/i.test(resource)?'Stored content':oaResourceLabel(resource)}
var rightsNames={};
// A program neither OpenAbstractions' own nor registered reads as its file
// stem and "(not registered)"; the full path stays on the element's title.
function rightsStem(program){const n=shortName(program),i=n.lastIndexOf('.');return i>0?n.slice(0,i):n}
// rightsNamed keeps the names every reply carries (Names, names), so a
// program read once keeps its name across the page's lists.
function rightsNamed(names){if(names)Object.assign(rightsNames,names)}
function rightsAppName(program){if(rightsNames&&rightsNames[program])return rightsNames[program];return rightsStem(program)+' (not registered)'}
function appendRightsRows(box,rules){
  for(const r of rules){const row=document.createElement('div'),text=document.createElement('div'),label=document.createElement('span'),caption=document.createElement('span'),control=document.createElement('div'),state=document.createElement('span'),edit=document.createElement('button');
   row.className='oa-row';row.permission=r;text.className='oa-row-text';label.className='oa-row-label';caption.className='oa-row-caption';control.className='oa-row-control';
   label.textContent=r.Selector?selectorPermissionLabel(r.Selector.Permission):rulePlain(rightsActionPlainCache,r.Action);label.title=r.Action;
   caption.textContent=(r.Selector?selectorScopeLabel(r.Selector.Scope,r.Selector.Name||r.Selector.Target):rightsTargetLabel(r.Resource))+' · '+(r.Expires?(r.Expired?'Expired ':'Until ')+new Date(r.Expires).toLocaleString():'Until removed');caption.title=r.Resource+(r.Expires?' · '+r.Expires:'');
   state.textContent=(r.Expired?'Expired · ':'')+(r.Permit?'Allowed':'Blocked');
   edit.textContent='Edit';edit.viewOnly=!!r.Selector&&!r.Selector.Editable;edit.disabled=edit.viewOnly;const said=document.createElement('p');said.className='oa-status';said.setAttribute('role','status');said.setAttribute('aria-live','polite');said.setAttribute('data-state','idle');
   edit.onclick=()=>{editListedRule(r,r.Selector&&!r.Selector.CanGrant?'remove':r.Permit?'permit':'deny');if(el('rights-editor').open){said.textContent='Review the change in the permission editor below.';said.setAttribute('data-state','ok')}};
   const remove=document.createElement('button');remove.textContent='Remove…';remove.viewOnly=!!r.Selector&&!r.Selector.Editable;remove.disabled=remove.viewOnly;row.remove=remove;
   // Remove asks for a second click within ten seconds, as Credentials does.
   remove.onclick=()=>{if(!remove.armed||Date.now()-remove.armed>10000){remove.armed=Date.now();said.textContent='Select Remove again within ten seconds to remove this permission.';said.setAttribute('data-state','idle');return}
    remove.armed=0;
    if(r.Selector){editListedRule(r,'remove');said.textContent='Review the removal in the permission editor below.';said.setAttribute('data-state','ok');return}
    return rightsEdit({edit:'revoke',account:r.Subject.Account,program:r.Subject.Program,action:r.Action,resource:r.Resource})};
   text.append(label,caption);control.append(state,edit,remove);row.append(text,control,said);box.append(row);appendRuleDetails(box,r)}
}
// appendRightsTable draws one group per app: its name as the heading, its
// rows, then the rules OpenAbstractions set up for it (its installation
// rules) in one closed expander with their count.
// OpenAbstractions' own programs (the Panel and the runtime) share one group:
// the server names them by product name, and a registered app never reads as
// one (reservedTitle). Each rule's Details names its program.
const rightsProductNames=['OpenAbstractions','OpenAbstractions Panel'];
function rightsIsOwnProgram(program){return rightsProductNames.includes(rightsNames[program])}
// Each app's group is an oa-group, its rows and expanders its direct
// children, so every skin draws it as a grouped list.
function appendRightsTable(out,rules){if(!rules.length)return;const groups=new Map();for(const r of rules){const own=rightsIsOwnProgram(r.Subject.Program),k=JSON.stringify([r.Subject.Account,own?'':r.Subject.Program]);if(!groups.has(k))groups.set(k,{subject:r.Subject,own,programs:new Set(),rules:[]});groups.get(k).programs.add(r.Subject.Program);groups.get(k).rules.push(r)}
 for(const g of groups.values()){const box=document.createElement('div'),title=document.createElement('h3');box.className='oa-group';box.subject=g.subject;title.textContent=(g.own?'OpenAbstractions':rightsAppName(g.subject.Program))+(exploreSelf&&g.subject.Account!==exploreSelf.account?' (another account)':'');title.title=[...g.programs].join('\n')+' — '+g.subject.Account;box.append(title);
  const setUp=g.rules.filter(r=>r.Why==='installation'&&r.Permit);appendRightsRows(box,g.rules.filter(r=>!setUp.includes(r)));
  if(setUp.length){const entry=document.createElement('details'),summary=document.createElement('summary'),body=document.createElement('div');entry.className='oa-expander';entry.installed=true;body.className='oa-expander-body';rightsSummary(summary,installedEntryName(setUp.length));entry.append(summary,body);appendRightsRows(body,setUp);box.append(entry)}
  out.append(box)}}
// Why is an operator-entered reason, including the runtime's seed reason.
// It supplies no installation provenance; every row shows its actual setter.
function installedEntryName(count){return 'Set up by OpenAbstractions ('+count+')'}
// showRightsList used to write "Rules revision: <8 hex>." as its own small
// muted line, dropping the inert "More rules remain."/"End of rules."
// wording entirely (the rights-next button beside it already carries that
// state). Even truncated, a revision is an identifier, and this Panel no
// longer shows one as visible text at all (task 2026-09-23, eighth
// first-time visitor, finding 2). The full value now lives only on the
// rules list heading's own title and in this section's Technical details.
function showRightsList(revision,empty){const out=el('rights-status');out.replaceChildren();if(empty)out.append(document.createTextNode('No rules on this page; unlisted actions are not granted.'));if(rightsSelectorStatus==='unsupported')para(out,'This service provides legacy exact permission management. Typed scopes and saved typed rules are unavailable in this view.');el('rights-heading').title=revision||'';}
// rightsRevisionDetails fills the list's Technical details, the Saved
// permissions section's last disclosure, after its groups and rows.
function rightsRevisionDetails(revision){const details=el('rights-revision'),body=el('rights-revision-body');details.hidden=!revision;body.textContent=revision?'Revision: '+revision:'';rightsBody(body)}
// rightsList reads one page at a time by default, on arrival, on Refresh
// (which resets rightsCursor to '' first) and on Next page. This runtime
// already grants itself dozens of rules at installation, so a rule Save
// rule or Allow downloads just landed for a freshly named program almost
// never sorts onto the page rightsCursor happened to be sitting on; it was
// never shown, on arrival or on Refresh, even though the confirmation said
// it was set (task 2026-09-23, fifteenth first-time visitor, finding 3).
// A caller that just wrote a rule instead passes target, the exact subject,
// action and resource the write echoed back; rightsList then walks forward
// from the first page, rendering every page along the way, until that rule
// turns up or the list is complete, so the page it actually landed on is
// the one already on screen. A revoke passes no target: the row it removed
// has nothing left to find, so page one is enough.
async function rightsList(target,automatic=false,signal){
 const generation=++rightsListGeneration;
 if(target&&rightsAppFilter&&(target.account!==rightsAppFilter.account||target.program!==rightsAppFilter.program)){rightsAppFilter=null;el('rights-filter').hidden=true}
 const find=target&&(r=>r.Subject.Account===target.account&&r.Subject.Program===target.program&&(target.selector||r.Action===target.action&&r.Resource===target.resource));
 const restarting=!!target||!rightsCursor;
 if(target)rightsCursor='';
 const rightsNamesRead={};let rules=[],catalog=null,complete=false,pages=0,cursor=rightsCursor,pageRevision=restarting?'':rightsRevision,plain=null,descriptions={},definitions=rightsDefinitions,bundles=rightsBundles,selectorAvailable=rightsSelectorAvailable,selectorStatus=rightsSelectorStatus;
 // draw shows the rules read so far: after the first page, while the rest
 // is read in the background, and again when the read ends. Show more
 // appears only when rows remain past the read's bound.
 const draw=async final=>{
 const done=final?complete:true;
 rightsNamed(rightsNamesRead);
 await ensureSelf(false);
 if(generation!==rightsListGeneration)return false;
 // Capture live inputs after every awaited read, immediately before the
 // synchronous chooser rebuild. Typing during a refresh keeps its draft base.
 const saved=Object.values(rightsDrafts).some(d=>d.dirty)?rightsDraftValues():null;
 rightsListedRules=rules.slice();rightsAvailable=true;rightsRevision=pageRevision;rightsCursor=final?cursor:rightsCursor;rightsActionPlainCache=plain;rightsDescriptionsCache=descriptions;rightsDefinitions=definitions;rightsBundles=bundles;rightsSelectorAvailable=selectorAvailable;rightsSelectorStatus=selectorStatus;invalidateRightsDrafts(pageRevision,false);
 rightsAccountChooser.rebuild();
 el('rights').replaceChildren();
 if(restarting){rebuildPermissionChooser(catalog||[]);rebuildCapabilityFilter()}
 allowPrograms(rules.map(r=>r.Subject));
 for(const r of rules)if(!r.Selector)rememberResource(r.Action,r.Resource);
 rebuildResourceChooser();
 el('rights-next').disabled=done;el('rights-next').hidden=done||!final;
 showRightsList(rightsRevision,!rules.length);
 if(rightsAppFilter){
  rules=rules.filter(r=>r.Subject.Account===rightsAppFilter.account&&r.Subject.Program===rightsAppFilter.program);
  if(!rightsFilterApplied){rightsProgramsSeen.add(rightsAppFilter.program);rightsProgramChooser.rebuild();el('rights-program-select').value=rightsAppFilter.program;el('rights-program-select').onchange();el('rights-account-select').value=exploreSelf&&exploreSelf.account===rightsAppFilter.account?rightsAppFilter.account:'__other__';el('rights-account-select').onchange();el('rights-account').value=rightsAppFilter.account;rightsFilterApplied=true;if(el('rights-heading').focus)el('rights-heading').focus()}
  if(!rules.length)emptyLine(el('rights'),restarting&&done?'This app has no saved permissions.':done?'No saved permissions for this app on these pages.':'No saved permissions for this app on these pages; choose Show more.');
 }
 rightsExpiringAt=rules.filter(r=>r.Expires&&!r.Expired).map(r=>r.Expires).sort()[0]||'';
 const selectedCapability=el('rights-capability-filter').value;if(selectedCapability){rules=rules.filter(r=>(r.Selector?selectorActionValue(r.Selector.Permission):r.Action)===selectedCapability);if(!rules.length)emptyLine(el('rights'),done?'No permissions for this capability.':'No permissions for this capability on these pages; choose Show more.')}
 if(rightsAppFilter)el('rights-filter-label').textContent='Permissions for '+rightsAppName(rightsAppFilter.program);appendRightsTable(el('rights'),rules);rightsRevisionDetails(rightsRevision);
 const ruled=new Set(rules.map(r=>r.Action));const unruled=(catalog||[]).filter(a=>!ruled.has(a));
 el('rights-unruled').replaceChildren();if(unruled.length&&!rightsAppFilter){const section=el('rights-unruled');heading(section,'Actions no rule names yet');const list=document.createElement('ul');for(const a of unruled){const li=document.createElement('li');li.textContent=rulePlain(rightsActionPlainCache,a);li.title=a;appendActionDescription(li,a);list.append(li)}section.append(list)}
 if(saved)restoreRightsDraftValues(saved);updateRightsDraftControls();
 if(final)defaultsList();
 return true};
 try{
  for(;;){
   const firstPage=pages===0&&restarting;
   const p=await call('/rights?cursor='+encodeURIComponent(cursor),undefined,signal);
   if(generation!==rightsListGeneration)return;
   if(p.Outcome!=='page'){rightsAvailable=false;rightsRevision='';updateRightsDraftControls();show('rights-status',rightsStatus(p.Outcome));el('rights-next').disabled=true;el('rights-next').hidden=true;return}
   if(!firstPage&&pageRevision&&p.Revision!==pageRevision){rightsAvailable=false;rightsRevision='';updateRightsDraftControls();show('rights-status',rightsStatus('gap'));return}
   if(pages===0)catalog=p.Catalog;
   if(firstPage){definitions=p.Definitions||[];bundles=p.Bundles||[];selectorAvailable=!!p.SelectorAvailable;selectorStatus=p.SelectorStatus||''}plain=p.ActionPlain;descriptions=p.Descriptions||{};Object.assign(rightsNamesRead,p.Names||{});pageRevision=p.Revision;cursor=p.Next;
   rules=rules.concat(p.Rules);complete=p.Complete;
   pages++;
   if(complete||pages>=20)break;
   if(pages===1&&restarting){if(!await draw(false))return;rightsFirstDrawn()}
  }
 }catch(e){if(generation!==rightsListGeneration)return;rightsAvailable=false;rightsRevision='';updateRightsDraftControls();fail('rights-status',e);return}
 const listed=rules.slice();if(await draw(true)){rightsFirstDrawn();return {rules:listed,complete,revision:pageRevision}}
}
el('rights-next').onclick=()=>rightsList();
// Defaults for all apps (rights_defaults.go): one switch per account-wide
// default, read after each complete permissions read, so a change made
// elsewhere shows through the same watch. A click writes one SetDefault at the
// revision it was drawn from and one sentence.
let defaultsRevision='',defaultsShown='';const defaultsRows={rows:new Map(),order:[],drawn:false};
const defaultsText={unsupported:'This runtime has no defaults for all apps. Update OpenAbstractions to change them.'};
function defaultStatus(o){return defaultsText[o]||rightsStatus(o)}
async function defaultsList(){try{const v=await call('/rights/defaults');if(v.Outcome!=='page'){defaultsRevision='';defaultsShown='';el('defaults-list').replaceChildren();defaultsRows.drawn=false;show('defaults-status',defaultStatus(v.Outcome));return}defaultsRevision=v.Revision;const shown=JSON.stringify(v.Defaults);if(shown===defaultsShown){defaultsRows.rows.forEach(b=>b.toggle.disabled=false);return}defaultsShown=shown;rightsRows(el('defaults-list'),defaultsRows,v.Defaults,d=>JSON.stringify(d),defaultItem,(box,d)=>{box.toggle.checked=!!d.On;box.toggle.disabled=false},'No defaults.')}catch(e){defaultsRevision='';defaultsShown='';fail('defaults-status',e)}}
function defaultItem(d){const toggle=document.createElement('input');toggle.type='checkbox';toggle.className='switch';if(toggle.setAttribute)toggle.setAttribute('aria-label',d.Label);toggle.onchange=()=>defaultSet(d,toggle);const caption=d.SetBy?(d.On?'Turned on':'Turned off')+' by '+rightsAppName(d.SetBy.Program)+(d.SetAt?', '+new Date(d.SetAt).toLocaleString():'')+'.':'';const box=rightsItem({label:d.Label,caption,controls:[toggle],details:[{summary:'Technical details',text:'Default: '+d.Name+'\nAction: '+d.Action+'\nResource: '+(d.EveryResource?'every resource of this action':d.Resource)+'\nState: '+(d.On?'on':'off')+'\nRevision: '+defaultsRevision}]});box.toggle=toggle;return box}
async function defaultSet(d,toggle){const on=!!toggle.checked;if(!defaultsRevision){toggle.checked=!on;show('defaults-status',defaultStatus('unavailable'));return}toggle.disabled=true;try{const r=await call('/rights/defaults',{revision:defaultsRevision,name:d.Name,on});defaultsShown='';await defaultsList();if(r.Outcome==='applied')show('defaults-status',(on?'Every app may now ':'No app may ')+d.Phrase+(on?'.':' unless you allowed that app.'));else show('defaults-status',defaultStatus(r.Outcome))}catch(e){defaultsShown='';await defaultsList();fail('defaults-status',e)}}
el('rights-ttl').value='0';el('rights-scope').value='one';
// rightsFirstPage settles once Saved permissions first shows rows.
let rightsFirstDrawn=()=>{};var rightsFirstPage=new Promise(resolve=>{rightsFirstDrawn=resolve});
var rightsListReady=rightsList();
var questionsReady=questions();
for(const[name,form]of[['grant','rights-grant'],['delegation','delegation-form']]){const edited=event=>{if(name==='grant'&&event?.target?.id&&event.target.id!=='rights-broad-confirm')el('rights-broad-confirm').checked=false;markRightsDraft(name)};el(form).addEventListener('input',edited);el(form).addEventListener('change',edited);el('rights-review-'+name).onclick=()=>reviewRightsDraft(name)}
if(typeof window.addEventListener==='function')window.addEventListener('pagehide',()=>{stopRightsWatch();rightsAvailable=false;updateRightsDraftControls()});
var rightsWatchReady=rightsListReady.then(followRights);
const allowSeen=new Set();let allowAccount='';
function allowPrograms(subjects){for(const s of subjects){if(!allowAccount)allowAccount=s.Account;if(allowSeen.has(s.Program))continue;allowSeen.add(s.Program)}rightsProgramChooser.rebuild();delegationProgramChooser.rebuild()}
// One-request answering picks its executable from the registered apps and
// the programs saved permissions name, Other… for any other.
const delegationProgramChooser=oaProgramChooser('delegation-program-select','delegation-program',()=>[...allowSeen],program=>rightsNames[program]);
el('delegation-program-select').addEventListener('change',delegationEffect);
// GET /rights/delegation answers delegationPage (rights_delegation.go) with
// lowercase keys: outcome, revision, account, questions. Its POST answers a
// rights PolicyEdit: Outcome, Revision, Current. rights_page_handler_test.go
// runs this script against the real handler (live walkthrough 2026-10-02, D2).
let delegationSnapshot=null;const delegationChoices=new Map();
function delegationStatus(outcome){return ({forbidden:'The question or permission service denied this request.',unavailable:'A required service is unavailable. No grant is assumed.',gap:'The request list changed. Refresh it before setting up a grant.',invalid:'The service rejected the request as invalid.',policy_forbidden:'The permission list is not available to this Panel.',policy_unavailable:'The permission service is unavailable.',policy_gap:'The permission list changed. Refresh permissions before setting up a grant.',policy_invalid:'The permission service rejected its current policy list.'})[outcome]||'Service result: '+outcome}
function delegationEffect(){const q=delegationChoices.get(el('delegation-question').value),option=el('delegation-option').value;el('delegation-question-details').textContent=q?q.text+'\nRequesting app: '+q.program+'\nAction: '+q.action+'\nResource: '+q.resource:'';rightsBody(el('delegation-question-details'));el('delegation-effect').textContent=option==='allow'?'The responder may submit Allow for this request. That can give the requesting app the exact action and resource permission for up to five minutes; the grant expires sooner if this answer permission expires.':option==='refuse'?'The responder may submit Refuse for this request. Refusal records no permission for the requesting app.':'Choose whether the responder may submit Allow or Refuse.';const path=el('delegation-program').value;el('delegation-save').disabled=!(rightsAvailable&&!rightsDrafts.delegation.stale&&!rightsDrafts.delegation.pending&&delegationSnapshot&&q&&option&&path&&el('delegation-confirm').checked);}
async function loadDelegation(){await rightsListReady;delegationChoices.clear();el('delegation-question').replaceChildren();const placeholder=document.createElement('option');placeholder.value='';placeholder.textContent='Choose a pending first-use request';el('delegation-question').append(placeholder);el('delegation-option').replaceChildren();const optionPrompt=document.createElement('option');optionPrompt.value='';optionPrompt.textContent='Select a request first';el('delegation-option').append(optionPrompt);try{const p=await call('/rights/delegation');delegationSnapshot=p;rightsNamed(p.names);if(p.outcome!=='page'){show('delegation-status',delegationStatus(p.outcome));return}for(const q of(p.questions||[])){delegationChoices.set(q.id,q);const o=document.createElement('option');o.value=q.id;o.textContent=q.sentence||q.text;o.title=q.program+' — '+q.action+' on '+q.resource;el('delegation-question').append(o)}for(const q of delegationChoices.values()){for(const answer of q.options){if(answer!=='allow'&&answer!=='refuse')continue;const o=document.createElement('option');o.value=answer;o.textContent=answer==='allow'?'Allow':'Refuse';el('delegation-option').append(o);break}}show('delegation-status',p.questions&&p.questions.length?'This grant will apply to '+accountLabel(p.account)+'. Choose a request, answer and executable.':'No pending first-use request is available to delegate. Refresh requests to check again.')}catch(e){delegationSnapshot=null;fail('delegation-status',e)}delegationEffect()}
el('delegation-question').onchange=()=>{const q=delegationChoices.get(el('delegation-question').value),select=el('delegation-option');select.replaceChildren();const prompt=document.createElement('option');prompt.value='';prompt.textContent=q?'Choose one answer':'Select a request first';select.append(prompt);if(q)for(const answer of q.options){if(answer!=='allow'&&answer!=='refuse')continue;const o=document.createElement('option');o.value=answer;o.textContent=answer==='allow'?'Allow':'Refuse';select.append(o)}delegationEffect()};
el('delegation-option').onchange=delegationEffect;el('delegation-program').oninput=delegationEffect;el('delegation-confirm').onchange=delegationEffect;
el('delegation-form').onsubmit=async e=>{e.preventDefault();const q=delegationChoices.get(el('delegation-question').value);if(!q||!delegationSnapshot||!el('delegation-confirm').checked)return;const revision=beginRightsDraftEdit('delegation');if(!revision)return;try{const r=await call('/rights/delegation',{questionId:q.id,responder:el('delegation-program').value,option:el('delegation-option').value,revision,confirmExactUse:true});if(r.Outcome==='applied'){resetRightsDraft('delegation');const confirmation='Five-minute answer permission set for '+rightsAppName(el('delegation-program').value)+'. Scope: '+rightsAppName(q.program)+' may receive '+el('delegation-option').value+' for '+rulePlain(rightsActionPlainCache,q.action)+' on '+oaResourceLabel(q.resource)+'. The responder still must make that answer.';await rightsList({account:delegationSnapshot.account,program:el('delegation-program').value,action:'abstraction.asks/question.answer',resource:r.Current&&r.Current.Resource||''});await loadDelegation();show('delegation-status',confirmation);return}if(r.Outcome==='conflict'){rightsDrafts.delegation.stale=true;await rightsList(undefined,true);updateRightsDraftControls()}show('delegation-status',rightsStatus(r.Outcome)+(r.Outcome==='conflict'? ' Refresh permissions and review the current request.':''))}catch(err){fail('delegation-status',err)}finally{endRightsDraftEdit('delegation')}};
var delegationReady=loadDelegation();
// The Assistants section (rights_assistant.go). GET /rights/assistant
// answers outcome, revision, assistants, candidates and warning; POST
// answers outcome and message. Every name and question comes from an app
// or a program path and is set as text, never as markup.
let assistantSnapshot=null;
const assistantChooser=oaProgramChooser('assistant-program-select','assistant-program',()=>assistantSnapshot?assistantSnapshot.candidates:[],program=>rightsNames[program]);
function assistantWarning(){const box=el('assistant-warning');box.replaceChildren();const program=el('assistant-program').value;if(!assistantSnapshot||!program)return;const w=assistantSnapshot.warning,name=rightsAppName(program);heading(box,'Let '+name+' answer OpenAbstractions questions for you');para(box,name+' will answer routine questions from your apps. It may let an app:');const yes=document.createElement('ul');for(const words of w.answers){const li=document.createElement('li');li.textContent=words;yes.append(li)}box.append(yes);para(box,'These always come to you:');const no=document.createElement('ul');for(const words of w.staysWithYou){const li=document.createElement('li');li.textContent=words;no.append(li)}box.append(no);if(w.choices)para(box,w.choices);para(box,'Warning: '+name+' '+w.caution);para(box,w.ends);el('assistant-save').textContent='Let '+name+' answer'}
function assistantReady(){el('assistant-save').disabled=!(assistantSnapshot&&el('assistant-program').value&&el('assistant-confirm').checked)}
let assistantsChain=Promise.resolve(),assistantsShown='';
function loadAssistants(){const run=assistantsChain.then(assistantsPass);assistantsChain=run.catch(()=>{});return run}
function assistantRow(a){const cleanup=document.createElement('label'),check=document.createElement('input');check.type='checkbox';cleanup.append(check,' Also revoke permissions granted through this app');const stop=document.createElement('button');stop.textContent='Revoke answering access';const never=document.createElement('button');never.textContent="Stop and don't ask again";const box=rightsItem({label:'',controls:[cleanup,stop,never]});const revoke=async(dontAskAgain)=>{try{const r=await call('/rights/assistant',{action:'stop',program:box.program,revision:assistantSnapshot.revision,alsoRevokeGrants:check.checked,dontAskAgain});await loadAssistants();await loadAnswered();show('assistant-status',r.message)}catch(e){fail('assistant-status',e)}};stop.onclick=()=>revoke(false);never.onclick=()=>revoke(true);return box}
async function assistantsPass(){try{const p=await call('/rights/assistant');assistantSnapshot=p;rightsNamed(p.names);const list=el('assistant-list');if(p.outcome!=='page'){list.replaceChildren();assistantRows.drawn=false;assistantsShown='';show('assistant-status',rightsStatus(p.outcome));return}
 rightsRows(list,assistantRows,p.assistants,a=>a.program,assistantRow,(box,a)=>{box.program=a.program;rightsFill(box,a.name+' answers routine questions for you'+ruleExpiryText(a.expires)+'.','',a.program)},'You answer questions yourself.');
 const shown=JSON.stringify([p.candidates,p.warning]);if(shown===assistantsShown)return;assistantsShown=shown;
 assistantChooser.rebuild();if(p.candidates.length&&el('assistant-program-select').value==='__other__'){el('assistant-program-select').value=p.candidates[0];el('assistant-program-select').onchange()}assistantWarning();assistantReady()}catch(e){assistantSnapshot=null;fail('assistant-status',e)}}
el('assistant-program-select').addEventListener('change',()=>{assistantWarning();assistantReady()});el('assistant-program').oninput=()=>{assistantWarning();assistantReady()};el('assistant-confirm').onchange=assistantReady;
el('assistant-form').onsubmit=async e=>{e.preventDefault();if(!assistantSnapshot||!el('assistant-confirm').checked)return;try{const r=await call('/rights/assistant',{action:'enroll',program:el('assistant-program').value,revision:assistantSnapshot.revision,confirm:true});el('assistant-confirm').checked=false;await loadAssistants();show('assistant-status',r.message)}catch(err){fail('assistant-status',err)}};
const answeredStateText={in_force:'',changed:' You changed this permission since.',ended:' This permission has ended.',refused:''};
let answeredChain=Promise.resolve();
function loadAnswered(){const run=answeredChain.then(answeredPass);answeredChain=run.catch(()=>{});return run}
function answeredRow(){const undo=document.createElement('button');undo.textContent='Undo';const box=rightsItem({label:'',controls:[undo]});box.undo=undo;undo.onclick=async()=>{try{const r=await call('/rights/answered',{id:box.answerId});await loadAnswered();show('answered-status',r.message)}catch(e){fail('answered-status',e)}};return box}
async function answeredPass(){try{const p=await call('/rights/answered');const list=el('answered-list');if(p.outcome!=='page'){list.replaceChildren();answeredRows.drawn=false;show('answered-status',questionStatus(p.outcome));return}
 rightsRows(list,answeredRows,p.answers,a=>a.id,answeredRow,(box,a)=>{const when=a.answered?new Date(a.answered).toLocaleString():'';box.answerId=a.id;box.undo.hidden=a.state!=='in_force';rightsFill(box,a.sentence,a.kind==='choice'?a.answererName+' chose '+choiceDisplay(a.optionLabel||a.option)+(when?' on '+when:'')+'. The app may already have acted on it, so it cannot be undone here.':a.answererName+' answered '+(a.option==='allow'?'Allow':'Not now')+(when?' on '+when:'')+'.'+(answeredStateText[a.state]||''),a.answerer)},'No questions answered for you yet.')}catch(e){fail('answered-status',e)}}
var assistantReadyPromise=loadAssistants();var answeredReady=loadAnswered();
// Resource opened as a bare free-text field, no hint what to type (a
// registry name for a download action, a host name for an inference
// action, a folder for a storage action). rightsResourcesSeen keeps, per
// action, every resource a rule already loaded here has named it with;
// choosing an action now offers a dropdown built from what this Panel
// already knows that action's own resources look like, "Other…" for
// anything else (task 2026-09-23, fifteenth first-time visitor, finding
// 6). The chooser rebuilds each time rightsList reads more rules and each
// time the action changes, the latter needing no new read since every
// action's resources loaded so far are already in rightsResourcesSeen.
const rightsResourcesSeen=new Map();
function rememberResource(action,resource){if(!action||!resource)return;if(!rightsResourcesSeen.has(action))rightsResourcesSeen.set(action,new Set());rightsResourcesSeen.get(action).add(resource)}
function resourceChooserSync(){const select=el('rights-resource-select'),input=el('rights-resource');const v=select.value;if(v==='__other__'){input.hidden=false;if((rightsResourcesSeen.get(el('rights-action').value)||new Set()).has(input.value))input.value=''}else{input.hidden=true;input.value=v}}
// preferFirst is true only when the action itself just changed (the
// action's own onchange below): the previous selection was necessarily
// scoped to a different action and is never worth preserving, so the
// chooser picks that new action's own first known resource instead of
// falling all the way back to Other…. A routine rebuild after rightsList
// reads more rules for the same action (no argument) still only replaces
// the current selection if it stopped being valid, the same as every
// other chooser here.
function rebuildResourceChooser(preferFirst){const select=el('rights-resource-select');const current=select.value;const known=[...(rightsResourcesSeen.get(el('rights-action').value)||[])].sort();select.replaceChildren();for(const r of known){const o=document.createElement('option');o.value=r;o.textContent=r;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);select.value=current&&known.indexOf(current)!==-1?current:(preferFirst&&known.length?known[0]:'__other__');resourceChooserSync()}
el('rights-resource-select').onchange=resourceChooserSync;
el('rights-action').onchange=()=>{el('rights-origin-id').value='';rightsOriginDraft=null;el('rights-broad-confirm').checked=false;rebuildResourceChooser(true);rightsGrantPreview();if(chosenSelectorCapability())loadRightsSecretNames()};el('rights-scope').onchange=()=>{el('rights-broad-confirm').checked=false;rightsGrantPreview()};
// Program path opened as one free-text field with no hint what to type; it
// now offers the registered applications (oaProgramChooser reads them),
// every program already named by a rule (allowSeen, the same set Allow's
// own datalist already builds) and the program a rule being edited names,
// "Other…" revealing the typed field for anything else (task 2026-09-23,
// twelfth first-time visitor, requirement 2). oaProgramChooser
// (panel_shell.go) is the one shared implementation Inference's Issue key
// and Credentials' App access build their own equivalent chooser from too
// (task 2026-09-23, fourteenth first-time visitor, finding 8).
let rightsProgramsSeen=new Set();
const rightsProgramChooser=oaProgramChooser('rights-program-select','rights-program',()=>[...allowSeen,...rightsProgramsSeen],program=>rightsNames[program]);
rightsProgramChooser.rebuild();var rightsDeviceHoldersReady=rightsProgramChooser.ready;
// oaAccountChooser (panel_shell.go) is the one shared implementation
// Credentials' App access now builds its own equivalent chooser from too
// (task 2026-09-23, fourteenth first-time visitor, finding 8).
const rightsAccountChooser=oaAccountChooser('rights-account-select','rights-account',()=>exploreSelf&&exploreSelf.account,()=>rightsSelfAccountName);
rightsAccountChooser.rebuild();var rightsSelfReady=ensureSelf();
// Save rule's three required fields now name themselves when left blank
// (task 2026-09-23, eighth first-time visitor, finding 4, the Issue key
// pattern): the browser blocks the submit event for a failed required
// field without running this form's own onsubmit, but always fires
// "invalid" on the field, submit or not.
const rightsGrantFieldLabel={'rights-account':'Account','rights-program':'Program path','rights-resource':'Resource'};
for(const id in rightsGrantFieldLabel)el(id).addEventListener('invalid',()=>show('rights-status',rightsGrantFieldLabel[id]+' is required.'));
el('rights-grant').onsubmit=e=>{e.preventDefault();const edit=rightsGrantPayload();if(edit)return rightsEdit(edit,'grant')};
el('rights-grant').addEventListener('input',rightsGrantPreview);el('rights-grant').addEventListener('change',rightsGrantPreview);
// The screenshot tool waits for data-ready once the first reads finished.
var rightsArrived=Promise.all([rightsFirstPage,questionsReady,choicesReady,permissionsReady,delegationReady,assistantReadyPromise,answeredReady]).then(()=>{if(typeof oaReady==='function')oaReady()});
function rightsLive(){if(document.visibilityState==='hidden')return Promise.resolve();return Promise.all([questionCursor===''?questions():null,choiceCursor===''?choicesList():null,permissionCursor===''?permissionsList():null,loadAnswered(),loadAssistants()])}
if(typeof document.addEventListener==='function'){const tick=()=>setTimeout(async()=>{await rightsLive();tick()},5000);tick();document.addEventListener('visibilitychange',rightsLive)}
</script>`
