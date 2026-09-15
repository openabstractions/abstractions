package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// testPython is the interpreter every Python-backed test runs. TestMain sets it
// before any test starts, or stops the run with one message.
var testPython string

const minimumPython = 10 // generated Python requires 3.10 or newer

// pythonSkipped is the reason Python-backed tests skip when the run opted out
// with -short or IDL_GEN_TESTS_WITHOUT_PYTHON=1; empty when they run.
var pythonSkipped string

const withoutPythonEnv = "IDL_GEN_TESTS_WITHOUT_PYTHON"

func TestMain(m *testing.M) {
	flag.Parse()
	if reason := pythonOptOut(testing.Short(), os.Getenv(withoutPythonEnv)); reason != "" {
		pythonSkipped = reason
		fmt.Fprintln(os.Stderr, "idl/gen tests: Python-backed tests skip ("+reason+"); Go, C++, Rust and JavaScript backend tests run")
		os.Exit(m.Run())
	}
	path, problem := resolveTestPython(os.Getenv("PYTHON"), exec.LookPath, pythonVersion)
	if problem != "" {
		fmt.Fprintln(os.Stderr, "idl/gen tests need a real Python interpreter: "+problem+
			"; run with -short or "+withoutPythonEnv+"=1 to skip the Python-backed tests")
		os.Exit(2)
	}
	testPython = path
	os.Exit(m.Run())
}

// pythonOptOut names why Python-backed tests skip, or returns "" when they run.
func pythonOptOut(short bool, env string) string {
	switch {
	case env == "1":
		return withoutPythonEnv + "=1"
	case short:
		return "-short"
	}
	return ""
}

func TestPythonOptOut(t *testing.T) {
	for _, c := range []struct {
		short bool
		env   string
		want  string
	}{{false, "", ""}, {true, "", "-short"}, {false, "1", withoutPythonEnv + "=1"}, {true, "1", withoutPythonEnv + "=1"}, {false, "0", ""}} {
		if got := pythonOptOut(c.short, c.env); got != c.want {
			t.Errorf("short=%v env=%q: %q, want %q", c.short, c.env, got, c.want)
		}
	}
}

// isStoreAlias reports the Windows App Execution Alias under
// %LOCALAPPDATA%\Microsoft\WindowsApps, which starts the Store instead of Python.
// Backslashes are replaced directly: filepath.ToSlash converts only the host's
// separator, so on Linux and macOS it left the Windows path unrecognised.
func isStoreAlias(path string) bool {
	return strings.Contains(strings.ToLower(strings.ReplaceAll(path, `\`, "/")), "/microsoft/windowsapps/")
}

func pythonVersion(path string) (int, int, error) {
	out, err := exec.Command(path, "-c", "import sys; print('%d %d' % sys.version_info[:2])").CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected version output %q", out)
	}
	major, err1 := strconv.Atoi(fields[0])
	minor, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("unexpected version output %q", out)
	}
	return major, minor, nil
}

// resolveTestPython chooses PYTHON when set, otherwise the first python3 or
// python on PATH that is not the Store alias. It returns a problem instead of a
// path when none is usable.
func resolveTestPython(explicit string, look func(string) (string, error), version func(string) (int, int, error)) (string, string) {
	candidate, source := explicit, "PYTHON="+explicit
	if explicit == "" {
		var aliases []string
		for _, name := range []string{"python3", "python"} {
			found, err := look(name)
			if err != nil || found == "" {
				continue
			}
			if isStoreAlias(found) {
				aliases = append(aliases, found)
				continue
			}
			candidate, source = found, name+" on PATH ("+found+")"
			break
		}
		if candidate == "" {
			if len(aliases) > 0 {
				return "", "PATH offers only the Windows Store alias (" + strings.Join(aliases, ", ") + "); set PYTHON to a real interpreter"
			}
			return "", "PYTHON is unset and no python3 or python is on PATH; set PYTHON to a real interpreter"
		}
	} else if isStoreAlias(explicit) {
		return "", "PYTHON=" + explicit + " is the Windows Store alias; set PYTHON to a real interpreter"
	}
	major, minor, err := version(candidate)
	if err != nil {
		return "", source + " does not run: " + err.Error()
	}
	if major != 3 || minor < minimumPython {
		return "", fmt.Sprintf("%s is Python %d.%d; 3.%d or newer is required", source, major, minor, minimumPython)
	}
	return candidate, ""
}

func TestResolveTestPython(t *testing.T) {
	alias := `C:\Users\u\AppData\Local\Microsoft\WindowsApps\python.exe`
	real := `C:\Python312\python.exe`
	version := func(major, minor int, err error) func(string) (int, int, error) {
		return func(string) (int, int, error) { return major, minor, err }
	}
	path := func(found map[string]string) func(string) (string, error) {
		return func(name string) (string, error) {
			if p, ok := found[name]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		}
	}
	cases := []struct {
		name, explicit string
		found          map[string]string
		version        func(string) (int, int, error)
		want, problem  string
	}{
		{"explicit", real, nil, version(3, 12, nil), real, ""},
		{"explicit alias", alias, nil, version(3, 12, nil), "", "Windows Store alias"},
		{"path skips alias", "", map[string]string{"python3": alias, "python": real}, version(3, 12, nil), real, ""},
		{"only aliases", "", map[string]string{"python3": alias, "python": alias}, version(3, 12, nil), "", "only the Windows Store alias"},
		{"nothing", "", nil, version(3, 12, nil), "", "no python3 or python"},
		{"broken", real, nil, version(0, 0, fmt.Errorf("exit status 9009")), "", "does not run"},
		{"too old", real, nil, version(3, 8, nil), "", "3.10 or newer"},
	}
	for _, c := range cases {
		got, problem := resolveTestPython(c.explicit, path(c.found), c.version)
		if got != c.want || (c.problem == "") != (problem == "") || !strings.Contains(problem, c.problem) {
			t.Errorf("%s: got %q, problem %q", c.name, got, problem)
		}
	}
}
