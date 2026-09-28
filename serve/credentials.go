package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	credclient "github.com/openabstractions/abstraction-credentials/go/client"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

const credentialsUsage = `Usage: openabstractions credentials <command>

Commands:
  add <name> --target HOST --for CONSUMER [options]
                       register a secret by name in this account
  list                 names, kinds, states, targets and consumers; never values
  rotate <name>        replace the secret of a registered name
  revoke <name>        destroy the secret and keep a tombstone
  allow <name> --program PATH [--deny]
                       let a program's work apply the credential, or deny it
  audit                the retained registration and application events
  backend file|secret-service --state-dir DIR
                       Linux: choose the runtime's store for that state directory

add options:
  --target HOST        a host the secret may be sent to, covering its
                       subdomains; repeatable, at least one
  --for CONSUMER       a service contract that may apply it, repeatable:
                       abstraction.download/http-execution@1,
                       abstraction.model/resolver@1, abstraction.inference/chat@1,
                       abstraction.inference/embed@1, abstraction.router/router@1
  --kind bearer|header bearer sends Authorization: Bearer <secret> (default)
  --header NAME        the header kind header sends
  --expires TIME       RFC 3339 expiry; rotate clears or replaces it
  --use-by PATH        also allow this program to apply it; repeatable

The secret is read from stdin with --from-stdin, or from the terminal with
echo off. It is never accepted as an argument: a command line is readable by
every process of this user. add, rotate and revoke print no secret, and no
command can read one back.

Every command accepts --endpoint, --timeout and --json. The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the installed runtime.

Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (conflict, invalid, forbidden, no_secure_store, ...),
4 unavailable (repeat later).
`

var credentialNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func validCredentialName(name string) bool { return credentialNamePattern.MatchString(name) }

// repeated collects a repeatable flag.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

// secretSource reads a secret from stdin or a terminal prompt with echo off.
type secretSource struct {
	stdin     io.Reader
	fromStdin bool
	prompt    func(io.Writer) ([]byte, error)
}

func (s secretSource) read(diagnostics io.Writer, command, usage string) ([]byte, error) {
	if s.fromStdin {
		data, err := io.ReadAll(io.LimitReader(s.stdin, credentials.MaxSecretBytes+3))
		if err != nil {
			return nil, err
		}
		data = trimLineEnd(data)
		if len(data) == 0 || len(data) > credentials.MaxSecretBytes {
			return nil, flagMistake(diagnostics, command, usage, fmt.Sprintf("the secret on stdin must be 1..%d bytes", credentials.MaxSecretBytes))
		}
		return data, nil
	}
	if s.prompt == nil {
		return nil, flagMistake(diagnostics, command, usage, "no terminal to prompt on; pass --from-stdin and write the secret to stdin")
	}
	data, err := s.prompt(diagnostics)
	if err != nil {
		return nil, flagMistake(diagnostics, command, usage, fmt.Sprintf("no terminal to prompt on (%v); pass --from-stdin", err))
	}
	data = trimLineEnd(data)
	if len(data) == 0 {
		return nil, flagMistake(diagnostics, command, usage, "empty secret")
	}
	return data, nil
}

// trimLineEnd drops one trailing line ending, the one a shell or prompt adds.
func trimLineEnd(data []byte) []byte {
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
		if n := len(data); n > 0 && data[n-1] == '\r' {
			data = data[:n-1]
		}
	}
	return data
}

