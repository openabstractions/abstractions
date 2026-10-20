package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
)

// Graphics driver resets on the Device page (resource RES-G6): a provider's
// Guard writes each fault as one "gpu fault" log record whose
// gpu_fault.line attribute is the fault's one Panel sentence, naming the
// engines stopped and whether the running request is paused. The page shows
// that sentence for each fault of the last seven days whose record the
// logging service verified the writer of, newest first, and only when that
// writer is a provider whose registered declaration names a card: resource,
// the providers that attach a Guard. Any program may write a "gpu fault"
// record under its own name; only a card provider's is the Guard's.
//
// History reads forward from its earliest record, so the Panel keeps one
// scan per Panel: each read continues from where the last stopped, a few
// pages at a time, and then follows the end. A gap starts it again.

const (
	gpuFaultMessage = "gpu fault"
	gpuFaultLineKey = "gpu_fault.line"
	gpuFaultAtKey   = "gpu_fault.at"
	// gpuFaultWindow is how far back a fault is shown.
	gpuFaultWindow = 7 * 24 * time.Hour
	// gpuFaultShown is how many faults the page shows.
	gpuFaultShown = 3
	// gpuFaultPagesPerRead bounds one read's history pages.
	gpuFaultPagesPerRead = 32
	gpuFaultPageRecords  = 256
)

// gpuFaultLine is one fault as the page shows it.
type gpuFaultLine struct {
	At   string `json:"at"`
	Line string `json:"line"`
	// By names the verified writer: its title, else its file stem.
	By string `json:"by,omitempty"`
}

// gpuFaultView is GET /card/faults. Outcome is "page" once the scan has
// reached the history's end, "reading" while it is still behind, or the
// logging outcome that stopped it.
type gpuFaultView struct {
	Outcome string         `json:"outcome"`
	Faults  []gpuFaultLine `json:"faults"`
	Reason  string         `json:"reason,omitempty"`
}

// gpuFaultScan is one Panel's progress through the history.
type gpuFaultScan struct {
	mu     sync.Mutex
	cursor string
	atEnd  bool
	faults []gpuFaultFound
}

// gpuFaultFound is a fault record and its verified writer program.
type gpuFaultFound struct {
	at      time.Time
	line    string
	program string
}

var gpuFaultScans sync.Map // *servicePanel → *gpuFaultScan

func (p *servicePanel) gpuFaultScan() *gpuFaultScan {
	scan, _ := gpuFaultScans.LoadOrStore(p, &gpuFaultScan{})
	return scan.(*gpuFaultScan)
}

// gpuFaultOf reads one record as a fault: the fault message, a line, and a
// writer the history service verified.
func gpuFaultOf(record wire.Record) (gpuFaultFound, bool) {
	if record.Msg != gpuFaultMessage || record.Attrs[gpuFaultLineKey] == "" {
		return gpuFaultFound{}, false
	}
	view := presentRecord(record)
	if !view.Author {
		return gpuFaultFound{}, false
	}
	program := ""
	for _, hop := range view.Hops {
		if hop.Verified && hop.Role == "writer" {
			program = hop.Exe
			if program == "" {
				program = hop.Program
			}
			break
		}
	}
	at, err := time.Parse(time.RFC3339Nano, record.Attrs[gpuFaultAtKey])
	if err != nil {
		at, err = time.Parse(time.RFC3339Nano, record.Time)
		if err != nil {
			return gpuFaultFound{}, false
		}
	}
	return gpuFaultFound{at: at, line: record.Attrs[gpuFaultLineKey], program: program}, true
}

// advance reads up to gpuFaultPagesPerRead pages from the scan's cursor.
// It returns the logging outcome when one stopped the read.
func (s *gpuFaultScan) advance(ctx context.Context) (string, error) {
	reader, err := panelMachine().ResolveLogReader(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return "", err
	}
	restarted := false
	for pages := 0; pages < gpuFaultPagesPerRead; pages++ {
		page, err := reader.ReadContext(ctx, s.cursor, gpuFaultPageRecords, logPageBytes)
		if err != nil {
			return "", err
		}
		if page.Outcome == wire.PageOutcomeGap && !restarted {
			s.cursor, s.atEnd, s.faults, restarted = "", false, nil, true
			continue
		}
		if page.Outcome != wire.PageOutcomePage {
			return page.Outcome.String(), nil
		}
		for _, record := range page.Records {
			if fault, ok := gpuFaultOf(record); ok {
				s.faults = append(s.faults, fault)
			}
		}
		if page.Next != "" {
			s.cursor = page.Next
		}
		if page.AtEnd {
			s.atEnd = true
			return "", nil
		}
	}
	return "", nil
}

// recent keeps the faults inside the window and returns the newest first
// whose writer show admits.
func (s *gpuFaultScan) recent(now time.Time, show func(program string) bool) []gpuFaultFound {
	kept := s.faults[:0]
	for _, fault := range s.faults {
		if now.Sub(fault.at) <= gpuFaultWindow {
			kept = append(kept, fault)
		}
	}
	s.faults = kept
	out := make([]gpuFaultFound, 0, gpuFaultShown)
	for i := len(kept) - 1; i >= 0 && len(out) < gpuFaultShown; i-- {
		if show(kept[i].program) {
			out = append(out, kept[i])
		}
	}
	return out
}

// cardProviders reads the programs of the registered declarations that name
// a card: resource: the providers the runtime grants card and wake rules
// (serve permitCardRules) and that attach a GPU fault Guard (RES-G1..G6).
func cardProviders(ctx context.Context) ([]string, error) {
	registry, err := panelMachine().ResolveRegistry(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	list, err := registry.Declarations(ctx)
	if err != nil {
		return nil, err
	}
	var programs []string
	for _, state := range list.Declarations {
		d := state.Declaration
		if d.Program == "" {
			continue
		}
		for _, resource := range d.Resources {
			if strings.HasPrefix(resource, "card:") {
				programs = append(programs, d.Program)
				break
			}
		}
	}
	return programs, nil
}

// isCardProvider reports whether program is one of providers' files.
func isCardProvider(providers []string, program string) bool {
	if program == "" {
		return false
	}
	for _, provider := range providers {
		if identity.SameSubjectProgram(provider, program) {
			return true
		}
	}
	return false
}

// cardFaults answers the page's graphics driver lines.
func (p *servicePanel) cardFaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	scan := p.gpuFaultScan()
	scan.mu.Lock()
	defer scan.mu.Unlock()
	view := gpuFaultView{Outcome: "page", Faults: []gpuFaultLine{}}
	outcome, err := scan.advance(ctx)
	switch {
	case err != nil:
		view.Outcome, view.Reason = "unavailable", err.Error()
	case outcome != "":
		view.Outcome = outcome
	case !scan.atEnd:
		view.Outcome = "reading"
	}
	var found []gpuFaultFound
	if len(scan.faults) > 0 {
		// An unreadable registry shows no line: a writer that cannot be
		// checked is not shown as the Guard.
		//unchecked: the error leaves providers empty, which shows nothing
		providers, _ := cardProviders(ctx)
		found = scan.recent(time.Now(), func(program string) bool { return isCardProvider(providers, program) })
	}
	var titles *programTitler
	for _, fault := range found {
		line := gpuFaultLine{At: fault.at.UTC().Format(time.RFC3339), Line: fault.line}
		if fault.program != "" {
			if titles == nil {
				titles = p.programTitler(ctx)
			}
			line.By = programReasonName(titles, fault.program)
		}
		view.Faults = append(view.Faults, line)
	}
	panelJSON(w, view)
}
