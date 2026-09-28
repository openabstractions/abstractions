package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

const rightsUsage = `Usage: openabstractions rights <command> [options]

Lists, grants, revokes and decides exact permit or deny rules; only the
runtime's own operator programs may list, edit or read them.

  list                    the rules and action catalogue at the current
                          policy revision (--cursor, --limit, --long)
  grant                   set one exact permit rule; --deny sets a deny rule
  grant --for BUNDLE      instead, write each rule of a named bundle
                          (downloads, inference) for --program, stopping at
                          the first that does not apply
  revoke                  remove one exact rule
  read                    one exact rule with who set it, when, why and its
                          expiry
  decide                  a decision, changing nothing; with --program,
                          answers only a designated enforcer
  register-action         add --action to the catalogue; grants no permission
  retire-action           remove --action and its rules from the catalogue

A rule names a proven program and exactly one action on one resource:
  --program PATH        the subject's absolute executable path (stored and
                        compared as its canonical long path)
  --account ID          the subject's account (default: this account)
  --action ACTION       a catalogue action, <owner>/<name>
  --resource RESOURCE   the exact resource the action names
  --revision REV        apply only at this policy revision (default: read
                        it now; a change in between is a conflict, not a
                        retry)
  --deny --why --ttl    grant: a deny rule, the recorded reason and expiry
  --registry --host --credential
                        grant --for: what the bundle covers
  --endpoint --runtime-program --timeout --json
                        every command's own runtime connection and output

The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if
set (connected unverified, with a notice), else the installed runtime,
verified; --runtime-program verifies an explicit --endpoint instead.

Exit codes: 0 done (a read or decide that answered, whatever it found), 1
runtime not resolved or transport failure, 2 usage, 3 typed refusal (forbidden,
conflict, invalid, gap), 4 unavailable.

Learn more: openabstractions-flat/abstraction-rights/CONTRACT.md
`

