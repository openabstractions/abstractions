package product

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// The per-machine folder vouches by path; every other named folder vouches
// only for a same-signer file; any other folder, a subfolder and a relative
// program vouch for nothing.
func TestShippedTrustsProtectedFoldersByPathAndOthersBySigner(t *testing.T) {
	machine, user, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	protected, signed := []string{machine}, []string{user}
	var signedFiles []string
	sameSigner := func(program string) bool { return slices.Contains(signedFiles, program) }
	for program, want := range map[string]bool{
		filepath.Join(machine, "openabstractions.exe"):        true,
		filepath.Join(machine, "sub", "openabstractions.exe"): false,
		filepath.Join(user, "openabstractions-mcp.exe"):       false,
		filepath.Join(elsewhere, "openabstractions.exe"):      false,
		"openabstractions.exe":                                false,
		"":                                                    false,
	} {
		if got := Shipped(program, protected, signed, sameSigner); got != want {
			t.Errorf("Shipped(%q) = %v, want %v", program, got, want)
		}
	}
	signedFiles = []string{filepath.Join(user, "openabstractions-mcp.exe"), filepath.Join(elsewhere, "openabstractions.exe")}
	if !Shipped(signedFiles[0], protected, signed, sameSigner) {
		t.Error("a same-signer file in a signed folder has no standing")
	}
	if Shipped(signedFiles[1], protected, signed, sameSigner) {
		t.Error("a same-signer file outside every named folder has standing")
	}
	if Shipped(signedFiles[0], protected, signed, nil) {
		t.Error("no signer check vouches")
	}
	if !Protected(machine, protected) || Protected(user, protected) || Protected("", protected) {
		t.Error("Protected misreads its folders")
	}
}

// The signer cache keys on the file's identity as well as its path: a file
// replaced at the same path is verified again.
func TestSameSignerCacheMissesAReplacedFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("signatures are read on Windows only")
	}
	program := filepath.Join(t.TempDir(), "tool.exe")
	if err := os.WriteFile(program, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	reads := 0
	savedSigner, savedExe := fileSigner, executable
	t.Cleanup(func() {
		fileSigner, executable = savedSigner, savedExe
		signerMu.Lock()
		runningSigner = nil
		clear(fileSigners)
		signerMu.Unlock()
	})
	executable = func() (string, error) { return `C:\running.exe`, nil }
	fileSigner = func(path string) string {
		if path == program {
			reads++
			if reads == 1 {
				return "oa-signer"
			}
			return ""
		}
		return "oa-signer"
	}
	signerMu.Lock()
	runningSigner = nil
	clear(fileSigners)
	signerMu.Unlock()
	if !SameSigner(program) || !SameSigner(program) || reads != 1 {
		t.Fatalf("the signer answer was not kept: reads %d", reads)
	}
	if err := os.Rename(program, program+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if SameSigner(program) {
		t.Error("the replaced file keeps the cached signer answer")
	}
	// An unsigned running program vouches for nothing.
	executable = func() (string, error) { return "", os.ErrNotExist }
	signerMu.Lock()
	runningSigner = nil
	clear(fileSigners)
	signerMu.Unlock()
	if SameSigner(program + ".old") {
		t.Error("an unsigned running program vouches")
	}
}

// SameSignerAs compares with the explicit reference file, verifies both now
// and lets no unsigned reference vouch.
func TestSameSignerAsUsesTheReferenceFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("signers are verified on Windows only")
	}
	saved := fileSigner
	t.Cleanup(func() { fileSigner = saved })
	signers := map[string]string{`C:\moved.exe`: "oa-signer", `C:\published.exe`: "oa-signer", `C:\other.exe`: "other-signer"}
	reads := 0
	fileSigner = func(path string) string { reads++; return signers[path] }
	if !SameSignerAs(`C:\published.exe`, `C:\moved.exe`) || !SameSignerAs(`C:\published.exe`, `C:\moved.exe`) || reads != 4 {
		t.Fatal("the same signer was refused or an answer was cached", reads)
	}
	if SameSignerAs(`C:\other.exe`, `C:\moved.exe`) || SameSignerAs(`C:\unsigned.exe`, `C:\moved.exe`) {
		t.Fatal("another or no signer was accepted")
	}
	if SameSignerAs(`C:\unsigned.exe`, `C:\unsigned-reference.exe`) || SameSignerAs(`C:\published.exe`, "") {
		t.Fatal("an unsigned or absent reference vouched")
	}
	if !Signed(`C:\moved.exe`) || Signed(`C:\unsigned.exe`) || Signed("") {
		t.Fatal("Signed misread a file")
	}
}
