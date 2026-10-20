package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	facade "github.com/openabstractions/abstraction-facade/go"
	inference "github.com/openabstractions/abstraction-inference/go"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

// The runtime hosted declaration owner supplies the Panel's static catalogue.
// Every caller receives an independent projection of that one source.
var hostedPresetsData = hostedcatalogue.Data()

type hostedPreset = hostedcatalogue.Preset

const (
	budgetMoney     = "money"
	budgetTokens    = "tokens"
	downloadPreset  = "download"
	maxPresetBudget = 1_000_000_000_000
)

var presetConsumers = []string{inference.Contract, router.CredentialConsumer}

func consumersForPreset(p hostedPreset) []string {
	if p.Kind == downloadPreset {
		return []string{"abstraction.download/http-execution@1"}
	}
	return presetConsumers
}
func loadHostedPresets(raw []byte) ([]hostedPreset, error) { return hostedcatalogue.Load(raw) }
func hostedPresets() ([]hostedPreset, error)               { return hostedcatalogue.All() }

// presetTarget is the host name an https address names, the target its key
// is applied to. The credentials holder takes lowercase targets, so an
// address with a capital letter in its host name is refused rather than
// changed.
func presetTarget(base string) (string, error) {
	return hostedcatalogue.Target(base)
}

// connectEdit is one Connect a service change. connect saves the key and adds
// the preset's model server with its daily budget; budget replaces a
// connected server's budget. The budget is in the preset's unit. The secret
// travels once and is zeroed after the call; it is never returned, logged or
// named in an error.
type connectEdit struct {
	Edit           string  `json:"edit"`
	Preset         string  `json:"preset"`
	Base           string  `json:"base"`
	Name           string  `json:"name"`
	Secret         string  `json:"secret"`
	Budget         int64   `json:"budget"`
	CredentialName *string `json:"credential_name,omitempty"`
}

