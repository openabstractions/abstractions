package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

// spendLimit is what the first-spend question says about a credential: the
// service it pays (a preset's label, with the person's own name for the key
// when they keep two accounts for one service) and its daily budget (INF-C2,
// vocabulary D64) in currency millionths and in tokens, 0 for none.
type spendLimit struct {
	Label  string
	Micros int64
	Tokens int64
}

// The hosted-account question's words (architect popup ruling 2026-11-10,
// row 3). Each takes the app's name and the service's label; swap them here.
var (
	spendTitle   = func(app, service string) string { return "Allow " + app + " to use your " + service + " account?" }
	spendBilling = func(app, service string) string {
		return service + " bills " + app + "'s requests to your account."
	}
	spendDetails = "Access stays if you replace the saved key. Change the limit in Permissions."
)

// firstSpendPrompt words a credential's first-use question as the account it
// spends (P-M9): it names the requesting app and the credential account, one
// sentence with the daily limit, and Allow / Not now / Never allow. The billing sentence,
// the token cap and where to change the limit sit in the details. A question
// about anything else, or a credential no model server names, keeps its
// words.
func firstSpendPrompt(ctx context.Context, view trayQuestionPresentation, permission *askclient.PermissionRequest, lookup func(context.Context, string) (spendLimit, bool)) trayQuestionPresentation {
	if permission == nil || len(permission.Targets) != 1 || lookup == nil {
		return view
	}
	target := permission.Targets[0]
	if target.Action != credentials.ActionApply {
		return view
	}
	resource, ok := credentials.ParseResource(target.Resource)
	if !ok {
		return view
	}
	limit, ok := lookup(ctx, resource.Name)
	if !ok {
		return view
	}
	app := programStem(target.Program)
	if view.Permission != nil && view.Permission.Requester != "" {
		app = view.Permission.Requester
	}
	title := spendTitle(app, limit.Label)
	body := spendBody(app, limit)
	extra := spendBilling(app, limit.Label)
	if limit.Micros > 0 && limit.Tokens > 0 {
		extra += " Token limit: " + tokenCount(limit.Tokens) + " a day."
	}
	extra += " " + spendDetails
	view.Instruction, view.Content, view.Footer = title, body, ""
	view.Details = strings.TrimSpace(extra + "\n\n" + view.Details)
	if view.PermissionContent != "" {
		view.PermissionContent = body
	}
	var choices []trayQuestionChoice
	for _, choice := range view.Choices {
		switch choice.Option {
		case "allow":
			choices = append(choices, trayQuestionChoice{Label: "Allow", Option: "allow"})
		case "refuse":
			choices = append(choices, trayQuestionChoice{Label: "Not now", Option: "refuse"})
		case "never":
			// The account's block stays one click away (popup ruling
			// 2026-11-10: Never allow where a block exists).
			choices = append(choices, trayQuestionChoice{Label: "Never allow", Option: "never"})
		}
	}
	if hasTrayChoice(choices, "allow") && hasTrayChoice(choices, "refuse") {
		// Not now is the decline: Enter, Escape and the close box answer it,
		// and no separate Cancel is drawn.
		view.Choices, view.Default, view.Dismiss = choices, "refuse", "refuse"
	}
	if view.Permission != nil {
		shown := *view.Permission
		shown.Instruction, shown.Question, shown.Content = title, title, body
		shown.Scope, shown.Effects, shown.Footer = body, "", ""
		shown.Details = strings.TrimSpace(extra + "\n\n" + shown.Details)
		empty := ""
		shown.Warning = &empty
		view.Permission = &shown
	}
	return view
}

// spendBody is the question's one sentence: the daily limit the account is
// held to, in money when it sets one, else in tokens, or no daily limit.
func spendBody(_ string, limit spendLimit) string {
	switch {
	case limit.Micros > 0:
		return "Up to " + spendAmount(limit.Micros) + " a day."
	case limit.Tokens > 0:
		return "Up to " + tokenCount(limit.Tokens) + " tokens a day."
	}
	return "No daily limit."
}

