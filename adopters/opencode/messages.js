import {createRequire} from 'node:module';
import {isAbsolute,basename,join} from 'node:path';
import {randomBytes} from 'node:crypto';
import {decodeChatBinding} from '@openabstractions/facade/messages';
import {assistantToolArguments} from './assistant-tools.js';
import {panelEnvironment} from './panel-launch.js';
import {createStdioMCP} from './mcp-stdio.js';
import {replyOnlyMarks} from './reply-only.js';

const require=createRequire(import.meta.url);
const agent=require('./generated/agent-tools.json');
const agentDefinitions=new Map(agent.tools.map(value=>[value.name,value]));
export function agentToolInputSchema(name){
  const metadata=agentDefinitions.get(name);if(!metadata)return;
  // The caller's inbox association remains host-owned; destinations retain
  // the native schema unchanged. Return a fresh value for each host hook.
  const schema=structuredClone(name==='oa_messages_inbox_open'?{type:'object',properties:{},additionalProperties:false}:metadata.inputSchema);
  // AB-1s F2: the plugin alone holds its child's adapter key and sets these.
  if(name==='oa_messages_watch')for(const field of adapterFields)delete schema.properties?.[field];
  return schema;
}
const adapterFields=['adapter_key','reserve_wake','wake_ticket','turn'];
// Models Connect retains the existing host-authenticating OpenCode owner.
// oa_image_generate is the plugin's own host tool, which delegates to the
// helper's for a picture in Pictures (image-tool.js).
export const messageTools=Object.freeze(agent.tools.filter(value=>value.name!=='oa_models_connect'&&value.name!=='oa_image_generate'));
export const messageToolNames=Object.freeze(messageTools.map(value=>value.name));
if(new Set(messageToolNames).size!==messageToolNames.length||!messageToolNames.every(name=>/^oa_[a-z0-9_]+$/.test(name)))throw Error('Invalid generated OA roster');
// The helper's instructions plus the one OpenCode fact they lack. Each
// result's steps carry the rest (helperSteps).
export const messageInstructions=agent.instructions+' An OA pointer starts one turn in an idle chat.';
export function validHostChatID(id){
  if(typeof id!=='string'||!id)return false;
  try{return decodeChatBinding(new TextEncoder().encode(JSON.stringify({chat_id:id}))).chatId===id;}catch{return false;}
}
function hostChatTitle(id,value){
  if(typeof value!=='string'||/[\p{Cc}\u2028\u2029\u202a-\u202e\u2066-\u2069\uD800-\uDFFF]/u.test(value))return '';
  try{return decodeChatBinding(new TextEncoder().encode(JSON.stringify({chat_id:id,chat_title:value}))).chatTitle??'';}catch{return '';}
}
function decoded(result){
  if(result?.structuredContent&&typeof result.structuredContent==='object')return result.structuredContent;
  const parts=result?.content?.filter(value=>value.type==='text');
  if(parts?.length!==1||typeof parts[0].text!=='string')throw Error('Invalid native MCP tool response');
  if(result.isError===true)return {outcome:'error',reason:'native_tool_error',message:parts[0].text};
  return JSON.parse(parts[0].text);
}
// The native session start marks a pending chat as an MCP tool error and
// carries the retry disposition in one text item. Other tool errors stay errors.
function decodeSessionStart(result,id){
  const opened=decoded(result);
  if(result?.isError!==true||result.structuredContent||opened.reason!=='native_tool_error'||opened.message.length>1024)return opened;
  let reply;
  try{reply=JSON.parse(opened.message);}catch{return opened;}
  if(reply?.outcome!=='unavailable'||reply.session_id!==id||reply.context!=='')return opened;
  const fields=Object.keys(reply).length;
  if(fields===3||(fields===4&&reply.adapter==='accepted'))return {outcome:'unavailable'};
  return opened;
}
// The helper's next steps for the model: text it appends after a structured
// result, addressed to the assistant and never marked untrusted (peer text is).
// Tool descriptions stay short because these steps arrive with the outcome
// that needs them, such as an uncertain Send or a pending access request.
function helperSteps(result){
  const parts=Array.isArray(result?.content)?result.content.slice(1):[];
  return parts.filter(value=>value?.type==='text'&&typeof value.text==='string'&&value.text
    &&Array.isArray(value.annotations?.audience)&&value.annotations.audience.includes('assistant')
    &&!value._meta?.['org.openabstractions/untrusted']).map(value=>value.text);
}
function modelResult(result){
  const value=decoded(result),steps=result?.structuredContent&&typeof result.structuredContent==='object'?helperSteps(result):[];
  return JSON.stringify(steps.length&&value&&typeof value==='object'&&!Array.isArray(value)?{...value,steps}:value);
}
// The detail names the last verified setup stage. Never return exception
// text, helper paths or an unverified session reply to the chat.
const helperGuidance={
  host_chat:'Try again from an open OpenCode chat in this project.',
  host_lookup:'OpenCode could not read this chat. Reopen the project and try again.',
  helper_selection:'Open the Abstraction Panel and check that OpenCode is connected, then retry.',
  helper_launch:'OpenCode could not start the connected helper. Check OpenCode in the Abstraction Panel, then retry.',
  helper_initialize:'The connected helper did not initialize. Check OpenCode in the Abstraction Panel, then retry.',
  helper_binding_refused:'The helper refused this chat binding. Reopen this chat and check OpenCode in the Abstraction Panel.',
  helper_binding:'The helper could not confirm this chat binding. Reopen this chat and try again.',
  helper_capacity:'Wait for another OpenCode chat to finish using its helper, then retry.',
};
class NativeHelperUnavailable extends Error{
  constructor(stage){super(stage);this.stage=stage;}
}
function unavailable(stage){
  const detail=stage&&Object.hasOwn(helperGuidance,stage)?{stage,guidance:helperGuidance[stage]}:{};
  return JSON.stringify({outcome:'unavailable',reason:'native_helper_unavailable',...detail});
}

