package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A generated Go dispatcher answers a handler panic with handler_error
// "handler failed" and writes the service, method, panic value and stack to
// standard error. A panicking remote admission check does the same. The
// generated file imports runtime/debug and nothing else that reaches out.
func TestGeneratedDispatcherWritesHandlerPanicStack(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	code := genGo(s)
	var imports []string
	for _, line := range strings.Split(code, "\n") {
		if strings.HasPrefix(line, "import") {
			imports = append(imports, line)
		}
	}
	if !slices.Equal(imports, []string{`import "time"`, `import "runtime/debug"`}) {
		t.Fatalf("generated imports %q", imports)
	}
	if err := checkImports(emitted{def: s, lang: "go", imports: includeImports(backends[0], s), body: code}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"go.mod": "module reply.test\n\ngo 1.22\n", "rec.go": code, "rec_test.go": replyGoTest, "panic_test.go": handlerPanicGoTest} {
		if e = os.WriteFile(filepath.Join(dir, n), []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
	}
	c := exec.Command("go", "test", "-run", "^TestHandlerPanic$", "-count=1", ".")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	out, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}

const handlerPanicGoTest = `package rec
import("errors";"os";"os/exec";"strings";"testing")
type remoteQuery struct{*QueryDispatcher}
func(remoteQuery)RemoteMethodProfile(string,string)(string,bool){return "call",true}
func TestHandlerPanic(t *testing.T){
 if os.Getenv("HANDLER_PANIC_CHILD")=="1"{
  _,e:=NewQueryClient(&QueryDispatcher{Handler:&handler{}}).Fail("panic");var se *ServiceError
  if !errors.As(e,&se)||se.Code!=ServiceErrorCodeHandlerError||se.Message!="handler failed"{t.Fatalf("wire reply %#v",e)}
  frame:=[]byte("{\"version\":1,\"service\":\"example.query/query@1\",\"method\":\"Reset\",\"arguments\":{}}")
  b,e:=ServeEndpointRemote(frame,"p","v",func(string,string,string)error{panic(errors.New("admission broke"))},remoteQuery{&QueryDispatcher{Handler:&handler{}}});if e!=nil{t.Fatal(e)}
  _,e=serviceResponse(b,"example.query/query@1","Reset");if !errors.As(e,&se)||se.Code!=ServiceErrorCodeHandlerError||se.Message!="handler failed"{t.Fatalf("admission reply %#v",e)}
  return
 }
 c:=exec.Command(os.Args[0],"-test.run=^TestHandlerPanic$");c.Env=append(os.Environ(),"HANDLER_PANIC_CHILD=1")
 var stdout,stderr strings.Builder;c.Stdout,c.Stderr=&stdout,&stderr
 if e:=c.Run();e!=nil{t.Fatalf("child %v\n%s\n%s",e,stdout.String(),stderr.String())}
 log:=stderr.String()
 for _,want:=range []string{"example.query/query@1 Fail: handler panic: secret panic\n","goroutine ","(*handler).Fail(","example.query/query@1 Reset admission: handler panic: admission broke\n"}{
  if !strings.Contains(log,want){t.Fatalf("standard error lacks %q:\n%s",want,log)}
 }
 if strings.Contains(stdout.String(),"handler panic"){t.Fatalf("panic written to standard output:\n%s",stdout.String())}
}
`
