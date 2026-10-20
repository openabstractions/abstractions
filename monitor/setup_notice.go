package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	logging "github.com/openabstractions/abstraction-logging/go"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The Setup page's one summary slot states why Install cannot run, how a
// step failed or that a step stopped moving (architect ruling 2026-11-08).
// Setup logs each such sentence once, when it first appears, with the raw
// error the page keeps off the screen.

// setupNoticeKinds are the sentences the page reports.
var setupNoticeKinds = map[string]logging.Level{"blocked": logging.LevelInfo, "failed": logging.LevelWarn, "stalled": logging.LevelWarn}

// maxSetupNotices bounds the sentences remembered as logged; past it the
// memory starts again, and a sentence may be logged once more.
const maxSetupNotices = 256

type setupNoticeRequest struct {
	Kind     string `json:"kind"`
	Sentence string `json:"sentence"`
	Detail   string `json:"detail,omitempty"`
}

// setupNotice logs a summary sentence the first time the page shows it and
// answers whether this call logged it.
func (p *servicePanel) setupNotice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var notice setupNoticeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&notice); err != nil {
		http.Error(w, "invalid setup notice", http.StatusBadRequest)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one setup notice required", http.StatusBadRequest)
		return
	}
	level, known := setupNoticeKinds[notice.Kind]
	if !known || notice.Sentence == "" || len(notice.Sentence) > 400 || len(notice.Detail) > 4000 ||
		!utf8.ValidString(notice.Sentence) || !utf8.ValidString(notice.Detail) {
		http.Error(w, "invalid setup notice", http.StatusBadRequest)
		return
	}
	logged := p.firstSetupNotice(notice.Kind + "\x00" + notice.Sentence + "\x00" + notice.Detail)
	if logged {
		attrs := map[string]string{"panel.action": "setup.notice", "setup.kind": notice.Kind, "setup.sentence": notice.Sentence}
		if notice.Detail != "" {
			attrs["setup.detail"] = notice.Detail
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
		defer cancel()
		//unchecked: a failure to log is shown in the Panel's sink state and does not change the page
		_ = p.log.record(ctx, level, "setup notice", attrs)
	}
	panelJSON(w, map[string]bool{"logged": logged})
}

// firstSetupNotice is whether key is new, and remembers it.
func (p *servicePanel) firstSetupNotice(key string) bool {
	p.setupNoticeMu.Lock()
	defer p.setupNoticeMu.Unlock()
	if p.setupNotices[key] {
		return false
	}
	if p.setupNotices == nil || len(p.setupNotices) >= maxSetupNotices {
		p.setupNotices = map[string]bool{}
	}
	p.setupNotices[key] = true
	return true
}

// setupInstallCancel records the person's Cancel on the approved install:
// the page and the package driver start no further step of it until the
// next Install click approves the plan again. A step already running
// finishes and records its phase.
func (p *servicePanel) setupInstallCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	step, ok := decodeInstallStep(w, r)
	if !ok {
		return
	}
	journal, err := p.setupJournalForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	record, err := journal.MarkInstall(ctx, step.SetupPlanID, func(record *setup.InstallRecord) { record.Cancelled = true })
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	panelJSON(w, map[string]any{"install": record})
}

// setupInstallCancelled is whether the person cancelled the approved
// install of planID.
func setupInstallCancelled(ctx context.Context, journal setup.Journal, planID string) bool {
	record, err := journal.LoadInstall(ctx)
	return err == nil && record.PlanID == planID && record.Cancelled
}
