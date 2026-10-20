package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

type reviewPolicyFixture struct {
	revision      int
	rules         map[string]rights.RuleRecord
	writes        int
	failAt        int
	uncertain     bool
	badReadback   bool
	finalReadback bool
	readbacks     int
	stale         bool
	setter        rights.Subject
}

func (f *reviewPolicyFixture) rev() string { return fmt.Sprint("policy:", f.revision) }
func reviewRuleKey(s rights.Subject, action, resource string) string {
	return s.Account + "|" + s.Program + "|" + action + "|" + resource
}
func (f *reviewPolicyFixture) ReadRuleContext(_ context.Context, s rights.Subject, action, resource string) (rights.RuleRead, error) {
	if record, ok := f.rules[reviewRuleKey(s, action, resource)]; ok {
		if f.writes > 0 {
			f.readbacks++
		}
		if f.badReadback && f.writes > 0 {
			record.Expires = ""
		}
		if f.finalReadback && f.writes == 5 && f.readbacks > 5 {
			record.Why = "changed by another operator"
		}
		return rights.RuleRead{Outcome: rights.RuleReadOutcomeFound, Revision: f.rev(), Record: &record}, nil
	}
	return rights.RuleRead{Outcome: rights.RuleReadOutcomeUnknown, Revision: f.rev()}, nil
}
func (f *reviewPolicyFixture) SetRuleForContext(_ context.Context, expected string, rule rights.PolicyRule, ttl time.Duration, why string) (rights.PolicyEdit, error) {
	f.writes++
	if ttl != time.Hour || !strings.HasPrefix(why, "personal review ") {
		return rights.PolicyEdit{}, errors.New("wrong write binding")
	}
	if f.uncertain {
		return rights.PolicyEdit{}, errors.New("reply lost")
	}
	if expected != f.rev() || f.stale || f.failAt == f.writes {
		return rights.PolicyEdit{Outcome: rights.PolicyEditOutcomeConflict, Revision: f.rev()}, nil
	}
	f.revision++
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.rules[reviewRuleKey(rule.Subject, rule.Action, rule.Resource)] = rights.RuleRecord{Rule: rule, SetBy: f.setter, SetAt: now.Format(time.RFC3339Nano), Expires: now.Add(ttl).Format(time.RFC3339Nano), Why: why}
	return rights.PolicyEdit{Outcome: rights.PolicyEditOutcomeApplied, Revision: f.rev(), Current: &rule}, nil
}

func reviewFixture(t *testing.T) (*servicePanel, *reviewPolicyFixture, *permissionReviewSources, permissionReviewContext) {
	t.Helper()
	setter := rights.Subject{Account: "account-fixture", Program: "/trusted/native-panel"}
	f := &reviewPolicyFixture{rules: map[string]rights.RuleRecord{}, setter: setter}
	program := filepath.Join(t.TempDir(), "python.exe")
	if err := os.WriteFile(program, []byte("inert program fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &permissionReviewSources{
		Nonce: "fixture-native-run", StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Resolve: func(context.Context) (permissionReviewOperator, rights.Subject, error) { return f, setter, nil },
		Program: inspectPermissionProgram,
		App: func(context.Context, string) (string, string, string, error) {
			return program, "Selected image app", "registered:v1", nil
		},
		Hosts: func(context.Context) (iclient.HostList, error) {
			return iclient.HostList{Outcome: iclient.ListOutcomePage, Revision: "hosts:v1", Hosts: []iclient.HostState{{Up: true, Entry: iclient.HostEntry{Name: "selected-image", Profiles: []string{"image"}, DeclaredBy: "/trusted/provider", Kind: "native"}}}}, nil
		},
	}
	return &servicePanel{permissionReview: s}, f, s, permissionReviewContext{Feature: "notifications", Program: program}
}

func reviewRequest(t *testing.T, p *servicePanel, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("X-Panel-Key", "test-key")
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	p.routes("test-key").ServeHTTP(w, r)
	return w
}

func TestPermissionReviewNativeChoiceAndReadback(t *testing.T) {
	p, f, s, c := reviewFixture(t)
	v, _, _, err := buildPermissionReview(context.Background(), *s, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Rules) != 1 || v.Rules[0] != (grants.Rule{Action: grants.NotificationPostAction, Resource: "account"}) || v.TTLMS != 3600000 || !strings.Contains(v.Disclosure, "all scripts") {
		t.Fatalf("wrong ceiling/disclosure: %+v", v)
	}
	if f.writes != 0 {
		t.Fatal("review wrote permissions")
	}
	choice := permissionReviewChoice{Context: v.Context, Shown: v.ID, Allow: true}
	w := reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", choice)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var result permissionReviewResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "applied" || len(result.Landed) != 1 || f.writes != 1 || result.Landed[0].SetBy != f.setter || result.Landed[0].Why != "personal review "+v.ID {
		t.Fatalf("wrong authoritative result: %+v", result)
	}
	if result.Landed[0].Rule.Subject.Program != v.Program.Program {
		t.Fatal("wrong program")
	}
}

