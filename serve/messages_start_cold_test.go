package main

import (
	"context"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"os"
	"path/filepath"
	"testing"
)

func TestMessagesColdSelectionRechecksCurrentNativeFacts(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	cwd := filepath.Join(t.TempDir(), "original")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	claim := &msg.ChatBinding{ChatID: "original", Launch: &msg.ChatLaunch{Session: "original", Origin: msg.ChatOriginDesktop, Cwd: cwd, Mode: msg.ChatModePlan}}
	canonical, err := s.contextForChat(ctx, actor.Account, actor.Program, claim.ChatID, claim)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.coldResumeForChat(ctx, actor.Account, actor.Program, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkColdResume(ctx, actor.Account, actor.Program, selected); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"foreign", actor.Program}, {actor.Account, "foreign"}} {
		if _, err := s.coldResumeForChat(ctx, pair[0], pair[1], canonical); err == nil {
			t.Fatal("foreign selection")
		}
	}
	claim.ChatTitle = "renamed"
	if _, err := s.contextForChat(ctx, actor.Account, actor.Program, claim.ChatID, claim); err != nil {
		t.Fatal(err)
	}
	if err := s.checkColdResume(ctx, actor.Account, actor.Program, selected); err != nil {
		t.Fatal("display changed selected authority", err)
	}
	if _, err := s.contextForChat(ctx, actor.Account, actor.Program, claim.ChatID, &msg.ChatBinding{ChatID: claim.ChatID}); err != nil {
		t.Fatal(err)
	}
	if err := s.checkColdResume(ctx, actor.Account, actor.Program, selected); err == nil {
		t.Fatal("withdrawn launch accepted")
	}
	if _, err := s.contextForChat(ctx, actor.Account, actor.Program, claim.ChatID, claim); err != nil {
		t.Fatal(err)
	}
	selected, err = s.coldResumeForChat(ctx, actor.Account, actor.Program, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cwd, cwd+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.checkColdResume(ctx, actor.Account, actor.Program, selected); err == nil {
		t.Fatal("replacement cwd accepted")
	}
}
