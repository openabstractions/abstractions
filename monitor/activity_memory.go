package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/modelplace"
)

// The Activity timeline's Memory lines (architect rulings 3 and 5,
// 2026-11-12): one line per confirmed yield names the holder, requested
// model and asker. One line per blocked load names the unmanaged holder
// and tells the person how to free that memory.
// The resource service writes both to its lease audit, one JSON object per
// line under the runtime's state folder (serve runtime_resources.go,
// <state>/resources/audit.jsonl). This Panel runs as the same account and
// reads that file; it is display data, so a missing or unreadable audit
// lists nothing and logs one line.

// memoryAuditLines and memoryAuditBytes bound one read. The audit rolls at
// one megabyte (abstraction-resource service.MaxAudit) into one previous
// file, audit.jsonl.1; a read takes at most memoryAuditBytes of each file
// and keeps the last memoryAuditLines lines of the two.
const (
	memoryAuditLines = 1 << 15
	memoryAuditBytes = 4 << 20
)

// memoryActivityEvents is the most lines one answer carries, newest kept.
const memoryActivityEvents = 512

// memoryAuditPath is the lease audit of the runtime this Panel serves: under
// the -runtime-state-dir it was given, else under this account's installed
// runtime state (modelplace.RuntimeStateFor, the folder serve's own
// "resources audit" reads). A named runtime without a state folder has no
// audit here. Tests replace it.
var memoryAuditPath = func() (string, error) {
	state := ""
	if panelExplicit != nil {
		if panelExplicit.StateDir == "" {
			return "", fmt.Errorf("the runtime at %s was named without its state folder", panelExplicit.Endpoint)
		}
		state = panelExplicit.StateDir
	} else {
		resolved, err := modelplace.RuntimeStateFor(runtime.GOOS, os.Getenv, os.UserHomeDir)
		if err != nil {
			return "", err
		}
		state = resolved
	}
	return filepath.Join(state, "resources", "audit.jsonl"), nil
}

// memoryAuditRecord is the part of one audit line the Memory lines read.
type memoryAuditRecord struct {
	At     string `json:"at"`
	Event  string `json:"event"`
	Asker  string `json:"asker"`
	Holder string `json:"holder"`
	// Lease is decoded from "lease": the json package matches field names
	// case-insensitively.
	Lease      string
	Label      string `json:"label"`
	AskerLease string `json:"asker_lease"`
	Resource   string `json:"resource"`
	Amount     int64  `json:"amount"`
}

// activityMemory answers GET with the retained Memory lines.
func (p *servicePanel) activityMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	out := activityPage{Outcome: "page", Events: []activityEvent{}, Complete: true}
	path, err := memoryAuditPath()
	if err != nil {
		log.Printf("activity: memory lines unavailable: %v", err)
		panelJSON(w, out)
		return
	}
	lines, err := readMemoryAudit(path)
	if err != nil {
		log.Printf("activity: memory lines unavailable: %v", err)
		panelJSON(w, out)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	out.Events = memoryActivityLines(lines, p.programTitler(ctx))
	panelJSON(w, out)
}

// readMemoryAudit is the last memoryAuditLines non-empty lines of the audit
// at path and its previous file, oldest first. A missing file adds nothing.
func readMemoryAudit(path string) ([]string, error) {
	var lines []string
	for _, name := range []string{path + ".1", path} {
		file, err := os.Open(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(io.LimitReader(file, memoryAuditBytes))
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); line != "" {
				lines = append(lines, line)
			}
		}
		err = scanner.Err()
		//unchecked: a read-only file; the scan's own error is the one reported
		file.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(lines) > memoryAuditLines {
		lines = lines[len(lines)-memoryAuditLines:]
	}
	return lines, nil
}

// memoryActivityLines words the freed and foreign records of lines, oldest
// first. A lease's label comes from the latest label record for that lease
// anywhere in lines, since an asker labels its lease after the yield it
// asked for; a freed record's own label wins for its holder.
func memoryActivityLines(lines []string, names *programTitler) []activityEvent {
	records := make([]memoryAuditRecord, 0, len(lines))
	labels := map[string]string{}
	for _, line := range lines {
		var record memoryAuditRecord
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if record.Event == "label" && record.Lease != "" {
			labels[record.Lease] = record.Label
			continue
		}
		records = append(records, record)
	}
	events := []activityEvent{}
	for _, record := range records {
		at, err := time.Parse(time.RFC3339Nano, record.At)
		if err != nil {
			continue
		}
		event := activityEvent{At: at.UnixMilli(), App: "Memory"}
		switch record.Event {
		case "freed":
			holderLabel := record.Label
			if holderLabel == "" {
				holderLabel = labels[record.Lease]
			}
			askerLabel := ""
			if record.AskerLease != "" {
				askerLabel = labels[record.AskerLease]
			}
			event.What = freedSentence(names, record.Holder, holderLabel, record.Asker, askerLabel)
			event.Program, event.Also = record.Asker, record.Holder
		case "foreign":
			event.What = foreignMemorySentence(memoryProgramName(names, record.Holder), record.Amount)
			if event.What == "" {
				continue
			}
			event.Program, event.Also = record.Asker, record.Holder
		default:
			continue
		}
		events = append(events, event)
	}
	if len(events) > memoryActivityEvents {
		events = events[len(events)-memoryActivityEvents:]
	}
	return events
}

