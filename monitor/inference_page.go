package main

// inferencePage is the Panel's Models page (research/panel-redesign/SPEC.md
// §4, Models): "Models your apps can use." It carries the choice group
// (selection_page.go: Use, per-app choosers, fallback order) and the models
// available for the chosen use with Add a model. The model calls are
// Activity's.
//
// It calls the router's public offers (/inference/models), the inference
// operator's server list (/inference/hosts), the provider declarations
// (/registry/view), the local model setup calls (/setup/modelhost/*,
// setup_modelhost_chat.go) and the selection policies (/registry/selection),
// all with the panel key. A model shows by its friendly name (oaModelName in
// panel_shell.go): never a file path or a hash. Lists update in place while
// the page is visible; nothing waits for a Refresh. Its words follow
// research/vocabulary/DECISION.md: model server (D11), modality as "use"
// (D82); the JSON fields keep their wire names.
// The page says itself when its first reads are done (oaReady in its
// script), which the hidden data-ready-owner marker declares.
var inferencePage = `<div data-ready-owner="page" hidden></div>` + oaPageHead("inference", "Models", "Models your apps can use.") +
	selectionPolicySection +
	oaGroup("models-section", "Available models", "", oaStatus("models-status"), `<div id="models"></div>`) +
	oaGroup("add-model", "Add a model", "", oaStatus("add-model-status"), `<div id="add-model-rows"></div>`,
		`<div id="add-model-folder" hidden>`+oaRow("Folder", "", `<input id="add-model-path" type="text" data-oa-other aria-label="Folder" autocomplete="off" spellcheck="false"><button id="add-model-check" type="button">Check folder</button>`, "add-model-folder-status")+`</div>`,
		`<div id="add-model-tech" hidden>`+oaTechnicalDetails(`<pre id="add-model-reason"></pre>`)+`</div>`) +
	inferenceScript + selectionPolicyScript

