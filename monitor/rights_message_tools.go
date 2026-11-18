package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Permissions page's "Messages between agents" group lists each pair of
// the person's agents with "Allow full tool use when <sender> messages
// <receiver>" (S5 architect ruling, 2026-11-22). Off by default, a turn
// another agent's message started in the receiver is reply-only. The switch
// writes the receiving agent's own rule, application.message_full_tools on
// app:<sender>, through this Panel's rights operator: only the person sets
// it, here. Every change is an Activity line. The receiver's MCP connection
// reads the rule through OA for each such turn.

// messageToolsRules is the rights operator surface the switches read and
// change.
type messageToolsRules interface {
	ListPolicyContext(ctx context.Context, cursor string, limit int64) (rightsclient.PolicyPage, error)
	SetRuleContext(ctx context.Context, expected string, rule rightsclient.PolicyRule) (rwire.PolicyEdit, error)
	RevokeRuleContext(ctx context.Context, expected string, subject rightsclient.Subject, action, resource string) (rwire.PolicyEdit, error)
}

// messageToolsOperator resolves the operator and the person's account, the
// account every rule names. Tests replace it.
var messageToolsOperator = func(ctx context.Context) (messageToolsRules, string, error) {
	caller, err := panelMachine().ObserveCaller(ctx)
	if err != nil || caller.Outcome.String() != "observed" || caller.Account == "" {
		return nil, "", errors.New("the Panel's own account could not be verified")
	}
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, "", err
	}
	return operator, caller.Account, nil
}

// messageToolsAgent is one of the person's agents: Setup's connector
// registration, by its routing name, title and program.
type messageToolsAgent struct {
	Name, Title, Program string
}

// messageToolsAgents lists the agents a switch may name. Tests replace it.
var messageToolsAgents = func(ctx context.Context, p *servicePanel) []messageToolsAgent {
	return connectorAgents(p.programTitler(ctx).registered)
}

func connectorAgents(registered []fwire.ApplicationEntry) []messageToolsAgent {
	var agents []messageToolsAgent
	for _, entry := range registered {
		if title, ok := registeredConnectorTitle(entry); ok {
			agents = append(agents, messageToolsAgent{Name: entry.Descriptor.Name, Title: title, Program: entry.Descriptor.Program})
		}
	}
	return agents
}

// messageToolsPair is one switch as the page draws it.
type messageToolsPair struct {
	Sender      string `json:"sender"`
	Receiver    string `json:"receiver"`
	SenderApp   string `json:"senderApp"`
	ReceiverApp string `json:"receiverApp"`
	Label       string `json:"label"`
	On          bool   `json:"on"`
}

type messageToolsView struct {
	Outcome  string             `json:"outcome"`
	Revision string             `json:"revision,omitempty"`
	Pairs    []messageToolsPair `json:"pairs"`
	Message  string             `json:"message,omitempty"`
}

type messageToolsEdit struct {
	Revision string `json:"revision"`
	Sender   string `json:"sender"`
	Receiver string `json:"receiver"`
	On       bool   `json:"on"`
}

// messageToolsLabel is the ruling's row text.
func messageToolsLabel(sender, receiver string) string {
	return "Allow full tool use when " + sender + " messages " + receiver
}

// messageToolsPolicyPages bounds one read of the saved rules.
const messageToolsPolicyPages = 16

// readMessageTools lists the switches and the policy revision they were read
// at. A rule counts only when it names the person's account, the receiver's
// registered program and the sender exactly, and allows.
func readMessageTools(ctx context.Context, rules messageToolsRules, account string, agents []messageToolsAgent) (messageToolsView, error) {
	on := map[[2]string]bool{}
	cursor, revision := "", ""
	for range messageToolsPolicyPages {
		page, err := rules.ListPolicyContext(ctx, cursor, 64)
		if err != nil {
			return messageToolsView{}, err
		}
		if page.Outcome != rightsclient.PolicyPageOutcomePage {
			return messageToolsView{Outcome: page.Outcome.String(), Pairs: []messageToolsPair{}}, nil
		}
		revision = page.Revision
		for _, rule := range page.Rules {
			if rule.Action != grants.ApplicationMessageFullToolsAction || !rule.Permit || rule.Subject.Account != account {
				continue
			}
			for _, receiver := range agents {
				if identity.SameSubjectProgram(receiver.Program, rule.Subject.Program) {
					on[[2]string{rule.Resource, receiver.Name}] = true
				}
			}
		}
		if page.Complete || page.Next == "" {
			break
		}
		cursor = page.Next
	}
	view := messageToolsView{Outcome: "page", Revision: revision, Pairs: []messageToolsPair{}}
	for _, sender := range agents {
		for _, receiver := range agents {
			view.Pairs = append(view.Pairs, messageToolsPair{Sender: sender.Title, Receiver: receiver.Title, SenderApp: sender.Name, ReceiverApp: receiver.Name,
				Label: messageToolsLabel(sender.Title, receiver.Title), On: on[[2]string{"app:" + sender.Name, receiver.Name}]})
		}
	}
	return view, nil
}

// rightsMessageTools lists the switches (GET) or turns one (POST).
func (p *servicePanel) rightsMessageTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	var edit messageToolsEdit
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(&edit) != nil || decoder.Decode(&extra) != io.EOF || !rightsText(edit.Revision, 128) || !rightsText(edit.Sender, 64) || !rightsText(edit.Receiver, 64) {
			http.Error(w, "invalid switch edit", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	rules, account, err := messageToolsOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	agents := messageToolsAgents(ctx, p)
	if r.Method == http.MethodGet {
		view, err := readMessageTools(ctx, rules, account, agents)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, view)
		return
	}
	var sender, receiver *messageToolsAgent
	for i := range agents {
		if agents[i].Name == edit.Sender {
			sender = &agents[i]
		}
		if agents[i].Name == edit.Receiver {
			receiver = &agents[i]
		}
	}
	if sender == nil || receiver == nil {
		panelJSON(w, messageToolsView{Outcome: "invalid", Pairs: []messageToolsPair{}, Message: "That agent is no longer set up."})
		return
	}
	subject := rightsclient.Subject{Account: account, Program: receiver.Program}
	resource := "app:" + sender.Name
	state := "off"
	if edit.On {
		state = "on"
	}
	p.logAction(ctx, "rights.message_full_tools", map[string]string{"rights.revision": edit.Revision, "rights.sender": sender.Name, "rights.receiver": receiver.Name, "rights.state": state})
	var result rwire.PolicyEdit
	if edit.On {
		result, err = rules.SetRuleContext(ctx, edit.Revision, rightsclient.PolicyRule{Subject: subject, Action: grants.ApplicationMessageFullToolsAction, Resource: resource, Permit: true})
	} else {
		result, err = rules.RevokeRuleContext(ctx, edit.Revision, subject, grants.ApplicationMessageFullToolsAction, resource)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	view := messageToolsView{Outcome: result.Outcome.String(), Revision: result.Revision, Pairs: []messageToolsPair{}}
	if result.Outcome == rwire.PolicyEditOutcomeApplied {
		what, outcome := "Allowed full tool use when "+sender.Title+" messages "+receiver.Title, "Allowed"
		view.Message = what + "."
		if !edit.On {
			what, outcome = "Turned off full tool use when "+sender.Title+" messages "+receiver.Title, "Turned off"
			view.Message = receiver.Title + " turns started by " + sender.Title + "'s messages are reply-only again."
		}
		p.appActivity.add(applicationActivity{App: receiver.Title, Program: receiver.Program, What: what, Outcome: outcome})
	}
	panelJSON(w, view)
}
