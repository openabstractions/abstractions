// Run with node monitor/rights_list_page_test.js; no browser or service is
// started. Covers Saved permissions on the Permissions page (rights_page.go)
// as the panel audit of 2026-11-01 found it: the Panel's and the runtime's
// identical operator rules listed twice under one "OpenAbstractions"
// heading, "app:<name>" and "host:<name>" as visible text, a Details row
// after every rule, and a "Set up by OpenAbstractions" count that grew while
// later pages were read. Since the ruling of 2026-11-09 the Panel and the
// runtime share one "OpenAbstractions" group after the apps, their identical
// set-up rules show once, and the exact rules sit in Advanced's one
// Technical details.
const fs=require('fs'),vm=require('./panel_record_test_helper.js').vm,assert=require('assert');
function node(tag='div'){return {tagName:tag.toUpperCase(),value:'',checked:false,hidden:false,disabled:false,textContent:'',className:'',title:'',children:[],listeners:{},attrs:{},dataset:{},get options(){return this.children},replaceChildren(){this.children=[]},setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return Object.hasOwn(this.attrs,k)?this.attrs[k]:null},hasAttribute(k){return Object.hasOwn(this.attrs,k)},removeAttribute(k){delete this.attrs[k]},append(...items){this.children.push(...items)},addEventListener(name,fn){(this.listeners[name]??=[]).push(fn)},querySelector(){return this.save},querySelectorAll(){return this.children.flatMap(n=>n.tagName==='BUTTON'?[n]:n.querySelectorAll?n.querySelectorAll():[])}}}
const fields=new Map();
function el(id){if(!fields.has(id))fields.set(id,node(id.includes('select')||id==='rights-action'?'select':'div'));return fields.get(id)}
el('rights-grant').save=node('button');
function raw(file,name){const s=fs.readFileSync(__dirname+'/'+file,'utf8'),m='const '+name+' = `',start=s.indexOf(m)+m.length;assert(start>=m.length,name);return s.slice(start,s.indexOf('`',start))}
const plain=raw('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0];
const page=fs.readFileSync(__dirname+'/rights_page.go','utf8').split('<script>')[1].split('</script>')[0].replace('` + serviceCommonScript + `',raw('service_page.go','serviceCommonScript'));

const panel='C:\\OA\\OpenAbstractions Panel.exe',runtime='C:\\OA\\openabstractions.exe',app='C:\\Apps\\pretend-lemonade.exe',account='S-1-5-21-1';
const own=(program,action,resource,why,setBy)=>({Subject:{Account:account,Program:program},Action:action,Resource:resource,Permit:true,Why:why,SetBy:{Account:account,Program:setBy}});
const operator=program=>[own(program,'abstraction.facade/application.read','app:pretend-lemonade','registered by the app',runtime),own(program,'abstraction.inference/complete','host:demonstration-model-server','inference server add',panel)];
const names={[panel]:'OpenAbstractions Panel',[runtime]:'OpenAbstractions',[app]:'Pretend Lemonade'};
const plainWords={'abstraction.facade/application.read':'application read','abstraction.inference/complete':'use models','abstraction.download/fetch':'download files'};
const first={Outcome:'page',Revision:'r1',Catalog:Object.keys(plainWords),ActionPlain:plainWords,Names:names,Rules:[...operator(panel),...operator(runtime),{Subject:{Account:account,Program:app},Action:'abstraction.inference/complete',Resource:'host:demonstration-model-server',Permit:true,Why:'',SetBy:{Account:account,Program:panel}}],Definitions:[],Bundles:[],SelectorAvailable:false,Next:'p2',Complete:false};
const second={...first,Rules:[own(panel,'abstraction.download/fetch','registry','installation',runtime),own(runtime,'abstraction.download/fetch','registry','installation',runtime)],Next:'',Complete:true};
let releaseSecond;const secondRead=new Promise(r=>releaseSecond=r);
const ok=value=>({ok:true,json:async()=>value,text:async()=>JSON.stringify(value)});
const context={console,URLSearchParams,AbortController,location:{search:'?k=test'},localStorage:{length:0,key(){},getItem(){return null},setItem(){}},document:{getElementById:el,createElement:node},setTimeout(){return 0},clearTimeout(){},addEventListener(){},fetch:async(path,opts={})=>{
 if(path.startsWith('/rights/observe?'))return new Promise((resolve,reject)=>opts.signal.addEventListener('abort',()=>reject(Object.assign(Error('aborted'),{name:'AbortError'})),{once:true}));
 if(path==='/rights?cursor=')return ok(first);
 if(path==='/rights?cursor=p2'){await secondRead;return ok(second)}
 if(path==='/explore')return ok({self:{account,program:panel},selfAccountName:'Owner',probes:[]});
 if(path.startsWith('/apps/registrations'))return ok({outcome:'page',entries:[{role:'application',program:app,name:'pretend-lemonade',title:'Pretend Lemonade'}]});
 if(path.startsWith('/questions')||path.startsWith('/choices'))return ok({Outcome:'page',Records:[],Next:'',Complete:true});
 if(path.startsWith('/rights/requests'))return ok({outcome:'unsupported'});
 if(path==='/rights/assistant')return ok({outcome:'page',revision:'r1',assistants:[],candidates:[],warning:{answers:[],staysWithYou:[]}});
 if(path==='/rights/answered')return ok({outcome:'page',answers:[]});
 if(path==='/rights/defaults')return ok({Outcome:'page',Revision:'r1',Defaults:[]});
 throw Error('unexpected '+path);
}};context.window=context;vm.createContext(context);vm.runInContext(plain,context);vm.runInContext(page,context);
async function until(predicate,what){for(let i=0;i<200;i++){if(predicate())return;await new Promise(resolve=>setImmediate(resolve))}throw Error('expected '+what)}
// visible is every text a person reads in a tree (titles are not text).
function visible(n){const children=n.tagName==='DETAILS'&&!n.open?(n.children||[]).slice(0,1):n.children||[];return [n.textContent||''].concat(children.flatMap(visible)).join('\n')}
function inspect(n){return [n.textContent||''].concat((n.children||[]).flatMap(inspect)).join('\n')}
const groups=()=>el('rights').children.filter(b=>b.className==='oa-group');
const heading=g=>g.children[0].textContent;
const setUp=g=>g.children.find(c=>c.installed);
(async()=>{
 await until(()=>groups().length===2,'the first page drawn');
 for(const g of groups().filter(g=>heading(g).startsWith('OpenAbstractions')))
  assert.strictEqual(setUp(g).children[0].textContent,'Set up by OpenAbstractions','no count while later pages are read');
 releaseSecond();
 await until(()=>groups().length===2&&setUp(groups()[1]).children[0].textContent.includes('('),'the complete list drawn');
 assert.deepStrictEqual(groups().map(heading),['Pretend Lemonade','OpenAbstractions'],'the apps first, then the own programs of OpenAbstractions under one heading');
 for(const g of groups().filter(g=>heading(g).startsWith('OpenAbstractions'))){
  assert.deepStrictEqual(g.children.map(c=>c.tagName),['H3','DETAILS'],'operator rows fold together; the exact rules sit in Advanced');
  assert.strictEqual(setUp(g).children[0].textContent,'Set up by OpenAbstractions (3)','the count, once complete');
 }
 for(const g of groups().filter(g=>heading(g).startsWith('OpenAbstractions')))setUp(g).open=true;
 const text=visible(el('rights'));
 assert(!/\bapp:|\bhost:/.test(text),'no resource id is visible text: '+text);
 assert(text.includes('Pretend Lemonade · Until removed'),'an app resource reads as its registered title');
 assert(text.includes('Demonstration model server · Until removed'),'a model server resource reads as its name in words');
 assert(!/\nDetails\n/.test('\n'+text+'\n'),'an exact rule has no Details row');
 const appRows=groups()[0].children.filter(c=>c.className==='oa-row');
 assert.strictEqual(appRows.length,1,'the app\'s own rule stays a row');
 const group=node(),typed={Subject:{Account:account,Program:app},Action:'abstraction.credentials/use',Resource:'opaque-exact-target',Permit:false,Why:'installation',SetBy:{Account:account,Program:app},Selector:{Permission:{Action:'abstraction.credentials/use',ResourceProfile:'abstraction.credentials/named-secret@1'},Scope:'one',Name:'exact-secret',Target:'opaque-exact-target',Editable:true,CanGrant:true}};
 context.appendRightsTable(group,[typed]);
 const appGroup=group.children[0],details=appGroup.children.filter(c=>c.tagName==='DETAILS');
 assert.strictEqual(details.length,0,'typed permission has no per-group or per-row Details');
 assert(!appGroup.children.some(c=>c.installed),'operator-entered reason never proves setup provenance');
 assert.strictEqual(context.rightsSetUp(false,{...typed,Permit:true}),false,'an allowed rule reason does not fabricate installer ownership');
 assert(visible(appGroup).includes('exact-secret · Until removed'),'exact one-name scope stays clear');
 assert(!visible(appGroup).includes('opaque-exact-target'),'exact target is confined to technical details');
 const exact=context.rightsListedJSON.join('\n');assert(exact.includes('opaque-exact-target')&&exact.includes('"Permit": false'),'full exact rule and blocked choice stay inspectable in the Technical details under Advanced');
 assert.strictEqual(context.rightsTargetLabel('app:unregistered-id-0123456789'),'An app','unknown registration never fabricates a display name');
 console.log('rights_list_page_test: ok');
})().catch(error=>{console.error(error);process.exit(1)});
