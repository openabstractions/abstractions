import test from 'node:test';
import assert from 'node:assert/strict';
import Plugin from '../plugin.js';
import {tool} from '@opencode-ai/plugin/tool';
import {EventEmitter} from 'node:events';
import {PassThrough,Writable} from 'node:stream';
import {createStdioMCP} from '../mcp-stdio.js';
import {defaultHelperTools} from '../tool-config.js';

const config=()=>({provider:{openabstractions:{npm:'@openabstractions/opencode',models:{}}}});
test('verified host lifecycle observes completed native Connect before any canonical tool call',async()=>{
  let ready=false,children=0,closed=0,direct=0,popups=0;
  const calls=[];
  const native=async()=>ready?{outcome:'connected',hostRegistered:true,helperEnrolled:true,helperProgram:'C:/kept/oa-opencode-integration.exe'}:{outcome:'connect_pending'};
  const plugin=await Plugin({directory:'owned',client:{session:{get:async({path})=>({data:{id:path.id,directory:'owned'}}),promptAsync:async()=>{throw Error('idle model work forbidden');}}}}, {},{
    tool,env:{},connectionRequired:true,requestOpenCodeConnection:async()=>{popups++;return native();},checkOpenCodeConnection:native,
    resolveMessages:async()=>{direct++;throw Error('stock client retired');},
    openMessagesHelper:({id,program})=>{children++;assert.equal(program,'C:/kept/oa-opencode-integration.exe');return {initialize:async()=>({}),isClosed:()=>false,
      call:async(name,args)=>{calls.push({name,args});return {structuredContent:name==='oa_opencode_session_start'?{outcome:'observed',session_id:id,context:'canonical'}:{outcome:'page'}};},close:async()=>{closed++;}};},
    resolveApplications:async()=>({announce:async()=>({outcome:'applied',instance:'metadata'}),withdraw:async()=>({outcome:'applied'})}),
    probeRuntime:async()=>true,readInventory:async()=>({offers:[]}),readCapability:async()=>false,readSelectionPolicy:async()=>({outcome:'unknown'}),
  });
  try{
    await plugin.config(config());assert.deepEqual(Object.keys(plugin.tool).sort(),[...defaultHelperTools].sort());
    const call=()=>plugin.tool.oa_asks_list_answerable.execute({limit:10}, {sessionID:'ses_own',abort:new AbortController().signal});
    assert.equal(JSON.parse(await call()).outcome,'connect_pending');assert.equal(children,0);
    await plugin.event({event:{type:'session.created',properties:{info:{id:'ses_own',directory:'owned'}}}});
    assert.equal(children,0);ready=true;
    await plugin.event({event:{type:'session.status',properties:{sessionID:'ses_own',status:{type:'idle'}}}});
    assert.equal(children,1);assert.deepEqual(calls,[{name:'oa_opencode_session_start',args:{}}]);assert.equal(popups,1);
    assert.equal(JSON.parse(await call()).outcome,'page');assert.equal(children,1);assert.equal(direct,0);
    await plugin.event({event:{type:'session.idle',properties:{sessionID:'ses_own'}}});
    await plugin.event({event:{type:'session.deleted',properties:{info:{id:'ses_own',directory:'owned'}}}});
    assert.equal(closed,1);
  }finally{await plugin.dispose();}
});

test('a person-owned global MCP roster suppresses wrappers without modifying that entry',async()=>{
  const plugin=await Plugin({directory:'owned',client:{}},{},{tool,env:{},probeRuntime:async()=>true,readInventory:async()=>({offers:[]}),readCapability:async()=>false,readSelectionPolicy:async()=>({outcome:'unknown'})});
  const doc=config();doc.mcp={OpenAbstractions:{type:'local',command:['person-owned-helper']}};
  const before=JSON.stringify(doc.mcp);
  try{
    await plugin.config(doc);assert.ok(!Object.keys(plugin.tool).some(name=>/^oa_(messages|asks)_/.test(name)));
    const out={system:[]};await plugin['experimental.chat.system.transform']({},out);assert.deepEqual(out.system,[]);
    assert.equal(JSON.stringify(doc.mcp),before);
  }finally{await plugin.dispose();}
});

test('an idle OpenCode chat reconnects its closed stdio to the newly enrolled connector',async()=>{
  const opened=[],started=[],watched=[];
  const oldProgram='C:/old/oa-opencode-integration.exe',newProgram='C:/new/oa-opencode-integration.exe';
  let newContext='different';
  let program=oldProgram,releaseWatch,reads=0;
  const panels=[];
  const native=async()=>({outcome:'connected',hostRegistered:true,helperEnrolled:true,helperProgram:program});
  const plugin=await Plugin({directory:'owned',client:{session:{get:async({path})=>({data:{id:path.id,directory:'owned'}}),status:async()=>({data:{}}),promptAsync:async()=>{throw Error('idle model work forbidden');}}}}, {},{
    tool,env:{},connectionRequired:true,requestOpenCodeConnection:native,checkOpenCodeConnection:options=>{reads++;panels.push(options.readOnly);return native();},
    messagesSchedule:(fn,ms)=>setTimeout(fn,Math.min(ms,5)),
    openMessagesHelper:({id,program:selected})=>{
      opened.push(selected);
      const child=new EventEmitter();
      child.stdout=new PassThrough();
      child.stdin=new Writable({
        write(bytes,_encoding,done){
          const {id:request,method,params}=JSON.parse(bytes.toString());
          const reply=result=>queueMicrotask(()=>child.stdout.write(JSON.stringify({jsonrpc:'2.0',id:request,result})+'\n'));
          if(method==='initialize')reply({protocolVersion:'2025-11-25',serverInfo:{name:'fixture'},capabilities:{}});
          if(method==='tools/call'){
            if(params.name==='oa_opencode_session_start'){
              started.push({id,selected});
              reply({structuredContent:{outcome:'observed',session_id:id,context:selected===oldProgram?'canonical':newContext}});
              if(selected===newProgram&&newContext==='different')newContext='canonical';
            }else{
              assert.equal(params.name,'oa_messages_watch');
              watched.push(selected);
              if(selected===oldProgram)releaseWatch=()=>child.emit('exit',1);
            }
          }
          done();
        },
        final(done){done();queueMicrotask(()=>child.emit('exit',0));},
      });
      return createStdioMCP({program:selected,spawn:()=>child});
    },
    resolveApplications:async()=>({announce:async()=>({outcome:'applied',instance:'metadata'}),withdraw:async()=>({outcome:'applied'})}),
    probeRuntime:async()=>true,readInventory:async()=>({offers:[]}),readCapability:async()=>false,readSelectionPolicy:async()=>({outcome:'unknown'}),
  });
  const eventually=async(predicate,description)=>{
    const deadline=Date.now()+1500;
    while(!predicate()&&Date.now()<deadline)await new Promise(resolve=>setTimeout(resolve,5));
    assert.ok(predicate(),description);
  };
  try{
    await plugin.config(config());
    await plugin.event({event:{type:'session.created',properties:{info:{id:'ses_original',directory:'owned'}}}});
    await eventually(()=>releaseWatch,'original chat holds a Watch');
    program=newProgram;
    releaseWatch();
    await eventually(()=>watched.includes(newProgram),'existing chat holds a Watch through the new connector');
    assert.deepEqual(opened,[oldProgram,newProgram,newProgram]);
    assert.deepEqual(started,opened.map(selected=>({id:'ses_original',selected})));
    assert.deepEqual(watched,[oldProgram,newProgram]);
    assert.equal(reads,2);
    assert.deepEqual(panels,[true,true]);
  }finally{await plugin.dispose();}
});
