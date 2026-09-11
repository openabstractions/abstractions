#include <abstraction/router/client.hpp>
#include <algorithm>
#include <cctype>
#include <iostream>
#include <stdexcept>
namespace r = abstraction::router;
void require(bool condition,const char*message){if(!condition)throw std::runtime_error(message);}
void caller(const r::Observation& observed){
 require(!observed.caller.user_description.empty(),"missing bound user");
 auto path=observed.caller.path_description;std::transform(path.begin(),path.end(),path.begin(),[](unsigned char c){return char(std::tolower(c));});
 require(path.find("router_consumer")!=std::string::npos,"caller is not the C++ process");
}
int main(int argc,char**argv){
 try{
 if(argc!=2)return 2;std::string mode=argv[1];r::Client client;
 if(mode=="absent"){bool refused=false;try{client.Models();}catch(const std::exception&){refused=true;}require(refused,"absent service succeeded");std::cout<<"PASS: absent service, no fallback\n";return 0;}
 auto models=client.Models();caller(models.observation);auto hosts=client.Hosts();caller(hosts.observation);
 if(mode=="empty"){require(models.models.empty()&&hosts.hosts.empty()&&hosts.asked.empty()&&hosts.doubled.empty(),"empty arrays changed");std::cout<<"PASS: empty model and host arrays\n";return 0;}
 require(models.models.size()==2&&hosts.hosts.size()==3,"wrong inventory");
 bool resident=false;for(const auto&f:models.models)for(const auto&a:f.names)if(a.host=="lemonade"&&a.resident&&a.servable)resident=true;require(resident,"resident alias lost");
 bool down=false;for(const auto&h:hosts.hosts)if(h.host=="ollama")down=!h.up&&!h.why.empty()&&h.installed==0&&h.resident.empty();require(down,"host failure was hidden");
 auto refreshed=client.Models(true);require(refreshed.models.size()==models.models.size(),"fresh inventory changed");
 r::PickRequest request;request.model="qwen/qwen3.6-35b-a3b";request.fresh=false;
 auto picked=client.Pick(request);caller(picked.observation);require(picked.decision.verdict=="resident"&&picked.decision.loads==0&&!picked.decision.endpoint.empty()&&!picked.decision.authorised.has_value(),"unrestricted resident choice changed");
 request.allowed.emplace(); // Present [] is NONE, not absent/all.
 auto denied=client.Pick(request);require(denied.decision.verdict=="unauthorised"&&denied.decision.endpoint.empty()&&denied.decision.authorised.has_value()&&denied.decision.authorised->hosts.empty()&&!denied.decision.withheld.empty(),"empty allowance broadened");
 request.allowed->hosts={"lmstudio"};auto cold=client.Pick(request);require(cold.decision.verdict=="would-load"&&cold.decision.loads==1&&cold.decision.host=="lmstudio","permitted fallback changed");
 request.model="";auto empty=client.Pick(request);require(empty.decision.asked.empty()&&empty.decision.verdict=="unparseable"&&empty.decision.endpoint.empty(),"empty model changed");
 auto before=client.Hosts();require(before.asked.size()==4,"audit lost decisions");for(const auto&a:before.asked)require(a.caller==before.observation.caller.path_description&&a.user==before.observation.caller.user_description,"audit not bound to caller");
 abstraction::ipc::FrameTransport transport(r::default_endpoint(),10000,1<<20);
 for(const auto&item:std::vector<std::pair<std::string,std::string>>{
 {R"({"version":1,"service":"abstraction.router/router@1","method":"Missing","arguments":{}})","unknown_method"},
 {R"({"version":1,"service":"abstraction.router/router@1","method":"Pick","arguments":{"request":{"model":"qwen2.5","fresh":"false"}}})","wrong_type"},
 {R"({"version":1,"service":"abstraction.router/router@1","method":"Pick","arguments":{"request":{"model":"qwen2.5","fresh":false,"caller":{"path":"spoof"}}}})","unknown_field"}}){
 auto response=transport.ExchangeFrame(item.first);bool refused=false;auto method=item.second=="unknown_method"?"Missing":"Pick";
 try{r::service_response(response,"abstraction.router/router@1",method);}catch(const r::ServiceError&e){refused=e.code==item.second;}
 require(refused,"wrong typed refusal");}
 require(client.Hosts().asked.size()==before.asked.size(),"malformed or spoofed request reached provider");
 std::cout<<"PASS: Models, Hosts, Pick, caller audit, host failure, empty allowance, typed refusals\n";
 }catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}
}
