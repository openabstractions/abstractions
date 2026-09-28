package main

// explorePage is the Panel's Explore page: try each capability as this
// Panel and see the result next to the rule that allows or blocks it (task
// 2026-09-23, requirement 1: split out of the Status page, its own page at
// /explore).
// explore-status carries one plain sentence ("Showing this Panel's own
// capabilities. Probes act as monitor.exe...") plus a Technical details
// disclosure beside it; it used to be a <pre>, rendering that sentence
// monospace, like a JSON dump instead of a status sentence (task
// 2026-09-23, fourteenth first-time visitor, finding 1). It is a <div>
// now, not a <p>, since a <details> element cannot sit inside a <p>.
const explorePage = `<h1 id="explore">Explore</h1><p class="oa-note">Try each capability as this Panel and see the result next to the rule that allows or blocks it.</p><form id="explore-subject"><input id="explore-program" aria-label="Program" placeholder="Program path (blank shows this Panel)"><input id="explore-account" aria-label="Account" placeholder="Account (blank shows this Panel)"><span id="explore-account-label" class="oa-note"></span></form><button id="explore-load">Load</button><div id="explore-status"></div><div id="explore-list"></div>
<script>` + serviceCommonScript + `
let exploreSelf=null,exploreActionPlain=null;
// argumentPlaceholderText and argumentCaptionText give a friendlier
// placeholder and, where useful, a helper caption for a probe's own raw
// Argument word; storage read's own "DIGEST" is the one this page named
// nowhere near it, with no example shape and no hint where to find one
// (task 2026-09-23, thirteenth first-time visitor, finding 8). An
// argument this map does not carry keeps its own raw word unchanged.
const argumentPlaceholderText={DIGEST:"the object's digest (sha256:…)"};
const argumentCaptionText={DIGEST:'Copy a digest from the Device or Work page.'};
// explorePlain names one deciding rule in plain words, the same table
// Questions and rights uses (rights_panel.go's rightsActionPlain, sent here
// as v.actionPlain); an action that map does not carry falls back to
// oaActionFallback's generic phrase, same as everywhere else this map is
// used.
function explorePlain(action){const p=exploreActionPlain&&exploreActionPlain[action];return p?capitalize(p):oaActionFallback(action)}
function exploreQuery(p,arg){const q={capability:p.capability,operation:p.operation,program:el('explore-program').value,account:el('explore-account').value};if(arg)q.argument=arg;if(p.writes)q.confirm='write';return '/explore?'+new URLSearchParams(q)}
// exploreRevision reads the first 8 hex characters out of a rules revision
// (task 2026-09-23, seventh first-time visitor, finding 2: the full value
// is never visible page text); the caller keeps the full value as the
// element's own title.
function exploreRevision(revision){const m=/[0-9a-f]{8,}/i.exec(revision||'');return (m?m[0]:revision||'').slice(0,8)}
function ruleView(r){if(!r)return 'This call needs no rule.';const who=r.subject.account+' running '+r.subject.program;if(r.outcome==='found'||r.outcome==='expired')return 'Rule for '+who+': '+(r.permit?'permit ':'deny ')+explorePlain(r.action)+' on '+r.resource+(r.outcome==='expired'?' (expired, decides nothing)':'')+', set by '+(r.setBy?r.setBy.program:'?')+' at '+r.setAt+(r.why?' because '+r.why:'')+'. Rules revision: '+exploreRevision(r.revision)+'.';if(r.outcome==='unknown')return 'No exact rule for '+who+': '+explorePlain(r.action)+' on '+r.resource+' is not granted. Rules revision: '+exploreRevision(r.revision)+'.';if(r.outcome==='forbidden')return 'The rules service did not show the rule: you are not a rules operator.';return 'The rule could not be read: '+r.outcome+(r.error?' ('+r.error+')':'')}
function plainOf(text){return typeof oaPlain==='function'?oaPlain(text):{line:String(text),details:''}}
function showPlain(target,text){const p=plainOf(text);target.replaceChildren();const line=document.createElement('p');line.textContent=p.line;target.append(line);if(p.details&&p.details!==p.line){const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=p.details;details.append(summary,pre);target.append(details)}}
const exploreAnsweredOutcomes=new Set(['read','listed','resolved','page','applied','opened','data','permitted','found','observed','accepted','requested','already_terminal']);
const exploreRefusalText={forbidden:'this Panel is not authorized',unavailable:'the service is not available right now',incompatible:'the runtime does not support this call',invalid:'the request was invalid',unmet_requirements:'a required guarantee is unavailable',not_ready:'the service is not ready yet',runtime_unavailable:'the runtime could not be reached'};
function exploreCount(v){if(v.outcome!=='page'||!v.summary)return null;for(const val of Object.values(v.summary))if(Array.isArray(val))return val.length;return null}
// exploreRuleSetter names who set the deciding rule, the same words Rights'
// own ruleSetter (rights_page.go) reads a rule's SetBy/SetAt/Why as, over
// the lowercase field names Explore's own envelope carries (exploreRuleView,
// explore_panel.go) rather than Rights' PolicyRule ones.
function exploreRuleSetter(r){const date=r.setAt?new Date(r.setAt).toLocaleDateString():'';if(r.why==='installation')return 'set at installation';if(exploreSelf&&r.setBy&&r.setBy.program===exploreSelf.program&&r.setBy.account===exploreSelf.account)return 'set by you'+(date?' on '+date:'');return (r.setBy?'set by '+shortName(r.setBy.program):'set by an unknown program')+(date?' on '+date:'')}
// exploreOutcomeSentence used to read "Allowed; the call answered.", the
// deciding rule's own action, resource, account SID, two full paths, an ISO
// time and the rules revision left in a raw pre element sibling with no
// disclosure at all (task 2026-09-23, twelfth first-time visitor, finding
// 2). A call a
// rule decided now names that rule in the one visible line, the same words
// the rule's own row would use ("Allowed: Model resolve on ollama, set at
// installation."); the rule's full record, with everything the id, path,
// account and time carry, moves into this result's own Technical details
// (renderResult), alongside the raw JSON that was already there.
function exploreOutcomeSentence(v,rule){if(exploreAnsweredOutcomes.has(v.outcome)){if(rule&&rule.outcome==='found')return 'Allowed: '+explorePlain(rule.action)+' on '+argumentPhrase(rule.resource)+', '+exploreRuleSetter(rule)+'.';const n=exploreCount(v);return n===null?'Allowed; the call answered.':'Allowed; '+n+' item'+(n===1?'':'s')+'.'}const reason=exploreRefusalText[v.outcome]||exploreRefusalText[v.resolution]||(v.detail?plainOf(v.detail).line:'outcome '+v.outcome);return 'Refused: '+reason+'.'}
function renderResult(target,v,rule){target.replaceChildren();const line=document.createElement('p');line.textContent=exploreOutcomeSentence(v,rule);target.append(line);const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=JSON.stringify({capability:v.capability,operation:v.operation,contract:v.contract,subject:v.subject,outcome:v.outcome,resolution:v.resolution,detail:v.detail,summary:v.summary},null,2);details.append(summary,pre);if(rule){const rulePre=document.createElement('pre');rulePre.textContent=ruleView(rule);details.append(rulePre)}target.append(details)}
// exploreNotOffered marks a probe this runtime refused as incompatible once
// (task 2026-09-23, finding 5): calling it again would only repeat the same
// refusal, so the row says so plainly and Call, Grant and Revoke stop
// inviting another try.
function exploreNotOffered(box){if(box.notOffered)return;box.notOffered=true;box.run.disabled=true;box.run.title='Not offered by this runtime';if(box.grant){box.grant.disabled=true;box.revoke.disabled=true}const note=document.createElement('p');note.className='error';note.textContent='Not offered by this runtime.';box.append(note)}
// exploreRun used to set box.grant.disabled unconditionally; a capability
// with no rule at all (task 2026-09-23, tenth first-time visitor, finding
// 6) has no box.grant or box.revoke to set, since such a probe shows the
// "Nothing to grant" note in their place instead. Setting it threw
// "Cannot set properties of undefined (setting 'disabled')" on every call
// to one of those probes, caught as a raw error instead of a result
// (task 2026-09-23, eleventh first-time visitor, finding 1).
// A missing argument used to be reported at the top of the page,
// explore-status, shared by every probe here; on a page of twenty rows a
// first-time visitor read it as an error belonging to the whole page
// rather than the one field beside it (task 2026-09-23, thirteenth
// first-time visitor, finding 8). It now shows in the one place beside
// the row's own field, box.argumentNote.
async function exploreRun(p,box){const arg=box.argument?box.argument.value:'';if(box.argumentNote)box.argumentNote.textContent='';if(p.argument&&!arg){if(box.argumentNote)box.argumentNote.textContent='Fill in '+(argumentPlaceholderText[p.argument]||p.argument)+' above to call '+p.capability+' '+p.operation+'.';return}if(p.writes&&!confirm(p.capability+' '+p.operation+': '+(p.note||'writes')+'. Call it?')){show('explore-status',p.capability+' '+p.operation+' was not called.');return}try{const v=await call(exploreQuery(p,arg));renderResult(box.result,v.result,v.rule);box.result.className=v.result.resolution||v.result.outcome==='error'?'error':'';box.lastRule=v.result.rule;box.lastRuleView=v.rule;if(box.grant)box.grant.disabled=box.revoke.disabled=!v.result.rule;if(v.result.resolution==='incompatible')exploreNotOffered(box)}catch(e){showPlain(box.result,e.message||String(e));box.result.className='error'}}
// Grant on a capability an installation rule (or an earlier Grant) already
// permits used to still round-trip a "set" edit for the identical rule,
// which could land as a conflict or another outcome this page's own
// rightsStatus table reads as nothing worth restating, reading as though
// the click did nothing at all (task 2026-09-23, fourteenth first-time
// visitor, finding 6: "router hosts" is decided by an installation rule
// that already permits it). box.lastRuleView (the full deciding-rule
// record exploreRun already reads for the result line above) carries
// whether it already permits or denies this exact action, so Grant and
// Revoke can say so directly instead of attempting an edit with nothing
// left to change.
async function exploreEdit(p,box,edit,permit){if(!box.lastRule){show('explore-status','Call the probe first; its rule names the action and resource.');return}const view=box.lastRuleView;if(edit==='set'&&view&&view.outcome==='found'&&view.permit===permit){show('explore-status',permit?'Already allowed.':'Already denied.');return}if(edit==='revoke'&&(!view||view.outcome!=='found')){show('explore-status','Nothing to revoke; no rule currently decides this call.');return}try{const page=await call('/rights?cursor=');if(page.Outcome!=='page'){show('explore-status',rightsStatus(page.Outcome));return}const r=await call('/rights',{edit,revision:page.Revision,account:el('explore-account').value,program:el('explore-program').value,action:box.lastRule.action,resource:box.lastRule.resource,...(edit==='set'?{permit}:{})});show('explore-status',r.Outcome==='applied'?(edit==='set'?(permit?'Granted':'Denied'):'Revoked')+' '+explorePlain(box.lastRule.action)+' on '+box.lastRule.resource+' at revision '+exploreRevision(page.Revision)+'; calling again.':rightsStatus(r.Outcome));if(r.Outcome==='applied')await exploreRun(p,box)}catch(e){fail('explore-status',e)}}
const rightsText={forbidden:'You are not authorized to administer rules; nothing was changed.',unavailable:'The rules service could not complete this; no change is assumed. List again before another edit.',gap:'The rules changed while listing; start the list again.',invalid:'The rules service rejected this rule as invalid.',conflict:'The rules changed since it was listed; nothing was changed. List again and reconcile before another edit.'};
function rightsStatus(o){return rightsText[o]||'Rules service outcome: '+o}
// argumentPhrase turns a probe's own placeholder-shaped rule resource (e.g.
// "<registry>") into a plain clause naming what fills it, instead of
// printing the placeholder syntax itself as row text (task 2026-09-23,
// finding 5). A resource that is not a placeholder prints as before.
// argumentPhrase also redacts a resource that is itself a raw contract id
// (a capability rule scoped to its own contract, e.g. "abstraction.config/
// editor@1" for config rewrite): task 2026-09-23, seventh first-time
// visitor, finding 2 covers a rule's resource the same as its action; the
// id stays reachable on the card's own title, set alongside it.
// "service" is the vocabulary decision's own word for an "abstraction.x/y@n"
// string (research/vocabulary/DECISION.md D4), replacing "this capability's
// own contract" (task 2026-09-23, twelfth first-time visitor, finding 4);
// the same word oaResourceLabel now reads it as (panel_shell.go).
function argumentPhrase(resource){const placeholder=/^<(.+)>$/.exec(resource);if(placeholder)return 'the '+placeholder[1]+' you enter';if(/^abstraction\.[a-z]+\//.test(resource))return "this service";return resource}
// exploreLoad used to run only on a Load click, so the page opened as one
// sentence and one button with nothing this Panel can already show without
// waiting on it (task 2026-09-23, twelfth first-time visitor, finding 1: the
// other pages already read themselves on arrival). It now also runs once,
// unclicked, as soon as the page's own script loads; Load still re-runs it
// for a different program or account typed into the form above.
async function exploreLoad(){try{const wasEmpty=!el('explore-program').value&&!el('explore-account').value;const v=await call('/explore');exploreSelf=v.self;exploreActionPlain=v.actionPlain;if(!el('explore-program').value)el('explore-program').value=v.self.program;
// The Account field left blank filled itself with the raw SID as visible
// input text; probes still need that raw value to query with, so it stays
// the field's own value, but a plain label beside it now names the account
// a person reads as their own, the SID kept on the field's title and in
// Technical details (task 2026-09-23, tenth first-time visitor, finding 4).
if(!el('explore-account').value){el('explore-account').value=v.self.account;el('explore-account').title=v.self.account;el('explore-account-label').textContent=v.selfAccountName?'your account, '+v.selfAccountName:''}
const list=el('explore-list');list.replaceChildren();for(const p of v.probes){const box=document.createElement('div');box.className='record';const title=document.createElement('h3');title.textContent=p.capability+' '+p.operation+(p.writes?' (writes)':'');title.title=p.contract;
// contract used to show the probe's raw contract id as its only
// description (task 2026-09-23, seventh first-time visitor, finding 2: an
// id is never visible page text); it now reads one plain sentence, the
// deciding rule in the same plain words Questions and rights uses
// (explorePlain), with the contract id and the rule's own action and
// resource kept as this paragraph's title.
const contract=document.createElement('p');contract.title=p.contract+(p.rule?' — '+p.rule.action+' on '+p.rule.resource:'');contract.textContent='Tries '+p.capability+' '+p.operation+'.'+(p.rule?' Decided by '+explorePlain(p.rule.action)+' on '+argumentPhrase(p.rule.resource)+'.':' This call needs no rule.')+(p.note?' '+p.note:'');box.append(title,contract);if(p.argument){const label=document.createElement('label');label.textContent='Argument: ';box.argument=document.createElement('input');box.argument.placeholder=argumentPlaceholderText[p.argument]||p.argument;label.append(box.argument);box.append(label);if(argumentCaptionText[p.argument]){const caption=document.createElement('span');caption.className='oa-note';caption.textContent=argumentCaptionText[p.argument];box.append(caption)}box.argumentNote=document.createElement('p');box.argumentNote.className='error';box.append(box.argumentNote)}const run=document.createElement('button');box.run=run;run.textContent='Call';run.onclick=()=>exploreRun(p,box);box.append(run);
// Grant and Revoke, disabled until a call names the rule they would edit,
// used to stay disabled forever for a capability with no rule at all,
// explaining nothing (task 2026-09-23, tenth first-time visitor, finding 6).
// Reusing the shared grant control's own "Nothing to grant" wording
// (panel_shell.go's gApplyHeld), such a probe shows that plain note instead
// of two buttons that could never do anything.
if(p.rule){box.grant=document.createElement('button');box.grant.textContent='Grant';box.grant.disabled=true;box.grant.onclick=()=>exploreEdit(p,box,'set',true);box.revoke=document.createElement('button');box.revoke.textContent='Revoke';box.revoke.disabled=true;box.revoke.onclick=()=>exploreEdit(p,box,'revoke',false);box.append(box.grant,box.revoke)}else{const note=document.createElement('span');note.className='oa-grant-status';note.textContent='Nothing to grant; this capability needs no rule.';box.append(note)}
box.result=document.createElement('div');box.append(box.result);list.append(box)}// The status line used to name the caller by its own full path ("Probes
// act as C:\...\Abstraction Panel.exe."); it now names it by its file name,
// with the full path on the line's own title and in Technical details
// (task 2026-09-23, ninth first-time visitor, finding 5).
const status=el('explore-status');status.replaceChildren();
status.append(document.createTextNode((wasEmpty?"Showing this Panel's own capabilities. ":'')+'Probes act as '+shortName(v.self.program)+'. Rules name the program above.'));
status.title=v.self.program;
const pathDetails=document.createElement('details');const pathSummary=document.createElement('summary');pathSummary.textContent='Technical details';const pathPre=document.createElement('pre');pathPre.textContent='Program: '+v.self.program+'\nAccount: '+v.self.account;pathDetails.append(pathSummary,pathPre);status.append(pathDetails);
}catch(e){fail('explore-status',e)}}
el('explore-load').onclick=exploreLoad;
var exploreLoadReady=exploreLoad();
</script>`
