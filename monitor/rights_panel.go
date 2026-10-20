package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	facade "github.com/openabstractions/abstraction-facade/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// rightsActionPlain names every rights action a plain phrase, for the Status
// page's "Show rules" list and its second list of actions no rule names yet
// (task 2026-09-23, third first-time-visitor walkthrough: the list otherwise
// prints raw action names like "abstraction.credentials/holder.manage"). It
// covers every action installationRules (serve/runtime_rights.go) writes,
// worded as capability then action in the decided words (RENAME-PLAN §3,
// Panel step 7: S11 and D11, so "inference server manage" and "router
// servers" where the wire still says host), and every action serve/runtime_*.go registers into
// o.RightsActions beyond those (rights_catalogue_test.go enumerates that
// full catalogue and fails when this map falls behind it, and fails when a
// page's <span title="action">phrase</span> says anything else). An action
// missing here still shows a phrase, oaActionFallback's generic one
// (panel_shell.go), with the raw id in parentheses; the raw name is also
// always carried as a title attribute, so nothing this runtime knows is
// ever shown as only an id.
var rightsActionPlain = map[string]string{
	host.ConfigEditAction:                        "config rewrite",
	host.LogHistoryAction:                        "logging history",
	host.JobSubmitAction:                         "jobs submit",
	host.JobCancelAction:                         "jobs cancel",
	host.JobInventoryAction:                      "jobs all",
	host.ModelLookupAction:                       "model resolve",
	routerservice.ActionInventory:                "router servers",
	routerservice.ActionRoute:                    "router pick",
	host.ResourceTableReadAction:                 "resource table read",
	resourceservice.ActionHold:                   "resource hold",
	resourceservice.ActionYield:                  "resource yield",
	credentials.ActionManage:                     "credentials manage",
	credentials.ActionRead:                       "credentials list",
	credentials.ActionSecretRead:                 "read saved secret value",
	credentials.ActionSecretCreate:               "create saved secret",
	credentials.ActionSecretRotate:               "rotate saved secret value",
	credentials.ActionSecretRevoke:               "revoke saved secret",
	credentials.ActionSecretMetadataRead:         "read named secret metadata",
	inference.ActionHostManage:                   "inference server manage",
	inference.ActionKeyIssue:                     "inference key issue",
	inference.ActionAuditRead:                    "inference audit read",
	inference.ActionComplete:                     "inference complete",
	"abstraction.inference/selection.manage":     "inference selection manage",
	"abstraction.inference/selection.manage_for": "inference selection manage for other apps",
	"abstraction.facade/application.manage":      "application manage",
	"abstraction.facade/provider.manage":         "provider manage",
	// abstraction.credentials/apply, abstraction.storage/inventory.provide,
	// the three actions below (task 2026-09-23, sixth first-time visitor,
	// finding 2) and every abstraction.facade/application.* and
	// abstraction.storage/* action below it are named by their exact string
	// for the same reason as the two facade actions above: each is declared
	// in serve, a main package the monitor package cannot import
	// (credentials.go's setApplyRule, provider_inventory.go's
	// ActionInventoryProvide, applications.go's ActionApplication*,
	// runtime_inventory.go's ActionInventoryRead, runtime_lending.go's
	// ActionLend). rights_catalogue_test.go's servePackageActions repeats
	// this same list, in sync by hand for the same reason.
	"abstraction.credentials/apply":           "credentials apply",
	"abstraction.storage/inventory.provide":   "storage inventory provide",
	"abstraction.storage/inventory.read":      "storage inventory read",
	"abstraction.storage/lend":                "storage lend",
	"abstraction.facade/application.read":     "application read",
	"abstraction.facade/application.announce": "application announce",
	"abstraction.facade/application.activate": "application activate",
	// Named by the register account-wide default (abstraction-rights
	// account_defaults.go, RIGHTS-R22).
	"abstraction.facade/application.register_self": "application add itself",
	// The four actions below are abstraction-rights/go's own generic
	// catalogue (rwire.ResourceActions, rights_catalogue_test.go), imported
	// there rather than named by a per-capability constant: no capability
	// package here exports these four as named constants of its own.
	"abstraction.storage/content.read":    "storage content read",
	"abstraction.storage/content.write":   "storage content write",
	"abstraction.storage/content.observe": "storage content observe",
	"abstraction.storage/content.remove":  "storage content remove",
	// abstraction.asks/question.ask (abstraction-asks/go's own
	// ResourceActions) has no named Go constant of its own either.
	"abstraction.asks/question.ask":      "show app requests",
	"abstraction.asks/question.answer":   "questions answer",
	"abstraction.asks/notification.post": "show notifications",
}

