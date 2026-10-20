// Run with node monitor/setup_page_test.js; the test invokes only the page
// script against an in-memory catalog reply, without a browser or service.
const fs = require('fs'), vm = require('vm'), assert = require('assert');
const panelRules = require('./static/panel_rules.js');
const { allowlistedClasses } = require('./panel_fake_dom_test_helper.js');
// Every rule, the class contract included, with the classes the lint
// allowlist still grants Get started (none since S7 removed its last rows).
const assertPanelRules = (root, label) => panelRules.assertPanelRules(root, label, { allowClasses: allowlistedClasses('setup') });
assert.deepStrictEqual(allowlistedClasses('setup'), [], 'Get started has no allowlisted page-local class');

function source() {
  const go = fs.readFileSync(__dirname + '/setup_page.go', 'utf8');
  const start = go.indexOf('const setupPageBody = `') + 'const setupPageBody = `'.length;
  assert(start >= 'const setupPageBody = `'.length, 'setupPageBody not found');
  let script = go.slice(start, go.indexOf('</script>`', start)).split('<script>')[1];
  const glue = '` + serviceCommonScript + `';
  assert(script.includes(glue), 'serviceCommonScript glue not found');
  const common = fs.readFileSync(__dirname + '/service_page.go', 'utf8');
  const marker = 'const serviceCommonScript = `';
  const commonStart = common.indexOf(marker) + marker.length;
  script = script.replace(glue, common.slice(commonStart, common.indexOf('`', commonStart)));
  // The shell's shared script (panel_shell.go's plainScript) precedes every
  // page script in the served document; the page calls its helpers
  // (oaTechnical marks its Technical details).
  const shell = fs.readFileSync(__dirname + '/panel_shell.go', 'utf8');
  const plainMarker = 'const plainScript = `';
  const plainStart = shell.indexOf(plainMarker) + plainMarker.length;
  const plain = shell.slice(plainStart, shell.indexOf('`', plainStart)).split('<script>')[1].split('</script>')[0];
  return plain + '\n' + script;
}
// node is a fake element; createElement passes its tag so the Panel rules
// (static/panel_rules.js) see buttons, paragraphs and disclosures.
function node(tag) {
  return {tagName:tag?String(tag).toUpperCase():undefined, textContent:'', className:'', disabled:false, hidden:false, checked:false,
    dataset:{}, children:[], append(...items){this.children.push(...items)},
    replaceChildren(...items){this.children=[...items]},
    setAttribute(name,value){this[name]=String(value)}};
}
function text(n) { return [n.textContent, ...n.children.map(text)].join('\n'); }
function findHref(n) { return n.href || n.children.map(findHref).find(Boolean) || ''; }
// findText finds the first node at any depth whose own text is label: a
// progress row holds its buttons in its trailing control (oa-row-control).
// withSection gives a test's fake Apps group the DOM calls the page uses to
// put other-copy rows before More picture engines.
function withSection(get) { const section = get('setup-choices'), anchor = get('setup-engines'); anchor.parentNode = section;
  section.insertBefore = (row, before) => { row.remove = () => { section.children = section.children.filter(c => c !== row); }; const at = section.children.indexOf(before); if (at < 0) section.children.push(row); else section.children.splice(at, 0, row); }; }
// besideBoxes lists the "also install" switches at any depth.
function besideBoxes(n) { return [...(n.name === 'setup-beside' ? [n] : []), ...n.children.flatMap(besideBoxes)]; }
function findText(n, label) { if (n.textContent === label) return n; for (const c of n.children) { const hit = findText(c, label); if (hit) return hit; } return undefined; }

