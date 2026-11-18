package main

import "strings"

// rightsPage is the Permissions page (research/panel-redesign/SPEC.md §4,
// S3): what your apps may do. Its words follow
// research/vocabulary/DECISION.md: permissions, Allow and Block (D53);
// Dismiss for a question closed without an answer (D50), while the wire
// action stays "retire" until asks/operator@2. Sections in order: Questions
// waiting (first-use requests and app questions in one list; the Panel's
// navigation badge counts them), Saved permissions by app, Defaults for all
// apps (rights_defaults.go), Messages between agents (the agent-messages
// default, AB-1w), Answering access, Advanced (the Panel's own
// administrative access), then Past answers until Activity carries them.
// The page is presentation only: every read and write goes through the
// existing handlers (rights_panel.go, questions_panel.go and
// rights_assistant.go). Every list updates while the page is
// visible; "Show more" appears only past a page.
var rightsPage = pageOwnsReady(oaPageHead("rights", "Permissions", "What your apps may do.")) +
	`<section id="questions" data-runtime-echo class="oa-group"><h2>Questions waiting</h2><p class="oa-caption">Questions your apps are waiting on.</p>` +
	`<p class="oa-status" id="questions-status" role="status" aria-live="polite" data-state="idle"></p><div id="questions-list"></div>` +
	`<p class="oa-status" id="permissions-status" role="status" aria-live="polite" data-state="idle"></p><div id="permissions-list"></div>` +
	`<p class="oa-status" id="choices-status" role="status" aria-live="polite" data-state="idle"></p><div id="choices-list"></div>` +
	`<p class="oa-empty" id="questions-empty" hidden>No questions are waiting.</p>` +
	`<button id="questions-next" data-status="questions-status" hidden>Show more</button></section>` +
	`<section id="rights-section" data-runtime-echo class="oa-group"><h2 id="rights-heading" tabindex="-1">Saved permissions</h2><p class="oa-caption">What each app may do.</p>` +
	`<div id="rights-filter" hidden><p id="rights-filter-label"></p><a id="rights-show-all" href="#">Show all apps</a></div>` +
	`<div id="rights"></div>` +
	`<button id="rights-next" data-status="rights-status" hidden>Show more</button>` +
	`<div id="rights-status" class="oa-status" role="status" aria-live="polite" data-state="idle"></div>` +
	`</section>` +
	`<section id="defaults" data-runtime-echo class="oa-group"><h2>Defaults for all apps</h2><p class="oa-caption">Every app may do these unless you block that app.</p>` +
	`<p class="oa-status" id="defaults-status" role="status" aria-live="polite" data-state="idle"></p><div id="defaults-list"></div></section>` +
	// The switch stays hidden, its status line loading, until the first
	// defaults read shows its saved state (agentAskShow) or says why it cannot.
	runtimeGroup(oaGroup("agent-messages", "Messages between agents", agentMessagesCaption, strings.Replace(oaRow(agentMessagesAskLabel, agentMessagesAskCaption, `<input type="checkbox" class="switch" id="agent-messages-ask" aria-label="`+agentMessagesAskName+`" disabled hidden>`, "agent-messages-status"), oaStatusLine("agent-messages-status", false), oaStatus("agent-messages-status"), 1),
		// S5 (2026-11-22): one switch per agent pair, drawn by messageToolsList
		// (rights_message_tools.go).
		`<div id="agent-tools-list"></div><p class="oa-status" id="agent-tools-status" role="status" aria-live="polite" data-state="idle"></p>`)) +
	`<section id="assistant" data-runtime-echo class="oa-group"><h2>Answering access</h2><p class="oa-caption">The app that answers routine questions for you.</p><span id="answer-delegation"></span>` +
	`<div id="assistant-list"></div><p class="oa-status" id="assistant-status" role="status" aria-live="polite" data-state="idle"></p>` +
	`<details class="oa-expander"><summary>Choose an app that answers<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><form id="assistant-form"><label for="assistant-program-select">App that answers</label> <select id="assistant-program-select"></select><input id="assistant-program" hidden data-oa-other><div id="assistant-warning"></div><label><input id="assistant-confirm" type="checkbox"> I have read this.</label> <button id="assistant-save" data-status="assistant-status" disabled>Let it answer</button></form></div></details>` +
	`<details class="oa-expander"><summary>Answered for you<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><div id="answered-list"></div></div></details><p class="oa-status" id="answered-status" role="status" aria-live="polite" data-state="idle"></p></section>` +
	`<section id="rights-advanced" data-runtime-echo class="oa-group"><h2>Advanced</h2><p class="oa-caption">Exact permissions.</p>` +
	`<details id="rights-editor" class="oa-expander"><summary id="rights-editor-heading">Permission editor<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><form id="rights-grant"><p class="oa-caption">Choose Other… to enter an account, app path or exact resource that is not listed. All three are required.</p>
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
	oaExpander("Filter by capability", `<label>Capability <select id="rights-capability-filter"><option value="">All capabilities</option></select></label>`) +
	`<details id="rights-revision" class="oa-expander" data-oa-technical hidden><summary>Technical details<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body" id="rights-revision-body"></div></details>` +
	`<details id="rights-admin-access" class="oa-expander" hidden><summary>Panel administrative access<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><p>This Panel manages OpenAbstractions' permissions through its configuration, separately from saved app permissions. Removing a saved app permission leaves it in place.</p></div></details>` +
	`<details class="oa-expander"><summary>Capabilities without a saved permission<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body"><div id="rights-unruled"></div></div></details></section>` +
	`<section id="app-questions" data-runtime-echo class="oa-group"><h2>Past answers</h2><p class="oa-caption">Questions you answered or that closed.</p>` +
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
// The waiting lists read every page of their sources on each pass, up to
// waitingPageLimit pages each, so "No questions are waiting." covers every
// retained record and the live reread continues past the first page. One
// Show more appears only when a source holds more, and raises the bound for
// all three (architect ruling 2026-11-09, second block).
var waitingPageLimit=32;let questionsMore=false,choicesMore=false,permissionsMore=false;
// waitingPages reads path from its first page: outcome names a page's
// outcome, merge adds a later page to the first, done reports the last page
// and next its continuation. A failed page answers as itself; a read cut off
// by the bound carries more.
async function waitingPages(path,outcome,merge,done,next){let all=null,cursor='';for(let i=0;i<waitingPageLimit;i++){const page=await call(path+'?cursor='+encodeURIComponent(cursor));if(outcome(page)!=='page')return page;if(all)merge(all,page);else all=Object.assign({},page);if(done(page))return all;cursor=next(page)}all.more=true;return all}
// waitingJoin adds a later page's records to the earlier ones, once each.
function waitingJoin(earlier,later,id){const seen=new Set(earlier.map(id));return earlier.concat((later||[]).filter(r=>!seen.has(id(r))))}
function renderWaitingMore(){const b=el('questions-next');b.hidden=!(questionsMore||choicesMore||permissionsMore);b.disabled=b.hidden}
// Preserve app-owned text exactly, exposing control/format characters. The
// generic error translator must never reinterpret an app's title or labels.
function choiceDisplay(text){return String(text??'').replace(/[\p{Cc}\p{Cf}]/gu,c=>c==='\n'||c==='\r'||c==='\t'?c:'\\u'+c.codePointAt(0).toString(16).toUpperCase().padStart(4,'0'))}
const choiceText={unsupported:'This version of OpenAbstractions does not support ordinary app questions. Permission requests remain available.',forbidden:'This Panel is not authorized to answer app questions.',unavailable:'App questions are unavailable. No decision is assumed.',gap:'The app questions changed. Refresh them to continue.',invalid:'The service rejected this app question action.',unknown:'This app request is no longer retained.',gone:'The app has acknowledged this request.',expired:'This app request has expired; no option was selected.',conflict:'This app request was already decided or changed.'};
function choiceStatus(outcome){return choiceText[outcome]||'App question service outcome: '+outcome}
async function choiceAct(action,shown,option){try{const r=await call('/choices',{action,shown,option:option||''});if(r.Outcome==='answered'||r.Outcome==='dismissed'){await choicesList();show('choices-status',r.Outcome==='answered'?'Selection recorded for the app.':'Request closed without selecting an option.')}else show('choices-status',choiceStatus(r.Outcome))}catch(e){fail('choices-status',e)}}
// The waiting lists are read again while the page is visible. Passes run
// one at a time; a pass that reads what is already shown keeps the rows,
// their focus and the status sentence the last click wrote.
let choicesChain=Promise.resolve(),choicesShown='',choicesFailed=false;
function invalidateChoices(){choicesShown='';choicesFailed=true;choicesWaiting=null;renderQuestionsEmpty();el('choices-list').replaceChildren();el('choices-history-list').replaceChildren();choicesMore=false;renderWaitingMore()}
function choiceLive(record){return record.State==='pending'&&Date.now()<record.Request.PendingUntilMs&&Date.now()<record.Request.RequestRef.ReplayUntilMs}
function choicesList(){const run=choicesChain.then(choicesPass);choicesChain=run.catch(()=>{});return run}
async function choicesPass(){try{
 const page=await waitingPages('/choices',p=>p.Outcome,(all,p)=>{all.Records=waitingJoin(all.Records||[],p.Records,r=>r.ID);all.Shared=(all.Shared||[]).concat(p.Shared||[])},p=>p.Complete||!p.Next,p=>p.Next),pending=el('choices-list'),history=el('choices-history-list');
 if(page.Outcome!=='page'){invalidateChoices();show('choices-status',choiceStatus(page.Outcome));return}
 choicesMore=!!page.more;renderWaitingMore();if(choicesFailed){show('choices-status','');choicesFailed=false}
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
}catch(e){invalidateChoices();fail('choices-status',e)}}
var choicesReady=choicesList();
// Typed permission requests (asks ASK-A10, ASK-O10): the native dialog's
// words and answers. The answer names the effect digest the row showed; the
// server re-reads the request and answers only while it still matches.
let permissionsChain=Promise.resolve(),permissionsShown='',permissionsFailed=false;
function invalidatePermissions(){permissionsShown='';permissionsFailed=true;permissionsWaiting=null;renderQuestionsEmpty();el('permissions-list').replaceChildren();permissionsMore=false;renderWaitingMore()}
const permissionText={unsupported:'This version of OpenAbstractions does not list permission requests here.',forbidden:'This Panel is not authorized to answer permission requests.',unavailable:'Permission requests are unavailable. No decision is assumed.',gap:'The permission requests changed. Refresh them to continue.',invalid:'The service rejected this permission request action.'};
function permissionStatus(outcome){return permissionText[outcome]||'Permission request outcome: '+outcome}
function permissionsList(){const run=permissionsChain.then(permissionsPass);permissionsChain=run.catch(()=>{});return run}
async function permissionAct(request,option){try{const r=await call('/rights/requests',{id:request.id,option,digest:request.digest});await permissionsList();show('permissions-status',r.message)}catch(e){fail('permissions-status',e)}}
async function permissionsPass(){try{
 const page=await waitingPages('/rights/requests',p=>p.outcome,(all,p)=>{all.requests=waitingJoin(all.requests||[],p.requests,r=>r.id)},p=>p.complete||!p.next,p=>p.next),list=el('permissions-list');
 if(page.outcome!=='page'){invalidatePermissions();if(page.outcome==='unsupported'){permissionsWaiting=0;renderQuestionsEmpty();show('permissions-status','')}else show('permissions-status',permissionStatus(page.outcome));return}
 permissionsMore=!!page.more;renderWaitingMore();if(permissionsFailed){show('permissions-status','');permissionsFailed=false}
 const shown=JSON.stringify(page.requests);if(shown===permissionsShown)return;permissionsShown=shown;
 list.replaceChildren();
 for(const request of page.requests){
  const box=rightsKeep(permissionItems,JSON.stringify(request),()=>{
   const controls=request.choices.map(c=>{const b=document.createElement('button');b.textContent=c.label;b.onclick=()=>permissionAct(request,c.option);return b});
   return rightsItem({label:request.instruction,caption:request.warning?request.content+'\n'+request.warning:request.content,controls,details:[{summary:'Technical details',text:request.details}]})});
  list.append(box)}
 rightsKept(permissionItems);permissionsWaiting=page.requests.length;renderQuestionsEmpty();
}catch(e){invalidatePermissions();fail('permissions-status',e)}}
var permissionsReady=permissionsList();
const questionText={forbidden:'This Panel is not authorized as an operator of OpenAbstractions; nothing was changed.',unavailable:'The question service could not complete this; no decision is assumed. Retry or list again.',gap:'The question list changed; start the list again.',invalid:'The question service rejected this request as invalid.',unknown:'That question is no longer retained.',conflict:'A different answer is already recorded.'};
function questionStatus(o){return questionText[o]||'Question service outcome: '+o}
function ruleExpiryText(value,expired){const at=new Date(value);if(!value||Number.isNaN(at.getTime()))return '';return ((expired===undefined?at.getTime()<=Date.now():expired)?' (expired ':' (expires ')+at.toLocaleString()+')'}
function questionRule(r){
 if(r.ruleOutcome==='delegated')return '\nAn app answered through Answering access. Its permission was time-limited. To allow this permanently, add a permission under Saved permissions.';
 if(!r.rule)return r.partialError?'\nThe answer was recorded, but the permission needs review: '+r.partialError:'';
 const what=(r.rule.Permit?'allow ':'block ')+rulePhrase(rightsActionPlainCache,r.rule.Action)+(r.paired?' and '+rulePhrase(rightsActionPlainCache,r.paired.Action):'')+' on '+rightsTargetLabel(r.rule.Resource)+' for '+rightsAppName(r.rule.Subject.Program);
 if(r.edit&&r.edit.Outcome==='applied'&&(!r.paired||(r.pairedEdit&&r.pairedEdit.Outcome==='applied')))return '\nPermission saved: '+what+' (asked at first use).';
 if(r.partialError)return '\nThe answer was recorded, but the permission may not have been saved: '+what+'. '+r.partialError+' List the questions and choose Retry saving permission.';
 return '\nThe permission was not saved ('+(r.edit?r.edit.Outcome:r.ruleOutcome||'no decision')+'): '+what+'. List the questions and choose Retry saving permission.';
}
const ruleStateText={written:'Permission saved.',not_written:'Permission not saved.',delegated:'Answered through Answering access. The permission was time-limited.'};
// questionAct's own reply used to say "... List again to refresh." while
// the list under it kept its stale rows; it now refreshes the list itself
// before showing the confirmation, the same fix every other list-changing
// write on this page and elsewhere gets (task 2026-09-23, fourteenth
// first-time visitor, findings 2 and 3).
async function questionAct(a){try{const r=await call('/questions',a);if(a.action==='finish_provider'){await questions();show('questions-status',r.Message||'Setup was not finished.');return}if(r.Outcome==='answered'||r.Outcome==='retired'){const line=(a.action==='answer'?'Answer recorded':a.action==='rule'?'Tried saving the permission again':'Question dismissed')+'.'+questionRule(r);await questions();show('questions-status',line)}else show('questions-status',questionStatus(r.Outcome))}catch(e){fail('questions-status',e)}}
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
function invalidateQuestions(){questionsShown='';questionsFailed=true;questionsWaiting=null;renderQuestionsEmpty();el('questions-list').replaceChildren();el('questions-history-list').replaceChildren();questionsMore=false;renderWaitingMore()}
function questions(){const run=questionsChain.then(questionsPass);questionsChain=run.catch(()=>{});return run}
async function questionsPass(){
 try{
  await rightsListReady;
  const p=await waitingPages('/questions',p=>p.Outcome,(all,page)=>{all.Records=waitingJoin(all.Records||[],page.Records,r=>r.ID);for(const k of ['sentences','choices','ruleStates','programs','askers','nativePrompts'])if(page[k])all[k]=Object.assign({},all[k]||{},page[k])},page=>page.Complete||!page.Next,page=>page.Next);
  const pending=el('questions-list'),history=el('questions-history-list');
  if(p.Outcome!=='page'){invalidateQuestions();show('questions-status',questionStatus(p.Outcome));return}
  questionsMore=!!p.more;renderWaitingMore();if(questionsFailed){show('questions-status','');questionsFailed=false}
  const shown=JSON.stringify([p.Records,p.sentences,p.choices,p.ruleStates,p.programs,p.askers,p.nativePrompts]);if(shown===questionsShown)return;questionsShown=shown;
  pending.replaceChildren();history.replaceChildren();
  let pendingCount=0,answeredCount=0;
  for(const q of p.Records){
   const box=rightsKeep(questionItems,JSON.stringify([q,(p.programs||{})[q.ID],(p.sentences||{})[q.ID],(p.askers||{})[q.ID],(p.choices||{})[q.ID],(p.ruleStates||{})[q.ID],(p.nativePrompts||{})[q.ID]]),()=>{
   const s=questionSentence(q,(p.programs||{})[q.ID],(p.sentences||{})[q.ID],(p.askers||{})[q.ID]),choices=questionChoiceList(p,q);
   const answered=(choices.find(c=>c.option===q.Option)||{label:q.Option}).label,state=(p.ruleStates||{})[q.ID];
   const lines=s.line.split('\n'),status=(q.Option?'Answered: '+answered:'Waiting for your answer')+(state?' · '+(ruleStateText[state]||'Permission state: '+state):'');
   const providerSetupRow=(p.nativePrompts||{})[q.ID]&&q.Option==='allow',finishProvider=providerSetupRow&&state!=='written';
   const controls=[];if(providerSetupRow){const b=document.createElement('button');b.textContent=finishProvider?'Finish setup':'Check setup';b.onclick=()=>questionAct({action:'finish_provider',id:q.ID});controls.push(b)}else if(!q.Option)for(const c of choices){const b=document.createElement('button');b.textContent=c.label;b.onclick=()=>questionAct({action:'answer',id:q.ID,option:c.option});controls.push(b)}
   if(state==='not_written'&&!(p.nativePrompts||{})[q.ID]){const retry=document.createElement('button');retry.textContent='Retry saving permission';retry.onclick=()=>questionAct({action:'rule',id:q.ID});controls.push(retry)}
   if((p.nativePrompts||{})[q.ID])lines.push('Finish setup in the native OpenAbstractions permission prompt.');
   if(!providerSetupRow){const dismiss=document.createElement('button');dismiss.textContent='Dismiss';dismiss.onclick=()=>questionAct({action:'retire',id:q.ID});controls.push(dismiss)}
   return rightsItem({label:lines[0].replace(/\.$/,''),caption:lines.slice(1).concat([status]).join(' · '),title:s.title,controls,details:s.raw?[{summary:'Technical details',text:s.raw}]:[]});});
   if(q.Option&&!((p.nativePrompts||{})[q.ID]&&q.Option==='allow'&&(p.ruleStates||{})[q.ID]!=='written')){answeredCount++;history.append(box)}else{pendingCount++;pending.append(box)}
  }
  rightsKept(questionItems);
  questionsWaiting=pendingCount;renderQuestionsEmpty();
  if(!answeredCount)emptyLine(history,'No past answers.');
 }catch(e){invalidateQuestions();fail('questions-status',e)}
}
el('questions-next').onclick=()=>{waitingPageLimit+=32;return Promise.all([questions(),choicesList(),permissionsList()])};
let rightsCursor='',rightsRevision='',exploreSelf=null,rightsActionPlainCache=null,rightsDescriptionsCache={},rightsDefinitions=[],rightsBundles=[],rightsSelectorAvailable=false,rightsSelectorStatus='',rightsOriginDraft=null;const rightsText={forbidden:'This Panel is not authorized as an operator of OpenAbstractions; nothing was changed.',unavailable:'The rules service could not complete this; no change is assumed. List again before another edit.',gap:'The rules changed while listing; start the list again.',invalid:'The rules service rejected this rule as invalid.',conflict:'The rules changed since it was listed; nothing was changed. List again and reconcile before another edit.'};
function actionDescriptionText(action){const item=rightsDescriptionsCache[action],d=item&&item.Description;if(!d)return '';const risk=['routine','sensitive','administrative','unknown'].includes(d.DeclaredRisk)?d.DeclaredRisk:'unknown';return d.AppLabel+': '+d.Description+'\nFeature declared for '+rightsAppName(d.FeatureApp.Program)+' — '+accountLabel(d.FeatureApp.Account)+'.\nDeclared risk: '+risk+'. Options: Allow, Block, Remove rule.\nPublished by '+rightsAppName(item.PublishedBy.Program)+' — '+accountLabel(item.PublishedBy.Account)+'. This operator declaration does not verify the descriptor author or app behavior.'}
function appendActionDescription(box,action){const sentence=actionDescriptionText(action);if(!sentence)return;const details=document.createElement('details'),summary=document.createElement('summary'),text=document.createElement('div');rightsSummary(summary,'Action description');text.textContent=sentence;text.title=action;details.className='oa-expander';details.append(summary,rightsBody(text));box.append(details)}
const rightsQuery=new URLSearchParams(location.search);
let rightsAppFilter=rightsQuery.get('account')&&rightsQuery.get('program')?{account:rightsQuery.get('account'),program:rightsQuery.get('program')}:null;
let rightsFilterApplied=false;
if(rightsAppFilter){el('rights-filter').hidden=false;el('rights-filter-label').textContent='Permissions for '+rightsAppName(rightsAppFilter.program);el('rights-filter-label').title=rightsAppFilter.program+' — '+rightsAppFilter.account;el('rights-show-all').href='/rights?k='+encodeURIComponent(key)+'#rights-section'}
function rightsStatus(o){return rightsText[o]||'Rules service outcome: '+o}
let rightsListedRules=[];
let rightsAvailable=false,rightsListGeneration=0,rightsWatchRun=null,rightsObserveAfter='',rightsClosed=false;
const rightsDrafts={grant:{dirty:false,revision:'',stale:false}};
const rightsDraftFields=['rights-account-select','rights-account','rights-program-select','rights-program','rights-action','rights-resource-select','rights-resource','rights-scope','rights-name','rights-broad-confirm','rights-origin-id','rights-permit','rights-ttl','rights-why'];
function rightsDraftValues(){return rightsDraftFields.map(id=>({id,value:el(id).value,checked:el(id).checked,hidden:el(id).hidden}))}
function restoreRightsDraftValues(values){for(const f of values){const n=el(f.id);if(n.tagName==='SELECT'&&f.value&&!Array.from(n.options).some(o=>o.value===f.value)){const o=document.createElement('option');o.value=f.value;o.textContent='Previously selected';n.append(o)}n.value=f.value;n.checked=f.checked;n.hidden=f.hidden}}
function markRightsDraft(name){const d=rightsDrafts[name];if(!d.dirty){d.dirty=true;d.revision=rightsRevision;d.stale=d.revision!==rightsRevision}updateRightsDraftControls()}
function resetRightsDraft(name){if(name)rightsDrafts[name]={dirty:false,revision:'',stale:false};if(name==='grant'){rightsOriginDraft=null;el('rights-origin-id').value='';el('rights-broad-confirm').checked=false}updateRightsDraftControls()}
function invalidateRightsDrafts(revision,expiry){for(const d of Object.values(rightsDrafts))if(d.dirty&&(expiry||d.revision!==revision))d.stale=true;updateRightsDraftControls()}
function updateRightsDraftControls(){el('rights-admin-access').hidden=!rightsAvailable;for(const[name,form]of[['grant','rights-grant']]){const d=rightsDrafts[name],review=el('rights-review-'+name);review.hidden=!d.stale;review.disabled=!rightsAvailable;const save=el(form).querySelector&&el(form).querySelector('button:not([type="button"])');if(save)save.disabled=!rightsAvailable||d.stale||d.pending}if(el('rights').querySelectorAll)for(const button of el('rights').querySelectorAll('button'))button.disabled=!rightsAvailable||button.viewOnly===true;rightsGrantPreview()}
function rightsDraftRevision(name){const d=rightsDrafts[name];if(!rightsAvailable){show('rights-status',rightsStatus('unavailable'));return ''}if(d.pending)return '';if(d.stale){show('rights-status','Permissions changed. Your entries are kept. Review refreshed permissions before saving.');return ''}return d.dirty?d.revision:rightsRevision}
function beginRightsDraftEdit(name){const revision=rightsDraftRevision(name);if(revision){rightsDrafts[name].pending=true;updateRightsDraftControls()}return revision}
function endRightsDraftEdit(name){if(name)rightsDrafts[name].pending=false;updateRightsDraftControls()}
async function reviewRightsDraft(name){if(!rightsAvailable)return;const d=rightsDrafts[name];d.revision=rightsRevision;d.stale=false;updateRightsDraftControls()}
function rightsRetryPause(signal){return new Promise(resolve=>{if(signal.aborted){resolve();return}const done=()=>{clearTimeout(timer);signal.removeEventListener('abort',done);resolve()},timer=setTimeout(done,2000);signal.addEventListener('abort',done,{once:true})})}
function stopRightsWatch(){if(rightsWatchRun)rightsWatchRun.abort();rightsWatchRun=null;rightsListGeneration++}
function invalidateRightsSnapshot(){rightsListGeneration++;rightsAvailable=false;rightsRevision='';rightsCursor='';rightsListedRules=[];el('rights').replaceChildren();el('rights-unruled').replaceChildren();el('rights-heading').title='';rightsRevisionDetails('');el('rights-next').disabled=true;el('rights-next').hidden=true;invalidateDefaultsSnapshot();invalidateRightsDrafts('',true)}
async function followRights(){
 if(rightsClosed||typeof AbortController==='undefined')return;
 stopRightsWatch();const run=new AbortController();rightsWatchRun=run;
 try{
  while(rightsWatchRun===run&&!run.signal.aborted){
   try{
    const revision=rightsRevision,query=new URLSearchParams({revision,after:rightsObserveAfter});
    const o=await call('/rights/observe?'+query,undefined,run.signal);
    if(rightsWatchRun!==run||run.signal.aborted)return;
    if(o.Outcome==='changed'||o.Outcome==='unchanged'){
     rightsObserveAfter=o.Cursor;
     if(o.Outcome==='changed'||!rightsAvailable){
      invalidateRightsDrafts(o.Revision,o.Outcome==='changed'&&o.Revision===revision);
      rightsCursor='';
      await rightsList(undefined,true,run.signal);
     }
    }else{
     invalidateRightsSnapshot();
     show('rights-status',rightsStatus(o.Outcome));await rightsRetryPause(run.signal);
    }
   }catch(err){
    if(run.signal.aborted||rightsWatchRun!==run)return;
    invalidateRightsSnapshot();
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
function ruleText(r){return (r.Permit?'allow ':'block ')+rulePhrase(rightsActionPlainCache,r.Action)+' on '+rightsTargetLabel(r.Resource)+' for '+rightsAppName(r.Subject.Program)+' — '+accountLabel(r.Subject.Account)+'.'}
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
async function rightsEdit(e,name,removed){
 const revision=name?beginRightsDraftEdit(name):rightsAvailable?rightsRevision:'';if(!revision)return;
 const operation=e.edit==='set'?(name==='grant'?rightsGrantOperation():'Update'):'Remove';
 const originRule=e.bundle_origin_id&&rightsListedRules.find(r=>r.Subject.Account===e.account&&r.Subject.Program===e.program&&r.Selector?.Origin?.ID===e.bundle_origin_id);
 const confirmation=JSON.parse(JSON.stringify({bundle:e.bundle_ref&&rightsBundles.find(b=>b.BundleRef===e.bundle_ref),origin:originRule?.Selector?.Origin,originSubject:originRule?.Subject}));
 try{
  const r=await call('/rights',{...e,revision});
  if(rightsClosed)return;
  if(r.Outcome==='applied'){
   resetRightsDraft(name);const values=name==='grant'?rightsDraftValues():null;rightsCursor='';el('rights-capability-filter').value='';
   const selector=e.resource_profile||e.bundle_ref||e.bundle_origin_id;
   const listed=await rightsList({account:e.account,program:e.program,...(selector?{selector:true}:{action:e.action,resource:e.resource})});
   if(values){restoreRightsDraftValues(values);updateRightsDraftControls()}
   if(!rightsReadbackMatches(listed,r,e,confirmation)){show('rights-status','The service accepted the change, but its saved state could not be confirmed. Check Saved permissions before trying again.');return}
   const line=operation==='Remove'?(removed||'Permission removed. Saved permissions refreshed.'):(operation==='Add'?'Permission added':'Permission updated')+' for '+rightsAppName(e.program)+'. Saved permissions refreshed.';
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
// rulePhrase is the same phrase inside a sentence, as the table words it
// ("Allow Notes to read stored content"), never capitalized mid-sentence.
function rulePhrase(map,action){const phrase=map&&map[action];return phrase||oaActionFallback(action)}
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
function selectorTargetWords(s){return profilePhrase(s.Permission.ResourceProfile)==='saved secrets'?selectorScopeLabel(s.Scope,s.Name):s.Scope==='all'?'All selected targets':'One selected target'}
function appendSelectorDetails(box,r){
 if(!r.Selector)return;
 const s=r.Selector,details=document.createElement('details'),summary=document.createElement('summary'),text=document.createElement('div');
 rightsSummary(summary,'Scope and origin');
 text.textContent='Target type: '+profilePhrase(s.Permission.ResourceProfile)+'\nApplies to: '+selectorTargetWords(s)+'\nEditing: '+(s.Editable?'This target can be edited here.':'View only; this target cannot be edited here.');
 let exact='Action: '+s.Permission.Action+'\nProfile: '+s.Permission.ResourceProfile+'\nScope: '+s.Scope+'\nTarget: '+s.Target+'\nEditing: '+(s.Editable?'owning target codec available':'view-only: profile decoder unavailable');
 if(s.Origin){
  const origin=s.Origin;
  text.textContent+='\nOriginal permission set: '+origin.Remaining+' of '+origin.Original+' permissions remain ('+origin.State+').\nOriginal permissions:\n'+origin.Members.map(m=>selectorPermissionLabel(m.Permission)+' — '+selectorTargetWords(m)).join('\n');
  exact+='\nBundle: '+origin.BundleRef+'\nDefinition digest: '+origin.Digest+'\nOriginal grant: '+origin.ID+'\nCurrent original members: '+origin.Remaining+'/'+origin.Original+' ('+origin.State+')\nOriginal expansion:\n'+origin.Members.map(m=>m.Permission.Action+' | '+m.Permission.ResourceProfile+' | '+m.Scope+' | '+m.Target).join('\n');
  const button=document.createElement('button');button.textContent='Remove remaining bundle rules';button.onclick=()=>editBundleOrigin(r);if(s.Editable&&origin.BundleRef==='abstraction.credentials/full-control@1'&&origin.Members.every(m=>m.Editable))box.append(button)
 }
 details.className='oa-expander';details.append(summary,rightsBody(text),rightsExpander('Technical details',exact));box.append(details)
}
function editBundleOrigin(r){editListedRule(r,'remove');rightsOriginDraft=r.Selector.Origin;el('rights-origin-id').value=rightsOriginDraft.ID;rightsSummary(el('rights-editor-heading'),'Review remaining bundle rules for '+rightsAppName(r.Subject.Program));el('rights-broad-confirm').checked=false;rightsGrantPreview()}
function rightsGrantPayload(){const subject={account:el('rights-account').value,program:el('rights-program').value},capability=chosenSelectorCapability();if(el('rights-origin-id').value){if(!el('rights-broad-confirm').checked){show('rights-status','Review the original bundle and confirm removing its remaining rules.');return null}return {edit:'remove_bundle',...subject,bundle_origin_id:el('rights-origin-id').value}}const base=capability?{...subject,scope:el('rights-scope').value,...(el('rights-scope').value==='one'?{credential_name:el('rights-name').value}:{})}: {...subject,action:el('rights-action').value,resource:el('rights-resource').value};if(capability){if(!rightsSelectorAvailable||!(el('rights-permit').value==='remove'?capability.CodecAvailable:capability.Editable)||!(el('rights-permit').value==='remove'?['one','all']:capability.SupportedSelectors).includes(base.scope)){show('rights-status','This permission profile is view-only or no longer available.');return null}if((base.scope==='all'||capability.BundleRef)&&!el('rights-broad-confirm').checked){show('rights-status','Review the complete scope and effects, then confirm before saving.');return null}if(capability.BundleRef)base.bundle_ref=capability.BundleRef;else{base.action=capability.Definition.Action;base.resource_profile=capability.Definition.ResourceProfile}}else if(/^(selector|bundle):/.test(base.action)){show('rights-status','This permission profile is no longer available.');return null}if(el('rights-permit').value==='remove')return {edit:'revoke',...base};const lifetime=el('rights-ttl').value;if(!Object.hasOwn(rightsLifetimeLabels,lifetime)){show('rights-status','Choose a lifetime before saving this permission.');return null}const edit={edit:'set',...base,permit:el('rights-permit').value==='permit'};if(Number(lifetime))edit.ttl_ms=Number(lifetime);if(el('rights-why').value)edit.why=el('rights-why').value;return edit}
// permissionLabel is the one source of a permission's words: a definition's
// or bundle's label when it reads as a phrase, else the action's plain
// phrase (rulePlain). An id-shaped label ("abstraction.<layer>/…") never
// becomes page text; the raw value stays on the option's title.
function permissionLabel(label,action){return label&&!/^abstraction\.[a-z0-9_-]+\//.test(label)?label:rulePlain(rightsActionPlainCache,action)}
// profilePhrase names a resource profile's target kind in words.
function profilePhrase(profile){return profile==='abstraction.rights/legacy-exact-string@1'?'exact resource':profile==='abstraction.credentials/named-use@2'||profile==='abstraction.credentials/named-secret@1'?'saved secrets':'unrecognized target type'}
function selectorPermissionLabel(key){const d=rightsDefinitions.find(d=>d.Definition.Action===key.Action&&d.Definition.ResourceProfile===key.ResourceProfile);return permissionLabel(d&&d.Label,key.Action)}
function selectorGrantPreview(capability){
 const out=el('rights-preview'),origin=el('rights-origin-id').value?rightsOriginDraft:null,scope=origin?origin.Members[0]?.Scope:el('rights-scope').value,name=origin?origin.Members[0]?.Name:el('rights-name').value,remove=!!origin||el('rights-permit').value==='remove';
 const keys=origin?(origin.LinkedMembers||[]).map(m=>m.Permission):capability.BundleRef?capability.Members:[{Action:capability.Definition.Action,ResourceProfile:capability.Definition.ResourceProfile}];
 const words=document.createElement('p');words.textContent=(remove?'Remove ':el('rights-permit').value==='deny'?'Block ':'Allow ')+(origin?'the remaining rules of this set of permissions':(capability.Label&&!/^abstraction\.[a-z0-9_-]+\//.test(capability.Label)?capability.Label:rulePhrase(rightsActionPlainCache,capability.BundleRef||capability.Definition.Action)))+' for '+(el('rights-program').value?rightsAppName(el('rights-program').value):'the selected application')+' on '+selectorTargetWords({Permission:keys[0]||{},Scope:scope,Name:name})+'.'+(remove?' Other saved permissions remain unchanged.':' Lifetime: '+(rightsLifetimeWords[rightsLifetimeLabels[el('rights-ttl').value]]||'choose a lifetime')+'.');out.append(words);
 if(scope==='all'){const warning=document.createElement('p');warning.textContent=(keys.every(k=>profilePhrase(k.ResourceProfile)==='saved secrets')?'Broad scope: this affects current and future saved application secrets in this account. Runtime-managed local keys are excluded. ':'Broad scope: this applies to all selected targets. ')+(capability?.AllEffect||'');out.append(warning)}
 // The long explanations sit behind Learn more; the caption stays one line.
 const more=[];if(capability?.BundleRef||origin){more.push('Full control is a fixed set of six saved-secret operations: Use, read value, create, rotate, revoke and named metadata inspection. It excludes secret enumeration, audit, permission administration, runtime-managed local keys and future operations. A later bundle version requires another grant.');if(capability?.Disclosure)para(out,capability.Disclosure)}
 if(keys.some(k=>k.Action==='abstraction.credentials/secret.read'))more.push('Read value returns the secret bytes to this application. It can copy or use the value elsewhere. Website and consumer restrictions on Use do not limit bytes after they are returned.');
 if(more.length){const learn=document.createElement('details'),heading=document.createElement('summary'),body=document.createElement('div');learn.className='oa-expander';body.className='oa-expander-body';rightsSummary(heading,'Learn more');for(const text of more)para(body,text);learn.append(heading,body);out.append(learn)}
 if(origin)para(out,'Current original grants: '+origin.Remaining+' of '+origin.Original+' ('+origin.State+'). Remove these '+keys.length+' currently linked rules, including retained expired rules:');
 const members=document.createElement('ul');for(const key of keys){const item=document.createElement('li');item.textContent=selectorPermissionLabel(key)+' — '+selectorTargetWords({Permission:key,Scope:scope,Name:name});members.append(item)}out.append(members);
 if(origin){const original=document.createElement('details'),heading=document.createElement('summary'),list=document.createElement('div');rightsSummary(heading,'Immutable original bundle expansion');list.textContent=origin.Members.map(m=>selectorPermissionLabel(m.Permission)+' — '+profilePhrase(m.Permission.ResourceProfile)+' — '+selectorTargetWords(m)).join('\n');original.className='oa-expander';original.append(heading,rightsBody(list));out.append(original)}
 const details=oaTechnical(document.createElement('details')),summary=document.createElement('summary'),text=document.createElement('div');rightsSummary(summary,'Technical details');text.textContent='Account: '+el('rights-account').value+'\nProgram: '+el('rights-program').value+'\nScope: '+scope+'\nTarget: '+selectorScopeLabel(scope,name)+'\nChoice: '+(remove?'Remove':el('rights-permit').value==='deny'?'Block':'Allow')+'\nLifetime: '+(remove?'existing rules removed':rightsLifetimeLabels[el('rights-ttl').value]||'choose a lifetime')+'\nReason: '+(remove?'none':el('rights-why').value)+'\nRevision: '+(rightsDrafts.grant.dirty?rightsDrafts.grant.revision:rightsRevision)+'\n'+keys.map(k=>'Action: '+k.Action+'\nProfile: '+k.ResourceProfile).join('\n');if(capability?.BundleRef||origin)text.textContent+='\nBundle: '+(origin?origin.BundleRef:capability.BundleRef)+'\nDefinition digest: '+(origin?origin.Digest:capability.DefinitionDigest)+(origin?'\nOriginal grant: '+origin.ID+'\nCurrent original members: '+origin.Remaining+'/'+origin.Original+' ('+origin.State+')\nOriginal expansion:\n'+origin.Members.map(m=>m.Permission.Action+' | '+m.Permission.ResourceProfile+' | '+m.Scope+' | '+m.Target).join('\n'):'');else text.textContent+='\nOwner contract: '+capability.Definition.OwnerContract+'\nCollection: '+capability.Collection+'\nTarget type: '+capability.RequestType+'\nReceiving operations: '+(capability.Definition.Operations||[]).join(', ')+'\nEnforced by: '+(capability.Definition.EnforcedBy||[]).join(', ')+'\nLifecycle: '+(capability.Definition.Lifecycle||'')+'\nDisclosure: '+(capability.Definition.Disclosure||'')+'\nSupported selectors: '+capability.SupportedSelectors.join(', ');details.className='oa-expander';details.append(summary,rightsBody(text));out.append(details);
}
// The lifetime as it reads inside a sentence.
var rightsLifetimeWords={'Until removed':'until removed','Five minutes':'five minutes','One hour':'one hour','One day':'one day','Choose a lifetime':'choose a lifetime'};
function rightsGrantOperation(){if(el('rights-permit').value==='remove'||el('rights-origin-id').value)return 'Remove';const capability=chosenSelectorCapability();return rightsListedRules.some(r=>r.Subject.Account===el('rights-account').value&&r.Subject.Program===el('rights-program').value&&(capability?r.Selector&&selectorActionValue(r.Selector.Permission)===el('rights-action').value&&r.Selector.Scope===el('rights-scope').value&&(r.Selector.Name||'')===(el('rights-scope').value==='one'?el('rights-name').value:''):!r.Selector&&r.Action===el('rights-action').value&&r.Resource===el('rights-resource').value))?'Update':'Add'}
function rightsGrantPreview(){el('rights-submit').textContent=rightsGrantOperation()+' permission';updateSelectorControls();const out=el('rights-preview');out.replaceChildren();const capability=chosenSelectorCapability();if(capability||el('rights-origin-id').value){selectorGrantPreview(capability);return}const choice=el('rights-permit').value,remove=choice==='remove',program=el('rights-program').value,resource=el('rights-resource').value,action=el('rights-action').value,lifetime=rightsLifetimeLabels[el('rights-ttl').value]||'Choose a lifetime';el('rights-ttl').disabled=remove;el('rights-why').disabled=remove;const words=document.createElement('p');words.textContent=remove?'Remove the saved exact rule for '+(program?rightsAppName(program):'the selected application')+' on '+(resource||'the selected resource')+'. Other permissions remain unchanged.':(choice==='deny'?'Block ':'Allow ')+(program?rightsAppName(program):'the selected application')+' to '+(action?rulePhrase(rightsActionPlainCache,action):'the selected action')+' on '+(resource||'the selected resource')+'. Lifetime: '+(rightsLifetimeWords[lifetime]||'choose a lifetime')+'.';out.append(words);const details=oaTechnical(document.createElement('details')),summary=document.createElement('summary'),text=document.createElement('div');rightsSummary(summary,'Technical details');text.textContent='Account: '+el('rights-account').value+'\nProgram: '+program+'\nAction: '+action+'\nExact resource: '+resource+'\nChoice: '+(remove?'Remove rule':choice==='deny'?'Block':'Allow')+(remove?'':'\nLifetime: '+lifetime+'\nReason: '+el('rights-why').value)+'\nRevision: '+(rightsDrafts.grant.dirty?rightsDrafts.grant.revision:rightsRevision);details.className='oa-expander';details.append(summary,rightsBody(text));out.append(details);if(resource==='*'){const note=document.createElement('p');note.textContent='This is an exact resource named *. It does not select all resources.';out.append(note)}}
function editListedRule(r,choice){if(!rightsAvailable||(r.Selector&&(!r.Selector.Editable||(choice!=='remove'&&!r.Selector.CanGrant))))return;resetRightsDraft('grant');rightsProgramsSeen.add(r.Subject.Program);rightsProgramChooser.rebuild();el('rights-program-select').value=r.Subject.Program;el('rights-program-select').onchange();el('rights-program').value=r.Subject.Program;el('rights-account-select').value=exploreSelf&&r.Subject.Account===exploreSelf.account?r.Subject.Account:'__other__';el('rights-account-select').onchange();el('rights-account').value=r.Subject.Account;el('rights-action').value=r.Selector?selectorActionValue(r.Selector.Permission):r.Action;if(r.Selector){el('rights-scope').value=r.Selector.Scope;el('rights-name').value=r.Selector.Name||'';loadRightsSecretNames()}rememberResource(r.Action,r.Resource);rebuildResourceChooser();el('rights-resource-select').value=r.Resource;resourceChooserSync();el('rights-permit').value=choice;el('rights-ttl').value=r.Expires&&!r.Expired?'':'0';el('rights-why').value=r.Why||'';rightsSummary(el('rights-editor-heading'),'Review permission for '+rightsAppName(r.Subject.Program));markRightsDraft('grant');openRightsEditor()}
function openRightsEditor(){const editor=el('rights-editor');editor.open=true;const field=el('rights-program-select');if(editor.scrollIntoView)editor.scrollIntoView({behavior:'smooth',block:'nearest'});if(field.focus)field.focus()}
// Opening the editor with no draft names it again (a reviewed draft renamed it).
el('rights-editor').ontoggle=()=>{if(el('rights-editor').open&&!rightsDrafts.grant.dirty)rightsSummary(el('rights-editor-heading'),'Permission editor')};
// Saved permissions read by app: the app's registered name (its program's
// file name when it has none) as a heading, then one row per permission
// with its state, Edit and Remove. One closed Technical details per app keeps
// the exact subject, rule, scope and setter inspectable. A
// resource that names an app by its kind and name reads as the app's
// registered title; one that names a model server or a saved key reads as
// that name in words.
let rightsAppTitles={};
const rightsAppTitlesRead=(typeof oaRegisteredApps==='function'?oaRegisteredApps():Promise.resolve([])).then(apps=>{for(const a of apps||[])if(a.name)rightsAppTitles[a.name]=a.title||a.name},()=>{});
function rightsWords(id){return capitalize(String(id).replace(/[-_.]+/g,' ').trim())||'Unnamed'}
function rightsTargetLabel(resource){if(/^sha256:/i.test(resource))return 'Stored content';const m=/^(app|host|credential):(.+)$/.exec(resource);if(!m)return oaResourceLabel(resource);if(m[1]==='app')return rightsAppTitles[m[2]]||'An app';if(m[1]==='host')return oaResourceLabel(m[2]);return rightsWords(m[2])+' key'}
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
   // An exact rule reads in the app page's words (oaRuleWords): the same
   // label and lifetime on both pages, and no target for a device hold,
   // whose label names the device.
   const words=oaRuleWords(rulePlain(rightsActionPlainCache,r.Action),r),lifetime=words.caption.split(' · ').pop();
   label.textContent=r.Sentence||(r.Selector?selectorPermissionLabel(r.Selector.Permission):words.label);
   caption.textContent=r.Sentence?lifetime:r.Selector?selectorTargetWords(r.Selector)+' · '+lifetime:[r.Action==='abstraction.resource/hold'?'':capitalize(rightsTargetLabel(r.Resource)),lifetime].filter(Boolean).join(' · ');
   state.textContent=(r.Expired?'Expired · ':'')+(r.Permit?'Allowed':'Blocked');
   edit.textContent='Edit';edit.viewOnly=!!r.Selector&&!r.Selector.Editable;edit.disabled=edit.viewOnly;const said=document.createElement('p');said.className='oa-status';said.setAttribute('role','status');said.setAttribute('aria-live','polite');said.setAttribute('data-state','idle');
   edit.onclick=()=>{editListedRule(r,r.Selector&&!r.Selector.CanGrant?'remove':r.Permit?'permit':'deny');if(el('rights-editor').open){said.textContent='Review the change in the permission editor below.';said.setAttribute('data-state','ok')}};
   const remove=document.createElement('button');remove.textContent=r.Sentence?'Remove':'Remove…';remove.viewOnly=!!r.Selector&&!r.Selector.Editable;remove.disabled=remove.viewOnly;row.remove=remove;
   // Remove asks for a second click within ten seconds, as Credentials does.
   remove.onclick=()=>{if(!remove.armed||Date.now()-remove.armed>10000){remove.armed=Date.now();said.textContent='Select Remove again within ten seconds to remove this permission.';said.setAttribute('data-state','idle');return}
    remove.armed=0;
    if(r.Selector){editListedRule(r,'remove');said.textContent='Review the removal in the permission editor below.';said.setAttribute('data-state','ok');return}
    return rightsEdit({edit:'revoke',account:r.Subject.Account,program:r.Subject.Program,action:r.Action,resource:r.Resource},undefined,r.Removed)};
   text.append(label,caption);control.append(state,edit,remove);row.append(text,control,said);box.append(row)}
}
// appendRightsTable draws one group per app: its name as the heading, its
// rows, then the rules OpenAbstractions set up for it in one closed expander,
// with their count once the read is complete.
// OpenAbstractions' own programs (the Panel and the runtime) each have their
// own group under their product name (program_titles.go); a registered app
// never reads as one (reservedTitle). The rules one of them set for itself or
// the other fold under "Set up by OpenAbstractions", so the two identical
// operator rule sets never read as two lists of rows.
const rightsProductNames=['OpenAbstractions','OpenAbstractions Panel'];
function rightsIsOwnProgram(program){return rightsProductNames.includes(rightsNames[program])}
function rightsSetUp(own,r){return r.Permit&&own&&!!r.SetBy&&rightsIsOwnProgram(r.SetBy.Program)}
// Each app's group is an oa-group, its rows and expanders its direct
// children, so every skin draws it as a grouped list.
// OpenAbstractions' own programs share one group, listed after the apps
// (architect ruling 2026-11-09). The exact rules go to the one Technical
// details under Advanced (rightsRevisionDetails).
var rightsListedJSON=[];
function appendRightsTable(out,rules,done=true){if(!rules.length)return;const groups=new Map();for(const r of rules){const own=rightsIsOwnProgram(r.Subject.Program),k=JSON.stringify([r.Subject.Account,own?'':r.Subject.Program]);if(!groups.has(k))groups.set(k,{subject:r.Subject,own,rules:[]});groups.get(k).rules.push(r)}
 const ordered=[...groups.values()].filter(g=>!g.own).concat([...groups.values()].filter(g=>g.own));
 for(const g of ordered){const box=document.createElement('div'),title=document.createElement('h3');box.className='oa-group';box.subject=g.subject;title.textContent=(g.own?'OpenAbstractions':rightsAppName(g.subject.Program))+(exploreSelf&&g.subject.Account!==exploreSelf.account?' (another account)':'');box.append(title);
  // The Panel and the runtime hold the same set-up rules: each shows once.
  const setUpAll=g.rules.filter(r=>rightsSetUp(g.own,r)),seen=new Set(),setUp=setUpAll.filter(r=>{const k=JSON.stringify([r.Action,r.Resource,r.Permit,r.Expires||0,r.Selector||null]);if(seen.has(k))return false;seen.add(k);return true});appendRightsRows(box,g.rules.filter(r=>!setUpAll.includes(r)));
  if(setUp.length){const entry=document.createElement('details'),summary=document.createElement('summary'),body=document.createElement('div');entry.className='oa-expander';entry.installed=true;body.className='oa-expander-body';rightsSummary(summary,installedEntryName(setUp.length,done));entry.append(summary,body);appendRightsRows(body,setUp);box.append(entry)}
  for(const r of g.rules)rightsListedJSON.push(JSON.stringify({rule:r,description:actionDescriptionText(r.Action)||undefined},null,2));
  out.append(box)}}
// Why is an operator-entered reason, including the runtime's seed reason.
// It supplies no installation provenance; every row shows its actual setter.
// While later pages are still read the count is left out, so it never jumps.
function installedEntryName(count,done=true){return 'Set up by OpenAbstractions'+(done?' ('+count+')':'')}
// showRightsList used to write "Rules revision: <8 hex>." as its own small
// muted line, dropping the inert "More rules remain."/"End of rules."
// wording entirely (the rights-next button beside it already carries that
// state). Even truncated, a revision is an identifier, and this Panel no
// longer shows one as visible text at all (task 2026-09-23, eighth
// first-time visitor, finding 2). The full value now lives only on the
// rules list heading's own title and in this section's Technical details.
function showRightsList(revision,empty){const out=el('rights-status');out.replaceChildren();if(empty)out.append(document.createTextNode('No saved permissions yet.'));if(rightsSelectorStatus==='unsupported')para(out,'This service provides legacy exact permission management. Typed scopes and saved typed rules are unavailable in this view.');el('rights-heading').title=revision||'';}
// rightsRevisionDetails fills the list's Technical details, the Saved
// permissions section's last disclosure, after its groups and rows.
function rightsRevisionDetails(revision){const details=el('rights-revision'),body=el('rights-revision-body');details.hidden=!revision;body.textContent=revision?'Revision: '+revision:'';rightsBody(body);if(revision&&rightsListedJSON.length){const pre=document.createElement('pre');pre.textContent=rightsListedJSON.join('\n\n');body.append(pre)}}
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
 if(rightsClosed)return;
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
 await ensureSelf(false);await rightsAppTitlesRead;
 if(rightsClosed||generation!==rightsListGeneration)return false;
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
 const selectedCapability=el('rights-capability-filter').value;if(selectedCapability){rules=rules.filter(r=>(r.Selector?selectorActionValue(r.Selector.Permission):r.Action)===selectedCapability);if(!rules.length)emptyLine(el('rights'),done?'No permissions for this capability.':'No permissions for this capability on these pages; choose Show more.')}
 if(rightsAppFilter)el('rights-filter-label').textContent='Permissions for '+rightsAppName(rightsAppFilter.program);rightsListedJSON=[];appendRightsTable(el('rights'),rules,done&&final);rightsRevisionDetails(rightsRevision);
 const ruled=new Set(rules.map(r=>r.Action));const unruled=(catalog||[]).filter(a=>!ruled.has(a));
 el('rights-unruled').replaceChildren();if(unruled.length&&!rightsAppFilter){const section=el('rights-unruled');heading(section,'No saved permission names these yet');const list=document.createElement('ul');for(const a of unruled){const li=document.createElement('li');li.textContent=rulePlain(rightsActionPlainCache,a);li.title=a;appendActionDescription(li,a);list.append(li)}section.append(list)}
 if(saved)restoreRightsDraftValues(saved);updateRightsDraftControls();
 if(final)defaultsList();
 return true};
 try{
  for(;;){
   const firstPage=pages===0&&restarting;
   const p=await call('/rights?cursor='+encodeURIComponent(cursor),undefined,signal);
   if(generation!==rightsListGeneration)return;
   if(p.Outcome!=='page'){invalidateRightsSnapshot();show('rights-status',rightsStatus(p.Outcome));rightsFirstDrawn();return}
   if(!firstPage&&pageRevision&&p.Revision!==pageRevision){invalidateRightsSnapshot();show('rights-status',rightsStatus('gap'));rightsFirstDrawn();return}
   if(pages===0)catalog=p.Catalog;
   if(firstPage){definitions=p.Definitions||[];bundles=p.Bundles||[];selectorAvailable=!!p.SelectorAvailable;selectorStatus=p.SelectorStatus||''}plain=p.ActionPlain;descriptions=p.Descriptions||{};Object.assign(rightsNamesRead,p.Names||{});pageRevision=p.Revision;cursor=p.Next;
   rules=rules.concat(p.Rules);complete=p.Complete;
   pages++;
   if(complete||pages>=20)break;
   if(pages===1&&restarting){if(!await draw(false))return;rightsFirstDrawn()}
  }
 }catch(e){if(generation!==rightsListGeneration)return;invalidateRightsSnapshot();fail('rights-status',e);rightsFirstDrawn();return}
 const listed=rules.slice();if(await draw(true)){rightsFirstDrawn();return {rules:listed,complete,revision:pageRevision}}
}
el('rights-next').onclick=()=>rightsList();
// Messages between agents (rights_defaults.go Agents): one switch, "Ask me
// first", on while the agent-messages default is off. It is read with the
// defaults and at their revision. A click during a save is written after it;
// one conflict rereads the revision; a refused or failed write turns the
// switch back and says why.
// Until a read stands behind it the switch is hidden, so no On or Off shows
// that was never read. The first shown state, or the first reason it cannot
// show, settles defaultsFirstRead, which the page's readiness waits for.
const agentAsk={saved:true,wanted:true,saving:null,shown:false};
let defaultsFirstDone=()=>{};var defaultsFirstRead=new Promise(resolve=>{defaultsFirstDone=resolve});
function agentAskSay(state,sentence){oaSetStatus('agent-messages-status',state,sentence)}
function agentAskShow(v){const s=el('agent-messages-ask');
 if(v.Outcome!=='page'||!v.Agents){agentAskHold(v.Outcome==='page'||v.Outcome==='unsupported'?'Update OpenAbstractions to change this.':defaultStatus(v.Outcome));return}
 agentAsk.saved=!v.Agents.On;if(!agentAsk.saving){agentAsk.wanted=agentAsk.saved;s.checked=agentAsk.saved}s.disabled=false;s.hidden=false;if(!agentAsk.shown){agentAsk.shown=true;agentAskSay('idle','')}defaultsFirstDone()}
// agentAskHold hides the switch while no current defaults read stands behind
// it and says why.
function agentAskHold(sentence){agentAsk.shown=false;const s=el('agent-messages-ask');s.disabled=true;s.hidden=true;agentAskSay('error',sentence);defaultsFirstDone()}
function agentAskRevert(sentence){agentAsk.wanted=agentAsk.saved;el('agent-messages-ask').checked=agentAsk.saved;agentAskSay('error',sentence)}
async function agentAskWrites(){let retried=false;while(agentAsk.wanted!==agentAsk.saved){const ask=agentAsk.wanted;if(!defaultsRevision){agentAskRevert(defaultStatus('unavailable'));return}let r;
 try{r=await call('/rights/defaults',{revision:defaultsRevision,name:'agent-messages',on:!ask})}catch(e){agentAskRevert(oaPlain(e&&e.message||String(e)).line);return}
 if(r.Outcome==='applied'){agentAsk.saved=ask;retried=false;if(r.Revision)defaultsRevision=r.Revision;if(agentAsk.wanted===ask)agentAskSay('ok',ask?'Your agents now ask you before messaging each other.':'Your agents now message each other without asking you.');continue}
 if(r.Outcome==='conflict'&&!retried){retried=true;try{const v=await call('/rights/defaults');if(v.Outcome==='page'&&v.Agents){defaultsRevision=v.Revision;agentAsk.saved=!v.Agents.On;continue}}catch(e){}}
 agentAskRevert(defaultStatus(r.Outcome));return}}
async function agentAskSave(){if(agentAsk.saving)return agentAsk.saving;const run=agentAskWrites();agentAsk.saving=run;try{await run}finally{agentAsk.saving=null}}
el('agent-messages-ask').onchange=()=>{agentAsk.wanted=!!el('agent-messages-ask').checked;return agentAskSave()};
// Full tool use per agent pair (rights_message_tools.go, S5 ruling
// 2026-11-22): off by default, a turn another agent's message started is
// reply-only. Read with the defaults; a switch writes one rule at the
// revision it was read at, then the list reads again.
let agentToolsRevision='',agentToolsShown='';const agentToolsRows={last:new Map(),next:new Map()};
function agentToolsItem(){const s=document.createElement('input');s.type='checkbox';s.className='switch';const box=rightsItem({label:'',controls:[s]});box.toggle=s;s.onchange=()=>agentToolsSave(box,s.checked);return box}
async function messageToolsList(){if(rightsClosed)return;try{const v=await call('/rights/message-tools');if(rightsClosed)return;const out=el('agent-tools-list');if(v.outcome!=='page'){agentToolsRevision='';out.replaceChildren();oaSetStatus('agent-tools-status','error',defaultStatus(v.outcome));return}agentToolsRevision=v.revision||'';const shown=JSON.stringify(v.pairs);if(shown===agentToolsShown)return;agentToolsShown=shown;out.replaceChildren();
 for(const pair of v.pairs){const box=rightsKeep(agentToolsRows,pair.senderApp+'|'+pair.receiverApp,agentToolsItem);box.pair=pair;box.row.querySelector('.oa-row-label').textContent=pair.label;box.toggle.setAttribute('aria-label',pair.label);if(!box.saving)box.toggle.checked=!!pair.on;out.append(box)}rightsKept(agentToolsRows)}catch(e){if(!rightsClosed)fail('agent-tools-status',e)}}
async function agentToolsSave(box,on){if(box.saving)return;box.saving=true;box.toggle.disabled=true;try{const r=await call('/rights/message-tools',{revision:agentToolsRevision,sender:box.pair.senderApp,receiver:box.pair.receiverApp,on});if(r.outcome==='applied'){if(r.revision)agentToolsRevision=r.revision;oaSetStatus('agent-tools-status','ok',r.message||'')}else{box.toggle.checked=!on;oaSetStatus('agent-tools-status','error',r.message||defaultStatus(r.outcome))}}catch(e){box.toggle.checked=!on;fail('agent-tools-status',e)}finally{box.saving=false;box.toggle.disabled=false;agentToolsShown='';await messageToolsList()}}
// Defaults for all apps (rights_defaults.go): one switch per account-wide
// default, read after each complete permissions read, so a change made
// elsewhere shows through the same watch. A switch stays usable while it
// saves and shows the person's latest choice: a click during a save is
// written after it, at the revision that save returned. A refused or failed
// write turns the switch back to the saved state and its sentence says why.
// defaultsReadMark drops a defaults reply that a newer read or a failed
// policy read overtook; restarting the watch leaves a current read standing.
let defaultsRevision='',defaultsShown='',defaultsReadMark=0;const defaultsRows={rows:new Map(),order:[],drawn:false};
const defaultsText={unsupported:'This version of OpenAbstractions has no defaults for all apps. Update OpenAbstractions to change them.'};
function defaultStatus(o){return defaultsText[o]||rightsStatus(o)}
function invalidateDefaultsSnapshot(){defaultsReadMark++;defaultsRevision='';defaultsShown='';el('defaults-list').replaceChildren();defaultsRows.rows.clear();defaultsRows.order=[];defaultsRows.drawn=false;agentAskHold('This setting is unavailable right now.')}
function defaultCaption(d){return [d.Caption||'',d.SetBy?(d.On?'Turned on':'Turned off')+' by '+rightsAppName(d.SetBy.Program)+(d.SetAt?', '+new Date(d.SetAt).toLocaleString():'')+'.':''].filter(Boolean).join(' ')}
async function defaultsList(){if(rightsClosed)return;void messageToolsList();const readMark=++defaultsReadMark;try{const v=await call('/rights/defaults');if(rightsClosed||readMark!==defaultsReadMark||!rightsAvailable)return;if(v.Outcome!=='page'){invalidateDefaultsSnapshot();agentAskShow(v);show('defaults-status',defaultStatus(v.Outcome));return}defaultsRevision=v.Revision;agentAskShow(v);const shown=JSON.stringify(v.Defaults);if(shown===defaultsShown)return;defaultsShown=shown;rightsRows(el('defaults-list'),defaultsRows,v.Defaults,d=>d.Name,defaultItem,defaultFill,'No defaults.')}catch(e){if(rightsClosed||readMark!==defaultsReadMark||!rightsAvailable)return;invalidateDefaultsSnapshot();fail('defaults-status',e)}}
// defaultFill shows a default's saved state; a switch that is saving keeps
// the person's choice until its saves finish.
function defaultFill(box,d){box.d=d;box.saved=!!d.On;rightsFill(box,d.Label,defaultCaption(d));if(!box.saving){box.wanted=box.saved;box.toggle.checked=box.saved}}
function defaultItem(d){const toggle=document.createElement('input');toggle.type='checkbox';toggle.className='switch';if(toggle.setAttribute)toggle.setAttribute('aria-label',d.Label);const box=rightsItem({label:d.Label,caption:defaultCaption(d),controls:[toggle]});box.toggle=toggle;toggle.onchange=()=>{box.wanted=!!toggle.checked;return defaultSave(box)};return box}
// defaultSave writes until the saved state is the person's latest choice.
// One conflict rereads the revision and tries again.
async function defaultSave(box){if(box.saving)return box.saving;const run=defaultWrites(box);box.saving=run;try{await run}finally{box.saving=null;defaultsShown='';await defaultsList()}}
async function defaultWrites(box){let retried=false;while(box.wanted!==box.saved){const on=box.wanted,d=box.d;if(!defaultsRevision){defaultRevert(box,defaultStatus('unavailable'));return}let r;try{r=await call('/rights/defaults',{revision:defaultsRevision,name:d.Name,on})}catch(e){defaultRevert(box);fail('defaults-status',e);return}
 if(r.Outcome==='applied'){box.saved=on;retried=false;if(r.Revision)defaultsRevision=r.Revision;if(box.wanted===on)show('defaults-status',(on?'Every app may now ':'No app may ')+d.Phrase+(on?'.':' unless you allowed that app.'));continue}
 if(r.Outcome==='conflict'&&!retried){retried=true;try{const v=await call('/rights/defaults');if(v.Outcome==='page'){defaultsRevision=v.Revision;const now=(v.Defaults||[]).find(x=>x.Name===d.Name);if(now){box.d=now;box.saved=!!now.On}continue}}catch(e){}}
 defaultRevert(box,defaultStatus(r.Outcome));return}}
// defaultRevert shows the saved state again and, when given, why the change
// was not saved.
function defaultRevert(box,sentence){box.wanted=box.saved;box.toggle.checked=box.saved;if(sentence)show('defaults-status',sentence)}
el('rights-ttl').value='0';el('rights-scope').value='one';
// rightsFirstPage settles once Saved permissions first shows rows, or once
// its first read failed and said so: the page is ready either way.
let rightsFirstDrawn=()=>{};var rightsFirstPage=new Promise(resolve=>{rightsFirstDrawn=resolve});
var rightsListReady=rightsList();
var questionsReady=questions();
for(const[name,form]of[['grant','rights-grant']]){const edited=event=>{if(name==='grant'&&event?.target?.id&&event.target.id!=='rights-broad-confirm')el('rights-broad-confirm').checked=false;markRightsDraft(name)};el(form).addEventListener('input',edited);el(form).addEventListener('change',edited);el('rights-review-'+name).onclick=()=>reviewRightsDraft(name)}
if(typeof window.addEventListener==='function')window.addEventListener('pagehide',()=>{rightsClosed=true;stopRightsWatch();invalidateRightsSnapshot()});
var rightsWatchReady=rightsListReady.then(followRights);
const allowSeen=new Set();let allowAccount='';
function allowPrograms(subjects){for(const s of subjects){if(!allowAccount)allowAccount=s.Account;if(allowSeen.has(s.Program))continue;allowSeen.add(s.Program)}rightsProgramChooser.rebuild()}
// Answering access uses the existing enrolled native-app owner.
let assistantSnapshot=null;
const assistantChooser=oaProgramChooser('assistant-program-select','assistant-program',()=>assistantSnapshot?assistantSnapshot.candidates:[],program=>rightsNames[program]);
function assistantWarning(){el('assistant-confirm').checked=false;const box=el('assistant-warning');box.replaceChildren();const program=el('assistant-program').value;if(!assistantSnapshot||!program)return;const w=assistantSnapshot.warning,name=rightsAppName(program);heading(box,'Let '+name+' answer OpenAbstractions questions for you');para(box,name+' will answer routine questions from your apps. It may let an app:');const yes=document.createElement('ul');for(const words of w.answers){const li=document.createElement('li');li.textContent=words;yes.append(li)}box.append(yes);para(box,'These always come to you:');const no=document.createElement('ul');for(const words of w.staysWithYou){const li=document.createElement('li');li.textContent=words;no.append(li)}box.append(no);if(w.choices)para(box,w.choices);para(box,'Warning: '+name+' '+w.caution);para(box,w.ends);el('assistant-save').textContent='Let '+name+' answer'}
function assistantReady(){el('assistant-save').disabled=!(assistantSnapshot&&el('assistant-program').value&&el('assistant-confirm').checked)}
let assistantsChain=Promise.resolve(),assistantsShown='';
function loadAssistants(){const run=assistantsChain.then(assistantsPass);assistantsChain=run.catch(()=>{});return run}
function assistantRow(a){const cleanup=document.createElement('label'),check=document.createElement('input');check.type='checkbox';cleanup.append(check,' Also revoke permissions granted through this app');const stop=document.createElement('button');stop.textContent='Revoke answering access';const never=document.createElement('button');never.textContent="Stop and don't ask again";const box=rightsItem({label:'',caption:' ',controls:[stop,never]});if(cleanup.style)cleanup.style.gridColumn='1 / -1';box.row.append(cleanup);const revoke=async(dontAskAgain)=>{try{const r=await call('/rights/assistant',{action:'stop',program:box.program,revision:assistantSnapshot.revision,alsoRevokeGrants:check.checked,dontAskAgain});await loadAssistants();await loadAnswered();show('assistant-status',r.message)}catch(e){fail('assistant-status',e)}};stop.onclick=()=>revoke(false);never.onclick=()=>revoke(true);return box}
async function assistantsPass(){try{const p=await call('/rights/assistant');assistantSnapshot=p;rightsNamed(p.names);const list=el('assistant-list');if(p.outcome!=='page'){list.replaceChildren();assistantRows.drawn=false;assistantsShown='';assistantSnapshot=null;assistantWarning();assistantReady();show('assistant-status',rightsStatus(p.outcome));return}
 rightsRows(list,assistantRows,p.assistants,a=>a.program,assistantRow,(box,a)=>{box.program=a.program;/* The app's name is the label; what it does and until when is the caption, so the row's controls never squeeze a sentence (ruling 2026-11-09). */const at=new Date(a.expires),until=a.expires&&!Number.isNaN(at.getTime())?(at.getTime()<=Date.now()?'Expired ':'Expires ')+at.toLocaleString([],{year:'numeric',month:'numeric',day:'numeric',hour:'numeric',minute:'2-digit'}):'';rightsFill(box,a.name,'Answers routine questions for you'+(until?' · '+until:''),a.program)},'You answer questions yourself.');
 const shown=JSON.stringify([p.revision,p.candidates,p.warning]);if(shown===assistantsShown)return;assistantsShown=shown;
 assistantChooser.rebuild();if(p.candidates.length&&el('assistant-program-select').value==='__other__'){el('assistant-program-select').value=p.candidates[0];el('assistant-program-select').onchange()}assistantWarning();assistantReady()}catch(e){assistantSnapshot=null;assistantsShown='';assistantWarning();assistantReady();fail('assistant-status',e)}}
el('assistant-program-select').addEventListener('change',()=>{assistantWarning();assistantReady()});el('assistant-program').oninput=()=>{assistantWarning();assistantReady()};el('assistant-confirm').onchange=assistantReady;
el('assistant-form').onsubmit=async e=>{e.preventDefault();if(!assistantSnapshot||!el('assistant-confirm').checked)return;try{const r=await call('/rights/assistant',{action:'enroll',program:el('assistant-program').value,revision:assistantSnapshot.revision,confirm:true});el('assistant-confirm').checked=false;await loadAssistants();show('assistant-status',r.message)}catch(err){fail('assistant-status',err)}};
const answeredStateText={in_force:'',changed:' This permission changed since.',ended:' This permission has ended.',refused:''};
let answeredChain=Promise.resolve();
function loadAnswered(){const run=answeredChain.then(answeredPass);answeredChain=run.catch(()=>{});return run}
function answeredRow(){const undo=document.createElement('button');undo.textContent='Undo';const box=rightsItem({label:'',controls:[undo]});box.undo=undo;undo.onclick=async()=>{try{const r=await call('/rights/answered',{id:box.answerId});await loadAnswered();show('answered-status',r.message)}catch(e){fail('answered-status',e)}};return box}
async function answeredPass(){try{const p=await call('/rights/answered');const list=el('answered-list');if(p.outcome!=='page'){list.replaceChildren();answeredRows.drawn=false;show('answered-status',questionStatus(p.outcome));return}
 rightsRows(list,answeredRows,p.answers,a=>a.id,answeredRow,(box,a)=>{const when=a.answered?new Date(a.answered).toLocaleString():'';box.answerId=a.id;box.undo.hidden=a.state!=='in_force';rightsFill(box,String(a.sentence||'').replace(/[.]$/,''),a.kind==='choice'?a.answererName+' chose '+choiceDisplay(a.optionLabel||a.option)+(when?' on '+when:'')+'. The app may already have acted on it, so it cannot be undone here.':a.answererName+' answered '+(a.option==='allow'?'Allow':'Not now')+(when?' on '+when:'')+'.'+(answeredStateText[a.state]||''),a.answerer)},'No questions answered for you yet.')}catch(e){fail('answered-status',e)}}
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
var rightsArrived=Promise.all([rightsFirstPage,defaultsFirstRead,questionsReady,choicesReady,permissionsReady,assistantReadyPromise,answeredReady]).then(()=>{if(typeof oaReady==='function')oaReady()});
function rightsLive(){if(document.visibilityState==='hidden')return Promise.resolve();return Promise.all([questions(),choicesList(),permissionsList(),loadAnswered(),loadAssistants()])}
if(typeof document.addEventListener==='function'){const tick=()=>setTimeout(async()=>{await rightsLive();tick()},5000);tick();document.addEventListener('visibilitychange',rightsLive)}
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(rightsLive);
</script>`
