package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestFirstUseAdmitsRightsValidLongTargetAndRejectsForeignAccount(t *testing.T) {
	a, book := firstUseBook(t)
	target := rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), strings.Repeat("p", 240), "app.exe")}
	resource := strings.Repeat("r", 1024)
	a.admitFirstUse(target, nil, host.ModelLookupAction, resource)
	records, _, err := book.ApplicationHistory()
	if err != nil || len(records) != 1 || records[0].Permission == nil || records[0].Permission.Targets[0].Program != target.Program || records[0].Permission.Targets[0].Resource != resource {
		t.Fatalf("long target missing: %+v %v", records, err)
	}
	// Account rejection is observable and cannot admit authority to this book.
	rejected := false
	a.report = func(error) { rejected = true }
	target.Account = "foreign"
	a.admitFirstUse(target, nil, host.ModelLookupAction, "hf")
	records, _, err = book.ApplicationHistory()
	if err != nil || len(records) != 1 || !rejected {
		t.Fatalf("foreign account: %+v rejected=%v %v", records, rejected, err)
	}
}

func TestCanonicalDelegationGrantRevokeRestartForBothRepresentations(t *testing.T) {
	for _, long := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "digest"}[long], func(t *testing.T) {
			a, _ := firstUseBook(t)
			policyPath := filepath.Join(t.TempDir(), "rights.json")
			policy, err := rights.LoadDecisionPolicy(policyPath, []string{asks.AnswerAction})
			if err != nil {
				t.Fatal(err)
			}
			a.rights.policy = policy
			responder := rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "responder.exe")}
			target := filepath.Join(t.TempDir(), "target.exe")
			if long {
				target = filepath.Join(t.TempDir(), strings.Repeat("p", 240), "target.exe")
			}
			name, err := asks.FirstUseAnswerResource(target, host.ModelLookupAction, "hf", "allow")
			if err != nil {
				t.Fatal(err)
			}
			prefix := "first-use@1:"
			if long {
				prefix = "first-use@2:"
			}
			if !strings.HasPrefix(name, prefix) {
				t.Fatalf("resource %s", name)
			}
			snapshot, err := policy.OperatorSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			permit := true
			edit, err := policy.ChangeRule(snapshot.Revision, responder, asks.AnswerAction, name, rights.RuleEdit{Permit: &permit, By: rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "panel.exe")}, TTL: 5 * time.Minute, Exact: true}, func() error { return nil })
			if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatal(edit, err)
			}
			if delegation, err := a.exactDelegation(responder, target, host.ModelLookupAction, "hf", "allow"); err != nil || delegation == nil {
				t.Fatal(delegation, err)
			}
			policy, err = rights.LoadDecisionPolicy(policyPath, []string{asks.AnswerAction})
			if err != nil {
				t.Fatal(err)
			}
			a.rights.policy = policy
			if delegation, err := a.exactDelegation(responder, target, host.ModelLookupAction, "hf", "allow"); err != nil || delegation == nil {
				t.Fatal("restart", delegation, err)
			}
			read := policy.ReadRule(responder, asks.AnswerAction, name)
			edit, err = policy.ChangeRule(read.Revision, responder, asks.AnswerAction, name, rights.RuleEdit{By: rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "panel.exe")}}, func() error { return nil })
			if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatal(edit, err)
			}
			policy, err = rights.LoadDecisionPolicy(policyPath, []string{asks.AnswerAction})
			if err != nil {
				t.Fatal(err)
			}
			a.rights.policy = policy
			if delegation, err := a.exactDelegation(responder, target, host.ModelLookupAction, "hf", "allow"); err == nil || delegation != nil {
				t.Fatal("revoked rule revived", delegation, err)
			}
		})
	}
}

func TestShortTupleDigestIsAnUnusedOpaqueRule(t *testing.T) {
	a, _ := firstUseBook(t)
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), []string{asks.AnswerAction})
	if err != nil {
		t.Fatal(err)
	}
	a.rights.policy = policy
	responder := rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "responder.exe")}
	target := filepath.Join(t.TempDir(), "target.exe")
	encoded, _ := json.Marshal([4]string{target, host.ModelLookupAction, "hf", "allow"})
	digest := sha256.Sum256(encoded)
	opaque := "first-use@2:" + hex.EncodeToString(digest[:])
	snapshot, err := policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	permit := true
	edit, err := policy.ChangeRule(snapshot.Revision, responder, asks.AnswerAction, opaque, rights.RuleEdit{Permit: &permit, By: rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "panel.exe")}, TTL: 5 * time.Minute, Exact: true}, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	if delegation, err := a.exactDelegation(responder, target, host.ModelLookupAction, "hf", "allow"); err == nil || delegation != nil {
		t.Fatal("short tuple used noncanonical digest", delegation, err)
	}
	// Neither a foreign target nor a foreign responder can bypass account binding.
	p, _ := asks.NewPermissionRequest(asks.PermissionTarget{Account: "foreign", Program: target, Action: host.ModelLookupAction, Resource: "hf"})
	if _, err := a.authorizeAnswerer(context.Background(), answerer{subject: responder}, asks.Record{Key: asks.FirstUseKey, Permission: &p}, a.scope, "allow"); err == nil {
		t.Fatal("foreign target accepted")
	}
}

func TestResponderAuthorityComesFromPinnedPermissionRatherThanWording(t *testing.T) {
	a, _ := firstUseBook(t)
	a.rights.operators = []string{filepath.Join(t.TempDir(), "runtime.exe")}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), []string{asks.AnswerAction, host.ModelLookupAction})
	if err != nil {
		t.Fatal(err)
	}
	a.rights.policy = policy
	a.tierFacts = grants.TierFacts{ListedRegistry: func(name string) bool { return name == "hf" }}
	responder := rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "responder.exe")}
	target := filepath.Join(t.TempDir(), "target.exe")
	permission, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: a.rights.owner, Program: target, Action: host.ModelLookupAction, Resource: "hf"})
	if err != nil {
		t.Fatal(err)
	}
	name, err := asks.FirstUseAnswerResource(target, host.ModelLookupAction, "hf", "allow")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	permit := true
	edit, err := policy.ChangeRule(snapshot.Revision, responder, asks.AnswerAction, name, rights.RuleEdit{Permit: &permit, By: rwire.Subject{Account: a.rights.owner, Program: filepath.Join(t.TempDir(), "panel.exe")}, TTL: 5 * time.Minute, Exact: true}, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	record := asks.Record{ID: "pinned-question", Key: asks.FirstUseKey, About: "A display label", Text: "Allow this application to look up a model?", Options: []string{"allow", "refuse", "never"}, Permission: &permission}
	effect, err := a.authorizeAnswerer(context.Background(), answerer{subject: responder}, record, a.scope, "allow")
	if err != nil || effect == nil {
		t.Fatalf("wording selected authority: %v", err)
	}
	// Even a valid exact grant cannot make the target answer its own question.
	if _, err := a.authorizeAnswerer(context.Background(), answerer{subject: rwire.Subject{Account: "foreign", Program: responder.Program}}, record, a.scope, "allow"); err == nil {
		t.Fatal("foreign responder accepted")
	}
	record.Permission = nil
	if _, err := a.authorizeAnswerer(context.Background(), answerer{subject: responder}, record, a.scope, "allow"); err == nil {
		t.Fatal("missing current permission fell back to text")
	}
}
