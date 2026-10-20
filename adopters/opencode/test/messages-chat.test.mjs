import test from 'node:test';
import assert from 'node:assert/strict';
import {tool} from '@opencode-ai/plugin/tool';
import {createMessages,messageTools,messageToolNames,messageInstructions,validHostChatID} from '../messages.js';
import {jsonTool} from '../v2.js';

const result=value=>({structuredContent:value});
const context=(id,abort=new AbortController().signal)=>({sessionID:id,abort});

test('the V2 metadata shim leaves native wrappers unavailable without a validating host tool API',async()=>{
  let reads=0,children=0;
  const owner=createMessages({tool:jsonTool,directory:'owned',helperProgram:()=> 'C:/kept/oa-opencode-integration.exe',
    client:{session:{get:async()=>{reads++;throw Error('unsupported V2 session API');}}},open:()=>{children++;throw Error('must not launch');}});
  try{
    assert.deepEqual(JSON.parse(await owner.definitions.oa_models_list.execute({},context('ses_A'))),{outcome:'unavailable',reason:'native_helper_unavailable'});
    assert.equal(reads,0);assert.equal(children,0);
  }finally{await owner.close();}
});
function fixture({limit=32,ready=async()=>true,get,titleUpdates=true,start}={}){
  const opened=[],calls=[],closed=[],live=new Set(['ses_A','ses_B','ses_C']),bodies=new Map(),titles=new Map();
  let hostReads=0,program='C:/kept/oa-opencode-integration.exe',handler,closeFailure=false,startFailure=false;
  const owner=createMessages({tool,directory:'owned',limit,ready,environment:{},helperProgram:()=>program,
    client:{session:{get:async(request)=>{hostReads++;return get?get(request):live.has(request.path.id)?{data:{id:request.path.id,directory:'owned',title:titles.get(request.path.id)}}:{error:'missing'};},promptAsync:async()=>{throw Error('idle waking is off');}}},
    open:({program,id,title})=>{
      opened.push({program,id,title});let ended=false;
      return {initialize:async()=>({}),isClosed:()=>ended,
        async call(name,args,options){
          calls.push({id,name,args,signal:options.signal});
          if(name==='oa_opencode_session_start')return start?start(id):result(startFailure||!titleUpdates&&Object.keys(args).length?{outcome:'invalid'}:{outcome:'observed',session_id:id,context:'canonical-'+id});
          if(handler)return handler(name,args,options,id);
          if(name==='oa_messages_inbox_open')return result({outcome:'opened'});
          if(name==='oa_messages_wait')return result({outcome:'page',cursor:'cursor-'+id,messages:bodies.get(id)??[]});
          return result({outcome:'page'});
        },async close(){ended=true;closed.push(id);if(closeFailure)throw Error('owned child still live');}};
    }});
  return {owner,opened,calls,closed,bodies,titles,live,context,hostReads:()=>hostReads,setProgram:value=>{program=value;},handle:value=>{handler=value;},
    failClose:value=>{closeFailure=value;},failStart:value=>{startFailure=value;},
    execute:(name,args={},id='ses_A',signal)=>owner.definitions[name].execute(args,context(id,signal))};
}
const event=(type,id='ses_A',extra={})=>({event:{type,properties:type==='session.status'
  ?{sessionID:id,status:{type:'idle'},...extra}:{info:{id,directory:'owned',...extra}}}});

test('native helper setup reports a safe stage and never dispatches before binding',async()=>{
  const secret='raw native error with C:/private/session and bearer token';
  for(const caseName of ['host_chat','host_reply_mismatch','host_lookup','helper_selection','helper_launch','helper_initialize','helper_binding_refused','helper_binding']){
    const stage=caseName==='host_reply_mismatch'?'host_chat':caseName;
    const calls=[];
    const owner=createMessages({tool,directory:'owned',
      helperProgram:()=>stage==='helper_selection'?'':'C:/kept/oa-opencode-integration.exe',
      client:{session:{get:async({path:{id}})=>{
        if(stage==='host_lookup')throw Error(secret);
        return {data:{id:caseName==='host_reply_mismatch'?'ses_other':id,directory:caseName==='host_chat'?'another project':'owned'}};
      }}},
      open:()=>{
        if(stage==='helper_launch')throw Error(secret);
        return {
          initialize:async()=>{if(stage==='helper_initialize')throw Error(secret);},
          call:async(name)=>{
            calls.push(name);
            return result(stage==='helper_binding_refused'?{outcome:'not_permitted',reason:secret}
              :{outcome:'observed',session_id:'ses_other',context:secret});
          },
          close:async()=>{},
        };
      },
    });
    try{
      const raw=await owner.definitions.oa_messages_inbox_open.execute({},context('ses_A'));
      const answer=JSON.parse(raw);
      assert.equal(answer.outcome,'unavailable');
      assert.equal(answer.reason,'native_helper_unavailable');
      assert.equal(answer.stage,stage);
      assert.ok(answer.guidance);
      assert.ok(!raw.includes('private')&&!raw.includes('bearer')&&!raw.includes('ses_other'));
      assert.deepEqual(calls,stage.startsWith('helper_binding')?['oa_opencode_session_start']:[]);
    }finally{await owner.close();}
  }
});

