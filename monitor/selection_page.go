package main

// selectionPolicySection is the Models page's choice group (SPEC §4,
// Models): the Use selector, one chooser row per
// registered app ("Chat model for <app>") and the fallback order behind
// an expander. Every chooser is filled on arrival with the stored choice.
//
// It reads and writes the same selection policies as before through
// /registry/selection (registry_panel.go): one policy per app and use,
// named oa-chat-default or oa-image-default, whose first candidate is the
// app's model and whose later candidates are its fallbacks. Choosing "App's
// own choice" deletes the app's policy. Engine selection and routing stay
// with the selection service and the router.
//
// The selection service keeps policies per program only, so the per-app
// rows carry the choice; there is no account-wide default per use to show
// (TODO.md). The Use selector is a select of the router's seven uses, chat
// and image first, fixed here until a uses endpoint exists (TODO.md).
var selectionPolicySection = `<section id="selection-section" class="oa-group"><h2>Chosen models</h2>` +
	oaRow("Use", "", `<select id="models-profile" aria-label="Use"><option value="chat" selected>Chat</option><option value="image">Images</option><option value="embed">Embeddings</option><option value="transcription">Transcription</option><option value="speech">Speech</option><option value="live">Live audio</option><option value="video">Video</option></select>`, "") +
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
let apps=[],reads={},epochs={},busy=0,fallbackProgram='',statusSeq=0,shownUse='';
function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n}
function option(value,label){const o=node('option',null,label);o.value=value;return o}
function empty(sentence){return node('p','oa-empty',sentence)}
function statusLine(){const p=node('p','oa-status');p.id='selection-line-'+(++statusSeq);if(p.setAttribute){p.setAttribute('role','status');p.setAttribute('aria-live','polite')}return p}
function focused(n){return document.activeElement===n}
async function selectionRequest(payload){const r=await fetch('/registry/selection',{method:'POST',headers:{'X-Panel-Key':key,'Content-Type':'application/json'},body:JSON.stringify(payload)});if(!r.ok)throw Error(await r.text());return r.json()}
// Registered applications (the Facade's directory), each once, by title.
async function loadApps(){const r=await fetch('/apps/registrations?'+new URLSearchParams({source:'applications',cursor:'',wait_ms:'0'}),{headers:{'X-Panel-Key':key}});if(!r.ok)throw Error(await r.text());const page=await r.json();if(page.outcome!=='page'&&page.outcome!=='stale')throw Error(page.error||page.outcome);const seen=new Set(),list=[];for(const entry of page.entries||[]){const program=entry.program||'';if(!program||seen.has(program))continue;seen.add(program);const d=entry.application&&entry.application.Descriptor||{};list.push({program,title:String(entry.title||d.Title||entry.name||oaShortName(program))})}return list}
function wire(c){return{model:c.Model,placement:c.Placement==='hosted'?'hosted':'local',credential:c.Credential||'',provider:c.Provider||''}}
function candidates(read){return read&&read.Outcome==='found'&&read.Policy?read.Policy.Candidates||[]:[]}
// offerFor finds the offered model a stored candidate names, by family or
// by one of its aliases.
function offerFor(offers,model){return offers.find(o=>o.Family===model||(o.Names||[]).includes(model))||null}
function sameModel(offers,a,b){if(a===b)return true;const offer=offerFor(offers,a);return !!offer&&offer===offerFor(offers,b)}
// A new candidate takes its placement from the servers offering it: local
// when any of them runs here, pinned to this machine's provider that offers
// it (as Get started's picture chooser pins), hosted with that server's key
// otherwise.
async function newCandidate(model,offers){const offer=offerFor(offers,model),servers=offer&&offer.Servers||[];const hosts={};for(const h of await modelsHosts())if(h&&h.Entry)hosts[h.Entry.Name]=h.Entry;if(!servers.length||servers.some(s=>!hosts[s]||!hosts[s].Hosted)){const providers=await modelsProviders();return{model,placement:'local',credential:'',provider:servers.find(s=>providers.includes(s))||''}}const hosted=servers.map(s=>hosts[s]).find(e=>e.Credential);if(!hosted)throw Error('needs-key');return{model,placement:'hosted',credential:hosted.Credential,provider:''}}
function readSentence(read){switch(read.Outcome){case'forbidden':return'The runtime did not permit this Panel to read this choice.';case'unavailable':return'The runtime could not read this choice right now.';default:return''}}
function changeSentence(change,done){switch(change.Outcome){case'applied':return done;case'conflict':return'The choice changed elsewhere; this shows the current one.';case'forbidden':return'The runtime did not permit this Panel to change this choice.';case'unavailable':return'The runtime could not save this choice right now.';default:return'The runtime refused this choice.'}}
async function reread(app,name){const result=await selectionRequest({edit:'read',target_program:app.program,name});reads[app.program]=result.read;return result.read}
// fillChooser sets a chooser's options and value from the stored policy,
// leaving a chooser the person is using alone unless force is set.
function fillChooser(select,read,offers,force){if(!force&&focused(select))return;const options=[option('',"App's own choice")];for(const o of offers)options.push(option(o.Family,oaModelName(o.Family)));const first=candidates(read)[0];let value='';if(first){const offer=offerFor(offers,first.Model);if(offer)value=offer.Family;else{value=first.Model;options.push(option(first.Model,oaModelName(first.Model)+' (not available now)'))}}select.replaceChildren(...options);select.value=value}
function createAppRow(app){const row=node('div','oa-row'),text=node('div','oa-row-text'),control=node('div','oa-row-control'),label=node('span','oa-row-label'),select=node('select'),status=statusLine();text.append(label);control.append(select);row.append(text,control,status);row.parts={label,select,status};select.onchange=()=>choose(row.program,row);return row}
function updateAppRow(row,app,read,offers,use){row.program=app.program;row.parts.label.textContent=rowWords[use]+' for '+app.title;if(row.parts.select.setAttribute)row.parts.select.setAttribute('aria-label',rowWords[use]+' for '+app.title);if(row.dataset.busy)return;fillChooser(row.parts.select,read,offers,false);const refused=read.error||read.Outcome!=='found'&&read.Outcome!=='unknown';row.parts.select.disabled=!!refused;if(read.error){row.readError=true;oaShowError(row.parts.status.id,read.error)}else if(refused){row.readError=true;oaSetStatus(row.parts.status.id,'error',readSentence(read))}else if(row.readError){row.readError=false;oaSetStatus(row.parts.status.id,'idle','')}}
// choose stores the app's model as its policy's first candidate and keeps
// every other candidate after it in order; only the candidate moved to
// first place leaves the fallbacks. The app's own choice deletes the policy
// with its fallback order.
function bump(program){epochs[program]=(epochs[program]||0)+1}
async function choose(program,row){const app=apps.find(a=>a.program===program);if(!app)return;const use=modelsUse(),name=policyNames[use],select=row.parts.select,statusID=row.parts.status.id,value=select.value,read=reads[program]||{};busy++;bump(program);row.dataset.busy='yes';oaSetStatus(statusID,'loading','Saving…');try{let change,done;if(!value){if(read.Outcome!=='found'){oaSetStatus(statusID,'ok',app.title+' already chooses its own '+modelsUseWord(use)+' model.');return}change=(await selectionRequest({edit:'delete',target_program:program,name,revision:read.Revision})).change;done=app.title+' now chooses its own '+modelsUseWord(use)+' model; its fallback order is removed.'}else{const offers=await modelsOffers(use),same=candidates(read).find(c=>sameModel(offers,c.Model,value));let first;try{first=same?wire(same):await newCandidate(value,offers)}catch(e){if(e.message!=='needs-key')throw e;fillChooser(select,read,offers,true);oaSetStatus(statusID,'error','This model needs a saved key before an app can use it.');return}const rest=candidates(read).slice(1).map(wire).filter(c=>wireKey(c)!==wireKey(first));change=(await selectionRequest({edit:'put',target_program:program,name,revision:read.Outcome==='found'?read.Revision||'':'',candidates:[first,...rest].slice(0,8)})).change;done=app.title+' now uses '+oaModelName(value)+' as its '+modelsUseWord(use)+' model.'}const fresh=await reread(app,name);fillChooser(select,fresh,await modelsOffers(use),true);oaSetStatus(statusID,change.Outcome==='applied'?'ok':'error',changeSentence(change,done));if(program===fallbackProgram)await renderFallback()}catch(e){oaShowError(statusID,e)}finally{bump(program);busy--;delete row.dataset.busy}}
// render runs one pass at a time; a request during a pass runs once after it.
let running=null,pending=null;
function render(live){pending={live:pending?pending.live&&live:live};if(!running)running=(async()=>{try{while(pending){const next=pending;pending=null;await pass(next.live)}}finally{running=null}})();return running}
async function pass(live){if(live&&busy)return;const use=modelsUse(),name=policyNames[use],box=el('selection-apps');if(use!==shownUse){box.replaceChildren();el('selection-fallback-rows').replaceChildren();shownUse=use}
 if(!name){el('selection-fallback').hidden=true;box.replaceChildren(empty('Apps choose their own '+modelsUseWord(use)+' model.'));oaSetStatus('selection-status','idle','');return}
 if(!live)oaSetStatus('selection-status','loading');
 let offers=[];try{[apps,offers]=await Promise.all([loadApps(),modelsOffers(use)])}catch(e){if(!live)box.replaceChildren();oaShowError('selection-status',e);return}
 el('selection-fallback').hidden=!apps.length;
 if(!apps.length){box.replaceChildren(empty('No apps are registered yet.'));oaSetStatus('selection-status','idle','');return}
 const before=apps.map(app=>epochs[app.program]||0);
 const results=await Promise.all(apps.map(app=>selectionRequest({edit:'read',target_program:app.program,name}).then(r=>r.read,error=>({error}))));
 if(modelsUse()!==use)return;
 // A read that started before a save for that app finished is older than
 // the saved state: it is dropped.
 apps.forEach((app,i)=>{if((epochs[app.program]||0)===before[i])reads[app.program]=results[i]});
 oaKeyedRows(box,apps,app=>app.program,createAppRow,(row,app,i)=>updateAppRow(row,app,reads[app.program]||results[i],offers,use));
 oaSetStatus('selection-status','idle','');await renderFallback()}
