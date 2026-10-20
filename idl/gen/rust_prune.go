package main

import (
	"regexp"
	"strings"
)

// rsPrune removes the private items a generated crate never reaches. The
// prelude carries every codec helper a definition might need, and a crate that
// compiles with warnings denied carries no dead code; nothing is silenced with
// an allow attribute instead. Public items are the crate's API and stay.
//
// An item is a private free function, constant, struct or type alias at column
// zero, or a method of the private Reader. Its comments and attributes go with
// it. An item is kept when code outside every item names it, or when a kept
// item does; mutually recursive helpers nobody calls go together. Names are
// matched in the source with comments and literals blanked, so a wire key or a
// sentence naming a helper keeps nothing alive.
func rsPrune(body string) string {
	for {
		next := rsPruneOnce(body)
		if next == body {
			return rsPruneImports(body)
		}
		body = next
	}
}

var (
	rsFreeItem   = regexp.MustCompile(`^(?:fn|const|struct|type) ([A-Za-z_][A-Za-z0-9_]*)`)
	rsReaderItem = regexp.MustCompile(`^    fn ([A-Za-z_][A-Za-z0-9_]*)`)
)

type rsItem struct {
	name       string
	method     bool
	start, end int // line indexes, end exclusive
}

func rsItems(lines []string) []rsItem {
	var items []rsItem
	inReader := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\n")
		if strings.HasPrefix(line, "impl<'a> Reader<'a> {") {
			inReader = true
			continue
		}
		if inReader && line == "}" {
			inReader = false
			continue
		}
		var m []string
		prefix := ""
		if inReader {
			m = rsReaderItem.FindStringSubmatch(line)
			prefix = "    "
		} else {
			m = rsFreeItem.FindStringSubmatch(line)
		}
		if m == nil {
			continue
		}
		start := i
		for start > 0 {
			prev := strings.TrimRight(lines[start-1], "\n")
			if strings.HasPrefix(prev, prefix+"//") || strings.HasPrefix(prev, prefix+"#[") {
				start--
				continue
			}
			break
		}
		end := i + 1
		single := strings.HasSuffix(line, ";") || (strings.HasSuffix(line, "}") && strings.Count(line, "{") == strings.Count(line, "}"))
		if !single {
			for end < len(lines) {
				last := strings.TrimRight(lines[end-1], "\n")
				if last == prefix+"}" || last == prefix+"};" {
					break
				}
				end++
			}
		}
		items = append(items, rsItem{name: m[1], method: inReader, start: start, end: end})
		i = end - 1
	}
	return items
}

func rsPruneOnce(body string) string {
	lines := strings.SplitAfter(body, "\n")
	items := rsItems(lines)
	code := strings.SplitAfter(rsBlankLiterals(body), "\n")
	owner := make([]int, len(lines))
	for i := range owner {
		owner[i] = -1
	}
	for k, it := range items {
		for j := it.start; j < it.end; j++ {
			owner[j] = k
		}
	}
	// Index uses once, rather than searching every kept item's text once per
	// candidate helper. These are the same ASCII word boundaries as Go's \b;
	// a Reader method additionally requires the immediately preceding dot.
	free, methods := map[string][]int{}, map[string][]int{}
	for k, it := range items {
		if it.method {
			methods[it.name] = append(methods[it.name], k)
		} else {
			free[it.name] = append(free[it.name], k)
		}
	}
	edges := make([][]int, len(items))
	kept := make([]bool, len(items))
	var queue []int
	link := func(from int, matches []int) {
		for _, to := range matches {
			if from >= 0 {
				edges[from] = append(edges[from], to)
			} else if !kept[to] {
				kept[to] = true
				queue = append(queue, to)
			}
		}
	}
	for line, text := range code {
		for pos := 0; pos < len(text); {
			if !rsWordByte(text[pos]) {
				pos++
				continue
			}
			start := pos
			for pos < len(text) && rsWordByte(text[pos]) {
				pos++
			}
			word := text[start:pos]
			link(owner[line], free[word])
			if start > 0 && text[start-1] == '.' {
				link(owner[line], methods[word])
			}
		}
	}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		for _, other := range edges[k] {
			if !kept[other] {
				kept[other] = true
				queue = append(queue, other)
			}
		}
	}
	var out strings.Builder
	removed := false
	for j, l := range lines {
		if owner[j] >= 0 && !kept[owner[j]] {
			removed = true
			continue
		}
		out.WriteString(l)
	}
	if !removed {
		return body
	}
	result := out.String()
	// A removed item leaves the blank line that separated it; two in a row are one.
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return result
}

func rsWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func rsPruneImports(body string) string {
	const use = "use std::collections::BTreeMap;\n"
	if !strings.Contains(body, use) {
		return body
	}
	rest := strings.Replace(rsBlankLiterals(body), use, "", 1)
	if strings.Contains(rest, "BTreeMap") {
		return body
	}
	return strings.TrimPrefix(strings.Replace(body, use, "", 1), "\n")
}

// rsBlankLiterals replaces comments, string literals and character literals
// with spaces, keeping every newline so line indexes still agree.
func rsBlankLiterals(src string) string {
	b := []byte(src)
	out := make([]byte, len(b))
	copy(out, b)
	blank := func(from, to int) {
		for k := from; k < to && k < len(out); k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	for i := 0; i < len(b); {
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			j := i
			for j < len(b) && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case b[i] == '"':
			j := i + 1
			for j < len(b) && b[j] != '"' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			blank(i+1, j)
			i = j + 1
		case b[i] == '\'':
			// A character literal closes within a short escape; a lifetime does not close.
			j := i + 1
			if j < len(b) && b[j] == '\\' {
				j += 2
				for j < len(b) && b[j] != '\'' && j-i < 12 {
					j++
				}
			} else {
				j++
			}
			if j < len(b) && b[j] == '\'' {
				blank(i+1, j)
				i = j + 1
			} else {
				i++
			}
		default:
			i++
		}
	}
	return string(out)
}
