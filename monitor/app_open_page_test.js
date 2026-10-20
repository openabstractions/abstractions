// Run with node monitor/app_open_page_test.js. Open reaches an app in one
// press from the Apps list and from Setup's Open ComfyUI: the Apps row of an
// app with an activation recipe carries Open, which opens the app's page
// with open=1, and the app page opens the app once on arrival with that flag
// and drops it from the address. No browser or service starts.
const fs=require('fs'),vm=require('./panel_record_test_helper.js').vm,assert=require('assert');
const {assertPanelRules,}=require('./static/panel_rules.js');
const {allowlistedClasses}=require('./panel_fake_dom_test_helper.js');
function constant(file,name){const s=fs.readFileSync(__dirname+'/'+file,'utf8'),m='const '+name+' = `',i=s.indexOf(m)+m.length;assert(i>=m.length,name);return s.slice(i,s.indexOf('`',i))}
const plain=constant('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0];
function fakes(){
 const fields=new Map();
 function node(tag='div'){return {tag,tagName:tag.toUpperCase(),value:'',textContent:'',className:'',hidden:false,disabled:false,dataset:{},attrs:{},children:[],replaceChildren(...v){this.children=[...v]},append(...v){this.children.push(...v)},setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return k in this.attrs?this.attrs[k]:null},hasAttribute(k){return k in this.attrs},removeAttribute(k){delete this.attrs[k]},addEventListener(){},removeEventListener(){}}}
 function el(id){if(!fields.has(id))fields.set(id,node(id==='app-open'||id==='app-remove'?'button':'div'));return fields.get(id)}
 return {node,el};
}
const settle=async()=>{for(let i=0;i<20;i++)await new Promise(r=>setImmediate(r))};

(async()=>{
 // The app page with open=1 opens the app once and drops the flag.
 {
  const {node,el}=fakes(),actions=[],replaced=[];
  el('app-page').dataset={name:'comfyui',title:'ComfyUI',shown:'digest-1',defaults:''};
  el('app-open').textContent='Open';el('app-open').className='oa-primary';
  const context={console,URLSearchParams,AbortController,Date,location:{search:'?k=key&open=1',pathname:'/app/comfyui'},history:{replaceState(s,t,url){replaced.push(url)}},
   setTimeout(){return 0},clearTimeout(){},addEventListener(){},
   document:{getElementById:el,createElement:node,documentElement:{dataset:{}},visibilityState:'visible',addEventListener(){}},
   fetch:async(path,opts={})=>{
    if(path==='/app/comfyui/facts')return {ok:true,json:async()=>({outcome:'page',rulesOutcome:'page',callsOutcome:'page',rules:[],calls:[],defaults:[],running:actions.length>0})};
    if(path==='/app/comfyui/start'){actions.push(JSON.parse(opts.body));return {ok:true,json:async()=>({outcome:'ready',message:'ComfyUI is open.'})}}
    return new Promise(()=>{});
   }};
  context.window=context;vm.createContext(context);
  vm.runInContext(plain,context);
  vm.runInContext(constant('service_page.go','serviceCommonScript')+constant('app_page.go','appPageScript'),context);
  await context.appOpenedOnArrival;await settle();
  assert.deepStrictEqual(actions,[{confirm:true,shown:'digest-1'}],'open=1 opens the app once');
  assert.deepStrictEqual(replaced,['/app/comfyui?k=key'],'the flag leaves the address, so a reload opens nothing');
  assert.equal(el('app-open-status').textContent,'ComfyUI is open.');
  assert(el('app-open').disabled&&el('app-open').textContent==='Running');
 }
 // An Apps row whose app has an activation recipe carries Open; a row
 // without one stays a plain link to its page.
 {
  const {node,el}=fakes();
  const registry={outcome:'page',cursor:'c1',entries:[
   {name:'comfyui',role:'application',title:'ComfyUI',application:{Descriptor:{Name:'comfyui',Title:'ComfyUI',Activation:{Verb:'open'}},Instances:[]}},
   {name:'notes',role:'application',title:'Notes',application:{Descriptor:{Name:'notes',Title:'Notes'},Instances:[]}}]};
  const location={search:'?k=key'};
  const context={console,URLSearchParams,AbortController,location,setTimeout(){return 0},clearTimeout(){},
   document:{getElementById:el,createElement:node,documentElement:{dataset:{}},visibilityState:'visible',addEventListener(){}},
   fetch:async(path)=>{
    if(path==='/runtime')return {ok:true,json:async()=>({capabilities:[]})};
    if(path==='/apps/pending')return {ok:true,json:async()=>({outcome:'page',entries:[]})};
    if(path.startsWith('/apps/registrations?')){const q=new URLSearchParams(path.split('?')[1]);if(q.get('wait_ms')!=='0')return new Promise(()=>{});return {ok:true,json:async()=>q.get('source')==='providers'?{outcome:'page',cursor:'p1',entries:[]}:registry}}
    throw Error('unexpected '+path);
   }};
  context.window=context;vm.createContext(context);
  vm.runInContext(plain,context);
  vm.runInContext(constant('service_page.go','serviceCommonScript')+constant('service_page.go','appsPageScript'),context);
  await context.appsArrived;
  const [comfy,notes]=el('apps').children;
  assert.equal(comfy.label.textContent,'ComfyUI');assert.equal(comfy.open.textContent,'Open');assert.equal(comfy.open.hidden,false);
  assert.equal(comfy.link.href,'/app/comfyui?k=key');
  comfy.open.onclick();
  assert.equal(location.href,'/app/comfyui?k=key&open=1','Open opens the app page, which opens the app');
  assert.equal(notes.open,undefined,'an app OpenAbstractions cannot open has no Open');assert.equal(notes.href,'/app/notes?k=key');
  // A running app shows Running in its caption and no second Open.
  registry.entries[0].application.Instances=[{}];context.renderApps();
  assert.strictEqual(el('apps').children[0],comfy,'the row stays the same element');assert.equal(comfy.open.hidden,true);assert.equal(comfy.caption.textContent,'Running');
  registry.entries[0].application.Instances=[];context.renderApps();
  // The section's status line, as oaStatus renders it.
  el('apps-status').tagName='P';el('apps-status').className='oa-status';el('apps-status').setAttribute('role','status');
  assertPanelRules({tagName:'MAIN',children:[{tagName:'SECTION',children:[el('apps-status'),el('apps')]}]},'Apps',{allowClasses:allowlistedClasses('status')});
  context.appsRun.abort();
 }
 console.log('PASS: Open from the Apps list and on arrival with open=1');
})().catch(e=>{console.error(e);process.exitCode=1});
