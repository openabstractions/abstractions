package main

// inferencePage is the Panel's inference page: model servers with each
// credential's spend today, the inference gateway and its gateway keys, and
// the inference audit. It renders every value as text and calls only
// /inference/* with the panel key. Its words follow
// research/vocabulary/DECISION.md: model server (D11), modality (D82),
// inference gateway and gateway key (D59, D60), API (D62) and budget (D64);
// the JSON fields it reads keep their wire names (Hosts, Profiles, Ceiling)
// until inference/chat@2 and inference/operator@2.
// Its own grant widget's status line is a hand-written copy of
// grantWidgetHTML's markup (panel_shell.go), predating that shared
// function; it carried the same <pre>-renders-a-plain-sentence-monospace
// defect that fix corrected there and missed this copy (task 2026-09-23,
// fourteenth first-time visitor, finding 1).
const inferencePage = `<h1>Inference</h1><p>Model calls this device makes: model servers, gateway keys, and every decision recorded. Each action below needs its own permission: <span title="abstraction.inference/host.manage">inference server manage</span> to add or remove model servers, <span title="abstraction.inference/key.issue">inference key issue</span> to issue or revoke gateway keys, and <span title="abstraction.inference/audit.read">inference audit read</span> to read the audit log.</p><div class="oa-grant" id="inference-grant" data-grant-rules="abstraction.inference/host.manage,abstraction.inference/key.issue,abstraction.inference/audit.read" data-grant-resource="account"><button type="button">Grant these to this Panel</button><p class="oa-grant-status" aria-live="polite"></p></div>
<section id="hosts-section"><h2>Model servers and spend</h2><p>Model servers this device calls directly for model completions, separate from the gateway keys other apps use to reach it.</p><button id="hosts-list">Refresh</button><pre id="hosts-status"></pre><div class="oa-table"><table><thead><tr><th>Name</th><th>Kind</th><th>Base</th><th>Credential</th><th>Modalities</th><th>Registered by</th><th>State</th><th>Spend today</th><th>Budget</th><th></th></tr></thead><tbody id="hosts"></tbody></table></div>
<form id="host-add"><input id="host-name" required placeholder="Name (ollama, lmstudio, or lemonade)"><select id="host-base-select" aria-label="Base URL"></select> <input id="host-base" required placeholder="base URL" hidden><input id="host-wire" placeholder="API, for a hosted server (openai-compatible or anthropic-messages)"><input id="host-credential" placeholder="credential name"><input id="host-profiles" placeholder="Modalities (default depends on the API: chat, embed, transcription, speech, image)"><input id="host-tokens" type="number" min="0" placeholder="tokens per day"><input id="host-micros" type="number" min="0" step="0.01" placeholder="Spend per day, in your currency"><button>Add model server</button></form><p>Adding a model server also sets it up for the command line and this Panel, and applies the credential if it's a hosted server. Whichever finishes second, adding the model server or registering the credential, is what writes this rule.</p></section>
<section id="gateway"><h2>Inference gateway</h2><p>The inference gateway is a local proxy other apps on this account can call. It lets apps that already speak the OpenAI or Anthropic API reach this device through a local address and key, with no changes to the app. It's off by default, only listens on 127.0.0.1, and stays set after a restart. Changing it needs <span title="abstraction.inference/host.manage">inference server manage</span>.</p><button id="gateway-read">Refresh</button><pre id="gateway-status"></pre><form id="gateway-set"><input id="gateway-address" placeholder="127.0.0.1:PORT (default 127.0.0.1:8793)"><button id="gateway-on" type="submit">Turn on</button><button id="gateway-off" type="button">Turn off</button></form></section>
<section id="keys-section"><h2>Gateway keys</h2><p>A gateway key lets one other app reach this device through the inference gateway, separate from the model servers this device calls directly. The key alone doesn't grant access: the app also needs <span title="abstraction.inference/complete">inference complete</span> on a model server, and permission to apply a hosted server's credential. The key is shown once, so save it.</p><button id="keys-list">Refresh</button><pre id="keys-status"></pre><div class="oa-table"><table><thead><tr><th>Program</th><th>State</th><th>Credential</th><th>Issued</th><th>Issued by</th><th></th></tr></thead><tbody id="keys"></tbody></table></div>
<form id="key-issue"><select id="key-program-select" aria-label="Program"></select><input id="key-program" required placeholder="Program path" hidden><input id="key-credential" placeholder="Credential (optional)"><button>Issue key</button></form><div id="key-shown"></div></section>
<section id="audit-section"><h2>Audit</h2><p>Every decision from direct calls and the gateway, with how strongly each caller's identity was verified.</p><button id="audit-start">Show from the start</button><button id="audit-next" disabled>Next page</button><pre id="audit-status"></pre><div class="oa-table"><table><thead><tr><th>#</th><th>Time</th><th>Route</th><th>Verified</th><th>Program</th><th>Model server</th><th>Model</th><th>Credential</th><th>Outcome</th><th>Reason</th><th>Tokens in/out</th></tr></thead><tbody id="audit"></tbody></table></div></section>
<script>
const key=new URLSearchParams(location.search).get('k');let hostsRevision='',gatewayRevision='',auditCursor=0;
const el=id=>document.getElementById(id);function show(id,x){el(id).textContent=typeof x==='string'?x:JSON.stringify(x,null,2)}
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function shortName(path){const parts=String(path||'').split(/[\\/]/).filter(Boolean);return parts.length?parts[parts.length-1]:path}
const outcomeText={forbidden:'The runtime did not permit this Panel; nothing was changed.',unavailable:'The runtime could not answer; nothing is assumed. Try again.',conflict:'The configuration changed or the program already holds a key; list again.',invalid:'The runtime refused the request as invalid.',unknown:'Nothing by that name.',no_secure_store:'The runtime has no platform store to hold a key.'};
function outcome(o,reason){return (outcomeText[o]||oaPlain(o).line)+(reason?' ('+reason+')':'')}
// row's own titles array names, per cell, the raw value behind a shortened
// or reformatted display (task 2026-09-23, tenth first-time visitor,
// findings 2 and 3: the Issued column read a raw ISO timestamp, and Issued
// by read a full path, both as visible text).
function row(tbody,cells,titles,button){const tr=document.createElement('tr');cells.forEach((c,i)=>{const td=document.createElement('td');td.textContent=c==null?'':String(c);if(titles&&titles[i])td.title=titles[i];tr.append(td)});const td=document.createElement('td');if(button)td.append(button);tr.append(td);el(tbody).append(tr)}
function action(label,fn){const b=document.createElement('button');b.textContent=label;b.onclick=fn;return b}
// hostsStatus keeps the configuration revision off the visible sentence: the
// revision is a value to compare, not a sentence to read (task 2026-09-23
// finding 3, "Configuration revision providers-v2:e3b0c44298fc1c149afbf4c8"
// printed as text). It stays available as a title attribute, muted and small
// via CSS, for whoever needs to compare it; hostsRevision (the JS variable)
// still carries it for the conditional edit either way.
// hostsStatus's empty line now says what to do next, the same shape as the
// Gateway keys and Audit sections' own empty and end-of-list lines (task
// 2026-09-23, ninth first-time visitor, finding 3: Show hosts with no hosts
// left only the table's own headers and no line at all to explain them).
function hostsStatus(l){const e=el('hosts-status');e.title='Configuration revision '+l.Revision;e.textContent=l.Hosts.length?'':'No model servers registered. Add one below.'}
// The Kind cell carries both what the Class column used to say and what Kind
// always said: a local host's Kind is already one of a known set of local
// backends (ollama, lmstudio, lemonade), and a hosted host's Kind is already
// one of a known set of wire formats (openai-compatible, anthropic-messages),
// so "hosted" or "local" was never new information next to it (task
// 2026-09-23 finding 3, the one visible row reading the same word twice).
function hostKind(e){return(e.Hosted?'hosted: ':'local: ')+e.Kind}
// currency turns a spend figure carried on the wire in micros (millionths of
// the account's currency) into the two-decimal amount a person reads (task
// 2026-09-23, fourth first-time visitor: "Spend per day (in millionths of
// your currency)" read as an internal unit). The wire keeps carrying micros;
// only the display and the form's own entry convert.
function currency(micros){return (micros/1e6).toFixed(2)}
// The State column used to read "down: Get \"http://127.0.0.1:9999/api/
// tags\": dial tcp ...: connectex: ...", a raw connection error as visible
// text; it now reads oaHostState's own plain word, the error kept as that
// cell's own title (task 2026-09-23, eleventh first-time visitor, finding
// 5).
// hosts, keys and audit each also refresh the grant widget above, the same
// fetch their own arrival check makes, so a rule granted since this page
// opened shows up here without a reload (task 2026-09-23, fifteenth
// first-time visitor, finding 1).
async function hosts(){if(window.oaGrantRefresh)oaGrantRefresh();try{const l=await call('/inference/hosts');el('hosts').replaceChildren();if(l.Outcome!=='page'){hostsRevision='';el('hosts-status').title='';show('hosts-status',outcome(l.Outcome));return}hostsRevision=l.Revision;hostsStatus(l);for(const h of l.Hosts){const e=h.Entry;row('hosts',[e.Name,hostKind(e),e.Base,e.Credential||'none',(e.Profiles||[]).join(', '),e.DeclaredBy||'',oaHostState(h.Up),h.Spend?h.Spend.Tokens+' tokens, '+currency(h.Spend.Micros)+' ('+h.Spend.Day+')':'0',e.Ceiling?e.Ceiling.TokensPerDay+' tokens, '+currency(e.Ceiling.MicrosPerDay):'no budget'],[null,null,null,null,null,null,h.Why||null],action('Remove',()=>hostEdit({edit:'remove',name:e.Name})));if(e.Base)hostBasesSeen.add(e.Base)}rebuildHostBaseSelect()}catch(e){hostsRevision='';el('hosts-status').title='';oaShowError('hosts-status',e.message)}}
// hostEdit's applied line used to read "Applied; configuration revision
// providers-v2:42a6…", the new revision printed as visible text; it now
// reads "Applied.", the revision kept on the line's own title and in
// Technical details, oaShowResult's own shape (task 2026-09-23, tenth
// first-time visitor, finding 1).
// hostEdit used to refuse before its own list had ever been read
// ("List hosts before changing them."), even though hosts() below now
// reads it on arrival, and left the reply as "Applied. ... List again
// before another change." while the table under it kept its stale rows
// (task 2026-09-23, fourteenth first-time visitor, findings 2 and 3).
// hosts() already runs once on load; an edit reads it again on success,
// which both refreshes the table and gives hostEdit its own revision for
// the very next edit, so the precondition and the "list again" instruction
// both go.
async function hostEdit(body){try{const c=await call('/inference/hosts',{...body,revision:hostsRevision});if(c.Outcome==='applied'){await hosts();oaShowResult('hosts-status','Applied.'+(c.Reason?' Not every rule was written: '+c.Reason+'.':''),c,'Configuration revision '+c.Revision)}else{el('hosts-status').title='';show('hosts-status',outcome(c.Outcome,c.Reason))}}catch(e){oaShowError('hosts-status',e.message)}}
el('hosts-list').onclick=hosts;
var hostsReady=hosts();
// Add host's two required fields now name themselves when left blank (task
// 2026-09-23, seventh first-time visitor, finding 4, the Issue key
// pattern): the browser blocks the submit event for a failed required
// field without running this form's own onsubmit, but always fires
// "invalid" on the field, submit or not.
for(const id of ['host-name','host-base']){const label={'host-name':'Name','host-base':'Base URL'}[id];el(id).addEventListener('invalid',()=>show('hosts-status',label+' is required to add a model server.'))}
// Base URL opened as one empty field with no hint what to type; it now
// offers ollama's own documented default (serve's own --base help text)
// and every base this page has already seen among the current hosts,
// "Other…" revealing the typed field for anything else (task 2026-09-23,
// eleventh first-time visitor, requirement 3).
let hostBasesSeen=new Set(['http://127.0.0.1:11434']);
function rebuildHostBaseSelect(){const select=el('host-base-select');const current=select.value;select.replaceChildren();for(const base of[...hostBasesSeen].sort()){const o=document.createElement('option');o.value=base;o.textContent=base;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);select.value=current&&(hostBasesSeen.has(current)||current==='__other__')?current:[...hostBasesSeen][0]}
el('host-base-select').onchange=()=>{const v=el('host-base-select').value;if(v==='__other__'){el('host-base').hidden=false;el('host-base').value='';el('host-base').focus()}else{el('host-base').hidden=true;el('host-base').value=v}};
el('host-base').hidden=true;rebuildHostBaseSelect();el('host-base').value=el('host-base-select').value;
el('host-add').onsubmit=ev=>{ev.preventDefault();const wire=el('host-wire').value.trim(),name=el('host-name').value.trim(),tokens=Number(el('host-tokens').value||0),micros=Math.round(Number(el('host-micros').value||0)*1e6);const profiles=el('host-profiles').value.split(',').map(s=>s.trim()).filter(Boolean);const host={Name:name,Hosted:!!wire,Kind:wire||name,Base:el('host-base').value.trim(),Credential:el('host-credential').value.trim()};if(profiles.length)host.Profiles=profiles;if(tokens||micros)host.Ceiling={TokensPerDay:tokens,MicrosPerDay:micros};hostEdit({edit:'add',host})};
// gatewayText leaves the setting revision out of the sentence, the same
// treatment hostsStatus gives the hosts revision (task 2026-09-23 finding 3,
// "Setting revision gateway-v1:e3b0c44298fc1c149afbf4c8." sitting mid-text);
// gateway() below puts it on the element's title instead.
function gatewayText(s){return 'Setting: '+(s.Open?'on at '+s.Address:'off')+'\nGateway: '+(s.Listening?'listening on http://'+s.ListeningAddress+'/v1 (OpenAI) and http://'+s.ListeningAddress+' (Anthropic)':'closed'+(s.Why?': '+s.Why:''))}
async function gateway(){try{const s=await call('/inference/gateway');if(s.Outcome!=='page'){gatewayRevision='';el('gateway-status').title='';show('gateway-status',outcome(s.Outcome));return}gatewayRevision=s.Revision;el('gateway-status').title='Setting revision '+s.Revision;show('gateway-status',gatewayText(s))}catch(e){gatewayRevision='';el('gateway-status').title='';oaShowError('gateway-status',e.message)}}
// gatewaySet used to refuse before the setting had ever been read; gateway()
// below now reads it on arrival, so that precondition goes the same way
// hostEdit's did (task 2026-09-23, fourteenth first-time visitor, findings
// 2 and 3).
async function gatewaySet(open){try{const c=await call('/inference/gateway',{revision:gatewayRevision,open,address:open?el('gateway-address').value.trim():''});if(c.Outcome!=='applied'){show('gateway-status',outcome(c.Outcome,c.Reason));return}await gateway()}catch(e){oaShowError('gateway-status',e.message)}}
el('gateway-read').onclick=gateway;el('gateway-set').onsubmit=ev=>{ev.preventDefault();gatewaySet(true)};el('gateway-off').onclick=()=>gatewaySet(false);
var gatewayReady=gateway();
// time reads a raw epoch millisecond value as a local readable time; the
// raw ISO value moves to the cell's own title (task 2026-09-23, tenth
// first-time visitor, finding 2: "Issued 2026-09-23T15:09:12.464Z").
function time(ms){return ms?oaLocalTime(ms):''}
async function keys(){if(window.oaGrantRefresh)oaGrantRefresh();try{const l=await call('/inference/keys');el('keys').replaceChildren();if(l.Outcome!=='page'){show('keys-status',outcome(l.Outcome));return}show('keys-status',l.Keys.length?'':'No keys issued.');for(const k of l.Keys){row('keys',[k.Program,k.State,k.Credential,time(k.IssuedUnixMs),shortName(k.IssuedBy)],[null,null,null,k.IssuedUnixMs?new Date(k.IssuedUnixMs).toISOString():null,k.IssuedBy],k.State==='active'?action('Revoke',()=>keyEdit({edit:'revoke',program:k.Program})):null);if(k.Program)keyProgramsSeen.add(k.Program)}keyProgramChooser.rebuild()}catch(e){oaShowError('keys-status',e.message)}}
// Issue key's own Program path was a bare text box while Rights already
// offered a chooser for the same kind of field; both now build their own
// equivalent chooser from oaProgramChooser (panel_shell.go), the one
// shared implementation (task 2026-09-23, fourteenth first-time visitor,
// finding 8). Known programs here are every program already issued a key
// (keys() above) and every program the Device page's own resource table
// shows as a holder.
let keyProgramsSeen=new Set();
const keyProgramChooser=oaProgramChooser('key-program-select','key-program',()=>[...keyProgramsSeen]);
async function loadKeyDeviceHolders(){try{const v=await call('/card/table');for(const r of(v.resources||[]))for(const h of(r.holders||[]))if(h.program)keyProgramsSeen.add(h.program)}catch(e){}keyProgramChooser.rebuild()}
keyProgramChooser.rebuild();var keyDeviceHoldersReady=loadKeyDeviceHolders();
// keyEdit's own reply used to say "Issued. List again to refresh." (or
// "Revoked...") while the table under it kept its stale rows; it now
// refreshes the table itself before showing the confirmation (task
// 2026-09-23, fourteenth first-time visitor, finding 3).
async function keyEdit(body){el('key-shown').replaceChildren();try{const r=await call('/inference/keys',body);if(r.Outcome!=='applied'){show('keys-status',outcome(r.Outcome,r.Reason));return}if(body.edit==='issue'){const p=document.createElement('p');p.textContent='Key for '+r.Record.Program+', shown once. Give it to that program as its API key, with the gateway as its base URL:';const k=document.createElement('div');k.className='key';k.textContent=r.Key;el('key-shown').append(p,k)}await keys();show('keys-status',body.edit==='issue'?'Issued.':'Revoked. The gateway refuses the key from its next request.')}catch(e){oaShowError('keys-status',e.message)}}
el('keys-list').onclick=keys;
var keysReady=keys();
// Issue key used to rely on the input's own "required" attribute alone: with
// no keys listed yet and the program path left blank, a click showed no row,
// no error and no field. The browser blocks the submit event itself for a
// failed required field (this handler never even runs) and auto-focuses
// that field, but names nothing in keys-status, so the only sign was
// whatever native validation bubble the browser chose to draw — easy to
// miss, and never observable at all if a browser suppresses it (task
// 2026-09-23, fifth first-time visitor, finding 6). The input's own
// "invalid" event fires exactly when that block happens, submit or not, so
// keys-status now always names the problem too.
el('key-program').addEventListener('invalid', () => show('keys-status', 'Program path is required to issue a key.'));
el('key-issue').onsubmit=ev=>{ev.preventDefault();keyEdit({edit:'issue',program:el('key-program').value.trim(),credential:el('key-credential').value.trim()})};
async function audit(){if(window.oaGrantRefresh)oaGrantRefresh();try{const p=await call('/inference/audit?cursor='+auditCursor);if(p.Outcome==='gap'){auditCursor=p.Next;show('audit-status','Older entries are no longer retained; reading from '+p.Next+'.');return audit()}if(p.Outcome!=='page'){show('audit-status',outcome(p.Outcome));el('audit-next').disabled=true;return}for(const e of p.Entries)row('audit',[e.Sequence,time(e.UnixMs),e.Route,e.Rung,e.Program,e.Host,e.Model,e.Credential,e.Outcome,e.Reason,e.TokensIn+'/'+e.TokensOut],[null,e.UnixMs?new Date(e.UnixMs).toISOString():null]);auditCursor=p.Next;el('audit-next').disabled=p.AtEnd;show('audit-status',p.AtEnd?'End of the audit; read the next page later for new decisions.':'More entries remain.')}catch(e){oaShowError('audit-status',e.message)}}
el('audit-start').onclick=()=>{auditCursor=0;el('audit').replaceChildren();audit()};el('audit-next').onclick=audit;
</script><script>oaGrant('inference-grant','hosts-list,gateway-read,keys-list,audit-start')</script>`
