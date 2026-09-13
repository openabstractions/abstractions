#include <abstraction/model/client.hpp>
#include <abstraction/facade/resolution.hpp>
#include <iostream>
int main(int argc,char**argv){try{
 if(argc!=2)return 2;
 auto binding=abstraction::facade::ResolveService<abstraction::model::api::ModelResolverService>(abstraction::facade::ResolutionClient(argv[1]));
 abstraction::model::api::Ref ref;ref.registry="fixture";ref.repo="weights";
 auto result=binding->Resolve(ref);abstraction::model::detail::validate(result);
 if(result.outcome!="resolved"||!result.request||result.request->artifact.size!=21)throw std::runtime_error("model generic result");
 auto bytes=abstraction::download::request::encode(*result.request);if(bytes.empty())throw std::runtime_error("missing named codec");
 ref.repo="private";result=binding->Resolve(ref);if(result.outcome!="unsupported_mapping"||result.request)throw std::runtime_error("private mapping exposed");
 ref.repo="missing";if(binding->Resolve(ref).outcome!="unavailable")throw std::runtime_error("absence changed");
 std::cout<<"generic model lookup and named request codec passed\n";
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
