#include <abstraction/facade/inventory.hpp>
#include <iostream>
#include <map>
#include <set>
using namespace abstraction;
void require(bool b) { if (!b) throw std::runtime_error("inventory assertion failed"); }
template<class F> void refuses(F f) { bool caught=false; try {f();} catch (...) {caught=true;} require(caught); }
int main(int argc,char** argv) {
 try {
  if(argc!=3) { std::cout << "job_inventory_consumer ENDPOINT pages|denied\n"; return argc==2?0:2; }
  facade::ResolutionClient resolver(argv[1]);
  auto inventory=facade::resolve_job_inventory(resolver);
  auto generic=facade::resolve_service<facade::job_api::JobInventoryService>(resolver);
  if(std::string(argv[2])=="denied") { require(generic->list_work("",2).outcome=="forbidden"); return 0; }
  auto jobs=facade::resolve_jobs(resolver);
  auto acceptance=facade::resolve_service<facade::job_api::RecoverableAcceptanceService>(resolver);
  auto operations=facade::resolve_service<facade::job_api::OperationControlService>(resolver);
  auto history=acceptance->get_history_window();
  std::set<std::string> submitted,seen;
  // Even submissions carry a caller label and odd ones none; ListWork reports each (JOB-A12).
  std::map<std::string,std::string> labels;
  for(int i=0;i<5;i++) {
   facade::job_api::Submission s; s.identity.key=std::to_string(i); s.identity.history_epoch=history.history_epoch;
   s.kind="download"; s.spec={'{','}'}; if(i%2==0) s.label="cpp fixture \xc2\xb7 "+std::to_string(i);
   auto r=acceptance->submit(s);require(r.outcome=="accepted");submitted.insert(r.receipt->operation_id);labels[r.receipt->operation_id]=s.label;
   require(acceptance->reconcile(s.identity).receipt->operation_id==r.receipt->operation_id);
   require(operations->observe_work(s.identity).outcome=="observed");
  }
  auto first=generic->list_work("",2);require(first.outcome=="page" && !first.complete);
  auto labelled=[&](const facade::job_api::OperationSnapshot& s){require(s.label==labels.at(s.receipt.operation_id) && !s.label_derived);};
  for(const auto& s:first.snapshots) {labelled(s);seen.insert(s.receipt.operation_id);}
  auto second=generic->list_work(first.next,2);auto replay=generic->list_work(first.next,2);
  require(second.next==replay.next && second.snapshots.size()==replay.snapshots.size());
  for(std::size_t i=0;i<second.snapshots.size();i++) require(second.snapshots[i].receipt.operation_id==replay.snapshots[i].receipt.operation_id);
  auto page=second;
  for(int count=0;;count++) {
   require(count<20 && page.outcome=="page");
   for(const auto& s:page.snapshots) {labelled(s);require(seen.insert(s.receipt.operation_id).second);}
   if(page.complete) break;
   page=generic->list_work(page.next,2);
  }
  require(seen==submitted);require(generic->list_work(first.next,2).outcome=="gap");
  refuses([&]{inventory.list_work("",0);});refuses([&]{inventory.list_work("",65);});
  ipc::CancellationSource source;source.cancel();
  refuses([&]{inventory.with_cancellation(source.token()).list_work("",2);});
  refuses([&]{inventory.with_deadline(ipc::Clock::now()).list_work("",2);});
  require(inventory.list_work("",2).outcome=="page");
  // Semantic counterexamples use the same production response validator.
  facade::job_api::InventoryPage bad;bad.outcome=abstraction::job::acceptance::InventoryOutcome::Page;bad.next="same";
  refuses([&]{facade::job_detail::validate_inventory(bad,"same",2);});
  bad.complete=true;refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.outcome=abstraction::job::acceptance::InventoryOutcome::Gap;refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.snapshots[0].progress.done=-1;refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.snapshots[0].receipt.identity.key.clear();refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.snapshots.push_back(bad.snapshots.front());refuses([&]{facade::job_detail::validate_inventory(bad,"",64);});
  bad=first;bad.snapshots[0].receipt.identity.key=std::string(513u<<10,'x');facade::job_detail::validate_inventory(bad,"",2);
  bad=first;bad.next=std::string(129,'x');refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.snapshots[0].progress.total=1;bad.snapshots[0].progress.done=2;facade::job_detail::validate_inventory(bad,"",2);
  std::cout << "pages continuation replay gap labels cancellation deadline semantic-refusals passed\n";
  return 0;
 } catch(const std::exception& e) {std::cerr<<e.what()<<'\n';return 1;}
}
