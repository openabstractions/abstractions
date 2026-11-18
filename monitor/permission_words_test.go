package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Agent-ux 2026-11-22, Permissions: an app's page and the Permissions page
// word a saved permission alike, a device hold names the device it uses
// ("Use the graphics card"), and the rows on both pages read through the
// one shared oaRuleWords.
func TestPermissionWordsAreSharedAndPlain(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel script")
	}
	script := strings.TrimSuffix(strings.TrimPrefix(plainScript, "<script>"), "</script>")
	fixture := `const vm=require('node:vm'),assert=require('node:assert/strict'),fs=require('node:fs');
const context=vm.createContext({console,document:{documentElement:{dataset:{}},getElementById(){return null},addEventListener(){}},window:{},location:{search:''}});
context.window=context;vm.runInContext(fs.readFileSync(0,'utf8'),context);
const words=(phrase,rule)=>JSON.parse(vm.runInContext('JSON.stringify(oaRuleWords('+JSON.stringify(phrase)+','+JSON.stringify(rule)+'))',context));
const hold=resource=>({Action:'abstraction.resource/hold',Resource:resource});
assert.deepEqual(words('Hold a device resource',hold('card:0')),{label:'Use the graphics card',caption:'Until removed'});
assert.deepEqual(words('Hold a device resource',hold('card:1')),{label:'Use graphics card 2',caption:'Until removed'});
assert.deepEqual(words('Hold a device resource',hold('memory')),{label:'Use memory',caption:'Until removed'});
assert.deepEqual(words('Hold a device resource',hold('awake')),{label:'Keep this computer awake',caption:'Until removed'});
assert.deepEqual(words('Look up models',{Action:'abstraction.model/lookup',Resource:'card:0',Expired:true}),{label:'Look up models',caption:'Graphics card · Expired'});
assert.deepEqual(words('Read stored content',{Action:'abstraction.storage/content.read',Resource:'sha256:ab'}),{label:'Read stored content',caption:'Stored content · Until removed'});
`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", fixture)
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	// Both pages draw their rule rows through oaRuleWords.
	if !strings.Contains(appPageScript, "oaRuleWords(") || !strings.Contains(rightsPageScriptForWords(), "oaRuleWords(") {
		t.Fatal("an app's page or the Permissions page words its rules apart from oaRuleWords")
	}
}

// rightsPageScriptForWords is the Permissions page's markup and script.
func rightsPageScriptForWords() string { return rightsPage }

// The elevated-callers switch reads as a plain statement with its caption,
// and the threat model quotes the exact label.
func TestElevatedCallersSwitchReadsAsAStatement(t *testing.T) {
	view := presentDefault(rwire.AccountDefault{Name: "elevated-callers", On: true})
	if view.Label != "Programs run as administrator can change permissions without asking" || view.Caption != "You see each change in Activity." {
		t.Fatalf("elevated-callers: %+v", view)
	}
	if encoded, _ := json.Marshal(view); !strings.Contains(string(encoded), `"Caption":"You see each change in Activity."`) {
		t.Fatalf("the caption does not reach the page: %s", encoded)
	}
}

// The website publishes separately; its exact label quote is checked in the
// private workspace while the Panel's own wording check runs everywhere.
func TestThreatModelQuotesElevatedCallersSwitch(t *testing.T) {
	view := presentDefault(rwire.AccountDefault{Name: "elevated-callers", On: true})
	raw, err := os.ReadFile(privateInput(t, "..", "openabstractions-flat", "openabstractions.github.io", "threat-model.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `The switch "`+view.Label+`" on the Permissions page`) {
		t.Fatal("the threat model does not quote the switch's label")
	}
}

// Assistant identity shows on agent apps only.
func TestAssistantIdentityOnlyOnAgentApps(t *testing.T) {
	for name, want := range map[string]bool{"example": false, "comfyui": false, "oa-claude-integration": true, "oa-codex-integration-0123": true, "opencode": true} {
		if got := appIsAgent(name); got != want {
			t.Errorf("%s: agent %v, want %v", name, got, want)
		}
	}
	_, p := appPagePanel(t, true)
	doc := appPageGet(t, p, "/app/example?k="+appPageKey).Body.String()
	if strings.Contains(doc, "Assistant identity") || strings.Contains(doc, "app-agent-identity") {
		t.Fatal("a non-assistant app shows Assistant identity")
	}
}
