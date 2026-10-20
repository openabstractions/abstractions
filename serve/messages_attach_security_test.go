package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// messageStartFixtureConsent returns the sender's consent for the fixture's
// receiver recipe, as progressMessageStart computes it.
func messageStartFixtureConsent(t *testing.T, h *applicationsHost, r *messageRecord) messageStartConsent {
	t.Helper()
	target, err := h.directory.prepareMessageStart(context.Background(), "receiver")
	if err != nil {
		t.Fatal(err)
	}
	consent, ok := h.messageStartConsent(context.Background(), r, target)
	if !ok {
		t.Fatal("fixture sender not admitted")
	}
	return consent
}

// VISION 2026-10-31: the start permit binds the sentence it was granted
// under. A permit granted under the earlier sentence admits nothing and the
// person is asked once more; a "Never allow" given under it stays in force.
func TestMessageStartPermitUnderEarlierSentenceAsksAgain(t *testing.T) {
	for _, c := range []struct {
		previous, earlier bool
	}{{false, true}, {false, false}, {true, true}, {true, false}} {
		earlier := c.earlier
		h, key, r, calls, _ := messagesStartProgressFixture(t)
		consent := messageStartFixtureConsent(t, h, r)
		if consent.resource == consent.legacy || consent.resource == consent.previous || consent.previous == "" {
			t.Fatal("resource does not bind the sentence")
		}
		for _, resource := range []string{consent.resource, consent.legacy, consent.previous} {
			if _, ok := grants.MessageStartResource(resource); !ok {
				t.Fatal(resource)
			}
		}
		earlierResource := consent.legacy
		if c.previous {
			earlierResource = consent.previous
		}
		if err := h.messageRights.policy.Set(consent.subject, grants.ApplicationMessageStartAction, earlierResource, earlier); err != nil {
			t.Fatal(err)
		}
		asked := ""
		h.messageRights.firstUse = func(_ rights.Subject, _ func() asks.Publisher, _ string, resource string) string {
			asked = resource
			return "pending"
		}
		if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 0 {
			t.Fatal("earlier-sentence decision started the receiver", earlier)
		}
		if earlier && asked != consent.resource || !earlier && asked != "" {
			t.Fatalf("earlier permit %v asked %q", earlier, asked)
		}
	}
}

// The resource changes with the owner's sentence and with nothing the sender
// claims; a paired subject's resource differs from every native one and
// changes with a new pairing.
func TestMessageStartResourceBindsSentenceAndPairing(t *testing.T) {
	target := applicationMessageStartRequest{Application: "receiver", Program: "C:\\receiver.exe", ProgramKey: "key", EnrollmentRevision: "1", RegistrationBinding: "b", ProfileFingerprint: "p", Session: "s", Context: "ctx"}
	paired := messagePairedStartResource("aa", "g1", target)
	if paired == messagePairedStartResource("aa", "g2", target) || paired == messagePairedStartResource("bb", "g1", target) {
		t.Fatal("paired resource ignores the pinned key or the pairing generation")
	}
	if _, ok := grants.MessageStartResource(paired); !ok {
		t.Fatal(paired)
	}
	// The bound sentence carries the display name as a slot the popup fills
	// in; the receiver's program and recipe in the resource tie the permit.
	if !strings.Contains(messageStartWording(), grants.MessageStartDisplaySlot) || !strings.Contains(messageStartWording(), "also while you're away") || messageStartWording() == messageStartPreviousWording() {
		t.Fatal("bound wording", messageStartWording())
	}
	other := target
	other.Application, other.Program = "other", "C:\\other.exe"
	if messageStartResource(applications.CapturedAgentApplication{}, target) == messageStartResource(applications.CapturedAgentApplication{}, other) {
		t.Fatal("resource not tied to the receiver program")
	}
}

// Every woken turn has an Activity row naming the conversation's folder
// label, never a path, and carrying the exact start permit the person can
// revoke from that row.
func TestMessageStartActivityNamesFolderAndPermit(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Cwd: work, Origin: msg.ChatOriginDesktop, Mode: msg.ChatModePlan}
	h, key, r, calls, _ := messagesStartProgressFixtureWith(t, "", "claude-v1", &msg.ChatBinding{ChatID: launch.Session, Launch: launch})
	sink := &messageAuditSink{}
	if err := h.configureMessageAudit(sink); err != nil {
		t.Fatal(err)
	}
	defer h.messageAudit.close()
	if err := h.messages.updateMessageNotice(context.Background(), key, r.Token, func(current *messageRecord) {
		entry, err := h.messageAudit.prepare(current)
		if err != nil {
			t.Fatal(err)
		}
		current.Audit = entry
	}); err != nil {
		t.Fatal(err)
	}
	current, err := h.messages.readRecord(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	resource := allowMessageStartFixture(t, h, key, current)
	if err := h.progressMessageStartSettled(context.Background(), key, current); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("attach start", calls.Load())
	}
	var row map[string]string
	for _, record := range sink.records {
		if record.Attrs["message_id"] == r.ID+":start" {
			row = record.Attrs
		}
	}
	if row == nil || row["to_folder"] != "work" || row["permit_action"] != grants.ApplicationMessageStartAction || row["permit_resource"] != resource || row["permit_program"] != r.Actor.Program || row["account"] != r.Actor.Account {
		t.Fatal("start Activity row", row)
	}
	// The row's permit is the one the start ran under: revoking it ends the
	// direction, and the next question asks the person again.
	if err := h.messageRights.policy.Revoke(rights.Subject{Account: row["account"], Program: row["permit_program"]}, row["permit_action"], row["permit_resource"]); err != nil {
		t.Fatal(err)
	}
	if got := h.messageRights.decide(context.Background(), rights.Subject{Account: r.Actor.Account, Program: r.Actor.Program}, grants.ApplicationMessageStartAction, resource).Outcome; got != rights.DecisionOutcomeNotGranted {
		t.Fatal("revoked from the Activity row but still", got)
	}
}
