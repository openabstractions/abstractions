// Page tests execute the actual generated codecs in a VM. This loader supplies
// the generated modules' static imports without a browser or package install.
const fs = require('fs'), path = require('path'), nodeVM = require('vm');
const sdk = path.join(__dirname, 'static/native-sdk'), decoder = new TextDecoder();
const families = {
 resource:'abstraction-resource/javascript/js/abstraction/resource/internal.mjs',
 logging:'abstraction-logging/javascript/js/abstraction/logging/internal.mjs',
 facade:'abstraction-facade/javascript/js/abstraction/facade/internal.mjs',
 inference:'abstraction-inference/javascript/js/abstraction/inference/api/internal.mjs',
 job:'abstraction-job/javascript/js/abstraction/job/acceptance/internal.mjs',
};
const aliases = {
 '@openabstractions/rights/internal':'abstraction-rights/javascript/js/abstraction/rights/api/internal.mjs',
 '@openabstractions/job-acceptance/internal':families.job,
};
const modules = new Map();
function load(filename) {
 filename = path.resolve(filename);
 if (modules.has(filename)) return modules.get(filename);
 let source = fs.readFileSync(filename,'utf8');
 const dependencies = {}, exports = [...source.matchAll(/export\s+(?:async\s+)?(?:function|const|class)\s+(\w+)/g)].map(m=>m[1]);
 source = source.replace(/import \* as (\w+) from "([^"]+)";/g,(_,name,spec)=>{
  dependencies[spec]=load(aliases[spec]?path.join(sdk,aliases[spec]):path.resolve(path.dirname(filename),spec));
  return 'const '+name+'=dependencies['+JSON.stringify(spec)+'];';
 });
 const dynamic = {};
 source = source.replace(/import\('([^']+)'\)/g,(_,spec)=>{
  dynamic[spec]=load(path.resolve(path.dirname(filename),spec.replace(/^\.\/sdk\//,'./native-sdk/')));
  return 'Promise.resolve(dynamic['+JSON.stringify(spec)+'])';
 });
 source=source.replace(/\bexport\s+(?=(?:async\s+)?(?:function|const|class)\b)/g,'');
 const factory=nodeVM.runInNewContext('(function(dependencies,dynamic){'+source+';return {'+exports.join(',')+'}})',{TextEncoder,TextDecoder,Uint8Array});
 const module=factory(dependencies,dynamic);modules.set(filename,module);return module;
}
const codecs=Object.fromEntries(Object.entries(families).map(([name,file])=>[name,load(path.join(sdk,file))]));
const bridge=load(path.join(__dirname,'static/panel-records.mjs'));
const go=fs.readFileSync(path.join(__dirname,'panel_records.go'),'utf8'), marker='const panelRecordScript = `';
const start=go.indexOf(marker)+marker.length, script=go.slice(start,go.indexOf('`',start));

