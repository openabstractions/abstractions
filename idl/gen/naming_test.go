package main

import (
	"strings"
	"testing"
)

func TestLanguageNaming(t *testing.T) {
	for _, c := range []struct{ in, snake, upper, camel, pascal string }{
		{"Decide", "decide", "DECIDE", "decide", "Decide"},
		{"DecideFor", "decide_for", "DECIDE_FOR", "decideFor", "DecideFor"},
		{"policy_revision", "policy_revision", "POLICY_REVISION", "policyRevision", "PolicyRevision"},
		{"GetHTTPStatus", "get_http_status", "GET_HTTP_STATUS", "getHttpStatus", "GetHTTPStatus"},
		{"HTTPServer", "http_server", "HTTP_SERVER", "httpServer", "HTTPServer"},
		{"ReadV2", "read_v2", "READ_V2", "readV2", "ReadV2"},
		{"sha256_digest", "sha256_digest", "SHA256_DIGEST", "sha256Digest", "Sha256Digest"},
		{"OAServiceFrame", "oa_service_frame", "OA_SERVICE_FRAME", "oaServiceFrame", "OAServiceFrame"},
		{"not_granted", "not_granted", "NOT_GRANTED", "notGranted", "NotGranted"},
		{"wait_ms", "wait_ms", "WAIT_MS", "waitMs", "WaitMs"},
	} {
		if got := snakeCase(c.in); got != c.snake {
			t.Errorf("snakeCase(%q)=%q, want %q", c.in, got, c.snake)
		}
		if got := upperSnake(c.in); got != c.upper {
			t.Errorf("upperSnake(%q)=%q, want %q", c.in, got, c.upper)
		}
		if got := camelCase(c.in); got != c.camel {
			t.Errorf("camelCase(%q)=%q, want %q", c.in, got, c.camel)
		}
		if got := pascalCase(c.in); got != c.pascal {
			t.Errorf("pascalCase(%q)=%q, want %q", c.in, got, c.pascal)
		}
	}
	for in, want := range map[string]string{"class": "class_", "from": "from_", "self": "self_", "type": "type", "next": "next"} {
		if got := pyName(in); got != want {
			t.Errorf("pyName(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestLanguageNameOverridesAndCollisions(t *testing.T) {
	f := Field{Name: "policy_revision", Ann: map[string]string{"javascript.name": "revision"}}
	if f.Ident("javascript") != "revision" || f.Ident("python") != "policy_revision" || f.Ident("go") != "PolicyRevision" {
		t.Fatal("override or mapping lost", f.Ident("javascript"), f.Ident("python"))
	}
	s, e := parse(head + `struct Record {1: required string fooBar 2: required string foo_bar}(document="true",unknown_fields="refuse")`)
	if e != nil {
		t.Fatal(e)
	}
	for _, lang := range []string{"python", "javascript"} {
		if e := validateServiceBackend(s, lang); e == nil || !strings.Contains(e.Error(), "add "+lang+".name") {
			t.Fatalf("%s accepted colliding fields: %v", lang, e)
		}
	}
	s, e = parse(head + `struct Record {1: required string fooBar 2: required string foo_bar(python.name="foo_bar_text", javascript.name="fooBarText")}(document="true",unknown_fields="refuse")`)
	if e != nil {
		t.Fatal(e)
	}
	for _, lang := range []string{"python", "javascript"} {
		if e := validateServiceBackend(s, lang); e != nil {
			t.Fatalf("%s refused resolved collision: %v", lang, e)
		}
	}
	s, e = parse(head + `struct Record {1: required string value}(document="true",unknown_fields="refuse")
service Query { Record Echo(1: string arguments) }(wire_name="example/query@1")`)
	if e != nil {
		t.Fatal(e)
	}
	if e := validateServiceBackend(s, "javascript"); e == nil || !strings.Contains(e.Error(), "reserved word") {
		t.Fatalf("JavaScript accepted a reserved parameter: %v", e)
	}
	if e := validateServiceBackend(s, "python"); e != nil {
		t.Fatalf("Python refused a parameter that is not a Python keyword: %v", e)
	}
}
