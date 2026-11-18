package main

import (
	"bytes"
	"encoding/json"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"strings"
	"testing"
)

func gatewayDescriptionFixture() *appmodel.Description {
	return &appmodel.Description{Version: "appmodel@1", Kinds: []appmodel.Kind{{Name: "node", Schema: `{}`, Fields: []appmodel.KindField{}}}, Commands: []appmodel.CommandSpec{{Name: "node.x", Class: appmodel.CommandClassEdit, Title: "Edit", InputSchema: `{}`, OutputSchema: `{}`, Targets: []string{}}}, Extensions: []string{}, Bounds: appmodel.ContextBounds{SnapshotBytes: 4096, ChangeBytes: 1024, RetainedRevisions: 16, PendingProposals: 1, CallsInFlight: 1}}
}

func TestApplicationV3GatewayDescriptionOrder(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*appmodel.Description)
	}{
		{"version before size", "version:future", func(d *appmodel.Description) { d.Version = "future"; d.Kinds = nil }},
		{"size before kind", "description", func(d *appmodel.Description) { d.Kinds = nil; d.Commands[0].Name = "bad" }},
		{"duplicate kind before targets", "kind:node", func(d *appmodel.Description) {
			d.Kinds = append(d.Kinds, d.Kinds[0])
			d.Commands[0].Targets = []string{"edge"}
		}},
		{"kind schema before fields", "schema:node:oneOf", func(d *appmodel.Description) {
			d.Kinds[0].Schema = `{"oneOf":[]}`
			d.Kinds[0].Fields = []appmodel.KindField{{Name: "", Role: appmodel.FieldRoleOrdinary}}
		}},
		{"fields before command", "fields:node", func(d *appmodel.Description) {
			d.Kinds[0].Fields = []appmodel.KindField{{Name: "", Role: appmodel.FieldRoleOrdinary}}
			d.Commands[0].Name = "bad"
		}},
		{"name before kind", "name:missing.BAD", func(d *appmodel.Description) { d.Commands[0].Name = "missing.BAD" }},
		{"total name bytes", "name:" + strings.Repeat("a", 64) + "." + strings.Repeat("b", 64), func(d *appmodel.Description) {
			d.Kinds[0].Name = strings.Repeat("a", 64)
			d.Commands[0].Name = d.Kinds[0].Name + "." + strings.Repeat("b", 64)
			d.Commands[0].Targets = []string{"missing"}
			d.Commands[0].InputSchema = `{"oneOf":[]}`
		}},
		{"unknown kind", "kind:zebra.run", func(d *appmodel.Description) { d.Commands[0].Name = "zebra.run" }},
		{"targets before flags and bounds", "targets:node.x", func(d *appmodel.Description) {
			d.Commands[0].Targets = []string{"edge"}
			d.Commands[0].Proposable = true
			d.Bounds.SnapshotBytes = 1
		}},
		{"proposable before undoable", "proposable:node.x", func(d *appmodel.Description) {
			d.Commands[0].Class = appmodel.CommandClassRead
			d.Commands[0].Proposable = true
			d.Commands[0].Undoable = true
		}},
		{"undoable before longrunning", "undoable:node.x", func(d *appmodel.Description) {
			d.Commands[0].Class = appmodel.CommandClassRead
			d.Commands[0].Undoable = true
			d.Commands[0].LongRunning = true
		}},
		{"longrunning before title", "long_running:node.x", func(d *appmodel.Description) { d.Commands[0].LongRunning = true; d.Commands[0].Title = "" }},
		{"title before schemas", "title:node.x", func(d *appmodel.Description) { d.Commands[0].Title = ""; d.Commands[0].InputSchema = `{"oneOf":[]}` }},
		{"output schema before bounds", "schema:node.x:oneOf", func(d *appmodel.Description) { d.Commands[0].OutputSchema = `{"oneOf":[]}`; d.Bounds.SnapshotBytes = 1 }},
		{"input schema names $schema", "schema:node.x:$schema", func(d *appmodel.Description) {
			d.Commands[0].InputSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`
		}},
		{"nested format names format", "schema:node:format", func(d *appmodel.Description) {
			d.Kinds[0].Schema = `{"type":"object","properties":{"at":{"type":"string","format":"date-time"}}}`
		}},
		{"unnameable keyword keeps the bare word", "schema:node", func(d *appmodel.Description) { d.Kinds[0].Schema = `{"bad key":1}` }},
		{"snapshot above max", "bounds", func(d *appmodel.Description) { d.Bounds.SnapshotBytes = 262145 }},
		{"change above max", "bounds", func(d *appmodel.Description) { d.Bounds.ChangeBytes = 262145 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := gatewayDescriptionFixture()
			tc.change(d)
			err := validateApplicationV3Description(d)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v want %s", err, tc.want)
			}
		})
	}
	for _, size := range []int{127, 128} {
		d := gatewayDescriptionFixture()
		d.Kinds[0].Name = strings.Repeat("a", 64)
		d.Commands[0].Name = d.Kinds[0].Name + "." + strings.Repeat("b", size-65)
		if err := validateApplicationV3Description(d); err != nil {
			t.Fatalf("valid %d-byte command name: %v", size, err)
		}
	}
	d := gatewayDescriptionFixture()
	d.Commands[0].Undoable = true
	d.Bounds.SnapshotBytes = 262144
	d.Bounds.ChangeBytes = 262144
	if err := validateApplicationV3Description(d); err != nil {
		t.Fatalf("valid undoable edit without undo extension: %v", err)
	}
}

func TestApplicationV3GatewayMaximumGeneratedReplies(t *testing.T) {
	for _, pattern := range []string{`"`, `\`, `<`, `&`, "é", "<>&\"\\é"} {
		t.Run(pattern, func(t *testing.T) {
			properties := func(n int) string {
				var effects bytes.Buffer
				encoder := json.NewEncoder(&effects)
				encoder.SetEscapeHTML(false)
				_ = encoder.Encode(map[string]string{"value": strings.Repeat(pattern, n)})
				return strings.TrimSpace(effects.String())
			}
			ref := appmodel.ContextRef{Instance: "i", Context: "c", Epoch: "e"}
			for _, record := range []struct {
				name string
				make func(int) *appmodel.Envelope
			}{
				{"command", func(n int) *appmodel.Envelope {
					return &appmodel.Envelope{Result: &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Effects: properties(n)}}
				}},
				{"snapshot", func(n int) *appmodel.Envelope {
					return &appmodel.Envelope{Snapshot: &appmodel.ContextSnapshot{Outcome: appmodel.ReadingOutcomePage, Context: ref, Entities: []appmodel.Entity{{ID: "id", Kind: "node", Properties: properties(n)}}, Collections: []appmodel.Collection{}, Selections: []appmodel.Selection{}}}
				}},
				{"changes", func(n int) *appmodel.Envelope {
					return &appmodel.Envelope{Changes: &appmodel.ChangeBatch{Outcome: appmodel.ReadingOutcomePage, Context: ref, Changes: []appmodel.Change{{Patch: `[{"op":"replace","path":"/entities/id/properties","value":` + properties(n) + `}]`}}}}
				}},
				{"description", func(n int) *appmodel.Envelope {
					d := gatewayDescriptionFixture()
					d.Extensions = make([]string, n)
					for i := range d.Extensions {
						d.Extensions[i] = pattern
					}
					return &appmodel.Envelope{Description: &appmodel.DescriptionResult{Outcome: appmodel.ReadingOutcomePage, Description: d}}
				}},
			} {
				t.Run(record.name, func(t *testing.T) {
					encode := func(n int) []byte {
						e := record.make(n)
						var value any
						switch {
						case e.Result != nil:
							value = e.Result
						case e.Snapshot != nil:
							value = e.Snapshot
						case e.Changes != nil:
							value = e.Changes
						case e.Description != nil:
							value = e.Description
						}
						raw, err := appmodel.ToJSON(value)
						if err != nil {
							t.Fatal(err)
						}
						return raw
					}
					boundedSize := func(n int) int {
						e := record.make(n)
						if e.Description != nil {
							return len(appmodel.EncodeDescription(e.Description.Description))
						}
						return len(encode(n))
					}
					low, high := 0, 262144
					for low < high {
						mid := (low + high + 1) / 2
						if boundedSize(mid) <= 262144 {
							low = mid
						} else {
							high = mid - 1
						}
					}
					if boundedSize(low) < 262144-64 || boundedSize(low) > 262144 || boundedSize(low+1) <= 262144 {
						t.Fatalf("record is not maximal at its owning bound: size=%d next=%d", boundedSize(low), boundedSize(low+1))
					}
					reply := applications.CallResult{Outcome: applications.OutcomeApplied, Answer: record.make(low)}
					size, err := measureApplicationV3Reply(reply)
					t.Logf("owner-bound-bytes=%d answer-record=%d generated-applications-reply=%d limit=%d", boundedSize(low), len(encode(low)), size, ownedApplicationFrameLimit)
					if err != nil || !applicationV3ReplyFits(reply) {
						t.Fatalf("maximal generated reply refused: size=%d err=%v", size, err)
					}
				})
			}
		})
	}
}

