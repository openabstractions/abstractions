import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {createRequire,stripTypeScriptTypes} from 'node:module';
import {tool} from '@opencode-ai/plugin/tool';
import Plugin from '../plugin.js';

const require=createRequire(import.meta.url),generated=require('../generated/agent-tools.json');
// The plugin's own oa_image_generate delegates to the helper's and keeps its
// host schema (save_to, edits); every other helper tool is projected unchanged.
const agent={...generated,tools:generated.tools.filter(value=>value.name!=='oa_image_generate')};
const root=new URL('./fixtures/opencode-aec0b9/',import.meta.url);
const pins=JSON.parse(readFileSync(new URL('source.json',root),'utf8'));
function pinned(name){
  const bytes=readFileSync(new URL(name,root));
  assert.equal(createHash('sha256').update(bytes).digest('hex'),pins.files[name].sha256);
  return bytes.toString('utf8');
}
function span(source,start,end){
  const begin=source.indexOf(start),finish=source.indexOf(end,begin);
  assert.ok(begin>=0&&finish>begin,'pinned V1 consumer span exists');
  return source.slice(begin,finish);
}

// Execute the pinned 1.18.34 V1 registry's unchanged hook consumer, its exact
// JSON-schema selection function, and the model-tool construction header.
async function modelTool(plugin,original){
  const body=span(pinned('tool-registry.ts'),'          const output = {','\n        }),');
  const registry=new Function('tool','plugin','TaskTool','describeTask','input','codeModeDescription',
    `return (function*(){${body}})();`)(original,{trigger:function*(_name,input,output){yield plugin['tool.definition'](input,output);}},
    {id:'task'},()=>{throw Error('unexpected task description');},{},undefined);
  let current=registry.next();
  while(!current.done){await current.value;current=registry.next();}
  const item=current.value;
  const selection=span(pinned('tool-json-schema.ts'),'export function fromTool','\nfunction normalize');
  const javascript=stripTypeScriptTypes(selection.replace('export function','function'),{mode:'transform'});
  const fromTool=new Function('fromSchema',javascript+'\nreturn fromTool;')(()=>{throw Error('canonical hook schema was ignored');});
  const header=span(pinned('session-tools.ts'),'    const schema = ProviderTransform.schema(input.model, ToolJsonSchema.fromTool(item))','\n      execute(args, options) {');
  return new Function('item','ProviderTransform','input','ToolJsonSchema','tools','tool','jsonSchema',
    header+'\n    })\nreturn tools[item.id];')(item,{schema:(_model,schema)=>schema},{model:{}},{fromTool},{},value=>value,value=>value);
}

test('the pinned V1 model consumer receives every canonical public schema, including in compact mode',async()=>{
  const captured=[];
  for(const compactTools of [false,true]){
    const plugin=await Plugin({client:{}},{compactTools,tools:Object.fromEntries(agent.tools.map(value=>[value.name,true]))},
      {tool,env:{},connectionRequired:false});
    try{
      const tools=[];
      for(const metadata of agent.tools){
        const definition=plugin.tool[metadata.name];assert.ok(definition,metadata.name);
        const parameters=tool.schema.object(definition.args);
        const original={id:metadata.name,description:definition.description,parameters,jsonSchema:tool.schema.toJSONSchema(parameters)};
        const emitted=await modelTool(plugin,original);
        const expected=metadata.name==='oa_messages_inbox_open'?{type:'object',properties:{},additionalProperties:false}:structuredClone(metadata.inputSchema);
        // AB-1s F2: the model's watch carries none of the plugin's adapter fields.
        if(metadata.name==='oa_messages_watch')for(const field of ['adapter_key','reserve_wake','wake_ticket','turn'])delete expected.properties[field];
        assert.deepEqual(emitted.inputSchema,expected,metadata.name);
        tools.push({name:metadata.name,inputSchema:emitted.inputSchema});
      }
      captured.push({compactTools,tools});
      const first={description:'native',parameters:{}};
      await plugin['tool.definition']({toolID:'oa_inference_complete'},first);
      first.jsonSchema.oneOf.length=0;
      const next={description:'native',parameters:{}};
      await plugin['tool.definition']({toolID:'oa_inference_complete'},next);
      assert.deepEqual(next.jsonSchema,agent.tools.find(value=>value.name==='oa_inference_complete').inputSchema);
    }finally{await plugin.dispose();}
  }
  // The focused native schema owner validates start/resume/reasoning cases
  // against these actual post-hook, post-consumer schemas with its existing
  // JSON Schema validator. No second execution validator enters the plugin.
  if(process.env.OA_OPENCODE_MODEL_SCHEMAS_OUT)writeFileSync(process.env.OA_OPENCODE_MODEL_SCHEMAS_OUT,JSON.stringify(captured));
});
