package personfolders

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("PERSONFOLDERS_TEST_CHILD") == "report" {
		os.Exit(Main(reportRunner{}))
	}
	os.Exit(Main(m))
}

// reportRunner stands in for a helper child's tests: it prints what Main
// left in its environment.
type reportRunner struct{}

func (reportRunner) Run() int {
	os.Stdout.WriteString("home=" + os.Getenv("HOME") + os.Getenv("USERPROFILE") + " marker=" + os.Getenv(marker) + "\n")
	return 0
}

func inside(path, folder string) bool {
	rel, err := filepath.Rel(folder, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Every variable lies in the stand-in, every folder a real home has exists,
// and the toolchains' folders stay outside it.
func TestTheTestsRunInsideAStandIn(t *testing.T) {
	person := os.Getenv(marker)
	if person == "" || person != made {
		t.Fatalf("marker %q, made %q", person, made)
	}
	for name, want := range under(person) {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}
	for _, folder := range folders {
		if info, err := os.Stat(filepath.Join(person, folder)); err != nil || !info.IsDir() {
			t.Errorf("%s: %v", folder, err)
		}
	}
	for _, lookup := range []func() (string, error){os.UserCacheDir, os.UserConfigDir, os.UserHomeDir} {
		if dir, err := lookup(); err != nil || !inside(dir, person) {
			t.Errorf("%q, %v lies outside the stand-in %q", dir, err, person)
		}
	}
	for _, name := range []string{"GOPATH", "GOCACHE", "GOENV"} {
		if value := os.Getenv(name); value == "" || inside(value, person) {
			t.Errorf("%s=%q is not the person's", name, value)
		}
	}
}

// Under gives one test a person of its own: the folders exist and every
// lookup lies inside it.
func TestUnderMovesOneTestsFolders(t *testing.T) {
	person := t.TempDir()
	if err := Under(person, t.Setenv); err != nil {
		t.Fatal(err)
	}
	for name, want := range under(person) {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}
	for _, folder := range folders {
		if info, err := os.Stat(filepath.Join(person, folder)); err != nil || !info.IsDir() {
			t.Errorf("%s: %v", folder, err)
		}
	}
	for _, lookup := range []func() (string, error){os.UserCacheDir, os.UserConfigDir, os.UserHomeDir} {
		if dir, err := lookup(); err != nil || !inside(dir, person) {
			t.Errorf("%q, %v lies outside the test's person %q", dir, err, person)
		}
	}
}

// A second Enter in a process that already stands in keeps the stand-in.
func TestAnInheritedStandInIsKept(t *testing.T) {
	before := os.Getenv("HOME")
	person, err := Enter()
	if err != nil || person != os.Getenv(marker) || os.Getenv("HOME") != before {
		t.Fatalf("Enter = %q, %v; HOME %q, was %q", person, err, os.Getenv("HOME"), before)
	}
}

// A helper child started with no home in its environment keeps the
// environment its parent chose; one that inherits the stand-in keeps it.
func TestAHelperChildKeepsItsEnvironment(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reduced := []string{"PERSONFOLDERS_TEST_CHILD=report", "TEMP=" + os.TempDir(), "TMP=" + os.TempDir(), "TMPDIR=" + os.TempDir()}
	if runtime.GOOS == "windows" {
		reduced = append(reduced, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	for name, c := range map[string]struct {
		env  []string
		want string
	}{
		"reduced":   {reduced, "home= marker="},
		"inherited": {append(os.Environ(), "PERSONFOLDERS_TEST_CHILD=report"), "home=" + os.Getenv("HOME") + os.Getenv("USERPROFILE") + " marker=" + os.Getenv(marker)},
	} {
		cmd := exec.Command(exe)
		cmd.Env = c.env
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != c.want {
			t.Errorf("%s child: %q, %v; want %q", name, out, err, c.want)
		}
	}
}
