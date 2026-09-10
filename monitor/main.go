// Command monitor is the control panel: a window onto what this machine is
// doing with downloads, and the controls for it.
//
// Everything it shows about a job comes from abstraction.Discover, and every
// button it draws beside one is a capability the handle admits to having. What
// it shows about the machine — which tiers exist, which serves, what is on the
// network — comes from the layers that own those answers, and every change it
// makes goes into the one file this machine keeps its answers in.
//
// # What this window is not allowed to be
//
// A loopback socket carries no caller identity: any process here can open one,
// and the kernel vouches for none of them. The router settled that yesterday by
// refusing to route from its HTTP window at all. This one cannot be read-only —
// its whole purpose is to change where a person's downloads go — so the rule is
// applied where it can be: the powers are bounded and the boundary is written
// down rather than assumed.
//
//   - A key minted at startup and handed to the browser in the URL this program
//     opens. It is sent in a header, which a page on another origin cannot set
//     without a preflight this server never answers, so a web page cannot drive
//     this window. It is not proof of who is calling and is not offered as one.
//   - The store may be pointed only at an address a broadcast THIS machine sent
//     got an answer from, with the share and folder checked segment by segment
//     and the whole path proven writable first. A path typed into a page is
//     untrusted input in exactly the way a record's sink is.
//   - A tier can be switched off by anyone who reaches this socket, which
//     reduces where bytes go and never redirects them.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	download "github.com/openabstractions/abstraction-download/go"

	// The panel names the tiers, so it has to have them linked. An application
	// that did this would be choosing who fetches its bytes; this program
	// fetches nothing on anybody's behalf and delegates nothing — it reports
	// what the machine has and writes down what a person decided about it, and
	// a tier that is not linked cannot even be named.
	_ "github.com/openabstractions/abstraction-download/go/all"
	abstraction "github.com/openabstractions/abstraction-facade/go"
	job "github.com/openabstractions/abstraction-job/go"
)

//go:embed page.html
var page []byte

func main() {
	addr := flag.String("addr", "127.0.0.1:8734", "address to listen on; loopback only")
	open := flag.Bool("open", true, "open the window in the default browser")
	native := flag.Bool("native", windowed(), "draw a window on the desktop instead of serving a page")
	flag.Parse()

	a, err := abstraction.Discover()
	if err != nil {
		fail(*native, err)
	}
	m := &window{a: a, downloads: a.Download(), jobs: a.Jobs(),
		reach: download.DefaultRefusals(), key: mint(), panel: newPanel(), cfg: config.Watch()}
	defer m.cfg.Close()

	if *native {
		if err := m.desktop(); err != nil {
			fail(true, err)
		}
		return
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil || !loopback(host) {
		log.Fatalf("refusing %q: this shows what is on your disk and binds loopback only", *addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", m.serveHTML)
	mux.HandleFunc("/state", m.guard(m.serveState))
	mux.HandleFunc("/events", m.guard(m.serveEvents))
	mux.HandleFunc("/act", m.guard(m.act))

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + ln.Addr().String() + "/?k=" + m.key
	fmt.Println("control panel:", url)
	fmt.Println("the key is this run's only; nothing is written to disk")
	if *open {
		launch(url)
	}
	log.Fatal(http.Serve(ln, mux))
}

// mint is this run's key. Not persisted: a key on disk is one more file to be
// read by exactly the local processes it could never have excluded anyway, and
// reopening the window is one command.
func mint() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// guard requires this run's key on everything but the page itself.
//
// Sent as a header rather than a query parameter: a form or an image on another
// origin can reach a loopback port and cannot set one, because doing so makes
// the browser ask this server for permission first and this server answers
// nothing. That closes the drive-by, which is the attack a window like this
// actually gets. It does not identify the caller, and nothing here pretends it
// does.
func (w *window) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Panel-Key")
		if got == "" {
			got = r.URL.Query().Get("k")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(w.key)) != 1 {
			http.Error(rw, "this window is opened by the control panel itself, with the key it printed", http.StatusForbidden)
			return
		}
		next(rw, r)
	}
}

type window struct {
	a         abstraction.Machine
	downloads download.Client
	jobs      job.Store
	// reach is the one switch DREAM.md § Rights draws per host: off, and the
	// next connection any runner on this machine would open to it is refused
	// with the reason typed here. The same file every runner reads.
	reach download.Refusals

	// key is this run's, and the only thing between this socket and whatever
	// page the browser happens to be showing.
	key string

	// panel is what the window learned by asking rather than by reading: what
	// answered a broadcast, and how the last quick test went.
	panel *panel

	// cfg is this machine's answers, subscribed to rather than re-read. A person
	// editing config.json, another window, or jobd writing a tier off all reach
	// this window without anybody asking again.
	cfg *config.Subscription
}

func (w *window) serveHTML(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A bookmarked address has last run's key in it, and drawing the panel
	// anyway would leave a person looking at an empty window whose only
	// explanation is a 403 in a console they will never open.
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("k")), []byte(w.key)) != 1 {
		rw.WriteHeader(http.StatusForbidden)
		fmt.Fprint(rw, stale)
		return
	}
	rw.Write(page)
}

