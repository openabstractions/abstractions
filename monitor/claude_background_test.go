package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestClaudeBackgroundAsksNeedsOwnPersonReviewAndKeepsMessageCompanion(t *testing.T) {
	c, commands := backgroundFixture(t)
	c.backgroundReview = func(context.Context, claudePanelCLI, string) (bool, error) {
		t.Fatal("message review substituted for Asks")
		return false, nil
	}
	if _, err := c.setBackgroundScope(context.Background(), "1", "enable", "asks"); err == nil || len(*commands) != 0 {
		t.Fatal("missing Asks review mutated", err, *commands)
	}
	reviewed := false
	c.asksBackgroundReview = func(context.Context, claudePanelCLI, string) (bool, error) {
		if len(*commands) != 0 {
			t.Fatal("mutation preceded Asks person review")
		}
		reviewed = true
		return true, nil
	}
	if out, err := c.setBackgroundScope(context.Background(), "1", "enable", "asks"); err != nil || out.State != "owned" || !reviewed {
		t.Fatal(out, err)
	}
	found := false
	for _, command := range *commands {
		found = found || strings.Contains(command, "--scope asks")
	}
	if !found {
		t.Fatal("Asks scope not explicit", *commands)
	}
	_, _ = c.command(context.Background(), "fixture", "watch-permission", "claude", "enable")
	*commands = nil
	if out, err := c.setBackgroundScope(context.Background(), "1", "disable", "asks"); err != nil || out.State != "disabled" {
		t.Fatal(out, err)
	}
	for _, command := range *commands {
		if strings.Contains(command, "[plugin claude remove") {
			t.Fatal("Asks disable removed message companion", *commands)
		}
	}
}

func TestClaudeBackgroundRequiresExactPersonAnswer(t *testing.T) {
	now := time.Now().UTC()
	created := now.Add(-time.Minute)
	expires := now.Add(time.Minute)
	program := "C:/OpenAbstractions/Panel.exe"
	question := askclient.ApplicationQuestion{RequestKey: "request", Key: "content.replace", Slots: map[string]string{"target": "Claude user watch permission", "replacement": "allow header checks only", "code": "bound-request", "until": expires.Format(time.RFC3339)}}
	template, _ := asks.Find(question.Key)
	text, err := template.Render(asks.Ask{Asker: asks.AskerDisplay(program), Slots: question.Slots})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"person", "responder", "wrong-id", "wrong-text", "wrong-target", "wrong-key", "wrong-asker", "kept", "refuse", "pending", "expired", "future", "before-request"} {
		t.Run(mode, func(t *testing.T) {
			by := askclient.AnswererOperator
			record := askclient.RecordMetadata{ID: "q", Key: question.Key, Asker: asks.AskerDisplay(program), About: question.Slots["target"], Text: text, Option: "once", Yes: true, Answered: now.Add(-time.Second).Format(time.RFC3339Nano)}
			switch mode {
			case "responder":
				by = askclient.AnswererResponder
			case "wrong-id":
				record.ID = "other"
			case "wrong-text":
				record.Text += " changed"
			case "wrong-target":
				record.About = "other"
			case "wrong-key":
				record.Key = "other"
			case "wrong-asker":
				record.Asker = "other"
			case "kept":
				record.Kept = true
			case "refuse":
				record.Yes = false
			case "pending":
				record.Option = ""
			case "expired":
				expires = now
			case "future":
				record.Answered = now.Add(time.Second).Format(time.RFC3339Nano)
			case "before-request":
				record.Answered = created.Add(-time.Second).Format(time.RFC3339Nano)
			}
			read := askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeFound, Record: &record, AnsweredBy: &by}
			if got := approvedClaudeBackground(read, "q", program, question, created, expires, now); got != (mode == "person") {
				t.Fatal(mode, got)
			}
		})
	}
}

