package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveInvalidWordUsesDeclaredErrorChannel(t *testing.T) {
	for _, tc := range []struct{ profile, codes, want string }{
		{"1", `error_codes="errors",`, "invalid_request"},
		{"2", "", "invalid_request"},
		{"2", `error_codes="errors",`, "invalid"},
	} {
		t.Run(tc.profile+tc.want, func(t *testing.T) {
			d, err := parse(constraintHead + `const list<string> errors = ["invalid"]
enum Outcome {1:invalid 2:unchanged}(unknown="refuse",reader="act")
struct Page {1:required string next 2:required Outcome outcome}(document="true",unknown_fields="refuse")
service Watch {Page Observe(1:string cursor,2:i64 wait_ms(range="0..30000"))(observe="cursor")}(wire_name="test/watch@` + tc.profile + `",` + tc.codes + `doc="Bounded observation.")`)
			if err != nil {
				t.Fatal(err)
			}
			svc := d.Services[0]
			m := svc.Methods[0]
			for _, lang := range []string{"go", "python", "javascript", "rust", "cpp"} {
				var b strings.Builder
				switch lang {
				case "go":
					goObserve(&b, d, svc, m)
				case "python":
					pyObserve(&b, d, svc, m)
				case "javascript":
					jsObserve(&b, d, svc, m)
				case "rust":
					rsObserve(&b, d, svc)
				case "cpp":
					cppObserve(&b, d, svc)
				}
				got := b.String()
				if lang == "python" || lang == "javascript" || lang == "cpp" {
					if strings.Contains(got, `ServiceError("invalid",`) != (tc.want == "invalid") {
						t.Fatal(lang, got)
					}
				} else if !strings.Contains(got, `"`+tc.want+`"`) {
					t.Fatal(lang, got)
				}
			}
			// The real Go iterator refuses before transport creation or dispatch.
			dir := t.TempDir()
			writeNamespaceFile(t, dir, "go.mod", "module observe.word.test\n\ngo 1.26\n")
			writeNamespaceFile(t, dir, "rec.go", genGo(d))
			writeNamespaceFile(t, dir, "rec_test.go", fmt.Sprintf(`package rec
import "testing"
func TestInvalidWait(t *testing.T){c:=NewWatchClient(nil);count:=0;for _,err:=range c.ObservePages("",30001){count++;e,ok:=err.(*ServiceError);if !ok||string(e.Code)!=%q{t.Fatal(err)}};if count!=1{t.Fatal(count)}}
`, tc.want))
			cmd := exec.Command("go", "test", ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK=off")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			run := func(argv ...string) {
				t.Helper()
				c := exec.Command(argv[0], argv[1:]...)
				c.Dir = dir
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("%v: %v\n%s", argv, err, out)
				}
			}
			writeNamespaceFile(t, dir, "rec.py", genPy(d))
			writeNamespaceFile(t, dir, "test.py", fmt.Sprintf(`import rec
try:
 rec.WatchClient(None).observe_pages('',30001)
 raise AssertionError('invalid wait dispatched')
except rec.ServiceError as e:
 assert %q == 'invalid' and e.code == 'invalid'
except ValueError:
 assert %q == 'invalid_request'
`, tc.want, tc.want))
			run(os.Getenv("PYTHON"), "test.py")
			writeNamespaceFile(t, dir, "rec.mjs", genJS(d))
			writeNamespaceFile(t, dir, "test.mjs", fmt.Sprintf(`import assert from 'node:assert/strict';import * as r from './rec.mjs';
assert.throws(()=>new r.WatchClient(null).observePages('',30001n),e=>%q==='invalid'?e instanceof r.ServiceError&&e.code==='invalid':e instanceof RangeError);
`, tc.want))
			run("node", "test.mjs")
			writeNamespaceFile(t, dir, "rec.h", genCpp(d))
			writeNamespaceFile(t, dir, "test.cpp", fmt.Sprintf(`#include "rec.h"
#include <cassert>
struct Transport{std::string exchange_frame(std::string_view){throw std::runtime_error("invalid wait dispatched");}};
int main(){Transport t;rec::WatchClient<Transport> c(t);try{c.observe_pages("",30001);assert(false);}catch(const rec::ServiceError& e){assert(std::string(%q)=="invalid"&&e.code=="invalid");}catch(const std::invalid_argument&){assert(std::string(%q)=="invalid_request");}}
`, tc.want, tc.want))
			run("g++", "-std=c++17", "test.cpp", "-o", "cpp-probe.exe")
			run(filepath.Join(dir, "cpp-probe.exe"))
			writeNamespaceFile(t, dir, "rec.rs", genRust(d))
			writeNamespaceFile(t, dir, "main.rs", fmt.Sprintf(`mod rec;use rec::*;
struct Transport;impl FrameTransport for Transport{type Error=&'static str;fn write_frame(&self,_:&[u8])->Result<(),Self::Error>{unreachable!()}fn exchange_frame(&self,_:&[u8])->Result<Vec<u8>,Self::Error>{unreachable!()}}
fn main(){let c=WatchClient::new(Transport);match c.observe_pages(String::new(),30001).next().unwrap(){Err(CallError::Dispatch(code))=>assert_eq!(code,%q),_=>panic!("invalid wait dispatched")};}
`, tc.want))
			run(rustServiceCompiler(t), "--edition=2021", "main.rs", "-o", "rust-probe.exe")
			run(filepath.Join(dir, "rust-probe.exe"))
		})
	}
}
