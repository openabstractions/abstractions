#include <abstraction/facade/storage.hpp>
#include <thread>
#include <iostream>
void check(bool b){if(!b)throw std::runtime_error("storage assertion failed");}
template<class F>void refuses(F f){bool caught=false;try{f();}catch(...){caught=true;}check(caught);}
int main(int argc,char**argv){try{
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"consumer endpoint digest roundtrip|open|foreign|revoked|gap [handle size]\n";return 0;}
 if(argc<4)return 2;abstraction::facade::ResolutionClient resolver(argv[1]);auto deadline=abstraction::ipc::Clock::now()+std::chrono::seconds(5);auto client=abstraction::facade::ResolveStorage(resolver,{},"local",deadline);const std::string digest=argv[2],mode=argv[3];auto generic=abstraction::facade::ResolveService<abstraction::storage::content::ContentReaderService>(resolver,{},"local",deadline);
 if(mode=="foreign"||mode=="revoked"||mode=="gap"){
  if(argc!=6)return 2;abstraction::storage::content::Resource r{argv[4],digest,std::stoll(argv[5]),"unverified"};
  auto result=generic->Read(r.handle,0,4);check(result.outcome==(mode=="gap"?"gap":"forbidden"));
  if(mode=="foreign")check(client.Close(r).outcome=="forbidden");if(mode=="revoked")check(client.Close(r).outcome=="closed");return 0;
 }
 if(mode=="denied"){check(generic->Open(digest).outcome=="forbidden");return 0;}
 auto opened=generic->Open(digest);check(opened.outcome=="opened"&&opened.resource.has_value());const auto r=*opened.resource;check(r.verification=="unverified"&&r.digest==digest);
 if(mode=="open"){std::cout<<r.handle<<" "<<r.size<<'\n';return 0;}
 std::string bytes;for(std::int64_t offset=0;;){auto result=generic->Read(r.handle,offset,4);abstraction::storage::content_detail::read(result,r,offset,4);check(result.outcome=="data");const auto& c=*result.chunk;bytes.append(c.data.begin(),c.data.end());offset+=c.data.size();if(c.eof)break;}
 check(bytes=="content fixture bytes");check(generic->Close(r.handle).outcome=="closed");check(generic->Read(r.handle,0,4).outcome=="gap");
 abstraction::ipc::CancellationSource scoped;auto bound=abstraction::facade::ResolveStorage(resolver.WithCancellation(scoped.Token()));scoped.Cancel();refuses([&]{bound.Open(digest);});
 auto expiring=abstraction::facade::ResolveStorage(resolver,{},"local",abstraction::ipc::Clock::now()+std::chrono::milliseconds(200));std::this_thread::sleep_for(std::chrono::milliseconds(220));refuses([&]{expiring.Open(digest);});
 abstraction::facade::ResolveRequest request;request.capability="abstraction.storage";request.contracts={"abstraction.storage/content-reader@1"};request.scope="local";auto selected=resolver.Resolve(request);selected.reference->contract="forged@1";refuses([&]{abstraction::facade::local_binding(request,selected);});
 abstraction::ipc::CancellationSource cancelled;cancelled.Cancel();refuses([&]{client.WithCancellation(cancelled.Token()).Open(digest);});refuses([&]{client.WithDeadline(abstraction::ipc::Clock::now()).Open(digest);});
 abstraction::storage::content::ReadResult bad;bad.outcome="data";refuses([&]{abstraction::storage::content_detail::read(bad,r,0,4);});bad.chunk=abstraction::storage::content::Chunk{0,r.size,{},true};refuses([&]{abstraction::storage::content_detail::read(bad,r,0,4);});
 std::cout<<"PASS bounded bytes, explicit unverified claim, release/gap, cancellation/deadline\n";return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
