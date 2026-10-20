import {spawn as nativeSpawn} from 'node:child_process';
import {appendFile,mkdir,rename,stat} from 'node:fs/promises';
import {dirname} from 'node:path';

const maximumLine = 8 << 20;
const maximumPending = 64;
const failure = (message, dispatched = false) => Object.assign(new Error(message), {dispatched});

// Helper diagnostics: each line a helper writes to standard error is appended
// to stderrLog with its time and the helper process ID. The file keeps one
// previous generation past stderrLogLimit, so the two hold at most twice that.
const stderrLogLimit = 256 << 10;
const stderrLineLimit = 4096;
const stderrLogQueueLimit = 256;
let stderrLogWrites = Promise.resolve();
let stderrLogQueued = 0;
// appendStderrLog queues one write; lines beyond stderrLogQueueLimit pending
// writes are dropped. When rotation fails (a viewer holding the previous
// generation open on Windows), the line is dropped so the log stays bounded.
export function appendStderrLog(path,text,fs={rename}){
  if(stderrLogQueued>=stderrLogQueueLimit)return stderrLogWrites;
  stderrLogQueued++;
  stderrLogWrites=stderrLogWrites.then(async()=>{
    try{
      await mkdir(dirname(path),{recursive:true,mode:0o700});
      const size=await stat(path).then(info=>info.size,()=>0);
      if(size+Buffer.byteLength(text)>stderrLogLimit){
        try{await fs.rename(path,path+'.1');}catch{return;}
      }
      await appendFile(path,text,{mode:0o600});
    }catch{/* Diagnostics never decide whether a helper runs. */}
    finally{stderrLogQueued--;}
  });
  return stderrLogWrites;
}