const inferenceScript = `<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);
// oaModelsHooks are the page parts that redraw when the use changes or the
// page refreshes while visible (selection_page.go registers one).
var oaModelsHooks=[];
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()}
function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n}
const modelUses={chat:'Chat',image:'Images',embed:'Embeddings',transcription:'Transcription',speech:'Speech',live:'Live audio',video:'Video'};
function modelsUse(){return el('models-profile').value||'chat'}
function modelsUseLabel(use){return modelUses[use]||use}
// modelsUseWord names a use inside a sentence ("its own speech model").
const modelUseWords={chat:'chat',image:'picture',embed:'embedding',transcription:'transcription',speech:'speech',live:'live audio',video:'video'};
function modelsUseWord(use){return modelUseWords[use]||use}
// Each read below happens once per refresh.
let offerCache={},hostsRead=null,providersRead=null;
function modelsOffers(use){if(!offerCache[use])offerCache[use]=call('/inference/models?profile='+encodeURIComponent(use)).then(s=>s.Offers||[]).catch(e=>{delete offerCache[use];throw e});return offerCache[use]}
// modelsHosts lists the model servers, which tell a local model from a
// hosted one; an unreadable list counts every server as local.
function modelsHosts(){if(!hostsRead)hostsRead=call('/inference/hosts').then(h=>h.Hosts||[]).catch(()=>{hostsRead=null;return []});return hostsRead}
// modelsProviders names this machine's provider declarations, the names a
// local candidate pins (as Get started's picture chooser does); an
// unreadable list pins nothing.
function modelsProviders(){if(!providersRead)providersRead=call('/registry/view').then(v=>((v.providers||{}).Declarations||[]).filter(d=>d.Role==='provider'&&d.Declaration).map(d=>d.Declaration.Name)).catch(()=>{providersRead=null;return []});return providersRead}
function modelContext(o){return o.Context>0&&o.ContextSource!=='none'&&o.ContextSource!=='unknown'?'Up to '+o.Context.toLocaleString()+' tokens':''}
// serial runs one pass of fn at a time; a call during a pass runs once
// after it, and its promise settles after that pass.
function serial(fn){let running=null,again=false;return function(){if(running){again=true;return running}running=(async()=>{try{do{again=false;await fn()}while(again)}finally{running=null}})();return running}}
function createModelRow(){const row=node('div','oa-row'),text=node('div','oa-row-text'),label=node('span','oa-row-label'),caption=node('span','oa-row-caption');text.append(label,caption);row.append(text);row.parts={label,caption};return row}
function updateModelRow(row,o){row.parts.label.textContent=oaModelName(o.Family);row.parts.caption.textContent=modelContext(o);row.parts.caption.hidden=!row.parts.caption.textContent}
const models=serial(async()=>{const use=modelsUse(),label=modelsUseLabel(use);try{const offers=await modelsOffers(use);if(use!==modelsUse())return;oaKeyedRows(el('models'),offers,o=>o.Family,createModelRow,updateModelRow);if(!offers.length)el('models').replaceChildren(node('p','oa-empty','No models are available for '+label+' yet.'));oaSetStatus('models-status','idle','')}catch(e){if(use!==modelsUse())return;el('models').replaceChildren();oaShowError('models-status',e.message)}});
el('models-profile').onchange=()=>{oaSetStatus('models-status','loading');models();for(const hook of oaModelsHooks)hook(false)};
var modelsReady=models();

// Add a model: the local engine, the model folder it reads in place, and
// each model in it hosted on demand. A step that needs approval asks once
// more on the same button ("Confirm"), with the exact approval behind the
// status line's Technical details. One step runs at a time.
let local=null,localBusy=false,folderPreview=null,localSeq=0;
const anotherStep='Another step is running.';
function folderName(path){return oaShortName(path)||'Model folder'}
function bytes(value){let amount=Number(value)||0;const units=['bytes','KB','MB','GB','TB'];let unit=0;while(amount>=1000&&unit<units.length-1){amount/=1000;unit++}return unit===0?Math.round(amount)+' bytes':amount.toFixed(amount>=10?1:2).replace(/\.?0+$/,'')+' '+units[unit]}
function localRow(){const row=node('div','oa-row'),text=node('div','oa-row-text'),control=node('div','oa-row-control'),label=node('span','oa-row-label'),caption=node('span','oa-row-caption'),status=node('p','oa-status');status.id='add-model-line-'+(++localSeq);if(status.setAttribute){status.setAttribute('role','status');status.setAttribute('aria-live','polite')}text.append(label,caption);row.append(text,control,status);row.parts={label,caption,control,status};return row}
function setRowText(row,label,caption){row.parts.label.textContent=label;row.parts.caption.textContent=caption||'';row.parts.caption.hidden=!caption}
function button(label,onclick){const b=node('button',null,label);b.type='button';b.dataset.label=label;b.onclick=onclick;return b}
function disarm(b){b.dataset.armed='';b.textContent=b.dataset.label}
// confirmed runs a step that needs approval: the first click shows the
// approval and turns the button into Confirm, the second one runs it; the
// button then reads its own label again, whether the step worked or not.
// The approval and the step read the row's current item at click time.
function confirmed(b,row,ask,approval,run){return async()=>{if(localBusy){oaSetStatus(row.parts.status.id,'ok',anotherStep);return}if(b.dataset.armed!=='yes'){b.dataset.armed='yes';b.textContent='Confirm';oaShowResult(row.parts.status.id,ask(),approval());return}try{return await run()}finally{disarm(b)}}}
// localStep runs one step; its sentence goes to the row's status line, or
// to the group's when the step finished the row (Add removes its row).
async function localStep(row,request,done){if(localBusy){oaSetStatus(row.parts.status.id,'ok',anotherStep);return null}localBusy=true;const rowKey=row.dataset.key;oaSetStatus(row.parts.status.id,'loading','Working…');let reply=null;try{reply=await request();await readLocal();const kept=Array.from(el('add-model-rows').children||[]).find(r=>r.dataset&&r.dataset.key===rowKey);oaSetStatus(row.fixed?row.parts.status.id:kept?kept.parts.status.id:'add-model-status','ok',done(reply))}catch(e){oaShowError(row.parts.status.id,e)}finally{localBusy=false}return reply}
function openFolderRow(row,sentence){el('add-model-folder').hidden=false;oaSetStatus(row.parts.status.id,'ok',sentence)}
// The rows Add a model shows for the local state: the engine while it is
// missing, then the model folder and each model in it.
function localItems(state){const feature=state.feature||{},engine=state.engine||{},root=state.root;if(!feature.installed)return[{key:'feature'}];if(!engine.staged)return[{key:'engine'}];if(!root)return[{key:'folder'}];return[{key:'folder'}].concat((root.models||[]).map(m=>({key:'model:'+m.id,model:m})))}
function modelLabel(m){return oaModelName(m.name||m.family)}
function createLocalRow(item){const row=localRow();if(item.key==='unavailable');else if(item.key==='feature')row.parts.control.append(button('Add',()=>localStep(row,()=>call('/setup/modelhost/feature/add',{plan_id:(local||{}).plan_id||'',confirm:true}),r=>r.feature&&r.feature.installed?'The local model engine is added.':'The local model engine was not added.')));
 else if(item.key==='engine'){const bundled=()=>((local||{}).engine||{}).bundled||{};const prepare=button('Prepare',null);prepare.onclick=confirmed(prepare,row,()=>'Confirm to prepare the included engine; it starts only when an app asks.',()=>bundled().approval,()=>localStep(row,()=>call('/setup/modelhost/engine/bundled',{plan_id:(local||{}).plan_id||'',approval_digest:bundled().approval_digest,confirm:true}),r=>r.staged?'The local model engine is ready.':'The engine was not prepared.'));row.parts.prepare=prepare;const recovery=node('a',null,'Get started');recovery.href='/setup?k='+encodeURIComponent(key)+'#setup-modelhost';row.parts.control.append(prepare,recovery)}
 else if(item.key==='folder')row.parts.control.append(button('Choose folder…',()=>openFolderRow(row,(local||{}).root?'Name another folder that holds your model files.':'Name the folder that holds your model files.')));
 else if(!item.model.hosted){const host=button('Host on demand',null);host.onclick=confirmed(host,row,()=>'Confirm to host '+modelLabel(row.model)+'; it starts only when an app asks.',()=>row.model.approval,()=>localStep(row,()=>call('/setup/modelhost/host',{plan_id:(local||{}).plan_id||'',store:((local||{}).root||{}).store,id:row.model.id,approval_digest:row.model.approval_digest,confirm:true}),r=>r.hosted?modelLabel(row.model)+' is hosted on demand.':'The model was not hosted.'));row.parts.control.append(host)}
 return row}
function updateLocalRow(row,item){const state=local||{},root=state.root;row.model=item.model;if(item.key==='unavailable')setRowText(row,'Local models',"Can't be added on this computer.");else if(item.key==='feature')setRowText(row,'Local model engine','Not added yet.');else if(item.key==='engine'){const bundled=(state.engine||{}).bundled||{};row.parts.prepare.hidden=!bundled.available;setRowText(row,'Local model engine',bundled.available?'Ready to prepare.':'Prepare it from Get started.')}else if(item.key==='folder'){const models=root&&root.models||[];setRowText(row,root?folderName(root.path):'Model folder',!root?'None chosen yet.':models.length?models.length+(models.length===1?' model, read in place.':' models, read in place.'):'No supported models found.')}else{const m=item.model;setRowText(row,modelLabel(m),[m.family,m.quant,m.size!=null?bytes(m.size):''].filter(Boolean).join(' · '));if(m.hosted&&!row.hosted){row.hosted=true;row.parts.control.replaceChildren(node('span',null,'Hosted on demand'))}}}
// A computer without local models shows one row saying so, its reason
// under the group's Technical details.
function renderLocal(){const box=el('add-model-rows'),state=local||{},reason=state.available?'':String(state.reason||'');el('add-model-reason').textContent=reason;el('add-model-tech').hidden=!reason;oaKeyedRows(box,state.available?localItems(state):[{key:'unavailable'}],item=>item.key,createLocalRow,updateLocalRow);oaSetStatus('add-model-status','idle','')}
// readLocal rereads the local state and updates the rows in place; a failed
// reread keeps the rows the page already shows.
const readLocal=serial(async()=>{try{local=await call('/setup/modelhost/chat')}catch(e){if(!local){el('add-model-rows').replaceChildren();el('add-model-tech').hidden=true;oaShowError('add-model-status',e)}return}renderLocal()});
// Check folder previews the folder read-only; Confirm registers it. Its
// row is in the markup, so its status line is fixed.
const folderCheckRow={fixed:true,dataset:{key:'folder-check'},parts:{status:{id:'add-model-folder-status'}}};
el('add-model-check').dataset.label='Check folder';
el('add-model-check').onclick=async()=>{const b=el('add-model-check'),path=el('add-model-path').value.trim(),state=local||{};if(localBusy){oaSetStatus('add-model-folder-status','ok',anotherStep);return}if(!path){oaSetStatus('add-model-folder-status','error','Name a folder first.');return}if(b.dataset.armed==='yes'&&folderPreview){disarm(b);const preview=folderPreview;folderPreview=null;const reply=await localStep(folderCheckRow,()=>call('/setup/modelhost/root',{plan_id:state.plan_id||'',path:preview.root.path,approval_digest:preview.approval_digest,confirm:true}),r=>r.registered?folderName(preview.root.path)+' is added; its models stay where they are.':'The folder was not added.');if(reply&&reply.registered){el('add-model-folder').hidden=true;el('add-model-path').value=''}return}oaSetStatus('add-model-folder-status','loading','Checking the folder without changing it…');try{folderPreview=await call('/setup/modelhost/root/preview',{path});b.dataset.armed='yes';b.textContent='Confirm';oaShowResult('add-model-folder-status','Confirm to read '+folderName(folderPreview.root.path)+' in place.',folderPreview.approval)}catch(e){folderPreview=null;disarm(b);oaShowError('add-model-folder-status',e)}};
el('add-model-path').oninput=()=>{folderPreview=null;disarm(el('add-model-check'))};
var localReady=readLocal();
// The page is ready once its first reads finish: the offers, Add a model
// and the choice group (selection_page.go adds its pass and calls
// oaModelsMarkReady).
var oaModelsFirstReads=[modelsReady,localReady];
function oaModelsMarkReady(){oaReady()}

// While the page is visible its lists follow the runtime.
function modelsLive(){if(document.visibilityState==='hidden')return;offerCache={};hostsRead=null;providersRead=null;models();for(const hook of oaModelsHooks)hook(true);if(!localBusy)readLocal()}
if(typeof setInterval==='function')setInterval(modelsLive,20000);
if(document.addEventListener)document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible')modelsLive()});
</script>`
