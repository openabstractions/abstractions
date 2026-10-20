import {createRequire} from 'node:module';
import {randomUUID} from 'node:crypto';
import {assistantToolArguments} from './assistant-tools.js';
import {waitForPermissionRequest,permissionObservationQuestionIDs,permissionProjection} from './permission-wait.js';

const require=createRequire(import.meta.url);
export const modelsConnectMetadata=require('./generated/messages-tools.json').host_tools?.find(value=>value.name==='oa_models_connect');

// The host program making model calls owns these native clients. Messages and
// Asks retain their separate kept helper route. No caller identity is an input.
export function createModelsConnect({tool,resolvePermission,resolveRouter}){
  if(!modelsConnectMetadata)throw Error('Generated provider connection definition unavailable');
  const fields=assistantToolArguments(modelsConnectMetadata.inputSchema,tool.schema),validator=tool.schema.object(fields).strict();
  return tool({description:modelsConnectMetadata.description,args:fields,async execute(args,context={}){
    const parsed=validator.safeParse(args??{});if(!parsed.success)return JSON.stringify({outcome:'invalid'});
    const {provider}=parsed.data;
    const binding=modelsConnectMetadata._meta?.['org.openabstractions/provider_bindings']?.[provider];
    if(!binding)return JSON.stringify({outcome:'unavailable',reason:'provider_definition_unavailable'});
    const signal=context.abort;
    const guarantees=modelsConnectMetadata._meta?.['org.openabstractions/required_router_guarantees'];
    if(!Array.isArray(guarantees)||guarantees.length!==1||typeof guarantees[0]!=='string'||!guarantees[0])return JSON.stringify({outcome:'unavailable',reason:'provider_definition_unavailable'});
    let router;
    try{router=await resolveRouter({signal,guarantees});}
    catch{return JSON.stringify({outcome:'unavailable',reason:'provider_setup_unsupported'});}
    let permission;
    try{permission=await resolvePermission({signal});}catch{return JSON.stringify({outcome:'unavailable',reason:'permission_unavailable'});}
    let observed;
    try{observed=await permission.request({requestKey:'models-connect-'+randomUUID(),selection:{permission:{action:'abstraction.credentials/apply',resourceProfile:'abstraction.rights/legacy-exact-string@1'},selector:{kind:'one',target:new TextEncoder().encode('credential:'+binding.Credential)}}});}
    catch{return JSON.stringify({outcome:'unknown',reason:'permission_request_unknown'});}
    const ids=permissionObservationQuestionIDs(observed);
    const waitingClient={observeQuestion:async(...args)=>{const value=await permission.observeQuestion(...args);if(value?.observation)observed=value.observation;return value;}};
    const verdict=await waitForPermissionRequest(observed,{client:waitingClient,signal});
    if(verdict==='blocked'||verdict==='refused')return JSON.stringify({outcome:'declined'});
    if(observed?.outcome==='answered'&&observed.answer?.option==='allow'&&!observed.answer.pending)return JSON.stringify({outcome:'unavailable',reason:'provider_not_set_up'});
    if(verdict!=='allowed'||signal?.aborted)return JSON.stringify(ids.length?{...permissionProjection('waiting',ids),outcome:'pending'}:{outcome:'unavailable',reason:'permission_unconfirmed'});
    try{
      const page=await router.hosts(true);
      const matches=page?.hosts?.filter(host=>host.host===binding.Name&&host.hosted===true&&host.base===binding.Base&&host.wire===binding.Kind&&host.credential===binding.Credential&&host.servable===true);
      return JSON.stringify(matches?.length===1?{outcome:'connected'}:{outcome:'unavailable',reason:'provider_not_set_up'});
    }catch{return JSON.stringify({outcome:'unavailable',reason:'provider_not_set_up'});}
  }});
}
