package main

// serviceCommonScript is shared by the Status page and the six pages split
// out of it (task 2026-09-23, requirement 1: each of Work, Questions and
// rights, Settings, Logging, Identity and Explore gets its own page instead
// of scrolling one long Status page). Every one of those pages already used
// this same handful of DOM and fetch helpers inline; this is that text once,
// prefixed onto each page's own script tag, so it still reads as one
// self-contained script block, the shape monitor/*_page_test.js expects.
const serviceCommonScript = `const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);function show(id,x){el(id).textContent=typeof x==='string'?x:JSON.stringify(x,null,2)}
function fail(id,e){ if (typeof oaShowError === 'function') oaShowError(id, e.message || String(e)); else show(id, e.message || String(e)) }
async function call(path,body,signal){const r=await fetch(path,{signal,method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function para(parent,text,cls,title){const p=document.createElement('p');p.textContent=text;if(cls)p.className=cls;if(title)p.title=title;parent.append(p);return p}
function heading(parent,text){const h=document.createElement('h3');h.textContent=text;parent.append(h)}
function shortName(path){const parts=String(path).split(/[\\/]/).filter(Boolean);return parts.length?parts[parts.length-1]:path}
function capitalize(s){return s?s.charAt(0).toUpperCase()+s.slice(1):s}
`

