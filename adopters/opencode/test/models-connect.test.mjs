import test from 'node:test';
import assert from 'node:assert/strict';
import {tool} from '@opencode-ai/plugin/tool';
import {createModelsConnect,modelsConnectMetadata} from '../models-connect.js';
import Plugin from '../plugin.js';

test('generated provider tool requests under its own native client and reads only its own setup',async()=>{
  const binding=modelsConnectMetadata._meta['org.openabstractions/provider_bindings'].deepseek;
  for(const kind of ['connected','missing','not-servable','conflict','duplicate','declined','pending','answer-without-grant','request-lost']){
    let requests=0,reads=0,intent;
    const permission={async request(value){requests++;intent=value;if(kind==='request-lost')throw Error('private-error');
      return kind==='declined'?{outcome:'answered',answer:{id:'native-question',option:'refuse'}}
        :kind==='pending'?{outcome:'pending',answer:{id:'native-question',pending:true}}:kind==='answer-without-grant'?{outcome:'answered',answer:{id:'native-question',option:'allow'}}:{outcome:'permitted',policyRevision:'native-policy'};}};
    const host={host:binding.Name,base:binding.Base,wire:binding.Kind,credential:binding.Credential,hosted:true,servable:kind!=='not-servable'};
    if(kind==='conflict')host.base='https://person.example';
    const definition=createModelsConnect({tool,resolvePermission:async()=>permission,resolveRouter:async({guarantees})=>{assert.deepEqual(guarantees,modelsConnectMetadata._meta['org.openabstractions/required_router_guarantees']);return {hosts:async()=>{reads++;return {hosts:kind==='missing'?[]:kind==='duplicate'?[host,host]:[host]};}};}});
    const out=JSON.parse(await definition.execute({provider:'deepseek'}));
    const want=kind==='connected'?'connected':kind==='declined'?'declined':kind==='pending'?'pending':kind==='request-lost'?'unknown':'unavailable';
    assert.equal(out.outcome,want,kind);assert.equal(requests,1);assert.equal(intent.purpose,undefined);
    assert.equal(new TextDecoder().decode(intent.selection.selector.target),'credential:deepseek');
    assert.equal(Object.hasOwn(intent,'program'),false);assert.equal(Object.hasOwn(intent,'account'),false);
    if(['pending','declined','unknown'].includes(want))assert.equal(reads,0);
    if(want==='pending'){assert.deepEqual(out.question_ids,['native-question']);assert.match(out.message,/Questions in the OpenAbstractions Panel/);}else assert.equal(Object.hasOwn(out,'question_ids'),false);
  }
});

test('canonical plugin provider connection uses the model program native clients, with no helper call',async()=>{
  let requests=0,hosts=0,children=0;
  const binding=modelsConnectMetadata._meta['org.openabstractions/provider_bindings'].deepseek;
  const plugin=await Plugin({client:{}},{},{tool,env:{},connectionRequired:false,
    resolvePermission:async()=>({request:async()=>{requests++;return {outcome:'permitted',policyRevision:'native-policy'};}}),
    resolveRouter:async(options,need)=>{if(need?.guarantees)assert.deepEqual(need.guarantees,modelsConnectMetadata._meta['org.openabstractions/required_router_guarantees']);return {hosts:async fresh=>{hosts++;assert.equal(fresh,true);return {hosts:[{host:binding.Name,base:binding.Base,wire:binding.Kind,credential:binding.Credential,hosted:true,servable:true}]};}};},
    openMessagesHelper:()=>{children++;throw Error('model connection must use the model caller');},
    probeRuntime:async()=>true,readInventory:async()=>({offers:[]}),readCapability:async()=>false,readSelectionPolicy:async()=>({outcome:'unknown'})});
  try{
    await plugin.config({provider:{openabstractions:{npm:'@openabstractions/opencode',models:{}}}});
    assert.deepEqual(JSON.parse(await plugin.tool.oa_models_connect.execute({provider:'deepseek'},{sessionID:'host-only',abort:new AbortController().signal})),{outcome:'connected'});
    assert.equal(requests,1);assert.equal(hosts,1);assert.equal(children,0);
  }finally{await plugin.dispose();}
});

