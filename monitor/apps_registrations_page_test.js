// Drive the actual Apps script (service_page.go) through the registry's own
// observation: arrival, a new runtime epoch (stale), withdrawal, an
// unavailable registry and page exit. The Apps list never writes.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const {assertPanelRules}=require('./static/panel_rules.js');
const {allowlistedClasses}=require('./panel_fake_dom_test_helper.js');
const fields=new Map(),events=new Map(),timers=new Map(),pending=[];let timerID=0,writes=0;
function node(tag='div'){return {tag,tagName:tag.toUpperCase(),value:'',textContent:'',hidden:false,children:[],attrs:{},replaceChildren(){this.children=[]},append(...v){this.children.push(...v)},setAttribute(k,v){this.attrs[k]=String(v)},removeAttribute(k){delete this.attrs[k]},addEventListener(){}}}
function el(id){if(!fields.has(id))fields.set(id,node());return fields.get(id)}
function constant(file,name){const source=fs.readFileSync(__dirname+'/'+file,'utf8'),marker='const '+name+' = `',start=source.indexOf(marker)+marker.length;return source.slice(start,source.indexOf('`',start))}
const app=(name,title,instances=0)=>({name,program:'C:/Apps/'+name+'.exe',role:'application',state:instances+' current announcements',subject:{Account:'alice',Program:'C:/Apps/'+name+'.exe'},application:{Descriptor:{Name:name,Program:'C:/Apps/'+name+'.exe',Title:title},Instances:Array.from({length:instances},()=>({}))}});
let first={outcome:'page',cursor:'a1',entries:[app('editor','Editor <img>')]};
const ok=value=>({ok:true,json:async()=>value});
const context={console,URLSearchParams,AbortController,Date,location:{search:'?k=test'},document:{getElementById:el,createElement:node,documentElement:{dataset:{}}},
 setTimeout(fn){const id=++timerID;timers.set(id,fn);return id},clearTimeout(id){timers.delete(id)},addEventListener(name,fn){events.set(name,fn)},
 fetch:async(path,options={})=>{
  if(options.body)writes++;
  if(path==='/runtime')return ok({capabilities:[]});
  if(path.startsWith('/apps/registrations?')){const query=new URLSearchParams(path.split('?')[1]);assert.equal(query.get('source'),'applications');
   if(query.get('wait_ms')==='0')return ok(first);
   return new Promise(resolve=>{const entry={signal:options.signal,query,resolve:value=>resolve(ok(value)),aborted:false};pending.push(entry);options.signal.addEventListener('abort',()=>{entry.aborted=true},{once:true})})}
  throw Error('unexpected '+path);
 }};context.window=context;vm.createContext(context);
vm.runInContext(constant('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0],context);
vm.runInContext(constant('service_page.go','serviceCommonScript')+constant('service_page.go','appsPageScript'),context);
async function until(condition){for(let i=0;i<100;i++){if(condition())return;await new Promise(resolve=>setImmediate(resolve))}throw Error('the Apps script did not advance')}
const labels=()=>el('apps').children.map(row=>row.children[0]?.children[0]?.textContent);
const take=()=>{assert(pending.length,'registry observer missing');return pending.shift()};
(async()=>{
 await until(()=>pending.length);
 await context.appsArrived;assert.equal(context.document.documentElement.dataset.ready,'1','the page marks itself ready once its first reads finished');
 assert.deepEqual(labels(),['Editor <img>'],'a registered title is text, never markup');
 let watch=take();assert.equal(watch.query.get('cursor'),'a1');assert.equal(watch.query.get('wait_ms'),'30000','registration changes use the registry\'s bounded Observe');
 watch.resolve({outcome:'stale',cursor:'a2',entries:[app('editor','Editor <img>',1),app('notes','Notes')]});await until(()=>pending.length);
 assert.deepEqual(labels(),['Editor <img>','Notes'],'a new runtime epoch replaces the complete snapshot');
 assert.equal(el('apps').children[0].children[0].children[1].textContent,'Running');
 const notes=el('apps').children[1];
 watch=take();assert.equal(watch.query.get('cursor'),'a2');
 watch.resolve({outcome:'page',cursor:'a3',entries:[app('notes','Notes')]});await until(()=>pending.length);
 assert.deepEqual(labels(),['Notes'],'a withdrawn registration leaves the list');
 assert.strictEqual(el('apps').children[0],notes,'a reread keeps the row of an app still registered');
 assertPanelRules({tagName:'SECTION',children:[el('apps')]},'Apps',{allowClasses:allowlistedClasses('status')});
 watch=take();watch.resolve({outcome:'unavailable',entries:[]});await until(()=>timers.size===1);
 assert.equal(el('apps-health').hidden,false,'an unavailable registry shows the banner');
 assert.deepEqual(labels(),['Notes'],'the last list read stays while the banner names the failure');
 first={outcome:'page',cursor:'a4',entries:[app('notes','Notes'),app('paint','Paint')]};
 const retry=timers.entries().next().value;timers.delete(retry[0]);retry[1]();await until(()=>pending.length);
 assert.deepEqual(labels(),['Notes','Paint'],'the retry starts again from a full read');assert.equal(el('apps-health').hidden,true,'the banner hides once the registry reads again');
 const late=take();events.get('pagehide')();assert(late.signal.aborted&&late.aborted,'page exit cancels the registry observation');
 late.resolve({outcome:'page',cursor:'a5',entries:[]});await new Promise(resolve=>setImmediate(resolve));
 assert.deepEqual(labels(),['Notes','Paint'],'a late response cannot change a closed page');assert.equal(timers.size,0);assert.equal(writes,0,'the Apps list never writes');
 console.log('PASS: Apps follows the registry through epochs and withdrawal, names an unavailable registry, retries from a full read and cancels on page exit');
})().catch(error=>{if(context.appsRun)context.appsRun.abort();console.error(error);process.exitCode=1});