const nativeStart=(value,isError)=>({isError,content:[{type:'text',text:JSON.stringify(value)}]});

test('native text-only pending start retries the same chat before dispatch',async()=>{
  let starts=0;
  const f=fixture({start:id=>{
    starts++;
    return starts<=2 ? nativeStart({outcome:'unavailable',session_id:id,context:'',...(starts===2?{adapter:'accepted'}:{})},true)
      : nativeStart({outcome:'observed',session_id:id,context:'canonical-'+id,adapter:'accepted'},false);
  }});
  try{
    assert.deepEqual(JSON.parse(await f.execute('oa_messages_inbox_open')),{outcome:'opened'});
    assert.deepEqual(f.calls.map(({name})=>name),[
      'oa_opencode_session_start','oa_opencode_session_start','oa_opencode_session_start','oa_messages_inbox_open',
    ]);
    assert.equal(f.opened.length,1);
  }finally{await f.owner.close();}
});

test('terminal or mismatched native text-only start errors stay private and stop',async()=>{
  const hidden='raw error in C:/private/session with bearer token';
  for(const denied of [
    {outcome:'invalid'},
    {outcome:'forbidden',session_id:'ses_A',context:'',detail:hidden},
    {outcome:'unavailable',session_id:'ses_foreign',context:''},
    {outcome:'unavailable',session_id:'ses_A',context:'',detail:hidden},
  ]){
    const f=fixture({start:()=>nativeStart(denied,true)});
    try{
      const raw=await f.execute('oa_messages_inbox_open');
      assert.deepEqual(JSON.parse(raw),{outcome:'unavailable',reason:'native_helper_unavailable',
        stage:'helper_binding_refused',guidance:'The helper refused this chat binding. Reopen this chat and check OpenCode in the Abstraction Panel.'});
      assert.ok(!raw.includes(hidden)&&!raw.includes('ses_foreign'));
      assert.deepEqual(f.calls.map(({name})=>name),['oa_opencode_session_start']);
    }finally{await f.owner.close();}
  }
});

test('Messages definitions return a Promise for both refused and dispatched calls',async()=>{
  const f=fixture();
  try{
    const refused=f.execute('oa_messages_wait',{});
    assert.equal(typeof refused?.then,'function');
    assert.deepEqual(JSON.parse(await refused),{outcome:'invalid'});
    assert.equal(f.hostReads(),0);
    assert.deepEqual(f.calls,[]);

    const requested=f.execute('oa_messages_wait',{wait_ms:0});
    assert.equal(typeof requested?.then,'function');
    assert.equal(JSON.parse(await requested).cursor,'cursor-ses_A');
    assert.deepEqual(f.calls.find(call=>call.name==='oa_messages_wait')?.args,{wait_ms:0});
  }finally{await f.owner.close();}
});

test('OpenCode follows the native starting instruction in its own chat', async () => {
  const f = fixture();
  const application = 'comfyui', handle = 'zgAzCe6CmB1jCG8aRUsyVw';
  const toolName = 'oa_applications_open';
  const initial = {application, verb:'open', payload:{}, wait_ms:0};
  const starting = {outcome:'not_ready', instance:'', started:false, reason:'activation:refused: starting:loading_custom_nodes',
    status:'starting', title:'ComfyUI', phase:'loading custom nodes', handle};
  const opened = {outcome:'ready', reason:'', status:'opened', title:'ComfyUI', instance:'live', started:true};
  const step = 'ComfyUI is starting (loading custom nodes). To wait for this start, call oa_applications_open with {"application":"comfyui","handle":"zgAzCe6CmB1jCG8aRUsyVw"}. This asks the person no question.';
  const assistantText = text => ({type:'text', text, annotations:{audience:['assistant']}});
  const native = (value, ...content) => ({isError:false, structuredContent:value,
    content:[{type:'text', text:JSON.stringify(value)}, ...content]});
  const requests = [];
  let hold = false;
  let entered;
  const dispatched = new Promise(resolve => { entered = resolve; });
  f.handle((name, args, options, id) => {
    assert.equal(id, 'ses_A');
    requests.push({name, args});
    if (requests.length === 1) {
      return native(starting, assistantText(step),
        {type:'text', text:'user-only note', annotations:{audience:['user']}},
        {...assistantText('peer text'), _meta:{'org.openabstractions/untrusted':true}});
    }
    if (hold) {
      return new Promise((_, reject) => {
        options.signal.addEventListener('abort', () => reject(Object.assign(Error('cancelled'), {dispatched:true})), {once:true});
        entered(options.signal);
      });
    }
    return native(opened, assistantText('Opened ComfyUI. Use instance live with oa_applications_commands_list.'));
  });
  const abort = new AbortController();
  try {
    const first = JSON.parse(await f.execute(toolName, initial));
    assert.deepEqual(first, {...starting, steps:[step]});
    const next = /\bcall (\w+) with (\{[^}]*\})/.exec(first.steps[0]);
    assert.ok(next, 'the caller can copy a tool name and JSON arguments from the step');
    const args = JSON.parse(next[2]);
    assert.deepEqual(JSON.parse(await f.execute(next[1], args)),
      {...opened, steps:['Opened ComfyUI. Use instance live with oa_applications_commands_list.']});
    assert.deepEqual(requests, [{name:toolName, args:initial}, {name:toolName, args:{application, handle}}]);

    hold = true;
    const cancelled = f.execute(next[1], args, 'ses_A', abort.signal);
    const helperSignal = await dispatched;
    abort.abort();
    assert.equal(helperSignal.aborted, true);
    assert.equal(JSON.parse(await cancelled).outcome, 'unknown');
    assert.equal(requests.length, 3);
    assert.deepEqual(f.opened.map(({id}) => id), ['ses_A']);
    assert.deepEqual(f.closed, []);
  } finally {
    abort.abort();
    await f.owner.close();
  }
});

