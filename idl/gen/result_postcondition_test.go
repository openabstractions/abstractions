package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialsMetadataResultPostconditionCoverage(t *testing.T) {
	path := productionFile(t, "abstraction-credentials/credentials.thrift")
	d, err := loadDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plain := string(raw)
	for _, annotation := range []string{`result_present="current",result_when="outcome",result_is="found",`, `result_present="value",result_when="outcome",result_is="read",`} {
		if strings.Count(plain, annotation) != 1 {
			t.Fatal("missing/duplicate credential relation", annotation)
		}
		plain = strings.Replace(plain, annotation, "", 1)
	}
	before := mustParse(t, plain)
	for _, svc := range d.Services {
		for _, m := range svc.Methods {
			p := m.ResultPostcondition
			wanted := map[string]string{"ScopedHolder.Metadata": "current:found", "SecretReader.Read": "value:read"}[svc.Name+"."+m.Name]
			if wanted != "" {
				if p == nil || p.Payload.Name+":"+strings.Join(p.Members, ",") != wanted || p.Discriminator.Name != "outcome" {
					t.Fatal(svc.Name, m.Name, p)
				}
			} else if p != nil {
				t.Fatal("unexpected Credentials postcondition", svc.Name, m.Name)
			}
		}
	}
	if fmt.Sprint(definitionHashes(before)) != fmt.Sprint(definitionHashes(d)) || len(compatibility(before, d)) != 0 || len(compatibility(d, before)) != 0 {
		t.Fatal("Credentials wire identity changed")
	}
}

func TestAsksResultPostconditionCoverage(t *testing.T) {
	path := productionFile(t, "abstraction-asks/asks.thrift")
	d, err := loadDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	annotation := `result_present="answer",result_when="outcome",result_is="pending,answered",`
	if strings.Count(string(raw), annotation) != 4 {
		t.Fatal("expected question and permission Ask/Request/Observe annotations")
	}
	before, err := parseWithIncludes(strings.ReplaceAll(string(raw), annotation, ""), func(name string) (*Definition, error) { return loadDefinition(filepath.Join(filepath.Dir(path), name)) })
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range d.Services {
		for _, m := range svc.Methods {
			p := m.ResultPostcondition
			if svc.Name == "QuestionApplication" && (m.Name == "Ask" || m.Name == "Observe") || svc.Name == "PermissionApplication" && (m.Name == "Request" || m.Name == "Observe") {
				if p == nil || p.Payload.Name != "answer" || p.Discriminator.Name != "outcome" || strings.Join(p.Members, ",") != "pending,answered" {
					t.Fatal(svc.Name, m.Name, p)
				}
			} else if p != nil {
				t.Fatal("unexpected Asks postcondition", svc.Name, m.Name)
			}
		}
	}
	if fmt.Sprint(definitionHashes(before)) != fmt.Sprint(definitionHashes(d)) || len(compatibility(before, d)) != 0 || len(compatibility(d, before)) != 0 {
		t.Fatal("Asks wire identity changed")
	}
}

func TestResultPostconditionMemberSets(t *testing.T) {
	comma := strings.NewReplacer("1:ok", `1:ok(wire="ok,no")`, `result_is="ok"`, `result_is="ok,no"`).Replace(postFixture)
	if members := mustParse(t, comma).Services[0].Methods[0].ResultPostcondition.Members; len(members) != 1 || members[0] != "ok,no" {
		t.Fatal("existing exact comma-containing spelling changed", members)
	}
	for _, invalid := range []string{"ok,", ",ok", "ok,,no", "ok,ok", "ok,missing", "ok, no", "ok || no"} {
		if _, err := parse(strings.Replace(postFixture, `result_is="ok"`, `result_is="`+invalid+`"`, 1)); err == nil {
			t.Fatal("invalid member set accepted", invalid)
		}
	}
	forward := mustParse(t, strings.Replace(postFixture, `result_is="ok"`, `result_is="ok,no"`, 1)).Services[0].Methods[0]
	reverse := mustParse(t, strings.Replace(postFixture, `result_is="ok"`, `result_is="no,ok"`, 1)).Services[0].Methods[0]
	if strings.Join(forward.ResultPostcondition.Members, ",") != "ok,no" || fmt.Sprint(forward.ResultPostcondition) != fmt.Sprint(reverse.ResultPostcondition) {
		t.Fatal("member-set lowering is not canonical")
	}
	m := mustParse(t, postFixture).Services[0].Methods[0]
	for lang, want := range map[string]string{
		"go":         `if (r.Outcome.String()=="ok")!=(r.Data!=nil){err=&ServiceError{Code:ServiceErrorCodeInvalidResult};return}` + "\n",
		"cpp":        `if ((wire_name(r.outcome)=="ok")!=r.data.has_value()) throw ServiceError("invalid_result","");` + "\n",
		"python":     "        if (r.outcome == \"ok\") != (r.data is not None):\n            raise ServiceError(\"invalid_result\", \"\")\n",
		"javascript": "    if ((r.outcome === \"ok\") !== (r.data !== undefined && r.data !== null)) throw new ServiceError(\"invalid_result\", \"\");\n",
		"rust":       "        if (r.outcome.as_str() == \"ok\") != r.data.is_some() { return Err(CallError::Service(ServiceError { code: \"invalid_result\".into(), message: ::std::string::String::new() })); }\n",
	} {
		var out strings.Builder
		emitResultPostcondition(&out, m, lang, "r")
		if out.String() != want {
			t.Fatalf("%s single-member output changed:\n%s", lang, out.String())
		}
	}
}

func TestConfigResultPostconditionCoverage(t *testing.T) {
	path := productionFile(t, "abstraction-config/config.thrift")
	d, err := loadDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	annotation := `result_present="snapshot",result_when="outcome",result_is="snapshot",`
	before := mustParseConfig(t, strings.Replace(string(raw), annotation, "", 1))
	var found bool
	for _, svc := range d.Services {
		for _, m := range svc.Methods {
			p := m.ResultPostcondition
			if svc.Name == "ConfigObserver" && m.Name == "Observe" {
				found = p != nil && p.Payload.Name == "snapshot" && p.Discriminator.Name == "outcome" && strings.Join(p.Members, ",") == "snapshot"
			} else if svc.Name == "ConfigReaderV2" && m.Name == "Read" {
				if p == nil || p.Payload.Name != "corrupt_source" || strings.Join(p.Members, ",") != "corrupt" {
					t.Fatal("missing typed corrupt-source relation")
				}
			} else if svc.Name == "ConfigEditorV2" && m.Name == "ReadUser" {
				if p == nil || p.Payload.Name != "snapshot" || strings.Join(p.Members, ",") != "read" {
					t.Fatal("missing typed user snapshot relation")
				}
			} else if p != nil {
				t.Fatal("unexpected Config postcondition", svc.Name, m.Name)
			}
		}
	}
	if !found || fmt.Sprint(definitionHashes(before)) != fmt.Sprint(definitionHashes(d)) || len(compatibility(before, d)) != 0 || len(compatibility(d, before)) != 0 {
		t.Fatal("Config relation missing or wire identity changed")
	}
}