func TestPermissionReviewPictureCompiledCeiling(t *testing.T) {
	_, f, s, _ := reviewFixture(t)
	c := permissionReviewContext{Feature: "picture-workflow", App: "registered-app", Host: "selected-image"}
	v, _, _, err := buildPermissionReview(context.Background(), *s, c)
	if err != nil {
		t.Fatal(err)
	}
	want, err := grants.FeatureRules(grants.Picture, grants.For{Hosts: []string{"selected-image"}})
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, grants.Rule{Action: "abstraction.asks/question.ask", Resource: "account"}, grants.Rule{Action: "abstraction.router/inventory.read", Resource: "abstraction.router/inventory"})
	if raw, _ := json.Marshal(v.Rules); string(raw) != func() string { b, _ := json.Marshal(want); return string(b) }() {
		t.Fatalf("wrong rules %s", raw)
	}
	result, err := applyPermissionReview(context.Background(), *s, permissionReviewChoice{Context: v.Context, Shown: v.ID, Allow: true})
	if err != nil || result.Outcome != "applied" || len(result.Landed) != 5 || f.writes != 5 {
		t.Fatalf("%+v %v writes%d", result, err, f.writes)
	}
}

func TestPermissionReviewContextMutationRefused(t *testing.T) {
	for _, name := range []string{"program-bytes", "program-file", "account", "registration", "host-config", "host-revision", "host-down", "host-not-image", "injected-host", "wrong-digest", "existing-permit", "existing-block"} {
		t.Run(name, func(t *testing.T) {
			_, f, s, c := reviewFixture(t)
			if strings.HasPrefix(name, "host-") || name == "registration" || name == "injected-host" {
				c = permissionReviewContext{Feature: "picture-workflow", App: "registered-app", Host: "selected-image"}
			}
			v, _, _, err := buildPermissionReview(context.Background(), *s, c)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "program-bytes":
				if err := os.WriteFile(c.Program, []byte("other program"), 0600); err != nil {
					t.Fatal(err)
				}
			case "program-file":
				if err := os.Rename(c.Program, c.Program+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(c.Program, []byte("inert program fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "account":
				s.Resolve = func(context.Context) (permissionReviewOperator, rights.Subject, error) {
					setter := f.setter
					setter.Account = "another-account"
					return f, setter, nil
				}
			case "registration":
				old := s.App
				s.App = func(ctx context.Context, n string) (string, string, string, error) {
					p, title, _, e := old(ctx, n)
					return p, title, "registered:v2", e
				}
			case "host-config", "host-revision", "host-down", "host-not-image":
				old := s.Hosts
				s.Hosts = func(ctx context.Context) (iclient.HostList, error) {
					list, e := old(ctx)
					switch name {
					case "host-config":
						list.Hosts[0].Entry.DeclaredBy = "/other/provider"
					case "host-revision":
						list.Revision = "hosts:v2"
					case "host-down":
						list.Hosts[0].Up = false
					case "host-not-image":
						list.Hosts[0].Entry.Profiles = []string{"chat"}
					}
					return list, e
				}
			case "injected-host":
				c.Host = "unregistered-image"
			case "wrong-digest":
				v.ID = "fabricated"
			case "existing-permit", "existing-block":
				r := v.Rules[0]
				f.rules[reviewRuleKey(v.Subject, r.Action, r.Resource)] = rights.RuleRecord{Rule: rights.PolicyRule{Subject: v.Subject, Action: r.Action, Resource: r.Resource, Permit: name == "existing-permit"}}
			}
			_, err = applyPermissionReview(context.Background(), *s, permissionReviewChoice{Context: c, Shown: v.ID, Allow: true})
			if err == nil || f.writes != 0 {
				t.Fatalf("mutated context wrote: %v writes%d", err, f.writes)
			}
		})
	}
}

func TestPermissionReviewPartialAndUncertainRemainVisible(t *testing.T) {
	for _, name := range []string{"second-conflict", "lost-reply", "bad-readback", "stale-revision", "final-readback"} {
		t.Run(name, func(t *testing.T) {
			p, f, s, _ := reviewFixture(t)
			v, _, _, err := buildPermissionReview(context.Background(), *s, permissionReviewContext{Feature: "picture-workflow", App: "registered-app", Host: "selected-image"})
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "second-conflict":
				f.failAt = 2
			case "lost-reply":
				f.uncertain = true
			case "bad-readback":
				f.badReadback = true
			case "stale-revision":
				f.stale = true
			case "final-readback":
				f.finalReadback = true
			}
			w := reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", permissionReviewChoice{Context: v.Context, Shown: v.ID, Allow: true})
			var result permissionReviewResult
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatalf("%d %s: %v", w.Code, w.Body, err)
			}
			if result.Outcome == "applied" || (name != "final-readback" && result.Stopped == nil) {
				t.Fatalf("false completion %+v", result)
			}
			if name == "second-conflict" && (len(result.Landed) != 1 || f.writes != 2) {
				t.Fatalf("lost partial %+v writes%d", result, f.writes)
			}
			if (name == "lost-reply" || name == "bad-readback") && (result.Unknown == nil || result.Outcome != "uncertain" || f.writes != 1) {
				t.Fatalf("lost uncertainty %+v", result)
			}
		})
	}
}

