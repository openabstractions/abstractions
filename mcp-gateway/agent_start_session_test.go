package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func sessionTestBinding() startprofile.CustodyBinding {
	return startprofile.CustodyBinding{Account: "native-account", ProgramKey: "native-program", EnrollmentRevision: "enrollment-1", RecipeDigest: strings.Repeat("a", 64)}
}
func sessionTestResult(id string) []byte {
	raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": id, "result": "MODEL_TEXT_MUST_NOT_BE_RETAINED"})
	return raw
}

func TestAgentSessionCreationResumeAndUncertainCreation(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	binding := sessionTestBinding()
	first, err := beginAgentSession(ctx, state, binding, "owned-context", "native-config")
	if err != nil {
		t.Fatal(err)
	}
	if !agentSessionUUID.MatchString(first.record.Session) || !reflect.DeepEqual(first.arguments(), []string{"--session-id", first.record.Session}) {
		t.Fatal("new creation did not select its own UUID")
	}
	before, _ := os.ReadFile(first.path)
	if _, err := beginAgentSession(ctx, state, binding, "owned-context", "native-config"); agentSessionReason(err) != "session_creation_uncertain" {
		t.Fatal("uncertain creation permitted a fresh attempt", err)
	}
	after, _ := os.ReadFile(first.path)
	if string(before) != string(after) {
		t.Fatal("uncertain receipt changed")
	}
	if err := first.observe(sessionTestResult(first.record.Session)); err != nil {
		t.Fatal(err)
	}
	// Historical creation can be recorded after the canonical question ended.
	// It creates no launch permission; later callers need a separate native claim.
	if err := first.commit(ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(first.path)
	if strings.Contains(string(stored), "MODEL_TEXT_MUST_NOT_BE_RETAINED") {
		t.Fatal("model content entered custody")
	}
	second, err := beginAgentSession(ctx, state, binding, "owned-context", "native-config")
	if err != nil {
		t.Fatal(err)
	}
	if second.record.Session != first.record.Session || !reflect.DeepEqual(second.arguments(), []string{"--resume", first.record.Session}) {
		t.Fatal("resume selected another or latest session")
	}
	if err := second.observe([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"` + first.record.Session + `"}`)); agentSessionReason(err) != "session_result_unconfirmed" {
		t.Fatal("missing history/vendor failure accepted", err)
	}
	third, err := beginAgentSession(ctx, state, binding, "owned-context", "native-config")
	if err != nil || third.record.Session != first.record.Session || !third.resume {
		t.Fatal("a failed resume replaced historical session custody", err)
	}
}

func TestAgentSessionBindingChangesCannotSelectExistingOrNewSession(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	binding := sessionTestBinding()
	lease, err := beginAgentSession(ctx, state, binding, "owned-context", "native-config")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.observe(sessionTestResult(lease.record.Session)); err != nil {
		t.Fatal(err)
	}
	if err := lease.commit(ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(lease.path)
	for _, field := range []string{"account", "program", "enrollment", "recipe", "configuration"} {
		t.Run(field, func(t *testing.T) {
			changed := binding
			config := "native-config"
			switch field {
			case "account":
				changed.Account = "other-account"
			case "program":
				changed.ProgramKey = "other-program"
			case "enrollment":
				changed.EnrollmentRevision = "enrollment-2"
			case "recipe":
				changed.RecipeDigest = strings.Repeat("b", 64)
			case "configuration":
				config = "other-config"
			}
			if _, err := beginAgentSession(ctx, state, changed, "owned-context", config); err == nil {
				t.Fatal("changed ownership selected vendor history")
			}
			after, _ := os.ReadFile(lease.path)
			if string(before) != string(after) {
				t.Fatal("changed binding replaced historical custody")
			}
		})
	}
}

func TestAgentSessionNativeEnvelopeRejectsModelTextAndWrongIDs(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	foreign := "22222222-2222-4222-8222-222222222222"
	for _, raw := range []string{
		`{"result":"{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"` + id + `\"}"}`,
		`{"type":"assistant","subtype":"success","is_error":false,"session_id":"` + id + `"}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"` + foreign + `"}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"` + id + `","session_id":"` + foreign + `"}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"` + strings.ToUpper(foreign) + `"}`,
	} {
		if ok, err := nativeAgentSessionEnvelope([]byte(raw), id); err == nil || ok {
			t.Fatal("untrusted envelope supplied a session ID")
		}
	}
	init := []byte(`{"type":"system","subtype":"init","session_id":"` + id + `"}`)
	if ok, err := nativeAgentSessionEnvelope(init, id); err != nil || ok {
		t.Fatal("init alone confirmed durable creation", err)
	}
	if ok, err := nativeAgentSessionEnvelope(sessionTestResult(id), id); err != nil || !ok {
		t.Fatal("native result refused", err)
	}
}

func TestAgentSessionHistoricalRecordRefusesConfigSwapAndCorruption(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	lease, err := beginAgentSession(ctx, state, sessionTestBinding(), "owned-context", "native-config")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.observe(sessionTestResult(lease.record.Session)); err != nil {
		t.Fatal(err)
	}
	if err := lease.commit(ctx, func(context.Context) error { return errors.New("held configuration replaced") }); agentSessionReason(err) != "session_binding_changed" {
		t.Fatal(err)
	}
	if _, err := beginAgentSession(ctx, state, sessionTestBinding(), "owned-context", "native-config"); agentSessionReason(err) != "session_creation_uncertain" {
		t.Fatal("failed local facts cleared pending custody", err)
	}
	if err := os.WriteFile(lease.path, []byte(`{"schema":"oa.agent.session@1","session":"forged"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := beginAgentSession(ctx, state, sessionTestBinding(), "owned-context", "native-config"); agentSessionReason(err) != "session_custody_unavailable" {
		t.Fatal("corruption enrolled history", err)
	}
}

func TestAgentStartPrivateCustodyMetadataHasNoArgumentFallback(t *testing.T) {
	valid := sessionTestBinding()
	meta := valid.Metadata()
	if got, err := startprofile.ParseCustodyMetadata(meta); err != nil || got != valid {
		t.Fatal(got, err)
	}
	for _, bad := range []map[string]any{nil, {"arguments": meta}, {startprofile.CustodyMetaKey: "model text"}, {startprofile.CustodyMetaKey: map[string]any{"account": "spoofed"}}} {
		if _, err := startprofile.ParseCustodyMetadata(bad); err == nil {
			t.Fatal("missing/forged private metadata accepted")
		}
	}
	extra := valid.Metadata()
	extra[startprofile.CustodyMetaKey].(map[string]any)["session_id"] = "owner-conversation"
	if _, err := startprofile.ParseCustodyMetadata(extra); err == nil {
		t.Fatal("ordinary input enrolled a vendor session")
	}
}
