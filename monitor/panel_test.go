package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	download "github.com/openabstractions/abstraction-download/go"
	"github.com/openabstractions/abstraction-download/go/nas"
	job "github.com/openabstractions/abstraction-job/go"
)

// own points config at a directory of this test's own, so a test never rewrites
// the answers the machine it is running on lives by.
func own(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ProgramData", filepath.Join(dir, "machine"))
	for _, v := range config.EnvVars {
		t.Setenv(v, "")
	}
	return dir
}

func panelOver(t *testing.T, store job.Store) (*window, *download.Runner) {
	t.Helper()
	r := download.DiscoverIn(store)
	cfg := config.Watch()
	t.Cleanup(func() { cfg.Close() })
	return &window{downloads: download.NewClient(r), jobs: store, key: "k", panel: newPanel(), cfg: cfg}, r
}

func storeIn(t *testing.T, dir string) job.Store {
	t.Helper()
	s, err := job.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A switch thrown in the window is obeyed by a runner that was already running,
// which is the whole difference between a setting and a control.
func TestSwitchingATierOffTakesEffectWithoutARestart(t *testing.T) {
	own(t)
	shared := t.TempDir()
	if err := config.Edit(config.UserPath(), func(c *config.Config) error {
		c.NASStore = shared
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w, r := panelOver(t, storeIn(t, t.TempDir()))
	if got := r.Rebind(); got != nas.System {
		t.Fatalf("a configured, reachable store was not being served by: %s", got)
	}
	if err := w.serve(nas.System, "the house is asleep", false); err != nil {
		t.Fatal(err)
	}
	if got := r.Rebind(); got == nas.System {
		t.Fatal("the runner kept delegating to a tier that was switched off")
	}
	if err := w.serve(nas.System, "", true); err != nil {
		t.Fatal(err)
	}
	if got := r.Rebind(); got != nas.System {
		t.Fatalf("switching it back on did not take: %s", got)
	}
}

// The reason survives, because it is what an application shows a person who
// wonders why their download is being fetched the slow way.
func TestTheReasonIsKeptWithTheSwitch(t *testing.T) {
	own(t)
	w, _ := panelOver(t, storeIn(t, t.TempDir()))
	if err := w.serve(nas.System, "on holiday\nand a second line", false); err != nil {
		t.Fatal(err)
	}
	if got := config.Load().Off[nas.System]; got != "on holiday" {
		t.Fatalf("reason kept as %q", got)
	}
	for _, o := range download.Offers(config.Load()) {
		if o.System == nas.System && (!o.Off || o.Why != "on holiday") {
			t.Fatalf("the tier does not report itself off with its reason: %+v", o)
		}
	}
}

func TestATierThisProgramDoesNotHaveCannotBeSwitched(t *testing.T) {
	own(t)
	w, _ := panelOver(t, storeIn(t, t.TempDir()))
	if err := w.serve("../../etc", "", false); err == nil {
		t.Fatal("a name that is not a tier was written into the machine's answers")
	}
}

// A path typed into a page is untrusted in exactly the way a record's sink is,
// and an address off the network is worse.
func TestTheStoreCanOnlyBePointedAtSomethingThisMachineFound(t *testing.T) {
	own(t)
	w, _ := panelOver(t, storeIn(t, t.TempDir()))
	if _, err := w.storeOn("10.0.0.9", "share", "abstraction"); err == nil {
		t.Fatal("an address nobody found was accepted")
	}
	w.panel.found = []found{{Address: "10.0.0.9"}}
	for _, bad := range []string{"..", "../..", `a\b`, "c:", "*"} {
		if _, err := w.storeOn("10.0.0.9", bad, "abstraction"); err == nil {
			t.Fatalf("%q was accepted as a share name", bad)
		}
		if _, err := w.storeOn("10.0.0.9", "share", bad); err == nil {
			t.Fatalf("%q was accepted as a folder", bad)
		}
	}
	got, err := w.storeOn("10.0.0.9", "docker", "abstraction/store")
	if err != nil || got != "//10.0.0.9/docker/abstraction/store" {
		t.Fatalf("built %q, %v", got, err)
	}
}

func TestTheWindowRefusesWithoutItsOwnKey(t *testing.T) {
	own(t)
	w, _ := panelOver(t, storeIn(t, t.TempDir()))
	srv := httptest.NewServer(w.guard(w.act))
	defer srv.Close()

	res, err := http.Post(srv.URL, "application/x-www-form-urlencoded",
		strings.NewReader("do=serve&system=nas&on=0"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a request with no key got %s", res.Status)
	}
	if len(config.Load().Off) != 0 {
		t.Fatal("it changed the machine anyway")
	}
}

// The environment still wins, and the window has to say so rather than write a
// file that nothing reads.
func TestAnEnvironmentOverrideIsReported(t *testing.T) {
	own(t)
	t.Setenv(config.EnvVars["nas_store"], t.TempDir())
	w, _ := panelOver(t, storeIn(t, t.TempDir()))
	if got := w.delegation(w.cfg.Current()).Overridden; len(got) != 1 || got[0] != "nas_store" {
		t.Fatalf("overridden reported as %v", got)
	}
}

// The quick test goes down the same path a real download does, and says which
// tier answered.
func TestTheQuickTestReportsWhoServedIt(t *testing.T) {
	own(t)
	body := []byte("panel")
	src := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write(body)
	}))
	defer src.Close()

	w, _ := panelOver(t, storeIn(t, t.TempDir()))
	if err := w.test(src.URL + "/small.bin"); err != nil {
		t.Fatal(err)
	}
	got := w.awaitTrial(t)
	if got.Error != "" || got.Served != "here" || got.Bytes != int64(len(body)) {
		t.Fatalf("%+v", got)
	}
	if b, err := os.ReadFile(got.Path); err != nil || string(b) != string(body) {
		t.Fatalf("landed as %q, %v", b, err)
	}
	os.Remove(got.Path)
}

// awaitTrial waits on the panel's own notification. There is no clock here and
// no loop: the test that started the work is told when it is over.
func (w *window) awaitTrial(t *testing.T) trial {
	t.Helper()
	w.panel.mu.Lock()
	done := w.panel.settled
	w.panel.mu.Unlock()
	<-done
	w.panel.mu.Lock()
	defer w.panel.mu.Unlock()
	return w.panel.trial
}

// TestLiveNASRoundTrip is the transcript: find a NAS rather than type it, point
// this machine at it from the window, watch it serve a real download, switch it
// off, and watch the next one served here. No environment variable, no restart.
//
// Skipped unless ABSTRACTION_LIVE_NAS names the share to use, because a gate that
// depends on a house being switched on is not a gate.
func TestLiveNASRoundTrip(t *testing.T) {
	share := os.Getenv("ABSTRACTION_LIVE_NAS")
	if share == "" {
		t.Skip("set ABSTRACTION_LIVE_NAS=<share>/<folder> to run this against the real network")
	}
	name, folder, _ := strings.Cut(share, "/")
	own(t)
	store := storeIn(t, t.TempDir())
	w, r := panelOver(t, store)

	if err := w.find(); err != nil {
		t.Fatal(err)
	}
	w.panel.mu.Lock()
	searching := w.panel.searched
	w.panel.mu.Unlock()
	<-searching
	hosts := w.panel.found
	var host string
	for _, f := range hosts {
		t.Logf("found %-16s %-20q says %q via %s shares %v", f.Address, f.Name, f.Says, f.How, f.Shares)
		// The share it is going to be pointed at may not be in the list: a share
		// marked hidden is served and not browsable, which is what the one this
		// project has used all along turns out to be. So the host is chosen by
		// what it answered, and the share is named.
		if strings.Contains(f.How, "smb") && host == "" {
			host = f.Address
		}
	}
	if host == "" {
		t.Fatal("nothing on this network answered as a file server")
	}

	if err := w.adopt(host, name, folder); err != nil {
		t.Fatal(err)
	}
	t.Logf("adopted %s", config.Load().NASStore)
	if got := r.Rebind(); got != nas.System {
		t.Fatalf("after adopting, work would still go to %s", got)
	}

	on := live(t, r, w, "on")
	t.Logf("ON:  served by %-5s %d bytes in %s at %s", on.Served, on.Bytes, on.Took, on.Path)
	if on.Served != nas.System || on.Error != "" || on.Bytes == 0 {
		t.Fatalf("with the NAS on: %+v", on)
	}

	if err := w.serve(nas.System, "proving the switch", false); err != nil {
		t.Fatal(err)
	}
	if got := r.Rebind(); got == nas.System {
		t.Fatal("switching it off did not take")
	}
	off := live(t, r, w, "off")
	t.Logf("OFF: served by %-5s %d bytes in %s at %s", off.Served, off.Bytes, off.Took, off.Path)
	// Not "here": switching one tier off hands the work to the next one down,
	// which on this machine is the OS transfer service. That is the chain doing
	// what it is for, and asserting "here" would have been asserting that this
	// machine has nothing else.
	if off.Served == nas.System || off.Error != "" || off.Bytes == 0 {
		t.Fatalf("with the NAS off: %+v", off)
	}
}

// live runs the quick test while sweeping the way a supervisor would.
//
// The heartbeat is not decoration. Without one the client correctly runs the
// work in this process — an application with nobody watching its store does its
// own downloading, which is the whole design — and no tier is ever offered
// anything. So a delegation this window arranges reaches a NAS only while a
// supervisor is watching, and this test says so by being one: announce, then
// delegate, reconcile and adopt, which is every call jobd makes.
func live(t *testing.T, r *download.Runner, w *window, tag string) trial {
	t.Helper()
	if err := download.Heartbeat(w.jobs, download.Owner(), r.Tier(), "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	defer download.StopHeartbeat(w.jobs)
	if err := w.test(""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	w.panel.mu.Lock()
	settled := w.panel.settled
	w.panel.mu.Unlock()
	sub := job.Watch(w.jobs, download.Kind)
	defer sub.Close()
	// And a tick, because a delegated job moves on another machine's store and
	// nothing on this one changes while it does: a watch here reports silence
	// for the whole transfer. jobd carries the same ticker for the same reason.
	tick := time.NewTicker(2 * time.Second)
	said := ""
	defer tick.Stop()
	for {
		download.Heartbeat(w.jobs, download.Owner(), r.Tier(), "", 30*time.Second)
		r.ReconcileAll(ctx)
		r.DelegateAll(ctx)
		r.Adopt(ctx)
		if recs, err := w.jobs.List(); err == nil && len(recs) > 0 {
			last := recs[len(recs)-1]
			// Only when it moves. The interesting number here is how long the
			// record sits unchanged: a NAS that finished in eleven seconds
			// reported `running` to this machine for a hundred, because the SMB
			// client keeps serving the copy of the record it already has.
			if line := fmt.Sprintf("%s: %s %s done=%d %s", tag, last.State, servedBy(last), last.Progress.Done, last.Error); line != said {
				said = line
				t.Logf("%s  %s", time.Now().Format(time.TimeOnly), line)
			}
		}
		select {
		case <-settled:
			w.panel.mu.Lock()
			got := w.panel.trial
			w.panel.mu.Unlock()
			os.Remove(got.Path)
			return got
		case <-ctx.Done():
			t.Fatalf("%s: the quick test never settled", tag)
		case <-sub.Changes():
		case <-tick.C:
		}
	}
}

// A bookmarked address carries last run's key, and a person following one must
// be told that rather than shown an empty window.
func TestAStaleLinkIsToldSo(t *testing.T) {
	own(t)
	w, _ := panelOver(t, storeIn(t, t.TempDir()))
	srv := httptest.NewServer(http.HandlerFunc(w.serveHTML))
	defer srv.Close()

	for _, at := range []string{"/", "/?k=yesterday"} {
		res, err := http.Get(srv.URL + at)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "Not this link") {
			t.Fatalf("%s got %s: %.80s", at, res.Status, body)
		}
	}
	res, err := http.Get(srv.URL + "/?k=" + w.key)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "Control panel") {
		t.Fatalf("the right key got %s", res.Status)
	}
}