func TestPermissionReviewPrivateDecisionReceipt(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			p, f, s, c := reviewFixture(t)
			oldContext, oldPath := startPermissionReview, startPermissionReviewReceipt
			defer func() { startPermissionReview, startPermissionReviewReceipt = oldContext, oldPath }()
			path := filepath.Join(t.TempDir(), "new-private-review", "decision.json")
			if err := preparePermissionReceiptPath(path); err != nil {
				t.Fatal(err)
			}
			if err := privatedir.CheckDir(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			startPermissionReview, startPermissionReviewReceipt = c, path
			v, _, _, err := buildPermissionReview(context.Background(), *s, c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("receipt exists before human choice")
			}
			w := reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", permissionReviewChoice{Context: c, Shown: v.ID, Allow: allow})
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var receipt permissionReviewReceipt
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Profile != "oa-personal-permission-review@1" || receipt.PID != os.Getpid() || receipt.NativeActor != f.setter || receipt.Review.ID != v.ID || receipt.Review.Nonce != s.Nonce || receipt.Review.Context != c || receipt.DecidedAt == "" || receipt.StartedAt != s.StartedAt {
				t.Fatalf("wrong receipt %+v", receipt)
			}
			if !allow && (receipt.Result.Outcome != "refused" || f.writes != 0) {
				t.Fatal("refusal wrote policy")
			}
			if allow && (receipt.Result.Outcome != "applied" || len(receipt.Result.Landed) != 1) {
				t.Fatal("missing landed proof")
			}
			for _, secret := range []string{"bearer", "panel-key", "nonce-token", "password"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("unexpected secret marker %q", secret)
				}
			}
			// Existing output is never replaced, including another explicit refusal.
			w = reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", permissionReviewChoice{Context: c, Shown: v.ID, Allow: false})
			if w.Code != 409 {
				t.Fatalf("existing receipt accepted: %d", w.Code)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, after) {
				t.Fatal("existing receipt changed")
			}
		})
	}
}

