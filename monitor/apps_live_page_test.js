// Run with node monitor/apps_live_page_test.js. The Apps page while visible:
// Check again finds the fake runtime recovered and writes its sentence; the
// page's own timer checks again, and the sentence, the banner and the app
// rows are the same elements as before (the S5 review rule).
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const {assertPanelRules}=require('./static/panel_rules.js');
const {assertLiveUpdateKeepsRow,allowlistedClasses}=require('./panel_fake_dom_test_helper.js');
function constant(file,name){const s=fs.readFileSync(__dirname+'/'+file,'utf8'),m='const '+name+' = `',i=s.indexOf(m)+m.length;assert(i>=m.length,name);return s.slice(i,s.indexOf('`',i))}
const fields=new Map();
function node(tag='div'){return {tag,tagName:tag.toUpperCase(),textContent:'',className:'',hidden:false,attrs:{},children:[],replaceChildren(){this.children=[]},append(...v){this.children.push(...v)},setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return k in this.attrs?this.attrs[k]:null},removeAttribute(k){delete this.attrs[k]},addEventListener(){}}}
function el(id){if(!fields.has(id))fields.set(id,node(id==='apps-health-retry'?'button':'div'));return fields.get(id)}
let failing=true,checks=0;
const registry={outcome:'page',cursor:'c1',entries:[{name:'notes',role:'application',application:{Descriptor:{Name:'notes',Title:'Notes'},Instances:[]}}]};
const timers=[];
const context={console,URLSearchParams,AbortController,location:{search:'?k=key'},setTimeout(fn){timers.push(fn);return timers.length},clearTimeout(){},
 document:{getElementById:el,createElement:node,documentElement:{dataset:{}},visibilityState:'visible',addEventListener(){}},
 fetch:async(path,opts={})=>{
  if(path==='/runtime'){checks++;return {ok:true,json:async()=>failing?{capabilities:[{name:'Work submission',contract:'abstraction.job/acceptance@1',status:'unavailable'}]}:{capabilities:[]}}}
  if(path.startsWith('/apps/registrations?')){const q=new URLSearchParams(path.split('?')[1]);if(q.get('wait_ms')!=='0')return new Promise(()=>{});return {ok:true,json:async()=>registry}}
  throw Error('unexpected '+path);
 }};
context.window=context;vm.createContext(context);
vm.runInContext(constant('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0],context);
vm.runInContext(constant('service_page.go','serviceCommonScript')+constant('service_page.go','appsPageScript'),context);
(async()=>{
 await context.appsArrived;
 assert.equal(context.document.documentElement.dataset.ready,'1');
 assert.equal(el('apps-health').hidden,false,'a failing service shows the banner');
 const row=el('apps').children[0];assert.equal(row.label.textContent,'Notes');
 const timer=timers.find(fn=>String(fn).includes('appsCheckRuntime'));assert(timer,'the page checks again while visible');
 const before=checks;
 const {sentence}=await assertLiveUpdateKeepsRow({
  click:async()=>{failing=false;await el('apps-health-retry').onclick()},
  change:()=>{registry.entries[0].application.Instances=[{}]},data:()=>registry,
  tick:timer,row:()=>el('apps-health'),status:()=>el('apps-health-status'),label:'Apps, health banner'});
 assert(/everything works\.$/.test(sentence),'Check again writes one sentence');assert.equal(checks,before+2,'the click and the timer checked the runtime');
 assert.equal(el('apps-health-label').textContent,'OpenAbstractions works again.');
 assert.equal(el('apps-health').hidden,false,'the banner stays with its sentence after the reread');
 assert.strictEqual(el('apps').children[0],row,'the app row is the same element');
 assertPanelRules({tagName:'MAIN',children:[{tagName:'SECTION',children:[el('apps-health'),el('apps')]}]},'Apps',{allowClasses:allowlistedClasses('status')});
 context.appsRun.abort();
 console.log('PASS: Apps: Check again writes its sentence, the timer rereads, the sentence and the rows survive');
})().catch(e=>{console.error(e);process.exitCode=1});
