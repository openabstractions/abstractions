"""Prove the installed C++ configuration client reaches the real per-user Go service.

Default execution builds the central host. --service-only is an explicit smaller
fixture for independent integration work and must not be reported as central-host coverage.
"""
import argparse,json,os,queue,shutil,subprocess,sys,threading,uuid
from pathlib import Path
HERE=Path(__file__).resolve().parent
ROOT=HERE.parents[2]
sys.path.insert(0,str(HERE.parent))
from workspace import layer,environment,msvc_toolchain,certify_msvc, msvc_ninja_arguments, source_revision, dry_run_stop, DRY_RUN_HELP, CMAKE_BUILD_TYPE_RELEASE, ReadinessOutput
BUILD=ROOT/'.build'/'config'
def config_host_invocation(host, env, provider_home, case, endpoint, service_only, isolated_name):
 """Keep native profile metadata for the explicitly private central runtime."""
 host_env=dict(env,ABSTRACTION_STORE='host-environment-must-not-leak')
 if service_only:
  host_env.update(APPDATA=str(provider_home),HOME=str(provider_home),
                  USERPROFILE=str(provider_home),PROGRAMDATA=str(case/'machine'))
  arguments=[str(host),'--endpoint',endpoint]
 else:
  arguments=[str(host),'serve','runtime','--isolated',isolated_name,
             '--state-dir',str(provider_home.resolve()),'--config-endpoint',endpoint,
             '--without-jobs','--without-models','--supervised']
 return arguments,host_env

p=argparse.ArgumentParser(description=__doc__+'\nCertifies MSVC on Windows: run from a vcvars64 developer environment; other toolchains are refused before building.')
p.add_argument('--run',action='store_true',help='build and execute the isolated Windows service check')
p.add_argument('--dry-run', action='store_true', help=DRY_RUN_HELP)
p.add_argument('--service-only',action='store_true',help='use a small Serve fixture instead of the central host')
p.add_argument('--toolchain',action='store_true',help='print CMake metadata without executing services')
a=p.parse_args()
if a.toolchain:
 cmake=msvc_toolchain(dict(os.environ));generator=msvc_ninja_arguments(cmake)
 subprocess.run([cmake,'--version'],check=True)
 subprocess.run([generator[-1].split('=',1)[1],'--version'],check=True)
 raise SystemExit(0)
if not (a.run or a.dry_run):p.print_help();raise SystemExit(0)
source_revision()
if a.dry_run: dry_run_stop('config', a)
if os.name!='nt':raise SystemExit('This conformance runner measures Windows. Unit tests also cover Linux.')
BUILD.mkdir(parents=True,exist_ok=True)
env=environment(BUILD)
cmake=msvc_toolchain(env)
generator=msvc_ninja_arguments(cmake)
def run(command,cwd=ROOT,environment=env,timeout=120):
 r=subprocess.run([str(x) for x in command],cwd=cwd,env=environment,capture_output=True,text=True,encoding='utf-8',timeout=timeout)
 print(r.stdout.replace(str(ROOT),'$ROOT').replace(ROOT.as_posix(),'$ROOT'),end='')
 if r.returncode:raise RuntimeError(r.stdout+r.stderr)
 return r.stdout
run(['go','version'])
# The selected carrier's sibling declarations are artifact inputs. A fresh
# fixture-only image directory has no inherited installation declarations.
host=BUILD/('host-'+uuid.uuid4().hex[:12])/'host.exe';host.parent.mkdir()
if a.service_only:run(['go','build','-o',host,HERE/'service_main.go'])
else:run(['go','build','-o',host,'.'],ROOT/'serve')
identity=uuid.uuid4().hex[:8];native=BUILD/('n-'+identity);stage=BUILD/('s-'+identity);outside=BUILD/('c-'+identity)
# A fresh tree per run: a cache configured by another toolchain or generator
# installs no per-configuration export file for --config Release.
run([cmake,'-S',layer('abstraction-config')/'cpp','-B',native,*generator,'-DBUILD_SHARED_LIBS=OFF','-DCMAKE_DISABLE_FIND_PACKAGE_abstraction_ipc=TRUE',CMAKE_BUILD_TYPE_RELEASE])
certify_msvc(native)
run([cmake,'--build',native,'--config','Release','--parallel','2'])
run([cmake,'--install',native,'--config','Release','--prefix',stage])
run([cmake,'-S',HERE,'-B',outside,*generator,'-DCMAKE_PREFIX_PATH='+str(stage),CMAKE_BUILD_TYPE_RELEASE])
certify_msvc(outside)
run([cmake,'--build',outside,'--config','Release','--parallel','2'])
consumer=outside/'config_consumer.exe'
case=BUILD/uuid.uuid4().hex[:12];client_home=case/'client';provider_home=case/'provider'
client_home.mkdir(parents=True)
source=provider_home/('abstraction' if a.service_only else 'config')/'config.json'
values={'store':'provider-store','nas_store':'provider-nas','log_sink':'provider-log','log_service':'provider-service','off':{'nas':'maintenance'}}
if a.service_only:
 source.parent.mkdir(parents=True)
 source.write_text(json.dumps(values),encoding='utf-8')
endpoint='\\\\.\\pipe\\oa-config-proof-'+uuid.uuid4().hex
client_env=dict(env,ABSTRACTION_CONFIG_ENDPOINT=endpoint)
for key in ['HOME','USERPROFILE','LOCALAPPDATA','APPDATA','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_STATE_HOME','XDG_CACHE_HOME']:client_env[key]=str(client_home)
for key in ['ABSTRACTION_STORE','ABSTRACTION_NAS_STORE','ABSTRACTION_LOG','ABSTRACTION_LOG_SERVICE']:client_env[key]=''
arguments,host_env=config_host_invocation(host,env,provider_home,case,endpoint,a.service_only,'config-'+uuid.uuid4().hex[:12])
server=subprocess.Popen(arguments,env=host_env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,encoding='utf-8')
ready=ReadinessOutput(server)
def consume(mode,*args,environment=client_env):run([consumer,mode,*args],client_home,environment,15)
try:
 ready.expect('config: listening ' if a.service_only else 'READY 1',prefix=a.service_only)
 if not a.service_only:
  source.parent.mkdir(parents=True,exist_ok=True)
  source.write_text(json.dumps(values),encoding='utf-8')
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
 stdout,stderr=ready.finish()
consume('absent')
assert not list(client_home.rglob('*')),'absent-service fallback wrote client state'
assert 'ignoring' in stderr,'malformed provider source was silently ignored'
print('PASS C++ installed client -> generated protocol -> shared framing -> '+('Serve fixture' if a.service_only else 'central host')+' -> identity-bound same-user config provider')
print('PASS caller override, provider provenance/reload, value-only stamp, malformed/missing source, request refusal, absent service, no client store')