// rightsEdit is one conditional policy change the person made in the panel.
// Account and program name the administrative target; they prove nothing about
// the panel or the target program.
type rightsEdit struct {
	Edit            string `json:"edit"`
	Revision        string `json:"revision"`
	Account         string `json:"account"`
	Program         string `json:"program"`
	Action          string `json:"action"`
	Resource        string `json:"resource"`
	Permit          bool   `json:"permit"`
	TTLMS           int64  `json:"ttl_ms,omitempty"`
	Why             string `json:"why,omitempty"`
	ResourceProfile string `json:"resource_profile,omitempty"`
	Scope           string `json:"scope,omitempty"`
	CredentialName  string `json:"credential_name,omitempty"`
	BundleRef       string `json:"bundle_ref,omitempty"`
	BundleOriginID  string `json:"bundle_origin_id,omitempty"`
}

func rightsText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// policyRuleView is one listed rule plus who set it, read individually
// through the same operator: ListPolicyContext's bulk page carries no
// provenance of its own (rightsclient.PolicyRule has no SetBy), while
// ReadRuleContext's single-rule record does. Field names match
// rightsclient.PolicyRule/PolicyPage's own (untagged, so their Go field name
// is the wire name) so the existing list rendering keeps working unchanged.
type policyRuleView struct {
	Subject  rightsclient.Subject
	Action   string
	Resource string
	Permit   bool
	SetBy    *rightsclient.Subject `json:"SetBy,omitempty"`
	SetAt    string                `json:"SetAt,omitempty"`
	Why      string                `json:"Why,omitempty"`
	Expires  string                `json:"Expires,omitempty"`
	Expired  bool                  `json:"Expired"`
	Selector *selectorRuleView     `json:"Selector,omitempty"`
}

// Target bytes stay in the native adapter. Known owning codecs expose only
// safe target metadata; unknown profiles remain opaque and view-only.
type selectorRuleView struct {
	Permission rwire.PermissionKey
	Scope      string
	Name       string `json:"Name,omitempty"`
	Target     string
	Editable   bool
	CanGrant   bool
	Origin     *bundleOriginView `json:"Origin,omitempty"`
}

type bundleOriginView struct {
	ID            string
	BundleRef     string
	Digest        string
	Members       []selectorRuleView
	LinkedMembers []selectorRuleView
	Original      int
	Remaining     int
	State         string
}

type permissionDefinitionView struct {
	rwire.SelectorPermissionDefinition
	Editable       bool
	CodecAvailable bool
}

type permissionBundleView struct {
	rwire.PermissionBundleDefinition
	Editable bool
}

type selectorEditView struct {
	Outcome      string
	Revision     string
	Current      []policyRuleView
	BundleOrigin *bundleOriginView `json:"BundleOrigin,omitempty"`
}

func presentSelectorEdit(edit rwire.SelectorEdit) selectorEditView {
	view := selectorEditView{Outcome: edit.Outcome.String(), Revision: edit.Revision, Current: []policyRuleView{}}
	now := time.Now()
	for _, record := range edit.Current {
		selector := presentSelector(record.Rule.Selection)
		setter := record.SetBy
		row := policyRuleView{Subject: record.Rule.Subject, Action: record.Rule.Selection.Permission.Action, Resource: selector.Target, Permit: record.Rule.Permit, SetBy: &setter, SetAt: record.SetAt, Why: record.Why, Expires: record.Expires, Selector: &selector}
		if record.Expires != "" {
			at, err := time.Parse(time.RFC3339Nano, record.Expires)
			row.Expired = err != nil || !at.After(now)
		}
		view.Current = append(view.Current, row)
	}
	if edit.BundleOrigin != nil {
		origin := edit.BundleOrigin
		view.BundleOrigin = &bundleOriginView{ID: origin.ID, BundleRef: origin.Expansion.BundleRef, Digest: origin.Expansion.DefinitionDigest, Original: len(origin.Expansion.Selections), State: "original expansion; current rules are listed separately"}
		for _, selection := range origin.Expansion.Selections {
			view.BundleOrigin.Members = append(view.BundleOrigin.Members, presentSelector(selection))
		}
	}
	return view
}

