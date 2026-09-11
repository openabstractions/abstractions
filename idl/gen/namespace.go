package main

import (
	"fmt"
	gotoken "go/token"
	"path/filepath"
	"strings"
)

// Namespace changes generated names and file locations, never the wire.
// Definitions without declarations retain their existing output exactly.
func namespaceFor(names map[string]string, lang string) string {
	if n, ok := names[lang]; ok {
		return n
	}
	return names["*"]
}

func validateNamespace(lang, name string) error {
	if lang != "*" && lang != "go" && lang != "cpp" && lang != "python" && lang != "javascript" && lang != "rust" {
		return fmt.Errorf("namespace targets unknown language %q", lang)
	}
	if name == "" {
		return fmt.Errorf("namespace %s is empty", lang)
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" {
			return fmt.Errorf("namespace %q has an empty component", name)
		}
		for i, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
				return fmt.Errorf("namespace %q requires dot-separated identifiers", name)
			}
		}
		if part == "_" || strings.HasPrefix(part, "__") {
			return fmt.Errorf("namespace %q uses reserved identifier %q", name, part)
		}
		// Output must also be a usable path on Windows, even when generated elsewhere.
		upper := strings.ToUpper(part)
		if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" ||
			len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '0' && upper[3] <= '9' {
			return fmt.Errorf("namespace %q uses reserved file name %q", name, part)
		}
		langs := []string{lang}
		if lang == "*" {
			langs = []string{"go", "cpp", "python", "javascript", "rust"}
		}
		for _, target := range langs {
			if namespaceKeyword(target, part) {
				return fmt.Errorf("namespace %q uses %s keyword %q", name, target, part)
			}
		}
	}
	return nil
}

func namespaceKeyword(lang, word string) bool {
	if lang == "go" {
		return gotoken.IsKeyword(word)
	}
	keywords := map[string]string{
		"cpp":        "alignas alignof and and_eq asm auto bitand bitor bool break case catch char char8_t char16_t char32_t class compl concept const consteval constexpr constinit const_cast continue co_await co_return co_yield decltype default delete do double dynamic_cast else enum explicit export extern false float for friend goto if inline int long mutable namespace new noexcept not not_eq nullptr operator or or_eq private protected public register reinterpret_cast requires return short signed sizeof static static_assert static_cast struct switch template this thread_local throw true try typedef typeid typename union unsigned using virtual void volatile wchar_t while xor xor_eq",
		"python":     "False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield",
		"javascript": "await break case catch class const continue debugger default delete do else enum export extends false finally for function if implements import in instanceof interface let new null package private protected public return static super switch this throw true try typeof var void while with yield",
		"rust":       "Self abstract as async await become box break const continue crate do dyn else enum extern false final fn for if impl in let loop macro match mod move mut override priv pub ref return self static struct super trait true try type typeof union unsafe unsized use virtual where while yield",
	}
	return strings.Contains(" "+keywords[lang]+" ", " "+word+" ")
}

func namespacedOutput(b backend, names map[string]string, body string) (string, string) {
	n := namespaceFor(names, b.lang)
	if n == "" {
		return b.path, body
	}
	parts := strings.Split(n, ".")
	path := filepath.ToSlash(filepath.Join(append([]string{strings.Split(b.path, "/")[0]}, append(parts, filepath.Base(b.path))...)...))
	if b.lang == "go" {
		body = strings.Replace(body, "package rec\n", "package "+parts[len(parts)-1]+"\n", 1)
	}
	if b.lang == "cpp" {
		body = strings.Replace(body, "namespace rec {", "namespace "+strings.Join(parts, "::")+" {", 1)
		body = strings.Replace(body, "\n}  // namespace rec\n", "\n}  // namespace "+strings.Join(parts, "::")+"\n", 1)
	}
	if b.lang == "docs" {
		// Keep generated pages beside the site's shared stylesheet/navigation.
		path = n + ".schema.html"
	}
	return path, body
}
