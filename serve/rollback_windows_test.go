package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestUserActivationFollowsEachReleasedLayout(t *testing.T) {
	root := `D:\Tools\OpenAbstractions`
	jobdw := filepath.Join(root, "tools", "jobdw.exe")
	twin := filepath.Join(root, "tools", "openabstractionsw.exe")
	none := func(string) bool { return false }
	for version, want := range map[string][]string{
		"0.1.4":   {"start"},
		"0.0.9":   {"start"},
		"0.1.5":   {"start"},
		"0.1.5.0": {"start"},
		"0.1.6":   {"start", "--runtime"},
		"0.1.7":   {"start", "--runtime"},
	} {
		image, args, err := userActivation(root, version, none)
		if err != nil || image != jobdw || !reflect.DeepEqual(args, want) {
			t.Fatalf("%s: %s %v err=%v, want jobdw.exe %v", version, image, args, err, want)
		}
	}
	// A layout that ships the windowless serve link is started through it,
	// whatever its version says; a local build is 0.0.0.
	shipsTwin := func(path string) bool { return path == twin }
	for _, version := range []string{"0.1.8", "0.0.0", "1.0.0", "not a version"} {
		image, args, err := userActivation(root, version, shipsTwin)
		if err != nil || image != twin || !reflect.DeepEqual(args, []string{"start"}) {
			t.Fatalf("%s: %s %v err=%v", version, image, args, err)
		}
	}
	for _, bad := range []string{"", "0.1", "a.b.c", "0.-1.5", "0.1.x"} {
		if _, _, err := userActivation(root, bad, none); err == nil {
			t.Fatalf("accepted version %q", bad)
		}
	}
}

type startedCall struct {
	image string
	args  []string
}

func TestRestartUserPredecessorsRunsEachProductsActivation(t *testing.T) {
	codeC := "{00000000-0000-0000-0000-00000000000C}"
	codeD := "{00000000-0000-0000-0000-00000000000D}"
	codeE := "{00000000-0000-0000-0000-00000000000E}"
	codeF := "{00000000-0000-0000-0000-00000000000F}"
	values := map[string]map[string]string{
		codeA: {"VersionString": "0.1.5", "InstallLocation": `D:\Tools\OpenAbstractions\`},
		codeB: {"VersionString": "0.1.7", "InstallLocation": testFolder + `\`},
		codeC: {"VersionString": "0.1.6"},
		codeD: {"VersionString": "0.1.7", "InstallLocation": `D:\Removed\OpenAbstractions\`},
		codeE: {"VersionString": "0.1.8", "InstallLocation": `D:\Broken\OpenAbstractions\`},
		codeF: {"VersionString": "0.1.8", "InstallLocation": `D:\Current\OpenAbstractions\`},
	}
	present := map[string]bool{
		`D:\Tools\OpenAbstractions\tools\jobdw.exe`:               true,
		testFolder + `\tools\jobdw.exe`:                           true,
		`D:\Broken\OpenAbstractions\tools\openabstractionsw.exe`:  true,
		`D:\Current\OpenAbstractions\tools\openabstractionsw.exe`: true,
	}
	var calls []startedCall
	run := func(image string, args []string) error {
		calls = append(calls, startedCall{image, args})
		if strings.HasPrefix(image, `D:\Broken`) {
			return errors.New("exit status 1")
		}
		return nil
	}
	list := strings.Join([]string{codeA, codeB, codeC, codeD, codeE, codeF}, ";")
	started, notes, err := restartUserPredecessors(list, fakeProductInfo(values, nil), func(p string) bool { return present[p] }, run)
	want := []startedCall{
		{`D:\Tools\OpenAbstractions\tools\jobdw.exe`, []string{"start"}},
		// A 0.1.7 predecessor is started the way its Startup shortcut starts it.
		{testFolder + `\tools\jobdw.exe`, []string{"start", "--runtime"}},
		{`D:\Broken\OpenAbstractions\tools\openabstractionsw.exe`, []string{"start"}},
		{`D:\Current\OpenAbstractions\tools\openabstractionsw.exe`, []string{"start"}},
	}
	if started != 3 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("started=%d calls=%v, want %v", started, calls, want)
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "records no install location") || !strings.Contains(notes[1], `has no D:\Removed`) {
		t.Fatalf("notes=%v", notes)
	}
	if err == nil || !strings.Contains(err.Error(), codeE) || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("failed start not reported: %v", err)
	}
}

func TestRestartUserPredecessorsRefusesUnregisteredProduct(t *testing.T) {
	ran := false
	run := func(string, []string) error { ran = true; return nil }
	if _, _, err := restartUserPredecessors(codeA, fakeProductInfo(nil, nil), func(string) bool { return true }, run); err == nil || ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
	denied := map[string]error{codeA + "/VersionString": syscall.Errno(5)}
	if _, _, err := restartUserPredecessors(codeA, fakeProductInfo(map[string]map[string]string{codeA: {}}, denied),
		func(string) bool { return true }, run); err == nil || ran {
		t.Fatalf("registration read failure hidden: err=%v ran=%v", err, ran)
	}
}

func TestMachineRollbackStartsOnlyStoppedInstances(t *testing.T) {
	states := map[string]uint32{
		hostServiceName + "_1a": windows.SERVICE_STOPPED,
		hostServiceName + "_2b": windows.SERVICE_RUNNING,
		hostServiceName + "_3c": windows.SERVICE_STOPPED,
		hostServiceName + "_4d": windows.SERVICE_STOPPED,
		hostServiceName + "_5e": windows.SERVICE_STOPPED,
	}
	names := []string{hostServiceName + "_1a", hostServiceName + "_2b", hostServiceName + "_3c", hostServiceName + "_4d", hostServiceName + "_5e", hostServiceName + "_6f"}
	var starts []string
	started, notes, err := startStoppedInstances(instanceControl{
		list: func() ([]string, error) { return names, nil },
		state: func(name string) (uint32, error) {
			state, ok := states[name]
			if !ok {
				return 0, windows.ERROR_SERVICE_DOES_NOT_EXIST
			}
			return state, nil
		},
		start: func(name string) error {
			starts = append(starts, name)
			switch name {
			case hostServiceName + "_3c":
				return windows.ERROR_SERVICE_ALREADY_RUNNING
			case hostServiceName + "_4d":
				return windows.ERROR_ACCESS_DENIED
			case hostServiceName + "_5e":
				return windows.ERROR_SERVICE_DOES_NOT_EXIST
			}
			return nil
		},
	})
	wantStarts := []string{hostServiceName + "_1a", hostServiceName + "_3c", hostServiceName + "_4d", hostServiceName + "_5e"}
	if !reflect.DeepEqual(starts, wantStarts) || started != 2 || len(notes) != 2 || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("starts=%v started=%d notes=%v err=%v", starts, started, notes, err)
	}
	listFailure := errors.New("enumeration denied")
	if _, _, err := startStoppedInstances(instanceControl{list: func() ([]string, error) { return nil, listFailure }}); !errors.Is(err, listFailure) {
		t.Fatal(err)
	}
}

// The real runner waits for an inert activation that exits and reports a
// nonzero exit. It inherits no output pipes.
func TestRunActivationWaitsForCommandExit(t *testing.T) {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	if err := runActivation(comspec, []string{"/d", "/c", "exit", "0"}); err != nil {
		t.Fatal(err)
	}
	if err := runActivation(comspec, []string{"/d", "/c", "exit", "7"}); err == nil {
		t.Fatal("nonzero activation exit accepted")
	}
}