type policyPageView struct {
	Outcome  string
	Revision string
	Catalog  []string
	// ActionPlain names known actions in plain words (rightsActionPlain),
	// sent alongside Catalog and Rules so the page never has to guess a
	// phrase for a raw action name it did not itself invent.
	ActionPlain       map[string]string
	Descriptions      map[string]rightsclient.DescribedAction `json:"Descriptions,omitempty"`
	Rules             []policyRuleView
	Next              string
	Complete          bool
	SelectorAvailable bool
	SelectorStatus    string                     `json:"SelectorStatus,omitempty"`
	Definitions       []permissionDefinitionView `json:"Definitions,omitempty"`
	// Names titles the page's programs (program_titles.go): OpenAbstractions'
	// own by product name, registered apps by their registered title.
	Names   map[string]string      `json:"Names,omitempty"`
	Bundles []permissionBundleView `json:"Bundles,omitempty"`
}

// presentPolicyPage enriches a listed page with who set each rule (task
// 2026-09-23, "Show rules": each rule renders as "<plain phrase>: allowed for
// <program>, set at installation"). One extra read per listed rule, bounded to
// the page's own limit (at most 16 here), over the same resolved operator.
func presentPolicyPage(ctx context.Context, operator *rightsclient.Operator, page rightsclient.PolicyPage) policyPageView {
	view := policyPageView{Outcome: page.Outcome.String(), Revision: page.Revision, Catalog: page.Catalog, ActionPlain: rightsActionPlain, Next: page.Next, Complete: page.Complete, Rules: []policyRuleView{}}
	for _, rule := range page.Rules {
		row := policyRuleView{Subject: rule.Subject, Action: rule.Action, Resource: rule.Resource, Permit: rule.Permit}
		if read, err := operator.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource); err == nil && read.Revision == page.Revision && read.Record != nil {
			setBy := read.Record.SetBy
			row.SetBy, row.SetAt, row.Why, row.Expires = &setBy, read.Record.SetAt, read.Record.Why, read.Record.Expires
			row.Expired = read.Outcome.String() == "expired"
		}
		view.Rules = append(view.Rules, row)
	}
	return view
}

func (e rightsEdit) valid() bool {
	if e.typed() {
		return e.validSelectorEdit()
	}
	target := rightsText(e.Revision, 128) && rightsText(e.Account, 128) && rightsText(e.Program, 4096) &&
		identity.ValidSubjectProgram(e.Program) && rightsText(e.Action, 128) && rightsText(e.Resource, 1024)
	switch e.Edit {
	case "set":
		return target && e.TTLMS >= 0 && e.TTLMS <= 31536000000 &&
			(e.Why == "" || rightsText(e.Why, 256))
	case "revoke":
		return target && !e.Permit && e.TTLMS == 0 && e.Why == ""
	}
	return false
}

func (e rightsEdit) typed() bool {
	return e.ResourceProfile != "" || e.BundleRef != "" || e.BundleOriginID != "" || e.Scope != "" || e.CredentialName != ""
}

func (e rightsEdit) selector() (rwire.ResourceSelector, error) {
	kind, ok := rwire.ParseResourceSelectorKind(e.Scope)
	if !ok {
		return rwire.ResourceSelector{}, errors.New("invalid target scope")
	}
	selector := rwire.ResourceSelector{Kind: kind}
	if kind == rwire.ResourceSelectorKindOne {
		target, err := credentials.PrepareCredentialTarget(e.CredentialName)
		if err != nil {
			return rwire.ResourceSelector{}, err
		}
		selector.Target = target
	} else if e.CredentialName != "" {
		return rwire.ResourceSelector{}, errors.New("all scope has no individual name")
	}
	return selector, nil
}

