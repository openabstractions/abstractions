package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// codexToolTimeoutSeconds matches the 30-minute tool budget OpenCode gets.
// Long tools (pictures, completions, command calls with wait_ms) need it, and
// codex mcp add has no flag for it, so Codex would otherwise use its 60 s.
const codexToolTimeoutSeconds = 1800

const codexConfigLimit = 4 << 20

// codexSetupLog receives the one line a skipped timeout edit costs.
var codexSetupLog = func(line string) { checkLine(os.Stderr, line) }

// codexEntriesMatch compares two saved entries, counting OA's own tool timeout
// and no timeout as the same entry. Any other timeout is the person's choice.
func codexEntriesMatch(a, b codexEntry) bool {
	a.ToolTimeout, b.ToolTimeout = codexOwnedTimeout(a.ToolTimeout), codexOwnedTimeout(b.ToolTimeout)
	return reflect.DeepEqual(a, b)
}

func codexOwnedTimeout(value *float64) *float64 {
	if value != nil && *value == codexToolTimeoutSeconds {
		return nil
	}
	return value
}

// ensureCodexToolTimeout sets tool_timeout_sec on OA's saved Codex server.
// A file it cannot read, recognise or replace stays as it is.
func ensureCodexToolTimeout(home, name string) {
	if err := setCodexToolTimeout(filepath.Join(home, "config.toml"), name); err != nil {
		codexSetupLog("Codex setup: tool timeout left unchanged (" + err.Error() + ")")
	}
}

