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
	`<div id="credentials-panel-title" data-title="` + oaPanelTitle + `" hidden></div>` +
	connectSection + credentialEditorStyle +
	`<section id="saved" data-runtime-echo class="oa-group"><h2>Saved credentials</h2>` + oaStatus("list-status") + `<p class="oa-empty" id="records-empty" hidden>No credentials registered.</p><div id="records"></div>` +
	oaRow("Storage", "", `<button id="list-next" type="button" hidden>Show more</button>`, "limits") + `</section>
<section id="new" class="oa-group"><h2>Add credential</h2>` +
	oaRow("Add a credential", "", `<button id="add-open" type="button">Add a credential…</button>`, "add-open-status") +
	`<dialog id="credential-dialog" data-credential-editor aria-labelledby="credential-title"><form id="add-form" hidden>
<h2 id="credential-title">Add credential</h2><p>Save a key without granting any app permission to use it.</p>
<label for="add-for">Connection preset</label><select id="add-for" aria-label="Connection preset"></select>
<label for="add-name">Name (required)</label><input id="add-name" aria-label="Name" required autocomplete="off" spellcheck="false" data-oa-other><small>A unique name: letters, numbers, dots, dashes or underscores.</small>
<label for="add-secret">Secret (required)</label><div><input id="add-secret" type="password" autocomplete="off" required aria-label="Secret" data-oa-secret><button id="add-reveal" type="button" aria-pressed="false">Show secret</button></div>
<label for="add-targets">Allowed host names (required)</label><input id="add-targets" aria-label="Allowed host names" required spellcheck="false" data-oa-other><small>Only these hosts may receive this key. Separate several with commas.</small>
<label for="add-expires">Expires</label><select id="add-expires" aria-label="Expires"><option value="">Never</option><option value="date">On a date…</option><option value="30">In 30 days</option><option value="90">In 90 days</option><option value="365">In a year</option></select>
<label id="add-expiry-label" for="add-expiry-date" hidden>Date and time (your local time zone)</label><input id="add-expiry-date" type="datetime-local" aria-label="Expiry date and time in your local time zone" hidden>
<details><summary id="add-scope-summary">Key type and uses: Bearer · chat</summary><label for="add-kind">Send the key as</label><select id="add-kind" aria-label="Send the key as"><option value="bearer">Authorization: Bearer (API token)</option><option value="header">A custom HTTP header</option></select><label id="add-header-label" for="add-header" hidden>HTTP header name</label><input id="add-header" aria-label="Header name" value="X-Api-Key" hidden>
<p>Allowed uses (at least one required)</p><small>These restrict the key’s use. App access is granted separately.</small><div id="add-consumers-select" aria-label="Allowed uses"></div><input id="add-consumers" aria-label="Other use contract IDs" data-oa-other hidden>` + oaStatusLine("add-consumers-status", false) + `</details>
<div><button id="add-button" type="submit" class="oa-primary">Save credential</button><button id="add-cancel" type="button">Cancel</button></div>` + oaStatusLine("add-status", false) + `</form></dialog></section>
<section id="access" class="oa-group"><h2>App access</h2><p class="oa-caption">Which apps may use a credential.</p>` + oaStatus("allow-status") + `<div id="allow-rules"></div>` +
	oaRow("Set app access", "", `<button id="allow-open" type="button">Set app access…</button>`, "allow-open-status") +
	`<form id="allow-form" hidden>` +
	oaRow("Credential", "", `<select id="allow-name-select" aria-label="Credential name"></select><input id="allow-name" aria-label="Credential name" data-oa-other hidden>`, "") +
	oaRow("Account", "", `<select id="allow-account-select" aria-label="Account"></select><input id="allow-account" aria-label="Account" data-oa-other hidden>`, "") +
	oaRow("App", "", `<select id="allow-program-select" aria-label="App"></select><input id="allow-program" aria-label="App program path" placeholder="Full program path" data-oa-other hidden>`, "") +
	oaRow("Access", "", `<select id="allow-permit" aria-label="Allow or block"><option value="permit">Allow</option><option value="deny">Block</option></select>`, "") +
	oaRow("Save this rule", "", `<button id="allow-button" type="submit">Save</button>`, "allow-save-status") + `</form></section>
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);
// This Panel's own name (program_titles.go), for the program that saved a
// key from here; the markup carries it.
const credentialPanelTitle=(el('credentials-panel-title')||{}).dataset&&el('credentials-panel-title').dataset.title||'';
function show(id,x){oaSetStatus(id,'ok',typeof x==='string'?x:JSON.stringify(x,null,2))}
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function shortName(path){return oaShortName(path)}
function commaList(v){return v.split(',').map(x=>x.trim()).filter(x=>x)}
// expiresIn reads an Expires choice (Never, or a number of days from now)
// as the wire's ISO time, or '' for never.
function expiresIn(days){if(!days)return '';return new Date(Date.now()+Number(days)*864e5).toISOString()}
function credentialExpiryChoice(){if(el('add-expires').value!=='date')return expiresIn(el('add-expires').value);const value=el('add-expiry-date').value,date=new Date(value);if(!value||!Number.isFinite(date.getTime())||date.getTime()<=Date.now())throw Error('Choose a future expiry date and time in your local time zone.');return date.toISOString()}
el('add-expires').onchange=()=>{const custom=el('add-expires').value==='date';el('add-expiry-date').hidden=el('add-expiry-label').hidden=!custom;el('add-expiry-date').required=custom};
el('add-reveal').onclick=()=>{const input=el('add-secret'),shown=input.type==='password';input.type=shown?'text':'password';el('add-reveal').textContent=shown?'Hide secret':'Show secret';if(el('add-reveal').setAttribute)el('add-reveal').setAttribute('aria-pressed',String(shown))};
const outcomeText={forbidden:'This panel is not authorized for credentials; nothing changed.',unavailable:'The credentials service could not complete this; list again before another conditional edit.',gap:'The listing changed; start again.',invalid:'The credentials service rejected this as invalid.',no_secure_store:'OpenAbstractions has no configured platform store; nothing was stored.',exhausted:'This account already holds its maximum number of credentials.',unsupported_kind:'OpenAbstractions does not support that kind of credential.',unknown:'No credential has that name.',conflict:'The listed revision is stale; list again before retrying.'};
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
const credentialServices=new Map(),credentialApps=new Map();
function appName(program){return credentialApps.get(program)||(/^remote:[0-9a-f]{64}$/.test(program)?'Paired computer':shortName(program))||'Unknown app'}
// A saved key for a service reads by the service's label ("DeepSeek"),
// found by the one address the key may be sent to; a person who keeps keys
// for two accounts of one service reads the name they gave each key
// ("DeepSeek (work)"). OpenAbstractions' own and generated key names never
// show (architect ruling C, 2026-11-08).
function credentialService(m){if(!m||m.Kind==='openabstractions/local-key@1'||!m.Scope||m.Scope.Targets.length!==1)return null;for(const service of credentialServices.values())if(service.target===m.Scope.Targets[0])return service;return null}
function personKeyName(name,service){return !!name&&name!==service.credential&&name.slice(0,3)!=='oa-'&&!/^[a-z0-9][a-z0-9_-]*-[a-z2-7]{13}$/.test(name)}
let credentialRecords=[];
function metaBoxName(m){if(m.Kind!=='openabstractions/local-key@1'){const service=credentialService(m);if(!service)return m.Name;const accounts=credentialRecords.filter(r=>r.State!=='revoked'&&credentialService(r)===service);return accounts.length>1&&personKeyName(m.Name,service)?service.label+' ('+m.Name+')':service.label}const program=credentialKeyPrograms&&credentialKeyPrograms.get(m.Name);return program?'Gateway key for '+appName(program):'Gateway key'}
// credentialSentenceName reads the same gateway-key program metaBoxName
// does, worded for a sentence position ("Revoked the gateway key for
// testapp.exe.") rather than metaBoxName's own heading position ("Gateway
// key for testapp.exe"); an ordinary credential's own name is already the
// name a person chose, so it reads unchanged either way (task 2026-09-23,
// thirteenth first-time visitor, finding 6).
function credentialSentenceName(name){const program=credentialKeyPrograms&&credentialKeyPrograms.get(name);return program?'the gateway key for '+appName(program):(credentialNames.get(name)||name)}
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
 const details=oaTechnical(document.createElement('details'));details.className='oa-expander';const dsummary=document.createElement('summary');dsummary.textContent='Technical details';const chevron=document.createElement('span');chevron.className='oa-row-chevron';if(chevron.setAttribute)chevron.setAttribute('aria-hidden','true');dsummary.append(chevron);const dbody=document.createElement('div');dbody.className='oa-expander-body';const pre=document.createElement('pre');dbody.append(pre);details.append(dsummary,dbody);
 text.append(label,caption,details);
 const control=document.createElement('div');control.className='oa-row-control';
 const replace=document.createElement('button');replace.type='button';replace.textContent='Replace key…';
 const remove=document.createElement('button');remove.type='button';remove.textContent='Remove';
 const access=document.createElement('button');access.type='button';access.textContent='App access…';
 const setupQuestions=document.createElement('a');setupQuestions.textContent='Catalog questions…';setupQuestions.href='/rights?k='+encodeURIComponent(key)+'#questions';setupQuestions.hidden=true;
 control.append(access,replace,remove,setupQuestions);
 const editor=document.createElement('div');editor.className='oa-row-control';editor.hidden=true;
 const secret=document.createElement('input');secret.type='password';secret.autocomplete='off';if(secret.setAttribute){secret.setAttribute('aria-label','New secret');secret.setAttribute('data-oa-secret','')}
 const expires=document.createElement('select');for(const[v,t]of[['','Never'],['30','In 30 days'],['90','In 90 days'],['365','In a year']]){const o=document.createElement('option');o.value=v;o.textContent=t;expires.append(o)}
 const save=document.createElement('button');save.type='button';save.textContent='Save';
 const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel';
 editor.append(secret,expires,save,cancel);
 const status=document.createElement('p');status.className='oa-status';if(status.setAttribute){status.setAttribute('role','status');status.setAttribute('aria-live','polite')}
 row.append(text,control,editor,status);
 Object.assign(row,{label,caption,details,pre,access,replace,remove,setupQuestions,editor,secret,expires,save,cancel,status});
 const say=(t,state)=>{status.textContent=t;if(status.setAttribute)status.setAttribute('data-state',state||'ok')};
 row.say=say;
 access.onclick=()=>{selectCredentialName('allow-name-select','allow-name',row.record.Name);el('allow-form').hidden=false;say('Choose an app and Allow or Block below, then Save.');if(el('allow-form').scrollIntoView)el('allow-form').scrollIntoView({block:'nearest'});if(el('allow-program-select').focus)el('allow-program-select').focus()};
 replace.onclick=()=>{editor.hidden=!editor.hidden;if(editor.hidden)secret.value='';say(editor.hidden?'Nothing replaced.':'Type the new key, then Save.');if(!editor.hidden&&secret.focus)secret.focus()};
 cancel.onclick=()=>{secret.value='';editor.hidden=true;say('Nothing replaced.')};
 save.onclick=()=>credentialReplace(row);
 remove.onclick=()=>credentialRemove(row);
 return row}
// A row's caption is when the key was connected and which apps may use it,
// on one line. Technical details shows its name, who saved it and the
// services it is scoped to.
const credentialStateWords={expired:'Expired',revoked:'Removed'};
function credentialUsers(m){if(credentialAccessRules===undefined)return '';if(credentialAccessRules===null)return 'App access could not be read';const apps=[];for(const r of credentialAccessRules)if(r.Permit&&r.Resource==='credential:'+m.Name&&!ownProgram(r.Subject.Program)){const name=appName(r.Subject.Program);if(!apps.includes(name))apps.push(name)}return apps.length?'Used by '+apps.join(', '):'Not used by any app yet'}
function credentialCaption(m){const gateway=m.Kind==='openabstractions/local-key@1',parts=[];if(credentialStateWords[m.State])parts.push(credentialStateWords[m.State]);parts.push((gateway?'Issued ':credentialService(m)?'Connected ':'Saved ')+oaWhen(m.Registered));const users=gateway||m.State==='revoked'?'':credentialUsers(m);if(users)parts.push(users);return parts.join(' · ')}
function credentialTechnical(m){const consumers=m.Scope.Consumers||[];return ['Name: '+m.Name,'Kind: '+m.Kind+(m.Header?' ('+m.Header+')':''),'Address: '+(m.Scope.Targets||[]).join(', '),'Services: '+consumers.map(id=>consumerServiceName(id)+' ('+id+')').join(', '),'Registered by: '+appName(m.RegisteredBy.Program)+' ('+m.RegisteredBy.Program+')','Registered: '+m.Registered,'State: '+m.State,'Revision: '+m.Revision].concat(m.Expires?['Expires: '+m.Expires]:[],m.LastApplied?['Last applied: '+m.LastApplied]:[]).join('\n')}
function updateCredentialRow(row,m){
 row.record=m;
 row.label.textContent=metaBoxName(m);
 row.caption.textContent=credentialCaption(m);
 row.pre.textContent=credentialTechnical(m);
 const service=credentialService(m);row.setupQuestions.hidden=!(service&&service.setup&&m.Name===service.credential);
 const active=m.State!=='revoked';row.replace.hidden=row.remove.hidden=!active;row.access.hidden=!active||m.Kind==='openabstractions/local-key@1';if(!active){row.editor.hidden=true;row.secret.value=''}}
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
function rebuildNameChooser(selectId,inputId){const select=el(selectId);const current=select.value;select.replaceChildren();for(const[name,label]of credentialNames){const o=document.createElement('option');o.value=name;o.textContent=label;o.title=name;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);const names=[...credentialNames.keys()];select.value=current&&(credentialNames.has(current)||current==='__other__'&&el(inputId).value)?current:(names[0]||'__other__');nameChooserSync(selectId,inputId)}
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
// Display names come from the existing preset and application catalogues.
// A failed optional name lookup leaves the saved name or executable basename.
const credentialDisplayReady=Promise.all([oaRegisteredApps().then(apps=>{for(const a of apps)credentialApps.set(a.program,a.title||a.name||shortName(a.program))}),call('/connect').then(r=>{for(const p of r.presets||[])if(p.credential&&p.base)credentialServices.set(p.id,{label:p.label,credential:p.credential,target:addressHost(p.base),setup:p.id===p.credential&&p.kind!=='download'})}).catch(()=>{})]);
async function loadKeyPrograms(){if(credentialKeyPrograms)return;try{const r=await call('/inference/keys');if(r.Outcome==='page')credentialKeyPrograms=new Map(r.Keys.map(k=>[k.Name,k.Program]))}catch(e){}}
// list reads as many pages as are shown (Show more adds one), so a reread
// past the first page keeps every shown row current.
let listPages=1;
async function list(){try{await credentialDisplayReady;await ensureAllowSelf();await loadKeyPrograms();let cursor='',records=[],p,pages=0;do{p=await call('/credentials?cursor='+encodeURIComponent(cursor));if(p.Outcome!=='page'){show('list-status',outcomeStatus(p.Outcome));el('list-next').hidden=true;return}records.push(...p.Records);cursor=p.Next;pages++}while(!p.Complete&&pages<listPages);
 const sig=JSON.stringify([records,p.Complete,p.Limits]);if(sig===credentialsLast)return;credentialsLast=sig;
 if(el('list-status').getAttribute&&el('list-status').getAttribute('data-state')==='loading')oaSetStatus('list-status','ok','');
 el('list-next').hidden=!!p.Complete;show('limits',limitsText(p.Limits));el('records-empty').hidden=!!records.length;
 credentialRecords=records;credentialNames.clear();for(const m of records)credentialNames.set(m.Name,metaBoxName(m));renderAllowRules();
 oaKeyedRows(el('records'),records,m=>m.Name,credentialRow,updateCredentialRow);
 // The rows above (connect_page.go) read which keys are saved.
 window.credentialsSaved=new Map(records.map(m=>[m.Name,m]));if(window.connectRedraw)window.connectRedraw();
 for(const[selectId,inputId] of nameChoosers)rebuildNameChooser(selectId,inputId)}catch(e){oaShowError('list-status',e.message)}}
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
function credentialScopeSummary(){const uses=consumersSelected().map(id=>id==='__other__'?'custom uses':consumerServiceName(id));el('add-scope-summary').textContent='Key type and uses: '+(el('add-kind').value==='header'?'HTTP header':'Bearer')+' · '+(uses.join(', ')||'Choose at least one use')}
function consumerToggle(value,label,title,pressed){const b=document.createElement('label'),input=document.createElement('input');input.type='checkbox';b.value=value;b.textContent=label;if(title)b.title=title;const set=v=>{b.pressed=input.checked=v;if(value==='__other__')el('add-consumers').hidden=!v};set(pressed);input.onchange=()=>{set(input.checked);credentialScopeSummary();oaSetStatus('add-consumers-status','ok',label+(b.pressed?' on.':' off.'))};b.onclick=e=>{if(e)return;set(!b.pressed);credentialScopeSummary();oaSetStatus('add-consumers-status','ok',label+(b.pressed?' on.':' off.'))};b.append(input);return b}
function renderConsumersSelect(){const box=el('add-consumers-select');box.replaceChildren();for(const id of credentialConsumerIds)box.append(consumerToggle(id,consumerServiceName(id),id,id==='abstraction.inference/chat@1'));box.append(consumerToggle('__other__','Other…','',false));credentialScopeSummary()}
function consumersSelected(){return [...el('add-consumers-select').children].filter(b=>b.pressed).map(b=>b.value)}
// Add a credential… shows the form; nothing is typed until the person asks.
el('add-form').hidden=true;
el('allow-form').hidden=true;
el('allow-open').onclick=()=>{const f=el('allow-form');f.hidden=!f.hidden;oaSetStatus('allow-open-status','ok',f.hidden?'The form is closed.':'Choose a credential and an app, then Save.')};
function closeCredentialEntry(){el('add-secret').value='';el('add-secret').type='password';el('add-reveal').textContent='Show secret';if(el('add-reveal').setAttribute)el('add-reveal').setAttribute('aria-pressed','false');el('add-form').hidden=true;const d=el('credential-dialog');if(d.close)d.close();oaSetStatus('add-open-status','ok','Nothing added.')}
el('add-cancel').onclick=closeCredentialEntry;
el('credential-dialog').addEventListener('cancel',e=>{if(e&&e.preventDefault)e.preventDefault();closeCredentialEntry()});
el('add-open').onclick=()=>{el('add-form').hidden=false;const d=el('credential-dialog');if(d.showModal&&!d.open)d.showModal();if(el('add-name').focus)el('add-name').focus();oaSetStatus('add-open-status','ok','')};
renderConsumersSelect();
// Store used to say "Stored testcred. List again to see it." while the
// list under it kept its stale rows; it now reads the list again itself
// (task 2026-09-23, fourteenth first-time visitor, findings 2 and 3).
el('add-form').onsubmit=async e=>{e.preventDefault();const secret=el('add-secret').value;el('add-secret').value='';if(!secret){show('add-status','Enter the secret first.');return}const consumers=consumersSelected().filter(c=>c!=='__other__').concat(consumersSelected().includes('__other__')?commaList(el('add-consumers').value):[]);if(!consumers.length){show('add-status','Choose at least one allowed use.');return}let expiry;try{expiry=credentialExpiryChoice()}catch(err){show('add-status',err.message);return}const badConsumer=consumers.find(c=>!credentialConsumerPattern.test(c));if(badConsumer){show('add-status','Consumers must each be a contract id like abstraction.download/http-execution@1 ('+badConsumer+' is not); separate several with commas.');return}if(!el('add-name').value.trim()||!el('add-targets').value.trim()){show('add-status',(el('add-name').value.trim()?'Address':'Name')+' is required.');return}const body={action:'add',name:el('add-name').value.trim(),kind:el('add-kind').value,header:el('add-kind').value==='header'?el('add-header').value.trim():'',targets:commaList(el('add-targets').value),consumers,expires:expiry,secret,expected_revision:''};const preset=addPresetUnchanged(body);if(preset)return addThroughPreset(preset,secret);try{const r=await call('/credentials',body);if(r.Outcome==='stored'){el('add-form').reset();renderConsumersSelect();el('add-consumers').hidden=true;el('add-kind').onchange();el('add-expires').onchange();addForSync();await list();closeCredentialEntry();oaShowResult('add-open-status','Stored '+body.name+'.',r,'Revision '+r.Revision)}else{el('add-status').title='';show('add-status',outcomeStatus(r.Outcome))}}catch(err){oaShowError('add-status',err.message)}};
// A key for a hosted preset, with the preset's name and address and the
// default bearer kind, chat use and no expiry, is saved the way its Connect…
// row saves it (/connect): under the preset's name, with the uses the
// address for apps needs and with the preset's model server, so its row
// reads configured (panel audit 2026-11-01: DeepSeek stayed "Not connected"
// after its key was saved here). Apps still need their own permission to use
// it. Any other choice saves the key alone.
function addPresetUnchanged(body){const v=el('add-for').value;if(!v||v==='__other__')return null;let c;try{c=JSON.parse(v)}catch(e){return null}if(!c.preset||body.name!==c.name||body.targets.length!==1||body.targets[0]!==c.target||body.kind!=='bearer'||body.expires||body.consumers.length!==1||body.consumers[0]!=='abstraction.inference/chat@1')return null;return c}
const addConnectWords={conflict:'A key with this name is already saved with other settings; nothing was changed. Replace or remove it under Saved credentials first.',forbidden:'This Panel may not save keys or model servers; nothing was changed.',unavailable:'OpenAbstractions could not save this key right now; nothing was changed.',invalid:'OpenAbstractions refused this key as invalid; nothing was changed.',no_secure_store:outcomeText.no_secure_store,exhausted:outcomeText.exhausted};
async function addThroughPreset(c,secret){oaSetStatus('add-status','loading','Saving…');try{const r=await call('/connect',{edit:'connect',preset:c.preset,base:'',secret,budget:0});if(r.outcome!=='applied'){oaSetStatus('add-status','error',addConnectWords[r.outcome]||'The key was not saved; nothing was changed.');return}el('add-form').reset();renderConsumersSelect();el('add-consumers').hidden=true;el('add-kind').onchange();el('add-expires').onchange();addForSync();oaSetStatus('add-status','idle','');await list();if(window.connectChanged)await window.connectChanged();closeCredentialEntry();oaShowResult('add-open-status','Saved the key and configured '+c.label+'. Each app still needs your permission to use it.',r)}catch(e){oaShowError('add-status',e)}}
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
// accessSentence is one use rule as the person reads it (architect ruling C):
// Name the app and the credential account. After Remove, say the app can
// no longer use the account.
function credentialAccountName(name){const m=credentialRecords.find(r=>r.Name===name);return m?metaBoxName(m):name}
function accessSentence(r,removed){const other=accountLabel(r.Subject.Account)==='this account'?'':' on another account on this device';return appName(r.Subject.Program)+(removed?' can no longer use':r.Permit?' can use':' can\'t use')+' your '+credentialAccountName(r.Resource.replace(/^credential:/,''))+' account'+other}
// ownProgram is one of OpenAbstractions' own programs, whose own key reads are
// no app's use of the key.
function ownProgram(program){const title=credentialApps.get(program);return title==='OpenAbstractions'||title===credentialPanelTitle||!!(allowSelf&&allowSelf.program===program)}
// credentialAccessRules is undefined until the first read of the rules
// finishes (a row says nothing about access yet), null after a failed read.
let credentialAccessRules;
function accessRuleRow(r){const row=document.createElement('div');row.className='oa-row';const text=document.createElement('div');text.className='oa-row-text';const sentence=document.createElement('span');sentence.className='oa-row-label';sentence.textContent=accessSentence(r);const details=oaTechnical(document.createElement('details'));details.className='oa-expander';const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=JSON.stringify(r,null,2);details.append(summary,pre);text.append(sentence,details);const control=document.createElement('div');control.className='oa-row-control';const remove=document.createElement('button');remove.type='button';remove.textContent='Remove';remove.onclick=()=>accessRemove(r);control.append(remove);row.append(text,control);Object.assign(row,{sentence,remove,rule:r});return row}
// Remove takes one use rule away and says what changed.
async function accessRemove(r){if(!allowRevision){oaSetStatus('allow-status','error','App access is not available yet; wait for permissions to load.');return}try{const result=await call('/rights',{edit:'revoke',revision:allowRevision,account:r.Subject.Account,program:r.Subject.Program,action:r.Action,resource:r.Resource,permit:false});if(result.Outcome==='applied'){await allowPolicyList();oaSetStatus('allow-status','ok',accessSentence(r,true)+'.')}else oaSetStatus('allow-status','error',rightsOutcomeStatus(result.Outcome))}catch(e){oaShowError('allow-status',e.message)}}
// renderAllowRules lists the apps' use rules, never OpenAbstractions' own.
function renderAllowRules(){if(!Array.isArray(credentialAccessRules))return;const apps=credentialAccessRules.filter(r=>!ownProgram(r.Subject.Program));show('allow-status',apps.length?'':'No saved app access rules.');el('allow-rules').replaceChildren();for(const r of apps)el('allow-rules').append(accessRuleRow(r))}
function refreshCredentialAccess(){for(const row of el('records').children)if(row.record)updateCredentialRow(row,row.record)}
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
// The rules are read page by page at one revision. A list that changed while
// it was read is read once more from its start before the page says it could
// not be read. Each page's program names (the Panel's own titles) name who
// saved a key and who may use it.
async function readAccessRules(){let cursor='',revision='',rules=[],seen=new Set();for(;;){const p=await call('/rights?cursor='+encodeURIComponent(cursor));if(p.Outcome!=='page')throw Object.assign(Error(rightsOutcomeStatus(p.Outcome)),{plain:true});if(revision&&revision!==p.Revision)return null;revision=p.Revision;for(const[program,name]of Object.entries(p.Names||{}))credentialApps.set(program,name);rules.push(...(p.Rules||[]).filter(r=>r.Action==='abstraction.credentials/apply'));if(p.Complete)return{revision,rules};if(!p.Next||seen.has(p.Next)||seen.size>=128)return null;seen.add(p.Next);cursor=p.Next}}
async function allowPolicyList(){try{await credentialDisplayReady;const read=await readAccessRules()||await readAccessRules();if(!read)throw Object.assign(Error(rightsOutcomeStatus('gap')),{plain:true});const{revision,rules}=read;for(const row of el('records').children)if(row.record)updateCredentialRow(row,row.record);const sig=JSON.stringify([revision,rules]);if(sig===allowLast)return;allowLast=sig;allowRevision=revision;credentialAccessRules=rules;await ensureAllowSelf();renderAllowRules();refreshCredentialAccess()}catch(e){allowLast='';allowRevision='';credentialAccessRules=null;el('allow-rules').replaceChildren();refreshCredentialAccess();el('allow-status').title='';if(e.plain)oaSetStatus('allow-status','error',e.message);else oaShowError('allow-status',e.message)}}
var allowPolicyReady=allowPolicyList();
// Program path and Account were bare text boxes here while Rights already
// offered choosers for both; both now build their own equivalent chooser
// from oaProgramChooser and oaAccountChooser (panel_shell.go), the one
// shared implementation each (task 2026-09-23, fourteenth first-time
// visitor, finding 8). The program chooser lists registered applications.
// Each app reads by its registered title; the chooser opens on the first
// registered app, and Other… stays for a program that is not registered.
const allowProgramChooser=oaProgramChooser('allow-program-select','allow-program',null,program=>credentialApps.get(program));
function allowProgramDefault(){const select=el('allow-program-select'),first=Array.from(select.options||select.children||[]).find(o=>o.value&&o.value!=='__other__');if(select.value==='__other__'&&!el('allow-program').value&&first){select.value=first.value;if(select.onchange)select.onchange()}}
let allowSelf=null,allowSelfAccountName='',allowSelfPromise=null;
const allowAccountChooser=oaAccountChooser('allow-account-select','allow-account',()=>allowSelf&&allowSelf.account,()=>allowSelfAccountName);
// ensureAllowSelf is accessSentence's own accountLabel, resolved once and
// shared: allowPolicyList awaits it before rendering rows so "this
// account" is already known the first time rules render, not only after
// loadAllowChoosers happens to resolve first (rights_page.go's own
// ensureSelf, the same fix, task 2026-09-23, fifteenth first-time
// visitor, finding 4).
function ensureAllowSelf(){if(!allowSelfPromise)allowSelfPromise=call('/explore').then(v=>{allowSelf=v.self;allowSelfAccountName=v.selfAccountName||'';if(allowSelf&&allowSelf.program&&!credentialApps.has(allowSelf.program))credentialApps.set(allowSelf.program,credentialPanelTitle);allowAccountChooser.rebuild()}).catch(()=>{});return allowSelfPromise}
async function loadAllowChoosers(){await credentialDisplayReady;await allowProgramChooser.ready;allowProgramChooser.rebuild();allowProgramDefault();await ensureAllowSelf()}
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
el('allow-form').onsubmit=async e=>{e.preventDefault();const name=el('allow-name').value,program=el('allow-program').value,account=el('allow-account').value,used=allowRevision,permit=el('allow-permit').value==='permit';for(const id of ['allow-name','allow-account','allow-program'])if(!el(id).value.trim()){oaSetStatus('allow-save-status','error',allowFieldLabel[id]+' is required.');return}if(!used){oaSetStatus('allow-save-status','error','App access is not available yet; wait for permissions to load.');return}const resource='credential:'+name;const body={edit:'set',revision:allowRevision,account,program,action:'abstraction.credentials/apply',resource,permit};try{const r=await call('/rights',body);if(r.Outcome==='applied'){await allowPolicyList();oaShowResult('allow-save-status',accessSentence({Permit:permit,Subject:{Program:program,Account:account},Resource:resource})+'.',r,'Program: '+program+'; Account: '+account+'; id: '+name+'; revision '+used)}else{el('allow-save-status').title='';show('allow-save-status',rightsOutcomeStatus(r.Outcome))}}catch(err){oaShowError('allow-save-status',err.message)}};
// For lists the hosted model servers (Providers' server list); choosing one
// takes its credential name and its address. A key for anything else takes
// a typed name and address behind "Other…".
function addressHost(base){try{return new URL(base).hostname}catch(e){const m=/^[a-z]+:\/\/([^/:]+)/i.exec(String(base||''));return m?m[1]:''}}
async function loadAddFor(){let hosted=[];try{const l=await call('/connect');hosted=(l.presets||[]).filter(p=>p.base&&p.kind!=='download').map(p=>({Name:p.label+' (preset)',Credential:p.credential,Base:p.base,Preset:p.id,Label:p.label}))}catch(e){}
 const select=el('add-for'),current=select.value;select.replaceChildren();
 const valueOf=e=>JSON.stringify({name:e.Credential||e.Name,target:addressHost(e.Base),preset:e.Preset,label:e.Label});
 for(const e of hosted){const o=document.createElement('option');o.value=valueOf(e);o.textContent=e.Name;select.append(o)}
 const other=document.createElement('option');other.value='__other__';other.textContent='Custom connection';select.append(other);
 const values=hosted.map(valueOf);select.value=values.includes(current)?current:(values[0]||'__other__');addForSync()}
function addForSync(){const v=el('add-for').value,other=!v||v==='__other__';el('add-name').hidden=el('add-targets').hidden=false;if(other){el('add-name').value='';el('add-targets').value=''}else{const c=JSON.parse(v);el('add-name').value=c.name;el('add-targets').value=c.target}}
el('add-for').onchange=addForSync;
el('add-kind').onchange=()=>{el('add-header').hidden=el('add-header-label').hidden=el('add-kind').value!=='header';el('add-header').required=el('add-kind').value==='header';credentialScopeSummary()};
var addForReady=loadAddFor();
// The page tells the screenshot tool it is ready once its first readings
// have finished (S1's oaReady; data-ready until it exists).
function oaPageReady(){if(typeof oaReady==='function')oaReady();else if(typeof document!=='undefined'&&document.documentElement)document.documentElement.dataset.ready='1'}
Promise.all([listReady,allowPolicyReady,addForReady,window.connectReady]).then(oaPageReady,oaPageReady);
// The page updates itself while it is visible; there is no Refresh button.
// oaQuiet runs one reading at a time: a timer tick while one is still
// running is skipped.
function oaQuietOnce(fn){let busy=false;return async()=>{if(busy)return;busy=true;try{await fn()}finally{busy=false}}}
const credentialsQuiet=oaQuietOnce(()=>Promise.all([list(),allowPolicyList(),window.connectChanged?window.connectChanged():null]));
window.credentialsTick=credentialsQuiet;
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(credentialsQuiet);
// Connect's existing controller saved a credential; read its metadata now.
window.credentialsChanged=()=>list();
if(typeof setInterval==='function')setInterval(()=>{if(document.visibilityState!=='hidden')credentialsQuiet()},15000);
</script>`

