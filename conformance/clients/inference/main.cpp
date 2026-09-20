// An installed C++ application resolves abstraction.inference/chat@1 through
// the facade and calls complete() and stream() over the shared transport. In
// audit mode it resolves operator@1 and prints every retained audit entry.
#include <abstraction/facade/inference.hpp>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <iostream>
#include <stdexcept>
#include <string>
namespace api=abstraction::inference::api;
void check(bool b,const std::string& what){if(!b)throw std::runtime_error("inference consumer assertion failed: "+what);}
api::Request request(const std::string& model,const std::string& text){
 api::Request r;r.model=model;r.guarantees={std::string(api::kRequestGuaranteeLocalOnly)};
 api::Message m;m.role=api::Role::User;api::Part p;p.kind=api::PartKind::Text;p.text=text;m.parts.push_back(p);r.messages.push_back(m);return r;
}
int main(int argc,char**argv){try{
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"inference_consumer <runtime> <runtime without inference> refused|served\ninference_consumer <runtime> audit\n";return 0;}
 if(argc==3&&std::string(argv[2])=="audit"){
  abstraction::facade::ResolutionClient resolver(argv[1],10000);
  auto op=abstraction::facade::resolve_inference_operator(resolver,{},abstraction::facade::Scope::Local);
  std::string out;std::int64_t cursor=0;
  for(;;){
   auto page=op.audit(cursor,256);
   if(page.outcome==api::AuditOutcome::Gap){cursor=page.next;continue;}
   check(page.outcome==api::AuditOutcome::Page,std::string("audit ")+std::string(api::wire_name(page.outcome)));
   for(const auto& e:page.entries)out+="AUDIT\t"+std::to_string(e.sequence)+"\t"+std::string(api::wire_name(e.route))+"\t"+e.outcome+"\t"+e.reason+"\t"+std::to_string(e.tokens_in)+"\t"+std::to_string(e.tokens_out)+"\t"+e.program+"\t"+e.rung+"\n";
   cursor=page.next;
   if(page.at_end||page.entries.empty())break;
  }
  std::fwrite(out.data(),1,out.size(),stdout);
  return 0;
 }
 if(argc!=4)return 2;
 const std::string mode=argv[3];
 abstraction::facade::ResolutionClient absent(argv[2],10000);
 bool refused=false;
 try{abstraction::facade::resolve_inference(absent,{},abstraction::facade::Scope::Local);}catch(const std::exception& e){refused=std::string(e.what()).find("unavailable")!=std::string::npos;if(!refused)std::cerr<<"absence: "<<e.what()<<'\n';}
 check(refused,"a runtime without inference must refuse resolution as unavailable");
 abstraction::facade::ResolutionClient resolver(argv[1],10000);
 auto chat=abstraction::facade::resolve_inference(resolver,{},abstraction::facade::Scope::Local);
 if(mode=="refused"){
  auto reply=chat.complete(request("fixture-chat:1b","hi"));
  check(reply.outcome==api::ReplyOutcome::NotPermitted&&reply.reason=="rights:not_granted",std::string("complete before a rule = ")+std::string(api::wire_name(reply.outcome))+" "+reply.reason);
  std::cout<<"PASS cpp refused: absence=unavailable complete="<<api::wire_name(reply.outcome)<<'\n';
  return 0;
 }
 if(mode!="served")return 2;
 auto reply=chat.complete(request("fixture-chat:1b","hi"));
 check(reply.outcome==api::ReplyOutcome::Completed&&reply.message.parts.size()==1&&reply.message.parts[0].text=="Hello from the fixture runtime"&&reply.host=="ollama"&&reply.usage.input==5,"complete");
 const auto started=std::chrono::steady_clock::now();
 double first=-1;std::string text;bool ended=false;
 chat.stream(request("fixture-chat:1b","hi"),[&](const api::Delta& d){
  if(d.kind==api::DeltaKind::Part&&d.part){if(first<0)first=std::chrono::duration<double,std::milli>(std::chrono::steady_clock::now()-started).count();text+=d.part->text;}
  if(d.kind==api::DeltaKind::End&&d.end)ended=d.end->outcome==api::ReplyOutcome::Completed;
  return true;
 });
 check(ended&&text=="Hello from the fixture runtime","stream");
 bool held=false;
 chat.stream(request("fixture-chat:1b","HOLD"),[&](const api::Delta& d){if(d.kind==api::DeltaKind::Part){held=true;return false;}return true;});
 check(held,"a held stream yields its first part");
 auto denied=chat.complete(request("denied-chat","hi"));
 check(denied.outcome==api::ReplyOutcome::NotPermitted&&denied.reason=="rights:not_granted","denied host");
 char latency[32];std::snprintf(latency,sizeof latency,"%.2f",first);
 std::cout<<"PASS cpp served: complete, stream, cancel, refusal\n"<<"FIRST_TOKEN_MS cpp "<<latency<<'\n';
 return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