// connectResult is a connect or budget edit's one outcome: applied, or the
// word and the part (credential or server) that refused.
type connectResult struct {
	Outcome string `json:"outcome"`
	Name    string `json:"name,omitempty"`
	Part    string `json:"part,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// presetHolder and presetOperator are the parts of credentials holder@1 and
// inference operator@1 a connect uses.
type presetHolder interface {
	Store(ctx context.Context, expected string, reg cwire.Registration) (cwire.StoreResult, error)
	Rotate(ctx context.Context, expected string, rotation cwire.Rotation) (cwire.RotateResult, error)
}

type presetOperator interface {
	Hosts(ctx context.Context) (iclient.HostList, error)
	AddHost(ctx context.Context, expected string, host iclient.HostEntry) (iclient.HostChange, error)
	RemoveHost(ctx context.Context, expected, name string) (iclient.HostChange, error)
}

// presetCeiling is kept with its unit's daily budget set to amount (0: none).
func presetCeiling(unit string, amount int64, kept *iclient.CeilingLimit) *iclient.CeilingLimit {
	var c iclient.CeilingLimit
	if kept != nil {
		c = *kept
	}
	if unit == budgetTokens {
		c.TokensPerDay = amount
	} else {
		c.MicrosPerDay = amount
	}
	if c == (iclient.CeilingLimit{}) {
		return nil
	}
	return &c
}

// presetUnit uses the preset's original host identity. Credential names are
// freely chosen and never select a provider or its budget unit.
func presetUnit(presets []hostedPreset, host iclient.HostEntry) string {
	unit := budgetMoney
	for _, p := range presets {
		switch {
		case p.Credential != "" && p.Credential == host.Name && p.Base == host.Base && p.API == host.Kind:
			return p.Budget
		case p.Base == "":
			unit = p.Budget
		}
	}
	return unit
}

// sameScope reports whether a saved credential is the one a preset saves: an
// active or expired bearer key for target, applied for at least the
// preset's consumers.
func sameScope(current *cwire.Metadata, target string, consumers []string) bool {
	if current == nil || current.Kind != "bearer" || current.State != cwire.StateActive && current.State != cwire.StateExpired ||
		len(current.Scope.Targets) != 1 || current.Scope.Targets[0] != target {
		return false
	}
	if len(consumers) == 1 && consumers[0] == "abstraction.download/http-execution@1" && len(current.Scope.Consumers) != 1 {
		return false
	}
	for _, consumer := range consumers {
		if !slices.Contains(current.Scope.Consumers, consumer) {
			return false
		}
	}
	return true
}

// connectPreset saves secret under the preset's credential name, scoped to
// its address and to chat and model-list reads, and adds the preset's model
// server with that credential and a daily budget of amount in the preset's
// unit (0: none). A credential already saved under that name with the same
// scope, left by an earlier Connect whose server was not added or saved by
// hand, has its key replaced and the Connect continues; one saved for
// another use is a conflict.
func connectPreset(ctx context.Context, holder presetHolder, operator presetOperator, preset hostedPreset, base string, secret []byte, amount int64) (connectResult, error) {
	return connectPresetEntry(ctx, holder, operator, preset, base, secret, amount, nil)
}

// A named creation never rotates an existing credential. Blank names receive
// a fresh bounded name; the holder remains the collision and permission owner.
func connectPresetEntry(ctx context.Context, holder presetHolder, operator presetOperator, preset hostedPreset, base string, secret []byte, amount int64, chosen *string) (connectResult, error) {
	if preset.Kind == downloadPreset && amount != 0 {
		return connectResult{Outcome: "invalid", Part: "credential", Reason: "budget"}, nil
	}
	name := preset.Credential
	if preset.Base != "" {
		base = preset.Base
	}
	target, err := presetTarget(base)
	if err != nil {
		return connectResult{Outcome: "invalid", Part: "server", Reason: "address"}, nil
	}
	if name == "" {
		name = target
		if !validCredentialName(name) {
			return connectResult{Outcome: "invalid", Part: "server", Reason: "address"}, nil
		}
	}
	if chosen != nil {
		name = strings.TrimSpace(*chosen)
		if name == "" {
			var suffix [8]byte
			if _, err := rand.Read(suffix[:]); err != nil {
				return connectResult{}, errors.New("A credential name could not be created")
			}
			prefix := preset.ID
			if len(prefix) > 50 {
				prefix = prefix[:50]
			}
			name = prefix + "-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(suffix[:]))
		}
		if !validCredentialName(name) || !inferenceName.MatchString(name) {
			return connectResult{Outcome: "invalid", Part: "credential", Reason: "name"}, nil
		}
	}
	stored, err := holder.Store(ctx, "", cwire.Registration{Name: name, Kind: "bearer",
		Scope: cwire.Scope{Targets: []string{target}, Consumers: consumersForPreset(preset)}, Secret: secret})
	if err != nil {
		return connectResult{}, errors.New("Saved credentials could not be reached")
	}
	if chosen == nil && stored.Outcome == cwire.StoreOutcomeConflict && sameScope(stored.Current, target, consumersForPreset(preset)) {
		rotated, err := holder.Rotate(ctx, stored.Revision, cwire.Rotation{Name: name, Secret: secret})
		if err != nil {
			return connectResult{}, errors.New("Saved credentials could not be reached")
		}
		if rotated.Outcome != cwire.RotateOutcomeRotated {
			return connectResult{Outcome: rotated.Outcome.String(), Name: name, Part: "credential"}, nil
		}
	} else if stored.Outcome != cwire.StoreOutcomeStored {
		return connectResult{Outcome: stored.Outcome.String(), Name: name, Part: "credential"}, nil
	}
	if preset.Kind == downloadPreset {
		return connectResult{Outcome: "applied", Name: name}, nil
	}
	serverName := preset.Credential
	if serverName == "" {
		serverName = target
	}
	entry := iclient.HostEntry{Name: serverName, Hosted: true, Kind: preset.API, Base: base, Credential: name, Ceiling: presetCeiling(preset.Budget, amount, nil)}
	var change iclient.HostChange
	// A server list that changed between the read and the edit is read
	// again once.
	for attempt := 0; attempt < 2; attempt++ {
		list, err := operator.Hosts(ctx)
		if err != nil {
			return connectResult{}, err
		}
		if change, err = operator.AddHost(ctx, list.Revision, entry); err != nil {
			return connectResult{}, err
		}
		if change.Outcome.String() != "conflict" {
			break
		}
	}
	if change.Outcome.String() != "applied" {
		return connectResult{Outcome: change.Outcome.String(), Name: name, Part: "server", Reason: change.Reason}, nil
	}
	return connectResult{Outcome: "applied", Name: name}, nil
}

// budgetPreset replaces the daily budget, in the unit of the preset its
// credential comes from, of the operator's hosted model server name: the
// declaration is removed and added again with the new budget, and restored
// as it was when the new one is refused. Its spend today is kept (INF-C2).
func budgetPreset(ctx context.Context, operator presetOperator, presets []hostedPreset, name string, amount int64) (connectResult, error) {
	list, err := operator.Hosts(ctx)
	if err != nil {
		return connectResult{}, err
	}
	var current *iclient.HostEntry
	for i := range list.Hosts {
		if e := list.Hosts[i].Entry; e.Name == name && e.Hosted && e.DeclaredBy == "operator" {
			current = &e
		}
	}
	if current == nil {
		return connectResult{Outcome: "unknown", Name: name, Part: "server"}, nil
	}
	removed, err := operator.RemoveHost(ctx, list.Revision, name)
	if err != nil {
		return connectResult{}, err
	}
	if removed.Outcome.String() != "applied" {
		return connectResult{Outcome: removed.Outcome.String(), Name: name, Part: "server", Reason: removed.Reason}, nil
	}
	replaced := *current
	unit := presetUnit(presets, *current)
	replaced.Ceiling = presetCeiling(unit, amount, current.Ceiling)
	change, err := operator.AddHost(ctx, removed.Revision, replaced)
	if err == nil && change.Outcome.String() == "applied" {
		return connectResult{Outcome: "applied", Name: name}, nil
	}
	restored, restoreErr := operator.AddHost(ctx, removed.Revision, *current)
	if restoreErr != nil || restored.Outcome.String() != "applied" {
		return connectResult{}, fmt.Errorf("the model server %s was removed and could not be added again: %v %v", name, errors.Join(err, restoreErr), restored.Outcome)
	}
	if err != nil {
		return connectResult{}, err
	}
	return connectResult{Outcome: change.Outcome.String(), Name: name, Part: "server", Reason: change.Reason}, nil
}

// connectService lists the presets (GET) and connects one or changes a
// connected server's budget (POST) through the resolved credentials holder
// and inference operator, each deciding this Panel's permission itself.
func (p *servicePanel) connectService(w http.ResponseWriter, r *http.Request) {
	presets, err := hostedPresets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		panelJSON(w, map[string]any{"presets": presets})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	var edit connectEdit
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&edit); err != nil {
		http.Error(w, "invalid connect edit", http.StatusBadRequest)
		return
	}
	secret := []byte(edit.Secret)
	edit.Secret = ""
	defer clear(secret)
	if edit.Budget < 0 || edit.Budget > maxPresetBudget {
		http.Error(w, "invalid connect edit", http.StatusBadRequest)
		return
	}
	var preset hostedPreset
	switch edit.Edit {
	case "connect":
		found := false
		for _, candidate := range presets {
			if candidate.ID == edit.Preset {
				preset, found = candidate, true
			}
		}
		if !found || len(secret) < 1 || len(secret) > credentials.MaxSecretBytes || (preset.Base == "") == (edit.Base == "") || preset.Kind == downloadPreset && edit.Budget != 0 {
			http.Error(w, "invalid connect edit", http.StatusBadRequest)
			return
		}
	case "budget":
		if !inferenceName.MatchString(edit.Name) || len(secret) != 0 {
			http.Error(w, "invalid connect edit", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "invalid connect edit", http.StatusBadRequest)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	var operator presetOperator
	if preset.Kind != downloadPreset {
		operator, err = panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			panelError(w, err)
			return
		}
	}
	var result connectResult
	if edit.Edit == "budget" {
		p.logAction(ctx, "inference.host.budget", map[string]string{"inference.host": edit.Name})
		result, err = budgetPreset(ctx, operator, presets, edit.Name, edit.Budget)
	} else {
		holder, resolveErr := panelMachine().ResolveCredentials(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if resolveErr != nil {
			panelError(w, resolveErr)
			return
		}
		p.logAction(ctx, "credentials.connect", map[string]string{"credentials.preset": preset.ID})
		result, err = connectPresetEntry(ctx, holder, operator, preset, edit.Base, secret, edit.Budget, edit.CredentialName)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}
