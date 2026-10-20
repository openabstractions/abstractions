package main

import (
	"context"
	"encoding/json"
	"errors"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type nativeApplicationExchange func([]byte) ([]byte, error)

func (f nativeApplicationExchange) ExchangeFrame(b []byte) ([]byte, error) { return f(b) }

func TestNativeApplicationReadsOnlyItsFacetAndPresence(t *testing.T) {
	row := nativeRegistrationFixture(registrationwire.Manifest{Version: "applications@3", Name: "own", Title: "Own", Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}}, "/apps/owner", "r1", "", nil)
	row.Spec.Applications = append([]registrationwire.ApplicationFacet{{Name: "other", Manifest: registrationwire.Manifest{Version: "applications@3", Name: "other", Title: "Other", ScriptHost: true, Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}}}}, row.Spec.Applications...)
	row.Applications = []registrationwire.ApplicationReading{{Name: "other", ManifestDigest: strings.Repeat("a", 64), OwnedInstances: []string{"other-owned"}}}
	if _, err := registrationwire.DecodeRegistration(registrationwire.EncodeRegistration(&row)); err != nil {
		t.Fatalf("invalid received-facts fixture: %v", err)
	}
	n := &nativeApplications{application: "own", transport: func(context.Context) applications.ApplicationsTransport {
		return nativeApplicationExchange(func([]byte) ([]byte, error) {
			value := applications.EncodePage(&applications.Page{Outcome: applications.OutcomePage, Registrations: []registrationwire.Registration{row}})
			return json.Marshal(map[string]any{"version": 1, "service": "abstraction.facade/applications@3", "method": "Observe", "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
		})
	}}
	page, err := n.Observe(context.Background(), "", 0)
	if err != nil || len(page.Applications) != 1 || page.Applications[0].Descriptor.Name != "own" || n.owned || n.scriptHost || len(page.Applications[0].Instances) != 0 {
		t.Fatalf("another facet supplied native facts: page=%+v owned=%v script=%v err=%v", page, n.owned, n.scriptHost, err)
	}
}

// The declaration is received through the existing generated @3 client. This
// inert receiver supplies no signed-image or installed registration proof.
func TestNativeDeclaredScriptHostNeverOfferedOrEnrolledAsAssistant(t *testing.T) {
	own(t)
	assistantQuietFixture(t, func(string) rwire.DecisionOutcome { return rwire.DecisionOutcomeForbidden })
	program := filepath.Join(t.TempDir(), "declared-host.exe")
	if err := os.WriteFile(program, []byte("ordinary inert program fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &nativeApplications{application: "declared-host"}
	n.transport = func(context.Context) applications.ApplicationsTransport {
		return nativeApplicationExchange(func(b []byte) ([]byte, error) {
			var frame struct{ Service, Method string }
			if err := json.Unmarshal(b, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Service != "abstraction.facade/applications@3" || frame.Method != "Observe" {
				t.Fatalf("unexpected native call: %s", b)
			}
			manifest := registrationwire.Manifest{Version: "applications@3", Name: "declared-host", Title: "Declared host", ScriptHost: true, Host: registrationwire.HostRecipe{ReadinessDeadlineMs: 1000}, Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}}
			value := applications.EncodePage(&applications.Page{Outcome: applications.OutcomePage, Registrations: []registrationwire.Registration{nativeRegistrationFixture(manifest, program, "declared-role-revision", strings.Repeat("a", 64), nil)}})
			return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
		})
	}
	ctx := context.Background()
	page, err := n.client(ctx).Observe(applications.Query{Application: n.application, Limit: 1})
	if err != nil || len(page.Registrations) != 1 || !page.Registrations[0].Spec.Applications[0].Manifest.ScriptHost {
		t.Fatalf("generated native role was not received: %+v %v", page, err)
	}
	p := &servicePanel{comfyApps: n}
	if got := p.assistantCandidates(ctx, nil); len(got) != 0 {
		t.Errorf("declared script host offered as an assistant: %q", got)
	}
	if !n.scriptHost {
		t.Error("native display projection dropped declared host metadata")
	}
	if err := p.refuseAssistant(ctx, program); err == nil {
		t.Error("declared script host passed enrollment eligibility")
	}
}

// nativeLaunchHelper is an absolute program on this platform; identity
// compares only absolute programs.
var nativeLaunchHelper = map[bool]string{true: `C:\OA\launch-helper.exe`, false: "/opt/oa/launch-helper"}[filepath.Separator == '\\']

func TestNativeApplicationClientUsesSignedProgramAndConditionalContract(t *testing.T) {
	var methods []string
	n := &nativeApplications{application: "fixture-native"}
	n.transport = func(context.Context) applications.ApplicationsTransport {
		return nativeApplicationExchange(func(b []byte) ([]byte, error) {
			var frame struct {
				Service, Method string
				Arguments       json.RawMessage
			}
			if err := json.Unmarshal(b, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Service != "abstraction.facade/applications@3" {
				t.Fatalf("legacy contract: %s", b)
			}
			methods = append(methods, frame.Method)
			var value []byte
			switch frame.Method {
			case "Observe":
				manifest := registrationwire.Manifest{Version: "applications@3", Name: "fixture-native", Title: "Native fixture", Offers: appmodel.CommandDeclaration{Version: "abstraction.facade/appmodel@1"}, Verbs: []registrationwire.ActivationVerb{{Name: "open", Class: appmodel.CommandClassReveal, InputSchema: applications.Raw(`{"type":"object","properties":{},"additionalProperties":false}`)}}}
				value = applications.EncodePage(&applications.Page{Outcome: applications.OutcomePage, Registrations: []registrationwire.Registration{nativeRegistrationFixture(manifest, nativeLaunchHelper, "observed-revision", strings.Repeat("a", 64), []string{"owned"})}})
			case "Register":
				var args struct{ Request applications.RegisterRequest }
				if err := json.Unmarshal(frame.Arguments, &args); err != nil {
					t.Fatal(err)
				}
				if args.Request.Program != nativeLaunchHelper || args.Request.ExpectedRevision != "" || strings.Contains(string(frame.Arguments), "arguments") {
					t.Fatalf("registration supplied recipe authority: %s", b)
				}
				value = applications.EncodeChange(&applications.Change{Outcome: applications.OutcomeApplied})
			case "Remove":
				var args struct{ Request json.RawMessage }
				if err := json.Unmarshal(frame.Arguments, &args); err != nil {
					t.Fatal(err)
				}
				request, err := applications.DecodeChangeRequest(args.Request)
				if err != nil {
					t.Fatal(err)
				}
				if request.ExpectedRevision != "observed-revision" {
					t.Fatalf("lost observed revision: %s", b)
				}
				value = applications.EncodeChange(&applications.Change{Outcome: applications.OutcomeStale})
			case "Activate":
				var args struct {
					Key     string
					Request json.RawMessage
				}
				if err := json.Unmarshal(frame.Arguments, &args); err != nil {
					t.Fatal(err)
				}
				request, err := applications.DecodeActivateRequest(args.Request)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(args.Key, "a3.") || request.Application != "fixture-native" || request.Verb != "open" || string(request.Payload) != "{}" {
					t.Fatalf("native activation differs: %s", b)
				}
				value = applications.EncodeActivationResult(&applications.ActivationResult{Outcome: applications.ActivationOutcomeReady, Instance: "owned"})
			default:
				t.Fatalf("unexpected method: %s", b)
			}
			return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
		})
	}
	ctx := context.Background()
	if _, err := n.Remove(ctx, "fixture-native"); err == nil {
		t.Fatal("removed without an observed revision")
	}
	page, err := n.Observe(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Applications) != 1 || len(page.Applications[0].Instances) != 0 || !n.owned || page.Applications[0].Descriptor.Activation == nil {
		t.Fatal("native channel or manifest projection differs")
	}
	change, err := n.Remove(ctx, "fixture-native")
	if err != nil || change.Outcome != fwire.ApplicationOutcomeStale {
		t.Fatalf("stale removal lost: %+v %v", change, err)
	}
	if _, err := n.Register(ctx, fwire.ApplicationDescriptor{Program: nativeLaunchHelper}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Activate(ctx, "fixture-native"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "Observe,Remove,Register,Observe,Activate" {
		t.Fatal(methods)
	}
}

func TestNativeApplicationActivationRetainsKeyAndDeliveryFailures(t *testing.T) {
	for _, keyFailure := range []bool{true, false} {
		n := &nativeApplications{application: "fixture-native", canOpen: true}
		failure := errors.New("inert failure")
		calls := 0
		n.transport = func(context.Context) applications.ApplicationsTransport {
			return nativeApplicationExchange(func([]byte) ([]byte, error) { calls++; return nil, failure })
		}
		if keyFailure {
			n.requestKey = func() (string, error) { return "", failure }
		}
		if _, err := n.Activate(context.Background(), "fixture-native"); err == nil {
			t.Fatal("failure became a successful activation")
		}
		want := 1
		if keyFailure {
			want = 0
		}
		if calls != want {
			t.Fatalf("delivery was retried or sent without a key: %d", calls)
		}
	}
}

func TestNativeApplicationObserveRestartsStaleWithoutRetainingState(t *testing.T) {
	n := &nativeApplications{application: "app", revision: "old", owned: true, canOpen: true, scriptHost: true}
	calls := 0
	n.transport = func(context.Context) applications.ApplicationsTransport {
		return nativeApplicationExchange(func(raw []byte) ([]byte, error) {
			calls++
			var frame struct {
				Service, Method string
				Arguments       struct{ Query applications.Query }
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatal(err)
			}
			if calls == 1 && frame.Arguments.Query.Cursor != "old" {
				t.Fatal("missing old cursor")
			}
			if calls == 2 && frame.Arguments.Query.Cursor != "" {
				t.Fatal("restart reused opaque cursor")
			}
			outcome := applications.OutcomeStale
			if calls == 2 {
				outcome = applications.OutcomePage
			}
			value := applications.EncodePage(&applications.Page{Outcome: outcome, Registrations: []registrationwire.Registration{}})
			return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
		})
	}
	page, err := n.Observe(context.Background(), "old", 0)
	if err != nil || page.Outcome != fwire.ApplicationOutcomePage || calls != 2 || n.revision != "" || n.owned || n.canOpen || n.scriptHost {
		t.Fatalf("stale state survived %+v %v calls%d", page, err, calls)
	}
	n.revision = "cached"
	n.canOpen = true
	n.transport = func(context.Context) applications.ApplicationsTransport {
		return nativeApplicationExchange(func([]byte) ([]byte, error) { return nil, errors.New("unavailable") })
	}
	if _, err := n.Observe(context.Background(), "", 0); err == nil || n.revision != "" || n.canOpen {
		t.Fatal("failed native read retained control state")
	}
}

// Setup's re-pin is the operator's Register naming the revision Observe read;
// a stale revision's conflict is returned as it is.
func TestNativeApplicationReplaceNamesTheObservedRevision(t *testing.T) {
	var methods []string
	conflict := false
	n := &nativeApplications{application: "fixture-native"}
	n.transport = func(context.Context) applications.ApplicationsTransport {
		return nativeApplicationExchange(func(b []byte) ([]byte, error) {
			var frame struct {
				Service, Method string
				Arguments       json.RawMessage
			}
			if err := json.Unmarshal(b, &frame); err != nil {
				t.Fatal(err)
			}
			methods = append(methods, frame.Method)
			var value []byte
			switch frame.Method {
			case "Observe":
				manifest := registrationwire.Manifest{Version: "applications@3", Name: "fixture-native", Title: "Native fixture", Offers: appmodel.CommandDeclaration{Version: "abstraction.facade/appmodel@1"}}
				value = applications.EncodePage(&applications.Page{Outcome: applications.OutcomePage, Registrations: []registrationwire.Registration{nativeRegistrationFixture(manifest, nativeLaunchHelper, "observed-revision", strings.Repeat("a", 64), nil)}})
			case "Register":
				var args struct{ Request json.RawMessage }
				if err := json.Unmarshal(frame.Arguments, &args); err != nil {
					t.Fatal(err)
				}
				request, err := applications.DecodeRegisterRequest(args.Request)
				if err != nil {
					t.Fatal(err)
				}
				if request.Program != nativeLaunchHelper || request.ExpectedRevision != "observed-revision" {
					t.Fatalf("the re-pin lost the observed revision: %s", b)
				}
				outcome := applications.OutcomeApplied
				if conflict {
					outcome = applications.OutcomeConflict
				}
				value = applications.EncodeChange(&applications.Change{Outcome: outcome, Reason: map[bool]string{true: "revision"}[conflict]})
			default:
				t.Fatalf("unexpected method: %s", b)
			}
			return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
		})
	}
	ctx := context.Background()
	if _, err := n.Replace(ctx, nativeLaunchHelper); err == nil {
		t.Fatal("replaced without an observed revision")
	}
	if _, err := n.Observe(ctx, "", 0); err != nil {
		t.Fatal(err)
	}
	if change, err := n.Replace(ctx, nativeLaunchHelper); err != nil || change.Outcome != fwire.ApplicationOutcomeApplied {
		t.Fatalf("re-pin: %+v %v", change, err)
	}
	conflict = true
	if change, err := n.Replace(ctx, nativeLaunchHelper); err != nil || change.Outcome != fwire.ApplicationOutcomeConflict || change.Reason != "revision" {
		t.Fatalf("stale re-pin: %+v %v", change, err)
	}
	if strings.Join(methods, ",") != "Observe,Register,Observe,Register" {
		t.Fatal(methods)
	}
}