func (e rightsEdit) validSelectorEdit() bool {
	if !rightsText(e.Revision, 128) || !rightsText(e.Account, 128) || !rightsText(e.Program, 4096) || !identity.ValidSubjectProgram(e.Program) || e.Resource != "" {
		return false
	}
	if e.Edit == "remove_bundle" {
		return rightsText(e.BundleOriginID, 128) && e.BundleRef == "" && e.ResourceProfile == "" && e.Action == "" && e.Scope == "" && e.CredentialName == "" && !e.Permit && e.TTLMS == 0 && e.Why == ""
	}
	if e.BundleOriginID != "" {
		return false
	}
	if _, err := e.selector(); err != nil {
		return false
	}
	if e.BundleRef != "" {
		if e.BundleRef != credentials.FullControlBundle || e.Edit != "set" || !e.Permit || e.Action != "" || e.ResourceProfile != "" {
			return false
		}
	} else if !editableCredentialPermission(rwire.PermissionKey{Action: e.Action, ResourceProfile: e.ResourceProfile}) {
		return false
	}
	switch e.Edit {
	case "set":
		return e.TTLMS >= 0 && e.TTLMS <= 31536000000 && (e.Why == "" || rightsText(e.Why, 256))
	case "revoke":
		return !e.Permit && e.TTLMS == 0 && e.Why == ""
	}
	return false
}

func selectorUnsupported(err error) bool {
	var resolution *facade.ResolutionError
	var service *rightsclient.ServiceError
	return errors.As(err, &resolution) && string(resolution.Status) == "incompatible" || errors.As(err, &service) && service.Code == rwire.ServiceErrorCodeUnknownMethod
}

type credentialNamesView struct {
	Outcome  string
	Names    []string
	Next     string
	Complete bool
}

// Suggestions project public Holder.List metadata. The Credentials owner
// defines supported saved kinds; this list supplies no membership or grant.
func presentCredentialNames(page cwire.MetadataPage) credentialNamesView {
	view := credentialNamesView{Outcome: page.Outcome.String(), Names: []string{}, Next: page.Next, Complete: page.Complete}
	if page.Outcome == cwire.PageOutcomePage {
		for _, record := range page.Records {
			if credentials.SavedSecretKind(record.Kind) {
				view.Names = append(view.Names, record.Name)
			}
		}
	}
	return view
}

