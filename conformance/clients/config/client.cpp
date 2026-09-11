#include <abstraction/config/client.hpp>
#include <iostream>
#include <stdexcept>
using namespace abstraction;
static void require(bool value,const char* why){if(!value)throw std::runtime_error(why);}
struct InvalidArguments {
 ipc::FrameTransport inner;
 explicit InvalidArguments(std::string endpoint):inner(std::move(endpoint)){}
 std::string ExchangeFrame(std::string_view frame){
  std::string bad(frame);auto pos=bad.find("\"overrides\": {");require(pos!=std::string::npos,"missing argument fixture anchor");
  bad.insert(pos+std::string("\"overrides\": {").size(),"\"unexpected\":true,");return inner.ExchangeFrame(bad);
 }
};
int main(int argc,char**argv){
 try{
  require(argc>=2,"mode required");std::string mode=argv[1];config::Client client;
  if(mode=="absent"){try{client.Read();}catch(const ipc::FrameError&){std::cout<<"PASS absent\n";return 0;}throw std::runtime_error("local fallback on absent service");}
  if(mode=="bad-request"){
   InvalidArguments transport(config::default_endpoint());config::ConfigReaderClient<InvalidArguments> generated(transport);
   try{generated.Read(config::RunOverrides{});}catch(const config::ServiceError&e){require(e.code=="unknown_field","wrong refusal");std::cout<<"PASS refusal\n";return 0;}
   throw std::runtime_error("unknown argument accepted");
  }
  auto value=client.Read();require(!value.stamp.empty(),"missing stamp");
  if(mode=="defaults"){
   require(value.store.empty()&&value.nas_store.empty()&&value.log_sink.empty()&&value.log_service.empty()&&value.off.empty(),"nonempty default");
   require(value.origins.store.rung=="default"&&value.origins.store.path.empty(),"bad default provenance");
  }else{
   require(argc==4,"expected value and provider source required");
   require(value.store==argv[2],"wrong store");require(value.nas_store=="provider-nas"&&value.log_sink=="provider-log"&&value.log_service=="provider-service","wrong provider values");
   require(value.off.at("nas")=="maintenance","missing tier refusal reason");
   require(value.origins.nas_store.rung=="user"&&value.origins.nas_store.path==argv[3],"provider source path lost");
   if(mode=="override")require(value.origins.store.rung=="environment"&&value.origins.store.path.empty(),"caller override provenance lost");
   else require(value.origins.store.rung=="user"&&value.origins.store.path==argv[3],"host environment substituted caller environment");
   if(mode=="same-value"){config::RunOverrides run;run.store=value.store;auto changed=client.ReadWithOverrides(run);require(changed.stamp==value.stamp&&changed.origins.store.rung=="environment","stamp follows provenance");}
  }
  std::cout<<"PASS config "<<mode<<"\n";return 0;
 }catch(const std::exception&e){std::cerr<<e.what()<<"\n";return 1;}
}