func TestApplicationV3GatewayOversizedCallReplyIsUnknown(t *testing.T) {
	result := applications.CallResult{Outcome: applications.OutcomeApplied, Answer: &appmodel.Envelope{Result: &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Effects: `{"value":"` + strings.Repeat("x", ownedApplicationFrameLimit) + `"}`}}}
	got := boundedApplicationV3CallReply(result)
	if got.Outcome != applications.OutcomeUnknown || got.Reason != "bound:reply_bytes" || got.Answer != nil {
		t.Fatalf("oversized result: %+v", got)
	}
}

func TestApplicationV3GatewayToolArguments(t *testing.T) {
	ref := appmodel.ContextRef{Instance: "i1", Context: "c", Epoch: "e"}
	for _, row := range []struct {
		tool  string
		value any
		class appmodel.CommandClass
	}{
		{"app_read", &appmodel.ReadRequest{Context: ref, Entities: []string{}, Limit: 1}, appmodel.CommandClassRead},
		{"app_changes", &appmodel.ChangesRequest{Context: ref}, appmodel.CommandClassRead},
		{"app_reveal", &appmodel.Reveal{Context: ref, Entities: []string{"entity"}, Style: appmodel.RevealStyleHighlight}, appmodel.CommandClassReveal},
		{"app_undo", &appmodel.UndoRequest{Context: ref, UndoGroup: "u", ExpectedRevision: "r", IdempotencyKey: "k"}, appmodel.CommandClassEdit},
		{"app_job", &appmodel.JobRequest{Instance: "i1", Job: "j"}, appmodel.CommandClassRead},
	} {
		t.Run(row.tool, func(t *testing.T) {
			raw, err := appmodel.ToJSON(row.value)
			if err != nil {
				t.Fatal(err)
			}
			call := appmodel.GatewayCall{Application: "app", Instance: "i1", Tool: row.tool, Arguments: appmodel.Raw(raw)}
			v, err := decodeApplicationV3Invocation(call)
			if err != nil || v.class != row.class {
				t.Fatalf("%+v %v", v, err)
			}
			call.Instance = "foreign"
			if _, err = decodeApplicationV3Invocation(call); err == nil || err.Error() != "instance" {
				t.Fatalf("wrong instance accepted: %v", err)
			}
			call.Arguments = appmodel.Raw(`{"unknown":true}`)
			if _, err = decodeApplicationV3Invocation(call); err == nil {
				t.Fatal("wrong shape accepted")
			}
		})
	}
	// A command's arguments is its input_schema object; the gateway builds the
	// Invocation later. A binding tool's call carries no command-only member.
	mode := appmodel.InvokeModePropose
	command := appmodel.GatewayCall{Application: "app", Instance: "i1", Tool: "node.read", Arguments: appmodel.Raw(`{"node":"5"}`), Mode: &mode, IdempotencyKey: "k1", Context: "c", ExpectedRevision: "r1"}
	if v, err := decodeApplicationV3Invocation(command); err != nil || v.mode != appmodel.InvokeModePropose || v.key != "k1" || v.contextName != "c" || v.revisionBefore != "r1" || v.context != nil {
		t.Fatalf("command call: %+v %v", v, err)
	}
	for _, arguments := range []string{`[]`, `"x"`, `null`} {
		command.Arguments = appmodel.Raw(arguments)
		if _, err := decodeApplicationV3Invocation(command); err == nil || err.Error() != "field:$:type" {
			t.Fatalf("%s: %v", arguments, err)
		}
	}
	for member, call := range map[string]appmodel.GatewayCall{
		"mode":            {Tool: "app_read", Mode: &mode},
		"idempotency_key": {Tool: "app_undo", IdempotencyKey: "k"},
		"context":         {Tool: "app_contexts", Context: "c"},
	} {
		call.Arguments = appmodel.Raw("{}")
		if _, err := decodeApplicationV3Invocation(call); err == nil || err.Error() != member {
			t.Fatalf("%s on %s: %v", member, call.Tool, err)
		}
	}
	for _, tool := range []string{"app_describe", "app_contexts"} {
		if _, err := decodeApplicationV3Invocation(appmodel.GatewayCall{Tool: tool, Arguments: appmodel.Raw(" {} \n")}); err != nil {
			t.Fatal(err)
		}
		if _, err := decodeApplicationV3Invocation(appmodel.GatewayCall{Tool: tool, Arguments: appmodel.Raw(`{"context":{}}`)}); err == nil {
			t.Fatal("nonempty binding input accepted")
		}
	}
}