func TestPermissionReviewReceiptUnsafePathsAndInterruptedOutput(t *testing.T) {
	dir := t.TempDir()
	if err := preparePermissionReceiptPath("relative.json"); err == nil {
		t.Fatal("relative output accepted")
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	// A pre-existing ordinary folder is refused rather than tightened.
	if err := preparePermissionReceiptPath(filepath.Join(dir, "decision.json")); err == nil {
		t.Fatal("nonprivate parent accepted")
	}
	path := filepath.Join(dir, "private-new", "decision.json")
	if err := preparePermissionReceiptPath(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"profile":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePermissionReceiptPath(path); err == nil {
		t.Fatal("partial existing output accepted")
	}
	var receipt permissionReviewReceipt
	raw, _ := os.ReadFile(path)
	if json.Unmarshal(raw, &receipt) == nil {
		t.Fatal("partial output interpreted as a decision")
	}
}

func TestPermissionReviewReceiptContextAndMissingChoice(t *testing.T) {
	p, f, s, c := reviewFixture(t)
	oldContext, oldPath := startPermissionReview, startPermissionReviewReceipt
	defer func() { startPermissionReview, startPermissionReviewReceipt = oldContext, oldPath }()
	path := filepath.Join(t.TempDir(), "new-private-review", "decision.json")
	if err := preparePermissionReceiptPath(path); err != nil {
		t.Fatal(err)
	}
	startPermissionReview, startPermissionReviewReceipt = c, path
	v, _, _, err := buildPermissionReview(context.Background(), *s, c)
	if err != nil {
		t.Fatal(err)
	}
	// A GET is an observation. Process interruption here leaves no decision.
	w := reviewRequest(t, p, "GET", "/permission-review?k=test-key", nil)
	if w.Code != 200 || f.writes != 0 {
		t.Fatalf("GET %d writes%d", w.Code, f.writes)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("GET manufactured a decision")
	}
	other := c
	other.Program = filepath.Join(t.TempDir(), "other.exe")
	w = reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", permissionReviewChoice{Context: other, Shown: v.ID, Allow: true})
	if w.Code != 409 || f.writes != 0 {
		t.Fatal("mutated native launch context accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("mutation manufactured a decision")
	}
}

func TestPermissionReviewReceiptLinkedParentRefused(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := privatedir.Ensure(private); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Skipf("creating an inert directory link is unavailable: %v", err)
	}
	if err := preparePermissionReceiptPath(filepath.Join(alias, "decision.json")); err == nil {
		t.Fatal("linked receipt parent accepted")
	}
}

func TestPermissionReviewRefuseAndMalformedRequestDoNotWrite(t *testing.T) {
	p, f, s, c := reviewFixture(t)
	v, _, _, err := buildPermissionReview(context.Background(), *s, c)
	if err != nil {
		t.Fatal(err)
	}
	w := reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", permissionReviewChoice{Context: c, Shown: v.ID, Allow: false})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"outcome":"refused"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, body := range []any{map[string]any{"context": c, "shown": v.ID, "allow": true, "ttl_ms": 0}, map[string]any{"context": c, "shown": v.ID, "allow": true, "account": "other"}, map[string]any{"context": permissionReviewContext{Feature: "all", Program: c.Program}, "shown": v.ID, "allow": true}} {
		w = reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", body)
		if w.Code != 400 {
			t.Fatalf("accepted injected field: %d %s", w.Code, w.Body)
		}
	}
	if f.writes != 0 {
		t.Fatal("refused/malformed request wrote")
	}
}

func TestPermissionReviewRefuseCannotResetByReload(t *testing.T) {
	p, f, s, c := reviewFixture(t)
	v, _, _, err := buildPermissionReview(context.Background(), *s, c)
	if err != nil {
		t.Fatal(err)
	}
	choice := permissionReviewChoice{Context: c, Shown: v.ID, Allow: false}
	w := reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", choice)
	if w.Code != 200 {
		t.Fatalf("refuse %d %s", w.Code, w.Body)
	}
	// Reload uses the exact same source-bound view and stays terminal.
	old := startPermissionReview
	startPermissionReview = c
	defer func() { startPermissionReview = old }()
	w = reviewRequest(t, p, "GET", "/permission-review?k=test-key", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"outcome":"refused"`) {
		t.Fatalf("reload reset %d %s", w.Code, w.Body)
	}
	choice.Allow = true
	w = reviewRequest(t, p, "POST", "/permission-review/choice?k=test-key", choice)
	if w.Code != 409 || f.writes != 0 {
		t.Fatalf("second choice granted: %d writes%d", w.Code, f.writes)
	}
}

func TestPermissionReviewConcurrentDuplicateAndSettledReadback(t *testing.T) {
	p, f, s, c := reviewFixture(t)
	v, _, _, err := buildPermissionReview(context.Background(), *s, c)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(permissionReviewChoice{Context: c, Shown: v.ID, Allow: true})
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/permission-review/choice?k=test-key", bytes.NewReader(body))
			w := httptest.NewRecorder()
			p.routes("test-key").ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		}
		if code == 409 {
			conflict++
		}
	}
	if success != 1 || conflict != 1 || f.writes != 1 {
		t.Fatalf("duplicate decision success%d conflict%d writes%d", success, conflict, f.writes)
	}
	old := startPermissionReview
	startPermissionReview = c
	defer func() { startPermissionReview = old }()
	w := reviewRequest(t, p, "GET", "/permission-review?k=test-key", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"outcome":"applied"`) || !strings.Contains(w.Body.String(), `"landed":[`) {
		t.Fatalf("settled proof disappeared: %d %s", w.Code, w.Body)
	}
}

func TestPermissionReviewPageAndNativeEntry(t *testing.T) {
	_, _, s, c := reviewFixture(t)
	v, _, _, err := buildPermissionReview(context.Background(), *s, c)
	if err != nil {
		t.Fatal(err)
	}
	page := permissionReviewFragment(v)
	for _, want := range []string{"Allow for one hour", "Refuse", "all scripts", "Lifetime: One hour", "Technical details"} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
	if _, err := parsePanelStartPage("permission-review"); err != nil {
		t.Fatal(err)
	}
	if _, err := parsePermissionReviewStart("permission-review", "notifications", "", c.Program, ""); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][5]string{{"questions", "notifications", "", c.Program, ""}, {"permission-review", "notifications", "app", c.Program, ""}, {"permission-review", "notifications", "", c.Program, "injected"}, {"permission-review", "picture-workflow", "app", c.Program, "host"}, {"permission-review", "unknown", "", c.Program, ""}} {
		if _, err := parsePermissionReviewStart(args[0], args[1], args[2], args[3], args[4]); err == nil {
			t.Fatalf("accepted %+v", args)
		}
	}
}