// A bounded client for the existing MCP stdio transport. It owns only its
// spawned child, framing, request IDs and cancellation; services own outcomes.
export function createStdioMCP({program,args=[],cwd,env,spawn=nativeSpawn,stderrLog=''}) {
  const child=spawn(program,args,{cwd,env,stdio:['pipe','pipe',stderrLog?'pipe':'ignore'],windowsHide:true});
  if(stderrLog&&child.stderr){
    let partial='';
    const write=line=>{
      if(line.length>stderrLineLimit)line=line.slice(0,stderrLineLimit)+' [truncated]';
      void appendStderrLog(stderrLog,`${new Date().toISOString()} [${child.pid}] ${line}\n`);
    };
    child.stderr.setEncoding?.('utf8');
    child.stderr.on('data',chunk=>{
      const lines=(partial+String(chunk)).split(/\r?\n/);partial=lines.pop();
      if(partial.length>stderrLineLimit){write(partial);partial='';}
      for(const line of lines)if(line)write(line);
    });
    child.stderr.on('end',()=>{if(partial)write(partial);partial='';});
    child.stderr.on('error',()=>{});
  }
  const pending=new Map();
  let serial=0,buffer=Buffer.alloc(0),ended=false,closing;
  let exited=false;
  const exit=new Promise(resolve=>{child.once('exit',()=>{exited=true;resolve();});child.once('error',()=>{exited=true;resolve();});});
  function rejectAll(message) {
    for(const row of pending.values()){row.finish();row.reject(failure(message,row.dispatched));}
    pending.clear();
  }
  function send(value) {
    if(ended||!child.stdin?.writable)throw failure('Native MCP connection ended');
    const text=JSON.stringify(value)+'\n';
    if(Buffer.byteLength(text)>maximumLine)throw failure('Native MCP frame exceeds bound');
    child.stdin.write(text,error=>{if(error){ended=true;rejectAll('Native MCP write failed');}});
  }
  child.stdout.on('data',chunk=>{
    try{
      buffer=Buffer.concat([buffer,Buffer.from(chunk)]);
      for(;;){
        const end=buffer.indexOf(10);
        if(end<0){if(buffer.length>maximumLine)throw failure('Native MCP frame exceeds bound');break;}
        if(end>maximumLine)throw failure('Native MCP frame exceeds bound');
        const line=buffer.subarray(0,end);buffer=buffer.subarray(end+1);
        if(!line.length)continue;
        const frame=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(line));
        if(!frame||typeof frame!=='object'||Array.isArray(frame)||frame.jsonrpc!=='2.0')throw failure('Invalid native MCP frame');
        if(Object.hasOwn(frame,'id')&&typeof frame.id!=='string'&&!(typeof frame.id==='number'&&Number.isSafeInteger(frame.id)))throw failure('Invalid native MCP request ID');
        if(typeof frame.method==='string'){
          if(Object.hasOwn(frame,'id'))send({jsonrpc:'2.0',id:frame.id,error:{code:-32601,message:'Method not found'}});
          continue;
        }
        if(!Object.hasOwn(frame,'id'))throw failure('Invalid native MCP response');
        const row=pending.get(frame.id);if(!row)continue;
        if(Object.hasOwn(frame,'result')===Object.hasOwn(frame,'error'))throw failure('Invalid native MCP result');
        pending.delete(frame.id);row.finish();
        if(frame.error)row.reject(failure('Native MCP request failed',[-32601,-32602,-32600].includes(frame.error.code)?false:row.dispatched));else row.resolve(frame.result);
      }
    }catch{ended=true;rejectAll('Invalid native MCP response');void close().catch(()=>{});}
  });
  child.stdout.on('error',()=>{ended=true;rejectAll('Native MCP read failed');void close().catch(()=>{});});
  child.stdin.on('error',()=>{ended=true;rejectAll('Native MCP write failed');void close().catch(()=>{});});
  child.once('error',()=>{ended=true;rejectAll('Native MCP child unavailable');});
  child.once('exit',()=>{ended=true;rejectAll('Native MCP child ended');});
  function request(method,params,{signal,timeout=120000}={}) {
    if(ended||signal?.aborted)return Promise.reject(failure('Native MCP request cancelled'));
    if(pending.size>=maximumPending)return Promise.reject(failure('Native MCP pending bound reached'));
    if(serial>=Number.MAX_SAFE_INTEGER)return Promise.reject(failure('Native MCP request ID bound reached'));
    const id=++serial;
    return new Promise((resolve,reject)=>{
      let timer;
      const abort=()=>{
        const row=pending.get(id);if(!row)return;
        pending.delete(id);row.finish();
        if(row.dispatched&&!ended){try{send({jsonrpc:'2.0',method:'notifications/cancelled',params:{requestId:id,reason:'Request cancelled'}});}catch{}}
        reject(failure('Native MCP request cancelled',row.dispatched));
      };
      const finish=()=>{clearTimeout(timer);signal?.removeEventListener('abort',abort);};
      const row={resolve,reject,finish,dispatched:false};pending.set(id,row);
      signal?.addEventListener('abort',abort,{once:true});
      timer=setTimeout(abort,timeout);
      try{send({jsonrpc:'2.0',id,method,params});row.dispatched=true;}catch(error){pending.delete(id);finish();reject(error);}
    });
  }
  async function close() {
    if(closing)return closing;
    ended=true;rejectAll('Native MCP connection closed');
    const work=(async()=>{
      child.stdin?.end();
      let timer;
      await Promise.race([exit,new Promise(resolve=>{timer=setTimeout(resolve,1000);})]);clearTimeout(timer);
      if(!exited){child.kill();await Promise.race([exit,new Promise(resolve=>{timer=setTimeout(resolve,1000);})]);clearTimeout(timer);}
      if(!exited){child.kill('SIGKILL');await Promise.race([exit,new Promise(resolve=>{timer=setTimeout(resolve,1000);})]);clearTimeout(timer);}
      child.stdout?.destroy();child.stdin?.destroy();
      if(!exited)throw failure('Native MCP child did not exit');
    })();
    closing=work;
    // Failed cleanup retains this owned child. A later explicit cleanup may
    // retry its termination; callers must keep it counted until positive exit.
    work.catch(()=>{if(closing===work)closing=undefined;});
    return work;
  }
  return {pid:()=>child.pid,isClosed:()=>ended,
    async initialize(options){
      const result=await request('initialize',{protocolVersion:'2025-11-25',capabilities:{},clientInfo:{name:'oa-opencode-native-plugin',version:'1'}},options);
      if(result?.protocolVersion!=='2025-11-25'||!result.serverInfo||!result.capabilities){await close();throw failure('Native MCP initialization unavailable');}
      send({jsonrpc:'2.0',method:'notifications/initialized'});return result;
    },call:(name,args,options)=>request('tools/call',{name,arguments:args},options),close};
}
