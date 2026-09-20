package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

const rightsUsage = `Usage: openabstractions rights <command> [options]

Commands:
  list      the rules and the action catalogue at the current policy revision
  grant     set one exact permit rule; --deny sets an exact deny rule
  revoke    remove one exact rule
  read      one exact rule with who set it, when, why and its expiry
  decide    a decision, changing nothing
  register-action  add --action to the catalogue; grants no permission
  retire-action    remove --action and its rules from the catalogue

A rule names a proven program and exactly one action on one resource:
  --program PATH       the subject's absolute executable path
  --account ID         the subject's account: a Windows SID or a POSIX uid
                       (default: this account)
  --action ACTION      a catalogue action, <owner>/<name>
  --resource RESOURCE  the exact resource the action names

grant, revoke, register-action and retire-action:
  --revision REV       apply only at this policy revision; without it, the
                       revision list reads now, and a change in between is a
                       conflict, never a retry
grant:
  --deny               an exact deny rule
  --why TEXT           the reason recorded with the rule (0..256 bytes)
  --ttl DURATION       expire the rule after DURATION (default: no expiry)
  --for BUNDLE         instead of --action and --resource, write each exact
                       permit rule of a bundle for --program, one conditional
                       edit at a time; a rule that does not apply stops the
                       bundle, and the rules that landed are listed
                       downloads: abstraction.job/acceptance.submit, model
                         lookup on each --registry, apply on each --credential
                       inference: abstraction.router/route, complete on
                         host:<name> for each --host, apply on each --credential
  --registry LIST      comma-separated registries for downloads (default hf,ollama)
  --host LIST          comma-separated inference hosts (required for inference)
  --credential LIST    comma-separated registered credential names
                       (default: none); --why defaults to "allow <bundle>"
list:
  --cursor C           continue a listing
  --limit N            rules per page, 1..64 (default 64)
decide:
  without --program    Authorization.Decide for this program
  with --program       Authorization.DecideFor, which answers only a designated
                       enforcer; the exact rule on record is read beside it

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Only the runtime's operator programs may list, edit or read rules.

Exit codes: 0 done (a read or decide that answered, whatever it found), 1
runtime not resolved or transport failure, 2 usage, 3 typed refusal (forbidden,
conflict, invalid, gap), 4 unavailable.
`

// rightsRule is one exact rule named on the command line. The checks match the
// Panel's rights edit (monitor/rights_panel.go): bounded single-line text and
// an absolute clean program path.
type rightsRule struct {
	account, program, action, resource string
}

func rightsText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (r rightsRule) check(command string, needProgram bool) error {
	if needProgram || r.program != "" {
		if !rightsText(r.program, 4096) || !identity.ValidSubjectProgram(r.program) {
			return &exitError{exitUsage, fmt.Errorf("%s: --program must be a clean absolute executable path", command)}
		}
	}
	if !rightsText(r.account, 128) {
		return &exitError{exitUsage, fmt.Errorf("%s: --account must be 1..128 bytes on one line", command)}
	}
	if !rightsText(r.action, 128) || !strings.Contains(r.action, "/") {
		return &exitError{exitUsage, fmt.Errorf("%s: --action must be a catalogue action <owner>/<name>", command)}
	}
	if !rightsText(r.resource, 1024) {
		return &exitError{exitUsage, fmt.Errorf("%s: --resource must be 1..1024 bytes on one line", command)}
	}
	return nil
}

func (r rightsRule) subject() rights.Subject {
	return rights.Subject{Account: r.account, Program: r.program}
}

// ruleJSON is one exact rule as the rights command prints it.
type ruleJSON struct {
	Subject  probeSubject `json:"subject"`
	Action   string       `json:"action"`
	Resource string       `json:"resource"`
	Permit   bool         `json:"permit"`
}

func ruleOf(r rights.PolicyRule) ruleJSON {
	return ruleJSON{Subject: probeSubject{Account: r.Subject.Account, Program: r.Subject.Program}, Action: r.Action, Resource: r.Resource, Permit: r.Permit}
}

// recordJSON is a rule with who set it, when, why and its expiry.
type recordJSON struct {
	Rule    ruleJSON     `json:"rule"`
	SetBy   probeSubject `json:"set_by"`
	SetAt   string       `json:"set_at"`
	Why     string       `json:"why"`
	Expires string       `json:"expires"`
}

