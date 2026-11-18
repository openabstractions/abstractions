package main

import (
	"encoding/json"
	"io"
	"net/http"

	facade "github.com/openabstractions/abstraction-facade/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Permissions page's "Defaults for all apps" group (rights RIGHTS-R22,
// RIGHTS-O13; VISION 2026-10-07): one switch per account-wide default the
// runtime configures. A switch writes one SetDefault through the runtime's
// rights operator; the runtime decides, the Panel only presents.

// defaultLabels names each default the runtime configures as its row label;
// defaultPhrases word it inside the sentence a click writes ("Every app may
// now …"). A default the runtime adds later shows its name until it has words
// here.
var defaultLabels = map[string]string{
	"register": "Add itself to Apps",
	"ask":      "Ask you questions",
	"announce": "Say when it is running",
	"notify":   "Send you notifications",
	"download": "Download in the background",
	// A plain statement of what the switch does; it stays on after a fresh
	// install (architect design A), so its caption says where each change
	// shows (agent-ux 2026-11-22).
	"elevated-callers": "Programs run as administrator can change permissions without asking",
	// agent-messages has its own group on the Permissions page.
	agentMessagesDefault: "Message each other without asking",
}

// agentMessagesDefault is the runtime's switch for messages between the
// person's kept agents (rights DefaultAgentMessages). The Permissions page
// shows it as its own group, "Messages between agents", after the defaults
// for all apps, which leave it out (AB-1w Panel ruling, 2026-11-09). Its one
// switch reads "Ask me first" and is on while the default is off.
const agentMessagesDefault = "agent-messages"

const (
	agentMessagesCaption    = "Your agents can send each other messages and questions."
	agentMessagesAskLabel   = "Ask me first"
	agentMessagesAskCaption = ""
	agentMessagesAskName    = "Ask before an agent messages another"
)

// defaultCaptions are the captions under a default's label.
var defaultCaptions = map[string]string{
	"elevated-callers": "You see each change in Activity.",
}

var defaultPhrases = map[string]string{
	"register":           "add itself to Apps",
	"ask":                "ask you questions",
	"announce":           "say when it is running",
	"notify":             "send you notifications",
	"download":           "download in the background",
	"elevated-callers":   "change permissions without asking when run as administrator",
	agentMessagesDefault: "message each other without asking",
}

// defaultView is one default as the page draws it.
type defaultView struct {
	Name    string
	Label   string
	Caption string `json:"Caption,omitempty"`
	Phrase  string
	On      bool
	SetBy   *rightsclient.Subject `json:"SetBy,omitempty"`
	SetAt   string                `json:"SetAt,omitempty"`
	// Technical carries the action and resource for Technical details.
	Action        string
	Resource      string
	EveryResource bool
}

type defaultsView struct {
	Outcome  string
	Revision string
	Defaults []defaultView
	// Agents is the agent-messages switch, listed apart from Defaults.
	Agents  *defaultView `json:"Agents,omitempty"`
	Current *defaultView `json:"Current,omitempty"`
}

func presentDefault(d rwire.AccountDefault) defaultView {
	label, phrase := defaultLabels[d.Name], defaultPhrases[d.Name]
	if label == "" || phrase == "" {
		label, phrase = d.Name, "use the default "+d.Name
	}
	return defaultView{Name: d.Name, Label: label, Caption: defaultCaptions[d.Name], Phrase: phrase, On: d.On, SetBy: d.SetBy, SetAt: d.SetAt, Action: d.Action, Resource: d.Resource, EveryResource: d.EveryResource}
}

// defaultEdit is one switch turned on the page.
type defaultEdit struct {
	Revision string `json:"revision"`
	Name     string `json:"name"`
	On       bool   `json:"on"`
}

// rightsDefaults lists the defaults (GET) or turns one switch (POST). A
// runtime older than ListDefaults reads "unsupported".
func (p *servicePanel) rightsDefaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var edit defaultEdit
	if r.Method == "POST" {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(&edit) != nil || decoder.Decode(&extra) != io.EOF || !rightsText(edit.Revision, 128) || !rightsText(edit.Name, 64) {
			http.Error(w, "invalid default edit", 400)
			return
		}
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	if r.Method == "GET" {
		page, err := operator.ListDefaultsContext(ctx)
		if selectorUnsupported(err) {
			panelJSON(w, defaultsView{Outcome: "unsupported", Defaults: []defaultView{}})
			return
		}
		if err != nil {
			panelError(w, err)
			return
		}
		view := defaultsView{Outcome: page.Outcome.String(), Revision: page.Revision, Defaults: []defaultView{}}
		for _, d := range page.Defaults {
			shown := presentDefault(d)
			if d.Name == agentMessagesDefault {
				view.Agents = &shown
				continue
			}
			view.Defaults = append(view.Defaults, shown)
		}
		panelJSON(w, view)
		return
	}
	state := "off"
	if edit.On {
		state = "on"
	}
	p.logAction(ctx, "rights.default", map[string]string{"rights.revision": edit.Revision, "rights.default": edit.Name, "rights.default.state": state})
	result, err := operator.SetDefaultContext(ctx, edit.Revision, edit.Name, edit.On)
	if selectorUnsupported(err) {
		panelJSON(w, defaultsView{Outcome: "unsupported", Defaults: []defaultView{}})
		return
	}
	if err != nil {
		panelError(w, err)
		return
	}
	view := defaultsView{Outcome: result.Outcome.String(), Revision: result.Revision, Defaults: []defaultView{}}
	if result.Current != nil {
		current := presentDefault(*result.Current)
		view.Current = &current
	}
	panelJSON(w, view)
}
