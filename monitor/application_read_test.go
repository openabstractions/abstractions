package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// applicationReadFake records the read rules the confirm handlers write.
type applicationReadFake struct {
	allowed, cleared []string
	err              error
}

func (f *applicationReadFake) allowPanelRead(_ context.Context, name string) error {
	f.allowed = append(f.allowed, name)
	return f.err
}

func (f *applicationReadFake) clearPanelRead(_ context.Context, name string) error {
	f.cleared = append(f.cleared, name)
	return f.err
}

func TestAConfirmedRegistrationAllowsThePanelReadAndRemoveClearsIt(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	reads := p.appReadRules.(*applicationReadFake)
	shown := shownDigest(t, applicationCall(t, h, "GET", "/application/register", "").Body.String())
	if len(reads.allowed) != 0 {
		t.Fatalf("showing the page wrote a rule: %v", reads.allowed)
	}
	if rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":"`+shown+`"}`); rec.Code != 200 {
		t.Fatalf("confirm %d: %s", rec.Code, rec.Body.String())
	}
	if !slices.Equal(reads.allowed, []string{"example"}) || len(reads.cleared) != 0 {
		t.Fatalf("the confirmed registration wrote %v, cleared %v", reads.allowed, reads.cleared)
	}
	p.appRequest.remove, p.appRequest.done = "example", ""
	shown = buttonDigest(t, applicationCall(t, h, "GET", "/application/remove", "").Body.String(), "/application/remove/confirm")
	if rec := applicationCall(t, h, "POST", "/application/remove/confirm", `{"confirm":true,"shown":"`+shown+`"}`); rec.Code != 200 {
		t.Fatalf("remove %d: %s", rec.Code, rec.Body.String())
	}
	if !slices.Equal(reads.cleared, []string{"example"}) || len(reads.allowed) != 1 {
		t.Fatalf("the confirmed removal cleared %v, allowed %v", reads.cleared, reads.allowed)
	}
}

func TestAnUnconfirmedRegistrationWritesNoReadRule(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	reads := p.appReadRules.(*applicationReadFake)
	applicationCall(t, h, "GET", "/application/register", "")
	if rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":false,"shown":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unconfirmed click: %d", rec.Code)
	}
	if rec := applicationCall(t, h, "POST", "/application/register/cancel", `{"confirm":false,"shown":""}`); rec.Code != 200 {
		t.Fatalf("cancel %d", rec.Code)
	}
	applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":""}`)
	if len(reads.allowed) != 0 || len(reads.cleared) != 0 || len(apps.entries) != 0 {
		t.Fatalf("without the confirmation: allowed %v cleared %v entries %v", reads.allowed, reads.cleared, apps.entries)
	}
	// A cancelled removal clears nothing either.
	p.appRequest.remove, p.appRequest.done = "example", ""
	applicationCall(t, h, "POST", "/application/remove/cancel", `{"confirm":false,"shown":""}`)
	if len(reads.cleared) != 0 {
		t.Fatalf("a cancelled removal cleared %v", reads.cleared)
	}
}

func TestAFailedReadRuleKeepsTheRegistrationAndSaysSo(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	p.appReadRules.(*applicationReadFake).err = errors.New("permission service down")
	rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":""}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Registered Example.") || !strings.Contains(rec.Body.String(), "permission service down") {
		t.Fatalf("confirm %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := apps.entries["example"]; !ok {
		t.Fatal("a failed rule write undid the registration")
	}
}

// readFilteredDirectory filters Observe the way the Applications service does
// (serve/applications.go): an entry is listed only when the rights policy
// permits application read on app:<name> for the reader.
type readFilteredDirectory struct {
	*applicationDirectoryFake
	policy *rights.DecisionPolicy
	reader rwire.Subject
}

func (d *readFilteredDirectory) Observe(ctx context.Context, cursor string, wait int64) (fwire.ApplicationPage, error) {
	page, err := d.applicationDirectoryFake.Observe(ctx, cursor, wait)
	visible := page.Applications[:0]
	for _, entry := range page.Applications {
		if d.policy.Decide(d.reader, applicationReadAction, applicationResource(entry.Descriptor.Name)).Outcome == rwire.DecisionOutcomePermitted {
			visible = append(visible, entry)
		}
	}
	page.Applications = visible
	return page, err
}

func (d *readFilteredDirectory) listed(t *testing.T) []string {
	t.Helper()
	page, err := d.Observe(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range page.Applications {
		names = append(names, entry.Descriptor.Name)
	}
	return names
}

// panelApplicationRuntime hosts a fresh rights policy that has application
// read in its catalogue and no rule; this test process is its operator, as
// the Panel is the installed runtime's.
func panelApplicationRuntime(t *testing.T) *rights.DecisionPolicy {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "decisions.json"), []string{applicationReadAction, applicationActivateAction})
	if err != nil {
		t.Fatal(err)
	}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.RightsPolicy, o.RightsEndpoint = policy, o.ConfigEndpoint+"-rights"
		o.RightsOperator = func(_ context.Context, peer *identity.Peer) error {
			process, err := peer.Process.AtLeast(listen.Program.Process)
			if err != nil || process.PID != os.Getpid() {
				return rightsservice.ErrOperatorForbidden
			}
			return nil
		}
	})
	return policy
}