test('purpose and own-identity extra arguments refuse before native Request',async()=>{
  const definition=createModelsConnect({tool,resolvePermission:async()=>{throw Error('must not resolve');},resolveRouter:async()=>{throw Error('must not read');}});
  for(const args of [{provider:'deepseek',purpose:''},{provider:'deepseek',purpose:'cannot be conveyed'},{provider:'deepseek',program:'foreign'},{provider:'deepseek',sessionID:'foreign'},{provider:'custom'}])assert.equal(JSON.parse(await definition.execute(args)).outcome,'invalid');
  assert.equal(definition.description,modelsConnectMetadata.description);
  // The schema admits provider alone; the description keeps the key rule.
  assert.match(definition.description,/never ask for, pass or return a key/);
});

test('old runtime without setup guarantee cannot raise a native question',async()=>{
  let requests=0;
  const definition=createModelsConnect({tool,resolvePermission:async()=>{requests++;throw Error('must not ask');},resolveRouter:async()=>{throw Error('unsupported guarantee');}});
  assert.deepEqual(JSON.parse(await definition.execute({provider:'deepseek'})),{outcome:'unavailable',reason:'provider_setup_unsupported'});
  assert.equal(requests,0);
});

test('pending wait keeps the actual question once across observe failure and never resubmits',async()=>{
  let requests=0;const ids=[];
  const permission={request:async()=>{requests++;return {outcome:'pending',answer:{id:'native-submitted',pending:true}};},observeQuestion:async id=>{ids.push(id);if(ids.length===1)return {observation:{outcome:'pending',answer:{id,pending:true}}};throw Error('rights:question_pending:never-parse-errors');}};
  const definition=createModelsConnect({tool,resolveRouter:async()=>({hosts:async()=>{throw Error('must not read');}}),resolvePermission:async()=>permission});
  const pending=JSON.parse(await definition.execute({provider:'deepseek'}));
  assert.equal(pending.outcome,'pending');assert.deepEqual(pending.question_ids,['native-submitted']);
  assert.match(pending.message,/Questions in the OpenAbstractions Panel/);
  assert.equal(requests,1);assert.deepEqual(ids,['native-submitted','native-submitted']);
  for(const observed of [{outcome:'unavailable'},{outcome:'denied',policyRevision:'native-policy'}]){
    const noQuestion=createModelsConnect({tool,resolveRouter:async()=>({}),resolvePermission:async()=>({request:async()=>observed})});
    const out=JSON.parse(await noQuestion.execute({provider:'deepseek'}));assert.equal(Object.hasOwn(out,'question_ids'),false);assert.equal(out.outcome,observed.outcome==='denied'?'declined':'unavailable');
  }
});

test('90 second provider wait and cancellation retain the request and Panel Questions cue',async t=>{
  for(const cancel of [false,true]){
    let now=1000,requests=0,reads=0;const waits=[],abort=new AbortController();
    t.mock.method(Date,'now',()=>now);
    const definition=createModelsConnect({tool,resolveRouter:async()=>({hosts:async()=>{reads++;throw Error('must not read');}}),resolvePermission:async()=>({
      request:async()=>{requests++;return {outcome:'pending',answer:{id:'actual-question',pending:true}};},
      observeQuestion:async(id,wait)=>{assert.equal(id,'actual-question');waits.push(Number(wait));now+=Number(wait);if(cancel)abort.abort();return {observation:{outcome:'pending'}};},
    })});
    const out=JSON.parse(await definition.execute({provider:'deepseek'},{abort:abort.signal}));
    assert.equal(out.outcome,'pending');assert.deepEqual(out.question_ids,['actual-question']);
    assert.match(out.message,/Questions in the OpenAbstractions Panel/);
    assert.equal(requests,1);assert.equal(reads,0);assert.equal(now,cancel?26000:91000);
    assert.deepEqual(waits,cancel?[25000]:[25000,25000,25000,15000]);
    t.mock.restoreAll();
  }
});
