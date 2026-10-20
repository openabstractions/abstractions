package main

// selectionPolicySection is the Models page's choice group, Default models
// (SPEC §4, Models; layout ruling 2026-11-10 in backlog/P-J1a.md): the Use
// selector, the default for all apps, one chooser row per declared named policy
// (with prior conventions for apps without declarations) and fallback order behind
// an expander. Every chooser is filled on arrival with the stored choice.
//
// It reads and writes service-owned account defaults separately from raw
// account/program/name overrides. Declared names filter the router's live
// use catalogue; absent declarations preserve the chat/image conventions.
// Each named policy keeps its own revision, first choice and fallback order.
// Removing an override reveals the same-name account default, when present.
// Explicit model requests keep their own meaning. Routing stays service-owned.
var selectionPolicySection = `<section id="selection-section" class="oa-group"><h2>Default models</h2>` + setupStepRow +
	// Use shows once the runtime's uses are read (inference_page.go
	// readUses): no empty chooser (R19b render check, 2026-11-09).
	`<div id="models-profile-row" hidden>` + oaRow("Defaults for", "Each use has its own default model.", `<select id="models-profile" aria-label="Defaults for" disabled></select>`, "") + `</div>` +
	`<div id="selection-defaults"></div>` +
	`<div id="selection-apps"></div>` + oaStatus("selection-status") +
	`<div id="selection-fallback" hidden>` + oaExpander("Fallback order",
	oaRow("App", "", `<select id="selection-fallback-app" aria-label="App"></select>`, "")+
		`<div id="selection-fallback-rows"></div>`+
		oaRow("Add a fallback", "", `<select id="selection-fallback-model" aria-label="Model to add"></select><button id="selection-fallback-add" type="button">Add</button>`, "selection-fallback-status")) +
	`</div></section>`

