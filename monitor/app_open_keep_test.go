package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// appKeepingDirectory is the runtime as Open sees an app that registered
// itself: AskConfirmation raises the Keep question, and the person's Keep
// arrives on the observe numbered keepAt (0: the person does not answer).
type appKeepingDirectory struct {
	*appPageDirectory
	asked    []string
	keepAt   int
	observes int
}

func (k *appKeepingDirectory) Observe(ctx context.Context, after string, limit int64) (fwire.ApplicationPage, error) {
	if len(k.asked) > 0 {
		k.observes++
		if k.keepAt > 0 && k.observes >= k.keepAt {
			for i := range k.entries {
				k.entries[i].SelfRegistered = false
			}
		}
	}
	return k.appPageDirectory.Observe(ctx, after, limit)
}

func (k *appKeepingDirectory) AskConfirmation(_ context.Context, name string) (fwire.ApplicationChange, error) {
	k.asked = append(k.asked, name)
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

func appKeepingPanel(t *testing.T, keepAt int) (*appKeepingDirectory, *servicePanel, string) {
	t.Helper()
	apps, p := appPagePanel(t, true)
	apps.entries[0].SelfRegistered = true
	keeping := &appKeepingDirectory{appPageDirectory: apps, keepAt: keepAt}
	p.comfyApps = keeping
	savedWait, savedPoll := appKeepWait, appKeepPoll
	appKeepWait, appKeepPoll = 300*time.Millisecond, time.Millisecond
	t.Cleanup(func() { appKeepWait, appKeepPoll = savedWait, savedPoll })
	raw, _ := json.Marshal(appActionBody{Confirm: true, Shown: applicationDigest(&apps.entries[0].Descriptor)})
	return keeping, p, string(raw)
}

// Architect ruling 2026-11-10: registration asks nothing, and an app's Keep
// question is raised when the person acts on it in Apps. On R24 Open on an
// app that added itself answered "OpenAbstractions did not let the Panel open
// …" and asked nothing. Open now raises the Keep question and opens the app
// once the person keeps it.
func TestOpenOnAnAppThatAddedItselfAsksKeepThenOpens(t *testing.T) {
	apps, p, body := appKeepingPanel(t, 3)
	rec := appPagePost(t, p, "/app/example/start", body)
	var reply appActionReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || rec.Code != 200 || reply.Outcome != "ready" || reply.Message != "Example <b> is open." {
		t.Fatalf("Open after Keep: %d %s", rec.Code, rec.Body)
	}
	if len(apps.asked) != 1 || apps.asked[0] != "example" || len(apps.activated) != 1 {
		t.Fatalf("asked %v, activated %v", apps.asked, apps.activated)
	}
}

// A kept app opens without a Keep question.
func TestOpenOnAKeptAppAsksNoKeep(t *testing.T) {
	apps, p, body := appKeepingPanel(t, 0)
	apps.entries[0].SelfRegistered = false
	rec := appPagePost(t, p, "/app/example/start", body)
	if rec.Code != 200 || len(apps.asked) != 0 || len(apps.activated) != 1 {
		t.Fatalf("kept Open: %d %s asked %v", rec.Code, rec.Body, apps.asked)
	}
}

// While the person has not answered Keep, Open starts nothing and says what
// waits.
func TestOpenWaitingForKeepStartsNothing(t *testing.T) {
	apps, p, body := appKeepingPanel(t, 0)
	rec := appPagePost(t, p, "/app/example/start", body)
	var reply appActionReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || rec.Code != 200 || reply.Outcome != "not_ready" ||
		reply.Message != "OpenAbstractions is waiting for your answer to keep Example <b>. Answer its question, then select Open again." {
		t.Fatalf("unanswered Keep: %d %s", rec.Code, rec.Body)
	}
	if len(apps.asked) != 1 || len(apps.activated) != 0 {
		t.Fatalf("asked %v, activated %v", apps.asked, apps.activated)
	}
}

// Removing the app at the Keep question opens nothing.
func TestOpenOnAnAppRemovedAtTheKeepQuestionOpensNothing(t *testing.T) {
	apps, p, body := appKeepingPanel(t, 0)
	p.comfyApps = &appRemovingDirectory{appKeepingDirectory: apps}
	rec := appPagePost(t, p, "/app/example/start", body)
	var reply appActionReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.Outcome != "unknown" || reply.Message != "Example <b> was removed." || len(apps.activated) != 0 {
		t.Fatalf("removed at Keep: %d %s", rec.Code, rec.Body)
	}
}

// appRemovingDirectory removes the entry when the person answers the Keep
// question with Remove.
type appRemovingDirectory struct{ *appKeepingDirectory }

func (k *appRemovingDirectory) AskConfirmation(ctx context.Context, name string) (fwire.ApplicationChange, error) {
	change, err := k.appKeepingDirectory.AskConfirmation(ctx, name)
	k.entries = nil
	return change, err
}

// An app registered through its manifest is opened through the native
// applications@3 owner, whose page carries no kept state. On R24 its Open was
// refused without a Keep question. The directory Apps lists says it waits
// for Keep; Open asks there and opens through the native owner.
func TestOpenOnANativeAppThatAddedItselfAsksKeepThroughTheDirectory(t *testing.T) {
	apps, p, _ := appKeepingPanel(t, 3)
	d := apps.entries[0].Descriptor
	native := &appPageDirectory{entries: []fwire.ApplicationEntry{{Descriptor: d, Scope: fwire.ScopeLocal}}, activate: fwire.ApplicationActivationOutcomeReady}
	saved := appPageNative
	appPageNative = func(*servicePanel, context.Context, string) (setupApplications, error) { return native, nil }
	t.Cleanup(func() { appPageNative = saved })
	raw, _ := json.Marshal(appActionBody{Confirm: true, Shown: applicationDigest(&native.entries[0].Descriptor)})
	rec := appPagePost(t, p, "/app/example/start", string(raw))
	var reply appActionReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.Outcome != "ready" || len(apps.asked) != 1 || len(native.activated) != 1 || len(apps.activated) != 0 {
		t.Fatalf("native Open after Keep: %d %s asked %v native %v directory %v", rec.Code, rec.Body, apps.asked, native.activated, apps.activated)
	}
}