func TestTheConfirmedRegistrationListsTheAppForThePanelUntilRevoked(t *testing.T) {
	own(t)
	policy := panelApplicationRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	self := exploreSelf()
	reader := rwire.Subject{Account: self.Account, Program: self.Program}
	apps := &readFilteredDirectory{applicationDirectoryFake: &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}, policy: policy, reader: reader}
	request, _, err := parseApplicationStart(writeApplicationManifest(t, manifestText(applicationTestProgram(t), "Example")), "", "overview")
	if err != nil {
		t.Fatal(err)
	}
	p := &servicePanel{comfyApps: apps}
	p.appRequest.register = request.register
	h := p.handler(applicationTestKey)

	// Fresh runtime: no rule, and showing the page writes none.
	shown := shownDigest(t, applicationCall(t, h, "GET", "/application/register", "").Body.String())
	if read := policy.ReadRule(reader, applicationReadAction, "app:example"); read.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatalf("a rule exists before the click: %+v", read)
	}
	rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":"`+shown+`"}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"message":"Registered Example."}` {
		t.Fatalf("confirm %d: %s", rec.Code, rec.Body.String())
	}
	if got := apps.listed(t); !slices.Equal(got, []string{"example"}) {
		t.Fatalf("Apps lists %v after the confirmed registration", got)
	}
	read := policy.ReadRule(reader, applicationReadAction, "app:example")
	if read.Record == nil || !read.Record.Rule.Permit || read.Record.Why != applicationReadWhy || !sameFile(read.Record.SetBy.Program, self.Program) {
		t.Fatalf("the rule %+v", read)
	}
	// The same edit lets the Panel open the entry (default-policy D4).
	if open := policy.ReadRule(reader, applicationActivateAction, "app:example"); open.Record == nil || !open.Record.Rule.Permit || open.Record.Why != applicationReadWhy || open.Revision != read.Revision {
		t.Fatalf("the activate rule %+v", open)
	}

	// The person revokes it; the Panel no longer lists the app.
	operator, err := panelMachine().ResolveRightsOperator(ctx, exploreLocal)
	if err != nil {
		t.Fatal(err)
	}
	if edit, err := operator.RevokeRuleContext(ctx, read.Revision, rightsclient.Subject(reader), applicationReadAction, "app:example"); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("revoke %+v %v", edit, err)
	}
	if got := apps.listed(t); len(got) != 0 {
		t.Fatalf("Apps lists %v after the revoke", got)
	}

	// The installer asks again (a reinstall). The page cannot show the hidden
	// entry; the person's confirmation writes the rule again, the name's
	// holder conflicts, and the request waits for a review of what is there.
	p.appRequest.register, p.appRequest.done = request.register, ""
	shown = shownDigest(t, applicationCall(t, h, "GET", "/application/register", "").Body.String())
	rec = applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":"`+shown+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "reload to review it") {
		t.Fatalf("second confirm %d: %s", rec.Code, rec.Body.String())
	}
	if got := apps.listed(t); !slices.Equal(got, []string{"example"}) {
		t.Fatalf("Apps lists %v after the second confirmed registration", got)
	}
	page := applicationCall(t, h, "GET", "/application/register", "").Body.String()
	rec = applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":"`+shownDigest(t, page)+`"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Example is already registered.") || len(apps.entries) != 1 {
		t.Fatalf("the reviewed confirm %d: %s", rec.Code, rec.Body.String())
	}

	// A confirmed Remove clears the rule.
	p.appRequest.remove, p.appRequest.done = "example", ""
	shown = buttonDigest(t, applicationCall(t, h, "GET", "/application/remove", "").Body.String(), "/application/remove/confirm")
	rec = applicationCall(t, h, "POST", "/application/remove/confirm", `{"confirm":true,"shown":"`+shown+`"}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"message":"Removed Example."}` {
		t.Fatalf("remove %d: %s", rec.Code, rec.Body.String())
	}
	for _, action := range panelEntryActions {
		if read := policy.ReadRule(reader, action, "app:example"); read.Outcome != rwire.RuleReadOutcomeUnknown {
			t.Fatalf("the %s rule outlived the confirmed removal: %+v", action, read)
		}
	}
	// Clearing a name without a rule is already done.
	if err := (panelApplicationReads{}).clearPanelRead(ctx, "example"); err != nil {
		t.Fatal(err)
	}
}

func TestAnUnconfirmedRegistrationWritesNoRuleOnTheRuntime(t *testing.T) {
	own(t)
	policy := panelApplicationRuntime(t)
	self := exploreSelf()
	reader := rwire.Subject{Account: self.Account, Program: self.Program}
	apps := &readFilteredDirectory{applicationDirectoryFake: &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}, policy: policy, reader: reader}
	request, _, err := parseApplicationStart(writeApplicationManifest(t, manifestText(applicationTestProgram(t), "Example")), "", "overview")
	if err != nil {
		t.Fatal(err)
	}
	p := &servicePanel{comfyApps: apps}
	p.appRequest.register = request.register
	h := p.handler(applicationTestKey)
	applicationCall(t, h, "GET", "/application/register", "")
	applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":false,"shown":""}`)
	applicationCall(t, h, "POST", "/application/register/cancel", `{"confirm":false,"shown":""}`)
	if read := policy.ReadRule(reader, applicationReadAction, "app:example"); read.Outcome != rwire.RuleReadOutcomeUnknown || len(apps.entries) != 0 {
		t.Fatalf("an unconfirmed registration wrote %+v, entries %v", read, apps.entries)
	}
}