func setCodexToolTimeout(path, name string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > codexConfigLimit {
		return errors.New("config.toml is not a plain file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, changed, err := codexConfigWithToolTimeout(raw, name)
	if err != nil || !changed {
		return err
	}
	return cas.ChangeLimit(path, codexConfigLimit, func(current []byte) ([]byte, error) {
		// Codex may have saved the file while this edit was prepared; keep its write.
		if !bytes.Equal(current, raw) {
			return nil, errors.New("config.toml changed during the edit")
		}
		return next, nil
	})
}

type codexTOMLTable struct {
	key          []string
	array        bool
	eol          string
	lastValueEnd int
	timeout      []byte
}

// codexConfigWithToolTimeout adds one tool_timeout_sec line to the
// [mcp_servers.<name>] table and leaves every other byte as it was. It reads
// only the TOML structure it needs: table headers, strings, comments and
// bracket depth. Anything it does not recognise is an error.
func codexConfigWithToolTimeout(raw []byte, name string) ([]byte, bool, error) {
	var tables []*codexTOMLTable
	current := &codexTOMLTable{}
	pos := 0
	if bytes.HasPrefix(raw, []byte("\xef\xbb\xbf")) {
		pos = 3
	}
	var multiline byte
	depth := 0
	for pos < len(raw) {
		lineEnd := bytes.IndexByte(raw[pos:], '\n')
		next := len(raw)
		if lineEnd < 0 {
			lineEnd = len(raw)
		} else {
			lineEnd += pos
			next = lineEnd + 1
		}
		line := raw[pos:lineEnd]
		eol := "\n"
		if bytes.HasSuffix(line, []byte("\r")) {
			line, eol = line[:len(line)-1], "\r\n"
		}
		start := 0
		if multiline == 0 && depth == 0 {
			start = len(line) - len(bytes.TrimLeft(line, " \t"))
			if start < len(line) && line[start] == '[' {
				key, array, rest, err := codexTOMLHeader(line[start:])
				if err != nil || !codexTOMLBlank(rest) {
					return nil, false, errors.New("unrecognised table header")
				}
				current = &codexTOMLTable{key: key, array: array, eol: eol, lastValueEnd: next}
				tables = append(tables, current)
				pos = next
				continue
			}
			if key, _, rest, ok := codexTOMLKey(line[start:]); ok && len(key) == 1 && key[0] == "tool_timeout_sec" {
				rest = bytes.TrimLeft(rest, " \t")
				if len(rest) == 0 || rest[0] != '=' {
					return nil, false, errors.New("unrecognised key")
				}
				current.timeout = codexTOMLValue(rest[1:])
			}
		}
		content, err := codexTOMLScan(line[start:], &multiline, &depth)
		if err != nil {
			return nil, false, err
		}
		if content && multiline == 0 && depth == 0 {
			current.lastValueEnd = next
		}
		pos = next
	}
	if multiline != 0 || depth != 0 {
		return nil, false, errors.New("unterminated value")
	}
	var target *codexTOMLTable
	for _, table := range tables {
		if !table.array && len(table.key) == 2 && table.key[0] == "mcp_servers" && table.key[1] == name {
			if target != nil {
				return nil, false, errors.New("server table repeated")
			}
			target = table
		}
	}
	if target == nil {
		return nil, false, errors.New("server table not found")
	}
	if target.timeout != nil {
		if string(target.timeout) == "1800" || string(target.timeout) == "1800.0" {
			return raw, false, nil
		}
		return nil, false, errors.New("the person set a different tool timeout")
	}
	line := "tool_timeout_sec = 1800" + target.eol
	at := target.lastValueEnd
	if at == len(raw) && len(raw) > 0 && raw[len(raw)-1] != '\n' {
		line = target.eol + "tool_timeout_sec = 1800"
	}
	next := make([]byte, 0, len(raw)+len(line))
	next = append(append(append(next, raw[:at]...), line...), raw[at:]...)
	return next, true, nil
}

// codexTOMLScan follows strings, comments and bracket depth across one line.
// It reports whether the line holds anything other than blanks and comments.
func codexTOMLScan(line []byte, multiline *byte, depth *int) (bool, error) {
	content := *multiline != 0 || *depth != 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		if *multiline != 0 {
			if *multiline == '"' && c == '\\' {
				i++
				continue
			}
			if c == *multiline && bytes.HasPrefix(line[i:], bytes.Repeat([]byte{c}, 3)) {
				i += 2
				for extra := 0; extra < 2 && i+1 < len(line) && line[i+1] == c; extra++ {
					i++
				}
				*multiline = 0
			}
			continue
		}
		switch c {
		case ' ', '\t':
			continue
		case '#':
			return content, nil
		}
		content = true
		switch c {
		case '"', '\'':
			if bytes.HasPrefix(line[i:], bytes.Repeat([]byte{c}, 3)) {
				*multiline = c
				i += 2
				continue
			}
			end := codexTOMLStringEnd(line, i)
			if end < 0 {
				return false, errors.New("unterminated string")
			}
			i = end
		case '[', '{':
			*depth++
		case ']', '}':
			*depth--
			if *depth < 0 {
				return false, errors.New("unbalanced brackets")
			}
		}
	}
	return content, nil
}

// codexTOMLStringEnd returns the index of the quote closing the one-line
// string that opens at start, or -1.
func codexTOMLStringEnd(line []byte, start int) int {
	quote := line[start]
	for i := start + 1; i < len(line); i++ {
		if quote == '"' && line[i] == '\\' {
			i++
			continue
		}
		if line[i] == quote {
			return i
		}
	}
	return -1
}

// codexTOMLKey reads a dotted key of bare and quoted parts. Quoted parts with
// escapes are refused rather than decoded.
func codexTOMLKey(text []byte) ([]string, int, []byte, bool) {
	var key []string
	i := 0
	for {
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i >= len(text) {
			return nil, 0, nil, false
		}
		switch c := text[i]; {
		case c == '"' || c == '\'':
			end := codexTOMLStringEnd(text, i)
			if end < 0 || (c == '"' && bytes.IndexByte(text[i+1:end], '\\') >= 0) {
				return nil, 0, nil, false
			}
			key = append(key, string(text[i+1:end]))
			i = end + 1
		default:
			start := i
			for i < len(text) && (text[i] == '_' || text[i] == '-' || text[i] >= '0' && text[i] <= '9' || text[i] >= 'a' && text[i] <= 'z' || text[i] >= 'A' && text[i] <= 'Z') {
				i++
			}
			if i == start {
				return nil, 0, nil, false
			}
			key = append(key, string(text[start:i]))
		}
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i < len(text) && text[i] == '.' {
			i++
			continue
		}
		return key, i, text[i:], true
	}
}

func codexTOMLHeader(line []byte) ([]string, bool, []byte, error) {
	array := bytes.HasPrefix(line, []byte("[["))
	open := 1
	if array {
		open = 2
	}
	key, _, rest, ok := codexTOMLKey(line[open:])
	closing := []byte("]")
	if array {
		closing = []byte("]]")
	}
	if !ok || !bytes.HasPrefix(rest, closing) {
		return nil, false, nil, errors.New("header")
	}
	return key, array, rest[len(closing):], nil
}

func codexTOMLBlank(rest []byte) bool {
	rest = bytes.TrimLeft(rest, " \t")
	return len(rest) == 0 || rest[0] == '#'
}

// codexTOMLValue returns a one-line value without its trailing comment.
func codexTOMLValue(rest []byte) []byte {
	if comment := bytes.IndexByte(rest, '#'); comment >= 0 {
		rest = rest[:comment]
	}
	return bytes.TrimSpace(rest)
}