// servicePage is the Panel's Status page: what needs the person's answer,
// and short links to every other page (task 2026-09-23, fourth first-time
// visitor: the six sections below used to be anchors scrolled to on this
// same page; each is now its own page at its own path, so Status keeps only
// its own intro, the needs-you line, Check the service, and these links).
const servicePage = `<style>.oa-note{color:var(--oa-text-secondary);font-size:.9em;margin:.2em 0}</style><h1 id="service-check">Status</h1><p class="oa-note">What applications on this account do through the shared runtime, and what needs your answer.</p>
<p id="needs-you" aria-live="polite"></p>
<button id="status">Check the service</button><pre id="check-status"></pre><div id="runtime"></div>
<section><h2>Work</h2><p>Downloads and other work started from this Panel or by other apps.</p><a id="work-link" href="#">Open Work</a></section>
<section><h2>Questions and permissions</h2><p>Questions waiting for your answer, and the rules that decide what apps may do.</p><a id="rights-link" href="#">Open Questions and permissions</a></section>
<section><h2>Inference</h2><p>Model servers, spending per credential, the inference gateway and its keys, and the inference history.</p><a id="inference-link" href="#">Open Inference</a></section>
<section><h2>Device</h2><p>Who's using a limited resource on this device right now, what's downloading, and what's keeping the device awake.</p><a id="card-link" href="#">Open Device</a></section>
<section><h2>Providers</h2><p>Registered providers and model servers: who registered each one, its readiness, and the model servers other runtimes report.</p><a id="registry-link" href="#">Open Providers</a></section>
<section><h2>Credentials</h2><p>Saved credentials and which apps may use them.</p><a id="credentials-link" href="#">Open Credentials</a></section>
<section><h2>Settings</h2><p>Your saved settings, and what's in effect right now.</p><a id="settings-link" href="#">Open Settings</a></section>
<section><h2>Logging</h2><p>Records kept by the runtime.</p><a id="logging-link" href="#">Open Logging</a></section>
<section><h2>Identity</h2><p>Which runtime this device is using and what this platform can prove about it.</p><a id="identity-link" href="#">Open Identity</a></section>
<section><h2>Explore</h2><p>Try each capability as this Panel and see the result next to the rule that allows or blocks it.</p><a id="explore-link" href="#">Open Explore</a></section>
<script>` + serviceCommonScript + `
el('work-link').href='/work?k='+encodeURIComponent(key);
el('rights-link').href='/rights?k='+encodeURIComponent(key);
el('inference-link').href='/inference?k='+encodeURIComponent(key);
el('registry-link').href='/registry?k='+encodeURIComponent(key);
el('card-link').href='/card?k='+encodeURIComponent(key);
el('credentials-link').href='/credentials-page?k='+encodeURIComponent(key);
el('settings-link').href='/settings?k='+encodeURIComponent(key);
el('logging-link').href='/logging?k='+encodeURIComponent(key);
el('identity-link').href='/identity?k='+encodeURIComponent(key);
el('explore-link').href='/explore?k='+encodeURIComponent(key);
function jobActive(s){return s.State==='pending'||s.State==='running'||s.State==='transferred'}
function countWord(n,word){return n+' '+word+(n===1?'':'s')}
async function needsYou(){try{const q=await call('/questions?cursor=');if(q.Outcome!=='page')return;let pending=0;for(const rec of q.Records)if(!rec.Option)pending++;const inv=await call('/inventory?cursor=');if(inv.Outcome!=='page')return;let downloading=0;for(const s of inv.Snapshots)if(jobActive(s))downloading++;try{const acct=await call('/account-work?cursor=');if(acct.Outcome==='page')for(const s of acct.Snapshots)if(jobActive(s))downloading++}catch(e){}const parts=[];parts.push(pending?countWord(pending,'app')+(pending===1?' is':' are')+' waiting for your answer under Questions.':'No app is waiting on you.');parts.push(downloading?countWord(downloading,'download')+(downloading===1?' is':' are')+' in progress under Work.':'Nothing is downloading.');show('needs-you',parts.join(' '))}catch(e){}}
var needsYouReady=needsYou();
// endpointShortName reads the meaningful tail of a runtime endpoint (a named
// pipe path on Windows, an absolute socket path elsewhere): the isolated
// runtime's own name and its service suffix, e.g. "panel9-runtime" out of
// "\\.\pipe\openabstractions-user-S-1-5-21-...-panel9-runtime", skipping the
// numeric security identifier in between (task 2026-09-23, seventh
// first-time visitor, finding 3: "the runtime named when it started" named
// nothing). Walking back from the last "-"-separated token, everything that
// is not purely numeric and not "S" or "user" (the security identifier's own
// shape) belongs to that tail; a name with no such tail (nothing to skip)
// returns its own basename unchanged.
function endpointShortName(endpoint){
 const base=String(endpoint||'').split(/[\\/]/).filter(Boolean).pop()||'';
 const parts=base.split('-');
 let start=parts.length;
 while(start>0){const p=parts[start-1];if(p===''||p==='S'||p==='user'||/^[0-9]+$/.test(p))break;start--}
 return start<parts.length?parts.slice(start).join('-'):base;
}
function runtimeSentence(v){if(v.explicit)return 'This Panel is connected to the runtime at '+endpointShortName(v.explicit.endpoint)+'.';if(v.bootstrap!=='unknown')return 'This Panel is connected to the installed runtime.';return 'This Panel is running from a development build; no installed runtime was found next to it.'}
const capabilityPlainStatus={unavailable:1,forbidden:1,incompatible:1,invalid_request:1};
function capabilityLine(c){if(!capabilityPlainStatus[c.status])return c.name+': '+c.label;return c.name+': '+oaPlain('service resolution: '+c.status+': '+c.contract).line}
function renderRuntime(v){const out=el('runtime');out.replaceChildren();para(out,runtimeSentence(v));if(v.detail&&!v.explicit)para(out,v.detail);para(out,v.error?'The service did not answer: '+v.error:'The service answered.');for(const c of v.capabilities||[])para(out,capabilityLine(c),c.status==='resolved'?'':c.status==='forbidden'?'error':'');const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='Technical details';const pre=document.createElement('pre');pre.textContent=JSON.stringify(v,null,2);details.append(summary,pre);out.append(details)}
// Check the service used to write nothing a second click could tell apart
// from the first: renderRuntime always rebuilds the same content from the
// same reply, with no line saying a check had actually just run (task
// 2026-09-23, thirteenth first-time visitor, finding 1). One line beside
// the button now names when this click's own answer came back.
el('status').onclick=async()=>{try{const v=await call('/runtime');renderRuntime(v);el('check-status').textContent='Checked at '+oaClockText(new Date(),false)+': '+(v.error?'the service did not answer: '+v.error:'the service answered')+'.'}catch(e){el('runtime').replaceChildren();fail('check-status',e)}};
</script>`
