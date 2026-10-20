package main

// registryPage is the Panel's Providers page: the model servers the runtime
// reaches with their spend, the inference gateway and its gateway keys (all
// three moved here from Models by the Panel pages audit,
// research/adopter-boundary/AUDIT-2026-10-06.md, with their script in
// serversScript), the model inventory, the
// providers registered in abstraction.facade/registry@1 with their readiness
// and described services, and the model servers remote runtimes report. Its
// words follow research/vocabulary/DECISION.md: Providers and Registered
// providers (D41, D42), model server (D11), modality (D82) and the three
// role phrases of D10; the wire role values provider, host and remote stay.
// It renders every value as text and calls the panel's loopback endpoints for
// readings and public IPC edits.
//
// Its shape follows the Panel redesign (research/panel-redesign/SPEC.md §4,
// slice S8): one caption, "Where models run."; the server list is the one
// table; every form is a set of choosers. A model server is picked from the
// kinds and addresses the runtime already knows (the servers registered so
// far and the model servers providers declared), a hosted API from the two
// wire formats, and a typed name or address appears only behind "Other…".
// The managed-provider form left the page: providers register themselves
// through the Facade (VISION 2026-10-06), and its typed executable path and
// contract lists had no chooser to offer. /registry/providers still accepts
// both edits; the page uses withdraw.
// oaEmptyHidden is a list's empty sentence, hidden until the list is empty;
// the page's script shows it, so the section's status line keeps the last
// sentence a click wrote.
func oaEmptyHidden(id, sentence string) string {
	return `<p class="oa-empty" id="` + id + `" hidden>` + sentence + `</p>`
}

