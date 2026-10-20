// Run with node monitor/rights_agents_page_test.js; no browser or service is
// started. Covers Messages between agents on the Permissions page
// (rights_page.go, AB-1w Panel ruling 2026-11-09): the page is ready only
// after the defaults read, "Ask me first" shows its saved state then, a stale
// revision rereads once and writes again, a refused write turns the switch
// back, and a failed read hides the switch and says why.
const fs=require('fs'),vm=require('./panel_record_test_helper.js').vm,assert=require('assert');
function node(tag='div'){return {tagName:tag.toUpperCase(),value:'',checked:false,hidden:false,disabled:false,textContent:'',className:'',title:'',children:[],listeners:{},attrs:{},dataset:{},get options(){return this.children},replaceChildren(){this.children=[]},setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return Object.hasOwn(this.attrs,k)?this.attrs[k]:null},hasAttribute(k){return Object.hasOwn(this.attrs,k)},removeAttribute(k){delete this.attrs[k]},append(...items){this.children.push(...items)},addEventListener(name,fn){(this.listeners[name]??=[]).push(fn)},querySelector(){return this.save},querySelectorAll(){return []}}}
const fields=new Map();
function el(id){if(!fields.has(id))fields.set(id,node(id.includes('select')||id==='rights-action'?'select':'div'));return fields.get(id)}
el('rights-grant').save=node('button');
// The page's markup: the switch hidden and disabled, its status loading.
Object.assign(el('agent-messages-ask'),{hidden:true,disabled:true});
Object.assign(el('agent-messages-status'),{textContent:'Loading…'});el('agent-messages-status').attrs['data-state']='loading';
function raw(file,name){const s=fs.readFileSync(__dirname+'/'+file,'utf8'),m='const '+name+' = `',start=s.indexOf(m)+m.length;assert(start>=m.length,name);return s.slice(start,s.indexOf('`',start))}
const plain=raw('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0];
const page=fs.readFileSync(__dirname+'/rights_page.go','utf8').split('<script>')[1].split('</script>')[0].replace('` + serviceCommonScript + `',raw('service_page.go','serviceCommonScript'));

const account='S-1-5-21-1',panel='C:\\OA\\OpenAbstractions Panel.exe';
const ok=value=>({ok:true,json:async()=>value,text:async()=>JSON.stringify(value)});
let revision='r1',agents={Name:'agent-messages',Label:'Message each other without asking',On:false},refuse='',readFails='',reads=0;const writes=[];
let releaseDefaults;const defaultsHeld=new Promise(r=>releaseDefaults=r);
const context={console,URLSearchParams,AbortController,location:{search:'?k=test'},localStorage:{length:0,key(){},getItem(){return null},setItem(){}},document:{getElementById:el,createElement:node,createTextNode:text=>({textContent:text})},setTimeout(){return 0},clearTimeout(){},addEventListener(){},fetch:async(path,opts={})=>{
 if(path.startsWith('/rights/observe?'))return new Promise((resolve,reject)=>opts.signal.addEventListener('abort',()=>reject(Object.assign(Error('aborted'),{name:'AbortError'})),{once:true}));
 if(path==='/rights?cursor=')return ok({Outcome:'page',Revision:'p1',Catalog:[],ActionPlain:{},Names:{},Rules:[],Definitions:[],Bundles:[],SelectorAvailable:false,Next:'',Complete:true});
 if(path==='/explore')return ok({self:{account,program:panel},selfAccountName:'Owner',probes:[]});
 if(path.startsWith('/apps/registrations'))return ok({outcome:'page',entries:[]});
 if(path.startsWith('/questions')||path.startsWith('/choices'))return ok({Outcome:'page',Records:[],Next:'',Complete:true});
 if(path.startsWith('/rights/requests'))return ok({outcome:'unsupported'});
 if(path==='/rights/assistant')return ok({outcome:'page',revision:'r1',assistants:[],candidates:[],warning:{answers:[],staysWithYou:[]}});
 if(path==='/rights/answered')return ok({outcome:'page',answers:[]});
 if(path==='/rights/defaults'&&!opts.body){reads++;await defaultsHeld;if(readFails)return ok({Outcome:readFails,Defaults:[]});return ok({Outcome:'page',Revision:revision,Defaults:[{Name:'download',Label:'Download in the background',Phrase:'download in the background',On:true}],Agents:agents})}
 if(path==='/rights/defaults'){const edit=JSON.parse(opts.body);writes.push(edit);
  if(refuse)return ok({Outcome:refuse,Defaults:[]});
  if(edit.revision!==revision)return ok({Outcome:'conflict',Revision:revision,Defaults:[]});
  revision='r'+(writes.length+1);agents={...agents,On:edit.on,SetBy:{Account:account,Program:panel}};return ok({Outcome:'applied',Revision:revision,Defaults:[],Current:agents})}
 throw Error('unexpected '+path);
}};context.window=context;vm.createContext(context);vm.runInContext(plain,context);
let ready=false;context.oaReady=()=>{ready=true};
vm.runInContext(page,context);
async function until(predicate,what){for(let i=0;i<200;i++){if(predicate())return;await new Promise(resolve=>setImmediate(resolve))}throw Error('expected '+what)}
const toggle=el('agent-messages-ask'),status=()=>el('agent-messages-status').textContent;
(async()=>{
 await until(()=>reads===1,'the defaults read after Saved permissions');
 for(let i=0;i<20;i++)await new Promise(resolve=>setImmediate(resolve));
 assert.strictEqual(ready,false,'the page is not ready before the defaults read');
 assert.strictEqual(toggle.hidden,true,'no On or Off shows before the read');
 assert.strictEqual(status(),'Loading…');
 releaseDefaults();
 await until(()=>ready,'the page ready after the defaults read');
 assert.strictEqual(toggle.hidden,false);assert.strictEqual(toggle.disabled,false);
 assert.strictEqual(toggle.checked,true,'a default that is off reads Ask me first On');
 assert.strictEqual(status(),'');
 assert(el('defaults-list').children.length>0,'Defaults for all apps is drawn when the page is ready');

 toggle.checked=false;await toggle.onchange();
 assert.deepStrictEqual(writes,[{revision:'r1',name:'agent-messages',on:true}],'Ask me first Off writes the default on at the read revision');
 assert.strictEqual(status(),'Your agents now message each other without asking you.');

 revision='r9';const readsBefore=reads;toggle.checked=true;await toggle.onchange();
 assert.deepStrictEqual(writes.slice(1).map(w=>[w.revision,w.on]),[['r2',false],['r9',false]],'a stale revision writes again at the reread one');
 assert.strictEqual(reads-readsBefore,1,'exactly one reread');
 assert.strictEqual(toggle.checked,true);assert.strictEqual(status(),'Your agents now ask you before messaging each other.');

 refuse='forbidden';toggle.checked=false;await toggle.onchange();
 assert.strictEqual(toggle.checked,true,'a refused write turns the switch back On');
 assert.strictEqual(status(),context.defaultStatus('forbidden'));
 assert.strictEqual(status(),'This Panel is not authorized as an operator of OpenAbstractions; nothing was changed.');

 readFails='forbidden';await context.defaultsList();
 assert.strictEqual(toggle.hidden,true,'a failed read hides the switch');
 assert.strictEqual(status(),context.defaultStatus('forbidden'),'and says why');
 readFails='';await context.defaultsList();
 assert.strictEqual(toggle.hidden,false,'a later read shows it again');assert.strictEqual(toggle.checked,true);
 console.log('rights_agents_page_test: ok');
 process.exit(0);
})().catch(error=>{console.error(error);process.exit(1)});
