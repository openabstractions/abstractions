// Inert C ABI recorder: exercises public client headers without native IPC.
// This fixture supplies the ABI itself, including when SDK targets use a DLL.
#undef ABSTRACTION_IPC_SHARED
#include <abstraction/facade/inference.hpp>
#include <cstring>
#include <iostream>
namespace ipc=abstraction::ipc;
namespace facade=abstraction::facade;
struct oa_ipc_cancellation { bool signalled=false; };
struct oa_ipc_reply { std::string bytes; };
struct Call { std::string endpoint; uint32_t budget; oa_ipc_cancellation* cancellation; bool verified; };
std::vector<Call> calls;
extern "C" {
uint32_t oa_ipc_features(){return OA_IPC_FEATURE_XPC;}
oa_ipc_status oa_ipc_cancellation_create(oa_ipc_cancellation** out){*out=new oa_ipc_cancellation;return OA_IPC_OK;}
void oa_ipc_cancellation_signal(oa_ipc_cancellation* token){token->signalled=true;}
void oa_ipc_cancellation_release(oa_ipc_cancellation* token){delete token;}
oa_ipc_status oa_ipc_open(const char*,size_t,uint32_t,oa_ipc_connection**){return OA_IPC_INTERNAL_ERROR;}
oa_ipc_status oa_ipc_open_cancelable(const char*,size_t,uint32_t,oa_ipc_cancellation*,oa_ipc_connection**){return OA_IPC_INTERNAL_ERROR;}
oa_ipc_status oa_ipc_open_verified(const char*,size_t,uint32_t,oa_ipc_cancellation*,const oa_ipc_server_expectation*,oa_ipc_connection**){return OA_IPC_INTERNAL_ERROR;}
oa_ipc_status oa_ipc_write(oa_ipc_connection*,const void*,size_t,size_t*){return OA_IPC_INTERNAL_ERROR;}
oa_ipc_status oa_ipc_read(oa_ipc_connection*,void*,size_t,size_t*){return OA_IPC_INTERNAL_ERROR;}
void oa_ipc_close(oa_ipc_connection*){}
oa_ipc_status oa_ipc_select_runtime(uint32_t,oa_ipc_cancellation*,oa_ipc_runtime_selection**){return OA_IPC_INTERNAL_ERROR;}
const oa_ipc_server_expectation* oa_ipc_selected_server(const oa_ipc_runtime_selection*){return nullptr;}
void oa_ipc_runtime_selection_release(oa_ipc_runtime_selection*){}
oa_ipc_status oa_ipc_activate_runtime(const oa_ipc_server_expectation*,uint32_t,oa_ipc_cancellation*,int32_t*){return OA_IPC_INTERNAL_ERROR;}
oa_ipc_status oa_ipc_session_call(const char* endpoint,size_t length,uint32_t budget,oa_ipc_cancellation* cancellation,const oa_ipc_server_expectation* server,const void* data,size_t size,uint32_t,uint32_t,oa_ipc_reply** reply,size_t* sent){
 calls.push_back({std::string(endpoint,length),budget,cancellation,server&&std::string(server->program,server->program_length)=="/trusted/runtime"});
 *reply=nullptr;*sent=0;
 if(cancellation&&cancellation->signalled)return OA_IPC_CANCELLED;
 if(!budget)return OA_IPC_TIMEOUT;
 const std::string frame(static_cast<const char*>(data),size);
 std::string method,value,service="abstraction.inference/chat@1";
 if(frame.find("\"Resolve\"")!=std::string::npos){
  method="Resolve";service="abstraction.facade/resolver@1";
  value=R"({"status":"resolved","reference":{"provider":"fixture","capability":"abstraction.inference","contract":"abstraction.inference/chat@1","guarantees":[],"scope":"local","transport":"oa-framed-local@1","endpoint":"xpc:selected"}})";
  if(frame.find("abstraction.inference/operator@1")!=std::string::npos)value.replace(value.find("chat@1"),6,"operator@1");
 }else if(frame.find("abstraction.inference/operator@1")!=std::string::npos){
  for(const auto* candidate:{"Hosts","AddHost","RemoveHost","Keys","IssueKey","RevokeKey","Audit"})if(frame.find(std::string("\"")+candidate+"\"")!=std::string::npos)method=candidate;
  *reply=new oa_ipc_reply{"{\"version\":1,\"service\":\"abstraction.inference/operator@1\",\"method\":\""+method+"\",\"ok\":false,\"payload\":{\"code\":\"forbidden\",\"message\":\"inert fixture\"}}"};
  *sent=size;return OA_IPC_OK;
 }else if(frame.find("\"Start\"")!=std::string::npos){method="Start";value=R"({"outcome":"not_permitted","reason":"fixture","operation":"","host":"","model":"","retention_ms":0,"idle_ms":0,"retained_deltas":0})";
 }else if(frame.find("\"Observe\"")!=std::string::npos){method="Observe";value=R"({"outcome":"unknown","deltas":[],"next":0,"at_end":false})";
 }else{method="Cancel";value=R"({"outcome":"unknown"})";}
 *reply=new oa_ipc_reply{"{\"version\":1,\"service\":\""+service+"\",\"method\":\""+method+"\",\"ok\":true,\"payload\":{\"value\":"+value+"}}"};
 *sent=size;return OA_IPC_OK;
}
const unsigned char* oa_ipc_reply_data(const oa_ipc_reply* reply,size_t* size){*size=reply->bytes.size();return reinterpret_cast<const unsigned char*>(reply->bytes.data());}
void oa_ipc_reply_release(oa_ipc_reply* reply){delete reply;}
}
int failures=0;
void check(bool ok,const char* message){if(!ok){std::cerr<<message<<'\n';failures++;}}
void invoke(const abstraction::inference::Chat& chat,int operation){
 if(operation==0)chat.start({});
 else if(operation==1)chat.observe("op",0,1,1024,25000);
 else chat.cancel("op");
}
void invoke_operator(const abstraction::inference::Operator& client,int operation){
 try{
  switch(operation){
   case 0:client.hosts();break;
   case 1:client.add_host("revision",{});break;
   case 2:client.remove_host("revision","fixture");break;
   case 3:client.keys();break;
   case 4:client.issue_key("/fixture/program","");break;
   case 5:client.revoke_key("/fixture/program");break;
   default:client.audit(0,1);break;
  }
  check(false,"operator fixture must refuse without effect");
 }catch(const abstraction::inference::api::ServiceError& error){check(error.code=="forbidden","operator refusal mapping changed");}
}
int main(){try{
 ipc::CancellationSource cancellation;
 facade::ResolutionClient resolver("xpc:resolver");
 resolver=resolver.with_cancellation(cancellation.token()).with_server_expectation(ipc::ServerExpectation{2,"1000","/trusted/runtime"});
 for(int operation=0;operation<3;operation++){
  for(int limit: {2000,120000,0}){
   auto chat=limit?facade::resolve_inference(resolver,{},facade::Scope::Local,ipc::Clock::now()+std::chrono::milliseconds(limit))
                  :facade::resolve_inference(resolver,{},facade::Scope::Local);
   calls.clear();invoke(chat,operation);
   check(calls.size()==1,"one call without retry");
   auto call=calls.back();
   const uint32_t expected=limit==2000?2000:(operation==1?30000:5000);
   check(call.budget<=expected&&call.budget>expected-1000,"caller limit/default wait margin lost");
   check(call.endpoint=="xpc:selected"&&call.cancellation&&call.verified,"retained endpoint/cancellation/trust lost");
  }
 }
 for(int operation=0;operation<7;operation++){
  for(int limit: {2000,120000,0}){
   auto client=limit?facade::resolve_inference_operator(resolver,{},facade::Scope::Local,ipc::Clock::now()+std::chrono::milliseconds(limit))
                    :facade::resolve_inference_operator(resolver,{},facade::Scope::Local);
   calls.clear();invoke_operator(client,operation);
   check(calls.size()==1,"one operator call without retry");
   auto call=calls.back();const uint32_t expected=limit==2000?2000:10000;
   check(call.budget<=expected&&call.budget>expected-1000,"operator caller limit/default margin lost");
   check(call.endpoint=="xpc:selected"&&call.cancellation&&call.verified,"operator binding lost");
  }
 }
#ifdef OA_INFERENCE_WAITING_REPAIR
 // Shared header preserves frame bounds and earliest fixed/relative budget.
 auto transport=ipc::FrameTransport("xpc:selected",uint32_t(5000),16).with_call_deadline(ipc::Clock::now()+std::chrono::seconds(2));
 calls.clear();bool oversized=false;
 try{transport.exchange_frame(std::string(17,'x'));}catch(const ipc::FrameError&){oversized=true;}
 check(oversized&&calls.empty(),"bounded call changed frame limit");
 for(int operation=0;operation<3;operation++){
  auto expired=abstraction::inference::Chat("xpc:selected").with_deadline(ipc::Clock::now()-std::chrono::seconds(1));
  calls.clear();bool refused=false;
  try{invoke(expired,operation);}catch(const ipc::FrameError& error){refused=error.status==ipc::Status::Timeout;}
  check(refused&&calls.empty(),"expired inherited deadline reached transport");
 }
 for(int operation=0;operation<7;operation++){
  auto expired=abstraction::inference::Operator("xpc:selected").with_deadline(ipc::Clock::now()-std::chrono::seconds(1));
  calls.clear();bool refused=false;
  try{invoke_operator(expired,operation);}catch(const ipc::FrameError& error){refused=error.status==ipc::Status::Timeout;}
  check(refused&&calls.empty(),"expired operator deadline reached transport");
 }
#endif
 auto chat=facade::resolve_inference(resolver,{},facade::Scope::Local);
 auto client=facade::resolve_inference_operator(resolver,{},facade::Scope::Local);
 cancellation.cancel();
 for(int operation=0;operation<3;operation++){
  calls.clear();bool refused=false;
  try{invoke(chat,operation);}catch(const ipc::FrameError& error){refused=error.status==ipc::Status::Cancelled;}
  check(refused&&calls.size()==1&&calls[0].cancellation,"cancellation lost or retried");
 }
 for(int operation=0;operation<7;operation++){
  calls.clear();bool refused=false;
  try{invoke_operator(client,operation);}catch(const ipc::FrameError& error){refused=error.status==ipc::Status::Cancelled;}
  check(refused&&calls.size()==1&&calls[0].cancellation,"operator cancellation lost or retried");
 }
 std::cout<<"caller waiting controls: "<<failures<<" failure(s)\n";return failures?1:0;
}catch(const std::exception& error){std::cerr<<error.what()<<'\n';return 2;}}
