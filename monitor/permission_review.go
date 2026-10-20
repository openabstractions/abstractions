package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go/service"
)

const permissionReviewTTL = time.Hour

// These are proposals for the person's personal permission editor. They carry
// no authority: review and apply both resolve the account, program and feature.
type permissionReviewContext struct {
	Feature string `json:"feature"`
	App     string `json:"app,omitempty"`
	Program string `json:"program,omitempty"`
	Host    string `json:"host,omitempty"`
}

func (c permissionReviewContext) valid() bool {
	switch c.Feature {
	case "notifications":
		return c.App == "" && c.Host == "" && filepath.IsAbs(c.Program) && rightsText(c.Program, 4096)
	case "picture-workflow":
		return c.Program == "" && rightsText(c.App, 128) && (c.Host == "" || rightsText(c.Host, 64))
	}
	return false
}

type permissionReviewOperator interface {
	grants.Operator
	ReadRuleContext(context.Context, rights.Subject, string, string) (rights.RuleRead, error)
}

type permissionReviewProgram struct {
	Program string `json:"program"`
	FileID  string `json:"file_id"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}

// Read the existing local file through identity's platform owner. Binding both
// the file ID and bytes makes replacement between review and approval stale.
func inspectPermissionProgram(path string) (permissionReviewProgram, error) {
	var result permissionReviewProgram
	if !filepath.IsAbs(path) {
		return result, errors.New("the program must be an existing absolute local path")
	}
	if err := identity.LocalPath(path); err != nil {
		return result, err
	}
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("the selected program is not a regular file")
	}
	key, err := identity.FileKey(path)
	if err != nil {
		return result, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return result, err
	}
	canonical := identity.CanonicalProgramPath(path)
	now, err := identity.FileKey(canonical)
	current, statErr := os.Stat(canonical)
	if err != nil || statErr != nil || !os.SameFile(info, current) || key != now || n != info.Size() {
		return result, errors.New("the selected program changed while it was read")
	}
	return permissionReviewProgram{canonical, key, hex.EncodeToString(h.Sum(nil)), n}, nil
}

type permissionReviewSources struct {
	Nonce     string
	StartedAt string
	Resolve   func(context.Context) (permissionReviewOperator, rights.Subject, error)
	Program   func(string) (permissionReviewProgram, error)
	App       func(context.Context, string) (string, string, string, error) // program, title, registration digest
	Hosts     func(context.Context) (iclient.HostList, error)
	HostLabel func(context.Context, iclient.HostEntry) string
}

func (p *servicePanel) permissionReviewSource() permissionReviewSources {
	if p.permissionReview != nil {
		return *p.permissionReview
	}
	p.permissionReviewMu.Lock()
	if p.permissionReviewNonce == "" {
		p.permissionReviewNonce = mint()
		p.permissionReviewStarted = time.Now().UTC().Format(time.RFC3339Nano)
	}
	nonce, started := p.permissionReviewNonce, p.permissionReviewStarted
	p.permissionReviewMu.Unlock()
	return permissionReviewSources{
		Nonce: nonce, StartedAt: started,
		Resolve: func(ctx context.Context) (permissionReviewOperator, rights.Subject, error) {
			machine := panelMachine()
			caller, err := machine.ObserveCaller(ctx)
			if err != nil || caller.Outcome.String() != "observed" || caller.Account == "" || caller.Program == "" {
				return nil, rights.Subject{}, errors.New("the Panel's account and program could not be verified")
			}
			op, err := machine.ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
			return op, rights.Subject{Account: caller.Account, Program: caller.Program}, err
		},
		Program: inspectPermissionProgram,
		App: func(ctx context.Context, name string) (string, string, string, error) {
			apps, err := p.setupApplicationsForPanel(ctx)
			if err != nil {
				return "", "", "", err
			}
			entry, readable := appPageEntry(ctx, apps, name)
			if !readable || entry == nil {
				return "", "", "", errors.New("the selected app is no longer registered")
			}
			if entry.SelfRegistered {
				return "", "", "", errors.New("confirm this app's registration before reviewing its permissions")
			}
			return entry.Descriptor.Program, appEntryTitle(*entry), applicationDigest(&entry.Descriptor), nil
		},
		Hosts: func(ctx context.Context) (iclient.HostList, error) {
			op, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
			if err != nil {
				return iclient.HostList{}, err
			}
			return op.Hosts(ctx)
		},
		HostLabel: func(ctx context.Context, host iclient.HostEntry) string {
			if title, ok := p.programTitler(ctx).title(host.DeclaredBy); ok {
				return title
			}
			return "Model server"
		},
	}
}

type permissionReviewView struct {
	Nonce        string                  `json:"nonce"`
	Context      permissionReviewContext `json:"context"`
	Subject      rights.Subject          `json:"subject"`
	Program      permissionReviewProgram `json:"identity"`
	Title        string                  `json:"title"`
	Registration string                  `json:"registration,omitempty"`
	HostRevision string                  `json:"host_revision,omitempty"`
	Host         *iclient.HostEntry      `json:"host_identity,omitempty"`
	HostLabel    string                  `json:"host_label,omitempty"`
	Rules        []grants.Rule           `json:"rules"`
	Sentences    []string                `json:"sentences"`
	Lifetime     string                  `json:"lifetime"`
	TTLMS        int64                   `json:"ttl_ms"`
	Disclosure   string                  `json:"disclosure"`
	Revision     string                  `json:"revision"`
	ID           string                  `json:"id"`
}

func reviewDigest(v permissionReviewView) string {
	v.ID = ""
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func reviewImageHosts(list iclient.HostList) []iclient.HostEntry {
	var hosts []iclient.HostEntry
	if list.Outcome != iclient.ListOutcomePage || list.Revision == "" {
		return hosts
	}
	for _, host := range list.Hosts {
		if host.Up && slices.Contains(host.Entry.Profiles, "image") {
			hosts = append(hosts, host.Entry)
		}
	}
	return hosts
}

func buildPermissionReview(ctx context.Context, s permissionReviewSources, proposal permissionReviewContext) (permissionReviewView, permissionReviewOperator, rights.Subject, error) {
	v := permissionReviewView{Context: proposal, TTLMS: permissionReviewTTL.Milliseconds(), Lifetime: "One hour", Nonce: s.Nonce}
	if !proposal.valid() {
		return v, nil, rights.Subject{}, errors.New("invalid permission review context")
	}
	if s.Nonce == "" {
		return v, nil, rights.Subject{}, errors.New("the native review identity is unavailable")
	}
	op, setter, err := s.Resolve(ctx)
	if err != nil {
		return v, nil, setter, err
	}
	if op == nil || setter.Account == "" || setter.Program == "" {
		return v, nil, setter, errors.New("the native permission operator identity is unavailable")
	}
	program := proposal.Program
	v.Title = "Python executable"
	if proposal.App != "" {
		program, v.Title, v.Registration, err = s.App(ctx, proposal.App)
		if err != nil {
			return v, op, setter, err
		}
	}
	v.Program, err = s.Program(program)
	if err != nil {
		return v, op, setter, err
	}
	v.Subject = rights.Subject{Account: setter.Account, Program: v.Program.Program}
	// Unregistered programs are deliberately described by the actual image.
	// Notification access belongs to every script that runs through that image.
	if proposal.Feature == "notifications" {
		v.Title = programStem(v.Program.Program)
		v.Disclosure = "This permission covers this executable whenever it runs during the next hour."
		if asks.ScriptHostSubject(v.Subject, asks.Publisher{}) {
			v.Disclosure = "This permission covers all scripts using this executable, including scripts you run later."
		}
		v.Rules = []grants.Rule{{Action: grants.NotificationPostAction, Resource: grants.NotificationPostResource}}
		v.Sentences = []string{"Show notifications in your account, with actions you can choose."}
	} else {
		list, err := s.Hosts(ctx)
		if err != nil {
			return v, op, setter, err
		}
		hosts := reviewImageHosts(list)
		if proposal.Host == "" && len(hosts) == 1 {
			proposal.Host = hosts[0].Name
			v.Context.Host = proposal.Host
		}
		for _, host := range hosts {
			if host.Name == proposal.Host {
				copy := host
				v.Host = &copy
			}
		}
		if v.Host == nil {
			return v, op, setter, errors.New("choose an available image server from the current list")
		}
		v.HostRevision = list.Revision
		v.HostLabel = "Model server"
		if s.HostLabel != nil {
			v.HostLabel = s.HostLabel(ctx, *v.Host)
		}
		v.Rules, err = grants.FeatureRules(grants.Picture, grants.For{Hosts: []string{v.Host.Name}})
		if err != nil {
			return v, op, setter, err
		}
		v.Rules = append(v.Rules, grants.Rule{Action: "abstraction.asks/question.ask", Resource: "account"}, grants.Rule{Action: router.ActionInventory, Resource: router.ResourceInventory})
		v.Sentences = []string{"Make pictures with the selected image server.", "Save the pictures it makes.", "Read back the pictures it made.", "Ask you questions when the workflow needs your choice.", "List available model servers."}
		v.Disclosure = "The selected app receives these permissions. Registration itself grants no permissions."
		if asks.ScriptHostSubject(v.Subject, asks.Publisher{}) {
			v.Disclosure += " This permission covers all scripts using this executable, including scripts you run later."
		}
	}
	// Read each exact target at one revision. Any retained rule is preserved,
	// including explicit blocks and expired records; the person reviews it in
	// Saved permissions instead of this flow replacing it.
	for _, rule := range v.Rules {
		read, err := op.ReadRuleContext(ctx, v.Subject, rule.Action, rule.Resource)
		if err != nil {
			return v, op, setter, err
		}
		if read.Outcome != rights.RuleReadOutcomeUnknown || read.Record != nil || read.Revision == "" {
			return v, op, setter, errors.New("a permission already exists or could not be read; review Saved permissions first")
		}
		if v.Revision != "" && v.Revision != read.Revision {
			return v, op, setter, errors.New("permissions changed while reading; review again")
		}
		v.Revision = read.Revision
	}
	v.ID = reviewDigest(v)
	return v, op, setter, nil
}

type permissionReviewChoice struct {
	Context permissionReviewContext `json:"context"`
	Shown   string                  `json:"shown"`
	Allow   bool                    `json:"allow"`
}

type permissionReviewResult struct {
	Outcome  string              `json:"outcome"`
	Message  string              `json:"message"`
	Revision string              `json:"revision,omitempty"`
	Landed   []rights.RuleRecord `json:"landed"`
	Stopped  *grants.Rule        `json:"stopped,omitempty"`
	Unknown  *grants.Rule        `json:"unknown,omitempty"`
}

type permissionReviewDecision struct {
	View   permissionReviewView
	Result permissionReviewResult
}

func (p *servicePanel) settledPermissionReview(c permissionReviewContext) (permissionReviewDecision, bool) {
	p.permissionReviewActionMu.Lock()
	defer p.permissionReviewActionMu.Unlock()
	if v, ok := p.permissionReviewDecisions[c]; ok {
		return v, true
	}
	// An entry that let the person choose an image server remains settled
	// when reloaded without the chosen server in its original URL.
	if c.Host == "" {
		for target, v := range p.permissionReviewDecisions {
			if target.Feature == c.Feature && target.App == c.App && target.Program == c.Program {
				return v, true
			}
		}
	}
	return permissionReviewDecision{}, false
}

func settledPermissionReviewFragment(d permissionReviewDecision) string {
	rows := []string{`<p role="status">` + html.EscapeString(d.Result.Message) + `</p>`}
	for _, saved := range d.Result.Landed {
		for i, rule := range d.View.Rules {
			if rule.Action == saved.Rule.Action && rule.Resource == saved.Rule.Resource {
				rows = append(rows, oaRow(d.View.Sentences[i], "Saved until "+saved.Expires, "", ""))
			}
		}
	}
	return `<h1>Permission review finished</h1>` + oaGroup("permission-review-finished", d.View.Title, "", rows...)
}

type permissionReviewReceipt struct {
	Profile     string                 `json:"profile"`
	PID         int                    `json:"pid"`
	StartedAt   string                 `json:"started_at"`
	DecidedAt   string                 `json:"decided_at"`
	Review      permissionReviewView   `json:"review"`
	NativeActor rights.Subject         `json:"native_actor"`
	Result      permissionReviewResult `json:"result"`
}

func validatePermissionReceiptPath(path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return errors.New("the review receipt needs an absolute path in an existing private folder")
	}
	if err := identity.LocalPath(path); err != nil {
		return err
	}
	if err := privatedir.CheckDir(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("the review receipt already exists or could not be checked")
	}
	return nil
}

// Only startup may create the task-specific receipt folder. Ensure refuses an
// existing nonprivate folder and never changes its ACL. Requests only check it.
func preparePermissionReceiptPath(path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return errors.New("the review receipt needs an absolute path")
	}
	if err := identity.LocalPath(path); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(filepath.Dir(parent)); err != nil {
		return errors.New("the receipt folder's parent must already exist")
	}
	if err := privatedir.Ensure(parent); err != nil {
		return err
	}
	return validatePermissionReceiptPath(path)
}

func writePermissionReviewReceipt(path string, s permissionReviewSources, v permissionReviewView, actor rights.Subject, result permissionReviewResult) error {
	if path == "" {
		return nil
	}
	if err := validatePermissionReceiptPath(path); err != nil {
		return err
	}
	receipt := permissionReviewReceipt{Profile: "oa-personal-permission-review@1", PID: os.Getpid(), StartedAt: s.StartedAt, DecidedAt: time.Now().UTC().Format(time.RFC3339Nano), Review: v, NativeActor: actor, Result: result}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writeModelHostRecord(path, data)
}

// Each approved permission is written through the existing sequential owner.
// Authoritative readback is part of each edit's success; uncertain edits stop.
type reviewedPermissionWriter struct {
	op      permissionReviewOperator
	setter  rights.Subject
	records []rights.RuleRecord
	unknown *grants.Rule
	started time.Time
}

func (w *reviewedPermissionWriter) SetRuleForContext(ctx context.Context, revision string, rule rights.PolicyRule, ttl time.Duration, why string) (rights.PolicyEdit, error) {
	edit, err := w.op.SetRuleForContext(ctx, revision, rule, ttl, why)
	if err != nil {
		w.unknown = &grants.Rule{Action: rule.Action, Resource: rule.Resource}
		return edit, err
	}
	if edit.Outcome != rights.PolicyEditOutcomeApplied {
		return edit, nil
	}
	read, err := w.op.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource)
	if err != nil || read.Revision != edit.Revision || !reviewRecordMatches(read, rule, w.setter, why) {
		w.unknown = &grants.Rule{Action: rule.Action, Resource: rule.Resource}
		return edit, errors.New("the saved permission could not be verified")
	}
	set, _ := time.Parse(time.RFC3339Nano, read.Record.SetAt)
	if set.Before(w.started.Add(-time.Second)) || set.After(time.Now().Add(time.Second)) {
		w.unknown = &grants.Rule{Action: rule.Action, Resource: rule.Resource}
		return edit, errors.New("the saved permission's time could not be verified")
	}
	w.records = append(w.records, *read.Record)
	return edit, nil
}

func reviewRecordMatches(read rights.RuleRead, rule rights.PolicyRule, setter rights.Subject, why string) bool {
	if read.Outcome != rights.RuleReadOutcomeFound || read.Record == nil {
		return false
	}
	r := read.Record
	if r.Rule != rule || r.SetBy != setter || r.Why != why {
		return false
	}
	set, err := time.Parse(time.RFC3339Nano, r.SetAt)
	if err != nil {
		return false
	}
	expires, err := time.Parse(time.RFC3339Nano, r.Expires)
	return err == nil && expires.Sub(set) == permissionReviewTTL
}

func applyPermissionReview(ctx context.Context, s permissionReviewSources, choice permissionReviewChoice) (permissionReviewResult, error) {
	result := permissionReviewResult{Outcome: "refused", Message: "No permissions were changed.", Landed: []rights.RuleRecord{}}
	if !choice.Allow {
		return result, nil
	}
	v, op, setter, err := buildPermissionReview(ctx, s, choice.Context)
	if err != nil {
		return result, err
	}
	if choice.Shown == "" || choice.Shown != v.ID {
		return result, errors.New("the app, server or permissions changed; review again")
	}
	why := "personal review " + v.ID
	w := &reviewedPermissionWriter{op: op, setter: setter, started: time.Now()}
	written, err := grants.Write(ctx, w, v.Revision, v.Subject, v.Rules, permissionReviewTTL, why)
	result.Landed, result.Stopped, result.Unknown, result.Revision = w.records, written.Stopped, w.unknown, written.Revision
	if err != nil || written.Outcome != "applied" {
		result.Outcome = "partial"
		if w.unknown != nil {
			result.Outcome = "uncertain"
		}
		result.Message = "Some permissions could not be verified. Review the saved permissions before continuing. No automatic retry was made."
		return result, nil
	}
	for _, saved := range result.Landed {
		read, e := op.ReadRuleContext(ctx, saved.Rule.Subject, saved.Rule.Action, saved.Rule.Resource)
		if e != nil || read.Revision != result.Revision || read.Record == nil || *read.Record != saved || read.Outcome != rights.RuleReadOutcomeFound {
			result.Outcome, result.Message = "uncertain", "The saved permissions changed or could not be verified. Review them before continuing."
			result.Unknown = &grants.Rule{Action: saved.Rule.Action, Resource: saved.Rule.Resource}
			return result, nil
		}
	}
	result.Outcome, result.Message = "applied", "Permissions saved for one hour."
	return result, nil
}

func reviewContextFromURL(r *http.Request) permissionReviewContext {
	q := r.URL.Query()
	return permissionReviewContext{Feature: q.Get("feature"), App: q.Get("app"), Program: q.Get("program"), Host: q.Get("host")}
}

// decidePermissionReview is the single decision owner for management pages
// and standalone native management consent. Presentation supplies no authority.
func (p *servicePanel) decidePermissionReview(ctx context.Context, choice permissionReviewChoice) (permissionReviewResult, int, error) {
	p.permissionReviewActionMu.Lock()
	defer p.permissionReviewActionMu.Unlock()
	refusal := func(message string, status int) (permissionReviewResult, int, error) {
		return permissionReviewResult{}, status, errors.New(message)
	}
	if !choice.Context.valid() {
		return refusal("invalid permission choice", 400)
	}
	if startPermissionReviewReceipt != "" && !permissionReviewLaunchMatches(startPermissionReview, choice.Context) {
		return refusal("the proposed review context changed", 409)
	}
	source := p.permissionReviewSource()
	if _, decided := p.permissionReviewDecisions[choice.Context]; decided {
		return refusal("this review already has a decision; open a fresh review for another choice", 409)
	}
	view, _, actor, err := buildPermissionReview(ctx, source, choice.Context)
	if err != nil || view.ID != choice.Shown {
		return refusal("the review changed; review again", 409)
	}
	if p.permissionReviewDecisions == nil {
		p.permissionReviewDecisions = map[permissionReviewContext]permissionReviewDecision{}
	}
	// Claim before any edit; interruption is uncertain and cannot be replayed.
	p.permissionReviewDecisions[choice.Context] = permissionReviewDecision{View: view, Result: permissionReviewResult{Outcome: "uncertain", Message: "This decision did not finish. Review Saved permissions before continuing.", Landed: []rights.RuleRecord{}}}
	if err := validatePermissionReceiptPath(startPermissionReviewReceipt); err != nil {
		return refusal(err.Error(), 409)
	}
	result, err := applyPermissionReview(ctx, source, choice)
	if err != nil {
		return refusal(err.Error(), 409)
	}
	if err := writePermissionReviewReceipt(startPermissionReviewReceipt, source, view, actor, result); err != nil {
		result.Outcome, result.Message = "uncertain", "The decision receipt could not be written. Review Saved permissions before continuing."
		p.permissionReviewDecisions[choice.Context] = permissionReviewDecision{View: view, Result: result}
		return result, 500, errors.New("The decision receipt could not be written. Saved permissions require review before continuing.")
	}
	p.permissionReviewDecisions[choice.Context] = permissionReviewDecision{View: view, Result: result}
	return result, 200, nil
}

func (p *servicePanel) permissionReviewRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/permission-review", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", 405)
			return
		}
		ctx, cancel := panelCall(r)
		defer cancel()
		proposal := reviewContextFromURL(r)
		if proposal == (permissionReviewContext{}) {
			proposal = startPermissionReview
		}
		if terminal, decided := p.settledPermissionReview(proposal); decided {
			if strings.Contains(r.Header.Get("Accept"), "application/json") {
				panelJSON(w, terminal.Result)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, panelPage(r, key, "rights", "", settledPermissionReviewFragment(terminal)))
			return
		}
		source := p.permissionReviewSource()
		if proposal.Feature == "picture-workflow" && proposal.Host == "" && proposal.valid() {
			list, err := source.Hosts(ctx)
			if err != nil {
				http.Error(w, "Image servers could not be read.", 503)
				return
			}
			hosts := reviewImageHosts(list)
			if len(hosts) != 1 {
				var choices strings.Builder
				choices.WriteString(`<h1>Choose an image server</h1><p>Choose the server this app may use to make pictures.</p>`)
				for i, host := range hosts {
					q := url.Values{"k": {key}, "feature": {proposal.Feature}, "app": {proposal.App}, "host": {host.Name}}
					label := fmt.Sprintf("Model server %d", i+1)
					if source.HostLabel != nil {
						label = source.HostLabel(ctx, host)
					}
					choices.WriteString(oaRowNav(label, "", "/permission-review?"+q.Encode()) + oaTechnicalDetails(`<p>`+html.EscapeString(host.Name)+`</p><p>`+html.EscapeString(host.DeclaredBy)+`</p>`))
				}
				if len(hosts) == 0 {
					choices.WriteString(`<p>No image server is available right now.</p>`)
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				_, _ = io.WriteString(w, panelPage(r, key, "rights", "", choices.String()))
				return
			}
		}
		view, _, _, err := buildPermissionReview(ctx, source, proposal)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			panelJSON(w, view)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, panelPage(r, key, "rights", "", permissionReviewFragment(view)))
	}))
	mux.HandleFunc("/permission-review/choice", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		var choice permissionReviewChoice
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		d.DisallowUnknownFields()
		if d.Decode(&choice) != nil || d.Decode(new(any)) != io.EOF || !choice.Context.valid() {
			http.Error(w, "invalid permission choice", 400)
			return
		}
		ctx, cancel := panelCall(r)
		defer cancel()
		result, status, err := p.decidePermissionReview(ctx, choice)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		panelJSON(w, result)
	}))
}

func permissionReviewFragment(v permissionReviewView) string {
	e := html.EscapeString
	choice, _ := json.Marshal(permissionReviewChoice{Context: v.Context, Shown: v.ID})
	var operations strings.Builder
	operations.WriteString(`<div class="oa-row"><div class="oa-row-text"><span class="oa-row-label">Allowed operations</span><ul>`)
	for _, sentence := range v.Sentences {
		operations.WriteString(`<li>` + e(sentence) + `</li>`)
	}
	operations.WriteString(`</ul></div></div>`)
	rows := []string{operations.String()}
	rows = append(rows, oaRow("Lifetime: One hour", "Each permission stops applying when it expires.", "", ""))
	if v.Host != nil {
		rows = append(rows, oaRow(v.HostLabel, "Selected image server.", "", ""))
	}
	details, _ := json.MarshalIndent(v, "", "  ")
	features := make([]map[string]string, 0, len(v.Rules))
	for i, rule := range v.Rules {
		features = append(features, map[string]string{"action": rule.Action, "resource": rule.Resource, "sentence": v.Sentences[i]})
	}
	featureJSON, _ := json.Marshal(features)
	rows = append(rows, oaTechnicalDetails(`<dl><dt>Program file</dt><dd>`+e(v.Program.Program)+`</dd><dt>Account</dt><dd>`+e(v.Subject.Account)+`</dd></dl><pre>`+e(string(details))+`</pre>`), oaRow("Your choice", "", `<button id="review-allow" class="oa-primary">Allow for one hour</button><button id="review-refuse">Refuse</button>`, "review-status"), `<div id="review-saved"></div>`)
	return `<h1>Review permissions</h1>` + oaGroup("permission-review", "Allow "+v.Title+" to use these features?", v.Disclosure, rows...) + `<script>` + serviceCommonScript + `const reviewChoice=` + string(choice) + `;const reviewFeatures=` + string(featureJSON) + `;` + permissionReviewScript + `</script>`
}

const permissionReviewScript = `
let reviewPending=false;
async function choosePermission(allow){
 if(reviewPending)return;
 reviewPending=true;el('review-allow').disabled=true;el('review-refuse').disabled=true;
 try{
  const result=await call('/permission-review/choice',{...reviewChoice,allow});
  el('review-status').textContent=result.message;
  const list=el('review-saved');list.replaceChildren();
  for(const record of result.landed||[]){const feature=reviewFeatures.find(f=>f.action===record.Rule.Action&&f.resource===record.Rule.Resource);const p=document.createElement('p');p.textContent=(feature?feature.sentence:'Saved permission')+' Ends '+new Date(record.Expires).toLocaleString()+'.';list.append(p)}
 }catch(e){el('review-status').textContent=e.message+' Review again before continuing.'}
}
el('review-allow').onclick=()=>choosePermission(true);
el('review-refuse').onclick=()=>choosePermission(false);
`

var startPermissionReview permissionReviewContext
var startPermissionReviewReceipt string

type permissionReviewLaunchFlags struct {
	Native, NativeAsks, Background, Tray, CheckWebView2 bool
	Operation, Receipt                                  string
}

func validatePermissionReviewLaunch(c permissionReviewContext, flags permissionReviewLaunchFlags) error {
	if c == (permissionReviewContext{}) {
		if flags.Receipt != "" {
			return errors.New("-review-receipt requires native permission review context")
		}
		return nil
	}
	if (!flags.Native && !flags.NativeAsks) || flags.Background || flags.Tray || flags.CheckWebView2 || flags.Operation != "" {
		return errors.New("permission review needs its standalone native presentation")
	}
	return nil
}

func permissionReviewLaunchMatches(launch, choice permissionReviewContext) bool {
	return launch.Feature == choice.Feature && launch.App == choice.App && launch.Program == choice.Program && (launch.Host == "" || launch.Host == choice.Host)
}

func parsePermissionReviewStart(page, feature, app, program, host string) (permissionReviewContext, error) {
	c := permissionReviewContext{Feature: feature, App: app, Program: program, Host: host}
	if c == (permissionReviewContext{}) {
		if page == "permission-review" {
			return c, errors.New("permission review needs a supported feature proposal")
		}
		return c, nil
	}
	if page != "permission-review" || !c.valid() {
		return c, fmt.Errorf("permission review needs -page permission-review and a supported feature with its app or local program")
	}
	return c, nil
}