// The helper renders the pointer from these generated templates. The plugin
// submits only text that matches one exactly; peer text never reaches a prompt.
const wakePrompt=require('./generated/messages-tools.json').wake_prompt;
// ASK-R14: the fixed pointer for a question this chat's enrolled program may
// answer. It carries no question text, asker or ID.
export const asksPointer=wakePrompt.asks;
const validTicket=value=>typeof value==='string'&&/^(?:[0-9a-f]{8}|[0-9a-f]{64})$/.test(value);
const validSender=value=>typeof value==='string'&&value.length>0&&[...value].length<=32&&value.trim()===value&&/^[\p{L}\p{N}\p{M} _-]+$/u.test(value);
export function validWakePointer(text,ticket){
  if(typeof text!=='string'||!validTicket(ticket)||text.length>512)return false;
  // A pointer sent before the prefix read "OpenAbstractions: " reads as its current form.
  if(text.startsWith('OA: '))text='OpenAbstractions: '+text.slice(4);
  const render=value=>value.replaceAll('{ticket}',ticket);
  if(ticket.length===64)return text===render(wakePrompt.legacy);
  if([wakePrompt.repeat,wakePrompt.anonymous].some(value=>text===render(value)))return true;
  const [prefix,suffix]=render(wakePrompt.first).split('{sender}');
  if(!text.startsWith(prefix)||!text.endsWith(suffix))return false;
  return validSender(text.slice(prefix.length,text.length-suffix.length));
}
function watchPage(value){
  if(!value||typeof value!=='object'||value.outcome!=='page'||typeof value.context!=='string'||!value.context
    ||typeof value.revision!=='string'||Buffer.byteLength(value.revision)>128
    ||typeof value.wake!=='string'||Buffer.byteLength(value.wake)>128||/[\p{Cc}]/u.test(value.wake))return null;
  const count=(key,max)=>Number.isSafeInteger(value[key])&&value[key]>=0&&value[key]<=max?value[key]:null;
  const waitingKey=typeof value.waiting_asks_wake==='string'&&Buffer.byteLength(value.waiting_asks_wake)<=128&&!/[\p{Cc}]/u.test(value.waiting_asks_wake)?value.waiting_asks_wake:'';
  return {context:value.context,revision:value.revision,wake:value.wake,reason:typeof value.reason==='string'?value.reason:'',
    // Serve counts up to 50 incoming records plus 1024 actor delivery events.
    unread:count('unread',50+1024),questions:count('pending_questions',50),replies:count('awaited_replies',50),
    waitingAsks:count('waiting_asks',64),answerableAsks:count('answerable_asks',128),waitingKey,
    ticket:validTicket(value.wake_ticket)?value.wake_ticket:'',pointer:typeof value.wake_pointer==='string'?value.wake_pointer:'',
    sender:validSender(value.wake_sender)?value.wake_sender:'',
    // S5 (2026-11-22): present while OA marks a turn reply-only; true when the
    // person allowed full tool use for every sender of that turn.
    peerFullTools:value.peer_full_tools===true,
    // ASK-R14: the ordered key of what this chat's enrolled program may answer.
    asksWake:typeof value.asks_wake==='string'&&Buffer.byteLength(value.asks_wake)<=128&&!/[\p{Cc}]/u.test(value.asks_wake)?value.asks_wake:'',
    // An older helper reports only the oldest unread acceptance.
    newestUnread:[value.newest_unread_unix_ms,value.oldest_unread_unix_ms].find(at=>Number.isSafeInteger(at)&&at>0)??0};
}
const watchWaitMs=25000,watchFloorMs=1000;
// How many of a project's most recent sessions stay reachable while closed.
const dormantSessions=8;

// The gateway's own state folder, os.UserConfigDir()/OpenAbstractions/mcp-gateway,
// keeps the helpers' standard error beside helper-lifecycle.json.
export function helperStderrLog(environment=process.env,platform=process.platform){
  const home=environment.HOME||environment.USERPROFILE;
  const base=platform==='win32'?environment.APPDATA
    :platform==='darwin'?home&&join(home,'Library','Application Support')
    :environment.XDG_CONFIG_HOME||home&&join(home,'.config');
  return typeof base==='string'&&isAbsolute(base)?join(base,'OpenAbstractions','mcp-gateway','opencode-helpers.log'):'';
}