func zeroSecret(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func credentialsCommand(args []string, stdin io.Reader, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, credentialsUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "credentials: a command is required", "openabstractions credentials --help")
	}
	switch args[0] {
	case "add", "list", "rotate", "revoke", "allow", "audit", "backend":
	default:
		return commandMistake(diagnostics, fmt.Sprintf("credentials: no command called %q", args[0]), "openabstractions credentials --help")
	}
	command := "credentials " + args[0]
	if containsHelp(args[1:]) {
		_, err := io.WriteString(output, credentialsUsage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	var targets, consumers, useBy repeated
	flags.Var(&targets, "target", "host the secret may be sent to; repeatable")
	flags.Var(&consumers, "for", "service contract that may apply it; repeatable")
	flags.Var(&useBy, "use-by", "program allowed to apply it; repeatable")
	kind := flags.String("kind", "bearer", "bearer or header")
	header := flags.String("header", "", "header name for kind header")
	expires := flags.String("expires", "", "RFC 3339 expiry")
	fromStdin := flags.Bool("from-stdin", false, "read the secret from stdin")
	program := flags.String("program", "", "program path for allow")
	deny := flags.Bool("deny", false, "with allow, record an explicit deny")
	stateDir := flags.String("state-dir", "", "with backend, the runtime's state directory")
	positional, err := parsePositional(flags, args[1:])
	if err != nil {
		return badFlag(flags, diagnostics, command, credentialsUsage, args[1:], err)
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, credentialsUsage, "--timeout must not be negative")
	}
	wantsName := args[0] != "list" && args[0] != "audit"
	if wantsName != (len(positional) == 1) || len(positional) > 1 {
		if wantsName {
			return flagMistake(diagnostics, command, credentialsUsage, "name exactly one credential")
		}
		return flagMistake(diagnostics, command, credentialsUsage, "takes no arguments")
	}
	name := ""
	if wantsName {
		name = positional[0]
		if args[0] != "backend" && !validCredentialName(name) {
			return flagMistake(diagnostics, command, credentialsUsage, fmt.Sprintf("%q must be 1-64 characters from A-Z, a-z, 0-9, _, - and .", name))
		}
	}
	secrets := secretSource{stdin: stdin, fromStdin: *fromStdin, prompt: promptSecret}
	switch args[0] {
	case "backend":
		return credentialsBackend(output, diagnostics, name, *stateDir)
	case "add":
		reg := cwire.Registration{Name: name, Kind: *kind, Header: *header, Scope: cwire.Scope{Targets: targets, Consumers: consumers}}
		if len(targets) == 0 || len(consumers) == 0 {
			return flagMistake(diagnostics, command, credentialsUsage, "give at least one --target and one --for")
		}
		if *expires != "" {
			at, err := time.Parse(time.RFC3339, *expires)
			if err != nil {
				return flagMistake(diagnostics, command, credentialsUsage, fmt.Sprintf("--expires %q is not RFC 3339", *expires))
			}
			reg.Expires = at.UTC().Format("2006-01-02T15:04:05.000000Z")
		}
		programs, err := absolutePrograms(diagnostics, command, credentialsUsage, useBy)
		if err != nil {
			return err
		}
		secret, err := secrets.read(diagnostics, command, credentialsUsage)
		if err != nil {
			return err
		}
		defer zeroSecret(secret)
		reg.Secret = secret
		holder, w, err := resolveHolder(options, diagnostics, command)
		if err != nil {
			return err
		}
		defer w.stop()
		call, done := w.call()
		result, err := holder.Store(call, "", reg)
		done()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		if result.Outcome != cwire.StoreOutcomeStored {
			return refusal(command, result.Outcome.String(), "")
		}
		for _, p := range programs {
			if err := setApplyRule(options, w, diagnostics, command, name, p, true); err != nil {
				return err
			}
		}
		return credentialReport(output, options.asJSON, result.Current, fmt.Sprintf("stored %s revision %s", name, result.Revision))
	case "rotate", "revoke":
		holder, w, err := resolveHolder(options, diagnostics, command)
		if err != nil {
			return err
		}
		defer w.stop()
		current, err := findCredential(w, holder, command, name)
		if err != nil {
			return err
		}
		if args[0] == "revoke" {
			call, done := w.call()
			result, err := holder.Revoke(call, current.Revision, name)
			done()
			if err != nil {
				return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
			}
			if result.Outcome != cwire.RevokeOutcomeRevoked {
				return refusal(command, result.Outcome.String(), "")
			}
			return credentialReport(output, options.asJSON, result, fmt.Sprintf("revoked %s revision %s", name, result.Revision))
		}
		rotation := cwire.Rotation{Name: name}
		if *expires != "" {
			at, err := time.Parse(time.RFC3339, *expires)
			if err != nil {
				return flagMistake(diagnostics, command, credentialsUsage, fmt.Sprintf("--expires %q is not RFC 3339", *expires))
			}
			rotation.Expires = at.UTC().Format("2006-01-02T15:04:05.000000Z")
		}
		secret, err := secrets.read(diagnostics, command, credentialsUsage)
		if err != nil {
			return err
		}
		defer zeroSecret(secret)
		rotation.Secret = secret
		call, done := w.call()
		result, err := holder.Rotate(call, current.Revision, rotation)
		done()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		if result.Outcome != cwire.RotateOutcomeRotated {
			return refusal(command, result.Outcome.String(), "")
		}
		return credentialReport(output, options.asJSON, result.Current, fmt.Sprintf("rotated %s revision %s", name, result.Revision))
	case "allow":
		programs, err := absolutePrograms(diagnostics, command, credentialsUsage, repeated{*program})
		if err != nil || *program == "" {
			return flagMistake(diagnostics, command, credentialsUsage, "--program must name an absolute executable path")
		}
		w := newWaiting(options.budget)
		defer w.stop()
		if err := setApplyRule(options, w, diagnostics, command, name, programs[0], !*deny); err != nil {
			return err
		}
		verb := "allowed"
		if *deny {
			verb = "denied"
		}
		return credentialReport(output, options.asJSON, map[string]string{"name": name, "program": programs[0], "rule": verb}, fmt.Sprintf("%s %s for %s", verb, name, programs[0]))
	case "list":
		holder, w, err := resolveHolder(options, diagnostics, command)
		if err != nil {
			return err
		}
		defer w.stop()
		records, limits, err := listCredentials(w, holder, command)
		if err != nil {
			return err
		}
		if options.asJSON {
			return writeJSON(output, map[string]any{"limits": limits, "records": records})
		}
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "NAME\tKIND\tSTATE\tREVISION\tTARGETS\tCONSUMERS")
		for _, r := range records {
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Kind, r.State, r.Revision, strings.Join(r.Scope.Targets, ","), strings.Join(r.Scope.Consumers, ","))
		}
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "store: %s\n", limits.SecureStore)
		return table.Flush()
	case "audit":
		holder, w, err := resolveHolder(options, diagnostics, command)
		if err != nil {
			return err
		}
		defer w.stop()
		var entries []cwire.AuditEntry
		cursor := ""
		for {
			call, done := w.call()
			page, err := holder.Audit(call, cursor, 256)
			done()
			if err != nil {
				return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
			}
			if page.Outcome != cwire.PageOutcomePage {
				return refusal(command, page.Outcome.String(), "")
			}
			entries = append(entries, page.Entries...)
			if page.AtEnd || page.Next == cursor {
				break
			}
			cursor = page.Next
		}
		if options.asJSON {
			return writeJSON(output, map[string]any{"entries": entries})
		}
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "TIME\tEVENT\tNAME\tOUTCOME\tCONSUMER\tTARGET\tPROGRAM")
		for _, e := range entries {
			program := ""
			if e.Subject != nil {
				program = e.Subject.Program
			}
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Time, e.Event, e.Name, e.Outcome, e.Consumer, e.Target, program)
		}
		return table.Flush()
	}
	return nil
}

