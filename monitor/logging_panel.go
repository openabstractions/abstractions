package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	logging "github.com/openabstractions/abstraction-logging/go"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
)

// Page bounds the Panel asks for; the contract allows up to 256 records and
// 65,536 encoded bytes.
const (
	logPageRecords = 64
	logPageBytes   = 65536
	logMaxWait     = 25 * time.Second
)

// hopView is one attestation as the page shows it. Standing is what the hop is
// worth once assessed from the history service's own stamp (LOG-I13).
type hopView struct {
	Hop      int    `json:"hop"`
	By       string `json:"by"`
	Role     string `json:"role"`
	Standing string `json:"standing"`
	Verified bool   `json:"verified"`
	Program  string `json:"program,omitempty"`
	Host     string `json:"host,omitempty"`
	User     string `json:"user,omitempty"`
	Exe      string `json:"exe,omitempty"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	PID      int    `json:"pid"`
}

// logRecordView is one retained record. A sink_gap is the LOG-S13 record a
// handler delivers on recovery, naming how many records it dropped and when.
type logRecordView struct {
	Kind      string            `json:"kind"`
	Time      string            `json:"time"`
	Level     int64             `json:"level"`
	LevelName string            `json:"levelName"`
	Message   string            `json:"message"`
	Job       string            `json:"job,omitempty"`
	Program   string            `json:"program,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
	Dropped   string            `json:"dropped,omitempty"`
	Since     string            `json:"since,omitempty"`
	Until     string            `json:"until,omitempty"`
	Hops      []hopView         `json:"hops"`
	Author    bool              `json:"author"`
	Disputed  bool              `json:"disputed"`
}

type logView struct {
	Mode      string          `json:"mode"`
	Outcome   string          `json:"outcome"`
	Cursor    string          `json:"cursor"`
	Next      string          `json:"next"`
	AtEnd     bool            `json:"atEnd"`
	Records   []logRecordView `json:"records"`
	Hidden    int             `json:"hidden"`
	Sink      panelSinkState  `json:"sink"`
	CheckedAt string          `json:"checkedAt"`
}

type logFilter struct {
	program      string
	level        *int64
	since, until time.Time
}

func (f logFilter) keep(r logRecordView) bool {
	if f.program != "" {
		match := strings.Contains(strings.ToLower(r.Program), f.program)
		for _, h := range r.Hops {
			match = match || strings.Contains(strings.ToLower(h.Program), f.program) || strings.Contains(strings.ToLower(h.Exe), f.program)
		}
		if !match {
			return false
		}
	}
	// A sink-loss gap is never hidden by level: it reports records that are gone.
	if f.level != nil && r.Kind != "sink_gap" && r.Level < *f.level {
		return false
	}
	if !f.since.IsZero() || !f.until.IsZero() {
		at, err := time.Parse(time.RFC3339Nano, r.Time)
		if err != nil || (!f.since.IsZero() && at.Before(f.since)) || (!f.until.IsZero() && at.After(f.until)) {
			return false
		}
	}
	return true
}

// presentRecord converts a retained record. The last hop is the history
// service's own stamp when it asserts verification: the Panel reads history
// only from the service it authenticated, and that service appends a stamp to
// every record it accepts (LOG-I6). The Panel authorises no relay, so an earlier
// hop another receiver asserted stays asserted (LOG-I14).
func presentRecord(value wire.Record) logRecordView {
	view := logRecordView{Kind: "record", Time: value.Time, Level: value.Level, LevelName: logging.Level(value.Level).String(),
		Message: value.Msg, Job: value.Job, Attrs: value.Attrs, Hops: []hopView{}}
	record, err := logging.DecodeRecord(wire.Encode(&value))
	if err != nil {
		for _, a := range value.Identity {
			view.Hops = append(view.Hops, hopView{Hop: int(a.Hop), By: a.By, Role: "unreadable", Standing: "claimed", UID: -1, GID: -1, PID: -1})
		}
		return view
	}
	var anchor *logging.Attestation
	if n := len(record.Identity); n > 0 && record.Identity[n-1].Verified && record.Identity[n-1].By != logging.BySelf {
		anchor = &record.Identity[n-1]
	}
	provenance := record.Assess(anchor, logging.Policy{})
	for k, a := range record.Identity {
		role := "relay"
		switch {
		case k == 0 && a.By == logging.BySelf:
			role, view.Program = "writer claim", a.Program
		case k == 0 && a.By == logging.ByUnclaimed:
			role = "no writer claim"
		case k == 0:
			role = "writer"
		case k == 1:
			role = "writer"
		}
		view.Hops = append(view.Hops, hopView{Hop: k, By: a.By, Role: role, Standing: provenance.Standing[k].String(), Verified: a.Verified,
			Program: a.Program, Host: a.Host, User: a.User, Exe: a.Exe, UID: a.UID, GID: a.GID, PID: a.PID})
	}
	_, view.Author = provenance.Author()
	view.Disputed = provenance.Disputed()
	if value.Level == int64(logging.LevelWarn) && value.Msg == logging.GapMessage {
		dropped, since, until := value.Attrs["dropped"], value.Attrs["since"], value.Attrs["until"]
		if _, err := strconv.ParseUint(dropped, 10, 64); err == nil && since != "" && until != "" {
			view.Kind, view.Dropped, view.Since, view.Until = "sink_gap", dropped, since, until
		}
	}
	return view
}