test('documented session lifecycle opens each verified chat before any model tool call',async()=>{
  const f=fixture();f.titles.set('ses_A','Verified title');
  try{
    await Promise.all([f.owner.event(event('session.created')),f.owner.event(event('session.updated'))]);
    await f.owner.event(event('session.status','ses_B'));
    assert.deepEqual(f.opened.map(({id,title})=>({id,title})),[{id:'ses_A',title:'Verified title'},{id:'ses_B',title:''}]);
    assert.ok(f.calls.every(call=>call.name==='oa_opencode_session_start'));
    assert.ok(f.calls.every(call=>Object.keys(call.args).length===0));
    await f.execute('oa_messages_inbox_open');assert.equal(f.opened.length,2);
  }finally{await f.owner.close();}
});

test('message access shows its running status and preserves both native requests and answers',async()=>{
  const f=fixture();
  const pending=new Map();
  const displayed=[];
  const deferred=()=>{
    let resolve;
    const promise=new Promise(done=>{resolve=done;});
    return {promise,resolve};
  };
  const started=deferred();
  f.handle((name,args,options)=>{
    if(name!=='oa_messages_request_access')return result({outcome:'page'});
    assert.equal(options.timeout,125000);
    assert.equal(options.signal.aborted,false);
    const response=deferred();
    pending.set(args.idempotency_key,response);
    if(pending.size===2)started.resolve();
    return response.promise;
  });
  const toolContext={...context('ses_A'),metadata:value=>displayed.push(value)};
  const request=(receiver,idempotency_key)=>f.owner.definitions.oa_messages_request_access.execute({receiver,idempotency_key},toolContext);
  const first=request('Claude','same-claude-key');
  const second=request('Codex','same-codex-key');
  try{
    await started.promise;
    const status={title:'Checking message access. If OpenAbstractions asks, answer in its permission popup.'};
    assert.deepEqual(displayed,[status,status]);
    pending.get('same-codex-key').resolve(result({outcome:'pending',answer:{id:'question-1'}}));
    assert.deepEqual(JSON.parse(await second),{outcome:'pending',answer:{id:'question-1'}});
    pending.get('same-claude-key').resolve(result({outcome:'permitted',policy_revision:'current'}));
    assert.deepEqual(JSON.parse(await first),{outcome:'permitted',policy_revision:'current'});
    await f.owner.definitions.oa_messages_inboxes_list.execute({},toolContext);
    assert.deepEqual(displayed,[status,status]);
    assert.deepEqual(f.calls.filter(call=>call.name==='oa_messages_request_access').map(call=>call.args),[
      {receiver:'Claude',idempotency_key:'same-claude-key'},{receiver:'Codex',idempotency_key:'same-codex-key'},
    ]);
  }finally{
    for(const response of pending.values())response.resolve(result({outcome:'unavailable'}));
    await f.owner.close();
  }
});

test('event payloads cannot supply a host session, directory, title or native readiness proof',async()=>{
  let ready=false;
  const f=fixture({ready:async()=>ready});f.titles.set('ses_A','Host title');
  try{
    await f.owner.event(event('session.created','ses_A',{title:'Forged event title'}));
    assert.equal(f.opened.length,0);ready=true;
    for(const input of [event('session.created','unknown'),event('session.updated','ses_A',{directory:'foreign'}),
      event('session.status','bad\nchat'),event('session.idle'),event('unknown')])await f.owner.event(input);
    assert.equal(f.opened.length,0);
    await f.owner.event(event('session.updated','ses_A',{title:'Forged event title'}));
    assert.equal(f.opened[0].title,'Host title');
    await f.owner.suspend();f.setProgram('');await f.owner.event(event('session.status'));
    assert.equal(f.opened.length,1);
  }finally{await f.owner.close();}
  for(const data of [{id:'ses_B',directory:'owned'},{id:'ses_A',directory:'foreign'}]){
    const denied=fixture({get:async()=>({data})});
    try{await denied.owner.event(event('session.status'));assert.equal(denied.opened.length,0);}
    finally{await denied.owner.close();}
  }
});

