package main

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func (g *applicationV3Gateway) commands(ctx context.Context, c applicationV3Caller, query appmodel.CommandsQuery) applications.CommandsResult {
	answer := &appmodel.CommandListing{Application: query.Application, Instance: query.Instance, Commands: []appmodel.CommandSpec{}}
	refuse := func(outcome, reason string) applications.CommandsResult {
		answer.Outcome, _ = appmodel.ParseReadingOutcome(outcome)
		answer.Reason = reason
		return applications.CommandsResult{Outcome: applications.OutcomePage, Answer: answer}
	}
	if !c.valid(g.directory.owner) {
		return applications.CommandsResult{Outcome: applications.OutcomeForbidden, Reason: "caller"}
	}
	k, err := g.load(ctx, c.peer, query.Application)
	if err != nil {
		if errors.Is(err, errApplicationV3NotFound) {
			return refuse("not_found", "application:"+query.Application)
		}
		return refuse("unavailable", "manifest")
	}
	defer k.close()
	query.Application = k.manifest.Name
	answer.Application = query.Application
	decision := g.policy.decide(ctx, c.subject, "abstraction.facade/command.read", "app:"+query.Application)
	if decision.Outcome.String() == "denied" {
		return refuse("not_permitted", "")
	}
	if decision.Outcome.String() == "unavailable" {
		return refuse("unavailable", "rights")
	}
	if !k.live[query.Instance] {
		return refuse("stale", "instance:"+query.Instance)
	}
	if out, why := g.permission(ctx, c, k, appmodel.CommandClassRead, true, "Commands"); out != "" {
		return refuse(out, why)
	}
	s := g.session(query.Application, query.Instance)
	if s == nil || s.bindingValue().manifestRevision != k.revision {
		return refuse("unavailable", "owned_host")
	}
	d, _, err := g.description(ctx, c, k, s, func(ctx context.Context) error { return g.quiet(ctx, c, k, "abstraction.facade/command.read") })
	if err != nil {
		out, reason := applicationV3DescriptionFailure(err)
		return refuse(out, reason)
	}
	for _, spec := range d.Commands {
		if spec.Name == ownedBackendStop && isWorkflowApplication(k.manifest.Name) && !s.startedFor(c) {
			continue
		}
		for _, decl := range k.manifest.Offers.Commands {
			if spec.Name == decl.Name && spec.Class == decl.Class {
				answer.Commands = append(answer.Commands, spec)
				break
			}
		}
	}
	answer.Outcome = appmodel.ReadingOutcomePage
	if g.quiet(ctx, c, k, "abstraction.facade/command.read") != nil {
		return applications.CommandsResult{Outcome: applications.OutcomeForbidden, Reason: "rights"}
	}
	result, err := fitApplicationV3Reply("Commands", applications.CommandsResult{Outcome: applications.OutcomePage, Answer: answer})
	if err != nil {
		return applications.CommandsResult{Outcome: applications.OutcomeUnavailable, Reason: err.Error()}
	}
	return result
}

// openVerbs projects the same kept declaration that Activate validates.
func (g *applicationV3Gateway) openVerbs(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, agent, openOnly bool) []appmodel.ListedOpenVerb {
	if k.state != "enabled" || len(k.manifest.Verbs) == 0 {
		return nil
	}
	resource := "app:" + k.manifest.Name
	activation := g.policy.decide(ctx, c.subject, ActionApplicationActivate, resource).Outcome
	operator := activation == rights.DecisionOutcomePermitted
	if !operator && (!agent || activation != rights.DecisionOutcomeNotGranted) {
		return nil
	}
	var verbs []appmodel.ListedOpenVerb
	for _, verb := range k.manifest.Verbs {
		if (openOnly || !operator) && !applicationV3OpenClass(verb.Class) {
			continue
		}
		decision := g.policy.decide(ctx, c.subject, "abstraction.facade/command."+verb.Class.String(), resource).Outcome
		if openClassMayProceed(decision) {
			verbs = append(verbs, appmodel.ListedOpenVerb{
				Name: verb.Name, Description: "Use the " + verb.Name + " activation verb.", InputSchema: appmodel.Raw(verb.InputSchema),
			})
		}
	}
	if len(verbs) > 0 && (k.recheck(ctx) != nil || c.recheck() != nil) {
		return nil
	}
	return verbs
}

// describeOpen is applications@3 DescribeOpen: an open handle's state, for
// the caller it was given to only (architect ruling 2026-11-22). It reads no
// application and asks nothing.
func (g *applicationV3Gateway) describeOpen(c applicationV3Caller, query appmodel.OpenHandleQuery) applications.DescribeResult {
	if !c.valid(g.directory.owner) {
		return applications.DescribeResult{Outcome: applications.OutcomeForbidden, Reason: "caller"}
	}
	answer := &appmodel.ApplicationListing{Outcome: appmodel.ReadingOutcomePage, Applications: []appmodel.ListedApplication{}}
	state, ok := g.later.state(query.OpenHandle, c.subject)
	if !ok || query.Application != "" && query.Application != state.Application {
		answer.Outcome, answer.Reason = appmodel.ReadingOutcomeNotFound, "open_handle"
		return applications.DescribeResult{Outcome: applications.OutcomePage, Answer: answer}
	}
	answer.Open = &state
	return applications.DescribeResult{Outcome: applications.OutcomePage, Answer: answer}
}