func parseLogFilter(r *http.Request) (logFilter, error) {
	q := r.URL.Query()
	f := logFilter{program: strings.ToLower(strings.TrimSpace(q.Get("program")))}
	if len(f.program) > 256 || !utf8.ValidString(f.program) {
		return f, errors.New("invalid program filter")
	}
	if level := q.Get("level"); level != "" {
		n, err := strconv.ParseInt(level, 10, 64)
		if err != nil || n < -100 || n > 100 {
			return f, errors.New("invalid level filter")
		}
		f.level = &n
	}
	for _, item := range []struct {
		name string
		at   *time.Time
	}{{"since", &f.since}, {"until", &f.until}} {
		if text := q.Get(item.name); text != "" {
			at, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return f, errors.New("invalid " + item.name + " filter: RFC 3339 required")
			}
			*item.at = at
		}
	}
	return f, nil
}

// failureText names a logging service refusal by its code before its message.
func failureText(err error) string {
	var service *wire.ServiceError
	if errors.As(err, &service) && service.Message != "" {
		return string(service.Code) + ": " + service.Message
	}
	return err.Error()
}

// panelAbsence reports a failed resolution or call. An absent runtime says so
// in words before the resolution error.
func panelAbsence(w http.ResponseWriter, err error) {
	var resolution *client.ResolutionError
	if errors.As(err, &resolution) && resolution.Status == client.RuntimeUnavailable {
		http.Error(w, "The runtime is absent: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, failureText(err), http.StatusServiceUnavailable)
}

// logging reads retained history through abstraction.logging/reader@1, or with
// follow=1 waits for later records through abstraction.logging/observer@1. The
// cursor is the service's opaque continuation; a gap is returned as its outcome
// and never restarted here. Filters narrow the bounded page after it is read.
func (p *servicePanel) logging(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	q := r.URL.Query()
	cursor := q.Get("cursor")
	if len(cursor) > 512 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid history cursor", 400)
		return
	}
	filter, err := parseLogFilter(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	follow := q.Get("follow") == "1"
	wait := 10 * time.Second
	if text := q.Get("wait"); text != "" {
		ms, err := strconv.ParseInt(text, 10, 64)
		if err != nil || ms < 0 || time.Duration(ms)*time.Millisecond > logMaxWait {
			http.Error(w, "invalid wait: 0 to 25000 milliseconds", 400)
			return
		}
		wait = time.Duration(ms) * time.Millisecond
	}
	view := logView{Mode: "read", Cursor: cursor, Records: []logRecordView{}}
	var page wire.Page
	if follow {
		view.Mode = "follow"
		ctx, cancel := context.WithTimeout(r.Context(), wait+5*time.Second)
		defer cancel()
		observer, err := panelMachine().ResolveLogObserver(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			panelAbsence(w, err)
			return
		}
		if observer, err = observer.WithTimeout(wait + 3*time.Second); err != nil {
			panelError(w, err)
			return
		}
		page, err = observer.ObserveContext(ctx, cursor, logPageRecords, logPageBytes, wait.Milliseconds())
		if err != nil {
			panelAbsence(w, err)
			return
		}
	} else {
		ctx, cancel := panelCall(r)
		defer cancel()
		reader, err := panelMachine().ResolveLogReader(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			panelAbsence(w, err)
			return
		}
		page, err = reader.ReadContext(ctx, cursor, logPageRecords, logPageBytes)
		if err != nil {
			panelAbsence(w, err)
			return
		}
	}
	view.Outcome, view.Next, view.AtEnd = page.Outcome.String(), page.Next, page.AtEnd
	for _, record := range page.Records {
		presented := presentRecord(record)
		if filter.keep(presented) {
			view.Records = append(view.Records, presented)
		} else {
			view.Hidden++
		}
	}
	view.Sink = p.log.state()
	view.CheckedAt = time.Now().Format(time.RFC3339)
	panelJSON(w, view)
}
