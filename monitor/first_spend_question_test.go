package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// The first-spend question (P-M9, architect ruling C 2026-11-08) goes
// through today's presenter in fixture mode, so nothing is shown on screen:
// a credential's first-use question names the service and its daily budget,
// offers Allow and Not now, and its allow writes the credential's use
// rule as any first-use allow does.
func TestFirstSpendQuestionNamesTheServiceAndItsBudget(t *testing.T) {
	jan := nativePresenterProgram("jan", "jan.exe")
	record := firstUseFixture("question-spend", jan, "abstraction.credentials/apply", "credential:oa-panel-deepseek")
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	surface := &fakeSurface{option: "allow"}
	rules := &fakeRules{}
	p := testPresenter(unlockedSession(), surface, operator, rules)
	var asked []string
	p.spend = func(_ context.Context, name string) (spendLimit, bool) {
		asked = append(asked, name)
		return spendLimit{Label: "DeepSeek", Micros: 5_000_000}, true
	}
	result := p.present(context.Background(), record)
	if result.Outcome != presentAnswered {
		t.Fatalf("present %+v", result)
	}
	if len(asked) != 1 || asked[0] != "oa-panel-deepseek" {
		t.Fatalf("the presenter read the budget of %v", asked)
	}
	shown := surface.shown[0]
	if shown.Instruction != "Allow jan to use your DeepSeek account?" {
		t.Fatalf("instruction %q", shown.Instruction)
	}
	if shown.Content != "Up to $5 a day." {
		t.Fatalf("content %q", shown.Content)
	}
	if !strings.HasPrefix(shown.Details, "DeepSeek bills jan's requests to your account. Access stays if you replace the saved key. Change the limit in Permissions.") {
		t.Fatalf("details %q", shown.Details)
	}
	var labels []string
	for _, c := range shown.Choices {
		labels = append(labels, c.Label+"="+c.Option)
	}
	if strings.Join(labels, ",") != "Allow=allow,Not now=refuse,Never allow=never" || shown.Default != "refuse" || shown.Dismiss != "refuse" {
		t.Fatalf("choices %v default %q dismiss %q", labels, shown.Default, shown.Dismiss)
	}
	if shown.Permission != nil && (shown.Permission.Question != shown.Instruction || shown.Permission.Content != shown.Content) {
		t.Fatalf("permission view %+v", shown.Permission)
	}
	for _, text := range []string{shown.Instruction, shown.Content} {
		if strings.Contains(text, "oa-panel") {
			t.Fatalf("an internal credential name is visible: %q", text)
		}
	}
	if len(rules.edits) != 1 || rules.edits[0].Action != "abstraction.credentials/apply" || rules.edits[0].Resource != "credential:oa-panel-deepseek" || !rules.edits[0].Permit {
		t.Fatalf("the allow wrote %+v", rules.edits)
	}

	// The budget line follows the credential's setting.
	if got := spendBody("OpenCode", spendLimit{Label: "DeepSeek", Tokens: 1_000_000}); got != "Up to 1,000,000 tokens a day." {
		t.Fatalf("a token budget %q", got)
	}
	if got := spendBody("OpenCode", spendLimit{Label: "OpenRouter", Micros: 500_000}); got != "Up to $0.50 a day." {
		t.Fatalf("cents %q", got)
	}
	if got := spendBody("OpenCode", spendLimit{Label: "DeepSeek"}); got != "No daily limit." {
		t.Fatalf("no budget %q", got)
	}
	other := pendingFixture()
	surface = &fakeSurface{}
	p = testPresenter(unlockedSession(), surface, &fakeOperator{records: []askclient.RecordMetadata{other}}, &fakeRules{})
	p.spend = func(context.Context, string) (spendLimit, bool) {
		t.Fatal("a model lookup read a budget")
		return spendLimit{}, false
	}
	p.present(context.Background(), other)
	if len(surface.shown) != 1 || surface.shown[0].Instruction != "Allow opencode to look up models?" {
		t.Fatalf("another question %+v", surface.shown)
	}
}

// Not now saves nothing: the app asks again (architect popup ruling
// 2026-11-10, row 3).
func TestFirstSpendNotNowSavesNoRule(t *testing.T) {
	opencode := nativePresenterProgram("opencode", "opencode.exe")
	record := firstUseFixture("question-spend-not-now", opencode, "abstraction.credentials/apply", "credential:deepseek")
	surface := &fakeSurface{option: "refuse"}
	rules := &fakeRules{}
	p := testPresenter(unlockedSession(), surface, &fakeOperator{records: []askclient.RecordMetadata{record}}, rules)
	p.spend = func(context.Context, string) (spendLimit, bool) { return spendLimit{Label: "DeepSeek"}, true }
	if result := p.present(context.Background(), record); result.Outcome != presentAnswered {
		t.Fatalf("present %+v", result)
	}
	if len(rules.edits) != 0 {
		t.Fatalf("Not now wrote %+v", rules.edits)
	}
}

