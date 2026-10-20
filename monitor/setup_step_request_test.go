package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The presenter's setup-request read outlasts the runtime's hold. The
// transport's default 5 s budget ended every read with "context deadline
// exceeded" before the runtime answered (R24, every 35 s).
func TestSetupRequestReadOutlastsTheRuntimesHold(t *testing.T) {
	if got := panelApplicationsTransport(listen.FrameClient{Endpoint: "x"}).Timeout; got <= setupRequestHold {
		t.Fatalf("the read's budget %v ends before the runtime's hold of %v", got, setupRequestHold)
	}
}

func TestSetupFinishFlags(t *testing.T) {
	for _, test := range []struct {
		name       string
		finish     bool
		flags      []string
		positional int
		valid      bool
	}{
		{"old bundle", true, []string{"finish"}, 0, true},
		{"upgrade", true, []string{"finish", "update", "native", "quiet"}, 0, true},
		{"ordinary setup", false, []string{"setup", "setup-use"}, 0, true},
		{"update alone", false, []string{"update"}, 0, false},
		{"update with setup", false, []string{"setup", "update"}, 0, false},
		{"finish with runtime", true, []string{"finish", "runtime-program"}, 0, false},
		{"finish positional", true, []string{"finish"}, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := setupFinishFlags(test.finish, test.flags, test.positional); (err == nil) != test.valid {
				t.Fatalf("validation = %v, valid %v", err, test.valid)
			}
		})
	}
}

// The presenter starts the step once per request, then names that request
// in its next read, which clears it in the runtime. A refused read waits
// before the next instead of spinning.
func TestSetupRequestWatchStartsTheStepOncePerRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := applications.SetupRequest{ID: "r1", Use: "code", Application: "OpenCode"}
	var afters []string
	replies := []applications.SetupRequestRead{
		{Outcome: applications.OutcomeForbidden},
		{Outcome: applications.OutcomePage, Request: &request},
		{Outcome: applications.OutcomePage, Request: &request},
		{Outcome: applications.OutcomePage},
	}
	read := func(_ context.Context, after string) (applications.SetupRequestRead, error) {
		afters = append(afters, after)
		if len(afters) > len(replies) {
			cancel()
			return applications.SetupRequestRead{}, errors.New("closed")
		}
		return replies[len(afters)-1], nil
	}
	var launched []applications.SetupRequest
	pauses := 0
	watchSetupRequests(ctx, read, func(r applications.SetupRequest) error { launched = append(launched, r); return nil },
		func(ctx context.Context) error { pauses++; return ctx.Err() })
	if len(launched) != 1 || launched[0] != request {
		t.Fatalf("launched %+v", launched)
	}
	if strings.Join(afters, ",") != ",,r1,r1,r1" || pauses != 2 {
		t.Fatalf("reads after %q, pauses %d", afters, pauses)
	}
}

// -setup-use and -setup-app come together, with a known use and a display
// name without control characters.
func TestSetupRequesterFlags(t *testing.T) {
	if r, err := stepRequesterFlags("", "", ""); r != nil || err != nil {
		t.Fatalf("no request = %+v, %v", r, err)
	}
	if r, err := stepRequesterFlags("code", "OpenCode", "r1"); err != nil || *r != (setup.StepRequester{App: "OpenCode", Use: "code", Request: "r1"}) {
		t.Fatalf("request = %+v, %v", r, err)
	}
	if r, err := stepRequesterFlags("video", "OpenCode", "r2"); err != nil || *r != (setup.StepRequester{App: "OpenCode", Use: "video", Request: "r2"}) {
		t.Fatalf("video request = %+v, %v", r, err)
	}
	for _, c := range [][2]string{{"code", ""}, {"", "OpenCode"}, {"everything", "OpenCode"}, {"image", "Comfy\nUI"}, {"image", strings.Repeat("a", 129)}} {
		if _, err := stepRequesterFlags(c[0], c[1], "r1"); err == nil {
			t.Fatalf("%q was accepted", c)
		}
	}
	for _, request := range []string{"", strings.Repeat("x", 65), "bad\nrequest"} {
		if _, err := stepRequesterFlags("code", "An app", request); err == nil {
			t.Fatal("invalid request id accepted", request)
		}
	}
	if err := launchSetupStepFor(applications.SetupRequest{ID: "x", Use: "code", Application: "Open\x00Code"}); err == nil {
		t.Fatal("a control character reached the command line")
	}
}
