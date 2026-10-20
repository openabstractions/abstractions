// Generated codec receiving, without a browser or native/runtime effects.
const assert=require('assert');
const {bridge,nativeJSON,fixture,prepare}=require('./panel_record_test_helper.js');
(async()=>{
 const logging=nativeJSON('logging','HistoryUsageResult',{outcome:'available',usage:{retained_bytes:0}});
 const usage=await bridge.decode('logging','HistoryUsageResult',logging);
 assert.strictEqual(usage.usage.retained_bytes,0);
 assert.strictEqual(usage.usage.max_use,undefined);
 const context=prepare({});
 assert.deepStrictEqual(await context.oaPanelResponse('/card/storage/logs',logging),usage);
 const record='{"resource":"card:0","capacity":9007199254740993,"held":0,"holders":[],"observed":"2026-11-04T00:00:00Z","instrument":"none"}';
 const view=await bridge.decode('resource','ResourceState',record);
 assert.strictEqual(bridge.details(view),record,'technical details keep original integers');
 await assert.rejects(()=>bridge.decode('resource','ResourceState',record.replace('"held":0','"held":0,"held":1')));
 const card=fixture('/card/table',{resources:[{resource:'card:0',capacity:10,held:0,holders:[],observed:'2026-11-04T00:00:00Z',instrument:'none'}]});
 assert.strictEqual((await context.oaPanelResponse('/card/table',card)).resources[0].resource,'card:0');
 const candidate=await bridge.encode('inference','SelectionCandidateV2',{model:'native/model',placement:'local',provider:'native'});
 assert.strictEqual(candidate.provider,'native');
 await assert.rejects(()=>bridge.encode('inference','SelectionCandidateV2',{model:'a',placement:'person'}));
 console.log('Panel generated record decoding passed');
})().catch(error=>{console.error(error);process.exitCode=1});
