#include <abstraction/facade/logging.hpp>
#include <iostream>
#include <set>
#include <thread>
void check(bool b){if(!b)throw std::runtime_error("observation assertion failed");}
template<class F>void refuses(F f){bool refused=false;try{f();}catch(...){refused=true;}check(refused);}
int main(int argc,char**argv){try{
 if(argc==2&&std::string(argv[1])=="--help"){std::cout<<"consumer runtime observe|unsupported|gap [cursor]\n";return 0;}
 if(argc<3)return 2;namespace af=abstraction::facade;namespace ipc=abstraction::ipc;namespace log=abstraction::logging;
 af::ResolutionClient resolver(argv[1]);const std::string mode=argv[2];
 if(mode=="unsupported"){bool refused=false;try{af::ResolveLogObserver(resolver);}catch(const af::ResolutionError&e){refused=e.status!="resolved";}check(refused);return 0;}
 auto observer=af::ResolveLogObserver(resolver);
 if(mode=="gap"){if(argc!=4)return 2;auto page=observer.Observe(argv[3],3,65536,0);check(page.outcome=="gap"&&page.records.empty()&&page.next==argv[3]&&!page.at_end);return 0;}
 auto history=af::ResolveLogReader(resolver);auto logger=af::ResolveLog(resolver);auto page=history.Read("",3,65536);check(page.outcome=="page"&&page.records.empty()&&page.at_end);auto cursor=page.next;
 auto start=ipc::Clock::now();std::exception_ptr writer_error;std::thread writer([&]{try{std::this_thread::sleep_for(std::chrono::milliseconds(80));logger.Log(1,"arrived");}catch(...){writer_error=std::current_exception();}});
 try{page=observer.Observe(cursor,3,65536,1000);}catch(...){writer.join();throw;}writer.join();if(writer_error)std::rethrow_exception(writer_error);
 check(page.outcome=="page"&&page.records.size()==1&&page.records[0].msg=="arrived");check(ipc::Clock::now()-start>=std::chrono::milliseconds(50));cursor=page.next;
 for(int i=0;i<20;++i)logger.Log(1,"slow-"+std::to_string(i));
 std::set<std::string> seen;for(int calls=0;seen.size()<20&&calls<30;++calls){std::this_thread::sleep_for(std::chrono::milliseconds(10));page=observer.Observe(cursor,3,65536,1000);check(page.outcome=="page"&&page.records.size()<=3);for(const auto&r:page.records)check(seen.insert(r.msg).second);cursor=page.next;}
 check(seen.size()==20);for(int i=0;i<20;++i)check(seen.count("slow-"+std::to_string(i))==1);
 ipc::CancellationSource source;auto canceled=af::ResolveLogObserver(resolver.WithCancellation(source.Token()));std::thread cancel([&]{std::this_thread::sleep_for(std::chrono::milliseconds(50));source.Cancel();});bool stopped=false;start=ipc::Clock::now();try{canceled.Observe(cursor,3,65536,30000);}catch(const ipc::FrameError&e){stopped=e.status==ipc::Status::cancelled;}cancel.join();check(stopped&&ipc::Clock::now()-start<std::chrono::seconds(1));
 check(history.Read(cursor,3,65536).records.empty());logger.Log(1,"after-cancel");page=observer.Observe(cursor,3,65536,1000);check(page.outcome=="page"&&page.records.size()==1&&page.records[0].msg=="after-cancel");cursor=page.next;
 auto shortwait=af::ResolveLogObserver(resolver,{},"local",ipc::Clock::now()+std::chrono::milliseconds(70));bool timed=false;try{shortwait.Observe(cursor,3,65536,30000);}catch(const ipc::FrameError&e){timed=e.status==ipc::Status::timeout;}check(timed);
 log::Page bad;bad.outcome="page";bad.next=cursor;bad.at_end=false;refuses([&]{log::history_detail::validate(bad,cursor,3,65536);});bad.outcome="gap";bad.next="advanced";refuses([&]{log::history_detail::validate(bad,cursor,3,65536);});
 std::cout<<cursor<<'\n';return 0;
}catch(const std::exception&e){std::cerr<<e.what()<<'\n';return 1;}}
