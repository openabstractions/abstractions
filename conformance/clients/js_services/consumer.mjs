import {RecoverableAcceptanceClient,OperationControlClient,JobInventoryClient,newSubmission,newRequestIdentity,FailureClass,FailureCause} from '@openabstractions/job-acceptance';
import {ContentReaderClient} from '@openabstractions/storage-content';
import {newRequest,newSource,newConstraints,Network,networkCostGuarantees,credentialGuarantees,encode as encodeRequest} from '@openabstractions/download-request';
import assert from 'node:assert/strict';
import {Machine,ResolutionError} from '@openabstractions/facade';
import {FrameTransport,NativeConnector,Status} from '@openabstractions/ipc';
import {SinkClient,HistoryReaderClient,newRecord} from '@openabstractions/logging';
// --runtime-endpoint OVERRIDE is the endpoint override in place of ABSTRACTION_RUNTIME_ENDPOINT.
// The native bootstrap reads the variable through the C runtime, which does not see process.env
// assignments, so the option moves the endpoint in the connector instead. Selection and identity
// verification stay the native connector's: the option supplies no trust.
class EndpointOverride extends NativeConnector {
  #endpoint;
  constructor(endpoint) { super(); this.#endpoint=endpoint; }
  runtimeEndpoint() { return this.#endpoint; }
}
const argv=process.argv.slice(2);
const at=argv.indexOf('--runtime-endpoint');
if(at>=0&&at+1>=argv.length)throw new TypeError('--runtime-endpoint needs a value');
const [override]=at>=0?argv.splice(at,2).slice(1):[undefined];
const [mode,endpoint]=argv;
const connector=override===undefined?new NativeConnector():new EndpointOverride(override);
const machine=new Machine(endpoint,{connector,timeout:1000});
if(mode==='roundtrip') {
  assert.equal(connector.runtimeEndpoint(),endpoint);
  const sink=(await machine.resolveService('abstraction.logging/sink@1')).client(SinkClient);
  const record={...newRecord(),schema:1n,time:'2026-09-13T10:00:00.000000Z',level:2n,msg:'JavaScript exact ☃ payload',attrs:{fixture:'js-services'}};
  await sink.write(record);
  const reader=(await machine.resolveService('abstraction.logging/reader@1')).client(HistoryReaderClient);
  const page=await reader.read('',16n,65536n);
  assert.equal(page.outcome,'page');assert.equal(page.records.length,1);
  assert.equal(page.records[0].msg,record.msg);assert.equal(page.records[0].level,2n);
  assert.equal(page.records[0].attrs.fixture,'js-services');assert.ok(page.next);assert.equal(page.atEnd,true);
  const invalid=await reader.read('',0n,65536n);assert.equal(invalid.outcome,'invalid_request');assert.deepEqual(invalid.records,[]);
  await assert.rejects(machine.resolveService('abstraction.logging/sink@1',{guarantees:['unsupported@1']}));
  const reusable=new Machine(endpoint,{connector,timeout:1000});
  const binding=await reusable.resolveService('abstraction.logging/reader@1');
  await new Promise(resolve=>setTimeout(resolve,1100));
  assert.equal((await binding.client(HistoryReaderClient).read('',1n,65536n)).outcome,'page');
  assert.equal(binding.endpoint,(await machine.resolveService('abstraction.logging/reader@1')).endpoint);
  const stopped=new AbortController();stopped.abort();
  await assert.rejects(binding.withWaiting({cancellation:stopped.signal}).client(HistoryReaderClient).read('',1n,65536n),e=>e.status===Status.Cancelled);
  assert.equal((await binding.client(HistoryReaderClient).read('',1n,65536n)).outcome,'page');
  console.log('PASS exact logging/history/refusal/default reuse');
 } else if(mode==='job-storage') {
 const [url,digest]=argv.slice(2),max=65536n;
 const bind=await machine.resolveService('abstraction.job/acceptance@1',{maxFrame:2097152});
 const jobs=bind.client(RecoverableAcceptanceClient),history=await jobs.getHistoryWindow();
 const submission=newSubmission();submission.identity={...newRequestIdentity(),key:'js-caller-owned-key',historyEpoch:history.historyEpoch};submission.kind='download';
 const request=newRequest();request.artifact={digest,size:150000n};request.sources=[{...newSource(),scheme:'http',locator:url}];submission.spec=encodeRequest(request);
 submission.requiredGuarantees=['abstraction.job/reconciliation@1'];
 const accepted=await jobs.submit(submission);assert.equal(accepted.outcome,'accepted');assert.ok(accepted.receipt);
 const receipt=accepted.receipt;assert.deepEqual(receipt.identity,submission.identity);assert.equal(receipt.logicalOwner,history.logicalOwner);assert.ok(receipt.acceptedGuarantees.includes(submission.requiredGuarantees[0]));
 // Reconstruct at the selected endpoint and recover the same receipt, with no second Submit.
 const recovered=await bind.withWaiting().client(RecoverableAcceptanceClient).reconcile(submission.identity);
 assert.equal(recovered.outcome,'accepted');assert.deepEqual(recovered.receipt,receipt);
 const opsBinding=await machine.resolveService('abstraction.job/operations@1',{maxFrame:2097152});assert.equal(opsBinding.endpoint,bind.endpoint);
 const ops=opsBinding.client(OperationControlClient);let observed;
 const until=performance.now()+10000;
 do {observed=await ops.observeWork(submission.identity);assert.equal(observed.outcome,'observed');assert.deepEqual(observed.snapshot.receipt,receipt);if(observed.snapshot.state==='complete')break;assert.ok(performance.now()<until,observed.snapshot.state);assert.ok(!['failed','cancelled'].includes(observed.snapshot.state));await new Promise(r=>setTimeout(r,20));}while(true);
 const chunks=[];let offset=0n;
 while(true){const result=await ops.readResult(submission.identity,offset,max);assert.equal(result.outcome,'data');const c=result.chunk;assert.deepEqual(c.receipt,receipt);assert.equal(c.offset,offset);assert.equal(c.total,150000n);assert.ok(c.data instanceof Uint8Array);assert.ok(c.data.length<=Number(max));chunks.push(c.data);offset+=BigInt(c.data.length);assert.equal(c.eof,offset===c.total);if(c.eof)break;assert.ok(c.data.length>0);}
 assert.equal(chunks.length,3);assert.equal(Buffer.concat(chunks).toString(),'x'.repeat(150000));
 assert.equal((await ops.readResult(submission.identity,150001n,max)).outcome,'invalid');
 const inventory=(await machine.resolveService('abstraction.job/inventory@1',{maxFrame:2097152})).client(JobInventoryClient);const page=await inventory.listWork('',8n);assert.equal(page.outcome,'page');assert.equal(page.snapshots.length,1);assert.deepEqual(page.snapshots[0].receipt,receipt);
 // No caller label was sent: the runtime derived one from the loopback source URL (JOB-A12).
 assert.equal(page.snapshots[0].label,'127.0.0.1');assert.equal(page.snapshots[0].labelDerived,true);
 const content=(await machine.resolveService('abstraction.storage/content-reader@1')).client(ContentReaderClient);
 const opened=await content.open(digest);assert.equal(opened.outcome,'opened');assert.equal(opened.resource.digest,digest);assert.equal(opened.resource.size,150000n);
 const parts=[];offset=0n;while(true){const r=await content.read(opened.resource.handle,offset,max);assert.equal(r.outcome,'data');assert.equal(r.chunk.offset,offset);assert.equal(r.chunk.total,150000n);parts.push(r.chunk.data);offset+=BigInt(r.chunk.data.length);assert.equal(r.chunk.eof,offset===150000n);if(r.chunk.eof)break;assert.ok(r.chunk.data.length>0&&r.chunk.data.length<=65536);}
 assert.equal(parts.length,3);assert.equal(Buffer.concat(parts).toString(),'x'.repeat(150000));
 assert.equal((await content.read(opened.resource.handle,-1n,max)).outcome,'invalid');
 assert.equal((await content.close(opened.resource.handle)).outcome,'closed');assert.equal((await content.read(opened.resource.handle,0n,max)).outcome,'gap');
 assert.equal((await content.open('sha256:'+'0'.repeat(64))).outcome,'forbidden');
 // The runtime's cost source reports a metered path. A request with network unmetered requires
 // network-cost@1, waits with the word network:metered and is cancelled with nothing fetched
 // (download DL-N2 to DL-N6, JOB-A15). The word is written before the lease is released.
 const constrained=newRequest();constrained.sources=[{...newSource(),scheme:'http',locator:url+'/constrained'}];constrained.constraints={...newConstraints(),network:Network.Unmetered};
 const waiting=newSubmission();waiting.identity={...newRequestIdentity(),key:'js-waiting-key',historyEpoch:history.historyEpoch};waiting.kind='download';waiting.spec=encodeRequest(constrained);waiting.requiredGuarantees=[...networkCostGuarantees];
 const held=await jobs.submit(waiting);assert.equal(held.outcome,'accepted');assert.ok(held.receipt.acceptedGuarantees.includes(networkCostGuarantees[0]));
 const waitUntil=performance.now()+10000;
 do {const s=(await ops.observeWork(waiting.identity)).snapshot;if(s.waiting==='network:metered'&&s.state==='pending')break;assert.ok(['pending','running'].includes(s.state),s.state);assert.ok(performance.now()<waitUntil,s.state);await new Promise(r=>setTimeout(r,20));}while(true);
 assert.equal((await jobs.cancelWork(waiting.identity)).outcome,'requested');
 do {const s=(await ops.observeWork(waiting.identity)).snapshot;if(s.state==='cancelled'){assert.equal(s.waiting,'');break;}assert.ok(performance.now()<waitUntil,s.state);await new Promise(r=>setTimeout(r,20));}while(true);
 // The runtime admits the credential hf and refuses applying it as revoked: the operation fails
 // permanently with cause credential and the applier outcome. A name it does not hold is refused
 // at admission with no receipt (job JOB-A8, JOB-A16; download DL-K1).
 const named=newRequest();named.sources=[{...newSource(),scheme:'http',locator:url+'/credential',credential:'hf'}];
 const credentialed=newSubmission();credentialed.identity={...newRequestIdentity(),key:'js-credential-key',historyEpoch:history.historyEpoch};credentialed.kind='download';credentialed.spec=encodeRequest(named);credentialed.requiredGuarantees=[...credentialGuarantees];
 assert.equal((await jobs.submit(credentialed)).outcome,'accepted');
 let ended;do {ended=(await ops.observeWork(credentialed.identity)).snapshot;if(ended.state==='failed')break;assert.ok(['pending','running'].includes(ended.state),ended.state);assert.ok(performance.now()<waitUntil+10000,ended.state);await new Promise(r=>setTimeout(r,20));}while(true);
 assert.equal(ended.failure.classification,FailureClass.Permanent);assert.equal(ended.failure.cause,FailureCause.Credential);assert.equal(ended.failure.message,'download attempt failed: credential:revoked:hf');
 const missing=newRequest();missing.sources=[{...newSource(),scheme:'http',locator:url+'/missing',credential:'missing'}];
 const unheld=newSubmission();unheld.identity={...newRequestIdentity(),key:'js-missing-key',historyEpoch:history.historyEpoch};unheld.kind='download';unheld.spec=encodeRequest(missing);unheld.requiredGuarantees=[...credentialGuarantees];
 const refused=await jobs.submit(unheld);assert.equal(refused.outcome,'invalid');assert.equal(refused.reason,'credential:unknown:missing');assert.equal(refused.receipt??null,null);
 console.log('PASS generated job acceptance/reconciliation/inventory with derived label/result and authorized storage, three binary chunks each; network:metered wait cancelled; credential cause and admission refusal');
 } else if(mode==='installed') {
 // The shared selector answers with this host's installed identity or a trust refusal.
 const answer=await connector.selectRuntime({timeout:5000}).then(server=>server,error=>error);
 if(answer instanceof Error)assert.ok([Status.Untrusted,Status.ProofUnavailable].includes(answer.status),`selection: ${answer.status} ${answer.message}`);
 else {assert.equal(answer.principalKind,process.platform==='win32'?1:2);assert.ok(Object.isFrozen(answer)&&answer.program.length>0);}
 const stopped=new AbortController();stopped.abort();
 await assert.rejects(connector.selectRuntime({cancellation:stopped.signal}),e=>e.status===Status.Cancelled);
 await assert.rejects(connector.selectRuntime({timeout:0}),e=>e.status===Status.Timeout);
 // The endpoint override names the fixture host, which is never the selected installation.
 assert.equal(connector.runtimeEndpoint(),endpoint);
 await assert.rejects(new Machine(null,{connector,timeout:5000}).resolveService('abstraction.logging/sink@1'),
   e=>e instanceof ResolutionError&&e.status==='runtime_unavailable'&&[Status.Untrusted,Status.ProofUnavailable].includes(e.cause?.status));
 console.log('PASS native installed selection: '+(answer instanceof Error?`refused with status ${answer.status}`:'an installation is registered')+`; the endpoint override (${override===undefined?'variable':'option'}) is not trusted`);
 } else if(mode==='verified'||mode==='untrusted') {
 const [principal,program]=argv.slice(2);
 const server={principalKind:process.platform==='win32'?1:2,principal,program:mode==='untrusted'?(process.platform==='win32'?'C:\\untrusted\\other.exe':'/untrusted/other'):program};
 // These modes prove trust propagation, and cancel/timeout/queue prove deadline
 // semantics. Each call below owns a fresh budget equal to the facade's default
 // per-call timeout; no call shares a budget with another, so host load from
 // parallel builds cannot sum across calls. Latencies are printed as evidence.
 const perCall=5000;
 const verified=new Machine(endpoint,{connector,timeout:perCall,server});
 server.program='mutated caller object';
 const timed=async(label,call)=>{const start=performance.now();try{return await call();}finally{console.log(`verified-latency ${label} ${(performance.now()-start).toFixed(1)}ms budget ${perCall}ms`);}};
 if(mode==='untrusted')await timed('untrusted-resolve',()=>assert.rejects(verified.resolveService('abstraction.logging/reader@1'),e=>e instanceof ResolutionError&&e.status==='runtime_unavailable'&&e.cause?.status===Status.Untrusted));
 else {
  // A call scope covers exactly its single resolution.
  const scoped=await timed('scoped-resolve',()=>verified.callScope().resolveService('abstraction.logging/reader@1'));
  assert.equal(scoped.waiting.server.program,program);
  const binding=await timed('resolve',()=>verified.resolveService('abstraction.logging/reader@1'));
  assert.equal(binding.waiting.server.program,program);
  for(const [label,view] of [['default',()=>binding],['withWaiting',()=>binding.withWaiting()],['callScope',()=>binding.callScope()]]) {
   const selected=view();
   assert.equal(selected.waiting.server.program,program);
   assert.equal((await timed(label+'-read',()=>selected.client(HistoryReaderClient).read('',1n,65536n))).outcome,'page');
  }
 }
 console.log('PASS native verified '+mode);
} else if(mode==='forged') {
  await assert.rejects(machine.resolveService('abstraction.logging/sink@1'),e=>e.status==='invalid_resolution');
} else if(mode==='malformed') {
  const reader=new HistoryReaderClient(new FrameTransport(endpoint,{timeout:1000}));
  await assert.rejects(reader.read('',1n,65536n));
} else if(mode==='queue') {
  const blocker=new AbortController();
  const first=new FrameTransport(endpoint,{timeout:1500,cancellation:blocker.signal}).exchangeFrame(new Uint8Array([1]));
  // The fixture selects one libuv worker. Later calls must expire/cancel while queued.
  const started=performance.now();
  const cancelled=new AbortController();
  const pending=new FrameTransport(endpoint,{timeout:1000,cancellation:cancelled.signal}).exchangeFrame(new Uint8Array([2]));
  setTimeout(()=>cancelled.abort(),40);
  await assert.rejects(pending,e=>e.status===Status.Cancelled);
  await assert.rejects(new FrameTransport(endpoint,{timeout:40}).exchangeFrame(new Uint8Array([3])),e=>e.status===Status.Timeout);
  assert.ok(performance.now()-started<700);
  blocker.abort();await assert.rejects(first,e=>e.status===Status.Cancelled);
} else if(mode==='cancel'||mode==='timeout') {
  const controller=new AbortController();
  const transport=new FrameTransport(endpoint,{timeout:mode==='timeout'?80:1000,cancellation:controller.signal});
  const started=performance.now();
  const waiting=transport.exchangeFrame(new Uint8Array([1]));
  const timer=mode==='cancel'?setTimeout(()=>controller.abort(),40):null;
  try {await assert.rejects(waiting,e=>e.status===(mode==='cancel'?Status.Cancelled:Status.Timeout));}
  finally {clearTimeout(timer);}
  assert.ok(performance.now()-started<1500);
} else {
  const transport=new FrameTransport(endpoint,{timeout:1000,maxFrame:1024});
  await assert.rejects(transport.exchangeFrame(new Uint8Array([1])),e=>mode==='oversized'?e.status===Status.InvalidArgument:mode==='truncated'?e.status===Status.Disconnected:typeof e.status==='number');
}