// freedSentence is one confirmed yield (ruling 3): "OpenAbstractions freed
// memory in <holder app> (<holder model>) for <asker model>, which <asker
// app> asked for." A label is "<app>: <model>" as its lease holder set it,
// split at the first ": ". A missing label names the program. A holder
// with no model part drops the parentheses. An asker with no model part
// is named once as the app the memory was freed for.
func freedSentence(names *programTitler, holder, holderLabel, asker, askerLabel string) string {
	holderApp, holderModel := splitLeaseLabel(holderLabel)
	holderApp = memoryAppName(names, holder, holderApp)
	askerApp, askerModel := splitLeaseLabel(askerLabel)
	askerApp = memoryAppName(names, asker, askerApp)
	in := holderApp
	if holderModel != "" {
		in += " (" + holderModel + ")"
	}
	if askerModel == "" {
		return "OpenAbstractions freed memory in " + in + " for " + askerApp + "."
	}
	return "OpenAbstractions freed memory in " + in + " for " + askerModel + ", which " + askerApp + " asked for."
}

// splitLeaseLabel is a label's app and model parts; a label without ": " is
// all app.
func splitLeaseLabel(label string) (app, model string) {
	label = strings.TrimSpace(label)
	if i := strings.Index(label, ": "); i >= 0 {
		return strings.TrimSpace(label[:i]), strings.TrimSpace(label[i+2:])
	}
	return label, ""
}

// foreignMemorySentence is ruling 5's sentence for memory no lease covers,
// worded and sized as the provider kit's refusal words it
// (abstraction-provider-kit/go/refusal foreignSentence): whole gigabytes of
// 2^30, at least 1. A program with no name to give holds memory the person
// is told about in the first sentence alone. "" for no amount.
func foreignMemorySentence(name string, amount int64) string {
	if amount <= 0 {
		return ""
	}
	gb := (amount + 1<<29) >> 30
	if gb < 1 {
		gb = 1
	}
	if name == "" || name == unknownProgramWords {
		return fmt.Sprintf("A program on this computer holds %d GB that OpenAbstractions doesn't manage.", gb)
	}
	return fmt.Sprintf("%s holds %d GB that OpenAbstractions doesn't manage. Unload it in %s, or close %s.", name, gb, name, name)
}

// unknownProgramWords names a program the audit gives no path for.
const unknownProgramWords = "a program on this computer"

// memoryProgramName names a program in a Memory line by the Device page's
// rule (programTitler.deviceName, architect ruling R35 item 5): its
// registered or product title, else its signed FileDescription. The file
// name is the last resort and logs one line per program
// (programFileTitle). An empty program reads unknownProgramWords.
// memoryAppName names a lease's program in a Memory line. A program with a
// registered title or a signed file description is named by it, whatever its
// label says. A program without either is
// named by its label's app part, which passes the same check as a file
// description, so no label reads "OpenAbstractions" or another reserved
// title.
func memoryAppName(names *programTitler, program, labelApp string) string {
	if strings.TrimSpace(program) != "" {
		if name := names.deviceName(program); name != "" {
			return name
		}
	}
	if name := deviceDescription(labelApp); name != "" {
		return name
	}
	return memoryProgramName(names, program)
}

func memoryProgramName(names *programTitler, program string) string {
	if strings.TrimSpace(program) == "" {
		return unknownProgramWords
	}
	if name := names.deviceName(program); name != "" {
		return name
	}
	name := programFileTitle(program)
	if name == "" {
		return unknownProgramWords
	}
	logNameFallback(program)
	return name
}

// programFileTitle is the provider kit refusal's file-name rule (refusal
// programTitle): the file name without .exe, an all-lowercase name
// capitalized as a product name, and a
// package family by its package name.
func programFileTitle(program string) string {
	name := program
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > len(".exe") && strings.EqualFold(name[len(name)-len(".exe"):], ".exe") {
		name = name[:len(name)-len(".exe")]
	}
	if !strings.ContainsAny(program, `/\`) {
		name = strings.TrimPrefix(name, "msix:")
		if i := strings.LastIndexByte(name, '_'); i > 0 {
			name = name[:i]
		}
	}
	if name != "" && name == strings.ToLower(name) && name[0] >= 'a' && name[0] <= 'z' {
		name = string(name[0]-('a'-'A')) + name[1:]
	}
	return name
}

// namedByFile holds the programs already logged as named by their file, so
// a live page that rereads every few seconds logs each one once.
var (
	namedByFileMu sync.Mutex
	namedByFile   = map[string]bool{}
)

// logNameFallback writes one log line the first time this process names
// program by its file: it has no registered title and no signed
// FileDescription.
func logNameFallback(program string) {
	namedByFileMu.Lock()
	defer namedByFileMu.Unlock()
	if namedByFile[program] {
		return
	}
	if len(namedByFile) >= 512 {
		clear(namedByFile)
	}
	namedByFile[program] = true
	log.Printf("memory: %s has no registered title or file description; named by its file", program)
}