func TestModelResultPostconditionCoverage(t *testing.T) {
	path := productionFile(t, "abstraction-model/model.thrift")
	d, err := loadDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plain := string(raw)
	// ModelResolver and ModelManifest carry the resolved relation; every other
	// Model service (catalog@1 among them) carries none.
	related := map[string]string{"ModelResolver": "request", "ModelManifest": "manifest"}
	seen := 0
	for _, svc := range d.Services {
		want, ok := related[svc.Name]
		if !ok {
			for _, m := range svc.Methods {
				if m.ResultPostcondition != nil {
					t.Fatal("unexpected Model postcondition", svc.Name, m.Name)
				}
			}
			continue
		}
		seen++
		if len(svc.Methods) != 1 {
			t.Fatal("unexpected Model methods")
		}
		m := svc.Methods[0]
		p := m.ResultPostcondition
		if want == "" || p == nil || p.Payload.Name != want || p.Discriminator.Name != "outcome" || strings.Join(p.Members, ",") != "resolved" {
			t.Fatal(svc.Name, p)
		}
		annotation := fmt.Sprintf("result_present=%q,result_when=%q,result_is=%q", want, "outcome", "resolved")
		plain = strings.Replace(plain, annotation+",", "", 1)
		plain = strings.Replace(plain, annotation, "", 1)
	}
	if seen != len(related) {
		t.Fatal("expected both related Model services")
	}
	// Reload the same include graph with only the non-wire annotations removed.
	before, err := parseWithIncludes(plain, func(name string) (*Definition, error) { return loadDefinition(filepath.Join(filepath.Dir(path), name)) })
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(definitionHashes(before)) != fmt.Sprint(definitionHashes(d)) || len(compatibility(before, d)) != 0 || len(compatibility(d, before)) != 0 {
		t.Fatal("Model wire identity changed")
	}
}

func TestStorageResultPostconditionCoverage(t *testing.T) {
	path := productionFile(t, "abstraction-storage/content.thrift")
	d, err := loadDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plain := string(raw)
	want := map[string]string{"ContentReader.Open": "resource:opened", "ContentReader.Read": "chunk:data", "ContentWriter.Begin": "upload:started", "ContentWriter.Commit": "stored:committed"}
	for _, svc := range d.Services {
		for _, m := range svc.Methods {
			key := svc.Name + "." + m.Name
			expected, ok := want[key]
			if !ok {
				if m.ResultPostcondition != nil {
					t.Fatal("unexpected relation", svc.Name, m.Name)
				}
				continue
			}
			p := m.ResultPostcondition
			if p == nil || p.Payload.Name+":"+strings.Join(p.Members, ",") != expected || p.Discriminator.Name != "outcome" {
				t.Fatal("missing Storage relation", key, p)
			}
			plain = strings.Replace(plain, fmt.Sprintf("result_present=%q,result_when=%q,result_is=%q,", m.ResultPresent, m.ResultWhen, m.ResultIs), "", 1)
			delete(want, key)
		}
	}
	if len(want) != 0 {
		t.Fatal("missing Storage methods", want)
	}
	before := mustParse(t, plain)
	if fmt.Sprint(definitionHashes(before)) != fmt.Sprint(definitionHashes(d)) || len(compatibility(before, d)) != 0 || len(compatibility(d, before)) != 0 {
		t.Fatal("Storage wire identity changed")
	}
}

