package main

import "strings"

// structDocWidth is the column a struct doc comment wraps before.
const structDocWidth = 80

// structDoc renders a struct's doc annotation as line comments, one prefix per
// line, wrapped before structDocWidth columns. Whitespace in the annotation
// collapses to single spaces. An absent or blank annotation renders nothing,
// so structs without documentation keep their exact output.
func structDoc(st Struct, prefix string) string {
	words := strings.Fields(st.Ann["doc"])
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := prefix
	for _, word := range words {
		if line != prefix && len(line)+1+len(word) > structDocWidth {
			b.WriteString(line + "\n")
			line = prefix
		}
		if line == prefix {
			line += word
		} else {
			line += " " + word
		}
	}
	b.WriteString(line + "\n")
	return b.String()
}
