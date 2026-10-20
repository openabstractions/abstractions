package startprofile

import (
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"strings"
	"testing"
)

func TestColdResumeCarrierClosedAndOptional(t *testing.T) {
	c := &ColdResume{Context: "chat-retained", CwdKey: "retained-id", Revision: strings.Repeat("a", 64), Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: "original", Launch: &msg.ChatLaunch{Session: "original", Origin: msg.ChatOriginDesktop, Cwd: t.TempDir(), Mode: msg.ChatModePlan}}))}
	got, err := ParseColdResumeMetadata(map[string]any{ColdResumeMetaKey: c.Metadata()})
	if err != nil || *got != *c {
		t.Fatal(got, err)
	}
	if got, err := ParseColdResumeMetadata(nil); got != nil || err != nil {
		t.Fatal(got, err)
	}
	for _, change := range []func(map[string]any){func(m map[string]any) { m["extra"] = "x" }, func(m map[string]any) { delete(m, "binding") }, func(m map[string]any) { m["revision"] = "bad" }, func(m map[string]any) { m["binding"] = "{}" }, func(m map[string]any) { m["cwd_key"] = 42 }} {
		m := c.Metadata()
		change(m)
		if _, err := ParseColdResumeMetadata(map[string]any{ColdResumeMetaKey: m}); err == nil {
			t.Fatal("malformed accepted", m)
		}
	}
}

func TestColdResumeCarriesCodexThreadAlone(t *testing.T) {
	thread := "019a0000-1111-7222-8333-444455556666"
	c := &ColdResume{Context: "chat-retained", Revision: strings.Repeat("b", 64), Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: thread}))}
	got, err := ParseColdResumeMetadata(map[string]any{ColdResumeMetaKey: c.Metadata()})
	if err != nil || *got != *c {
		t.Fatal(got, err)
	}
	if id, err := got.Thread(); err != nil || id != thread {
		t.Fatal(id, err)
	}
	if _, err := got.Launch(); err == nil {
		t.Fatal("a thread selection is no Claude launch")
	}
	for _, bad := range []*ColdResume{
		{Context: c.Context, Revision: c.Revision, CwdKey: "folder", Binding: c.Binding},
		{Context: c.Context, Revision: c.Revision, Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: "not-a-thread"}))},
		{Context: c.Context, Revision: "short", Binding: c.Binding},
	} {
		if _, err := bad.Thread(); err == nil {
			t.Fatal("malformed thread selection accepted", bad)
		}
	}
	claude := &ColdResume{Context: "chat-retained", CwdKey: "retained-id", Revision: strings.Repeat("a", 64), Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: "original", Launch: &msg.ChatLaunch{Session: "original", Origin: msg.ChatOriginDesktop, Cwd: t.TempDir(), Mode: msg.ChatModePlan}}))}
	if _, err := claude.Thread(); err == nil {
		t.Fatal("a Claude launch is no thread selection")
	}
}
