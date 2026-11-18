package main

import (
	"html"
	"os"
	"strings"
	"testing"
)

func TestServiceTraitsCiteTheirRuleOnce(t *testing.T) {
	def, err := parse(head + `struct Record {1:required string value}(document="true",unknown_fields="refuse")
service Router {
 Record Models() (readonly="true")
 Record Hosts() (readonly="true")
 Record Offer() (readonly="true")
 Record Pick() (readonly="true")
}(wire_name="test/router@1")
service Other {Record Read() (readonly="true")}(wire_name="test/other@1")`)
	if err != nil {
		t.Fatal(err)
	}
	got := genDocs(def)
	if !strings.Contains(got, `<a href="threat-model.html">Security</a></footer>`) {
		t.Fatal("generated schema footer must link to the public threat model")
	}
	if strings.Count(got, rule("DEF-S3")) != 2 {
		t.Fatalf("want one trait citation in each service section: %s", got)
	}
	if strings.Count(got, html.EscapeString(traitText(def.Services[0].Methods[0]))) != 5 {
		t.Fatalf("every method must retain its trait text: %s", got)
	}
	if err := verifyDocs(emitted{def: def, lang: "docs", body: got, source: examplePath(t)}); err != nil {
		t.Fatal(err)
	}
}

func TestStructSizeNotesCiteTheirRuleOnce(t *testing.T) {
	def, err := parse(maxEncodedHead() + `struct Plain {}(unknown_fields="refuse")
struct One {}(document="true",unknown_fields="refuse",max_encoded="32")
struct Two {}(unknown_fields="refuse",max_encoded="64")
struct Three {}(unknown_fields="refuse",max_encoded="128")
struct Four {}(unknown_fields="refuse",max_encoded="256")
struct Five {}(unknown_fields="refuse",max_encoded="512")
struct Six {}(unknown_fields="refuse",max_encoded="1024")
struct Chunk {}(unknown_fields="refuse",chunk="true")`)
	if err != nil {
		t.Fatal(err)
	}
	got := genDocs(def)
	if strings.Count(got, rule("DEF-A12")) != 1 {
		t.Fatalf("want one size citation in the structs section: %s", got)
	}
	if strings.Count(got, "compact value span at most ") != 6 || strings.Count(got, "one chunk of a stream") != 1 {
		t.Fatalf("every annotated struct must retain its size note: %s", got)
	}
	if err := verifyDocs(emitted{def: def, lang: "docs", body: got, source: examplePath(t)}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalDefinitionLinksInServiceAndMethodDocs(t *testing.T) {
	def, err := parse(head + `struct Record {1:required string value}(document="true",unknown_fields="refuse")
service Sample {oneway void Ping()}(wire_name="test/sample@1")`)
	if err != nil {
		t.Fatal(err)
	}
	text := `<b>before</b> [<Lease & "holder">](https://openabstractions.github.io/reference.html#concept-resource.lease) <script>after</script>`
	def.Services[0].Doc = text
	def.Services[0].Methods[0].Doc = text
	got := genDocs(def)
	want := `<p>&lt;b&gt;before&lt;/b&gt; <a href="https://openabstractions.github.io/reference.html#concept-resource.lease">&lt;Lease &amp; &#34;holder&#34;&gt;</a> &lt;script&gt;after&lt;/script&gt;</p>`
	if strings.Count(got, want) != 2 {
		t.Fatalf("service and method canonical links were not escaped and linked: %s", got)
	}
	if err := verifyDocs(emitted{def: def, lang: "docs", body: got, source: examplePath(t)}); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedDocumentationLinksStayEscaped(t *testing.T) {
	for _, text := range []string{
		`<img src=x onerror=alert(1)>`,
		`[x](javascript:alert(1))`,
		`[x](http://openabstractions.github.io/reference.html#concept-resource.lease)`,
		`[x](https://evil.example/reference.html#concept-resource.lease)`,
		`[x](https://openabstractions.github.io/reference.html#concept-resource.lease" onclick="x)`,
		`[x](https://openabstractions.github.io/reference.html#concept-)`,
		`[x](https://openabstractions.github.io/reference.html#concept-resource.lease`,
		`[x](https://openabstractions.github.io/reference.html#concept-resource.lease?x=1)`,
	} {
		if got := docHTML(text); got != html.EscapeString(text) {
			t.Errorf("unsupported source gained markup: %q -> %q", text, got)
		}
	}
}

func TestCanonicalDefinitionLinksInOwnSymbolHeadings(t *testing.T) {
	def, err := parse(head + `struct Record {1:required string value}(document="true",unknown_fields="refuse",doc="[Record](https://openabstractions.github.io/reference.html#concept-log.record) fields.")
service Sample {oneway void Ping()}(wire_name="test/sample@1",doc="[Sample](https://openabstractions.github.io/reference.html#concept-resource.leases) service.")`)
	if err != nil {
		t.Fatal(err)
	}
	got := genDocs(def)
	for _, want := range []string{
		`<h3 id="struct-Record"><a href="https://openabstractions.github.io/reference.html#concept-log.record"><code>Record</code></a></h3>`,
		`<h2 id="service-Sample">Service <a href="https://openabstractions.github.io/reference.html#concept-resource.leases"><code>Sample</code></a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("own symbol heading lacks canonical link %q", want)
		}
	}
	if strings.Contains(got, "fields.</p>") {
		t.Fatal("heading support unexpectedly added a struct doc body")
	}
	if err := verifyDocs(emitted{def: def, lang: "docs", body: got, source: examplePath(t)}); err != nil {
		t.Fatal(err)
	}
}

func TestSymbolHeadingLinkRequiresOneExactCanonicalTarget(t *testing.T) {
	url := "https://openabstractions.github.io/reference.html#concept-resource.lease"
	for _, text := range []string{
		"[Other](" + url + ")",
		"[lease](" + url + ")",
		"[Lease](javascript:alert(1))",
		"[Lease](http://openabstractions.github.io/reference.html#concept-resource.lease)",
		"[Lease](" + url + ") [Lease](https://openabstractions.github.io/reference.html#concept-job.lease)",
	} {
		if got := symbolDocHTML("Lease", text); got != mono("Lease") {
			t.Errorf("unmatched, unsafe or ambiguous link changed heading: %q -> %s", text, got)
		}
	}
	if got := symbolDocHTML(`Lease<&"`, `[Lease<&"](`+url+`)`); got != `<a href="`+url+`"><code>Lease&lt;&amp;&#34;</code></a>` {
		t.Fatalf("heading name was not escaped: %s", got)
	}
	if got := symbolDocHTML("Lease", "[Lease]("+url+") [Lease]("+url+")"); strings.Count(got, "<a ") != 1 {
		t.Fatalf("repeated same target is not one heading link: %s", got)
	}
}

func TestEnumHeadingUsesOwnCanonicalDocumentation(t *testing.T) {
	src := head + doc + `enum Evidence {1: verified 2: claimed}(unknown="refuse",reader="act",doc="[Evidence](https://openabstractions.github.io/reference.html#concept-resource.measurement-evidence) is accounting metadata.")`
	def, err := parse(src)
	if err != nil {
		t.Fatal(err)
	}
	got := genDocs(def)
	want := `<h3 id="enum-Evidence"><a href="https://openabstractions.github.io/reference.html#concept-resource.measurement-evidence"><code>Evidence</code></a></h3>`
	if !strings.Contains(got, want) {
		t.Fatalf("enum heading lacks its own canonical link: %s", got)
	}
	if err := verifyDocs(emitted{def: def, lang: "docs", body: got, source: examplePath(t)}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		`[Other](https://openabstractions.github.io/reference.html#concept-resource.measurement-evidence)`,
		`[Evidence](javascript:alert(1))`,
		`<script>before</script> [Evidence](https://evil.example/reference.html#concept-resource.measurement-evidence)`,
		`[Evidence](https://openabstractions.github.io/reference.html#concept-resource.measurement-evidence) [Evidence](https://openabstractions.github.io/reference.html#concept-identity.proof-ceiling)`,
	} {
		def.Enums[0].Ann["doc"] = text
		got = genDocs(def)
		if !strings.Contains(got, `<h3 id="enum-Evidence"><code>Evidence</code></h3>`) || strings.Contains(got, "<script>before") {
			t.Fatalf("unmatched, unsafe or conflicting enum documentation changed heading: %q", text)
		}
	}
}

func examplePath(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("../testdata/job.thrift"); err == nil {
		return "../testdata/job.thrift"
	}
	return privateInput(t, "..", "..", "openabstractions-flat", "abstraction-job", "job.thrift")
}

func exampleDefinition(t *testing.T) *Definition {
	t.Helper()
	src, err := os.ReadFile(examplePath(t))
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
	return emitted{def: def, lang: "docs", body: genDocs(def), source: examplePath(t)}
}

func TestDocsAreTheSameBytesTwice(t *testing.T) {
	def := exampleDefinition(t)
	if genDocs(def) != genDocs(def) {
		t.Fatal("two runs of the docs backend disagree")
	}
}

func TestDocsCarryEveryDeclaredName(t *testing.T) {
	docs := exampleDocs(t)
	if err := verifyDocs(docs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(docs.body, "<title>OpenAbstractions — schema</title>") || strings.Contains(docs.body, "Open Abstractions") {
		t.Fatal("schema documentation must use the product's display name")
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
