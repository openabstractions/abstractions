package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
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
	// Request and Expires name the change request that stopped the bundle.
	Request string `json:"request,omitempty"`
	Expires string `json:"expires,omitempty"`
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

// rightsBundle requests each rule of a bundle as a permit for the named
// program, one change request at a time within one --wait (RIGHTS-O17): the
// person confirms each in its own popup. The first rule that does not apply
// stops the bundle, a pending one included; the command prints the rules
// that landed and the one that stopped it, and never retries. --revision
// applies the first rule only at that revision; later rules apply at the
// current one.
func rightsBundle(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command, bundle string, rule rightsRule, rules []grants.Rule, revision, why string, ttl, wait time.Duration, asJSON bool, output io.Writer) error {
	command += " --for " + bundle
	operator, err := resolveOperator(machine, source, call, command)
	if err != nil {
		return err
	}
	reply := bundleReply{Command: command, Bundle: bundle, Subject: subjectOf(rule.subject()), Why: why, Rules: rules, Landed: []grants.Rule{}, Outcome: "applied"}
	deadline := time.Now().Add(wait)
	var failure error
	for _, r := range rules {
		state, err := requestRuleChange(operator, command, rights.PolicyRule{Subject: rule.subject(), Action: r.Action, Resource: r.Resource, Permit: true}, revision, false, why, ttl, max(time.Until(deadline), 0), timeoutOf(call))
		revision = ""
		if err != nil {
			stopped := r
			reply.Outcome, reply.Stopped = "uncertain", &stopped
			failure = err
			break
		}
		if state.Revision != "" {
			reply.Revision = state.Revision
		}
		if state.Outcome != rightswire.ChangeRequestOutcomeApplied {
			stopped := r
			reply.Outcome, reply.Stopped, reply.Request, reply.Expires = state.Outcome.String(), &stopped, state.ID, state.Expires
			failure = changeFailure(command, state)
			if state.Outcome != rightswire.ChangeRequestOutcomePending {
				failure = refusal(command, state.Outcome.String(), fmt.Sprintf("%s on %s did not apply; %d of %d rules landed", r.Action, r.Resource, len(reply.Landed), len(rules)))
			}
			break
		}
		reply.Landed = append(reply.Landed, r)
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
	// Every write into b below targets a strings.Builder, which never errors;
	// the only write that can fail is the io.WriteString to output at the end.
	var b strings.Builder
	//unchecked: strings.Builder never errors
	fmt.Fprintf(&b, "%s: %s for %s running %s", reply.Command, reply.Outcome, reply.Subject.Account, reply.Subject.Program)
	if reply.Revision != "" {
		//unchecked: strings.Builder never errors
		fmt.Fprintf(&b, "; policy revision now %s", reply.Revision)
	}
	//unchecked: strings.Builder never errors
	b.WriteString("\n")
	for _, r := range reply.Landed {
		//unchecked: strings.Builder never errors
		fmt.Fprintf(&b, "  permit %s on %s (why %q)\n", r.Action, r.Resource, reply.Why)
	}
	if reply.Stopped != nil {
		//unchecked: strings.Builder never errors
		fmt.Fprintf(&b, "  stopped at %s on %s: %s", reply.Stopped.Action, reply.Stopped.Resource, reply.Outcome)
		if reply.Current != nil {
			//unchecked: strings.Builder never errors
			fmt.Fprintf(&b, "; current rule: %s", ruleWord(reply.Current.Permit))
		}
		//unchecked: strings.Builder never errors
		b.WriteString("\n")
	}
	if _, err := io.WriteString(output, b.String()); err != nil {
		return err
	}
	return failure
}
