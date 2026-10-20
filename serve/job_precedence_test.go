package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
)

// JOB-A13 (research/wake-and-inference-rights/REPORT.md recommendation 6):
// an explicit deny wins. Under a contract-wide submit permit, a deny on the
// download kind refuses downloads; the permit alone admits them and reads
// the kind's rule without asking the person anything.
func TestAKindDenyWinsOverTheContractWidePermit(t *testing.T) {
	options := gatedRuntime(t)
	bin := probeBinary(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("precedence\n")) }))
	defer server.Close()
	grant := func(args ...string) {
		t.Helper()
		reply, err := runRightsCommand(t, append([]string{"grant", "--endpoint", options.endpoint, "--program", bin,
			"--action", host.JobSubmitAction}, args...)...)
		if err != nil || reply.Outcome != "applied" {
			t.Fatalf("grant %v: %v %+v", args, err, reply)
		}
	}
	grant("--resource", host.JobResource)
	if accepted, _ := probeAs(t, bin, options, "jobs", "submit", server.URL+"/permitted.bin"); accepted.Outcome != "accepted" {
		t.Fatalf("a download under the contract-wide permit %+v", accepted)
	}
	if pending := pendingFirstUse(t, options); len(pending) != 0 {
		t.Fatalf("reading the kind's rule under a permit asked %+v", pending)
	}
	grant("--resource", host.JobKindResource("download"), "--deny")
	if refused, _ := probeAs(t, bin, options, "jobs", "submit", server.URL+"/denied.bin"); refused.Outcome != "forbidden" {
		t.Fatalf("a download denied by its kind under the contract-wide permit %+v", refused)
	}
}
