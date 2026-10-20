// Drive the actual Apps script (service_page.go) with an app its installer
// asked to connect that is Not connected (application_pending.go): its row
// sits beside the registered apps with its title, Not connected, Connect and
// Learn more. A name another app holds names it on the first Connect click,
// and only the second click replaces it through /apps/pending/connect; a
// refused Connect keeps the row.
const fs=require('fs'),vm=require('./panel_record_test_helper.js').vm,assert=require('assert');
const {assertPanelRules}=require('./static/panel_rules.js');
const {allowlistedClasses}=require('./panel_fake_dom_test_helper.js');
const fields=new Map(),timers=new Map(),pending=[],writes=[];let timerID=0;
function node(tag='div'){return {tag,tagName:tag.toUpperCase(),value:'',textContent:'',hidden:false,disabled:false,children:[],attrs:{},replaceChildren(...v){this.children=[...v]},append(...v){this.children.push(...v)},setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return this.attrs[k]},removeAttribute(k){delete this.attrs[k]},addEventListener(){}}}
function el(id){if(!fields.has(id))fields.set(id,node());return fields.get(id)}
function constant(file,name){const source=fs.readFileSync(__dirname+'/'+file,'utf8'),marker='const '+name+' = `',start=source.indexOf(marker)+marker.length;return source.slice(start,source.indexOf('`',start))}
const registered=(name,title)=>({name,program:'C:/Apps/'+name+'.exe',role:'application',state:'0 current announcements',application:{Descriptor:{Name:name,Program:'C:/Apps/'+name+'.exe',Title:title},Instances:[]}});
const waiting={name:'coder',title:'Coder <b>',program:'coder',publisher:'Publisher: Example Corp, verified by its signature.',
 digest:'d'.repeat(64),replaces:'Old Coder',shown:'e'.repeat(64)};
let apps={outcome:'page',cursor:'a1',entries:[registered('editor','Editor')]},waitingNow=[waiting],allowReply=null;
const ok=value=>({ok:true,json:async()=>value}),refused=text=>({ok:false,text:async()=>text});
const context={console,URLSearchParams,AbortController,Date,location:{search:'?k=test'},document:{getElementById:el,createElement:node,documentElement:{dataset:{}}},
 setTimeout(fn){const id=++timerID;timers.set(id,fn);return id},clearTimeout(id){timers.delete(id)},addEventListener(){},
 fetch:async(path,options={})=>{
  if(path==='/runtime')return ok({capabilities:[]});
  if(path==='/apps/pending')return ok({outcome:'page',entries:waitingNow});
  if(path==='/apps/pending/connect'){writes.push(JSON.parse(options.body));return allowReply()}
  if(path.startsWith('/apps/registrations?')){const query=new URLSearchParams(path.split('?')[1]);
   if(query.get('wait_ms')==='0')return ok(query.get('source')==='providers'?{outcome:'page',cursor:'p1',entries:[]}:apps);
   return new Promise(resolve=>{pending.push({query,resolve:value=>resolve(ok(value))});options.signal.addEventListener('abort',()=>{},{once:true})})}
  throw Error('unexpected '+path);
 }};context.window=context;vm.createContext(context);
vm.runInContext(constant('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0],context);
vm.runInContext(constant('service_page.go','serviceCommonScript')+constant('service_page.go','appsPageScript'),context);
async function until(condition){for(let i=0;i<100;i++){if(condition())return;await new Promise(resolve=>setImmediate(resolve))}throw Error('the Apps script did not advance')}
const rows=()=>el('apps').children;
const label=row=>row.label.textContent;
const watching=()=>pending.some(e=>e.query.get('source')==='applications');
const take=()=>{const i=pending.findIndex(e=>e.query.get('source')==='applications');return pending.splice(i,1)[0]};
(async()=>{
 await until(watching);await context.appsArrived;
 assert.deepEqual(rows().map(label),['Coder <b>','Editor'],'the Not connected app is a row beside the registered apps, its title as text');
 const row=rows()[0];
 assert.equal(row.caption.textContent,'Not connected');
 assert.equal(row.connect.textContent,'Connect');assert.equal(row.connect.className,'oa-primary');
 const more=row.children[4];assert.equal(more.tag,'details');assert.equal(more.children[0].textContent,'Learn more','details wait behind Learn more');
 assert.deepEqual(row.more.children.map(p=>p.textContent),['Program: coder','Publisher: Example Corp, verified by its signature.']);
 assert(row.replaces.hidden,'the replaced app is named only after a click');
 assert(!JSON.stringify(rows().map(r=>r.textContent)).includes('d'.repeat(16)),'the request digest is never page text');
 assertPanelRules({tagName:'SECTION',children:[el('apps')]},'Apps',{naming:true,allowClasses:allowlistedClasses('status')});

 // The first click names the app it replaces and writes nothing.
 await row.connect.onclick();
 assert.equal(writes.length,0,'the first click replaces nothing');
 assert(!row.replaces.hidden);assert.equal(row.replaces.textContent,'This replaces Old Coder connected now.');
 assert.equal(row.connect.textContent,'Replace');

 // A refused Connect says why in the row, keeps it and enables its button.
 allowReply=()=>refused("this app's request or the app it replaces changed since Apps showed it; review it again");
 await row.connect.onclick();
 assert.deepEqual(writes[0],{name:'coder',digest:'d'.repeat(64),shown:'e'.repeat(64)},'Connect sends the request and the replaced entry the row showed');
 assert.equal(row.status.attrs['data-state'],'error');assert(row.status.textContent.length>0,'the refusal is one sentence in the row');
 assert.strictEqual(rows()[0],row);assert.equal(row.connect.disabled,false);assert.equal(row.connect.textContent,'Connect');

 // Connect, then Replace, connects it; the row leaves and the group says what happened.
 allowReply=()=>ok({message:'Coder <b> is connected to OpenAbstractions.'});
 await row.connect.onclick();assert.equal(writes.length,1);
 await row.connect.onclick();
 assert.equal(writes.length,2);
 assert.deepEqual(rows().map(label),['Editor'],'a connected app no longer waits');
 assert.equal(el('apps-pending-status').textContent,'Coder <b> is connected to OpenAbstractions.');
 waitingNow=[];
 take().resolve({outcome:'page',cursor:'a2',entries:[registered('coder','Coder <b>'),registered('editor','Editor')]});
 await until(watching);
 assert.deepEqual(rows().map(label),['Coder <b>','Editor'],'the connected app is listed with the registered apps');
 assertPanelRules({tagName:'SECTION',children:[el('apps')]},'Apps',{naming:true,allowClasses:allowlistedClasses('status')});
 context.appsRun.abort();
 console.log('PASS: a Not connected app shows in Apps with Connect; the first click names the app it replaces; the second replaces it and moves it to the registered apps');
})().catch(error=>{if(context.appsRun)context.appsRun.abort();console.error(error);process.exitCode=1});