// The fallback order of one app: its candidates after the first, in order,
// one row each with its own status line.
function candidateKey(c){return [c.Placement,c.Model,c.Credential||'',c.Provider||''].join('|')}
function wireKey(c){return [c.placement,c.model,c.credential||'',c.provider||''].join('|')}
function createFallbackRow(){const row=node('div','oa-row'),text=node('div','oa-row-text'),control=node('div','oa-row-control'),label=node('span','oa-row-label'),caption=node('span','oa-row-caption'),up=node('button',null,'Move up'),remove=node('button',null,'Remove'),status=statusLine();up.type='button';remove.type='button';text.append(label,caption);control.append(up,remove);row.append(text,control,status);row.parts={label,caption,up,remove,status};up.onclick=()=>reorder(row.index,row.index-1,oaModelName(row.cand.Model)+' moved up.',row.parts.status.id);remove.onclick=()=>reorder(row.index,-1,oaModelName(row.cand.Model)+' is no longer a fallback.','selection-fallback-status');return row}
function updateFallbackRow(row,c,i){row.index=i+1;row.cand=c;row.parts.label.textContent=(i+1)+'. '+oaModelName(c.Model);row.parts.caption.textContent=c.Placement==='hosted'?'Hosted with the key '+(c.Credential||''):'On this device';row.parts.up.hidden=i===0}
async function renderFallback(){const use=modelsUse(),name=policyNames[use],pick=el('selection-fallback-app'),list=el('selection-fallback-rows'),add=el('selection-fallback-add'),model=el('selection-fallback-model');if(!name)return;if(!apps.some(a=>a.program===fallbackProgram))fallbackProgram=apps.length?apps[0].program:'';if(!focused(pick)){pick.replaceChildren(...apps.map(a=>option(a.program,a.title)));pick.value=fallbackProgram}const app=apps.find(a=>a.program===fallbackProgram);if(!app){list.replaceChildren(empty('No apps are registered yet.'));model.replaceChildren();add.disabled=true;return}
 const read=reads[app.program]||{},cands=candidates(read),offers=await modelsOffers(use);
 if(cands.length<2)list.replaceChildren(empty(app.title+' has no fallback models.'));else oaKeyedRows(list,cands.slice(1),candidateKey,createFallbackRow,updateFallbackRow);
 const left=offers.filter(o=>!cands.some(c=>offerFor([o],c.Model)));if(!focused(model)){const chosen=model.value;model.replaceChildren(...left.map(o=>option(o.Family,oaModelName(o.Family))));if(left.some(o=>o.Family===chosen))model.value=chosen}add.disabled=!cands.length||cands.length>=8||!left.length||read.Outcome!=='found'}