test('lifecycle startup failure, deletion and helper replacement retain the existing cleanup owner',async()=>{
  const f=fixture();
  try{
    f.failStart(true);await f.owner.event(event('session.created'));
    assert.deepEqual(f.closed,['ses_A']);
    f.failStart(false);await f.owner.event(event('session.status'));
    f.setProgram('C:/updated/oa-opencode-integration.exe');await f.owner.event(event('session.updated'));
    assert.equal(f.opened.length,3);assert.equal(f.closed.length,2);
    await f.owner.event(event('session.deleted'));assert.equal(f.closed.length,3);
    await f.owner.event(event('session.status'));assert.equal(f.opened.length,4);
    await f.owner.close();await f.owner.event(event('session.status'));assert.equal(f.opened.length,4);
  }finally{await f.owner.close();}
});

test('deletion during host verification fences late lifecycle startup',async()=>{
  let release;
  const f=fixture({get:()=>new Promise(resolve=>{release=resolve;})});
  const starting=f.owner.event(event('session.status'));
  await f.owner.event(event('session.deleted'));
  release({data:{id:'ses_A',directory:'owned',title:'Host'}});await starting;
  assert.equal(f.opened.length,0);await f.owner.close();
});

test('canonical wrappers use all generated definitions and refuse own-chat arguments before launch',async()=>{
  const f=fixture();
  try{
    assert.equal(messageToolNames.length,messageTools.length);assert.equal(new Set(messageToolNames).size,messageTools.length);
    assert.ok(messageToolNames.every(name=>name.startsWith('oa_')&&!name.startsWith('OpenAbstractions_')));
    assert.deepEqual(Object.keys(f.owner.definitions),messageToolNames);
    assert.deepEqual(Object.keys(f.owner.definitions.oa_messages_inbox_open.args),[]);
    for(const args of [{context:'forged'},{chat_id:'ses_B'},{sessionID:'ses_B'},{native_actor:'foreign'}])
      assert.deepEqual(JSON.parse(await f.execute('oa_messages_inbox_open',args)),{outcome:'invalid'});
    assert.equal(f.hostReads(),0);assert.equal(f.opened.length,0);
    assert.ok(f.owner.definitions.oa_messages_prepare.args.context,'authorized destination selection remains');
    assert.ok(f.owner.definitions.oa_messages_send.args.context,'native prepared destination stays exact');
    for(const metadata of messageTools)assert.equal(f.owner.definitions[metadata.name].description,metadata.description);
    assert.match(messageInstructions,/oa_messages_prepare/);assert.ok(!messageInstructions.includes('OpenAbstractions_oa_'));
  }finally{await f.owner.close();}
});

test('full public native wrappers preserve nested arguments and use each verified chat child',async()=>{
  const f=fixture();
  const arguments_={command:{inputs:[null,true,7,{nested:{arbitrary:['kept']}}]},extra:'preserved'};
  const args={application:'app:example',instance:'instance',tool:'run',arguments:arguments_};
  try{
    // The native emitted AppModel schema owns the exact input fields.
    const metadata=messageTools.find(value=>value.name==='oa_applications_command_call');
    assert.ok(metadata);
    assert.equal(JSON.parse(await f.execute(metadata.name,args,'ses_A')).outcome,'page');
    assert.equal(JSON.parse(await f.execute(metadata.name,args,'ses_B')).outcome,'page');
    const calls=f.calls.filter(row=>row.name===metadata.name);
    assert.deepEqual(calls.map(row=>row.id),['ses_A','ses_B']);
    assert.deepEqual(calls.map(row=>row.args.arguments),[arguments_,arguments_]);
    assert.deepEqual(JSON.parse(await f.execute(metadata.name,{...args,sessionID:'ses_B'},'ses_A')),{outcome:'invalid'});
    assert.equal(f.calls.filter(row=>row.name===metadata.name).length,2);
    f.handle(()=>result({outcome:'forbidden',reason:'native_policy'}));
    assert.deepEqual(JSON.parse(await f.execute('oa_models_list',{},'ses_A')),{outcome:'forbidden',reason:'native_policy'});
    f.handle(()=>result({outcome:'unsupported'}));
    assert.deepEqual(JSON.parse(await f.execute('oa_jobs_follow',{identity:{key:'accepted',history_epoch:'epoch'},after:'',wait_ms:0},'ses_A')),{outcome:'unsupported'});
    assert.ok(!messageToolNames.some(name=>name.endsWith('_session_start')||name.endsWith('_session_end')));
  }finally{await f.owner.close();}
});

