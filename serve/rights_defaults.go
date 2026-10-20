package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/openabstractions/abstraction-facade/go/client"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

const rightsDefaultsUsage = `Usage: openabstractions rights defaults [--on NAME | --off NAME] [--revision REV] [options]

Lists the defaults for all apps, or turns one on or off. Each default lets
every program of this account that has no rule of its own on the target do
one thing; a rule made for one program, a Block included, wins. A script host
never receives one. Turning a default writes no rule.

  --on NAME      turn the named default on
  --off NAME     turn the named default off
  --revision REV apply only at this policy revision (default: read it now)

Example:
  openabstractions rights defaults --off download

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict, invalid), 4 unavailable.
`

// defaultJSON is one default as rights defaults prints it.
type defaultJSON struct {
	Name          string        `json:"name"`
	Action        string        `json:"action"`
	Resource      string        `json:"resource,omitempty"`
	EveryResource bool          `json:"every_resource,omitempty"`
	On            bool          `json:"on"`
	SetBy         *probeSubject `json:"set_by,omitempty"`
	SetAt         string        `json:"set_at,omitempty"`
}

func defaultOf(d rightswire.AccountDefault) defaultJSON {
	out := defaultJSON{Name: d.Name, Action: d.Action, Resource: d.Resource, EveryResource: d.EveryResource, On: d.On, SetAt: d.SetAt}
	if d.SetBy != nil {
		out.SetBy = &probeSubject{Account: d.SetBy.Account, Program: d.SetBy.Program}
	}
	return out
}

// rightsDefaultsReply is the JSON document rights defaults prints.
type rightsDefaultsReply struct {
	Command  string        `json:"command"`
	Outcome  string        `json:"outcome"`
	Revision string        `json:"revision,omitempty"`
	Defaults []defaultJSON `json:"defaults,omitempty"`
	Current  *defaultJSON  `json:"current,omitempty"`
}

// rightsDefaults lists the defaults through the operator profile, or turns one
// (name non-empty) at revision, read now when empty (rights RIGHTS-O13).
func rightsDefaults(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command, name string, on bool, revision string, asJSON bool, output io.Writer) error {
	operator, err := resolveOperator(machine, source, call, command)
	if err != nil {
		return err
	}
	if name == "" || revision == "" {
		ctx, cancel := call()
		page, err := operator.ListDefaultsContext(ctx)
		cancel()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		if page.Outcome != rightswire.PolicyPageOutcomePage {
			return printDefaults(output, asJSON, rightsDefaultsReply{Command: command, Outcome: page.Outcome.String()}, rightsOperatorRefusal(command, page.Outcome.String(), ""))
		}
		if name == "" {
			reply := rightsDefaultsReply{Command: command, Outcome: page.Outcome.String(), Revision: page.Revision}
			for _, d := range page.Defaults {
				reply.Defaults = append(reply.Defaults, defaultOf(d))
			}
			return printDefaults(output, asJSON, reply, nil)
		}
		revision = page.Revision
	}
	ctx, cancel := call()
	edit, err := operator.SetDefaultContext(ctx, revision, name, on)
	cancel()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: outcome uncertain; list the defaults before another edit: %w", command, err)}
	}
	reply := rightsDefaultsReply{Command: command, Outcome: edit.Outcome.String(), Revision: edit.Revision}
	if edit.Current != nil {
		current := defaultOf(*edit.Current)
		reply.Current = &current
	}
	var failure error
	if edit.Outcome != rightswire.PolicyEditOutcomeApplied {
		failure = rightsOperatorRefusal(command, edit.Outcome.String(), "at revision "+revision)
	}
	return printDefaults(output, asJSON, reply, failure)
}

// revisionGiven reports whether the command line named --revision.
func revisionGiven(flags *flag.FlagSet) bool {
	given := false
	flags.Visit(func(f *flag.Flag) { given = given || f.Name == "revision" })
	return given
}

func onWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func printDefaults(output io.Writer, asJSON bool, reply rightsDefaultsReply, failure error) error {
	if asJSON {
		if err := writeJSON(output, reply); err != nil {
			return err
		}
		return failure
	}
	if reply.Defaults != nil {
		table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "DEFAULT\tSTATE\tACTION\tRESOURCE")
		for _, d := range reply.Defaults {
			resource := d.Resource
			if d.EveryResource {
				resource = "every resource"
			}
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", d.Name, onWord(d.On), d.Action, resource)
		}
		if err := table.Flush(); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "policy revision %s\n", reply.Revision)
		return err
	}
	text := fmt.Sprintf("%s: %s", reply.Command, reply.Outcome)
	if reply.Current != nil {
		text += fmt.Sprintf("; %s is %s; policy revision now %s", reply.Current.Name, onWord(reply.Current.On), reply.Revision)
	}
	if _, err := fmt.Fprintln(output, text); err != nil {
		return err
	}
	return failure
}
