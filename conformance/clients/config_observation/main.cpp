#include <abstraction/facade/resolution.hpp>
#include <abstraction/config/rec.h>
#include <iostream>
#include <thread>
#include <type_traits>
namespace af=abstraction::facade; namespace cfg=abstraction::config; namespace ipc=abstraction::ipc;
void check(bool b){if(!b)throw std::runtime_error("config generic binding assertion");}
static_assert(cfg::ConfigObserverService::kWireName=="abstraction.config/observer@1");
static_assert(cfg::ConfigReaderService::kCapability=="abstraction.config");
struct LocalReader:cfg::ConfigReader {
 cfg::Snapshot read(const cfg::RunOverrides& v) override {cfg::Snapshot s;s.store=v.store;return s;}
};
void alternate_transport() {
 LocalReader handler;cfg::ConfigReaderDispatcher transport(handler);
 af::ServiceReference ref;ref.capability="abstraction.config";ref.contract=std::string(cfg::ConfigReaderService::kWireName);
 ref.provider="test-handler";ref.scope=abstraction::facade::Scope::Local;ref.transport="in-process-test";ref.endpoint="test-owned";
 auto bound=af::bind_service<cfg::ConfigReaderService>(ref,transport);cfg::RunOverrides v;v.store="typed-dispatch";
 auto moved=std::move(bound);check(moved->read(v).store==v.store);
 ref.contract="wrong";bool refused=false;try{af::bind_service<cfg::ConfigReaderService>(ref,transport);}catch(const af::ResolutionError&){refused=true;}check(refused);
}
int main(int argc,char**argv){try {
 alternate_transport();
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"consumer runtime observe|gap [cursor]\nconsumer runtime edit applied|forbidden|unavailable\n";return 0;}
 if(argc<3)return 2;
 af::ResolutionClient resolver(argv[1]);auto deadline=ipc::Clock::now()+std::chrono::seconds(5);
 if(std::string(argv[2])=="edit"){
  if(argc!=4)return 2;const std::string expected=argv[3];
  auto editor=af::resolve_service<cfg::ConfigEditorService>(resolver);
  auto before=editor->read_user();auto values=before.values;values.off["cpp-edit-policy"]=expected;
  auto result=editor->replace_user(before.revision,values);check(result.outcome==expected);
  if(expected=="applied"){check(result.snapshot.values.off.at("cpp-edit-policy")=="applied"&&editor->read_user().revision==result.snapshot.revision);std::cout<<"PASS policy-permitted edit applied\n";return 0;}
  check(result.snapshot.revision.empty()&&result.snapshot.values.off.empty()&&result.snapshot.values.store.empty());
  check(editor->read_user().revision==before.revision);
  std::cout<<"PASS "<<expected<<" edit left settings unchanged\n";return 0;
 }
 auto observer=af::resolve_service<cfg::ConfigObserverService>(resolver,{},abstraction::facade::Scope::Local,deadline);
 auto moved=std::move(observer); // generated client retains its transport reference
 if(std::string(argv[2])=="gap") {auto v=moved->observe({},argv[3],0);check(v.outcome=="gap"&&!v.snapshot);return 0;}
 auto reader=af::resolve_service<cfg::ConfigReaderService>(af::ResolutionClient(argv[1],100));
 std::this_thread::sleep_for(std::chrono::milliseconds(150)); // reusable default has a fresh operation budget
 cfg::RunOverrides overrides;overrides.store="caller-run";check(reader->read(overrides).store=="caller-run");
 auto editor=af::resolve_service<cfg::ConfigEditorService>(resolver,{},abstraction::facade::Scope::Local,deadline);
 auto user=editor->read_user();user.values.store="first";check(editor->replace_user(user.revision,user.values).outcome=="applied");
 auto first=moved->observe({},"",0);check(first.outcome=="snapshot"&&first.snapshot&&first.snapshot->store=="first"&&!first.cursor.empty());
 std::exception_ptr error;std::thread writer([&]{try{std::this_thread::sleep_for(std::chrono::milliseconds(80));auto u=editor->read_user();u.values.store="latest";check(editor->replace_user(u.revision,u.values).outcome=="applied");}catch(...){error=std::current_exception();}});
 cfg::ConfigObservation next;try{next=moved->observe({},first.cursor,1000);}catch(...){writer.join();throw;}writer.join();if(error)std::rethrow_exception(error);
 check(next.outcome=="snapshot"&&next.snapshot&&next.snapshot->store=="latest"&&next.cursor!=first.cursor);
 ipc::CancellationSource signal;auto canceled=af::resolve_service<cfg::ConfigObserverService>(resolver.with_cancellation(signal.token()));
 std::thread cancel([&]{std::this_thread::sleep_for(std::chrono::milliseconds(30));signal.cancel();});bool stopped=false;
 try{canceled->observe({},next.cursor,30000);}catch(const ipc::FrameError&e){stopped=e.status==ipc::Status::Cancelled;}cancel.join();check(stopped);
 auto short_call=af::resolve_service<cfg::ConfigObserverService>(resolver,{},abstraction::facade::Scope::Local,ipc::Clock::now()+std::chrono::milliseconds(30));bool timed=false;
 try{short_call->observe({},next.cursor,30000);}catch(const ipc::FrameError&e){timed=e.status==ipc::Status::Timeout;}check(timed);
 check(reader->read({}).store=="latest");check(moved.reference().contract==std::string(cfg::ConfigObserverService::kWireName));
 bool refused=false;try{af::resolve_service<cfg::ConfigObserverService>(resolver,{"unavailable-promise"});}catch(const af::ResolutionError&){refused=true;}check(refused);
 std::cout<<next.cursor<<'\n';return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
