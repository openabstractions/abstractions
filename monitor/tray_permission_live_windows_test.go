//go:build windows

package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/monitor/win"
	"golang.org/x/sys/windows"
)

type permissionFixtureRules struct {
	fakeRules
	operator *askclient.Operator
}

func (r *permissionFixtureRules) ApplyFirstUseAllowContext(ctx context.Context, expected, id string) (rightsclient.PolicyEdit, error) {
	details, err := askclient.ReadDetailsContext(ctx, r.operator, probe.Self().Account, id)
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	read := details.Question.Question
	if read.Record == nil || read.Record.Option != "allow" || details.Permission == nil {
		return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeInvalid}, nil
	}
	rules := grants.FirstUsePermissionRules(probe.Self().Account, *read.Record, *details.Permission)
	return r.SetRulesForContext(ctx, expected, rules, 0, grants.FirstUseWhy)
}

func TestPermissionFixtureRulesRecordsStructuredAllow(t *testing.T) {
	book, operator := trayTestBook(t)
	id := ask(t, book, "headless-native-preview", os.Args[0], "abstraction.credentials/apply", "credential:windows11-test-secret")
	rules := &permissionFixtureRules{operator: operator}
	answered, err := grants.AnswerQuestion(context.Background(), operator, rules, probe.Self().Account, id, "allow")
	if err != nil || answered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered || answered.Edit == nil ||
		answered.Edit.Outcome != rightsclient.PolicyEditOutcomeApplied || len(rules.edits) != 1 {
		t.Fatalf("preview's synthetic Allow must complete without a nil fake owner: %+v edits=%+v err=%v", answered, rules.edits, err)
	}
	details, err := askclient.ReadDetailsContext(context.Background(), operator, probe.Self().Account, id)
	if err != nil || details.Permission == nil {
		t.Fatalf("exact retained preview target: %+v %v", details, err)
	}
	target := details.Permission.Targets[0]
	edit := rules.edits[0]
	if !edit.Permit || edit.Subject.Account != target.Account || edit.Subject.Program != target.Program || edit.Action != target.Action || edit.Resource != target.Resource {
		t.Fatalf("synthetic fixture changed the typed scope: edit=%+v target=%+v", edit, target)
	}
	if edit.Action != "abstraction.credentials/apply" || edit.Resource != "credential:windows11-test-secret" {
		t.Fatalf("wrong named-secret target: %+v", edit)
	}
	t.Logf("synthetic Book recorded Allow and fake Rules retained exact action=%s resource=%s; no capability or secret use", edit.Action, edit.Resource)
}

