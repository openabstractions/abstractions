//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
	"github.com/openabstractions/abstractions/monitor/win"
)

// Opt-in: OA_POPUP_STYLE_DIR=<folder> builds every question kind the Asks
// presenter shows on Windows from fixture records through the production
// view builders (trayQuestionViewForContent, trayTypedPermissionView,
// trayChoiceView, firstSpendPrompt) and trayWindowsQuestionPrompt, and
// writes each native prompt as <folder>/fixtures-before/<kind>.json with a
// plain summary in <folder>/views-before.md. The win package's
// TestPopupStyleRender draws them off screen. fixtures-after holds the same
// prompts with the smallest data-only changes that make each kind follow the
// recorded popup decisions; it is a proposal and
// changes no product code.
func TestPopupStyleFixtures(t *testing.T) {
	dir := os.Getenv("OA_POPUP_STYLE_DIR")
	if dir == "" {
		t.Skip("OA_POPUP_STYLE_DIR=<folder> writes the popup style fixtures")
	}
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })

	render := func(key string, slots map[string]string, asker string) string {
		q, ok := asks.Find(key)
		if !ok {
			t.Fatalf("catalogue key %s", key)
		}
		text, err := q.Render(asks.Ask{Asker: asker, Slots: slots})
		if err != nil {
			t.Fatalf("render %s: %v", key, err)
		}
		return text
	}
	asked := "2026-11-12T02:18:58.6774718Z"
	until := "2026-11-12T02:19:31Z"
	comfyConnector := `C:\Users\person\Documents\ComfyUI\custom_nodes\comfyui-openabstractions\comfyui-connector.exe`
	opencodeConnector := filepath.Join(root, "oa-opencode-integration.exe")
	if err := os.WriteFile(opencodeConnector, []byte("fixture connector"), 0o600); err != nil {
		t.Fatal(err)
	}
	python := `C:\Users\person\AppData\Local\Programs\Python\Python312\python.exe`
	opencode := `C:\Users\person\AppData\Local\Programs\opencode\opencode.exe`
	pythonPublisher := trayPublisher{Subject: "Python Software Foundation", Trusted: true, Description: "Python", OriginalName: "python.exe"}

	type kind struct {
		name, row, trigger string
		view               trayQuestionPresentation
		after              func(trayQuestionPresentation) trayQuestionPresentation
	}
	var kinds []kind
	add := func(name, row, trigger string, view trayQuestionPresentation, after func(trayQuestionPresentation) trayQuestionPresentation) {
		kinds = append(kinds, kind{name, row, trigger, view, after})
	}
	// identity fills the identity row of a consent popup that names an app
	// but shows none, by the rules already recorded: the app's display name,
	// the inline publisher line only when not verified (promptPublisherSummary,
	// ruling 2026-11-10 row 2) and none for OpenAbstractions' own programs
	// (ruling C 2026-11-08), and the app's own icon (permissionIconProgram).
	identity := func(name string, publisher trayPublisher, program string) func(trayQuestionPresentation) trayQuestionPresentation {
		return func(v trayQuestionPresentation) trayQuestionPresentation {
			if v.Permission == nil {
				return v
			}
			card := *v.Permission
			card.Requester = name
			if card.Publisher == "" && !(*programTitler)(nil).oaOwnProgram(program) {
				card.Publisher = promptPublisherSummary(publisher)
			}
			card.RequesterProgram = permissionIconProgram("", program, publisher)
			v.Permission = &card
			return v
		}
	}

	// Keep: the R31 record (title slot was the file stem before a15647c87),
	// the same record after that fix, and a signed app.
	keep := func(title string, publisher trayPublisher) trayQuestionPresentation {
		slots := map[string]string{"program": comfyConnector, "title": title}
		record := askclient.RecordMetadata{ID: "keep-q", Key: asks.SelfRegisteredKey, About: comfyConnector, Asked: asked,
			Text: render(asks.SelfRegisteredKey, slots, "openabstractions"), Options: []string{"keep", "remove"}}
		return trayQuestionViewForContent(record, publisher.forPrompt(nil), nil, "", &askclient.CatalogueContent{Slots: slots})
	}
	add("keep-unsigned-r31", "Keep (R31, before the title fix)", "Panel Connect without a matching expectation, or an action on a waiting app in Apps (AskConfirmation)",
		keep("comfyui-connector", trayPublisher{}), func(trayQuestionPresentation) trayQuestionPresentation {
			// After: the same record with a15647c87, whose title slot is the manifest title.
			return identity("ComfyUI", trayPublisher{}, comfyConnector)(keep("ComfyUI", trayPublisher{}))
		})
	add("keep-unsigned", "Keep, unsigned", "Panel Connect without a matching expectation, or an action on a waiting app in Apps (AskConfirmation)",
		keep("ComfyUI", trayPublisher{}), identity("ComfyUI", trayPublisher{}, comfyConnector))
	signed := trayPublisher{Subject: "Comfy Org", Trusted: true, Description: "ComfyUI", OriginalName: "ComfyUI.exe"}
	add("keep-signed", "Keep, signed", "as Keep, unsigned", keep("ComfyUI", signed), identity("ComfyUI", signed, `C:\Program Files\ComfyUI\ComfyUI.exe`))

	// First-use: the Python background download (script host), an unsigned
	// OpenCode using models, a verified app.
	firstUse := func(id, program, action, resource string, publisher trayPublisher) trayQuestionPresentation {
		record := askclient.RecordMetadata{ID: id, Asker: "abstraction-runtime.exe", Key: asks.FirstUseKey, About: program,
			Text: program + " wants to " + action + " on " + resource, Asked: asked, Options: []string{"allow", "refuse"}}
		permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "S-1-5-21-1000", Program: program, Action: action, Resource: resource}}}
		return trayQuestionViewForContent(record, publisher.forPrompt(nil), permission, "", nil)
	}
	add("first-use-python-download", "First use, script host (R31 Python download)", "an app's first call to an action it has no permission for (rights.first_use)",
		firstUse("fu-python", python, grants.JobSubmitAction, "job:download", pythonPublisher), nil)
	add("first-use-unsigned", "First use, unsigned app", "as above", firstUse("fu-opencode", opencode, "abstraction.inference/complete", "host:llama", trayPublisher{}), nil)
	add("first-use-verified", "First use, verified app", "as above",
		firstUse("fu-signed", `C:\Program Files\Krita\bin\krita.exe`, "abstraction.inference/complete", "host:llama", trayPublisher{Subject: "Krita Foundation", Trusted: true, Description: "Krita"}), nil)
	// Hosted spend (the provider key form is a native control and is left
	// out of the fixture).
	spend := func() trayQuestionPresentation {
		record := firstUseFixture("spend", opencode, "abstraction.credentials/apply", "credential:oa-panel-deepseek")
		permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "S-1-5-21-1000", Program: opencode, Action: "abstraction.credentials/apply", Resource: "credential:oa-panel-deepseek"}}}
		view := trayQuestionViewForPermission(record, trayPublisher{}.forPrompt(nil), permission)
		return firstSpendPrompt(context.Background(), view, permission, func(context.Context, string) (spendLimit, bool) {
			return spendLimit{Label: "DeepSeek", Micros: 5_000_000, Tokens: 1_000_000}, true
		})
	}
	add("hosted-spend", "Hosted account use", "an app's first use of a hosted provider account", spend(), nil)

	// Typed permissions: a message permit and a full-control secret bundle.
	msgRecord, msgOperator := messagePermissionPresenterFixture(t)
	add("message-send", "Message send permission (typed)", "an agent's first message to another agent (oa_messages_request_access)",
		trayTypedPermissionView(msgRecord, msgOperator.details, nil), nil)
	typedRecord, typedDetails := typedNativeFixture(t, true, true)
	add("typed-all-secrets", "Typed permission, all items (orange)", "an app's typed permission request over a whole collection",
		trayTypedPermissionView(typedRecord, typedDetails, nil), nil)

	// Command once: R31 "Allow OpenCode to open ComfyUI this once?". The
	// message fixture above points Setup's connector folder elsewhere.
	agentConnectorRoot = func() string { return root }
	cmdSlots := map[string]string{"program": opencodeConnector, "operation": "Open ComfyUI", "operationmore": "", "command": "open", "code": "dff7e50bb2dedea635e4", "until": until}
	cmdRecord := askclient.RecordMetadata{ID: "f1571bbf40" + strings.Repeat("0", 22), Key: asks.ApplicationCommandOnceKey, About: opencodeConnector, Asker: "oa-opencode-integration.exe",
		Asked: asked, Options: []string{"once", "refuse"}, Text: render(asks.ApplicationCommandOnceKey, cmdSlots, "oa-opencode-integration.exe")}
	add("command-once", "App command once (R31)", "an agent calls a commit-class app command (applications@3 Activate/Call)",
		trayQuestionViewForContent(cmdRecord, trayPublisher{}, nil, opencodeConnector, &askclient.CatalogueContent{Slots: cmdSlots}),
		func(v trayQuestionPresentation) trayQuestionPresentation {
			v = identity("OpenCode", trayPublisher{}, opencodeConnector)(v)
			v.Details = strings.Join([]string{"Requesting application: OpenCode", "Program: " + opencodeConnector, "Command: open",
				"Request: dff7e50bb2dedea635e4", "Expires: 12 Nov 2026 04:19", "Closing this window means Not now."}, "\n")
			v.Permission.Details = v.Details
			return v
		})

	// Agent enrollment and connection update.
	enrollSlots := map[string]string{"program": filepath.Join(root, "oa-codex-integration.exe"), "title": "Codex integration"}
	enrollRecord := askclient.RecordMetadata{ID: "enroll-q", Key: asks.AgentEnrollmentKey, About: enrollSlots["program"], Asked: asked,
		Text: render(asks.AgentEnrollmentKey, enrollSlots, "openabstractions"), Options: []string{"keep", "refuse"}}
	add("agent-enrollment", "Agent enrollment", "an agent connector's first message or Setup's connection of an assistant",
		trayQuestionViewForContent(enrollRecord, trayPublisher{}.forPrompt(nil), nil, "", &askclient.CatalogueContent{Slots: enrollSlots}),
		identity("Codex", trayPublisher{}, enrollSlots["program"]))
	updateSlots := map[string]string{"members": "Claude integration and OpenCode", "membersmore": "", "memberslast": "", "code": strings.Repeat("c", 48)}
	updateRecord := askclient.RecordMetadata{ID: "update-q", Key: asks.AgentUpdateKey, Asked: asked, Asker: "openabstractions.exe", Options: []string{"update", "not_now"},
		Text: render(asks.AgentUpdateKey, updateSlots, "openabstractions")}
	add("agent-update", "Connection update", "an OA update that widens an installed connector", trayQuestionViewForContent(updateRecord, trayPublisher{}, nil, "", nil), nil)

	// Program review: an unsigned app whose file changed, and a shipped file
	// with no record.
	reviewRecord, reviewContent := unsignedReviewFixture(t)
	add("program-review-unsigned", "Unsigned program changed", "a kept unsigned app's image no longer matches its pin",
		trayQuestionViewForContent(reviewRecord, trayPublisher{}, nil, "", reviewContent), nil)
	add("program-review-shipped", "Program review (OA files)", "a shipped program file has no recorded pin", programReviewNativeFixture(t, "shipped:hash-mismatch", true), nil)

	// Setup: coding model with pictures, and the new video setup (LTX).
	for _, fixture := range setupRouteWordingFixtures(t) {
		if fixture.Name == "bf16/1/pictures-true" {
			add("setup-code-pictures", "Setup, coding model and pictures", "the person starts Setup, or an app's request for use code/image",
				setupRouteWordingView(t, fixture), nil)
		}
	}
	set, ok := modelhost.VideoModelSetNamed(modelhost.LTX25.Name)
	engine, engineOK := modelhost.PinnedVideoEngine("windows", "amd64")
	if !ok || !engineOK {
		t.Fatal("the LTX video set or its engine is not pinned for windows/amd64")
	}
	video := setup.VideoSetupFact(set, engine)
	videoSlots, err := asks.SetupFactSlots(asks.SetupQuestionFacts{Occurrence: strings.Repeat("b", 48), Requester: "OpenCode", RequestUse: "video", Video: &video})
	if err != nil {
		t.Fatal(err)
	}
	videoBody, err := asks.SetupQuestionPresentation(videoSlots)
	if err != nil {
		t.Fatal(err)
	}
	videoRecord := askclient.RecordMetadata{ID: "setup-video", Key: asks.SetupModelsKey, About: videoSlots["occurrence"], Text: videoBody.Instruction, Options: []string{"setup", "not_now"}}
	add("setup-video", "Setup, video (LTX)", "an app's request for use video (oa_setup / Setup)",
		trayQuestionViewForContent(videoRecord, trayPublisher{}, nil, "", &askclient.CatalogueContent{Slots: videoSlots}), nil)

	// Pairing and the Windows Security log.
	add("pairing", "Pairing", "the person pairs two computers", trayQuestionViewForAsker(nativePairingFixture(t), trayPublisher{}, nil, ""), nil)
	logSlots := map[string]string{"code": "a1b2c3d4", "until": until}
	logRecord := askclient.RecordMetadata{ID: "log-q", Asker: "reader.exe", Key: asks.SystemSecurityLogKey, About: "a1b2c3d4", Asked: asked,
		Options: []string{"once", "refuse", "never"}, Text: render(asks.SystemSecurityLogKey, map[string]string{"asker": "reader.exe", "code": "a1b2c3d4", "until": until}, "reader.exe")}
	reader := `C:\Tools\reader.exe`
	add("security-log", "Windows Security log read", "an app asks to read the Security log (system access helper)",
		trayQuestionViewForContent(logRecord, trayPublisher{}.forPrompt(nil), nil, reader, &askclient.CatalogueContent{Slots: logSlots}),
		identity("reader", trayPublisher{}, reader))

	// Application-callable catalogue questions (question family).
	qRecord := func(id, key string, slots map[string]string) askclient.RecordMetadata {
		q, _ := asks.Find(key)
		options := []string{}
		for _, o := range q.Options {
			options = append(options, o.Name)
		}
		return askclient.RecordMetadata{ID: id, Asker: "opencode.exe", Key: key, Asked: asked, Options: options, Text: render(key, slots, "opencode.exe")}
	}
	for _, c := range []struct {
		name, key string
		slots     map[string]string
	}{
		{"download-reach", "download.reach", map[string]string{"host": "huggingface.co"}},
		{"content-replace", "content.replace", map[string]string{"target": "“notes.md”", "replacement": "“notes (edited).md”", "code": "c0de", "until": until}},
		{"project-edit", "opencode.project.edit", map[string]string{"folder": `C:\Users\person\src\shop`, "code": "c0de", "until": until}},
	} {
		add(c.name, "App question: "+c.key, "an app's ordinary question (application@2 Ask)",
			trayQuestionViewForContent(qRecord("q-"+c.name, c.key, c.slots), trayPublisher{}.forPrompt(nil), nil, opencode, &askclient.CatalogueContent{Slots: c.slots}), nil)
	}
	add("app-choice", "App choice (TaskDialog)", "an app's ordinary choice (application@2 Choose)", trayChoiceView(choiceFixture(), nil), nil)

	summaries := map[string]*strings.Builder{"before": {}, "after": {}}
	for _, k := range kinds {
		for _, set := range []string{"before", "after"} {
			view := k.view
			if set == "after" {
				if k.after == nil {
					continue
				}
				view = k.after(view)
			}
			prompt, err := trayWindowsQuestionPrompt(view)
			if err != nil {
				t.Fatalf("%s: %v", k.name, err)
			}
			prompt.CredentialForm = nil
			raw, err := json.MarshalIndent(prompt, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			folder := filepath.Join(dir, "fixtures-"+set)
			if err := os.MkdirAll(folder, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(folder, k.name+".json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			summarizePopup(summaries[set], k.name, k.row, k.trigger, view, prompt)
		}
	}
	for set, b := range summaries {
		if err := os.WriteFile(filepath.Join(dir, "views-"+set+".md"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d kinds to %s at %s", len(kinds), dir, time.Now().Format(time.RFC3339))
}

func summarizePopup(b *strings.Builder, name, row, trigger string, view trayQuestionPresentation, prompt win.QuestionPrompt) {
	family := map[nativeQuestionFamily]string{nativeQuestionGeneric: "question", nativeQuestionAppChoice: "app choice", nativeQuestionConsent: "consent", nativeQuestionPairing: "pairing consent"}[view.presentation]
	renderer := "Windows 11 dialog"
	if prompt.AppChoice {
		renderer = "TaskDialog"
	}
	fmt.Fprintf(b, "## %s — %s\n\n", name, row)
	fmt.Fprintf(b, "- trigger: %s\n- family: %s; renderer: %s; level: %s\n", trigger, family, renderer, prompt.Level)
	fmt.Fprintf(b, "- window title: %q\n", prompt.Title)
	heading, body := prompt.Instruction, prompt.Content
	identity := "none"
	if v := prompt.PermissionView; v != nil {
		heading = v.Question
		body = strings.TrimSpace(v.Scope + "\n" + v.Effects)
		if v.Requester != "" || v.Publisher != "" {
			icon := "generic application icon"
			if v.RequesterProgram != "" {
				icon = "icon of " + filepath.Base(v.RequesterProgram)
			}
			identity = fmt.Sprintf("%s; name %q; line %q", icon, v.Requester, v.Publisher)
		}
	}
	fmt.Fprintf(b, "- heading: %q\n- identity row: %s\n- body: %q\n", heading, identity, body)
	labels := []string{}
	for _, c := range prompt.Choices {
		labels = append(labels, fmt.Sprintf("%s (%s)", c.Label, c.Value))
	}
	def := prompt.Default
	if def == "" {
		def = "none (Enter = Cancel id; no button emphasised)"
	}
	dismiss := prompt.Dismiss
	if dismiss == "" {
		dismiss = "none: a Cancel button closes without answering"
	}
	fmt.Fprintf(b, "- buttons: %s\n- default (Enter): %s\n- Escape/close: %s\n", strings.Join(labels, ", "), def, dismiss)
	if prompt.Footer != "" {
		fmt.Fprintf(b, "- footer: %q\n", prompt.Footer)
	}
	lines := strings.Split(prompt.Details, "\n")
	sort.SliceStable(lines, func(int, int) bool { return false })
	fmt.Fprintf(b, "- details:\n")
	for _, line := range lines {
		fmt.Fprintf(b, "  - `%s`\n", line)
	}
	b.WriteString("\n")
}
