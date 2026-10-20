package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-identity/listen"
	"golang.org/x/sys/windows"
)

// ASK-R3: a party to the question is refused under any spelling Windows
// resolves to the same program: another letter case and the 8.3 short name.
// The spellings are checked against the runtime and the verified asker,
// where no file-identity check applies, so a call site that compared plain
// strings fails here.
func TestSelfAnswerMatchesPartiesUnderWindowsSpellings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "A Long Folder Name For Short Names")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	party := writeProgram(t, dir, "Party Application.exe")
	target := writeProgram(t, t.TempDir(), "target-app.exe")

	long, err := windows.UTF16PtrFromString(party)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(long, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Fatalf("short name of %s: %v", party, err)
	}
	short := windows.UTF16ToString(buf[:n])
	spellings := map[string]string{"case variant": strings.ToUpper(party)}
	if strings.EqualFold(short, party) {
		t.Logf("8.3 short names are disabled on this volume; checking the case variant only")
	} else {
		spellings["short name"] = short
		spellings["lower-case short name"] = strings.ToLower(short)
	}
	for name, spelling := range spellings {
		if spelling == party {
			t.Fatalf("%s is the same string as %s", name, party)
		}
		if !selfAnswer(spelling, asks.Publisher{}, asks.Record{}, target, party) {
			t.Errorf("the runtime answered its own question as its %s %s", name, spelling)
		}
		bound := asks.Record{Via: listen.Seen{Bound: true, Path: party}}
		if !selfAnswer(spelling, asks.Publisher{}, bound, target, "") {
			t.Errorf("the asker answered its own question as its %s %s", name, spelling)
		}
		if !selfAnswer(spelling, asks.Publisher{}, asks.Record{}, party, "") {
			t.Errorf("the target answered its own question as its %s %s", name, spelling)
		}
	}
	if _, ok := spellings["short name"]; !ok {
		t.Skip("8.3 short names are disabled on this volume")
	}
}
