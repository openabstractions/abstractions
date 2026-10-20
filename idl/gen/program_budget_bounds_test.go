package main

import (
	"fmt"
	"strings"
	"testing"
)

// Exercise the actual unpublished production declarations through all codecs.
func TestProgramBudgetBoundsFiveLanguages(t *testing.T) {
	d, err := loadDefinition(productionFile(t, "abstraction-inference/inference.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	d, err = selected(d, []string{"ProgramBudget", "ProgramBudgetEntry", "ProgramBudgetPage", "ProgramBudgetResult", "ProgramBudgetChange", "ProgramBudgetReadOutcome", "ProgramBudgetEditOutcome", "Spend"})
	if err != nil {
		t.Fatal(err)
	}
	d.Namespaces, d.Services = map[string]string{"*": "rec"}, nil
	d.NoIPC, d.NamedCodecs = true, true
	var lines, wants []string
	add := func(name, raw, want string) { lines = append(lines, name+"\t"+raw); wants = append(wants, want) }
	change := func(revision, credential, program string) string {
		return fmt.Sprintf(`{"expected_revision":%q,"credential":%q,"program":%q}`, revision, credential, program)
	}
	add("ProgramBudgetChange", change("", "", ""), "accept")
	add("ProgramBudgetChange", change(strings.Repeat("r", 128), strings.Repeat("c", 64), strings.Repeat("p", 4096)), "accept")
	add("ProgramBudgetChange", change(strings.Repeat("r", 129), "c", "p"), "bad_value")
	add("ProgramBudgetChange", change("r", strings.Repeat("c", 65), "p"), "bad_value")
	add("ProgramBudgetChange", change("r", "c", strings.Repeat("p", 4097)), "bad_value")
	spend := `{"day":"2026-11-04","tokens":0,"micros":0,"requests":0,"images":0,"audio_seconds":0,"characters":0}`
	entry := func(program string) string { return fmt.Sprintf(`{"program":%q,"spend":%s}`, program, spend) }
	add("ProgramBudgetEntry", entry("/a\t\u0085"), "accept")
	add("ProgramBudgetEntry", entry(""), "bad_value")
	add("ProgramBudgetEntry", entry(strings.Repeat("p", 4096)), "accept")
	add("ProgramBudgetEntry", entry(strings.Repeat("p", 4097)), "bad_value")
	page := func(entries, profiles string) string {
		return `{"outcome":"page","revision":"program-budgets-1","entries":[` + entries + `],"profiles":[` + profiles + `]}`
	}
	add("ProgramBudgetPage", page("", `"`+strings.Repeat("p", 256)+`"`), "accept")
	add("ProgramBudgetPage", page("", `"`+strings.Repeat("p", 257)+`"`), "bad_value")
	add("ProgramBudgetPage", page("", strings.TrimSuffix(strings.Repeat(`"chat",`, 16), ",")), "accept")
	add("ProgramBudgetPage", page("", strings.TrimSuffix(strings.Repeat(`"chat",`, 17), ",")), "bad_value")
	add("ProgramBudgetPage", page(strings.TrimSuffix(strings.Repeat(entry("/p")+",", 256), ","), ""), "accept")
	add("ProgramBudgetPage", page(strings.TrimSuffix(strings.Repeat(entry("/p")+",", 257), ","), ""), "bad_value")
	add("ProgramBudgetPage", page(strings.TrimSuffix(strings.Repeat(entry(strings.Repeat("p", 4096))+",", 256), ","), ""), "encoded_size")
	for _, name := range []string{"ProgramBudgetPage", "ProgramBudgetResult"} {
		raw := `{"outcome":"unavailable","revision":"","` + map[string]string{"ProgramBudgetPage": `entries":[],"profiles":[]`, "ProgramBudgetResult": `reason":""`}[name] + `}`
		add(name, raw, "accept")
		add(name, strings.Replace(raw, `"revision":""`, `"revision":"`+strings.Repeat("r", 129)+`"`, 1), "bad_value")
		add(name, strings.Replace(raw, `"revision":""`, `"revision":"a\tb"`, 1), "bad_value")
	}
	add("ProgramBudgetResult", `{"outcome":"invalid","revision":"","reason":"`+strings.Repeat("r", 256)+`"}`, "accept")
	add("ProgramBudgetResult", `{"outcome":"invalid","revision":"","reason":"`+strings.Repeat("r", 257)+`"}`, "bad_value")
	names := []string{"ProgramBudgetChange", "ProgramBudgetEntry", "ProgramBudgetPage", "ProgramBudgetResult"}
	goCases, pyCases, jsCases, rustCases, cppCases := "", "", "", "", ""
	for _, name := range names {
		snake := snakeName(name)
		goCases += fmt.Sprintf("case %q:_,e=r.Decode%s([]byte(raw));", name, name)
		pyCases += fmt.Sprintf("%q:rec.decode_%s,", name, snake)
		jsCases += fmt.Sprintf("%q:r.decode%s,", name, name)
		rustCases += fmt.Sprintf("%q=>rec::decode_%s_document(raw.as_bytes()).map(|_|()),", name, snake)
		cppCases += fmt.Sprintf("if(kind==%q)rec::decode_%s(raw);else ", name, snake)
	}
	files := map[string]string{
		"go.mod": "module bound.test\n\ngo 1.22\n", "cases.txt": strings.Join(lines, "\n") + "\n",
		"main.go":  strings.Replace(messagesObservationGo, `case "page":_,e=r.DecodePage([]byte(raw));case "inboxes":_,e=r.DecodeInboxPage([]byte(raw));case "send":_,e=r.DecodeSendRequest([]byte(raw))`, goCases, 1),
		"main.py":  strings.Replace(messagesObservationPython, `'page':rec.decode_page,'inboxes':rec.decode_inbox_page,'send':rec.decode_send_request`, pyCases, 1),
		"main.mjs": strings.Replace(messagesObservationJS, `page:r.decodePage,inboxes:r.decodeInboxPage,send:r.decodeSendRequest`, jsCases, 1),
		"main.rs":  strings.Replace(messagesObservationRust, `"page"=>rec::decode_page_document(raw.as_bytes()).map(|_|()),"inboxes"=>rec::decode_inbox_page_document(raw.as_bytes()).map(|_|()),_=>rec::decode_send_request_document(raw.as_bytes()).map(|_|())`, rustCases+`_=>panic!("unknown case")`, 1),
		"main.cpp": strings.Replace(messagesObservationCpp, `if(kind=="page")rec::decode_page(raw);else if(kind=="inboxes")rec::decode_inbox_page(raw);else rec::decode_send_request(raw);`, cppCases+`throw std::runtime_error("unknown case");`, 1),
	}
	dir := t.TempDir()
	// Decode a valid seed, then mutate the native value so writer guards are
	// exercised independently of decoder rejection in every backend.
	seed := page(entry("/p"), `"chat"`)
	files["main.go"] = strings.TrimSuffix(files["main.go"], "}") + fmt.Sprintf(`;for i:=0;i<5;i++{func(){defer func(){if e:=recover();e!=nil{fmt.Println(e.(*r.Refusal).Word)}}();p,e:=r.DecodeProgramBudgetPage([]byte(%q));if e!=nil{panic(e)};switch i{case 0:p.Entries=make([]r.ProgramBudgetEntry,257);for j:=range p.Entries{p.Entries[j].Program="/p"};case 1:p.Profiles=make([]string,17);for j:=range p.Profiles{p.Profiles[j]="chat"};case 2:p.Profiles=[]string{strings.Repeat("p",257)};case 3,4:n:=64;if i==3{n=4096};p.Entries=make([]r.ProgramBudgetEntry,256);for j:=range p.Entries{p.Entries[j].Program=strings.Repeat("p",n)}};r.EncodeProgramBudgetPage(p);fmt.Println("accept")}()}}`, seed)
	files["main.py"] += fmt.Sprintf(`
for i in range(5):
 try:
  p=rec.decode_program_budget_page(%q.encode())
  if i==0:p.entries=[rec.ProgramBudgetEntry(program='/p')]*257
  if i==1:p.profiles=['chat']*17
  if i==2:p.profiles=['p'*257]
  if i>=3:p.entries=[rec.ProgramBudgetEntry(program='p'*(4096 if i==3 else 64))]*256
  rec.encode_program_budget_page(p);print('accept')
 except rec.Refusal as e:print(e.word)
`, seed)
	files["main.mjs"] += fmt.Sprintf(`
for(let i=0;i<5;i++){try{const p=r.decodeProgramBudgetPage(Buffer.from(%q));if(i===0)p.entries=Array(257).fill({...p.entries[0]});if(i===1)p.profiles=Array(17).fill('chat');if(i===2)p.profiles=['p'.repeat(257)];if(i>=3)p.entries=Array(256).fill({...p.entries[0],program:'p'.repeat(i===3?4096:64)});r.encodeProgramBudgetPage(p);console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}`, seed)
	files["main.rs"] = strings.TrimSuffix(files["main.rs"], "}") + fmt.Sprintf(`std::panic::set_hook(Box::new(|_|{}));for i in 0..5{match std::panic::catch_unwind(||{let mut p=rec::decode_program_budget_page_document(%q.as_bytes()).unwrap();if i==0{p.entries=vec![p.entries[0].clone();257]};if i==1{p.profiles=vec!["chat".into();17]};if i==2{p.profiles=vec!["p".repeat(257)]};if i>=3{p.entries[0].program="p".repeat(if i==3{4096}else{64});p.entries=vec![p.entries[0].clone();256]};if let Err(e)=rec::encode_program_budget_page_document(&p){std::panic::panic_any(e)}}){Ok(_)=>println!("accept"),Err(e)=>println!("{}",e.downcast_ref::<rec::Refusal>().unwrap().word)}}}`, seed)
	files["main.cpp"] = strings.TrimSuffix(files["main.cpp"], "}") + fmt.Sprintf(`for(int i=0;i<5;i++){try{auto p=rec::decode_program_budget_page(%q);if(i==0)p.entries.resize(257,p.entries[0]);if(i==1)p.profiles.resize(17,"chat");if(i==2)p.profiles={std::string(257,'p')};if(i>=3){p.entries[0].program=std::string(i==3?4096:64,'p');p.entries.resize(256,p.entries[0]);}rec::encode_program_budget_page(p);std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}}`, seed)
	wants = append(wants, "bad_value", "bad_value", "bad_value", "encoded_size", "accept")
	for _, backend := range backends {
		path := map[string]string{"go": "rec/rec.go", "python": "rec.py", "javascript": "rec.mjs", "rust": "rec.rs", "cpp": "rec.h"}[backend.lang]
		if path != "" {
			files[path] = mustEmitIncluded(t, backend, d)
		}
	}
	for name, body := range files {
		writeNamespaceFile(t, dir, name, body)
	}
	runFiveConstraintPrograms(t, dir, strings.Join(wants, "\n")+"\n")
}
