"""Run every conformance runner with --dry-run and require it to stop before building.

A dry run covers imports, argument handling, toolchain selection and source
revision. It catches a runner that fails on every --run in seconds, without a
build. No arguments are needed; --help prints this text.
"""
import argparse
from pathlib import Path
import subprocess
import sys

HERE = Path(__file__).resolve().parent


def main():
    argparse.ArgumentParser(description=__doc__).parse_args()
    failed = []
    runners = sorted(HERE.glob('*/run.py'))
    for runner in runners:
        name = runner.parent.name
        try:
            result = subprocess.run([sys.executable, str(runner), '--dry-run'], cwd=HERE.parents[1],
                                    capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=120)
        except subprocess.TimeoutExpired:
            failed.append(name + ': timeout after 120 s')
            continue
        output = result.stdout + result.stderr
        if result.returncode != 0 or ('DRY RUN ' + name + ': stopped before building') not in output:
            failed.append(name + ': exit ' + str(result.returncode) + '\n' + output.strip()[-800:])
        else:
            print('dry run ok:', name)
    if not runners:
        failed.append('no conformance runners found')
    if failed:
        print('\n'.join(failed), file=sys.stderr)
        raise SystemExit(1)
    print('PASS', len(runners), 'conformance runner dry runs')


if __name__ == '__main__':
    main()
