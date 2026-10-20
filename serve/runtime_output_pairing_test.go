package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// INF-V4, INF-S7: a job refused for want of its output profile admits the
// paired question for output:video, once for its program, and none once the
// program holds both rights or a denial of either.
func TestARefusedJobAdmitsThePairedOutputQuestion(t *testing.T) {
	r, _, book := inputAsks(t)
	app := inference.Subject{Account: r.owner, Program: appProgram("comfyui")}
	r.askOutput(context.Background(), app, inference.OutputVideo)
	r.askOutput(context.Background(), app, inference.OutputVideo)
	r.askOutput(context.Background(), app, "live")
	records, _, err := book.ApplicationHistory()
	if err != nil || len(records) != 1 {
		t.Fatalf("one paired output question expected: %+v %v", records, err)
	}
	program, action, resource, ok := asks.FirstUse(records[0].Key, records[0].About, records[0].Text)
	if !ok || program != app.Program || action != contentWriteAction || resource != "output:video" {
		t.Fatalf("question %+v", records[0])
	}
	denied := inference.Subject{Account: r.owner, Program: appProgram("editor")}
	if err := r.policy.Set(rwire.Subject{Account: denied.Account, Program: denied.Program}, contentReadAction, "output:video", false); err != nil {
		t.Fatal(err)
	}
	r.askOutput(context.Background(), denied, inference.OutputVideo)
	if records, _, err := book.ApplicationHistory(); err != nil || len(records) != 1 {
		t.Fatalf("a denied output profile asked again: %+v %v", records, err)
	}
}

// INF-S7, the coordinator's condition of 2026-10-02: the rules one Allow on
// the paired output question writes let an application write a video and
// read back only the videos its own jobs produced. Two applications that
// both hold output:video each read their own clip and neither reads the
// other's, because a generated output is read back only by the account and
// program its provenance names.
func TestOutputPairingReadsOnlyTheAppsOwnOutputs(t *testing.T) {
	state := t.TempDir()
	r := testRights(t, state)
	for _, action := range []string{contentReadAction, contentWriteAction} {
		if err := r.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	runtimeOptions, _ := isolatedRuntime(t)
	content, err := composeContent(runtimeOptions, state, r)
	if err != nil {
		t.Fatal(err)
	}
	apps := []inference.Subject{
		{Account: r.owner, Program: filepath.Join(state, "comfyui.exe")},
		{Account: r.owner, Program: filepath.Join(state, "opencode.exe")},
	}
	clips := make([]string, len(apps))
	for i, app := range apps {
		// The rules the Panel writes for an Allow on the app's paired question.
		answer := askclient.RecordMetadata{Key: asks.FirstUseKey, About: app.Program, Option: "allow",
			Text: app.Program + " wants to " + contentWriteAction + " on output:video"}
		rules := grants.FirstUseRules(r.owner, answer)
		if len(rules) != 2 {
			t.Fatalf("an output answer wrote %d rules", len(rules))
		}
		for _, rule := range rules {
			if err := r.policy.Set(rule.Subject, rule.Action, rule.Resource, rule.Permit); err != nil {
				t.Fatal(err)
			}
		}
		clip := []byte(fmt.Sprintf("\x1aE\xdf\xa3 the clip of %s", app.Program))
		clips[i] = fmt.Sprintf("sha256:%x", sha256.Sum256(clip))
		commit, outcome := content.prepareWrite(context.Background(), app, inference.OutputVideo, "video/webm", int64(len(clip)))
		if outcome != inference.ContentResolved {
			t.Fatalf("%s prepare video output: %v", app.Program, outcome)
		}
		if outcome := commit(context.Background(), clips[i], clip); outcome != inference.ContentResolved {
			t.Fatalf("%s commit video output: %v", app.Program, outcome)
		}
		if got, outcome := content.read(context.Background(), app, clips[i], 1<<20); outcome != inference.ContentResolved || !bytes.Equal(got, clip) {
			t.Fatalf("%s reading its own clip: %v", app.Program, outcome)
		}
	}
	for i, app := range apps {
		other := clips[1-i]
		if _, outcome := content.read(context.Background(), app, other, 1<<20); outcome != inference.ContentForbidden {
			t.Fatalf("%s read another application's clip: %v", app.Program, outcome)
		}
	}
}
