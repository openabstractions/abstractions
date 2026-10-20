import test from 'node:test';
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {tool} from '@opencode-ai/plugin/tool';
import Plugin from '../plugin.js';
import {toolNames,toolNameFor,optionalTools,defaultHelperTools,toolChoices} from '../tool-config.js';
import {agentToolInputSchema,messageInstructions} from '../messages.js';
import {modelsConnectMetadata} from '../models-connect.js';

test('OpenCode exposes each generated OA wrapper and selected host tool once under its canonical name',async()=>{
  const require=createRequire(import.meta.url),agent=require('../generated/agent-tools.json');
  const generated=agent.tools.map(value=>value.name);
  assert.equal(new Set(generated).size,generated.length,'catalogue collisions cannot be hidden by object assignment');
  const plugin=await Plugin({client:{}},{compactTools:true,tools:Object.fromEntries(toolNames.map(name=>[name,true]))},{tool,env:{},connectionRequired:false});
  try{
    const actual=Object.keys(plugin.tool);
    assert.equal(new Set(actual).size,actual.length);
    assert.ok(actual.every(name=>/^oa_[a-z0-9_]+$/.test(name)),actual.join(','));
    for(const name of generated)assert.equal(actual.filter(value=>value===name).length,1,name);
    for(const name of toolNames.map(toolNameFor))assert.equal(actual.filter(value=>value===name).length,1,name);
  }finally{await plugin.dispose();}
});

test('agents reach application workflows through the generic OA application tools',async()=>{
  const plugin=await Plugin({client:{}},{compactTools:true,tools:Object.fromEntries(toolNames.map(name=>[name,true]))},{tool,env:{},connectionRequired:false});
  try{
    const actual=Object.keys(plugin.tool);
    for(const name of ['oa_applications_describe','oa_applications_open','oa_applications_commands_list','oa_applications_command_call'])
      assert.ok(actual.includes(name),name);
    assert.deepEqual(actual.filter(name=>/comfy/i.test(name)),[],'no application-specific tool');
    assert.match(plugin.tool.oa_image_generate.description,/oa_applications_command_call/);
  }finally{await plugin.dispose();}
});

// Owner bug R27: a local 32k chat overflowed because OpenCode's first request
// carried about 7,600 Qwen tokens of OA tool text and instructions. OpenCode
// sends every registered definition with every request, so the default set
// and its text are bounded here. Measured 2026-11-11: 10,490 definition bytes
// (2,969 Qwen3.6 tokens) and 1,717 instruction bytes (352 tokens).
const DEFAULT_DEFINITION_BYTES=11000,INSTRUCTION_BYTES=1850;

test('the default OA tool definitions and instructions stay within their prompt budget',()=>{
  const require=createRequire(import.meta.url),agent=require('../generated/agent-tools.json');
  const lines=defaultHelperTools.map(name=>{
    const metadata=name==='oa_models_connect'?modelsConnectMetadata:agent.tools.find(value=>value.name===name);
    assert.ok(metadata,name);
    return JSON.stringify({type:'function',function:{name,description:metadata.description,parameters:agentToolInputSchema(name)}});
  });
  const bytes=lines.reduce((sum,line)=>sum+Buffer.byteLength(line),0);
  assert.ok(bytes<=DEFAULT_DEFINITION_BYTES,`default OA tool definitions take ${bytes} bytes`);
  assert.ok(Buffer.byteLength(messageInstructions)<=INSTRUCTION_BYTES,`OA instructions take ${Buffer.byteLength(messageInstructions)} bytes`);
  for(const name of defaultHelperTools){
    const description=name==='oa_models_connect'?modelsConnectMetadata.description:agent.tools.find(value=>value.name===name).description;
    assert.ok(description.split('. ').length<=3,`${name} description has at most three sentences: ${description}`);
  }
});

test('optional OA tools register only when the tools option enables them',()=>{
  for(const name of ['oa_messages_send','oa_messages_reply','oa_messages_wait','oa_messages_inbox_open','oa_messages_inboxes_list',
    'oa_messages_prepare','oa_messages_request_access','oa_asks_list_answerable','oa_asks_answer','oa_asks_request_answering',
    'oa_models_list','oa_models_connect','oa_inference_complete'])assert.ok(defaultHelperTools.includes(name),name);
  assert.ok(optionalTools.length>0&&optionalTools.every(name=>toolNames.includes(name)&&!defaultHelperTools.includes(name)));
  assert.deepEqual([...toolChoices(undefined).disabled].sort(),[...optionalTools].sort());
  const chosen=toolChoices({oa_jobs_follow:true,oa_messages_send:false});
  assert.ok(chosen.enabled.has('oa_jobs_follow')&&!chosen.disabled.has('oa_jobs_follow'));
  assert.ok(chosen.disabled.has('oa_messages_send')&&chosen.disabled.has('oa_api_read'));
  assert.deepEqual(chosen.unknown,[]);
});
