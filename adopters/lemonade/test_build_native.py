"""Native producer fixtures; opt-in owned IPC compilation, no WiX or signing."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import types
import sys
import subprocess
import shutil
import unittest
import zipfile
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from personfolders import setUpModule, tearDownModule  # noqa: F401

SPEC = importlib.util.spec_from_file_location('native_recipe', Path(__file__).with_name('build_native.py'))
recipe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(recipe)


class NativeProducerTest(unittest.TestCase):
    def logging_fixture(self, root):
        """Actual patch adapter and shared handler over fixed, in-memory sinks."""
        compiler = os.environ.get('OA_LEMONADE_CXX')
        if not compiler:
            self.skipTest('selected C++ source-fixture compiler not supplied')
        compiler = Path(compiler)
        self.assertTrue(compiler.is_absolute() and compiler.is_file())
        repository = recipe.HERE.parents[1]
        patch = (recipe.HERE / 'oa-native.patch').read_text(encoding='utf-8')
        section = patch.split('diff --git a/src/cpp/server/openabstractions.cpp ', 1)[1].split('\ndiff --git ', 1)[0]
        actual = '\n'.join(line[1:] for line in section.splitlines() if line.startswith('+') and not line.startswith('+++'))
        adapter = actual[actual.index('std::int64_t oa_level'):actual.index('} // namespace\n\nstd::shared_ptr<AixLog::Sink> make_log_sink', actual.index('class OpenAbstractionsLogSink'))]
        include = root / 'include/abstraction/logging'
        include.mkdir(parents=True)
        # This fixture replaces only the service writer; the policy handler and
        # generated Record are the actual owning source, not a copied algorithm.
        (include / 'client.hpp').write_text(r'''
#pragma once
#include <abstraction/logging/rec.h>
#include <atomic>
#include <functional>
#include <memory>
#include <stdexcept>
#include <thread>
namespace fixed_transport {inline std::atomic<bool> blocked{false},entered{false};inline std::atomic<int> active{0};}
namespace abstraction::ipc {
using CancellationToken=std::shared_ptr<std::atomic<bool>>;
class CancellationSource {CancellationToken token_=std::make_shared<std::atomic<bool>>(false);public:CancellationToken token()const{return token_;}void cancel()const{token_->store(true);}};
enum class Status{Timeout,Cancelled};
class FrameError:public std::runtime_error{public:Status status;FrameError(const char* text,Status s):std::runtime_error(text),status(s){}};
}
namespace abstraction::logging {
class Logger {
 std::function<void(const Record&)> writer_;ipc::CancellationToken token_;
public:
 Logger(std::function<void(const Record&)> writer,ipc::CancellationToken token):writer_(std::move(writer)),token_(std::move(token)){}
 void write(const Record& record)const{
  struct Active{Active(){++fixed_transport::active;}~Active(){--fixed_transport::active;}}active;
  if(fixed_transport::blocked){fixed_transport::entered=true;while(fixed_transport::blocked&&!(token_&&token_->load()))std::this_thread::yield();}
  if(token_&&token_->load())throw ipc::FrameError("cancelled fixed sink",ipc::Status::Cancelled);writer_(record);
 }
 void log(std::int64_t level,const std::string& text,const std::map<std::string,std::string>& attrs)const{Record r;r.level=level;r.msg=text;r.attrs=attrs;write(r);}
};
}
''', encoding='utf-8')
        prelude = r'''
#include <abstraction/logging/async_handler.hpp>
#include <algorithm>
#include <cassert>
#include <condition_variable>
#include <cstdio>
#include <deque>
#include <iostream>
#include <map>
#include <memory>
#include <mutex>
#include <optional>
#include <thread>
namespace logging=abstraction::logging;
namespace fixed {
logging::AsyncHandler::Writer initial;
logging::AsyncHandler::Rebinder rebind;
bool first=true;
std::atomic<int> resolves{0};
}
namespace oa {
class ResolutionClient{public:abstraction::ipc::CancellationToken token;ResolutionClient with_cancellation(abstraction::ipc::CancellationToken value)const{auto copy=*this;copy.token=std::move(value);return copy;}};
logging::Logger resolve_log(const ResolutionClient& resolver){
 ++fixed::resolves;
 auto writer=fixed::first?(fixed::first=false,fixed::initial):fixed::rebind();
 return logging::Logger(std::move(writer),resolver.token);
}
}
oa::ResolutionClient make_resolver(){return {};}
namespace AixLog {
enum class Severity{trace,debug,info,notice,warning,error,fatal};struct Filter{};
struct Tag{const char*text="component";explicit operator bool()const{return text!=nullptr;}};
struct Metadata{Severity severity=Severity::info;Tag tag;};
struct Sink{explicit Sink(const Filter&){}virtual ~Sink()=default;virtual void log(const Metadata&,const std::string&)=0;};
}
'''
        return compiler, repository, adapter, prelude

    def compile_logging_fixture(self, root, compiler, repository, source):
        cpp = root / 'main.cpp'
        cpp.write_text(source, encoding='utf-8')
        output = root / ('fixture.exe' if os.name == 'nt' else 'fixture')
        command = [str(compiler), '-std=c++17', '-O0', '-pthread', '-UNDEBUG',
                   '-I' + str(root / 'include'),
                   '-I' + str(repository / 'openabstractions-flat/abstraction-logging/cpp/include'),
                   '-I' + str(repository / 'openabstractions-flat/abstraction-logging/cpp'),
                   str(cpp), '-o', str(output)]
        subprocess.run(command, check=True, timeout=90)
        return output

    def test_logging_adapter_keeps_the_failed_record(self):
        with tempfile.TemporaryDirectory(prefix='lemonade-log-loss-') as temporary:
            root = Path(temporary)
            compiler, repository, adapter, prelude = self.logging_fixture(root)
            main = r'''
int main(){
 std::mutex mutex;std::condition_variable changed;bool down=true;int attempts=0;std::vector<std::string> delivered;
 fixed::initial=[&](const logging::Record& record){std::lock_guard<std::mutex> lock(mutex);++attempts;changed.notify_all();if(down)throw std::runtime_error("owned outage");delivered.push_back(record.msg);changed.notify_all();};
 fixed::rebind=[&]{return fixed::initial;};
 auto sink=std::make_unique<OpenAbstractionsLogSink>(AixLog::Filter{});
 {std::lock_guard<std::mutex> lock(mutex);sink->log(AixLog::Metadata{},"first");}
 {std::unique_lock<std::mutex> lock(mutex);assert(changed.wait_for(lock,std::chrono::seconds(2),[&]{return attempts>0;}));down=false;}
 sink->log(AixLog::Metadata{},"second");
 {std::unique_lock<std::mutex> lock(mutex);assert(changed.wait_for(lock,std::chrono::seconds(3),[&]{return std::find(delivered.begin(),delivered.end(),"second")!=delivered.end();}));
  assert(std::find(delivered.begin(),delivered.end(),"first")!=delivered.end());}
 assert(fixed::resolves>1);const int rebound=fixed::resolves;
 sink->log(AixLog::Metadata{},"third");
 {std::unique_lock<std::mutex> lock(mutex);assert(changed.wait_for(lock,std::chrono::seconds(2),[&]{return std::find(delivered.begin(),delivered.end(),"third")!=delivered.end();}));}
 assert(fixed::resolves==rebound);
 sink.reset();std::cout<<"PASS actual adapter retains failed record before later record\n";
}
'''
            binary = self.compile_logging_fixture(root, compiler, repository, prelude + adapter + main)
            subprocess.run([str(binary)], check=True, timeout=10)

    def test_embedded_download_fixture_keeps_its_owned_controls(self):
        with tempfile.TemporaryDirectory(prefix='lemonade-download-fixture-') as temporary:
            root = Path(temporary)
            compiler, repository, _, _ = self.logging_fixture(root)
            patch = (recipe.HERE / 'oa-native.patch').read_text(encoding='utf-8')
            for name in ('src/cpp/server/openabstractions.cpp', 'tests/oa_runtime_fixture.py'):
                section = patch.split('diff --git a/' + name + ' ', 1)[1].split('\ndiff --git ', 1)[0]
                body = '\n'.join(line[1:] for line in section.splitlines()
                                 if line.startswith('+') and not line.startswith('+++')) + '\n'
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(body, encoding='utf-8')
            emitted = root / 'download.cpp'
            subprocess.run([sys.executable, '-B', str(root / 'tests/oa_runtime_fixture.py'), str(emitted)],
                           check=True, timeout=10)
            binary = self.compile_logging_fixture(root, compiler, repository, emitted.read_text(encoding='utf-8'))
            subprocess.run([str(binary), str(root / 'owned-downloads')], check=True, timeout=10)

    def test_logging_adapter_shared_outage_and_lifetime(self):
        with tempfile.TemporaryDirectory(prefix='lemonade-log-shared-') as temporary:
            root = Path(temporary)
            compiler, repository, adapter, prelude = self.logging_fixture(root)
            common = repository / 'openabstractions-flat/abstraction-logging/cpp/test/sink_outage.cpp'
            bridge = r'''
class AdapterHandler {
 std::unique_ptr<OpenAbstractionsLogSink> sink_;
public:
 AdapterHandler(logging::AsyncHandler::Writer writer,logging::AsyncHandler::Rebinder rebind,logging::AsyncOptions options){
  fixed::initial=std::move(writer);fixed::rebind=std::move(rebind);fixed::first=true;
  sink_=std::make_unique<OpenAbstractionsLogSink>(AixLog::Filter{},std::move(options));
 }
 void log(std::int64_t,const std::string& message){sink_->log(AixLog::Metadata{},message);}
 logging::HandlerCounts counts()const{return sink_->counts();}
 logging::HandlerCounts close(std::chrono::milliseconds timeout){return sink_->close(timeout);}
};
#define OA_LOGGING_OUTAGE_HANDLER AdapterHandler
#define main shared_outage_main
'''
            suffix = r'''
#undef main
int main(int argc,char**argv){
 const auto result=shared_outage_main(argc,argv);if(result)return result;
 // The actual adapter maps metadata while the fixed receiver captures records.
 std::mutex mutex;std::vector<logging::Record> records;
 fixed::initial=[&](const logging::Record& r){std::lock_guard<std::mutex> lock(mutex);records.push_back(r);};
 fixed::rebind=[] {return fixed::initial;};fixed::first=true;
 auto mapped=std::make_unique<OpenAbstractionsLogSink>(AixLog::Filter{});
 const int resolutions_before=fixed::resolves;
 const AixLog::Severity levels[]={AixLog::Severity::trace,AixLog::Severity::debug,AixLog::Severity::info,AixLog::Severity::notice,AixLog::Severity::warning,AixLog::Severity::error,AixLog::Severity::fatal};
 const std::int64_t expected[]={-8,-4,0,2,4,8,12};
 for(auto level:levels){AixLog::Metadata meta;meta.severity=level;mapped->log(meta,"mapped\r\n");}
 assert(eventually([&]{return mapped->counts().written==7;}));
 assert(fixed::resolves==resolutions_before+1);
 auto counts=mapped->close(std::chrono::seconds(1));assert(counts.accepted==7&&counts.written==7&&counts.queued==0);
 {std::lock_guard<std::mutex> lock(mutex);assert(records.size()==7);for(int i=0;i<7;++i){const auto&r=records[i];assert(r.level==expected[i]&&r.msg=="mapped"&&r.attrs.at("component")=="component"&&r.attrs.at("program")=="lemond"&&r.identity.size()==1&&r.identity[0].program=="lemond");}}
 mapped.reset();
 // Simulate a blocked native write. The publisher holds AixLog's lock;
 // queue admission still returns, and cancellation joins the fixed write.
 fixed::first=true;fixed_transport::entered=false;fixed_transport::blocked=true;
 auto blocked=std::make_unique<OpenAbstractionsLogSink>(AixLog::Filter{});
 std::mutex aixlog_lock;{
  std::lock_guard<std::mutex> lock(aixlog_lock);const auto start=std::chrono::steady_clock::now();
  for(int i=0;i<100;++i)blocked->log(AixLog::Metadata{},"blocked");
  assert(std::chrono::steady_clock::now()-start<std::chrono::seconds(1));
 }
 assert(eventually([]{return fixed_transport::entered.load();}));
 assert(blocked->counts().accepted==100&&blocked->counts().queued==100);
 const auto start=std::chrono::steady_clock::now();blocked.reset();
 assert(std::chrono::steady_clock::now()-start<std::chrono::seconds(2));
 assert(eventually([]{return fixed_transport::active.load()==0;}));fixed_transport::blocked=false;
 std::cout<<"PASS actual adapter metadata, AixLog nonblocking admission and bounded cancelled shutdown\n";
}
'''
            source = prelude + adapter + bridge + '\n#include "' + common.as_posix() + '"\n' + suffix
            binary = self.compile_logging_fixture(root, compiler, repository, source)
            subprocess.run([str(binary), str(repository / 'conformance/faults/sink-outage/sink-outage.json')],
                           check=True, timeout=35)

            # Compile the same adapter against actual public headers as well.
            # This is compile-only; it neither links nor calls native IPC.
            real = root / 'real.cpp'
            aixlog = prelude[prelude.index('namespace AixLog {'):]
            real.write_text('#include <abstraction/facade/logging.hpp>\n'
                            '#include <abstraction/logging/async_handler.hpp>\n'
                            'namespace oa=abstraction::facade;\n'
                            'oa::ResolutionClient make_resolver(){return oa::ResolutionClient("owned-never-dialled");}\n'
                            + aixlog + adapter, encoding='utf-8')
            includes = []
            for module in ('abstraction-logging', 'abstraction-facade', 'abstraction-identity'):
                base = repository / 'openabstractions-flat' / module / 'cpp'
                includes += [base / 'include', base]
            owner = recipe.source_module(repository / 'installer/sdk_smokes/prepare.py')
            selected = owner.msvc_toolchain()
            project = root / 'actual-headers'
            project.mkdir()
            (project / 'CMakeLists.txt').write_text(
                'cmake_minimum_required(VERSION 3.20)\nproject(lemonade_log_headers LANGUAGES CXX)\n'
                'add_library(adapter OBJECT "' + real.as_posix() + '")\n'
                'target_compile_features(adapter PRIVATE cxx_std_17)\n'
                'target_compile_options(adapter PRIVATE /UNDEBUG)\n'
                'target_include_directories(adapter PRIVATE ' + ' '.join('"' + p.as_posix() + '"' for p in includes) + ')\n',
                encoding='utf-8')
            subprocess.run([str(selected['cmake']), '-S', str(project), '-B', str(project / 'build'),
                            '-G', selected['generator'], '-A', 'x64',
                            '-DCMAKE_TRY_COMPILE_TARGET_TYPE=STATIC_LIBRARY',
                            '-DCMAKE_FIND_USE_PACKAGE_REGISTRY=FALSE'], check=True, timeout=60)
            subprocess.run([str(selected['cmake']), '--build', str(project / 'build'), '--config', 'Release',
                            '--target', 'adapter', '--parallel', '1', '--', '/nodeReuse:false'],
                           check=True, timeout=90)

    def test_explicit_runtime_startup_uses_actual_parser_and_native_server_proof(self):
        """Opt-in bounded receiver; uses selected SDK and real owned named pipe."""
        cmake = os.environ.get('OA_LEMONADE_CMAKE')
        selected = os.environ.get('OA_LEMONADE_NATIVE_SOURCE')
        if not cmake or not selected:
            self.skipTest('selected native source/toolchain fixture not supplied')
        source = Path(selected)
        base = Path(os.environ['OA_LEMONADE_BASE_SOURCE'])
        sdk = Path(os.environ['OA_LEMONADE_TRUST_SDK'])
        toolchain = Path(os.environ['OA_LEMONADE_TOOLCHAIN'])
        identity = recipe.HERE.parents[1] / 'openabstractions-flat/abstraction-identity/cpp/test/server_trust/fixture.cpp'
        actual = (source / 'src/cpp/server/openabstractions.cpp').read_text(encoding='utf-8')
        selection = actual[actual.index('struct RuntimeSelection {'):actual.index('struct SavedJob {')]
        # Actual body contains the anonymous-namespace close/open around configure_runtime.
        prelude = r'''
#include <abstraction/facade/resolution.hpp>
#include <lemon/cli_parser.h>
#include <lemon/openabstractions.h>
#include <windows.h>
#include <sddl.h>
#include <algorithm>
#include <filesystem>
#include <mutex>
#include <iostream>
#include <cassert>
namespace lemon::utils {
std::string get_environment_variable_utf8(const std::string& name) {
    const char* value=std::getenv(name.c_str());return value?value:"";
}
std::string get_cache_dir(){return "owned-cache";}
std::string get_config_dir(){return "owned-config";}
}
namespace lemon::openabstractions {
namespace fs=std::filesystem;
namespace oa=abstraction::facade;
namespace {
'''
        main = r'''
} // namespace
} // namespace lemon::openabstractions
extern "C" const char* fixture_begin(int);
extern "C" size_t fixture_finish();
extern "C" const char* fixture_program();
extern "C" const char* fixture_principal();
extern "C" const char* fixture_link_self();
extern "C" const char* fixture_rename(const char*);
extern "C" int fixture_copy_self_to(const char*);
extern "C" void fixture_identity_cleanup();
int main(int argc,char**argv) {
    assert(argc==2);std::string mode=argv[1];
    std::string program=fixture_program(), account=fixture_principal();
    bool peer=mode=="good"||mode=="wrong-program"||mode=="wrong-account"||mode=="changed-program";
    std::string endpoint=peer?fixture_begin(1):"oa-explicit-fixture-absent";
    if(mode=="wrong-program") program="C:/Windows/System32/cmd.exe";
    if(mode=="wrong-account") account="S-1-0-0";
    if(mode=="changed-program")program=fixture_link_self();
    std::vector<std::string> args={"fixture","--host","127.0.0.1","--port","13387"};
    if(mode!="default"&&mode!="ambient") {
        args.insert(args.end(),{"--oa-runtime-endpoint",endpoint});
        if(mode!="partial")args.insert(args.end(),{"--oa-runtime-program",program,"--oa-runtime-account",account});
    }
    if(mode=="invalid-account")args.back()="not-a-SID";
    if(mode=="invalid-program")args[args.size()-3]="relative.exe";
    if(mode=="unc-program")args[args.size()-3]="\\\\no-network.invalid\\share\\server.exe";
    if(mode=="empty")args.back()="";
    if(mode=="control")args.back()="S-1-0-0\n";
    if(mode=="ambient")_putenv_s("LEMONADE_OA_RUNTIME_ENDPOINT","foreign");
    std::vector<char*> raw;for(auto&arg:args)raw.push_back(arg.data());
    bool refused=false;
    try {
        lemon::CLIParser parser;
        const auto status=parser.parse(static_cast<int>(raw.size()),raw.data());
        if(status!=0)refused=true;
    } catch(const std::exception&){refused=true;}
    const bool invalid=mode=="partial"||mode=="invalid-account"||mode=="invalid-program"||mode=="unc-program"||mode=="empty"||mode=="control"||mode=="ambient";
    assert(refused==invalid);
    if(invalid){std::cout<<"PASS startup refusal "<<mode<<"\n";return 0;}
    auto resolver=lemon::openabstractions::make_resolver();
    if(mode=="default") {
        // Do not select/query an installed runtime; default resolver still holds lazy selection.
        auto explicit_copy=resolver.with_server_expectation(abstraction::ipc::ServerExpectation{OA_IPC_PRINCIPAL_WINDOWS_SID,account,program});
        assert(explicit_copy.server()->program==program);
        std::cout<<"PASS unchanged default lazy construction\n";return 0;
    }
    assert(resolver.server()->program==program&&resolver.server()->principal==account);
    auto cancelled=resolver.with_cancellation(abstraction::ipc::CancellationToken{});
    assert(cancelled.server()->program==program&&cancelled.server()->principal==account);
    bool immutable=false;try{lemon::openabstractions::configure_runtime(endpoint,program,account);}catch(const std::exception&){immutable=true;}assert(immutable);
    if(mode=="changed-program") {
        assert(fixture_rename(program.c_str()));assert(fixture_copy_self_to(program.c_str()));
    }
    {
        abstraction::ipc::Stream stream(endpoint,abstraction::ipc::Clock::now()+std::chrono::seconds(2),{},resolver.server());
        if(mode=="good") {
            assert(stream.valid());assert(stream.write_all("ping"));char reply[4];size_t moved=0;assert(stream.read_some(reply,4,moved)&&moved==4);
        } else assert(!stream.valid()&&stream.status()==abstraction::ipc::Status::Untrusted);
    }
    assert(fixture_finish()==(mode=="good"?4:0));fixture_identity_cleanup();
    std::cout<<"PASS actual native peer "<<mode<<"\n";
}
'''
        with tempfile.TemporaryDirectory(prefix='lemonade-trust-') as temporary:
            root = Path(temporary)
            (root / 'main.cpp').write_text(prelude + selection + main, encoding='utf-8')
            paths = [source / 'src/cpp/include', base / 'src/cpp/include',
                     Path(os.environ['OA_LEMONADE_VERSION_INCLUDE']),
                     Path(os.environ['OA_LEMONADE_CLI_INCLUDE'])]
            cmake_source = ('cmake_minimum_required(VERSION 3.20)\nproject(lemonade_trust LANGUAGES CXX)\n'
                'find_package(abstraction_facade_resolution CONFIG REQUIRED)\n'
                'add_executable(trust main.cpp "' + identity.as_posix() + '" "' +
                (source / 'src/cpp/server/cli_parser.cpp').as_posix() + '")\n'
                'target_compile_features(trust PRIVATE cxx_std_17)\n'
                'target_compile_definitions(trust PRIVATE LEMONADE_WITH_OPENABSTRACTIONS=1 LEMONADE_OA_BRANDED_BUILD=1)\n'
                'target_compile_options(trust PRIVATE /UNDEBUG)\n'
                'target_include_directories(trust PRIVATE ' + ' '.join('"' + p.as_posix() + '"' for p in paths) + ')\n'
                'target_link_libraries(trust PRIVATE abstraction::facade_resolution advapi32)\n')
            (root / 'CMakeLists.txt').write_text(cmake_source, encoding='utf-8')
            subprocess.run([cmake, '-S', str(root), '-B', str(root / 'build'), '-G', 'Visual Studio 18 2026',
                '-A', 'x64', '-DCMAKE_TOOLCHAIN_FILE=' + str(toolchain), '-DCMAKE_PREFIX_PATH=' + str(sdk),
                '-DCMAKE_FIND_USE_SYSTEM_ENVIRONMENT_PATH=FALSE', '-DCMAKE_FIND_USE_PACKAGE_REGISTRY=FALSE'],check=True,timeout=90)
            subprocess.run([cmake,'--build',str(root / 'build'),'--config','Release','--parallel','1','--','/nodeReuse:false'],check=True,timeout=120)
            for mode in ('good','wrong-program','wrong-account','changed-program','partial','invalid-account',
                         'invalid-program','unc-program','empty','control','ambient','default'):
                result=subprocess.run([str(root/'build/Release/trust.exe'),mode],capture_output=True,text=True,timeout=10)
                print(result.stdout, end='')
                self.assertEqual(result.returncode,0,result.stdout+result.stderr)

    def test_regular_keeps_hardlink_read_policy_and_checks_directory_ancestry(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);inside=root/'inside';inside.mkdir()
            source=self.put(inside,'data',b'read-only input');outside=root/'outside'
            os.link(source,outside)
            self.assertEqual(recipe.regular(source,[inside]),source.resolve())
            with self.assertRaisesRegex(ValueError,'outside'):
                recipe.regular(outside,[inside])
            with mock.patch.object(recipe._boundary.os.path,'samefile',side_effect=PermissionError('denied')):
                with self.assertRaises(PermissionError):recipe.regular(source,[inside])

    def test_npm_cache_and_source_helper_are_bound_without_ambient_imports(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);inputs=root/'inputs';cache=inputs/'npm';cache.mkdir(parents=True)
            self.put(cache,'entry',b'cache');destination=root/'copied'
            recipe.npm_cache(cache,inputs,{'build_inputs':{'npm/entry':recipe.pin(cache/'entry')}},destination)
            self.assertEqual((destination/'entry').read_bytes(),b'cache')
            with mock.patch.object(recipe.BOUNDARY_PATH.__class__,'read_bytes',return_value=b'changed'):
                with self.assertRaisesRegex(ValueError,'helper changed'):recipe.producer_sources()
            script=root/'build_native.py';shutil.copyfile(recipe.HERE/'build_native.py',script)
            spec=importlib.util.spec_from_file_location('isolated_missing_helper',script)
            module=importlib.util.module_from_spec(spec)
            with self.assertRaisesRegex(ValueError,'plain source'):
                spec.loader.exec_module(module)
            shutil.copyfile(recipe.BOUNDARY_PATH,root/'file_boundary.py')
            spec.loader.exec_module(module)
            self.assertEqual(module.regular(cache/'entry',[inputs]),(cache/'entry').resolve())

    def put(self, root, name, data=b'fixture'):
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def package_wix_fixture(self, path, sources_root, *, shim=b'fixture apphost '):
        store = path.parent / '.store/wix/5.0.2/wix/5.0.2'
        payload = {'wix.dll': b'fixture managed WiX 5.0.2', 'wix.runtimeconfig.json': b'{}'}
        package = store / 'wix.5.0.2.nupkg'
        package.parent.mkdir(parents=True, exist_ok=True)
        with zipfile.ZipFile(package, 'w') as archive:
            for name, data in payload.items():
                archive.writestr('tools/net6.0/any/' + name, data)
                self.put(store, 'tools/net6.0/any/' + name, data)
        policy = {'wix': dict(file=package.name, **recipe.pin(package))}
        repository = Path(__file__).resolve().parents[2]
        selected = os.environ.get('OA_LEMONADE_WIX_PACKAGES_OWNER')
        candidates = [Path(selected)] if selected else [repository / 'installer/wix_packages.py',
                                                       repository.parent / 'redist/windows/wix_packages.py']
        owner = next((path for path in candidates if path.is_file()), None)
        if owner is None:
            raise FileNotFoundError('current shared wix_packages.py required; pair the Redist source or set OA_LEMONADE_WIX_PACKAGES_OWNER')
        self.put(sources_root, 'redist/windows/wix_packages.py', owner.read_bytes()
                 + ('\nPACKAGES=' + repr(policy) + '\n').encode())
        path.write_bytes(shim + b'.store\\wix\\5.0.2\\wix\\5.0.2\\tools\\net6.0\\any\\wix.dll\0')

    def package_tool_version(self, command, **kwargs):
        path = Path(command[0])
        data = path.read_bytes()
        if data.startswith(b'git'):
            return 'git version 2.56.0.windows.4\n'
        if data.startswith(b'python'):
            return 'Python 3.12.10\n'
        if b'.store\\wix\\5.0.2\\wix\\5.0.2\\tools\\net6.0\\any\\wix.dll\0' in data:
            return '5.0.2+fixture\n'
        return 'wrong tool identity\n'

    def test_materialization_acquisition_names_and_role_join_have_one_source(self):
        manifest = recipe.read_json(recipe.HERE / 'native-inputs.json')
        rows = recipe.local_source_inputs()
        self.assertEqual(rows[0], ('upstream', manifest['upstream']['url'], manifest['upstream']['commit']))
        self.assertEqual(rows[1:], [(row['name'], row['url'], row['commit']) for row in manifest['native_dependencies']])
        supplier = {'id': 'WixUiCa_X64', 'sha256': 'a' * 64, 'bytes': 3}
        with mock.patch.object(recipe, 'ui_rows', return_value=[supplier]):
            own, external, names = recipe.source_roles(Path('sources'), Path('package'), Path('extension'))
        self.assertEqual(own, tuple(('File', name, 'Release/' + name) for name in recipe.OWN_PE))
        self.assertEqual(external, (('Binary', 'WixUiCa_X64', {'sha256': 'a' * 64, 'bytes': 3}),))
        self.assertEqual(names, ())

    def test_materialize_refuses_relative_tool_before_any_dispatch(self):
        args = types.SimpleNamespace(**{name: Path('ambient-tool') for name in ('git', 'python', 'node', 'npm', 'wix')})
        with mock.patch.object(recipe.subprocess, 'run') as run, \
             mock.patch.object(recipe.subprocess, 'check_output') as output, \
             mock.patch.object(recipe, 'source_module') as owner:
            with self.assertRaisesRegex(ValueError, 'explicit absolute'):
                recipe.materialize(args)
        run.assert_not_called()
        output.assert_not_called()
        owner.assert_not_called()

    def test_cmake_instance_literal_preserves_unicode_dollar_and_delimiters(self):
        cmake = os.environ.get('OA_LEMONADE_CMAKE')
        if not cmake:
            self.skipTest('selected metadata-only CMake not supplied')
        value = 'C:/VS/éλληνικά/${variable}/name]=]tail'
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            script = root / 'literal.cmake'
            output = root / 'value.txt'
            script.write_text('set(CMAKE_GENERATOR_INSTANCE ' + recipe.cmake_literal(value) + ')\n'
                              'file(WRITE ' + recipe.cmake_literal(output.as_posix()) + ' "${CMAKE_GENERATOR_INSTANCE}")\n', encoding='utf-8')
            subprocess.run([cmake, '-P', str(script)], check=True)
            self.assertEqual(output.read_text(encoding='utf-8'), value)

    def test_materialize_missing_cache_or_ui_refuses_before_source_reconstruction(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = types.SimpleNamespace(
                **{name: self.put(root, 'tools/' + name) for name in ('git', 'python', 'node', 'npm', 'wix')},
                npm_cache=root / 'cache', sources_root=root / 'sources',
                wix_ui_package=root / 'ui.nupkg', wix_ui_extension=root / 'ui.dll',
                inputs_root=root / 'inputs')
            with mock.patch.object(recipe, 'environment', side_effect=ValueError('missing OS tool role')):
                with self.assertRaisesRegex(ValueError, 'missing OS tool role'):
                    recipe.materialize(args)
            self.assertFalse(args.inputs_root.exists())
            with mock.patch.object(recipe, 'local_checkout') as checkout, \
                 mock.patch.object(recipe.subprocess, 'run') as run, \
                 mock.patch.object(recipe.subprocess, 'check_output') as output:
                with self.assertRaisesRegex(ValueError, 'offline npm cache'):
                    recipe.materialize(args)
                self.put(args.npm_cache, 'entry')
                with mock.patch.object(recipe, 'ui_rows', side_effect=ValueError('pinned UI differs')):
                    with self.assertRaisesRegex(ValueError, 'pinned UI differs'):
                        recipe.materialize(args)
            checkout.assert_not_called()
            run.assert_not_called()
            output.assert_not_called()
            self.assertFalse(args.inputs_root.exists())

    def test_projected_git_owner_blocks_foreign_index_and_url_rewrite(self):
        checkout = Path(__file__).resolve().parents[2]
        manifest = checkout / 'scripts/split.manifest'
        if manifest.is_file():
            section = manifest.read_text(encoding='utf-8').split('repo redist\n', 1)[1].split('\nrepo ', 1)[0]
            row = next(line.split() for line in section.splitlines()
                       if line.startswith('\tfile scripts/git_env.py '))
            target, original = row[1:]
            selected = checkout / original
        else:
            target, selected = 'scripts/git_env.py', checkout.parent / 'redist/scripts/git_env.py'
        git = shutil.which('git')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.put(root, 'sources/redist/' + target, selected.read_bytes())
            hooks = root / 'empty-hooks'
            hooks.mkdir()
            with self.assertRaises(FileNotFoundError):
                recipe.selected_git_environment(root / 'missing-source', hooks)
            safe = recipe.selected_git_environment(root / 'sources', hooks)
            cache = root / 'cache'
            subprocess.run([git, 'init', str(cache)], env=safe, check=True, capture_output=True)
            self.put(cache, 'selected.txt', b'selected immutable object')
            subprocess.run([git, '-C', str(cache), 'add', 'selected.txt'], env=safe, check=True)
            subprocess.run([git, '-C', str(cache), '-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid',
                            '-c', 'commit.gpgsign=false', 'commit', '-m', 'fixture'], env=safe, check=True, capture_output=True)
            commit = subprocess.check_output([git, '-C', str(cache), 'rev-parse', 'HEAD'], env=safe, text=True).strip()
            tree = subprocess.check_output([git, '-C', str(cache), 'rev-parse', 'HEAD^{tree}'], env=safe, text=True).strip()
            sentinel = self.put(root, 'foreign/index', b'foreign index must remain byte equal')
            global_config = self.put(root, 'poison.gitconfig', b'[url "https://network.invalid/"]\n insteadOf = file:\n')
            poisoned = dict(os.environ, GIT_DIR=str(cache / '.git'), GIT_WORK_TREE=str(root / 'foreign'),
                GIT_INDEX_FILE=str(sentinel), GIT_CONFIG_GLOBAL=str(global_config), GIT_CONFIG_PARAMETERS='invalid injection',
                GIT_CONFIG_COUNT='1', GIT_CONFIG_KEY_0='url.https://network.invalid/.insteadOf', GIT_CONFIG_VALUE_0='file:',
                GIT_EXEC_PATH=str(root / 'foreign-exec'), GIT_TEMPLATE_DIR=str(root / 'foreign-template'))
            before = recipe.inventory(root / 'foreign')
            with mock.patch.object(sys, 'dont_write_bytecode', False):
                isolated = recipe.selected_git_environment(root / 'sources', hooks, poisoned)
            self.assertNotIn('GIT_DIR', isolated)
            self.assertEqual(isolated['GIT_ALLOW_PROTOCOL'], 'file')
            recipe.local_checkout(git, cache, root / 'reconstructed', commit, tree, env=isolated)
            subprocess.run([git, '-C', str(root / 'reconstructed'), 'add', 'selected.txt'], env=isolated, check=True)
            self.assertEqual(recipe.inventory(root / 'foreign'), before)
            self.assertEqual((root / 'reconstructed/selected.txt').read_bytes(), b'selected immutable object')
            self.assertFalse(list((root / 'sources').rglob('__pycache__')))

    def test_local_checkout_is_local_only_and_checks_exact_tree(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with mock.patch.object(recipe.subprocess, 'run') as run, mock.patch.object(recipe, 'clean_git') as clean:
                recipe.local_checkout('explicit-git', root, root / 'out', 'a' * 40, 'b' * 40)
            self.assertIn('--depth=1', run.call_args_list[1].args[0])
            self.assertIn('--no-recurse-submodules', run.call_args_list[1].args[0])
            self.assertIn(root.as_uri(), run.call_args_list[1].args[0])
            self.assertEqual(len(run.call_args_list), 3)
            clean.assert_called_once_with(root / 'out', commit='a' * 40, tree='b' * 40, git_tool='explicit-git', env=None)

    def test_materialize_outputs_feed_existing_prepare_layout_without_build(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools = {name: self.put(root, 'host/' + name) for name in recipe.HOST_TOOLS}
            pins = {'openabstractions/' + name: 'a' * 40
                    for name in (*recipe.SDK_REPOS, 'abstractions', 'redist')}
            recipe.write_json(root / 'pins.json', pins)
            selected = root / 'sources/abstractions/adopters/lemonade'
            patch = self.put(selected, 'oa-native.patch')
            manifest = {'upstream': {'commit': 'b' * 40, 'tree': 'c' * 40},
                        'oa_delta': {'file': patch.name, 'sha256': recipe.pin(patch)['sha256'], 'expected_tree': 'd' * 40},
                        'native_dependencies': [{'name': 'libwebsockets', 'commit': 'e' * 40, 'tree': 'f' * 40}]}
            recipe.write_json(selected / 'native-inputs.json', manifest)
            cache = root / 'cache'
            self.put(cache, 'content/pkg', b'only selected offline cache')
            package, extension = self.put(root, 'ui.nupkg'), self.put(root, 'ui.dll')
            args = types.SimpleNamespace(pins=root / 'pins.json', sources_root=root / 'sources',
                git_cache_root=root / 'git-cache', inputs_root=root / 'inputs', npm_cache=cache,
                wix_ui_package=package, wix_ui_extension=extension,
                **{name: tools[name] for name in ('git', 'python', 'node', 'npm', 'wix')})
            selection = {'cmake': tools['cmake'], 'msbuild': tools['msbuild'],
                         'installation_path': root / 'VS', 'installation_version': '18.9.12112.369', 'generator': 'VS selected'}
            def checkout(git, cache, destination, commit, tree, **kwargs):
                destination.mkdir(parents=True)
            with mock.patch.object(recipe, 'clean_git'), mock.patch.object(recipe, 'local_checkout', side_effect=checkout), \
                 mock.patch.object(recipe, 'selected_git_environment', return_value={'safe': 'environment'}), \
                 mock.patch.object(recipe, 'source_module', return_value=types.SimpleNamespace(msvc_toolchain=lambda: selection)), \
                 mock.patch.object(recipe, 'lws_transform', return_value=b'# actual-owner body separately tested'), \
                 mock.patch.object(recipe, 'lws_diff', return_value=b'expected canonical diff'), \
                 mock.patch.object(recipe, 'ui_rows'), mock.patch.object(recipe.subprocess, 'run') as run, \
                 mock.patch.object(recipe.subprocess, 'check_output', return_value='d' * 40):
                result = recipe.materialize(args)
            self.assertEqual(Path(result['source_root']), args.inputs_root / 'upstream/lemonade')
            self.assertEqual(Path(result['deps_root']), args.inputs_root / 'upstream/deps')
            self.assertEqual(recipe.read_json(args.inputs_root / 'host-tools.json')['tools'],
                             {name: recipe.pin(path) for name, path in tools.items()})
            self.assertEqual((args.inputs_root / 'npm-cache/content/pkg').read_bytes(), b'only selected offline cache')
            toolchain = (args.inputs_root / 'toolchain.cmake').read_text(encoding='utf-8')
            self.assertIn(str(root / 'VS').replace('\\', '/') + ',version=18.9.12112.369', toolchain)
            commands = [call.args[0] for call in run.call_args_list]
            self.assertEqual(len(commands), 3)  # apply, local checkpoint, metadata-only CMake script
            self.assertTrue(all('--build' not in command for command in commands))
            self.assertIn('-P', commands[-1])

    def test_actual_source_lws_transform_matches_canonical_configured_diff(self):
        source_value = os.environ.get('OA_LEMONADE_SOURCE')
        cmake_value = os.environ.get('OA_LEMONADE_CMAKE')
        if not source_value or not cmake_value:
            self.skipTest('selected real native source/CMake metadata fixture not supplied')
        source = Path(source_value)
        manifest = recipe.read_json(recipe.HERE / 'native-inputs.json')
        self.assertEqual(subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD^{tree}'], text=True).strip(),
                         manifest['oa_delta']['expected_tree'])
        self.assertEqual((source / 'CMakeLists.txt').read_bytes(),
                         subprocess.check_output(['git', '-C', str(source), 'show', 'HEAD:CMakeLists.txt']))
        row = next(row for row in manifest['native_dependencies'] if row['name'] == 'libwebsockets')
        git = shutil.which('git')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            disposable = root / 'lws'
            recipe.local_checkout(git, source / 'd/libwebsockets', disposable, row['commit'], row['tree'])
            script = root / 'transform.cmake'
            script.write_bytes(recipe.lws_transform(source))
            subprocess.run([cmake_value, '-Dlibwebsockets_SOURCE_DIR=' + str(disposable),
                            '-DWIN32=TRUE', '-DLEMONADE_OA_BRANDED_BUILD=TRUE', '-P', str(script)], check=True)
            diff = recipe.lws_diff(git, disposable)
            self.assertIn(b'diff --git a/CMakeLists.txt', diff)
            self.assertIn(b'diff --git a/lib/plat/windows/windows-pipe.c', diff)
            self.assertEqual(diff, recipe.lws_diff(git, source / 'd/libwebsockets'))
            subprocess.run([cmake_value, '-Dlibwebsockets_SOURCE_DIR=' + str(disposable),
                            '-DWIN32=TRUE', '-DLEMONADE_OA_BRANDED_BUILD=TRUE', '-P', str(script)], check=True)
            self.assertEqual(diff, recipe.lws_diff(git, disposable))

    def test_received_selection_checks_five_sources_and_every_measured_input(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tool = self.put(root, 'tools/cmake.exe')
            document = {'schema': 1, 'sources': {'openabstractions/' + name: 'a' * 40 for name in recipe.SDK_REPOS},
                        'build_inputs': {'tools/cmake.exe': recipe.pin(tool)}}
            document['sources']['openabstractions/abstractions'] = 'b' * 40
            document['sources']['openabstractions/redist'] = 'c' * 40
            received = root / 'received.json'
            recipe.write_json(received, document)
            with mock.patch.object(recipe, 'clean_git') as clean, \
                 mock.patch.object(recipe, 'selected_git_environment', return_value={'safe': 'environment'}):
                self.assertEqual(recipe.received_inputs(received, root, root), document)
                self.assertEqual(clean.call_count, 7)
                tool.write_bytes(b'changed')
                with self.assertRaisesRegex(ValueError, 'differs'):
                    recipe.received_inputs(received, root, root)
                document['sources'].pop('openabstractions/abstraction-facade')
                recipe.write_json(received, document)
                with self.assertRaisesRegex(ValueError, 'SDK source missing'):
                    recipe.received_inputs(received, root, root)
            self.assertNotIn('version', document)  # Capability module versions are independent.

    def test_explicit_tools_and_environment_refuse_ambient_selection(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tool = self.put(root, 'tools/cmake.exe')
            received = {'build_inputs': {'tools/cmake.exe': recipe.pin(tool)}}
            self.assertEqual(recipe.selected_tool(tool, received, root), str(tool.resolve()))
            with self.assertRaisesRegex(ValueError, 'measured'):
                recipe.selected_tool(tool, {'build_inputs': {}}, root)
            with mock.patch.dict(recipe.os.environ, {'ABSTRACTION_RUNTIME_ENDPOINT': 'foreign',
                                                    'CMAKE_PREFIX_PATH': 'foreign',
                                                    'SystemDrive': 'Z:'}, clear=False):
                environment = recipe.environment([tool], root / 'npm')
            self.assertNotIn('ABSTRACTION_RUNTIME_ENDPOINT', environment)
            self.assertNotIn('CMAKE_PREFIX_PATH', environment)
            self.assertEqual(environment['npm_config_offline'], 'true')
            self.assertEqual(environment['MSBUILDDISABLENODEREUSE'], '1')
            if os.name == 'nt':
                self.assertEqual(environment['SystemDrive'], Path(os.environ['SystemRoot']).drive)
                self.assertNotIn('USERPROFILE', environment)
                self.assertEqual(set(environment['PATH'].split(os.pathsep)),
                                 {str(tool.parent), str((Path(os.environ['SystemRoot']) / 'System32').resolve())})
                with mock.patch.dict(recipe.os.environ, {'SystemRoot': 'relative-directory'}):
                    with self.assertRaisesRegex(ValueError, 'SystemRoot'):
                        recipe.environment([tool], root / 'npm')
                with mock.patch.object(recipe, 'regular', side_effect=FileNotFoundError('missing system tool')):
                    with self.assertRaisesRegex(FileNotFoundError, 'missing system tool'):
                        recipe.environment([tool], root / 'npm')

    def test_named_host_tools_support_installed_paths_and_refuse_drift(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            inputs, installed = root / 'inputs', root / 'installed-host'
            tools = {name: self.put(installed, name, name.encode()) for name in recipe.HOST_TOOLS}
            metadata = inputs / 'host-tools.json'
            metadata.parent.mkdir()
            recipe.write_json(metadata, {'schema': 1, 'tools': {name: recipe.pin(path) for name, path in tools.items()}})
            received = {'build_inputs': {'host-tools.json': recipe.pin(metadata)}}
            args = types.SimpleNamespace(host_tools=metadata, inputs_root=inputs, **tools)
            paths, facts = recipe.host_tools(args, received)
            self.assertEqual(paths['cmake'], str(tools['cmake']))
            self.assertEqual(facts['cmake'], dict(recipe.pin(tools['cmake']), path=str(tools['cmake'])))
            tools['node'].write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError, 'host tool differs'):
                recipe.host_tools(args, received)
            document = recipe.read_json(metadata)
            document['tools']['foreign'] = recipe.pin(tools['cmake'])
            recipe.write_json(metadata, document)
            received['build_inputs']['host-tools.json'] = recipe.pin(metadata)
            with self.assertRaisesRegex(ValueError, 'finite host tool'):
                recipe.host_tools(args, received)

    def test_public_shared_accessor_import_leaves_selected_source_unchanged(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            extension = self.put(root, 'ui.dll')
            checkout = Path(__file__).resolve().parents[2]
            manifest = checkout / 'scripts/split.manifest'
            if manifest.is_file():
                # Use the actual public projection owner, not an invented layout.
                section = manifest.read_text(encoding='utf-8').split('repo redist\n', 1)[1].split('\nrepo ', 1)[0]
                row = next(line.split() for line in section.splitlines()
                           if line.startswith('\tfile windows/wix_packages.py '))
                target, original = row[1:]
                selected_source = checkout / original
            else:
                target = 'windows/wix_packages.py'
                selected_source = checkout.parent / 'redist' / target
            if not selected_source.is_file():
                self.skipTest('selected public redist checkout required')
            helper = self.put(root, 'redist/' + target, selected_source.read_bytes())
            before = recipe.inventory(root / 'redist')
            with mock.patch.object(sys, 'dont_write_bytecode', False):
                with self.assertRaisesRegex(ValueError, 'selected UI extension DLL differs'):
                    recipe.ui_rows(root, root / 'ui.nupkg', extension)
            self.assertEqual(recipe.inventory(root / 'redist'), before)
            self.assertFalse((helper.parent / '__pycache__').exists())
            self.assertFalse((root / 'abstractions/redist').exists())

    def test_materializer_selector_uses_actual_public_tree_projection_without_bytecode(self):
        checkout = Path(__file__).resolve().parents[2]
        manifest = checkout / 'scripts/split.manifest'
        if manifest.is_file():
            section = manifest.read_text(encoding='utf-8').split('repo redist\n', 1)[1].split('\nrepo ', 1)[0]
            row = next(line.split() for line in section.splitlines()
                       if line.startswith('\ttree sdk_smokes '))
            target, original = row[1:3]
            selected = checkout / original / 'prepare.py'
        else:
            target, selected = 'sdk_smokes', checkout.parent / 'redist/sdk_smokes/prepare.py'
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            helper = self.put(root, 'redist/' + target + '/prepare.py', selected.read_bytes())
            before = recipe.inventory(root)
            with mock.patch.object(sys, 'dont_write_bytecode', False):
                owner = recipe.source_module(helper)
                chosen = {'cmake': Path('selected-cmake'), 'generator': 'selected-generator'}
                with mock.patch.object(owner, 'msvc_toolchain', return_value=chosen):
                    self.assertEqual(owner.msvc_cmake(), (chosen['cmake'], chosen['generator']))
            self.assertEqual(recipe.inventory(root), before)

    def test_source_payload_uses_actual_ids_anchors_and_app_bitmap_overrides(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = self.put(root, 'Release/lemond.exe')
            icon = self.put(root, 'favicon.ico', b'icon')
            banner = self.put(root, 'banner.bmp', b'owned banner')
            product = root / 'Product.wxs'
            product.write_text('''<?xml version="1.0"?>
<?ifndef BuildDir?><?define BuildDir = "unused"?><?endif?>
<Wix><Package><?if 1 = 0?><File Id="excluded" Source="foreign"/><?endif?>
<StandardDirectory Id="LocalAppDataFolder"><Directory Id="INSTALLDIR" Name="lemonade_oa">
<Directory Id="BinDir" Name="bin"/></Directory></StandardDirectory>
<DirectoryRef Id="BinDir"><Component><File Id="EXACT_DAEMON_KEY" Source="$(var.BuildDir)/Release/lemond.exe"/></Component></DirectoryRef>
<Icon Id="LemonadeIcon" SourceFile="$(var.BuildDir)/favicon.ico"/>
<WixVariable Id="WixUIBannerBmp" Value="$(var.BuildDir)/banner.bmp"/>
</Package></Wix>''', encoding='utf-8')
            result = recipe.source_payload(product, [], {'BuildDir': root}, [root],
                                           [dict(id='WixUI_Bmp_Banner', **recipe.pin(icon))])
            self.assertEqual(result['files'], [dict(id='EXACT_DAEMON_KEY',
                path='LocalAppDataFolder/lemonade_oa/bin/lemond.exe', **recipe.pin(binary))])
            self.assertEqual(result['icons'], [dict(id='LemonadeIcon', **recipe.pin(icon))])
            self.assertEqual(result['binaries'], [dict(id='WixUI_Bmp_Banner', **recipe.pin(banner))])
            with self.assertRaises(ValueError):
                recipe.source_payload(product, [], {}, [root], [])
            product.write_text(product.read_text().replace('Id="BinDir" Name="bin"', 'Id="BinDir" Name="../outside"'))
            with self.assertRaises(ValueError):
                recipe.source_payload(product, [], {'BuildDir': root}, [root], [])

    def test_source_graph_and_command_keep_package_phase_separate(self):
        calls = []
        with mock.patch.object(recipe, 'run', side_effect=lambda command, **_: calls.append(command)):
            recipe.cmake_build('selected-cmake', Path('owned-build'), {}, 'wix_installer_minimal')
        self.assertEqual(calls, [['selected-cmake', '--build', Path('owned-build'), '--config',
                                 'Release', '--parallel', '1', '--target', 'wix_installer_minimal',
                                 '--', '/nodeReuse:false']])
        self.assertFalse(any(name in calls[0] for name in recipe.OWN_PE))

    @unittest.skipUnless(os.environ.get('OA_LEMONADE_CMAKE'), 'explicit admitted CMake required')
    def test_actual_cmake_refuses_ambient_package_and_keeps_selected_prefix(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for folder, name in (('ambient', 'OA938Ambient'), ('selected', 'OA938Selected')):
                self.put(root, folder + '/lib/cmake/' + name + '/' + name + 'Config.cmake',
                         ('set(' + name + '_FOUND TRUE)\n').encode())
            script = self.put(root, 'find.cmake', (
                'set(CMAKE_SYSTEM_PREFIX_PATH "' + (root / 'ambient').as_posix() + '")\n'
                'set(CMAKE_PREFIX_PATH "' + (root / 'selected').as_posix() + '")\n'
                'find_package(OA938Ambient CONFIG QUIET)\n'
                'find_package(OA938Selected CONFIG REQUIRED)\n'
                'if(OA938Ambient_FOUND)\nmessage(STATUS "AMBIENT_PRESENT")\nendif()\n'
                'message(STATUS "SELECTED_PRESENT")\n').encode())
            cmake = os.environ['OA_LEMONADE_CMAKE']
            control = subprocess.run([cmake, '-P', str(script)], check=True, capture_output=True, text=True)
            self.assertIn('AMBIENT_PRESENT', control.stdout)
            guarded = subprocess.run([cmake, *['-D' + key + '=' + value for key, value in recipe.FIND_FLAGS.items()],
                                      '-P', str(script)], check=True, capture_output=True, text=True)
            self.assertNotIn('AMBIENT_PRESENT', guarded.stdout)
            self.assertIn('SELECTED_PRESENT', guarded.stdout)

    def test_prepare_uses_selected_sdk_cmake_owners_and_offline_native_targets(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            inputs = root / 'inputs'
            tools = {name: self.put(inputs, 'tools/' + name, name.encode()) for name in
                     ('cmake', 'python', 'node', 'npm', 'wix', 'msbuild', 'git', 'toolchain')}
            self.put(inputs, 'npm/_cacache/content', b'pinned cache')
            transform = self.put(inputs, 'libwebsockets-configure.patch', b'owned transform')
            ui = self.put(inputs, 'wix/ui.nupkg')
            extension = self.put(inputs, 'wix/ui.dll')
            host_metadata = inputs / 'host-tools.json'
            recipe.write_json(host_metadata, {'schema': 1, 'tools': {
                name: recipe.pin(tools[name]) for name in recipe.HOST_TOOLS}})
            received = {'schema': 1, 'sources': {}, 'build_inputs': recipe.inventory(inputs)}
            receipt_path = root / 'input-receipt.json'
            recipe.write_json(receipt_path, received)
            args = types.SimpleNamespace(input_receipt=receipt_path, inputs_root=inputs,
                sources_root=root / 'sources', source_root=root / 'native-source',
                deps_root=root / 'deps', out=root / 'out', npm_cache=inputs / 'npm',
                wix_ui_package=ui,wix_ui_extension=extension,
                host_tools=host_metadata,
                version='0.8.0', generator='Visual Studio 18 2026', **tools)
            commands, targets = [], []

            def export(source, out, version, python, env):
                self.assertEqual(version, '2026.43.3+oa.0.8.0')
                folder = out / 'source/lemonade'
                self.put(folder, '.version', version.encode())
                return folder, self.put(out, 'source.tar.gz')

            def command(command, **_):
                commands.append(command)
                if '--install' in command:
                    self.put(args.out, 'sdk/include/protocol.hpp')
                    self.put(args.out, 'sdk/lib/abstraction_ipc.lib')

            def build(cmake, folder, env, *names):
                targets.append(names)
                if names:
                    for name in recipe.OWN_PE:
                        self.put(folder, 'Release/' + name)
                    self.put(folder, 'resources/web-app/index.html')
                    self.put(folder, 'CMakeCache.txt')
                    self.put(folder, 'wix_installer_minimal.vcxproj')
                    self.put(folder, 'installer/Product.wxs')

            with mock.patch.object(recipe, 'received_inputs', return_value=received), \
                    mock.patch.object(recipe, 'selected_git_environment', side_effect=lambda *args: args[2] if len(args) > 2 else {'safe': 'environment'}), \
                    mock.patch.object(recipe, 'clean_git') as source_check, \
                    mock.patch.object(recipe, 'ui_rows', return_value=[]), \
                    mock.patch.object(recipe, 'source_export', side_effect=export), \
                    mock.patch.object(recipe, 'run', side_effect=command), \
                    mock.patch.object(recipe, 'cmake_build', side_effect=build), \
                    mock.patch.object(recipe.subprocess, 'check_output', return_value=transform.read_bytes()):
                result = recipe.prepare(args)
            stored = recipe.read_json(args.out / 'native-receipt.json')
            self.assertEqual(stored['source'], 'source/lemonade')
            self.assertEqual(args.out / recipe.relative(stored['source']), args.out / 'source/lemonade')
            with self.assertRaisesRegex(ValueError, 'noncanonical relative input'):
                recipe.relative(stored['source'].replace('/', chr(92)))
            self.assertEqual(targets, [(), (), ('lemond', 'lemonade', 'LemonadeServer', 'web-app')])
            self.assertEqual([row['id'] for row in result['own_pe']], list(recipe.OWN_PE))
            native = next(command for command in commands if '-DLEMONADE_OPENABSTRACTIONS=ON' in command)
            self.assertIn('-DLEMONADE_OA_PACKAGE_MODE=release', native)
            self.assertIn('-DFETCHCONTENT_FULLY_DISCONNECTED=ON', native)
            self.assertIn('-DLEMONADE_OA_QA_LABEL=', native)
            configurations = [command for command in commands if '-S' in command]
            self.assertEqual(len(configurations), 3)
            for configuration in configurations:
                for key, value in recipe.FIND_FLAGS.items():
                    self.assertIn('-D' + key + '=' + value, configuration)
                self.assertIn('-DCMAKE_MAKE_PROGRAM=' + str(tools['msbuild']), configuration)
            self.assertIn('-DCMAKE_PREFIX_PATH=' + str(args.out / 'sdk'), native)
            self.assertTrue(any('mbedtls/framework' in str(call) for call in source_check.call_args_list))
            self.assertTrue(result['sdk'])

    def package_fixture(self, root):
        native = root / 'native'
        source = native / 'source'
        build = native / 'build'
        inputs = root / 'inputs'
        tools = {}
        for name in ('cmake', 'python', 'node', 'npm', 'wix', 'msbuild', 'git'):
            tools[name] = self.put(inputs, 'tools/' + name, name.encode())
        self.package_wix_fixture(tools['wix'], root / 'sources')
        self.put(native, 'sdk/include/protocol.hpp')
        self.put(native, 'sdk/lib/abstraction_ipc.lib')
        for name in recipe.OWN_PE:
            self.put(build, 'Release/' + name, ('unsigned ' + name).encode())
        self.put(build, 'Release/resources/favicon.ico', b'icon')
        self.put(build, 'resources/web-app/index.html')
        self.put(build, 'resources/docs/index.html')
        self.put(build, 'resources/licenses/LICENSE')
        self.put(build, 'CMakeCache.txt')
        self.put(build, 'wix_installer_minimal.vcxproj')
        product = self.put(build, 'installer/Product.wxs', b'''<Wix><Package>
<StandardDirectory Id="LocalAppDataFolder"><Directory Id="INSTALLDIR" Name="lemonade_oa"><Directory Id="BinDir" Name="bin"/></Directory></StandardDirectory>
<ComponentGroup Id="Executables" Directory="BinDir"><Component>''' + b''.join(
            ('<File Id="' + name + '" Source="$(var.BuildDir)/Release/' + name + '"/>').encode()
            for name in recipe.OWN_PE) + b'''</Component></ComponentGroup>
<Icon Id="LemonadeIcon" SourceFile="$(var.BuildDir)/Release/resources/favicon.ico"/>
</Package></Wix>''')
        for name in ('WebAppFragment.minimal.wxs', 'DocsFragment.minimal.wxs', 'OALicenses.wxs'):
            self.put(build, 'installer/' + name, b'<Wix/>')
        self.put(source, '.version', b'2026.43.3+oa.0.8.0')
        supplier = self.put(inputs, 'wix/WixUiCa_X64', b'supplier')
        supplier_package = self.put(inputs, 'wix/extension.nupkg')
        extension = self.put(inputs, 'wix/extension.dll')
        host_metadata = inputs / 'host-tools.json'
        recipe.write_json(host_metadata, {'schema': 1, 'tools': {
            name: recipe.pin(path) for name, path in tools.items()}})
        recipe.write_json(inputs / 'selected-toolchain.json', {'tools': {
            name: dict(recipe.pin(path), path=str(path)) for name, path in tools.items()}})
        for name in ('build_native.py', 'file_boundary.py'):
            self.put(root / 'sources/abstractions/adopters/lemonade', name, (recipe.HERE / name).read_bytes())
        received = {'schema': 1, 'sources': {}, 'build_inputs': recipe.inventory(inputs)}
        input_receipt = root / 'received.json'
        recipe.write_json(input_receipt, received)
        record = {'schema': 1, 'version': '0.8.0', 'producer_sources':recipe.producer_sources(), 'received_input_receipt': recipe.pin(input_receipt),
                  'selected_inputs': received, 'sdk': recipe.inventory(native / 'sdk'),
                  'wix_ui_package':recipe.pin(supplier_package),'wix_ui_extension':recipe.pin(extension),
                  'tools': {name: dict(recipe.pin(path), path=str(path)) for name, path in tools.items()},
                  'payload': recipe.inventory(build / 'Release'), 'resources': recipe.inventory(build / 'resources'),
                  'source_files': recipe.inventory(source), 'package_project': recipe.pin(build / 'wix_installer_minimal.vcxproj'),
                  'cmake_cache': recipe.pin(build / 'CMakeCache.txt'), 'build': 'build', 'source': 'source'}
        record['product_source'] = recipe.pin(product)
        recipe.write_json(native / 'native-receipt.json', record)
        signatures = {'schema': 1, 'result': 'pass', 'files': []}
        for name in recipe.OWN_PE:
            path = build / 'Release' / name
            path.write_bytes(('signed ' + name).encode())
            signatures['files'].append(dict(name='Release/' + name, **recipe.pin(path)))
        signature_file = root / 'signatures.json'
        recipe.write_json(signature_file, signatures)
        args = types.SimpleNamespace(native_root=native, input_receipt=input_receipt,
            inputs_root=inputs, sources_root=root / 'sources', signatures=signature_file,
            wix_ui_package=supplier_package,wix_ui_extension=extension,version='0.8.0',out=root / 'artifact',npm_cache=root / 'npm',**tools)
        args.host_tools = host_metadata
        return args, received

    def ui_rows(self, sources, package, extension):
        return [dict(id=name, **recipe.pin(package)) for name in recipe.UI_IDS]

    def test_component_group_directory_default_and_component_override_stay_anchored(self):
        with tempfile.TemporaryDirectory() as temporary:
            args, _ = self.package_fixture(Path(temporary))
            build, source = args.native_root / 'build', args.native_root / 'source'
            product = build / 'installer/Product.wxs'
            original = product.read_text(encoding='utf-8')

            def payload():
                return recipe.source_payload(product, [], recipe.package_variables(build, source),
                                             [source, build], [])['files']

            self.assertTrue(all(row['path'].startswith('LocalAppDataFolder/lemonade_oa/bin/')
                                for row in payload()))
            product.write_text(original.replace('<Component>', '<Component Directory="INSTALLDIR">'),
                               encoding='utf-8')
            self.assertTrue(all(row['path'] == 'LocalAppDataFolder/lemonade_oa/' + row['id']
                                for row in payload()))
            product.write_text(original.replace('Directory="BinDir"', 'Directory="ForeignDir"'),
                               encoding='utf-8')
            with self.assertRaises(KeyError):
                payload()
            product.write_text(original.replace('<StandardDirectory Id="LocalAppDataFolder">',
                                                '<StandardDirectory Id="ProgramFilesFolder">'),
                               encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'unanchored native File destination'):
                payload()

    def test_scoped_package_license_source_keeps_unresolved_symbol_refusal(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = self.put(root, 'licenses/@discoveryjs/json-ext/LICENSE', b'selected license')
            product = self.put(root, 'Product.wxs', b'''<Wix><Package>
<StandardDirectory Id="LocalAppDataFolder"><Directory Id="INSTALLDIR" Name="lemonade_oa"/></StandardDirectory>
<ComponentGroup Directory="INSTALLDIR"><Component><File Id="ScopedLicense"
 Source="$(var.WebAppSourceDir)/licenses/@discoveryjs/json-ext/LICENSE"/></Component></ComponentGroup>
</Package></Wix>''')
            rows = recipe.source_payload(product, [], {'WebAppSourceDir': root}, [root], [])['files']
            self.assertEqual(rows, [dict(recipe.pin(source), id='ScopedLicense',
                                         path='LocalAppDataFolder/lemonade_oa/LICENSE')])
            for value in ('$(var.Missing)/LICENSE', '@MISSING@/LICENSE'):
                with self.subTest(value=value), self.assertRaisesRegex(ValueError, 'unresolved WiX source symbol'):
                    recipe.expand(value, {'WebAppSourceDir': root})

    def test_package_emits_complete_source_rows_and_preserves_signed_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            args, received = self.package_fixture(Path(temporary))

            def build(_wix, build, source, extension, output, env):
                self.assertEqual(extension,args.wix_ui_extension)
                output.write_bytes(b'unsigned MSI')

            with mock.patch.object(recipe, 'received_inputs', return_value=received), \
                    mock.patch.object(recipe, 'selected_git_environment', side_effect=lambda *args: args[2] if len(args) > 2 else {'safe': 'environment'}), \
                    mock.patch.object(recipe, 'selected_tool', side_effect=lambda path, *_: str(path)), \
                    mock.patch.object(recipe, 'ui_rows', side_effect=self.ui_rows), \
                    mock.patch.object(recipe.subprocess, 'check_output', side_effect=self.package_tool_version), \
                    mock.patch.object(recipe, 'generate_package_fragments'), \
                    mock.patch.object(recipe, 'package_wix', side_effect=build):
                result = recipe.package(args)
            self.assertEqual({row['id'] for row in result['expected_payload']['files']}, set(recipe.OWN_PE))
            self.assertEqual(result['expected_payload']['icons'][0]['id'], 'LemonadeIcon')
            self.assertEqual(result['expected_payload']['binaries'][0]['id'], 'WixUiCa_X64')
            self.assertEqual(result['bundle_app_sha256'], recipe.pin(args.out / 'bundle-app.json')['sha256'])
            self.assertIn('build/installer/WebAppFragment.minimal.wxs',result['package_inputs'])
            self.assertTrue(all('build/Release/'+name in result['package_inputs'] for name in recipe.OWN_PE))

    def test_package_relocates_current_tools_and_preserves_original_compiler_provenance(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);args,received=self.package_fixture(root)
            selected=args.sources_root/'abstractions/adopters/lemonade/build_native.py'
            selected.write_bytes(b'original selected native producer; retained as data\n')
            native_path=args.native_root/'native-receipt.json'
            native=recipe.read_json(native_path)
            native['producer_sources']=recipe.native_producer_sources(args.sources_root)
            recipe.write_json(native_path,native)
            snapshots={path:path.read_bytes() for path in (args.input_receipt,args.host_tools,
                args.inputs_root/'selected-toolchain.json',native_path,args.signatures)}
            for name in recipe.PACKAGE_TOOLS:
                current=root/'current-tools'/name
                current.parent.mkdir(exist_ok=True);current.write_bytes((name+' current receiver').encode())
                if name == 'wix':
                    self.package_wix_fixture(current, args.sources_root, shim=b'different SDK apphost ')
                setattr(args,name,current)
            for name in set(recipe.HOST_TOOLS)-set(recipe.PACKAGE_TOOLS):delattr(args,name)
            def build(_wix,build,source,extension,output,env):output.write_bytes(b'unsigned MSI')
            with mock.patch.object(recipe,'received_inputs',return_value=received), \
                 mock.patch.object(recipe,'selected_git_environment',side_effect=lambda *args:args[2] if len(args)>2 else {}), \
                 mock.patch.object(recipe,'selected_tool',side_effect=lambda path,*_:str(path)), \
                 mock.patch.object(recipe,'ui_rows',side_effect=self.ui_rows), \
                 mock.patch.object(recipe.subprocess,'check_output',side_effect=self.package_tool_version), \
                 mock.patch.object(recipe,'generate_package_fragments'),mock.patch.object(recipe,'package_wix',side_effect=build):
                recipe.package(args)
            self.assertEqual({path:path.read_bytes() for path in snapshots},snapshots)

    def test_package_refuses_mutation_and_wrong_signed_roster(self):
        for case in ('mutation', 'roster', 'resource', 'product', 'during-product', 'tool', 'producer', 'during-tool',
                     'recorded-unused-tool', 'wix-payload', 'during-wix-payload'):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                args, received = self.package_fixture(Path(temporary))
                if case == 'roster':
                    document = recipe.read_json(args.signatures)
                    document['files'].pop()
                    recipe.write_json(args.signatures, document)
                if case == 'resource':
                    args.wix_ui_package.write_bytes(b'changed')
                if case == 'product':
                    path = args.native_root / 'build/installer/Product.wxs'
                    path.write_bytes(path.read_bytes().replace(b'lemonade_oa', b'foreign-app'))
                if case == 'tool':args.python.write_bytes(b'changed selected tool')
                if case == 'producer':
                    (args.sources_root/'abstractions/adopters/lemonade/build_native.py').write_bytes(b'changed native source')
                if case == 'recorded-unused-tool':
                    record = recipe.read_json(args.native_root/'native-receipt.json')
                    record['tools']['msbuild']['sha256'] = '0' * 64
                    recipe.write_json(args.native_root/'native-receipt.json', record)
                managed = args.wix.parent/'.store/wix/5.0.2/wix/5.0.2/tools/net6.0/any/wix.dll'
                if case == 'wix-payload':managed.write_bytes(b'changed managed WiX')

                def build(*_):
                    if case == 'mutation':
                        (args.native_root / 'build/Release/lemond.exe').write_bytes(b'compiler changed signed file')
                    if case == 'during-product':
                        path = args.native_root / 'build/installer/Product.wxs'
                        path.write_bytes(path.read_bytes().replace(b'lemonade_oa', b'foreign-app'))
                    if case == 'during-tool':
                        Path(_[4]).write_bytes(b'unsigned MSI')
                        args.python.write_bytes(b'changed while packaging')
                    if case == 'during-wix-payload':
                        Path(_[4]).write_bytes(b'unsigned MSI')
                        managed.write_bytes(b'changed managed WiX while packaging')

                def ui(sources, path, extension):
                    if recipe.pin(path) != received['build_inputs']['wix/extension.nupkg']:
                        raise ValueError('selected supplier package differs')
                    return self.ui_rows(sources, path, extension)

                with mock.patch.object(recipe, 'received_inputs', return_value=received), \
                        mock.patch.object(recipe, 'selected_git_environment', side_effect=lambda *args: args[2] if len(args) > 2 else {'safe': 'environment'}), \
                        mock.patch.object(recipe, 'selected_tool', side_effect=lambda path, *_: str(path)), \
                        mock.patch.object(recipe, 'ui_rows', side_effect=ui), \
                        mock.patch.object(recipe.subprocess, 'check_output', side_effect=self.package_tool_version), \
                        mock.patch.object(recipe, 'generate_package_fragments'), \
                        mock.patch.object(recipe, 'package_wix', side_effect=build) as invoked:
                    with self.assertRaises(ValueError):
                        recipe.package(args)
                    if case not in ('mutation', 'during-product', 'during-tool', 'during-wix-payload'):
                        invoked.assert_not_called()
                self.assertFalse((args.out / 'installer-receipt.json').exists())

    def test_package_tools_refuse_unapproved_versions_and_managed_payload_changes(self):
        with tempfile.TemporaryDirectory() as temporary:
            args, _ = self.package_fixture(Path(temporary))
            with mock.patch.object(recipe.subprocess, 'check_output', side_effect=self.package_tool_version):
                _, facts = recipe.package_host_tools(args)
            self.assertEqual(facts['wix']['managed']['version'], '5.0.2')
            for tool, version in (('git', 'git version 2.57.0.windows.1'), ('python', 'Python 3.13.0'), ('wix', '5.0.3')):
                with self.subTest(tool=tool):
                    def output(command, **kwargs):
                        return version if Path(command[0]) == getattr(args, tool) else self.package_tool_version(command)
                    with mock.patch.object(recipe.subprocess, 'check_output', side_effect=output):
                        with self.assertRaisesRegex(ValueError, 'package host tool identity differs: ' + tool):
                            recipe.package_host_tools(args)
            extra = args.wix.parent/'.store/wix/5.0.2/wix/5.0.2/tools/net6.0/any/foreign.dll'
            extra.write_bytes(b'unlisted managed executable')
            with mock.patch.object(recipe.subprocess, 'check_output', side_effect=self.package_tool_version):
                with self.assertRaisesRegex(ValueError, 'installed payload differs'):
                    recipe.package_host_tools(args)

    def test_package_only_wix_is_shared_and_measured_inputs_refuse_changes(self):
        with tempfile.TemporaryDirectory() as temporary:
            args,_=self.package_fixture(Path(temporary));build=args.native_root/'build';source=args.native_root/'source'
            calls=[]
            recipe.package_wix(args.wix,build,source,args.wix_ui_extension,args.out/'local.msi',{},
                runner=lambda argv,**kw:calls.append((argv,kw)))
            argv=calls[0][0]
            self.assertEqual(argv[:4],[args.wix,'build','-arch','x64'])
            self.assertEqual(argv[-4:],[build/'installer/Product.wxs',*recipe.package_fragments(build)])
            self.assertIn('IncludeTauriApp=0',argv)
            inputs={p.relative_to(args.native_root).as_posix():recipe.pin(p) for p in recipe.package_fragments(build)}
            recipe.check_package_inputs(args.native_root,inputs)
            recipe.package_fragments(build)[0].write_bytes(b'changed generated source')
            with self.assertRaisesRegex(ValueError,'package input differs'):
                recipe.check_package_inputs(args.native_root,inputs)

    def test_package_fragment_generation_uses_original_fixed_recipe(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);source=root/'source';build=root/'build'
            script=self.put(source,'src/cpp/installer/generate_tauri_fragment.py')
            with mock.patch.object(recipe,'run') as run:
                recipe.generate_package_fragments('selected-python',build,source,{})
            self.assertEqual(run.call_count,3)
            for call,target in zip(run.call_args_list,recipe.package_fragments(build)):
                argv=call.args[0];self.assertEqual(argv[:2],['selected-python',script])
                self.assertEqual(argv[argv.index('--output')+1],target)
                self.assertIn('--component-group',argv);self.assertIn('--root-id',argv)
                self.assertIn('--path-variable',argv);self.assertIn('--id-prefix',argv)


if __name__ == '__main__':
    unittest.main()
