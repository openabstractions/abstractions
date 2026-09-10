package main

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	download "github.com/openabstractions/abstraction-download/go"
	job "github.com/openabstractions/abstraction-job/go"
)

type state struct {
	Where       string     `json:"where"`
	Bindings    []string   `json:"bindings"`
	Destination string     `json:"destination"`
	Jobs        []view     `json:"jobs"`
	Reach       []reach    `json:"reach"`
	Empty       bool       `json:"empty"`
	Nothing     string     `json:"nothing"`
	Delegation  delegation `json:"delegation"`
}

// reach is one host this machine's downloads have named, who opened a
// connection to it, and whether the switch is off. The application is the
// lease owner that did the reaching — the process that held the job while the
// socket was open — never the one that wrote the record, which is a claim.
type reach struct {
	Host    string   `json:"host"`
	Apps    []string `json:"apps"`
	Refused bool     `json:"refused"`
	Reason  string   `json:"reason"`
}

type view struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Source     string  `json:"source"`
	State      string  `json:"state"`
	Tone       string  `json:"tone"`
	Holder     string  `json:"holder"`
	Served     string  `json:"served"`
	Step       string  `json:"step"`
	Bar        string  `json:"bar"`
	Percent    float64 `json:"percent"`
	Unknown    bool    `json:"unknown"`
	Moved      string  `json:"moved"`
	Error      string  `json:"error"`
	Track      bool    `json:"track"`
	Finished   bool    `json:"finished"`
	CanPause   bool    `json:"canPause"`
	CanResume  bool    `json:"canResume"`
	CanCollect bool    `json:"canCollect"`
	CanCancel  bool    `json:"canCancel"`

	created time.Time
	moved   time.Time
	lapses  time.Time
}

// redrawIn is when this drawing would next differ by the clock alone: an
// elapsed time spelled a second later, a lease running out, a heartbeat going
// stale. Zero means never, and a window with nothing moving on it then waits
// only on the layers that can tell it something.
//
// Nothing is read to work this out. It is the one thing on the screen that
// changes without anybody writing anything down, and it is the only reason left
// to wake up.
func redrawIn(s state) time.Duration {
	now := time.Now()
	next := s.Delegation.redrawIn()
	at := func(d time.Duration) {
		if d > 0 && (next == 0 || d < next) {
			next = d
		}
	}
	for _, v := range s.Jobs {
		at(untilSpelledLater(now.Sub(v.moved)))
		if !v.lapses.IsZero() {
			at(untilSpelledSooner(v.lapses.Sub(now)))
		}
	}
	return next
}

func (g delegation) redrawIn() time.Duration {
	if g.lapses.IsZero() {
		return 0
	}
	return max(time.Until(g.lapses), 0)
}

func untilSpelledLater(age time.Duration) time.Duration {
	u := spellingUnit(age)
	return u - age%u
}

func untilSpelledSooner(left time.Duration) time.Duration {
	u := spellingUnit(left)
	if r := left % u; r > 0 {
		return r
	}
	return u
}

func (w *window) snapshot(cfg config.Config) state {
	s := state{
		Where:       w.downloads.Where(),
		Bindings:    w.a.Bindings(),
		Destination: defaultDestination(),
		Delegation:  w.delegation(cfg),
	}
	recs := w.downloads.Jobs().Records()
	s.Reach = w.reached(recs)
	moving, failed := 0, 0
	for _, rec := range recs {
		v := w.render(rec)
		switch {
		case v.CanCancel:
			moving++
		case v.Error != "":
			failed++
		}
		s.Jobs = append(s.Jobs, v)
	}
	// Work in hand before work behind us, newest first within each half. Sorted
	// rather than taken from the store's order, which is by id, and an id
	// derived from a destination does not sort by when it was made.
	sort.SliceStable(s.Jobs, func(i, j int) bool {
		if s.Jobs[i].Finished != s.Jobs[j].Finished {
			return !s.Jobs[i].Finished
		}
		return s.Jobs[i].created.After(s.Jobs[j].created)
	})
	switch {
	case len(recs) == 0:
		s.Empty = true
		s.Nothing = "Nothing has been downloaded on this machine yet."
	case moving > 0:
	case failed > 0:
		s.Nothing = fmt.Sprintf("Nothing is moving. %s did not finish.", count(failed, "download"))
	default:
		s.Nothing = fmt.Sprintf("Nothing is moving. %s here, all finished with.", count(len(recs), "download"))
	}
	return s
}