// Opt-in PID-scoped fixture. All requests and rights operators belong to this
// test's isolated runtime. The fake Rules object records expected edits without
// changing permissions; no real credential is saved or used by this fixture.
func TestLivePermissionDialogLifetimeAndGuards(t *testing.T) {
	if os.Getenv("OA_LIVE_PERMISSION") != "1" {
		t.Skip("OA_LIVE_PERMISSION=1 enables owned native permission checks")
	}
	if !win.InteractiveDesktop() {
		t.Fatal("native fixture requires unlocked Default desktop")
	}
	v := windows.RtlGetVersion()
	t.Logf("native RtlGetVersion major=%d minor=%d build=%d product=%d; modern permission=%t", v.MajorVersion, v.MinorVersion, v.BuildNumber, v.ProductType, v.MajorVersion == 10 && v.MinorVersion == 0 && v.BuildNumber >= 22000 && v.ProductType == 1)
	book, operator := trayTestBook(t)
	ready, stopped := make(chan *win.Window, 1), make(chan error, 1)
	go func() { stopped <- win.RunHidden("OA permission renderer fixture", func(w *win.Window) { ready <- w }) }()
	var w *win.Window
	select {
	case w = <-ready:
	case err := <-stopped:
		t.Fatalf("hidden host: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("hidden host timeout")
	}
	t.Cleanup(func() {
		w.Do(func() { win.DismissActiveQuestion(); w.Close() })
		select {
		case err := <-stopped:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("owned hidden host did not stop")
		}
	})
	session := &trayWindowsSession{prompting: true}
	rules := &permissionFixtureRules{operator: operator}
	p := &questionPresenter{session: session, surface: &trayWindowsSurface{w: w, session: session},
		operator: defaultPresenterOperator, rules: func() grants.RuleOperator { return rules }, account: probe.Self().Account, watchEvery: 20 * time.Millisecond}
	ui := syscall.NewLazyDLL("user32.dll")
	enumerate, pidFor := ui.NewProc("EnumWindows"), ui.NewProc("GetWindowThreadProcessId")
	textFor, visible := ui.NewProc("GetWindowTextW"), ui.NewProc("IsWindowVisible")
	send, item := ui.NewProc("SendMessageW"), ui.NewProc("GetDlgItem")
	enabled := ui.NewProc("IsWindowEnabled")
	findOwn := func() uintptr {
		var found uintptr
		callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
			var pid uint32
			pidFor.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
			if pid != uint32(os.Getpid()) {
				return 1
			}
			if shown, _, _ := visible.Call(hwnd); shown == 0 {
				return 1
			}
			var title [256]uint16
			textFor.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
			if syscall.UTF16ToString(title[:]) == "OpenAbstractions Panel — Permission request" {
				found = hwnd
				return 0
			}
			return 1
		})
		enumerate.Call(callback, 0)
		return found
	}
	for _, mode := range []string{"close", "refuse", "allow", "stale", "expiry", "session"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "expiry" {
				if err := book.SetPendingLifetime(time.Second); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := book.SetPendingLifetime(5 * time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			id := ask(t, book, "win11-"+mode, os.Args[0], "abstraction.credentials/apply", "credential:windows11-test-secret")
			records, err := listQuestions(context.Background(), operator)
			if err != nil {
				t.Fatal(err)
			}
			var record askclient.RecordMetadata
			for _, r := range records {
				if r.ID == id {
					record = r
				}
			}
			result := make(chan presentResult, 1)
			go func() { result <- p.present(context.Background(), record) }()
			var hwnd uintptr
			deadline := time.Now().Add(5 * time.Second)
			for hwnd == 0 && time.Now().Before(deadline) {
				hwnd = findOwn()
				if hwnd == 0 {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if hwnd == 0 {
				select {
				case r := <-result:
					t.Fatalf("surface failed: %+v", r)
				default:
					t.Fatal("own permission window did not appear")
				}
			}
			modern := v.MajorVersion == 10 && v.MinorVersion == 0 && v.BuildNumber >= 22000 && v.ProductType == 1
			click := func(id uintptr) {
				if modern {
					send.Call(hwnd, 0x0111, id, 0)
				} else {
					send.Call(hwnd, 0x466, id, 0)
				}
			}
			switch mode {
			case "close":
				send.Call(hwnd, 0x0010, 0, 0)
			case "refuse", "allow":
				if modern {
					button, _, _ := item.Call(hwnd, 100)
					if button == 0 {
						t.Fatal("modern standard Button is absent")
					}
					if on, _, _ := enabled.Call(button); on != 0 {
						t.Fatal("answer button already enabled before host arming")
					}
				}
				click(100)
				select {
				case r := <-result:
					t.Fatalf("early answer accepted: %+v", r)
				case <-time.After(60 * time.Millisecond):
				}
				time.Sleep(600 * time.Millisecond)
				choice := uintptr(101)
				if mode == "allow" {
					choice = 100
				}
				click(choice)
			case "stale":
				answer, err := operator.AnswerQuestionContext(context.Background(), id, "refuse")
				if err != nil || answer.Outcome != askclient.OperatorDecisionOutcomeAnswered {
					t.Fatalf("external answer: %+v %v", answer, err)
				}
			case "session":
				session.revision.Add(1)
				p.surface.Dismiss()
			case "expiry": // real public Book expiry; presenter watch dismisses it
			}
			var r presentResult
			select {
			case r = <-result:
			case <-time.After(5 * time.Second):
				send.Call(hwnd, 0x0010, 0, 0)
				t.Fatal("modal did not finish")
			}
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			kept, found, err := book.ApplicationRecord(id)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "close", "session":
				if !found || kept.Option != "" {
					t.Fatalf("dismissal submitted an answer: %+v", kept)
				}
			case "refuse", "allow", "stale":
				want := mode
				if mode == "stale" {
					want = "refuse"
					if !r.ClosedElsewhere {
						t.Fatalf("stale host did not close: %+v", r)
					}
				}
				if !found || kept.Option != want {
					t.Fatalf("exact answer: got %+v want %s", kept, want)
				}
			case "expiry":
				if found || !r.ClosedElsewhere {
					t.Fatalf("expiry was not dismissed: found=%t result=%+v", found, r)
				}
			}
			t.Logf("mode=%s pid=%d hwnd=%d option=%q outcome=%d closedElsewhere=%t rights-edits=%d", mode, os.Getpid(), hwnd, kept.Option, r.Outcome, r.ClosedElsewhere, len(rules.edits))
		})
	}
	if len(rules.edits) != 1 || !rules.edits[0].Permit {
		t.Fatalf("only the isolated Allow should reach fake Rules: %+v", rules.edits)
	}
	if edit := rules.edits[0]; edit.Subject.Account != probe.Self().Account || edit.Action != "abstraction.credentials/apply" || edit.Resource != "credential:windows11-test-secret" {
		t.Fatalf("fake Rules must record the exact typed target: %+v", edit)
	}
}
