package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A rule id is a family of capitals, a dash, then any number of capitals and
// one or more digits: REG-3 and INF-V1 are both valid, the letter after the
// dash is not required. scripts/check.sh's TAG variable states the same form
// in its own comment, and the two are kept matching by hand — a citation
// this backend accepts and the gate's contract-tags rule calls stray, or the
// reverse, is the defect this pair fixed once (id form unified, gate rules
// the reviews asked for, 2026-09-23).
var citedTag = regexp.MustCompile(`\[([A-Z]+-[A-Z]*[0-9]+)\]`)

// ruleDocuments lists the pages checked for a declared tag at one ancestor
// directory of declaredTags' walk to the root: the two fixed language pages,
// and every CONTRACT.md or SPEC.md the tree currently holds under an
// abstraction-* directory, read from dir itself (the public, flat form) and
// from dir/openabstractions-flat (the private form), because the walk passes
// through both trees on its way to the root. Derived from the tree rather
// than typed out: a literal list named five of the seventeen contracts this
// project has, and REG, ENDPOINT, LEND, HOST, ROUTE, INF, CRED, RES, MODEL
// and ASK ids were invisible to every rule that read it (gate rules the
// reviews asked for, 2026-09-23).
func ruleDocuments(dir string) []string {
	out := []string{"LANGUAGE.md", "idl/LANGUAGE.md"}
	for _, prefix := range []string{"", "openabstractions-flat"} {
		entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(prefix)))
		if err != nil {
			continue
		}
		var names []string
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), "abstraction-") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			for _, doc := range []string{"CONTRACT.md", "SPEC.md"} {
				candidate := filepath.Join(dir, filepath.FromSlash(prefix), name, doc)
				if _, err := os.Stat(candidate); err != nil {
					continue
				}
				rel := name + "/" + doc
				if prefix != "" {
					rel = prefix + "/" + rel
				}
				out = append(out, rel)
			}
		}
	}
	return out
}

func verifyDocs(e emitted) error {
	if err := checkNames(e); err != nil {
		return err
	}
	if err := checkRepetition(e); err != nil {
		return err
	}
	return checkTags(e)
}

// A selection narrows what this ranges over, and narrowing a presence check is
// how one stops meaning anything. Three things stop it here. The scope is not
// the flag, it is the surface list in scripts/generate.targets, which is a
// declaration of the artefact's whole contents and is compared against the
// artefact byte for byte — so a scope cannot shrink without the line shrinking
// in the same commit. A name that is not a surface — a refusal word, a typedef,
// an encoding setting — is not narrowable at all and stays a floor under every
// selection. And the count travels with the failure, because a check whose
// scope moves has to say how far it reached.
func checkNames(e emitted) error {
	names := definitionNames(e.def)
	var missing []string
	for _, n := range names {
		if !strings.Contains(e.body, mono(n)) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("the %s backend leaves %s out of the %d names this artefact declares; every one of them is on the page",
			e.lang, strings.Join(missing, ", "), len(names))
	}
	return nil
}

func definitionNames(s *Definition) []string {
	var out []string
	for _, svc := range s.Services {
		out = append(out, svc.Name)
		for _, m := range svc.Methods {
			out = append(out, m.Name)
			for _, f := range m.Args {
				out = append(out, f.Name, f.Type)
			}
		}
	}
	for _, td := range s.Typedefs {
		out = append(out, td.Alias)
	}
	for _, st := range s.Structs {
		out = append(out, st.Name)
		for _, f := range st.Fields {
			out = append(out, f.Name, f.Type)
		}
	}
	for _, en := range s.Enums {
		out = append(out, en.Name)
		for _, m := range en.Members {
			out = append(out, m.Name)
		}
	}
	for _, c := range s.Consts {
		out = append(out, c.Name)
	}
	if s.Vocab != nil {
		for _, t := range s.Vocab.Terms {
			out = append(out, t.Name)
		}
	}
	for _, r := range s.Refusals {
		out = append(out, r.Word)
	}
	if s.Proto != nil {
		for _, op := range s.Proto.Operations {
			out = append(out, op.Name)
		}
	}
	return out
}

// Checking only that every name is present lets a page satisfy the check by
// padding, and one did: a column headed "rule" holding the same tag in every
// row, under a heading that already held it. A tag is a citation, and a second
// citation of one rule earns its place only by reaching a construct the first
// did not. The section is where that becomes true — a reader who arrives at
// Structs has not read Encoding, so a rule may be cited again there — and it is
// false anywhere below one heading, which is what padding looks like. The test
// of the boundary is the encoding table: eight rows, eight different rules, one
// per setting, and that column is the only place the mapping exists.
func checkRepetition(e emitted) error {
	var repeated []string
	for _, section := range strings.Split(e.body, "\n<h2 ") {
		count := map[string]int{}
		for _, m := range citedTag.FindAllStringSubmatch(section, -1) {
			count[m[1]]++
		}
		for tag, n := range count {
			if n > 1 {
				repeated = append(repeated, fmt.Sprintf("%s %d times under %s", tag, n, sectionName(section)))
			}
		}
	}
	if len(repeated) > 0 {
		sort.Strings(repeated)
		return fmt.Errorf("the %s backend cites %s; a rule is cited once in the section it applies to",
			e.lang, strings.Join(repeated, ", "))
	}
	return nil
}

func sectionName(section string) string {
	const anchor = `id="`
	if !strings.HasPrefix(section, anchor) {
		return "the page opening"
	}
	rest := section[len(anchor):]
	if i := strings.IndexByte(rest, '"'); i >= 0 {
		return rest[:i]
	}
	return "the page opening"
}

// A citation is enforcement by a reader who already knows the pattern, and it
// only enforces anything while the rule it names is findable. An unresolved tag
// is therefore a build failure and not a broken link somebody notices later.
func checkTags(e emitted) error {
	cited := map[string]bool{}
	for _, m := range citedTag.FindAllStringSubmatch(e.body, -1) {
		cited[m[1]] = true
	}
	if len(cited) == 0 {
		return nil
	}
	declared, searched := declaredTags(e.source)
	if len(declared) == 0 {
		return fmt.Errorf("the %s backend cites %d rules and no rule document was found under %s",
			e.lang, len(cited), strings.Join(searched, ", "))
	}
	var unresolved []string
	for tag := range cited {
		if declared[tag] == "" {
			unresolved = append(unresolved, tag)
		}
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		return fmt.Errorf("the %s backend cites %s, which no rule document declares; searched %s",
			e.lang, strings.Join(unresolved, ", "), strings.Join(searched, ", "))
	}
	return nil
}

func declaredTags(definition string) (map[string]string, []string) {
	found := map[string]string{}
	var searched []string
	dir, err := filepath.Abs(filepath.Dir(definition))
	if err != nil {
		return found, searched
	}
	for {
		for _, rel := range ruleDocuments(dir) {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			src, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if !contains(searched, path) {
				searched = append(searched, path)
			}
			for tag := range declarations(string(src)) {
				if found[tag] == "" {
					found[tag] = path
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return found, searched
		}
		dir = parent
	}
}

// A tag inside parentheses is a back-reference to a rule stated elsewhere, so it
// is not evidence the rule exists.
func declarations(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range citedTag.FindAllStringSubmatchIndex(src, -1) {
		if m[0] > 0 && src[m[0]-1] == '(' {
			continue
		}
		out[src[m[2]:m[3]]] = true
	}
	return out
}