// selectionPolicyScript runs after the Models page script (inference_page.go),
// whose modelsUse, modelsUseLabel, modelsUseWord, modelsOffers, modelsHosts,
// modelsProviders and oaKeyedRows (panel_shell.go) it reads, and registers its render in
// oaModelsHooks. A reread updates each row in place, so a row's status
// sentence and its focused chooser stay; one pass runs at a time, and a pass
// that read an app's choice while a save for that app ran keeps the saved
// state (epochs). Its names stay inside one function.
const selectionPolicyScript = `<script>
(function(){
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);
// The generic policy name each use's apps read (an adopter's chat
// integration reads oa-chat-default); other uses have none yet.
const policyNames={chat:'oa-chat-default',image:'oa-image-default'};
const rowWords={chat:'Chat model',image:'Picture model'};
function policyWord(name){return name===policyNames.chat?'chat':name===policyNames.image?'pictures':name}
// A hosted model no saved key reaches; every chooser says it in one place.
const needsKey='This model needs a saved key before an app can use it. Add its key under Credentials.';
let apps=[],reads={},epochs={},busy=0,fallbackProgram='',statusSeq=0,shownUse='';
let accountReads={},defaultBusy=false,defaultEpoch=0;
// setupReads holds, for a policy name with no account default of its own,
// the first candidate apps follow anyway: the selection owner's effective
// read, which falls back to the profile default Setup writes (INF-R1).
let setupReads={};
function setupFirst(name){return (accountReads[name]||{}).outcome==='unknown'?setupReads[name]||null:null}
// A preparation link suggests an offered model. It conveys no saved policy
// or authority; each application's existing chooser confirms its own write.
const preparedModel=new URLSearchParams(location.search).get('model')||'',preparedUse=new URLSearchParams(location.search).get('use')||'';
function preparedChoice(row,app,read,offers,use){if(!row.parts.confirm){const confirm=node('button',null,'Use prepared model');confirm.type='button';confirm.onclick=()=>choose(row.key,row);row.children[1].append(confirm);row.parts.confirm=confirm}const offered=offerFor(offers,preparedModel),def=accountReads[app.name]||{},suggest=use===preparedUse&&!!offered&&read.outcome==='unknown'&&def.outcome==='unknown'&&!app.blocked&&!row.dataset.busy;row.parts.confirm.hidden=!suggest;if(suggest&&!focused(row.parts.select)){row.parts.select.value=offered.Family;row.parts.caption.textContent='Prepared model; confirm to use it for this app.'}}
function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n}
function compare(a,b){return a<b?-1:a>b?1:0}
function option(value,label){const o=node('option',null,label);o.value=value;return o}
function empty(sentence){return node('p','oa-empty',sentence)}
function statusLine(){const p=node('p','oa-status');p.id='selection-line-'+(++statusSeq);if(p.setAttribute){p.setAttribute('role','status');p.setAttribute('aria-live','polite')}return p}
function focused(n){return document.activeElement===n}
async function selectionRequest(payload){if(payload.candidates)payload=await oaPanelRequest('/registry/selection',payload);const r=await fetch('/registry/selection',{method:'POST',headers:{'X-Panel-Key':key,'Content-Type':'application/json'},body:JSON.stringify(payload)});if(!r.ok)throw Error(await r.text());return oaPanelResponse('/registry/selection',await r.json())}
// Registered applications (the Facade's directory), each once, by title.
async function loadApps(use){const r=await fetch('/apps/registrations?'+new URLSearchParams({source:'applications',selection:'true',cursor:'',wait_ms:'0'}),{headers:{'X-Panel-Key':key}});if(!r.ok)throw Error(await r.text());const page=await r.json();if(page.outcome!=='page')throw Error(page.error||page.outcome);const groups=new Map();for(const entry of page.entries||[]){const program=entry.program||'';if(!program)continue;const d=entry.application&&entry.application.Descriptor||{},title=String(entry.title||d.Title||entry.name||oaShortName(program));if(!groups.has(program))groups.set(program,[]);groups.get(program).push({entry,title})}
 const list=[];for(const [program,entries] of groups){entries.sort((a,b)=>compare(String(a.entry.name||''),String(b.entry.name||''))||compare(a.title,b.title));const title=entries[0].title,declared=entries.filter(v=>v.entry.choosers_status==='declared'),unavailable=entries.some(v=>v.entry.choosers_status==='unavailable');if(unavailable){list.push({program,title,use,name:policyNames[use]||'',key:JSON.stringify([program,'unavailable']),blocked:'OpenAbstractions could not read this app’s declared choices.','declared':false});continue}if(declared.length){const names=new Map();for(const v of declared)for(const p of v.entry.choosers&&v.entry.choosers.Entries||[]){if(!names.has(p.Name))names.set(p.Name,new Set());names.get(p.Name).add(p.Modality)}for(const [name,profiles] of [...names].sort((a,b)=>compare(a[0],b[0])))if(profiles.has(use))list.push({program,title,use,name,key:JSON.stringify([program,name]),declared:true,blocked:profiles.size>1?'This policy name declares conflicting uses.':''})}else if(policyNames[use]){const name=policyNames[use];list.push({program,title,use,name,key:JSON.stringify([program,name]),declared:false,blocked:''})}}
 list.registered=groups.size;return list}

function wire(c){return{model:c.model,placement:c.placement==='hosted'?'hosted':'local',credential:c.credential||'',provider:c.provider||''}}
function candidates(read){return read&&read.outcome==='found'&&read.policy?read.policy.candidates||[]:[]}
// offerFor finds the offered model a stored candidate names, by family or
// by one of its aliases.
function offerFor(offers,model){return offers.find(o=>o.Family===model||(o.Names||[]).includes(model))||null}
function sameModel(offers,a,b){if(a===b)return true;const offer=offerFor(offers,a);return !!offer&&offer===offerFor(offers,b)}
// A new candidate takes its placement from the servers offering it: local
// when any of them runs here, pinned to this machine's provider that offers
// it (as Get started's picture chooser pins), hosted with that server's key
// otherwise.
async function newCandidate(model,offers){const offer=offerFor(offers,model),servers=offer&&offer.Servers||[];const hosts={};for(const h of await modelsHosts())if(h&&h.Entry)hosts[h.Entry.Name]=h.Entry;if(!servers.length||servers.some(s=>!hosts[s]||!hosts[s].Hosted)){const providers=await modelsProviders();return{model,placement:'local',credential:'',provider:servers.find(s=>providers.includes(s))||''}}const hosted=servers.map(s=>hosts[s]).find(e=>e.Credential);if(!hosted)throw Error('needs-key');return{model,placement:'hosted',credential:hosted.Credential,provider:''}}
function readSentence(read){switch(read.outcome){case'forbidden':return'OpenAbstractions did not permit this Panel to read this choice.';case'unavailable':return'OpenAbstractions could not read this choice right now.';default:return''}}
function changeSentence(change,done){switch(change.outcome){case'applied':return done;case'conflict':return'The choice changed elsewhere; this shows the current one.';case'forbidden':return'OpenAbstractions did not permit this Panel to change this choice.';case'unavailable':return'OpenAbstractions could not save this choice right now.';default:return'OpenAbstractions refused this choice.'}}
async function reread(app,name){const result=await selectionRequest({edit:'read',target_program:app.program,name});reads[app.key]=result.read;return result.read}
// fillChooser sets a chooser's options and value from the stored policy,
// leaving a chooser the person is using alone unless force is set.
function fillChooser(select,read,offers,force,emptyLabel){if(!force&&focused(select))return;const options=[option('',emptyLabel||"App's own choice")];for(const o of offers)options.push(option(o.Family,o.Title||oaModelName(o.Family)));const first=candidates(read)[0];let value='';if(first){const offer=offerFor(offers,first.model);if(offer)value=offer.Family;else{value=first.model;options.push(option(first.model,oaModelName(first.model)+' (not available now)'))}}select.replaceChildren(...options);select.value=value}
function createAppRow(app){const row=node('div','oa-row'),text=node('div','oa-row-text'),control=node('div','oa-row-control'),label=node('span','oa-row-label'),select=node('select'),status=statusLine();text.append(label);control.append(select);row.append(text,control,status);row.parts={label,select,status};select.onchange=()=>choose(row.key,row);return row}
function updateAppRow(row,app,read,offers,use){row.program=app.program;row.key=app.key;const def=accountReads[app.name]||{},inherited=read.outcome==='unknown',first=candidates(def)[0],label=(rowWords[use]||modelsUseLabel(use)+' model')+' for '+app.title+(app.declared?' — '+policyWord(app.name):'');row.parts.label.textContent=label;if(!row.parts.caption){row.parts.caption=node('span','oa-row-caption');row.children[0].append(row.parts.caption)}const setup=setupFirst(app.name);row.parts.caption.textContent=read.outcome==='found'?'App override':inherited&&def.outcome==='found'?'Follows the account default for '+policyWord(app.name)+'.':inherited&&setup?'Follows the '+modelsUseWord(use)+' model Setup chose.':inherited&&def.outcome==='unknown'?'No saved choice; this app chooses its own model.':'';if(row.parts.select.setAttribute)row.parts.select.setAttribute('aria-label',label);if(app.blocked){row.readError=true;row.parts.select.replaceChildren();row.parts.select.disabled=true;oaSetStatus(row.parts.status.id,'error',app.blocked);return}if(row.dataset.busy)return;const unknownDefault=inherited&&(def.error||def.outcome!=='found'&&def.outcome!=='unknown'),emptyLabel=first?'Use account default — '+oaModelName(first.model):unknownDefault?'Account default unavailable':setup?'Use Setup’s model — '+oaModelName(setup.model):"App's own choice";fillChooser(row.parts.select,read,offers,!!row.forceFill,emptyLabel);row.forceFill=false;const refused=read.error||read.outcome!=='found'&&read.outcome!=='unknown'||unknownDefault;row.parts.select.disabled=!!refused;if(read.error||unknownDefault){row.readError=true;oaShowError(row.parts.status.id,read.error||def.error||'The account default is unavailable.')}else if(refused){row.readError=true;oaSetStatus(row.parts.status.id,'error',readSentence(read))}else if(row.readError){row.readError=false;oaSetStatus(row.parts.status.id,'idle','')}}
// choose stores the app's model as its policy's first candidate and keeps
// every other candidate after it in order; only the candidate moved to
// first place leaves the fallbacks. The app's own choice deletes the policy
// with its fallback order.
function bump(program){epochs[program]=(epochs[program]||0)+1}
async function choose(id,row){const app=apps.find(a=>a.key===id);if(!app||app.blocked||app.use!==modelsUse())return;const program=app.program,use=app.use,name=app.name,select=row.parts.select,statusID=row.parts.status.id,value=select.value,read=reads[id]||{};busy++;bump(id);row.dataset.busy='yes';oaSetStatus(statusID,'loading','Saving…');try{let change,done;if(!value){if(read.outcome!=='found'){oaSetStatus(statusID,'ok',app.title+' already follows its saved default or its own choice.');return}change=(await selectionRequest({edit:'delete',target_program:program,name,revision:read.revision})).change;done=(accountReads[name]||{}).outcome==='found'?app.title+' now follows the account default for '+policyWord(name)+'.':(accountReads[name]||{}).outcome==='unknown'?app.title+' now chooses its own '+modelsUseWord(use)+' model; its fallback order is removed.':'The app override is removed; its account default is unavailable.'}else{const offers=await modelsOffers(use);if(modelsUse()!==use)return;const same=candidates(read).find(c=>sameModel(offers,c.model,value));let first;try{first=same?wire(same):await newCandidate(value,offers)}catch(e){if(e.message!=='needs-key')throw e;fillChooser(select,read,offers,true);oaSetStatus(statusID,'error',needsKey);return}if(modelsUse()!==use)return;const rest=candidates(read).slice(1).map(wire).filter(c=>wireKey(c)!==wireKey(first));change=(await selectionRequest({edit:'put',target_program:program,name,revision:read.outcome==='found'?read.revision||'':'',candidates:[first,...rest].slice(0,8)})).change;done=app.title+' now uses '+oaModelName(value)+' as its '+modelsUseWord(use)+' model.'}const fresh=await reread(app,name);fillChooser(select,fresh,await modelsOffers(use),true);oaSetStatus(statusID,change.outcome==='applied'?'ok':'error',changeSentence(change,done));if(id===fallbackProgram)await renderFallback()}catch(e){row.forceFill=true;oaShowError(statusID,e)}finally{bump(id);busy--;delete row.dataset.busy;render(false)}}
// render runs one pass at a time; a request during a pass runs once after it.
function defaultPayload(name,edit,extra={}){return{account_default:true,edit,name,...extra}}
function createDefaultRow(){const row=createAppRow();row.parts.select.onchange=()=>chooseDefault(row);return row}
async function showDefaults(use,entries,offers){const names=[...new Set([...(policyNames[use]?[policyNames[use]]:[]),...entries.filter(a=>!a.blocked).map(a=>a.name)])].sort(compare),epoch=defaultEpoch;const results=await Promise.all(names.map(name=>selectionRequest(defaultPayload(name,'read')).then(r=>r.read,error=>({error})))),effective=await Promise.all(names.map((name,i)=>results[i].outcome==='unknown'?selectionRequest({edit:'effective',name}).then(r=>r.read&&r.read.outcome==='found'&&r.read.policy&&(r.read.policy.candidates||[])[0]||null,()=>null):null));if(modelsUse()!==use||epoch!==defaultEpoch)return false;accountReads=Object.fromEntries(names.map((name,i)=>[name,results[i]]));setupReads=Object.fromEntries(names.map((name,i)=>[name,effective[i]]));oaKeyedRows(el('selection-defaults'),names,name=>name,createDefaultRow,(row,name)=>{row.name=name;row.use=use;const read=accountReads[name]||{},label='All apps'+(name===policyNames[use]?'':' — '+policyWord(name));row.parts.label.textContent=label;if(!row.parts.caption){row.parts.caption=node('span','oa-row-caption');row.children[0].append(row.parts.caption)}const setup=setupFirst(name);row.parts.caption.textContent=setup?'Setup chose this model. Choose another to change it.':'Apps use this unless they choose their own.';row.parts.select.setAttribute('aria-label',label);if(defaultBusy)return;fillChooser(row.parts.select,read,offers,!!row.forceFill,setup?'Setup’s model — '+oaModelName(setup.model):'No default');row.forceFill=false;const refused=read.error||read.outcome!=='found'&&read.outcome!=='unknown';row.parts.select.disabled=!!refused;if(refused){row.readError=true;oaShowError(row.parts.status.id,read.error||readSentence(read)||'The account default is unavailable.')}else if(row.readError){row.readError=false;oaSetStatus(row.parts.status.id,'idle','')}});return true}
// defaultChange writes value as the account default name's first
// candidate at read's revision, keeping the fallbacks after it; current
// says whether the write still applies after each wait.
async function defaultChange(name,use,value,read,current){const offers=await modelsOffers(use);if(!current())return null;const same=candidates(read).find(c=>sameModel(offers,c.model,value)),first=same?wire(same):await newCandidate(value,offers),rest=candidates(read).slice(1).map(wire).filter(c=>wireKey(c)!==wireKey(first));if(!current())return null;return (await selectionRequest(defaultPayload(name,'put',{revision:read.outcome==='found'?read.revision||'':'',candidates:[first,...rest].slice(0,8)}))).change}
// The model rows' Use for chat and Use for pictures (libraryScript): the
// same account default write as the chooser, for the use's policy name.
oaModelsUseFor=async function(use,family){const name=policyNames[use];if(!name)return{change:{outcome:'invalid'}};if(defaultBusy)return{busy:true};defaultBusy=true;defaultEpoch++;try{const read=(await selectionRequest(defaultPayload(name,'read'))).read;if(read.outcome!=='found'&&read.outcome!=='unknown')return{change:{outcome:read.outcome}};const change=await defaultChange(name,use,family,read,()=>true);accountReads[name]=(await selectionRequest(defaultPayload(name,'read'))).read;return{change}}finally{defaultEpoch++;defaultBusy=false;render(false)}};
async function chooseDefault(row){const name=row.name,use=row.use,read=accountReads[name]||{},value=row.parts.select.value,statusID=row.parts.status.id;if(defaultBusy||use!==modelsUse()||read.error||read.outcome!=='found'&&read.outcome!=='unknown'){row.forceFill=true;if(defaultBusy)oaSetStatus(statusID,'idle','Another choice is still saving; nothing was changed.');render(false);return}defaultBusy=true;defaultEpoch++;row.forceFill=true;oaSetStatus(statusID,'loading','Saving…');try{let change,done;if(!value){if(read.outcome!=='found'){oaSetStatus(statusID,'ok','No account default is set for '+policyWord(name)+'.');return}change=(await selectionRequest(defaultPayload(name,'delete',{revision:read.revision}))).change;done='The account default for '+policyWord(name)+' is removed.'}else{change=await defaultChange(name,use,value,read,()=>use===modelsUse());if(!change)return;done='Account default saved for '+policyWord(name)+'.'}accountReads[name]=(await selectionRequest(defaultPayload(name,'read'))).read;oaSetStatus(statusID,change.outcome==='applied'?'ok':'error',changeSentence(change,done))}catch(e){if(e.message==='needs-key')oaSetStatus(statusID,'error',needsKey);else oaShowError(statusID,e)}finally{defaultEpoch++;defaultBusy=false;render(false)}}
let running=null,pending=null;
function render(live){pending={live:pending?pending.live&&live:live};if(!running)running=(async()=>{try{while(pending){const next=pending;pending=null;await pass(next.live)}}finally{running=null}})();return running}
async function pass(live){if(live&&(busy||defaultBusy))return;const use=modelsUse(),box=el('selection-apps');if(!usesAvailable){apps=[];reads={};accountReads={};box.replaceChildren();el('selection-defaults').replaceChildren();el('selection-fallback').hidden=true;oaRuntimeEcho('selection-status',()=>oaSetStatus('selection-status','error','Model uses are unavailable right now.'));return}if(use!==shownUse){reads={};accountReads={};box.replaceChildren();el('selection-defaults').replaceChildren();el('selection-fallback-rows').replaceChildren();shownUse=use}
 if(!live)oaSetStatus('selection-status','loading');
 let offers=[];try{[apps,offers]=await Promise.all([loadApps(use),modelsOffers(use)])}catch(e){apps=[];reads={};accountReads={};box.replaceChildren();el('selection-defaults').replaceChildren();el('selection-fallback').hidden=true;el('selection-fallback-rows').replaceChildren();oaShowError('selection-status',e);return}
 if(!await showDefaults(use,apps,offers))return;
 el('selection-fallback').hidden=!apps.length;
 if(!apps.length){box.replaceChildren();oaSetStatus('selection-status','idle','');return}
 const before=apps.map(app=>epochs[app.key]||0);
 const results=await Promise.all(apps.map(app=>app.blocked?Promise.resolve({error:app.blocked}):selectionRequest({edit:'read',target_program:app.program,name:app.name}).then(r=>r.read,error=>({error}))));
 if(modelsUse()!==use)return;
 // A read that started before a save for that app finished is older than
 // the saved state: it is dropped.
 apps.forEach((app,i)=>{if((epochs[app.key]||0)===before[i])reads[app.key]=results[i]});
 oaKeyedRows(box,apps,app=>app.key,createAppRow,(row,app,i)=>{const read=reads[app.key]||results[i];updateAppRow(row,app,read,offers,use);preparedChoice(row,app,read,offers,use)});
 oaSetStatus('selection-status','idle','');await renderFallback()}
// The fallback order of one app: its candidates after the first, in order,
// one row each with its own status line.
function candidateKey(c){return [c.placement,c.model,c.credential||'',c.provider||''].join('|')}
function wireKey(c){return [c.placement,c.model,c.credential||'',c.provider||''].join('|')}
function createFallbackRow(){const row=node('div','oa-row'),text=node('div','oa-row-text'),control=node('div','oa-row-control'),label=node('span','oa-row-label'),caption=node('span','oa-row-caption'),up=node('button',null,'Move up'),remove=node('button',null,'Remove'),status=statusLine();up.type='button';remove.type='button';text.append(label,caption);control.append(up,remove);row.append(text,control,status);row.parts={label,caption,up,remove,status};up.onclick=()=>reorder(row.index,row.index-1,oaModelName(row.cand.model)+' moved up.',row.parts.status.id);remove.onclick=()=>reorder(row.index,-1,oaModelName(row.cand.model)+' is no longer a fallback.','selection-fallback-status');return row}
function updateFallbackRow(row,c,i){row.index=i+1;row.cand=c;row.parts.label.textContent=(i+1)+'. '+oaModelName(c.model);row.parts.caption.textContent=c.placement==='hosted'?'Hosted with the key '+(c.credential||''):'On this device';row.parts.up.hidden=i===0}
async function renderFallback(){const use=modelsUse(),pick=el('selection-fallback-app'),list=el('selection-fallback-rows'),add=el('selection-fallback-add'),model=el('selection-fallback-model');if(!apps.some(a=>a.key===fallbackProgram))fallbackProgram=apps.length?apps[0].key:'';if(!focused(pick)){pick.replaceChildren(...apps.map(a=>option(a.key,a.title+(a.declared?' — '+policyWord(a.name):''))));pick.value=fallbackProgram}const app=apps.find(a=>a.key===fallbackProgram);add.disabled=false;if(!app||app.blocked){add.reason=app?app.blocked:'No apps are registered yet.';list.replaceChildren(empty(add.reason));model.replaceChildren();return}
 const read=reads[app.key]||{},cands=candidates(read),offers=await modelsOffers(use);if(app.use!==modelsUse()||app.key!==fallbackProgram)return;
 if(read.outcome==='unknown'&&(accountReads[app.name]||{}).outcome==='found'){list.replaceChildren(empty('This app follows the account default for '+policyWord(app.name)+'.'));model.replaceChildren();add.reason=app.title+' follows the account default. Choose its own '+modelsUseWord(use)+' model above first.';return}if(cands.length<2)list.replaceChildren(empty(app.title+' has no fallback models.'));else oaKeyedRows(list,cands.slice(1),candidateKey,createFallbackRow,updateFallbackRow);
 const left=offers.filter(o=>!cands.some(c=>offerFor([o],c.model)));if(!focused(model)){const chosen=model.value;model.replaceChildren(...left.map(o=>option(o.Family,o.Title||oaModelName(o.Family))));if(left.some(o=>o.Family===chosen))model.value=chosen}add.reason=!cands.length||read.outcome!=='found'?app.title+' has no '+modelsUseWord(use)+' model of its own yet. Choose one above first.':cands.length>=8?app.title+' already has eight models in its order.':!left.length?'Every model on offer is already in '+app.title+'’s order.':''}
async function saveFallback(app,read,list,done,statusID){if(!app||app.key!==fallbackProgram||app.blocked||app.use!==modelsUse()||!apps.some(a=>a.key===app.key&&!a.blocked&&a.use===app.use)){oaSetStatus(statusID,'error','The app list changed; nothing was saved. Choose the app again.');return}const name=app.name;busy++;bump(app.key);oaSetStatus(statusID,'loading','Saving…');try{const change=(await selectionRequest({edit:'put',target_program:app.program,name,revision:read.revision||'',candidates:list})).change;await reread(app,name);await renderFallback();oaSetStatus(statusID,change.outcome==='applied'?'ok':'error',changeSentence(change,done))}catch(e){oaShowError(statusID,e)}finally{bump(app.key);busy--}}
function reorder(from,to,done,statusID){const app=apps.find(a=>a.key===fallbackProgram),read=reads[fallbackProgram]||{},list=candidates(read).map(wire),[moved]=list.splice(from,1);if(to>=0)list.splice(to,0,moved);return saveFallback(app,read,list,done,statusID)}
el('selection-fallback-app').onchange=()=>{fallbackProgram=el('selection-fallback-app').value;el('selection-fallback-rows').replaceChildren();oaSetStatus('selection-fallback-status','idle','');renderFallback()};
el('selection-fallback-add').onclick=async()=>{const app=apps.find(a=>a.key===fallbackProgram),value=el('selection-fallback-model').value,reason=el('selection-fallback-add').reason;if(reason){oaSetStatus('selection-fallback-status','error',reason);return}if(!app||app.blocked||app.use!==modelsUse()||!value){oaSetStatus('selection-fallback-status','error','Choose a model for this app first.');return}const read=reads[app.key]||{},list=candidates(read).map(wire);try{list.push(await newCandidate(value,await modelsOffers(app.use)));if(app.use!==modelsUse())return}catch(e){if(e.message==='needs-key'){oaSetStatus('selection-fallback-status','error',needsKey);return}oaShowError('selection-fallback-status',e);return}return saveFallback(app,read,list.slice(0,8),oaModelName(value)+' is now a fallback for '+app.title+'.','selection-fallback-status')};
oaModelsHooks.push(render);
oaModelsFirstReads.push(modelsUsesReady.then(()=>render(false)));
Promise.all(oaModelsFirstReads).then(oaModelsMarkReady,oaModelsMarkReady);
})();
</script>`
