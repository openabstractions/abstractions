package main

// libraryScript draws the Models page's Loaded now, Model files and
// Engines server rows, and runs the established setup's row (setup_step.go)
// in Default models. It runs after inferenceScript, whose call, el,
// node, serial, bytes, button, confirmed, localStep, local, modelsLive's
// hooks and oaModelsUseFor (set by selection_page.go) it uses.
//
// One read of /inference/library (models_library.go) feeds both model
// sections, with the graphics memory each running engine holds; the
// account defaults (/registry/selection) mark the chat and picture models
// apps use, and /setup/modelhost/chat says which copy OA's own model host
// can host or already hosts. A model row says what the person can do:
// Use for chat, Use for pictures, Remove (the model host stops hosting it;
// the file stays where it is). OA deletes no file here. A row names its
// engines by their upstream projects ("Runs on llama.cpp"); the router's
// engine guide for every model sits behind the group's one Technical
// details (architect ruling 2026-11-09, second block).
// While the first read of the model folders runs, the section says so and
// asks again; it never shows an empty list for a read still running.
// A model row's caption is its size, its engine and its state; the folders
// stay on the caption's title and under Technical details, and the Context
// chooser sits in the row's controls (layout ruling 2026-11-10,
// backlog/P-J1a.md).
const libraryScript = `<script>
let libraryView=null,libraryDefaults={},librarySeq=0,libraryTimer=null;
const libraryRows=new Map();
// The groups of On this computer, in order. Model files is the one group
// while the router cannot say what runs them.
const libraryGroupWords=[['chat','Chat'],['image','Pictures'],['other','Other uses'],['none','Not set up yet'],['files','Model files']];
const libraryPolicyNames={chat:'oa-chat-default',image:'oa-image-default'};
const libraryChangeText={conflict:'The choice changed elsewhere; try again.',forbidden:'OpenAbstractions did not permit this Panel to change this choice.',unavailable:'OpenAbstractions could not save this choice right now.'};
function libraryStatusLine(){const p=node('p','oa-status');p.id='models-local-line-'+(++librarySeq);if(p.setAttribute){p.setAttribute('role','status');p.setAttribute('aria-live','polite')}return p}
// libraryProblem writes one sentence and keeps the owner's own words
// behind Technical details.
function libraryProblem(id,line,raw){const e=el(id);if(!e)return;oaStatusState(e,'error');if(e.replaceChildren)e.replaceChildren();e.textContent=line;if(raw){const d=node('details','oa-error-details'),s=node('summary',null,'Technical details'),pre=node('pre',null,String(raw));d.append(s,pre);e.append(d)}}
// libraryClear drops an empty-state sentence before keyed rows return.
function libraryClear(box){if(Array.from(box.children||[]).some(c=>!c.dataset||c.dataset.key==null))box.replaceChildren()}
function libraryGroupOf(m){if(!(libraryView||{}).catalog_known)return 'files';if(!m.ready)return 'none';if(m.uses.includes('chat'))return 'chat';if(m.uses.includes('image'))return 'image';return 'other'}
// libraryIsDefault reports whether the account default for use names m.
function libraryIsDefault(m,use){const first=libraryDefaults[use];return !!first&&(first===m.key||(m.names||[]).includes(first))}
// libraryHosted names an exact inventory copy in ModelHost's hosted list,
// including a reconciler-hosted model in an external folder.
function libraryHosted(m){return(m.objects||[]).find(o=>o.hosted)||null}
// libraryRunsOn names the engines that run m by upstream name, once each:
// the engine guide's engines, else the model servers that are known engines.
function libraryRunsOn(m){const names=[],known=e=>Object.prototype.hasOwnProperty.call(oaEngineTitles,String(e).toLowerCase());for(const e of(m.runs_on||[]).length?m.runs_on:(m.engines||[]).filter(known)){const n=oaEngineName(e);if(!names.includes(n))names.push(n)}return names.length?'Runs on '+names.join(' or '):''}
function libraryCaption(m){const known=(libraryView||{}).catalog_known,parts=[];if(m.size)parts.push(oaBytes(m.size));if(known&&m.ready&&libraryRunsOn(m))parts.push(libraryRunsOn(m));if(libraryIsDefault(m,'chat'))parts.push('Chat model for your apps');else if(libraryIsDefault(m,'image'))parts.push('Picture model for your apps');else if(known)parts.push(m.running?'Loaded now':m.ready?'Ready':'No engine runs it yet');return parts.join(' · ')}
// libraryActions are what the person can do with m, in order.
function libraryActions(m){const list=[];if(m.ready&&m.uses.includes('chat')&&!libraryIsDefault(m,'chat'))list.push('chat');else if(m.ready&&m.uses.includes('image')&&!libraryIsDefault(m,'image'))list.push('image');if(libraryHosted(m))list.push('remove');return list}
function libraryRow(){const row=node('div','oa-row'),text=node('div','oa-row-text'),label=node('span','oa-row-label'),caption=node('span','oa-row-caption'),control=node('div','oa-row-control'),status=libraryStatusLine();text.append(label,caption);row.append(text,control,status);row.parts={label,caption,control,status};row.fixed=true;row.actions=null;return row}
// libraryStep runs one model host step through Engines' one-step-at-a-time
// runner, then reads the rows again.
async function libraryStep(row,request,done){const reply=await localStep(row,request,done);for(const b of row.parts.control.children||[])if(b.dataset&&b.dataset.armed==='yes')disarm(b);await libraryRead();return reply}
async function libraryUnhostCopies(model){for(const copy of(model.objects||[]).filter(o=>o.hosted)){const reply=await call('/setup/modelhost/unhost',{store:copy.store,id:copy.id,confirm:true});if(!reply.unhosted)throw Error('OpenAbstractions kept a hosted copy.')}return{unhosted:true}}
function libraryButton(row,action){const name=()=>row.model.title||oaModelName(row.model.name);
 if(action==='chat')return button('Use for chat',()=>libraryUse(row,'chat'));
 if(action==='image')return button('Use for pictures',()=>libraryUse(row,'image'));
 const b=button('Remove',null);b.onclick=confirmed(b,row,()=>libraryIsDefault(row.model,'chat')||libraryIsDefault(row.model,'image')?'Confirm to remove '+name()+' from OpenAbstractions; apps stop using it until Set up runs again, and its file stays where it is.':'Confirm to remove '+name()+' from OpenAbstractions; its file stays where it is.',()=>undefined,()=>libraryStep(row,()=>libraryUnhostCopies(row.model),()=>name()+' is removed from OpenAbstractions; its file stays where it is.'));return b}
// A row keeps its buttons while one waits for Confirm.
// The Context chooser of a model OA's model host runs (architect decision D,
// 2026-11-08): Automatic plus each size that fits this machine, from
// /inference/library. The choice is the model_context setting: the model
// host unloads the model once no request uses it, and the next conversation
// loads at the new size. It sits in the row's controls before the buttons;
// what context does is behind the section's Learn more.
let libraryContextUser=null;
function libraryTokens(n){return n>=1024?Math.floor(n/1024)+'K':String(n)}
async function libraryContextRead(){try{const u=await call('/settings/typed?action=user'),snap=u.snapshot||{},e=(snap.entries||[]).find(e=>e.key==='model_context');libraryContextUser={revision:snap.revision||'',map:(e&&e.value&&e.value.text_map)||{}}}catch(e){libraryContextUser=null}}
function libraryContextOptions(c){return [{value:'',text:'Automatic context'}].concat((c.sizes||[]).map(s=>({value:String(s.context),text:libraryTokens(s.context)+' context'+(s.slots===1?', one chat at a time':'')})))}
// libraryContextRow fills the row's Context chooser, made once per row; a
// model without one leaves the row without it.
function libraryContextRow(row,m){const c=m.context;if(!c)return null;let sel=row.parts.context;
 if(!sel){sel=node('select');if(sel.setAttribute)sel.setAttribute('aria-label','Context');row.parts.context=sel;sel.onchange=()=>libraryContextSave(row,sel)}
 const options=libraryContextOptions(c),sig=JSON.stringify(options);
 if(sel.dataset.sig!==sig){sel.replaceChildren(...options.map(o=>{const op=node('option',null,o.text);op.value=o.value;return op}));sel.dataset.sig=sig}
 const chosen=String((libraryContextUser&&libraryContextUser.map[m.key])||'');
 sel.value=options.some(o=>o.value===chosen)?chosen:'';sel.disabled=!libraryContextUser;return sel}
async function libraryContextSave(row,select){const sid=row.parts.status.id;if(!libraryContextUser)return;
 const map=Object.assign({},libraryContextUser.map);if(select.value)map[row.model.key]=select.value;else delete map[row.model.key];
 const change={key:'model_context'};if(Object.keys(map).length)change.set={kind:'text_map',text_map:map};
 select.disabled=true;oaSetStatus(sid,'loading','Saving…');
 try{const saved=await call('/settings/typed',{revision:libraryContextUser.revision,changes:[change]});
  if(saved.outcome==='applied')oaSetStatus(sid,'ok','The new size applies to the next conversation.');
  else oaSetStatus(sid,'error',saved.outcome==='conflict'?'The choice changed elsewhere; try again.':saved.outcome==='not_permitted'?'This setting cannot be changed.':'OpenAbstractions could not save this choice right now.')}
 catch(e){modelsShowError(sid,e)}
 await libraryContextRead();libraryContextRow(row,row.model)}
function libraryUpdate(row,m){libraryRows.set(m.key,row);row.model=m;row.parts.label.textContent=m.title||oaModelName(m.name);row.parts.caption.textContent=libraryCaption(m);row.parts.caption.title=(m.places||[]).map(p=>p.path).filter(Boolean).join('\n');libraryComponents(row,m.components||[]);const context=libraryContextRow(row,m),actions=libraryActions(m),sig=(context?'context ':'')+actions.join(' '),armed=Array.from(row.parts.control.children||[]).some(b=>b.dataset&&b.dataset.armed==='yes');if(sig!==row.actions&&!armed){row.actions=sig;row.parts.control.replaceChildren(...(context?[context]:[]),...actions.map(a=>libraryButton(row,a)))}}
const libraryComponentWords={projector:'Vision projector',vae:'Picture decoder',lora:'Model adjustment',adapter:'Model adapter',encoder:'Input encoder'};
function libraryComponents(row,components){if(!components.length){if(row.parts.components)row.parts.components.hidden=true;return}if(!row.parts.components){row.parts.components=node('div');row.children[0].append(row.parts.components)}row.parts.components.hidden=false;row.parts.components.replaceChildren(...components.map(c=>{const detail=node('div','oa-row-caption'),role=libraryComponentWords[c.role]||'Model component';detail.textContent=role+(c.name?' · '+oaModelName(c.name):'')+(c.place&&c.place.name?' · in '+c.place.name:'');detail.title=c.place&&c.place.path||'';return detail}))}
// Use for chat and Use for pictures make the model the account default
// that apps follow (the choice group's own write, oaModelsUseFor).
async function libraryUse(row,use){const sid=row.parts.status.id,name=row.model.title||oaModelName(row.model.name);if(typeof oaModelsUseFor!=='function'){oaSetStatus(sid,'error','Model choices cannot be saved on this page right now.');return}oaSetStatus(sid,'loading','Saving…');try{const r=await oaModelsUseFor(use,row.model.key);if(r.busy){oaSetStatus(sid,'ok','Another choice is being saved; try again.');return}const outcome=r.change&&r.change.outcome;if(outcome==='applied')oaSetStatus(sid,'ok',name+' is now the '+(use==='chat'?'chat':'picture')+' model for your apps.');else oaSetStatus(sid,'error',libraryChangeText[outcome]||'OpenAbstractions refused this choice.');await libraryRead()}catch(e){if(e&&e.message==='needs-key')oaSetStatus(sid,'error','This model needs a saved key before an app can use it.');else oaShowError(sid,e)}}
function libraryGroupBox(){const box=node('div'),heading=node('h3'),rows=node('div');box.append(heading,rows);box.parts={heading,rows};return box}
function libraryRenderModels(){const v=libraryView,box=el('models-local-rows'),models=v.models||[];
 el('models-local-guide').textContent=models.map(m=>(m.title||oaModelName(m.name))+'\n'+(m.engine_guide||['Engine association is unknown.']).concat((m.places||[]).filter(p=>p.path).map(p=>'in '+p.path)).map(line=>'  '+line).join('\n')).join('\n\n');el('models-local-tech').hidden=!models.length;
 if(!models.length){libraryRows.clear();box.replaceChildren(...(v.files_known&&v.catalog_known?[modelsEmptyNote()]:[]));return}
 libraryClear(box);
 const groups=libraryGroupWords.map(([id,title])=>({id,title,models:models.filter(m=>libraryGroupOf(m)===id)})).filter(g=>g.models.length);
 oaKeyedRows(box,groups,g=>g.id,libraryGroupBox,(group,g)=>{group.parts.heading.textContent=g.title;oaKeyedRows(group.parts.rows,g.models,m=>m.key,m=>libraryRows.get(m.key)||libraryRow(),libraryUpdate)});
 for(const k of [...libraryRows.keys()])if(!models.some(m=>m.key===k))libraryRows.delete(k)}
function libraryTextRow(){const row=node('div','oa-row'),text=node('div','oa-row-text'),label=node('span','oa-row-label'),caption=node('span','oa-row-caption');text.append(label,caption);row.append(text);row.parts={label,caption};return row}
// Loaded now: one row per loaded model, its engine and its graphics memory;
// one row says so when nothing is loaded.
function libraryRenderRunning(){const v=libraryView,box=el('models-running-rows'),list=v.running||[];if(!v.catalog_known){box.replaceChildren();return}libraryClear(box);oaKeyedRows(box,list.length?list:[{none:true}],r=>r.none?'none':r.key+'\n'+r.engine,libraryTextRow,(row,r)=>{if(r.none){row.parts.label.textContent='No model loaded';row.parts.caption.textContent='Apps load one when they ask.';return}row.parts.label.textContent=r.title||oaModelName(r.name);row.parts.caption.textContent=[oaEngineName(r.engine),r.memory>0?oaBytes(r.memory)+' of graphics memory':''].filter(Boolean).join(' · ')})}
function libraryStatus(){const v=libraryView;
 if(v.reading)oaSetStatus('models-local-status','loading','Reading model folders…');
 else if(v.files_refused)libraryProblem('models-local-status','This Panel may not read the model folders.',v.files_error);
 else if(v.files_error)oaRuntimeEcho('models-local-status',()=>libraryProblem('models-local-status','The model folders could not be read right now.',v.files_error),v.files_error);
 else if(v.catalog_refused)libraryProblem('models-local-status','This Panel may not see which engines run these models.',v.catalog_error);
 else if(v.catalog_error)libraryProblem('models-local-status','Which engines run these models is unknown right now.',v.catalog_error);
 else oaSetStatus('models-local-status','idle','');
 if(v.catalog_refused)libraryProblem('models-running-status','This Panel may not see which models are loaded.',v.catalog_error);
 else if(v.catalog_error)libraryProblem('models-running-status','Which models are loaded is unknown right now.',v.catalog_error);
 else oaSetStatus('models-running-status','idle','')}
// libraryDefaultsRead reads what an app without its own choice follows for
// chat and pictures: the selection owner's effective resolution, which
// includes the profile default Setup writes (INF-R1).
async function libraryDefaultsRead(){const found={};await Promise.all(Object.entries(libraryPolicyNames).map(async([use,name])=>{try{const r=await call('/registry/selection',{edit:'effective',name});const read=r.read||{},first=read.outcome==='found'&&read.policy&&(read.policy.candidates||[])[0];if(first)found[use]=first.model}catch(e){}}));libraryDefaults=found}
// libraryRead reads the model rows; while the first read of the model
// folders runs it asks again after a few seconds.
var libraryRead=serial(async()=>{let v;try{v=await call('/inference/library')}catch(e){if(!libraryView){el('models-local-rows').replaceChildren();el('models-running-rows').replaceChildren()}oaShowError('models-local-status',e);oaShowError('models-running-status',e);return}
 await libraryDefaultsRead();await libraryContextRead();await localReady;libraryView=v;libraryRenderModels();libraryRenderRunning();libraryRenderEngines();libraryStatus();
 if(v.reading&&typeof setTimeout==='function'){clearTimeout(libraryTimer);libraryTimer=setTimeout(()=>libraryRead(),3000)}});
// Engines lists the model servers (/inference/hosts) and each native
// provider declaration that serves an inference use (/registry/view), the
// model host among them; Providers manages them. A native engine reads
// "Loaded now" while Loaded now lists a model it runs, "Cannot start" when
// its provider is refused or unreachable (the runtime's reason under
// Technical details), and "Starts when an app asks" otherwise. A failed
// /registry/view read shows one row saying so, never "No engines yet."
let libraryEngineHosts=[],libraryEngineNatives=[],libraryEngineNativeError='';
const libraryCannotStart=new Set(['refused','unreachable','not_ready','disabled']);
function libraryNativeEngines(v){const p=v.providers||{};if(v.providers_error||p.Outcome!=='page')throw Error(v.providers_error||'provider declarations: '+(p.Outcome||'no reply'));return(p.Declarations||[]).filter(d=>d.Role==='provider'&&d.Declaration&&(d.Declaration.Contracts||[]).some(c=>String(c).startsWith('abstraction.inference/'))).map(d=>({name:d.Declaration.Name,readiness:String(d.Readiness||''),why:String(d.Why||'')}))}
async function libraryEngines(){const box=el('models-engine-rows');let l;try{[l,libraryEngineNatives]=await Promise.all([call('/inference/hosts'),call('/registry/view').then(v=>{const n=libraryNativeEngines(v);libraryEngineNativeError='';return n}).catch(e=>{libraryEngineNativeError=String(e&&e.message||e);return[]})])}catch(e){box.replaceChildren();oaShowError('models-engine-status',e);return}if(l.Outcome!=='page'){libraryEngineHosts=[];if(!libraryEngineNatives.length&&!libraryEngineNativeError){box.replaceChildren();libraryProblem('models-engine-status',l.Outcome==='forbidden'?'This Panel may not read the model servers.':'The model servers could not be read right now.',l.Outcome);return}}else{libraryEngineHosts=l.Hosts||[];oaSetStatus('models-engine-status','idle','')}libraryRenderEngines()}
function libraryNativeCaption(n){if(((libraryView&&libraryView.running)||[]).some(r=>r.engine===n.name))return'On this computer · Loaded now';return libraryCannotStart.has(n.readiness)?'On this computer · Cannot start':'On this computer · Starts when an app asks'}
function libraryRowDetails(row,raw){let d=row.parts.details;if(!raw){if(d)d.hidden=true;return}if(!d){d=node('details','oa-error-details');const s=node('summary',null,'Technical details'),pre=node('pre');d.append(s,pre);row.children[0].append(d);row.parts.details=d}d.hidden=false;d.children[1].textContent=raw}
function libraryRenderEngines(){const box=el('models-engine-rows'),names=libraryEngineNatives.map(n=>n.name),hosts=libraryEngineHosts.filter(h=>!names.includes(h.Entry.Name)),rows=hosts.map(h=>({key:'host\n'+h.Entry.Name,name:h.Entry.Name,caption:(h.Entry.Hosted?'Online service':'On this computer')+' · '+oaHostState(h.Up)})).concat(libraryEngineNatives.map(n=>{const caption=libraryNativeCaption(n);return{key:'native\n'+n.name,name:n.name,caption,details:caption.endsWith('Cannot start')?(n.why||n.readiness):''}}));if(libraryEngineNativeError)rows.push({key:'native-error',label:'Engines cannot be read now.',caption:'',details:libraryEngineNativeError});if(!rows.length){box.replaceChildren(node('p','oa-empty','No engines yet.'));return}libraryClear(box);oaKeyedRows(box,rows,r=>r.key,libraryTextRow,(row,r)=>{row.parts.label.textContent=r.label||oaEngineName(r.name);row.parts.label.title=r.name||'';row.parts.caption.textContent=r.caption;libraryRowDetails(row,r.details)})}
el('models-providers').href='/registry?k='+encodeURIComponent(key||'');
// setupStepLoad and setupStepReady are the established setup's row
// (setupStepScript, in the script before this one).
oaModelsFirstReads.push(libraryRead(),libraryEngines(),setupStepReady);
// The live reread (modelsLive) runs these hooks with live set.
oaModelsHooks.push(live=>{if(live){libraryRead();libraryEngines();setupStepLoad()}});
</script>`
