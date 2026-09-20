package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// bundleReply is the JSON document `rights grant --for` prints.
type bundleReply struct {
	Command  string        `json:"command"`
	Bundle   string        `json:"for"`
	Outcome  string        `json:"outcome"`
	Revision string        `json:"revision,omitempty"`
	Subject  *probeSubject `json:"subject"`
	Why      string        `json:"why"`
	Rules    []grants.Rule `json:"rules"`
	Landed   []grants.Rule `json:"landed"`
	Stopped  *grants.Rule  `json:"stopped,omitempty"`
	Current  *ruleJSON     `json:"current,omitempty"`
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// rightsBundle writes each rule of a bundle as a permit for the named program,
// one conditional edit at a time, starting at --revision or at the revision
// list reads now. The first rule that does not apply stops the bundle; the
// command prints the rules that landed and the one that stopped it, and never
// retries.
func rightsBundle(machine *client.Machine, call func() (context.Context, context.CancelFunc), command, bundle string, rule rightsRule, rules []grants.Rule, revision, why string, ttl time.Duration, asJSON bool, output io.Writer) error {
	command += " --for " + bundle
	operator, err := resolveOperator(machine, call, command)
	if err != nil {
		return err
	}
	reply := bundleReply{Command: command, Bundle: bundle, Subject: subjectOf(rule.subject()), Why: why, Rules: rules, Landed: []grants.Rule{}}
	if revision == "" {
		ctx, cancel := call()
		page, err := operator.ListPolicyContext(ctx, "", 1)
		cancel()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		if page.Outcome != rights.PolicyPageOutcomePage {
			reply.Outcome = page.Outcome.String()
			return bundlePrint(output, asJSON, reply, refusal(command, page.Outcome.String(), "listing the policy revision"))
		}
		revision = page.Revision
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(rules))*timeoutOf(call))
	result, writeErr := grants.Write(ctx, operator, revision, rule.subject(), rules, ttl, why)
	cancel()
	reply.Outcome, reply.Revision, reply.Landed, reply.Stopped = result.Outcome, result.Revision, result.Landed, result.Stopped
	if result.Current != nil {
		current := ruleOf(*result.Current)
		reply.Current = &current
	}
	var failure error
	switch {
	case writeErr != nil:
		failure = &exitError{exitNotResolved, fmt.Errorf("%s: %s on %s is uncertain; list the policy before another edit: %w", command, result.Stopped.Action, result.Stopped.Resource, writeErr)}
	case result.Outcome != "applied":
		failure = refusal(command, result.Outcome, fmt.Sprintf("%s on %s did not apply; %d of %d rules landed", result.Stopped.Action, result.Stopped.Resource, len(result.Landed), len(rules)))
	}
	return bundlePrint(output, asJSON, reply, failure)
}

// timeoutOf is the per-call budget the call function gives.
func timeoutOf(call func() (context.Context, context.CancelFunc)) time.Duration {
	ctx, cancel := call()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		return 10 * time.Second
	}
	return time.Until(deadline)
}

func bundlePrint(output io.Writer, asJSON bool, reply bundleReply, failure error) error {
	if asJSON {
		if err := writeJSON(output, reply); err != nil {
			return err
		}
		return failure
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s for %s running %s", reply.Command, reply.Outcome, reply.Subject.Account, reply.Subject.Program)
	if reply.Revision != "" {
		fmt.Fprintf(&b, "; policy revision now %s", reply.Revision)
	}
	b.WriteString("\n")
	for _, r := range reply.Landed {
		fmt.Fprintf(&b, "  permit %s on %s (why %q)\n", r.Action, r.Resource, reply.Why)
	}
	if reply.Stopped != nil {
		fmt.Fprintf(&b, "  stopped at %s on %s: %s", reply.Stopped.Action, reply.Stopped.Resource, reply.Outcome)
		if reply.Current != nil {
			fmt.Fprintf(&b, "; current rule: %s", ruleWord(reply.Current.Permit))
		}
		b.WriteString("\n")
	}
	if _, err := io.WriteString(output, b.String()); err != nil {
		return err
	}
	return failure
}
