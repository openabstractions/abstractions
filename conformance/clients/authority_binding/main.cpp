#include <abstraction/facade/asks.hpp>
#include <abstraction/facade/rights.hpp>
#include <abstraction/facade/storage.hpp>
#include <iostream>
#include <thread>
void check(bool b){if(!b)throw std::runtime_error("authority assertion failed");}
template<class F>void refuses(F f){bool refused=false;try{f();}catch(...){refused=true;}check(refused);}
int main(int argc,char**argv){try{
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"consumer runtime questions|rights|operator|rights-operator|rights-catalogue expected [id-or-revision-or-expiry]\n";return 0;}
 if(argc<4)return 2;
 namespace af=abstraction::facade;namespace ipc=abstraction::ipc;
 af::ResolutionClient resolver(argv[1]);const std::string mode=argv[2],expected=argv[3];
 if(mode=="rights-catalogue"){
  const char* account=std::getenv("OA_AUTHORITY_ACCOUNT");const char* program=std::getenv("OA_AUTHORITY_PROGRAM");check(account&&program);
  namespace ra=abstraction::rights::api;const std::string action="fixture.app/thing.use",resource="thing",why="installed C++ grant";
  auto op=af::resolve_service<ra::AuthorizationOperatorService>(resolver);
  auto decisions=af::resolve_service<ra::AuthorizationService>(resolver);
  const ra::Subject me{account,program};
  if(expected=="register"){
   check(decisions->decide(action,resource).outcome=="unknown_action");
   auto page=op->list_policy("",64);check(page.outcome=="page");
   auto stale=op->register_action("unobserved",action);check(stale.outcome=="conflict"&&stale.revision==page.revision&&!stale.current);
   auto bad=op->register_action(page.revision,"Fixture/Thing");check(bad.outcome=="invalid"&&bad.revision.empty()&&!bad.current);
   auto added=op->register_action(page.revision,action);
   check(added.outcome=="applied"&&added.current&&added.current->action==action&&added.current->registered_by.account==account&&added.current->registered_by.program==program&&!added.current->registered_at.empty());
   auto replay=op->register_action(page.revision,action);check(replay.outcome=="conflict"&&replay.revision==added.revision&&replay.current);
   check(decisions->decide(action,resource).outcome=="not_granted");
   ra::PolicyRule rule{me,action,resource,true};
   auto grant=op->set_rule_for(added.revision,rule,3600000,why);check(grant.outcome=="applied"&&grant.current&&grant.current->permit);
   check(op->set_rule_for(added.revision,rule,3600000,why).outcome=="conflict");
   check(decisions->decide(action,resource).outcome=="permitted");
   auto read=op->read_rule(me,action,resource);check(read.outcome=="found"&&read.record&&read.revision==grant.revision);
   const auto&r=*read.record;check(r.why==why&&r.set_by.account==account&&r.set_by.program==program&&!r.set_at.empty()&&r.expires>r.set_at&&r.rule.permit);
   std::cout<<r.expires<<'\n';return 0;
  }
  check(expected=="expired");
  check(decisions->decide(action,resource).outcome=="not_granted");
  auto read=op->read_rule(me,action,resource);check(read.outcome=="expired"&&read.record&&read.record->rule.permit&&read.record->why==why);
  if(argc==5)check(read.record->expires==argv[4]);
  auto page=op->list_policy("",64);check(page.outcome=="page");
  check(op->retire_action(page.revision,"abstraction.storage/content.read").outcome=="invalid");
  auto retired=op->retire_action(page.revision,action);check(retired.outcome=="applied"&&!retired.current&&retired.revision!=page.revision);
  check(op->retire_action(retired.revision,action).outcome=="unknown");
  check(op->read_rule(me,action,resource).outcome=="unknown");
  check(decisions->decide(action,resource).outcome=="unknown_action");
  std::cout<<"PASS installed C++ rights catalogue register/conflict/enforce/expiry/provenance/retire\n";return 0;
 }
 if(mode=="rights-operator"){
  check(argc==5);const std::string digest=argv[4];
  const char* account=std::getenv("OA_AUTHORITY_ACCOUNT");const char* program=std::getenv("OA_AUTHORITY_PROGRAM");check(account&&program);
  auto op=af::resolve_service<abstraction::rights::api::AuthorizationOperatorService>(resolver);
  auto storage=af::resolve_service<abstraction::storage::content::ContentReaderService>(resolver);
  auto denied=storage->open(digest);check(denied.outcome=="forbidden"&&!denied.resource);
  auto page=op->list_policy("",64);
  abstraction::rights::api::PolicyRule rule{{account,program},"abstraction.storage/content.read",digest,true};
  if(expected=="forbidden"){
   check(page.outcome=="forbidden"&&page.rules.empty()&&page.catalog.empty()&&page.revision.empty());
   auto refused=op->set_rule("unobserved",rule);check(refused.outcome=="forbidden"&&!refused.current&&refused.revision.empty());return 0;
  }
  check(page.outcome=="page"&&page.complete&&page.next.empty()&&page.rules.empty());
  const auto old=page.revision;
  auto grant=op->set_rule(old,rule);check(grant.outcome=="applied"&&grant.current&&grant.current->permit&&!grant.revision.empty());
  auto replay=op->set_rule(old,rule);check(replay.outcome=="conflict"&&replay.current&&replay.current->permit&&replay.revision==grant.revision);
  auto opened=storage->open(digest);check(opened.outcome=="opened"&&opened.resource&&opened.resource->digest==digest);
  const auto resource=*opened.resource;std::int64_t offset=0;int chunks=0;
  for(;;){auto read=storage->read(resource.handle,offset,65536);check(read.outcome=="data"&&read.chunk);const auto&chunk=*read.chunk;
   check(chunk.offset==offset&&chunk.total==150000&&chunk.data.size()<=65536);for(auto b:chunk.data)check(b=='x');offset+=chunk.data.size();++chunks;
   check(chunk.eof==(offset==chunk.total));if(chunk.eof)break;check(!chunk.data.empty());}
  check(offset==150000&&chunks==3);
  auto revoke=op->revoke_rule(grant.revision,rule.subject,rule.action,rule.resource);check(revoke.outcome=="applied"&&!revoke.current);
  auto stopped=storage->read(resource.handle,0,1);check(stopped.outcome=="forbidden"&&!stopped.chunk);
  check(storage->close(resource.handle).outcome=="closed");
  auto noop=op->revoke_rule(revoke.revision,rule.subject,rule.action,rule.resource);check(noop.outcome=="applied"&&noop.revision==revoke.revision&&!noop.current);
  std::cout<<"PASS generated rights operator conditional grant/revoke with 150000-byte enforced content\n";return 0;
 }
 if(mode=="operator"){
  check(argc==5); const std::string id=argv[4];
  auto op=af::resolve_service<abstraction::asks::api::QuestionOperatorService>(resolver);
  auto page=op->list_questions("",1);
  if(expected=="forbidden"){
   check(page.outcome=="forbidden"&&page.records.empty()&&page.next.empty()&&!page.complete);
   auto denied=op->answer_question(id,"once");check(denied.outcome=="forbidden"&&!denied.record);
   auto unretired=op->retire_question(id);check(unretired.outcome=="forbidden"&&!unretired.record);return 0;
  }
  auto app=af::resolve_service<abstraction::asks::api::QuestionApplicationService>(resolver);
  check(app->ask({"operator-pagination","download.reach",{{"host","second.example"}}}).outcome=="pending");
  page=op->list_questions("",1);check(page.outcome=="page"&&page.records.size()==1&&!page.complete&&!page.next.empty());
  auto next=op->list_questions(page.next,1);check(next.outcome=="page"&&next.records.size()==1&&next.complete&&next.next.empty()&&next.records[0].id!=page.records[0].id);
  auto answer=op->answer_question(id,"once");check(answer.outcome=="answered"&&answer.record&&answer.record->id==id&&answer.record->option=="once"&&!answer.record->answered.empty()&&answer.record->yes&&!answer.record->kept);
  auto replay=op->answer_question(id,"once");check(replay.outcome=="answered"&&replay.record&&replay.record->answered==answer.record->answered);
  auto conflict=op->answer_question(id,"never");check(conflict.outcome=="conflict"&&!conflict.record);
  auto unknown=op->answer_question("unknown-id","once");check(unknown.outcome=="unknown"&&!unknown.record);
  auto gap=op->list_questions(page.next,1);check(gap.outcome=="gap"&&gap.records.empty()&&gap.next.empty()&&!gap.complete);
  auto fresh=op->list_questions("",64);check(fresh.outcome=="page"&&fresh.records.size()==2&&fresh.complete&&fresh.next.empty());
  // Retire the pending pagination question; the answered question stays for restart replay.
  const auto other=fresh.records[0].id==id?fresh.records[1].id:fresh.records[0].id;
  auto retired=op->retire_question(other);check(retired.outcome=="retired"&&retired.record&&retired.record->id==other&&retired.record->option.empty());
  auto again=op->retire_question(other);check(again.outcome=="retired"&&!again.record);
  check(app->observe("operator-pagination",0).outcome=="gone");
  check(app->ask({"operator-pagination","download.reach",{{"host","second.example"}}}).outcome=="gone");
  check(op->retire_question("unknown-id").outcome=="unknown");
  check(op->answer_question(other,"once").outcome=="unknown");
  auto remaining=op->list_questions("",64);check(remaining.outcome=="page"&&remaining.records.size()==1&&remaining.records[0].id==id);
  std::cout<<"PASS operator policy/page/answer/replay/conflict/gap/retirement\n";return 0;
 }
 if(mode=="rights"){
  const char* account=std::getenv("OA_AUTHORITY_ACCOUNT");if(!account)throw std::runtime_error("fixture account required");
  auto c=af::resolve_rights(resolver);auto generic=af::resolve_service<abstraction::rights::api::AuthorizationService>(resolver);auto d=generic->decide("fixture.read","resource");check(d.outcome==expected&&!d.policy_revision.empty());
  if(argc==5)check(d.policy_revision!=argv[4]);
  auto relay=generic->decide_for({account,"/invented/program"},"fixture.read","resource");check(relay.outcome=="forbidden"&&relay.policy_revision.empty());
  abstraction::rights::api::Decision bad{abstraction::rights::api::DecisionOutcome::Permitted,""};refuses([&]{abstraction::rights::validate(bad);});bad={abstraction::rights::api::DecisionOutcome::Unavailable,"invented"};refuses([&]{abstraction::rights::validate(bad);});
  ipc::CancellationSource cancel;auto stopped=af::resolve_rights(resolver.with_cancellation(cancel.token()));cancel.cancel();refuses([&]{stopped.decide("fixture.read","resource");});
  std::cout<<d.policy_revision<<'\n';return 0;
 }
 auto c=af::resolve_asks(resolver);abstraction::asks::api::ApplicationQuestion q{"stable-question","download.reach",{{"host","example.com"}}};
 auto generic=af::resolve_service<abstraction::asks::api::QuestionApplicationService>(resolver);auto result=generic->ask(q);check(result.outcome==expected);
 if(expected=="gone"){check(!result.answer);check(c.observe(q.request_key).outcome=="gone");return 0;}
 check(result.answer.has_value());auto id=result.answer->id;if(argc==5)check(id==argv[4]);
 check(generic->observe("unknown-key",0).outcome=="unknown");q.slots["host"]="different.example";check(c.ask(q).outcome=="conflict");
 if(expected=="answered"){check(result.answer->option=="once"&&!result.answer->kept&&result.answer->yes);std::cout<<id<<'\n';return 0;}
 auto start=ipc::Clock::now();check(c.observe("stable-question",20).outcome=="pending");check(ipc::Clock::now()-start<std::chrono::seconds(1));
 ipc::CancellationSource source;auto waiting=af::resolve_asks(resolver.with_cancellation(source.token()));std::thread cancel([&]{std::this_thread::sleep_for(std::chrono::milliseconds(50));source.cancel();});
 bool cancelled=false;try{waiting.observe("stable-question",30000);}catch(const ipc::FrameError&e){cancelled=e.status==ipc::Status::Cancelled;}cancel.join();check(cancelled);
 auto expired=af::resolve_asks(resolver,{},abstraction::facade::Scope::Local,ipc::Clock::now()+std::chrono::milliseconds(200));std::this_thread::sleep_for(std::chrono::milliseconds(220));refuses([&]{expired.observe("stable-question");});
 abstraction::asks::api::QuestionObservation bad;bad.outcome=abstraction::asks::api::ObservationOutcome::Pending;refuses([&]{abstraction::asks::validate(bad);});bad.answer=*result.answer;bad.answer->yes=true;refuses([&]{abstraction::asks::validate(bad);});
 std::cout<<id<<'\n';return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}

