// Pure page receiving; no browser, service or model.
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const src=fs.readFileSync(__dirname+'/activity_messages_page.go','utf8');
const marker='const messageActivityScript = `',start=src.indexOf(marker)+marker.length;
const script=src.slice(start,src.indexOf('`',start));
class Node {constructor(){this.children=[];this.textContent='';this.hidden=false;this.attrs={};}append(...v){this.children.push(...v)}replaceChildren(...v){this.children=[...v]}setAttribute(k,v){this.attrs[k]=String(v)}}
const nodes=new Map(),listeners={},timers=new Map();let timerID=0,now=1000,reads=0,held=null;
const el=id=>{if(!nodes.has(id))nodes.set(id,new Node());return nodes.get(id)};
const row={id:'message-1',at:1000,expiresAt:400000,from:'<img src=x>',to:'peer',context:'ctx',technical:{from:'app:internal',context:'ctx'},kind:'question',outcome:'sent'};
class FixtureDate extends Date {static now(){return now}}
const context={Map,Promise,String,encodeURIComponent,console,Date:FixtureDate,el,
 document:{hidden:false,createElement:()=>new Node(),addEventListener:(name,fn)=>listeners[name]=fn},
 window:{addEventListener:(name,fn)=>listeners[name]=fn},
 setTimeout:fn=>{timers.set(++timerID,fn);return timerID},clearTimeout:id=>timers.delete(id),
 oaSetStatus:(id,_state,text)=>el(id).textContent=text,oaShowError:(id,text)=>el(id).textContent=text,
 call:async path=>{if(path.startsWith('/activity/messages?'))return{outcome:'page',events:[row,{...row,outcome:'completed',version:2,updatedAt:241000},{...row,outcome:'submitted',version:1,updatedAt:1001}],atEnd:true};reads++;if(held)return held;return{outcome:'page',id:row.id,headline:'<script>peer</script>',body:'exact\né 9007199254740993',expiresAt:row.expiresAt}}
};
vm.createContext(context);vm.runInContext(script,context);
assert.equal(context.messageDeliveryText({deliveryState:'undelivered',undeliveredReason:'context_closed'}),' · Not delivered: conversation closed');
(async()=>{
 await context.readMessageActivity();assert.equal(reads,0,'history does not fetch body');assert.equal(el('message-events').children.length,1,'canonical ID coalesces repeated events');
 const item=el('message-events').children[0];assert(item.children[0].textContent.includes('<img src=x>'));assert.equal(item.children[0].children.length,0,'peer text creates no elements');
 assert.match(item.children[1].textContent,/answered after 4 min/,'canonical newer state wins over late older metadata');
 vm.runInContext("keepMessageActivity({id:'message-1',version:1,updatedAt:1001,deliveryState:'delivered',deliveryAt:1500});renderMessageActivity()",context);
 assert.match(el('message-events').children[0].children[1].textContent,/answered after 4 min.*Delivered to the inbox; processing unconfirmed/,'delivery evidence preserves the independent answer state');
 vm.runInContext("keepMessageActivity({id:'message-1',version:3,updatedAt:242000,outcome:'completed',at:1000,expiresAt:400000,from:'peer',to:'receiver',kind:'question'});renderMessageActivity()",context);
 assert.match(el('message-events').children[0].children[1].textContent,/Delivered to the inbox; processing unconfirmed/,'later task metadata preserves terminal delivery');
 vm.runInContext("keepMessageActivity({id:'forward',at:1000,expiresAt:400000,metadataOnly:true,from:'Sender',to:'Peer',kind:'note',outcome:'sent',deliveryState:'undelivered',undeliveredReason:'unconfirmed',deliveryAt:1600});renderMessageActivity()",context);
 const forwarded=el('message-events').children.find(n=>n.children[0].textContent.includes('Sender'));
 assert(forwarded);assert.match(forwarded.children[1].textContent,/Delivery was not confirmed/);assert.equal(forwarded.children.length,2,'body-free forwarding metadata offers no Open');
 vm.runInContext("messageEvents.delete('forward');renderMessageActivity()",context);
 assert(!item.children[0].textContent.includes('ctx'),'routing IDs stay in details');assert.equal(item.children[2].children[0].textContent,'Technical details');assert.match(item.children[2].children[1].textContent,/app:internal/);
 assert.equal(item.children[0].attrs['data-evidence'],'claimed');await item.children[3].onclick();assert.equal(reads,1);assert.equal(el('message-body').textContent,'exact\né 9007199254740993');assert.equal(el('message-headline').textContent,'<script>peer</script>');assert.equal(el('message-reader').hidden,false);
 el('message-close').onclick();assert.equal(el('message-body').textContent,'');assert.equal(el('message-reader').hidden,true);assert.equal(el('messages-status').textContent,'Message closed.');await context.openActivityMessage(row.id);
 now=400000;for(const fn of [...timers.values()])fn();assert.equal(el('message-body').textContent,'');assert.equal(el('message-headline').textContent,'');assert.equal(el('message-reader').hidden,true);assert.equal(el('message-events').children[0].children.length,3,'expired row has no Open');
 now=1000;await context.openActivityMessage(row.id);context.document.hidden=true;listeners.visibilitychange();assert.equal(el('message-body').textContent,'');
 context.document.hidden=false;await context.openActivityMessage(row.id);listeners.pagehide();assert.equal(el('message-body').textContent,'');
 for(const outcome of ['forbidden','unavailable']){held=Promise.resolve({outcome});await context.openActivityMessage(row.id);assert.equal(el('message-body').textContent,'');assert.equal(el('message-reader').hidden,true);assert(!/retained|expired|removed/i.test(el('messages-status').textContent),'unknown access must not claim deletion');assert.match(el('messages-status').textContent,outcome==='forbidden'?/refused/:/unavailable/)}
 let resolve;held=new Promise(r=>resolve=r);const pending=context.openActivityMessage(row.id);context.clearMessageBody();resolve({outcome:'page',id:row.id,body:'late body',expiresAt:2000});await pending;assert.equal(el('message-body').textContent,'','retired read cannot restore body');
 vm.runInContext("messageEvents.clear();messageEvents.set('start',{id:'start',at:1000,expiresAt:400000,from:'Sender',to:'Receiver',kind:'question',outcome:'receiver_start_unknown'});renderMessageActivity()",context);
 assert.match(el('message-events').children[0].children[1].textContent,/Receiver start outcome unknown/);assert.equal(el('message-events').children[0].children.length,3,'startup activity has no body read');assert.equal(el('message-events').children[0].children[2].textContent,'Stop this turn');
 vm.runInContext("messageEvents.get('start').outcome='receiver_start_ready';renderMessageActivity()",context);assert.match(el('message-events').children[0].children[1].textContent,/Receiver inbox ready/);
 // VISION 2026-10-31: a woken turn's row names the conversation's folder and revokes its direction through the rights edit.
 const edits=[];context.call=async(path,body)=>{if(path==='/rights?cursor=')return{Outcome:'page',Revision:'r7'};edits.push([path,body]);return{Outcome:'applied'}};
 vm.runInContext("messageEvents.clear();messageEvents.set('woken',{id:'woken',at:1000,expiresAt:400000,from:'Sender',to:'Receiver',kind:'question',outcome:'receiver_start_started',folder:'work',revoke:{account:'acct',program:'remote:'+'a'.repeat(64),action:'abstraction.facade/application.message_start',resource:'app:receiver/start:'+'b'.repeat(64)}});renderMessageActivity()",context);
 const woken=el('message-events').children[0];assert.match(woken.children[1].textContent,/conversation in “work”/);assert.equal(woken.children[3].textContent,'Stop waking conversations');
 await woken.children[3].onclick();assert.equal(edits.length,1);assert.equal(edits[0][0],'/rights');assert.equal(edits[0][1].edit,'revoke');assert.equal(edits[0][1].revision,'r7');assert.equal(edits[0][1].action,'abstraction.facade/application.message_start');assert.match(el('messages-status').textContent,/can no longer wake/);
 vm.runInContext("messageEvents.get('woken').revoke=undefined;renderMessageActivity()",context);assert.equal(el('message-events').children[0].children.length,3,'a row without its permit offers no revoke');
 // VISION 2026-10-31: Activity stops a running woken turn without revoking the direction, and shows a turn another woken turn asked for.
 const stops=[];context.call=async(path,body)=>{stops.push([path,body]);return{outcome:'page',reason:'stopped'}};
 vm.runInContext("messageEvents.clear();messageEvents.set('run:start',{id:'run:start',at:1000,expiresAt:400000,from:'Sender',to:'Receiver',kind:'question',outcome:'receiver_start_ready',origin:'q-person'});renderMessageActivity()",context);
 const running=el('message-events').children[0];assert.match(running.children[1].textContent,/asked by a turn another assistant woke/);assert.equal(running.children[2].textContent,'Stop this turn');
 await running.children[2].onclick();assert.equal(stops.length,1);assert.equal(stops[0][0],'/activity/message/stop?id=run%3Astart');assert.match(el('messages-status').textContent,/stops within two seconds/);
 vm.runInContext("messageEvents.get('run:start').outcome='receiver_start_stopped';renderMessageActivity()",context);assert.match(el('message-events').children[0].children[1].textContent,/Stopped by you/);assert.equal(el('message-events').children[0].children.length,2,'a stopped turn offers no second stop');
 // Review 31: a Codex turn queued into the running Codex app is the app's own, so Activity says so and offers no stop; a headless one says it ran read-only.
 vm.runInContext("messageEvents.get('run:start').outcome='receiver_start_ready';messageEvents.get('run:start').delivery='desktop_queue';renderMessageActivity()",context);assert.match(el('message-events').children[0].children[1].textContent,/running Codex app, which runs this turn itself; OA cannot stop it/);assert.equal(el('message-events').children[0].children.length,2,'a turn the Codex app runs offers no stop');
 vm.runInContext("messageEvents.get('run:start').delivery='read_only';renderMessageActivity()",context);assert.match(el('message-events').children[0].children[1].textContent,/read-only because the Codex app was closed/);assert.equal(el('message-events').children[0].children[2].textContent,'Stop this turn');
 vm.runInContext("messageEvents.get('run:start').outcome='receiver_start_hop_limit';renderMessageActivity()",context);assert.match(el('message-events').children[0].children[1].textContent,/too many assistants in a row/);
 // An unreadable history retires its rows, body and paging controls. A later page clears the old status.
 for(const response of [{outcome:'forbidden'},{outcome:'unavailable'},new Error('offline')]){
  context.call=async()=>{if(response instanceof Error)throw response;return response};
  await context.readMessageActivity();assert.equal(el('message-events').children.length,0);assert.equal(el('messages-more').hidden,true);assert.equal(el('message-reader').hidden,true);
 }
 context.call=async()=>({outcome:'page',events:[{...row,technical:{}}],atEnd:true});await context.readMessageActivity();
 assert.equal(el('messages-status').textContent,'','successful history clears Loading or the previous refusal');
 assert.equal(el('message-events').children[0].children.some(n=>n.children[0]&&n.children[0].textContent==='Technical details'),false,'empty technical data has no disclosure');
 el('messages-status').textContent='This turn was stopped.';await context.readMessageActivity();assert.equal(el('messages-status').textContent,'This turn was stopped.','ordinary reread keeps the action result');
 context.call=async()=>({outcome:'gap'});await context.readMessageActivity();assert.equal(el('messages-more').hidden,true,'a gap retires the old paging cursor');
 console.log('message Activity page PASS');
})().catch(e=>{console.error(e);process.exitCode=1});
