package main

// registryPage is the Panel's registry page: the hosts the runtime reaches
// with who declared each and the profiles it serves, the providers declared in
// abstraction.facade/registry@1 with their readiness and described contracts,
// and the hosts remote runtimes report.
// It renders every value as text and calls only /registry/view with the panel
// key.
const registryPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Registry</title>
<style>body{font:16px system-ui;max-width:1200px;margin:2rem auto;padding:1rem}button{font:inherit;margin:.3rem;padding:.4rem}section{border-top:1px solid #aaa;padding:1rem 0}table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #ddd;padding:.3rem;text-align:left;vertical-align:top;overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style></head><body>
<h1>Registry</h1><p>Where the runtime's hosts and providers come from: a person's configuration (operator), a product's own record of where it listens, the router's built-in address (default), a provider declaration, or another runtime under explicit trust. Reading it is decided as host.manage, provider.manage and router inventory.read for this Panel.</p>
<button id="registry-read">Read</button><pre id="registry-status"></pre>
<section><h2>Hosts</h2><pre id="hosts-status"></pre><table><thead><tr><th>Name</th><th>Class</th><th>Kind</th><th>Base</th><th>Declared by</th><th>Profiles</th><th>Readiness</th></tr></thead><tbody id="hosts"></tbody></table></section>
<section><h2>Providers</h2><p>Processes outside the runtime and other runtimes, declared in the registry. Ready means the process at the endpoint runs the declared program and describes every declared contract ready.</p><pre id="providers-status"></pre><table><thead><tr><th>Name</th><th>Activation</th><th>Contracts</th><th>Endpoint</th><th>Program</th><th>Resources</th><th>Readiness</th><th>Described</th><th>Restarts</th><th>Accepted</th><th>Declared by</th></tr></thead><tbody id="providers"></tbody></table></section>
<section><h2>Remote runtimes</h2><p>Hosts another runtime reports, in its domain. Their credentials stay on that runtime.</p><pre id="remote-status"></pre><table><thead><tr><th>Host</th><th>Domain</th><th>Kind</th><th>Declared by</th><th>Profiles</th><th>Readiness</th></tr></thead><tbody id="remote"></tbody></table></section>
<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);function show(id,x){el(id).textContent=x}
const outcomeText={forbidden:'The runtime did not permit this Panel to read this.',unavailable:'The runtime could not answer; nothing is assumed. Read again.',invalid:'The runtime refused the request as invalid.'};
function row(tbody,cells){const tr=document.createElement('tr');for(const c of cells){const td=document.createElement('td');td.textContent=c==null?'':String(c);tr.append(td)}el(tbody).append(tr)}
function list(x){return (x||[]).join(', ')}
function state(up,why){return up?'up':'down'+(why?': '+why:'')}
async function read(){
 show('registry-status','Reading…');
 let v;try{const r=await fetch('/registry/view',{headers:{'X-Panel-Key':key}});if(!r.ok)throw Error(await r.text());v=await r.json()}catch(e){show('registry-status',e.message);return}
 show('registry-status','');for(const t of ['hosts','providers','remote'])el(t).replaceChildren();
 if(v.hosts_error)show('hosts-status',v.hosts_error);else if(v.hosts.Outcome!=='page')show('hosts-status',outcomeText[v.hosts.Outcome]||('Outcome: '+v.hosts.Outcome));else{show('hosts-status',v.hosts.Hosts.length?'':'No hosts.');for(const h of v.hosts.Hosts){const e=h.Entry;row('hosts',[e.Name,e.Hosted?'hosted':'local',e.Kind,e.Base,e.DeclaredBy||'',list(e.Profiles),state(h.Up,h.Why)])}}
 if(v.providers_error)show('providers-status',v.providers_error);else if(v.providers.Outcome!=='page')show('providers-status',outcomeText[v.providers.Outcome]||('Outcome: '+v.providers.Outcome));else{show('providers-status',v.providers.Declarations.length?'':'No providers declared.');for(const p of v.providers.Declarations){const d=p.Declaration;row('providers',[d.Name,d.Activation,list(d.Contracts),d.Endpoint+(d.Remote?' ('+d.Remote.ServerName+')':''),d.Program,list(d.Resources),p.Readiness+(p.Why?': '+p.Why:''),(p.Described||[]).map(s=>s.Contract+' '+s.Readiness).join(', '),p.Restarts,list(p.Accepted),p.DeclaredBy])}}
 if(v.remote_error)show('remote-status',v.remote_error);else{show('remote-status',v.remote.length?'':'No remote runtime reports hosts.');for(const h of v.remote)row('remote',[h.Host,h.Domain,h.Hosted?h.Wire:'local',h.DeclaredBy||'',list(h.Profiles),state(h.Up,h.Why)])}
}
el('registry-read').onclick=read;
</script></body></html>`