// Only native Connect selects this executable. Tool/config/options text never
// reaches the launcher. The helper's own kept native peer supplies authority.
function openKeptChild({program,id,title,directory,environment,adapterKey}){
  if(typeof program!=='string'||!isAbsolute(program)||/[\0\r\n]/.test(program)
    ||!/^oa-opencode-integration(?:-[0-9a-f]{64})?(?:\.exe)?$/i.test(basename(program)))throw new NativeHelperUnavailable('helper_selection');
  return createStdioMCP({program,cwd:directory,env:{...panelEnvironment(environment),OA_OPENCODE_HOST_CHAT_ID:id,OA_OPENCODE_HOST_CHAT_TITLE:title,OA_OPENCODE_ADAPTER_KEY:adapterKey},stderrLog:helperStderrLog(environment)});
}

// ToolContext/session events are documented host facts. Every chat retains one
// child and parallel MCP request IDs. The live-child LRU is bounded at32.
const latestKey=(observed='',hinted='')=>observed>hinted?observed:hinted;

export function createMessages({tool,directory,client,helperProgram=()=>'',environment=process.env,
  open=openKeptChild,limit=32,ready=async()=>true,reconnect=async()=>helperProgram(),wake=false,schedule=setTimeout,unschedule=clearTimeout,clock=Date.now}){
  const children=new Map();
  // S5 (2026-11-22): reply-only marks by host chat ID. The module map
  // outlives an instance reload of this directory (reload-handover.js).
  const markKey=id=>JSON.stringify([directory,id]);
  const definitions={};
  let closed=false,lifecycle=0,used=0,allocation=Promise.resolve();
  const current=row=>!closed&&!row.ended&&children.get(row.id)===row;
  const waking=()=>wake&&typeof client?.session?.promptAsync==='function'&&typeof client?.session?.status==='function';
  function transform({messages}={}){
    if(!wake||!Array.isArray(messages)||!messages.length)return;
    const session=messages.findLast(message=>message?.info?.sessionID)?.info.sessionID;
    if(!session||messages.some(message=>message?.info?.sessionID&&message.info.sessionID!==session))return;
    const row=children.get(session),page=row?.turnPage;
    if(!row||!current(row)||!row.busy||!row.live||!page)return;
    const message=messages.findLast(item=>item?.info?.sessionID===session&&item.info.role==='user'&&typeof item.info.id==='string'&&Array.isArray(item.parts));
    if(!message)return;
    const fresh=!row.messageNotice&&page.wake&&row.floor!==null&&page.wake>row.floor;
    const reply=fresh&&page.replies>0&&wakePrompt.awaited_replies;
    const askEligible=!row.askNotice&&asksTarget()===row&&page.asksWake>row.asksHigh;
    const urgentAsk=askEligible&&page.waitingAsks>0&&page.waitingKey>row.waitingHint&&wakePrompt.waiting_asks;
    const ordinaryAsk=askEligible&&page.answerableAsks>0&&wakePrompt.answerable_asks;
    const ordinary=fresh&&page.unread>0&&page.questions!==null&&wakePrompt.summary;
    const urgentAllowed=clock()-row.lastUrgent>=30000;
    const showReply=urgentAllowed&&reply,showAsk=urgentAllowed&&urgentAsk;
    const hints=[];
    if(showReply||ordinary&&(!reply||page.unread>page.replies)){
      hints.push(showReply?showReply.replaceAll('{awaited_replies}',String(page.replies)):ordinary.replaceAll('{unread}',String(page.unread-(reply?page.replies:0))).replaceAll('{pending_questions}',String(page.questions)));
      row.messageNotice={wake:page.wake};
    }
    if(showAsk||ordinaryAsk&&!urgentAsk){
      hints.push(showAsk?showAsk.replaceAll('{waiting_asks}',String(page.waitingAsks)):ordinaryAsk.replaceAll('{answerable_asks}',String(page.answerableAsks)));
      row.askNotice={wake:page.asksWake,waitingKey:page.waitingKey};
    }
    if(!hints.length)return;
    if(showReply||showAsk)row.lastUrgent=clock();
    const index=messages.indexOf(message);
    // OpenCode consumes this array in place. Copy the message and its parts
    // so a later transform of the same stored message has no old hint.
    messages[index]={...message,parts:[...message.parts,{type:'text',id:'prt_'+randomBytes(12).toString('hex'),messageID:message.info.id,sessionID:session,synthetic:true,text:hints.join(' ')}]};
  }
  const pause=(row,ms)=>new Promise(resolve=>{
    const signal=row.abort.signal;if(signal.aborted)return resolve();
    let timer;const done=()=>{unschedule(timer);signal.removeEventListener('abort',done);resolve();};
    signal.addEventListener('abort',done,{once:true});timer=schedule(done,ms);
  });
  // OpenCode lists only working sessions in its status map. A missing or
  // unreadable map is never treated as idle.
  async function idle(row){
    if(row.busy||!current(row))return false;
    try{
      const out=await client.session.status({throwOnError:true,signal:AbortSignal.any([row.abort.signal,AbortSignal.timeout(2000)])});
      const map=out?.data;if(!map||typeof map!=='object'||Array.isArray(map))return false;
      const status=map[row.id];
      return current(row)&&!row.busy&&(status===undefined||status?.type==='idle');
    }catch{return false;}
  }
  // A woken turn keeps the agent and model of the person's last turn there.
  async function lastChoice(id){
    try{
      const out=await client.session.messages({path:{id},query:{limit:20},signal:AbortSignal.timeout(2000)});
      const info=(Array.isArray(out?.data)?out.data:[]).map(value=>value?.info).filter(value=>value?.role==='user').at(-1);
      const choice={};
      if(typeof info?.agent==='string'&&info.agent)choice.agent=info.agent;
      if(typeof info?.model?.providerID==='string'&&typeof info?.model?.modelID==='string')choice.model={providerID:info.model.providerID,modelID:info.model.modelID};
      return choice;
    }catch{return {};}
  }
  function notice(sender){
    try{void Promise.resolve(client?.tui?.showToast?.({body:{title:'OpenAbstractions',message:sender?`Message from ${sender}`:'New message',variant:'info'}})).catch(()=>{});}
    catch{/* Display supplies no delivery. */}
  }
  // A message whose wake failed waits in the inbox; the person learns it is there.
  function waitingNotice(sender){
    try{void Promise.resolve(client?.tui?.showToast?.({body:{title:'OpenAbstractions',message:`${sender?`New message from ${sender}`:'New message'}; it waits until this chat is idle.`,variant:'info'}})).catch(()=>{});}
    catch{/* Display supplies no delivery. */}
  }
  // After an unknown delivery, waking retries once: at the next idle event
  // or after 60 seconds, whichever comes first. A further failure stays paused
  // until the chat's next turn.
  const pausedRetryMs=60000;
  function resumePaused(row){
    if(!row.retry||!row.paused||!current(row))return;
    row.retry=false;
    if(row.retryTimer){unschedule(row.retryTimer);row.retryTimer=null;}
    row.paused=false;
    void wakeChat(row);
  }
  // AB-1s F2: OA honors a turn signal or reservation only with the key this
  // plugin gave its child. It goes only to a child whose start answered that
  // it holds it; an older child refuses the unknown field.
  const keyed=row=>row.adapter?{adapter_key:row.key}:{};
  // AB-1s ruling 2026-11-09: OA learns where turns begin and end, so a woken
  // turn's messages carry its chain and a person's turn starts at zero. A lost
  // signal ends at OA's 30-minute lease. Signals go out in order, and the next
  // wake waits for one in flight so it never clears a newer woken turn.
  function signalTurn(row,turn){
    const sent=(row.turnSignal||Promise.resolve()).then(async()=>{
      try{
        await row.starting;if(!current(row))return;
        await row.api.call('oa_messages_watch',{revision:'',wait_ms:0,turn,...keyed(row)},{signal:row.abort.signal,timeout:10000});
      }catch{/* OA's lease ends a chain whose signal was lost. */}
    });
    row.turnSignal=sent;row.tasks.add(sent);void sent.finally(()=>row.tasks.delete(sent));
    return sent;
  }
  // One pointer per reserved wake. The helper's ledger refuses a new ticket
  // while an earlier pointer is unread. Keep the same bound in this chat
  // after submission, even if the helper's reservation lease expires.
  async function wakeChat(row){
    if(row.paused&&row.wake&&row.wake!==row.toldWake&&current(row)&&row.floor!==null&&row.wake>row.floor){
      // A newer message reached a chat whose wake is paused.
      row.toldWake=row.wake;waitingNotice(row.sender);
    }
    if(!waking()||row.waking||row.paused||row.pendingWake?.submitted||!row.wake||!current(row))return;
    // A chat reached only because it is recent never wakes for messages its
    // own turns already saw; see watch.
    if(row.floor===null||!(row.wake>row.floor))return;
    row.waking=true;
    try{
      if(row.turnSignal)await row.turnSignal;
      if(!await idle(row))return;
      const wake=row.wake, readGeneration=row.wakeReads;
      const args={revision:'',wait_ms:0,reserve_wake:wake,...keyed(row)};
      const previousTicket=row.pendingWake?.ticket||row.held;
      if(previousTicket)args.wake_ticket=previousTicket;
      const page=watchPage(decoded(await row.api.call('oa_messages_watch',args,{signal:row.abort.signal,timeout:10000})));
      if(!page||!current(row)||page.context!==row.context||row.wakeReads!==readGeneration)return;
      // Another watch can observe newer content while this reservation is in flight.
      if(row.wake!==wake)return;
      row.wake=page.wake;
      if(!page.wake||page.wake!==wake||!page.ticket||!validWakePointer(page.pointer,page.ticket)){row.held='';return;}
      row.held=page.ticket;
      if(!await idle(row))return;
      const choice=await lastChoice(row.id);
      if(!await idle(row))return;
      try{
        if(!current(row)||row.wake!==wake)return;
        if(row.wakeReads!==readGeneration){row.held='';return;}
        const pending={ticket:page.ticket,wake,submitted:false};
        row.pendingWake=pending;
        // The next busy turn is the one this pointer starts. An unknown
        // delivery keeps that mark: a person's turn then signals nothing.
        row.started=true;
        // S5 (2026-11-22): that turn is reply-only until the person's next
        // turn in this chat (reply-only.js).
        replyOnlyMarks.set(markKey(row.id),{sender:page.sender,full:page.peerFullTools});
        // Retirement or disposal aborts a pointer still in flight.
        await client.session.promptAsync({path:{id:row.id},body:{...choice,parts:[{type:'text',text:page.pointer}]},throwOnError:true,
          signal:AbortSignal.any([row.abort.signal,AbortSignal.timeout(10000)])});
        pending.submitted=true;
      }catch{
        if(!row.pendingWake)return;
        // Delivery is unknown. The held ticket stays outstanding; the next
        // turn in this chat, by the person or the pointer, resumes waking.
        // The first failure since that turn retries the same ticket once and
        // tells the person the message is waiting.
        row.paused=true;
        if(!row.retried&&current(row)){
          row.retried=true;row.retry=true;
          row.retryTimer=schedule(()=>{row.retryTimer=null;resumePaused(row);},pausedRetryMs);
          row.toldWake=row.wake;waitingNotice(page.sender);
        }
        return;
      }
      row.held='';row.busy=true;notice(page.sender);
    }catch{/* Observation recovers on the next watch page or idle event. */}
    finally{row.waking=false;}
  }
  // ASK-R14: a question this program may answer wakes one open chat: the
  // live chat the person used last. Every other chat takes the key without a
  // turn, so one question starts one turn. Dormant chats are never woken
  // for a question; that would change start consent.
  function asksTarget(){
    let best=null;
    for(const row of children.values())if(row.live&&current(row)&&(!best||row.used>best.used))best=row;
    return best;
  }
  async function wakeAsks(row){
    if(!waking()||!row.live||row.waking||row.paused||!current(row)||!row.asksWake||!(row.asksWake>row.asksHigh))return;
    if(asksTarget()!==row){row.asksHigh=row.asksWake;return;}
    row.waking=true;
    try{
      if(row.turnSignal)await row.turnSignal;
      if(!await idle(row))return;
      const page=watchPage(decoded(await row.api.call('oa_messages_watch',{revision:'',wait_ms:0},{signal:row.abort.signal,timeout:10000})));
      if(!page||!current(row)||page.context!==row.context)return;
      row.asksWake=page.asksWake;
      if(!page.asksWake||!(page.asksWake>row.asksHigh))return;
      const choice=await lastChoice(row.id);
      if(!await idle(row)||!current(row))return;
      // One pointer per key, also when its delivery stays unknown.
      row.asksHigh=page.asksWake;row.started=true;
      try{
        await client.session.promptAsync({path:{id:row.id},body:{...choice,parts:[{type:'text',text:asksPointer}]},throwOnError:true,
          signal:AbortSignal.any([row.abort.signal,AbortSignal.timeout(10000)])});
      }catch{return;}
      row.busy=true;
    }catch{/* The next watch page or idle event retries. */}
    finally{row.waking=false;}
  }
  // An open headers Wait marks this chat as watching. Each page records the
  // current wake key; an idle chat with a wake gets one pointer turn.
  async function watch(row){
    let revision='',delay=0;
    while(current(row)){
      const started=Date.now();let page=null;
      try{page=watchPage(decoded(await row.api.call('oa_messages_watch',{revision,wait_ms:watchWaitMs},{signal:row.abort.signal,timeout:watchWaitMs+10000})));}
      catch{/* Recovery below. */}
      if(!current(row))return;
      if(!page||page.context!==row.context){
        row.turnPage=null;
        revision='';delay=Math.min(30000,Math.max(watchFloorMs,delay*2));
        await pause(row,delay);
        if(current(row)&&row.api.isClosed?.()){
          row.reconnecting=true;
          row.starting=reopen(row);
          try{await row.starting;}catch{/* A later watch iteration retries the read-only reconnect. */}
          finally{row.reconnecting=false;}
        }
        continue;
      }
      delay=0;revision=page.revision;row.wake=page.wake;if(page.wake)row.sender=page.sender;
      row.turnPage=page;
      // A closed chat reached at start wakes once when its newest waiting
      // message arrived after its last activity, also beside an older unread
      // note. Messages its last turns already saw keep it asleep. The helper's
      // ledger refuses a second wake for the same key.
      if(row.floor===null)row.floor=page.newestUnread>row.lastActive?'':page.wake;
      row.asksWake=page.asksWake;
      if(page.wake)await wakeChat(row);
      await wakeAsks(row);
      if(Date.now()-started<watchFloorMs)await pause(row,watchFloorMs);
    }
  }
  function launch(row,program){
    try{return open({program,id:row.id,title:row.title,directory,environment,adapterKey:row.key});}
    catch(error){throw error instanceof NativeHelperUnavailable?error:new NativeHelperUnavailable('helper_launch');}
  }
  // Replace only a closed stdio transport. Re-read native Connect and require
  // the successor to prove the same canonical inbox before watching again.
  async function reopen(row){
    await row.api.close();
    if(row.candidate){await row.candidate.close();row.candidate=null;}
    if(!current(row))throw Error('Native helper owner ended');
    const program=await reconnect();
    if(!current(row)||!program)throw Error('Native helper unavailable');
    const api=launch(row,program);
    row.candidate=api;
    try{
      const opened=await bind(row,api,program);
      if(!current(row)||opened.context!==row.context)throw Error('Native helper correlation changed');
      row.program=program;row.api=api;row.adapter=opened.adapter==='accepted';row.candidate=null;
      return api;
    }catch(error){await api.close();row.candidate=null;throw error;}
  }
  async function bind(row,api,program){
    try{await api.initialize({signal:row.abort.signal,timeout:10000});}
    catch{throw new NativeHelperUnavailable('helper_initialize');}
    let opened;
    for(let attempts=0;attempts<5;attempts++){
      try{opened=decodeSessionStart(await api.call('oa_opencode_session_start',{}, {signal:row.abort.signal,timeout:10000}),row.id);}
      catch{throw new NativeHelperUnavailable('helper_binding');}
      if(!current(row)||helperProgram()!==program)throw new NativeHelperUnavailable('helper_selection');
      if(opened?.outcome==='observed')break;
      if(opened?.outcome!=='unavailable')throw new NativeHelperUnavailable(opened?.outcome?'helper_binding_refused':'helper_binding');
      await new Promise(resolve=>setTimeout(resolve,100));
    }
    if(opened?.outcome!=='observed'||opened.session_id!==row.id||typeof opened.context!=='string'||!opened.context||Buffer.byteLength(opened.context)>64||/[\p{Cc}]/u.test(opened.context))throw new NativeHelperUnavailable('helper_binding');
    return opened;
  }
  async function finish(row){
    if(!row)return;
    row.ended=true;row.abort.abort();
    if(row.retryTimer){unschedule(row.retryTimer);row.retryTimer=null;}
    if(row.api)await row.api.close();
    await Promise.allSettled([row.starting,...row.tasks]);
    if(row.candidate)await row.candidate.close();
  }
  async function retire(row){
    await finish(row);
    if(children.get(row.id)===row)children.delete(row.id);
  }
  async function actual(id,signal){
    if(closed||!validHostChatID(id)||typeof client?.session?.get!=='function')return;
    const generation=lifecycle;
    let page;
    try{page=await client.session.get({path:{id},throwOnError:true,
      signal:signal?AbortSignal.any([signal,AbortSignal.timeout(2000)]):AbortSignal.timeout(2000)});}
    catch{throw new NativeHelperUnavailable('host_lookup');}
    if(closed||generation!==lifecycle||page?.error||page?.data?.id!==id||page.data.directory!==directory)return;
    return {id,generation,title:hostChatTitle(id,page.data.title),subagent:Boolean(page.data.parentID)};
  }
  // A rename, or OpenCode's own title after the first turn, reaches the open
  // helper through its start call. The chat keeps its helper, inbox and Wait.
  // A helper without title updates refuses the argument; it is then restarted
  // with the new title once idle.
  function retitle(row,title){
    row.wanted=title;
    if(row.retitling)return;
    const work=(async()=>{
      try{
        await row.starting;
        while(current(row)&&!row.fixedTitle&&row.wanted!==row.title){
          const next=row.wanted;
          const out=decoded(await row.api.call('oa_opencode_session_start',{chat_title:next},{signal:row.abort.signal,timeout:10000}));
          if(!current(row))return;
          if(out?.outcome==='invalid'){row.fixedTitle=true;return;}
          if(out?.outcome!=='observed'||out.session_id!==row.id||out.context!==row.context)return;
          row.title=next;
        }
      }catch{/* The next session event retries. */}
      finally{row.retitling=null;row.tasks.delete(work);}
    })();
    row.retitling=work;row.tasks.add(work);
  }
  async function child(id,generation,title,lastActive=null){
    const before=allocation;let release;allocation=new Promise(resolve=>{release=resolve;});await before;
    try{
    if(closed||generation!==lifecycle)throw new NativeHelperUnavailable('host_chat');
    const program=helperProgram();
    if(typeof program!=='string'||!program)throw new NativeHelperUnavailable('helper_selection');
    let row=children.get(id);
    // A helper that cannot take a title update is restarted for a rename on
    // the next idle call. It never tears down an active Wait solely for a
    // title; the host ID and canonical inbox persist.
    const recovering=row&&(row.reconnecting||waking()&&row.api?.isClosed?.());
    if(row&&(row.ended||!recovering&&(row.program!==program||row.api?.isClosed?.()||row.fixedTitle&&row.title!==title&&row.active===0))){await retire(row);row=null;}
    if(row&&!row.fixedTitle&&row.title!==title)retitle(row,title);
    if(!row){
      if(children.size>=limit){
        const oldest=[...children.values()].filter(value=>value.active===0).sort((a,b)=>a.used-b.used)[0];
        if(!oldest)throw new NativeHelperUnavailable('helper_capacity');
        await retire(oldest);
      }
      if(closed||generation!==lifecycle)throw new NativeHelperUnavailable('host_chat');
      // floor: the highest wake key that starts no turn. A row created by a
      // host event gets ''. A row reachDormant created first stays null until
      // its first watch page, compared with lastActive, the session's last
      // update in Unix milliseconds.
      const dormant=lastActive!==null;
      row={id,title,program,key:randomBytes(16).toString('hex'),adapter:false,abort:new AbortController(),tasks:new Set(),used:++used,active:0,busy:false,paused:false,retried:false,retry:false,retryTimer:null,toldWake:'',sender:'',waking:false,wake:'',held:'',pendingWake:null,wakeReads:0,started:false,turn:'',turnSignal:null,floor:dormant?null:'',lastActive:dormant?lastActive:0,live:!dormant,asksWake:'',asksHigh:''};
      children.set(id,row);
      Object.assign(row,{turnPage:null,messageNotice:null,askNotice:null,waitingHint:'',lastUrgent:-Infinity});
      row.starting=(async()=>{
        row.api=launch(row,program);
        const opened=await bind(row,row.api,program);
        row.context=opened.context;
        row.adapter=opened.adapter==='accepted';
        if(waking())void watch(row);
        return row.api;
      })().catch(async error=>{row.ended=true;row.abort.abort();await row.api?.close();if(children.get(id)===row)children.delete(id);throw error;});
      row.starting.catch(()=>{});
    }
    row.used=++used;row.active++;return row;
    }finally{release();}
  }
  async function execute(name,args,context){
    let row,dispatched=false;
    try{
      const fact=await actual(context?.sessionID,context?.abort);if(!fact)return unavailable('host_chat');
      row=await child(fact.id,fact.generation,fact.title);row.live=true;
      const api=await row.starting;if(!current(row))return unavailable('host_chat');
      const signal=context?.abort?AbortSignal.any([row.abort.signal,context.abort]):row.abort.signal;
      if(name==='oa_messages_request_access'&&!signal.aborted){
        try{context.metadata?.({title:'Checking message access. If OpenAbstractions asks, answer in its permission popup.'});}
        catch{/* Host display never changes the native request. */}
      }
      const pendingAtRead=row.pendingWake;
      const heldAtRead=row.held;
      // A read can finish before the next model step has rendered a hint.
      // Reconcile only the watch keys already observed when dispatching it.
      const observed=row.busy?row.turnPage:null;
      const messageNotice=row.messageNotice;
      const askNotice=row.askNotice;
      const messageKey=latestKey(observed?.wake,messageNotice?.wake);
      const askKey=latestKey(observed?.asksWake,askNotice?.wake);
      const waitingKey=latestKey(observed?.waitingKey,askNotice?.waitingKey);
      const work=api.call(name,args,{signal,timeout:125000});dispatched=true;row.tasks.add(work);
      let result;try{result=await work;}finally{row.tasks.delete(work);}
      if(!current(row))return JSON.stringify({outcome:'unknown',reason:'native_reply_lost',reconciliation:'Reconcile only identical arguments in this original host chat.'});
      if(name==='oa_messages_wait'&&decoded(result).outcome==='page'){
        if(row.pendingWake===pendingAtRead&&row.held===heldAtRead
          &&(!args.wake_ticket||args.wake_ticket===(pendingAtRead?.ticket||heldAtRead))){
          row.wakeReads++;
          if(pendingAtRead&&pendingAtRead.wake>row.floor)row.floor=pendingAtRead.wake;
          row.pendingWake=null;row.held='';
        }
      }
      if(name==='oa_messages_wait'&&!args.wake_ticket&&decoded(result).outcome==='page'){
        if(row.floor!==null&&messageKey>row.floor)row.floor=messageKey;
        if(row.messageNotice?.wake<=messageKey)row.messageNotice=null;
      }
      if(name==='oa_asks_list_answerable'&&decoded(result).outcome==='page'){
        if(askKey>row.asksHigh)row.asksHigh=askKey;
        if(waitingKey>row.waitingHint)row.waitingHint=waitingKey;
        if(row.askNotice?.wake<=askKey&&row.askNotice.waitingKey<=waitingKey)row.askNotice=null;
      }
      return modelResult(result);
    }catch(error){
      if(dispatched&&error?.dispatched!==false)return JSON.stringify({outcome:'unknown',reason:'native_reply_lost',reconciliation:'Reconcile only identical arguments in this original host chat; never resend automatically.'});
      return unavailable(error instanceof NativeHelperUnavailable?error.stage:undefined);
    }finally{if(row)row.active--;}
  }
  // Reports whether the chat now has its helper, or needs none: false when
  // native readiness, the host lookup or the helper start failed.
  async function observe(id,lastActive=null){
    let row;
    try{
      // A subagent session's events never take a helper slot from a chat;
      // its own tool calls still open one.
      const fact=await actual(id);if(!fact||fact.subagent)return true;
      if(!await ready())return false;
      row=await child(fact.id,fact.generation,fact.title,lastActive);
      await row.starting;
      return true;
    }catch{
      // A later genuine host event may retry native readiness or startup.
      // Lifecycle observation never replays a Messages or Asks operation.
      return false;
    }finally{if(row)row.active--;}
  }
  for(const metadata of messageTools)Object.defineProperty(definitions,metadata.name,{enumerable:true,get:()=>{
    // The only changed schema is the caller association: inbox_open has no
    // inputs. Destination routing and exact native Send arguments stay intact.
    const schema=agentToolInputSchema(metadata.name);
    const fields=assistantToolArguments(schema,tool.schema),validator=tool.schema.object(fields).strict();
    return tool({description:metadata.description,args:fields,execute:async(args,context)=>{
      if(typeof validator.safeParse!=='function')return unavailable();
      const parsed=validator.safeParse(args??{});if(!parsed.success)return JSON.stringify({outcome:'invalid'});
      return execute(metadata.name,parsed.data,context);
    }});
  }});
  // Owner, 2026-11-09: dormant chats must be reachable. OpenCode's server
  // starts a turn in any stored session of this project through promptAsync,
  // also one nobody opened since OpenCode started. While OpenCode runs, the
  // most recent top-level sessions of this project get their own kept helper,
  // which binds the chat and wakes it as for an open chat, once for messages
  // that arrived after its last activity. Messages its own turns already saw
  // start no turn: on 2026-11-09 such notes started an old chat seconds after
  // the person said hi in a new one. Sessions are never created; subagent
  // sessions are left alone.
  // Reports success: false when the session list or a chat's helper failed,
  // and the caller tries again later.
  async function reachDormant(count=dormantSessions){
    if(closed||!waking()||typeof client?.session?.list!=='function')return true;
    const generation=lifecycle;
    let sessions;
    try{
      const out=await client.session.list({query:{directory},throwOnError:true,signal:AbortSignal.timeout(5000)});
      sessions=Array.isArray(out?.data)?out.data:[];
    }catch{return false;}
    const recent=sessions.filter(value=>value&&value.directory===directory&&!value.parentID&&validHostChatID(value.id)&&Number.isFinite(value.time?.updated))
      .sort((a,b)=>b.time.updated-a.time.updated).slice(0,Math.max(0,Math.min(count,limit-1)));
    let reached=true;
    for(const value of recent){
      if(closed||generation!==lifecycle)return true;
      if(!children.has(value.id)&&!await observe(value.id,value.time.updated))reached=false;
    }
    return reached;
  }
  // The host calls this at every event. The closed chats are reached once;
  // a failed reach retries at a later event, backing off up to 30 seconds.
  const dormant={done:false,running:null,next:0,delay:0};
  function reachDormantDue(){
    if(dormant.done||dormant.running||clock()<dormant.next)return dormant.running??undefined;
    dormant.running=(async()=>{
      let reached=false;
      try{reached=await reachDormant();}catch{/* Retried below. */}
      if(reached)dormant.done=true;
      else{dormant.delay=Math.min(30000,Math.max(1000,dormant.delay*2));dormant.next=clock()+dormant.delay;}
      dormant.running=null;
      return reached;
    })();
    return dormant.running;
  }
  // call runs one helper tool for a host tool that delegates to it, such as
  // oa_image_generate, and returns the helper's structured answer.
  const call=async(name,args,context)=>JSON.parse(await execute(name,args,context));
  // S5 (2026-11-22): the reply-only mark of a chat's current turn, and the
  // live answer through OA whether the person allowed full tool use for it.
  // Kept apart from the chat's helper row, so a retired row keeps its mark.
  function replyOnly(id){return replyOnlyMarks.get(markKey(id))??null;}
  async function peerFullTools(id){
    const row=children.get(id);if(!row||row.ended||!replyOnlyMarks.has(markKey(id)))return false;
    try{
      const api=await row.starting;if(!current(row))return false;
      const page=watchPage(decoded(await api.call('oa_messages_watch',{revision:'',wait_ms:0},{signal:row.abort.signal,timeout:10000})));
      return page?.peerFullTools===true;
    }catch{return false;}
  }
  return {definitions,call,transform,enable(){},reachDormant,reachDormantDue,replyOnly,peerFullTools,
    async suspend(){lifecycle++;const results=await Promise.allSettled([...children.values()].map(retire));const errors=results.filter(value=>value.status==='rejected').map(value=>value.reason);if(errors.length)throw new AggregateError(errors,'Native helpers did not close');},
    rebind(next){if(next.tool)tool=next.tool;if(next.client)client=next.client;if(next.helperProgram)helperProgram=next.helperProgram;},
    event({event}={}){
      if(event?.type==='server.instance.disposed'&&event.properties?.directory===directory)return this.close();
      if(event?.type==='session.deleted'&&event.properties?.info?.directory===directory){lifecycle++;const row=children.get(event.properties.info.id);return row?retire(row):undefined;}
      if(['session.created','session.updated'].includes(event?.type)&&event.properties?.info?.directory===directory&&!event.properties.info.parentID)
        return observe(event.properties.info.id);
      if(event?.type==='session.status'||event?.type==='session.idle'){
        const id=event.properties?.sessionID,row=children.get(id);
        const busy=event.type==='session.status'&&event.properties?.status?.type!=='idle';
        if(row&&!row.ended){
          row.busy=busy;row.live=true;
          if(busy){
            // A running turn reads or reconciles any earlier pointer.
            row.paused=false;row.retried=false;row.retry=false;
            if(row.retryTimer){unschedule(row.retryTimer);row.retryTimer=null;}
            if(!row.turn){
              row.turn=row.started?'woken':'person';row.started=false;
              if(row.turn==='person')replyOnlyMarks.delete(markKey(id));
              if(row.turn==='person'&&waking())void signalTurn(row,'person');
            }
          }else{
            if(row.turn==='woken'&&waking())void signalTurn(row,'ended');
            row.turn='';
            row.messageNotice=null;
            row.askNotice=null;
            if(row.retry)resumePaused(row);
            else void wakeChat(row).then(()=>wakeAsks(row));
          }
        }
        if(event.type==='session.status')return observe(id);
      }
    },async close(){closed=true;lifecycle++;const results=await Promise.allSettled([...children.values()].map(retire));const errors=results.filter(value=>value.status==='rejected').map(value=>value.reason);if(errors.length)throw new AggregateError(errors,'Native helpers did not close');}};
}
