#include <abstraction/facade/inventory.hpp>
#include <iostream>
#include <set>
using namespace abstraction;
void require(bool b) { if (!b) throw std::runtime_error("inventory assertion failed"); }
template<class F> void refuses(F f) { bool caught=false; try {f();} catch (...) {caught=true;} require(caught); }
int main(int argc,char** argv) {
 try {
  if(argc!=3) { std::cout << "job_inventory_consumer ENDPOINT pages|denied\n"; return argc==2?0:2; }
  facade::ResolutionClient resolver(argv[1]);
  auto inventory=facade::ResolveJobInventory(resolver);
  auto generic=facade::ResolveService<facade::job_api::JobInventoryService>(resolver);
  if(std::string(argv[2])=="denied") { require(generic->ListWork("",2).outcome=="forbidden"); return 0; }
  auto jobs=facade::ResolveJobs(resolver);
  auto acceptance=facade::ResolveService<facade::job_api::RecoverableAcceptanceService>(resolver);
  auto operations=facade::ResolveService<facade::job_api::OperationControlService>(resolver);
  auto history=acceptance->GetHistoryWindow();
  std::set<std::string> submitted,seen;
  for(int i=0;i<5;i++) {
   facade::job_api::Submission s; s.identity.key=std::to_string(i); s.identity.history_epoch=history.history_epoch;
   s.kind="download"; s.spec={'{','}'};
   auto r=acceptance->Submit(s);require(r.outcome=="accepted");submitted.insert(r.receipt->operation_id);
   require(acceptance->Reconcile(s.identity).receipt->operation_id==r.receipt->operation_id);
   require(operations->ObserveWork(s.identity).outcome=="observed");
  }
  auto first=generic->ListWork("",2);require(first.outcome=="page" && !first.complete);
  for(const auto& s:first.snapshots) seen.insert(s.receipt.operation_id);
  auto second=generic->ListWork(first.next,2);auto replay=generic->ListWork(first.next,2);
  require(second.next==replay.next && second.snapshots.size()==replay.snapshots.size());
  for(std::size_t i=0;i<second.snapshots.size();i++) require(second.snapshots[i].receipt.operation_id==replay.snapshots[i].receipt.operation_id);
  auto page=second;
  for(int count=0;;count++) {
   require(count<20 && page.outcome=="page");
   for(const auto& s:page.snapshots) require(seen.insert(s.receipt.operation_id).second);
   if(page.complete) break;
   page=generic->ListWork(page.next,2);
  }
  require(seen==submitted);require(generic->ListWork(first.next,2).outcome=="gap");
  refuses([&]{inventory.ListWork("",0);});refuses([&]{inventory.ListWork("",65);});
  ipc::CancellationSource source;source.Cancel();
  refuses([&]{inventory.WithCancellation(source.Token()).ListWork("",2);});
  refuses([&]{inventory.WithDeadline(ipc::Clock::now()).ListWork("",2);});
  require(inventory.ListWork("",2).outcome=="page");
  // Semantic counterexamples use the same production response validator.
  facade::job_api::InventoryPage bad;bad.outcome="page";bad.next="same";
  refuses([&]{facade::job_detail::validate_inventory(bad,"same",2);});
  bad.complete=true;refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.outcome="gap";refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.snapshots[0].progress.done=-1;refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.snapshots[0].receipt.identity.key.clear();refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.snapshots.push_back(bad.snapshots.front());refuses([&]{facade::job_detail::validate_inventory(bad,"",64);});
  bad=first;bad.snapshots[0].receipt.identity.key=std::string(513u<<10,'x');facade::job_detail::validate_inventory(bad,"",2);
  bad=first;bad.next=std::string(129,'x');refuses([&]{facade::job_detail::validate_inventory(bad,"",2);});
  bad=first;bad.snapshots[0].progress.total=1;bad.snapshots[0].progress.done=2;facade::job_detail::validate_inventory(bad,"",2);
  std::cout << "pages continuation replay gap cancellation deadline semantic-refusals passed\n";
  return 0;
 } catch(const std::exception& e) {std::cerr<<e.what()<<'\n';return 1;}
}