func backgroundFixture(t *testing.T) (*claudePanelConnections, *[]string) {
	t.Helper()
	commands := []string{}
	watchStates := map[string]string{"messages": "unreviewed", "asks": "unreviewed"}
	root := t.TempDir()
	c := &claudePanelConnections{revision: 1, rows: []claudeSavedConnection{{Kind: "user", Registration: "saved", Program: "C:/OA/kept.exe", ProgramKey: "fixture-key", HelperState: "current"}}, state: func() (string, error) { return root, nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, prepare: func(context.Context) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: "C:/OA/kept.exe"}, nil
	}, admit: func(context.Context, claudePanelImage) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: "C:/OA/kept.exe"}, nil
	}}
	c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		commands = append(commands, fmt.Sprint(args))
		if args[0] == "connections" {
			return json.Marshal(map[string]any{"outcome": "page", "connections": c.rows})
		}
		if args[0] == "watch-permission" {
			scope := "messages"
			if args[len(args)-1] == "asks" {
				scope = "asks"
			}
			if args[2] == "enable" {
				watchStates[scope] = "owned"
			}
			if args[2] == "disable" {
				watchStates[scope] = "disabled"
			}
			return json.Marshal(claudeBackgroundState{Outcome: "page", State: watchStates[scope], Preserved: args[2] == "disable"})
		}
		if args[0] == "plugin" || args[0] == "connect" {
			return []byte("verified"), nil
		}
		return nil, errors.New("unexpected command")
	}
	return c, &commands
}

func TestClaudeBackgroundEnableReviewPrecedesMutationAndDisableKeepsConnection(t *testing.T) {
	c, commands := backgroundFixture(t)
	reviewed := false
	c.backgroundReview = func(context.Context, claudePanelCLI, string) (bool, error) {
		if len(*commands) != 0 {
			t.Fatal("mutation preceded person review")
		}
		reviewed = true
		return true, nil
	}
	result, err := c.setBackground(context.Background(), "1", "enable")
	if err != nil || result.State != "owned" || !reviewed || len(*commands) != 4 {
		t.Fatal(result, err, *commands)
	}
	if (*commands)[1][:17] != "[watch-permission" || (*commands)[3][:7] != "[plugin" {
		t.Fatal(*commands)
	}
	*commands = nil
	result, err = c.setBackground(context.Background(), "1", "disable")
	if err != nil || result.State != "disabled" || !result.Preserved || len(*commands) != 4 || len(c.rows) != 1 {
		t.Fatal(result, err, *commands)
	}
	if (*commands)[1][:17] != "[watch-permission" || (*commands)[2][:17] != "[watch-permission" || (*commands)[3][:7] != "[plugin" {
		t.Fatal(*commands)
	}
}

func TestClaudeBackgroundDeclineAndStaleViewChangeNothing(t *testing.T) {
	c, commands := backgroundFixture(t)
	c.backgroundReview = func(context.Context, claudePanelCLI, string) (bool, error) { return false, nil }
	result, err := c.setBackground(context.Background(), "1", "enable")
	if err != nil || result.Outcome != "declined" || len(*commands) != 0 {
		t.Fatal(result, err, *commands)
	}
	if _, err = c.setBackground(context.Background(), "stale", "enable"); err == nil || len(*commands) != 0 {
		t.Fatal(err, *commands)
	}
	c.backgroundReview = nil
	if _, err = c.setBackground(context.Background(), "1", "enable"); err == nil || len(*commands) != 0 {
		t.Fatal("missing native review mutated settings", err, *commands)
	}
}

func TestClaudeBackgroundAsksEnableAfterMessagesDisableAndMaintenance(t *testing.T) {
	c, commands := backgroundFixture(t)
	c.backgroundReview = func(context.Context, claudePanelCLI, string) (bool, error) { return true, nil }
	c.asksBackgroundReview = c.backgroundReview
	defer c.close()
	for _, step := range []struct{ scope, action string }{{"messages", "enable"}, {"messages", "disable"}, {"asks", "enable"}} {
		if _, err := c.setBackgroundScope(context.Background(), "1", step.action, step.scope); err != nil {
			t.Fatal(step, err, *commands)
		}
	}
	*commands = nil
	if err := c.updateUserMod(context.Background(), &claudePanelFixtureImage{program: "C:/OA/kept.exe"}); err != nil {
		t.Fatal(err)
	}
	if len(*commands) != 3 || !strings.Contains((*commands)[1], "--scope asks") || !strings.Contains((*commands)[2], "[plugin claude install") {
		t.Fatal("approved Asks companion maintenance did not run", *commands)
	}
	if _, err := c.setBackgroundScope(context.Background(), "1", "disable", "asks"); err != nil {
		t.Fatal(err)
	}
	*commands = nil
	if err := c.updateUserMod(context.Background(), &claudePanelFixtureImage{program: "C:/OA/kept.exe"}); err != nil {
		t.Fatal(err)
	}
	for _, command := range *commands {
		if strings.Contains(command, "[plugin") {
			t.Fatal("all-disabled maintenance installed companion", *commands)
		}
	}
}