test('two verified host titles stay distinct and peer/model titles never select the own display name',async()=>{
  const f=fixture();f.titles.set('ses_A','Release review');f.titles.set('ses_B','Another chat');
  f.bodies.set('ses_A',[{body:'peer content',chat_title:'Peer override'}]);
  try{
    await f.execute('oa_messages_wait',{wait_ms:0},'ses_A');await f.execute('oa_messages_inbox_open',{},'ses_B');
    assert.deepEqual(f.opened.map(({id,title})=>({id,title})),[{id:'ses_A',title:'Release review'},{id:'ses_B',title:'Another chat'}]);
    assert.equal(JSON.parse(await f.execute('oa_messages_inbox_open',{title:'Model override'})).outcome,'invalid');
    assert.equal(f.opened.length,2);
  }finally{await f.owner.close();}
});

test('unsafe, oversized and absent host titles are dropped; the generated maximum byte title survives',async()=>{
  for(const title of [undefined,'bad\nname','bad\u202ename','\ud800','x'.repeat(801),'😀'.repeat(200)]){
    const f=fixture();f.titles.set('ses_A',title);
    try{await f.execute('oa_messages_inbox_open');assert.equal(f.opened[0].title,title==='😀'.repeat(200)?title:'');}
    finally{await f.owner.close();}
  }
});

test('a rename reaches the open helper without restarting it or cancelling an active wait',async()=>{
  const f=fixture();let held;
  f.titles.set('ses_A','Before');
  f.handle(name=>name==='oa_messages_wait'?new Promise(resolve=>{held=resolve;}):result({outcome:'page'}));
  try{
    const waiting=f.execute('oa_messages_wait',{wait_ms:25000});
    while(!held)await new Promise(resolve=>setImmediate(resolve));
    f.titles.set('ses_A','After');await f.owner.event(event('session.updated'));
    const renamed=()=>f.calls.filter(row=>row.name==='oa_opencode_session_start'&&row.args.chat_title==='After');
    while(!renamed().length)await new Promise(resolve=>setImmediate(resolve));
    assert.equal(f.opened.length,1);assert.equal(f.closed.length,0);
    held(result({outcome:'page',cursor:'retained',messages:[{body:'queued question'}]}));
    assert.equal(JSON.parse(await waiting).messages[0].body,'queued question');
    // A later event with the same title sends nothing more.
    await f.owner.event(event('session.updated'));await f.execute('oa_applications_list');
    for(let n=0;n<5;n++)await new Promise(resolve=>setImmediate(resolve));
    assert.equal(renamed().length,1);
    assert.deepEqual(f.opened.map(({id,title})=>({id,title})),[{id:'ses_A',title:'Before'}]);
    // Unsafe host text never reaches the helper.
    f.titles.set('ses_A','bad\u202ename');await f.owner.event(event('session.updated'));
    while(!f.calls.some(row=>row.args.chat_title===''))await new Promise(resolve=>setImmediate(resolve));
    assert.ok(f.calls.every(row=>row.args.chat_title===undefined||['After',''].includes(row.args.chat_title)));
  }finally{await f.owner.close();}
});

test('each open chat follows its own session title',async()=>{
  const f=fixture();f.titles.set('ses_A','AI abstractions');f.titles.set('ses_B','AI abstractions');
  try{
    await f.owner.event(event('session.created','ses_A'));await f.owner.event(event('session.created','ses_B'));
    f.titles.set('ses_A','Fix inbox titles');f.titles.set('ses_B','Review installer');
    await f.owner.event(event('session.updated','ses_A'));await f.owner.event(event('session.updated','ses_B'));
    const sent=()=>f.calls.filter(row=>row.args.chat_title).map(({id,args})=>id+':'+args.chat_title).sort();
    while(sent().length<2)await new Promise(resolve=>setImmediate(resolve));
    assert.deepEqual(sent(),['ses_A:Fix inbox titles','ses_B:Review installer']);
    assert.equal(f.opened.length,2);assert.equal(f.closed.length,0);
  }finally{await f.owner.close();}
});

