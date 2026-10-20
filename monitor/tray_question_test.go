package main

import (
	"encoding/json"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

func firstUseFixture(id, program, action, resource string) askclient.RecordMetadata {
	return askclient.RecordMetadata{
		ID: id, Asker: "abstraction-runtime.exe", Key: asks.FirstUseKey, About: program,
		Text:  program + " wants to " + action + " on " + resource,
		Asked: "2026-09-30T09:00:00Z", Options: []string{"allow", "refuse", "never"},
	}
}

func TestPermissionInventoryQuestionLeadsWithBoundAppAndEffect(t *testing.T) {
	record := firstUseFixture("inventory", `C:\apps\opencode.exe`, "abstraction.router/inventory.read", "abstraction.router/inventory")
	view := trayQuestionView(record, trayPublisher{Trusted: true, Subject: "Recorded Signer", Description: "OpenCode"})
	if view.Permission.Question != "Allow OpenCode to see available models and servers?" {
		t.Fatal(view.Permission.Question)
	}
	visible := view.Permission.Question + view.Permission.Requester + view.Permission.Publisher + view.Permission.Content + view.Permission.Scope + view.Permission.Effects + view.Permission.Footer
	for _, generic := range []string{"Applies to this app", "ongoing", "Cancel or Escape", "must retry"} {
		if strings.Contains(visible, generic) {
			t.Fatal("generic explanation is outside Details", generic)
		}
	}
	for _, detail := range []string{"Applies to this app", "ongoing permission", "ongoing block", "Closing this window means Not now."} {
		if !strings.Contains(view.Permission.Details, detail) {
			t.Fatal("Details lost explanation", detail)
		}
	}
	if strings.Contains(visible, "Recorded Signer") || !strings.Contains(view.Permission.Details, "Publisher: Recorded Signer (verified)") || !strings.Contains(visible, "OpenCode") || view.Default != "refuse" || view.Dismiss != "refuse" {
		t.Fatal("requester, publisher or safe default changed", view)
	}
}

func TestPermissionWildcardTargetStaysInDetails(t *testing.T) {
	for _, target := range []struct{ action, resource string }{{"example/action", "bucket:*"}, {"abstraction.credentials/apply", "credential:*"}} {
		view := trayQuestionView(firstUseFixture("wildcard", `C:\apps\requester.exe`, target.action, target.resource), trayPublisher{})
		if !strings.Contains(view.Details, "Target: “"+target.resource+"”.") || strings.Contains(view.Content, target.resource) {
			t.Fatal("wildcard target left the details", view)
		}
	}
}

func TestPairedMessagePermissionShowsClaimsWithoutPublisherAuthority(t *testing.T) {
	peer := "remote:" + strings.Repeat("a", 64)
	record := firstUseFixture("remote", peer, "abstraction.facade/application.message", "app:receiver")
	record.Asker = "Paired computer \"Home PC\". Unverified program claim \"agent.exe\"."
	permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: peer, Action: "abstraction.facade/application.message", Resource: "app:receiver"}}}
	view := trayQuestionViewForPermission(record, trayPublisher{Trusted: true, Subject: "Must not transfer", Description: "Must not transfer"}, permission)
	if view.Permission == nil || !strings.Contains(view.Content, "Home PC") || !strings.Contains(view.Permission.Content, "Unverified program claim \"agent.exe\"") || strings.Contains(view.Content, "Must not transfer") || strings.Contains(view.Permission.Publisher, "Must not transfer") || view.Permission.RequesterProgram != "" {
		t.Fatal(view)
	}
	if !strings.Contains(view.Permission.Details, "paired computer to this receiver") {
		t.Fatal("display narrowed remote authority to the claim", view)
	}
	for _, exact := range []string{peer, "abstraction.facade/application.message", "app:receiver"} {
		if !strings.Contains(view.Details, exact) {
			t.Fatal("lost canonical target", view)
		}
	}
	// Display prose never chooses a remote permission for an ordinary target.
	permission.Targets[0].Program = `C:\apps\native.exe`
	ordinary := trayQuestionViewForPermission(record, trayPublisher{}, permission)
	if strings.Contains(ordinary.Content, "Home PC") || strings.Contains(ordinary.Permission.Content, "Unverified program claim") {
		t.Fatal("display selected authority", ordinary)
	}
}

