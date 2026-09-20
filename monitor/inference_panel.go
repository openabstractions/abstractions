package main

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	facade "github.com/openabstractions/abstraction-facade/go"
	identity "github.com/openabstractions/abstraction-identity"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
)

// inferenceHostEdit is one host change the person made on the inference page.
type inferenceHostEdit struct {
	Edit     string            `json:"edit"`
	Revision string            `json:"revision"`
	Host     iclient.HostEntry `json:"host"`
	Name     string            `json:"name"`
}

// inferenceKeyEdit issues or revokes the gateway window key of one program.
type inferenceKeyEdit struct {
	Edit       string `json:"edit"`
	Program    string `json:"program"`
	Credential string `json:"credential"`
}

var inferenceName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (e inferenceHostEdit) valid() bool {
	if !rightsText(e.Revision, 128) {
		return false
	}
	switch e.Edit {
	case "add":
		h := e.Host
		return inferenceName.MatchString(h.Name) && rightsText(h.Kind, 128) && rightsText(h.Base, 2048) &&
			(h.Credential == "" || inferenceName.MatchString(h.Credential)) && (h.Ceiling == nil || h.Ceiling.TokensPerDay >= 0 && h.Ceiling.MicrosPerDay >= 0)
	case "remove":
		return inferenceName.MatchString(e.Name)
	}
	return false
}

func (e inferenceKeyEdit) valid() bool {
	program := rightsText(e.Program, 4096) && identity.ValidSubjectProgram(e.Program)
	switch e.Edit {
	case "issue":
		return program && (e.Credential == "" || inferenceName.MatchString(e.Credential))
	case "revoke":
		return program && e.Credential == ""
	}
	return false
}

// inferenceRoutes serves the inference page and its calls. Every call goes
// through the resolved abstraction.inference/operator@1, which decides
// host.manage, key.issue or audit.read for this Panel; typed outcomes are
// returned unchanged and resolution failures read as unavailable.
func (p *servicePanel) inferenceRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/inference", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, inferencePage)
	})
	mux.HandleFunc("/inference/hosts", guard(key, p.inferenceHosts))
	mux.HandleFunc("/inference/keys", guard(key, p.inferenceKeys))
	mux.HandleFunc("/inference/audit", guard(key, p.inferenceAudit))
	mux.HandleFunc("/inference/gateway", guard(key, p.inferenceGateway))
}

// inferenceGatewayEdit opens or closes the gateway window at the listed
// setting revision.
type inferenceGatewayEdit struct {
	Revision string `json:"revision"`
	Open     bool   `json:"open"`
	Address  string `json:"address"`
}

var gatewayEditAddress = regexp.MustCompile(`^127\.0\.0\.1:[1-9][0-9]{0,4}$`)

func (e inferenceGatewayEdit) valid() bool {
	return rightsText(e.Revision, 128) && (e.Address == "" || gatewayEditAddress.MatchString(e.Address))
}

// inferenceGateway reads the gateway window setting, or writes it through
// operator@1 SetGateway, which the runtime applies before replying.
func (p *servicePanel) inferenceGateway(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var edit inferenceGatewayEdit
	if r.Method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&edit); err != nil || !edit.valid() {
			http.Error(w, "invalid gateway edit", 400)
			return
		}
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	var result any
	if r.Method == "GET" {
		result, err = operator.Gateway(ctx)
	} else {
		p.logAction(ctx, "inference.gateway.set", map[string]string{"inference.gateway.open": strconv.FormatBool(edit.Open), "inference.gateway.address": edit.Address, "inference.revision": edit.Revision})
		result, err = operator.SetGateway(ctx, edit.Revision, edit.Open, edit.Address)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}

func (p *servicePanel) inferenceHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var edit inferenceHostEdit
	if r.Method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&edit); err != nil || !edit.valid() {
			http.Error(w, "invalid host edit", 400)
			return
		}
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	var result any
	switch edit.Edit {
	case "":
		result, err = operator.Hosts(ctx)
	case "add":
		p.logAction(ctx, "inference.host.add", map[string]string{"inference.host": edit.Host.Name, "inference.revision": edit.Revision})
		result, err = operator.AddHost(ctx, edit.Revision, edit.Host)
	case "remove":
		p.logAction(ctx, "inference.host.remove", map[string]string{"inference.host": edit.Name, "inference.revision": edit.Revision})
		result, err = operator.RemoveHost(ctx, edit.Revision, edit.Name)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}

func (p *servicePanel) inferenceKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var edit inferenceKeyEdit
	if r.Method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&edit); err != nil || !edit.valid() {
			http.Error(w, "invalid key edit", 400)
			return
		}
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	var result any
	switch edit.Edit {
	case "":
		result, err = operator.Keys(ctx)
	case "issue":
		// The key itself is never logged; the page shows it once.
		p.logAction(ctx, "inference.key.issue", map[string]string{"inference.program": edit.Program, "inference.credential": edit.Credential})
		w.Header().Set("Cache-Control", "no-store")
		result, err = operator.IssueKey(ctx, edit.Program, edit.Credential)
	case "revoke":
		p.logAction(ctx, "inference.key.revoke", map[string]string{"inference.program": edit.Program})
		result, err = operator.RevokeKey(ctx, edit.Program)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}

func (p *servicePanel) inferenceAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 405)
		return
	}
	cursor, err := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	if raw := r.URL.Query().Get("cursor"); raw == "" {
		cursor, err = 0, nil
	} else if len(raw) > 20 || !utf8.ValidString(raw) || strings.HasPrefix(raw, "-") {
		err = strconv.ErrSyntax
	}
	if err != nil {
		http.Error(w, "invalid audit cursor", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := operator.Audit(ctx, cursor, 64)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, page)
}
