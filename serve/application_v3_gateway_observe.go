package main

import (
	"context"
	"encoding/json"
	"errors"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	watch "github.com/openabstractions/abstraction-watch/go"
	"strconv"
	"strings"
	"time"
)

func (g *applicationV3Gateway) registrationRows(ctx context.Context, c applicationV3Caller, filter string) ([]registration.Registration, <-chan struct{}, time.Duration, error) {
	views, changed, until, outcome := g.directory.applicationV3Snapshot(ctx, c.subject, "")
	if outcome != legacy.ApplicationOutcomePage {
		return nil, changed, until, errors.New(outcome.String())
	}
	visible := map[string]applicationV3RegistrationView{}
	for _, view := range views {
		visible[view.Name] = view
	}
	records, _, _, _, _, err := g.directory.programRegistrationsFor(ctx, true)
	if err != nil {
		return nil, changed, until, err
	}
	rows := []registration.Registration{}
	covered := map[string]bool{}
	for _, record := range records {
		if len(record.Spec.Applications) == 0 {
			continue
		}
		allowed, selected := true, filter == ""
		record.Applications = []registration.ApplicationReading{}
		for _, facet := range record.Spec.Applications {
			view, ok := visible[facet.Name]
			// A shared record is indivisible. Every application facet must be in
			// the caller's current read scope before its names are disclosed.
			if !ok {
				allowed = false
				break
			}
			if view.Revision != record.Revision {
				return nil, changed, until, errors.New("registration changed")
			}
			if facet.Name == filter {
				selected = true
			}
			reading := registration.ApplicationReading{Name: facet.Name, ManifestDigest: view.ManifestDigest, Instances: view.Instances, OwnedInstances: []string{}}
			for _, instance := range view.Instances {
				if s := g.session(view.Name, instance.Instance); s != nil && s.bindingValue().manifestRevision == record.Revision && s.guard(ctx, s.bindingValue()) == nil {
					reading.OwnedInstances = append(reading.OwnedInstances, instance.Instance)
				}
			}
			record.Applications = append(record.Applications, reading)
		}
		for _, facet := range record.Spec.Applications {
			covered[facet.Name] = true
		}
		if allowed && selected {
			rows = append(rows, record)
		}
	}
	for name := range visible {
		if !covered[name] && (filter == "" || filter == name) {
			return nil, changed, until, errRegistrationEncodedSize
		}
	}
	return rows, changed, until, nil
}

func (g *applicationV3Gateway) rowsCursor(c applicationV3Caller, filter string, rows []registration.Registration) string {
	raw, _ := json.Marshal(struct {
		Epoch, Account, Program, Filter string
		Rows                            []registration.Registration
	}{g.epoch, c.subject.Account, c.subject.Program, filter, rows})
	return g.epoch + "." + applicationV3BodyDigest(raw)
}

func (g *applicationV3Gateway) observe(ctx context.Context, c applicationV3Caller, query applications.Query) applications.Page {
	refuse := func(outcome applications.Outcome, reason string) applications.Page {
		return applications.Page{Outcome: outcome, Registrations: []registration.Registration{}, Reason: reason}
	}
	if query.Limit < 1 || query.Limit > 64 || query.WaitMs < 0 || query.WaitMs > 30000 {
		return refuse(applications.OutcomeInvalid, "bounds")
	}
	if !c.valid(g.directory.owner) {
		return refuse(applications.OutcomeForbidden, "caller")
	}
	if query.Application != "" && !providerName.MatchString(query.Application) {
		return refuse(applications.OutcomeInvalid, "application")
	}
	var page applications.Page
	var expiry time.Time
	var selected string
	read := func() (bool, error) {
		if !c.valid(g.directory.owner) {
			page = refuse(applications.OutcomeForbidden, "caller")
			return true, nil
		}
		rows, _, until, err := g.registrationRows(ctx, c, query.Application)
		expiry = time.Now().Add(until)
		if err != nil {
			page = refuse(applications.OutcomeUnavailable, "directory")
			if errors.Is(err, errRegistrationEncodedSize) {
				page.Reason = errRegistrationEncodedSize.Error()
			}
			return true, nil
		}
		selected = g.rowsCursor(c, query.Application, rows)
		offset := 0
		if query.Cursor != "" {
			parts := strings.Split(query.Cursor, ".")
			if len(parts) != 3 || parts[0] != g.epoch || parts[0]+"."+parts[1] != selected {
				page = refuse(applications.OutcomeStale, "")
				return true, nil
			}
			offset, err = strconv.Atoi(parts[2])
			if err != nil || offset < 0 || offset > len(rows) {
				page = refuse(applications.OutcomeStale, "")
				return true, nil
			}
		}
		page = applications.Page{Outcome: applications.OutcomePage, Registrations: []registration.Registration{}}
		for offset < len(rows) && len(page.Registrations) < int(query.Limit) {
			page.Registrations = append(page.Registrations, rows[offset])
			page.Cursor = selected + "." + strconv.Itoa(offset+1)
			if !applicationV3ReplyFits(page) {
				page.Registrations = page.Registrations[:len(page.Registrations)-1]
				if len(page.Registrations) == 0 {
					page = refuse(applications.OutcomeUnavailable, "bound:reply_bytes")
					return true, nil
				}
				break
			}
			offset++
		}
		page.Cursor = selected + "." + strconv.Itoa(offset)
		if !applicationV3ReplyFits(page) {
			page = refuse(applications.OutcomeUnavailable, "bound:reply_bytes")
			return true, nil
		}
		return page.Cursor != query.Cursor, nil
	}
	outcome, err := g.directory.gate.Wait(ctx, time.Duration(query.WaitMs)*time.Millisecond, watch.Probe{Read: read, Recheck: func() error {
		if !c.valid(g.directory.owner) {
			return errors.New("caller")
		}
		rows, _, _, err := g.registrationRows(ctx, c, query.Application)
		if err != nil {
			return err
		}
		if page.Outcome == applications.OutcomePage && g.rowsCursor(c, query.Application, rows) != selected {
			return errors.New("directory changed")
		}
		return nil
	}, Deadline: func() time.Time { return expiry }})
	if err != nil || outcome == watch.Busy || outcome == watch.Failed {
		return refuse(applications.OutcomeUnavailable, "directory")
	}
	return page
}
