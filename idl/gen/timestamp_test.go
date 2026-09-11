package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Keep the counterexamples in the normal generator test suite, including the
// field-to-writer dispatch. The separate timestamp runner exercises all five
// backends against the same compatibility vectors.
func TestGeneratedTimestampCompatibility(t *testing.T) {
	def := exampleDefinition(t)
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("rec.go", []byte(genGo(def)))
	write("go.mod", []byte("module timestamp.test\n\ngo 1.22\n"))
	for name, path := range map[string]string{"cases.tsv": "../test/timestamps/cases.tsv", "base.json": "../test/corpus/accept.base.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		write(name, data)
	}
	write("rec_test.go", []byte(timestampGoTest))
	command := exec.Command("go", "test", ".")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated timestamp checks: %v\n%s", err, output)
	}
}

const timestampGoTest = `package rec
import("os";"strings";"testing")
func TestTimestampVectors(t *testing.T) {
 base,err:=os.ReadFile("base.json");if err!=nil{t.Fatal(err)}
 cases,err:=os.ReadFile("cases.tsv");if err!=nil{t.Fatal(err)}
 for _,line:=range strings.Split(strings.TrimSpace(string(cases)),"\n") {
  p:=strings.Split(line,"\t")
  t.Run(p[0],func(t *testing.T){
   input:=strings.Replace(string(base),"2026-08-20T05:07:15.134811Z",p[1],1)
   r,err:=Decode([]byte(input))
   if p[2]=="bad_timestamp" {
    if err==nil{t.Fatal("invalid timestamp accepted")}
    refusal,ok:=err.(*Refusal);if !ok||refusal.Word!="bad_timestamp"{t.Fatalf("wrong refusal: %v",err)}
    v,_:=Decode(base);v.UpdatedAt=p[1]
    defer func(){e:=recover();f,ok:=e.(*Refusal);if !ok||f.Word!="bad_timestamp"{t.Fatalf("invalid encoder: %v",e)}}()
    Encode(v);return
   }
   if err!=nil{t.Fatal(err)}
   encoded:=Encode(r);again,err:=Decode(encoded);if err!=nil{t.Fatal(err)}
   if again.UpdatedAt!=p[2]{t.Fatalf("want %s, got %s",p[2],again.UpdatedAt)}
   if !MicrosTimestamp(again.UpdatedAt)||MicrosTimestamp(p[1])!=(p[1]==p[2]){t.Fatal("write grammar predicate")}
   if string(Encode(again))!=string(encoded){t.Fatal("encoding not idempotent")}
  })
 }
}
`
