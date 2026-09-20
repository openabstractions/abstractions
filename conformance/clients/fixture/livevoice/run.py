"""Build and measure the isolated live-voice probe on Windows or Linux.

Each client sends 200 ms / 6400-byte PCM frames while Observe long-polls in
parallel and a real inference chat provider streams fake upstream tokens.
Reports p99 round-trip latency minus measured backend/long-poll waiting time
for Append and Observe separately. The gate is <200 ms in each direction.
This tests transport suitability; it does not claim a production live@1 API.

--measure builds the Go fixture and runs Go and Python. --ipc-library can
reuse an explicitly supplied shared library; otherwise CMake builds one.
No persistent runtime registration or installed product is changed.
"""
import argparse
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
import uuid

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1]))
from workspace import ROOT, environment, layer, build_root, cmake_for, source_revision


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--measure", action="store_true")
    p.add_argument("--frames", type=int, default=100)
    p.add_argument("--warmup", type=int, default=5)
    p.add_argument("--language", choices=("all","go","python"), default="all")
    p.add_argument("--fault-delay-ms", type=int, default=0, help="inject dispatch delay; 250 must fail the latency gate")
    p.add_argument("--ipc-library", type=Path)
    p.add_argument("--cmake")
    p.add_argument("--results", type=Path)
    a = p.parse_args()
    if not a.measure:
        p.print_help()
        return
    if sys.platform not in ("win32", "linux"):
        p.error("measurement requires Program proof on Windows or Linux")
    if a.frames < 1 or a.warmup < 0 or a.frames + a.warmup > 9999:
        p.error("invalid frame count")
    revision = source_revision()
    with tempfile.TemporaryDirectory(prefix="livevoice-", dir=build_root()) as tmp:
        build = Path(tmp)
        env = environment(build)
        def run(args, timeout=300):
            out = subprocess.run(list(map(str,args)),cwd=ROOT,env=env,capture_output=True,text=True,timeout=timeout)
            if out.returncode:
                raise RuntimeError(" ".join(map(str,args))+"\n"+out.stdout+out.stderr)
            return out.stdout
        exe = build / ("livevoice.exe" if os.name == "nt" else "livevoice")
        run(["go","build","-o",exe,HERE/"main.go"])
        shared = a.ipc_library
        if not shared and a.language!="go":
            cmake = cmake_for(env,a.cmake)
            run([cmake,"-S",layer("abstraction-identity")/"cpp","-B",build/"native",
                 "-DBUILD_SHARED_LIBS=ON","-DABSTRACTION_IPC_BUILD_TESTS=OFF","-DCMAKE_BUILD_TYPE=Release"])
            run([cmake,"--build",build/"native","--config","Release"])
            run([cmake,"--install",build/"native","--config","Release","--prefix",build/"prefix"])
            shared=build/"prefix"/("bin/abstraction_ipc.dll" if os.name=="nt" else "lib/libabstraction_ipc.so")
        if shared: env["ABSTRACTION_IPC_LIBRARY"]=str(shared.resolve())
        env["PYTHONPATH"]=os.pathsep.join(map(str,[HERE/"wire"/"py",layer("abstraction-identity")/"py",layer("abstraction-inference")/"py"]))
        results=[]
        for name, command in (("go",[exe,"--mode","go"]),("python",[sys.executable,HERE/"probe.py"])):
            if a.language!="all" and name!=a.language: continue
            token="oa-live-"+uuid.uuid4().hex[:12]
            endpoint="\\\\.\\pipe\\"+token if os.name=="nt" else str(build/token)
            chat_endpoint=endpoint+"-chat"
            server=subprocess.Popen([str(exe),"--mode","server","--endpoint",endpoint,"--chat-endpoint",chat_endpoint,"--fault-delay-ms",str(a.fault_delay_ms)],
                                    cwd=build,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
            lines=queue.Queue()
            threading.Thread(target=lambda:lines.put(server.stdout.readline()),daemon=True).start()
            try:
                if lines.get(timeout=15).strip()!="READY":
                    raise RuntimeError("voice server failed to start: "+server.stderr.read())
                output=run(command+["--endpoint",endpoint,"--chat-endpoint",chat_endpoint,"--frames",a.frames,"--warmup",a.warmup],timeout=30+(a.frames+a.warmup)*0.25)
                result=json.loads(output)
                result.update(platform=sys.platform,source=revision)
                results.append(result)
                print(json.dumps(result),flush=True)
            finally:
                server.terminate()
                try: server.wait(timeout=10)
                except subprocess.TimeoutExpired: server.kill();server.wait(timeout=5)
                server.stdout.close();server.stderr.close()
        if a.results:
            a.results.parent.mkdir(parents=True,exist_ok=True)
            a.results.write_text(json.dumps(results,indent=2)+"\n",encoding="utf-8")


if __name__=="__main__":
    main()
