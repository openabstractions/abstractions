package main

// inferencePage is the Panel's one Models page (backlog/P-J1a.md, Models
// page, 2026-11-01; layout ruling 2026-11-10): "Models on this computer and
// the apps that use them." Four sections, in the order a person asks about
// models:
//
//   - Loaded now: the models an engine has loaded, with the engine and the
//     graphics memory the resource table (the Device page's source) reports
//     for it; one row says so when nothing is loaded.
//   - Default models: the established setup's row and the choice group
//     (selection_page.go: Use, the default for all apps, per-app choosers,
//     fallback order).
//   - Model files: every model file the storage inventory found, by use
//     (Chat, Pictures, Other uses, Not set up yet), with its size, engine
//     and state, and what the person can do with it (models_library.go,
//     libraryScript below).
//   - Engines: the model servers, the model folder and the local engine,
//     and a link to Providers, which adds servers and issues app keys.
//
// It calls /inference/library (the storage inventory joined with the
// router's families and the resource table's graphics memory), the router's
// public offers (/inference/models), the inference operator's server list
// (/inference/hosts), the provider declarations (/registry/view), the local
// model setup calls (/setup/modelhost/*, setup_modelhost_chat.go), the
// setup step (/setup/step) and the selection policies (/registry/selection),
// all with the panel key. A model shows by its friendly name (oaModelName in
// panel_shell.go): never a file path or a hash. Lists update in place while
// the page is visible; nothing waits for a Refresh. Its words follow
// research/vocabulary/DECISION.md: model server (D11), modality as "use"
// (D82); the JSON fields keep their wire names.
// The page says itself when its first reads are done (oaReady in its
// script), which the hidden data-ready-owner marker declares.
// Loaded now and Model files are read from the runtime: while the
// runtime-down banner shows they are hidden whole (runtimeGroup), so the page
// shows no heading over an emptied list (R19b render check, 2026-11-09).
var inferencePage = `<div data-ready-owner="page" hidden></div>` + oaPageHead("inference", "Models", "Models on this computer and the apps that use them.") +
	runtimeGroup(oaGroup("models-running", "Loaded now", "", runtimeEcho(oaStatus("models-running-status")), `<div id="models-running-rows"></div>`)) +
	selectionPolicySection +
	runtimeGroup(oaGroup("models-local", "Model files", "", libraryReadingStatus("models-local-status", "Reading model folders…"), `<div id="models-local-rows"></div>`,
		`<div id="models-local-tech" hidden>`+oaTechnicalDetails(`<pre id="models-local-guide"></pre>`)+`</div>`)) +
	runtimeGroup(oaGroup("add-model", "Engines", "", oaStatusLine("models-engine-status", false), `<div id="models-engine-rows"></div>`, oaStatus("add-model-status"), `<div id="add-model-rows"></div>`,
		`<div id="add-model-folder" hidden>`+oaRow("Folder", "", `<input id="add-model-path" type="text" data-oa-other aria-label="Folder" autocomplete="off" spellcheck="false"><button id="add-model-check" type="button">Check folder</button>`, "add-model-folder-status")+`</div>`,
		`<div id="add-model-tech" hidden>`+oaTechnicalDetails(`<pre id="add-model-reason"></pre>`)+`</div>`,
		`<a class="oa-row oa-row-nav" id="models-providers" href="/registry"><span class="oa-row-text"><span class="oa-row-label">Providers</span><span class="oa-row-caption">Add model servers and give apps keys.</span></span><span class="oa-row-chevron" aria-hidden="true"></span></a>`)) +
	inferenceScript + `<script>` + setupStepScript + `</script>` + libraryScript + selectionPolicyScript + programReviewPanel

// libraryReadingStatus is a status line that says what it waits for while
// its first read runs.
func libraryReadingStatus(id, sentence string) string {
	return `<p class="oa-status" id="` + id + `" role="status" aria-live="polite" data-state="loading" aria-busy="true">` + sentence + `</p>`
}