// rightsSubUsage is each rights subcommand's own --help: its usage line, its
// own flags and one example, in place of the whole rightsUsage catalogue.
func rightsSubUsage(sub string) string {
	switch sub {
	case "list":
		return `Usage: openabstractions rights list [--cursor C] [--limit N] [--long] [options]

Lists the rules and action catalogue at the current policy revision.

  --cursor C   continue a listing
  --limit N    rules per page, 1..64 (default 64)
  --long       print each rule's real account and full program path;
               without it, the caller's own account reads "this account"
               and each program reads its short file name

Example:
  openabstractions rights list --long

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden), 4 unavailable.
`
	case "grant":
		return `Usage: openabstractions rights grant --program PATH --action ACTION --resource RESOURCE [options]
       openabstractions rights grant --for BUNDLE --program PATH [options]

Sets one exact permit rule; --deny sets a deny rule. --for writes each rule
of a named bundle (downloads, inference) for --program instead, one
conditional edit at a time; the first rule that does not apply stops it.

  --program PATH        the subject's absolute executable path
  --account ID          the subject's account (default: this account)
  --action ACTION       a catalogue action, <owner>/<name>
  --resource RESOURCE   the exact resource the action names
  --deny                an exact deny rule
  --why TEXT             the reason recorded with the rule (0..256 bytes)
  --ttl DURATION         expire the rule after DURATION (default: no expiry)
  --revision REV         apply only at this policy revision
  --for BUNDLE           downloads or inference, instead of --action/--resource
  --registry LIST        grant --for downloads: registries (default hf,ollama)
  --host LIST            grant --for inference: hosts (required)
  --credential LIST      grant --for: registered credential names

Example:
  openabstractions rights grant --program C:\tools\app.exe --action abstraction.credentials/apply --resource credential:hf

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict, invalid), 4 unavailable.
`
	case "revoke":
		return `Usage: openabstractions rights revoke --program PATH --action ACTION --resource RESOURCE [options]

Removes one exact rule.

  --program PATH        the subject's absolute executable path
  --account ID          the subject's account (default: this account)
  --action ACTION       a catalogue action, <owner>/<name>
  --resource RESOURCE   the exact resource the action names
  --revision REV        apply only at this policy revision

Example:
  openabstractions rights revoke --program C:\tools\app.exe --action abstraction.credentials/apply --resource credential:hf

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict), 4 unavailable.
`
	case "read":
		return `Usage: openabstractions rights read --program PATH --action ACTION --resource RESOURCE [options]

Prints one exact rule with who set it, when, why and its expiry.

  --program PATH        the subject's absolute executable path
  --account ID          the subject's account (default: this account)
  --action ACTION       a catalogue action, <owner>/<name>
  --resource RESOURCE   the exact resource the action names

Example:
  openabstractions rights read --program C:\tools\app.exe --action abstraction.credentials/apply --resource credential:hf

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Exit codes: 0 found or expired, 1 runtime not resolved or transport failure,
2 usage, 3 typed refusal (forbidden), 4 unavailable.
`
	case "decide":
		return `Usage: openabstractions rights decide --action ACTION --resource RESOURCE [options]
       openabstractions rights decide --program PATH --action ACTION --resource RESOURCE [options]

Decides whether a rule permits, changing nothing. Without --program, decides
for this program; with it, answers only a designated enforcer, and the exact
rule on record is read beside it.

  --program PATH        the subject's absolute executable path
  --account ID          the subject's account (default: this account)
  --action ACTION       a catalogue action, <owner>/<name>
  --resource RESOURCE   the exact resource the action names

Example:
  openabstractions rights decide --action abstraction.credentials/apply --resource credential:hf

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Exit codes: 0 permitted, 1 runtime not resolved or transport failure, 2 usage,
3 forbidden or invalid, 4 unavailable.
`
	case "register-action":
		return `Usage: openabstractions rights register-action --action ACTION [--revision REV] [options]

Adds --action to the catalogue; grants no permission.

  --action ACTION   a catalogue action, <owner>/<name>
  --revision REV    apply only at this policy revision

Example:
  openabstractions rights register-action --action abstraction.example/thing

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict), 4 unavailable.
`
	case "retire-action":
		return `Usage: openabstractions rights retire-action --action ACTION [--revision REV] [options]

Removes --action and its rules from the catalogue.

  --action ACTION   a catalogue action, <owner>/<name>
  --revision REV    apply only at this policy revision

Example:
  openabstractions rights retire-action --action abstraction.example/thing

Every command accepts --endpoint, --runtime-program, --timeout and --json.
Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict), 4 unavailable.
`
	}
	return rightsUsage
}

// rightsRule is one exact rule named on the command line. The checks match the
// Panel's rights edit (monitor/rights_panel.go): bounded single-line text and
// an absolute clean program path.
type rightsRule struct {
	account, program, action, resource string
}

func rightsText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (r rightsRule) check(diagnostics io.Writer, command, usage string, needProgram bool) error {
	if needProgram || r.program != "" {
		if !rightsText(r.program, 4096) || !identity.ValidSubjectProgram(r.program) {
			return flagMistake(diagnostics, command, usage, "--program must be a clean absolute executable path")
		}
	}
	if !rightsText(r.account, 128) {
		return flagMistake(diagnostics, command, usage, "--account must be 1..128 bytes on one line")
	}
	if !rightsText(r.action, 128) || !strings.Contains(r.action, "/") {
		return flagMistake(diagnostics, command, usage, "--action must be a catalogue action <owner>/<name>")
	}
	if !rightsText(r.resource, 1024) {
		return flagMistake(diagnostics, command, usage, "--resource must be 1..1024 bytes on one line")
	}
	return nil
}

func (r rightsRule) subject() rights.Subject {
	return rights.Subject{Account: r.account, Program: canonicalSubjectProgram(r.program)}
}

