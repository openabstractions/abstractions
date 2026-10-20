package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
)

// An application manifest file names the folders that exist only on the
// installing machine by placeholder (VISION 2026-10-06): {app} is the folder
// the installer put the manifest in, {local_app_data} the account's local
// application data and {pictures} its Pictures folder. The Panel resolves
// them before the Facade's own check and refuses any path that resolves
// outside them, asking the file system by identity (identity.Within).

// manifestFolders are the folders a manifest's paths may name.
type manifestFolders map[string]string

// manifestPlaceholder is one value that begins with a placeholder: the
// placeholder's name and the rest below it.
var manifestPlaceholder = regexp.MustCompile(`^\{([a-z_]+)\}(?:[\\/](.*))?$`)

// resolve returns the value the descriptor carries. A value beginning with a
// placeholder is the named folder joined with the rest, which must stay
// strictly inside that folder; a placeholder anywhere else, or an unknown
// one, is refused. An absolute value without one must lie inside one of the
// folders. The program must lie inside {app}. Any other value is literal.
func (f manifestFolders) resolve(value string, program bool) (string, error) {
	path, root := value, ""
	if strings.Contains(value, "{") {
		m := manifestPlaceholder.FindStringSubmatch(value)
		if m == nil {
			return "", errors.New("a placeholder must begin the value and name a folder: {app}, {local_app_data} or {pictures}")
		}
		folder, known := f[m[1]]
		if !known {
			return "", fmt.Errorf("unknown placeholder {%s}", m[1])
		}
		if m[2] == "" {
			return "", fmt.Errorf("{%s} must be followed by a path inside it", m[1])
		}
		// A manifest is portable text: the rest separates with \ or / on
		// every OS and joins with the native separator.
		rest := filepath.FromSlash(strings.ReplaceAll(m[2], `\`, "/"))
		path, root = filepath.Join(folder, rest), folder
	} else if !filepath.IsAbs(value) {
		if program {
			return "", errors.New("the program must be {app}/... or an absolute path inside the application's folder")
		}
		return value, nil
	}
	if err := identity.LocalPath(path); err != nil {
		return "", err
	}
	roots := []string{root}
	switch {
	case program:
		roots = []string{f["app"]}
	case root == "":
		roots = []string{f["app"], f["local_app_data"], f["pictures"]}
	}
	for _, folder := range roots {
		if folder == "" {
			continue
		}
		if inside, err := identity.Within(path, folder); err == nil && inside {
			if program {
				if err := refuseSharedInterpreter(path); err != nil {
					return "", err
				}
			}
			return path, nil
		}
	}
	if program {
		return "", fmt.Errorf("%s is outside the application's folder", path)
	}
	return "", fmt.Errorf("%s is outside the application's folders", path)
}

// An application's own interpreter is a program image inside its folder, a
// copy and never the shared interpreter (VISION 2026-10-07). Python reads the
// pyvenv.cfg beside its program, or else the one in the folder above, and
// runs from the `home` folder it names; a program that is the same file as a
// file in that home, such as a hard link to its python.exe, is the shared
// interpreter under another name, and registering it would make one grant
// cover every program that interpreter runs.

// venvConfigLimit bounds the pyvenv.cfg read; Python's own files are a few
// hundred bytes.
const venvConfigLimit = 64 << 10

// refuseSharedInterpreter refuses program when it is the same file, by file
// identity, as a file in the home folder of the pyvenv.cfg Python reads for
// it. A program with no pyvenv.cfg beside or above it is not a venv's and
// passes; a home that is not on a local drive (identity.LocalPath) is refused
// before anything in it is opened.
func refuseSharedInterpreter(program string) error {
	home, config, err := venvHome(program)
	if err != nil || home == "" {
		return err
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("%s names a home folder that is not an absolute path", config)
	}
	if err := identity.LocalPath(home); err != nil {
		return fmt.Errorf("%s names a home folder that is not on a local drive: %w", config, err)
	}
	key, err := identity.FileKey(program)
	if err != nil {
		return fmt.Errorf("the program %s could not be read: %w", program, err)
	}
	entries, err := os.ReadDir(home)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // Python could not run from it either
	}
	if err != nil {
		return fmt.Errorf("the home folder %s that %s names could not be read: %w", home, config, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		shared := filepath.Join(home, entry.Name())
		if other, err := identity.FileKey(shared); err == nil && other == key {
			return fmt.Errorf("the program %s is the same file as the shared interpreter %s; an application's interpreter is a copy inside its own folder", program, shared)
		}
	}
	return nil
}

// venvHome returns the home value of the pyvenv.cfg Python reads for program
// and that file's path, or "" when there is none.
func venvHome(program string) (home, config string, err error) {
	for _, folder := range []string{filepath.Dir(program), filepath.Dir(filepath.Dir(program))} {
		config = filepath.Join(folder, "pyvenv.cfg")
		info, err := os.Lstat(config)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil && !info.Mode().IsRegular() {
			// A link would let the file Python reads differ from the one
			// checked here, or name a home in another folder's venv.
			return "", config, fmt.Errorf("%s is a link or not a file", config)
		}
		file, err := os.Open(config)
		if err != nil {
			return "", config, fmt.Errorf("%s could not be read: %w", config, err)
		}
		data, err := io.ReadAll(io.LimitReader(file, venvConfigLimit+1))
		file.Close()
		if err != nil || len(data) > venvConfigLimit {
			return "", config, fmt.Errorf("%s could not be read or is over 64 KiB", config)
		}
		return venvConfigHome(data), config, nil
	}
	return "", "", nil
}

// venvConfigHome is the value of the first `home = ...` line, as Python's
// getpath reads it: the key trimmed and compared without regard to ASCII
// case, the value trimmed.
func venvConfigHome(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	for _, line := range bytes.Split(data, []byte("\n")) {
		key, value, found := bytes.Cut(line, []byte("="))
		if found && homeKey(bytes.TrimSpace(key)) {
			return string(bytes.TrimSpace(value))
		}
	}
	return ""
}

// homeKey reports whether a pyvenv.cfg key is "home" in any ASCII case; the
// key is a configuration word, no path.
func homeKey(key []byte) bool {
	const want = "home"
	if len(key) != len(want) {
		return false
	}
	for i := range key {
		if key[i]|0x20 != want[i] {
			return false
		}
	}
	return true
}
