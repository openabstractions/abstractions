package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const maxEncodedFixture = `struct Doc {
 1: required string value(length="..128",pattern="no_control")
}(document="true",unknown_fields="refuse",max_encoded="32")
struct Outer {1: required Doc child}(unknown_fields="refuse")
struct Two {1: required Outer child}(unknown_fields="refuse")
struct Three {1: required Two child}(unknown_fields="refuse")
struct Empty {}(unknown_fields="refuse")
struct Big {1:required list<Empty> items(length="..200000")}(unknown_fields="refuse",max_encoded="1048576")
`

func maxEncodedHead() string {
	return strings.Replace(constraintHead, "  12: trailing_bytes", "  12: encoded_size(stage=\"structure\")\n  13: trailing_bytes", 1)
}

func TestMaxEncodedValidationAndParentProof(t *testing.T) {
	base := maxEncodedHead() + maxEncodedFixture
	for _, text := range []string{"0", "-1", "01", "+1", "1048577", "9223372036854775808", "1.0"} {
		if _, err := parse(strings.Replace(base, `max_encoded="32"`, `max_encoded="`+text+`"`, 1)); err == nil {
			t.Errorf("invalid ceiling %s accepted", text)
		}
	}
	if _, err := parse(constraintHead + maxEncodedFixture); err == nil || !strings.Contains(err.Error(), "encoded_size") {
		t.Fatalf("missing typed size refusal accepted: %v", err)
	}
	if _, err := parse(strings.Replace(base, `max_encoded="32"`, `max_encoded="32",chunk="true"`, 1)); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("aggregate ceiling silently replaced stream chunk semantics: %v", err)
	}
	large := maxEncodedHead() + `struct Child {1:required string value(length="..2000000")}(unknown_fields="refuse",max_encoded="262144")
struct Parent {1:required list<Child> children(length="..64")}(document="true",unknown_fields="refuse")`
	if _, err := parse(large); err == nil || !strings.Contains(err.Error(), "record Parent encodes") {
		t.Fatalf("parent inherited child exemption: %v", err)
	}
	bounded := strings.Replace(large, `document="true",unknown_fields="refuse"`, `document="true",unknown_fields="refuse",max_encoded="1048576"`, 1)
	s := mustParse(t, bounded)
	if got := s.recordSize(s.Struct("Parent"), 0, "Parent", map[*Struct]bool{}); got.open != "" || got.bytes <= recordCeiling {
		t.Fatal("compact annotation replaced physical proof", got)
	}
	without := strings.ReplaceAll(bounded, `,max_encoded="262144"`, "")
	if _, err := parse(without); err == nil || !strings.Contains(err.Error(), "record Child encodes") {
		t.Fatalf("unannotated oversized child accepted: %v", err)
	}
	if !strings.Contains(genDocs(mustParse(t, base)), "compact value span at most 32 bytes") {
		t.Fatal("aggregate ceiling absent from generated schema prose")
	}
}

func TestMaxEncodedCompactChildDoesNotProvePhysicalParent(t *testing.T) {
	fixture := maxEncodedHead() + `struct Child {1:required string value(length="1..1",pattern="ascii_name")}(unknown_fields="refuse",max_encoded="13")
struct Parent {1:required list<Child> children(length="..40000")}(document="true",unknown_fields="refuse")`
	if _, err := parse(fixture); err == nil || !strings.Contains(err.Error(), "record Parent encodes") {
		t.Fatalf("compact child ceiling admitted an oversized physical parent: %v", err)
	}
	child := struct {
		Value string `json:"value"`
	}{"x"}
	children := make([]struct {
		Value string `json:"value"`
	}, 40000)
	for i := range children {
		children[i] = child
	}
	physical, err := json.MarshalIndent(struct {
		Children any `json:"children"`
	}{children}, "", "  ")
	if err != nil || len(physical) != 1280022 || len(physical) <= recordCeiling {
		t.Fatal("physical counterexample changed", len(physical), err)
	}
	// The rejected propagation of the 13-byte child cap computed this false proof.
	if falseProof := 20 + 6 + 40000*(13+6); falseProof != 760026 || falseProof > recordCeiling {
		t.Fatal("false proof fixture changed", falseProof)
	}
}