func resolveHolder(options serviceOptions, diagnostics io.Writer, command string) (*credclient.Holder, *waiting, error) {
	w := newWaiting(options.budget)
	call, done := w.call()
	machine, source := options.machine(diagnostics, command)
	holder, err := machine.ResolveCredentials(call, client.Requirements{})
	done()
	if err != nil {
		w.stop()
		return nil, nil, notResolved(command, source, err)
	}
	return holder, w, nil
}

func listCredentials(w *waiting, holder *credclient.Holder, command string) ([]cwire.Metadata, cwire.Limits, error) {
	var records []cwire.Metadata
	var limits cwire.Limits
	cursor := ""
	for {
		call, done := w.call()
		page, err := holder.List(call, cursor, 64)
		done()
		if err != nil {
			return nil, limits, &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		if page.Outcome != cwire.PageOutcomePage {
			return nil, limits, refusal(command, page.Outcome.String(), "")
		}
		records, limits = append(records, page.Records...), page.Limits
		if page.Complete {
			return records, limits, nil
		}
		cursor = page.Next
	}
}

func findCredential(w *waiting, holder *credclient.Holder, command, name string) (cwire.Metadata, error) {
	records, _, err := listCredentials(w, holder, command)
	if err != nil {
		return cwire.Metadata{}, err
	}
	for _, r := range records {
		if r.Name == name && r.State != cwire.StateRevoked {
			return r, nil
		}
	}
	return cwire.Metadata{}, refusal(command, "unknown", name)
}

func absolutePrograms(diagnostics io.Writer, command, usage string, paths []string) ([]string, error) {
	var programs []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, flagMistake(diagnostics, command, usage, fmt.Sprintf("program %q must be an absolute executable path", p))
		}
		programs = append(programs, filepath.Clean(p))
	}
	return programs, nil
}