func TestApplicationV3GatewayExpectedCanonicalAnswer(t *testing.T) {
	ref := appmodel.ContextRef{Instance: "i", Context: "c", Epoch: "e"}
	rows := []struct {
		tool  string
		value any
	}{
		{"app_describe", &appmodel.DescriptionResult{Outcome: appmodel.ReadingOutcomeUnavailable, Reason: "owned_host"}},
		{"app_contexts", &appmodel.ContextPage{Outcome: appmodel.ReadingOutcomePage, Contexts: []appmodel.ContextEntry{}}},
		{"app_read", &appmodel.ContextSnapshot{Outcome: appmodel.ReadingOutcomePage, Context: ref, Entities: []appmodel.Entity{}, Collections: []appmodel.Collection{}, Selections: []appmodel.Selection{}}},
		{"app_changes", &appmodel.ChangeBatch{Outcome: appmodel.ReadingOutcomePage, Context: ref, Changes: []appmodel.Change{}}},
		{"node.read", &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Effects: appmodel.Raw("{}")}},
	}
	for _, row := range rows {
		t.Run(row.tool, func(t *testing.T) {
			raw, err := appmodel.ToJSON(row.value)
			if err != nil {
				t.Fatal(err)
			}
			env, err := applicationV3Answer(row.tool, raw)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := appmodel.ToJSON(env)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = appmodel.DecodeOne(encoded); err != nil {
				t.Fatal(err)
			}
			if _, err = applicationV3Answer(row.tool, appmodel.EncodeEnvelope(&appmodel.Envelope{Call: &appmodel.GatewayCall{Application: "app", Instance: "i", Tool: "app_describe", Arguments: appmodel.Raw("{}")}})); err == nil {
				t.Fatal("request member accepted as answer")
			}
			if _, err = applicationV3Answer(row.tool, append([]byte(" "), raw...)); err == nil {
				t.Fatal("noncanonical text accepted")
			}
		})
	}
}

