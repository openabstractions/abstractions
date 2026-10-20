#include <abstraction/model/client.hpp>
#ifndef MODEL_DIRECT
#include <abstraction/facade/client.hpp>
#include <sstream>
#include <thread>
#endif
#include <iostream>
void require_at(bool value,int line) {if(!value) throw std::runtime_error("model assertion failed at line "+std::to_string(line));}
#define require(value) require_at((value),__LINE__)
template<class F> void refuses_at(F f,int line) {bool caught=false;try{f();}catch(...){caught=true;}require_at(caught,line);}
#define refuses(f) refuses_at((f),__LINE__)
struct ModelReplyTransport {
 std::string reply;
 std::string exchange_frame(std::string_view) {return reply;}
};
// Outcome/payload presence belongs to the generated service client. The raw
// record codec permits these records so a malformed peer can be exercised.
void refuses_invalid_reply(const abstraction::model::api::LookupResult& result) {
 namespace api=abstraction::model::api;
 ModelReplyTransport transport{"{\"version\":1,\"service\":\"abstraction.model/resolver@1\",\"method\":\"Resolve\",\"ok\":true,\"payload\":{\"value\":"+api::to_json(result)+"}}"};
 api::ModelResolverClient<ModelReplyTransport> client(transport);
 try {client.resolve(api::Ref{});}
 catch(const api::ServiceError& error) {require(error.code=="invalid_result");return;}
 throw std::runtime_error("generated model client exposed an invalid reply");
}
int main(int argc,char** argv) {
 try {
  if(argc!=2 || std::string(argv[1])=="--help") {std::cout<<"model_consumer ENDPOINT\n";return 0;}
  const auto deadline=abstraction::ipc::Clock::now()+std::chrono::seconds(12);
#ifdef MODEL_DIRECT
  abstraction::model::Client client(argv[1],deadline);
#else
  abstraction::facade::Machine machine(argv[1]);
  auto client=machine.resolve_model({},abstraction::facade::Scope::Local,deadline);
#endif
  abstraction::model::api::Ref ref;ref.registry="fixture";ref.repo="weights";
  auto result=client.resolve(ref);require(result.outcome=="resolved" && result.request.has_value());
  require(result.request->artifact.size==21);
  ref.repo="private";auto privateResult=client.resolve(ref);require(privateResult.outcome=="unsupported_mapping" && !privateResult.request);
  ref.repo="missing";require(client.resolve(ref).outcome=="unavailable");
  ref.repo="weights";
  abstraction::ipc::CancellationSource source;source.cancel();
  refuses([&]{client.with_cancellation(source.token()).resolve(ref);});
  refuses([&]{client.with_deadline(abstraction::ipc::Clock::now()).resolve(ref);});
  auto bad=result;bad.outcome=abstraction::model::api::LookupOutcome::Forbidden;refuses_invalid_reply(bad);
  bad=result;bad.request.reset();refuses_invalid_reply(bad);
  bad=result;bad.request->artifact.digest="sha256:no";refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->sources[0].locator="http://user:secret@host/file";refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->sources.clear();refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->artifact.size=-1;refuses([&]{abstraction::model::detail::validate(bad);});
  bad=result;bad.request->sources[0].locator="HTTP://host/path with space";abstraction::model::detail::validate(bad);

#ifndef MODEL_DIRECT
  auto jobs=machine.resolve_job_operations({},abstraction::facade::Scope::Local,deadline);auto history=jobs.get_history_window();
  abstraction::facade::job_api::Submission submission;submission.identity={"model-download",history.history_epoch};submission.kind="download";
  auto encoded=abstraction::download::request::encode(*result.request);submission.spec.assign(encoded.begin(),encoded.end());
  require(jobs.submit(submission).outcome=="accepted");
  for(;;) {auto observed=jobs.observe_work(submission.identity);require(observed.outcome=="observed");if(observed.snapshot->state=="complete")break;
   require(observed.snapshot->state!="failed" && abstraction::ipc::Clock::now()<deadline);std::this_thread::sleep_for(std::chrono::milliseconds(20));}
  std::ostringstream output;auto copied=jobs.copy_result(submission.identity,output);if(copied.error)std::rethrow_exception(copied.error);
  require(output.str()=="portable model result" && copied.confirmed==21);
#endif
  std::cout<<"model lookup refusal cancellation validation passed\n";return 0;
 }catch(const std::exception& e){std::cerr<<e.what()<<'\n';return 1;}
}
