package main

import (
	"strings"
	"testing"
)

// Every backend exercises the decoded-key rule, fieldless grant records and
// the existing last/preserve/refuse distinctions through actual readers.
func TestGrantDuplicateKeysFiveLanguages(t *testing.T) {
	for _, tc := range []struct {
		name, policy, duplicates, fields string
	}{
		{"grant", "grant", "refuse", `1: optional string id(omit="absent")`},
		{"fieldless", "grant", "refuse", ""},
		{"last", "grant", "last", `1: optional string id(omit="absent")`},
		{"preserve", "preserve", "refuse", `1: optional string id(omit="absent")`},
		{"closed", "refuse", "refuse", `1: optional string id(omit="absent")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := strings.Replace(head, `duplicate_keys = "refuse"`, `duplicate_keys = "`+tc.duplicates+`"`, 1)
			fixture += `struct Doc {` + tc.fields + `}(document="true",unknown_fields="` + tc.policy + `")`
			s := mustParse(t, fixture)
			cases := []struct{ input, want string }{
				{`{}`, "accept"},
				{`{"x":1,"x":2}`, "duplicate_key"},
				{`{"x":1,"\u0078":2}`, "duplicate_key"},
				{`{"__proto__":1,"constructor":2,"toString":3}`, "accept"},
				{`{"__proto__":1,"__proto__":2}`, "duplicate_key"},
				{`{"x":{"y":1,"y":2}}`, "duplicate_key"},
				{`{"x":[{"y":"\uD83D\uDE00"}]}`, "accept"},
				{`{"x":` + strings.Repeat("[", 70) + strings.Repeat("]", 70) + `}`, "depth_exceeded"},
				{`{"\ud800":1}`, "bad_string"},
			}
			inputs, wants := []string{}, []string{}
			for i, c := range cases {
				want := c.want
				if tc.policy == "refuse" && i > 0 && want != "bad_string" {
					want = "unknown_field"
				} else if tc.duplicates == "last" && want == "duplicate_key" {
					want = "accept"
				}
				inputs = append(inputs, c.input)
				wants = append(wants, want)
			}
			if tc.fields != "" {
				inputs = append(inputs, `{"id":"a","\u0069d":"b"}`)
				wants = append(wants, "duplicate_field")
			}
			dir := t.TempDir()
			for name, body := range map[string]string{
				"cases.txt": strings.Join(inputs, "\n") + "\n",
				"go.mod":    "module bound.test\n\ngo 1.22\n", "rec/rec.go": genGo(s),
				"rec.py": genPy(s), "rec.mjs": genJS(s), "rec.rs": genRust(s), "rec.h": genCpp(s),
				"main.go": grantKeysGo, "main.py": grantKeysPython, "main.mjs": grantKeysJS,
				"main.rs": grantKeysRust, "main.cpp": grantKeysCpp,
			} {
				writeNamespaceFile(t, dir, name, body)
			}
			runFiveConstraintPrograms(t, dir, strings.Join(wants, "\n")+"\n")
		})
	}
}

// [DEF-E8]: last applies to map entries; opaque JSON retains every member byte.
func TestLastOpaqueDuplicateMembersRoundTripFiveLanguages(t *testing.T) {
	s := mustParse(t, strings.Replace(head, `duplicate_keys = "refuse"`, `duplicate_keys = "last"`, 1)+
		`struct Doc {1:required json payload 2:required map<string,string> labels}(document="true",unknown_fields="refuse")`)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"cases.txt": `{"payload":{"a":1,"a":2},"labels":{"k":"first","k":"last"}}` + "\n",
		"go.mod":    "module bound.test\n\ngo 1.22\n", "rec/rec.go": genGo(s),
		"rec.py": genPy(s), "rec.mjs": genJS(s), "rec.rs": genRust(s), "rec.h": genCpp(s),
		"main.go": `package main
import("fmt";"os";r "bound.test/rec")
func main(){input,_:=os.ReadFile("cases.txt");v,e:=r.Decode(input);if e!=nil{panic(e)};if v.Labels["k"]!="last"{panic("map did not keep last")};fmt.Println(string(v.Payload));again,e:=r.Decode(r.Encode(v));if e!=nil{panic(e)};fmt.Println(string(again.Payload))}`,
		"main.py": `import rec
v=rec.decode(open('cases.txt','rb').read())
assert v.labels['k']=='last'
def raw(value): return value if isinstance(value,str) else value.decode()
print(raw(v.payload))
print(raw(rec.decode(rec.encode(v)).payload))
`,
		"main.mjs": `import * as r from './rec.mjs';import fs from 'node:fs';
const v=r.decode(fs.readFileSync('cases.txt'));if(v.labels.k!=='last')throw Error('map did not keep last');
const raw=value=>typeof value==='string'?value:new TextDecoder().decode(value);
console.log(raw(v.payload));console.log(raw(r.decode(r.encode(v)).payload));`,
		"main.rs": `mod rec;fn main(){let input=std::fs::read("cases.txt").unwrap();let v=rec::decode(&input).unwrap();assert_eq!(v.labels.get("k").unwrap(),"last");println!("{}",std::str::from_utf8(&v.payload).unwrap());let again=rec::decode(&rec::encode(&v)).unwrap();println!("{}",std::str::from_utf8(&again.payload).unwrap());}`,
		"main.cpp": `#include "rec.h"
#include <fstream>
#include <iostream>
#include <cassert>
int main(){std::ifstream f("cases.txt");std::string input((std::istreambuf_iterator<char>(f)),{});auto v=rec::decode(input);assert(v.labels.at("k")=="last");std::cout<<v.payload<<'\n';auto again=rec::decode(rec::encode(v));std::cout<<again.payload<<'\n';}`,
	} {
		writeNamespaceFile(t, dir, name, body)
	}
	runFiveConstraintPrograms(t, dir, "{\"a\":1,\"a\":2}\n{\"a\":1,\"a\":2}\n")
}

const grantKeysGo = `package main
import("fmt";"os";"strings";r "bound.test/rec")
func main(){b,_:=os.ReadFile("cases.txt");for _,line:=range strings.Split(strings.TrimSpace(string(b)),"\n"){_,e:=r.Decode([]byte(line));if e!=nil{fmt.Println(e.(*r.Refusal).Word)}else{fmt.Println("accept")}}}`

const grantKeysPython = `import rec
for line in open('cases.txt',encoding='utf-8').read().splitlines():
 try:rec.decode(line.encode());print('accept')
 except rec.Refusal as e:print(e.word)
`

const grantKeysJS = `import * as r from './rec.mjs';import fs from 'node:fs';for(const line of fs.readFileSync('cases.txt','utf8').trim().split('\n')){try{r.decode(Buffer.from(line));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}`

const grantKeysRust = `mod rec;fn main(){for line in std::fs::read_to_string("cases.txt").unwrap().lines(){match rec::decode(line.as_bytes()){Ok(_)=>println!("accept"),Err(e)=>println!("{}",e.word)}}}`

const grantKeysCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
int main(){std::ifstream f("cases.txt");std::string line;while(std::getline(f,line)){try{rec::decode(line);std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}}`
