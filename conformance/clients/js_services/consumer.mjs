import {RecoverableAcceptanceClient,OperationControlClient,JobInventoryClient,newSubmission} from '@openabstractions/job-acceptance';
import {ContentReaderClient} from '@openabstractions/storage-content';
import {newRequest,newSource,encode as encodeRequest} from '@openabstractions/download-request';
import assert from 'node:assert/strict';
import {Machine} from '@openabstractions/facade';
import {FrameTransport,NativeConnector,Status} from '@openabstractions/ipc';
import {SinkClient,HistoryReaderClient,newRecord} from '@openabstractions/logging';
const [mode,endpoint]=process.argv.slice(2);
const connector=new NativeConnector();
const machine=new Machine(endpoint,{connector,timeout:1000});
if(mode==='roundtrip') {
  assert.equal(connector.runtimeEndpoint(),endpoint);
  const sink=(await machine.resolveService('abstraction.logging/sink@1')).client(SinkClient);
  const record={...newRecord(),schema:1n,time:'2026-09-13T10:00:00.000000Z',level:2n,msg:'JavaScript exact ☃ payload',attrs:{fixture:'js-services'}};
  await sink.Write(record);
  const reader=(await machine.resolveService('abstraction.logging/reader@1')).client(HistoryReaderClient);
  const page=await reader.Read('',16n,65536n);
  assert.equal(page.outcome,'page');assert.equal(page.records.length,1);
  assert.equal(page.records[0].msg,record.msg);assert.equal(page.records[0].level,2n);
  assert.equal(page.records[0].attrs.fixture,'js-services');assert.ok(page.next);assert.equal(page.at_end,true);
  const invalid=await reader.Read('',0n,65536n);assert.equal(invalid.outcome,'invalid_request');assert.deepEqual(invalid.records,[]);
  await assert.rejects(machine.resolveService('abstraction.logging/sink@1',{guarantees:['unsupported@1']}));
  const reusable=new Machine(endpoint,{connector,timeout:1000});
  const binding=await reusable.resolveService('abstraction.logging/reader@1');
  await new Promise(resolve=>setTimeout(resolve,1100));
  assert.equal((await binding.client(HistoryReaderClient).Read('',1n,65536n)).outcome,'page');
  assert.equal(binding.endpoint,(await machine.resolveService('abstraction.logging/reader@1')).endpoint);
  const stopped=new AbortController();stopped.abort();
  await assert.rejects(binding.withWaiting({cancellation:stopped.signal}).client(HistoryReaderClient).Read('',1n,65536n),e=>e.status===Status.cancelled);
  assert.equal((await binding.client(HistoryReaderClient).Read('',1n,65536n)).outcome,'page');
  console.log('PASS exact logging/history/refusal/default reuse');
 } else if(mode==='job-storage') {
 const [url,digest]=process.argv.slice(4),max=65536n;
 const bind=await machine.resolveService('abstraction.job/acceptance@1',{maxFrame:2097152});
 const jobs=bind.client(RecoverableAcceptanceClient),history=await jobs.GetHistoryWindow();
 const submission=newSubmission();submission.identity={key:'js-caller-owned-key',history_epoch:history.history_epoch};submission.kind='download';
 const request=newRequest();request.artifact={digest,size:150000n};request.sources=[{...newSource(),scheme:'http',locator:url}];submission.spec=encodeRequest(request);
 submission.required_guarantees=['abstraction.job/reconciliation@1'];
 const accepted=await jobs.Submit(submission);assert.equal(accepted.outcome,'accepted');assert.ok(accepted.receipt);
 const receipt=accepted.receipt;assert.deepEqual(receipt.identity,submission.identity);assert.equal(receipt.logical_owner,history.logical_owner);assert.ok(receipt.accepted_guarantees.includes(submission.required_guarantees[0]));
 // Reconstruct at the selected endpoint and recover the same receipt, with no second Submit.
 const recovered=await bind.withWaiting().client(RecoverableAcceptanceClient).Reconcile(submission.identity);
 assert.equal(recovered.outcome,'accepted');assert.deepEqual(recovered.receipt,receipt);
 const opsBinding=await machine.resolveService('abstraction.job/operations@1',{maxFrame:2097152});assert.equal(opsBinding.endpoint,bind.endpoint);
 const ops=opsBinding.client(OperationControlClient);let observed;
 const until=performance.now()+10000;
 do {observed=await ops.ObserveWork(submission.identity);assert.equal(observed.outcome,'observed');assert.deepEqual(observed.snapshot.receipt,receipt);if(observed.snapshot.state==='complete')break;assert.ok(performance.now()<until,observed.snapshot.state);assert.ok(!['failed','cancelled'].includes(observed.snapshot.state));await new Promise(r=>setTimeout(r,20));}while(true);
 const chunks=[];let offset=0n;
 while(true){const result=await ops.ReadResult(submission.identity,offset,max);assert.equal(result.outcome,'data');const c=result.chunk;assert.deepEqual(c.receipt,receipt);assert.equal(c.offset,offset);assert.equal(c.total,150000n);assert.ok(c.data instanceof Uint8Array);assert.ok(c.data.length<=Number(max));chunks.push(c.data);offset+=BigInt(c.data.length);assert.equal(c.eof,offset===c.total);if(c.eof)break;assert.ok(c.data.length>0);}
 assert.equal(chunks.length,3);assert.equal(Buffer.concat(chunks).toString(),'x'.repeat(150000));
 assert.equal((await ops.ReadResult(submission.identity,150001n,max)).outcome,'invalid');
 const inventory=(await machine.resolveService('abstraction.job/inventory@1',{maxFrame:2097152})).client(JobInventoryClient);const page=await inventory.ListWork('',8n);assert.equal(page.outcome,'page');assert.equal(page.snapshots.length,1);assert.deepEqual(page.snapshots[0].receipt,receipt);
 const content=(await machine.resolveService('abstraction.storage/content-reader@1')).client(ContentReaderClient);
 const opened=await content.Open(digest);assert.equal(opened.outcome,'opened');assert.equal(opened.resource.digest,digest);assert.equal(opened.resource.size,150000n);
 const parts=[];offset=0n;while(true){const r=await content.Read(opened.resource.handle,offset,max);assert.equal(r.outcome,'data');assert.equal(r.chunk.offset,offset);assert.equal(r.chunk.total,150000n);parts.push(r.chunk.data);offset+=BigInt(r.chunk.data.length);assert.equal(r.chunk.eof,offset===150000n);if(r.chunk.eof)break;assert.ok(r.chunk.data.length>0&&r.chunk.data.length<=65536);}
 assert.equal(parts.length,3);assert.equal(Buffer.concat(parts).toString(),'x'.repeat(150000));
 assert.equal((await content.Read(opened.resource.handle,-1n,max)).outcome,'invalid');
 assert.equal((await content.Close(opened.resource.handle)).outcome,'closed');assert.equal((await content.Read(opened.resource.handle,0n,max)).outcome,'gap');
 assert.equal((await content.Open('sha256:'+'0'.repeat(64))).outcome,'forbidden');
 console.log('PASS generated job acceptance/reconciliation/inventory/result and authorized storage, three binary chunks each');
 } else if(mode==='verified'||mode==='untrusted') {
 const [principal,program]=process.argv.slice(4);
 const server={principalKind:process.platform==='win32'?1:2,principal,program:mode==='untrusted'?(process.platform==='win32'?'C:\\untrusted\\other.exe':'/untrusted/other'):program};
 const verified=new Machine(endpoint,{connector,timeout:1000,server});
 server.program='mutated caller object';
 if(mode==='untrusted')await assert.rejects(verified.resolveService('abstraction.logging/reader@1'),e=>e.status===Status.untrusted);
 else {
  const binding=await verified.callScope().resolveService('abstraction.logging/reader@1');
  assert.equal(binding.waiting.server.program,program);
  for(const view of [binding,binding.withWaiting(),binding.callScope()])assert.equal((await view.client(HistoryReaderClient).Read('',1n,65536n)).outcome,'page');
 }
 console.log('PASS native verified '+mode);
} else if(mode==='forged') {
  await assert.rejects(machine.resolveService('abstraction.logging/sink@1'),e=>e.status==='invalid_resolution');
} else if(mode==='malformed') {
  const reader=new HistoryReaderClient(new FrameTransport(endpoint,{timeout:1000}));
  await assert.rejects(reader.Read('',1n,65536n));
} else if(mode==='queue') {
  const blocker=new AbortController();
  const first=new FrameTransport(endpoint,{timeout:1500,cancellation:blocker.signal}).exchangeFrame(new Uint8Array([1]));
  // The fixture selects one libuv worker. Later calls must expire/cancel while queued.
  const started=performance.now();
  const cancelled=new AbortController();
  const pending=new FrameTransport(endpoint,{timeout:1000,cancellation:cancelled.signal}).exchangeFrame(new Uint8Array([2]));
  setTimeout(()=>cancelled.abort(),40);
  await assert.rejects(pending,e=>e.status===Status.cancelled);
  await assert.rejects(new FrameTransport(endpoint,{timeout:40}).exchangeFrame(new Uint8Array([3])),e=>e.status===Status.timeout);
  assert.ok(performance.now()-started<700);
  blocker.abort();await assert.rejects(first,e=>e.status===Status.cancelled);
} else if(mode==='cancel'||mode==='timeout') {
  const controller=new AbortController();
  const transport=new FrameTransport(endpoint,{timeout:mode==='timeout'?80:1000,cancellation:controller.signal});
  const started=performance.now();
  const waiting=transport.exchangeFrame(new Uint8Array([1]));
  const timer=mode==='cancel'?setTimeout(()=>controller.abort(),40):null;
  try {await assert.rejects(waiting,e=>e.status===(mode==='cancel'?Status.cancelled:Status.timeout));}
  finally {clearTimeout(timer);}
  assert.ok(performance.now()-started<1500);
} else {
  const transport=new FrameTransport(endpoint,{timeout:1000,maxFrame:1024});
  await assert.rejects(transport.exchangeFrame(new Uint8Array([1])),e=>mode==='oversized'?e.status===Status.invalidArgument:mode==='truncated'?e.status===Status.disconnected:typeof e.status==='number');
}
