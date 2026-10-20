package main

import (
	"context"
	"fmt"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// Registration facts remain separate from policy. Only the authenticated local
// runtime's receiving account can associate a native descriptor with saved access.
type appsRegistration struct {
	Subject     *rightsclient.Subject   `json:"subject,omitempty"`
	Program     string                  `json:"program"`
	Name        string                  `json:"name"`
	Role        string                  `json:"role"`
	State       string                  `json:"state"`
	Provider    *fwire.DeclarationState `json:"provider,omitempty"`
	Application *fwire.ApplicationEntry `json:"application,omitempty"`
	// Title is the entry's one name (appEntryTitle), "(added itself)"
	// included while it awaits the person's Keep; SelfRegistered offers the
	// row's Confirm… action.
	Title string `json:"title,omitempty"`
	// Does is a provider's few words for what it does (providerDisplay).
	Does                 string                     `json:"does,omitempty"`
	SelfRegistered       bool                       `json:"self_registered,omitempty"`
	Choosers             *registrationwire.Choosers `json:"choosers,omitempty"`
	ChoosersStatus       string                     `json:"choosers_status,omitempty"`
	RegistrationRevision string                     `json:"registration_revision,omitempty"`
}

type appsRegistrationPage struct {
	Outcome       string             `json:"outcome"`
	Cursor        string             `json:"cursor,omitempty"`
	Entries       []appsRegistration `json:"entries"`
	Error         string             `json:"error,omitempty"`
	IdentityError string             `json:"identity_error,omitempty"`
}

func appsRegistrationSubject(account, program string) *rightsclient.Subject {
	if account == "" || !identity.ValidSubjectProgram(program) || !filepath.IsAbs(program) {
		return nil
	}
	program = identity.NormalizeSubjectProgram(program)
	if !identity.ValidSubjectProgram(program) || !filepath.IsAbs(program) {
		return nil
	}
	return &rightsclient.Subject{Account: account, Program: program}
}

func (p *servicePanel) appsRegistrations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	source, cursor := r.URL.Query().Get("source"), r.URL.Query().Get("cursor")
	wait, err := strconv.ParseInt(r.URL.Query().Get("wait_ms"), 10, 64)
	if err != nil || wait < 0 || wait > 30000 || (source != "providers" && source != "applications") || (cursor != "" && !rightsText(cursor, 4096)) {
		http.Error(w, "invalid registration observation", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(wait)*time.Millisecond+5*time.Second)
	defer cancel()
	machine := panelMachine()
	view := appsRegistrationPage{Outcome: "unavailable", Entries: []appsRegistration{}}
	// This is the caller echo of the same authenticated local machine used for
	// the registration read. A failed echo leaves readable facts unassociated.
	identityCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	caller, callerErr := machine.ObserveCaller(identityCtx)
	stop()
	account := ""
	if callerErr == nil && caller.Outcome == fwire.CallerOutcomeObserved && caller.Account != "" {
		account = caller.Account
	} else {
		view.IdentityError = "The runtime could not establish the receiving account. Registered entries are shown separately from saved access."
	}
	if source == "providers" {
		registry, resolveErr := machine.ResolveRegistry(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if resolveErr != nil {
			view.Error = resolveErr.Error()
		} else if snapshot, readErr := registry.Observe(ctx, cursor, time.Duration(wait)*time.Millisecond); readErr != nil {
			view.Error = readErr.Error()
		} else {
			view.Outcome, view.Cursor = snapshot.Outcome.String(), snapshot.Cursor
			if snapshot.Outcome == fwire.DeclarationListOutcomePage {
				for _, state := range snapshot.Declarations {
					if state.Role != fwire.DeclarationRoleProvider || state.Declaration.Program == "" {
						continue
					}
					title, does := providerDisplay(state.Declaration.Name, shippedProvider(state))
					if reservedTitle(title) {
						title = unregisteredProgramName(state.Declaration.Program)
					}
					view.Entries = append(view.Entries, appsRegistration{Subject: appsRegistrationSubject(account, state.Declaration.Program), Program: state.Declaration.Program,
						Name: state.Declaration.Name, Role: "provider", State: state.Readiness.String(), Provider: &state, Title: title, Does: does})
				}
			}
		}
	} else {
		applications, resolveErr := machine.ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if resolveErr != nil {
			view.Error = resolveErr.Error()
		} else if snapshot, readErr := applications.Observe(ctx, cursor, wait); readErr != nil {
			view.Error = readErr.Error()
		} else {
			view.Outcome, view.Cursor = snapshot.Outcome.String(), snapshot.Cursor
			if snapshot.Outcome == fwire.ApplicationOutcomePage || snapshot.Outcome == fwire.ApplicationOutcomeStale {
				for _, entry := range snapshot.Applications {
					if entry.Scope != fwire.ScopeLocal {
						continue
					}
					view.Entries = append(view.Entries, appsRegistration{Subject: appsRegistrationSubject(account, entry.Descriptor.Program), Program: entry.Descriptor.Program,
						Name: entry.Descriptor.Name, Role: "application", State: strconv.Itoa(len(entry.Instances)) + " current announcements", Application: &entry,
						Title: appEntryTitle(entry), SelfRegistered: entry.SelfRegistered})
				}
			}
		}
	}
	if source == "applications" && r.URL.Query().Get("selection") == "true" && (view.Outcome == "page" || view.Outcome == "stale") {
		var rows []registrationwire.Registration
		var readErr error
		if account == "" || view.Outcome != "page" {
			readErr = fmt.Errorf("registration identity or snapshot unavailable")
		} else {
			binding, err := machine.ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
			if err != nil {
				readErr = err
			} else {
				client := applications.NewApplicationsClient(binding.Transport().WithContext(ctx))
				rows, readErr = selectionRegistrationRows(client.Observe)
				fresh, err := machine.ObserveCaller(ctx)
				if err != nil || fresh.Outcome != fwire.CallerOutcomeObserved || fresh.Account != account || fresh.Program != caller.Program {
					readErr = fmt.Errorf("registration caller changed")
				}
			}
		}
		if readErr == nil {
			directory, err := machine.ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
			if err != nil {
				readErr = err
			} else if fresh, err := directory.Observe(ctx, "", 0); err != nil || fresh.Outcome != fwire.ApplicationOutcomePage || fresh.Cursor != view.Cursor {
				readErr = fmt.Errorf("registration snapshot changed")
			}
		}
		projectChoosers(&view, rows, readErr, account)
	}
	if r.Context().Err() == nil {
		w.Header().Set("Cache-Control", "no-store")
		panelJSON(w, view)
	}
}

// selectionRegistrationRows uses the existing bounded, permission-filtered @3
// snapshot. A stale/failed partial snapshot supplies no declaration metadata.
func selectionRegistrationRows(observe func(applications.Query) (applications.Page, error)) ([]registrationwire.Registration, error) {
	rows := []registrationwire.Registration{}
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i <= 64; i++ {
		page, err := observe(applications.Query{Cursor: cursor, Limit: 64})
		if err != nil {
			return nil, err
		}
		if page.Outcome != applications.OutcomePage {
			return nil, fmt.Errorf("native registration %s", page.Outcome)
		}
		if len(page.Registrations) == 0 {
			if cursor != "" && page.Cursor != cursor {
				return nil, fmt.Errorf("native registration changed")
			}
			return rows, nil
		}
		if page.Cursor == "" || page.Cursor == cursor || len(rows)+len(page.Registrations) > 64 {
			return nil, fmt.Errorf("native registration bounds")
		}
		for _, row := range page.Registrations {
			for _, facet := range row.Spec.Applications {
				if seen[facet.Name] {
					return nil, fmt.Errorf("native registration repeated")
				}
				seen[facet.Name] = true
			}
		}
		rows = append(rows, page.Registrations...)
		cursor = page.Cursor
	}
	return nil, fmt.Errorf("native registration bounds")
}

// projectChoosers joins metadata only to the existing caller-visible
// directory entry's exact name/program/account. It never creates another @2 row.
func projectChoosers(view *appsRegistrationPage, rows []registrationwire.Registration, readErr error, account string) {
	for i := range view.Entries {
		entry := &view.Entries[i]
		entry.Choosers = nil
		entry.RegistrationRevision = ""
		entry.ChoosersStatus = "unavailable"
		if readErr != nil || account == "" || entry.Subject == nil || entry.Subject.Account != account || !identity.SameSubjectProgram(entry.Subject.Program, entry.Program) {
			continue
		}
		entry.ChoosersStatus = "absent"
		for _, row := range rows {
			manifest, _, found := nativeApplicationFacet(&row, entry.Name)
			if !found {
				continue
			}
			if !identity.SameSubjectProgram(row.Spec.Program, entry.Program) || row.Revision == "" || row.ShadowedBy != nil {
				entry.ChoosersStatus = "unavailable"
				break
			}
			entry.RegistrationRevision = row.Revision
			entry.Choosers = manifest.Choosers
			if manifest.Choosers != nil {
				entry.ChoosersStatus = "declared"
			}
			break
		}
	}
}
