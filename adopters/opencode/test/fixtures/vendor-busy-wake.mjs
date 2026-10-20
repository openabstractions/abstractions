import {writeFile} from 'node:fs/promises';
import {setTimeout as delay} from 'node:timers/promises';
import {tool} from '@opencode-ai/plugin/tool';
import {createMessages} from '../messages.js';

export default async function busyWakeFixture({client,directory}){
  let busy=null,revision=0,read=false,observed=0;
  const owner=createMessages({tool,client,directory,wake:true,helperProgram:()=> 'inert-watch',
    open:({id})=>({
      async initialize(){},
      async close(){},
      async call(name,args,{signal}){
        const result=value=>({structuredContent:value});
        if(name==='oa_opencode_session_start')return result({outcome:'observed',session_id:id,context:'fixture-inbox'});
        if(name==='oa_messages_watch'){
          if(args.turn)return result({outcome:'page'});
          if(args.reserve_wake)throw Error('the fixture must stay in the person turn');
          observed=Math.max(observed,Number(args.revision)||0);
          while(String(revision)===args.revision)await delay(10,null,{signal});
          return result({outcome:'page',context:'fixture-inbox',revision:String(revision),
            wake:revision&&!read?'00000001':'',unread:revision&&!read?1:0,pending_questions:0});
        }
        if(name==='oa_messages_wait'){
          read=true;revision++;
          return result({outcome:'page',cursor:'fixture-cursor',messages:[]});
        }
        if(name==='oa_messages_inbox_open')return result({outcome:'page'});
        throw Error('unexpected inert inbox call: '+name);
      }
    })});
  const hooks={
    async event(input){
      if(input.event?.type==='session.status'&&input.event.properties.status.type==='busy')busy=input;
      await owner.event(input);
    },
    async 'experimental.chat.messages.transform'(_input,output){owner.transform(output);},
    tool:{
      fixture_deliver:tool({description:'Deliver the local test note during this turn.',args:{},async execute(_args,context){
        await owner.call('oa_messages_inbox_open',{},context);
        const until=Date.now()+5000;
        while(busy?.event.properties.sessionID!==context.sessionID&&Date.now()<until)await delay(10);
        if(busy?.event.properties.sessionID!==context.sessionID)throw Error('no vendor busy event');
        await owner.event(busy);
        revision=1;
        while(observed<1&&Date.now()<until)await delay(10);
        if(observed<1)throw Error('watch did not observe the local delivery');
        return 'Fixture delivery observed.';
      }}),
      oa_messages_wait:owner.definitions.oa_messages_wait,
    },
  };
  await writeFile(new URL('./registered',import.meta.url),'registered');
  return hooks;
}