func (w *window) reached(recs []*job.Record) []reach {
	refused, _ := w.reach.List()
	apps := map[string]map[string]bool{}
	for _, rec := range recs {
		spec, err := download.SpecOf(rec)
		if err != nil {
			continue
		}
		for _, src := range spec.Sources {
			host := download.HostOf(src.Locator)
			if host == "" {
				continue
			}
			if apps[host] == nil {
				apps[host] = map[string]bool{}
			}
			if who := program(rec.Lease.Owner); who != "" {
				apps[host][who] = true
			}
		}
	}
	for host := range refused {
		if apps[host] == nil {
			apps[host] = map[string]bool{}
		}
	}
	var out []reach
	for host, who := range apps {
		r := reach{Host: host}
		for app := range who {
			r.Apps = append(r.Apps, app)
		}
		sort.Strings(r.Apps)
		for name, why := range refused {
			if host == name || strings.HasSuffix(host, "."+name) {
				r.Refused, r.Reason = true, why
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// program is the application half of an owner, which the download layer spells
// as program@host:pid.
func program(owner string) string {
	name, _, _ := strings.Cut(owner, "@")
	return name
}

func (w *window) render(rec *job.Record) view {
	h := w.downloads.Open(rec.ID)
	v := view{ID: rec.ID, created: rec.CreatedAt.Time}
	if dest, err := h.Destination(); err == nil {
		v.Path, v.Name = dest, path.Base(strings.ReplaceAll(dest, `\`, "/"))
	}
	// The one thing the facade will not answer. A handle knows where its bytes
	// land and not where they come from, so the source is read out of the
	// download layer's own spec. See feedback/2026-09-06-job-window.md.
	if spec, err := download.SpecOf(rec); err == nil && len(spec.Sources) > 0 {
		v.Source = spec.Sources[0].Locator
	}

	abandoned := !rec.State.Terminal() && !rec.Paused() && !rec.Delegated() && w.jobs.Claimable(rec)
	v.State, v.Tone = label(rec, abandoned)
	v.Holder = holder(rec, abandoned)
	// Which tier actually did this one. The record has always known and the
	// window only said so while the job was in flight, so the answer disappeared
	// at the moment it became a fact rather than a plan.
	v.Served = servedBy(rec)
	v.Step = step(rec.Progress.Step)
	v.Bar, v.Percent, v.Unknown = bar(rec)
	v.Moved = "last moved " + ago(rec.UpdatedAt.Time)
	v.moved = rec.UpdatedAt.Time
	if !rec.State.Terminal() && rec.Lease.Held(time.Now()) {
		v.lapses = rec.Lease.ExpiresAt.Time
	}
	v.Error = rec.Error
	// A failure is still today's business, so it stays with the live work even
	// though the contract calls it terminal.
	v.Finished = rec.State.Terminal() && rec.State != job.StateFailed
	v.Track = !rec.State.Terminal() || (rec.State == job.StateFailed && rec.Progress.Done > 0)

	_, pausable := h.(job.Pausable)
	waiting := rec.State == job.StateTransferred
	v.CanCancel = !rec.State.Terminal()
	v.CanCollect = waiting
	v.CanPause = pausable && v.CanCancel && !waiting && !rec.Paused()
	v.CanResume = pausable && v.CanCancel && !waiting && (rec.Paused() || abandoned)
	return v
}

func label(rec *job.Record, abandoned bool) (string, string) {
	switch {
	case rec.State == job.StateComplete:
		return "done", "good"
	case rec.State == job.StateFailed:
		return "failed", "bad"
	case rec.State == job.StateCancelled:
		return "cancelled", "muted"
	case rec.State == job.StateTransferred:
		return "waiting to be collected", "good"
	case rec.Wants() == job.WantCancel:
		return "cancelling", "warn"
	case rec.Paused():
		return "paused", "warn"
	case abandoned:
		return "stopped", "warn"
	case rec.State == job.StateRunning:
		return "downloading", "live"
	}
	return "queued", "muted"
}

// holder answers the question a person asks second, and the honest answer is
// often nobody: a lease that lapsed with the record still saying running is a
// process that was killed, which is the case this whole project is about.
func holder(rec *job.Record, abandoned bool) string {
	if d := rec.Delegation; d != nil {
		s := "handed to " + d.System
		if d.ExternalID != "" {
			s += " · " + d.ExternalID
		}
		if d.Delivered {
			s += " · collected"
		}
		return s
	}
	if rec.State.Terminal() {
		return ""
	}
	if rec.State == job.StateTransferred {
		return "here and proven, but nobody has collected it"
	}
	if rec.Lease.Held(time.Now()) {
		return rec.Lease.Owner + " is working on it, for another " + short(time.Until(rec.Lease.ExpiresAt.Time))
	}
	if abandoned && rec.State == job.StateRunning {
		return "nobody — " + rec.Lease.Owner + " stopped without saying so"
	}
	if rec.Paused() && rec.Intent != nil && rec.Intent.By != "" {
		return "paused by " + rec.Intent.By
	}
	return "nobody is working on it"
}

func step(s *job.Step) string {
	if s == nil {
		return ""
	}
	if s.Of > 0 {
		return fmt.Sprintf("%s — step %d of %d", s.Name, s.Ordinal, s.Of)
	}
	return s.Name
}

func bar(rec *job.Record) (string, float64, bool) {
	done, total := rec.Progress.Done, rec.Progress.Total
	switch rec.State {
	case job.StateComplete:
		// A finished job holds the whole artifact whatever its last progress
		// write said, and a delegate that reported nothing leaves that at zero.
		if total > done {
			done = total
		}
		return bytes(done), 100, false
	case job.StateCancelled:
		return "stopped at " + bytes(done), 0, false
	case job.StateFailed:
		if done == 0 {
			return "nothing arrived", 0, false
		}
	}
	if total <= 0 {
		if done == 0 {
			return "size not known yet", 0, true
		}
		return bytes(done) + " so far, of an unknown total", 0, true
	}
	pct := float64(done) / float64(total) * 100
	return fmt.Sprintf("%s of %s · %.0f%%", bytes(done), bytes(total), pct), pct, false
}

func bytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTP"[exp])
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return short(time.Since(t)) + " ago"
}

func short(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// spellingUnit is the step short prints in, and the boundaries are its own.
func spellingUnit(d time.Duration) time.Duration {
	switch {
	case d < time.Minute:
		return time.Second
	case d < time.Hour:
		return time.Minute
	case d < 48*time.Hour:
		return time.Hour
	}
	return 24 * time.Hour
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
