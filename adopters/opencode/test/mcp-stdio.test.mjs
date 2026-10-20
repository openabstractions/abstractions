import test from 'node:test';
import assert from 'node:assert/strict';
import {EventEmitter} from 'node:events';
import {PassThrough,Writable} from 'node:stream';
import {mkdtemp,readFile,rm,stat,writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {appendStderrLog,createStdioMCP} from '../mcp-stdio.js';

function fixture(handler){
  const frames=[],child=new EventEmitter();child.pid=123;child.stdout=new PassThrough();
  child.stdin=new Writable({write(bytes,_encoding,done){for(const line of bytes.toString().trim().split('\n')){const frame=JSON.parse(line);frames.push(frame);handler?.(frame,reply);}done();},final(done){done();queueMicrotask(()=>child.emit('exit',0));}});
  child.kill=()=>{child.emit('exit',0);return true;};
  const reply=value=>queueMicrotask(()=>child.stdout.write(JSON.stringify({jsonrpc:'2.0',...value})+'\n'));
  const api=createStdioMCP({program:'fixture',spawn:()=>child});
  return {api,frames,reply,child};
}
const initialized=(frame,reply)=>{if(frame.method==='initialize')reply({id:frame.id,result:{protocolVersion:'2025-11-25',serverInfo:{name:'actual-mcp'},capabilities:{}}});};

test('stdio initialize, server request refusal, parallel calls and cancellation keep distinct IDs',async()=>{
  let held;
  const f=fixture((frame,reply)=>{
    initialized(frame,reply);
    if(frame.method==='tools/call'){
      if(frame.params.name==='wait')held=frame;
      else reply({id:frame.id,result:{content:[{type:'text',text:'{"outcome":"page"}'}]}});
    }
  });
  try{
    await f.api.initialize({timeout:1000});
    assert.equal(f.frames[1].method,'notifications/initialized');
    f.reply({id:'server-request',method:'roots/list',params:{}});
    await new Promise(resolve=>setImmediate(resolve));
    assert.deepEqual(f.frames.at(-1),{jsonrpc:'2.0',id:'server-request',error:{code:-32601,message:'Method not found'}});
    const cancel=new AbortController(),slow=f.api.call('wait',{}, {signal:cancel.signal,timeout:1000});
    const fast=await f.api.call('foreground',{}, {timeout:1000});
    assert.equal(JSON.parse(fast.content[0].text).outcome,'page');
    assert.notEqual(f.frames.filter(value=>value.method==='tools/call').at(-1).id,held.id);
    cancel.abort();await assert.rejects(slow,error=>error.dispatched===true);
    assert.deepEqual(f.frames.at(-1).params,{requestId:held.id,reason:'Request cancelled'});
    f.reply({id:held.id,result:{content:[]}}); // A late cancelled response cannot resolve another call.
    assert.equal((await f.api.call('next',{}, {timeout:1000})).content.length,1);
  }finally{await f.api.close();}
  assert.equal(f.child.stdin.writableEnded,true);
});

test('malformed framing fails closed and protocol refusal proves no tool dispatch',async()=>{
  const f=fixture((frame,reply)=>{initialized(frame,reply);if(frame.method==='tools/call')reply({id:frame.id,error:{code:-32601,message:'untrusted error text'}});});
  try{await f.api.initialize();await assert.rejects(f.api.call('missing',{}),error=>error.dispatched===false&&!error.message.includes('untrusted'));}
  finally{await f.api.close();}
  const bad=fixture();const work=bad.api.initialize({timeout:1000});
  bad.child.stdout.write(Buffer.from([0xff,0x0a]));
  await assert.rejects(work,/Invalid native MCP response/);await bad.api.close();
});

test('failed child termination permits a later owned cleanup to observe positive exit',async()=>{
  const f=fixture();f.child.stdin.end=()=>{};f.child.kill=()=>false;
  await assert.rejects(f.api.close(),/did not exit/);
  f.child.emit('exit',0);
  await f.api.close();
  assert.equal(f.api.isClosed(),true);
});

test('small client speaks stdio MCP to the actual gateway binary',async t=>{
  const program=process.env.OA_A1_GATEWAY_BINARY;
  if(!program){t.skip('source owner supplies the freshly built actual gateway binary');return;}
  const state=await mkdtemp(join(tmpdir(),'oa-a1-mcp-'));
  const api=createStdioMCP({program,args:['--state-dir',state],env:{...process.env,ABSTRACTION_RUNTIME_ENDPOINT:process.env.ABSTRACTION_RUNTIME_ENDPOINT}});
  try{
    const result=await api.initialize({timeout:10000});assert.equal(result.serverInfo.name,'openabstractions-mcp');
    await assert.rejects(api.call('oa_nonexistent_source_fixture',{}, {timeout:5000}),error=>error.dispatched===false);
  }finally{await api.close();await rm(state,{recursive:true,force:true});}
});

test('helper standard error reaches the bounded gateway log with time and process',async()=>{
  const state=await mkdtemp(join(tmpdir(),'oa-helper-stderr-'));
  const log=join(state,'mcp-gateway','opencode-helpers.log');
  let options;
  const child=new EventEmitter();child.pid=4321;child.stdout=new PassThrough();child.stderr=new PassThrough();
  child.stdin=new Writable({write(_bytes,_encoding,done){done();},final(done){done();queueMicrotask(()=>child.emit('exit',0));}});
  child.kill=()=>{child.emit('exit',0);return true;};
  const api=createStdioMCP({program:'fixture',stderrLog:log,spawn:(_program,_args,value)=>{options=value;return child;}});
  try{
    assert.equal(options.stdio[2],'pipe');
    child.stderr.write('openabstractions-mcp: first\r\nsecond ');
    child.stderr.write('half\n'+'x'.repeat(5000)+'\n');
    child.stderr.end('last without newline');
    let text='';
    for(let i=0;i<100&&!text.includes('last without newline');i++){await new Promise(resolve=>setTimeout(resolve,10));text=await readFile(log,'utf8').catch(()=>'');}
    const lines=text.trimEnd().split('\n');
    assert.equal(lines.length,4);
    assert.match(lines[0],/^\d{4}-\d\d-\d\dT[^ ]+Z \[4321\] openabstractions-mcp: first$/);
    assert.match(lines[1],/ \[4321\] second half$/);
    assert.ok(lines[2].endsWith(' [truncated]')&&lines[2].length<4200);
    assert.match(lines[3],/ \[4321\] last without newline$/);
    // Past its bound the log keeps one previous generation.
    child.stderr=new PassThrough();
    const big=createStdioMCP({program:'fixture',stderrLog:log,spawn:()=>{const next=new EventEmitter();next.pid=99;next.stdout=new PassThrough();next.stderr=new PassThrough();next.stdin=new Writable({write(_b,_e,done){done();},final(done){done();queueMicrotask(()=>next.emit('exit',0));}});next.kill=()=>{next.emit('exit',0);return true;};queueMicrotask(()=>{for(let n=0;n<80;n++)next.stderr.write('y'.repeat(4000)+'\n');next.stderr.end();});return next;}});
    let previous=0;
    for(let i=0;i<300&&!previous;i++){await new Promise(resolve=>setTimeout(resolve,10));previous=(await stat(log+'.1').catch(()=>null))?.size??0;}
    await new Promise(resolve=>setTimeout(resolve,200));
    assert.ok(previous>0&&previous<=256<<10,String(previous));
    assert.ok((await stat(log)).size<=256<<10);
    await big.close();
  }finally{await api.close();await rm(state,{recursive:true,force:true});}
});

test('without a log the helper standard error is not piped',()=>{
  let options;
  const child=new EventEmitter();child.pid=1;child.stdout=new PassThrough();child.stdin=new Writable({write(_b,_e,done){done();}});
  createStdioMCP({program:'fixture',spawn:(_program,_args,value)=>{options=value;return child;}});
  assert.equal(options.stdio[2],'ignore');
});

test('helper standard error goes to the gateway state folder of each platform',async()=>{
  const {helperStderrLog}=await import('../messages.js');
  // Paths join in this host's own spelling; compare them with forward slashes.
  const slashes=value=>value.split(String.fromCharCode(92)).join('/').replace(/^[A-Za-z]:(?=\/[^/])/,'');
  assert.equal(slashes(helperStderrLog({APPDATA:'/roaming'},'win32')),'/roaming/OpenAbstractions/mcp-gateway/opencode-helpers.log');
  assert.equal(slashes(helperStderrLog({HOME:'/Users/a'},'darwin')),'/Users/a/Library/Application Support/OpenAbstractions/mcp-gateway/opencode-helpers.log');
  assert.equal(slashes(helperStderrLog({HOME:'/home/a',XDG_CONFIG_HOME:'/cfg'},'linux')),'/cfg/OpenAbstractions/mcp-gateway/opencode-helpers.log');
  assert.equal(helperStderrLog({APPDATA:'relative'},'win32'),'');
});

test('a failed stderr log rotation drops the line so the log stays bounded',async()=>{
  const state=await mkdtemp(join(tmpdir(),'oa-helper-rotate-'));
  const log=join(state,'helpers.log');
  try{
    await writeFile(log,'z'.repeat((256<<10)-10));
    const before=(await stat(log)).size;
    await appendStderrLog(log,'y'.repeat(100)+'\n',{rename:async()=>{throw Object.assign(new Error('busy'),{code:'EBUSY'});}});
    assert.equal((await stat(log)).size,before);
  }finally{await rm(state,{recursive:true,force:true});}
});

test('stderr log writes beyond 256 pending are dropped',async()=>{
  const state=await mkdtemp(join(tmpdir(),'oa-helper-queue-'));
  const log=join(state,'helpers.log');
  try{
    let last;
    for(let n=0;n<400;n++)last=appendStderrLog(log,'line '+n+'\n');
    await last;
    const lines=(await readFile(log,'utf8')).trimEnd().split('\n');
    assert.equal(lines.length,256);
    assert.equal(lines[255],'line 255');
  }finally{await rm(state,{recursive:true,force:true});}
});
