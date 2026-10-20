package main

import (
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
)

// TestTheJobDrainFitsTheSupervisorsShutdownWait holds RUN-U3 against RUN-S4:
// the runtime drains its jobs inside the time the supervisor waits before it
// forces the child, with a second left for the other services to close, and
// both fit inside the service manager's stop wait.
func TestTheJobDrainFitsTheSupervisorsShutdownWait(t *testing.T) {
	if host.JobDrainBound+time.Second > runtimeShutdownWait {
		t.Fatalf("the job drain (%s) leaves under a second of the supervisor's %s shutdown wait", host.JobDrainBound, runtimeShutdownWait)
	}
	if runtimeShutdownWait+runtimeForceWait > hostStopWait {
		t.Fatalf("the child's shutdown (%s) and force (%s) waits exceed the service stop wait %s", runtimeShutdownWait, runtimeForceWait, hostStopWait)
	}
}