test('a helper without title updates is restarted with the new title once idle',async()=>{
  const f=fixture({titleUpdates:false});let held;
  f.titles.set('ses_A','Before');
  f.handle(name=>name==='oa_messages_wait'?new Promise(resolve=>{held=resolve;}):result({outcome:'page'}));
  try{
    const waiting=f.execute('oa_messages_wait',{wait_ms:25000});
    while(!held)await new Promise(resolve=>setImmediate(resolve));
    f.titles.set('ses_A','After');await f.execute('oa_applications_list');
    for(let n=0;n<5;n++)await new Promise(resolve=>setImmediate(resolve));
    assert.equal(f.opened.length,1);assert.equal(f.closed.length,0);
    held(result({outcome:'page',cursor:'retained',messages:[{body:'queued question'}]}));
    assert.equal(JSON.parse(await waiting).messages[0].body,'queued question');
    f.handle(undefined);f.bodies.set('ses_A',[{body:'queued after rename'}]);
    const reopened=JSON.parse(await f.execute('oa_messages_wait',{wait_ms:0}));
    assert.equal(reopened.messages[0].body,'queued after rename');
    assert.deepEqual(f.opened.map(({id,title})=>({id,title})),[{id:'ses_A',title:'Before'},{id:'ses_A',title:'After'}]);
  }finally{await f.owner.close();}
});

test('failed eviction and failed startup cleanup retain counted custody until positive cleanup',async()=>{
  const f=fixture({limit:1});
  try{
    await f.execute('oa_messages_inbox_open');f.failClose(true);
    assert.equal(JSON.parse(await f.execute('oa_messages_inbox_open',{},'ses_B')).outcome,'unavailable');
    assert.equal(f.opened.length,1,'failed eviction cannot allocate beyond the live bound');
    f.failClose(false);await f.execute('oa_messages_inbox_open',{},'ses_B');assert.equal(f.opened.length,2);
    f.failStart(true);f.failClose(true);f.setProgram('C:/new-kept/oa-opencode-integration.exe');
    assert.equal(JSON.parse(await f.execute('oa_messages_inbox_open',{},'ses_C')).outcome,'unavailable');
    assert.equal(f.opened.length,2,'failed old-child replacement retains custody');
    f.failClose(false);await f.execute('oa_messages_inbox_open',{},'ses_C'); // Startup itself is refused and cleaned.
    f.failClose(true);await f.execute('oa_messages_inbox_open',{},'ses_C');
    const count=f.opened.length;
    await f.execute('oa_messages_inbox_open',{},'ses_A');assert.equal(f.opened.length,count,'failed startup cleanup remains counted');
    f.failStart(false);f.failClose(false);await f.execute('oa_messages_inbox_open',{},'ses_A');assert.equal(f.opened.length,count+1);
  }finally{f.failClose(false);await f.owner.close();}
});

test('failed deletion, suspension and disposal preserve owned rows for explicit cleanup retry',async()=>{
  const f=fixture();
  await f.execute('oa_messages_inbox_open');f.failClose(true);
  await assert.rejects(f.owner.event({event:{type:'session.deleted',properties:{info:{id:'ses_A',directory:'owned'}}}}),/still live/);
  assert.equal(JSON.parse(await f.execute('oa_messages_inbox_open')).outcome,'unavailable');assert.equal(f.opened.length,1);
  await assert.rejects(f.owner.suspend(),/did not close/);
  f.failClose(false);await f.execute('oa_messages_inbox_open');assert.equal(f.opened.length,2);
  f.failClose(true);await assert.rejects(f.owner.close(),/did not close/);const attempts=f.closed.length;
  f.failClose(false);await f.owner.close();assert.equal(f.closed.length,attempts+1,'dispose retries the retained owned row');
});

test('two actual host sessions use distinct children and retain their own cursor and queued bodies across reopen',async()=>{
  const f=fixture();
  try{
    f.bodies.set('ses_A',[{body:'only A'}]);f.bodies.set('ses_B',[{body:'only B'}]);
    const a=JSON.parse(await f.execute('oa_messages_wait',{wait_ms:0},'ses_A'));
    const b=JSON.parse(await f.execute('oa_messages_wait',{wait_ms:0},'ses_B'));
    assert.deepEqual(a.messages,[{body:'only A'}]);assert.deepEqual(b.messages,[{body:'only B'}]);assert.notEqual(a.cursor,b.cursor);
    assert.deepEqual(f.opened.map(value=>value.id),['ses_A','ses_B']);
    await f.owner.event({event:{type:'session.deleted',properties:{info:{id:'ses_A',directory:'owned'}}}});
    assert.deepEqual(f.closed,['ses_A']);
    f.bodies.set('ses_A',[{body:'queued while closed'}]);
    const resumed=JSON.parse(await f.execute('oa_messages_wait',{wait_ms:0},'ses_A'));
    assert.deepEqual(resumed.messages,[{body:'queued while closed'}]);assert.equal(resumed.cursor,a.cursor);
    assert.deepEqual(f.opened.map(value=>value.id),['ses_A','ses_B','ses_A']);
  }finally{await f.owner.close();}
});