// setApplyRule records the exact abstraction.credentials/apply rule for program
// on credential:<name> through the runtime's rights operator service.
func setApplyRule(options serviceOptions, w *waiting, diagnostics io.Writer, command, name, program string, permit bool) error {
	account, err := user.Current()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: account unavailable: %w", command, err)}
	}
	call, done := w.call()
	machine, source := options.machine(diagnostics, command)
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	rule := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: program}, Action: credentials.ActionApply, Resource: credentials.ResourceFor(name), Permit: permit}
	for attempt := 0; attempt < 2; attempt++ {
		call, done := w.call()
		page, err := operator.ListPolicyContext(call, "", 1)
		done()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: rights: %w", command, err)}
		}
		if page.Outcome != rights.PolicyPageOutcomePage {
			return refusal(command, page.Outcome.String(), "rights operator")
		}
		call, done = w.call()
		edit, err := operator.SetRuleContext(call, page.Revision, rule)
		done()
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: rights: %w", command, err)}
		}
		if edit.Outcome == rights.PolicyEditOutcomeConflict && attempt == 0 {
			continue
		}
		if edit.Outcome != rights.PolicyEditOutcomeApplied {
			return refusal(command, edit.Outcome.String(), "rights rule")
		}
		return nil
	}
	return nil
}

func credentialsBackend(output, diagnostics io.Writer, choice, state string) error {
	const command = "credentials backend"
	if choice != credentials.StoreFile && choice != "file" && choice != credentials.StoreSecretService {
		return flagMistake(diagnostics, command, credentialsUsage, "choose file or secret-service")
	}
	if state == "" {
		// The default state is the account's; a view private to a package must not write it.
		if err := refuseVirtualizedProfile(command, bootstrap.CurrentProfileView); err != nil {
			return err
		}
		var err error
		if state, err = managedJobRoot(""); err != nil {
			return err
		}
		state = filepath.Dir(state)
	}
	if !filepath.IsAbs(state) {
		return flagMistake(diagnostics, command, credentialsUsage, "--state-dir must be absolute")
	}
	if choice == "file" {
		choice = credentials.StoreFile
	}
	dir := filepath.Join(state, "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, credentialsBackendFile), []byte(choice+"\n"), 0o600); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "credentials store for %s: %s (restart the runtime to apply)\n", state, choice)
	return err
}

func credentialReport(output io.Writer, asJSON bool, value any, text string) error {
	if asJSON {
		return writeJSON(output, value)
	}
	_, err := fmt.Fprintln(output, text)
	return err
}