func TestApplicationV3GatewaySecretSnapshot(t *testing.T) {
	d := &appmodel.Description{Kinds: []appmodel.Kind{{Name: "node", Fields: []appmodel.KindField{{Name: "token", Role: appmodel.FieldRoleSecret}}}}}
	for _, raw := range []string{`{"ordinary":1}`, `{"token":null}`} {
		env := &appmodel.Envelope{Snapshot: &appmodel.ContextSnapshot{Entities: []appmodel.Entity{{Kind: "node", Properties: appmodel.Raw(raw)}}}}
		err := applicationV3SecretAnswer(d, env)
		if raw == `{"ordinary":1}` && err != nil {
			t.Fatal(err)
		}
		if raw != `{"ordinary":1}` && (err == nil || err.Error() != "secret:node.token") {
			t.Fatalf("secret disclosed: %v", err)
		}
	}
}

func TestApplicationV3GatewaySecretPatchAndEffects(t *testing.T) {
	d := &appmodel.Description{Kinds: []appmodel.Kind{{Name: "account", Fields: []appmodel.KindField{{Name: "api_key", Role: appmodel.FieldRoleOrdinary}}}, {Name: "node", Fields: []appmodel.KindField{{Name: "api_key", Role: appmodel.FieldRoleSecret}}}, {Name: "later", Fields: []appmodel.KindField{{Name: "api_key", Role: appmodel.FieldRoleSecret}}}}}
	for _, tc := range []struct {
		patch  string
		secret bool
	}{
		{`[{"op":"add","path":"/entities/a1","value":{"kind":"account","properties":{"api_key":"ordinary"}}}]`, false},
		{`[{"op":"replace","path":"/entities/a1/properties/api_key","value":"hidden"}]`, true},
		{`[{"op":"remove","path":"/entities/a1/properties/api_key"}]`, true},
		{`[{"op":"copy","path":"/entities/a1/properties/public","from":"/entities/n1/properties/api_key"}]`, true},
		{`[{"op":"add","path":"/entities/n1","value":{"kind":"node","properties":{"api_key":null}}}]`, true},
		{`[{"op":"add","path":"/partial","value":{"nested":{"properties":{"api_key":"hidden"}}}}]`, true},
		{`[{"op":"replace","path":"/entities/a1/properties/title","value":"ordinary"}]`, false},
		{`[{"op":"replace","path":"/entities/a1/properties","value":{"api_key":"hidden"}}]`, true},
		{`[{"op":"replace","path":"/entities/a1/properties","value":{"title":"ordinary"}}]`, false},
		{`[{"op":"copy","path":"/entities/a2","from":"/entities/a1"}]`, false},
		{`[{"op":"replace","path":"/entities/a1/kind","value":"account"}]`, false},
	} {
		err := applicationV3SecretAnswer(d, &appmodel.Envelope{Changes: &appmodel.ChangeBatch{Changes: []appmodel.Change{{Patch: tc.patch}}}})
		if tc.secret {
			if err == nil || err.Error() != "secret:node.api_key" {
				t.Fatalf("patch %s: %v", tc.patch, err)
			}
		} else if err != nil {
			t.Fatalf("ordinary patch: %v", err)
		}
	}
	for _, effects := range []string{`{"api_key":"hidden"}`, `{"nested":[{"api_key":null}]}`} {
		err := applicationV3SecretAnswer(d, &appmodel.Envelope{Result: &appmodel.CommandResult{Effects: effects}})
		if err == nil || err.Error() != "secret:node.api_key" {
			t.Fatalf("effects %s: %v", effects, err)
		}
	}
	if err := applicationV3SecretAnswer(d, &appmodel.Envelope{Result: &appmodel.CommandResult{Effects: `{"value":1}`}}); err != nil {
		t.Fatal(err)
	}
}

// An agent that follows a job with {"job": "j35"} alone learns the field it
// left out (owner's PC, R36: app_job answered missing_field).
func TestApplicationV3GatewayNamesTheMissingField(t *testing.T) {
	for _, row := range []struct{ tool, arguments, want string }{
		{"app_job", `{"job":"j35"}`, "missing_field:instance"},
		{"app_job", `{"instance":"i1"}`, "missing_field:job"},
		{"app_read", `{"entities":[],"limit":1}`, "missing_field:context"},
	} {
		_, err := decodeApplicationV3Invocation(appmodel.GatewayCall{Application: "app", Instance: "i1", Tool: row.tool, Arguments: appmodel.Raw(row.arguments)})
		if got := applicationV3CodecWord(err); got != row.want {
			t.Errorf("%s %s: %q, want %q", row.tool, row.arguments, got, row.want)
		}
	}
}