test('one child handles simultaneous tools without a long Wait starving a foreground call',async()=>{
  const f=fixture();let held;
  f.handle((name,_args,options)=>name==='oa_messages_wait'?new Promise((resolve,reject)=>{held={resolve,reject};options.signal.addEventListener('abort',()=>reject(Object.assign(Error('cancelled'),{dispatched:true})),{once:true});}):result({outcome:'page'}));
  try{
    const abort=new AbortController(),waiting=f.execute('oa_messages_wait',{wait_ms:25000},'ses_A',abort.signal);
    while(!held)await new Promise(resolve=>setImmediate(resolve));
    const foreground=await Promise.race([f.execute('oa_applications_list',{},'ses_A'),new Promise((_,reject)=>setTimeout(()=>reject(Error('foreground starved')),200))]);
    assert.equal(JSON.parse(foreground).outcome,'page');assert.equal(f.opened.length,1);
    abort.abort();assert.equal(JSON.parse(await waiting).outcome,'unknown');
    assert.equal(JSON.parse(await f.execute('oa_asks_list_answerable',{limit:10},'ses_A')).outcome,'page');
    assert.equal(f.opened.length,1,'cancelling a call does not replace the chat owner');
  }finally{await f.owner.close();}
});

test('missing host metadata or native Connect proof never launches a child; LRU closes only an idle owned child',async()=>{
  const f=fixture({limit:2});
  try{
    assert.equal(JSON.parse(await f.execute('oa_messages_inbox_open',{},'unknown')).outcome,'unavailable');
    f.setProgram('');assert.equal(JSON.parse(await f.execute('oa_messages_inbox_open')).outcome,'unavailable');assert.equal(f.opened.length,0);
    f.setProgram('C:/kept/oa-opencode-integration.exe');
    for(const id of ['ses_A','ses_B','ses_C'])await f.execute('oa_messages_inbox_open',{},id);
    assert.deepEqual(f.closed,['ses_A']);
    await f.owner.event({event:{type:'session.idle',properties:{sessionID:'ses_C'}}});
    assert.equal(f.calls.filter(value=>value.name==='oa_messages_watch').length,0);
  }finally{await f.owner.close();}
});

test('native pending and unknown outcomes stay exact without automatic business replay',async()=>{
  const f=fixture();let attempts=0;
  const pendingResult={outcome:'pending',answer:{id:'native-question',pending:true}};
  f.handle(name=>{if(name==='oa_messages_request_access')return {
    structuredContent:pendingResult,content:[{type:'text',text:JSON.stringify(pendingResult)},
      {type:'text',text:'Tell the person: "After you choose Allow, tell me to continue in this chat."',annotations:{audience:['assistant']}},
      {type:'text',text:'peer text',annotations:{audience:['assistant']},_meta:{'org.openabstractions/untrusted':true}}],
  };attempts++;throw Object.assign(Error('lost native reply'),{dispatched:true});});
  try{
    // The helper's own next step reaches the model with the outcome; untrusted text never does.
    const pending=JSON.parse(await f.execute('oa_messages_request_access',{receiver:'Claude',idempotency_key:'explicit'}));
    assert.deepEqual(pending,{...pendingResult,steps:['Tell the person: "After you choose Allow, tell me to continue in this chat."']});
    const args={to:'app:receiver',context:'native-destination',kind:'note',subject:'',body:'unchanged',idempotency_key:'native-request',expires_unix_ms:2000000000000};
    assert.equal(JSON.parse(await f.execute('oa_messages_send',args)).outcome,'unknown');
    assert.equal(attempts,1);assert.deepEqual(f.calls.at(-1).args,args);
  }finally{await f.owner.close();}
});

