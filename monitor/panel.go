package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	download "github.com/openabstractions/abstraction-download/go"
	"github.com/openabstractions/abstraction-download/go/nas"
	job "github.com/openabstractions/abstraction-job/go"
)

// tier is one place this machine could send work, and what a person may do
// about it.
type tier struct {
	System   string `json:"system"`
	Usable   bool   `json:"usable"`
	Off      bool   `json:"off"`
	Why      string `json:"why,omitempty"`
	Serving  bool   `json:"serving"`
	Store    string `json:"store,omitempty"`
	Discover bool   `json:"discover"`
}

// found is one host on the network, offered and never adopted.
type found struct {
	Address string   `json:"address"`
	Name    string   `json:"name"`
	Says    string   `json:"says"`
	Shares  []string `json:"shares"`
	How     string   `json:"how"`
	Files   bool     `json:"files"`
}

// Serves reports that this host answered as a file server. Not the same as "it
// has no shares": a hidden share is served and not listed, which is what the one
// this project has used all along turns out to be.
func (f found) Serves() bool { return strings.Contains(f.How, "smb") }

// trial is the last quick test: whether the setup a person just made actually
// moves bytes, which today can only be found out by losing a real download.
type trial struct {
	Running bool   `json:"running"`
	Source  string `json:"source"`
	Served  string `json:"served,omitempty"`
	Bytes   int64  `json:"bytes"`
	Took    string `json:"took,omitempty"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
	At      string `json:"at,omitempty"`
}

// origin is one key and the authority that answered for it. Per key, because a
// machine whose store comes from the user file and whose log sink comes from
// the machine file has two answers, and one line naming one file sends the
// person editing it to the wrong place.
type origin struct {
	Key  string `json:"key"`
	Rung string `json:"rung"`
	Path string `json:"path,omitempty"`
}

// delegation is the whole first screen: where work goes, what it could go to,
// and why not.
type delegation struct {
	Serving    string   `json:"serving"`
	Supervisor string   `json:"supervisor"`
	Alone      bool     `json:"alone"`
	Tiers      []tier   `json:"tiers"`
	NASStore   string   `json:"nasStore"`
	From       []origin `json:"from"`
	File       string   `json:"file"`
	Overridden []string `json:"overridden"`
	Found      []found  `json:"found"`
	Searching  bool     `json:"searching"`
	Trial      trial    `json:"trial"`
	Told       string   `json:"told"`

	// lapses is when Alone would flip with nothing else happening. A heartbeat
	// going stale is the one answer here that changes by itself, and
	// SupervisorOf keeps the moment to itself, so the rule is repeated: three
	// missed beats.
	lapses time.Time
}

// beat mirrors download.SupervisorOf's fallback for a heartbeat that does not
// say how often it beats.
func beat(every string) time.Duration {
	if d, err := time.ParseDuration(every); err == nil && d > 0 {
		return d
	}
	return 30 * time.Second
}

// panel holds what the window learned by asking, which is everything the machine
// does not already write down: what answered a broadcast a moment ago, and how
// the last test went.
type panel struct {
	mu        sync.Mutex
	found     []found
	searching bool
	trial     trial

	// searched and settled close when the current search or test finishes, so
	// anything waiting on one waits on a notification rather than on a clock.
	searched chan struct{}
	settled  chan struct{}

	// change is how the window is told to redraw between ticks: discovery and a
	// quick test are the only things here that finish without a record moving.
	change chan struct{}

	offers []download.Offer
	asked  time.Time
}

func newPanel() *panel {
	return &panel{
		searched: closed(),
		settled:  closed(),
		change:   make(chan struct{}, 1),
	}
}

func closed() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func (p *panel) moved() {
	select {
	case p.change <- struct{}{}:
	default:
	}
}

// probed is how stale an answer about the tiers may be. Asking means touching a
// share, and whether a share answers is the one thing on this screen nothing
// can be subscribed to. It is asked only when the window is drawing, which it
// now does when something happened rather than once a second, so a machine with
// nothing moving asks nobody.
const probed = 4 * time.Second

func (p *panel) tiers(cfg config.Config) []download.Offer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.asked) < probed && p.offers != nil {
		return p.offers
	}
	p.offers, p.asked = download.Offers(cfg), time.Now()
	return p.offers
}

// stale forgets the cached answer, so a switch a person just threw is visible in
// the next redraw rather than up to `probed` later.
func (p *panel) stale() {
	p.mu.Lock()
	p.asked = time.Time{}
	p.mu.Unlock()
}

// origins lists the keys something actually answered for, in the schema's own
// order. A key still on its default is left out: four rows saying nothing
// decided this would bury the one row that did.
func origins(cfg config.Config) []origin {
	var out []origin
	for _, key := range config.Keys {
		o := cfg.Origin(key)
		if o.Rung == config.Default {
			continue
		}
		out = append(out, origin{Key: key, Rung: o.Rung, Path: o.Path})
	}
	return out
}

func (w *window) delegation(cfg config.Config) delegation {
	d := delegation{
		NASStore: cfg.NASStore,
		From:     origins(cfg),

		Serving: "here",
		Told:    w.cfg.How(),
	}
	for _, key := range config.Keys {
		if cfg.Origin(key).Rung == config.Environment {
			d.Overridden = append(d.Overridden, key)
		}
	}
	for _, o := range d.From {
		if o.Rung == config.User {
			d.File = o.Path
			break
		}
	}
	if status, ok := w.cfg.(interface{ Error() error }); ok && status.Error() != nil {
		d.Supervisor = "Configuration unavailable; last observed values retained"
		return d
	}
	for _, o := range w.panel.tiers(cfg) {
		t := tier{System: o.System, Usable: o.Usable, Off: o.Off, Why: o.Why,
			Discover: o.System == nas.System}
		if o.System == nas.System {
			t.Store = cfg.NASStore
		}
		if o.Usable && d.Serving == "here" {
			t.Serving, d.Serving = true, o.System
		}
		d.Tiers = append(d.Tiers, t)
	}
	// Whether anything is watching this store, because it decides whether any of
	// the switches above do anything at all: a tier is only ever offered work by
	// a supervisor, and with none alive this machine downloads in whichever
	// application asked, whatever is configured here.
	if sup, live := download.SupervisorOf(w.jobs); live {
		d.Supervisor = fmt.Sprintf("%s is watching, last seen %s", sup.Owner, sup.Seen.Format(time.Kitchen))
		d.lapses = sup.Seen.Time.Add(3 * beat(sup.Every))
	} else {
		d.Alone = true
		d.Supervisor = "Nothing is watching this machine's store, so nothing is handed on: " +
			"downloads run inside whichever application asked for them, and stop when it does. " +
			"`jobd start` changes that."
	}
	p := w.panel
	p.mu.Lock()
	d.Found, d.Searching, d.Trial = p.found, p.searching, p.trial
	p.mu.Unlock()
	return d
}

// serve switches a tier on or off, in the file this machine keeps its answers
// in. Negative only: a tier that is not configured or not reachable cannot be
// forced on, and a switch that silently does nothing is worse than no switch.
func (w *window) serve(system, reason string, on bool) error {
	if !known(w.panel.tiers(w.cfg.Current()), system) {
		return fmt.Errorf("%q is not a tier this program has", system)
	}
	err := w.editConfiguration(func(c *config.Config) error {
		if on {
			delete(c.Off, system)
			return nil
		}
		if c.Off == nil {
			c.Off = map[string]string{}
		}
		c.Off[system] = firstLine(reason, "switched off in the control panel")
		return nil
	})
	w.panel.stale()
	return err
}

func known(offers []download.Offer, system string) bool {
	for _, o := range offers {
		if o.System == system {
			return true
		}
	}
	return false
}

// find asks the network and keeps what answered, so that adopting one afterwards
// can be checked against a list this machine drew rather than against a string
// the browser sent.
func (w *window) find() error {
	p := w.panel
	p.mu.Lock()
	if p.searching {
		p.mu.Unlock()
		return nil
	}
	p.searching, p.searched = true, make(chan struct{})
	done := p.searched
	p.mu.Unlock()
	p.moved()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var out []found
		for _, f := range nas.Find(ctx) {
			one := found{Address: f.Address, Name: f.Name, Says: f.Says,
				Shares: f.Shares, How: strings.Join(f.How, " · ")}
			one.Files = one.Serves()
			out = append(out, one)
		}
		// What answered as a file server first, then what listed the most
		// shares. A device that never answered the SMB question may still serve
		// one — the share this project uses is hidden — so nothing is dropped,
		// it is only put further down.
		sort.SliceStable(out, func(i, j int) bool {
			if a, b := out[i].Serves(), out[j].Serves(); a != b {
				return a
			}
			return len(out[i].Shares) > len(out[j].Shares)
		})
		p.mu.Lock()
		p.found, p.searching = out, false
		p.mu.Unlock()
		close(done)
		p.moved()
	}()
	return nil
}

// storeOn builds the path for a share on a host, and refuses one this machine
// did not find.
//
// The address is the sink field of this screen. A path typed into a page is
// untrusted in exactly the way the record's sink was, and a discovered address is
// worse because it came off the network — so the only addresses that may be
// written here are ones a broadcast this machine sent got an answer from, and the
// share and folder are checked segment by segment rather than pasted.
func (w *window) storeOn(address, share, dir string) (string, error) {
	p := w.panel
	p.mu.Lock()
	seen := false
	for _, f := range p.found {
		seen = seen || f.Address == address
	}
	p.mu.Unlock()
	if !seen {
		return "", fmt.Errorf("%q is not one of the hosts this machine found; search again", address)
	}
	return nas.Path(address, share, dir)
}

// check proves a store could live there before anything is written down. A share
// that mounts read-only fails at the first real download and looks exactly like
// a download that started.
func (w *window) check(address, share, dir string) error {
	root, err := w.storeOn(address, share, dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return nas.Check(ctx, root)
}

// adopt points this machine at a store, after proving it. Nothing restarts: the
// supervisor rebuilds its chain when the machine's answer changes.
func (w *window) adopt(address, share, dir string) error {
	root, err := w.storeOn(address, share, dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := nas.Check(ctx, root); err != nil {
		return err
	}
	err = w.editConfiguration(func(c *config.Config) error {
		c.NASStore = root
		// Choosing a NAS is the same act as wanting one. Leaving an earlier
		// switch off would set it up and then not use it, with the reason for
		// not using it now describing a machine that is gone.
		delete(c.Off, nas.System)
		return nil
	})
	w.panel.stale()
	return err
}

// forget takes the NAS store back out. A person who moves house should not have
// to find a JSON file.
func (w *window) forget() error {
	err := w.editConfiguration(func(c *config.Config) error {
		c.NASStore = ""
		return nil
	})
	w.panel.stale()
	return err
}

// TestSource is what the quick test fetches unless a person names something
// else: small, stable, and served over plain https by a host that has no idea
// who we are.
const TestSource = "https://go.dev/VERSION?m=text"

// test fetches something small the way a real download would and reports which
// tier served it, how long it took and where it landed.
//
// It goes through Submit and Deliver like everything else on purpose. A test
// with its own code path proves that code path works and says nothing about the
// one a person's downloads take, and the setup this screen just changed is
// exactly what it has to exercise.
func (w *window) test(source string) error {
	if strings.TrimSpace(source) == "" {
		source = TestSource
	}
	p := w.panel
	p.mu.Lock()
	if p.trial.Running {
		p.mu.Unlock()
		return errors.New("a test is already running")
	}
	// The previous test's file goes now rather than never. Keeping the current
	// one is the point — a person clicked a button that says bytes arrived and
	// the path under it should lead somewhere — but keeping every one of them
	// turns a diagnostic into litter.
	previous := p.trial.Path
	p.trial, p.settled = trial{Running: true, Source: source}, make(chan struct{})
	done := p.settled
	p.mu.Unlock()
	p.moved()

	go func() {
		if previous != "" {
			os.Remove(previous)
		}
		out := w.attempt(source)
		out.At = time.Now().Format(time.Kitchen)
		p.mu.Lock()
		p.trial = out
		p.mu.Unlock()
		close(done)
		p.moved()
	}()
	return nil
}

func (w *window) attempt(source string) trial {
	out := trial{Source: source}
	dir := filepath.Join(os.TempDir(), "abstraction-panel-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		out.Error = err.Error()
		return out
	}
	// A name of its own each time, so the answer is about this attempt: the same
	// destination twice is one piece of work by the identity rule this layer is
	// built on, and the second test would report the first one's result.
	dest := filepath.Join(dir, fmt.Sprintf("test-%d.bin", time.Now().UnixNano()))
	started := time.Now()
	h, err := w.downloads.Get(source, dest)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rec, err := w.downloads.Deliver(ctx, h.ID())
	out.Took = time.Since(started).Round(time.Millisecond).String()
	if err != nil {
		out.Error = err.Error()
	}
	if rec != nil {
		out.Bytes, out.Served, out.Path = rec.Progress.Done, servedBy(rec), dest
	}
	return out
}

// servedBy is the question the window already knew the answer to and never
// asked: which tier actually did this one. A record that was never delegated was
// fetched by whichever process held the lease, which is this machine.
func servedBy(rec *job.Record) string {
	if rec.Delegation != nil && rec.Delegation.System != "" {
		return rec.Delegation.System
	}
	return "here"
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if s == "" {
		return fallback
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
