package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"testing"
)

func TestClaudeConnectionRetryAfterDeclineAndRestartRequiresFreshKeep(t *testing.T) {
	ctx := context.Background()
	project, err := fileid.Resolve(ctx, fileid.Self, t.TempDir(), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	state := "pending"
	commands, admissions := 0, 0
	makeController := func() *claudePanelConnections {
		c := &claudePanelConnections{prepare: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, enroll: func(context.Context, claudePanelImage) (string, error) { return state, nil }, state: func() (string, error) { return t.TempDir(), nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, command: func(context.Context, string, ...string) ([]byte, error) {
			if state != "enrolled" {
				t.Fatal("unapproved helper executed")
			}
			commands++
			return nil, nil
		}}
		c.admit = func(_ context.Context, image claudePanelImage) (claudePanelImage, error) {
			admissions++
			if state != "enrolled" {
				return nil, errors.New("not kept")
			}
			return &claudePanelFixtureImage{program: image.Program(), key: image.FileKey()}, nil
		}
		return c
	}
	c := makeController()
	result, err := c.saveProject(ctx, project)
	if err != nil || result.Outcome != "permission_pending" || commands != 0 || admissions != 0 {
		t.Fatal(result, err, commands, admissions)
	}
	c.close()
	// Decline and panel restart retain the same candidate file, but no authority.
	state = "pending"
	c = makeController()
	result, err = c.saveProject(ctx, project)
	if err != nil || result.Outcome != "permission_pending" || commands != 0 {
		t.Fatal(result, err)
	}
	state = "enrolled"
	if result, err = c.saveProject(ctx, project); err != nil || result.Outcome != "saved" || commands != 1 || admissions != 1 {
		t.Fatal(result, err, commands, admissions)
	}
	state = "pending"
	if result, err = c.saveProject(ctx, project); err != nil || result.Outcome != "permission_pending" || commands != 1 {
		t.Fatal(result, err, commands)
	}
	state = "enrolled"
	if _, err = c.saveProject(ctx, project); err != nil || commands != 2 {
		t.Fatal(err, commands)
	}
	c.close()
}

func TestCodexConnectionRetryDoesNotDispatchPendingCandidate(t *testing.T) {
	c := codexPanelFixture(t)
	state := "pending"
	calls := 0
	c.enroll = func(context.Context, claudePanelImage) (string, error) { return state, nil }
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Registration: "saved"})
	}
	result, err := c.change(context.Background(), false, "1")
	if err != nil || result.Outcome != "permission_pending" || calls != 0 {
		t.Fatal(result, err, calls)
	}
	c.close()
	c = codexPanelFixture(t)
	c.enroll = func(context.Context, claudePanelImage) (string, error) { return state, nil }
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Registration: "saved"})
	}
	state = "enrolled"
	if _, err = c.change(context.Background(), false, "1"); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	state = "pending"
	if result, err = c.change(context.Background(), false, "1"); err != nil || result.Outcome != "permission_pending" || calls != 1 {
		t.Fatal(result, err, calls)
	}
	c.close()
}

func TestConnectionRetryRejectsChangedAdmission(t *testing.T) {
	c := codexPanelFixture(t)
	c.admit = func(context.Context, claudePanelImage) (claudePanelImage, error) {
		return &claudePanelFixtureImage{key: "replacement"}, nil
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("changed image executed")
		return nil, nil
	}
	if _, err := c.change(context.Background(), false, "1"); err == nil {
		t.Fatal("changed native admission accepted")
	}
	c.close()
}