// canonicalSubjectProgram gives a rights rule the shared subject spelling.
func canonicalSubjectProgram(program string) string {
	if program == "" {
		return ""
	}
	return identity.NormalizeSubjectProgram(program)
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
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, rightsUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "rights: a command is required", "openabstractions rights --help")
	}
	switch args[0] {
	case "list", "grant", "revoke", "read", "decide", "register-action", "retire-action":
	default:
		return commandMistake(diagnostics, fmt.Sprintf("rights: no command called %q", args[0]), "openabstractions rights --help")
	}
	sub := args[0]
	subUsage := rightsSubUsage(sub)
	if containsHelp(args[1:]) {
		_, err := io.WriteString(output, subUsage)
		return err
	}
	command := "rights " + sub
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options probeOptions
	options.bind(flags)
	var rule rightsRule
	var revision, why, cursor string
	var deny, long bool
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
	flags.BoolVar(&long, "long", false, "list: print each rule's real account and full program path")
	var bundle, registries, hosts, credentialNames string
	flags.StringVar(&bundle, "for", "", "grant a bundle: downloads or inference")
	flags.StringVar(&registries, "registry", "hf,ollama", "registries a downloads bundle may look up")
	flags.StringVar(&hosts, "host", "", "inference hosts an inference bundle may complete on")
	flags.StringVar(&credentialNames, "credential", "", "credential names a bundle may have applied")
	if err := flags.Parse(args[1:]); err != nil {
		return badFlag(flags, diagnostics, command, subUsage, args[1:], err)
	}
	if flags.NArg() != 0 {
		return flagMistake(diagnostics, command, subUsage, fmt.Sprintf("unexpected arguments %q", flags.Args()))
	}
	if options.timeout <= 0 {
		return flagMistake(diagnostics, command, subUsage, "--timeout must be positive")
	}
	bundleFlags := false
	flags.Visit(func(f *flag.Flag) {
		bundleFlags = bundleFlags || f.Name == "for" || f.Name == "registry" || f.Name == "host" || f.Name == "credential"
	})
	var bundleRules []grants.Rule
	if bundleFlags {
		if args[0] != "grant" || bundle == "" || rule.action != "" || rule.resource != "" || deny {
			return flagMistake(diagnostics, command, subUsage, "--registry, --host and --credential belong to grant --for, which names no --action, --resource or --deny")
		}
		selection := grants.For{Credentials: splitList(credentialNames), Hosts: splitList(hosts)}
		if bundle == grants.Downloads {
			selection.Registries = splitList(registries)
		}
		var err error
		if bundleRules, err = grants.Rules(bundle, selection); err != nil {
			return flagMistake(diagnostics, command, subUsage, err.Error())
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
	machine, source, err := rightsMachine(options, diagnostics, command, subUsage)
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
			return flagMistake(diagnostics, command, subUsage, "use --action <owner>/<name> and optional --revision; rule fields do not apply")
		}
		return rightsActionEdit(machine, source, call, command, rule.action, revision, args[0] == "retire-action", options.asJSON, output)
	case "list":
		if limit < 1 || limit > 64 || len(cursor) > 256 {
			return flagMistake(diagnostics, command, subUsage, "--limit is 1..64 and --cursor at most 256 bytes")
		}
		return rightsList(machine, source, call, command, cursor, limit, long, rule.account, options.asJSON, output)
	case "decide":
		if err := rule.check(diagnostics, command, subUsage, false); err != nil {
			return err
		}
		return rightsDecide(machine, source, call, command, rule, options.asJSON, output)
	}
	if bundleRules != nil {
		if !rightsText(rule.program, 4096) || !identity.ValidSubjectProgram(rule.program) {
			return flagMistake(diagnostics, command, subUsage, "--program must be a clean absolute executable path")
		}
		if ttl < 0 || len(why) > 256 || strings.IndexFunc(why, unicode.IsControl) >= 0 {
			return flagMistake(diagnostics, command, subUsage, "--why is 0..256 bytes on one line and --ttl is not negative")
		}
		warnVirtualizedProgram(diagnostics, rule.program, os.Getenv("LOCALAPPDATA"), bootstrap.CurrentProfileView)
		return rightsBundle(machine, source, call, command, bundle, rule, bundleRules, revision, why, ttl, options.asJSON, output)
	}
	if err := rule.check(diagnostics, command, subUsage, true); err != nil {
		return err
	}
	if args[0] == "read" {
		return rightsRead(machine, source, call, command, rule, options.asJSON, output)
	}
	if ttl < 0 || (args[0] == "revoke" && (deny || why != "" || ttl != 0)) || len(why) > 256 || strings.IndexFunc(why, unicode.IsControl) >= 0 {
		return flagMistake(diagnostics, command, subUsage, "--deny, --why and --ttl belong to grant; --why is 0..256 bytes on one line")
	}
	if args[0] == "grant" {
		warnVirtualizedProgram(diagnostics, rule.program, os.Getenv("LOCALAPPDATA"), bootstrap.CurrentProfileView)
	}
	return rightsEdit(machine, source, call, command, rule, revision, !deny, why, ttl, args[0] == "revoke", options.asJSON, output)
}