// The actual production methods and their imported payload codecs share the
// postcondition truth table. These are injected RPC transports, without native
// endpoints or a registry provider. Structural codecs still encode bad pairs.
func TestProductionResultPostconditionAllLanguages(t *testing.T) {
	out := t.TempDir()
	for _, source := range []string{"abstraction-download/request.thrift", "abstraction-storage/content.thrift", "abstraction-model/model.thrift", "abstraction-config/config.thrift", "abstraction-rights/rights.thrift", "abstraction-asks/asks.thrift", "abstraction-credentials/credentials.thrift"} {
		args := []string{productionFile(t, source), out, "--named-codecs", "go", "cpp", "python", "rust", "javascript"}
		if source == "abstraction-asks/asks.thrift" || source == "abstraction-config/config.thrift" {
			jsRights := "../../rights/api/index.mjs"
			if source == "abstraction-config/config.thrift" {
				jsRights = "../rights/api/index.mjs"
			}
			args = append(args, "--go-import=rights=post.test/abstraction/rights/api", "--rust-import=rights=rights_api", "--js-import=rights="+jsRights)
		}
		if strings.Contains(source, "abstraction-model/") {
			args = append(args, "--go-import=request=post.test/abstraction/download/request", "--go-import=content=post.test/abstraction/storage/content", "--rust-import=request=request_api", "--rust-import=content=content_api", "--js-import=request=../../download/request/index.mjs", "--js-import=content=../../storage/content/index.mjs")
		}
		var report bytes.Buffer
		if err := run(args, &report); err != nil {
			t.Fatal(source, err, report.String())
		}
	}
	execute := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "PYTHONPATH="+filepath.Join(out, "py"))
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v: %v\n%s", args, e, b)
		}
	}
	// Every case uses the same generated sources. Only its driver varies.
	rustLibraries := map[string]bool{}
	buildRustLibrary := func(t *testing.T, compiler, crate, source string, dependencies ...string) {
		t.Helper()
		if rustLibraries[crate] {
			return
		}
		args := []string{compiler, "--edition=2021", "--crate-type=rlib", "--crate-name", crate, filepath.Join(out, filepath.FromSlash(source)), "-o", "lib" + crate + ".rlib"}
		execute(t, out, append(args, dependencies...)...)
		rustLibraries[crate] = true
	}
	for _, tc := range []struct{ name, service, method, record, field, payload, extra string }{
		{"ModelResolver", "abstraction.model/resolver@1", "Resolve", "LookupResult", "request", `{"artifact":{"digest":"sha256:` + strings.Repeat("a", 64) + `"},"sources":[{"scheme":"https","locator":"https://example.invalid/opaque?x=%2F"}]}`, ""},
		{"ModelManifest", "abstraction.model/manifest@1", "Resolve", "ManifestResult", "manifest", `{"id":"opaque-id","addressing":"content","kind":"abstraction.model/descriptor@1","entries":[],"names":[],"store":"opaque-store","descriptor":{"family":"opaque/family"}}`, `,"resolved_revision":"opaque/revision"`},
		{"ContentReader", "abstraction.storage/content-reader@1", "Open", "OpenResult", "resource", `{"handle":"opaque-handle","digest":"sha256:` + strings.Repeat("a", 64) + `","size":3,"verification":"unverified"}`, ""},
		{"ContentReader", "abstraction.storage/content-reader@1", "Read", "ReadResult", "chunk", `{"offset":0,"total":3,"data":"AP8K","eof":true}`, ""},
		{"ContentWriter", "abstraction.storage/content-writer@1", "Begin", "BeginResult", "upload", `{"handle":"opaque-upload","digest":"sha256:` + strings.Repeat("a", 64) + `","size":3,"received":0}`, `,"limit":16`},
		{"ContentWriter", "abstraction.storage/content-writer@1", "Commit", "CommitResult", "stored", `{"digest":"sha256:` + strings.Repeat("a", 64) + `","size":3,"evidence":"hashed"}`, `,"received":0`},
		{"ConfigObserver", "abstraction.config/observer@1", "Observe", "ConfigObservation", "snapshot", `{"nas_store":"opaque/config","store":"","log_sink":"","log_service":"","off":{},"origins":{"nas_store":{"rung":"default","path":""},"store":{"rung":"default","path":""},"log_sink":{"rung":"default","path":""},"log_service":{"rung":"default","path":""},"off":{"rung":"default","path":""}},"stamp":"opaque-stamp"}`, `,"cursor":"c2"`},
		{"QuestionApplication", "abstraction.asks/application@1", "Ask", "QuestionObservation", "answer", `{"id":"opaque-answer"}`, ""},
		{"QuestionApplication", "abstraction.asks/application@1", "Observe", "QuestionObservation", "answer", `{"id":"opaque-answer"}`, ""},
		{"PermissionApplication", "abstraction.asks/permission@1", "Request", "PermissionObservation", "answer", `{"id":"opaque-answer"}`, `,"policy_revision":""`},
		{"PermissionApplication", "abstraction.asks/permission@1", "Observe", "PermissionObservation", "answer", `{"id":"opaque-answer"}`, `,"policy_revision":""`},
		{"ScopedHolder", "abstraction.credentials/holder@2", "Metadata", "MetadataReadResult", "current", `{"name":"opaque-metadata","kind":"bearer","revision":"opaque-revision","state":"active","scope":{"targets":["example.invalid"],"consumers":["abstraction.download/http-execution@1"]},"registered_by":{"account":"fixture-account","program":"/fixture-program"},"registered":"2026-10-30T00:00:00.000000Z"}`, ""},
		{"SecretReader", "abstraction.credentials/reader@1", "Read", "SecretReadResult", "value", `{"name":"opaque-secret","revision":"opaque-revision","secret":"Zml4dHVyZQ=="}`, ""},
	} {
		t.Run(tc.name+"."+tc.method, func(t *testing.T) {
			marker := "opaque?x=%2F"
			success := "resolved"
			arguments := `{"ref":{"registry":"inert","repo":"opaque/repo","revision":"","quant":"","file":""}}`
			if tc.field == "manifest" {
				marker = "opaque/family"
			}
			if tc.method == "Open" {
				marker = "opaque-handle"
				success = "opened"
				arguments = `{"digest":"sha256:` + strings.Repeat("a", 64) + `"}`
			}
			if tc.method == "Read" {
				marker = "AP8K"
				success = "data"
				arguments = `{"handle":"opaque-handle","offset":0,"max_bytes":3}`
			}
			if tc.method == "Begin" {
				marker, success = "opaque-upload", "started"
				arguments = `{"request":"opaque-request-0001","digest":"sha256:` + strings.Repeat("a", 64) + `","size":3}`
			}
			if tc.method == "Commit" {
				marker, success = "sha256:"+strings.Repeat("a", 64), "committed"
				arguments = `{"handle":"opaque-upload"}`
			}
			if tc.name == "ConfigObserver" {
				marker, success = "opaque/config", "snapshot"
				arguments = `{"overrides":{"nas_store":"","store":"","log_sink":"","log_service":""},"cursor":"c1","wait_ms":0}`
			}
			if tc.name == "QuestionApplication" || tc.name == "PermissionApplication" {
				marker, success = "opaque-answer", "pending"
				arguments = `{"question":{"request_key":"kept-request","key":"known-question","slots":{}}}`
				if tc.method == "Observe" {
					arguments = `{"request_key":"kept-request","wait_ms":0}`
				} else if tc.name == "PermissionApplication" {
					arguments = `{"request":{"request_key":"kept-request"}}`
				}
			}
			if tc.name == "ScopedHolder" {
				marker, success = "opaque-metadata", "found"
				arguments = `{"name":"opaque-metadata"}`
			}
			if tc.name == "SecretReader" {
				marker, success = "opaque-secret", "read"
				arguments = `{"name":"opaque-secret"}`
			}
			replies, records := []string{}, []string{}
			type pair struct {
				outcome string
				present bool
			}
			pairs := []pair{{success, true}, {"unavailable", false}, {success, false}, {"unavailable", true}}
			goodCount, presentCount := 2, 1
			if tc.name == "QuestionApplication" || tc.name == "PermissionApplication" {
				pairs = nil
				for _, valid := range []bool{true, false} {
					outcomes := []string{"pending", "answered", "unknown", "gone", "invalid", "conflict", "forbidden", "unavailable"}
					if tc.name == "PermissionApplication" {
						outcomes = append(outcomes, "permitted", "denied")
					}
					for _, outcome := range outcomes {
						pairs = append(pairs, pair{outcome, (outcome == "pending" || outcome == "answered") == valid})
					}
				}
				goodCount, presentCount = 8, 2
				if tc.name == "PermissionApplication" {
					goodCount = 10
				}
			}
			if tc.name == "ScopedHolder" {
				pairs = nil
				for _, valid := range []bool{true, false} {
					for _, outcome := range []string{"found", "unknown", "invalid", "forbidden", "unavailable", "conflict"} {
						pairs = append(pairs, pair{outcome, (outcome == "found") == valid})
					}
				}
				goodCount, presentCount = 6, 1
			}
			if tc.name == "SecretReader" {
				pairs = nil
				for _, valid := range []bool{true, false} {
					for _, outcome := range []string{"read", "unknown", "expired", "revoked", "lost", "invalid", "forbidden", "unavailable", "conflict", "unsupported_kind"} {
						pairs = append(pairs, pair{outcome, (outcome == "read") == valid})
					}
				}
				goodCount, presentCount = 10, 1
			}
			for _, pair := range pairs {
				extra := tc.extra
				if tc.method == "Begin" && pair.outcome != "started" {
					extra = `,"limit":0`
				}
				if pair.outcome != "resolved" {
					extra = strings.Replace(extra, "opaque/revision", "", 1)
				}
				r := `{"outcome":"` + pair.outcome + `"` + extra
				if pair.present {
					r += `,"` + tc.field + `":` + tc.payload
				}
				r += `}`
				records = append(records, r)
				replies = append(replies, `{"version":1,"service":"`+tc.service+`","method":"`+tc.method+`","ok":true,"payload":{"value":`+r+`}}`)
			}
			request := `{"version":1,"service":"` + tc.service + `","method":"` + tc.method + `","arguments":` + arguments + `}`
			render := func(code, language string) string {
				field := tc.field
				extraHandlers := ""
				binaryCheck := ""
				if tc.field == "chunk" {
					binaryCheck = map[string]string{
						"go":         `if i==0&&string(v.Chunk.Data)!="\x00\xff\n"{panic("binary changed")};`,
						"python":     `if i==0: assert v.chunk.data==bytes([0,255,10])`,
						"javascript": `if(i===0)assert.deepEqual([...v.chunk.data],[0,255,10]);`,
						"cpp":        `if(i==0)assert((v.chunk->data==std::vector<std::uint8_t>{0,255,10}));`,
						"rust":       `assert_eq!(v.chunk.as_ref().unwrap().data,vec![0,255,10]);`,
					}[language]
				}
				if tc.name == "SecretReader" {
					binaryCheck = map[string]string{
						"go":         `if i==0&&string(v.Value.Secret)!="fixture"{panic("binary changed")};`,
						"python":     `if i==0: assert v.value.secret==b"fixture"`,
						"javascript": `if(i===0)assert.equal(new TextDecoder().decode(v.value.secret),"fixture");`,
						"cpp":        `if(i==0)assert((v.value->secret==std::vector<std::uint8_t>{'f','i','x','t','u','r','e'}));`,
						"rust":       `assert_eq!(v.value.as_ref().unwrap().secret,b"fixture");`,
					}[language]
				}
				if language == "python" && binaryCheck == "" {
					binaryCheck = "pass"
				}
				if tc.name == "ConfigObserver" {
					code = strings.NewReplacer(
						"post.test/abstraction/model/api", "post.test/abstraction/config",
						"from abstraction.model import api as r", "import abstraction.config as r",
						"./js/abstraction/model/api/index.mjs", "./js/abstraction/config/index.mjs",
						"abstraction/model/api/rec.h", "abstraction/config/rec.h",
						"abstraction::model::api", "abstraction::config",
						"use model_api::*", "use config_api::*",
						"Resolve(r.Ref)", "Observe(r.RunOverrides,string,int64)",
						"Resolve(r.Ref{})", `Observe(r.RunOverrides{},"c1",0)`,
						"resolve(const r::Ref&)", "observe(const r::RunOverrides&,const std::string&,const std::int64_t&)",
						"resolve(r::Ref{})", `observe(r::RunOverrides{},"c1",0)`,
						"resolve(r.Ref())", `observe(r.RunOverrides(),"c1",0)`,
						"resolve(r.newRef())", `observe(r.newRunOverrides(),"c1",0n)`,
						"resolve(Ref::default())", `observe(RunOverrides::default(),"c1".into(),0)`,
						`"Resolve"`, `"Observe"`).Replace(code)
				}
				if tc.name == "QuestionApplication" || tc.name == "PermissionApplication" {
					goParams, cppParams, callArgs, rustArgs := "r.ApplicationQuestion", "const r::ApplicationQuestion&", "r.ApplicationQuestion{}", "ApplicationQuestion::default()"
					if language == "python" {
						callArgs = "r.ApplicationQuestion()"
					}
					if language == "javascript" {
						callArgs = "r.newApplicationQuestion()"
					}
					if language == "cpp" {
						callArgs = "r::ApplicationQuestion{}"
					}
					if tc.method == "Observe" {
						goParams, cppParams = "string,int64", "const std::string&,const std::int64_t&"
						callArgs, rustArgs = `"kept-request",0`, `"kept-request".into(),0`
						if language == "javascript" {
							callArgs = `"kept-request",0n`
						}
					}
					code = strings.NewReplacer(
						"post.test/abstraction/model/api", "post.test/abstraction/asks/api",
						"from abstraction.model import api as r", "from abstraction.asks import api as r",
						"./js/abstraction/model/api/index.mjs", "./js/abstraction/asks/api/index.mjs",
						"abstraction/model/api/rec.h", "abstraction/asks/api/rec.h",
						"abstraction::model::api", "abstraction::asks::api",
						"use model_api::*", "use asks_api::*",
						"Resolve(r.Ref)", tc.method+"("+goParams+")",
						"Resolve(r.Ref{})", tc.method+"("+callArgs+")",
						"resolve(const r::Ref&)", snakeName(tc.method)+"("+cppParams+")",
						"resolve(r::Ref{})", snakeName(tc.method)+"("+callArgs+")",
						"resolve(r.Ref())", snakeName(tc.method)+"("+callArgs+")",
						"resolve(r.newRef())", snakeName(tc.method)+"("+callArgs+")",
						"resolve(Ref::default())", snakeName(tc.method)+"("+rustArgs+")",
						`"Resolve"`, fmt.Sprintf("%q", tc.method)).Replace(code)
					other, goOther, cppOther := "Observe", "string,int64", "const std::string&,const std::int64_t&"
					if tc.method == "Observe" {
						other, goOther, cppOther = "Ask", "r.ApplicationQuestion", "const r::ApplicationQuestion&"
					}
					if language == "go" {
						extraHandlers = fmt.Sprintf("func(handler)%s(%s)(r.QuestionObservation,error){return r.QuestionObservation{},nil}", other, goOther)
					}
					if language == "cpp" {
						extraHandlers = fmt.Sprintf("r::QuestionObservation %s(%s)override{return {};}", snakeName(other), cppOther)
					}
					if tc.name == "PermissionApplication" {
						code = strings.NewReplacer("ApplicationQuestion", "PermissionIntent", "QuestionObservation", "PermissionObservation").Replace(code)
						extraHandlers = strings.NewReplacer("ApplicationQuestion", "PermissionIntent", "QuestionObservation", "PermissionObservation", "Ask(", "Request(", "ask(", "request(").Replace(extraHandlers)
						if language == "go" {
							extraHandlers += "\nfunc(handler)ObserveQuestion(string,int64)(r.PendingQuestion,error){return r.PendingQuestion{},nil}"
						}
						if language == "cpp" {
							extraHandlers += "\nr::PendingQuestion observe_question(const std::string&,const std::int64_t&)override{return {};}"
						}
					}
				}
				if tc.name == "ScopedHolder" || tc.name == "SecretReader" {
					code = strings.NewReplacer(
						"post.test/abstraction/model/api", "post.test/abstraction/credentials/api",
						"from abstraction.model import api as r", "from abstraction.credentials import api as r",
						"./js/abstraction/model/api/index.mjs", "./js/abstraction/credentials/api/index.mjs",
						"abstraction/model/api/rec.h", "abstraction/credentials/api/rec.h",
						"abstraction::model::api", "abstraction::credentials::api",
						"use model_api::*", "use credentials_api::*",
						"Resolve(r.Ref)", tc.method+"(string)", "Resolve(r.Ref{})", tc.method+`("opaque-metadata")`,
						"resolve(const r::Ref&)", snakeName(tc.method)+"(const std::string&)", "resolve(r::Ref{})", snakeName(tc.method)+`("opaque-metadata")`,
						"resolve(r.Ref())", snakeName(tc.method)+`("opaque-metadata")`, "resolve(r.newRef())", snakeName(tc.method)+`("opaque-metadata")`,
						"resolve(Ref::default())", snakeName(tc.method)+`("opaque-metadata".into())`, `"Resolve"`, fmt.Sprintf("%q", tc.method)).Replace(code)
					if tc.name == "SecretReader" {
						code = strings.ReplaceAll(code, "opaque-metadata", "opaque-secret")
					}
					methods := []struct{ name, goParams, cppParams string }{{"Store", "string,r.Registration", "const std::string&,const r::Registration&"}, {"Rotate", "string,r.Rotation", "const std::string&,const r::Rotation&"}, {"Revoke", "string,string", "const std::string&,const std::string&"}}
					if tc.name == "SecretReader" {
						methods = nil
					}
					for _, method := range methods {
						if language == "go" {
							extraHandlers += fmt.Sprintf("func(handler)%s(%s)(r.%sResult,error){return r.%sResult{},nil};\n", method.name, method.goParams, method.name, method.name)
						}
						if language == "cpp" {
							extraHandlers += fmt.Sprintf("r::%sResult %s(%s)override{return {};}", method.name, snakeName(method.name), method.cppParams)
						}
					}
				}
				if tc.name == "ContentReader" || tc.name == "ContentWriter" {
					arg := fmt.Sprintf("%q", "sha256:"+strings.Repeat("a", 64))
					goParams, cppParams, callArgs, rustArgs := "string", "const std::string&", arg, arg+".into()"
					if tc.method == "Read" {
						goParams = "string,int64,int64"
						cppParams = "const std::string&,const std::int64_t&,const std::int64_t&"
						callArgs = `"opaque-handle",0,3`
						rustArgs = `"opaque-handle".into(),0,3`
					}
					if tc.method == "Begin" {
						goParams, cppParams = "string,string,int64", "const std::string&,const std::string&,const std::int64_t&"
						callArgs = `"opaque-request-0001",` + arg + `,3`
						rustArgs = `"opaque-request-0001".into(),` + arg + `.into(),3`
					}
					if tc.method == "Commit" {
						callArgs, rustArgs = `"opaque-upload"`, `"opaque-upload".into()`
					}
					if language == "javascript" && tc.method == "Read" {
						callArgs = `"opaque-handle",0n,3n`
					}
					if language == "javascript" && tc.method == "Begin" {
						callArgs = `"opaque-request-0001",` + arg + `,3n`
					}
					code = strings.NewReplacer(
						"post.test/abstraction/model/api", "post.test/abstraction/storage/content",
						"from abstraction.model import api as r", "import abstraction.storage.content as r",
						"./js/abstraction/model/api/index.mjs", "./js/abstraction/storage/content/index.mjs",
						"abstraction/model/api/rec.h", "abstraction/storage/content/rec.h",
						"abstraction::model::api", "abstraction::storage::content",
						"use model_api::*", "use content_api::*",
						"Resolve(r.Ref)", tc.method+"("+goParams+")",
						"Resolve(r.Ref{})", tc.method+"("+callArgs+")",
						"resolve(const r::Ref&)", snakeName(tc.method)+"("+cppParams+")",
						"resolve(r::Ref{})", snakeName(tc.method)+"("+callArgs+")",
						"resolve(r.Ref())", snakeName(tc.method)+"("+callArgs+")",
						"resolve(r.newRef())", snakeName(tc.method)+"("+callArgs+")",
						"resolve(Ref::default())", snakeName(tc.method)+"("+rustArgs+")",
						`"Resolve"`, fmt.Sprintf("%q", tc.method)).Replace(code)
					if language == "go" {
						extraHandlers = `func(handler)Close(string)(r.CloseResult,error){return r.CloseResult{Outcome:r.CloseOutcomeClosed},nil}`
						if tc.method == "Open" {
							extraHandlers += ";\n" + `func(handler)Read(string,int64,int64)(r.ReadResult,error){return r.ReadResult{Outcome:r.ReadOutcomeUnavailable},nil}`
						} else {
							extraHandlers += ";\n" + `func(handler)Open(string)(r.OpenResult,error){return r.OpenResult{Outcome:r.OpenOutcomeUnavailable},nil}`
						}
					}
					if language == "cpp" {
						extraHandlers = `r::CloseResult close(const std::string&)override{return {r::CloseOutcome::Closed};}`
						if tc.method == "Open" {
							extraHandlers += `r::ReadResult read(const std::string&,const std::int64_t&,const std::int64_t&)override{return {r::ReadOutcome::Unavailable,{}};}`
						} else {
							extraHandlers += `r::OpenResult open(const std::string&)override{return {r::OpenOutcome::Unavailable,{}};}`
						}
					}
					if tc.name == "ContentWriter" {
						extraHandlers = ""
						for _, method := range []struct{ name, goParams, cppParams string }{
							{"Begin", "string,string,int64", "const std::string&,const std::string&,const std::int64_t&"},
							{"Append", "string,int64,[]byte", "const std::string&,const std::int64_t&,const std::vector<std::uint8_t>&"},
							{"Commit", "string", "const std::string&"},
							{"Abort", "string", "const std::string&"},
						} {
							if method.name == tc.method {
								continue
							}
							if language == "go" {
								extraHandlers += fmt.Sprintf("func(handler)%s(%s)(r.%sResult,error){return r.%sResult{},nil};\n", method.name, method.goParams, method.name, method.name)
							}
							if language == "cpp" {
								extraHandlers += fmt.Sprintf("r::%sResult %s(%s)override{return {};}", method.name, snakeName(method.name), method.cppParams)
							}
						}
					}
				}
				if language == "go" {
					field = exported(field)
				}
				code = strings.NewReplacer("i<2", fmt.Sprintf("i<%d", goodCount), "i>=2", fmt.Sprintf("i>=%d", goodCount), "i==0", fmt.Sprintf("i<%d", presentCount), "i===0", fmt.Sprintf("i<%d", presentCount), "i<4", fmt.Sprintf("i<%d", len(pairs))).Replace(code)
				return strings.NewReplacer("BINARY_CHECK", binaryCheck, "EXTRA_HANDLERS", extraHandlers, "REPLIES", quotedList(replies), "RECORDS", quotedList(records), "RECORD_SNAKE", snakeName(tc.record), "RECORD", tc.record, "REQUEST", fmt.Sprintf("%q", request), "SERVICE_WIRE", fmt.Sprintf("%q", tc.service), "SERVICE", tc.name, "FIELD", field, "MARKER", fmt.Sprintf("%q", marker)).Replace(code)
			}

			t.Run("go", func(t *testing.T) {
				dir := filepath.Join(out, "go")
				writeNamespaceFile(t, dir, "go.mod", "module post.test\n\ngo 1.26\n")
				code := `package main
import("errors";"strings";"encoding/json";r "post.test/abstraction/model/api")
type transport struct{raw string};func(t transport)ExchangeFrame([]byte)([]byte,error){return []byte(t.raw),nil}
type handler struct{v r.RECORD};func(h handler)Resolve(r.Ref)(r.RECORD,error){return h.v,nil};EXTRA_HANDLERS
func main(){for i,raw:=range []string{REPLIES}{v,e:=r.NewSERVICEClient(transport{raw}).Resolve(r.Ref{});if i<2{if e!=nil{panic(e)};if (v.FIELD!=nil)!=(i==0){panic("presence")};if i==0&&!strings.Contains(string(r.EncodeRECORD(&v)),MARKER){panic("opaque value changed")};BINARY_CHECK}else{var s *r.ServiceError;if !errors.As(e,&s)||s.Code!=r.ServiceErrorCodeInvalidResult||v.FIELD!=nil{panic("malformed result exposed")}}};for i,raw:=range []string{RECORDS}{v,e:=r.DecodeRECORD([]byte(raw));if e!=nil{panic(e)};d:=r.SERVICEDispatcher{Handler:handler{*v}};frame,e:=d.ExchangeFrame([]byte(REQUEST));if e!=nil{panic(e)};var envelope struct{Ok bool;Payload struct{Code string;Value json.RawMessage}};if e=json.Unmarshal(frame,&envelope);e!=nil{panic(e)};if envelope.Ok!=(i<2)||(i>=2&&(envelope.Payload.Code!="invalid_result"||len(envelope.Payload.Value)!=0)){panic("dispatcher exposed invalid reply")};_,e=r.NewSERVICEClient(transport{string(frame)}).Resolve(r.Ref{});if(i<2)!=(e==nil){panic("dispatcher postcondition")}}}
`
				code = render(code, "go")
				writeNamespaceFile(t, dir, "main.go", code)
				execute(t, dir, "go", "run", ".")
			})
			t.Run("python", func(t *testing.T) {
				code := `from abstraction.model import api as r
class Transport:
    def __init__(self,raw): self.raw=raw
    def exchange_frame(self,_): return self.raw.encode()
for i,raw in enumerate([REPLIES]):
    try:
        v=r.SERVICEClient(Transport(raw)).resolve(r.Ref())
        assert i<2 and (v.FIELD is not None)==(i==0)
        if i==0: assert MARKER.encode() in r.encode_RECORD_SNAKE(v)
        BINARY_CHECK
    except r.ServiceError as e:
        assert i>=2 and e.code=='invalid_result'
`
				code = render(code, "python")
				writeNamespaceFile(t, out, "model.py", code)
				execute(t, out, servicePython(t), "model.py")
			})
			t.Run("javascript", func(t *testing.T) {
				code := `import assert from 'node:assert/strict';import * as r from './js/abstraction/model/api/index.mjs';for(const [i,raw]of[REPLIES].entries()){const c=new r.SERVICEClient({exchangeFrame:async()=>new TextEncoder().encode(raw)});if(i<2){const v=await c.resolve(r.newRef());assert.equal(v.FIELD!=null,i===0);if(i===0)assert.ok(new TextDecoder().decode(r.encodeRECORD(v)).includes(MARKER));BINARY_CHECK}else await assert.rejects(c.resolve(r.newRef()),e=>e instanceof r.ServiceError&&e.code==='invalid_result');}`
				code = render(code, "javascript")
				writeNamespaceFile(t, out, "model.mjs", code)
				execute(t, out, "node", "model.mjs")
			})
			t.Run("cpp", func(t *testing.T) {
				code := `#include <abstraction/model/api/rec.h>
#include <cassert>
namespace r=abstraction::model::api;struct Transport{std::string raw;std::string exchange_frame(std::string_view){return raw;}};struct Handler:r::SERVICE{r::RECORD v;r::RECORD resolve(const r::Ref&)override{return v;}EXTRA_HANDLERS};
int main(){const char* replies[]={REPLIES};for(int i=0;i<4;i++){Transport f{replies[i]};r::SERVICEClient<Transport> c(f);try{auto v=c.resolve(r::Ref{});assert(i<2&&v.FIELD.has_value()==(i==0));if(i==0)assert(r::to_json(v).find(MARKER)!=std::string::npos);BINARY_CHECK}catch(const r::ServiceError&e){assert(i>=2&&e.code=="invalid_result");}}const char* records[]={RECORDS};for(int i=0;i<4;i++){Handler h;h.v=r::decode_RECORD_SNAKE(records[i]);r::SERVICEDispatcher d(h);auto raw=d.exchange_frame(REQUEST);try{r::detail::service_response(raw,SERVICE_WIRE,"Resolve");assert(i<2);}catch(const r::ServiceError&e){assert(i>=2&&e.code=="invalid_result");}}}`
				code = render(code, "cpp")
				writeNamespaceFile(t, out, "model.cpp", code)
				execute(t, out, "g++", "-std=c++17", "-I", filepath.Join(out, "cpp"), "model.cpp", "-o", "model.exe")
				execute(t, out, filepath.Join(out, "model.exe"))
			})
			t.Run("rust", func(t *testing.T) {
				compiler := rustServiceCompiler(t)
				crate, source := "model_api", "rs/abstraction/model/api/rec.rs"
				if tc.name == "ConfigObserver" {
					crate, source = "config_api", "rs/abstraction/config/rec.rs"
				}
				if tc.name == "QuestionApplication" || tc.name == "PermissionApplication" {
					crate, source = "asks_api", "rs/abstraction/asks/api/rec.rs"
				}
				if tc.name == "ScopedHolder" || tc.name == "SecretReader" {
					crate, source = "credentials_api", "rs/abstraction/credentials/api/rec.rs"
				}
				buildRustLibrary(t, compiler, "request_api", "rs/abstraction/download/request/rec.rs")
				buildRustLibrary(t, compiler, "content_api", "rs/abstraction/storage/content/rec.rs")
				buildRustLibrary(t, compiler, "rights_api", "rs/abstraction/rights/api/rec.rs")
				buildRustLibrary(t, compiler, crate, source, "--extern", "request_api=librequest_api.rlib",
					"--extern", "content_api=libcontent_api.rlib", "--extern", "rights_api=librights_api.rlib")
				code := `use model_api::*;struct Transport{raw:&'static str}impl FrameTransport for Transport{type Error=();fn write_frame(&self,_:&[u8])->Result<(),()>{Ok(())}fn exchange_frame(&self,_:&[u8])->Result<Vec<u8>,()>{Ok(self.raw.as_bytes().to_vec())}}fn main(){for(i,raw)in[REPLIES].iter().enumerate(){match SERVICEClient::new(Transport{raw}).resolve(Ref::default()){Ok(v)=>{assert!(i<2&&v.FIELD.is_some()==(i==0));if i==0{assert!(String::from_utf8(encode_RECORD_SNAKE_document(&v).unwrap()).unwrap().contains(MARKER));BINARY_CHECK}},Err(CallError::Service(e))=>assert!(i>=2&&e.code=="invalid_result"),_=>panic!("wrong result")}}}`
				code = render(code, "rust")
				writeNamespaceFile(t, out, "model.rs", code)
				execute(t, out, compiler, "--edition=2021", "model.rs", "--extern", crate+"=lib"+crate+".rlib", "--extern", "content_api=libcontent_api.rlib", "-L", out, "-o", "model-rs.exe")
				execute(t, out, filepath.Join(out, "model-rs.exe"))
			})
		})
	}
}

