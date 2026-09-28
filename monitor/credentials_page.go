package main

// credentialsPage is the Panel's Credentials page. Its words follow
// research/vocabulary/DECISION.md §4: Saved credentials, Add credential
// (credentials row 9), Allow and Block (D53); the rule it edits keeps its
// wire action abstraction.credentials/apply.
const credentialsPage = `<h1>Credentials</h1>
<p>Secrets registered on this account: their names, kinds, status, targets, consumers, and where they're used. You type the secret once; it's never shown, logged, or sent back to you again.</p>
<p><a id="back" href="#">Back to Status</a></p>
<section id="saved"><h2>Saved credentials</h2><button id="list-start">Refresh</button><button id="list-next" disabled>Next page</button><pre id="list-status"></pre><pre id="limits"></pre><div id="records"></div></section>
<section id="new"><h2>Add credential</h2><p>Register a name that isn't in use yet.</p>
<form id="add-form"><input id="add-name" required placeholder="name"><select id="add-kind" aria-label="Kind"><option value="bearer">bearer</option><option value="header">header</option></select><input id="add-header" placeholder="Header name (for the header kind)"><input id="add-targets" required placeholder="Targets, separated by commas"><select id="add-consumers-select" multiple required aria-label="Consumers"></select><input id="add-consumers" placeholder="Other consumers, separated by commas" hidden><input id="add-expires" type="datetime-local" aria-label="Expiry (optional)"><input id="add-secret" type="password" autocomplete="off" required placeholder="secret"><button>Add</button></form>
<pre id="add-status"></pre></section>
<section id="rotate"><h2>Rotate</h2><p>Replace the secret and expiry for a name you've already listed.</p>
<form id="rotate-form"><select id="rotate-name-select" aria-label="Name"></select><input id="rotate-name" required placeholder="name" hidden><input id="rotate-revision" required placeholder="Revision (from the list)"><input id="rotate-expires" type="datetime-local" aria-label="Expiry (optional)"><input id="rotate-secret" type="password" autocomplete="off" required placeholder="new secret"><button>Rotate</button></form>
<pre id="rotate-status"></pre></section>
<section id="revoke"><h2>Revoke</h2><p>Delete the secret right away and keep a record that it existed, at the revision you've already listed.</p>
<form id="revoke-form"><select id="revoke-name-select" aria-label="Name"></select><input id="revoke-name" required placeholder="name" hidden><input id="revoke-revision" required placeholder="Revision (from the list)"><button>Revoke</button></form>
<pre id="revoke-status"></pre></section>
<section id="access"><h2>App access</h2><p>Set whether one app on one account can use this credential. This uses the rule <span title="abstraction.credentials/apply">credentials apply</span> on that credential (for example credential:huggingface). Applies to the rules as they were when you last clicked Show; if they changed since, Show again first. It's the same rule editor as <a id="rights-editor-link" href="#">Questions and permissions</a>. Listing and editing any of it needs this Panel to hold <span title="abstraction.credentials/holder.manage">credentials manage</span> and <span title="abstraction.credentials/holder.read">credentials list</span>.</p>
<button id="allow-list">Refresh</button><pre id="allow-status"></pre><div id="allow-rules"></div>
<form id="allow-form"><select id="allow-name-select" aria-label="Credential name"></select><input id="allow-name" required placeholder="credential name" hidden><select id="allow-account-select" aria-label="Account"></select><input id="allow-account" required placeholder="account" hidden><select id="allow-program-select" aria-label="Program"></select><input id="allow-program" required placeholder="Program path" hidden><select id="allow-permit" aria-label="Allow or block"><option value="permit">Allow</option><option value="deny">Block</option></select><button>Save</button></form></section>
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);
function show(id,x){el(id).textContent=typeof x==='string'?x:JSON.stringify(x,null,2)}
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function shortName(path){const parts=String(path||'').split(/[\\/]/).filter(Boolean);return parts.length?parts[parts.length-1]:path}
el('back').href='/?k='+encodeURIComponent(key);
el('rights-editor-link').href='/rights?k='+encodeURIComponent(key)+'#rights-section';
function commaList(v){return v.split(',').map(x=>x.trim()).filter(x=>x)}
function isoOrEmpty(v){if(!v)return '';const d=new Date(v);return isNaN(d.getTime())?'':d.toISOString()}
const outcomeText={forbidden:'This panel is not authorized for credentials; nothing changed.',unavailable:'The credentials service could not complete this; list again before another conditional edit.',gap:'The listing changed; start again.',invalid:'The credentials service rejected this as invalid.',no_secure_store:'This runtime has no configured platform store; nothing was stored.',exhausted:'This account already holds its maximum number of credentials.',unsupported_kind:'This runtime\'s applier does not support that kind.',unknown:'No credential has that name.',conflict:'The listed revision is stale; list again before retrying.'};
function outcomeStatus(o){return outcomeText[o]||'Credentials service outcome: '+o}
// storeLabel names the platform store in words a reader recognizes, for the
// wire words Limits.SecureStore carries (abstraction-credentials/go/backend.go);
// an unrecognized future word still shows, unchanged, rather than disappearing.
function storeLabel(s){const known={'windows-credential-manager':'Windows Credential Manager','secret-service':'the desktop secret service','file-0600':'a local file, restricted to this account','macos-keychain':'macOS Keychain','none':'no platform store'};return known[s]||s}
// limitsText turns the "Show credentials" limits line into a sentence,
// instead of the "store: ...; kinds: ...; max secret bytes: ...; max
// credentials: ..." field dump the person read literally (task 2026-09-23
// finding 5).
function limitsText(l){return 'Secrets are kept in '+storeLabel(l.SecureStore)+'. Up to '+l.MaxCredentials+' credentials of kind '+l.SupportedKinds.join(' or ')+', each up to '+l.MaxSecretBytes.toLocaleString()+' bytes.'}
// metaBox's own row used to concatenate its id, revision, targets, raw
// consumer contract ids, the full registered-by path and a raw timestamp
// onto one dense block of text. It now reads one summary line, "<name> ·
// <state> · registered by <file name> <readable time>", a consumers line in
// plain words when any are set, and every raw value (kind, header, revision,
// targets, consumers' own ids, expiry, last applied, full path, exact
// timestamp) behind Technical details (task 2026-09-23, tenth first-time
// visitor, findings 2 and 3).
// A gateway key's own credential name is "local-key.<hash of the target
// program>.<random>", meaningless as visible text; credentialKeyPrograms
// (loaded once, best-effort, alongside the list) names the program the
// key was actually issued for, from Inference's own key list, the one
// place that program is recorded in plain text (task 2026-09-23, eleventh
// first-time visitor, finding 7).
function metaBoxName(m){if(m.Kind!=='openabstractions/local-key@1')return m.Name;const program=credentialKeyPrograms&&credentialKeyPrograms.get(m.Name);return program?'Gateway key for '+shortName(program):'Gateway key'}
// credentialSentenceName reads the same gateway-key program metaBoxName
// does, worded for a sentence position ("Revoked the gateway key for
// testapp.exe.") rather than metaBoxName's own heading position ("Gateway
// key for testapp.exe"); an ordinary credential's own name is already the
// name a person chose, so it reads unchanged either way (task 2026-09-23,
// thirteenth first-time visitor, finding 6).
function credentialSentenceName(name){const program=credentialKeyPrograms&&credentialKeyPrograms.get(name);return program?'the gateway key for '+shortName(program):name}
// consumerServiceName names a consumer contract, for the saved list below
// and for the Consumers chooser above, the same way for both (task
// 2026-09-23, twelfth first-time visitor, finding 5). oaServiceName's own
// "inference" entry (panel_shell.go) intentionally names the whole layer,
// used by capability-unavailable sentences elsewhere that should not
// narrow to one operation; a consumer, by contrast, always names one
// specific inference operation, so this credentials-local table overrides
// each one (and model/resolver, "model resolve", the rights catalogue's
// own phrase for it) before falling back to oaServiceName for anything
// else this runtime's consumers ever carry (today, only downloads).
const consumerServiceNames={'inference/chat':'chat','inference/embed':'embeddings','inference/image':'image generation','inference/live':'live voice','inference/speech':'speech','inference/transcription':'transcription','model/resolver':'model resolve'};
function consumerServiceName(id){const contract=oaContractName(id);if(!contract)return id;const full=contract.layer+'/'+contract.name;return consumerServiceNames[full]||oaServiceName(contract)}
function metaBox(m){const box=document.createElement('div');box.className='record';
const summary=document.createElement('p');summary.textContent=metaBoxName(m)+' · '+m.State+' · registered by '+shortName(m.RegisteredBy.Program)+' '+oaLocalTime(m.Registered);summary.title='id: '+m.Name+'; '+m.RegisteredBy.Program+'; registered '+m.Registered;box.append(summary);
const consumers=(m.Scope.Consumers||[]).map(consumerServiceName);
if(consumers.length){const used=document.createElement('p');used.textContent='Used by: '+consumers.join(', ')+'.';box.append(used)}
const details=document.createElement('details');const dsummary=document.createElement('summary');dsummary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=JSON.stringify(m,null,2);details.append(dsummary,pre);box.append(details);
if(m.State!=='revoked'){const label=metaBoxName(m);const chosen=document.createElement('span');chosen.className='oa-note';const rot=document.createElement('button');rot.textContent='Use for rotate';rot.onclick=()=>{el('rotate-revision').value=m.Revision;chooseForCredential('rotate','rotate','rotate-name-select','rotate-name',m.Name,label,chosen)};const rev=document.createElement('button');rev.textContent='Use for revoke';rev.onclick=()=>{el('revoke-revision').value=m.Revision;chooseForCredential('revoke','revoke','revoke-name-select','revoke-name',m.Name,label,chosen)};const allow=document.createElement('button');allow.textContent='Allow/block this name';allow.onclick=()=>chooseForCredential('allow/block','access','allow-name-select','allow-name',m.Name,label,chosen);box.append(rot,rev,allow,chosen)}
return box}
// Use for rotate, Use for revoke and Allow/deny this name used to fill a
// field in a form far below the saved list with no visible change here at
// all: the person had to already know to scroll down and look (task
// 2026-09-23, fourteenth first-time visitor, finding 7). Each button now
// scrolls that section into view, focuses whichever field selectCredentialName
// left visible, and names what it chose right beside the button that was
// clicked, the one place the person was already looking.
function chooseForCredential(kind,sectionId,selectId,inputId,name,label,note){
 selectCredentialName(selectId,inputId,name);
 const section=el(sectionId);if(section&&section.scrollIntoView)section.scrollIntoView({behavior:'smooth',block:'start'});
 const target=el(inputId).hidden?el(selectId):el(inputId);
 if(target&&target.focus)target.focus();
 note.textContent='Chosen for '+kind+': '+label+'.';
}
// Rotate, Revoke and Allow/deny's own name field used to be filled by
// these buttons with a gateway key's raw name, "local-key.<hash>.
// <random>", as the field's own visible, editable text; nothing near
// these forms read it as the friendly name metaBoxName already computes
// for the very same credential (task 2026-09-23, thirteenth first-time
// visitor, finding 5). Each of the three now offers a chooser built from
// the credentials this Panel has listed, the friendly name as each
// option's own text and the raw name it still sends as the option's
// value, so typing or reading still works unchanged; "Other…" reveals a
// typed field for a name not listed yet. credentialNames (raw name ->
// friendly label) is the one map behind all three and the saved list's
// own metaBoxName, so a name reads the same word everywhere.
let credentialNames=new Map();
const nameChoosers=[['rotate-name-select','rotate-name'],['revoke-name-select','revoke-name'],['allow-name-select','allow-name']];
function nameChooserSync(selectId,inputId){const v=el(selectId).value;if(v==='__other__'){el(inputId).hidden=false;if(credentialNames.has(el(inputId).value))el(inputId).value=''}else{el(inputId).hidden=true;el(inputId).value=v}}
function rebuildNameChooser(selectId,inputId){const select=el(selectId);const current=select.value;select.replaceChildren();for(const[name,label]of credentialNames){const o=document.createElement('option');o.value=name;o.textContent=label;o.title=name;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);select.value=current&&(credentialNames.has(current)||current==='__other__')?current:'__other__';nameChooserSync(selectId,inputId)}
function selectCredentialName(selectId,inputId,name){el(selectId).value=credentialNames.has(name)?name:'__other__';nameChooserSync(selectId,inputId);if(!credentialNames.has(name))el(inputId).value=name}
for(const[selectId,inputId] of nameChoosers){el(selectId).onchange=()=>nameChooserSync(selectId,inputId);rebuildNameChooser(selectId,inputId)}
let listCursor='';
// loadKeyPrograms reads Inference's own key list once, best-effort: a
// Panel without inference key issue still lists credentials, just without
// a gateway key's own program name filled in (it falls back to "Gateway
// key" alone).
let credentialKeyPrograms=null;
async function loadKeyPrograms(){if(credentialKeyPrograms)return;try{const r=await call('/inference/keys');if(r.Outcome==='page')credentialKeyPrograms=new Map(r.Keys.map(k=>[k.Name,k.Program]))}catch(e){}}
async function list(){try{await loadKeyPrograms();const p=await call('/credentials?cursor='+encodeURIComponent(listCursor));if(p.Outcome!=='page'){show('list-status',outcomeStatus(p.Outcome));el('list-next').disabled=true;return}listCursor=p.Next;el('list-next').disabled=p.Complete;show('limits',limitsText(p.Limits));show('list-status',p.Records.length?(p.Complete?'End of list.':'More records remain.'):'No credentials registered.');el('records').replaceChildren();for(const m of p.Records){el('records').append(metaBox(m));credentialNames.set(m.Name,metaBoxName(m))}for(const[selectId,inputId] of nameChoosers)rebuildNameChooser(selectId,inputId)}catch(e){oaShowError('list-status',e.message)}}
el('list-start').onclick=()=>{listCursor='';return list()};el('list-next').onclick=list;
var listReady=list();
// Every required field across Add, Rotate and Revoke now names itself when
// left blank (task 2026-09-23, seventh first-time visitor, finding 4, the
// Issue key pattern): the browser blocks the submit event for a failed
// required field without running the form's own onsubmit, but always
// fires "invalid" on the field, submit or not.
const requiredFieldLabel={'add-name':'Name','add-targets':'Targets','add-consumers-select':'Consumers','add-secret':'Secret','rotate-name':'Name','rotate-revision':'Revision','rotate-secret':'New secret','revoke-name':'Name','revoke-revision':'Revision'};
for(const id in requiredFieldLabel){const status={add:'add-status',rotate:'rotate-status',revoke:'revoke-status'}[id.split('-')[0]];el(id).addEventListener('invalid',()=>show(status,requiredFieldLabel[id]+' is required.'))}
// A stored credential used to print "Stored test-credential at revision
// 2-3a9e13ed."; the revision now sits on the line's own title and in
// Technical details, oaShowResult's own shape (task 2026-09-23, tenth
// first-time visitor, finding 1).
// abstraction-credentials/go's own holder.go validates Consumers against a
// contract-id pattern ("abstraction.<layer>/<name>@<n>", e.g.
// "abstraction.download/http-execution@1") the field's own placeholder
// never states; a first-time visitor typing an ordinary word gets refused
// by the credentials service as invalid, with no reason in the wire
// protocol for the Panel to surface (wire.StoreResult carries only an
// Outcome). Checking the shape here, before sending, catches that case
// with an actual explanation and stops the malformed request from being
// sent at all (task 2026-09-23, eleventh first-time visitor, finding 6).
const credentialConsumerPattern=/^[a-z0-9][a-z0-9.-]{0,62}\/[a-z0-9][a-z0-9._-]{0,62}@[1-9][0-9]{0,5}$/;
// Consumers opened as one free-text field with no hint what to type; a
// plain word failed the credentials service's own contract-id pattern
// with no reason (task 2026-09-23, eleventh first-time visitor, finding
// 6). It now lists the credentials module's own known consumers
// (abstraction-credentials/go's generated Consumers var) and every
// inference capability a gateway key can be issued for (each contract
// constant in abstraction-inference/go), each shown by its plain phrase;
// "Other…" reveals the typed field, still checked against the same
// pattern, for a consumer this list does not carry yet (task 2026-09-23,
// twelfth first-time visitor, requirement 1). Each option's own phrase
// reads through consumerServiceName, the same function the saved list
// below already reads a stored consumer id through (metaBox's own
// consumers line), so the two never carry a different word for the same
// consumer (task 2026-09-23, twelfth first-time visitor, finding 5).
const credentialConsumerIds=[
 'abstraction.download/http-execution@1',
 'abstraction.model/resolver@1',
 'abstraction.inference/chat@1',
 'abstraction.inference/embed@1',
 'abstraction.inference/image@1',
 'abstraction.inference/live@1',
 'abstraction.inference/speech@1',
 'abstraction.inference/transcription@1',
];
function renderConsumersSelect(){const select=el('add-consumers-select');select.replaceChildren();for(const id of credentialConsumerIds){const o=document.createElement('option');o.value=id;o.textContent=consumerServiceName(id);o.title=id;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other)}
function consumersSelected(){return Array.from(el('add-consumers-select').selectedOptions||[]).map(o=>o.value)}
el('add-consumers-select').onchange=()=>{el('add-consumers').hidden=!consumersSelected().includes('__other__')};
renderConsumersSelect();
// Store used to say "Stored testcred. List again to see it." while the
// list under it kept its stale rows; it now reads the list again itself
// (task 2026-09-23, fourteenth first-time visitor, findings 2 and 3).
el('add-form').onsubmit=async e=>{e.preventDefault();const secret=el('add-secret').value;el('add-secret').value='';const consumers=consumersSelected().filter(c=>c!=='__other__').concat(commaList(el('add-consumers').value));const badConsumer=consumers.find(c=>!credentialConsumerPattern.test(c));if(badConsumer){show('add-status','Consumers must each be a contract id like abstraction.download/http-execution@1 ('+badConsumer+' is not); separate several with commas.');return}const body={action:'add',name:el('add-name').value,kind:el('add-kind').value,header:el('add-header').value,targets:commaList(el('add-targets').value),consumers,expires:isoOrEmpty(el('add-expires').value),secret,expected_revision:''};try{const r=await call('/credentials',body);if(r.Outcome==='stored'){el('add-form').reset();el('add-consumers').hidden=true;listCursor='';await list();oaShowResult('add-status','Stored '+body.name+'.',r,'Revision '+r.Revision)}else{el('add-status').title='';show('add-status',outcomeStatus(r.Outcome))}}catch(err){oaShowError('add-status',err.message)}};
// Rotate and Revoke's own confirmations used to name a gateway key by its
// raw "local-key.<hash>.<random>" name, with the revision as visible text
// too ("Revoked local-key.7d0c0be6.a1b2 at revision 3-f60fe181."); both
// now read credentialSentenceName's friendly phrase, through oaShowResult,
// with the raw name and revision kept in Technical details (task
// 2026-09-23, thirteenth first-time visitor, finding 6).
// Both used to add "List again before another conditional edit." (Rotate)
// or leave the list stale with no word about it at all (Revoke) while the
// table under them kept its stale rows; both now read the list again
// themselves (task 2026-09-23, fourteenth first-time visitor, findings 2
// and 3).
el('rotate-form').onsubmit=async e=>{e.preventDefault();const secret=el('rotate-secret').value;el('rotate-secret').value='';const name=el('rotate-name').value;const body={action:'rotate',name,expected_revision:el('rotate-revision').value,expires:isoOrEmpty(el('rotate-expires').value),secret};try{const r=await call('/credentials',body);if(r.Outcome==='rotated'){listCursor='';await list();oaShowResult('rotate-status','Rotated '+credentialSentenceName(name)+'.',r,'id: '+name+'; revision '+r.Revision)}else{el('rotate-status').title='';show('rotate-status',outcomeStatus(r.Outcome))}}catch(err){oaShowError('rotate-status',err.message)}};
el('revoke-form').onsubmit=async e=>{e.preventDefault();const name=el('revoke-name').value;const body={action:'revoke',name,expected_revision:el('revoke-revision').value};try{const r=await call('/credentials',body);if(r.Outcome==='revoked'){listCursor='';await list();oaShowResult('revoke-status','Revoked '+credentialSentenceName(name)+'.',r,'id: '+name+'; revision '+r.Revision)}else{el('revoke-status').title='';show('revoke-status',outcomeStatus(r.Outcome))}}catch(err){oaShowError('revoke-status',err.message)}};
let allowRevision='';
const rightsOutcomeText={forbidden:'This panel is not authorized to administer permissions; nothing was changed.',unavailable:'The rights service could not complete this; list again before another edit.',gap:'The policy changed while listing; start again.',invalid:'The rights service rejected this rule as invalid.',conflict:'The policy changed since it was listed; list again before another edit.'};
function rightsOutcomeStatus(o){return rightsOutcomeText[o]||'Rights service outcome: '+o}
// accessSentence reads one abstraction.credentials/apply rule as a sentence,
// the one formatter Save's own confirmation and the granted-rules list
// below both read through. Save's own confirmation used to name the
// program by file name with no account at all; the list below read the
// raw full path and the raw account SID with no shortName or accountLabel
// at all, two different sentences for the same rule (task 2026-09-23,
// fifteenth first-time visitor, finding 4).
function accountLabel(account){return allowSelf&&account===allowSelf.account?'this account':account}
function accessSentence(r){return (r.Permit?'Allowed ':'Denied ')+shortName(r.Subject.Program)+' to apply '+credentialSentenceName(r.Resource.replace(/^credential:/,''))+' for '+accountLabel(r.Subject.Account)+'.'}
// "Show policy" used to write only "Policy revision <rev>. Choose allow or
// deny below." into allow-status, never the policy itself: a click read as
// producing no visible change (task 2026-09-23, fourth first-time visitor,
// finding 3). It now lists every abstraction.credentials/apply rule as its
// own row in allow-rules, the same shape Rights' own rule list already
// gives its rules, instead of joining every sentence into the one status
// line with the "No app has access yet." wording wrapped around them
// (task 2026-09-23, fifteenth first-time visitor, finding 4); the revision
// moves to a title attribute, the same treatment inference_page.go gives
// its own configuration and setting revisions.
// Save's three required fields now name themselves when left blank (task
// 2026-09-23, eighth first-time visitor, finding 4, the Issue key pattern):
// the browser blocks the submit event for a failed required field without
// running this form's own onsubmit, but always fires "invalid" on the
// field, submit or not.
const allowFieldLabel={'allow-name':'Credential name','allow-account':'Account','allow-program':'Program path'};
for(const id in allowFieldLabel)el(id).addEventListener('invalid',()=>show('allow-status',allowFieldLabel[id]+' is required.'));
// App access Save used to refuse ("List rights policy before allowing.")
// unless Show policy had been clicked first; allowPolicyList now reads it
// on arrival too, the same fix every other list on this page and
// elsewhere gets (task 2026-09-23, fourteenth first-time visitor,
// findings 2 and 3).
// allowPolicyList also refreshes the grant widget above the saved list,
// the same fetch its own arrival check makes, so a rule granted since
// this page opened shows up here without a reload (task 2026-09-23,
// fifteenth first-time visitor, finding 1).
async function allowPolicyList(){if(window.oaGrantRefresh)oaGrantRefresh();try{const p=await call('/rights?cursor=');if(p.Outcome!=='page'){allowRevision='';el('allow-status').title='';show('allow-status',rightsOutcomeStatus(p.Outcome));el('allow-rules').replaceChildren();return}allowRevision=p.Revision;el('allow-status').title='Policy revision '+p.Revision;const rules=(p.Rules||[]).filter(r=>r.Action==='abstraction.credentials/apply');await ensureAllowSelf();show('allow-status',rules.length?'':'No app has access yet.');el('allow-rules').replaceChildren();for(const r of rules){const row=document.createElement('p');row.textContent=accessSentence(r);row.title=r.Subject.Program+'; '+r.Subject.Account+'; '+r.Resource;el('allow-rules').append(row)}}catch(e){allowRevision='';el('allow-status').title='';oaShowError('allow-status',e.message)}}
el('allow-list').onclick=allowPolicyList;
var allowPolicyReady=allowPolicyList();
// Program path and Account were bare text boxes here while Rights already
// offered choosers for both; both now build their own equivalent chooser
// from oaProgramChooser and oaAccountChooser (panel_shell.go), the one
// shared implementation each (task 2026-09-23, fourteenth first-time
// visitor, finding 8).
let allowProgramsSeen=new Set();
const allowProgramChooser=oaProgramChooser('allow-program-select','allow-program',()=>[...allowProgramsSeen]);
let allowSelf=null,allowSelfAccountName='',allowSelfPromise=null;
const allowAccountChooser=oaAccountChooser('allow-account-select','allow-account',()=>allowSelf&&allowSelf.account,()=>allowSelfAccountName);
// ensureAllowSelf is accessSentence's own accountLabel, resolved once and
// shared: allowPolicyList awaits it before rendering rows so "this
// account" is already known the first time rules render, not only after
// loadAllowChoosers happens to resolve first (rights_page.go's own
// ensureSelf, the same fix, task 2026-09-23, fifteenth first-time
// visitor, finding 4).
function ensureAllowSelf(){if(!allowSelfPromise)allowSelfPromise=call('/explore').then(v=>{allowSelf=v.self;allowSelfAccountName=v.selfAccountName||'';allowAccountChooser.rebuild()}).catch(()=>{});return allowSelfPromise}
async function loadAllowChoosers(){try{const v=await call('/card/table');for(const r of(v.resources||[]))for(const h of(r.holders||[]))if(h.program)allowProgramsSeen.add(h.program)}catch(e){}allowProgramChooser.rebuild();await ensureAllowSelf()}
allowProgramChooser.rebuild();allowAccountChooser.rebuild();var allowChoosersReady=loadAllowChoosers();
// An applied allow/deny used to print "Allowed C:\test\app.exe to apply
// huggingface at revision sha256:…", the full program path and the revision
// both visible text; it now names the program by its file name, and the
// revision sits on the line's own title and in Technical details,
// oaShowResult's own shape (task 2026-09-23, tenth first-time visitor,
// finding 1). It used to add "List rights policy again before another
// edit." while the policy list under it kept its stale state; it now
// reads the policy list again itself (task 2026-09-23, fourteenth
// first-time visitor, findings 2 and 3).
el('allow-form').onsubmit=async e=>{e.preventDefault();const name=el('allow-name').value,program=el('allow-program').value,account=el('allow-account').value,used=allowRevision,permit=el('allow-permit').value==='permit';const resource='credential:'+name;const body={edit:'set',revision:allowRevision,account,program,action:'abstraction.credentials/apply',resource,permit};try{const r=await call('/rights',body);if(r.Outcome==='applied'){await allowPolicyList();oaShowResult('allow-status',accessSentence({Permit:permit,Subject:{Program:program,Account:account},Resource:resource}),r,'Program: '+program+'; Account: '+account+'; id: '+name+'; revision '+used)}else{el('allow-status').title='';show('allow-status',rightsOutcomeStatus(r.Outcome))}}catch(err){oaShowError('allow-status',err.message)}};
</script>`
