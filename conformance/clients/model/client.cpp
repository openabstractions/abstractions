#include <abstraction/model/client.hpp>
#ifndef MODEL_DIRECT
#include <abstraction/facade/client.hpp>
#include <sstream>
#include <thread>
#endif
#include <iostream>
void require(bool value) {if(!value) throw std::runtime_error("model assertion failed");}
template<class F> void refuses(F f) {bool caught=false;try{f();}catch(...){caught=true;}require(caught);}
int main(int argc,char** argv) {
 try {
  if(argc!=2 || std::string(argv[1])=="--help") {std::cout<<"model_consumer ENDPOINT\n";return 0;}
  const auto deadline=abstraction::ipc::Clock::now()+std::chrono::seconds(12);
#ifdef MODEL_DIRECT
  abstraction::model::Client client(argv[1],deadline);
#else
  abstraction::facade::Machine machine(argv[1]);
  auto client=machine.ResolveModel({},"local",deadline);
#endif
  abstraction::model::api::Ref ref;ref.registry="fixture";ref.repo="weights";
  auto result=client.Resolve(ref);require(result.outcome=="resolved" && result.request.has_value());
  require(result.request->artifact.size==21);
  ref.repo="private";auto privateResult=client.Resolve(ref);require(privateResult.outcome=="unsupported_mapping" && !privateResult.request);
  ref.repo="missing";require(client.Resolve(ref).outcome=="unavailable");
  ref.repo="weights";
  abstraction::ipc::CancellationSource source;source.Cancel();
  refuses([&]{client.WithCancellation(source.Token()).Resolve(ref);});
  refuses([&]{client.WithDeadline(abstraction::ipc::Clock::now()).Resolve(ref);});
  auto bad=result;bad.outcome="forbidden";refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request.reset();refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->artifact.digest="sha256:no";refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->sources[0].locator="http://user:secret@host/file";refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->sources.clear();refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->artifact.size=-1;refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->sources[0].locator="HTTP://host/path with space";abstraction::model::detail::validate(bad);

#ifndef MODEL_DIRECT
  auto jobs=machine.ResolveJobOperations({},"local",deadline);auto history=jobs.GetHistoryWindow();
  abstraction::facade::job_api::Submission submission;submission.identity={"model-download",history.history_epoch};submission.kind="download";
  auto encoded=abstraction::download::request::encode(*result.request);submission.spec.assign(encoded.begin(),encoded.end());
  require(jobs.Submit(submission).outcome=="accepted");
  for(;;) {auto observed=jobs.ObserveWork(submission.identity);require(observed.outcome=="observed");if(observed.snapshot->state=="complete")break;
   require(observed.snapshot->state!="failed" && abstraction::ipc::Clock::now()<deadline);std::this_thread::sleep_for(std::chrono::milliseconds(20));}
  std::ostringstream output;auto copied=jobs.CopyResult(submission.identity,output);if(copied.error)std::rethrow_exception(copied.error);
  require(output.str()=="portable model result" && copied.confirmed==21);
#endif
  std::cout<<"model lookup refusal cancellation validation passed\n";return 0;
 }catch(const std::exception& e){std::cerr<<e.what()<<'\n';return 1;}
}