const postFixture = compatHead + `enum Outcome {1:ok 2:no}(unknown="refuse",reader="act")
struct Data {1:required bool value}(unknown_fields="refuse")
struct Reply {1:required Outcome outcome 2:optional Data data(omit="absent")}(document="true",unknown_fields="refuse")
service Query {Reply Read()(result_present="data",result_when="outcome",result_is="ok")}(wire_name="test/query@1")`

func TestResultPostconditionNonWireAndTyped(t *testing.T) {
	plain := strings.Replace(postFixture, `(result_present="data",result_when="outcome",result_is="ok")`, "", 1)
	before, after := mustParse(t, plain), mustParse(t, postFixture)
	if fmt.Sprint(definitionHashes(before)) != fmt.Sprint(definitionHashes(after)) || len(compatibility(before, after)) != 0 || len(compatibility(after, before)) != 0 {
		t.Fatal("non-wire metadata changed wire hash/compatibility")
	}
	if after.Services[0].Methods[0].ResultPostcondition == nil {
		t.Fatal("postcondition not lowered")
	}
	for _, change := range [][2]string{{`result_is="ok"`, `result_is="unknown"`}, {`result_when="outcome"`, `result_when="data"`}, {`result_present="data"`, `result_present="outcome"`}, {`result_is="ok"`, `result_is=""`}, {`,result_is="ok"`, ""}, {`required Outcome outcome`, `optional Outcome outcome(omit="absent")`}, {`optional Data data(omit="absent")`, `required Data data`}} {
		if _, err := parse(strings.Replace(postFixture, change[0], change[1], 1)); err == nil {
			t.Fatal("invalid postcondition accepted", change)
		}
	}
}

