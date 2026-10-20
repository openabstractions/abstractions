package main

// Setup is one page of ticks (architect ruling 2026-11-09, owner-approved,
// backlog/hp4-hp5-adopter-updates.md): Apps, Models (chat and coding,
// pictures), Engines (a heading like Apps and Models, owner 2026-11-09, with
// the choice OA made as its caption), the OpenAbstractions row and a sticky
// summary row that holds the page's only button. A tick arrives showing what
// is installed; clearing a tick removes; the button applies every change.
var setupPage = oaPageHead("setup", "Catalog", "Install apps and models built for OpenAbstractions.") + pictureWorkspaceScript() + setupPageBody

// setupPageBody is the page below its head. The script builds every row from
// the catalogue (/setup/catalog), the overview (/setup/overview), the saved
// state (/setup/state) and the model reads; #setup-choices and
// #setup-modelhost are the anchors other pages link to.
const setupPageBody = `<section id="setup-choices" class="oa-group"><h2>Apps</h2><div id="setup-app-rows"></div></section>
<section id="setup-models" class="oa-group"><h2>Models</h2><h3 id="setup-chat-head">Chat and coding</h3><div id="setup-chat-rows"></div><h3 id="setup-picture-head" hidden>Pictures</h3><div id="setup-picture-rows"></div></section>
<section id="setup-engines" class="oa-group"><h2>Engines</h2><p id="setup-engine-choice" class="oa-caption">Chosen for this computer: llama.cpp for chat.</p><div id="setup-engine-rows"></div></section>
<section id="setup-oa" class="oa-group"><div id="setup-oa-rows"></div></section>
<div id="setup-summary-row" class="oa-row oa-footer"><div class="oa-row-text"><span id="setup-status" class="oa-row-label" role="status" aria-live="polite" data-state="loading">Checking what is installed…</span><span id="setup-facts" class="oa-row-caption"></span></div><div class="oa-row-control"><button id="setup-install" class="oa-primary" type="button" disabled>Install</button></div></div>
<div data-ready-owner="page" hidden></div>
<script>` + serviceCommonScript + `
// ---- Words. Apps keep their own words; every engine row comes from the
// overview under its upstream name with the capabilities it serves for OA
// (architect ruling 2026-11-09, option c): llama.cpp, Lemonade, Diffusers,
// TheNoise. A runtime an engine or app needs has no row of its own.
const setupEngines=()=>(setupOverview&&setupOverview.engines)||[{id:'modelhost',title:'llama.cpp',caption:'Chat and embeddings',kind:'included'}];
const setupEngine=id=>setupEngines().find(e=>e.id===id)||null;
function setupEnginePackages(){
 return setupEngines().filter(e=>{
  const choice=setupChoice(e.id);
  return e.kind==='package'&&choice&&!choice.application&&setupOffered(e.id)
 }).map(e=>e.id)
}
const setupEnginePackage=id=>setupEnginePackages().includes(id);
const setupPublishers={'openabstractions':'OpenAbstractions','lemonade-sdk':'Lemonade SDK','black-forest-labs':'Black Forest Labs','comfy-org':'Comfy Org','unsloth':'Unsloth','tongyi-mai':'Tongyi Lab','ggml-org':'ggml.ai','qwen':'Qwen'};
const setupLicenceLinks={'Apache-2.0':'https://www.apache.org/licenses/LICENSE-2.0','MIT':'https://opensource.org/license/mit','GPL-3.0':'https://www.gnu.org/licenses/gpl-3.0.html'};
const setupReleasePage='https://github.com/openabstractions/redist/releases/latest';
// setupWindowsApps opens Windows Settings at Apps to remove a desktop app.
const setupWindowsApps='ms-settings:appsfeatures';
// setupUpstream are an adopted engine's own facts where its catalogue
// package is the runtime it runs on: the details name the engine, not the
// runtime (architect review 2026-11-09).
const setupUpstream={pytorch:{publisher:'Hugging Face',license:'Apache-2.0',source:'https://github.com/huggingface/diffusers'}};
function setupText(node,value){node.textContent=value==null?'':String(value)}
// Sizes read in the Panel's one format (oaBytes, oaBytesOf).
const setupBytes=oaBytes,setupBytesOf=oaBytesOf;
// setupJoin joins names as a sentence does: "A", "A and B", "A, B and C".
const setupJoin=names=>names.length<2?names.join(''):names.slice(0,-1).join(', ')+' and '+names[names.length-1];
// setupNamed is a list of up to three names, else a count: "4 items".
const setupNamed=names=>names.length>3?names.length+' items':setupJoin(names);
function setupPublisher(id){const s=String(id||'');return setupPublishers[s.toLowerCase()]||s.replace(/[-_]+/g,' ')}
function setupModelPublisher(model){const source=String(model.source||''),m=source.match(/^https:\/\/huggingface\.co\/([^/]+)\//);return setupPublisher(m?m[1]:String(model.repository||'').split('/')[0])}
function setupLicenceLink(licence){for(const id in setupLicenceLinks)if(String(licence||'').startsWith(id))return setupLicenceLinks[id];return ''}
// setupLicenceText writes a pinned licence in the Panel's spelling, the noun
// "licence" of its Licence label; the pin keeps its reviewed bytes.
const setupLicenceText=licence=>String(licence||'').replace(/\blicenses\b/g,'licences').replace(/\blicense\b/g,'licence');
// setupVersionText is a package version without its preview build suffix:
// "0.8.0-r17" reads "0.8.0"; the pin keeps the full version as its identity.
const setupVersionText=version=>String(version||'').replace(/^(\d+\.\d+\.\d+)-r\d+$/,'$1');
// setupNotice logs a summary sentence once; the raw error goes to the log
// with it and never onto the page.
const setupNoticed=new Set();
function setupNotice(kind,sentence,detail){const id=kind+'\n'+sentence+'\n'+(detail||'');if(!sentence||setupNoticed.has(id))return;setupNoticed.add(id);call('/setup/notice',{kind,sentence,detail:String(detail||'').slice(0,4000)}).catch(()=>{})}

// ---- What the page knows.
let setupCatalog=null,setupOverview=null,setupLastState=null,setupCoding=null,setupHost=null,setupModelState=null;
let setupStateRead=false,setupStateError=false,setupOverviewRead=false,setupCopies={resolved:false,list:[],incomplete:false};
// setupWant is each tick the person changed; a row without one shows its
// default: what is installed, plus the recommended model an installed app
// lacks (rule 6).
const setupWant=new Map();
let setupPlan=null,setupPlanNow=null,setupPlanError=null,setupPlanKey='',setupShown=null,setupWorking=false,setupResult=null,setupRowError=new Map();
const setupChoice=id=>(((setupCatalog||{}).choices||[]).find(c=>c.id===id))||null;
const setupChoiceComponents=id=>(setupChoice(id)||{}).components||[];
// setupLineComponents are a row's packages with the ones they depend on: the
// torch runtime ComfyUI needs is in ComfyUI's download line.
const setupLineComponents=id=>{const out=[];const add=c=>{if(out.includes(c))return;out.push(c);for(const d of setupCatalogComponent(c).dependencies||[])add(d)};for(const c of setupChoiceComponents(id))add(c);return out};
const setupCatalogComponent=id=>(((setupCatalog||{}).components||[]).find(c=>c.id===id))||{};
const setupComponentState=id=>(((setupLastState||{}).components||[]).find(r=>r.id===id))||null;
const setupOffers=()=>(setupOverview&&setupOverview.picture_models)||[];
const setupOffer=id=>setupOffers().find(o=>'model:'+o.id===id)||null;
const setupCodingModel=()=>(setupOverview&&setupOverview.coding_model)||null;
// setupOffered is whether the release carries a catalogue row and it fits
// this computer: a row the release does not carry is never shown or sent.
function setupOffered(id){if(setupOverview)return (setupOverview.offered||[]).includes(id);const own=setupChoiceComponents(id);return own.length>0&&own.every(c=>setupCatalogComponent(c).artifact)&&!(setupChoice(id)||{}).application}
// setupMode is "current" when the saved plan is this release's and approved,
// "earlier" when it is an earlier release's, else "none".
function setupMode(){const s=setupLastState;if(!s)return 'none';if(s.approved&&s.current_catalog)return 'current';if(!s.current_catalog&&s.plan&&s.plan.id)return 'earlier';return 'none'}
function setupInstalled(id){
 if(setupIsPictureAppRow(id))return false;
 if(setupDesktopIntegrationRow(id))return !!setupDesktopIntegrationFacts().installed;
 if(id==='coding')return !!(setupCodingModel()&&setupCodingModel().installed);
 if(id.startsWith('model:'))return !!(setupOffer(id)||{}).prepared;
 const own=setupChoiceComponents(id);if(!own.length||setupMode()==='none')return false;
 return own.every(c=>setupComponentInstalled(c))}
// setupStale is an installed package of an earlier release whose pin this
// release changes: Install updates it.
function setupStale(id){if(setupMode()!=='earlier'||!setupInstalled(id))return false;const planned=new Map((((setupLastState||{}).plan||{}).components||[]).map(c=>[c.id,c.artifact||{}]));return setupChoiceComponents(id).some(c=>{const a=planned.get(c)||{},b=setupCatalogComponent(c).artifact||{};return a.sha256!==b.sha256||a.version!==b.version})}
// setupPretick is the recommended model an installed app has none of
// (rule 6): the picture or coding model the installed app lacks.
function setupPretick(id){
 // A stopped install arrives with its own ticks only: Install continues
 // exactly that approval.
 if(setupStopped)return false;
 if(id==='coding')return setupCodingDesktop()&&!setupInstalled('coding');
 // Clearing an app's tick selects nothing new for it.
 const offer=setupOffer(id);return !!(offer&&offer.recommended&&!setupOffers().some(o=>o.prepared)&&setupInstalled(setupPictureApp())&&setupWant.get(setupPictureApp())!==false)}
function setupWanted(id){return setupWant.has(id)?setupWant.get(id):setupInstalled(id)||setupPretick(id)}
// setupNeededBy names what needs a row: the engine for a picture app
// or model, ComfyUI for a model only ComfyUI reads.
function setupNeededBy(id){const by=[];const engine=setupOverview&&setupOverview.picture_engine;
 if(id===engine){if(setupWanted(setupPictureApp())||setupPictureAppDependentModels().length)by.push(setupPictureTitle());if(!by.length)for(const o of setupOffers())if(setupWanted('model:'+o.id))by.push(o.title||'a picture model')}
 if(id===setupPictureApp())by.push(...setupPictureAppDependentModels());
 return by}
const setupPictureAppDependentModels=()=>setupOffers().filter(o=>setupPictureApp()&&o.required_application===setupPictureApp()&&!o.prepared&&setupWanted('model:'+o.id)).map(o=>o.title||'a picture model');
function setupTicked(id){return setupWanted(id)||setupNeededBy(id).length>0}
// setupClearable is whether clearing an installed row removes it here: an
// app or engine Setup installed that nothing needs, an update pending or not.
function setupClearable(id){return setupEnginePackage(id)&&setupEngine(id).can_remove===true&&setupInstalled(id)&&!setupNeededBy(id).length}
// setupLocked is an installed row whose tick cannot be cleared here; its
// state line names why (setupLockReason).
const setupLocked=id=>setupInstalled(id)&&setupTicked(id)&&!setupClearable(id);
// setupLockReason is the one reason an installed row's tick is locked: what
// needs it, or the page that removes it. An earlier release's package says
// "Update available" instead, and its details say to update it first.
function setupLockReason(id){const by=setupNeededBy(id);if(by.length)return 'needed by '+setupJoin(by);
 if(setupDesktopIntegrationRow(id))return 'remove in Windows Settings';
 if(setupEnginePackage(id)&&!setupEngine(id).can_remove)return 'managed by OpenAbstractions';
 if(id==='coding'){const used=(setupCodingModel()||{}).used_by||[];return used.length?'needed by '+setupJoin(used):'remove it in Models'}
 if(id.startsWith('model:'))return 'remove it in Models';
 return ''}
function setupRowIDs(){const ids=[];const picture=setupPictureApp();if(picture)ids.push(picture);if(setupPictureAppFacts().oa_build&&setupRetainedPicture().id)ids.push(setupRetainedPicture().id);const desktop=setupDesktopApp();if(desktop)ids.push(desktop);ids.push('coding');for(const o of setupOffers())ids.push('model:'+o.id);return ids.concat(setupEnginePackages())}
function setupRowTitle(id){if(id&&id===setupPictureApp())return setupPictureTitle();if(id&&id===setupRetainedPicture().id)return setupRetainedPicture().title;if(setupDesktopIntegrationRow(id)){const choice=setupChoice(id);return choice.title||id}const engine=setupEngine(id);if(engine)return engine.title;if(id==='coding')return (setupCodingModel()||{}).title||'The coding model';if(id.startsWith('model:')){const o=setupOffer(id)||{};return o.title||'The picture model'}return id==='oa'?'OpenAbstractions':id===setupLegacyDesktop?(setupChoice(id)||{}).title||id:id}
// Desktop integration has a separate Catalog action and never enters a package plan.
function setupPackageChanges(){const adds=[],updates=[],removals=[];for(const id of setupRowIDs()){if(setupIsPictureAppRow(id)||setupDesktopIntegrationRow(id))continue;const ticked=setupTicked(id),installed=setupInstalled(id);if(ticked&&!installed)adds.push(id);else if(ticked&&installed&&setupUpdate(id))updates.push(id);else if(!ticked&&installed&&setupClearable(id))removals.push(id)}return {adds,updates,removals}}
const setupDesktopIntegrationFacts=()=>((setupOverview||{}).desktop_integration||{});
function setupDesktopApp(){const id=setupDesktopIntegrationFacts().app;return id&&(setupChoice(id)||{}).application?id:''}
const setupDesktopIntegrationRow=id=>!!id&&id===setupDesktopApp();
function setupCodingDesktop(){const f=setupDesktopIntegrationFacts();return f.coding&&f.installed?setupDesktopApp():''}
function setupAddedOf(id){if(!setupPlanNow||!setupPlanNow.added)return null;const own=setupLineCounted(id),rows=setupPlanNow.added.filter(a=>own.includes(a.component_id));if(!rows.length)return null;return {download:rows.reduce((n,a)=>n+Number(a.download_bytes||0),0),disk:rows.reduce((n,a)=>n+Number(a.disk_bytes||0),0),update:rows.some(a=>a.update)}}
function setupUpdate(id){return setupEnginePackage(id)&&setupStale(id)}
function setupInstalledBytes(id){if(id==='coding')return Number((setupCodingModel()||{}).download_bytes||0);if(id.startsWith('model:'))return Number((setupOffer(id)||{}).download_bytes||0);let n=0;for(const c of setupChoiceComponents(id)){const p=((setupOverview||{}).installed||{})[c];if(p)n+=Number(p.bytes||0)}return n}
// setupLineCounted are the packages a row's line counts: its own, and a
// dependency not yet installed while no other ticked row counts it first.
function setupLineCounted(id){return setupLineComponents(id).filter(c=>setupChoiceComponents(id).includes(c)||!setupComponentInstalled(c))}
// An installed or uncertain package whose removal no step runs reads
// installed: Install settles the removal (setupSettleRemovals).
function setupComponentInstalled(c){const row=setupComponentState(c),e=(row&&row.effect)||{};return setupMode()!=='none'&&(e.phase==='installed'&&!(e.removing&&setupComponentRunning(c))||e.phase==='uncertain'&&e.removing&&!setupComponentRunning(c))}
function setupCatalogDownload(id){let n=0;for(const c of setupLineCounted(id)){const a=setupCatalogComponent(c).artifact||{};n+=Number(a.bytes||0)+Number((((a.native_resources||{}).backend||{}).archive||{}).bytes||0)}return n}
// setupPlanModels are the picture models the plan prepares and its coding
// model: their sizes join the packages' in the one total.
function setupPlanModels(plan){if(!plan)return [];return [...(plan.picture_models||[]).filter(m=>m.selected),...(plan.coding_model?[plan.coding_model]:[])]}
function setupTotals(){const plan=setupPlanNow,models=setupPlanModels(plan);return {download:Number((plan&&plan.add_download_bytes)||0)+models.reduce((n,m)=>n+Number(m.download_bytes||0),0),disk:Number((plan&&plan.add_disk_bytes)||0)+models.reduce((n,m)=>n+Number(m.peak_bytes||0),0)}}

// ---- Rows. Each row is an expander: its checkbox, title, one caption, one
// state line with a thin progress bar, and the chevron; its details hold
// version, publisher, licence, source, size, folder and use.
const setupRowNodes=new Map();
function setupNode(tag,text,className){const n=document.createElement(tag);if(text!=null)n.textContent=String(text);if(className)n.className=className;return n}
function setupRow(id,tick){let row=setupRowNodes.get(id);if(row&&!!row.box===tick)return row;
 const details=setupNode('details',null,'oa-expander'),summary=setupNode('summary'),text=setupNode('span',null,'oa-row-text'),title=setupNode('span',setupRowTitle(id),'oa-row-label'),caption=setupNode('span','','oa-row-caption'),line=setupNode('span','','oa-status'),bar=setupNode('progress'),body=setupNode('div',null,'oa-expander-body'),chevron=setupNode('span',null,'oa-row-chevron');
 const key=id.replace(/[^a-z0-9-]/gi,'-');details.id='setup-row-'+key;title.id='setup-title-'+key;line.id='setup-line-'+key;line.setAttribute('role','status');chevron.setAttribute('aria-hidden','true');bar.hidden=true;
 let box=null;if(tick){box=setupNode('input');box.type='checkbox';box.id='setup-tick-'+key;box.setAttribute('aria-labelledby',title.id);box.setAttribute('aria-label',setupRowTitle(id));box.setAttribute('aria-describedby',line.id);box.onchange=()=>setupTick(id,box.checked);summary.append(box)}else{const space=setupNode('span',null,'oa-check-space');space.setAttribute('aria-hidden','true');summary.append(space)}
 // llama.cpp's row carries Prepare in its header while the engine is not prepared.
 const prepare=id==='modelhost'?setupNode('button','Prepare'):null;if(prepare){prepare.type='button';prepare.id='setup-modelhost-prepare';prepare.hidden=true;prepare.setAttribute('data-status',line.id);prepare.onclick=e=>{if(e&&e.preventDefault)e.preventDefault();return setupPrepareEngine()}}
 text.append(title,caption,line,bar);summary.append(text,...(prepare?[prepare]:[]),chevron);details.append(summary,body);
 row={id,details,summary,chevron,box,title,caption,line,bar,body,prepare,facts:''};setupRowNodes.set(id,row);return row}
// setupFact is one line of a row's details: a label and its value, and a
// link when it has one.
function setupFact(label,value,href,linkText){const row=setupNode('div',null,'oa-row'),text=setupNode('span',null,'oa-row-text'),control=setupNode('span',null,'oa-row-control');text.append(setupNode('span',label,'oa-row-label'));// A long value reads as the fact's caption, under its label.
if(value&&String(value).length>48)text.append(setupNode('span',value,'oa-row-caption'));else if(value)control.append(setupNode('span',value));if(href){const a=setupNode('a',linkText||value);a.href=href;a.target='_blank';a.rel='noopener noreferrer';control.append(a)}row.append(text,control);row.spec=JSON.stringify([label,value,href,linkText]);return row}
// setupTechnical holds raw values (a folder, a pinned revision) behind
// Technical details, the one place the Panel shows them.
function setupTechnical(lines){const kept=lines.filter(Boolean);if(!kept.length)return null;const details=setupNode('details',null,'oa-expander'),summary=setupNode('summary','Technical details'),chevron=setupNode('span',null,'oa-row-chevron'),body=setupNode('div',null,'oa-expander-body');chevron.setAttribute('aria-hidden','true');summary.append(chevron);for(const line of kept)body.append(setupNode('p',line));details.append(summary,body);details.spec=JSON.stringify(kept);return oaTechnical(details)}
function setupDetails(row,items){const parts=items.filter(Boolean),key=JSON.stringify(parts.map(p=>p.spec));if(row.facts===key&&row.body.children&&row.body.children.length)return;row.facts=key;row.body.replaceChildren(...parts)}
function setupLine(row,text,state){setupText(row.line,text);if(row.line.setAttribute)row.line.setAttribute('data-state',state||'idle');else row.line.dataset.state=state||'idle'}
function setupBar(row,done,total,busy){if(total>0){row.bar.hidden=false;row.bar.max=total;row.bar.value=Math.max(0,Math.min(done,total))}else if(busy){row.bar.hidden=false;if(row.bar.removeAttribute)row.bar.removeAttribute('value')}else row.bar.hidden=true}

// setupLive is a row's state while the button's work runs: its download,
// its check, its preparation or its error. null when the work has not
// reached it.
function setupLive(id){
 if(setupRowError.has(id))return {text:setupRowError.get(id),state:'error'};
 // A row of a stopped install reads Stopped; a coding download whose job
 // still runs shows its bytes.
 if(!setupWorking&&setupStopped&&[...setupStopped.ch.adds,...setupStopped.ch.updates].includes(id)){const a=id==='coding'?((((setupCoding||{}).status||{}).assets)||[])[0]||{}:{},t=a.transfer||{};return t.total>0&&['pending','running'].includes(t.state)?{text:'Downloading · '+setupBytesOf(t.done||0,t.total),done:t.done,total:t.total}:{text:setupStopped.ch.updates.includes(id)?'Update ready':'Stopped'}}
 if(!setupWorking||!setupWork||!setupWork.rows.includes(id))return null;
 if(id===setupLegacyDesktop)return {text:setupWork.opencode?'Opens the OpenAbstractions installer':'Waiting',busy:!!setupWork.opencode};
 if(id==='coding'){const a=((((setupCoding||{}).status||{}).assets)||[])[0]||{},t=a.transfer||{};if(a.phase==='verified')return {text:'Installed'};if(t.total>0&&t.state!=='complete')return {text:'Downloading · '+setupBytesOf(t.done||0,t.total),done:t.done,total:t.total};if(t.state==='complete')return {text:'Checking files',busy:true};return {text:'Waiting'}}
 if(id.startsWith('model:')){const offer=setupOffer(id)||{},s=setupModelStatus(offer.id);if(!s||!s.approved)return {text:'Waiting'};if(s.provider_configured)return {text:'Installed'};const assets=s.assets||[];if(assets.length&&assets.every(a=>a.phase==='verified'))return {text:'Preparing',busy:true};let done=0,total=0;for(const a of assets){const pinned=((offer.assets||[]).find(x=>x.id===a.id)||{}).download||{},bytes=Number(pinned.bytes||0);total+=bytes;if(a.phase==='verified')done+=bytes;else if(a.transfer&&a.transfer.total>0)done+=Number(a.transfer.done||0)}if(assets.some(a=>a.phase==='requested'))return {text:'Downloading · '+setupBytesOf(done,total),done,total};return {text:'Checking files',busy:true}}
 if(setupWork.removals.includes(id))return setupInstalled(id)?{text:'Removing',busy:true}:null;
 const own=setupChoiceComponents(id).map(setupComponentState).filter(Boolean);if(!own.length)return {text:'Waiting'};
 // An update keeps the earlier version installed until it is replaced:
 // its row reads Updating, with the step's progress when it reports one.
 if(own.every(r=>(r.effect||{}).phase==='installed'&&!(r.effect||{}).removing)){if(!(setupWork.updates||[]).includes(id))return {text:'Installed'};const a=setupChoiceComponents(id).map(c=>((setupLastState||{}).actions||{})[c]).find(Boolean)||{};return a.total>0?{text:'Updating · '+Number(a.done||0).toLocaleString('en-US')+' of '+Number(a.total).toLocaleString('en-US')+' files',done:a.done,total:a.total}:{text:'Updating…',busy:true}}
 for(const r of own){const e=r.effect||{},t=r.transfer||{};if(e.removing)return {text:'Removing',busy:true};if(e.phase==='requested'&&t.total>0&&t.state!=='complete')return {text:'Downloading · '+setupBytesOf(t.done||0,t.total),done:t.done,total:t.total};if(e.phase==='requested'||e.phase==='verified')return {text:'Checking files',busy:true};if(['installing','staged','checked'].includes(e.phase))return {text:'Preparing',busy:true}}
 return {text:'Waiting'}}
function setupModelStatus(planID){const r=setupModelState;if(!r)return null;if(r.plan&&r.plan.id===planID)return r.status||null;return (r.choice_status||{})[planID]||null}

// setupIdle is a row's state line between changes, from the closed set of
// the ruling: installed with its use and size, its download, an update, a
// removal, or why it cannot change.
function setupIdle(id){
 // A package a step runs on now says so with its progress.
 for(const c of setupChoiceComponents(id)){const a=((setupLastState||{}).actions||{})[c];if(a&&setupComponentRunning(c))return (a.kind==='remove'?'Removing':a.kind==='repair'?'Repairing':'Installing')+(a.total?' · '+setupCount(a)+' files checked':'')}
 const fix=setupFix(id);if(fix)return setupFixLine(fix);
 const installed=setupInstalled(id),ticked=setupTicked(id),by=setupNeededBy(id);
 if(setupDesktopIntegrationRow(id)){const f=setupDesktopIntegrationFacts();if(f.reason)return f.reason;if(!installed)return 'Not installed';const coding=setupCodingModel()||{},parts=['Installed'];if(id===setupCodingDesktop()&&(coding.used_by||[]).includes(setupRowTitle(id)))parts.push('uses '+coding.title);parts.push(setupLockReason(id));return parts.join(' · ')}
 if(installed&&!ticked){const frees=setupInstalledBytes(id);return 'Will be removed'+(frees?' · frees '+setupBytes(frees):'')}
 if(installed){const added=setupAddedOf(id);if(setupUpdate(id))return 'Update available'+(added&&added.download?' · '+setupBytes(added.download)+' download':'');const parts=['Installed'];
  // A locked row says why; a picture model also names the app reading it.
  if(id.startsWith('model:')&&setupInstalled(setupPictureApp()))parts.push('used by '+setupPictureTitle());
  const lock=setupLocked(id)?setupLockReason(id):'';if(lock)parts.push(lock);
  else if(setupEnginePackage(id))parts.push('not used by any app');
  const size=setupInstalledBytes(id);if(size)parts.push(setupBytes(size));return parts.join(' · ')}
 if(id===setupPictureApp()&&!ticked&&setupOtherCopy())return 'Another '+setupPictureTitle()+' is on this computer. Setup leaves it as it is.';
 if(id==='coding'||id.startsWith('model:')){const m=id==='coding'?setupCodingModel()||{}:setupOffer(id)||{},size=setupBytes(m.download_bytes);if(m.archived)return size+' · copies from your download archive';const app=id==='coding'&&ticked?setupCodingDesktop():'';const used=app?'Used by '+setupRowTitle(app)+' · ':'';return used+size+' download'}
 const added=setupAddedOf(id),download=added?added.download:setupCatalogDownload(id),size=setupBytes(download)+' download';
 if(ticked&&by.length)return setupLineCounted(id).length&&by.every(name=>name!==setupPictureTitle()||setupInstalled(setupPictureApp()))?'Needed by '+setupJoin(by)+' · '+size:'Needed by '+setupJoin(by);
 return size}
const setupOtherCopy=(id=setupPictureApp())=>setupCopies.list.some(c=>c.app===id);

// setupDrawRow writes one row: tick, caption, state line, bar and details.
function setupDrawRow(id){if(setupIsPictureAppRow(id))return setupDrawPictureApp(id);const kind=(setupEngine(id)||{}).kind,tick=id!=='oa'&&kind!=='included'&&kind!=='provider'&&!setupDesktopIntegrationRow(id),row=setupRow(id,tick);if(row.desktopAction)row.desktopAction.hidden=true;setupText(row.title,setupRowTitle(id));
 const offer=id.startsWith('model:')?setupOffer(id):null;const engine=setupEngine(id);setupText(row.caption,id==='coding'?'Recommended for this computer':offer?offer.caption:engine?engine.caption:(setupDesktopIntegrationRow(id)&&setupDesktopIntegrationFacts().coding?'Coding assistant':id==='oa'?'Version '+((setupOverview||{}).version||''):''));
 if(row.box){const by=setupNeededBy(id);row.box.checked=setupTicked(id);row.box.disabled=setupWorking||!setupStateRead||setupStateError||by.length>0||setupLocked(id)}
 if(id==='oa')setupLine(row,'');else if(id==='modelhost')setupDrawHost(row);else if(kind==='provider')setupLine(row,setupEngine(id).ready?'Included with OpenAbstractions · Model in place':'Included with OpenAbstractions · No picture model yet');else{const live=setupLive(id);if(live){setupLine(row,live.text,live.state);setupBar(row,live.done||0,live.total||0,live.busy)}else{setupLine(row,setupStateRead&&!setupStateError?setupIdle(id):'');setupBar(row,0,0,false)}}
 setupDrawRepair(id,row);setupDrawDetails(id,row);return row}
function setupDrawDetails(id,row){const items=[];
 if(setupEnginePackage(id)){const own=setupChoiceComponents(id),a=(setupCatalogComponent(own[0]).artifact)||{},installed=((setupOverview||{}).installed||{})[own[0]];
  const was=(((((setupLastState||{}).plan||{}).components)||[]).find(c=>c.id===own[0])||{}).artifact;
  const up=setupUpstream[id];
  if(up){items.push(setupFact('Publisher',up.publisher));items.push(setupFact('Licence',up.license,setupLicenceLink(up.license),'Read it'));items.push(setupFact('Source',null,up.source,'View the source code'))}
  else{let now=setupVersionText(a.version),before=setupUpdate(id)&&was&&was.version?setupVersionText(was.version):'';
   // An update between two builds of one version names both builds in full.
   if(before&&before===now){now=String(a.version||'');before=String(was.version)}
   items.push(setupFact('Version',before?now+' (installed: '+before+')':now))}
  if(a.publisher&&!up)items.push(setupFact('Publisher',setupPublisher(a.publisher)));
  if(a.license&&!up)items.push(setupFact('Licence',setupLicenceText(a.license),setupLicenceLink(a.license),setupLicenceLink(a.license)?'Read it':''));
  if(!up&&/^https:\/\/github\.com\//.test(a.source_url||''))items.push(setupFact('Source',null,a.source_url,'View the source code'));
  if(installed&&installed.bytes)items.push(setupFact('Size on disk',setupBytes(installed.bytes)));
  const by=setupNeededBy(id);if(by.length)items.push(setupFact('Used by',setupJoin(by)));
  if(id===setupPictureApp())for(const p of (setupPlanNow&&setupPlanNow.permissions)||[])items.push(setupFact('Permissions',(p.sentences||[]).join(' ')));
  items.push(setupTechnical([installed&&installed.folder?'Folder: '+installed.folder:'',a.source_commit&&!up?'Source revision: '+a.source_commit:'',a.sha256&&!up?'Package SHA-256: '+a.sha256:'',...setupCopies.list.filter(c=>c.app===id).map(c=>'Other copy: '+String(c.location||'')),setupRowFault(id)]))}
 else if(id==='coding'||id.startsWith('model:')){const m=id==='coding'?setupCodingModel()||{}:setupOffer(id)||{},licence=(m.licenses||[]).join(', '),link=(((m.assets||[])[0]||{}).license_url)||setupLicenceLink(licence);
  items.push(setupFact('Publisher',setupModelPublisher(m)));if(licence)items.push(setupFact('Licence',setupLicenceText(licence),link,link?'Read it':''));items.push(setupFact(setupInstalled(id)?'Size on disk':'Download',setupBytes(m.download_bytes)));
  const used=id==='coding'?(m.used_by||[]):m.required_application&&m.required_application===setupPictureApp()||setupInstalled(setupPictureApp())?[setupPictureTitle()]:[];if(used.length)items.push(setupFact('Used by',setupJoin(used)));
  if(/^https:\/\/huggingface\.co\//.test(m.source||''))items.push(setupFact('Source',null,m.source,'Read the model card'));
  if(setupInstalled(id))items.push(setupFact('Remove','Models lists every copy and can remove it.','/inference?k='+encodeURIComponent(key),'Open Models'));
  items.push(setupTechnical([m.repository?'Repository: '+m.repository:'',m.revision?'Pinned revision: '+m.revision:'',setupRowFault(id)]))}
 const fixed=setupFix(id);if(fixed&&fixed.detail)items.push(setupFact('OpenAbstractions fix',fixed.detail));
 if(setupDesktopIntegrationRow(id)){if(setupInstalled(id))items.push(setupFact('Remove',null,setupWindowsApps,'Open Windows Settings'))}
 else if(id==='oa')items.push(setupFact('Updates',null,setupReleasePage,'Check for updates'));
 else if((setupEngine(id)||{}).kind==='provider')items.push(setupFact('Installed by','OpenAbstractions'));
 if(!setupEnginePackage(id)&&id!=='coding'&&!id.startsWith('model:')&&setupRowFault(id))items.push(setupTechnical([setupRowFault(id)]));
 if(id!=='modelhost')setupDetails(row,items)}

// ---- Picture integration and the retained native package.
const setupIsPictureAppRow=id=>!!id&&(id===setupRetainedPicture().id||id===setupPictureApp());
const setupPictureAppFacts=()=>(setupOverview&&setupOverview.picture_application)||{};
const setupRetainedPicture=()=>setupPictureAppFacts().retained||{};
const setupPictureApp=()=>setupPictureAppFacts().id||'';
const setupWorkspace=typeof oaPictureWorkspace==='object'&&oaPictureWorkspace||{};
const setupWorkspaceApp=()=>setupWorkspace.application||'';
const setupPictureTitle=()=>setupPictureAppFacts().title||setupWorkspace.title||'the picture app';
const setupPictureStep=()=>setupPictureAppFacts().connect_step||'';
const setupAdapterPath=path=>typeof path==='string'&&/^\/setup\/[a-z0-9-]+(?:\/[a-z0-9-]+)*$/.test(path)?path:'';
function setupPicturePath(action){
 const routes=setupPictureAppFacts().actions||{};
 return Object.prototype.hasOwnProperty.call(routes,action)?setupAdapterPath(routes[action]):'';
}
let setupPictureAppBusy='';
// setupPictureAppProgress is the row's state while its work runs, or its error.
function setupPictureAppProgress(id){if(setupRowError.has(id))return {text:setupRowError.get(id),state:'error'};
 const official=id===setupPictureApp(),step=official?setupPictureStep():setupRetainedPicture().component,a=((setupLastState||{}).actions||{})[step];
 if(a&&setupComponentRunning(step))return official?{text:a.kind==='update'?'Updating':'Connecting',busy:true}:{text:'Removing'+(a.total?' · '+setupCount(a)+' files checked':''),done:a.done,total:a.total,busy:!a.total};
 if(setupPictureAppBusy===id)return {text:official?'Opening '+setupPictureTitle():'Removing',busy:true};
 return null}
const setupLegacyDesktop='opencode';
const setupDesktopIntegrationStep='desktop-integration';
let setupDesktopIntegrationBusy=false;
function setupHasDesktopAction(id){if(!setupDesktopIntegrationRow(id))return false;const f=setupDesktopIntegrationFacts();return (f.installed&&f.action)||setupComponentRunning(setupDesktopIntegrationStep)||setupStepEnded(setupDesktopIntegrationStep)}
function setupDrawDesktopIntegration(id){const row=setupRow(id,false),f=setupDesktopIntegrationFacts(),busy=setupDesktopIntegrationBusy||setupComponentRunning(setupDesktopIntegrationStep),done=setupStepEnded(setupDesktopIntegrationStep);
 const title=setupRowTitle(id),update=f.action==='update';
 setupText(row.title,title);setupText(row.caption,f.coding?'Coding assistant':'');
 const failure=busy?'':setupRowError.get(id)||(done&&done.failure);
 const line=failure||(busy?'Applying the integration…':f.reason||(update?'Close '+title+' before updating its connection.':done&&!done.cancelled?(done.kind==='update'?'Update':'Repair')+' finished. Open '+title+' to use the integration.':f.action?'Close '+title+' before repairing its connection.':''));
 setupLine(row,setupOverviewRead?line:'',failure?'error':'');setupBar(row,0,0,busy);
 if(row.repair)row.repair.hidden=true;
 const button=setupRowAction(row,'setup-action-',setupApplyDesktopIntegration,'desktopAction');
 button.hidden=!setupOverviewRead||!f.installed||!f.action;button.disabled=busy||setupWorking||setupActionsRunning();setupText(button,failure?'Retry':(update?'Update ':'Repair ')+title+' connection');
 setupDetails(row,[setupTechnical([failure&&(setupRowFault(id)||(done&&done.detail)||'')])]);return row}
async function setupApplyDesktopIntegration(id){if(!setupDesktopIntegrationRow(id)||!setupInstalled(id)||!setupDesktopIntegrationFacts().action||setupDesktopIntegrationBusy||setupWorking||setupActionsRunning())return;
 const action=setupDesktopIntegrationFacts().action;
 setupRowError.delete(id);setupRowDetail.delete(id);setupDesktopIntegrationBusy=true;setupDraw();
 try{const reply=await call('/setup/desktop-integration?action='+encodeURIComponent(action),{confirm:true});setupLastState={...(setupLastState||{}),actions:reply.actions||{},outcomes:reply.outcomes||{}};
  const outcome=setupStepEnded(setupDesktopIntegrationStep,null,reply.step_id);if(outcome&&outcome.failure)return setupDesktopIntegrationFailed(outcome);
  if(setupComponentRunning(setupDesktopIntegrationStep))setupWatchStep();else return setupReload()}
 catch(e){const busy=setupBusy(e);if(busy){setupLastState={...(setupLastState||{}),actions:busy.actions||{},outcomes:busy.outcomes||{}};setupWatchStep()}
  else return setupDesktopIntegrationFailed({failure:setupRowTitle(id)+(action==='update'?' could not be updated.':' could not be repaired.'),detail:String(e&&e.message||e)})}
 finally{setupDesktopIntegrationBusy=false;setupDraw()}}
function setupDesktopIntegrationFailed(o){const app=setupDesktopApp();if(app){setupRowError.set(app,o.failure);if(o.detail)setupRowDetail.set(app,o.detail)}return setupReload()}
function setupDrawPictureApp(id){const row=setupRow(id,false),f=setupPictureAppFacts(),live=setupPictureAppProgress(id),waiting=setupWorking||setupActionsRunning()||!setupOverviewRead||!!setupPictureAppBusy;setupText(row.title,setupRowTitle(id));setupText(row.caption,id===setupPictureApp()?'Create pictures':'Installed by an earlier OpenAbstractions');
 let line='',label='',action='',ready=true;
 if(id===setupRetainedPicture().id){line=f.oa_build_removing?'Removing stopped before it finished':'Installed · removing it keeps your pictures and models';label='Remove '+setupRetainedPicture().removal_title;action='remove-oa'}
 else if(f.state==='connected'&&f.changed){line='Installed · changed since you connected it';label='Connect';action='connect'}
 else if(f.state==='connected'&&f.update&&!f.update_blocked){line='Installed · an update to its OpenAbstractions integration is ready';label='Update';action='update'}
 else if(f.state==='connected'){line=f.update?'Installed · update unavailable: '+f.update_blocked:f.restart?'Installed · restart '+setupPictureTitle()+' to use the updated integration':'Installed · connected to OpenAbstractions';label=f.open?'Open':'';action='open'}
 else if(f.state==='connect'){line=f.connectable?'Installed · not connected to OpenAbstractions':'Installed · this OpenAbstractions build can\'t connect it yet';label='Connect';action='connect';ready=!!f.connectable}
 else if(f.state==='finish'){line=f.finish||'';label='Open';action='open'}
 else if(f.state==='get'){line='Not installed';label='Get '+setupPictureTitle();action='get'}
 if(live){setupLine(row,live.text,live.state);setupBar(row,live.done||0,live.total||0,live.busy)}else{setupLine(row,setupOverviewRead?line:'');setupBar(row,0,0,false)}
 setupPictureAppButton(row,setupOverviewRead?label:'',action,waiting||!ready||!!(live&&live.busy));
 const items=[];
 if(id===setupRetainedPicture().id){items.push(setupFact('Removed','Its program, Apps entry and Start menu shortcut.'));items.push(setupFact('Kept','Your pictures, models and the files you added.'));items.push(setupTechnical([setupRowFault(id)]))}
 else{if(f.publisher)items.push(setupFact('Publisher',f.publisher));if(f.license)items.push(setupFact('Licence',f.license,setupLicenceLink(f.license),'Read it'));if(f.source)items.push(setupFact('Source',null,f.source,'View the source code'));
  if(f.state!=='connected')items.push(setupPictureAppFolderFact(row,waiting));
  items.push(setupTechnical([f.folder?'Folder: '+f.folder:'',setupRowFault(id)]))}
 setupDetails(row,items);if(row.folder)row.folder.disabled=waiting;return row}
// setupPictureAppFolderFact is the details row that chooses a portable picture application's
// folder with the window's folder picker.
function setupPictureAppFolderFact(row,waiting){const fact=setupNode('div',null,'oa-row'),text=setupNode('span',null,'oa-row-text'),control=setupNode('span',null,'oa-row-control'),button=setupNode('button','Choose folder…');
 const status=setupNode('span','','oa-status');status.id='setup-picture-folder-status';status.setAttribute('role','status');text.append(setupNode('span','Portable '+setupPictureTitle(),'oa-row-label'),setupNode('span','Choose its folder when the Catalog did not find it.','oa-row-caption'),status);button.type='button';button.id='setup-comfy-folder';button.setAttribute('data-status',status.id);button.disabled=waiting;button.onclick=e=>{if(e&&e.preventDefault)e.preventDefault();return setupPictureAppAction(row.id,'folder')};control.append(button);fact.append(text,control);fact.spec=JSON.stringify(['Portable '+setupPictureTitle(),'folder',!!waiting]);row.folder=button;return fact}
async function setupPictureAppAction(id,action){const f=setupPictureAppFacts();if(!action||!setupIsPictureAppRow(id)||setupPictureAppBusy||setupActionsRunning())return;
 if(action==='remove-oa'?id!==setupRetainedPicture().id||!f.oa_build:id!==setupPictureApp())return;
 setupRowError.delete(id);setupRowDetail.delete(id);
 if(action==='get'){const url=setupWorkspace.download;if(url&&typeof open==='function')open(url,'_blank');return}
 const path=setupPicturePath(action);if(!path)return;
 const step=action==='connect'||action==='update'||action==='remove-oa';setupPictureAppBusy=action==='folder'?'':id;setupDraw();
 try{const reply=await call(path,action==='folder'?{}:{confirm:true});
  if(step){setupLastState={...(setupLastState||{}),actions:reply.actions||{},outcomes:reply.outcomes||{}};setupPictureAppBusy='';setupDraw();setupWatchStep();return}
  if(action==='folder'){if(!reply.available)setupRowError.set(id,'This window can\'t show a folder picker.');else if(reply.chosen){await setupReadOverview()}}}
 catch(e){const text=String(e&&e.message||e),busy=setupBusy(e);if(busy){setupLastState={...(setupLastState||{}),actions:busy.actions||{},outcomes:busy.outcomes||{}};setupPictureAppBusy='';setupDraw();setupWatchStep();return}
  // A refusal the Panel writes as one plain sentence is said as it is.
  const first=text.trim().split('\n')[0],said=first.length<=160&&/^[A-Z][^{}\\:]*\.$/.test(first)?first:oaPlain(text).line;setupRowError.set(id,said);setupRowDetail.set(id,text);setupNotice('failed',said,text)}
 setupPictureAppBusy='';setupDraw()}
function setupPictureAppButton(row,label,action,disabled){if(!row.action){const button=setupNode('button','');button.type='button';button.id='setup-action-'+row.id;button.setAttribute('data-status',row.line.id);button.onclick=e=>{if(e&&e.preventDefault)e.preventDefault();return setupPictureAppAction(row.id,row.actionName)};if(row.summary.insertBefore)row.summary.insertBefore(button,row.chevron);else row.summary.append(button);row.action=button}
 row.actionName=action;row.action.hidden=!label;setupText(row.action,label||'');row.action.disabled=!!disabled}
// setupConnectionFailed shows a Connect that did not finish on the app's row and
// reads the rows again.
function setupConnectionFailed(id,o){const title=setupRowTitle(id),update=o.kind==='update',reason=setupReason(o.failure);setupRowError.set(id,reason||(update?'Update':'Connect')+' did not finish. Try again.');if(o.detail||o.failure)setupRowDetail.set(id,String(o.detail||o.failure));setupNotice('failed',update?'The '+title+' integration was not updated.':title+' was not connected.',String(o.detail||o.failure||''));return setupReload()}

// ---- Repair. An installed app whose adopter fix-up is gone (the overview's
// fixes, setup.Fixup) says so on its row with one Repair button and never
// reads Installed. Repair runs as a Setup step; the app must be closed.
const setupFix=id=>(((setupOverview||{}).fixes)||[]).find(f=>f.app===id&&f.state!=='current')||null;
function setupRowAction(row,prefix,activate,member='repair'){if(!row[member]){const button=setupNode('button','Repair');button.type='button';button.id=prefix+row.id.replace(/[^a-z0-9-]/gi,'-');button.setAttribute('data-status',row.line.id);button.onclick=e=>{if(e&&e.preventDefault)e.preventDefault();return activate(row.id)};if(row.summary.insertBefore)row.summary.insertBefore(button,row.chevron);else row.summary.append(button);row[member]=button}return row[member]}
// setupFixLine is the row's state line: what is wrong and what to do, in the
// sibling rows' compact form.
function setupFixLine(fix){if(fix.state==='needs_repair')return fix.sentence+(fix.repair&&fix.running?' · Close '+fix.name+' to repair':'');if(fix.supported)return fix.sentence+' · Reinstall '+fix.name+' with OpenAbstractions';return fix.sentence+((fix.versions||[]).length?' · Supports '+setupJoin(fix.versions):'')}
function setupDrawRepair(id,row){const fix=setupFix(id);if(!fix||!fix.repair){if(row.repair)row.repair.hidden=true;return}
 setupRowAction(row,'setup-repair-',setupRepair);
 row.repair.hidden=false;row.repair.disabled=setupWorking||setupActionsRunning()||!setupStateRead}
async function setupRepair(id){const fix=setupFix(id),row=setupRowNodes.get(id);if(!fix||!row||setupActionsRunning())return;setupRowError.delete(id);setupRowDetail.delete(id);row.repair.disabled=true;setupLine(row,'Repairing','loading');setupBar(row,0,0,true);
 try{const reply=await call('/setup/repair',{component_id:fix.component,confirm:true});setupLastState={...(setupLastState||{}),actions:reply.actions||{},outcomes:reply.outcomes||{}};setupDraw();setupWatchStep()}
 catch(e){const text=String(e&&e.message||e),busy=setupBusy(e);let said='';if(busy){said=String(busy.error||text);setupLastState={...(setupLastState||{}),actions:busy.actions||{},outcomes:busy.outcomes||{}}}
  if(!said){const line=text.trim().split('\n')[0];said=line.length<=160&&/\.$/.test(line)?line:'Repair did not start. Try again.'}
  setupRowError.set(id,said);setupNotice('failed',said,text);setupBar(row,0,0,false);setupDraw();if(setupActionsRunning())setupWatchStep()}}
// setupRepairFailed shows a Repair's failure on its row and reads the rows again.
function setupRepairFailed(c,o){const id=setupComponentRow(c);setupRowError.set(id,String(o.failure||'Repair did not finish.'));if(o.detail)setupRowDetail.set(id,String(o.detail));setupNotice('failed',String(o.failure||''),'repair');return setupReload()}

// ---- The model host row: included with OpenAbstractions, no tick. Its
// pinned-ZIP recovery (linked from Models as #setup-modelhost) sits in its
// details.
// A failed read or a stopped runtime says nothing here: the page's banner
// says OpenAbstractions is not running.
function setupDrawHost(row){const h=setupHost||{},engine=h.engine||{},feature=h.feature||{},bundled=engine.bundled||{},known=!!setupHost&&setupHost.available!==false&&!(typeof oaRuntimeIsDown==='function'&&oaRuntimeIsDown());
 if(!known){if(!setupHostBusy)setupLine(row,'')}else if(engine.staged)setupLine(row,'Included with OpenAbstractions · Ready');else if(!setupHostBusy)setupLine(row,setupHostSaid||'Not prepared yet.',setupHostSaidState);
 if(row.prepare){row.prepare.hidden=!(known&&!engine.staged&&feature.installed&&bundled.available&&bundled.approval_digest);row.prepare.disabled=setupHostBusy}
 if(!row.recovery)setupBuildRecovery(row);setupHostRecoveryState()}
// setupPrepareEngine is Prepare on llama.cpp's row: it stages the engine
// included with OpenAbstractions, the step Models' Prepare runs.
let setupHostSaid='',setupHostSaidState='idle';
async function setupPrepareEngine(){const row=setupRowNodes.get('modelhost'),h=setupHost||{},bundled=(h.engine||{}).bundled||{};if(setupHostBusy||!row||!bundled.approval_digest)return;setupHostBusy=true;setupHostSaid='';setupLine(row,'Preparing llama.cpp…','loading');setupBar(row,0,0,true);row.prepare.disabled=true;
 try{const r=await call('/setup/modelhost/engine/bundled',{plan_id:h.plan_id||'',approval_digest:bundled.approval_digest,confirm:true});if(!r.staged){setupHostSaid='llama.cpp was not prepared. Try again.';setupHostSaidState='error'}}
 catch(e){setupHostSaid=oaPlain(e&&e.message||e).line;setupHostSaidState='error';setupNotice('failed','llama.cpp was not prepared.',String(e&&e.message||e))}
 setupHostBusy=false;setupBar(row,0,0,false);await setupReadHost()}
function setupBuildRecovery(row){const pin=setupFact('Pinned release',''),file=setupNode('input'),consentRow=setupNode('label'),consent=setupNode('input'),stage=setupNode('button','Verify and stage'),status=setupNode('p','','oa-status'),choose=setupNode('div',null,'oa-row'),chooseText=setupNode('span',null,'oa-row-text'),chooseControl=setupNode('span',null,'oa-row-control'),technical=setupNode('div');
 file.type='file';file.id='setup-modelhost-archive';file.accept='.zip,application/zip';file.setAttribute('aria-label','Choose the engine ZIP…');consent.type='checkbox';consent.id='setup-modelhost-engine-consent';consentRow.append(consent,setupNode('span','I approve staging this exact ZIP.'));stage.id='setup-modelhost-engine-stage';stage.type='button';status.id='setup-modelhost-engine-status';status.setAttribute('role','status');technical.id='setup-modelhost-pin';
 chooseText.append(setupNode('span','Choose the engine ZIP…','oa-row-label'));chooseControl.append(file,stage);choose.append(chooseText,chooseControl,consentRow,status);
 row.recovery={pin,file,consent,consentRow,stage,status,choose,technical};row.body.replaceChildren(pin,choose,technical);
 file.onchange=setupHostRecoveryState;consent.onchange=setupHostRecoveryState;stage.onclick=setupStageEngine}
let setupHostBusy=false;
function setupHostRecoveryState(){const row=setupRowNodes.get('modelhost');if(!row||!row.recovery)return;const r=row.recovery,h=setupHost||{},pin=h.pin||{},engine=h.engine||{},feature=h.feature||{},file=(r.file.files||[])[0],matches=!!file&&file.name===pin.archive&&Number(file.size)===Number(pin.size),eligible=!!(feature.installed&&pin.archive&&!pin.archive_refused&&!engine.staged);
 const control=r.pin.children&&r.pin.children[1];r.pin.hidden=!pin.archive;if(control)control.replaceChildren(setupNode('span',pin.archive?'llama.cpp '+String(pin.release||'')+' · '+setupBytes(pin.size):''));
 r.choose.hidden=!eligible;r.consentRow.hidden=!eligible||!engine.approval;r.stage.disabled=setupHostBusy||!matches||!pin.sha256||!engine.approval||!engine.approval_digest||!r.consent.checked;
 if(file&&eligible&&!setupHostBusy)setupText(r.status,matches?'This file matches the pinned ZIP; Setup checks it again before staging.':'This file is not the pinned ZIP.');
 const url=/^https:\/\/github\.com\/ggml-org\/llama\.cpp\/releases\/download\/[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/.test(pin.url||'')?pin.url:'';const technical=setupTechnical([pin.archive?'Archive: '+pin.archive:'',pin.sha256?'SHA-256: '+pin.sha256:'',pin.engine_dir?'Destination: '+pin.engine_dir:'',url?'Publisher download: '+url:'',engine.approval?'Approval: '+engine.approval:'']),spec=technical?technical.spec:'';if(spec!==r.techSpec){r.techSpec=spec;r.technical.replaceChildren(...(technical?[technical]:[]))}}
async function setupStageEngine(){const row=setupRowNodes.get('modelhost'),r=row.recovery,h=setupHost||{},pin=h.pin||{},engine=h.engine||{},file=(r.file.files||[])[0];if(setupHostBusy||!file||file.name!==pin.archive||Number(file.size)!==Number(pin.size)||!r.consent.checked)return;const form=new FormData();form.set('plan_id',h.plan_id||'');form.set('pin_digest',pin.sha256);form.set('approval_digest',engine.approval_digest);form.set('confirm','true');form.set('archive',file,file.name);setupHostBusy=true;setupText(r.status,'Checking and staging the pinned engine.');setupHostRecoveryState();let sentence='';try{const response=await fetch('/setup/modelhost/engine',{method:'POST',headers:{'X-Panel-Key':key},body:form});if(!response.ok)throw Error(await response.text());sentence=(await response.json()).staged?'The pinned engine is staged.':'The pinned engine was not confirmed as staged.'}catch(e){sentence='Staging stopped. Check that the file is the pinned ZIP.';setupNotice('failed',sentence,String(e&&e.message||e))}setupHostBusy=false;r.consent.checked=false;await setupReadHost();setupText(r.status,sentence)}
async function setupReadHost(){try{setupHost=await call('/setup/modelhost/chat')}catch(e){setupHost={available:false}}setupDraw()}
function setupShowRecovery(){if(location.hash!=='#setup-modelhost')return;const row=setupRowNodes.get('modelhost');if(row){row.details.open=true;if(row.details.scrollIntoView)row.details.scrollIntoView()}}

// ---- The page.
function setupDraw(){setupPlanNow=setupPlan&&!setupPlanError&&setupPlanKey===setupRequestKey()?setupPlan:null;if(!setupCatalog)return setupDrawSummary();
 const apps=[],chat=[],pictures=[],engines=[];for(const id of setupRowIDs()){const node=(setupHasDesktopAction(id)?setupDrawDesktopIntegration(id):setupDrawRow(id)).details;if(id==='coding')chat.push(node);else if(id.startsWith('model:'))pictures.push(node);else if(setupEnginePackage(id))engines.push(node);else apps.push(node)}
 const placed=[];for(const e of setupEngines()){const node=e.kind==='package'?engines.find(n=>n.id==='setup-row-'+e.id):setupDrawRow(e.id).details;if(node)placed.push(node)}engines.splice(0,engines.length,...placed);
 oaPlaceRows(el('setup-app-rows'),apps);oaPlaceRows(el('setup-chat-rows'),chat);oaPlaceRows(el('setup-picture-rows'),pictures);oaPlaceRows(el('setup-engine-rows'),engines);oaPlaceRows(el('setup-oa-rows'),[setupDrawRow('oa').details]);
 el('setup-picture-head').hidden=!pictures.length;
 const picture=setupEngine(setupOverview&&setupOverview.picture_engine),host=setupEngine('modelhost');setupText(el('setup-engine-choice'),'Chosen for this computer: '+(picture?picture.title+' for pictures, ':'')+(host?host.title:'llama.cpp')+' for chat.');
 setupDrawSummary()}
// setupSay writes the summary row: one sentence, its facts, the button.
function setupSay(sentence,facts,state,button){const status=el('setup-status'),install=el('setup-install');setupText(status,sentence);status.dataset.state=state||'idle';setupText(el('setup-facts'),facts||'');install.hidden=!button;if(button){setupText(install,button.label);install.disabled=!!button.disabled;install.dataset.action=button.action||''}}
const setupLoading='Checking what is installed…',setupChecking='Checking what to install…';
// setupBlocked is the one reason the button cannot run, the first of them.
function setupBlocked(ch,totals){
 if(!setupStateRead||!setupOverviewRead||!setupCopies.resolved)return {sentence:setupLoading,state:'loading'};
 if(setupStateError)return {sentence:'Setup could not read what is installed.',state:'failed',retry:true};
 if(((setupLastState||{}).components||[]).some(r=>r.effect&&r.effect.phase==='installing')&&!setupWorking)return {sentence:'Another install is running. It finishes in a moment.',state:'blocked'};
 if(!ch.adds.length&&!ch.updates.length&&!ch.removals.length)return {sentence:'Tick to install. Clear a tick to remove.',state:'idle'};
 if(setupNeedsPlan(ch)&&setupPlanKey!==setupRequestKey())return {sentence:setupChecking,state:'loading'};
 if(setupPlanError)return {sentence:'Setup could not read what to install. Try again in a moment.',state:'blocked'};
 if(setupPlanNow&&(!setupPlanNow.preparable||(setupPlanNow.blockers||[]).length))return {sentence:'Some packages are not ready yet. Clear their ticks.',state:'blocked'};
 const disk=setupPlanNow&&setupPlanNow.disk;if(disk&&Number(disk.free_bytes)>=0&&totals.disk>Number(disk.free_bytes)){const short=totals.disk-Number(disk.free_bytes),fit=setupFreeingTick(ch,short);return {sentence:'Not enough space on '+String(disk.volume||'this drive')+'. '+(fit?'Clear '+setupRowTitle(fit)+' or free '+setupBytes(short)+'.':'Clear a tick or free '+setupBytes(short)+'.'),state:'blocked'}}
 return null}
// setupFreeingTick is the one added row whose space alone covers short, the
// smallest such, so the reason names the untick that fits.
function setupFreeingTick(ch,short){let best='',bestBytes=Infinity;for(const id of ch.adds){if(setupNeededBy(id).length)continue;let bytes=0;if(id==='coding')bytes=Number((setupCodingModel()||{}).peak_bytes||0);else if(id.startsWith('model:'))bytes=Number((setupOffer(id)||{}).peak_bytes||0);else{const a=setupAddedOf(id);bytes=a?a.disk:0}if(bytes>=short&&bytes<bestBytes){best=id;bestBytes=bytes}}return best}
function setupSentence(ch){const names=ids=>ids.map(setupRowTitle);
 if(ch.removals.length&&(ch.adds.length||ch.updates.length)){const parts=[];if(ch.adds.length)parts.push('Adds '+ch.adds.length+(ch.adds.length===1?' item':' items'));if(ch.updates.length)parts.push((parts.length?'updates ':'Updates ')+ch.updates.length);parts.push('removes '+ch.removals.length);return setupJoin(parts)+'.'}
 if(ch.removals.length)return 'Removes '+setupNamed(names(ch.removals))+'.';
 if(ch.adds.length===1&&!ch.updates.length&&setupPretick(ch.adds[0])&&!setupWant.has(ch.adds[0]))return 'Adds '+setupRowTitle(ch.adds[0])+(ch.adds[0]==='coding'?', the coding model '+setupRowTitle(setupCodingDesktop())+' lacks.':', the picture model '+setupPictureTitle()+' lacks.');
 const adds=names(ch.adds.filter(id=>!setupNeededBy(id).length||setupWanted(id))),companions=ch.adds.filter(id=>setupNeededBy(id).length&&!setupWanted(id));let s='';
 if(adds.length)s='Adds '+setupNamed(companions.length&&adds.length<3?[...adds,...companions.map(setupRowTitle)]:adds);else if(companions.length)s='Adds '+setupNamed(companions.map(setupRowTitle));
 if(ch.updates.length)s+=(s?' and updates ':'Updates ')+setupNamed(names(ch.updates));return s+'.'}
function setupFacts(ch,totals){const parts=[];if(totals.download>0)parts.push(setupBytes(totals.download)+' download');if(totals.disk>0)parts.push('needs '+setupBytes(totals.disk));const disk=setupPlanNow&&setupPlanNow.disk;if(disk&&(ch.adds.length||ch.updates.length))parts.push(setupBytes(disk.free_bytes)+' free on '+String(disk.volume||'this drive'));const frees=ch.removals.reduce((n,id)=>n+setupInstalledBytes(id),0);if(frees>0)parts.push('frees '+setupBytes(frees));return parts.join(' · ')}
// setupSayStopped is the summary of an install that stopped before it
// finished: what is left, this release's sizes for it, and Update or
// Install, which continues it in one click.
function setupSayStopped(){const s=setupStopped,ids=s.ch.adds.length?s.ch.adds:s.ch.updates,names=ids.map(setupRowTitle),models=ids.some(id=>id==='coding'||id.startsWith('model:'));setupShown=null;const kept=!s.update&&s.record&&s.record.failure&&s.record.failure.sentence?s.record.failure:null,failure=kept&&(!/^Removing /.test(kept.sentence)||(s.stuck||[]).includes(kept.component))?kept:null;if(failure&&failure.detail)setupRowDetail.set(failure.component,String(failure.detail));const update=!s.ch.adds.length&&names.length,plan=s.earlier&&names.length&&!failure?setupPlanNow:null,had=new Set(((s.plan||{}).components||[]).map(c=>c.id)),ok=plan&&!(plan.blockers||[]).length&&(plan.components||[]).every(c=>had.has(c.id)),sizes=ok?setupFacts(s.ch,setupTotals()):'';s.shown=ok?setupShownPin(plan):'';setupSay(failure?failure.sentence:names.length?(update?setupNamed(names)+(names.length>1?' updates ready.':' update ready.'):'Finish installing '+setupNamed(names)+'.'):s.update?'Catalog stopped when OpenAbstractions updated.':'Catalog stopped before it finished.',names.length?[sizes,models?'Downloaded model files are kept.':''].filter(Boolean).join(' · '):(s.stuck||[]).length?'Removing '+setupNamed((s.stuck||[]).map(setupRowTitle))+' stopped before it finished.':s.record.granted||!(s.record.rules||[]).length?(s.workspace==='no_shortcut'?'The '+setupPictureTitle()+' shortcut is missing.':setupPictureTitle()+' is not in Apps yet.'):setupPictureTitle()+'\'s permissions are not saved yet.','stopped',{label:update&&!failure?'Update':plan&&!ok?'Continue':'Install',action:'continue'})}
function setupDrawSummary(){if(setupWorking)return setupSayWorking();
 // A running step is the authority: it replaces an earlier result.
 const step=setupRunningStep();if(step){setupResult=null;setupShown=null;setupSay(step.sentence,[step.facts,setupBusyApply?'Apply again when it finishes.':''].filter(Boolean).join(' '),'working',step.cancellable?{label:'Cancel',action:'cancel-step'}:null);setupWatchStep();return}
 if(setupResult)return setupSay(setupResult.sentence,'',setupResult.state,setupResult.button);if(setupStopped)return setupSayStopped();
 const ch=setupPackageChanges(),totals=setupTotals(),blocked=setupBlocked(ch,totals);
 if(blocked){setupSay(blocked.sentence,blocked.state==='loading'?'':setupFacts(ch,totals),blocked.state,blocked.retry?{label:'Try again',action:'reload'}:{label:ch.removals.length?'Apply changes':'Install',disabled:true});setupShown=null;if(!['loading','idle'].includes(blocked.state))setupNotice('blocked',blocked.sentence,setupPlanError?String(setupPlanError.message||setupPlanError):((setupPlanNow||{}).blockers||[]).join('; '));return}
 const sentence=setupSentence(ch);
 setupShown={key:setupRequestKey(),changes:ch,plan:setupPlanNow,signature:setupSignature(setupPlanNow)};
 setupSay(sentence,setupFacts(ch,totals),'ready',{label:ch.removals.length?'Apply changes':'Install',action:'install'})}

// ---- The plan behind the summary.
// setupNeedsPlan is whether the change installs packages or models: a
// removal alone needs no plan.
function setupNeedsPlan(ch){return ch.adds.length>0||ch.updates.length>0}
function setupPackageChange(ch){return [...ch.adds,...ch.updates].some(id=>setupEnginePackage(id)||id.startsWith('model:'))}
// setupRequest is the plan request the ticks draw: every ticked package the
// release offers (a tick saved on an item it does not carry is dropped),
// the picture models to prepare, the coding model and other copies.
function setupRequest(){const ch=setupPackageChanges();if(!setupNeedsPlan(ch))return null;
 const coding=ch.adds.includes('coding')?(setupCodingModel()||{}).id||'':'';
 if(!setupPackageChange(ch))return {selected:[],coding_model_plan_id:coding};
 const selected=setupEnginePackages().filter(id=>!setupDesktopIntegrationRow(id)&&setupTicked(id));
 const body={selected,coding_model_plan_id:coding,picture_model_plan_ids:setupOffers().filter(o=>!o.prepared&&setupTicked('model:'+o.id)).map(o=>o.id)};
 const workspace=setupWorkspaceApp();if(workspace&&selected.includes(workspace)&&(setupOtherCopy(workspace)||setupCopies.incomplete))body.beside=[workspace];return body}
const setupRequestKey=()=>JSON.stringify(setupRequest());
function setupSignature(plan){if(!plan)return '';return JSON.stringify({id:plan.id,added:plan.added||[],models:setupPlanModels(plan).map(m=>m.id),permissions:(plan.permissions||[]).map(p=>p.id),preparable:plan.preparable,blockers:plan.blockers||[]})}
let setupPlanBusy=false,setupPlanAgain=false;
// One plan read at a time; a tick during a read asks for one more.
async function setupRefreshPlan(){if(setupPlanBusy){setupPlanAgain=true;return}setupPlanBusy=true;try{do{setupPlanAgain=false;const request=setupRequest(),k=JSON.stringify(request);if(!request){setupPlan=null;setupPlanError=null;setupPlanKey=k;setupDraw();continue}if(k===setupPlanKey&&setupPlan&&!setupPlanError)continue;setupDraw();let plan=null,error=null;try{plan=await call('/setup/plan',request)}catch(e){error=e}if(plan&&plan.archive_looked)await setupReadOverview();if(k!==setupRequestKey()){setupPlanAgain=true;continue}setupPlan=plan;setupPlanError=error;setupPlanKey=k;setupDraw()}while(setupPlanAgain)}finally{setupPlanBusy=false}}
function setupTick(id,on){setupAfterStep=null;setupBusyApply=false;if(setupWorking)return;setupWant.set(id,on);setupResult=null;setupStopped=null;setupRowError.clear();setupDraw();return setupRefreshPlan()}

// ---- Reads.
async function setupReadState(){try{const state=await call('/setup/state');setupLastState=state;if(state.catalog)setupCatalog=state.catalog;setupStateError=false}catch(e){setupStateError=true;setupNotice('failed','Setup could not read what is installed.',String(e&&e.message||e))}setupStateRead=true}
async function setupReadOverview(){try{setupOverview=await call('/setup/overview')}catch(e){setupOverview=null;setupNotice('failed','Setup could not read its overview.',String(e&&e.message||e))}setupOverviewRead=true}
async function setupReadCopies(){try{const reply=await call('/setup/other-copies');setupCopies={resolved:true,list:reply.copies||[],incomplete:!!reply.incomplete}}catch(e){setupCopies={resolved:true,list:[],incomplete:true}}}
async function setupReadCoding(){try{setupCoding=await call('/setup/modelhost/coding')}catch(e){setupCoding=null}}
// setupReload is the first read and Try again after a failed one: every
// row stays on the page while it runs.
async function setupReload(){setupResult=null;try{setupCatalog=await call('/setup/catalog')}catch(e){}await Promise.all([setupReadCopies(),setupReadOverview(),setupReadState(),setupReadCoding()]);setupDraw();if(!setupStateError&&await setupReadStopped()){setupDraw();if(setupStopped&&setupStopped.earlier&&!setupStopped.unrecorded){await setupRefreshPlan();setupDraw()}return}await setupRefreshPlan();setupDraw()}

// ---- The button's work. The click approves the summary's plan, its
// picture models and its permissions together; every later step runs on its
// own, in order. After a restart or an update the page shows the unfinished
// install as stopped; the person's Install continues it from each step's
// journal (owner, 2026-11-09).
let setupStopped=null,setupLastRows=[],setupWork=null,setupInstallRecord=null,setupInstallModelEngine='',setupInstallBusy=false,setupInstallTimer=0,setupInstallEpoch=0,setupInstallWaiting={step:'',since:0,progress:''},setupInstalledApplication='',setupDriverRunning=false,setupPollTimer=0;
const setupInstallCancelled={};
const setupStallMs=300000;
async function setupInstallRequest(epoch,...args){if(epoch!==setupInstallEpoch)throw setupInstallCancelled;const reply=await call(...args);if(epoch!==setupInstallEpoch)throw setupInstallCancelled;return reply}
// setupActionsRunning is whether the Panel names a Setup step running now
// (setup_actions.go); a step past its deadline counts as ended.
const setupActionsRunning=()=>Object.keys((setupLastState&&setupLastState.actions)||{}).some(c=>setupComponentRunning(c));
// setupComponentRunning is whether the Panel names a step of component c
// running now. A removal no step runs (setupStuckRemovals) was left by a
// refused or lost uninstall or by an update; Install settles it.
// A removal reports its files; one whose count has not moved for
// setupStepStallMs counts as stopped, and Install cancels and settles it.
const setupStepStallMs=180000;
const setupComponentRunning=(c,state)=>{const a=(((state||setupLastState)||{}).actions||{})[c];if(!a||a.deadline&&Date.parse(a.deadline)<=Date.now())return false;return !(a.kind==='remove'&&a.progress_at&&Date.now()-Date.parse(a.progress_at)>setupStepStallMs)};
// setupRunningStep is the package step the Panel runs now, in words: what
// it does to which row, how far it is, for the summary with Cancel.
function setupRunningStep(){const actions=(setupLastState||{}).actions||{};for(const c of Object.keys(actions)){if(!setupComponentRunning(c))continue;const a=actions[c],id=setupComponentRow(c),title=c===setupRetainedPicture().component?setupRetainedPicture().removal_title:setupRowTitle(id);
 const sentence=c===setupDesktopIntegrationStep?(a.kind==='update'?'Updating the ':'Repairing the ')+setupRowTitle(setupDesktopIntegrationFacts().app)+' connection…':a.kind==='connect'?'Connecting '+title+'…':a.kind==='update'?'Updating the '+title+' integration…':(a.kind==='remove'?'Removing ':a.kind==='repair'?'Repairing ':'Installing ')+title+'…',facts=a.total?setupCount(a)+' files checked.':'';return {component:c,sentence,facts,cancellable:!!a.cancellable}}return null}
const setupCount=a=>Number(a.done||0).toLocaleString()+' of '+Number(a.total).toLocaleString();
// setupWatchStep rereads the state every three seconds while a step runs.
// When it ends, the work that waited for it (setupAfterStep) continues, or
// the page's rows reload.
// A step that failed ends the work that waited for it and shows as the
// result with Try again; a cancelled one reloads the rows.
let setupStepTimer=0,setupAfterStep=null,setupBusyApply=false;function setupWatchStep(){if(setupStepTimer)return;const watched=Object.entries((setupLastState||{}).actions||{}).map(([c,a])=>[c,(a||{}).step_id]);setupStepTimer=setTimeout(async()=>{setupStepTimer=0;try{setupLastState=await call('/setup/state')}catch(e){}if(setupRunningStep()){setupDraw();setupWatchStep();return}const next=setupAfterStep;setupAfterStep=null;
 for(const [c,id] of watched){const o=setupStepEnded(c,null,id);if(o&&o.failure){
  if(c===setupDesktopIntegrationStep)return setupDesktopIntegrationFailed(o);
  if(o.kind==='repair')return setupRepairFailed(c,o);
  if(o.kind==='connect'||o.kind==='update')return setupConnectionFailed(setupComponentRow(c),o);
  return setupStepFailed(c,o)}if(o&&o.cancelled)return setupReload()}if(next)next();else setupReload()},3000)}
// setupStepEnded is how component c's step ended (setup_actions.go): the
// outcome of stepID, or with no id one other than prior, the outcome the
// page read before its request.
const setupStepEnded=(c,state,stepID,prior)=>{const o=(((state||setupLastState)||{}).outcomes||{})[c];if(!o)return null;if(stepID)return o.step_id===stepID?o:null;return o.step_id!==prior?o:null};
const setupPriorStep=(state,c)=>(((state||{}).outcomes||{})[c]||{}).step_id;
const setupOutcomeError=o=>o&&(o.failure?Error(o.failure):o.cancelled?Error('the removal was cancelled'):null);
const setupEngineComponentRow=c=>setupEnginePackages().find(id=>setupChoiceComponents(id).includes(c));
const setupComponentRow=c=>c===setupRetainedPicture().component?setupRetainedPicture().id:c===setupPictureStep()?setupPictureApp():setupEngineComponentRow(c)||(((setupCatalog||{}).choices||[]).find(ch=>(ch.components||[]).includes(c))||{}).id||c;
function setupStepFailed(c,o){const id=setupComponentRow(c);setupWork={rows:[id],removals:[id],opencode:false,record:null};setupFailed(id,'Setup could not remove it.',o.failure)}
// setupOutcomeResult shows a failed removal step of a row still removing as
// the result with Try again.
function setupOutcomeResult(state,stuck){for(const id of stuck)for(const c of setupChoiceComponents(id)){const o=((state||{}).outcomes||{})[c];if(o&&o.failure){const reason=setupReason(o.failure);setupResult={sentence:setupRowTitle(id)+' was not removed.'+(reason?' '+reason:''),state:'failed',button:{label:'Try again',action:'retry'}};return}}}
async function setupCancelStep(){const step=setupRunningStep();if(!step)return setupReload();el('setup-install').disabled=true;try{await call('/setup/cancel-step',{component_id:step.component})}catch(e){setupNotice('failed','The step could not be cancelled.',String(e&&e.message||e))}setupWatchStep()}
function setupStuckRemovals(state){const retained=setupRetainedPicture().component;if(!retained)return [];return ((state&&state.components)||[]).filter(r=>r.id!==retained&&r.effect&&r.effect.removing&&!setupComponentRunning(r.id,state))}
async function setupSettleRemovals(){let state=await call('/setup/state');for(const row of setupStuckRemovals(state)){
 // A stalled removal step is cancelled first; it ends within moments.
 if((state.actions||{})[row.id]){await call('/setup/cancel-step',{component_id:row.id}).catch(()=>{});state=await call('/setup/state');for(let i=0;i<20&&(state.actions||{})[row.id];i++){await new Promise(r=>setTimeout(r,500));state=await call('/setup/state')}}
 // Settling a removal that already deleted files continues it as its own
 // step; the page shows that step, also when the request itself failed.
 let failed=null,reply=null;const prior=setupPriorStep(state,row.id);try{reply=await call('/setup/settle-removal',{component_id:row.id,plan_id:state.plan.id})}catch(e){failed=e}
 const now=await call('/setup/state');if(setupComponentRunning(row.id,now)){setupLastState=now;return false}const ended=setupStepEnded(row.id,now,reply&&reply.step_id,prior),err=setupOutcomeError(ended);if(err)throw err;if(!ended&&failed)throw failed}setupLastState=await call('/setup/state');return true}
function setupSayWorking(){const w=setupWork||{rows:[],removals:[]},rows=w.rows.filter(id=>!w.removals.includes(id));if(!rows.length){setupSay('Removing '+setupNamed(w.removals.map(setupRowTitle))+'.','','working',{label:'Cancel',action:'cancel'});return}const done=rows.filter(id=>{const live=setupLive(id);return live&&live.text==='Installed'}).length;setupSay('Installing '+Math.min(rows.length,done+1)+' of '+rows.length+'. Keep this page open.','','working',{label:'Cancel',action:'cancel'})}
function setupBeginWork(ch,record){setupWorking=true;setupResult=null;setupRowError.clear();const rows=[...ch.adds,...ch.updates,...ch.removals];setupWork={rows,removals:ch.removals.slice(),updates:ch.updates.slice(),opencode:false,record:record||null};setupLastRows=rows.slice();setupDraw()}
function setupEndWork(){setupWorking=false;setupWork=null;if(setupInstallTimer)clearTimeout(setupInstallTimer);setupInstallTimer=0;setupInstallWaiting={step:'',since:0,progress:''}}
// setupFailed ends the work on the row that stopped: the summary names it,
// the row says why in one sentence, the raw error goes to the log and to
// the row's Technical details. A refusal written as a plain sentence by
// the service is its reason in the summary.
function setupReason(detail){const t=String(detail||'').replace(/^(step [a-z]+|continue)(?: showed no change)?: /,'').split(/; |\n/)[0].trim().replace(/\.$/,'');if(!t||/^downloads are unavailable/i.test(t)||t.length>160||/[:{}\\\/<>=]|https?|\b[0-9]{3}\b/.test(t))return '';return t[0].toUpperCase()+t.slice(1)+'.'}
const setupRowDetail=new Map(),setupRowFault=id=>setupRowError.has(id)&&setupRowDetail.has(id)?'Error: '+setupRowDetail.get(id):'';
function setupFailed(id,rowSentence,detail,kind){const title=setupRowTitle(id),removing=setupWork&&setupWork.removals.includes(id),reason=kind==='stalled'?'':setupReason(detail),keepPlan=setupInstallRecord&&setupInstallRecord.plan_id&&!String(setupInstallRecord.plan_id).startsWith('coding-model-')?setupInstallRecord.plan_id:'',keep=()=>{if(keepPlan)Promise.resolve().then(()=>call('/setup/install/failed',{setup_plan_id:keepPlan,component:String(id),sentence:setupResult.sentence,detail:String(detail||'')})).catch(()=>{})};setupEndWork();if(detail)setupRowDetail.set(id,String(detail));if(reason){setupResult={sentence:title+(removing?' was not removed. ':' did not install. ')+reason,state:'failed',button:{label:'Try again',action:'retry'}};setupRowError.set(id,rowSentence);keep();setupNotice(kind||'failed',setupResult.sentence,detail);setupDraw();setupHoldTryAgain();return}if(/^downloads are unavailable/i.test(String(detail||'').replace(/^step [a-z]+: /,''))){rowSentence='Downloads are unavailable.';setupResult={sentence:'Downloads are unavailable. Check the internet connection.',state:'failed',button:{label:'Try again',action:'retry'}}}else{const others=((setupWork&&setupWork.rows)||setupLastRows).filter(other=>other!==id&&!(setupWork&&setupWork.removals.includes(other))&&!setupInstalled(other)&&!(setupLive(other)||{}).text?.startsWith('Installed')).map(setupRowTitle);setupResult={sentence:title+(removing?' was not removed':' did not install')+(others.length?'; '+setupNamed(others)+(others.length>1?' were':' was')+' not installed either.':'.'),state:'failed',button:{label:'Try again',action:'retry'}}}setupRowError.set(id,rowSentence);keep();setupNotice(kind||'failed',setupResult.sentence,detail);setupDraw();setupHoldTryAgain()}
async function setupHoldTryAgain(){try{setupLastState=await call('/setup/state')}catch(e){}const button=el('setup-install');if(setupStopped||setupResult&&setupResult.button&&setupResult.button.action==='retry'){button.disabled=setupActionsRunning();if(button.disabled)setTimeout(setupHoldTryAgain,3000)}}
// setupSucceeded says pictures work only when the setup step reads Ready
// with its picture part running (/setup/step); otherwise ComfyUI reads as
// installed, with the step's own Try pictures again.
async function setupPicturesWork(){try{const v=await call('/setup/step');if(!v||!v.ready)return 'unknown';return v.action==='Try pictures again'?'no':'yes'}catch(e){return 'unknown'}}
async function setupSucceeded(){const w=setupWork||{rows:[]},pictures=w.rows.some(id=>(id===setupWorkspaceApp()||id.startsWith('model:'))&&!w.removals.includes(id)),application=setupInstalledApplication;setupEndWork();setupWant.clear();setupResult={sentence:'Changes applied.',state:'done'};if(pictures&&application){const p=await setupPicturesWork(),title=setupPictureTitle();setupResult=p==='yes'?{sentence:title+' is ready.',state:'done',button:{label:'Open '+title,action:'open'}}:p==='no'?{sentence:title+' is installed, but its test picture didn\'t finish.',state:'done',button:{label:'Try again',action:'pictures'}}:{sentence:title+' is installed.',state:'done',button:{label:'Open '+title,action:'open'}}}setupResult.application=application;return setupRefreshAll()}
async function setupTryPictures(){const result=setupResult||{},button=el('setup-install');button.disabled=true;try{const r=await call('/setup/step',{});setupResult={sentence:String(r.message||'Setup tries pictures again.'),state:'done',application:result.application}}catch(e){setupResult={sentence:oaPlain(e&&e.message||e).line,state:'failed',button:{label:'Try again',action:'pictures'},application:result.application};setupNotice('failed','Setup could not try pictures again.',String(e&&e.message||e))}setupDraw()}
async function setupRefreshAll(){await Promise.all([setupReadState(),setupReadOverview(),setupReadCoding()]);setupPlan=null;setupPlanKey='';setupDraw();await setupRefreshPlan()}
async function setupRenderStateReply(state){if(state&&state.components){setupLastState=state;if(state.catalog)setupCatalog=state.catalog}setupDraw()}
async function setupDriveApproved(state,strict=false){if(setupDriverRunning||!state.current_catalog||!state.approved||!state.plan||!state.plan.preparable)return;setupDriverRunning=true;try{let current=state;for(;;){const before=current;current=await call('/setup/advance-next',{plan_id:before.plan.id});setupRenderStateReply(current);const componentID=current.advanced;if(!componentID)break;const rowOf=s=>JSON.stringify((s.components||[]).find(row=>row.id===componentID));if(rowOf(current)===rowOf(before))break}}catch(e){if(strict)throw e}finally{setupDriverRunning=false}}
// setupWatching runs work while rereading the state and the model every
// three seconds, so each row's progress moves during a long step.
async function setupWatching(work,modelURL){let watching=true;const watch=async()=>{if(!watching)return;try{const state=await call('/setup/state');if(!watching)return;setupLastState=state;if(modelURL)setupModelState=await call(modelURL);if(watching)setupDraw()}catch(e){}if(watching)setTimeout(watch,3000)};setTimeout(watch,3000);try{return await work()}finally{watching=false}}
function setupProgress(){const rows=((setupLastState&&setupLastState.components)||[]).map(row=>[row.id,(row.effect||{}).phase,row.transfer&&row.transfer.done,row.transfer&&row.transfer.state]);const models=JSON.stringify(setupModelState&&[setupModelState.status,setupModelState.choice_status,setupModelState.progress]);const coding=JSON.stringify(setupCoding&&setupCoding.status);return JSON.stringify([rows,models,coding])}
// setupInstallSteps are the steps a record approves, in order.
function setupInstallSteps(record,plan){const selected=(plan&&plan.selected)||[],steps=[];if(record.opencode)steps.push(setupLegacyDesktop);if(record.coding_model_plan_id)steps.push('coding');if(selected.some(id=>id===setupWorkspaceApp()||id==='lemonade'||id==='thenoise'||id==='pytorch'))steps.push('feature');if(selected.length)steps.push('apps');if(record.model_plan_id||(record.picture_model_plan_ids||[]).length)steps.push('model');if((record.rules||[]).length)steps.push('permissions');if(setupWorkspaceApp()&&selected.includes(setupWorkspaceApp()))steps.push('workspace');return steps}
// setupStepRow is the row a failed step names.
function setupStepRow(step,state){if(step===setupLegacyDesktop)return setupLegacyDesktop;if(step==='coding')return 'coding';if(step==='model'){const record=setupInstallRecord||{},ids=[record.model_plan_id,...(record.picture_model_plan_ids||[])].filter(Boolean);const open=ids.find(id=>{const s=setupModelStatus(id);return !(s&&s.provider_configured)});return 'model:'+(open||ids[0]||'')}if(step==='permissions'||step==='workspace')return setupWorkspaceApp();if(step==='feature'){const engine=setupOverview&&setupOverview.picture_engine;return engine||setupPictureApp()}const rows=(state&&state.components)||(setupLastState&&setupLastState.components)||[],installed=r=>!!(r&&r.effect&&r.effect.phase==='installed'),plan=((state&&state.plan)||(setupLastState&&setupLastState.plan)||{}).components||[],deps=id=>((plan.find(c=>c.id===id)||{}).dependencies)||[],
  // With nothing failed, the stuck component is the first one whose
  // dependencies are installed: the one the install was attempting.
  stuck=rows.find(r=>r.effect&&['failed','uncertain'].includes(r.effect.phase))||rows.find(r=>!installed(r)&&deps(r.id).every(d=>{const x=rows.find(y=>y.id===d);return !x||installed(x)}))||rows.find(r=>!installed(r));return (stuck&&setupEngineComponentRow(stuck.id))||(setupWork&&setupWork.rows[0])||setupPictureApp()}
// setupStallSentence is a stalled step's row sentence: a model whose files
// are all verified stopped while being prepared, any other model or the
// coding model stopped downloading.
function setupStallSentence(step){if(step==='model'){const id=setupStepRow('model').slice(6),s=setupModelStatus(id),assets=(s&&s.assets)||[];if(assets.length&&assets.every(a=>a.phase==='verified'))return 'Preparing it stopped making progress.';return 'The download stopped making progress.'}return step==='coding'?'The download stopped making progress.':setupStepSentences.stalled}
const setupStepSentences={[setupLegacyDesktop]:'The OpenAbstractions installer did not open.',coding:'The download stopped.',feature:'Install the latest OpenAbstractions to add its picture support.',apps:'Installing stopped.',model:'The download stopped.',permissions:'Its permissions were not saved.',workspace:'It was not added to Apps.',stalled:'This stopped making progress.',start:'Setup could not start installing it.'};
async function setupInstallStep(step,record,state,epoch){const planID=record.plan_id;
 switch(step){
// Only a previously saved install can reach the maintenance step for that release.
case setupLegacyDesktop:{if(!record.opencode)throw Error('The stopped install did not include OpenCode.');setupWork.opencode=true;setupDraw();const review=await setupInstallRequest(epoch,'/setup/maintenance');if(!review.available)throw Error('the OpenAbstractions installer is unavailable');await setupInstallRequest(epoch,'/setup/maintenance',{plan_id:review.plan_id,confirm:true});return 'done'}
case 'coding':{let reply=await setupInstallRequest(epoch,'/setup/modelhost/coding');const model=reply.status&&reply.status.plan;if(!model||model.id!==record.coding_model_plan_id)throw Error('The approved coding model changed.');const approval={plan_id:model.id,download_bytes:model.download_bytes,peak_bytes:model.peak_bytes,licenses:model.licenses};if(!reply.status.approved)reply=await setupInstallRequest(epoch,'/setup/modelhost/coding',{action:'start',setup_plan_id:planID,confirm:true,approval});setupCoding=reply;const asset=(reply.status.assets||[])[0]||{},t=asset.transfer||{};if(asset.phase==='verified')return 'done';if(asset.phase==='failed'||t.state==='failed'||t.state==='cancelled')throw Error('coding download: '+String(t.failure||asset.reason||t.state||asset.phase));if(t.state==='complete'){setupCoding=await setupInstallRequest(epoch,'/setup/modelhost/coding',{action:'check',confirm:true,approval});const checked=((setupCoding.status||{}).assets||[])[0]||{};if(checked.phase==='verified')return 'done'}return 'waiting'}
case 'feature':{const selected=(state.plan&&state.plan.selected)||[],engines=['thenoise','pytorch'].filter(e=>selected.includes(e));if(setupInstallModelEngine&&setupInstallModelEngine!=='lemonade'&&!engines.includes(setupInstallModelEngine))engines.push(setupInstallModelEngine);for(const engine of engines){const feature=await setupInstallRequest(epoch,'/setup/feature?engine='+engine);if(feature.installed)continue;if(!feature.available)throw Error(feature.reason||'the engine feature cannot be added on this computer');const added=await setupInstallRequest(epoch,'/setup/feature/add',{plan_id:planID,confirm:true,engine});if(!added.installed)throw Error(added.reason||'the engine feature was not confirmed')}return 'done'}
case 'apps':{let current=await setupInstallRequest(epoch,'/setup/state');setupRenderStateReply(current);await setupWatching(()=>setupDriveApproved(current,true));current=await setupInstallRequest(epoch,'/setup/state');setupRenderStateReply(current);const rows=current.components||[];const stuck=rows.find(row=>row.effect&&['failed','uncertain'].includes(row.effect.phase));if(stuck)throw Error('package '+stuck.id+' stopped: '+String(stuck.effect.reason||stuck.effect.phase));if(rows.length&&rows.every(row=>row.effect&&(row.effect.phase==='installed'||row.effect.phase==='removed'||row.effect.removing)))return 'done';return 'waiting'}
case 'model':{const engine=setupInstallModelEngine||'lemonade',url=engine==='lemonade'?'/setup/model':'/setup/model?engine='+engine,bound=engine==='lemonade'?{}:{engine},targets=[record.model_plan_id,...(record.picture_model_plan_ids||[])].filter(Boolean);
 let reply=await setupInstallRequest(epoch,url);setupModelState=reply;const st=id=>(reply.plan&&reply.plan.id===id?reply.status:(reply.choice_status||{})[id])||{};let waiting=false;
 for(const id of targets){let s=st(id);if(s.provider_configured){if(reply.default_pending&&reply.plan&&reply.plan.id===id)reply=setupModelState=await setupInstallRequest(epoch,'/setup/model/reconcile',{plan_id:id,setup_plan_id:planID,...bound});continue}
  if(!s.approved){reply=setupModelState=await setupInstallRequest(epoch,'/setup/model/start',{plan_id:id,confirm:true,setup_plan_id:planID,...bound});s=st(id)}
  for(const a of s.assets||[]){if(a.phase==='pending'||(a.phase==='requested'&&a.transfer&&a.transfer.state==='complete')){reply=setupModelState=await setupWatching(()=>setupInstallRequest(epoch,'/setup/model/advance',{plan_id:id,asset_id:a.id,setup_plan_id:planID,...bound}),url);setupDraw()}}
  s=st(id);const failed=(s.assets||[]).find(a=>a.phase==='failed');if(failed)throw Error('picture model file '+String(failed.id)+': '+String(failed.reason||'failed'));
  if((s.assets||[]).length&&s.assets.every(a=>a.phase==='verified')&&!s.provider_configured){reply=setupModelState=await setupWatching(()=>setupInstallRequest(epoch,'/setup/model/configure',{plan_id:id,setup_plan_id:planID,...bound}),url);s=st(id)}
  if(!s.provider_configured)waiting=true}
 setupModelState=reply;setupDraw();return waiting?'waiting':'done'}
case 'permissions':{if(record.granted)return 'done';setupInstallRecord=await setupInstallRequest(epoch,'/setup/install/grants',{setup_plan_id:planID});return 'done'}
case 'workspace':{const status=setupAdapterPath(setupWorkspace.status),register=setupAdapterPath(setupWorkspace.register);if(!status||!register)throw Error('The picture workspace is unavailable.');let app=await setupInstallRequest(epoch,status);if(app.conflict)throw Error(app.reason||'A different application entry is registered.');if(!app.registered||app.shortcut===false)app=await setupInstallRequest(epoch,register,{setup_plan_id:planID,confirm:true});if(app.registered&&app.matching&&app.application){setupInstalledApplication=app.application;return 'done'}return 'waiting'}}
return 'done'}
async function setupInstallDrive(){if(setupInstallBusy||!setupInstallRecord)return;setupInstallBusy=true;const record=setupInstallRecord,epoch=setupInstallEpoch;let failedStep='start',state=null;try{state=await setupInstallRequest(epoch,'/setup/install');setupRenderStateReply(state);const steps=setupInstallSteps(record,state.plan);for(const step of steps){failedStep=step;const result=await setupInstallStep(step,setupInstallRecord||record,state,epoch);if(epoch!==setupInstallEpoch)return;setupDraw();if(result!=='done'){try{setupLastState=await setupInstallRequest(epoch,'/setup/state')}catch(e){if(e===setupInstallCancelled)throw e}const progress=setupProgress();if(setupInstallWaiting.step!==step||setupInstallWaiting.progress!==progress||setupActionsRunning())setupInstallWaiting={step,since:Date.now(),progress};if(Date.now()-setupInstallWaiting.since>=setupStallMs){setupFailed(setupStepRow(step,setupLastState),setupStallSentence(step),'step '+step+' showed no change: '+progress,'stalled');return}setupDraw();if(!setupInstallTimer)setupInstallTimer=setTimeout(()=>{setupInstallTimer=0;setupInstallDrive()},3000);return}}
 await setupSucceeded()}catch(e){if(e===setupInstallCancelled||epoch!==setupInstallEpoch)return;setupFailed(setupStepRow(failedStep,setupLastState),setupStepSentences[failedStep]||setupStepSentences.apps,'step '+failedStep+': '+String(e&&e.message||e))}finally{setupInstallBusy=false}}
// setupReadStopped finds an approved, uncancelled install that has not
// finished, approved under this release's catalogue or an earlier one. Opening the page starts nothing (owner, 2026-11-09): its rows
// arrive ticked and read Stopped, and the summary offers Install, which
// continues it (setupContinue).
async function setupReadStopped(){setupStopped=null;if(!setupCopies.resolved)return false;try{const reply=await call('/setup/install'),earlier=!reply.current_catalog;if(!reply.install||!reply.install.plan_id||reply.install.cancelled||(earlier?!(reply.plan&&reply.plan.id===reply.install.plan_id):!reply.approved))return setupReadUnrecorded(reply,earlier);const record=reply.install,selected=(reply.plan&&reply.plan.selected)||[];
 for(const id of selected)if(setupOffered(id))setupWant.set(id,true);for(const id of [record.model_plan_id,...(record.picture_model_plan_ids||[])].filter(Boolean))setupWant.set('model:'+id,true);if(record.coding_model_plan_id)setupWant.set('coding',true);
 setupLastState=reply;const ch=setupPackageChanges(),own=new Set([...selected,...[record.model_plan_id,...(record.picture_model_plan_ids||[])].filter(Boolean).map(id=>'model:'+id),...(record.coding_model_plan_id?['coding']:[])]);
 ch.adds=ch.adds.filter(id=>own.has(id));ch.updates=ch.updates.filter(id=>own.has(id));ch.removals=[];
 // A finished install leaves its record: nothing is left to do when every
 // row it approved is installed and its permissions were written.
 // A ComfyUI that is installed but not in Apps or the Start menu
 // (reply.workspace) leaves the install unfinished: Install adds it. Another
 // program's entry under its name stays; the finished install says so once.
 const workspace=setupWorkspaceApp()&&selected.includes(setupWorkspaceApp())&&['unregistered','stale','no_shortcut'].includes(reply.workspace)?reply.workspace:'';
 const stuck=setupStuckRemovals(reply).map(r=>setupEngineComponentRow(r.id)||r.id);
 if(!ch.adds.length&&!ch.updates.length&&(record.granted||!(record.rules||[]).length)&&!workspace&&!stuck.length){setupWant.clear();if(setupWorkspaceApp()&&selected.includes(setupWorkspaceApp())&&reply.workspace==='other'){const note=reply.workspace_note||'A picture workspace entry for another program is already registered; it was preserved.';setupResult={sentence:note,state:'idle'};setupNotice('blocked',note,'')}return false}
 setupOutcomeResult(reply,stuck);setupStopped={record,ch,update:record.interrupted==='update',engine:reply.model_engine||'',earlier,plan:reply.plan||null,workspace,stuck};return true}catch(e){return false}}
// setupReadUnrecorded reads a plan with no install record of its own, as a
// removal approves one: a removal no step runs, or an installed ComfyUI not
// in Apps or the Start menu, leaves it stopped; Install settles the removal
// and adds ComfyUI to Apps under the approved plan (R20b, 2026-11-10).
function setupReadUnrecorded(reply,earlier){const plan=reply.plan||{},selected=plan.selected||[];if(!plan.id)return false;
 const workspace=setupWorkspaceApp()&&selected.includes(setupWorkspaceApp())&&['unregistered','stale','no_shortcut'].includes(reply.workspace)?reply.workspace:'';
 const stuck=setupStuckRemovals(reply).map(r=>setupEngineComponentRow(r.id)||r.id);
 if(!stuck.length&&!workspace)return false;setupLastState=reply;setupOutcomeResult(reply,stuck);
 setupStopped={record:{plan_id:plan.id},ch:{adds:[],updates:[],removals:[]},update:false,engine:reply.model_engine||'',earlier,plan,workspace,stuck,unrecorded:true};return true}
// Try again resumes from the recorded phases: a failed download or an
// uncertain install is retried, an interrupted one recovered; then the steps
// continue.
// setupRecoverRows resumes each package from its recorded phase: a failed
// download or an uncertain install is retried, an interrupted one
// recovered. False when downloads are unavailable; the row then says so.
async function setupRecoverRows(){if(!await setupSettleRemovals()){setupAfterStep=setupTryAgain;setupDraw();return false}const state=await call('/setup/state');for(const row of state.components||[]){const effect=row.effect||{},phase=effect.phase;const earlier=!state.current_catalog&&['requested','installing','uncertain','staged','checked'].includes(phase);const path=earlier?'/setup/recover':phase==='failed'||phase==='uncertain'||(row.id==='pytorch-engine'&&phase==='staged'&&effect.component_root_key)?'/setup/retry':phase==='installing'?'/setup/recover':'';if(!path||!state.plan||!(state.approved||earlier))continue;try{await call(path,{component_id:row.id,plan_id:state.plan.id})}catch(e){const raw=String(e&&e.message||e);if(/^downloads are unavailable/i.test(raw)){setupFailed(setupStepRow('apps',state),'Downloads are unavailable.',raw);return false}setupNotice('failed','Try again could not resume a package.',raw)}}return true}
async function setupTryAgain(){const button=el('setup-install');try{setupLastState=await call('/setup/state')}catch(e){}if(setupActionsRunning()){button.disabled=true;return setupHoldTryAgain()}const ch={adds:setupLastRows.slice(),updates:[],removals:[]};setupResult=null;setupRowError.clear();try{if(!await setupRecoverRows())return;if(!setupInstallRecord){const reply=await call('/setup/install');if(reply.install&&reply.install.plan_id&&!reply.install.cancelled)setupInstallRecord=reply.install}}catch(e){setupFailed(setupStepRow('apps'),setupStepSentences.apps,'Try again: '+String(e&&e.message||e));return}if(!setupInstallRecord){setupDraw();return setupRefreshPlan()}setupBeginWork(ch,setupInstallRecord);return setupInstallDrive()}
// setupContinue is Install on a stopped install: the same approval
// continues from each step's journal. A verified file stays verified and
// is not downloaded again.
async function setupContinue(){const stopped=setupStopped,button=el('setup-install');if(!stopped)return;button.disabled=true;try{setupLastState=await call('/setup/state')}catch(e){}if(setupActionsRunning())return setupHoldTryAgain();if((stopped.stuck||[]).length){try{if(!await setupSettleRemovals()){setupAfterStep=setupContinue;setupDraw();return}}catch(e){setupStopped=null;setupFailed(stopped.stuck[0],'Its removal could not be settled.','settle removal: '+String(e&&e.message||e));return}}if(stopped.unrecorded&&!stopped.workspace){setupStopped=null;return setupReload()}if(stopped.workspace&&!stopped.ch.adds.length&&!stopped.ch.updates.length&&(stopped.record.granted||!(stopped.record.rules||[]).length))return setupContinueWorkspace(stopped);if(stopped.earlier)return setupContinueEarlier(stopped);
 setupLastRows=[...stopped.ch.adds,...stopped.ch.updates];
 try{const reply=await call('/setup/install/continue',{setup_plan_id:stopped.record.plan_id});setupStopped=null;setupInstallRecord=reply.install;setupInstallModelEngine=reply.model_engine||stopped.engine;setupRenderStateReply(reply);if(!await setupRecoverRows())return}
 catch(e){setupStopped=null;setupFailed(setupLastRows[0]||setupPictureApp(),setupStepSentences.start,'continue: '+String(e&&e.message||e));return}
 setupBeginWork(stopped.ch,setupInstallRecord);return setupInstallDrive()}
// setupContinueWorkspace is Install on an install whose packages,
// models and permissions are done and whose ComfyUI is not in Apps or the
// Start menu: the workspace step alone runs, under the approval the
// installed packages already have.
async function setupContinueWorkspace(stopped){setupStopped=null;setupInstallRecord=stopped.record;const epoch=setupInstallEpoch,ch={adds:[],updates:[setupWorkspaceApp()],removals:[]};setupBeginWork(ch,stopped.record);setupWork.updates=[];
 try{const step=await setupInstallStep('workspace',stopped.record,null,epoch);if(epoch!==setupInstallEpoch)return;if(step!=='done')throw Error('the workspace step is still waiting');await setupSucceeded()}
 catch(e){if(e===setupInstallCancelled||epoch!==setupInstallEpoch)return;setupFailed(setupWorkspaceApp(),setupStepSentences.workspace,'step workspace: '+String(e&&e.message||e))}}
// setupContinueEarlier is Install on an install the earlier release
// approved: its unfinished rows are settled (/setup/recover), then the same
// ticks are planned against this release. A plan the earlier approval
// covers installs at once; one that adds a package, a model or a
// permission shows its ordinary summary for the person's Install. The
// ticks stay as they were; verified files are reused.
async function setupContinueEarlier(stopped){setupLastRows=[...stopped.ch.adds,...stopped.ch.updates];
 try{if(!await setupRecoverRows())return;await setupReadState()}catch(e){setupStopped=null;setupFailed(setupLastRows[0]||setupPictureApp(),setupStepSentences.start,'continue: '+String(e&&e.message||e));return}
 // The stopped install's own ticks stay exactly; nothing is preticked.
 for(const id of setupRowIDs())setupWant.set(id,setupWanted(id));
 setupStopped=null;setupPlanKey='';await setupRefreshPlan();if(setupShown&&(setupCovered(stopped,setupShown.plan)||stopped.shown&&setupShownPin(setupShown.plan)===stopped.shown))return setupInstall()}
// setupShownPin is the plan a stopped banner showed: its summary and every
// package's pin, so one click approves exactly what the banner sized.
const setupShownPin=plan=>plan?setupSignature(plan)+JSON.stringify((plan.components||[]).map(c=>[c.id,c.artifact||null,c.dependency_versions||null])):'';
// setupCovered is whether the earlier approval covers plan: every package
// at the exact pin the earlier plan approved (a new version brings its own
// size, licence, publisher and program), and no model or permission it did
// not name. A missing plan is covered by nothing.
function setupCovered(stopped,plan){if(!plan)return false;const r=stopped.record,pin=c=>JSON.stringify([c.artifact||null,c.dependency_versions||null]),had=new Map(((stopped.plan||{}).components||[]).map(c=>[c.id,pin(c)])),models=new Set([r.model_plan_id,...(r.picture_model_plan_ids||[]),r.coding_model_plan_id].filter(Boolean));
 return (plan.components||[]).every(c=>had.get(c.id)===pin(c))&&setupPlanModels(plan).every(m=>models.has(m.id))&&(((plan.permissions||[])[0]||{}).id||'')===(r.rules_digest||'')}
// Cancel stops the work after its current step and records it, so the page
// shows nothing to continue on its next opening.
async function setupCancel(){const record=setupInstallRecord;setupInstallEpoch++;setupInstallRecord=null;setupEndWork();try{if(record&&record.plan_id&&!String(record.plan_id).startsWith('coding-model-'))await call('/setup/install/cancel',{setup_plan_id:record.plan_id})}catch(e){setupNotice('failed','Setup could not record the cancelled install.',String(e&&e.message||e))}await setupRefreshAll()}
// setupCarry saves an earlier release's plan as this release's, with the
// rows to remove still in it, so they can be removed (/setup/intent).
async function setupCarry(epoch,removals){const selected=setupEnginePackages().filter(id=>setupTicked(id)||removals.includes(id));const body={selected};const plan=await setupInstallRequest(epoch,'/setup/plan',body);return setupInstallRequest(epoch,'/setup/intent',{...body,plan_id:plan.id,confirm:true})}
// setupBusy reads a 409 refusal that names another running Setup step.
const setupBusy=e=>e&&e.busy||null;
const setupBusyTries=5,setupBusyWaitMs=1000;
async function setupRemove(epoch,id){const state=await setupInstallRequest(epoch,'/setup/state');if(!state.plan||!state.approved)throw Error('no approved plan to remove from');if(id===setupPictureApp()){for(const c of setupChoiceComponents(id)){
 // The removal runs as its own step; the page shows it while it runs, also
 // when the request itself failed (WebView2 drops long requests).
 // A step that ended before the page read the state is read from its
 // outcome; only a success goes on.
 let failed=null,reply=null,now=null;const prior=setupPriorStep(state,c);
 // A busy refusal with no step listed is tried again in place.
 for(let tries=1;;tries++){failed=null;try{reply=await setupInstallRequest(epoch,'/setup/uninstall',{component_id:c,plan_id:state.plan.id,confirm:true})}catch(e){if(e===setupInstallCancelled)throw e;failed=e}
  now=await setupInstallRequest(epoch,'/setup/state');if(setupComponentRunning(c,now)){setupLastState=now;return 'running'}if(!(failed&&setupBusy(failed)))break;if(Object.keys(now.actions||{}).some(id=>setupComponentRunning(id,now))){setupLastState=now;return 'busy'}if(tries>=setupBusyTries)throw Error('Another Catalog step is running.');await new Promise(go=>setTimeout(go,setupBusyWaitMs))}const ended=setupStepEnded(c,now,reply&&reply.step_id,prior),err=setupOutcomeError(ended);if(err)throw err;if(!ended&&failed)throw failed}}else await setupInstallRequest(epoch,'/setup/engine/remove',{engine:id,plan_id:state.plan.id,confirm:true})}
// setupInstall is the button's click: it approves exactly the summary it
// drew, or redraws a summary whose plan changed since.
async function setupInstall(){setupBusyApply=false;const shown=setupShown;if(!shown||shown.key!==setupRequestKey())return setupRefreshPlan();el('setup-install').disabled=true;const request=setupRequest();let fresh=null;if(request){try{fresh=await call('/setup/plan',request)}catch(e){setupPlanError=e;setupDraw();return}if(setupSignature(fresh)!==shown.signature){setupPlan=fresh;setupPlanKey=JSON.stringify(request);setupDraw();return}}
 const ch=shown.changes,epoch=setupInstallEpoch,record={};setupBeginWork(ch,null);let failedRow=ch.removals[0]||ch.adds[0]||ch.updates[0];
 const go=async from=>{try{if(ch.removals.length){if(!from&&setupMode()!=='current')await setupCarry(epoch,ch.removals);for(let i=from;i<ch.removals.length;i++){const id=ch.removals[i];failedRow=id;const removed=await setupRemove(epoch,id);if(removed==='running'||removed==='busy'){setupEndWork();setupResult=null;
  // The rest of this click continues when the step ends well; a refused
  // removal is tried again then.
  setupAfterStep=()=>{if(epoch!==setupInstallEpoch)return;setupBeginWork(ch,null);return go(removed==='busy'?i:i+1)};setupDraw();return}await setupReadState();setupDraw()}}
  if(fresh){failedRow=setupWork.rows.find(id=>!ch.removals.includes(id))||failedRow;const reply=await setupInstallRequest(epoch,'/setup/install',{...request,plan_id:fresh.id,model_plan_id:fresh.picture_model?fresh.picture_model.id:'',permissions_id:((fresh.permissions||[])[0]||{}).id||'',confirm:true});Object.assign(record,reply.install);setupInstallModelEngine=reply.model_engine||'';setupRenderStateReply(reply);
   for(const row of reply.components||[])if(row.effect&&row.effect.phase==='removed')await setupInstallRequest(epoch,'/setup/reinstall',{component_id:row.id,plan_id:reply.plan.id,confirm:true})}
  if(!record.plan_id){await setupSucceeded();return}
  setupInstallRecord=record;setupWork.record=record;await setupInstallDrive()}
 catch(e){if(e===setupInstallCancelled||epoch!==setupInstallEpoch)return;const busy=setupBusy(e);if(busy){setupEndWork();setupResult=null;setupLastState={...(setupLastState||{}),actions:busy.actions||{},outcomes:busy.outcomes||{}};await setupReadState();setupBusyApply=!ch.removals.includes(failedRow);setupDraw();return}const raw=String(e&&e.message||e);setupFailed(failedRow,ch.removals.includes(failedRow)?'Setup could not remove it.':/finish or explicitly cancel active work/.test(raw)?'An earlier install has not finished.':setupStepSentences.start,raw)}};return go(0)}
el('setup-install').onclick=()=>{const action=el('setup-install').dataset.action;if(action==='cancel')return setupCancel();if(action==='cancel-step')return setupCancelStep();if(action==='retry')return setupTryAgain();if(action==='continue')return setupContinue();if(action==='reload')return setupReload();if(action==='pictures')return setupTryPictures();if(action==='open'){const application=setupResult&&setupResult.application;if(application)location.href='/app/'+encodeURIComponent(application)+'?k='+encodeURIComponent(key)+'&open=1';return}return setupInstall()};
if(typeof addEventListener==='function')addEventListener('hashchange',setupShowRecovery);
// The page is ready once its first reads have answered and the summary
// says what the button does.
const setupFirstReads=[setupReload(),setupReadHost()];
Promise.allSettled(setupFirstReads).then(()=>{setupShowRecovery();if(typeof oaReady==='function')oaReady();else if(document.documentElement)document.documentElement.dataset.ready='1'});
</script>`