// rights lists the decision policy and applies conditional grants, denies and
// revocations through the resolved operator service. The service's operator
// authorization decides whether this panel may act; typed outcomes are returned
// unchanged. Resolution and transport failures report unavailability.
func (p *servicePanel) rights(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var edit rightsEdit
	if r.Method == "POST" {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&edit)
		var extra any
		if decodeErr != nil || decoder.Decode(&extra) != io.EOF || !edit.valid() {
			http.Error(w, "invalid rights edit", 400)
			return
		}
	} else if cursor := r.URL.Query().Get("cursor"); len(cursor) > 256 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid rights cursor", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	if r.Method == "GET" && r.URL.Query().Get("credential_names") == "1" {
		holder, err := panelMachine().ResolveCredentials(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			panelError(w, err)
			return
		}
		page, err := holder.List(ctx, r.URL.Query().Get("cursor"), 32)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, presentCredentialNames(page))
		return
	}
	if edit.typed() {
		p.editSelectorRights(ctx, w, edit)
		return
	}
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	if edit.Edit != "" {
		p.logAction(ctx, "rights."+edit.Edit, map[string]string{"rights.revision": edit.Revision, "rights.action": edit.Action})
	}
	subject := rightsclient.Subject{Account: edit.Account, Program: edit.Program}
	if edit.Edit == "" {
		page, err := operator.ListPolicyContext(ctx, r.URL.Query().Get("cursor"), 16)
		if err != nil {
			panelError(w, err)
			return
		}
		view := presentPolicyPage(ctx, operator, page)
		if page.Outcome.String() == "page" {
			reader, err := panelMachine().ResolveRights(ctx, facade.Requirements{Scope: facade.ScopeLocal})
			if err != nil {
				panelError(w, err)
				return
			}
			view = joinPolicyDescriptions(ctx, view, reader)
			if view.Outcome == "page" && r.URL.Query().Get("cursor") == "" {
				selectorReader, err := panelMachine().ResolveRightsSelector(ctx, facade.Requirements{Scope: facade.ScopeLocal})
				if selectorUnsupported(err) {
					view.SelectorStatus = "unsupported"
				} else if err != nil {
					panelError(w, err)
					return
				} else {
					selectorOperator, err := panelMachine().ResolveRightsSelectorOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
					if err != nil {
						panelError(w, err)
						return
					}
					view = joinPolicySelectors(ctx, view, selectorReader, selectorOperator)
				}
			}
			// Every program the page shows is titled here, in one batch:
			// rule subjects, who set each rule, and the programs an
			// action description names.
			programs := make([]string, 0, 2*len(view.Rules))
			for _, rule := range view.Rules {
				programs = append(programs, rule.Subject.Program)
				if rule.SetBy != nil {
					programs = append(programs, rule.SetBy.Program)
				}
			}
			for _, described := range view.Descriptions {
				if described.PublishedBy != nil {
					programs = append(programs, described.PublishedBy.Program)
				}
				if described.Description != nil {
					programs = append(programs, described.Description.FeatureApp.Program)
				}
			}
			names := p.programTitler(ctx)
			for _, entry := range names.registered {
				programs = append(programs, entry.Descriptor.Program)
			}
			view.Names = names.names(programs)
		}
		panelJSON(w, view)
		return
	}
	var result any
	switch edit.Edit {
	case "set":
		rule := rightsclient.PolicyRule{Subject: subject, Action: edit.Action, Resource: edit.Resource, Permit: edit.Permit}
		if edit.TTLMS != 0 || edit.Why != "" {
			result, err = operator.SetRuleForContext(ctx, edit.Revision, rule, time.Duration(edit.TTLMS)*time.Millisecond, edit.Why)
		} else {
			result, err = operator.SetRuleContext(ctx, edit.Revision, rule)
		}
	case "revoke":
		result, err = operator.RevokeRuleContext(ctx, edit.Revision, subject, edit.Action, edit.Resource)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}

// Typed edits use only the explicitly resolved @2 operator. The owner codec
// prepares name metadata; no secret reader or legacy write is involved.
func (p *servicePanel) editSelectorRights(ctx context.Context, w http.ResponseWriter, edit rightsEdit) {
	operator, err := panelMachine().ResolveRightsSelectorOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	subject := rightsclient.Subject{Account: edit.Account, Program: edit.Program}
	p.logAction(ctx, "rights."+edit.Edit, map[string]string{"rights.revision": edit.Revision, "rights.action": edit.Action, "rights.profile": edit.ResourceProfile})
	var result rwire.SelectorEdit
	if edit.Edit == "remove_bundle" {
		result, err = operator.RemoveBundleContext(ctx, edit.Revision, subject, edit.BundleOriginID)
	} else {
		selector, _ := edit.selector()
		if edit.BundleRef != "" {
			result, err = operator.ApplyBundleContext(ctx, edit.Revision, subject, edit.BundleRef, selector, time.Duration(edit.TTLMS)*time.Millisecond, edit.Why)
		} else {
			selection := rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: edit.Action, ResourceProfile: edit.ResourceProfile}, Selector: selector}
			if edit.Edit == "set" {
				result, err = operator.SetSelectorRuleContext(ctx, edit.Revision, rwire.SelectorRule{Subject: subject, Selection: selection, Permit: edit.Permit}, time.Duration(edit.TTLMS)*time.Millisecond, edit.Why)
			} else {
				result, err = operator.RemoveSelectorRuleContext(ctx, edit.Revision, subject, selection)
			}
		}
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, presentSelectorEdit(result))
}