var registryPage = oaPageHead("registry", "Providers", "Where models run.") + `<div data-ready-owner="page" hidden></div>` +
	`<section id="hosts-section" class="oa-group"><h2>Model servers</h2>` + oaStatus("hosts-status") +
	`<div class="oa-table"><table><thead><tr><th>Name</th><th>Kind</th><th>Credential</th><th>State</th><th>Spend today</th><th>Budget</th><th></th></tr></thead><tbody id="hosts"></tbody></table></div>` + oaEmptyHidden("hosts-empty", "No model servers registered.") +
	oaExpander("Add a model server", `<form id="host-add">`+
		oaRow("API", "", `<select id="host-wire" aria-label="API"><option value="">Local server</option><option value="openai-compatible">Hosted, OpenAI-compatible</option><option value="anthropic-messages">Hosted, Anthropic Messages</option></select>`, "")+
		oaRow("Server", "", `<select id="host-name-select" aria-label="Server"></select><input id="host-name" aria-label="Server name" data-oa-other hidden>`, "")+
		oaRow("Address", "", `<select id="host-base-select" aria-label="Address"></select><input id="host-base" aria-label="Address" data-oa-other hidden>`, "")+
		oaRow("Modalities", "", `<span id="host-profiles"></span>`, "host-profiles-status")+
		oaRow("Key", "", `<select id="host-credential" aria-label="Key"><option value="">None</option></select>`, "")+
		oaRow("Tokens per day", "", `<select id="host-tokens" aria-label="Tokens per day"><option value="0">No limit</option><option value="100000">100,000</option><option value="1000000">1,000,000</option><option value="10000000">10,000,000</option></select>`, "")+
		oaRow("Spend per day", "", `<select id="host-micros" aria-label="Spend per day"><option value="0">No limit</option><option value="1">1.00</option><option value="5">5.00</option><option value="20">20.00</option></select>`, "")+
		oaRow("Add this server", "", `<button id="host-add-button" type="submit" class="oa-primary" data-status="hosts-status">Add model server</button>`, "")+
		`</form>`) + `</section>
<section id="gateway" class="oa-group"><h2>Inference gateway</h2><p class="oa-caption">A local address for apps that speak the OpenAI or Anthropic API.</p><form id="gateway-set">` +
	oaRow("Address", "", `<select id="gateway-address" aria-label="Address"></select><input id="gateway-address-other" aria-label="Other address" data-oa-other hidden>`, "") +
	oaRow("Gateway", "", `<input type="checkbox" class="switch" id="gateway-switch" aria-label="Gateway">`, "gateway-status") +
	`</form></section>
<section id="keys-section" class="oa-group"><h2>Gateway keys</h2><p class="oa-caption">Keys that let one app use the gateway, each shown once.</p>` + oaStatus("keys-status") +
	`<div class="oa-table"><table><thead><tr><th>Program</th><th>State</th><th>Credential</th><th>Issued</th><th>Issued by</th><th></th></tr></thead><tbody id="keys"></tbody></table></div>` + oaEmptyHidden("keys-empty", "No keys issued.") +
	oaExpander("Issue a key", `<form id="key-issue">`+
		oaRow("Program", "", `<select id="key-program-select" aria-label="Program"></select><input id="key-program" aria-label="Program" data-oa-other hidden>`, "")+
		oaRow("Key", "", `<select id="key-credential" aria-label="Key"><option value="">None</option></select>`, "")+
		oaRow("Issue", "", `<button id="key-issue-button" type="submit" data-status="keys-status">Issue key</button>`, "")+
		`</form>`) + `<div id="key-shown-row" hidden>` + oaRow("New key, shown once", "Give it to that app as its API key, with the gateway as its base URL.", `<code id="key-shown" data-oa-secret></code><button id="key-copy" type="button">Copy</button>`, "key-copy-status") + `</div></section>
<section id="providers-section" class="oa-group"><h2>Registered providers</h2><p class="oa-caption">Programs and runtimes that provide services on this device.</p>` + oaStatus("registry-status") + oaStatusLine("providers-status", false) + oaStatusLine("provider-edit-status", false) + `<div id="providers"></div>` +
	oaExpander("Model inventory", oaStatusLine("models-status", false)+`<div class="oa-table"><table><thead><tr><th>Model family</th><th>Names and providers</th><th>Availability</th><th>Stored in</th><th>Components</th></tr></thead><tbody id="models"></tbody></table></div>`) + `</section>
<section id="remote-section" class="oa-group"><h2>Remote runtimes</h2><p class="oa-caption">Model servers another trusted runtime reports.</p>` + oaStatusLine("remote-status", false) +
	`<div class="oa-table"><table><thead><tr><th>Model server</th><th>Domain</th><th>Kind</th><th>Registered by</th><th>Modalities</th><th>Readiness</th></tr></thead><tbody id="remote"></tbody></table></div></section>
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);function show(id,x){oaSetStatus(id,"ok",x)}
// row's own titles array names, per cell, the raw value behind a plain
// reading (task 2026-09-23, eleventh first-time visitor, finding 5: the
// State column read a raw connection error as visible text).
function row(tbody,cells,titles){const tr=document.createElement('tr');cells.forEach((c,i)=>{const td=document.createElement('td');td.textContent=c==null?'':String(c);if(titles&&titles[i])td.title=titles[i];tr.append(td)});el(tbody).append(tr)}
function list(x){return (x||[]).join(', ')}
function modelAlias(a){const state=a.Servable?'available':'not served';const source=a.Hosted?'hosted':(a.Host?'local':((a.HeldIn||[]).length?'stored':'unserved'));return a.Name+' — '+(a.Host||'no model server')+' ('+source+', '+state+(a.Resident?', provider reports loaded':'')+')'}
function modelAvailability(f){const aliases=f.Names||[];const servable=aliases.some(a=>a.Servable),resident=aliases.some(a=>a.Resident),stored=(f.HeldIn||[]).length||aliases.some(a=>(a.HeldIn||[]).length);if(servable)return aliases.some(a=>a.Servable&&a.Resident)?'Available; a provider reports a name loaded':'Available from provider';if(resident)return stored?'Stored; a provider reports loaded but cannot serve':'Provider reports loaded but cannot serve';if(stored)return 'Stored only; no server can answer';if(aliases.some(a=>a.Host))return 'Provider reports a name that is not servable';return 'No server or stored file reported'}
function modelComponents(f){return (f.Components||[]).map(c=>c.Role+(c.Name?' ('+c.Name+')':'')+' in '+c.Store).join('; ')}
function modelsTable(models){for(const f of models||[])row('models',[f.Family,(f.Names||[]).map(modelAlias).join('; ')||'No model names reported',modelAvailability(f),list(f.HeldIn)||'not reported',modelComponents(f)||'none'])}
// rolePlain reads a registration's wire role (facade FAC-R6's closed enum) as
// the Panel's phrase for it (DECISION.md D10); the wire value stays on the
// cell's own title.
const rolePlain={provider:'managed process',host:'model server',remote:'remote runtime'};
// servicePlain reads a service contract ("abstraction.inference/chat@1") as
// its layer and name ("inference chat"); the contract itself stays on the
// cell's title, since an id is never visible page text (owner, 2026-10-06).
function servicePlain(c){const m=/^abstraction\.([a-z0-9_-]+)\/([a-z0-9_.-]+)@\d+$/.exec(c);if(!m)return c;const name=m[2].replace(/[._-]+/g,' ');return m[1]===m[2]?m[1]:m[1]+' '+name}
// providersTable builds the Providers table itself rather than filling a
// fixed set of columns: Activation, Contracts, Endpoint, Program, Resources,
// Described and Accepted each apply to some provider roles and not others (a
// host declares none of Contracts, Endpoint or Program; a provider not yet
// described has nothing in Described), so a column every declaration leaves
// empty is dropped instead of printing a blank cell in every row (task
// 2026-09-23 finding 4, the same approach card_page.go already takes for the
// Device page's holder columns). A column that stays, because at least one
// row carries a value, still states the meaning of an empty cell in the rows
// that have none, rather than leave it blank.
function providersTable(decls,revision){
 const cols={
  activation:decls.some(p=>p.Declaration.Activation),
  contracts:decls.some(p=>(p.Declaration.Contracts||[]).length),
  endpoint:decls.some(p=>p.Declaration.Endpoint),
  program:decls.some(p=>p.Declaration.Program),
  resources:decls.some(p=>(p.Declaration.Resources||[]).length),
  described:decls.some(p=>(p.Described||[]).length),
  accepted:decls.some(p=>(p.Accepted||[]).length),
 };
 const headers=['Name','Role'];
 if(cols.activation)headers.push('Activation');
 if(cols.contracts)headers.push('Services');
 if(cols.endpoint)headers.push('Endpoint');
 if(cols.program)headers.push('Program');
 headers.push('Engine');
 if(cols.resources)headers.push('Resources');
 headers.push('Readiness');
 if(cols.described)headers.push('Described');
 headers.push('Restarts');
 if(cols.accepted)headers.push('Accepted');
 headers.push('Registered by');
 headers.push('Action');
 const table=document.createElement('table');
 const thead=document.createElement('thead');thead.innerHTML='<tr>'+headers.map(x=>'<th>'+x+'</th>').join('')+'</tr>';
 const tbody=document.createElement('tbody');
 for(const p of decls){
  const d=p.Declaration;
  const cells=[d.Name,rolePlain[p.Role]||p.Role||''];
  if(cols.activation)cells.push(d.Activation||'not set');
  const servicesAt=cells.length;if(cols.contracts)cells.push((d.Contracts||[]).length?d.Contracts.map(servicePlain).join(', '):'none');
  if(cols.endpoint)cells.push(d.Endpoint?d.Endpoint+(d.Remote?' ('+d.Remote.ServerName+')':''):'none');
  // Program and Registered by name a program by its file name; the full
  // path stays on the cell's title (Panel rule: no path as page text).
  const programAt=cells.length;if(cols.program)cells.push(d.Program?oaShortName(d.Program):'none');
  cells.push(d.Host?(d.Host.Kind+' '+d.Host.Base):'');
  if(cols.resources)cells.push((d.Resources||[]).length?list(d.Resources):'none');
  const readinessAt=cells.length;cells.push(p.Readiness);
  const describedAt=cells.length;if(cols.described)cells.push((p.Described||[]).length?p.Described.map(s=>servicePlain(s.Contract)+' '+s.Readiness).join(', '):'not yet');
  cells.push(p.Restarts);
  if(cols.accepted)cells.push((p.Accepted||[]).length?list(p.Accepted):'none');
  const declaredAt=cells.length;cells.push(oaShortName(p.DeclaredBy));
  const tr=document.createElement('tr');cells.forEach((c,i)=>{const td=document.createElement('td');td.textContent=c==null?'':String(c);if(i===1&&p.Role)td.title=p.Role;if(cols.program&&i===programAt&&d.Program)td.title=d.Program;if(i===declaredAt&&p.DeclaredBy)td.title=p.DeclaredBy;if(i===readinessAt&&p.Why)td.title=p.Why;if(cols.contracts&&i===servicesAt&&(d.Contracts||[]).length)td.title=list(d.Contracts);if(cols.described&&i===describedAt&&(p.Described||[]).length)td.title=p.Described.map(s=>s.Contract).join(', ');tr.append(td)});
  const action=document.createElement('td');if(p.Role==='provider'){const b=document.createElement('button');b.type='button';b.textContent=p.DeclaredBy==='installation'||p.DeclaredBy==='product'?'Disable':'Withdraw';b.onclick=()=>withdrawProvider(d.Name,revision);action.append(b)}else action.textContent='';tr.append(action);
  tbody.append(tr);
 }
 table.append(thead,tbody);
 // .oa-table is the wrapper that scrolls sideways when this table's own
 // headers cannot fit, never the page (task 2026-09-23, eighth first-time
 // visitor, finding 6).
 const wrap=document.createElement('div');wrap.className='oa-table';wrap.append(table);
 return wrap;
}
async function editProvider(payload){show('provider-edit-status','Saving registration…');try{const r=await fetch('/registry/providers',{method:'POST',headers:{'X-Panel-Key':key,'Content-Type':'application/json'},body:JSON.stringify(payload)});if(!r.ok)throw Error(await r.text());const result=await r.json();show('provider-edit-status',result.Outcome==='applied'?'Provider registration updated.':('Registry did not apply the change: '+(result.Outcome||'unknown')+(result.Reason?': '+result.Reason:'')));await read()}catch(e){show('provider-edit-status','Provider registration failed: '+e.message)}}
function withdrawProvider(name,revision){if(!confirm('Withdraw or disable provider '+name+'? A running provider process will be stopped by the Registry.'))return;editProvider({edit:'withdraw',revision,name})}
// showDenial writes one plain sentence naming which tables a single "Show"
// click could not read, with each table's raw reason kept behind the same
// disclosure oaShowError uses, rather than three separately worded denials
// on three lines (task 2026-09-23, first-time-user walkthrough: "The runtime
// did not permit this Panel to read this." twice, then "router operation not
// permitted", for what was in every case the same one click).
function showDenial(id,line,detail){
 const e=el(id);if(e.replaceChildren)e.replaceChildren();oaStatusState(e,'error');e.textContent=line;
 if(detail){const d=document.createElement('details');d.className='oa-error-details';const s=document.createElement('summary');s.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=detail;d.append(s,pre);e.append(d)}
}
// read used to run only on a "Show hosts, providers and remote runtimes"
// click, so the page opened as headers and no rows at all (task
// 2026-09-23, fifteenth first-time visitor, finding 2: every other list
// on this Panel already reads itself on arrival). It now also runs once,
// unclicked, as soon as the page's own script loads; the button, renamed
// Refresh, still re-runs it.
// A reading equal to the last one changes nothing on the page, so a
// refresh keeps every row, its status sentence and focus in place.
let registryLastView='';
async function read(){

 let v;try{const r=await fetch('/registry/view',{headers:{'X-Panel-Key':key}});if(!r.ok)throw Error(await r.text());v=await r.json()}catch(e){oaShowError('registry-status',e.message);return}
 const sig=JSON.stringify(v);if(sig===registryLastView)return;registryLastView=sig;window.registryCurrentProviders=v.providers;
 for(const t of ['models','providers','remote'])el(t).replaceChildren();
 const sections=[
  {label:'Models',statusId:'models-status',failed:!!v.models_error,text:v.models_error,
   render:()=>{show('models-status',v.models.Models.length?'':'No models or stored model files reported.');modelsTable(v.models.Models)}},
  {label:'Registered providers',statusId:'providers-status',failed:!!(v.providers_error||v.providers.Outcome!=='page'),text:v.providers_error||v.providers.Outcome,
   render:()=>{show('providers-status',v.providers.Declarations.length?'':'No providers registered.');if(v.providers.Declarations.length)el('providers').append(providersTable(v.providers.Declarations,v.providers.Revision))}},
  {label:'Remote runtimes',statusId:'remote-status',failed:!!v.remote_error,text:v.remote_error,
   render:()=>{show('remote-status',v.remote.length?'':'No remote runtime reports model servers.');for(const h of v.remote)row('remote',[h.Host,h.Domain,h.Hosted?h.Wire:'local',h.DeclaredBy||'',list(h.Profiles),oaHostState(h.Up)],[null,null,null,null,null,h.Why||null])}},
 ];
 const failed=sections.filter(s=>s.failed);
 if(!failed.length){show('registry-status','')}
 else{
  const names=failed.map(s=>s.label).join(', ');
  const reasons=failed.map(s=>oaPlain(s.text).line);
  const sameReason=reasons.every(r=>r===reasons[0]);
  showDenial('registry-status',names+' could not be read.'+(sameReason?' '+reasons[0]:''),failed.map(s=>s.label+': '+s.text).join('\n'));
 }
 for(const s of sections){if(s.failed)show(s.statusId,'');else s.render()}
}
// The page updates itself while it is visible (Panel rules, 2026-10-07);
// there is no Refresh button.
var registryReadReady=read();
// oaQuiet runs one reading at a time: a timer tick while one is still
// running is skipped.
function oaQuietOnce(fn){let busy=false;return async()=>{if(busy)return;busy=true;try{await fn()}finally{busy=false}}}
const readQuiet=oaQuietOnce(read);
if(typeof setInterval==='function')setInterval(()=>{if(document.visibilityState!=='hidden')readQuiet()},15000);
</script><script>` + serversScript + `</script>`

