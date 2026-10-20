package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesChatLaunchUnknownHostWordsRetainedButNeverAdmitted(t *testing.T) {
	for _, words := range [][2]string{{"future-host", "plan"}, {"desktop", "future-mode"}, {"future-host", "future-mode"}} {
		raw, err := json.Marshal(map[string]string{"session": "host", "origin": words[0], "mode": words[1], "cwd": t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		var launch msg.ChatLaunch
		if err := msg.FromJSON(raw, &launch); err != nil {
			t.Fatal("host vocabulary was closed", err)
		}
		encoded, err := msg.ToJSON(launch)
		if err != nil {
			t.Fatal(err)
		}
		var kept map[string]string
		if err := json.Unmarshal(encoded, &kept); err != nil || kept["origin"] != words[0] || kept["mode"] != words[1] {
			t.Fatal("unknown host words changed", string(encoded), err)
		}
		if _, err := messageChatLaunchKey("host", &launch); err == nil {
			t.Fatal("unknown host fact granted launch admission", launch)
		}
	}
}

func TestMessagesChatLaunchKnownStoredWordsRemainCompatible(t *testing.T) {
	for _, origin := range []string{"desktop", "cli"} {
		for _, mode := range []string{"plan", "read_only", "default", "accept_edits"} {
			// encoding/json is the existing retained message store's owner;
			// these capitalized keys and wire words are its former record shape.
			old, err := json.Marshal(map[string]string{"Session": "host", "Origin": origin, "Mode": mode, "Cwd": t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			var launch msg.ChatLaunch
			if err := json.Unmarshal(old, &launch); err != nil || !launch.Origin.Known() || !launch.Mode.Known() {
				t.Fatal(launch, err)
			}
			if _, err := messageChatLaunchKey("host", &launch); err != nil {
				t.Fatal("known host fact no longer admitted", err)
			}
			kept, err := json.Marshal(launch)
			if err != nil {
				t.Fatal(err)
			}
			var words map[string]string
			if err := json.Unmarshal(kept, &words); err != nil || words["Origin"] != origin || words["Mode"] != mode {
				t.Fatal(string(kept), err)
			}
		}
	}
}

func TestMessagesChatLaunchPersistsOnlyNativeCorrelationInExistingStore(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	cwd := filepath.Join(t.TempDir(), "original")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	claim := &msg.ChatBinding{ChatID: "native-session", ChatTitle: "Review\u202e\n plan", Launch: &msg.ChatLaunch{Session: "native-session", Origin: msg.ChatOriginDesktop, Cwd: cwd, Mode: msg.ChatModePlan}}
	canonical, err := s.contextForChat(ctx, actor.Account, actor.Program, claim.ChatID, claim)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := openMessageStore(filepath.Dir(s.dir), s.now)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := restarted.retainedChats(ctx, actor.Account, actor.Program)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if rows[0].Context != canonical || rows[0].Title != "Review plan" || rows[0].LastSeenUnixMs == 0 {
		t.Fatal(rows)
	}
	launch, err := rows[0].currentLaunch()
	if err != nil || *launch != *claim.Launch {
		t.Fatal(launch, err)
	}
	if foreign, err := restarted.retainedChats(ctx, "foreign-account", actor.Program); err != nil || len(foreign) != 0 {
		t.Fatal(foreign, err)
	}
	if foreign, err := restarted.retainedChats(ctx, actor.Account, "foreign-program"); err != nil || len(foreign) != 0 {
		t.Fatal(foreign, err)
	}
	if err := os.Rename(cwd, cwd+"-kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := rows[0].currentLaunch(); err == nil {
		t.Fatal("a replacement cwd retained the original host identity")
	}
}

func TestMessagesChatLaunchMissingOrForeignFactsFailClosed(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	claim := &msg.ChatBinding{ChatID: "host"}
	if _, err := s.contextForChat(ctx, actor.Account, actor.Program, claim.ChatID, claim); err != nil {
		t.Fatal(err)
	}
	rows, err := s.retainedChats(ctx, actor.Account, actor.Program)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if _, err := rows[0].currentLaunch(); err == nil {
		t.Fatal("missing host facts became a launch")
	}
	for _, launch := range []*msg.ChatLaunch{
		{Session: "foreign", Origin: msg.ChatOriginDesktop, Cwd: t.TempDir(), Mode: msg.ChatModePlan},
		{Session: "host", Origin: msg.ChatOriginDesktop, Cwd: "relative", Mode: msg.ChatModePlan},
		{Session: "host", Cwd: t.TempDir(), Mode: msg.ChatModePlan},
		{Session: "host", Origin: msg.ChatOriginCli, Cwd: t.TempDir()},
	} {
		claim.Launch = launch
		if _, err := s.contextForChat(ctx, actor.Account, actor.Program, claim.ChatID, claim); err == nil {
			t.Fatal("invalid host facts retained", launch)
		}
	}
	// An older association has no independently recorded chat metadata.
	if _, err := s.contextForChat(ctx, actor.Account, actor.Program, "legacy-only"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.retainedChats(ctx, actor.Account, actor.Program)
	if err != nil || len(rows) != 1 {
		t.Fatal("legacy address inferred into discovery", rows, err)
	}
}
