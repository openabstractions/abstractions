package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Memory lines (architect rulings 3 and 5, 2026-11-12) read the
// resource service's lease audit file as the runtime writes it under its
// state folder: a confirmed yield is one sentence naming both labels, an
// asker label written after its yield still names the asker, and memory no
// lease covers reads as the foreign sentence. A lease with no label reads
// by its signed FileDescription (R35 item 5), else by its file name with
// one log line, and a record with no program reads "a program on this
// computer". Labels and other events add no line.
func TestActivityMemoryLinesFromTheLeaseAudit(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, "resources", "audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	comfy, opencode := `C:\Comfy\python.exe`, `C:\Tools\opencode.exe`
	lines := []string{
		`{"at":"2026-11-12T09:00:00Z","event":"label","lease":"card-1","holder":"C:\\Comfy\\python.exe","label":"ComfyUI: Wan 2.2","amount":0,"took_ms":0}`,
		`{"at":"2026-11-12T09:01:00Z","event":"acquired","asker":"C:\\Comfy\\python.exe","lease":"card-1","resource":"card:0","amount":1,"took_ms":0}`,
		`{"at":"2026-11-12T09:02:00Z","event":"freed","holder":"C:\\Comfy\\python.exe","lease":"card-1","label":"ComfyUI: Wan 2.2","asker":"C:\\Tools\\opencode.exe","asker_lease":"card-2","resource":"card:0","amount":21474836480,"took_ms":0}`,
		`{"at":"2026-11-12T09:02:01Z","event":"label","lease":"card-2","holder":"C:\\Tools\\opencode.exe","label":"OpenCode: LTX-2.5","amount":0,"took_ms":0}`,
		`{"at":"2026-11-12T09:03:00Z","event":"freed","holder":"C:\\Engines\\llama-server.exe","lease":"card-3","asker":"C:\\Tools\\opencode.exe","asker_lease":"card-4","resource":"card:0","amount":1,"took_ms":0}`,
		`{"at":"2026-11-12T09:04:00Z","event":"freed","holder":"C:\\Comfy\\python.exe","lease":"card-1","asker":"C:\\Tools\\opencode.exe","asker_lease":"card-5","resource":"card:0","amount":1,"took_ms":0}`,
		`{"at":"2026-11-12T09:04:01Z","event":"label","lease":"card-5","holder":"C:\\Tools\\opencode.exe","label":"OpenCode","amount":0,"took_ms":0}`,
		`{"at":"2026-11-12T09:05:00Z","event":"foreign","asker":"C:\\Comfy\\python.exe","holder":"C:\\Ollama\\ollama.exe","resource":"card:0","amount":21474836480,"took_ms":0}`,
		`{"at":"2026-11-12T09:06:00Z","event":"freed","holder":"","lease":"card-6","asker":"C:\\Tools\\opencode.exe","asker_lease":"card-7","resource":"card:0","amount":1,"took_ms":0}`,
		`{"at":"2026-11-12T09:06:01Z","event":"label","lease":"card-7","holder":"C:\\Tools\\opencode.exe","label":"OpenCode: LTX-2.5","amount":0,"took_ms":0}`,
		`{"at":"2026-11-12T09:07:00Z","event":"foreign","asker":"C:\\Comfy\\python.exe","holder":"","resource":"card:0","amount":21474836480,"took_ms":0}`,
		`not json`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := memoryAuditPath
	t.Cleanup(func() { memoryAuditPath = old })
	memoryAuditPath = func() (string, error) { return path, nil }
	signed := signedDescription
	t.Cleanup(func() { signedDescription = signed })
	signedDescription = func(program string) string {
		return map[string]string{opencode: "OpenCode", `C:\Ollama\ollama.exe`: "Ollama"}[program]
	}
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/activity/memory", nil)
	page := decodePanel[activityPage](t, r.Code, r.Body.Bytes())
	want := []string{
		"OpenAbstractions freed memory in ComfyUI (Wan 2.2) for LTX-2.5, which OpenCode asked for.",
		"OpenAbstractions freed memory in Llama-server for OpenCode.",
		"OpenAbstractions freed memory in ComfyUI (Wan 2.2) for OpenCode.",
		"Ollama holds 20 GB that OpenAbstractions doesn't manage. Unload it in Ollama, or close Ollama.",
		"OpenAbstractions freed memory in a program on this computer for LTX-2.5, which OpenCode asked for.",
		"A program on this computer holds 20 GB that OpenAbstractions doesn't manage.",
	}
	if page.Outcome != "page" || len(page.Events) != len(want) {
		t.Fatalf("%+v", page)
	}
	for i, e := range page.Events {
		if e.What != want[i] || e.App != "Memory" || e.Result != "" {
			t.Errorf("line %d = %+v, want %q", i, e, want[i])
		}
	}
	if e := page.Events[0]; e.Program != opencode || e.Also != comfy || e.At != 1794474120000 {
		t.Errorf("the yield line names its asker and holder: %+v", e)
	}
	if e := page.Events[3]; e.Program != comfy || e.Also != `C:\Ollama\ollama.exe` {
		t.Errorf("the foreign line names its asker and the foreign program: %+v", e)
	}
	if n := strings.Count(logged.String(), "named by its file"); n != 1 || !strings.Contains(logged.String(), "llama-server.exe") {
		t.Errorf("the file-name fallback logs once, for llama-server: %q", logged.String())
	}
	panelRequest(t, panel, "/activity/memory", nil)
	if n := strings.Count(logged.String(), "named by its file"); n != 1 {
		t.Errorf("a reread logs the same program again: %q", logged.String())
	}
}

// A missing audit lists nothing: Memory lines are display data.
func TestActivityMemoryMissingAuditListsNothing(t *testing.T) {
	old := memoryAuditPath
	t.Cleanup(func() { memoryAuditPath = old })
	memoryAuditPath = func() (string, error) { return filepath.Join(t.TempDir(), "resources", "audit.jsonl"), nil }
	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/activity/memory", nil)
	page := decodePanel[activityPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || len(page.Events) != 0 {
		t.Fatalf("%+v", page)
	}
}

// The Device page's foreign-memory sentence names a program with no title by
// its file stem; /card/table logs that once per program, and a titled,
// leased or small row logs nothing.
func TestCardForeignNamedByFileLogsOnce(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	untitled, titled := `C:\Games\renderer.exe`, `C:\Ollama\ollama.exe`
	resources := []cardResourceState{{Resource: "card:0", Holders: []cardHolder{
		{Program: untitled, Amount: 2 << 30, Evidence: cardEvidenceVerified},
		{Program: titled, Amount: 2 << 30, Evidence: cardEvidenceVerified},
		{Program: `C:\Comfy\python.exe`, Amount: 2 << 30, Evidence: cardEvidenceVerified, Lease: "card-1"},
		{Program: `C:\Edge\msedge.exe`, Amount: 1 << 20, Evidence: cardEvidenceVerified},
	}}}
	titles := map[string]string{titled: "Ollama"}
	logForeignNamedByFile(resources, titles)
	logForeignNamedByFile(resources, titles)
	if n := strings.Count(logged.String(), "named by its file"); n != 1 || !strings.Contains(logged.String(), "renderer.exe") {
		t.Fatalf("%q", logged.String())
	}
}

// R35 review: a lease label names the app only for a program without a
// registered title or signed description, and never with a reserved title.
// A signed program is named by its description whatever its label says.
func TestMemoryLabelCannotNameAnotherApp(t *testing.T) {
	signed := signedDescription
	t.Cleanup(func() { signedDescription = signed })
	signedDescription = func(program string) string {
		return map[string]string{`C:\Tools\opencode.exe`: "OpenCode"}[program]
	}
	names := &programTitler{}
	got := freedSentence(names, `C:\Tools\opencode.exe`, "ComfyUI: Wan 2.2", `C:\Other\tool.exe`, "OpenAbstractions: LTX-2.5")
	if strings.Contains(got, "ComfyUI") || strings.Contains(got, "for LTX-2.5, which OpenAbstractions") {
		t.Fatalf("a label renamed a program: %q", got)
	}
	if !strings.Contains(got, "in OpenCode (Wan 2.2)") {
		t.Fatalf("the signed holder lost its name: %q", got)
	}
	if got := memoryAppName(names, `C:\Comfy\python.exe`, "ComfyUI"); got != "ComfyUI" {
		t.Fatalf("a nameless program's label app = %q", got)
	}
}
