#include <abstraction/inference/client.hpp>
#include <cassert>
#include <fstream>
#include <filesystem>
#include <stdexcept>
using namespace abstraction::inference;
struct Port {
 int starts=0,cancels=0;bool fail=true;
 api::ChatAdmission start(const api::RequestV3&){++starts;api::ChatAdmission a;a.outcome=api::ChatStartOutcome::Unknown;return a;}
 api::ChatPage observe(const std::string& id,std::int64_t cursor,std::int64_t,std::int64_t,std::int64_t){assert(id=="retained"&&cursor==7);if(fail)throw std::runtime_error("wait deadline/disconnect");api::ChatPage p;p.outcome=api::PageOutcome::Gap;p.next=9;return p;}
 api::ChatCancellation cancel(const std::string&){++cancels;return {};}
};
int main(int argc,char** argv){
 assert(argc==2);std::ifstream input(argv[1]);assert(input);std::vector<api::ChatEvent> events;std::string line;
 std::ifstream policy_input(std::filesystem::path(argv[1]).parent_path()/"chat3_policy_local_only.json");assert(policy_input);
 const std::string policy_bytes((std::istreambuf_iterator<char>(policy_input)),std::istreambuf_iterator<char>());
 const auto policy=api::decode_request_v3(policy_bytes);const auto policy_roundtrip=api::decode_request_v3(api::encode_request_v3(policy));
 for(const auto& r:{policy,policy_roundtrip}){assert(r.target.selection_policy=="private"&&r.target.model.empty());assert(r.content.guarantees==std::vector<std::string>{std::string(api::kChatGuaranteeLocalOnly)}&&r.content.credential.empty());assert(r.identity.key=="privacy-key"&&r.identity.history_epoch=="epoch");}
 while(std::getline(input,line))events.push_back(api::decode_chat_event(line));assert(events.size()==15);
 FoldV3 suffix(false,2);for(std::size_t i=2;i<events.size();++i)suffix.add(events[i]);assert(suffix.reply()->blocks.size()==4&&suffix.reply()->block_ends.size()==5);
 for(auto outcome:{api::ChatResultOutcome::Completed,api::ChatResultOutcome::Failed,api::ChatResultOutcome::Cancelled}){FoldV3 f;auto p=events[1],e=events.back();p.sequence=0;e.sequence=1;e.end->outcome=outcome;if(outcome!=api::ChatResultOutcome::Completed)e.end->finish_reason.reset();f.add(p);f.add(e);assert(f.reply()->result.outcome==outcome&&f.reply()->block_ends.empty());}
 for(bool retain:{false,true}){
  FoldV3 f(retain);for(const auto& e:events){assert(!f.reply());f.add(e);}const auto r=*f.reply();
  assert(r.text=="Hello world"&&r.blocks.size()==5&&r.blocks[3].arguments=="{}"&&r.blocks[4].text=="declined");
  assert(!r.blocks[0].text.empty()==retain&&!r.blocks[1].text.empty()==retain);
  assert(r.blocks[0].seal->origin=="vendor/opaque"&&r.blocks[0].seal->bytes==std::vector<std::uint8_t>({0,255,65}));
  assert(r.block_ends[1].seal_omitted=="encoded_size");
  assert(f.progress->done==0&&!f.progress->total&&r.result.usage.input==0&&!r.result.usage.reasoning);
  assert(r.result.served_by->server=="fixture"&&r.result.finish_reason=="other");
  assert(r.result.timing&&r.result.timing->queue_ms==0&&r.result.timing->load_ms==31&&r.result.timing->prefill_ms==14&&r.result.timing->decode_ms==20&&r.result.timing->reused_tokens==0&&!r.result.timing->restore_ms);
  assert(r.result.timing->load=="future_load"&&r.result.timing->load_source=="future_source");
  assert(events.back().end->finish_reason=="future_vendor_finish");
  bool refused=false;try{f.add(events.back());}catch(const api::ServiceError&){refused=true;}assert(refused);
 }
 FoldV3 f;bool refused=false;try{f.add(events[1]);}catch(const api::ServiceError&){refused=true;}assert(refused);
 for(std::size_t i=0;i<3;++i)f.add(events[i]);auto d=events[1];d.sequence=3;refused=false;try{f.add(d);}catch(const api::ServiceError&){refused=true;}assert(refused);
 Port port;const auto unknown=open_chat_v3(port,api::RequestV3{});assert(unknown.admission->outcome==api::ChatStartOutcome::Unknown&&port.starts==1);
 auto s=resume_chat_v3(port,"retained",7);for(int i=0;i<3;++i){if(i==2)port.fail=false;refused=false;try{s.next();}catch(const std::exception&){refused=true;}assert(refused&&s.cursor==7&&s.fold.cursor==7&&s.request=="retained"&&port.cancels==0&&port.starts==1);}
 s.cancel();assert(port.cancels==1);
}