func TestMaxEncodedCompatibility(t *testing.T) {
	base := maxEncodedHead() + maxEncodedFixture + `service Echo {Doc Read()}(wire_name="example/echo@1")`
	for _, change := range []struct {
		old, next  string
		compatible bool
	}{
		{"", `,max_encoded="32"`, false},
		{`,max_encoded="32"`, `,max_encoded="31"`, false},
		{`,max_encoded="32"`, `,max_encoded="33"`, true},
		{`,max_encoded="32"`, "", true},
	} {
		old := strings.Replace(base, `,max_encoded="32"`, change.old, 1)
		next := strings.Replace(base, `,max_encoded="32"`, change.next, 1)
		if got := len(compatProblems(t, old, next)) == 0; got != change.compatible {
			t.Errorf("compatibility %q -> %q: got %v", change.old, change.next, got)
		}
		if definitionHashes(mustParse(t, old))[0][1] == definitionHashes(mustParse(t, next))[0][1] {
			t.Error("ceiling change did not enter definition hash")
		}
	}
}

func TestMaxEncodedFiveLanguages(t *testing.T) {
	fixture := strings.Replace(maxEncodedHead(), `depth_limit    = "64"`, `depth_limit    = "8"`, 1) + maxEncodedFixture
	s := mustParse(t, fixture)
	canonical := func(value string) string { return "{\n  \"value\": \"" + value + "\"\n}" }
	at := canonical(strings.Repeat("a", 20))
	if len(at) != 37 {
		t.Fatal("boundary fixture changed", len(at))
	}
	cases := []struct{ input, want string }{
		{at + "\n", "accept"},
		{canonical(strings.Repeat("a", 21)) + "\n", "encoded_size"},
		{strings.Repeat(" ", 64) + at + "\n\t  ", "accept"},
		{canonical(strings.Repeat("é", 10)) + "\n", "accept"},
		{canonical(strings.Repeat("é", 10)+"a") + "\n", "encoded_size"},
		{`{"value":"` + strings.Repeat(`\u00e9`, 10) + `"}`, "encoded_size"},
		{`{"value":false,"padding":"` + strings.Repeat("a", 20) + `"}`, "encoded_size"},
		{`{"value":false}`, "wrong_type"},
		{`{"value":"","value":""}`, "duplicate_field"},
		{`{"value":"","x":{"y":1,"y":2}}`, "unknown_field"},
		{`{"value":"\ud800"}`, "bad_string"},
		{`{"value":"\u0001"}`, "bad_value"},
		{`{"value":"\"}["}`, "accept"},
		{`{"value":` + strings.Repeat("[", 9) + strings.Repeat("]", 9) + `}`, "depth_exceeded"},
		{`{"value":"` + strings.Repeat("a", 40), "encoded_size"},
		{canonical(strings.Repeat(" ", 20)), "accept"},
		{canonical(strings.Repeat(" ", 21)), "encoded_size"},
	}
	compactNear := `{"value":"` + strings.Repeat("a", 19) + `"}`
	for _, rawBytes := range []int{recordCeiling - 1, recordCeiling, recordCeiling + 1} {
		padded := `{"value":` + strings.Repeat(" ", rawBytes-len(compactNear)) + `"` + strings.Repeat("a", 19) + `"}`
		want := "accept"
		if rawBytes > recordCeiling {
			want = "encoded_size"
		}
		if len(padded) != rawBytes {
			t.Fatal("raw-span fixture changed")
		}
		cases = append(cases, struct{ input, want string }{padded, want})
	}
	dir := t.TempDir()
	names, wants := []string{}, []string{}
	for i, c := range cases {
		name := fmt.Sprintf("case-%d.json", i)
		writeNamespaceFile(t, dir, name, c.input)
		names, wants = append(names, name), append(wants, c.want)
	}
	// Same near-limit value passes standalone and three levels deep. Physical
	// whitespace grows with nesting, while the compact count stays unchanged.
	wants = append(wants, "accept", "accept", "encoded_size", "accept", "encoded_size", "accept", "encoded_size", "nested accept", "nested accept", "encoded_size", "big accept", "encoded_size")
	for name, body := range map[string]string{
		"cases.txt": strings.Join(names, "\n") + "\n", "go.mod": "module bound.test\n\ngo 1.22\n",
		"rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "rec.mjs": genJS(s),
		"main.go": maxEncodedDriverGo, "main.py": maxEncodedDriverPython, "main.cpp": maxEncodedDriverCpp,
		"main.rs": maxEncodedDriverRust, "main.mjs": maxEncodedDriverJS,
	} {
		writeNamespaceFile(t, dir, name, body)
	}
	runFiveConstraintPrograms(t, dir, strings.Join(wants, "\n")+"\n")
}

