package nativecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// This is the registered product family, independent of its version, install
// path, account and architecture. It conveys no OA application authority.
const claudeProductFamily = "Claude_pzs8sxrjxfjjc"

// Claude Code keeps each CLI version in its package data folder as
// <Claude-Data>\<family>\claude-code\<version>\<build>\claude.exe. It updates
// itself silently and removes earlier versions. A saved CLI path is a locator
// within that folder; the current image is authenticated afresh on every use.
var (
	claudeCLIProgram    = regexp.MustCompile(`(?i)^claude\.exe$`)
	claudeCLIDataLayout = regexp.MustCompile(`(?i)^Claude-Data[/\\]` + regexp.QuoteMeta(claudeProductFamily) + `[/\\]claude-code$`)
	claudeCLIVersionDir = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}$`)
	claudeCLIBuildDir   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

const claudeCLIDirectoryLimit = 64

// ClaudeCLIInstallRoot returns the claude-code folder of a CLI path in Claude's
// own package data layout. Any other path has no install root.
func ClaudeCLIInstallRoot(program string) (string, bool) {
	if !filepath.IsAbs(program) || !claudeCLIProgram.MatchString(filepath.Base(program)) {
		return "", false
	}
	build := filepath.Dir(program)
	version := filepath.Dir(build)
	root := filepath.Dir(version)
	family := filepath.Dir(root)
	data := filepath.Dir(family)
	if !claudeCLIBuildDir.MatchString(filepath.Base(build)) || !claudeCLIVersionDir.MatchString(filepath.Base(version)) ||
		!claudeCLIDataLayout.MatchString(filepath.Join(filepath.Base(data), filepath.Base(family), filepath.Base(root))) {
		return "", false
	}
	return root, true
}

// SameClaudeCLIInstall reports whether two saved CLI locators name the same
// Claude Code installation: the same path, or two versions in one claude-code
// folder. It is a locator comparison and grants no execution authority.
func SameClaudeCLIInstall(a, b string) bool {
	return sameCLIInstall(ClaudeCLIInstallRoot, a, b, sameCLIInstallRoot)
}

// FollowClaudeCLI maps a saved CLI locator to the newest installed version in
// the same claude-code folder. A path outside Claude's package data layout is
// returned unchanged. The result is a location only: the caller authenticates
// the image exactly as it authenticated the saved one.
func FollowClaudeCLI(ctx context.Context, program string) (string, error) {
	root, ok := ClaudeCLIInstallRoot(program)
	if !ok {
		return program, nil
	}
	versions, err := claudeCLIDirectories(root)
	if err != nil {
		return "", fmt.Errorf("Claude Code versions unavailable: %w", err)
	}
	type version struct {
		name  string
		parts [3]int
	}
	var ordered []version
	for _, name := range versions {
		if !claudeCLIVersionDir.MatchString(name) {
			continue
		}
		var v version
		v.name = name
		for i, part := range strings.Split(name, ".") {
			v.parts[i], _ = strconv.Atoi(part)
		}
		ordered = append(ordered, v)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i].parts, ordered[j].parts
		for k := range a {
			if a[k] != b[k] {
				return a[k] > b[k]
			}
		}
		return ordered[i].name > ordered[j].name
	})
	for _, v := range ordered {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		directory := filepath.Join(root, v.name)
		builds, err := claudeCLIDirectories(directory)
		if err != nil {
			return "", fmt.Errorf("Claude Code version %s unavailable: %w", v.name, err)
		}
		var found []string
		for _, build := range builds {
			if !claudeCLIBuildDir.MatchString(build) {
				continue
			}
			candidate := filepath.Join(directory, build, "claude.exe")
			info, err := os.Lstat(candidate)
			if err == nil && info.Mode().IsRegular() {
				found = append(found, candidate)
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
		}
		switch len(found) {
		case 0:
			// A version still being installed has no CLI yet.
			continue
		case 1:
			return found[0], nil
		default:
			return "", fmt.Errorf("Claude Code version %s has several CLI builds", v.name)
		}
	}
	return "", fmt.Errorf("no Claude Code CLI is installed in its package folder: %w", fs.ErrNotExist)
}

// ClaudeCLICurrent reports whether a saved CLI locator names an existing file
// that is also the newest version Claude Code installed beside it. A locator
// pinned to a version Claude Code kept after updating is not current.
func ClaudeCLICurrent(ctx context.Context, program string) bool {
	info, err := os.Lstat(program)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	current, err := FollowClaudeCLI(ctx, program)
	if err != nil {
		return false
	}
	if current == program {
		return true
	}
	// A hard link in an older version still names the older CLI location.
	if same, err := fileid.SameFile(filepath.Dir(current), filepath.Dir(program)); err != nil || !same {
		return false
	}
	same, err := fileid.SameFile(current, program)
	return err == nil && same
}

// claudeCLIDirectories lists the plain subdirectories of one folder; links,
// junctions and files are skipped. The count is bounded.
func claudeCLIDirectories(path string) ([]string, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(claudeCLIDirectoryLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > claudeCLIDirectoryLimit {
		return nil, errors.New("too many entries")
	}
	var names []string
	for _, entry := range entries {
		if entry.Type() == fs.ModeDir {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
