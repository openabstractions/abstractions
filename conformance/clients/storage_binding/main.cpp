#include <abstraction/facade/storage.hpp>
#include <thread>
#include <fstream>
#include <iostream>
#include <iterator>
void check(bool b){if(!b)throw std::runtime_error("storage assertion failed");}
template<class F>void refuses(F f){bool caught=false;try{f();}catch(...){caught=true;}check(caught);}
std::vector<std::uint8_t> file_bytes(const char* path){std::ifstream in(path,std::ios::binary);if(!in)throw std::runtime_error("fixture file");return {std::istreambuf_iterator<char>(in),std::istreambuf_iterator<char>()};}
// Change modes: endpoint digest changes-forbidden | changes-snapshot | changes-await cursor kind | changes-gap cursor.
int changes_mode(abstraction::facade::ResolutionClient& resolver,const std::string& digest,const std::string& mode,int argc,char**argv){
 auto changes=abstraction::facade::resolve_storage_changes(resolver);
 refuses([&]{changes.observe("",0,0);});
 if(mode=="changes-forbidden"){
  auto page=changes.observe("",16,0);check(page.outcome=="forbidden"&&page.changes.empty()&&page.next.empty());
  auto listing=changes.list("",16);check(listing.outcome=="forbidden"&&listing.objects.empty()&&listing.cursor.empty());
  std::cout<<"PASS observe and list forbidden\n";return 0;
 }
 if(mode=="changes-snapshot"){
  std::string continuation,cursor;bool present=false;int pages=0;
  for(;;){auto page=changes.list(continuation,1);check(page.outcome=="page");check(cursor.empty()||page.cursor==cursor);cursor=page.cursor;++pages;
   for(const auto& o:page.objects)present=present||o.digest==digest;if(page.complete)break;continuation=page.continuation;}
  std::cout<<"CURSOR "<<cursor<<" PRESENT "<<(present?"yes":"no")<<" PAGES "<<pages<<'\n';return 0;
 }
 if(argc<5)return 2;const std::string cursor=argv[4];
 if(mode=="changes-gap"){auto page=changes.observe(cursor,16,0);check(page.outcome=="gap"&&page.changes.empty()&&page.next==cursor);std::cout<<"PASS gap\n";return 0;}
 if(mode=="changes-await"){
  if(argc!=6)return 2;const std::string kind=argv[5];std::string next=cursor;
  const auto deadline=abstraction::ipc::Clock::now()+std::chrono::seconds(15);
  while(abstraction::ipc::Clock::now()<deadline){
   auto page=changes.observe(next,16,2000);check(page.outcome=="page");next=page.next;
   for(const auto& c:page.changes)if(c.kind==kind&&c.digest==digest){std::cout<<"NEXT "<<next<<'\n';return 0;}
  }
  throw std::runtime_error("change not observed: "+kind+" "+digest);
 }
 return 2;
}
// Writer modes: endpoint digest mode request|handle [file|size].
int writer_mode(abstraction::facade::ResolutionClient& resolver,abstraction::storage::Client& reader,const std::string& digest,const std::string& mode,int argc,char**argv){
 namespace content=abstraction::storage::content;
 if(mode=="read-missing"){check(reader.open(digest).outcome=="not_found");return 0;}
 if(mode=="read-bytes"){
  if(argc!=5)return 2;const auto expected=file_bytes(argv[4]);auto opened=reader.open(digest);check(opened.outcome=="opened");const auto r=*opened.resource;
  std::vector<std::uint8_t> got;for(std::int64_t offset=0;;){auto page=reader.read(r,offset,65536);check(page.outcome=="data");const auto& c=*page.chunk;got.insert(got.end(),c.data.begin(),c.data.end());offset+=c.data.size();if(c.eof)break;}
  check(got==expected);check(reader.close(r).outcome=="closed");std::cout<<"PASS separate reader exact "<<got.size()<<" bytes\n";return 0;
 }
 if(argc<5)return 2;
 auto writer=abstraction::facade::resolve_storage_writer(resolver);const std::string request=argv[4];
 refuses([&]{writer.begin("short",digest,1);});refuses([&]{writer.begin(request,"sha256:AB",1);});
 check(abstraction::storage::Writer::new_request_id().size()==32);
 if(mode=="write-denied"){check(writer.begin(request,digest,1).outcome=="forbidden");return 0;}
 if(mode=="write-oversized"){if(argc!=6)return 2;const auto size=std::stoll(argv[5]);auto r=writer.begin(request,digest,size);check(r.outcome=="too_large"&&r.limit>0&&r.limit<size);return 0;}
 if(argc!=6)return 2;const auto bytes=file_bytes(argv[5]);const auto size=static_cast<std::int64_t>(bytes.size());
 if(mode=="write-revoked"){
  content::Upload u{request,digest,size,10};std::vector<std::uint8_t> rest(bytes.begin()+10,bytes.end());
  check(writer.append(u,10,rest).outcome=="forbidden");check(writer.commit(u).outcome=="forbidden");check(writer.abort(u).outcome=="aborted");check(writer.abort(u).outcome=="gap");return 0;
 }
 if(mode=="write"){
  // The default writer overload bounds resolution only; the upload starts after that five-second budget.
  std::this_thread::sleep_for(std::chrono::milliseconds(5300));
  auto stored=writer.write(request,digest,bytes);check(stored.evidence=="hashed"&&stored.digest==digest&&stored.size==size);std::cout<<"PASS authorized write committed "<<stored.size<<" bytes\n";return 0;}
 if(mode=="write-duplicate"){auto begun=writer.begin(request,digest,size);check(begun.outcome=="committed"&&begun.stored->evidence=="hashed");check(writer.write(request,digest,bytes).evidence=="hashed");return 0;}
 if(mode=="write-conflict"){bool refused=false;try{writer.write(request,digest,bytes);}catch(const abstraction::storage::WriteOutcome& e){refused=e.operation=="begin"&&e.outcome=="conflict";}check(refused);return 0;}
 if(mode=="write-partial"){
  auto begun=writer.begin(request,digest,size);check(begun.outcome=="started");std::vector<std::uint8_t> head(bytes.begin(),bytes.begin()+10);
  auto appended=writer.append(*begun.upload,0,head);check(appended.outcome=="accepted"&&appended.received==10);
  auto early=writer.commit(*begun.upload);check(early.outcome=="incomplete"&&early.received==10);
  std::vector<std::uint8_t> over(bytes.begin()+10,bytes.end());over.push_back('x');auto beyond=writer.append(*begun.upload,10,over);check(beyond.outcome=="too_large"&&beyond.received==10);
  std::cout<<begun.upload->handle<<'\n';return 0;
 }
 return 2;
}
int main(int argc,char**argv){try{
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"consumer endpoint digest roundtrip|open|foreign|revoked|gap [handle size]\n"
  "consumer endpoint digest read-missing | read-bytes file\n"
  "consumer endpoint digest write-denied request | write-oversized request size | write|write-duplicate|write-conflict|write-partial request file | write-revoked handle file\n";return 0;}
 if(argc<4)return 2;abstraction::facade::ResolutionClient resolver(argv[1]);auto deadline=abstraction::ipc::Clock::now()+std::chrono::seconds(5);auto client=abstraction::facade::resolve_storage(resolver,{},abstraction::facade::Scope::Local,deadline);const std::string digest=argv[2],mode=argv[3];auto generic=abstraction::facade::resolve_service<abstraction::storage::content::ContentReaderService>(resolver,{},abstraction::facade::Scope::Local,deadline);
 if(mode.rfind("changes-",0)==0)return changes_mode(resolver,digest,mode,argc,argv);
 if(mode.rfind("write",0)==0||mode.rfind("read-",0)==0)return writer_mode(resolver,client,digest,mode,argc,argv);
 if(mode=="foreign"||mode=="revoked"||mode=="gap"){
  if(argc!=6)return 2;abstraction::storage::content::Resource r{argv[4],digest,std::stoll(argv[5]),abstraction::storage::content::Verification::Unverified};
  auto result=generic->read(r.handle,0,4);check(result.outcome==(mode=="gap"?"gap":"forbidden"));
  if(mode=="foreign")check(client.close(r).outcome=="forbidden");if(mode=="revoked")check(client.close(r).outcome=="closed");return 0;
 }
 if(mode=="denied"){check(generic->open(digest).outcome=="forbidden");return 0;}
 auto opened=generic->open(digest);check(opened.outcome=="opened"&&opened.resource.has_value());const auto r=*opened.resource;check(r.verification=="unverified"&&r.digest==digest);
 if(mode=="open"){std::cout<<r.handle<<" "<<r.size<<'\n';return 0;}
 std::string bytes;for(std::int64_t offset=0;;){auto result=generic->read(r.handle,offset,4);abstraction::storage::content_detail::read(result,r,offset,4);check(result.outcome=="data");const auto& c=*result.chunk;bytes.append(c.data.begin(),c.data.end());offset+=c.data.size();if(c.eof)break;}
 check(bytes=="content fixture bytes");check(generic->close(r.handle).outcome=="closed");check(generic->read(r.handle,0,4).outcome=="gap");
 abstraction::ipc::CancellationSource scoped;auto bound=abstraction::facade::resolve_storage(resolver.with_cancellation(scoped.token()));scoped.cancel();refuses([&]{bound.open(digest);});
 auto expiring=abstraction::facade::resolve_storage(resolver,{},abstraction::facade::Scope::Local,abstraction::ipc::Clock::now()+std::chrono::milliseconds(200));std::this_thread::sleep_for(std::chrono::milliseconds(220));refuses([&]{expiring.open(digest);});
 // The default overloads bound resolution only; calls after its five-second budget still get a fresh wait.
 auto fresh=abstraction::facade::resolve_storage(resolver);std::this_thread::sleep_for(std::chrono::milliseconds(5300));
 auto late=fresh.open(digest);check(late.outcome=="opened");check(fresh.close(*late.resource).outcome=="closed");
 abstraction::facade::ResolveRequest request;request.capability="abstraction.storage";request.contracts={"abstraction.storage/content-reader@1"};request.scope=abstraction::facade::Scope::Local;auto selected=resolver.resolve(request);selected.reference->contract="forged@1";refuses([&]{abstraction::facade::local_binding(request,selected);});
 abstraction::ipc::CancellationSource cancelled;cancelled.cancel();refuses([&]{client.with_cancellation(cancelled.token()).open(digest);});refuses([&]{client.with_deadline(abstraction::ipc::Clock::now()).open(digest);});
 abstraction::storage::content::ReadResult bad;bad.outcome=abstraction::storage::content::ReadOutcome::Data;refuses([&]{abstraction::storage::content_detail::read(bad,r,0,4);});bad.chunk=abstraction::storage::content::Chunk{0,r.size,{},true};refuses([&]{abstraction::storage::content_detail::read(bad,r,0,4);});
 std::cout<<"PASS bounded bytes, explicit unverified claim, release/gap, cancellation/deadline\n";return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
