package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Owner-hit on R35: the upgrade ran no Setup step with ComfyUI closed, and
// no integration record existed until a step was started by hand. The
// presenter's pass writes the expectation while ComfyUI runs, updates the
// integration at the first tick after ComfyUI closes, and stops once the
// record is settled. A tick while the step's update holds the claim does
// nothing.
func TestPresenterUpdatesTheKeptComfyIntegrationOnceComfyCloses(t *testing.T) {
	f := newUpgradeFixture(t)
	stubComfyProcesses(t, 4242)
	tick := make(chan time.Time)
	looked := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var held atomic.Bool
	held.Store(true)
	var claimed, unclaimedPass bool
	claim := func() (func(), bool, error) {
		defer func() {
			select {
			case looked <- struct{}{}:
			case <-ctx.Done():
			}
		}()
		claimed = !held.Load()
		if !claimed {
			return nil, false, nil
		}
		return func() {}, true, nil
	}
	passes := make(chan struct{})
	done := make(chan struct{})
	defer joinComfyWatch(t, cancel, done)
	go func() {
		defer close(done)
		watchComfyIntegration(ctx, *f.journal, claim, tick, func(ctx context.Context, journal setup.Journal) {
			if !claimed {
				unclaimedPass = true
				return
			}
			officialPicture.maintain(f.panel, ctx, journal)
			select {
			case passes <- struct{}{}:
			case <-ctx.Done():
			}
		})
	}()
	// The first looks find the step's update holding the claim.
	awaitComfyWatch(t, ctx, done, looked)
	tickComfyWatch(t, ctx, done, tick)
	awaitComfyWatch(t, ctx, done, looked)
	held.Store(false)
	tickComfyWatch(t, ctx, done, tick)
	awaitComfyWatch(t, ctx, done, looked)
	awaitComfyWatch(t, ctx, done, passes)
	if f.installs != 0 || officialPicture.readRecord().PendingUpdate == nil {
		t.Fatalf("ComfyUI open: installs %d record %+v", f.installs, officialPicture.readRecord())
	}
	stubComfyProcesses(t)
	tickComfyWatch(t, ctx, done, tick)
	awaitComfyWatch(t, ctx, done, looked)
	awaitComfyWatch(t, ctx, done, passes)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the pass did not stop once the record was settled")
	}
	if unclaimedPass {
		t.Fatal("a maintenance pass ran while the step held the claim")
	}
	if r := officialPicture.readRecord(); f.installs != 1 || r.PendingUpdate != nil || f.service.keepAsks != 0 || f.service.questions != 0 {
		t.Fatalf("ComfyUI closed: installs %d record %+v", f.installs, r)
	}
}

// A pass that finds ComfyUI closed and leaves the record unsettled, such as
// a failing extension MSI, waits longer before the next one.
func TestPresenterBacksOffAFailingComfyUpdate(t *testing.T) {
	f := newUpgradeFixture(t)
	old := officialPicture.installPinned
	t.Cleanup(func() { officialPicture.installPinned = old })
	officialPicture.installPinned = func(context.Context, setup.Catalog, string, func() error) (func(context.Context) error, error) {
		f.installs++
		return nil, errors.New("the MSI failed")
	}
	tick := make(chan time.Time)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan struct{})
	defer joinComfyWatch(t, cancel, done)
	go func() {
		defer close(done)
		watchComfyIntegration(ctx, *f.journal, func() (func(), bool, error) { return func() {}, true, nil }, tick,
			func(ctx context.Context, journal setup.Journal) { officialPicture.maintain(f.panel, ctx, journal) })
	}()
	for range 7 {
		tickComfyWatch(t, ctx, done, tick)
	}
	cancel()
	joinComfyWatch(t, cancel, done)
	// Passes at ticks 0, 2, 5: one tick, then two, then four skipped.
	if f.installs != 3 {
		t.Fatalf("installs %d over seven ticks", f.installs)
	}
}

func awaitComfyWatch(t *testing.T, ctx context.Context, done, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-done:
		t.Fatal("ComfyUI watch exited before the expected event")
	case <-ctx.Done():
		t.Fatal("ComfyUI watch did not report the expected event")
	}
}

func tickComfyWatch(t *testing.T, ctx context.Context, done <-chan struct{}, tick chan<- time.Time) {
	t.Helper()
	select {
	case tick <- time.Now():
	case <-done:
		t.Fatal("ComfyUI watch exited before the expected tick")
	case <-ctx.Done():
		t.Fatal("ComfyUI watch did not receive the expected tick")
	}
}