func TestResourceResultPostconditionCoverage(t *testing.T) {
	path := productionFile(t, "abstraction-resource/resource.thrift")
	d, err := loadDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Resources": "list:ok", "Holders": "state:ok", "AcquireFor": "lease:acquired", "Acquire": "lease:acquired", "AcquireFree": "lease:acquired", "Renew": "change:ok", "Release": "change:ok", "Label": "change:ok", "Observe": "page:ok", "Answer": "answer:ok"}
	plain := string(raw)
	for _, svc := range d.Services {
		for _, m := range svc.Methods {
			expected, ok := want[m.Name]
			if !ok {
				if m.ResultPostcondition != nil {
					t.Fatal("unexpected relation", m.Name)
				}
				continue
			}
			p := m.ResultPostcondition
			if p == nil || p.Payload.Name+":"+strings.Join(p.Members, ",") != expected || p.Discriminator.Name != "outcome" {
				t.Fatal("missing/wrong generated result relation", m.Name, p)
			}
			plain = strings.Replace(plain, fmt.Sprintf("result_present=%q,result_when=%q,result_is=%q,", m.ResultPresent, m.ResultWhen, m.ResultIs), "", 1)
			delete(want, m.Name)
		}
	}
	if len(want) != 0 {
		t.Fatal("missing methods", want)
	}
	before, err := parseWithIncludes(plain, func(name string) (*Definition, error) { return loadDefinition(filepath.Join(filepath.Dir(path), name)) })
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(definitionHashes(before)) != fmt.Sprint(definitionHashes(d)) || len(compatibility(before, d)) != 0 || len(compatibility(d, before)) != 0 {
		t.Fatal("Resource wire identity changed")
	}
}