// Ordinary application forms; native Asks permission presentation is unchanged.
const credentialEditorStyle = `<style>
dialog[data-credential-editor]{box-sizing:border-box;width:min(480px,calc(100vw - 32px));max-height:calc(100vh - 48px);padding:24px;border:1px solid GrayText;border-radius:10px;background:Canvas;color:CanvasText;box-shadow:0 16px 56px #0004}
dialog[data-credential-editor]::backdrop{background:#0006}
dialog[data-credential-editor] h2{margin:0 0 8px;font-size:18px;position:sticky;top:0;background:Canvas;z-index:1}
dialog[data-credential-editor] form>p{margin:0 0 12px}
dialog[data-credential-editor] label{display:block;font-weight:600;margin:10px 0 5px}
dialog[data-credential-editor] form>div{display:flex;gap:8px;align-items:center}
dialog[data-credential-editor] button{white-space:nowrap}
dialog[data-credential-editor] input:not([type=checkbox]),dialog[data-credential-editor] select{box-sizing:border-box;width:100%;min-width:0}
dialog[data-credential-editor] small{display:block;opacity:.8;margin:4px 0 8px}
dialog[data-credential-editor] form>div:last-of-type{display:flex;gap:8px;justify-content:flex-end;margin-top:22px;position:sticky;bottom:-24px;background:Canvas;padding:12px 0;z-index:2}
dialog[data-credential-editor] details{margin-top:18px}
dialog[data-credential-editor] #add-consumers-select>label{display:flex;flex-direction:row-reverse;justify-content:flex-end;gap:8px;margin:8px 0;font-weight:400}
dialog[data-credential-editor] [hidden]{display:none!important}
</style>`
