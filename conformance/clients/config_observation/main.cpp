#include <abstraction/facade/resolution.hpp>
#include <abstraction/config/rec.h>
#include <iostream>
#include <thread>
#include <type_traits>
namespace af=abstraction::facade; namespace cfg=abstraction::config; namespace ipc=abstraction::ipc;
void check(bool b){if(!b)throw std::runtime_error("config generic binding assertion");}
static_assert(cfg::ConfigObserverService::wire_name=="abstraction.config/observer@1");
static_assert(cfg::ConfigReaderService::capability=="abstraction.config");
struct LocalReader:cfg::ConfigReader {
 cfg::Snapshot Read(const cfg::RunOverrides& v) override {cfg::Snapshot s;s.store=v.store;return s;}
};
void alternate_transport() {
 LocalReader handler;cfg::ConfigReaderDispatcher transport(handler);
 af::ServiceReference ref;ref.capability="abstraction.config";ref.contract=std::string(cfg::ConfigReaderService::wire_name);
 ref.provider="test-handler";ref.scope="local";ref.transport="in-process-test";ref.endpoint="test-owned";
 auto bound=af::BindService<cfg::ConfigReaderService>(ref,transport);cfg::RunOverrides v;v.store="typed-dispatch";
 auto moved=std::move(bound);check(moved->Read(v).store==v.store);
 ref.contract="wrong";bool refused=false;try{af::BindService<cfg::ConfigReaderService>(ref,transport);}catch(const af::ResolutionError&){refused=true;}check(refused);
}
int main(int argc,char**argv){try {
 alternate_transport();
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"consumer runtime observe|gap [cursor]\n";return 0;}
 if(argc<3)return 2;
 af::ResolutionClient resolver(argv[1]);auto deadline=ipc::Clock::now()+std::chrono::seconds(5);
 auto observer=af::ResolveService<cfg::ConfigObserverService>(resolver,{},"local",deadline);
 auto moved=std::move(observer); // generated client retains its transport reference
 if(std::string(argv[2])=="gap") {auto v=moved->Observe({},argv[3],0);check(v.outcome=="gap"&&!v.snapshot);return 0;}
 auto reader=af::ResolveService<cfg::ConfigReaderService>(af::ResolutionClient(argv[1],100));
 std::this_thread::sleep_for(std::chrono::milliseconds(150)); // reusable default has a fresh operation budget
 cfg::RunOverrides overrides;overrides.store="caller-run";check(reader->Read(overrides).store=="caller-run");
 auto editor=af::ResolveService<cfg::ConfigEditorService>(resolver,{},"local",deadline);
 auto user=editor->ReadUser();user.values.store="first";check(editor->ReplaceUser(user.revision,user.values).outcome=="applied");
 auto first=moved->Observe({},"",0);check(first.outcome=="snapshot"&&first.snapshot&&first.snapshot->store=="first"&&!first.cursor.empty());
 std::exception_ptr error;std::thread writer([&]{try{std::this_thread::sleep_for(std::chrono::milliseconds(80));auto u=editor->ReadUser();u.values.store="latest";check(editor->ReplaceUser(u.revision,u.values).outcome=="applied");}catch(...){error=std::current_exception();}});
 cfg::ConfigObservation next;try{next=moved->Observe({},first.cursor,1000);}catch(...){writer.join();throw;}writer.join();if(error)std::rethrow_exception(error);
 check(next.outcome=="snapshot"&&next.snapshot&&next.snapshot->store=="latest"&&next.cursor!=first.cursor);
 ipc::CancellationSource signal;auto canceled=af::ResolveService<cfg::ConfigObserverService>(resolver.WithCancellation(signal.Token()));
 std::thread cancel([&]{std::this_thread::sleep_for(std::chrono::milliseconds(30));signal.Cancel();});bool stopped=false;
 try{canceled->Observe({},next.cursor,30000);}catch(const ipc::FrameError&e){stopped=e.status==ipc::Status::cancelled;}cancel.join();check(stopped);
 auto short_call=af::ResolveService<cfg::ConfigObserverService>(resolver,{},"local",ipc::Clock::now()+std::chrono::milliseconds(30));bool timed=false;
 try{short_call->Observe({},next.cursor,30000);}catch(const ipc::FrameError&e){timed=e.status==ipc::Status::timeout;}check(timed);
 check(reader->Read({}).store=="latest");check(moved.Reference().contract==std::string(cfg::ConfigObserverService::wire_name));
 bool refused=false;try{af::ResolveService<cfg::ConfigObserverService>(resolver,{"unavailable-promise"});}catch(const af::ResolutionError&){refused=true;}check(refused);
 std::cout<<next.cursor<<'\n';return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