// rightsReply is the JSON document a rights command prints.
type rightsReply struct {
	Command    string        `json:"command"`
	Outcome    string        `json:"outcome"`
	Revision   string        `json:"revision,omitempty"`
	Subject    *probeSubject `json:"subject,omitempty"`
	Action     string        `json:"action,omitempty"`
	Resource   string        `json:"resource,omitempty"`
	Permit     *bool         `json:"permit,omitempty"`
	Current    *ruleJSON     `json:"current,omitempty"`
	Record     *recordJSON   `json:"record,omitempty"`
	Rules      []ruleJSON    `json:"rules,omitempty"`
	Catalog    []string      `json:"catalog,omitempty"`
	Next       string        `json:"next,omitempty"`
	Complete   bool          `json:"complete,omitempty"`
	RuleOnFile *rightsReply  `json:"rule_on_record,omitempty"`
}

func subjectOf(s rights.Subject) *probeSubject {
	return &probeSubject{Account: s.Account, Program: s.Program}
}

func rightsCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || isHelp(args[0]) {
		_, err := io.WriteString(output, rightsUsage)
		return err
	}
	switch args[0] {
	case "list", "grant", "revoke", "read", "decide", "register-action", "retire-action":
	default:
		return &exitError{exitUsage, fmt.Errorf("rights: no command called %q; run openabstractions rights --help", args[0])}
	}
	command := "rights " + args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		if _, err := fmt.Fprint(diagnostics, rightsUsage); err == nil {
			flags.PrintDefaults()
		}
	}
	var options probeOptions
	options.bind(flags)
	var rule rightsRule
	var revision, why, cursor string
	var deny bool
	var ttl time.Duration
	var limit int64
	flags.StringVar(&rule.program, "program", "", "subject executable path")
	flags.StringVar(&rule.account, "account", "", "subject account (default: this account)")
	flags.StringVar(&rule.action, "action", "", "catalogue action")
	flags.StringVar(&rule.resource, "resource", "", "exact resource")
	flags.StringVar(&revision, "revision", "", "policy revision to apply at")
	flags.StringVar(&why, "why", "", "reason recorded with a grant")
	flags.BoolVar(&deny, "deny", false, "grant an exact deny rule")
	flags.DurationVar(&ttl, "ttl", 0, "expire a grant after this duration")
	flags.StringVar(&cursor, "cursor", "", "listing continuation")
	flags.Int64Var(&limit, "limit", 64, "rules per page")
	var bundle, registries, hosts, credentialNames string
	flags.StringVar(&bundle, "for", "", "grant a bundle: downloads or inference")
	flags.StringVar(&registries, "registry", "hf,ollama", "registries a downloads bundle may look up")
	flags.StringVar(&hosts, "host", "", "inference hosts an inference bundle may complete on")
	flags.StringVar(&credentialNames, "credential", "", "credential names a bundle may have applied")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &exitError{exitUsage, err}
	}
	if flags.NArg() != 0 {
		return &exitError{exitUsage, fmt.Errorf("%s: unexpected arguments %q", command, flags.Args())}
	}
	if options.timeout <= 0 {
		return &exitError{exitUsage, fmt.Errorf("%s: --timeout must be positive", command)}
	}
	bundleFlags := false
	flags.Visit(func(f *flag.Flag) {
		bundleFlags = bundleFlags || f.Name == "for" || f.Name == "registry" || f.Name == "host" || f.Name == "credential"
	})
	var bundleRules []grants.Rule
	if bundleFlags {
		if args[0] != "grant" || bundle == "" || rule.action != "" || rule.resource != "" || deny {
			return &exitError{exitUsage, fmt.Errorf("%s: --registry, --host and --credential belong to grant --for, which names no --action, --resource or --deny", command)}
		}
		selection := grants.For{Credentials: splitList(credentialNames), Hosts: splitList(hosts)}
		if bundle == grants.Downloads {
			selection.Registries = splitList(registries)
		}
		var err error
		if bundleRules, err = grants.Rules(bundle, selection); err != nil {
			return &exitError{exitUsage, fmt.Errorf("%s: %w", command, err)}
		}
		if why == "" {
			why = grants.DefaultWhy(bundle)
		}
	}
	if rule.account == "" {
		_, account, err := principal()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: account unavailable: %w", command, err)}
		}
		rule.account = account
	}
	machine, err := options.machine()
	if err != nil {
		return err
	}
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), options.timeout)
	}
	switch args[0] {
	case "register-action", "retire-action":
		invalid := false
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "action", "revision", "endpoint", "runtime-program", "timeout", "json":
			default:
				invalid = true
			}
		})
		if invalid || !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}/[a-z0-9][a-z0-9._-]{0,62}$`).MatchString(rule.action) {
			return &exitError{exitUsage, fmt.Errorf("%s: use --action <owner>/<name> and optional --revision; rule fields do not apply", command)}
		}
		return rightsActionEdit(machine, call, command, rule.action, revision, args[0] == "retire-action", options.asJSON, output)
	case "list":
		if limit < 1 || limit > 64 || len(cursor) > 256 {
			return &exitError{exitUsage, fmt.Errorf("%s: --limit is 1..64 and --cursor at most 256 bytes", command)}
		}
		return rightsList(machine, call, command, cursor, limit, options.asJSON, output)
	case "decide":
		if err := rule.check(command, false); err != nil {
			return err
		}
		return rightsDecide(machine, call, command, rule, options.asJSON, output)
	}
	if bundleRules != nil {
		if !rightsText(rule.program, 4096) || !identity.ValidSubjectProgram(rule.program) {
			return &exitError{exitUsage, fmt.Errorf("%s: --program must be a clean absolute executable path", command)}
		}
		if ttl < 0 || len(why) > 256 || strings.IndexFunc(why, unicode.IsControl) >= 0 {
			return &exitError{exitUsage, fmt.Errorf("%s: --why is 0..256 bytes on one line and --ttl is not negative", command)}
		}
		return rightsBundle(machine, call, command, bundle, rule, bundleRules, revision, why, ttl, options.asJSON, output)
	}
	if err := rule.check(command, true); err != nil {
		return err
	}
	if args[0] == "read" {
		return rightsRead(machine, call, command, rule, options.asJSON, output)
	}
	if ttl < 0 || (args[0] == "revoke" && (deny || why != "" || ttl != 0)) || len(why) > 256 || strings.IndexFunc(why, unicode.IsControl) >= 0 {
		return &exitError{exitUsage, fmt.Errorf("%s: --deny, --why and --ttl belong to grant; --why is 0..256 bytes on one line", command)}
	}
	return rightsEdit(machine, call, command, rule, revision, !deny, why, ttl, args[0] == "revoke", options.asJSON, output)
}

func rightsActionEdit(machine *client.Machine, call func() (context.Context, context.CancelFunc), command, action, revision string, retire, asJSON bool, output io.Writer) error {
	operator, err := resolveOperator(machine, call, command)
	if err != nil {
		return err
	}
	if revision == "" {
		ctx, cancel := call()
		page, err := operator.ListPolicyContext(ctx, "", 1)
		cancel()
		if err != nil {
			return notResolved(command, err)
		}
		if page.Outcome != rightswire.PolicyPageOutcomePage {
			return rightsPrint(output, asJSON, rightsReply{Command: command, Outcome: page.Outcome.String()}, refusal(command, page.Outcome.String(), "listing the policy revision"))
		}
		revision = page.Revision
	}
	ctx, cancel := call()
	var edit rightswire.ActionEdit
	if retire {
		edit, err = operator.RetireActionContext(ctx, revision, action)
	} else {
		edit, err = operator.RegisterActionContext(ctx, revision, action)
	}
	cancel()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: outcome uncertain; list the catalogue before another edit: %w", command, err)}
	}
	reply := rightsReply{Command: command, Outcome: edit.Outcome.String(), Revision: edit.Revision, Action: action}
	var failure error
	if edit.Outcome != rightswire.ActionEditOutcomeApplied {
		failure = refusal(command, edit.Outcome.String(), "at revision "+revision)
	}
	return rightsPrint(output, asJSON, reply, failure)
}

func resolveOperator(machine *client.Machine, call func() (context.Context, context.CancelFunc), command string) (*rights.Operator, error) {
	ctx, cancel := call()
	defer cancel()
	operator, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		return nil, notResolved(command, err)
	}
	return operator, nil
}

func rightsList(machine *client.Machine, call func() (context.Context, context.CancelFunc), command, cursor string, limit int64, asJSON bool, output io.Writer) error {
	operator, err := resolveOperator(machine, call, command)
	if err != nil {
		return err
	}
	ctx, cancel := call()
	page, err := operator.ListPolicyContext(ctx, cursor, limit)
	cancel()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	reply := rightsReply{Command: command, Outcome: page.Outcome.String(), Revision: page.Revision, Catalog: page.Catalog, Next: page.Next, Complete: page.Complete}
	for _, r := range page.Rules {
		reply.Rules = append(reply.Rules, ruleOf(r))
	}
	if asJSON {
		if err := writeJSON(output, reply); err != nil {
			return err
		}
	} else if page.Outcome == rightswire.PolicyPageOutcomePage {
		table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
		fmt.Fprintf(table, "policy revision %s; %d catalogue actions\n", page.Revision, len(page.Catalog))
		fmt.Fprintln(table, "RULE\tACCOUNT\tPROGRAM\tACTION\tRESOURCE")
		for _, r := range page.Rules {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", ruleWord(r.Permit), r.Subject.Account, r.Subject.Program, r.Action, r.Resource)
		}
		if !page.Complete {
			fmt.Fprintf(table, "more rules: --cursor %s\n", page.Next)
		}
		if err := table.Flush(); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(output, "%s: %s\n", command, page.Outcome); err != nil {
		return err
	}
	if page.Outcome != rightswire.PolicyPageOutcomePage {
		return refusal(command, page.Outcome.String(), "")
	}
	return nil
}

func ruleWord(permit bool) string {
	if permit {
		return "permit"
	}
	return "deny"
}

func rightsEdit(machine *client.Machine, call func() (context.Context, context.CancelFunc), command string, rule rightsRule, revision string, permit bool, why string, ttl time.Duration, revoke, asJSON bool, output io.Writer) error {
	operator, err := resolveOperator(machine, call, command)
	if err != nil {
		return err
	}
	if revision == "" {
		ctx, cancel := call()
		page, err := operator.ListPolicyContext(ctx, "", 1)
		cancel()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		if page.Outcome != rightswire.PolicyPageOutcomePage {
			return rightsPrint(output, asJSON, rightsReply{Command: command, Outcome: page.Outcome.String()}, refusal(command, page.Outcome.String(), "listing the policy revision"))
		}
		revision = page.Revision
	}
	subject := rule.subject()
	ctx, cancel := call()
	var edit rights.PolicyEdit
	switch {
	case revoke:
		edit, err = operator.RevokeRuleContext(ctx, revision, subject, rule.action, rule.resource)
	case why != "" || ttl != 0:
		edit, err = operator.SetRuleForContext(ctx, revision, rights.PolicyRule{Subject: subject, Action: rule.action, Resource: rule.resource, Permit: permit}, ttl, why)
	default:
		edit, err = operator.SetRuleContext(ctx, revision, rights.PolicyRule{Subject: subject, Action: rule.action, Resource: rule.resource, Permit: permit})
	}
	cancel()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: outcome uncertain; list the policy before another edit: %w", command, err)}
	}
	reply := rightsReply{Command: command, Outcome: edit.Outcome.String(), Revision: edit.Revision, Subject: subjectOf(subject), Action: rule.action, Resource: rule.resource}
	if edit.Current != nil {
		current := ruleOf(*edit.Current)
		reply.Current = &current
	}
	if !revoke {
		reply.Permit = &permit
	}
	var failure error
	if edit.Outcome != rightswire.PolicyEditOutcomeApplied {
		failure = refusal(command, edit.Outcome.String(), "at revision "+revision)
	}
	if !asJSON {
		text := fmt.Sprintf("%s: %s at revision %s; policy revision now %s", command, edit.Outcome, revision, edit.Revision)
		if edit.Current != nil {
			text += fmt.Sprintf("; current rule: %s %s on %s for %s running %s", ruleWord(edit.Current.Permit), edit.Current.Action, edit.Current.Resource, edit.Current.Subject.Account, edit.Current.Subject.Program)
		} else if edit.Outcome == rightswire.PolicyEditOutcomeApplied || edit.Outcome == rightswire.PolicyEditOutcomeConflict {
			text += "; no exact rule"
		}
		_, err := fmt.Fprintln(output, text)
		if err != nil {
			return err
		}
		return failure
	}
	return rightsPrint(output, true, reply, failure)
}

func rightsPrint(output io.Writer, asJSON bool, reply rightsReply, failure error) error {
	if asJSON {
		if err := writeJSON(output, reply); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(output, "%s: %s\n", reply.Command, reply.Outcome); err != nil {
		return err
	}
	return failure
}

// readRule reads the exact rule through the operator, or says why it could not.
func readRule(machine *client.Machine, call func() (context.Context, context.CancelFunc), command string, rule rightsRule) (rightsReply, error) {
	subject := rule.subject()
	reply := rightsReply{Command: command, Subject: subjectOf(subject), Action: rule.action, Resource: rule.resource}
	ctx, cancel := call()
	defer cancel()
	operator, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		var resolution *client.ResolutionError
		if errors.As(err, &resolution) {
			reply.Outcome = string(resolution.Status)
		}
		return reply, notResolved(command, err)
	}
	read, err := operator.ReadRuleContext(ctx, subject, rule.action, rule.resource)
	if err != nil {
		return reply, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	reply.Outcome, reply.Revision = read.Outcome.String(), read.Revision
	if read.Record != nil {
		reply.Record = &recordJSON{Rule: ruleOf(read.Record.Rule), SetBy: *subjectOf(read.Record.SetBy), SetAt: read.Record.SetAt, Why: read.Record.Why, Expires: read.Record.Expires}
		permit := read.Record.Rule.Permit
		reply.Permit = &permit
	}
	return reply, nil
}

func rightsRead(machine *client.Machine, call func() (context.Context, context.CancelFunc), command string, rule rightsRule, asJSON bool, output io.Writer) error {
	reply, err := readRule(machine, call, command, rule)
	if err != nil {
		return err
	}
	var failure error
	switch reply.Outcome {
	case "found", "expired", "unknown":
	default:
		failure = refusal(command, reply.Outcome, "")
	}
	if asJSON {
		return rightsPrint(output, true, reply, failure)
	}
	text := fmt.Sprintf("%s: %s at policy revision %s", command, reply.Outcome, reply.Revision)
	if reply.Permit != nil {
		text += fmt.Sprintf(": %s %s on %s for %s running %s", ruleWord(*reply.Permit), rule.action, rule.resource, rule.account, rule.program)
	}
	if _, err := fmt.Fprintln(output, text); err != nil {
		return err
	}
	if reply.Record != nil {
		if text, err := jsonText(reply.Record); err == nil {
			fmt.Fprintln(output, text)
		}
	}
	return failure
}

func rightsDecide(machine *client.Machine, call func() (context.Context, context.CancelFunc), command string, rule rightsRule, asJSON bool, output io.Writer) error {
	ctx, cancel := call()
	decisions, err := machine.ResolveRights(ctx, local)
	cancel()
	if err != nil {
		return notResolved(command, err)
	}
	ctx, cancel = call()
	var decision rights.Decision
	subject := rule.subject()
	if rule.program == "" {
		self := probeSelf()
		subject = rights.Subject{Account: self.Account, Program: self.Program}
		decision, err = decisions.DecideContext(ctx, rule.action, rule.resource)
	} else {
		decision, err = decisions.DecideForContext(ctx, subject, rule.action, rule.resource)
	}
	cancel()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	reply := rightsReply{Command: command, Outcome: decision.Outcome.String(), Revision: decision.PolicyRevision, Subject: subjectOf(subject), Action: rule.action, Resource: rule.resource}
	if rule.program != "" {
		onRecord, _ := readRule(machine, call, "rights read", rule)
		reply.RuleOnFile = &onRecord
	}
	var failure error
	switch decision.Outcome {
	case rightswire.DecisionOutcomeForbidden, rightswire.DecisionOutcomeInvalid, rightswire.DecisionOutcomeUnavailable:
		failure = refusal(command, decision.Outcome.String(), "")
	}
	if asJSON {
		return rightsPrint(output, true, reply, failure)
	}
	text := fmt.Sprintf("%s: %s for %s running %s: %s on %s", command, decision.Outcome, subject.Account, subject.Program, rule.action, rule.resource)
	if decision.PolicyRevision != "" {
		text += " at policy revision " + decision.PolicyRevision
	}
	if decision.Outcome == rightswire.DecisionOutcomeForbidden && rule.program != "" {
		text += " (DecideFor answers only a designated enforcer)"
	}
	if reply.RuleOnFile != nil {
		text += "; rule on record: " + reply.RuleOnFile.Outcome
		if reply.RuleOnFile.Permit != nil {
			text += " " + ruleWord(*reply.RuleOnFile.Permit)
		}
	}
	if _, err := fmt.Fprintln(output, text); err != nil {
		return err
	}
	return failure
}