// tokenCount is a token count as the Panel shows it, with thousands
// separated by commas.
func tokenCount(n int64) string {
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	return b.String()
}

// spendAmount is a currency-millionths amount in dollars: whole dollars
// without cents ("$5"), others with two decimals ("$0.50").
func spendAmount(micros int64) string {
	if micros%1_000_000 == 0 {
		return "$" + tokenCount(micros/1_000_000)
	}
	return "$" + strconv.FormatFloat(float64(micros)/1e6, 'f', 2, 64)
}

// presenterSpend reads a credential's budget from the inference operator's
// model servers: the strictest nonzero daily budget in each unit among the
// servers that name it (INF-C2), and its label from the preset whose address
// those servers have. A credential no server names is not a spending
// question.
func presenterSpend(ctx context.Context, name string) (spendLimit, bool) {
	operator, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return spendLimit{}, false
	}
	list, err := operator.Hosts(ctx)
	if err != nil {
		return spendLimit{}, false
	}
	var servers []hostedServer
	for _, h := range list.Hosts {
		if h.Entry.Hosted {
			s := hostedServer{Credential: h.Entry.Credential, Base: h.Entry.Base, Kind: h.Entry.Kind}
			if c := h.Entry.Ceiling; c != nil {
				s.Micros, s.Tokens = c.MicrosPerDay, c.TokensPerDay
			}
			servers = append(servers, s)
		}
	}
	return spendOf(name, servers)
}

// hostedServer is the part of a hosted model server's entry spendOf reads.
type hostedServer struct {
	Credential, Base, Kind string
	Micros, Tokens         int64
}

// spendOf is presenterSpend over listed servers.
func spendOf(name string, servers []hostedServer) (spendLimit, bool) {
	found := false
	limit := spendLimit{}
	for _, s := range servers {
		if s.Credential != name {
			continue
		}
		found = true
		limit.Micros = strictest(limit.Micros, s.Micros)
		limit.Tokens = strictest(limit.Tokens, s.Tokens)
		if limit.Label == "" {
			limit.Label = serviceLabel(name, s.Base, s.Kind, servers)
		}
	}
	return limit, found
}

// serviceLabel names the account a credential pays for a person: the
// service's own label ("DeepSeek"), and, when the person keeps keys for two
// accounts of one service, the name they gave this key ("DeepSeek (work)").
// An internal or generated key name never shows; a key for no known service
// reads "this service".
func serviceLabel(name, base, kind string, servers []hostedServer) string {
	preset, ok := hostedcatalogue.PresetFor(base, kind)
	if !ok {
		return "this service"
	}
	accounts := map[string]bool{}
	for _, s := range servers {
		if other, same := hostedcatalogue.PresetFor(s.Base, s.Kind); same && other.ID == preset.ID && s.Credential != "" {
			accounts[s.Credential] = true
		}
	}
	if len(accounts) > 1 && personKeyName(name, preset) {
		return preset.Label + " (" + name + ")"
	}
	return preset.Label
}

// generatedKeyName is the Panel's own name for a key saved without one:
// the preset id and thirteen base32 characters.
var generatedKeyName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*-[a-z2-7]{13}$`)

// personKeyName reports whether a credential's name is one the person chose,
// rather than the preset's own, a generated one or OpenAbstractions' own.
func personKeyName(name string, preset hostedcatalogue.Preset) bool {
	return name != "" && name != preset.Credential && !strings.HasPrefix(name, "oa-") && !generatedKeyName.MatchString(name)
}

// strictest is the smaller nonzero of two daily budgets, 0 when both are.
func strictest(kept, next int64) int64 {
	if next > 0 && (kept == 0 || next < kept) {
		return next
	}
	return kept
}

func hasTrayChoice(choices []trayQuestionChoice, option string) bool {
	for _, choice := range choices {
		if choice.Option == option {
			return true
		}
	}
	return false
}
