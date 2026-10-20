// opencode.go dispatches `openabstractions opencode open`, the sandboxed
// session (move step 7, after 0.4.0), and holds the plugin identity checks it
// uses. The adopter's own adopters/opencode/configure.js writes OpenCode's
// config (VISION 2026-10-06, move step 6).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const opencodeUsage = `Usage: openabstractions opencode <command>

Commands:
  open        open the installed native CLI in its Windows sandbox with an
              approved project folder (open --help lists launch options)

The adopter's own configure.js, shipped with its plugin, writes OpenCode's
config and lists the servable chat models (node configure.js --help).
`

// openCodeNPM is the adopter's package name, which the plugin entries and
// provider blocks the session reads must name.
const openCodeNPM = "@openabstractions/opencode"

var openCodePackagePattern = regexp.MustCompile(`^@openabstractions/opencode(?:@[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)?$`)

func opencodeCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, opencodeUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "opencode: a command is required", "openabstractions opencode --help")
	}
	if args[0] == "open" {
		return openCodeSessionCommand(args[1:], output, diagnostics)
	}
	return commandMistake(diagnostics, fmt.Sprintf("opencode: no command called %q", args[0]), "openabstractions opencode --help")
}

func openCodePluginEntry(raw json.RawMessage) (string, map[string]json.RawMessage, error) {
	var name string
	if json.Unmarshal(raw, &name) == nil && name != "" {
		return name, nil, nil
	}
	var tuple []json.RawMessage
	if err := json.Unmarshal(raw, &tuple); err != nil || len(tuple) != 2 {
		return "", nil, fmt.Errorf("expected a plugin string or [plugin, options] tuple")
	}
	if err := json.Unmarshal(tuple[0], &name); err != nil || name == "" {
		return "", nil, fmt.Errorf("tuple plugin name must be a nonempty string")
	}
	options := map[string]json.RawMessage{}
	if err := json.Unmarshal(tuple[1], &options); err != nil || options == nil {
		return "", nil, fmt.Errorf("tuple plugin options must be an object")
	}
	return name, options, nil
}

// openCodeCompatibleProvider reports whether a provider block's npm names
// the adopter: the package, or a local entry file whose package.json names it.
func openCodeCompatibleProvider(name string) bool {
	if openCodePackageSpecifier(name) {
		return true
	}
	var entry string
	if strings.HasPrefix(name, "file://") {
		parsed, err := url.Parse(name)
		if err != nil || parsed.Scheme != "file" || parsed.Host != "" {
			return false
		}
		entry = filepath.FromSlash(parsed.Path)
		if len(entry) >= 3 && entry[0] == '\\' && entry[2] == ':' {
			entry = entry[1:]
		}
	} else if filepath.IsAbs(name) {
		entry = name
	} else {
		return false
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(entry), "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(data, &pkg) == nil && pkg.Name == openCodeNPM
}

func openCodePackageSpecifier(value string) bool {
	return openCodePackagePattern.MatchString(value)
}

func openCodeFileURL(path string) string {
	path = filepath.ToSlash(path)
	if len(path) >= 2 && path[1] == ':' {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// openCodeRegisteredAdapter reports whether an opencode.json plugin entry is
// the adopter: a version-pinned package, or a local directory or file whose
// package.json names it and exports export ("./server" for the server plugin).
func openCodeRegisteredAdapter(value, export string) bool {
	if openCodePackageSpecifier(value) {
		return true
	}
	if strings.HasPrefix(value, "file://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "file" || parsed.Host != "" {
			return false
		}
		value = filepath.FromSlash(parsed.Path)
		if len(value) >= 3 && value[0] == '\\' && value[2] == ':' {
			value = value[1:]
		}
	}
	if strings.HasPrefix(filepath.ToSlash(value), "//") || strings.HasPrefix(filepath.VolumeName(value), `\\`) || !filepath.IsAbs(value) {
		return false
	}
	if info, err := os.Stat(value); err == nil && !info.IsDir() {
		value = filepath.Dir(value)
	}
	data, err := os.ReadFile(filepath.Join(value, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Name    string                     `json:"name"`
		Exports map[string]json.RawMessage `json:"exports"`
	}
	return json.Unmarshal(data, &pkg) == nil && pkg.Name == openCodeNPM && len(pkg.Exports[export]) > 0
}
