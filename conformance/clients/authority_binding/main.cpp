#include <abstraction/facade/asks.hpp>
#include <abstraction/facade/rights.hpp>
#include <abstraction/facade/storage.hpp>
#include <iostream>
#include <thread>
void check(bool b){if(!b)throw std::runtime_error("authority assertion failed");}
template<class F>void refuses(F f){bool refused=false;try{f();}catch(...){refused=true;}check(refused);}
int main(int argc,char**argv){try{
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"consumer runtime questions|rights|operator|rights-operator expected [id-or-revision]\n";return 0;}
 if(argc<4)return 2;
 namespace af=abstraction::facade;namespace ipc=abstraction::ipc;
 af::ResolutionClient resolver(argv[1]);const std::string mode=argv[2],expected=argv[3];
 if(mode=="rights-operator"){
  check(argc==5);const std::string digest=argv[4];
  const char* account=std::getenv("OA_AUTHORITY_ACCOUNT");const char* program=std::getenv("OA_AUTHORITY_PROGRAM");check(account&&program);
  auto op=af::ResolveService<abstraction::rights::api::AuthorizationOperatorService>(resolver);
  auto storage=af::ResolveService<abstraction::storage::content::ContentReaderService>(resolver);
  auto denied=storage->Open(digest);check(denied.outcome=="forbidden"&&!denied.resource);
  auto page=op->ListPolicy("",64);
  abstraction::rights::api::PolicyRule rule{{account,program},"abstraction.storage/content.read",digest,true};
  if(expected=="forbidden"){
   check(page.outcome=="forbidden"&&page.rules.empty()&&page.catalog.empty()&&page.revision.empty());
   auto refused=op->SetRule("unobserved",rule);check(refused.outcome=="forbidden"&&!refused.current&&refused.revision.empty());return 0;
  }
  check(page.outcome=="page"&&page.complete&&page.next.empty()&&page.rules.empty());
  const auto old=page.revision;
  auto grant=op->SetRule(old,rule);check(grant.outcome=="applied"&&grant.current&&grant.current->permit&&!grant.revision.empty());
  auto replay=op->SetRule(old,rule);check(replay.outcome=="conflict"&&replay.current&&replay.current->permit&&replay.revision==grant.revision);
  auto opened=storage->Open(digest);check(opened.outcome=="opened"&&opened.resource&&opened.resource->digest==digest);
  const auto resource=*opened.resource;std::int64_t offset=0;int chunks=0;
  for(;;){auto read=storage->Read(resource.handle,offset,65536);check(read.outcome=="data"&&read.chunk);const auto&chunk=*read.chunk;
   check(chunk.offset==offset&&chunk.total==150000&&chunk.data.size()<=65536);for(auto b:chunk.data)check(b=='x');offset+=chunk.data.size();++chunks;
   check(chunk.eof==(offset==chunk.total));if(chunk.eof)break;check(!chunk.data.empty());}
  check(offset==150000&&chunks==3);
  auto revoke=op->RevokeRule(grant.revision,rule.subject,rule.action,rule.resource);check(revoke.outcome=="applied"&&!revoke.current);
  auto stopped=storage->Read(resource.handle,0,1);check(stopped.outcome=="forbidden"&&!stopped.chunk);
  check(storage->Close(resource.handle).outcome=="closed");
  auto noop=op->RevokeRule(revoke.revision,rule.subject,rule.action,rule.resource);check(noop.outcome=="applied"&&noop.revision==revoke.revision&&!noop.current);
  std::cout<<"PASS generated rights operator conditional grant/revoke with 150000-byte enforced content\n";return 0;
 }
 if(mode=="operator"){
  check(argc==5); const std::string id=argv[4];
  auto op=af::ResolveService<abstraction::asks::api::QuestionOperatorService>(resolver);
  auto page=op->ListQuestions("",1);
  if(expected=="forbidden"){
   check(page.outcome=="forbidden"&&page.records.empty()&&page.next.empty()&&!page.complete);
   auto denied=op->AnswerQuestion(id,"once");check(denied.outcome=="forbidden"&&!denied.record);return 0;
  }
  auto app=af::ResolveService<abstraction::asks::api::QuestionApplicationService>(resolver);
  check(app->Ask({"operator-pagination","download.reach",{{"host","second.example"}}}).outcome=="pending");
  page=op->ListQuestions("",1);check(page.outcome=="page"&&page.records.size()==1&&!page.complete&&!page.next.empty());
  auto next=op->ListQuestions(page.next,1);check(next.outcome=="page"&&next.records.size()==1&&next.complete&&next.next.empty()&&next.records[0].id!=page.records[0].id);
  auto answer=op->AnswerQuestion(id,"once");check(answer.outcome=="answered"&&answer.record&&answer.record->id==id&&answer.record->option=="once"&&!answer.record->answered.empty()&&answer.record->yes&&!answer.record->kept);
  auto replay=op->AnswerQuestion(id,"once");check(replay.outcome=="answered"&&replay.record&&replay.record->answered==answer.record->answered);
  auto conflict=op->AnswerQuestion(id,"never");check(conflict.outcome=="conflict"&&!conflict.record);
  auto unknown=op->AnswerQuestion("unknown-id","once");check(unknown.outcome=="unknown"&&!unknown.record);
  auto gap=op->ListQuestions(page.next,1);check(gap.outcome=="gap"&&gap.records.empty()&&gap.next.empty()&&!gap.complete);
  auto fresh=op->ListQuestions("",64);check(fresh.outcome=="page"&&fresh.records.size()==2&&fresh.complete&&fresh.next.empty());
  std::cout<<"PASS operator policy/page/answer/replay/conflict/gap\n";return 0;
 }
 if(mode=="rights"){
  const char* account=std::getenv("OA_AUTHORITY_ACCOUNT");if(!account)throw std::runtime_error("fixture account required");
  auto c=af::ResolveRights(resolver);auto generic=af::ResolveService<abstraction::rights::api::AuthorizationService>(resolver);auto d=generic->Decide("fixture.read","resource");check(d.outcome==expected&&!d.policy_revision.empty());
  if(argc==5)check(d.policy_revision!=argv[4]);
  auto relay=generic->DecideFor({account,"/invented/program"},"fixture.read","resource");check(relay.outcome=="forbidden"&&relay.policy_revision.empty());
  abstraction::rights::api::Decision bad{"permitted",""};refuses([&]{abstraction::rights::validate(bad);});bad={"unavailable","invented"};refuses([&]{abstraction::rights::validate(bad);});
  ipc::CancellationSource cancel;auto stopped=af::ResolveRights(resolver.WithCancellation(cancel.Token()));cancel.Cancel();refuses([&]{stopped.Decide("fixture.read","resource");});
  std::cout<<d.policy_revision<<'\n';return 0;
 }
 auto c=af::ResolveAsks(resolver);abstraction::asks::api::ApplicationQuestion q{"stable-question","download.reach",{{"host","example.com"}}};
 auto generic=af::ResolveService<abstraction::asks::api::QuestionApplicationService>(resolver);auto result=generic->Ask(q);check(result.outcome==expected);
 if(expected=="gone"){check(!result.answer);check(c.Observe(q.request_key).outcome=="gone");return 0;}
 check(result.answer.has_value());auto id=result.answer->id;if(argc==5)check(id==argv[4]);
 check(generic->Observe("unknown-key",0).outcome=="unknown");q.slots["host"]="different.example";check(c.Ask(q).outcome=="conflict");
 if(expected=="answered"){check(result.answer->option=="once"&&!result.answer->kept&&result.answer->yes);std::cout<<id<<'\n';return 0;}
 auto start=ipc::Clock::now();check(c.Observe("stable-question",20).outcome=="pending");check(ipc::Clock::now()-start<std::chrono::seconds(1));
 ipc::CancellationSource source;auto waiting=af::ResolveAsks(resolver.WithCancellation(source.Token()));std::thread cancel([&]{std::this_thread::sleep_for(std::chrono::milliseconds(50));source.Cancel();});
 bool cancelled=false;try{waiting.Observe("stable-question",30000);}catch(const ipc::FrameError&e){cancelled=e.status==ipc::Status::cancelled;}cancel.join();check(cancelled);
 auto expired=af::ResolveAsks(resolver,{},"local",ipc::Clock::now()+std::chrono::milliseconds(200));std::this_thread::sleep_for(std::chrono::milliseconds(220));refuses([&]{expired.Observe("stable-question");});
 abstraction::asks::api::QuestionObservation bad;bad.outcome="pending";refuses([&]{abstraction::asks::validate(bad);});bad.answer=*result.answer;bad.answer->yes=true;refuses([&]{abstraction::asks::validate(bad);});
 std::cout<<id<<'\n';return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}