// One truth table is fed through every actual generated RPC client. Standalone
// codecs remain structural: the bad results can be encoded for this falsifier.
func TestGeneratedResultPostconditionAllLanguages(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		generatedResultPostconditionCorpus(t, postFixture, []string{
			`{"outcome":"ok","data":{"value":false}}`, `{"outcome":"no"}`,
			`{"outcome":"ok"}`, `{"outcome":"no","data":{"value":false}}`,
		}, 2)
	})
	t.Run("members", func(t *testing.T) {
		fixture := strings.Replace(postFixture, "2:no", "2:no 3:also 4:gone 5:invalid 6:conflict 7:forbidden 8:unavailable", 1)
		fixture = strings.Replace(fixture, `result_is="ok"`, `result_is="ok,also"`, 1)
		var records []string
		for _, valid := range []bool{true, false} {
			for _, outcome := range []string{"ok", "also", "no", "gone", "invalid", "conflict", "forbidden", "unavailable"} {
				record := `{"outcome":"` + outcome + `"`
				if (outcome == "ok" || outcome == "also") == valid {
					record += `,"data":{"value":false}`
				}
				records = append(records, record+`}`)
			}
		}
		generatedResultPostconditionCorpus(t, fixture, records, 8)
	})
}

func TestGeneratedOpenResultPostconditionAllLanguages(t *testing.T) {
	fixture := strings.Replace(postFixture, `unknown="refuse",reader="act"`, `unknown="grant",reader="validate"`, 1)
	if fixture == postFixture {
		t.Fatal("open enum fixture marker missing")
	}
	generatedResultPostconditionCorpus(t, fixture, []string{`{"outcome":"ok","data":{"value":false}}`, `{"outcome":"future_word"}`, `{"outcome":"ok"}`, `{"outcome":"future_word","data":{"value":false}}`}, 2)
}