func TestClaudeBackgroundStatusUsesOnlyReceiptCommand(t *testing.T) {
	c, commands := backgroundFixture(t)
	result := c.readBackground(context.Background())
	if result.State != "unreviewed" || len(*commands) != 1 {
		t.Fatal(result, *commands)
	}
	expected := []string{"[watch-permission claude status --state-dir"}
	if !reflect.DeepEqual([]string{(*commands)[0][:len(expected[0])]}, expected) {
		t.Fatal(*commands)
	}
}

func TestClaudeBackgroundChangedConnectionAfterReviewWritesNoHostRule(t *testing.T) {
	c, commands := backgroundFixture(t)
	c.backgroundReview = func(context.Context, claudePanelCLI, string) (bool, error) {
		c.rows[0].Registration = "changed"
		return true, nil
	}
	if _, err := c.setBackground(context.Background(), "1", "enable"); err == nil || len(*commands) != 1 || (*commands)[0][:12] != "[connections" {
		t.Fatal(err, *commands)
	}
}

func TestClaudeBackgroundDisabledIntentSurvivesConnectAndHelperMaintenance(t *testing.T) {
	c, commands := backgroundFixture(t)
	c.userMod = true
	c.enroll = func(context.Context, claudePanelImage) (string, error) { return "enrolled", nil }
	c.backgroundReview = func(context.Context, claudePanelCLI, string) (bool, error) { return true, nil }
	defer c.close()
	removed, err := c.setBackground(context.Background(), "1", "disable")
	if err != nil || removed.State != "disabled" || !removed.Preserved {
		t.Fatal(removed, err)
	}
	*commands = nil
	saved, err := c.saveProject(context.Background(), fileid.FileID{})
	if err != nil || saved.Outcome != "saved" || saved.Companion != "disabled" {
		t.Fatal(saved, err)
	}
	image := &claudePanelFixtureImage{program: "C:/OA/kept.exe"}
	if err := c.updateUserMod(context.Background(), image); err != nil {
		t.Fatal(err)
	}
	for _, command := range *commands {
		if len(command) >= 7 && command[:7] == "[plugin" {
			t.Fatal("maintenance re-enabled disabled companion", *commands)
		}
	}
	*commands = nil
	enabled, err := c.setBackground(context.Background(), "1", "enable")
	if err != nil || enabled.State != "owned" {
		t.Fatal(enabled, err, *commands)
	}
	installs := 0
	for _, command := range *commands {
		if len(command) >= 7 && command[:7] == "[plugin" {
			installs++
		}
	}
	if installs != 1 {
		t.Fatal("explicit Enable did not restore the companion", *commands)
	}
}

func TestClaudeBackgroundDisablePreservesApprovedSiblingAndFailedUndoCompanion(t *testing.T) {
	for _, scope := range []string{"messages", "asks"} {
		for _, sibling := range []string{"owned", "preexisting"} {
			t.Run(scope+"/"+sibling, func(t *testing.T) {
				c, commands := backgroundFixture(t)
				original := c.command
				c.command = func(ctx context.Context, program string, args ...string) ([]byte, error) {
					if args[0] == "watch-permission" && args[2] == "status" {
						*commands = append(*commands, fmt.Sprint(args))
						return json.Marshal(claudeBackgroundState{Outcome: "page", State: sibling})
					}
					return original(ctx, program, args...)
				}
				if _, err := c.setBackgroundScope(context.Background(), "1", "disable", scope); err != nil {
					t.Fatal(err)
				}
				for _, command := range *commands {
					if strings.Contains(command, "[plugin claude remove") {
						t.Fatal("approved sibling lost companion", *commands)
					}
				}
			})
		}
		t.Run(scope+"/undo-failure", func(t *testing.T) {
			c, commands := backgroundFixture(t)
			original := c.command
			c.command = func(ctx context.Context, program string, args ...string) ([]byte, error) {
				if args[0] == "watch-permission" && args[2] == "disable" {
					return nil, errors.New("foreign settings")
				}
				return original(ctx, program, args...)
			}
			if _, err := c.setBackgroundScope(context.Background(), "1", "disable", scope); err == nil {
				t.Fatal("undo failure accepted")
			}
			for _, command := range *commands {
				if strings.Contains(command, "[plugin") {
					t.Fatal("failed undo removed companion", *commands)
				}
			}
		})
	}
}