const maxEncodedDriverGo = `package main
import("fmt";"os";"strings";r "bound.test/rec")
func word(e error)string{if e==nil{return "accept"};return e.(*r.Refusal).Word}
func encode(value string){defer func(){if e:=recover();e!=nil{fmt.Println(e.(*r.Refusal).Word)}}(); b:=r.Encode(&r.Doc{Value:value});if len(b)!=len(value)+18||b[len(b)-1]!='\n'{panic("value/LF count")};fmt.Println("accept")}
func nested(value string){b,e:=r.ToJSON(r.Three{Child:r.Two{Child:r.Outer{Child:r.Doc{Value:value}}}});if e!=nil{fmt.Println(word(e));return};var v r.Three;if e=r.FromJSON(b,&v);e!=nil{panic(e)};fmt.Println("nested accept")}
func big(n int){b,e:=r.ToJSON(r.Big{Items:make([]r.Empty,n)});if e!=nil{fmt.Println(word(e));return};if n==200000{panic("raw encoder guard missing")};var v r.Big;if e=r.FromJSON(b,&v);e!=nil{panic(e)};fmt.Println("big accept")}
func main(){b,_:=os.ReadFile("cases.txt");for _,name:=range strings.Fields(string(b)){data,_:=os.ReadFile(name);_,e:=r.Decode(data);fmt.Println(word(e))};for _,v:=range []string{strings.Repeat("a",19),strings.Repeat("a",20),strings.Repeat("a",21),strings.Repeat("é",10),strings.Repeat("é",10)+"a",strings.Repeat(" ",20),strings.Repeat(" ",21)}{encode(v)};for _,n:=range []int{19,20,21}{nested(strings.Repeat("a",n))};big(100000);big(200000)}`

const maxEncodedDriverPython = `import rec
for name in open('cases.txt').read().splitlines():
 try: rec.decode(open(name,'rb').read());print('accept')
 except rec.Refusal as e: print(e.word)
for value in ['a'*19,'a'*20,'a'*21,'é'*10,'é'*10+'a',' '*20,' '*21]:
 try:
  data=rec.encode(rec.Doc(value=value));assert len(data)==len(value.encode())+18 and data[-1:]==b'\n';print('accept')
 except rec.Refusal as e: print(e.word)
for value in ['a'*19,'a'*20,'a'*21]:
 try:
  data=rec.to_json(rec.Three(child=rec.Two(child=rec.Outer(child=rec.Doc(value=value)))));rec.from_json(rec.Three,data);print('nested accept')
 except rec.Refusal as e: print(e.word)
for n in [100000,200000]:
 try:
  data=rec.to_json(rec.Big(items=[rec.Empty()]*n))
  if n==200000: raise RuntimeError('raw encoder guard missing')
  rec.from_json(rec.Big,data);print('big accept')
 except rec.Refusal as e: print(e.word)
`