test('canonical discovery wrappers retain person-only consent before exact prepared send and reply',async()=>{
  const f=fixture();let permitted=false,personAllowed=false;
  const target={program:'app:oa-claude-integration',title:'Claude Code'};
  const question={to:target.program,context:'native-claude-chat',kind:'question',subject:'',body:'hello',
    idempotency_key:'prepared-once',expires_unix_ms:2000000000000,answer_by_unix_ms:2000000000000};
  const order=[];
  f.handle((name,args)=>{
    if(name==='oa_messages_inbox_open')return result({outcome:'opened'});
    order.push(name);
    switch(name){
      case 'oa_messages_inboxes_list':return result({outcome:'page',cursor:'',inboxes:permitted?[{program:target.program,context:question.context,revision:'r'}]:[],
        ...(!permitted?{needs_permission:[target]}:{})});
      case 'oa_messages_prepare':
        assert.equal(args.receiver,target.program);
        return result(permitted?{outcome:'prepared',guidance:'',send_arguments:question}:{outcome:'forbidden',reason:'access:oa-claude-integration',guidance:''});
      case 'oa_messages_request_access':
        assert.equal(args.receiver,target.program);assert.equal(personAllowed,true,'model invocation supplies no person answer');
        permitted=true;return result({outcome:'permitted',policy_revision:'committed-rule'});
      case 'oa_messages_send':assert.deepEqual(args,question);return result({outcome:'sent',message_id:'native-question'});
      case 'oa_messages_wait':assert.equal(args.after,'');return result({outcome:'page',cursor:'next',messages:[{header:{message_id:'native-reply',reply_to:'native-question'},body:'hello back'}]});
      case 'oa_messages_reply':assert.deepEqual(args,{message_id:'native-question-for-this-chat',body:'received'});return result({outcome:'sent'});
      default:throw Error('unexpected tool '+name);
    }
  });
  try{
    // The production wrapper builds validators from generated agent-tools.json.
    // No host-side schema or alias table is substituted for the catalogue.
    const listed=JSON.parse(await f.execute('oa_messages_inboxes_list'));
    assert.deepEqual(listed.inboxes,[]);assert.deepEqual(listed.needs_permission,[target]);
    assert.equal(JSON.parse(await f.execute('oa_messages_prepare',{receiver:target.program,body:'hello'})).outcome,'forbidden');
    personAllowed=true; // inert native-person seam; tool arguments never answer it.
    assert.equal(JSON.parse(await f.execute('oa_messages_request_access',{receiver:target.program,idempotency_key:'person-intent'})).outcome,'permitted');
    const prepared=JSON.parse(await f.execute('oa_messages_prepare',{receiver:target.program,body:'hello'}));
    assert.equal(JSON.parse(await f.execute('oa_messages_send',prepared.send_arguments)).outcome,'sent');
    assert.equal(JSON.parse(await f.execute('oa_messages_wait',{after:'',wait_ms:0})).messages[0].body,'hello back');
    assert.equal(JSON.parse(await f.execute('oa_messages_reply',{message_id:'native-question-for-this-chat',body:'received'})).outcome,'sent');
    assert.deepEqual(order,['oa_messages_inboxes_list','oa_messages_prepare','oa_messages_request_access','oa_messages_prepare','oa_messages_send','oa_messages_wait','oa_messages_reply']);
    assert.equal(f.opened.length,1);assert.ok(f.calls.every(call=>call.id==='ses_A'));
    assert.match(messageInstructions,/needs_permission/);assert.match(messageInstructions,/When inboxes, other_chats and needs_permission are all empty, no agent can be messaged right now\./);
  }finally{await f.owner.close();}
});

test('empty discovery prepares the person message by display name and preserves open-chat guidance',async()=>{
  const f=fixture(),order=[];
  const guidance='Tell the person to open a chat in that app with OpenAbstractions connected, then try again.';
  f.handle((name,args)=>{
    if(name==='oa_messages_inbox_open')return result({outcome:'opened'});
    order.push(name);
    if(name==='oa_messages_inboxes_list')return result({outcome:'page',cursor:'',inboxes:[],needs_permission:[]});
    assert.equal(name,'oa_messages_prepare','empty discovery grants no permission to send or ask');
    assert.deepEqual(args,{receiver:'Claude',body:'hello from the person'});
    return result({outcome:'receiver_unavailable',guidance});
  });
  try{
    const empty=JSON.parse(await f.execute('oa_messages_inboxes_list'));
    assert.deepEqual(empty.inboxes,[]);assert.deepEqual(empty.needs_permission,[]);
    for(const sentence of [
      'When inboxes, other_chats and needs_permission are all empty, no agent can be messaged right now.',
      'oa_messages_prepare checks reach or permission first and sends nothing; its outcome gives the next step.',
    ])assert.ok(messageInstructions.includes(sentence));
    const next=JSON.parse(await f.execute('oa_messages_prepare',{receiver:'Claude',body:'hello from the person'}));
    assert.equal(next.outcome,'receiver_unavailable');assert.equal(next.guidance,guidance);
    assert.ok(!next.guidance.includes('installed'));assert.equal(next.send_arguments,undefined);
    assert.deepEqual(order,['oa_messages_inboxes_list','oa_messages_prepare']);
    assert.equal(f.opened.length,1);
  }finally{await f.owner.close();}
});

test('host correlation reuses generated ChatBinding byte and control bounds',()=>{
  for(const id of ['ses_non_uuid','opaque transcript/name','😀'.repeat(64)])assert.equal(validHostChatID(id),true);
  for(const id of ['',null,'😀'.repeat(65),'bad\nchat','bad\0chat'])assert.equal(validHostChatID(id),false);
});

test('OpenCode preserves an explicit MCP error when the helper cannot stage an image',async()=>{
  const f=fixture();
  const message='gateway: image storage is unavailable';
  f.handle((name,args)=>{
    assert.equal(name,'oa_inference_complete');
    assert.equal(args.image_path,'C:/pictures/generated.png');
    return {isError:true,content:[{type:'text',text:message}]};
  });
  try{
    const out=JSON.parse(await f.execute('oa_inference_complete',{
      model:'text-only',prompt:'Describe this image',hosting:'local',image_path:'C:/pictures/generated.png'
    }));
    assert.deepEqual(out,{outcome:'error',reason:'native_tool_error',message});
  }finally{await f.owner.close();}
});
