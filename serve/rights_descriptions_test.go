package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActionDescriptionFileIsClosedAndBounded(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// JSON escaping the actual native executable avoids inventing a portable
	// pathname that the receiving platform cannot normalize.
	quoted, err := jsonText(program)
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"action":"opencode/log-source.share","feature_app":{"account":"account","program":` + quoted + `},"app_label":"OpenCode","description":"Share event contents with the assistant model","declared_risk":"sensitive"}`
	path := filepath.Join(t.TempDir(), "descriptor.json")
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := readActionDescriptionFile(path)
	if err != nil || d.Action != "opencode/log-source.share" || d.FeatureApp.Program != program {
		t.Fatalf("valid declaration: %+v %v", d, err)
	}
	for name, data := range map[string]string{
		"duplicate":        strings.Replace(valid, `"app_label":"OpenCode"`, `"app_label":"OpenCode","app_label":"Other"`, 1),
		"nested duplicate": strings.Replace(valid, `"account":"account"`, `"account":"account","account":"other"`, 1),
		"unknown":          strings.Replace(valid, `"app_label":"OpenCode"`, `"app_label":"OpenCode","permit":true`, 1),
		"missing":          strings.Replace(valid, `,"declared_risk":"sensitive"`, "", 1),
		"trailing":         valid + `{}`,
		"number":           strings.Replace(valid, `"app_label":"OpenCode"`, `"app_label":42`, 1),
		"null":             strings.Replace(valid, `"description":"Share event contents with the assistant model"`, `"description":null`, 1),
		"template":         strings.Replace(valid, quoted, `"<final installed absolute OpenCode executable>"`, 1),
		"oversize":         valid + strings.Repeat(" ", 64<<10),
		"bad UTF8":         valid + string([]byte{0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readActionDescriptionFile(path); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}

func TestRightsCommandPublishesDescriptionsWithoutChangingGrants(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on this transport")
	}
	options, _ := credentialsRuntime(t)
	self := probeSelf()
	d := actionDescriptionJSON{Action: "opencode/log-source.share", FeatureApp: probeSubject{Account: "described account", Program: self.Program}, AppLabel: "OpenCode", Description: "Share event contents with the assistant model", DeclaredRisk: "sensitive"}
	path := filepath.Join(t.TempDir(), "descriptor.json")
	write := func() {
		text, err := jsonText(d)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) rightsReply {
		t.Helper()
		r, err := runRightsCommand(t, append(args, "--endpoint", options.endpoint)...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return r
	}
	write()
	r := run("register-action", "--description-file", path)
	if r.Outcome != "applied" || r.Description == nil || r.Description.Description.FeatureApp.Account != "described account" || r.Description.PublishedBy.Account != self.Account {
		t.Fatalf("publication: %+v", r)
	}
	if r := run("decide", "--action", d.Action, "--resource", "exact-source"); r.Outcome != "not_granted" {
		t.Fatal("description granted permission", r)
	}
	run("grant", "--action", d.Action, "--resource", "exact-source", "--program", self.Program)
	d.Description = "Share selected event contents with the assistant model"
	write()
	run("register-action", "--description-file", path)
	if r := run("decide", "--action", d.Action, "--resource", "exact-source"); r.Outcome != "permitted" {
		t.Fatal("correction changed grant", r)
	}
	_, err := runRightsCommand(t, "register-action", "--description-file", path, "--action", "other/action", "--endpoint", options.endpoint)
	assertExit(t, err, exitUsage, "descriptor action mismatch")
}
