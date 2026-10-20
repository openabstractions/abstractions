//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

// junction makes link a directory junction to target and removes the link
// alone when the test ends, before any temporary directory holding it is
// removed: link removal never recurses through a junction.
func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove junction %s: %v", link, err)
		}
	})
}

// stateAliases names the existing installed state folder by other spellings
// that reach the same folder: a junction, its 8.3 short name and the \\?\
// prefix. The short name case is left out when the volume makes no short
// names.
func stateAliases(t *testing.T, installed string) map[string]string {
	t.Helper()
	aliases := map[string]string{`\\?\ prefix`: `\\?\` + installed}
	link := filepath.Join(t.TempDir(), "state-junction")
	junction(t, link, installed)
	aliases["junction"] = link
	long, err := syscall.UTF16PtrFromString(installed)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, syscall.MAX_LONG_PATH)
	n, err := syscall.GetShortPathName(long, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Logf("no 8.3 short name for %s: %v", installed, err)
		return aliases
	}
	if short := syscall.UTF16ToString(buf[:n]); !strings.EqualFold(short, installed) {
		aliases["8.3 short name"] = short
	} else {
		t.Logf("the volume makes no 8.3 short names; that case is not checked")
	}
	return aliases
}

// shareAliases spells a local folder through the loopback administrative
// share: \\localhost\C$\…, \\127.0.0.1\C$\… and \\?\UNC\localhost\C$\….
func shareAliases(t *testing.T, folder string) map[string]string {
	t.Helper()
	volume := filepath.VolumeName(folder)
	if len(volume) != 2 || volume[1] != ':' {
		t.Fatalf("%s is not on a drive letter", folder)
	}
	rest := volume[:1] + `$` + folder[len(volume):]
	return map[string]string{
		`\\localhost share`:       `\\localhost\` + rest,
		`\\127.0.0.1 share`:       `\\127.0.0.1\` + rest,
		`\\?\UNC\localhost share`: `\\?\UNC\localhost\` + rest,
	}
}

// F1: --operator-program opens nothing before identity.LocalPath admits
// both the program and the state directory. A state directory in the NT
// \??\UNC\ form, a \\?\ drive path on a mapped network drive (stood in for
// localPath, so no drive is mapped) and a program on a share are refused
// with no stat and no final-path read of anything.
func TestOperatorProgramOpensNothingOffALocalDrive(t *testing.T) {
	savedStat, savedFinal, savedLocal := statPath, finalPath, localPath
	t.Cleanup(func() { statPath, finalPath, localPath = savedStat, savedFinal, savedLocal })
	var opened []string
	statPath = func(p string) (os.FileInfo, error) { opened = append(opened, p); return savedStat(p) }
	finalPath = func(p string) (string, error) { opened = append(opened, p); return savedFinal(p) }
	localPath = func(p string) error {
		if strings.HasPrefix(strings.ToUpper(p), `\\?\X:`) || strings.HasPrefix(strings.ToUpper(p), `X:`) {
			return &identity.NotLocalPathError{Path: p, Reason: "a network drive"}
		}
		return savedLocal(p)
	}
	panel := operatorProgramFixture(t)
	state := t.TempDir()
	for _, tc := range []struct{ name, state, program string }{
		{"NT UNC state directory", `\??\UNC\localhost\C$\x`, panel},
		{"mapped drive state directory", `\\?\X:\x`, panel},
		{"share program", state, `\\host\share\x.exe`},
	} {
		opened = nil
		_, err := parseRuntime([]string{"--isolated", isolatedName(), "--state-dir", tc.state, "--operator-program", tc.program}, io.Discard)
		if !errors.Is(err, identity.ErrNotLocalPath) {
			t.Errorf("%s: %v; want identity's refusal", tc.name, err)
		}
		if len(opened) != 0 {
			t.Errorf("%s: opened %q before the refusal", tc.name, opened)
		}
	}
}

// The identity comparison the check repeats after creating the folder never
// opens a loopback-share spelling of an existing folder: identity refuses it
// as off a local drive, and distinctState refuses the state directory.
func TestSameFolderRefusesALoopbackShareWithoutOpeningIt(t *testing.T) {
	folder := t.TempDir()
	for name, alias := range shareAliases(t, folder) {
		if same, err := sameFolder(folder, alias); !errors.Is(err, identity.ErrNotLocalPath) || same {
			t.Errorf("%s: sameFolder(%s, %s) = %v, %v; want identity's refusal", name, folder, alias, same, err)
		}
		if err := distinctState(alias, folder); err == nil {
			t.Errorf("%s: distinctState accepted %s", name, alias)
		}
	}
}

// A local folder whose final path is a share, as a symbolic link in its
// ancestry can make it, is refused once resolved: the resolved name goes
// through identity.LocalPath too. The link is stood in by finalPath, so the
// test makes none: the stand-in folder resolves to the share spelling of the
// installed runtime's parent folder. A refusal must come before anything is
// created.
func TestOperatorProgramRefusesAStateFolderResolvingToAShare(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	installed, err := runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	below, err := filepath.Rel(local, installed)
	if err != nil {
		t.Fatal(err)
	}
	mapped := t.TempDir()
	share := shareAliases(t, local)[`\\localhost share`]
	real := finalPath
	t.Cleanup(func() { finalPath = real })
	finalPath = func(path string) (string, error) {
		if strings.EqualFold(filepath.Clean(path), filepath.Clean(mapped)) {
			return share, nil
		}
		return real(path)
	}
	state := filepath.Join(mapped, below)
	panel := operatorProgramFixture(t)
	if _, err := parseRuntime([]string{"--isolated", isolatedName(), "--state-dir", state, "--operator-program", panel}, io.Discard); !errors.Is(err, identity.ErrNotLocalPath) {
		t.Fatalf("a state directory resolving to a share: %v; want identity's refusal", err)
	}
	for _, path := range []string{installed, state} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("a refused --operator-program left %s behind: %v", path, err)
		}
	}
	if err := localPath(local); err != nil {
		t.Fatalf("the test's own folder %s is not local: %v", local, err)
	}
}

// absentStateAliases builds a state directory that becomes the installed
// runtime's state folder once created: the installed folder's parent is a
// junction into the isolated state's parent. The installed folder does not
// exist when parsing starts.
func absentStateAliases(t *testing.T, installed string) map[string]string {
	t.Helper()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(installed)), 0o700); err != nil {
		t.Fatal(err)
	}
	junction(t, filepath.Dir(installed), elsewhere)
	dangling := filepath.Join(t.TempDir(), "dangling-junction")
	junction(t, dangling, installed)
	return map[string]string{
		"parent junction into the isolated state": filepath.Join(elsewhere, filepath.Base(installed)),
		"dangling junction":                       dangling,
	}
}
