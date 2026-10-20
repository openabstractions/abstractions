package main

// credentialsPage is the Panel's Credentials page. Its words follow
// research/vocabulary/DECISION.md §4: Saved credentials, Add credential
// (credentials row 9), Allow and Block (D53); the rule it edits keeps its
// wire action abstraction.credentials/apply.
//
// Its shape follows the Panel redesign (research/panel-redesign/SPEC.md §4,
// slice S8): one caption, "Keys your apps use for hosted services."; every
// field is a chooser except the secret itself. Add picks the hosted model
// server a key is for (its credential name and address come from the
// server's own entry), each saved credential carries its own "Replace key…"
// and "Remove" (a second click confirms), and a typed name or address appears
// only behind "Other…". Every button writes one sentence into its row's status line.
var credentialsPage = oaPageHead("credentials", "Credentials", "Keys your apps use for hosted services.") + `<div data-ready-owner="page" hidden></div>` +
	`<section id="saved" class="oa-group"><h2>Saved credentials</h2>` + oaStatus("list-status") + `<p class="oa-empty" id="records-empty" hidden>No credentials registered.</p><div id="records"></div>` +
	oaRow("Storage", "", `<button id="list-next" type="button" hidden>Show more</button>`, "limits") + `</section>
<section id="new" class="oa-group"><h2>Add credential</h2>` +
	oaRow("Add a credential", "", `<button id="add-open" type="button">Add a credential…</button>`, "add-open-status") +
	`<form id="add-form" hidden>` +
	oaRow("For", "", `<select id="add-for" aria-label="For"></select><input id="add-name" aria-label="Name" placeholder="Name" data-oa-other hidden><input id="add-targets" aria-label="Address" placeholder="Address, like api.example.com" data-oa-other hidden>`, "") +
	oaRow("Kind", "", `<select id="add-kind" aria-label="Kind"><option value="bearer">Bearer token</option><option value="header">Header</option></select><input id="add-header" aria-label="Header name" value="X-Api-Key" hidden>`, "") +
	oaRow("Used for", "", `<span id="add-consumers-select" aria-label="Used for"></span><input id="add-consumers" aria-label="Other uses" data-oa-other hidden>`, "add-consumers-status") +
	oaRow("Expires", "", `<select id="add-expires" aria-label="Expires"><option value="">Never</option><option value="30">In 30 days</option><option value="90">In 90 days</option><option value="365">In a year</option></select>`, "") +
	oaRow("Secret", "", `<input id="add-secret" type="password" autocomplete="off" required aria-label="Secret" data-oa-secret>`, "") +
	oaRow("Save", "", `<button id="add-button" type="submit" class="oa-primary">Add</button>`, "add-status") + `</form></section>
<section id="access" class="oa-group"><h2>App access</h2><p class="oa-caption">Which apps may use a credential.</p>` + oaStatus("allow-status") + `<div id="allow-rules"></div>` +
	oaRow("Set app access", "", `<button id="allow-open" type="button">Set app access…</button>`, "allow-open-status") +
	`<form id="allow-form" hidden>` +
	oaRow("Credential", "", `<select id="allow-name-select" aria-label="Credential name"></select><input id="allow-name" aria-label="Credential name" data-oa-other hidden>`, "") +
	oaRow("Account", "", `<select id="allow-account-select" aria-label="Account"></select><input id="allow-account" aria-label="Account" data-oa-other hidden>`, "") +
	oaRow("App", "", `<select id="allow-program-select" aria-label="App"></select><input id="allow-program" aria-label="App" data-oa-other hidden>`, "") +
	oaRow("Access", "", `<select id="allow-permit" aria-label="Allow or block"><option value="permit">Allow</option><option value="deny">Block</option></select>`, "") +
	oaRow("Save this rule", "", `<button id="allow-button" type="submit">Save</button>`, "allow-save-status") + `</form></section>
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);
function show(id,x){oaSetStatus(id,'ok',typeof x==='string'?x:JSON.stringify(x,null,2))}
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function shortName(path){return oaShortName(path)}
function commaList(v){return v.split(',').map(x=>x.trim()).filter(x=>x)}
// expiresIn reads an Expires choice (Never, or a number of days from now)
// as the wire's ISO time, or '' for never.
function expiresIn(days){if(!days)return '';return new Date(Date.now()+Number(days)*864e5).toISOString()}
const outcomeText={forbidden:'This panel is not authorized for credentials; nothing changed.',unavailable:'The credentials service could not complete this; list again before another conditional edit.',gap:'The listing changed; start again.',invalid:'The credentials service rejected this as invalid.',no_secure_store:'This runtime has no configured platform store; nothing was stored.',exhausted:'This account already holds its maximum number of credentials.',unsupported_kind:'This runtime\'s applier does not support that kind.',unknown:'No credential has that name.',conflict:'The listed revision is stale; list again before retrying.'};
function outcomeStatus(o){return outcomeText[o]||'Credentials service outcome: '+o}
// storeLabel names the platform store in words a reader recognizes, for the
// wire words Limits.SecureStore carries (abstraction-credentials/go/backend.go);
// an unrecognized future word still shows, unchanged, rather than disappearing.
function storeLabel(s){const known={'windows-dpapi-file':'an encrypted file for this Windows account','windows-credential-manager':'Windows Credential Manager','macos-login-keychain':'the macOS login Keychain','secret-service':'the desktop secret service','file-0600':'a local file, restricted to this account','macos-keychain':'macOS Keychain','none':'no platform store'};return known[s]||s}
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
// credentialRow is one saved credential as an oa-row: its name, a caption
// (state, who registered it, what uses it), Technical details, the row
// actions "Replace key…" and "Remove", an editor for the new secret, and
// the row's own status line. updateCredentialRow refreshes it in place, so
// a reread keeps the row, its sentence and focus (Panel rules, S8 review).
function credentialRow(m){
 const row=document.createElement('div');row.className='oa-row';
 const text=document.createElement('div');text.className='oa-row-text';
 const label=document.createElement('span');label.className='oa-row-label';
 const caption=document.createElement('span');caption.className='oa-row-caption';
 const details=oaTechnical(document.createElement('details'));details.className='oa-expander';const dsummary=document.createElement('summary');dsummary.textContent='Technical details';const pre=document.createElement('pre');details.append(dsummary,pre);
 text.append(label,caption,details);
 const control=document.createElement('div');control.className='oa-row-control';
 const replace=document.createElement('button');replace.type='button';replace.textContent='Replace key…';
 const remove=document.createElement('button');remove.type='button';remove.textContent='Remove';
 control.append(replace,remove);
 const editor=document.createElement('div');editor.className='oa-row-control';editor.hidden=true;
 const secret=document.createElement('input');secret.type='password';secret.autocomplete='off';if(secret.setAttribute){secret.setAttribute('aria-label','New secret');secret.setAttribute('data-oa-secret','')}
 const expires=document.createElement('select');for(const[v,t]of[['','Never'],['30','In 30 days'],['90','In 90 days'],['365','In a year']]){const o=document.createElement('option');o.value=v;o.textContent=t;expires.append(o)}
 const save=document.createElement('button');save.type='button';save.textContent='Save';
 const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel';
 editor.append(secret,expires,save,cancel);
 const status=document.createElement('p');status.className='oa-status';if(status.setAttribute){status.setAttribute('role','status');status.setAttribute('aria-live','polite')}
 row.append(text,control,editor,status);
 Object.assign(row,{label,caption,pre,replace,remove,editor,secret,expires,save,cancel,status});
 const say=(t,state)=>{status.textContent=t;if(status.setAttribute)status.setAttribute('data-state',state||'ok')};
 row.say=say;
 replace.onclick=()=>{editor.hidden=!editor.hidden;say(editor.hidden?'Nothing replaced.':'Type the new key, then Save.');if(!editor.hidden&&secret.focus)secret.focus()};
 cancel.onclick=()=>{secret.value='';editor.hidden=true;say('Nothing replaced.')};
 save.onclick=()=>credentialReplace(row);
 remove.onclick=()=>credentialRemove(row);
 return row}
function updateCredentialRow(row,m){
 row.record=m;
 row.label.textContent=metaBoxName(m);
 const consumers=(m.Scope.Consumers||[]).map(consumerServiceName);
 row.caption.textContent=m.State+' · registered by '+shortName(m.RegisteredBy.Program)+' '+oaWhen(m.Registered)+(consumers.length?' · used by '+consumers.join(', '):'');
 row.caption.title='id: '+m.Name+'; '+m.RegisteredBy.Program+'; registered '+m.Registered;
 row.pre.textContent=JSON.stringify(m,null,2);
 const active=m.State!=='revoked';row.replace.hidden=row.remove.hidden=!active;if(!active)row.editor.hidden=true}
// Replace key… sends the new secret at the revision this row last read.
async function credentialReplace(row){const m=row.record,secret=row.secret.value;row.secret.value='';if(!secret){row.say('Type the new key first.');return}
 try{const r=await call('/credentials',{action:'rotate',name:m.Name,expected_revision:m.Revision,expires:expiresIn(row.expires.value),secret});
  if(r.Outcome==='rotated'){row.editor.hidden=true;await list();row.say('Replaced the key for '+credentialSentenceName(m.Name)+'.');row.status.title='id: '+m.Name+'; revision '+r.Revision}
  else row.say(outcomeStatus(r.Outcome))}catch(e){row.say(oaPlain(e.message).line,'error')}}
// Remove asks for a second click within ten seconds, then revokes.
async function credentialRemove(row){const m=row.record;
 if(!row.armed||Date.now()-row.armed>10000){row.armed=Date.now();row.say('Click Remove again to delete '+credentialSentenceName(m.Name)+'.');return}
 row.armed=0;
 try{const r=await call('/credentials',{action:'revoke',name:m.Name,expected_revision:m.Revision});
  if(r.Outcome==='revoked'){await list();row.say('Removed '+credentialSentenceName(m.Name)+'.');row.status.title='id: '+m.Name+'; revision '+r.Revision}
  else row.say(outcomeStatus(r.Outcome))}catch(e){row.say(oaPlain(e.message).line,'error')}}
// App access names a credential through a chooser of the listed names,
// the friendly label as option text and the raw name as its value;
// "Other…" takes a typed name.
let credentialNames=new Map();
const nameChoosers=[['allow-name-select','allow-name']];
function nameChooserSync(selectId,inputId){const v=el(selectId).value;if(v==='__other__'){el(inputId).hidden=false;if(credentialNames.has(el(inputId).value))el(inputId).value=''}else{el(inputId).hidden=true;el(inputId).value=v}}
function rebuildNameChooser(selectId,inputId){const select=el(selectId);const current=select.value;select.replaceChildren();for(const[name,label]of credentialNames){const o=document.createElement('option');o.value=name;o.textContent=label;o.title=name;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);const names=[...credentialNames.keys()];select.value=current&&(credentialNames.has(current)||current==='__other__')?current:(names[0]||'__other__');nameChooserSync(selectId,inputId)}
function selectCredentialName(selectId,inputId,name){el(selectId).value=credentialNames.has(name)?name:'__other__';nameChooserSync(selectId,inputId);if(!credentialNames.has(name))el(inputId).value=name}
for(const[selectId,inputId] of nameChoosers){el(selectId).onchange=()=>nameChooserSync(selectId,inputId);rebuildNameChooser(selectId,inputId)}
// An unchanged reading leaves the saved list, its sentences and focus as
// they are.
let credentialsLast='';
// loadKeyPrograms reads Inference's own key list once, best-effort: a
// Panel without inference key issue still lists credentials, just without
// a gateway key's own program name filled in (it falls back to "Gateway
// key" alone).
let credentialKeyPrograms=null;
async function loadKeyPrograms(){if(credentialKeyPrograms)return;try{const r=await call('/inference/keys');if(r.Outcome==='page')credentialKeyPrograms=new Map(r.Keys.map(k=>[k.Name,k.Program]))}catch(e){}}
// list reads as many pages as are shown (Show more adds one), so a reread
// past the first page keeps every shown row current.
let listPages=1;
async function list(){try{await loadKeyPrograms();let cursor='',records=[],p,pages=0;do{p=await call('/credentials?cursor='+encodeURIComponent(cursor));if(p.Outcome!=='page'){show('list-status',outcomeStatus(p.Outcome));el('list-next').hidden=true;return}records.push(...p.Records);cursor=p.Next;pages++}while(!p.Complete&&pages<listPages);
 const sig=JSON.stringify([records,p.Complete,p.Limits]);if(sig===credentialsLast)return;credentialsLast=sig;
 if(el('list-status').getAttribute&&el('list-status').getAttribute('data-state')==='loading')oaSetStatus('list-status','ok','');
 el('list-next').hidden=!!p.Complete;show('limits',limitsText(p.Limits));el('records-empty').hidden=!!records.length;
 oaKeyedRows(el('records'),records,m=>m.Name,credentialRow,updateCredentialRow);
 for(const m of records)credentialNames.set(m.Name,metaBoxName(m));for(const[selectId,inputId] of nameChoosers)rebuildNameChooser(selectId,inputId)}catch(e){oaShowError('list-status',e.message)}}
el('list-next').onclick=()=>{listPages++;return list()};
var listReady=list();
// Every required field across Add, Rotate and Revoke now names itself when
// left blank (task 2026-09-23, seventh first-time visitor, finding 4, the
// Issue key pattern): the browser blocks the submit event for a failed
// required field without running the form's own onsubmit, but always
// fires "invalid" on the field, submit or not.
const requiredFieldLabel={'add-name':'Name','add-targets':'Targets','add-consumers-select':'Consumers','add-secret':'Secret'};
for(const id in requiredFieldLabel)el(id).addEventListener('invalid',()=>show('add-status',requiredFieldLabel[id]+' is required.'));
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
// Used for is a set of toggle buttons (aria-pressed), chat on by default;
// Other… shows a typed field. Each press writes one sentence.
function consumerToggle(value,label,title,pressed){const b=document.createElement('button');b.type='button';b.value=value;b.textContent=label;if(title)b.title=title;const set=v=>{b.pressed=v;if(b.setAttribute)b.setAttribute('aria-pressed',String(v))};set(pressed);b.onclick=()=>{set(!b.pressed);if(value==='__other__')el('add-consumers').hidden=!b.pressed;oaSetStatus('add-consumers-status','ok',label+(b.pressed?' on.':' off.'))};return b}
function renderConsumersSelect(){const box=el('add-consumers-select');box.replaceChildren();for(const id of credentialConsumerIds)box.append(consumerToggle(id,consumerServiceName(id),id,id==='abstraction.inference/chat@1'));box.append(consumerToggle('__other__','Other…','',false))}
function consumersSelected(){return [...el('add-consumers-select').children].filter(b=>b.pressed).map(b=>b.value)}
// Add a credential… shows the form; nothing is typed until the person asks.
el('add-form').hidden=true;
el('allow-form').hidden=true;
el('allow-open').onclick=()=>{const f=el('allow-form');f.hidden=!f.hidden;oaSetStatus('allow-open-status','ok',f.hidden?'The form is closed.':'Choose a credential and an app, then Save.')};
el('add-open').onclick=()=>{const f=el('add-form');f.hidden=!f.hidden;oaSetStatus('add-open-status','ok',f.hidden?'The form is closed.':'Fill in the form below, then Add.')};
renderConsumersSelect();
// Store used to say "Stored testcred. List again to see it." while the
// list under it kept its stale rows; it now reads the list again itself
// (task 2026-09-23, fourteenth first-time visitor, findings 2 and 3).
el('add-form').onsubmit=async e=>{e.preventDefault();const secret=el('add-secret').value;el('add-secret').value='';const consumers=consumersSelected().filter(c=>c!=='__other__').concat(commaList(el('add-consumers').value));const badConsumer=consumers.find(c=>!credentialConsumerPattern.test(c));if(badConsumer){show('add-status','Consumers must each be a contract id like abstraction.download/http-execution@1 ('+badConsumer+' is not); separate several with commas.');return}if(!el('add-name').value.trim()||!el('add-targets').value.trim()){show('add-status',(el('add-name').value.trim()?'Address':'Name')+' is required.');return}const body={action:'add',name:el('add-name').value.trim(),kind:el('add-kind').value,header:el('add-kind').value==='header'?el('add-header').value.trim():'',targets:commaList(el('add-targets').value),consumers,expires:expiresIn(el('add-expires').value),secret,expected_revision:''};try{const r=await call('/credentials',body);if(r.Outcome==='stored'){el('add-form').reset();renderConsumersSelect();el('add-consumers').hidden=true;el('add-header').hidden=true;addForSync();await list();oaShowResult('add-status','Stored '+body.name+'.',r,'Revision '+r.Revision)}else{el('add-status').title='';show('add-status',outcomeStatus(r.Outcome))}}catch(err){oaShowError('add-status',err.message)}};
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
let allowRevision='',allowLast='';
const rightsOutcomeText={forbidden:'This panel is not authorized to administer permissions; nothing was changed.',unavailable:'The rights service could not complete this; list again before another edit.',gap:'The policy changed while listing; start again.',invalid:'The rights service rejected this rule as invalid.',conflict:'The policy changed since it was listed; list again before another edit.'};
function rightsOutcomeStatus(o){return rightsOutcomeText[o]||'Rights service outcome: '+o}
// accessSentence reads one abstraction.credentials/apply rule as a sentence,
// the one formatter Save's own confirmation and the granted-rules list
// below both read through. Save's own confirmation used to name the
// program by file name with no account at all; the list below read the
// raw full path and the raw account SID with no shortName or accountLabel
// at all, two different sentences for the same rule (task 2026-09-23,
// fifteenth first-time visitor, finding 4).
// An account other than this one reads as "another account on this device";
// its security identifier stays on the row's title, never as text.
function accountLabel(account){return allowSelf&&account===allowSelf.account?'this account':'another account on this device'}
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
for(const id in allowFieldLabel)el(id).addEventListener('invalid',()=>show('allow-save-status',allowFieldLabel[id]+' is required.'));
// App access Save used to refuse ("List rights policy before allowing.")
// unless Show policy had been clicked first; allowPolicyList now reads it
// on arrival too, the same fix every other list on this page and
// elsewhere gets (task 2026-09-23, fourteenth first-time visitor,
// findings 2 and 3).
async function allowPolicyList(){try{const p=await call('/rights?cursor=');if(p.Outcome!=='page'){allowRevision='';el('allow-status').title='';show('allow-status',rightsOutcomeStatus(p.Outcome));el('allow-rules').replaceChildren();return}{const sig=JSON.stringify(p);if(sig===allowLast)return;allowLast=sig}allowRevision=p.Revision;el('allow-status').title='Policy revision '+p.Revision;const rules=(p.Rules||[]).filter(r=>r.Action==='abstraction.credentials/apply');await ensureAllowSelf();show('allow-status',rules.length?'':'No app has access yet.');el('allow-rules').replaceChildren();for(const r of rules){const row=document.createElement('p');row.textContent=accessSentence(r);row.title=r.Subject.Program+'; '+r.Subject.Account+'; '+r.Resource;el('allow-rules').append(row)}}catch(e){allowRevision='';el('allow-status').title='';oaShowError('allow-status',e.message)}}
var allowPolicyReady=allowPolicyList();
// Program path and Account were bare text boxes here while Rights already
// offered choosers for both; both now build their own equivalent chooser
// from oaProgramChooser and oaAccountChooser (panel_shell.go), the one
// shared implementation each (task 2026-09-23, fourteenth first-time
// visitor, finding 8). The program chooser lists registered applications.
const allowProgramChooser=oaProgramChooser('allow-program-select','allow-program');
let allowSelf=null,allowSelfAccountName='',allowSelfPromise=null;
const allowAccountChooser=oaAccountChooser('allow-account-select','allow-account',()=>allowSelf&&allowSelf.account,()=>allowSelfAccountName);
// ensureAllowSelf is accessSentence's own accountLabel, resolved once and
// shared: allowPolicyList awaits it before rendering rows so "this
// account" is already known the first time rules render, not only after
// loadAllowChoosers happens to resolve first (rights_page.go's own
// ensureSelf, the same fix, task 2026-09-23, fifteenth first-time
// visitor, finding 4).
function ensureAllowSelf(){if(!allowSelfPromise)allowSelfPromise=call('/explore').then(v=>{allowSelf=v.self;allowSelfAccountName=v.selfAccountName||'';allowAccountChooser.rebuild()}).catch(()=>{});return allowSelfPromise}
async function loadAllowChoosers(){await allowProgramChooser.ready;await ensureAllowSelf()}
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
el('allow-form').onsubmit=async e=>{e.preventDefault();const name=el('allow-name').value,program=el('allow-program').value,account=el('allow-account').value,used=allowRevision,permit=el('allow-permit').value==='permit';const resource='credential:'+name;const body={edit:'set',revision:allowRevision,account,program,action:'abstraction.credentials/apply',resource,permit};try{const r=await call('/rights',body);if(r.Outcome==='applied'){await allowPolicyList();oaShowResult('allow-save-status',accessSentence({Permit:permit,Subject:{Program:program,Account:account},Resource:resource}),r,'Program: '+program+'; Account: '+account+'; id: '+name+'; revision '+used)}else{el('allow-save-status').title='';show('allow-save-status',rightsOutcomeStatus(r.Outcome))}}catch(err){oaShowError('allow-save-status',err.message)}};
// For lists the hosted model servers (Providers' server list); choosing one
// takes its credential name and its address. A key for anything else takes
// a typed name and address behind "Other…".
function addressHost(base){try{return new URL(base).hostname}catch(e){const m=/^[a-z]+:\/\/([^/:]+)/i.exec(String(base||''));return m?m[1]:''}}
async function loadAddFor(){let hosted=[];try{const l=await call('/inference/hosts');if(l&&l.Outcome==='page')hosted=(l.Hosts||[]).map(h=>h.Entry).filter(e=>e&&e.Hosted)}catch(e){}
 const select=el('add-for'),current=select.value;select.replaceChildren();
 for(const e of hosted){const o=document.createElement('option');o.value=JSON.stringify({name:e.Credential||e.Name,target:addressHost(e.Base)});o.textContent=e.Name;select.append(o)}
 const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);
 const values=hosted.map(e=>JSON.stringify({name:e.Credential||e.Name,target:addressHost(e.Base)}));select.value=values.includes(current)?current:(values[0]||'__other__');addForSync()}
function addForSync(){const v=el('add-for').value,other=!v||v==='__other__';el('add-name').hidden=el('add-targets').hidden=!other;if(other){el('add-name').value='';el('add-targets').value=''}else{const c=JSON.parse(v);el('add-name').value=c.name;el('add-targets').value=c.target}}
el('add-for').onchange=addForSync;
el('add-kind').onchange=()=>{el('add-header').hidden=el('add-kind').value!=='header'};
var addForReady=loadAddFor();
// The page tells the screenshot tool it is ready once its first readings
// have finished (S1's oaReady; data-ready until it exists).
function oaPageReady(){if(typeof oaReady==='function')oaReady();else if(typeof document!=='undefined'&&document.documentElement)document.documentElement.dataset.ready='1'}
Promise.all([listReady,allowPolicyReady,addForReady]).then(oaPageReady,oaPageReady);
// The page updates itself while it is visible; there is no Refresh button.
// oaQuiet runs one reading at a time: a timer tick while one is still
// running is skipped.
function oaQuietOnce(fn){let busy=false;return async()=>{if(busy)return;busy=true;try{await fn()}finally{busy=false}}}
const credentialsQuiet=oaQuietOnce(()=>Promise.all([list(),allowPolicyList()]));
window.credentialsTick=credentialsQuiet;
if(typeof setInterval==='function')setInterval(()=>{if(document.visibilityState!=='hidden')credentialsQuiet()},15000);
</script>`
