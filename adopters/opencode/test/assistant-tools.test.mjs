import test from 'node:test';
import assert from 'node:assert/strict';
import {tool} from '@opencode-ai/plugin/tool';
import {assistantToolArguments} from '../assistant-tools.js';
import {jsonTool, inputSchema} from '../v2.js';

test('generic host schema resolves nested local references and preserves constraints', () => {
  const schema = {type: 'object', properties: {reference: {$ref: '#/$defs/Reference'}, optional: {type: 'integer', minimum: 1, maximum: 10}}, required: ['reference'],
    $defs: {Reference: {type: 'object', additionalProperties: false, properties: {nested: {$ref: '#/$defs/Name'}}, required: ['nested']}, Name: {type: 'string', minLength: 2, maxLength: 8, pattern: '^ok'}}};
  const host = tool.schema.object(assistantToolArguments(schema, tool.schema));
  assert.deepEqual(host.parse({reference: {nested: 'okay'}}), {reference: {nested: 'okay'}});
  for (const value of [{}, {reference: {nested: 'x'}}, {reference: {nested: 'wrong'}}, {reference: {nested: 'okay', extra: true}}, {reference: {nested: 'okay'}, optional: 11}]) assert.equal(host.safeParse(value).success, false);
});

test('external, missing and cyclic schema references are refused', () => {
  for (const schema of [
    {type: 'object', properties: {x: {$ref: 'https://example.invalid/schema'}}},
    {type: 'object', properties: {x: {$ref: '#/$defs/Missing'}}},
    {type: 'object', properties: {x: {$ref: '#/$defs/Cycle'}}, $defs: {Cycle: {$ref: '#/$defs/Cycle'}}},
  ]) assert.throws(() => assistantToolArguments(schema, tool.schema));
});

test('allowed nested maps keep arbitrary JSON while typed maps and closed records retain constraints', () => {
  const schema={type:'object',additionalProperties:false,properties:{
    arguments:{type:'object',additionalProperties:true},
    typed:{type:'object',additionalProperties:{$ref:'#/$defs/Value'}},
    untyped:{},
  },required:['arguments','typed'], $defs:{Value:{type:['array','null'],items:{}}}};
  const validate=tool.schema.object(assistantToolArguments(schema,tool.schema)).strict();
  const input={arguments:{command:{rows:[null,true,7,{nested:'kept'}]},free:[1,2]},typed:{first:[null,{deep:true}],second:null},untyped:{anything:[false]}};
  assert.deepEqual(validate.parse(input),input);
  assert.equal(validate.safeParse({...input,own_context:'foreign'}).success,false);
  assert.equal(validate.safeParse({...input,typed:{wrong:'text'}}).success,false);
  const projected=inputSchema(jsonTool({args:assistantToolArguments(schema,jsonTool.schema)}));
  assert.equal(projected.properties.arguments.additionalProperties,true);
  assert.deepEqual(projected.properties.untyped,{});
  assert.deepEqual(projected.properties.typed.additionalProperties,{anyOf:[{type:'array',items:{}},{type:'null'}]});
});

test('native numeric and nullable enum values retain exact allowed values', () => {
  const schema={type:'object',properties:{wait_ms:{type:'integer',enum:[0]},choice:{enum:[null,7,'ready']}},required:['wait_ms','choice']};
  const validate=tool.schema.object(assistantToolArguments(schema,tool.schema)).strict();
  for(const choice of [null,7,'ready'])assert.deepEqual(validate.parse({wait_ms:0,choice}),{wait_ms:0,choice});
  for(const input of [{wait_ms:1,choice:null},{wait_ms:'0',choice:7},{wait_ms:0,choice:false}])assert.equal(validate.safeParse(input).success,false);
});

test('the V2 host preserves canonical argument bounds, unions and closed records', () => {
  const schema = {type: 'object', properties: {
    name: {type: 'string', minLength: 1, maxLength: 64, pattern: '^oa-'},
    count: {type: 'integer', minimum: 1, maximum: 256},
    optional: {type: ['string', 'null']},
    rows: {type: 'array', minItems: 1, maxItems: 4, items: {type: 'object', additionalProperties: false,
      properties: {ready: {type: 'boolean'}}, required: ['ready']}},
  }, required: ['name', 'count', 'rows']};
  const projected = inputSchema(jsonTool({args: assistantToolArguments(schema, jsonTool.schema)}));
  assert.deepEqual(projected, {...schema, additionalProperties: false,
    properties: {...schema.properties, optional: {anyOf: [{type: 'string'}, {type: 'null'}]}}});
});
