package main

// registryPage is the Panel's Providers page: the model servers the runtime
// reaches with who registered each and the modalities it serves, the
// providers registered in abstraction.facade/registry@1 with their readiness
// and described services, and the model servers remote runtimes report. Its
// words follow research/vocabulary/DECISION.md: Providers and Registered
// providers (D41, D42), model server (D11), modality (D82) and the three
// role phrases of D10; the wire role values provider, host and remote stay.
// It renders every value as text and calls only /registry/view with the panel
// key. Its own grant widget's status line is a hand-written copy of
// grantWidgetHTML's markup (panel_shell.go), predating that shared
// function; it carried the same <pre>-renders-a-plain-sentence-monospace
// defect that fix corrected there and missed this copy (task 2026-09-23,
// fourteenth first-time visitor, finding 1).
const registryPage = `<h1>Providers</h1><p>Inference is where you use model servers; this page is where the runtime learns about them: which app or file registered each one, and the providers this device runs itself. A model server or provider comes from something you configured, an app's own record of where it listens, a built-in default address, a provider's registration, or another trusted runtime. Seeing this needs <span title="abstraction.inference/host.manage">inference server manage</span>, <span title="abstraction.facade/provider.manage">provider manage</span>, and <span title="abstraction.router/inventory.read">router servers</span>.</p><div class="oa-grant" id="registry-grant" data-grant-rules="abstraction.inference/host.manage,abstraction.facade/provider.manage,abstraction.router/inventory.read|abstraction.router/inventory" data-grant-resource="account"><button type="button">Grant these to this Panel</button><p class="oa-grant-status" aria-live="polite"></p></div>
<button id="registry-read">Refresh</button><pre id="registry-status"></pre>
<section id="hosts-section"><h2>Model servers</h2><pre id="hosts-status"></pre><div class="oa-table"><table><thead><tr><th>Name</th><th>Kind</th><th>Base</th><th>Registered by</th><th>Modalities</th><th>Readiness</th></tr></thead><tbody id="hosts"></tbody></table></div></section>
<section id="providers-section"><h2>Registered providers</h2><p>Every registered provider and its role: a managed process this device runs, a model server, or a remote runtime, which is another trusted runtime. Registered by installation names the file that added it. Ready means the process at that address is running and describes everything it should; for a model server, it means the last check reached the engine.</p><pre id="providers-status"></pre><div id="providers"></div></section>
<section id="remote-section"><h2>Remote runtimes</h2><p>Model servers reported by another runtime in its own domain. Their credentials stay on that runtime.</p><pre id="remote-status"></pre><div class="oa-table"><table><thead><tr><th>Model server</th><th>Domain</th><th>Kind</th><th>Registered by</th><th>Modalities</th><th>Readiness</th></tr></thead><tbody id="remote"></tbody></table></div></section>
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);function show(id,x){el(id).textContent=x}
// row's own titles array names, per cell, the raw value behind a plain
// reading (task 2026-09-23, eleventh first-time visitor, finding 5: the
// State column read a raw connection error as visible text).
function row(tbody,cells,titles){const tr=document.createElement('tr');cells.forEach((c,i)=>{const td=document.createElement('td');td.textContent=c==null?'':String(c);if(titles&&titles[i])td.title=titles[i];tr.append(td)});el(tbody).append(tr)}
function list(x){return (x||[]).join(', ')}
// rolePlain reads a registration's wire role (facade FAC-R6's closed enum) as
// the Panel's phrase for it (DECISION.md D10); the wire value stays on the
// cell's own title.
const rolePlain={provider:'managed process',host:'model server',remote:'remote runtime'};
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
function providersTable(decls){
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
 const table=document.createElement('table');
 const thead=document.createElement('thead');thead.innerHTML='<tr>'+headers.map(x=>'<th>'+x+'</th>').join('')+'</tr>';
 const tbody=document.createElement('tbody');
 for(const p of decls){
  const d=p.Declaration;
  const cells=[d.Name,rolePlain[p.Role]||p.Role||''];
  if(cols.activation)cells.push(d.Activation||'not set');
  if(cols.contracts)cells.push((d.Contracts||[]).length?list(d.Contracts):'none');
  if(cols.endpoint)cells.push(d.Endpoint?d.Endpoint+(d.Remote?' ('+d.Remote.ServerName+')':''):'none');
  if(cols.program)cells.push(d.Program||'none');
  cells.push(d.Host?(d.Host.Kind+' '+d.Host.Base):'');
  if(cols.resources)cells.push((d.Resources||[]).length?list(d.Resources):'none');
  cells.push(p.Readiness+(p.Why?': '+p.Why:''));
  if(cols.described)cells.push((p.Described||[]).length?p.Described.map(s=>s.Contract+' '+s.Readiness).join(', '):'not yet');
  cells.push(p.Restarts);
  if(cols.accepted)cells.push((p.Accepted||[]).length?list(p.Accepted):'none');
  cells.push(p.DeclaredBy);
  const tr=document.createElement('tr');cells.forEach((c,i)=>{const td=document.createElement('td');td.textContent=c==null?'':String(c);if(i===1&&p.Role)td.title=p.Role;tr.append(td)});
  tbody.append(tr);
 }
 table.append(thead,tbody);
 // .oa-table is the wrapper that scrolls sideways when this table's own
 // headers cannot fit, never the page (task 2026-09-23, eighth first-time
 // visitor, finding 6).
 const wrap=document.createElement('div');wrap.className='oa-table';wrap.append(table);
 return wrap;
}
// showDenial writes one plain sentence naming which tables a single "Show"
// click could not read, with each table's raw reason kept behind the same
// disclosure oaShowError uses, rather than three separately worded denials
// on three lines (task 2026-09-23, first-time-user walkthrough: "The runtime
// did not permit this Panel to read this." twice, then "router operation not
// permitted", for what was in every case the same one click).
function showDenial(id,line,detail){
 const e=el(id);if(e.replaceChildren)e.replaceChildren();e.textContent=line;
 if(detail){const d=document.createElement('details');d.className='oa-error-details';const s=document.createElement('summary');s.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=detail;d.append(s,pre);e.append(d)}
}
// read used to run only on a "Show hosts, providers and remote runtimes"
// click, so the page opened as headers and no rows at all (task
// 2026-09-23, fifteenth first-time visitor, finding 2: every other list
// on this Panel already reads itself on arrival). It now also runs once,
// unclicked, as soon as the page's own script loads; the button, renamed
// Refresh, still re-runs it. It also refreshes the grant widget above,
// the same fetch its own arrival check makes, so a rule granted since
// this page opened shows up here without a reload (task 2026-09-23,
// fifteenth first-time visitor, finding 1).
async function read(){
 show('registry-status','Reading…');
 if(window.oaGrantRefresh)oaGrantRefresh();
 let v;try{const r=await fetch('/registry/view',{headers:{'X-Panel-Key':key}});if(!r.ok)throw Error(await r.text());v=await r.json()}catch(e){oaShowError('registry-status',e.message);return}
 for(const t of ['hosts','providers','remote'])el(t).replaceChildren();
 const sections=[
  {label:'Model servers',statusId:'hosts-status',failed:!!(v.hosts_error||v.hosts.Outcome!=='page'),text:v.hosts_error||v.hosts.Outcome,
   render:()=>{show('hosts-status',v.hosts.Hosts.length?'':'No model servers.');for(const h of v.hosts.Hosts){const e=h.Entry;row('hosts',[e.Name,(e.Hosted?'hosted: ':'local: ')+e.Kind,e.Base,e.DeclaredBy||'',list(e.Profiles),oaHostState(h.Up)],[null,null,null,null,null,h.Why||null])}}},
  {label:'Registered providers',statusId:'providers-status',failed:!!(v.providers_error||v.providers.Outcome!=='page'),text:v.providers_error||v.providers.Outcome,
   render:()=>{show('providers-status',v.providers.Declarations.length?'':'No providers registered.');if(v.providers.Declarations.length)el('providers').append(providersTable(v.providers.Declarations))}},
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
el('registry-read').onclick=read;
var registryReadReady=read();
</script><script>oaGrant('registry-grant','registry-read')</script>`