async function saveFallback(list,done,statusID){const app=apps.find(a=>a.program===fallbackProgram);if(!app)return;const name=policyNames[modelsUse()],read=reads[app.program]||{};busy++;bump(app.program);oaSetStatus(statusID,'loading','Saving…');try{const change=(await selectionRequest({edit:'put',target_program:app.program,name,revision:read.Revision||'',candidates:list})).change;await reread(app,name);await renderFallback();oaSetStatus(statusID,change.Outcome==='applied'?'ok':'error',changeSentence(change,done))}catch(e){oaShowError(statusID,e)}finally{bump(app.program);busy--}}
function reorder(from,to,done,statusID){const list=candidates(reads[fallbackProgram]).map(wire),[moved]=list.splice(from,1);if(to>=0)list.splice(to,0,moved);return saveFallback(list,done,statusID)}
el('selection-fallback-app').onchange=()=>{fallbackProgram=el('selection-fallback-app').value;el('selection-fallback-rows').replaceChildren();oaSetStatus('selection-fallback-status','idle','');renderFallback()};
el('selection-fallback-add').onclick=async()=>{const app=apps.find(a=>a.program===fallbackProgram),value=el('selection-fallback-model').value;if(!app||!value){oaSetStatus('selection-fallback-status','error','Choose a model for this app first.');return}const list=candidates(reads[app.program]).map(wire);try{list.push(await newCandidate(value,await modelsOffers(modelsUse())))}catch(e){if(e.message==='needs-key'){oaSetStatus('selection-fallback-status','error','This model needs a saved key before an app can use it.');return}oaShowError('selection-fallback-status',e);return}return saveFallback(list.slice(0,8),oaModelName(value)+' is now a fallback for '+app.title+'.','selection-fallback-status')};
oaModelsHooks.push(render);
oaModelsFirstReads.push(render(false));
Promise.all(oaModelsFirstReads).then(oaModelsMarkReady,oaModelsMarkReady);
})();
</script>`