func generatedResultPostconditionCorpus(t *testing.T, fixture string, records []string, goodCount int) {
	t.Helper()
	d := mustParse(t, fixture)
	var quotedReplies, quotedRecords []string
	for _, record := range records {
		quotedRecords = append(quotedRecords, fmt.Sprintf("%q", record))
		quotedReplies = append(quotedReplies, fmt.Sprintf("%q", `{"version":1,"service":"test/query@1","method":"Read","ok":true,"payload":{"value":`+record+`}}`))
	}
	render := func(code string) string {
		return strings.NewReplacer("i<2", fmt.Sprintf("i<%d", goodCount), "i>=2", fmt.Sprintf("i>=%d", goodCount), "i<4", fmt.Sprintf("i<%d", len(records)), "RECORDS", strings.Join(quotedRecords, ",")).Replace(code)
	}
	run := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off")
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v: %v\n%s", args, e, out)
		}
	}
	t.Run("go", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "go.mod", "module post.test\n\ngo 1.26\n")
		writeNamespaceFile(t, dir, "rec.go", genGo(d))
		writeNamespaceFile(t, dir, "rec_test.go", render(fmt.Sprintf(`package rec
import("testing";"errors")
type transport struct{raw string};func(f transport)ExchangeFrame([]byte)([]byte,error){return []byte(f.raw),nil}
type handler struct{r Reply};func(h handler)Read()(Reply,error){return h.r,nil}
func TestReceive(t *testing.T){for i,raw:=range []string{%s}{v,e:=NewQueryClient(transport{raw}).Read();if i<2{if e!=nil{t.Fatal(i,e)};if i==0&&(v.Data==nil||v.Data.Value){t.Fatal(v)}}else{var s *ServiceError;if !errors.As(e,&s)||s.Code!=ServiceErrorCodeInvalidResult{t.Fatal(i,e)};if v.Data!=nil{t.Fatal("malformed payload exposed with error")}}};for i,raw:=range []string{RECORDS}{r,e:=Decode([]byte(raw));if e!=nil{t.Fatal("standalone codec narrowed",e)};d:=&QueryDispatcher{Handler:handler{*r}};frame,e:=d.ExchangeFrame([]byte("{\"version\":1,\"service\":\"test/query@1\",\"method\":\"Read\",\"arguments\":{}}"));if e!=nil{t.Fatal(e)};_,e=serviceResponse(frame,"test/query@1","Read");if i<2{if e!=nil{t.Fatal(e)}}else{var s *ServiceError;if !errors.As(e,&s)||s.Code!=ServiceErrorCodeInvalidResult{t.Fatal("dispatcher did not produce error reply",e)}}}}
`, strings.Join(quotedReplies, ","))))
		run(t, dir, "go", "test", ".")
	})
	t.Run("python", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.py", genPy(d))
		writeNamespaceFile(t, dir, "test.py", render(fmt.Sprintf(`import rec
class Transport:
    def __init__(self,raw): self.raw=raw
    def exchange_frame(self,_): return self.raw.encode()
for i,raw in enumerate([%s]):
    try:
        result=rec.QueryClient(Transport(raw)).read()
        assert i<2
        if i==0: assert result.data.value is False
    except rec.ServiceError as e:
        assert i>=2 and e.code=='invalid_result'
`, strings.Join(quotedReplies, ","))))
		python := os.Getenv("PYTHON")
		if python == "" {
			t.Fatal("PYTHON missing")
		}
		run(t, dir, python, "test.py")
	})
	t.Run("javascript", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.mjs", genJS(d))
		writeNamespaceFile(t, dir, "test.mjs", render(fmt.Sprintf(`import assert from 'node:assert/strict';import * as r from './rec.mjs';
const replies=[%s];for(let i=0;i<4;i++){const c=new r.QueryClient({exchangeFrame:async()=>new TextEncoder().encode(replies[i])});if(i<2){const v=await c.read();if(i===0)assert.equal(v.data.value,false);}else await assert.rejects(c.read(),e=>e instanceof r.ServiceError&&e.code==='invalid_result');}
`, strings.Join(quotedReplies, ","))))
		run(t, dir, "node", "test.mjs")
	})
	t.Run("cpp", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.h", genCpp(d))
		writeNamespaceFile(t, dir, "test.cpp", render(fmt.Sprintf(`#include "rec.h"
#include <cassert>
namespace r=rec;struct Transport{std::string raw;std::string exchange_frame(std::string_view){return raw;}};struct Handler:r::Query{r::Reply value;r::Reply read() override{return value;}};
int main(){const char* replies[]={%s};for(int i=0;i<4;i++){Transport f{replies[i]};r::QueryClient<Transport> c(f);try{auto v=c.read();assert(i<2);if(i==0)assert(v.data&&!v.data->value);}catch(const r::ServiceError&e){assert(i>=2&&e.code=="invalid_result");}}const char* records[]={RECORDS};for(int i=0;i<4;i++){Handler h;h.value=r::decode(records[i]);r::QueryDispatcher dispatcher(h);auto response=dispatcher.exchange_frame(R"({"version":1,"service":"test/query@1","method":"Read","arguments":{}})");try{r::detail::service_response(response,"test/query@1","Read");assert(i<2);}catch(const r::ServiceError&e){assert(i>=2&&e.code=="invalid_result");}}}
`, strings.Join(quotedReplies, ","))))
		run(t, dir, "g++", "-std=c++17", "test.cpp", "-o", "probe.exe")
		run(t, dir, filepath.Join(dir, "probe.exe"))
	})
	t.Run("rust", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.rs", genRust(d))
		writeNamespaceFile(t, dir, "main.rs", render(fmt.Sprintf(`mod rec;use rec::*;struct Transport{raw:&'static str}impl FrameTransport for Transport{type Error=&'static str;fn write_frame(&self,_:&[u8])->std::result::Result<(),Self::Error>{Ok(())}fn exchange_frame(&self,_:&[u8])->std::result::Result<Vec<u8>,Self::Error>{Ok(self.raw.as_bytes().to_vec())}}
fn main(){for(i,raw)in [%s].iter().enumerate(){match QueryClient::new(Transport{raw}).read(){Ok(v)=>{assert!(i<2);if i==0{assert!(!v.data.unwrap().value);}},Err(CallError::Service(e))=>assert!(i>=2&&e.code=="invalid_result"),_=>panic!("wrong result")}}}
`, strings.Join(quotedReplies, ","))))
		run(t, dir, rustServiceCompiler(t), "--edition=2021", "main.rs", "-o", "probe.exe")
		run(t, dir, filepath.Join(dir, "probe.exe"))
	})
}
