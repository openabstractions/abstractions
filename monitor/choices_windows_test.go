//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstractions/monitor/win"
)

func TestSessionPresenterKeepsOrdinaryPendingAndDeliversPermission(t *testing.T) {
	book, operator := trayTestBook(t)
	app, err := panelMachine().ResolveAsksChoices(context.Background(), facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	ordinary := admitPublicChoice(t, app, operator, "app-owned")
	surface := &fakeSurface{err: errPromptNotShown, dismissed: make(chan struct{})}
	presenter := &questionPresenter{session: unlockedSession(), surface: surface,
		operator: func(context.Context) (presenterOperator, error) { return operator, nil }, account: probe.Self().Account}

	// Run through a complete production poll interval with only the app's
	// request pending. A native surface must receive no app-owned choice.
	ctx, cancel := context.WithTimeout(context.Background(), trayPollInterval+time.Second)
	surface.during = cancel
	pollTrayLoop(ctx, nil, presenter, func() { t.Error("ordinary request opened the Panel") })
	cancel()
	if len(surface.shown) != 0 {
		t.Fatalf("session presenter displayed an app-owned choice: %+v", surface.shown)
	}

	permission := ask(t, book, "permission-with-app-choice", "app.exe", "abstraction.model/lookup", "hf")
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	surface.during = cancel
	pollTrayLoop(ctx, nil, presenter, func() { t.Error("permission delivery opened the Panel") })
	cancel()
	if len(surface.shown) != 1 || !strings.Contains(surface.shown[0].Details, "Question: "+permission) || surface.shown[0].AppChoice {
		t.Fatalf("permission was not delivered through the native surface: %+v", surface.shown)
	}
	observed, err := app.ObserveContext(context.Background(), ordinary.Request.RequestRef, 0)
	if err != nil || observed.Outcome != askclient.ChoiceOutcomePending || observed.ID != ordinary.ID || observed.Answer != nil {
		t.Fatalf("session presentation changed the app request: %+v %v", observed, err)
	}
}

// This opt-in development fixture drives only dialogs belonging to this test
// PID. It exercises the production hidden-window surface and public runtime;
// it never waits for a person or changes an installed runtime. The retained
// private ordinary renderer is invoked explicitly by this fixture; production
// session presenters deliver permission questions.
func TestLiveChoiceTaskDialogPublicAnswerCloseAndStale(t *testing.T) {
	if os.Getenv("OA_LIVE_CHOICES") != "1" {
		t.Skip("OA_LIVE_CHOICES=1 enables automatic native dialog fixture")
	}
	if !win.InteractiveDesktop() {
		t.Fatal("native fixture requires an unlocked interactive desktop")
	}
	app, operator := panelChoiceRuntime(t)
	ready := make(chan *win.Window, 1)
	loopDone := make(chan error, 1)
	go func() { loopDone <- win.RunHidden("OA ordinary-choice fixture", func(w *win.Window) { ready <- w }) }()
	var w *win.Window
	select {
	case w = <-ready:
	case err := <-loopDone:
		t.Fatalf("hidden window: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("hidden window did not initialize")
	}
	t.Cleanup(func() {
		w.Do(w.Close)
		select {
		case err := <-loopDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("owned hidden window did not stop")
		}
	})
	session := &trayWindowsSession{prompting: true}
	p := &questionPresenter{session: session, surface: &trayWindowsSurface{w: w, session: session}, choices: func(context.Context) (presenterChoiceOperator, error) { return operator, nil }, watchEvery: 20 * time.Millisecond}
	ui := syscall.NewLazyDLL("user32.dll")
	enumerate := ui.NewProc("EnumWindows")
	pidFor := ui.NewProc("GetWindowThreadProcessId")
	textFor := ui.NewProc("GetWindowTextW")
	visible := ui.NewProc("IsWindowVisible")
	classFor := ui.NewProc("GetClassNameW")
	send := ui.NewProc("SendMessageW")
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
			if syscall.UTF16ToString(title[:]) == "OpenAbstractions Panel — App choice" {
				found = hwnd
				return 0
			}
			return 1
		})
		enumerate.Call(callback, 0)
		return found
	}
	for _, mode := range []string{"cancel", "close", "stale"} {
		t.Run(mode, func(t *testing.T) {
			record := admitPublicChoice(t, app, operator, "native-"+mode)
			result := make(chan choicePresentResult, 1)
			go func() { result <- p.presentChoice(context.Background(), record) }()
			var hwnd uintptr
			deadline := time.Now().Add(5 * time.Second)
			for hwnd == 0 && time.Now().Before(deadline) {
				hwnd = findOwn()
				time.Sleep(10 * time.Millisecond)
			}
			if hwnd == 0 {
				select {
				case r := <-result:
					t.Fatalf("native surface failed: %+v", r)
				default:
					t.Fatal("own TaskDialog not shown")
				}
			}
			// TaskDialog's command links are DirectUI controls without child HWNDs.
			// The owning config test checks their command-link and default flags;
			// this fixture drives the real modal through its documented messages.
			var class [128]uint16
			classFor.Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
			if syscall.UTF16ToString(class[:]) != "#32770" {
				send.Call(hwnd, 0x466, 2, 0)
				t.Fatalf("expected native dialog class, got %q", syscall.UTF16ToString(class[:]))
			}
			time.Sleep(120 * time.Millisecond)
			switch mode {
			case "cancel":
				time.Sleep(650 * time.Millisecond)
				send.Call(hwnd, 0x466, 101, 0) // opaque cancel is the second offered command link
			case "close":
				send.Call(hwnd, 0x466, 8, 0)
			case "stale":
				decision, err := operator.AnswerChoiceContext(context.Background(), record.ID, "cancel")
				if err != nil || decision.Outcome != askclient.ChoiceDecisionOutcomeAnswered {
					t.Fatalf("external answer %+v %v", decision, err)
				}
			}
			var r choicePresentResult
			select {
			case r = <-result:
			case <-time.After(5 * time.Second):
				send.Call(hwnd, 0x466, 8, 0)
				t.Fatal("native prompt did not finish")
			}
			observed, err := app.ObserveContext(context.Background(), record.Request.RequestRef, 0)
			want := askclient.ChoiceOutcomeAnswered
			option := "cancel"
			if mode == "close" {
				want = askclient.ChoiceOutcomeDismissed
				option = ""
			}
			if err != nil || observed.Outcome != want || observed.Answer == nil || observed.Answer.Option != option {
				t.Fatal(fmt.Sprintf("public observation %+v %v", observed, err))
			}
			if mode == "stale" {
				if !r.ClosedElsewhere {
					t.Fatalf("stale modal submitted: %+v", r)
				}
			} else if r.Err != nil {
				t.Fatal(r.Err)
			}
		})
	}
}
