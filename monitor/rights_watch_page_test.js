// Native watch is exercised in rights_observe_test.go. This VM drives the real
// browser script through automatic refresh, drafts, conflicts and cancellation.
const fs=require('fs'),vm=require('./panel_record_test_helper.js').vm,assert=require('assert');
const {panelRuleViolations}=require('./static/panel_rules.js');
const fields=new Map(),events=new Map(),timers=new Map();let timerID=0;
function node(tag='div'){return {tagName:tag.toUpperCase(),value:'',checked:false,hidden:false,disabled:false,children:[],listeners:{},attrs:{},get options(){return this.children},replaceChildren(){this.children=[]},setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return Object.hasOwn(this.attrs,k)?this.attrs[k]:null},hasAttribute(k){return Object.hasOwn(this.attrs,k)},append(...items){this.children.push(...items)},addEventListener(name,fn){(this.listeners[name]??=[]).push(fn)},dispatch(name){for(const f of this.listeners[name]||[])f()},querySelector(){return this.save},querySelectorAll(){return this.children.flatMap(n=>n.tagName==='BUTTON'?[n]:n.querySelectorAll?n.querySelectorAll():[])}}}
function el(id){if(!fields.has(id))fields.set(id,node(id.includes('select')||id==='rights-action'?'select':'div'));return fields.get(id)}
for(const id of ['rights-grant'])el(id).save=node('button');
function raw(file,name){const s=fs.readFileSync(__dirname+'/'+file,'utf8'),m='const '+name+' = `',start=s.indexOf(m)+m.length;assert(start>=m.length);return s.slice(start,s.indexOf('`',start))}
const plain=raw('panel_shell.go','plainScript').split('<script>')[1].split('</script>')[0];
const pageSource=fs.readFileSync(__dirname+'/rights_page.go','utf8');
assert(pageSource.includes('id="rights-admin-access" class="oa-expander" hidden>'),'administrative access is hidden before any authorized list');
const markup=pageSource.slice(pageSource.indexOf('var rightsPage = ')).split('<script>')[0];
function disclosed(id){const stack=[];for(const match of markup.matchAll(/<\/?([a-z][a-z0-9-]*)\b[^>]*>/gi)){const tag=match[1],html=match[0];if(html.startsWith('</')){const at=stack.map(n=>n.tag).lastIndexOf(tag);if(at>=0)stack.length=at;continue}if(html.includes('id="'+id+'"'))return stack.some(n=>n.tag==='details'&&!/\bopen(?:[\s=>])/.test(n.html));if(!['input','br','hr'].includes(tag))stack.push({tag,html})}throw Error('missing '+id)}
for(const id of ['rights-grant','assistant-form','questions-history-list','choices-history-list','answered-list'])assert(disclosed(id),'secondary content starts inside a closed native disclosure: '+id);
for(const id of ['rights-status','questions-status','choices-status','assistant-status','answered-status'])assert(!disclosed(id),'service status remains visible with panels collapsed: '+id);
// Owner's Panel rules (2026-10-06): each section opens with its heading and one short caption.
const sections=markup.split('<section').slice(1);
assert.deepEqual(sections.map(section=>/^ id="([^"]+)"/.exec(section)[1]),['questions','rights-section','defaults','assistant','rights-advanced','app-questions'],'Questions waiting, saved permissions, defaults for all apps (VISION 2026-10-07), answering access, Advanced, then past answers (SPEC §4)');
for(const section of sections)assert(/^[^>]*><h2[^>]*>[^<]*<\/h2><p class="oa-caption">[^<]{1,80}<\/p>/.test(section),'section heading with one short caption: '+section.slice(0,80));
for(const id of ['questions-next','rights-next'])assert(new RegExp('id="'+id+'"[^>]*hidden>Show more<').test(markup),'Show more appears only past a page: '+id);
// Questions waiting has one Show more for its three sources (architect
// ruling 2026-11-09, second block).
for(const id of ['choices-next','permissions-next'])assert(!markup.includes('id="'+id+'"'),'no second Show more: '+id);
assert(!/Refresh|Next page|placeholder=|id="rights-allow"/.test(markup),'no Refresh, Next page, typed placeholder or grouped Allow form');
assert(!/ComfyUI|OpenCode/.test(markup),'the permissions page names no adopter');
const page=pageSource.split('<script>')[1].split('</script>')[0].replace('` + serviceCommonScript + `',raw('service_page.go','serviceCommonScript'));
el('rights-admin-access').hidden=true;
const action='opencode/log-source.share',target={Account:'account',Program:'/apps/actual-caller'};
const typedKey={Action:'abstraction.credentials/apply',ResourceProfile:'abstraction.credentials/named-use@2'},typedChoice='selector:'+JSON.stringify([typedKey.Action,typedKey.ResourceProfile]);
const typedDefinition={Definition:typedKey,Label:'Use saved secret',SupportedSelectors:['one','all'],Editable:true};
const unruledAction='fixture/no-saved-rule';
let policy={Outcome:'page',Revision:'one',Catalog:[action,unruledAction],ActionPlain:{[action]:'share event contents',[unruledAction]:'use fixture capability'},Rules:[{Subject:target,Action:action,Resource:'exact-source',Permit:true,Expires:'2030-01-01T00:00:00.000Z',Expired:false}],Definitions:[typedDefinition],Bundles:[],SelectorAvailable:true,Next:'',Complete:true};
const observes=[],writes=[];let getCount=0,pendingWrite=null,pendingGet=null,pendingGetSignal=null,deferGet=false,listingOutcome='page',observationSerial=0;
const ok=value=>({ok:true,json:async()=>value});
const context={console,URLSearchParams,AbortController,location:{search:'?k=test'},localStorage:{length:0,key(){},getItem(){return null},setItem(){}},document:{getElementById:el,createElement:node},setTimeout(fn){const id=++timerID;timers.set(id,fn);return id},clearTimeout(id){timers.delete(id)},addEventListener(name,fn){events.set(name,fn)},fetch:async(path,opts={})=>{
 if(path.startsWith('/rights/observe?'))return new Promise((resolve,reject)=>{const entry={path,resolve:value=>resolve(ok({...value,Cursor:value.Outcome==='changed'?'native-cursor-'+(++observationSerial):new URLSearchParams(path.split('?')[1]).get('after')||'native-initial'})),reject,signal:opts.signal};observes.push(entry);opts.signal.addEventListener('abort',()=>reject(Object.assign(Error('aborted'),{name:'AbortError'})),{once:true})});
 if(path.startsWith('/rights?')){getCount++;if(listingOutcome==='transport')throw Error('Permission listing lost');if(listingOutcome!=='page')return ok({Outcome:listingOutcome});if(deferGet){deferGet=false;pendingGetSignal=opts.signal;return new Promise(resolve=>{pendingGet=value=>resolve(ok(value))})}return ok(policy)}
 if(path==='/rights'&&opts.body){writes.push(JSON.parse(opts.body));return new Promise(resolve=>{pendingWrite=value=>resolve(ok(value))})}
 if(path==='/explore')return ok({self:{account:'account',program:'/apps/panel'},selfAccountName:'Owner',probes:[]});
 if(path==='/card/table')return ok({resources:[]});
 if(path.startsWith('/questions'))return ok({Outcome:'page',Records:[],Next:'',Complete:true});
 if(path==='/rights/delegation')return ok({outcome:'page',revision:policy.Revision,account:'account',questions:[]});
 if(path==='/rights/assistant')return ok({outcome:'page',revision:policy.Revision,assistants:[],candidates:[],warning:{answers:[],staysWithYou:[]}});
 if(path==='/rights/answered')return ok({outcome:'page',answers:[]});
 if(path==='/rights/defaults')return ok({Outcome:'page',Revision:policy.Revision,Defaults:[{Name:'fixture-default',Label:'Fixture default',On:true,Action:action,Resource:'exact-default'}]});
 throw Error('unexpected '+path);
}};context.window=context;vm.createContext(context);vm.runInContext(plain,context);vm.runInContext(page,context);
const value=s=>vm.runInContext(s,context);
async function until(predicate){for(let i=0;i<100;i++){if(predicate())return;await new Promise(resolve=>setImmediate(resolve))}throw Error('expected script progress')}
const take=()=>{assert(observes.length);return observes.shift()};
function resumeRetry(){const [id,retry]=timers.entries().next().value;timers.delete(id);retry()}
function noCurrentPolicy(){assert.equal(value('rightsListedRules.length'),0,'failed policy clears current source rules');assert.equal(el('rights').children.length,0,'failed policy clears rows including raw Technical details');assert.equal(value('rightsRevision'),'','failed policy clears current revision');assert.equal(el('rights-heading').title,'','failed policy clears revision title');assert(el('rights-revision').hidden,'failed policy hides revision disclosure');assert.equal(el('rights-revision-body').textContent,'','failed policy clears raw revision');assert.equal(el('defaults-list').children.length,0,'failed policy clears source-derived defaults and their raw details');assert.equal(value('defaultsRevision'),'','failed policy disables default mutations');assert.equal(el('rights-unruled').children.length,0,'failed policy clears inferred absence of rules and action descriptions')}
async function closeRace(kind){
 const localFields=new Map(),localEvents=new Map();let release,reads=0,observations=0;
 const find=id=>{if(!localFields.has(id))localFields.set(id,node(id.includes('select')||id==='rights-action'?'select':'div'));return localFields.get(id)};find('rights-grant').save=node('button');
 const c={...context,document:{...context.document,getElementById:find},addEventListener:(n,f)=>localEvents.set(n,f),fetch:async(path,opts={})=>{
  if(path.startsWith('/rights?')){reads++;if(kind==='initial'&&reads===1)return new Promise(resolve=>{release=()=>resolve(ok(policy))});return ok(policy)}
  if(path==='/rights'&&opts.body)return new Promise(resolve=>{release=()=>resolve(ok({Outcome:'applied',Current:policy.Rules[0]}))});
  if(path.startsWith('/rights/observe?')){observations++;return new Promise((resolve,reject)=>opts.signal.addEventListener('abort',()=>reject(Object.assign(Error('aborted'),{name:'AbortError'})),{once:true}))}
  return context.fetch(path,opts);
 }};c.window=c;vm.createContext(c);vm.runInContext(plain,c);vm.runInContext(page,c);
 let edit;
 if(kind==='initial')await until(()=>release);else{await c.rightsListReady;await until(()=>observations===1);edit=vm.runInContext('rightsEdit({edit:"revoke",account:"account",program:"/apps/actual-caller",action:"'+action+'",resource:"exact-source"},null)',c);await until(()=>release)}
 localEvents.get('pagehide')();release();if(edit)await edit;await c.rightsListReady;await new Promise(resolve=>setImmediate(resolve));
 assert.equal(reads,1,kind+': close prevents a new policy readback');assert.equal(observations,kind==='initial'?0:1,kind+': close prevents observer startup/restart');assert.equal(vm.runInContext('rightsAvailable',c),false);assert.equal(find('rights').children.length,0,kind+': late completion cannot restore rows');assert.equal(vm.runInContext('rightsWatchRun',c),null);
}
(async()=>{
 assert(el('rights-admin-access').hidden,'identity and page startup alone claim no administrative admission');
 await context.rightsListReady;await until(()=>observes.length===1);
 assert.equal(el('rights-admin-access').hidden,false,'an authorized permission list exposes the Panel administrative role');
 assert(el('rights-unruled').children.length>0,'the source catalog seeds the current no-rule section');
 // The actual saved-selector and preview renderers keep ordinary copy plain.
 // Exact profile/action/target/origin bytes remain available in Technical details.
 const lint=box=>panelRuleViolations(box,{rules:['raw-value','technical-summary']});
 const technical=n=>n.hasAttribute&&n.hasAttribute('data-oa-technical');
 const allText=n=>(n.textContent||'')+(n.children||[]).map(allText).join('\n');
 const plainText=n=>technical(n)?'':(n.textContent||'')+(n.children||[]).map(plainText).join('\n');
 const advancedText=n=>technical(n)?allText(n):(n.children||[]).map(advancedText).join('\n');
 const original=node();original.textContent='Profile: abstraction.credentials/named-secret@1';
 assert(lint(original).some(v=>v.rule==='raw-value'),'original raw-profile counterexample must fail the actual DOM rule');
 const one={Permission:{Action:'abstraction.credentials/secret.read',ResourceProfile:'abstraction.credentials/named-secret@1'},Scope:'one',Name:'fixture-alpha',Target:'fixture-alpha',Editable:true};
 const all={Permission:typedKey,Scope:'all',Target:'All saved application secrets in this account',Editable:true};
 const unknown={Permission:{Action:'unknown/operation',ResourceProfile:'abstraction.credentials/unknown-profile@9'},Scope:'one',Target:'sha256:'+('a'.repeat(64)),Editable:false};
 const members=['apply','secret.read','secret.create','secret.rotate','secret.revoke','secret.metadata.read'].map(a=>({Permission:{Action:'abstraction.credentials/'+a,ResourceProfile:a==='apply'?typedKey.ResourceProfile:one.Permission.ResourceProfile},Scope:'one',Name:'fixture-alpha',Target:'fixture-alpha',Editable:true}));
 const origin={BundleRef:'abstraction.credentials/full-control@1',Digest:'b'.repeat(64),ID:'frozen-origin-id',Remaining:5,Original:6,State:'partial/changed',Members:members,LinkedMembers:members.slice(0,5)};
 for(const selection of [one,all,unknown,{...one,Origin:origin}]){
  const box=node();context.appendSelectorDetails(box,{Selector:selection});
  assert.deepEqual(lint(box),[],'saved selector ordinary/technical DOM');
  const normal=plainText(box),exact=advancedText(box);
  assert(!normal.includes(selection.Permission.ResourceProfile),'ordinary profile code leaked');
  assert(exact.includes(selection.Permission.ResourceProfile)&&exact.includes(selection.Target)&&exact.includes(selection.Scope),'exact selector diagnostics lost');
  if(selection===unknown){assert(normal.includes('unrecognized target type'));assert(normal.includes('View only'));assert(!normal.includes(selection.Target),'opaque unknown target must remain advanced');}
  if(selection.Origin){assert(normal.includes('5 of 6'));assert(exact.includes(origin.Digest)&&exact.includes(origin.ID));for(const m of members)assert(exact.includes(m.Permission.Action),'frozen original member omitted');}
 }
 const savedValues=new Map(['rights-account','rights-program','rights-permit','rights-scope','rights-name','rights-ttl','rights-why','rights-origin-id'].map(id=>[id,el(id).value]));
 el('rights-account').value='account';el('rights-program').value='/apps/actual-caller';el('rights-permit').value='permit';el('rights-origin-id').value='';el('rights-ttl').value='300000';el('rights-why').value='fixture';
 for(const [selection,capability] of [[one,{Definition:one.Permission,Label:'Read secret value',SupportedSelectors:['one']}],[all,{Definition:all.Permission,Label:'Use',SupportedSelectors:['one','all']}],[unknown,{Definition:unknown.Permission,Label:'Unrecognized permission',SupportedSelectors:[]} ]]){
  el('rights-scope').value=selection.Scope;el('rights-name').value=selection.Name||'';el('rights-preview').replaceChildren();
  context.selectorGrantPreview(capability);
  assert.deepEqual(lint(el('rights-preview')),[],'typed effect preview ordinary/technical DOM');
  assert(advancedText(el('rights-preview')).includes(selection.Permission.ResourceProfile),'advanced preview identifier missing');
  if(selection===unknown)assert(!plainText(el('rights-preview')).includes('saved application secrets'),'unknown preview inferred saved-secret authority');
 }
 context.testOrigin=origin;value('rightsOriginDraft=testOrigin');el('rights-origin-id').value=origin.ID;el('rights-permit').value='remove';el('rights-preview').replaceChildren();context.selectorGrantPreview(null);
 assert.deepEqual(lint(el('rights-preview')),[],'bundle removal preview ordinary/technical DOM');
 assert(plainText(el('rights-preview')).includes('5 of 6'));
 assert(advancedText(el('rights-preview')).includes(origin.Digest));
 for(const m of members)assert(advancedText(el('rights-preview')).includes(m.Permission.Action),'complete immutable expansion missing from advanced preview');
 value('rightsOriginDraft=null');delete context.testOrigin;for(const [id,v] of savedValues)el(id).value=v;

 for(const outcome of ['forbidden','unavailable','transport']){
  listingOutcome=outcome;await context.rightsList();
  assert.equal(el('rights-admin-access').hidden,true,'failed listing clears administrative admission: '+outcome);
  noCurrentPolicy();
  listingOutcome='page';await context.rightsList();
  assert.equal(el('rights-admin-access').hidden,false,'fresh authorized listing restores administrative admission');
 }
 const firstQuery=new URLSearchParams(take().path.split('?')[1]);assert.equal(firstQuery.get('revision'),'one','following starts automatically at the listed revision');assert.equal(firstQuery.get('after'),'','the source supplies the initial snapshot cursor');
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
 const beforeExpiry=new URLSearchParams(observes[0].path.split('?')[1]);assert(beforeExpiry.get('after').startsWith('native-cursor-'),'the presenter retains the actual observation cursor');assert.equal(beforeExpiry.get('refresh_at'),null,'expiry belongs to the policy source');
 policy={...policy,Rules:[{...policy.Rules[0],Expired:true}]};take().resolve({Outcome:'changed',Revision:'three'});await until(()=>observes.length===1);
 assert.equal(value('rightsRevision'),'three');assert(value('rightsDrafts.grant.stale'),'unchanged-revision expiry requires draft review');assert.notEqual(new URLSearchParams(observes[0].path.split('?')[1]).get('after'),beforeExpiry.get('after'),'expiry advances the source cursor without changing durable revision');
 take().resolve({Outcome:'unavailable'});await until(()=>timers.size===1);
 assert.equal(el('rights-admin-access').hidden,true,'an unavailable observer clears administrative admission');
 assert.equal(value('rightsAvailable'),false);await el('rights-grant').onsubmit({preventDefault(){}});assert.equal(writes.length,1,'outage suppresses edits');
 noCurrentPolicy();assert.equal(el('rights-program').value,'/apps/latest-draft');assert.equal(el('rights-why').value,'Latest reason');assert.equal(value('rightsDrafts.grant.revision'),'three');assert(value('rightsDrafts.grant.stale'),'outage preserves the stale draft fence');
 assert.equal(el('rights-admin-access').hidden,true,'authority loss leaves the editor without administrative admission while helper disclosures are collapsed');
 resumeRetry();await until(()=>observes.length===1);
 take().resolve({Outcome:'unchanged',Revision:'three'});await until(()=>observes.length===1);assert(value('rightsAvailable'),'recovery requires a fresh list even at the same revision');
 assert.equal(el('rights-admin-access').hidden,false,'recovery restores admission only through the fresh authorized list');
 assert(el('defaults-list').children.length>0,'fresh policy restores source-derived defaults');
 assert(el('rights-unruled').children.length>0,'fresh policy restores current absence claims from its catalog');
 const recoveredGets=getCount;take().resolve({Outcome:'unchanged',Revision:'three'});await until(()=>observes.length===1);assert.equal(getCount,recoveredGets,'healthy unchanged does not relist');assert.equal(value('rightsDrafts.grant.revision'),'three');assert(value('rightsDrafts.grant.stale'),'recovery never silently rebases the dirty draft');
 take().resolve({Outcome:'forbidden'});await until(()=>timers.size===1);
 assert.equal(el('rights-admin-access').hidden,true,'a forbidden observer clears administrative admission');
 noCurrentPolicy();
 resumeRetry();await until(()=>observes.length===1);
 take().resolve({Outcome:'unchanged',Revision:'three'});await until(()=>observes.length===1);
 assert.equal(el('rights-admin-access').hidden,false);
 take().reject(Error('Observation connection lost'));await until(()=>timers.size===1);
 assert.equal(el('rights-admin-access').hidden,true,'a lost observation connection clears administrative admission');
 noCurrentPolicy();
 resumeRetry();await until(()=>observes.length===1);
 take().resolve({Outcome:'unchanged',Revision:'three'});await until(()=>observes.length===1);
 assert.equal(el('rights-admin-access').hidden,false);
 deferGet=true;const invalidatedRead=context.rightsList();await until(()=>pendingGet);
 take().resolve({Outcome:'forbidden'});await until(()=>timers.size===1);pendingGet(policy);pendingGet=null;await invalidatedRead;noCurrentPolicy();
 resumeRetry();await until(()=>observes.length===1);take().resolve({Outcome:'unchanged',Revision:'three'});await until(()=>observes.length===1);
 deferGet=true;take().resolve({Outcome:'changed',Revision:'four'});await until(()=>pendingGet);
 events.get('pagehide')();assert(pendingGetSignal.aborted,'page exit aborts the queued refresh request');pendingGet({...policy,Revision:'four'});await new Promise(resolve=>setImmediate(resolve));
 assert.equal(el('rights-admin-access').hidden,true,'page exit clears administrative admission and a late response cannot restore it');
 noCurrentPolicy();assert.equal(value('rightsWatchRun'),null);assert.equal(timers.size,0);
 const closeResults=await Promise.allSettled(['initial','mutation'].map(closeRace));for(const r of closeResults)if(r.status==='rejected')console.error(r.reason);assert(closeResults.every(r=>r.status==='fulfilled'),'both early and accepted-mutation close races stay closed');
 console.log('PASS: Permissions follow automatically; changes/expiry preserve stale draft base and values; no mutation replay; outage/recovery and page cancellation are bounded');
})().catch(err=>{context.stopRightsWatch();console.error(err);process.exitCode=1});
