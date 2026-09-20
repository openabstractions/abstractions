package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
)

const inferenceUsage = `Usage: openabstractions inference <command>

Commands:
  host list            configured hosts, their state and each credential's spend today
  host add <name> --base URL [--wire KIND --credential NAME] [--profiles LIST] [ceilings]
                       add a host at the listed configuration revision; also
                       writes the complete rule on host:<name> for the command
                       line and the Panel, and for a hosted host the runtime's
                       apply rule on its credential, which listings need
  host remove <name>   remove a host
  key issue --for PROGRAM [--credential NAME]
                       mint the gateway window key for one program and print it
                       once; set it as that program's API key
  key revoke --for PROGRAM
                       destroy the program's key; the window refuses it next
  key list             every local key: program, credential, state; never a key
  audit                retained inference decisions from both routes, with the
                       rung of proof each caller was bound at
  gateway status       the gateway window setting, and whether the window
                       listens now
  gateway on [--address 127.0.0.1:PORT]
                       open the window on the running runtime and keep it open
                       across restarts (default address: the recorded one, or
                       127.0.0.1:8793)
  gateway off          close the window and every window connection, and keep
                       it closed across restarts

host add:
  --base URL           a local runtime's address (http://127.0.0.1:11434), or a
                       hosted API root (https://openrouter.ai/api/v1); plain
                       http only on loopback
  --wire KIND          makes the host hosted: openai-compatible,
                       anthropic-messages or <owner>/<name>@<n>; without it the
                       name is a local kind: ollama, lmstudio or lemonade
  --credential NAME    the registered credential the service applies to it
  --profiles LIST      comma-separated profiles the host serves: chat, embed,
                       transcription, speech, image, live or <owner>/<name>@<n>
                       (default: all six for openai-compatible and the local
                       kinds, chat for anthropic-messages and other wires);
                       the router picks only a host serving the requested
                       profile
  --tokens-per-day N   the credential's daily token ceiling
  --micros-per-day N   the credential's daily spend ceiling in currency millionths

key issue:
  --for PROGRAM        the absolute executable path the window requires of the
                       peer presenting the key (python.exe is every script it runs)
  --credential NAME    let the window's requests for this program spend under a
                       hosted credential; without it they stay local-only

audit:
  --cursor N           start at sequence N (default: the oldest retained)
  --limit N            print at most N entries (default: all)

The window is closed by default and listens on 127.0.0.1 only. gateway on opens
it on an installed runtime; openabstractions serve runtime --gateway
127.0.0.1:<port> opens it for one foreground runtime without changing the
setting. A program with a key still needs abstraction.inference/complete on the host,
and abstraction.credentials/apply on a hosted credential:
  openabstractions rights grant --for inference --program PROGRAM --host NAME

Every command accepts --endpoint, --timeout and --json.

Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict, invalid, no_secure_store), 4 unavailable,
6 unknown.
`

func inferenceCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || isHelp(args[0]) {
		_, err := io.WriteString(output, inferenceUsage)
		return err
	}
	command := "inference " + args[0]
	rest := args[1:]
	switch args[0] {
	case "host", "key", "gateway":
		if len(rest) == 0 || isHelp(rest[0]) {
			_, err := io.WriteString(output, inferenceUsage)
			return err
		}
		command += " " + rest[0]
		switch command {
		case "inference host list", "inference host add", "inference host remove", "inference key issue", "inference key revoke", "inference key list",
			"inference gateway status", "inference gateway on", "inference gateway off":
		default:
			return &exitError{exitUsage, fmt.Errorf("%s: no such command; run openabstractions inference --help", command)}
		}
		rest = rest[1:]
	case "audit":
	default:
		return &exitError{exitUsage, fmt.Errorf("inference: no command called %q; run openabstractions inference --help", args[0])}
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		if _, err := fmt.Fprint(diagnostics, inferenceUsage); err == nil {
			flags.PrintDefaults()
		}
	}
	var options serviceOptions
	options.bind(flags)
	base := flags.String("base", "", "host address")
	wireKind := flags.String("wire", "", "hosted wire kind")
	credential := flags.String("credential", "", "credential name")
	tokens := flags.Int64("tokens-per-day", 0, "daily token ceiling")
	micros := flags.Int64("micros-per-day", 0, "daily spend ceiling in currency millionths")
	profiles := flags.String("profiles", "", "comma-separated profiles the host serves")
	program := flags.String("for", "", "program path")
	cursor := flags.Int64("cursor", 0, "audit start sequence")
	limit := flags.Int("limit", 0, "audit entries to print")
	address := flags.String("address", "", "gateway window address, 127.0.0.1:PORT")
	positional, err := parsePositional(flags, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &exitError{exitUsage, fmt.Errorf("%s: %w", command, err)}
	}
	wantsName := command == "inference host add" || command == "inference host remove"
	if wantsName != (len(positional) == 1) || len(positional) > 1 {
		if wantsName {
			return &exitError{exitUsage, fmt.Errorf("%s: name exactly one host", command)}
		}
		return &exitError{exitUsage, fmt.Errorf("%s: takes no arguments", command)}
	}
	if options.budget < 0 || *tokens < 0 || *micros < 0 || *cursor < 0 || *limit < 0 {
		return &exitError{exitUsage, fmt.Errorf("%s: --timeout, ceilings, --cursor and --limit must not be negative", command)}
	}
	if *address != "" && (command != "inference gateway on" || gatewayAddress(*address) != nil) {
		return &exitError{exitUsage, fmt.Errorf("%s: --address is for gateway on and names 127.0.0.1:PORT", command)}
	}
	needsProgram := command == "inference key issue" || command == "inference key revoke"
	if needsProgram && !identity.ValidSubjectProgram(*program) {
		return &exitError{exitUsage, fmt.Errorf("%s: --for must name an absolute executable path or msix:<package family>", command)}
	}
	w := newWaiting(options.budget)
	defer w.stop()
	call, done := w.call()
	operator, err := options.machine().ResolveInferenceOperator(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, err)
	}
	transport := func(err error) error { return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)} }
	switch command {
	case "inference host list":
		call, done := w.call()
		list, err := operator.Hosts(call)
		done()
		if err != nil {
			return transport(err)
		}
		if list.Outcome != iclient.ListOutcomePage {
			return refusal(command, list.Outcome.String(), "")
		}
		if options.asJSON {
			return writeJSON(output, list)
		}
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "NAME\tCLASS\tKIND\tBASE\tCREDENTIAL\tPROFILES\tDECLARED BY\tUP\tSPEND TODAY\tCEILING")
		for _, h := range list.Hosts {
			class, spend, ceiling := "local", "", ""
			if h.Entry.Hosted {
				class = "hosted"
			}
			if h.Spend != nil {
				spend = fmt.Sprintf("%d tokens, %d micros", h.Spend.Tokens, h.Spend.Micros)
			}
			if c := h.Entry.Ceiling; c != nil {
				ceiling = fmt.Sprintf("%d tokens, %d micros", c.TokensPerDay, c.MicrosPerDay)
			}
			up := "up"
			if !h.Up {
				up = "down: " + h.Why
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.Entry.Name, class, h.Entry.Kind, h.Entry.Base, h.Entry.Credential,
				strings.Join(h.Entry.Profiles, ","), h.Entry.DeclaredBy, up, spend, ceiling)
		}
		fmt.Fprintf(table, "revision: %s\n", list.Revision)
		return table.Flush()
	case "inference host add", "inference host remove":
		call, done := w.call()
		list, err := operator.Hosts(call)
		done()
		if err != nil {
			return transport(err)
		}
		if list.Outcome != iclient.ListOutcomePage {
			return refusal(command, list.Outcome.String(), "reading the configuration revision")
		}
		name := positional[0]
		call, done = w.call()
		var change iclient.HostChange
		if command == "inference host add" {
			entry := iclient.HostEntry{Name: name, Hosted: *wireKind != "", Kind: *wireKind, Base: *base, Credential: *credential, Profiles: splitList(*profiles)}
			if !entry.Hosted {
				entry.Kind = name
			}
			if *tokens > 0 || *micros > 0 {
				entry.Ceiling = &iclient.CeilingLimit{TokensPerDay: *tokens, MicrosPerDay: *micros}
			}
			change, err = operator.AddHost(call, list.Revision, entry)
		} else {
			change, err = operator.RemoveHost(call, list.Revision, name)
		}
		done()
		if err != nil {
			return transport(err)
		}
		if change.Outcome != iclient.EditOutcomeApplied {
			return refusal(command, change.Outcome.String(), change.Reason)
		}
		text := fmt.Sprintf("%s %s; configuration revision %s", strings.TrimPrefix(command, "inference host "), name, change.Revision)
		if change.Reason != "" {
			text += "\nnot every rule was written: " + change.Reason
		}
		return credentialReport(output, options.asJSON, change, text)
	case "inference gateway status", "inference gateway on", "inference gateway off":
		call, done := w.call()
		state, err := operator.Gateway(call)
		done()
		if err != nil {
			return transport(err)
		}
		if state.Outcome != iclient.ListOutcomePage {
			return refusal(command, state.Outcome.String(), "")
		}
		if command != "inference gateway status" {
			call, done = w.call()
			change, err := operator.SetGateway(call, state.Revision, command == "inference gateway on", *address)
			done()
			if err != nil {
				return transport(err)
			}
			if change.Outcome != iclient.EditOutcomeApplied {
				return refusal(command, change.Outcome.String(), change.Reason)
			}
			call, done = w.call()
			state, err = operator.Gateway(call)
			done()
			if err != nil {
				return transport(err)
			}
			if state.Outcome != iclient.ListOutcomePage {
				return refusal(command, state.Outcome.String(), "")
			}
		}
		if options.asJSON {
			return writeJSON(output, state)
		}
		setting := "off"
		if state.Open {
			setting = "on at " + state.Address
		}
		now := "closed"
		if state.Listening {
			now = fmt.Sprintf("listening on http://%s/v1 (OpenAI) and http://%s (Anthropic)", state.ListeningAddress, state.ListeningAddress)
		} else if state.Why != "" {
			now = "closed: " + state.Why
		}
		_, err = fmt.Fprintf(output, "setting: %s\nwindow:  %s\nrevision: %s\n", setting, now, state.Revision)
		return err
	case "inference key issue":
		call, done := w.call()
		issued, err := operator.IssueKey(call, filepath.Clean(*program), *credential)
		done()
		if err != nil {
			return transport(err)
		}
		if issued.Outcome != iclient.EditOutcomeApplied {
			return refusal(command, issued.Outcome.String(), issued.Reason)
		}
		if options.asJSON {
			return writeJSON(output, issued)
		}
		_, err = fmt.Fprintf(output, "%s\n\nThis key is shown once. Give it to %s as its API key (OPENAI_API_KEY, ANTHROPIC_API_KEY or its own setting), with the window's base URL as its base URL.\n", issued.Key, issued.Record.Program)
		return err
	case "inference key revoke":
		call, done := w.call()
		revoked, err := operator.RevokeKey(call, filepath.Clean(*program))
		done()
		if err != nil {
			return transport(err)
		}
		if revoked.Outcome != iclient.EditOutcomeApplied {
			return refusal(command, revoked.Outcome.String(), "")
		}
		return credentialReport(output, options.asJSON, revoked, "revoked the key of "+*program)
	case "inference key list":
		call, done := w.call()
		list, err := operator.Keys(call)
		done()
		if err != nil {
			return transport(err)
		}
		if list.Outcome != iclient.ListOutcomePage {
			return refusal(command, list.Outcome.String(), "")
		}
		if options.asJSON {
			return writeJSON(output, list)
		}
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "PROGRAM\tSTATE\tCREDENTIAL\tISSUED\tISSUED BY")
		for _, k := range list.Keys {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", k.Program, k.State, k.Credential, time.UnixMilli(k.IssuedUnixMs).UTC().Format(time.RFC3339), k.IssuedBy)
		}
		return table.Flush()
	case "inference audit":
		entries := []iclient.AuditEntry{}
		next := *cursor
		for *limit == 0 || len(entries) < *limit {
			call, done := w.call()
			page, err := operator.Audit(call, next, 256)
			done()
			if err != nil {
				return transport(err)
			}
			if page.Outcome == iclient.AuditOutcomeGap && next == *cursor {
				next = page.Next
				continue
			}
			if page.Outcome != iclient.AuditOutcomePage {
				return refusal(command, page.Outcome.String(), "")
			}
			entries = append(entries, page.Entries...)
			next = page.Next
			if page.AtEnd || len(page.Entries) == 0 {
				break
			}
		}
		if *limit > 0 && len(entries) > *limit {
			entries = entries[:*limit]
		}
		if options.asJSON {
			return writeJSON(output, map[string]any{"entries": entries, "next": next})
		}
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "SEQ\tTIME\tROUTE\tRUNG\tPROGRAM\tHOST\tMODEL\tCREDENTIAL\tOUTCOME\tREASON\tTOKENS IN/OUT")
		for _, e := range entries {
			fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d/%d\n", e.Sequence, time.UnixMilli(e.UnixMs).UTC().Format(time.RFC3339), e.Route, e.Rung,
				e.Program, e.Host, e.Model, e.Credential, e.Outcome, e.Reason, e.TokensIn, e.TokensOut)
		}
		return table.Flush()
	}
	return nil
}