// VISION 2026-10-31: the question allowing a paired computer to wake existing
// conversations names that computer and states the conversations' own access.
func TestPairedAttachPermissionNamesPairedComputer(t *testing.T) {
	peer := "remote:" + strings.Repeat("a", 64)
	action, resource := "abstraction.facade/application.message_start", "app:codex/start:"+strings.Repeat("b", 64)
	record := firstUseFixture("remote-start", peer, action, resource)
	record.Asker = "Paired computer \"Home PC\"."
	permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: peer, Action: action, Resource: resource}}}
	view := trayQuestionViewForPermission(record, trayPublisher{Trusted: true, Subject: "Must not transfer", Description: "Must not transfer"}, permission)
	if view.Permission == nil || !strings.Contains(view.Content, "Home PC") || strings.Contains(view.Content, "Must not transfer") || view.Permission.RequesterProgram != "" {
		t.Fatal(view)
	}
	if view.Permission.Question != "Allow Paired computer to give “codex” tasks in your “codex” chats?" || !strings.Contains(view.Permission.Details, "paired computer to this receiver") {
		t.Fatal("paired attach question", view)
	}
}

// Historical wording fixtures contain Windows paths even on another host.
// This display-only projection never reads or answers a production question;
// the presenter obtains validated permission details through the operator.
func trayQuestionView(record askclient.RecordMetadata, publisher trayPublisher) trayQuestionPresentation {
	program, action, resource, ok := asks.FirstUse(record.Key, record.About, record.Text)
	if !ok {
		return trayQuestionViewForPermission(record, publisher, nil)
	}
	primary := askclient.PermissionTarget{Account: "legacy-display", Program: program, Action: action, Resource: resource}
	permission := askclient.PermissionRequest{Targets: []askclient.PermissionTarget{primary}}
	if asks.PairedPermission(action, resource) {
		paired := primary
		paired.Action = asks.PermissionContentRead
		permission.Targets = append(permission.Targets, paired)
	}
	return trayQuestionViewForPermission(record, publisher, &permission)
}

// visibleText is what the prompt shows without the details expander.
func visibleText(p trayQuestionPresentation) string {
	return p.Title + "\n" + p.Instruction + "\n" + p.Content + "\n" + p.Footer
}

