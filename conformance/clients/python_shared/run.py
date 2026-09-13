"""Install isolated Python packages and exercise shared native IPC against Go."""
import argparse
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import uuid

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true", help="build/install into a temporary prefix and run isolated fixtures")
    parser.add_argument("--cmake", default="cmake")
    args = parser.parse_args()
    if not args.run:
        parser.print_help()
        return
    def command(argv, cwd=ROOT, env=None):
        subprocess.run([str(x) for x in argv], cwd=cwd, env=env, check=True, timeout=120)

    with tempfile.TemporaryDirectory(prefix="oa-python-") as tmp:
        base = Path(tmp)
        native, prefix = base/"native", base/"prefix"
        command([args.cmake,"-S",ROOT/"openabstractions-flat/abstraction-identity/cpp","-B",native,
                 "-DBUILD_SHARED_LIBS=ON","-DABSTRACTION_IPC_BUILD_TESTS=OFF",f"-DCMAKE_INSTALL_PREFIX={prefix}"])
        command([args.cmake,"--build",native,"--config","Release"])
        command([args.cmake,"--install",native,"--config","Release"])
        library = prefix/("bin/abstraction_ipc.dll" if os.name=="nt" else "lib/libabstraction_ipc.dylib" if sys.platform=="darwin" else "lib/libabstraction_ipc.so")
        if sys.platform == "darwin":
            raise RuntimeError("Darwin identity refusal requires its separate native proof; this fixture does not claim it passes")
        packages=[]
        for name in ("identity","facade","logging"):
            path=base/name
            shutil.copytree(ROOT/f"openabstractions-flat/abstraction-{name}/py",path)
            packages.append(path)
        if not (base/"logging/abstraction/logging/rec.py").is_file():
            raise RuntimeError("generated logging Python source is required")
        installed=base/"python"
        command([sys.executable,"-m","pip","install","--no-index","--no-build-isolation","--no-deps","--target",installed,*packages])
        test=base/"test_transport.py"
        test.write_text("import sys; sys.path.insert(0, " + repr(str(installed)) + ")\n" + (ROOT/"openabstractions-flat/abstraction-identity/py/test_transport.py").read_text(encoding="utf-8"),encoding="utf-8")
        command([sys.executable,"-I",test],base)
        history_test=base/"test_history.py"
        history_test.write_text("import sys; sys.path.insert(0, " + repr(str(installed)) + ")\n" + (ROOT/"openabstractions-flat/abstraction-facade/py/test_history.py").read_text(encoding="utf-8"),encoding="utf-8")
        command([sys.executable,"-I",history_test],base)
        host=base/("host.exe" if os.name=="nt" else "host")
        command(["go","build","-o",host,HERE/"host.go"])
        principal=subprocess.check_output([str(host),"principal"],text=True,timeout=5).strip()
        home=base/"app";home.mkdir()
        consumer=base/"consumer.py";shutil.copyfile(HERE/"consumer.py",consumer)
        env=dict(os.environ,HOME=str(home),USERPROFILE=str(home),PYTHONDONTWRITEBYTECODE="1",ABSTRACTION_IPC_PREFIX=str(prefix))
        env.pop("ABSTRACTION_IPC_LIBRARY",None)
        def invoke(endpoint,mode):
            child_env=dict(env)
            child_env["ABSTRACTION_RUNTIME_ENDPOINT"]="" if mode=="bootstrap" else endpoint
            command([sys.executable,"-I",consumer,installed,library,endpoint,mode,principal,host.resolve()],home,child_env)
            if list(home.iterdir()):
                raise AssertionError("client created provider files")
        def endpoint():
            return r"\\.\pipe\oa-python-"+uuid.uuid4().hex if os.name=="nt" else str(base/(uuid.uuid4().hex+".sock"))
        default_env=dict(env,ABSTRACTION_RUNTIME_ENDPOINT="")
        expected=subprocess.check_output([str(host),"bootstrap"],env=default_env,text=True,timeout=5).strip()
        invoke(expected,"bootstrap")
        invoke(endpoint(),"absent")
        for mode in ("runtime","oversized","quiet"):
            address=endpoint()
            proc=subprocess.Popen([str(host),mode,address],cwd=base,env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,encoding="utf-8")
            lines=queue.Queue()
            threading.Thread(target=lambda:[lines.put(line.rstrip()) for line in proc.stdout],daemon=True).start()
            try:
                if lines.get(timeout=10)!="READY":raise AssertionError("host readiness missing")
                invoke(address,{"runtime":"log","oversized":"oversized","quiet":"cancel"}[mode])
                if mode=="runtime":
                    line=lines.get(timeout=5)
                    if not line.startswith("RECORD "):raise AssertionError(line)
                    record=json.loads(line[7:])
                    assert record["msg"]=="python shared IPC ✓" and record["level"]==2
                    assert record["attrs"]=={"component":"outside-consumer"}
                    assert record.get("identity"),"receiving boundary did not retain caller evidence"
            finally:
                proc.stdin.write("stop\n");proc.stdin.flush()
                try:proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill();proc.wait();raise
                if proc.returncode:raise AssertionError(proc.stderr.read())
        print("PASS: installed Python resolver/logging, exact payload, absent runtime, oversized frame, cancellation, empty client home")


if __name__=="__main__":
    main()