// serversScript is the moved sections' own script, kept inside one function
// so its helpers never collide with the Providers page's own key, el, show
// and row.
const serversScript = `(function(){
const key=new URLSearchParams(location.search).get('k');let hostsRevision='',gatewayRevision='';
const el=id=>document.getElementById(id);function show(id,x){oaSetStatus(id,'ok',typeof x==='string'?x:JSON.stringify(x,null,2))}
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function shortName(path){return oaShortName(path)}
const outcomeText={forbidden:'The runtime did not permit this Panel; nothing was changed.',unavailable:'The runtime could not answer; nothing is assumed. Try again.',conflict:'The configuration changed or the program already holds a key; list again.',invalid:'The runtime refused the request as invalid.',unknown:'Nothing by that name.',no_secure_store:'The runtime has no platform store to hold a key.'};
function outcome(o,reason){return (outcomeText[o]||oaPlain(o).line)+(reason?' ('+reason+')':'')}
// row's own titles array names, per cell, the raw value behind a shortened
// or reformatted display (task 2026-09-23, tenth first-time visitor,
// findings 2 and 3: the Issued column read a raw ISO timestamp, and Issued
// by read a full path, both as visible text).
function row(tbody,cells,titles,button){const tr=document.createElement('tr');cells.forEach((c,i)=>{const td=document.createElement('td');td.textContent=c==null?'':String(c);if(titles&&titles[i])td.title=titles[i];tr.append(td)});if(arguments.length>3){const td=document.createElement('td');if(button)td.append(button);tr.append(td)}el(tbody).append(tr)}
function action(label,fn){const b=document.createElement('button');b.textContent=label;b.onclick=fn;return b}
// tableRow makes a row of n cells and an action cell; fillRow updates its
// cells' text and titles in place and swaps the action only when its label
// changes.
function tableRow(n){const tr=document.createElement('tr');for(let i=0;i<=n;i++)tr.append(document.createElement('td'));return tr}
function fillRow(tr,cells,titles,label,fn){cells.forEach((c,i)=>{const td=tr.children[i];td.textContent=c==null?'':String(c);td.title=titles&&titles[i]||''});const td=tr.children[cells.length];const b=td.children[0];if(!label){if(b)td.replaceChildren();return}if(b&&b.textContent===label){b.onclick=fn;return}td.replaceChildren(action(label,fn))}
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
function hostsStatus(l){el('hosts-status').title='Configuration revision '+l.Revision;if(el('hosts-status').getAttribute&&el('hosts-status').getAttribute('data-state')==='loading')oaSetStatus('hosts-status','ok','');el('hosts-empty').hidden=!!l.Hosts.length}
// The Kind cell carries both what the Class column used to say and what Kind
// always said: a local host's Kind is already one of a known set of local
// backends, and a hosted host's Kind is already
// one of a known set of wire formats (openai-compatible, anthropic-messages),
// so "hosted" or "local" was never new information next to it (task
// 2026-09-23 finding 3, the one visible row reading the same word twice).
// hostModalities names what a server serves: "all six" when it serves the
// full set, else the list.
const hostAllProfiles=['chat','embed','transcription','speech','image','live'];
function hostModalities(e){const p=e.Profiles||[];return hostAllProfiles.every(x=>p.includes(x))?'all modalities':(p.join(', ')||'no modalities')}
function hostDetails(e){return e.Base+' · '+(e.Profiles||[]).join(', ')+' · registered by '+(e.DeclaredBy||'')}
// hostNameLine puts the address and modalities as the Name cell's second line.
function hostNameLine(td,e){td.append(document.createElement('br'));const small=document.createElement('span');small.className='oa-row-caption';small.textContent=e.Base+' · '+hostModalities(e);td.append(small)}
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
let hostsLast='',gatewayLast='',keysLast='';
async function hosts(){try{const l=await call('/inference/hosts');{const sig=JSON.stringify(l);if(sig===hostsLast)return;hostsLast=sig}if(l.Outcome!=='page'){el('hosts').replaceChildren();hostsRevision='';el('hosts-status').title='';show('hosts-status',outcome(l.Outcome));return}hostsRevision=l.Revision;hostsStatus(l);oaKeyedRows(el('hosts'),l.Hosts,h=>h.Entry.Name,()=>tableRow(6),(tr,h)=>{const e=h.Entry;fillRow(tr,[e.Name,hostKind(e),e.Credential||'none',oaHostState(h.Up),h.Spend?h.Spend.Tokens+' tokens, '+currency(h.Spend.Micros)+' ('+h.Spend.Day+')':'0',e.Ceiling?e.Ceiling.TokensPerDay+' tokens, '+currency(e.Ceiling.MicrosPerDay):'no budget'],[hostDetails(e),null,null,h.Why||null],'Remove',()=>hostEdit({edit:'remove',name:e.Name}));hostNameLine(tr.children[0],e)});for(const h of l.Hosts){const e=h.Entry;if(e.Base)hostBasesSeen.add(e.Base);if(!e.Hosted&&e.Kind)hostKindsSeen.set(e.Kind,e.Base||'')}rebuildHostBaseSelect();rebuildHostNameSelect()}catch(e){hostsRevision='';el('hosts-status').title='';oaShowError('hosts-status',e.message)}}
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
var hostsReady=hosts();
// Add host's two required fields now name themselves when left blank (task
// 2026-09-23, seventh first-time visitor, finding 4, the Issue key
// pattern): the browser blocks the submit event for a failed required
// field without running this form's own onsubmit, but always fires
// "invalid" on the field, submit or not.
for(const id of ['host-name','host-base']){const label={'host-name':'Name','host-base':'Base URL'}[id];el(id).addEventListener('invalid',()=>show('hosts-status',label+' is required to add a model server.'))}
// Base URL opened as one empty field with no hint what to type; it now
// offers the documented default local server address (serve's own --base help text)
// and every base this page has already seen among the current hosts,
// "Other…" revealing the typed field for anything else (task 2026-09-23,
// eleventh first-time visitor, requirement 3).
let hostBasesSeen=new Set(['http://127.0.0.1:11434']);
function rebuildHostBaseSelect(){const select=el('host-base-select');const current=select.value;select.replaceChildren();for(const base of[...hostBasesSeen].sort()){const o=document.createElement('option');o.value=base;o.textContent=base;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);select.value=current&&(hostBasesSeen.has(current)||current==='__other__')?current:[...hostBasesSeen][0]}
el('host-base-select').onchange=()=>{const v=el('host-base-select').value;if(v==='__other__'){el('host-base').hidden=false;el('host-base').value='';el('host-base').focus()}else{el('host-base').hidden=true;el('host-base').value=v}};
el('host-base').hidden=true;rebuildHostBaseSelect();el('host-base').value=el('host-base-select').value;
el('host-add').onsubmit=ev=>{ev.preventDefault();const wire=el('host-wire').value.trim(),name=el('host-name').value.trim(),tokens=Number(el('host-tokens').value||0),micros=Math.round(Number(el('host-micros').value||0)*1e6);const profiles=hostProfilesChosen();const host={Name:name,Hosted:!!wire,Kind:wire||name,Base:el('host-base').value.trim(),Credential:el('host-credential').value.trim()};if(profiles.length)host.Profiles=profiles;if(tokens||micros)host.Ceiling={TokensPerDay:tokens,MicrosPerDay:micros};hostEdit({edit:'add',host})};
// gatewayText leaves the setting revision out of the sentence, the same
// treatment hostsStatus gives the hosts revision (task 2026-09-23 finding 3,
// "Setting revision gateway-v1:e3b0c44298fc1c149afbf4c8." sitting mid-text);
// gateway() below puts it on the element's title instead.
function gatewayText(s){if(s.Listening)return 'On. OpenAI apps use http://'+s.ListeningAddress+'/v1 and Anthropic apps use http://'+s.ListeningAddress+'.';if(s.Open)return 'On at '+s.Address+', but not listening'+(s.Why?': '+s.Why:'')+'.';return 'Off.'}
async function gateway(){try{const s=await call('/inference/gateway');{const sig=JSON.stringify(s);if(sig===gatewayLast)return;gatewayLast=sig}if(s.Outcome!=='page'){gatewayRevision='';el('gateway-status').title='';show('gateway-status',outcome(s.Outcome));return}gatewayRevision=s.Revision;el('gateway-status').title='Setting revision '+s.Revision;show('gateway-status',gatewayText(s));el('gateway-switch').checked=!!s.Open;if(s.Address&&s.Address!==gatewayShown){gatewayShown=s.Address;gatewayAddressChoose(s.Address)}}catch(e){gatewayRevision='';el('gateway-status').title='';oaShowError('gateway-status',e.message)}}
// gatewaySet used to refuse before the setting had ever been read; gateway()
// below now reads it on arrival, so that precondition goes the same way
// hostEdit's did (task 2026-09-23, fourteenth first-time visitor, findings
// 2 and 3).
async function gatewaySet(open){try{const c=await call('/inference/gateway',{revision:gatewayRevision,open,address:open?gatewayAddress():''});if(c.Outcome!=='applied'){show('gateway-status',outcome(c.Outcome,c.Reason));return}await gateway()}catch(e){oaShowError('gateway-status',e.message)}}
el('gateway-set').onsubmit=ev=>{ev.preventDefault()};el('gateway-switch').onchange=()=>gatewaySet(!!el('gateway-switch').checked);
var gatewayReady=gateway();
// time reads a raw epoch millisecond value as a local readable time; the
// raw ISO value moves to the cell's own title (task 2026-09-23, tenth
// first-time visitor, finding 2: "Issued 2026-09-23T15:09:12.464Z").
function time(ms){return ms?oaLocalTime(ms):''}
async function keys(){try{const l=await call('/inference/keys');{const sig=JSON.stringify(l);if(sig===keysLast)return;keysLast=sig}if(l.Outcome!=='page'){el('keys').replaceChildren();show('keys-status',outcome(l.Outcome));return}if(el('keys-status').getAttribute&&el('keys-status').getAttribute('data-state')==='loading')oaSetStatus('keys-status','ok','');el('keys-empty').hidden=!!l.Keys.length;oaKeyedRows(el('keys'),l.Keys,k=>k.Program+'\n'+k.IssuedUnixMs,()=>tableRow(5),(tr,k)=>fillRow(tr,[shortName(k.Program),k.State,k.Credential||'none',time(k.IssuedUnixMs),shortName(k.IssuedBy)],[k.Program,null,null,k.IssuedUnixMs?new Date(k.IssuedUnixMs).toISOString():null,k.IssuedBy],k.State==='active'?'Revoke':'',()=>keyEdit({edit:'revoke',program:k.Program})));for(const k of l.Keys){if(k.Program)keyProgramsSeen.add(k.Program)}keyProgramChooser.rebuild()}catch(e){oaShowError('keys-status',e.message)}}
// Issue key's own Program path was a bare text box while Rights already
// offered a chooser for the same kind of field; both now build their own
// equivalent chooser from oaProgramChooser (panel_shell.go), the one
// shared implementation (task 2026-09-23, fourteenth first-time visitor,
// finding 8). Known programs here are the registered applications and
// every program already issued a key (keys() above).
let keyProgramsSeen=new Set();
const keyProgramChooser=oaProgramChooser('key-program-select','key-program',()=>[...keyProgramsSeen]);
keyProgramChooser.rebuild();var keyDeviceHoldersReady=keyProgramChooser.ready;
// keyEdit's own reply used to say "Issued. List again to refresh." (or
// "Revoked...") while the table under it kept its stale rows; it now
// refreshes the table itself before showing the confirmation (task
// 2026-09-23, fourteenth first-time visitor, finding 3).
let keyShownAt=0;
function keyClear(){keyShownAt=0;el('key-shown').textContent='';el('key-shown-row').hidden=true}
el('key-copy').onclick=async()=>{const k=el('key-shown').textContent;if(!k){oaSetStatus('key-copy-status','ok','The key is no longer shown; issue a new one.');return}try{await navigator.clipboard.writeText(k);oaSetStatus('key-copy-status','ok','Copied the key.')}catch(e){oaSetStatus('key-copy-status','error','The key could not be copied; select it and copy it by hand.')}};
if(typeof addEventListener==='function'){addEventListener('pagehide',keyClear);if(typeof document.addEventListener==='function')document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='hidden')keyClear()})}
async function keyEdit(body){keyClear();try{const r=await call('/inference/keys',body);if(r.Outcome!=='applied'){show('keys-status',outcome(r.Outcome,r.Reason));return}if(body.edit==='issue'){el('key-shown').textContent=r.Key;el('key-shown-row').hidden=false;keyShownAt=Date.now();oaSetStatus('key-copy-status','ok','Key for '+shortName(r.Record.Program)+'.')}await keys();show('keys-status',body.edit==='issue'?'Issued.':'Revoked. The gateway refuses the key from its next request.')}catch(e){oaShowError('keys-status',e.message)}}
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
// Server chooses a local server kind the runtime already knows: the kinds
// of the servers registered so far and of the model servers providers
// declared (window.registryCurrentProviders, the Providers script's own
// reading). Choosing a kind also chooses its address. A hosted API, or a
// kind nobody declared, takes a typed name behind "Other…".
let hostKindsSeen=new Map();
function hostKindsFromProviders(){const decls=(window.registryCurrentProviders&&window.registryCurrentProviders.Declarations)||[];for(const p of decls){const h=p.Declaration&&p.Declaration.Host;if(h&&h.Kind&&!hostKindsSeen.has(h.Kind)){hostKindsSeen.set(h.Kind,h.Base||'');if(h.Base)hostBasesSeen.add(h.Base)}}}
function rebuildHostNameSelect(){hostKindsFromProviders();const select=el('host-name-select');const current=select.value;select.replaceChildren();if(!el('host-wire').value)for(const kind of hostKindsSeen.keys()){const o=document.createElement('option');o.value=kind;o.textContent=kind;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);const kinds=el('host-wire').value?[]:[...hostKindsSeen.keys()];select.value=current&&(kinds.includes(current)||current==='__other__')?current:(kinds[0]||'__other__');hostNameSync()}
function hostNameSync(){const v=el('host-name-select').value;if(v==='__other__'){el('host-name').hidden=false;if(hostKindsSeen.has(el('host-name').value))el('host-name').value=''}else{el('host-name').hidden=true;el('host-name').value=v;const base=hostKindsSeen.get(v);if(base){hostBasesSeen.add(base);rebuildHostBaseSelect();el('host-base-select').value=base;el('host-base').hidden=true;el('host-base').value=base}}}
// Modalities are toggle buttons for what the chosen API serves (serve's
// server add help); all on means the API's default, so none are sent.
const hostApiProfiles={'':['chat','embed','transcription','speech','image','live'],'openai-compatible':['chat','embed','transcription','speech','image','live'],'anthropic-messages':['chat']};
const hostProfileWords={chat:'Chat',embed:'Embeddings',transcription:'Transcription',speech:'Speech',image:'Images',live:'Live voice'};
function rebuildHostProfiles(){const out=el('host-profiles');out.replaceChildren();for(const p of hostApiProfiles[el('host-wire').value]||['chat']){const b=document.createElement('button');b.type='button';b.textContent=hostProfileWords[p]||p;b.dataset=b.dataset||{};b.dataset.profile=p;const press=v=>{b.pressed=v;if(b.setAttribute)b.setAttribute('aria-pressed',String(v))};press(true);b.onclick=()=>{press(!b.pressed);oaSetStatus('host-profiles-status','ok',b.textContent+(b.pressed?' on.':' off.'))};out.append(b)}}
function hostProfilesChosen(){const all=[...el('host-profiles').children];const on=all.filter(b=>b.pressed).map(b=>b.dataset.profile);return on.length===all.length?[]:on}
el('host-name-select').onchange=hostNameSync;el('host-wire').onchange=()=>{rebuildHostNameSelect();rebuildHostProfiles()};rebuildHostProfiles();
rebuildHostNameSelect();if(window.registryReadReady&&window.registryReadReady.then)window.registryReadReady.then(rebuildHostNameSelect);
// Address offers the gateway's recorded address and its documented default
// (serve's gateway on help), with "Other…" for a typed one.
function gatewayAddressChoose(address){const select=el('gateway-address');const known=[...new Set([address,'127.0.0.1:8793'].filter(Boolean))];select.replaceChildren();for(const a of known){const o=document.createElement('option');o.value=a;o.textContent=a;select.append(o)}const other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);select.value=known[0];el('gateway-address-other').hidden=true}
let gatewayShown='';
function gatewayAddress(){const v=el('gateway-address').value;return v==='__other__'?el('gateway-address-other').value.trim():v}
el('gateway-address').onchange=()=>{el('gateway-address-other').hidden=el('gateway-address').value!=='__other__'};
gatewayAddressChoose('');
// Key lists the registered credentials, gateway keys excepted, for both
// forms; None is the default.
async function loadCredentialNames(){try{const p=await call('/credentials?cursor=');if(!p||p.Outcome!=='page')return;for(const id of ['host-credential','key-credential']){const select=el(id),current=select.value;select.replaceChildren();const none=document.createElement('option');none.value='';none.textContent='None';select.append(none);for(const m of p.Records||[]){if(m.State==='revoked'||m.Kind==='openabstractions/local-key@1')continue;const o=document.createElement('option');o.value=m.Name;o.textContent=m.Name;select.append(o)}select.value=current||''}}catch(e){}}
var credentialNamesReady=loadCredentialNames();
// The sections update themselves while the page is visible; the Panel has
// no Refresh buttons. serversRefresh lets a test drive one reading.
window.serversRefresh={hosts,gateway,keys};
// The page tells the screenshot tool it is ready once its first readings
// have finished (S1's oaReady; data-ready until it exists).
function oaPageReady(){if(typeof oaReady==='function')oaReady();else if(typeof document!=='undefined'&&document.documentElement)document.documentElement.dataset.ready='1'}
Promise.all([window.registryReadReady,hostsReady,gatewayReady,keysReady]).then(oaPageReady,oaPageReady);
function oaQuietOnce(fn){let busy=false;return async()=>{if(busy)return;busy=true;try{await fn()}finally{busy=false}}}
const serversQuiet=oaQuietOnce(()=>{if(keyShownAt&&Date.now()-keyShownAt>60000)keyClear();return Promise.all([hosts(),gateway(),keys()])});
window.serversTick=serversQuiet;
if(typeof setInterval==='function')setInterval(()=>{if(document.visibilityState!=='hidden')serversQuiet()},15000);
})();
`
