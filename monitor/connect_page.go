package main

// connectSection is the Credentials page's Connect a service group (P-M9):
// one row per hosted service preset (router's hostedcatalogue, served by
// /connect), each reading today's spend and its daily budget from the
// inference operator's server list, with a chooser of budgets; a service not
// yet connected offers Connect…, which asks for its key and a budget. The
// last row shows the gateway's address as selectable text: an app gives it
// as its base URL with any key, and OpenAbstractions applies the saved key
// (CONTRACT.md INF-W4, INF-W5). A preset's budget is money or tokens,
// whichever its replies carry (INF-C2): money is currency millionths on the
// wire and two decimals here, as the Providers page shows it. Its words
// follow research/vocabulary/DECISION.md: model server (D11), budget (D64),
// credential (credentials row 9).
var connectSection = runtimeGroup(oaGroup("connect", "Connect a service", "Use your key in any app through OpenAbstractions.",
	oaStatus("connect-status"), `<div id="connect-rows"></div>`,
	oaRow("Address for apps", "", `<code id="connect-gateway"></code>`, "connect-gateway-status"))) + connectScript

const connectScript = `<script>
(function(){
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n}
// The budgets a person picks from, per unit: money in currency millionths,
// tokens as counted.
const connectBudgets={money:{values:[0,1e6,2e6,5e6,20e6],start:2e6},tokens:{values:[0,1e5,1e6,5e6,20e6],start:1e6}};
function unitOf(item){return item.preset&&item.preset.budget==='tokens'?'tokens':'money'}
function amount(micros){return (micros/1e6).toFixed(2)}
function grouped(n){return String(n).replace(/\B(?=(\d{3})+(?!\d))/g,',')}
function quantity(unit,n){return unit==='tokens'?grouped(n)+' tokens':amount(n)}
function dailyLimit(unit){return unit==='tokens'?'daily token limit':'daily spending limit'}
function budgetLabel(unit,n){return n>0?quantity(unit,n)+' a day':'No '+dailyLimit(unit)}
function budgetOf(unit,h){const c=h.Entry.Ceiling||{};return (unit==='tokens'?c.TokensPerDay:c.MicrosPerDay)||0}
function spentOf(unit,h){const s=h.Spend||{};return (unit==='tokens'?s.Tokens:s.Micros)||0}
function budgetChooser(unit,current,label){const s=node('select');if(s.setAttribute)s.setAttribute('aria-label',label);const listed=connectBudgets[unit].values,values=listed.includes(current)?listed:listed.concat([current]);for(const v of values){const o=node('option',null,budgetLabel(unit,v));o.value=String(v);if(v===current)o.selected=true;s.append(o)}s.value=String(current);return s}
// spendLine is a connected row's caption: "0.42 today, budget 2.00 a day"
// or "12,000 tokens today, budget 1,000,000 tokens a day".
function spendLine(unit,h){const budget=budgetOf(unit,h);return quantity(unit,spentOf(unit,h))+' today, '+(budget>0?'budget '+quantity(unit,budget)+' a day':'no '+dailyLimit(unit))}
let connectPresets=[],connectHosts=[],connectSeq=0;
// connectItems: every preset with a fixed address, then each hosted server
// the operator connected through the generic preset, then the generic row.
function connectItems(){const fixed=connectPresets.filter(p=>p.base),generic=connectPresets.filter(p=>!p.base);const items=fixed.map(p=>({key:p.id,preset:p,host:connectHosts.find(h=>h.Entry.Hosted&&(h.Entry.Name===p.credential&&h.Entry.Base===p.base&&h.Entry.Kind===p.api))}));for(const h of connectHosts)if(h.Entry.Hosted&&h.Entry.DeclaredBy==='operator'&&h.Entry.Credential&&!fixed.some(p=>p.credential===h.Entry.Name&&p.base===h.Entry.Base&&p.api===h.Entry.Kind))items.push({key:'server:'+h.Entry.Name,preset:generic[0]||null,host:h,label:h.Entry.Name});for(const p of generic)items.push({key:p.id,preset:p});return items}
function createConnectRow(item){const row=node('div','oa-row'),text=node('div','oa-row-text'),label=node('span','oa-row-label'),caption=node('span','oa-row-caption'),control=node('div','oa-row-control'),status=node('p','oa-status');status.id='connect-line-'+(++connectSeq);if(status.setAttribute){status.setAttribute('role','status');status.setAttribute('aria-live','polite')}text.append(label,caption);row.append(text,control,status);row.parts={label,caption,control,status};return row}
function rowName(item){return item.label||item.preset.label}
// savedKey is the saved credential named for a preset that has no model
// server yet (the Credentials list publishes the saved records); such a key
// was saved with other settings, so the row says it is saved, not connected.
function savedKey(item){const saved=window.credentialsSaved,p=item.preset,name=item.host&&item.host.Entry.Credential||p&&p.credential;const m=saved&&name&&saved.get(name);return !!m&&m.State==='active'}
// Registration and a confirmed connection are separate service facts. Saving
// a key configures the server; only the server list's Up observation confirms it.
function updateConnectRow(row,item){row.item=item;row.parts.label.textContent=rowName(item);const source=item.preset&&item.preset.kind==='download',configured=!source&&!!item.host,unit=unitOf(item);row.parts.caption.textContent=source?'Save or replace a credential for private downloads from api.github.com.':configured?(item.host.Up===true?'Connected':'Configured; connection not confirmed')+' · '+spendLine(unit,item.host):(item.preset&&!item.preset.base?'Any model server that speaks the OpenAI API.':savedKey(item)?'A key is saved; connect to let apps use it.':'Not connected');const mode=configured?'budget:'+budgetOf(unit,item.host):'connect';if(row.mode===mode)return;row.mode=mode;row.parts.control.replaceChildren();if(configured){const current=budgetOf(unit,item.host);const s=budgetChooser(unit,current,'Budget per day for '+rowName(item));s.onchange=()=>setBudget(row,Number(s.value));row.parts.control.append(s)}else{const b=node('button',null,source?'Save credential…':'Connect…');b.type='button';b.onclick=()=>openConnect(row);row.parts.control.append(b)}}
// openConnect shows the key, the budget and, for the generic row, the
// address; nothing is typed before the person asks.
function closeConnect(row){if(!row.form)return;row.form.secret.value='';row.form.secret.type='password';if(row.form.reveal){row.form.reveal.textContent='Show key';if(row.form.reveal.setAttribute)row.form.reveal.setAttribute('aria-pressed','false')}if(row.form.close)row.form.close();row.form.hidden=true}
function openConnect(row){const item=row.item,p=item.preset,unit=unitOf(item),source=p.kind==='download';if(row.form){row.form.hidden=false;if(row.form.showModal&&!row.form.open)row.form.showModal();if(row.form.secret.focus)row.form.secret.focus();return}
 const dialog=node('dialog'),form=node('form'),parts={};if(dialog.setAttribute){dialog.setAttribute('data-credential-editor','');dialog.setAttribute('aria-label',(source?'Save credential for ':'Connect to ')+rowName(item))}dialog.append(form);
 form.append(node('h2',null,(source?'Save credential for ':'Connect to ')+rowName(item)));
 function field(label,input,hint){const l=node('label',null,label),id='connect-field-'+(++connectSeq);input.id=id;if(l.setAttribute)l.setAttribute('for',id);if(input.setAttribute)input.setAttribute('aria-label',label);form.append(l,input);if(hint)form.append(node('small',null,hint));return input}
 if(!p.base){parts.base=field('API address (required)',node('input'),'An HTTPS base URL, for example https://api.example.com/v1.');parts.base.type='url';parts.base.required=true}
 parts.secret=field(source?'Access token (required)':'API key (required)',node('input'),'The key is saved securely and is never displayed in the saved list.');parts.secret.type='password';parts.secret.autocomplete='off';parts.secret.required=true;if(parts.secret.setAttribute)parts.secret.setAttribute('data-oa-secret','');
 const reveal=node('button',null,'Show key');reveal.type='button';reveal.onclick=()=>{const shown=parts.secret.type==='password';parts.secret.type=shown?'text':'password';reveal.textContent=shown?'Hide key':'Show key';if(reveal.setAttribute)reveal.setAttribute('aria-pressed',String(shown))};form.append(reveal);
 parts.name=field('Key name (optional)',node('input'),'Leave blank for a unique generated name, or use letters, numbers, dots, dashes and underscores. Existing keys are never replaced.');parts.name.autocomplete='off';parts.name.spellcheck=false;if(parts.name.setAttribute)parts.name.setAttribute('data-oa-other','');
 if(!source){parts.budget=field(unit==='tokens'?'Daily token limit (optional)':'Daily spending limit (optional)',budgetChooser(unit,0,'Daily limit'),unit==='tokens'?'No daily token limit is applied unless you choose one. Token usage is shown; monetary cost is not reported.':'No daily spending limit is applied unless you choose one. Reported spend is still shown on this page.')}
 parts.status=node('p','oa-status');parts.status.id='connect-dialog-status-'+(++connectSeq);if(parts.status.setAttribute){parts.status.setAttribute('role','status');parts.status.setAttribute('aria-live','polite')}const actions=node('div'),go=node('button','oa-primary',source?'Save credential':'Connect'),cancel=node('button',null,'Cancel');go.type='submit';cancel.type='button';cancel.onclick=()=>{closeConnect(row);oaSetStatus(row.parts.status.id,'ok','Nothing connected.')};go.onclick=e=>{if(e)e.preventDefault();if(form.reportValidity&&!form.reportValidity())return;return connect(row,parts)};form.onsubmit=e=>{e.preventDefault();return connect(row,parts)};actions.append(go,cancel);form.append(actions,parts.status);
 dialog.secret=parts.secret;dialog.reveal=reveal;dialog.parts=parts;dialog.hidden=false;dialog.oncancel=e=>{if(e)e.preventDefault();closeConnect(row)};row.form=dialog;row.append(dialog);if(dialog.showModal)dialog.showModal();if(parts.secret.focus)parts.secret.focus()}
const outcomeWords={conflict:'already has this name. Choose a different key name; the existing key was not changed',forbidden:'cannot be changed from this Panel',invalid:'was refused as invalid',unknown:'is no longer connected',no_secure_store:'cannot be saved: this runtime has no platform store',exhausted:'cannot be saved: the account holds its maximum number of credentials',unavailable:'could not be reached'};
function partWord(part){return part==='credential'?'Its key':'Its model server'}
async function connect(row,parts){const p=row.item.preset,secret=parts.secret.value;parts.secret.value='';if(!secret){oaSetStatus(parts.status.id,'error','Paste the key first.');return}const base=parts.base?parts.base.value.trim():'';const credentialName=parts.name?parts.name.value.trim():'';if(credentialName&&!/^[A-Za-z0-9_.-]{1,64}$/.test(credentialName)){oaSetStatus(parts.status.id,'error','Key name must be 1–64 letters, numbers, dots, dashes or underscores.');return}if(parts.base&&!base){oaSetStatus(parts.status.id,'error','Name the address first.');return}oaSetStatus(parts.status.id,'loading','Saving…');try{const r=await call('/connect',{edit:'connect',preset:p.id,base,secret,credential_name:credentialName,budget:parts.budget?Number(parts.budget.value):0});if(r.outcome!=='applied'){oaSetStatus(parts.status.id,'error',partWord(r.part)+' '+(outcomeWords[r.outcome]||'answered '+r.outcome)+'.');return}closeConnect(row);await readConnect();if(window.credentialsChanged)await window.credentialsChanged();oaSetStatus(row.parts.status.id,'ok',p.kind==='download'?'Credential saved for private downloads from api.github.com. Apps use its name; permission is checked separately.':'Saved the key and configured '+(p.base?p.label:r.name)+'. Each app still needs your permission to use it.')}catch(e){oaShowError(parts.status.id,e)}}
async function setBudget(row,budget){const name=row.item.host.Entry.Name,unit=unitOf(row.item);oaSetStatus(row.parts.status.id,'loading','Changing the budget…');try{const r=await call('/connect',{edit:'budget',name,budget});if(r.outcome!=='applied'){oaSetStatus(row.parts.status.id,'error',partWord(r.part)+' '+(outcomeWords[r.outcome]||'answered '+r.outcome)+'.');await readConnect();return}await readConnect();oaSetStatus(row.parts.status.id,'ok',rowName(row.item)+(budget>0?(unit==='tokens'?' may use up to ':' may spend up to ')+quantity(unit,budget)+' a day.':' has no '+dailyLimit(unit)+'.'))}catch(e){oaShowError(row.parts.status.id,e)}}
async function readGateway(){try{const g=await call('/inference/gateway');if(g.Outcome!=='page')throw Error('The address for apps could not be read.');if(g.Listening){el('connect-gateway').textContent='http://'+g.ListeningAddress+'/v1';el('connect-gateway').hidden=false;oaSetStatus('connect-gateway-status','ok','Give an app this address as its base URL and any text as its key.')}else{el('connect-gateway').textContent='';el('connect-gateway').hidden=true;oaSetStatus('connect-gateway-status','ok','The gateway is off; turn it on under Providers.')}}catch(e){el('connect-gateway').textContent='';el('connect-gateway').hidden=true;oaShowError('connect-gateway-status',e)}}
async function readConnect(){try{const [presets,hosts]=await Promise.all([call('/connect'),call('/inference/hosts')]);if(hosts.Outcome!=='page'||!Array.isArray(presets.presets))throw Error('Connected services could not be read.');connectPresets=presets.presets;connectHosts=hosts.Hosts||[];oaKeyedRows(el('connect-rows'),connectItems(),i=>i.key,createConnectRow,updateConnectRow);oaSetStatus('connect-status','idle','')}catch(e){for(const row of el('connect-rows').children)if(row.form)row.form.secret.value='';el('connect-rows').replaceChildren();connectPresets=[];connectHosts=[];oaShowError('connect-status',e);throw e}}
window.connectChanged=()=>Promise.all([readConnect().catch(()=>{}),readGateway()]);
// connectRedraw refreshes the rows' words from the last read, after the
// saved credentials changed.
window.connectRedraw=()=>{if(connectPresets.length)oaKeyedRows(el('connect-rows'),connectItems(),i=>i.key,createConnectRow,updateConnectRow)};
var connectReady=window.connectChanged();
window.connectReady=connectReady;
window.connectSpendLine=spendLine;
})();
</script>`