// The account is the service's label. Two accounts of one service carry the
// name the person gave each key; an internal or generated name never shows.
func TestSpendLabelNamesTheServiceNeverTheKey(t *testing.T) {
	deepseek := func(name string, tokens int64) hostedServer {
		return hostedServer{Credential: name, Base: "https://api.deepseek.com/v1", Kind: "openai-compatible", Tokens: tokens}
	}
	if limit, ok := spendOf("oa-panel-deepseek", []hostedServer{deepseek("oa-panel-deepseek", 0)}); !ok || limit.Label != "DeepSeek" {
		t.Fatalf("one account %+v %v", limit, ok)
	}
	servers := []hostedServer{deepseek("work", 2_000_000), deepseek("deepseek-abcdefghijklm", 0), deepseek("work", 1_000_000)}
	if limit, _ := spendOf("work", servers); limit.Label != "DeepSeek (work)" || limit.Tokens != 1_000_000 {
		t.Fatalf("the person's label and the strictest budget %+v", limit)
	}
	if limit, _ := spendOf("deepseek-abcdefghijklm", servers); limit.Label != "DeepSeek" {
		t.Fatalf("a generated name %+v", limit)
	}
	if _, ok := spendOf("unknown", servers); ok {
		t.Fatal("a credential no server names asked about spending")
	}
}

// OpenAbstractions' own programs carry no Publisher line.
func TestOwnProgramQuestionHasNoPublisherLine(t *testing.T) {
	own := ownProgramTitles()
	if len(own) == 0 {
		t.Skip("no own program")
	}
	record := firstUseFixture("question-own", own[0].program, "abstraction.credentials/apply", "credential:deepseek")
	view := trayQuestionView(record, trayPublisher{})
	if strings.Contains(view.Content, "Publisher") || view.Permission == nil || view.Permission.Publisher != "" || strings.Contains(view.Permission.Details, "Publisher") {
		t.Fatalf("own program view %+v", view.Permission)
	}
	other := firstUseFixture("question-other", nativePresenterProgram("jan", "jan.exe"), "abstraction.credentials/apply", "credential:deepseek")
	if view := trayQuestionView(other, trayPublisher{}); view.Permission == nil || view.Permission.Publisher == "" {
		t.Fatal("another program lost its Publisher line")
	}
}

// An agent connector, Setup's image signed by this Panel's signer, is one of
// OpenAbstractions' own programs: its permission question carries no
// Publisher line, as its registration question does (oaOwnProgram is the one
// rule for both).
func TestConnectorPermissionQuestionReadsAsOwnProgram(t *testing.T) {
	const name = "oa-claude-integration"
	image := installedConnectorImages(t, name)[name]
	record := firstUseFixture("question-connector", image, "abstraction.credentials/apply", "credential:deepseek")
	view := trayQuestionView(record, trayPublisher{})
	if strings.Contains(view.Content, "Publisher") || view.Permission == nil || view.Permission.Publisher != "" || strings.Contains(view.Permission.Details, "Publisher") {
		t.Fatalf("connector view %+v", view.Permission)
	}
}

// A program that registered itself under a connector name, or carries a
// connector's file name outside Setup's folder, keeps its Publisher line in
// the permission question: only Setup's installed image is a connector.
func TestConnectorLookAlikePermissionQuestionKeepsPublisherLine(t *testing.T) {
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	if err := os.WriteFile(filepath.Join(root, "oa-claude-integration.exe"), []byte("connector"), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	for _, program := range []string{filepath.Join(elsewhere, "evil.exe"), filepath.Join(elsewhere, "oa-claude-integration.exe")} {
		if err := os.WriteFile(program, []byte("look-alike"), 0o600); err != nil {
			t.Fatal(err)
		}
		names := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, SelfRegistered: true,
			Descriptor: fwire.ApplicationDescriptor{Name: "oa-claude-integration", Program: program, Title: "Claude"}}})
		record := firstUseFixture("question-look-alike", program, "abstraction.credentials/apply", "credential:deepseek")
		view := trayQuestionView(record, trayPublisher{Names: names})
		if view.Permission == nil || view.Permission.Publisher == "" || !strings.Contains(view.Permission.Details, "Publisher") {
			t.Fatalf("look-alike %s lost its Publisher line: %+v", program, view.Permission)
		}
	}
}

// Never allow stays offered and writes the block on the account.
func TestFirstSpendNeverAllowIsABlock(t *testing.T) {
	opencode := nativePresenterProgram("opencode", "opencode.exe")
	record := firstUseFixture("question-spend-never", opencode, "abstraction.credentials/apply", "credential:deepseek")
	surface := &fakeSurface{option: "never"}
	rules := &fakeRules{}
	p := testPresenter(unlockedSession(), surface, &fakeOperator{records: []askclient.RecordMetadata{record}}, rules)
	p.spend = func(context.Context, string) (spendLimit, bool) { return spendLimit{Label: "DeepSeek"}, true }
	if result := p.present(context.Background(), record); result.Outcome != presentAnswered {
		t.Fatalf("present %+v", result)
	}
	if len(rules.edits) != 1 || rules.edits[0].Resource != "credential:deepseek" || rules.edits[0].Permit {
		t.Fatalf("Never allow wrote %+v", rules.edits)
	}
}
