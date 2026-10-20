package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/grants"
)

// VISION 2026-10-31: Activity stops a running woken turn through the native
// operator's StopForPerson, by the question's ID; a GET stops nothing.
func TestActivityMessageStopsWokenTurn(t *testing.T) {
	person := &messagePersonFake{}
	old := messagePersonOperator
	t.Cleanup(func() { messagePersonOperator = old })
	messagePersonOperator = func(context.Context) (messagePersonReader, error) { return person, nil }
	panel := (&servicePanel{}).handler("test-key")
	if r := panelRequest(t, panel, "/activity/message/stop?id=q-1:start", nil); r.Code != 405 || len(person.ids) != 0 {
		t.Fatal("GET stopped a turn", r.Code, person.ids)
	}
	r := panelRequest(t, panel, "/activity/message/stop?id=q-1:start", map[string]string{})
	got := decodePanel[map[string]string](t, r.Code, r.Body.Bytes())
	if got["outcome"] != "page" || got["reason"] != "stopped" || len(person.ids) != 1 || person.ids[0] != "stop:q-1" {
		t.Fatal(got, person.ids)
	}
}

// VISION 2026-10-31: a woken turn's Activity row offers revoking its exact
// start permit, also a paired computer's; malformed rows offer nothing, and
// the Panel only revokes a paired subject's rule, never sets one.
func TestActivityMessageStartRowRevokesItsDirection(t *testing.T) {
	native := filepath.Join(t.TempDir(), "sender.exe")
	paired := "remote:" + strings.Repeat("a", 64)
	resource := "app:receiver/start:" + strings.Repeat("b", 64)
	row := func(program string) map[string]string {
		return map[string]string{"outcome": "receiver_start_started", "account": "acct", "permit_program": program, "permit_action": grants.ApplicationMessageStartAction, "permit_resource": resource}
	}
	for _, program := range []string{native, paired} {
		got := messageStartRevoke(row(program))
		if got == nil || got.Program != program || got.Resource != resource || got.Action != grants.ApplicationMessageStartAction || got.Account != "acct" {
			t.Fatal(program, got)
		}
		if !(rightsEdit{Edit: "revoke", Revision: "r", Account: got.Account, Program: got.Program, Action: got.Action, Resource: got.Resource}).valid() {
			t.Fatal("Panel refuses the row's revoke", program)
		}
	}
	if (rightsEdit{Edit: "set", Revision: "r", Account: "acct", Program: paired, Action: grants.ApplicationMessageStartAction, Resource: resource, Permit: true}).valid() {
		t.Fatal("Panel sets a paired subject's permit")
	}
	for name, change := range map[string]func(map[string]string){
		"not a start":      func(a map[string]string) { a["outcome"] = "sent" },
		"other action":     func(a map[string]string) { a["permit_action"] = "abstraction.facade/application.message" },
		"bad resource":     func(a map[string]string) { a["permit_resource"] = "app:receiver" },
		"relative program": func(a map[string]string) { a["permit_program"] = "sender.exe" },
		"no account":       func(a map[string]string) { a["account"] = "" },
	} {
		a := row(native)
		change(a)
		if messageStartRevoke(a) != nil {
			t.Fatal(name)
		}
	}
}
