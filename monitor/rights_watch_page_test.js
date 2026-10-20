// Native watch is exercised in rights_observe_test.go. This VM drives the real
// browser script through automatic refresh, drafts, conflicts and cancellation.
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const fields=new Map(),events=new Map(),timers=new Map();let timerID=0;
function node(tag='div'){return {tagName:tag.toUpperCase(),value:'',checked:false,hidden:false,disabled:false,children:[],listeners:{},get options(){return this.children},replaceChildren(){this.children=[]},setAttribute(){},append(...items){this.children.push(...items)},addEventListener(name,fn){(this.listeners[name]??=[]).push(fn)},dispatch(name){for(const f of this.listeners[name]||[])f()},querySelector(){return this.save},querySelectorAll(){return this.children.flatMap(n=>n.tagName==='BUTTON'?[n]:n.querySelectorAll?n.querySelectorAll():[])}}}
function el(id){if(!fields.has(id))fields.set(id,node(id.includes('select')||id==='rights-action'?'select':'div'));return fields.get(id)}
for(const id of ['rights-grant','delegation-form'])el(id).save=node('button');
function raw(file,name){const s=fs.readFileSync(__dirname+'/'+file,'utf8'),m='const '+name+' = `',start=s.indexOf(m)+m.length;assert(start>=m.length);return s.slice(start,s.indexOf('`',start))}
const plain=raw('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0];
const pageSource=fs.readFileSync(__dirname+'/rights_page.go','utf8');
assert(pageSource.includes('id="rights-admin-access" class="oa-expander" hidden>'),'administrative access is hidden before any authorized list');
const markup=pageSource.slice(pageSource.indexOf('var rightsPage = ')).split('<script>')[0];
function disclosed(id){const stack=[];for(const match of markup.matchAll(/<\/?([a-z][a-z0-9-]*)\b[^>]*>/gi)){const tag=match[1],html=match[0];if(html.startsWith('</')){const at=stack.map(n=>n.tag).lastIndexOf(tag);if(at>=0)stack.length=at;continue}if(html.includes('id="'+id+'"'))return stack.some(n=>n.tag==='details'&&!/\bopen(?:[\s=>])/.test(n.html));if(!['input','br','hr'].includes(tag))stack.push({tag,html})}throw Error('missing '+id)}
for(const id of ['rights-grant','assistant-form','delegation-form','questions-history-list','choices-history-list','answered-list'])assert(disclosed(id),'secondary content starts inside a closed native disclosure: '+id);
for(const id of ['rights-status','questions-status','choices-status','assistant-status','delegation-status','answered-status'])assert(!disclosed(id),'service status remains visible with panels collapsed: '+id);
// Owner's Panel rules (2026-10-06): each section opens with its heading and one short caption.
const sections=markup.split('<section').slice(1);
assert.deepEqual(sections.map(section=>/^ id="([^"]+)"/.exec(section)[1]),['questions','rights-section','defaults','assistant','answer-delegation','app-questions'],'Questions waiting, saved permissions, defaults for all apps (VISION 2026-10-07), answering access, Advanced, then past answers (SPEC §4)');
for(const section of sections)assert(/^[^>]*><h2[^>]*>[^<]*<\/h2><p class="oa-caption">[^<]{1,80}<\/p>/.test(section),'section heading with one short caption: '+section.slice(0,80));
for(const id of ['questions-next','choices-next','rights-next'])assert(new RegExp('id="'+id+'"[^>]*hidden>Show more<').test(markup),'Show more appears only past a page: '+id);
assert(!/Refresh|Next page|placeholder=|id="rights-allow"/.test(markup),'no Refresh, Next page, typed placeholder or grouped Allow form');
assert(!/ComfyUI|OpenCode/.test(markup),'the permissions page names no adopter');
const page=pageSource.split('<script>')[1].split('</script>')[0].replace('` + serviceCommonScript + `',raw('service_page.go','serviceCommonScript'));
el('rights-admin-access').hidden=true;
const action='opencode/log-source.share',target={Account:'account',Program:'/apps/actual-caller'};
const typedKey={Action:'abstraction.credentials/apply',ResourceProfile:'abstraction.credentials/named-use@2'},typedChoice='selector:'+JSON.stringify([typedKey.Action,typedKey.ResourceProfile]);
const typedDefinition={Definition:typedKey,Label:'Use saved secret',SupportedSelectors:['one','all'],Editable:true};
let policy={Outcome:'page',Revision:'one',Catalog:[action],ActionPlain:{[action]:'share event contents'},Rules:[{Subject:target,Action:action,Resource:'exact-source',Permit:true,Expires:'2030-01-01T00:00:00.000Z',Expired:false}],Definitions:[typedDefinition],Bundles:[],SelectorAvailable:true,Next:'',Complete:true};
const observes=[],writes=[];let getCount=0,pendingWrite=null,pendingGet=null,pendingGetSignal=null,deferGet=false,listingOutcome='page';
const ok=value=>({ok:true,json:async()=>value});
const context={console,URLSearchParams,AbortController,location:{search:'?k=test'},localStorage:{length:0,key(){},getItem(){return null},setItem(){}},document:{getElementById:el,createElement:node},setTimeout(fn){const id=++timerID;timers.set(id,fn);return id},clearTimeout(id){timers.delete(id)},addEventListener(name,fn){events.set(name,fn)},fetch:async(path,opts={})=>{
 if(path.startsWith('/rights/observe?'))return new Promise((resolve,reject)=>{const entry={path,resolve:value=>resolve(ok(value)),reject,signal:opts.signal};observes.push(entry);opts.signal.addEventListener('abort',()=>reject(Object.assign(Error('aborted'),{name:'AbortError'})),{once:true})});
 if(path.startsWith('/rights?')){getCount++;if(listingOutcome==='transport')throw Error('Permission listing lost');if(listingOutcome!=='page')return ok({Outcome:listingOutcome});if(deferGet){deferGet=false;pendingGetSignal=opts.signal;return new Promise(resolve=>{pendingGet=value=>resolve(ok(value))})}return ok(policy)}
 if(path==='/rights'&&opts.body){writes.push(JSON.parse(opts.body));return new Promise(resolve=>{pendingWrite=value=>resolve(ok(value))})}
 if(path==='/explore')return ok({self:{account:'account',program:'/apps/panel'},selfAccountName:'Owner',probes:[]});
 if(path==='/card/table')return ok({resources:[]});
 if(path.startsWith('/questions'))return ok({Outcome:'page',Records:[],Next:'',Complete:true});
 if(path==='/rights/delegation')return ok({outcome:'page',revision:policy.Revision,account:'account',questions:[]});
 if(path==='/rights/assistant')return ok({outcome:'page',revision:policy.Revision,assistants:[],candidates:[],warning:{answers:[],staysWithYou:[]}});
 if(path==='/rights/answered')return ok({outcome:'page',answers:[]});
 throw Error('unexpected '+path);
}};context.window=context;vm.createContext(context);vm.runInContext(plain,context);vm.runInContext(page,context);
const value=s=>vm.runInContext(s,context);
async function until(predicate){for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setImmediate(resolve))}throw Error('expected script progress')}
const take=()=>{assert(observes.length);return observes.shift()};
function resumeRetry(){const [id,retry]=timers.entries().next().value;timers.delete(id);retry()}
(async()=>{
 assert(el('rights-admin-access').hidden,'identity and page startup alone claim no administrative admission');
 await context.rightsListReady;await until(()=>observes.length===1);
 assert.equal(el('rights-admin-access').hidden,false,'an authorized permission list exposes the Panel administrative role');
 for(const outcome of ['forbidden','unavailable','transport']){
  listingOutcome=outcome;await context.rightsList();
  assert.equal(el('rights-admin-access').hidden,true,'failed listing clears administrative admission: '+outcome);
  listingOutcome='page';await context.rightsList();
  assert.equal(el('rights-admin-access').hidden,false,'fresh authorized listing restores administrative admission');
 }
 assert.equal(new URLSearchParams(take().path.split('?')[1]).get('revision'),'one','following starts automatically at the listed revision');
 // Restart the pending run so the discarded test handle cannot be mistaken for
 // a change response. Page lifecycle abort is also exercised at the end.
 context.stopRightsWatch();context.followRights();await until(()=>observes.length===1);
 const initialGets=getCount;take().resolve({Outcome:'unchanged',Revision:'one'});await until(()=>observes.length===1);
 assert.equal(getCount,initialGets,'quiet/no-op does not repeatedly read all rule records');
 // Begin typing after a refresh of a clean form has already started.
 deferGet=true;take().resolve({Outcome:'changed',Revision:'intermediate'});await until(()=>pendingGet);
 el('rights-program').value='/apps/typed-draft';el('rights-account').value='typed account';el('rights-resource').value='typed resource';el('rights-action').value=typedChoice;el('rights-scope').value='one';el('rights-name').value='alpha';el('rights-permit').value='permit';el('rights-ttl').value='300000';el('rights-why').value='Initial reason';el('rights-grant').dispatch('input');
 policy={...policy,Revision:'intermediate'};pendingGet(policy);pendingGet=null;await until(()=>observes.length===1);
 assert.equal(el('rights-program').value,'/apps/typed-draft');assert.equal(el('rights-account').value,'typed account','typing during a clean-form refresh retains the new draft');
 assert.equal(value('rightsDrafts.grant.revision'),'one');
 // Continue editing a dirty form during a second deferred policy read.
 deferGet=true;take().resolve({Outcome:'changed',Revision:'two'});await until(()=>pendingGet);
 el('rights-program').value='/apps/latest-draft';el('rights-account').value='latest account';el('rights-resource').value='latest resource';el('rights-scope').value='all';el('rights-name').value='beta';el('rights-broad-confirm').checked=true;el('rights-ttl').value='3600000';el('rights-why').value='Latest reason';el('rights-grant').dispatch('input');
 policy={...policy,Revision:'two'};pendingGet(policy);pendingGet=null;await until(()=>observes.length===1);
 assert.equal(el('rights-program').value,'/apps/latest-draft');assert.equal(el('rights-account').value,'latest account');assert.equal(el('rights-resource').value,'latest resource');
 assert.equal(el('rights-ttl').value,'3600000');assert.equal(el('rights-why').value,'Latest reason','watch refresh preserves the lifetime and reason drafted during the read');
 assert.equal(el('rights-action').value,typedChoice);assert.equal(el('rights-scope').value,'all');assert.equal(el('rights-name').value,'beta');assert(el('rights-broad-confirm').checked,'the complete typed target draft survives an automatic refresh');
 assert.equal(value('rightsDrafts.grant.revision'),'one');assert(value('rightsDrafts.grant.stale'));assert(el('rights-grant').save.disabled);
 assert(!el('rights-editor').open,'a collapsed editor remains collapsed while watch refresh preserves its stale draft');
 await el('rights-grant').onsubmit({preventDefault(){}});assert.equal(writes.length,0,'a changed snapshot cannot silently rebase and submit a dirty draft');
 await context.reviewRightsDraft('grant');assert.equal(value('rightsDrafts.grant.revision'),'two');
 const saving=el('rights-grant').onsubmit({preventDefault(){}});await until(()=>pendingWrite);
 assert(el('rights-grant').save.disabled,'an in-flight form edit disables duplicate submission');
 await el('rights-grant').onsubmit({preventDefault(){}});assert.equal(writes.length,1);
 assert.equal(writes[0].revision,'two');
 assert.equal(writes[0].ttl_ms,3600000);assert.equal(writes[0].why,'Latest reason');
 assert.equal(writes[0].resource_profile,typedKey.ResourceProfile);assert.equal(writes[0].scope,'all');assert(!('credential_name' in writes[0]),'an All edit carries no stale individual target');
 policy={...policy,Revision:'three'};take().resolve({Outcome:'changed',Revision:'three'});await until(()=>observes.length===1);
 pendingWrite({Outcome:'conflict',Revision:'three',Current:policy.Rules[0]});pendingWrite=null;await saving;
 assert.equal(writes.length,1,'a conflicting mutation is never replayed');assert.equal(value('rightsDrafts.grant.revision'),'two');assert(value('rightsDrafts.grant.stale'));assert.equal(el('rights-program').value,'/apps/latest-draft');
 assert.equal(el('rights-ttl').value,'3600000');assert.equal(el('rights-why').value,'Latest reason','a conflict retains the complete proposed effect for explicit review');
 assert.equal(el('rights-scope').value,'all');assert.equal(el('rights-name').value,'beta');
 await context.reviewRightsDraft('grant');
 assert.equal(new URLSearchParams(observes[0].path.split('?')[1]).get('refresh_at'),'2030-01-01T00:00:00.000Z');
 policy={...policy,Rules:[{...policy.Rules[0],Expired:true}]};take().resolve({Outcome:'changed',Revision:'three'});await until(()=>observes.length===1);
 assert.equal(value('rightsRevision'),'three');assert(value('rightsDrafts.grant.stale'),'unchanged-revision expiry requires draft review');assert.equal(new URLSearchParams(observes[0].path.split('?')[1]).get('refresh_at'),null,'service-confirmed expiry consumes the refresh hint');
 take().resolve({Outcome:'unavailable'});await until(()=>timers.size===1);
 assert.equal(el('rights-admin-access').hidden,true,'an unavailable observer clears administrative admission');
 assert.equal(value('rightsAvailable'),false);await el('rights-grant').onsubmit({preventDefault(){}});assert.equal(writes.length,1,'outage suppresses edits');
 assert(el('rights-add').disabled,'authority loss disables adding while helper disclosures are collapsed');
 resumeRetry();await until(()=>observes.length===1);
 take().resolve({Outcome:'unchanged',Revision:'three'});await until(()=>observes.length===1);assert(value('rightsAvailable'),'recovery requires a fresh list even at the same revision');
 assert.equal(el('rights-admin-access').hidden,false,'recovery restores admission only through the fresh authorized list');
 take().resolve({Outcome:'forbidden'});await until(()=>timers.size===1);
 assert.equal(el('rights-admin-access').hidden,true,'a forbidden observer clears administrative admission');
 resumeRetry();await until(()=>observes.length===1);
 take().resolve({Outcome:'unchanged',Revision:'three'});await until(()=>observes.length===1);
 assert.equal(el('rights-admin-access').hidden,false);
 take().reject(Error('Observation connection lost'));await until(()=>timers.size===1);
 assert.equal(el('rights-admin-access').hidden,true,'a lost observation connection clears administrative admission');
 resumeRetry();await until(()=>observes.length===1);
 take().resolve({Outcome:'unchanged',Revision:'three'});await until(()=>observes.length===1);
 assert.equal(el('rights-admin-access').hidden,false);
 deferGet=true;take().resolve({Outcome:'changed',Revision:'four'});await until(()=>pendingGet);
 events.get('pagehide')();assert(pendingGetSignal.aborted,'page exit aborts the queued refresh request');pendingGet({...policy,Revision:'four'});await new Promise(resolve=>setImmediate(resolve));
 assert.equal(el('rights-admin-access').hidden,true,'page exit clears administrative admission and a late response cannot restore it');
 assert.equal(value('rightsRevision'),'three','a late cancelled list cannot replace the current snapshot');assert.equal(value('rightsWatchRun'),null);assert.equal(timers.size,0);
 console.log('PASS: Permissions follow automatically; changes/expiry preserve stale draft base and values; no mutation replay; outage/recovery and page cancellation are bounded');
})().catch(err=>{context.stopRightsWatch();console.error(err);process.exitCode=1});