func (g *applicationV3Gateway) describe(ctx context.Context, c applicationV3Caller, query appmodel.ApplicationsQuery) applications.DescribeResult {
	if !c.valid(g.directory.owner) {
		return applications.DescribeResult{Outcome: applications.OutcomeForbidden, Reason: "caller"}
	}
	answer := &appmodel.ApplicationListing{Outcome: appmodel.ReadingOutcomePage, Applications: []appmodel.ListedApplication{}}
	var values []*applicationV3Kept
	var err error
	if query.Application != "" {
		var k *applicationV3Kept
		k, err = g.load(ctx, c.peer, query.Application)
		if err == nil {
			values = []*applicationV3Kept{k}
		}
	} else {
		values, err = g.list(ctx, c.peer)
	}
	if err != nil {
		answer.Outcome = appmodel.ReadingOutcomeUnavailable
		answer.Reason = "directory"
		if errors.Is(err, errApplicationV3NotFound) {
			answer.Outcome = appmodel.ReadingOutcomeNotFound
			answer.Reason = "application:" + query.Application
		}
		return applications.DescribeResult{Outcome: applications.OutcomePage, Answer: answer}
	}
	defer func() {
		for _, v := range values {
			v.close()
		}
	}()
	sort.Slice(values, func(i, j int) bool { return values[i].manifest.Name < values[j].manifest.Name })
	agent, _ := g.agentSubject(ctx, c)
	for _, k := range values {
		entry := appmodel.ListedApplication{Application: k.manifest.Name, Title: k.manifest.Title, Instances: []appmodel.ListedInstance{}}
		var verbs []appmodel.ListedOpenVerb
		if agent {
			verbs = g.openVerbs(ctx, c, k, true, true)
		}
		// The quiet open-only projection never raises a command.read question.
		out, why := g.permission(ctx, c, k, appmodel.CommandClassRead, query.Application != "" && (!agent || len(verbs) == 0), "Describe")
		if out != "" {
			if out == "unavailable" || query.Application != "" && (!agent || len(verbs) == 0) {
				answer.Outcome, _ = appmodel.ParseReadingOutcome(out)
				answer.Reason = why
				answer.Applications = nil
				answer.OpenVerbs = nil
				return applications.DescribeResult{Outcome: applications.OutcomePage, Answer: answer}
			}
			if agent && len(verbs) > 0 {
				answer.Applications = append(answer.Applications, entry)
				answer.OpenVerbs = append(answer.OpenVerbs, appmodel.ApplicationOpenVerbs{Application: k.manifest.Name, Verbs: verbs})
			}
			continue
		}
		verbs = g.openVerbs(ctx, c, k, agent, false)
		instances := make([]string, 0, len(k.live))
		for instance := range k.live {
			instances = append(instances, instance)
		}
		sort.Strings(instances)
		for _, instance := range instances {
			listed := appmodel.ListedInstance{Instance: instance, Contexts: []appmodel.ContextEntry{}}
			s := g.session(k.manifest.Name, instance)
			if s == nil || s.bindingValue().manifestRevision != k.revision {
				listed.Reason = "owned_host"
			} else {
				d, _, e := g.description(ctx, c, k, s, func(ctx context.Context) error { return g.quiet(ctx, c, k, "abstraction.facade/command.read") })
				if e != nil {
					_, listed.Reason = applicationV3DescriptionFailure(e)
				} else {
					listed.Interface = "abstraction.facade/" + d.Version
					call := &appmodel.GatewayCall{Application: k.manifest.Name, Instance: instance, Tool: "app_contexts", Arguments: appmodel.Raw("{}")}
					var env *appmodel.Envelope
					_, e = s.call(ctx, call, func(raw []byte) error { var err error; env, err = applicationV3Answer("app_contexts", raw); return err }, func(ctx context.Context, _ ownedApplicationBinding, _ *appmodel.GatewayCall) error {
						return g.quiet(ctx, c, k, "abstraction.facade/command.read")
					})
					if e != nil || env.Contexts.Outcome != appmodel.ReadingOutcomePage {
						listed.Interface = ""
						listed.Reason = "contexts"
					} else {
						for _, context := range env.Contexts.Contexts {
							if context.Context.Instance != instance {
								listed.Interface = ""
								listed.Reason = "instance"
								listed.Contexts = nil
								break
							}
							listed.Contexts = append(listed.Contexts, context)
						}
					}
				}
			}
			entry.Instances = append(entry.Instances, listed)
		}
		if g.quiet(ctx, c, k, "abstraction.facade/command.read") != nil {
			return applications.DescribeResult{Outcome: applications.OutcomeForbidden, Reason: "rights"}
		}
		answer.Applications = append(answer.Applications, entry)
		if len(verbs) > 0 {
			answer.OpenVerbs = append(answer.OpenVerbs, appmodel.ApplicationOpenVerbs{Application: k.manifest.Name, Verbs: verbs})
		}
	}
	result, err := fitApplicationV3Reply("Describe", applications.DescribeResult{Outcome: applications.OutcomePage, Answer: answer})
	if err != nil {
		return applications.DescribeResult{Outcome: applications.OutcomeUnavailable, Reason: err.Error()}
	}
	visible := make(map[string]bool, len(result.Answer.Applications))
	for _, entry := range result.Answer.Applications {
		visible[entry.Application] = true
	}
	result.Answer.OpenVerbs = slices.DeleteFunc(result.Answer.OpenVerbs, func(entry appmodel.ApplicationOpenVerbs) bool {
		return !visible[entry.Application]
	})
	if len(result.Answer.OpenVerbs) == 0 {
		result.Answer.OpenVerbs = nil
	}
	return result
}