// rightsMachine resolves this command's runtime connection and reports which
// of the three rules resolveEndpoint applies chose it. With --endpoint (and,
// to verify it, --runtime-program) the caller's own explicit server
// expectation applies, through options.machine(). With neither flag and
// runtimeEndpointVar unset, options.machine() selects and verifies the
// installed runtime, unchanged. With neither flag but the variable naming an
// endpoint, options.machine() would resolve through client.Discover(), which
// verifies the installed runtime's registration regardless of which endpoint
// the variable named (bootstrap.SelectInstalled reads the variable only for
// the endpoint name, never for the server it expects) — silently applying
// the wrong identity check to a different server. This instead connects to
// the named endpoint unverified and says so once. probe uses the same
// function, under the same name, for the same reason.
func rightsMachine(options probeOptions, diagnostics io.Writer, command, usage string) (*client.Machine, endpointSource, error) {
	if options.endpoint == "" && options.runtimeProgram == "" {
		if endpoint, source := resolveEndpoint(""); source == endpointFromVar {
			warnUnverifiedEndpoint(diagnostics, command, endpoint)
			return client.New(endpoint), source, nil
		}
	}
	source := endpointInstalled
	if options.endpoint != "" {
		source = endpointExplicit
	}
	machine, err := options.machine(diagnostics, command, usage)
	return machine, source, err
}

// warnVirtualizedProgram writes a one-line warning to diagnostics when program
// lies under localAppData (this process's own %LOCALAPPDATA%) and profile
// reports a virtualized view: a caller there sees a packaged app's private
// copy of AppData, and the nominal path an operator types and grants can
// differ from the physical path the runtime's peer identity actually reports
// and compares rules against. The operator decides whether to grant the
// physical path instead, or move the program outside AppData; this never
// blocks the grant. profile is a parameter, in the shape of
// bootstrap.CurrentProfileView, so a test can supply a fixed view without
// touching this process's own AppData.
func warnVirtualizedProgram(diagnostics io.Writer, program, localAppData string, profile func() (bootstrap.ProfileView, error)) {
	if localAppData == "" || program == "" || !withinFold(program, localAppData) {
		return
	}
	view, err := profile()
	if err != nil || !view.Virtualized {
		return
	}
	//unchecked: a warning with no return value to report a write failure through
	fmt.Fprintf(diagnostics, "rights grant: warning: --program %s is under this process's own AppData, and this process sees package %s's private copy of it. The runtime may report a different physical path for a program under a packaged app. Grant the path openabstractions status reports as this program's identity.\n", program, view.Family)
}