(async () => {
  const settled = async () => { for (let i = 0; i < 40; i++) await new Promise(resolve=>setImmediate(resolve)); };
  assert(/<button id="setup-review" disabled>/.test(fs.readFileSync(__dirname + '/setup_page.go','utf8')),'the Review control starts disabled while its required discovery is pending');
  const fields = new Map(), choices = [
    {value:'comfyui', checked:true}, {value:'opencode', checked:false},
    {value:'lemonade', checked:true},
  ];
  const get = id => { if (!fields.has(id)) fields.set(id, node()); return fields.get(id); };
  let request, savedState;
  const permissions = [{app:'comfyui',title:'ComfyUI',id:'perm-digest',sentences:['Make pictures with the local picture engine.','Save the pictures it makes.']}];
  const approvedRules = [{action:'abstraction.inference/complete',resource:'host:lemonade-image'}];
  const pictureModel = {id:'model-plan',repository:'Comfy-Org/model',revision:'5f526678002e43af5551dadb73ce2e8c91b43afe',download_bytes:16171164282,peak_bytes:18432770815,licenses:['Apache-2.0','MIT'],assets:[{id:'main',download:{label:'Main weights',bytes:100}}]};
  const plan = {id:'plan-1', selected:['comfyui','lemonade'], download_bytes:123,
    peak_bytes:456, preparable:false, ready:false, blockers:['<pending packages>'], licenses:['<license>'],
    components:[{id:'engine', title:'<Lemonade>', artifact:{version:'1.2', publisher:'<publisher>', license:'MIT', bytes:16132299052,platform:'windows/amd64',signature:'unsigned',source_commit:'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',source_url:'https://github.com/openabstractions/example/commit/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}, dependencies:[]}],
    picture_model:pictureModel, permissions,
    configuration:[{id:'chat',title:'Choose a chat model and provider for OpenCode'}]};
  const status = selected => ({plan:{...plan,selected},approved:true,current_catalog:true,ready:false,configuration:{},components:[{id:'engine',effect:{phase:'pending'}}]});
  const context = {
    document:{documentElement:{dataset:{}}, getElementById:get, querySelectorAll:selector=>selector.includes(':checked')?choices.filter(x=>x.checked):choices,
      createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'}, URLSearchParams,
    fetch:async(path,options)=>{request={path,options};const body=options&&options.body?JSON.parse(options.body):{};if(path==='/setup/other-copies')return{ok:true,json:async()=>({copies:[]})};if(path==='/setup/state')return savedState?{ok:true,json:async()=>savedState}:{ok:false,text:async()=>'404 no saved setup'};if(path==='/setup/plan'){const reply={...plan,id:body.selected.includes('opencode')?'plan-opencode':'plan-1',selected:body.selected};return {ok:true,json:async()=>reply}}return {ok:false,text:async()=>'unexpected request '+path}},
    console,
  };
  vm.runInNewContext(source(), context);
  assert.strictEqual(get('setup-status').textContent, 'Checking for other copies of ComfyUI.', 'the Review row says the copy check is running');
  await settled();
  assert.strictEqual(get('setup-status').textContent, '', 'with no other copy, the Review row keeps its one caption and an empty status line');
  assert.strictEqual(get('setup-status').dataset.state, 'idle', 'the status line leaves its loading state');
  assert.strictEqual(context.document.documentElement.dataset.ready, '1', 'the page marks itself ready once its first reads answer');
  assert(fs.readFileSync(__dirname + '/setup_page.go','utf8').includes('<div data-ready-owner="page" hidden></div>'), 'Get started declares in its markup that it calls oaReady itself');
  await get('setup-review').onclick();
  assert.strictEqual(request.path, '/setup/plan');
  assert.deepStrictEqual(JSON.parse(request.options.body), {selected:['comfyui','lemonade']});
  assert.strictEqual(get('setup-plan').hidden, false);
  // One approval screen: the goal, each app and model, the totals, the
  // permissions as sentences and one Install button (hostile critic A1).
  assert(text(get('setup-summary')).includes('Goal: Create pictures, Run pictures locally.'));
  assert(text(get('setup-summary')).includes('Package review is still in progress, so Setup cannot install yet.'));
  assert(text(get('setup-summary')).includes('Windows may ask you to approve adding local picture support.'));
  assert.strictEqual(get('setup-install').disabled, true, 'a plan still in package review cannot install');
  assert(text(get('setup-components')).includes('<Lemonade>'), 'untrusted catalog text is rendered as text');
  assert.strictEqual(get('setup-components').children[0].children[0].textContent, '<Lemonade>');
  assert(text(get('setup-components')).includes('16.1 GB'), 'artifact bytes use the human-readable byte field and formatter');
  assert(text(get('setup-components')).includes('Windows 64-bit'));
  assert(text(get('setup-components')).includes('Unsigned package'));
  assert(text(get('setup-components')).includes('Source revision: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'));
  assert(text(get('setup-components')).includes('View source code at this revision'));
  assert.strictEqual(findHref(get('setup-components')),'https://github.com/openabstractions/example/commit/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
  const modelItem = get('setup-components').children[1];
  assert.strictEqual(modelItem.children[0].textContent, 'Starter picture model (FLUX.2 Klein)');
  assert.strictEqual(modelItem.children[1].textContent, 'Publisher: Comfy-Org · License: Apache-2.0, MIT · 16.2 GB', 'the model shows its publisher, licence and size');
  assert(!modelItem.children[1].textContent.includes(pictureModel.revision), 'the pinned revision stays behind the model details');
  assert(text(get('setup-permissions')).includes('ComfyUI will be allowed to:'));
  assert(text(get('setup-permissions')).includes('Make pictures with the local picture engine.'));
  assert(!/abstraction\.|output:|host:/.test(text(get('setup-permissions'))), 'permissions are sentences with no rule ID');
  assert(text(get('setup-blockers')).includes('Licences: <license>, Apache-2.0, MIT.'));
  assert(text(get('setup-configuration')).includes('Choose a chat model and provider for OpenCode'));
  assertPanelRules(['setup-summary','setup-components','setup-permissions','setup-blockers','setup-configuration'].map(get),'the review');
  choices[1].checked = true;
  choices[1].onchange();
  assert.strictEqual(get('setup-plan').hidden, true, 'changing a goal invalidates the prior review');
  assert.strictEqual(get('setup-install').disabled, true, 'changing a goal disables Install until the next review');
  await get('setup-install').onclick();
  assert.strictEqual(request.path, '/setup/plan', 'a stale review cannot install after a goal changes');
  savedState = {...status(['comfyui','opencode','lemonade']), plan:{...plan,id:'plan-opencode',selected:['comfyui','opencode','lemonade']}};
  const reloadFields = new Map(), reloadChoices = [
    {value:'comfyui', checked:false}, {value:'opencode', checked:false},
    {value:'lemonade', checked:false},
  ];
  const reloadGet = id => { if (!reloadFields.has(id)) reloadFields.set(id, node()); return reloadFields.get(id); };
  const reloadContext = {
    document:{getElementById:reloadGet, querySelectorAll:selector=>selector.includes(':checked')?reloadChoices.filter(x=>x.checked):reloadChoices,
      createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'}, URLSearchParams,
    fetch:async(path)=>path==='/setup/install'?{ok:true,json:async()=>({...savedState,install:{}})}:{ok:true,json:async()=>savedState},
    console,
  };
  vm.runInNewContext(source(), reloadContext);
  await settled();
  assert.deepStrictEqual(reloadChoices.filter(x=>x.checked).map(x=>x.value), ['comfyui','opencode','lemonade']);
  assert.strictEqual(reloadGet('setup-progress').hidden, false, 'saved setup progress returns after reload');
  assert.strictEqual(reloadGet('setup-install').disabled, true, 'restoring progress does not offer a second Install');
  assert(text(reloadGet('setup-status')).includes('Saved setup plan restored.'));

  const lateFields = new Map(), lateChoices = [
    {value:'comfyui', checked:false}, {value:'opencode', checked:false},
    {value:'lemonade', checked:false},
  ];
  const lateGet = id => { if (!lateFields.has(id)) lateFields.set(id, node()); return lateFields.get(id); };
  let releaseState;
  const lateContext = {
    document:{getElementById:lateGet, querySelectorAll:selector=>selector.includes(':checked')?lateChoices.filter(x=>x.checked):lateChoices,
      createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'}, URLSearchParams,
    fetch:path=>path==='/setup/other-copies'?Promise.resolve({ok:true,json:async()=>({copies:[]})}):path==='/setup/state'?new Promise(resolve=>{releaseState=()=>resolve({ok:true,json:async()=>savedState})}):Promise.reject(Error('unexpected request')),
    console, 
  };
  vm.runInNewContext(source(), lateContext);
  lateChoices[1].checked=true;
  lateChoices[1].onchange();
  releaseState();
  await new Promise(resolve=>setImmediate(resolve));
  assert.deepStrictEqual(lateChoices.filter(x=>x.checked).map(x=>x.value), ['opencode'], 'late saved-state response does not overwrite a new choice');

  // A plan approved before one Install existed keeps its step-by-step
  // sections: the feature, the model review and the progress rows.
  const actionFields = new Map(), actionChoices = [
    {value:'comfyui', checked:false}, {value:'opencode', checked:false},
    {value:'lemonade', checked:false},
  ];
  const actionGet = id => { if (!actionFields.has(id)) actionFields.set(id, node()); return actionFields.get(id); };
  const readyPlan = {...plan,id:'approved-plan',selected:['comfyui'],preparable:true,download_bytes:16132299052,peak_bytes:20000000000,blockers:[],licenses:['GPL-3.0'],
    components:[{id:'comfyui-app',title:'ComfyUI',dependencies:[],artifact:{version:'1',publisher:'Example',license:'GPL-3.0',bytes:100}}],
    configuration:[{id:'comfyui-image-policy',title:'Choose an image model'}]};
  let actionStatus={plan:readyPlan,approved:true,current_catalog:true,ready:false,configuration:{},components:[{id:'comfyui-app',effect:{phase:'pending'}}]};
  const actionContext={
    document:{getElementById:actionGet,querySelectorAll:selector=>selector.includes(':checked')?actionChoices.filter(x=>x.checked):actionChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,
    setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{request={path,options};if(path==='/setup/other-copies')return{ok:true,json:async()=>({copies:[]})};if(path==='/setup/state')return {ok:true,json:async()=>actionStatus};if(path==='/setup/install')return {ok:true,json:async()=>({...actionStatus,install:{}})};if(path==='/setup/feature')return {ok:true,json:async()=>({available:true,installed:false,reason:'Add the optional image support feature'})};if(path==='/setup/model')return {ok:true,json:async()=>({enabled:true,plan:{id:'model-plan',model:'user.LocalApprovedKlein',repository:'Comfy-Org/model',revision:'abc',license:'Apache-2.0',license_url:'https://www.apache.org/licenses/LICENSE-2.0',download_bytes:16171164282,peak_bytes:18432770815,assets:[{id:'main',download:{label:'Main weights',bytes:100}}]},choices:[{id:'z-plan',model:'user.Z-Image-Turbo',name:'Z-Image-Turbo',source:'https://huggingface.co/Tongyi-MAI/Z-Image-Turbo',licenses:['Apache-2.0'],license_text:'Tongyi-MAI publishes Z-Image-Turbo under the Apache License 2.0.',download_bytes:20690152836,peak_bytes:64217942124,assets:[]},{id:'q-plan',model:'user.Qwen-Image-2512',name:'Qwen-Image-2512',source:'javascript:alert(1)',licenses:['Apache-2.0'],license_text:'Qwen publishes Qwen-Image-2512 under the Apache License 2.0.',download_bytes:19439778886,peak_bytes:60466820282,assets:[]}],disk:{enough:false,required_peak_bytes:18432770815,stage_available_bytes:1,provider_required_bytes:16171164282,provider_available_bytes:1},status:{approved:false,assets:[]}})};if(path==='/setup/advance'){const phase=actionStatus.components[0].effect.phase;if(phase==='pending')actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'requested'},transfer:{state:'running',done:50,total:100}}]};else if(phase==='requested')actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'verified'}}]};else actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'installed'}}]};return {ok:true,json:async()=>actionStatus}}if(path==='/setup/retry'){actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'requested'},transfer:{state:'running',done:0,total:100}}]};return {ok:true,json:async()=>actionStatus}}if(path==='/setup/recover'){actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'installed'}}]};return {ok:true,json:async()=>actionStatus}}return {ok:false,text:async()=>'unexpected request '+path}},
  };
  vm.runInNewContext(source(),actionContext);
  await settled();
  assert.strictEqual(actionGet('setup-model').hidden,false,'an earlier approved picture plan shows the separate fixed starter-model review');
  assert(text(actionGet('setup-model-review')).includes('16.2 GB download'));
  assert(text(actionGet('setup-model-review')).includes('18.4 GB peak free space'));
  assert.strictEqual(actionGet('setup-model-approve').disabled,true,'insufficient disk space disables model approval');
  const offered=text(actionGet('setup-model-choices'));
  assert(offered.includes('Z-Image-Turbo · Apache-2.0 · 20.7 GB download'),'Setup lists each other picture model with its licence and size');
  assert(offered.includes('Qwen-Image-2512 · Apache-2.0 · 19.4 GB download'));
  assert(offered.includes('Tongyi-MAI publishes Z-Image-Turbo under the Apache License 2.0.'),'Setup shows each model licence text');
  const links=[];(function walk(n){for(const c of n.children||[]){if(c.href!==undefined)links.push(c.href);walk(c)}})(actionGet('setup-model-choices'));
  assert(links.includes('https://huggingface.co/Tongyi-MAI/Z-Image-Turbo')&&links.includes('https://www.apache.org/licenses/LICENSE-2.0'),'model card and licence links');
  assert(!links.includes('javascript:alert(1)'),'only a Hugging Face model card becomes a link');
  assert(!offered.includes('Approve'),'offered models carry no approval control');
  assert.strictEqual(actionStatus.components[0].effect.phase,'requested','approved transfer is submitted and then observed');
  assert.strictEqual(actionGet('setup-image-feature').hidden,false,'approved local picture plans show the fixed core feature prerequisite');
  assert(text(actionGet('setup-image-feature-status')).includes('Add the optional image support feature'));
  assert.strictEqual(actionGet('setup-image-feature-add').hidden,false,'the feature action appears only when the fixed feature is available and missing');
  assert(text(actionGet('setup-progress-list')).includes('Downloaded 50 bytes of 100 bytes'));
  assert(!text(actionGet('setup-progress-list')).includes('Start this step'),'approved normal progress advances without per-component clicks');
  actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'requested'},transfer:{state:'complete',done:100,total:100}}]};
  await actionContext.setupRefreshState();
  assert.strictEqual(actionStatus.components[0].effect.phase,'installed','approved download and install steps advance without extra clicks');
  assert(text(actionGet('setup-configuration')).includes('Still needed: Choose an image model'),'application install stays pending until its configuration requirement is verified');
  actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'requested'},transfer:{state:'unknown'}}]};
  await actionContext.setupRefreshState();
  assert.strictEqual(request.path,'/setup/state','unknown transfer state is observed without advancing or resubmitting work');
  assert.strictEqual(actionStatus.components[0].effect.phase,'requested');
  assert(text(actionGet('setup-progress-list')).includes('temporarily unavailable'));
  actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'failed',reason:'service refused'},transfer:null}]};
  await actionContext.setupRefreshState(false);
  let actionButton=findText(actionGet('setup-progress-list').children[0],'Retry failed download');
  assert(actionButton,'only a known failed step exposes Retry');
  // A failed component is one row: its name, one sentence of status, Retry
  // in the row's control and the row's own status line (SPEC §4, S7).
  const failedRow=actionGet('setup-progress-list').children[0];
  assert.deepStrictEqual(failedRow.children.map(c=>c.className),['oa-row-text','oa-row-control','oa-status']);
  assert.strictEqual(failedRow.className,'oa-row');
  assert.strictEqual(failedRow.children[0].children[1].textContent,'Step failed: service refused.');
  // A reread updates rows in place: the same row and the same button, so
  // focus stays on it (S5 review rule).
  await actionContext.setupRefreshState(false);
  assert.strictEqual(actionGet('setup-progress-list').children[0],failedRow,'a reread keeps the row');
  assert.strictEqual(findText(failedRow,'Retry failed download'),actionButton,'a reread keeps the button');
  await actionButton.onclick();
  assert.match(failedRow.children[2].textContent,/^Retrying .+\.$/,'Retry writes one sentence into its row');
  assertPanelRules(failedRow,'a failed component row');
  assert.strictEqual(request.path,'/setup/retry');
  const retried=failedRow.children[2].textContent;
  await actionContext.setupRefreshState(false);
  assert.strictEqual(request.path,'/setup/state','the tick rereads');
  assert.strictEqual(actionGet('setup-progress-list').children[0],failedRow,'the tick keeps the row');
  assert.strictEqual(failedRow.children[2].textContent,retried,'the sentence Retry wrote survives the tick');
  assert.strictEqual(failedRow.children[0].children[1].textContent,'Download in progress.','the server\'s new state shows in the same row');
  assert.strictEqual(findText(failedRow,'Retry failed download'),undefined,'a download in progress offers no Retry');
  // One reread at a time: a tick during a pass waits for it, then runs once.
  {
    const held=[];let inFlight=0,most=0;const plain=actionContext.fetch;
    actionContext.fetch=(path,options)=>path==='/setup/state'?new Promise(resolve=>{inFlight++;most=Math.max(most,inFlight);held.push(()=>{inFlight--;resolve(plain(path,options))})}):plain(path,options);
    const first=actionContext.setupRefreshState(false),second=actionContext.setupRefreshState(false),third=actionContext.setupRefreshState(false);
    await settled();
    assert.strictEqual(held.length,1,'no second pass while one runs');
    held.shift()();await settled();
    assert.strictEqual(held.length,1,'the ticks during the pass become one more pass');
    held.shift()();await Promise.all([first,second,third]);await settled();
    assert.strictEqual(held.length,0);assert.strictEqual(most,1,'never two passes at once');
    actionContext.fetch=plain;
  }
  actionStatus={...actionStatus,components:[{id:'comfyui-app',effect:{phase:'installing'}}]};
  await actionContext.setupRefreshState(false);
  actionButton=findText(actionGet('setup-progress-list').children[0],'Check interrupted installation');
  assert(actionButton,'an interrupted install exposes an explicit recovery action');
  await actionButton.onclick();
  assert.strictEqual(request.path,'/setup/recover');

  // A generated component's ordinary retry uses the exact reviewed plan.
  // Missing birth ownership and a removal in progress offer no install retry.
  const ptRetryState={...actionStatus,plan:{...readyPlan,id:'pt-interrupted',selected:['pytorch'],components:[{id:'pytorch-engine',title:'PyTorch'}]},components:[{id:'pytorch-engine',effect:{phase:'staged',component_root_key:'fixture-root-key'}}]};
  actionContext.setupRenderState(ptRetryState);
  actionButton=findText(actionGet('setup-progress-list').children[0],'Retry component setup');
  assert(actionButton,'an interrupted same-root component offers its explicit retry');
  actionStatus=ptRetryState;
  await actionButton.onclick();
  assert.strictEqual(request.path,'/setup/retry');
  assert.deepStrictEqual(JSON.parse(request.options.body),{component_id:'pytorch-engine',plan_id:'pt-interrupted'});
  for(const effect of [{phase:'staged'},{phase:'staged',component_root_key:'fixture-root-key',removing:true}]){
    actionContext.setupRenderState({...ptRetryState,components:[{id:'pytorch-engine',effect}]});
    assert(!text(actionGet('setup-progress-list')).includes('Retry component setup'),'unowned/removing roots cannot be retried as an install');
  }

  // One Install: tick the goal, Review, Install. Every later step runs on its
  // own in order, with no further click, and ends with ComfyUI opened on the
  // picture recipe (hostile critic A1).
  const runtime = () => {
    const sim = {feature:false, phases:{'comfyui-app':'pending','lemonade-engine':'pending'}, approved:false, record:null,
      model:{approved:false, assets:[{id:'main',phase:'pending'},{id:'vae',phase:'pending'}], provider_configured:false},
      comfy:{registered:false, ready:false, web_ready:false}, calls:[], timers:[], slow:false, transferDone:true};
    const installPlan = {...plan,id:'install-plan',selected:['comfyui'],preparable:true,blockers:[],licenses:['GPL-3.0'],
      components:[{id:'comfyui-app',title:'ComfyUI',dependencies:[],artifact:{version:'1',publisher:'OA',license:'GPL-3.0',bytes:100}},
        {id:'lemonade-engine',title:'Lemonade',dependencies:[],artifact:{version:'2',publisher:'lemonade-sdk',license:'Apache-2.0',bytes:100}}],
      configuration:[]};
    sim.plan = installPlan;
    const state = () => ({plan:installPlan,approved:sim.approved,current_catalog:true,ready:false,configuration:{},
      components:Object.entries(sim.phases).map(([id,phase])=>({id,effect:{phase}}))});
    const modelReply = () => ({enabled:true,available:true,setup_plan_id:'install-plan',plan:{id:'model-plan',repository:'Comfy-Org/model',assets:[]},
      disk:{enough:true},feature:{installed:sim.feature},status:{approved:sim.model.approved,assets:sim.model.assets.map(a=>({...a,transfer:a.phase==='requested'?{state:sim.transferDone?'complete':'running'}:undefined})),provider_configured:sim.model.provider_configured}});
    const json = body => ({ok:true,json:async()=>body});
    sim.fetch = async(path,options)=>{const body=options&&options.body?JSON.parse(options.body):null;sim.calls.push(body?path:'GET '+path);
      switch(path){
      case '/setup/other-copies': return json({copies:[]});
      case '/setup/state': return sim.approved?json(state()):{ok:false,text:async()=>'404 no saved setup'};
      case '/setup/plan': return json(installPlan);
      case '/setup/install':
        if(!body)return json({...state(),install:sim.record||{}});
        assert.deepStrictEqual(body,{selected:['comfyui'],plan_id:'install-plan',model_plan_id:'model-plan',permissions_id:'perm-digest',confirm:true},'one Install approves the plan, its picture model and the stated permissions together');
        sim.approved=true;sim.record={plan_id:'install-plan',model_plan_id:'model-plan',rules:approvedRules,rules_digest:'perm-digest'};return json({...state(),install:sim.record});
      case '/setup/feature': return json({available:true,installed:sim.feature});
      case '/setup/feature/add': assert.deepStrictEqual(body,{plan_id:'install-plan',confirm:true});sim.feature=true;return json({available:true,installed:true});
      case '/setup/advance': assert(sim.feature,'the engine waits for local picture support');sim.phases[body.component_id]='installed';return json(state());
      case '/setup/model': return json(modelReply());
      case '/setup/model/start': assert.deepStrictEqual(body,{plan_id:'model-plan',confirm:true,setup_plan_id:'install-plan'});sim.model.approved=true;return json(modelReply());
      case '/setup/model/advance': {const a=sim.model.assets.find(x=>x.id===body.asset_id);a.phase=a.phase==='pending'&&sim.slow?'requested':'verified'}return json(modelReply());
      case '/setup/model/configure': assert(Object.values(sim.phases).every(p=>p==='installed'),'the picture engine is configured after the apps are installed');sim.model.provider_configured=true;return json(modelReply());
      case '/setup/install/grants': assert.deepStrictEqual(body,{setup_plan_id:'install-plan'});sim.record={...sim.record,granted:true};return json(sim.record);
      case '/setup/comfy/status': return json({available:true,...sim.comfy,web_url:sim.comfy.web_ready?'http://127.0.0.1:8188/':''});
      case '/setup/comfy/register': sim.comfy.registered=true;return json({available:true,...sim.comfy});
      case '/setup/comfy/activate': sim.comfy.ready=true;sim.comfy.web_ready=true;return json({available:true,...sim.comfy,web_url:'http://127.0.0.1:8188/'});
      case '/setup/install/open': sim.record={...sim.record,opened:true};return json(sim.record);
      }
      return {ok:false,text:async()=>'unexpected request '+path}};
    return sim;
  };
  const installSim = runtime();
  const installFields = new Map(), installChoices = [{value:'comfyui', checked:false}, {value:'opencode', checked:false}, {value:'lemonade', checked:false}];
  const installGet = id => { if (!installFields.has(id)) installFields.set(id, node()); return installFields.get(id); };
  const installContext = {document:{getElementById:installGet,querySelectorAll:selector=>selector.includes(':checked')?installChoices.filter(x=>x.checked):installChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:fn=>{installSim.timers.push(fn);return installSim.timers.length},clearTimeout:()=>{},console,fetch:installSim.fetch};
  for (const id of ['setup-model','setup-comfy','setup-image-feature','setup-install-progress']) installGet(id).hidden = true; // as the page's markup starts them
  vm.runInNewContext(source(),installContext);
  await settled();
  let actions = 0;
  installChoices[0].checked = true; installChoices[0].onchange(); actions++;
  await installGet('setup-review').onclick(); actions++;
  assert.strictEqual(installGet('setup-install').disabled, false, 'a reviewed, reviewable plan offers Install');
  await installGet('setup-install').onclick(); actions++;
  await settled();
  const posts = installSim.calls.filter(c=>!c.startsWith('GET '));
  const firstOf = path => posts.indexOf(path);
  assert(!posts.includes('/setup/intent'), 'no separate save step');
  for (const [before, after] of [['/setup/install','/setup/feature/add'],['/setup/feature/add','/setup/advance'],['/setup/advance','/setup/model/start'],
    ['/setup/model/start','/setup/model/advance'],['/setup/model/advance','/setup/model/configure'],['/setup/model/configure','/setup/install/grants'],
    ['/setup/install/grants','/setup/comfy/register'],['/setup/comfy/register','/setup/comfy/activate'],['/setup/comfy/activate','/setup/install/open']]) {
    assert(firstOf(before) >= 0 && firstOf(after) > firstOf(before), before+' runs before '+after+': '+posts.join(', ')+' | '+text(installGet('setup-install-status'))+' | '+text(installGet('setup-model-status')));
  }
  assert.strictEqual(actions, 3, 'tick the goal, Review and Install');
  assert.strictEqual(installGet('setup-install-progress').hidden, false);
  assert.strictEqual(installGet('setup-model').hidden, true, 'the step-by-step model section stays hidden during one Install');
  assert.strictEqual(installGet('setup-comfy').hidden, true, 'the step-by-step workspace section stays hidden during one Install');
  assert(text(installGet('setup-install-status')).includes('ComfyUI is open with the picture recipe.'), text(installGet('setup-install-status')));
  assert.strictEqual(installGet('setup-install-steps').children.length, 6);
  assert(installGet('setup-install-steps').children.every(row=>row.className==='oa-row'&&text(row).endsWith('\nDone')), text(installGet('setup-install-steps')));

  // A restart resumes the approved steps where their journals stand, with no
  // click and no second approval.
  const resumeSim = runtime();
  Object.assign(resumeSim, {approved:true, feature:true, record:{plan_id:'install-plan',model_plan_id:'model-plan',rules:approvedRules,rules_digest:'perm-digest',granted:true}});
  resumeSim.phases = {'comfyui-app':'installed','lemonade-engine':'installed'};
  resumeSim.model = {approved:true, assets:[{id:'main',phase:'verified'}], provider_configured:true};
  resumeSim.comfy = {registered:true, ready:false, web_ready:false};
  const resumeFields = new Map(), resumeChoices = [{value:'comfyui', checked:false}, {value:'opencode', checked:false}, {value:'lemonade', checked:false}];
  const resumeGet = id => { if (!resumeFields.has(id)) resumeFields.set(id, node()); return resumeFields.get(id); };
  vm.runInNewContext(source(),{document:{getElementById:resumeGet,querySelectorAll:selector=>selector.includes(':checked')?resumeChoices.filter(x=>x.checked):resumeChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:fn=>{resumeSim.timers.push(fn);return resumeSim.timers.length},clearTimeout:()=>{},console,fetch:resumeSim.fetch});
  await settled();
  const resumed = resumeSim.calls.filter(c=>!c.startsWith('GET '));
  assert.deepStrictEqual(resumed, ['/setup/comfy/activate','/setup/install/open'], 'only the unfinished steps run after a restart: '+resumed.join(', '));
  assert(text(resumeGet('setup-install-status')).includes('ComfyUI is open with the picture recipe.'));

  // A step still in progress waits and continues on its own.
  const waitSim = runtime();
  Object.assign(waitSim, {approved:true, feature:true, record:{plan_id:'install-plan',model_plan_id:'model-plan',rules:approvedRules,rules_digest:'perm-digest'}});
  waitSim.phases = {'comfyui-app':'installed','lemonade-engine':'installed'};
  Object.assign(waitSim, {slow:true, transferDone:false});
  const waitFields = new Map(), waitChoices = [{value:'comfyui', checked:false}, {value:'opencode', checked:false}, {value:'lemonade', checked:false}];
  const waitGet = id => { if (!waitFields.has(id)) waitFields.set(id, node()); return waitFields.get(id); };
  let waitNow = 1000000;
  const waitContext = {Date:{now:()=>waitNow},document:{getElementById:waitGet,querySelectorAll:selector=>selector.includes(':checked')?waitChoices.filter(x=>x.checked):waitChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:fn=>{waitSim.timers.push(fn);return waitSim.timers.length},clearTimeout:()=>{},console,fetch:waitSim.fetch};
  vm.runInNewContext(source(),waitContext);
  await settled();
  assert(text(waitGet('setup-install-status')).includes('Waiting for the picture model.'), text(waitGet('setup-install-status')));
  assert(!waitSim.calls.includes('/setup/install/open'));
  assert.strictEqual(waitGet('setup-install-again').hidden, true, 'no Try again while a step is young');
  const waitRows = waitGet('setup-install-steps').children.slice();
  waitNow += 600001;
  { const fn = waitSim.timers.filter(f=>String(f).includes('setupInstallDrive')).pop() || waitSim.timers[waitSim.timers.length-1]; await fn(); await settled(); }
  assert(text(waitGet('setup-install-status')).includes('Still waiting for the picture-model downloads (0 of 2 verified).'), text(waitGet('setup-install-status')));
  assert.strictEqual(waitGet('setup-install-again').hidden, false, 'a long wait offers Try again');
  assert(waitGet('setup-install-steps').children.every((row,i)=>row===waitRows[i]), 'a tick updates the step rows in place');
  waitSim.transferDone = true;
  while (waitSim.timers.length) { const fn = waitSim.timers.shift(); await fn(); await settled(); }
  assert(waitSim.calls.includes('/setup/install/open'), 'the waiting step continued on its own: '+waitSim.calls.join(', '));

  // A step that fails reads Stopped, never In progress, beside Try again and
  // one sentence of status (SPEC §4, S7: the failed state).
  const stopSim = runtime();
  Object.assign(stopSim, {approved:true, feature:true, record:{plan_id:'install-plan',model_plan_id:'model-plan',rules:approvedRules,rules_digest:'perm-digest',granted:true}});
  stopSim.phases = {'comfyui-app':'installed','lemonade-engine':'installed'};
  stopSim.model = {approved:true, assets:[{id:'main',phase:'verified'}], provider_configured:true};
  stopSim.comfy = {registered:true, ready:false, web_ready:false};
  const stopFetch = stopSim.fetch;
  stopSim.fetch = async(path,options)=>path==='/setup/comfy/activate'?{ok:false,text:async()=>'ComfyUI did not start'}:stopFetch(path,options);
  const stopFields = new Map(), stopChoices = [{value:'comfyui', checked:false}, {value:'opencode', checked:false}, {value:'lemonade', checked:false}];
  const stopGet = id => { if (!stopFields.has(id)) stopFields.set(id, node()); return stopFields.get(id); };
  vm.runInNewContext(source(),{document:{getElementById:stopGet,querySelectorAll:selector=>selector.includes(':checked')?stopChoices.filter(x=>x.checked):stopChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:fn=>{stopSim.timers.push(fn);return stopSim.timers.length},clearTimeout:()=>{},console,fetch:stopSim.fetch});
  await settled();
  assert.strictEqual(text(stopGet('setup-install-status')), 'Setup stopped: ComfyUI did not start');
  assert.strictEqual(stopGet('setup-install-again').hidden, false, 'a stopped step offers Try again');
  const stopRows = stopGet('setup-install-steps').children.map(text);
  assertPanelRules(['setup-install-steps','setup-progress-list'].map(stopGet),'the stopped install');
  assert(stopRows.some(row=>row.endsWith('\nStopped')) && !stopRows.some(row=>row.endsWith('\nIn progress')), stopRows.join(' | '));

  const priorFields=new Map(),priorChoices=[{value:'comfyui',checked:false},{value:'opencode',checked:false},{value:'lemonade',checked:false}];
  const priorGet=id=>{if(!priorFields.has(id))priorFields.set(id,node());return priorFields.get(id)};
  const priorState={plan:{...readyPlan,id:'older-plan',selected:['comfyui'],preparable:false},approved:false,current_catalog:false,ready:false,configuration:{},components:[{id:'comfyui-app',effect:{phase:'installed'}},{id:'lemonade-engine',effect:{phase:'requested'}}]};
  const priorCalls=[];
  const priorContext={document:{getElementById:priorGet,querySelectorAll:selector=>selector.includes(':checked')?priorChoices.filter(x=>x.checked):priorChoices,createElement:tag=>node(tag)},location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,fetch:async(path,options)=>{priorCalls.push(path);if(path==='/setup/other-copies')return{ok:true,json:async()=>({copies:[]})};if(path==='/setup/state')return{ok:true,json:async()=>priorState};if(path==='/setup/comfy/status')return{ok:true,json:async()=>({available:true,registered:false,reason:'Check the installed workspace before opening'})};if(path==='/setup/comfy/activate'){assert.deepStrictEqual(JSON.parse(options.body),{setup_plan_id:'older-plan',confirm:true});return{ok:true,json:async()=>({available:true,registered:true,readable:true,matching:true,starting:true})}}throw Error('unexpected prior-plan action '+path)}};
  vm.runInNewContext(source(),priorContext);
  await new Promise(resolve=>setImmediate(resolve));
  assert(text(priorGet('setup-status')).includes('earlier catalog'),'prior plan is identified as stale');
  assert.strictEqual(priorGet('setup-install').disabled,true,'saved old review cannot install new work');
  assert.strictEqual(priorGet('setup-comfy').hidden,false,'verified old Comfy install has an open path');
  assert.strictEqual(priorGet('setup-comfy-register').hidden,true,'stale plan cannot register a new app descriptor');
  assert(!priorCalls.includes('/setup/advance'),'old requested transfer is never resumed automatically');
  assert(!priorCalls.includes('/setup/comfy/activate'),'Get started starts no app; its app page does');

  const comfyFields=new Map(),comfyChoices=[{value:'comfyui',checked:true},{value:'opencode',checked:false},{value:'lemonade',checked:false}],comfyGet=id=>{if(!comfyFields.has(id))comfyFields.set(id,node());return comfyFields.get(id)};
  let comfyRegistered=false,comfyStarting=false,comfyReady=false,comfyWebReady=false,comfyLaunchCalls=0,comfyRegisterBody,comfyActivateBody;
  const comfyContext={document:{getElementById:comfyGet,querySelectorAll:selector=>selector.includes(':checked')?comfyChoices.filter(x=>x.checked):comfyChoices,createElement:tag=>node(tag)},location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,fetch:async(path,options)=>{const body=options&&options.body?JSON.parse(options.body):{};if(path==='/setup/other-copies')return{ok:true,json:async()=>({copies:[]})};if(path==='/setup/state')return{ok:false,text:async()=>'404'};if(path==='/setup/model')return{ok:true,json:async()=>({enabled:true,available:true,setup_plan_id:'approved-plan',plan:{id:'model-plan',assets:[]},disk:{enough:true},feature:{installed:true},status:{ready:true,approved:true,assets:[]}})};if(path==='/setup/comfy/status')return{ok:true,json:async()=>comfyRegistered?{available:true,registered:true,readable:true,matching:true,ready:comfyReady,starting:comfyStarting,web_ready:comfyWebReady,web_url:comfyWebReady?'http://127.0.0.1:8188/':''}:{available:true,registered:false,reason:'not registered'}};if(path==='/setup/comfy/register'){comfyRegisterBody=body;comfyRegistered=true;return{ok:true,json:async()=>({available:true,registered:true,readable:true,matching:true})}}if(path==='/setup/comfy/activate'){comfyActivateBody=body;comfyLaunchCalls++;comfyStarting=true;return{ok:true,json:async()=>({available:true,registered:true,readable:true,matching:true,starting:true,outcome:'not_ready',started:true})}}throw Error('unexpected request '+path)}};
  vm.runInNewContext(source(),comfyContext);
  await comfyContext.setupRefreshModel();
  assert.strictEqual(comfyGet('setup-comfy-register').hidden,false,'the explicit fixed registration step appears only after exact picture readiness');
  await comfyGet('setup-comfy-register').onclick();
  assert.deepStrictEqual(comfyRegisterBody,{setup_plan_id:'approved-plan',confirm:true},'registration is bound to current approved plan and explicit click');
  await comfyContext.setupRefreshComfy();
  assert.strictEqual(comfyLaunchCalls,0,'Get started starts nothing; the app page owns Start');
  assert.strictEqual(comfyGet('setup-comfy-web').hidden,true,'no browser link appears before the fixed local UI responds');
  comfyStarting=false;comfyReady=true;comfyWebReady=true;
  await comfyContext.setupRefreshComfy();
  assert.strictEqual(comfyGet('setup-comfy-web').hidden,false,'the Open action appears only after the availability check');
  assert.strictEqual(comfyGet('setup-comfy-web').href,'http://127.0.0.1:8188/','the page link is fixed to the local ComfyUI address');
  assert.strictEqual(comfyGet('setup-comfy-next').hidden,false,'the picture recipe next step appears with the verified UI link');
  // A differing registered entry shows its one-sentence reason; the command
  // stays in Technical details, and Remove is on the app page.
  const rmFields=new Map(),rmGet=id=>{if(!rmFields.has(id))rmFields.set(id,node());return rmFields.get(id)};
  const rmCalls=[];
  const rmContext={document:{getElementById:rmGet,querySelectorAll:()=>[],createElement:tag=>node(tag)},location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path)=>{rmCalls.push(path);
      if(path==='/setup/state')return{ok:false,text:async()=>'404'};
      if(path==='/setup/model')return{ok:true,json:async()=>({enabled:true,available:true,setup_plan_id:'approved-plan',plan:{id:'model-plan',assets:[]},disk:{enough:true},feature:{installed:true},status:{ready:true,approved:true,assets:[]}})};
      if(path==='/setup/comfy/status')return{ok:true,json:async()=>({available:true,registered:true,conflict:true,reason:'The registered picture workspace entry is from an earlier OpenAbstractions build; it was preserved.',name:'ComfyUI for OpenAbstractions',command:'openabstractions applications remove comfyui'})};
      return{ok:true,json:async()=>({})}}};
  vm.runInNewContext(source(),rmContext);
  await rmContext.setupRefreshModel();
  assert.strictEqual(rmGet('setup-comfy-conflict').hidden,false,'the details are offered');
  assert(!rmGet('setup-comfy-status').textContent.includes('openabstractions applications remove'),'the visible sentence carries no command');
  assert(rmGet('setup-comfy-command').textContent.includes('openabstractions applications remove comfyui'),'the command stays in the details');
  assert(!rmGet('setup-comfy-status').textContent.includes('comfyui'),'no application id is visible page text');
  assert(!rmCalls.some(p=>p.startsWith('/setup/comfy/remove')||p.startsWith('/setup/comfy/activate')),'Get started removes and starts nothing');

  // An update: four OpenCode steps and a removal run without a reload, and the
  // driver is not limited to a fixed number of steps per component.
  const updFields=new Map(),updGet=id=>{if(!updFields.has(id))updFields.set(id,node());return updFields.get(id)};
  const updChoices=[{value:'comfyui',checked:false},{value:'opencode',checked:true},{value:'lemonade',checked:false}];
  const updPlan={...plan,id:'update-plan',selected:['opencode'],preparable:true,blockers:[],licenses:['MIT'],
    components:[{id:'opencode-official',title:'OpenCode',dependencies:[],artifact:{version:'2.0.0',publisher:'OA',license:'MIT',bytes:100}}],
    configuration:[],replaces:[{component_id:'opencode-official',title:'OpenCode',from:'1.0.0',to:'2.0.0'}]};
  const v1={version:'1.0.0'};
  const steps=[{phase:'requested',retained:v1},{phase:'verified',retained:v1},{phase:'staged',retained:v1},{phase:'checked',retained:v1},
    {phase:'installed',retired:[v1]},{phase:'installed'}];
  let updState={plan:updPlan,approved:true,current_catalog:true,ready:false,configuration:{},components:[{id:'opencode-official',effect:{phase:'pending',retained:v1}}]};
  const updCalls=[],rendered=[];
  const updContext={document:{getElementById:updGet,querySelectorAll:selector=>selector.includes(':checked')?updChoices.filter(x=>x.checked):updChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{updCalls.push(path);if(path==='/setup/other-copies')return{ok:true,json:async()=>({copies:[]})};if(path==='/setup/state')return{ok:true,json:async()=>updState};if(path==='/setup/plan')return{ok:true,json:async()=>updPlan};
      if(path==='/setup/advance'){const next=steps.shift();updState={...updState,components:[{id:'opencode-official',effect:next,transfer:next.phase==='requested'?{state:'complete',done:100,total:100}:undefined}]};const row=text(updGet('setup-progress-list'));rendered.push(row);return{ok:true,json:async()=>updState}}
      if(path==='/setup/recover'){updState={...updState,components:[{id:'opencode-official',effect:{phase:'failed',retained:v1}}]};return{ok:true,json:async()=>updState}}
      throw Error('unexpected update request '+path)}};
  vm.runInNewContext(source(),updContext);
  await settled();
  await updGet('setup-review').onclick();
  assert(text(updGet('setup-summary')).includes('Replaces OpenCode 1.0.0 with 2.0.0'),'the screen names the installed version it replaces: '+text(updGet('setup-summary')));
  assert(text(updGet('setup-summary')).includes('stays in use until its replacement passes its check'));
  await updContext.setupRefreshState();
  assert.strictEqual(updCalls.filter(p=>p==='/setup/advance').length,6,'every update step and the removal ran without a reload: '+updCalls.join(','));
  assert.deepStrictEqual(updState.components[0].effect,{phase:'installed'},'the earlier version was removed automatically');
  assert(rendered.some(row=>row.includes('New version unpacked beside the one in use')),'the staged row is named');
  assert(rendered.some(row=>row.includes('New version checked; switching to it next')),'the checked row is named');
  assert(rendered.some(row=>row.includes('Version 1.0.0 stays in use')),'the retained version is named');
  assert(rendered.some(row=>row.includes('The earlier version is removed next')),'the pending removal is named');

  // An earlier release's unfinished step offers one explicit settle action.
  updState={plan:{...updPlan,id:'older-plan',preparable:false},approved:false,current_catalog:false,ready:false,configuration:{},components:[{id:'opencode-official',effect:{phase:'staged',retained:v1}}]};
  updCalls.length=0;
  await updContext.setupRefreshState();
  assert(!updCalls.includes('/setup/advance'),'an earlier plan is never advanced automatically');
  const settle=findText(updGet('setup-progress-list').children[0],'Settle this unfinished step');
  assert(settle,'an earlier plan\'s staged step offers Settle');
  await settle.onclick();
  assert(updCalls.includes('/setup/recover'),'Settle calls the explicit recovery');
  assert.strictEqual(updState.components[0].effect.phase,'failed');
  // Offered picture models: each is added after the starter model with its own
  // consent. Models' per-app rows choose the picture model, not Get started.
  const picFields=new Map(),picGet=id=>{if(!picFields.has(id))picFields.set(id,node());return picFields.get(id)};
  const picCalls=[];
  const zPlan={id:'z-plan',model:'user.Z-Image-Turbo',name:'Z-Image-Turbo',source:'https://huggingface.co/Tongyi-MAI/Z-Image-Turbo',licenses:['Apache-2.0'],license_text:'Tongyi-MAI publishes Z-Image-Turbo.',download_bytes:20690152836,peak_bytes:64217942124,assets:[{id:'main'},{id:'encoder'},{id:'vae'}]};
  const starterReady={approved:true,provider_configured:true,ready:true,assets:[]};
  let picReply={enabled:true,available:true,setup_plan_id:'approved-plan',plan:{id:'model-plan',model:'user.LocalApprovedKlein',assets:[]},choices:[zPlan],disk:{enough:true},feature:{installed:true},status:starterReady,choice_status:{'z-plan':{approved:false,assets:[]}}};
  let picture={models:[{id:'user.LocalApprovedKlein',name:'FLUX.2 [klein] 4B'},{id:'user.Z-Image-Turbo',name:'Z-Image-Turbo'}],current:'user.LocalApprovedKlein'};
  const picContext={document:{getElementById:picGet,querySelectorAll:()=>[],createElement:tag=>node(tag)},location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{const body=options&&options.body?JSON.parse(options.body):null;picCalls.push([path,body]);
      if(path==='/setup/model')return{ok:true,json:async()=>picReply};
      if(path==='/setup/model/start'){picReply={...picReply,choice_status:{'z-plan':{approved:true,assets:[{id:'main',phase:'requested'},{id:'encoder',phase:'verified'},{id:'vae',phase:'requested'}]}}};return{ok:true,json:async()=>picReply}}
      if(path==='/setup/model/advance')return{ok:true,json:async()=>picReply};
      if(path==='/setup/model/picture'){if(body)picture={...picture,current:body.model,message:'ComfyUI now draws with Z-Image-Turbo.'};return{ok:true,json:async()=>picture}}
      if(path==='/setup/state')return{ok:true,json:async()=>({plan:{id:''},components:[],configuration:{},approved:false})};
      if(path==='/setup/feature')return{ok:true,json:async()=>({available:true,installed:true})};
      return{ok:true,json:async()=>({})}}};
  vm.runInNewContext(source(),picContext);
  picContext.setupRenderModel(picReply);
  const findByText=(n,t)=>n.textContent===t?n:n.children.map(c=>findByText(c,t)).find(Boolean);
  const add=findByText(picGet('setup-model-choices'),'Add Z-Image-Turbo');
  assert(add&&add.disabled,'an offered model has its own Add button, disabled until consent');
  const consentBox=(function find(n){return n.type==='checkbox'?n:n.children.map(find).find(Boolean)})(picGet('setup-model-choices'));
  assert(consentBox,'an offered model has its own consent checkbox');
  consentBox.checked=true;consentBox.onchange();
  assert.strictEqual(add.disabled,false,'consent enables the Add button');
  await add.onclick();
  const started=picCalls.find(c=>c[0]==='/setup/model/start');
  assert(started&&started[1].plan_id==='z-plan'&&started[1].confirm===true,'Add starts exactly the offered plan: '+JSON.stringify(started));
  assert(text(picGet('setup-model-choices')).includes('Adding Z-Image-Turbo.'),'Add writes one sentence: '+text(picGet('setup-model-choices')));
  assert(text(picGet('setup-model-choices')).includes('encoder: verified'),'an approved offered model shows its files');
  const page=fs.readFileSync(__dirname+'/setup_page.go','utf8');
  assert(!page.includes('setup-picture')&&!page.includes('/setup/model/picture'),'Get started writes no picture policy; Models owns that choice');
  assert(!page.includes('setup copy'),'consent names the download and private setup');
  assert(!picCalls.some(c=>c[0]==='/setup/model/picture'),'the picture policy is never read or written here');
  // OpenCode's chat model comes from the Models page's selection chooser and
  // its rights from the first-use prompt (VISION 2026-10-06): Setup has no
  // OpenCode chat step, and a plan saved with the retired row calls nothing.
  assert(!page.includes('/setup/opencode/chat')&&!page.includes('id="setup-opencode"'),'Setup has no OpenCode chat step');
  const ocCalls=[],ocFields=new Map(),ocGet=id=>{if(!ocFields.has(id))ocFields.set(id,node());return ocFields.get(id)};
  const ocState={plan:{id:'oc-plan',selected:['opencode'],components:[],configuration:[{id:'opencode-chat-provider',title:'Choose a chat model or provider for OpenCode'}]},components:[],configuration:{'opencode-chat-provider':false},approved:true,current_catalog:true};
  const ocContext={document:{getElementById:ocGet,querySelectorAll:()=>[],createElement:tag=>node(tag)},location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,oaGrant:()=>{},
    fetch:async(path)=>{ocCalls.push(path);if(path==='/setup/state')return{ok:true,json:async()=>ocState};return{ok:true,json:async()=>({})}}};
  vm.runInNewContext(source(),ocContext);
  for(let i=0;i<5;i++)await new Promise(r=>setImmediate(r));
  assert(ocCalls.includes('/setup/state')&&!ocCalls.some(p=>p.startsWith('/setup/opencode')),'a saved OpenCode plan calls no OpenCode step');

  // An unmarked ComfyUI keeps OA's build out of the plan until the person
  // ticks its box; the page shows where the copy is (VISION 2026-10-02).
  const copyFields=new Map(),copyChoices=[{value:'comfyui',checked:true},{value:'opencode',checked:false},{value:'lemonade',checked:false}];
  const copyGet=id=>{if(!copyFields.has(id))copyFields.set(id,node());return copyFields.get(id)};
  const copyRequests=[];const copyLocation='C:\\Users\\person\\Desktop\\ComfyUI_windows_portable';
  const copyContext={document:{getElementById:copyGet,querySelectorAll:selector=>selector.includes(':checked')?copyChoices.filter(x=>x.checked):copyChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{copyRequests.push({path,body:options&&options.body?JSON.parse(options.body):null});if(path==='/setup/other-copies')return {ok:true,json:async()=>({copies:[{app:'comfyui',title:'ComfyUI',location:copyLocation,source:'folder'}]})};if(path==='/setup/state')return {ok:false,text:async()=>'404 no saved setup'};if(path==='/setup/plan'){const body=JSON.parse(options.body);return {ok:true,json:async()=>({...plan,id:'plan-beside',selected:body.selected,other_copies:[{app:'comfyui',title:'ComfyUI',location:copyLocation}]})}}return {ok:false,text:async()=>'unexpected request '+path}}};
  withSection(copyGet);
  vm.runInNewContext(source(),copyContext);
  await copyContext.setupLoadOtherCopies();
  // The copy is one row in the Apps group: its switch, captioned with what
  // Setup found; no loose sentence between groups (S7 follow-up).
  const copyRow=copyGet('setup-choices').children[0];
  assert.strictEqual(copyGet('setup-choices').children.length,1,'one row per app, straight in the Apps group');
  assert.strictEqual(copyRow.className,'oa-row');
  assert.strictEqual(copyRow.children[0].children[0].textContent,'Also install the OpenAbstractions build of ComfyUI');
  assert.strictEqual(copyRow.children[0].children[1].className,'oa-row-caption');
  assert.strictEqual(copyRow.children[0].children[1].textContent,'ComfyUI is already installed; Setup leaves it as it is.','the page says another copy is installed');
  assert(!text(copyGet('setup-choices')).includes(copyLocation),'the copy\'s folder is no page text (Panel rule: no path)');
  assert.strictEqual(copyRow.children[0].children[1].title,copyLocation,'the folder stays in the caption\'s title');
  await copyGet('setup-review').onclick();
  assert(!copyRequests.some(r=>r.path==='/setup/plan'),'an unmarked copy keeps OA\'s build out of the plan by default');
  assert(text(copyGet('setup-status')).includes('already installed'));
  const beside=besideBoxes(copyGet('setup-choices'))[0];
  assert.strictEqual(beside.checked,false,'installing OA\'s build beside it starts unticked');
  beside.checked=true;beside.onchange();
  await copyGet('setup-review').onclick();
  const besidePlan=copyRequests.filter(r=>r.path==='/setup/plan');
  assert.deepStrictEqual(besidePlan.map(r=>r.body),[{selected:['comfyui'],beside:['comfyui']}],'the explicit tick installs OA\'s build beside the copy');
  assert(text(copyGet('setup-summary')).includes('Left as it is: ComfyUI.'),'the reviewed plan names the copy it leaves');
  assertPanelRules(['setup-choices','setup-summary'].map(copyGet),'another copy and the review');
  assert(!text(copyGet('setup-summary')).includes(copyLocation),'the reviewed plan shows no folder path');

  // An incomplete scan asks for an explicit Beside choice for every selected
  // app OA builds, including apps for which it found no copy. OpenCode comes
  // from its official installer and has no Beside choice.
  const incompleteFields=new Map(),incompleteChoices=[{value:'comfyui',checked:true},{value:'opencode',checked:true},{value:'lemonade',checked:false}];
  const incompleteGet=id=>{if(!incompleteFields.has(id))incompleteFields.set(id,node());return incompleteFields.get(id)};
  const incompleteRequests=[];let blockPlan=false,releasePlan;
  const incompleteContext={document:{getElementById:incompleteGet,querySelectorAll:selector=>selector.includes(':checked')?incompleteChoices.filter(x=>x.checked):incompleteChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{incompleteRequests.push({path,body:options&&options.body?JSON.parse(options.body):null});if(path==='/setup/other-copies')return {ok:true,json:async()=>({copies:[],incomplete:true})};if(path==='/setup/state')return {ok:false,text:async()=>'404 no saved setup'};if(path==='/setup/plan'){const body=JSON.parse(options.body);if(blockPlan)return new Promise(resolve=>{releasePlan=()=>resolve({ok:true,json:async()=>({...plan,id:'plan-incomplete-stale',selected:body.selected})})});return {ok:true,json:async()=>({...plan,id:'plan-incomplete',selected:body.selected})}}return {ok:false,text:async()=>'unexpected request '+path}}};
  withSection(incompleteGet);
  vm.runInNewContext(source(),incompleteContext);
  await incompleteContext.setupLoadOtherCopies();
  const incompleteBox=app=>besideBoxes(incompleteGet('setup-choices')).find(box=>box.value===app);
  assert(incompleteBox('comfyui'),'the incomplete scan offers a choice with no discovered copies');
  assert(text(incompleteGet('setup-status')).includes('could not finish checking for other copies'));
  assert.strictEqual(incompleteBox('comfyui').checked,false);
  assert.strictEqual(incompleteBox('opencode'),undefined,'no Beside choice for OpenCode, which OA does not build');
  assert(!besideBoxes(incompleteGet('setup-choices')).some(box=>box.value==='lemonade'),'incomplete scan offers Beside only for ComfyUI');
  await incompleteGet('setup-review').onclick();
  assert.deepStrictEqual(incompleteRequests.filter(r=>r.path==='/setup/plan').map(r=>r.body),[{selected:['opencode']}],'ComfyUI stays out of the plan until Beside is explicitly chosen after an incomplete scan; OpenCode needs no Beside choice');
  incompleteBox('comfyui').checked=true;incompleteBox('comfyui').onchange();
  await incompleteGet('setup-review').onclick();
  assert.deepStrictEqual(incompleteRequests.filter(r=>r.path==='/setup/plan').map(r=>r.body).slice(1),[{selected:['comfyui','opencode'],beside:['comfyui']}],'the incomplete scan choices reach the reviewed plan');

  // Changing Beside while a plan is in flight invalidates its key and keeps
  // the stale response from restoring a review.
  blockPlan=true;
  const staleReview=incompleteGet('setup-review').onclick();
  await settled();
  incompleteBox('comfyui').checked=false;incompleteBox('comfyui').onchange();
  releasePlan();
  await staleReview;
  assert.strictEqual(incompleteGet('setup-plan').hidden,true,'a plan response for the former Beside choice is discarded');
  assert.strictEqual(incompleteGet('setup-install').disabled,true,'the stale plan cannot re-enable Install');

  // Lemonade does not use the ComfyUI Beside decision. An incomplete
  // scan therefore must not block a Lemonade-only plan or render a Beside box.
  const lemonadeFields=new Map(),lemonadeChoices=[{value:'comfyui',checked:false},{value:'opencode',checked:false},{value:'lemonade',checked:true}];
  const lemonadeGet=id=>{if(!lemonadeFields.has(id))lemonadeFields.set(id,node());return lemonadeFields.get(id)};
  const lemonadeRequests=[];
  const lemonadeContext={document:{getElementById:lemonadeGet,querySelectorAll:selector=>selector.includes(':checked')?lemonadeChoices.filter(x=>x.checked):lemonadeChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{lemonadeRequests.push({path,body:options&&options.body?JSON.parse(options.body):null});if(path==='/setup/other-copies')return {ok:true,json:async()=>({copies:[],incomplete:true})};if(path==='/setup/state')return {ok:false,text:async()=>'404 no saved setup'};if(path==='/setup/plan'){const body=JSON.parse(options.body);return {ok:true,json:async()=>({...plan,id:'plan-lemonade-incomplete',selected:body.selected})}}return {ok:false,text:async()=>'unexpected request '+path}}};
  withSection(lemonadeGet);
  vm.runInNewContext(source(),lemonadeContext);
  await lemonadeContext.setupLoadOtherCopies();
  assert(!besideBoxes(lemonadeGet('setup-choices')).length,'Lemonade-only selection has no meaningless Beside choice');
  await lemonadeGet('setup-review').onclick();
  assert.deepStrictEqual(lemonadeRequests.filter(r=>r.path==='/setup/plan').map(r=>r.body),[{selected:['lemonade']}],'an incomplete optional-app scan does not hold a Lemonade-only plan');

  // A failed scan request is also incomplete. An OpenCode-only plan has no
  // Beside decision (OpenCode comes from its official installer), so the
  // failed scan holds nothing.
  const failedScanFields=new Map(),failedScanChoices=[{value:'comfyui',checked:false},{value:'opencode',checked:true},{value:'lemonade',checked:false}];
  const failedScanGet=id=>{if(!failedScanFields.has(id))failedScanFields.set(id,node());return failedScanFields.get(id)};
  const failedScanRequests=[];
  const failedScanContext={document:{getElementById:failedScanGet,querySelectorAll:selector=>selector.includes(':checked')?failedScanChoices.filter(x=>x.checked):failedScanChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{failedScanRequests.push({path,body:options&&options.body?JSON.parse(options.body):null});if(path==='/setup/other-copies')throw new Error('scan unavailable');if(path==='/setup/state')return {ok:false,text:async()=>'404 no saved setup'};if(path==='/setup/plan'){const body=JSON.parse(options.body);return {ok:true,json:async()=>({...plan,id:'plan-after-scan-failure',selected:body.selected})}}return {ok:false,text:async()=>'unexpected request '+path}}};
  withSection(failedScanGet);
  vm.runInNewContext(source(),failedScanContext);
  await failedScanContext.setupLoadOtherCopies();
  assert(text(failedScanGet('setup-status')).includes('could not finish checking'),'a failed copy scan is shown as incomplete');
  assert(!besideBoxes(failedScanGet('setup-choices')).length,'an OpenCode-only selection has no Beside choice');
  await failedScanGet('setup-review').onclick();
  assert.deepStrictEqual(failedScanRequests.filter(r=>r.path==='/setup/plan').map(r=>r.body),[{selected:['opencode']}],'a failed copy scan does not hold an OpenCode-only plan');

  // The initial scan itself is a prerequisite: no plan or install can race
  // ahead of a late discovered-copy or incomplete-scan result.
  const delayedScanFields=new Map(),delayedScanChoices=[{value:'comfyui',checked:true},{value:'opencode',checked:true},{value:'lemonade',checked:false}];
  const delayedScanGet=id=>{if(!delayedScanFields.has(id))delayedScanFields.set(id,node());return delayedScanFields.get(id)};
  let releaseDelayedScan;const delayedScanRequests=[];
  const delayedSavedPlan={...plan,id:'approved-saved-plan',selected:['comfyui','opencode'],preparable:true,components:[{id:'comfyui-app',dependencies:[]}]};
  const delayedSavedState={plan:delayedSavedPlan,current_catalog:true,approved:true,ready:false,configuration:{},components:[{id:'comfyui-app',effect:{phase:'pending'}}]};
  const delayedScanContext={document:{getElementById:delayedScanGet,querySelectorAll:selector=>selector.includes(':checked')?delayedScanChoices.filter(x=>x.checked):delayedScanChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{delayedScanRequests.push({path,body:options&&options.body?JSON.parse(options.body):null});if(path==='/setup/other-copies')return new Promise(resolve=>{releaseDelayedScan=()=>resolve({ok:true,json:async()=>({copies:[],incomplete:true})})});if(path==='/setup/state')return{ok:true,json:async()=>delayedSavedState};if(path==='/setup/plan'){const body=JSON.parse(options.body);return{ok:true,json:async()=>({...delayedSavedPlan,id:'delayed-scan-plan',selected:body.selected})}}if(path==='/setup/install')return{ok:true,json:async()=>({})};if(path==='/setup/advance')return{ok:true,json:async()=>delayedSavedState};return{ok:false,text:async()=>'unexpected request '+path}}};
  withSection(delayedScanGet);
  vm.runInNewContext(source(),delayedScanContext);
  await settled();
  assert(text(delayedScanGet('setup-status')).startsWith('Saved setup plan restored.'),'the restored plan has the Review row\'s one status sentence');
  delayedScanGet('setup-review').disabled=true; // The DOM stub does not parse the markup's initial disabled attribute.
  assert.strictEqual(delayedScanGet('setup-review').disabled,true,'Review stays disabled until the first other-copy scan settles');
  await delayedScanGet('setup-review').onclick();
  await delayedScanGet('setup-install').onclick();
  assert(!delayedScanRequests.some(r=>r.path==='/setup/plan'||r.path==='/setup/install'||r.path==='/setup/advance'),'neither plan nor install/resume effects are submitted while the scan is pending');
  assert(text(delayedScanGet('setup-status')).includes('Setup is still checking'),'a click during the check says why nothing happened');
  releaseDelayedScan();await settled();
  assert(!delayedScanRequests.some(r=>r.path==='/setup/install'||r.path==='/setup/advance'),'an approved saved plan also waits for the fresh incomplete-scan decision');
  assert.strictEqual(delayedScanGet('setup-review').disabled,false,'Review unlocks after the scan resolves with an incomplete result');
  const delayedBeside=app=>besideBoxes(delayedScanGet('setup-choices')).find(box=>box.value===app);
  delayedBeside('comfyui').checked=true;delayedBeside('comfyui').onchange();
  await delayedScanGet('setup-review').onclick();
  assert.deepStrictEqual(delayedScanRequests.filter(r=>r.path==='/setup/plan').map(r=>r.body),[{selected:['comfyui','opencode'],beside:['comfyui']}],'the resolved scan choices are carried into the plan');

  // If the incomplete scan resolves before a saved plan, restore must render
  // Beside choices for the restored apps and discard the non-persisted choice.
  const restoredRaceFields=new Map(),restoredRaceChoices=[{value:'comfyui',checked:false},{value:'opencode',checked:false},{value:'lemonade',checked:false}];
  const restoredRaceGet=id=>{if(!restoredRaceFields.has(id))restoredRaceFields.set(id,node());return restoredRaceFields.get(id)};
  let releaseSavedState;const restoredRaceRequests=[];
  const savedPlan={...plan,id:'saved-plan',selected:['comfyui','opencode'],preparable:true,configuration:[]};
  const resumeState={plan:savedPlan,current_catalog:true,approved:false,ready:false,configuration:{},components:[]};
  const resumeContext={document:{getElementById:restoredRaceGet,querySelectorAll:selector=>selector.includes(':checked')?restoredRaceChoices.filter(x=>x.checked):restoredRaceChoices,createElement:tag=>node(tag)},
    location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{restoredRaceRequests.push({path,body:options&&options.body?JSON.parse(options.body):null});if(path==='/setup/other-copies')return{ok:true,json:async()=>({copies:[],incomplete:true})};if(path==='/setup/state')return new Promise(resolve=>{releaseSavedState=()=>resolve({ok:true,json:async()=>resumeState})});if(path==='/setup/plan'){const body=JSON.parse(options.body);return{ok:true,json:async()=>({...savedPlan,id:'fresh-plan',selected:body.selected})}}return{ok:false,text:async()=>'unexpected request '+path}}};
  withSection(restoredRaceGet);
  vm.runInNewContext(source(),resumeContext);
  await settled();
  assert(text(restoredRaceGet('setup-status')).includes('could not finish checking'),'the first incomplete scan result is visible while restore is pending');
  releaseSavedState();
  await settled();
  const resumeBeside=app=>besideBoxes(restoredRaceGet('setup-choices')).find(box=>box.value===app);
  assert.strictEqual(resumeBeside('comfyui').checked,false,'Beside is not restored from the saved plan');
  assert.strictEqual(resumeBeside('opencode'),undefined,'OpenCode, which OA does not build, gets no Beside choice');
  assert.strictEqual(restoredRaceGet('setup-install').disabled,true,'a restored preparable plan is not install-enabled without fresh review');
  await restoredRaceGet('setup-install').onclick();
  assert(!restoredRaceRequests.some(r=>r.path==='/setup/install'),'clicking the stale saved plan cannot install it');
  resumeBeside('comfyui').checked=true;resumeBeside('comfyui').onchange();
  assert.strictEqual(restoredRaceGet('setup-install').disabled,true,'choosing Beside still requires a fresh catalog review');
  await restoredRaceGet('setup-review').onclick();
  assert.deepStrictEqual(restoredRaceRequests.filter(r=>r.path==='/setup/plan').map(r=>r.body),[{selected:['comfyui','opencode'],beside:['comfyui']}],'fresh review includes the newly confirmed Beside choices');
  assert.strictEqual(restoredRaceGet('setup-install').disabled,false,'only the fresh reviewed plan enables Install');

  // Chat engine recovery (#setup-modelhost): Models › Add a model links here
  // for the pinned ZIP. The digest, the destination folder and the approval
  // text stay in Technical details; staging needs the exact file and its own
  // consent, and writes one sentence without a folder.
  const mhFields=new Map(),mhGet=id=>{if(!mhFields.has(id))mhFields.set(id,node());return mhFields.get(id)};
  const mhRequests=[],mhListeners={},mhLocation={search:'?k=panel-key',hash:''};let mhStaged=false;
  const mhPin={release:'b10825',archive:'llama-b10825-bin-win-vulkan-x64.zip',size:123456,sha256:'a'.repeat(64),url:'https://github.com/ggml-org/llama.cpp/releases/download/b10825/llama-b10825-bin-win-vulkan-x64.zip',engine_dir:'C:\\ProgramData\\OpenAbstractions\\engines\\llama.cpp\\b10825'};
  const mhState=()=>({plan_id:'setup-plan',available:true,feature:{available:true,installed:true},pin:mhPin,engine:{staged:mhStaged,path:mhStaged?mhPin.engine_dir:'',approval:'Stage the reviewed llama.cpp files at C:\\ProgramData\\OpenAbstractions\\engines.',approval_digest:'engine-approval'}});
  class MemoryFormData{constructor(){this.fields=new Map()}set(key,value,filename){this.fields.set(key,{value,filename})}get(key){const entry=this.fields.get(key);return entry&&entry.value}getEntry(key){return this.fields.get(key)}}
  const mhContext={document:{getElementById:mhGet,querySelectorAll:()=>[],createElement:tag=>node(tag)},location:mhLocation,URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,FormData:MemoryFormData,addEventListener:(name,fn)=>{mhListeners[name]=fn},
    fetch:async(path,options)=>{mhRequests.push(path);if(path==='/setup/other-copies')return{ok:true,json:async()=>({copies:[]})};if(path==='/setup/state')return{ok:false,text:async()=>'404 no saved setup'};if(path==='/setup/modelhost/chat')return{ok:true,json:async()=>mhState()};
      if(path==='/setup/modelhost/engine'){const form=options.body;assert.strictEqual(options.headers['X-Panel-Key'],'panel-key');assert.strictEqual(options.headers['Content-Type'],undefined,'multipart upload leaves the browser boundary header unset');assert.deepStrictEqual([...form.fields.keys()],['plan_id','pin_digest','approval_digest','confirm','archive']);assert.strictEqual(form.get('plan_id'),'setup-plan');assert.strictEqual(form.get('pin_digest'),mhPin.sha256);assert.strictEqual(form.get('approval_digest'),'engine-approval');assert.strictEqual(form.get('confirm'),'true');assert.strictEqual(form.getEntry('archive').filename,mhPin.archive);mhStaged=true;return{ok:true,json:async()=>({staged:true,path:mhPin.engine_dir})}}
      throw Error('unexpected request '+path)}};
  vm.runInNewContext(source(),mhContext);
  await settled();
  assert.strictEqual(mhGet('setup-modelhost').hidden,true,'the recovery stays out of Get started unless Models links to it');
  assert(!mhRequests.includes('/setup/modelhost/chat'),'a hidden recovery reads nothing');
  mhLocation.hash='#setup-modelhost';mhListeners.hashchange();await settled();
  assert.strictEqual(mhGet('setup-modelhost').hidden,false,'the #setup-modelhost link shows the recovery');
  assert(text(mhGet('setup-modelhost-pin-caption')).startsWith('llama.cpp b10825 · '),text(mhGet('setup-modelhost-pin-caption')));
  assertPanelRules(['setup-modelhost-pin-caption','setup-modelhost-engine-status','setup-modelhost-pin'].map(mhGet),'the recovery');
  const mhDetails=mhGet('setup-modelhost-pin').children[0];
  assert.strictEqual(mhDetails.tagName,'DETAILS','the pin sits in a disclosure');
  assert.strictEqual(mhDetails.children[0].textContent,'Technical details');
  assert.strictEqual(mhDetails.className,'oa-expander','the disclosure is oaExpander\'s');
  assert.strictEqual(mhDetails.children[0].children[0].className,'oa-row-chevron','its summary carries the row chevron');
  assert.strictEqual(mhDetails.children[1].className,'oa-expander-body');
  for(const fact of ['SHA-256: '+mhPin.sha256,'Destination: '+mhPin.engine_dir,'Approval: Stage the reviewed llama.cpp files','Archive: '+mhPin.archive])assert(text(mhDetails).includes(fact),'Technical details hold '+fact);
  assert.strictEqual(findHref(mhDetails),mhPin.url,'only the pinned publisher URL is linked');
  const mhArchive=mhGet('setup-modelhost-archive');
  mhArchive.files=[{name:'other-engine.zip',size:mhPin.size}];mhArchive.onchange();
  assert.strictEqual(mhGet('setup-modelhost-engine-stage').disabled,true,'a different archive cannot be staged');
  mhArchive.files=[{name:mhPin.archive,size:mhPin.size}];mhArchive.onchange();
  assert.strictEqual(mhGet('setup-modelhost-engine-stage').disabled,true,'staging needs its own consent');
  mhGet('setup-modelhost-engine-consent').checked=true;mhGet('setup-modelhost-engine-consent').onchange();
  assert.strictEqual(mhGet('setup-modelhost-engine-stage').disabled,false,'the exact pinned file plus consent enables staging');
  await mhGet('setup-modelhost-engine-stage').onclick();
  assert.strictEqual(text(mhGet('setup-modelhost-engine-status')),'The pinned engine is staged.','Stage writes one sentence with no folder');
  assert(text(mhGet('setup-modelhost-pin')).includes('Staged at: '+mhPin.engine_dir),'the staged folder is a technical detail');
  assert.strictEqual(mhGet('setup-modelhost-engine-stage').hidden,true,'a staged engine offers no second staging');
  assertPanelRules(['setup-modelhost-pin-caption','setup-modelhost-engine-status','setup-modelhost-pin'].map(mhGet),'the staged recovery');
  assert(!mhRequests.some(r=>/model\/start|engine\/start|generate|modelhost\/(host|root|feature)/.test(r)),'the recovery stages the engine and nothing else: '+mhRequests.join(', '));
  // Engine package approval does not approve its model. Every later action
  // carries the fixed engine plus both reviewed identities, and a delayed
  // model view cannot replace the newly selected engine's review.
  for (const engine of ['thenoise','pytorch']) {
  const tnFields=new Map(),tnGet=id=>{if(!tnFields.has(id))tnFields.set(id,node());return tnFields.get(id)};
  const tnRequests=[];let releaseOldModel,tnApproved=false,tnInstalled=true,tnReady=false;
  const tnPlan={id:'tn-model',engine:engine,model:engine==='thenoise'?'flux_klein':'flux2-klein-4b',repository:'Comfy-Org/model',licenses:['Apache-2.0'],download_bytes:123,peak_bytes:456,assets:[{id:'main',license:'Apache-2.0',download:{label:'Main weights',bytes:123}}]};
  const tnReply=()=>({engine:engine,engine_installed:tnInstalled,enabled:true,available:true,setup_plan_id:'tn-package',plan:tnPlan,feature:{installed:true},disk:{enough:true},status:{approved:tnApproved,assets:[{id:'main',phase:tnApproved?'requested':'pending'}]}});
  const tnContext={document:{getElementById:tnGet,querySelectorAll:()=>[],createElement:tag=>node(tag)},location:{search:'?k=panel-key'},URLSearchParams,setTimeout:()=>1,clearTimeout:()=>{},console,
    fetch:async(path,options)=>{const body=options&&options.body?JSON.parse(options.body):null;tnRequests.push({path,body});if(path==='/setup/other-copies')return{ok:true,json:async()=>({copies:[]})};if(path==='/setup/state')return tnReady?{ok:true,json:async()=>({approved:true,plan:{id:'tn-package',selected:[engine]},components:[]})}:{ok:false,text:async()=>'no saved setup'};if(path==='/setup/feature?engine='+engine)return{ok:true,json:async()=>({installed:true,available:true})};if(path==='/setup/model')return new Promise(resolve=>{releaseOldModel=()=>resolve({ok:true,json:async()=>({...tnReply(),engine:'lemonade',plan:{...tnPlan,id:'old-model',model:'old Lemonade model'}})})});if(path==='/setup/model?engine='+engine)return{ok:true,json:async()=>tnReply()};if(path==='/setup/model/start'){assert.deepStrictEqual(body,{plan_id:'tn-model',confirm:true,setup_plan_id:'tn-package',engine:engine});tnApproved=true;return{ok:true,json:async()=>tnReply()}}if(path==='/setup/engine/remove'){assert.deepStrictEqual(body,{engine:engine,plan_id:'tn-package',confirm:true});tnInstalled=false;return{ok:true,json:async()=>({})}}return{ok:false,text:async()=>'unexpected '+path}}};
  vm.runInNewContext(source(),tnContext);await settled();
  const oldModel=vm.runInNewContext('setupRefreshModel()',tnContext);await settled();
  tnGet('setup-engine-remove-consent').checked=true;
  tnReady=true;tnGet('setup-engine').value=engine;await tnGet('setup-engine').onchange();
  assert.strictEqual(tnGet('setup-engine-remove-consent').checked,false,'engine switch clears removal approval');
  await tnGet('setup-engine-remove').onclick();assert(!tnRequests.some(r=>r.path==='/setup/engine/remove'),'switched engine requires fresh removal consent');
  releaseOldModel();await oldModel;
  assert(text(tnGet('setup-model-review')).includes(tnPlan.model)&&!text(tnGet('setup-model-review')).includes('old Lemonade model'),'stale model reply cannot change the selected engine review');
  assert(!tnRequests.some(r=>r.body),'package state and model review submit no model or native activation');
  tnGet('setup-engine-remove-consent').checked=true;
  vm.runInNewContext("setupRenderModel({...modelState,setup_plan_id:'replacement-package'})",tnContext);
  assert.strictEqual(tnGet('setup-engine-remove-consent').checked,false,'changed package review clears removal approval');
  await tnGet('setup-engine-remove').onclick();assert(!tnRequests.some(r=>r.path==='/setup/engine/remove'),'changed package requires fresh removal consent');
  vm.runInNewContext("setupRenderModel({...modelState,setup_plan_id:'tn-package'})",tnContext);
  await tnGet('setup-model-approve').onclick();assert(!tnRequests.some(r=>r.path==='/setup/model/start'),'model consent is separate from package approval');
  tnGet('setup-model-consent').checked=true;tnGet('setup-model-consent').onchange();await tnGet('setup-model-approve').onclick();
  assert(tnRequests.some(r=>r.path==='/setup/model/start'),'fixed model consent permits model setup');
  await tnGet('setup-engine-remove').onclick();assert(!tnRequests.some(r=>r.path==='/setup/engine/remove'),'engine removal has its own explicit consent');
  tnGet('setup-engine-remove-consent').checked=true;tnGet('setup-engine-remove-consent').onchange();await tnGet('setup-engine-remove').onclick();
  assert(tnRequests.some(r=>r.path==='/setup/engine/remove'),'owned engine removal uses its fixed package identity');
  assert.strictEqual(tnGet('setup-engine-remove-consent').checked,false,'completed removal clears its one-time consent');
  }
  process.stdout.write('setup page catalog review behavior passed\n');
})().catch(e=>{ console.error(e); process.exitCode=1; });
