package main

// rightsPage is the Panel's Questions and permissions page: questions apps
// have asked, and the policy rules that decide what apps may do (task
// 2026-09-23, requirement 1: split out of the Status page, its own page at
// /rights). Its words follow research/vocabulary/DECISION.md: permissions,
// Allow and Block (D53); Dismiss for a question closed without an answer
// (D50), while the wire action stays "retire" until asks/operator@2.
const rightsPage = `<h1>Questions and permissions</h1><p class="oa-note">Questions waiting for your answer, and the rules that decide what apps may do.</p>
<section id="questions"><h2>Questions</h2><p>Questions apps have asked you. Allow lets the app do it; Dismiss removes the question without answering.</p><button id="questions-start">Refresh</button><button id="questions-next" disabled>Next page</button><pre id="questions-status"></pre><div id="questions-list"></div></section>
<section id="rights-section"><h2 id="rights-heading">Permissions</h2><p class="oa-note">Allow or block one action for one account and app on one resource.</p><p class="oa-note">An interpreter like python.exe counts as one app for every script it runs; allowing it allows all of them.</p><button id="rights-start">Refresh</button><button id="rights-next" disabled>Next page</button><pre id="rights-status"></pre><div id="rights"></div>
<form id="rights-grant"><select id="rights-account-select" aria-label="Account"></select><input id="rights-account" required placeholder="account" hidden><select id="rights-program-select" aria-label="Program"></select><input id="rights-program" required placeholder="Program path" hidden><label for="rights-action">Action known to this runtime (registered by an app, whether or not a rule governs it yet)</label><select id="rights-action" aria-label="Action"></select><label for="rights-resource-select">A registry name for downloads, a model server name for inference, a folder for storage.</label><select id="rights-resource-select" aria-label="Resource"></select><input id="rights-resource" required placeholder="resource" hidden><select id="rights-permit" aria-label="Allow or block"><option value="permit">Allow</option><option value="deny">Block</option></select><button>Save rule</button></form>
<form id="rights-allow"><select id="allow-for" aria-label="What to allow"><option value="downloads">Allow downloads for…</option><option value="inference">Allow inference for…</option></select><input id="allow-program" list="allow-programs" required placeholder="Program path"><datalist id="allow-programs"></datalist><input id="allow-account" placeholder="Account (optional)"><input id="allow-names" placeholder="Registries (for downloads) or model servers (for inference), separated by commas"><input id="allow-credentials" placeholder="Credential names, separated by commas"><button>Allow</button></form><p>This allows every rule in the group for that app.</p></section>
<script>` + serviceCommonScript + `
let questionCursor='';const questionText={forbidden:'You are not authorized to act on questions; nothing was changed.',unavailable:'The question service could not complete this; no decision is assumed. Retry or list again.',gap:'The question list changed; start the list again.',invalid:'The question service rejected this request as invalid.',unknown:'That question is no longer retained.',conflict:'A different answer is already recorded.'};
function questionStatus(o){return questionText[o]||'Question service outcome: '+o}
function questionRule(r){if(!r.rule)return '';const what=(r.rule.Permit?'allow ':'block ')+r.rule.Action+' on '+r.rule.Resource+' for '+r.rule.Subject.Program;if(r.edit&&r.edit.Outcome==='applied')return '\nRule written: '+what+' (asked at first use).';return '\nThe rule was not written ('+(r.edit?r.edit.Outcome:r.ruleOutcome||'no decision')+'): '+what+'. List the questions and choose Retry rule.'}
const ruleStateText={written:'Rule written.',not_written:'Answered, rule not written.'};
// questionAct's own reply used to say "... List again to refresh." while
// the list under it kept its stale rows; it now refreshes the list itself
// before showing the confirmation, the same fix every other list-changing
// write on this page and elsewhere gets (task 2026-09-23, fourteenth
// first-time visitor, findings 2 and 3).
async function questionAct(a){try{const r=await call('/questions',a);if(r.Outcome==='answered'||r.Outcome==='retired'){const line=(a.action==='answer'?'Answer recorded':'Question dismissed')+'.'+questionRule(r);questionCursor='';await questions();show('questions-status',line)}else show('questions-status',questionStatus(r.Outcome))}catch(e){fail('questions-status',e)}}
// A first-use question's own Text is a server-filled template,
// "<program's full path> wants to <raw action id> on <resource>" (the
// generic case abstraction-asks/go's own Questions[FirstUseKey] builds,
// and FirstUse reads the same way back apart); this Panel used to show
// that whole raw sentence unchanged (task 2026-09-23, thirteenth
// first-time visitor, finding 7). questionSentence recovers the same
// three parts FirstUse itself would (the About slot is the exact prefix,
// action never carries a space, so the first " on " is the one splitting
// action from resource) and reads them the way every other confirmation
// on this page already does: the program by its file name, the action
// through rulePlain (rightsActionPlainCache, the same catalogue Save rule
// itself builds from). A question this shape does not fit (every other
// key) reads unchanged.
function questionSentence(q){
 if(q.About&&q.Text.startsWith(q.About+' wants to ')){
  const rest=q.Text.slice((q.About+' wants to ').length);
  const sep=rest.indexOf(' on ');
  if(sep>0){
   const action=rest.slice(0,sep),resource=rest.slice(sep+4);
   if(action&&resource&&!/\s/.test(action))return{line:shortName(q.About)+' wants to '+rulePlain(rightsActionPlainCache,action).toLowerCase()+' on '+resource+'.',title:q.About+'; '+action,raw:q.Text};
  }
 }
 return{line:q.Text,title:'',raw:null};
}
// questions used to run independently of rightsList, so a first-use
// question could render before rightsList had ever populated
// rightsActionPlainCache, falling back to the generic action phrase
// instead of the catalogue's own curated one. It now waits for
// rightsListReady first (assigned once, near the bottom of this script,
// before questions is actually called), so the two loads still both
// happen once on arrival, in sequence rather than racing.
async function questions(){try{await rightsListReady;const p=await call('/questions?cursor='+encodeURIComponent(questionCursor));el('questions-list').replaceChildren();if(p.Outcome!=='page'){show('questions-status',questionStatus(p.Outcome));el('questions-next').disabled=true;return}questionCursor=p.Next;el('questions-next').disabled=p.Complete;show('questions-status',p.Records.length?(p.Complete?'End of current questions.':'More questions remain.'):'No retained questions.');for(const q of p.Records){const box=document.createElement('div');const s=questionSentence(q);const text=document.createElement('pre');const state=(p.ruleStates||{})[q.ID];text.textContent=s.line+'\n'+(q.Option?'Answered: '+q.Option:'Pending')+(state?'\n'+(ruleStateText[state]||'Rule state: '+state):'');if(s.title)text.title=s.title;box.append(text);if(s.raw){const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=s.raw;details.append(summary,pre);box.append(details)}if(!q.Option)for(const option of q.Options){const b=document.createElement('button');b.textContent=option;b.onclick=()=>questionAct({action:'answer',id:q.ID,option});box.append(b)}if(state==='not_written'){const retry=document.createElement('button');retry.textContent='Retry rule';retry.onclick=()=>questionAct({action:'answer',id:q.ID,option:q.Option});box.append(retry)}const retire=document.createElement('button');retire.textContent='Dismiss';retire.onclick=()=>questionAct({action:'retire',id:q.ID});box.append(retire);el('questions-list').append(box)}}catch(e){fail('questions-status',e)}}
el('questions-start').onclick=()=>{questionCursor='';return questions()};el('questions-next').onclick=questions;
let rightsCursor='',rightsRevision='',exploreSelf=null,rightsActionPlainCache=null;const rightsText={forbidden:'You are not authorized to administer rules; nothing was changed.',unavailable:'The rules service could not complete this; no change is assumed. List again before another edit.',gap:'The rules changed while listing; start the list again.',invalid:'The rules service rejected this rule as invalid.',conflict:'The rules changed since it was listed; nothing was changed. List again and reconcile before another edit.'};
function rightsStatus(o){return rightsText[o]||'Rules service outcome: '+o}
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
function ruleText(r){return (r.Permit?'allow ':'block ')+rulePlain(rightsActionPlainCache,r.Action)+' on '+oaResourceLabel(r.Resource)+' for '+shortName(r.Subject.Program)+' — '+accountLabel(r.Subject.Account)+'.'}
// rightsEdit used to refuse before rightsList had ever been read ("List
// rules before editing.") and, once applied, tell the person to list
// again while the table under it kept its stale rows; rightsList already
// runs on arrival (below) and now runs again on a successful edit, so the
// precondition and the "list again" instruction both go (task 2026-09-23,
// fourteenth first-time visitor, findings 2 and 3).
async function rightsEdit(e){try{const r=await call('/rights',{...e,revision:rightsRevision});if(r.Outcome==='applied'){const line=r.Current?'Rule set: '+ruleText(r.Current):'Rule revoked.';rightsCursor='';await rightsList(r.Current?{account:r.Current.Subject.Account,program:r.Current.Subject.Program,action:r.Current.Action,resource:r.Current.Resource}:undefined);oaShowResult('rights-status',line,r.Current||undefined);return}show('rights-status',rightsStatus(r.Outcome)+(r.Outcome==='conflict'&&r.Current?'\nCurrent rule: '+ruleText(r.Current):''))}catch(e){fail('rights-status',e)}}
function accountLabel(account){return exploreSelf&&account===exploreSelf.account?'this account':account}
function setByText(setBy){return setBy?shortName(setBy.Program)+' ('+accountLabel(setBy.Account)+')':'unknown'}
// Save rule's Account field used to open blank and required, with no
// default, while Allow's own quick-grant form already defaults its account
// to this one and says so ("Account (optional)"). ensureSelf now also keeps
// Save rule's own Account chooser in sync as soon as this Panel's own
// account resolves, the same "this account, <name>" wording Explore's own
// arrival sentence uses, with Other… for a different one (task 2026-09-23,
// twelfth first-time visitor, finding 6).
let rightsSelfAccountName='';
async function ensureSelf(){if(!exploreSelf){try{const v=await call('/explore');exploreSelf=v.self;rightsSelfAccountName=v.selfAccountName||''}catch(e){}}rightsAccountChooser.rebuild();return exploreSelf}
// rulePlain names one action for the catalogue dropdown and the "Actions no
// rule names yet" list: a known action (ActionPlain, rights_panel.go's
// rightsActionPlain) shows its plain phrase; an action this map does not
// carry a phrase for falls back to oaActionFallback's generic one. Neither
// ever shows the raw id itself: every caller already keeps it as the
// element's own title attribute (task 2026-09-23, seventh first-time
// visitor, finding 2: an id is never visible page text).
function rulePlain(map,action){const phrase=map&&map[action];return phrase?capitalize(phrase):oaActionFallback(action)}
function ruleSetter(r){const date=r.SetAt?new Date(r.SetAt).toLocaleDateString():'';if(r.Why==='installation')return 'set at installation';if(exploreSelf&&r.SetBy&&r.SetBy.Program===exploreSelf.program&&r.SetBy.Account===exploreSelf.account)return 'set by you'+(date?' on '+date:'');return 'set by '+setByText(r.SetBy)+(date?' on '+date:'')}
// dup names an action that repeats within this program's own group (task
// 2026-09-23, finding 2: two rules named only "Model resolve" or "Resource
// hold" with no way to tell them apart); a repeated action's sentence also
// names its resource, the one thing distinguishing it from its sibling.
function ruleSentence(r,plain,dup){return rulePlain(plain,r.Action)+(dup?' on '+oaResourceLabel(r.Resource):'')+': '+(r.Permit?'allowed':'blocked')+' for '+shortName(r.Subject.Program)+', '+ruleSetter(r)+'.'}
// showRightsList used to write "Rules revision: <8 hex>." as its own small
// muted line, dropping the inert "More rules remain."/"End of rules."
// wording entirely (the rights-next button beside it already carries that
// state). Even truncated, a revision is an identifier, and this Panel no
// longer shows one as visible text at all (task 2026-09-23, eighth
// first-time visitor, finding 2). The full value now lives only on the
// rules list heading's own title and in this section's Technical details.
function showRightsList(revision,empty){const out=el('rights-status');out.replaceChildren();if(empty)out.append(document.createTextNode('No rules on this page; unlisted actions are not granted.'));el('rights-heading').title=revision||'';if(revision){const details=document.createElement('details');details.className='oa-error-details';const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent='Revision: '+revision;details.append(summary,pre);out.append(details)}}
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
async function rightsList(target){
 const find=target&&(r=>r.Subject.Account===target.account&&r.Subject.Program===target.program&&r.Action===target.action&&r.Resource===target.resource);
 const restarting=!!target||!rightsCursor;
 if(target)rightsCursor='';
 let rules=[],catalog=null,complete=false,pages=0;
 try{
  for(;;){
   const firstPage=pages===0&&restarting;
   const p=await call('/rights?cursor='+encodeURIComponent(rightsCursor));
   if(p.Outcome!=='page'){rightsRevision='';show('rights-status',rightsStatus(p.Outcome));el('rights-next').disabled=true;return}
   if(!firstPage&&rightsRevision&&p.Revision!==rightsRevision){rightsRevision='';show('rights-status',rightsStatus('gap'));return}
   if(pages===0)catalog=p.Catalog;
   rightsActionPlainCache=p.ActionPlain;rightsRevision=p.Revision;rightsCursor=p.Next;
   rules=rules.concat(p.Rules);complete=p.Complete;
   const matched=find&&p.Rules.some(find);
   pages++;
   if(!target||matched||complete||pages>=20)break;
  }
 }catch(e){rightsRevision='';fail('rights-status',e);return}
 el('rights').replaceChildren();
 if(restarting){el('rights-action').replaceChildren();for(const a of (catalog||[])){const o=document.createElement('option');o.value=a;o.textContent=rulePlain(rightsActionPlainCache,a);o.title=a;el('rights-action').append(o)}}
 allowPrograms(rules.map(r=>r.Subject));
 for(const r of rules)rememberResource(r.Action,r.Resource);
 rebuildResourceChooser();
 el('rights-next').disabled=complete;
 showRightsList(rightsRevision,!rules.length);
 await ensureSelf();
 const order=[],groups=new Map();
 for(const r of rules){const key=r.Subject.Account+'|'+r.Subject.Program;if(!groups.has(key)){groups.set(key,{subject:r.Subject,rules:[]});order.push(key)}groups.get(key).rules.push(r)}
 for(const key of order){const g=groups.get(key);const group=document.createElement('div');group.className='rights-program';const gheading=document.createElement('h3');const strong=document.createElement('strong');strong.textContent=shortName(g.subject.Program);strong.title=g.subject.Program;const acct=document.createElement('span');acct.textContent=' — '+accountLabel(g.subject.Account);gheading.append(strong,acct);group.append(gheading);const counts=new Map();for(const r of g.rules)counts.set(r.Action,(counts.get(r.Action)||0)+1);for(const r of g.rules){const box=document.createElement('div');const text=document.createElement('pre');text.textContent=ruleSentence(r,rightsActionPlainCache,counts.get(r.Action)>1);text.title=r.Action+' on '+r.Resource+(r.Why&&r.Why!=='installation'?'. '+r.Why:'');const revoke=document.createElement('button');revoke.textContent='Revoke';revoke.onclick=()=>rightsEdit({edit:'revoke',account:r.Subject.Account,program:r.Subject.Program,action:r.Action,resource:r.Resource});box.append(text,revoke);group.append(box)}el('rights').append(group)}
 const ruled=new Set(rules.map(r=>r.Action));const unruled=(catalog||[]).filter(a=>!ruled.has(a));
 if(unruled.length){const section=document.createElement('div');heading(section,'Actions no rule names yet');const list=document.createElement('ul');for(const a of unruled){const li=document.createElement('li');li.textContent=rulePlain(rightsActionPlainCache,a);li.title=a;list.append(li)}section.append(list);el('rights').append(section)}
}
el('rights-start').onclick=()=>{rightsCursor='';return rightsList()};el('rights-next').onclick=rightsList;
var rightsListReady=rightsList();
var questionsReady=questions();
const allowSeen=new Set();let allowAccount='';
function allowPrograms(subjects){for(const s of subjects){if(!allowAccount)allowAccount=s.Account;if(allowSeen.has(s.Program))continue;allowSeen.add(s.Program);const o=document.createElement('option');o.value=s.Program;el('allow-programs').append(o)}rightsProgramChooser.rebuild()}
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
el('rights-action').onchange=()=>rebuildResourceChooser(true);
// Program path opened as one free-text field with no hint what to type; it
// now offers every program already named by a rule (allowSeen, the same
// set Allow's own datalist already builds) and every program the Device
// page's own resource table shows as a holder, "Other…" revealing the
// typed field for anything else. No endpoint on this runtime lists either
// running programs or registered applications; those two sources are what
// this Panel actually has (task 2026-09-23, twelfth first-time visitor,
// requirement 2). oaProgramChooser (panel_shell.go) is the one shared
// implementation Inference's Issue key and Credentials' App access now
// build their own equivalent chooser from too (task 2026-09-23, fourteenth
// first-time visitor, finding 8).
let rightsProgramsSeen=new Set();
const rightsProgramChooser=oaProgramChooser('rights-program-select','rights-program',()=>[...allowSeen,...rightsProgramsSeen]);
async function loadRightsDeviceHolders(){try{const v=await call('/card/table');for(const r of(v.resources||[]))for(const h of(r.holders||[]))if(h.program)rightsProgramsSeen.add(h.program)}catch(e){}rightsProgramChooser.rebuild()}
rightsProgramChooser.rebuild();var rightsDeviceHoldersReady=loadRightsDeviceHolders();
// oaAccountChooser (panel_shell.go) is the one shared implementation
// Credentials' App access now builds its own equivalent chooser from too
// (task 2026-09-23, fourteenth first-time visitor, finding 8).
const rightsAccountChooser=oaAccountChooser('rights-account-select','rights-account',()=>exploreSelf&&exploreSelf.account,()=>rightsSelfAccountName);
rightsAccountChooser.rebuild();var rightsSelfReady=ensureSelf();
function allowList(v){return v.split(',').map(x=>x.trim()).filter(x=>x)}
// Allow's required program path now names itself when left blank (task
// 2026-09-23, seventh first-time visitor, finding 4, the Issue key
// pattern).
el('allow-program').addEventListener('invalid',()=>show('rights-status','Program path is required to allow anything.'));
// The Allow confirmation used to read "Allowed downloads for C:\...\
// my-program.exe: permit Jobs submit on abstraction.job/acceptance@1.",
// the program's full path and the raw contract-shaped resource both
// visible text. It now names the program by its file name and the
// resource through oaResourceLabel, the same reading Rights' own rule rows
// give a raw resource; the full path and the raw resource sit on the
// line's own title and behind Technical details (task 2026-09-23,
// eleventh first-time visitor, finding 4).
// allowResourceLabel reads a landed rule's resource the same way
// oaResourceLabel does, except a contract-shaped one (the job-acceptance
// service every downloads or inference bundle grants Jobs submit on) names
// the bundle this confirmation already knows it granted, "the downloads
// service" or "the inference service", instead of oaResourceLabel's own
// generic "this service" (task 2026-09-23, twelfth first-time visitor,
// finding 4).
function allowResourceLabel(resource,bundle){return /^abstraction\.[a-z]+\//.test(resource)?'the '+bundle+' service':oaResourceLabel(resource)}
// rightsAllow used to refuse before rightsList had ever been read ("List
// rules before allowing.") and, once applied, tell the person to list
// again while the table under it kept its stale rows; it now reads the
// list again itself on success, the same fix rightsEdit above gets (task
// 2026-09-23, fourteenth first-time visitor, findings 2 and 3).
async function rightsAllow(){const bundle=el('allow-for').value,names=allowList(el('allow-names').value);const body={for:bundle,revision:rightsRevision,account:el('allow-account').value||allowAccount,program:el('allow-program').value,registries:bundle==='downloads'?names:[],hosts:bundle==='inference'?names:[],credentials:allowList(el('allow-credentials').value)};try{const r=await call('/rights/allow',body);const landed=r.landed.map(x=>rulePlain(rightsActionPlainCache,x.action)+' on '+allowResourceLabel(x.resource,bundle)).join('\n');if(r.outcome==='applied'){rightsCursor='';await rightsList(r.landed&&r.landed.length?{account:body.account,program:body.program,action:r.landed[0].action,resource:r.landed[0].resource}:undefined);oaShowResult('rights-status','Allowed '+bundle+' for '+shortName(body.program)+': '+landed,r,'Program: '+body.program);return}el('rights-status').title='';show('rights-status',rightsStatus(r.outcome)+'\nStopped at '+rulePlain(rightsActionPlainCache,r.stopped.action)+' on '+allowResourceLabel(r.stopped.resource,bundle)+'; '+r.landed.length+' of '+r.rules.length+' rules landed'+(landed?':\n'+landed:'.'))}catch(e){fail('rights-status',e)}}
el('rights-allow').onsubmit=e=>{e.preventDefault();return rightsAllow()};
// Save rule's three required fields now name themselves when left blank
// (task 2026-09-23, eighth first-time visitor, finding 4, the Issue key
// pattern): the browser blocks the submit event for a failed required
// field without running this form's own onsubmit, but always fires
// "invalid" on the field, submit or not.
const rightsGrantFieldLabel={'rights-account':'Account','rights-program':'Program path','rights-resource':'Resource'};
for(const id in rightsGrantFieldLabel)el(id).addEventListener('invalid',()=>show('rights-status',rightsGrantFieldLabel[id]+' is required.'));
el('rights-grant').onsubmit=e=>{e.preventDefault();return rightsEdit({edit:'set',account:el('rights-account').value,program:el('rights-program').value,action:el('rights-action').value,resource:el('rights-resource').value,permit:el('rights-permit').value==='permit'})};
</script>`
