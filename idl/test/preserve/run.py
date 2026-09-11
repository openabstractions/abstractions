"""Exercise the preserve fixture across generated backends; no production migration."""
import argparse
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys

HERE = Path(__file__).resolve().parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--scratch", type=Path, required=True)
parser.add_argument("--language", action="append", choices=["go", "python", "javascript", "cpp", "rust"])
parser.add_argument("--duplicates", action="append", choices=["refuse", "last"], help="default: test both duplicate-key policies")
parser.add_argument("--escape", choices=["minimal", "ascii"], default="minimal")
if len(sys.argv) == 1:
    parser.print_help()
    raise SystemExit(0)
args = parser.parse_args()
scratch = args.scratch.resolve()
scratch.mkdir(parents=True, exist_ok=True)
env = dict(os.environ, GOWORK="off")

def run(command, cwd):
    result = subprocess.run(command, cwd=cwd, env=env, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f"{command!r}: exit {result.returncode}\n{result.stdout}\n{result.stderr}")
    return result.stdout

decoder = json.JSONDecoder()
def members(text):
    """Decoded keys and original value tokens, independently of any generated codec."""
    i = text.index("{") + 1
    result = {}
    while True:
        while text[i].isspace(): i += 1
        if text[i] == "}": return result
        key, i = decoder.raw_decode(text, i)
        while text[i].isspace(): i += 1
        assert text[i] == ":"
        i += 1
        while text[i].isspace(): i += 1
        start = i
        _, i = decoder.raw_decode(text, i)
        result[key] = text[start:i]
        while text[i].isspace(): i += 1
        if text[i] == ",": i += 1

def tokens(text):
    # Remove only whitespace outside strings; preserve escapes and number spelling.
    out, i = [], 0
    while i < len(text):
        if text[i] == '"':
            _, end = decoder.raw_decode(text, i)
            out.append(text[i:end]); i = end
        else:
            if not text[i].isspace(): out.append(text[i])
            i += 1
    return "".join(out)

def check_scope(before, after, kind="root", mutate=False):
    known = {"root": ["id", "child", "children", "empty", "closed", "dropped"], "child": ["name", "count"], "empty": []}[kind]
    a, b = members(before), members(after)
    extras = [k for k in a if k not in known]
    for key in extras:
        assert key in b and tokens(a[key]) == tokens(b[key]), (kind, key, a[key], b.get(key))
    expected_order = [k for k in known if k in b] + sorted(extras, key=lambda k: k.encode())
    assert list(b) == expected_order, (list(b), expected_order)
    if mutate and kind != "empty":
        assert json.loads(b["id" if kind == "root" else "name"]) == ("edited" if kind == "root" else "edited-child")
    if kind == "root":
        for key in ("child", "empty"):
            if key in a: check_scope(a[key], b[key], key, mutate)
        if "children" in a:
            # Fixtures use child objects whose original raw token spelling also matters.
            def objects(text):
                values=[]; i=1
                while True:
                    while text[i].isspace() or text[i]==",": i+=1
                    if text[i]=="]":return values
                    start=i; _,i=decoder.raw_decode(text,i); values.append(text[start:i])
            for x,y in zip(objects(a["children"]), objects(b["children"])):check_scope(x,y,"child",mutate)
        if "dropped" in b: assert list(members(b["dropped"])) == ["fixed"]
        for key in ("closed", "dropped"):
            if key in a:
                assert json.loads(a[key])["fixed"] == json.loads(b[key])["fixed"], "known string changed"