func TestTrayQuestionViewNamesApplicationPublisherAndPlainAction(t *testing.T) {
	program := `C:\apps\opencode\opencode.exe`
	record := firstUseFixture("0123456789abcdef0123456789abcdef", program, "abstraction.inference/complete", "host:lemonade")
	prompt := trayQuestionView(record, trayPublisher{Subject: "OpenAbstractions Test Signer", Trusted: true})
	if prompt.Title != "OpenAbstractions" || prompt.Instruction != "Allow opencode (not registered) to use AI models?" {
		t.Fatalf("prompt should be branded and name the application and action: %+v", prompt)
	}
	if prompt.Content != "" || !strings.Contains(prompt.Details, "Request: use AI models on the model server \u201clemonade\u201d.") ||
		!strings.Contains(prompt.Details, "Publisher: OpenAbstractions Test Signer (verified)") {
		t.Fatalf("a verified prompt has no body; the details name the resource and publisher: %+v", prompt)
	}
	visible := visibleText(prompt)
	for _, hidden := range []string{program, `\`, "/", record.ID, "abstraction.", "host:"} {
		if strings.Contains(visible, hidden) {
			t.Fatalf("visible prompt text shows %q: %q", hidden, visible)
		}
	}
	for _, exact := range []string{"Program: " + program, "Action: abstraction.inference/complete", "Resource: host:lemonade", "Asked: " + record.Asked, "Question: " + record.ID} {
		if !strings.Contains(prompt.Details, exact) {
			t.Fatalf("details should carry %q: %q", exact, prompt.Details)
		}
	}
	if !strings.Contains(prompt.Details, "ongoing permission") || !strings.Contains(prompt.Details, "ongoing block") || !strings.Contains(prompt.Details, "Not now saves no permission.") {
		t.Fatalf("prompt must explain the persistent effects of first-use answers: %q", prompt.Footer)
	}
	want := []trayQuestionChoice{{Label: "Allow", Option: "allow"}, {Label: "Not now", Option: "refuse"}, {Label: "Never allow", Option: "never"}}
	if len(prompt.Choices) != len(want) {
		t.Fatalf("choices = %+v", prompt.Choices)
	}
	for i := range want {
		if prompt.Choices[i] != want[i] {
			t.Fatalf("choice %d = %+v, want %+v", i, prompt.Choices[i], want[i])
		}
	}
}

func TestTrayQuestionViewSaysWhenPublisherIsUnverified(t *testing.T) {
	prompt := trayQuestionView(firstUseFixture("q1", `C:\tools\bun.exe`, "abstraction.model/lookup", "hf"), trayPublisher{})
	if prompt.Instruction != "Allow bun (not registered) to look up models?" || prompt.Content != "Publisher not verified." ||
		!strings.Contains(prompt.Details, "Request: look up models in the model registry \u201chf\u201d.") {
		t.Fatalf("unverified prompt = %+v", prompt)
	}
	unnamed := trayQuestionView(firstUseFixture("q1", `C:\tools\app.exe`, "abstraction.model/lookup", "hf"), trayPublisher{Trusted: true})
	if unnamed.Content != "" || !strings.Contains(unnamed.Details, "Publisher: verified; no name was recorded") {
		t.Fatalf("a verified signature whose signer name was not recorded must not read as unsigned: %+v", unnamed)
	}
}

// TestTrayQuestionViewNamesASignedProgramByItsDescription: a trusted program
// is named by its recorded signed description, and its path moves to the
// details beside its signed original file name (VISION 2026-10-01). The
// review's probe, powershell.exe copied as OllamaHelper.exe, reads as Windows
// PowerShell with the script-host sentence asks gives (ASK-A8). A signed
// program that is no script host adds no sentence, and a description beside
// an untrusted signature names nothing.
func TestTrayQuestionViewNamesASignedProgramByItsDescription(t *testing.T) {
	program := `C:\Windows\Temp\OllamaHelper.exe`
	powershell := trayPublisher{Subject: "Microsoft Windows", Trusted: true, Description: "Windows PowerShell", OriginalName: "PowerShell.EXE"}
	prompt := trayQuestionView(firstUseFixture("q8", program, "abstraction.inference/complete", "host:lemonade"), powershell)
	if prompt.Instruction != "Allow Windows PowerShell to use AI models?" || prompt.Content != "It runs scripts that aren't verified." ||
		!strings.Contains(prompt.Details, "Windows PowerShell runs scripts; its publisher covers Windows PowerShell and the script it runs is unverified.") ||
		!strings.Contains(prompt.Details, "Publisher: Microsoft Windows (verified)") {
		t.Fatalf("a signed script host must be named by its description with the script-host sentence: %+v", prompt)
	}
	if strings.Contains(visibleText(prompt), "OllamaHelper") || strings.Contains(visibleText(prompt), `\`) {
		t.Fatalf("the on-disk name stays behind the expander: %q", visibleText(prompt))
	}
	for _, exact := range []string{"Program: " + program, "Original file name: PowerShell.EXE"} {
		if !strings.Contains(prompt.Details, exact) {
			t.Fatalf("details should carry %q: %q", exact, prompt.Details)
		}
	}

	opencode := trayQuestionView(firstUseFixture("q9", `C:\apps\opencode.exe`, "abstraction.model/lookup", "hf"),
		trayPublisher{Subject: "Example Publisher", Trusted: true, Description: "OpenCode", OriginalName: "opencode.exe"})
	if opencode.Instruction != "Allow OpenCode to look up models?" || strings.Contains(opencode.Content, "runs scripts") {
		t.Fatalf("a signed program that is no script host: %+v", opencode)
	}

	long := strings.Repeat("Very Long Interpreter Name ", 4) // 108 runes
	capped := trayQuestionView(firstUseFixture("q11", `C:\apps\host.exe`, "abstraction.model/lookup", "hf"),
		trayPublisher{Subject: "Example Publisher", Trusted: true, Description: long, OriginalName: "python.exe"})
	shown := string([]rune(long)[:maxProgramName-1]) + "…"
	if capped.Instruction != "Allow "+shown+" to look up models?" ||
		!strings.Contains(capped.Details, shown+" runs scripts; its publisher covers "+shown+" and") ||
		strings.Contains(visibleText(capped), long) || !strings.Contains(capped.Details, "Description: "+long) {
		t.Fatalf("a long description is shortened to %d runes in visible text and kept whole in the details: %+v", maxProgramName, capped)
	}

	claimed := trayQuestionView(firstUseFixture("q10", program, "abstraction.model/lookup", "hf"),
		trayPublisher{Description: "Windows PowerShell", OriginalName: "PowerShell.EXE"})
	if claimed.Instruction != "Allow OllamaHelper (not registered) to look up models?" || strings.Contains(claimed.Content, "Windows PowerShell") ||
		strings.Contains(claimed.Details, "Original file name") {
		t.Fatalf("an untrusted description must name nothing: %+v", claimed)
	}
}

func TestTrayQuestionViewKeepsUnreadableResourceInDetails(t *testing.T) {
	resource := `D:\models\0123456789abcdef0123456789abcdef.gguf`
	prompt := trayQuestionView(firstUseFixture("q2", `C:\a\app.exe`, "abstraction.storage/content.read", resource), trayPublisher{})
	if strings.Contains(visibleText(prompt), "0123456789abcdef") || strings.Contains(visibleText(prompt), `D:\`) {
		t.Fatalf("a path or hash resource must stay behind the expander: %q", visibleText(prompt))
	}
	if prompt.Instruction != "Allow app (not registered) to read stored content?" || !strings.Contains(prompt.Details, "Resource: "+resource) {
		t.Fatalf("prompt = %+v", prompt)
	}
	job := trayQuestionView(firstUseFixture("q3", `C:\a\app.exe`, "abstraction.job/acceptance.submit", "abstraction.job/acceptance@1"), trayPublisher{})
	if job.Instruction != "Allow app (not registered) to submit background work?" || strings.Contains(visibleText(job), "@1") {
		t.Fatalf("job prompt = %+v", job)
	}
	// JOB-A13: submission is asked for per kind of job.
	for _, c := range []struct{ resource, instruction string }{
		{"job:inference", "Allow app (not registered) to run AI work in the background?"},
		{"job:download", "Allow app (not registered) to download in the background?"},
	} {
		kind := trayQuestionView(firstUseFixture("q4", `C:\a\app.exe`, "abstraction.job/acceptance.submit", c.resource), trayPublisher{})
		if kind.Instruction != c.instruction || strings.Contains(visibleText(kind), "job:") {
			t.Fatalf("%s prompt = %+v", c.resource, kind)
		}
	}
}

// INF-V4: the paired input-profile question reads as the capability it serves,
// in one short caption, and shows no resource or action id. The paired
// output question says that one Allow covers writing the app's results and
// reading them back.
func TestTrayQuestionViewWordsAPairedInputQuestion(t *testing.T) {
	for _, c := range []struct{ resource, instruction string }{
		{"input:audio", "Allow opencode (not registered) to send audio for transcription?"},
		{"input:image", "Allow opencode (not registered) to send images for editing, vision or video?"},
		{"output:image", "Allow opencode (not registered) to save the images it makes and read them back?"},
		{"output:speech", "Allow opencode (not registered) to save the speech it makes and read it back?"},
		{"output:video", "Allow opencode (not registered) to save the videos it makes and read them back?"},
	} {
		prompt := trayQuestionView(firstUseFixture("q7", `C:\a\opencode.exe`, "abstraction.storage/content.write", c.resource), trayPublisher{})
		if prompt.Instruction != c.instruction || !strings.Contains(prompt.Details, "Resource: "+c.resource) {
			t.Fatalf("%s prompt = %+v", c.resource, prompt)
		}
		for _, hidden := range []string{"input:", "output:", "content write", "abstraction."} {
			if strings.Contains(visibleText(prompt), hidden) {
				t.Fatalf("visible %s prompt shows %q: %q", c.resource, hidden, visibleText(prompt))
			}
		}
	}
}

func TestTrayQuestionViewWordsALendQuestion(t *testing.T) {
	prompt := trayQuestionView(firstUseFixture("q6", `C:\a\app.exe`, "abstraction.storage/lend", "engine:lmstudio"), trayPublisher{})
	if prompt.Instruction != "Allow app (not registered) to lend a model?" ||
		!strings.Contains(prompt.Details, "Request: lend a model to the engine \u201clmstudio\u201d.") ||
		!strings.Contains(prompt.Details, "Resource: engine:lmstudio") {
		t.Fatalf("lend prompt = %+v", prompt)
	}
	for _, hidden := range []string{"engine:", "storage lend", "abstraction."} {
		if strings.Contains(visibleText(prompt), hidden) {
			t.Fatalf("visible lend prompt shows %q: %q", hidden, visibleText(prompt))
		}
	}
	odd := trayQuestionView(firstUseFixture("q7", `C:\a\app.exe`, "abstraction.storage/lend", `engine:D:\engines\0123456789abcdef0123`), trayPublisher{})
	if !strings.Contains(odd.Details, "Request: lend a model to an engine.") || strings.Contains(visibleText(odd), `D:\engines`) {
		t.Fatalf("an unreadable engine name must stay behind the expander: %+v", odd)
	}
}

func TestTrayQuestionViewOtherQuestionsKeepServiceText(t *testing.T) {
	record := askclient.RecordMetadata{ID: "q4", Asker: "comfyui.exe", Key: "download.reach",
		Text: "comfyui.exe wants to fetch from huggingface.co", Options: []string{"allow", "once", "refuse", "never"}}
	prompt := trayQuestionView(record, trayPublisher{Subject: "ignored", Trusted: true})
	if prompt.Title != "OpenAbstractions" || prompt.Instruction != "comfyui wants to fetch from huggingface.co" || prompt.Content != "" || prompt.Footer != "" {
		t.Fatalf("prompt = %+v", prompt)
	}
	// The catalogue's popup words, from the retained slots (architect popup
	// ruling 2026-11-10, row 25).
	worded := trayQuestionViewForContent(record, trayPublisher{}, nil, "", &askclient.CatalogueContent{Slots: map[string]string{"host": "huggingface.co"}})
	labels := []string{}
	for _, choice := range worded.Choices {
		labels = append(labels, choice.Label+"="+choice.Option)
	}
	if worded.Instruction != "Allow comfyui to download from huggingface.co?" || worded.Content != "" || strings.Join(labels, ",") != "Allow=allow,Not now=refuse" || worded.Default != "refuse" || worded.Dismiss != "refuse" {
		t.Fatalf("worded prompt = %+v %v", worded, labels)
	}
}

func TestTrayCataloguePopupChoices(t *testing.T) {
	for _, tc := range []struct {
		key, choices, dismiss string
		options               []string
	}{
		{"content.replace", "once=Allow,not_now=Not now", "not_now", []string{"once", "never"}},
		{"download.reach", "refuse=Not now,allow=Allow", "refuse", []string{"refuse", "once", "allow", "never"}},
		{"opencode.project.edit", "once=Allow,refuse=Not now", "refuse", []string{"once", "refuse", "never"}},
		{"rights.register", "allow=Allow,refuse=Not now,never=Never allow", "refuse", []string{"allow", "refuse", "never"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			record := askclient.RecordMetadata{ID: "catalogue-choice", Asker: "Editor", Key: tc.key, Text: "Editor requests access.", Options: tc.options}
			view := trayQuestionView(record, trayPublisher{})
			if got := choiceLabels(view); got != tc.choices || view.Default != tc.dismiss || view.Dismiss != tc.dismiss {
				t.Fatalf("choices %q, default %q, dismiss %q", got, view.Default, view.Dismiss)
			}
		})
	}
}

func TestWindowsPermissionViewKeepsQualifiedScopeAndRecordedEffects(t *testing.T) {
	record := firstUseFixture("windows-permission", `C:\apps\requester.exe`, "abstraction.inference/complete", "host:lemonade")
	for _, publisher := range []trayPublisher{{}, {Trusted: true}, {Trusted: true, Subject: "Recorded Signer"}} {
		prompt := trayQuestionView(record, publisher)
		view := prompt.Permission
		if view == nil || view.Instruction != "Allow requester (not registered) to use AI models?" || !strings.Contains(view.Details, "Resource: host:lemonade") {
			t.Fatalf("typed permission must keep its resource in details: %+v", view)
		}
		if strings.Count(view.Instruction+view.Content, "use AI models") != 1 {
			t.Fatalf("visible request repeats its action: %+v", view)
		}
		for _, effect := range []string{"ongoing permission", "ongoing block", "Not now saves no permission."} {
			if strings.Contains(view.Content, effect) || !strings.Contains(view.Details, effect) {
				t.Fatalf("permission effect %q is not in the details alone: %+v", effect, view)
			}
		}
		if view.Details != prompt.Details || view.Footer != "" || prompt.Footer != "" || !strings.Contains(view.Details, "Closing this window means Not now.") ||
			view.Warning == nil || *view.Warning != "" || prompt.Default != "refuse" || prompt.Dismiss != "refuse" {
			t.Fatalf("exact details, full effects and safe dismissal must remain: %+v", prompt)
		}
		if publisher.Trusted && publisher.Subject != "" && (strings.Contains(view.Content, "Recorded Signer") || !strings.Contains(view.Details, "Publisher: Recorded Signer (verified)")) {
			t.Fatalf("a verified publisher belongs in the details: %+v", view)
		}
		if publisher.Trusted && publisher.Subject == "" && !strings.Contains(view.Details, "Publisher: verified; no name was recorded") {
			t.Fatalf("a verified unnamed signature must remain distinct: %+v", view)
		}
		if !publisher.Trusted && view.Content != "Publisher not verified." {
			t.Fatalf("unverified publisher must remain explicit: %+v", view)
		}
		frame, err := json.Marshal(prompt)
		if err != nil || strings.Contains(string(frame), `"Permission":`) || strings.Contains(string(frame), `"Warning":`) {
			t.Fatalf("Windows private metadata must preserve the native bridge frame shape: %s, %v", frame, err)
		}
	}
}

func TestWindowsPermissionStyleComesFromTypedRecordAndKeepsPairedScope(t *testing.T) {
	record := firstUseFixture("paired", `C:\apps\requester.exe`, "abstraction.storage/content.write", "input:image")
	prompt := trayQuestionView(record, trayPublisher{})
	if prompt.Permission == nil || !strings.Contains(prompt.Permission.Details, "also covers reading this app's content back") ||
		!strings.Contains(prompt.Permission.Details, "Action: abstraction.storage/content.read") {
		t.Fatalf("paired scope must remain visible and exact: %+v", prompt)
	}
	// The catalogue may use its common consent layout. Missing structured
	// permission facts retain neutral content and no permission styling.
	generic := trayQuestionViewForPermission(record, trayPublisher{Trusted: true}, nil)
	if generic.PermissionPrompt || generic.Level != promptLevelBlue || generic.Instruction != record.Text || generic.Content != "" || generic.Warning != "" {
		t.Fatalf("untyped text must retain the neutral projection: %+v", generic)
	}
}

// TestTrayQuestionNamesTheBoundAskerProgram: two programs with one file name
// ask; each prompt names its asker by the display name derived from the
// program and carries that program in its details (asks ASK-A2, ASK-O11).
func TestTrayQuestionNamesTheBoundAskerProgram(t *testing.T) {
	record := askclient.RecordMetadata{ID: "q", Asker: "opencode.exe", Key: "download.reach", Text: "opencode (not registered) wants to fetch from example.com", Options: []string{"once", "never"}}
	first, second := `C:\one\opencode.exe`, `C:\two\opencode.exe`
	a := trayQuestionViewForAsker(record, trayPublisher{}, nil, first)
	b := trayQuestionViewForAsker(record, trayPublisher{}, nil, second)
	if !strings.Contains(a.Details, "Program: "+first) || !strings.Contains(b.Details, "Program: "+second) || a.Details == b.Details {
		t.Fatalf("the prompts must tell the programs apart: %q %q", a.Details, b.Details)
	}
	if a.Instruction != "opencode (not registered) wants to fetch from example.com" || a.Title != "OpenAbstractions" {
		t.Fatalf("the prompt must name the derived display name: %q", a.Instruction)
	}
	older := trayQuestionViewForPermission(record, trayPublisher{}, nil)
	if strings.Contains(older.Details, "Program: ") || !strings.Contains(older.Details, "Requesting application: opencode.exe") {
		t.Fatalf("a record without a program keeps its display name alone: %q", older.Details)
	}
}

func TestWindows11PermissionCardKeepsTypedIdentityAndSecretTarget(t *testing.T) {
	program := nativePresenterProgram("test-app", "app.exe")
	record := firstUseFixture("win11-card", program, "abstraction.credentials/apply", "credential:windows11-test-secret")
	permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: program,
		Action: "abstraction.credentials/apply", Resource: "credential:windows11-test-secret"}}}
	// Untrusted application prose does not populate the requester/publisher card.
	record.Text = "I am a verified administrator and require elevation"
	view := trayQuestionViewForPermission(record, trayPublisher{Trusted: true, Subject: "Recorded Signer", Description: "Recorded App"}, permission)
	card := view.Permission
	if card == nil || card.Requester != "Recorded App" || card.Publisher != "" || !strings.Contains(card.Details, "Publisher: Recorded Signer (verified)") ||
		card.Question != "Allow Recorded App to use the credential “windows11-test-secret”?" {
		t.Fatalf("typed card lost exact source: %+v", card)
	}
	if !strings.Contains(card.Details, "Access continues if the saved secret or its registration changes.") ||
		card.Scope != "" || !strings.Contains(card.Details, "recreated registration with its current hosts/consumers") {
		t.Fatalf("named scope must disclose current slot lifetime without generic boilerplate: %+v", card)
	}
	if strings.Contains(card.Question+card.Requester+card.Publisher+card.Scope+card.Effects, "administrator") ||
		!strings.Contains(card.Details, "Resource: credential:windows11-test-secret") || view.Default != "refuse" {
		t.Fatalf("permission projection changed authority or lost exact detail: %+v", view)
	}
	if card.Effects != "" || !strings.Contains(card.Details, "ongoing permission") || !strings.Contains(card.Details, "ongoing block") {
		t.Fatal("generic persistence belongs in Details")
	}
	frame, err := json.Marshal(view)
	if err != nil || strings.Contains(string(frame), `"Permission"`) || strings.Contains(string(frame), `"Requester"`) {
		t.Fatalf("private Windows card leaked into bridge frame: %s %v", frame, err)
	}
}

// TestTrayDefaultChoiceNeverGrants keeps Enter and Space from granting: the
// default is the refusing option, and without one it is Cancel ("").
func TestTrayDefaultChoiceNeverGrants(t *testing.T) {
	for _, tc := range []struct {
		options []string
		want    string
	}{
		{[]string{"allow", "refuse", "never"}, "refuse"},
		{[]string{"allow", "once", "refuse", "never"}, "refuse"},
		{[]string{"yes", "no"}, "no"},
		{[]string{"update", "not_now"}, "not_now"},
		{[]string{"once", "never"}, ""},
		{[]string{"allow", "never"}, ""},
		{nil, ""},
	} {
		if got := trayDefaultChoice(tc.options); got != tc.want {
			t.Errorf("trayDefaultChoice(%v) = %q, want %q", tc.options, got, tc.want)
		}
	}
	prompt := trayQuestionView(firstUseFixture("q5", `C:\a\app.exe`, "abstraction.model/lookup", "hf"), trayPublisher{})
	if prompt.Default != "refuse" || prompt.Choices[0].Option != "allow" {
		t.Fatalf("first-use prompt must default to refuse while listing allow first: %+v", prompt)
	}
}

func TestTrayDialogTextMakesHiddenControlsVisible(t *testing.T) {
	got := trayDialogText("opencode.exe\u202eexe\x00\nline")
	if got != "opencode.exe\\u202Eexe\\u0000\nline" {
		t.Fatalf("unsafe text = %q", got)
	}
}
