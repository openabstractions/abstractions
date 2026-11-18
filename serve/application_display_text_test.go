package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// An announced context with a title past its display limit is shown
// shortened; the presence is applied, and the caller's value is unchanged.
func TestApplicationPresenceLongContextTitleFailsOpen(t *testing.T) {
	ctx := context.Background()
	state := privatetest.Dir(t)
	operator := rights.Subject{Account: "account", Program: filepath.Join(state, "operator.exe")}
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "app.exe")}
	d, err := openApplications(state, "account", func(_ context.Context, s rights.Subject, action, _ string) (bool, error) {
		if action == ActionApplicationManage {
			return s.Program == operator.Program, nil
		}
		return s.Program == app.Program, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := d.register(ctx, operator, wire.ApplicationDescriptor{Name: "editor", Program: app.Program, Title: "Editor"}); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	long := strings.Repeat("é", 200) // 400 bytes
	p := wire.ApplicationPresence{Application: "editor", LeaseMs: 1000, Contexts: []wire.ApplicationContext{{Name: "document", Title: long, Revision: "1"}}}
	if r := d.announce(ctx, app, p); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("a long context title refused the presence: %+v", r)
	}
	if p.Contexts[0].Title != long {
		t.Fatal("the caller's presence was changed")
	}
	page := d.observe(ctx, app, "", 0)
	got := page.Applications[0].Instances[0].Contexts[0].Title
	if len(got) > applicationTitleBytes || !utf8.ValidString(got) || !strings.HasPrefix(long, got) || len(got) < applicationTitleBytes-1 {
		t.Fatalf("shown title %d bytes %q", len(got), got)
	}
}

// A described command with a long or missing title is shown with a fitted
// title, and the description then passes the gateway's check.
func TestApplicationV3DescriptionLongCommandTitleFailsOpen(t *testing.T) {
	d := gatewayDescriptionFixture()
	d.Commands[0].Title = strings.Repeat("t", 300)
	d.Commands[0].Description = strings.Repeat("d", 5000)
	fitApplicationV3Description(d)
	if err := validateApplicationV3Description(d); err != nil {
		t.Fatal(err)
	}
	if len(d.Commands[0].Title) != applicationTitleBytes || len(d.Commands[0].Description) != applicationDescriptionBytes {
		t.Fatal(len(d.Commands[0].Title), len(d.Commands[0].Description))
	}
	d = gatewayDescriptionFixture()
	d.Commands[0].Title = ""
	fitApplicationV3Description(d)
	if err := validateApplicationV3Description(d); err != nil || d.Commands[0].Title != "node.x" {
		t.Fatal(d.Commands[0].Title, err)
	}
}