// withinFold reports whether p lies inside folder, comparing case-
// insensitively and on either separator, as Windows paths compare: p and
// folder always name Windows paths (a --program value and %LOCALAPPDATA%),
// whether this process itself runs on Windows or, as in its tests, on Linux,
// so the comparison cleans by hand instead of through path/filepath, which
// would only recognize the host's own separator.
func withinFold(p, folder string) bool {
	p = path.Clean(strings.ReplaceAll(p, `\`, "/"))
	folder = path.Clean(strings.ReplaceAll(folder, `\`, "/"))
	return len(p) > len(folder)+1 && strings.EqualFold(p[:len(folder)], folder) && p[len(folder)] == '/'
}

func rightsActionEdit(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command, action, revision string, retire, asJSON bool, output io.Writer) error {
	operator, err := resolveOperator(machine, source, call, command)
	if err != nil {
		return err
	}
	if revision == "" {
		ctx, cancel := call()
		page, err := operator.ListPolicyContext(ctx, "", 1)
		cancel()
		if err != nil {
			return notResolved(command, source, err)
		}
		if page.Outcome != rightswire.PolicyPageOutcomePage {
			return rightsPrint(output, asJSON, rightsReply{Command: command, Outcome: page.Outcome.String()}, rightsOperatorRefusal(command, page.Outcome.String(), "listing the policy revision"))
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

func resolveOperator(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command string) (*rights.Operator, error) {
	ctx, cancel := call()
	defer cancel()
	operator, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		return nil, notResolved(command, source, err)
	}
	return operator, nil
}

func rightsList(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command, cursor string, limit int64, long bool, self string, asJSON bool, output io.Writer) error {
	operator, err := resolveOperator(machine, source, call, command)
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
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "RULE\tACCOUNT\tPROGRAM\tACTION\tRESOURCE")
		for _, r := range page.Rules {
			account := r.Subject.Account
			if !long && account == self {
				account = "this account"
			}
			program := r.Subject.Program
			if !long {
				program = shortProgramName(program)
			}
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", ruleWord(r.Permit), account, program, r.Action, r.Resource)
		}
		if !page.Complete {
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "more rules: --cursor %s\n", page.Next)
		}
		if err := table.Flush(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "policy revision %s; %d catalogue actions\n", page.Revision, len(page.Catalog)); err != nil {
			return err
		}
	}
	if page.Outcome != rightswire.PolicyPageOutcomePage {
		// The single returned error carries this outcome; nothing prints it a
		// second time, undecorated, to output first.
		return rightsOperatorRefusal(command, page.Outcome.String(), "")
	}
	return nil
}

// rightsOperatorRefusal is refusal for a command the runtime gates to its own
// operator programs: list, grant, revoke, read, decide, register-action and
// retire-action all resolve the policy through the caller's own resolved
// operator identity, and answer forbidden the same way when the caller is
// not one. A forbidden outcome replaces reason with the one fix: no rule can
// grant operator standing, since the runtime's operator programs are fixed by
// installation (serve/runtime_credentials.go operatorPrograms), not by a
// policy rule. Any other outcome keeps its own reason unchanged.
func rightsOperatorRefusal(command, outcome, reason string) error {
	if outcome != "forbidden" {
		return refusal(command, outcome, reason)
	}
	return refusal(command, outcome, "run this from the installed openabstractions.exe, its windowless sibling or the Abstraction Panel; those hold operator standing by installation, and no rights grant can extend it to another program")
}

// shortProgramName is a rule's --program as rights list prints it by default:
// the file name only, both separators recognized so a policy-stored Windows
// path reads the same on any host. --long prints the full path this shortens.
func shortProgramName(program string) string {
	cleaned := strings.ReplaceAll(program, `\`, "/")
	if i := strings.LastIndexByte(cleaned, '/'); i >= 0 {
		return cleaned[i+1:]
	}
	return program
}

func ruleWord(permit bool) string {
	if permit {
		return "permit"
	}
	return "deny"
}

func rightsEdit(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command string, rule rightsRule, revision string, permit bool, why string, ttl time.Duration, revoke, asJSON bool, output io.Writer) error {
	operator, err := resolveOperator(machine, source, call, command)
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
			return rightsPrint(output, asJSON, rightsReply{Command: command, Outcome: page.Outcome.String()}, rightsOperatorRefusal(command, page.Outcome.String(), "listing the policy revision"))
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
func readRule(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command string, rule rightsRule) (rightsReply, error) {
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
		return reply, notResolved(command, source, err)
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

func rightsRead(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command string, rule rightsRule, asJSON bool, output io.Writer) error {
	reply, err := readRule(machine, source, call, command, rule)
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
			//unchecked: this auxiliary detail line's write failure must not override the outcome this call already means to report
			fmt.Fprintln(output, text)
		}
	}
	return failure
}

func rightsDecide(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command string, rule rightsRule, asJSON bool, output io.Writer) error {
	ctx, cancel := call()
	decisions, err := machine.ResolveRights(ctx, local)
	cancel()
	if err != nil {
		return notResolved(command, source, err)
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
		if onRecord, err := readRule(machine, source, call, "rights read", rule); err == nil {
			reply.RuleOnFile = &onRecord
		}
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