func joinComfyWatch(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("ComfyUI watch did not exit after cancellation")
	}
}

func watchComfyIntegration(ctx context.Context, journal setup.Journal, claim func() (func(), bool, error), tick <-chan time.Time, pass func(context.Context, setup.Journal)) {
	officialPicture.maintenance(journal, claim, pass).watch(ctx, tick)
}

func TestExtensionMaintenanceSkipsHeldClaimsAndBacksOffOnlyWhenIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tick := make(chan time.Time, 4)
	for range cap(tick) {
		tick <- time.Time{}
	}
	claims, passes, releases := 0, 0, 0
	claimed := false
	duty := extensionMaintenance{
		title: "Example App",
		claim: func() (func(), bool, error) {
			claims++
			if claims == 1 {
				return nil, false, nil
			}
			if claimed {
				t.Fatal("overlapping maintenance passes")
			}
			claimed = true
			return func() { claimed = false; releases++ }, true, nil
		},
		settled: func() bool { return passes == 3 },
		idle:    func() bool { return passes > 0 },
		pass: func(context.Context) {
			if !claimed {
				t.Fatal("maintenance ran without a claim")
			}
			passes++
		},
	}
	duty.watch(ctx, tick)
	if claims != 4 || passes != 3 || releases != 3 || claimed || len(tick) != 0 {
		t.Fatalf("claims %d, passes %d, releases %d, held %v, ticks left %d", claims, passes, releases, claimed, len(tick))
	}
}

func TestPictureMaintenanceKeepsItsExistingClaimKey(t *testing.T) {
	if officialPicture.claimName != `Local\OpenAbstractions.ComfyMaintenance` {
		t.Fatalf("maintenance claim changed: %q", officialPicture.claimName)
	}
}

func TestExtensionMaintenanceSinglePassUsesClaimWithoutWatcherProbes(t *testing.T) {
	claimed, released := false, false
	passes := 0
	duty := extensionMaintenance{
		title: "Example App",
		claim: func() (func(), bool, error) {
			claimed = true
			return func() { released = true }, true, nil
		},
		settled: func() bool { t.Fatal("single pass probed settled state"); return false },
		idle:    func() bool { t.Fatal("single pass probed host idle"); return false },
		pass: func(context.Context) {
			if !claimed || released {
				t.Fatal("pass ran outside the claim")
			}
			passes++
		},
	}
	if ran, idle := duty.tryPass(t.Context(), false); !ran || idle || !released || passes != 1 {
		t.Fatalf("single pass ran=%v idle=%v released=%v passes=%d", ran, idle, released, passes)
	}
}

// The Panel binds legacy and current extension operations through the same
// window key. These requests exercise routing without reaching install effects.
func TestSetupExtensionRoutesUsePanelKey(t *testing.T) {
	const key = "extension-window-key"
	handler := new(servicePanel).handler(key)
	for _, route := range []struct {
		path, method string
		status       int
		sentence     string
	}{
		{"/setup/comfy/status", http.MethodPost, http.StatusMethodNotAllowed, "GET required"},
		{"/setup/comfy/register", http.MethodGet, http.StatusMethodNotAllowed, "POST required"},
		{"/setup/comfy/activate", http.MethodPost, http.StatusGone, "Use the application's page in Apps"},
		{"/setup/comfy/remove", http.MethodGet, http.StatusMethodNotAllowed, "POST required"},
		{"/setup/comfy/connect", http.MethodGet, http.StatusMethodNotAllowed, "POST required"},
		{"/setup/comfy/open", http.MethodGet, http.StatusMethodNotAllowed, "POST required"},
		{"/setup/comfy/folder", http.MethodGet, http.StatusMethodNotAllowed, "POST required"},
		{"/setup/comfy/remove-oa-build", http.MethodGet, http.StatusMethodNotAllowed, "POST required"},
	} {
		t.Run(route.path, func(t *testing.T) {
			for _, supplied := range []string{"", "different-window-key", key} {
				req := httptest.NewRequest(route.method, "http://127.0.0.1"+route.path, nil)
				req.Header.Set("X-Panel-Key", supplied)
				reply := httptest.NewRecorder()
				handler.ServeHTTP(reply, req)
				want, sentence := http.StatusForbidden, "this window is opened by the control panel itself"
				if supplied == key {
					want, sentence = route.status, route.sentence
				}
				if reply.Code != want || !strings.Contains(reply.Body.String(), sentence) {
					t.Fatalf("response status=%d body=%q, want status=%d and %q", reply.Code, reply.Body.String(), want, sentence)
				}
			}
		})
	}
}
