package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-facade/go-core/protected"
)

const ticket = "0123456789abcdef0123456789abcdef"

type recorded struct {
	ran      bool
	endpoint string
	servers  []listen.ServerExpectation
	op, tkt  string
}

func fakeEnv(r *recorded) helperEnv {
	image := `C:\Program Files\OpenAbstractions\tools\openabstractions-system.exe`
	return helperEnv{
		goos: "windows",
		self: func() (identity.User, error) {
			return identity.User{Kind: "windows", SID: "S-1-5-21-1-2-3-1001", Elevated: true, Integrity: "high", IntegrityRID: 0x3000}, nil
		},
		image:   func() (string, error) { return image, nil },
		trusted: func(string) error { return nil },
		servers: func(helper string, self identity.User) ([]listen.ServerExpectation, error) {
			return []listen.ServerExpectation{{Principal: identity.User{Kind: "windows", SID: self.SID}, Program: `C:\Program Files\OpenAbstractions\tools\openabstractionsw.exe`}}, nil
		},
		endpoint: func(service string) (string, error) { return `\\.\pipe\openabstractions-user-S-1-5-21-1-2-3-1001-` + service, nil },
		run: func(_ context.Context, endpoint string, servers []listen.ServerExpectation, op, tkt string, act protected.Act) error {
			r.ran, r.endpoint, r.servers, r.op, r.tkt = true, endpoint, servers, op, tkt
			if act == nil {
				return errors.New("no act")
			}
			return nil
		},
	}
}

// TestHelperCommandLine is FAC-H1's grammar: every other command line is
// refused before the helper reads its token or talks to anything.
func TestHelperCommandLine(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"security-log-page"},
		{"security-log-page", "--ticket"},
		{"security-log-page", "--ticket", ticket, "--endpoint", `\\.\pipe\x`},
		{"cmd.exe", "/c", "whoami"},
		{"security-log-page", "--ticket", "ZZ"},
		{"host", "sandbox-roots", "grant"},
	} {
		var r recorded
		env := fakeEnv(&r)
		env.self = func() (identity.User, error) { t.Fatal("read the token for a refused command line"); return identity.User{}, nil }
		var stderr bytes.Buffer
		if code := run(args, &stderr, env); code != exitUsage || r.ran {
			t.Fatalf("%q: exit %d ran %v", args, code, r.ran)
		}
	}
	var r recorded
	var stderr bytes.Buffer
	if code := run([]string{"security-log-page", "--ticket", ticket}, &stderr, fakeEnv(&r)); code != exitDone || !r.ran {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if r.op != protected.OperationSecurityLogPage || r.tkt != ticket || !strings.HasSuffix(r.endpoint, "-system-access-v1") || len(r.servers) != 1 {
		t.Fatalf("ran %+v", r)
	}
}

// TestHelperRefusesWithoutElevation is FAC-H1's own checks: an unelevated
// token, another platform, an untrusted copy and a runtime refusal each stop
// the helper before or instead of any act: exit 5 without elevation, 6 for a
// copy outside the machine-wide installation, 3 for the runtime's refusal.
func TestHelperRefusesWithoutElevation(t *testing.T) {
	notInstalled := map[string]bool{"untrusted copy": true, "no trusted runtime": true}
	cases := map[string]func(*helperEnv){
		"unelevated": func(e *helperEnv) {
			e.self = func() (identity.User, error) {
				return identity.User{Kind: "windows", SID: "S-1-5-21-1-2-3-1001", Integrity: "medium", IntegrityRID: 0x2000}, nil
			}
		},
		"elevated flag at medium integrity": func(e *helperEnv) {
			e.self = func() (identity.User, error) {
				return identity.User{Kind: "windows", SID: "S-1-5-21-1-2-3-1001", Elevated: true, IntegrityRID: 0x2000}, nil
			}
		},
		"token unreadable": func(e *helperEnv) {
			e.self = func() (identity.User, error) { return identity.User{}, errors.New("no token") }
		},
		"not Windows": func(e *helperEnv) { e.goos = "linux" },
		"untrusted copy": func(e *helperEnv) {
			e.trusted = func(string) error { return errors.New("owned by this account") }
		},
		"no trusted runtime": func(e *helperEnv) {
			e.servers = func(string, identity.User) ([]listen.ServerExpectation, error) { return nil, protected.ErrUnavailable }
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			var r recorded
			env := fakeEnv(&r)
			change(&env)
			var stderr bytes.Buffer
			want := exitNotElevated
			if notInstalled[name] {
				want = exitNotInstalled
			}
			if code := run([]string{"security-log-page", "--ticket", ticket}, &stderr, env); code != want || r.ran {
				t.Fatalf("exit %d want %d ran %v: %s", code, want, r.ran, stderr.String())
			}
		})
	}
	for want, err := range map[int]error{
		exitRefused:  protected.ErrRefused,
		exitActError: errors.New("protected: the act reported denied"),
	} {
		var r recorded
		env := fakeEnv(&r)
		env.run = func(context.Context, string, []listen.ServerExpectation, string, string, protected.Act) error { return err }
		var stderr bytes.Buffer
		if code := run([]string{"security-log-page", "--ticket", ticket}, &stderr, env); code != want {
			t.Fatalf("%v: exit %d want %d", err, code, want)
		}
	}
}
