package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
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
	Title          string `json:"title,omitempty"`
	SelfRegistered bool   `json:"self_registered,omitempty"`
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
					view.Entries = append(view.Entries, appsRegistration{Subject: appsRegistrationSubject(account, state.Declaration.Program), Program: state.Declaration.Program,
						Name: state.Declaration.Name, Role: "provider", State: state.Readiness.String(), Provider: &state})
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
	if r.Context().Err() == nil {
		w.Header().Set("Cache-Control", "no-store")
		panelJSON(w, view)
	}
}
