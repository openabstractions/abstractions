"""Generate job codecs and check timestamp reader/writer compatibility in isolation."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

HERE = Path(__file__).resolve().parent
IDL = HERE.parents[1]
definition = IDL / "testdata/job.thrift"
if not definition.exists():
    definition = IDL.parent / "openabstractions-flat/abstraction-job/job.thrift"
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--scratch", type=Path, required=True, help="new or existing isolated output directory")
parser.add_argument("--language", action="append", choices=["go", "python", "javascript", "rust", "cpp"], help="default: all available toolchains")
if len(sys.argv) == 1:
    parser.print_help()
    raise SystemExit(0)
args = parser.parse_args()
scratch = args.scratch.resolve()
scratch.mkdir(parents=True, exist_ok=True)
env = dict(os.environ, GOWORK="off")

def run(command, cwd=scratch):
    result = subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f"{command!r} exited {result.returncode}\n{result.stdout}\n{result.stderr}")
    return result.stdout

# One fixed record with a unique timestamp token. Other instants and opaque
# data must not be normalized when the top-level updated_at is changed.
base = json.loads((IDL / "test/corpus/accept.base.json").read_text())
base["spec"] = {"timestamp": "2026-09-11t12:00:00+02:00"}
(scratch / "base.json").write_text(json.dumps(base, separators=(",", ":")), encoding="utf-8")
shutil.copyfile(HERE / "cases.tsv", scratch / "cases.tsv")
token = "2026-08-20T05:07:15.134811Z"
expected = [line.split("\t")[0] + "\t" + line.split("\t")[2] for line in (HERE / "cases.tsv").read_text().splitlines()]

sources = {}
sources["go"] = r'''package main
import("os";"fmt";"strings";"encoding/json";rec "timestamp.test/generated/go/rec")
func invalidWrite(r *rec.Record) (bad bool) {defer func(){if e:=recover();e!=nil {v,ok:=e.(*rec.Refusal);bad=ok&&v.Word=="bad_timestamp"}}();rec.Encode(r);return}
func main(){base,_:=os.ReadFile("base.json");cases,_:=os.ReadFile("cases.tsv")
for _,line:=range strings.Split(strings.TrimSpace(string(cases)),"\n") {p:=strings.Split(line,"\t");input:=strings.Replace(string(base),"TOKEN",p[1],1);r,e:=rec.Decode([]byte(input));result:=""
if e!=nil {result=e.(*rec.Refusal).Word;v,_:=rec.Decode(base);v.UpdatedAt=p[1];if !invalidWrite(v){panic("invalid writer accepted")}} else {
encoded:=rec.Encode(r);again,e:=rec.Decode(encoded);if e!=nil{panic(e)};result=again.UpdatedAt
if !rec.MicrosTimestamp(result)||rec.MicrosTimestamp(p[1])!=(p[1]==result){panic("micros predicate")}
var doc map[string]json.RawMessage;json.Unmarshal(encoded,&doc);if !strings.Contains(string(doc["spec"]),"2026-09-11t12:00:00+02:00"){panic("opaque timestamp altered")}
if string(rec.Encode(again))!=string(encoded){panic("non-idempotent")}}
fmt.Println(p[0]+"\t"+result)}}
'''
sources["python"] = r'''import sys
sys.path.insert(0,"generated/py")
import rec
base=open("base.json","rb").read()
for line in open("cases.tsv"):
    name,value,expected=line.strip().split("\t")
    try: r=rec.decode(base.replace(b"TOKEN",value.encode(),1))
    except rec.Refusal as exc:
        result=exc.word
        v=rec.decode(base);v.updated_at=value
        try: rec.encode(v)
        except rec.Refusal as error: assert error.word=="bad_timestamp"
        else: raise AssertionError("invalid writer accepted")
    else:
        encoded=rec.encode(r);again=rec.decode(encoded);result=again.updated_at
        assert rec.micros_timestamp(result)
        assert rec.micros_timestamp(value)==(value==result)
        assert b"2026-09-11t12:00:00+02:00" in again.spec
        assert rec.encode(again)==encoded
    print(name+"\t"+result)
'''
sources["javascript"] = r'''import fs from 'node:fs';import * as rec from './generated/js/rec.mjs';
const base=fs.readFileSync('base.json','utf8');
for(const line of fs.readFileSync('cases.tsv','utf8').trim().split('\n')) {const [name,value]=line.split('\t');let r,result;
try{r=rec.decode(Buffer.from(base.replace('TOKEN',value)));}catch(e){if(!(e instanceof rec.Refusal))throw e;result=e.word;const v=rec.decode(Buffer.from(base));v.updated_at=value;let bad=false;try{rec.encode(v)}catch(e){bad=e instanceof rec.Refusal&&e.word==='bad_timestamp'}if(!bad)throw Error('invalid writer accepted');}
if(r){const encoded=rec.encode(r),again=rec.decode(encoded);result=again.updated_at;if(!rec.microsTimestamp(result)||rec.microsTimestamp(value)!==(value===result))throw Error('micros predicate');if(!Buffer.from(again.spec).toString().includes('2026-09-11t12:00:00+02:00'))throw Error('opaque altered');if(!Buffer.from(encoded).equals(Buffer.from(rec.encode(again))))throw Error('non-idempotent');}
console.log(name+'\t'+result);}
'''
sources["rust"] = r'''#[path="generated/rs/rec.rs"] mod rec;
fn main(){std::panic::set_hook(Box::new(|_|{}));let base=std::fs::read_to_string("base.json").unwrap();let cases=std::fs::read_to_string("cases.tsv").unwrap();
for line in cases.lines(){let p:Vec<_>=line.split('\t').collect();let input=base.replacen("TOKEN",p[1],1);let result=match rec::decode(input.as_bytes()) {
Err(e)=>{let mut v=rec::decode(base.as_bytes()).unwrap();v.updated_at=p[1].into();assert!(std::panic::catch_unwind(||rec::encode(&v)).is_err());e.word.to_owned()},
Ok(r)=>{let encoded=rec::encode(&r);let again=rec::decode(&encoded).unwrap();assert!(rec::micros_timestamp(&again.updated_at));assert_eq!(rec::micros_timestamp(p[1]),p[1]==again.updated_at);assert!(String::from_utf8_lossy(&again.spec).contains("2026-09-11t12:00:00+02:00"));assert_eq!(encoded,rec::encode(&again));again.updated_at}};println!("{}\t{}",p[0],result);}}
'''
sources["cpp"] = r'''#include "generated/cpp/rec.h"
#include <fstream>
#include <iostream>
#include <sstream>
#include <cassert>
int main(){std::ifstream b("base.json");std::string base((std::istreambuf_iterator<char>(b)),{});std::ifstream cases("cases.tsv");std::string line;
while(std::getline(cases,line)){std::istringstream row(line);std::string name,value,expected;std::getline(row,name,'\t');std::getline(row,value,'\t');std::getline(row,expected);std::string input=base;input.replace(input.find("TOKEN"),std::string("TOKEN").size(),value);std::string result;
try{auto r=rec::decode(input);auto encoded=rec::encode(r);auto again=rec::decode(encoded);result=again.updated_at;assert(rec::micros_timestamp(result));assert(rec::micros_timestamp(value)==(value==result));assert(again.spec.find("2026-09-11t12:00:00+02:00")!=std::string::npos);assert(rec::encode(again)==encoded);}
catch(const rec::Refusal&e){result=e.word;auto v=rec::decode(base);v.updated_at=value;bool bad=false;try{rec::encode(v);}catch(const rec::Refusal&e){bad=std::string(e.word)=="bad_timestamp";}assert(bad);}
std::cout<<name<<"\t"<<result<<"\n";}}
'''
languages = args.language or list(sources)
vcvars = Path(os.environ.get("VCVARS", "C:/Program Files/Microsoft Visual Studio/18/Community/VC/Auxiliary/Build/vcvars64.bat"))
executables = {"go": "go", "python": sys.executable, "javascript": "node", "rust": "rustc", "cpp": "c++"}
passed = []
for lang in languages:
    if lang == "cpp" and os.name == "nt":
        available = vcvars.is_file()
    else:
        available = shutil.which(executables[lang]) is not None
    if not available:
        if args.language:
            raise SystemExit(f"{lang}: requested toolchain unavailable")
        print(f"{lang}: UNPROVEN (toolchain unavailable)")
        continue
    run(["go", "run", ".", str(definition), str(scratch / "generated"), lang], IDL / "gen")
    extension = {"go": "go", "python": "py", "javascript": "mjs", "rust": "rs", "cpp": "cpp"}[lang]
    (scratch / ("driver." + extension)).write_text(sources[lang].replace("TOKEN", token), encoding="utf-8")
    if lang == "go":
        (scratch / "go.mod").write_text("module timestamp.test\n\ngo 1.22\n", encoding="utf-8")
        command = ["go", "run", "driver.go"]
    elif lang == "python": command = [sys.executable, "-B", "driver.py"]
    elif lang == "javascript": command = ["node", "driver.mjs"]
    elif lang == "rust":
        run(["rustc", "--edition=2021", "-A", "warnings", "driver.rs", "-o", "driver-rust.exe"])
        command = [str(scratch / "driver-rust.exe")]
    else:
        if os.name == "nt":
            (scratch / "build.cmd").write_text(f'@echo off\ncall "{vcvars}" >nul\ncl /nologo /std:c++20 /EHsc /utf-8 driver.cpp /Fe:driver-cpp.exe\n', encoding="utf-8")
            run(["cmd", "/c", "build.cmd"])
        else: run(["c++", "-std=c++20", "driver.cpp", "-o", "driver-cpp.exe"])
        command = [str(scratch / "driver-cpp.exe")]
    actual = run(command).splitlines()
    (scratch / (lang + "-results.txt")).write_text("\n".join(actual)+"\n", encoding="utf-8")
    if actual != expected:
        raise AssertionError(f"{lang} timestamp mismatch: {actual!r}")
    passed.append(lang)
    print(f"{lang}: {len(expected)} reader/writer cases passed")
if not passed: raise SystemExit("No timestamp backend was tested")
