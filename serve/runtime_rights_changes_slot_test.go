package main

import (
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-asks/go/catalogue"
)

// Security review 2026-11-22, LOW: a long program path keeps its drive and
// the start of its folder, then an ellipsis and the file name.
func TestProgramSlotKeepsDriveAndFileName(t *testing.T) {
	program := `C:\Users\person\` + strings.Repeat("deep\\", 60) + `local.exe`
	slot := programSlot(program)
	if utf8.RuneCountInString(slot) > catalogue.MaxSlot || !strings.HasPrefix(slot, `C:\Users\person\deep`) || !strings.HasSuffix(slot, `…\local.exe`) {
		t.Fatal(slot)
	}
	if short := `C:\Tools\local.exe`; programSlot(short) != short {
		t.Fatal(programSlot(short))
	}
	// The change sentence keeps the same shape within its own room (F5).
	words := declarationWords(rwire.PolicyChange{Kind: rwire.ChangeKindDeclaration, Name: "localai", Program: program})
	if !strings.Contains(words, `(C:\Users\person\deep`) || !strings.Contains(words, `…\local.exe)`) || utf8.RuneCountInString(words) > catalogue.MaxSlot {
		t.Fatal(words)
	}
}

// Security review 2026-11-22, F3: a long address keeps its scheme and host
// whole and shortens only its path, so a long subdomain cannot hide the real
// domain; a host too long for the room keeps the domain at its end.
func TestAddressKeepsSchemeAndHost(t *testing.T) {
	host := strings.Repeat("login.", 12) + "attacker.invalid"
	address := "https://" + host + "/v1/" + strings.Repeat("path/", 40)
	words := addressWords(address)
	if words != "https://"+host+"…" {
		t.Fatal(words)
	}
	if words := addressWords("https://api.example.com/" + strings.Repeat("p", 100)); !strings.HasPrefix(words, "https://api.example.com/ppp") || utf8.RuneCountInString(words) != 80 {
		t.Fatal(words)
	}
	if fitted := addressFit(address, 40); !strings.HasSuffix(fitted, "attacker.invalid") || !strings.HasPrefix(fitted, "https://…") || utf8.RuneCountInString(fitted) > 40 {
		t.Fatal(fitted)
	}
	if slot := addressFit(address, catalogue.MaxSlot); !strings.HasPrefix(slot, "https://"+host) || utf8.RuneCountInString(slot) > catalogue.MaxSlot {
		t.Fatal(slot)
	}
	if short := "https://api.example.com/v1"; addressWords(short) != short {
		t.Fatal(addressWords(short))
	}
}
