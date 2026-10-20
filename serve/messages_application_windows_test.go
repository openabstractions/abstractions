//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestMessageApplicationFamilyKeepsNativeImageAndRegistrationBoundaries(t *testing.T) {
	base := "oa-claude-integration"
	a, b := base+"-"+strings.Repeat("a", 32), base+"-"+strings.Repeat("b", 32)
	f := messagesObserveFixtureNamed(t, a, "current-a.exe")
	// Cross-platform family spelling is injected; all registration, image and
	// person Keep checks remain the actual native directory owners.
	family := func(name string) string {
		if name == base || name == a || name == b {
			return base
		}
		return name
	}
	image := func(leaf string) string {
		switch leaf {
		case "current-a.exe":
			return a
		case "current-b.exe":
			return b
		}
		return ""
	}
	resolve := func(name string) (string, error) {
		return f.run.d.resolveMessageApplicationWith(f.run.ctx, name, family, image)
	}
	if got, err := resolve(base); err != nil || got != a {
		t.Fatal(got, err)
	}
	raw, err := os.ReadFile(f.selfProgram)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "current-b.exe")
	if err := os.WriteFile(program, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, f.run, b, identity.NormalizeSubjectProgram(program))
	if got, err := resolve(base); err != nil || got != a {
		t.Fatal("registration supplied Keep", got, err)
	}
	agent1478Set(t, f.run, b, "", true)
	answer(t, f.run.f, agent1478Question(t, f.run, b).ID, "keep")
	agent1478Wait(t, f.run, b, "enrolled")
	if got, err := resolve(base); got != "" || !errors.Is(err, errMessageApplicationAmbiguous) {
		t.Fatal("family collision selected a receiver", got, err)
	}
	old := f.run.d.currentAgentEnrollmentImage(f.run.ctx, a, nil)
	if old.Enrollment == nil {
		t.Fatal(old)
	}
	agent1478Set(t, f.run, a, old.Enrollment.Revision, false)
	if got, err := resolve(base); err != nil || got != b {
		t.Fatal("current replacement not selected", got, err)
	}
	if got, err := resolve(a); err != nil || got != a {
		t.Fatal("registered unkept exact name became absence", got, err)
	}
	// Replace the file identity retained by Keep. The inert image reader used
	// by this fixture deliberately validates file identity rather than signing.
	if err := os.Rename(program, program+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, raw, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := resolve(base); got != "" || !errors.Is(err, errMessageApplicationUnavailable) {
		t.Fatal("changed native image became usable/absent", got, err)
	}
	if got, err := resolve("unregistered-exact"); got != "" || !errors.Is(err, errMessageApplicationUnknown) {
		t.Fatal(got, err)
	}
	current := f.run.d.currentAgentEnrollmentImage(f.run.ctx, "opencode", nil)
	if current.Outcome == applications.OutcomePage && current.Enrollment != nil && current.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
		t.Fatal("stock inherited enrollment", current)
	}
}
