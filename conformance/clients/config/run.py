"""Prove the installed C++ configuration client reaches the real per-user Go service.

Default execution builds the central host. --service-only is an explicit smaller
fixture for independent integration work and must not be reported as central-host coverage.
"""
import argparse,json,os,queue,shutil,subprocess,sys,threading,uuid
from pathlib import Path
HERE=Path(__file__).resolve().parent
ROOT=HERE.parents[2]
sys.path.insert(0,str(HERE.parent))
from workspace import layer,environment,compiler_metadata
BUILD=ROOT/'.build'/'config'
def cmake_path():
 found=os.environ.get('CMAKE') or shutil.which('cmake')
 if found:return found
 for edition in ('Community','Professional','Enterprise','BuildTools'):
  p=Path(os.environ.get('ProgramFiles','C:/Program Files'))/('Microsoft Visual Studio/18/'+edition+'/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin/cmake.exe')
  if p.is_file():return str(p)
 raise RuntimeError('CMake not found; set CMAKE')
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--run',action='store_true',help='build and execute the isolated Windows service check')
p.add_argument('--service-only',action='store_true',help='use a small Serve fixture instead of the central host')
p.add_argument('--toolchain',action='store_true',help='print CMake metadata without executing services')
a=p.parse_args()
if a.toolchain:subprocess.run([cmake_path(),'--version'],check=True);raise SystemExit(0)
if not a.run:p.print_help();raise SystemExit(0)
if os.name!='nt':raise SystemExit('This conformance runner measures Windows. Unit tests also cover Linux.')
BUILD.mkdir(parents=True,exist_ok=True)
env=environment(BUILD)
def run(command,cwd=ROOT,environment=env,timeout=120):
 r=subprocess.run([str(x) for x in command],cwd=cwd,env=environment,capture_output=True,text=True,encoding='utf-8',timeout=timeout)
 print(r.stdout.replace(str(ROOT),'$ROOT').replace(ROOT.as_posix(),'$ROOT'),end='')
 if r.returncode:raise RuntimeError(r.stdout+r.stderr)
 return r.stdout
run(['go','version'])
if a.service_only:run(['go','build','-o',BUILD/'host.exe',HERE/'service_main.go'])
else:run(['go','build','-o',BUILD/'host.exe','.'],ROOT/'serve')
cmake=cmake_path()
run([cmake,'-S',layer('abstraction-config')/'cpp','-B',BUILD/'native','-DBUILD_SHARED_LIBS=OFF','-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_ipc=TRUE'])
compiler_metadata(BUILD/'native')
run([cmake,'--build',BUILD/'native','--config','Release'])
identity=uuid.uuid4().hex[:8];stage=BUILD/('s-'+identity);outside=BUILD/('c-'+identity)
run([cmake,'--install',BUILD/'native','--config','Release','--prefix',stage])
run([cmake,'-S',HERE,'-B',outside,'-DCMAKE_PREFIX_PATH='+str(stage)])
run([cmake,'--build',outside,'--config','Release'])
consumer=outside/'Release'/'config_consumer.exe'
case=BUILD/uuid.uuid4().hex[:12];client_home=case/'client';provider_home=case/'provider'
client_home.mkdir(parents=True);provider_home.mkdir(parents=True)
source=provider_home/'abstraction'/'config.json';source.parent.mkdir()
values={'store':'provider-store','nas_store':'provider-nas','log_sink':'provider-log','log_service':'provider-service','off':{'nas':'maintenance'}}
source.write_text(json.dumps(values),encoding='utf-8')
endpoint='\\\\.\\pipe\\oa-config-proof-'+uuid.uuid4().hex
client_env=dict(env,ABSTRACTION_CONFIG_ENDPOINT=endpoint)
for key in ['HOME','USERPROFILE','LOCALAPPDATA','APPDATA','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_STATE_HOME','XDG_CACHE_HOME']:client_env[key]=str(client_home)
for key in ['ABSTRACTION_STORE','ABSTRACTION_NAS_STORE','ABSTRACTION_LOG','ABSTRACTION_LOG_SERVICE']:client_env[key]=''
host_env=dict(env,APPDATA=str(provider_home),HOME=str(provider_home),USERPROFILE=str(provider_home),PROGRAMDATA=str(case/'machine'),ABSTRACTION_STORE='host-environment-must-not-leak')
arguments=[str(BUILD/'host.exe')]+([] if a.service_only else ['serve','config'])+['--endpoint',endpoint]
server=subprocess.Popen(arguments,env=host_env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,encoding='utf-8')
ready=queue.Queue();threading.Thread(target=lambda:ready.put(server.stdout.readline()),daemon=True).start()
def consume(mode,*args,environment=client_env):run([consumer,mode,*args],client_home,environment,15)
try:
 if not ready.get(timeout=15).startswith('config: listening '):raise RuntimeError('config listener did not become ready')
 consume('read','provider-store',source)
 consume('same-value','provider-store',source)
 consume('override','caller-store',source,environment=dict(client_env,ABSTRACTION_STORE='caller-store'))
 before=source.read_bytes();consume('bad-request');assert source.read_bytes()==before,'refused client changed provider'
 values['store']='provider-changed';source.write_text(json.dumps(values),encoding='utf-8')
 consume('read','provider-changed',source)
 source.write_text('{broken',encoding='utf-8');consume('defaults')
 source.unlink();consume('defaults')
 assert server.poll() is None,'service died with client'
 assert not list(client_home.rglob('*')),'client created local configuration/store'
finally:
 if server.poll() is None:server.kill()
 stdout,stderr=server.communicate(timeout=10)
consume('absent')
assert not list(client_home.rglob('*')),'absent-service fallback wrote client state'
assert 'ignoring' in stderr,'malformed provider source was silently ignored'
print('PASS C++ installed client -> generated protocol -> shared framing -> '+('Serve fixture' if a.service_only else 'central host')+' -> identity-bound same-user config provider')
print('PASS caller override, provider provenance/reload, value-only stamp, malformed/missing source, request refusal, absent service, no client store')
