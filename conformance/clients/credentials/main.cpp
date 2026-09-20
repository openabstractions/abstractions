#include <abstraction/facade/credentials.hpp>
#include <iostream>
#include <stdexcept>
#include <string>
namespace api=abstraction::credentials::api;
void check(bool b,const std::string& what){if(!b)throw std::runtime_error("credentials consumer assertion failed: "+what);}
int main(int argc,char**argv){try{
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"credentials_consumer <runtime> denied|listed <account>\n";return 0;}
 if(argc!=4)return 2;
 const std::string mode=argv[2];
 abstraction::facade::ResolutionClient resolver(argv[1],10000);
 auto holder=abstraction::facade::resolve_credentials(resolver,{},abstraction::facade::Scope::Local);
 auto applier=abstraction::facade::resolve_credentials_applier(resolver,{},abstraction::facade::Scope::Local);
 auto page=holder.list("",64);
 api::Use usage;usage.subject.account=argv[3];usage.subject.program=argv[0];usage.consumer="abstraction.download/http-execution@1";usage.name="hf";usage.target="huggingface.co";
 auto applied=applier.apply(usage);
 check(applied.outcome==api::ApplyOutcome::Forbidden&&applied.headers.empty(),std::string("application apply must be forbidden, got ")+std::string(api::wire_name(applied.outcome)));
 if(mode=="denied"){
  check(page.outcome==api::PageOutcome::Forbidden&&page.records.empty(),std::string("list before a holder.read rule = ")+std::string(api::wire_name(page.outcome)));
 }else if(mode=="listed"){
  check(page.outcome==api::PageOutcome::Page&&page.records.size()==1,std::string("list after a holder.read rule = ")+std::string(api::wire_name(page.outcome)));
  const auto& r=page.records[0];
  check(r.name=="hf"&&r.kind=="bearer"&&r.state=="active"&&!r.revision.empty(),"listed metadata");
  std::cout<<"record "<<r.name<<" kind="<<r.kind<<" state="<<r.state<<" store="<<page.limits.secure_store<<'\n';
 }else return 2;
 std::cout<<"PASS "<<mode<<": list="<<api::wire_name(page.outcome)<<" apply="<<api::wire_name(applied.outcome)<<'\n';
 return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
