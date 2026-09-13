import * as jobs from '@openabstractions/job-acceptance';
import * as storage from '@openabstractions/storage-content';
import * as config from '@openabstractions/config';
import * as rights from '@openabstractions/rights';
import * as asks from '@openabstractions/asks';
import assert from 'node:assert/strict';
import {Binding,validateReference} from '@openabstractions/facade';
let now=10;Object.defineProperty(globalThis,'performance',{value:{now:()=>now},configurable:true});
const seen=[];const signal={};
const connector={connect(endpoint,options){seen.push({endpoint,...options});return {
  async exchangeFrame(frame){if(options.deadline<=now)throw new Error('deadline');return frame;},async writeFrame(){}
};}};
const ref=Object.freeze({provider:'implementation',endpoint:'fixed'});
const binding=new Binding(connector,'fixed',{timeout:5,cancellation:signal},ref);
await binding.exchangeFrame(new Uint8Array([1]));assert.equal(seen.at(-1).deadline,15);
now=13;await binding.exchangeFrame(new Uint8Array([1]));assert.equal(seen.at(-1).deadline,18);
const scope=binding.callScope();now=16;await scope.exchangeFrame(new Uint8Array([1]));now=19;
await assert.rejects(scope.exchangeFrame(new Uint8Array([2])),/deadline/);
assert.equal(seen.at(-1).deadline,18);assert.equal(seen.at(-1).cancellation,signal);assert.equal(scope.reference,ref);
await binding.exchangeFrame(new Uint8Array([3]));assert.equal(seen.at(-1).deadline,24);
const fixed=new Binding(connector,'fixed',{deadline:20,cancellation:signal});now=21;await assert.rejects(fixed.exchangeFrame(new Uint8Array()),/deadline/);
const request={capability:'cap',contracts:['cap/v1'],scope:'local',guarantees:['g']};
const valid={status:'resolved',reference:{provider:'p',capability:'cap',contract:'cap/v1',scope:'local',transport:'custom',endpoint:'fixed',guarantees:['g']}};
assert.equal(validateReference(request,valid).endpoint,'fixed');
for(const change of [{provider:''},{capability:'wrong'},{contract:'wrong'},{scope:'remote'},{guarantees:[]},{endpoint:'bad\0path'}])
  assert.throws(()=>validateReference(request,{...valid,reference:{...valid.reference,...change}}));
assert.throws(()=>validateReference(request,{status:'unavailable',reference:valid.reference}));
console.log('PASS pure binding, cumulative scope, fixed deadline, reference refusal; no native import');

for(const [api,name] of [[jobs,'RecoverableAcceptanceClient'],[storage,'ContentReaderClient'],[config,'ConfigReaderClient'],[rights,'AuthorizationClient'],[asks,'QuestionApplicationClient']]) assert.equal(typeof api[name],'function',name);