function nativeJSON(family,name,value,patch={}) {
 const owner=codecs[family];
 // The migrated record fixtures use i64 counters/PIDs; native JS carries them as bigint.
 const camel=value=>typeof value==='number'&&Number.isInteger(value)?BigInt(value):Array.isArray(value)?value.map(camel):value&&typeof value==='object'?Object.fromEntries(Object.entries(value).map(([key,value])=>[key[0].toLowerCase()+key.slice(1).replace(/_([a-z])/g,(_,c)=>c.toUpperCase()),camel(value)])):value;
 const typed=Object.assign(owner['new'+name](),camel(value),patch);
 if(name==='ResourceState')typed.holders=typed.holders.map(holder=>Object.assign(owner.newHolder(),holder));
 if(name==='HistoryUsageResult'&&typed.usage)typed.usage=Object.assign(owner.newHistoryUsage(),typed.usage);
 if(name==='CallerObservation')typed.attributes=typed.attributes.map(attribute=>Object.assign(owner.newCallerAttribute(),attribute));
 if(name==='SelectionReadV2'&&typed.policy){typed.policy=Object.assign(owner.newSelectionPolicyV2(),typed.policy);typed.policy.candidates=typed.policy.candidates.map(candidate=>Object.assign(owner.newSelectionCandidateV2(),candidate))}
 return decoder.decode(owner['encode'+name](typed));
}
function fixture(pathname,value) {
 const route=String(pathname).split('?')[0];
 if(route==='/card/table'&&value&&value.resources) {
  const titles={...(value.titles||{})};
  const resources=value.resources.map(resource=>{
   if(typeof resource==='string')return resource;
   const holders=(resource.holders||[]).map(holder=>{
    if(holder.title)titles[holder.program]=holder.title;
    const {title,...native}=holder;
    return JSON.parse(nativeJSON('resource','Holder',native,{evidence:native.evidence||'claimed'}));
   });
   return nativeJSON('resource','ResourceState',{...resource,holders},{observed:resource.observed||'2026-11-04T00:00:00Z'});
  });
  return {...value,resources,titles};
 }
 if(route==='/card/storage/logs'&&typeof value!=='string')return nativeJSON('logging','HistoryUsageResult',value);
 if(route==='/identity'&&value&&value.runtime&&value.runtime.outcome&&!value.runtime.record){
  const {absent,error,accountName,...record}=value.runtime;
  return {...value,runtime:{absent,error,accountName,record:nativeJSON('facade','CallerObservation',record)}};
 }
 if(route==='/registry/selection'&&value){
  value={...value};
  if(value.read&&typeof value.read!=='string')value.read=nativeJSON('inference','SelectionReadV2',value.read);
  if(value.change&&typeof value.change!=='string')value.change=nativeJSON('inference','SelectionChange',value.change);
 }
 if(route.startsWith('/setup/')&&value&&value.progress){
  value={...value,progress:Object.fromEntries(Object.entries(value.progress).map(([key,progress])=>[key,typeof progress==='string'?progress:nativeJSON('job','WorkProgress',progress)]))};
 }
 return value;
}
// Read the product presentation facts for the Node-only page fixtures.
function readProductTitles() {
 const owner=fs.readFileSync(path.join(__dirname,'../mcp-gateway/product/engines.go'),'utf8');
 const ids=Object.fromEntries([...owner.matchAll(/^\s*(\w+)\s*=\s*("[^"]*")$/gm)].map(([,id,value])=>[id,JSON.parse(value)]));
 const rows=owner.match(/var enginePresentation = map\[string\]struct\{ title, caption string \}\{([\s\S]*?)^}/m)[1].trim().split(/\r?\n/);
 const titles=Object.create(null);
 for(const row of rows){
  const [,id,title]=row.match(/^\s*(\w+):\s*\{(?:title:\s*)?("[^"]*")(?:,\s*"[^"]*")?\},\s*$/);
  if(!Object.hasOwn(ids,id))throw Error('unknown provider title key: '+id);
  titles[ids[id]]=JSON.parse(title);
 }
 const engines=Object.assign(Object.create(null),titles),resources=Object.create(null);
 for(const [id,title] of Object.entries(titles)){
  if(id.endsWith('-image'))engines[id.slice(0,-6)]??=title;
 }
 const names=JSON.parse(fs.readFileSync(path.join(__dirname,'../mcp-gateway/product/model_titles.json'),'utf8'));
 for(const {provider,title,engines: engineIDs=[],resources: resourceIDs=[]} of names){
  const value=provider?titles[provider]:title;
  if(!value)throw Error('unknown model presentation title: '+provider);
  for(const id of engineIDs)engines[id]=value;
  for(const id of resourceIDs)resources[id]=value;
 }
 return {engines,resources};
}
const productTitles = readProductTitles();
const prepared = new WeakSet();
function prepare(context) {
 if(!context||prepared.has(context))return context;
 prepared.add(context);context.oaRecordModule=Promise.resolve(bridge);
 context.oaProductTitles??=productTitles;
 (nodeVM.isContext(context)?nodeVM.runInContext:nodeVM.runInNewContext)(script,context);
 if(context.fetch){const fetch=context.fetch;context.fetch=async(url,...args)=>{
  const response=await fetch(url,...args);
  return new Proxy(response,{get(target,key){if(key==='json')return async()=>fixture(url,await target.json());const value=Reflect.get(target,key,target);return typeof value==='function'?value.bind(target):value}});
 }}
 if(context.call){const call=context.call;context.call=async(url,...args)=>context.oaPanelResponse(url,fixture(url,await call(url,...args)))}
 return context;
}
const vm={...nodeVM,
 runInNewContext(source,context,options){return nodeVM.runInNewContext(source,prepare(context),options)},
 runInContext(source,context,options){return nodeVM.runInContext(source,prepare(context),options)},
};
module.exports={vm,codecs,bridge,fixture,prepare,nativeJSON};
