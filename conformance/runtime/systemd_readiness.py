"""Exercise current-source runtime recovery in isolated transient systemd user units."""
import argparse
import configparser
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]

def run(args, *, env=None, cwd=None, timeout=20, check=True):
    value = subprocess.run([str(a) for a in args], env=env, cwd=cwd,
                           capture_output=True, text=True, timeout=timeout)
    if check and value.returncode:
        raise RuntimeError(f"{args[0]} failed ({value.returncode}): {value.stderr[-3000:]} {value.stdout[-3000:]}")
    return value

def wait_for(test, description, seconds=20):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        result = test()
        if result:
            return result
        time.sleep(0.1)
    raise RuntimeError("deadline: " + description)

def show(unit):
    result = run(['systemctl', '--user', 'show', unit, '--property=LoadState,ActiveState,MainPID,NRestarts,Restart,RestartUSec,TimeoutStopUSec,KillMode,SendSIGKILL'], check=False)
    values = dict(line.split('=', 1) for line in result.stdout.splitlines() if '=' in line)
    if 'LoadState' not in values:
        raise RuntimeError("unable to inspect owned unit: " + result.stderr)
    return values

@contextmanager
def fixture_directory():
    # Failed cleanup retains the fixture paths for diagnosis; no running unit
    # can lose its files through TemporaryDirectory's automatic finalizer.
    directory = Path(tempfile.mkdtemp(prefix='oa-readiness-'))
    try:
        yield directory
    except BaseException:
        print('Retained fixture directory:', directory)
        raise
    else:
        shutil.rmtree(directory)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true')
    parser.add_argument('--source-revision', help='full reviewed source commit; supplied by Windows git for WSL worktrees')
    args = parser.parse_args()
    if not args.run:
        parser.print_help()
        return
    if not args.source_revision or not re.fullmatch('[0-9a-f]{40}', args.source_revision):
        parser.error('--source-revision requires a full commit hash')
    if not Path('/proc/1/comm').read_text().strip() == 'systemd':
        raise RuntimeError('PID 1 must be systemd')
    if run(['systemctl', '--user', 'is-system-running']).stdout.strip() != 'running':
        raise RuntimeError('user manager must already be running')
    template = ROOT / 'installer/posix/linux/abstraction-runtime.service'
    config = configparser.ConfigParser(interpolation=None)
    config.optionxform = str
    config.read(template)
    settings = dict(config['Service'])
    expected = {'Type':'exec', 'Restart':'on-failure', 'RestartSec':'2s',
                'TimeoutStopSec':'10s', 'KillMode':'control-group', 'SendSIGKILL':'yes'}
    if {k:v for k,v in settings.items() if k != 'ExecStart'} != expected:
        raise RuntimeError('unit template changed; review lifecycle settings')
    build = ROOT / '.build/systemd-readiness'
    build.mkdir(parents=True, exist_ok=True)
    binary = build / 'openabstractions'
    run(['go', 'build', '-o', binary, '.'], cwd=ROOT/'serve', timeout=120)
    run([binary, 'status', '--help'], check=False)
    evidence = {'source_revision': args.source_revision, 'uid': os.getuid(),
                'go': run(['go','version']).stdout.strip(),
                'systemd': run(['systemctl','--version']).stdout.splitlines()[0],
                'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
                'template_sha256': hashlib.sha256(template.read_bytes()).hexdigest(),
                'settings': expected, 'cases': []}
    units = []
    with fixture_directory() as temporary:
        base = Path(temporary)
        try:
            for degraded in (False, True):
                case = base / ('degraded' if degraded else 'healthy')
                case.mkdir()
                for name in ('home','config','cache','data','run','state'):
                    (case/name).mkdir(mode=0o700)
                endpoint = case/'run/runtime.sock'
                paths = [endpoint, case/'run/log.sock',case/'run/config.sock',case/'run/jobs.sock']
                env = dict(os.environ, HOME=str(case/'home'), XDG_CONFIG_HOME=str(case/'config'),
                           XDG_CACHE_HOME=str(case/'cache'), XDG_DATA_HOME=str(case/'data'),
                           XDG_RUNTIME_DIR=str(case/'run'))
                unit = 'oa-readiness-' + uuid.uuid4().hex + '.service'
                units.append(unit)
                log = build/(unit+'.log')
                command = ['systemd-run','--user','--unit='+unit,'--collect']
                command += ['--property='+k+'='+v for k,v in expected.items()]
                command += ['--property=StandardOutput=append:'+str(log), '--property=StandardError=append:'+str(log)]
                for key in ('HOME','XDG_CONFIG_HOME','XDG_CACHE_HOME','XDG_DATA_HOME','XDG_RUNTIME_DIR'):
                    command += ['--setenv='+key+'='+env[key]]
                command += [binary,'serve','runtime','--endpoint',paths[0],'--log-endpoint',paths[1],
                            '--config-endpoint',paths[2],'--jobs-endpoint',paths[3],
                            '--state-dir',case/'state','--out',case if degraded else case/'records.jsonl']
                run(command)
                observed = {}
                def readiness():
                    nonlocal observed
                    result = run([binary,'status','--endpoint',endpoint,'--json','--timeout','1s'],env=env,check=False,timeout=3)
                    try:
                        observed = json.loads(result.stdout)
                    except ValueError:
                        return False
                    statuses = {x['contract']:x['status'] for x in observed.get('capabilities',[])}
                    if len(statuses) != 5:
                        return False
                    return all(value == ('not_ready' if degraded and key=='abstraction.logging/sink@1' else 'resolved')
                               for key,value in statuses.items())
                wait_for(readiness,'five typed capability statuses')
                before = show(unit)
                pid = int(before['MainPID'])
                if pid <= 1 or Path(f'/proc/{pid}/exe').resolve() != binary.resolve():
                    raise RuntimeError('owned MainPID executable mismatch')
                record = {'unit':unit,'degraded':degraded,'before':before,'initial':observed}
                if not degraded:
                    run(['systemctl','--user','kill','--kill-whom=main','--signal=SIGKILL',unit])
                    def replacement():
                        current = show(unit)
                        next_pid = int(current.get('MainPID', '0'))
                        if next_pid > 1 and next_pid != pid and int(current.get('NRestarts', '0')) >= 1:
                            return current
                        return None
                    after = wait_for(replacement, 'manager replacement MainPID')
                    wait_for(readiness,'typed readiness after replacement')
                    if Path('/proc/'+after['MainPID']+'/exe').resolve()!=binary.resolve():
                        raise RuntimeError('replacement executable mismatch')
                    record.update(after=after,recovered=observed)
                else:
                    # Observe for more than two configured restart intervals.
                    end = time.monotonic()+5
                    while time.monotonic()<end:
                        if not readiness():
                            raise RuntimeError('healthy capabilities degraded with logging')
                        current=show(unit)
                        if current['MainPID']!=before['MainPID'] or current['NRestarts']!=before['NRestarts']:
                            raise RuntimeError('logging dependency caused host restart')
                        time.sleep(0.2)
                    record['stable_seconds']=5
                run(['systemctl','--user','stop',unit],timeout=15)
                wait_for(lambda: show(unit)['LoadState']=='not-found','transient unit removal',5)
                if any(path.exists() for path in paths):
                    raise RuntimeError('runtime left socket after manager stop')
                if not (case/'state/jobs').is_dir():
                    raise RuntimeError('manager stop removed durable user state')
                record['cleanup']='stopped, unit absent, sockets absent, durable data retained until fixture cleanup'
                evidence['cases'].append(record)
        finally:
            failures=[]
            for unit in units:
                if show(unit)['LoadState']!='not-found':
                    stopped=run(['systemctl','--user','stop',unit],check=False,timeout=15)
                    if stopped.returncode:
                        failures.append(stopped.stderr)
                    run(['systemctl','--user','reset-failed',unit],check=False)
                    if show(unit)['LoadState']!='not-found':
                        failures.append('owned unit remains: '+unit)
            evidence['cleanup_errors']=failures
            (build/'result.json').write_text(json.dumps(evidence,indent=2)+'\n')
            if failures:
                raise RuntimeError('cleanup failed: '+repr(failures))
    print(json.dumps({'result':'PASS','evidence':str(build/'result.json'),
                      'units':[{'unit':c['unit'],'degraded':c['degraded'],'pid':c['before']['MainPID'],
                                'replacement':c.get('after',{}).get('MainPID'),'cleanup':c['cleanup']}
                               for c in evidence['cases']]},indent=2))

if __name__=='__main__':
    main()