const maxEncodedDriverJS = `import * as r from './rec.mjs';import fs from 'node:fs';
for(const name of fs.readFileSync('cases.txt','utf8').trim().split('\n')){try{r.decode(fs.readFileSync(name));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}
for(const value of ['a'.repeat(19),'a'.repeat(20),'a'.repeat(21),'é'.repeat(10),'é'.repeat(10)+'a',' '.repeat(20),' '.repeat(21)]){try{const b=r.encode({value});if(b.length!==new TextEncoder().encode(value).length+18||b.at(-1)!==10)throw Error('value/LF count');console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}
for(const value of ['a'.repeat(19),'a'.repeat(20),'a'.repeat(21)]){try{const b=r.toJson('Three',{child:{child:{child:{value}}}});r.fromJson('Three',b);console.log('nested accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}
for(const n of [100000,200000]){try{const b=r.toJson('Big',{items:Array(n).fill({})});if(n===200000)throw Error('raw encoder guard missing');r.fromJson('Big',b);console.log('big accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}`

const maxEncodedDriverCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
#include <sstream>
int main(){std::ifstream names("cases.txt");std::string name;while(std::getline(names,name)){std::ifstream f(name);std::stringstream b;b<<f.rdbuf();try{rec::decode(b.str());std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}
for(const auto& value:{std::string(19,'a'),std::string(20,'a'),std::string(21,'a'),std::string("éééééééééé"),std::string("ééééééééééa"),std::string(20,' '),std::string(21,' ')}){try{rec::Doc v;v.value=value;auto b=rec::encode(v);if(b.size()!=value.size()+18||b.back()!='\n')return 2;std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}
for(int n:{19,20,21}){try{rec::Three v;v.child.child.child.value=std::string(n,'a');auto b=rec::to_json(v);rec::from_json<rec::Three>(b);std::cout<<"nested accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}
for(int n:{100000,200000}){try{rec::Big v;v.items.resize(n);auto b=rec::to_json(v);if(n==200000)return 3;rec::from_json<rec::Big>(b);std::cout<<"big accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}}`

const maxEncodedDriverRust = `mod rec;use rec::JsonRecord;
fn main(){std::panic::set_hook(Box::new(|_|{}));for name in std::fs::read_to_string("cases.txt").unwrap().lines(){let b=std::fs::read(name).unwrap();match rec::decode(&b){Ok(_)=>println!("accept"),Err(e)=>println!("{}",e.word)}}
for value in ["a".repeat(19),"a".repeat(20),"a".repeat(21),"é".repeat(10),"é".repeat(10)+"a"," ".repeat(20)," ".repeat(21)]{let len=value.len();match std::panic::catch_unwind(||rec::encode(&rec::Doc{value})){Ok(b)=>{assert_eq!(b.len(),len+18);assert_eq!(*b.last().unwrap(),b'\n');println!("accept")},Err(e)=>println!("{}",e.downcast_ref::<rec::Refusal>().unwrap().word)}}
for n in [19,20,21]{match std::panic::catch_unwind(||rec::Three{child:rec::Two{child:rec::Outer{child:rec::Doc{value:"a".repeat(n)}}}}.to_json()){Ok(b)=>{rec::Three::from_json(&b).unwrap();println!("nested accept")},Err(e)=>println!("{}",e.downcast_ref::<rec::Refusal>().unwrap().word)}}
for n in [100000,200000]{match std::panic::catch_unwind(||rec::Big{items:(0..n).map(|_|rec::Empty{}).collect()}.to_json()){Ok(b)=>{assert_ne!(n,200000,"raw encoder guard missing");rec::Big::from_json(&b).unwrap();println!("big accept")},Err(e)=>println!("{}",e.downcast_ref::<rec::Refusal>().unwrap().word)}}}`