vcvars = Path(os.environ.get("VCVARS", "C:/Program Files/Microsoft Visual Studio/18/Community/VC/Auxiliary/Build/vcvars64.bat"))
languages = args.language or ["go", "python", "javascript", "rust", "cpp"]
all_results=[]
canonical={}
for policy in args.duplicates or ("refuse", "last"):
    for lang in languages:
        tool={"go":"go", "python":sys.executable, "javascript":"node", "rust":"rustc", "cpp":"c++"}[lang]
        available=vcvars.exists() if lang=="cpp" and os.name=="nt" else shutil.which(tool) is not None
        if not available:
            if args.language:raise RuntimeError(f"requested {lang} toolchain unavailable")
            print(f"{lang}: UNPROVEN (toolchain unavailable)");continue
        work=scratch / (policy+"-"+lang);work.mkdir(exist_ok=True)
        schema=(HERE/"preserve.thrift").read_text()
        schema=schema.replace('escape = "minimal"', f'escape = "{args.escape}"')
        if policy=="last":
            schema=schema.replace('duplicate_keys = "refuse"','duplicate_keys = "last"')
            lines=[];refusal=False;ordinal=0
            for line in schema.splitlines():
                if line.strip()=="refusal {":refusal=True
                if refusal and re.match(r"\s*\d+:",line):
                    if "duplicate_key (" in line:continue
                    ordinal+=1;line=re.sub(r"^\s*\d+:",f" {ordinal}:",line)
                if line.strip()=="}":refusal=False
                lines.append(line)
            schema="\n".join(lines)+"\n"
        definition=work/"preserve.thrift";definition.write_text(schema,encoding="utf-8")
        run(["go","run",".",str(definition),str(work/"generated"),lang], HERE.parents[1]/"gen")
        extension={"go":"go", "python":"py", "javascript":"mjs", "rust":"rs", "cpp":"cpp"}[lang]
        shutil.copyfile(HERE/("driver."+extension), work/("driver."+extension))
        if lang=="go":
            (work/"go.mod").write_text("module preserve.test\n\ngo 1.22\n")
            run(["go","build","-o","driver.exe","driver.go"],work);command=[str(work/"driver.exe")]
        elif lang=="python":command=[sys.executable,"-B",str(work/"driver.py")]
        elif lang=="javascript":command=["node",str(work/"driver.mjs")]
        elif lang=="rust":
            run(["rustc","--edition=2021","-A","warnings","driver.rs","-o","driver.exe"],work);command=[str(work/"driver.exe")]
        else:
            if os.name=="nt":
                (work/"build.cmd").write_text(f'@echo off\ncall "{vcvars}" >nul\ncl /nologo /std:c++20 /EHsc /utf-8 driver.cpp /Fe:driver.exe\n')
                run(["cmd","/c","build.cmd"],work)
            else:run(["c++","-std=c++20","driver.cpp","-o","driver.exe"],work)
            command=[str(work/"driver.exe")]
        count=0
        for fixture in sorted((HERE/"corpus").glob("*.json")):
            word=fixture.name.split(".")[0]
            expected="ok" if word=="accept" or (word=="duplicate_key" and policy=="last") else word
            output=work/(fixture.name+".out")
            mode="mutate" if word=="accept" else "read"
            actual=run(command+[str(fixture),str(output),mode],work)
            assert actual==expected,(policy,lang,fixture.name,expected,actual)
            if actual=="ok":
                check_scope(fixture.read_text(encoding="utf-8"),output.read_text(encoding="utf-8"),mutate=mode=="mutate")
                second=work/"again.json"
                assert run(command+[str(output),str(second),"read"],work)=="ok"
                assert output.read_bytes()==second.read_bytes(), "non-idempotent preservation"
                identity=(policy,"read",fixture.name)
                assert output.read_bytes()==canonical.setdefault(identity,output.read_bytes()), "backend output differs"
            count+=1
        for line in (HERE/"writes.tsv").read_text().splitlines():
            name,scope,key,value,expected=line.split("\t")
            if scope=="bad-key" and lang=="rust":continue # String cannot contain invalid Unicode.
            if expected=="duplicate_key" and policy=="last":expected="ok"
            output=work/(name+".out")
            actual=run(command+[str(HERE/"base.json"),str(output),scope,key,str(HERE/"raw"/value)],work)
            assert actual==expected,(policy,lang,name,expected,actual)
            if actual=="ok":
                result=members(output.read_text(encoding="utf-8"))
                if scope=="child":result=members(result["child"])
                if scope=="repeated":result=members(result["children"][1:-1])
                assert tokens(result[key])==tokens((HERE/"raw"/value).read_text())
                identity=(policy,"write",name)
                assert output.read_bytes()==canonical.setdefault(identity,output.read_bytes()), "backend output differs"
            count+=1
        text=f"{args.escape}/{policy}/{lang}: {count} preservation and writer checks passed"
        print(text,flush=True);all_results.append(text)
if not all_results:raise RuntimeError("No backend tested")
(scratch/"results.txt").write_text("\n".join(all_results)+"\n")