// stale is what a link from a previous run gets. Deliberately plain: it is the
// one page here that is shown to somebody who has not been let in.
const stale = `<!doctype html><meta charset="utf-8"><title>Control panel</title>
<style>body{font:15px/1.5 system-ui,sans-serif;margin:12vh auto;max-width:32rem;padding:0 1.5rem}
code{background:#8881;padding:.15rem .35rem;border-radius:4px}</style>
<h1>Not this link</h1>
<p>Each run of the control panel mints its own key and prints the address that
carries it. This one is from a run that has ended, or from something else on
this machine that guessed the port.</p>
<p>Start it again — <code>monitor</code> — and use the address it prints.</p>`

func (w *window) serveState(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(w.snapshot(w.cfg.Current()))
}

// serveEvents pushes a snapshot when something happened: a record moved, this
// machine's answers changed, or the panel learned something by asking. Nothing
// is on a tick. The only timer left is set from the drawing itself, for the
// elapsed times and the leases that change with no writer at all, and with
// nothing moving it is not set.
func (w *window) serveEvents(rw http.ResponseWriter, r *http.Request) {
	flush, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-store")

	sub := w.downloads.Jobs()
	defer sub.Close()
	// This stream's own subscription, so that one reader taking a change does
	// not take it from another. The same reason w.downloads.Jobs() is called
	// here and not held on the window.
	cfg := config.Watch()
	defer cfg.Close()
	clock := time.NewTimer(time.Hour)
	defer clock.Stop()

	for {
		s := w.snapshot(cfg.Current())
		b, err := json.Marshal(s)
		if err != nil {
			return
		}
		fmt.Fprintf(rw, "data: %s\n\n", b)
		flush.Flush()
		clock.Stop()
		if d := redrawIn(s); d > 0 {
			clock.Reset(d)
		}
		select {
		case <-r.Context().Done():
			return
		case <-sub.Changes():
		case <-cfg.Changes():
			w.panel.stale()
		case <-w.panel.change:
		case <-clock.C:
		}
	}
}

func (w *window) act(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "post only", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	var err error
	switch r.FormValue("do") {
	case "pause":
		err = w.pause(r.FormValue("id"))
	case "resume":
		err = w.resume(r.FormValue("id"))
	case "collect":
		err = w.downloads.TakeDelivery(r.FormValue("id"))
	case "cancel":
		err = w.downloads.Open(r.FormValue("id")).Cancel()
	case "add":
		_, _, err = w.downloads.ResumeOrGet(r.FormValue("source"), r.FormValue("destination"))
	case "refuse":
		err = w.reach.Refuse(r.FormValue("host"), r.FormValue("reason"))
	case "allow":
		err = w.reach.Allow(r.FormValue("host"))
	case "serve":
		err = w.serve(r.FormValue("system"), r.FormValue("reason"), r.FormValue("on") == "1")
	case "find":
		err = w.find()
	case "check":
		err = w.check(r.FormValue("address"), r.FormValue("share"), r.FormValue("folder"))
	case "adopt":
		err = w.adopt(r.FormValue("address"), r.FormValue("share"), r.FormValue("folder"))
	case "forget":
		err = w.forget()
	case "test":
		err = w.test(r.FormValue("source"))
	default:
		err = fmt.Errorf("unknown action")
	}
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

func (w *window) pause(id string) error {
	p, ok := w.downloads.Open(id).(job.Pausable)
	if !ok {
		return fmt.Errorf("this download cannot be paused")
	}
	return p.Pause()
}

// resume is two calls and both are needed. Clearing the intent makes the job
// eligible again; resubmitting is what offers somebody to do it, because a
// record nobody is watching stays still no matter what it says it wants.
func (w *window) resume(id string) error {
	p, ok := w.downloads.Open(id).(job.Pausable)
	if !ok {
		return fmt.Errorf("this download cannot be resumed")
	}
	if err := p.Resume(); err != nil {
		return err
	}
	rec, err := w.jobs.Load(id)
	if err != nil {
		return err
	}
	spec, err := download.SpecOf(rec)
	if err != nil {
		return err
	}
	_, _, err = w.downloads.ResumeOrSubmit(spec)
	return err
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func launch(url string) {
	if runtime.GOOS != "windows" {
		return
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func defaultDestination() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads")
}