const inferenceScript = `<script>
const key=new URLSearchParams(location.search).get('k');
const el=id=>document.getElementById(id);
// oaModelsHooks are the page parts that redraw when the use changes or the
// page refreshes while visible (selection_page.go registers one).
var oaModelsHooks=[];
// oaModelsUseFor(use, family) makes family the account default for use;
// the choice group (selection_page.go) sets it, the model rows call it.
var oaModelsUseFor=null;
async function call(path,body){if(body&&body.candidates&&path.split('?')[0]==='/registry/selection')body=await oaPanelRequest(path,body);const r=await fetch(path,{method:body?'POST':'GET',headers:{'X-Panel-Key':key,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});if(!r.ok){const text=await r.text();let reply=null;try{reply=JSON.parse(text)}catch(_){}if(reply&&reply.busy){const e=Error('Another Catalog step is running.');e.busy=reply;throw e}throw Error(text)}return oaPanelResponse(path,await r.json())}
// modelsShowError shows a busy refusal as the page's another-step sentence.
function modelsShowError(id,e){if(e&&e.busy){oaSetStatus(id,'ok',anotherStep);return}oaShowError(id,e)}
function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n}
const modelUses={chat:'Chat',image:'Images',embed:'Embeddings',transcription:'Transcription',speech:'Speech',live:'Live audio',video:'Video'};
function modelsUse(){return el('models-profile').value||''}
function modelsUseLabel(use){return modelUses[use]||use}
// modelsUseWord names a use inside a sentence ("its own speech model").
const modelUseWords={chat:'chat',image:'picture',embed:'embedding',transcription:'transcription',speech:'speech',live:'live audio',video:'video'};
function modelsUseWord(use){return modelUseWords[use]||use}
let usesAvailable=false;
const readUses=serial(async()=>{const select=el('models-profile'),prior=select.value||new URLSearchParams(location.search).get('use');try{const snapshot=await call('/inference/modalities'),uses=snapshot.modalities;if(!Array.isArray(uses)||!uses.length)throw Error('OpenAbstractions has no model uses available.');select.replaceChildren(...uses.map(use=>{const option=node('option',null,modelsUseLabel(use));option.value=use;return option}));select.value=uses.includes(prior)?prior:uses.includes('chat')?'chat':uses[0];select.disabled=false;usesAvailable=true;el('models-profile-row').hidden=false;return true}catch(e){usesAvailable=false;el('models-profile-row').hidden=true;select.disabled=true;select.replaceChildren();select.value='';oaShowError('selection-status',e);return false}});
var modelsUsesReady=readUses();
// Each read below happens once per refresh.
let offerCache={},hostsRead=null,providersRead=null;
function modelsOffers(use){if(!offerCache[use])offerCache[use]=call('/inference/models?profile='+encodeURIComponent(use)).then(s=>use==='image'?(s.Offers||[]).filter(o=>['resident','would-load'].includes((s.Picks||{})[o.Family])):s.Offers||[]).catch(e=>{delete offerCache[use];throw e});return offerCache[use]}
// modelsHosts lists the model servers, which tell a local model from a
// hosted one; an unreadable list counts every server as local.
function modelsHosts(){if(!hostsRead)hostsRead=call('/inference/hosts').then(h=>h.Hosts||[]).catch(()=>{hostsRead=null;return []});return hostsRead}
// modelsProviders names this machine's provider declarations, the names a
// local candidate pins (as Get started's picture chooser does); an
// unreadable list pins nothing.
function modelsProviders(){if(!providersRead)providersRead=call('/registry/view').then(v=>((v.providers||{}).Declarations||[]).filter(d=>d.Role==='provider'&&d.Declaration).map(d=>d.Declaration.Name)).catch(()=>{providersRead=null;return []});return providersRead}
// serial runs one pass of fn at a time; a call during a pass runs once
// after it, and its promise settles after that pass.
function serial(fn){let running=null,again=false;return function(){if(running){again=true;return running}running=(async()=>{try{do{again=false;await fn()}while(again)}finally{running=null}})();return running}}
el('models-profile').onchange=()=>{for(const hook of oaModelsHooks)hook(false)};

// Engines: the local engine and the model folder it reads in place. The
// models in that folder are rows of On this computer (libraryScript), where
// Set up hosts one on demand and Remove from OA stops hosting it. A step
// that needs approval asks once more on the same button ("Confirm"), with
// the exact approval behind the status line's Technical details. One step
// runs at a time, on this section and on the model rows alike.
let local=null,localBusy=false,folderPreview=null,localSeq=0;
const anotherStep='Another step is running.';
function folderName(path){return oaShortName(path)||'Model folder'}
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
async function localStep(row,request,done,working){if(localBusy){oaSetStatus(row.parts.status.id,'ok',anotherStep);return null}localBusy=true;const rowKey=row.dataset.key;oaSetStatus(row.parts.status.id,'loading',working||'Working…');let reply=null;try{reply=await request();await readLocal();const kept=Array.from(el('add-model-rows').children||[]).find(r=>r.dataset&&r.dataset.key===rowKey);oaSetStatus(row.fixed?row.parts.status.id:kept?kept.parts.status.id:'add-model-status','ok',done(reply))}catch(e){modelsShowError(row.parts.status.id,e)}finally{localBusy=false}return reply}
function openFolderRow(row,sentence){el('add-model-folder').hidden=false;oaSetStatus(row.parts.status.id,'ok',sentence)}
async function chooseFolder(row){if(localBusy){oaSetStatus(row.parts.status.id,'ok',anotherStep);return}localBusy=true;oaSetStatus(row.parts.status.id,'loading','Choosing a folder…');try{const picked=await call('/inference/folder',{});if(!picked.available){openFolderRow(row,'Name the folder that holds your model files.');return}if(!picked.path){oaSetStatus(row.parts.status.id,'idle','');return}folderPreview=null;disarm(el('add-model-check'));el('add-model-path').value=picked.path;openFolderRow(row,'Check the selected folder before adding it.')}catch(e){modelsShowError(row.parts.status.id,e)}finally{localBusy=false}}
// The rows Engines shows for the local state: the engine while it is
// missing, then the model folder.
function localItems(state){const feature=state.feature||{},engine=state.engine||{};if(!feature.installed)return[{key:'feature'}];if(!engine.staged)return[{key:'engine'}];return[{key:'folder'}]}
function createLocalRow(item){const row=localRow();if(item.key==='unavailable');else if(item.key==='feature')row.parts.control.append(button('Add',()=>localStep(row,()=>call('/setup/modelhost/feature/add',{plan_id:(local||{}).plan_id||'',confirm:true}),r=>r.feature&&r.feature.installed?'llama.cpp is added.':'llama.cpp was not added.')));
 else if(item.key==='engine'){const bundled=()=>((local||{}).engine||{}).bundled||{};const prepare=button('Prepare',null);prepare.onclick=()=>localStep(row,()=>call('/setup/modelhost/engine/bundled',{plan_id:(local||{}).plan_id||'',approval_digest:bundled().approval_digest,confirm:true}),r=>r.staged?'llama.cpp is ready.':'llama.cpp was not prepared.','Preparing llama.cpp…');row.parts.prepare=prepare;const recovery=node('a',null,'Open Catalog');recovery.href='/setup?k='+encodeURIComponent(key)+'#setup-modelhost';row.parts.control.append(prepare,recovery)}
 else if(item.key==='folder')row.parts.control.append(button('Choose folder…',()=>chooseFolder(row)));
 return row}
function updateLocalRow(row,item){const state=local||{},root=state.root;if(item.key==='unavailable')setRowText(row,'Local models',state.unavailable==='platform'?'Not supported on this computer.':state.unavailable==='isolated'?'Not available in this test home.':'Cannot be read now.');else if(item.key==='feature')setRowText(row,'llama.cpp','Not added yet.');else if(item.key==='engine'){const bundled=(state.engine||{}).bundled||{};row.parts.prepare.hidden=!bundled.available;setRowText(row,'llama.cpp',bundled.available?'Ready to prepare.':'Prepare it in Catalog.')}else if(item.key==='folder'){const models=root&&root.models||[];setRowText(row,'Model folder',!root?'None chosen yet':(folderName(root.path)+' folder · ')+(models.length?models.length+(models.length===1?' model, read in place':' models, read in place'):'No supported models found'));if(root)row.parts.label.title=root.path}}
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
el('add-model-check').onclick=async()=>{const b=el('add-model-check'),path=el('add-model-path').value.trim(),state=local||{};if(localBusy){oaSetStatus('add-model-folder-status','ok',anotherStep);return}if(!path){oaSetStatus('add-model-folder-status','error','Name a folder first.');return}if(b.dataset.armed==='yes'&&folderPreview){disarm(b);const preview=folderPreview;folderPreview=null;const reply=await localStep(folderCheckRow,()=>call('/setup/modelhost/root',{plan_id:state.plan_id||'',path:preview.root.path,approval_digest:preview.approval_digest,confirm:true}),r=>r.registered?folderName(preview.root.path)+' is added; its models stay where they are.':'The folder was not added.');if(reply&&reply.registered){el('add-model-folder').hidden=true;el('add-model-path').value=''}return}oaSetStatus('add-model-folder-status','loading','Checking the folder without changing it…');try{folderPreview=await call('/setup/modelhost/root/preview',{path});b.dataset.armed='yes';b.textContent='Confirm';oaShowResult('add-model-folder-status','Confirm to read '+folderName(folderPreview.root.path)+' in place.',folderPreview.approval)}catch(e){folderPreview=null;disarm(b);modelsShowError('add-model-folder-status',e)}};
el('add-model-path').oninput=()=>{folderPreview=null;disarm(el('add-model-check'))};
var localReady=readLocal();
// The page is ready once its first reads finish: the uses, Engines, the
// model rows (libraryScript adds its read) and the choice group
// (selection_page.go adds its pass and calls oaModelsMarkReady).
var oaModelsFirstReads=[modelsUsesReady,localReady];
function oaModelsMarkReady(){oaReady()}

// While the page is visible its lists follow the runtime.
async function modelsLive(){if(document.visibilityState==='hidden')return;offerCache={};hostsRead=null;providersRead=null;await readUses();for(const hook of oaModelsHooks)hook(true);if(!localBusy)readLocal()}
if(typeof setInterval==='function')setInterval(modelsLive,20000);
if(typeof oaOnRuntimeBack==='function')oaOnRuntimeBack(modelsLive);
if(document.addEventListener)document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible')modelsLive()});
</script>`
