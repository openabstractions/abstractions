package product

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// Product standing is the one rule serve and the Panel apply to decide that
// a program file is one OpenAbstractions shipped (A-SEC6): such a program
// may read by a product name, show no Publisher line in a prompt and rank as
// a shipped engine. A folder only an administrator writes (InstallFolders)
// vouches for its files by path. Any other folder, including the per-user
// installation's (UserInstallFolder) and the running program's own folder
// when it is one of those, vouches only for a file Windows verifies with the
// running program's own signer (SameSigner). A program any user-level
// process could drop into a folder never gains standing from the folder.

// InstallFolders are the folders whose programs have product standing by
// path: the per-machine installation on Windows, which only an administrator
// writes. Every other platform has none.
func InstallFolders() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	if dir := os.Getenv("ProgramFiles"); dir != "" {
		return []string{filepath.Join(dir, "OpenAbstractions", "tools")}
	}
	return nil
}

// UserInstallFolder is the per-user installation's folder on Windows, which
// any program the person runs may write.
func UserInstallFolder() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	if dir, err := layout.LocalAppData(); err == nil {
		return filepath.Join(dir, "Programs", "OpenAbstractions", "tools")
	}
	return ""
}

// SameFolder reports whether two absolute folder paths name the same folder.
// It compares names with 8.3 aliases expanded and follows no junction or
// other link: a link to an administrator's folder is not that folder, since
// whoever made the link may point it elsewhere.
func SameFolder(a, b string) bool {
	return sameName(a, b)
}

// SameProgram reports whether two absolute program paths name one of
// OpenAbstractions' own programs alike, as SameFolder compares folders: 8.3
// aliases expand, and no junction or other link is followed. A process
// started through a junction keeps that spelling, and its maker may point
// the junction at OpenAbstractions' folder afterwards, so file identity
// cannot vouch for it. Empty, relative and invalid paths never match.
func SameProgram(a, b string) bool {
	return sameName(a, b)
}

func sameName(a, b string) bool {
	if a == "" || b == "" || !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	a, b = identity.NormalizeSubjectProgram(filepath.Clean(a)), identity.NormalizeSubjectProgram(filepath.Clean(b))
	return identity.ValidSubjectProgram(a) && identity.ValidSubjectProgram(b) && identity.SamePathName(a, b)
}

// InFolder reports whether an absolute program path sits directly in folder,
// as SameFolder compares folders.
func InFolder(program, folder string) bool {
	if program == "" || !filepath.IsAbs(program) {
		return false
	}
	return SameFolder(filepath.Dir(filepath.Clean(program)), folder)
}

// Shipped reports whether program has product standing: it sits directly in
// one of protected, folders only an administrator writes, or directly in one
// of signed and sameSigner verifies its file. Callers pass InstallFolders,
// their own folder and UserInstallFolder, and SameSigner, or a test's
// replacements. An empty or relative program has none.
func Shipped(program string, protected, signed []string, sameSigner func(string) bool) bool {
	if program == "" || !filepath.IsAbs(program) {
		return false
	}
	for _, folder := range protected {
		if InFolder(program, folder) {
			return true
		}
	}
	for _, folder := range signed {
		if InFolder(program, folder) {
			return sameSigner != nil && sameSigner(program)
		}
	}
	return false
}

// Protected reports whether folder is one of protected, as SameFolder
// compares folders.
func Protected(folder string, protected []string) bool {
	for _, p := range protected {
		if SameFolder(folder, p) {
			return true
		}
	}
	return false
}

// signerAge is how long a signer answer is kept for one file identity.
const signerAge = 10 * time.Minute

type signerAnswer struct {
	at  time.Time
	key string
}

var (
	signerMu      sync.Mutex
	runningSigner *signerAnswer
	fileSigners   = map[string]signerAnswer{}
	// executable is the running program's file; a test replaces it.
	executable = os.Executable
	// fileSigner is a file's verified embedded signer key, "" for none; a
	// test replaces it.
	fileSigner = verifiedSigner
)

func verifiedSigner(program string) string {
	code, _, key, err := identity.VerifyFilePublisher(program, nil)
	if err != nil || !code.Trusted {
		return ""
	}
	return key
}

// signerCacheKey keys an answer by the file's path and its file identity
// (identity.ProgramKey), so a file replaced at the same path misses the cache
// and is verified again.
func signerCacheKey(program string) string {
	return program + "\x00" + identity.ProgramKey(program)
}

// Signed reports a program file Windows verifies as trusted with an embedded
// signer, by SameSigner's verification and without its cache. A
// catalog-signed or unsigned file and every platform but Windows read false.
func Signed(program string) bool {
	return runtime.GOOS == "windows" && program != "" && fileSigner(program) != ""
}

// SameSignerAs reports a program file whose signature Windows verifies with
// the same embedded signer as reference, an explicit file such as the moved
// image of the running program. os.Executable names the original path after
// a rename, so it cannot stand for a moved image. Both files are verified
// now, by SameSigner's verification and without its cache. An unsigned or
// catalog-signed reference vouches for nothing, as does every platform but
// Windows.
func SameSignerAs(program, reference string) bool {
	if runtime.GOOS != "windows" || program == "" || reference == "" {
		return false
	}
	key := fileSigner(reference)
	return key != "" && fileSigner(program) == key
}

// SameSigner reports a program file whose signature Windows verifies with
// the same embedded signer as the running program's own file (identity's
// publisher key). An unsigned running program, a catalog-signed file, a
// missing file and every platform but Windows vouch for nothing. Each answer
// is kept for ten minutes under the file's path and identity.
func SameSigner(program string) bool {
	if runtime.GOOS != "windows" || program == "" {
		return false
	}
	now := time.Now()
	cacheKey := signerCacheKey(program)
	signerMu.Lock()
	self := runningSigner
	known, ok := fileSigners[cacheKey]
	signerMu.Unlock()
	if self == nil || now.Sub(self.at) >= signerAge {
		key := ""
		if exe, err := executable(); err == nil {
			key = fileSigner(exe)
		}
		self = &signerAnswer{at: now, key: key}
		signerMu.Lock()
		runningSigner = self
		signerMu.Unlock()
	}
	if self.key == "" {
		return false
	}
	if !ok || now.Sub(known.at) >= signerAge {
		known = signerAnswer{at: now, key: fileSigner(program)}
		signerMu.Lock()
		if len(fileSigners) >= 64 {
			clear(fileSigners)
		}
		fileSigners[cacheKey] = known
		signerMu.Unlock()
	}
	return known.key == self.key
}
