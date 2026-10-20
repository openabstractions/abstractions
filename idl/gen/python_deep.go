package main

import "strings"

// pyRecursiveDepthLimit is the deepest depth_limit the recursive Python
// skip_container serves. It spends two interpreter frames per nesting level
// and Python's default recursion limit is 1000, so a definition declaring a
// deeper limit (up to 1000, [DEF-E9]) would end in RecursionError instead of
// depth_exceeded. Such a definition gets pyIterativeSkip, which walks the
// value with an explicit stack and refuses at the same depth.
const pyRecursiveDepthLimit = 256

const pyRecursiveSkip = `    def skip_container(self):
        opener = self.at()
        shut = _RBRACE if opener == _LBRACE else _RBRACK
        self.enter()
        self.pos += 1
        self.ws()
        seen = set()
        if self.at() != shut:
            while True:
                self.ws()
                if opener == _LBRACE:
                    if self.at() != _QUOTE:
                        raise self.refuse("malformed")
                    k = self.string()
@SKIPDUP@                    self.ws()
                    if self.at() != _COLON:
                        raise self.refuse("malformed")
                    self.pos += 1
                    self.ws()
                self.skip_value()
                self.ws()
                if self.at() != _COMMA:
                    break
                self.pos += 1
        if self.at() != shut:
            raise self.refuse("malformed")
        self.pos += 1
        self.depth -= 1
`

const pyIterativeSkip = `    # Walked with an explicit stack: this definition's depth_limit is deeper
    # than Python's recursion limit allows a recursive walk to reach.
    def skip_container(self):
        stack = []
        while True:
            self.ws()
            c = self.at()
            if c == _LBRACE or c == _LBRACK:
                self.enter()
                self.pos += 1
                self.ws()
                shut = _RBRACE if c == _LBRACE else _RBRACK
                stack.append((c, shut, set()))
                if self.at() != shut:
                    if c == _LBRACE:
                        self.skip_key(stack[-1][2])
                    continue
            else:
                self.skip_value()
            while True:
                opener, shut, seen = stack[-1]
                self.ws()
                if self.at() == _COMMA:
                    self.pos += 1
                    self.ws()
                    if opener == _LBRACE:
                        self.skip_key(seen)
                    break
                if self.at() != shut:
                    raise self.refuse("malformed")
                self.pos += 1
                self.depth -= 1
                stack.pop()
                if not stack:
                    return

    def skip_key(self, seen):
        if self.at() != _QUOTE:
            raise self.refuse("malformed")
        k = self.string()
@SKIPDUP8@        self.ws()
        if self.at() != _COLON:
            raise self.refuse("malformed")
        self.pos += 1
`

// pyDeepSkip swaps the recursive skip_container for the iterative one when
// the definition's depth_limit is past what recursion reaches. Every other
// definition keeps its bytes.
func pyDeepSkip(s *Definition, decode, skipDup string) string {
	if s.Encoding.DepthLimit <= pyRecursiveDepthLimit {
		return decode
	}
	if strings.Count(decode, pyRecursiveSkip) != 1 {
		panic("pyDeepSkip: the Python reader no longer holds its recursive skip_container")
	}
	var dedented []string
	for _, line := range strings.SplitAfter(skipDup, "\n") {
		dedented = append(dedented, strings.TrimPrefix(line, "            "))
	}
	iterative := strings.Replace(pyIterativeSkip, "@SKIPDUP8@", strings.Join(dedented, ""), 1)
	return strings.Replace(decode, pyRecursiveSkip, iterative, 1)
}