func TestPermissionReviewNativeModeRestrictions(t *testing.T) {
	_, _, _, c := reviewFixture(t)
	if err := validatePermissionReviewLaunch(c, permissionReviewLaunchFlags{Native: true}); err != nil {
		t.Fatal(err)
	}
	if err := validatePermissionReviewLaunch(c, permissionReviewLaunchFlags{NativeAsks: true}); err != nil {
		t.Fatal(err)
	}
	for _, flags := range []permissionReviewLaunchFlags{{Native: false}, {Native: true, Background: true}, {Native: true, Tray: true}, {Native: true, CheckWebView2: true}, {Native: true, Operation: "other-operation"}} {
		if err := validatePermissionReviewLaunch(c, flags); err == nil {
			t.Fatalf("conflicting launch accepted %+v", flags)
		}
	}
	if err := validatePermissionReviewLaunch(permissionReviewContext{}, permissionReviewLaunchFlags{Native: true, Receipt: "unbound.json"}); err == nil {
		t.Fatal("receipt without context accepted")
	}
	if _, err := parsePermissionReviewStart("permission-review", "", "", "", ""); err == nil {
		t.Fatal("empty native review context accepted")
	}
}

func TestPermissionReviewResolvesSelectedRegisteredApp(t *testing.T) {
	_, _, _, c := reviewFixture(t)
	d := fwire.ApplicationDescriptor{Name: "selected-app", Title: "Selected app", Program: c.Program}
	apps := &appPageDirectory{entries: []fwire.ApplicationEntry{{Descriptor: d, Scope: fwire.ScopeLocal}}}
	p := &servicePanel{comfyApps: apps}
	s := p.permissionReviewSource()
	program, title, digest, err := s.App(context.Background(), "selected-app")
	if err != nil || program != c.Program || title != "Selected app" || digest != applicationDigest(&d) {
		t.Fatalf("wrong registration binding %q %q %q %v", program, title, digest, err)
	}
	apps.entries[0].SelfRegistered = true
	if _, _, _, err := s.App(context.Background(), "selected-app"); err == nil {
		t.Fatal("unconfirmed registration accepted")
	}
	apps.entries[0].SelfRegistered = false
	apps.entries[0].Scope = fwire.ScopeRemote
	if _, _, _, err := s.App(context.Background(), "selected-app"); err == nil {
		t.Fatal("nonlocal app accepted")
	}
	apps.entries = nil
	if _, _, _, err := s.App(context.Background(), "selected-app"); err == nil {
		t.Fatal("removed app accepted")
	}
}

func TestPermissionReviewUsesExistingPanelRules(t *testing.T) {
	_, _, s, c := reviewFixture(t)
	v, _, _, err := buildPermissionReview(context.Background(), *s, c)
	if err != nil {
		t.Fatal(err)
	}
	for name, fragment := range map[string]string{"review": permissionReviewFragment(v), "settled": settledPermissionReviewFragment(permissionReviewDecision{View: v, Result: permissionReviewResult{Outcome: "refused", Message: "No permissions were changed."}})} {
		for _, violation := range append(lintPanelPage(fragment, loadRawValueShapes(t)), pageClassViolations(fragment)...) {
			t.Errorf("%s: %s %s", name, violation.Rule, violation.Detail)
		}
	}
}
