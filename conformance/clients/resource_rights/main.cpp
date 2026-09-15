#include <abstraction/facade/client.hpp>
#include <iostream>
#include <map>
void check(bool b,const std::string& what){if(!b)throw std::runtime_error("resource rights assertion failed: "+what);}
template<class F>std::string code_of(F f){
 try{f();return "ok";}
 catch(const abstraction::logging::ServiceError& e){return e.code;}
 catch(const abstraction::router::ServiceError& e){return e.code;}
}
int main(int argc,char**argv){try{
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"resource_rights_consumer <runtime> denied|granted|revoked|outage\n";return 0;}
 if(argc!=3)return 2;
 const std::string mode=argv[2];
 abstraction::facade::Machine machine(argv[1]);
 const auto deadline=abstraction::ipc::Clock::now()+std::chrono::seconds(10);
 auto history=machine.ResolveLogReader({},"local",deadline);
 auto model=machine.ResolveModel({},"local",deadline);
 auto router=machine.ResolveRouter({},"local",deadline);
 abstraction::model::api::Ref ref;ref.registry="fixture";ref.repo="weights";
 abstraction::router::PickRequest pick;pick.model="qwen2.5";
 std::map<std::string,std::string> got{
  {"history",code_of([&]{history.Read("",16,65536);})},
  {"model",model.Resolve(ref).outcome},
  {"inventory",code_of([&]{router.Models();})},
  {"route",code_of([&]{router.Pick(pick);})}};
 std::map<std::string,std::string> want;
 if(mode=="denied"||mode=="revoked")want={{"history","forbidden"},{"model","forbidden"},{"inventory","forbidden"},{"route","forbidden"}};
 else if(mode=="granted")want={{"history","ok"},{"model","resolved"},{"inventory","ok"},{"route","ok"}};
 else if(mode=="outage")want={{"history","policy_unavailable"},{"model","unavailable"},{"inventory","policy_unavailable"},{"route","policy_unavailable"}};
 else return 2;
 for(const auto& [name,value]:want)check(got[name]==value,mode+" "+name+" = "+got[name]);
 std::cout<<"PASS "<<mode<<": history="<<got["history"]<<" model="<<got["model"]<<" inventory="<<got["inventory"]<<" route="<<got["route"]<<'\n';
 return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
