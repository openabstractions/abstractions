package product

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// A junction to the per-machine folder is no per-machine folder, and an 8.3
// alias of it is.
func TestShippedFollowsNoJunction(t *testing.T) {
	machine := filepath.Join(t.TempDir(), "OpenAbstractions tools")
	if err := os.Mkdir(machine, 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(machine, "lemonaded.exe")
	if err := os.WriteFile(program, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "tools")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, machine).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	protected := []string{machine}
	if !Shipped(program, protected, nil, nil) {
		t.Fatal("the per-machine folder's program has no standing")
	}
	if Shipped(filepath.Join(link, "lemonaded.exe"), protected, nil, nil) || SameFolder(link, machine) {
		t.Error("a junction to the per-machine folder counts as that folder")
	}
	short, aliased := shortPath(t, machine)
	if !aliased {
		t.Logf("the volume keeps no 8.3 alias for %s", machine)
		return
	}
	if !Shipped(filepath.Join(short, "lemonaded.exe"), protected, nil, nil) {
		t.Errorf("the 8.3 alias %s of the per-machine folder is not that folder", short)
	}
	if !Shipped(program, []string{short}, nil, nil) {
		t.Errorf("the per-machine folder named by its 8.3 alias %s does not vouch for its program", short)
	}
}

// A program reached through a junction is not the program the junction
// reaches; its 8.3 alias is. Invalid and relative names never match.
func TestSameProgramFollowsNoJunction(t *testing.T) {
	machine := filepath.Join(t.TempDir(), "OpenAbstractions tools")
	if err := os.Mkdir(machine, 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(machine, "openabstractions.exe")
	if err := os.WriteFile(program, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "tools")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, machine).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	if !SameProgram(program, strings.ToUpper(program)) {
		t.Error("a case spelling is another program")
	}
	if SameProgram(program, filepath.Join(link, "openabstractions.exe")) {
		t.Error("a program through a junction is the program it reaches")
	}
	for _, bad := range []string{"", "openabstractions.exe", program + "\x00"} {
		if SameProgram(program, bad) || SameProgram(bad, bad) {
			t.Errorf("%q matches", bad)
		}
	}
	if short, aliased := shortPath(t, program); aliased && !SameProgram(program, short) {
		t.Errorf("the 8.3 alias %s is another program", short)
	}
}

// shortPath is path's 8.3 spelling and whether it differs from path.
func shortPath(t *testing.T, path string) (string, bool) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n >= uint32(len(buf)) {
		t.Fatalf("GetShortPathName(%q): n=%d err=%v", path, n, err)
	}
	short := filepath.Clean(windows.UTF16ToString(buf[:n]))
	return short, !strings.EqualFold(short, path)
}
