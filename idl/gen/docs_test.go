package main

import (
	"os"
	"strings"
	"testing"
)

var example = func() string {
	if _, err := os.Stat("../testdata/job.thrift"); err == nil {
		return "../testdata/job.thrift"
	}
	return "../../openabstractions-flat/abstraction-job/job.thrift"
}()

func exampleDefinition(t *testing.T) *Definition {
	t.Helper()
	src, err := os.ReadFile(example)
	if err != nil {
		t.Fatal(err)
	}
	def, err := parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func exampleDocs(t *testing.T) emitted {
	t.Helper()
	def := exampleDefinition(t)
	return emitted{def: def, lang: "docs", body: genDocs(def), source: example}
}

func TestDocsAreTheSameBytesTwice(t *testing.T) {
	def := exampleDefinition(t)
	if genDocs(def) != genDocs(def) {
		t.Fatal("two runs of the docs backend disagree")
	}
}

func TestDocsCarryEveryDeclaredName(t *testing.T) {
	if err := verifyDocs(exampleDocs(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAnUndeclaredTagFailsTheBackend(t *testing.T) {
	e := exampleDocs(t)
	e.body += rule("DEF-Z9")
	err := checkTags(e)
	if err == nil || !strings.Contains(err.Error(), "DEF-Z9") {
		t.Fatalf("a citation to an undeclared rule was accepted: %v", err)
	}
}

func TestARepeatedTagFailsTheBackend(t *testing.T) {
	e := exampleDocs(t)
	e.body += rule("DEF-P1")
	err := checkRepetition(e)
	if err == nil || !strings.Contains(err.Error(), "DEF-P1 2 times under protocol") {
		t.Fatalf("a rule cited twice in one section was accepted: %v", err)
	}
}

func TestATagCitedAgainInAnotherSectionIsAccepted(t *testing.T) {
	e := exampleDocs(t)
	e.body += rule("DEF-E1")
	if err := checkRepetition(e); err != nil {
		t.Fatalf("a rule cited once per section was refused: %v", err)
	}
}

func TestAConstantRuleColumnFailsTheBackend(t *testing.T) {
	e := exampleDocs(t)
	e.body = strings.ReplaceAll(e.body, "<td>required</td>", "<td>required "+rule("DEF-A4")+"</td>")
	err := checkRepetition(e)
	if err == nil || !strings.Contains(err.Error(), "DEF-A4") {
		t.Fatalf("one tag repeated down a column was accepted: %v", err)
	}
}

func TestATagInsideParenthesesIsNotADeclaration(t *testing.T) {
	found := declarations("a back-reference ([DEF-A3]) and a statement [DEF-A4]")
	if found["DEF-A3"] {
		t.Error("a parenthesised back-reference was read as a declaration")
	}
	if !found["DEF-A4"] {
		t.Error("a stated rule was not read as a declaration")
	}
}
